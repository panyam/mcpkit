# mcpkit/conformance/tasks-v2 — sentinel

The full SEP-2663 / SEP-2322 / SEP-2575 / SEP-2243 server-conformance
suite lives in
[`modelcontextprotocol/conformance`](https://github.com/modelcontextprotocol/conformance)
on `main`. It travelled there through the
[`panyam/mcpconformance`](https://github.com/panyam/mcpconformance) fork
(branch `feat/tasks-mrtr-extension`), but that hop is history:
`MCPCONFORMANCE_TASKS_V2_PATH` defaults to `../conf-upstream-main`, a
direct clone of upstream. Run it from mcpkit via:

```bash
make testconf-tasks-v2
```

`conformance/scripts/conf-tasks-v2.sh` builds the upstream CLI, spawns
the `examples/tasks-v2` Go fixture, runs every upstream `tasks-*`
scenario through `node dist/index.js server`, gates on zero FAILURE
rows, and then runs this folder's local sentinel.

It does **not** invoke vitest in a fork. The old shape did, and that was
the bug: it ran upstream's `all-scenarios.test.ts` with
`TASKS_SERVER_URL` / `TASKS_SERVER_CMD` set, neither of which upstream
reads, and that test file spawns its own `everything-server.ts`. The
stage graded the TypeScript reference server and never touched mcpkit.

## What lives here

This folder is a sentinel placeholder. It exists to host **future
mcpkit-stricter scenarios**, assertions that go beyond what the spec
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

Once the test passes, the next `make testconf-tasks-v2` picks it up
automatically, since the script chains the upstream run with
`vitest run tasks-v2/`.

## When to upstream a stricter test

If a stricter assertion turns out to reflect a clarification that
should land in the spec text, lift-shift it into upstream
(`src/scenarios/server/tasks/`) and propose the spec edit.
