// receiver is a standalone webhook target for the whole-enchilada
// demo. It verifies the Standard Webhooks signature scheme that
// mcpkit's events.WebhookRegistry emits by default, logs every
// payload it accepts to stdout, and exposes a /__received endpoint
// returning the captured payloads as JSON for the demokit walkthrough
// + e2e tests to drain.
//
// Intentionally zero mcpkit dependency — this binary demonstrates
// what an arbitrary downstream service looks like as a webhook
// consumer. In production each tenant deploys their own receivers
// in their own infrastructure; the one in this compose graph is
// labeled "example consumer," not infra.
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const standardWebhooksMaxSkewSeconds = 5 * 60

// receivedEvent captures one accepted webhook delivery for /__received
// to surface. The Body is preserved as raw JSON so the demokit
// walkthrough can pretty-print it.
type receivedEvent struct {
	ReceivedAt time.Time       `json:"received_at"`
	WebhookID  string          `json:"webhook_id"`
	Body       json.RawMessage `json:"body"`
}

type receiver struct {
	secret string
	mu     sync.Mutex
	events []receivedEvent
}

func main() {
	addr := flag.String("addr", ":9090", "listen address")
	secret := flag.String("secret", os.Getenv("WEBHOOK_SECRET"),
		"shared secret used to verify Standard Webhooks signatures (whsec_... or raw bytes)")
	flag.Parse()

	if *secret == "" {
		log.Fatal("receiver requires -secret or WEBHOOK_SECRET; refusing to start with unauthenticated webhook endpoint")
	}

	r := &receiver{secret: *secret}

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", r.handleWebhook)
	mux.HandleFunc("/__received", r.handleReceived)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	log.Printf("[receiver] listening on %s", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatalf("receiver failed: %v", err)
	}
}

// handleWebhook receives a Standard Webhooks delivery, verifies the
// signature + timestamp skew, decodes the JSON body, and appends to
// the in-memory event log.
func (r *receiver) handleWebhook(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer req.Body.Close()

	id := req.Header.Get("webhook-id")
	ts := req.Header.Get("webhook-timestamp")
	sig := req.Header.Get("webhook-signature")
	if id == "" || ts == "" || sig == "" {
		http.Error(w, "missing required webhook headers", http.StatusBadRequest)
		return
	}

	tsSec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		http.Error(w, "webhook-timestamp not an integer", http.StatusBadRequest)
		return
	}
	if skew := time.Now().Unix() - tsSec; skew > standardWebhooksMaxSkewSeconds || skew < -standardWebhooksMaxSkewSeconds {
		http.Error(w, "webhook-timestamp outside accepted skew", http.StatusBadRequest)
		return
	}

	if !verifyStandardWebhooksSig(r.secret, id, ts, body, sig) {
		http.Error(w, "signature verification failed", http.StatusUnauthorized)
		return
	}

	r.mu.Lock()
	r.events = append(r.events, receivedEvent{
		ReceivedAt: time.Now().UTC(),
		WebhookID:  id,
		Body:       json.RawMessage(body),
	})
	r.mu.Unlock()
	replica := req.Header.Get("X-Replica")
	if replica == "" {
		replica = "?"
	}
	log.Printf("[receiver] accepted webhook-id=%s replica=%s body=%s", id, replica, string(body))
	w.WriteHeader(http.StatusOK)
}

// handleReceived returns the captured event log as a JSON array. Used
// by the demokit walkthrough's "what did the receiver see" step and
// by e2e tests to drain expected deliveries.
func (r *receiver) handleReceived(w http.ResponseWriter, _ *http.Request) {
	r.mu.Lock()
	out := make([]receivedEvent, len(r.events))
	copy(out, r.events)
	r.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// verifyStandardWebhooksSig implements the Standard Webhooks v1
// signature scheme: HMAC-SHA256 over "{id}.{timestamp}.{body}" keyed by
// the raw secret, base64-encoded, formatted as one or more space-
// separated "v1,<sig>" entries in the webhook-signature header. Any
// matching entry passes verification.
//
// The secret may be either the raw bytes or the whsec_<base64> form
// that mcpkit emits; this helper accepts both.
func verifyStandardWebhooksSig(secret, id, ts string, body []byte, sigHeader string) bool {
	keyBytes := []byte(secret)
	if strings.HasPrefix(secret, "whsec_") {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
		if err == nil {
			keyBytes = decoded
		}
	}

	mac := hmac.New(sha256.New, keyBytes)
	mac.Write([]byte(id))
	mac.Write([]byte("."))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	for _, entry := range strings.Fields(sigHeader) {
		v, sig, ok := strings.Cut(entry, ",")
		if !ok || v != "v1" {
			continue
		}
		if hmac.Equal([]byte(sig), []byte(want)) {
			return true
		}
	}
	return false
}
