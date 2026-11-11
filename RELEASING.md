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

1. **Gate.** The commit you're releasing has a green `Test` run on main:
   `gh run list --workflow test.yml --branch main --commit "$(git rev-parse HEAD)"`.
   CI already ran the root suite `make test` runs, and the sub-module suites,
   on it, so re-running it locally repeats that work. Run `make test` yourself
   only when that run is missing or red.
   That run's `tagged-modules` job builds and vets every module in
   `SUB_MODS_TO_TAG` against main (#1486).
   A stable minor still adds `make audit` and `make testall`, which CI doesn't
   cover.
2. **Bump sub-module requires.** `make bump-root V=<version>` — repoints every
   sub-module's `require github.com/panyam/mcpkit` at `<version>`, and every
   tagged sub-module's requires on other tagged sub-modules too
   (`scripts/bump-pins.sh`). Required because sub-modules call APIs added in
   the root and in each other. Without it, `go get …/ext/tasks@<version>` would
   resolve an older root that lacks them, and v0.7.0's
   `experimental/ext/events/stores/gorm` would have resolved an `events` without
   the `VerifiedAt` field it uses. `v0.0.0` placeholder pins in `tests/*` are
   left alone, as the non-library policy allows.
   Accepts pre-release strings (`v0.4.0-b1`) fine. It also sets the version of
   `@panyam/mcpkit-rooms` (`experimental/ext/rooms/ts/package.json`), which is
   published at the release's version. Commit the resulting go.mod and
   package.json changes to `main` and push.

   **If its tidy fails, fix the module and run `make tidy-all` and
   `make verify-submodule-deps` before tagging.** `bump-root` stops at the
   failure, so the steps after it never ran. The failure has a second cost: a
   module missing a `replace` makes tidy ask proxy.golang.org for the new
   version before its tag exists. The proxy and sum.golang.org cache that
   miss as "unknown revision", and serve it after the tag is pushed. Then
   `make check-install` fails for every command that needs the module, for up
   to about an hour. v0.8.18 hit this: Lanternfall lacked the replace for
   `experimental/ext/events/clients/go`, and `rooms-channel` and
   `events-bridge` would not install from the proxy. The tag was fine, and
   `GOPROXY=direct GONOSUMDB=github.com/panyam/mcpkit go install …` worked.
   Wait it out and rerun `check-install`.
3. **Tag + push.** `make tag-push V=<version>` — creates and pushes the root
   tag plus one per sub-module (`ext/tasks/<version>`, …). The `Makefile` just
   string-interpolates `V`, so hyphenated pre-release strings are valid git
   tags. It also leaves two commits on `main` and pushes them first: the tags
   point at `release: installable tree for <version>`, which drops every
   in-repo `replace` from the tagged modules, and
   `release: restore in-repo replaces after <version>` puts them back. Go
   refuses `go install pkg@<version>` for a module that carries a `replace`,
   which is why no command here had installed at any tag before #1488
   (`scripts/release-replaces.sh`). `make rehearse-release` runs the whole
   thing at a fake version in a clone, and CI runs it on every PR.
4. **Verify resolution.** From a scratch module:
   ```
   GOPROXY=direct GOFLAGS=-mod=mod go list -m github.com/panyam/mcpkit@<version>
   GOPROXY=direct GOFLAGS=-mod=mod go list -m github.com/panyam/mcpkit/ext/tasks@<version>
   ```
   For a pre-release, also confirm `@latest` still returns the stable tag.
   The tag also publishes `@panyam/mcpkit-rooms` to npm through
   `publish-npm-rooms.yml`, which `check-release-workflows` now counts. See
   § npm below for the one-time setup.
5. **Publish.** `make release-publish V=<version>` runs three steps and stops
   at the first failure. The GitHub Release exists only if the first two pass.
   - `check-release-workflows --wait` polls until every tag-triggered workflow
     has run green against the tag, for up to 20 minutes
     (`RELEASE_WAIT_SECONDS`). A tag that triggers nothing looks the same as a
     tag whose workflows all passed. v0.4.0 through v0.6.0 shipped that way,
     with `publish-images.yml` never run (#1412).
   - `check-install` runs `go install` for every command in the root and the
     tagged modules, from an empty module. A "not found" in the first few
     minutes is usually the proxy catching up, so rerun before reading it as
     broken.
   - `gh release create`, with gh's own login (see the token note below). The
     notes come from `docs/releases/<version>.md`, and a stable tag without
     that file stops before any check. A `-bN` tag is marked as a pre-release
     and may go without a note.
   Each check also runs on its own, as `make check-release-workflows V=` and
   `make check-install V=`. Until #1414 they were checklist lines here.

## npm: `@panyam/mcpkit-rooms`

The room bridge's page client (`experimental/ext/rooms/ts`) is published on every release tag by
`.github/workflows/publish-npm-rooms.yml`, at the release's version, through servicekit's shared
workflow with npm trusted publishing (no token). `scripts/rooms-ts-version.sh check` fails CI when
the package's version drifts from the rooms module's mcpkit pin, and fails the publish when the tag
disagrees. A version already on npm is skipped, so re-running the workflow is safe.

**Once, before the first release that publishes it.** npm can only attach a trusted publisher to a
package that already exists, so the first version goes up by hand. After `make bump-root V=vX.Y.Z`
is committed and before `make tag-push`:

```
cd experimental/ext/rooms/ts
pnpm install --frozen-lockfile
pnpm build && pnpm check:pack
npm publish --access public          # publishes X.Y.Z, the version bump-root set
npm trust github @panyam/mcpkit-rooms --repo panyam/mcpkit --file publish-npm-rooms.yml --allow-publish
```

Then push the tags as usual. The workflow finds X.Y.Z already published and skips it, and every
later tag publishes on its own.

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

Pushing commits and tags is unaffected. That goes over SSH, through the agent
socket. `~/.ssh/id_github` does not exist in the container, so the old
`ssh -i ~/.ssh/id_github` form fails with "Identity file not accessible" and
then "Permission denied (publickey)" (#1415). `~/.ssh/agent.sock` holds the
right key:

```
SSH_AUTH_SOCK=~/.ssh/agent.sock git push origin main
```

## Full-minor checklist (e.g. tagging the final v0.4.0)

- [ ] `make audit` green (govulncheck + gosec + gitleaks + race)
- [ ] `make testall` green; `make testconf` green on a clean clone. Without
      Docker (no Keycloak), `testall` cannot run, so run its stage-8 conformance
      suites directly: `testconf`, `testconfauth`, `testconf-tasks`,
      `testconf-tasks-v2`, `testconf-mrtr`, `testconf-file-inputs`,
      `testconf-auth-server`, `testconf-skills`, and `testconf-events`
      (informational), plus `testconf-client` and `testconf-stateless`.
      Stages 8f/8g need a `pending` checkout: run `make conf-pending-setup` once.
- [ ] `CHANGELOG.md` `[X.Y.0]` finalized with real PR references + date;
      `docs/releases/vX.Y.0.md` written
- [ ] `make bump-root V=vX.Y.0` + commit
- [ ] `make tag-push V=vX.Y.0`; verify `go get …@vX.Y.0`
- [ ] `make verify-submodule-deps-resolve` green, run **after** `tag-push`, since
      sibling pins now name the tag being created. This is the network check:
      the default `verify-submodule-deps` only asks whether a pinned version
      *looks* like a real tag, so a sibling pinned at a tag nobody pushed passes
      it and breaks `go get` for every outside consumer. That is what #1291 was.
- [ ] `make release-publish V=vX.Y.0` green. It waits for the tag's workflows
      (#1412), checks every command installs (#1488), and only then creates the
      GitHub Release (#1414)
