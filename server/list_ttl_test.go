package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
)

// TestWithListTTLMs_AppliesToAllFourEndpoints verifies that WithListTTLMs
// surfaces ttlMs on every paginated list response: tools/list, prompts/list,
// resources/list, and resources/templates/list. The hint is server-wide per
// the SEP-2549 design — uniform across endpoints.
func TestWithListTTLMs_AppliesToAllFourEndpoints(t *testing.T) {
	cases := []struct {
		name        string
		opt         []Option
		wantPresent bool
		wantTTL     float64
	}{
		{"unset omits ttlMs", nil, false, 0},
		{"positive surfaces value", []Option{WithListTTLMs(300000)}, true, 300000},
		{"zero surfaces explicit immediately-stale", []Option{WithListTTLMs(0)}, true, 0},
		{"negative treated as unset", []Option{WithListTTLMs(-1)}, false, 0},
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
				got, present := m["ttlMs"]
				if present != tc.wantPresent {
					t.Errorf("%s: ttlMs present = %v, want %v; raw=%s",
						method, present, tc.wantPresent, res.Raw)
				}
				if tc.wantPresent {
					if gotF, ok := got.(float64); !ok || gotF != tc.wantTTL {
						t.Errorf("%s: ttlMs = %v (%T), want %v; raw=%s",
							method, got, got, tc.wantTTL, res.Raw)
					}
				}
				if _, stale := m["ttl"]; stale {
					t.Errorf("%s: stale `ttl` field present; raw=%s", method, res.Raw)
				}
			}
		})
	}
}

// TestWithListCacheControl_SetsTTLAndScope verifies the combined option sets
// both ttlMs and cacheScope on every list endpoint in a single call.
func TestWithListCacheControl_SetsTTLAndScope(t *testing.T) {
	c := newListTTLClient(t, WithListCacheControl(120000, core.CacheScopePrivate))
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
		if ttl, ok := m["ttlMs"].(float64); !ok || ttl != 120000 {
			t.Errorf("%s: ttlMs = %v, want 120000; raw=%s", method, m["ttlMs"], res.Raw)
		}
		if m["cacheScope"] != core.CacheScopePrivate {
			t.Errorf("%s: cacheScope = %v, want %q; raw=%s",
				method, m["cacheScope"], core.CacheScopePrivate, res.Raw)
		}
	}
}

// TestListToolsPage_ExposesTTLMs verifies the client's typed ListToolsPage
// helper surfaces the SEP-2549 ttlMs hint to callers. The pre-existing
// zero-arg ListTools() still drops it for backward compat — callers wanting
// the hint should switch to ListToolsPage.
func TestListToolsPage_ExposesTTLMs(t *testing.T) {
	c := newListTTLClient(t, WithListTTLMs(120000))

	page, err := c.ListToolsPage("")
	if err != nil {
		t.Fatalf("ListToolsPage: %v", err)
	}
	if page.TTLMs == nil {
		t.Fatal("expected non-nil TTLMs on ListToolsPage result; server configured WithListTTLMs(120000)")
	}
	if *page.TTLMs != 120000 {
		t.Errorf("TTLMs = %d, want 120000", *page.TTLMs)
	}
}

// TestListXPage_HelpersAllSurfaceTTLMs is a smoke test that all four typed
// page helpers expose ttlMs — caught a regression where one endpoint was
// missed in the dispatcher wiring.
func TestListXPage_HelpersAllSurfaceTTLMs(t *testing.T) {
	c := newListTTLClient(t, WithListTTLMs(60000))

	t1, err := c.ListToolsPage("")
	if err != nil || t1.TTLMs == nil || *t1.TTLMs != 60000 {
		t.Errorf("ListToolsPage TTLMs = %v, err = %v", t1.TTLMs, err)
	}
	r1, err := c.ListResourcesPage("")
	if err != nil || r1.TTLMs == nil || *r1.TTLMs != 60000 {
		t.Errorf("ListResourcesPage TTLMs = %v, err = %v", r1.TTLMs, err)
	}
	rt1, err := c.ListResourceTemplatesPage("")
	if err != nil || rt1.TTLMs == nil || *rt1.TTLMs != 60000 {
		t.Errorf("ListResourceTemplatesPage TTLMs = %v, err = %v", rt1.TTLMs, err)
	}
	p1, err := c.ListPromptsPage("")
	if err != nil || p1.TTLMs == nil || *p1.TTLMs != 60000 {
		t.Errorf("ListPromptsPage TTLMs = %v, err = %v", p1.TTLMs, err)
	}
}

// TestReadResource_ServerDefaultCacheControl verifies WithReadResourceCacheControl
// surfaces ttlMs / cacheScope on a resources/read response when the handler
// itself sets neither field. SEP-2549 added resources/read to the cacheable
// coverage list mid-cycle.
func TestReadResource_ServerDefaultCacheControl(t *testing.T) {
	c := newReadTTLClient(t, nil, "", WithReadResourceCacheControl(30000, core.CacheScopePrivate))

	res, err := c.Call("resources/read", map[string]any{"uri": "file:///fixture"})
	if err != nil {
		t.Fatalf("resources/read: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(res.Raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ttl, ok := m["ttlMs"].(float64); !ok || ttl != 30000 {
		t.Errorf("ttlMs = %v, want 30000; raw=%s", m["ttlMs"], res.Raw)
	}
	if m["cacheScope"] != core.CacheScopePrivate {
		t.Errorf("cacheScope = %v, want %q; raw=%s", m["cacheScope"], core.CacheScopePrivate, res.Raw)
	}
}

// TestReadResource_HandlerOverridesCacheControl verifies a resource handler
// that sets TTLMs / CacheScope on its return value wins over the server-wide
// WithReadResourceCacheControl default — the default only fills unset fields.
func TestReadResource_HandlerOverridesCacheControl(t *testing.T) {
	c := newReadTTLClient(t, core.IntPtr(5000), core.CacheScopePublic,
		WithReadResourceCacheControl(30000, core.CacheScopePrivate))

	res, err := c.Call("resources/read", map[string]any{"uri": "file:///fixture"})
	if err != nil {
		t.Fatalf("resources/read: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(res.Raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ttl, ok := m["ttlMs"].(float64); !ok || ttl != 5000 {
		t.Errorf("ttlMs = %v, want 5000 (handler override); raw=%s", m["ttlMs"], res.Raw)
	}
	if m["cacheScope"] != core.CacheScopePublic {
		t.Errorf("cacheScope = %v, want %q (handler override); raw=%s",
			m["cacheScope"], core.CacheScopePublic, res.Raw)
	}
}

// TestReadResourceFull_TypedCacheHints verifies the client's ReadResourceFull
// helper surfaces the SEP-2549 ttlMs / cacheScope hints on the typed
// core.ResourceResult — the plain ReadResource helper drops that envelope.
func TestReadResourceFull_TypedCacheHints(t *testing.T) {
	c := newReadTTLClient(t, nil, "", WithReadResourceCacheControl(45000, core.CacheScopePrivate))

	rr, err := c.ReadResourceFull("file:///fixture")
	if err != nil {
		t.Fatalf("ReadResourceFull: %v", err)
	}
	if rr.TTLMs == nil || *rr.TTLMs != 45000 {
		t.Errorf("TTLMs = %v, want &45000", rr.TTLMs)
	}
	if rr.CacheScope != core.CacheScopePrivate {
		t.Errorf("CacheScope = %q, want %q", rr.CacheScope, core.CacheScopePrivate)
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

	return connectListTTLClient(t, srv)
}

// newReadTTLClient builds a server with a single resource whose handler
// returns the given per-read TTLMs / CacheScope (handlerTTL nil / handlerScope
// "" leaves the fields unset so the server default applies). Used to exercise
// the resources/read cache-hint path in both directions.
func newReadTTLClient(t *testing.T, handlerTTL *int, handlerScope string, opts ...Option) *client.Client {
	t.Helper()

	srv := NewServer(core.ServerInfo{Name: "read-ttl-test", Version: "0.0.1"}, opts...)
	srv.RegisterResource(
		core.ResourceDef{URI: "file:///fixture", Name: "fixture", MimeType: "text/plain"},
		func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{
				Contents:   []core.ResourceReadContent{{URI: req.URI, MimeType: "text/plain", Text: "ok"}},
				TTLMs:      handlerTTL,
				CacheScope: handlerScope,
			}, nil
		},
	)
	return connectListTTLClient(t, srv)
}

// connectListTTLClient serves srv over httptest and returns a connected
// client, registering cleanup for both.
func connectListTTLClient(t *testing.T, srv *Server) *client.Client {
	t.Helper()

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
