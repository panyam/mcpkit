package main

import (
	"bytes"
	_ "embed"
	"html/template"
	"log"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
)

// SEP-2356 Phase 2.1 — apps-mode demonstration. The two HTML files below
// embed the bridge JS and use mcp.selectFile / mcp.selectFiles to drive the
// existing upload_image and analyze_documents tools through an in-iframe
// picker. This is the human-in-the-loop case that file-uploads-wg flagged
// MCP Apps was missing — the agentic case lives in main.go's tool handlers.

//go:embed apps/upload-image.html
var uploadImageAppHTML string

//go:embed apps/analyze-documents.html
var analyzeDocumentsAppHTML string

// registerAppsTools installs two MCP App tools that share handlers with the
// regular SEP-2356 demo tools. Each app exposes a ui:// resource whose HTML
// hosts a button that calls mcp.selectFile / mcp.selectFiles, then routes
// the resulting data URI(s) through mcp.callTool to the underlying tool.
func registerAppsTools(srv *server.Server, uploadDir string) {
	const fiveMB = 5 * 1024 * 1024

	imageMax := fiveMB
	imageDesc := core.FileInputDescriptor{
		Accept:  []string{"image/*"},
		MaxSize: &imageMax,
	}

	uploadImageHTML := mustRender("upload_image_app", uploadImageAppHTML, "apps_upload_image")
	ui.RegisterAppTool(srv, ui.AppToolConfig{
		Name:        "apps_upload_image",
		Description: "MCP App that opens an in-iframe file picker and uploads the chosen image via upload_image.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"image": core.FileInputProperty(imageDesc),
				"caption": map[string]any{
					"type":        "string",
					"description": "Optional caption to print alongside the image metadata.",
				},
			},
		},
		ResourceURI: "ui://file-inputs/upload-image",
		Visibility:  []core.UIVisibility{core.UIVisibilityModel, core.UIVisibilityApp},
		ToolHandler: func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			// When invoked by the model with `image` already populated, fall
			// through to the regular handler. When invoked by the user
			// clicking the in-app button (which round-trips back as
			// mcp.callTool("upload_image", ...)), this branch is also fine —
			// the host treats it as the same tool.
			return uploadImageHandler(ctx, req, uploadDir)
		},
		ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI: req.URI, MimeType: core.AppMIMEType, Text: uploadImageHTML,
			}}}, nil
		},
	})

	pdfDesc := core.FileInputDescriptor{
		Accept: []string{"application/pdf", ".pdf"},
	}

	analyzeDocsHTML := mustRender("analyze_docs_app", analyzeDocumentsAppHTML, "apps_analyze_documents")
	ui.RegisterAppTool(srv, ui.AppToolConfig{
		Name:        "apps_analyze_documents",
		Description: "MCP App that opens an in-iframe multi-file picker and analyzes the chosen PDFs via analyze_documents.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"documents": core.FileInputArrayProperty(pdfDesc),
			},
		},
		ResourceURI: "ui://file-inputs/analyze-documents",
		Visibility:  []core.UIVisibility{core.UIVisibilityModel, core.UIVisibilityApp},
		ToolHandler: func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return analyzeDocumentsHandler(ctx, req, uploadDir)
		},
		ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI: req.URI, MimeType: core.AppMIMEType, Text: analyzeDocsHTML,
			}}}, nil
		},
	})
}

// mustRender pre-renders an apps HTML template against the bridge data so
// the runtime ResourceHandler returns a static string. Bridge JS gets baked
// in via {{ .Bridge.BridgeJS }}; failure here is a startup-only programmer
// error (template syntax bug in the embedded HTML), so panic.
func mustRender(name, raw, appName string) string {
	tmpl := template.Must(template.New(name).Parse(raw))
	template.Must(tmpl.Parse(ui.BridgeTemplateDef()))
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, struct {
		Bridge ui.BridgeData
	}{
		Bridge: ui.NewBridgeData(appName, "0.1.0"),
	}); err != nil {
		log.Fatalf("[file-inputs-demo] render %s: %v", name, err)
	}
	return buf.String()
}
