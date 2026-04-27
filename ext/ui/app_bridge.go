package ui

import (
	"context"
	"encoding/json"

	"github.com/panyam/mcpkit/core"
)

// AppBridge abstracts the bidirectional communication channel between the host
// (AppHost) and the app (iframe JS or in-process Go handler). The protocol is
// JSON-RPC 2.0, mirroring the postMessage protocol defined in mcp-app-bridge.ts.
type AppBridge interface {
	// Send sends a JSON-RPC request to the app and waits for the response.
	// Used by AppHost to call tools/list and tools/call on the app.
	Send(ctx context.Context, req *core.Request) (*core.Response, error)

	// SetRequestHandler registers a handler for app→host requests
	// (e.g., tools/call, resources/read forwarded to the MCP server).
	// Must be called before Start.
	SetRequestHandler(fn func(ctx context.Context, req *core.Request) *core.Response)

	// SetNotificationHandler registers a handler for app→host notifications
	// (e.g., notifications/tools/list_changed, ui/log).
	// Must be called before Start.
	SetNotificationHandler(fn func(method string, params json.RawMessage))

	// Start begins the bridge's communication loop.
	Start() error

	// Close shuts down the bridge.
	Close() error
}
