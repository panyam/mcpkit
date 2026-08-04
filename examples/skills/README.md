# examples/skills

> ⚠ **Experimental** — tracks [SEP-2640](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2640) (Skills), a draft SEP. Wire format may change.

End-to-end example for SEP-2640 (Skills extension): a mcpkit server that
exposes Agent Skills under the `skill://` URI scheme, plus a demokit
walkthrough that drives a host against it.

> **Looking for the minimal shape?** This example is the **full** surface —
> archives (`.tar.gz` / `.zip`), remote source adapters (GitHub, archive
> directories, multi-source mounts), and fsnotify-driven invalidation. For the
> scoped-down core the WG blessed on 2026-06-30 (a skills file + tool handling,
> load-on-demand, no archives), start with [`examples/skills-core`](../skills-core).

The walkthrough exercises the SEP's wire shape step by step:
capability declaration, the discovery index, SHA-256 digest
verification, and reading both single-segment and nested-prefix skills.
Full step-by-step is in [WALKTHROUGH.md](./WALKTHROUGH.md) (regenerate
via `just readme`).

## Running

### Two-terminal walkthrough (recommended)

```bash
# Terminal 1: start the server
just serve

# Terminal 2: drive the walkthrough
just demo        # text mode (non-interactive)
just tui         # interactive TUI (paginated, key-driven)
just note        # notebook mode (Bubble Tea cells)
```

### Agent mode

```bash
just agent                                  # scripted agent, no LLM (also the golden test via just agent-test)
MODEL=qwen2.5-7b-instruct just agent-live   # a live model improvising against the same server
```

`just agent` runs a scripted agent (mcpkit's host layer plus a deterministic
`StubProvider`) against an in-process copy of this server, so it needs no second
terminal and no model. It shows what skills are *for* on the agent side: the
host discovers the server's skills, digest-verifies them, and injects them into
the model's system instructions before the first turn. The agent then answers a
git question by the team's `git-workflow` conventions and a PDF question by the
`pdf-processing` skill, with no tool call. The knowledge rides in the prompt.
The whole run is deterministic, so it doubles as a golden-transcript test
(`agent_scenario_test.go`). With `MODEL=... just agent-live` a real model derives the same
answers from the injected blocks instead of reciting scripted ones.

### Security & conformance harness

```bash
just security        # run the harness (no LLM, non-interactive, screen-shareable)
just security-test   # the same, as a Go regression test
```

`just security` runs the SEP-2640 host-side defenses over this fixture against
an in-process server (`security_demo.go`). Each step prints its anchor and a
`PASS`/`REJECT` outcome; the integrity, byte-budget, and scheme-rejection steps
assert the SEP-mandated rejection, so the harness doubles as a regression guard
(`security_demo_test.go`, wired into CI alongside the agent test). It was built
for the skills-over-MCP WG as a working answer to the open progressive-disclosure
question and a data point on where the resource-fetch bound belongs.

| Step | Shows | Anchor |
|------|-------|--------|
| 1. Progressive disclosure | Catalog carries frontmatter only; the body arrives on demand, digest-verified | SEP-2640 progressive disclosure · `experimental-ext-skills#85` · mcpkit #910 |
| 2. Supporting-file integrity | A pinned file verifies; an unlisted file is refused (`ErrSupportingFileUnpinned`); a post-listing tamper is rejected (`ErrDigestMismatch`) | threat model B1 · mcpkit #866 |
| 3. Resource-fetch byte budget | An over-cap read is rejected before decode (`ErrResourceTooLarge`) | threat model T6 · `experimental-ext-skills#831` · mcpkit #867 |
| 4. Cross-origin scheme rejection | A `file://` resource URI is refused (`ErrInvalidScheme`) | threat model T5 (`adv-file-url`) |

The same four defenses also appear as steps in the demokit walkthrough
(`just demo` / `WALKTHROUGH.md`, "Threat-model defenses" section), so they show
up in the narrated, screen-shareable flow alongside the rest of the extension.
The walkthrough runs against the external `just serve` process, so its
digest-mismatch step forces the mismatch with a wrong pin; the `just security`
harness is the one that stages a real post-listing on-disk tamper (it owns its
in-process server).

Deliberately not covered: whole-set / signed attestation (a separate build
track). Host-side origin tagging and per-origin name resolution (PR #1185) could
be added as a step 5 once demoing those is in scope.

### Other server modes

```bash
just serve-archive   # publishes each skill as one .tar.gz resource
just serve-zip       # publishes each skill as one .zip resource
```

In archive mode `resources/list` returns one URI per skill (e.g.
`skill://pdf-processing.tar.gz`) instead of N URIs per file. The
post-unpack virtual namespace hosts see is identical either way — that's
SEP-2640's whole-skill atomic-delivery story.

### Custom port or skills directory

```bash
go run . --serve --addr=:9090 --skills=./some-other-skills
```

## Bundled fixture

Three skills under `skills/` mirror the SEP-2640 Examples table:

| Skill path             | Files                                                       |
|------------------------|-------------------------------------------------------------|
| `git-workflow`         | `SKILL.md`                                                  |
| `pdf-processing`       | `SKILL.md`, `references/FORMS.md`, `scripts/extract.py`     |
| `acme/billing/refunds` | `SKILL.md`, `templates/email.md`                            |

`pdf-processing/SKILL.md` carries Extra frontmatter (`version`, `tags`)
to exercise the `Annotations` surface that surfaces extras under the
`io.modelcontextprotocol.skills/` reverse-domain prefix.

## Adding a real-world skill (anthropics/skills docx)

```bash
just fetch-docx
just serve
```

`just fetch-docx` clones the public [`anthropics/skills`](https://github.com/anthropics/skills) repo
to `/tmp/anthropics-skills-cache` (override via `ANTHROPIC_SKILLS_CACHE`),
copies the `docx` skill into `skills/docx`, and warns if the upstream
frontmatter `name` does not match the staged path (SEP-2640 requires
them to be equal). Re-running is idempotent.

The fetch-at-run-time pattern keeps this repo's history small and side-steps
licence ambiguity around vendoring third-party skill bundles. Same shape
as `scripts/apps-playwright-test.sh`.

## What the walkthrough proves

- The `io.modelcontextprotocol/skills` capability appears under
  `capabilities.extensions` in the `initialize` response (object, not
  array).
- `skill://index.json` round-trips and parses against the
  `https://schemas.agentskills.io/discovery/0.2.0/schema.json` shape.
- Every `skill-md` index entry's `digest` is `sha256:[a-f0-9]{64}` and
  matches `sha256.Sum256` of the served bytes — satisfies the SEP-2640
  MUST that hosts verify retrieved content against the digest.
- Resources/read works for both single-segment skills (`git-workflow`)
  and nested-prefix skills (`acme/billing/refunds`), confirming the
  prefix-segment routing is uniform.
- Relative references inside a skill (e.g. `templates/email.md` from
  `acme/billing/refunds/SKILL.md`) resolve via
  `skills.ResolveRelative(skillRoot, ref)`.

## Conformance

This server also doubles as the fixture for the SEP-2640 conformance
suite:

```bash
# from repo root
MCPCONFORMANCE_SKILLS_PATH=$HOME/newstack/mcpkit/conf-skills just testconf-skills
```

The Makefile target spawns this binary in `--serve` mode on `:18099`
before invoking the fork-side scenario runner. The fork-side Scenario
classes are tracked under mcpkit#567.

## Spec reference

- SEP-2640: <https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2640>
- Agent Skills specification: <https://agentskills.io/specification>
- Working group: skills-over-mcp-wg

## Next steps

- [Skills — the minimal core shape](../skills-core/)
