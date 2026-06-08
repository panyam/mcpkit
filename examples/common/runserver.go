package common

import (
	"log"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// ServerConfig is the canonical config for a non-UI mcpkit example
// server. Name and Addr are required; everything else has a sensible
// default. See RunServer for the wiring contract.
type ServerConfig struct {
	// Name is the server name reported via core.ServerInfo (required).
	Name string

	// Version is reported via core.ServerInfo. Defaults to "0.1.0" when
	// empty — examples rarely care about the value and benefit from a
	// stable default.
	Version string

	// Addr is the TCP listen address passed to server.WithListen
	// (required). Defaults are intentionally NOT applied here so a
	// missing addr surfaces at startup instead of binding ":8080"
	// silently.
	Addr string

	// LogPrefix is the canonical color-logger prefix passed to
	// MCPServerOptions. Defaults to "[mcp] " when empty.
	//
	// Ignored when Logger is non-nil — the caller is then responsible
	// for the prefix attached to the logger they pass in.
	LogPrefix string

	// Logger is an optional pre-built logger. When set, RunServer skips
	// MCPServerOptions and wires logging via WithMCPLogging(Logger) so
	// callers that need custom color rules (NewMCPLogger extras) keep
	// their handle. When nil, the canonical 5-rule logger is used.
	Logger *log.Logger

	// Options are additional server options appended to the canonical
	// baseline (WithListen + logging) before NewServer is called. Use
	// for construct-time wiring like WithExtension, WithAuth,
	// WithListCacheControl, etc.
	Options []server.Option

	// Register is invoked after NewServer for tool/middleware/extension
	// registration that requires the constructed *server.Server. Called
	// before ListenAndServe so registrations are live by the time the
	// transport starts accepting connections. May be nil.
	Register func(*server.Server)

	// TransportOptions are appended after server.WithStreamableHTTP(true)
	// when calling ListenAndServe. Use for serve-time wiring like
	// WithStatelessMode, WithSSE, WithEventStore, etc.
	TransportOptions []server.TransportOption

	// TracerProvider, when non-nil, wires SEP-414 trace middleware
	// into the server via server.WithTracerProvider. Pass the result
	// of commonotel.SetupTelemetry directly — it's already wrapped
	// in mcpotel.NewProvider so no adapter call is needed at the
	// example call site. A nil value (or core.NoopTracerProvider{})
	// is the default and adds zero overhead.
	TracerProvider core.TracerProvider
}

// RunServer wires up the canonical mcpkit-example server lifecycle:
//
//  1. Build baseline options (WithListen + canonical color logger via
//     MCPServerOptions, or WithListen + WithMCPLogging(cfg.Logger) when
//     a pre-built logger is supplied).
//  2. Append cfg.Options.
//  3. Construct *server.Server with core.ServerInfo{Name, Version}.
//  4. Invoke cfg.Register (if set) for tool/middleware registration.
//  5. Log "[<name>] listening on <addr>".
//  6. Call srv.ListenAndServe(WithStreamableHTTP(true), cfg.TransportOptions...).
//
// Behaves like a normal blocking serve — returns whatever
// ListenAndServe returns (typically a graceful-shutdown nil or a bind
// error). Callers that need to log additional info (extra tool names,
// temp dirs, etc.) should log before calling RunServer; the helper's
// own "listening on" line stays minimal and uniform across examples.
//
// For examples whose serve loop diverges substantially from this
// shape (mux setup with side endpoints, parallel webhook listeners),
// build the server manually with NewMCPLogger + MCPServerOptions and
// call srv.ListenAndServe directly — see examples/events/discord/ for
// the canonical exception.
func RunServer(cfg ServerConfig) error {
	if cfg.Version == "" {
		cfg.Version = "0.1.0"
	}
	if cfg.LogPrefix == "" {
		cfg.LogPrefix = "[mcp] "
	}

	var opts []server.Option
	if cfg.Logger != nil {
		opts = append(opts, server.WithListen(cfg.Addr))
		opts = append(opts, WithMCPLogging(cfg.Logger)...)
	} else {
		opts = append(opts, MCPServerOptions(cfg.Addr, cfg.LogPrefix)...)
	}
	if cfg.TracerProvider != nil {
		opts = append(opts, server.WithTracerProvider(cfg.TracerProvider))
	}
	opts = append(opts, cfg.Options...)

	srv := server.NewServer(core.ServerInfo{Name: cfg.Name, Version: cfg.Version}, opts...)

	if cfg.Register != nil {
		cfg.Register(srv)
	}

	log.Printf("[%s] listening on %s", cfg.Name, cfg.Addr)

	transportOpts := append([]server.TransportOption{server.WithStreamableHTTP(true)}, cfg.TransportOptions...)
	return srv.ListenAndServe(transportOpts...)
}
