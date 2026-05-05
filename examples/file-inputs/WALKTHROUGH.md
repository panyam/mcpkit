# MCP File Inputs (SEP-2356) — Data URI File Arguments

Walks through SEP-2356, which lets servers declare file-input properties on tool inputSchemas via the `x-mcp-file` JSON Schema extension. Clients render a file picker for those fields and pass selected files as RFC 2397 base64 data URIs (`data:<mediatype>;name=<filename>;base64,<...>`). The server decodes the URI, validates size/MIME, and processes the bytes.

## What you'll learn

- **Connect to the file-inputs server** — `client.NewClient(...)` + `Connect()`. Once Phase 1.6 lands, the client will auto-advertise `fileInputs` when the option is enabled; for now the demo connects with the default capability set and the server unconditionally exposes `x-mcp-file` so we can see the wire shape.
- **tools/list — confirm x-mcp-file appears on inputSchemas** — Bypass any typed helper and decode the raw response so we can see the JSON Schema shape exactly as a client would. `properties.image.x-mcp-file` carries `{accept: ["image/*"], maxSize: 5242880}` — that's the picker hint.
- **upload_image — encode + call with a real PNG** — Read `testdata/pixel.png` (a 24×24 RGB gradient, embedded at build time), encode it via `core.EncodeDataURI`, and pass the resulting string as the `image` argument. The handler runs `core.DecodeDataURI` to recover bytes, media type, and the original filename. Size and MIME validation will be enforced by `server.ValidateFileInput` once Phase 1.4 lands; today the handler trusts the input.
- **analyze_documents — array-of-files input** — Demonstrates `core.FileInputArrayProperty` — the schema marks the `documents` array's *items* with `x-mcp-file`, so a host renders one picker per row. The walkthrough loads two embedded PDFs (`testdata/contract.pdf`, `testdata/appendix.pdf`) and sends both in one call.
- **process_any_file — no accept/maxSize filter** — Empty `FileInputDescriptor{}` means "any file, any size." Useful for ad-hoc inspection. The handler still decodes via `core.DecodeDataURI`, which rejects malformed or non-base64 URIs. The walkthrough reads `testdata/README.txt` so the payload is a real on-disk file.
- **Optional: send a file from disk** — Pass `--file <path>` on the demo command line to read an image from disk and upload it. Skipped silently when the flag isn't set so the walkthrough stays hermetic; demonstrates the on-disk → data URI path you'd use in a real client integration. Phase 1.6 will fold this into `client.PrepareFileArg(path, descriptor)`.
- **upload_image rejects wrong MIME (text/plain into image/* slot)** — The descriptor declares `accept: ["image/*"]`. Sending a text/plain data URI hits the dispatcher's accept-pattern matcher (`core.FileMatchesAccept`), which fails before the handler runs. The error data carries `mediaType` (what we sent) and `accept` (what the server requires) so a client can render a useful message.

Equivalent curl:

```bash
URI='data:text/plain;name=x.txt;base64,aGVsbG8='
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"upload_image\",\"arguments\":{\"image\":\"$URI\"}}}"
```
- **upload_image rejects oversized payload (6 MiB into 5 MiB cap)** — Same descriptor declares `maxSize: 5_242_880` (5 MiB). We synthesize a 6 MiB null-byte buffer, encode as `image/png`, and send it. The validator decodes the data URI, sees the size cap is exceeded, and short-circuits with structured size info.

Equivalent curl (generates the 6 MiB payload via Python):

```bash
BIG=$(python3 -c 'import base64; print("data:image/png;name=big.png;base64," + base64.b64encode(b"\x00" * (6*1024*1024)).decode())')
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"upload_image\",\"arguments\":{\"image\":\"$BIG\"}}}"
```
- **analyze_documents rejects per-element with field path tracking** — Send a 2-element array where element 0 is a valid PDF and element 1 is a text/plain payload. The dispatcher's array-items walker validates each element against `items.x-mcp-file` and surfaces the path of the offender — `data.field == "documents[1]"`. Useful so a client rendering rich error UX can highlight the specific input that failed instead of asking the user to re-pick everything.

Equivalent curl (note the array form in `arguments.documents`):

```bash
GOOD='data:application/pdf;name=ok.pdf;base64,JVBERi0xLjQKJSVFT0YK'
BAD='data:text/plain;name=bad.txt;base64,aGVsbG8='
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"analyze_documents\",\"arguments\":{\"documents\":[\"$GOOD\",\"$BAD\"]}}}"
```

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

    Note over Host,Server: Step 7: upload_image rejects wrong MIME (text/plain into image/* slot)
    Host->>Server: tools/call upload_image { image: data:text/plain;… }
    Server-->>Host: -32602 + data: {reason: file_type_not_accepted, mediaType, accept, field}

    Note over Host,Server: Step 8: upload_image rejects oversized payload (6 MiB into 5 MiB cap)
    Host->>Server: tools/call upload_image { image: data:image/png;… 6 MiB }
    Server-->>Host: -32602 + data: {reason: file_too_large, actualSize, maxSize, field}

    Note over Host,Server: Step 9: analyze_documents rejects per-element with field path tracking
    Host->>Server: tools/call analyze_documents { documents: [valid pdf, text/plain] }
    Server-->>Host: -32602 + data.field = "documents[1]"
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

### Validation — server rejects non-conforming uploads (Phase 1.4)

The server is started with `server.WithFileInputValidation()` (see `examples/file-inputs/main.go`), so the dispatcher walks each tool's `inputSchema` for `x-mcp-file` properties and runs `core.ValidateFileInput` on every matching arg BEFORE the handler runs. Failures surface as JSON-RPC `-32602` with a structured `data` payload — that exact shape is the contract pinned by `conformance/file-inputs/scenarios.test.ts`.

The next three steps exercise all three failure modes the validator covers. Each step also includes the equivalent raw `curl` so you can repro on the wire — the demo's helpers (`c.Call`, `client.RPCError`) are just convenience wrappers over the same JSON-RPC traffic.

To repro any of these manually:

```bash
# Init a session first
SID=$(curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -d '{"jsonrpc":"2.0","id":"i","method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"x","version":"1"},"capabilities":{"fileInputs":{}}}}' \
  -D - -o /dev/null | grep -i 'mcp-session-id' | awk '{print $2}' | tr -d '\r\n')
curl -s -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" -H "Accept: application/json" -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' >/dev/null
```

With `$SID` exported, the per-test curls below land cleanly on the running server.

### Step 7: upload_image rejects wrong MIME (text/plain into image/* slot)

The descriptor declares `accept: ["image/*"]`. Sending a text/plain data URI hits the dispatcher's accept-pattern matcher (`core.FileMatchesAccept`), which fails before the handler runs. The error data carries `mediaType` (what we sent) and `accept` (what the server requires) so a client can render a useful message.

Equivalent curl:

```bash
URI='data:text/plain;name=x.txt;base64,aGVsbG8='
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"upload_image\",\"arguments\":{\"image\":\"$URI\"}}}"
```

### Step 8: upload_image rejects oversized payload (6 MiB into 5 MiB cap)

Same descriptor declares `maxSize: 5_242_880` (5 MiB). We synthesize a 6 MiB null-byte buffer, encode as `image/png`, and send it. The validator decodes the data URI, sees the size cap is exceeded, and short-circuits with structured size info.

Equivalent curl (generates the 6 MiB payload via Python):

```bash
BIG=$(python3 -c 'import base64; print("data:image/png;name=big.png;base64," + base64.b64encode(b"\x00" * (6*1024*1024)).decode())')
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"upload_image\",\"arguments\":{\"image\":\"$BIG\"}}}"
```

### Step 9: analyze_documents rejects per-element with field path tracking

Send a 2-element array where element 0 is a valid PDF and element 1 is a text/plain payload. The dispatcher's array-items walker validates each element against `items.x-mcp-file` and surfaces the path of the offender — `data.field == "documents[1]"`. Useful so a client rendering rich error UX can highlight the specific input that failed instead of asking the user to re-pick everything.

Equivalent curl (note the array form in `arguments.documents`):

```bash
GOOD='data:application/pdf;name=ok.pdf;base64,JVBERi0xLjQKJSVFT0YK'
BAD='data:text/plain;name=bad.txt;base64,aGVsbG8='
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"analyze_documents\",\"arguments\":{\"documents\":[\"$GOOD\",\"$BAD\"]}}}"
```

### MCP Apps mode (Phase 2.1)

This same server also registers two MCP App tools that drive the same handlers via in-iframe file pickers — the human-in-the-loop case file-uploads-wg flagged as a gap:

- `apps_upload_image` — `ui://file-inputs/upload-image` — single image picker (`mcp.selectFile`)
- `apps_analyze_documents` — `ui://file-inputs/analyze-documents` — multi PDF picker (`mcp.selectFiles`)

To exercise these, point a host that supports the MCP Apps extension (e.g. MCPJam) at this server and invoke either tool — the host renders the embedded HTML + bridge, the user clicks the picker, and the bridge encodes the chosen file(s) as data URI(s) before calling the regular tool. The walkthrough above doesn't drive these because demokit can't synthesize iframe user-gestures.

### Where to look in the code

- Schema helpers: `core.FileInputProperty` / `core.FileInputArrayProperty` / `core.ExtractFileInputDescriptor` — core/file_input.go
- Wire encoding: `core.EncodeDataURI` / `core.DecodeDataURI` / `core.IsDataURI` — core/datauri.go
- Capability marker: `ClientCapabilities.FileInputs` + `core.HasFileInputs(ctx)` — core/protocol.go, core/file_input.go
- Server validation (Phase 1.4): `server.ValidateFileInput` — pending
- Capability gating (Phase 1.5): strip `x-mcp-file` from tools/list when client lacks the cap — pending
- Client helpers (Phase 1.6): `client.FileInputsFromTool` / `client.PrepareFileArg` — pending
- Bridge `selectFile` / `selectFiles` (Phase 2.1): `ext/ui/assets/file-picker.ts` — shipped
- Apps fixtures: `examples/file-inputs/apps/upload-image.html`, `analyze-documents.html`
- SEP-2356 spec: modelcontextprotocol/specification PR 2356

## Run it

```bash
go run ./examples/file-inputs/
```

Pass `--non-interactive` to skip pauses:

```bash
go run ./examples/file-inputs/ --non-interactive
```
