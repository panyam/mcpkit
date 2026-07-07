package core

import (
	"context"
	"testing"
)

func TestTaskBucketKey_FallsBackToSession(t *testing.T) {
	// No keyer installed → falls back to the session ID.
	ctx := context.Background()
	if got := TaskBucketKey(ctx); got != "" {
		t.Errorf("no session, no keyer: got %q, want empty", got)
	}
}

func TestTaskBucketKey_UsesKeyerWhenInstalled(t *testing.T) {
	type subjectKey struct{}
	keyer := func(ctx context.Context) string {
		s, _ := ctx.Value(subjectKey{}).(string)
		return s
	}
	ctx := WithTaskBucketKeyer(context.Background(), keyer)
	ctx = context.WithValue(ctx, subjectKey{}, "tenant-A")
	if got := TaskBucketKey(ctx); got != "tenant-A" {
		t.Errorf("keyer installed: got %q, want tenant-A", got)
	}
}

func TestWithTaskBucketKeyer_NilIsNoOp(t *testing.T) {
	ctx := context.Background()
	if WithTaskBucketKeyer(ctx, nil) != ctx {
		t.Error("nil keyer should return the context unchanged")
	}
}
