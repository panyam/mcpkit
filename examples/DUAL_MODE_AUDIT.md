# Dual-mode audit — examples on the SEP-2575 stateless wire

Tracks [issue 478](https://github.com/panyam/mcpkit/issues/478): every example
ships with `server.stateless.DefaultMode = ModeDual`, so it *accepts* the
SEP-2575 stateless wire on upgrade. This audit answers the harder question:
does each example actually **work** when driven over the stateless wire, not
just compile?

## How this is verified

- **Auto-drivable examples** run in `just verify-dual` (`scripts/verify-dual.sh`):
  each walkthrough runs non-interactively against a server on the legacy wire
  AND the stateless wire (`--wire=legacy` / `--wire=stateless`, from issue 824),
  and must produce no failure markers on either. demokit's non-interactive
  renderer always exits 0, so the signal is the output: the common helpers
  print an indented `ERROR:` / `UNEXPECTED:` on a failed step.
- **Everything else** is graded by inspection (interactive flows, UI apps,
  infra examples) and noted below with the reason it isn't auto-driven.

## Verdict matrix

| Example | legacy | stateless | Verdict |
|---|:---:|:---:|---|
| `tasks-v2` | ✅ | ✅ | dual-clean (fixed by PR 832 — Mcp-Name on task ops) |
| `mrtr` | ✅ | ✅ | dual-clean (MRTR via `ctx.RequestInput`; stateless parity since `0202c926`) |
| `file-inputs` | ✅ | ✅ | dual-clean (fixed by PR 834 — file-input validation on stateless) |
| `list-ttl` | ✅ | ✅ | dual-clean (plain tools; SEP-2549 is server-side) |
| `tasks` (v1) | ✅ | n/a | deprecated, legacy-only (v1 is being removed; not targeted at stateless) |
| `skills` | ✅\* | ✅ | works; drives its client wire via an interactive `demokit.Choice`, so `--wire` can't steer it (\*legacy needs the matching client mode) |
| `auth/*` | ✅ | ✅ | wire-verified by `make smoke-wire`; tool flows are token-gated, not in verify-dual |
| `elicitation` | ✅ | — | interactive by design — `-32042` consent-URL flow (SEP-2643) needs a browser approval step |
| `fine-grained-auth` | ✅ | — | interactive — needs a live Keycloak + browser token flow |
| `apps/*` | ✅ | — | UI / AppHost-mediated; handlers use `ctx.Elicit` / `ctx.Sample` (server-initiated push, forbidden on stateless). MRTR migration tracked in issue 835 |
| `events/*`, `whole-enchilada` | ✅ | — | infra (Docker/tokens); not auto-driven here |
| `host/*` | ✅ | — | not `--wire`-threaded yet (orchestrates other servers); not in verify-dual |
| `protogen` | ✅ | — | experimental / outdated; out of scope |

## Gaps the audit surfaced (both fixed)

Both were the same class — a dispatch feature implemented in the legacy path
(`Dispatcher.handleToolsCall`) but not mirrored in the stateless backend
(`callToolForStateless`) — and neither was caught by conformance, because the
task / file-input suites run on the legacy wire while the stateless suite uses
the cart fixture (no task ops, no file inputs). The intersection was untested.

- **PR 832** — task ops (`tasks/get|update|cancel`) require the SEP-2243
  `Mcp-Name: <taskId>` routing header. On the legacy wire the session covered
  routing; on stateless the client never set it (`core.DeriveMcpName` had no
  task cases, and the helpers passed typed structs the deriver can't read).
- **PR 834** — SEP-2356 file-input size/MIME validation was skipped on the
  stateless `tools/call` path, so invalid files reached the handler instead of
  returning `-32602`.

## Why some examples are legacy-only

The protocol forbids server-initiated push (`ctx.Sample` / `ctx.Elicit`) on the
stateless wire — there is no session to correlate the round-trip against. Tools
that need input/sampling MUST use the MRTR pattern (`ctx.RequestInput` +
`core.NewSamplingInputRequest` / `core.NewElicitationInputRequest`), which
threads the correlation state explicitly through the request/response cycle (see
`mrtr`). Examples still using direct push (`apps/*`) are therefore legacy-only
until migrated (issue 835). See the "Dual-mode posture" section in
[`CONVENTIONS.md`](CONVENTIONS.md) for what new examples should do.
