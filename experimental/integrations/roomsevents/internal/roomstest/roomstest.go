// Package roomstest stands up a room bridge with its events for this module's
// tests: an in-memory relay, the bridge, roomsevents on the same MCP server,
// and token auth that maps "agent-<subject>" to that subject with every scope.
package roomstest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/experimental/ext/rooms"
	"github.com/panyam/mcpkit/experimental/ext/rooms/memroom"
	"github.com/panyam/mcpkit/experimental/integrations/roomsevents"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/require"
)

// Room is the resource the tests use.
const Room = "design:demo/board.kicad_sch"

var allScopes = []string{rooms.ScopeRoomsAttach, rooms.ScopePagesRead, rooms.ScopePagesAct}

// Stack is one running bridge.
type Stack struct {
	Relay  *memroom.Relay
	Bridge *rooms.Bridge
	URL    string
}

type tokens struct{}

func (tokens) Validate(r *http.Request) error {
	if (tokens{}).Claims(r) == nil {
		return &core.AuthError{Code: http.StatusUnauthorized, Message: "unknown token"}
	}
	return nil
}

func (tokens) Claims(r *http.Request) *core.Claims {
	sub, ok := strings.CutPrefix(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "agent-")
	if !ok || sub == "" {
		return nil
	}
	return &core.Claims{Subject: sub, Scopes: allScopes}
}

// New starts a stack and stops it when t ends. opts are added to the bridge's
// own, after its page events.
func New(t *testing.T, opts ...rooms.Option) *Stack {
	t.Helper()
	st := &Stack{Relay: memroom.New()}
	ev := roomsevents.New()
	st.Bridge = rooms.New(st.Relay.Transport("bridge"), append([]rooms.Option{rooms.WithPageEvents(ev.Publish)}, opts...)...)
	srv := server.NewServer(core.ServerInfo{Name: "rooms-events-test", Version: "0"}, server.WithAuth(tokens{}))
	st.Bridge.Register(srv)
	ev.Register(srv, st.Bridge, events.Config{StreamHeartbeatInterval: time.Hour})
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(func() {
		st.Bridge.Close()
		ts.Close()
	})
	st.URL = ts.URL + "/mcp"
	return st
}

type bearer string

func (b bearer) Token() (string, error) { return string(b), nil }

// Token is the bearer token for subject.
func Token(subject string) string { return "agent-" + subject }

// Agent connects an MCP session acting for subject.
func (st *Stack) Agent(t *testing.T, subject string) *client.Client {
	t.Helper()
	c := client.NewClient(st.URL, core.ClientInfo{Name: "agent-" + subject, Version: "0"},
		client.WithTokenSource(bearer(Token(subject))), client.WithGetSSEStream())
	require.NoError(t, c.Connect(t.Context()))
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// Attach attaches c to resource.
func Attach(t *testing.T, c *client.Client, resource string) {
	t.Helper()
	res, err := c.ToolCallFull(t.Context(), "attach", map[string]any{"resource": resource})
	require.NoError(t, err)
	require.False(t, res.IsError, "attach failed")
}

// Page joins resource as a page acting for subject.
func (st *Stack) Page(t *testing.T, subject, resource string) *rooms.Page {
	t.Helper()
	p, err := rooms.JoinPage(context.Background(), st.Relay.Transport(subject), resource)
	require.NoError(t, err)
	t.Cleanup(p.Leave)
	return p
}

// WaitAgreed waits until page has agreed to the session c in resource.
func (st *Stack) WaitAgreed(t *testing.T, sessionID, resource string, page rooms.PeerID) {
	t.Helper()
	require.Eventually(t, func() bool { return st.Bridge.PageAgreed(sessionID, resource, page) },
		3*time.Second, 10*time.Millisecond, "page %s never agreed to session %s", page, sessionID)
}

// WaitListed waits until the bridge has listed page, which is when it starts
// hearing the page's events.
func WaitListed(t *testing.T, c *client.Client, page rooms.PeerID) {
	t.Helper()
	require.Eventually(t, func() bool {
		res, err := c.ToolCallFull(context.Background(), "list_pages", map[string]any{})
		if err != nil || res.IsError {
			return false
		}
		raw, _ := json.Marshal(res.StructuredContent)
		var out struct {
			Pages []rooms.PageInfo `json:"pages"`
		}
		_ = json.Unmarshal(raw, &out)
		for _, p := range out.Pages {
			if p.Page == page {
				return true
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond, "page %s never listed", page)
}
