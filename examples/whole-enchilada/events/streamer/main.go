// streamer is a long-running events/stream subscriber the operator
// runs in its own terminal during the whole-enchilada demo. It opens
// an SEP-2575 stateless-wire connection to the event-server, calls
// events/stream, and prints every notification (event payload,
// heartbeat, truncation, terminal frame) as it arrives down the
// open SSE response.
//
// Push delivery is the headline events SEP mode: lowest latency, no
// polling round-trips, server controls cadence. This binary is the
// operator-visible counterpart to issue 753's server-side enabling
// PR (response-as-SSE on the stateless wire).
//
// Usage:
//
//	streamer --server http://localhost:9090/mcp \
//	         --tenant  asgard \
//	         --username alice \
//	         --event   chat.message
//
// Or via the Makefile wrapper at the demo's leaf:
//
//	make stream TENANT=A USERNAME=alice
//
// Exit: ctrl+C / SIGTERM cancels the stream cleanly. The server
// receives the disconnect, fires its on_unsubscribe defer chain, and
// removes the subscription from its local index.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
)

func main() {
	server := flag.String("server", envOr("STREAMER_SERVER", "http://localhost:9090/mcp"),
		"MCP endpoint URL (default: nginx frontdoor)")
	token := flag.String("token", os.Getenv("TOKEN"),
		"Bearer token. Acquire via `make newtoken TENANT=<X>` upstream of this binary.")
	username := flag.String("username", os.Getenv("USERNAME"),
		"OAuth username for ROPC token acquisition. Alternative to --token — pair with --password.")
	password := flag.String("password", os.Getenv("PASSWORD"),
		"OAuth password for ROPC token acquisition. Defaults to USERNAME when omitted (realm seeds align).")
	keycloakURL := flag.String("keycloak-url", envOr("KEYCLOAK_URL", "http://localhost:8180"),
		"Keycloak base URL for ROPC token acquisition (only consulted when --username/--password are set).")
	clientID := flag.String("client-id", envOr("OAUTH_CLIENT_ID", "mcp-events-poller"),
		"OAuth client ID used to authenticate the ROPC token request.")
	clientSecret := flag.String("client-secret", envOr("OAUTH_CLIENT_SECRET", "mcpkit-demo-secret-DEMO-ONLY"),
		"OAuth client secret used to authenticate the ROPC token request.")
	tenantLabel := flag.String("tenant", os.Getenv("TENANT"),
		"Realm name. Used as a log-prefix display label, AND — when --username is set — as the Keycloak realm.")
	eventName := flag.String("event", envOr("STREAMER_EVENT", "chat.message"),
		"Event source name to stream.")
	flag.Parse()

	// PASSWORD defaults to USERNAME when both --username is set and
	// --password is empty (mirrors the Makefile poller/webhook
	// convention; the demo realms seed passwords equal to usernames).
	if *password == "" {
		*password = *username
	}

	if *token == "" && *username != "" {
		acquired, err := acquireTokenROPC(*keycloakURL, *tenantLabel, *clientID, *clientSecret, *username, *password)
		if err != nil {
			log.Fatalf("[streamer] ROPC token acquisition failed: %v", err)
		}
		*token = acquired
		log.Printf("[streamer] acquired bearer token via ROPC (user=%s realm=%s)", *username, *tenantLabel)
	}

	if *token == "" {
		log.Fatalf("[streamer] need either --token (or TOKEN=) OR --username [+ --password]. " +
			"Run `make newtoken TENANT=<X>` to acquire one.")
	}

	prefix := "[streamer"
	if *tenantLabel != "" {
		prefix = "[streamer " + *tenantLabel
	}
	prefix += "]"

	// Last-seen X-Replica response header. The event-server stamps this
	// per HTTP response (demo-only deployment metadata, not part of the
	// MCP spec). For the streamer the value is set when the SSE open
	// 200 lands and stays stable for the life of the stream; a
	// reconnect to a different replica flips it.
	var lastReplica atomic.Value
	lastReplica.Store("")

	c := client.NewClient(*server, core.ClientInfo{
		Name:    "whole-enchilada-streamer",
		Version: "0.1.0",
	},
		client.WithClientBearerToken(*token),
		// SEP-2575 stateless wire. events/stream rides the new
		// response-as-SSE path landed under issue 753 — the open POST
		// response carries notifications/events/event frames as SSE
		// events, no per-session push channel needed, nginx can
		// round-robin freely across replicas.
		client.WithClientMode(client.ClientModeStateless),
		client.WithInspectResponse(func(resp *http.Response) {
			r := resp.Header.Get("X-Replica")
			if r == "" {
				return
			}
			prev, _ := lastReplica.Load().(string)
			if r != prev {
				lastReplica.Store(r)
				log.Printf("%s served by %s", prefix, r)
			}
		}),
	)
	if err := c.Connect(); err != nil {
		log.Fatalf("%s connect failed: %v", prefix, err)
	}
	defer func() { _ = c.Close() }()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Printf("%s opening events/stream for %q...", prefix, *eventName)

	stream, err := eventsclient.Stream(ctx, c, eventsclient.StreamOptions{
		EventName: *eventName,
		OnEvent: func(ev events.Event) {
			printEvent(prefix, ev)
		},
		OnHeartbeat: func(cursor *string) {
			cur := "<nil>"
			if cursor != nil {
				cur = *cursor
			}
			log.Printf("%s heartbeat cursor=%s", prefix, cur)
		},
		OnTruncated: func(cursor *string) {
			cur := "<nil>"
			if cursor != nil {
				cur = *cursor
			}
			log.Printf("%s TRUNCATED — server signaled a gap; resumed at cursor=%s", prefix, cur)
		},
		OnError: func(e error) {
			log.Printf("%s upstream error (transient — subscription stays active): %v", prefix, e)
		},
		OnTerminated: func(e error) {
			if e != nil {
				log.Printf("%s TERMINATED: %v", prefix, e)
			} else {
				log.Printf("%s TERMINATED (clean)", prefix)
			}
		},
	})
	if err != nil {
		log.Fatalf("%s stream open failed: %v", prefix, err)
	}
	log.Printf("%s subscribed — listening for events", prefix)

	// Block until the stream goroutine exits (operator hit ctrl+C OR
	// server sent terminated OR the underlying call errored).
	<-stream.Done()
	if e := stream.Err(); e != nil {
		log.Printf("%s stream ended with error: %v", prefix, e)
	} else {
		log.Printf("%s stream ended cleanly", prefix)
	}
}

// printEvent renders a delivered event to stdout — one line per
// event, time-stamped, with the tenant tag pulled out of the JSON
// payload for at-a-glance scanning across the operator's sibling
// terminals. Matches the poller's print format so the two delivery
// modes show identical output for the same source.
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

// acquireTokenROPC obtains a bearer access token from Keycloak via
// OAuth 2.0 Resource Owner Password Credentials (RFC 6749 §4.3) for
// the given realm. Same shape as poller's helper — see that file for
// the full rationale (deprecated by OAuth 2.1; kept for CI / one-liner
// usage).
func acquireTokenROPC(keycloakURL, realm, clientID, clientSecret, username, password string) (string, error) {
	if realm == "" {
		return "", fmt.Errorf("--tenant is required when using --username/--password")
	}
	endpoint := strings.TrimRight(keycloakURL, "/") + "/realms/" + realm + "/protocol/openid-connect/token"
	form := url.Values{
		"grant_type":    {"password"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"username":      {username},
		"password":      {password},
	}
	resp, err := http.PostForm(endpoint, form)
	if err != nil {
		return "", fmt.Errorf("POST %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, string(body))
	}
	var t struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &t); err != nil {
		return "", fmt.Errorf("token response decode failed: %w", err)
	}
	if t.AccessToken == "" {
		return "", fmt.Errorf("token response missing access_token")
	}
	return t.AccessToken, nil
}
