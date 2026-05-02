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
	"os"
	"strings"

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

	srv := server.NewServer(
		core.ServerInfo{Name: "file-inputs-demo", Version: "0.1.0"},
		server.WithListen(*addr),
	)

	registerTools(srv)

	log.Printf("[file-inputs-demo] listening on %s — POST /mcp", *addr)
	log.Printf("[file-inputs-demo] tools: upload_image, analyze_documents, process_any_file")
	if err := srv.ListenAndServe(server.WithStreamableHTTP(true)); err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}

// registerTools installs the three SEP-2356 demo tools on the server.
// Extracted so tests / future fixtures can reuse the same registration.
func registerTools(srv *server.Server) {
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
		uploadImageHandler,
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
		analyzeDocumentsHandler,
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
		processAnyFileHandler,
	)
}

func uploadImageHandler(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
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

	var b strings.Builder
	fmt.Fprintf(&b, "received image\n")
	fmt.Fprintf(&b, "  filename:    %s\n", displayName(filename))
	fmt.Fprintf(&b, "  media type:  %s\n", mediaType)
	fmt.Fprintf(&b, "  size:        %d bytes\n", len(data))
	if caption != "" {
		fmt.Fprintf(&b, "  caption:     %s\n", caption)
	}
	return core.TextResult(b.String()), nil
}

func analyzeDocumentsHandler(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
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
		fmt.Fprintf(&b, "  [%d] %s — %s, %d bytes\n", i, displayName(filename), mediaType, len(data))
	}
	return core.TextResult(b.String()), nil
}

func processAnyFileHandler(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
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
	return core.TextResult(fmt.Sprintf(
		"received %s (%s, %d bytes)",
		displayName(filename), mediaType, len(data),
	)), nil
}
