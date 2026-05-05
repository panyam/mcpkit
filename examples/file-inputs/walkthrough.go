package main

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
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


func runDemo() {
	serverURL := common.ServerURL()

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
		Note("`client.NewClient(...)` + `client.WithFileInputs()` + `Connect()`. The `WithFileInputs` option advertises `capabilities.fileInputs={}` on the wire — without it, the server would strip `x-mcp-file` from every tool's inputSchema (per SEP-2356 cap-gating). The next step inspects the raw response to confirm the keyword survives.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			c = client.NewClient(serverURL+"/mcp",
				core.ClientInfo{Name: "file-inputs-host", Version: "1.0"},
				client.WithFileInputs(),
			)
			if err := c.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n    Start the server with: make serve\n", err)
				return nil
			}
			fmt.Printf("    Connected to %s %s (fileInputs cap declared)\n", c.ServerInfo.Name, c.ServerInfo.Version)
			return nil
		})

	demo.Step("tools/list — extract file-input descriptors").
		Arrow("Host", "Server", "tools/list").
		DashedArrow("Server", "Host", "tools[] with x-mcp-file marked properties").
		Note("`client.FileInputsFromTool(tool)` extracts the per-property descriptors a server advertised. Single-file properties land under their name (`\"image\"`); array-of-files shapes land under `name[]` (`\"documents[]\"`) so callers can disambiguate without re-walking the schema.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			page, err := c.ListToolsPage("")
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return nil
			}
			for _, tool := range page.Tools {
				fmt.Printf("    %-20s\n", tool.Name)
				descs := client.FileInputsFromTool(tool)
				if len(descs) == 0 {
					fmt.Printf("      (no x-mcp-file properties)\n")
					continue
				}
				for prop, d := range descs {
					fmt.Printf("      %-15s accept=%v maxSize=%s\n",
						prop, accepts(&d), maxSize(&d))
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

	demo.Step("Optional: send a file from disk via client.PrepareFileArg").
		Arrow("Host", "Server", "tools/call upload_image (from --file <path>)").
		DashedArrow("Server", "Host", "decoded metadata of the on-disk file").
		Note("Pass `--file <path>` on the demo command line to read an image from disk and upload it. Skipped silently when the flag isn't set so the walkthrough stays hermetic. The encode path is now `client.PrepareFileArg(path, descriptor)` — single call that reads the file, detects MIME, validates against the descriptor (size + accept patterns, same rules as the server-side validator), and returns the data URI. Failures surface as typed errors (`*core.FileTooLargeError`, `*core.FileTypeNotAcceptedError`) so callers can branch with `errors.As`.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			path := flagValue("--file")
			if path == "" {
				fmt.Printf("    skipped (pass --file <path> to exercise this step)\n")
				return nil
			}
			// Pull the descriptor straight from the server's tools/list so
			// we send a payload that satisfies whatever constraints the
			// server actually advertised — no risk of a stale local copy
			// drifting from the descriptor's accept list.
			tools, err := c.ListToolsPage("")
			if err != nil {
				fmt.Printf("    ERROR listing tools: %v\n", err)
				return nil
			}
			descs := client.FileInputsFromTool(findTool(tools.Tools, "upload_image"))
			imageDesc := descs["image"]

			uri, err := client.PrepareFileArg(path, &imageDesc)
			if err != nil {
				fmt.Printf("    ERROR (%T): %v\n", err, err)
				return nil
			}
			fmt.Printf("    %s prepared (%d bytes encoded)\n", filepath.Base(path), len(uri))

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

	// -- SEP-2356 Phase 1.4 — server-side validation rejection demos --

	demo.Section("Validation — server rejects non-conforming uploads (Phase 1.4)",
		"The server is started with `server.WithFileInputValidation()` (see `examples/file-inputs/main.go`), so the dispatcher walks each tool's `inputSchema` for `x-mcp-file` properties and runs `core.ValidateFileInput` on every matching arg BEFORE the handler runs. Failures surface as JSON-RPC `-32602` with a structured `data` payload — that exact shape is the contract pinned by `conformance/file-inputs/scenarios.test.ts`.",
		"",
		"The next three steps exercise all three failure modes the validator covers. They drive the server through the Go MCP client (`*client.Client`); the `client.RPCError` returned on rejection carries the same structured `data` field the wire emits. Each step prints `error.code`, `error.message`, and `error.data` so the rejection contract is visible in the demo output.",
		"",
		"Each step also attaches a copy-pasteable `bash` block (rendered via demokit `VerbatimLang` so the lines survive any terminal width) reproducing the same call on the wire — useful for validating from a non-Go SDK or sanity-checking the JSON shape directly.",
	)

	demo.Step("upload_image rejects wrong MIME (text/plain into image/* slot)").
		Arrow("Host", "Server", "tools/call upload_image { image: data:text/plain;… }").
		DashedArrow("Server", "Host", "-32602 + data: {reason: file_type_not_accepted, mediaType, accept, field}").
		Note("The descriptor declares `accept: [\"image/*\"]`. Sending a text/plain data URI hits the dispatcher's accept-pattern matcher (`core.FileMatchesAccept`), which fails before the handler runs. The error data carries `mediaType` (what we sent) and `accept` (what the server requires) so a client can render a useful message.").
		VerbatimLang("Reproduce on the wire", "bash", `# Mint a session
SID=$(curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -d '{"jsonrpc":"2.0","id":"i","method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"x","version":"1"},"capabilities":{"fileInputs":{}}}}' \
  -D - -o /dev/null | grep -i 'mcp-session-id' | awk '{print $2}' | tr -d '\r\n')
curl -s -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" -H "Accept: application/json" -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' >/dev/null

# Send a text/plain payload into upload_image's image/* slot
URI='data:text/plain;name=x.txt;base64,aGVsbG8='
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"upload_image\",\"arguments\":{\"image\":\"$URI\"}}}"
`).
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			uri := core.EncodeDataURI([]byte("hello"), "text/plain", "x.txt")
			fmt.Printf("    sending: data URI with mediaType=text/plain (%d bytes total)\n", len(uri))
			_, err := c.Call("tools/call", map[string]any{
				"name":      "upload_image",
				"arguments": map[string]any{"image": uri},
			})
			printRPCError(err, "file_type_not_accepted")
			return nil
		})

	demo.Step("upload_image rejects oversized payload (6 MiB into 5 MiB cap)").
		Arrow("Host", "Server", "tools/call upload_image { image: data:image/png;… 6 MiB }").
		DashedArrow("Server", "Host", "-32602 + data: {reason: file_too_large, actualSize, maxSize, field}").
		Note("Same descriptor declares `maxSize: 5_242_880` (5 MiB). We synthesize a 6 MiB null-byte buffer, encode as `image/png`, and send it. The validator decodes the data URI, sees the size cap is exceeded, and short-circuits with structured size info.").
		VerbatimLang("Reproduce on the wire", "bash", `# Mint a session
SID=$(curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -d '{"jsonrpc":"2.0","id":"i","method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"x","version":"1"},"capabilities":{"fileInputs":{}}}}' \
  -D - -o /dev/null | grep -i 'mcp-session-id' | awk '{print $2}' | tr -d '\r\n')
curl -s -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" -H "Accept: application/json" -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' >/dev/null

# Generate a 6 MiB image/png payload (Python keeps the curl short)
BIG=$(python3 -c 'import base64; print("data:image/png;name=big.png;base64," + base64.b64encode(b"\x00" * (6*1024*1024)).decode())')
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"upload_image\",\"arguments\":{\"image\":\"$BIG\"}}}"
`).
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			big := make([]byte, 6*1024*1024) // 6 MiB of zeros
			uri := core.EncodeDataURI(big, "image/png", "big.png")
			fmt.Printf("    sending: 6 MiB image/png payload (server cap is 5 MiB)\n")
			_, err := c.Call("tools/call", map[string]any{
				"name":      "upload_image",
				"arguments": map[string]any{"image": uri},
			})
			printRPCError(err, "file_too_large")
			return nil
		})

	demo.Step("analyze_documents rejects per-element with field path tracking").
		Arrow("Host", "Server", "tools/call analyze_documents { documents: [valid pdf, text/plain] }").
		DashedArrow("Server", "Host", "-32602 + data.field = \"documents[1]\"").
		Note("Send a 2-element array where element 0 is a valid PDF and element 1 is a text/plain payload. The dispatcher's array-items walker validates each element against `items.x-mcp-file` and surfaces the path of the offender — `data.field == \"documents[1]\"`. Useful so a client rendering rich error UX can highlight the specific input that failed instead of asking the user to re-pick everything.").
		VerbatimLang("Reproduce on the wire", "bash", `# Mint a session
SID=$(curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -d '{"jsonrpc":"2.0","id":"i","method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"x","version":"1"},"capabilities":{"fileInputs":{}}}}' \
  -D - -o /dev/null | grep -i 'mcp-session-id' | awk '{print $2}' | tr -d '\r\n')
curl -s -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" -H "Accept: application/json" -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' >/dev/null

# Send 2 documents — index 0 valid PDF, index 1 wrong MIME
GOOD='data:application/pdf;name=ok.pdf;base64,JVBERi0xLjQKJSVFT0YK'
BAD='data:text/plain;name=bad.txt;base64,aGVsbG8='
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"analyze_documents\",\"arguments\":{\"documents\":[\"$GOOD\",\"$BAD\"]}}}"
`).
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			good := core.EncodeDataURI([]byte("%PDF-1.4\n%%EOF\n"), "application/pdf", "ok.pdf")
			bad := core.EncodeDataURI([]byte("hello"), "text/plain", "bad.txt")
			fmt.Printf("    sending: 2 documents (index 0: valid PDF, index 1: text/plain)\n")
			_, err := c.Call("tools/call", map[string]any{
				"name":      "analyze_documents",
				"arguments": map[string]any{"documents": []string{good, bad}},
			})
			printRPCError(err, "file_type_not_accepted")
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

	common.SetupRenderer(demo)

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

// findTool picks a tool by name from a ListToolsPage result. The demo
// uses this to look up the descriptor a server advertised before
// preparing a payload, so the encode pipeline always matches whatever
// constraints the server actually wants.
func findTool(tools []core.ToolDef, name string) core.ToolDef {
	for _, t := range tools {
		if t.Name == name {
			return t
		}
	}
	return core.ToolDef{}
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

// printRPCError formats a `*client.RPCError` for the validation rejection
// demo steps. wantReason is the SEP-2356 reason the demo expects to see —
// printed alongside the actual reason so a regression in the wire shape
// is visible in the demo output (and not just in the conformance suite).
func printRPCError(err error, wantReason string) {
	if err == nil {
		fmt.Printf("    UNEXPECTED: server accepted the call; no validation triggered\n")
		return
	}
	var rpc *client.RPCError
	if !errors.As(err, &rpc) {
		fmt.Printf("    transport error: %v\n", err)
		return
	}
	fmt.Printf("    error.code:    %d\n", rpc.Code)
	fmt.Printf("    error.message: %s\n", rpc.Message)
	if rpc.Data == nil {
		fmt.Printf("    error.data:    <none>\n")
		return
	}
	pretty, _ := json.MarshalIndent(rpc.Data, "      ", "  ")
	fmt.Printf("    error.data:    %s\n", string(pretty))
	gotReason := ""
	if m, ok := rpc.Data.(map[string]any); ok {
		gotReason, _ = m["reason"].(string)
	}
	if wantReason != "" && gotReason != wantReason {
		fmt.Printf("    WARN: data.reason = %q, expected %q\n", gotReason, wantReason)
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


