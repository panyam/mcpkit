# mcpkit/conformance/tasks-v2 — sentinel

The full SEP-2663 / SEP-2322 / SEP-2575 / SEP-2243 server-conformance
suite migrated upstream to the
[`panyam/mcpconformance`](https://github.com/panyam/mcpconformance) fork
of `modelcontextprotocol/conformance`, on the
[`feat/tasks-mrtr-extension`](https://github.com/panyam/mcpconformance/tree/feat/tasks-mrtr-extension)
branch. Run it from mcpkit via:

```bash
make testconf-tasks-v2
```

The Makefile target invokes vitest in the fork (auto-spawning the
`examples/tasks-v2` Go fixture) and then runs this folder's local
sentinel afterward.

## What lives here

This folder is a sentinel placeholder. It exists to host **future
mcpkit-stricter scenarios** — assertions that go beyond what the spec
mandates because mcpkit deliberately picks the louder/safer option
where the spec is silent (e.g., `-32602` over silent ack on edge
cases). Today there are no such tests; the placeholder
(`scenarios.test.ts`) just keeps the folder discoverable so future
contributors know where to put them.

## Adding a stricter local scenario

```bash
cd conformance && npm install
# Edit conformance/tasks-v2/scenarios.test.ts (or add a sibling .test.ts file)
npx vitest run tasks-v2/
```

Once the test passes, the next `make testconf-tasks-v2` will pick it
up automatically — the Makefile target chains the fork run with
`vitest run tasks-v2/`.

## When to upstream a stricter test

If a stricter assertion turns out to reflect a clarification that
should land in the spec text, lift-shift it into the fork
(`src/scenarios/server/tasks/`) and propose the spec edit. The fork
follows upstream `modelcontextprotocol/conformance` conventions, so
porting is mostly a file move.
