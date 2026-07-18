package agent

import (
	"context"
	"testing"
)

func TestCosineSimilarity(t *testing.T) {
	if got := CosineSimilarity([]float32{1, 0}, []float32{1, 0}); got != 1 {
		t.Fatalf("identical vectors = %v, want 1", got)
	}
	if got := CosineSimilarity([]float32{1, 0}, []float32{0, 1}); got != 0 {
		t.Fatalf("orthogonal vectors = %v, want 0", got)
	}
	// mismatched length and zero vectors degrade to 0, never error
	if got := CosineSimilarity([]float32{1, 2, 3}, []float32{1, 2}); got != 0 {
		t.Fatalf("mismatched length = %v, want 0", got)
	}
	if got := CosineSimilarity([]float32{0, 0}, []float32{1, 1}); got != 0 {
		t.Fatalf("zero vector = %v, want 0", got)
	}
}

func TestStubEmbedderDeterministicAndMeaningful(t *testing.T) {
	e := StubEmbedder{}
	ctx := context.Background()

	// deterministic: same text -> same vector, fixed dimension
	a, _ := e.Embed(ctx, []string{"the quick brown fox"})
	b, _ := e.Embed(ctx, []string{"the quick brown fox"})
	if len(a[0]) != DefaultStubEmbedderDim {
		t.Fatalf("dim = %d, want %d", len(a[0]), DefaultStubEmbedderDim)
	}
	if CosineSimilarity(a[0], b[0]) != 1 {
		t.Fatal("same text should embed identically")
	}

	// meaningful: shared words -> higher similarity than disjoint words
	vecs, _ := e.Embed(ctx, []string{
		"my favorite programming language",
		"the programming language i use",
		"my dog is a border collie",
	})
	shared := CosineSimilarity(vecs[0], vecs[1])
	disjoint := CosineSimilarity(vecs[0], vecs[2])
	if shared <= disjoint {
		t.Fatalf("shared-word similarity %.3f should exceed disjoint %.3f", shared, disjoint)
	}
}
