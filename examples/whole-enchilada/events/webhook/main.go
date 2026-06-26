// webhook is a tenant-scoped local webhook receiver the operator runs
// in its own terminal during the whole-enchilada stage-2 demo. It
// authenticates as one tenant (Bearer token from `make newtoken`),
// spins up a local HTTP listener on a free port, registers a webhook
// subscription with the event-server pointing back at that listener,
// HMAC-verifies every delivery, and prints to stdout.
//
// Usage:
//
//	webhook --server http://localhost:9090/mcp \
//	        --token   $(make newtoken TENANT=A) \
//	        --tenant  asgard \
//	        --event   chat.message
//
// Or via the Makefile wrapper at the demo's leaf:
//
//	make webhook TENANT=A TOKEN=$TA
//
// Exit: ctrl+C / SIGTERM. The subscription's TTL prunes it on the
// server side shortly after the binary exits.
package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
)

func main() {
	server := flag.String("server", envOr("WEBHOOK_SERVER", "http://localhost:9090/mcp"),
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
	eventName := flag.String("event", envOr("WEBHOOK_EVENT", "chat.message"),
		"Event source name to subscribe to.")
	listenAddr := flag.String("listen", envOr("WEBHOOK_LISTEN", "127.0.0.1:0"),
		"Local listen address. Default 127.0.0.1:0 picks a free port and reports the URL.")
	publicURL := flag.String("public-url", os.Getenv("WEBHOOK_PUBLIC_URL"),
		"Public URL the event-server uses to call this listener. Defaults to http://<listen-addr>. Override when the event-server runs in Docker and needs host.docker.internal or a tunneled URL.")
	// ttlMs: tristate via string so absent / null / numeric are
	// distinguishable on the wire (spec PR1 commit 99f3589c
	// §"Subscription TTL"). Empty = absent (server picks default),
	// "null" = no-expiry request, "<int>" = suggested ms.
	ttlMsFlag := flag.String("ttl-ms", os.Getenv("TTL_MS"),
		`Client-suggested subscription TTL in ms (spec PR1 commit 99f3589c §"Subscription TTL"). Empty = absent (server picks). "null" = request no-expiry (server returns refreshBefore:null when WithAllowInfiniteWebhookTTL is enabled — see EVENTS_ALLOW_INFINITE_TTL on the event-server side). "<int>" = suggested ms. Demo flavor table: 900000 (15m, granted as-is), 30000 (30s, clamped UP to MinWebhookTTL=5min), null (no-expiry).`)
	// reply-status: the HTTP status the local receiver returns after
	// verifying the signature. Demonstrates server-side response
	// branching (2xx ack vs 410 abandon vs 5xx retry-then-suspend).
	replyStatus := flag.Int("reply-status", envOrInt("REPLY_STATUS", 200),
		"HTTP status returned by the local receiver after verifying each delivery. 200 (default) = ack, the happy path. 410 = abandon THIS delivery without affecting the subscription (PR 778 / spec PR1 commit 905ade36). 500 = treat as transient failure, server retries 3× then suspends. Other values pass through unchanged for ad-hoc exploration.")
	// exit-after: terminate the receiver process after N deliveries.
	// Composes with TTL_MS=null to demonstrate the failure-based GC
	// end-to-end (PR 783) — once the listener is gone, the server's
	// deliveries fail with connection-refused, the failure run
	// accumulates, and past EVENTS_NO_EXPIRY_GC_WINDOW the no-expiry
	// sub is dropped + a `terminated` envelope is posted.
	exitAfter := flag.Int("exit-after", envOrInt("EXIT_AFTER", 0),
		"Exit the process after N successfully-received deliveries. 0 (default) = run until Ctrl+C. Combined with TTL_MS=null this exercises the no-expiry failure-based GC path end-to-end: deliveries fail with connection_refused after exit, failure run accumulates, past EVENTS_NO_EXPIRY_GC_WINDOW (set on the event-server) the registry drops the sub and POSTs a `terminated` envelope.")
	flag.Parse()

	if *token == "" && *username != "" && *password != "" {
		acquired, err := acquireTokenROPC(*keycloakURL, *tenantLabel, *clientID, *clientSecret, *username, *password)
		if err != nil {
			log.Fatalf("[webhook] ROPC token acquisition failed: %v", err)
		}
		*token = acquired
		log.Printf("[webhook] acquired bearer token via ROPC (user=%s realm=%s)", *username, *tenantLabel)
	}

	if *token == "" {
		log.Fatal("[webhook] need either --token (or TOKEN=) OR --username + --password (or USERNAME= + PASSWORD=). " +
			"Run `make newtoken TENANT=<X>` to acquire one.")
	}

	prefix := "[webhook"
	if *tenantLabel != "" {
		prefix = "[webhook " + *tenantLabel
	}
	prefix += "]"

	// Listener up first so we know the URL before we subscribe.
	listener, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		log.Fatalf("%s listen: %v", prefix, err)
	}
	defer func() { _ = listener.Close() }()

	callbackURL := *publicURL
	if callbackURL == "" {
		// The event-server in this demo runs inside Docker. From the
		// container's POV, 127.0.0.1 is the container's loopback —
		// NOT the host's — so a callback URL like
		// http://127.0.0.1:<port> can never reach this binary
		// running on the host. Rewrite the host portion to
		// host.docker.internal (Docker Desktop auto-maps on
		// macOS/Windows; Linux needs extra_hosts:
		// host.docker.internal:host-gateway in compose). The
		// listener still binds to 127.0.0.1 so external machines
		// can't hit it.
		addr := listener.Addr().String()
		host, port, splitErr := net.SplitHostPort(addr)
		if splitErr == nil && (host == "127.0.0.1" || host == "::1" || host == "0.0.0.0" || host == "" || host == "[::]") {
			callbackURL = "http://host.docker.internal:" + port
		} else {
			callbackURL = "http://" + addr
		}
	}
	log.Printf("%s listening on %s — webhook deliveries go to %s/webhook", prefix, listener.Addr(), callbackURL)

	c := client.NewClient(*server, core.ClientInfo{
		Name:    "whole-enchilada-webhook",
		Version: "0.1.0",
	},
		client.WithClientBearerToken(*token),
		// SEP-2575 stateless wire — same as poller. Lets nginx round-
		// robin freely across replicas for N>1.
		client.WithClientMode(client.ClientModeStateless),
	)
	if err := c.Connect(); err != nil {
		log.Fatalf("%s connect failed: %v", prefix, err)
	}
	defer func() { _ = c.Close() }()

	subOpts := eventsclient.SubscribeOptions{
		EventName:   *eventName,
		CallbackURL: callbackURL + "/webhook",
	}
	// Wire the --ttl-ms tristate into the SDK options. "null" sets
	// NoExpiry; a positive int sets TTLMs; empty leaves both unset so
	// the SDK omits the ttlMs key entirely.
	switch *ttlMsFlag {
	case "":
		// absent — server picks default
	case "null":
		subOpts.NoExpiry = true
		log.Printf("%s ttlMs=null — requesting no-expiry subscription", prefix)
	default:
		n, err := strconv.ParseInt(*ttlMsFlag, 10, 64)
		if err != nil {
			log.Fatalf("%s --ttl-ms must be empty, \"null\", or an integer; got %q: %v", prefix, *ttlMsFlag, err)
		}
		subOpts.TTLMs = &n
		log.Printf("%s ttlMs=%d — client-suggested TTL in ms", prefix, n)
	}

	sub, err := eventsclient.Subscribe(context.Background(), c, subOpts)
	if err != nil {
		log.Fatalf("%s subscribe failed: %v", prefix, err)
	}
	defer sub.Stop()
	// Surface the server-granted refreshBefore so the demo operator
	// can see clamp / null / as-suggested directly in this window's
	// log stream — primary readout for the TTL negotiation beat.
	if rb := sub.RefreshBefore(); rb != nil {
		log.Printf("%s subscribed sub_id=%s refreshBefore=%s — events route to %s/webhook",
			prefix, sub.ID(), rb.Format(time.RFC3339), callbackURL)
	} else {
		log.Printf("%s subscribed sub_id=%s refreshBefore=null (no-expiry granted) — events route to %s/webhook",
			prefix, sub.ID(), callbackURL)
	}

	receiver := &deliveryReceiver{
		secret:      sub.Secret(),
		prefix:      prefix,
		replyStatus: *replyStatus,
	}
	// EXIT_AFTER closes shutdown so the main select loop unwinds.
	// We make it a function call to avoid a data race between the
	// receiver writing to a channel and the select reading; the
	// channel is closed exactly once via sync.Once.
	shutdown := make(chan struct{})
	if *exitAfter > 0 {
		log.Printf("%s exit-after=%d — process will exit after %d successfully-received deliveries", prefix, *exitAfter, *exitAfter)
		receiver.exitAfter = *exitAfter
		receiver.exitSignal = shutdown
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", receiver.handle)

	srv := &http.Server{Handler: mux}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	serverErr := make(chan error, 1)
	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
		close(serverErr)
	}()

	select {
	case <-ctx.Done():
		log.Printf("%s shutdown", prefix)
	case <-shutdown:
		log.Printf("%s exit-after reached — process exiting; next delivery will fail with connection_refused", prefix)
	case err := <-serverErr:
		log.Printf("%s server error: %v", prefix, err)
	}
	_ = srv.Shutdown(context.Background())
}

// deliveryReceiver verifies and prints each webhook POST. Reuses the
// Standard Webhooks signature scheme the events lib emits — the same
// verification any production receiver would do.
//
// replyStatus / exitAfter / exitSignal are demo-flavor knobs (see the
// --reply-status / --exit-after flags) — they let the same binary
// simulate happy-path delivery, deliberate per-delivery abandon
// (410 Gone), transient failure (5xx), or full process death after N
// deliveries. The server-side response branching IS the demo.
type deliveryReceiver struct {
	secret      string
	prefix      string
	replyStatus int
	// exitAfter > 0 enables the EXIT_AFTER beat: after this many
	// successfully-received deliveries the receiver closes
	// exitSignal exactly once, causing main's select to unwind and
	// the process to exit. delivered counts since process start.
	exitAfter  int
	exitSignal chan struct{}
	exitOnce   sync.Once
	delivered  atomic.Int64
}

const standardWebhooksMaxSkewSeconds = 5 * 60

func (r *deliveryReceiver) handle(w http.ResponseWriter, req *http.Request) {
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
	if abs64(time.Now().Unix()-tsSec) > standardWebhooksMaxSkewSeconds {
		http.Error(w, "webhook-timestamp outside skew window", http.StatusBadRequest)
		return
	}
	if !verifySignature(r.secret, id, ts, body, sig) {
		http.Error(w, "signature verification failed", http.StatusBadRequest)
		return
	}

	// Strip whsec_ from secret for HMAC; pull tenant from body for log.
	var tagged struct {
		Tenant string `json:"tenant"`
		Name   string `json:"name"`
	}
	_ = json.Unmarshal(body, &tagged)

	// Return the configured reply status (default 200). Demo modes:
	//   200 — happy path; server records delivery success
	//   410 — abandon-THIS-delivery; server logs "abandoned per
	//         receiver 410 Gone (subscription unaffected)" + sub
	//         stays Active=true (PR 778 / spec PR1 commit 905ade36)
	//   5xx — transient failure; server retries 3× then suspends
	//         (refresh reactivates per spec §"Webhook Delivery Status")
	status := r.replyStatus
	if status == 0 {
		status = http.StatusOK
	}
	// X-Replica is the event-server replica that delivered this POST
	// (stamped via events.WithWebhookExtraHeaders on the server). Shown
	// per delivery so the operator watches it rotate across replicas
	// under make drive-chat (Phase 3). "?" when N=1 / header absent.
	replica := req.Header.Get("X-Replica")
	if replica == "" {
		replica = "?"
	}
	fmt.Printf("%s %s replica=%s tenant=%-10s id=%s reply=%d body=%s\n",
		time.Now().Format("15:04:05"),
		r.prefix,
		replica,
		tagged.Tenant,
		id,
		status,
		string(body),
	)
	w.WriteHeader(status)

	// EXIT_AFTER counts ONLY successfully-received deliveries — a
	// non-2xx reply still increments because the receiver actually
	// SAW the delivery; the choice to fail it is the demo's point.
	if r.exitAfter > 0 && r.exitSignal != nil {
		seen := r.delivered.Add(1)
		if int(seen) >= r.exitAfter {
			r.exitOnce.Do(func() {
				log.Printf("%s exit-after target reached after %d deliveries", r.prefix, seen)
				close(r.exitSignal)
			})
		}
	}
}

func verifySignature(secret, id, ts string, body []byte, signature string) bool {
	rawSecret := secret
	if strings.HasPrefix(rawSecret, "whsec_") {
		decoded, err := base64.StdEncoding.DecodeString(rawSecret[len("whsec_"):])
		if err == nil {
			rawSecret = string(decoded)
		}
	}
	toSign := id + "." + ts + "." + string(body)
	mac := hmac.New(sha256.New, []byte(rawSecret))
	mac.Write([]byte(toSign))
	expected := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	for _, candidate := range strings.Split(signature, " ") {
		if hmac.Equal([]byte(candidate), []byte(expected)) {
			return true
		}
	}
	return false
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envOrInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
