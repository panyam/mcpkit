// Drop-in mcpkit equivalent of upstream's pdf-server example (default,
// i.e. no `--enable-interact` flag).
//
// Four-tool surface for drift-strict parity:
//
//	list_pdfs       (plain, visible to model)
//	read_pdf_bytes  (app-only)
//	display_pdf     (UI app tool with read-only viewer description)
//	save_pdf        (app-only)
//
// The remaining 5 tools (interact, submit_page_data, submit_save_data,
// submit_viewer_state, poll_pdf_commands) only register upstream when
// the operator passes `--enable-interact`. The harness used by upstream's
// servers.spec.ts does NOT pass that flag, so the default surface is
// 4 tools — that's what we mirror for the strict drift check.
//
// Scope of THIS fixture: the standard two servers.spec.ts tests
// ("loads app UI" + "screenshot matches golden"). Deliberately out of
// scope for this PR (tracked separately):
//   - pdf-annotations.spec.ts / pdf-annotations-api.spec.ts /
//     pdf-incremental-load.spec.ts / pdf-viewer-zoom.spec.ts — these
//     drive the `--enable-interact` surface and need the live interact
//     command queue + viewer long-poll wiring on the Go side.
//
// Run:  EXT_APPS_DIR=/tmp/ext-apps PORT=3101 go run .
package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/servicekit/middleware"
)

const (
	defaultPDF      = "https://arxiv.org/pdf/1706.03762"
	maxChunkBytes   = 512 * 1024
	resourceURI     = "ui://pdf-viewer/mcp-app.html"
	pdfDisplayName  = "PDF Server"
	pdfServerVer    = "2.0.0"
	defaultLogTag   = "[pdf-server] "
	defaultListenOn = "3101"
)

// Tool I/O Go types — the handlers return placeholder data because the
// visual test doesn't read the payload. The shapes only exist for the
// unmarshal/marshal path; tools/list parity is what the drift check verifies.

type listPdfsOutput struct {
	LocalFiles         []string `json:"localFiles"`
	AllowedDirectories []string `json:"allowedDirectories"`
	Truncated          bool     `json:"truncated"`
}

type readPdfBytesInput struct {
	URL       string  `json:"url"`
	Offset    float64 `json:"offset,omitempty"`
	ByteCount float64 `json:"byteCount,omitempty"`
}

type readPdfBytesOutput struct {
	URL        string  `json:"url"`
	Bytes      string  `json:"bytes"`
	Offset     float64 `json:"offset"`
	ByteCount  float64 `json:"byteCount"`
	TotalBytes float64 `json:"totalBytes"`
	HasMore    bool    `json:"hasMore"`
}

type displayPdfInput struct {
	URL  string  `json:"url,omitempty"`
	Page float64 `json:"page,omitempty"`
}

type displayPdfOutput struct {
	ViewUUID        string         `json:"viewUUID"`
	URL             string         `json:"url"`
	InitialPage     float64        `json:"initialPage"`
	TotalBytes      float64        `json:"totalBytes"`
	FormFieldValues map[string]any `json:"formFieldValues,omitempty"`
	FormFields      []any          `json:"formFields,omitempty"`
}

type savePdfInput struct {
	URL  string `json:"url"`
	Data string `json:"data"`
}

type savePdfOutput struct {
	FilePath string  `json:"filePath"`
	MtimeMs  float64 `json:"mtimeMs"`
}

var stringSchema = map[string]any{"type": "string"}

func main() {
	defaultPort := defaultListenOn
	if p := os.Getenv("PORT"); p != "" {
		defaultPort = p
	}
	addr := flag.String("addr", ":"+defaultPort, "listen address")
	flag.Parse()

	extAppsDir := os.Getenv("EXT_APPS_DIR")
	if extAppsDir == "" {
		extAppsDir = "/tmp/ext-apps"
	}
	htmlPath := filepath.Join(extAppsDir, "examples", "pdf-server", "dist", "mcp-app.html")
	htmlBytes, err := os.ReadFile(htmlPath)
	if err != nil {
		log.Fatalf("read %s: %v (set EXT_APPS_DIR and `npm run build` upstream)", htmlPath, err)
	}
	html := string(htmlBytes)

	opts := common.MCPServerOptions(*addr, defaultLogTag)
	opts = append(opts, server.WithExtension(&ui.UIExtension{}))
	srv := server.NewServer(
		core.ServerInfo{Name: pdfDisplayName, Version: pdfServerVer},
		opts...,
	)

	registerListPdfs(srv)
	registerReadPdfBytes(srv)
	registerDisplayPdf(srv, html)
	registerSavePdf(srv)

	cors := middleware.CORS(nil,
		middleware.CORSAllowMethods("GET", "POST", "DELETE", "OPTIONS"),
		middleware.CORSAllowHeaders("Content-Type", "Authorization", "Mcp-Session-Id", "Mcp-Protocol-Version"),
		middleware.CORSExposeHeaders("Mcp-Session-Id"),
	)

	log.Printf("pdf-server compat fixture listening on %s (MCP at /mcp)", *addr)
	log.Printf("serving mcp-app.html from %s (%d bytes)", htmlPath, len(html))
	if err := srv.Run(*addr, server.WithHandlerWrap(cors)); err != nil {
		log.Fatal(err)
	}
}

func registerListPdfs(srv *server.Server) {
	// Upstream uses server.tool(name, desc, {}, handler) with NO
	// outputSchema — the structuredContent is unvalidated but still
	// returned by the handler. TypedTool with Out=core.ToolResult is
	// the only shape that doesn't synthesize an outputSchema, so we
	// build the response inline and skip the typed output path.
	t := core.TypedTool[struct{}, core.ToolResult](
		"list_pdfs",
		"List available PDFs that can be displayed",
		func(ctx core.ToolContext, _ struct{}) (core.ToolResult, error) {
			return core.ToolResult{
				Content: []core.Content{{Type: "text", Text: "Any remote PDF accessible via HTTPS can also be loaded dynamically."}},
				StructuredContent: listPdfsOutput{
					LocalFiles:         []string{},
					AllowedDirectories: []string{},
				},
			}, nil
		},
		core.WithToolExecution(&core.ToolExecution{TaskSupport: core.TaskSupportForbidden}),
		core.WithInputSchemaOverride(map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
	)
	srv.RegisterTool(t.ToolDef, t.Handler)
}

func registerReadPdfBytes(srv *server.Server) {
	t := core.TypedTool[readPdfBytesInput, readPdfBytesOutput](
		"read_pdf_bytes",
		"Read a range of bytes from a PDF (max 512KB per request). The model should NOT call this tool directly.",
		func(ctx core.ToolContext, _ readPdfBytesInput) (readPdfBytesOutput, error) {
			return readPdfBytesOutput{}, nil
		},
		core.WithToolExecution(&core.ToolExecution{TaskSupport: core.TaskSupportForbidden}),
		core.WithToolMeta(&core.ToolMeta{
			UI: &core.UIMetadata{Visibility: []core.UIVisibility{core.UIVisibilityApp}},
		}),
		core.WithInputSchemaOverride(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "PDF URL or local file path",
				},
				"offset": map[string]any{
					"type":        "number",
					"minimum":     0,
					"default":     0,
					"description": "Byte offset",
				},
				"byteCount": map[string]any{
					"type":        "number",
					"minimum":     1,
					"maximum":     maxChunkBytes,
					"default":     maxChunkBytes,
					"description": "Bytes to read",
				},
			},
			"required": []string{"url"},
		}),
		core.WithOutputSchemaOverride(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": stringSchema,
				"bytes": map[string]any{
					"type":        "string",
					"description": "Base64 encoded bytes",
				},
				"offset":     map[string]any{"type": "number"},
				"byteCount":  map[string]any{"type": "number"},
				"totalBytes": map[string]any{"type": "number"},
				"hasMore":    map[string]any{"type": "boolean"},
			},
			"required": []string{"url", "bytes", "offset", "byteCount", "totalBytes", "hasMore"},
		}),
	)
	t.Title = "Read PDF Bytes"
	srv.RegisterTool(t.ToolDef, t.Handler)
}

func registerDisplayPdf(srv *server.Server, html string) {
	// Upstream's description switches on the --enable-interact flag.
	// Default (no flag → disableInteract = true) advertises a read-only
	// viewer; the interact-on description is longer and lists the
	// command sub-actions. We mirror the default (read-only) form.
	desc := `Show and render a PDF in a read-only viewer.

Use this tool when the user wants to view or read a PDF. The renderer displays the document for viewing. The widget exposes app-registered tools for page navigation, text extraction, searching, and zoom control.

Accepts local files (use list_pdfs), client MCP root directories, or any HTTPS URL.`

	ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[displayPdfInput, displayPdfOutput]{
		Name:        "display_pdf",
		Title:       "Display PDF",
		Description: desc,
		Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportForbidden},
		InputSchemaOverride: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"default":     defaultPDF,
					"description": "PDF URL or local file path",
				},
				"page": map[string]any{
					"type":        "number",
					"minimum":     1,
					"default":     1,
					"description": "Initial page",
				},
			},
		},
		OutputSchemaOverride: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"viewUUID": map[string]any{
					"type":        "string",
					"description": "UUID for this viewer instance",
				},
				"url":         stringSchema,
				"initialPage": map[string]any{"type": "number"},
				"totalBytes":  map[string]any{"type": "number"},
				"formFieldValues": map[string]any{
					"type": "object",
					"additionalProperties": map[string]any{
						"anyOf": []any{
							map[string]any{"type": "string"},
							map[string]any{"type": "boolean"},
						},
					},
					"description": "Form field values filled by the user via elicitation",
				},
				"formFields": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name":   stringSchema,
							"type":   stringSchema,
							"page":   map[string]any{"type": "number"},
							"label":  stringSchema,
							"x":      map[string]any{"type": "number"},
							"y":      map[string]any{"type": "number"},
							"width":  map[string]any{"type": "number"},
							"height": map[string]any{"type": "number"},
							"exportValue": map[string]any{
								"type":        "string",
								"description": "Radio button value — pass this to fill_form",
							},
							"options": map[string]any{
								"type":        "array",
								"items":       stringSchema,
								"description": "Dropdown/listbox option values",
							},
						},
						"required": []string{"name", "type", "page", "x", "y", "width", "height"},
					},
					"description": "Form fields with bounding boxes in model coordinates (top-left origin)",
				},
			},
			"required": []string{"viewUUID", "url", "initialPage", "totalBytes"},
		},
		Handler: func(ctx core.ToolContext, _ displayPdfInput) (displayPdfOutput, error) {
			return displayPdfOutput{
				ViewUUID:    "00000000-0000-0000-0000-000000000000",
				URL:         defaultPDF,
				InitialPage: 1,
				TotalBytes:  0,
			}, nil
		},
		ResourceURI: resourceURI,
		ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI: req.URI, MimeType: core.AppMIMEType, Text: html,
			}}}, nil
		},
	})
}

func registerSavePdf(srv *server.Server) {
	t := core.TypedTool[savePdfInput, savePdfOutput](
		"save_pdf",
		"Save annotated PDF bytes back to a local file. The model should NOT call this tool directly — use interact with action: save_as instead.",
		func(ctx core.ToolContext, _ savePdfInput) (savePdfOutput, error) {
			return savePdfOutput{}, nil
		},
		core.WithToolExecution(&core.ToolExecution{TaskSupport: core.TaskSupportForbidden}),
		core.WithToolMeta(&core.ToolMeta{
			UI: &core.UIMetadata{Visibility: []core.UIVisibility{core.UIVisibilityApp}},
		}),
		core.WithInputSchemaOverride(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "Original PDF URL or local file path",
				},
				"data": map[string]any{
					"type":        "string",
					"description": "Base64-encoded PDF bytes",
				},
			},
			"required": []string{"url", "data"},
		}),
		core.WithOutputSchemaOverride(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"filePath": stringSchema,
				"mtimeMs":  map[string]any{"type": "number"},
			},
			"required": []string{"filePath", "mtimeMs"},
		}),
	)
	t.Title = "Save PDF"
	srv.RegisterTool(t.ToolDef, t.Handler)
}
