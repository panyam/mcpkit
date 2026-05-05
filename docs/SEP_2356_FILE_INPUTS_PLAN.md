# Plan: SEP-2356 File Input Support

**Issue:** mcpkit 313
**Branch:** `feat/sep-2356-file-inputs` (from main)
**Spec PR:** modelcontextprotocol/specification PR 2356
**Prototype PRs:** TypeScript SDK PR 1633, Python SDK PR 2217

## Summary

Add declarative file inputs for tools and elicitation. Servers mark schema
properties with `x-mcp-file` to signal "render a file picker here." Clients
encode selected files as RFC 2397 data URIs and pass them as string arguments.

Two phases: core protocol support (Phase 1), then MCP Apps bridge integration
(Phase 2).

## Key spec details

- Files encoded as data URIs: `data:<mediatype>;name=<filename>;base64,<data>`
- `name=` parameter is percent-encoded
- `x-mcp-file` is a JSON Schema extension keyword on `{"type": "string", "format": "uri"}`
- `FileInputDescriptor`: `accept` (MIME patterns / extensions), `maxSize` (bytes)
- Client capability: `fileInputs` in `ClientCapabilities`
- Server MUST NOT include file input fields without client `fileInputs` capability
- Validation: server rejects oversized files with `-32602` + reason `"file_too_large"`
- Elicitation: `x-mcp-file` on `StringSchema` for form file fields

## Constraints check

| Constraint | Status |
|------------|--------|
| C1 (typed contexts) | OK — no new context types needed |
| C2 (consolidated structs) | OK |
| C3 (no globals) | OK |
| server/C4 (no spec extensions without WG) | OK — implementing in-review SEP |

## Phase 1: Core protocol support

### 1.1: Types ✅ shipped

**Files:** `core/file_input.go`, `core/protocol.go`

- [x] `FileInputDescriptor` struct with `Accept []string` + `MaxSize *int` (`omitempty` JSON tags).
- [x] `FileInputs *struct{}` added to `ClientCapabilities`.
- [x] `core.HasFileInputs(ctx) bool` helper.
- [x] `core.FileInputSchemaKey = "x-mcp-file"` constant.

### 1.2: Data URI helpers ✅ shipped

**Files:** `core/datauri.go`, `core/datauri_test.go`

- [x] `EncodeDataURI(data, mediaType, filename) string` (percent-encodes `name=` via `url.PathEscape`).
- [x] `DecodeDataURI(uri) (data, mediaType, filename, err)` — defaults mediaType to `text/plain;charset=US-ASCII` per RFC 2397, preserves unknown media-type parameters, falls back to `RawStdEncoding` for tolerance.
- [x] `IsDataURI(s) bool`.
- [x] Sentinel errors: `ErrNotDataURI`, `ErrMalformedDataURI`, `ErrNonBase64DataURI`, `ErrInvalidDataURIName`.
- [x] Tests cover round-trip, percent-encoded filenames, default media type, rejected non-base64 / non-data scheme / bad base64, capability JSON round-trip.

### 1.3: Schema helpers for x-mcp-file ✅ shipped (alongside 1.1)

**Files:** `core/file_input.go`, `core/file_input_test.go`

- [x] `FileInputProperty(desc) map[string]any` — `{"type":"string","format":"uri","x-mcp-file":desc}`.
- [x] `FileInputArrayProperty(desc) map[string]any` — array of file-input items.
- [x] `ExtractFileInputDescriptor(prop) *FileInputDescriptor` — handles both typed-value (`FileInputDescriptor` struct) and JSON-unmarshalled (`map[string]any`) shapes; the JSON path is needed when round-tripping schemas through `tools/list`.

### 1.4: Server-side validation ✅ shipped

**Files:** `core/file_validation.go` (new), `core/file_validation_test.go` (new), `server/file_validation.go` (new), `server/file_validation_test.go` (new), `server/dispatch.go`, `server/server.go`, `examples/file-inputs/main.go`

- [x] `core.ValidateFileInput(uri, desc)` — decode data URI, check size + MIME.
- [x] `core.FileMatchesAccept` — exact / wildcard subtype / extension hint matcher (mirrors JS-side `fileMatchesAccept` in `ext/ui/assets/file-picker.ts` so both sides agree).
- [x] Typed errors `core.FileTooLargeError` + `core.FileTypeNotAcceptedError` carrying structured `Data()` payloads. Sentinels `core.ErrFileTooLarge` + `core.ErrFileTypeNotAccepted` for `errors.Is`.
- [x] Reason constants `core.FileInputReasonTooLarge` ("file_too_large") and `core.FileInputReasonTypeNotAccepted` ("file_type_not_accepted") align with bridge JS sentinel error names.
- [x] `server.WithFileInputValidation()` Option enables the dispatcher hook. Disabled by default — handlers can opt in.
- [x] Dispatcher walks the tool's InputSchema for `x-mcp-file` properties (single + array `items` shape), runs `core.ValidateFileInput` on every matching arg, returns `-32602` with structured `data: {reason, field, actualSize, maxSize}` (too-large) or `data: {reason, field, mediaType, accept}` (wrong MIME) on failure. Wire shape frozen by `conformance/file-inputs/scenarios.test.ts`.
- [x] `examples/file-inputs/` opts into the validator — manual hand-rolled checks dropped.
- [x] 11 new core unit tests + 5 new server unit tests + 2 conformance scenarios (`file-inputs-04` + `file-inputs-05`) flipped from red to green.

### 1.5: Server capability gating ✅ shipped

**Files:** `core/file_input.go`, `core/file_input_test.go`, `core/handler_context.go`, `server/dispatch.go`, `server/file_validation_test.go`

- [x] `core.StripFileInputKeywords(schema any) any` — pure function, deep-copy walk that removes the keyword from every property (single + array items). Foreign shapes (typed structs, json.RawMessage on the elicitation path) pass through unchanged.
- [x] Dispatcher's `handleToolsList` strips when `d.clientCaps.FileInputs == nil`. Stored ToolDef.InputSchema in the registry is never mutated — different clients on the same server may declare the cap and need the keyword back.
- [x] `BaseContext.Elicit` strips `requestedSchema` (json.RawMessage decode → strip → re-encode) when `clientCaps.FileInputs == nil`. Round-trip cost only paid for cap-less clients.
- [x] **Spec interpretation locked**: strip the keyword, keep the property visible (legacy clients still call the tool with a text-input fallback). Documented in `conformance/file-inputs/README.md`. Asserted by scenario `file-inputs-02`.
- [x] Tests: 2 core (strip + foreign-shape passthrough), 2 server (cap-aware sees keyword, cap-less sees stripped property), 1 conformance scenario flipped red→green.

### 1.6: Client helpers ✅ shipped

**Files:** `client/file_input.go` (new), `client/file_input_test.go` (new), `client/client.go`

- [x] `client.FileInputsFromTool(tool) map[string]FileInputDescriptor` — single-file lands under `"name"`, array shape under `"name[]"` so callers can disambiguate.
- [x] `client.PrepareFileArg(path, desc) (string, error)` — reads file, detects MIME (extension lookup → http.DetectContentType fallback → octet-stream), runs validation via `core.ValidateFileInput` (same rules as server side), returns data URI. Surfaces typed errors (`*core.FileTooLargeError`, `*core.FileTypeNotAcceptedError`) so callers branch with `errors.As`.
- [x] `client.WithFileInputs() ClientOption` — sets `capabilities.fileInputs={}` on initialize. Without it, the server-side gating from 1.5 strips `x-mcp-file` from `tools/list`.
- [x] Plumbed into `Client.Connect()` capability building.
- [x] Tests: 8 — single + array extraction, foreign-shape passthrough, happy-path round-trip, oversized + wrong-MIME rejection (with errors.Is + errors.As), nil descriptor skips validation, capability advertisement.
- [x] Demo simplified: `examples/file-inputs/walkthrough.go` drops hand-rolled `walkSchemaForFileInputs` + `guessImageMIME` + manual `os.ReadFile` + `EncodeDataURI`. Now uses `WithFileInputs()` + `FileInputsFromTool` + `PrepareFileArg` end-to-end.

### 1.7: Example + tests 🟡 starter shipped (conformance pending)

**Files:** `examples/file-inputs/` (shipped: `main.go`, `walkthrough.go`, `Makefile`, `README.md`, `WALKTHROUGH.md`, `testdata/{pixel.png,contract.pdf,appendix.pdf,README.txt}`)

- [x] Demokit walkthrough running against a real `make serve` MCP server.
- [x] Three demo tools: `upload_image` (image/* + 5 MB cap), `analyze_documents` (array of PDFs), `process_any_file` (no filter).
- [x] Embedded fixtures via `//go:embed testdata/...` so `make demo` is hermetic; readers can inspect / replace real files on disk.
- [x] Optional `--file <path>` step exercises the on-disk → data URI path.
- [x] VS Code MCP config in `examples/file-inputs/README.md`.
- [ ] Conformance scenarios pending Phase 1.4–1.6 (oversized → `file_too_large`, wrong MIME → `file_type_not_accepted`, capability gating, multi-file arrays, percent-encoded filename round-trip).

Example server with tools:
- `upload_image` — accepts a single image file (`accept: ["image/*"]`, `maxSize: 5MB`)
- `analyze_documents` — accepts multiple PDF files (`accept: [".pdf", "application/pdf"]`)
- `process_any_file` — accepts any file type (no accept filter)

Example showing:
- Tool registration with `x-mcp-file` schema properties
- Data URI decoding in handler
- Size/type validation
- Error responses for invalid inputs

Conformance-style tests:
- [x] Client with `fileInputs` cap sees `x-mcp-file` in tool schema
- [x] Client without `fileInputs` cap does NOT see `x-mcp-file`
- [x] Valid file upload succeeds
- [x] Oversized file returns `-32602` with `"file_too_large"`
- [x] Wrong MIME type returns `-32602` with `"file_type_not_accepted"`
- [x] Multi-file array input works
- [x] Filename with special chars round-trips through percent-encoding

**`make testconf-file-inputs` — 7/7 passing.** Phase 1 of SEP-2356 fully implemented mcpkit-side. Suite is now the WG-facing acceptance bar — any reference impl can be pointed at it.

## Phase 2: MCP Apps bridge integration

### 2.1: Bridge `selectFile` API ✅ shipped

**Files:** `ext/ui/assets/mcp-app-bridge.ts`, `ext/ui/assets/file-picker.ts` (new), `ext/ui/assets/mcp-app-bridge.d.ts`, `ext/ui/assets/mcp-app-bridge.test.ts`, `ext/ui/assets/package.json`, `examples/file-inputs/apps/{upload-image,analyze-documents}.html`, `examples/file-inputs/apps.go`

- [x] `mcp.selectFile(descriptor) → Promise<string>` (single) and `mcp.selectFiles(descriptor) → Promise<string[]>` (multi).
- [x] Synthesizes a hidden `<input type="file">`; sets `accept` from descriptor (joined with comma); sets `multiple` for multi.
- [x] FileReader → data URI; injects `name=<percent-encoded>` parameter using a PathEscape-equivalent encoder so output matches `core.EncodeDataURI` byte-for-byte.
- [x] Client-side validation runs BEFORE FileReader: `maxSize` byte check, accept-pattern matcher (exact MIME / wildcard subtype / extension hint).
- [x] Sentinel errors: `MCPFileSelectionCanceled`, `MCPFileTooLarge`, `MCPFileTypeNotAccepted`. `reason` fields align with server-side `-32602` codes from #361.
- [x] Cancel detection: native `cancel` event (Chrome 113+ / Safari 17+ / Firefox 91+) plus focus-return fallback.
- [x] Picker code lives in its own `file-picker.ts` module (extracted from the main bridge file); bundled via esbuild — build switched from `tsc` to `esbuild --bundle --format=iife`.
- [x] 13 new vitest cases (61/61 total) covering happy path, percent-encoding, accept propagation, oversized rejection, MIME mismatch, wildcard match, extension match, empty descriptor, cancel, multi-file ordering, binary round-trip, canonical `core.EncodeDataURI` interop.
- [x] Apps-mode fixtures in `examples/file-inputs/apps/` — `apps_upload_image` and `apps_analyze_documents` tools register `ui://` resources whose HTML drives `mcp.selectFile` / `mcp.selectFiles` then routes through the existing tool handlers.
- [x] Host-mediated variant (`via: "host"` to route through host postMessage) deferred to follow-up issue #370.

### 2.2: Bridge elicitation file picker

**Files:** `ext/ui/assets/mcp-app-bridge.ts`

- [ ] When bridge receives an elicitation schema with `x-mcp-file` on a
  `StringSchema`, render a file picker widget instead of a text input
- [ ] Use the `FileInputDescriptor` from `x-mcp-file` for `accept`/`maxSize`
- [ ] Return selected file as data URI in the elicitation response content

### 2.3: Large file considerations

- [ ] Document: data URIs in `postMessage` can be slow for large files
- [ ] Consider `maxSize` guidance: recommend ≤10MB for inline data URIs
- [ ] Future: `URL.createObjectURL` + blob transfer for larger files
  (out of scope for SEP-2356 — spec explicitly uses data URIs)

### 2.4: CSP considerations

- [ ] Ensure bridge CSP allows `data:` URIs in relevant contexts
- [ ] Document that `data:` URIs may be blocked by strict CSP policies

## Implementation order

```
Phase 1:
  1.1 (types) → 1.2 (data URI) → 1.3 (schema helpers) →
  1.4 (validation) → 1.5 (capability gating) → 1.6 (client) →
  1.7 (example + tests)

Phase 2 (follow-up PR):
  2.1 (bridge selectFile) → 2.2 (elicitation file picker) →
  2.3 + 2.4 (docs)
```

Phase 1 is self-contained. Phase 2 depends on Phase 1 types but can be a
separate PR.

## Cross-SEP interactions

| SEP | Interaction | Impact |
|-----|-------------|--------|
| SEP-2663 (Tasks) | File input in a tool that returns a task — no conflict | None |
| SEP-2322 (MRTR) | MRTR `inputRequests` could include elicitation with `x-mcp-file` | Composes cleanly |
| SEP-2575 (Stateless) | `fileInputs` capability in per-request `_meta` | Same pattern as tasks extension |
| SEP-1865 (MCP Apps) | Bridge needs `selectFile` + file picker rendering | Phase 2 |

## Open questions

1. **Capability gating behavior:** When client lacks `fileInputs`, does the server
   strip `x-mcp-file` from the schema (tool still visible, just no file picker hint)?
   Or hide the entire tool? Spec says "MUST NOT include file input fields" — likely
   means strip the keyword, not hide the tool.
2. **Elicitation StringSchema in Go:** Do we have a typed `StringSchema` in core?
   Or is elicitation `requestedSchema` just `json.RawMessage`? Need to check.
3. **TypedTool integration:** `TypedTool[In, Out]` auto-derives JSON Schema from
   Go structs. How does a struct field opt into `x-mcp-file`? Custom struct tag?
   e.g. `FileArg string \`json:"photo" mcp:"file,accept=image/*,maxSize=5242880"\``
