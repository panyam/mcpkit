# Events: canonical-tuple subscription identity

<!-- STUB -->

> [!IMPORTANT]
> **Stub page.** Header is filled out so the graph and links stay accurate, but the body below is an outline only. Track progress in [INDEX.md](./INDEX.md).

> **Kind:** leaf · **Prerequisites:** [events](./events.md)
> **Reachable from:** [events](./events.md) Next-to-read
> **Spec:** Events SEP — subscription identity · **Code:** mcpkit's subscription id derivation

## Prerequisites

- You know what a subscription is in the events model. → If not, read [events](./events.md).

## Context

Subscriptions are identified by a server-derived id, not a client-supplied one. The id is derived from a **canonical tuple** of `(principal, delivery.url, name, params)` — same tuple = same subscription, even across reconnects. This page covers why it works that way, what counts as part of the tuple, and the rules that fall out.

## What this page will cover

- The canonical tuple: `(principal, delivery.url, name, params)`
- Why principal is part of the tuple: same delivery URL + same name from a different principal = different subscription
- Server-derived id format: `sub_<base64-of-hash>`, non-load-bearing (clients shouldn't parse)
- The three rules:
  1. **No client id** — client doesn't pick the subscription id; server derives it
  2. **Auth required** — anonymous principal not allowed
  3. **Client-supplied secret** — `whsec_`-prefixed (used by webhook signing)
- Idempotency: re-subscribing with the same canonical tuple returns the same id, doesn't double-deliver
- Refresh semantics: how a suspended subscription gets reactivated

## Next to read

*(Terminal — return to [events](./events.md).)*
