// Command conformance-client drives this SDK through the events-client-*
// scenarios in modelcontextprotocol/conformance (#540). The runner launches it
// with the scenario's server URL as the only argument:
//
//	node dist/index.js client --scenario events-client-push \
//	  --command 'go run ./cmd/conformance-client'
//
// It is deliberately thin. Every behaviour the scenarios grade (drain, poll
// floor, dead-stream reconnect, refresh, cursor handling) has to come from the
// SDK, so the driver only does what an application would: check the
// capability, list, subscribe in the mode each descriptor names, wait, stop.
// A red check is a finding about the SDK, not something to patch here.
//
// It lives in this module rather than in cmd/testclient so the root mcpkit
// module does not have to depend on experimental/.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
)

const eventsExtensionID = "io.modelcontextprotocol/events"

// Revisions that use the stateless SEP-2575 lifecycle.
var statelessVersions = map[string]bool{"2026-07-28": true}

type descriptor struct {
	Name     string   `json:"name"`
	Delivery []string `json:"delivery"`
}

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: conformance-client <server-url>")
	}
	serverURL := os.Args[1]
	log.Printf("scenario=%s server=%s", os.Getenv("MCP_CONFORMANCE_SCENARIO"), serverURL)

	ctx, cancel := context.WithTimeout(context.Background(), runDuration())
	defer cancel()

	opts := []client.ClientOption{client.WithClientLogging(log.Default())}
	if statelessVersions[os.Getenv("MCP_CONFORMANCE_PROTOCOL_VERSION")] {
		opts = append(opts, client.WithClientMode(client.ClientModeStateless))
	}
	c := client.NewClient(serverURL,
		core.ClientInfo{Name: "mcpkit-events-conformance", Version: "0.1.0"}, opts...)
	if err := c.Connect(context.Background()); err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer c.Close()

	if !c.ServerSupportsExtension(eventsExtensionID) {
		log.Printf("server does not declare %s; nothing to do", eventsExtensionID)
		return
	}

	res, err := c.Call(ctx, "events/list", map[string]any{})
	if err != nil {
		log.Fatalf("events/list: %v", err)
	}
	var listed struct {
		Events []descriptor `json:"events"`
	}
	if err := json.Unmarshal(res.Raw, &listed); err != nil {
		log.Fatalf("events/list: %v", err)
	}

	var receiverURL string
	for _, d := range listed.Events {
		if len(d.Delivery) == 0 {
			continue
		}
		switch d.Delivery[0] {
		case "push":
			stream, err := eventsclient.Stream(ctx, c, eventsclient.StreamOptions{EventName: d.Name})
			if err != nil {
				log.Printf("events/stream %s: %v", d.Name, err)
				continue
			}
			defer stream.Stop()
		case "webhook":
			if receiverURL == "" {
				receiverURL = startReceiver()
			}
			sub, err := eventsclient.Subscribe(ctx, c, eventsclient.SubscribeOptions{
				EventName:   d.Name,
				CallbackURL: receiverURL,
			})
			if err != nil {
				log.Printf("events/subscribe %s: %v", d.Name, err)
				continue
			}
			defer sub.Stop()
		case "poll":
			poll, err := eventsclient.Poll(ctx, c, eventsclient.PollOptions{
				EventName: d.Name,
				// Re-listing is the application's part of the rule; the SDK
				// only surfaces the notification.
				OnListChanged: func() {
					if _, err := c.Call(ctx, "events/list", map[string]any{}); err != nil {
						log.Printf("events/list after list_changed: %v", err)
					}
				},
			})
			if err != nil {
				log.Printf("events/poll %s: %v", d.Name, err)
				continue
			}
			defer poll.Stop()
		default:
			log.Printf("%s: unknown delivery mode %q", d.Name, d.Delivery[0])
		}
	}

	<-ctx.Done()
}

// runDuration is context.durationMs from MCP_CONFORMANCE_CONTEXT, the length
// of the scenario's script.
func runDuration() time.Duration {
	var ctx struct {
		DurationMs int `json:"durationMs"`
	}
	_ = json.Unmarshal([]byte(os.Getenv("MCP_CONFORMANCE_CONTEXT")), &ctx)
	if ctx.DurationMs <= 0 {
		ctx.DurationMs = 8000
	}
	return time.Duration(ctx.DurationMs) * time.Millisecond
}

// startReceiver serves a callback that accepts every delivery. The webhook
// scenario grades only the subscribe calls, so verifying deliveries here would
// test nothing.
func startReceiver() string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("receiver: %v", err)
	}
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	return "http://" + ln.Addr().String() + "/events"
}
