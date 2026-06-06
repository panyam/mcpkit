# wiki-explorer — interactive graph App with a nullable output field

Rung 5 on the [examples ladder](../README.md#reading-order--examples-ladder).
One tool, but the App iframe renders an interactive force-directed
Wikipedia link graph — first example where the iframe is doing real
work, not just printing the tool result.

## What it Shows

- **Interactive App UI.** The iframe pulls in a graph library, lays
  out nodes for the linked Wikipedia pages, and lets the user click to
  expand or query. The host doesn't know any of that — it sees one
  tool with structured output.
- **Nullable field at the wire.** The output's `error` field is
  `*string` in Go (nil-or-string). Upstream's `z.string().nullable()`
  emits `{"anyOf": [{"type":"string"}, {"type":"null"}]}` in JSON
  Schema, which Go's reflection alone can't produce. The fixture uses
  `OutputSchemaPatch` with `Prop("error").Replace(...)` to land the
  matching shape without restating the whole schema.
- **The patch-builder pattern in practice.** ~8 lines of patch vs ~33
  lines of full-override that the fixture used to ship. Reflection
  still does the heavy lifting for `page` + `links`; the patch only
  touches the field reflection can't get right.

## Or Run Live

### Start Server

```bash
make demo-app EXAMPLE=wiki-explorer
```

Starts the mcpkit-Go fixture on `http://localhost:3101/mcp` and basic-host on `http://localhost:8080`. (Pass `OPEN=1` to auto-open the browser.)

## Try It Out on basic-host

Open <http://localhost:8080> in your browser. Then:

1. Pick **Wiki Explorer** from the server dropdown.
2. Pick **get-first-degree-links** from the tool dropdown, click **Call Tool**.
3. The iframe renders the result; interact with it directly to drive subsequent tool calls (no model in the loop).

<a href="screenshots/01-mcp-graph.png" target="_blank"><img src="screenshots/01-mcp-graph.png" alt="Wiki Explorer App: force-directed graph in the iframe with the Model Context Protocol page at the center and its first-degree links spread around it" width="50%"></a>

## Try It Out from a Host

Connect to `http://localhost:3101/mcp` from your favorite MCP host — VS Code, Claude Desktop, [MCPJam Inspector](https://github.com/MCPJam/inspector), or any spec-compliant client.

**Prompts to try** (LLM-driven hosts):

> "Show me what the Wikipedia page for Model Context Protocol links to."
> "Explore the link graph from https://en.wikipedia.org/wiki/Knowledge_graph"
> "Get first-degree links for the Wikipedia article about Transformer architecture."
> "Build me a one-hop link graph starting at https://en.wikipedia.org/wiki/Model_context_protocol"

Any of these should make the model call `get-first-degree-links`. The
App iframe renders the result as a force-directed graph — click a
node directly and the App calls `get-first-degree-links` itself via
the bridge to expand from that node (no model in the loop).

**Verify the wire shape** (no LLM needed):

| What | How | What you should see |
|---|---|---|
| Smoke test the tool | Select `get-first-degree-links`, call with `{"url": "https://en.wikipedia.org/wiki/Model_context_protocol"}` | Result panel: `{"page": {"url":"…","title":"Model Context Protocol"}, "links": [...], "error": null}` |
| Verify nullable on the wire | Expand the tool's `outputSchema` and find the `error` property | `{"anyOf": [{"type":"string"}, {"type":"null"}]}` — the nullable anyOf form, not `"type": "string"` |

See [Other ways to test a fixture](../README.md#other-ways-to-test-a-fixture) in the compat README for wire inspection, upstream comparison, the strict Playwright gate, and connecting from VS Code / Claude Desktop / other MCP hosts.

## What to Try Next

- Compare against [`map`](../map/README.md) (rung 5, sibling) — two
  tools instead of one, less interactive iframe.
- [`pdf-server`](../pdf-server/README.md) (rung 7) takes the "iframe
  doing real work" pattern to its endgame with a 9-tool surface and
  server-side command queue.
- See [`main.go`](main.go) — the `OutputSchemaPatch` block is one
  contiguous chunk of code.
