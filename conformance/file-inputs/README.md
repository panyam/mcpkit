# SEP-2356 File Inputs — Conformance

Verifies that an MCP server correctly implements the SEP-2356 file-input
wire contract: `x-mcp-file` schema marker visibility gated on the client's
`fileInputs` capability, RFC 2397 base64 data URI round-trip, and
structured `-32602` error responses for oversized / wrong-MIME payloads.

[sep-2356]: https://github.com/modelcontextprotocol/specification/pull/2356

## Wire contract

| Concern | Expected wire shape |
|---|---|
| Schema marker (cap declared) | `x-mcp-file: <FileInputDescriptor>` on `{type: "string", format: "uri"}` properties (and on `items` for arrays) |
| Schema marker (no cap) | keyword stripped; underlying `string`/`uri` property remains so the tool stays callable |
| File payload | `data:<mediaType>;name=<pct-encoded-filename>;base64,<payload>` |
| Filename encoding | `url.PathEscape`-equivalent — encodes `( ) ! * '` (broader than `encodeURIComponent`) |
| Oversized payload | `-32602` + `data: {reason: "file_too_large", actualSize, maxSize}` |
| Wrong MIME | `-32602` + `data: {reason: "file_type_not_accepted", mediaType, accept}` |

The cap-gating interpretation locked here is **strip the keyword, keep the
property**. Spec text says "MUST NOT include file input fields" — this
suite reads that as the keyword, not the whole property, so legacy clients
without the SEP-2356 cap can still call the tools and render a text input
as a fallback.

## Server fixture

[`examples/file-inputs/`](../../examples/file-inputs/) registers three
demo tools that exercise every SEP-2356 shape:

| Tool | Descriptor | Shape |
|------|-----------|-------|
| `upload_image` | `accept: ["image/*"]`, `maxSize: 5 MiB` | single-file (`properties.image.x-mcp-file`) |
| `analyze_documents` | `accept: ["application/pdf", ".pdf"]` | array (`properties.documents.items.x-mcp-file`) |
| `process_any_file` | `{}` (no constraints) | single-file, no filter |

The Apps-mode wrappers (`apps_upload_image`, `apps_analyze_documents`)
are not exercised here — they're bridge UX, not wire format.

## Scenarios

| ID | What it tests | Status today | Unblocked by |
|----|---------------|--------------|--------------|
| `file-inputs-01` | client WITH `fileInputs` cap sees `x-mcp-file` on every file property | ✅ green | — |
| `file-inputs-02` | client WITHOUT cap does NOT see `x-mcp-file` (but tools stay visible) | ❌ red | [#362 — A4 capability gating](https://github.com/panyam/mcpkit/issues/362) |
| `file-inputs-03` | valid file upload round-trips bytes / media type / filename | ✅ green | — |
| `file-inputs-04` | oversized file → `-32602` + `data.reason: "file_too_large"` | ✅ green (A3 shipped) | — |
| `file-inputs-05` | wrong MIME → `-32602` + `data.reason: "file_type_not_accepted"` | ✅ green (A3 shipped) | — |
| `file-inputs-06` | array-of-files input (`analyze_documents`) handles multiple URIs | ✅ green | — |
| `file-inputs-07` | filename with special chars (parens, spaces, quotes) round-trips through percent-encoding | ✅ green | — |

6 green, 1 red. The remaining red (cap-gating) is the done-bar for A4
(#362). When `file-inputs-02` flips green, SEP-2356 Phase 1.4 + 1.5 are
complete and this suite becomes the WG-facing acceptance bar.

## Running

```bash
# from repo root — handles build + spawn + tear-down
make testconf-file-inputs

# or manually against an already-running server
cd conformance && npm install
SERVER_URL=http://localhost:18097/mcp \
  npx tsx --test file-inputs/scenarios.test.ts
```

## Why locked-in error data shape

Scenarios 4 and 5 assert structured error data:

```json
{
  "code": -32602,
  "message": "...",
  "data": {
    "reason": "file_too_large",
    "actualSize": 5243904,
    "maxSize": 5242880
  }
}
```

Locked here so:

- Multiple language SDKs converge on the same shape (this suite is the
  cross-impl contract).
- Clients can render rich error UX (showing the actual vs maximum size,
  not just a stringified message).
- The `reason` field doubles as a stable machine-readable identifier;
  bridge sentinel error names (`MCPFileTooLarge`, `MCPFileTypeNotAccepted`)
  align with the same constants on the JS side.

If the SEP-2356 / SEP-2631 reconciliation later reshapes the error
contract, this suite is the single point that needs updating.
