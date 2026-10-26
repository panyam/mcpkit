package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	core "github.com/panyam/mcpkit/core"
)

// A stateless Connect makes one round trip, the server/discover probe, so that
// probe is what decides whether a server is usable. A server that refuses the
// connection or the credentials must fail Connect, so a Group reports it as
// failed or needing login instead of ready (chakra#112).

func TestStatelessConnectFailsWhenTheServerIsDown(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL + "/mcp"
	ts.Close()

	c := NewClient(url, core.ClientInfo{Name: "t", Version: "1"}, WithClientMode(ClientModeStateless))
	if err := c.Connect(t.Context()); err == nil {
		t.Fatal("stateless Connect to a server that isn't there succeeded")
	}
}

func TestStatelessConnectReportsAnAuthRejection(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="test"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()

	c := NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "t", Version: "1"}, WithClientMode(ClientModeStateless))
	err := c.Connect(t.Context())
	var authErr *ClientAuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("stateless Connect to a 401 server = %v, want a ClientAuthError", err)
	}
	if got := classifyConnectErr(err); got != StateNeedsLogin {
		t.Errorf("Group would classify it as %v, want needs-login", got)
	}
}

func TestStatelessConnectFailsOnAServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	c := NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "t", Version: "1"}, WithClientMode(ClientModeStateless))
	if err := c.Connect(t.Context()); err == nil {
		t.Fatal("stateless Connect to a server answering 503 succeeded")
	}
}

// A server that answers discover with a JSON-RPC error is up and speaking the
// wire, so it still connects, as issue 829 needs for -32601.
func TestStatelessConnectToleratesARejectedDiscover(t *testing.T) {
	fake := &fakeMCPServer{
		handler: func(string, json.RawMessage) (int, []byte) {
			body, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": 1,
				"error": map[string]any{"code": -32603, "message": "discover is broken"},
			})
			return 200, body
		},
	}
	ts, url := fake.start()
	defer ts.Close()

	c := NewClient(url, core.ClientInfo{Name: "t", Version: "1"}, WithClientMode(ClientModeStateless))
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("a server that answers discover with an error is reachable, Connect = %v", err)
	}
}

func TestGroupDoesNotMarkADownStatelessServerReady(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL + "/mcp"
	ts.Close()

	c := NewClient(url, core.ClientInfo{Name: "t", Version: "1"}, WithClientMode(ClientModeStateless))
	g := NewGroup()
	g.Add("down", c, false)
	g.Start(t.Context())
	defer g.Close()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		switch st, _ := g.State("down"); st {
		case StateReady:
			t.Fatal("a down stateless server was marked ready")
		case StateFailed, StateNeedsLogin:
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the down server never settled")
}
