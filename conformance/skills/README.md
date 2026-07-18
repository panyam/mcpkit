# SEP-2640 Skills Conformance

Drives the SEP-2640 skills conformance scenarios from the
`panyam/mcpconformance` fork against the `examples/skills` server
fixture.

## What runs

The scenarios live in the fork at
`src/scenarios/server/skills/` and are wired to the check IDs in
`src/seps/sep-2640.yaml` (25 MUST/SHOULD requirements extracted from
SEP-2640).

Coverage in this revision:

- URI shape: name matches final segment of `<skill-path>`
- No nested skills (negative)
- Manifest required at root (negative)
- Frontmatter required fields: `name`, `description`
- Index `$schema` URI present
- Index entry required fields: `name` and `digest` for every entry
  (both `skill-md` and `archive`)
- Digest format (`sha256:` + 64 lowercase hex)
- Digest correctness (recompute over the served bytes, compare)
- Archive safety: reject path traversal, absolute paths, escaping
  symlinks, total-unpacked-size cap
- Archive URL suffix, mimeType, SKILL.md at archive root
- Capability declaration value is the empty object `{}`, not `[]`
- Resource metadata: mimeType, Name, Description from frontmatter
- `_meta` reverse-domain prefix
- Host loads by URI alone (no index required)
- Server exposes `skill://index.json`

## Prerequisites

1. `panyam/mcpconformance` worktree (branch `chore/sep-2640-yaml`) at
   `~/newstack/mcpkit/conf-skills` (the default `MCPCONFORMANCE_SKILLS_PATH`).
   Override per-invocation if the branch moves.
2. Node.js and npm available for the vitest runner.
3. `examples/skills` builds clean (the target builds it before running).

## How to run

```bash
# default path
just testconf-skills

# override path
MCPCONFORMANCE_SKILLS_PATH=/path/to/conf-skills just testconf-skills
```

The target builds `examples/skills/skills-demo`, launches it on
`:18099` pointing at `examples/skills/skills/`, and runs the vitest
suite from the fork. Server is torn down on completion.

## Spec reference

- SEP-2640: <https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2640>
- Fork: <https://github.com/panyam/mcpconformance> PR 330
- mcpkit impl: `ext/skills/`
