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
- Logger + middleware come from `examples/common` — one line each, no
  copy-paste of color rules:
  ```go
  logger := common.NewMCPLogger("[mcp] ")
  opts := []server.Option{server.WithListen(*addr)}
  opts = append(opts, common.WithMCPLogging(logger)...)
  ```
  `common.NewMCPLogger` wraps `demokit.NewColorLogger` with the canonical
  five-rule set (see `examples/common/logger.go`); `common.WithMCPLogging`
  returns `[]server.Option` for both transport-level request logging and
  the MCP dispatch middleware. If you need to tint additional log lines,
  pass `demokit.ColorRule`s as variadic extras to `NewMCPLogger`.
- Server construction:
  ```go
  srv := server.NewServer(
      core.ServerInfo{Name: "<name>", Version: "0.1.0"},
      opts...,  // example-specific options append after common.WithMCPLogging
  )
  ```
- Serve via `srv.ListenAndServe(server.WithStreamableHTTP(true))` — graceful
  shutdown is built in (server/server.go:879 wraps
  `gohttp.ListenAndServeGraceful`). **Never** call `http.ListenAndServe`
  directly.
- Side endpoints (e.g. `/inject` for synthetic events) go through
  `server.WithMux(func(mux *http.ServeMux) { ... })` — not a hand-rolled
  `http.Server{}`.

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

---

## 3. walkthrough.go

### Skeleton

```go
package main

import (
    "os"
    "strings"

    "github.com/panyam/demokit"
    "github.com/panyam/demokit/tui"
    "github.com/panyam/mcpkit/client"
)

func runDemo() {
    serverURL := "http://localhost:8080"
    for i, arg := range os.Args[1:] {
        if arg == "--url" && i+2 < len(os.Args) {
            serverURL = os.Args[i+2]
        }
    }

    demo := demokit.New("<Title>").
        Dir("<name>").
        Description("<one paragraph: what this example teaches>").
        Actors(
            demokit.Actor("Host",   "MCP Host (this client)"),
            demokit.Actor("Server", "MCP Server (make serve)"),
        )

    demo.Section("Setup", "Start the MCP server in a separate terminal first:", "...")

    // ... .Step(...).Arrow(...).DashedArrow(...).Note(...).Run(func(ctx) { ... })

    demo.Section("Where to look in the code", "- ...", "- ...")

    if demokit.IsTUI() {
        demo.WithRenderer(tui.New())
    }

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
- For error paths: print the JSON-RPC error code + message + structured data
  via a small `printRPCError(err, label)` helper.

### Fixtures

- **Static bytes** (images, PDFs, sample text) → embed via `//go:embed
  testdata/...` so the walkthrough is hermetic. No working-directory tricks.
- **Live events** (webhook deliveries, push notifications) → expose a
  `/inject` POST endpoint via `server.WithMux` and have the walkthrough
  `POST` to it.

Pick whichever the example actually needs — both are first-class.

---

## 4. WALKTHROUGH.md

- **Auto-generated.** Regenerated via `make readme` (`go run . --doc md >
  WALKTHROUGH.md`). Never hand-edit.
- Sections demokit emits: "What you'll learn" (bulleted step titles),
  "Flow" (Mermaid sequence diagram from `Arrow` / `DashedArrow`), "Steps"
  (one `###` per step with the `Note()` prose expanded), and any
  hand-written `Section()` blocks.
- Commit the generated file. CI / `/example-audit` re-runs `make readme`
  and diffs to catch drift.

## 5. README.md

Hand-curated narrative. Required sections (in order):

1. **Title + one-paragraph what-this-is**
2. **Quick Start** — the exact two commands a reader runs (typically
   `make serve` in one terminal, `make demo` in another).
3. **What it demonstrates** — bullet list mapping to the demokit steps.
4. **Architecture** — Mermaid block-or-sequence diagram if the example has
   non-trivial topology (separate processes, webhook callbacks, MCP Apps
   bridge).
5. **Where to look in the code** — bullet list of `path/file.go:symbol`
   pointers, mirroring the closing `Section()` in `walkthrough.go`.

Optional: "What's still pending" (phase tracker), "Setup — getting an API
token", "Make targets" (only if the Makefile has more than the baseline four).

---

## 6. Makefile

### Baseline targets (every example)

```make
demo: ## Run the demokit walkthrough (interactive, TUI)
	go run . --tui

serve: ## Start the <name> demo server (default :8080)
	go run . --serve

readme: ## Regenerate WALKTHROUGH.md from demo definitions
	go run . --doc md > WALKTHROUGH.md

build: ## Build the binary
	go build -o <name>-demo .

.PHONY: demo serve readme build
.DEFAULT_GOAL := demo
```

- `## help-text` after each target — discoverable via `make help` if the
  repo grows one.
- `.DEFAULT_GOAL := demo` so a bare `make` runs the walkthrough.
- `--doc md` (no `=`) is the canonical form; `--doc=md` works too but the
  convention is the spaced form for readability.

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
- [ ] `serve-srv-listenandserve` — `serve()` uses `srv.ListenAndServe(...)`,
  not `http.ListenAndServe(...)`.
- [ ] `mux-withmux` — side-endpoint registration uses `server.WithMux(...)`,
  not a hand-rolled `http.Server{}`. (Skip if the example has no side
  endpoints.)
- [ ] `filterargs-promoted` — call site uses `demokit.FilterArgs(args, extras...)`
  with `BoolFlag("--serve")` to override demokit's value-form `--serve`. No
  inline `filterFlags()` definition remains.
- [ ] `tui-helper` — `walkthrough.go` uses
  `if demokit.IsTUI() { demo.WithRenderer(tui.New()) }` before `demo.Execute()`
  (no hand-rolled `os.Args` scan).
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
  JSON-RPC error via a small `printRPCError(err, label)`-style helper
  (CONVENTIONS.md §3 "Client logging convention"), not ad-hoc inline
  `json.MarshalIndent` blocks. Skip if the example has no error-path
  steps.
- [ ] `walkthrough-md-fresh` — committed `WALKTHROUGH.md` matches
  `go run . --doc md` output (no drift).
- [ ] `makefile-baseline` — Makefile has the four baseline targets (`demo` /
  `serve` / `readme` / `build`) with `## help-text` comments.
- [ ] `makefile-default-goal` — Makefile sets `.DEFAULT_GOAL := demo`.
- [ ] `readme-quickstart` — README has a Quick Start block with `make serve`
  + `make demo`.
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

- [ ] `host-makefile-baseline` — Makefile has `demo` and `readme` targets
  only (no `serve`, no `build` — there's nothing to start standalone and
  no binary to ship). `.DEFAULT_GOAL := demo` still applies.

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
  no separate-process server to start, so `make serve` doesn't apply.
- **`main.go` is single-mode.** No `--serve` flag, no `runDemo()` —
  `main()` directly constructs the demo and calls `demo.Execute()`.
- **No HTTP middleware.** All transports are in-process, so
  `WithRequestLogging` / `WithMiddleware(LoggingMiddleware)` aren't wired.
  Demokit owns the user-facing output via its renderer.
- **Multi-actor demos.** Walkthroughs typically declare 4 actors (e.g.
  `Srv` / `Client` / `Host` / `Bridge`) instead of the non-UI two-actor
  shape, because the demo's narrative arc traverses both client→server
  and host→bridge legs.
- **Makefile reduced.** Only `demo` and `readme` targets — no `serve`, no
  `build`. `.DEFAULT_GOAL := demo` still applies.

What host examples still share with non-UI:

- `walkthrough.go` structure (demokit `.Section` / `.Step` pattern, raw
  JSON pretty-print on success, `printRPCError` helper on errors, client
  closes after `Execute`).
- `WALKTHROUGH.md` auto-generated via `make readme` → `go run . --doc md`.
- README sections: Quick Start (single-terminal — just `make demo`), What
  it demonstrates, Where to look in the code.
- `demokit.IsTUI()` / `demokit.IsNonInteractive()` for renderer + mode
  selection.

**If a host example later needs to expose its server over HTTP** (so an
external host like MCPJam could connect), promote it to a non-UI example
shape and drop this addendum's relaxations. That's a per-example decision,
not a global policy — see the rationale conversation in PR/commit history
if relitigating.
