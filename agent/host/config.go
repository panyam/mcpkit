package host

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/client"
)

// Config is agentchat's JSON configuration. Secrets are never inlined:
// apiKeyEnv / authTokenEnv name environment variables to read at startup,
// following the same indirection convention the rest of the repo's example
// configs use.
type Config struct {
	// Model configures the OpenAI-compatible endpoint.
	Model ModelConfig `json:"model"`

	// Instructions is the system prompt. Optional.
	Instructions string `json:"instructions,omitempty"`

	// MaxSteps caps model calls per turn. Zero uses the agent default.
	MaxSteps int `json:"maxSteps,omitempty"`

	// MaxTreeSteps and MaxTreeTokens cap the AGGREGATE model steps / tokens
	// across a turn's whole sub-agent tree (parent + sub-agents + fan-out
	// members + handoff rounds), not just per-Runner MaxSteps. Zero means
	// unbounded. The safety rail for deep or chatty multi-agent trees.
	MaxTreeSteps  int `json:"maxTreeSteps,omitempty"`
	MaxTreeTokens int `json:"maxTreeTokens,omitempty"`

	// Interruptible opts the main agent's turn into breaking the fan-out join
	// barrier when a sub-agent raises a Signal mid-flight (issue 1167): the first
	// signal cancels the remaining in-flight calls and the model re-plans with
	// the partial results, instead of waiting for every sub-agent. Only useful
	// with signalling sub-agents (SubAgents + CanSignal). False (the default)
	// keeps the pure fan-out-then-join. Pairs with SignalPolicy: leave it unset
	// (inject) so the turn re-plans rather than aborts.
	Interruptible bool `json:"interruptible,omitempty"`

	// RunnerControl, when true, gives the main agent the runner-control
	// meta-tools (issue 1166): spawn_agent / await_agent / cancel_agent /
	// list_agents, over the configured sub-agent personas. The model can run a
	// persona in the background, get a handle, and await or cancel it — the
	// model-driven, handle-based counterpart to calling a persona as a blocking
	// tool. Only meaningful alongside SubAgents. False (the default) omits them.
	RunnerControl bool `json:"runnerControl,omitempty"`

	// SignalPolicy selects how the main agent reacts to an upward Signal a
	// signalling sub-agent raises (issue 1165), read at the dispatch join:
	//   - "" / "inject" — inject the signal as context and continue (default).
	//   - "abort_on_escalate" — end the turn when a child raises "escalate".
	// Either way the signal is injected so the main model sees it. Only meaningful
	// alongside a sub-agent with CanSignal.
	SignalPolicy string `json:"signalPolicy,omitempty"`

	// AllowPreempt lets a sub-agent's advisory "preempt" signal actually break
	// the interruptible barrier (cancel the other in-flight sub-agents) instead
	// of only being injected for the model to weigh (issue 1176). False (the
	// default) is safe: a preempt cannot cancel siblings, so a rogue or
	// prompt-injected sub-agent cannot unilaterally kill the parallel work — the
	// main model decides on re-plan. Only meaningful with Interruptible +
	// signalling sub-agents; "escalate" is unaffected (it always breaks).
	AllowPreempt bool `json:"allowPreempt,omitempty"`

	// Servers lists the MCP servers to connect.
	Servers []ServerConfig `json:"servers"`

	// Triggers lists proactive-turn bindings over the configured event
	// streams.
	Triggers []TriggerConfig `json:"triggers,omitempty"`

	// MetaTools exposes the async control-plane tools to the model
	// (subscribe_events, create_trigger, list_tasks, cancel_task, ...).
	// Auto-implied when any server has events or triggers configured.
	MetaTools bool `json:"metaTools,omitempty"`

	// TaskGraceSec is how long a task-backed tool call stays inline before
	// detaching to the background (completion arrives as injected context
	// and a transcript line; /tasks manages running tasks). Zero uses the
	// 10s default; negative disables detaching (wait inline forever).
	TaskGraceSec int `json:"taskGraceSec,omitempty"`

	// Approval configures the tool-call permission ladder. Nil means the
	// gate is off: every tool call the model makes runs (the pre-approval
	// behavior). Set it to gate calls behind a mode and per-tool rules,
	// with "ask" prompts routed through the terminal elicitation UI.
	Approval *ApprovalConfig `json:"approval,omitempty"`

	// Connections is a named registry of model connections with one
	// active. When set it supersedes Model for the chat provider and
	// enables runtime /provider switching; Model stays as the
	// single-connection quick-start path. See ConnectionsConfig.
	Connections *ConnectionsConfig `json:"connections,omitempty"`

	// Offload configures tool-result offloading. Nil means off: tool
	// results flow into the conversation verbatim. Set it to store
	// over-threshold results out of band and hand the model a compact
	// stub plus a read_tool_result tool. The backing ToolResultStore is
	// supplied by the surface via WithToolResultStore (in-memory when
	// omitted), the same split as WithRunStore.
	Offload *OffloadConfig `json:"offload,omitempty"`

	// Memory enables model-managed working memory: a remember/recall/forget
	// scratchpad the model reads and writes across turns. Nil means off.
	// The backing MemoryStore is supplied by the surface via
	// WithMemoryStore (in-memory when omitted), the same split as
	// WithRunStore and WithToolResultStore.
	Memory *MemoryConfig `json:"memory,omitempty"`

	// Compaction enables history compaction: when the conversation exceeds
	// MaxTokens, the head is summarized (by the chat provider) and a recent
	// tail is kept verbatim, before each turn. Nil means off — history is
	// sent verbatim. Lossy; complementary to Offload (lossless).
	Compaction *CompactionConfig `json:"compaction,omitempty"`

	// SubAgents declares specialist personas the main agent can delegate to
	// as tools (AgentSource). Each runs over the SAME provider and a filtered
	// view of the SAME server tools, with its own instructions — a persona,
	// not a separately-configured agent. Empty means no sub-agents.
	SubAgents []SubAgentConfig `json:"subAgents,omitempty"`

	// FanOut declares parallel-ensemble tools: each group is a single tool the
	// main agent calls once to broadcast a task to all its member personas
	// concurrently, receiving one aggregated result (agent.FanOutSource).
	// Empty means no fan-out tools.
	FanOut []FanOutGroupConfig `json:"fanOut,omitempty"`

	// Team, when set, drives the conversation as a handoff Team (agent.Team)
	// instead of the single main agent — control transfers between members and
	// persists across user turns. Mutually exclusive with SubAgents, FanOut, and
	// Memory (NewApp errors if combined). Nil means single-agent mode.
	Team *HostTeamConfig `json:"team,omitempty"`
}

// SubAgentConfig is one delegatable persona. The host builds it into an
// agent.AgentSource over a child Runner that shares the main provider and a
// FilterSource-narrowed view of the server tools.
type SubAgentConfig struct {
	// Name is the tool name the main agent calls to delegate. Required.
	Name string `json:"name"`

	// Description tells the main agent when to delegate to this persona.
	Description string `json:"description,omitempty"`

	// Instructions is the persona's system prompt. Empty means the sub-agent
	// is defined only by the task it is handed.
	Instructions string `json:"instructions,omitempty"`

	// Allow narrows which server tools this persona may use (by tool name).
	// Empty means all server tools.
	Allow []string `json:"allow,omitempty"`

	// MaxDepth caps sub-agent nesting for this persona. Zero uses the agent
	// default.
	MaxDepth int `json:"maxDepth,omitempty"`

	// InputSchema, when set, gives the persona a TYPED input tool instead of the
	// default {task} — the parent must pass arguments matching this JSON schema,
	// and the persona is seeded with them (structured input, 1033). Empty keeps
	// {task: string}.
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`

	// ResponseSchema, when set, makes the persona return a JSON value coerced to
	// this schema instead of free text (structured output, 1033) — the parent
	// gets typed output back. Empty returns text.
	ResponseSchema json.RawMessage `json:"responseSchema,omitempty"`

	// CanSignal, when true, gives the persona the signal_parent control tool
	// (issue 1165), so it can raise an upward Signal to the main agent —
	// "escalate" or "custom" (reporting only its own state; it has no knowledge
	// of sibling agents). The main agent reacts per Config.SignalPolicy
	// (inject-and-continue by default). False (the default) keeps the persona
	// server-tools-only. Ignored for async personas (a detached child has no
	// live parent dispatch to signal).
	CanSignal bool `json:"canSignal,omitempty"`

	// Async, when true, builds the persona as the spawn-and-continue Task form
	// (agent.AsyncAgentSource, 1035): the delegate tool acks immediately and the
	// child runs in the background, its result injected as context on a later
	// turn — for long-running subtasks the parent should not block on. False
	// (the default) is the blocking Tool form (answer this turn).
	Async bool `json:"async,omitempty"`
}

// FanOutGroupConfig declares one parallel-ensemble tool: the main agent calls
// Name once and the task is broadcast to every Member persona concurrently, the
// results aggregated in member order (agent.FanOutSource). Members are built
// exactly like SubAgents (shared provider, serverTools-only, own instructions)
// but are not also exposed as individual delegate tools — the group is one tool.
type FanOutGroupConfig struct {
	// Name is the tool the main agent calls to fan out. Required.
	Name string `json:"name"`

	// Description tells the main agent when to fan out to this group.
	Description string `json:"description,omitempty"`

	// Members are the personas the task is broadcast to. At least one required.
	Members []SubAgentConfig `json:"members,omitempty"`
}

// HostTeamConfig declares a handoff Team that drives the whole conversation
// instead of the single main agent (agent.Team). Control transfers between
// members and stays where it was transferred across user turns. Setting Team
// replaces the single-agent loop, so it may NOT be combined with SubAgents,
// FanOut, or Memory on the main agent (NewApp errors); team members are
// serverTools-only personas.
type HostTeamConfig struct {
	// Members are the agents. At least one; Start must name one.
	Members []TeamMemberConfig `json:"members,omitempty"`

	// Start is the agent that receives a conversation's first turn. Required.
	Start string `json:"start"`

	// MaxHandoffs caps transfers within one user turn. Zero uses the agent
	// default.
	MaxHandoffs int `json:"maxHandoffs,omitempty"`
}

// TeamMemberConfig is one Team agent: a persona (SubAgentConfig — shared
// provider, serverTools filtered by Allow, own instructions) plus the members
// it may hand off to.
type TeamMemberConfig struct {
	SubAgentConfig

	// HandoffTo lists the member names this agent may transfer control to. Each
	// becomes a transfer_to_<name> tool offered only to this agent. Empty means
	// a terminal agent that must answer rather than transfer.
	HandoffTo []string `json:"handoffTo,omitempty"`
}

// CompactionConfig is the host's view of history compaction; it maps to an
// agent.SummarizingCompactor over the chat provider. Its presence enables
// compaction.
type CompactionConfig struct {
	// MaxTokens is the budget (estimated) above which compaction fires.
	// Required (must be > 0); NewApp fails if it is not set.
	MaxTokens int `json:"maxTokens"`

	// KeepRecent is how many trailing messages stay verbatim. Zero uses the
	// agent default (6).
	KeepRecent int `json:"keepRecent,omitempty"`
}

// build maps the host config onto an agent.SummarizingCompactor using
// provider (the chat model) as the summarizer.
func (c *CompactionConfig) build(provider agent.Provider) (agent.Compactor, error) {
	return agent.NewSummarizingCompactor(agent.SummarizingConfig{
		Provider:   provider,
		MaxTokens:  c.MaxTokens,
		KeepRecent: c.KeepRecent,
	})
}

// MemoryConfig is the host's view of working memory. Its presence enables
// the MemorySource; the fields tune how memory reaches the turn.
type MemoryConfig struct {
	// InjectSummary, when true, prepends a summary of the current
	// scratchpad as a RoleSystem message before each turn, so the model
	// stays aware of what it saved without a recall call. It costs tokens
	// proportional to the injected slice, so it is opt-in; when false the
	// model still reaches memory through the recall tool on demand.
	InjectSummary bool `json:"injectSummary,omitempty"`

	// SummaryMaxItems bounds how many notes the injected summary carries,
	// keeping the newest. Zero means no item cap (the whole scratchpad).
	// Only meaningful with InjectSummary.
	SummaryMaxItems int `json:"summaryMaxItems,omitempty"`

	// SummaryMaxChars bounds the injected summary's rendered length (a cheap
	// token proxy), dropping the oldest kept notes until it fits. Zero means
	// no length cap. Only meaningful with InjectSummary.
	SummaryMaxChars int `json:"summaryMaxChars,omitempty"`

	// InjectRecall, when true, injects the notes RELEVANT to the current
	// user message as a RoleSystem message before each turn, so the model
	// "just knows" what matters without a recall call — the auto-push (RAG)
	// half of semantic recall. Complementary to InjectSummary (ambient,
	// recency-budgeted): a deployment usually picks one. Backend-agnostic,
	// but only useful with a relevance-ranking store (a semantic store);
	// over the substring default it injects notes literally containing the
	// query words.
	InjectRecall bool `json:"injectRecall,omitempty"`

	// RecallTopK caps how many relevant notes InjectRecall injects. Zero
	// uses the agent default (5).
	RecallTopK int `json:"recallTopK,omitempty"`

	// RecallMinScore drops recalled notes scoring below it (the poison guard
	// — a semantic store scores every note, so a floor keeps low-TopK recall
	// from injecting the least-irrelevant notes when nothing truly matches).
	// Zero means no floor.
	RecallMinScore float64 `json:"recallMinScore,omitempty"`

	// SessionScoped, when true, namespaces working memory by the active run
	// id, so each session (RunStore run) gets its own scratchpad and recall
	// never crosses sessions — a /sessions resume or fork carries its own
	// memory on a durable backend. Opt-in: the default is one shared
	// scratchpad across all sessions, which is often the point of persistent
	// memory. A no-op without a RunStore (the run id is always "", so every
	// turn shares the default namespace).
	SessionScoped bool `json:"sessionScoped,omitempty"`
}

// summaryOptions maps the host config onto the agent-layer budget.
func (c *MemoryConfig) summaryOptions() agent.SummaryOptions {
	return agent.SummaryOptions{MaxItems: c.SummaryMaxItems, MaxChars: c.SummaryMaxChars}
}

// recallOptions maps the host config onto the agent-layer recall budget.
func (c *MemoryConfig) recallOptions() agent.RecallOptions {
	return agent.RecallOptions{TopK: c.RecallTopK, MinScore: c.RecallMinScore}
}

// OffloadConfig is the host's view of tool-result offloading; it maps to
// an agent.OffloadConfig. Its presence enables offloading.
type OffloadConfig struct {
	// ThresholdBytes is the model-visible size at or above which a
	// successful result is offloaded. Zero uses the agent default
	// (4 KB).
	ThresholdBytes int `json:"thresholdBytes,omitempty"`

	// PreviewLen is how many leading characters the stub carries inline.
	// Zero uses the agent default.
	PreviewLen int `json:"previewLen,omitempty"`

	// PerTool overrides ThresholdBytes for named tools; a value <= 0
	// pins that tool to never offload (always inline).
	PerTool map[string]int `json:"perTool,omitempty"`
}

// toAgent maps the host config onto the agent-layer OffloadConfig.
func (c *OffloadConfig) toAgent() agent.OffloadConfig {
	return agent.OffloadConfig{
		Threshold:        c.ThresholdBytes,
		PreviewLen:       c.PreviewLen,
		PerToolThreshold: c.PerTool,
	}
}

// ApprovalConfig is the host's view of the agent approval ladder. It maps to
// an agent.TieredApproval whose "ask" outcome is presented through the same
// terminal UI as elicitation (via ElicitationCoordinator.Confirm).
type ApprovalConfig struct {
	// Mode is the default disposition for calls no rule covers: "ask"
	// (default), "read-only-auto" (auto-allow tools that declare the
	// readOnlyHint annotation, ask for the rest), or "allow" (run
	// everything, "yolo"). An unknown value falls back to "ask".
	Mode string `json:"mode,omitempty"`

	// Rules pins per-tool overrides that win over Mode: tool name ->
	// "allow" | "ask" | "deny". Unknown rule values are ignored.
	Rules map[string]string `json:"rules,omitempty"`

	// Remember caches a tool the user approved so later calls to it skip the
	// prompt for the life of the session.
	Remember bool `json:"remember,omitempty"`
}

// approvalPrompt renders the yes/no question shown when a tool call needs
// approval. The args are trimmed so a large payload does not flood the prompt.
func approvalPrompt(req agent.ApprovalRequest) string {
	args := strings.TrimSpace(string(req.Args.Raw()))
	if len(args) > 200 {
		args = args[:200] + "…"
	}
	if args == "" || args == "{}" {
		return fmt.Sprintf("Allow tool call %q?", req.ToolName)
	}
	return fmt.Sprintf("Allow tool call %q with %s?", req.ToolName, args)
}

// parseApprovalMode maps a config string to an agent mode, defaulting to ask.
func parseApprovalMode(s string) agent.ApprovalMode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "allow", "yolo", "auto", "full-auto":
		return agent.ModeAlwaysAllow
	case "read-only-auto", "readonly", "read-only", "auto-edit":
		return agent.ModeReadOnlyAuto
	default:
		return agent.ModeAlwaysAsk
	}
}

// approvalModeName is the inverse of parseApprovalMode for display.
func approvalModeName(m agent.ApprovalMode) string {
	switch m {
	case agent.ModeAlwaysAllow:
		return "allow"
	case agent.ModeReadOnlyAuto:
		return "read-only-auto"
	default:
		return "ask"
	}
}

// parseToolRule maps a config rule string to an agent rule; ok is false for an
// unrecognized value so the caller can skip it.
func parseToolRule(s string) (agent.ToolRule, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "allow":
		return agent.RuleAllow, true
	case "deny":
		return agent.RuleDeny, true
	case "ask":
		return agent.RuleAsk, true
	default:
		return 0, false
	}
}

// buildApproval turns the config into a live policy whose ask seam presents
// through the shared elicitation coordinator. Returns nil when no approval is
// configured (the Runner then runs every call).
func (c *ApprovalConfig) buildApproval(confirm agent.AskFunc) *agent.TieredApproval {
	if c == nil {
		return nil
	}
	opts := []agent.TieredOption{
		agent.WithDefaultMode(parseApprovalMode(c.Mode)),
		agent.WithAsk(confirm),
		agent.WithRememberApprovals(c.Remember),
	}
	for tool, r := range c.Rules {
		if rule, ok := parseToolRule(r); ok {
			opts = append(opts, agent.WithToolRule(tool, rule))
		}
	}
	return agent.NewTieredApproval(opts...)
}

// taskGrace resolves the configured grace window.
func (c *Config) taskGrace() time.Duration {
	switch {
	case c.TaskGraceSec < 0:
		return 0
	case c.TaskGraceSec == 0:
		return client.DefaultTaskGrace
	default:
		return time.Duration(c.TaskGraceSec) * time.Second
	}
}

// ModelConfig points at an OpenAI-compatible chat-completions endpoint.
type ModelConfig struct {
	BaseURL string `json:"baseUrl"`
	Model   string `json:"model"`
	// APIKeyEnv names the environment variable holding the bearer key.
	// Empty means unauthenticated (local servers).
	APIKeyEnv string `json:"apiKeyEnv,omitempty"`

	// Backup, when set, wraps the model in a FailoverProvider: a call
	// that fails cleanly on the primary retries here once, and the
	// primary is benched for a cooldown. See /health in the REPL.
	Backup *BackupModelConfig `json:"backup,omitempty"`
}

// BackupModelConfig is the failover endpoint (same shape as the primary,
// minus further nesting).
type BackupModelConfig struct {
	BaseURL   string `json:"baseUrl"`
	Model     string `json:"model"`
	APIKeyEnv string `json:"apiKeyEnv,omitempty"`
}

// ServerConfig is one MCP server connection.
type ServerConfig struct {
	// ID is the source identifier (used for collision-qualified tool
	// names). Must not contain underscores.
	ID string `json:"id"`

	// URL is the MCP endpoint (streamable HTTP).
	URL string `json:"url"`

	// Auth configures how to authenticate to this server. Nil means
	// unauthenticated.
	Auth *AuthConfig `json:"auth,omitempty"`

	// Allow, when non-empty, restricts this server to the named tools
	// (a FilterSource capability boundary, not a display preference).
	Allow []string `json:"allow,omitempty"`

	// Required makes boot block until this server is connected: NewApp waits
	// for it (up to the required-timeout) instead of returning while it is
	// still connecting. Use it for a server the agent is useless without.
	// Non-required servers (the default) connect in the background and never
	// block boot — the agent is usable immediately and their tools wire in as
	// they become ready. See docs/AGENT_SERVER_STATE.md.
	Required bool `json:"required,omitempty"`

	// Skills controls SEP-2640 skill loading for this server. Nil or true
	// auto-detects (servers without the capability are skipped silently);
	// false opts out even when the server advertises skills.
	Skills *bool `json:"skills,omitempty"`

	// SkillsMode selects how enabled skills enter context: "eager" (full
	// SKILL.md bodies in the system prompt), "catalog" (only name +
	// description; bodies fetched on demand via the load_skill tool), or ""
	// (auto — eager below a small skill count, catalog at/above). Progressive
	// disclosure keeps a large skill set from bloating every request. Ignored
	// when Skills is false.
	//
	// Mode is also the trust lever for a lower-trust skills server. Skills are
	// data, not code (ext/skills is enforced no-exec), so the risk they carry
	// is context-poisoning: a skill body is text the model reads as guidance.
	// "eager" splices that text into the system prompt at connect time, with no
	// per-skill gate. "catalog" is the safer default for a server you do not
	// fully trust: a skill enters context only when the model calls load_skill,
	// and load_skill is an ordinary tool, so it flows through the same approval
	// ladder as every other call (set Approval.Rules["load_skill"] = "ask" to
	// confirm each activation; the prompt shows the requested skill name). The
	// fetched body also lands in tool history rather than the system prompt, so
	// it carries less authority. Reserve "eager" for servers you trust.
	SkillsMode string `json:"skillsMode,omitempty"`

	// SkillsAllow, when non-empty, restricts this server to the named skills
	// (by skill Name), a hard capability boundary rather than a display
	// preference. The fetched index is narrowed to the allowed entries before
	// anything else, so both the injected block (eager or catalog) and the
	// load_skill tool only ever see allowed skills. Nil or empty means every
	// skill the server advertises. This is the skills analog of Allow.
	SkillsAllow []string `json:"skillsAllow,omitempty"`

	// Events lists the event streams to open on this server. Each event
	// feeds the injection policy (and any trigger bindings that match).
	Events []EventConfig `json:"events,omitempty"`
}

// EventConfig subscribes one event name and optionally overrides its
// context hint (host config wins over the server-advertised _meta hint).
type EventConfig struct {
	Name string `json:"name"`

	// Hint overrides how occurrences reach the model (priority,
	// aggregation window, template, retention, sensitivity).
	Hint *agent.ContextHint `json:"hint,omitempty"`
}

// TriggerConfig declares one proactive-turn binding, mediated by the
// anti-nag policy (one firing per binding until user engagement plus the
// cooldown, session budget on top).
type TriggerConfig struct {
	// Server and Event select the stream; Server empty matches any.
	Server string `json:"server,omitempty"`
	Event  string `json:"event"`

	// Filter is a set of top-level payload field equality checks (all
	// must match). The config-file rendition of the code-level filter
	// hook; embedders wanting richer predicates use the agent package
	// directly.
	Filter map[string]string `json:"filter,omitempty"`

	// Instructions seed the proactive turn.
	Instructions string `json:"instructions"`

	// Label names the binding in transcripts and logs.
	Label string `json:"label"`

	// CooldownSec is the re-arm floor in seconds (0 = default 300).
	CooldownSec int `json:"cooldownSec,omitempty"`
}

// AuthConfig selects one of the client auth modes MCP supports. Secrets are
// env-indirected like everything else in this config.
type AuthConfig struct {
	// Type is "bearer" (static token), "client-credentials" (OAuth
	// machine-to-machine via PRM/AS discovery), or "oauth"
	// (authorization-code browser flow: PRM/AS discovery + PKCE, DCR when no
	// client is pre-registered; the /mcp overlay's login action re-runs it).
	Type string `json:"type"`

	// TokenEnv names the env var holding the static bearer token.
	// Required for type "bearer".
	TokenEnv string `json:"tokenEnv,omitempty"`

	// ClientIDEnv / ClientSecretEnv name the env vars holding the OAuth
	// client credentials. Required for type "client-credentials".
	ClientIDEnv     string `json:"clientIdEnv,omitempty"`
	ClientSecretEnv string `json:"clientSecretEnv,omitempty"`

	// Scopes to request for OAuth types. Empty inherits the server's PRM
	// scopes_supported.
	Scopes []string `json:"scopes,omitempty"`

	// AllowInsecure permits an http:// authorization server (dev/test
	// only; production AS endpoints must be https).
	AllowInsecure bool `json:"allowInsecure,omitempty"`
}

// Validate checks mode-specific requirements and that named env vars are
// actually set, so misconfiguration fails at startup rather than as a
// mid-conversation 401.
func (a *AuthConfig) Validate() error {
	switch a.Type {
	case "bearer":
		if a.TokenEnv == "" {
			return fmt.Errorf("auth type bearer requires tokenEnv")
		}
		if os.Getenv(a.TokenEnv) == "" {
			return fmt.Errorf("auth env %s is not set", a.TokenEnv)
		}
	case "client-credentials":
		if a.ClientIDEnv == "" || a.ClientSecretEnv == "" {
			return fmt.Errorf("auth type client-credentials requires clientIdEnv and clientSecretEnv")
		}
		for _, env := range []string{a.ClientIDEnv, a.ClientSecretEnv} {
			if os.Getenv(env) == "" {
				return fmt.Errorf("auth env %s is not set", env)
			}
		}
	case "oauth":
		// Authorization-code browser flow. No mandatory env: the client
		// self-registers via DCR when no clientIdEnv is given, and the PKCE
		// flow acquires interactively on the first 401. clientIdEnv (+ optional
		// clientSecretEnv) pin a pre-registered client when set.
		if a.ClientIDEnv != "" && os.Getenv(a.ClientIDEnv) == "" {
			return fmt.Errorf("auth env %s is not set", a.ClientIDEnv)
		}
	default:
		return fmt.Errorf("unknown auth type %q (want bearer, client-credentials, or oauth)", a.Type)
	}
	return nil
}

// LoadConfig reads and validates a config file.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("agentchat: parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("agentchat: %s: %w", path, err)
	}
	return &cfg, nil
}

// LoadConfigWithOverlay loads the base config, then merges the sibling
// local-overlay file (overlayPathFor) over it when that file exists, then
// validates. This is the read side of runtime-config persistence: the overlay
// carries only the mutable picks a slash command changed (active connection,
// approval mode), written by WithConfigOverlay.
//
// The merge is deliberately a second json.Unmarshal into the same struct, so
// the semantics are the standard library's, pinned by tests: a scalar present
// in the overlay overrides the base, a map merges by key (base entries the
// overlay omits survive), a slice present in the overlay REPLACES the base
// slice wholesale, and a key the overlay omits is left untouched. That last
// rule is what lets a one-line overlay (just active) leave servers and
// connections intact.
func LoadConfigWithOverlay(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("agentchat: parse %s: %w", path, err)
	}
	overlay := overlayPathFor(path)
	if oraw, err := os.ReadFile(overlay); err == nil {
		if err := json.Unmarshal(oraw, &cfg); err != nil {
			return nil, fmt.Errorf("agentchat: parse %s: %w", overlay, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("agentchat: %s: %w", path, err)
	}
	return &cfg, nil
}

// Validate enforces the invariants the app relies on.
func (c *Config) Validate() error {
	// A connections registry supersedes Model for the chat provider, so
	// Model is only required when no connections are configured.
	if c.Connections == nil {
		if c.Model.BaseURL == "" || c.Model.Model == "" {
			return fmt.Errorf("model.baseUrl and model.model are required (or set a connections block)")
		}
	}
	seen := map[string]bool{}
	for i, s := range c.Servers {
		if s.ID == "" || s.URL == "" {
			return fmt.Errorf("servers[%d]: id and url are required", i)
		}
		if seen[s.ID] {
			return fmt.Errorf("servers[%d]: duplicate id %q", i, s.ID)
		}
		seen[s.ID] = true
		if s.Auth != nil {
			if err := s.Auth.Validate(); err != nil {
				return fmt.Errorf("servers[%d] (%s): %w", i, s.ID, err)
			}
		}
	}
	return nil
}

// APIKey resolves the model bearer key from the environment. Empty when
// unset or unconfigured.
func (c *Config) APIKey() string {
	if c.Model.APIKeyEnv == "" {
		return ""
	}
	return os.Getenv(c.Model.APIKeyEnv)
}
