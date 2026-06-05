# budget-allocator — deeply nested SaaS budget data

Rung 4 on the [examples ladder](../README.md#reading-order--examples-ladder).
One tool, but the output is a deeply nested object (config + analytics
with multi-month history and stage benchmarks). First fixture where
reflection of nested Go structs + maps produces the matching shape
without override.

## What it shows

- **Multi-level nested output.** `get-budget-data` returns a Budget
  Config (categories + presets) plus Analytics (24 months of
  historical allocations + benchmarks by company stage). Each nested
  level uses Go structs + `map[string]any`-style typed maps; the
  reflector emits the matching JSON Schema cleanly.
- **No override needed.** Standard struct tags get this right. Sits
  in the sweet spot of what reflection can handle.

## Run it

```bash
# mcpkit-Go fixture + MCPJam (default — wire-level inspection)
make demo-app EXAMPLE=budget-allocator-server

# Same Go fixture rendered in basic-host (iframe + bridge JS)
RENDERER=basic-host make demo-app EXAMPLE=budget-allocator-server

# Compare against upstream's TS reference server
make demo-upstream EXAMPLE=budget-allocator-server

# Strict parity check (visual baseline + tools/list diff, requires Docker)
EXAMPLE=budget-allocator-server make test-apps-playwright-docker
```

## Prompts to try

Connect to `Budget Allocator Server`, then paste any of these:

```
Show me my SaaS budget allocation.
```

![Budget Allocator App: iframe shows the budget configuration with category sliders + preset chips + the analytics charts panel](screenshots/01-default-budget.png)

```
What does my budget look like for an early-stage startup?
```

```
Display the budget allocator with the analytics for the last 24 months.
```

![Analytics panel of the Budget Allocator iframe showing 24 months of historical category allocations + the stage-benchmark comparison](screenshots/02-analytics-view.png)

```
How should a Series A company allocate their budget across categories?
```

![Budget Allocator iframe showing benchmark comparison for Series A stage with per-category percentile bands](screenshots/03-series-a-benchmark.png)

The model calls `get-budget-data`; the iframe renders an interactive
budget allocator (sliders for categories, analytics charts, benchmark
comparison).

### Direct tool call (no LLM needed)

| What | How | What you should see |
|---|---|---|
| Smoke test | Select `get-budget-data`, call with empty input | Tool result: `{"config": {"categories": [...], "presetBudgets": [...]}, "analytics": {"history": [...], "benchmarks": [...]}}` |
| Verify the nested shape | Expand `outputSchema.properties.analytics.properties.history.items` | Nested item schema with month + per-category allocations — reflected from the Go struct, no override |

## What to look at next

- [`scenario-modeler`](../scenario-modeler/README.md) — rung-4
  sibling; adds a nullable field at depth that reflection alone
  CAN'T produce (OutputSchemaOverride still required).
- [`cohort-heatmap`](../cohort-heatmap/README.md) /
  [`customer-segmentation`](../customer-segmentation/README.md) —
  same rung, different data shapes.
