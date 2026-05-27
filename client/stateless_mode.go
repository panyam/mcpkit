package client

import (
	"os"
	"strings"
	"sync"
)

// SEP-2575 client-side wire-mode selection.
//
// Mirrors the server-side stateless.Mode but lives in package client
// because the option API (ClientOption, WithClientMode) and the runtime
// state belong to *Client. The two enums are deliberately not shared —
// the *server* "Dual" mode is about which wires a server accepts; the
// *client* "Adaptive" mode is about which wire a client speaks. They
// compose pairwise (Adaptive client + Dual server is the default).

// ClientMode selects how the client negotiates the wire shape with the
// server during Connect.
type ClientMode int

const (
	// ClientModeLegacyOnly performs the legacy initialize handshake at
	// Connect time. Never sends _meta envelopes or MCP-Protocol-Version
	// headers; pure pre-SEP-2575 behavior. Zero value (kept for the
	// strict-back-compat opt-out path).
	ClientModeLegacyOnly ClientMode = iota

	// ClientModeAdaptive probes server/discover first; on -32601 falls
	// back to the legacy initialize handshake. Once a server is
	// classified as stateless, subsequent calls carry the _meta envelope
	// and the MCP-Protocol-Version HTTP header. Default.
	ClientModeAdaptive

	// ClientModeStateless skips initialize entirely. Every call carries
	// the SEP-2575 _meta envelope and the MCP-Protocol-Version header.
	// server/discover may still be issued explicitly via DiscoverServer.
	ClientModeStateless
)

// String returns the lowercase token used in env vars and structured logs.
func (m ClientMode) String() string {
	switch m {
	case ClientModeLegacyOnly:
		return "legacy"
	case ClientModeAdaptive:
		return "adaptive"
	case ClientModeStateless:
		return "stateless"
	default:
		return "unknown"
	}
}

// ParseClientMode decodes a token (the inverse of String). Accepts
// "legacy", "adaptive", "stateless"; case-insensitive; empty maps to
// caller's fallback via ok=false.
func ParseClientMode(s string) (ClientMode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "legacy":
		return ClientModeLegacyOnly, true
	case "adaptive":
		return ClientModeAdaptive, true
	case "stateless":
		return ClientModeStateless, true
	default:
		return ClientModeAdaptive, false
	}
}

// DefaultClientMode is the wire mode used when no WithClientMode option
// is passed and MCPKIT_CLIENT_MODE is unset.
//
// Default: ClientModeLegacyOnly — preserves pre-SEP-2575 behavior so
// existing client code keeps working on upgrade. Opting into Adaptive
// (the recommended migration path) is one of:
//
//	client.NewClient(url, info, client.WithClientMode(client.ClientModeAdaptive))
//	export MCPKIT_CLIENT_MODE=adaptive
//	func init() { client.DefaultClientMode = client.ClientModeAdaptive }
//
// The shipping default may flip to Adaptive in a future major release
// once the stateless wire is in widespread use. Until then, an extra
// server/discover probe on every Connect would be a silent latency
// regression for clients talking only to legacy servers.
//
// Tests use SetDefaultClientMode to flip under write-lock for safe
// concurrent test execution.
var DefaultClientMode = ClientModeLegacyOnly

var defaultClientModeMu sync.RWMutex

// ClientModeEnvVar names the override-via-environment knob.
const ClientModeEnvVar = "MCPKIT_CLIENT_MODE"

// ResolveClientMode seeds NewClient's mode from env var or DefaultClientMode.
// WithClientMode option clobbers when present (options run after seeding).
//
// Precedence (high → low):
//
//	1. client.WithClientMode(m)
//	2. MCPKIT_CLIENT_MODE env var
//	3. client.DefaultClientMode
func ResolveClientMode() ClientMode {
	if v, ok := ParseClientMode(os.Getenv(ClientModeEnvVar)); ok {
		return v
	}
	defaultClientModeMu.RLock()
	defer defaultClientModeMu.RUnlock()
	return DefaultClientMode
}

// SetDefaultClientMode mutates DefaultClientMode under write-lock.
// Prefer this over direct assignment in tests so ResolveClientMode's
// read-lock sees a consistent value.
func SetDefaultClientMode(m ClientMode) {
	defaultClientModeMu.Lock()
	defer defaultClientModeMu.Unlock()
	DefaultClientMode = m
}

// WithClientMode pins the wire mode for this Client. Highest-precedence
// override — beats MCPKIT_CLIENT_MODE env and DefaultClientMode.
func WithClientMode(m ClientMode) ClientOption {
	return func(c *Client) { c.mode = m }
}
