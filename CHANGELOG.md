# Changelog

All notable changes to mcpkit are recorded here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Each release also has a fuller write-up under [`docs/releases/`](docs/releases/).
Releases before 0.3.0 were tag-only and are not back-filled here.

## [Unreleased]

## [0.7.0] - 2026-09-25

A minor release consolidating 57 PRs since v0.6.0. Two tagged modules,
`experimental/ext/events` and `ext/ui`, break behaviour on the wire, so this is
a minor rather than a patch. Full write-up:
[`docs/releases/v0.7.0.md`](docs/releases/v0.7.0.md).

**Breaking:** Events webhooks are signed with the decoded secret, endpoints must
answer a verification challenge before they get a subscription, `http://`
callbacks are refused, and `events/poll` and `events/subscribe` honour each
type's `delivery` list. The `ext/ui` bridge JS sends MCP Apps `2026-01-26` wire
shapes, so hosts written against the old bridge output need updating. Migration
for each is in the write-up.

The Go toolchain floor is now 1.26.6, which clears five standard-library
vulnerabilities (#1485). `cmd/mcpskills`, which did not compile at v0.6.0, builds
again (#1487). Install it from source until #1488 lands.

This is the first release whose tag-triggered workflows are expected to run
(#1411, #1413). Confirm with `make check-release-workflows V=v0.7.0`.

### Breaking
- **`experimental/ext/events`: webhook signatures key the HMAC on the decoded
  secret** (#1432), as the spec defines, rather than on the literal `whsec_...`
  string. Out-of-tree receivers must decode too. `eventsclient.Receiver`, the
  Python client and the whole-enchilada receiver are updated. The legacy
  `X-MCP-Signature` mode is unchanged.
- **`experimental/ext/events`: webhook endpoints are verified before any
  delivery** (#1444). `events/subscribe` POSTs a signed
  `{"type":"verification","challenge":…}` envelope to `delivery.url` and only
  creates the subscription if the nonce is echoed in a 2xx body. Otherwise it
  fails with `-32015` and `data.reason` `challenge_failed` or a connection
  category. **Receivers that do not answer stop getting new subscriptions.**
  `eventsclient.Receiver` and the Python client answer automatically. Other
  receivers call `events.AnswerVerificationChallenge` and must be listening
  before they subscribe. `WithUnsafeSkipEndpointVerification()` restores the old
  behaviour. Waivers: `WithWebhookDeliveryAllowlist`, `WithPreVerifier` /
  `WithPreVerifiedDeliveryURL`, and the receiver-published document below.
- **`experimental/ext/events`: `events/subscribe` and `events/unsubscribe`
  enforce the spec** (#1434). An `http://` callback is refused with `-32602`
  unless `WithUnsafeWebhookAllowPlaintextCallbacks()` is set, and
  `WithWebhookAllowPrivateNetworks(true)` no longer implies it. A subscribe for a
  mode the type does not advertise returns `-32014`. An unsubscribe naming no
  subscription returns `-32011` instead of success. `WebhookRegistry.Unregister`
  returns a `bool`. A source with no declared `Delivery` now advertises
  `webhook` whenever a webhook registry exists, not only when it can stream.
- **`experimental/ext/events`: `events/poll` honours the descriptor's
  `delivery` list** (#1416). Polling a type whose `delivery` omits `poll`
  returns `-32014 Unsupported`. A source with no declared `Delivery` gets one
  derived at registration: `poll`, plus `push` if it implements `Subscribe`,
  plus `webhook` if a webhook registry exists.
- **`ext/ui` bridge JS speaks the MCP Apps `2026-01-26` wire shapes** (#1452).
  `MCPApp.updateModelContext(params)` sends `{content?, structuredContent?}` as
  given instead of wrapping it in `{context}`. `downloadFile` takes the spec
  `{contents}` object, and the old `(url, filename)` form still works and becomes
  a single `resource_link`. `requestTeardown()` sends
  `ui/notifications/request-teardown` and `log()` sends `notifications/message`
  (`{level, logger, data}`) instead of the non-spec `ui/teardown` and `ui/log`.
  `hostCapabilities` is read from the initialize result's `hostCapabilities`
  (it was always `{}` against a spec host). Messages from any window other than
  the parent are ignored. **Hosts that answered `ui/initialize` with
  `capabilities` instead of `hostCapabilities`, or that relied on `ui/log` or
  `ui/teardown`, need updating.** View code needs no change.

### Added
- **`ext/ui`: `AppHost` answers the View's `ui/*` host requests itself** (#1456).
  `ui/open-link`, `ui/download-file`, `ui/message`, `ui/request-display-mode`
  and `ui/update-model-context` go to the `HostHandlers` passed with
  `ui.WithHostHandlers` instead of being forwarded to the MCP server, which
  cannot answer them. A nil handler returns `-32601`, bad params `-32602`, and
  a handler can pick its own code with `ui.HostError`.
  `ui/request-display-mode` with no handler answers `inline`
  (`ui.DefaultDisplayMode`), since the spec requires an answer. Other View
  notifications (`size-changed`, `request-teardown`, `notifications/message`)
  reach `HostHandlers.Notification`. Those five methods used to fail at the
  server as unknown methods, so no working setup depended on the old path. A
  fixture recorded from the real bridge JS is replayed through `AppHost` in
  tests, so the two halves cannot drift apart unnoticed.
- **`ext/ui`: mcpkit's View extras work on upstream's `App`** (#1475).
  `ext/ui/assets/mcp-app-extras.js` (ES module, types in `mcp-app-extras.d.ts`)
  exports the SEP-2356 file picker (`selectFile`, `selectFiles`) and
  `withTraceRelay(transport, provider)`, which stamps SEP-414 trace context onto
  outbound requests and notifications of any MCP transport, including
  upstream's `PostMessageTransport`. The bridge now uses the same modules, and
  CI checks both bundles against their sources. `MCPApp.hostInfo` is new (#1452).
- **`experimental/ext/events`: receiver-published well-known document** (#1449,
  opt-in). With `WithWellKnownReceiverDocs()`, a callback is verified without a
  challenge when its origin serves `/.well-known/mcp-webhook-receiver.json`
  covering its path. Publish one with `events.WellKnownReceiverHandler`.
- **`experimental/ext/events`: `YieldGap`** (#1427), public `CanonicalKey` and
  `DeriveSubscriptionID` (#1430), `WebhookRegistry.UnsafeAllowCallbackOrigins`
  (#1462), `WebhookRegistry.PostGapByEventName` (#1469), and `EventsExtension`,
  which declares the capability under
  `capabilities.extensions["io.modelcontextprotocol/events"]` (#1416, #1424).
  v0.6.0 declared no Events capability, and the top-level form #1416 briefly
  added never shipped, so this is additive.
- **`examples/apps/any-frontend`** (#1474): one Go backend serving the same App
  through the mcpkit bridge, upstream `App`, React `useApp`, and `App` with the
  extras, driven by a Playwright suite in CI.

### Changed
- **`core.ClientSupportsExtension` reads the SEP-2575 per-request envelope**
  (#1458), so `ClientSupportsUI`, `ClientSupportsTasks` and the `BaseContext`
  methods return true on the stateless wire for a client that declared the
  extension there. They returned false for every stateless client. Same API,
  and the old answer was wrong, so this is a change rather than a break. The
  session wire is unaffected.
- **Verification POSTs are rate-limited per destination host** (#1448), 60 a
  minute by default. Over budget, `events/subscribe` answers `-32013` with
  `data.limit` `verifications_per_host`. Tune with `WithVerificationRateLimit`.
- **A source's gap and termination reach its webhook subscribers** (#1466).
  `YieldGap` sends a `gap` envelope with the source's latest cursor and
  `YieldTerminated` sends `terminated` and removes the subscription. `PostGap`
  with an empty cursor sends nothing.
- **Push frames carry the subscription id in `_meta`** (#1433), on every
  `notifications/events/*` frame. The top-level `requestId` stays.
- **The events stack is one compose file** (#1384). `docker-compose.yaml` is
  gone and `make up` layers `compose.dev.yaml` over `events-stack.yaml`, with
  follow-up fixes to the smoke script, stale recipes and `just clean-backends`.
  `examples/agents/deep-agent-supervisor` is removed.
- **Release process:** `make tag-push` pushes the root tag separately so tag
  workflows fire (#1411), and `make check-release-workflows V=<tag>` confirms
  they did (#1413).
- **Constraint C8:** recipes dispatch to `scripts/` and do not hold shell logic,
  gated by `make check-recipe-complexity`. The baseline went from 66 to 0.
- The bridge tests run in CI against upstream's ext-apps schemas.
  `docs/APPS_DESIGN.md` records frontend independence as a design decision. A
  prose pass across the published docs, and dependency updates including the
  OTel log family in lock-step at v0.22.0.

### Fixed
- **`cmd/mcpskills` compiles against the current `ext/skills`** (#1487). At
  v0.6.0 it called the removed `ListSkills` and `ErrNestedSkill`. `inspect` now
  uses `skills/list` and its JSON drops `indexSchema` and `type`, and `verify`
  accepts nested skills.
- **Go toolchain floor raised to 1.26.6** (#1485), clearing GO-2026-6218,
  GO-2026-6090, GO-2026-6089, GO-2026-6091 and GO-2026-5972.
- **`client` `list_changed` tests no longer race the GET SSE stream** (#1484,
  contributed by hy3560, fixes #1482).
- **Failure-based GC now drops suspended no-expiry webhook subscriptions**
  (#1447). A suspended target received no deliveries, so its GC window was never
  checked again and it stayed in the store indefinitely.
- **`stores/gorm` persists `VerifiedAt` (#1444), `Subject` and `SessionID`
  (#1445)**, so a subscription restored after a restart keeps its verification
  and can still be ended by backchannel logout. New columns default to `''` and
  `AutoMigrate` adds them to existing tables.
- **Push sends no `truncated: true` when the source has no position** (#1468),
  per the spec's rule for types without replay.
- **`events/poll` always returns an `events` array and `events/list` is sorted**
  (#1416). **`InProcessAppBridge` `tools/list` is sorted** (#1408).
- **The bridge answers `ui/resource-teardown` as a request and answers `ping`**
  (#1452). Host-context updates merge, `toolcancelled` carries `reason`, and
  `ui/notifications/initialized` goes out before anything a `connected`
  listener sends.
- Examples: the discord walkthrough subscribes with `arguments`, not `params`
  (#1445), and the skills security harness asserts the `ResolveRelative` defense
  for a `file://` reference (#1445). A flaky `TestWaitForTaskTimeout` compares
  errors with `errors.Is` (#1406).

### Conformance
- `testconf-events` drives all five scenarios: discovery 12/12, poll 30/30,
  push 20/20, webhook 25/26, webhook-delivery 23/23. kitchen-sink gained the
  `--conformance-events` controls the scenarios need (#1427, #1429, #1430,
  #1446, #1447, #1462, #1464, #1467). The one red row,
  `subscribe-auth-required`, is untestable rather than a defect.

## [0.6.0] - 2026-09-17

A minor release consolidating 37 PRs since v0.5.2. Three tagged modules break
API, so this is a minor rather than a patch. Full write-up:
[`docs/releases/v0.6.0.md`](docs/releases/v0.6.0.md).

**Breaking:** `ext/skills` deletes the `skill://index.json` surface in favour of
`skills/list` and `skills/get`; `experimental/ext/events` renames two duration
fields that were wrong against the spec; `experimental/ext/agents` moves its
extension ID to a vendor namespace. Migration recipes for each are in the
write-up.

### Breaking
- **`ext/skills`: `skill://index.json` and everything serving it is removed**,
  per the 2026-08-21 SEP-2640 revision. Gone: `IndexURI`, `IndexPath`,
  `IndexSchemaURI`, `IsIndexURI`, `IndexResourceDef`, `Index`, `IndexEntry`,
  `NewIndex`, `SkillType`, `MetaKeyFileDigests`, `FileDigest`,
  `Provider.Catalog`, `Client.ListSkills`, `Client.ReadSkillFileVerified`,
  `WithoutIndex`. Enumerate with `skills/list`, fetch with `skills/get`. The
  `_meta` version counter moved onto the `skills/list` result rather than being
  dropped.
- **`experimental/ext/events`: `nextPollSeconds` becomes `nextPollMs` (5 becomes
  5000) and `maxAge` in seconds becomes `maxAgeMs` in milliseconds**, across
  `events/poll`, `events/subscribe` and `events/stream`. In Go,
  `MaxAgeSeconds` becomes `MaxAgeMs` on `RegisterParams`, `WebhookTarget` and
  the GORM row. mcpkit had shipped the pre-rename names for four months against
  a spec that renamed them in 2026-05.
- **`experimental/ext/agents`: extension ID moves from
  `io.modelcontextprotocol/agents` to `io.mcpkit/agents`**, since no SEP defines
  the primitive and the working group has not chosen among three wire shapes.

### Fixed
- **Middleware bypass on the SEP-2575 stateless wire.** `resources/read` did not
  run the middleware chain, so a rate limiter, audit hook or authorization gate
  applied to `tools/call` and `prompts/get` and silently did not apply to
  resource reads. Where the middleware is an authorization gate this is a bypass:
  a caller refused a tool could ask for the resource instead. The session wire
  was never affected. Also completes template matching on the stateless wire,
  which previously returned `-32602 unknown resource`.
- **Extension settings are inlined per SEP-2133**, which maps an extension
  identifier straight to its settings object. mcpkit wrapped them in an
  `{id, specVersion, stability, config}` envelope of its own, so a conformant
  client reading `extensions[id].directoryRead` found nothing and
  `resources/directory/read` was served but invisible.
- **`ext/skills` accepts any URI scheme.** `ParseURI` required exactly `skill`,
  refusing a conforming catalog served under a domain-native scheme, which
  SEP-2640 explicitly permits.

### Added
- **SEP-2640 `skills/list` and `skills/get`**, server and client, with the
  `SkillEntry` shape, frontmatter verification, size checks, server page-size
  options and a cursor-following directory read.
- **Nested skills are permitted**, per the 2026-08-21 SEP-2640 reversal.
- **`skills/get` carries `ttlMs` and `cacheScope`**, now REQUIRED by the stable
  spec page.
- **Request-time scope challenges across all primitives.**
  `core.ScopeChallengeFunc` decides per request and sees the request, so the
  answer can depend on the arguments. Available on `ToolDef`, `ResourceDef`,
  `ResourceTemplate` and `PromptDef`. `RequiredScopes` and `AcceptedScopes` are
  deprecated but fully honoured, so nothing changes for an existing server.
- **Events `list_changed` and termination on event-type removal.**
- **The events stack ships as one self-contained compose.**

### Changed
- **The agent SDK left the repository**, extracted to
  [chakra](https://github.com/panyam/chakra). Not a breaking change: those
  modules were never in `SUB_MODS_TO_TAG` and were never tagged. Old
  `agent/vX.Y.Z` tags stay resolvable.
- **`testconf-tasks-v2` and `testconf-mrtr` now grade mcpkit.** Both reported
  PASS for months while scoring upstream's own reference server. Suite runs also
  report the upstream worktree SHA and warn when it is behind.
- **New conformance coverage:** `testconf-events` (INFO), `testconf-skills`
  retargeted to upstream `main` and gated, and the SEP-2350 scope-challenge
  suite at 17/17. Server tier-check moves 30/30 to 31/31.
- CI and build hygiene, a prose sweep across the docs corpus, and dependency
  updates across the Go and npm module groups.

## [0.5.2] - 2026-08-21

A patch release. `ctx` now reaches the wire, so an ordinary MCP call is
cancellable; the rest is module and CI hygiene. No protocol changes and no
breaking API changes. Full write-up:
[`docs/releases/v0.5.2.md`](docs/releases/v0.5.2.md).

### Fixed
- **`ctx` is threaded to the transport**, so cancelling a context or setting a
  deadline actually stops an in-flight call. `Client.callImpl` accepted a `ctx`
  and dropped it before `callDirect`, and the streamable transport defaulted to
  `context.Background()` unless the caller hand-built a `CallContext`. Only the
  streamable HTTP transport honours cancellation; legacy SSE and the core
  adapter document that they ignore it, unchanged.
- **Five placeholder sibling pins resolve.** `verify-submodule-deps.sh` parsed
  only the root require and skipped sibling requires, so unresolvable pins
  passed CI.

### Changed
- **C4 extension isolation is a real CI gate** (`scripts/check-ext-isolation.sh`)
  rather than an unrun shell block in `CONSTRAINTS.md`. Membership derives from
  the module path, so new extension trees are covered on arrival.
- **The agent SDK left the release train.** `agent/` moved to
  `experimental/agent/`, absent from `SUB_MODS_TO_TAG`, untagged, no
  compatibility promise. See `VERSIONING.md` § Agent SDK.

## [0.5.1] - 2026-08-07

A security patch. Three dependencies carrying published advisories move to fixed
versions. No API, protocol, or behavior changes. Full write-up:
[`docs/releases/v0.5.1.md`](docs/releases/v0.5.1.md).

### Fixed
- **`golang.org/x/crypto` v0.51.0 to v0.52.0** across all 16 modules requiring
  it, clearing GO-2026-5021, GO-2026-5023, and GO-2026-5033. The dependency is
  indirect everywhere and no affected symbol is reachable from mcpkit code, so
  this is supply-chain hygiene rather than a fix for an exploitable path. All
  modules move in lock-step per `DEPENDENCY_POLICY.md`.
- **`vitest` 2.1.9 to 3.2.6** in `conformance/`, `tools/compat-reports`, and
  `tools/conformance-report`, the three development trees still on `^2.0.0`.
  `ext/ui/assets` and `agent/surfaces/web/web` were already on 4.x.
- **`hono` 4.12.14 to 4.13.1** in `examples/tasks/package-lock.json`, a
  transitive peer dependency. The advisory range is `hono <=4.12.33`, so the fix
  line sits above the 4.12 series.

## [0.5.0] - 2026-08-06

Agent web surface, `context.Context` through the whole client I/O surface, and
the last documentation gap in mcpkit's Tier 1 posture closed. API-breaking on
the client and on two module paths; no protocol capability is removed. Full
write-up: [`docs/releases/v0.5.0.md`](docs/releases/v0.5.0.md).

Two thirds of the release is the agent layer. The rest is a breaking but
mechanical client change that makes every call cancellable, a documentation
push that took the SEP-1730 audit from 42/48 to 48/48, and a security pass that
found four reachable advisories nothing was previously scanning for.

The client and server entry-point changes below came from external feedback on
the SDK's first-run experience. Migration guide:
[`docs/CLIENT_CONTEXT_MIGRATION.md`](docs/CLIENT_CONTEXT_MIGRATION.md).

### Fixed
- **`ListTools(nil)` no longer panics.** Passing an untyped `nil` for a
  `context.Context` crashed with a nil-pointer dereference in the pagination
  loop's per-item cancellation check (`client/iterators.go`). Go permits `nil`
  for a `context.Context` parameter and neither the compiler nor `go vet` flags
  it, so this compiled and then crashed at run time. Every exported method that
  accepts a context now normalizes `nil` to `context.Background()`. It is a
  crash guard, not an endorsement — pass a real context so cancellation works.
- **`Server.Register` no longer drops unsupported values silently.** The type
  switch had no `default`, so anything that was not a `Tool`, `Resource`,
  `ResourceTemplate`, `Prompt`, or `core.TypedToolResult` was discarded with no
  error, no log, and no failure until a caller hit the missing name. Writing
  `&server.Tool{...}` instead of `server.Tool{...}` was enough to lose a tool.
  It now panics with the offending type and argument index. `Register` takes
  `...any` so a single call can mix primitive kinds, which is why the type
  system cannot catch this; registration is a start-up action, so a panic
  surfaces it immediately.

### Added
- **`Server.Ready() <-chan struct{}` and `Server.Addr() string`.** `Run` and
  `ListenAndServe` block and bound their listener inside the goroutine they
  start, so a caller had no way to know when the port was reachable and had to
  sleep before connecting. `Ready` closes once the listener is bound; `Addr`
  reports the address it bound, which makes `":0"` usable. `Ready` never closes
  if the bind fails, so select on it together with the error from `Run`.
- **`Server.RunWithListener(ln net.Listener, opts ...TransportOption)`** serves
  on a listener the caller has already bound, so the port is accepting before
  serving starts and there is no window to race at all.

### Breaking
- **Every `client.Client` method that performs I/O now takes a
  `context.Context` first.** Previously the list methods took one and
  `ToolCall` / `ReadResource` / `Call` / `Connect` did not, which left the most
  common calls with no way to time out or cancel, and made the `nil`-context
  crash above easy to hit. Affects `Connect`, `Call`, `CallContext`, `ToolCall`,
  `ToolCallFull`, `ReadResource`, `ReadResourceFull`, `SubscribeResource`,
  `UnsubscribeResource`, `SetLogLevel`, `NotifyRootsChanged`, the four
  `ListXPage` helpers, and the package-level `ToolCall` / `ToolCallTyped` /
  task helpers (`GetTask`, `UpdateTask`, `CancelTask`, `GetTaskV1`,
  `GetTaskPayloadV1`, `ListTasksV1`, `CancelTaskV1`, `ToolCallAsTaskV1`).
  Accessors are unchanged. The compiler flags every call site; see the
  migration guide for the full before/after table, including the two shapes
  (variadic and generic helpers) that surface as type errors rather than
  "not enough arguments".
- **`Connect(ctx)` bounds the handshake, not the session** — mirroring
  `grpc.DialContext`. Once it returns `nil` the session outlives the context,
  so a short timeout is safe. `WithConnectTimeout` still applies and composes;
  whichever fires first wins. Use `Close` to end a session.

### Changed
- Requires `github.com/panyam/servicekit` v0.1.4 for its new
  `http.WithListener` option, which is what lets mcpkit bind before serving and
  report readiness honestly.

### Added (agent + protocol)
- **Agent web surface** (issue 1193). A browser surface over the same
  `agent/host` the terminal drives, on the same session at the same time.
  Per-session event log on `gocurrent.Queue` with replay from offset 0, a
  pending-ask barrier so an elicitation reaches every surface with first-answer
  wins, a Connect bridge with a server-streaming `Watch`, a DockView + Solid
  frontend with five observability panels (sub-agent tree, activity timeline,
  memory inspector, tool-call and offload inspector, budget gauges), multi-
  session routing by `session_id`, and RunStore-backed persistence.
  PR 1201, 1205, 1206, 1208, 1209, 1210, 1211, 1214, 1226.
- **Server-declared agent discovery** (epic 1142). `experimental/ext/agents`
  implements the Agents WG pre-SEP wire primitive: `agents/list` returns a
  roster without tool schemas, `agents/get` resolves one agent's instructions
  and scoped tools. Only discovery is new wire surface; invocation rides the
  existing `tools/call`. Includes a Go client SDK, an `AgentSource` bridge,
  SEP-414 discovery spans, and a deep-agent supervisor demo. PR 1181, 1186,
  1190, 1191.
- **`preempt` signal kind** with a kind-aware barrier break, parent-granted
  rather than child-authoritative, and the opt-in interruptible turn.
  PR 1170, 1178.
- **Four primitive guides and 25 Go `Example` functions**, where the repo
  previously had none. `docs/COMPLETIONS.md`, `docs/PROMPTS.md`,
  `docs/ELICITATION.md`, and a Ping section in `ARCHITECTURE.md`. Completions
  was the only core capability slot with no user-facing documentation; prompts
  was the only core primitive with no dedicated prose. The examples run under
  `go test`, so the guides cannot drift. PR 1229, 1231, 1232, 1233.

### Breaking (additional)
- **`agent/web` moved to `agent/surfaces/web`** and `cmd/agentchat` to
  `agent/surfaces/chat`, grouping the surfaces under one parent. Import paths
  change; there is no shim, because the agent modules have never carried a
  release tag. PR 1223.
- **Elicitation responses are validated against `requestedSchema`.** An
  accepted response that returns a string where the server asked for an
  `integer`, a value outside a declared `enum`, or omits a `required` property
  now gets `-32602` with a structured error list instead of being forwarded.
  Opt out with `client.WithElicitationValidation(false)`; mirrors
  `server.WithSchemaValidation(false)`, and both default to on. PR 1234.

### Fixed (security)
- **Four reachable advisories nothing was scanning for.** `govulncheck ./...`
  is module-scoped and does not descend into nested modules, so the root scan
  covered the root module and nothing else, leaving all 24 published
  sub-modules unscanned including `ext/auth`. Fixing the scan surfaced
  GO-2025-3540 (`redis/go-redis/v9`), GO-2026-5970 (`golang.org/x/text`),
  GO-2026-6061 (`google.golang.org/grpc`), and GO-2026-5004 (`jackc/pgx/v5`),
  all reachable from mcpkit's own code and all now bumped. `stores/redis` is
  published, so 0.4.0 shipped with a reachable go-redis advisory.
  PR 1216, 1222.
- **A vulnerable pillow resolution** kept alive purely by a
  `requires-python = ">=3.9"` floor nothing needed. PR 1236.
- **The `pre-push` hook had been blocking every push** for anyone who ran
  `make setup-hooks`, via a stale path left behind by a module move. PR 1224.
- **Host-side SEP-2640 skills hardening**: origin tagging so a skill body
  carries its origin label before entering context, and per-origin name
  resolution so a bare name served by more than one origin returns a
  disambiguation prompt rather than silently first-matching. PR 1185, 1187.

### Changed (supply chain + CI)
- **A weekly `vulncheck` workflow and a monthly repo-wide dependency sweep**,
  both time-triggered because the advisory database moves independently of the
  code. PR 1224, 1227.
- **Two new CI gates.** `make check-dep-consistency` fails when a third-party
  dependency is pinned at two or more versions across published modules, since
  MVS resolves to the maximum and a split pin means some modules silently build
  against a version their `go.mod` does not name. `make check-dependabot-dirs`
  fails when a configured Dependabot directory no longer exists, which is how
  one npm tree went unmonitored. PR 1227, 1236.
- **Dependabot rescoped**: Go updates move as one lock-step sweep rather than
  per-directory PRs, which could not satisfy the lock-step rule the dependency
  policy requires. PR 1227.
- **`make` is the only task runner CI uses.** CI installed a third-party action
  to run six recipes `make` already had. Justfiles remain as an experiment;
  `make` is authoritative when they disagree. PR 1235.
- **Repository history rewritten.** 141 MB of committed build artifacts purged,
  taking the repository from 466 MB to 23 MB. Commit SHAs changed; no published
  module content did, verified by comparing every tag's tree SHA before and
  after, so `sum.golang.org` checksums for released versions remain valid. A
  `pre-commit` hook and CI gate now reject committed executables by magic bytes.

## [0.4.0] - 2026-08-03

Full notes: [`docs/releases/v0.4.0.md`](docs/releases/v0.4.0.md).

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
- **Go toolchain floor raised to 1.26.5** across the root module and every
  sub-module. Clears **GO-2026-5856** (Encrypted Client Hello privacy leak in
  the `crypto/tls` standard library, reachable from `server.ListenAndServe` and
  `client.DoWithAuthRetry`), fixed in go1.26.5. `just audit`'s govulncheck stage
  is green on the shipped toolchain.
- **Fixed a data race on `Client.transport`.** `doConnect` published the
  transport field while a concurrent `Close` (reachable via `client.Group`'s
  async connect) read it, both unsynchronized. Both accesses now go through the
  existing `Client` mutex, with the lock scoped to the field read/write so a
  `Close` racing an in-flight connect still cancels without blocking. Surfaced
  by `go test -race` under the go1.26.5 scheduler.
- **Generalized `resultType` discriminator + DiscoverResult caching hints
  (2026-07-28 final revision, issue 1174).** Every result the server emits on
  the 2026-07-28 wire now carries the required `resultType` field (stamped
  `"complete"` when a result type doesn't set its own; the MRTR
  `"input_required"` and tasks `"task"` variants keep their values), via a
  new `core.InjectResultTypeIntoResult` applied at both dispatch chokepoints
  — always on the stateless wire, feature-gated on the legacy wire so
  pre-draft sessions keep byte-identical output. `DiscoverResult` gains the
  now-required `ttlMs` + `cacheScope` fields (conformant defaults: 0,
  public). Clears 11 of the 13 wire-schema-valid failures surfaced by
  upstream conformance's new per-version schema validation; the remaining
  task-envelope errors are an upstream validator gap (no `resultType: task`
  branch), annotated in `conformance/known-gaps.yaml`.
- **CIMD advertised-support gate + SEP-2207 refresh-token registration.**
  `OAuthTokenSource` now prefers its configured `ClientMetadataURL` as the
  client_id exactly when the AS advertises
  `client_id_metadata_document_supported` (SEP-991 SHOULD; the flag surfaced
  by oneauth v0.1.36), falling back to DCR when the AS does not advertise it
  and DCR is available; a CIMD-only configuration still presents the URL
  best-effort. `DefaultClientRegistration()` now declares
  `refresh_token` alongside `authorization_code` in `grant_types`
  (SEP-2207). Clears the last two client-conformance warnings: the full
  suite's baseline is down to the two SEP-1932-gated DPoP scenarios.
  oneauth bumped to v0.1.36 across all consuming modules.
- **Adaptive-probe protocol-version header (SEP-2575).** The
  `ClientModeAdaptive` / `ClientModeStateless` `server/discover` probe now
  carries the `MCP-Protocol-Version` HTTP header matching its
  `_meta.protocolVersion`. The header was previously attached only after the
  client flipped to the stateless wire, so a compliant stateless-only server
  (which MUST reject headerless requests) rejected the probe itself and
  adaptive mode could never connect. Surfaced by the SEP-2352
  `auth/authorization-server-migration` conformance scenario, which now
  passes 3/3 — the AS-change re-registration machinery (issue 500 cluster D)
  was already correct once the wire connected. (issue 1100)
- **`auth.JWTBearerTokenSource` — RFC 7523 JWT-bearer grant for workload
  identity federation (SEP-1933, issue 1101).** A `core.TokenSource` for
  workloads whose identity is attested out-of-band: the caller supplies the
  signed assertion (static or via `AssertionProvider` for rotating platform
  credentials) and the source exchanges it at the discovered AS with
  `grant_type=urn:ietf:params:oauth:grant-type:jwt-bearer`, no browser and no
  client secret. Scope step-up via `TokenForScopes`; a failed exchange is
  surfaced verbatim with no self-retry or grant fallback per RFC 7523. Flips
  the `auth/wif-jwt-bearer` conformance extension scenario to pass (12/12
  checks).
- **2025-03-26 legacy OAuth discovery fallback (issue 451, reversed wontfix).**
  `ext/auth.DiscoverMCPAuth` now falls back to the 2025-03-26 authorization
  spec's discovery shape when the server publishes no Protected Resource
  Metadata: AS metadata at the origin's `oauth-authorization-server`
  well-known path, then the legacy default endpoints (`/authorize`, `/token`,
  `/register` at the origin). Reached only on a definitive 404 at both RFC
  9728 PRM locations — any other PRM failure still errors, so a modern
  server's flow cannot be downgraded, and a `WWW-Authenticate`-advertised PRM
  URL never falls back. New `MCPAuthInfo.LegacyDiscovery` field; `PRM` is nil
  on this path (nil-guard added to `ClientCredentialsTokenSource`). Flips the
  two `auth/2025-03-26-*` conformance scenarios to pass: Client: Auth
  aggregate 16/16.
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
- **SEP-2575 final-revision `_meta` identity alignment (spec PR 3002).**
  `clientInfo` in the per-request `_meta` envelope is now optional on the
  stateless wire: requests that omit it are served instead of rejected with
  `-32602` (clients SHOULD still send it, and mcpkit's client does). Servers
  now stamp `_meta["io.modelcontextprotocol/serverInfo"]` on every stateless
  success result via new `core.InjectServerInfoIntoResult` (caller-set values
  win; error responses are not stamped), restoring the server identity the
  removed `initialize` handshake used to carry. *Wire change:* `serverInfo`
  moved out of the `server/discover` result body into the result `_meta`;
  `client.DiscoverResult` reads the `_meta` form first and falls back to the
  pre-3002 body field, so older draft servers keep working. New
  `core.MetaKeyServerInfo` and `core.ResultMeta`. Both fields are
  self-reported and unverified, for display/logging/debugging only.
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

[0.7.0]: https://github.com/panyam/mcpkit/releases/tag/v0.7.0
[0.6.0]: https://github.com/panyam/mcpkit/releases/tag/v0.6.0
[0.5.2]: https://github.com/panyam/mcpkit/releases/tag/v0.5.2
[0.5.1]: https://github.com/panyam/mcpkit/releases/tag/v0.5.1
[0.5.0]: https://github.com/panyam/mcpkit/releases/tag/v0.5.0
[0.4.0]: https://github.com/panyam/mcpkit/releases/tag/v0.4.0
[0.3.0]: https://github.com/panyam/mcpkit/releases/tag/v0.3.0
