// Package common holds helpers shared across mcpkit examples — the seam
// where "every example needs this now" updates land in one place instead
// of N copy-paste edits. See examples/CONVENTIONS.md for the conventions
// these helpers encode.
package common

import (
	"log"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/server"
)

// NewMCPLogger returns the canonical demokit ColorLogger used across
// mcpkit examples. The five built-in rules tint:
//
//   - error= and ERROR markers (red)
//   - [http] → outbound transport (gray / dim blue)
//   - [http] ← inbound transport (cyan / blue)
//   - MCP <method> dispatch (bright green / green)
//
// extraRules append to the canonical set so callers can tint
// example-specific log lines without losing the shared baseline.
func NewMCPLogger(prefix string, extraRules ...demokit.ColorRule) *log.Logger {
	rules := []demokit.ColorRule{
		{Contains: "error=", DarkColor: demokit.ANSIRed},
		{Contains: "ERROR", DarkColor: demokit.ANSIRed},
		{Contains: "[http] →", DarkColor: demokit.ANSIGray, LightColor: demokit.ANSIDimBlue},
		{Contains: "[http] ←", DarkColor: demokit.ANSICyan, LightColor: demokit.ANSIBlue},
		{Contains: "MCP ", DarkColor: demokit.ANSIBrightGreen, LightColor: demokit.ANSIGreen},
	}
	rules = append(rules, extraRules...)
	return demokit.NewColorLogger(prefix, rules)
}

// WithMCPLogging returns the standard server options that wire a logger
// to both transport-level request logging and the MCP dispatch
// middleware path. Use directly when you need the logger handle for
// example-specific tints; otherwise prefer MCPServerOptions which bundles
// listen + logging into one call.
func WithMCPLogging(logger *log.Logger) []server.Option {
	return []server.Option{
		server.WithRequestLogging(logger),
		server.WithMiddleware(server.LoggingMiddleware(logger)),
	}
}

// MCPServerOptions returns the canonical baseline server options every
// non-UI example wants: WithListen + the canonical color logger wired to
// both transport request logging and dispatch middleware.
//
// Callers append example-specific options:
//
//	opts := common.MCPServerOptions(*addr, "[mcp] ")
//	opts = append(opts, server.WithListTTL(60))
//	srv := server.NewServer(info, opts...)
//
// If you need the logger handle (custom tinted log lines, extra rules),
// use NewMCPLogger + WithMCPLogging instead.
func MCPServerOptions(addr, prefix string) []server.Option {
	logger := NewMCPLogger(prefix)
	opts := []server.Option{server.WithListen(addr)}
	return append(opts, WithMCPLogging(logger)...)
}
