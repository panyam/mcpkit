package agent

import (
	"context"
	"testing"

	"github.com/panyam/mcpkit/core"
)

func textResult(s string) core.ToolResult {
	return core.ToolResult{Content: []core.Content{{Type: "text", Text: s}}}
}

func TestInMemoryToolResultStore_PutGet(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryToolResultStore()

	if _, err := s.PutToolResult(ctx, PutToolResultRequest{Ref: "res:a", Result: textResult("hello")}); err != nil {
		t.Fatalf("PutToolResult: %v", err)
	}
	resp, err := s.GetToolResult(ctx, GetToolResultRequest{Ref: "res:a"})
	if err != nil || !resp.Found {
		t.Fatalf("GetToolResult = (%+v, %v)", resp, err)
	}
	if toolResultText(&resp.Result) != "hello" {
		t.Fatalf("stored result = %q", toolResultText(&resp.Result))
	}
}

func TestInMemoryToolResultStore_UnknownRefIsAppState(t *testing.T) {
	resp, err := NewInMemoryToolResultStore().GetToolResult(context.Background(), GetToolResultRequest{Ref: "res:nope"})
	if err != nil || resp.Found {
		t.Fatalf("Get(unknown) = (%+v, %v), want Found=false, nil error", resp, err)
	}
}

func TestInMemoryToolResultStore_MaxEntriesEvictsOldest(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryToolResultStore(WithMaxToolResults(2))

	for _, ref := range []string{"res:1", "res:2", "res:3"} {
		if _, err := s.PutToolResult(ctx, PutToolResultRequest{Ref: ref, Result: textResult(ref)}); err != nil {
			t.Fatalf("Put %s: %v", ref, err)
		}
	}

	if resp, _ := s.GetToolResult(ctx, GetToolResultRequest{Ref: "res:1"}); resp.Found {
		t.Fatal("res:1 should have been evicted as the oldest")
	}
	for _, ref := range []string{"res:2", "res:3"} {
		if resp, _ := s.GetToolResult(ctx, GetToolResultRequest{Ref: ref}); !resp.Found {
			t.Fatalf("%s should still be present under the cap", ref)
		}
	}
}

func TestInMemoryToolResultStore_UnboundedByDefault(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryToolResultStore()
	for i := 0; i < 50; i++ {
		ref := "res:" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		if _, err := s.PutToolResult(ctx, PutToolResultRequest{Ref: ref, Result: textResult(ref)}); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if resp, _ := s.GetToolResult(ctx, GetToolResultRequest{Ref: "res:a0"}); !resp.Found {
		t.Fatal("first entry evicted despite unbounded default")
	}
}
