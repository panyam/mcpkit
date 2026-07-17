# Examples — Conventions

Single source of truth for how an mcpkit example is laid out. Read this before
adding a new example, auditing an existing one, or upgrading an older one to
the current standard.

The three skills that operate on examples — `/example-new`, `/example-audit`,
`/example-upgrade` — all enforce this document. If you change something here,
re-run `/example-audit` across `examples/` to find drift.

Reference examples (the canon):

- `examples/file-inputs/` — non-UI, scripted walkthrough, static fixtures.
- `examples/events/discord/` — non-UI, scripted walkthrough, live event injection.
- `examples/apps/vanilla/` — UI / MCP Apps (host-driven, no scripted walkthrough).

**Documented exception — `examples/getting-started/`.** This is the minimal
quickstart the docs-site Get Started guide extracts snippets from. It is
deliberately **not** a demokit walkthrough and does **not** use
`examples/common`: it is a plain `server/` + `client/` pair whose only
dependency is `github.com/panyam/mcpkit`, so a newcomer reading the Get Started
page can copy the code into their own project verbatim. `/example-audit` should
skip it (no `--serve` dispatch, no `runDemo`, no `WALKTHROUGH.md`, no
`common.RunServer`). Every other non-UI example follows the full convention
below.

---

## 1. Directory layout

### Non-UI examples (default)

```
examples/<name>/
├── main.go               # dual-mode: --serve runs the server, no flag runs the walkthrough
├── walkthrough.go        # demokit walkthrough (one or more files, package main)
├── WALKTHROUGH.md        # auto-generated; never hand-edit
├── README.md             # hand-curated narrative
├── Makefile              # demo / serve / readme / build (+ example-specific extras)
├── go.mod / go.sum       # local replaces for mcpkit / demokit
└── testdata/             # optional — only if the walkthrough needs static fixtures
```

### UI examples (MCP Apps)

```
examples/<name>/
├── main.go               # single-mode: just runs the server (no scripted walkthrough)
├── <app>.html            # the iframe payload — uses {{ template "mcpkit-bridge" .Bridge }}
├── README.md             # carries setup + sequence diagrams + screenshots in lieu of WALKTHROUGH.md
├── Makefile              # at minimum a `run` (or `serve`) target
├── go.mod / go.sum
└── screenshots/          # optional — visual proof of the rendered UI
```

UI examples **do not** have `walkthrough.go` or `WALKTHROUGH.md` — the host
(MCPJam, Claude Desktop) drives interaction, not demokit. See §7 for the full
UI addendum.

---

## 2. main.go (non-UI)

### Dual-mode dispatch

```go
package main

import (
    "flag"
    "log"
    "os"
    "strings"

    "github.com/panyam/demokit"
    "github.com/panyam/mcpkit/core"
    "github.com/panyam/mcpkit/server"
)

func main() {
    for _, arg := range os.Args[1:] {
        if strings.TrimSpace(arg) == "--serve" {
            serve()
            return
        }
    }
    runDemo() // defined in walkthrough.go
}
```

### serve()

- `flag.String` for `-addr` (default `:8080`) and any example-specific flags.
- Parse with `flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:], extras...))`.
  `FilterArgs` strips demokit's own dispatcher flags by default
  (`--non-interactive`, `--record`, `--replay`, `--doc`, `--from`, `--out`,
  `--serve`, `--input-timeout`); pass extra `demokit.BoolFlag(...)` /
  `demokit.ValueFlag(...)` for the example's own flags. **Note:** demokit
  declares `--serve` as a value flag (its live-demo web mode), but examples use
  bare `--serve` as a dual-mode dispatch trigger — pass `demokit.BoolFlag("--serve")`
  to override the built-in. Canonical shape:
  ```go
  flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:],
      demokit.BoolFlag("--serve"),    // override demokit's value-form
      demokit.ValueFlag("--url"),     // demo-side server URL override
      // ... example-specific extras
  ))
  ```
  Renderer / mode predicates use `demokit.IsTUI()` and `demokit.IsNonInteractive()` —
  no hand-rolled `os.Args` scans.
- The full bootstrap-and-serve loop comes from `common.RunServer`. Construct
  the canonical baseline (listen + logger + middleware), register tools, log
  `[<name>] listening on <addr>`, and call `srv.ListenAndServe(WithStreamableHTTP(true), ...)`
  with graceful-shutdown wiring in one call:
  ```go
  if err := common.RunServer(common.ServerConfig{
      Name: "<name>",
      Addr: *addr,
      Options: []server.Option{
          server.WithExtension(&ui.UIExtension{}),
          server.WithFileInputValidation(),
      },
      Register: func(srv *server.Server) {
          registerTools(srv)
      },
  }); err != nil {
      log.Fatalf("ListenAndServe: %v", err)
  }
  ```
  Fields beyond `Name`/`Addr`:
  - `Version` defaults to `"0.1.0"`.
  - `LogPrefix` defaults to `"[mcp] "` and is passed to the canonical
    color logger.
  - `Options` appends `server.Option`s (e.g. `WithExtension`,
    `WithListCacheControl`, `WithAuth`) to the baseline before `NewServer`.
  - `Register` runs after `NewServer` so it has the `*server.Server` for
    `RegisterTool` / `UseMiddleware` / extension registration.
  - `TransportOptions` appends `server.TransportOption`s after
    `WithStreamableHTTP(true)` at `ListenAndServe` time — use for
    `WithStatelessMode`, `WithMux`, `WithHandlerWrap`, `WithSSE`, etc.
  - `Logger` (optional `*log.Logger`) — when set, RunServer skips
    `MCPServerOptions` and uses `WithMCPLogging(Logger)` instead, so
    callers that need custom color rules (`NewMCPLogger(prefix, extras...)`)
    keep their handle.
  - `TracerProvider` (optional `core.TracerProvider`) — when non-nil,
    wires `server.WithTracerProvider(cfg.TracerProvider)` into the
    baseline. Pass the result of `commonotel.SetupTelemetry` directly
    (it returns the mcpotel-wrapped provider). See §Telemetry wiring
    below for the canonical helper invocation.

  When the serve loop diverges substantially from this shape (parallel
  webhook listeners, multiple servers in one process), fall back to
  manual `common.MCPServerOptions(*addr, "[mcp] ")` + `server.NewServer`
  + `srv.ListenAndServe(...)`. The canonical exception today is
  `examples/events/discord/` — see its `main.go` for why.

### Telemetry wiring

Every example exposes the same `--exporter` / `--otlp-endpoint` flag
pair via `common.RegisterTelemetryFlags(flag.CommandLine)`, then
calls `commonotel.SetupTelemetry(ctx, ...)` and threads the result
into `common.ServerConfig.TracerProvider`. Default `--exporter=""`
returns `core.NoopTracerProvider{}` (zero overhead, no spans) — an
operator opts in per invocation. No example dumps spans to stdout
unless explicitly asked.

`--exporter` accepts four values:

- `""` (default) — `core.NoopTracerProvider{}`. Zero overhead.
- `stdout` — `stdouttrace` exporter writing to `os.Stdout`. Teaching
  / demo mode.
- `otlp` — `otlptracegrpc` exporter to `--otlp-endpoint` (default
  `localhost:4317`). If the endpoint is unreachable, falls back to
  Noop with a log warning.
- `auto` — probe the OTLP endpoint; if reachable, behave like
  `otlp`; if not, fall back to Noop **silently** (no warning). Right
  pick for examples that may or may not have the local
  `docker/observability/` stack running.

Canonical wiring inside `serve()`:

```go
import (
    "context"
    "log"

    "github.com/panyam/mcpkit/examples/common"
    commonotel "github.com/panyam/mcpkit/examples/common/otel"
)

func serve() {
    addr := flag.String("addr", ":8080", "listen address")
    tel := common.RegisterTelemetryFlags(flag.CommandLine)
    flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:],
        demokit.BoolFlag("--serve"),
        demokit.ValueFlag("--url"),
        // ... example-specific extras
    ))

    tp, shutdown, err := commonotel.SetupTelemetry(context.Background(),
        commonotel.WithExporter(*tel.Exporter),
        commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
        commonotel.WithServiceName("<example-name>"),
    )
    if err != nil {
        log.Fatalf("commonotel.SetupTelemetry: %v", err)
    }
    defer shutdown(context.Background())

    // ... existing setup ...

    if err := common.RunServer(common.ServerConfig{
        Name:           "<example-name>",
        Addr:           *addr,
        TracerProvider: tp,
        // ... existing fields ...
    }); err != nil {
        log.Fatalf("ListenAndServe: %v", err)
    }
}
```

The `--exporter` / `--otlp-endpoint` flags pass through
`demokit.FilterArgs` unstripped (they're not in demokit's default
strip set), so they reach the stdlib `flag.Parse` naturally — no
`demokit.ValueFlag(...)` registration needed.

When `--exporter=otlp` is selected and the endpoint is unreachable,
SetupTelemetry logs a warning and falls back to Noop — a dead
`docker/observability/` stack never breaks `just demo`. Bring the
stack up with `cd docker/observability && just up` before the OTLP
path lights up in Grafana.

`examples/otel/stdout/` is the deliberate exception: its
`defaultExporter = "stdout"` because the example's whole purpose is
showing traces. Every other example defaults to `""`.

### Wire selection (SEP-2575)

Examples pick the SEP-2575 wire from a single `--wire` flag rather than
hand-rolling `stateless.ParseMode` / `client.ParseClientMode`. Register
it next to the telemetry flags via `common.RegisterWireFlags(flag.CommandLine)`
and thread the handle into `common.ServerConfig.Wire`. A walkthrough that
doesn't call `flag.Parse` uses `common.WireFromArgs()` (mirrors
`ExporterFromArgs`).

`--wire` drives BOTH halves of a demo binary so the server and the
walkthrough client agree on one knob:

- `--wire=legacy` — server `ModeLegacyOnly` + client `ClientModeLegacyOnly`
- `--wire=dual` — server `ModeDual` + client `ClientModeAdaptive` (dual is
  the only asymmetric mapping: a Dual server speaks both wires, so the
  client probes then falls back)
- `--wire=stateless` — server `ModeStateless` + client `ClientModeStateless`
- `--wire=""` (default) — make no selection; each side falls through to
  `MCPKIT_STATELESS_MODE` / `MCPKIT_CLIENT_MODE` env or its package default
  (server `ModeDual`, client `ClientModeLegacyOnly`)

`--server-wire` / `--client-wire` override one side when a demo needs them
decoupled. An unrecognized token logs a warning and falls through to the
default rather than binding a surprising wire.

Canonical wiring inside `serve()` (extends the telemetry setup above):

```go
func serve() {
    addr := flag.String("addr", ":8080", "listen address")
    tel := common.RegisterTelemetryFlags(flag.CommandLine)
    wire := common.RegisterWireFlags(flag.CommandLine)
    flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:],
        demokit.BoolFlag("--serve"),
        demokit.ValueFlag("--url"),
    ))

    // ... telemetry setup ...

    if err := common.RunServer(common.ServerConfig{
        Name:           "<example-name>",
        Addr:           *addr,
        TracerProvider: tp,
        Wire:           wire, // applied after TransportOptions; CLI overrides hardcoded WithStatelessMode
    }); err != nil {
        log.Fatalf("ListenAndServe: %v", err)
    }
}
```

Like the telemetry flags, the `--wire` family passes through
`demokit.FilterArgs` unstripped in `=` form (`--wire=stateless`). Only the
space-separated form (`--wire stateless`) needs an explicit
`demokit.ValueFlag("--wire")` in the filter list; most examples use the
`=` form and skip it.

`examples/stateless/` is the deliberate exception: it keeps its own
`--mode` flag defaulting to `stateless` because the SEP-2575 conformance
fixture drives it with no flag and needs the stateless wire by default,
not the `ModeDual` fall-through `--wire=""` would give.

### Dual-mode posture (SEP-2575)

Every new non-UI example MUST work on both wires — the legacy session wire
and the SEP-2575 stateless wire — because servers default to `ModeDual`.
`just verify-dual` enforces this for the auto-drivable examples;
`examples/DUAL_MODE_AUDIT.md` records the per-example verdict.

Rules for a dual-safe example:

- **Never call `ctx.Sample` / `ctx.Elicit` in a tool handler.** Server-initiated
  push doesn't exist on the stateless wire (no session to correlate the
  round-trip against), so those error with `ErrNoRequestFunc`. Use the MRTR
  pattern instead: `ctx.RequestInput(core.InputRequests{...})` with
  `core.NewSamplingInputRequest` / `core.NewElicitationInputRequest`, which
  threads the correlation state explicitly. See `examples/mrtr`.
- **Don't rely on session-scoped state.** Cross-call state belongs in an
  explicit handle threaded through tool arguments (SEP-2567); see
  `examples/stateless`. Handle-pattern migration is tracked in issue 470.
- **Thread `--wire`** via `common.RegisterWireFlags` + `ServerConfig.Wire`
  (server) and `common.WireFromArgs().ClientOption()` (walkthrough) so the
  example is driveable on either wire from one flag (see Wire selection above).
- **Routable methods carry their name.** A handler that issues task ops or
  other routable calls relies on the SEP-2243 `Mcp-Name` header, which the
  typed client helpers emit automatically when params are passed as maps (see
  `core.DeriveMcpName`). On the stateless wire there is no session to route by,
  so a missing name fails the call.

Genuinely interactive examples (browser consent, AppHost UI) may stay
legacy-only; document the reason in `DUAL_MODE_AUDIT.md` rather than forcing a
stateless path that can't exist.

#### Client-side wiring (walkthrough.go)

Walkthroughs (runDemo) typically don't call `flag.Parse` — they rely
on `common.ServerURL`'s ad-hoc `os.Args` scan for `--url`. The
symmetric helper for telemetry is `common.ExporterFromArgs()`, which
scans `os.Args` for `--exporter` / `--otlp-endpoint` and returns the
same `*TelemetryFlags` shape `RegisterTelemetryFlags` would have
populated. Pair it with `commonotel.SetupClientTelemetry`, which
pre-sets the client-side instrumentation library name so spans group
correctly in OTel-aware backends:

```go
import (
    "context"
    "log"

    "github.com/panyam/mcpkit/client"
    "github.com/panyam/mcpkit/examples/common"
    commonotel "github.com/panyam/mcpkit/examples/common/otel"
)

func runDemo() {
    serverURL := common.ServerURL()

    tel := common.ExporterFromArgs()
    tp, shutdown, err := commonotel.SetupClientTelemetry(context.Background(),
        commonotel.WithExporter(*tel.Exporter),
        commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
        commonotel.WithServiceName("<example-name>-host"),
    )
    if err != nil { log.Fatalf("commonotel.SetupClientTelemetry: %v", err) }
    defer shutdown(context.Background())

    // ... demo definition ...

    c := client.NewClient(serverURL+"/mcp", info,
        client.WithTracerProvider(tp),
        // ... other client options ...
    )
}
```

Service-name convention: `<example-name>-host` for the walkthrough
side, matching the server's `<example-name>` / `<example-name>-demo`
so Grafana / Tempo filter-by-service distinguishes the two halves of
each stitched trace.

For walkthroughs that DO call `flag.Parse` (e.g. when they need
example-specific flags), use `common.RegisterTelemetryFlags(flag.CommandLine)`
instead of `ExporterFromArgs` — both populate the same struct shape.

#### Metrics wiring (issue 668)

The same `--exporter` / `--otlp-endpoint` flag pair drives an optional
`commonotel.SetupMetrics(...)` call. When set, the dispatch path
emits the four canonical MCP server instruments through the issue 7
`core.MeterProvider` seam: `mcp.tool.calls`, `mcp.jsonrpc.errors`,
`mcp.tool.duration` (ms), `mcp.sessions.active`. Modes mirror
`SetupTelemetry`:

- `""` (default) — `core.NoopMeterProvider{}`. Zero overhead.
- `stdout` — `stdoutmetric` exporter wrapped in a 5s periodic reader.
- `otlp` — `otlpmetricgrpc` exporter to `--otlp-endpoint`. Dial-failure
  falls back to Noop with a warning.
- `auto` — same as `otlp` but silent on dial-failure.

Canonical wiring inside `serve()` (extends the trace + logs setup):

```go
meterProvider, metricsShutdown, err := commonotel.SetupMetrics(context.Background(),
    commonotel.WithExporter(*tel.Exporter),
    commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
    commonotel.WithServiceName("<example-name>"),
)
if err != nil { log.Fatalf("commonotel.SetupMetrics: %v", err) }
defer metricsShutdown(context.Background())

common.RunServer(common.ServerConfig{
    // ...
    TracerProvider: tp,
    MeterProvider:  meterProvider,
})
```

Exemplars are wired by default: the ext/otel meter adapter forwards
ctx unchanged, and the OTel SDK stamps the active span's identity
onto each measurement. Grafana renders these as clickable dots that
pivot to the matching trace in Tempo.

#### Grafana dashboards (issue 668)

**Canonical first.** The bundled [`mcpkit — overview`](http://localhost:3000/d/mcpkit-overview)
dashboard (stable permalink `/d/mcpkit-overview`) works for ANY example
that emits the four canonical instruments — pick the example from the
`$service` dropdown, the rest of the panels rescope automatically.
The four canonical instruments + the `tool` / `code` attributes are
the shared contract, so no per-example wiring is needed to see
metrics on day one.

**Per-example dashboards are an escape hatch**, NOT the default.
Reach for one only when an example surfaces metrics the canonical
dashboard can't express — e.g., `ext/tasks` task-lifecycle panels,
`events` replica fanout, `apps` iframe-bridge latency. Most examples
will never need one.

When an example DOES need its own dashboard, the convention is:

- Drop the JSON at `docker/observability/grafana/provisioning/dashboards/files/<example>/overview.json`.
- Set `"uid": "<example>"` so the dashboard is reachable at
  `/d/<example>` (Grafana-native stable URL).
- Grafana's `foldersFromFilesStructure: true` provisioning option
  auto-creates a folder named `<example>` in the UI from the
  directory name.
- Scope the `$service` variable to `<example>.*` so it covers the
  server (`<example>`) plus the walkthrough host (`<example>-host`).

A scaling story (Make-driven template + manifest generator) is
tracked on issue 737 — fires when more than one example needs a
specialized dashboard. Today, per-example dashboards are
hand-checked-in copies (fine for 0-3 of them).

See [`docker/observability/`](../docker/observability/README.md) for
the LGTM stack the dashboards consume.

#### Logs wiring (issue 668)

The same `--exporter` / `--otlp-endpoint` flag pair drives an optional
`commonotel.SetupLogs(...)` call. When set, log records emitted via
`slog.*Context(ctx, ...)` ship to the configured OTLP endpoint and
carry `trace_id` / `span_id` stamped by the otelslog bridge — the
Loki↔Tempo pivot in Grafana. Modes mirror `SetupTelemetry`:

- `""` (default) — `slog.Default()`. No OTLP side, no SDK pulled.
- `stdout` — `stdoutlog` exporter → otelslog bridge. JSON records on
  the configured writer (default `os.Stdout`).
- `otlp` — `otlploggrpc` exporter → otelslog bridge. Dial-failure
  falls back to `slog.Default()` with a warning.
- `auto` — same as `otlp` but silent on dial-failure.

Canonical wiring inside `serve()` (extends the trace setup above):

```go
logsLogger, logsShutdown, err := commonotel.SetupLogs(context.Background(),
    commonotel.WithExporter(*tel.Exporter),
    commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
    commonotel.WithServiceName("<example-name>"),
)
if err != nil { log.Fatalf("commonotel.SetupLogs: %v", err) }
defer logsShutdown(context.Background())
slog.SetDefault(logsLogger)
```

Tool handlers then log via `slog.InfoContext(ctx, "msg", "k", "v")`
— **always pass ctx**, otherwise the bridge can't read the active
span and Grafana shows the log line without a trace pivot.

`commonotel.SetupClientLogs(...)` is the walkthrough-side sibling
(pre-sets `ClientInstrumentationName`).

> **Coexistence with MCP `notifications/message`**: server-side
> observability (this helper) and client-visible MCP logs
> (`core.MCPLogHandler`) emit through distinct slog handlers and
> don't interfere. A server can compose both via `slogmulti` or
> two separate `*slog.Logger` instances.

- Side endpoints (e.g. `/inject` for synthetic events) go through
  `server.WithMux(func(mux *http.ServeMux) { ... })` (in `TransportOptions`)
  — not a hand-rolled `http.Server{}`. Graceful shutdown is built into
  `srv.ListenAndServe` via `gohttp.ListenAndServeGraceful`; **never** call
  `http.ListenAndServe` directly.

### Logger color rules (canonical set)

The five-rule set lives in `examples/common/logger.go` (see
`common.NewMCPLogger`). Examples consume it through the helper rather than
inlining the rule list. If a future change adjusts the rules, every example
picks it up via `examples/common` — that's the single point of update.

Tint additional example-specific lines via the variadic extras:

```go
logger := common.NewMCPLogger("[mcp] ",
    demokit.ColorRule{Contains: "[upload]", DarkColor: demokit.ANSIYellow},
)
```

### Tool registration style (issue 851)

Prefer typed registration for tools with real inputs: define an input struct
and register via `srv.Register(core.TextTool[In](...))` or
`core.TypedTool[In, Out](...)`. mcpkit derives the JSON Schema from the struct
tags and hands the handler a decoded, validated value — no hand-written
`map[string]any` schema, no `req.Bind` / `json.Unmarshal(req.Arguments)`. Pick
`Out` by what the handler returns: `string` (→ `TextTool`), `core.ToolResult`
(sync with `IsError` / structured content), or `core.ToolResponse` (MRTR / task
variants). `examples/getting-started` is the reference shape.

Raw `srv.RegisterTool(core.ToolDef{...}, handler)` is still correct — do **not**
force a conversion — in these cases:

- **The handler needs the raw `core.ToolRequest`.** MRTR / task-composition
  handlers (`examples/mrtr`, `examples/tasks-v2`'s `test_tool_with_task`) drive
  `ctx.RequestInput` / `ctx.InputResponse` and return `core.ToolResponse`; a
  typed input struct adds nothing.
- **The schema needs JSON Schema features struct tags can't express** —
  conditional `if/then/else`, `allOf`/`anyOf`, `$anchor`/`$ref`, or the
  SEP-2356 `x-mcp-file` marker (`examples/file-inputs`). Use
  `core.WithInputSchemaOverride(...)` with a typed handler, or stay raw.
- **The tool is a conformance fixture whose wire schema is asserted.** Convert
  to a typed handler but pin the exact schema with
  `core.WithInputSchemaOverride(...)` — `TypedTool`'s reflected schema adds
  `$schema` + `properties:{}`, which can drift an asserted `tools/list` shape.
  `examples/stateless` does this; verify with the relevant `just testconf-*`.

An empty-input tool (`struct{}`) gains nothing from typing beyond consistency;
converting one is optional, not required.

---

## 3. walkthrough.go

### Skeleton

```go
package main

import (
    "github.com/panyam/demokit"
    "github.com/panyam/mcpkit/client"
    "github.com/panyam/mcpkit/examples/common"
)

func runDemo() {
    serverURL := common.ServerURL()  // honors --url, $MCPKIT_SERVER_URL, or default :8080

    demo := demokit.New("<Title>").
        Dir("<name>").
        Description("<one paragraph: what this example teaches>").
        Actors(
            demokit.Actor("Host",   "MCP Host (this client)"),
            demokit.Actor("Server", "MCP Server (just serve)"),
        )

    demo.Section("Setup", "Start the MCP server in a separate terminal first:", "...")

    // ... .Step(...).Arrow(...).DashedArrow(...).Note(...).Run(func(ctx) { ... })

    demo.Section("Where to look in the code", "- ...", "- ...")

    common.SetupRenderer(demo)  // applies tui.New() if --tui was passed

    demo.Execute()
}
```

### Step pattern

Every executable stage uses:

```go
demo.Step("<Title>").
    Arrow("Host", "Server", "<solid-line label>").       // sync request
    DashedArrow("Server", "Host", "<dashed-line label>"). // async response/notification
    Note("<prose explaining the step; can be multi-line, can include code>").
    Run(func(ctx demokit.StepContext) *demokit.StepResult {
        // 1. call MCP via *client.Client
        res, err := c.Call("tools/call", map[string]any{...})
        if err != nil {
            fmt.Printf("    ERROR: %v\n", err)
            return nil
        }

        // 2. unmarshal raw JSON for pretty-printing
        var v any
        if err := json.Unmarshal(res.Raw, &v); err != nil {
            fmt.Printf("    ERROR decoding raw: %v\n", err)
            return nil
        }

        // 3. print to demo stdout (demokit captures it for replay/doc mode)
        pretty, _ := json.MarshalIndent(v, "", "  ")
        fmt.Println(string(pretty))
        return nil
    })
```

A `nil` return means "step succeeded with no message" — the canonical
shape across examples. Examples that want to surface a custom status or
jump to a specific next step return a non-nil `*demokit.StepResult` (see
`demokit/result.go`).

### Notes are prose; call shapes go in Verbatim

`Note(...)` is the step's prose explanation, rendered as wrapped
paragraphs in the TUI box and the plain renderer. Inline backticks render
as literal backticks inside the bordered box — they do not become syntax
highlighting. When the audience needs to see an actual call shape, a
multi-line snippet, or a shell command, attach a `Verbatim(label,
content)` (or `VerbatimLang` / `Shell` / `VerbatimVariants`) block. The
verbatim renders outside the wrapped prose, preserves character-exact
formatting, and gets copy support in the TUI.

The rule is about *shapes*, not identifiers. A method or type name
mentioned in passing (e.g., "the Provider", "Connect") reads fine
without backticks. The thing to lift is anything that resembles code you
could copy and run.

Bad — call shape rendered as backtick-bracketed prose:

```go
.Note("`client.NewClient(..., client.WithClientMode(mode))` then `Connect()`. Inspect `c.UsingStatelessWire()` after the call.").
```

Good — prose explains intent, Verbatim carries the shape:

```go
.Note("Construct the client with the chosen wire mode, then connect. Inspect the new accessor after the call to see which wire engaged.").
Verbatim("Go", `c := client.NewClient(serverURL+"/mcp", info,
    client.WithClientMode(wireMode),
)
c.Connect()
wire := c.UsingStatelessWire()`).
```

Shell commands take the same shape via the `Shell(content)` shorthand —
`Shell` is `VerbatimLang("", "bash", content)` and is the right pick for
copy-pasteable `make` / `curl` / `bash` invocations. The
`VerbatimVariants("Reproduce on the wire", ...)` block below is a
specialized form for steps that issue an MCP call.

### Verbatim variants — "Reproduce on the wire"

Every step that makes an MCP call attaches a `VerbatimVariants("Reproduce on
the wire", ...)` block, chained between `.Note(...)` and `.Run(...)`. Two
variants per call step:

- `curl` (marked `.Default()`) — raw JSON-RPC over HTTP, copy-pasteable.
  Shows the wire format directly so readers can validate behaviour from a
  non-Go SDK or sanity-check the JSON shape.
- `go` — the equivalent `*client.Client` form, mirroring what the step's
  `Run` closure actually does.

```go
.Note("...").
VerbatimVariants("Reproduce on the wire",
    demokit.MakeVariant("curl", "bash", `curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"foo","arguments":{}}}' | jq '.result'`).Default(),
    demokit.MakeVariant("go", "go", `res, _ := c.ToolCall("foo", map[string]any{})
// ... mirror the Run closure`),
).
Run(func(ctx demokit.StepContext) ...
```

The curl variants chain shell vars (`$SID`, `$TOK_*`, etc.) set up by an
earlier step — the first call-making step mints the session id via the
standard initialize + `notifications/initialized` ack pattern. See
`examples/elicitation/main.go` and `examples/fine-grained-auth/main.go`
for canonical chains.

Default markdown output (the form written to `WALKTHROUGH.md`) shows only
the curl variant — `walkthrough-md-fresh` runs `go run . --doc md` with no
`--variant` flag and asserts that. Pass `--variant=go` or `--variant=all`
at the CLI to render the Go form; TUI / notebook renderers surface both
with copyable variant labels regardless.

Inside Go raw-string literals (backticks) you cannot include a backtick.
Avoid struct tags in the Go variant; use double-quoted strings with
escapes if you need quoted JSON inline, or describe the value in a `//`
comment.

If your `Run` body needs to call something that takes a
`context.Context` (e.g. `client.CallToolWithInputs`), do **not** name a
local variable `ctx` — the parameter is already named `ctx` and shadows
won't compile via `:=`. Use `bgCtx := context.Background()` or pull from
the demokit-provided `ctx.Ctx` (which honors `Timeout` / `Cancellable`).

### Client logging convention

- Connect once per walkthrough; close at the end (`defer c.Close()` or
  explicit `if c != nil { c.Close() }` after `demo.Execute()`).
- Use `*client.Client` from `github.com/panyam/mcpkit/client`, not raw HTTP.
- Pretty-print the **raw** JSON (`res.Raw`) — never the typed struct. The
  point is to show the wire format.
- For error paths: render the JSON-RPC error via `common.PrintRPCError(err,
  wantReason)`. Pass `wantReason=""` for plain rendering; pass a non-empty
  string when the demo wants to assert that `err.Data["reason"]` matches a
  spec-defined value (a WARN line is printed on mismatch — useful for
  spec-validation demos where wire-shape regressions should surface in
  stdout, not just in the conformance suite).

### Fixtures

- **Static bytes** (images, PDFs, sample text) → embed via `//go:embed
  testdata/...` so the walkthrough is hermetic. No working-directory tricks.
- **Live events** (webhook deliveries, push notifications) → expose a
  `/inject` POST endpoint via `server.WithMux` and have the walkthrough
  `POST` to it.

Pick whichever the example actually needs — both are first-class.

---

## 4. WALKTHROUGH.md

- **Auto-generated.** Regenerated via `just readme` (`go run . --doc md >
  WALKTHROUGH.md`). Never hand-edit.
- Sections demokit emits: "What you'll learn" (bulleted step titles),
  "Flow" (Mermaid sequence diagram from `Arrow` / `DashedArrow`), "Steps"
  (one `###` per step with the `Note()` prose expanded), and any
  hand-written `Section()` blocks.
- Commit the generated file. CI / `/example-audit` re-runs `just readme`
  and diffs to catch drift.

## 5. README.md

Hand-curated narrative. The READMEs render on the docs site as the
examples/tutorials track (issue 508), so they should read as tutorials, not
just reference. Required sections (in order):

1. **Title + one-paragraph what-this-is**
2. **Status line** — a one-line blockquote directly under the title stating
   maturity and the spec it tracks, so a reader sees at a glance whether the
   feature is stable or experimental (mcpkit ships several draft SEPs ahead of
   the spec — say so). Format:
   - Stable, merged SEP: `> **Stable** — implements [SEP-N](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/N) (<Name>), merged into the MCP spec.`
   - Experimental / draft SEP: `> ⚠ **Experimental** — tracks [SEP-N](.../pull/N) (<Name>), a draft SEP. Wire format may change.`
   - Core / no numbered SEP: `> **Stable** — MCP base protocol (<area>).`

   The SEP PR is the canonical spec-and-discussion venue (don't invent a Discord
   invite; the repo has none). Use `modelcontextprotocol/modelcontextprotocol/pull/N`,
   not the old `.../specification/...` path.
3. **Quick Start** — the exact two commands a reader runs (typically
   `just serve` in one terminal, `just demo` in another).
4. **What it demonstrates** — bullet list mapping to the demokit steps.
5. **Architecture** — Mermaid block-or-sequence diagram if the example has
   non-trivial topology (separate processes, webhook callbacks, MCP Apps
   bridge).
6. **Where to look in the code** — bullet list of `path/file.go:symbol`
   pointers, mirroring the closing `Section()` in `walkthrough.go`.
7. **Next steps** — 1-3 links pointing the reader onward: the most related
   example(s) and the canonical design/migration doc. Use repo-relative links
   (`../other-example/`) so they resolve both on GitHub and on the rendered
   site.

Optional: "What's still pending" (phase tracker), "Setup — getting an API
token", "Recipes" (only if the justfile has more than the baseline four).

---

## 6. justfile

### Baseline recipes (every example)

```just
# Run the walkthrough (bare `just` runs this)
default: demo

# Run the demokit walkthrough (interactive, TUI)
demo:
    go run . --tui

# Run the walkthrough in notebook mode (Bubble Tea cells)
note:
    go run . --note

# Start the <name> demo server (default :8080)
serve:
    go run . --serve

# Regenerate WALKTHROUGH.md from demo definitions
readme:
    go run . --doc md > WALKTHROUGH.md

# Build the binary
build:
    go build -o <name>-demo .
```

- Doc comment on the line above each recipe — discoverable via `just --list`.
- `default: demo` so a bare `just` runs the walkthrough.
- `--doc md` (no `=`) is the canonical form; `--doc=md` works too but the
  convention is the spaced form for readability.
- `just note` shells out to `--note`, which `demokit.Mode()` resolves to
  `"notebook"`. `common.SetupRenderer` routes that to
  `notebookbridge.New()` — wired once centrally, so every walkthrough
  inherits notebook mode without per-example renderer glue. Notebook
  output cells render with horizontal-only borders (no vertical bars),
  which keeps streamed output clean to mouse-select and copy.

### Extras (allowed, example-specific)

`test`, `test-race`, integration harnesses, client-tool wrappers, `inject`
shortcuts — fine to add. Keep them grouped under section comment dividers
(`# ── Tests ─────────…`) and add them to `.PHONY`.

---

## 7. UI examples (MCP Apps) — addendum

UI examples diverge from the non-UI shape because the host (MCPJam, Claude
Desktop) drives interaction, not demokit. They follow these rules instead:

- **No `walkthrough.go` / `WALKTHROUGH.md`.** Demokit can't synthesize
  iframe user-gestures, so a scripted walkthrough would be a lie.
- **`main.go` is single-mode.** No `--serve` flag, no `runDemo()`. The
  binary just starts the server. Use `server.Run(*addr)` (simpler) unless
  you need explicit shutdown control.
- **`server.WithExtension(&ui.UIExtension{})`** is required.
- **HTML asset** (`<app>.html`) embedded via `//go:embed` and rendered with
  `html/template` — `{{ template "mcpkit-bridge" .Bridge }}` injects the
  bridge JS.
- **Tool registration** uses `ui.RegisterTypedAppTool(...)` (not plain
  `RegisterTool`) so the host knows the tool ships an app.
- **README.md replaces WALKTHROUGH.md** for procedural docs:
  - Sequence diagrams (LLM → server, iframe → server, app-provided tools)
    in lieu of demokit-generated ones.
  - "Try it — Step by Step" with LLM prompts + UI clicks.
  - `screenshots/` directory referenced inline.
- **Makefile** can be minimal (a single `run: ; go run .` target). If you
  add `serve`, alias it to `run`.

**UI examples must use the same logger as non-UI** —
`demokit.NewColorLogger` with the canonical 5-rule set from §2,
`server.WithRequestLogging(logger)`, and
`server.WithMiddleware(server.LoggingMiddleware(logger))`. Even minimal UI
examples (e.g. apps/vanilla) carry the middleware so a side-by-side
`go run .` + host shows the same tinted MCP traffic readers see in non-UI
demos. Also shared: default port `:8080`, and the README's "Where to look in
the code" pointer list.

---

## 8. Audit checklist (for `/example-audit`)

Each check has a **stable ID** in `code-style`. `/example-audit` and
`/example-upgrade` use these IDs as a contract — renaming one is a breaking
change. Items not applicable to a given example are omitted from the audit
output rather than emitted as N/A.

### Precondition (both UI and non-UI)

- [ ] `build-broken` — `cd <dir> && go build ./...` succeeds. If this fails,
  most other checks (especially `walkthrough-md-fresh`) cannot run; the audit
  emits this finding alone and stops further checks that depend on a working
  build.

### Non-UI examples

- [ ] `dispatch-loop` — `main.go` has the `--serve` dispatch loop and falls
  through to `runDemo()` otherwise.
- [ ] `logger-colorlogger` — `serve()` uses `demokit.NewColorLogger` (not
  `log.Default()`, not stdlib `log` only).
- [ ] `serve-srv-listenandserve` — `serve()` uses `common.RunServer(...)` or
  `srv.ListenAndServe(...)` (never `http.ListenAndServe(...)`). Prefer
  `common.RunServer` for the canonical bootstrap-and-serve loop; fall back
  to manual `ListenAndServe` only when the example has a documented
  divergence (e.g. `examples/events/discord/`).
- [ ] `mux-withmux` — side-endpoint registration uses `server.WithMux(...)`,
  not a hand-rolled `http.Server{}`. (Skip if the example has no side
  endpoints.)
- [ ] `filterargs-promoted` — call site uses `demokit.FilterArgs(args, extras...)`
  with `BoolFlag("--serve")` to override demokit's value-form `--serve`. No
  inline `filterFlags()` definition remains.
- [ ] `tui-helper` — `walkthrough.go` uses `common.SetupRenderer(demo)`
  before `demo.Execute()` (no inline `os.Args` scan, no hand-rolled
  `if demokit.IsTUI() { demo.WithRenderer(tui.New()) }` block).
- [ ] `url-helper` — `runDemo()` reads the server URL via
  `common.ServerURL()` (no inline `for _, arg := range os.Args[1:]`
  scan, no per-example hardcoded `"http://localhost:8080"` default).
- [ ] `mode-helpers` — mode predicates use `demokit.IsTUI()` /
  `demokit.IsNonInteractive()`; no local `tuiMode()` / `nonInteractive()`
  helpers. (Skip `IsNonInteractive` check if the example doesn't reference
  non-interactive mode.)
- [ ] `client-close` — walkthrough closes the client (`c.Close()` deferred or
  after `Execute`).
- [ ] `pretty-print-raw` — **success-path** MCP-call step `Run()` blocks
  `json.Unmarshal(res.Raw, &v)` and pretty-print, not the typed struct.
  Skip for steps that don't make an MCP call (e.g. a step that calls a
  bootstrap HTTP endpoint to mint demo tokens has no `res.Raw`).
- [ ] `error-path-helper` — error-path step `Run()` blocks render the
  JSON-RPC error via `common.PrintRPCError(err, wantReason)` (see §3
  "Client logging convention"), not ad-hoc inline `json.MarshalIndent`
  blocks or a per-example `printRPCError` copy. Skip if the example has
  no error-path steps.
- [ ] `walkthrough-md-fresh` — committed `WALKTHROUGH.md` matches
  `go run . --doc md` output (no drift). Note the canonical form takes no
  `--variant` flag, so the committed markdown shows only the curl
  (`.Default()`) variant of each `VerbatimVariants` block.
- [ ] `makefile-baseline` — Makefile has the five baseline targets (`demo` /
  `note` / `serve` / `readme` / `build`) with `## help-text` comments.
- [ ] `wire-verbatim-variants` — every demo step that makes an MCP call
  attaches a `VerbatimVariants("Reproduce on the wire", curl(Default), go)`
  block between `.Note(...)` and `.Run(...)` (see §3 "Verbatim variants").
  Skip steps that don't make a call (bootstrap HTTP, narrative-only).
- [ ] `makefile-default-goal` — Makefile sets `.DEFAULT_GOAL := demo`.
- [ ] `readme-quickstart` — README has a Quick Start block with `just serve`
  + `just demo`.
- [ ] `readme-what-it-demonstrates` — README has a "What it demonstrates"
  bullet list mapping to the walkthrough steps.
- [ ] `readme-where-to-look` — README has a "Where to look in the code"
  section with `path/file.go:symbol` pointers.

### UI examples

- [ ] `ui-extension` — `main.go` registers
  `server.WithExtension(&ui.UIExtension{...})`.
- [ ] `ui-bridge-template` — HTML asset includes
  `{{ template "mcpkit-bridge" .Bridge }}`.
- [ ] `ui-typed-app-tool` — UI tools registered via
  `ui.RegisterTypedAppTool(...)`, not plain `RegisterTool`.
- [ ] `ui-no-walkthrough` — no `walkthrough.go` / `WALKTHROUGH.md` exist.
- [ ] `ui-readme-diagrams` — README contains sequence diagrams and references
  to the `screenshots/` directory.
- [ ] `logger-colorlogger` — same as non-UI; required even for minimal UI
  examples per §7.
- [ ] `mux-withmux` — same as non-UI (skip if no side endpoints).

### Host-side examples

Host-side examples (`host/01-apphost`, `host/02-multi-server`) are
in-process by construction — see §9. Apply the non-UI walkthrough rules
(`tui-helper`, `mode-helpers`, `client-close`, `pretty-print-raw`,
`error-path-helper`) and the README rules (`readme-quickstart`,
`readme-what-it-demonstrates`, `readme-where-to-look`). The following
non-UI checks **do not apply** and must be omitted from the audit output
(not emitted as `[FAIL]`):

- `dispatch-loop` — host examples are single-mode (no `--serve` branch).
- `logger-colorlogger` — no HTTP middleware in an in-process demo.
- `serve-srv-listenandserve` — no HTTP server.
- `mux-withmux` — no side endpoints.
- `filterargs-promoted` — only relevant if the example runs its own
  `flag.Parse` for server-side flags.

Plus a host-specific Makefile rule:

- [ ] `host-makefile-baseline` — Makefile has `demo`, `note`, and `readme`
  targets only (no `serve`, no `build` — there's nothing to start
  standalone and no binary to ship). `.DEFAULT_GOAL := demo` still
  applies.

---

## 9. Host-side examples — addendum

Host-side examples demonstrate mcpkit's **host-side Go APIs** (`AppHost`,
`ServerRegistry`, `InProcessAppBridge`, etc.) — i.e. the code an MCP host
author writes to consume servers and bridge to apps. They follow most of
the non-UI conventions but with a different process shape:

- **Single-process by construction.** A host example brings up server +
  client + AppHost + (in-process) AppBridge inside one `go run .`
  invocation. `InProcessAppBridge` is part of what's being demonstrated —
  "you can exercise host code without spinning up an iframe app." There is
  no separate-process server to start, so `just serve` doesn't apply.
- **`main.go` is single-mode.** No `--serve` flag, no `runDemo()` —
  `main()` directly constructs the demo and calls `demo.Execute()`.
- **No HTTP middleware.** All transports are in-process, so
  `WithRequestLogging` / `WithMiddleware(LoggingMiddleware)` aren't wired.
  Demokit owns the user-facing output via its renderer.
- **Multi-actor demos.** Walkthroughs typically declare 4 actors (e.g.
  `Srv` / `Client` / `Host` / `Bridge`) instead of the non-UI two-actor
  shape, because the demo's narrative arc traverses both client→server
  and host→bridge legs.
- **Makefile reduced.** Only `demo`, `note`, and `readme` targets — no
  `serve`, no `build`. `.DEFAULT_GOAL := demo` still applies. `note` loops
  through each contained example with `--note`, mirroring how `demo` loops
  with `--non-interactive` (see `examples/host/Makefile`).

What host examples still share with non-UI:

- `walkthrough.go` structure (demokit `.Section` / `.Step` pattern, raw
  JSON pretty-print on success, `printRPCError` helper on errors, client
  closes after `Execute`).
- `WALKTHROUGH.md` auto-generated via `just readme` → `go run . --doc md`.
- README sections: Quick Start (single-terminal — just `just demo`), What
  it demonstrates, Where to look in the code.
- `demokit.IsTUI()` / `demokit.IsNonInteractive()` for renderer + mode
  selection.

**If a host example later needs to expose its server over HTTP** (so an
external host like MCPJam could connect), promote it to a non-UI example
shape and drop this addendum's relaxations. That's a per-example decision,
not a global policy — see the rationale conversation in PR/commit history
if relitigating.
