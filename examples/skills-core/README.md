# examples/skills-core

> ⚠ **Experimental** — the minimal [SEP-2640](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2640) (Skills) shape, a draft SEP. Wire format may change.

The **minimal** SEP-2640 shape: a skills file served over MCP's Resources
primitive, plus tool handling, consumed load-on-demand. This is the
scoped-down core the Skills Over MCP Working Group blessed at its
2026-06-30 meeting ([discussion 2994](https://github.com/modelcontextprotocol/modelcontextprotocol/discussions/2994)).

If you want the full surface — archives (`.tar.gz` / `.zip`), remote source
adapters (GitHub, archive directories, multi-source mounts), and
fsnotify-driven index invalidation — see [`examples/skills`](../skills). Those
are the deferred / extended features and are deliberately left out here.

## What this shows

1. `resources/list` — every file under a skill directory is a `skill://` resource.
2. `skill://index.json` — the discovery catalog, generated from the live provider (never on disk), each entry carrying a SHA-256 digest.
3. Read a `SKILL.md` — a skill is data: YAML frontmatter (`name`, `description`) plus a markdown body of instructions. mcpkit delivers it over `resources/read`; it is never staged to disk and never executed.
4. **Tool handling** — the bundled `commit-helper` skill instructs the host to call the `format_commit` tool. The walkthrough reads the skill, then calls the tool. Skills make ordinary tools easier to use well.

## Run it

```
Terminal 1:  make serve   # skills-core server, file mode, :8080
Terminal 2:  make demo    # this walkthrough (interactive TUI)
```

Or drive it non-interactively:

```
make serve            # in one terminal
go run . --non-interactive
```

Wire mode: mcpkit's server defaults to dual-wire (SEP-2575). The first step
lets you pick `adaptive` (probe `server/discover`, fall back to `initialize`),
`stateless`, or `legacy`; the rest of the walkthrough is identical either way.

## The skill

`skills/commit-helper/SKILL.md` describes a task and points the host at a
tool:

```markdown
---
name: commit-helper
description: Turn a change description into a Conventional Commits subject line using the format_commit tool
---
...call the `format_commit` tool with type / scope / summary...
```

The server registers `format_commit` alongside the skill provider
(`main.go`). That pairing — a skills file plus the tool it references — is the
minimal shape in one example.

## Safety posture

`ext/skills` treats a skill as data delivered over resource primitives, never
as code to run: it does not import `os/exec` and never writes skill content to
disk. That invariant is enforced by `TestNoCodeExecutionSurface` in the
`ext/skills` package.

See [`WALKTHROUGH.md`](WALKTHROUGH.md) for the generated step-by-step (run
`make readme` to regenerate).

## Next steps

- [Skills — the full surface (archives, remote, fsnotify)](../skills/)
