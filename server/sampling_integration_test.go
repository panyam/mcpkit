package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	client "github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	server "github.com/panyam/mcpkit/server"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Reference json and fmt to keep imports.
var (
	_ json.RawMessage
	_ = fmt.Sprintf
)

// TestSampleNoContext verifies that Sample() returns ErrNoRequestFunc when called
// without a session context (e.g., outside a tool handler or with no transport).
func TestSampleNoContext(t *testing.T) {
	_, err := core.Sample(context.Background(), core.CreateMessageRequest{
		Messages:  []core.SamplingMessage{{Role: "user", Content: core.Content{Type: "text", Text: "test"}}},
		MaxTokens: 100,
	})
	if err != core.ErrNoRequestFunc {
		t.Fatalf("expected ErrNoRequestFunc, got %v", err)
	}
}

// TestSampleNotSupported verifies that Sample() returns ErrSamplingNotSupported
// when the client did not declare sampling capability during initialization.
// This simulates a session context with a RequestFunc but no sampling capability.
func TestSampleNotSupported(t *testing.T) {
	var logLevel atomic.Pointer[core.LogLevel]
	caps := &core.ClientCapabilities{} // no sampling capability
	request := core.RequestFunc(func(ctx context.Context, method string, params any) (json.RawMessage, error) {
		t.Fatal("request func should not be called")
		return nil, nil
	})
	ctx := core.ContextWithSession(context.Background(), nil, request, &logLevel, caps, nil)

	_, err := core.Sample(ctx, core.CreateMessageRequest{
		Messages:  []core.SamplingMessage{{Role: "user", Content: core.Content{Type: "text", Text: "test"}}},
		MaxTokens: 100,
	})
	if err != core.ErrSamplingNotSupported {
		t.Fatalf("expected ErrSamplingNotSupported, got %v", err)
	}
}

// TestSampleRoundTrip verifies the full sampling round-trip across all 3 transports
// (Streamable HTTP, SSE, in-memory). A server tool calls Sample() to request LLM
// inference from the client. The client's sampling handler returns a canned response.
// The tool returns the LLM response as text.
func TestSampleRoundTrip(t *testing.T) {
	forAllSamplingTransports(t, func(t *testing.T, c *client.Client) {
		text, err := c.ToolCall("sample-tool", map[string]any{})
		if err != nil {
			t.Fatalf("ToolCall failed: %v", err)
		}
		if text != "LLM says: Hello from LLM" {
			t.Fatalf("unexpected result: %q", text)
		}
	})
}

// TestSampleTimeout verifies that Sample() returns context.DeadlineExceeded when
// the client takes too long to respond. Uses a slow sampling handler with a short
// tool timeout to trigger the deadline.
func TestSampleTimeout(t *testing.T) {
	srv := newSamplingTestServer(nil) // no tool timeout on server
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	// Client with a handler that takes longer than the tool's 200ms timeout.
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test-client", Version: "1.0"},
		client.WithSamplingHandler(func(ctx context.Context, req core.CreateMessageRequest) (core.CreateMessageResult, error) {
			time.Sleep(500 * time.Millisecond)
			return core.CreateMessageResult{Model: "slow", Role: "assistant", Content: core.Content{Type: "text", Text: "too late"}}, nil
		}),
	)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	// The tool call should timeout (the server's tool has a 200ms context timeout)
	_, err := c.ToolCall("sample-timeout-tool", map[string]any{})
	if err == nil {
		t.Fatal("expected error from timeout, got nil")
	}
}

// TestSampleClientError verifies that when the client's sampling handler returns
// an error, it propagates back to the Sample() call in the tool handler.
func TestSampleClientError(t *testing.T) {
	srv := newSamplingTestServer(nil)
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test-client", Version: "1.0"},
		client.WithSamplingHandler(func(ctx context.Context, req core.CreateMessageRequest) (core.CreateMessageResult, error) {
			return core.CreateMessageResult{}, fmt.Errorf("LLM unavailable")
		}),
	)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	_, err := c.ToolCall("sample-tool", map[string]any{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- Test helpers ---

// newSamplingTestServer creates a server with a "sample-tool" that calls Sample()
// and a "sample-timeout-tool" that uses a short context timeout.
func newSamplingTestServer(toolTimeout *time.Duration) *server.Server {
	var opts []server.Option
	if toolTimeout != nil {
		opts = append(opts, server.WithToolTimeout(*toolTimeout))
	}
	srv := server.NewServer(core.ServerInfo{Name: "test-sampling-server", Version: "1.0.0"}, opts...)

	// sample-tool: calls Sample() and returns the LLM response text
	srv.RegisterTool(
		core.ToolDef{
			Name:        "sample-tool",
			Description: "Calls sampling/createMessage and returns the result",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			result, err := core.Sample(ctx, core.CreateMessageRequest{
				Messages:  []core.SamplingMessage{{Role: "user", Content: core.Content{Type: "text", Text: "Say hello"}}},
				MaxTokens: 100,
			})
			if err != nil {
				return core.ErrorResult(err.Error()), nil
			}
			return core.TextResult("LLM says: " + result.Content.Text), nil
		},
	)

	// sample-timeout-tool: calls Sample() with a short deadline
	srv.RegisterTool(
		core.ToolDef{
			Name:        "sample-timeout-tool",
			Description: "Calls Sample() with a short deadline to test timeout",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			tctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
			defer cancel()
			result, err := core.Sample(tctx, core.CreateMessageRequest{
				Messages:  []core.SamplingMessage{{Role: "user", Content: core.Content{Type: "text", Text: "timeout test"}}},
				MaxTokens: 100,
			})
			if err != nil {
				return core.ToolResult{}, err
			}
			return core.TextResult(result.Content.Text), nil
		},
	)

	return srv
}

// forAllSamplingTransports runs a test against all 3 transports with a server
// that has a sampling tool and a client with a sampling handler.
func forAllSamplingTransports(t *testing.T, fn func(t *testing.T, c *client.Client)) {
	t.Helper()

	samplingHandler := client.SamplingHandler(func(ctx context.Context, req core.CreateMessageRequest) (core.CreateMessageResult, error) {
		return core.CreateMessageResult{
			Model: "test-model",
			Role:  "assistant",
			Content: core.Content{
				Type: "text",
				Text: "Hello from LLM",
			},
		}, nil
	})

	t.Run("streamable", func(t *testing.T) {
		srv := newSamplingTestServer(nil)
		handler := srv.Handler(server.WithStreamableHTTP(true))
		ts := httptest.NewServer(handler)
		t.Cleanup(ts.Close)

		c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test-client", Version: "1.0"},
			client.WithSamplingHandler(samplingHandler))
		if err := c.Connect(); err != nil {
			t.Fatalf("Connect failed: %v", err)
		}
		t.Cleanup(func() { c.Close() })
		fn(t, c)
	})

	t.Run("sse", func(t *testing.T) {
		srv := newSamplingTestServer(nil)
		handler := srv.Handler(server.WithSSE(true), server.WithStreamableHTTP(false))
		ts := httptest.NewServer(handler)

		c := client.NewClient(ts.URL+"/mcp/sse", core.ClientInfo{Name: "test-client", Version: "1.0"},
			client.WithSSEClient(), client.WithSamplingHandler(samplingHandler))
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
		srv := newSamplingTestServer(nil)
		// Build client first to get its HandleServerRequest, then wire to transport
		c := client.NewClient("memory://", core.ClientInfo{Name: "test-client", Version: "1.0"},
			client.WithSamplingHandler(samplingHandler))
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
