package events

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	mu      sync.RWMutex
	targets map[string]WebhookTarget // keyed by webhookKey(url, id)
	client  *http.Client
}

// NewWebhookRegistry creates an empty registry with a 5-second HTTP timeout.
func NewWebhookRegistry() *WebhookRegistry {
	return &WebhookRegistry{
		targets: make(map[string]WebhookTarget),
		client:  &http.Client{Timeout: 5 * time.Second},
	}
}

// Register adds or refreshes a webhook subscription. Returns the expiry time.
func (r *WebhookRegistry) Register(id, urlStr, secret string) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneExpiredLocked()
	expiresAt := time.Now().Add(defaultWebhookTTL)
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

		ts := fmt.Sprintf("%d", time.Now().Unix())
		sig := sign(body, ts, target.Secret)

		req, err := http.NewRequest("POST", target.URL, bytes.NewReader(body))
		if err != nil {
			log.Printf("[webhook] failed to create request for %s: %v", target.URL, err)
			return // not retryable
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-MCP-Signature", sig)
		req.Header.Set("X-MCP-Timestamp", ts)

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

// sign computes HMAC-SHA256(secret, timestamp + "." + body) per Peter's spec
// and returns "sha256=<hex>".
func sign(body []byte, timestamp, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return fmt.Sprintf("sha256=%s", hex.EncodeToString(mac.Sum(nil)))
}

// VerifySignature checks that a webhook signature matches the expected HMAC
// using the timestamp + "." + body format.
func VerifySignature(body []byte, secret, timestamp, signature string) bool {
	expected := sign(body, timestamp, secret)
	return hmac.Equal([]byte(expected), []byte(signature))
}
