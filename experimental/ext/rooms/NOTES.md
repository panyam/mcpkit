# ext/rooms — implementation notes

Why the bridge is shaped the way it is. Usage is in the README; the design discussion is #1581.

## Page tools live in the server's one registry, filtered per session

The server has a single tool registry shared by every session, and no per-session tool list. The
bridge registers each page tool there once and installs a `tools/list` middleware that removes page
tools of rooms the session has not attached to. The call handler re-checks attachment, so a session
that names a tool it was never shown is refused.

This has a cost on the stateless wire, where a handler that skips `InvokeWithMiddleware` bypasses
the chain (C7). The bridge refuses to attach without a session, so nothing is callable there, but
an unfiltered `tools/list` would show every room's tool names.

A name is one definition bridge-wide, which is why the first page to register a name fixes its
schema. That is the simplest answer to #1581's open question about mismatched page versions.

## Agreement and pairing are two things

#1581's first acceptance case needs both: a session paired with page 1 that can still call page 2
by name. So a page *agreeing* to a session (switch or code) is the authorization, and *paired* is
the one page the session's calls default to, the page that agreed most recently. Targeting rules 2
and 3 differ only because of this: rule 3 applies once the paired page has gone and exactly one
agreeing page is left.

## The scope gate is the bridge's own

ext/auth's `NewScopeMiddleware` would do the job, but constraint C4 forbids one extension module
requiring another, and leaving the gate to the host means acting tools are ungated by default. So
`scopeGate` evaluates `ScopeChallenge` on the bridge's own tools before dispatch, with the same
403 and `core.InsufficientScopeChallenge` header ext/auth produces. A host that also installs
ext/auth's middleware gets the same answer twice.

## Pages are not initialized

The bridge sends `tools/list` without an `initialize`. The room already authenticated both peers,
and the page's capabilities are fixed by this protocol. A peer is in the roster only once its
`tools/list` has succeeded, which is also how a second bridge or any other non-page peer stays out.

## A page cannot address a bridge it has not heard from

Pages learn the bridge's peer id from its first request, then send it the state it missed (label,
the agents switch). A page listed after a session attached is sent that session's pairing request
when it is first listed, since it was not in the roster at attach time.

## Session close comes from the server's hook

Slice 1 shipped before the server had a session-close hook for extensions, so `EndSession` was
exported for hosts to call and a pairing whose session vanished sat in memory until its page left.
#1587 added `Server.OnSessionClose`, which `Register` now hooks; `EndSession` stays exported for
hosts that end sessions some other way. The scope gate likewise builds its 403 with
`ScopeChallenge.AuthError` from #1587 rather than by hand. It still can't use `ext/auth`'s
middleware (C4), but the response comes from the same core code.
