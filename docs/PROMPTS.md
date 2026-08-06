# Prompts

A prompt is a named, parameterised message template your server hands to a client. The client shows it in a picker, collects any arguments, and drops the resulting messages into a conversation with a model.

That makes prompts the one primitive the user invokes directly. Tools are chosen by a model and resources are read on demand, but a prompt is something a person picks from a menu. Name and describe them accordingly.

## Registering a prompt

```go
srv.RegisterPrompt(
    core.PromptDef{Name: "changelog", Description: "Draft a changelog entry"},
    func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResponse, error) {
        return core.PromptResult{
            Description: "Changelog draft",
            Messages: []core.PromptMessage{{
                Role:    "user",
                Content: core.Content{Type: "text", Text: "Draft a changelog entry."},
            }},
        }, nil
    },
)
```

`Role` is `user` or `assistant`. Returning more than one message is how you supply a worked example: an assistant message showing the shape you want, followed by a user message with the real request.

The handler returns `core.PromptResponse`, a sealed interface with two implementations. `core.PromptResult` is the sync answer and the one you write most of the time. `core.InputRequiredResult` is the MRTR variant: return it to ask the client for sampling, elicitation, or roots input mid-call, and the client re-sends the same `prompts/get` with the answers attached. See [MRTR_TUTORIAL.md](MRTR_TUTORIAL.md).

The interface is sealed through an unexported marker method, so an external type cannot impersonate a response variant.

Runnable versions of everything on this page are in [`server/example_prompts_test.go`](../server/example_prompts_test.go) and run with the test suite.

## Arguments

Declare arguments on the definition so a client can prompt for them, then read them back in the handler:

```go
core.PromptDef{
    Name: "review",
    Arguments: []core.PromptArgument{
        {Name: "lang", Description: "Source language", Required: true,
         Schema: map[string]any{"type": "string"}},
    },
}
```

```go
lang, _ := req.Arguments["lang"].(string)
```

Two things to know.

**Arguments are decoded JSON, not strings.** `req.Arguments` is a `map[string]any` holding already-decoded values, so a JSON number arrives as `float64` and an object as `map[string]any`. String arguments need a type assertion.

**A `Schema` turns on server-side validation.** When an argument declares one, the dispatcher validates the incoming value before your handler runs and rejects a bad request with `-32602` and a structured error list. Arguments with no schema are passed through unchecked. Turn the call-time check off with `server.WithSchemaValidation(false)` if you would rather validate inside the handler.

Marking an argument `Required` is advertisement, not enforcement. Give it a schema if you want the dispatcher to reject a missing value.

## Content types

`PromptMessage.Content` is the same `core.Content` type tool results use, so a prompt can carry anything a tool result can.

Text:

```go
core.Content{Type: "text", Text: "Review this Go code."}
```

An image, base64 with a mime type:

```go
core.Content{Type: "image", Data: base64Data, MimeType: "image/png"}
```

An embedded resource:

```go
core.Content{
    Type: "resource",
    Resource: &core.ResourceContent{
        URI:      "file:///etc/app.conf",
        MimeType: "text/plain",
        Text:     "debug = true",
    },
}
```

Prefer an embedded resource over inlined text when the content has a URI the client already knows about. It lets the client attribute the content, re-fetch it, or link to it, instead of treating it as anonymous prose. `Text` and `Blob` on the embedded resource are mutually exclusive; use `Blob` with base64 for binary.

## Listing

`prompts/list` returns every registered definition with its description and arguments, which is what a client renders in its picker. mcpkit returns all prompts in a single page: `nextCursor` is always empty because the server's page size is fixed at "no pagination". See [COMPLETIONS.md](COMPLETIONS.md) for suggesting values for a prompt argument once the user starts typing.

Adding or removing a prompt at runtime through the registry broadcasts `notifications/prompts/list_changed`, so clients refresh without polling.

## Related

- [Argument completion](COMPLETIONS.md) for autocompleting prompt argument values
- [Getting started](GETTING_STARTED.md) for a first server end to end
- [Architecture](ARCHITECTURE.md) for where prompts sit in dispatch
