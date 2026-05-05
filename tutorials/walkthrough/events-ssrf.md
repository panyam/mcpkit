# Events: webhook SSRF guard

<!-- STUB -->

> [!IMPORTANT]
> **Stub page.** Header is filled out so the graph and links stay accurate, but the body below is an outline only. Track progress in [INDEX.md](./INDEX.md).

> **Kind:** leaf · **Prerequisites:** [events](./events.md)
> **Reachable from:** [events](./events.md) Next-to-read
> **Spec:** Events SEP — webhook delivery (security) · **Code:** mcpkit's webhook delivery loop

## Prerequisites

- You know how webhook delivery works in events. → If not, read [events](./events.md).

## Context

Webhooks accept arbitrary URLs from clients, which makes them a server-side request forgery (SSRF) attack surface. mcpkit's webhook delivery dials each URL through a dial-time SSRF guard that's TOCTOU-safe under DNS rebinding. This page covers the threat model and the mechanism.

## What this page will cover

- Threat model: what an attacker can do with an unguarded webhook URL field
- Dial-time IP resolution + filtering against private/loopback/link-local ranges
- TOCTOU under DNS rebinding: why a second resolve at connect time is necessary
- No-redirects policy: why redirect chains are forbidden
- 256 KiB body cap — REJECT not TRUNCATE; 413 is non-retryable
- Interaction with the suspend-after-N-in-window policy

## Next to read

*(Terminal — return to [events](./events.md).)*
