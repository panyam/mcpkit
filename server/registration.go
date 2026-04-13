package server

import core "github.com/panyam/mcpkit/core"

// Tool bundles a tool definition with its handler for single-struct
// registration via [Server.Register]. This is the recommended way to
// register tools — it keeps the definition and handler together as a
// single value, making it easy to build tool registries or load tools
// from configuration.
//
// Example:
//
//	srv.Register(server.Tool{
//	    ToolDef: core.ToolDef{Name: "echo", Description: "Echo input", InputSchema: schema},
//	    Handler: func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
//	        return core.TextResult("echoed"), nil
//	    },
//	})
type Tool struct {
	core.ToolDef
	Handler core.ToolHandler
}

// Resource bundles a resource definition with its handler.
type Resource struct {
	core.ResourceDef
	Handler core.ResourceHandler
}

// ResourceTemplate bundles a resource template definition with its handler.
type ResourceTemplate struct {
	core.ResourceTemplate
	Handler core.TemplateHandler
}

// Prompt bundles a prompt definition with its handler.
type Prompt struct {
	core.PromptDef
	Handler core.PromptHandler
}

// Register registers one or more tools, resources, resource templates, or
// prompts using single-struct registration. Each argument must be a [Tool],
// [Resource], [ResourceTemplate], or [Prompt] value.
//
// The existing two-argument methods (RegisterTool, RegisterResource, etc.)
// remain available for backward compatibility.
//
// Example:
//
//	srv.Register(
//	    server.Tool{ToolDef: core.ToolDef{Name: "a"}, Handler: handlerA},
//	    server.Resource{ResourceDef: core.ResourceDef{URI: "test://b"}, Handler: handlerB},
//	    server.Prompt{PromptDef: core.PromptDef{Name: "c"}, Handler: handlerC},
//	)
func (s *Server) Register(items ...any) {
	for _, item := range items {
		switch v := item.(type) {
		case Tool:
			s.RegisterTool(v.ToolDef, v.Handler)
		case Resource:
			s.RegisterResource(v.ResourceDef, v.Handler)
		case ResourceTemplate:
			s.RegisterResourceTemplate(v.ResourceTemplate, v.Handler)
		case Prompt:
			s.RegisterPrompt(v.PromptDef, v.Handler)
		}
	}
}
