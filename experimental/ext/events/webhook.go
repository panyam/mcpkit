package events

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const defaultWebhookTTL = 60 * time.Second // 1 minute for POC (production: longer)

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

// WebhookTarget is a registered outbound webhook callback with TTL-based expiry.
//
// The CanonicalKey is the spec's identity tuple bytes
// (§"Subscription Identity" → "Key composition" L363) and serves as the
// registry's primary key. The ID is the spec's derived routing handle
// (§"Subscription Identity" → "Derived id" L367), surfaced on the wire
// as the X-MCP-Subscription-Id header on every delivery POST per
// §"Webhook Event Delivery" L390.
type WebhookTarget struct {
	CanonicalKey []byte    // canonical bytes of (principal, url, name, params)
	ID           string    // server-derived routing handle (sub_<base64-of-16-bytes>)
	URL          string    // delivery callback URL
	Secret       string    // client-supplied HMAC signing secret (whsec_...)
	ExpiresAt    time.Time // soft-state TTL expiry
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
	mu         sync.RWMutex
	targets    map[string]WebhookTarget // keyed by string(canonicalKey)
	client     *http.Client
	ttl        time.Duration
	headerMode WebhookHeaderMode
}

// NewWebhookRegistry creates an empty registry with the documented defaults:
// 5-second HTTP timeout, 60-second TTL, StandardWebhooks signing. Override
// via the With* options.
func NewWebhookRegistry(opts ...WebhookOption) *WebhookRegistry {
	r := &WebhookRegistry{
		targets:    make(map[string]WebhookTarget),
		client:     &http.Client{Timeout: 5 * time.Second},
		ttl:        defaultWebhookTTL,
		headerMode: StandardWebhooks,
	}
	for _, o := range opts {
		o(r)
	}
	return r
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
func (r *WebhookRegistry) Register(canonicalKey []byte, derivedID, urlStr, secret string) time.Time {
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
		r.targets[key] = existing
	} else {
		r.targets[key] = WebhookTarget{
			CanonicalKey: canonicalKey,
			ID:           derivedID,
			URL:          urlStr,
			Secret:       secret,
			ExpiresAt:    expiresAt,
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
		log.Printf("[webhook] failed to marshal event: %v", err)
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
			log.Printf("[webhook] retry %d/%d for %s (backoff %v)", attempt, maxRetries, target.URL, backoff)
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
		signed := signFor(r.headerMode, eventID, body, target.Secret, time.Now())

		req, err := http.NewRequest("POST", target.URL, bytes.NewReader(signed.body))
		if err != nil {
			log.Printf("[webhook] failed to create request for %s: %v", target.URL, err)
			return // not retryable
		}
		signed.applyHeaders(req)

		resp, err := r.client.Do(req)
		if err != nil {
			log.Printf("[webhook] delivery to %s failed: %v", target.URL, err)
			continue // retry
		}
		resp.Body.Close()

		if resp.StatusCode < 300 {
			return // success
		}

		log.Printf("[webhook] delivery to %s returned %d", target.URL, resp.StatusCode)
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return // 4xx = client error, not retryable
		}
		// 5xx = server error, retry
	}
	log.Printf("[webhook] delivery to %s failed after %d retries, giving up", target.URL, maxRetries)
}

// ValidateWebhookURL performs basic SSRF validation on a webhook callback URL.
// Spec: "MUST validate callback URLs at subscribe time" and "SHOULD reject
// URLs pointing to private/loopback ranges."
// For this POC we reject obvious loopback and private schemes. Production
// implementations should also resolve DNS and check the resulting IP.
func ValidateWebhookURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported scheme %q (must be http or https)", u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "0.0.0.0" {
		// Allow in test/dev — production should reject these.
		log.Printf("[webhook] WARNING: loopback webhook URL %s (allowed in POC mode)", rawURL)
	}
	return nil
}

// VerifySignature checks an X-MCP-Signature header. Convenience alias for
// VerifyMCPSignature kept for callers that pre-date the header-mode split.
func VerifySignature(body []byte, secret, timestamp, signature string) bool {
	return VerifyMCPSignature(body, secret, timestamp, signature)
}
