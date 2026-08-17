# lsp

Language servers in the agent loop, so the model works against what the
compiler thinks rather than against what it remembers writing.

`agent/ext/files` gave the agent byte-exact edits and `search_files`. Neither
knows what a symbol is. An agent renaming a method greps for the name, gets the
string matches, and cannot tell a call site from a comment that mentions it.

```go
ext, _ := lsp.New(lsp.Config{
    Root: "/work/project",
    Servers: []lsp.ServerSpec{
        {Command: []string{"gopls"}, Extensions: []string{".go"}, LanguageID: "go"},
    },
    Writes: []lsp.WriteSpec{
        {Tool: "edit_file", Paths: files.PathArg},
        {Tool: "write_file", Paths: files.PathArg},
    },
})
app, _ := host.NewApp(cfg, out, in, host.WithExtension(fx, ext))
```

`Command` comes from configuration and is deliberately unreachable from a tool
argument. A model's instructions can come from content it read, so an agent
that could name the subprocess it starts would be one injected instruction away
from running anything.

## Diagnostics arrive twice, for two different questions

|  | Within a turn | Across turns |
|---|---|---|
| Mechanism | appended to the write's tool result | transient `ContextStage` |
| The claim | "this edit introduced these errors" | "the file currently has these errors" |
| Lands in history | yes, and stays true there | never |

The split is the design, not an implementation detail.

A tool result joins the conversation permanently. That is right for a statement
about what an edit *caused*, which stays true as a record of the past. It is
wrong for a statement about *current state*, which stops being true the moment
the model fixes one of the errors, and which would then accumulate one stale
block per edit until the context held ten overlapping and mostly wrong
descriptions of the same file. `host.contextPipeline` already names that failure
for memory injection.

So why not put everything in the stage? Because **stages run once per turn**, at
`RunTurn`, while an edit-check-fix loop runs across the tool-call steps *inside*
a turn. A model that edits at step 3 and keeps working to step 12 would learn
nothing for nine steps. The tool result is the only path that reaches it in
time.

```
$ edit_file {"path": "cache.go", ...}
  wrote cache.go
  cache.go: 1 problem(s) after this edit:
    42:14: error: undefined: fmtt
```

The per-turn block reports only what is currently wrong, and says nothing at all
when nothing is. An all-clear repeated every turn spends context to say nothing
and trains the model to skip the section.

A server that does not answer in time is reported as not having answered:

```
cache.go: the language server did not report back within 8s
```

Silence would read as "no problems" and the diagnostics we happened to be
holding would read as problems this edit caused, sending the model to fix
something it already fixed.

### The first publication is not always the answer

Most servers publish once with what they found. Some compute in two phases and
publish an **empty set immediately**, then the real diagnostics after a slow
pass. rust-analyzer does this, with about two seconds between the two.

Taking the first publication there reports a file that does not compile as
clean, which is the worst direction for this to be wrong in. So each
publication restarts a settle timer and the last set wins, with
`Config.DiagnosticsTimeout` bounding the whole wait.

`ServerSpec.SettleDelay` sets that quiet period. Left at zero it is chosen from
the name the server reports at `initialize`: three seconds for rust-analyzer,
250ms for everything else. A table of known servers is not elegant and is what
every LSP client ends up with, because nothing in the protocol lets a server say
"that answer was provisional". The tell that a new server needs an entry is a
broken file reported as clean.

Content the server already has is not resent, so a navigation call and a
re-check over an unchanged file cost no round trip. That also matters for
clangd, which does not re-publish for a `didChange` carrying content it already
holds.

## The tools take a symbol name, not a position

LSP addresses every request by a cursor position, and a model has no cursor.
Asking it for one means asking it to count lines and columns out of `read_file`
output.

```
$ find_references {"path": "cache.go", "symbol": "CacheWrapper.Get"}
handler.go:31:18: 	v, err := c.Get(ctx, key)
worker.go:88:9: 	_ = cache.Get(ctx, id)
```

A bare name works when it is unique. When it is not, the call is refused rather
than resolved to a guess, which is the discipline `edit_file` already applies to
a non-unique anchor:

```
cache.go: "Close" is ambiguous: it matches Reader.Close, Writer.Close. Use the qualified name
```

Names are matched on the bare form, the qualified form, and whatever decorated
spelling the server uses, since gopls reports a method as `(*CacheWrapper).Get`.

**LSP 3.17 does not remove the cursor.** Its `positionEncoding` negotiation only
changes how a column is counted, and no LSP version has a name-addressed
request.

Servers disagree about whether to negotiate at all, so both paths are live:

| Server | `positionEncoding` |
|---|---|
| gopls, typescript-language-server, pyright | omitted, so utf-16 |
| rust-analyzer, clangd | `utf-8` |

An omitted value means utf-16 per spec, so columns arrive in code units and a
line containing an emoji counts differently from its bytes. That conversion
lives in one function and no position ever reaches the model.

A location outside the workspace is named but not quoted, since a definition in
a dependency is a real answer and reading that file would reach outside the root
that confines everything else.

## There is no `document_symbols` tool

The file outline is the repo map's question, and the repo map answers it across
the whole tree rather than one file at a time. Shipping both would mean two
mechanisms for one question, with the better one arriving later and having to
argue against an incumbent. `textDocument/documentSymbol` is called internally
for symbol resolution, so the capability is here and only the tool is withheld.

## Closing

This is the first extension to own a subprocess, which is why `host.Extension`
grew `Close`. `App.Close` calls it in reverse registration order. A server that
ignores `shutdown` and keeps running after its stdin closes is killed, because
a host that blocks forever on Close is worse than one that leaves an orphan.

## Wiring it with the file tools

`surfaces.WorkspaceExtensions` does the pairing, registering checkpoint, then
files, then this. The order is load-bearing in both directions: checkpoint must
see a file *before* it is written and this must see it *after*.

```go
exts, _ := surfaces.WorkspaceExtensions(surfaces.WorkspaceConfig{
    Root:            "/work/project",
    LanguageServers: []lsp.ServerSpec{{Command: []string{"gopls"}, Extensions: []string{".go"}, LanguageID: "go"}},
})
```

Language servers are opt-in there, unlike checkpoint, which is opt-out. A
checkpoint costs a directory and is wanted wherever writes are. A language
server is a subprocess and an index of the whole tree, and nothing can guess
which languages a workspace holds or which server should drive them.

## Testing

The suite drives a stub language server that is the test binary re-executed, so
it needs nothing installed and exercises spawn, framing, and teardown for real.
It proves nothing about whether gopls behaves the way the stub does.

The `lsp_live` tag runs the same surface against real servers and is not wired
into CI:

```bash
go test -tags lsp_live ./experimental/agent/ext/lsp/ -v
```

`TestLive*` drives one server, defaulting to `gopls` and overridable with
`LSP_LIVE_SERVER`. `TestServers` is the multi-language conformance probe: it
checks startup, `documentSymbol` decoding, symbol resolution by name, and that a
file which does not compile produces a diagnostic, for gopls,
typescript-language-server, pyright, rust-analyzer and clangd. Each subtest skips
when its binary is absent.

That probe is what found the provisional-publication bug (#1303), which shipped
because gopls was the only server ever tried. Adding a server to it is the
cheapest way to find the next assumption of that kind.

## Status

Stdio JSON-RPC client, diagnostics on both paths, two navigation tools, and the
host extension. No code actions: applying a server-proposed edit is a write, and
writes belong to `ext/files` for anchoring and `ext/checkpoint` for reversal
rather than going around both.
