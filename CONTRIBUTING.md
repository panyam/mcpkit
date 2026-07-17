# Contributing to mcpkit

Thanks for your interest in mcpkit! Contributions — bug reports, fixes,
examples, docs, and new SEP implementations — are all welcome.

## Ground rules

- Be kind and constructive. mcpkit exists to help the MCP ecosystem; we
  aim to complement the official SDKs, not compete with them.
- Keep changes focused. One logical change per pull request.
- Add or update tests for any behavior change. The conformance suites are
  the contract — a green suite is the bar for merge.

## Getting started

```bash
git clone https://github.com/panyam/mcpkit
cd mcpkit
just test          # core/server/client/testutil unit tests
```

Go 1.26+ is required. The base conformance suite additionally needs Node.js 22+.

## Repository layout

mcpkit is a multi-module repo. The root module holds `core/`, `server/`,
`client/`, and `testutil/`. Extensions live in their own modules under
`ext/` and `experimental/ext/` (`ext/auth`, `ext/tasks`, `ext/ui`,
`ext/otel`, `ext/skills`, `experimental/ext/events`, `experimental/ext/protogen`).

Because each extension is a separate `go.mod`, `just test` does **not** cover
them. After changing anything in `core/`, run `just tidy-all` so the
sub-modules pick up new imports, and run the relevant sub-module suite
(`just test-auth`, `just test-ui`, etc.). See [CLAUDE.md](CLAUDE.md) for the
full command reference and the package-level gotchas.

## Tests and conformance

```bash
just test              # unit tests (root module)
just test-auth         # ext/auth
just test-ui           # ext/ui
just testconf          # base MCP server conformance (published upstream suite; Node 22+)
just testall           # everything + Keycloak + HTML report
just audit             # govulncheck + gosec + gitleaks + race
```

The base `just testconf` runs against the published
`@modelcontextprotocol/conformance` CLI and needs no extra checkouts. The
per-SEP suites (`testconf-tasks-v2`, `testconf-mrtr`, `testconf-stateless`,
…) drive fixtures under `examples/` against upstream / fork conformance
worktrees; see the `MCPCONFORMANCE_*_PATH` notes in
[`conformance/Makefile`](conformance/Makefile) and [CLAUDE.md](CLAUDE.md).

## Submitting a change

1. Branch from `main`.
2. Make your change with tests.
3. Run `just test` (plus the relevant sub-module suite) and `just testconf`.
4. Open a pull request describing the change and linking any related issue
   or SEP.

## Reporting bugs and requesting features

Open an issue at https://github.com/panyam/mcpkit/issues. For security
reports, please avoid filing a public issue — contact the maintainer
directly.

## License

By contributing, you agree that your contributions will be licensed under
the [Apache License 2.0](LICENSE).
