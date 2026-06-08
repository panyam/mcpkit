// event-server is the MCP-facing tier of the whole-enchilada demo.
// It hosts the MCP Events extension methods (events/list, events/poll,
// events/subscribe, events/unsubscribe), receives synthetic events
// over HTTP from the push-server, and fans out to subscribers via push
// (SSE), poll, and webhook.
//
// Stage 1: in-memory stores, single replica, single tenant, anonymous
// principal. Stages 2-4 add Keycloak, Postgres, Redis, multi-replica,
// admin frontend, and OTel without changing this binary's public
// surface.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/server"
	gohttp "github.com/panyam/servicekit/http"
)

const eventStoreCap = 1000

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	bearer := flag.String("inject-bearer", os.Getenv("EVENT_INJECT_BEARER"),
		"shared secret required on /events/<name>/inject (empty = open)")
	tenant := flag.String("tenant", "default",
		"tenant id stamped onto event Meta in the demo / anonymous-auth path. When OAUTH_INTROSPECTION_URL or OAUTH_ISSUER is set, the per-subscription principal carries the tenant derived from token claims and this flag becomes a label only.")
	flag.Parse()

	chatSrc := events.NewHTTPSource[ChatMessageData](events.EventDef{
		Name:        "chat.message",
		Description: "Synthetic chat messages fed by the push-server tier.",
		Delivery:    []string{"push", "poll", "webhook"},
		Meta:        map[string]any{"category": "messaging", "tenant": *tenant},
		// Tenant-aware filter: with introspection auth wired, deliver
		// only to subscribers whose Claims.Tenant matches the event's
		// tenant tag. Stage-1 (no tenant tag on payload) and anonymous
		// mode (no Claims.Tenant) both fall through to "deliver to
		// everyone."
		Match: tenantMatchFunc,
	}, events.HTTPSourceConfig{
		Bearer: *bearer,
		YieldingOpts: []events.YieldingOption{
			events.WithMaxSize(1000),
		},
	})

	presenceSrc := events.NewHTTPSource[PresenceChangedData](events.EventDef{
		Name:        "presence.changed",
		Description: "Cursorless presence transitions fed by the push-server tier.",
		Delivery:    []string{"push", "webhook"},
		Meta:        map[string]any{"category": "presence", "tenant": *tenant},
		Match:       tenantMatchFunc,
	}, events.HTTPSourceConfig{
		Bearer: *bearer,
		YieldingOpts: []events.YieldingOption{
			events.WithoutCursors(),
		},
	})

	webhooks := events.NewWebhookRegistry(
		// Stage-1 escape: demo receivers run on loopback (compose network
		// resolves to private IPs); production-default SSRF guard would
		// reject. Production stages will wire a real allowlist.
		events.WithWebhookAllowPrivateNetworks(true),
	)

	srvOpts := []server.Option{
		server.WithSubscriptions(),
		server.WithListen(*addr),
	}

	// Auth posture (introspection > JWT > anonymous), matching the
	// discord / telegram demo pattern. Introspection wins when both
	// envs are set because its revocation-visibility story is the
	// load-bearing stage-2 walkthrough step.
	authPosture := "anonymous (--tenant=" + *tenant + " label only)"
	if validator := tryEnableIntrospection(); validator != nil {
		srvOpts = append(srvOpts, server.WithAuth(validator))
		authPosture = "introspection (" + os.Getenv("OAUTH_INTROSPECTION_URL") + ") — tenant derived from realm claim"
	}

	srv := server.NewServer(
		core.ServerInfo{Name: "whole-enchilada-event-server", Version: "0.1.0"},
		srvOpts...,
	)
	registerResources(srv, chatSrc)

	cfg := events.Config{
		Sources:  []events.EventSource{chatSrc, presenceSrc},
		Webhooks: webhooks,
		Server:   srv,
	}
	if !hasIntrospectionEnv() {
		// Anonymous-mode demo escape — explicit fallback principal so
		// `make demo` works without any auth provider. When auth IS
		// wired, the validator stamps the principal from claims and
		// anonymous subscribes are rejected per the events spec.
		cfg.UnsafeAnonymousPrincipal = "demo-principal"
	}
	events.Register(cfg)

	log.Printf("[event-server] tenant=%s auth=%s listening on %s", *tenant, authPosture, *addr)
	log.Printf("[event-server] inject endpoints: %s, %s", chatSrc.InjectPath(), presenceSrc.InjectPath())

	if err := srv.ListenAndServe(
		server.WithStreamableHTTP(true),
		server.WithSSE(true),
		server.WithEventStore(gohttp.NewMemoryEventStore(eventStoreCap)),
		server.WithMux(func(mux *http.ServeMux) {
			// HTTPSource inject endpoints alongside MCP at /mcp.
			mux.Handle(chatSrc.InjectPath(), chatSrc.Handler())
			mux.Handle(presenceSrc.InjectPath(), presenceSrc.Handler())
			mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("ok"))
			})
		}),
	); err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}

// tryEnableIntrospection wires a multi-realm IntrospectionValidator
// when OAUTH_INTROSPECTION_URLS is set, otherwise returns nil so the
// caller falls back to the next posture in the chain. Recognized env
// vars:
//
//   OAUTH_INTROSPECTION_URLS REQUIRED. Comma-separated list of N
//                            introspection endpoints — one per realm
//                            the event-server accepts tokens from. A
//                            token is accepted if ANY listed realm's
//                            introspection returns active=true.
//                            Single-realm deployments pass exactly one
//                            URL.
//   OAUTH_CLIENT_ID          REQUIRED. Client ID used to authenticate
//                            to every realm's introspection endpoint
//                            via client_secret_basic. The same client
//                            ID is registered in every realm in the
//                            demo; production deployments may need
//                            per-realm IDs (file an issue if needed).
//   OAUTH_CLIENT_SECRET      REQUIRED. Same as above — shared across
//                            realms for demo simplicity.
//   OAUTH_CACHE_TTL          Optional duration. Default 30s. Set to 0
//                            to disable caching (every request hits
//                            the AS — the load-bearing knob for the
//                            "token revocation" walkthrough step).
//
// The previous OAUTH_INTROSPECTION_URL singular-form env var is no
// longer recognized — single-realm deployments now pass exactly one
// URL through OAUTH_INTROSPECTION_URLS.
func tryEnableIntrospection() *MultiRealmIntrospectionValidator {
	raw := os.Getenv("OAUTH_INTROSPECTION_URLS")
	if raw == "" {
		return nil
	}
	cacheTTL := 30 * time.Second
	if rawTTL := os.Getenv("OAUTH_CACHE_TTL"); rawTTL != "" {
		if d, err := time.ParseDuration(rawTTL); err == nil {
			cacheTTL = d
		}
	}
	return buildMultiRealmValidator(realmConfig{
		URLs:         strings.Split(raw, ","),
		ClientID:     os.Getenv("OAUTH_CLIENT_ID"),
		ClientSecret: os.Getenv("OAUTH_CLIENT_SECRET"),
		CacheTTL:     cacheTTL,
	})
}

// hasIntrospectionEnv reports whether OAUTH_INTROSPECTION_URLS is set.
// Used to decide whether to fall back to the UnsafeAnonymousPrincipal
// demo escape on events.Config.
func hasIntrospectionEnv() bool {
	return os.Getenv("OAUTH_INTROSPECTION_URLS") != ""
}
