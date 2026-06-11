// synth is an operator-runnable synthetic event producer for the
// whole-enchilada events demo. Stack starts silent; the operator runs
// this in sibling terminals to start producing events at the
// configured cadence.
//
// From the events lib's perspective every event is just an opaque JSON
// payload POSTed to /events/<name>/inject. The only thing event-
// shape-specific is the random "vocabulary" used to populate fields —
// chat picks from channel/sender/text, presence picks from user/state.
// One binary, one ticker loop, one POST function; the vocab is
// selected by --event.
//
// Usage:
//
//	synth --event chat.message --every 2s
//	synth --event presence.changed --every 5s --tenants asgard
//
// Or via the leaf Makefile wrappers:
//
//	make drive-chat                 # --event chat.message --every 2s
//	make drive-presence             # --event presence.changed --every 5s
//	make drive-chat EVERY=200ms     # high-volume mode
//	make drive-chat TENANTS=asgard   # single-tenant only
//
// New event types: drop a new vocab into vocabs.go, register it in the
// vocab map, no new binary needed.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	eventName := flag.String("event", "chat.message",
		"event source name to inject into. One of: "+strings.Join(vocabNames(), ", "))
	target := flag.String("target", envOr("EVENT_SERVER_URL", "http://localhost:9090"),
		"event-server base URL (nginx frontdoor). Stage-2 compose default: localhost:9090.")
	bearer := flag.String("bearer", envOr("EVENT_INJECT_BEARER", "stage-1-shared-secret"),
		"shared secret matching the events compose's EVENT_INJECT_BEARER default.")
	every := flag.Duration("every", 0,
		"cadence between synthetic events. Defaults to the vocab's recommended rate when zero.")
	tenants := flag.String("tenants", envOr("DRIVE_TENANTS", "asgard,babylon,camelot"),
		"comma-separated tenant tags; each event rotates through them in order. Empty = no tag.")
	flag.Parse()

	vocab, ok := vocabs[*eventName]
	if !ok {
		log.Fatalf("[synth] unknown --event %q; known: %s", *eventName, strings.Join(vocabNames(), ", "))
	}
	interval := *every
	if interval <= 0 {
		interval = vocab.defaultEvery
	}
	tenantTags := splitNonEmpty(*tenants)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Printf("[synth] event=%s target=%s every=%s tenants=%v", *eventName, *target, interval, tenantTags)
	run(ctx, *target, *bearer, *eventName, vocab, interval, tenantTags)
	log.Printf("[synth] shutdown")
}

func run(ctx context.Context, target, bearer, eventName string, vocab vocab, interval time.Duration, tenants []string) {
	rng := rand.New(rand.NewSource(time.Now().UnixNano() + vocab.rngOffset))
	client := &http.Client{Timeout: 10 * time.Second}
	url := target + "/events/" + eventName + "/inject"

	t := time.NewTicker(interval)
	defer t.Stop()

	tenantIdx := 0
	seq := 0
	pushed := 0
	for {
		select {
		case <-ctx.Done():
			log.Printf("[synth] seq=%d pushed=%d", seq, pushed)
			return
		case ts := <-t.C:
			seq++ // increments on every tick, including failed posts — receivers see gaps
			tenant := ""
			if len(tenants) > 0 {
				tenant = tenants[tenantIdx%len(tenants)]
				tenantIdx++
			}
			payload := vocab.sample(rng, ts.UTC().Format(time.RFC3339Nano), tenant, seq)
			if err := post(ctx, client, url, bearer, payload); err != nil {
				log.Printf("[synth] seq=%d %s: %v", seq, url, err)
				continue
			}
			pushed++
		}
	}
}

func post(ctx context.Context, client *http.Client, url, bearer string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func splitNonEmpty(s string) []string {
	var out []string
	for _, raw := range strings.Split(s, ",") {
		if v := strings.TrimSpace(raw); v != "" {
			out = append(out, v)
		}
	}
	return out
}
