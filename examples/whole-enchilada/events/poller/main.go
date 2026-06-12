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
//	       --tenant  asgard \
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
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
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
	username := flag.String("username", os.Getenv("USERNAME"),
		"OAuth username for ROPC token acquisition. Alternative to --token — pair with --password. Tenant comes from --tenant.")
	password := flag.String("password", os.Getenv("PASSWORD"),
		"OAuth password for ROPC token acquisition. Alternative to --token — pair with --username.")
	keycloakURL := flag.String("keycloak-url", envOr("KEYCLOAK_URL", "http://localhost:8180"),
		"Keycloak base URL for ROPC token acquisition (only consulted when --username/--password are set).")
	clientID := flag.String("client-id", envOr("OAUTH_CLIENT_ID", "mcp-events-poller"),
		"OAuth client ID used to authenticate the ROPC token request.")
	clientSecret := flag.String("client-secret", envOr("OAUTH_CLIENT_SECRET", "mcpkit-demo-secret-DEMO-ONLY"),
		"OAuth client secret used to authenticate the ROPC token request.")
	tenantLabel := flag.String("tenant", os.Getenv("TENANT"),
		"Realm name. Used as a log-prefix display label, AND — when --username/--password are set — as the Keycloak realm to acquire the token from.")
	eventName := flag.String("event", envOr("POLLER_EVENT", "chat.message"),
		"Event source name to poll.")
	interval := flag.Duration("interval", 1*time.Second,
		"Cadence between polls. Lower = more responsive; higher = less server load.")
	flag.Parse()

	if *token == "" && *username != "" && *password != "" {
		acquired, err := acquireTokenROPC(*keycloakURL, *tenantLabel, *clientID, *clientSecret, *username, *password)
		if err != nil {
			log.Fatalf("[poller] ROPC token acquisition failed: %v", err)
		}
		*token = acquired
		log.Printf("[poller] acquired bearer token via ROPC (user=%s realm=%s)", *username, *tenantLabel)
	}

	if *token == "" {
		log.Fatalf("[poller] need either --token (or TOKEN=) OR --username + --password (or USERNAME= + PASSWORD=). " +
			"Run `make newtoken TENANT=<X>` to acquire one.")
	}

	prefix := "[poller"
	if *tenantLabel != "" {
		prefix = "[poller " + *tenantLabel
	}
	prefix += "]"

	c := client.NewClient(*server, core.ClientInfo{
		Name:    "whole-enchilada-poller",
		Version: "0.1.0",
	},
		client.WithClientBearerToken(*token),
		// SEP-2575 stateless wire — every poll is self-contained, so
		// nginx can round-robin across replicas freely. Required for
		// the N>1 demo posture (legacy wire keeps session state in the
		// receiving replica's in-memory map; cross-replica continuity
		// would need the not-yet-implemented shared session store).
		client.WithClientMode(client.ClientModeStateless),
	)
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
			cur, authDead := pollOnce(c, *eventName, cursor, prefix)
			if authDead {
				log.Printf("%s token invalidated by AS (401) — exiting", prefix)
				return
			}
			if cur != nil {
				cursor = *cur
			}
		}
	}
}

// pollOnce sends a single events/poll, prints every returned event,
// and returns the next cursor (nil to keep the current one when the
// call fails transiently — transient failures shouldn't stall the
// loop). The second return is authDead=true ONLY for HTTP 401, which
// the demo treats as terminal: the operator just revoked the
// session, the token is dead for the rest of its lifespan, and
// silently retrying would just spam the AS. The walkthrough's
// "revoke a session → watch the poller die" beat depends on this.
func pollOnce(c *client.Client, eventName, cursor, prefix string) (next *string, authDead bool) {
	res, err := c.Call("events/poll", map[string]any{
		"name":   eventName,
		"cursor": cursor,
	})
	if err != nil {
		// 401 surfaces as ClientAuthError on the auth-retry path
		// (most common, this is what the bearer-token flow uses); the
		// plain transport path produces HTTPStatusError instead.
		// Either one with StatusCode 401 means the session was
		// revoked and there's no way to recover without re-login —
		// exit so the iterm pane goes quiet.
		var authErr *client.ClientAuthError
		if errors.As(err, &authErr) && authErr.StatusCode == http.StatusUnauthorized {
			return nil, true
		}
		var httpErr *client.HTTPStatusError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnauthorized {
			return nil, true
		}
		fmt.Fprintf(os.Stderr, "%s poll error: %v\n", prefix, err)
		return nil, false
	}
	var pr struct {
		Events  []events.Event `json:"events"`
		Cursor  *string        `json:"cursor"`
		HasMore bool           `json:"hasMore"`
	}
	if err := json.Unmarshal(res.Raw, &pr); err != nil {
		fmt.Fprintf(os.Stderr, "%s decode error: %v\n", prefix, err)
		return nil, false
	}
	for _, ev := range pr.Events {
		printEvent(prefix, ev)
	}
	return pr.Cursor, false
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

// acquireTokenROPC obtains a bearer access token from Keycloak via
// OAuth 2.0 Resource Owner Password Credentials (RFC 6749 §4.3) for
// the given realm. ROPC is deprecated by OAuth 2.1; we keep this path
// for the demo's CI / one-liner usage where browser login is
// impractical. Matches the behavior of `make newtoken-ci`.
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
