package main

import (
	"flag"
	"os"

	"github.com/panyam/demokit"
)

func main() {
	// Strip demokit's dispatcher flags (--doc, --mode, --tui, --from,
	// --non-interactive, --variant) from os.Args before our own
	// flag.Parse — otherwise `make readme` (which passes --doc=md)
	// crashes our parser. See demokit.FilterArgs godoc.
	filtered := demokit.FilterArgs(os.Args[1:])
	flag.CommandLine.Parse(filtered)

	serverURL := flag.String("url", envOr("MCP_URL", "http://localhost:8080"),
		"event-server URL (default nginx frontdoor in the compose stack)")
	receiverURL := flag.String("receiver", envOr("RECEIVER_URL", "http://localhost:9090"),
		"receiver URL the walkthrough subscribes its webhook to")
	_ = flag.CommandLine.Parse(filtered)

	runDemo(*serverURL, *receiverURL)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
