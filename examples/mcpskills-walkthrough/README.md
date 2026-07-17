# mcpskills walkthrough

A demokit-narrated end-to-end exercise of the [`mcpskills`](../../cmd/mcpskills) CLI: verify, serve, inspect, pack, unpack, byte-equality diff. Doubles as a CI smoke test for the published CLI surface when run with `--non-interactive`.

## Run

```bash
make demo            # text mode (default)
make tui             # interactive TUI
make note            # notebook mode
make smoke           # non-interactive CI smoke test
```

Or directly:

```bash
go run .                            # text
go run . --tui                      # TUI
go run . --non-interactive          # CI smoke
go run . --doc=md > WALKTHROUGH.md  # regenerate the rendered doc
```

## What it walks

| Step | What happens |
|---|---|
| 1 | Locate or build the `mcpskills` binary (env var, repo-local, or `go build`) |
| 2 | Write the embedded `fixture/` (two skills) to a fresh temp dir |
| 3 | `mcpskills verify` lints the fixture |
| 4 | `mcpskills serve` runs in the background on a free port |
| 5 | `mcpskills inspect --json` connects, walks the index, verifies every digest |
| 6 | (optional) Repeat inspect against `MCPSKILLS_INSPECT_UPSTREAM_URL` |
| 7 | `mcpskills pack` writes a `.tar.gz` for one skill |
| 8 | `mcpskills unpack` extracts it to a sibling tree |
| 9 | Walk both trees and assert byte equality |

Cleanup is deferred until after the demokit run returns, so interactive viewers can inspect the temp tree on the final pause.

## Binary discovery

The walkthrough resolves the `mcpskills` binary in this order:

1. `$MCPSKILLS_BIN` (explicit override)
2. `<repo-root>/bin/mcpskills` (produced by `make build-mcpskills`)
3. Fresh `go build` of `./cmd/mcpskills` into a temp path

The temp binary, if built, is cleaned up at the end alongside the temp fixture dir.

## Optional upstream inspect

Set `MCPSKILLS_INSPECT_UPSTREAM_URL` to any spec-compliant MCP server URL to run the inspect step a second time against it. Useful for proving the CLI works against non-mcpkit implementations of SEP-2640.

```bash
MCPSKILLS_INSPECT_UPSTREAM_URL=http://localhost:9000/mcp make smoke
```

When the env var is unset, the step prints a one-line skip note and the walkthrough continues.

## CI integration

`just test-mcpskills-walkthrough` at the repo root runs this same walkthrough with `--non-interactive` and asserts exit 0. Use it as the per-commit gate for the CLI's behavioral surface (verify lints, serve listens, inspect verifies, pack round-trips through unpack).
