# customer-segmentation — customer clusters with multi-dimension features

Rung 4 on the [examples ladder](../README.md#reading-order--examples-ladder).
One tool, customer cluster data. The iframe renders cluster scatter
plots + per-segment summaries.

## What it shows

- **Customer segmentation output.** `get-customer-data` returns
  cluster assignments + per-cluster feature averages + per-customer
  records. The iframe lets you explore segments interactively.
- **Multi-dimensional records.** Each customer record carries
  numeric + categorical features; clusters carry feature averages
  + sizes. Reflection of the nested Go shape produces the schema
  cleanly without override.

## Run it

Boots the mcpkit-Go fixture (`main.go` in this folder) and opens
[MCPJam Inspector](https://github.com/MCPJam/inspector) so you can poke
at the protocol surface:

```bash
make demo-app EXAMPLE=customer-segmentation
```

Paste `http://localhost:3101/mcp` into MCPJam's server list and connect.
Then browse `tools/list`, `_meta.ui`, and tool-call payloads on the wire.

See [Other ways to test a fixture](../README.md#other-ways-to-test-a-fixture) in the compat README for wire inspection, upstream comparison, and the strict Playwright gate.

## Prompts to try

Connect to `Customer Segmentation Server`, then paste any of these:

```
Show me my customer segments.
```

![Customer Segmentation App: iframe shows the cluster scatter plot with color-coded segments + the per-segment summary panel on the right](screenshots/01-cluster-view.png)

```
Cluster my customers and visualize the segments.
```

```
Which customer segments are most valuable?
```

![Customer Segmentation iframe with the high-value segment highlighted and its feature averages shown in the summary panel](screenshots/02-high-value-segment.png)

```
Display customer segmentation with average revenue per cluster.
```

The model calls `get-customer-data`; the iframe renders the cluster
scatter plot + per-segment summaries.

### Direct tool call (no LLM needed)

| What | How | What you should see |
|---|---|---|
| Smoke test | Select `get-customer-data`, call with empty input | Tool result: nested clusters + customer records in `structuredContent` |
| Iframe renders the clusters | Same call, scroll up | Scatter plot + segment summary in the App iframe |

## What to look at next

- [`cohort-heatmap`](../cohort-heatmap/README.md) — rung-4 sibling,
  different analytical shape.
- [`budget-allocator`](../budget-allocator/README.md) — rung-4 with
  config + analytics nested model.
