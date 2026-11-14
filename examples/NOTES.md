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

Two CI jobs cover examples (#1431). Each runs beside the others, because together they outran
every other job in `test.yml`:

- `examples-build` runs `make check-examples-build`. Every module builds and passes `go vet`. Vet
  type-checks `_test.go` files too, and `go build` skips them. A stale `buildServer` call site in
  kitchen-sink's tests passed the build-only check for two merged PRs.
- `examples-test` runs `make check-example-tests-listed`, then `make test-examples`.

`check-example-tests-listed` fails in three cases. A module has tests and neither the umbrella nor
a `test.yml` `working-directory` runs them. An umbrella target points at a module with no tests.
The Makefile and justfile umbrellas name different modules. `tasks-v2` hit the second case: its
tests left with the agent SDK, and `test-tasks-v2` kept passing on "no test files".
`apps/any-frontend` and `rooms/lanternfall` run in their own jobs, since those need Chromium.

The discord and telegram scripts still live in `experimental/scripts/`. Moving them under
`examples/scripts/` is left from #688.

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
- **No test runs a walkthrough's `Run` steps, so run the demo after changing library behaviour.**
  A discord step subscribed to a dead `http://localhost:1/sink` and expected success; endpoint
  verification broke it and every test stayed green (#1444). Run it non-interactively against a
  server on a free port: `go run . --serve -addr 127.0.0.1:18411 &` then
  `go run . --non-interactive --url http://127.0.0.1:18411`, and grep the output for `UNEXPECTED`.
  If something else holds port 8080, every call returns 405, which looks like a demo bug.
- **`common.SetupRenderer(demo)` is mandatory for `--tui`.** demokit ships no renderer by default,
  so without the call the binary silently falls back to PlainRenderer regardless of `--tui` or
  `--mode=tui`.
- Standard flag wiring uses `demokit.FilterArgs(os.Args[1:], demokit.ValueFlag("--url"), ...)` with
  the binary's own value flags declared as extras so they survive the filter. Declare your own
  flags **first**, then call `flag.CommandLine.Parse` **once** on the filtered list.

### Generated docs have two prose sources, and one of them is not this repo

`WALKTHROUGH.md` is `go run . --doc md` output. Editing it directly is reverted by the next
`just readme` and shows up as drift in `/example-audit`, so the words have to be changed where
they actually live:

1. **The example's own Go strings** - `demokit.New(...)`, `.Description(...)`, `demo.Step(...)`,
   `Note(...)`, `Arrow(...)`, `Section(...)` in `walkthrough.go` / `main.go`.
2. **demokit's renderer** - `Demo.Markdown()` in `github.com/panyam/demokit`. Anything it formats
   around your strings is not yours to change here.

A docs-wide prose pass found this the hard way in 2026-09. Purging em-dashes from the published
docs left 99 behind across twelve walkthroughs, all from one line in `demokit/markdown.go` that
wrote the "What you'll learn" teaser as `- **%s** — %s`. No amount of editing this repo could
remove them; it took demokit v0.0.32 and a bump. Budget for that when a convention has to hold
across generated output.

**`examples/host/` is the exception and inverts the pair.** `01-apphost` and `02-multi-server`
generate `README.md` (`go run ./<dir>/ --readme > README.md`) and hand-write `WALKTHROUGH.md`.
Everywhere else it is the other way round. Check the `readme` recipe before editing either file.

**Regeneration must be deterministic or the file diffs against itself.** `examples/host/01-apphost`
flapped for a while because `InProcessAppBridge` ranged a map to answer `tools/list`; two `make
readme` runs produced two different READMEs. Nothing gated it, so it read as unrelated churn in
whichever PR regenerated next. See `ext/ui/NOTES.md`.

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

---

## disclosure: interop with the WG's TypeScript prototype

`examples/disclosure` and `experimental/ext/groups` were checked against
[progressive-disclosure-wg PR 20](https://github.com/modelcontextprotocol/progressive-disclosure-wg/pull/20)
at `9554415c` (2026-11-13). Both directions agree on negotiation, `groups/list`, `-32002` for an
unknown group, and calling a grouped tool without fetching its group. Two differences remain, and
both come from PR 20 predating the 2026-11-07 draft:

- PR 20 nests `groups/get` results under `contents`. The draft dropped the wrapper, and so does
  mcpkit, so each side's client finds no tools in the other side's result.
- PR 20's server returns the same `tools/list` to every caller. The draft says a plain client
  should get the flat list, and mcpkit's does.

To rerun it, check PR 20 out next to this repo and build `sdk/typescript`, then
`examples/groups-server/typescript` and `examples/groups-client/typescript`, with `npm install &&
npm run build` in each.

- **Their client, our server.** Serve the fixture with `go run . --serve --addr=127.0.0.1:18425`,
  then connect their `GroupsClient` over `StreamableHTTPClientTransport`, once with
  `capabilities.extensions["io.modelcontextprotocol/groups"]` and once without. Put the script
  inside their client example, so `@modelcontextprotocol/ext-grouping` resolves.
- **Our client, their server.** Their server speaks stdio. Pass
  `client.WithCommandTransport("node", []string{"<pr20>/examples/groups-server/typescript/dist/index.js"})`
  and `groups.ClientOption()` to `client.NewClient`, then call `groups.NewClient(c).List` and `.Get`.
