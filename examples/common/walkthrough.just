# examples/common/walkthrough.just
#
# Shared justfile fragment for example fixtures that ship a demokit
# walkthrough. Per-fixture justfile is a one-liner:
#
#   import '../../../common/walkthrough.just'     (compat fixture: 3 dirs deep)
#   import '../../common/walkthrough.just'        (non-compat: 2 dirs deep)
#
# Override the defaults per-invocation via environment variables:
#
#   FIXTURE_NAME=my-fixture-demo SERVER_PORT=3201 just serve
#
# Defaults:
#   FIXTURE_NAME → directory basename + "-demo" (e.g. basic-vanillajs-demo)
#   SERVER_PORT  → 3101 (the compat-fixture default)

FIXTURE_NAME := env('FIXTURE_NAME', file_name(justfile_directory()) + "-demo")
SERVER_PORT := env('SERVER_PORT', '3101')

# Run the demokit walkthrough (interactive TUI; requires `just serve` in another terminal)
demo:
    go run . --demo --tui

# Run the walkthrough in notebook mode (Bubble Tea cells)
note:
    go run . --demo --note

# Start the fixture as an MCP server
serve:
    EXT_APPS_DIR=${EXT_APPS_DIR:-/tmp/ext-apps} PORT=${PORT:-{{SERVER_PORT}}} go run .

# Regenerate WALKTHROUGH.md from the walkthrough definitions
readme:
    go run . --demo --doc md > WALKTHROUGH.md

# Capture a trace of the walkthrough (interactive — press Enter to advance each step; requires `just serve` in another terminal)
record:
    go run . --demo --tui --record walkthrough.trace.json
    @echo ""
    @echo "Trace written to walkthrough.trace.json — commit it (it's the source of truth for the bundle)."

# Build a self-contained HTML player from the recorded trace (output: bundle/index.html + sibling JS/CSS). Commit bundle/ — docs-site picks it up.
bundle:
    @mkdir -p bundle
    go run . --demo --doc bundle --from walkthrough.trace.json --out bundle/index.html
    @echo "Wrote bundle/index.html — commit bundle/ so docs-site can publish it."

# Alias for `just bundle`
walkthrough: bundle

# Short alias for `just bundle`
walk: bundle

# Build the fixture binary
build:
    go build -o {{FIXTURE_NAME}} .
