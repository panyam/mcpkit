# disclosure: 200 tools, eight groups and two skills

An agent connects to a server with 200 tools. `tools/list` sends every schema, and the host puts
all of them into the model's context, about 82 KB of JSON here. The model may need one billing
tool.

This example serves that situation so you can measure it, and then serves the fix. The server
has 200 tools in eight themes (billing, deploy, search, users, storage, metrics, alerts, docs) and
two SEP-2640 skills. `billing-refunds` uses billing tools. `incident-triage` uses alerts, metrics
and deploy tools.

Each theme is also a group, served by the experimental groups extension in
`experimental/ext/groups`. A client that declares the extension sees eight one-line groups
instead of 200 tools, and fetches one group's tools with `groups/get` when it needs them. The
billing group lists the `billing-refunds` skill next to its tools. A client that doesn't declare
the extension still gets the flat list.

## Run it

```bash
just serve          # terminal 1: the fixture on :8080
just demo           # terminal 2: the walkthrough
```

Both sides take `--wire=legacy` or `--wire=stateless`.

The walkthrough connects a plain client, counts the bytes of the flat `tools/list`, lists the
skills, and reads `billing-refunds` with its digest checked. Then it connects a groups client,
lists the groups and fetches billing. See [WALKTHROUGH.md](WALKTHROUGH.md) for the steps.

## Check mode

`--check` connects two clients and prints one row per check:

```bash
go run . --serve --addr=127.0.0.1:18420 --wire=stateless &
go run . --check --url http://127.0.0.1:18420 --wire=stateless
```

```
  PASS stateless plain client: tools/list returns every tool: 200 of 200 tools, 82055 bytes
  PASS stateless plain client: SKILL.md reads and verifies: skill://billing-refunds/SKILL.md, 424 bytes, digest verified true
  PASS stateless groups client: groups/list returns one group per theme: 8 groups, want 8, 1030 bytes
  PASS stateless groups client: groups/get billing returns its tools and SKILL.md: 25 tools, want 25, SKILL.md listed true
  PASS stateless groups client: tools/list leaves out grouped tools: 0 of 200 tools, 156 bytes
  INFO stateless sizes: groups/get billing vs flat tools/list: groups/list 1030 + groups/get billing 10702 + tools/list 156 = 11888 bytes, flat tools/list 82055 bytes (14%)
```

The plain client declares no extensions and must get the flat list. The groups client declares
`io.modelcontextprotocol/groups`, an extension the Progressive Disclosure working group drafts.
It must get eight groups, the billing group's 25 tools and its skill, and an empty `tools/list`.
The command exits 1 if any row fails. The last row adds up what the groups client reads to reach
the billing tools, next to the flat list.

## Files

| File | What it holds |
|---|---|
| `fixture.go` | The 200 tool definitions, the eight groups and the embedded `skills/` tree |
| `check.go` | The `--check` rows |
| `walkthrough.go` | The demokit walkthrough |
| `skills/` | `billing-refunds` and `incident-triage` |
