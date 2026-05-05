Apply fixes that bring an mcpkit example into compliance with `examples/CONVENTIONS.md`. The action half of the audit/upgrade pair.

## Args

- `$1` — example name (e.g. `discord-events`) or relative path (e.g. `examples/events/discord`). If missing, ask.

## Steps

1. **Read `examples/CONVENTIONS.md`.** It's the spec; every fix must produce code that passes its checklist.

2. **Detect findings source — pipeline mode vs cold mode:**
   - **Pipeline mode:** scan recent conversation context for an `Audit complete: N pass, M fail` line emitted by `/example-audit` against `$1`. If found AND the audit is for the same target, parse the `[FAIL]` blocks (their stable IDs and `Where:` hints) and skip to step 4.
   - **Cold mode:** no recent audit found. Run the audit checks internally (re-using the logic from `/example-audit` — read CONVENTIONS.md §8, walk the checklist, gather findings). Don't emit the full audit report; just summarize: `<N> issues found in examples/<path>: <id>, <id>, <id>`.

3. **If `build-broken` is among the findings, stop.** Surface the build error to the user and ask them to fix it first (the upgrade skill can't safely apply other fixes against code that doesn't compile). Exit.

4. **Group findings by fix complexity** and present them as a plan:
   - **Mechanical** (single-edit, low-risk): `makefile-default-goal`, `filterargs-promoted`, `tui-helper`, `mode-helpers`, `logger-colorlogger`, `serve-srv-listenandserve`, `mux-withmux` (when the side-endpoint already exists in a hand-rolled mux), `walkthrough-md-fresh` (just re-run `make readme`).
   - **Structural** (touches multiple files or rewrites a section): `dispatch-loop`, `makefile-baseline` (full Makefile rewrite), `readme-quickstart` / `readme-what-it-demonstrates` / `readme-where-to-look` (README sections), `ui-extension`, `ui-bridge-template`, `ui-typed-app-tool`.
   - **Manual-only** (skill flags but doesn't auto-apply): `client-close` (placement depends on walkthrough flow), `pretty-print-raw` (depends on the step's intent), `ui-readme-diagrams` (needs human-authored sequence diagrams). Report these as "would need manual edits at <file>:<line>" and skip them.

5. **Show the plan to the user before editing.** Format:
   ```
   Upgrade plan for examples/<path>:

   Will apply (mechanical):
     - <id>  →  <one-line summary of the edit>
     ...

   Will apply (structural — review carefully):
     - <id>  →  <one-line summary of the edit>
     ...

   Cannot auto-apply (needs your hand):
     - <id>  →  <reason>
     ...

   Proceed? [yes/no/select]
   ```
   On `yes` apply mechanical + structural. On `select` ask which IDs to apply. On `no` exit.

6. **Apply fixes in dependency order.** Fix structural before mechanical when the mechanical depends on it (e.g. fix `dispatch-loop` before `filterargs-promoted` if `serve()` doesn't exist yet). For each fix:
   - Edit the file with the canonical pattern from CONVENTIONS.md (cite the section number in the edit-plan, not in code comments).
   - Don't add explanatory comments to the fixed code — the fix should look like the canonical pattern, not like a migration.
   - Don't squash unrelated changes into the upgrade — only the bytes the convention requires.

7. **`walkthrough-md-fresh` is always last.** After all other fixes, run `cd <dir> && go run . --doc md > WALKTHROUGH.md` to regenerate. If the regen produces no diff against the committed file, the fix wasn't needed (rare — usually means the audit was wrong; flag it).

8. **Verify before reporting:**
   - `cd <dir> && go build ./...` — must succeed.
   - For each fixed ID, do a one-shot grep to confirm the anti-pattern is gone (e.g. after `serve-srv-listenandserve`, grep for `http.ListenAndServe` and assert it's not in `main.go`).
   - If any fix didn't take, surface it as a manual follow-up rather than retrying blindly.

9. **Report:**
   - Fixed: `<id>` (file:line of the edit).
   - Skipped (manual): `<id>` (file:line, reason).
   - Build status.
   - Suggest re-running `/example-audit <name>` to confirm green (the user can verify independently).

## Canonical fix recipes

For each ID, the upgrade applies this exact transformation. (Read CONVENTIONS.md for the *why*.)

- **`logger-colorlogger`** — replace the existing logger construction in `serve()` with the canonical 5-rule `demokit.NewColorLogger(...)` from CONVENTIONS.md §2.
- **`serve-srv-listenandserve`** — replace `http.ListenAndServe(*addr, mux)` with `srv.ListenAndServe(server.WithStreamableHTTP(true))`. If the example also needed a custom mux for side endpoints, lift those routes into `server.WithMux(func(mux *http.ServeMux) { ... })` passed to `NewServer`.
- **`mux-withmux`** — extract any hand-rolled `http.Server{}` + custom mux into `server.WithMux(...)` on `server.NewServer(...)`. The side-endpoint handlers themselves don't change.
- **`filterargs-promoted`** — replace `flag.CommandLine.Parse(filterFlags(os.Args[1:]))` with the canonical `demokit.FilterArgs(...)` call (see CONVENTIONS.md §2). Delete the local `filterFlags` definition entirely. Bump `go.mod` to the demokit version that has `FilterArgs` if not already there (`v0.0.16`+).
- **`tui-helper`** — replace the `for _, arg := range os.Args[1:] { if ... "--tui" ... }` block with `if demokit.IsTUI() { demo.WithRenderer(tui.New()) }`.
- **`mode-helpers`** — delete local `tuiMode()` / `nonInteractive()` helpers; replace their call sites with `demokit.IsTUI()` / `demokit.IsNonInteractive()`.
- **`makefile-baseline`** — rewrite the Makefile to the four-target baseline from CONVENTIONS.md §6, **preserving any example-specific extras** (the user added them deliberately) under their existing section comment dividers.
- **`makefile-default-goal`** — append `.DEFAULT_GOAL := demo` to the Makefile if missing.
- **`dispatch-loop`** — wrap the existing `main()` body in the dual-mode dispatcher from CONVENTIONS.md §2. The current `main()` becomes either `serve()` or `runDemo()` based on whether it's the server or the walkthrough.
- **`readme-quickstart` / `readme-what-it-demonstrates` / `readme-where-to-look`** — insert missing sections at the canonical position (CONVENTIONS.md §5). Don't rewrite existing prose; only add what's missing. If the section exists but is in the wrong order, leave it (don't reorder hand-curated docs).
- **`ui-extension`** — add `server.WithExtension(&ui.UIExtension{})` to `server.NewServer(...)`. Add the `ext/ui` dependency to `go.mod` if missing.
- **`ui-bridge-template`** — add `{{ template "mcpkit-bridge" .Bridge }}` inside `<head>` of the HTML asset. If the asset is currently embedded as a static string, switch to `html/template` rendering.
- **`ui-typed-app-tool`** — replace `srv.RegisterTool(...)` with `ui.RegisterTypedAppTool(srv, ...)` for tools that ship UI.
- **`walkthrough-md-fresh`** — `cd <dir> && go run . --doc md > WALKTHROUGH.md`. Always last in the upgrade order.

## Principles

- **Audit/upgrade is a contract.** If a fix doesn't address a published audit ID, don't apply it. Side-quests belong in their own session.
- **Show the plan first.** Even mechanical edits go through the plan/confirm gate — the user may have local reasons to skip an item (mid-refactor, test in progress).
- **CONVENTIONS.md is the spec.** Never embed conventions in this skill's recipes that aren't in the doc. If you find yourself wanting to, update the doc first.
- **One example at a time.** Don't accept a list of names; the user runs the skill once per example. Bulk migrations are a separate workflow.
- **Don't rewrite prose.** README sections that exist but drift from the convention shape are out of scope — flag them as manual review, don't auto-edit narrative.
