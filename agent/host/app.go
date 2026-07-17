package host

import (
	"bufio"
	"context"
	"errors"
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
	ui       Surface
	failover *agent.FailoverProvider

	injection *agent.InjectionPolicy
	triggers  *agent.TriggerPolicy
	approval  *agent.TieredApproval
	fanIn     *gocurrent.FanIn[agent.IncomingEvent]
	streams   []*eventsclient.StreamCall
	tasksMu   sync.Mutex
	bgTasks   map[string]*client.BackgroundTask
	subsMu    sync.Mutex
	subs      map[string]*subscription
	metaTools bool
	turnMu    sync.Mutex
	eventStop context.CancelFunc

	// store and runID are the persistence seam (WithRunStore): runID is
	// the run turns append to, created lazily on the first persisted
	// turn or set by AttachRun / Resume / Fork. Guarded by turnMu.
	store agent.RunStore
	runID string

	// toolResultStore backs tool-result offloading when Config.Offload
	// is set; nil when offloading is off.
	toolResultStore agent.ToolResultStore

	// connections + providerSwitch are the runtime provider-switch seam
	// (Config.Connections): the Runner holds the switch, /provider swaps
	// its underlying. Both nil when Connections is not configured.
	connections    *ConnectionRegistry
	providerSwitch *providerSwitch

	// commands is the slash-command registry every surface dispatches
	// through (Dispatch / Commands). Built in NewApp.
	commands *CommandRegistry
}

// AppOption customizes construction. The provider override exists so tests
// (and offline demos) can run the full app on a scripted StubProvider.
type AppOption func(*appOptions)

type appOptions struct {
	provider        agent.Provider
	ui              agent.ElicitationUI
	tp              core.TracerProvider
	logger          *slog.Logger
	store           agent.RunStore
	toolResultStore agent.ToolResultStore
	providerBuilder ProviderBuilder
	surface         Surface
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

	var surface Surface = newRenderer(out)
	if o.surface != nil {
		surface = o.surface
	}
	elicUI := o.ui
	if elicUI == nil {
		elicUI = terminalElicitationUI(bufio.NewReader(in), out)
	}
	coord := agent.NewElicitationCoordinator(elicUI)

	multi := agent.NewMultiSource()
	app := &App{cfg: cfg, sources: multi, ui: surface, bgTasks: map[string]*client.BackgroundTask{}, subs: map[string]*subscription{}, store: o.store}
	// The approval "ask" prompt rides the same FIFO seam as elicitation, so a
	// gated tool call never stacks a dialog against a concurrent elicitation.
	ask := func(ctx context.Context, req agent.ApprovalRequest) (bool, error) {
		return coord.Confirm(ctx, approvalPrompt(req))
	}
	app.approval = cfg.Approval.buildApproval(ask)

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
			agent.WithTaskStatusHook(func(dt *core.DetailedTask) { app.ui.Emit(UIEvent{Kind: UITaskStatus, TaskStatus: dt}) }),
			agent.WithTaskGrace(cfg.taskGrace()),
			agent.WithTaskDetachHook(app.onTaskDetach),
			agent.WithTaskCompletionHook(func(bt *client.BackgroundTask) { app.onTaskComplete(sc.ID, bt) }))
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
		block, err := loadSkillsBlock(app.clients[i], sc.ID, surface, o.tp)
		if err != nil {
			app.Close()
			return nil, err
		}
		if block != "" {
			instructions += "\n\n" + block
		}
	}

	provider := o.provider
	if provider == nil && cfg.Connections != nil {
		reg, err := NewConnectionRegistry(cfg.Connections, o.providerBuilder)
		if err != nil {
			app.Close()
			return nil, err
		}
		active, err := reg.ActiveProvider()
		if err != nil {
			app.Close()
			return nil, err
		}
		app.connections = reg
		app.providerSwitch = newProviderSwitch(active)
		provider = app.providerSwitch
	}
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

	app.metaTools = cfg.MetaTools || len(cfg.Triggers) > 0
	for _, sc := range cfg.Servers {
		app.metaTools = app.metaTools || len(sc.Events) > 0
	}
	if app.metaTools {
		if err := app.registerMetaTools(multi); err != nil {
			app.Close()
			return nil, err
		}
	}

	// Offloading wraps the whole aggregate, so one read_tool_result and
	// one store cover every server's tools. The stub it substitutes is a
	// normal ToolResult, so the transcript and persisted log see exactly
	// what the model saw.
	var runnerTools agent.ToolSource = multi
	if cfg.Offload != nil {
		store := o.toolResultStore
		if store == nil {
			store = agent.NewInMemoryToolResultStore()
		}
		app.toolResultStore = store
		runnerTools = agent.NewOffloadingSource(multi, store, cfg.Offload.toAgent())
	}

	runnerCfg := agent.RunnerConfig{
		Provider:       provider,
		Tools:          runnerTools,
		Instructions:   instructions,
		MaxSteps:       cfg.MaxSteps,
		TracerProvider: o.tp,
	}
	// Only set Approval when a policy exists: a nil *TieredApproval boxed in
	// the interface would read as non-nil and gate every call to a deny.
	if app.approval != nil {
		runnerCfg.Approval = app.approval
	}
	runner, err := agent.NewRunner(runnerCfg)
	if err != nil {
		app.Close()
		return nil, err
	}
	app.runner = runner
	app.registerBuiltinCommands()

	if app.metaTools {
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
	for _, s := range a.listSubscriptions() {
		s.call.Stop()
	}
	if a.fanIn != nil {
		a.fanIn.Stop()
	}
	a.tasksMu.Lock()
	for _, bt := range a.bgTasks {
		bt.Cancel()
	}
	a.tasksMu.Unlock()
	for _, c := range a.clients {
		c.Close()
	}
}

// RunTurn executes one user input, rendering events as they stream. History
// threads across turns; a cancelled or failed turn leaves history at its
// pre-turn state (and persists nothing) so the next attempt is clean.
func (a *App) RunTurn(ctx context.Context, input string) error {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	a.triggers.NotifyEngagement()
	a.drainInjectionLocked()
	userMsg := agent.Message{Role: agent.RoleUser, Text: input}
	a.history = append(a.history, userMsg)

	emit := func(e agent.Event) { a.ui.Emit(UIEvent{Kind: UIRunnerEvent, RunnerEvent: e}) }
	var pe *PersistingEmit
	if a.store != nil {
		if err := a.ensureRunLocked(ctx); err != nil {
			a.history = a.history[:len(a.history)-1]
			a.ui.Emit(UIEvent{Kind: UITurnFailed, Err: err.Error()})
			return err
		}
		pe = NewPersistingEmit(a.store, a.runID, emit)
		emit = pe.Emit
	}

	result, err := a.runner.Run(ctx, a.history, emit)
	if err != nil {
		a.history = a.history[:len(a.history)-1]
		a.ui.Emit(UIEvent{Kind: UITurnFailed, Err: err.Error()})
		return err
	}
	a.history = append(a.history, result.Messages...)
	if pe != nil {
		a.persistTurnLocked(ctx, append([]agent.Message{userMsg}, result.Messages...), pe)
	}
	a.ui.Emit(UIEvent{Kind: UITurnDone, Result: result})
	return nil
}

// Tools renders the current merged tool list (the /tools command).
func (a *App) Tools(ctx context.Context) error {
	defs, err := a.sources.Tools(ctx)
	if err != nil {
		return err
	}
	a.ui.Emit(UIEvent{Kind: UICommand, Command: CmdResult{Kind: CmdTools, Tools: defs}})
	return nil
}


// Providers returns the configured connection names and the active one,
// or (nil, "") when no connection registry is configured. Structured so a
// surface (REPL, TUI, web) renders it however it likes.
func (a *App) Providers() (names []string, active string) {
	if a.connections == nil {
		return nil, ""
	}
	return a.connections.Names(), a.connections.Active()
}

// SwitchProvider makes name the active chat provider for subsequent
// turns. An unknown name or a build failure leaves the current provider
// in place (the error says which). The swap takes effect on the next
// model call; a turn already streaming finishes on its original provider.
func (a *App) SwitchProvider(name string) error {
	if a.connections == nil {
		return fmt.Errorf("host: no connections configured")
	}
	p, err := a.connections.SetActive(name)
	if err != nil {
		return err
	}
	a.providerSwitch.set(p)
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
	a.ui.Emit(UIEvent{Kind: UIPrompt})
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "":
		case strings.HasPrefix(line, "/"):
			res, err := a.Dispatch(ctx, line)
			if errors.Is(err, ErrUnknownCommand) {
				a.ui.Emit(UIEvent{Kind: UITurnFailed, Err: fmt.Sprintf("unknown command %q (try /tools, /provider, /session, /quit)", line)})
			} else if err != nil {
				a.ui.Emit(UIEvent{Kind: UITurnFailed, Err: err.Error()})
			} else {
				a.ui.Emit(UIEvent{Kind: UICommand, Command: res})
				if res.Quit {
					return nil
				}
			}
		default:
			tctx, cancel := turnCtx()
			a.RunTurn(tctx, line)
			cancel()
		}
		a.ui.Emit(UIEvent{Kind: UIPrompt})
	}
	return scanner.Err()
}
