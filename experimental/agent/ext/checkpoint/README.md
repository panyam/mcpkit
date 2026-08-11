# checkpoint

Makes an agent's side effects undoable, by letting whoever wrote a tool say how
to reverse it.

## Why a seam rather than a file snapshot

The host cannot know how to undo `create_issue`, `insert_row`, or `deploy`. It
could know how to undo a file write, but only because restricting "undo" to
files assumes the answer. So reversal is a seam that tools plug into, and the
file snapshot here is its first implementation rather than its definition.

## Restore and compensate are not the same thing

Two operations get called "undo" and only one is safe to run unattended. This
distinction is the reason `Reversal` has two fields instead of one.

|  | Restore | Compensate |
|---|---|---|
| Example | put a file back | delete an issue that was created |
| Inverse? | yes, you land exactly where you were | no, the issue existed and was deleted; notifications fired |
| Order matters? | no | yes |
| Survives intervening work? | yes | no, a linked issue cannot be deleted cleanly |
| Likely to fail? | rarely, it is local | often: permissions, locks, network |
| Harness may run it automatically | **yes** | **no** |

The harness runs restores and surfaces compensations for a human to decide on.
Chaining compensations automatically, with ordering and partial-failure
recovery, is a saga orchestrator, and constraint A8 rules one out of this repo.

```go
type Reversal struct {
    Restore    func(ctx context.Context) error
    Compensate *agent.ToolCall
}
```

`Reversible()` reports whether a `Restore` exists. A compensation alone is
deliberately not reversible: it is a new call with consequences of its own, so
counting it would let a tool auto-approve on the strength of an offset nobody
verified.

## The file store

Content-addressed. Each captured file is hashed, blobs are shared across
checkpoints, and a per-checkpoint manifest maps path to hash.

```
<root>/blobs/ab/cdef...        contents, named by sha256
<root>/manifests/<id>.json     {path -> hash}
```

Content addressing is what makes a checkpoint per turn affordable: a file
captured across twenty turns and edited in one costs two blobs, not twenty.

```go
store, _ := checkpoint.NewStore(".mcpkit/checkpoints")
cp, _ := store.Open("turn-42")
cp.Add("/work/src/a.go", "/work/src/b.go")   // captures current content
// ... the agent edits both ...
cp.Restore()                                  // back to what Add saw
```

Four behaviours worth knowing:

- **First capture wins.** Re-adding a path already in the checkpoint is a
  no-op, so the checkpoint holds the state at the start of the turn. Capturing
  on every write would build a restore point that undoes only the last edit.
- **A missing file is recorded as absent.** Restoring it deletes whatever the
  call went on to create, which is what undoing a creation means.
- **Restore is idempotent, not atomic.** POSIX has no multi-file rename, so a
  restore stages everything to temp files first (a missing blob or unwritable
  directory fails before anything is touched) and then renames them into
  place. A failure during the rename phase leaves a mix. The fix is to run it
  again: restoring the same checkpoint twice reaches the same state, and the
  manifest is never consumed.
- **A path that changed shape is refused, not restored.** See below.

Paths are absolute, so a checkpoint is valid on the machine that took it and
is not portable off it.

## Restoring only what is still the thing that was captured

A checkpoint records what it found at each path: a regular file's content, that
the path was missing, or that it was something else. Restore compares that
against what is there now, and declines any path where the two disagree.

The gap this closes is unusually wide. Capture happens at the top of a turn and
a restore happens whenever someone types `/undo`, so a path has minutes or
hours in which to become a symlink pointing somewhere else. Renaming onto it
writes the old content to the link's target, and for a path captured as absent
the restore is an `os.Remove`, which deletes that target outright. Neither is a
race in any tight sense; both are just what happens next if nothing looks.

```
turn-7: 3 file(s) restored

1 path(s) REFUSED (not restored):
  /work/notes.md — is now a symlink, was regular at capture
```

A refusal is per path, so one tampered file does not cost you the rest of the
turn. It is deliberately not a containment check: this package has no workspace
root and does not want one, because a checkpointed tool may legitimately write
to a cache or temp directory outside any single tree. Confining paths is the
**tool's** job, and `agent/ext/files` is the worked example of doing it with an
`os.Root` handle. What this adds is narrower and needs no root: restoring
through a thing that is not what you captured is not something any caller
wants, whatever their layout.

Capture uses `Lstat`, so a path that is *already* a symlink is recorded as
unsupported rather than followed. Following it would copy the target's content
into the blob store and set up a restore that writes back to a file the caller
never named.

Through the `Reversal` seam a refusal surfaces as an **error**, because the
harness runs restores unattended (constraint A11) and has nowhere to put
detail. `/undo` calls `Restore` directly and prints each refusal in full.

## What this does not cover

**Shell commands.** Capture happens before the call, reading the paths out of
the tool's arguments. What `make install` will touch is unknowable until it has
touched it. Covering it would mean snapshotting the whole tree before and
diffing after, which is correct and expensive. A tool with no reverser is
simply not checkpointed, which is why it stays irreversible for approval
purposes.

**Anything off this machine.** By construction. That is what `Compensate` is
for, and why the harness will not run it for you.

## Wiring it into a host

```go
cp, _ := checkpoint.New(checkpoint.Config{
    Root: ".mcpkit/checkpoints",
    Writes: []checkpoint.WriteSpec{{
        Tool:  "write_file",
        Paths: func(args map[string]any) []string {
            p, _ := args["path"].(string)
            return []string{p}
        },
    }},
})
app, _ := host.NewApp(cfg, out, in, host.WithExtension(cp))
```

`WriteSpec` says which arguments name files. A tool that is not listed is
never captured, which is not the same as being ignored: see below.

Because a `WriteSpec` is keyed by **tool name** and supplied by whoever builds
the host, a tool becomes checkpointable without either package importing the
other. `agent/ext/files` is the worked example: it exports a plain
`func(map[string]any) []string` and this package keeps its own struct.

```go
checkpoint.WriteSpec{Tool: "edit_file", Paths: files.EditPaths}
```

A tool declaring its own `Reverser` here instead is the obvious thing to
reach for and is what constraint C4 rules out: this package's API would become
implicitly stabilized for that tool's benefit with no design decision saying
the two must interoperate.

- **One checkpoint per turn**, created lazily on the first captured write, so a
  read-only turn costs nothing on disk.
- **Depth 0 only.** A sub-agent's writes land inside the parent's checkpoint.
  The useful restore point is the turn, not each frame of the agent tree.
- **`/undo`** restores the most recent checkpoint, or a named one.
  **`/checkpoints`** lists them.

## What `/undo` says about what it could not undo

A turn that edits three files and creates a GitHub issue would report "3 files
restored" under any design that only knows what it captured, and the issue
would go unmentioned. So the extension records every call that ran, changed
something, and offered no reverser:

```
turn-7: 3 file(s) restored

1 call(s) had no reverser and were NOT undone:
  create_issue {"title":"flaky test in CI"}
```

Read-only calls and calls the permission gate denied are deliberately absent
from that list: neither had anything to undo, and padding the list would train
you to skip it.

## When a tool has no reverser

**Approval is the first line, not undo.** A tool with no reverser is
irreversible, so `ModeReversibleAuto` asks before it runs. The best time to
handle an un-undoable action is before it happens.

**Then a model can propose an offset.** Set `Config.Proposer` and `/undo` asks
it for inverse calls covering the gaps:

```
turn-7: 1 file(s) restored

1 call(s) had no reverser and were NOT undone:
  create_issue {"title":"flaky test in CI"}

1 offset(s) proposed. None runs without your approval:
  delete_issue map[id:41]
    why: removes the issue that was filed
    ran
```

Three tiers, in decreasing certainty:

| Tier | Who decides what runs | Who runs it |
|---|---|---|
| `Restore` | tool author, at capture time | harness, unattended |
| `Compensate` | tool author, declared | human approves, harness runs it |
| model-proposed | the model, at undo time | human approves each, harness runs it |

**Nothing but a `Restore` ever runs unattended.** A nil `Approve` means no
proposal runs at all, and the report says so per proposal rather than skipping
quietly. An approval prompt that errors is a reason not to act.

### Why the proposer gets a fresh conversation

`ModelProposer` runs its Runner over a slice built only from the gap list. The
turn's own history never reaches it.

That is a security property. If the turn went wrong because of prompt
injection — content in a fetched page that read as instructions — then running
the cleanup inside that same context is asking the attacker to write the
cleanup. Spotlighting (`agent.Spotlight`) exists because such content is
indistinguishable from instructions once it is in the transcript; the answer
here is to not carry the transcript over. The gap list is itself untrusted for
the same reason, which is the other half of why a proposal is a suggestion to
a human rather than an instruction to the harness.

Build the proposer's Runner with `ProposalSchema()`, its own provider, and no
memory:

```go
r, _ := agent.NewRunner(agent.RunnerConfig{
    Provider:       provider,
    Tools:          tools,
    ResponseSchema: checkpoint.ProposalSchema(),
})
mp, _ := checkpoint.NewModelProposer(r)
```

## Status

Seam, file store, host extension, and the model-proposed tier.
