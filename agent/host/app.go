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
	cfg       *Config
	runner    *agent.Runner
	sources   *agent.MultiSource
	clients   []*client.Client
	history   []agent.Message
	observers []Observer
	replOut   io.Writer // terminal REPL draws its own prompt here
	failover  *agent.FailoverProvider

	// group owns the MCP server connection lifecycle (async connect, per-server
	// state, backoff reconnect); its observer registers a server's tools when it
	// becomes ready. serverTools mirrors the server sources for sub-agent
	// personas. See docs/AGENT_SERVER_STATE.md.
	group       *client.Group
	serverTools *agent.MultiSource

	// oauthSources holds the interactive (oauth) token source per server id, so
	// LoginServer can force a fresh browser login. Only oauth-typed servers have
	// an entry; its presence is what CanLogin reports.
	oauthSources map[string]loginSource

	// tp is the tracer provider, held so the ready-observer can load a late
	// server's skills with the same instrumentation as the boot path.
	tp core.TracerProvider

	// Skills load in the ready-observer (late servers too), so their prompt
	// blocks and load_skill catalog are shared mutable state read live: the
	// dynamic system prompt (buildInstructions -> RunnerConfig.InstructionsFunc)
	// and the load_skill tool. serverOrder keeps assembly deterministic.
	skillsMu     sync.Mutex
	skillBlocks  map[string]string        // serverID -> system-prompt block (eager bodies or catalog listing)
	skillCatalog map[string][]catalogSkill // serverID -> catalog entries for load_skill
	serverOrder  []string
	loadSkillReg bool // load_skill registered once, lazily, on the first catalog skill

	injection *agent.EventInjectionPolicy
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
	emitMu    sync.Mutex // serializes emit so concurrent server-connect events don't race the renderer
	evCtx     context.Context // subscription lifetime ctx; the ready-observer subscribes late servers on it
	eventStop context.CancelFunc

	// store and runID are the persistence seam (WithRunStore): runID is
	// the run turns append to, created lazily on the first persisted
	// turn or set by AttachRun / Resume / Fork. Guarded by turnMu.
	store agent.RunStore
	runID string

	// sessionsMu guards sessionsCursor, the paging position the /sessions
	// picker remembers so "/sessions more" advances (the store cursor is
	// opaque, so the host holds where the last page ended).
	sessionsMu     sync.Mutex
	sessionsCursor string

	// toolResultStore backs tool-result offloading when Config.Offload
	// is set; nil when offloading is off.
	toolResultStore agent.ToolResultStore

	// memory is the working-memory source when Config.Memory is set; nil
	// when memory is off. Held so RunTurn can inject its summary and the
	// /memory command can list it.
	memory *agent.MemorySource

	// connections + providerSwitch are the runtime provider-switch seam
	// (Config.Connections): the Runner holds the switch, /provider swaps
	// its underlying. Both nil when Connections is not configured.
	connections    *ConnectionRegistry
	providerSwitch *providerSwitch

	// commands is the slash-command registry every surface dispatches
	// through (Dispatch / Commands). Built in NewApp.
	commands *CommandRegistry

	// overlay persists mutable config picks (active connection, approval
	// mode) back to a local-overlay file when WithConfigOverlay is set; nil
	// means runtime changes are session-only.
	overlay *configOverlay
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
	memoryStore     agent.MemoryStore
	providerBuilder ProviderBuilder
	observers       []Observer
	configPath      string
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

// WithConfigOverlay enables runtime-config persistence: mutable picks changed
// via slash commands (the /provider active connection and the /approve mode)
// are written back to the sibling local-overlay file derived from configPath
// (kitchen-sink.json -> kitchen-sink.local.json), so they survive a restart
// once the launcher loads the config with LoadConfigWithOverlay. configPath is
// the base config path, not the overlay path. Without this option the picks are
// session-only. A persist failure degrades to a warning, never a failed
// command (mirrors the RunStore persistence contract).
func WithConfigOverlay(configPath string) AppOption {
	return func(o *appOptions) { o.configPath = configPath }
}

// registerServerTools wraps a now-ready server's client as a ToolSource (with
// its per-server allow filter) and adds it to the aggregate and, when
// sub-agents are configured, the sub-agent view. It is called from the
// connection Group's observer on StateReady — possibly concurrently for
// several servers — so it relies on MultiSource.Add being safe for concurrent
// use. A registration failure degrades to a warning, never a crash.
func (a *App) registerServerTools(sc ServerConfig, c *client.Client) {
	if c == nil {
		return
	}
	var src agent.ToolSource = agent.NewClientSource(c,
		agent.WithInputHandler(client.DefaultInputHandler(c)),
		agent.WithTaskStatusHook(func(dt *core.DetailedTask) { a.emit(HostEvent{Kind: HostTaskStatus, TaskStatus: dt}) }),
		agent.WithTaskGrace(a.cfg.taskGrace()),
		agent.WithTaskDetachHook(a.onTaskDetach),
		agent.WithTaskCompletionHook(func(bt *client.BackgroundTask) { a.onTaskComplete(sc.ID, bt) }))
	if len(sc.Allow) > 0 {
		allowed := make(map[string]bool, len(sc.Allow))
		for _, name := range sc.Allow {
			allowed[name] = true
		}
		src = agent.NewFilterSource(src, func(d core.ToolDef) bool { return allowed[d.Name] })
	}
	// Outermost: map an unreachable-server transport error on any call to a
	// non-fatal ErrNotAvailableNow miss (docs/AGENT_SERVER_STATE.md).
	src = newAvailabilitySource(src, sc.ID)
	if err := a.sources.Add(sc.ID, src); err != nil {
		a.emit(HostEvent{Kind: HostSessionWarn, Err: fmt.Sprintf("register tools for %s: %v", sc.ID, err)})
		return
	}
	if a.serverTools != nil {
		_ = a.serverTools.Add(sc.ID, src)
	}
	// A late-added source changes the tool list; clear any cached aggregate so
	// the next turn sees the new tools.
	a.sources.Invalidate()
}

// errString renders an error for a HostEvent's Err field, empty for nil.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
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
	// A nil writer is a valid "discard output" request: the default renderer and
	// the terminal elicitation UI both write to it, and servers now emit
	// state-change events during construction, so a nil here would panic.
	if out == nil {
		out = io.Discard
	}

	observers := o.observers
	if len(observers) == 0 {
		observers = []Observer{newRenderer(out, envColorEnabled())}
	}
	elicUI := o.ui
	if elicUI == nil {
		elicUI = terminalElicitationUI(bufio.NewReader(in), out)
	}
	coord := agent.NewElicitationCoordinator(elicUI)

	multi := agent.NewMultiSource()
	app := &App{cfg: cfg, sources: multi, observers: observers, replOut: out, bgTasks: map[string]*client.BackgroundTask{}, subs: map[string]*subscription{}, store: o.store,
		tp: o.tp, skillBlocks: map[string]string{}, skillCatalog: map[string][]catalogSkill{}, oauthSources: map[string]loginSource{}}
	for _, sc := range cfg.Servers {
		app.serverOrder = append(app.serverOrder, sc.ID)
	}
	if o.configPath != "" {
		app.overlay = &configOverlay{path: overlayPathFor(o.configPath)}
	}
	// The approval "ask" prompt rides the same FIFO seam as elicitation, so a
	// gated tool call never stacks a dialog against a concurrent elicitation.
	ask := func(ctx context.Context, req agent.ApprovalRequest) (bool, error) {
		return coord.Confirm(ctx, approvalPrompt(req))
	}
	app.approval = cfg.Approval.buildApproval(ask)

	// serverTools mirrors the server sources only — no meta-tools, no
	// sub-agents — so a sub-agent persona gets a filtered view of the servers
	// without seeing the meta-tools or the other personas (which would let it
	// recurse). Built only when sub-agents are configured.
	if len(cfg.SubAgents) > 0 {
		app.serverTools = agent.NewMultiSource()
	}

	// Build one client per server and hand it to the connection Group, which
	// connects them asynchronously and calls the observer as each transitions.
	// A server's tools register the moment it becomes ready (registerServerTools),
	// so a server that is down at boot — or comes up late — wires its tools in
	// without blocking or failing the agent. Required servers block boot below.
	serverByID := make(map[string]ServerConfig, len(cfg.Servers))
	clientByID := make(map[string]*client.Client, len(cfg.Servers))
	onState := func(ch client.StateChange) {
		app.emit(HostEvent{Kind: HostServerStateChanged, ServerID: ch.ID, ServerState: ch.State.String(), Err: errString(ch.Err)})
		if ch.State == client.StateReady {
			app.registerServerTools(serverByID[ch.ID], clientByID[ch.ID])
			app.onServerSkills(serverByID[ch.ID], clientByID[ch.ID])
			app.onServerEvents(serverByID[ch.ID])
		}
	}
	app.group = client.NewGroup(client.WithObserver(onState))
	for _, sc := range cfg.Servers {
		copts := []client.ClientOption{
			client.WithGetSSEStream(),
			client.WithTasksExtension(),
			client.WithElicitationHandler(coord.Handler()),
			client.WithToolsListChangedHandler(multi.Invalidate),
			client.WithTracerProvider(o.tp),
		}
		authOpt, loginSrc, err := authOption(sc)
		if err != nil {
			app.Close()
			return nil, err
		}
		if authOpt != nil {
			copts = append(copts, authOpt)
		}
		if loginSrc != nil {
			app.oauthSources[sc.ID] = loginSrc
		}
		c := client.NewClient(sc.URL, core.ClientInfo{Name: "agentchat", Version: "0.1"}, copts...)
		app.clients = append(app.clients, c)
		serverByID[sc.ID] = sc
		clientByID[sc.ID] = c
		app.group.Add(sc.ID, c, sc.Required)
	}
	// The event infrastructure (fanIn + subscription ctx) must exist before the
	// servers connect, so the ready-observer can subscribe a server's event
	// streams the moment it connects — late servers included. The consumer
	// goroutine starts later, once the Runner exists; streams buffer until then.
	app.metaTools = cfg.MetaTools || len(cfg.Triggers) > 0
	for _, sc := range cfg.Servers {
		app.metaTools = app.metaTools || len(sc.Events) > 0
	}
	if app.metaTools {
		app.evCtx, app.eventStop = context.WithCancel(context.Background())
		app.fanIn = gocurrent.NewFanIn[agent.IncomingEvent]()
	}

	app.group.Start(context.Background())
	// Block only for the servers marked required; the rest keep connecting in
	// the background and register via the observer as they become ready.
	if err := app.group.WaitRequired(context.Background()); err != nil {
		app.Close()
		return nil, fmt.Errorf("agentchat: %w", err)
	}
	// Wait for the initial connect round to settle (bounded) so reachable
	// servers are ready and their tools + skills registered before the first
	// turn; a down server fails fast and does not block, and a slow one wires
	// in later via the observer. Skills (eager blocks + the load_skill catalog)
	// are loaded in the ready-observer (onServerSkills) into shared state that
	// the dynamic system prompt (buildInstructions) and load_skill read live, so
	// a server that connects after boot contributes its skills on the next turn.
	_ = app.group.WaitSettled(context.Background())

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

	app.injection = agent.NewEventInjectionPolicy(agent.EventInjectionConfig{Hints: hintOverrides(cfg)})
	app.triggers = agent.NewTriggerPolicy(agent.TriggerPolicyConfig{
		Bindings: buildTriggerBindings(cfg.Triggers),
		Logger:   o.logger,
	})

	if app.metaTools {
		if err := app.registerMetaTools(multi); err != nil {
			app.Close()
			return nil, err
		}
	}

	// Working memory is a leaf source added to the aggregate, so its
	// remember/recall/forget tools sit alongside the server tools (and are
	// covered by offloading below like any other source).
	if cfg.Memory != nil {
		if err := app.registerMemory(multi, o.memoryStore); err != nil {
			app.Close()
			return nil, err
		}
	}

	// Sub-agent personas are AgentSources added to the aggregate: the main
	// agent delegates to them as tools. Each runs on the shared provider over
	// a filtered view of serverTools (built above), so it never sees the
	// meta-tools or its sibling personas.
	if len(cfg.SubAgents) > 0 {
		if err := app.registerSubAgents(multi, app.serverTools, provider, o.tp); err != nil {
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
		Provider: provider,
		Tools:    runnerTools,
		// Dynamic system prompt: cfg.Instructions plus the eager/catalog blocks
		// of the currently-connected servers, recomputed each turn so a
		// late-connecting server's skills appear without a restart.
		InstructionsFunc: app.buildInstructions,
		MaxSteps:         cfg.MaxSteps,
		TracerProvider:   o.tp,
	}
	// Only set Approval when a policy exists: a nil *TieredApproval boxed in
	// the interface would read as non-nil and gate every call to a deny.
	if app.approval != nil {
		runnerCfg.Approval = app.approval
	}
	if cfg.Compaction != nil {
		compactor, err := cfg.Compaction.build(provider)
		if err != nil {
			app.Close()
			return nil, err
		}
		runnerCfg.Compactor = compactor
	}
	runner, err := agent.NewRunner(runnerCfg)
	if err != nil {
		app.Close()
		return nil, err
	}
	app.runner = runner
	app.registerBuiltinCommands()

	// The fanIn + subscription ctx were built before the servers connected, and
	// the ready-observer has been subscribing each server's event streams; now
	// that the Runner exists, start the consumer that drains them into the
	// injection/trigger policies.
	if app.metaTools && app.fanIn != nil {
		go app.consumeEvents(app.evCtx)
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
	// The Group owns the server connections' lifecycle: closing it stops the
	// connect/retry goroutines and closes the member clients. Fall back to
	// closing clients directly only if the Group was never built (an early
	// construction error).
	if a.group != nil {
		a.group.Close()
	} else {
		for _, c := range a.clients {
			c.Close()
		}
	}
}

// ServerTools returns the tools exposed by a single connected MCP server, in
// that server's own (unqualified) naming — the data behind the /mcp overlay's
// per-server tool view. found is false for an unknown or not-yet-ready server
// id (app state, not an error). Each server's source is registered in the
// aggregate under its own id, so looking it up by that id returns only that
// server's tools (respecting its Allow filter) and never the meta-tools or
// sub-agents registered under other ids.
func (a *App) ServerTools(ctx context.Context, id string) (defs []core.ToolDef, found bool, err error) {
	if a.sources == nil {
		return nil, false, nil
	}
	return a.sources.SourceTools(ctx, id)
}

// loginSource is the subset of an interactive OAuth token source the host needs
// to force a fresh login: dropping the cached token so the client's next
// connect re-runs the browser authorization-code flow. *extauth.OAuthTokenSource
// satisfies it.
type loginSource interface{ Invalidate() }

// canLogin reports whether an interactive (oauth) auth type is configured for
// the server, so a surface knows when to offer the login action.
func (a *App) canLogin(id string) bool { return a.oauthSources[id] != nil }

// LoginServer forces a fresh interactive login for an oauth-configured server:
// it drops any cached token and reconnects, so the client's next connect runs
// the browser authorization-code flow again. A server without an interactive
// (oauth) auth type, or an unknown id, is a no-op — canLogin / the server
// status CanLogin flag tells a surface when to offer it.
func (a *App) LoginServer(id string) {
	src := a.oauthSources[id]
	if src == nil {
		return
	}
	src.Invalidate()
	a.ReconnectServer(id)
}

// ReconnectServer forces the named MCP server to attempt connecting again: it
// wakes a failed server that is sleeping between backoff retries and un-parks a
// needs-login server (the caller has since logged in). A ready or unknown
// server is a no-op. It is the data-only capability behind the /servers
// reconnect command and the interactive /mcp overlay's reconnect action; the
// surface renders the resulting state transitions from the group observer.
func (a *App) ReconnectServer(id string) {
	if a.group != nil {
		a.group.Reconnect(id)
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

	emit := func(e agent.Event) { a.emit(HostEvent{Kind: HostRunnerEvent, RunnerEvent: e}) }
	var pe *PersistingEmit
	if a.store != nil {
		if err := a.ensureRunLocked(ctx); err != nil {
			a.history = a.history[:len(a.history)-1]
			a.emit(HostEvent{Kind: HostTurnFailed, Err: err.Error()})
			return err
		}
		pe = NewPersistingEmit(a.store, a.runID, emit)
		emit = pe.Emit
	}

	// The memory summary is woven into the per-turn slice only, never into
	// a.history — see withMemorySummaryLocked.
	turnMsgs := a.withMemorySummaryLocked(ctx)
	result, err := a.runner.Run(ctx, turnMsgs, emit)
	if err != nil {
		a.history = a.history[:len(a.history)-1]
		a.emit(HostEvent{Kind: HostTurnFailed, Err: err.Error()})
		return err
	}
	a.history = append(a.history, result.Messages...)
	if pe != nil {
		a.persistTurnLocked(ctx, append([]agent.Message{userMsg}, result.Messages...), pe)
	}
	a.emit(HostEvent{Kind: HostTurnDone, Result: result})
	return nil
}

// Tools renders the current merged tool list (the /tools command).
func (a *App) Tools(ctx context.Context) error {
	defs, err := a.sources.Tools(ctx)
	if err != nil {
		return err
	}
	a.emit(HostEvent{Kind: HostCommandResult, Command: CmdResult{Kind: CmdTools, Tools: defs}})
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

// ModelLabel is the human-facing name of the active model for a status line:
// the active connection name when a ConnectionRegistry is configured, else the
// single-model identifier.
func (a *App) ModelLabel() string {
	if _, active := a.Providers(); active != "" {
		return active
	}
	return a.cfg.Model.Model
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

// emit fans a host event out to every registered observer. Fire-and-forget:
// observers render, trace, or push it; none reply. Serialized: the async server
// connections (Group observer), the turn goroutine, and event goroutines all
// emit, so the lock keeps a not-inherently-concurrent observer (the terminal
// renderer writing to one io.Writer) from racing.
func (a *App) emit(ev HostEvent) {
	a.emitMu.Lock()
	defer a.emitMu.Unlock()
	for _, o := range a.observers {
		o.On(ev)
	}
}

// promptREPL draws the terminal REPL's input marker. The prompt is
// surface chrome, not a host event — the REPL owns it because the REPL is
// the terminal surface's own loop.
func (a *App) promptREPL() {
	if a.replOut != nil {
		fmt.Fprint(a.replOut, "> ")
	}
}

// REPL runs the interactive loop until EOF or /quit. turnCtx wraps each
// turn so the caller's signal handling can cancel the in-flight turn
// without killing the loop.
func (a *App) REPL(ctx context.Context, in io.Reader, turnCtx func() (context.Context, context.CancelFunc)) error {
	if turnCtx == nil {
		turnCtx = func() (context.Context, context.CancelFunc) { return context.WithCancel(ctx) }
	}
	scanner := bufio.NewScanner(in)
	a.promptREPL()
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "":
		case strings.HasPrefix(line, "/"):
			res, err := a.Dispatch(ctx, line)
			if errors.Is(err, ErrUnknownCommand) {
				a.emit(HostEvent{Kind: HostTurnFailed, Err: fmt.Sprintf("unknown command %q (try /tools, /provider, /session, /quit)", line)})
			} else if err != nil {
				a.emit(HostEvent{Kind: HostTurnFailed, Err: err.Error()})
			} else {
				a.emit(HostEvent{Kind: HostCommandResult, Command: res})
				if res.Quit {
					return nil
				}
			}
		default:
			tctx, cancel := turnCtx()
			a.RunTurn(tctx, line)
			cancel()
		}
		a.promptREPL()
	}
	return scanner.Err()
}
