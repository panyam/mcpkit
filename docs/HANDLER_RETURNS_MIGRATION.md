## Handler Returns Migration (`ToolResponse` / `PromptResponse`)

`ToolHandler` and `PromptHandler` now return sealed-interface response types instead of concrete result structs. This is a **breaking signature change** that landed pre-customer to remove three in-process sentinel fields from `core.ToolResult` (`IsInputRequired`, `InputRequests`, `GoAsync`). New response variants now plug in by adding a method on the sealed interface, not a new flag.

> Tracking: [issue #486](https://github.com/panyam/mcpkit/issues/486).

## TL;DR

```go
// Before
func myTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error)
func myPrompt(ctx core.PromptContext, req core.PromptRequest) (core.PromptResult, error)

// After
func myTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error)
func myPrompt(ctx core.PromptContext, req core.PromptRequest) (core.PromptResponse, error)
```

Handler bodies usually don't change — `core.ToolResult{...}` / `core.PromptResult{...}` literals satisfy the sealed interface. Two body-level patterns need a small touch-up:

| Before | After |
|---|---|
| `return core.ToolResult{GoAsync: true}, nil` | `return core.GoAsyncResult{}, nil` |
| `result.IsInputRequired` / `result.InputRequests` field access on the return value | Type-assert the response: `if ir, ok := resp.(core.InputRequiredResult); ok { ... }` |

`ctx.RequestInput(...)` continues to work — it now returns a typed `(core.InputRequiredResult, error)` so its return satisfies `ToolResponse` directly.

## What ships in `core`

```go
type ToolResponse interface { toolResponse() }

func (ToolResult)          toolResponse() {}  // wire "complete"
func (InputRequiredResult) toolResponse() {}  // wire "input_required"  — SEP-2322
func (CreateTaskResult)    toolResponse() {}  // wire "task"            — SEP-2663
func (GoAsyncResult)       toolResponse() {}  // in-process discriminator; never serialized

type PromptResponse interface { promptResponse() }
func (PromptResult) promptResponse() {}
```

The interfaces are sealed via unexported marker methods, so external packages can't impersonate a core variant. Dispatch type-switches on the concrete value and emits the matching wire envelope. `ext/tasks` intercepts `GoAsyncResult` to spawn the SEP-2663 continuation goroutine; the legacy dispatcher reshapes `InputRequiredResult` with a freshly-minted `requestState`.

## Why now

- Three sentinel fields on a wire struct is a sum type masquerading as a record. New result variants (a future `InputRequiredResult` on `PromptResponse` for SEP-2322 prompt support, etc.) would have meant a new flag and another `json:"-"` field.
- `ToolResult` is now a pure wire shape — every field on it appears on the JSON envelope.
- Pre-customer is the cheapest time to flip the signature. Downstream consumers are limited.

## Mechanical migration recipe

1. Change every handler signature: `(core.ToolResult, error)` → `(core.ToolResponse, error)`; same for prompts.
2. Replace `core.ToolResult{GoAsync: true}` literals with `core.GoAsyncResult{}`.
3. Where you previously read `IsInputRequired` / `InputRequests` off a handler's `core.ToolResult` return value, type-assert against `core.InputRequiredResult` instead.
4. If you used `core.TypedTool[X, core.ToolResult]` with a handler that wants to return `InputRequiredResult` / `GoAsyncResult` / `CreateTaskResult`, switch the type parameter to `core.ToolResponse`:

   ```go
   core.TypedTool[MyInput, core.ToolResponse]("my_tool", "...",
       func(ctx core.ToolContext, in MyInput) (core.ToolResponse, error) { ... },
   )
   ```

   `core.TypedTool[X, core.ToolResult]` keeps working for handlers that only return a sync `ToolResult`.

5. Run `make tidy-all` (each sub-module pins its own `core` version).
