# Apps Compat — mcpkit ⇄ ext-apps

mcpkit-Go drop-in replacements for upstream's
[`modelcontextprotocol/ext-apps`](https://github.com/modelcontextprotocol/ext-apps)
Playwright suite. Each fixture under `examples/apps/compat/<name>/`
exposes the same MCP tool surface as the upstream TypeScript example,
serves upstream's verbatim `dist/mcp-app.html`, and is verified
end-to-end by upstream's own Playwright tests run against the Go
binary via `basic-host`.

## Coverage

| Metric | Count |
|---|---|
| Total upstream examples | 25 |
| Drift-strict parity (OK) | 21 |
| In progress (WIP / PROT) | 0 |
| Skipped by upstream's test set | 4 |
| Not yet implemented | 0 |
| Pass rate over implemented | 21 / 21 (100%) |

## Per-fixture status

| # | Fixture | Status | Detail |
|---|---|---|---|
| 1 | basic-server-vanillajs | ✅ OK | 2/2 pass against mcpkit-local baseline. Resolution: PR 534 |
| 2 | basic-server-preact | ✅ OK | 2/2 pass + drift strict OK. PR 540 |
| 3 | basic-server-react | ✅ OK | 2/2 pass + drift strict OK. PR 540 |
| 4 | basic-server-solid | ✅ OK | 2/2 pass + drift strict OK. PR 540 |
| 5 | basic-server-svelte | ✅ OK | 2/2 pass + drift strict OK. PR 540 |
| 6 | basic-server-vue | ✅ OK | 2/2 pass + drift strict OK. PR 540 |
| 7 | budget-allocator-server | ✅ OK | 2/2 pass + drift strict OK. Rich nested output (config + analytics with maps); standard struct tags + Go maps reflect cleanly. PR 553 |
| 8 | cohort-heatmap-server | ✅ OK | 2/2 pass + drift strict OK. PR 549 (float64 numerics workaround for int-vs-number gap, see 548) |
| 9 | customer-segmentation-server | ✅ OK | 2/2 pass + drift strict OK. PR 549 |
| 10 | debug-server | ✅ OK | 2/2 pass + drift strict OK. 3 tools (debug-tool/refresh/log). PR 549 (uses InputSchemaOverride for the `payload: any` field — gap 2 in issue 548) |
| 11 | integration-server | ✅ OK | 5/5 pass (2 standard + 3 interaction: Send Message, Send Log, Open Link). Drift strict OK. PR 549 |
| 12 | lazy-auth-server | ⏭ SKIP | Not in upstream's servers.spec.ts. Auth-flow specific; no default Playwright test |
| 13 | map-server | ✅ OK | 2/2 pass + drift strict OK. 2 tools (show-map + geocode). PR 552 |
| 14 | pdf-server | ✅ OK | 2/2 pass + drift strict OK (4 tools — list_pdfs / read_pdf_bytes / display_pdf / save_pdf, matching upstream's default-without-`--enable-interact` surface). The 4 additional pdf-*.spec.ts files (annotations, annotations-api, incremental-load, viewer-zoom) drive the `--enable-interact` surface and need the interact command-queue + long-poll backend; deferred to follow-up issue 554. PR 555 |
| 15 | qr-server | ⏭ SKIP | Upstream SKIP_SERVERS |
| 16 | quickstart | ✅ OK | 2/2 pass + drift strict OK. PR 543 (tsx-fallback for upstream servers without dist/index.js) |
| 17 | say-server | ⏭ SKIP | Upstream SKIP_SERVERS (HuggingFace TTS download) |
| 18 | scenario-modeler-server | ✅ OK | 2/2 pass + drift strict OK via OutputSchemaOverride (nullable `breakEvenMonth`). PR 553 |
| 19 | shadertoy-server | ✅ OK | 2/2 pass + drift strict OK via InputSchemaOverride (multi-line GLSL default). PR 552 |
| 20 | sheet-music-server | ✅ OK | 2/2 pass + drift strict OK via TypedAppToolConfig.InputSchemaOverride. PR 545 (closes 542) |
| 21 | system-monitor-server | ✅ OK | 2/2 pass + drift strict OK. 2 tools (info + app-only stats). PR 549 |
| 22 | threejs-server | ✅ OK | 2/2 pass + drift strict OK via InputSchemaOverride (multi-line code default). 2 tools (show_threejs_scene + learn_threejs). PR 552 |
| 23 | transcript-server | ✅ OK | 2/2 pass + drift strict OK. PR 543 |
| 24 | video-resource-server | ⏭ SKIP | Not in upstream's servers.spec.ts; only in generate-grid-screenshots.spec.ts which is excluded from default test runs. No Playwright test to satisfy. |
| 25 | wiki-explorer-server | ✅ OK | 2/2 pass + drift strict OK via new OutputSchemaOverride (nullable `error` field — PR 552 adds the library symbol) |

## Legend

- **✅ OK** — `loads app UI` + `screenshot matches golden` Playwright
  tests pass, and the `tools/list` parity check matches upstream's TS
  server byte-for-byte under DOCKER mode
  (`mcr.microsoft.com/playwright:v1.57.0-noble`, the same image upstream
  uses for `test:e2e:docker`).
- **🟡 WIP / PROT** — surface working; visual baseline or interaction
  tests pending.
- **⏭ SKIP** — upstream's `servers.spec.ts` deliberately omits this
  example (special build-time dependency or not in the default test
  matrix).
- **⬜ NOT** — not yet implemented as an mcpkit drop-in.

## How parity is verified

Each fixture runs through `scripts/apps-playwright-test.sh` in two
modes:

1. **Native** (host OS) — fast `loads app UI` iteration. The visual
   screenshot test is Docker-pinned and is expected to fail on
   non-Linux hosts; that gap is intentional.
2. **DOCKER** — runs the upstream Playwright image with the fixture as
   the MCP server. The committed baseline is a single canonical
   Linux PNG per fixture (matching upstream's own pinning convention).
   This mode also runs a **strict `tools/list` parity check** that
   spins up upstream's TypeScript reference server on a side port,
   fetches `tools/list` from both, JSON-diffs them, and fails on any
   schema drift outside a small filter for SDK-emit differences
   (`$schema`, `additionalProperties`, `propertyNames`, plus an
   `integer` ⇄ `number` subtype normalization). Drift fails the gate.

## How this report is generated

`tools/compat-reports/src/apps.ts` fetches the body of umbrella
tracking issue `panyam/mcpkit#533` and renders its
status table as this Markdown file. The umbrella issue is the
hand-maintained source of truth — every fixture PR updates the row
along with the code. The script is deterministic: re-running on an
unchanged umbrella body produces a byte-identical file.

To refresh:

```sh
make refresh-apps-compat-report
```

The CI `check-apps-compat-stale` gate enforces that PRs touching
`examples/apps/compat/**` re-run the refresh and commit any diff.

## See also

- [Conformance Coverage](../../CONFORMANCE.md) — MCP spec compliance matrix
- [Upstream Conformance Audit](../UPSTREAM_AUDIT.md) — scenario-level grading vs upstream's conformance suite
- [Fixture Parity Audit](../FIXTURE_AUDIT.md) — public-API discipline check on conformance fixtures
- `examples/apps/compat/README.md` — drop-in fixture pattern + the wrapper script
