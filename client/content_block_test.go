package client_test

import (
	"testing"

	client "github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	server "github.com/panyam/mcpkit/server"
)

// TestToolResultCarriesResourceLinkAndBlockMeta checks a resource_link block,
// and annotations and _meta on a text block, survive tools/call on both wires.
func TestToolResultCarriesResourceLinkAndBlockMeta(t *testing.T) {
	size := int64(7)
	for _, mode := range []client.ClientMode{client.ClientModeLegacyOnly, client.ClientModeStateless} {
		t.Run(mode.String(), func(t *testing.T) {
			srv := server.NewServer(core.Implementation{Name: "links", Version: "1.0"})
			srv.RegisterTool(core.ToolDef{Name: "find", InputSchema: map[string]any{"type": "object"}},
				func(core.ToolContext, core.ToolRequest) (core.ToolResponse, error) {
					return core.ToolResult{Content: []core.Content{
						{Type: "text", Text: "found one", Annotations: map[string]any{"audience": []string{"user"}},
							Meta: map[string]any{"com.example/k": "v"}},
						core.ResourceLinkContent(core.ResourceDef{URI: "file:///hit", Name: "hit", Size: &size}),
					}}, nil
				})
			c := newInstructionsClient(t, srv, core.Implementation{Name: "c", Version: "1"}, mode)

			res, err := c.ToolCallFull(t.Context(), "find", map[string]any{})
			if err != nil {
				t.Fatalf("ToolCallFull: %v", err)
			}
			if len(res.Content) != 2 {
				t.Fatalf("content = %+v, want 2 blocks", res.Content)
			}
			text, link := res.Content[0], res.Content[1]
			if aud, _ := text.Annotations["audience"].([]any); len(aud) != 1 || text.Meta["com.example/k"] != "v" {
				t.Errorf("text block = %+v", text)
			}
			if link.Type != "resource_link" || link.URI != "file:///hit" || link.Name != "hit" || link.Size == nil || *link.Size != 7 {
				t.Errorf("link block = %+v", link)
			}
		})
	}
}
