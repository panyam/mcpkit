# customer-segmentation — customer clusters with multi-dimension features

Rung 4 on the [examples ladder](../README.md#reading-order--examples-ladder).
One tool, customer cluster data. The iframe renders cluster scatter
plots + per-segment summaries.

## What it Shows

- **Customer segmentation output.** `get-customer-data` returns
  cluster assignments + per-cluster feature averages + per-customer
  records. The iframe lets you explore segments interactively.
- **Multi-dimensional records.** Each customer record carries
  numeric + categorical features; clusters carry feature averages
  + sizes. Reflection of the nested Go shape produces the schema
  cleanly without override.

## Or Run Live

### Start Server

```bash
make demo-app EXAMPLE=customer-segmentation
```

Starts the mcpkit-Go fixture on `http://localhost:3101/mcp` and basic-host on `http://localhost:8080`. (Pass `OPEN=1` to auto-open the browser.)

## Try It Out on basic-host

Open <http://localhost:8080> in your browser. Then:

1. Pick **Customer Segmentation Server** from the server dropdown.
2. Pick **get-customer-data** from the tool dropdown, click **Call Tool**.
3. The iframe renders the result; interact with it directly to drive subsequent tool calls (no model in the loop).

<a href="screenshots/01-cluster-view.png" target="_blank"><img src="screenshots/01-cluster-view.png" alt="Customer Segmentation App: iframe shows the cluster scatter plot with color-coded segments + the per-segment summary panel on the right" width="50%"></a>

## Try It Out from a Host

Connect to `http://localhost:3101/mcp` from your favorite MCP host — VS Code, Claude Desktop, [MCPJam Inspector](https://github.com/MCPJam/inspector), or any spec-compliant client.

**Prompts to try** (LLM-driven hosts):

> "Show me my customer segments."
> "Cluster my customers and visualize the segments."
> "Which customer segments are most valuable?"
> "Display customer segmentation with average revenue per cluster."

The model calls `get-customer-data`; the iframe renders the cluster
scatter plot + per-segment summaries.

**Verify the wire shape** (no LLM needed):

| What | How | What you should see |
|---|---|---|
| Smoke test | Select `get-customer-data`, call with empty input | Tool result: nested clusters + customer records in `structuredContent` |
| Iframe renders the clusters | Same call, scroll up | Scatter plot + segment summary in the App iframe |

See [Other ways to test a fixture](../README.md#other-ways-to-test-a-fixture) in the compat README for wire inspection, upstream comparison, the strict Playwright gate, and connecting from VS Code / Claude Desktop / other MCP hosts.

## What to Try Next

- [`cohort-heatmap`](../cohort-heatmap/README.md) — rung-4 sibling,
  different analytical shape.
- [`budget-allocator`](../budget-allocator/README.md) — rung-4 with
  config + analytics nested model.
