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
  - A `-bN` pre-release is not a "stable release" for MCP SDK Tier-1 purposes.
    That's expected; a beta is a beta.

Pre-1.0 note: while there are no external clients, breaking changes may ride
intermediate `v0.3.x` tags until a minor is complete. `CHANGELOG.md` keeps the
in-progress minor's section (e.g. `[0.4.0]`) as the accumulating target;
intermediate checkpoints and `-bN` pre-releases don't get their own changelog
entry.

## Cutting a release (stable or pre-release)

From a green `main`:

1. **Gate.** `make test` (add `make audit` / `make testall` for a stable minor;
   a `-bN` pre-release can use `make test` alone).
2. **Bump sub-module requires.** `make bump-root V=<version>` — repoints every
   sub-module's `require github.com/panyam/mcpkit` at `<version>`. Required
   because sub-modules call APIs added in the root; without it,
   `go get …/ext/tasks@<version>` would resolve an older root that lacks them.
   Accepts pre-release strings (`v0.4.0-b1`) fine. Commit the resulting go.mod
   changes to `main` and push.
3. **Tag + push.** `make tag-push V=<version>` — creates and pushes the root
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

## GitHub Release: use gh's own login, not the PAT

Publish with `gh`'s stored `gho_` OAuth login, which carries full `repo` scope:

```
env -u GH_TOKEN gh release create <tag> --repo panyam/mcpkit \
  --title "<tag>" --notes-file docs/releases/<tag>.md [--prerelease]
```

`env -u GH_TOKEN` is the whole trick. With `GH_TOKEN="$GH_PERSONAL_TOKEN"` set,
`gh` uses the fine-grained PAT, which lacks **Contents: write** and 403s on
Releases; unset it for the command and `gh` falls back to the login in
`~/.config/gh/hosts.yml`, which does not. Verified publishing v0.6.0 on
2026-09-17. `gh release edit` works the same way.

This supersedes a long-standing note here that a Release had to be created in
the browser with a URL-encoded prefill. That was true of the PAT and was never
true of the stored login, so the browser step was never actually necessary. If
you hit a 403, check which credential `gh` picked up before reaching for the
browser.

The same `env -u GH_TOKEN` form reads Dependabot alerts, which the PAT also
403s on. The fine-grained PAT is still the right credential for `panyam/*` PR
and issue work; it is release and security endpoints it cannot reach.

### The wider PAT gap

The missing Contents permission is one facet of a fine-grained PAT that is scoped
narrower than release work needs. During the v0.5.1 release the same token blocked
three separate things:

| Operation | Endpoint | Missing permission |
|---|---|---|
| `gh release create` / `edit` | `POST`/`PATCH /repos/…/releases` | Contents: read and write |
| Enable or read Dependabot alerts and automated security fixes | `…/vulnerability-alerts`, `…/automated-security-fixes` | Administration: read and write |
| Read open Dependabot alerts | `…/dependabot/alerts` | Dependabot alerts: read |
| Read branch protection | `…/branches/main/protection` | Administration: read |

Every row above is reachable with `env -u GH_TOKEN gh …`, which is the practical
workaround for all of them rather than widening the PAT.

`GET /repos/…/private-vulnerability-reporting` works on Metadata alone, which is why
that one field is readable while the rest return 403.

A 403 here is not a 404. A status script that treats any non-success as "disabled"
will report a fully-configured repository as unprotected. Distinguish the two.

Pushing commits and tags is unaffected: that goes over SSH, and per `CLAUDE.md` needs
the personal key pinned, since the agent offers the EMU key first.

```
GIT_SSH_COMMAND="ssh -i ~/.ssh/id_github -o IdentitiesOnly=yes" git push origin main
```

## Full-minor checklist (e.g. tagging the final v0.4.0)

- [ ] `make audit` green (govulncheck + gosec + gitleaks + race)
- [ ] `make testall` green; `make testconf` green on a clean clone
- [ ] `CHANGELOG.md` `[X.Y.0]` finalized with real PR references + date;
      `docs/releases/vX.Y.0.md` written
- [ ] `make bump-root V=vX.Y.0` + commit
- [ ] `make verify-submodule-deps-resolve` green. This is the network check: the
      default `verify-submodule-deps` only asks whether a pinned version *looks*
      like a real tag, so a sibling pinned at a tag nobody pushed passes it and
      breaks `go get` for every outside consumer. That is what #1291 was.
- [ ] `make tag-push V=vX.Y.0`; verify `go get …@vX.Y.0`
- [ ] GitHub Release published (`env -u GH_TOKEN gh release create`, per the token note)
