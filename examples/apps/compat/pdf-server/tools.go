package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
)

// --- Shared schema constants ------------------------------------------------

var stringSchema = map[string]any{"type": "string"}

// interactActionEnum is the action-name enum reused by interact's top-level
// `action` field and by each `commands[].action` element.
var interactActionEnum = []string{
	"navigate", "search", "find", "search_navigate", "zoom",
	"add_annotations", "update_annotations", "remove_annotations",
	"highlight_text", "fill_form", "get_text", "get_screenshot",
	"get_viewer_state", "save_as",
}

// interactCommandSchema is the `items` shape of the commands[] array AND
// the per-action schema upstream uses when action is given at the top level.
// Built once; reused at registration time.
var interactCommandSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"action": map[string]any{
			"type":        "string",
			"enum":        interactActionEnum,
			"description": "Action to perform",
		},
		"page": map[string]any{
			"type":        "number",
			"minimum":     1,
			"description": "Page number (for navigate, highlight_text, get_screenshot, get_text)",
		},
		"query": map[string]any{
			"type":        "string",
			"description": "Search text (for search / find / highlight_text)",
		},
		"matchIndex": map[string]any{
			"type":        "number",
			"minimum":     0,
			"description": "Match index (for search_navigate)",
		},
		"scale": map[string]any{
			"type":        "number",
			"minimum":     0.5,
			"maximum":     3.0,
			"description": "Zoom scale, 1.0 = 100% (for zoom)",
		},
		"annotations": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
			},
			"description": "Annotation objects (see types in description). Each needs: id, type, page. For update_annotations only id+type are required.",
		},
		"ids": map[string]any{
			"type":        "array",
			"items":       stringSchema,
			"description": "Annotation IDs (for remove_annotations)",
		},
		"color": map[string]any{
			"type":        "string",
			"description": "Color override (for highlight_text)",
		},
		"content": map[string]any{
			"type":        "string",
			"description": "Tooltip/note content (for highlight_text)",
		},
		"fields": map[string]any{
			"type":        "array",
			"items":       formFieldSchema,
			"description": "Form fields to fill (for fill_form): { name, value } where value is string or boolean",
		},
		"intervals": map[string]any{
			"type":        "array",
			"items":       pageIntervalSchema,
			"description": "Page ranges for get_text. Each has optional start/end. [{start:1,end:5}], [{}] = all pages. Max 20 pages.",
		},
		"path": map[string]any{
			"type":        "string",
			"description": "Target file path for save_as. Absolute path or file:// URL. Omit to overwrite the original file (requires overwrite: true).",
		},
		"overwrite": map[string]any{
			"type":        "boolean",
			"description": "Overwrite if file exists (for save_as). Default false.",
		},
	},
	"required": []string{"action"},
}

// formFieldSchema mirrors upstream's FormField zod object — used as the
// items schema of fill_form's `fields` array.
var formFieldSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"name": stringSchema,
		"value": map[string]any{
			"anyOf": []any{
				map[string]any{"type": "string"},
				map[string]any{"type": "boolean"},
			},
		},
	},
	"required": []string{"name", "value"},
}

var pageIntervalSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"start": map[string]any{"type": "number", "minimum": 1},
		"end":   map[string]any{"type": "number", "minimum": 1},
	},
}

// --- I/O Go types -----------------------------------------------------------

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

type displayPdfInput struct {
	URL              string  `json:"url,omitempty"`
	Page             float64 `json:"page,omitempty"`
	ElicitFormInputs bool    `json:"elicit_form_inputs,omitempty"`
}

type displayPdfOutput struct {
	ViewUUID    string  `json:"viewUUID"`
	URL         string  `json:"url"`
	InitialPage float64 `json:"initialPage"`
	TotalBytes  float64 `json:"totalBytes"`
}

type interactInput struct {
	ViewUUID    string                   `json:"viewUUID"`
	Action      string                   `json:"action,omitempty"`
	Page        *float64                 `json:"page,omitempty"`
	Query       string                   `json:"query,omitempty"`
	MatchIndex  *float64                 `json:"matchIndex,omitempty"`
	Scale       *float64                 `json:"scale,omitempty"`
	Annotations []map[string]any         `json:"annotations,omitempty"`
	IDs         []string                 `json:"ids,omitempty"`
	Color       string                   `json:"color,omitempty"`
	Content     string                   `json:"content,omitempty"`
	Fields      []map[string]any         `json:"fields,omitempty"`
	Intervals   []map[string]any         `json:"intervals,omitempty"`
	Path        string                   `json:"path,omitempty"`
	Overwrite   bool                     `json:"overwrite,omitempty"`
	Commands    []map[string]any         `json:"commands,omitempty"`
}

type submitPageDataInput struct {
	RequestID string           `json:"requestId"`
	Pages     []map[string]any `json:"pages"`
}

type submitSaveDataInput struct {
	RequestID string `json:"requestId"`
	Data      string `json:"data,omitempty"`
	Error     string `json:"error,omitempty"`
}

type submitViewerStateInput struct {
	RequestID string `json:"requestId"`
	State     string `json:"state,omitempty"`
	Error     string `json:"error,omitempty"`
}

type pollPdfCommandsInput struct {
	ViewUUID string `json:"viewUUID"`
}

type savePdfInput struct {
	URL  string `json:"url"`
	Data string `json:"data"`
}

type savePdfOutput struct {
	FilePath string  `json:"filePath"`
	MtimeMs  float64 `json:"mtimeMs"`
}

// --- Tool registrations -----------------------------------------------------

// registerAllTools wires the 9-tool `--enable-interact` surface onto the
// server, sharing the hub for all stateful tools (interact + submit_* +
// poll_pdf_commands) and the html bytes for display_pdf's UI resource.
func registerAllTools(srv *server.Server, h *hub, html string) {
	registerListPdfs(srv)
	registerReadPdfBytes(srv)
	registerDisplayPdf(srv, html)
	registerInteract(srv, h)
	registerPollPdfCommands(srv, h)
	registerSubmitPageData(srv, h)
	registerSubmitSaveData(srv, h)
	registerSubmitViewerState(srv, h)
	registerSavePdf(srv)
}

func registerListPdfs(srv *server.Server) {
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
	t := core.TypedTool[readPdfBytesInput, core.ToolResult](
		"read_pdf_bytes",
		"Read a range of bytes from a PDF (max 512KB per request). The model should NOT call this tool directly.",
		func(ctx core.ToolContext, in readPdfBytesInput) (core.ToolResult, error) {
			res, err := fetchPDFRange(ctx, in.URL, int(in.Offset), int(in.ByteCount))
			if err != nil {
				return core.ToolResult{
					Content: []core.Content{{Type: "text", Text: fmt.Sprintf("Error: %v", err)}},
					IsError: true,
				}, nil
			}
			return core.ToolResult{
				Content: []core.Content{{
					Type: "text",
					Text: fmt.Sprintf("%d bytes at %d/%d", res.ByteCount, res.Offset, res.TotalBytes),
				}},
				StructuredContent: res,
			}, nil
		},
		core.WithToolExecution(&core.ToolExecution{TaskSupport: core.TaskSupportForbidden}),
		core.WithToolMeta(&core.ToolMeta{
			UI: &core.UIMetadata{Visibility: []core.UIVisibility{core.UIVisibilityApp}},
		}),
		core.WithInputSchemaPatch(func(s *core.SchemaBuilder) {
			s.Prop("url").Desc("PDF URL or local file path").Required()
			s.Prop("offset").Type("number").Desc("Byte offset").Min(0).Default(0)
			s.Prop("byteCount").Type("number").
				Desc("Bytes to read").
				Min(1).Max(maxChunkBytes).Default(maxChunkBytes)
		}),
		// Output handler returns core.ToolResult, so reflection skips the
		// output schema — Override is the only path for the wire shape.
		core.WithOutputSchemaOverride(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url":        stringSchema,
				"bytes":      map[string]any{"type": "string", "description": "Base64 encoded bytes"},
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

// registerDisplayPdf mirrors upstream's `--enable-interact` mode.
//
// Output goes through core.ToolResult so we can carry both
// structuredContent (the typed body) AND _meta extras (viewUUID,
// interactEnabled, writable) — the strings the
// "display_pdf result includes viewUUID and interactEnabled meta" test
// substring-checks for.
func registerDisplayPdf(srv *server.Server, html string) {
	desc := `Open a PDF in an interactive viewer. Call this ONCE per PDF.

**All follow-up actions go through the ` + "`interact`" + ` tool** with the returned viewUUID — annotating, signing, stamping, filling forms, navigating, searching, extracting text/screenshots. Calling display_pdf again creates a SEPARATE viewer with a different viewUUID — interact calls using the new UUID will not reach the viewer the user already sees.

Returns a viewUUID in structuredContent. Pass it to ` + "`interact`" + `:
- add_annotations, update_annotations, remove_annotations, highlight_text
- fill_form (fill PDF form fields)
- navigate, search, find, search_navigate, zoom
- get_text, get_screenshot, get_viewer_state (extract content / read selection & current page)
- save_as (write annotated PDF to disk)

Accepts local files (use list_pdfs), client MCP root directories, or any HTTPS URL.
Set ` + "`elicit_form_inputs`" + ` to true to prompt the user to fill form fields before display.`

	ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[displayPdfInput, core.ToolResult]{
		Name:        "display_pdf",
		Title:       "Display PDF",
		Description: desc,
		Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportForbidden},
		InputSchemaPatch: func(s *core.SchemaBuilder) {
			s.Prop("url").Desc("PDF URL or local file path").Default(defaultPDF)
			s.Prop("page").Type("number").Desc("Initial page").Min(1).Default(1)
			s.Prop("elicit_form_inputs").Type("boolean").
				Desc("If true and the PDF has form fields, prompt the user to fill them before displaying").
				Default(false)
		},
		// Output handler returns core.ToolResult — Override is the only path
		// (typed Out=ToolResult skips reflection + patch). Keeps the deeply
		// nested formFields[].items + formFieldValues record-of-union shapes
		// matching upstream byte-for-byte.
		OutputSchemaOverride: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"viewUUID": map[string]any{
					"type":        "string",
					"description": "UUID for this viewer instance — pass to interact tool",
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
		Handler: func(ctx core.ToolContext, in displayPdfInput) (core.ToolResult, error) {
			url := in.URL
			if url == "" {
				url = defaultPDF
			}
			pageNum := in.Page
			if pageNum < 1 {
				pageNum = 1
			}
			uuid := randomUUID()
			total := probeTotalBytes(ctx, url)

			textBody := fmt.Sprintf(`PDF opened. viewUUID: %s

→ To annotate, sign, stamp, fill forms, navigate, extract, or save to a file: call `+"`interact`"+` with this viewUUID.
→ DO NOT call display_pdf again — that spawns a separate viewer with a different viewUUID; your interact calls would target the new empty one, not the one the user is looking at.

URL: %s`, uuid, url)

			return core.ToolResult{
				Content: []core.Content{{Type: "text", Text: textBody}},
				StructuredContent: displayPdfOutput{
					ViewUUID:    uuid,
					URL:         url,
					InitialPage: pageNum,
					TotalBytes:  float64(total),
				},
				Meta: &core.ToolResultMeta{
					Extras: map[string]any{
						"viewUUID":        uuid,
						"interactEnabled": true,
						"writable":        false,
					},
				},
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

// --- interact (the kitchen-sink action-router) ------------------------------

func registerInteract(srv *server.Server, h *hub) {
	desc := interactDescription()
	t := core.TypedTool[interactInput, core.ToolResult](
		"interact",
		desc,
		func(ctx core.ToolContext, in interactInput) (core.ToolResult, error) {
			return dispatchInteract(ctx, h, in)
		},
		core.WithToolExecution(&core.ToolExecution{TaskSupport: core.TaskSupportForbidden}),
		core.WithInputSchemaOverride(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"viewUUID": map[string]any{
					"type":        "string",
					"description": "The viewUUID of the PDF viewer (from display_pdf result)",
				},
				"action": map[string]any{
					"type":        "string",
					"enum":        interactActionEnum,
					"description": "Action to perform (for single command). Use `commands` array for batching.",
				},
				"page":        interactCommandSchema["properties"].(map[string]any)["page"],
				"query":       interactCommandSchema["properties"].(map[string]any)["query"],
				"matchIndex":  interactCommandSchema["properties"].(map[string]any)["matchIndex"],
				"scale":       interactCommandSchema["properties"].(map[string]any)["scale"],
				"annotations": interactCommandSchema["properties"].(map[string]any)["annotations"],
				"ids":         interactCommandSchema["properties"].(map[string]any)["ids"],
				"color":       interactCommandSchema["properties"].(map[string]any)["color"],
				"content":     interactCommandSchema["properties"].(map[string]any)["content"],
				"fields":      interactCommandSchema["properties"].(map[string]any)["fields"],
				"intervals":   interactCommandSchema["properties"].(map[string]any)["intervals"],
				"path":        interactCommandSchema["properties"].(map[string]any)["path"],
				"overwrite":   interactCommandSchema["properties"].(map[string]any)["overwrite"],
				"commands": map[string]any{
					"type":        "array",
					"items":       interactCommandSchema,
					"description": "Array of commands to execute sequentially. More efficient than separate calls. Tip: end with get_pages+getScreenshots to verify changes.",
				},
			},
			"required": []string{"viewUUID"},
		}),
	)
	t.Title = "Interact with PDF"
	srv.RegisterTool(t.ToolDef, t.Handler)
}

// dispatchInteract turns the input envelope into a sequence of
// commands (either batch via `commands` or single via `action`) and
// runs each through processCommand. Failures stop the batch; each
// successful step contributes one content item to the result.
func dispatchInteract(ctx core.ToolContext, h *hub, in interactInput) (core.ToolResult, error) {
	cmdList := buildCommandList(in)
	if len(cmdList) == 0 {
		return core.ToolResult{
			Content: []core.Content{{
				Type: "text",
				Text: "No action or commands specified. Provide either `action` (single command) or `commands` (batch).",
			}},
			IsError: true,
		}, nil
	}

	var allContent []core.Content
	for i, cmd := range cmdList {
		content, isErr := processCommand(ctx, h, in.ViewUUID, cmd)
		if isErr {
			errText := summarizeText(content)
			if len(cmdList) > 1 {
				allContent = append(allContent, core.Content{
					Type: "text",
					Text: fmt.Sprintf("ERROR at step %d/%d (%v): %s", i+1, len(cmdList), cmd["action"], errText),
				})
			} else {
				return core.ToolResult{
					Content: []core.Content{{Type: "text", Text: fmt.Sprintf("ERROR: %s", errText)}},
					IsError: true,
				}, nil
			}
			break
		}
		if len(content) == 1 {
			allContent = append(allContent, content[0])
		} else {
			// Squash multi-part successes into one slot so 1:1 batch indexing holds.
			parts := []string{}
			for _, c := range content {
				if c.Type == "text" {
					parts = append(parts, c.Text)
				}
			}
			allContent = append(allContent, core.Content{Type: "text", Text: strings.Join(parts, "\n")})
		}
	}
	return core.ToolResult{Content: allContent}, nil
}

func buildCommandList(in interactInput) []map[string]any {
	if len(in.Commands) > 0 {
		return in.Commands
	}
	if in.Action == "" {
		return nil
	}
	single := map[string]any{"action": in.Action}
	if in.Page != nil {
		single["page"] = *in.Page
	}
	if in.Query != "" {
		single["query"] = in.Query
	}
	if in.MatchIndex != nil {
		single["matchIndex"] = *in.MatchIndex
	}
	if in.Scale != nil {
		single["scale"] = *in.Scale
	}
	if len(in.Annotations) > 0 {
		single["annotations"] = in.Annotations
	}
	if len(in.IDs) > 0 {
		single["ids"] = in.IDs
	}
	if in.Color != "" {
		single["color"] = in.Color
	}
	if in.Content != "" {
		single["content"] = in.Content
	}
	if len(in.Fields) > 0 {
		single["fields"] = in.Fields
	}
	if len(in.Intervals) > 0 {
		single["intervals"] = in.Intervals
	}
	if in.Path != "" {
		single["path"] = in.Path
	}
	if in.Overwrite {
		single["overwrite"] = in.Overwrite
	}
	return []map[string]any{single}
}

// processCommand routes one command. Returns the content slice for that
// step and an isError flag. Three families:
//
//   - fire-and-forget (navigate, search, find, search_navigate, zoom,
//     add_annotations, update_annotations, remove_annotations,
//     highlight_text, fill_form): enqueue + ack
//   - request/response (get_text, get_screenshot, get_viewer_state):
//     enqueue + await submit_*
//   - save_as: enqueue + await submit_save_data (then would persist;
//     this fixture stops after the await — none of the targeted tests
//     exercise on-disk saving)
func processCommand(ctx core.ToolContext, h *hub, viewUUID string, cmd map[string]any) ([]core.Content, bool) {
	action, _ := cmd["action"].(string)
	switch action {
	case "navigate", "search", "find", "search_navigate", "zoom",
		"add_annotations", "update_annotations", "remove_annotations",
		"highlight_text", "fill_form":
		viewerCmd := pdfCommand{"type": action}
		for k, v := range cmd {
			if k == "action" {
				continue
			}
			viewerCmd[k] = v
		}
		h.enqueueCommand(viewUUID, viewerCmd)
		return []core.Content{{Type: "text", Text: fmt.Sprintf("Queued: %s", action)}}, false

	case "get_text":
		reqID := randomUUID()
		intervals := cmd["intervals"]
		if intervals == nil {
			if p, ok := cmd["page"].(float64); ok {
				intervals = []map[string]any{{"start": p, "end": p}}
			} else {
				intervals = []map[string]any{{}}
			}
		}
		h.enqueueCommand(viewUUID, pdfCommand{
			"type":           "get_pages",
			"requestId":      reqID,
			"intervals":      intervals,
			"getText":        true,
			"getScreenshots": false,
		})
		res := h.awaitPage(ctx, reqID)
		if res.err != "" {
			return []core.Content{{Type: "text", Text: res.err}}, true
		}
		return renderPageDataAsText(res.payload), false

	case "get_screenshot":
		p, ok := cmd["page"].(float64)
		if !ok {
			return []core.Content{{Type: "text", Text: "get_screenshot requires `page`"}}, true
		}
		reqID := randomUUID()
		h.enqueueCommand(viewUUID, pdfCommand{
			"type":           "get_pages",
			"requestId":      reqID,
			"intervals":      []map[string]any{{"start": p, "end": p}},
			"getText":        false,
			"getScreenshots": true,
		})
		res := h.awaitPage(ctx, reqID)
		if res.err != "" {
			return []core.Content{{Type: "text", Text: res.err}}, true
		}
		return renderPageDataAsImage(res.payload), false

	case "get_viewer_state":
		reqID := randomUUID()
		h.enqueueCommand(viewUUID, pdfCommand{
			"type":      "get_viewer_state",
			"requestId": reqID,
		})
		res := h.awaitState(ctx, reqID)
		if res.err != "" {
			return []core.Content{{Type: "text", Text: res.err}}, true
		}
		return []core.Content{{Type: "text", Text: res.payload}}, false

	case "save_as":
		reqID := randomUUID()
		h.enqueueCommand(viewUUID, pdfCommand{
			"type":      "save_as",
			"requestId": reqID,
		})
		res := h.awaitSave(ctx, reqID)
		if res.err != "" {
			return []core.Content{{Type: "text", Text: res.err}}, true
		}
		// On-disk persistence is out of scope for this fixture — none of
		// the targeted tests drive save_as. Acknowledge the data so
		// drift-strict surface parity still holds.
		return []core.Content{{Type: "text", Text: "Saved (in-memory ack — on-disk persistence not implemented in compat fixture)"}}, false

	default:
		return []core.Content{{Type: "text", Text: fmt.Sprintf("Unknown action: %q", action)}}, true
	}
}

// renderPageDataAsText turns the JSON payload submit_page_data carried
// (an array of {page, text?, image?}) into the text-block content
// upstream emits for get_text.
func renderPageDataAsText(payload string) []core.Content {
	var pages []struct {
		Page float64 `json:"page"`
		Text *string `json:"text,omitempty"`
	}
	if err := json.Unmarshal([]byte(payload), &pages); err != nil {
		return []core.Content{{Type: "text", Text: payload}}
	}
	var out []core.Content
	for _, p := range pages {
		if p.Text != nil {
			out = append(out, core.Content{
				Type: "text",
				Text: fmt.Sprintf("--- Page %d ---\n%s", int(p.Page), *p.Text),
			})
		}
	}
	if len(out) == 0 {
		out = []core.Content{{Type: "text", Text: "No text content returned"}}
	}
	return out
}

// renderPageDataAsImage extracts the first base64 image block from the
// submit_page_data payload for get_screenshot. Falls back to a text
// hint if no image was produced.
func renderPageDataAsImage(payload string) []core.Content {
	var pages []struct {
		Page  float64 `json:"page"`
		Image *string `json:"image,omitempty"`
	}
	if err := json.Unmarshal([]byte(payload), &pages); err != nil {
		return []core.Content{{Type: "text", Text: payload}}
	}
	for _, p := range pages {
		if p.Image != nil && *p.Image != "" {
			return []core.Content{{Type: "image", Data: *p.Image, MimeType: "image/png"}}
		}
	}
	return []core.Content{{Type: "text", Text: "No screenshot data returned"}}
}

func summarizeText(content []core.Content) string {
	var parts []string
	for _, c := range content {
		if c.Type == "text" {
			parts = append(parts, c.Text)
		}
	}
	return strings.TrimSpace(strings.TrimPrefix(strings.Join(parts, " — "), "Error: "))
}

// --- submit_* + poll + save_pdf --------------------------------------------

func registerPollPdfCommands(srv *server.Server, h *hub) {
	t := core.TypedTool[pollPdfCommandsInput, core.ToolResult](
		"poll_pdf_commands",
		"Poll for pending commands for a PDF viewer. The model should NOT call this tool directly.",
		func(ctx core.ToolContext, in pollPdfCommandsInput) (core.ToolResult, error) {
			cmds := h.longPoll(ctx, in.ViewUUID)
			// Mirror upstream's wire shape: a structuredContent.commands
			// array. The text content carries the count for debugging.
			return core.ToolResult{
				Content: []core.Content{{Type: "text", Text: fmt.Sprintf("%d command(s)", len(cmds))}},
				StructuredContent: map[string]any{
					"commands": cmds,
				},
			}, nil
		},
		core.WithToolExecution(&core.ToolExecution{TaskSupport: core.TaskSupportForbidden}),
		core.WithToolMeta(&core.ToolMeta{
			UI: &core.UIMetadata{Visibility: []core.UIVisibility{core.UIVisibilityApp}},
		}),
		core.WithInputSchemaPatch(func(s *core.SchemaBuilder) {
			s.Prop("viewUUID").Desc("The viewUUID of the PDF viewer").Required()
		}),
	)
	t.Title = "Poll PDF Commands"
	srv.RegisterTool(t.ToolDef, t.Handler)
}

func registerSubmitPageData(srv *server.Server, h *hub) {
	t := core.TypedTool[submitPageDataInput, core.ToolResult](
		"submit_page_data",
		"Submit rendered page data for a get_pages request (used by viewer). The model should NOT call this tool directly.",
		func(ctx core.ToolContext, in submitPageDataInput) (core.ToolResult, error) {
			payload, _ := json.Marshal(in.Pages)
			if !h.resolvePending("page", in.RequestID, string(payload), "") {
				return core.ToolResult{
					Content: []core.Content{{Type: "text", Text: fmt.Sprintf("No pending request for %s", in.RequestID)}},
					IsError: true,
				}, nil
			}
			return core.ToolResult{Content: []core.Content{{Type: "text", Text: fmt.Sprintf("Submitted %d page(s)", len(in.Pages))}}}, nil
		},
		core.WithToolExecution(&core.ToolExecution{TaskSupport: core.TaskSupportForbidden}),
		core.WithToolMeta(&core.ToolMeta{
			UI: &core.UIMetadata{Visibility: []core.UIVisibility{core.UIVisibilityApp}},
		}),
		core.WithInputSchemaOverride(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"requestId": map[string]any{
					"type":        "string",
					"description": "The request ID from the get_pages command",
				},
				"pages": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"page": map[string]any{"type": "number"},
							"text": stringSchema,
							"image": map[string]any{
								"type":        "string",
								"description": "Base64 PNG image data",
							},
						},
						"required": []string{"page"},
					},
					"description": "Page data entries",
				},
			},
			"required": []string{"requestId", "pages"},
		}),
	)
	t.Title = "Submit Page Data"
	srv.RegisterTool(t.ToolDef, t.Handler)
}

func registerSubmitSaveData(srv *server.Server, h *hub) {
	t := core.TypedTool[submitSaveDataInput, core.ToolResult](
		"submit_save_data",
		"Submit annotated PDF bytes for a save_as request (used by viewer). The model should NOT call this tool directly.",
		func(ctx core.ToolContext, in submitSaveDataInput) (core.ToolResult, error) {
			ok := h.resolvePending("save", in.RequestID, in.Data, in.Error)
			if !ok {
				return core.ToolResult{
					Content: []core.Content{{Type: "text", Text: fmt.Sprintf("No pending request for %s", in.RequestID)}},
					IsError: true,
				}, nil
			}
			return core.ToolResult{Content: []core.Content{{Type: "text", Text: "Submitted"}}}, nil
		},
		core.WithToolExecution(&core.ToolExecution{TaskSupport: core.TaskSupportForbidden}),
		core.WithToolMeta(&core.ToolMeta{
			UI: &core.UIMetadata{Visibility: []core.UIVisibility{core.UIVisibilityApp}},
		}),
		core.WithInputSchemaPatch(func(s *core.SchemaBuilder) {
			s.Prop("requestId").Desc("The request ID from the save_as command").Required()
			s.Prop("data").Desc("Base64-encoded PDF bytes")
			s.Prop("error").Desc("Error message if the viewer failed to build bytes")
		}),
	)
	t.Title = "Submit Save Data"
	srv.RegisterTool(t.ToolDef, t.Handler)
}

func registerSubmitViewerState(srv *server.Server, h *hub) {
	t := core.TypedTool[submitViewerStateInput, core.ToolResult](
		"submit_viewer_state",
		"Submit a viewer-state snapshot for a get_viewer_state request (used by viewer). The model should NOT call this tool directly.",
		func(ctx core.ToolContext, in submitViewerStateInput) (core.ToolResult, error) {
			ok := h.resolvePending("state", in.RequestID, in.State, in.Error)
			if !ok {
				return core.ToolResult{
					Content: []core.Content{{Type: "text", Text: fmt.Sprintf("No pending request for %s", in.RequestID)}},
					IsError: true,
				}, nil
			}
			return core.ToolResult{Content: []core.Content{{Type: "text", Text: "Submitted"}}}, nil
		},
		core.WithToolExecution(&core.ToolExecution{TaskSupport: core.TaskSupportForbidden}),
		core.WithToolMeta(&core.ToolMeta{
			UI: &core.UIMetadata{Visibility: []core.UIVisibility{core.UIVisibilityApp}},
		}),
		core.WithInputSchemaPatch(func(s *core.SchemaBuilder) {
			s.Prop("requestId").Desc("The request ID from the get_viewer_state command").Required()
			s.Prop("state").Desc("JSON-encoded viewer state snapshot")
			s.Prop("error").Desc("Error message if the viewer failed to read state")
		}),
	)
	t.Title = "Submit Viewer State"
	srv.RegisterTool(t.ToolDef, t.Handler)
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
		core.WithInputSchemaPatch(func(s *core.SchemaBuilder) {
			s.Prop("url").Desc("Original PDF URL or local file path").Required()
			s.Prop("data").Desc("Base64-encoded PDF bytes").Required()
		}),
		// Reflection on savePdfOutput emits filePath + mtimeMs already;
		// no patch needed — drift parity holds against upstream.
	)
	t.Title = "Save PDF"
	srv.RegisterTool(t.ToolDef, t.Handler)
}

// randomUUID emits a v4-shaped string. Crypto/rand here is overkill for
// a per-process fixture but keeps the wire shape (36-char hex with
// dashes) matching upstream's randomUUID for any string-shape assertions.
func randomUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	hexStr := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hexStr[0:8], hexStr[8:12], hexStr[12:16], hexStr[16:20], hexStr[20:32])
}

func interactDescription() string {
	return `Interact with a PDF viewer: annotate, navigate, search, extract text/screenshots, fill forms.
IMPORTANT: viewUUID must be the exact UUID returned by display_pdf (e.g. "a1b2c3d4-..."). Do NOT use arbitrary strings.

**BATCHING**: Send multiple commands in one call via ` + "`commands`" + ` array. Commands run sequentially; results are returned in the same order, one content item per command. If a command fails, the batch stops there and that command's slot contains text starting with ` + "`ERROR`" + ` — content.length tells you how far it got. TIP: End with ` + "`get_screenshot`" + ` to verify your changes.

**ANNOTATION** — add_annotations with array of annotation objects. Each needs: id (unique string), type, page (1-indexed).

**COORDINATE SYSTEM**: PDF points (1pt = 1/72in), origin at page TOP-LEFT corner. X increases rightward, Y increases downward.
- US Letter = 612×792pt. Margins: top≈y=50, bottom≈y=742, left≈x=72, right≈x=540, center≈(306, 396).
- Rectangle/circle/stamp x,y is the TOP-LEFT corner. To place a 200×30 box at the TOP of the page: x=72, y=50, width=200, height=30.
- For highlights/underlines, each rect's y is the TOP of the highlighted region.

Annotation types:
• highlight: rects:[{x,y,width,height}], color?, content? • underline: rects:[{x,y,w,h}], color?
• strikethrough: rects:[{x,y,w,h}], color? • note: x, y, content, color?
• rectangle: x, y, width, height, color?, fillColor?, rotation? • circle: x, y, width, height, color?, fillColor?
• line: x1, y1, x2, y2, color? • freetext: x, y, content, fontSize?, color?
• stamp: x, y, label (any text, e.g. APPROVED, DRAFT, CONFIDENTIAL), color?, rotation?
• image: imageUrl (required), x?, y?, width?, height?, mimeType?, rotation?, aspect? — places an image (signature, logo, etc.) on the page. Pass a local file path or HTTPS URL (NO data: URIs, NO base64). Width/height auto-detected if omitted. Users can also drag & drop images directly onto the viewer.

TIP: For text annotations, prefer highlight_text (auto-finds text) over manual rects.

Example — add a signature image and a stamp, then screenshot to verify:
` + "```json" + `
{"viewUUID":"…","commands":[
  {"action":"add_annotations","annotations":[
    {"id":"sig1","type":"image","page":1,"x":72,"y":700,"imageUrl":"/path/to/signature.png"},
    {"id":"s1","type":"stamp","page":1,"x":300,"y":400,"label":"APPROVED"}
  ]},
  {"action":"get_screenshot","page":1}
]}
` + "```" + `

• highlight_text: auto-find and highlight text (query, page?, color?, content?)
• update_annotations: partial update (id+type required) • remove_annotations: remove by ids

**NAVIGATION**: navigate (page), search (query), find (query, silent), search_navigate (matchIndex), zoom (scale 0.5–3.0)

**TEXT/SCREENSHOTS**:
• get_text: extract text from pages. Optional ` + "`page`" + ` for single page, or ` + "`intervals`" + ` for ranges [{start?,end?}]. Max 20 pages.
• get_screenshot: capture a single page as PNG image. Requires ` + "`page`" + `.
• get_viewer_state: snapshot of the live viewer — JSON {currentPage, pageCount, zoom, displayMode, selectedAnnotationIds, selection:{text,contextBefore,contextAfter,boundingRect}|null}. Use this to read what the user has selected or which page they're on.

**FORMS** — fill_form: fill fields with ` + "`fields`" + ` array of {name, value}.

**SAVE** — save_as: write the annotated PDF (annotations + form values) to a file. Pass ` + "`path`" + ` (absolute path or file://) for a new location, or omit ` + "`path`" + ` to overwrite the original. Set ` + "`overwrite: true`" + ` to replace an existing file (always required when omitting ` + "`path`" + `).`
}
