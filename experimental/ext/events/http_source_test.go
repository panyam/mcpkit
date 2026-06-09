package events_test

import (
	"context"
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type chatMsg struct {
	Sender string `json:"sender"`
	Text   string `json:"text"`
}

func newChatHTTPSource(t *testing.T, opts events.HTTPSourceConfig) (*events.HTTPSource[chatMsg], *httptest.Server) {
	t.Helper()
	src := events.NewHTTPSource[chatMsg](events.EventDef{
		Name:        "chat.message",
		Description: "Chat messages from the test feeder",
		Delivery:    []string{"push", "poll", "webhook"},
	}, opts)
	srv := httptest.NewServer(src.Handler())
	t.Cleanup(srv.Close)
	return src, srv
}

func postInject(t *testing.T, srv *httptest.Server, bearer string, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func TestHTTPSource_AcceptsValidInject(t *testing.T) {
	src, srv := newChatHTTPSource(t, events.HTTPSourceConfig{})

	resp := postInject(t, srv, "", `{"sender":"alice","text":"hello"}`)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)

	recent := src.Recent(10)
	require.Len(t, recent, 1)
	assert.Equal(t, "alice", recent[0].Sender)
	assert.Equal(t, "hello", recent[0].Text)
}

func TestHTTPSource_RejectsNonPost(t *testing.T) {
	_, srv := newChatHTTPSource(t, events.HTTPSourceConfig{})

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	assert.Equal(t, http.MethodPost, resp.Header.Get("Allow"))
}

func TestHTTPSource_RequiresBearerWhenConfigured(t *testing.T) {
	const secret = "s3cret"
	src, srv := newChatHTTPSource(t, events.HTTPSourceConfig{Bearer: secret})

	noAuth := postInject(t, srv, "", `{"sender":"a","text":"x"}`)
	noAuth.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, noAuth.StatusCode)

	wrongAuth := postInject(t, srv, "wrong", `{"sender":"a","text":"x"}`)
	wrongAuth.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, wrongAuth.StatusCode)

	rightAuth := postInject(t, srv, secret, `{"sender":"a","text":"x"}`)
	rightAuth.Body.Close()
	assert.Equal(t, http.StatusAccepted, rightAuth.StatusCode)

	require.Len(t, src.Recent(10), 1, "exactly one event accepted across the three attempts")
	assert.Equal(t, "a", src.Recent(10)[0].Sender)
}

func TestHTTPSource_RejectsOversizedBody(t *testing.T) {
	_, srv := newChatHTTPSource(t, events.HTTPSourceConfig{MaxBodyBytes: 32})

	big := bytes.Repeat([]byte("x"), 64)
	resp := postInject(t, srv, "", `{"sender":"a","text":"`+string(big)+`"}`)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
}

func TestHTTPSource_RejectsMalformedJSON(t *testing.T) {
	_, srv := newChatHTTPSource(t, events.HTTPSourceConfig{})

	resp := postInject(t, srv, "", `{not-json`)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHTTPSource_InjectPathDefault(t *testing.T) {
	src := events.NewHTTPSource[chatMsg](events.EventDef{
		Name:     "chat.message",
		Delivery: []string{"push"},
	}, events.HTTPSourceConfig{})
	assert.Equal(t, "/events/chat.message/inject", src.InjectPath())
}

func TestHTTPSource_InjectPathOverride(t *testing.T) {
	src := events.NewHTTPSource[chatMsg](events.EventDef{
		Name:     "chat.message",
		Delivery: []string{"push"},
	}, events.HTTPSourceConfig{InjectPath: "/feeds/chat"})
	assert.Equal(t, "/feeds/chat", src.InjectPath())
}

func TestHTTPSource_YieldingOptionsForwarded(t *testing.T) {
	src := events.NewHTTPSource[chatMsg](events.EventDef{
		Name:     "chat.typing",
		Delivery: []string{"push"},
	}, events.HTTPSourceConfig{
		YieldingOpts: []events.YieldingOption{events.WithoutCursors()},
	})

	srv := httptest.NewServer(src.Handler())
	defer srv.Close()

	resp := postInject(t, srv, "", `{"sender":"a","text":"typing..."}`)
	defer resp.Body.Close()
	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	// Cursorless source advertises Def().Cursorless and Poll returns empty.
	assert.True(t, src.Def().Cursorless, "WithoutCursors should propagate to the inner YieldingSource def")
	poll := src.Poll("", 100)
	assert.Empty(t, poll.Events, "cursorless source's Poll must always return empty")
}

func TestHTTPSource_InProcessYieldEquivalentToHTTP(t *testing.T) {
	src, srv := newChatHTTPSource(t, events.HTTPSourceConfig{})

	require.NoError(t, src.Yield(context.Background(), chatMsg{Sender: "alice", Text: "in-proc"}))

	resp := postInject(t, srv, "", `{"sender":"bob","text":"via-http"}`)
	resp.Body.Close()
	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	recent := src.Recent(10)
	require.Len(t, recent, 2)
	assert.Equal(t, "alice", recent[0].Sender)
	assert.Equal(t, "bob", recent[1].Sender)
}
