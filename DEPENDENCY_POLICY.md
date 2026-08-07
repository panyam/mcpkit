# Dependency Policy

## Philosophy

The root module (`core/`, `server/`, `client/`) stays dependency-light. Heavier or opinionated dependencies (OAuth stacks, OTel SDK, ORMs, UI tooling) live in sub-modules with their own `go.mod`, so consumers only pull what they import.

## Update cadence

**Go modules are updated by a repo-wide sweep, not per-directory.** `.github/workflows/dep-sweep.yml` runs monthly (and on demand), updates every module via `scripts/dep-sweep.sh`, reconciles the tree with `just tidy-all`, runs the suites, and opens one pull request. Run it locally with `just dep-sweep [patch|minor]`.

The sweep exists because per-directory updates cannot satisfy the lock-step rule below. Dependabot opens one PR per directory and cannot touch modules outside its globs, but the ~60 modules under `examples/` and `tests/` depend on the published ones through `replace` directives. A grouped Dependabot bump therefore left their `go.mod` stale and broke `test-agent` and `test-auth` with `updates to go.mod needed`. The worked example is PR 1225, where a 9-directory bump required 57 further modules to be tidied in lock-step.

- **Cross-module pins are bumped in lock-step.** A `oneauth` bump touches all dependent modules in one PR so module resolution stays consistent. This is enforced, not just documented: `just check-dep-consistency` fails CI when a third-party dependency is pinned at two or more versions across the published modules. Known-and-accepted divergence lives in `scripts/dep-baseline.json`; regenerate with `just update-dep-baseline`.
- **Dependabot still owns the single-directory trees** where per-directory PRs are the correct shape: the two shipped npm trees (`ext/ui/assets`, `agent/surfaces/web/web`) and GitHub Actions. `make check-dependabot-dirs` fails CI if one of those paths stops existing, because Dependabot ignores a directory it cannot find without warning.

## Security updates

Security fixes do not wait for the monthly sweep:

- **Dependabot security updates** are a repository setting, independent of `.github/dependabot.yml`, and still open CVE pull requests immediately.
- **`.github/workflows/vulncheck.yml`** scans the published surface weekly, plus on every release tag. It is time-triggered rather than tied to pushes because the advisory database moves independently of this repository: a CVE published after your last commit affects code that has not changed.
- `just audit` runs govulncheck, gosec, and gitleaks. It is a pre-release gate, not a per-PR CI job.
- `just vulncheck` scans the whole published module surface: the root module plus every module in `SUB_MODS_TO_TAG`. This matters because `govulncheck ./...` is module-scoped and does not descend into nested modules, so a root-only scan silently skips every sub-module. Examples are out of scope; they are demos, not modules anyone imports.
- A vulnerability in a dependency with a fix available is treated at the severity of the vulnerability itself: critical (CVSS 7.0+) within 7 days, others in the next regular release. See `SECURITY.md`.

### Reachability scanning does not replace version matching

`govulncheck` in its default mode is reachability-based. It answers "does this code
call a vulnerable symbol" and stays silent when the answer is no. That is the correct
low-noise default for a release gate, and it is why `vulncheck.yml` can gate without
drowning every release in advisories about code paths mcpkit never executes.

It also means a stale dependency can sit in the tree indefinitely with no local signal.
Verified during the v0.5.1 release with `ext/auth` pinned back to `golang.org/x/crypto
v0.51.0`:

```
No vulnerabilities found.
This scan also found 0 vulnerabilities in packages you import and 14
vulnerabilities in modules you require, but your code doesn't appear to call
these vulnerabilities.
```

Exit code 0. `vulncheck.yml` was working correctly and stayed green while three
advisories with fixes available sat in 16 modules. An external version-matching scan is
what surfaced them.

Treat the two as complementary. Reachability answers "are we exploitable"; version
matching answers "are we current". A dependency policy needs both answers.

**Prefer `osv-scanner` over `govulncheck -scan module` for the version-matching pass.**
Two reasons, both learned the hard way:

- `govulncheck -scan module` rejects package patterns (`./...` is an error) and requires
  Go files in the invocation directory. mcpkit's root holds no `.go` files, so it cannot
  scan the root module without a per-module `-C` dance across 88 `go.mod` files.
- `osv-scanner` reads `go.mod`, npm lockfiles, and `uv.lock` in one pass, so a single
  invocation covers every ecosystem in the repo.

```
osv-scanner scan source -r --include-git-root .
```

Note `--include-git-root`: without it the scanner skips the repository root and reports
"No package sources found". `--no-call-analysis` takes an argument, so it will silently
swallow a following `.` if passed bare.

### Known scanning gaps

- **`GO-2026-5932` has no published fix.** It is the notice that
  `golang.org/x/crypto/openpgp` is unmaintained; mcpkit does not import that package. Any
  version-matching scan reports it forever, so a gating osv-scanner job needs an
  `osv-scanner.toml` suppression before it can be turned on, or it goes permanently red.
- **Gitignored lockfiles are invisible to every scanner.** `conformance/`,
  `tools/compat-reports`, and `tools/conformance-report` gitignore their
  `package-lock.json` by design. Nothing (Dependabot, `npm audit`, osv-scanner on a clean
  checkout) can see those trees. A stale `vitest` sat at 2.1.9 there for exactly this
  reason.
- **`examples/` npm trees are excluded from `dependabot.yml`** on the
  demos-are-not-artifacts rule. That rule is defensible for shipped blast radius, but it
  means a third-party scan pointed at the repository will keep finding things CI does
  not. A stale `hono` sat in `examples/tasks/package-lock.json` for this reason.
- **`uv.lock` is scanned by nothing today.** The Python tree is outside both
  `vulncheck.yml` and `dependabot.yml`.

### Verify the fix version against the advisory range

A reported "upgrade to X" is not automatically sufficient. During v0.5.1 the reported
`hono` target was 4.12.27, but the advisory range was `hono <=4.12.33` across 18
advisories, so 4.12.27 would have cleared nothing. Check the range, not just the
suggested version:

```
npm audit --package-lock-only --json   # `range` and `fixAvailable` per advisory
osv-scanner ...                        # `affected[].ranges[].events[].fixed`
```

## Repository security settings

Dependabot alerts, Dependabot security updates, and private vulnerability reporting are
**repository settings, not files**. `dependabot.yml` governs version updates only.
Enabling alerts requires no commit, and no `dependabot.yml` entry is needed for a tree to
produce alerts.

Private vulnerability reporting is enabled on this repository. It is what unlocks the
draft-advisory to published-GHSA flow: GitHub is a CNA, so a published advisory receives
a CVE and propagates to OSV, and from there into `npm audit`, Dependabot, Snyk, Trivy,
and `govulncheck`. Without it there is no route for a vulnerability in mcpkit to reach a
downstream consumer's scanner. It is a public-repository feature and cannot be enabled on
a private repo.

Account-wide defaults live at `https://github.com/settings/security_analysis`; per-repo
state at `https://github.com/panyam/mcpkit/settings/security_analysis`.

## Toolchain

The two most recent Go releases are supported. The `go` directive in each `go.mod` states the minimum version.
