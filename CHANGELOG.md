# Changelog

All notable changes to mcpkit are recorded here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Each release also has a fuller write-up under [`docs/releases/`](docs/releases/).
Releases before 0.3.0 were tag-only and are not back-filled here.

## [0.4.0] - Unreleased

Full notes: [`docs/releases/v0.4.0.md`](docs/releases/v0.4.0.md).

> **Not tagged as `v0.4.0` yet.** This entry is the accumulating target for the
> breaking bundle. Work-in-progress lands under intermediate **`v0.3.x`** tags
> while the issues below are worked through; the `v0.4.0` tag is cut only once
> the full bundle is complete. Until then, treat this section as the running
> plan, not a shipped release.

API-breaking bundle. 0.4.0 gathers the backward-incompatible API changes we
had queued behind a version boundary while mcpkit still has no external clients
to migrate. It does **not** remove any protocol capability. The SEP-2577
Roots / Sampling / Logging surfaces stay in place with their `// Deprecated:`
annotations; removal is deferred to a later release (tracked separately),
no earlier than the spec actually drops them (~2027). Keeping those surfaces
also preserves conformance against the deprecated-but-in-spec features on the
targeted spec version.

### Breaking
- **`core.Request.Params` is now `core.RawJSON`** (was `json.RawMessage`) —
  issue 733 slice 3, the final slice of the params-handling change. Read it with
  `req.Params.Bind(&typed)` / `.Meta()` / `.Field(key)` and the raw bytes with
  `req.Params.Raw()`; construct with `core.NewRawJSON(bytes)` or
  `core.MarshalRawJSON(v)`. A notification is still a `Request` with no ID, so
  its params flip too. Wire output is byte-identical — `Request.MarshalJSON`
  preserves param omission (JSON-RPC forbids `"params":null`). The transitional
  `ParamsLazy()` bridge is removed (`req.Params` *is* the cached parse now).
  Breaking for anyone constructing or reading `core.Request.Params` directly.
- **conformant-by-default** — safe-default SEP options flip from opt-in to
  opt-out. `server.NewServer(info)` now emits the SEP-2549 cache-control hints
  by default: list responses (tools/prompts/resources/templates) carry
  `ttlMs: 0` + `cacheScope: "public"`; `resources/read` carries `ttlMs: 0` +
  `cacheScope: "private"` (conservative — read content often varies per user).
  `ttlMs: 0` is "immediately stale", the same effective behavior as omitting the
  field but present so the SEP-2549 MUST check passes. Handlers still override
  per-read. New `server.WithoutListCacheControl()` /
  `server.WithoutReadResourceCacheControl()` opt-outs restore omission.
  *Behavior change:* list/read responses that previously omitted these fields
  now include them. (issue 496)
- **`QuotaStore` lifted to root `stores/`; `EventName` field renamed to `Key`.**
  The reservation-counter shape is generic `(Principal, Key) → counter`; the
  events SDK maps its `EventName` call sites through a one-line adapter. Breaking
  for external `QuotaStore` implementors (experimental surface). (issue 774)

### Added / Fixed
- **Client conformance wired into tier-check.** `scripts/refresh-conformance.sh`
  now builds `cmd/testclient` and passes it via `--client-cmd`, so
  `CONFORMANCE.md` scores the client scenario suites (Client: Core, Client:
  Auth) alongside the server ones — previously reported as skipped 0/0. New
  `testconf-client` target runs upstream's full client suite (core + auth +
  backcompat + extensions + draft, the same set tier-check runs) against
  `conformance/baseline.yml`. `cmd/testclient`'s best-effort fallback tool
  call now synthesizes arguments from the tool's input schema instead of
  sending empty args (fixes the `tools_call` scenario, which grades argument
  types). Remaining client failures are extension/draft/backcompat categories,
  annotated in `conformance/known-gaps.yaml`.
- **`core.RawJSON`** — a typed, parse-once wrapper for JSON-RPC raw values
  (params / `_meta` / …) with `Bind` / `Meta` / `Field` helpers; wire-transparent
  (round-trips identically to `json.RawMessage`). This is the read-side type
  behind the `Request.Params` flip above (issue 733). Every metadata reader on
  one request shares a single parse: the trace middleware's `_meta` readers
  (trace context / baggage / tracelink) and the SEP-2575 `_meta` gate now read
  through one cached parse instead of each re-scanning params. `Meta()` extracts
  only the `_meta` bytes and never copies a large `arguments` sibling, so
  metadata-only decode stays flat-allocation regardless of payload size —
  ~3× faster + ~3× less alloc on large `tools/call` payloads, and trace + gate
  together scan params once (2× faster at 1 MB).
- **Panic recovery in library goroutines** — a panic in a tool/background
  goroutine is recovered and surfaced as an error instead of crashing the host
  process. (issue 420)
- **v2 task-store multi-tenant isolation** — new `server.WithTaskBucketKeyer`
  derives the per-request task-store bucket from a `context.Context` (e.g. an
  auth subject) instead of the transport session. On the SEP-2575 stateless
  wire every task otherwise keys under `sessionID=""`, so tenants shared one
  bucket; the keyer closes that hole. Applies to v1 and v2, both wires; default
  behavior unchanged (session-ID keying). No `ext/auth` dependency. (issue 485)
- **`ClientModeStateless` works against discover-less servers** — `Connect` no
  longer hard-requires `server/discover`, so mcpkit connects to draft servers
  that don't expose discovery. (issue 829)
- **Protocol hardening** — server validates `Mcp-Name` for `prompts/get`
  (SEP-2243, issue 838), validates the `MCP-Protocol-Version` header against the
  body `protocolVersion` on `initialize` (issue 422), and rejects a duplicate
  `initialize` after a session is established (opt back in with
  `server.WithAllowReinitialize()`, issue 421).
- **Spec-compliant version negotiation** — on `initialize`, an unsupported
  requested `protocolVersion` now negotiates the server's preferred (latest)
  supported version and replies with it, instead of erroring with `-32602`
  (MCP 2025-03-26 §Version Negotiation). An absent `protocolVersion` is still
  rejected as malformed. *Behavior change:* a client that previously relied on
  the error must now check the returned `protocolVersion`.
- **Version feature-set resolver** — the version-gated behaviors (SEP-2243
  routing-header validation, SEP-2575 stateless `_meta` requirement) now resolve
  through a single `featuresForVersion` table (`server/protocol_features.go`)
  instead of scattered `negotiatedVersion == "..."` checks, so a new
  version-gated SEP is wired in one place across both wires.
- **`server.WithSupportedVersions(...)`** — override the accepted protocol
  versions per server so operators can drop older ones (e.g. refuse
  `2024-11-05`). `initialize` negotiates within the configured set (requests
  outside it get the set's preferred version); a post-init
  `MCP-Protocol-Version` header outside the set is HTTP 400. The stateless wire
  advertises its own draft-version set independently. (issue 419)

### Deprecated (unchanged in 0.4.0 — removal deferred)
- SEP-2577 Roots / Sampling / Logging surfaces keep their `// Deprecated:`
  blocks and full runtime behavior. Removal is deferred to a future release
  (no earlier than the spec drops them, ~2027). See
  [`docs/SEP_2577_DEPRECATIONS.md`](docs/SEP_2577_DEPRECATIONS.md).

### Already-landed breaks carried since 0.3.0 (documented here for the record)
- Handler return ABI is sealed-interface: `ToolHandler` returns
  `(core.ToolResponse, error)`, `PromptHandler` returns
  `(core.PromptResponse, error)`. (issue 486 / PR 487)
- experimental events request field renamed `params` → `arguments` on the wire
  and in Go structs. (PR 778)
- experimental events error codes generalized to the spec's reusable set.
  (issue 491)
- Error-code alignment landed on `main` since v0.3.0:
  `UnsupportedProtocolVersion` → **-32022**; `resources/read` cache defaults now
  applied on the stateless wire.

[0.4.0]: https://github.com/panyam/mcpkit/releases/tag/v0.4.0

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
