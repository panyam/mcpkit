package common

import (
	"os"

	"github.com/panyam/demokit"
	"github.com/panyam/demokit/notebookbridge"
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

// SetupRenderer wires the renderer matching demokit's --mode (or the
// bare --tui / --note aliases):
//
//	--mode=tui      / --tui  → tui.New()            (Lipgloss boxes)
//	--mode=notebook / --note → notebookbridge.New() (cell-based UI)
//	default                  → demokit's PlainRenderer
//
// Every example's `make note` target shells out to `--note`, which
// demokit.Mode() resolves to "notebook" — so notebook mode is wired
// once here and applies to all walkthroughs uniformly. notebookbridge
// renders output cells with horizontal-only borders (no vertical
// bars) so streamed output is clean to mouse-select and copy.
func SetupRenderer(demo *demokit.Demo) {
	switch demokit.Mode() {
	case "tui":
		demo.WithRenderer(tui.New())
	case "notebook":
		demo.WithRenderer(notebookbridge.New())
	}
}
