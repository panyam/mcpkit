package refs

import "github.com/panyam/demokit"

// MCP spec references shared across host examples.
var (
	MCPAppsSpec = demokit.Ref{
		Name: "MCP Apps Extension",
		URL:  "https://modelcontextprotocol.io/extensions/apps/overview",
	}
	ExtAppsRepo = demokit.Ref{
		Name: "ext-apps repo (spec + reference impl)",
		URL:  "https://github.com/modelcontextprotocol/ext-apps",
	}
	MCPSpec = demokit.Ref{
		Name: "MCP Specification",
		URL:  "https://spec.modelcontextprotocol.io",
	}
	MCPKitDocs = demokit.Ref{
		Name: "mcpkit APPS_HOST.md",
		URL:  "https://github.com/panyam/mcpkit/blob/main/docs/APPS_HOST.md",
	}
)
