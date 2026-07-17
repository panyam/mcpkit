# Releasing mcpkit

mcpkit is a multi-module repo: the root module plus a set of sub-modules
(`ext/*`, `stores/redis`, `experimental/ext/events/*`, `cmd/*`, `tests/*`,
`examples/mcpskills-walkthrough`). A release tags the root **and** every
sub-module in `SUB_MODS_TO_TAG` (see the `Makefile`) at the same version, so
`go get github.com/panyam/mcpkit/<sub-module>@<version>` resolves consistently.

## Version forms

- **Stable:** `vMAJOR.MINOR.PATCH` — e.g. `v0.3.1`, `v0.4.0`.
- **Pre-release ("b") tag:** `vMAJOR.MINOR.PATCH-bN` — e.g. `v0.4.0-b1`,
  `v0.4.0-b2`. Used occasionally for announcement windows.
  - The **hyphen is mandatory**. `v0.4.0b1` (no hyphen) is **not** valid Go
    SemVer and will not resolve.
  - Pre-releases sort *below* the final version, so `go get …@latest` keeps
    returning the highest **stable** tag. Consumers opt into a pre-release
    explicitly: `go get …@v0.4.0-b1`.
  - A `-bN` pre-release is not a "stable release" for MCP SDK Tier-1 purposes —
    that's expected; a beta is a beta.

Pre-1.0 note: while there are no external clients, breaking changes may ride
intermediate `v0.3.x` tags until a minor is complete. `CHANGELOG.md` keeps the
in-progress minor's section (e.g. `[0.4.0]`) as the accumulating target;
intermediate checkpoints and `-bN` pre-releases don't get their own changelog
entry.

## Cutting a release (stable or pre-release)

From a green `main`:

1. **Gate.** `just test` (add `just audit` / `just testall` for a stable minor;
   a `-bN` pre-release can use `just test` alone).
2. **Bump sub-module requires.** `just bump-root <version>` — repoints every
   sub-module's `require github.com/panyam/mcpkit` at `<version>`. Required
   because sub-modules call APIs added in the root; without it,
   `go get …/ext/tasks@<version>` would resolve an older root that lacks them.
   Accepts pre-release strings (`v0.4.0-b1`) fine. Commit the resulting go.mod
   changes to `main` and push.
3. **Tag + push.** `just tag-push <version>` — creates and pushes the root
   tag plus one per sub-module (`ext/tasks/<version>`, …). The `Makefile` just
   string-interpolates `V`, so hyphenated pre-release strings are valid git
   tags.
4. **Verify resolution.** From a scratch module:
   ```
   GOPROXY=direct GOFLAGS=-mod=mod go list -m github.com/panyam/mcpkit@<version>
   GOPROXY=direct GOFLAGS=-mod=mod go list -m github.com/panyam/mcpkit/ext/tasks@<version>
   ```
   For a pre-release, also confirm `@latest` still returns the stable tag.
5. **GitHub Release.** See the token note below. For a stable release write a
   release note; for a `-bN` pre-release, publish with **"Set as a pre-release"**
   checked.

## GitHub Release: token limitation

Creating a GitHub **Release** via `gh release create` currently fails from the
maintainer's setup — the personal access token can create PRs/issues/comments
and push tags, but lacks **Contents: write** (403 on Releases), and the
alternate keyring account can't reach this repo. Until the PAT is granted
Contents: read-and-write, create the release in the browser with a prefilled
URL:

```
https://github.com/panyam/mcpkit/releases/new?tag=<tag>&title=<title>&body=<url-encoded markdown>[&prerelease=1]
```

Build it by URL-encoding the note (Python `urllib.parse.urlencode`). Tags
themselves push fine via `just tag-push`; only the Release object needs the
browser step.

## Full-minor checklist (e.g. tagging the final v0.4.0)

- [ ] `just audit` green (govulncheck + gosec + gitleaks + race)
- [ ] `just testall` green; `just testconf` green on a clean clone
- [ ] `CHANGELOG.md` `[X.Y.0]` finalized with real PR references + date;
      `docs/releases/vX.Y.0.md` written
- [ ] `just bump-root vX.Y.0` + commit
- [ ] `just tag-push vX.Y.0`; verify `go get …@vX.Y.0`
- [ ] GitHub Release published (browser, per the token note)
