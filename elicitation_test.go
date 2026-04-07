package mcpkit

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestElicitNoContext verifies that Elicit() returns ErrNoRequestFunc when called
// without a session context (e.g., outside a tool handler or with no transport).
func TestElicitNoContext(t *testing.T) {
	_, err := Elicit(context.Background(), ElicitationRequest{
		Message: "Pick a color",
	})
	if err != ErrNoRequestFunc {
		t.Fatalf("expected ErrNoRequestFunc, got %v", err)
	}
}

// TestElicitNotSupported verifies that Elicit() returns ErrElicitationNotSupported
// when the client did not declare elicitation capability during initialization.
func TestElicitNotSupported(t *testing.T) {
	var logLevel atomic.Pointer[LogLevel]
	caps := &ClientCapabilities{} // no elicitation capability
	request := RequestFunc(func(ctx context.Context, method string, params any) (json.RawMessage, error) {
		t.Fatal("request func should not be called")
		return nil, nil
	})
	ctx := ContextWithSession(context.Background(), nil, request, &logLevel, caps, nil)

	_, err := Elicit(ctx, ElicitationRequest{Message: "test"})
	if err != ErrElicitationNotSupported {
		t.Fatalf("expected ErrElicitationNotSupported, got %v", err)
	}
}

// TestElicitAccept verifies the full elicitation round-trip with action "accept"
// across all 3 transports. The server tool calls Elicit() to ask for user input,
// the client handler returns an "accept" response with content.
func TestElicitAccept(t *testing.T) {
	forAllElicitationTransports(t, "accept", map[string]any{"color": "blue"}, func(t *testing.T, c *Client) {
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
	forAllElicitationTransports(t, "decline", nil, func(t *testing.T, c *Client) {
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
	forAllElicitationTransports(t, "cancel", nil, func(t *testing.T, c *Client) {
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

// newElicitationTestServer creates a server with an "elicit-tool" that calls Elicit()
// and returns the user's response.
func newElicitationTestServer() *Server {
	srv := NewServer(ServerInfo{Name: "test-elicitation-server", Version: "1.0.0"})

	srv.RegisterTool(
		ToolDef{
			Name:        "elicit-tool",
			Description: "Calls elicitation/create and returns the user's response",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx context.Context, req ToolRequest) (ToolResult, error) {
			result, err := Elicit(ctx, ElicitationRequest{
				Message:         "Pick a color",
				RequestedSchema: json.RawMessage(`{"type":"object","properties":{"color":{"type":"string","enum":["red","green","blue"]}}}`),
			})
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			if result.Action == "accept" {
				color, _ := result.Content["color"].(string)
				return TextResult("User chose: " + color), nil
			}
			return TextResult("User action: " + result.Action), nil
		},
	)

	return srv
}

// forAllElicitationTransports runs a test against all 3 transports with a server
// that has an elicitation tool and a client with an elicitation handler that
// returns the specified action and content.
func forAllElicitationTransports(t *testing.T, action string, content map[string]any, fn func(t *testing.T, c *Client)) {
	t.Helper()

	handler := ElicitationHandler(func(ctx context.Context, req ElicitationRequest) (ElicitationResult, error) {
		return ElicitationResult{
			Action:  action,
			Content: content,
		}, nil
	})

	t.Run("streamable", func(t *testing.T) {
		srv := newElicitationTestServer()
		h := srv.Handler(WithStreamableHTTP(true))
		ts := httptest.NewServer(h)
		t.Cleanup(ts.Close)

		c := NewClient(ts.URL+"/mcp", ClientInfo{Name: "test-client", Version: "1.0"},
			WithElicitationHandler(handler))
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

		c := NewClient(ts.URL+"/mcp/sse", ClientInfo{Name: "test-client", Version: "1.0"},
			WithSSEClient(), WithElicitationHandler(handler))
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
		c := NewClient("memory://", ClientInfo{Name: "test-client", Version: "1.0"},
			WithInMemoryServer(srv), WithElicitationHandler(handler))
		if err := c.Connect(); err != nil {
			t.Fatalf("Connect failed: %v", err)
		}
		t.Cleanup(func() { c.Close() })
		fn(t, c)
	})
}
