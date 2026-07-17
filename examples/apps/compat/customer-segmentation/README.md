# customer-segmentation — customer clusters with multi-dimension features

Rung 4 on the [examples ladder](../README.md#reading-order--examples-ladder).
One tool, 250-row customer payload. The iframe renders an interactive
Chart.js scatter plot with two configurable axes, a configurable size
metric, and a per-segment legend / hover detail panel.

## What it Shows

- **Real clustered data on the wire.** `get-customer-data` returns 250
  customers split across 4 segments (Enterprise / Mid-Market / SMB /
  Startup), each drawn from a Box-Muller-clustered Gaussian shaped by
  per-segment ranges. Server-side, the RNG is seeded with `PCG(42, 42)`
  so the scatter plot is bit-stable across runs.
- **Segment filter round trip.** The tool accepts an optional
  `segment` enum (`All` + the four segments). The iframe's dropdown
  re-issues `tools/call` with `{segment: "Enterprise"}` etc. — same
  call shape, server-side filter.
- **Session-stable pool.** The 250-row pool is generated once per
  process via `sync.Once`, so flipping the dropdown re-filters the
  same dataset rather than redrawing the chart.

## Run Pre-Recorded

> ▶ **[Play the walkthrough in your browser](https://panyam.github.io/mcpkit/walkthroughs/examples/apps/compat/customer-segmentation/)** — animated playback of every curl / Go call the walkthrough makes, step-by-step. No clone, no setup.

## Or Run Live

### Start Server

```bash
just demo-app EXAMPLE=customer-segmentation
```

Starts the mcpkit-Go fixture on `http://localhost:3101/mcp` and basic-host on `http://localhost:8080`. (Pass `OPEN=1` to auto-open the browser.)

## Try It Out on basic-host

Open <http://localhost:8080> in your browser. Then:

1. Pick **Customer Segmentation Server** from the server dropdown.
2. Pick **get-customer-data** from the tool dropdown, click **Call Tool** with empty input.
3. The iframe renders the scatter plot. Flip the segment dropdown to see basic-host re-issue `tools/call` with the new filter; flip the X / Y axis selectors or the size metric to re-plot the same dataset (no model in the loop).

<a href="screenshots/01-cluster-view.png" target="_blank"><img src="screenshots/01-cluster-view.png" alt="Customer Segmentation App: iframe shows the cluster scatter plot with color-coded segments + the per-segment summary panel on the right" width="50%"></a>

## Try It Out from a Host

Connect to `http://localhost:3101/mcp` from your favorite MCP host — VS Code, Claude Desktop, [MCPJam Inspector](https://github.com/MCPJam/inspector), or any spec-compliant client.

**Prompts to try** (LLM-driven hosts):

> "Show me my customer segments."
> "Which customer segments are most valuable by revenue?"
> "Just the Enterprise customers, please."

The model calls `get-customer-data` (optionally with `segment`); the
iframe renders the resulting scatter plot + per-segment legend.

See [Other ways to test a fixture](../README.md#other-ways-to-test-a-fixture) in the compat README for wire inspection, upstream comparison, the strict Playwright gate, and connecting from VS Code / Claude Desktop / other MCP hosts.

## What to Try Next

- [`cohort-heatmap`](../cohort-heatmap/README.md) — rung-4 sibling,
  different analytical shape (heatmap instead of scatter plot).
- [`budget-allocator`](../budget-allocator/README.md) — rung-4 with
  config + analytics nested model and richer bridge dance.
- See [`main.go`](main.go) for the data generator port — clustered
  Gaussian, seeded RNG.
