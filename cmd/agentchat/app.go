package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"

	gocurrent "github.com/panyam/gocurrent"
	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
)

// App is agentchat's testable core: everything the binary does minus flag
// parsing and signal handling. Construct with NewApp, converse with RunTurn
// (one input, one rendered turn) or REPL (interactive loop).
type App struct {
	cfg      *Config
	runner   *agent.Runner
	sources  *agent.MultiSource
	clients  []*client.Client
	history  []agent.Message
	renderer *renderer
	failover *agent.FailoverProvider

	injection *agent.InjectionPolicy
	triggers  *agent.TriggerPolicy
	fanIn     *gocurrent.FanIn[agent.IncomingEvent]
	streams   []*eventsclient.StreamCall
	turnMu    sync.Mutex
	eventStop context.CancelFunc
}

// AppOption customizes construction. The provider override exists so tests
// (and offline demos) can run the full app on a scripted StubProvider.
type AppOption func(*appOptions)

type appOptions struct {
	provider agent.Provider
	ui       agent.ElicitationUI
	tp       core.TracerProvider
	logger   *slog.Logger
}

// WithProvider overrides the OpenAI-compatible provider built from config.
func WithProvider(p agent.Provider) AppOption {
	return func(o *appOptions) { o.provider = p }
}

// WithElicitationUI overrides the terminal elicitation UI (tests script it).
func WithElicitationUI(ui agent.ElicitationUI) AppOption {
	return func(o *appOptions) { o.ui = ui }
}

// WithTracerProvider opts every layer into SEP 414 spans: agent turn/step/
// tool spans, client dispatch spans (stitched as children), and skills
// activation spans. Nil means noop.
func WithTracerProvider(tp core.TracerProvider) AppOption {
	return func(o *appOptions) { o.tp = tp }
}

// WithLogger sets the operational slog logger (failover transitions, future
// policy events). The transcript renderer is UI, not logging, and never
// routes here. Nil discards.
func WithLogger(l *slog.Logger) AppOption {
	return func(o *appOptions) { o.logger = l }
}

// NewApp connects every configured server and assembles the agent. The
// returned App owns the client connections; call Close when done.
func NewApp(cfg *Config, out io.Writer, in io.Reader, opts ...AppOption) (*App, error) {
	var o appOptions
	for _, opt := range opts {
		opt(&o)
	}
	if o.tp == nil {
		o.tp = core.NoopTracerProvider{}
	}
	if o.logger == nil {
		o.logger = slog.New(slog.DiscardHandler)
	}

	rend := newRenderer(out)
	ui := o.ui
	if ui == nil {
		ui = terminalElicitationUI(bufio.NewReader(in), out)
	}
	coord := agent.NewElicitationCoordinator(ui)

	multi := agent.NewMultiSource()
	app := &App{cfg: cfg, sources: multi, renderer: rend}

	for _, sc := range cfg.Servers {
		copts := []client.ClientOption{
			client.WithGetSSEStream(),
			client.WithTasksExtension(),
			client.WithElicitationHandler(coord.Handler()),
			client.WithToolsListChangedHandler(multi.Invalidate),
			client.WithTracerProvider(o.tp),
		}
		authOpt, err := authOption(sc)
		if err != nil {
			app.Close()
			return nil, err
		}
		if authOpt != nil {
			copts = append(copts, authOpt)
		}
		c := client.NewClient(sc.URL, core.ClientInfo{Name: "agentchat", Version: "0.1"}, copts...)
		if err := c.Connect(); err != nil {
			app.Close()
			return nil, fmt.Errorf("agentchat: connect %s (%s): %w", sc.ID, sc.URL, err)
		}
		app.clients = append(app.clients, c)

		var src agent.ToolSource = agent.NewClientSource(c,
			agent.WithInputHandler(client.DefaultInputHandler(c)),
			agent.WithTaskStatusHook(func(dt *core.DetailedTask) { rend.taskStatus(dt) }))
		if len(sc.Allow) > 0 {
			allowed := make(map[string]bool, len(sc.Allow))
			for _, name := range sc.Allow {
				allowed[name] = true
			}
			src = agent.NewFilterSource(src, func(d core.ToolDef) bool { return allowed[d.Name] })
		}
		if err := multi.Add(sc.ID, src); err != nil {
			app.Close()
			return nil, err
		}
	}

	instructions := cfg.Instructions
	for i, sc := range cfg.Servers {
		if sc.Skills != nil && !*sc.Skills {
			continue
		}
		block, err := loadSkillsBlock(app.clients[i], sc.ID, rend, o.tp)
		if err != nil {
			app.Close()
			return nil, err
		}
		if block != "" {
			instructions += "\n\n" + block
		}
	}

	provider := o.provider
	if provider == nil {
		var err error
		provider, err = agent.NewOpenAIProvider(agent.OpenAIConfig{
			BaseURL: cfg.Model.BaseURL,
			Model:   cfg.Model.Model,
			APIKey:  cfg.APIKey(),
		})
		if err != nil {
			app.Close()
			return nil, err
		}
	}
	if b := cfg.Model.Backup; b != nil {
		backup, err := agent.NewOpenAIProvider(agent.OpenAIConfig{
			BaseURL: b.BaseURL,
			Model:   b.Model,
			APIKey:  os.Getenv(b.APIKeyEnv),
		})
		if err != nil {
			app.Close()
			return nil, fmt.Errorf("agentchat: backup model: %w", err)
		}
		fo, err := agent.NewFailoverProvider(agent.FailoverConfig{
			Primary: provider,
			Backup:  backup,
			Logger:  o.logger,
		})
		if err != nil {
			app.Close()
			return nil, err
		}
		provider = fo
		app.failover = fo
	}

	app.injection = agent.NewInjectionPolicy(agent.InjectionConfig{Hints: hintOverrides(cfg)})
	app.triggers = agent.NewTriggerPolicy(agent.TriggerPolicyConfig{
		Bindings: buildTriggerBindings(cfg.Triggers),
		Logger:   o.logger,
	})

	runner, err := agent.NewRunner(agent.RunnerConfig{
		Provider:       provider,
		Tools:          multi,
		Instructions:   instructions,
		MaxSteps:       cfg.MaxSteps,
		TracerProvider: o.tp,
	})
	if err != nil {
		app.Close()
		return nil, err
	}
	app.runner = runner

	hasEvents := false
	for _, sc := range cfg.Servers {
		hasEvents = hasEvents || len(sc.Events) > 0
	}
	if hasEvents {
		evCtx, cancel := context.WithCancel(context.Background())
		app.eventStop = cancel
		app.fanIn = gocurrent.NewFanIn[agent.IncomingEvent]()
		if err := app.startEventStreams(evCtx); err != nil {
			cancel()
			app.Close()
			return nil, err
		}
		go app.consumeEvents(evCtx)
	}
	return app, nil
}

// Close stops event streams and disconnects every server.
func (a *App) Close() {
	if a.eventStop != nil {
		a.eventStop()
	}
	for _, s := range a.streams {
		s.Stop()
	}
	if a.fanIn != nil {
		a.fanIn.Stop()
	}
	for _, c := range a.clients {
		c.Close()
	}
}

// RunTurn executes one user input, rendering events as they stream. History
// threads across turns; a cancelled or failed turn leaves history at its
// pre-turn state so the next attempt is clean.
func (a *App) RunTurn(ctx context.Context, input string) error {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	a.triggers.NotifyEngagement()
	a.drainInjectionLocked()
	a.history = append(a.history, agent.Message{Role: agent.RoleUser, Text: input})
	result, err := a.runner.Run(ctx, a.history, a.renderer.handle)
	if err != nil {
		a.history = a.history[:len(a.history)-1]
		a.renderer.turnFailed(err)
		return err
	}
	a.history = append(a.history, result.Messages...)
	a.renderer.turnDone(result)
	return nil
}

// Tools renders the current merged tool list (the /tools command).
func (a *App) Tools(ctx context.Context) error {
	defs, err := a.sources.Tools(ctx)
	if err != nil {
		return err
	}
	a.renderer.toolList(defs)
	return nil
}

// REPL runs the interactive loop until EOF or /quit. turnCtx wraps each
// turn so the caller's signal handling can cancel the in-flight turn
// without killing the loop.
func (a *App) REPL(ctx context.Context, in io.Reader, turnCtx func() (context.Context, context.CancelFunc)) error {
	if turnCtx == nil {
		turnCtx = func() (context.Context, context.CancelFunc) { return context.WithCancel(ctx) }
	}
	scanner := bufio.NewScanner(in)
	a.renderer.prompt()
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "":
		case line == "/quit" || line == "/exit":
			return nil
		case line == "/tools":
			if err := a.Tools(ctx); err != nil {
				a.renderer.turnFailed(err)
			}
		case line == "/history":
			a.renderer.history(a.history)
		case line == "/health":
			a.renderer.health(a.failover)
		default:
			tctx, cancel := turnCtx()
			a.RunTurn(tctx, line)
			cancel()
		}
		a.renderer.prompt()
	}
	return scanner.Err()
}
