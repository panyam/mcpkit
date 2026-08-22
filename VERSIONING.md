# Versioning

mcpkit follows [semantic versioning](https://semver.org). The root module and the releasable protocol sub-modules are tagged in lock-step at the same version via `make tag-push V=vX.Y.Z` (see `RELEASING.md`), so a single version string identifies a consistent set across modules.

Everything below applies to the protocol modules. The agent SDK left this repository; see the note at the end.

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

The agent SDK is **no longer in this repository**. It was extracted to
[chakra](https://github.com/panyam/chakra), which consumes mcpkit as an ordinary dependency and
carries its own version line.

Nothing above applies to it, and nothing here depends on it. The dependency runs one way, and
`scripts/verify-submodule-deps.sh` enforces the two policies that remain, protocol and non-library.

The old `agent/vX.Y.Z` tags are not retracted. `github.com/panyam/mcpkit/agent` stays resolvable at
`v0.4.0`, `v0.5.0`, and `v0.5.1` forever, because module proxy content is immutable. Those tags
predate the split and the path they refer to no longer exists in this tree.
