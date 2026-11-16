// Package mcp holds the file-inputs server's tools (SEP-2356). It imports
// neither demokit nor otel, so the browser build in ../web stays small (#1716).
package mcp

import (
	"encoding/json"
	"fmt"
	"log"
	"mime"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// Options configures the demo-only extras around each upload. Both are
// optional, and the browser build leaves both empty.
type Options struct {
	// UploadDir, when set, is where each handler writes the decoded file so
	// you can inspect it. Empty skips the write.
	UploadDir string
	// Preview, when set, prints each received file. The CLI server passes a
	// terminal preview (an inline image, the first text lines, or a hex dump).
	Preview func(filename, mediaType string, data []byte)
}

func (o Options) preview(filename, mediaType string, data []byte) {
	if o.Preview != nil {
		o.Preview(filename, mediaType, data)
	}
}

// save writes the upload under UploadDir. With no UploadDir it saves
// nothing and reports that.
func (o Options) save(suggested, mediaType string, data []byte) (string, error) {
	if o.UploadDir == "" {
		return "(not saved: no upload directory)", nil
	}
	return saveUpload(o.UploadDir, suggested, mediaType, data)
}

// Register adds the three file-input tools and the two MCP Apps tools to
// srv. The CLI server and the browser build (../web) both call it.
func Register(srv *server.Server, opts Options) {
	registerTools(srv, opts)
	registerAppsTools(srv, opts)
}

// uploadDir is the demo-only inspection directory each handler writes
// received payloads into.
func registerTools(srv *server.Server, opts Options) {
	const fiveMB = 5 * 1024 * 1024

	imageMax := fiveMB
	imageDesc := core.FileInputDescriptor{
		Accept:  []string{"image/*"},
		MaxSize: &imageMax,
	}
	srv.RegisterTool(
		core.ToolDef{
			Name:        "upload_image",
			Description: "Accepts a single image (image/*, max 5 MB) and reports its size + media type.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"image": core.FileInputProperty(imageDesc),
					"caption": map[string]any{
						"type":        "string",
						"description": "Optional caption to print alongside the image metadata.",
					},
				},
				"required": []string{"image"},
			},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			return uploadImageHandler(ctx, req, opts)
		},
	)

	pdfDesc := core.FileInputDescriptor{
		Accept: []string{"application/pdf", ".pdf"},
	}
	srv.RegisterTool(
		core.ToolDef{
			Name:        "analyze_documents",
			Description: "Accepts an array of PDF files and reports a per-file summary.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"documents": core.FileInputArrayProperty(pdfDesc),
				},
				"required": []string{"documents"},
			},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			return analyzeDocumentsHandler(ctx, req, opts)
		},
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "process_any_file",
			Description: "Accepts any file with no MIME or size filter, useful for ad-hoc inspection.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"file": core.FileInputProperty(core.FileInputDescriptor{}),
				},
				"required": []string{"file"},
			},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			return processAnyFileHandler(ctx, req, opts)
		},
	)
}

func uploadImageHandler(ctx core.ToolContext, req core.ToolRequest, opts Options) (core.ToolResponse, error) {
	args, err := parseArgs(req.Arguments)
	if err != nil {
		return core.ErrorResult("invalid arguments: " + err.Error()), nil
	}
	uri, _ := args["image"].(string)
	if uri == "" {
		return core.ErrorResult("missing required field: image"), nil
	}
	caption, _ := args["caption"].(string)

	data, mediaType, filename, err := core.DecodeDataURI(uri)
	if err != nil {
		return core.ErrorResult("could not decode image data URI: " + err.Error()), nil
	}
	// Demo-only: echo the received payload to the server's stdout so a
	// side-by-side `just serve` + `just demo` shows the image on both
	// ends of the wire. A production handler would not do this.
	opts.preview(displayName(filename), mediaType, data)
	saved, saveErr := opts.save(filename, mediaType, data)

	var b strings.Builder
	fmt.Fprintf(&b, "received image\n")
	fmt.Fprintf(&b, "  filename:    %s\n", displayName(filename))
	fmt.Fprintf(&b, "  media type:  %s\n", mediaType)
	fmt.Fprintf(&b, "  size:        %d bytes\n", len(data))
	if caption != "" {
		fmt.Fprintf(&b, "  caption:     %s\n", caption)
	}
	if saveErr != nil {
		fmt.Fprintf(&b, "  save error:  %s\n", saveErr)
	} else {
		fmt.Fprintf(&b, "  saved as:    %s\n", saved)
	}
	return core.TextResult(b.String()), nil
}

func analyzeDocumentsHandler(ctx core.ToolContext, req core.ToolRequest, opts Options) (core.ToolResponse, error) {
	args, err := parseArgs(req.Arguments)
	if err != nil {
		return core.ErrorResult("invalid arguments: " + err.Error()), nil
	}
	rawList, _ := args["documents"].([]any)
	if len(rawList) == 0 {
		return core.ErrorResult("documents must be a non-empty array of data URIs"), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "analyzed %d document(s)\n", len(rawList))
	for i, item := range rawList {
		uri, ok := item.(string)
		if !ok {
			fmt.Fprintf(&b, "  [%d] not a string, skipped\n", i)
			continue
		}
		data, mediaType, filename, err := core.DecodeDataURI(uri)
		if err != nil {
			fmt.Fprintf(&b, "  [%d] decode error: %v\n", i, err)
			continue
		}
		// Demo-only: echo each received doc on the server side. See the
		// note in uploadImageHandler.
		opts.preview(displayName(filename), mediaType, data)
		saved, saveErr := opts.save(filename, mediaType, data)
		if saveErr != nil {
			fmt.Fprintf(&b, "  [%d] %s - %s, %d bytes (save error: %v)\n",
				i, displayName(filename), mediaType, len(data), saveErr)
		} else {
			fmt.Fprintf(&b, "  [%d] %s - %s, %d bytes → %s\n",
				i, displayName(filename), mediaType, len(data), saved)
		}
	}
	return core.TextResult(b.String()), nil
}

func processAnyFileHandler(ctx core.ToolContext, req core.ToolRequest, opts Options) (core.ToolResponse, error) {
	args, err := parseArgs(req.Arguments)
	if err != nil {
		return core.ErrorResult("invalid arguments: " + err.Error()), nil
	}
	uri, _ := args["file"].(string)
	if uri == "" {
		return core.ErrorResult("missing required field: file"), nil
	}
	data, mediaType, filename, err := core.DecodeDataURI(uri)
	if err != nil {
		return core.ErrorResult("could not decode file data URI: " + err.Error()), nil
	}
	// Demo-only: echo received payload on the server side.
	opts.preview(displayName(filename), mediaType, data)
	saved, saveErr := opts.save(filename, mediaType, data)
	suffix := ""
	if saveErr != nil {
		suffix = fmt.Sprintf(" (save error: %v)", saveErr)
	} else {
		suffix = " → " + saved
	}
	return core.TextResult(fmt.Sprintf(
		"received %s (%s, %d bytes)%s",
		displayName(filename), mediaType, len(data), suffix,
	)), nil
}

// saveUpload writes the decoded payload to dir under a sanitized name so a
// human can `ls` and inspect it after the demo finishes. If the suggested
// name is empty or unsafe, an `upload-<n>.<ext>` fallback is used; if the
// chosen path collides with an earlier upload, a numeric suffix is appended
// (`name-1.ext`, `name-2.ext`, …) so each call yields a distinct file.
//
// This is a demo-only convenience — a production handler would never write
// untrusted client data under a predictable path on the server.
func saveUpload(dir, suggested, mediaType string, data []byte) (string, error) {
	name := sanitizeUploadName(suggested, mediaType)
	path := filepath.Join(dir, name)
	for i := 1; ; i++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		path = filepath.Join(dir, base+"-"+strconv.Itoa(i)+ext)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	log.Printf("[upload] %s -> %s (%s, %d bytes)", suggested, path, mediaType, len(data))
	return path, nil
}

func sanitizeUploadName(suggested, mediaType string) string {
	clean := filepath.Base(suggested)
	clean = strings.TrimSpace(clean)
	if clean == "" || clean == "." || clean == "/" || clean == ".." {
		exts, _ := mime.ExtensionsByType(mediaType)
		ext := ".bin"
		if len(exts) > 0 {
			ext = exts[0]
		}
		return "upload" + ext
	}
	return clean
}

func parseArgs(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	return args, nil
}

func displayName(filename string) string {
	if filename == "" {
		return "<unnamed>"
	}
	return filename
}
