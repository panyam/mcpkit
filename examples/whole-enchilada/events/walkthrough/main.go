package main

import (
	"flag"
	"os"

	"github.com/panyam/demokit"
)

func main() {
	// Canonical example main shape: declare our own flags FIRST, then
	// call Parse() ONCE with demokit.FilterArgs stripping demokit's
	// dispatcher flags. FilterArgs returns a new []string without
	// touching os.Args, so demo.Execute() inside runDemo still sees
	// --tui / --doc / --non-interactive / --mode / --from / --variant
	// in os.Args and dispatches on them.
	//
	// Telling FilterArgs about our value-flags (--url) is load-bearing
	// — without those extras, FilterArgs treats the argument after our
	// flag as a positional and trips up the parser when it sees an
	// unexpected token.
	serverURL := flag.String("url", envOr("MCP_URL", "http://localhost:9090"),
		"event-server URL (default nginx frontdoor in the compose stack)")
	flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:],
		demokit.ValueFlag("--url"),
	))

	runDemo(*serverURL)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
