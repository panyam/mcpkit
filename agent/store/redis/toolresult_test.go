package redisstore

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

func newTestToolResultStore(t *testing.T, opts ...ToolResultOption) (*ToolResultStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	return NewToolResultStore(client, opts...), mr
}

func trText(s string) core.ToolResult {
	return core.ToolResult{Content: []core.Content{{Type: "text", Text: s}}}
}

func TestRedisToolResultStore_PutGet(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestToolResultStore(t)

	if _, err := s.PutToolResult(ctx, agent.PutToolResultRequest{Ref: "res:a", Result: trText("payload")}); err != nil {
		t.Fatalf("PutToolResult: %v", err)
	}
	resp, err := s.GetToolResult(ctx, agent.GetToolResultRequest{Ref: "res:a"})
	if err != nil || !resp.Found {
		t.Fatalf("GetToolResult = (%+v, %v)", resp, err)
	}
	if resp.Result.Content[0].Text != "payload" {
		t.Fatalf("round-trip lost content: %+v", resp.Result)
	}
}

func TestRedisToolResultStore_UnknownRefIsAppState(t *testing.T) {
	s, _ := newTestToolResultStore(t)
	resp, err := s.GetToolResult(context.Background(), agent.GetToolResultRequest{Ref: "res:gone"})
	if err != nil || resp.Found {
		t.Fatalf("Get(unknown) = (%+v, %v), want Found=false, nil error", resp, err)
	}
}

func TestRedisToolResultStore_TTLExpires(t *testing.T) {
	ctx := context.Background()
	s, mr := newTestToolResultStore(t, WithToolResultTTL(time.Minute))

	if _, err := s.PutToolResult(ctx, agent.PutToolResultRequest{Ref: "res:t", Result: trText("x")}); err != nil {
		t.Fatalf("PutToolResult: %v", err)
	}
	if resp, _ := s.GetToolResult(ctx, agent.GetToolResultRequest{Ref: "res:t"}); !resp.Found {
		t.Fatal("result missing before TTL elapsed")
	}
	mr.FastForward(2 * time.Minute)
	if resp, err := s.GetToolResult(ctx, agent.GetToolResultRequest{Ref: "res:t"}); err != nil || resp.Found {
		t.Fatalf("Get after TTL = (%+v, %v), want Found=false", resp, err)
	}
}

func TestRedisToolResultStore_KeyPrefixIsolation(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	a := NewToolResultStore(client, WithToolResultKeyPrefix("tenant-a"))
	b := NewToolResultStore(client, WithToolResultKeyPrefix("tenant-b"))
	if _, err := a.PutToolResult(ctx, agent.PutToolResultRequest{Ref: "res:shared", Result: trText("a")}); err != nil {
		t.Fatalf("PutToolResult: %v", err)
	}
	if resp, _ := b.GetToolResult(ctx, agent.GetToolResultRequest{Ref: "res:shared"}); resp.Found {
		t.Fatal("prefix isolation broken: b saw a's ref")
	}
}
