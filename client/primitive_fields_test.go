package client_test

import (
	"testing"

	client "github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	server "github.com/panyam/mcpkit/server"
)

// TestClientListsDecodeSpecPrimitiveFields checks the #1562 fields survive a
// real server-to-client round trip on both wires.
func TestClientListsDecodeSpecPrimitiveFields(t *testing.T) {
	icon := core.Icon{Src: "https://example.com/i.png"}
	size := int64(42)
	for _, mode := range []client.ClientMode{client.ClientModeLegacyOnly, client.ClientModeStateless} {
		t.Run(mode.String(), func(t *testing.T) {
			srv := server.NewServer(core.Implementation{Name: "fields", Version: "1.0"})
			srv.RegisterTool(core.ToolDef{Name: "t", InputSchema: map[string]any{"type": "object"}, Icons: []core.Icon{icon}},
				func(core.ToolContext, core.ToolRequest) (core.ToolResponse, error) { return core.TextResult(""), nil })
			srv.RegisterResource(core.ResourceDef{URI: "file:///r", Name: "r", Size: &size, Icons: []core.Icon{icon},
				Meta: &core.ResourceMeta{UI: &core.UIMetadata{ResourceUri: "ui://r"}}},
				func(core.ResourceContext, core.ResourceRequest) (core.ResourceResult, error) {
					return core.ResourceResult{}, nil
				})
			srv.RegisterResourceTemplate(core.ResourceTemplate{URITemplate: "file:///r/{x}", Name: "rt", Icons: []core.Icon{icon}},
				func(core.ResourceContext, string, map[string]string) (core.ResourceResult, error) {
					return core.ResourceResult{}, nil
				})
			srv.RegisterPrompt(core.PromptDef{Name: "p", Icons: []core.Icon{icon}, Meta: map[string]any{"k": "v"},
				Arguments: []core.PromptArgument{{Name: "q", Title: "Query", Schema: map[string]any{"type": "string"}}}},
				func(core.PromptContext, core.PromptRequest) (core.PromptResponse, error) {
					return core.PromptResult{}, nil
				})
			c := newInstructionsClient(t, srv, core.Implementation{Name: "c", Version: "1"}, mode)

			tools, err := c.ListTools(t.Context())
			if err != nil || len(tools) != 1 || len(tools[0].Icons) != 1 || tools[0].Icons[0].Src != icon.Src {
				t.Errorf("tools = %+v, err %v", tools, err)
			}
			resources, err := c.ListResources(t.Context())
			if err != nil || len(resources) != 1 {
				t.Fatalf("resources = %+v, err %v", resources, err)
			}
			if r := resources[0]; r.Size == nil || *r.Size != 42 || len(r.Icons) != 1 || r.Meta == nil || r.Meta.UI.ResourceUri != "ui://r" {
				t.Errorf("resource = %+v", r)
			}
			templates, err := c.ListResourceTemplates(t.Context())
			if err != nil || len(templates) != 1 || len(templates[0].Icons) != 1 {
				t.Errorf("templates = %+v, err %v", templates, err)
			}
			prompts, err := c.ListPrompts(t.Context())
			if err != nil || len(prompts) != 1 {
				t.Fatalf("prompts = %+v, err %v", prompts, err)
			}
			p := prompts[0]
			if len(p.Icons) != 1 || p.Meta["k"] != "v" || len(p.Arguments) != 1 || p.Arguments[0].Title != "Query" {
				t.Errorf("prompt = %+v", p)
			}
			if p.Arguments[0].Schema != nil {
				t.Errorf("prompt argument schema reached the client: %v", p.Arguments[0].Schema)
			}
		})
	}
}
