package client_test

import (
	"net/http/httptest"
	"testing"

	"github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// newNilCtxServer stands up a live server. A live server matters here: the
// original panic was in the per-item cancellation check inside the pagination
// loop, which is only reached after a page comes back. A mock that never
// returns results would not reproduce it.
func newNilCtxServer(t *testing.T) *client.Client {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "nilctx", Version: "1.0"})
	srv.RegisterTool(
		core.ToolDef{Name: "echo", Description: "echo", InputSchema: map[string]any{"type": "object"}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			return core.TextResult("ok"), nil
		},
	)
	srv.RegisterResource(
		core.ResourceDef{URI: "test://r", Name: "r"},
		func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{
				Contents: []core.ResourceReadContent{{URI: "test://r", Text: "body"}},
			}, nil
		},
	)
	srv.RegisterPrompt(
		core.PromptDef{Name: "p", Description: "p"},
		func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResponse, error) {
			return core.PromptResult{}, nil
		},
	)

	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "c", Version: "1.0"})
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// TestNilContextDoesNotPanic covers every exported method that takes a ctx.
//
// Go permits an untyped nil for a context.Context parameter and neither the
// compiler nor go vet flags it, so nothing stops a caller from passing one.
// Every internal call site in this repo passes a real context, which is
// exactly why the original nil-deref went unnoticed. These cases exist to
// exercise the path no first-party caller does.
func TestNilContextDoesNotPanic(t *testing.T) {
	c := newNilCtxServer(t)

	cases := []struct {
		name string
		call func() error
	}{
		{"ListTools", func() error { _, err := c.ListTools(nil); return err }},
		{"ListToolsForModel", func() error { _, err := c.ListToolsForModel(nil); return err }},
		{"ListResources", func() error { _, err := c.ListResources(nil); return err }},
		{"ListResourceTemplates", func() error { _, err := c.ListResourceTemplates(nil); return err }},
		{"ListPrompts", func() error { _, err := c.ListPrompts(nil); return err }},
		{"ListToolsPage", func() error { _, err := c.ListToolsPage(nil, ""); return err }},
		{"ListResourcesPage", func() error { _, err := c.ListResourcesPage(nil, ""); return err }},
		{"ListPromptsPage", func() error { _, err := c.ListPromptsPage(nil, ""); return err }},
		{"Call", func() error { _, err := c.Call(nil, "ping", nil); return err }},
		{"ToolCall", func() error { _, err := c.ToolCall(nil, "echo", map[string]any{}); return err }},
		{"ToolCallFull", func() error { _, err := c.ToolCallFull(nil, "echo", map[string]any{}); return err }},
		{"ReadResource", func() error { _, err := c.ReadResource(nil, "test://r"); return err }},
		{"ReadResourceFull", func() error { _, err := c.ReadResourceFull(nil, "test://r"); return err }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panicked on a nil context: %v", r)
				}
			}()
			if err := tc.call(); err != nil {
				t.Fatalf("returned an error on a nil context: %v", err)
			}
		})
	}
}

// TestNilContextIterators covers the range-over-func surface, where the
// original crash actually lived.
func TestNilContextIterators(t *testing.T) {
	c := newNilCtxServer(t)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("iterator panicked on a nil context: %v", r)
		}
	}()

	var tools int
	for _, err := range c.Tools(nil) {
		if err != nil {
			t.Fatalf("Tools(nil): %v", err)
		}
		tools++
	}
	if tools != 1 {
		t.Errorf("got %d tools, want 1", tools)
	}

	var res int
	for _, err := range c.Resources(nil) {
		if err != nil {
			t.Fatalf("Resources(nil): %v", err)
		}
		res++
	}
	if res != 1 {
		t.Errorf("got %d resources, want 1", res)
	}
}
