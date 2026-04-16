package client

import "context"

// ClientMiddleware intercepts outgoing client calls before they reach the
// transport. Use for tracing, logging, metrics, or request transformation.
//
// The middleware sees the method name (e.g., "tools/call", "tools/list",
// "logging/setLevel") and typed params. Call next to continue the chain,
// or return directly to short-circuit (e.g., cached responses, circuit breakers).
//
// Example — tracing middleware:
//
//	client.WithClientMiddleware(func(ctx context.Context, method string, params any,
//	    next client.ClientCallFunc) (*client.CallResult, error) {
//	    start := time.Now()
//	    result, err := next(ctx, method, params)
//	    log.Printf("→ %s (%s)", method, time.Since(start))
//	    return result, err
//	})
type ClientMiddleware func(ctx context.Context, method string, params any,
	next ClientCallFunc) (*CallResult, error)

// ClientCallFunc is the signature for the next handler in the client middleware chain.
type ClientCallFunc func(ctx context.Context, method string, params any) (*CallResult, error)

// WithClientMiddleware registers middleware that wraps all outgoing client calls
// (ToolCall, ListTools, ReadResource, etc.). Middleware executes in registration
// order: first registered = outermost (runs first on request, last on response).
func WithClientMiddleware(mw ...ClientMiddleware) ClientOption {
	return func(c *Client) {
		c.callMiddleware = append(c.callMiddleware, mw...)
	}
}
