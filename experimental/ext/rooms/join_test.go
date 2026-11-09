package rooms_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/panyam/mcpkit/experimental/ext/rooms"
)

type fakeMinter struct {
	err     error
	subject string
	room    string
}

var mintedExp = time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

func (m *fakeMinter) MintRoomToken(_ context.Context, subject, room string) (string, time.Time, error) {
	m.subject, m.room = subject, room
	if m.err != nil {
		return "", time.Time{}, m.err
	}
	return "tok-" + subject + "-" + room, mintedExp, nil
}

func joinOpts(m *fakeMinter) rooms.JoinOptions {
	return rooms.JoinOptions{
		Subject: func(r *http.Request) (string, error) {
			if u := r.Header.Get("X-User"); u != "" {
				return u, nil
			}
			return "", errors.New("sign in first")
		},
		RoomOf: func(r *http.Request) (string, error) {
			doc := r.URL.Query().Get("doc")
			if doc == "secret" {
				return "", errors.New("you cannot open secret")
			}
			return "doc:" + doc, nil
		},
		Minter:         m,
		RelayURL:       "/relay",
		BridgeSubjects: []string{"bridge-a"},
	}
}

func doJoin(t *testing.T, h http.Handler, method, query, user string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, "/join"+query, nil)
	if user != "" {
		req.Header.Set("X-User", user)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return rec, body
}

func TestJoinHandler_AnswersTheRoomTokenAndBridges(t *testing.T) {
	m := &fakeMinter{}
	rec, body := doJoin(t, rooms.JoinHandler(joinOpts(m)), http.MethodPost, "?doc=plan", "alice")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, "alice", m.subject)
	assert.Equal(t, "doc:plan", m.room)
	assert.Equal(t, map[string]any{
		"room":           "doc:plan",
		"relayUrl":       "/relay",
		"token":          "tok-alice-doc:plan",
		"bridgeSubjects": []any{"bridge-a"},
		"expiresAt":      "2030-01-02T03:04:05Z",
	}, body)
}

func TestJoinHandler_RefusesWithoutAUser(t *testing.T) {
	m := &fakeMinter{}
	rec, body := doJoin(t, rooms.JoinHandler(joinOpts(m)), http.MethodPost, "?doc=plan", "")

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "sign in first", body["error"])
	assert.Empty(t, m.room, "nothing is minted for a caller with no user")
}

func TestJoinHandler_RefusesARoomTheUserMayNotOpen(t *testing.T) {
	m := &fakeMinter{}
	rec, body := doJoin(t, rooms.JoinHandler(joinOpts(m)), http.MethodPost, "?doc=secret", "alice")

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "you cannot open secret", body["error"])
	assert.Empty(t, m.room)
}

func TestJoinHandler_EmptySubjectOrRoomIsRefused(t *testing.T) {
	o := joinOpts(&fakeMinter{})
	o.Subject = func(*http.Request) (string, error) { return "", nil }
	rec, _ := doJoin(t, rooms.JoinHandler(o), http.MethodPost, "?doc=plan", "alice")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	o = joinOpts(&fakeMinter{})
	o.RoomOf = func(*http.Request) (string, error) { return "", nil }
	rec, _ = doJoin(t, rooms.JoinHandler(o), http.MethodPost, "?doc=plan", "alice")
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestJoinHandler_MintFailureHidesItsCause(t *testing.T) {
	m := &fakeMinter{err: errors.New("key k-123 is missing")}
	rec, body := doJoin(t, rooms.JoinHandler(joinOpts(m)), http.MethodPost, "?doc=plan", "alice")

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, body["error"], "k-123")
}

func TestJoinHandler_AnswersPostOnly(t *testing.T) {
	m := &fakeMinter{}
	rec, _ := doJoin(t, rooms.JoinHandler(joinOpts(m)), http.MethodGet, "?doc=plan", "alice")

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, http.MethodPost, rec.Header().Get("Allow"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Empty(t, m.room, "a GET mints nothing")
}

func TestJoinHandler_PanicsOnMissingOptions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		unset func(*rooms.JoinOptions)
	}{
		{"Subject", func(o *rooms.JoinOptions) { o.Subject = nil }},
		{"RoomOf", func(o *rooms.JoinOptions) { o.RoomOf = nil }},
		{"Minter", func(o *rooms.JoinOptions) { o.Minter = nil }},
		{"RelayURL", func(o *rooms.JoinOptions) { o.RelayURL = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := joinOpts(&fakeMinter{})
			tc.unset(&o)
			assert.PanicsWithValue(t, "rooms: JoinHandler: JoinOptions."+tc.name+" is required", func() { rooms.JoinHandler(o) })
		})
	}
}

func TestJoinHandler_BridgeSubjectsAreCopied(t *testing.T) {
	o := joinOpts(&fakeMinter{})
	h := rooms.JoinHandler(o)
	o.BridgeSubjects[0] = "changed-after"
	_, body := doJoin(t, h, http.MethodPost, "?doc=plan", "alice")
	assert.Equal(t, []any{"bridge-a"}, body["bridgeSubjects"])
	assert.False(t, strings.Contains(body["token"].(string), "changed"))
}
