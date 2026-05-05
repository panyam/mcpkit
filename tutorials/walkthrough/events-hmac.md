# Events: webhook HMAC (Standard Webhooks)

<!-- STUB -->

> [!IMPORTANT]
> **Stub page.** Header is filled out so the graph and links stay accurate, but the body below is an outline only. Track progress in [INDEX.md](./INDEX.md).

> **Kind:** leaf · **Prerequisites:** [events](./events.md)
> **Reachable from:** [events](./events.md) Next-to-read
> **Spec:** Events SEP — webhook delivery (HMAC); [Standard Webhooks](https://www.standardwebhooks.com/) · **Code:** mcpkit's webhook signing path

## Prerequisites

- You know how webhook delivery works in events and that the client supplies a `whsec_` secret. → If not, read [events](./events.md).

## Context

Webhook receivers can't trust the source unless they verify the signature. mcpkit follows [Standard Webhooks](https://www.standardwebhooks.com/) for HMAC-based authentication: client supplies a `whsec_` prefixed secret at subscription time, server signs each delivery with HMAC-SHA256 over a canonical string, receiver verifies before processing.

## What this page will cover

- The Standard Webhooks signing scheme (msg-id + timestamp + body → HMAC-SHA256 → base64)
- Why client-supplied secret (server never generates): the receiver knows what to verify against without out-of-band coordination
- Header shape: `webhook-id`, `webhook-timestamp`, `webhook-signature`
- Replay protection via timestamp window
- Key rotation patterns (the receiver supports multiple active secrets)
- Why `whsec_` prefix is mandatory (interop signal)

## Next to read

*(Terminal — return to [events](./events.md).)*
