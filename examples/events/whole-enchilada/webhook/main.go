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
//	        --tenant  tenant-a \
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
		callbackURL = "http://" + listener.Addr().String()
	}
	log.Printf("%s listening on %s — webhook deliveries go to %s/webhook", prefix, listener.Addr(), callbackURL)

	c := client.NewClient(*server, core.ClientInfo{
		Name:    "whole-enchilada-webhook",
		Version: "0.1.0",
	}, client.WithClientBearerToken(*token))
	if err := c.Connect(); err != nil {
		log.Fatalf("%s connect failed: %v", prefix, err)
	}
	defer func() { _ = c.Close() }()

	sub, err := eventsclient.Subscribe(context.Background(), c, eventsclient.SubscribeOptions{
		EventName:   *eventName,
		CallbackURL: callbackURL + "/webhook",
	})
	if err != nil {
		log.Fatalf("%s subscribe failed: %v", prefix, err)
	}
	defer sub.Stop()
	log.Printf("%s subscribed sub_id=%s — events route to %s/webhook", prefix, sub.ID(), callbackURL)

	receiver := &deliveryReceiver{secret: sub.Secret(), prefix: prefix}
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
	case err := <-serverErr:
		log.Printf("%s server error: %v", prefix, err)
	}
	_ = srv.Shutdown(context.Background())
}

// deliveryReceiver verifies and prints each webhook POST. Reuses the
// Standard Webhooks signature scheme the events lib emits — the same
// verification any production receiver would do.
type deliveryReceiver struct {
	secret string
	prefix string
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

	fmt.Printf("%s %s tenant=%-10s id=%s body=%s\n",
		time.Now().Format("15:04:05"),
		r.prefix,
		tagged.Tenant,
		id,
		string(body),
	)
	w.WriteHeader(http.StatusOK)
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
