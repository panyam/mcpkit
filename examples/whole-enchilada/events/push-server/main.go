package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	common "github.com/panyam/mcpkit/examples/common"
	commonotel "github.com/panyam/mcpkit/examples/common/otel"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
)

// pushed counters surface via the admin HTTP port so `pusher.whole-enchilada`
// answers something useful from the network — a tiny status view of how many
// events each feeder has pushed and the last push outcome.
var (
	chatPushed     atomic.Int64
	presencePushed atomic.Int64
	startedAt      = time.Now()
)

func main() {
	target := flag.String("target", envOr("EVENT_SERVER_URL", "http://event-server.whole-enchilada"),
		"event-server URL to push events into")
	bearer := flag.String("bearer", os.Getenv("EVENT_INJECT_BEARER"),
		"shared secret matching the event-server's HTTPSourceConfig.Bearer")
	adminAddr := flag.String("admin-addr", ":9091",
		"admin HTTP listen address (/healthz + /status)")
	chatEvery := flag.Duration("chat-every", 2*time.Second, "cadence between synthetic chat messages")
	presenceEvery := flag.Duration("presence-every", 5*time.Second, "cadence between synthetic presence transitions")
	tenants := flag.String("tenants", envOr("PUSH_TENANTS", "asgard,babylon,camelot"),
		"comma-separated tenant tags; each emitted event rotates through them in order so subscribers from one tenant only see ~1/N of events. Empty string = no tag (stage-1 single-tenant mode)")
	tel := common.RegisterTelemetryFlags(flag.CommandLine)
	flag.Parse()

	tenantTags := splitNonEmpty(*tenants)

	// OTel telemetry. See the event-server's main.go comment block for
	// the EXPORTER selector semantics — auto mode (default) means
	// "best-effort OTLP with silent Noop fallback" so `just demo-up`
	// works whether docker/observability is running or not.
	_, shutdown, err := commonotel.SetupTelemetry(context.Background(),
		commonotel.WithExporter(*tel.Exporter),
		commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
		commonotel.WithServiceName("whole-enchilada-push-server"),
	)
	if err != nil {
		log.Fatalf("commonotel.SetupTelemetry: %v", err)
	}
	defer shutdown(context.Background())

	pusher := eventsclient.NewPusher(*target, *bearer)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Printf("[push-server] target=%s admin=%s chat-every=%s presence-every=%s tenants=%v",
		*target, *adminAddr, *chatEvery, *presenceEvery, tenantTags)

	go runAdmin(*adminAddr)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); runChatFeeder(ctx, pusher, *chatEvery, tenantTags) }()
	go func() { defer wg.Done(); runPresenceFeeder(ctx, pusher, *presenceEvery, tenantTags) }()
	wg.Wait()
	log.Printf("[push-server] shutdown")
}

// splitNonEmpty returns the comma-separated parts of s with whitespace
// trimmed and empty entries dropped. Returns nil for an empty / all-
// whitespace input — runChatFeeder and runPresenceFeeder treat nil as
// "no tenant tag" and emit untagged events (stage-1 mode).
func splitNonEmpty(s string) []string {
	var out []string
	for _, raw := range strings.Split(s, ",") {
		if v := strings.TrimSpace(raw); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func runAdmin(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"uptime_seconds":  int(time.Since(startedAt).Seconds()),
			"chat_pushed":     chatPushed.Load(),
			"presence_pushed": presencePushed.Load(),
		})
	})
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("[push-server] admin server failed: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
