# Dependency Policy

## Philosophy

The root module (`core/`, `server/`, `client/`) stays dependency-light. Heavier or opinionated dependencies (OAuth stacks, OTel SDK, ORMs, UI tooling) live in sub-modules with their own `go.mod`, so consumers only pull what they import.

## Update cadence

**Go modules are updated by a repo-wide sweep, not per-directory.** `.github/workflows/dep-sweep.yml` runs monthly (and on demand), updates every module via `scripts/dep-sweep.sh`, reconciles the tree with `just tidy-all`, runs the suites, and opens one pull request. Run it locally with `just dep-sweep [patch|minor]`.

The sweep exists because per-directory updates cannot satisfy the lock-step rule below. Dependabot opens one PR per directory and cannot touch modules outside its globs, but the ~60 modules under `examples/` and `tests/` depend on the published ones through `replace` directives. A grouped Dependabot bump therefore left their `go.mod` stale and broke `test-agent` and `test-auth` with `updates to go.mod needed`. The worked example is PR 1225, where a 9-directory bump required 57 further modules to be tidied in lock-step.

- **Cross-module pins are bumped in lock-step.** A `oneauth` bump touches all dependent modules in one PR so module resolution stays consistent. This is enforced, not just documented: `just check-dep-consistency` fails CI when a third-party dependency is pinned at two or more versions across the published modules. Known-and-accepted divergence lives in `scripts/dep-baseline.json`; regenerate with `just update-dep-baseline`.
- **Dependabot still owns the single-directory trees** where per-directory PRs are the correct shape: the two shipped npm trees (`ext/ui/assets`, `agent/web/web`) and GitHub Actions.

## Security updates

Security fixes do not wait for the monthly sweep:

- **Dependabot security updates** are a repository setting, independent of `.github/dependabot.yml`, and still open CVE pull requests immediately.
- **`.github/workflows/vulncheck.yml`** scans the published surface weekly, plus on every release tag. It is time-triggered rather than tied to pushes because the advisory database moves independently of this repository: a CVE published after your last commit affects code that has not changed.
- `just audit` runs govulncheck, gosec, and gitleaks. It is a pre-release gate, not a per-PR CI job.
- `just vulncheck` scans the whole published module surface: the root module plus every module in `SUB_MODS_TO_TAG`. This matters because `govulncheck ./...` is module-scoped and does not descend into nested modules, so a root-only scan silently skips every sub-module. Examples are out of scope; they are demos, not modules anyone imports.
- A vulnerability in a dependency with a fix available is treated at the severity of the vulnerability itself: critical (CVSS 7.0+) within 7 days, others in the next regular release. See `SECURITY.md`.

## Toolchain

The two most recent Go releases are supported. The `go` directive in each `go.mod` states the minimum version.
