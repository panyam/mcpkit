# examples/stateless — SEP-2575 stateless wire + SEP-2567 handle pattern

A single mcpkit server that demonstrates two complementary draft SEPs:

| SEP | What this example shows |
|---|---|
| **SEP-2575** | Pure stateless wire — no `initialize` handshake, every request carries a `_meta` envelope, `server/discover` instead of session lifecycle. The diagnostic tools (`test_*`) satisfy the upstream conformance scenario at `modelcontextprotocol/conformance/src/scenarios/server/stateless.ts`. |
| **SEP-2567** | Explicit state handles. The cart tools (`create_cart`, `add_item`, `checkout`) thread a server-minted `cart_id` through tool arguments instead of relying on session-scoped storage. State lives in `server.HandleStore[Cart]`. |

## Run

```bash
make serve           # pure stateless wire on :8080 (default)
make serve-dual      # accepts both legacy AND stateless wires on one URL
```

Override the address: `go run . --serve --addr=:9090`.

## Tools

### SEP-2567 cart (the user-facing demo)

| Tool | Args | Returns |
|---|---|---|
| `create_cart` | — | `{cart_id}` |
| `add_item` | `{cart_id, sku, quantity}` | `{cart_id, total, items}` |
| `checkout` | `{cart_id}` | `{order_id, total, items}` — handle is deleted on success |

The catalog is hardcoded: `apple` (0.50), `bread` (2.50), `coffee` (4.00), `orange` (0.75).

Carts expire after 1 hour with a 5-minute GC sweep — fine for the demo, tune for prod.

### SEP-2575 diagnostic tools (conformance hooks)

These exist so the upstream conformance scenario can drive specific spec invariants.

| Tool | What it tests |
|---|---|
| `test_missing_capability` | Returns `-32003` + structured `requiredCapabilities` when the per-request `_meta.clientCapabilities` lacks `sampling`. |
| `test_streaming_elicitation` | Uses MRTR (`ctx.RequestInput` with `core.NewElicitationInputRequest`) to return an `InputRequiredResult` chunk — never an independent JSON-RPC request on the response stream. |
| `test_logging_tool` | Emits `notifications/message` ONLY if the per-request `_meta.logLevel` opts in. |
| `test_trigger_tool_change` | Adds then removes a synthetic tool, broadcasting `notifications/tools/list_changed` to every open `subscriptions/listen` stream whose filter admits it. |
| `test_trigger_prompt_change` | Same idea, prompts surface. |

## Conformance

```bash
cd ../../conformance && make testconf-stateless
```

(Target lands alongside this example — see [`docs/SEP_2567_HANDLES.md`](../../docs/SEP_2567_HANDLES.md) for the bundle context.)

## Walkthrough

A scripted demokit walkthrough that drives the cart story end-to-end is intentionally deferred — see [`mcpkit#478`](https://github.com/panyam/mcpkit/issues/478) (the dual-mode + walkthrough audit) for the cross-cutting follow-up that will land it alongside the existing-examples sweep.

## Architecture notes

- The server runs in `stateless.ModeStateless` by default (conformance suite wants a pure stateless surface). `--mode=dual` opens the legacy wire alongside.
- `HandleStore[Cart]` is the interface; the default `NewHandleStore[Cart](...)` returns the in-memory impl. Persistent backends (Redis etc.) for cross-replica deployments are tracked in `mcpkit#471` — they drop in without changing tool-handler call sites.
- `add_item` uses `HandleStore.Put(cart_id, ...)` to update in place so the client's handle stays stable across rounds (the SEP-2567 happy path).
