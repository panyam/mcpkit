# Client context migration

Every `client.Client` method that performs I/O now takes a `context.Context` as
its first argument. Accessors (`SessionID`, `URL`, `ServerSupportsUI`, ...) are
unchanged — they touch no network.

This is a breaking change. It ships alongside a start-up readiness signal on the
server and a stricter `Server.Register`; see [What else changed](#what-else-changed).

## Why

The client was split down the middle. `ListTools` and the other paginated list
methods took a `ctx`; `ToolCall`, `ReadResource`, `Call`, and `Connect` took
none. Two consequences:

- **No cancellation or timeout** on the calls people make most. A hung
  `tools/call` could not be abandoned.
- **A latent panic.** Because half the surface took a `ctx`, callers reasonably
  passed one — including `nil`. Go permits an untyped `nil` for a
  `context.Context` parameter and neither the compiler nor `go vet` flags it, so
  `ListTools(nil)` compiled and then crashed with a nil-pointer dereference
  inside the pagination loop.

Adding `ctx` to the rest makes the rule uniform: if it talks to the server, it
takes a context.

## What changed

| Before | After |
|---|---|
| `c.Connect()` | `c.Connect(ctx)` |
| `c.Call(method, params)` | `c.Call(ctx, method, params)` |
| `c.CallContext(cc, method, params)` | `c.CallContext(ctx, cc, method, params)` |
| `c.ToolCall(name, args)` | `c.ToolCall(ctx, name, args)` |
| `c.ToolCallFull(name, args)` | `c.ToolCallFull(ctx, name, args)` |
| `c.ReadResource(uri)` | `c.ReadResource(ctx, uri)` |
| `c.ReadResourceFull(uri)` | `c.ReadResourceFull(ctx, uri)` |
| `c.SubscribeResource(uri)` | `c.SubscribeResource(ctx, uri)` |
| `c.UnsubscribeResource(uri)` | `c.UnsubscribeResource(ctx, uri)` |
| `c.SetLogLevel(level)` | `c.SetLogLevel(ctx, level)` |
| `c.NotifyRootsChanged()` | `c.NotifyRootsChanged(ctx)` |
| `c.ListToolsPage(cursor)` | `c.ListToolsPage(ctx, cursor)` |
| `c.ListResourcesPage(cursor)` | `c.ListResourcesPage(ctx, cursor)` |
| `c.ListResourceTemplatesPage(cursor)` | `c.ListResourceTemplatesPage(ctx, cursor)` |
| `c.ListPromptsPage(cursor)` | `c.ListPromptsPage(ctx, cursor)` |
| `client.ToolCallTyped[T](c, name, args)` | `client.ToolCallTyped[T](ctx, c, name, args)` |
| `client.ToolCall(c, name, args)` | `client.ToolCall(ctx, c, name, args)` |
| `client.GetTask(c, id)` | `client.GetTask(ctx, c, id)` |
| `client.UpdateTask(c, req)` | `client.UpdateTask(ctx, c, req)` |
| `client.CancelTask(c, id)` | `client.CancelTask(ctx, c, id)` |
| `client.GetTaskV1(c, id)` | `client.GetTaskV1(ctx, c, id)` |
| `client.GetTaskPayloadV1(c, id)` | `client.GetTaskPayloadV1(ctx, c, id)` |
| `client.ListTasksV1(c, cursor)` | `client.ListTasksV1(ctx, c, cursor)` |
| `client.CancelTaskV1(c, id)` | `client.CancelTaskV1(ctx, c, id)` |
| `client.ToolCallAsTaskV1(c, name, args, opts...)` | `client.ToolCallAsTaskV1(ctx, c, name, args, opts...)` |
| `bt.Cancel()` (`*BackgroundTask`) | `bt.Cancel(ctx)` |

Methods that already took a `ctx` (`ListTools`, `ListToolsForModel`,
`ListResources`, `ListResourceTemplates`, `ListPrompts`, and the `Tools` /
`Resources` / `ResourceTemplates` / `Prompts` iterators) are unchanged.

## How to migrate

The compiler finds every site. Build, and add the context each error names:

```
not enough arguments in call to c.ToolCall
    have (string, map[string]any)
    want (context.Context, string, any)
```

Pass whatever context is already in scope. Where there is none, and the call
is not cancellable in any meaningful way, `context.Background()` is correct and
honest:

```go
// before
out, err := c.ToolCall("greet", args)

// after — a request-scoped context, if you have one
out, err := c.ToolCall(ctx, "greet", args)

// after — a timeout, now possible for the first time
ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
defer cancel()
out, err := c.ToolCall(ctx, "greet", args)
```

Two call shapes fail with a *type* error rather than "not enough arguments",
so grep for them if your build stops short:

- **Variadic helpers** (`ToolCallAsTaskV1`) report
  `*client.Client does not implement context.Context`.
- **Generic helpers** (`ToolCallTyped[T]`) name the instantiated form,
  `ToolCallTyped[YourType]`, in the error.

### Connect's context bounds the handshake, not the session

`Connect(ctx)` follows `grpc.DialContext`: the context bounds the dial and the
`initialize` handshake. Once `Connect` returns `nil` the session is live and
outlives that context. Passing a short timeout is safe and will not tear the
connection down later.

```go
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
if err := c.Connect(ctx); err != nil {
    return err
}
cancel()          // the handshake budget is spent
defer c.Close()   // this is what ends the session
```

`WithConnectTimeout` still applies and composes with the context — whichever
fires first wins.

### A nil context no longer panics

`nil` is normalized to `context.Background()` on every exported method rather
than dereferenced. This is a crash guard, not an endorsement: pass a real
context so cancellation works.

## What else changed

Two adjacent fixes ship in the same release.

### Server readiness

`Run` and `ListenAndServe` block, so they are normally started in a goroutine —
but nothing reported when the listener was bound, which forced a
`time.Sleep` before connecting. Two additions remove the guess:

```go
go srv.Run(":0")
<-srv.Ready()      // closed once the port is accepting
addr := srv.Addr() // the bound address; ":0" resolves to a real port
```

`Ready` never closes if the bind fails, so select on it and the error together:

```go
errCh := make(chan error, 1)
go func() { errCh <- srv.Run(":8787") }()
select {
case <-srv.Ready():
case err := <-errCh:
    return fmt.Errorf("server failed to start: %w", err)
}
```

`RunWithListener(ln)` is the strongest form — the caller owns the bind, so the
port is reachable before serving even starts and there is no window to race.

### Register rejects unsupported types

`Server.Register(items ...any)` silently ignored any value that was not a
`Tool`, `Resource`, `ResourceTemplate`, `Prompt`, or `core.TypedToolResult`. A
one-character slip (`&server.Tool{...}` for `server.Tool{...}`) produced a
server missing a tool, with no error, no log, and no failure until a caller hit
the missing name at run time.

It now panics with the offending type and argument index. Registration happens
at start-up, so this surfaces immediately. If your build starts panicking here,
the registration was never taking effect.
