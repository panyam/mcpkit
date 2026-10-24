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

## Page events go out through a hook, not through ext/events

#1581 makes the bridge an events server for its rooms. Constraint C4 keeps this module from
requiring experimental/ext/events, so the bridge only validates a page's event and calls
`WithPageEvents`. experimental/integrations/roomsevents, a module with no `ext` path segment, does
the SEP-3415 side and asks the bridge `Attached` and `PageAgreed` to decide who hears what. An event
the bridge drops (bad name, over 16 KiB, from a page it has not listed yet) never reaches the hook.

## Session close comes from the server's hook

Slice 1 shipped before the server had a session-close hook for extensions, so `EndSession` was
exported for hosts to call and a pairing whose session vanished sat in memory until its page left.
#1587 added `Server.OnSessionClose`, which `Register` now hooks; `EndSession` stays exported for
hosts that end sessions some other way. The scope gate likewise builds its 403 with
`ScopeChallenge.AuthError` from #1587 rather than by hand. It still can't use `ext/auth`'s
middleware (C4), but the response comes from the same core code.

## The room resource answers its own subscriptions

The fallbacks' `rooms://events/{resource}` could have used the server's subscription registry and
`NotifyResourceUpdated`. That broadcasts to every subscriber of the URI, and any session can
subscribe to any URI, so a session that never attached would hear when alice's page did something.
The bridge answers `resources/subscribe` and `unsubscribe` for its own URIs in a middleware instead,
refuses a session that isn't attached, and sends `resources/updated` itself, only to sessions the
emitting page agreed to. Reads and `next_request` apply the same `hears` check, so all three paths
share one rule with the event forwarder in roomsevents.

## Reverse pairing: who makes the code, and who spends it

The page makes the code (`OfferPairingCode`), because it is what the page's user reads off the
screen. That lets a careless page offer a weak one, so the bridge refuses anything under six
characters of the pairing alphabet and limits a session to five wrong guesses a minute. A code is
spent twice over: the bridge deletes it when a session pairs, and the page withdraws it from every
bridge when it hears so. The bridge's delete is the one that counts, since a page in another language
may not withdraw; `TestPairing_CodeIsSingleUse` uses a hand-rolled page that never does.

A page sends its standing offer when a bridge greets it, which can reach the bridge before the
bridge has finished listing the page's tools. So offers are recorded from any page in the roster, and
`pair` requires the page to be listed when the code is spent.

## Agreement moved onto the page (#1610)

Agreement was per MCP session: a pairing granted a session, and ended with it. The stateless wire
has no session, and several bridges in a room (one per replica) would each have needed the same
pairing table. So the page holds it now, as grants to principals (subject, optional client), and
sends the full list to every bridge that lists it and again whenever it changes. A bridge keeps only
a copy, which it updates itself the moment `pair` or an accepted code makes a grant, so a call made
straight afterwards works before the page's own grants arrive; the page's next list replaces it.

The page only adds a grant for a code it offered or accepted. A peer in the room that sends
`paired_by_code` for anything else is ignored, since otherwise any peer with a relay token could
grant itself, or anyone, the page.

Two ordering rules came out of the tests. A page greets a bridge (label, focus, switch, grants,
offer) before answering its `tools/list`, so by the time the bridge has listed the page it already
has its state; the bridge handles one peer's messages in order. And a page sends `focusedAt` with
its focus, so a tab its user focused before a bridge joined still counts as focused last; without
it, the first stateless call into a room picked no default.

Joining a room on a call waits up to two seconds for the pages already there to be listed, so a call
naming a room the bridge has never joined finds them. Another bridge in the room never answers
`tools/list`, which is why the wait is capped rather than complete.

## Declared tools are what make the stateless wire work (#1610)

A stateless `tools/list` reads the server's one registry and skips the middleware chain (#1355), so
the per-session filter that hides other rooms' tools never runs there. Rather than wait on #1355,
`WithPageTools` puts a fixed catalog in the registry at `Register`, the same for every caller, so
there is nothing to filter. Without it, the dynamic registry stays a session-wire feature, and the
page tool handler refuses a call with no session instead of serving whatever pages happened to
register.

The tests run each acceptance case on both wires, and the agent helper asserts it really is on the
wire the test names (`UsingStatelessWire`), since a stateless client that quietly fell back to a
session would pass everything and grade nothing.

## Presence names principals, and is rate-limited by the bridge (#1616)

A presence report carries `{subject, client}` and never a session id, for the same reason agreement
moved to the page: the stateless wire has no session, and a tab user has no use for one. The
bridge keeps what it has seen per page, plus who attached to each room, so a tab listed after an
attach still hears of it. It reports again only when an agent is new to the page, starts acting,
or was last reported over 30 seconds ago, since a report per call would double the relay traffic
of a busy tab. The page owns the TTL: each bridge only knows its own callers, and the page is the
one place all of them meet.

## The caller, and why TrustBridges exists

The bridge sends the caller's principal in the call's `_meta` (`MetaCaller`), so a page can apply
its own rules per caller. But a page answers any peer in its room by default, and any peer holding
a relay token for the room could send it a `tools/call` with any caller it likes, past the bridge's
agreement check entirely. That was true before #1610 too; passing a caller just made it matter more.
`TrustBridges` limits a Go page to peers whose relay subject is one of the host's bridges, which both
transports stamp on each message. The TS client can't do the same yet, because massrelay's
`CollabClient` drops `identity` when it reports peers.
