// Example: SEP-2356 File Inputs — declarative file pickers for tools.
//
// Two-process architecture:
//
//	Terminal 1:  make serve         # MCP server on :8080
//	Terminal 2:  make demo          # demokit walkthrough (or `make demo --tui`)
//
// The server is a real MCP server — any host (VS Code, Claude Desktop,
// MCPJam) can connect to it and observe the SEP-2356 `x-mcp-file` keyword
// on tool inputSchemas. The walkthrough acts as a scripted MCP host that
// reads real files from disk, encodes them as RFC 2397 data URIs, and
// invokes each tool via tools/call.
package main

import (
	"flag"
	"fmt"
	"log"
	"mime"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

func main() {
	for _, arg := range os.Args[1:] {
		switch strings.TrimSpace(arg) {
		case "--serve":
			serve()
			return
		}
	}
	runDemo()
}

func serve() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.CommandLine.Parse(filterFlags(os.Args[1:]))

	// Same logger shape as examples/elicitation/main.go — tints request
	// flow so a side-by-side `make serve` + `make demo` shows what the
	// server saw for every tools/list and tools/call. WithRequestLogging
	// covers transport-level traffic; the LoggingMiddleware covers the
	// MCP dispatch path (method + duration + outcome).
	logger := demokit.NewColorLogger("[mcp] ", []demokit.ColorRule{
		{Contains: "error=", DarkColor: demokit.ANSIRed},
		{Contains: "ERROR", DarkColor: demokit.ANSIRed},
		{Contains: "[http] →", DarkColor: demokit.ANSIGray, LightColor: demokit.ANSIDimBlue},
		{Contains: "[http] ←", DarkColor: demokit.ANSICyan, LightColor: demokit.ANSIBlue},
		{Contains: "MCP ", DarkColor: demokit.ANSIBrightGreen, LightColor: demokit.ANSIGreen},
	})

	// One temp dir per server run, NOT auto-cleaned — the whole point is
	// that you can `ls` the directory and `open` the files after the
	// walkthrough finishes. A real handler would never persist user
	// uploads under a predictable path; this is purely for inspection.
	uploadDir, err := os.MkdirTemp("/tmp", "file-inputs-demo-")
	if err != nil {
		log.Fatalf("mktemp: %v", err)
	}

	srv := server.NewServer(
		core.ServerInfo{Name: "file-inputs-demo", Version: "0.1.0"},
		server.WithListen(*addr),
		server.WithRequestLogging(logger),
		server.WithMiddleware(server.LoggingMiddleware(logger)),
	)

	registerTools(srv, uploadDir)

	log.Printf("[file-inputs-demo] listening on %s — POST /mcp", *addr)
	log.Printf("[file-inputs-demo] tools: upload_image, analyze_documents, process_any_file")
	log.Printf("[file-inputs-demo] uploads will be written to %s (not auto-cleaned)", uploadDir)
	if err := srv.ListenAndServe(server.WithStreamableHTTP(true)); err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}

// registerTools installs the three SEP-2356 demo tools on the server.
// uploadDir is the demo-only inspection directory each handler writes
// received payloads into.
func registerTools(srv *server.Server, uploadDir string) {
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
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return uploadImageHandler(ctx, req, uploadDir)
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
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return analyzeDocumentsHandler(ctx, req, uploadDir)
		},
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "process_any_file",
			Description: "Accepts any file with no MIME or size filter — useful for ad-hoc inspection.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"file": core.FileInputProperty(core.FileInputDescriptor{}),
				},
				"required": []string{"file"},
			},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return processAnyFileHandler(ctx, req, uploadDir)
		},
	)
}

func uploadImageHandler(ctx core.ToolContext, req core.ToolRequest, uploadDir string) (core.ToolResult, error) {
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
	// side-by-side `make serve` + `make demo` shows the image on both
	// ends of the wire. A production handler would not do this.
	previewFile(displayName(filename), mediaType, data)
	saved, saveErr := saveUpload(uploadDir, filename, mediaType, data)

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

func analyzeDocumentsHandler(ctx core.ToolContext, req core.ToolRequest, uploadDir string) (core.ToolResult, error) {
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
			fmt.Fprintf(&b, "  [%d] not a string — skipped\n", i)
			continue
		}
		data, mediaType, filename, err := core.DecodeDataURI(uri)
		if err != nil {
			fmt.Fprintf(&b, "  [%d] decode error: %v\n", i, err)
			continue
		}
		// Demo-only: echo each received doc on the server side. See the
		// note in uploadImageHandler.
		previewFile(displayName(filename), mediaType, data)
		saved, saveErr := saveUpload(uploadDir, filename, mediaType, data)
		if saveErr != nil {
			fmt.Fprintf(&b, "  [%d] %s — %s, %d bytes (save error: %v)\n",
				i, displayName(filename), mediaType, len(data), saveErr)
		} else {
			fmt.Fprintf(&b, "  [%d] %s — %s, %d bytes → %s\n",
				i, displayName(filename), mediaType, len(data), saved)
		}
	}
	return core.TextResult(b.String()), nil
}

func processAnyFileHandler(ctx core.ToolContext, req core.ToolRequest, uploadDir string) (core.ToolResult, error) {
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
	previewFile(displayName(filename), mediaType, data)
	saved, saveErr := saveUpload(uploadDir, filename, mediaType, data)
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
