# SEP-2640 ↔ SEP-2575: Stateless skills interaction

The 2026-07-28 release candidate (announced on `blog.modelcontextprotocol.io/posts/2026-07-28-release-candidate`) ships a stateless protocol core: SEP-2575 removes `initialize`/`initialized`, SEP-2567 removes `Mcp-Session-Id`, SEP-2260 restructures server-to-client requests, SEP-2243 adds `Mcp-Method` / `Mcp-Name` routing headers, SEP-2549 adds `ttlMs` and `cacheScope` on list-result responses.

SEP-2640 (Skills Extension), drafted before the RC, declares the skills capability via `capabilities.extensions["io.modelcontextprotocol/skills"] = {}` in the `initialize` response. That mechanism disappears once SEP-2575 ships.

This doc captures how skills work end-to-end under the stateless model, the data-volume and operational tradeoffs, and the two spec-level questions that need answers either in an SEP-2640 update or in companion guidance. It exists so the `ext/skills` implementation (the SEP-2640 capability wiring in `extension.go`, plus the remaining client and conformance work) can be steered through the transition without re-architecting later.

## How a stateless skills client works

1. **Probe.** Client GETs `skill://index.json` (or `/.well-known/agent-skills/index.json` over the HTTP bridge if one exists). Success means skills-capable. 404 or `-32003` "Missing Required Capabilities" (per SEP-2575) means no.
2. **Cache the index.** Server returns `ttlMs` (SEP-2549) so the client knows when to re-poll. Public skills can ship with `cacheScope: public` so shared proxies and CDNs can hold the index.
3. **Pick a skill, fetch `SKILL.md`.** Verify the SHA-256 digest from the index against the bytes. Cache.
4. **Lazy-fetch support files on demand.** Same digest check per file.
5. **Re-poll the index past TTL.** Compare digests. Only re-read files whose digests changed.

No handshake, no session, no subscriptions. The polling cadence is set by the server via `ttlMs`. The fsnotify-based hot-reload tracked in this repo's skills work stays a server-internal optimization to keep the cached index fresh; clients still find out by polling.

## Data volume and operational tradeoffs

Versus the stateful baseline:

| Dimension | Stateful | Stateless | Net |
|---|---|---|---|
| Capability handshake | small, once per connection | gone | Removed |
| `resources/list` of N skills × M files | once per client, push-invalidated | once per client per TTL window, cacheable across clients via `cacheScope: public` | Roughly equivalent, more CDN-friendly |
| `skill://index.json` | once, push-invalidated | every TTL window | Slightly more traffic, offset by the index being tiny and digest-driven |
| Per-file reads | lazy, no re-read | lazy + digest-gated re-read | Win, digests skip unchanged content |
| Archive (`.tar.gz` / `.zip`) delivery | one fetch, push-invalidated | one fetch per TTL window, CDN-friendly | Win, stateless makes archives more attractive, not less |
| Subscriptions on skill URIs | cheap | expensive via `subscriptions/listen`, exactly the resource-subscription pressure pattern surfaced in WG discussion 2026-06-02 | Avoid, poll with TTL instead |
| Per-request routing headers (SEP-2243 `Mcp-Method`, `Mcp-Name`) | n/a | small per-request overhead | Adds up if a 50-file skill is fetched file-by-file, which is exactly what the archive entry type solves |
| Horizontal scale | sticky sessions or shared session store | none required | Win, matches the RC headline |

Net: marginally more polling traffic on `index.json`, offset by digest-gated reads, public-scope caching, and archives being a near-perfect fit. The DDoS-prone path (long-poll subscriptions) is simply not taken.

## Other tradeoffs

- **Capability discovery becomes probe-based** rather than handshake-declared. The probe is cheap (one HEAD or one GET on `index.json`), but it does mean a client cannot know up-front whether a server supports skills. In practice this is acceptable: clients trying to load a skill will either find one or not, and the failure mode is clean.
- **Version negotiation has nowhere obvious to live.** `initialize` was where a server could advertise `extensions["io.modelcontextprotocol/skills"] = {version: "1.2"}` once such a field existed. In stateless, that information has to surface elsewhere. See open question 2 below.
- **Server-side gating inverts.** Stateful: server could refuse a `resources/read skill://...` because the client didn't negotiate the capability. Stateless: the request itself is the asking, so the server either has the resource or returns `-32602` / `-32003`. Cleaner, fewer edge cases.
- **Horizontal scale is a clean win.** No sticky sessions, no shared session stores. Skills servers behind a CDN with `cacheScope: public` on `index.json` and the archives become trivial to scale.
- **Tool-allowlist server-name resolution** (raised in WG discussion 2026-06-02) is no harder in stateless. It was always a client-side identity question; `initialize` was not where it lived. Out of scope here.

## Spec-level questions (resolved 2026-06-04)

Both questions had answers already in flight upstream. The WG champion pointed at concrete mechanisms in the `skills-over-mcp-wg` channel thread "Extension discovery" (2026-06-04T18:05Z onward). Both resolve to the same surface (`capabilities.extensions[extensionID]` in the `server/discover` response), which simplifies the implementation story.

### Q1: How does capability discovery work without `initialize`?

**Answer: `server/discover`** (defined in SEP-2575 alongside the removal of `initialize`). See `modelcontextprotocol.io/specification/draft/server/discover`.

- It is a single RPC every server **MUST** implement.
- Response returns `supportedVersions`, `capabilities` (`ServerCapabilities`, the same type that previously sat under `initialize`), `serverInfo`, and optional `instructions`.
- Response carries `ttlMs` and `cacheScope` (SEP-2549), so discovery is cacheable across clients and proxies.
- Calling it is **optional** for clients. A client may invoke any RPC inline and handle `UnsupportedProtocolVersionError`, so skipping discovery is a valid client choice.

**What this means for SEP-2640:** the existing `capabilities.extensions["io.modelcontextprotocol/skills"] = {}` declaration moves from the `initialize` response to the `server/discover` response. The SEP needs a one-line update to point at `server/discover` instead. No new mechanism, no probe scheme.

**Fallback path still valid:** for clients that skip `server/discover`, the probe model (GET `skill://index.json`, fall back to `resources/list` filtered for `skill://` URIs) remains a legitimate inline discovery option. SEP-2640's existing `host-no-empty-index-assumption` constraint already supports this.

### Q2: Where do extension versions get advertised?

**Answer: in the extension's settings object** (`capabilities.extensions[extensionID]`). See in-flight proposal at `modelcontextprotocol/agents-wg#18` ("SEP-XXXX: Extension Versioning", authored by Luca Chang, opened 2026-06-02).

- Each extension carries a semver in its settings object.
- May declare its core protocol version dependency.
- Major version diff = incompatible, negotiated via inline retry (mirrors SEP-2575's protocol version negotiation flow).
- Minor diff = no conflict; each peer uses the features common to both.
- Patch diff = no behavioral change.
- New error code `-32005` "Unsupported Extension Version" reports a major mismatch and surfaces the supported major versions for retry.

**What this means for SEP-2640:** `capabilities.extensions["io.modelcontextprotocol/skills"]` evolves from `{}` to something like `{version: "1.0.0"}` (exact shape pending PR 18 merge). Since the extension declaration lives in the `server/discover` response per Q1, the version rides along in the same surface. mcpkit already tracks the spec version as the `SpecVersion` constant in `ext/skills/extension.go`, so once PR 18's shape lands the only change is reading from that constant into the settings object.

## Impact on `ext/skills` work in flight

- **`extension.go`** currently returns `core.Extension{ID, SpecVersion, Stability}` from `SkillsExtension.Extension()`. That payload doesn't change. What changes is the transport plumbing path that surfaces it: today the dispatcher folds it into the `initialize` response, post-SEP-2575 it surfaces in the `server/discover` response. The dual-mode work (initialize legacy + `server/discover` stateless) belongs to mcpkit's broader SEP-2575 implementation, not to skills specifically. No skills-side rework needed.
- **Extension version field.** Once `modelcontextprotocol/agents-wg#18` settles, `SkillsExtension.Extension()` needs to read `SpecVersion` into the settings object (the value side of `capabilities.extensions[ID]`). That's a one-field addition keyed on PR 18's shape.
- **`indexer.go`** doesn't need changes for capability or version negotiation. Both live in `server/discover`. The index keeps doing what it does: discovery of skill entries with digests, TTL, and `cacheScope`.
- **Conformance YAML at `modelcontextprotocol/conformance#330`** has 25 check rows. None assert capability-via-initialize (confirmed by the 2026-06-04 review pass). The `sep-2640-host-no-empty-index-assumption` check remains consistent with both the `server/discover` answer and the inline-probe fallback. No PR 330 changes needed.
- **`conformance/skills/` scenarios** should assume `server/discover` for capability negotiation and treat skipping discovery as a valid inline path (per the spec: "Calling `server/discover` is optional for clients"). When extension versioning lands, add a scenario for `-32005` "Unsupported Extension Version" major-mismatch handling.
- **Findings PR planned against `modelcontextprotocol/experimental-ext-skills`** can carry this resolution as a documentation contribution: "here's how SEP-2640 lines up with `server/discover` and the in-flight extension versioning proposal."

## Status

- 2026-06-04: doc written. Companion WG message posted in `skills-over-mcp-wg` channel (msg `1512140837033738402`).
- 2026-06-04 (PR 330 review pass): the conformance YAML at `modelcontextprotocol/conformance#330` was checked for initialize-dependence. None of the 25 check rows assert capability-via-initialize, and none reference `capabilities.extensions`. The closest discovery-related check (`sep-2640-host-no-empty-index-assumption`) is consistent with a multi-signal discovery model (probe `index.json`, fall back to `resources/list` filter). No PR 330 changes needed for stateless support.
- 2026-06-04 (WG response): Peter Alexander (Anthropic) opened an "Extension discovery" thread on the WG post and answered both questions with concrete pointers. Q1 → `server/discover` (already defined in SEP-2575). Q2 → `modelcontextprotocol/agents-wg#18` (in-flight Extension Versioning proposal). Doc updated to reflect both resolutions. Outstanding action: track PR 18 for the final version-shape; once it merges, surface `SpecVersion` from `ext/skills/extension.go` into the extension settings object per the agreed shape.

## References

- SEP-2640 (Skills Extension): `modelcontextprotocol/modelcontextprotocol#2640`
- SEP-2575 (stateless protocol core, also home of `subscriptions/listen`, `-32003`, and `server/discover`): in the 2026-07-28 RC bundle
- `server/discover` spec: `modelcontextprotocol.io/specification/draft/server/discover`
- Extension Versioning proposal (in flight): `modelcontextprotocol/agents-wg#18`
- SEP-2133 (Extensions Track, reverse-DNS identification): in the 2026-07-28 RC bundle
- SEP-2549 (TTL for List Results, applies to `server/discover` response): merged Final 2026-05-15
- SEP-2243 (`Mcp-Method` / `Mcp-Name` routing headers): in the 2026-07-28 RC bundle
- 2026-07-28 RC announcement: `blog.modelcontextprotocol.io/posts/2026-07-28-release-candidate`
