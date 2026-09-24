# ext/ui — implementation notes

MCP Apps. For the design see `docs/APPS_DESIGN.md`, `docs/APPS_HOST.md`, and
`docs/APPS_ONBOARDING.md`; for the bridge trace relay see `docs/SEP_414_OTEL.md` § Apps Bridge
trace context relay.

---

## Lifecycle

**`Client.Connect()` before `AppHost.Start()`.** `AppHost.Close()` only closes the bridge, and it does
not close the client.

---

## CORS for browser clients

MCP servers serving browser apps need `Mcp-Session-Id` in **both** `Access-Control-Allow-Headers`
and `Access-Control-Expose-Headers`, plus `DELETE` in the allowed methods. Missing the Expose half
is the common failure, and a fairly quiet one: the request succeeds and the session id is invisible to JS.

Use `servicekit/middleware.CORS()` with options.

---

## apps/compat Playwright baselines are Docker-pinned to Linux

`make test-apps-playwright` runs upstream's `ext-apps` Playwright suite against a mcpkit-Go drop-in
under `examples/apps/compat/<name>/`.

**One canonical baseline per fixture, no platform suffix**, pinned to
`mcr.microsoft.com/playwright:v1.57.0-noble`, the same image upstream uses for `test:e2e:docker`.
Regenerate with `make test-apps-playwright-docker` (`DOCKER=1`).

Native mode is for fast local `loads app UI` iteration. **The `screenshot matches golden` test will
fail against the Linux baseline on a macOS or Windows host.** That is intentional; use `DOCKER=1`
for the visual gate.

DOCKER mode also runs a **strict** `tools/list` parity check against upstream's TypeScript
reference server on a side port. Any divergence fails the build. The diff filters `$schema`
(different SDKs emit different draft URLs) and `additionalProperties` (mcpkit's permissive default
per `core/schema.go`); everything else is enforced.

Baselines are per-fixture committed PNGs rather than upstream's tree, mostly because basic-host renders
one dropdown entry per server and compat runs spin up 1 server versus upstream CI's 25.

Wrapper env vars (`HARNESS_PORT`, `SANDBOX_PORT`, `FIXTURE_PORT`, `UPSTREAM_PORT`, `EXT_APPS_DIR`,
`DOCKER`, `SKIP_DRIFT_CHECK`) and the drop-in pattern are documented in
`examples/apps/compat/README.md`.

**Port note**: apps/compat Playwright fixtures own host ports 8080 and 3101. Anything else that
wants a demo port must avoid them, which is why the whole-enchilada stack moved to 9090.

---

## Bridge trace relay: the test gotcha

Cross-wire trace tests **must** install `server.WithTracerProvider(...)` on the inner server. The
server-side middleware is what extracts `_meta.traceparent` off the wire into handler ctx. Without
it, `TraceContextFromContext` in the handler returns zero even when the wire genuinely carried a
traceparent, and the test fails for a reason that has nothing to do with the bridge.

---

## Map order reaches generated docs

`InProcessAppBridge.handleToolsList` answers `tools/list` from `b.tools`, a map, and
`AppHost.ListAllTools` preserves whatever the bridge returns. That is deliberate on the host side:
a real iframe app's tool order is the app's own and may be meaningful, so the host does not sort
it. But it meant the in-process bridge handed back a different order every run.

That reached disk, because `examples/host/01-apphost` regenerates its `README.md` from a live run.
`make readme` produced a different file each time, and since nothing gates generated example docs
it surfaced as unexplained churn in whichever PR regenerated next (#1405 was the one that noticed).

Fixed in #1408 by sorting in the bridge, matching `ServerRegistry.AllTools`, which already sorts
its own aggregation for the same reason. `TestBridge_Send_ToolsList_Deterministic` pins it with
four tools registered in neither sorted nor reverse-sorted order, asserted over 10 iterations, so
a pass-through implementation cannot coincidentally satisfy it.

The general rule: anything an example renders into a committed file has to be deterministic. A map
range that is invisible in a test is not invisible in a regenerated document.

---

## Spec conformance: the tests agreed with the bug

A 2026-09-24 diff of ext-apps v2.0.1 against this package (kept in mcpcontrib at
`proposals/mcp-apps-wg/ext-apps-v2.0.1-coverage.md`) found the bridge JS sending `ui/*` messages in
shapes the spec does not define: `updateModelContext` as `{context}`, `downloadFile` as
`{url, filename}`, host capabilities read from `result.capabilities` instead of `hostCapabilities`,
`ui/resource-teardown` handled as a notification when it is a request. Every bridge test passed,
because the fixtures in `mcp-app-bridge.test.ts` were written to match the bridge rather than the
spec.

The same blind spot hid a silent failure between our own halves. The Go host handlers decode the
spec shape, the bridge sends the wrong one, and the pair yields an empty request with no error.
Each side's unit tests pass on their own.

Two rules follow. Test fixtures come from upstream's `src/spec.types.ts`, never from what our code
emits. And a feature that spans the bridge and `AppHost` needs a pair test that drives the real
bridge JS into the Go host.

The review also found that `AppHost` cannot host a stock ext-apps View at all: it forwards
`ui/initialize` to the MCP server, and `AppBridge` has no host→View notify path. Tracked in #1454.

---

## Open items

- **`ctx.Elicit` / `ctx.Sample` handlers need migrating to MRTR** for stateless-wire support
  (#835). They are forbidden on the stateless wire by construction. SEP-3118 (app-rendered
  elicitations) depends on this path.
- **Spec conformance against ext-apps v2.0.1**, from the review above: bridge wire shapes (#1452),
  `AppHost` answering `ui/*` host requests (#1456), View lifecycle (#1454), proxy policy for
  visibility, errors and cancellation (#1453), resource metadata emitted on the tool instead of the
  resource (#1455). #772, the v1.7.0 tracker, is closed.
- **`ClientSupportsUI` is false on the stateless wire** even when the request declares the
  extension (#1458), so Apps servers fall back to plain output for every stateless client.
