package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/panyam/mcpkit/core"
)

// HTTP header names for the W3C Trace Context propagation that
// Deliver / DeliverToTarget stamp on every outbound POST when a
// trace context is available — SEP-414 P6 (issue 683). Bare W3C
// names; not under any io.modelcontextprotocol/ namespace.
const (
	traceparentHTTPHeader = "traceparent"
	tracestateHTTPHeader  = "tracestate"
)

// traceContextForDelivery resolves the trace context to stamp on an
// outbound webhook POST. Precedence:
//  1. ctx (passed in from the in-process call chain — Register's
//     emit hook, EmitToWebhooks helper, etc.)
//  2. event.Meta (the persistent carrier — caller pre-stamped via
//     YieldingSource.SetMetaFunc or the yield-time auto-injection)
//
// Returns a zero TraceContext when no source has one — the caller
// skips setting the HTTP headers in that case, preserving "no
// propagation when none was intended" for backward-compat with
// pre-SEP-414 receivers.
func traceContextForDelivery(ctx context.Context, event Event) core.TraceContext {
	if tc := core.TraceContextFromContext(ctx); !tc.IsZero() {
		return tc
	}
	return core.ExtractTraceContext(event.Meta)
}

// Webhook TTL spec envelope. WG guidance (Peter, 2026-06-05 in
// #triggers-events-wg) is that webhook subscription TTLs must fall in
// [5min, 24h]: shorter creates excessive refresh churn for a long-
// running production receiver, longer is a soft-state leak. The
// envelope is enforced by WithWebhookTTL at registration time —
// out-of-range values are clamped with a logged warning. Tests / demos
// that legitimately need a sub-minimum TTL (driving the SDK's
// auto-refresh path on a fast cadence) opt out via
// WithUnsafeWebhookTTLBypass(), mirroring the UnsafeAnonymousPrincipal
// precedent.
const (
	// DefaultWebhookTTL is the soft-state expiry the registry applies to
	// every subscription when no explicit TTL is configured. Sits inside
	// the spec envelope.
	DefaultWebhookTTL = 1 * time.Hour

	// MinWebhookTTL is the spec envelope floor. WithWebhookTTL clamps
	// any positive smaller value up to this floor unless
	// WithUnsafeWebhookTTLBypass is also set.
	MinWebhookTTL = 5 * time.Minute

	// MaxWebhookTTL is the spec envelope ceiling. WithWebhookTTL clamps
	// any larger value down to this ceiling. The bypass does not raise
	// the ceiling — operator intent above 24h is rare enough to handle
	// with a code change.
	MaxWebhookTTL = 24 * time.Hour

	defaultWebhookMaxBodyBytes     = 256 * 1024       // spec §"Webhook Security" → "Delivery profile" L487
	defaultWebhookSuspendThreshold = 5                // N consecutive failures before Active=false
	defaultWebhookSuspendWindow    = 10 * time.Minute // sliding window over which failures accumulate
)

// WebhookOption configures a WebhookRegistry at construction time.
type WebhookOption func(*WebhookRegistry)

// WebhookOnRemoveHook fires whenever a target is actually removed from
// the registry — explicit Unregister, TTL prune, or PostTerminated.
// Does NOT fire on the suspend transition (Active=true→false), which
// keeps the target in the registry as paused — suspend ≠ unsubscribe;
// refresh reactivates without re-firing on_subscribe per spec
// §"Webhook Delivery Status" L460.
//
// Hooks fire OUTSIDE the registry's lock so a slow listener can't
// serialize Register/Unregister/PostTerminated.
type WebhookOnRemoveHook func(t WebhookTarget)

// WithWebhookOnRemove registers a callback that fires when a target is
// actually removed from the registry. Multiple calls accumulate — all
// hooks fire in registration order. events.Register installs an SDK-
// internal hook here to fire safeOnUnsubscribe on the EventDef's
// OnUnsubscribe field; deployments wanting their own diagnostic
// listener can pass an additional hook via this option.
func WithWebhookOnRemove(h WebhookOnRemoveHook) WebhookOption {
	return func(r *WebhookRegistry) {
		if h != nil {
			r.onRemoveHooks = append(r.onRemoveHooks, h)
		}
	}
}

// WithWebhookTTL overrides the registry's subscription TTL within the
// spec envelope [MinWebhookTTL, MaxWebhookTTL]. Out-of-envelope values
// are clamped at registration time and a warning is logged via the
// registry's logger (defaults to log.Printf). Pass <=0 to keep the
// default of DefaultWebhookTTL.
//
// For tests and demos that legitimately need a sub-minimum TTL (driving
// the SDK's auto-refresh path on a fast cadence), also pass
// WithUnsafeWebhookTTLBypass() to disable the clamp. Production
// deployments MUST NOT use the bypass.
func WithWebhookTTL(ttl time.Duration) WebhookOption {
	return func(r *WebhookRegistry) {
		if ttl > 0 {
			r.ttl = ttl
			r.ttlExplicit = true
		}
	}
}

// WithUnsafeWebhookTTLBypass disables the [MinWebhookTTL, MaxWebhookTTL]
// envelope clamp on WithWebhookTTL. After this option the registry
// honors any positive TTL the operator supplied verbatim — including
// sub-minimum values needed by SDK refresh-loop tests and the
// discord/test-ttl walkthrough step.
//
// Production deployments MUST NOT use this option. Out-of-envelope TTLs
// either burn through refresh capacity (too short) or leak soft state
// (too long); the envelope exists for a reason. The constructor logs a
// stark warning when the bypass is active so an accidental production
// use shows up in logs immediately, mirroring the UnsafeAnonymousPrincipal
// posture.
func WithUnsafeWebhookTTLBypass() WebhookOption {
	return func(r *WebhookRegistry) {
		r.ttlClampBypass = true
	}
}

// WithWebhookHeaderMode selects the header / signature wire format used for
// outbound deliveries. Defaults to StandardWebhooks (per upstream WG PR#1
// line 434, comment r3167245184). See WebhookHeaderMode for the available
// modes (StandardWebhooks, MCPHeaders).
func WithWebhookHeaderMode(mode WebhookHeaderMode) WebhookOption {
	return func(r *WebhookRegistry) {
		r.headerMode = mode
	}
}

// WithWebhookSuspendThreshold overrides the count of consecutive
// delivery failures (within the suspend window) that trips a target
// into Active=false. Default 5. Pass <=0 to keep the default.
//
// Per spec §"Webhook Event Delivery" L413 + §"Webhook Delivery Status"
// L460: "after repeated failures the server SHOULD set active: false."
// The spec doesn't fix a number; this option lets deployments tune
// hysteresis.
func WithWebhookSuspendThreshold(n int) WebhookOption {
	return func(r *WebhookRegistry) {
		if n > 0 {
			r.suspendThreshold = n
		}
	}
}

// WithWebhookSuspendWindow overrides the sliding window over which
// consecutive failures count toward suspension. Default 10 minutes.
// Pass <=0 to keep the default.
//
// Failures separated by more than W don't accumulate — a receiver
// with one failure per hour for weeks shouldn't get suspended; only
// a current run of failures does.
func WithWebhookSuspendWindow(d time.Duration) WebhookOption {
	return func(r *WebhookRegistry) {
		if d > 0 {
			r.suspendWindow = d
		}
	}
}

// WithWebhookMaxBodyBytes overrides the outbound delivery body cap.
// Default is 256 KiB per spec §"Webhook Security" → "Delivery profile"
// L487. Pass <=0 to keep the default.
//
// Cap mode is REJECT, not TRUNCATE — truncation would corrupt the
// HMAC signature and silently drop event content. Events whose
// serialized envelope exceeds the cap are logged and skipped (will
// never get smaller on retry).
func WithWebhookMaxBodyBytes(n int) WebhookOption {
	return func(r *WebhookRegistry) {
		if n > 0 {
			r.maxBodyBytes = n
		}
	}
}

// WithWebhookAllowPrivateNetworks permits outbound webhook deliveries to
// loopback / private / link-local IP ranges. The default is FALSE (strict);
// turn this ON for demo and test setups that deliver to local httptest
// servers, NEVER in production.
//
// Per spec §"Webhook Security" → "SSRF prevention" L464, deployments MUST
// guard against DNS rebinding by checking the resolved IP at dial time.
// The net.Dialer.Control callback installed by NewWebhookRegistry rejects:
//   - 127.0.0.0/8, ::1 (loopback)
//   - 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16 (RFC1918 private IPv4)
//   - 169.254.0.0/16, fe80::/10 (link-local — includes AWS metadata service)
//   - fc00::/7 (IPv6 ULA)
//   - 0.0.0.0, :: (unspecified)
//   - 224.0.0.0/4, ff00::/8 (multicast)
//   - 255.255.255.255 (broadcast)
//   - IPv4-mapped IPv6 forms of any of the above
//
// When this option is enabled (allow=true), the guard is bypassed —
// all of those ranges become dialable. The discord/telegram demos enable
// this so `make demo` works against local httptest servers.
func WithWebhookAllowPrivateNetworks(allow bool) WebhookOption {
	return func(r *WebhookRegistry) {
		r.allowPrivateNetworks = allow
	}
}

// WebhookTarget is a registered outbound webhook callback with TTL-based expiry.
//
// The CanonicalKey is the spec's identity tuple bytes
// (§"Subscription Identity" → "Key composition" L363) and serves as the
// registry's primary key. The ID is the spec's derived routing handle
// (§"Subscription Identity" → "Derived id" L367), surfaced on the wire
// as the X-MCP-Subscription-Id header on every delivery POST per
// §"Webhook Event Delivery" L390.
//
// EventName, Principal, Params: copies of the identity components
// stored separately so OnRemove hooks (and Match / Transform on
// fanout) can construct a HookContext + params payload per spec
// §"Server SDK Guidance" L623-629 without re-parsing canonical
// bytes. Redundant with CanonicalKey; cheap to store, and the
// registry already owns the only writer.
type WebhookTarget struct {
	CanonicalKey  []byte    // canonical bytes of (principal, url, name, params)
	ID            string    // server-derived routing handle (sub_<base64-of-16-bytes>)
	URL           string    // delivery callback URL
	Secret        string    // client-supplied HMAC signing secret (whsec_...)
	ExpiresAt     time.Time // soft-state TTL expiry
	MaxAgeSeconds int       // per-spec replay floor (§"Cursor Lifecycle" L529); 0 = no floor

	EventName string         // event-type name (used by Match / Transform / OnRemove lookup)
	Principal string         // resolved subscription principal (used by HookContext)
	Params    map[string]any // canonicalized subscription params (passed to hooks)

	// Subject is the raw OAuth `sub` claim — same value the
	// IntrospectionValidator / JWTValidator stamped on
	// core.Claims.Subject at subscribe time. Stored alongside Principal
	// so BCL fan-out (issue 709) can match a revoked session by `sub`
	// when the AS only carried that side of the (sub, sid) tuple.
	// Empty for UnsafeAnonymousPrincipal subscriptions.
	Subject string

	// SessionID is the OIDC `sid` claim — same value the validators
	// stamped on core.Claims.SessionID at subscribe time. Stored on
	// the target so BCL fan-out can drop exactly the subscriptions
	// tied to a revoked session in O(N) over the webhook store
	// without consulting the AS again. Empty when the token carried
	// no sid (legacy issuers / non-OIDC tokens / anonymous demo
	// escape).
	SessionID string

	// Status is per-target delivery health, surfaced on subscribe refresh
	// response per spec §"Webhook Delivery Status" L425-460.
	// Mutated only via *WebhookRegistry methods under r.mu so the
	// snapshot returned by Targets()/DeliveryStatus() is consistent.
	Status DeliveryStatus

	// FailureCount is the internal counter for the suspend state machine.
	// Tracks consecutive failures within the current sliding-window run.
	// Not surfaced on the wire; the wire-visible signal is Status.Active.
	// Exported so external WebhookStore implementations can round-trip
	// the value across a restart — an in-process registry never reads
	// it across the package boundary.
	FailureCount int
}

// DeliveryStatus is the per-target delivery-health summary surfaced on
// the events/subscribe refresh response per spec §"Webhook Delivery
// Status" L425-460. Subscribers use it to decide whether to back off,
// re-create the subscription with a fresh secret, or alert.
//
// LastError is a CATEGORICAL string from a fixed set; the spec
// explicitly forbids raw response bodies / headers / status lines
// because the subscribe response is visible to the subscriber and
// arbitrary receiver responses must not become a data oracle. Empty
// when no failure has happened on the current run.
//
// LastDeliveryAt is set to the most recent successful delivery time;
// nil when never successfully delivered. FailedSince is set on the
// first failure of a current failure run; nil when the last attempt
// succeeded. Both timestamps serialize to ISO-8601 (RFC3339).
type DeliveryStatus struct {
	Active         bool
	LastDeliveryAt *time.Time
	LastError      DeliveryErrorBucket
	FailedSince    *time.Time
}

// DeliveryErrorBucket is the spec's categorical lastError set per
// L460. Values that don't appear in this list MUST NOT leak into the
// subscribe response.
type DeliveryErrorBucket string

const (
	DeliveryErrorNone              DeliveryErrorBucket = ""
	DeliveryErrorConnectionRefused DeliveryErrorBucket = "connection_refused"
	DeliveryErrorTimeout           DeliveryErrorBucket = "timeout"
	DeliveryErrorTLS               DeliveryErrorBucket = "tls_error"
	DeliveryError3xxRedirect       DeliveryErrorBucket = "http_3xx_redirect"
	DeliveryError4xx               DeliveryErrorBucket = "http_4xx"
	DeliveryError5xx               DeliveryErrorBucket = "http_5xx"
	DeliveryErrorChallengeFailed   DeliveryErrorBucket = "challenge_failed"
)

// WebhookRegistry tracks outbound webhook subscriptions and delivers events
// with HMAC-SHA256 signed payloads. Subscriptions have TTL-based soft state
// per the spec — they expire if the client stops refreshing.
//
// The HMAC signing secret is always client-supplied per the spec; the
// registry stores it as-is on Register and uses it directly when signing
// outbound deliveries.
//
// The registry is keyed on the spec's canonical-tuple bytes
// (§"Subscription Identity" L363) — two subscribes with the same
// canonical key (same principal, url, name, params) refer to the same
// subscription. Cross-tenant isolation is by construction since
// principal is part of the key.
type WebhookRegistry struct {
	mu                   sync.RWMutex
	store                WebhookStore // canonicalKey → WebhookTarget; default in-memory
	client               *http.Client
	ttl                  time.Duration
	ttlExplicit          bool // operator passed WithWebhookTTL (drives clamp decision)
	ttlClampBypass       bool // WithUnsafeWebhookTTLBypass — disables clamp
	headerMode           WebhookHeaderMode
	allowPrivateNetworks bool          // when false (default), Dialer.Control rejects private/loopback IPs (spec §"Webhook Security" → "SSRF prevention" L464)
	maxBodyBytes         int           // outbound POST body cap (spec §"Webhook Security" → "Delivery profile" L487); default 256 KiB
	suspendThreshold     int           // consecutive failures → Active=false (spec §"Webhook Delivery Status" L460); default 5
	suspendWindow        time.Duration // sliding window over which failures accumulate; default 10min

	// onRemoveHooks fire when a target is actually removed from the
	// registry (Unregister, TTL prune, PostTerminated). The SDK uses
	// these to drive on_unsubscribe and quota release per spec
	// §"Server SDK Guidance" → "Subscription lifecycle hooks" L691.
	// Mutated only at construction (WithWebhookOnRemove) and via
	// AddOnRemoveHook; reads under mu.RLock. Hooks fire OUTSIDE the
	// lock to keep a slow listener from serializing the registry.
	onRemoveHooks []WebhookOnRemoveHook

	// defResolver lets Deliver look up an event-type's Match /
	// Transform hooks (spec §"Server SDK Guidance" L623-629) at
	// fanout time without WebhookRegistry needing to know about
	// EventDef directly. events.Register installs this; callers that
	// hand-deliver events (e.g., TypedSource authors reaching for
	// EmitToWebhooks themselves) get a no-op resolver and the
	// per-target match/transform path is a passthrough.
	// Set under mu, read under mu.RLock.
	defResolver func(eventName string) EventDef

	// logf is the logging hook used by deliver paths. Defaults to log.Printf;
	// tests override via setLogfForTest to capture failures (including SSRF
	// dial-time rejections) for assertions.
	logf func(format string, args ...any)
}

// SetDefResolver installs the resolver Deliver uses to look up an
// event-type's Match / Transform hooks at fanout time. events.Register
// is the only caller — it points the resolver at the source map so the
// registry stays decoupled from EventDef. Safe to call after the
// registry has been handed out; the field is mu-guarded.
// applyTTLClamp enforces the [MinWebhookTTL, MaxWebhookTTL] envelope on
// the TTL the operator configured via WithWebhookTTL. Called from
// NewWebhookRegistry after all options have applied so the bypass +
// explicit-TTL flags are known. Logs at most one clamp warning per
// constructor invocation; the bypass logs its own stark warning so an
// accidental production bypass is loud.
func (r *WebhookRegistry) applyTTLClamp() {
	if r.ttlClampBypass {
		r.logf("[events] WARNING: WithUnsafeWebhookTTLBypass set — webhook TTL clamp DISABLED. "+
			"Production deployments MUST NOT use this option; the [%s, %s] envelope exists for a reason.",
			MinWebhookTTL, MaxWebhookTTL)
		return
	}
	if !r.ttlExplicit {
		// Default TTL (DefaultWebhookTTL = 1h) is in-envelope by
		// construction; no clamp + no warning.
		return
	}
	switch {
	case r.ttl < MinWebhookTTL:
		r.logf("[events] WARNING: WithWebhookTTL=%s is below the spec envelope floor (%s); clamping up. "+
			"Use WithUnsafeWebhookTTLBypass for tests / demos that need a sub-minimum TTL.",
			r.ttl, MinWebhookTTL)
		r.ttl = MinWebhookTTL
	case r.ttl > MaxWebhookTTL:
		r.logf("[events] WARNING: WithWebhookTTL=%s is above the spec envelope ceiling (%s); clamping down.",
			r.ttl, MaxWebhookTTL)
		r.ttl = MaxWebhookTTL
	}
}

func (r *WebhookRegistry) SetDefResolver(fn func(eventName string) EventDef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defResolver = fn
}

// AddOnRemoveHook registers a callback that fires when a target is
// actually removed from the registry. Equivalent to WithWebhookOnRemove
// but callable after construction — used by events.Register to install
// the SDK's safeOnUnsubscribe wiring on a registry the user
// constructed.
func (r *WebhookRegistry) AddOnRemoveHook(h WebhookOnRemoveHook) {
	if h == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onRemoveHooks = append(r.onRemoveHooks, h)
}

// fireOnRemove invokes every registered onRemove hook with the given
// target. MUST be called outside r.mu so a slow listener doesn't
// serialize the registry. Each hook runs with panic recovery — a
// buggy listener can't take down the caller (Unregister / Register's
// prune path / PostTerminated).
func (r *WebhookRegistry) fireOnRemove(t WebhookTarget) {
	r.mu.RLock()
	hooks := append([]WebhookOnRemoveHook(nil), r.onRemoveHooks...)
	r.mu.RUnlock()
	for _, h := range hooks {
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					r.logf("[webhook] OnRemove hook panic for target %s: %v", t.ID, rec)
				}
			}()
			h(t)
		}()
	}
}

// NewWebhookRegistry creates an empty registry with the documented defaults:
// 5-second HTTP timeout, DefaultWebhookTTL (1h) TTL inside the
// [MinWebhookTTL, MaxWebhookTTL] envelope, StandardWebhooks signing,
// SSRF-strict outbound dialing (loopback / private / link-local rejected).
// Override via the With* options.
//
// When WithWebhookTTL is passed with an out-of-envelope value, the
// constructor clamps it to the nearest envelope edge and logs a warning.
// WithUnsafeWebhookTTLBypass disables the clamp for tests / demos —
// see its docs for when (not) to use it.
func NewWebhookRegistry(opts ...WebhookOption) *WebhookRegistry {
	r := &WebhookRegistry{
		store:            NewInMemoryWebhookStore(),
		ttl:              DefaultWebhookTTL,
		headerMode:       StandardWebhooks,
		maxBodyBytes:     defaultWebhookMaxBodyBytes,
		suspendThreshold: defaultWebhookSuspendThreshold,
		suspendWindow:    defaultWebhookSuspendWindow,
		logf:             log.Printf,
	}
	for _, o := range opts {
		o(r)
	}
	r.applyTTLClamp()
	// http.Client wired AFTER options apply so the Dialer.Control callback
	// can read the resolved allowPrivateNetworks setting. Spec
	// §"Webhook Security" → "SSRF prevention" L464.
	//
	// CheckRedirect: explicitly disable the default 10-redirect follow.
	// A receiver returning 3xx to an internal address would otherwise
	// bypass the dial-time SSRF guard via Go's redirect chain. Per
	// spec same paragraph L464.
	r.client = &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: r.dialContext(),
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return r
}

// dialContext returns the net.Dialer.DialContext used by the outbound
// http.Client. The Dialer's Control callback inspects the resolved
// IP:port AFTER DNS resolution but BEFORE the connect syscall, rejecting
// any address that falls in the SSRF blocklist (unless
// allowPrivateNetworks is set). Per spec §"Webhook Security" → "SSRF
// prevention" L464.
//
// Inspecting at Control rather than resolving manually avoids a TOCTOU
// where the resolver and the dialer could see different addresses (DNS
// rebinding); the address passed to Control is the exact one the
// connect syscall will use.
func (r *WebhookRegistry) dialContext() func(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			if r.allowPrivateNetworks {
				return nil
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("SSRF guard: invalid dial address %q: %w", address, err)
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return fmt.Errorf("SSRF guard: unresolved dial address %q", address)
			}
			if reason := isBlockedIP(ip); reason != "" {
				return fmt.Errorf("SSRF guard: blocked dial to %s (%s)", ip, reason)
			}
			return nil
		},
	}
	return dialer.DialContext
}

// isBlockedIP returns a non-empty reason string when ip falls in any of
// the SSRF-blocked ranges. Returns "" for public addresses. Handles
// IPv4-mapped IPv6 by re-checking the unmapped form.
//
// Ranges blocked (must match the WithWebhookAllowPrivateNetworks doc):
//   - loopback (127.0.0.0/8, ::1)
//   - RFC1918 private IPv4 (10/8, 172.16/12, 192.168/16)
//   - link-local (169.254.0.0/16, fe80::/10)
//   - IPv6 ULA (fc00::/7)
//   - unspecified (0.0.0.0, ::)
//   - multicast (224.0.0.0/4, ff00::/8)
//   - broadcast (255.255.255.255)
func isBlockedIP(ip net.IP) string {
	// Normalize IPv4-mapped-IPv6 to IPv4 form so "::ffff:127.0.0.1" is
	// classified the same as "127.0.0.1".
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	switch {
	case ip.IsLoopback():
		return "loopback"
	case ip.IsLinkLocalUnicast():
		return "link-local"
	case ip.IsLinkLocalMulticast(), ip.IsInterfaceLocalMulticast(), ip.IsMulticast():
		return "multicast"
	case ip.IsUnspecified():
		return "unspecified"
	case ip.IsPrivate():
		// Covers RFC1918 (10/8, 172.16/12, 192.168/16) AND IPv6 ULA (fc00::/7).
		return "private"
	}
	// Broadcast — net.IP.IsGlobalUnicast() returns false for it but we
	// want a clear message.
	if ip.Equal(net.IPv4bcast) {
		return "broadcast"
	}
	return ""
}

// dialContextForTest exposes the dialer for unit tests that exercise
// per-CIDR rejection without spinning up a server in every range.
func (r *WebhookRegistry) dialContextForTest() func(ctx context.Context, network, addr string) (net.Conn, error) {
	return r.dialContext()
}

// setLogfForTest swaps the registry's log hook so tests can capture
// delivery failures (including SSRF dial-time rejections) for assertions.
func (r *WebhookRegistry) setLogfForTest(f func(format string, args ...any)) {
	r.logf = f
}

// RegisterParams bundles the inputs for Register. A struct rather than
// a positional list because EventName / Principal / Params took the
// arg count past the readability ceiling.
type RegisterParams struct {
	CanonicalKey  []byte
	DerivedID     string
	URL           string
	Secret        string
	MaxAgeSeconds int

	// EventName / Principal / Params: copies of the identity
	// components the registry stores so OnRemove hooks have full
	// HookContext + payload available (per spec §"Server SDK
	// Guidance" → "Subscription lifecycle hooks" L691) without
	// re-parsing canonical bytes. Fed by the same canonical-tuple
	// inputs the caller already used to compute CanonicalKey.
	EventName string
	Principal string
	Params    map[string]any

	// Subject / SessionID — copies of claims.Subject / claims.SessionID
	// stamped on the target so BCL fan-out can match a revoked session
	// against this subscription without re-introspecting. See
	// WebhookTarget for the longer rationale (issue 709).
	Subject   string
	SessionID string
}

// Register adds or refreshes a webhook subscription keyed on the spec's
// canonical tuple (§"Subscription Identity" → "Key composition" L363).
// Two calls with the same CanonicalKey refer to the same subscription
// — second call refreshes TTL + replaces secret.
//
// Returns (expiresAt, isNew). isNew is true on first registration
// (caller fires safeOnSubscribe per spec §"Server SDK Guidance" L691)
// and false on refresh — refresh ≠ subscribe, so on_subscribe MUST
// NOT re-fire. Caller resolves the distinction; the registry just
// reports it.
//
// DerivedID is the X-MCP-Subscription-Id value the registry emits on
// every delivery POST; it MUST be deriveSubscriptionID(CanonicalKey)
// from identity.go. Passed in (rather than computed here) so the
// caller can derive once and reuse for both Register and the
// subscribe-response body.
//
// MaxAgeSeconds is the per-subscription replay floor per spec
// §"Cursor Lifecycle" → "Bounding replay with maxAge" L529. Stored on
// the target for use on (future) reconnect-with-replay; 0 means no
// floor. On refresh, an explicit non-zero value replaces the prior
// stored floor; 0 leaves the existing value untouched (treats omission
// as "don't change").
//
// Side effect: prunes expired targets before registering. Each pruned
// target fires the onRemove hooks — TTL expiry counts as an
// unsubscribe (spec §"Server SDK Guidance" → "Unsubscribe timing by
// mode" L707).
func (r *WebhookRegistry) Register(p RegisterParams) (expiresAt time.Time, isNew bool) {
	r.mu.Lock()
	pruned := r.pruneExpiredLocked()
	expiresAt = time.Now().Add(r.ttl)
	getResp, _ := r.store.GetWebhook(context.Background(), GetWebhookRequest{CanonicalKey: p.CanonicalKey})
	if getResp.Found {
		existing := getResp.Target
		// Refresh: update expiry and secret if provided. Secret rotation
		// per spec is allowed by supplying a new value on refresh.
		existing.ExpiresAt = expiresAt
		if p.Secret != "" {
			existing.Secret = p.Secret
		}
		if p.MaxAgeSeconds > 0 {
			existing.MaxAgeSeconds = p.MaxAgeSeconds
		}
		// A successful refresh reactivates a suspended target per spec
		// §"Webhook Delivery Status" L460. Clear the failure run so
		// deliveries can resume. Pending events do NOT replay
		// automatically (would re-flood a recovering receiver); the
		// client signals replay intent via the next events/poll or by
		// waiting for live events.
		if !existing.Status.Active {
			existing.Status.Active = true
			existing.Status.LastError = DeliveryErrorNone
			existing.Status.FailedSince = nil
			existing.FailureCount = 0
		}
		_, _ = r.store.SaveWebhook(context.Background(), SaveWebhookRequest{Target: existing})
		isNew = false
	} else {
		_, _ = r.store.SaveWebhook(context.Background(), SaveWebhookRequest{Target: WebhookTarget{
			CanonicalKey:  p.CanonicalKey,
			ID:            p.DerivedID,
			URL:           p.URL,
			Secret:        p.Secret,
			ExpiresAt:     expiresAt,
			MaxAgeSeconds: p.MaxAgeSeconds,
			EventName:     p.EventName,
			Principal:     p.Principal,
			Subject:       p.Subject,
			SessionID:     p.SessionID,
			Params:        p.Params,
			// Active defaults to true on first registration. The
			// suspend state machine flips this to false after
			// repeated failures (spec §"Webhook Delivery Status"
			// L460); a successful refresh resets it.
			Status: DeliveryStatus{Active: true},
		}})
		isNew = true
	}
	r.mu.Unlock()

	// Fire onRemove for any pruned targets OUTSIDE the lock — a slow
	// listener (e.g., one that releases an upstream resource via
	// blocking I/O inside on_unsubscribe) shouldn't serialize the
	// registry's hot path.
	for _, t := range pruned {
		r.fireOnRemove(t)
	}
	return expiresAt, isNew
}

// Unregister removes a webhook subscription by canonical-tuple key.
// No-op if no entry matches. Per spec §"Unsubscribing: events/unsubscribe"
// L509, the derived id is not accepted as input — callers resolve via
// the same canonical tuple they would for a subscribe.
//
// Fires onRemove hooks for the deleted target — explicit Unregister
// gets the same on_unsubscribe lifecycle (spec §"Server SDK
// Guidance" → "Unsubscribe timing by mode" L707) as TTL prune and
// PostTerminated.
func (r *WebhookRegistry) Unregister(canonicalKey []byte) {
	r.mu.Lock()
	resp, _ := r.store.DeleteWebhook(context.Background(), DeleteWebhookRequest{CanonicalKey: canonicalKey})
	r.mu.Unlock()
	if resp.Found {
		r.fireOnRemove(resp.Removed)
	}
}

// Targets returns a snapshot of all non-expired AND non-suspended
// webhook targets. Used by Deliver to fan out an event; suspended
// targets (Active=false after N consecutive failures, per spec
// §"Webhook Delivery Status" L460) are excluded so dead receivers
// don't keep getting retry traffic.
//
// Lookup-by-canonical-key paths (PostGap, PostTerminated) bypass this
// filter — control envelopes for terminated/gap should still POST to
// suspended targets if anything (last-gasp signals).
func (r *WebhookRegistry) Targets() []WebhookTarget {
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := time.Now()
	listResp, _ := r.store.ListWebhooks(context.Background(), ListWebhooksRequest{})
	out := make([]WebhookTarget, 0, len(listResp.Targets))
	for _, t := range listResp.Targets {
		if t.ExpiresAt.After(now) && t.Status.Active {
			out = append(out, t)
		}
	}
	return out
}

// lookupTarget returns the stored target for canonicalKey, or
// (zero, false) when absent. Package-private helper used by tests
// inside experimental/ext/events that need to inspect a specific
// target without going through the public Targets() snapshot. NOT
// part of the public surface.
func (r *WebhookRegistry) lookupTarget(canonicalKey []byte) (WebhookTarget, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	resp, _ := r.store.GetWebhook(context.Background(), GetWebhookRequest{CanonicalKey: canonicalKey})
	return resp.Target, resp.Found
}

// pruneExpiredLocked removes expired subscriptions and returns the
// removed targets so the caller can fire onRemove hooks OUTSIDE the
// lock — TTL prune is an unsubscribe per spec §"Server SDK Guidance"
// → "Unsubscribe timing by mode" L707. Must hold r.mu write lock.
func (r *WebhookRegistry) pruneExpiredLocked() []WebhookTarget {
	now := time.Now()
	ctx := context.Background()
	// ListWebhooks snapshot + per-key DeleteWebhook avoids mutating
	// the store during iteration — the in-memory impl tolerates Go map
	// delete-during-range, but a Postgres / SQL impl wouldn't. The
	// seam contract is the same for both backends; #630 doesn't have
	// to special-case this path.
	listResp, _ := r.store.ListWebhooks(ctx, ListWebhooksRequest{})
	var removed []WebhookTarget
	for _, t := range listResp.Targets {
		if t.ExpiresAt.After(now) || t.ExpiresAt.Equal(now) {
			continue
		}
		delResp, _ := r.store.DeleteWebhook(ctx, DeleteWebhookRequest{CanonicalKey: t.CanonicalKey})
		if delResp.Found {
			log.Printf("[webhook] subscription %s expired, removing", delResp.Removed.ID)
			removed = append(removed, delResp.Removed)
		}
	}
	return removed
}

// DeliverToTarget POSTs an event to a single target identified by
// canonical key, bypassing the broadcast fanout (and thus skipping
// Match / Transform). Used by EmitToSubscription for targeted delivery
// per spec §"Server SDK Guidance" L630 — the "the author has already
// shaped this event for this specific subscription" model means
// hooks are deliberately not applied here.
//
// Returns false when there is no live target for the canonical key
// (unregistered between the index lookup and this call, suspended,
// or expired). Caller treats this as a normal drop — racing
// targeted-emit against teardown is expected, not an error.
//
// ctx threads through to r.deliver so the outbound HTTP POST carries
// the W3C `traceparent` header — SEP-414 P6 propagation across the
// cross-process events bus boundary (issue 683).
func (r *WebhookRegistry) DeliverToTarget(ctx context.Context, canonicalKey []byte, event Event) bool {
	r.mu.RLock()
	resp, _ := r.store.GetWebhook(context.Background(), GetWebhookRequest{CanonicalKey: canonicalKey})
	r.mu.RUnlock()
	if !resp.Found {
		return false
	}
	target := resp.Target
	if !target.Status.Active || time.Now().After(target.ExpiresAt) {
		return false
	}

	body, err := json.Marshal(event)
	if err != nil {
		r.logf("[webhook] DeliverToTarget: marshal failed for %s: %v", target.ID, err)
		return false
	}
	if len(body) > r.maxBodyBytes {
		r.logf("[webhook] DeliverToTarget: event %s body %d bytes exceeds cap %d for %s; dropping",
			event.EventID, len(body), r.maxBodyBytes, target.ID)
		return false
	}
	tc := traceContextForDelivery(ctx, event)
	go r.deliver(target, event.EventID, body, tc)
	return true
}

// ExpireAll forcibly expires all subscriptions (test helper).
func (r *WebhookRegistry) ExpireAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	past := time.Now().Add(-1 * time.Second)
	ctx := context.Background()
	listResp, _ := r.store.ListWebhooks(ctx, ListWebhooksRequest{})
	for _, t := range listResp.Targets {
		t.ExpiresAt = past
		_, _ = r.store.SaveWebhook(ctx, SaveWebhookRequest{Target: t})
	}
}

// Deliver sends an event to webhook subscribers of that event name.
//
// Per-target processing (spec §"Server SDK Guidance" L623-629):
//  1. Filter by event name — only targets whose EventName matches the
//     event are considered.
//  2. safeMatch with the EventDef's Match — skip target if false.
//  3. safeTransform with the EventDef's Transform — use the returned
//     event's body when the bool says modified, otherwise reuse the
//     original marshal.
//
// Each POST carries an HMAC-SHA256 signature; failures retry with
// exponential backoff. When transform modifies the event, the per-
// target body is re-marshaled and re-signed (the receiver's HMAC
// check would fail otherwise) and re-checked against the body cap.
//
// Match / Transform are looked up via SetDefResolver. Callers that
// haven't installed a resolver (TypedSource authors hand-emitting)
// get a passthrough — Match=nil delivers to all matching-name targets,
// Transform=nil reuses the original body.
func (r *WebhookRegistry) Deliver(ctx context.Context, event Event) {
	targets := r.Targets()
	if len(targets) == 0 {
		return
	}

	// One marshal upfront for the unmodified path; per-target
	// re-marshal only when transform actually changes the event
	// (passthrough short-circuit avoids the alloc).
	originalBody, err := json.Marshal(event)
	if err != nil {
		r.logf("[webhook] failed to marshal event: %v", err)
		return
	}
	if len(originalBody) > r.maxBodyBytes {
		// Spec §"Webhook Security" → "Delivery profile" L487 caps
		// outbound delivery bodies at 256 KiB (configurable via
		// WithWebhookMaxBodyBytes). Reject oversized — truncation
		// would corrupt the HMAC signature and silently drop event
		// content. Re-trying won't shrink the body, so this is
		// terminal for the event.
		r.logf("[webhook] event %s body %d bytes exceeds cap %d; dropping (will not retry)",
			event.EventID, len(originalBody), r.maxBodyBytes)
		return
	}

	r.mu.RLock()
	resolver := r.defResolver
	r.mu.RUnlock()
	var def EventDef
	if resolver != nil {
		def = resolver(event.Name)
	}

	for _, t := range targets {
		if t.EventName != "" && t.EventName != event.Name {
			// Targets whose EventName is unset are pre-identity-
			// tracking fixtures — skip the name filter for them so
			// legacy tests keep working. Real targets registered
			// via the subscribe handler always carry the name.
			continue
		}

		hc := newHookContext(context.Background(), t.Principal, t.ID, DeliveryModeWebhook)
		if !safeMatch(def.Match, hc, event, t.Params) {
			continue
		}
		delivered, modified := safeTransform(def.Transform, hc, event, t.Params)

		body := originalBody
		if modified {
			b, err := json.Marshal(delivered)
			if err != nil {
				r.logf("[webhook] transform-modified event %s: marshal failed for target %s: %v",
					event.EventID, t.ID, err)
				continue
			}
			if len(b) > r.maxBodyBytes {
				r.logf("[webhook] transform-modified event %s: body %d bytes exceeds cap %d for target %s; dropping",
					event.EventID, len(b), r.maxBodyBytes, t.ID)
				continue
			}
			body = b
		}
		// Resolve the per-target trace context off the outer ctx /
		// event.Meta (we resolve once per target rather than once
		// outside the loop so each target's delivery span lifetime
		// is independent). See SEP-414 P6 (issue 683).
		tc := traceContextForDelivery(ctx, delivered)
		go r.deliver(t, delivered.EventID, body, tc)
	}
}

const (
	maxRetries     = 3
	initialBackoff = 500 * time.Millisecond
	maxBackoff     = 5 * time.Second
)

// recordDeliverySuccess updates the target's DeliveryStatus after a
// successful delivery attempt. Active stays/becomes true (clears any
// prior suspension per spec §"Webhook Delivery Status" L460);
// LastDeliveryAt advances; LastError + FailedSince clear (the current
// failure run, if any, is over); failure counter resets to 0.
func (r *WebhookRegistry) recordDeliverySuccess(canonicalKey []byte, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx := context.Background()
	getResp, _ := r.store.GetWebhook(ctx, GetWebhookRequest{CanonicalKey: canonicalKey})
	if !getResp.Found {
		return
	}
	t := getResp.Target
	atCopy := at
	t.Status.Active = true
	t.Status.LastDeliveryAt = &atCopy
	t.Status.LastError = DeliveryErrorNone
	t.Status.FailedSince = nil
	t.FailureCount = 0
	_, _ = r.store.SaveWebhook(ctx, SaveWebhookRequest{Target: t})
}

// recordDeliveryFailure updates the target's DeliveryStatus after the
// FINAL failed attempt (all retries exhausted). LastError gets the
// categorical bucket; FailedSince is set on the FIRST failure of a
// CURRENT run (sliding-window resets via suspendWindow) and preserved
// across subsequent failures so subscribers can see how long the
// receiver has been unreachable.
//
// Suspend rule (spec §"Webhook Delivery Status" L460): if FailedSince
// is older than suspendWindow, reset the run (this failure starts a
// new run). Otherwise, count consecutive failures within the run; on
// hitting suspendThreshold, flip Active=false.
func (r *WebhookRegistry) recordDeliveryFailure(canonicalKey []byte, bucket DeliveryErrorBucket) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx := context.Background()
	getResp, _ := r.store.GetWebhook(ctx, GetWebhookRequest{CanonicalKey: canonicalKey})
	if !getResp.Found {
		return
	}
	t := getResp.Target
	now := time.Now()
	t.Status.LastError = bucket

	// Sliding-window failure counting. If the current run is older
	// than the window, reset — this failure starts a fresh run.
	// FailureCount is per-target; tracked alongside the wire-visible
	// DeliveryStatus fields.
	if t.Status.FailedSince == nil || now.Sub(*t.Status.FailedSince) > r.suspendWindow {
		startCopy := now
		t.Status.FailedSince = &startCopy
		t.FailureCount = 1
	} else {
		t.FailureCount++
	}
	wasActive := t.Status.Active
	if t.FailureCount >= r.suspendThreshold {
		t.Status.Active = false
	}
	_, _ = r.store.SaveWebhook(ctx, SaveWebhookRequest{Target: t})
	// On the Active true→false transition, auto-Post a
	// {type:terminated} envelope to the receiver as a courtesy
	// notification (spec §"Non-event webhook bodies" L420). Uses
	// postTerminatedSilent so the target stays in the registry
	// (Active=false observable; reactivate via refresh per
	// §"Webhook Delivery Status" L460). Snapshot the target struct
	// before releasing the lock — the async deliverControl needs
	// URL/Secret/ID outside the lock.
	if wasActive && !t.Status.Active {
		r.postTerminatedSilent(t, ControlError{
			Code:    -32603,
			Message: "subscription suspended after repeated delivery failures: " + string(bucket),
		})
	}
}

// DeliveryStatus returns a snapshot of the target's delivery health.
// Returns the zero value when no target matches canonicalKey.
func (r *WebhookRegistry) DeliveryStatus(canonicalKey []byte) DeliveryStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	resp, _ := r.store.GetWebhook(context.Background(), GetWebhookRequest{CanonicalKey: canonicalKey})
	if resp.Found {
		return resp.Target.Status
	}
	return DeliveryStatus{}
}

// classifyTransportError maps a Go net/http transport-layer error
// (returned from http.Client.Do when no HTTP response was received) to
// the categorical bucket spec L460 mandates. NEVER inspects err.Error()
// substrings beyond the standard library's own type-asserted markers
// (net.Error, *url.Error wrappers) — keeps the bucket boundaries
// stable across Go versions and avoids accidentally surfacing receiver-
// chosen content into the subscribe response.
func classifyTransportError(err error) DeliveryErrorBucket {
	if err == nil {
		return DeliveryErrorNone
	}
	// net.Error.Timeout() is the type-safe way to detect deadline
	// exceeded / i/o timeout without parsing strings.
	if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
		return DeliveryErrorTimeout
	}
	// Substring match for the OS-level errno text Go wraps. These
	// strings are stable across Go versions per the standard library
	// docs but kept as a fallback — the type-safe net.OpError + syscall
	// inspection would be more robust if we ever needed to distinguish
	// further.
	s := err.Error()
	switch {
	case strings.Contains(s, "connection refused"):
		return DeliveryErrorConnectionRefused
	case strings.Contains(s, "tls:") || strings.Contains(s, "x509:") || strings.Contains(s, "certificate"):
		return DeliveryErrorTLS
	}
	// Default: unclassified network failure. Surface as connection_refused
	// since it's the most common transport failure and the alternatives
	// (timeout, tls_error) are already handled above.
	return DeliveryErrorConnectionRefused
}

// deliver attempts to POST the event with exponential backoff on failure.
// eventID is the event's identifier; used as webhook-id (stable across
// retries so the receiver's dedup works). Spec: SHOULD retry with
// exponential backoff.
//
// tc is the W3C trace context resolved at Deliver-time; when non-zero
// the `traceparent` (and `tracestate` if present) HTTP headers are
// stamped on every retry attempt so the receiver can stitch in.
// SEP-414 P6 (issue 683).
func (r *WebhookRegistry) deliver(target WebhookTarget, eventID string, body []byte, tc core.TraceContext) {
	backoff := initialBackoff
	// Tracks the last per-attempt failure bucket. Recorded onto
	// deliveryStatus only after all retries are exhausted (i.e., this
	// is the FINAL outcome). A transient blip during a successful
	// retry-cycle would otherwise falsely appear as "current failure"
	// on the next subscribe refresh.
	lastErrBucket := DeliveryErrorNone

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			r.logf("[webhook] retry %d/%d for %s (backoff %v)", attempt, maxRetries, target.URL, backoff)
			time.Sleep(backoff)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}

		// webhook-id == eventId for event deliveries: stable across retries
		// (so receiver dedup works) and consistent across delivery paths
		// (webhook emit + poll backfill of the same upstream event collapse
		// under the receiver's eventId-keyed dedup).
		//
		// X-MCP-Subscription-Id carries target.ID (the spec's derived
		// id over the canonical tuple, §"Subscription Identity" →
		// "Derived id" L367) so the receiver can select the correct
		// secret without parsing the body. Per spec §"Webhook Event
		// Delivery" L390 + §"Webhook Security" L472: this is the only
		// MCP-specific header on a Standard Webhooks delivery.
		signed := signFor(r.headerMode, eventID, body, target.Secret, time.Now()).
			withSubscriptionID(target.ID)

		req, err := http.NewRequest("POST", target.URL, bytes.NewReader(signed.body))
		if err != nil {
			r.logf("[webhook] failed to create request for %s: %v", target.URL, err)
			return // not retryable
		}
		signed.applyHeaders(req)
		if !tc.IsZero() {
			req.Header.Set(traceparentHTTPHeader, tc.Traceparent)
			if tc.Tracestate != "" {
				req.Header.Set(tracestateHTTPHeader, tc.Tracestate)
			}
		}

		resp, err := r.client.Do(req)
		if err != nil {
			r.logf("[webhook] delivery to %s failed: %v", target.URL, err)
			// Don't record the per-attempt failure on the target yet —
			// only the FINAL outcome (after all retries exhausted) gets
			// stored on deliveryStatus. Otherwise transient blips
			// during a successful retry would falsely show up as
			// "current failure" on the next subscribe refresh.
			lastErrBucket = classifyTransportError(err)
			continue // retry
		}
		resp.Body.Close()

		if resp.StatusCode < 300 {
			r.recordDeliverySuccess(target.CanonicalKey, time.Now())
			return // success
		}

		r.logf("[webhook] delivery to %s returned %d", target.URL, resp.StatusCode)
		switch {
		case resp.StatusCode >= 300 && resp.StatusCode < 400:
			// 3xx is non-retryable. We disabled redirect-following
			// via CheckRedirect; a receiver returning 3xx is signalling
			// "go elsewhere" but we're not allowed to (spec §"Webhook
			// Security" → "SSRF prevention" L464). Re-trying won't
			// change the response; treat as terminal.
			r.recordDeliveryFailure(target.CanonicalKey, DeliveryError3xxRedirect)
			return
		case resp.StatusCode == http.StatusRequestEntityTooLarge:
			// Spec §"Webhook Security" → "Delivery profile" L487:
			// 413 MUST be non-retryable. Receiver rejects our
			// payload size; retrying won't change that.
			r.recordDeliveryFailure(target.CanonicalKey, DeliveryError4xx)
			return
		case resp.StatusCode >= 400 && resp.StatusCode < 500:
			r.recordDeliveryFailure(target.CanonicalKey, DeliveryError4xx)
			return // 4xx = client error, not retryable
		}
		// 5xx = server error, retry. Bucket so the final-outcome record
		// (below the retry loop) knows which bucket to record.
		lastErrBucket = DeliveryError5xx
	}
	r.logf("[webhook] delivery to %s failed after %d retries, giving up", target.URL, maxRetries)
	r.recordDeliveryFailure(target.CanonicalKey, lastErrBucket)
}

// ValidateWebhookURL is a fail-fast subscribe-time check on a webhook
// callback URL. Rejects non-http(s) schemes and obvious loopback hostnames
// unless the registry has WithWebhookAllowPrivateNetworks(true).
//
// This is the SUBSCRIBE-time check, not the load-bearing one. The
// authoritative SSRF guard is the dial-time check in dialContext, which
// is TOCTOU-safe under DNS rebinding (per spec §"Webhook Security" →
// "SSRF prevention" L464). ValidateWebhookURL is a UX aid: catches
// obvious mistakes at subscribe so the client gets -32015
// CallbackEndpointError immediately rather than a delayed delivery failure.
func (r *WebhookRegistry) ValidateWebhookURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported scheme %q (must be http or https)", u.Scheme)
	}
	if r.allowPrivateNetworks {
		return nil
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0":
		return fmt.Errorf("loopback hostnames are not permitted; set WithWebhookAllowPrivateNetworks(true) for demos")
	}
	// Hostnames that aren't bare loopback strings get a free pass at
	// subscribe time — DNS resolution is the dial-time guard's job.
	return nil
}

// VerifySignature checks an X-MCP-Signature header. Convenience alias for
// VerifyMCPSignature kept for callers that pre-date the header-mode split.
func VerifySignature(body []byte, secret, timestamp, signature string) bool {
	return VerifyMCPSignature(body, secret, timestamp, signature)
}
