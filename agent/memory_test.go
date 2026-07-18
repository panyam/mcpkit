package agent

import (
	"context"
	"strings"
	"testing"
)

func TestInMemoryMemoryStore_CRUD(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryMemoryStore()

	if _, err := s.PutMemory(ctx, PutMemoryRequest{Item: MemoryItem{Key: "a", Value: "alpha"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutMemory(ctx, PutMemoryRequest{Item: MemoryItem{Key: "b", Value: "beta"}}); err != nil {
		t.Fatal(err)
	}

	// stamped a zero CreatedAt
	got, _ := s.ListMemories(ctx, ListMemoriesRequest{})
	if len(got.Items) != 2 {
		t.Fatalf("list = %d items, want 2", len(got.Items))
	}
	if got.Items[0].Key != "a" || got.Items[1].Key != "b" {
		t.Fatalf("list order = %q,%q, want a,b (oldest first)", got.Items[0].Key, got.Items[1].Key)
	}
	if got.Items[0].CreatedAt.IsZero() {
		t.Fatal("PutMemory should stamp a zero CreatedAt")
	}
}

func TestInMemoryMemoryStore_UpdateKeepsPosition(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryMemoryStore()
	_, _ = s.PutMemory(ctx, PutMemoryRequest{Item: MemoryItem{Key: "a", Value: "alpha"}})
	_, _ = s.PutMemory(ctx, PutMemoryRequest{Item: MemoryItem{Key: "b", Value: "beta"}})
	// overwrite a — must not move it to the back
	_, _ = s.PutMemory(ctx, PutMemoryRequest{Item: MemoryItem{Key: "a", Value: "alpha2"}})

	got, _ := s.ListMemories(ctx, ListMemoriesRequest{})
	if len(got.Items) != 2 || got.Items[0].Key != "a" || got.Items[0].Value != "alpha2" {
		t.Fatalf("update reordered or lost the value: %+v", got.Items)
	}
}

func TestInMemoryMemoryStore_QueryFilter(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryMemoryStore()
	_, _ = s.PutMemory(ctx, PutMemoryRequest{Item: MemoryItem{Key: "lang", Value: "Go"}})
	_, _ = s.PutMemory(ctx, PutMemoryRequest{Item: MemoryItem{Key: "editor", Value: "vim"}})

	// match on value, case-insensitive
	got, _ := s.ListMemories(ctx, ListMemoriesRequest{Query: "go"})
	if len(got.Items) != 1 || got.Items[0].Key != "lang" {
		t.Fatalf("query 'go' = %+v, want just lang", got.Items)
	}
	// match on key
	got, _ = s.ListMemories(ctx, ListMemoriesRequest{Query: "edit"})
	if len(got.Items) != 1 || got.Items[0].Key != "editor" {
		t.Fatalf("query 'edit' = %+v, want just editor", got.Items)
	}
}

func TestInMemoryMemoryStore_DeleteUnknownIsAppState(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryMemoryStore()
	resp, err := s.DeleteMemory(ctx, DeleteMemoryRequest{Key: "nope"})
	if err != nil {
		t.Fatalf("deleting an unknown key must be app-state, got error: %v", err)
	}
	if resp.Deleted {
		t.Fatal("Deleted should be false for an unknown key")
	}

	_, _ = s.PutMemory(ctx, PutMemoryRequest{Item: MemoryItem{Key: "a", Value: "alpha"}})
	resp, _ = s.DeleteMemory(ctx, DeleteMemoryRequest{Key: "a"})
	if !resp.Deleted {
		t.Fatal("Deleted should be true after removing a stored key")
	}
	got, _ := s.ListMemories(ctx, ListMemoriesRequest{})
	if len(got.Items) != 0 {
		t.Fatalf("after delete, list = %d, want 0", len(got.Items))
	}
}

func TestInMemoryMemoryStore_MaxEvictsOldest(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryMemoryStore(WithMaxMemories(2))
	_, _ = s.PutMemory(ctx, PutMemoryRequest{Item: MemoryItem{Key: "a", Value: "1"}})
	_, _ = s.PutMemory(ctx, PutMemoryRequest{Item: MemoryItem{Key: "b", Value: "2"}})
	_, _ = s.PutMemory(ctx, PutMemoryRequest{Item: MemoryItem{Key: "c", Value: "3"}})

	got, _ := s.ListMemories(ctx, ListMemoriesRequest{})
	if len(got.Items) != 2 || got.Items[0].Key != "b" || got.Items[1].Key != "c" {
		t.Fatalf("cap evict = %+v, want b,c (a evicted)", got.Items)
	}
}

func TestMemorySource_RememberRecallForget(t *testing.T) {
	ctx := context.Background()
	m, err := NewMemorySource(NewInMemoryMemoryStore())
	if err != nil {
		t.Fatal(err)
	}

	// the three tools are exposed
	defs, _ := m.Tools(ctx)
	names := map[string]bool{}
	for _, d := range defs {
		names[d.Name] = true
	}
	for _, want := range []string{RememberToolName, RecallToolName, ForgetToolName} {
		if !names[want] {
			t.Fatalf("tool %q not exposed; have %v", want, names)
		}
	}

	// remember with an explicit key
	res, err := m.Call(ctx, RememberToolName, map[string]any{"key": "lang", "value": "Go"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content[0].Text, "lang") {
		t.Fatalf("remember = %+v", res)
	}

	// recall it back
	res, _ = m.Call(ctx, RecallToolName, map[string]any{"query": "go"})
	if res.IsError || !strings.Contains(res.Content[0].Text, "Go") {
		t.Fatalf("recall = %+v, want the stored value", res)
	}

	// forget it
	res, _ = m.Call(ctx, ForgetToolName, map[string]any{"key": "lang"})
	if res.IsError || !strings.Contains(res.Content[0].Text, "forgot") {
		t.Fatalf("forget = %+v", res)
	}

	// recall now empty
	res, _ = m.Call(ctx, RecallToolName, map[string]any{})
	if res.IsError || !strings.Contains(res.Content[0].Text, "empty") {
		t.Fatalf("recall after forget = %+v, want empty", res)
	}
}

func TestMemorySource_RememberAutoKey(t *testing.T) {
	ctx := context.Background()
	m, _ := NewMemorySource(NewInMemoryMemoryStore())
	res, _ := m.Call(ctx, RememberToolName, map[string]any{"value": "no key given"})
	if res.IsError || !strings.Contains(res.Content[0].Text, "mem-") {
		t.Fatalf("auto-key remember = %+v, want a mem- key", res)
	}
}

func TestMemorySource_ForgetUnknownIsNotError(t *testing.T) {
	ctx := context.Background()
	m, _ := NewMemorySource(NewInMemoryMemoryStore())
	res, err := m.Call(ctx, ForgetToolName, map[string]any{"key": "ghost"})
	if err != nil {
		t.Fatalf("forget unknown should not be a dispatch error: %v", err)
	}
	if res.IsError {
		t.Fatal("forget unknown should be a normal model-visible result, not IsError")
	}
	if !strings.Contains(res.Content[0].Text, "no memory") {
		t.Fatalf("forget unknown = %q", res.Content[0].Text)
	}
}

func TestMemorySource_Summary(t *testing.T) {
	ctx := context.Background()
	m, _ := NewMemorySource(NewInMemoryMemoryStore())

	// empty memory injects nothing
	sum, err := m.Summary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum != "" {
		t.Fatalf("empty summary = %q, want empty string", sum)
	}

	_, _ = m.Call(ctx, RememberToolName, map[string]any{"key": "lang", "value": "Go"})
	_, _ = m.Call(ctx, RememberToolName, map[string]any{"key": "os", "value": "darwin"})
	sum, _ = m.Summary(ctx)
	if !strings.Contains(sum, "lang: Go") || !strings.Contains(sum, "os: darwin") {
		t.Fatalf("summary = %q, want both notes", sum)
	}
}
