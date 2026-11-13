# disclosure: 200 tools and two skills

An agent connects to a server with 200 tools. `tools/list` sends every schema, and the host puts
all of them into the model's context, about 82 KB of JSON here. The model may need one billing
tool.

This example serves that situation so you can measure it. The server has 200 tools in eight
themes (billing, deploy, search, users, storage, metrics, alerts, docs) and two SEP-2640 skills.
`billing-refunds` uses billing tools. `incident-triage` uses alerts, metrics and deploy tools.

## Run it

```bash
just serve          # terminal 1: the fixture on :8080
just demo           # terminal 2: the walkthrough
```

Both sides take `--wire=legacy` or `--wire=stateless`.

The walkthrough connects, counts the bytes of the flat `tools/list`, lists the skills, and reads
`billing-refunds` with its digest checked. See [WALKTHROUGH.md](WALKTHROUGH.md) for the steps.

## Check mode

`--check` connects two clients and prints one row per check:

```bash
go run . --serve --addr=127.0.0.1:18420 --wire=stateless &
go run . --check --url http://127.0.0.1:18420 --wire=stateless
```

```
  PASS stateless plain client: tools/list returns every tool: 200 of 200 tools, 82054 bytes
  PASS stateless plain client: SKILL.md reads and verifies: skill://billing-refunds/SKILL.md, 424 bytes, digest verified true
  FAIL stateless groups client: groups/list returns one group per theme: RPC error -32601: method not found: groups/list
  FAIL stateless groups client: groups/get billing returns its tools and SKILL.md: RPC error -32601: method not found: groups/get
  FAIL stateless groups client: tools/list leaves out grouped tools: 200 of 200 tools, 82054 bytes
  INFO stateless sizes: groups/get billing vs flat tools/list: flat tools/list 82054 bytes, groups/get n/a
```

The plain client declares no extensions and must get the flat list. The groups client declares
`io.modelcontextprotocol/groups`, an extension the Progressive Disclosure working group drafts.
mcpkit does not serve it yet, so the groups rows fail and the command exits 1. The last row
prints the size of one group next to the flat list.

## Files

| File | What it holds |
|---|---|
| `fixture.go` | The 200 tool definitions and the embedded `skills/` tree |
| `check.go` | The `--check` rows |
| `walkthrough.go` | The demokit walkthrough |
| `skills/` | `billing-refunds` and `incident-triage` |
