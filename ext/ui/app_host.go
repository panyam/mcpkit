package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// AppHost wraps an MCP Client and an AppBridge, mediating between an MCP App
// (running in a browser iframe or in-process) and an MCP server.
//
// It provides:
//   - Host→App: ListAppTools and CallAppTool forward requests to the app
//   - App→Host: app requests (tools/call, resources/read) are forwarded to the server
//   - Cache: app tool list is cached and refreshed on notifications/tools/list_changed
//   - Aggregation: ListAllTools merges server and app tools for LLM presentation
type AppHost struct {
	client *client.Client
	bridge AppBridge

	mu       sync.RWMutex
	appTools []core.ToolDef

	ctx    context.Context
	cancel context.CancelFunc
}

// AppHostOption configures an AppHost.
type AppHostOption func(*AppHost)

// NewAppHost creates an AppHost that mediates between the given MCP Client
// (connected to a server) and AppBridge (connected to an app).
//
// Call Start() after creating to wire up request/notification routing.
func NewAppHost(c *client.Client, bridge AppBridge, opts ...AppHostOption) *AppHost {
	h := &AppHost{
		client: c,
		bridge: bridge,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Start wires up the bridge handlers and begins the communication loop.
// Must be called after client.Connect() — the bridge may need to forward
// requests that require an active MCP session.
func (h *AppHost) Start(ctx context.Context) error {
	h.ctx, h.cancel = context.WithCancel(ctx)

	// Wire app→host request routing: when the app calls MCPApp.callTool()
	// or MCPApp.readResource(), forward to the MCP server via Client.Call().
	h.bridge.SetRequestHandler(h.handleAppRequest)

	// Wire app→host notification routing: when the app sends
	// notifications/tools/list_changed, refresh our cached tool list.
	h.bridge.SetNotificationHandler(h.handleAppNotification)

	if err := h.bridge.Start(); err != nil {
		return fmt.Errorf("start bridge: %w", err)
	}

	// Fetch the initial tool list from the app so ListAppTools works
	// immediately without waiting for a list_changed notification.
	if err := h.RefreshAppTools(h.ctx); err != nil {
		// Non-fatal — the app may not have registered tools yet.
		// They'll show up when notifications/tools/list_changed fires.
		_ = err
	}

	return nil
}

// Close shuts down the bridge. The caller is responsible for closing the
// underlying Client separately — AppHost does not own the Client lifetime.
func (h *AppHost) Close() error {
	if h.cancel != nil {
		h.cancel()
	}
	return h.bridge.Close()
}

// ListAppTools returns the tools registered by the app. Returns the cached
// list; the cache is refreshed automatically on notifications/tools/list_changed.
func (h *AppHost) ListAppTools(ctx context.Context) ([]core.ToolDef, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	result := make([]core.ToolDef, len(h.appTools))
	copy(result, h.appTools)
	return result, nil
}

// CallAppTool invokes a tool registered by the app via the bridge.
func (h *AppHost) CallAppTool(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	params, err := json.Marshal(map[string]any{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal tool call params: %w", err)
	}

	resp, err := h.bridge.Send(ctx, &core.Request{
		Method: "tools/call",
		Params: params,
	})
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("app tool error %d: %s", resp.Error.Code, resp.Error.Message)
	}

	raw, err := ToBytes(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("read tool result: %w", err)
	}

	var result core.ToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("unmarshal tool result: %w", err)
	}
	return &result, nil
}

// ListAllTools returns tools from both the MCP server and the app, suitable
// for presenting to an LLM. Server tools come first, then app tools.
func (h *AppHost) ListAllTools(ctx context.Context) ([]core.ToolDef, error) {
	serverTools, err := h.client.ListTools()
	if err != nil {
		return nil, fmt.Errorf("list server tools: %w", err)
	}

	h.mu.RLock()
	appTools := make([]core.ToolDef, len(h.appTools))
	copy(appTools, h.appTools)
	h.mu.RUnlock()

	all := make([]core.ToolDef, 0, len(serverTools)+len(appTools))
	all = append(all, serverTools...)
	all = append(all, appTools...)
	return all, nil
}

// RefreshAppTools sends tools/list to the app and updates the cached tool list.
func (h *AppHost) RefreshAppTools(ctx context.Context) error {
	resp, err := h.bridge.Send(ctx, &core.Request{
		Method: "tools/list",
	})
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("app tools/list error %d: %s", resp.Error.Code, resp.Error.Message)
	}

	raw, err := ToBytes(resp.Result)
	if err != nil {
		return fmt.Errorf("read tools list result: %w", err)
	}

	var result struct {
		Tools []core.ToolDef `json:"tools"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("unmarshal tools list: %w", err)
	}

	h.mu.Lock()
	h.appTools = result.Tools
	h.mu.Unlock()
	return nil
}

// handleAppRequest routes app→host JSON-RPC requests to the MCP server.
// Called when the app uses MCPApp.callTool(), MCPApp.readResource(), etc.
func (h *AppHost) handleAppRequest(ctx context.Context, req *core.Request) *core.Response {
	callResult, err := h.client.Call(req.Method, json.RawMessage(req.Params))
	if err != nil {
		return &core.Response{
			ID:    req.ID,
			Error: &core.Error{Code: core.ErrCodeInternal, Message: err.Error()},
		}
	}
	return &core.Response{
		ID:     req.ID,
		Result: callResult.Raw,
	}
}

// handleAppNotification routes app→host notifications.
func (h *AppHost) handleAppNotification(method string, params json.RawMessage) {
	switch method {
	case "notifications/tools/list_changed":
		// Refresh cached tool list in the background.
		go func() {
			if h.ctx != nil {
				h.RefreshAppTools(h.ctx)
			}
		}()
	}
}

// ToBytes converts a Response.Result (any) to []byte for unmarshalling.
// Response.Result may be json.RawMessage, []byte, or an arbitrary struct.
func ToBytes(v any) ([]byte, error) {
	switch val := v.(type) {
	case json.RawMessage:
		return []byte(val), nil
	case []byte:
		return val, nil
	case nil:
		return []byte("null"), nil
	default:
		return json.Marshal(v)
	}
}
