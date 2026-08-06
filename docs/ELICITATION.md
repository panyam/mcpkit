# Elicitation

A tool handler that needs a decision from the user, not from the model, asks for it with elicitation. The server sends `elicitation/create` with a JSON Schema describing what it wants; the client renders a form, collects the answer, and sends it back.

This is a server-to-client request, the reverse of the usual direction. The client must have declared elicitation support at handshake time, so a handler cannot rely on it being available.

## Asking for input

```go
result, err := ctx.Elicit(core.ElicitationRequest{
    Message: "Which database should I connect to?",
    RequestedSchema: json.RawMessage(`{
        "type": "object",
        "properties": {
            "database": {"type": "string", "enum": ["prod", "staging", "dev"]}
        }
    }`),
})
```

`result.Action` is `accept`, `decline`, or `cancel`. Check it before touching `result.Content`: on decline and cancel the content is undefined, and a user who cancelled has not given you a value to fall back on.

Errors worth distinguishing:

| Error | Means |
|---|---|
| `ErrElicitationNotSupported` | the client never declared the capability |
| `ErrNoRequestFunc` | no push channel, see the stateless note below |
| `context.DeadlineExceeded` | the user did not answer in time |

Runnable examples of the defaults behavior described below are in [`client/example_elicitation_test.go`](../client/example_elicitation_test.go).

## Defaults

A schema property can declare a `default`. When the user accepts and leaves that field out, the client fills it in before the response goes back to the server:

```json
{"type": "object", "properties": {
  "env":      {"type": "string",  "default": "dev"},
  "replicas": {"type": "integer", "default": 2}
}}
```

A handler that returns only `{"env": "prod"}` produces `{"env": "prod", "replicas": 2}` on the wire.

The merge happens inside mcpkit's `elicitation/create` dispatch path, after your handler returns. Handler authors stay unaware of it: return what the user typed and the defaults land on their own.

Four rules govern the merge:

- **User input always wins.** Defaults fill absent keys only, so a supplied value is never overwritten, including one that equals its type's zero value.
- **Defaults apply only on `accept`.** Filling them on decline or cancel would invent input the user never gave.
- **A default whose type contradicts its schema is dropped.** An `integer` property declaring `"default": "two"` sends no value at all, because forwarding it would put wire-invalid data in front of the server.
- **`integer` accepts a whole number.** JSON numbers all decode to `float64` in Go, so a default of `2` satisfies an `integer` property while `2.5` does not.

This implements SEP-1034.

## Enums

An `enum` makes the client render a fixed choice instead of a free-text field. `enumNames` is optional and supplies display labels positionally; a client that does not understand it falls back to the raw values.

```json
{"env": {
  "type": "string",
  "enum": ["dev", "staging", "prod"],
  "enumNames": ["Development", "Staging", "Production"],
  "default": "dev"
}}
```

## mcpkit does not validate the response

The schema tells the client what to collect. It is not enforced on the way back: mcpkit does not check `result.Content` against `requestedSchema` before handing it to your handler.

So a handler that asked for an `integer` can receive a string, and one that offered three enum values can receive a fourth. Validate anything you are going to act on:

```go
env, ok := result.Content["env"].(string)
if !ok || !slices.Contains([]string{"dev", "staging", "prod"}, env) {
    return core.ErrorResult("invalid environment"), nil
}
```

Content values are decoded JSON, so numbers arrive as `float64` and objects as `map[string]any`, the same as prompt arguments.

## URL mode

Some flows cannot happen in a form: an OAuth consent screen, a payment step, a device pairing page. URL mode hands the user a link instead of a schema.

```go
core.ElicitURL(ctx, core.ElicitationRequest{
    Message:       "Approve access in your browser",
    Mode:          core.ElicitModeURL,
    URL:           "https://example.com/consent/abc",
    ElicitationID: "abc",
})
```

Note the shape: form-mode elicitation has a context method (`ctx.Elicit`), while `ElicitURL` is a package function taking the context as its first argument. A handler context embeds `context.Context`, so passing `ctx` straight through works.

`RequestedSchema` must not be set in URL mode, and the client must have opted in with `WithElicitationURLSupport`, separately from the form-mode handler. Without it the request is rejected as invalid params.

The response only tells you the user acknowledged the link. When the out-of-band flow actually finishes, the server signals it with `NotifyElicitationComplete`, which sends `notifications/elicitation/complete` carrying the `elicitationId` so the client can correlate it with the original request and retry whatever was blocked. This is SEP-1036.

## On the stateless wire

`ctx.Elicit` needs a server-to-client push channel, which the SEP-2575 stateless wire does not have: the spec forbids independent JSON-RPC requests on a `tools/call` response stream. Calling it there returns `ErrNoRequestFunc`.

Stateless handlers ask through MRTR instead, which carries the request in the tool result and gets the answer on a follow-up call:

```go
return ctx.RequestInput(core.InputRequests{
    "which-db": core.NewElicitationInputRequest(req),
})
```

See [MRTR_TUTORIAL.md](MRTR_TUTORIAL.md). The client side needs no extra work: `DefaultInputHandler` routes an MRTR elicitation request to the same handler a push-mode request would reach.

## Related

- [MRTR input-gathering](MRTR_TUTORIAL.md) for the stateless and multi-round path
- [Prompts](PROMPTS.md) for the other place schemas describe user-supplied values
- `examples/elicitation/` for a runnable two-terminal walkthrough including URL mode
