package server

// Server-side middleware for intercepting JSON-RPC requests before and after
// dispatch. Middleware runs after auth (claims are in context) but before
// method routing. Use for tracing, metrics, audit logging, rate limiting,
// or custom per-method authorization.

import (
	"context"
	"encoding/json"
	"log"
	"time"

	core "github.com/panyam/mcpkit/core"
)

// Middleware intercepts a JSON-RPC request. Call next to continue the chain,
// or return a *core.Response directly to short-circuit (e.g., reject a request).
//
// Middleware sees the full request (method, params, ID) and the context
// (which includes auth claims via core.AuthClaims(ctx) and session notification
// state). The response from next can be inspected or modified before returning.
//
// Example — logging middleware:
//
//	mcpkit.WithMiddleware(mcpkit.LoggingMiddleware(logger))
//
// Example — per-method rate limiting:
//
//	func RateLimitMiddleware(limiter *rate.Limiter) mcpkit.Middleware {
//	    return func(ctx context.Context, req *mcpkit.Request, next mcpkit.MiddlewareFunc) *mcpkit.Response {
//	        if !limiter.Allow() {
//	            return mcpkit.NewErrorResponse(req.ID, -32000, "rate limit exceeded")
//	        }
//	        return next(ctx, req)
//	    }
//	}
type Middleware func(ctx context.Context, req *core.Request, next MiddlewareFunc) *core.Response

// MiddlewareFunc is the signature for the next handler in the middleware chain.
type MiddlewareFunc func(context.Context, *core.Request) *core.Response

// WithMiddleware registers server-side middleware that intercepts all JSON-RPC
// requests. Middleware executes in registration order: the first registered
// middleware is the outermost (runs first on request, last on response).
//
// Middleware runs after auth checks (claims are in context) but before method
// routing and dispatch.
func WithMiddleware(mw ...Middleware) Option {
	return func(o *serverOptions) {
		o.middleware = append(o.middleware, mw...)
	}
}

// --- Sending Middleware (outgoing server-to-client messages) ---

// NotifyInterceptor wraps outgoing server-to-client notifications before they
// reach the transport. Interceptors see the method and params of every
// notification (logging, progress, resource updates, custom). Call next to
// continue the chain, or return without calling next to suppress.
//
// Example — log all outgoing notifications:
//
//	server.WithNotifyInterceptor(func(method string, params any, next core.NotifyFunc) {
//	    log.Printf("→ notify %s", method)
//	    next(method, params)
//	})
type NotifyInterceptor func(method string, params any, next core.NotifyFunc)

// RequestInterceptor wraps outgoing server-to-client requests (sampling,
// elicitation) before they reach the transport. Call next to continue the
// chain, or return an error to reject the request.
//
// Example — log sampling requests:
//
//	server.WithRequestInterceptor(func(ctx context.Context, method string, params any, next core.RequestFunc) (json.RawMessage, error) {
//	    log.Printf("→ request %s", method)
//	    return next(ctx, method, params)
//	})
type RequestInterceptor func(ctx context.Context, method string, params any, next core.RequestFunc) (json.RawMessage, error)

// WithNotifyInterceptor registers interceptors for outgoing notifications.
// Interceptors execute in registration order (first = outermost).
func WithNotifyInterceptor(fn ...NotifyInterceptor) Option {
	return func(o *serverOptions) {
		o.notifyInterceptors = append(o.notifyInterceptors, fn...)
	}
}

// WithRequestInterceptor registers interceptors for outgoing server-to-client
// requests (sampling, elicitation). Interceptors execute in registration order.
func WithRequestInterceptor(fn ...RequestInterceptor) Option {
	return func(o *serverOptions) {
		o.requestInterceptors = append(o.requestInterceptors, fn...)
	}
}

// LoggingMiddleware logs every JSON-RPC request with method name, latency,
// and error status. Useful for debugging and operational monitoring.
//
// Example output:
//
//	MCP initialize ok [1.2ms]
//	MCP tools/call ok [45.3ms]
//	MCP tools/call error=-32602 (invalid params) [0.1ms]
func LoggingMiddleware(logger *log.Logger) Middleware {
	return func(ctx context.Context, req *core.Request, next MiddlewareFunc) *core.Response {
		start := time.Now()
		resp := next(ctx, req)
		elapsed := time.Since(start)

		if resp != nil && resp.Error != nil {
			logger.Printf("MCP %s error=%d (%s) [%s]",
				req.Method, resp.Error.Code, resp.Error.Message, elapsed)
		} else {
			logger.Printf("MCP %s ok [%s]", req.Method, elapsed)
		}
		return resp
	}
}

// ToolCallLogger logs tools/call requests with the tool name and isError status.
// Complements LoggingMiddleware: that one only sees JSON-RPC errors, while this
// one also surfaces "in-stream" tool errors — tool results with isError: true
// (returned at the JSON-RPC layer as success). Useful for visibility into
// authorization denials, scope failures, and other tool-level error signals.
//
// Non-tools/call requests pass through without logging.
//
// Example output:
//
//	tool=read_document ok
//	tool=update_document isError=true text="{\"error\":\"insufficient_scope\",..."
//	tool=initiate_payment isError=true text="..."
func ToolCallLogger(logger *log.Logger) Middleware {
	return func(ctx context.Context, req *core.Request, next MiddlewareFunc) *core.Response {
		if req.Method != "tools/call" {
			return next(ctx, req)
		}

		var params struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(req.Params, &params)

		resp := next(ctx, req)

		if resp == nil {
			return resp
		}
		if resp.Error != nil {
			logger.Printf("tool=%s rpcError=%d msg=%q",
				params.Name, resp.Error.Code, resp.Error.Message)
			return resp
		}

		// Result is `any` — marshal to bytes, then parse as ToolResult.
		raw, _ := json.Marshal(resp.Result)
		var result core.ToolResult
		_ = json.Unmarshal(raw, &result)

		if result.IsError {
			snippet := ""
			if len(result.Content) > 0 {
				snippet = result.Content[0].Text
				if len(snippet) > 80 {
					snippet = snippet[:80] + "..."
				}
			}
			logger.Printf("tool=%s isError=true text=%q", params.Name, snippet)
		} else {
			logger.Printf("tool=%s ok", params.Name)
		}
		return resp
	}
}
