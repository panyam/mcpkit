// Example: mcpskills CLI walkthrough.
//
// Drives the cmd/mcpskills binary end-to-end with narrated demokit
// steps: verify, serve (background), inspect (with --json), pack,
// unpack, byte-equality diff. Doubles as a CI smoke test for the CLI
// when run with --non-interactive.
//
// Run modes:
//
//	go run .                            # text walkthrough
//	go run . --tui                      # interactive TUI
//	go run . --note                     # notebook mode
//	go run . --non-interactive          # CI smoke (no pauses, exits 0/1)
//	go run . --doc=md                   # regenerate WALKTHROUGH.md
//
// Optional env vars:
//
//	MCPSKILLS_BIN                       # use a pre-built binary instead of `go build`
//	MCPSKILLS_INSPECT_UPSTREAM_URL      # add an inspect-against-upstream step
package main

func main() {
	runDemo()
}
