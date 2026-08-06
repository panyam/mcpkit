// Command agentweb serves the agent host over the Connect web bridge — the
// browser analogue of the terminal agentchat, and a thin surface over
// agent/host.App exactly as agentchat is (issue 1196). It loads a host config,
// builds one App, and serves the HostService plus the DockView + Solid frontend
// (issue 1197) on one servicekit listener.
//
// `--demo` skips the config and builds an App over an offline, inexhaustible
// demo provider, so `go run ./cmd/agentweb --demo` serves a working, streaming
// surface with no model or MCP server to configure — the run/screenshot target.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	skhttp "github.com/panyam/servicekit/http"

	"github.com/panyam/mcpkit/agent/host"
	web "github.com/panyam/mcpkit/agent/web"
)

func main() {
	addr := flag.String("addr", ":8090", "address to listen on")
	configPath := flag.String("config", "", "path to a host config JSON (servers, model, policies)")
	demo := flag.Bool("demo", false, "run over an offline streaming demo provider (no config needed)")
	flag.Parse()

	// One factory builds a fresh App per web session from the same config, so a
	// CreateSession over the wire mints an independent conversation. The
	// SessionManager holds them all; its default session (created at startup)
	// backs an empty session_id, keeping the single-surface flow unchanged.
	factory := func(ctx context.Context) (*host.App, error) { return buildApp(*configPath, *demo) }
	mgr := web.NewSessionManager(factory)
	defaultApp, err := factory(context.Background())
	if err != nil {
		log.Fatalf("agentweb: %v", err)
	}
	mgr.SetDefault(defaultApp)
	defer mgr.CloseAll()

	srv := &http.Server{Addr: *addr, Handler: web.HandlerWithSessions(mgr)}
	fmt.Fprintf(os.Stderr, "agentweb serving at http://%s/ (Connect: /%s, Ctrl-C to stop)\n", *addr, "mcpkit.agentweb.v1.HostService")
	if err := skhttp.ListenAndServeGraceful(srv); err != nil {
		log.Fatalf("agentweb: serve: %v", err)
	}
}

// buildApp builds one App: the factory the SessionManager calls for the default
// session and every CreateSession. In demo mode it wires the offline demo
// provider; otherwise it loads the host config. The web surface renders in the
// browser off the event stream, so the App's own terminal renderer is discarded
// (output to io.Discard) — events reach clients through Subscribe / Watch.
func buildApp(configPath string, demo bool) (*host.App, error) {
	if demo {
		// Wire two delegate personas + a low offload threshold so a demo turn
		// exercises the whole observability surface: the scripted provider fans
		// out to researcher/analyst (sub-agent tree + tool calls), the
		// researcher's long reply comes back offloaded (tool inspector stub),
		// and reported usage moves the budget gauges.
		cfg := &host.Config{
			Model: host.ModelConfig{BaseURL: "http://demo", Model: "agentweb-demo"},
			SubAgents: []host.SubAgentConfig{
				{Name: "researcher", Description: "gathers background on a topic", Instructions: "You are a research sub-agent."},
				{Name: "analyst", Description: "assesses risks and trade-offs", Instructions: "You are a risk-analysis sub-agent."},
			},
			Offload: &host.OffloadConfig{ThresholdBytes: 300, PreviewLen: 120},
			// Gate the researcher delegate behind an approval "ask" (everything
			// else auto-runs) so a demo turn raises exactly one elicitation. The
			// prompt broadcasts to every surface; answering it on any surface
			// retracts it on the others, which is what this build (issue 1199)
			// exists to demonstrate.
			Approval: &host.ApprovalConfig{Mode: "allow", Rules: map[string]string{"researcher": "ask"}},
		}
		return host.NewApp(cfg, io.Discard, strings.NewReader(""),
			host.WithProvider(demoProvider{}),
			host.WithElicitationUI(demoElicitUI),
		)
	}
	if configPath == "" {
		return nil, fmt.Errorf("--config <path> is required (or pass --demo for the offline streaming demo)")
	}
	cfg, err := host.LoadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return host.NewApp(cfg, io.Discard, strings.NewReader(""))
}
