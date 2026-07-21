package client_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
)

var ci = core.ClientInfo{Name: "group-test", Version: "1.0"}

func testMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(testutil.NewTestServer().Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	return ts
}

// a URL that refuses connections (port 1 is never listenable), so Connect fails
// transiently and the member retries -> StateFailed.
const refusedURL = "http://127.0.0.1:1/mcp"

func waitState(t *testing.T, g *client.Group, id string, want client.ConnState, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if s, ok := g.State(id); ok && s == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	s, _ := g.State(id)
	t.Fatalf("%s: state = %v, want %v within %s", id, s, want, within)
}

func TestGroup_ReadyAndWaitRequired(t *testing.T) {
	ts := testMCPServer(t)
	g := client.NewGroup(client.WithRequiredTimeout(5 * time.Second))
	g.Add("a", client.NewClient(ts.URL+"/mcp", ci), true)
	g.Start(context.Background())
	defer g.Close()

	if err := g.WaitRequired(context.Background()); err != nil {
		t.Fatalf("WaitRequired: %v", err)
	}
	if s, _ := g.State("a"); s != client.StateReady {
		t.Fatalf("state = %v, want ready", s)
	}
	st := g.Status()
	if len(st) != 1 || st[0].ID != "a" || st[0].State != client.StateReady || !st[0].Required {
		t.Fatalf("status = %+v", st)
	}
}

func TestGroup_OptionalDownDoesNotBlockRequired(t *testing.T) {
	ts := testMCPServer(t)
	g := client.NewGroup(
		client.WithRequiredTimeout(5*time.Second),
		client.WithGroupBackoff(10*time.Millisecond, 50*time.Millisecond),
	)
	g.Add("up", client.NewClient(ts.URL+"/mcp", ci), true)     // required, works
	g.Add("down", client.NewClient(refusedURL, ci), false)     // optional, refused
	g.Start(context.Background())
	defer g.Close()

	// The required member being ready must not wait on the failing optional one.
	if err := g.WaitRequired(context.Background()); err != nil {
		t.Fatalf("WaitRequired blocked on the optional server: %v", err)
	}
	waitState(t, g, "down", client.StateFailed, 3*time.Second)
}

func TestGroup_WaitRequiredTimeout(t *testing.T) {
	g := client.NewGroup(
		client.WithRequiredTimeout(200*time.Millisecond),
		client.WithGroupBackoff(10*time.Millisecond, 50*time.Millisecond),
	)
	g.Add("down", client.NewClient(refusedURL, ci), true) // required, never ready
	g.Start(context.Background())
	defer g.Close()

	err := g.WaitRequired(context.Background())
	if err == nil {
		t.Fatal("WaitRequired should time out when a required server never becomes ready")
	}
	if !strings.Contains(err.Error(), "down") {
		t.Errorf("timeout error should name the laggard, got: %v", err)
	}
}

func TestGroup_ObserverSeesTransitions(t *testing.T) {
	ts := testMCPServer(t)
	var mu sync.Mutex
	seen := map[client.ConnState]bool{}
	g := client.NewGroup(
		client.WithRequiredTimeout(5*time.Second),
		client.WithObserver(func(sc client.StateChange) {
			mu.Lock()
			seen[sc.State] = true
			mu.Unlock()
		}),
	)
	g.Add("a", client.NewClient(ts.URL+"/mcp", ci), true)
	g.Start(context.Background())
	defer g.Close()

	if err := g.WaitRequired(context.Background()); err != nil {
		t.Fatalf("WaitRequired: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !seen[client.StateConnecting] || !seen[client.StateReady] {
		t.Errorf("observer missed transitions, saw: %v", seen)
	}
}

// TestGroup_CloseIdempotent verifies Close stops the retry loops (no goroutine
// leak / hang) even with a member that is still retrying, and is safe twice.
func TestGroup_CloseIdempotent(t *testing.T) {
	g := client.NewGroup(client.WithGroupBackoff(10*time.Millisecond, 50*time.Millisecond))
	g.Add("down", client.NewClient(refusedURL, ci), false)
	g.Start(context.Background())
	waitState(t, g, "down", client.StateFailed, 3*time.Second)

	done := make(chan struct{})
	go func() { g.Close(); g.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung (retry goroutine not stopped)")
	}
}
