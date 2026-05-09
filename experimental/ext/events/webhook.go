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
)

const (
	defaultWebhookTTL              = 60 * time.Second // 1 minute for POC (production: longer)
	defaultWebhookMaxBodyBytes     = 256 * 1024       // ζ-3 spec L487
	defaultWebhookSuspendThreshold = 5                // ζ-6: N consecutive failures
	defaultWebhookSuspendWindow    = 10 * time.Minute // ζ-6: sliding window W
)

// WebhookOption configures a WebhookRegistry at construction time.
type WebhookOption func(*WebhookRegistry)

// WebhookOnRemoveHook fires whenever a target is actually removed from
// the registry — explicit Unregister, TTL prune, or PostTerminated.
// Does NOT fire on the suspend transition (Active=true→false), which
// keeps the target in the registry as paused (η-3 / Q4: suspend ≠
// unsubscribe; refresh reactivates without re-firing on_subscribe).
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

// WithWebhookTTL overrides the registry's subscription TTL. Useful for tests
// (drive the SDK's TTL refresh behavior in seconds rather than minutes) and
// for deployments that want longer-lived subscriptions. Pass <=0 to keep
// the default of 60s.
func WithWebhookTTL(ttl time.Duration) WebhookOption {
	return func(r *WebhookRegistry) {
		if ttl > 0 {
			r.ttl = ttl
		}
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
// hysteresis. ζ-6.
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
// a current run of failures does. ζ-6.
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
// ζ-1's net.Dialer.Control callback rejects:
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
// EventName, Principal, Params (η-3): copies of the identity components
// stored separately so OnRemove hooks (and η-4 match/transform on
// fanout) can construct a HookContext + params payload without
// re-parsing canonical bytes. Redundant with CanonicalKey; cheap to
// store, and the registry already owns the only writer.
type WebhookTarget struct {
	CanonicalKey  []byte    // canonical bytes of (principal, url, name, params)
	ID            string    // server-derived routing handle (sub_<base64-of-16-bytes>)
	URL           string    // delivery callback URL
	Secret        string    // client-supplied HMAC signing secret (whsec_...)
	ExpiresAt     time.Time // soft-state TTL expiry
	MaxAgeSeconds int       // δ-3: per-spec replay floor (§"Cursor Lifecycle" L529); 0 = no floor

	EventName string         // event-type name (η-3 hook lookup)
	Principal string         // resolved subscription principal (η-3 HookContext)
	Params    map[string]any // canonicalized subscription params (η-3 hook payload)

	// ζ-5: per-target delivery health, surfaced on subscribe refresh
	// response per spec §"Webhook Delivery Status" L425-460.
	// Mutated only via *WebhookRegistry methods under r.mu so the
	// snapshot returned by Targets()/DeliveryStatus() is consistent.
	Status DeliveryStatus

	// ζ-6: internal counter for the suspend state machine. Tracks
	// consecutive failures within the current sliding-window run.
	// Not surfaced on the wire; the wire-visible signal is Status.Active.
	failureCount int
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
	targets              map[string]WebhookTarget // keyed by string(canonicalKey)
	client               *http.Client
	ttl                  time.Duration
	headerMode           WebhookHeaderMode
	allowPrivateNetworks bool          // ζ-1: when false (default), Dialer.Control rejects private/loopback IPs
	maxBodyBytes         int           // ζ-3: outbound POST body cap in bytes; default 256 KiB
	suspendThreshold     int           // ζ-6: consecutive failures → Active=false; default 5
	suspendWindow        time.Duration // ζ-6: sliding window over which failures accumulate; default 10min

	// onRemoveHooks fire when a target is actually removed from the
	// registry (Unregister, TTL prune, PostTerminated). η-3.
	// Mutated only at construction (WithWebhookOnRemove) and via
	// AddOnRemoveHook; reads under mu.RLock. Hooks fire OUTSIDE the
	// lock to keep a slow listener from serializing the registry.
	onRemoveHooks []WebhookOnRemoveHook

	// defResolver lets Deliver look up an event-type's Match /
	// Transform hooks at fanout time without WebhookRegistry needing
	// to know about EventDef directly. events.Register installs this;
	// callers that hand-deliver events (e.g., TypedSource authors
	// reaching for EmitToWebhooks themselves) get a no-op resolver
	// and the per-target match/transform path is a passthrough.
	// η-4. Set under mu, read under mu.RLock.
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
// 5-second HTTP timeout, 60-second TTL, StandardWebhooks signing,
// SSRF-strict outbound dialing (loopback / private / link-local rejected).
// Override via the With* options.
func NewWebhookRegistry(opts ...WebhookOption) *WebhookRegistry {
	r := &WebhookRegistry{
		targets:          make(map[string]WebhookTarget),
		ttl:              defaultWebhookTTL,
		headerMode:       StandardWebhooks,
		maxBodyBytes:     defaultWebhookMaxBodyBytes,
		suspendThreshold: defaultWebhookSuspendThreshold,
		suspendWindow:    defaultWebhookSuspendWindow,
		logf:             log.Printf,
	}
	for _, o := range opts {
		o(r)
	}
	// http.Client wired AFTER options apply so the Dialer.Control callback
	// can read the resolved allowPrivateNetworks setting. ζ-1 spec
	// §"Webhook Security" → "SSRF prevention" L464.
	//
	// CheckRedirect (ζ-2): explicitly disable the default 10-redirect
	// follow. A receiver returning 3xx to an internal address would
	// otherwise bypass the dial-time SSRF guard via Go's redirect chain.
	// Per spec same paragraph L464.
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


// RegisterParams bundles the inputs for Register. Promoted to a struct
// over a long positional list because η-3 added EventName / Principal
// / Params to the identity-side state — six positional args was
// already at the readability ceiling.
type RegisterParams struct {
	CanonicalKey  []byte
	DerivedID     string
	URL           string
	Secret        string
	MaxAgeSeconds int

	// EventName / Principal / Params (η-3): copies of the identity
	// components the registry stores so OnRemove hooks have full
	// HookContext + payload available without re-parsing canonical
	// bytes. Fed by the same canonical-tuple inputs the caller
	// already used to compute CanonicalKey.
	EventName string
	Principal string
	Params    map[string]any
}

// Register adds or refreshes a webhook subscription keyed on the spec's
// canonical tuple (§"Subscription Identity" → "Key composition" L363).
// Two calls with the same CanonicalKey refer to the same subscription
// — second call refreshes TTL + replaces secret.
//
// Returns (expiresAt, isNew). isNew is true on first registration
// (caller fires safeOnSubscribe) and false on refresh (η-3 / Q4:
// refresh ≠ subscribe). Caller resolves the distinction; the registry
// just reports it.
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
// target fires the onRemove hooks (η-3: lifecycle parity — TTL expiry
// is an unsubscribe).
func (r *WebhookRegistry) Register(p RegisterParams) (expiresAt time.Time, isNew bool) {
	r.mu.Lock()
	pruned := r.pruneExpiredLocked()
	expiresAt = time.Now().Add(r.ttl)
	key := string(p.CanonicalKey)
	if existing, ok := r.targets[key]; ok {
		// Refresh: update expiry and secret if provided. Secret rotation
		// per spec is allowed by supplying a new value on refresh.
		existing.ExpiresAt = expiresAt
		if p.Secret != "" {
			existing.Secret = p.Secret
		}
		if p.MaxAgeSeconds > 0 {
			existing.MaxAgeSeconds = p.MaxAgeSeconds
		}
		// ζ-6: a successful refresh reactivates a suspended target per
		// spec L460. Clear the failure run so deliveries can resume.
		// Pending events do NOT replay automatically (would re-flood a
		// recovering receiver); the client signals replay intent via
		// the next events/poll or by waiting for live events.
		if !existing.Status.Active {
			existing.Status.Active = true
			existing.Status.LastError = DeliveryErrorNone
			existing.Status.FailedSince = nil
			existing.failureCount = 0
		}
		r.targets[key] = existing
		isNew = false
	} else {
		r.targets[key] = WebhookTarget{
			CanonicalKey:  p.CanonicalKey,
			ID:            p.DerivedID,
			URL:           p.URL,
			Secret:        p.Secret,
			ExpiresAt:     expiresAt,
			MaxAgeSeconds: p.MaxAgeSeconds,
			EventName:     p.EventName,
			Principal:     p.Principal,
			Params:        p.Params,
			// ζ-5: Active defaults to true on first registration. The
			// suspend state machine in ζ-6 flips this to false after
			// repeated failures; a successful refresh resets it.
			Status: DeliveryStatus{Active: true},
		}
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
// Fires onRemove hooks for the deleted target — η-3's lifecycle
// parity. Callers that explicitly Unregister get the same on_unsubscribe
// behavior as TTL prune and PostTerminated.
func (r *WebhookRegistry) Unregister(canonicalKey []byte) {
	r.mu.Lock()
	target, ok := r.targets[string(canonicalKey)]
	if ok {
		delete(r.targets, string(canonicalKey))
	}
	r.mu.Unlock()
	if ok {
		r.fireOnRemove(target)
	}
}

// Targets returns a snapshot of all non-expired AND non-suspended
// webhook targets. Used by Deliver to fan out an event; suspended
// targets (ζ-6: Active=false after N consecutive failures) are
// excluded so dead receivers don't keep getting retry traffic.
//
// Lookup-by-canonical-key paths (PostGap, PostTerminated) bypass this
// filter — control envelopes for terminated/gap should still POST to
// suspended targets if anything (last-gasp signals).
func (r *WebhookRegistry) Targets() []WebhookTarget {
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := time.Now()
	out := make([]WebhookTarget, 0, len(r.targets))
	for _, t := range r.targets {
		if t.ExpiresAt.After(now) && t.Status.Active {
			out = append(out, t)
		}
	}
	return out
}

// pruneExpiredLocked removes expired subscriptions and returns the
// removed targets so the caller can fire onRemove hooks OUTSIDE the
// lock (η-3: lifecycle parity — TTL prune is an unsubscribe). Must
// hold r.mu write lock.
func (r *WebhookRegistry) pruneExpiredLocked() []WebhookTarget {
	now := time.Now()
	var removed []WebhookTarget
	for key, t := range r.targets {
		if t.ExpiresAt.Before(now) {
			log.Printf("[webhook] subscription %s expired, removing", t.ID)
			delete(r.targets, key)
			removed = append(removed, t)
		}
	}
	return removed
}

// ExpireAll forcibly expires all subscriptions (test helper).
func (r *WebhookRegistry) ExpireAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	past := time.Now().Add(-1 * time.Second)
	for k, v := range r.targets {
		v.ExpiresAt = past
		r.targets[k] = v
	}
}

// Deliver sends an event to webhook subscribers of that event name.
//
// Per-target processing (η-4):
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
func (r *WebhookRegistry) Deliver(event Event) {
	targets := r.Targets()
	if len(targets) == 0 {
		return
	}

	// One marshal upfront for the unmodified path; per-target
	// re-marshal only when transform actually changes the event
	// (Q8 short-circuit).
	originalBody, err := json.Marshal(event)
	if err != nil {
		r.logf("[webhook] failed to marshal event: %v", err)
		return
	}
	if len(originalBody) > r.maxBodyBytes {
		// ζ-3: spec L487 caps outbound delivery bodies at 256 KiB
		// (configurable via WithWebhookMaxBodyBytes). Reject
		// oversized — truncation would corrupt the HMAC signature
		// and silently drop event content. Re-trying won't shrink
		// the body, so this is terminal for the event.
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
			// Pre-η-3 targets had no EventName recorded — skip the
			// filter for those (zero string) so legacy fixtures
			// keep working. Real targets registered via the
			// subscribe handler always carry the name.
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
		go r.deliver(t, delivered.EventID, body)
	}
}

const (
	maxRetries     = 3
	initialBackoff = 500 * time.Millisecond
	maxBackoff     = 5 * time.Second
)

// recordDeliverySuccess updates the target's DeliveryStatus after a
// successful delivery attempt. Active stays/becomes true (clears any
// prior suspension — ζ-6); LastDeliveryAt advances; LastError +
// FailedSince clear (the current failure run, if any, is over);
// failure counter resets to 0.
func (r *WebhookRegistry) recordDeliverySuccess(canonicalKey []byte, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.targets[string(canonicalKey)]
	if !ok {
		return
	}
	atCopy := at
	t.Status.Active = true
	t.Status.LastDeliveryAt = &atCopy
	t.Status.LastError = DeliveryErrorNone
	t.Status.FailedSince = nil
	t.failureCount = 0
	r.targets[string(canonicalKey)] = t
}

// recordDeliveryFailure updates the target's DeliveryStatus after the
// FINAL failed attempt (all retries exhausted). LastError gets the
// categorical bucket; FailedSince is set on the FIRST failure of a
// CURRENT run (sliding-window resets see ζ-6 suspendWindow) and
// preserved across subsequent failures so subscribers can see how long
// the receiver has been unreachable.
//
// Suspend rule (ζ-6): if FailedSince is older than suspendWindow,
// reset the run (this failure starts a new run). Otherwise, count
// consecutive failures within the run; on hitting suspendThreshold,
// flip Active=false.
func (r *WebhookRegistry) recordDeliveryFailure(canonicalKey []byte, bucket DeliveryErrorBucket) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.targets[string(canonicalKey)]
	if !ok {
		return
	}
	now := time.Now()
	t.Status.LastError = bucket

	// ζ-6: sliding-window failure counting. If the current run is
	// older than the window, reset — this failure starts a fresh run.
	// failureCount is per-target; tracked alongside the wire-visible
	// DeliveryStatus fields.
	if t.Status.FailedSince == nil || now.Sub(*t.Status.FailedSince) > r.suspendWindow {
		startCopy := now
		t.Status.FailedSince = &startCopy
		t.failureCount = 1
	} else {
		t.failureCount++
	}
	wasActive := t.Status.Active
	if t.failureCount >= r.suspendThreshold {
		t.Status.Active = false
	}
	r.targets[string(canonicalKey)] = t
	// ζ-7.3: on the Active true→false transition, auto-Post a
	// {type:terminated} envelope to the receiver as a courtesy
	// notification. Uses postTerminatedSilent so the target stays
	// in the registry (Active=false observable; reactivate via refresh).
	// Snapshot the target struct before releasing the lock — the
	// async deliverControl needs URL/Secret/ID outside the lock.
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
	if t, ok := r.targets[string(canonicalKey)]; ok {
		return t.Status
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
func (r *WebhookRegistry) deliver(target WebhookTarget, eventID string, body []byte) {
	backoff := initialBackoff
	// ζ-5: tracks the last per-attempt failure bucket. Recorded onto
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
		// X-MCP-Subscription-Id carries target.ID (γ-2's derived id over
		// the canonical tuple) so the receiver can select the correct
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
			// ζ-2: 3xx is non-retryable. We disabled redirect-following
			// via CheckRedirect; a receiver returning 3xx is signalling
			// "go elsewhere" but we're not allowed to. Re-trying won't
			// change the response; treat as terminal.
			r.recordDeliveryFailure(target.CanonicalKey, DeliveryError3xxRedirect)
			return
		case resp.StatusCode == http.StatusRequestEntityTooLarge:
			// ζ-3 spec L487: 413 MUST be non-retryable. Receiver
			// rejects our payload size; retrying won't change that.
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
// obvious mistakes at subscribe so the client gets -32015 InvalidCallbackUrl
// immediately rather than a delayed delivery failure.
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
