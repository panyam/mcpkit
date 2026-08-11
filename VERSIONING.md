# Versioning

mcpkit follows [semantic versioning](https://semver.org). The root module and the releasable protocol sub-modules are tagged in lock-step at the same version via `make tag-push V=vX.Y.Z` (see `RELEASING.md`), so a single version string identifies a consistent set across modules.

The agent SDK is not on that train. Everything below applies to the protocol modules; the agent track has its own section at the end.

## Release types

- **Stable releases** (`vX.Y.Z`): production-ready, published as GitHub Releases with notes in `CHANGELOG.md` and `docs/releases/vX.Y.Z.md`.
- **Pre-releases** (`vX.Y.Z-bN`): beta tags cut for announcements or early adopters. Not production-ready and not covered by the compatibility expectations below.

## Breaking change policy

While the project is pre-1.0, breaking API changes may land in minor releases. They are handled deliberately:

- Breaking changes are batched into planned bundles (for example the 0.4.0 bundle) rather than scattered across releases. Each bundle's scope lives in `CHANGELOG.md` and a `docs/releases/` document with per-change migration recipes.
- Patch releases (`Z` bumps) never break API.
- Public surfaces slated for removal carry `// Deprecated:` annotations for at least 12 months before removal, with a pointer to the migration doc. Spec-driven deprecations (such as SEP-2577) additionally respect the spec's own deprecation window.
- No release removes an MCP protocol capability while the targeted spec version still requires it.

## Compatibility expectations

Within a minor line, upgrading a patch version is always safe. Across minor versions, read the `[X.Y.0]` entry in `CHANGELOG.md` first; if the release is a breaking bundle, the entry links the migration doc.

## Agent SDK

The agent SDK (`experimental/agent/` and its nine sub-modules) is **unreleased and carries no compatibility promise**. It is not tagged, not part of `make tag-push`, and nothing above applies to it.

This is deliberate. The protocol surface is at the conformance and policy bar for a stable release, while the agent track shipped in roughly two months and still takes breaking reshapes by design. Holding both to one version line would either freeze the agent surface before it has been pressure-tested or hold the protocol surface back. Separating the version lines costs nothing, because Go's compatibility contract is per module path and these were always separate modules.

Practical consequences:

- **There is no version to depend on.** `go get github.com/panyam/mcpkit/experimental/agent@main` resolves to a pseudo-version. That works, and it is the whole signal you get: a pseudo-version and an `experimental/` import path.
- **Most of the tree does not resolve at all.** Every agent module above the base pins its agent-to-agent dependencies at `v0.0.0` behind `replace` directives, and Go ignores replace outside the main module. This is enforced by `scripts/verify-submodule-deps.sh`, which will fail if someone "fixes" those pins.
- **The old `agent/vX.Y.Z` tags are not retracted.** `github.com/panyam/mcpkit/agent` stays resolvable at `v0.4.0`, `v0.5.0`, and `v0.5.1` forever, because module proxy content is immutable. Those tags predate this policy and the path they refer to no longer exists in the tree.

`make tag-agent` / `make tag-push-agent` exist over `AGENT_MODS_TO_TAG` and refuse to run without `AGENT_RELEASE_OK=1`. They are staged so that extracting this tree to its own repository is a copy rather than an invention, not because a release is planned from here.
