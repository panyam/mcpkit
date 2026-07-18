# pdf-server — 9-tool surface, command queue, long-poll

Rung 7 on the [examples ladder](../README.md#reading-order--examples-ladder) —
the most complex fixture. Goes beyond the request/response pattern of
the other examples: per-viewUUID command queue, long-poll endpoint,
server-initiated rendezvous (server enqueues a command, viewer
responds via separate tool, server's `Await` unblocks).

If you've worked through the earlier rungs, this is where the
patterns compound.

## What it Shows

- **9 tools**, mirroring upstream's `--enable-interact` surface:
  - `list_pdfs`, `read_pdf_bytes`, `display_pdf`, `save_pdf` — the
    base 4 that the default no-flag upstream surface also exposes.
  - `interact`, `poll_pdf_commands`, `submit_page_data`,
    `submit_save_data`, `submit_viewer_state` — the 5 that wire up
    the command-queue / rendezvous machinery.
- **Per-viewUUID state.** Each call to `display_pdf` mints a UUID;
  later `interact` calls reference it. Multiple PDFs can be open at
  once; the queue + waiters are per-UUID. Implementation in
  [`queue.go`](queue.go).
- **Long-poll endpoint.** The iframe calls `poll_pdf_commands` and
  blocks (server-side) until commands arrive or a timeout fires. The
  drain is batched — short window after wake-up lets in-flight
  `interact` calls join the same response.
- **Server-initiated rendezvous.** When the model asks for `get_text`
  or `get_screenshot`, the server enqueues a command with a
  `requestId`, then `Await`s a separate channel until the iframe calls
  `submit_page_data` with that `requestId`. Three rendezvous tables
  (pages, saves, viewer states) — each backed by a typed
  `gocurrent.SyncMap`.
- **HTTP range proxy.** `read_pdf_bytes` is a streaming range proxy
  for `https://` and `file://` URLs, so the iframe can stream PDFs
  larger than 512KB chunks. Implementation in
  [`bytes.go`](bytes.go).
- **The `core.ToolResultMeta.Extras` library addition.** `display_pdf`
  emits `_meta.interactEnabled`, `_meta.viewUUID`, `_meta.writable`
  via `Extras` — the wire spec lists `_meta` as an open object, so
  extension-namespaced keys spread at the top level of the meta
  object.

## Run Pre-Recorded

> ▶ **[Play the walkthrough in your browser](https://panyam.github.io/mcpkit/walkthroughs/examples/apps/compat/pdf-server/)** — animated playback of every curl / Go call the walkthrough makes, step-by-step. Covers the 9-tool surface inventory, list_pdfs, and a live display_pdf call (mints a real viewUUID against arxiv 1706.03762). The interact / poll / submit_* rendezvous is explained in narrative (it needs a real iframe to drive). No clone, no setup.

## Or Run Live

### Start Server

```bash
just demo-app EXAMPLE=pdf-server
```

Starts the mcpkit-Go fixture on `http://localhost:3101/mcp` and basic-host on `http://localhost:8080`. (Pass `OPEN=1` to auto-open the browser.)

## Try It Out on basic-host

Open <http://localhost:8080> in your browser. Then:

1. Pick **PDF Server** from the server dropdown.
2. Pick **display_pdf** from the tool dropdown, click **Call Tool**.
3. The iframe renders the result; interact with it directly to drive subsequent tool calls (no model in the loop).

<a href="screenshots/01-default-pdf.png" target="_blank"><img src="screenshots/01-default-pdf.png" alt="PDF Server with the default arxiv paper rendered in the iframe; tool result panel shows viewUUID in structuredContent and interactEnabled in _meta" width="50%"></a>

## Try It Out from a Host

Connect to `http://localhost:3101/mcp` from your favorite MCP host — VS Code, Claude Desktop, [MCPJam Inspector](https://github.com/MCPJam/inspector), or any spec-compliant client.

**Prompts to try** (LLM-driven hosts):

> "Show me the "Attention Is All You Need" paper."
> "Navigate to page 3."
> "Highlight every occurrence of "attention" in yellow."
> "Search for "transformer" and jump to the first match."
> "Zoom in to 150%."
> "Get the text from pages 2 through 4."
> "Take a screenshot of the current page."
> "What page am I currently on, and what's the zoom level?"

- The first prompt makes the model call `display_pdf` (the iframe
  renders the PDF, the tool result carries `viewUUID`). Subsequent
  prompts reuse that `viewUUID` via the `interact` tool — the model
  figures out the right `action`.
- `navigate`, `highlight_text`, `search`, `zoom` are fire-and-forget:
  server enqueues a command, viewer picks it up via long-poll.
- `get_text`, `get_screenshot`, `get_viewer_state` are
  request/response: server enqueues + blocks; viewer responds via
  `submit_page_data` / `submit_viewer_state`.

**Verify the wire shape** (no LLM needed):

| What | How | What you should see |
|---|---|---|
| Open the default PDF | Select `display_pdf`, call with empty input | Iframe renders the default arxiv PDF. Tool result has `viewUUID` in `structuredContent` AND in `_meta` (the `Extras` flow). |
| Custom PDF | `display_pdf` with `{"url": "https://arxiv.org/pdf/2401.04088"}` | Iframe renders the Mixtral paper. New viewUUID. |
| Navigate via interact | `interact` with `{"viewUUID":"<uuid>","action":"navigate","page":3}` | Iframe scrolls to page 3. Server enqueued a `{type:"navigate",page:3}` command; iframe long-polled and picked it up. |
| Highlight text | `interact` with `{"viewUUID":"<uuid>","action":"highlight_text","query":"attention","color":"yellow"}` | Iframe highlights every occurrence of "attention" on the current page in yellow. |
| Batched commands | `interact` with `{"viewUUID":"<uuid>","commands":[{"action":"navigate","page":1},{"action":"highlight_text","query":"transformer"},{"action":"zoom","scale":1.5}]}` | Three commands ride one tool call. Iframe applies them in order. |
| Server-initiated rendezvous | `interact` with `{"viewUUID":"<uuid>","action":"get_viewer_state"}` | Server enqueues a command with a generated requestId, blocks on its rendezvous channel. Iframe sees the command via the long-poll, then calls `submit_viewer_state` with that requestId carrying its live state. Server's await unblocks; the rendezvous payload becomes the tool result. |

See [Other ways to test a fixture](../README.md#other-ways-to-test-a-fixture) in the compat README for wire inspection, upstream comparison, the strict Playwright gate, and connecting from VS Code / Claude Desktop / other MCP hosts.

## What to Try Next

- This is the top of the ladder. If you want to see how the patterns
  compound from the bottom up, walk back through [`map`](../map/README.md)
  → [`wiki-explorer`](../wiki-explorer/README.md) →
  [`integration`](../integration/README.md).
- Read [`queue.go`](queue.go) — single-file implementation of the
  command queue + waiter + three rendezvous tables (~250 LOC).
- Read [`bytes.go`](bytes.go) — HTTP range proxy + local-file
  handling (~80 LOC).
- Open issue: [`#574`](https://github.com/panyam/mcpkit/issues/574)
  tracks the one remaining test (form-fields-names; needs a streaming
  AcroForm parser).
