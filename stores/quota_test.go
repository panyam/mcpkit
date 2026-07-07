package stores

import (
	"context"
	"testing"
)

func TestInMemoryQuotaStore_ReserveUpToMaxThenDeny(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryQuotaStore()

	r1, _ := s.ReserveQuota(ctx, ReserveQuotaRequest{Principal: "alice", Key: "chat.message", Max: 2})
	r2, _ := s.ReserveQuota(ctx, ReserveQuotaRequest{Principal: "alice", Key: "chat.message", Max: 2})
	r3, _ := s.ReserveQuota(ctx, ReserveQuotaRequest{Principal: "alice", Key: "chat.message", Max: 2})
	if !r1.Granted || !r2.Granted {
		t.Fatalf("first two reservations should be granted, got %v %v", r1.Granted, r2.Granted)
	}
	if r3.Granted {
		t.Errorf("third reservation should be denied at Max=2, got granted (count=%d)", r3.Count)
	}
}

func TestInMemoryQuotaStore_ReleaseThenReserve(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryQuotaStore()
	_, _ = s.ReserveQuota(ctx, ReserveQuotaRequest{Principal: "alice", Key: "e", Max: 1})
	if r, _ := s.ReserveQuota(ctx, ReserveQuotaRequest{Principal: "alice", Key: "e", Max: 1}); r.Granted {
		t.Fatal("expected denial at cap")
	}
	_, _ = s.ReleaseQuota(ctx, ReleaseQuotaRequest{Principal: "alice", Key: "e"})
	if r, _ := s.ReserveQuota(ctx, ReserveQuotaRequest{Principal: "alice", Key: "e", Max: 1}); !r.Granted {
		t.Fatal("expected grant after release")
	}
	// Release-at-zero is a benign no-op.
	_, _ = s.ReleaseQuota(ctx, ReleaseQuotaRequest{Principal: "bob", Key: "e"})
	if c, _ := s.CountQuota(ctx, CountQuotaRequest{Principal: "bob", Key: "e"}); c.Count != 0 {
		t.Errorf("release-at-zero should leave count 0, got %d", c.Count)
	}
}

func TestInMemoryQuotaStore_ScopedByPrincipalAndKey(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryQuotaStore()
	_, _ = s.ReserveQuota(ctx, ReserveQuotaRequest{Principal: "alice", Key: "chat.message", Max: 10})

	// A different principal or a different key is a distinct bucket.
	if c, _ := s.CountQuota(ctx, CountQuotaRequest{Principal: "bob", Key: "chat.message"}); c.Count != 0 {
		t.Errorf("cross-principal leak: bob count = %d, want 0", c.Count)
	}
	if c, _ := s.CountQuota(ctx, CountQuotaRequest{Principal: "alice", Key: "alert.fired"}); c.Count != 0 {
		t.Errorf("cross-key leak: alice/alert.fired count = %d, want 0", c.Count)
	}
	if c, _ := s.CountQuota(ctx, CountQuotaRequest{Principal: "alice", Key: "chat.message"}); c.Count != 1 {
		t.Errorf("own bucket count = %d, want 1", c.Count)
	}
}
