package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// scriptedSource is a ToolSource whose Call returns a fixed result, for
// driving OffloadingSource without a real MCP server.
type scriptedSource struct {
	def    core.ToolDef
	result core.ToolResult
	err    error
}

func (s *scriptedSource) Tools(context.Context) ([]core.ToolDef, error) {
	return []core.ToolDef{s.def}, nil
}
func (s *scriptedSource) Call(context.Context, string, map[string]any) (*core.ToolResult, error) {
	if s.err != nil {
		return nil, s.err
	}
	r := s.result
	return &r, nil
}

func newOffloader(t *testing.T, result core.ToolResult, cfg OffloadConfig) (*OffloadingSource, *InMemoryToolResultStore) {
	t.Helper()
	store := NewInMemoryToolResultStore()
	src := &scriptedSource{def: core.ToolDef{Name: "dump"}, result: result}
	return NewOffloadingSource(src, store, cfg), store
}

func callDump(t *testing.T, o *OffloadingSource) *core.ToolResult {
	t.Helper()
	res, err := o.Call(context.Background(), "dump", nil)
	if err != nil {
		t.Fatalf("Call(dump): %v", err)
	}
	return res
}

func TestOffloadingSource_SmallResultInlineUnchanged(t *testing.T) {
	o, store := newOffloader(t, textResult("short"), OffloadConfig{Threshold: 100})
	res := callDump(t, o)
	if toolResultText(res) != "short" {
		t.Fatalf("small result was altered: %q", toolResultText(res))
	}
	// nothing stored
	if resp, _ := store.GetToolResult(context.Background(), GetToolResultRequest{Ref: "res:anything"}); resp.Found {
		t.Fatal("small result should not have been offloaded")
	}
}

func TestOffloadingSource_LargeResultStubbedAndStored(t *testing.T) {
	big := strings.Repeat("x", 5000)
	o, store := newOffloader(t, textResult(big), OffloadConfig{Threshold: 4096, PreviewLen: 20})
	res := callDump(t, o)
	stub := toolResultText(res)

	if strings.Contains(stub, big) {
		t.Fatal("stub still contains the full payload")
	}
	if !strings.Contains(stub, "5000B") || !strings.Contains(stub, ReadToolResultName) {
		t.Fatalf("stub missing size or retrieval instruction: %q", stub)
	}
	// preview is bounded
	if strings.Count(stub, "x") > 20+2 {
		t.Fatalf("preview exceeded PreviewLen: %q", stub)
	}

	// the ref in the stub resolves to the full result
	ref := extractRef(t, stub)
	resp, err := store.GetToolResult(context.Background(), GetToolResultRequest{Ref: ref})
	if err != nil || !resp.Found {
		t.Fatalf("stored ref %q not found: %v", ref, err)
	}
	if toolResultText(&resp.Result) != big {
		t.Fatal("stored result does not match the original payload")
	}
}

// TestOffloadingSource_StubIsFaithful pins the log-fidelity property: the
// bytes fed back to the model (the stub) are exactly what a persisted
// RoleTool message would carry, so resume replays what the model saw.
func TestOffloadingSource_StubIsFaithful(t *testing.T) {
	o, _ := newOffloader(t, textResult(strings.Repeat("y", 5000)), OffloadConfig{})
	res := callDump(t, o)
	if res.IsError {
		t.Fatal("stub marked IsError")
	}
	if len(res.Content) != 1 || res.Content[0].Type != "text" {
		t.Fatalf("stub is not a single text content: %+v", res.Content)
	}
	if res.StructuredContent != nil {
		t.Fatal("stub leaked structured content (should live in the blob only)")
	}
}

func TestOffloadingSource_ErrorResultStaysInline(t *testing.T) {
	big := strings.Repeat("z", 5000)
	o, store := newOffloader(t, core.ToolResult{IsError: true, Content: []core.Content{{Type: "text", Text: big}}}, OffloadConfig{Threshold: 100})
	res := callDump(t, o)
	if !res.IsError || toolResultText(res) != big {
		t.Fatal("error result was offloaded; errors must stay inline")
	}
	_ = store
}

func TestOffloadingSource_PerToolPinnedInline(t *testing.T) {
	big := strings.Repeat("q", 5000)
	o, _ := newOffloader(t, textResult(big), OffloadConfig{
		Threshold:        100,
		PerToolThreshold: map[string]int{"dump": 0},
	})
	if toolResultText(callDump(t, o)) != big {
		t.Fatal("per-tool pin (threshold 0) should keep the result inline")
	}
}

func TestOffloadingSource_ReadWindowAndGrep(t *testing.T) {
	lines := []string{"alpha 1", "beta 2", "alpha 3", "gamma 4"}
	payload := strings.Join(lines, "\n") + strings.Repeat(" tail", 2000)
	o, _ := newOffloader(t, textResult(payload), OffloadConfig{Threshold: 100, PreviewLen: 10})
	stub := toolResultText(callDump(t, o))
	ref := extractRef(t, stub)

	// grep
	grep, err := o.Call(context.Background(), ReadToolResultName, map[string]any{"ref": ref, "pattern": "^alpha"})
	if err != nil {
		t.Fatalf("read grep: %v", err)
	}
	gt := toolResultText(grep)
	if !strings.Contains(gt, "alpha 1") || !strings.Contains(gt, "alpha 3") || strings.Contains(gt, "beta") {
		t.Fatalf("grep returned wrong lines: %q", gt)
	}

	// window
	win, err := o.Call(context.Background(), ReadToolResultName, map[string]any{"ref": ref, "offset": float64(0), "limit": float64(5)})
	if err != nil {
		t.Fatalf("read window: %v", err)
	}
	wt := toolResultText(win)
	if !strings.HasPrefix(wt, "alpha") || !strings.Contains(wt, "more chars") {
		t.Fatalf("window did not return a bounded prefix with continuation: %q", wt)
	}
}

func TestOffloadingSource_ReadUnknownRefGraceful(t *testing.T) {
	o, _ := newOffloader(t, textResult("x"), OffloadConfig{})
	res, err := o.Call(context.Background(), ReadToolResultName, map[string]any{"ref": "res:gone"})
	if err != nil {
		t.Fatalf("read unknown ref errored instead of degrading: %v", err)
	}
	if res.IsError {
		t.Fatal("unknown ref should be a graceful non-error answer")
	}
	if !strings.Contains(toolResultText(res), "no longer available") {
		t.Fatalf("unexpected graceful message: %q", toolResultText(res))
	}
}

func TestOffloadingSource_ToolsIncludesReadTool(t *testing.T) {
	o, _ := newOffloader(t, textResult("x"), OffloadConfig{})
	defs, err := o.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range defs {
		names = append(names, d.Name)
	}
	if !contains(names, "dump") || !contains(names, ReadToolResultName) {
		t.Fatalf("Tools() = %v, want both dump and %s", names, ReadToolResultName)
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// extractRef pulls the "res:xxxx" token out of a stub.
func extractRef(t *testing.T, stub string) string {
	t.Helper()
	i := strings.Index(stub, "res:")
	if i < 0 {
		t.Fatalf("no ref in stub: %q", stub)
	}
	end := i + 4
	for end < len(stub) && isHexish(stub[end]) {
		end++
	}
	return stub[i:end]
}

func isHexish(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f')
}
