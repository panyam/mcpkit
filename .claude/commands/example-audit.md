Audit an mcpkit example for compliance with `examples/CONVENTIONS.md`. Read-only — reports drift, never edits.

## Args

- `$1` — example name (e.g. `discord-events`) or relative path (e.g. `examples/events/discord`). If missing, ask.

## Steps

1. **Read `examples/CONVENTIONS.md`.** It is the spec; this skill is just the runner. If the doc has changed since this skill was written, follow the doc.

2. **Resolve the target directory:**
   - Try `examples/$1`, then `examples/events/$1`, then treat `$1` as a relative path.
   - Refuse if the directory doesn't exist.

3. **Detect example type** — UI vs non-UI:
   - **UI**: contains an `*.html` file AND `main.go` calls `server.WithExtension(&ui.UIExtension{...})`. Run the UI checklist (CONVENTIONS.md §8 second block).
   - **Non-UI**: everything else. Run the non-UI checklist (CONVENTIONS.md §8 first block).
   - If ambiguous (e.g. `*.html` present but no UI extension), report it as a finding and audit as non-UI.

4. **Walk the checklist.** For each item, attempt to verify automatically:
   - File presence: `Read` / `LS` the path.
   - Code shape: `grep` for the canonical symbol or anti-pattern.
   - Doc shape: read the file and check section headers.
   - Build precondition: run `cd <dir> && go build ./...`. If it fails, emit `build-broken` as the sole finding and skip checks that depend on a working build (especially `walkthrough-md-fresh`).
   - WALKTHROUGH.md drift: run `cd <dir> && go run . --doc md` and diff against the committed file.

5. **Emit findings** in the format under "## Output format" below. Use the **canonical IDs from CONVENTIONS.md §8** (one per checklist item) so `/example-upgrade` can match them deterministically.

6. **Do not edit any files.** Do not run `make readme`, do not run `go fmt`, do not run `make tidy`. The audit's only side effects are read operations + the `--doc md` build attempt in step 4.

7. **Final line:** print exactly `Audit complete: <N> pass, <M> fail` so a follow-up `/example-upgrade` invocation can detect a recent audit in conversation context.

## Output format

Headed Markdown so it's both human-readable and stable enough for `/example-upgrade` to parse from context. One block per finding.

```
## Audit: examples/<path>

**Type:** non-ui | ui
**Result:** <N> pass, <M> fail

---

### [FAIL] <stable-id>  (CONVENTIONS.md §<n>)
- **Where:** <file>:<line>  (or `missing` if the file/section is absent)
- **Found:** `<the offending code or text>`  (omit if missing)
- **Expect:** `<the canonical shape>`
- **Fix:** <one sentence; /example-upgrade expands>

### [PASS] <stable-id>
- (no body — pass items are one-liners)

---

Audit complete: <N> pass, <M> fail
```

### Stable IDs

The canonical list lives in `examples/CONVENTIONS.md` §8. This skill enforces
exactly that set — if you find yourself emitting an ID not in the doc, stop
and update the doc first.

Items not applicable to a given example are simply omitted from the output
(do not emit `[N/A]` lines — keeps the output skimmable). The `build-broken`
precondition, when triggered, is emitted alone and the rest of the checklist
is skipped.

## Principles

- **Read-only, no exceptions.** This skill never writes a file, never runs `go fmt`, never edits the Makefile. If the user wants fixes, they run `/example-upgrade`.
- **Stable IDs are a contract.** `/example-upgrade` parses these from context to know what to fix; renaming an ID is a breaking change to the audit/upgrade pipeline.
- **CONVENTIONS.md is the spec.** If a check here drifts from the doc, the doc wins — fix the skill.
- **Don't audit what isn't there.** If an example legitimately doesn't use a feature (e.g. no side endpoints → `mux-withmux` is N/A), omit the check rather than failing it.
- **Surface the evidence.** Every `[FAIL]` must include `Where:` so the user (or upgrade skill) can navigate to the offending code immediately.
