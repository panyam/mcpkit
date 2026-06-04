# examples/skills

End-to-end example for SEP-2640 (Skills extension): a mcpkit server that
exposes Agent Skills under the `skill://` URI scheme, plus a demokit
walkthrough that drives a host against it.

The walkthrough exercises the SEP's wire shape step by step:
capability declaration, the discovery index, SHA-256 digest
verification, and reading both single-segment and nested-prefix skills.
Full step-by-step is in [WALKTHROUGH.md](./WALKTHROUGH.md) (regenerate
via `make readme`).

## Running

### Two-terminal walkthrough (recommended)

```bash
# Terminal 1: start the server
make serve

# Terminal 2: drive the walkthrough
make demo        # text mode (non-interactive)
make tui         # interactive TUI (paginated, key-driven)
make note        # notebook mode (Bubble Tea cells)
```

### Other server modes

```bash
make serve-archive   # publishes each skill as one .tar.gz resource
make serve-zip       # publishes each skill as one .zip resource
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
make fetch-docx
make serve
```

`make fetch-docx` clones the public [`anthropics/skills`](https://github.com/anthropics/skills) repo
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
MCPCONFORMANCE_SKILLS_PATH=$HOME/newstack/mcpkit/conf-main make testconf-skills
```

The Makefile target spawns this binary in `--serve` mode on `:18099`
before invoking the fork-side scenario runner. The fork-side Scenario
classes are tracked under mcpkit#567.

## Spec reference

- SEP-2640: <https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2640>
- Agent Skills specification: <https://agentskills.io/specification>
- Working group: skills-over-mcp-wg
