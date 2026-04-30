package events

import (
	"bytes"
	"crypto/subtle"
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

// WithWebhookSecretMode selects how the registry decides on the
// per-subscription HMAC secret. See WebhookSecretMode (Server / Client /
// Identity). Defaults to WebhookSecretServer.
func WithWebhookSecretMode(mode WebhookSecretMode) WebhookOption {
	return func(r *WebhookRegistry) {
		r.secretMode = mode
	}
}

// WithWebhookRoot supplies the master secret used by Identity mode to derive
// per-subscription secrets via HMAC(root, canonicalTuple). Required when
// using WebhookSecretIdentity; ignored otherwise (kept for forward use).
// The bytes are not copied — pass a slice the caller doesn't mutate.
func WithWebhookRoot(root []byte) WebhookOption {
	return func(r *WebhookRegistry) {
		r.root = root
	}
}

// WebhookTarget is a registered outbound webhook callback with TTL-based expiry.
type WebhookTarget struct {
	ID        string // client-provided subscription ID
	URL       string
	Secret    string
	ExpiresAt time.Time
}

// webhookKey returns the composite key for a webhook subscription per spec:
// (delivery.url, id) for unauthenticated servers.
func webhookKey(urlStr, id string) string {
	return urlStr + "\x00" + id
}

// WebhookRegistry tracks outbound webhook subscriptions and delivers events
// with HMAC-SHA256 signed payloads. Subscriptions have TTL-based soft state
// per Peter's spec — they expire if the client stops refreshing.
type WebhookRegistry struct {
	mu         sync.RWMutex
	targets    map[string]WebhookTarget // keyed by webhookKey(url, id)
	client     *http.Client
	ttl        time.Duration
	headerMode WebhookHeaderMode
	secretMode WebhookSecretMode
	root       []byte
}

// NewWebhookRegistry creates an empty registry with the documented defaults:
// 5-second HTTP timeout, 60-second TTL, StandardWebhooks signing,
// WebhookSecretServer (server always generates secrets). Override via the
// With* options.
//
// Identity mode requires WithWebhookRoot. If the caller selects identity
// mode without providing a root, NewWebhookRegistry panics — surfacing the
// config bug at process start rather than at first subscribe.
func NewWebhookRegistry(opts ...WebhookOption) *WebhookRegistry {
	r := &WebhookRegistry{
		targets:    make(map[string]WebhookTarget),
		client:     &http.Client{Timeout: 5 * time.Second},
		ttl:        defaultWebhookTTL,
		headerMode: StandardWebhooks,
		secretMode: WebhookSecretServer,
	}
	for _, o := range opts {
		o(r)
	}
	if r.secretMode == WebhookSecretIdentity && len(r.root) == 0 {
		panic("events: WebhookSecretIdentity requires WithWebhookRoot to be set")
	}
	return r
}

// resolveSecret applies the registry's secret mode to a subscribe request.
// Returns the secret to store and use for signing, plus the id to record
// against the subscription (which may differ from the client-supplied id
// when identity mode derives its own).
//
// Inputs:
//   clientID, clientSecret — what the client supplied (may be empty)
//   name, url, params      — the canonical tuple inputs for identity mode
//
// Behaviour by mode:
//   Server   — always generate a fresh secret; ignore clientSecret. id is
//              passed through (clientID).
//   Client   — honour clientSecret; if empty, fall back to generating one.
//              id is clientID.
//   Identity — derive both secret and id from (name, url, params) via the
//              configured root. Ignore clientSecret AND clientID.
func (r *WebhookRegistry) resolveSecret(clientID, clientSecret, name, url string, params map[string]string) (id, secret string) {
	switch r.secretMode {
	case WebhookSecretIdentity:
		return deriveIdentityID(name, url, params), deriveIdentitySecret(r.root, name, url, params)
	case WebhookSecretClient:
		if clientSecret == "" {
			return clientID, generateSecret()
		}
		return clientID, clientSecret
	default: // WebhookSecretServer
		return clientID, generateSecret()
	}
}

// SecretMode returns the registry's configured secret mode (read-only).
// Subscribe handlers consult this when shaping their response.
func (r *WebhookRegistry) SecretMode() WebhookSecretMode { return r.secretMode }

// Register adds or refreshes a webhook subscription. Returns the expiry time.
func (r *WebhookRegistry) Register(id, urlStr, secret string) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneExpiredLocked()
	expiresAt := time.Now().Add(r.ttl)
	key := webhookKey(urlStr, id)
	if existing, ok := r.targets[key]; ok {
		// Refresh: update expiry and secret if provided
		existing.ExpiresAt = expiresAt
		if secret != "" {
			existing.Secret = secret
		}
		r.targets[key] = existing
	} else {
		r.targets[key] = WebhookTarget{ID: id, URL: urlStr, Secret: secret, ExpiresAt: expiresAt}
	}
	return expiresAt
}

// Unregister removes a webhook subscription by (url, id).
func (r *WebhookRegistry) Unregister(urlStr, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.targets, webhookKey(urlStr, id))
}

// UnregisterBySecret removes a webhook subscription identified by
// (url, secret). Used as the proof-of-possession unsubscribe path —
// clients that hold the secret can drop the subscription without knowing
// or remembering the server-assigned id. No-op if no match found.
//
// Constant-time secret comparison guards against timing-leak side channels.
func (r *WebhookRegistry) UnregisterBySecret(urlStr, secret string) {
	if secret == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	want := []byte(secret)
	for key, t := range r.targets {
		if t.URL == urlStr && subtle.ConstantTimeCompare([]byte(t.Secret), want) == 1 {
			delete(r.targets, key)
		}
	}
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
		go r.deliver(t, body)
	}
}

const (
	maxRetries     = 3
	initialBackoff = 500 * time.Millisecond
	maxBackoff     = 5 * time.Second
)

// deliver attempts to POST the event with exponential backoff on failure.
// Spec: SHOULD retry with exponential backoff.
func (r *WebhookRegistry) deliver(target WebhookTarget, body []byte) {
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

		signed := signFor(r.headerMode, body, target.Secret, time.Now())

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
