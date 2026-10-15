# ext/auth — implementation notes

Auth lore that is not in the design doc. For the design and the supported spec versions see
`docs/DESIGN.md`; for tracing see `docs/SEP_414_OTEL.md` § `ext/auth` JWT validator instrumentation
and § `oneauth` wiring.

---

## Lazy `OAuthTokenSource` scope acquisition (#818, PR 820)

`OAuthTokenSource.Token()` does **not** pre-acquire. Until a server 401/403 challenge selects the
scope, or the caller pins explicit `Scopes`, `Token()` returns `core.ErrNoTokenYet` and runs no
OAuth flow.

The client transport's `DoWithAuthRetry.SetAuth` treats that sentinel as *skip-header*: send the
request unauthenticated so the server's `WWW-Authenticate scope=` drives selection per RFC 6750
§3.1, then `OnUnauthorized` arms the source via `TokenForScopes`.

**`OnUnauthorized` routes every scope-aware 401 through `TokenForScopes`**, with no
`len(scopes) > 0` guard. A scope-less 401 is an empty merge that still flips the source out of its
deferred state, so the retry acquires via the PRM `scopes_supported` fallback instead of looping on
`ErrNoTokenYet`.

Mechanism: an `armed bool` gate in `Token()`, set by `TokenForScopes`. **`Invalidate` deliberately
leaves `armed` set**, so a step-up retry does not re-defer.

Discovery no longer pins scope. **`MCPAuthInfo.Scopes` is removed**, and both the probe
`WWW-Authenticate` scope capture and the Step-5 PRM-fallback assignment were dropped. Acquisition
reads the catalog from `info.PRM.ScopesSupported`, as does `ClientCredentialsTokenSource` (which
stays eager).

This fixes the eager-PRM bug where a per-method-gated server (`initialize` open, `tools/list`
needing `mcp:basic`) saw the client request the broad PRM `mcp:profile` first.

**Behavior change**: standalone `Token()` callers get `ErrNoTokenYet` until armed, or until they
set explicit `Scopes`. Documented on the type and on `Token`.

---

## DCR registers the redirect it will listen on (#1535)

`OAuthTokenSource` used to register `http://127.0.0.1:0/callback` through DCR, the placeholder in
`DefaultClientRegistration`, and then let oneauth's `LoginWithBrowser` listen on
`http://localhost:<random>/callback`. Host and port both differed from what was registered. Every
mock we tested against accepted it, because RFC 8252 §7.3 asks an authorization server to allow
any port on a loopback redirect. `metronome-mcp.fly.dev`, the SEP-3415 reference server, compares
redirect URIs exactly and answered `/authorize` with "redirect_uri is not registered".

The source now picks the loopback port once (`CallbackPort`, or a free port chosen on first use and
kept), registers `http://localhost:<port>/callback`, and passes the same port to `LoginWithBrowser`.
The port is kept so the cached DCR registration still matches on a later login. A caller-supplied
`DCRMeta` is sent as is, and that caller should set `CallbackPort` to agree with it.

**The source holds that port bound whenever no login is using it** (`portHold`), releasing it just
before `LoginWithBrowser` binds it and taking it back after. The first version picked a free port
and let it go, leaving it free across the discovery and DCR round trips. CI's conformance run starts
`auth/dpop` and `auth/dpop-nonce` as two testclient processes at once, and on Linux the second was
sometimes handed the port the first had just released. One login then failed to bind, and
`check-conformance-stale` saw `auth/dpop-nonce` drop from 9 passing checks to 3. It passed on
re-run, which is what made it look like noise. Before this change oneauth bound port 0, which the
kernel resolves atomically, so the race is new with the fix rather than old.

Two loose ends, deliberately not fixed here:

- **`Invalidate` keeps the cached DCR registration.** A server that restarts and forgets its clients
  (metronome does, on a crash) then refuses that `client_id` forever. `cmd/mcp-auth-proxy` gets
  around it by discarding the whole source on a 401.
- **`FollowRedirects` treats any answered GET as success.** When `/authorize` answers 400, the
  login waits out its five-minute timeout for a callback that never comes. The proxy's
  `approveHeadless` fails on a page with no redirect and no approvable form instead.

`cmd/mcp-auth-proxy` exists because the conformance runner cannot send a bearer token. It is how
`make conf-events-remote` grades another implementation's events server.

---

## Adaptive/stateless probe must carry `MCP-Protocol-Version` (PR 1113)

The `server/discover` probe runs **before** `useStatelessWire` flips, and the transport only
auto-attaches the header after. So a compliant stateless-only server rejected the probe and
adaptive mode could never connect. Fixed with an explicit header in `adaptiveProbe`.

**This one bug masked an entire migration scenario.** The SEP-2352 AS-change re-register machinery
(the `dcrAS` compare in `token_source.go`) was correct all along; it just never got exercised.
Worth remembering as a debugging pattern: a scenario failing at setup can look like a failure of
the feature it was written to test.

---

## 2025-03-26 legacy OAuth discovery fallback (#451, reversed wontfix)

The client-side ladder and the load-bearing no-downgrade rule live in `docs/DESIGN.md`
§ Supported spec versions.

Two things to preserve when touching it:
- Fall back **only** on a definitive 404 at *both* PRM well-knowns.
- The synthesized default-endpoint metadata carries S256, so the C11/C12 PKCE gate passes against
  our own document.

---

## `JWTBearerTokenSource` (RFC 7523 WIF grant, #1101)

The grant `assertion` is distinct from the private_key_jwt `client_assertion` client-auth. Do not
conflate them.

Built on oneauth's `AuthClient.JwtBearerGrant`, which has shipped since v0.1.5. **Check the
oneauth primitive before assuming a pushdown or version bump is needed**, since this one was already
there.

---

## The two-TracerProvider pattern

`JWTConfig` carries two distinct TracerProvider fields, and they are deliberately not one:

- `TracerProvider mcpcore.TracerProvider` gates ext/auth's own spans.
- `OneauthTracerProvider trace.TracerProvider` is the **OTel SDK type**, not mcpkit's abstraction,
  threaded via `keys.WithTracerProvider` to enable oneauth's internal spans.

They stay separate mostly because oneauth's API takes the OTel SDK type directly, and unwrapping inside
ext/auth would couple it to `ext/otel`. Adopters bridge with
`commonotel.UnderlyingOTelTP(tp)`, which type-asserts to `*mcpotel.Provider` and returns nil for
Noop. ext/auth depends on the core abstraction only; there is no compile-time dependency on
`ext/otel`.

Full detail in `docs/SEP_414_OTEL.md`.

---

## Bumping oneauth: sweep all 9 modules in lock-step

The operational rider that keeps biting. oneauth appears in nine modules' `go.mod`, six directly
and three as indirect:

`cmd/testclient`, `ext/auth`, `tests/keycloak`, `tests/e2e`, `examples/auth`,
`examples/fine-grained-auth`, `examples/events/discord`, `examples/events/telegram`,
`examples/whole-enchilada/events/event-server`.

`grep -rl 'panyam/oneauth v' --include=go.mod .` finds them, and is the list to trust over this one.

Bump all of them together and run `make tidy-all`. **Leaving even one behind fails CI's
`test-auth` job** via `tests/e2e` MVS resolution, and the error points at e2e rather than at the
module you missed.

Current pin: v0.1.39. Version-specific fixes worth knowing:
- **v0.1.14** — required for the two-TP pattern.
- **v0.1.18** — fixed `client.NewAuthClient` stripping the path from the issuer URL, which broke
  browser auth against Keycloak realms. `LoginWithBrowser` now prefers `c.cachedASMeta` over
  re-discovering at `http://kc/.well-known/openid-configuration`.
- **v0.1.19** — fixed `client.Login` (ROPC) to take the standard OAuth path (`requestTokenForm` on
  the discovered `token_endpoint` with `client_secret_basic`) when AS metadata is cached; the
  legacy `/auth/cli/token` JSON path remains the no-metadata fallback. `LoginRequest` gained
  `ClientSecret`, and `oneauth token password` gained `--client-secret`.
- **v0.1.39** — oneauth#387, the pushdown from #1538. `BrowserLoginRequest.Listener` lets the caller
  own the loopback listener (no release/rebind window), `LoopbackRedirectURI` gives the exact string
  to register, `FollowRedirects` fails on an authorization-server error instead of returning nil,
  and a headless consent opener sits beside it. mcpkit still uses its own `portHold` and
  `approveHeadless` until #1539 switches over.

Install the CLI once with `go install github.com/panyam/oneauth/cmd/oneauth@v0.1.39`. Use it from
PATH, since `go run @ver` gets confused by oneauth's own replace directives.
