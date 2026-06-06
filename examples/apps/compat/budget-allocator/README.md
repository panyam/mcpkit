# budget-allocator — deeply nested SaaS budget data

Rung 4 on the [examples ladder](../README.md#reading-order--examples-ladder).
One tool, but the output is a deeply nested object (config + analytics
with multi-month history and stage benchmarks). First fixture where
reflection of nested Go structs + maps produces the matching shape
without override.

## What it Shows

- **Multi-level nested output.** `get-budget-data` returns a Budget
  Config (categories + presets) plus Analytics (24 months of
  historical allocations + benchmarks by company stage). Each nested
  level uses Go structs + `map[string]any`-style typed maps; the
  reflector emits the matching JSON Schema cleanly.
- **No override needed.** Standard struct tags get this right. Sits
  in the sweet spot of what reflection can handle.

## Or Run Live

### Start Server

```bash
make demo-app EXAMPLE=budget-allocator
```

Starts the mcpkit-Go fixture on `http://localhost:3101/mcp` and basic-host on `http://localhost:8080`. (Pass `OPEN=1` to auto-open the browser.)

## Try It Out on basic-host

Open <http://localhost:8080> in your browser. Then:

1. Pick **Budget Allocator Server** from the server dropdown.
2. Pick **get-budget-data** from the tool dropdown, click **Call Tool**
   with empty input.
3. The iframe renders. Interact directly — drag the sliders, switch
   company stages, compare against the benchmark bands.

<a href="screenshots/01-default-budget.png" target="_blank"><img src="screenshots/01-default-budget.png" alt="Budget Allocator App: iframe shows the budget configuration with category sliders + preset chips + the analytics charts panel" width="50%"></a>

## Try It Out from a Host

Connect to `http://localhost:3101/mcp` from your favorite MCP host — VS Code, Claude Desktop, [MCPJam Inspector](https://github.com/MCPJam/inspector), or any spec-compliant client.

See [Other ways to test a fixture](../README.md#other-ways-to-test-a-fixture) in the compat README for wire inspection, upstream comparison, the strict Playwright gate, and connecting from VS Code / Claude Desktop / other MCP hosts.

## What to Try Next

- [`scenario-modeler`](../scenario-modeler/README.md) — rung-4
  sibling; adds a nullable field at depth that reflection alone
  CAN'T produce (OutputSchemaOverride still required).
- [`cohort-heatmap`](../cohort-heatmap/README.md) /
  [`customer-segmentation`](../customer-segmentation/README.md) —
  same rung, different data shapes.
