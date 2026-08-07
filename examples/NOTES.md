# examples/ — implementation notes

Conventions live in `CONVENTIONS.md`; the catalog is `README.md`; dual-wire coverage is
`DUAL_MODE_AUDIT.md`. This file is the wiring lore.

Agent examples (`examples/agents/`, including kitchen-sink) are covered in `agent/NOTES.md`
§ Examples.

---

## Test orchestration

`examples/Makefile` plus `examples/justfile` mirror `conformance/`'s pattern: one infra-free `test`
umbrella plus per-example recipes. The repo root `make test-examples` delegates here.

The `events/discord` and `events/telegram` suites **moved out of `experimental/`** — they test
example apps, not the library. `experimental/Makefile` and `justfile` keep the old target names as
**aliases delegating here**, so testall's per-suite timing and CI scripts keep working.

**The fragile part**: the CI `test-agent` job hardcodes example test steps in
`.github/workflows/test.yml` rather than calling `make test-agent`. Moving or adding an example
needs the workflow updated too. Folding that into the orchestrator is the remaining #688 work,
along with physically moving the discord/telegram scripts under `examples/scripts/`.

---

## Telemetry wiring

Every example exposes a uniform `--exporter` / `--otlp-endpoint` pair via
`common.RegisterTelemetryFlags(flag.CommandLine)` for serve paths, or `common.ExporterFromArgs()`
for walkthroughs (which mirrors `common.ServerURL`'s `os.Args` scan, so no `flag.Parse` is needed).

Helpers: `commonotel.SetupTelemetry(ctx, opts...)` for servers,
`commonotel.SetupClientTelemetry(ctx, opts...)` for walkthroughs (presets the client
instrumentation library name).

Four `EXPORTER` values:

| Value | Behavior |
|---|---|
| `""` (default) | Noop. Zero overhead, no spans. |
| `stdout` | stdouttrace |
| `otlp` | otlptracegrpc with a 500ms TCP probe; dial failure → Noop **plus a warning log** |
| `auto` | same probe, **silent** Noop fallback — the operator opted into maybe-on-maybe-off |

**The probe is what delivers the contract.** `otlptracegrpc.New` is lazy and returns non-nil even
against a refused endpoint, so without the probe "dial failure → Noop" would not actually happen.

`examples/otel/stdout/` is the documented carve-out, with `defaultExporter="stdout"` because
showing spans is the demo's whole point.

**Service-name convention**: `<example-name>` for the server, `<example-name>-host` for the client,
so Grafana's service filter distinguishes the two halves of a stitched trace. Threading point:
`common.ServerConfig.TracerProvider` plus `client.WithTracerProvider(tp)` on every real
`NewClient`.

Note that `examples/host/01-apphost` and `02-multi-server` do **not** use `common.RunServer`, so
sweeps over the common path miss them.

---

## demokit

- **Non-interactive mode cannot do browser steps.** A step that opens a browser and expects user
  action will fail under `--non-interactive`. Interactive mode is the primary path.
- **`common.SetupRenderer(demo)` is mandatory for `--tui`.** demokit ships no renderer by default,
  so without the call the binary silently falls back to PlainRenderer regardless of `--tui` or
  `--mode=tui`.
- Standard flag wiring uses `demokit.FilterArgs(os.Args[1:], demokit.ValueFlag("--url"), ...)` with
  the binary's own value flags declared as extras so they survive the filter. Declare your own
  flags **first**, then call `flag.CommandLine.Parse` **once** on the filtered list.

---

## whole-enchilada stage 2 (`examples/whole-enchilada/events/`)

A constellation of gotchas that bite together on the operator's first `make up`.

- **oneauth ≥ v0.1.19 required.** See `ext/auth/NOTES.md` for what each version fixed.
- **Keycloak 26 needs `KC_HOSTNAME` plus `KC_HOSTNAME_STRICT=false`.**
  `HostnameV2Provider.getFrontUriBuilder` calls `request.authority()`, which is null on HTTP/1.0
  healthchecks, so every probe NPEs, the container never goes healthy, and
  `depends_on: keycloak: service_healthy` gates everything else forever. The compose template sets
  both envs and probes with an explicit `Host: localhost` plus `Connection: close`.
- **nginx `default_server` must proxy, not `return 444`.** The walkthrough hits `localhost:9090`
  with no Host-header rewrite, so `return 444` closes the connection (curl exit 52, Go EOF). The
  default server now `proxy_pass`es to `http://event_server_pool`; the `*.whole_enchilada` aliases
  remain for the `make hosts-install` flow.
- **Port 9090, not 8080** — apps/compat Playwright fixtures own 8080 and 3101. Stack ports: 9090
  nginx (MCP/SSE), 8180 Keycloak, 3000 Grafana, 3100 Loki, 3200 Tempo, 4317/4318/8888 OTel
  collector, 9009 Mimir.
- **A shared `mcpkit` docker network bridges three compose files.** `docker/observability`,
  `docker/backends`, and this stack each declare `networks: mcpkit: { name: mcpkit, driver:
  bridge }`. The literal `name: mcpkit` with no project prefix is what makes cross-stack lookups
  resolve. Whichever stack starts first creates it.
- **`EXPORTER` in compose env does not reach `common.RegisterTelemetryFlags`.** The flag default is
  `""` (Noop). The compose `command:` **must** pass `--exporter=$EXPORTER` and
  `--otlp-endpoint=$OTEL_EXPORTER_OTLP_ENDPOINT` explicitly; setting only `environment:` does
  nothing.
- **Six-token convention**: `TOKEN_POLLER_TENANT_{A,B,C}` plus `TOKEN_WEBHOOK_TENANT_{A,B,C}`.
  Acquired per window via browser auth or ROPC. Realm JSONs seed `alice`/`bob`/`carol` plus
  `user{a,b,c}{1..5}` with passwords equal to usernames.
- **Recipe lines that emit values need an `@` prefix.** Without it,
  `$(TENANT=A just newtoken)` captures the runner's command echo *plus* the token. Usage-error
  checks redirect to `>&2` so a bad invocation does not poison the capture either.
- **The stage-2 walkthrough is pure narrative — it makes no MCP calls.** Stage-2 auth requires
  every method to carry a real bearer token, and having the walkthrough auto-acquire one would
  duplicate what the operator's own windows already do. The walkthrough orchestrates prose between
  actions; the binaries are the actual MCP clients. Both `demo` and `test` therefore work without
  tokens.
