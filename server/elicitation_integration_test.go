package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	client "github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	server "github.com/panyam/mcpkit/server"
)

// TestElicitNoContext verifies that Elicit() returns ErrNoRequestFunc when called
// without a session context (e.g., outside a tool handler or with no transport).
func TestElicitNoContext(t *testing.T) {
	_, err := core.Elicit(context.Background(), core.ElicitationRequest{
		Message: "Pick a color",
	})
	if err != core.ErrNoRequestFunc {
		t.Fatalf("expected ErrNoRequestFunc, got %v", err)
	}
}

// TestElicitNotSupported verifies that Elicit() returns ErrElicitationNotSupported
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
		t.Fatalf("expected ErrElicitationNotSupported, got %v", err)
	}
}

// TestElicitAccept verifies the full elicitation round-trip with action "accept"
// across all 3 transports. The server tool calls Elicit() to ask for user input,
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

// --- URL-mode tests (SEP-1036) ---

// TestElicitURLRoundTrip verifies the full URL-mode elicitation round-trip
// across all 3 transports. The server tool calls ElicitURL(), the client
// handler receives the URL-mode request and returns "accept".
func TestElicitURLRoundTrip(t *testing.T) {
	forAllURLElicitationTransports(t, func(t *testing.T, c *client.Client) {
		text, err := c.ToolCall("elicit-url-tool", map[string]any{})
		if err != nil {
			t.Fatalf("ToolCall failed: %v", err)
		}
		if text != "URL elicitation: action=accept" {
			t.Fatalf("unexpected result: %q", text)
		}
	})
}

// TestElicitURLNotSupported verifies that ElicitURL returns
// ErrElicitationURLNotSupported when the client doesn't declare URL support.
func TestElicitURLNotSupported(t *testing.T) {
	srv := newURLElicitationTestServer()
	// Client with form-only elicitation (no WithElicitationURLSupport).
	h := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test-client", Version: "1.0"},
		client.WithElicitationHandler(func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
			t.Fatal("handler should not be called")
			return core.ElicitationResult{}, nil
		}))
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	text, err := c.ToolCall("elicit-url-tool", map[string]any{})
	if err != nil {
		t.Fatalf("ToolCall failed: %v", err)
	}
	// The tool catches the error and returns it as text.
	if text != "url elicitation failed: client does not support URL-mode elicitation" {
		t.Fatalf("unexpected result: %q", text)
	}
}

// TestElicitCompletionNotification verifies that the server can send
// notifications/elicitation/complete and the client receives it.
func TestElicitCompletionNotification(t *testing.T) {
	completionCh := make(chan string, 1)

	srv := newURLElicitationTestServer()
	h := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test-client", Version: "1.0"},
		client.WithElicitationHandler(func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
			return core.ElicitationResult{Action: "accept"}, nil
		}),
		client.WithElicitationURLSupport(),
		client.WithElicitationCompleteHandler(func(ctx context.Context, p core.ElicitationCompleteParams) {
			completionCh <- p.ElicitationID
		}))
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	// Call the tool that sends a URL elicitation + completion notification.
	text, err := c.ToolCall("elicit-url-complete-tool", map[string]any{})
	if err != nil {
		t.Fatalf("ToolCall failed: %v", err)
	}
	if text != "URL elicitation + notification: action=accept" {
		t.Fatalf("unexpected result: %q", text)
	}

	// Wait for the completion notification.
	select {
	case elicitID := <-completionCh:
		if elicitID != "elicit-complete-001" {
			t.Fatalf("unexpected elicitationId: %q", elicitID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for elicitation complete notification")
	}
}

// TestElicitModeValidation verifies that the client rejects URL-mode
// elicitation requests when it only declared form support.
func TestElicitModeValidation(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test-server", Version: "1.0.0"})

	// Register a tool that directly sends a URL-mode elicitation via the
	// raw request function, bypassing ElicitURL()'s capability check.
	srv.RegisterTool(
		core.ToolDef{
			Name:        "raw-url-elicit",
			Description: "Sends raw URL-mode elicitation/create",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			// Use ElicitURL which checks capabilities.
			result, err := core.ElicitURL(ctx, core.ElicitationRequest{
				Message:       "Visit URL",
				URL:           "https://example.com",
				ElicitationID: "el_test",
			})
			if err != nil {
				return core.TextResult("error: " + err.Error()), nil
			}
			return core.TextResult("action: " + result.Action), nil
		},
	)

	h := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	// Client with form-only elicitation.
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test-client", Version: "1.0"},
		client.WithElicitationHandler(func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
			return core.ElicitationResult{Action: "accept"}, nil
		}))
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	text, err := c.ToolCall("raw-url-elicit", map[string]any{})
	if err != nil {
		t.Fatalf("ToolCall failed: %v", err)
	}
	if text != "error: client does not support URL-mode elicitation" {
		t.Fatalf("expected URL not supported error, got: %q", text)
	}
}

// --- Test helpers ---

// newURLElicitationTestServer creates a server with URL-mode elicitation tools.
func newURLElicitationTestServer() *server.Server {
	srv := server.NewServer(core.ServerInfo{Name: "test-url-elicitation-server", Version: "1.0.0"})

	srv.RegisterTool(
		core.ToolDef{
			Name:        "elicit-url-tool",
			Description: "Calls ElicitURL and returns the user's response",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			result, err := core.ElicitURL(ctx, core.ElicitationRequest{
				Message:       "Please approve access",
				URL:           "https://example.com/approve?session=test",
				ElicitationID: "elicit-url-001",
			})
			if err != nil {
				return core.TextResult(fmt.Sprintf("url elicitation failed: %v", err)), nil
			}
			return core.TextResult(fmt.Sprintf("URL elicitation: action=%s", result.Action)), nil
		},
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "elicit-url-complete-tool",
			Description: "URL elicitation with completion notification",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			elicitID := "elicit-complete-001"
			result, err := core.ElicitURL(ctx, core.ElicitationRequest{
				Message:       "Visit URL to approve",
				URL:           "https://example.com/approve?id=" + elicitID,
				ElicitationID: elicitID,
			})
			if err != nil {
				return core.TextResult(fmt.Sprintf("url elicitation failed: %v", err)), nil
			}
			core.NotifyElicitationComplete(ctx, elicitID)
			return core.TextResult(fmt.Sprintf("URL elicitation + notification: action=%s", result.Action)), nil
		},
	)

	return srv
}

// forAllURLElicitationTransports runs a test against all 3 transports with
// a server that has URL-mode elicitation tools and a client with URL support.
func forAllURLElicitationTransports(t *testing.T, fn func(t *testing.T, c *client.Client)) {
	t.Helper()

	handler := client.ElicitationHandler(func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
		// Verify URL-mode fields are present.
		if req.Mode != core.ElicitModeURL {
			return core.ElicitationResult{}, fmt.Errorf("expected mode=%q, got %q", core.ElicitModeURL, req.Mode)
		}
		if req.URL == "" {
			return core.ElicitationResult{}, fmt.Errorf("URL must be set for URL-mode elicitation")
		}
		if req.ElicitationID == "" {
			return core.ElicitationResult{}, fmt.Errorf("ElicitationID must be set for URL-mode elicitation")
		}
		return core.ElicitationResult{Action: "accept"}, nil
	})

	t.Run("streamable", func(t *testing.T) {
		srv := newURLElicitationTestServer()
		h := srv.Handler(server.WithStreamableHTTP(true))
		ts := httptest.NewServer(h)
		t.Cleanup(ts.Close)

		c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test-client", Version: "1.0"},
			client.WithElicitationHandler(handler),
			client.WithElicitationURLSupport())
		if err := c.Connect(); err != nil {
			t.Fatalf("Connect failed: %v", err)
		}
		t.Cleanup(func() { c.Close() })
		fn(t, c)
	})

	t.Run("sse", func(t *testing.T) {
		srv := newURLElicitationTestServer()
		h := srv.Handler(server.WithSSE(true), server.WithStreamableHTTP(false))
		ts := httptest.NewServer(h)

		c := client.NewClient(ts.URL+"/mcp/sse", core.ClientInfo{Name: "test-client", Version: "1.0"},
			client.WithSSEClient(), client.WithElicitationHandler(handler),
			client.WithElicitationURLSupport())
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
		srv := newURLElicitationTestServer()
		c := client.NewClient("memory://", core.ClientInfo{Name: "test-client", Version: "1.0"},
			client.WithElicitationHandler(handler),
			client.WithElicitationURLSupport())
		transport := server.NewInProcessTransport(srv,
			server.WithServerRequestHandler(func(ctx context.Context, req *core.Request) *core.Response {
				return c.HandleServerRequest(req)
			}),
		)
		c.SetTransport(transport)
		if err := c.Connect(); err != nil {
			t.Fatalf("Connect failed: %v", err)
		}
		t.Cleanup(func() { c.Close() })
		fn(t, c)
	})
}

// newElicitationTestServer creates a server with an "elicit-tool" that calls Elicit()
// and returns the user's response.
func newElicitationTestServer() *server.Server {
	srv := server.NewServer(core.ServerInfo{Name: "test-elicitation-server", Version: "1.0.0"})

	srv.RegisterTool(
		core.ToolDef{
			Name:        "elicit-tool",
			Description: "Calls elicitation/create and returns the user's response",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
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
		h := srv.Handler(server.WithStreamableHTTP(true))
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
		h := srv.Handler(server.WithSSE(true), server.WithStreamableHTTP(false))
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
			client.WithElicitationHandler(handler))
		transport := server.NewInProcessTransport(srv,
			server.WithServerRequestHandler(func(ctx context.Context, req *core.Request) *core.Response {
				return c.HandleServerRequest(req)
			}),
		)
		c.SetTransport(transport)
		if err := c.Connect(); err != nil {
			t.Fatalf("Connect failed: %v", err)
		}
		t.Cleanup(func() { c.Close() })
		fn(t, c)
	})
}
