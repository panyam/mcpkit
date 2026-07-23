package host

import (
	"io"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// HostEventKind tags a HostEvent. Each is a domain event — a thing that
// happened in the host lifecycle — not a render instruction; the same
// stream drives a renderer, a tracer, a metrics sink, or a web push.
type HostEventKind string

const (
	// HostRunnerEvent carries one streaming turn event (RunnerEvent) — the
	// agent.Event flow (text/thinking/tool deltas) folded into this stream.
	HostRunnerEvent HostEventKind = "runner-event"
	// HostCommandResult carries a slash-command result (Command).
	HostCommandResult HostEventKind = "command-result"
	// HostTurnDone reports a finished turn (Result).
	HostTurnDone HostEventKind = "turn-done"
	// HostTurnFailed reports a failed turn or a command error (Err).
	HostTurnFailed HostEventKind = "turn-failed"
	// HostSessionChanged reports the active session id changing (RunID;
	// empty = none).
	HostSessionChanged HostEventKind = "session-changed"
	// HostSessionWarn reports degraded persistence (Err) — the turn still
	// succeeded.
	HostSessionWarn HostEventKind = "session-warn"
	// HostTriggerFired reports a proactive-turn trigger (Label).
	HostTriggerFired HostEventKind = "trigger-fired"
	// HostSkillsLoaded reports a server's skills load (ServerID, Loaded,
	// Skipped).
	HostSkillsLoaded HostEventKind = "skills-loaded"
	// HostSkillSkipped reports one skipped skill (ServerID, URI, Err).
	HostSkillSkipped HostEventKind = "skill-skipped"
	// HostEventDropped reports a dropped subscription event (ServerID,
	// EventName).
	HostEventDropped HostEventKind = "event-dropped"
	// HostTaskStatus reports a background task status change (TaskStatus).
	HostTaskStatus HostEventKind = "task-status"
	// HostTaskDetached reports a task detaching to the background (Task).
	HostTaskDetached HostEventKind = "task-detached"
	// HostTaskCompleted reports a background task finishing (Task).
	HostTaskCompleted HostEventKind = "task-completed"
	// HostMessage is a plain informational line (Message).
	HostMessage HostEventKind = "message"
	// HostSubAgentEvent carries a sub-agent's nested turn-lifecycle event
	// (SubAgent) — a persona's activity, scoped/depth on the envelope, for a
	// surface to render indented under the parent's turn.
	HostSubAgentEvent HostEventKind = "sub-agent-event"
	// HostServerStateChanged reports an MCP server's connection state changing
	// (ServerID, ServerState; Err on failed/needs-login) — the async
	// graceful-degrade surface (see docs/AGENT_SERVER_STATE.md).
	HostServerStateChanged HostEventKind = "server-state-changed"
)

// HostEvent is one domain event the host announces: a thing that
// happened, fire-and-forget, fanned out to every Observer. It is a tagged
// union — only the fields named for the Kind (in each constant's doc) are
// set. The host never formats for a specific consumer; a renderer turns
// events into ANSI lines, a tracer into spans, a web surface into socket
// frames.
//
// This is distinct from the two other host interaction shapes: input
// arrives INTO the host from a surface (a surface calls Dispatch/RunTurn
// when the user acts), and mid-turn input REQUESTS use the elicitation
// seam (an AskFunc / ElicitationUI, 1→1 and blocking). Events are the
// 1→N fire-and-forget half; neither of the others rides this stream.
//
// Serializability note: most payloads are wire types (agent.Event,
// CmdResult, TurnResult, strings). The task/health events still carry
// live pointers (TaskStatus, Task, and the Failover inside a
// HostCommandResult) — a web surface needs serializable snapshots of
// those; that conversion is issue 994. The terminal renderer reads the
// pointers directly.
type HostEvent struct {
	Kind HostEventKind

	RunnerEvent agent.Event
	Command     CmdResult
	Result      *agent.TurnResult
	Err         string
	RunID       string
	Label       string
	Message     string

	ServerID    string
	ServerState string // ConnState on HostServerStateChanged
	URI         string
	EventName   string
	Loaded      int
	Skipped     int

	TaskStatus *core.DetailedTask
	Task       *client.BackgroundTask

	// SubAgent is the nested sub-agent event on HostSubAgentEvent.
	SubAgent agent.SubAgentEvent
}

// Observer receives the host's HostEvent stream and does whatever it
// wants with it — render to a terminal, emit spans, push to a socket,
// record for a test. The host fans every event out to all registered
// observers (WithObserver), so a renderer and a tracer coexist. On is
// called from the turn goroutine and from event goroutines; an Observer
// that is not inherently serialized must guard its own state.
//
// This is the fire-and-forget half of the host's I/O. It is deliberately
// NOT the input path: getting user input back is either surface-driven
// (a surface calls Dispatch/RunTurn) or, mid-turn, the 1→1 blocking
// elicitation seam — neither of which is an Observer.
type Observer interface {
	On(HostEvent)
}

// WithObserver registers an Observer for the host's event stream. Any
// WithObserver suppresses the default terminal renderer, so a TUI or web
// surface takes over rendering; add NewTerminalRenderer(out) explicitly
// alongside if you also want the terminal output (e.g. a renderer plus a
// tracer). Without any WithObserver, the App renders to the io.Writer
// passed to NewApp.
func WithObserver(o Observer) AppOption {
	return func(opts *appOptions) { opts.observers = append(opts.observers, o) }
}

// NewTerminalRenderer returns the built-in terminal Observer — the ANSI
// line renderer, writing to w. A TUI reuses it by pointing w at a buffer
// and reading the formatted transcript back, rather than reimplementing
// every event's formatting. Color follows the environment (NO_COLOR / TERM);
// use NewTerminalRendererColor to force the decision.
func NewTerminalRenderer(w io.Writer) Observer { return newRenderer(w, envColorEnabled()) }

// NewTerminalRendererColor is NewTerminalRenderer with an explicit color
// decision, for a surface that has already resolved --no-color (or its own
// precedence chain) and wants the ANSI dim styling suppressed accordingly.
func NewTerminalRendererColor(w io.Writer, colorEnabled bool) Observer {
	return newRenderer(w, colorEnabled)
}
