// Package ui provides the MCP Apps extension (io.modelcontextprotocol/ui)
// for mcpkit servers. It declares the extension in the initialize response
// so clients know the server supports interactive HTML UIs.
//
// This is a separate Go module (github.com/panyam/mcpkit/ext/ui) so that
// the core mcpkit module stays zero-deps. Import this package to advertise
// MCP Apps support on your server.
//
// Usage:
//
//	srv := server.NewServer(info,
//	    server.WithExtension(ui.UIExtension{}),
//	)
package ui

import (
	"context"
	"fmt"
	"strings"

	"github.com/panyam/mcpkit/core"
)

// UIExtension declares support for the MCP Apps extension.
// Register it on the server to advertise UI rendering capability
// in the initialize response. Also validates that tools referencing
// ui:// resources have matching resource registrations (via RefValidator).
type UIExtension struct{}

// Extension returns the MCP Apps extension metadata.
func (UIExtension) Extension() core.Extension {
	return core.Extension{
		ID:          core.UIExtensionID,
		SpecVersion: "2026-01-26",
		Stability:   core.Experimental,
	}
}

// ValidateRefs checks that all tools with _meta.ui.resourceUri reference a
// registered resource or matching template. Returns warnings for unresolvable
// references. Implements core.RefValidator.
func (UIExtension) ValidateRefs(tools []core.ToolDef, resourceURIs []string, templateURIs []string) []string {
	resourceSet := make(map[string]bool, len(resourceURIs))
	for _, uri := range resourceURIs {
		resourceSet[uri] = true
	}

	var warnings []string
	for _, t := range tools {
		if t.Meta == nil || t.Meta.UI == nil || t.Meta.UI.ResourceUri == "" {
			continue
		}
		uri := t.Meta.UI.ResourceUri

		// Check exact resource match
		if resourceSet[uri] {
			continue
		}

		// Check template match
		if matchesAnyTemplate(uri, templateURIs) {
			continue
		}

		warnings = append(warnings, fmt.Sprintf("warning: tool %q references resource %q but no resource or template is registered for that URI", t.Name, uri))
	}
	return warnings
}

// ToolResourceRegistrar is the interface needed by RegisterAppTool to register
// tools, resources, and resource templates. Satisfied by *server.Server without importing it.
type ToolResourceRegistrar interface {
	RegisterTool(def core.ToolDef, handler core.ToolHandler)
	RegisterResource(def core.ResourceDef, handler core.ResourceHandler)
	RegisterResourceTemplate(def core.ResourceTemplate, handler core.TemplateHandler)
}

// AppToolConfig configures a tool + resource pair for RegisterAppTool.
type AppToolConfig struct {
	// Name is the tool identifier used in tools/call.
	Name string

	// Description is a human-readable summary of what the tool does.
	Description string

	// InputSchema is the JSON Schema for the tool's arguments.
	InputSchema any

	// ResourceURI is the ui:// URI for the app's HTML resource.
	ResourceURI string

	// ToolHandler handles tool invocations.
	ToolHandler core.ToolHandler

	// ResourceHandler serves the HTML content for the ui:// resource.
	ResourceHandler core.ResourceHandler

	// Visibility controls who can see/call this tool.
	// Nil means default (both model and app).
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
	// Nil means the host decides.
	SupportedDisplayModes []core.DisplayMode

	// TemplateHandler serves HTML content for a ui:// resource template.
	// Required when ResourceURI contains "{" (template variable).
	// When set, RegisterAppTool registers a resource template instead of
	// a concrete resource.
	TemplateHandler core.TemplateHandler
}

// RegisterAppTool registers both a tool (with _meta.ui metadata) and its
// matching ui:// resource in one call. Ensures the tool's resourceUri and
// the resource URI are consistent, and sets the correct MIME type automatically.
//
// Example:
//
//	ui.RegisterAppTool(srv, ui.AppToolConfig{
//	    Name:        "build_deck",
//	    Description: "Build a slide deck",
//	    InputSchema: map[string]any{"type": "object"},
//	    ResourceURI: "ui://decks/view",
//	    ToolHandler: buildDeckHandler,
//	    ResourceHandler: serveDeckHTML,
//	})
func RegisterAppTool(reg ToolResourceRegistrar, cfg AppToolConfig) {
	uiMeta := &core.UIMetadata{
		ResourceUri:           cfg.ResourceURI,
		Visibility:            cfg.Visibility,
		CSP:                   cfg.CSP,
		Permissions:           cfg.Permissions,
		PrefersBorder:         cfg.PrefersBorder,
		Domain:                cfg.Domain,
		SupportedDisplayModes: cfg.SupportedDisplayModes,
	}

	reg.RegisterTool(
		core.ToolDef{
			Name:        cfg.Name,
			Description: cfg.Description,
			InputSchema: cfg.InputSchema,
			Meta:        &core.ToolMeta{UI: uiMeta},
		},
		cfg.ToolHandler,
	)

	if strings.Contains(cfg.ResourceURI, "{") {
		if cfg.TemplateHandler == nil {
			panic("RegisterAppTool: template URI " + cfg.ResourceURI + " requires TemplateHandler, got nil")
		}
		reg.RegisterResourceTemplate(
			core.ResourceTemplate{
				URITemplate: cfg.ResourceURI,
				Name:        cfg.Name + " UI",
				MimeType:    core.AppMIMEType,
			},
			cfg.TemplateHandler,
		)
	} else {
		if cfg.ResourceHandler == nil {
			panic("RegisterAppTool: concrete URI " + cfg.ResourceURI + " requires ResourceHandler, got nil")
		}
		reg.RegisterResource(
			core.ResourceDef{
				URI:      cfg.ResourceURI,
				Name:     cfg.Name + " UI",
				MimeType: core.AppMIMEType,
			},
			cfg.ResourceHandler,
		)
	}
}

// RequestDisplayMode sends a display mode change notification to the client.
// Call this from a tool handler to request the host to change how the app
// is displayed (e.g., switch from inline to fullscreen).
//
// The notification is fire-and-forget; the host may ignore it if the
// requested mode is not supported.
func RequestDisplayMode(ctx context.Context, mode core.DisplayMode) {
	core.Notify(ctx, "notifications/ui/displayMode", map[string]any{
		"displayMode": mode,
	})
}

// ElicitWithApp sends an elicitation/create request with MCP Apps metadata.
// This is a convenience wrapper around core.Elicit that populates _meta.ui
// so the host can render a UI resource during input collection.
func ElicitWithApp(ctx context.Context, req core.ElicitationRequest, ui *core.UIMetadata) (core.ElicitationResult, error) {
	if req.Meta == nil {
		req.Meta = &core.ElicitationMeta{}
	}
	req.Meta.UI = ui
	return core.Elicit(ctx, req)
}

// SampleWithApp sends a sampling/createMessage request with MCP Apps metadata.
// This is a convenience wrapper around core.Sample that populates _meta.ui
// so the host can associate the sampling request with a UI resource.
func SampleWithApp(ctx context.Context, req core.CreateMessageRequest, ui *core.UIMetadata) (core.CreateMessageResult, error) {
	if req.Meta == nil {
		req.Meta = &core.SamplingMeta{}
	}
	req.Meta.UI = ui
	return core.Sample(ctx, req)
}

// matchesAnyTemplate checks if a URI matches any of the given URI templates.
// Uses simple segment-based matching (same logic as server dispatch).
func matchesAnyTemplate(uri string, templates []string) bool {
	uParts := strings.Split(uri, "/")
	for _, tmpl := range templates {
		tParts := strings.Split(tmpl, "/")
		if len(tParts) != len(uParts) {
			continue
		}
		matched := true
		for i, tp := range tParts {
			if strings.HasPrefix(tp, "{") && strings.HasSuffix(tp, "}") {
				continue // template variable matches anything
			}
			if tp != uParts[i] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}
