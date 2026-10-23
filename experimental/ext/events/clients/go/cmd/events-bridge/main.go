// Command events-bridge runs the bridge from package bridge as a daemon: an
// MCP server at /mcp for the host (Claude Code connects to it over HTTP), and
// a webhook receiver at /hooks/ for the upstream events servers. Standing
// rules live in the state file, so they survive the chat session that made
// them and a restart of the daemon.
//
//	events-bridge -upstream ops=http://localhost:8080/mcp -dry-run
//	claude mcp add --transport http events-bridge http://localhost:8790/mcp
//
// A bearer token for an upstream comes from EVENTS_BRIDGE_TOKEN_<NAME>, with
// the name upper-cased and '-' as '_'.
//
// It lives in this module rather than the root one so the root mcpkit module
// does not have to depend on experimental/.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
	"github.com/panyam/mcpkit/experimental/ext/events/clients/go/bridge"
	"github.com/panyam/mcpkit/server"
)

type upstreamFlags []string

func (u *upstreamFlags) String() string     { return strings.Join(*u, ",") }
func (u *upstreamFlags) Set(v string) error { *u = append(*u, v); return nil }

func main() {
	var ups upstreamFlags
	flag.Var(&ups, "upstream", "name=url of an events server; repeatable")
	listen := flag.String("listen", ":8790", "listen address for /mcp and /hooks/")
	publicURL := flag.String("public-url", "http://localhost:8790", "base URL upstream servers POST deliveries to")
	state := flag.String("state", "events-bridge.json", "file holding watches, their secrets and cursors (created 0600)")
	claudeBin := flag.String("claude", "claude", "Claude Code binary for headless runs")
	allowed := flag.String("allowed-tools", "Read,Grep,Glob", "comma-separated tools every headless run gets; keep these read-only")
	outward := flag.String("outward-tools", "", "comma-separated outward-facing tools, added to a run only when approved")
	approveOutward := flag.Bool("approve-outward", false, "approve -outward-tools for every run, unattended")
	mcpConfig := flag.String("mcp-config", "", "MCP config for headless runs (passed with --strict-mcp-config)")
	dryRun := flag.Bool("dry-run", false, "print each run's prompt and arguments instead of starting claude")
	flag.Parse()

	if len(ups) == 0 {
		log.Fatal("at least one -upstream name=url is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var upstreams []bridge.Upstream
	for _, spec := range ups {
		name, url, ok := strings.Cut(spec, "=")
		if !ok {
			log.Fatalf("-upstream %q: want name=url", spec)
		}
		var opts []client.ClientOption
		if tok := os.Getenv("EVENTS_BRIDGE_TOKEN_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))); tok != "" {
			opts = append(opts, client.WithClientBearerToken(tok))
		}
		c := client.NewClient(url, core.ClientInfo{Name: "events-bridge", Version: "0.1.0"}, opts...)
		if err := c.Connect(ctx); err != nil {
			log.Fatalf("connect %s (%s): %v", name, url, err)
		}
		defer c.Close()
		upstreams = append(upstreams, bridge.Upstream{Name: name, Client: c})
	}

	sink := &bridge.HeadlessClaudeSink{
		Command:      *claudeBin,
		AllowedTools: splitList(*allowed),
		OutwardTools: splitList(*outward),
		OnResult: func(d eventsclient.Delivery, out []byte, err error) {
			if err != nil {
				log.Printf("run for %s %s failed: %v", d.QualifiedName(), d.Event.EventID, err)
				return
			}
			log.Printf("run for %s %s: %s", d.QualifiedName(), d.Event.EventID, strings.TrimSpace(string(out)))
		},
		OnResumeFailed: func(d eventsclient.Delivery, err error) {
			log.Printf("run for %s %s: session %s is gone (%v), starting fresh", d.QualifiedName(), d.Event.EventID, d.Session, err)
		},
	}
	if *approveOutward {
		sink.Approver = bridge.ApproverFunc(func(context.Context, eventsclient.Delivery, []string) (bool, error) { return true, nil })
	}
	if *mcpConfig != "" {
		// Absolute, because a run that resumes a session starts in that
		// session's directory rather than this one.
		cfgPath, err := filepath.Abs(*mcpConfig)
		if err != nil {
			log.Fatalf("mcp-config: %v", err)
		}
		sink.ExtraArgs = []string{"--mcp-config", cfgPath, "--strict-mcp-config"}
	}
	if *dryRun {
		sink.Runner = printRunner{}
	}

	b, err := bridge.New(bridge.Config{
		Upstreams: upstreams,
		PublicURL: strings.TrimSuffix(*publicURL, "/"),
		Sink:      sink,
		Store:     bridge.NewFileStore(*state),
	})
	if err != nil {
		log.Fatal(err)
	}
	defer b.Close()

	srv := server.NewServer(core.ServerInfo{Name: "events-bridge", Version: "0.1.0"})
	b.Register(srv)
	mux := http.NewServeMux()
	mux.Handle(bridge.HookPathPrefix, b.Handler())
	mux.Handle("/", srv.Handler(server.WithStreamableHTTP(true)))
	hs := &http.Server{Addr: *listen, Handler: mux}

	// Resume after the listener is up: each resubscribe's endpoint
	// verification POSTs to /hooks/ while Start is still running.
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	if err := b.Start(ctx); err != nil {
		log.Fatal(err)
	}
	log.Printf("events-bridge: MCP at %s/mcp, webhooks at %s%s, %d watch(es) resumed", *publicURL, *publicURL, bridge.HookPathPrefix, len(b.List()))

	select {
	case err := <-errc:
		log.Fatal(err)
	case <-ctx.Done():
		_ = hs.Shutdown(context.Background())
	}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// printRunner stands in for claude under -dry-run.
type printRunner struct{}

func (printRunner) Run(_ context.Context, dir, name string, args []string, stdin []byte) ([]byte, error) {
	if dir != "" {
		fmt.Printf("--- in %s\n", dir)
	}
	fmt.Printf("--- would run: %s %s\n%s\n---\n", name, strings.Join(args, " "), stdin)
	return []byte("dry run"), nil
}
