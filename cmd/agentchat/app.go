package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
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
}

// AppOption customizes construction. The provider override exists so tests
// (and offline demos) can run the full app on a scripted StubProvider.
type AppOption func(*appOptions)

type appOptions struct {
	provider agent.Provider
	ui       agent.ElicitationUI
}

// WithProvider overrides the OpenAI-compatible provider built from config.
func WithProvider(p agent.Provider) AppOption {
	return func(o *appOptions) { o.provider = p }
}

// WithElicitationUI overrides the terminal elicitation UI (tests script it).
func WithElicitationUI(ui agent.ElicitationUI) AppOption {
	return func(o *appOptions) { o.ui = ui }
}

// NewApp connects every configured server and assembles the agent. The
// returned App owns the client connections; call Close when done.
func NewApp(cfg *Config, out io.Writer, in io.Reader, opts ...AppOption) (*App, error) {
	var o appOptions
	for _, opt := range opts {
		opt(&o)
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
			client.WithElicitationHandler(coord.Handler()),
			client.WithToolsListChangedHandler(multi.Invalidate),
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
			agent.WithInputHandler(client.DefaultInputHandler(c)))
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

	runner, err := agent.NewRunner(agent.RunnerConfig{
		Provider:     provider,
		Tools:        multi,
		Instructions: cfg.Instructions,
		MaxSteps:     cfg.MaxSteps,
	})
	if err != nil {
		app.Close()
		return nil, err
	}
	app.runner = runner
	return app, nil
}

// Close disconnects every server.
func (a *App) Close() {
	for _, c := range a.clients {
		c.Close()
	}
}

// RunTurn executes one user input, rendering events as they stream. History
// threads across turns; a cancelled or failed turn leaves history at its
// pre-turn state so the next attempt is clean.
func (a *App) RunTurn(ctx context.Context, input string) error {
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
		default:
			tctx, cancel := turnCtx()
			a.RunTurn(tctx, line)
			cancel()
		}
		a.renderer.prompt()
	}
	return scanner.Err()
}
