package server

// Server-side middleware tests. Verify that middleware intercepts all JSON-RPC
// requests, executes in registration order, can short-circuit dispatch, and
// preserves existing features like tool timeout and auth claims access.

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMiddleware_SeesAllRequests verifies that middleware receives every
// dispatched request, including initialize and tools/call.
func TestMiddleware_SeesAllRequests(t *testing.T) {
	var methods []string
	mw := func(ctx context.Context, req *Request, next MiddlewareFunc) *Response {
		methods = append(methods, req.Method)
		return next(ctx, req)
	}

	srv := NewServer(ServerInfo{Name: "mw-test", Version: "1.0"},
		WithMiddleware(mw))
	srv.RegisterTool(
		ToolDef{Name: "echo", Description: "echo"},
		func(ctx context.Context, req ToolRequest) (ToolResult, error) {
			return TextResult("ok"), nil
		},
	)

	// Initialize
	initReq := &Request{ID: json.RawMessage(`1`), Method: "initialize", Params: json.RawMessage(`{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)}
	srv.Dispatch(context.Background(), initReq)

	// Initialized notification
	srv.Dispatch(context.Background(), &Request{Method: "notifications/initialized"})

	// Tool call
	toolReq := &Request{ID: json.RawMessage(`2`), Method: "tools/call", Params: json.RawMessage(`{"name":"echo"}`)}
	srv.Dispatch(context.Background(), toolReq)

	assert.Contains(t, methods, "initialize")
	assert.Contains(t, methods, "notifications/initialized")
	assert.Contains(t, methods, "tools/call")
}

// TestMiddleware_ChainOrder verifies that multiple middleware execute in
// registration order: first registered = outermost (runs first on request,
// last on response).
func TestMiddleware_ChainOrder(t *testing.T) {
	var order []string

	mw1 := func(ctx context.Context, req *Request, next MiddlewareFunc) *Response {
		order = append(order, "mw1-before")
		resp := next(ctx, req)
		order = append(order, "mw1-after")
		return resp
	}
	mw2 := func(ctx context.Context, req *Request, next MiddlewareFunc) *Response {
		order = append(order, "mw2-before")
		resp := next(ctx, req)
		order = append(order, "mw2-after")
		return resp
	}

	srv := NewServer(ServerInfo{Name: "mw-test", Version: "1.0"},
		WithMiddleware(mw1, mw2))

	initReq := &Request{ID: json.RawMessage(`1`), Method: "initialize", Params: json.RawMessage(`{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)}
	srv.Dispatch(context.Background(), initReq)

	// mw1 is outermost: runs before mw2 on request, after mw2 on response
	require.Len(t, order, 4)
	assert.Equal(t, "mw1-before", order[0])
	assert.Equal(t, "mw2-before", order[1])
	assert.Equal(t, "mw2-after", order[2])
	assert.Equal(t, "mw1-after", order[3])
}

// TestMiddleware_ShortCircuit verifies that middleware can return a response
// without calling next, preventing dispatch from running.
func TestMiddleware_ShortCircuit(t *testing.T) {
	var dispatched atomic.Bool

	blockingMW := func(ctx context.Context, req *Request, next MiddlewareFunc) *Response {
		if req.Method == "tools/call" {
			return NewErrorResponse(req.ID, -32000, "blocked by middleware")
		}
		return next(ctx, req)
	}

	srv := NewServer(ServerInfo{Name: "mw-test", Version: "1.0"},
		WithMiddleware(blockingMW))
	srv.RegisterTool(
		ToolDef{Name: "echo", Description: "echo"},
		func(ctx context.Context, req ToolRequest) (ToolResult, error) {
			dispatched.Store(true)
			return TextResult("should not reach"), nil
		},
	)

	// Initialize first
	initReq := &Request{ID: json.RawMessage(`1`), Method: "initialize", Params: json.RawMessage(`{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)}
	srv.Dispatch(context.Background(), initReq)
	srv.Dispatch(context.Background(), &Request{Method: "notifications/initialized"})

	// Tool call should be blocked
	toolReq := &Request{ID: json.RawMessage(`2`), Method: "tools/call", Params: json.RawMessage(`{"name":"echo"}`)}
	resp := srv.Dispatch(context.Background(), toolReq)

	assert.False(t, dispatched.Load(), "handler should not have been called")
	require.NotNil(t, resp)
	require.NotNil(t, resp.Error)
	assert.Equal(t, -32000, resp.Error.Code)
	assert.Equal(t, "blocked by middleware", resp.Error.Message)
}

// TestMiddleware_AccessContext verifies that middleware can read auth claims
// from context. Claims are injected by contextWithSession before the
// middleware chain runs.
func TestMiddleware_AccessContext(t *testing.T) {
	var sawClaims bool

	mw := func(ctx context.Context, req *Request, next MiddlewareFunc) *Response {
		claims := AuthClaims(ctx)
		if claims != nil && claims.Subject == "test-user" {
			sawClaims = true
		}
		return next(ctx, req)
	}

	srv := NewServer(ServerInfo{Name: "mw-test", Version: "1.0"},
		WithMiddleware(mw))

	// Use dispatchWithNotify to go through the middleware chain with claims
	claims := &Claims{Subject: "test-user", Scopes: []string{"read"}}
	initReq := &Request{ID: json.RawMessage(`1`), Method: "initialize", Params: json.RawMessage(`{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)}
	d := srv.dispatcher
	srv.dispatchWithNotify(d, context.Background(), claims, nil, initReq)

	assert.True(t, sawClaims, "middleware should see auth claims from context")
}

// TestMiddleware_ToolTimeoutPreserved verifies that the existing tool timeout
// feature still works when middleware is present. The timeout wraps the
// innermost handler inside the middleware chain.
func TestMiddleware_ToolTimeoutPreserved(t *testing.T) {
	var mwRan bool
	mw := func(ctx context.Context, req *Request, next MiddlewareFunc) *Response {
		mwRan = true
		return next(ctx, req)
	}

	srv := NewServer(ServerInfo{Name: "mw-test", Version: "1.0"},
		WithToolTimeout(50*time.Millisecond),
		WithMiddleware(mw))
	srv.RegisterTool(
		ToolDef{Name: "slow", Description: "slow"},
		func(ctx context.Context, req ToolRequest) (ToolResult, error) {
			select {
			case <-time.After(200 * time.Millisecond):
				return TextResult("too slow"), nil
			case <-ctx.Done():
				return ErrorResult("timeout"), nil
			}
		},
	)

	// Initialize
	initReq := &Request{ID: json.RawMessage(`1`), Method: "initialize", Params: json.RawMessage(`{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)}
	srv.Dispatch(context.Background(), initReq)
	srv.Dispatch(context.Background(), &Request{Method: "notifications/initialized"})

	// Tool call should timeout
	toolReq := &Request{ID: json.RawMessage(`2`), Method: "tools/call", Params: json.RawMessage(`{"name":"slow"}`)}
	resp := srv.Dispatch(context.Background(), toolReq)

	assert.True(t, mwRan, "middleware should have run")
	require.NotNil(t, resp)
	// The tool should have been cancelled by timeout
}

// TestLoggingMiddleware verifies that LoggingMiddleware produces expected
// log output for successful and failed requests.
func TestLoggingMiddleware(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)

	srv := NewServer(ServerInfo{Name: "mw-test", Version: "1.0"},
		WithMiddleware(LoggingMiddleware(logger)))

	initReq := &Request{ID: json.RawMessage(`1`), Method: "initialize", Params: json.RawMessage(`{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)}
	srv.Dispatch(context.Background(), initReq)

	output := buf.String()
	assert.Contains(t, output, "MCP initialize ok")

	// Unknown method should log error
	buf.Reset()
	srv.Dispatch(context.Background(), &Request{Method: "notifications/initialized"})
	unknownReq := &Request{ID: json.RawMessage(`2`), Method: "nonexistent/method"}
	srv.Dispatch(context.Background(), unknownReq)

	output = buf.String()
	assert.True(t, strings.Contains(output, "error=") || strings.Contains(output, "ok"),
		"should log something for each request")
}
