package rooms_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/rooms"
	"github.com/panyam/mcpkit/experimental/ext/rooms/memroom"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/require"
)

const room = "design:demo/board.kicad_sch"

var allScopes = []string{rooms.ScopeRoomsAttach, rooms.ScopePagesRead, rooms.ScopePagesAct}

// tokens is a bearer-token validator standing in for ext/auth's JWT
// validator: each token maps to the claims a real validator would produce.
type tokens map[string]*core.Claims

func (v tokens) Validate(r *http.Request) error {
	if v.Claims(r) == nil {
		return &core.AuthError{Code: http.StatusUnauthorized, Message: "unknown token"}
	}
	return nil
}

func (v tokens) Claims(r *http.Request) *core.Claims {
	return v[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
}

type stack struct {
	relay  *memroom.Relay
	bridge *rooms.Bridge
	url    string
	tokens tokens
}

func newStack(t *testing.T, opts ...rooms.Option) *stack {
	t.Helper()
	return newStackOn(t, memroom.New(), opts...)
}

// newStackOn is a bridge and its MCP server on an existing relay, so several
// bridges can share one room.
func newStackOn(t *testing.T, relay *memroom.Relay, opts ...rooms.Option) *stack {
	t.Helper()
	return newStackVia(t, relay, relay.Transport("bridge"), opts...)
}

// newStackVia is newStackOn with the bridge joining through tr, for tests that
// wrap the bridge's transport to watch what it sends.
func newStackVia(t *testing.T, relay *memroom.Relay, tr rooms.RoomTransport, opts ...rooms.Option) *stack {
	t.Helper()
	st := &stack{relay: relay, tokens: tokens{}}
	st.bridge = rooms.New(tr, opts...)
	srv := server.NewServer(core.ServerInfo{Name: "rooms-test", Version: "0"}, server.WithAuth(st.tokens))
	st.bridge.Register(srv)
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(func() {
		st.bridge.Close()
		ts.Close()
	})
	st.url = ts.URL + "/mcp"
	return st
}

// newLocalStack is single-user local mode: no auth, every session acts for
// "alice".
func newLocalStack(t *testing.T, opts ...rooms.Option) *stack {
	t.Helper()
	st := &stack{relay: memroom.New()}
	st.bridge = rooms.New(st.relay.Transport("bridge"), append(opts, rooms.WithLocalSubject("alice"))...)
	srv := server.NewServer(core.ServerInfo{Name: "rooms-test", Version: "0"})
	st.bridge.Register(srv)
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(func() {
		st.bridge.Close()
		ts.Close()
	})
	st.url = ts.URL + "/mcp"
	return st
}

// stepUpRefuser is a fixed token that refuses step-up, naming the scopes the
// server's insufficient_scope challenge asked for so a test can read them.
type stepUpRefuser struct{ token string }

func (s stepUpRefuser) Token() (string, error) { return s.token, nil }

func (s stepUpRefuser) TokenForScopes(scopes []string) (string, error) {
	return "", errors.New("step-up refused, challenge asked for: " + strings.Join(scopes, " "))
}

type agent struct {
	*client.Client
	changed chan struct{}
}

func (st *stack) agent(t *testing.T, subject string, scopes ...string) *agent {
	t.Helper()
	return st.agentOn(t, "session", subject, scopes...)
}

// wires are the two MCP wires an acceptance test runs on (#1610).
var wires = []string{"session", "stateless"}

// agentOn is an agent on the given wire: "session" (Streamable HTTP with an
// MCP session) or "stateless" (SEP-2575, no session).
func (st *stack) agentOn(t *testing.T, wire, subject string, scopes ...string) *agent {
	t.Helper()
	token := subject + "-" + strings.Join(scopes, ",")
	st.tokens[token] = &core.Claims{Subject: subject, Scopes: scopes}
	a := &agent{changed: make(chan struct{}, 64)}
	opts := []client.ClientOption{client.WithTokenSource(stepUpRefuser{token})}
	if wire == "stateless" {
		opts = append(opts, client.WithClientMode(client.ClientModeStateless))
	} else {
		opts = append(opts, client.WithGetSSEStream(), client.WithToolsListChangedHandler(func() {
			select {
			case a.changed <- struct{}{}:
			default:
			}
		}))
	}
	a.Client = client.NewClient(st.url, core.ClientInfo{Name: "agent-" + subject, Version: "0"}, opts...)
	require.NoError(t, a.Connect(t.Context()))
	require.Equal(t, wire == "stateless", a.UsingStatelessWire(), "the agent is on the %s wire", wire)
	t.Cleanup(func() { a.Close() })
	return a
}

type attachResult struct {
	PairingCode string           `json:"pairingCode"`
	Pages       []rooms.PageInfo `json:"pages"`
}

func (a *agent) attach(t *testing.T, resource string) attachResult {
	t.Helper()
	res, err := a.ToolCallFull(t.Context(), "attach", map[string]any{"resource": resource})
	require.NoError(t, err)
	require.False(t, res.IsError, text(res))
	var out attachResult
	b, _ := json.Marshal(res.StructuredContent)
	require.NoError(t, json.Unmarshal(b, &out))
	return out
}

// pages is list_pages for resource, or for the session's default rooms when
// none is given.
func (a *agent) pages(t *testing.T, resource ...string) []rooms.PageInfo {
	t.Helper()
	args := map[string]any{}
	if len(resource) > 0 {
		args["resource"] = resource[0]
	}
	res, err := a.ToolCallFull(t.Context(), "list_pages", args)
	require.NoError(t, err)
	require.False(t, res.IsError, text(res))
	var out struct {
		Pages []rooms.PageInfo `json:"pages"`
	}
	b, _ := json.Marshal(res.StructuredContent)
	require.NoError(t, json.Unmarshal(b, &out))
	return out.Pages
}

// waitPage waits until list_pages shows page in the state ok accepts.
func (a *agent) waitPage(t *testing.T, page rooms.PeerID, ok func(rooms.PageInfo) bool) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, p := range a.pages(t) {
			if p.Page == page && ok(p) {
				return true
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond, "page %s never reached the expected state", page)
}

func (a *agent) toolNames(t *testing.T) []string {
	t.Helper()
	tools, err := a.ListTools(t.Context())
	require.NoError(t, err)
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func text(res *core.ToolResult) string {
	var parts []string
	for _, c := range res.Content {
		parts = append(parts, c.Text)
	}
	b, _ := json.Marshal(res.StructuredContent)
	return strings.Join(parts, " ") + " " + string(b)
}

// recorder is a page tool that records which page ran it and with what.
type recorder struct {
	mu   sync.Mutex
	runs []string
}

func (r *recorder) tool(name, page string, readOnly bool) rooms.PageTool {
	return rooms.PageTool{
		Name:        name,
		Description: "test tool",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"sheet": map[string]any{"type": "string"}}},
		ReadOnly:    readOnly,
		Execute: func(_ context.Context, call rooms.PageCall) (core.ToolResult, error) {
			var args struct {
				Sheet string `json:"sheet"`
			}
			_ = json.Unmarshal(call.Arguments, &args)
			r.mu.Lock()
			r.runs = append(r.runs, page+":"+args.Sheet)
			r.mu.Unlock()
			return core.TextResult(page + " showed " + args.Sheet), nil
		},
	}
}

func (r *recorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.runs...)
}

func (st *stack) page(t *testing.T, subject, resource string, tools ...rooms.PageTool) *rooms.Page {
	t.Helper()
	p, err := rooms.JoinPage(t.Context(), st.relay.Transport(subject), resource)
	require.NoError(t, err)
	for _, tool := range tools {
		p.RegisterTool(tool)
	}
	t.Cleanup(p.Leave)
	return p
}

// callsTo counts tools/call requests the relay delivered to page.
func (st *stack) callsTo(page rooms.PeerID) int {
	n := 0
	for _, d := range st.relay.Deliveries() {
		var m struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(d.Payload, &m)
		if d.To == page && m.Method == "tools/call" {
			n++
		}
	}
	return n
}

func waitChanged(t *testing.T, a *agent) {
	t.Helper()
	select {
	case <-a.changed:
	case <-time.After(3 * time.Second):
		t.Fatal("no notifications/tools/list_changed")
	}
}
