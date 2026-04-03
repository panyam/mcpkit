package mcpkit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// initServer performs the full MCP initialization handshake on a server
// (initialize + notifications/initialized) so subsequent tool calls are accepted.
func initServer(srv *Server) {
	srv.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`0`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})
	srv.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})
}

func TestServerDispatch(t *testing.T) {
	srv := NewServer(ServerInfo{Name: "test", Version: "0.1.0"})
	srv.RegisterTool(
		ToolDef{Name: "greet", Description: "say hi"},
		func(ctx context.Context, req ToolRequest) (ToolResult, error) {
			return TextResult("hi"), nil
		},
	)
	initServer(srv)

	resp := srv.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"greet","arguments":{}}`),
	})

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	var result ToolResult
	json.Unmarshal(resp.Result, &result)
	if result.Content[0].Text != "hi" {
		t.Errorf("got %q, want hi", result.Content[0].Text)
	}
}

func TestServerToolTimeout(t *testing.T) {
	srv := NewServer(
		ServerInfo{Name: "test", Version: "0.1.0"},
		WithToolTimeout(50*time.Millisecond),
	)
	srv.RegisterTool(
		ToolDef{Name: "slow", Description: "blocks"},
		func(ctx context.Context, req ToolRequest) (ToolResult, error) {
			select {
			case <-ctx.Done():
				return ErrorResult("timeout: " + ctx.Err().Error()), nil
			case <-time.After(5 * time.Second):
				return TextResult("done"), nil
			}
		},
	)
	initServer(srv)

	resp := srv.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"slow","arguments":{}}`),
	})

	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %v", resp.Error)
	}
	var result ToolResult
	json.Unmarshal(resp.Result, &result)
	if !result.IsError {
		t.Error("expected tool result to be marked as error")
	}
}

func TestBearerTokenValidatorConstantTime(t *testing.T) {
	srv := NewServer(
		ServerInfo{Name: "test", Version: "0.1.0"},
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
			err := srv.CheckAuth(r)
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
	srv := NewServer(ServerInfo{Name: "test", Version: "0.1.0"})
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if err := srv.CheckAuth(r); err != nil {
		t.Errorf("no auth configured should pass, got: %v", err)
	}
}
