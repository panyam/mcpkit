// poller is a long-running events/poll subscriber the operator runs
// in its own terminal during the whole-enchilada stage-2 demo. It
// authenticates as one tenant (via Bearer token acquired upstream by
// `make newtoken`), polls a single event source, and prints every
// delivered event to stdout — visibly demonstrating per-tenant
// delivery isolation when run side-by-side with another tenant's
// poller in a sibling terminal.
//
// Usage:
//
//	poller --server http://localhost:9090/mcp \
//	       --token   $(make newtoken TENANT=A) \
//	       --tenant  tenant-a \
//	       --event   chat.message
//
// Or via the Makefile wrapper at the demo's leaf:
//
//	make poller TENANT=A TOKEN=$TA
//
// Exit: ctrl+C / SIGTERM. The poller does not unsubscribe — the
// server's TTL prunes the poll lease shortly after.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
)

func main() {
	server := flag.String("server", envOr("POLLER_SERVER", "http://localhost:9090/mcp"),
		"MCP endpoint URL (default: nginx frontdoor)")
	token := flag.String("token", os.Getenv("TOKEN"),
		"Bearer token. Acquire via `make newtoken TENANT=<X>` upstream of this binary.")
	tenantLabel := flag.String("tenant", os.Getenv("TENANT"),
		"Display label for log prefix only; does NOT change auth. The actual tenant is whatever realm the token came from.")
	eventName := flag.String("event", envOr("POLLER_EVENT", "chat.message"),
		"Event source name to poll.")
	interval := flag.Duration("interval", 1*time.Second,
		"Cadence between polls. Lower = more responsive; higher = less server load.")
	flag.Parse()

	if *token == "" {
		log.Fatalf("[poller] --token is required. Run `make newtoken TENANT=<X>` first.")
	}

	prefix := "[poller"
	if *tenantLabel != "" {
		prefix = "[poller " + *tenantLabel
	}
	prefix += "]"

	c := client.NewClient(*server, core.ClientInfo{
		Name:    "whole-enchilada-poller",
		Version: "0.1.0",
	}, client.WithClientBearerToken(*token))
	if err := c.Connect(); err != nil {
		log.Fatalf("%s connect failed: %v", prefix, err)
	}
	defer func() { _ = c.Close() }()
	log.Printf("%s connected, polling %q every %s", prefix, *eventName, *interval)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Start from cursor "0" so the demo can see backfill (events
	// injected before the poller started). Stage-2 demos are short
	// enough that the buffer fits; longer-running production would
	// poll from head (cursor: "" or omitted).
	cursor := "0"
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("%s shutdown", prefix)
			return
		case <-ticker.C:
			cur := pollOnce(c, *eventName, cursor, prefix)
			if cur != nil {
				cursor = *cur
			}
		}
	}
}

// pollOnce sends a single events/poll, prints every returned event,
// and returns the next cursor (or nil to keep the current one when
// the call fails — transient failures shouldn't stall the loop).
func pollOnce(c *client.Client, eventName, cursor, prefix string) *string {
	res, err := c.Call("events/poll", map[string]any{
		"name":   eventName,
		"cursor": cursor,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s poll error: %v\n", prefix, err)
		return nil
	}
	var pr struct {
		Events  []events.Event `json:"events"`
		Cursor  *string        `json:"cursor"`
		HasMore bool           `json:"hasMore"`
	}
	if err := json.Unmarshal(res.Raw, &pr); err != nil {
		fmt.Fprintf(os.Stderr, "%s decode error: %v\n", prefix, err)
		return nil
	}
	for _, ev := range pr.Events {
		printEvent(prefix, ev)
	}
	return pr.Cursor
}

// printEvent renders a delivered event to stdout — one line per event,
// time-stamped, with the tenant tag pulled out of the JSON payload
// for at-a-glance scanning across the operator's sibling terminals.
func printEvent(prefix string, ev events.Event) {
	var tagged struct {
		Tenant string `json:"tenant"`
	}
	_ = json.Unmarshal(ev.Data, &tagged)
	fmt.Printf("%s %s tenant=%-10s event=%s data=%s\n",
		time.Now().Format("15:04:05"),
		prefix,
		tagged.Tenant,
		ev.Name,
		string(ev.Data),
	)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
