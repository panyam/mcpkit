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
just setup-hooks   # recommended — see below
just test          # core/server/client/testutil unit tests
```

`just setup-hooks` installs two hooks. `pre-push` runs the test suite.
`pre-commit` rejects compiled executables and files over 5 MB.

The pre-commit hook is worth installing. A bare `go build` in a module
directory drops an executable named after that directory, with no extension,
which `git add -A` then sweeps into a commit. Several of these accumulated
before anyone noticed, and removing them needed a history rewrite that took
the repo from 466 MB to 23 MB. The hook detects them by magic bytes rather
than filename, since these files have no distinguishing name.

If a binary genuinely belongs in a commit, `git commit --no-verify` bypasses
it. CI runs the same check over the whole tree (`just check-no-binaries`), so
hooks are a fast local signal rather than the enforcement point.

Go 1.26+ and [`just`](https://github.com/casey/just) are required. The base
conformance suite additionally needs Node.js 22+. The task runner is moving
from make to just; during the transition the original Makefiles remain
alongside the justfiles with the same target names, so `make <target>` also
works. New targets must be added to both files.

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
