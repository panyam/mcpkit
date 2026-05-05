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

## The page contract (four parts)

A **root** is the kind of page that establishes a set of invariants downstream pages can assume. Root pages follow the same four-part contract:

1. **Prerequisites** — what must be true going in. Stated explicitly at the top, with a *"if not, read [X]"* guard pointing at the page that establishes each missing prerequisite. A foundational page says "none — foundational."
2. **Body** — the walkthrough itself. May begin with a short optional **Context** section (what the topic is and where it sits in MCP). Context isn't a contract requirement — it just helps the reader land softly. Don't use it to defend the page's existence.
3. **End-state** — what is true after reading. Listed as bullets in a section near the end. Downstream pages may assume any of these without re-deriving.
4. **Next to read** — which other pages build on this end-state. Pointers, not exhaustive.

This is dependency tracking for documentation. Instead of every page restating preliminaries, a page declares its prerequisites and moves on. As more pages get written, derived ones get *shorter*, not longer.

> [!NOTE]
> A root page is a *self-contained chunk*. A reader who has the prerequisites can read just this one page and walk away with its end-state. They never need to read sibling pages they don't care about.

Branch and leaf pages don't need the full contract — they elaborate on a part of a root and live within its parent's prerequisite envelope.

### Optional pattern: FAQ-style pages

A page may be organized as a series of **questions** rather than a single linear walkthrough. Each Q is a self-contained chunk; the set collectively covers the territory and establishes the End-state. Each Q can carry its own mid-journey `Branch →` callouts.

This works well when:

- The territory has multiple semi-independent angles a reader might want to skim selectively (taxonomy, mechanism, race conditions, integration with another concept, common gotcha).
- The page grew out of a real conversation — the questions you actually wanted answered are usually the right shape for a teaching doc.
- The reader can stop after any one Q with a partial-but-coherent end-state.

Example: [notifications.md](./notifications.md) — five Qs covering taxonomy, the worked example (list-changed), cancellation, progress, and capability mismatch.

Use sparingly. Most pages want a single linear walkthrough; FAQ-style is a tool for the surveyable-territory case.

## Per-page header

Every page declares its position in the graph:

```markdown
> **Kind:** root | branch | leaf · **Assumes:** <root pages whose end-state is a prerequisite>
> **Reachable from:** <…> · **Branches into:** <…>
> **Spec:** <spec link> · **Code:** <file paths>
```

- **root** — establishes invariants. Must include explicit **Prerequisites** (top) and **End-state** + **Next to read** (end) sections.
- **branch** — drills into a part of a root or connects two roots. Lives within its parent root's prerequisite envelope.
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

Prerequisites and End-state are the trivial branch points (start and end of a root). Mid-journey branch points — *moments* in the walkthrough where the reader could profitably fork into a side-trip — are marked inline with a callout:

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

### Links to stub pages

Every page referenced in any "Next to read" or "Branch →" callout must exist on disk — even if it's just a stub with a filled-out header and an outline body. Stubs are created at the same time as the link is added; clicking a `*(stub)*` link lands you on a real page that says "this is a stub, header is honest, body TBD." Tracking is in the [Stub pages table](./INDEX.md) in INDEX.

Why stubs instead of 404s: the graph stays accurate (every node has a Prerequisites header to parse), `make check` can enforce no-broken-links, and a reader who clicks a forward reference gets a meaningful "this is coming, here's what it'll cover" page instead of a confusing GitHub 404.

Stub pages carry a `<!-- STUB -->` HTML comment as the second line of the file (used by `build-graph.sh` to color stub nodes amber and to drive `make stats`). When a stub gets fully written, drop the comment and the linker should drop `*(stub)*` markers pointing at it.

## Target-shape tracking

mcpkit is converging on a target shape (per the [Dec-2025 transport WG post](https://blog.modelcontextprotocol.io/posts/2025-12-19-mcp-transport-future/) and various SEPs). Where the current implementation diverges from the target, we mark it inline with one of two callout types:

- `> [!WARNING]` — **target-shape gap (extension)**: difference will be reconciled by addition. No breaking changes.
- `> [!CAUTION]` — **target-incompatible (replacement)**: the converged target *replaces* the current implementation. Will require breaking changes when the migration lands.

As mcpkit converges, these blocks get deleted on the affected node and every journey passing through it gets cleaner for free. No coordinated rewrite.

## The index file

[INDEX.md](./INDEX.md) is a single-page projection of the entire graph: every page, its kind, prerequisites, end-state summary, leads-to, and branch points — in one table, plus a full mermaid render distinguishing written from planned. Useful for:

- Drawing the full graph without parsing every page header
- Spotting orphans, broken links, or roots whose end-state nothing depends on
- Checking the prerequisite closure when adding a new root

Per-page headers are the source of truth; the index is an aggregated view. When you add or change a page, also update the index entry.

## Authoring a new page (checklist)

1. Decide kind: **root** (establishes invariants) | **branch** (elaborates a root) | **leaf** (reference detail).
2. If root: identify which existing roots' end-states are prerequisites. Add explicit *"if not, read X"* guards in the Prerequisites section.
3. Write the per-page header (Kind / Assumes / Reachable from / Branches into / Spec / Code).
4. Write the body. Use note blocks per the conventions above.
5. If root: write **End-state** (bullets) and **Next to read** (pointers).
6. Update [INDEX.md](./INDEX.md): add a row to the node table, log any new branch points, update the mermaid graph.
7. Update [README.md](./README.md) if the new page belongs in the recommended reading order.
