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
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/panyam/mcpkit/core"
	common "github.com/panyam/mcpkit/examples/common"
	commonotel "github.com/panyam/mcpkit/examples/common/otel"
	"github.com/panyam/mcpkit/experimental/ext/events"
	extauth "github.com/panyam/mcpkit/ext/auth"
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
	tel := common.RegisterTelemetryFlags(flag.CommandLine)
	flag.Parse()

	// Set up OTel TracerProvider before constructing the server so
	// every span the server emits flows through. Exporter selector
	// honors --exporter / EXPORTER env (auto | stdout | otlp | "").
	// auto = best-effort OTLP with silent Noop fallback when the
	// observability stack isn't reachable — keeps `make demo-up`
	// working whether docker/observability is up or not.
	tp, shutdown, err := commonotel.SetupTelemetry(context.Background(),
		commonotel.WithExporter(*tel.Exporter),
		commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
		commonotel.WithServiceName("whole-enchilada-event-server"),
	)
	if err != nil {
		log.Fatalf("commonotel.SetupTelemetry: %v", err)
	}
	defer shutdown(context.Background())

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
		// SEP-414 P6 follow-up: emit events.webhook.deliver spans
		// around each outbound POST so Peter's demo shows the full
		// chain (client tools/call → server dispatch → webhook
		// delivery) as one row in Tempo.
		events.WithWebhookTracerProvider(tp),
	)

	srvOpts := []server.Option{
		server.WithSubscriptions(),
		server.WithListen(*addr),
		server.WithTracerProvider(tp),
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

	// Back-Channel Logout receivers — one handler per realm, each
	// mounted at /backchannel-logout/<realm>. When Keycloak revokes a
	// session, it POSTs a signed logout_token to the URL we register
	// on the realm's mcp-events-poller client. Each handler's
	// listener calls webhooks.TerminateBySession so matching
	// subscriptions get {type:terminated} envelopes. See mcpkit issue
	// 709 for the full design.
	bclHandlers := buildBCLHandlers(webhooks)

	log.Printf("[event-server] tenant=%s auth=%s listening on %s", *tenant, authPosture, *addr)
	log.Printf("[event-server] inject endpoints: %s, %s", chatSrc.InjectPath(), presenceSrc.InjectPath())
	for path := range bclHandlers {
		log.Printf("[event-server] BCL receiver mounted: %s", path)
	}

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
			for path, h := range bclHandlers {
				mux.Handle(path, h)
			}
		}),
	); err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}

// buildBCLHandlers wires one BackChannelLogoutHandler per realm
// derived from OAUTH_INTROSPECTION_URLS, with the listener routed to
// webhooks.TerminateBySession. Returns map[mountPath]handler — empty
// when introspection isn't configured (no Keycloak, no BCL).
//
// Required env vars (in addition to the introspection-mode envs):
//
//	OAUTH_ISSUER_BASE   Public-facing scheme://host:port of the AS,
//	                    matching what appears in token iss claims.
//	                    The full per-realm issuer is computed as
//	                    OAUTH_ISSUER_BASE + "/realms/" + <realm>.
//	                    Defaults to http://localhost:8180 (the demo's
//	                    Keycloak hostname).
//
// Audience for every realm's handler defaults to OAUTH_CLIENT_ID
// (mcp-event-server) — the same client_id that authenticates the
// introspection call. Keycloak emits BCL logout_tokens with aud =
// the client_id whose backchannel_logout_uri was hit, which matches.
func buildBCLHandlers(webhooks *events.WebhookRegistry) map[string]*extauth.BackChannelLogoutHandler {
	raw := os.Getenv("OAUTH_INTROSPECTION_URLS")
	if raw == "" {
		return nil
	}
	issuerBase := os.Getenv("OAUTH_ISSUER_BASE")
	if issuerBase == "" {
		issuerBase = "http://localhost:8180"
	}
	audience := os.Getenv("OAUTH_CLIENT_ID")
	if audience == "" {
		return nil
	}
	out := make(map[string]*extauth.BackChannelLogoutHandler)
	for _, introspectURL := range strings.Split(raw, ",") {
		introspectURL = strings.TrimSpace(introspectURL)
		realm := realmFromKeycloakURL(introspectURL)
		if realm == "" {
			continue
		}
		// Derive JWKS URL from the introspection URL (same realm,
		// just swap the protocol suffix).
		jwksURL := strings.Replace(introspectURL, "/token/introspect", "/certs", 1)
		h, err := extauth.NewBackChannelLogoutHandler(extauth.BackChannelLogoutConfig{
			Issuer:   issuerBase + "/realms/" + realm,
			Audience: audience,
			JWKSURL:  jwksURL,
		})
		if err != nil {
			log.Printf("[event-server] BCL handler for %s skipped: %v", realm, err)
			continue
		}
		// Capture realm for the log line — listeners run synchronously
		// inside the BCL POST handler so log emission is in the same
		// span as the AS-initiated request.
		realmCopy := realm
		h.RegisterListener(func(_ context.Context, sub, sid string) {
			killed := webhooks.TerminateBySession(sid, events.ControlError{
				Code:    -32012,
				Message: "session revoked by authorization server",
			})
			if killed == 0 && sub != "" {
				// Fall back to subject-scoped termination when the
				// logout_token only carried sub (no sid).
				killed = webhooks.TerminateBySubject(sub, events.ControlError{
					Code:    -32012,
					Message: "subject revoked by authorization server",
				})
			}
			log.Printf("[event-server] BCL fire: realm=%s sub=%s sid=%s killed=%d",
				realmCopy, sub, sid, killed)
		})
		out["/backchannel-logout/"+realm] = h
	}
	return out
}

// tryEnableIntrospection wires an iss-routing IntrospectionValidator
// when OAUTH_INTROSPECTION_URLS is set, otherwise returns nil so the
// caller falls back to the next posture in the chain. Recognized env
// vars:
//
//   OAUTH_INTROSPECTION_URLS REQUIRED. Comma-separated list of N
//                            Keycloak introspection endpoints — one
//                            per realm the event-server accepts
//                            tokens from. Each URL's
//                            /realms/<realm>/ segment is parsed as
//                            the routing key; the inbound JWT's iss
//                            picks which child to delegate to.
//                            Single-realm deployments pass exactly
//                            one URL.
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
func tryEnableIntrospection() *IssRoutingIntrospectionValidator {
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
	return buildIssRoutingValidator(realmConfig{
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
