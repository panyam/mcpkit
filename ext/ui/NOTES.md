# ext/ui — implementation notes

MCP Apps. For the design see `docs/APPS_DESIGN.md`, `docs/APPS_HOST.md`, and
`docs/APPS_ONBOARDING.md`; for the bridge trace relay see `docs/SEP_414_OTEL.md` § Apps Bridge
trace context relay.

---

## Lifecycle

**`Client.Connect()` before `AppHost.Start()`.** `AppHost.Close()` only closes the bridge — it does
not close the client.

---

## CORS for browser clients

MCP servers serving browser apps need `Mcp-Session-Id` in **both** `Access-Control-Allow-Headers`
and `Access-Control-Expose-Headers`, plus `DELETE` in the allowed methods. Missing the Expose half
is the common failure: the request succeeds and the session id is invisible to JS.

Use `servicekit/middleware.CORS()` with options.

---

## apps/compat Playwright baselines are Docker-pinned to Linux

`make test-apps-playwright` runs upstream's `ext-apps` Playwright suite against a mcpkit-Go drop-in
under `examples/apps/compat/<name>/`.

**One canonical baseline per fixture, no platform suffix**, pinned to
`mcr.microsoft.com/playwright:v1.57.0-noble` — the same image upstream uses for `test:e2e:docker`.
Regenerate with `make test-apps-playwright-docker` (`DOCKER=1`).

Native mode is for fast local `loads app UI` iteration. **The `screenshot matches golden` test will
fail against the Linux baseline on a macOS or Windows host.** That is intentional; use `DOCKER=1`
for the visual gate.

DOCKER mode also runs a **strict** `tools/list` parity check against upstream's TypeScript
reference server on a side port. Any divergence fails the build. The diff filters `$schema`
(different SDKs emit different draft URLs) and `additionalProperties` (mcpkit's permissive default
per `core/schema.go`); everything else is enforced.

Baselines are per-fixture committed PNGs rather than upstream's tree, because basic-host renders
one dropdown entry per server and compat runs spin up 1 server versus upstream CI's 25.

Wrapper env vars (`HARNESS_PORT`, `SANDBOX_PORT`, `FIXTURE_PORT`, `UPSTREAM_PORT`, `EXT_APPS_DIR`,
`DOCKER`, `SKIP_DRIFT_CHECK`) and the drop-in pattern are documented in
`examples/apps/compat/README.md`.

**Port note**: apps/compat Playwright fixtures own host ports 8080 and 3101. Anything else that
wants a demo port must avoid them — this is why the whole-enchilada stack moved to 9090.

---

## Bridge trace relay: the test gotcha

Cross-wire trace tests **must** install `server.WithTracerProvider(...)` on the inner server. The
server-side middleware is what extracts `_meta.traceparent` off the wire into handler ctx. Without
it, `TraceContextFromContext` in the handler returns zero even when the wire genuinely carried a
traceparent, and the test fails for a reason that has nothing to do with the bridge.

---

## Open items

- **`ctx.Elicit` / `ctx.Sample` handlers need migrating to MRTR** for stateless-wire support
  (#835). They are forbidden on the stateless wire by construction.
- **ext-apps v1.7.0 bridge JS feature coverage** is tracked in #772: `createSamplingMessage`,
  handshake guards, `allowUnsafeEval`.
