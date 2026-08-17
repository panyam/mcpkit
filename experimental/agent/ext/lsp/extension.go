package lsp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/agent"
	"github.com/panyam/mcpkit/experimental/agent/host"
)

// Extension runs language servers alongside the agent and reports what they
// think of the code it is writing.
//
// # Diagnostics arrive by two paths, because they answer two questions
//
// A tool result joins the conversation's history and a context stage does not,
// and that difference decides which path carries which claim.
//
// The middleware appends the problems an edit caused to that edit's own
// result. It is the only path that can reach the model within a turn: stages
// run once per turn, at RunTurn, while an edit-check-fix loop runs across the
// tool-call steps inside one. A model that edits at step 3 and keeps working
// to step 12 would otherwise learn nothing until the turn ended. What that
// block claims is "this edit introduced these errors", which stays true as a
// statement about the past, so history is the right place for it.
//
// The stage carries the current picture instead, and it is transient because
// "the file has these errors" stops being true the moment the model fixes one.
// Written into history it would go stale, accumulate one wrong block per edit,
// and eventually be summarized alongside real conversation. See
// host.contextPipeline, which names that failure for memory injection.
//
// # Why this is middleware rather than an event subscriber
//
// agent.ToolMiddleware says a middleware that merely observes should be an
// event subscriber. This one does not merely observe: it changes the result
// the model receives. A version of it that only recorded which files were
// touched would be an observer and would belong on the event stream, which an
// Extension currently has no route to; see this package's NOTES for that gap.
type Extension struct {
	host.BaseExtension

	pool    *pool
	writes  map[string]WriteSpec
	timeout time.Duration
	maxDiag int

	mu      sync.Mutex
	touched map[string]bool
}

// New starts the configured language servers and returns the extension.
//
// Servers are started here rather than lazily on first use, so a missing
// binary fails App construction with the command in the message. Started on
// demand it would surface as diagnostics that are permanently empty, which
// looks like clean code.
//
// With no servers configured nothing is started and every seam contributes
// nothing, so a surface can wire this unconditionally.
func New(cfg Config) (*Extension, error) {
	if cfg.Root == "" {
		return nil, fmt.Errorf("lsp: Config.Root is required")
	}
	writes := make(map[string]WriteSpec, len(cfg.Writes))
	for _, w := range cfg.Writes {
		if w.Tool == "" || w.Paths == nil {
			return nil, fmt.Errorf("lsp: WriteSpec needs both Tool and Paths")
		}
		writes[w.Tool] = w
	}
	timeout := cfg.DiagnosticsTimeout
	if timeout <= 0 {
		timeout = DefaultDiagnosticsTimeout
	}
	maxDiag := cfg.MaxDiagnostics
	if maxDiag <= 0 {
		maxDiag = DefaultMaxDiagnostics
	}
	// Bounded, because initialize is a request to a process we just spawned
	// and a server that never answers would otherwise hang App construction
	// with no deadline anywhere to break it. A binary that exists but is not a
	// language server is the realistic case, and it fails here rather than at
	// the first tool call.
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	p, err := startPool(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &Extension{
		pool:    p,
		writes:  writes,
		timeout: timeout,
		maxDiag: maxDiag,
		touched: map[string]bool{},
	}, nil
}

// Name identifies the extension, and is the source id its tools register under.
func (e *Extension) Name() string { return "lsp" }

// Tools returns goto_definition and find_references, or nil when no server is
// configured to answer them.
func (e *Extension) Tools() (agent.ToolSource, error) {
	if len(e.pool.clients) == 0 {
		return nil, nil
	}
	return &source{pool: e.pool, defs: toolDefs()}, nil
}

// Middleware re-checks a file after a tool writes it and appends what the
// server found to that tool's result.
func (e *Extension) Middleware() []agent.ToolMiddleware {
	if len(e.pool.clients) == 0 || len(e.writes) == 0 {
		return nil
	}
	return []agent.ToolMiddleware{e.checkAfterWrite}
}

// ContextStages contributes the current-diagnostics block.
func (e *Extension) ContextStages() []host.ContextStage {
	if len(e.pool.clients) == 0 {
		return nil
	}
	return []host.ContextStage{{Name: "lsp.diagnostics", Run: e.injectDiagnostics}}
}

// PromptSections tells the model what the tools are for, since the failure
// mode is not using them: an agent that already knows how to grep will grep.
func (e *Extension) PromptSections() []host.PromptSection {
	if len(e.pool.clients) == 0 {
		return nil
	}
	return []host.PromptSection{host.PromptSectionFunc(func(context.Context) string {
		return `## Navigating code

goto_definition and find_references ask a language server, so they know what a
symbol is. Prefer them over search_files whenever the question is about a symbol
rather than about text: a comment mentioning a name is not a reference, and two
different symbols can share a name.

Both take the name of a symbol, not a line and column. If a bare name appears
more than once in the file, pass the qualified form (Type.Method); an ambiguous
name is refused rather than guessed at.

Diagnostics from the server are reported to you automatically after you edit a
file, and the current set is included in your context each turn. You do not need
to ask for them.`
	})}
}

// Close shuts every language server down.
//
// It exists because a language server is a subprocess, and this is the first
// extension to own one. Directories and file handles could be left to the
// garbage collector and the process exit; a child process cannot, and one that
// outlives the App that spawned it holds a workspace lock and a share of memory
// nobody can account for.
func (e *Extension) Close() error { return e.pool.close() }

// checkAfterWrite runs the call, then asks the server what the write did.
//
// The diagnostics are gathered after next returns, so a denied or failed call
// costs nothing: there is no point re-checking a file that was not written.
func (e *Extension) checkAfterWrite(ctx context.Context, info agent.ToolCallInfo, next agent.ToolCallFunc) (*core.ToolResult, error) {
	spec, watched := e.writes[info.Call.Name]
	if !watched {
		return next(ctx, info)
	}
	res, err := next(ctx, info)
	if err != nil || res == nil || res.IsError {
		return res, err
	}

	args := map[string]any{}
	if info.Call.Args.Len() > 0 {
		if bindErr := info.Call.Args.Bind(&args); bindErr != nil {
			return res, nil
		}
	}
	report := e.recheck(ctx, spec.Paths(args))
	if report == "" {
		return res, nil
	}
	out := *res
	out.Content = append(append([]core.Content{}, res.Content...), core.Content{Type: "text", Text: report})
	return &out, nil
}

// recheck syncs each written path and renders what the servers report.
func (e *Extension) recheck(ctx context.Context, paths []string) string {
	var b strings.Builder
	for _, path := range paths {
		rel, err := e.pool.rel(path)
		if err != nil {
			continue
		}
		c := e.pool.forPath(rel)
		if c == nil {
			continue
		}
		e.mu.Lock()
		e.touched[rel] = true
		e.mu.Unlock()

		if !c.refresh(ctx, rel, e.timeout) {
			// Nothing arrived in time. Saying so beats both silence, which
			// reads as "no problems", and stale diagnostics, which read as
			// problems this edit caused.
			fmt.Fprintf(&b, "\n%s: the language server did not report back within %s", rel, e.timeout)
			continue
		}
		diags := c.diagnostics(rel)
		if len(diags) == 0 {
			fmt.Fprintf(&b, "\n%s: no problems reported", rel)
			continue
		}
		fmt.Fprintf(&b, "\n%s: %d problem(s) after this edit:\n%s", rel, len(diags), renderDiagnostics(c, rel, diags, e.maxDiag))
	}
	if b.Len() == 0 {
		return ""
	}
	return strings.TrimLeft(b.String(), "\n")
}

// injectDiagnostics weaves the current problem set in just before the user's
// message, which is where the transient phase puts its most salient block.
//
// It reports nothing when there is nothing wrong, rather than an explicit "no
// problems". A clean tree is the normal state, and a block asserting it every
// turn would spend context to say nothing while training the model to skip the
// section entirely.
func (e *Extension) injectDiagnostics(_ context.Context, msgs []agent.Message) []agent.Message {
	var b strings.Builder
	total := 0
	for _, rel := range e.trackedFiles() {
		c := e.pool.forPath(rel)
		if c == nil {
			continue
		}
		diags := c.diagnostics(rel)
		if len(diags) == 0 {
			continue
		}
		total += len(diags)
		fmt.Fprintf(&b, "\n%s:\n%s", rel, renderDiagnostics(c, rel, diags, e.maxDiag))
	}
	if total == 0 {
		return msgs
	}
	block := fmt.Sprintf("Current problems reported by the language server (%d):\n%s",
		total, strings.TrimLeft(b.String(), "\n"))
	return weaveBeforeUser(msgs, block)
}

// trackedFiles is every file a server has been told about, whether this
// extension's middleware sent it or a navigation tool did.
func (e *Extension) trackedFiles() []string {
	seen := map[string]bool{}
	e.mu.Lock()
	for rel := range e.touched {
		seen[rel] = true
	}
	e.mu.Unlock()
	for _, c := range e.pool.clients {
		for _, rel := range c.tracked() {
			seen[rel] = true
		}
	}
	return sortedKeys(seen)
}

// renderDiagnostics formats one file's problems as line:col: severity: message,
// capped, and says what the cap dropped.
func renderDiagnostics(c *client, rel string, diags []diagnostic, max int) string {
	sort.SliceStable(diags, func(i, j int) bool {
		if diags[i].Range.Start.Line != diags[j].Range.Start.Line {
			return diags[i].Range.Start.Line < diags[j].Range.Start.Line
		}
		return diags[i].Range.Start.Character < diags[j].Range.Start.Character
	})
	var b strings.Builder
	for i, d := range diags {
		if i >= max {
			fmt.Fprintf(&b, "  ... and %d more\n", len(diags)-max)
			break
		}
		fmt.Fprintf(&b, "  %d:%d: %s: %s\n", d.Range.Start.Line+1, d.Range.Start.Character+1, severityLabel(d.Severity), d.Message)
	}
	return strings.TrimRight(b.String(), "\n")
}

// weaveBeforeUser inserts a block as a system message immediately before the
// final message, which by construction is the user's.
//
// host has an identical unexported helper. Reimplemented rather than exported
// from there because exporting it would widen the surface #1240 is about to
// freeze for one caller's benefit, and the rule it encodes ("closest to the
// user message is most salient") is one line of slice arithmetic.
func weaveBeforeUser(msgs []agent.Message, block string) []agent.Message {
	if block == "" || len(msgs) == 0 {
		return msgs
	}
	n := len(msgs)
	out := make([]agent.Message, 0, n+1)
	out = append(out, msgs[:n-1]...)
	out = append(out, agent.Message{Role: agent.RoleSystem, Text: block})
	return append(out, msgs[n-1])
}

var _ host.Extension = (*Extension)(nil)
