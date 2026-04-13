package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	core "github.com/panyam/mcpkit/core"
)

// initServer performs the full MCP initialization handshake on a server
// (initialize + notifications/initialized) so subsequent tool calls are accepted.
// Note: Cannot use testutil.InitHandshake due to import cycle (package server
// tests cannot import testutil which imports server).
func initServer(srv *Server) {
	srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`0`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})
	srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})
}

func TestServerDispatch(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "test", Version: "0.1.0"})
	srv.RegisterTool(
		core.ToolDef{Name: "greet", Description: "say hi"},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("hi"), nil
		},
	)
	initServer(srv)

	resp := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"greet","arguments":{}}`),
	})

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	var result core.ToolResult
	json.Unmarshal(resp.Result, &result)
	if result.Content[0].Text != "hi" {
		t.Errorf("got %q, want hi", result.Content[0].Text)
	}
}

func TestServerToolTimeout(t *testing.T) {
	srv := NewServer(
		core.ServerInfo{Name: "test", Version: "0.1.0"},
		WithToolTimeout(50*time.Millisecond),
	)
	srv.RegisterTool(
		core.ToolDef{Name: "slow", Description: "blocks"},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			select {
			case <-ctx.Done():
				return core.ErrorResult("timeout: " + ctx.Err().Error()), nil
			case <-time.After(5 * time.Second):
				return core.TextResult("done"), nil
			}
		},
	)
	initServer(srv)

	resp := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"slow","arguments":{}}`),
	})

	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %v", resp.Error)
	}
	var result core.ToolResult
	json.Unmarshal(resp.Result, &result)
	if !result.IsError {
		t.Error("expected tool result to be marked as error")
	}
}

func TestBearerTokenValidatorConstantTime(t *testing.T) {
	srv := NewServer(
		core.ServerInfo{Name: "test", Version: "0.1.0"},
		WithBearerToken("secret-token"),
	)

	tests := []struct {
		name    string
		auth    string
		wantErr bool
	}{
		{"valid", "Bearer secret-token", false},
		{"wrong token", "Bearer wrong", true},
		{"no header", "", true},
		{"no bearer prefix", "Basic secret-token", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.auth != "" {
				r.Header.Set("Authorization", tt.auth)
			}
			_, err := srv.CheckAuth(r)
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestNoAuthConfigured(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "test", Version: "0.1.0"})
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, err := srv.CheckAuth(r); err != nil {
		t.Errorf("no auth configured should pass, got: %v", err)
	}
}
