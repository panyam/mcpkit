# SEP-2356 File Inputs — Data URI File Arguments

Demonstrates the `x-mcp-file` JSON Schema extension: a server marks string
properties of `format: "uri"` as file pickers, the client encodes the
selected file as an RFC 2397 base64 data URI
(`data:<mediatype>;name=<pct-encoded-filename>;base64,<payload>`), and the
server decodes the URI on the receiving end.

Spec: modelcontextprotocol/specification PR 2356.

## Three demo tools

| Tool | Schema marker | Demonstrates |
|------|--------------|--------------|
| `upload_image` | `image: FileInputProperty(image/*, max 5 MB)` | single-file picker with MIME wildcard + size cap |
| `analyze_documents` | `documents: FileInputArrayProperty(application/pdf, .pdf)` | array of files (`items.x-mcp-file`) |
| `process_any_file` | `file: FileInputProperty({})` | empty descriptor — any file, any size |

## Quick Start

```bash
# Terminal 1 — start the MCP server
make serve

# Terminal 2 — scripted walkthrough
make demo
# or for the interactive TUI:
go run . --tui
```

The walkthrough reads four embedded fixtures from
[`testdata/`](testdata/) — `pixel.png`, `contract.pdf`, `appendix.pdf`,
`README.txt` — encodes each via `core.EncodeDataURI`, and invokes every
tool. See [WALKTHROUGH.md](WALKTHROUGH.md) for the full sequence diagram
and step-by-step description.

To upload a file from disk instead, pass `--file <path>` to the demo:

```bash
go run . --file /path/to/photo.png
```

That step is skipped silently when the flag is absent so the default run
stays hermetic.

## Connecting an external MCP host

Any MCP host can drive the running server.

### VS Code (MCP extension)

Add to your VS Code MCP settings (`Cmd+Shift+P` → *MCP: Edit User
Configuration*):

```json
{
  "mcpServers": {
    "file-inputs-demo": {
      "type": "streamable-http",
      "url": "http://localhost:8080/mcp"
    }
  }
}
```

Reload the MCP view; the three tools will appear under
`file-inputs-demo`. Until VS Code learns the SEP-2356 file-picker hint, it
shows the file argument as a plain string — paste a data URI manually
(`data:image/png;base64,iVBORw0K…`) or use a small helper to build one.

### Claude Code

```bash
claude mcp add file-inputs --transport streamable-http http://localhost:8080/mcp
```

### MCPJam

1. Server URL: `http://localhost:8080/mcp`
2. Transport: Streamable HTTP

## Wire format cheatsheet

```
data:image/png;name=pixel.png;base64,iVBORw0KGgoAAAANS...
└─┬─┘└───┬──┘ └────┬────┘└─┬─┘└──────────┬──────────┘
  │     │         │       │              │
  scheme media-   filename encoding      payload
        type     (pct-enc) marker        (base64)
```

- `core.EncodeDataURI(data, mediaType, filename)` builds the string.
- `core.DecodeDataURI(uri)` returns `(data, mediaType, filename, error)`
  and rejects non-base64 forms with `ErrNonBase64DataURI`.
- `core.IsDataURI(s)` is a cheap prefix check.

## Where to look in the code

| What | Where |
|------|-------|
| Schema helpers | [`core.FileInputProperty`](../../core/file_input.go), `FileInputArrayProperty`, `ExtractFileInputDescriptor` |
| Wire encoding | [`core.EncodeDataURI`](../../core/datauri.go), `DecodeDataURI`, `IsDataURI` |
| Capability marker | `ClientCapabilities.FileInputs` ([`core/protocol.go`](../../core/protocol.go)), `core.HasFileInputs(ctx)` |
| SEP plan | [`docs/SEP_2356_FILE_INPUTS_PLAN.md`](../../docs/SEP_2356_FILE_INPUTS_PLAN.md) |
| Spec | modelcontextprotocol/specification PR 2356 |

## What's still pending

This example exercises Phase 1.1 (types) + 1.2 (data URIs) + a starter
1.7 walkthrough. Subsequent phases will extend it:

- **1.4** — `server.ValidateFileInput` for size/MIME enforcement (the
  handlers will swap their inline checks for the shared validator).
- **1.5** — strip `x-mcp-file` from `tools/list` when the client did not
  declare `capabilities.fileInputs`.
- **1.6** — `client.PrepareFileArg(path, descriptor)` so the host side of
  the walkthrough can drop the manual `os.ReadFile` + `EncodeDataURI`
  boilerplate.
- **2.x** — `mcp.selectFile()` in the MCP Apps bridge so an in-iframe app
  can prompt the user for a file and forward the data URI to a tool.
