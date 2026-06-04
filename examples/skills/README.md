# examples/skills

Minimal mcpkit server that exposes skills per SEP-2640 (Skills extension).

This example doubles as the fixture target for `make testconf-skills` in
the conformance suite. A scripted walkthrough is deferred; see mcpkit
566 for the full demokit treatment.

## What the server does

Walks the bundled `skills/` directory through
`github.com/panyam/mcpkit/ext/skills.SkillProvider` and publishes each
skill as MCP resources. Two distribution modes are supported via the
`--mode` flag:

- `file` (default): each file in every skill becomes a resource at
  `skill://<skill-path>/<file-path>`. SKILL.md gets `Name` and
  `Description` from its frontmatter; Extra frontmatter fields land in
  `Annotations` keyed by `io.modelcontextprotocol.skills/`.
- `archive` (`.tar.gz`) or `zip`: each skill becomes one archive
  resource at `skill://<skill-path>.tar.gz` (or `.zip`). The blob is
  packed on demand and digested for the index entry.

In both modes `skill://index.json` is auto-registered and the
`io.modelcontextprotocol/skills` capability appears under
`capabilities.extensions` in the initialize response.

## Fixture skills

Three skills mirror the SEP-2640 examples table:

| Skill path | Files |
|---|---|
| `git-workflow` | `SKILL.md` |
| `pdf-processing` | `SKILL.md`, `references/FORMS.md`, `scripts/extract.py` |
| `acme/billing/refunds` | `SKILL.md`, `templates/email.md` |

`pdf-processing/SKILL.md` carries Extra frontmatter (`version`, `tags`)
to exercise the `Annotations` surface.

## Running

```bash
# file mode on :8080
make serve

# archive mode on :8080 (.tar.gz per skill)
make serve-archive

# zip mode on :8080
make serve-zip

# custom port + skills dir
go run . --serve --addr=:9090 --skills=./some-other-skills
```

## Smoke test

```bash
# in another shell
curl -s -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize",
       "params":{"protocolVersion":"2025-11-25","capabilities":{},
                 "clientInfo":{"name":"test","version":"0"}}}' \
  | grep skills
```

Expect the response to include
`"io.modelcontextprotocol/skills":{...}` under
`result.capabilities.extensions`.

## Conformance

```bash
# from repo root
MCPCONFORMANCE_SKILLS_PATH=$HOME/newstack/mcpkit/conf-main make testconf-skills
```

The target launches this server in the configured mode, runs the
SEP-2640 scenario suite from the panyam/mcpconformance fork, and
reports per-scenario pass/fail wired to check IDs in
`src/seps/sep-2640.yaml`.

## Spec reference

- SEP-2640: <https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2640>
- Agent Skills specification: <https://agentskills.io/specification>
- Working group: skills-over-mcp-wg
