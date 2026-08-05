// Command agentweb serves the agent host over the Connect web bridge — the
// browser analogue of the terminal agentchat, and a thin surface over
// agent/host.App exactly as agentchat is (issue 1196). It loads a host config,
// builds one App, and serves the HostService plus the placeholder shell on one
// servicekit listener. The full frontend is E4.
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
	flag.Parse()

	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "agentweb: --config <path> is required (a host config JSON; see agent/host config)")
		os.Exit(2)
	}
	cfg, err := host.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("agentweb: load config: %v", err)
	}

	// The web surface renders in the browser off the event stream, so the App's
	// own terminal renderer is discarded here (no observer, output to Discard) —
	// events reach clients through Subscribe / Watch, not stdout.
	app, err := host.NewApp(cfg, io.Discard, strings.NewReader(""))
	if err != nil {
		log.Fatalf("agentweb: build app: %v", err)
	}
	defer app.Close()

	srv := &http.Server{Addr: *addr, Handler: web.Handler(app)}
	fmt.Fprintf(os.Stderr, "agentweb serving at http://%s/ (Connect: /%s, Ctrl-C to stop)\n", *addr, "mcpkit.agentweb.v1.HostService")
	if err := skhttp.ListenAndServeGraceful(srv); err != nil {
		log.Fatalf("agentweb: serve: %v", err)
	}
}
