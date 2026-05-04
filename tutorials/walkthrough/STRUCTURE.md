# How this walkthrough is organized

Read this if you want to understand *why* pages look the way they do, or if you're authoring a new page. Not required for readers who just want to learn MCP — go straight to a root from the [README](./README.md).

## The model: a DAG of mechanisms, level-ordered

We model MCP as a DAG, level-ordered:

- **Level 0** — "a thing happens between client and server"
- **Level 1** — the universal anatomy that every request, notification, or response traverses
- **Level 2+** — each L1 node opens into its own sub-DAG (transports, dispatch internals, reverse calls, tasks, …)

A "spine" or "journey" is just a *path query* through the DAG. We don't author spines; we author nodes, and any spine you care about renders as a sequence of node references.

Every page declares its place in the graph so you can enter from anywhere.

> [!NOTE]
> This document is being built **incrementally and conversationally** — one question at a time. New nodes appear as the discussion reaches them.

## The root contract

A **root** is a self-contained walkthrough that establishes a set of invariants downstream pages can assume. Every root follows the same four-part contract:

1. **Preconditions** — what must be true going in. Stated explicitly at the top, with a *"if not, read [X]"* guard pointing at the root that establishes each missing precondition. A foundational root says "none — foundational."
2. **Body** — the walkthrough itself.
3. **End-state** — what is true after reading. Listed as bullets in a section near the end. Downstream roots may assume any of these without re-deriving.
4. **Leads to** — which other roots build on this end-state. Pointers, not exhaustive.

This is dependency tracking for documentation. Instead of every page restating preliminaries, a page declares its preconditions and moves on. As more roots get written, derived pages get *shorter*, not longer.

> [!NOTE]
> A root is a *self-contained chunk*. A reader who has the preconditions can read just this root and walk away with the end-state. They never need to read sibling roots they don't care about.

Branch and leaf pages don't need the full contract — they elaborate on a part of a root and live within its parent's precondition envelope.

## Per-page header

Every page declares its position in the graph:

```markdown
> **Kind:** root | branch | leaf · **Assumes:** <root pages whose end-state is a precondition>
> **Reachable from:** <…> · **Branches into:** <…>
> **Spec:** <spec link> · **Code:** <file paths>
```

- **root** — establishes invariants. Must include explicit **Preconditions** (top) and **End-state** + **Leads to** (end) sections.
- **branch** — drills into a part of a root or connects two roots. Lives within its parent root's precondition envelope.
- **leaf** — reference detail. Read on demand.

## Note blocks (textbook-style sidenotes)

GitHub-rendered Markdown doesn't support true marginalia (notes alongside the main column), but [GitHub Alerts](https://docs.github.com/en/get-started/writing-on-github/getting-started-with-writing-and-formatting-on-github/basic-writing-and-formatting-syntax#alerts) render as styled callout boxes — the closest practical equivalent. We use the five reserved types with assigned roles:

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

> [!NOTE]
> We deliberately **don't** use Markdown footnotes (`[^1]`). GitHub supports them, but they yank the reader to the bottom of the page and back — breaking the inline flow we want from a journey doc. Callout blocks stay where the reader is. If a remark is too tangential for an inline callout, it probably belongs in a separate leaf page.

## Branch points within a journey

Preconditions and End-state are the trivial branch points (start and end of a root). Mid-journey branch points — *moments* in the walkthrough where the reader could profitably fork into a side-trip — are marked inline with a callout:

```markdown
> [!NOTE]
> **Branch →** [link to side-trip page]. Brief reason to follow the branch.
```

This keeps the main journey continuous while flagging where divergences live. [INDEX.md](./INDEX.md) aggregates branch points across all pages so you can see them all without opening every file.

## Spec & code anchors

Each page links to:

- **Spec anchor** — the normative MCP spec section
- **Code anchor** — relevant `path/to/file.go` in mcpkit (line numbers omitted on purpose; they rot)

External spec links happen at the *node* level. Inside a flow, links stay on-page so reading doesn't break. Click into a node only when you need normative detail.

## Target-shape tracking

mcpkit is converging on a target shape (per the [Dec-2025 transport WG post](https://blog.modelcontextprotocol.io/posts/2025-12-19-mcp-transport-future/) and various SEPs). Where the current implementation diverges from the target, we mark it inline with one of two callout types:

- `> [!WARNING]` — **target-shape gap (extension)**: difference will be reconciled by addition. No breaking changes.
- `> [!CAUTION]` — **target-incompatible (replacement)**: the converged target *replaces* the current implementation. Will require breaking changes when the migration lands.

As mcpkit converges, these blocks get deleted on the affected node and every journey passing through it gets cleaner for free. No coordinated rewrite.

## The index file

[INDEX.md](./INDEX.md) is a single-page projection of the entire graph: every page, its kind, preconditions, end-state summary, leads-to, and branch points — in one table, plus a full mermaid render distinguishing written from planned. Useful for:

- Drawing the full graph without parsing every page header
- Spotting orphans, broken links, or roots whose end-state nothing depends on
- Checking the precondition closure when adding a new root

Per-page headers are the source of truth; the index is an aggregated view. When you add or change a page, also update the index entry.

## Authoring a new page (checklist)

1. Decide kind: **root** (establishes invariants) | **branch** (elaborates a root) | **leaf** (reference detail).
2. If root: identify which existing roots' end-states are preconditions. Add explicit *"if not, read X"* guards in the Preconditions section.
3. Write the per-page header (Kind / Assumes / Reachable from / Branches into / Spec / Code).
4. Write the body. Use note blocks per the conventions above.
5. If root: write **End-state** (bullets) and **Leads to** (pointers).
6. Update [INDEX.md](./INDEX.md): add a row to the node table, log any new branch points, update the mermaid graph.
7. Update [README.md](./README.md) if the new page belongs in the recommended reading order.
