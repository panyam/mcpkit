# List-TTL (SEP-2549)

<!-- STUB -->

> [!IMPORTANT]
> **Stub page.** Header is filled out so the graph and links stay accurate, but the body below is an outline only. Track progress in [INDEX.md](./INDEX.md).

> **Kind:** leaf · **Prerequisites:** [notifications](./notifications.md), [extension-mechanisms](./extension-mechanisms.md)
> **Reachable from:** [notifications](./notifications.md) Next-to-read, [extension-mechanisms](./extension-mechanisms.md) Next-to-read
> **Spec:** [SEP-2549 (list-TTL)](https://modelcontextprotocol.io/specification/2025-06-18) · **Code:** `core/list_ttl_test.go` and the `*int` + omitempty pattern wherever it's applied to list responses

## Prerequisites

- You know `list_changed` and that it's a "refetch when you care" hint. → If not, read [notifications](./notifications.md).
- You know the four extension surfaces and that this is a `_meta`-only extension. → If not, read [extension-mechanisms](./extension-mechanisms.md).

## Context

`list_changed` notifications are best-effort. Capabilities can be flaky; some servers don't advertise them; clients may miss notifications across reconnects. **List-TTL (SEP-2549)** gives the server a way to attach a cache-lifetime hint to list responses: how long the client may treat the list as fresh. Pure `_meta`-only extension — no new methods, no new capabilities, no new notifications.

## What this page will cover

- The three-state semantics: `nil` (no guidance, default), `&0` (do not cache), `&N>0` (fresh for N seconds)
- Why `*int` + `omitempty` in Go — distinguishing nil from explicit zero in JSON
- Where the TTL field lives in list responses (`tools/list`, `prompts/list`, `resources/list`)
- The server's role: be honest about lifetime
- The client's role: cache up to TTL, refetch on TTL expiry, also refetch on `list_changed` (TTL is a backup, not a replacement)
- The `make testconf-list-ttl` 5-scenario conformance suite

## Next to read

*(Terminal — return to [notifications](./notifications.md) for the broader cache-invalidation model.)*
