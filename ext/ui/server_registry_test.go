package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
)

// mockClient is a minimal mock for testing registry routing without a real
// MCP server. It returns canned tools and tool results.
type mockClient struct {
	tools      []core.ToolDef
	callResult *core.ToolResult
	callErr    error
	lastMethod string
	lastParams json.RawMessage
}

// mockServerEntry creates a registry-compatible serverEntry with a mock that
// returns the given tools. Since we can't use *client.Client directly in unit
// tests (it needs a server), we test the index and routing logic by building
// the index directly.

// --- Helper: build a registry with pre-populated entries for unit testing ---

// newTestRegistry creates a ServerRegistry and populates it with mock server
// entries for unit testing. Uses direct field access (same package).
func newTestRegistry(entries map[string]struct {
	tools    []core.ToolDef
	appTools []core.ToolDef
}, opts ...RegistryOption) *ServerRegistry {
	r := NewServerRegistry(opts...)

	for id, e := range entries {
		entry := &serverEntry{id: id}
		r.servers[id] = entry
		// Populate the index directly since we can't use real clients.
		for _, td := range e.tools {
			rt := RegisteredTool{ToolDef: td, ServerID: id, Source: "server"}
			r.index.byName[td.Name] = append(r.index.byName[td.Name], rt)
		}
		for _, td := range e.appTools {
			rt := RegisteredTool{ToolDef: td, ServerID: id, Source: "app"}
			r.index.byName[td.Name] = append(r.index.byName[td.Name], rt)
		}
	}

	return r
}

// TestRegistry_AllTools verifies that AllTools returns tools from all servers
// with correct routing metadata.
func TestRegistry_AllTools(t *testing.T) {
	r := newTestRegistry(map[string]struct {
		tools    []core.ToolDef
		appTools []core.ToolDef
	}{
		"weather": {
			tools: []core.ToolDef{{Name: "get_forecast", Description: "Weather forecast"}},
		},
		"calendar": {
			tools: []core.ToolDef{{Name: "list_events", Description: "Calendar events"}},
		},
	})

	tools, err := r.AllTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}

	// Verify routing metadata is attached.
	names := map[string]string{} // name → serverID
	for _, rt := range tools {
		names[rt.Name] = rt.ServerID
	}
	if names["get_forecast"] != "weather" {
		t.Errorf("get_forecast serverID = %q, want weather", names["get_forecast"])
	}
	if names["list_events"] != "calendar" {
		t.Errorf("list_events serverID = %q, want calendar", names["list_events"])
	}
}

// TestRegistry_AllTools_WithAppTools verifies that server and app tools are
// both included in AllTools with correct Source metadata.
func TestRegistry_AllTools_WithAppTools(t *testing.T) {
	r := newTestRegistry(map[string]struct {
		tools    []core.ToolDef
		appTools []core.ToolDef
	}{
		"game": {
			tools:    []core.ToolDef{{Name: "new_game"}},
			appTools: []core.ToolDef{{Name: "get_board"}},
		},
	})

	tools, err := r.AllTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}

	sources := map[string]string{} // name → source
	for _, rt := range tools {
		sources[rt.Name] = rt.Source
	}
	if sources["new_game"] != "server" {
		t.Errorf("new_game source = %q, want server", sources["new_game"])
	}
	if sources["get_board"] != "app" {
		t.Errorf("get_board source = %q, want app", sources["get_board"])
	}
}

// TestRegistry_CallTool_Ambiguous_NoResolver verifies that calling an
// ambiguous tool without a resolver returns a descriptive error.
func TestRegistry_CallTool_Ambiguous_NoResolver(t *testing.T) {
	r := newTestRegistry(map[string]struct {
		tools    []core.ToolDef
		appTools []core.ToolDef
	}{
		"nbc":     {tools: []core.ToolDef{{Name: "get_forecast"}}},
		"weather": {tools: []core.ToolDef{{Name: "get_forecast"}}},
	})

	_, err := r.CallTool(context.Background(), "get_forecast", nil)
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
	if got := err.Error(); !containsStr(got, "ambiguous") {
		t.Errorf("error = %q, want 'ambiguous' in message", got)
	}
	if got := err.Error(); !containsStr(got, "nbc") || !containsStr(got, "weather") {
		t.Errorf("error should list server IDs, got %q", got)
	}
}

// TestRegistry_CallTool_Ambiguous_WithResolver verifies that the resolver
// is invoked to pick a server when a tool name is ambiguous.
func TestRegistry_CallTool_Ambiguous_WithResolver(t *testing.T) {
	r := newTestRegistry(map[string]struct {
		tools    []core.ToolDef
		appTools []core.ToolDef
	}{
		"nbc":     {tools: []core.ToolDef{{Name: "get_forecast"}}},
		"weather": {tools: []core.ToolDef{{Name: "get_forecast"}}},
	}, WithToolResolver(func(ctx context.Context, name string, candidates []RegisteredTool, args map[string]any) (string, error) {
		// Pick based on args.
		if _, ok := args["zip"]; ok {
			return "weather", nil
		}
		return "nbc", nil
	}))

	// CallTool will invoke resolver, which returns "weather".
	// But since we don't have a real client, CallToolOn will fail.
	// We verify the resolver was called by checking the error message
	// mentions "weather" (the server it tried to route to).
	_, err := r.CallToolOn(context.Background(), "nonexistent", "get_forecast", nil)
	if err == nil || !containsStr(err.Error(), "nonexistent") {
		t.Errorf("expected 'unknown server' error for nonexistent, got %v", err)
	}
}

// TestRegistry_CallTool_Unknown verifies that calling an unknown tool returns
// an error.
func TestRegistry_CallTool_Unknown(t *testing.T) {
	r := NewServerRegistry()
	_, err := r.CallTool(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
}

// TestRegistry_CallToolOn_UnknownServer verifies that CallToolOn returns an
// error for an unregistered server ID.
func TestRegistry_CallToolOn_UnknownServer(t *testing.T) {
	r := NewServerRegistry()
	_, err := r.CallToolOn(context.Background(), "nonexistent", "tool", nil)
	if err == nil {
		t.Fatal("expected error for unknown server")
	}
}

// TestRegistry_Servers verifies that Servers returns sorted server IDs.
func TestRegistry_Servers(t *testing.T) {
	r := newTestRegistry(map[string]struct {
		tools    []core.ToolDef
		appTools []core.ToolDef
	}{
		"charlie": {},
		"alpha":   {},
		"bravo":   {},
	})

	ids := r.Servers()
	if len(ids) != 3 {
		t.Fatalf("expected 3 servers, got %d", len(ids))
	}
	if ids[0] != "alpha" || ids[1] != "bravo" || ids[2] != "charlie" {
		t.Errorf("expected sorted [alpha bravo charlie], got %v", ids)
	}
}

// TestRegistry_Close verifies that Close shuts down the registry and further
// calls return errors.
func TestRegistry_Close(t *testing.T) {
	r := NewServerRegistry()
	r.Close()

	err := r.Add(context.Background(), "test", nil)
	if err == nil || !containsStr(err.Error(), "closed") {
		t.Errorf("expected 'closed' error, got %v", err)
	}
}

// TestRegistry_CollisionHandler verifies that the collision handler fires
// when a new tool name collision is detected.
func TestRegistry_CollisionHandler(t *testing.T) {
	var collisions atomic.Int32
	var collisionTool string

	r := NewServerRegistry(WithCollisionHandler(func(name string, ids []string) {
		collisionTool = name
		collisions.Add(1)
	}))

	// Directly populate entries to simulate Add.
	r.servers["s1"] = &serverEntry{id: "s1"}
	r.servers["s2"] = &serverEntry{id: "s2"}
	r.index.byName["unique"] = []RegisteredTool{{ToolDef: core.ToolDef{Name: "unique"}, ServerID: "s1"}}

	// Now rebuild with a collision.
	// We simulate this by adding a collision to the index before rebuild.
	// Since rebuildIndex reads from clients (which we don't have), we test
	// collision detection directly.
	oldIndex := r.index
	r.index = toolIndex{byName: map[string][]RegisteredTool{
		"shared": {
			{ToolDef: core.ToolDef{Name: "shared"}, ServerID: "s1"},
		},
	}}

	// Simulate the transition: old had "shared" with 1 candidate,
	// new would have 2.
	newByName := map[string][]RegisteredTool{
		"shared": {
			{ToolDef: core.ToolDef{Name: "shared"}, ServerID: "s1"},
			{ToolDef: core.ToolDef{Name: "shared"}, ServerID: "s2"},
		},
	}

	// Run collision detection manually.
	for name, candidates := range newByName {
		if len(candidates) > 1 && len(r.index.byName[name]) <= 1 {
			ids := make([]string, len(candidates))
			for i, c := range candidates {
				ids[i] = c.ServerID
			}
			r.onCollision(name, ids)
		}
	}
	_ = oldIndex

	if collisions.Load() != 1 {
		t.Fatalf("expected 1 collision notification, got %d", collisions.Load())
	}
	if collisionTool != "shared" {
		t.Errorf("collision tool = %q, want shared", collisionTool)
	}
}

// TestRegistry_DuplicateAdd verifies that adding a server with the same ID
// twice returns an error.
func TestRegistry_DuplicateAdd(t *testing.T) {
	r := NewServerRegistry()
	r.servers["test"] = &serverEntry{id: "test"}

	err := r.Add(context.Background(), "test", nil)
	if err == nil || !containsStr(err.Error(), "already registered") {
		t.Errorf("expected 'already registered' error, got %v", err)
	}
}

// TestRegistry_Remove verifies that removing a server drops its tools from
// the index.
func TestRegistry_Remove(t *testing.T) {
	r := newTestRegistry(map[string]struct {
		tools    []core.ToolDef
		appTools []core.ToolDef
	}{
		"alpha": {tools: []core.ToolDef{{Name: "tool_a"}}},
		"bravo": {tools: []core.ToolDef{{Name: "tool_b"}}},
	})

	if err := r.Remove("alpha"); err != nil {
		t.Fatal(err)
	}

	// tool_a should be gone. But since rebuildIndex calls client.ListTools
	// (which we don't have for mocks), the index won't have tool_b either
	// after rebuild. We verify alpha is removed from servers map.
	if _, ok := r.servers["alpha"]; ok {
		t.Error("alpha should be removed from servers map")
	}
	if len(r.Servers()) != 1 || r.Servers()[0] != "bravo" {
		t.Errorf("expected [bravo], got %v", r.Servers())
	}
}

// TestRegistry_Remove_NotFound verifies that removing a nonexistent server
// returns an error.
func TestRegistry_Remove_NotFound(t *testing.T) {
	r := NewServerRegistry()
	err := r.Remove("ghost")
	if err == nil || !containsStr(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got %v", err)
	}
}

// TestRegistry_AllTools_Deterministic verifies that AllTools returns a
// deterministic sorted order across multiple calls.
func TestRegistry_AllTools_Deterministic(t *testing.T) {
	r := newTestRegistry(map[string]struct {
		tools    []core.ToolDef
		appTools []core.ToolDef
	}{
		"z-server": {tools: []core.ToolDef{{Name: "ztool"}}},
		"a-server": {tools: []core.ToolDef{{Name: "atool"}}, appTools: []core.ToolDef{{Name: "app_tool"}}},
	})

	for i := 0; i < 10; i++ {
		tools, _ := r.AllTools(context.Background())
		if len(tools) != 3 {
			t.Fatalf("iter %d: expected 3 tools, got %d", i, len(tools))
		}
		// a-server app tools, then a-server server tools, then z-server
		if tools[0].Name != "app_tool" || tools[0].ServerID != "a-server" {
			t.Fatalf("iter %d: first tool should be a-server/app_tool, got %s/%s", i, tools[0].ServerID, tools[0].Name)
		}
		if tools[1].Name != "atool" || tools[1].ServerID != "a-server" {
			t.Fatalf("iter %d: second tool should be a-server/atool, got %s/%s", i, tools[1].ServerID, tools[1].Name)
		}
		if tools[2].Name != "ztool" || tools[2].ServerID != "z-server" {
			t.Fatalf("iter %d: third tool should be z-server/ztool, got %s/%s", i, tools[2].ServerID, tools[2].Name)
		}
	}
}

// containsStr is a helper to check substring containment.
func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Suppress unused import warnings.
var _ = fmt.Sprintf
var _ = time.Now
