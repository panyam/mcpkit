package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/panyam/mcpkit/experimental/agent"
	"github.com/panyam/mcpkit/experimental/agent/host"
	"github.com/panyam/mcpkit/core"
)

// WriteSpec declares that a tool writes files and says which of its arguments
// name them.
//
// Declared rather than detected, because capture happens BEFORE the call, when
// the pre-state still exists. There is no filesystem watcher and no
// after-the-fact diff. That is what keeps a checkpoint proportional to what a
// turn touched instead of to the size of the tree, and it is also why a shell
// command cannot be covered: what `make install` will touch is unknowable
// until it has touched it.
type WriteSpec struct {
	// Tool is the tool name to intercept, as the model calls it.
	Tool string

	// Paths returns the files the call is about to write, given its
	// arguments. A path that does not exist yet is fine and is the normal
	// case for a create: it is recorded as absent so undoing deletes it.
	Paths func(args map[string]any) []string
}

// Config configures the extension.
type Config struct {
	// Root is the directory the snapshot store lives in. Required.
	Root string

	// Writes declares the file-writing tools. A tool absent from this list is
	// never captured, and if it also declares no other reverser it is
	// reported by /undo as something that could not be undone rather than
	// passed over in silence.
	Writes []WriteSpec

	// Proposer suggests inverse calls for the gaps /undo could not close.
	// Nil means /undo reports the gaps and stops, which is the default and
	// is never wrong, only less helpful.
	Proposer Proposer

	// Approve gates every proposal. It is called once per proposal and the
	// call is made only if it returns true.
	//
	// Nil means no proposal is ever run: a proposal is a guess at an
	// operation nobody could reverse automatically, aimed at a tool that
	// changes things, so having no way to ask is a reason to do nothing
	// rather than a reason to proceed. Wire it to the host's
	// ElicitationCoordinator.Confirm to reuse the existing prompt.
	Approve func(ctx context.Context, p Proposal) (bool, error)

	// Tools dispatches an approved proposal. Nil disables running them even
	// when Approve says yes, since there would be nowhere to send the call.
	Tools agent.ToolSource
}

// Extension wires the reversal seam into a turn: capture before a write,
// restore on /undo, and report what could not be undone.
//
// Registered through host.WithExtension, so the middleware and the commands
// arrive as one unit sharing one store. That sharing is the reason
// host.Extension exists rather than five separate registrations.
type Extension struct {
	host.BaseExtension

	store    *Store
	writes   map[string]WriteSpec
	proposer Proposer
	approve  func(ctx context.Context, p Proposal) (bool, error)
	tools    agent.ToolSource

	mu         sync.Mutex
	turn       int
	current    *Checkpoint
	unreversed []Gap
}

// New builds the extension, creating the store directory if needed.
func New(cfg Config) (*Extension, error) {
	if cfg.Root == "" {
		return nil, fmt.Errorf("checkpoint: Config.Root is required")
	}
	store, err := NewStore(cfg.Root)
	if err != nil {
		return nil, err
	}
	writes := make(map[string]WriteSpec, len(cfg.Writes))
	for _, w := range cfg.Writes {
		if w.Tool == "" || w.Paths == nil {
			return nil, fmt.Errorf("checkpoint: WriteSpec needs both Tool and Paths")
		}
		writes[w.Tool] = w
	}
	return &Extension{
		store:    store,
		writes:   writes,
		proposer: cfg.Proposer,
		approve:  cfg.Approve,
		tools:    cfg.Tools,
	}, nil
}

// Name identifies the extension.
func (e *Extension) Name() string { return "checkpoint" }

// TurnStart begins a new restore point. The checkpoint itself is not created
// here — it is created lazily on the first captured write — so a turn that
// touches no files costs nothing on disk.
func (e *Extension) TurnStart(context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.turn++
	e.current = nil
	e.unreversed = nil
	return nil
}

// Middleware captures before a write and records what it could not capture.
func (e *Extension) Middleware() []agent.ToolMiddleware {
	return []agent.ToolMiddleware{e.wrap}
}

func (e *Extension) wrap(ctx context.Context, info agent.ToolCallInfo, next agent.ToolCallFunc) (*core.ToolResult, error) {
	// A sub-agent's writes belong inside the parent's checkpoint. The useful
	// restore point is the turn, not each frame of the tree, and capturing
	// per frame would also mean a child's checkpoint outliving the parent
	// turn that owns it.
	if info.Scope.Depth() > 0 {
		return next(ctx, info)
	}

	spec, writes := e.writes[info.Call.Name]
	if writes {
		// Captured before dispatch, which is the only moment the pre-state
		// exists. This runs outside the host's permission gate, so a call the
		// gate goes on to deny still gets captured; restoring it is a no-op
		// because nothing changed, which is the harmless direction.
		args := map[string]any{}
		if info.Call.Args.Len() > 0 {
			if err := info.Call.Args.Bind(&args); err != nil {
				return nil, fmt.Errorf("checkpoint: %q arguments are not a JSON object: %w", info.Call.Name, err)
			}
		}
		if paths := spec.Paths(args); len(paths) > 0 {
			cp, err := e.checkpoint()
			if err != nil {
				return nil, err
			}
			if err := cp.Add(paths...); err != nil {
				return nil, err
			}
		}
	}

	res, err := next(ctx, info)

	// Record the gap only for a call that ran and could have changed
	// something. A read-only tool has nothing to undo, and a denied call
	// never happened, so reporting either would train the user to ignore the
	// list.
	if !writes && !info.ReadOnly && !denied(err) {
		e.record(info)
	}
	return res, err
}

func denied(err error) bool {
	var d *agent.ToolDeniedError
	return errors.As(err, &d)
}

func (e *Extension) record(info agent.ToolCallInfo) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.unreversed = append(e.unreversed, Gap{
		Tool: info.Call.Name,
		Args: truncate(string(info.Call.Args.Raw()), 120),
	})
}

// checkpoint returns this turn's restore point, creating it on first use.
func (e *Extension) checkpoint() (*Checkpoint, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.current != nil {
		return e.current, nil
	}
	cp, err := e.store.Open("turn-" + strconv.Itoa(e.turn))
	if err != nil {
		return nil, err
	}
	e.current = cp
	return cp, nil
}

// Commands returns /undo and /checkpoints.
func (e *Extension) Commands() []*host.Command {
	return []*host.Command{
		{
			Name: "undo",
			Help: "restore files to the start of a turn (optionally: /undo <checkpoint-id>)",
			Run:  e.runUndo,
		},
		{
			Name: "checkpoints",
			Help: "list restore points, newest first",
			Run:  e.runList,
		},
	}
}

func (e *Extension) runUndo(ctx context.Context, args string) (host.CmdResult, error) {
	id := strings.TrimSpace(args)
	if id == "" {
		list, err := e.store.List()
		if err != nil {
			return host.CmdResult{}, err
		}
		if len(list) == 0 {
			return host.CmdResult{Kind: host.CmdMessage, Message: "nothing to undo: no checkpoints yet"}, nil
		}
		id = list[0].ID
	}
	cp, err := e.store.Load(id)
	if err != nil {
		return host.CmdResult{}, fmt.Errorf("checkpoint: no such checkpoint %q: %w", id, err)
	}
	res, err := cp.Restore()
	if err != nil {
		return host.CmdResult{}, err
	}
	report := e.undoReport(cp, res)
	if extra := e.offerProposals(ctx, e.gaps()); extra != "" {
		report += "\n\n" + extra
	}
	return host.CmdResult{Kind: host.CmdMessage, Message: report}, nil
}

// offerProposals asks the proposer for inverse calls and runs only the ones a
// human approves. Returns the transcript to append to the undo report, or ""
// when there was nothing to offer.
//
// Every exit that skips a proposal SAYS so. A silent skip here would leave the
// user believing an offset ran, which is the same false-confidence failure the
// gap report exists to prevent, one level down.
func (e *Extension) offerProposals(ctx context.Context, gaps []Gap) string {
	if e.proposer == nil || len(gaps) == 0 {
		return ""
	}
	proposals, err := e.proposer.Propose(ctx, gaps)
	if err != nil {
		return "could not propose offsets: " + err.Error()
	}
	if len(proposals) == 0 {
		return "no offsets proposed."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d offset(s) proposed. None runs without your approval:", len(proposals))
	for _, p := range proposals {
		fmt.Fprintf(&b, "\n  %s %v\n    why: %s", p.Tool, p.Args, p.Rationale)
		switch {
		case e.approve == nil:
			b.WriteString("\n    NOT RUN: no approval prompt is wired")
		case e.tools == nil:
			b.WriteString("\n    NOT RUN: no tool source is wired")
		default:
			ok, err := e.approve(ctx, p)
			if err != nil {
				fmt.Fprintf(&b, "\n    NOT RUN: approval failed: %v", err)
				continue
			}
			if !ok {
				b.WriteString("\n    declined")
				continue
			}
			if _, err := e.tools.Call(ctx, p.Tool, p.Args); err != nil {
				fmt.Fprintf(&b, "\n    FAILED: %v", err)
				continue
			}
			b.WriteString("\n    ran")
		}
	}
	return b.String()
}

func (e *Extension) gaps() []Gap {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Gap(nil), e.unreversed...)
}

// undoReport says what was restored AND what was not.
//
// The second half is the point. A turn that edits three files and creates a
// GitHub issue reports "3 files restored" under any design that only knows
// about what it captured, and the issue goes unmentioned. A safety net with an
// unreported hole is worse than none, because it stops being checked.
//
// There are two kinds of hole and they are listed separately, because the
// reader can act on them differently. A call with no reverser was never
// undoable and the answer is a proposed offset. A refused path WAS captured
// and would have been restored, except that it is not the thing that was
// captured any more, and the answer is to go and look at it.
func (e *Extension) undoReport(cp *Checkpoint, res RestoreResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d file(s) restored", cp.ID(), len(res.Restored))

	if len(res.Refused) > 0 {
		fmt.Fprintf(&b, "\n\n%d path(s) REFUSED (not restored):", len(res.Refused))
		for _, r := range res.Refused {
			fmt.Fprintf(&b, "\n  %s — %s", r.Path, r.Reason)
		}
	}

	e.mu.Lock()
	gaps := append([]Gap(nil), e.unreversed...)
	e.mu.Unlock()

	if len(gaps) == 0 {
		return b.String()
	}
	fmt.Fprintf(&b, "\n\n%d call(s) had no reverser and were NOT undone:", len(gaps))
	for _, g := range gaps {
		fmt.Fprintf(&b, "\n  %s %s", g.Tool, g.Args)
	}
	return b.String()
}

func (e *Extension) runList(_ context.Context, _ string) (host.CmdResult, error) {
	list, err := e.store.List()
	if err != nil {
		return host.CmdResult{}, err
	}
	if len(list) == 0 {
		return host.CmdResult{Kind: host.CmdMessage, Message: "no checkpoints yet"}, nil
	}
	var b strings.Builder
	for i, info := range list {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s  %s  %d file(s)", info.ID, info.Created.Format("15:04:05"), info.Files)
	}
	return host.CmdResult{Kind: host.CmdMessage, Message: b.String()}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
