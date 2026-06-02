# apps/compat — mcpkit-Go drop-ins for upstream ext-apps parity testing

Each subdirectory here is a mcpkit-Go MCP server that mimics one of
[`modelcontextprotocol/ext-apps`](https://github.com/modelcontextprotocol/ext-apps)'s
TypeScript example servers byte-for-byte at the protocol surface. We run
upstream's own Playwright suite against the Go binary to validate that mcpkit
hosts can drive any client that targets the upstream examples.

Tracked under issue 533 (umbrella) and the per-example issues it links to.

## Drop-in shape

A compat fixture must match its upstream counterpart on three things:

1. **Tool name + input schema + output schema.** The host's Playwright tests
   call the tool by name and assert against the response shape.
2. **Resource URI exposing the UI.** Upstream picks `ui://<tool-name>/mcp-app.html`;
   mirror it exactly so the host renders the iframe at the URL it expects.
3. **HTML body served verbatim from upstream's `dist/mcp-app.html`.** Read it
   from `$EXT_APPS_DIR` at startup; do not vendor or modify it. The fixture's
   only job is to wire mcpkit's protocol surface to the same iframe payload
   upstream's server would have served.

CORS is the only host-environment-specific concern: basic-host runs on port
8080, the fixture runs on 3101, so the browser needs `Mcp-Session-Id` exposed.
`examples/apps/compat/basic-vanillajs/main.go` shows the minimal wrapper.

Anything not on this list (logging, framework choice, transport flavor) is
free. The whole point is that `basic-host` cannot tell the fixture apart from
upstream's TS server at the wire level.

## Adding a fixture for a new upstream example

1. Create `examples/apps/compat/<name>/` with `go.mod`, `main.go`, and the
   matching tool / resource registration. Copy the structure of
   `basic-vanillajs/main.go`.
2. Add a `case` arm in `scripts/apps-playwright-test.sh` mapping the upstream
   `EXAMPLE` value to your `FIXTURE_DIR` and a `GREP_PATTERN` that scopes
   Playwright to your example's `test.describe` block.
3. Generate the baseline:
   ```bash
   UPDATE_SNAPSHOTS=1 EXAMPLE=<name> make test-apps-playwright
   ```
   This writes `examples/apps/compat/<fixture>/__snapshots__/<key>.png`.
4. Verify a clean run passes:
   ```bash
   EXAMPLE=<name> make test-apps-playwright
   ```
5. Commit the fixture, the script arm, and the baseline PNG.

## Snapshot baseline platform

`__snapshots__/*.png` files are committed per-fixture and are
**platform-specific**. Chromium's font fallback differs between Linux (Docker)
and macOS, producing ~5–10px layout shifts that exceed
`maxDiffPixelRatio: 0.06`. Regenerate on the OS that CI will use for the
comparison run; otherwise comparison runs on a different OS will fail visual
checks even though the protocol surface is correct.

Upstream documents the same constraint and pins their own snapshots to a
Linux Docker image (`mcr.microsoft.com/playwright:v1.57.0-noble`). We currently
pin our baseline to the platform of whoever ran `UPDATE_SNAPSHOTS=1` last; the
make target is not yet wired into CI. When it is, regenerate the baselines
inside the CI image and re-commit.

## Status legend

The umbrella issue tracks per-example status: `NOT` (not implemented),
`WIP` (in progress), `PROT` (protocol passes, visual diff outstanding),
`OK` (all-pass), `SKIP` (upstream marks as skipped for special-resource
reasons such as GPU or large model downloads).
