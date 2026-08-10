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

Three behaviours worth knowing:

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

Paths are absolute, so a checkpoint is valid on the machine that took it and
is not portable off it.

## What this does not cover

**Shell commands.** Capture happens before the call, reading the paths out of
the tool's arguments. What `make install` will touch is unknowable until it has
touched it. Covering it would mean snapshotting the whole tree before and
diffing after, which is correct and expensive. A tool with no reverser is
simply not checkpointed, which is why it stays irreversible for approval
purposes.

**Anything off this machine.** By construction. That is what `Compensate` is
for, and why the harness will not run it for you.

## Status

This module ships the seam and the file store. The `host.Extension` that wires
them into a turn (capture at depth 0, `/undo`, `/checkpoints`) is the second
half of #1267.
