# files

File reading and editing for an agent, where an edit written against a stale
view of a file fails instead of applying.

## The failure this prevents

An agent reads a file, decides on a change, and writes it back. In between,
the file changed underneath: a formatter ran, a sibling tool wrote it, the
user saved in their editor. An edit addressed by line number applies anyway
and silently discards that change, and nothing reports it, because nothing
noticed.

## Two mechanisms, two questions

|  | Anchor (the text being replaced) | Hash of the content |
|---|---|---|
| Answers | where does this change go | is this still the file I read |
| Survives | lines moving around | nothing, by design |
| Misses | a file reformatted underneath | where anything belongs |

Neither substitutes for the other, which is why both are here. An anchor can
still match uniquely in a file that was reformatted after you read it, and
that is exactly the case where applying it is wrong.

```go
src, _ := files.NewSource(files.Config{Root: "/work/project"})
```

`Root` is required and has no "unset means anywhere" mode. These tools are
driven by a model, and a model's instructions can come from content it read
rather than from the user, which `agent.Spotlight` exists because you cannot
distinguish after the fact. An unconfined editor turns any such instruction
into a write to an arbitrary path. Symlinks are resolved before the
containment check, so a link inside the root pointing out of it is refused
rather than followed.

## The tools

**`read_file`** returns the content and a hash of it.

```
path: notes.md
hash: dcf19a8dce4a

# Notes
...
```

**`edit_file`** takes that hash as `expect_hash`, plus a list of exact
replacements. It reports the new hash, so a follow-up edit needs no re-read.

Three refusals, all reported as `IsError` results rather than dispatch errors,
so the model gets them back and can correct rather than the turn aborting:

```
notes.md: file changed since it was read: expected dcf19a8dce4a, found 9dd516a4fd95; re-read it before editing
notes.md: hunk 0: anchor is not unique: "todo: write the" matches 2 places; extend it until it matches one
notes.md: hunk 0: anchor not found: "todo: write the middle"
```

Matching is byte-exact. There is no fuzzy or whitespace-normalized fallback,
because approximate matching is the mechanism that produces silently wrong
edits, which is the thing this package exists to prevent. An anchor that does
not match gets an error and a chance to look again.

All edits in one call apply together or none do, and every anchor resolves
against the original content rather than against the result of earlier edits.
So listing order cannot change the outcome, and an edit cannot match text an
earlier edit introduced.

## Wiring it into a host

```go
ext, _ := files.New(files.Config{Root: "/work/project"})
app, _ := host.NewApp(cfg, out, in, host.WithExtension(ext))
```

The extension contributes the tools and a prompt section stating the rules the
tools enforce. Those travel together on purpose: a model that has not been
told anchors must be unique will keep sending single-word anchors and keep
being refused, so shipping the tools alone would make the contract
discoverable only by failing.

## Undo, and why there is no Reverser here

An edit is checkpointable through `agent/ext/checkpoint`, but this package
contains no reversal code and does not import that one:

```go
cp, _ := checkpoint.New(checkpoint.Config{
    Root:   ".mcpkit/checkpoints",
    Writes: []checkpoint.WriteSpec{{Tool: "edit_file", Paths: files.EditPaths}},
})
app, _ := host.NewApp(cfg, out, in, host.WithExtension(ext, cp))
```

`checkpoint.WriteSpec` is a declaration keyed by tool name, supplied by the
wiring, so the two features compose at the config layer with no import edge in
either direction. `EditPaths` is a plain function rather than a `WriteSpec`
precisely to keep it that way. The alternative, this package declaring its own
`checkpoint.Reverser`, would stabilize checkpoint's API for this package's
benefit with no design decision saying the two must interoperate, which is the
coupling constraint C4 exists to prevent.

## Status

Anchored edit engine, the two tools, and the host extension. Not yet here:
whole-file creation and `write_file`, directory listing, and search.
