package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// RegisteredTool is a tool with routing metadata attached. The tool name
// stays clean (no server ID prefix) — routing is via ServerID sidecar.
type RegisteredTool struct {
	core.ToolDef
	ServerID string `json:"serverId"` // which server owns this tool
	Source   string `json:"source"`   // "server" or "app"
}

// ToolResolver is called when CallTool hits an ambiguous tool name (same name
// registered by multiple servers). It receives the candidates and the call
// arguments, and returns the server ID to route to.
//
// Implementations can use any strategy: static priority, arg-based routing,
// LLM sampling, user elicitation, round-robin, etc.
type ToolResolver func(ctx context.Context, name string,
	candidates []RegisteredTool, args map[string]any) (serverID string, err error)

// CollisionHandler is called when Add or a tools/list_changed notification
// creates a new tool name collision. Informational — lets the host log,
// alert, or adjust its resolver strategy proactively.
type CollisionHandler func(toolName string, serverIDs []string)

// ServerRegistry manages connections to multiple MCP servers and provides
// unified tool aggregation and routing. Each server can have its own auth,
// app bridge, and reconnection policy.
type ServerRegistry struct {
	mu      sync.RWMutex
	servers map[string]*serverEntry

	resolver    ToolResolver
	onCollision CollisionHandler
	onNotify    func(serverID, method string, params any)

	// Derived tool index, rebuilt on Add/Remove/list_changed.
	index toolIndex

	closed bool
}

type serverEntry struct {
	id     string
	client *client.Client
	host   *AppHost  // nil if no app bridge
	bridge AppBridge // nil if no app bridge
}

// toolIndex is a derived cache mapping tool names to their candidates.
type toolIndex struct {
	byName map[string][]RegisteredTool
}

// RegistryOption configures a ServerRegistry.
type RegistryOption func(*ServerRegistry)

// WithToolResolver sets the resolver for ambiguous tool names.
func WithToolResolver(fn ToolResolver) RegistryOption {
	return func(r *ServerRegistry) { r.resolver = fn }
}

// WithCollisionHandler sets a callback for tool name collision notifications.
func WithCollisionHandler(fn CollisionHandler) RegistryOption {
	return func(r *ServerRegistry) { r.onCollision = fn }
}

// WithRegistryNotificationHandler sets a unified notification handler that
// receives notifications from all servers, tagged with the server ID.
func WithRegistryNotificationHandler(fn func(serverID, method string, params any)) RegistryOption {
	return func(r *ServerRegistry) { r.onNotify = fn }
}

// NewServerRegistry creates a registry for managing multiple MCP server
// connections with unified tool routing.
func NewServerRegistry(opts ...RegistryOption) *ServerRegistry {
	r := &ServerRegistry{
		servers: make(map[string]*serverEntry),
		index:   toolIndex{byName: make(map[string][]RegisteredTool)},
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Add registers a connected MCP client under the given server ID. The client
// must already be connected (via client.Connect). The caller owns client
// construction and auth configuration — the registry only manages routing.
func (r *ServerRegistry) Add(ctx context.Context, id string, c *client.Client) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return fmt.Errorf("registry is closed")
	}
	if _, exists := r.servers[id]; exists {
		return fmt.Errorf("server %q already registered", id)
	}

	entry := &serverEntry{id: id, client: c}
	r.servers[id] = entry
	r.rebuildIndex()
	return nil
}

// AddWithBridge registers a connected MCP client with an app bridge. The
// bridge enables app-provided tool aggregation and host↔app request routing.
func (r *ServerRegistry) AddWithBridge(ctx context.Context, id string, c *client.Client, bridge AppBridge) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return fmt.Errorf("registry is closed")
	}
	if _, exists := r.servers[id]; exists {
		r.mu.Unlock()
		return fmt.Errorf("server %q already registered", id)
	}

	host := NewAppHost(c, bridge)
	entry := &serverEntry{id: id, client: c, host: host, bridge: bridge}
	r.servers[id] = entry

	// Release the lock before Start — it may call back into us via
	// the notification handler (tools/list_changed → rebuildIndex).
	r.mu.Unlock()

	if err := host.Start(ctx); err != nil {
		r.mu.Lock()
		delete(r.servers, id)
		r.mu.Unlock()
		return fmt.Errorf("start app host for %q: %w", id, err)
	}

	r.mu.Lock()
	r.rebuildIndex()
	r.mu.Unlock()
	return nil
}

// Remove disconnects and removes a server. Closes the app bridge if present.
// Does NOT close the underlying client — the caller owns client lifetime.
func (r *ServerRegistry) Remove(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.servers[id]
	if !ok {
		return fmt.Errorf("server %q not found", id)
	}

	if entry.host != nil {
		entry.host.Close()
	}

	delete(r.servers, id)
	r.rebuildIndex()
	return nil
}

// Close shuts down all servers in the registry. Closes app bridges but not
// the underlying clients (caller owns client lifetime).
func (r *ServerRegistry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.closed = true
	for _, entry := range r.servers {
		if entry.host != nil {
			entry.host.Close()
		}
	}
	r.servers = make(map[string]*serverEntry)
	r.index = toolIndex{byName: make(map[string][]RegisteredTool)}
	return nil
}

// Servers returns the IDs of all registered servers, sorted alphabetically.
func (r *ServerRegistry) Servers() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := make([]string, 0, len(r.servers))
	for id := range r.servers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// AllTools returns all tools from all servers with routing metadata.
// Server tools come first (grouped by server ID), then app tools.
func (r *ServerRegistry) AllTools(ctx context.Context) ([]RegisteredTool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var all []RegisteredTool
	for _, candidates := range r.index.byName {
		all = append(all, candidates...)
	}

	// Sort for deterministic output: by server ID, then source, then name.
	sort.Slice(all, func(i, j int) bool {
		if all[i].ServerID != all[j].ServerID {
			return all[i].ServerID < all[j].ServerID
		}
		if all[i].Source != all[j].Source {
			return all[i].Source < all[j].Source
		}
		return all[i].Name < all[j].Name
	})
	return all, nil
}

// CallTool routes a tool call by name. If the name is unambiguous (only one
// server has it), routes directly. If ambiguous, invokes the ToolResolver.
// If no resolver is set, returns a descriptive error listing candidates.
func (r *ServerRegistry) CallTool(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	r.mu.RLock()
	candidates := r.index.byName[name]
	r.mu.RUnlock()

	if len(candidates) == 0 {
		return nil, fmt.Errorf("unknown tool %q", name)
	}

	if len(candidates) == 1 {
		return r.CallToolOn(ctx, candidates[0].ServerID, name, args)
	}

	// Ambiguous — invoke resolver.
	if r.resolver == nil {
		ids := make([]string, len(candidates))
		for i, c := range candidates {
			ids[i] = c.ServerID
		}
		return nil, fmt.Errorf("ambiguous tool %q: found in servers [%s], use CallToolOn",
			name, strings.Join(ids, ", "))
	}

	serverID, err := r.resolver(ctx, name, candidates, args)
	if err != nil {
		return nil, fmt.Errorf("resolver failed for %q: %w", name, err)
	}
	return r.CallToolOn(ctx, serverID, name, args)
}

// CallToolOn routes a tool call to a specific server, bypassing resolution.
func (r *ServerRegistry) CallToolOn(ctx context.Context, serverID, name string, args map[string]any) (*core.ToolResult, error) {
	r.mu.RLock()
	entry, ok := r.servers[serverID]
	r.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unknown server %q", serverID)
	}

	// Check if this is an app tool first.
	if entry.host != nil {
		r.mu.RLock()
		candidates := r.index.byName[name]
		r.mu.RUnlock()

		for _, c := range candidates {
			if c.ServerID == serverID && c.Source == "app" {
				return entry.host.CallAppTool(ctx, name, args)
			}
		}
	}

	// Server tool — call via client.
	params, err := json.Marshal(map[string]any{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}

	callResult, cerr := entry.client.Call("tools/call", json.RawMessage(params))
	if cerr != nil {
		return nil, cerr
	}

	var result core.ToolResult
	if err := callResult.Unmarshal(&result); err != nil {
		return nil, fmt.Errorf("unmarshal tool result: %w", err)
	}
	return &result, nil
}

// rebuildIndex rebuilds the derived tool index from all server entries.
// Must be called with r.mu held (write lock).
func (r *ServerRegistry) rebuildIndex() {
	idx := toolIndex{byName: make(map[string][]RegisteredTool)}

	for _, entry := range r.servers {
		// Server tools.
		if entry.client == nil {
			continue
		}
		tools, err := entry.client.ListTools()
		if err == nil {
			for _, td := range tools {
				rt := RegisteredTool{ToolDef: td, ServerID: entry.id, Source: "server"}
				idx.byName[td.Name] = append(idx.byName[td.Name], rt)
			}
		}

		// App tools (if bridge is wired).
		if entry.host != nil {
			appTools, err := entry.host.ListAppTools(context.Background())
			if err == nil {
				for _, td := range appTools {
					rt := RegisteredTool{ToolDef: td, ServerID: entry.id, Source: "app"}
					idx.byName[td.Name] = append(idx.byName[td.Name], rt)
				}
			}
		}
	}

	// Detect new collisions.
	if r.onCollision != nil {
		for name, candidates := range idx.byName {
			if len(candidates) > 1 {
				// Check if this is a new collision (wasn't in old index).
				if len(r.index.byName[name]) <= 1 {
					ids := make([]string, len(candidates))
					for i, c := range candidates {
						ids[i] = c.ServerID
					}
					r.onCollision(name, ids)
				}
			}
		}
	}

	r.index = idx
}
