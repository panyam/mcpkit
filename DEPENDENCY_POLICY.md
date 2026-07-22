# Dependency Policy

## Philosophy

The root module (`core/`, `server/`, `client/`) stays dependency-light. Heavier or opinionated dependencies (OAuth stacks, OTel SDK, ORMs, UI tooling) live in sub-modules with their own `go.mod`, so consumers only pull what they import.

## Update cadence

- Dependabot (`.github/dependabot.yml`) opens weekly update PRs for the root module, the `ext/*` sub-modules, and GitHub Actions.
- Sub-modules not covered by Dependabot are swept manually with `make tidy-all`, which runs whenever `core/` imports change and before every release.
- Cross-module pins are bumped in lock-step. For example, a `oneauth` bump touches all dependent modules in one PR so CI's module-resolution stays consistent.

## Security updates

- `make audit` runs govulncheck, gosec, and gitleaks; it runs in CI and before every tagged release.
- A vulnerability in a dependency with a fix available is treated at the severity of the vulnerability itself: critical (CVSS 7.0+) within 7 days, others in the next regular release. See `SECURITY.md`.

## Toolchain

The two most recent Go releases are supported. The `go` directive in each `go.mod` states the minimum version.
