package client_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	client "github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	server "github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/server/stateless"
)

// connectOverStdio serves newTestMCPServer over an io.Pipe pair and connects
// a client in mode to it.
func connectOverStdio(t *testing.T, mode client.ClientMode) *client.Client {
	t.Helper()
	srv := newTestMCPServer()
	sr, cw := io.Pipe()
	cr, sw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		srv.RunIO(ctx, sr, sw)
		sw.Close()
	}()
	c := client.NewClient("stdio://", core.ClientInfo{Name: "c", Version: "1"},
		client.WithIOTransport(cr, cw), client.WithClientMode(mode))
	if err := c.Connect(ctx); err != nil {
		cancel()
		t.Fatalf("%v client: Connect over stdio: %v", mode, err)
	}
	t.Cleanup(func() {
		c.Close()
		cancel()
	})
	return c
}

func TestStdioServesBothWires(t *testing.T) {
	for _, tc := range []struct {
		mode          client.ClientMode
		wantStateless bool
	}{
		{client.ClientModeLegacyOnly, false},
		{client.ClientModeStateless, true},
		{client.ClientModeAdaptive, true},
	} {
		t.Run(tc.mode.String(), func(t *testing.T) {
			c := connectOverStdio(t, tc.mode)
			if got := c.UsingStatelessWire(); got != tc.wantStateless {
				t.Errorf("UsingStatelessWire = %v, want %v", got, tc.wantStateless)
			}
			if _, err := c.ListTools(context.Background()); err != nil {
				t.Fatalf("tools/list: %v", err)
			}
			out, err := c.ToolCall(context.Background(), "echo", map[string]any{"message": "hi"})
			if err != nil || out != "echo: hi" {
				t.Errorf("tools/call echo = %q, %v; want %q", out, err, "echo: hi")
			}
		})
	}
}

// A notification gets no reply on stdio, whether it omits id or sends
// "id":null (#1520).
func TestStdioStatelessNotificationGetsNoResponse(t *testing.T) {
	for _, idMember := range []string{"", `"id":null,`} {
		srv := newTestMCPServer()
		var out bytes.Buffer
		in := strings.NewReader(`{"jsonrpc":"2.0",` + idMember + `"method":"notifications/whatever","params":{"_meta":{` +
			`"io.modelcontextprotocol/protocolVersion":"` + core.DraftProtocolVersion2026V1 + `",` +
			`"io.modelcontextprotocol/clientInfo":{"name":"c","version":"1"},` +
			`"io.modelcontextprotocol/clientCapabilities":{}}}}` + "\n")
		if err := srv.RunIO(context.Background(), in, &out); err != nil {
			t.Fatalf("RunIO: %v", err)
		}
		if out.Len() != 0 {
			t.Errorf("id member %q: server answered a notification: %s", idMember, out.String())
		}
	}
}

// connectAdaptive connects an adaptive client to h over HTTP.
func connectAdaptive(t *testing.T, h http.Handler) (*client.Client, error) {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "c", Version: "1"},
		client.WithClientMode(client.ClientModeAdaptive))
	t.Cleanup(func() { c.Close() })
	return c, c.Connect(context.Background())
}

func TestAdaptiveFallsBackFromSessionOnlyServer(t *testing.T) {
	legacy := newTestMCPServer().Handler(server.WithStreamableHTTP(true), server.WithStatelessMode(stateless.ModeLegacyOnly))

	// A session-only server that refuses session-less requests with a plain
	// 400 before method dispatch, the way go-sdk and the TypeScript SDK do.
	plain400 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var req struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		if req.Method == "server/discover" {
			http.Error(w, "Bad Request: No valid session ID provided", http.StatusBadRequest)
			return
		}
		legacy.ServeHTTP(w, r)
	})

	for _, tc := range []struct {
		name string
		h    http.Handler
	}{
		{"mcpkit legacy-only", legacy},
		{"plain 400 on server/discover", plain400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := connectAdaptive(t, tc.h)
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			if c.UsingStatelessWire() || c.SessionID() == "" {
				t.Errorf("UsingStatelessWire=%v SessionID=%q, want a session", c.UsingStatelessWire(), c.SessionID())
			}
			if _, err := c.ListTools(context.Background()); err != nil {
				t.Errorf("tools/list: %v", err)
			}
		})
	}
}

func TestAdaptiveProbeJSONRPC400IsFatal(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32020,"message":"header mismatch"}}`))
	})
	if _, err := connectAdaptive(t, h); err == nil || !strings.Contains(err.Error(), "header mismatch") {
		t.Errorf("Connect = %v, want the server's -32020 error", err)
	}
}
