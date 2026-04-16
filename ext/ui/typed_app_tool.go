package ui

import (
	core "github.com/panyam/mcpkit/core"
)

// TypedAppToolConfig configures a typed tool + resource pair for
// RegisterTypedAppTool. It replaces AppToolConfig's InputSchema and
// ToolHandler with a typed handler — the schema is auto-derived from
// the In type parameter, and the handler receives typed input.
type TypedAppToolConfig[In, Out any] struct {
	// Name is the tool identifier used in tools/call.
	Name string

	// Description is a human-readable summary of what the tool does.
	Description string

	// Handler handles tool invocations with typed input.
	Handler func(ctx core.ToolContext, input In) (Out, error)

	// ResourceURI is the ui:// URI for the app's HTML resource.
	ResourceURI string

	// ResourceHandler serves the HTML content for the ui:// resource.
	ResourceHandler core.ResourceHandler

	// Visibility controls who can see/call this tool.
	Visibility []core.UIVisibility

	// CSP declares external domains the app needs.
	CSP *core.UICSPConfig

	// Permissions lists browser capabilities the app requests.
	Permissions []string

	// PrefersBorder hints whether the host should draw a visible border.
	PrefersBorder *bool

	// Domain requests a dedicated sandbox origin for the app.
	Domain string

	// SupportedDisplayModes declares which display modes this app supports.
	SupportedDisplayModes []core.DisplayMode

	// TemplateHandler serves HTML content for a ui:// resource template.
	TemplateHandler core.TemplateHandler
}

// RegisterTypedAppTool registers a typed tool + resource pair. It auto-derives
// InputSchema from the In type parameter and wraps the typed handler, then
// delegates to RegisterAppTool for all the app-specific wiring (UI metadata,
// template detection, resource registration, concrete fallback generation).
//
// Example:
//
//	type addTaskInput struct {
//	    Title    string `json:"title" jsonschema:"required,description=Task title"`
//	    Priority string `json:"priority,omitempty" jsonschema:"enum=low,enum=medium,enum=high"`
//	}
//
//	ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[addTaskInput, string]{
//	    Name:        "add_task",
//	    Description: "Add a task to the board",
//	    Handler: func(ctx core.ToolContext, input addTaskInput) (string, error) {
//	        return fmt.Sprintf("Added task: %s", input.Title), nil
//	    },
//	    ResourceURI:     "ui://tasks/board",
//	    ResourceHandler: serveBoardHTML,
//	})
func RegisterTypedAppTool[In, Out any](reg ToolResourceRegistrar, cfg TypedAppToolConfig[In, Out]) {
	typed := core.TypedTool[In, Out](cfg.Name, cfg.Description, cfg.Handler)
	RegisterAppTool(reg, AppToolConfig{
		Name:                  cfg.Name,
		Description:           cfg.Description,
		InputSchema:           typed.InputSchema,
		ResourceURI:           cfg.ResourceURI,
		ToolHandler:           typed.Handler,
		ResourceHandler:       cfg.ResourceHandler,
		Visibility:            cfg.Visibility,
		CSP:                   cfg.CSP,
		Permissions:           cfg.Permissions,
		PrefersBorder:         cfg.PrefersBorder,
		Domain:                cfg.Domain,
		SupportedDisplayModes: cfg.SupportedDisplayModes,
		TemplateHandler:       cfg.TemplateHandler,
	})
}
