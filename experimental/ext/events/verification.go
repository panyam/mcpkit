package events

// Endpoint verification, spec §"Webhook Security" → "Endpoint
// verification (anti-flooding)". HMAC stops a third party being
// deceived by forged deliveries; it does not stop one being flooded by
// real ones, since anyone can subscribe a victim's URL with a secret of
// their own. So the server confirms the endpoint wants deliveries before
// activating a subscription, by one of:
//
//	(a) a challenge handshake (POST a nonce, expect it echoed in a 2xx body)
//	(b) a server-configured allowlist            WithWebhookDeliveryAllowlist
//	(c) prior out-of-band verification           WithPreVerifier / WithPreVerifiedDeliveryURL
//	(d) a receiver-published well-known document  WithWellKnownReceiverDocs
//
// The result is cached per (principal, url). Issue 490.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// maxVerificationResponseBytes caps how much of an endpoint's reply to a
// verification POST the server reads. The echo is a few dozen bytes; the
// cap keeps an attacker-chosen URL from streaming an unbounded body into
// the subscribe handler.
const maxVerificationResponseBytes = 4 << 10

// PreVerifier reports whether a delivery URL was already verified for a
// principal through some mechanism outside the protocol, such as a
// dashboard where the receiver registered and confirmed the URL (spec
// path (c)). A true result skips the challenge POST. Implementations are
// called on the subscribe path, so a slow lookup delays subscribe; ctx
// carries the subscribe request's deadline.
//
// Return false for anything not positively verified. An error in the
// lookup should read as false, which falls through to the handshake.
type PreVerifier interface {
	IsPreVerified(ctx context.Context, principal, deliveryURL string) bool
}

// PreVerifierFunc adapts a plain function to PreVerifier.
type PreVerifierFunc func(ctx context.Context, principal, deliveryURL string) bool

// IsPreVerified calls f.
func (f PreVerifierFunc) IsPreVerified(ctx context.Context, principal, deliveryURL string) bool {
	return f(ctx, principal, deliveryURL)
}

// WithUnsafeSkipEndpointVerification turns endpoint verification off,
// restoring the pre-490 behavior of delivering to any callback URL that
// passes validation. This deliberately violates the spec's MUST: any
// authenticated caller can then point deliveries at a third party.
//
// Exists for tests and for receivers that predate the handshake. Prefer
// WithWebhookDeliveryAllowlist or WithPreVerifier, which waive the
// handshake for known URLs without opening it for all of them.
func WithUnsafeSkipEndpointVerification() WebhookOption {
	return func(r *WebhookRegistry) {
		r.skipEndpointVerification = true
	}
}

// WithWebhookDeliveryAllowlist waives the challenge handshake for
// delivery URLs matching any of patterns (spec path (b)). Each pattern
// is an absolute URL: a delivery URL matches when its scheme and host
// (including port) are equal to the pattern's and its path starts with
// the pattern's path at a segment boundary. So "https://hooks.example.com/mcp"
// covers "https://hooks.example.com/mcp/tenant-1" and not
// "https://hooks.example.com/mcpx". A pattern with an empty path covers
// the whole origin.
//
// The allowlist applies to every principal. It does not bypass the SSRF
// guard: allowlisted URLs are still dialed through the same checked path.
// Unparseable patterns are logged and dropped, which fails safe (the URL
// falls back to the handshake). Repeated calls append.
func WithWebhookDeliveryAllowlist(patterns []string) WebhookOption {
	return func(r *WebhookRegistry) {
		for _, p := range patterns {
			u, err := url.Parse(p)
			if err != nil || u.Scheme == "" || u.Host == "" {
				log.Printf("[webhook] WithWebhookDeliveryAllowlist: ignoring %q: not an absolute URL", p)
				continue
			}
			r.deliveryAllowlist = append(r.deliveryAllowlist, u)
		}
	}
}

// WithPreVerifier installs a PreVerifier consulted before the challenge
// handshake (spec path (c)). Repeated calls add verifiers; any one
// returning true is enough.
func WithPreVerifier(pv PreVerifier) WebhookOption {
	return func(r *WebhookRegistry) {
		if pv != nil {
			r.preVerifiers = append(r.preVerifiers, pv)
		}
	}
}

// WithPreVerifiedDeliveryURL marks one (principal, deliveryURL) pair as
// verified out of band. The match is exact on both. For a dynamic or
// multi-tenant set, implement PreVerifier instead.
func WithPreVerifiedDeliveryURL(principal, deliveryURL string) WebhookOption {
	return WithPreVerifier(PreVerifierFunc(func(_ context.Context, p, u string) bool {
		return p == principal && u == deliveryURL
	}))
}

// verificationCache is the per-(principal, url) soft state the spec asks
// for. An entry lives for ttl after its last use, so a principal that
// keeps refreshing keeps its verification.
type verificationCache struct {
	mu      sync.Mutex
	entries map[string]time.Time // key → expiry
}

func verificationKey(principal, deliveryURL string) string {
	return principal + "\x00" + deliveryURL
}

func (c *verificationCache) hit(key string, now time.Time, ttl time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	exp, ok := c.entries[key]
	if !ok {
		return false
	}
	if now.After(exp) {
		delete(c.entries, key)
		return false
	}
	c.entries[key] = now.Add(ttl)
	return true
}

func (c *verificationCache) put(key string, now time.Time, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]time.Time{}
	}
	c.entries[key] = now.Add(ttl)
}

func (c *verificationCache) sweep(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, exp := range c.entries {
		if now.After(exp) {
			delete(c.entries, k)
		}
	}
}

// verifyEndpointParams carries what the handshake needs to head and sign
// its POST like a real delivery.
type verifyEndpointParams struct {
	Principal    string
	URL          string
	Secret       string
	DerivedID    string
	CanonicalKey []byte
}

// verifyEndpoint confirms the endpoint at p.URL wants deliveries for
// p.Principal, in the order: skip option, cache, stored verified target,
// allowlist, pre-verifiers, challenge handshake. It returns the
// categorical failure bucket, DeliveryErrorNone on success. The bucket
// is the only thing that may reach the subscriber; nothing from the
// endpoint's response is returned.
func (r *WebhookRegistry) verifyEndpoint(ctx context.Context, p verifyEndpointParams) DeliveryErrorBucket {
	if r.skipEndpointVerification {
		return DeliveryErrorNone
	}
	now := time.Now()
	key := verificationKey(p.Principal, p.URL)
	if r.verified.hit(key, now, r.ttl) {
		return DeliveryErrorNone
	}
	// A subscription restored from a persistent store after a restart
	// carries its verification with it; refreshing it must not re-run
	// the handshake the spec says there is no later occasion for.
	if got, _ := r.store.GetWebhook(ctx, GetWebhookRequest{CanonicalKey: p.CanonicalKey}); got.Found && got.Target.VerifiedAt != nil {
		r.verified.put(key, now, r.ttl)
		return DeliveryErrorNone
	}
	if r.allowlisted(p.URL) || r.preVerified(ctx, p.Principal, p.URL) {
		r.verified.put(key, now, r.ttl)
		return DeliveryErrorNone
	}
	if r.wellKnown != nil && r.wellKnownCovers(ctx, p.URL, now) {
		r.verified.put(key, now, r.ttl)
		return DeliveryErrorNone
	}
	if u, err := url.Parse(p.URL); err == nil && !r.verifyLimit.allow(strings.ToLower(u.Hostname()), now) {
		return verificationThrottled
	}
	if bucket := r.challenge(ctx, p); bucket != DeliveryErrorNone {
		return bucket
	}
	r.verified.put(key, now, r.ttl)
	return DeliveryErrorNone
}

func (r *WebhookRegistry) allowlisted(deliveryURL string) bool {
	if len(r.deliveryAllowlist) == 0 {
		return false
	}
	u, err := url.Parse(deliveryURL)
	if err != nil {
		return false
	}
	for _, pat := range r.deliveryAllowlist {
		if !strings.EqualFold(u.Scheme, pat.Scheme) || !strings.EqualFold(u.Host, pat.Host) {
			continue
		}
		prefix := strings.TrimSuffix(pat.Path, "/")
		if prefix == "" || u.Path == prefix || strings.HasPrefix(u.Path, prefix+"/") {
			return true
		}
	}
	return false
}

func (r *WebhookRegistry) preVerified(ctx context.Context, principal, deliveryURL string) bool {
	for _, pv := range r.preVerifiers {
		if pv.IsPreVerified(ctx, principal, deliveryURL) {
			return true
		}
	}
	return false
}

// challenge runs the handshake: POST {type:verification, challenge:nonce}
// through the registry's SSRF-guarded, redirect-refusing client, then
// require a 2xx whose body echoes the nonce. The nonce lives only for
// this one exchange, so a replayed echo from an earlier handshake fails.
func (r *WebhookRegistry) challenge(ctx context.Context, p verifyEndpointParams) DeliveryErrorBucket {
	nonce, err := newChallengeNonce()
	if err != nil {
		r.logf("[webhook] verification: nonce generation failed: %v", err)
		return DeliveryErrorChallengeFailed
	}
	body, err := json.Marshal(controlEnvelope{Type: "verification", Challenge: nonce})
	if err != nil {
		return DeliveryErrorChallengeFailed
	}
	signed := signFor(r.headerMode, newControlMessageID("verification"), body, p.Secret, time.Now()).
		withSubscriptionID(p.DerivedID)
	req, err := http.NewRequestWithContext(ctx, "POST", p.URL, bytes.NewReader(signed.body))
	if err != nil {
		return DeliveryErrorConnectionRefused
	}
	signed.applyHeaders(req)

	resp, err := r.client.Do(req)
	if err != nil {
		r.logf("[webhook] verification POST to %s failed: %v", p.URL, err)
		return classifyTransportError(err)
	}
	defer resp.Body.Close()
	reply, _ := io.ReadAll(io.LimitReader(resp.Body, maxVerificationResponseBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		r.logf("[webhook] verification POST to %s returned %d", p.URL, resp.StatusCode)
		return DeliveryErrorChallengeFailed
	}
	var echo struct {
		Challenge string `json:"challenge"`
	}
	if json.Unmarshal(reply, &echo) != nil ||
		subtle.ConstantTimeCompare([]byte(echo.Challenge), []byte(nonce)) != 1 {
		r.logf("[webhook] verification POST to %s: challenge not echoed", p.URL)
		return DeliveryErrorChallengeFailed
	}
	return DeliveryErrorNone
}

func newChallengeNonce() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

// AnswerVerificationChallenge is the receiver half of the handshake. If
// body is a {type:"verification"} envelope it writes the 2xx echo the
// server expects and returns true; otherwise it writes nothing and
// returns false, leaving the response to the caller.
//
// Verify the request's signature before calling this. Echoing a
// challenge is consent to deliveries, and a receiver that echoes one it
// cannot verify consents on behalf of a subscription it did not create.
func AnswerVerificationChallenge(w http.ResponseWriter, body []byte) bool {
	var env controlEnvelope
	if json.Unmarshal(body, &env) != nil || env.Type != "verification" {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"challenge": env.Challenge})
	return true
}

// DefaultVerificationsPerHost and DefaultVerificationWindow are the default
// budget for challenge POSTs to one destination host: 60 a minute, allowing a
// burst of 60. Generous on purpose, since a gateway fronting many tenants
// verifies once per (principal, url) behind a single host.
const (
	DefaultVerificationsPerHost = 60
	DefaultVerificationWindow   = time.Minute
)

// WithVerificationRateLimit caps challenge POSTs to any one destination
// hostname at max per window, as a token bucket that refills continuously and
// holds at most max (spec §"Endpoint verification": the verification POST
// "SHOULD be rate-limited per destination host"). Only real challenges count:
// cache hits, allowlist matches and pre-verified URLs are free. A subscribe
// over budget fails with -32013 ResourceExhausted, data.limit
// "verifications_per_host". max <= 0 or window <= 0 disables the limit.
func WithVerificationRateLimit(max int, window time.Duration) WebhookOption {
	return func(r *WebhookRegistry) {
		r.verifyLimit.max = max
		r.verifyLimit.window = window
	}
}

// verificationThrottled is verifyEndpoint's answer for a challenge the
// per-host budget refused. It is not a spec lastError category and never
// reaches the wire as one; the subscribe handler turns it into -32013.
const verificationThrottled DeliveryErrorBucket = "verification_throttled"

// maxTrackedHosts bounds the limiter's memory. Past it, hosts whose buckets
// have refilled completely are forgotten, which loses nothing.
const maxTrackedHosts = 4096

type hostLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hosts  map[string]*hostBucket
}

type hostBucket struct {
	tokens float64
	last   time.Time
}

func (l *hostLimiter) allow(host string, now time.Time) bool {
	if l.max <= 0 || l.window <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hosts == nil {
		l.hosts = map[string]*hostBucket{}
	}
	rate := float64(l.max) / float64(l.window)
	b, ok := l.hosts[host]
	if !ok {
		if len(l.hosts) >= maxTrackedHosts {
			for h, old := range l.hosts {
				if old.tokens+float64(now.Sub(old.last))*rate >= float64(l.max) {
					delete(l.hosts, h)
				}
			}
		}
		b = &hostBucket{tokens: float64(l.max), last: now}
		l.hosts[host] = b
	}
	b.tokens += float64(now.Sub(b.last)) * rate
	if b.tokens > float64(l.max) {
		b.tokens = float64(l.max)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
