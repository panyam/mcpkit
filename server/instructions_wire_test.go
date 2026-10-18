package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
)

const testInstructions = "Call search before fetch."

// fullImplementation sets every optional Implementation field, so a wire
// assertion catches any one of them being dropped or renamed.
func fullImplementation() core.Implementation {
	return core.Implementation{
		Name:        "demo",
		Title:       "Demo Server",
		Version:     "1.0",
		Description: "Answers questions about demos.",
		WebsiteURL:  "https://example.com",
		Icons: []core.Icon{{
			Src:      "https://example.com/icon.png",
			MIMEType: "image/png",
			Sizes:    []string{"48x48"},
			Theme:    core.IconThemeDark,
		}},
	}
}

// wireObject decodes raw JSON into a generic map so assertions see the keys a
// client would, not a typed round-trip that could hide a misplaced field.
func wireObject(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := core.MarshalJSON(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return obj
}

func initializeResult(t *testing.T, s *Server) map[string]any {
	t.Helper()
	resp := s.dispatcher.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params:  core.NewRawJSON(json.RawMessage(`{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"c","version":"1"}}`)),
	})
	if resp.Error != nil {
		t.Fatalf("initialize error: %+v", resp.Error)
	}
	return wireObject(t, resp.Result)
}

// TestInitializeCarriesInstructionsOnResult covers issue 1560: every schema
// from 2025-03-26 on puts instructions on InitializeResult, beside serverInfo,
// and Implementation has no such field.
func TestInitializeCarriesInstructionsOnResult(t *testing.T) {
	s := NewServer(fullImplementation(), WithInstructions(testInstructions))
	result := initializeResult(t, s)

	if result["instructions"] != testInstructions {
		t.Errorf("result.instructions = %v, want %q (result=%v)", result["instructions"], testInstructions, result)
	}
	info, ok := result["serverInfo"].(map[string]any)
	if !ok {
		t.Fatalf("serverInfo = %#v, want an object", result["serverInfo"])
	}
	if _, present := info["instructions"]; present {
		t.Errorf("serverInfo carries instructions; the spec puts it on the result: %v", info)
	}
}

func TestInitializeOmitsInstructionsWhenUnset(t *testing.T) {
	result := initializeResult(t, NewServer(fullImplementation()))
	if _, present := result["instructions"]; present {
		t.Errorf("result.instructions present with no WithInstructions: %v", result)
	}
}

// TestInitializeServerInfoCarriesEveryImplementationField pins the
// 2025-11-25 Implementation keys, including icons, which mcpkit lacked.
func TestInitializeServerInfoCarriesEveryImplementationField(t *testing.T) {
	info := initializeResult(t, NewServer(fullImplementation()))["serverInfo"].(map[string]any)

	want := map[string]any{
		"name":        "demo",
		"title":       "Demo Server",
		"version":     "1.0",
		"description": "Answers questions about demos.",
		"websiteUrl":  "https://example.com",
	}
	for k, v := range want {
		if info[k] != v {
			t.Errorf("serverInfo.%s = %v, want %v", k, info[k], v)
		}
	}
	icons, ok := info["icons"].([]any)
	if !ok || len(icons) != 1 {
		t.Fatalf("serverInfo.icons = %#v, want one icon", info["icons"])
	}
	icon := icons[0].(map[string]any)
	if icon["src"] != "https://example.com/icon.png" || icon["mimeType"] != "image/png" || icon["theme"] != "dark" {
		t.Errorf("icon = %v, want src/mimeType/theme from the config", icon)
	}
	if sizes, _ := icon["sizes"].([]any); len(sizes) != 1 || sizes[0] != "48x48" {
		t.Errorf("icon.sizes = %v, want [48x48]", icon["sizes"])
	}
}

// TestServerDiscoverCarriesInstructionsOnResult covers the stateless half of
// issue 1560. The 2026-07-28 DiscoverResult has a top-level instructions
// field, and the serverInfo stamped into every result's _meta is an
// Implementation, which has none. Before the fix discover omitted it and
// every result's _meta repeated it.
func TestServerDiscoverCarriesInstructionsOnResult(t *testing.T) {
	_, url, teardown := newStatelessTestServerWithOpts(t, stateless.ModeDual, WithInstructions(testInstructions))
	defer teardown()

	for _, method := range []string{"server/discover", "tools/list"} {
		t.Run(method, func(t *testing.T) {
			r := decode(t, postStatelessJSON(t, url, map[string]any{
				"jsonrpc": "2.0", "id": 1, "method": method,
				"params": validMetaParams(),
			}, map[string]string{mcpProtocolVersionHeader: draftVersion}))
			if r.Error != nil {
				t.Fatalf("%s error: %+v", method, r.Error)
			}
			result := wireObject(t, r.Result)

			if method == "server/discover" && result["instructions"] != testInstructions {
				t.Errorf("discover result.instructions = %v, want %q", result["instructions"], testInstructions)
			}
			meta, _ := result["_meta"].(map[string]any)
			info, ok := meta[core.MetaKeyServerInfo].(map[string]any)
			if !ok {
				t.Fatalf("_meta serverInfo absent: %v", result)
			}
			if _, present := info["instructions"]; present {
				t.Errorf("_meta serverInfo carries instructions: %v", info)
			}
		})
	}
}
