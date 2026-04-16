package server

import (
	"context"
	"encoding/json"
	"net/http"

	core "github.com/panyam/mcpkit/core"
)

// MethodHandler handles a custom JSON-RPC method registered via
// WithMethodHandler or Server.HandleMethod. The handler receives the full
// MCP session context (EmitLog, Sample, Elicit, AuthClaims, etc.), the
// request ID, and raw JSON params.
//
// Return a *core.Response with the result or error. Use core.NewResponse
// for success, core.NewErrorResponse for errors.
//
// Custom methods participate in middleware and require initialization
// (they're dispatched after the "initialized" gate, same as tools/resources).
type MethodHandler func(ctx context.Context, id json.RawMessage, params json.RawMessage) *core.Response

// builtinMethods lists all MCP spec methods that cannot be overridden by
// custom handlers. Attempting to register a handler for these panics.
var builtinMethods = map[string]bool{
	"initialize":                   true,
	"notifications/initialized":    true,
	"initialized":                  true,
	"notifications/cancelled":      true,
	"notifications/roots/list_changed": true,
	"ping":                         true,
	"tools/list":                   true,
	"tools/call":                   true,
	"resources/list":               true,
	"resources/read":               true,
	"resources/templates/list":     true,
	"resources/subscribe":          true,
	"resources/unsubscribe":        true,
	"prompts/list":                 true,
	"prompts/get":                  true,
	"logging/setLevel":             true,
	"completion/complete":          true,
}

// WithMethodHandler registers a handler for a custom JSON-RPC method.
// Custom methods are dispatched after initialization (same as tools/resources)
// and participate in server middleware.
//
// Panics if the method name is a built-in MCP spec method.
//
// Example:
//
//	srv := server.NewServer(info,
//	    server.WithMethodHandler("events/poll", func(ctx context.Context, id json.RawMessage, params json.RawMessage) *core.Response {
//	        return core.NewResponse(id, map[string]any{"events": []any{}})
//	    }),
//	)
// WithHTTPHandler registers a custom HTTP handler on the server's mux.
// Use this for endpoints that need raw HTTP access (SSE streams, webhooks,
// file uploads) rather than JSON-RPC request/response.
//
// The pattern follows http.ServeMux conventions (e.g., "/events/stream",
// "GET /health"). The handler runs alongside MCP transport handlers on
// the same server.
//
// Example:
//
//	srv := server.NewServer(info,
//	    server.WithHTTPHandler("GET /events/stream", sseStreamHandler),
//	    server.WithHTTPHandler("/webhooks/", webhookHandler),
//	)
func WithHTTPHandler(pattern string, h http.Handler) Option {
	return func(o *serverOptions) {
		o.httpHandlers = append(o.httpHandlers, httpHandlerEntry{pattern, h})
	}
}

func WithMethodHandler(method string, h MethodHandler) Option {
	if builtinMethods[method] {
		panic("mcpkit: cannot override built-in MCP method: " + method)
	}
	return func(o *serverOptions) {
		if o.customHandlers == nil {
			o.customHandlers = make(map[string]MethodHandler)
		}
		o.customHandlers[method] = h
	}
}
