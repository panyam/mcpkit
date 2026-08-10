package checkpoint

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

func pathArg(args map[string]any) []string {
	p, _ := args["path"].(string)
	if p == "" {
		return nil
	}
	return []string{p}
}

func newExt(t *testing.T) (*Extension, string) {
	t.Helper()
	work := t.TempDir()
	e, err := New(Config{
		Root:   filepath.Join(t.TempDir(), "cp"),
		Writes: []WriteSpec{{Tool: "write_file", Paths: pathArg}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return e, work
}

func call(name string, args map[string]any) agent.ToolCall {
	raw, _ := json.Marshal(args)
	return agent.ToolCall{Name: name, Args: core.NewRawJSON(raw)}
}

// dispatch runs one call through the extension's middleware, with next
// performing the write the tool would have done.
func dispatch(t *testing.T, e *Extension, info agent.ToolCallInfo, effect func()) error {
	t.Helper()
	mw := e.Middleware()[0]
	_, err := mw(context.Background(), info, func(context.Context, agent.ToolCallInfo) (*core.ToolResult, error) {
		if effect != nil {
			effect()
		}
		return &core.ToolResult{}, nil
	})
	return err
}

func TestCaptureAndUndoRestoresFile(t *testing.T) {
	e, work := newExt(t)
	f := filepath.Join(work, "a.txt")
	write(t, f, "original")

	if err := e.TurnStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	info := agent.ToolCallInfo{Call: call("write_file", map[string]any{"path": f})}
	if err := dispatch(t, e, info, func() { write(t, f, "clobbered") }); err != nil {
		t.Fatal(err)
	}

	res, err := e.runUndo(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, f); got != "original" {
		t.Fatalf("undo gave %q, want %q", got, "original")
	}
	if !strings.Contains(res.Message, "1 file(s) restored") {
		t.Fatalf("undo report = %q", res.Message)
	}
}

// TestSubAgentWritesDoNotCheckpoint is the reason RunScope (#1259) exists. A
// checkpoint per nested frame is both wasteful and wrong: the useful restore
// point is the turn.
func TestSubAgentWritesDoNotCheckpoint(t *testing.T) {
	e, work := newExt(t)
	f := filepath.Join(work, "a.txt")
	write(t, f, "original")

	if err := e.TurnStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	info := agent.ToolCallInfo{
		Call:  call("write_file", map[string]any{"path": f}),
		Scope: agent.RunScope{Path: agent.AgentPath{}.Child("researcher")},
	}
	if err := dispatch(t, e, info, func() { write(t, f, "child wrote this") }); err != nil {
		t.Fatal(err)
	}

	list, err := e.store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("a sub-agent write created %d checkpoint(s), want 0", len(list))
	}
}

// TestReadOnlyTurnCreatesNoCheckpoint pins laziness: a turn that touches no
// files costs nothing on disk.
func TestReadOnlyTurnCreatesNoCheckpoint(t *testing.T) {
	e, _ := newExt(t)
	if err := e.TurnStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	info := agent.ToolCallInfo{Call: call("list_files", nil), ReadOnly: true}
	if err := dispatch(t, e, info, nil); err != nil {
		t.Fatal(err)
	}
	list, err := e.store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("a read-only turn created %d checkpoint(s), want 0", len(list))
	}
}

// TestUndoReportsUnreversedCalls is the gap-reporting requirement. Without it
// /undo says "1 file restored" while a created issue goes unmentioned, which
// is a safety net with an unreported hole.
func TestUndoReportsUnreversedCalls(t *testing.T) {
	e, work := newExt(t)
	f := filepath.Join(work, "a.txt")
	write(t, f, "original")

	if err := e.TurnStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(t, e, agent.ToolCallInfo{Call: call("write_file", map[string]any{"path": f})},
		func() { write(t, f, "edited") }); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(t, e, agent.ToolCallInfo{Call: call("create_issue", map[string]any{"title": "bug"})}, nil); err != nil {
		t.Fatal(err)
	}

	res, err := e.runUndo(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Message, "1 file(s) restored") {
		t.Fatalf("report lost the restore count: %q", res.Message)
	}
	if !strings.Contains(res.Message, "create_issue") {
		t.Fatalf("report did not name the unreversed call: %q", res.Message)
	}
	if !strings.Contains(res.Message, "NOT undone") {
		t.Fatalf("report did not say the call was not undone: %q", res.Message)
	}
}

// TestReadOnlyAndDeniedCallsAreNotReported pins the other side: a list that
// includes calls with nothing to undo trains the user to ignore it.
func TestReadOnlyAndDeniedCallsAreNotReported(t *testing.T) {
	e, _ := newExt(t)
	if err := e.TurnStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(t, e, agent.ToolCallInfo{Call: call("search", nil), ReadOnly: true}, nil); err != nil {
		t.Fatal(err)
	}

	mw := e.Middleware()[0]
	_, err := mw(context.Background(), agent.ToolCallInfo{Call: call("deploy", nil)},
		func(context.Context, agent.ToolCallInfo) (*core.ToolResult, error) {
			return nil, agent.DenyTool("declined by user")
		})
	if err == nil {
		t.Fatal("expected the denial to propagate")
	}

	e.mu.Lock()
	got := len(e.unreversed)
	e.mu.Unlock()
	if got != 0 {
		t.Fatalf("recorded %d unreversed calls, want 0 (read-only and denied are neither)", got)
	}
}

// TestTurnStartScopesTheGapList pins that /undo reports the turn it is undoing
// rather than everything the session ever did.
func TestTurnStartScopesTheGapList(t *testing.T) {
	e, _ := newExt(t)
	ctx := context.Background()
	if err := e.TurnStart(ctx); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(t, e, agent.ToolCallInfo{Call: call("create_issue", nil)}, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.TurnStart(ctx); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	got := len(e.unreversed)
	e.mu.Unlock()
	if got != 0 {
		t.Fatalf("a new turn kept %d stale gap(s)", got)
	}
}

// TestOneCheckpointPerTurn pins that several writes in a turn share a restore
// point rather than each making their own.
func TestOneCheckpointPerTurn(t *testing.T) {
	e, work := newExt(t)
	a, b := filepath.Join(work, "a.txt"), filepath.Join(work, "b.txt")
	write(t, a, "a1")
	write(t, b, "b1")

	if err := e.TurnStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{a, b} {
		if err := dispatch(t, e, agent.ToolCallInfo{Call: call("write_file", map[string]any{"path": f})},
			func() { write(t, f, "edited") }); err != nil {
			t.Fatal(err)
		}
	}

	list, err := e.store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("one turn made %d checkpoints, want 1", len(list))
	}
	if list[0].Files != 2 {
		t.Fatalf("checkpoint holds %d files, want 2", list[0].Files)
	}
}

func TestUndoCreatedFileDeletesIt(t *testing.T) {
	e, work := newExt(t)
	f := filepath.Join(work, "new.txt")

	if err := e.TurnStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(t, e, agent.ToolCallInfo{Call: call("write_file", map[string]any{"path": f})},
		func() { write(t, f, "created") }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.runUndo(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatalf("undo left the created file: %v", err)
	}
}

func TestUndoWithNoCheckpointsIsNotAnError(t *testing.T) {
	e, _ := newExt(t)
	res, err := e.runUndo(context.Background(), "")
	if err != nil {
		t.Fatalf("undo with nothing to undo should not error: %v", err)
	}
	if !strings.Contains(res.Message, "nothing to undo") {
		t.Fatalf("message = %q", res.Message)
	}
}

func TestUndoUnknownCheckpointErrors(t *testing.T) {
	e, _ := newExt(t)
	if _, err := e.runUndo(context.Background(), "turn-999"); err == nil {
		t.Fatal("undo of an unknown checkpoint should error")
	}
}

func TestCheckpointsListsNewestFirst(t *testing.T) {
	e, work := newExt(t)
	f := filepath.Join(work, "a.txt")
	write(t, f, "x")
	ctx := context.Background()
	for range 3 {
		if err := e.TurnStart(ctx); err != nil {
			t.Fatal(err)
		}
		if err := dispatch(t, e, agent.ToolCallInfo{Call: call("write_file", map[string]any{"path": f})}, nil); err != nil {
			t.Fatal(err)
		}
	}
	res, err := e.runList(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"turn-1", "turn-2", "turn-3"} {
		if !strings.Contains(res.Message, want) {
			t.Fatalf("listing missing %s: %q", want, res.Message)
		}
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("empty Root should error")
	}
	if _, err := New(Config{Root: t.TempDir(), Writes: []WriteSpec{{Tool: "x"}}}); err == nil {
		t.Fatal("a WriteSpec without Paths should error")
	}
}
