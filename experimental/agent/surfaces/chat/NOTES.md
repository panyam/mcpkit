# agent/surfaces/chat — implementation notes

Why the terminal surface is shaped the way it is, and the bugs that shaped it. For how to *use*
agentchat (flags, keys, `--ui` modes, walkthroughs) see `README.md`.

Formerly `cmd/agentchat`; moved here 2026-08-05 alongside `agent/surfaces/web`.

---

## The layering rule this surface exists to prove

**Host concerns live in `agent/host`; only CLI concerns live here.** Anything a web surface would
also want belongs in the host. Only stdin/stdout plus bubbletea wiring belongs here.

Concretely: the slash-command registry, `ConnectionRegistry`, `HostEvent`/`Observer`, and every
`CmdResult` shape are host-layer. Zero charm/lipgloss may appear in `agent/host`. `App.Dispatch`
stays **data-only** — an overlay yields a command *line* that the surface dispatches; it never
calls the host directly.

This is what lets `agent/surfaces/web` render the same `CmdResult` as a side panel.

---

## Surfaces: `--ui auto|tui|notebook|plain`

Resolved by `uiMode` in `tui.go`.

- **`tui`** (default, inline): finished segments commit to the terminal's own scrollback via
  `tea.Println`; only the live turn and the input are in-frame. Native scroll, copy/paste,
  survives exit. `isBoundary(kind)` decides: `HostRunnerEvent` streams live, everything else
  commits.
- **`notebook`** (alt-screen, hand-rolled, not demokit): a `bubbles/viewport` owning its own
  scroll, over foldable cells. `nbObserver` splits the HostEvent stream into labeled cells at
  boundaries, reusing `NewTerminalRenderer` for bodies. INS/NAV modes (esc→NAV; jk select, space
  fold, g/G ends).
- **`plain`**: no markdown rendering at all.

The Charm stack (bubbletea, bubbles, lipgloss, glamour) is the first external UI dependency;
recorded as a gap in `STACK_GAPS.md`.

---

## Markdown: glamour at commit, never while streaming

Both interactive surfaces render an assistant's *finished* prose as markdown once at commit,
while streaming stays raw. Glamour mangles a half-written fence and flickers if re-run per token.

Kept **CLI-only** (`markdown.go`, `mdRenderer` wrapping `charm/glamour`). Host `render.go` is
untouched: glamour emits terminal ANSI a web surface would not want, so the prose-vs-tool split
is done in the **surface observers** off the raw event stream. `EventTextDelta` is a prose block
and goes to glamour; tool, thinking, and turn-footer output is a meta block and stays verbatim.

> **Glamour must never see embedded ANSI or it garbles it into literal `[2m` sequences.**

Inline `tuiObserver.fold()` (split out of `On` for testability) accumulates `[]segBlock`. The
notebook keeps raw `body` plus glamoured `rendered` per cell, with the dim footer split out of
the glamour input in `assistantCell`. `NO_COLOR` falls through to raw passthrough.

**Copy tradeoff**: styled scrollback copies as glamour's *rendered* text (`•` not `-`, no fences,
width-padded), not source. ANSI is stripped by the terminal on select, so it is readable rather
than binary.

`glamour v0.10.0` keeps lipgloss on v1 (a v1.1.1 pseudo-version), so there is no v2 conflict.

---

## Four notebook streaming bugs (PR 1128)

All four were found running kitchen-sink. The inline `tui` surface is immune to the first three
by construction.

1. **Wide lines were clipped.** The `bubbles/viewport` clips horizontally; it does **not** wrap.
   `wrapCell` (`ansi.Wrap`, ANSI- and grapheme-aware, hard-breaking long tokens like JSON) wraps
   every cell body to `contentWidth()-2` before `indentBlock`. This also fixed a latent **+2
   overflow**: glamour wrapped prose to the full width and then the 2-space indent pushed it past
   the edge. `ansi` became a direct dependency because of this.

2. **Alternate keystrokes appeared to be swallowed while streaming.** Input was never lost.
   Streaming sent one `nbLiveMsg` per token and each re-rendered the *whole* transcript
   (`vp.SetContent(renderCells())`), starving keystroke render frames — you would type ABCDE and
   see A, ABC, ABCDE. Fixed by coalescing live renders behind a ~40ms `liveFlushMsg` debounce
   tick (`liveScheduled` gate, ~25fps). The wrap fix made `renderCells` heavier, so these two
   pair.

3. **Literal `[2m` control codes in thinking output.** Reasoning is dimmed with ANSI by the
   shared terminal renderer, and the notebook folded it into the *glamoured* assistant prose.
   Fixed by giving reasoning its own verbatim `thinking` cell, cut at `EventThinkingBegin` and
   closed at `EventThinkingEnd` in `nbObserver.render`, like tool cells. The inline surface was
   already fine, since thinking was a verbatim `segBlock` there.

4. **`Ctrl+O` raw markdown toggle.** The notebook toggles the whole transcript rich↔raw in place
   (cells keep `body` raw plus `rendered` glamoured, with a `RAW` status tag). The inline surface
   **cannot** toggle, because native scrollback is immutable, so it dumps the last reply's raw
   markdown to scrollback (`tuiObserver.lastRaw` via `rawProse`, prose blocks only) with a hint
   pointing at `--ui notebook`. The `Raw` binding is centralized in `keys.go`.

---

## Overlays and the focus stack

Typing `/mcp` (an alias of `/servers`) or `/sessions` opens a modal dialog in the bottom region
instead of printing a static list: selectable rows, per-row actions, Enter acts, Esc dismisses,
↑↓/jk move.

The widget lives **entirely here** (`overlay.go`), with zero charm/lipgloss in `agent/host`.

**The reusability seam** is the point: a `focusLayer` interface (`handleKey` / `setWidth` /
`View`) plus a `modalHost` embed carry "open, route keys to the modal, close" **once**, so both
TUI surfaces share it rather than each special-casing `overlay != nil`. `overlayFor(CmdResult)`
centralizes which `CmdKind`s open a dialog. What stays per-surface is genuine layout: inline is
input alone, notebook is viewport plus input.

**Dialog stack** (#1063 C4, PR 1124): `modalHost` holds a `stack []focusLayer` (`push` / `pop` /
`top` / `clear`). `setWidth` fans out to **all** layers so a revealed parent is pre-sized. An
overlay opened from within another nests, and Esc pops one level. `openOverlayMsg` pushes;
`closeOverlaysMsg` clears the whole stack, sent by the dispatch goroutine on a non-overlay result
(resume, reconnect) to return to the prompt.

**The push-always rule is correct because the input is blurred while any modal is open** — a
command cannot be typed mid-overlay, so every `openOverlayMsg` is unambiguously a nest.

The base view was deliberately **not** made a `focusLayer`: its key handling does not fit the
`handleKey → {Dismiss, Line}` shape, and forcing it would be low-value churn. Dimmed-base
compositing (A4) remains on the #1063 checklist.

**Per-server tool view** (#1117, PR 1119): a `t` action on a ready server row opens a read-only
nested overlay of that server's tools. The backing is data-only —
`MultiSource.SourceTools(ctx, id)` returns one source's unqualified tools (`found=false` for an
unknown id is app state, not an error), and `App.ServerTools` looks it up **by the server's own
id in the aggregate**, so it returns only that server's tools respecting its `Allow` filter, and
never meta-tools or sub-agents (they register under other ids). `/servers tools <name>` yields a
distinct `CmdServerTools` kind, so `/tools` stays a plain print.

---

## Status line

Both surfaces show `model <active> · session <id> · ctx <in>↑ <out>↓ tok`, kept in the managed
live region and **never `Println`'d to scrollback** (A4). `host.App.ModelLabel()` supplies the
active connection; the observers send a `usageMsg` on `HostTurnDone`; `formatStatus` is the
pure, tested renderer.

`--context-window <tokens>` adds a `N% context left` gauge. mcpkit has no per-model window table,
so without the flag it shows raw counts.

---

## Color accessibility

`color.go` holds **one** color decision that drives every surface.
`resolveColorEnabled(flagNoColor, look)` precedence: `--no-color` flag → `NO_COLOR` (any value,
per no-color.org) → `TERM=dumb`; otherwise on, with termenv auto-degrading truecolor→256→16→mono.

`applyLipglossProfile(false)` pins lipgloss to the **Ascii** profile so every Faint/Bold/Reverse/
Foreground renders as deterministic plain text. Called once at startup.

`accentColor = lipgloss.AdaptiveColor{Light:"4", Dark:"6"}` (blue on light, cyan on dark,
16-color-safe) is the one foreground, used for the overlay title and the selected row.

**Glyph, not just color**: the notebook nav selection carries a glyph marker rather than relying
on `Reverse` alone, so it survives `NO_COLOR` and dumb terminals.

`look` is injected (`os.LookupEnv` in production, a stub map in tests) so precedence is
verifiable without mutating the process environment.

---

## Prompt editing

- The bubbles textarea already binds most readline keys, but **word navigation is Meta-only**
  (`alt+←/→`), which is dead on default macOS terminals. `newPromptArea` adds `ctrl+←/→`.
  `/keys` prints the cheatsheet.
- **Enter submits** (both surfaces intercept it), so newline is rebound onto **`ctrl+j`**
  (reliable — keyLF is not Enter's keyCR), plus shift+enter on kitty-protocol terminals and
  alt+enter.
- **Notebook auto-grow** (`--notebook-max-lines`, default 20): the textarea's `repositionView`
  runs *during* `ta.Update` with the OLD height, so a just-inserted newline over-scrolls and
  hides line 1. Set the input to its full height budget *before* the edit, then shrink to the
  actual line count.

---

## Launcher gotchas that cost real time

1. **Server readiness must be a TCP probe, not `curl GET /mcp`.** A GET to an MCP endpoint opens
   the server's SSE stream and never returns, hanging the launcher forever. Use
   `(exec 3<>/dev/tcp/host/port)`. This bit both `run.sh` and `scripts/playground.sh`; the latter
   **still has the hanging-curl form**.
2. **A backgrounded demo server's logs must be redirected off the terminal** or they clobber the
   inline TUI's managed region.
3. **`go run` with the inline TUI renders fine** — verified under a pty. An earlier
   "build the binary instead" fix was a wrong guess; the real bug was always the hanging curl.
