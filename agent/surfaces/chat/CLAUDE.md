# agent/surfaces/chat — read before editing here

The terminal surface over `agent/host`. Formerly `cmd/agentchat`; the binary is still named
`agentchat`.

- **How to use it:** `README.md`
- **Why it is shaped this way, and the bugs behind it:** `NOTES.md`

## Traps

- **Glamour must never see embedded ANSI.** It garbles it into literal `[2m` sequences. Prose
  goes to glamour; tool, thinking, and footer output stays verbatim. The split happens in the
  surface observers, off the raw event stream.
- **Markdown renders once at commit, never per token.** Glamour mangles a half-written fence and
  flickers if re-run while streaming.
- **No charm/lipgloss may leak into `agent/host`.** Host stays surface-agnostic so the web
  surface can render the same `CmdResult`. `App.Dispatch` is data-only: an overlay yields a
  command *line* for the surface to dispatch, it never calls the host.
- **Never write to scrollback for anything that belongs in the managed live region** (A4). The
  status line in particular is not `Println`'d.
- **Server readiness checks must be a TCP probe**, `(exec 3<>/dev/tcp/host/port)`, never
  `curl GET /mcp` — a GET opens the SSE stream and never returns, hanging the launcher.
- **The notebook viewport clips, it does not wrap.** Anything added to a cell body has to go
  through `wrapCell` first.
- **Live renders are debounced (~40ms).** Re-rendering the whole transcript per token starves
  keystroke frames and looks like dropped input.
