package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
)

// TestWithListTTL_AppliesToAllFourEndpoints verifies that WithListTTL
// surfaces on every paginated list response: tools/list, prompts/list,
// resources/list, and resources/templates/list. The hint is server-wide
// per the SEP-2549 design — uniform across endpoints.
func TestWithListTTL_AppliesToAllFourEndpoints(t *testing.T) {
	cases := []struct {
		name        string
		opt         []Option
		wantPresent bool
		wantTTL     float64
	}{
		{"unset omits ttl", nil, false, 0},
		{"positive surfaces value", []Option{WithListTTL(300)}, true, 300},
		{"zero surfaces explicit 'do not cache'", []Option{WithListTTL(0)}, true, 0},
		{"negative treated as unset", []Option{WithListTTL(-1)}, false, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newListTTLClient(t, tc.opt...)
			for _, method := range []string{
				"tools/list",
				"prompts/list",
				"resources/list",
				"resources/templates/list",
			} {
				res, err := c.Call(method, nil)
				if err != nil {
					t.Fatalf("%s: %v", method, err)
				}
				var m map[string]any
				if err := json.Unmarshal(res.Raw, &m); err != nil {
					t.Fatalf("%s unmarshal: %v", method, err)
				}
				got, present := m["ttl"]
				if present != tc.wantPresent {
					t.Errorf("%s: ttl present = %v, want %v; raw=%s",
						method, present, tc.wantPresent, res.Raw)
				}
				if tc.wantPresent {
					if gotF, ok := got.(float64); !ok || gotF != tc.wantTTL {
						t.Errorf("%s: ttl = %v (%T), want %v; raw=%s",
							method, got, got, tc.wantTTL, res.Raw)
					}
				}
			}
		})
	}
}

// TestListToolsPage_ExposesTTL verifies the client's typed
// ListToolsPage helper surfaces the SEP-2549 TTL hint to callers.
// The pre-existing zero-arg ListTools() still drops it for backward
// compat — callers wanting TTL should switch to ListToolsPage.
func TestListToolsPage_ExposesTTL(t *testing.T) {
	c := newListTTLClient(t, WithListTTL(120))

	page, err := c.ListToolsPage("")
	if err != nil {
		t.Fatalf("ListToolsPage: %v", err)
	}
	if page.TTL == nil {
		t.Fatal("expected non-nil TTL on ListToolsPage result; server configured WithListTTL(120)")
	}
	if *page.TTL != 120 {
		t.Errorf("TTL = %d, want 120", *page.TTL)
	}
}

// TestListXPage_HelpersAllSurfaceTTL is a smoke test that all four typed
// page helpers expose TTL — caught a regression where one endpoint was
// missed in the dispatcher wiring.
func TestListXPage_HelpersAllSurfaceTTL(t *testing.T) {
	c := newListTTLClient(t, WithListTTL(60))

	t1, err := c.ListToolsPage("")
	if err != nil || t1.TTL == nil || *t1.TTL != 60 {
		t.Errorf("ListToolsPage TTL = %v, err = %v", t1.TTL, err)
	}
	r1, err := c.ListResourcesPage("")
	if err != nil || r1.TTL == nil || *r1.TTL != 60 {
		t.Errorf("ListResourcesPage TTL = %v, err = %v", r1.TTL, err)
	}
	rt1, err := c.ListResourceTemplatesPage("")
	if err != nil || rt1.TTL == nil || *rt1.TTL != 60 {
		t.Errorf("ListResourceTemplatesPage TTL = %v, err = %v", rt1.TTL, err)
	}
	p1, err := c.ListPromptsPage("")
	if err != nil || p1.TTL == nil || *p1.TTL != 60 {
		t.Errorf("ListPromptsPage TTL = %v, err = %v", p1.TTL, err)
	}
}

// newListTTLClient builds a server with one tool, one resource, one
// resource template, and one prompt registered, then connects a client
// over httptest. Just enough surface area for all four list endpoints
// to return a non-empty page.
func newListTTLClient(t *testing.T, opts ...Option) *client.Client {
	t.Helper()

	srv := NewServer(core.ServerInfo{Name: "list-ttl-test", Version: "0.0.1"}, opts...)

	srv.RegisterTool(
		core.ToolDef{Name: "echo", Description: "echo", InputSchema: map[string]any{"type": "object"}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("ok"), nil
		},
	)
	srv.RegisterResource(
		core.ResourceDef{URI: "file:///fixture", Name: "fixture", MimeType: "text/plain"},
		func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{
				Contents: []core.ResourceReadContent{{URI: req.URI, MimeType: "text/plain", Text: "ok"}},
			}, nil
		},
	)
	srv.RegisterResourceTemplate(
		core.ResourceTemplate{URITemplate: "file:///t/{name}", Name: "tmpl"},
		func(ctx core.ResourceContext, uri string, params map[string]string) (core.ResourceResult, error) {
			return core.ResourceResult{
				Contents: []core.ResourceReadContent{{URI: uri, MimeType: "text/plain", Text: "ok"}},
			}, nil
		},
	)
	srv.RegisterPrompt(
		core.PromptDef{Name: "hello", Description: "hello"},
		func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResult, error) {
			return core.PromptResult{}, nil
		},
	)

	handler := srv.Handler(WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "list-ttl-client", Version: "0.0.1"})
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
