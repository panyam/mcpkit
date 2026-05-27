# SEP-2567 — Explicit State Handles in mcpkit

[SEP-2567](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2567) ("Sessionless MCP via Explicit State Handles") is the SEP-2575 stateless wire's *application-layer* counterpart: the wire removes session machinery; SEP-2567 explains how to model stateful flows *without* sessions, using explicit server-minted handles threaded through tool parameters.

**SEP-2567 is design guidance — there is no wire contract and no upstream conformance suite.** Any storage you can hand a tool handler satisfies it: Redis, SQL, an in-memory `sync.Map`, a custom RPC backend, whatever. The SEP cares about *behavior* (server-minted opaque ids, threaded through tool args, no implicit session state) — not about any specific Go API.

mcpkit ships `server.HandleStore[T]` as **opt-in scaffolding** for the common case where you don't already have a store wired up. It handles opaque-id minting (collision-resistant base32), TTL/GC, and typed get/put/delete behind a small interface. Use it, replace it with your own store, or skip it entirely — all three are equally SEP-2567-compliant. The worked example in `examples/stateless/`'s cart story uses `HandleStore` for the on-ramp; a production deployment would as likely call Redis directly.

## The pattern in one example

```go
type Cart struct {
    Items []Item
    Total float64
}

// Server-side: one HandleStore per logical type.
carts := server.NewHandleStore[Cart]()

srv.RegisterTool(core.ToolDef{Name: "create_cart"}, func(_ core.ToolContext, _ core.ToolRequest) (core.ToolResult, error) {
    id := carts.Mint(Cart{}, 0)
    return jsonResult(map[string]any{"cart_id": id}), nil
})

srv.RegisterTool(core.ToolDef{Name: "add_item"}, func(_ core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
    var args struct{ CartID, SKU string; Quantity int }
    json.Unmarshal(req.Arguments, &args)

    cart, ok := carts.Get(args.CartID)
    if !ok { return core.ToolResult{}, fmt.Errorf("unknown cart_id") }

    cart.Items = append(cart.Items, lookupItem(args.SKU, args.Quantity))
    cart.Total += /* ... */

    // Update-in-place under the same cart_id — the SEP-2567 happy path.
    // Client's handle stays valid across rounds.
    carts.Put(args.CartID, cart, 0)
    return jsonResult(map[string]any{"cart_id": args.CartID, "total": cart.Total}), nil
})

srv.RegisterTool(core.ToolDef{Name: "checkout"}, func(_ core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
    var args struct{ CartID string }
    json.Unmarshal(req.Arguments, &args)
    cart, _ := carts.Get(args.CartID)
    carts.Delete(args.CartID)
    return jsonResult(map[string]any{"order_id": mintOrderID(), "total": cart.Total}), nil
})
```

The complete runnable version: [`examples/stateless/main.go`](../examples/stateless/main.go).

## API reference

```go
type HandleStore[T any] interface {
    Mint(v T, ttl time.Duration) string
    Put(id string, v T, ttl time.Duration) bool   // existed-already?
    Get(id string) (T, bool)
    Delete(id string) bool
    Len() int
    Close()
}

func NewHandleStore[T any](opts ...HandleStoreOption) HandleStore[T]  // default: in-memory
```

| Option | Default | Effect |
|---|---|---|
| `WithHandleDefaultTTL(d)` | `0` | Mint's `ttl=0` falls back to this; `0` here means "no expiry" |
| `WithHandleIDPrefix("cart")` | `""` | Minted ids become `cart-AB12CD...` for greppability |
| `WithHandleGCInterval(d)` | `0` | Background sweep of expired entries; `0` = lazy-only |

TTL nuances:
- `ttl > 0` — per-handle TTL override.
- `ttl == 0` — fall back to the store's `WithHandleDefaultTTL`.
- `ttl < 0` — force "never expires" even when a non-zero default is set.

IDs are 128-bit crypto-random base32 (26 chars), prefixed if configured. Collision-resistant for any sane store size.

## How this composes with SEP-2575 stateless wire

| Concern | SEP-2575 (wire) | SEP-2567 (application) |
|---|---|---|
| Per-request `_meta` envelope | mandatory | irrelevant — handles ride in tool args |
| Sessions / `Mcp-Session-Id` | gone | gone (handles replace) |
| `tools/list` / `prompts/list` | MUST NOT depend on session state | trivially satisfied — handles aren't in those endpoints |
| Cross-call state | not handled (lost) | use a handle |
| Compose with SEP-2549 list TTLs | yes — `_meta` envelope is per-request, lists cacheable at `(deployment, auth)` granularity | yes — handle stores stay out of list endpoints, so lists remain cacheable |

A stateless mcpkit server using `HandleStore[T]` for cart-style tools is the canonical post-SEP-2575 server: zero session storage in the process, every tool call is independent, but tools that need cross-call state thread an opaque handle through arguments — and the wire shape stays cacheable.

## When *not* to use a handle

- When the cross-call state is small enough to fit in the tool result (let the client carry it).
- When the state is genuinely per-user-and-persistent (use the real backing store, not an in-memory handle).
- When the tool is read-only and idempotent on its arguments (no state to hold).

## Persistent backends

`InMemoryHandleStore[T]` is in-memory and process-local. For real stateless deployments behind a load balancer where Mint and Get might hash to different replicas:

- Persistent backends (Redis, etc.) tracked in [panyam/mcpkit#471](https://github.com/panyam/mcpkit/issues/471). Drop-in via the `HandleStore[T]` interface — no parent-package changes needed.
- Admin endpoints over the store (introspection, eviction) tracked in [panyam/mcpkit#472](https://github.com/panyam/mcpkit/issues/472).

## When to migrate existing examples

mcpkit's pre-SEP-2575 examples (`apps/`, `tasks/`, `elicitation/`, ...) were written assuming session-scoped state. Each one is a candidate for the SEP-2567 handle pattern if its tools currently rely on session-keyed storage. The sweep is tracked in [panyam/mcpkit#470](https://github.com/panyam/mcpkit/issues/470); the dual-mode walkthrough audit (does the example *work* on both wires?) is [#478](https://github.com/panyam/mcpkit/issues/478).
