# mcpskills

Zero-Go-code CLI for [SEP-2640](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/specifications/server-features/skills.md) skills. Host a directory, inspect any compliant server, lint a skill bundle, or produce / consume archive bundles.

The binary is deliberately implementation-agnostic on the inspect side. It speaks the wire and verifies the cataloged digests, so the same command works against mcpkit, the reference TypeScript implementation, the PHP implementation, or any future SDK that follows the spec.

## Install / build

```bash
# From the mcpkit repo root:
make build-mcpskills          # produces ./bin/mcpskills

# Or directly with go install:
go install github.com/panyam/mcpkit/cmd/mcpskills@latest
```

## Subcommands

```
mcpskills serve   <dir>      Host a skills directory via MCP
mcpskills inspect <url>      Connect to any SEP-2640 server and verify digests
mcpskills verify  <dir>      Lint a skills directory for SEP-2640 compliance
mcpskills pack    <dir>      Pack a single skill into .tar.gz or .zip
mcpskills unpack  <archive>  Extract a skill archive with SEP safety guards
```

Run `mcpskills <cmd> --help` for the full surface of each subcommand.

## Demo flow

```bash
# 1. Lint an existing skills directory.
mcpskills verify ./my-skills

# 2. Host it.
mcpskills serve ./my-skills --addr :8080 &

# 3. From a separate terminal, inspect the running server. The
#    capability declaration, the index, and every cataloged digest
#    are checked end-to-end.
mcpskills inspect http://localhost:8080/mcp

# 4. Pack a single skill for distribution.
mcpskills pack ./my-skills/git-workflow --format zip

# 5. Extract an archive someone else shipped you, safely.
mcpskills unpack ./git-workflow.zip -o ./extracted
```

## Cross-implementation inspect

The inspect subcommand can verify any compliant server, not just one started by `mcpskills serve`. Point it at any URL that advertises `io.modelcontextprotocol/skills` and it will fetch `skill://index.json`, walk every entry, and SHA-256-verify each `skill-md` or `archive` payload against the digest the index promises:

```bash
mcpskills inspect http://upstream.example.com/mcp --json
```

A non-zero exit means at least one digest did not match.

## Output modes

- `--color auto|always|never` — applies to text-mode output. `auto` enables color only when stdout is a TTY.
- `--json` (inspect only) — emits a machine-readable JSON report instead of the boxed human-readable view.

## URL precedence

`mcpskills inspect` resolves the target URL in this order (highest wins):

1. positional `<url>` argument
2. `--url` flag
3. `$MCPSKILLS_INSPECT_URL` environment variable
4. `http://localhost:8080/mcp`

## Archive safety

`mcpskills unpack` enforces every SEP-2640 archive MUST: reject `../` traversal, reject absolute paths, reject escaping symlinks / hardlinks, and cap total unpacked size (default 100 MiB, override via `--max-bytes`). The same guards back the `skills.UnpackBytes` library function used by `ext/skills`.
