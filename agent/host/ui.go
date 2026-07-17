package host

import (
	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// UIKind tags a UIEvent so a Surface renders it without re-deriving what
// happened. One kind per distinct thing the host tells the surface.
type UIKind string

const (
	// UIRunnerEvent carries one streaming turn event (RunnerEvent) — the
	// agent.Event flow (text/thinking/tool deltas) folded into this stream.
	UIRunnerEvent UIKind = "runner-event"
	// UICommand carries a slash-command result (Command).
	UICommand UIKind = "command"
	// UITurnDone reports a finished turn (Result).
	UITurnDone UIKind = "turn-done"
	// UITurnFailed reports a failed turn or a command error (Err).
	UITurnFailed UIKind = "turn-failed"
	// UISession reports the active session id (RunID; empty = none).
	UISession UIKind = "session"
	// UISessionWarn reports degraded persistence (Err) — the turn still
	// succeeded.
	UISessionWarn UIKind = "session-warn"
	// UITriggerFired reports a proactive-turn trigger (Label).
	UITriggerFired UIKind = "trigger-fired"
	// UISkillsLoaded reports a server's skills load (ServerID, Loaded,
	// Skipped).
	UISkillsLoaded UIKind = "skills-loaded"
	// UISkillSkipped reports one skipped skill (ServerID, URI, Err).
	UISkillSkipped UIKind = "skill-skipped"
	// UIEventDropped reports a dropped subscription event (ServerID,
	// EventName).
	UIEventDropped UIKind = "event-dropped"
	// UITaskStatus reports a background task status change (TaskStatus).
	UITaskStatus UIKind = "task-status"
	// UITaskDetached reports a task detaching to the background (Task).
	UITaskDetached UIKind = "task-detached"
	// UITaskCompleted reports a background task finishing (Task).
	UITaskCompleted UIKind = "task-completed"
	// UIPrompt asks the surface to show its input prompt.
	UIPrompt UIKind = "prompt"
	// UIMessage is a plain informational line (Message).
	UIMessage UIKind = "message"
)

// UIEvent is one thing the host tells a Surface. It is a tagged union:
// only the fields named for the Kind (in each constant's doc) are set.
// The host never formats for a specific surface — the terminal renderer
// and a future web surface consume the same stream, one turning it into
// ANSI lines, the other into socket frames.
//
// Serializability note: most payloads are already wire types
// (agent.Event, CmdResult, TurnResult, strings). The task/health events
// still carry live pointers (TaskStatus, Task, and the Failover inside a
// UICommand) — a web surface needs serializable snapshots of those; that
// conversion is a follow-up (issue 992 tail), and the terminal renders
// the pointers directly today.
type UIEvent struct {
	Kind UIKind

	RunnerEvent agent.Event
	Command     CmdResult
	Result      *agent.TurnResult
	Err         string
	RunID       string
	Label       string
	Message     string

	ServerID  string
	URI       string
	EventName string
	Loaded    int
	Skipped   int

	TaskStatus *core.DetailedTask
	Task       *client.BackgroundTask
}

// Surface consumes the host's UIEvent stream and renders it however the
// surface wants (terminal lines, a web socket, a test recorder). It is
// the single seam the host outputs through — no host code writes to an
// io.Writer directly, which is what lets a web surface reuse the whole
// host unchanged. Emit is called from the turn goroutine and from event
// goroutines; a Surface that is not inherently serialized must guard its
// own state (the terminal renderer writes to one io.Writer, which is
// fine for the REPL's single-turn-at-a-time model plus occasional
// proactive turns).
type Surface interface {
	Emit(UIEvent)
}

// WithSurface overrides the terminal renderer with a custom Surface — the
// injection point for a web-chat host (or a test recorder). Omitted, the
// App renders to the io.Writer passed to NewApp.
func WithSurface(s Surface) AppOption {
	return func(o *appOptions) { o.surface = s }
}
