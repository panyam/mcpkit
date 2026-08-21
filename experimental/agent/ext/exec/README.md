# exec

Running project commands from an agent, where the operator names the commands
and the model chooses among them.

## What the model may name

Nothing. That is the whole design.

`Config.Commands` is an allowlist the operator writes, and each entry becomes
its own tool. The model picks a tool. There is no parameter anywhere that
carries a command, a binary, a flag string, or a path to a script, because a
model's instructions can come from content it read rather than from the user,
and `agent.Spotlight` exists because the two are not distinguishable after the
fact. An agent that can name the process it starts is one injected instruction
away from starting anything.

```go
src, _ := exec.NewSource(exec.Config{
    Roots: []string{"/work/mcpkit"},
    Commands: []exec.CommandSpec{{
        Name:        "test",
        Argv:        []string{"go", "test"},
        Description: "Run the Go tests.",
        Args:        &exec.ArgPolicy{Max: 3, Match: `\./[\w./-]*(\.\.\.)?`, Paths: true},
        ReadOnly:    true,
    }, {
        Name:        "build",
        Argv:        []string{"go", "build", "./..."},
        Description: "Compile every package.",
        Reversible:  true,
    }},
})
```

That registers `run_test` and `run_build`. Nothing is passed to a shell, so an
argument containing `;` or `$(` is an argument.

## One tool per command, not one tool taking a name

The obvious shape is a single `run_command` whose `name` parameter is an enum
over the allowlist. It contains the model equally well and it cannot express
the thing an operator actually wants, which is that `go build` may run
unattended while `terraform apply` asks first.

Safety annotations are per tool. Collapsed into one tool, every command shares
one `destructiveHint`, and `agent.ModeReversibleAuto` either prompts for all of
them or none.

| Spec field | Annotation | Effect on the approval ladder |
|---|---|---|
| `ReadOnly` | `readOnlyHint: true` | auto-allowed from `ModeReadOnlyAuto` up |
| `Reversible` | `destructiveHint: false` | auto-allowed from `ModeReversibleAuto` up |
| neither | nothing declared | asked under every mode below `ModeAlwaysAllow` |

Declaring nothing means destructive, because that is what an absent
`destructiveHint` means to the spec. A command nobody classified gets asked
about. The operator who wrote the allowlist is the one who knows which entries
are safe to run unattended, and a default that guessed on their behalf would
guess in the permissive direction.

## Arguments, when they are permitted at all

`Args` is nil by default and the tool then exposes no argument. A command that
accepts free-form arguments is a weaker statement than the allowlist appears to
be making.

When set, `Match` is required and is anchored end to end. An unanchored pattern
is a substring pattern: `\./\w+` would accept `./pkg;rm -rf ~` on the strength
of the `./pkg` inside it. `Paths` additionally requires every argument to
resolve inside a root, through symlinks, and applies to every argument rather
than to the ones that look like paths, so a command mixing flags and paths
cannot use it.

Path checking resolves symlinks on the longest existing part of the path and
keeps the rest. Requiring the whole path to exist would reject `./agent/...`,
which names no file; skipping symlinks would accept a link inside a root
pointing out of one.

## The sandbox

`Config.Sandbox` confines every command. Nil selects the platform's backend and
**fails construction when there is none**, rather than running unconfined.

| Platform | Backend |
|---|---|
| darwin | `sandbox-exec`, a generated Seatbelt profile |
| everything else | none; construction refuses |

`exec.Unconfined()` is the opt-out and has to be written in the config. It is
the right setting for a process an operator already isolated, which is the
ordinary deployed case: a container the operator built is the boundary, and a
second fence inside it protects nothing. Selecting it by accident is what the
design prevents, not selecting it at all.

Linux has no backend yet. Delegating to `bubblewrap` is the obvious shape and
could not be verified here, because CI runs on `ubuntu-latest` where the
AppArmor restriction on unprivileged user namespaces can block `bwrap`
outright, so it would have shipped less proven than the darwin path. Issue 1320
starts by establishing what a runner actually permits, before writing anything.

### What the profile does

- **Writes** are denied, then opened onto the roots and `WritePaths`. A path
  missing from that list fails loudly, which is the direction to fail in.
- **Reads** are open, then carved back by `DenyRead`. Confining reads to the
  workspace breaks every toolchain, which reads compilers, SDKs, module caches
  and system libraries from all over the disk, and the breakage reads as a bug
  rather than as policy. What is left is credential theft, which the default
  denials address directly and not exhaustively.
- **Network** is loopback only unless a command sets `AllowNetwork`. A test
  suite that starts a server on `127.0.0.1` is the ordinary case; egress is
  what lets a build script post the workspace somewhere.

`DefaultWritePaths` covers the temp directory and the build and module caches,
because a sandboxed `go build` fails on its build cache before it compiles
anything. That widens confinement past the roots and is worth knowing: under
the defaults, "outside every root" and "denied" are different statements.
`WritePaths: []string{}` is the roots-only setting.

Paths in the profile are symlink-resolved and emitted in both spellings.
Seatbelt matches the resolved path, so a rule naming `/tmp` covers nothing on
darwin where `/tmp` links to `/private/tmp`, and a read denial that appears to
be in force while permitting the read is worse than an absent one.

## Why the sandbox is here and not around a `ToolSource`

The coding epic routed sandboxed execution to a `ToolSource` wrapper. A wrapper
has nothing to confine. Most tools in this tree are in-process Go functions or
MCP calls with no subprocess in them, and isolating those would mean isolating
the agent process, which is a deployment decision. Only the code that spawns
can confine what it spawned.

**Extension-owned subprocesses are out of scope, deliberately.** `ext/lsp`
spawns `gopls` from `ServerSpec.Command`, outside the tool path, where no
wrapper reaches it. That command is operator-supplied and unreachable from any
tool argument, which is the same property that makes `client.CommandTransport`
a sanctioned exception under C6. A convention that every extension route its
spawns through a shared helper would be enforcement in name only. Recorded on
issue 1312.

## What the sandbox is not

It is not the primary defense. The approval gate is. The sandbox earns its
place under `ModeAlwaysAllow` and the auto modes, which is when these run
unattended, and as the thing that keeps the allowlist honest.

`sandbox-exec` has also been deprecated for years with an undocumented profile
language. The golden test pins what we generate. It cannot pin that Apple keeps
honoring it, and the live suite is the only thing that checks the other half.

### The allowlist names commands, not capabilities

This is the part to understand before trusting an allowlist, and it is stronger
than a composition problem between two extensions.

**Most useful entries execute workspace code by design.** `make test` runs
whatever the Makefile says. `go test` compiles and runs test code from the
repo. `npm run build` runs the scripts in `package.json`. So the allowlist
constrains which *program* starts, and for any real build command it says
almost nothing about what that program will do, because the program's behaviour
is defined by files in the workspace.

`ext/files` makes this sharper by letting the agent rewrite those files first,
but the property holds without it. A repository is content, and running a build
in a repository you did not write is running that repository's code. That is
true of a cloned dependency and of a PR branch under review, with no model
involved at all.

**A generic binary with a loose `ArgPolicy` is close to a shell.** `git` with
`Match: ".*"` reaches `-c core.pager=`, aliases, and hooks. Nothing here
detects that: `Match` is required to be non-empty and `.*` satisfies it. Keep
patterns narrow, and prefer a specific subcommand in `Argv` over a bare binary
plus permissive arguments.

What follows is that for a test or build loop the allowlist is a statement of
*intent*, useful for review and for the approval prompt, and the sandbox is
what does the containing. Treating the allowlist as the boundary is the mistake
this section exists to prevent.

## Reading the result

A non-zero exit is a normal result with `exit_code` set, not a tool error. The
point of running a build is to learn that it failed, and flagging that as an
error tells the model the tool malfunctioned rather than that the code did.

A timeout is a tool error, because the command did not finish and nothing it
printed is a verdict. The command runs in its own process group and the whole
group is killed, since a command is normally a launcher whose children do the
work and outlive it.

Output is capped at `MaxOutput` and dropped from the **middle**. A failing
build states the problem in the first lines or the last ones and almost never
in between. A capped result says how many bytes went missing, because a model
that cannot tell a truncated run from a complete one reads a passing tail as a
passing run.

## Wiring it into a host

```go
ext, err := exec.New(exec.Config{Roots: roots, Commands: commands})
if err != nil {
    return err
}
app, err := host.NewApp(cfg, host.WithExtensions(ext))
```

The approval prompt shows the resolved command line, the directory, and what is
confining it. The host default renders the call's JSON trimmed to 200
characters, which for these tools is an argument array with no sign of the
command it attaches to.

```
Run test?

  go test ./experimental/agent/...
  in /work/mcpkit
  confined by sandbox-exec, no network
```

## Tests

`go test ./...` needs nothing installed: commands are this test binary
re-executed, the way `ext/lsp` drives its stub server.

`go test -tags exec_live ./...` runs the real `sandbox-exec` and is darwin-only.
CI runs on Linux, so **nothing in CI exercises a real sandbox**. The live suite
is what caught the symlink-resolution bug above, and it is worth running before
believing a change to the profile.

## Status

Experimental, on the unreleased agent line. Issue 1312; child of the coding
epic 1252. The Linux backend is 1320, and the test and build feedback loop that
consumes this is 1313. Two open questions about the tool set itself: 1323 on
what happens when an allowlist gets large, and 1324 on whether it may change
after construction and who is allowed to widen it.
