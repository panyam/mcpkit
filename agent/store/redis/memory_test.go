package redisstore

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/panyam/mcpkit/agent"
)

func newTestMemoryStore(t *testing.T, opts ...MemoryOption) (*MemoryStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	return NewMemoryStore(client, opts...), mr
}

func TestRedisMemoryStore_RoundTripNamespaceAndQuery(t *testing.T) {
	s, _ := newTestMemoryStore(t)
	ctx := context.Background()
	put := func(ns, key, val string) {
		if _, err := s.PutMemory(ctx, agent.PutMemoryRequest{Item: agent.MemoryItem{Key: key, Value: val}, Namespace: ns}); err != nil {
			t.Fatal(err)
		}
	}
	list := func(ns, q string) []agent.ScoredMemory {
		resp, err := s.ListMemories(ctx, agent.ListMemoriesRequest{Namespace: ns, Query: q})
		if err != nil {
			t.Fatal(err)
		}
		return resp.Items
	}

	put("A", "k", "in-A")
	put("B", "k", "in-B") // same key, different namespace
	put("A", "region", "us-east-1")

	if got := list("A", ""); len(got) != 2 {
		t.Fatalf("namespace A should have 2 notes, got %d", len(got))
	}
	if got := list("B", ""); len(got) != 1 || got[0].Item.Value != "in-B" {
		t.Fatalf("namespace B leaked or missing: %+v", got)
	}
	// substring query on value, scoped to A
	if got := list("A", "east"); len(got) != 1 || got[0].Item.Key != "region" {
		t.Fatalf("query 'east' in A = %+v", got)
	}

	// delete in A leaves B untouched; an unknown key is Deleted=false
	if _, err := s.DeleteMemory(ctx, agent.DeleteMemoryRequest{Key: "k", Namespace: "A"}); err != nil {
		t.Fatal(err)
	}
	if got := list("B", ""); len(got) != 1 {
		t.Fatalf("delete in A affected B: %+v", got)
	}
	if dr, _ := s.DeleteMemory(ctx, agent.DeleteMemoryRequest{Key: "nope", Namespace: "A"}); dr.Deleted {
		t.Fatal("deleting an unknown key should report Deleted=false")
	}
}

func TestRedisMemoryStore_UpdatePreservesOrder(t *testing.T) {
	s, _ := newTestMemoryStore(t)
	ctx := context.Background()
	t1 := time.Unix(1000, 0).UTC()
	t2 := time.Unix(2000, 0).UTC()
	s.PutMemory(ctx, agent.PutMemoryRequest{Item: agent.MemoryItem{Key: "a", Value: "old", CreatedAt: t1}})
	s.PutMemory(ctx, agent.PutMemoryRequest{Item: agent.MemoryItem{Key: "b", Value: "b", CreatedAt: t2}})

	// update "a" (zero CreatedAt) — it must keep t1 and stay first
	s.PutMemory(ctx, agent.PutMemoryRequest{Item: agent.MemoryItem{Key: "a", Value: "new"}})

	resp, _ := s.ListMemories(ctx, agent.ListMemoriesRequest{})
	if len(resp.Items) != 2 || resp.Items[0].Item.Key != "a" || resp.Items[0].Item.Value != "new" {
		t.Fatalf("update should preserve order and refresh value: %+v", resp.Items)
	}
}

func TestRedisMemoryStore_TTLExpiresNamespace(t *testing.T) {
	s, mr := newTestMemoryStore(t, WithMemoryTTL(time.Minute))
	ctx := context.Background()
	s.PutMemory(ctx, agent.PutMemoryRequest{Item: agent.MemoryItem{Key: "k", Value: "v"}, Namespace: "sess"})

	mr.FastForward(2 * time.Minute) // past the TTL
	resp, err := s.ListMemories(ctx, agent.ListMemoriesRequest{Namespace: "sess"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 0 {
		t.Fatalf("namespace should be evicted after its TTL, got %+v", resp.Items)
	}
}
