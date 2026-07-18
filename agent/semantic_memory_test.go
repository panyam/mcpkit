package agent

import (
	"context"
	"testing"
)

func seedSemantic(t *testing.T) *SemanticMemoryStore {
	t.Helper()
	s, err := NewSemanticMemoryStore(StubEmbedder{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, it := range []MemoryItem{
		{Key: "lang", Value: "my favorite programming language is Go"},
		{Key: "editor", Value: "i edit code in vim every day"},
		{Key: "pet", Value: "my dog is a border collie named Pixel"},
	} {
		if _, err := s.PutMemory(ctx, PutMemoryRequest{Item: it}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestSemanticMemoryStore_RanksBySimilarity(t *testing.T) {
	s := seedSemantic(t)
	resp, err := s.ListMemories(context.Background(), ListMemoriesRequest{Query: "which programming language do i like"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 3 {
		t.Fatalf("got %d items, want all 3 ranked", len(resp.Items))
	}
	// the language note shares the most words with the query -> ranks first
	if resp.Items[0].Item.Key != "lang" {
		t.Fatalf("top hit = %q (score %.3f), want lang", resp.Items[0].Item.Key, resp.Items[0].Score)
	}
	// ranked descending, and the top is more relevant than the tail
	if resp.Items[0].Score <= resp.Items[2].Score {
		t.Fatalf("not ranked by descending score: %.3f then %.3f", resp.Items[0].Score, resp.Items[2].Score)
	}
}

func TestSemanticMemoryStore_Limit(t *testing.T) {
	s := seedSemantic(t)
	resp, _ := s.ListMemories(context.Background(), ListMemoriesRequest{Query: "programming language", Limit: 2})
	if len(resp.Items) != 2 {
		t.Fatalf("Limit=2 returned %d items", len(resp.Items))
	}
}

func TestSemanticMemoryStore_EmptyQueryListsAllOldestFirst(t *testing.T) {
	s := seedSemantic(t)
	resp, _ := s.ListMemories(context.Background(), ListMemoriesRequest{})
	if len(resp.Items) != 3 {
		t.Fatalf("empty query = %d items, want all 3", len(resp.Items))
	}
	if resp.Items[0].Item.Key != "lang" || resp.Items[2].Item.Key != "pet" {
		t.Fatalf("empty-query order = %q..%q, want lang..pet (oldest first)", resp.Items[0].Item.Key, resp.Items[2].Item.Key)
	}
	if resp.Items[0].Score != 0 {
		t.Fatal("empty query has no relevance score; want 0")
	}
}

func TestSemanticMemoryStore_Delete(t *testing.T) {
	s := seedSemantic(t)
	ctx := context.Background()
	if r, _ := s.DeleteMemory(ctx, DeleteMemoryRequest{Key: "editor"}); !r.Deleted {
		t.Fatal("delete of stored key should report Deleted")
	}
	resp, _ := s.ListMemories(ctx, ListMemoriesRequest{})
	if len(resp.Items) != 2 {
		t.Fatalf("after delete = %d items, want 2", len(resp.Items))
	}
	// unknown key is app-state, not an error (same contract as substring store)
	if r, err := s.DeleteMemory(ctx, DeleteMemoryRequest{Key: "ghost"}); err != nil || r.Deleted {
		t.Fatalf("delete unknown = (%v, %v), want (false, nil)", r.Deleted, err)
	}
}

func TestSemanticMemoryStore_NilEmbedder(t *testing.T) {
	if _, err := NewSemanticMemoryStore(nil); err == nil {
		t.Fatal("nil embedder should error")
	}
}

// TestSemanticRecallThroughMemorySource is the payoff: the recall tool is
// semantic (similarity-ranked) with no change to the model-facing surface,
// purely by swapping the MemoryStore behind MemorySource.
func TestSemanticRecallThroughMemorySource(t *testing.T) {
	m, err := NewMemorySource(seedSemantic(t))
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.Call(context.Background(), RecallToolName, map[string]any{"query": "which programming language do i like"})
	if err != nil {
		t.Fatal(err)
	}
	// the most similar note (the language one) renders first
	text := res.Content[0].Text
	if len(text) < 6 || text[:6] != "- lang" {
		t.Fatalf("semantic recall did not rank the language note first:\n%s", text)
	}
}
