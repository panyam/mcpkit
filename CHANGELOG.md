# Changelog

All notable changes to mcpkit are recorded here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Each release also has a fuller write-up under [`docs/releases/`](docs/releases/).
Releases before 0.3.0 were tag-only and are not back-filled here.

## [0.3.0] - 2026-06-29

Full notes: [`docs/releases/v0.3.0.md`](docs/releases/v0.3.0.md).

### Breaking
- Error codes renumbered for SEP-2907: `HeaderMismatch` -32001 → -32020,
  `MissingRequiredClientCapability` -32003 → -32021. Clients that switch on
  the numeric code must update. (PR 813)

### Added
- `examples/common` `--wire` flag for SEP-2575 wire selection, adopted across
  the non-UI examples; dual-mode audit + `make verify-dual` harness. (PR 826, PR 828, PR 836, issue 478)
- External stateless-draft conformance checker report — the client graded on
  the `2026-07-28` wire via `make testconf-external-checker`. (PR 830)
- Auth `step-up-keycloak` SUT exercising the `AcceptedScopes` OR-hierarchy and
  `includeGrantedScopes`, with `tests/keycloak` integration. (PR 819, PR 822)

### Changed
- Stateless wire (SEP-2575): middleware `*core.AuthError` now surfaces as
  HTTP 403 + `WWW-Authenticate` (was -32603 / 200); non-`AuthError` middleware
  errors map to 401 for legacy parity. (PR 816, issue 815)
- `OAuthTokenSource` defers scope acquisition until a challenge selects the
  scope; standalone `Token()` returns `ErrNoTokenYet` until armed. (PR 820, issue 818)
- Tasks v2 (SEP-2663) and MRTR (SEP-2322) conformance suites retargeted at
  `modelcontextprotocol/conformance` `main` (merged upstream).
- experimental events: `eventId` is now globally unique (random).
- `scripts/verify-submodule-deps.sh` discovers sub-modules dynamically.

### Fixed
- Stateless wire runs SEP-2356 file-input validation before the handler. (PR 834)
- Client emits the `Mcp-Name` routing header for task ops on the stateless wire. (PR 832)
- Client honors `WWW-Authenticate scope=` on 401 retry per RFC 6750 §3.1;
  scope-challenge 403s advertise the `resource_metadata` link. (PR 819)
- `step-up-keycloak` no longer forces stateless mode by default. (PR 821)
- `CAPABILITIES.md` protocol-negotiation version list corrected.

[0.3.0]: https://github.com/panyam/mcpkit/releases/tag/v0.3.0
