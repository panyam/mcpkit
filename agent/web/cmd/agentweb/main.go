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

	app, err := buildApp(*configPath, *demo)
	if err != nil {
		log.Fatalf("agentweb: %v", err)
	}
	defer app.Close()

	srv := &http.Server{Addr: *addr, Handler: web.Handler(app)}
	fmt.Fprintf(os.Stderr, "agentweb serving at http://%s/ (Connect: /%s, Ctrl-C to stop)\n", *addr, "mcpkit.agentweb.v1.HostService")
	if err := skhttp.ListenAndServeGraceful(srv); err != nil {
		log.Fatalf("agentweb: serve: %v", err)
	}
}

// buildApp builds the one shared App. In demo mode it wires the offline demo
// provider; otherwise it loads the host config. The web surface renders in the
// browser off the event stream, so the App's own terminal renderer is discarded
// (output to io.Discard) — events reach clients through Subscribe / Watch.
func buildApp(configPath string, demo bool) (*host.App, error) {
	if demo {
		cfg := &host.Config{Model: host.ModelConfig{BaseURL: "http://demo", Model: "agentweb-demo"}}
		return host.NewApp(cfg, io.Discard, strings.NewReader(""), host.WithProvider(demoProvider{}))
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
