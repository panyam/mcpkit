package eventsclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type pushedMsg struct {
	Sender string `json:"sender"`
	Text   string `json:"text"`
}

func fakeInjectServer(t *testing.T, bearer string, capture *string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		if bearer != "" {
			assert.Equal(t, "Bearer "+bearer, r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		if capture != nil {
			*capture = string(body)
		}
		w.WriteHeader(status)
	}))
}

func TestPusher_PushNamed_Success(t *testing.T) {
	var captured string
	srv := fakeInjectServer(t, "", &captured, http.StatusAccepted)
	defer srv.Close()

	p := eventsclient.NewPusher(srv.URL, "")
	err := p.PushNamed(context.Background(), "chat.message", pushedMsg{Sender: "alice", Text: "hi"})
	require.NoError(t, err)

	var got pushedMsg
	require.NoError(t, json.Unmarshal([]byte(captured), &got))
	assert.Equal(t, pushedMsg{Sender: "alice", Text: "hi"}, got)
}

func TestPusher_PushNamed_SendsBearer(t *testing.T) {
	srv := fakeInjectServer(t, "s3cret", nil, http.StatusAccepted)
	defer srv.Close()

	p := eventsclient.NewPusher(srv.URL, "s3cret")
	err := p.PushNamed(context.Background(), "chat.message", pushedMsg{Sender: "x", Text: "y"})
	require.NoError(t, err)
}

func TestPusher_PushNamed_PathConstruction(t *testing.T) {
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	p := eventsclient.NewPusher(srv.URL+"/", "")
	err := p.PushNamed(context.Background(), "presence.changed", map[string]any{"user": "a"})
	require.NoError(t, err)
	assert.Equal(t, "/events/presence.changed/inject", capturedPath)
}

func TestPusher_PushNamed_NonAcceptedReturnsPushError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("unauthorized"))
	}))
	defer srv.Close()

	p := eventsclient.NewPusher(srv.URL, "wrong")
	err := p.PushNamed(context.Background(), "chat.message", pushedMsg{Sender: "a", Text: "b"})
	require.Error(t, err)

	var pe *eventsclient.PushError
	require.True(t, errors.As(err, &pe))
	assert.Equal(t, "chat.message", pe.EventName)
	assert.Equal(t, http.StatusUnauthorized, pe.StatusCode)
	assert.Contains(t, pe.Body, "unauthorized")
}

func TestPusher_Push_RawBytes(t *testing.T) {
	var captured string
	srv := fakeInjectServer(t, "", &captured, http.StatusAccepted)
	defer srv.Close()

	p := eventsclient.NewPusher(srv.URL, "")
	raw := []byte(`{"sender":"raw","text":"bytes"}`)
	require.NoError(t, p.Push(context.Background(), "chat.message", raw))
	assert.JSONEq(t, string(raw), captured)
}

func TestPusher_ContextCancellation(t *testing.T) {
	// Server hangs forever; the cancelled context must unblock the call.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := eventsclient.NewPusher(srv.URL, "")
	err := p.PushNamed(ctx, "chat.message", pushedMsg{Sender: "a", Text: "b"})
	require.Error(t, err)
}
