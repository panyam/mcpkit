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
	defaultWebhookTTL          = 60 * time.Second // 1 minute for POC (production: longer)
	defaultWebhookMaxBodyBytes = 256 * 1024       // ζ-3 spec L487
)

// WebhookOption configures a WebhookRegistry at construction time.
type WebhookOption func(*WebhookRegistry)

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
type WebhookTarget struct {
	CanonicalKey  []byte    // canonical bytes of (principal, url, name, params)
	ID            string    // server-derived routing handle (sub_<base64-of-16-bytes>)
	URL           string    // delivery callback URL
	Secret        string    // client-supplied HMAC signing secret (whsec_...)
	ExpiresAt     time.Time // soft-state TTL expiry
	MaxAgeSeconds int       // δ-3: per-spec replay floor (§"Cursor Lifecycle" L529); 0 = no floor
}

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
	allowPrivateNetworks bool // ζ-1: when false (default), Dialer.Control rejects private/loopback IPs
	maxBodyBytes         int  // ζ-3: outbound POST body cap in bytes; default 256 KiB

	// logf is the logging hook used by deliver paths. Defaults to log.Printf;
	// tests override via setLogfForTest to capture failures (including SSRF
	// dial-time rejections) for assertions.
	logf func(format string, args ...any)
}

// NewWebhookRegistry creates an empty registry with the documented defaults:
// 5-second HTTP timeout, 60-second TTL, StandardWebhooks signing,
// SSRF-strict outbound dialing (loopback / private / link-local rejected).
// Override via the With* options.
func NewWebhookRegistry(opts ...WebhookOption) *WebhookRegistry {
	r := &WebhookRegistry{
		targets:      make(map[string]WebhookTarget),
		ttl:          defaultWebhookTTL,
		headerMode:   StandardWebhooks,
		maxBodyBytes: defaultWebhookMaxBodyBytes,
		logf:         log.Printf,
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


// Register adds or refreshes a webhook subscription keyed on the spec's
// canonical tuple (§"Subscription Identity" → "Key composition" L363).
// Two calls with the same canonicalKey refer to the same subscription
// — second call refreshes TTL + replaces secret. Returns the expiry time.
//
// The derivedID is the X-MCP-Subscription-Id value the registry emits
// on every delivery POST; it MUST be deriveSubscriptionID(canonicalKey)
// from identity.go. Passed in (rather than computed here) so the
// caller can derive once and reuse for both Register and the
// subscribe-response body.
//
// maxAgeSeconds is the per-subscription replay floor per spec
// §"Cursor Lifecycle" → "Bounding replay with maxAge" L529. Stored on
// the target for use on (future) reconnect-with-replay; 0 means no
// floor. On refresh, an explicit non-zero value replaces the prior
// stored floor; 0 leaves the existing value untouched (treats omission
// as "don't change").
func (r *WebhookRegistry) Register(canonicalKey []byte, derivedID, urlStr, secret string, maxAgeSeconds int) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneExpiredLocked()
	expiresAt := time.Now().Add(r.ttl)
	key := string(canonicalKey)
	if existing, ok := r.targets[key]; ok {
		// Refresh: update expiry and secret if provided. Secret rotation
		// per spec is allowed by supplying a new value on refresh.
		existing.ExpiresAt = expiresAt
		if secret != "" {
			existing.Secret = secret
		}
		if maxAgeSeconds > 0 {
			existing.MaxAgeSeconds = maxAgeSeconds
		}
		r.targets[key] = existing
	} else {
		r.targets[key] = WebhookTarget{
			CanonicalKey:  canonicalKey,
			ID:            derivedID,
			URL:           urlStr,
			Secret:        secret,
			ExpiresAt:     expiresAt,
			MaxAgeSeconds: maxAgeSeconds,
		}
	}
	return expiresAt
}

// Unregister removes a webhook subscription by canonical-tuple key.
// No-op if no entry matches. Per spec §"Unsubscribing: events/unsubscribe"
// L509, the derived id is not accepted as input — callers resolve via
// the same canonical tuple they would for a subscribe.
func (r *WebhookRegistry) Unregister(canonicalKey []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.targets, string(canonicalKey))
}

// Targets returns a snapshot of all non-expired webhook targets.
func (r *WebhookRegistry) Targets() []WebhookTarget {
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := time.Now()
	out := make([]WebhookTarget, 0, len(r.targets))
	for _, t := range r.targets {
		if t.ExpiresAt.After(now) {
			out = append(out, t)
		}
	}
	return out
}

// pruneExpiredLocked removes expired subscriptions. Must hold r.mu write lock.
func (r *WebhookRegistry) pruneExpiredLocked() {
	now := time.Now()
	for key, t := range r.targets {
		if t.ExpiresAt.Before(now) {
			log.Printf("[webhook] subscription %s expired, removing", t.ID)
			delete(r.targets, key)
		}
	}
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

// Deliver sends an event to all non-expired webhooks. Each POST includes an
// HMAC-SHA256 signature in X-MCP-Signature and a timestamp in X-MCP-Timestamp.
// Delivery failures are retried with exponential backoff per spec.
func (r *WebhookRegistry) Deliver(event Event) {
	targets := r.Targets()
	if len(targets) == 0 {
		return
	}

	body, err := json.Marshal(event)
	if err != nil {
		r.logf("[webhook] failed to marshal event: %v", err)
		return
	}

	// ζ-3: spec L487 caps outbound delivery bodies at 256 KiB (configurable
	// via WithWebhookMaxBodyBytes). Reject oversized — truncation would
	// corrupt the HMAC signature and silently drop event content.
	// Re-trying won't shrink the body, so this is terminal for the event.
	if len(body) > r.maxBodyBytes {
		r.logf("[webhook] event %s body %d bytes exceeds cap %d; dropping (will not retry)",
			event.EventID, len(body), r.maxBodyBytes)
		return
	}

	for _, t := range targets {
		go r.deliver(t, event.EventID, body)
	}
}

const (
	maxRetries     = 3
	initialBackoff = 500 * time.Millisecond
	maxBackoff     = 5 * time.Second
)

// deliver attempts to POST the event with exponential backoff on failure.
// eventID is the event's identifier; used as webhook-id (stable across
// retries so the receiver's dedup works). Spec: SHOULD retry with
// exponential backoff.
func (r *WebhookRegistry) deliver(target WebhookTarget, eventID string, body []byte) {
	backoff := initialBackoff

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
			continue // retry
		}
		resp.Body.Close()

		if resp.StatusCode < 300 {
			return // success
		}

		r.logf("[webhook] delivery to %s returned %d", target.URL, resp.StatusCode)
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			// ζ-2: 3xx is non-retryable. We disabled redirect-following
			// via CheckRedirect; a receiver returning 3xx is signalling
			// "go elsewhere" but we're not allowed to. Re-trying won't
			// change the response; treat as terminal.
			return
		}
		if resp.StatusCode == http.StatusRequestEntityTooLarge {
			// ζ-3 spec L487: 413 MUST be non-retryable. Receiver
			// rejects our payload size; retrying won't change that.
			return
		}
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return // 4xx = client error, not retryable
		}
		// 5xx = server error, retry
	}
	r.logf("[webhook] delivery to %s failed after %d retries, giving up", target.URL, maxRetries)
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
