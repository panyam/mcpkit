package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"time"
)

// runAlertFeeder yields a synthetic alert every interval, cycling
// across severity tiers so Match-by-severity has events to filter.
// Each alert carries a reporter username (PII) and an email-bearing
// message body so the Transform redaction story is visible on
// subscribers that opt in.
func runAlertFeeder(ctx context.Context, yield func(AlertData) error, interval time.Duration) {
	severities := []string{"P1", "P2", "P3"}
	services := []string{"api-gateway", "auth-service", "payments", "search"}
	reporters := []string{"alice", "bob", "carol"}
	messages := []string{
		"latency spike — page on-call alice@example.com",
		"5xx rate above 2%; check service mesh",
		"queue depth >10k; reach bob@example.com",
		"disk pressure on node-3",
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano() + 7))

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ts := <-t.C:
			a := AlertData{
				Severity:  severities[rng.Intn(len(severities))],
				Service:   services[rng.Intn(len(services))],
				Reporter:  reporters[rng.Intn(len(reporters))],
				Message:   messages[rng.Intn(len(messages))],
				Timestamp: ts.UTC().Format(time.RFC3339),
			}
			if err := yield(a); err != nil {
				log.Printf("[alert] yield: %v", err)
			}
		}
	}
}

// injectAlert posts one specific alert event for the walkthrough's
// deterministic Match + Transform steps.
func injectAlert(yield func(AlertData) error, severity, service, reporter, message string) error {
	return yield(AlertData{
		Severity:  severity,
		Service:   service,
		Reporter:  reporter,
		Message:   message,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
}

// formatAlertSummary is a tiny helper for the walkthrough's pretty
// printing — keeps the wire shape readable in the demo output.
func formatAlertSummary(a AlertData) string {
	return fmt.Sprintf("[%s] %s — reporter=%q msg=%q", a.Severity, a.Service, a.Reporter, a.Message)
}
