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
src, _ := files.NewSource(files.Config{Roots: []string{
    "/work/mcpkit",
    "/work/mcpkit-contribs",
}})
```

`Roots` is required and has no "unset means anywhere" mode. These tools are
driven by a model, and a model's instructions can come from content it read
rather than from the user, which `agent.Spotlight` exists because you cannot
distinguish after the fact. An unconfined editor turns any such instruction
into a write to an arbitrary path.

**A workspace is a set of directories, and one is the degenerate case.** A
coding session that stays inside a single repository is the exception: the
ordinary task changes an API in one repo and fixes its callers in another. An
agent given one root edits the API, reports success, and never sees what it
broke.

Naming a common parent instead of listing the roots is not a shortcut. It
silently widens confinement to everything else under that parent, which is the
property this exists to deny.

**Results are absolute paths.** Two repositories both holding `src/main.go` is
normal, so a bare relative path would name either. A relative path on the way
*in* still resolves against the first root, which is what keeps a single-root
workspace behaving as it always did.

The confinement is a directory handle (`os.Root`), not a string comparison.
Checking a path and then opening it by name are two separate operations, and
every way out of a workspace lives in the gap between them:

- A symlink is followed when the file is **opened**, not when the path is
  checked. `workspace/notes.md -> /etc/passwd` passes any comparison of the
  path text and then reads the wrong file.
- The filesystem can change between the two. A name that was a regular file
  when it was checked can be a symlink a moment later, so a check that
  happened earlier is a statement about the past.

`os.Root` resolves each component at open time against the directory it holds
and refuses anything that leaves it, which collapses the check and the use
into one act. There is one handle per root, and a path has to land inside one
of them. Escape attempts come back as refusals:

```
refusing ../../etc/passwd: outside every workspace root
refusing innocent.md: outside every workspace root      # a symlink pointing out
```

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

## Creating and replacing whole files

`edit_file` needs existing content to anchor against, so creating a file and
replacing one outright are `write_file`'s job. It carries the same discipline,
expressed through `expect_hash` rather than through a separate flag:

| Call | Meaning |
|---|---|
| no `expect_hash` | create; **refused** if the path already exists |
| `expect_hash` set | replace, only if the content still hashes to it |

There is deliberately no way to spell "write this, whatever is there". A
create-or-overwrite mode would reopen the hole `edit_file` exists to close, and
it would be the mode a model reached for the moment anchoring felt awkward.

```
$ write_file {"path": "notes.md", "content": "..."}
  [IsError] refusing notes.md: it already exists. Read it first and pass
            expect_hash to replace it, or use edit_file to change part of it.
```

Missing parent directories are created. Replacing a file keeps its mode, so
editing a script does not disarm it.

## Finding things

`list_files` walks the workspace; `search_files` matches lines and returns
them as `path:line: text`.

```
$ list_files {"dir": "agent/ext"}
agent/ext/files/edit.go
agent/ext/files/tools.go
...

$ search_files {"query": "func Get\\w+"}
a.go:12: func GetUser() (*User, error) {
a.go:31: func GetOrder(id string) (*Order, error) {
```

**The query is a regex** (RE2, so no catastrophic backtracking). Pass
`literal: true` for text containing `( ) [ ] . * + ? | \ $`. A pattern that
does not compile comes back as a refusal naming it, never as zero matches:
"no matches" and "your pattern was malformed" are answers you act on
differently, and reporting the second as the first sends you looking for code
that is sitting right there.

**Both tools say what they left out.** A capped listing that stayed quiet about
being capped reads as the complete contents of a directory, so it reports the
cap, the directories it skipped, and the files it would not search:

```
showing 100 of 4312 match(es); narrow the query or raise limit

3 file(s) not searched: 2 binary, 1 over 2 MiB, 0 unreadable
1 directory was skipped: node_modules
```

Those limits bound the work a call does. They are **not** a context-window
mechanism, and deliberately do not try to be: `agent.OffloadingSource` already
stores oversized tool results out of band behind a `read_tool_result` stub, and
a second truncation mechanism here would only disagree with it.

`Config.Exclude` sets which directories are skipped, defaulting to
`DefaultExclude` (`.git`, `node_modules`, `vendor`, and similar). A nil slice
means the default; an explicitly empty one means exclude nothing. It does not
read `.gitignore`, which is a different question: that file says what should
not be *committed*, and a `.env` is ignored while sometimes being exactly what
you need, while a vendored dependency is committed and is usually noise.

Traversal uses the same root handle as everything else, so it cannot leave the
workspace. A symlinked directory is listed as a name but never descended into,
which also means a link cycle cannot hang a walk.

## Wiring it into a host

```go
ext, _ := files.New(files.Config{Roots: []string{"/work/project"}})
app, _ := host.NewApp(cfg, out, in, host.WithExtension(ext))
```

The extension contributes the tools and a prompt section stating the rules the
tools enforce. Those travel together on purpose: a model that has not been
told anchors must be unique will keep sending single-word anchors and keep
being refused, so shipping the tools alone would make the contract
discoverable only by failing.

`files.NewSource` returns the bare `agent.ToolSource` instead, for a `Runner`
with no `agent/host`. Prefer `New` wherever there is a host, for the reason
above.

To get `/undo` over these writes, pair it with `agent/ext/checkpoint`,
registered **first** so its snapshot is taken before the write lands.
`surfaces.WorkspaceExtensions` does that pairing, and is what `agentchat
--workspace` and `agentweb --workspace` use.

## What the approval prompt shows

When a write is gated, the host asks before it runs. Its default rendering is a
call's JSON trimmed to 200 characters, which for an edit truncates the one
thing you are being asked to judge:

```
Allow tool call "edit_file" with {"path":"main.go","expect_hash":"dcf19a8dce4a"…
```

This extension renders its own writes instead, so the question is about the
change rather than the arguments:

```
Apply 2 change(s) to main.go?

  - func Old() {}
  + func New() {}

  - return nil
  + return fmt.Errorf("not implemented")
```

A whole-file write shows a capped preview and says whether it creates or
replaces, since those differ by whether anything is destroyed. Everything this
package does not own falls through to the host default untouched.

## Undo, and why there is no Reverser here

An edit is checkpointable through `agent/ext/checkpoint`, but this package
contains no reversal code and does not import that one:

```go
cp, _ := checkpoint.New(checkpoint.Config{
    Root:   ".mcpkit/checkpoints",
    Writes: []checkpoint.WriteSpec{
        {Tool: "edit_file", Paths: files.PathArg},
        {Tool: "write_file", Paths: files.PathArg},
    },
})
app, _ := host.NewApp(cfg, out, in, host.WithExtension(ext, cp))
```

`checkpoint.WriteSpec` is a declaration keyed by tool name, supplied by the
wiring, so the two features compose at the config layer with no import edge in
either direction. Every writing tool here names its target in the same `path` argument, so one
`PathArg` serves them all. It is a plain function rather than a `WriteSpec`
precisely to keep it that way. The alternative, this package declaring its own
`checkpoint.Reverser`, would stabilize checkpoint's API for this package's
benefit with no design decision saying the two must interoperate, which is the
coupling constraint C4 exists to prevent.

## Status

Anchored edit engine, five tools (`read_file`, `edit_file`, `write_file`,
`list_files`, `search_files`), and the host extension.
