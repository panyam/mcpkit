package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// WebhookTarget is a registered outbound webhook callback.
type WebhookTarget struct {
	URL    string
	Secret string
}

// WebhookRegistry tracks outbound webhook subscriptions and delivers events
// with HMAC-SHA256 signed payloads.
type WebhookRegistry struct {
	mu      sync.RWMutex
	targets map[string]WebhookTarget // keyed by URL
	client  *http.Client
}

// NewWebhookRegistry creates an empty registry with a 5-second HTTP timeout.
func NewWebhookRegistry() *WebhookRegistry {
	return &WebhookRegistry{
		targets: make(map[string]WebhookTarget),
		client:  &http.Client{Timeout: 5 * time.Second},
	}
}

// Register adds or updates a webhook target.
func (r *WebhookRegistry) Register(url, secret string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.targets[url] = WebhookTarget{URL: url, Secret: secret}
}

// Unregister removes a webhook target.
func (r *WebhookRegistry) Unregister(url string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.targets, url)
}

// Targets returns a snapshot of all registered webhook targets.
func (r *WebhookRegistry) Targets() []WebhookTarget {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]WebhookTarget, 0, len(r.targets))
	for _, t := range r.targets {
		out = append(out, t)
	}
	return out
}

// Deliver sends an event to all registered webhooks. Each POST includes an
// HMAC-SHA256 signature in the X-Signature-256 header. Delivery failures are
// logged but do not remove the target (appropriate for a POC).
func (r *WebhookRegistry) Deliver(event TelegramEvent) {
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

func (r *WebhookRegistry) deliver(target WebhookTarget, body []byte) {
	ts := fmt.Sprintf("%d", time.Now().Unix())
	sig := sign(body, ts, target.Secret)

	req, err := http.NewRequest("POST", target.URL, bytes.NewReader(body))
	if err != nil {
		log.Printf("[webhook] failed to create request for %s: %v", target.URL, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-MCP-Signature", sig)
	req.Header.Set("X-MCP-Timestamp", ts)

	resp, err := r.client.Do(req)
	if err != nil {
		log.Printf("[webhook] delivery to %s failed: %v", target.URL, err)
		return
	}
	resp.Body.Close()

	if resp.StatusCode >= 300 {
		log.Printf("[webhook] delivery to %s returned %d", target.URL, resp.StatusCode)
	}
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
