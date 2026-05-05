package common

import (
	"os"

	"github.com/panyam/demokit"
	"github.com/panyam/demokit/tui"
)

// DefaultServerURL is the canonical mcpkit example server URL.
// Walkthroughs default to this; override via --url flag or
// MCPKIT_SERVER_URL env var (see ServerURL).
const DefaultServerURL = "http://localhost:8080"

// ServerURLEnv is the env var name that overrides the default server URL
// when no --url flag is passed.
const ServerURLEnv = "MCPKIT_SERVER_URL"

// ServerURL returns the MCP server endpoint a walkthrough should connect
// to. Precedence (highest first):
//
//  1. --url <addr> on the command line
//  2. $MCPKIT_SERVER_URL env var
//  3. DefaultServerURL ("http://localhost:8080")
//
// Examples just call common.ServerURL() — no per-example default to drift.
func ServerURL() string {
	for i, arg := range os.Args[1:] {
		if arg == "--url" && i+2 < len(os.Args) {
			return os.Args[i+2]
		}
	}
	if u := os.Getenv(ServerURLEnv); u != "" {
		return u
	}
	return DefaultServerURL
}

// SetupRenderer wires the Bubble Tea TUI renderer onto demo if `--tui`
// was passed on the command line. No-op otherwise (demokit's default
// terminal renderer stays in place).
func SetupRenderer(demo *demokit.Demo) {
	if demokit.IsTUI() {
		demo.WithRenderer(tui.New())
	}
}
