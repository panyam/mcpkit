package server

import (
	"fmt"

	core "github.com/panyam/mcpkit/core"
)

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
//	    Handler: func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
//	        return core.TextResult("echoed"), nil
//	    },
//	})
type Tool struct {
	core.ToolDef
	Handler core.ToolHandler
	// TaskCallbacks provides optional per-tool overrides for tasks/get and
	// tasks/result handlers. When set, the task protocol consults these
	// callbacks before falling through to the TaskStore. See [TaskCallbacks].
	TaskCallbacks *TaskCallbacks
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
// [Resource], [ResourceTemplate], [Prompt], or [core.TypedToolResult] value.
//
// The existing two-argument methods (RegisterTool, RegisterResource, etc.)
// remain available for backward compatibility.
//
// Register panics on an argument of any other type. The parameter is `...any`
// so that a single call can mix the primitive kinds, which means the type
// system cannot check it — an unrecognized value would otherwise be dropped
// with no diagnostic, producing a server that is silently missing a tool.
// Passing a pointer (&server.Tool{...} instead of server.Tool{...}) is the
// common way to hit this. Registration happens at startup, so a panic surfaces
// the mistake immediately rather than as a confusing "unknown tool" at call
// time.
//
// Example:
//
//	srv.Register(
//	    server.Tool{ToolDef: core.ToolDef{Name: "a"}, Handler: handlerA},
//	    server.Resource{ResourceDef: core.ResourceDef{URI: "test://b"}, Handler: handlerB},
//	    server.Prompt{PromptDef: core.PromptDef{Name: "c"}, Handler: handlerC},
//	)
func (s *Server) Register(items ...any) {
	for i, item := range items {
		switch v := item.(type) {
		case Tool:
			s.RegisterTool(v.ToolDef, v.Handler)
			if v.TaskCallbacks != nil {
				s.Registry().SetToolCallbacks(v.Name, v.TaskCallbacks)
			}
		case core.TypedToolResult:
			s.RegisterTool(v.ToolDef, v.Handler)
		case Resource:
			s.RegisterResource(v.ResourceDef, v.Handler)
		case ResourceTemplate:
			s.RegisterResourceTemplate(v.ResourceTemplate, v.Handler)
		case Prompt:
			s.RegisterPrompt(v.PromptDef, v.Handler)
		default:
			panic(fmt.Sprintf(
				"server.Register: unsupported type %T at argument %d; "+
					"want server.Tool, server.Resource, server.ResourceTemplate, "+
					"server.Prompt, or core.TypedToolResult (note: pass the value, not a pointer)",
				item, i))
		}
	}
}
