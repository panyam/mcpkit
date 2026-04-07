package server_test

import (
	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/server"
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestElicitNoContext verifies that core.Elicit() returns core.ErrNoRequestFunc when called
// without a session context (e.g., outside a tool handler or with no transport).
func TestElicitNoContext(t *testing.T) {
	_, err := core.Elicit(context.Background(), core.ElicitationRequest{
		Message: "Pick a color",
	})
	if err != core.ErrNoRequestFunc {
		t.Fatalf("expected core.ErrNoRequestFunc, got %v", err)
	}
}

// TestElicitNotSupported verifies that core.Elicit() returns core.ErrElicitationNotSupported
// when the client did not declare elicitation capability during initialization.
func TestElicitNotSupported(t *testing.T) {
	var logLevel atomic.Pointer[core.LogLevel]
	caps := &core.ClientCapabilities{} // no elicitation capability
	request := core.RequestFunc(func(ctx context.Context, method string, params any) (json.RawMessage, error) {
		t.Fatal("request func should not be called")
		return nil, nil
	})
	ctx := core.ContextWithSession(context.Background(), nil, request, &logLevel, caps, nil)

	_, err := core.Elicit(ctx, core.ElicitationRequest{Message: "test"})
	if err != core.ErrElicitationNotSupported {
		t.Fatalf("expected core.ErrElicitationNotSupported, got %v", err)
	}
}

// TestElicitAccept verifies the full elicitation round-trip with action "accept"
// across all 3 transports. The server tool calls core.Elicit() to ask for user input,
// the client handler returns an "accept" response with content.
func TestElicitAccept(t *testing.T) {
	forAllElicitationTransports(t, "accept", map[string]any{"color": "blue"}, func(t *testing.T, c *client.Client) {
		text, err := c.ToolCall("elicit-tool", map[string]any{})
		if err != nil {
			t.Fatalf("ToolCall failed: %v", err)
		}
		if text != "User chose: blue" {
			t.Fatalf("unexpected result: %q", text)
		}
	})
}

// TestElicitDecline verifies elicitation round-trip when the user declines.
func TestElicitDecline(t *testing.T) {
	forAllElicitationTransports(t, "decline", nil, func(t *testing.T, c *client.Client) {
		text, err := c.ToolCall("elicit-tool", map[string]any{})
		if err != nil {
			t.Fatalf("ToolCall failed: %v", err)
		}
		if text != "User action: decline" {
			t.Fatalf("unexpected result: %q", text)
		}
	})
}

// TestElicitCancel verifies elicitation round-trip when the user cancels.
func TestElicitCancel(t *testing.T) {
	forAllElicitationTransports(t, "cancel", nil, func(t *testing.T, c *client.Client) {
		text, err := c.ToolCall("elicit-tool", map[string]any{})
		if err != nil {
			t.Fatalf("ToolCall failed: %v", err)
		}
		if text != "User action: cancel" {
			t.Fatalf("unexpected result: %q", text)
		}
	})
}

// --- Test helpers ---

// newElicitationTestServer creates a server with an "elicit-tool" that calls core.Elicit()
// and returns the user's response.
func newElicitationTestServer() *server.Server {
	srv := server.NewServer(core.ServerInfo{Name: "test-elicitation-server", Version: "1.0.0"})

	srv.RegisterTool(
		core.ToolDef{
			Name:        "elicit-tool",
			Description: "Calls elicitation/create and returns the user's response",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			result, err := core.Elicit(ctx, core.ElicitationRequest{
				Message:         "Pick a color",
				RequestedSchema: json.RawMessage(`{"type":"object","properties":{"color":{"type":"string","enum":["red","green","blue"]}}}`),
			})
			if err != nil {
				return core.ErrorResult(err.Error()), nil
			}
			if result.Action == "accept" {
				color, _ := result.Content["color"].(string)
				return core.TextResult("User chose: " + color), nil
			}
			return core.TextResult("User action: " + result.Action), nil
		},
	)

	return srv
}

// forAllElicitationTransports runs a test against all 3 transports with a server
// that has an elicitation tool and a client with an elicitation handler that
// returns the specified action and content.
func forAllElicitationTransports(t *testing.T, action string, content map[string]any, fn func(t *testing.T, c *client.Client)) {
	t.Helper()

	handler := client.ElicitationHandler(func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
		return core.ElicitationResult{
			Action:  action,
			Content: content,
		}, nil
	})

	t.Run("streamable", func(t *testing.T) {
		srv := newElicitationTestServer()
		h := srv.Handler(WithStreamableHTTP(true))
		ts := httptest.NewServer(h)
		t.Cleanup(ts.Close)

		c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test-client", Version: "1.0"},
			client.WithElicitationHandler(handler))
		if err := c.Connect(); err != nil {
			t.Fatalf("Connect failed: %v", err)
		}
		t.Cleanup(func() { c.Close() })
		fn(t, c)
	})

	t.Run("sse", func(t *testing.T) {
		srv := newElicitationTestServer()
		h := srv.Handler(WithSSE(true), WithStreamableHTTP(false))
		ts := httptest.NewServer(h)

		c := client.NewClient(ts.URL+"/mcp/sse", core.ClientInfo{Name: "test-client", Version: "1.0"},
			client.WithSSEClient(), client.WithElicitationHandler(handler))
		if err := c.Connect(); err != nil {
			ts.Close()
			t.Fatalf("SSE Connect failed: %v", err)
		}
		t.Cleanup(func() {
			c.Close()
			ts.Close()
		})
		fn(t, c)
	})

	t.Run("memory", func(t *testing.T) {
		srv := newElicitationTestServer()
		c := client.NewClient("memory://", core.ClientInfo{Name: "test-client", Version: "1.0"},
			client.WithInMemoryServer(srv), client.WithElicitationHandler(handler))
		if err := c.Connect(); err != nil {
			t.Fatalf("Connect failed: %v", err)
		}
		t.Cleanup(func() { c.Close() })
		fn(t, c)
	})
}
