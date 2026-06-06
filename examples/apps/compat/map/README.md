# map — CesiumJS interactive map + geocoding

Rung 5 on the [examples ladder](../README.md#reading-order--examples-ladder).
Two tools — one carries the iframe, the other is a plain MCP tool the
App calls via the bridge.

## What it Shows

- **App-side bridge calls.** `show-map` renders a CesiumJS globe in
  the iframe. The App calls `geocode` over the bridge (not via the
  model) to convert place names into bounding boxes. First example
  where a plain tool is exercised primarily by the App, not the
  model.
- **Comma-rich tool descriptions.** `geocode`'s input description
  contains a comma-separated list of examples (`"e.g., 'Paris',
  'Golden Gate Bridge', '1600 Pennsylvania Ave'"`). Struct-tag
  reflection would truncate at the first comma; `InputSchemaPatch`
  lands the full description through `Prop("query").Desc(...)
  .Required()`.

## Or Run Live

### Start Server

```bash
make demo-app EXAMPLE=map
```

Starts the mcpkit-Go fixture on `http://localhost:3101/mcp` and basic-host on `http://localhost:8080`. (Pass `OPEN=1` to auto-open the browser.)

## Try It Out on basic-host

Open <http://localhost:8080> in your browser. Then:

1. Pick **CesiumJS Map Server** from the server dropdown.
2. Pick **geocode** from the tool dropdown, click **Call Tool**.
3. The iframe renders the result; interact with it directly to drive subsequent tool calls (no model in the loop).

<a href="screenshots/01-paris-map.png" target="_blank"><img src="screenshots/01-paris-map.png" alt="CesiumJS Map App: iframe shows the globe zoomed to Paris with the camera positioned over the city center" width="50%"></a>

## Try It Out from a Host

Connect to `http://localhost:3101/mcp` from your favorite MCP host — VS Code, Claude Desktop, [MCPJam Inspector](https://github.com/MCPJam/inspector), or any spec-compliant client.

**Prompts to try** (LLM-driven hosts):

> "Show me a map of Paris."
> "Where is the Golden Gate Bridge? Show it on a map."
> "Geocode "1600 Pennsylvania Avenue" and then display it on the map."
> "Zoom in on Tokyo Tower."

The iframe also has its own search field — type a place name directly
and the App calls `geocode` via the bridge and updates the map (no
model in the loop).

**Verify the wire shape** (no LLM needed):

| What | How | What you should see |
|---|---|---|
| Render the default map | Select `show-map`, call with an empty input | Iframe renders the CesiumJS globe at the default view |
| Geocode a place | Select `geocode`, call with `{"query": "Paris"}` | Tool result: text block with coordinates + bounding box for up to 5 matches |
| Inspect comma-rich description | Expand `geocode`'s `inputSchema.properties.query.description` | The full `"...e.g., 'Paris', 'Golden Gate Bridge', '1600 Pennsylvania Ave'"` string survives intact |

See [Other ways to test a fixture](../README.md#other-ways-to-test-a-fixture) in the compat README for wire inspection, upstream comparison, the strict Playwright gate, and connecting from VS Code / Claude Desktop / other MCP hosts.

## What to Try Next

- [`wiki-explorer`](../wiki-explorer/README.md) (rung 5, sibling) —
  one-tool interactive graph; the App does the work itself.
- [`integration`](../integration/README.md) (rung 6) — App-callable
  tools with host-callback semantics (Send Message, Send Log, Open
  Link).
- See [`main.go`](main.go) — the `InputSchemaPatch` on `geocode`
  is one method-chain line.
