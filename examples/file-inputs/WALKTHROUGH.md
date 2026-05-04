# MCP File Inputs (SEP-2356) — Data URI File Arguments

Walks through SEP-2356, which lets servers declare file-input properties on tool inputSchemas via the `x-mcp-file` JSON Schema extension. Clients render a file picker for those fields and pass selected files as RFC 2397 base64 data URIs (`data:<mediatype>;name=<filename>;base64,<...>`). The server decodes the URI, validates size/MIME, and processes the bytes.

## What you'll learn

- **Connect to the file-inputs server** — `client.NewClient(...)` + `Connect()`. Once Phase 1.6 lands, the client will auto-advertise `fileInputs` when the option is enabled; for now the demo connects with the default capability set and the server unconditionally exposes `x-mcp-file` so we can see the wire shape.
- **tools/list — confirm x-mcp-file appears on inputSchemas** — Bypass any typed helper and decode the raw response so we can see the JSON Schema shape exactly as a client would. `properties.image.x-mcp-file` carries `{accept: ["image/*"], maxSize: 5242880}` — that's the picker hint.
- **upload_image — encode + call with a real PNG** — Read `testdata/pixel.png` (a 24×24 RGB gradient, embedded at build time), encode it via `core.EncodeDataURI`, and pass the resulting string as the `image` argument. The handler runs `core.DecodeDataURI` to recover bytes, media type, and the original filename. Size and MIME validation will be enforced by `server.ValidateFileInput` once Phase 1.4 lands; today the handler trusts the input.
- **analyze_documents — array-of-files input** — Demonstrates `core.FileInputArrayProperty` — the schema marks the `documents` array's *items* with `x-mcp-file`, so a host renders one picker per row. The walkthrough loads two embedded PDFs (`testdata/contract.pdf`, `testdata/appendix.pdf`) and sends both in one call.
- **process_any_file — no accept/maxSize filter** — Empty `FileInputDescriptor{}` means "any file, any size." Useful for ad-hoc inspection. The handler still decodes via `core.DecodeDataURI`, which rejects malformed or non-base64 URIs. The walkthrough reads `testdata/README.txt` so the payload is a real on-disk file.
- **Optional: send a file from disk** — Pass `--file <path>` on the demo command line to read an image from disk and upload it. Skipped silently when the flag isn't set so the walkthrough stays hermetic; demonstrates the on-disk → data URI path you'd use in a real client integration. Phase 1.6 will fold this into `client.PrepareFileArg(path, descriptor)`.

## Flow

```mermaid
sequenceDiagram
    participant Host as MCP Host (this client)
    participant Server as MCP Server (make serve)

    Note over Host,Server: Step 1: Connect to the file-inputs server
    Host->>Server: POST /mcp — initialize (capabilities.fileInputs={})
    Server-->>Host: serverInfo + capabilities

    Note over Host,Server: Step 2: tools/list — confirm x-mcp-file appears on inputSchemas
    Host->>Server: tools/list
    Server-->>Host: tools[] with x-mcp-file marked properties

    Note over Host,Server: Step 3: upload_image — encode + call with a real PNG
    Host->>Server: tools/call upload_image { image: data:image/png;name=…;base64,… }
    Server-->>Host: text result with size + media type

    Note over Host,Server: Step 4: analyze_documents — array-of-files input
    Host->>Server: tools/call analyze_documents { documents: [data:application/pdf;…, data:application/pdf;…] }
    Server-->>Host: summary line per document

    Note over Host,Server: Step 5: process_any_file — no accept/maxSize filter
    Host->>Server: tools/call process_any_file { file: data:text/plain;name=README.txt;base64,… }
    Server-->>Host: decoded media type + size

    Note over Host,Server: Step 6: Optional: send a file from disk
    Host->>Server: tools/call upload_image (from --file <path>)
    Server-->>Host: decoded metadata of the on-disk file
```

## Steps

### Setup

Start the MCP server in a separate terminal first:

```
Terminal 1:  make serve         # file-inputs server on :8080
Terminal 2:  make demo          # this walkthrough (--tui for the interactive TUI)
```

Any MCP host can connect to the running server (Claude Desktop, VS Code, MCPJam). The walkthrough below acts as a scripted host that reads files from disk, encodes them as data URIs via `core.EncodeDataURI`, and calls the tools. See the README for VS Code config.

### Wire format

SEP-2356 reuses two well-understood pieces:

- **Schema marker** — a string property of `format: "uri"` carries an extra `x-mcp-file` keyword whose value is a `FileInputDescriptor` (`accept` MIME patterns / extensions, optional `maxSize` in decoded bytes). Server-side helpers: `core.FileInputProperty(desc)` and `core.FileInputArrayProperty(desc)`.
- **Wire encoding** — files travel as RFC 2397 base64 data URIs with an optional percent-encoded `name=` parameter: `data:image/png;name=photo.png;base64,iVBORw0…`. Helpers: `core.EncodeDataURI(data, mediaType, filename)` and `core.DecodeDataURI(uri)`.

Capability negotiation: the client advertises `"fileInputs": {}` inside `ClientCapabilities` during `initialize`. Servers MUST NOT include `x-mcp-file` in tool schemas if the client did not declare the capability — `core.HasFileInputs(ctx)` is the server-side check (gating ships in Phase 1.5 of the plan).

### Step 1: Connect to the file-inputs server

`client.NewClient(...)` + `Connect()`. Once Phase 1.6 lands, the client will auto-advertise `fileInputs` when the option is enabled; for now the demo connects with the default capability set and the server unconditionally exposes `x-mcp-file` so we can see the wire shape.

### Step 2: tools/list — confirm x-mcp-file appears on inputSchemas

Bypass any typed helper and decode the raw response so we can see the JSON Schema shape exactly as a client would. `properties.image.x-mcp-file` carries `{accept: ["image/*"], maxSize: 5242880}` — that's the picker hint.

### Step 3: upload_image — encode + call with a real PNG

Read `testdata/pixel.png` (a 24×24 RGB gradient, embedded at build time), encode it via `core.EncodeDataURI`, and pass the resulting string as the `image` argument. The handler runs `core.DecodeDataURI` to recover bytes, media type, and the original filename. Size and MIME validation will be enforced by `server.ValidateFileInput` once Phase 1.4 lands; today the handler trusts the input.

### Step 4: analyze_documents — array-of-files input

Demonstrates `core.FileInputArrayProperty` — the schema marks the `documents` array's *items* with `x-mcp-file`, so a host renders one picker per row. The walkthrough loads two embedded PDFs (`testdata/contract.pdf`, `testdata/appendix.pdf`) and sends both in one call.

### Step 5: process_any_file — no accept/maxSize filter

Empty `FileInputDescriptor{}` means "any file, any size." Useful for ad-hoc inspection. The handler still decodes via `core.DecodeDataURI`, which rejects malformed or non-base64 URIs. The walkthrough reads `testdata/README.txt` so the payload is a real on-disk file.

### Step 6: Optional: send a file from disk

Pass `--file <path>` on the demo command line to read an image from disk and upload it. Skipped silently when the flag isn't set so the walkthrough stays hermetic; demonstrates the on-disk → data URI path you'd use in a real client integration. Phase 1.6 will fold this into `client.PrepareFileArg(path, descriptor)`.

### Where to look in the code

- Schema helpers: `core.FileInputProperty` / `core.FileInputArrayProperty` / `core.ExtractFileInputDescriptor` — core/file_input.go
- Wire encoding: `core.EncodeDataURI` / `core.DecodeDataURI` / `core.IsDataURI` — core/datauri.go
- Capability marker: `ClientCapabilities.FileInputs` + `core.HasFileInputs(ctx)` — core/protocol.go, core/file_input.go
- Server validation (Phase 1.4): `server.ValidateFileInput` — pending
- Capability gating (Phase 1.5): strip `x-mcp-file` from tools/list when client lacks the cap — pending
- Client helpers (Phase 1.6): `client.FileInputsFromTool` / `client.PrepareFileArg` — pending
- Bridge `selectFile` (Phase 2.1): `ext/ui/assets/mcp-app-bridge.ts` — pending
- SEP-2356 spec: modelcontextprotocol/specification PR 2356

## Run it

```bash
go run ./examples/file-inputs/
```

Pass `--non-interactive` to skip pauses:

```bash
go run ./examples/file-inputs/ --non-interactive
```
