package common

// Uniform --wire flag for SEP-2575 wire-mode selection across mcpkit
// example binaries. An example author registers the flag once and the
// helper resolves it into the right server.TransportOption and/or
// client.ClientOption, so examples stop hand-rolling
// stateless.ParseMode / client.ParseClientMode plumbing.
//
// SEP-2575 ships two wires on one URL (legacy session wire + stateless
// wire); SEP-2567 ("Sessionless MCP") is the application-layer
// counterpart. Examples should be runnable on either wire from a single
// CLI knob so the on-ramp doesn't bake one wire in.
//
// Precedence per side stays consistent with the rest of mcpkit:
// explicit flag > MCPKIT_STATELESS_MODE / MCPKIT_CLIENT_MODE env >
// package default (stateless.DefaultMode / client.DefaultClientMode).
// When --wire (and the per-side overrides) are empty the helper appends
// nothing, leaving the env/default fall-through untouched.

import (
	"flag"
	"log"
	"os"
	"strings"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/server/stateless"
)

// WireFlags holds the wire-selection flag values. The primary --wire
// drives BOTH halves of a demo binary (server and client) via a sensible
// pairing; --server-wire / --client-wire override one side when a demo
// genuinely needs them decoupled.
//
// Pairing applied by --wire:
//
//	--wire=legacy     server ModeLegacyOnly  + client ClientModeLegacyOnly
//	--wire=dual       server ModeDual        + client ClientModeAdaptive
//	--wire=stateless  server ModeStateless   + client ClientModeStateless
//
// dual is the only value that maps asymmetrically: a Dual server speaks
// both wires, so the client side pairs to Adaptive (probe then fall
// back), not a nonexistent "dual" client mode.
type WireFlags struct {
	// Wire is the primary --wire value: "" | legacy | dual | stateless.
	// "" means "make no selection" — both sides fall through to their
	// env var / package default.
	Wire *string

	// ServerWire is the --server-wire override: "" | legacy | dual |
	// stateless. Beats Wire for the server side only.
	ServerWire *string

	// ClientWire is the --client-wire override: "" | legacy | adaptive |
	// stateless. Beats Wire for the client side only.
	ClientWire *string
}

// RegisterWireFlags wires --wire / --server-wire / --client-wire onto fs.
// Call before flag.Parse. Mirrors RegisterTelemetryFlags.
//
// The flag names MUST be registered as demokit.ValueFlag in any binary
// that filters os.Args through demokit.FilterArgs, so they survive the
// filter — see examples/CONVENTIONS.md §Wire selection.
func RegisterWireFlags(fs *flag.FlagSet) *WireFlags {
	return &WireFlags{
		Wire:       fs.String("wire", "", `wire mode for both halves: "" (env/default) | legacy | dual | stateless (dual pairs client to adaptive)`),
		ServerWire: fs.String("server-wire", "", "override server wire only: legacy | dual | stateless (beats --wire)"),
		ClientWire: fs.String("client-wire", "", "override client wire only: legacy | adaptive | stateless (beats --wire)"),
	}
}

// WireFromArgs scans os.Args for --wire / --server-wire / --client-wire
// without calling flag.Parse. Mirrors ExporterFromArgs, for walkthroughs
// (runDemo) that don't otherwise parse flags. Returns a *WireFlags whose
// pointers are locally allocated so call sites resolve it identically to
// the RegisterWireFlags return.
//
// Recognized argv shapes (space- or =-separated):
//
//	--wire=stateless   --server-wire dual   --client-wire=adaptive
func WireFromArgs() *WireFlags {
	wire, serverWire, clientWire := "", "", ""
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--wire" && i+1 < len(args):
			wire = args[i+1]
			i++
		case strings.HasPrefix(a, "--wire="):
			wire = strings.TrimPrefix(a, "--wire=")
		case a == "--server-wire" && i+1 < len(args):
			serverWire = args[i+1]
			i++
		case strings.HasPrefix(a, "--server-wire="):
			serverWire = strings.TrimPrefix(a, "--server-wire=")
		case a == "--client-wire" && i+1 < len(args):
			clientWire = args[i+1]
			i++
		case strings.HasPrefix(a, "--client-wire="):
			clientWire = strings.TrimPrefix(a, "--client-wire=")
		}
	}
	return &WireFlags{Wire: &wire, ServerWire: &serverWire, ClientWire: &clientWire}
}

// serverToken resolves the effective server wire token: --server-wire if
// set, else --wire, else "".
func (w *WireFlags) serverToken() string {
	if s := deref(w.ServerWire); s != "" {
		return s
	}
	return deref(w.Wire)
}

// clientToken resolves the effective client wire token: --client-wire if
// set, else the client-equivalent of --wire (dual maps to adaptive),
// else "".
func (w *WireFlags) clientToken() string {
	if s := deref(w.ClientWire); s != "" {
		return s
	}
	switch strings.ToLower(strings.TrimSpace(deref(w.Wire))) {
	case "":
		return ""
	case "dual":
		return "adaptive"
	default:
		return deref(w.Wire) // legacy / stateless map 1:1
	}
}

// ServerTransportOption resolves the server wire into
// server.WithStatelessMode. ok=true only when a wire was explicitly
// selected; ok=false (with a nil option) means "make no selection" so
// the caller appends nothing and the server keeps its env/default wire.
//
// An unrecognized token logs a warning and returns ok=false rather than
// binding a surprising mode — a CLI typo falls through to the default
// instead of silently flipping the wire.
func (w *WireFlags) ServerTransportOption() (server.TransportOption, bool) {
	tok := w.serverToken()
	if tok == "" {
		return nil, false
	}
	mode, parsed := stateless.ParseMode(tok)
	if !parsed {
		log.Printf("[common] ignoring unrecognized wire %q for server (want legacy|dual|stateless)", tok)
		return nil, false
	}
	return server.WithStatelessMode(mode), true
}

// ClientOption resolves the client wire into client.WithClientMode.
// ok=true only when a wire was explicitly selected; ok=false (nil
// option) means the client keeps its env/default mode. Unrecognized
// tokens warn and fall through, matching ServerTransportOption.
func (w *WireFlags) ClientOption() (client.ClientOption, bool) {
	tok := w.clientToken()
	if tok == "" {
		return nil, false
	}
	mode, parsed := client.ParseClientMode(tok)
	if !parsed {
		log.Printf("[common] ignoring unrecognized wire %q for client (want legacy|adaptive|stateless)", tok)
		return nil, false
	}
	return client.WithClientMode(mode), true
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
