package massrelay_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panyam/massrelay/services"
	"github.com/stretchr/testify/require"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/rooms"
	"github.com/panyam/mcpkit/experimental/ext/rooms/massrelay"
	"github.com/panyam/mcpkit/experimental/ext/rooms/massrelay/internal/relaytest"
	"github.com/panyam/mcpkit/server"
)

const room = "design:demo/board.kicad_sch"

var allScopes = []string{rooms.ScopeRoomsAttach, rooms.ScopePagesRead, rooms.ScopePagesAct}

// relay is a real massrelay with JWT auth required; see relaytest.
type relay struct {
	svc *services.CollabService
	url string
}

func newRelay(t *testing.T) *relay {
	t.Helper()
	r, err := relaytest.Start()
	require.NoError(t, err)
	t.Cleanup(r.Close)
	return &relay{svc: r.Service, url: r.URL}
}

func token(t *testing.T, subject string, extra map[string]any) string {
	t.Helper()
	s, err := relaytest.Token(subject, extra)
	require.NoError(t, err)
	return s
}

func (r *relay) embedded(subject string) rooms.RoomTransport {
	return massrelay.Embedded(r.svc, &services.Principal{Subject: subject, ClientID: relaytest.ClientID})
}

func (r *relay) dial(t *testing.T, subject string) rooms.RoomTransport {
	return massrelay.Dial(r.url, token(t, subject, nil))
}

// transportKinds runs a contract test once per way of reaching the relay.
var transportKinds = []string{"embedded", "dial"}

func (r *relay) transport(t *testing.T, kind, subject string) rooms.RoomTransport {
	if kind == "embedded" {
		return r.embedded(subject)
	}
	return r.dial(t, subject)
}

func join(t *testing.T, tr rooms.RoomTransport, name string) rooms.Room {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	r, err := tr.Join(ctx, name)
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Leave() })
	return r
}

// next returns the next event of type want, skipping others, or fails.
func next(t *testing.T, r rooms.Room, want rooms.RoomEventType) rooms.RoomEvent {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-r.Events():
			require.True(t, ok, "events closed while waiting for %v", want)
			if ev.Type == want {
				return ev
			}
		case <-deadline:
			t.Fatalf("no event of type %v", want)
		}
	}
}

// quiet asserts r receives no message within a short window.
func quiet(t *testing.T, r rooms.Room) {
	t.Helper()
	timeout := time.After(300 * time.Millisecond)
	for {
		select {
		case ev, ok := <-r.Events():
			if ok && ev.Type == rooms.MessageReceived {
				t.Fatalf("unexpected message from %s: %s", ev.Peer.ID, ev.Payload)
			}
		case <-timeout:
			return
		}
	}
}

// --- bridge stack over the relay ---

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

// stack is the bridge joined to a real relay in-process, serving agents over
// MCP, with pages that dial the relay as browsers would.
type stack struct {
	relay  *relay
	bridge *rooms.Bridge
	url    string
	tokens tokens
}

func newStack(t *testing.T, opts ...rooms.Option) *stack {
	t.Helper()
	st := &stack{relay: newRelay(t), tokens: tokens{}}
	st.bridge = rooms.New(st.relay.embedded("bridge"), opts...)
	srv := server.NewServer(core.ServerInfo{Name: "rooms-massrelay-test", Version: "0"}, server.WithAuth(st.tokens))
	st.bridge.Register(srv)
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(func() {
		st.bridge.Close()
		ts.Close()
	})
	st.url = ts.URL + "/mcp"
	return st
}

type staticToken string

func (s staticToken) Token() (string, error) { return string(s), nil }

type agent struct{ *client.Client }

func (st *stack) agent(t *testing.T, subject string, scopes ...string) *agent {
	t.Helper()
	tok := subject + "-" + strings.Join(scopes, ",")
	st.tokens[tok] = &core.Claims{Subject: subject, Scopes: scopes}
	c := client.NewClient(st.url, core.ClientInfo{Name: "agent-" + subject, Version: "0"}, client.WithTokenSource(staticToken(tok)))
	require.NoError(t, c.Connect(t.Context()))
	t.Cleanup(func() { c.Close() })
	return &agent{c}
}

func (a *agent) attach(t *testing.T, resource string) {
	t.Helper()
	res, err := a.ToolCallFull(t.Context(), "attach", map[string]any{"resource": resource})
	require.NoError(t, err)
	require.False(t, res.IsError, text(res))
}

func (a *agent) waitPaired(t *testing.T, page rooms.PeerID) {
	t.Helper()
	require.Eventually(t, func() bool {
		res, err := a.ToolCallFull(t.Context(), "list_pages", map[string]any{})
		if err != nil || res.IsError {
			return false
		}
		var out struct {
			Pages []rooms.PageInfo `json:"pages"`
		}
		b, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(b, &out)
		for _, p := range out.Pages {
			if p.Page == page && p.Paired {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "page %s never paired", page)
}

func text(res *core.ToolResult) string {
	var parts []string
	for _, c := range res.Content {
		parts = append(parts, c.Text)
	}
	return strings.Join(parts, " ")
}

// page dials the relay as subject and joins room, counting the tools/call
// requests the relay delivers to it so a test can show addressing held.
func (st *stack) page(t *testing.T, subject string, tools ...rooms.PageTool) (*rooms.Page, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	tr := countingTransport{inner: st.relay.dial(t, subject), calls: calls}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	p, err := rooms.JoinPage(ctx, tr, room)
	require.NoError(t, err)
	for _, tool := range tools {
		p.RegisterTool(tool)
	}
	t.Cleanup(p.Leave)
	return p, calls
}

type countingTransport struct {
	inner rooms.RoomTransport
	calls *atomic.Int32
}

func (c countingTransport) Join(ctx context.Context, name string) (rooms.Room, error) {
	r, err := c.inner.Join(ctx, name)
	if err != nil {
		return nil, err
	}
	cr := &countingRoom{Room: r, out: make(chan rooms.RoomEvent, 1024)}
	go func() {
		defer close(cr.out)
		for ev := range r.Events() {
			var m struct {
				Method string `json:"method"`
			}
			if ev.Type == rooms.MessageReceived && json.Unmarshal(ev.Payload, &m) == nil && m.Method == "tools/call" {
				c.calls.Add(1)
			}
			cr.out <- ev
		}
	}()
	return cr, nil
}

type countingRoom struct {
	rooms.Room
	out chan rooms.RoomEvent
}

func (c *countingRoom) Events() <-chan rooms.RoomEvent { return c.out }

// recorder is a page tool that records which page ran it.
type recorder struct {
	mu   sync.Mutex
	runs []string
}

func (r *recorder) tool(name, page string) rooms.PageTool {
	return rooms.PageTool{
		Name:        name,
		Description: "test tool",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"sheet": map[string]any{"type": "string"}}},
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
