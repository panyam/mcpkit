// inject is a host-side event injector for the whole-enchilada stage-2
// demo. The operator runs it ad-hoc to fire a single event tagged for
// one tenant, watching to see which sibling terminals (poller/webhook
// subscribers) actually receive the event. It bypasses the push-server
// tier — events come straight from the operator's terminal, which makes
// the per-event tenant scoping crisply visible.
//
// Usage:
//
//	inject --tenant tenant-a --event chat.message --text "hello A"
//
// Or via the Makefile wrapper at the demo's leaf:
//
//	make inject TENANT=A EVENT=chat.message TEXT="hello A"
//	make inject TENANT=B EVENT=presence.changed STATE=online USER=bob
//
// The injector POSTs to nginx's /events/<name>/inject route (default
// localhost:8080), using the same EVENT_INJECT_BEARER shared secret
// the push-server uses in stage 1.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	target := flag.String("target", envOr("EVENT_SERVER_URL", "http://localhost:8080"),
		"event-server base URL (nginx frontdoor). Stage-2 compose default: localhost:8080.")
	bearer := flag.String("bearer", envOr("EVENT_INJECT_BEARER", "stage-1-shared-secret"),
		"shared secret for the /events/<name>/inject endpoint. Stage-1 default committed into the realm JSONs.")
	tenant := flag.String("tenant", os.Getenv("TENANT"),
		"Tenant tag stamped into the event payload. The event-server's tenantMatchFunc routes the event only to subscribers with claims.Tenant matching. REQUIRED.")
	eventName := flag.String("event", envOr("INJECT_EVENT", "chat.message"),
		"Event source name (chat.message | presence.changed).")
	// chat.message fields
	channel := flag.String("channel", "general", "chat.message: channel name.")
	sender := flag.String("sender", "demo", "chat.message: sender username.")
	text := flag.String("text", "hello from `make inject`", "chat.message: message body.")
	// presence.changed fields
	user := flag.String("user", "demo", "presence.changed: user whose state changed.")
	state := flag.String("state", "online", "presence.changed: online|away|offline.")
	flag.Parse()

	if *tenant == "" {
		log.Fatal("[inject] --tenant is required (one of tenant-a, tenant-b, tenant-c, or whatever realm a custom client lives in).")
	}

	payload := buildPayload(*eventName, *tenant, *channel, *sender, *text, *user, *state)
	body, err := json.Marshal(payload)
	if err != nil {
		log.Fatalf("[inject] encode: %v", err)
	}

	url := *target + "/events/" + *eventName + "/inject"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		log.Fatalf("[inject] build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if *bearer != "" {
		req.Header.Set("Authorization", "Bearer "+*bearer)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("[inject] POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		log.Fatalf("[inject] HTTP %d from %s", resp.StatusCode, url)
	}
	fmt.Printf("[inject] tenant=%s event=%s ok (%s)\n", *tenant, *eventName, url)
}

func buildPayload(eventName, tenant, channel, sender, text, user, state string) any {
	switch eventName {
	case "chat.message":
		return map[string]any{
			"tenant":  tenant,
			"channel": channel,
			"sender":  sender,
			"text":    text,
			"ts":      time.Now().UTC().Format(time.RFC3339),
		}
	case "presence.changed":
		return map[string]any{
			"tenant": tenant,
			"user":   user,
			"state":  state,
			"ts":     time.Now().UTC().Format(time.RFC3339),
		}
	default:
		// Generic fallback: just stamp tenant + timestamp + a "text"
		// field, leaving the rest of the body open-ended. Anyone who
		// wires a new event type into the event-server can use the
		// generic injector without code changes here.
		return map[string]any{
			"tenant": tenant,
			"text":   text,
			"ts":     time.Now().UTC().Format(time.RFC3339),
		}
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
