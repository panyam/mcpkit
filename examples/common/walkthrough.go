package common

import (
	"os"

	"github.com/panyam/demokit"
	"github.com/panyam/demokit/notebookbridge"
	"github.com/panyam/demokit/tui"
	"github.com/panyam/demokit/web"
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
// Also registers the demokit/web package so every walkthrough gets
// `--doc bundle` (generates a self-contained playable HTML page from
// a recorded trace) and `--serve <addr>` (live HTTP+WS player) for
// free. The recipe to publish a playable trace:
//
//	# Record a trace once (non-interactive so it doesn't pause for input):
//	go run . --demo --record .walkthrough.trace.json --non-interactive
//
//	# Bundle the trace + the player into one HTML page (+ sibling
//	# JS/CSS assets) ready to host on gh-pages / docs.site:
//	go run . --demo --doc bundle --from .walkthrough.trace.json --out walkthrough/index.html
//
// `make note` target shells out to `--note`, which demokit.Mode()
// resolves to "notebook" — so notebook mode is wired once here and
// applies to all walkthroughs uniformly. notebookbridge renders output
// cells with horizontal-only borders (no vertical bars) so streamed
// output is clean to mouse-select and copy.
func SetupRenderer(demo *demokit.Demo) {
	switch demokit.Mode() {
	case "tui":
		// HorizontalOnly borders so triple-click / drag-select on a
		// verbatim block grabs only the content -- no side `│` chars.
		// The tabbed switcher in VerbatimVariants keeps its labels
		// readable on top/bottom edges; copy-paste stays clean.
		demo.WithRenderer(tui.New().WithBorderStyle(demokit.BorderHorizontalOnly))
	case "notebook":
		demo.WithRenderer(notebookbridge.New().WithBorderStyle(demokit.BorderHorizontalOnly))
	}
	web.RegisterWith(demo)
}

// WireRecipe attaches a tabbed "Reproduce on the wire" block with two
// variants -- the wire-level curl form (default) and the equivalent
// *client.Client Go form -- to a walkthrough step. The TUI renders it
// as a tabbed switcher (press tab to flip between curl and Go); the
// horizontal-only borders configured in SetupRenderer mean copy-paste
// of inner content rows picks up only the content, no side chars.
//
// In markdown / HTML output, curl is rendered as the default variant
// (matching the convention from examples/file-inputs); pass
// `--variant=go` or `--variant=all` to override.
//
// Returns the step so calls keep chaining.
func WireRecipe(s *demokit.StepDef, curl, goSource string) *demokit.StepDef {
	return s.VerbatimVariants("Reproduce on the wire",
		demokit.MakeVariant("curl", "bash", curl).Default(),
		demokit.MakeVariant("go", "go", goSource),
	)
}
