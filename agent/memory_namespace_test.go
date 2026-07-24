package agent

import (
	"context"
	"testing"
)

// TestMemoryStore_NamespaceIsolation runs the per-request Namespace contract
// (issue 1003) against every in-process backend through the interface: a
// namespace sees only its own notes, the default ("") namespace is separate, and
// a delete never crosses namespaces.
func TestMemoryStore_NamespaceIsolation(t *testing.T) {
	sem, err := NewInMemorySemanticStore(StubEmbedder{})
	if err != nil {
		t.Fatal(err)
	}
	stores := map[string]MemoryStore{
		"substring": NewInMemoryMemoryStore(),
		"semantic":  sem,
	}

	for name, st := range stores {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			put := func(ns, key, val string) {
				if _, err := st.PutMemory(ctx, PutMemoryRequest{Item: MemoryItem{Key: key, Value: val}, Namespace: ns}); err != nil {
					t.Fatal(err)
				}
			}
			listVals := func(ns string) []string {
				resp, err := st.ListMemories(ctx, ListMemoriesRequest{Namespace: ns})
				if err != nil {
					t.Fatal(err)
				}
				var out []string
				for _, it := range resp.Items {
					out = append(out, it.Item.Value)
				}
				return out
			}

			put("A", "k", "in-A")
			put("B", "k", "in-B")
			put("", "k", "in-default") // same key, different scratchpads

			if got := listVals("A"); len(got) != 1 || got[0] != "in-A" {
				t.Fatalf("namespace A leaked or missing: %v", got)
			}
			if got := listVals("B"); len(got) != 1 || got[0] != "in-B" {
				t.Fatalf("namespace B leaked or missing: %v", got)
			}
			if got := listVals(""); len(got) != 1 || got[0] != "in-default" {
				t.Fatalf("default namespace leaked or missing: %v", got)
			}

			// deleting in A leaves B and the default untouched
			if _, err := st.DeleteMemory(ctx, DeleteMemoryRequest{Key: "k", Namespace: "A"}); err != nil {
				t.Fatal(err)
			}
			if got := listVals("A"); len(got) != 0 {
				t.Fatalf("A not empty after its own delete: %v", got)
			}
			if got := listVals("B"); len(got) != 1 {
				t.Fatalf("delete in A affected B: %v", got)
			}
		})
	}
}
