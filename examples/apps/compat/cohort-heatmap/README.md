# cohort-heatmap — retention heatmap with rich numeric data

Rung 4 on the [examples ladder](../README.md#reading-order--examples-ladder).
One tool, nested retention data. The iframe renders a classic
cohort-retention heatmap.

## What it shows

- **Retention cohort data.** `get-cohort-data` returns rows of
  cohorts and their retention percentages over time periods. Each
  row + column is a percentage; the iframe renders the matrix as a
  color-graded heatmap.
- **Integer-vs-number drift.** Cohort sizes are semantically integer.
  Go's `int` reflects to `"type": "integer"` while upstream's zod
  `z.number()` emits `"type": "number"`. The DOCKER drift comparator
  normalizes these (PR 549) so the fixture uses idiomatic Go types
  and still passes.

## Run it

Boots the mcpkit-Go fixture (`main.go` in this folder) and opens
[MCPJam Inspector](https://github.com/MCPJam/inspector) so you can poke
at the protocol surface:

```bash
make demo-app EXAMPLE=cohort-heatmap-server
```

Paste `http://localhost:3101/mcp` into MCPJam's server list and connect.
Then browse `tools/list`, `_meta.ui`, and tool-call payloads on the wire.

### [Optional] You can also do…

- **See the App rendered in basic-host.** Same Go fixture, but driven by
  basic-host (the canonical reference UI) instead of MCPJam. Opens a
  browser at `http://localhost:8080`:

  ```bash
  RENDERER=basic-host make demo-app EXAMPLE=cohort-heatmap-server
  ```

- **Hit upstream's TS reference server instead.** Useful for comparing
  the Go fixture's wire surface against the canonical implementation:

  ```bash
  make demo-upstream EXAMPLE=cohort-heatmap-server
  ```

  Add `RENDERER=basic-host` to render the upstream TS in basic-host
  instead of MCPJam.

- **Strict parity check against the mcpkit-Go fixture.** Runs upstream's
  Playwright suite against the Go binary — wire-level `tools/list` diff
  + visual PNG gate. Requires Docker:

  ```bash
  EXAMPLE=cohort-heatmap-server make test-apps-playwright-docker
  ```

## Prompts to try

Connect to `Cohort Heatmap Server`, then paste any of these:

```
Show me a cohort retention heatmap.
```

![Cohort Heatmap App: iframe shows the full retention matrix with cohorts on one axis, time-period columns on the other, color-graded retention percentages](screenshots/01-default-heatmap.png)

```
What's my user retention by signup month?
```

```
Display the cohort analysis dashboard.
```

```
How is retention looking month-over-month for the last six cohorts?
```

![Cohort Heatmap iframe filtered to the last six cohorts; the color gradient makes the retention dropoff visible at a glance](screenshots/02-recent-cohorts.png)

The model calls `get-cohort-data`; the iframe renders the heatmap
with cohorts on one axis, time periods on the other.

### Direct tool call (no LLM needed)

| What | How | What you should see |
|---|---|---|
| Smoke test | Select `get-cohort-data`, call with empty input | Tool result: nested cohort × period retention data in `structuredContent` |
| Iframe renders the heatmap | Same call, scroll up | Color-graded matrix in the App iframe |

## What to look at next

- [`customer-segmentation`](../customer-segmentation/README.md) —
  rung-4 sibling, different analytical shape.
- [`budget-allocator`](../budget-allocator/README.md) /
  [`scenario-modeler`](../scenario-modeler/README.md) — other
  rung-4 fixtures.
