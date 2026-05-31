# @mcpkit/conformance-report

Renders [`CONFORMANCE.md`](../../CONFORMANCE.md) at the repo root from two upstream artifacts:

- `tier-check --output json` (upstream `modelcontextprotocol/conformance` CLI) — per-scenario pass/fail
- `src/seps/traceability.json` (committed in the upstream conformance clone) — maps SEP → declared requirements → check IDs

Output: one Markdown file with a hand-edited `## Overview` block (preserved across regenerations) + a generated per-SEP coverage matrix + generated open-gaps section.

## CLI

```
conformance-report \
  --scorecard <path/to/tier-check.json> \
  --traceability <path/to/traceability.json> \
  --known-gaps <path/to/known-gaps.yaml> \
  --out <path/to/CONFORMANCE.md>
```

All flags are required. The tool re-reads the existing `--out` file (if present) to preserve hand-edited content between the file header and the `<!-- begin:generated -->` marker.

## Why a separate package

The renderer is small (~200 lines) but ports cleanly into upstream `tier-check --format markdown` once interest exists (issue #498 Phase 3). Living in-tree under `tools/` while we iterate matches the issue's Phase 1 decision; the cost of moving it later is a `git mv` + bin pointer update.

## Determinism

The output is intentionally free of wall-clock timestamps. The header stamps:

- Upstream conformance commit SHA (so an upstream-driven check-ID change diffs)
- MCP protocol version

The mcpkit commit SHA is **not** stamped — including it would diff the file on every commit even when no scenarios changed, defeating the CI staleness gate. `git blame CONFORMANCE.md` gives the same provenance.

## Tests

```
npm install
npm test
```

Tests are fixture-driven: golden `expected.md` + recorded scorecard/traceability inputs in `test/fixtures/`.
