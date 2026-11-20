// Command check-webhook-identity subscribes a webhook receiver to an events
// server and checks the receiver verifies a delivery with a key it took from
// the server's own Server Card. It also checks a card reached through a
// redirect to another origin is refused. It is the webhook half of
// `make exercise-server-card` (mission #1737, #1738).
//
//	go run ./cmd/check-webhook-identity -url http://127.0.0.1:8080/mcp -event chat.message
//
// The server must sign deliveries (kitchen-sink --webhook-signing-key) and
// accept loopback http callbacks. It prints one line per check and exits 1 if
// any fails.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	events "github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:8080/mcp", "the server's streamable HTTP URL")
	event := flag.String("event", "chat.message", "event to subscribe to")
	wait := flag.Duration("wait", 15*time.Second, "how long to wait for a delivery")
	flag.Parse()

	failed := false
	check := func(name string, ok bool, detail string) {
		mark := "ok  "
		if !ok {
			mark, failed = "FAIL", true
		}
		fmt.Printf("  %s %s", mark, name)
		if detail != "" {
			fmt.Printf(": %s", detail)
		}
		fmt.Println()
	}

	fmt.Printf("webhook identity @ %s (event %s)\n", *url, *event)
	ctx := context.Background()

	card, err := client.FetchServerCard(ctx, *url, client.WithInsecureCardFetch())
	keys := 0
	if err == nil {
		if remote, ok := client.CardRemote(card, *url); ok {
			if info, ok := events.CardWebhooks(remote.ExtensionsInfo); ok && info.SigningKeys != nil {
				keys = len(info.SigningKeys.Keys)
			}
		}
	}
	check("card publishes webhook signing keys", keys > 0, fmt.Sprintf("%d key(s), fetch err %v", keys, err))

	secret := events.GenerateSecret()
	rcv := eventsclient.NewReceiver[map[string]any](secret, eventsclient.WithServerIdentity(*url, client.WithInsecureCardFetch()))
	callback, stopRcv := serve(rcv)
	defer stopRcv()

	c := client.NewClient(*url, core.Implementation{Name: "check-webhook-identity", Version: "0.1.0"})
	if err := c.Connect(ctx); err != nil {
		check("connect", false, err.Error())
		os.Exit(1)
	}
	defer c.Close()
	sub, err := eventsclient.Subscribe(ctx, c, eventsclient.SubscribeOptions{EventName: *event, CallbackURL: callback, Secret: secret})
	if err != nil {
		check("subscribe", false, err.Error())
		os.Exit(1)
	}
	defer sub.StopAndUnsubscribe(context.Background())

	select {
	case ev := <-rcv.Events():
		check("webhook delivery verified with a key from the card", true, "eventId "+ev.EventID)
	case <-time.After(*wait):
		check("webhook delivery verified with a key from the card", false, fmt.Sprintf("no verified delivery in %s", *wait))
	}

	redirector, stopRedir := serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, *url+core.ServerCardPath, http.StatusFound)
	}))
	defer stopRedir()
	_, err = client.FetchServerCard(ctx, redirector+"/mcp", client.WithInsecureCardFetch())
	check("card reached through a cross-origin redirect is refused", errors.Is(err, client.ErrCardCrossOrigin), fmt.Sprint(err))

	if failed {
		os.Exit(1)
	}
}

// serve listens on a free loopback port and returns its base URL.
func serve(h http.Handler) (string, func()) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Println("listen:", err)
		os.Exit(1)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)
	return "http://" + ln.Addr().String(), func() { srv.Close() }
}
