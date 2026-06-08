package common

// Uniform --exporter / --otlp-endpoint flag registration for mcpkit
// example binaries (issue 666). Every example calls
// RegisterTelemetryFlags(flag.CommandLine) on its flag set and
// passes the returned struct's values into commonotel.SetupTelemetry.
// Default --exporter="" — operators opt in per invocation; no
// example dumps spans to stdout unless asked.

import (
	"flag"

	commonotel "github.com/panyam/mcpkit/examples/common/otel"
)

// TelemetryFlags holds pointers populated by RegisterTelemetryFlags.
// Pass Exporter and OTLPEndpoint into commonotel.SetupTelemetry's
// WithExporter / WithOTLPEndpoint options after flag.Parse.
//
// The struct shape (rather than two free pointer returns) lets a
// caller pass the whole thing to a helper without re-typing each
// field, and gives room for future flags (sampling rate, logs
// exporter, metrics exporter) without churning the function
// signature.
type TelemetryFlags struct {
	// Exporter is the --exporter flag value: "", "stdout", or
	// "otlp". Default "" so no example prints spans uninvited.
	Exporter *string

	// OTLPEndpoint is the --otlp-endpoint flag value. Default is
	// commonotel.DefaultOTLPEndpoint (matches docker/observability/).
	// Only consulted when Exporter=="otlp".
	OTLPEndpoint *string
}

// RegisterTelemetryFlags wires the uniform --exporter +
// --otlp-endpoint flag pair onto fs. Call before flag.Parse.
//
// Example wiring inside serve():
//
//	addr := flag.String("addr", ":8080", "listen address")
//	tel := common.RegisterTelemetryFlags(flag.CommandLine)
//	flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:],
//	    demokit.BoolFlag("--serve"),
//	    demokit.ValueFlag("--exporter"),
//	    demokit.ValueFlag("--otlp-endpoint"),
//	))
//	tp, shutdown, err := commonotel.SetupTelemetry(ctx,
//	    commonotel.WithServiceName("..."),
//	    commonotel.WithExporter(*tel.Exporter),
//	    commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
//	)
//
// The --exporter / --otlp-endpoint flag names MUST be registered as
// demokit.ValueFlag so FilterArgs doesn't strip them — see
// examples/CONVENTIONS.md §Telemetry wiring.
func RegisterTelemetryFlags(fs *flag.FlagSet) *TelemetryFlags {
	return &TelemetryFlags{
		Exporter:     fs.String("exporter", "", "trace exporter: \"\" (off, default) | stdout | otlp | auto (best-effort OTLP — silent Noop fallback if unreachable)"),
		OTLPEndpoint: fs.String("otlp-endpoint", commonotel.DefaultOTLPEndpoint, "OTLP gRPC endpoint when --exporter=otlp"),
	}
}
