# MCP Request Flow Walkthrough

A journey-shaped tour of what actually happens when an MCP client and server talk to each other — wire bytes, dispatch, the bidirectional contract, transport variations, and how all of it composes.

Complementary to the [official MCP spec](https://modelcontextprotocol.io/specification/2025-06-18). The spec is reference-shaped (feature-by-feature, normative shapes); this walkthrough is journey-shaped (follow a request, see every layer it touches, in the order it touches them).

This is a **working document, built incrementally and conversationally** — one question at a time. New roots get added as the discussion reaches them.

## Where to start

Each **root** is a self-contained chunk: read its preconditions, walk through it, end with a known set of invariants. Pick the ones you care about — you don't have to read them all.

Recommended reading order for someone new to the material:

1. **[Bring-up: from host to live session](./bringup.md)** — *root, foundational*  
   Server selection, transport selection, connection establishment, auth, the `initialize` handshake, capability negotiation. *End-state: a session is live, transport is chosen, auth is resolved, protocol version + capabilities are locked.*

2. **[Transport mechanics: stdio vs. streamable HTTP](./transport-mechanics.md)** — *root, foundational*  
   What the wire actually looks like per transport. The SSE "upgrade" demystified, the standing-GET back-channel, JSON-RPC correlation, per-direction ID spaces, the reverse-call origination constraint. *End-state: you can read messages off the wire on either transport and follow the correlation model.*

*(More roots get appended here as the conversation reaches them — per-request anatomy, reverse-call mechanics, notifications, tasks, …)*

## Other entry points

- **[STRUCTURE.md](./STRUCTURE.md)** — how this walkthrough is organized: the DAG model, the root contract (Preconditions / Body / End-state / Leads to), conventions for note blocks, branch points, spec/code anchors, target-shape tracking. Read this if you want to understand *why* pages look the way they do, or if you plan to author one.
- **[INDEX.md](./INDEX.md)** — single-page projection of the whole graph. Every page, its preconditions, end-state, successor pointers, and mid-journey branch points — in one table, with a full mermaid render. Read this to see the shape of what's covered (and what's planned) without opening individual files.
