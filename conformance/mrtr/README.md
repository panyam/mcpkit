# mcpkit/conformance/mrtr — sentinel

The full SEP-2322 MRTR server-conformance suite migrated upstream to
the [`panyam/mcpconformance`](https://github.com/panyam/mcpconformance)
fork, on the
[`feat/tasks-mrtr-extension`](https://github.com/panyam/mcpconformance/tree/feat/tasks-mrtr-extension)
branch (paired with the SEP-2663 tasks scenarios since the two surfaces
share base types). Run from mcpkit via:

```bash
make testconf-mrtr
```

The Makefile target invokes vitest in the fork (auto-spawning the
`examples/mrtr` Go fixture) and then runs this folder's local sentinel
afterward.

## What lives here

This folder is a sentinel placeholder for **future mcpkit-stricter
MRTR scenarios** — assertions that go beyond what SEP-2322 mandates
because mcpkit deliberately picks the louder/safer option where the
spec is silent. Today there are no such tests; `scenarios.test.ts` is
a placeholder so the folder is discoverable.

## Adding a stricter local scenario

```bash
cd conformance && npm install
# Edit conformance/mrtr/scenarios.test.ts (or add a sibling .test.ts file)
npx vitest run mrtr/
```

The `make testconf-mrtr` target picks up new tests in this folder
automatically.

## When to upstream a stricter test

If a stricter assertion reflects a clarification that should land in
the spec text, lift-shift it into the fork
(`src/scenarios/server/mrtr/`) and propose the spec edit.
