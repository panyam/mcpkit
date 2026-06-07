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
		"tenant id stamped onto subscriptions and served in events/list metadata; stage 2 will derive from Keycloak token claims")
	flag.Parse()

	chatSrc := events.NewHTTPSource[ChatMessageData](events.EventDef{
		Name:        "chat.message",
		Description: "Synthetic chat messages fed by the push-server tier.",
		Delivery:    []string{"push", "poll", "webhook"},
		Meta:        map[string]any{"category": "messaging", "tenant": *tenant},
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

	srv := server.NewServer(
		core.ServerInfo{Name: "whole-enchilada-event-server", Version: "0.1.0"},
		server.WithSubscriptions(),
		server.WithListen(*addr),
	)
	registerResources(srv, chatSrc)
	events.Register(events.Config{
		Sources:                  []events.EventSource{chatSrc, presenceSrc},
		Webhooks:                 webhooks,
		Server:                   srv,
		UnsafeAnonymousPrincipal: "demo-principal", // stage 2 replaces with Keycloak-derived principal
	})

	log.Printf("[event-server] tenant=%s listening on %s", *tenant, *addr)
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
