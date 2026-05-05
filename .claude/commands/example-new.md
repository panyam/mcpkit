Scaffold a new mcpkit example matching the conventions in `examples/CONVENTIONS.md`.

## Args

- `$1` — example name (required, kebab-case, e.g. `progress-reports`). Used as both the directory name and the demokit `Dir(...)` value.
- `--ui` — optional flag. Scaffold an MCP-Apps-style example (HTML asset + iframe bridge + no walkthrough.go) instead of the default scripted-walkthrough shape.

If `$1` is missing, ask the user for one before doing anything else.

## Steps

1. **Read `examples/CONVENTIONS.md`** before generating anything. It is the source of truth — if it has changed since this skill was written, follow the doc, not this file. If anything below contradicts CONVENTIONS.md, CONVENTIONS.md wins.

2. **Confirm the destination:**
   - Path: `examples/<name>/` (or `examples/events/<name>/` if the user says it's an events example — ask if ambiguous).
   - Refuse if the directory already exists. Suggest `/example-audit <name>` instead.

3. **Ask the user three questions** (one shot, not back-and-forth):
   - One-paragraph description of what the example demonstrates (goes into `demokit.Description(...)` and the README intro).
   - List of MCP tools the example will register (names + one-line each). If UI, also which ones ship apps.
   - Whether the example needs static fixtures (`testdata/`) or live event injection (`/inject` POST endpoint) — see CONVENTIONS.md §3 "Fixtures." Skip for UI examples.

4. **Generate files** matching the layout in CONVENTIONS.md §1:

   **Non-UI (default):**
   - `main.go` — dual-mode dispatcher + `serve()` with `common.NewMCPLogger("[mcp] ")` + `common.WithMCPLogging(logger)` (from `examples/common`, see CONVENTIONS.md §2), `srv.ListenAndServe(server.WithStreamableHTTP(true))`, `demokit.FilterArgs(os.Args[1:], demokit.BoolFlag("--serve"), demokit.ValueFlag("--url"))` (add more `ValueFlag(...)` for example-specific flags). If the example needs side endpoints (e.g. `/inject`), wire them via `server.WithMux(...)` — never hand-roll an `http.Server{}`. Add the `examples/common` dependency to `go.mod` (`require github.com/panyam/mcpkit/examples/common ...` + `replace ... => ../common`).
   - `walkthrough.go` — `runDemo()` skeleton from CONVENTIONS.md §3. Pre-fill: title, `Dir("<name>")`, the description from step 3, two actors (Host / Server), a `Setup` section pointing to `make serve`, one placeholder `Step()` per tool with `// TODO` markers in the `Run()` body for the user to fill in, the closing "Where to look in the code" `Section()`, the `if demokit.IsTUI() { demo.WithRenderer(tui.New()) }` block, and `demo.Execute()`.
   - `README.md` — Quick Start (`make serve` / `make demo`), What it demonstrates (mirrors the step list), Architecture placeholder (Mermaid block stub if the topology is non-trivial — leave blank otherwise), Where to look in the code (mirrors walkthrough.go).
   - `Makefile` — exactly the four baseline targets (`demo`, `serve`, `readme`, `build`) with `## help-text` comments, `.PHONY` line, `.DEFAULT_GOAL := demo`. No example-specific extras at scaffold time — the user adds those after.
   - `go.mod` — module name `github.com/panyam/mcpkit/examples/<name>` (or `.../examples/events/<name>`), Go 1.23+, dependencies on `github.com/panyam/mcpkit/{core,server,client}` and `github.com/panyam/demokit` at the version currently used by `examples/file-inputs/go.mod`. Add a local `replace github.com/panyam/mcpkit => ../..` directive (relative path from the new example).
   - `testdata/` — only if the user said static fixtures in step 3. Empty directory with a `.gitkeep` is fine; user adds the actual files.

   **UI (`--ui`):**
   - `main.go` — single-mode (no `--serve` dispatch, no `runDemo`). `server.NewServer` with `server.WithExtension(&ui.UIExtension{})`, `server.WithRequestLogging(logger)`, `server.WithMiddleware(server.LoggingMiddleware(logger))`. Tools registered via `ui.RegisterTypedAppTool(...)`. Embeds `<name>.html` via `//go:embed` and renders with `html/template` including `{{ template "mcpkit-bridge" .Bridge }}`. Serves via `server.Run(*addr)`.
   - `<name>.html` — minimal iframe payload: `<!doctype html>` + a `<script>` block scaffolding `MCPApp.callTool(...)` / `MCPApp.on('event', ...)` patterns, with `// TODO` markers.
   - `README.md` — required sections: setup (`go run . -addr :8080`), connect-a-host (MCPJam URL), Sequence Diagrams (3 Mermaid stubs: LLM→server, iframe→server, app-provided tools — leave content placeholder), "Try it — Step by Step" (LLM prompts + UI clicks placeholder), Screenshots placeholder, Where to look.
   - `Makefile` — single `run: ; go run . -addr :8080` target with help comment, `.PHONY: run`, `.DEFAULT_GOAL := run`.
   - `go.mod` — same as non-UI but also depends on `github.com/panyam/mcpkit/ext/ui` (separate go.mod — needs its own `replace` directive).
   - **Do not** create `walkthrough.go` or `WALKTHROUGH.md` — UI examples are host-driven.

5. **Generate the WALKTHROUGH.md** for non-UI examples by running `cd examples/<name> && go run . --doc md > WALKTHROUGH.md` once the scaffolding compiles. If the build fails, fix the obvious issues (missing imports, typos) and retry; if it still fails, leave a `WALKTHROUGH.md` placeholder with a comment telling the user to run `make readme` after they fill in the steps.

6. **Build sanity check:** `cd examples/<name> && go build ./...`. Surface any errors.

7. **Update `examples/README.md`** — add a one-line entry to its examples list pointing at the new directory.

8. **Report to the user:**
   - The directory created and what's in it.
   - The placeholder `// TODO` sites in `walkthrough.go` (or `<name>.html` for UI) the user needs to fill in.
   - How to run it (`make demo` for non-UI, `make run` + connect-host for UI).
   - Suggest `/example-audit <name>` after they finish filling it in.

## Principles

- **CONVENTIONS.md is the spec; this skill is the applier.** Never invent conventions in the scaffolded code; if you find yourself wanting to, update CONVENTIONS.md first and ask the user.
- **Match the canon.** Look at `examples/file-inputs/` (non-UI) and `examples/apps/vanilla/` (UI) when uncertain about a detail not pinned down by CONVENTIONS.md.
- **Don't fill in domain logic.** Tool handlers, walkthrough step bodies, and HTML interaction logic stay as `// TODO` markers — the skill's job is structure, not story.
- **One scaffold, then hand off.** After step 8, exit. Don't iterate on the example's content; that's the user's job (or a follow-up session).
