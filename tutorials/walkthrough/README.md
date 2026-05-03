# MCP Request Flow Walkthrough

A journey through what actually happens when an MCP client and server talk to each other — the bytes on the wire, the dispatch through the server, the bidirectional contract, and how all of it changes per transport.

This is **complementary** to the [official MCP spec](https://modelcontextprotocol.io/specification/2025-06-18), not a replacement. The spec is reference-shaped: feature-by-feature, normative shapes, capability flags. This walkthrough is journey-shaped: follow a request, see every layer it touches, in the order it touches them.

## How this is organized

We model MCP as a **DAG of mechanisms**, level-ordered:

- **Level 0** — "a thing happens between client and server"
- **Level 1** — the universal anatomy that every request, notification, or response traverses
- **Level 2+** — each L1 node opens into its own sub-DAG (transports, dispatch internals, reverse calls, tasks, …)

A "spine" or "journey" is just a *path query* through the DAG. We don't author spines; we author nodes, and any spine you care about renders as a sequence of node references.

Every page declares its place in the graph so you can enter from anywhere.

> [!NOTE]
> This document is being built **incrementally and conversationally** — one question at a time. The current map below only lists the nodes we've actually walked. It will grow.

## Conventions

### Note blocks (textbook-style sidenotes)

GitHub-rendered Markdown doesn't support true marginalia (notes alongside the main text), but [GitHub Alerts](https://docs.github.com/en/get-started/writing-on-github/getting-started-with-writing-and-formatting-on-github/basic-writing-and-formatting-syntax#alerts) render as styled callout boxes — the closest equivalent to textbook sidenotes:

> [!NOTE]
> Authorial commentary, "see also" pointers, brief tangents.

> [!TIP]
> Reader observations / annotations. Use this voice when adding hand-written notes from working through the material.

> [!IMPORTANT]
> Spec constraints that are easy to miss in implementation.

> [!WARNING]
> **Target-shape gap (extension)** — current mcpkit differs from the converged target by addition. Will be reconciled without breaking changes.

> [!CAUTION]
> **Target-incompatible (replacement)** — the converged target *replaces* (rather than extends) the current implementation. Will require breaking changes when the migration lands.

For genuinely tangential remarks, use [Markdown footnotes](https://docs.github.com/en/get-started/writing-on-github/getting-started-with-writing-and-formatting-on-github/basic-writing-and-formatting-syntax#footnotes)[^1] — less visually disruptive than a callout.

[^1]: Like this. Renders as a clickable superscript with a back-link from the footer.

### Spec & code anchors

Each page links to:

- **Spec anchor** — the normative MCP spec section
- **Code anchor** — relevant `path/to/file.go` in mcpkit (line numbers omitted on purpose; they rot)

External spec links happen at the *node* level. Inside a flow, links stay on-page so reading doesn't break. Click into a node only when you need normative detail.

### The root contract

A **root** is a self-contained walkthrough that establishes a set of invariants downstream pages can assume. Every root follows the same four-part contract:

1. **Preconditions** — what must be true going in. Stated explicitly at the top, with a *"if not, read [X]"* guard pointing at the root that establishes each missing precondition. A foundational root says "none — foundational."
2. **Body** — the walkthrough itself.
3. **End-state** — what is true after reading. Listed as bullets in a section near the end. Downstream roots may assume any of these without re-deriving.
4. **Leads to** — which other roots build on this end-state. Pointers, not exhaustive.

This is dependency tracking for documentation. Instead of every page restating preliminaries, a page declares its preconditions and moves on. As more roots get written, derived pages get *shorter*, not longer.

> [!NOTE]
> A root is a *self-contained chunk*. A reader who has the preconditions can read just this root and walk away with the end-state. They never need to read sibling roots they don't care about.

Branch and leaf pages don't need the full contract — they elaborate on a part of a root and live within its precondition envelope.

### Per-page header

Every page declares its position in the graph:

```markdown
> **Kind:** root | branch | leaf · **Assumes:** <root pages whose end-state is a precondition>
> **Reachable from:** <…> · **Branches into:** <…>
> **Spec:** <spec link> · **Code:** <file paths>
```

- **root** — establishes invariants. Must include explicit **Preconditions** (top) and **End-state** + **Leads to** (end) sections.
- **branch** — drills into a part of a root or connects two roots. Lives within its parent root's precondition envelope.
- **leaf** — reference detail. Read on demand.

### Branch points within a journey

Preconditions and End-state are the trivial branch points (start and end of a root). Mid-journey branch points — *moments* in the walkthrough where the reader could profitably fork into a side-trip — are marked inline with a callout:

```markdown
> [!NOTE]
> **Branch →** [link to side-trip page]. Brief reason to follow the branch.
```

This keeps the main journey continuous while flagging where divergences live. The index file ([INDEX.md](./INDEX.md)) aggregates branch points across all pages so you can see the full graph without opening every file.

### The index file

[INDEX.md](./INDEX.md) is a single-page projection of the entire graph: every page, its kind, preconditions, end-state summary, leads-to, and branch points — in one table. Useful for:

- Drawing the full graph without parsing every page header
- Spotting orphans, broken links, or roots whose end-state nothing depends on
- Checking the precondition closure when adding a new root

Per-page headers are the source of truth; the index is an aggregated view. When you add or change a page, also update the index entry.

### Target-shape tracking

mcpkit is converging on a target shape (per the [Dec-2025 transport WG post](https://blog.modelcontextprotocol.io/posts/2025-12-19-mcp-transport-future/) and various SEPs). Where the current implementation diverges from the target, we mark it with `[!WARNING]` or `[!CAUTION]`. As mcpkit converges, these blocks get deleted — no coordinated rewrite needed.

## Pages

| Page | Kind | Covers |
|------|------|--------|
| [Bring-up: from host to live session](./bringup.md) | root | Server selection, transport selection, connection establishment, auth, initialize handshake, capability negotiation. |
| [Transport mechanics: stdio vs. streamable HTTP](./transport-mechanics.md) | root | Wire format per transport, the SSE "upgrade," server-initiated back-channel, JSON-RPC correlation, ID spaces. |

## L0 / L1 map

```mermaid
graph LR
    L0[a request happens]
    L0 --> bringup["bring-up<br/>(root)"]
    L0 --> wire["transport mechanics<br/>(root)"]
    L0 --> call["per-request anatomy<br/>(forthcoming root)"]
    bringup --> session[(session live)]
    session -.->|unlocks| call
    wire -.->|foundational for| call
    bringup -.->|drills into| wire

    click bringup "./bringup.md"
    click wire "./transport-mechanics.md"
```

The map will fill in as questions get answered. This is incremental on purpose — the structure evolves with the material rather than being pre-planned.

## Status

Working document. Pages may be refactored as the DAG sharpens.
