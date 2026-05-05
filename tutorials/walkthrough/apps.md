# Apps (`ext/ui/`)

<!-- STUB -->

> [!IMPORTANT]
> **Stub page.** Header is filled out so the graph and links stay accurate, but the body below is an outline only. Track progress in [INDEX.md](./INDEX.md).

> **Kind:** root *(off-mainline)* · **Prerequisites:** [bring-up](./bringup.md), [transport-mechanics](./transport-mechanics.md), [extension-mechanisms](./extension-mechanisms.md)
> **Reachable from:** [extension-mechanisms](./extension-mechanisms.md) Next-to-read
> **Spec:** [`docs/APPS_DESIGN.md`](../../docs/APPS_DESIGN.md), [`docs/APPS_HOST.md`](../../docs/APPS_HOST.md), [`docs/APPS_ONBOARDING.md`](../../docs/APPS_ONBOARDING.md) *(mcpkit-specific)* · **Code:** `ext/ui/`

## Prerequisites

- You know the bring-up phases and have a session model in your head. → If not, read [bring-up](./bringup.md).
- You can read messages off the wire on each transport. → If not, read [transport-mechanics](./transport-mechanics.md).
- You know what counts as a library-architecture extension (thin protocol surface, bulk in host code). → If not, read [extension-mechanisms](./extension-mechanisms.md).

## Context

Apps is mcpkit's host-side architecture for managing MCP servers as live components in a JavaScript runtime. Most of the surface is host-architecture — AppHost lifecycle, Bridge JS, ServerRegistry — with a small protocol seam where MCP clients connect to servers. This page covers the architecture and how it relates to the protocol layer.

## What this page will cover

- AppHost: the lifecycle manager. Spins up clients, tracks connection state, mediates between JS host and MCP servers.
- Bridge JS: the web-side runtime that JS code in a browser uses to talk to MCP servers via the host.
- ServerRegistry: tracks live servers, their capabilities, their tool catalogs.
- The lifecycle gotcha: `Client.Connect()` before `AppHost.Start()`. `AppHost.Close()` only closes the bridge.
- CORS for browser clients: `Mcp-Session-Id` in Allow-Headers and Expose-Headers, `DELETE` in allowed methods.
- Demokit and the non-interactive vs. interactive paths.

## Next to read

*(Terminal — apps is off the main learning path. For the implementation reference, see `docs/APPS_DESIGN.md`.)*
