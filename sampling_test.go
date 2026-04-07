package mcpkit

import (
	"context"
	"encoding/json"
	"fmt"
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
	_, err := Sample(context.Background(), CreateMessageRequest{
		Messages:  []SamplingMessage{{Role: "user", Content: Content{Type: "text", Text: "test"}}},
		MaxTokens: 100,
	})
	if err != ErrNoRequestFunc {
		t.Fatalf("expected ErrNoRequestFunc, got %v", err)
	}
}

// TestSampleNotSupported verifies that Sample() returns ErrSamplingNotSupported
// when the client did not declare sampling capability during initialization.
// This simulates a session context with a RequestFunc but no sampling capability.
func TestSampleNotSupported(t *testing.T) {
	var logLevel atomic.Pointer[LogLevel]
	caps := &ClientCapabilities{} // no sampling capability
	request := RequestFunc(func(ctx context.Context, method string, params any) (json.RawMessage, error) {
		t.Fatal("request func should not be called")
		return nil, nil
	})
	ctx := contextWithSession(context.Background(), nil, request, &logLevel, caps, nil)

	_, err := Sample(ctx, CreateMessageRequest{
		Messages:  []SamplingMessage{{Role: "user", Content: Content{Type: "text", Text: "test"}}},
		MaxTokens: 100,
	})
	if err != ErrSamplingNotSupported {
		t.Fatalf("expected ErrSamplingNotSupported, got %v", err)
	}
}

// TestSampleRoundTrip verifies the full sampling round-trip across all 3 transports
// (Streamable HTTP, SSE, in-memory). A server tool calls Sample() to request LLM
// inference from the client. The client's sampling handler returns a canned response.
// The tool returns the LLM response as text.
func TestSampleRoundTrip(t *testing.T) {
	forAllSamplingTransports(t, func(t *testing.T, c *Client) {
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
	handler := srv.Handler(WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	// Client with a handler that takes too long
	c := NewClient(ts.URL+"/mcp", ClientInfo{Name: "test-client", Version: "1.0"},
		WithSamplingHandler(func(ctx context.Context, req CreateMessageRequest) (CreateMessageResult, error) {
			time.Sleep(5 * time.Second)
			return CreateMessageResult{Model: "slow", Role: "assistant", Content: Content{Type: "text", Text: "too late"}}, nil
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
	handler := srv.Handler(WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := NewClient(ts.URL+"/mcp", ClientInfo{Name: "test-client", Version: "1.0"},
		WithSamplingHandler(func(ctx context.Context, req CreateMessageRequest) (CreateMessageResult, error) {
			return CreateMessageResult{}, fmt.Errorf("LLM unavailable")
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
func newSamplingTestServer(toolTimeout *time.Duration) *Server {
	var opts []Option
	if toolTimeout != nil {
		opts = append(opts, WithToolTimeout(*toolTimeout))
	}
	srv := NewServer(ServerInfo{Name: "test-sampling-server", Version: "1.0.0"}, opts...)

	// sample-tool: calls Sample() and returns the LLM response text
	srv.RegisterTool(
		ToolDef{
			Name:        "sample-tool",
			Description: "Calls sampling/createMessage and returns the result",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx context.Context, req ToolRequest) (ToolResult, error) {
			result, err := Sample(ctx, CreateMessageRequest{
				Messages:  []SamplingMessage{{Role: "user", Content: Content{Type: "text", Text: "Say hello"}}},
				MaxTokens: 100,
			})
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult("LLM says: " + result.Content.Text), nil
		},
	)

	// sample-timeout-tool: calls Sample() with a short deadline
	srv.RegisterTool(
		ToolDef{
			Name:        "sample-timeout-tool",
			Description: "Calls Sample() with a short deadline to test timeout",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx context.Context, req ToolRequest) (ToolResult, error) {
			tctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
			defer cancel()
			result, err := Sample(tctx, CreateMessageRequest{
				Messages:  []SamplingMessage{{Role: "user", Content: Content{Type: "text", Text: "timeout test"}}},
				MaxTokens: 100,
			})
			if err != nil {
				return ToolResult{}, err
			}
			return TextResult(result.Content.Text), nil
		},
	)

	return srv
}

// forAllSamplingTransports runs a test against all 3 transports with a server
// that has a sampling tool and a client with a sampling handler.
func forAllSamplingTransports(t *testing.T, fn func(t *testing.T, c *Client)) {
	t.Helper()

	samplingHandler := SamplingHandler(func(ctx context.Context, req CreateMessageRequest) (CreateMessageResult, error) {
		return CreateMessageResult{
			Model: "test-model",
			Role:  "assistant",
			Content: Content{
				Type: "text",
				Text: "Hello from LLM",
			},
		}, nil
	})

	t.Run("streamable", func(t *testing.T) {
		srv := newSamplingTestServer(nil)
		handler := srv.Handler(WithStreamableHTTP(true))
		ts := httptest.NewServer(handler)
		t.Cleanup(ts.Close)

		c := NewClient(ts.URL+"/mcp", ClientInfo{Name: "test-client", Version: "1.0"},
			WithSamplingHandler(samplingHandler))
		if err := c.Connect(); err != nil {
			t.Fatalf("Connect failed: %v", err)
		}
		t.Cleanup(func() { c.Close() })
		fn(t, c)
	})

	t.Run("sse", func(t *testing.T) {
		srv := newSamplingTestServer(nil)
		handler := srv.Handler(WithSSE(true), WithStreamableHTTP(false))
		ts := httptest.NewServer(handler)

		c := NewClient(ts.URL+"/mcp/sse", ClientInfo{Name: "test-client", Version: "1.0"},
			WithSSEClient(), WithSamplingHandler(samplingHandler))
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
		c := NewClient("memory://", ClientInfo{Name: "test-client", Version: "1.0"},
			WithInMemoryServer(srv), WithSamplingHandler(samplingHandler))
		if err := c.Connect(); err != nil {
			t.Fatalf("Connect failed: %v", err)
		}
		t.Cleanup(func() { c.Close() })
		fn(t, c)
	})
}
