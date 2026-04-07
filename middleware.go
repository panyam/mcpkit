package mcpkit

// Server-side middleware for intercepting JSON-RPC requests before and after
// dispatch. Middleware runs after auth (claims are in context) but before
// method routing. Use for tracing, metrics, audit logging, rate limiting,
// or custom per-method authorization.

import (
	"context"
	"log"
	"time"
)

// Middleware intercepts a JSON-RPC request. Call next to continue the chain,
// or return a *Response directly to short-circuit (e.g., reject a request).
//
// Middleware sees the full request (method, params, ID) and the context
// (which includes auth claims via AuthClaims(ctx) and session notification
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
type Middleware func(ctx context.Context, req *Request, next MiddlewareFunc) *Response

// MiddlewareFunc is the signature for the next handler in the middleware chain.
type MiddlewareFunc func(context.Context, *Request) *Response

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

// LoggingMiddleware logs every JSON-RPC request with method name, latency,
// and error status. Useful for debugging and operational monitoring.
//
// Example output:
//
//	MCP initialize ok [1.2ms]
//	MCP tools/call ok [45.3ms]
//	MCP tools/call error=-32602 (invalid params) [0.1ms]
func LoggingMiddleware(logger *log.Logger) Middleware {
	return func(ctx context.Context, req *Request, next MiddlewareFunc) *Response {
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
