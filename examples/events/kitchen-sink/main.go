// kitchen-sink: single-process showcase of the per-subscription
// delivery surface. See package docs in events.go.
//
// Two-process pattern:
//
//	Terminal 1:  make serve           # the events server
//	Terminal 2:  make demo            # demokit walkthrough (--tui)
//	             make demo-test       # non-interactive
//	             make readme          # regenerate WALKTHROUGH.md
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/core"
	common "github.com/panyam/mcpkit/examples/common"
	commonotel "github.com/panyam/mcpkit/examples/common/otel"
	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/server"
	gohttp "github.com/panyam/servicekit/http"
)

func decodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

const (
	eventStoreCap        = 1000
	defaultChatEvery     = 2 * time.Second
	defaultAlertEvery    = 4 * time.Second
	defaultPresenceEvery = 3 * time.Second

	// quotaCap is the per-principal subscription cap configured for
	// chat.message; the walkthrough's quota step subscribes three
	// times and shows the third returns -32013.
	quotaCap = 2
)

func main() {
	for _, arg := range os.Args[1:] {
		if strings.TrimSpace(arg) == "--serve" {
			serve()
			return
		}
	}
	runDemo()
}

func serve() {
	addr := flag.String("addr", ":8080", "listen address")
	chatEvery := flag.Duration("chat-every", defaultChatEvery, "synthetic chat feeder cadence")
	alertEvery := flag.Duration("alert-every", defaultAlertEvery, "synthetic alert feeder cadence")
	presenceEvery := flag.Duration("presence-every", defaultPresenceEvery, "synthetic presence feeder cadence")
	tel := common.RegisterTelemetryFlags(flag.CommandLine)
	flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:],
		demokit.BoolFlag("--serve"),
		demokit.ValueFlag("--addr"),
		demokit.ValueFlag("--chat-every"),
		demokit.ValueFlag("--alert-every"),
		demokit.ValueFlag("--presence-every"),
		demokit.ValueFlag("--exporter"),
		demokit.ValueFlag("--otlp-endpoint"),
	))

	// SEP-414 P6 observability wiring (issue 667). Default selector
	// ("") returns a Noop TracerProvider — zero overhead. Operators
	// opt in via --exporter / EXPORTER env. See examples/CONVENTIONS.md
	// § Telemetry wiring for the four-value selector matrix.
	tp, shutdown, err := commonotel.SetupTelemetry(context.Background(),
		commonotel.WithExporter(*tel.Exporter),
		commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
		commonotel.WithServiceName("kitchen-sink-events"),
	)
	if err != nil {
		log.Fatalf("commonotel.SetupTelemetry: %v", err)
	}
	defer shutdown(context.Background())

	wired := buildServer(*addr, tp)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runChatFeeder(ctx, wired.chatYield, *chatEvery)
	go runAlertFeeder(ctx, wired.alertYield, *alertEvery)
	go runPresenceFeeder(ctx, wired.idx, wired.registry, *presenceEvery)

	log.Printf("[server] kitchen-sink listening on %s (MCP at /mcp; /inject for deterministic events)", *addr)

	if err := wired.srv.ListenAndServe(
		server.WithStreamableHTTP(true),
		server.WithSSE(true),
		server.WithEventStore(gohttp.NewMemoryEventStore(eventStoreCap)),
		server.WithMux(func(mux *http.ServeMux) {
			mux.HandleFunc("POST /inject", injectHandlerFor(wired))
		}),
	); err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}

// wiredServer bundles every handle the feeders + walkthrough + e2e
// tests need from buildServer. Yield closures must be returned from
// NewYieldingSource at construction time — the source itself does not
// expose a public Yield method.
type wiredServer struct {
	srv         *server.Server
	chatSrc     *events.YieldingSource[ChatMessageData]
	chatYield   func(context.Context, ChatMessageData) error
	alertSrc    *events.YieldingSource[AlertData]
	alertYield  func(context.Context, AlertData) error
	presenceSrc *events.YieldingSource[PresenceChangedData]
	idx         *events.SubscriptionIndex
	registry    *watchListRegistry
}

// buildServer wires the three sources, the watch-list registry, the
// SubscriptionIndex (passed to events.Register so EmitToSubscription
// can find subs), and the Quota cap. Returned so e2e_test.go can
// stand up the same shape without spawning ListenAndServe.
//
// tp opts the dispatch spine + webhook delivery path into SEP-414
// instrumentation. Nil or core.NoopTracerProvider{} = zero overhead.
// Tests pass nil; the serve() path passes the configured TracerProvider.
func buildServer(addr string, tp core.TracerProvider) *wiredServer {
	registry := newWatchListRegistry()

	chatSrc, chatYield := events.NewYieldingSource[ChatMessageData](chatEventDef(), events.WithMaxSize(eventStoreCap))
	alertSrc, alertYield := events.NewYieldingSource[AlertData](alertEventDef(), events.WithMaxSize(eventStoreCap))
	// Presence is fed by the OnSubscribe-provisioned upstream loop,
	// not by yielding into this source — but we still need a source
	// so events.Register sees the def and wires events/list +
	// events/subscribe routing. The library's broadcast path will
	// remain idle for this source; everything goes via
	// EmitToSubscription.
	presenceSrc, _ := events.NewYieldingSource[PresenceChangedData](presenceEventDef(registry), events.WithoutCursors())

	webhookOpts := []events.WebhookOption{
		events.WithWebhookAllowPrivateNetworks(true),
	}
	if tp != nil {
		webhookOpts = append(webhookOpts, events.WithWebhookTracerProvider(tp))
	}
	webhooks := events.NewWebhookRegistry(webhookOpts...)
	idx := events.NewSubscriptionIndex()
	quota := events.NewQuota(events.WithMaxSubscriptionsPerPrincipal("chat.message", quotaCap))

	// Canonical baseline (WithListen + the color logger wired to both
	// transport request logging and dispatch middleware) per
	// examples/CONVENTIONS.md § serve-srv-listenandserve. Mirrors the
	// discord precedent — kitchen-sink can't use `common.RunServer`
	// because of the three goroutine feeders + custom /inject mux, but
	// the per-option baseline still applies.
	srvOpts := common.MCPServerOptions(addr, "[mcp] ")
	srvOpts = append(srvOpts, server.WithSubscriptions())
	if tp != nil {
		srvOpts = append(srvOpts, server.WithTracerProvider(tp))
	}
	srv := server.NewServer(
		core.ServerInfo{Name: "kitchen-sink-events", Version: "0.1.0"},
		srvOpts...,
	)
	registerResources(srv, chatSrc, alertSrc)
	events.Register(events.Config{
		Sources:                  []events.EventSource{chatSrc, alertSrc, presenceSrc},
		Webhooks:                 webhooks,
		Server:                   srv,
		SubscriptionIndex:        idx,
		Quota:                    quota,
		UnsafeAnonymousPrincipal: "demo-principal",
	})
	return &wiredServer{
		srv: srv, chatSrc: chatSrc, chatYield: chatYield,
		alertSrc: alertSrc, alertYield: alertYield,
		presenceSrc: presenceSrc, idx: idx, registry: registry,
	}
}

// injectHandlerFor returns an HTTP handler that drives any of the
// three sources by an `event` query parameter. The walkthrough calls
// it to land deterministic events for the Match / Transform /
// presence steps without waiting for the random feeders.
func injectHandlerFor(w *wiredServer) http.HandlerFunc {
	type chatBody struct {
		Channel, Sender, Text string
	}
	type alertBody struct {
		Severity, Service, Reporter, Message string
	}
	type presenceBody struct {
		User, State string
	}

	return func(rw http.ResponseWriter, r *http.Request) {
		eventName := r.URL.Query().Get("event")
		switch eventName {
		case "chat.message":
			var body chatBody
			if err := decodeJSON(r, &body); err != nil {
				http.Error(rw, err.Error(), http.StatusBadRequest)
				return
			}
			_ = injectChat(r.Context(), w.chatYield, body.Channel, body.Sender, body.Text)
		case "alert.fired":
			var body alertBody
			if err := decodeJSON(r, &body); err != nil {
				http.Error(rw, err.Error(), http.StatusBadRequest)
				return
			}
			_ = injectAlert(r.Context(), w.alertYield, body.Severity, body.Service, body.Reporter, body.Message)
		case "presence.changed":
			var body presenceBody
			if err := decodeJSON(r, &body); err != nil {
				http.Error(rw, err.Error(), http.StatusBadRequest)
				return
			}
			emitPresence(w.idx, w.registry, PresenceChangedData{
				User:      body.User,
				State:     body.State,
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			})
		default:
			http.Error(rw, "unknown event (want chat.message, alert.fired, presence.changed)", http.StatusBadRequest)
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(`{"status":"ok"}`))
	}
}
