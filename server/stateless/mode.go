// Package stateless implements the SEP-2575 stateless wire — server/discover,
// per-request _meta envelope dispatch, subscriptions/listen, and the new
// HTTP-status error mapping. Designed so the legacy session wire and this
// wire can be served from one URL via the parent server package's Dual mode,
// and so removing the legacy wire is a single-package deletion that leaves
// this code untouched.
//
// Spec: https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2575
package stateless

import (
	"os"
	"strings"
	"sync"
)

// Mode selects which protocol wire(s) the Streamable HTTP transport will
// serve on a single URL. Distinct from the parent server package's
// WithStateless option, which controls process-architecture (no in-process
// session storage) — the two are orthogonal and may be combined freely.
//
// Example: pure SEP-2575 wire + no session storage (the common
// serverless/Lambda deployment shape):
//
//	server.New(impl,
//	    server.WithStatelessMode(stateless.ModeStateless),
//	    server.WithStateless(true),
//	)
//
// Default Dual mode + session-backed legacy clients allowed:
//
//	server.New(impl) // implicit stateless.ModeDual + WithStateless(false)
type Mode int

const (
	// ModeLegacyOnly accepts only the legacy session wire
	// (initialize/notifications/initialized handshake; Mcp-Session-Id
	// header). server/discover is rejected with -32601 + HTTP 404.
	// Zero-value-compatible with the previous default behavior, so a
	// build that explicitly opts out of SEP-2575 keeps the old shape.
	ModeLegacyOnly Mode = iota

	// ModeDual accepts both wires on the same URL, branching per-request
	// on the shape of the incoming payload (method == initialize /
	// presence of Mcp-Session-Id → legacy; MCP-Protocol-Version header
	// or _meta protocolVersion field → stateless). Default.
	ModeDual

	// ModeStateless accepts only the SEP-2575 stateless wire. initialize,
	// notifications/initialized, ping, logging/setLevel, and
	// resources/(un)subscribe are all rejected with -32601 + HTTP 404.
	ModeStateless
)

// String returns the lowercase token used in env vars and structured logs.
func (m Mode) String() string {
	switch m {
	case ModeLegacyOnly:
		return "legacy"
	case ModeDual:
		return "dual"
	case ModeStateless:
		return "stateless"
	default:
		return "unknown"
	}
}

// ParseMode decodes a mode token (the inverse of String). Accepts "legacy",
// "dual", "stateless"; case-insensitive; empty string maps to the caller's
// fallback via the returned ok=false.
func ParseMode(s string) (Mode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "legacy":
		return ModeLegacyOnly, true
	case "dual":
		return ModeDual, true
	case "stateless":
		return ModeStateless, true
	default:
		return ModeDual, false
	}
}

// DefaultMode is the wire mode used when no WithStatelessMode option is
// passed and no MCPKIT_STATELESS_MODE env var is set.
//
// Default: ModeDual — a fresh server.New() gains SEP-2575 wire support
// automatically. Integrators that need to pin the old behavior without
// per-construction plumbing can flip this in init():
//
//	func init() { stateless.DefaultMode = stateless.ModeLegacyOnly }
//
// Tests can flip with t.Cleanup-style restore. Reads serialize via
// defaultModeMu so concurrent test flips don't race with constructors.
var DefaultMode = ModeDual

var defaultModeMu sync.RWMutex

// ModeEnvVar names the override-via-environment knob. Public so callers
// can read or mutate it in tests (e.g., t.Setenv(stateless.ModeEnvVar, "legacy")).
const ModeEnvVar = "MCPKIT_STATELESS_MODE"

// ResolveMode seeds the runtime mode from the env var or, failing that,
// DefaultMode. Server constructors call this to seed transportConfig;
// the WithStatelessMode option clobbers the seed when present.
//
// Precedence (high → low):
//
//	1. WithStatelessMode (applied post-seed)
//	2. MCPKIT_STATELESS_MODE environment variable
//	3. DefaultMode (under read-lock)
func ResolveMode() Mode {
	if v, ok := ParseMode(os.Getenv(ModeEnvVar)); ok {
		return v
	}
	defaultModeMu.RLock()
	defer defaultModeMu.RUnlock()
	return DefaultMode
}

// SetDefaultMode mutates DefaultMode under write-lock. Test helpers should
// prefer this over direct assignment so the read-lock in ResolveMode sees
// a consistent value.
func SetDefaultMode(m Mode) {
	defaultModeMu.Lock()
	defer defaultModeMu.Unlock()
	DefaultMode = m
}
