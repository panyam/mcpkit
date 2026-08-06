# Argument completion

A user typing a prompt argument in a client UI should get suggestions, the same way a shell completes a filename. MCP does this with `completion/complete`: the client sends the argument name and whatever the user has typed so far, and your server answers with the candidates that match.

Completion is a server capability like tools or resources. It applies to two things: prompt arguments, and the variables in a resource URI template.

## Registering a handler

`RegisterCompletion` takes a reference type, the name of the thing being completed, and a handler.

```go
srv.RegisterCompletion("ref/prompt", "summarize",
    func(ctx core.PromptContext, ref core.CompletionRef, arg core.CompletionArgument) (core.CompletionResult, error) {
        styles := []string{"bullet", "brief", "detailed"}
        var matched []string
        for _, s := range styles {
            if strings.HasPrefix(s, arg.Value) {
                matched = append(matched, s)
            }
        }
        return core.CompletionResult{Values: matched, Total: len(matched)}, nil
    },
)
```

`arg.Name` is the argument being completed and `arg.Value` is the partial input. Prefix matching is the common case, but nothing requires it. Fuzzy matching, a database lookup, or ignoring the partial value entirely are all fine.

Runnable versions of every snippet on this page live in [`server/example_completion_test.go`](../server/example_completion_test.go) and run as part of the test suite.

## The two reference types

| Ref type | Second argument to `RegisterCompletion` | Completes |
|---|---|---|
| `ref/prompt` | the prompt name | an argument of that prompt |
| `ref/resource` | the URI **template**, not a concrete URI | a variable in that template |

The resource case trips people up. You register against the template string you passed to `RegisterResourceTemplate`, because that is what the client sends back:

```go
srv.RegisterCompletion("ref/resource", "file:///logs/{date}",
    func(ctx core.PromptContext, ref core.CompletionRef, arg core.CompletionArgument) (core.CompletionResult, error) {
        return core.CompletionResult{Values: []string{"2026-08-04", "2026-08-05"}, Total: 2}, nil
    },
)
```

Registering `file:///logs/2026-08-04` instead would never match, since the client completes the template, not an already-resolved URI.

## An unregistered argument is not an error

Ask for completion on something with no handler and the server returns an empty list with no error:

```go
resp, _ := srv.Dispatch(ctx, completionRequest("ref/prompt", "no-such-prompt", "style", "b"))
// resp.Error == nil, values == []
```

This is deliberate. A client can offer completion on every argument without first discovering which ones support it, and an argument that gains a handler later starts working with no client change. Treat "no suggestions" and "no handler" as the same thing.

## Returning more than 100 values

The spec caps a completion response at 100 values. You do not have to enforce that yourself. Return everything you found and the server truncates, records the real count in `Total`, and sets `HasMore`:

```go
return core.CompletionResult{Values: values}, nil   // len(values) == 150
// client sees: 100 values, Total 150, HasMore true
```

If you set `Total` yourself it is preserved. The automatic fill only happens when you leave it at zero, so a handler that knows the true size of a large result set (say, a count from a database that it only partially fetched) can report it accurately.

`HasMore` tells the client there is more behind the current partial input. There is no cursor and no second page: the client narrows the search by sending a longer `arg.Value`, and your handler filters again.

## Errors

Returning an error produces a JSON-RPC error response with code `-31003` (`core.ErrCodeCompletionError`) and the ref key in the message. Prefer an empty result over an error for the ordinary "nothing matched" case. Reserve errors for a genuine failure, like the backing store being unreachable, so a client can tell the difference between "no suggestions" and "completion is broken right now".

## Context

The handler receives a `core.PromptContext`, the same typed context prompt handlers get. It embeds `context.Context`, so cancellation works directly through `ctx.Done()` and `ctx.Err()`, and `ctx.AuthClaims()` gives you the authenticated identity when the server runs behind `ext/auth`. A UI calls completion on every keystroke, so keep handlers fast and avoid unbounded work.

## Wire shape

Request:

```json
{
  "jsonrpc": "2.0", "id": 1, "method": "completion/complete",
  "params": {
    "ref": {"type": "ref/prompt", "name": "summarize"},
    "argument": {"name": "style", "value": "b"}
  }
}
```

Response:

```json
{
  "jsonrpc": "2.0", "id": 1,
  "result": {"completion": {"values": ["bullet", "brief"], "total": 2, "hasMore": false}}
}
```

The server resolves the handler by joining the ref type and the name or URI, so `ref/prompt` and `ref/resource` never collide even if a prompt and a template share a string.

## Related

- [Getting started](GETTING_STARTED.md) for registering prompts and resource templates in the first place
- [Architecture](ARCHITECTURE.md) for where completion sits in the dispatch path
