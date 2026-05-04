package main

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/panyam/demokit"
	"github.com/panyam/demokit/tui"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// Walkthrough fixtures live on disk under testdata/ so readers can inspect
// real bytes (open them, hash them, replace them) without grepping for
// hex literals. They're embedded at build time so `make demo` stays
// hermetic — no working-directory arithmetic, no path flags.
//
//go:embed testdata/pixel.png
var fixturePixelPNG []byte

//go:embed testdata/contract.pdf
var fixtureContractPDF []byte

//go:embed testdata/appendix.pdf
var fixtureAppendixPDF []byte

//go:embed testdata/README.txt
var fixtureREADME []byte

// filterFlags strips top-level flags so the inner flag.Parse on -addr is
// happy when invoked from `make serve`. Same shape as list-ttl/walkthrough.go,
// updated for demokit v0.0.13's `--doc <format>` (replaces the removed
// `--readme` flag) and `--from <trace>` companion.
func filterFlags(args []string) []string {
	out := make([]string, 0, len(args))
	skip := false
	for _, a := range args {
		if skip {
			skip = false
			continue
		}
		switch {
		case a == "--serve", a == "--tui", a == "--non-interactive":
			continue
		case a == "--url", a == "--file", a == "--doc", a == "--from":
			skip = true
			continue
		case strings.HasPrefix(a, "--doc="), strings.HasPrefix(a, "--from="),
			strings.HasPrefix(a, "--url="), strings.HasPrefix(a, "--file="):
			continue
		}
		out = append(out, a)
	}
	return out
}

func runDemo() {
	serverURL := "http://localhost:8080"
	for i, arg := range os.Args[1:] {
		if arg == "--url" && i+2 < len(os.Args) {
			serverURL = os.Args[i+2]
		}
	}

	demo := demokit.New("MCP File Inputs (SEP-2356) — Data URI File Arguments").
		Dir("file-inputs").
		Description("Walks through SEP-2356, which lets servers declare file-input properties on tool inputSchemas via the `x-mcp-file` JSON Schema extension. Clients render a file picker for those fields and pass selected files as RFC 2397 base64 data URIs (`data:<mediatype>;name=<filename>;base64,<...>`). The server decodes the URI, validates size/MIME, and processes the bytes.").
		Actors(
			demokit.Actor("Host", "MCP Host (this client)"),
			demokit.Actor("Server", "MCP Server (make serve)"),
		)

	demo.Section("Setup",
		"Start the MCP server in a separate terminal first:",
		"",
		"```",
		"Terminal 1:  make serve         # file-inputs server on :8080",
		"Terminal 2:  make demo          # this walkthrough (--tui for the interactive TUI)",
		"```",
		"",
		"Any MCP host can connect to the running server (Claude Desktop, VS Code, MCPJam). The walkthrough below acts as a scripted host that reads files from disk, encodes them as data URIs via `core.EncodeDataURI`, and calls the tools. See the README for VS Code config.",
	)

	demo.Section("Wire format",
		"SEP-2356 reuses two well-understood pieces:",
		"",
		"- **Schema marker** — a string property of `format: \"uri\"` carries an extra `x-mcp-file` keyword whose value is a `FileInputDescriptor` (`accept` MIME patterns / extensions, optional `maxSize` in decoded bytes). Server-side helpers: `core.FileInputProperty(desc)` and `core.FileInputArrayProperty(desc)`.",
		"- **Wire encoding** — files travel as RFC 2397 base64 data URIs with an optional percent-encoded `name=` parameter: `data:image/png;name=photo.png;base64,iVBORw0…`. Helpers: `core.EncodeDataURI(data, mediaType, filename)` and `core.DecodeDataURI(uri)`.",
		"",
		"Capability negotiation: the client advertises `\"fileInputs\": {}` inside `ClientCapabilities` during `initialize`. Servers MUST NOT include `x-mcp-file` in tool schemas if the client did not declare the capability — `core.HasFileInputs(ctx)` is the server-side check (gating ships in Phase 1.5 of the plan).",
	)

	var c *client.Client

	demo.Step("Connect to the file-inputs server").
		Arrow("Host", "Server", "POST /mcp — initialize (capabilities.fileInputs={})").
		DashedArrow("Server", "Host", "serverInfo + capabilities").
		Note("`client.NewClient(...)` + `Connect()`. Once Phase 1.6 lands, the client will auto-advertise `fileInputs` when the option is enabled; for now the demo connects with the default capability set and the server unconditionally exposes `x-mcp-file` so we can see the wire shape.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			c = client.NewClient(serverURL+"/mcp",
				core.ClientInfo{Name: "file-inputs-host", Version: "1.0"},
			)
			if err := c.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n    Start the server with: make serve\n", err)
				return nil
			}
			fmt.Printf("    Connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
			return nil
		})

	demo.Step("tools/list — confirm x-mcp-file appears on inputSchemas").
		Arrow("Host", "Server", "tools/list").
		DashedArrow("Server", "Host", "tools[] with x-mcp-file marked properties").
		Note("Bypass any typed helper and decode the raw response so we can see the JSON Schema shape exactly as a client would. `properties.image.x-mcp-file` carries `{accept: [\"image/*\"], maxSize: 5242880}` — that's the picker hint.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			raw, err := c.Call("tools/list", nil)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return nil
			}
			var page struct {
				Tools []struct {
					Name        string         `json:"name"`
					InputSchema map[string]any `json:"inputSchema"`
				} `json:"tools"`
			}
			if err := json.Unmarshal(raw.Raw, &page); err != nil {
				fmt.Printf("    ERROR decoding tools/list: %v\n", err)
				return nil
			}
			for _, t := range page.Tools {
				fmt.Printf("    %-20s\n", t.Name)
				descs := walkSchemaForFileInputs(t.InputSchema)
				if len(descs) == 0 {
					fmt.Printf("      (no x-mcp-file properties)\n")
					continue
				}
				for prop, d := range descs {
					fmt.Printf("      %-15s accept=%v maxSize=%s\n",
						prop, accepts(d), maxSize(d))
				}
			}
			return nil
		})

	demo.Step("upload_image — encode + call with a real PNG").
		Arrow("Host", "Server", "tools/call upload_image { image: data:image/png;name=…;base64,… }").
		DashedArrow("Server", "Host", "text result with size + media type").
		Note("Read `testdata/pixel.png` (a 24×24 RGB gradient, embedded at build time), encode it via `core.EncodeDataURI`, and pass the resulting string as the `image` argument. The handler runs `core.DecodeDataURI` to recover bytes, media type, and the original filename. Size and MIME validation will be enforced by `server.ValidateFileInput` once Phase 1.4 lands; today the handler trusts the input.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			previewFile("pixel.png", "image/png", fixturePixelPNG)
			uri := core.EncodeDataURI(fixturePixelPNG, "image/png", "pixel.png")
			fmt.Printf("    encoded data URI: %d bytes (preview: %s…)\n",
				len(uri), shortPreview(uri, 80))

			out, err := c.ToolCall("upload_image", map[string]any{
				"image":   uri,
				"caption": "24×24 gradient swatch",
			})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return nil
			}
			fmt.Printf("    server response:\n%s", indent(out, "      "))
			return nil
		})

	demo.Step("analyze_documents — array-of-files input").
		Arrow("Host", "Server", "tools/call analyze_documents { documents: [data:application/pdf;…, data:application/pdf;…] }").
		DashedArrow("Server", "Host", "summary line per document").
		Note("Demonstrates `core.FileInputArrayProperty` — the schema marks the `documents` array's *items* with `x-mcp-file`, so a host renders one picker per row. The walkthrough loads two embedded PDFs (`testdata/contract.pdf`, `testdata/appendix.pdf`) and sends both in one call.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			previewFile("contract.pdf", "application/pdf", fixtureContractPDF)
			previewFile("appendix.pdf", "application/pdf", fixtureAppendixPDF)
			pdfs := []string{
				core.EncodeDataURI(fixtureContractPDF, "application/pdf", "contract.pdf"),
				core.EncodeDataURI(fixtureAppendixPDF, "application/pdf", "appendix.pdf"),
			}
			out, err := c.ToolCall("analyze_documents", map[string]any{
				"documents": pdfs,
			})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return nil
			}
			fmt.Printf("    server response:\n%s", indent(out, "      "))
			return nil
		})

	demo.Step("process_any_file — no accept/maxSize filter").
		Arrow("Host", "Server", "tools/call process_any_file { file: data:text/plain;name=README.txt;base64,… }").
		DashedArrow("Server", "Host", "decoded media type + size").
		Note("Empty `FileInputDescriptor{}` means \"any file, any size.\" Useful for ad-hoc inspection. The handler still decodes via `core.DecodeDataURI`, which rejects malformed or non-base64 URIs. The walkthrough reads `testdata/README.txt` so the payload is a real on-disk file.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			previewFile("README.txt", "text/plain", fixtureREADME)
			uri := core.EncodeDataURI(fixtureREADME, "text/plain", "README.txt")
			out, err := c.ToolCall("process_any_file", map[string]any{
				"file": uri,
			})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return nil
			}
			fmt.Printf("    server response:\n%s", indent(out, "      "))
			return nil
		})

	demo.Step("Optional: send a file from disk").
		Arrow("Host", "Server", "tools/call upload_image (from --file <path>)").
		DashedArrow("Server", "Host", "decoded metadata of the on-disk file").
		Note("Pass `--file <path>` on the demo command line to read an image from disk and upload it. Skipped silently when the flag isn't set so the walkthrough stays hermetic; demonstrates the on-disk → data URI path you'd use in a real client integration. Phase 1.6 will fold this into `client.PrepareFileArg(path, descriptor)`.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			path := flagValue("--file")
			if path == "" {
				fmt.Printf("    skipped (pass --file <path> to exercise this step)\n")
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				fmt.Printf("    ERROR reading %s: %v\n", path, err)
				return nil
			}
			mediaType := guessImageMIME(path)
			previewFile(filepath.Base(path), mediaType, data)
			uri := core.EncodeDataURI(data, mediaType, filepath.Base(path))
			fmt.Printf("    %s (%s, %d bytes)\n", filepath.Base(path), mediaType, len(data))
			out, err := c.ToolCall("upload_image", map[string]any{
				"image": uri,
			})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return nil
			}
			fmt.Printf("    server response:\n%s", indent(out, "      "))
			return nil
		})

	demo.Section("MCP Apps mode (Phase 2.1)",
		"This same server also registers two MCP App tools that drive the same handlers via in-iframe file pickers — the human-in-the-loop case file-uploads-wg flagged as a gap:",
		"",
		"- `apps_upload_image` — `ui://file-inputs/upload-image` — single image picker (`mcp.selectFile`)",
		"- `apps_analyze_documents` — `ui://file-inputs/analyze-documents` — multi PDF picker (`mcp.selectFiles`)",
		"",
		"To exercise these, point a host that supports the MCP Apps extension (e.g. MCPJam) at this server and invoke either tool — the host renders the embedded HTML + bridge, the user clicks the picker, and the bridge encodes the chosen file(s) as data URI(s) before calling the regular tool. The walkthrough above doesn't drive these because demokit can't synthesize iframe user-gestures.",
	)

	demo.Section("Where to look in the code",
		"- Schema helpers: `core.FileInputProperty` / `core.FileInputArrayProperty` / `core.ExtractFileInputDescriptor` — core/file_input.go",
		"- Wire encoding: `core.EncodeDataURI` / `core.DecodeDataURI` / `core.IsDataURI` — core/datauri.go",
		"- Capability marker: `ClientCapabilities.FileInputs` + `core.HasFileInputs(ctx)` — core/protocol.go, core/file_input.go",
		"- Server validation (Phase 1.4): `server.ValidateFileInput` — pending",
		"- Capability gating (Phase 1.5): strip `x-mcp-file` from tools/list when client lacks the cap — pending",
		"- Client helpers (Phase 1.6): `client.FileInputsFromTool` / `client.PrepareFileArg` — pending",
		"- Bridge `selectFile` / `selectFiles` (Phase 2.1): `ext/ui/assets/file-picker.ts` — shipped",
		"- Apps fixtures: `examples/file-inputs/apps/upload-image.html`, `analyze-documents.html`",
		"- SEP-2356 spec: modelcontextprotocol/specification PR 2356",
	)

	for _, arg := range os.Args[1:] {
		if strings.TrimSpace(arg) == "--tui" {
			demo.WithRenderer(tui.New())
			break
		}
	}

	demo.Execute()

	if c != nil {
		c.Close()
	}
}

// --- helpers used by both the server and the walkthrough ---

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

// --- walkthrough-only helpers ---

func walkSchemaForFileInputs(schema map[string]any) map[string]*core.FileInputDescriptor {
	out := map[string]*core.FileInputDescriptor{}
	props, _ := schema["properties"].(map[string]any)
	for name, raw := range props {
		prop, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if d := core.ExtractFileInputDescriptor(prop); d != nil {
			out[name] = d
			continue
		}
		// array-of-files: items carries the descriptor.
		if items, ok := prop["items"].(map[string]any); ok {
			if d := core.ExtractFileInputDescriptor(items); d != nil {
				out[name+"[]"] = d
			}
		}
	}
	return out
}

func accepts(d *core.FileInputDescriptor) string {
	if d == nil || len(d.Accept) == 0 {
		return "*"
	}
	return "[" + strings.Join(d.Accept, ", ") + "]"
}

func maxSize(d *core.FileInputDescriptor) string {
	if d == nil || d.MaxSize == nil {
		return "<unlimited>"
	}
	return fmt.Sprintf("%d bytes", *d.MaxSize)
}

// previewFile prints a small inline preview of the bytes about to be
// uploaded so the demo viewer can sanity-check the payload visually before
// it gets base64-stuffed into a data URI.
//
//   - image/*: emits the iTerm2 inline-image escape sequence
//     (`\x1b]1337;File=…:base64\x07`). iTerm2, WezTerm, and Mintty render
//     it as an inline thumbnail; other terminals consume the OSC silently.
//   - text/*: prints the first ~3 lines of the file.
//   - everything else: hex preview of the leading bytes.
func previewFile(filename, mediaType string, data []byte) {
	fmt.Printf("    ─ %s (%s, %d bytes) ─\n", filename, mediaType, len(data))
	switch {
	case strings.HasPrefix(mediaType, "image/"):
		previewImage(filename, data)
	case strings.HasPrefix(mediaType, "text/"):
		previewText(data, 3)
	default:
		previewHex(data, 32)
	}
}

func previewImage(filename string, data []byte) {
	encoded := base64.StdEncoding.EncodeToString(data)
	nameB64 := base64.StdEncoding.EncodeToString([]byte(filename))
	// iTerm2 inline-image protocol: OSC 1337 ; File=… ST.
	// width=auto + preserveAspectRatio=1 keeps tiny images legible.
	fmt.Printf("      \x1b]1337;File=name=%s;inline=1;preserveAspectRatio=1;width=auto;height=auto:%s\x07\n",
		nameB64, encoded)
}

func previewText(data []byte, maxLines int) {
	scanner := strings.Split(string(data), "\n")
	if len(scanner) > maxLines {
		scanner = scanner[:maxLines]
	}
	for _, line := range scanner {
		fmt.Printf("      │ %s\n", line)
	}
	fmt.Printf("      │ …\n")
}

func previewHex(data []byte, n int) {
	if len(data) < n {
		n = len(data)
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 && i%8 == 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%02x ", data[i])
	}
	fmt.Printf("      │ %s\n", strings.TrimRight(b.String(), " "))
	if len(data) > n {
		fmt.Printf("      │ … (+%d bytes)\n", len(data)-n)
	}
}

func shortPreview(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func indent(text, prefix string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n") + "\n"
}

// flagValue returns the value for `--name <value>` from os.Args, or "" if
// the flag is absent. We avoid the `flag` package here because the demo
// already shares argv with the server entry point.
func flagValue(name string) string {
	for i, arg := range os.Args[1:] {
		if arg == name && i+2 < len(os.Args) {
			return os.Args[i+2]
		}
	}
	return ""
}

func guessImageMIME(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	}
	return "application/octet-stream"
}

