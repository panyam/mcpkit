package client_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
)

// blockingServer serves a tool that parks until release is closed or its own
// context is cancelled, so a test can observe whether a caller's cancellation
// actually reaches the wire.
func blockingServer(t *testing.T, release <-chan struct{}) *httptest.Server {
	t.Helper()
	srv := testutil.NewTestServer()
	srv.Register(core.TextTool[struct{}]("block", "blocks until released or cancelled",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			select {
			case <-release:
				return "released", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	))
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	return ts
}

func connectTo(t *testing.T, url string) *client.Client {
	t.Helper()
	c := client.NewClient(url+"/mcp", core.ClientInfo{Name: "cancel-test", Version: "1.0"})
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// TestCallHonoursContextCancellation pins that an ordinary call is
// cancellable. It was not: callImpl took a ctx, spent it on trace capture,
// then called callDirect without it, and the streamable transport fell back
// to context.Background(). Only callers that hand-built a NewCallContext
// (events/stream) could abort a request, so a wedged tool call could not be
// interrupted by anything short of tearing down the client.
func TestCallHonoursContextCancellation(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	c := connectTo(t, blockingServer(t, release).URL)

	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan error, 1)
	go func() {
		_, err := c.ToolCall(ctx, "block", nil)
		returned <- err
	}()

	// Give the call time to reach the server and park, so this proves an
	// in-flight request is aborted rather than a pre-flight ctx check.
	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case err := <-returned:
		if err == nil {
			t.Fatal("cancelled call returned a nil error")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want a context.Canceled error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("call did not return within 5s of cancellation: ctx is not reaching the wire")
	}
}

// TestCallHonoursContextDeadline is the deadline half of the same contract: a
// caller that bounds a call with a timeout must get control back when it
// expires, not when the server finally answers.
func TestCallHonoursContextDeadline(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	c := connectTo(t, blockingServer(t, release).URL)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.ToolCall(ctx, "block", nil)
	if err == nil {
		t.Fatal("call outlived its deadline and returned success")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("deadline took %v to take effect", elapsed)
	}
}

// TestCallContextExplicitContextWins pins the precedence rule on
// Client.CallContext: a caller who built a CallContext with its own Context is
// stating intent, so the ambient ctx argument must not replace it.
func TestCallContextExplicitContextWins(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	c := connectTo(t, blockingServer(t, release).URL)

	inner, cancelInner := context.WithCancel(context.Background())
	cc := client.NewCallContext(inner)

	returned := make(chan error, 1)
	go func() {
		_, err := c.CallContext(context.Background(), cc, "tools/call",
			map[string]any{"name": "block", "arguments": map[string]any{}})
		returned <- err
	}()

	time.Sleep(300 * time.Millisecond)
	cancelInner() // the CallContext's own context, not the argument

	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled from the explicit CallContext, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling the explicit CallContext did not abort the call")
	}
}
