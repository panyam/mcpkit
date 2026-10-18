package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
)

var testIcon = core.Icon{Src: "https://example.com/i.png", MIMEType: "image/png", Sizes: []string{"48x48"}, Theme: core.IconThemeLight}

// registerFieldedPrimitives registers one tool, resource, template and prompt
// named "fielded" with every optional spec field mcpkit lacked before #1562
// set, plus a bare one of each named "bare" with none of them.
func registerFieldedPrimitives(s *Server) {
	size := int64(0)
	ui := &core.UIMetadata{ResourceUri: "ui://fielded"}
	s.RegisterTool(core.ToolDef{Name: "fielded", InputSchema: map[string]any{"type": "object"}, Icons: []core.Icon{testIcon}},
		func(core.ToolContext, core.ToolRequest) (core.ToolResponse, error) { return core.TextResult(""), nil })
	s.RegisterTool(core.ToolDef{Name: "bare", InputSchema: map[string]any{"type": "object"}},
		func(core.ToolContext, core.ToolRequest) (core.ToolResponse, error) { return core.TextResult(""), nil })

	readNothing := func(core.ResourceContext, core.ResourceRequest) (core.ResourceResult, error) {
		return core.ResourceResult{}, nil
	}
	s.RegisterResource(core.ResourceDef{URI: "file:///fielded", Name: "fielded", Size: &size, Icons: []core.Icon{testIcon},
		Meta: &core.ResourceMeta{UI: ui}}, readNothing)
	s.RegisterResource(core.ResourceDef{URI: "file:///bare", Name: "bare"}, readNothing)

	expandNothing := func(core.ResourceContext, string, map[string]string) (core.ResourceResult, error) {
		return core.ResourceResult{}, nil
	}
	s.RegisterResourceTemplate(core.ResourceTemplate{URITemplate: "file:///fielded/{x}", Name: "fielded", Icons: []core.Icon{testIcon},
		Meta: &core.ResourceMeta{UI: ui}}, expandNothing)
	s.RegisterResourceTemplate(core.ResourceTemplate{URITemplate: "file:///bare/{x}", Name: "bare"}, expandNothing)

	noPrompt := func(core.PromptContext, core.PromptRequest) (core.PromptResponse, error) {
		return core.PromptResult{}, nil
	}
	s.RegisterPrompt(core.PromptDef{Name: "fielded", Icons: []core.Icon{testIcon}, Meta: map[string]any{"com.example/tag": "x"},
		Arguments: []core.PromptArgument{{Name: "q", Title: "Query"}}}, noPrompt)
	s.RegisterPrompt(core.PromptDef{Name: "bare", Arguments: []core.PromptArgument{{Name: "q"}}}, noPrompt)
}

var listMethods = []struct{ method, key string }{
	{"tools/list", "tools"},
	{"resources/list", "resources"},
	{"resources/templates/list", "resourceTemplates"},
	{"prompts/list", "prompts"},
}

// listEntry returns the named entry of a list result, decoded generically so
// assertions see the wire keys rather than a typed round-trip.
func listEntry(t *testing.T, result any, key, name string) map[string]any {
	t.Helper()
	obj := wireObject(t, result)
	items, _ := obj[key].([]any)
	for _, it := range items {
		if m, ok := it.(map[string]any); ok && m["name"] == name {
			return m
		}
	}
	t.Fatalf("%s has no entry %q: %v", key, name, obj)
	return nil
}

func assertFieldedEntry(t *testing.T, key string, e map[string]any) {
	t.Helper()
	icons, _ := e["icons"].([]any)
	if len(icons) != 1 {
		t.Fatalf("%s icons = %v, want one", key, e["icons"])
	}
	if icon := icons[0].(map[string]any); icon["src"] != testIcon.Src || icon["theme"] != "light" {
		t.Errorf("%s icon = %v", key, icon)
	}
	switch key {
	case "resources":
		if size, present := e["size"]; !present || size != float64(0) {
			t.Errorf("resources size = %v (present %v), want 0 present", size, present)
		}
		fallthrough
	case "resourceTemplates":
		meta, _ := e["_meta"].(map[string]any)
		if ui, _ := meta["ui"].(map[string]any); ui["resourceUri"] != "ui://fielded" {
			t.Errorf("%s _meta = %v, want ui.resourceUri", key, e["_meta"])
		}
	case "prompts":
		if meta, _ := e["_meta"].(map[string]any); meta["com.example/tag"] != "x" {
			t.Errorf("prompts _meta = %v", e["_meta"])
		}
		args, _ := e["arguments"].([]any)
		if len(args) != 1 || args[0].(map[string]any)["title"] != "Query" {
			t.Errorf("prompts arguments = %v, want title Query", e["arguments"])
		}
	}
}

func assertBareEntry(t *testing.T, key string, e map[string]any) {
	t.Helper()
	for _, k := range []string{"icons", "size", "_meta", "annotations"} {
		if v, present := e[k]; present {
			t.Errorf("%s bare entry carries %s = %v", key, k, v)
		}
	}
	if key == "prompts" {
		if args, _ := e["arguments"].([]any); len(args) == 1 {
			if _, present := args[0].(map[string]any)["title"]; present {
				t.Errorf("prompts bare argument carries title: %v", args[0])
			}
		}
	}
}

// TestListsCarrySpecPrimitiveFields covers #1562 on the session wire: icons on
// all four primitives, size and _meta on resources, _meta on templates and
// prompts, and title on prompt arguments, each absent when unset.
func TestListsCarrySpecPrimitiveFields(t *testing.T) {
	s := NewServer(core.Implementation{Name: "fields", Version: "1.0"})
	registerFieldedPrimitives(s)
	initDispatcher(s.dispatcher)

	for _, m := range listMethods {
		t.Run(m.method, func(t *testing.T) {
			resp := s.dispatcher.Dispatch(context.Background(), &core.Request{
				JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: m.method,
			})
			if resp.Error != nil {
				t.Fatalf("%s error: %+v", m.method, resp.Error)
			}
			assertFieldedEntry(t, m.key, listEntry(t, resp.Result, m.key, "fielded"))
			assertBareEntry(t, m.key, listEntry(t, resp.Result, m.key, "bare"))
		})
	}
}

// TestStatelessListsCarrySpecPrimitiveFields is the same check on the
// SEP-2575 wire, which builds its list results separately.
func TestStatelessListsCarrySpecPrimitiveFields(t *testing.T) {
	s, url, teardown := newStatelessTestServer(t, stateless.ModeDual)
	defer teardown()
	registerFieldedPrimitives(s)

	for _, m := range listMethods {
		t.Run(m.method, func(t *testing.T) {
			r := decode(t, postStatelessJSON(t, url, map[string]any{
				"jsonrpc": "2.0", "id": 1, "method": m.method, "params": validMetaParams(),
			}, map[string]string{mcpProtocolVersionHeader: draftVersion}))
			if r.Error != nil {
				t.Fatalf("%s error: %+v", m.method, r.Error)
			}
			assertFieldedEntry(t, m.key, listEntry(t, r.Result, m.key, "fielded"))
			assertBareEntry(t, m.key, listEntry(t, r.Result, m.key, "bare"))
		})
	}
}
