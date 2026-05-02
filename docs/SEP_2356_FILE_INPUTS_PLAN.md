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

### 1.4: Server-side validation

**Files:** `server/file_validation.go` (new)

- [ ] `ValidateFileInput(value string, desc *FileInputDescriptor) error`
  — parse as data URI, check MIME against `accept` patterns, check decoded
  size against `maxSize`
- [ ] MIME matching: exact match (`image/png`), wildcard subtype (`image/*`),
  extension hint (`.pdf` — match against known MIME types)
- [ ] Return `-32602` with reason `"file_too_large"` for oversized files
- [ ] Return `-32602` with reason `"file_type_not_accepted"` for MIME mismatch
- [ ] Optional: middleware that auto-validates file inputs before handler runs
  (inspect inputSchema for `x-mcp-file` properties, validate matching args)

**Test:** `server/file_validation_test.go` — valid file passes, oversized rejected,
wrong MIME rejected, wildcard matching, extension matching.

### 1.5: Server capability gating

**Files:** `server/dispatch.go`

- [ ] When building tool list for `tools/list`: if client does NOT have
  `fileInputs` capability, strip `x-mcp-file` from schema properties
  (or omit tools that require file input? spec says MUST NOT include
  file input fields — clarify: strip the extension keyword, or hide the tool?)
- [ ] Same for elicitation: strip `x-mcp-file` from `requestedSchema` if
  client doesn't support

**Test:** Tools/list with and without `fileInputs` capability.

### 1.6: Client helpers

**Files:** `client/file_input.go` (new)

- [ ] `FileInputsFromTool(tool ToolDef) map[string]FileInputDescriptor`
  — scan a tool's inputSchema for `x-mcp-file` properties, return map of
  property name → descriptor
- [ ] `PrepareFileArg(path string, desc *FileInputDescriptor) (string, error)`
  — read file from path, validate against descriptor, encode as data URI
- [ ] Declare `fileInputs` capability in client initialization when configured

**Test:** `client/file_input_test.go` — extract descriptors from schema,
encode file as data URI argument.

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
- [ ] Client with `fileInputs` cap sees `x-mcp-file` in tool schema
- [ ] Client without `fileInputs` cap does NOT see `x-mcp-file`
- [ ] Valid file upload succeeds
- [ ] Oversized file returns `-32602` with `"file_too_large"`
- [ ] Wrong MIME type returns `-32602` with `"file_type_not_accepted"`
- [ ] Multi-file array input works
- [ ] Filename with special chars round-trips through percent-encoding

## Phase 2: MCP Apps bridge integration

### 2.1: Bridge `selectFile` API

**Files:** `ext/ui/assets/mcp-app-bridge.ts`

- [ ] New bridge method:
  ```js
  mcp.selectFile({ accept: ["image/*"], maxSize: 5242880 })
    .then(dataUri => { /* "data:image/png;name=photo.png;base64,..." */ })
  ```
- [ ] Opens native `<input type="file">` with `accept` attribute from descriptor
- [ ] Reads file via `FileReader.readAsDataURL`
- [ ] Adds `name=` parameter (percent-encoded original filename)
- [ ] Client-side size check against `maxSize` before encoding
- [ ] Returns the data URI string for the app to include in tool arguments

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
