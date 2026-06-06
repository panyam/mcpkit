# scenario-modeler — nullable field at depth, needs OutputSchemaOverride

Rung 4 on the [examples ladder](../README.md#reading-order--examples-ladder).
One tool, but the output's deeply nested templates contain a nullable
`breakEvenMonth` field at two different nesting depths. First fixture
where even the patch builder can't shrink to a one-liner — keeping
the full `OutputSchemaOverride` is genuinely cleaner.

## What it Shows

- **SaaS scenario projections.** `get-scenario-data` returns a set of
  pre-built scenario templates (conservative growth, aggressive, etc.)
  plus optional custom projections computed from caller-supplied
  inputs. Templates and custom projections both produce a `summary`
  object that has `breakEvenMonth: number | null`.
- **Nullable at depth.** The nullable field appears as
  `templates[].summary.breakEvenMonth` AND as
  `customSummary.breakEvenMonth` — same shape, two nesting paths.
  Hand-stitching that with `Patch.Replace` at deep paths is uglier
  than the explicit override, so the override stays for the output.
- **`InputSchemaPatch` for the input.** The input's `customInputs`
  field just needs a description tweak — patch is shorter than the
  old override there.

## Or Run Live

### Start Server

```bash
make demo-app EXAMPLE=scenario-modeler
```

Starts the mcpkit-Go fixture on `http://localhost:3101/mcp` and basic-host on `http://localhost:8080`. (Pass `OPEN=1` to auto-open the browser.)

## Try It Out on basic-host

Open <http://localhost:8080> in your browser. Then:

1. Pick **SaaS Scenario Modeler** from the server dropdown.
2. Pick **get-scenario-data** from the tool dropdown, click **Call Tool**.
3. The iframe renders the result; interact with it directly to drive subsequent tool calls (no model in the loop).

<a href="screenshots/01-templates.png" target="_blank"><img src="screenshots/01-templates.png" alt="Scenario Modeler App: iframe shows the pre-built scenario templates panel (conservative / aggressive / etc.) with summary KPIs" width="50%"></a>

## Try It Out from a Host

Connect to `http://localhost:3101/mcp` from your favorite MCP host — VS Code, Claude Desktop, [MCPJam Inspector](https://github.com/MCPJam/inspector), or any spec-compliant client.

**Prompts to try** (LLM-driven hosts):

> "Show me SaaS scenario projections."
> "Model an aggressive growth scenario for my startup."
> "What if my SaaS company starts at $50k MRR with 10% monthly growth and 3% churn? When do I break even at $20k fixed costs?"
> "Project my revenue with starting MRR $100k, 5% growth, 2% churn, 80% gross margin, $50k fixed costs."

The model calls `get-scenario-data` (with `customInputs` for the
parameterized prompts); the iframe renders the projection charts +
template comparison.

**Verify the wire shape** (no LLM needed):

| What | How | What you should see |
|---|---|---|
| Just the templates | Select `get-scenario-data`, call with empty input | Tool result has `templates[]` populated, `customProjections` / `customSummary` omitted |
| With custom inputs | Call with `{"customInputs": {"startingMRR": 100000, "monthlyGrowthRate": 0.05, "monthlyChurnRate": 0.02, "grossMargin": 0.8, "fixedCosts": 50000}}` | Result has `customProjections` and `customSummary` populated. `customSummary.breakEvenMonth` is a number or `null`. |
| Verify nullable on the wire | Expand `outputSchema.properties.customSummary.properties.breakEvenMonth` | `{"anyOf": [{"type":"number"}, {"type":"null"}]}` — the nullable form. |

See [Other ways to test a fixture](../README.md#other-ways-to-test-a-fixture) in the compat README for wire inspection, upstream comparison, the strict Playwright gate, and connecting from VS Code / Claude Desktop / other MCP hosts.

## What to Try Next

- [`wiki-explorer`](../wiki-explorer/README.md) — also has a nullable
  field, but at the top level (one path, one Replace call — patch
  fits cleanly there).
- [`budget-allocator`](../budget-allocator/README.md) — rung-4
  sibling without the nullable; reflection alone handles it.
