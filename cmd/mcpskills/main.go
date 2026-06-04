// Command mcpskills is a zero-Go-code CLI for SEP-2640 skills.
//
// Use it to host a skills directory over MCP, to inspect any
// SEP-2640-compliant server (mcpkit, TypeScript SDK, PHP SDK, anything),
// to validate a skill directory against the spec, and to pack/unpack
// skill archives.
//
// Subcommands:
//
//	mcpskills serve   <dir>      Serve a skills directory via MCP
//	mcpskills inspect <url>      Connect to a skills server and report
//	mcpskills verify  <dir>      Lint a skills directory for SEP-2640 compliance
//	mcpskills pack    <dir>      Pack a single skill into .tar.gz or .zip
//	mcpskills unpack  <archive>  Extract a skill archive with safety guards
//
// Run `mcpskills --help` for the full surface.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

const version = "0.1.0"

// colorFlag is the global --color flag value, consumed by subcommands
// that emit colored output. cobra makes it package-global via the root
// command's PersistentFlags binding.
var colorFlag string

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "mcpskills",
		Short: "SEP-2640 skills CLI: serve, inspect, verify, pack, unpack",
		Long: `mcpskills is a zero-Go-code CLI for SEP-2640.

Host a directory of skills with one command, introspect any compliant
server, lint a skill directory, or produce / consume archive bundles.`,
		Version:      version,
		SilenceUsage: true,
	}
	root.PersistentFlags().StringVar(&colorFlag, "color", "auto", "color output: auto | always | never")

	root.AddCommand(newServeCmd())
	root.AddCommand(newInspectCmd())
	root.AddCommand(newVerifyCmd())
	root.AddCommand(newPackCmd())
	root.AddCommand(newUnpackCmd())
	return root
}

func main() {
	if err := newRoot().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "mcpskills:", err)
		os.Exit(1)
	}
}
