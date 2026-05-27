package server

import (
	"github.com/panyam/mcpkit/server/stateless"
)

// WithStatelessMode pins the SEP-2575 wire mode for this server. The full
// enum, parser, env-var resolver, and default lifecycle live in
// server/stateless/mode.go — this file only carries the TransportOption
// wrapper so the option API stays in package server alongside every other
// With* TransportOption.
//
// Precedence (high → low):
//
//	1. server.WithStatelessMode(...)  ← this option
//	2. MCPKIT_STATELESS_MODE env var  ← seeded by stateless.ResolveMode
//	3. stateless.DefaultMode          ← package var, mutable in init()
//
// See the stateless.Mode godoc for the full mode table and the orthogonal
// relationship to WithStateless (process-architecture).
func WithStatelessMode(m stateless.Mode) TransportOption {
	return func(c *transportConfig) { c.statelessMode = m }
}
