# Dependency Policy

## Philosophy

The root module (`core/`, `server/`, `client/`) stays dependency-light. Heavier or opinionated dependencies (OAuth stacks, OTel SDK, ORMs, UI tooling) live in sub-modules with their own `go.mod`, so consumers only pull what they import.

## Update cadence

- Dependabot (`.github/dependabot.yml`) opens weekly update PRs for the root module, the `ext/*` sub-modules, and GitHub Actions.
- Sub-modules not covered by Dependabot are swept manually with `just tidy-all`, which runs whenever `core/` imports change and before every release.
- Cross-module pins are bumped in lock-step. For example, a `oneauth` bump touches all dependent modules in one PR so CI's module-resolution stays consistent.

## Security updates

- `just audit` runs govulncheck, gosec, and gitleaks. It is a pre-release gate, run before every tagged release, not a per-PR CI job.
- `just vulncheck` scans the whole published module surface: the root module plus every module in `SUB_MODS_TO_TAG`. This matters because `govulncheck ./...` is module-scoped and does not descend into nested modules, so a root-only scan silently skips every sub-module. Examples are out of scope; they are demos, not modules anyone imports.
- A vulnerability in a dependency with a fix available is treated at the severity of the vulnerability itself: critical (CVSS 7.0+) within 7 days, others in the next regular release. See `SECURITY.md`.

## Toolchain

The two most recent Go releases are supported. The `go` directive in each `go.mod` states the minimum version.
