package server

import (
	"os"
	"strings"
	"sync"
)

// StatelessMode selects which protocol wire(s) the Streamable HTTP transport
// will serve on a single URL. Distinct from the WithStateless option, which
// controls process-architecture (no in-process session storage) — the two
// are orthogonal and may be combined freely:
//
//	WithStatelessMode(StatelessModeStateless) + WithStateless(true)
//	    → pure SEP-2575 wire + no session storage (the common
//	      serverless/Lambda deployment shape)
//
//	WithStatelessMode(StatelessModeDual) + WithStateless(false)
//	    → legacy session-cached protocol AND SEP-2575 wire on the same
//	      URL; session-shaped clients get sessions, stateless clients
//	      get the new wire (default in this build).
type StatelessMode int

const (
	// StatelessModeLegacyOnly accepts only the legacy session wire
	// (initialize/notifications/initialized handshake; Mcp-Session-Id
	// header). server/discover is rejected with -32601 + HTTP 404.
	// Zero-value-compatible with the previous default behavior, so a
	// build that explicitly opts out of SEP-2575 keeps the old shape.
	StatelessModeLegacyOnly StatelessMode = iota

	// StatelessModeDual accepts both wires on the same URL, branching
	// per-request on the shape of the incoming payload (method ==
	// initialize / presence of Mcp-Session-Id → legacy; MCP-Protocol-Version
	// header or _meta protocolVersion field → stateless). Default.
	StatelessModeDual

	// StatelessModeStateless accepts only the SEP-2575 stateless wire.
	// initialize / notifications/initialized / ping / logging/setLevel /
	// resources/(un)subscribe are all rejected with -32601 + HTTP 404.
	StatelessModeStateless
)

// String returns the lowercase token used in env vars and structured logs.
func (m StatelessMode) String() string {
	switch m {
	case StatelessModeLegacyOnly:
		return "legacy"
	case StatelessModeDual:
		return "dual"
	case StatelessModeStateless:
		return "stateless"
	default:
		return "unknown"
	}
}

// ParseStatelessMode decodes a mode token (the inverse of String). Accepts
// "legacy", "dual", "stateless"; case-insensitive; empty string maps to the
// caller's fallback via the returned ok=false.
func ParseStatelessMode(s string) (StatelessMode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "legacy":
		return StatelessModeLegacyOnly, true
	case "dual":
		return StatelessModeDual, true
	case "stateless":
		return StatelessModeStateless, true
	default:
		return StatelessModeDual, false
	}
}

// DefaultStatelessMode is the wire mode used when no WithStatelessMode
// option is passed and no MCPKIT_STATELESS_MODE env var is set.
//
// Default: StatelessModeDual — a fresh server.New() gains SEP-2575 wire
// support automatically. Integrators that need to pin the old behavior
// without per-construction plumbing can flip this in init():
//
//	func init() { server.DefaultStatelessMode = server.StatelessModeLegacyOnly }
//
// Tests can flip this with t.Cleanup-style restore. Reads of this var
// are serialized by defaultStatelessModeMu so concurrent test flips
// don't race with a constructor reading the default.
var DefaultStatelessMode = StatelessModeDual

var defaultStatelessModeMu sync.RWMutex

// statelessModeEnvVar names the override-via-environment knob.
const statelessModeEnvVar = "MCPKIT_STATELESS_MODE"

// resolveStatelessMode seeds transportConfig.statelessMode from the env
// var or, failing that, DefaultStatelessMode. The WithStatelessMode option
// clobbers this seed when present (options run after seeding).
//
// Precedence (high → low):
//
//	1. WithStatelessMode (applied via the option closure, post-seed)
//	2. MCPKIT_STATELESS_MODE environment variable
//	3. DefaultStatelessMode (under read-lock)
func resolveStatelessMode() StatelessMode {
	if v, ok := ParseStatelessMode(os.Getenv(statelessModeEnvVar)); ok {
		return v
	}
	defaultStatelessModeMu.RLock()
	defer defaultStatelessModeMu.RUnlock()
	return DefaultStatelessMode
}

// WithStatelessMode pins the wire mode for this server. See StatelessMode
// constants for the three options. Highest-precedence override — beats
// MCPKIT_STATELESS_MODE and DefaultStatelessMode.
//
// Orthogonal to WithStateless (process-architecture); see the StatelessMode
// type doc for a worked example combining the two.
func WithStatelessMode(m StatelessMode) TransportOption {
	return func(c *transportConfig) { c.statelessMode = m }
}
