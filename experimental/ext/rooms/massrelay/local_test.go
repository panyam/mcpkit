package massrelay_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/rooms"
	"github.com/panyam/mcpkit/experimental/ext/rooms/massrelay"
	"github.com/panyam/mcpkit/server"
)

// A web app as #1677 describes it: one server with its MCP endpoint, a Local
// relay under /relay and the join endpoint, behind the app's own auth (the
// X-User header here).
type localApp struct {
	local  *massrelay.Local
	bridge *rooms.Bridge
	base   string
	tokens tokens
}

func newLocalApp(t *testing.T, o massrelay.LocalOptions) *localApp {
	t.Helper()
	local, err := massrelay.NewLocal(o)
	require.NoError(t, err)
	app := &localApp{local: local, tokens: tokens{}}
	app.bridge = rooms.New(local.Transport())
	srv := server.NewServer(core.ServerInfo{Name: "local-app", Version: "0"}, server.WithAuth(app.tokens))
	app.bridge.Register(srv)

	mux := http.NewServeMux()
	mux.Handle("/mcp", srv.Handler(server.WithStreamableHTTP(true)))
	mux.Handle("/relay/", http.StripPrefix("/relay", local.Relay()))
	mux.Handle("/join", local.JoinHandler(rooms.JoinOptions{
		Subject: func(r *http.Request) (string, error) {
			if u := r.Header.Get("X-User"); u != "" {
				return u, nil
			}
			return "", errors.New("sign in first")
		},
		RoomOf:   func(r *http.Request) (string, error) { return r.URL.Query().Get("room"), nil },
		RelayURL: "/relay",
	}))
	ts := httptest.NewServer(mux)
	t.Cleanup(func() {
		app.bridge.Close()
		ts.Close()
		local.Close()
	})
	app.base = ts.URL
	return app
}

func (a *localApp) join(t *testing.T, user, room string) rooms.JoinResponse {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, a.base+"/join?room="+room, nil)
	require.NoError(t, err)
	req.Header.Set("X-User", user)
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	var out rooms.JoinResponse
	require.NoError(t, json.NewDecoder(res.Body).Decode(&out))
	return out
}

func (a *localApp) dial(j rooms.JoinResponse) rooms.RoomTransport {
	return massrelay.Dial(a.base+j.RelayURL, j.Token)
}

// Acceptance 1 of #1677: a page joins through the handler, and its token is
// refused for a second room.
func TestLocal_JoinTokenAdmitsItsRoomAlone(t *testing.T) {
	app := newLocalApp(t, massrelay.LocalOptions{})
	j := app.join(t, "alice", room)

	assert.Equal(t, room, j.Room)
	assert.Equal(t, []string{massrelay.DefaultBridgeSubject}, j.BridgeSubjects)
	assert.WithinDuration(t, time.Now().Add(massrelay.DefaultTokenTTL), j.ExpiresAt, 5*time.Second)

	p := join(t, app.dial(j), room)
	assert.NotEmpty(t, p.Self())

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := app.dial(j).Join(ctx, "design:other/room")
	require.Error(t, err, "the token names one room, so the relay refuses any other")
}

func TestLocal_RefusesTokensItsSignerDidNotMint(t *testing.T) {
	app := newLocalApp(t, massrelay.LocalOptions{})
	other, err := massrelay.NewSigner(massrelay.SignerOptions{})
	require.NoError(t, err)
	tok, _, err := other.MintRoomToken(t.Context(), "alice", room)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err = massrelay.Dial(app.base+"/relay", tok).Join(ctx, room)
	require.Error(t, err)
}

// The whole path on a Local relay: a page joins through the endpoint and
// trusts the bridge it names, and an agent's call reaches the page's tool.
func TestLocal_AgentCallReachesAPageThatJoinedThroughTheEndpoint(t *testing.T) {
	app := newLocalApp(t, massrelay.LocalOptions{BridgeSubject: "app-bridge"})
	j := app.join(t, "alice", room)
	require.Equal(t, []string{"app-bridge"}, j.BridgeSubjects)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	page, err := rooms.JoinPage(ctx, app.dial(j), room, rooms.TrustBridges(j.BridgeSubjects...))
	require.NoError(t, err)
	t.Cleanup(page.Leave)
	rec := &recorder{}
	page.RegisterTool(rec.tool("show_sheet", "p1"))

	st := &stack{url: app.base + "/mcp", tokens: app.tokens}
	a := st.agent(t, "alice", allScopes...)
	a.attach(t, room)
	page.SetAgentsAllowed(true)
	a.waitPaired(t, page.ID())

	res, err := a.ToolCallFull(t.Context(), "show_sheet", map[string]any{"sheet": "s-power"})
	require.NoError(t, err)
	require.False(t, res.IsError, text(res))
	assert.Equal(t, []string{"p1:s-power"}, rec.all())
}

func TestSigner_ClaimsAreWhatTheRelayReads(t *testing.T) {
	s, err := massrelay.NewSigner(massrelay.SignerOptions{ClientID: "app", Issuer: "app-iss", TTL: time.Minute})
	require.NoError(t, err)
	tok, exp, err := s.MintRoomToken(t.Context(), "alice", room)
	require.NoError(t, err)

	claims, err := s.Authenticator().ValidateToken(t.Context(), tok)
	require.NoError(t, err)
	assert.Equal(t, "alice", claims.Subject)
	assert.Equal(t, "app", claims.ClientID)
	assert.Equal(t, []string{room}, claims.Rooms)
	assert.True(t, claims.ExpiresAt.Equal(exp), "reported %v, token says %v", exp, claims.ExpiresAt)
	assert.WithinDuration(t, time.Now().Add(time.Minute), exp, 2*time.Second)
	assert.Equal(t, "app", s.ClientID())
}

func TestSigner_RefusesEmptySubjectOrRoom(t *testing.T) {
	s, err := massrelay.NewSigner(massrelay.SignerOptions{})
	require.NoError(t, err)
	_, _, err = s.MintRoomToken(t.Context(), "", room)
	assert.Error(t, err)
	_, _, err = s.MintRoomToken(t.Context(), "alice", "")
	assert.Error(t, err)
}

func TestSigner_GivenKeySurvivesANewSigner(t *testing.T) {
	key := []byte("a key the app keeps across restarts")
	first, err := massrelay.NewSigner(massrelay.SignerOptions{Key: key})
	require.NoError(t, err)
	tok, _, err := first.MintRoomToken(t.Context(), "alice", room)
	require.NoError(t, err)

	second, err := massrelay.NewSigner(massrelay.SignerOptions{Key: key})
	require.NoError(t, err)
	_, err = second.Authenticator().ValidateToken(t.Context(), tok)
	assert.NoError(t, err, "same key, so the restarted signer accepts the old token")

	fresh, err := massrelay.NewSigner(massrelay.SignerOptions{})
	require.NoError(t, err)
	_, err = fresh.Authenticator().ValidateToken(t.Context(), tok)
	assert.Error(t, err, "a generated key is per process")
}
