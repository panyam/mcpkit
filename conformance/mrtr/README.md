# mcpkit/conformance/mrtr — sentinel

The SEP-2322 MRTR server-conformance scenarios (basic elicitation,
sampling, and roots/list round-trips; `requestState` validation;
multi-input single round; multi-round answer accumulation; wrong-key
tolerance; and the SEP-2663 MRTR to Tasks composition flow) live in
[`modelcontextprotocol/conformance`](https://github.com/modelcontextprotocol/conformance)
on `main`, as the `input-required-result-*` scenario family. They
travelled there through the
[`panyam/mcpconformance`](https://github.com/panyam/mcpconformance) fork
(branch `feat/tasks-mrtr-extension`), but that hop is history:
`MCPCONFORMANCE_MRTR_PATH` defaults to `../conf-upstream-main`, a direct
clone of upstream. Run from mcpkit via:

```bash
make testconf-mrtr
```

`conformance/scripts/conf-mrtr.sh` builds the upstream CLI, spawns
`cmd/testserver`, runs all 14 `input-required-result-*` scenarios
through `node dist/index.js server`, gates on zero FAILURE rows, then
runs upstream's `negative-mrtr.test.ts` and this folder's local
sentinel.

**The fixture is `cmd/testserver`, not `examples/mrtr`.** The scenarios
call `test_input_required_result_*` tools that only
`cmd/testserver/conformance_input_required.go` registers; `examples/mrtr`
answers all fourteen with "unknown tool". The old shape pointed at
`examples/mrtr` through `MRTR_SERVER_URL` / `MRTR_SERVER_CMD`, which
upstream does not read, so nothing in mcpkit was graded here at all.

Upstream's `negative-mrtr.test.ts` still runs after the gate. It spawns
its own deliberately-broken fixture and checks that the upstream checks
emit FAILURE against a bad server, so it grades the harness rather than
mcpkit. That is worth keeping as a guard against a vacuously-green run.

## What lives here

This folder is a sentinel placeholder for **future mcpkit-stricter
MRTR scenarios**, assertions that go beyond what SEP-2322 mandates
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
the spec text, lift-shift it into upstream
(`src/scenarios/server/input-required-result/`) and propose the spec
edit.
