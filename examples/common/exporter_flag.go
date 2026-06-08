package common

// Uniform --exporter / --otlp-endpoint flag registration for mcpkit
// example binaries (issue 666). Every example calls
// RegisterTelemetryFlags(flag.CommandLine) on its flag set and
// passes the returned struct's values into commonotel.SetupTelemetry.
// Default --exporter="" — operators opt in per invocation; no
// example dumps spans to stdout unless asked.
//
// Walkthroughs (runDemo) often don't call flag.Parse — they rely on
// common.ServerURL's ad-hoc os.Args scan for --url. For symmetric
// pickup of --exporter / --otlp-endpoint without flag.Parse, use
// ExporterFromArgs.

import (
	"flag"
	"os"
	"strings"

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

// ExporterFromArgs scans os.Args for --exporter / --otlp-endpoint
// without calling flag.Parse. Mirrors the ad-hoc pattern
// common.ServerURL uses for --url. Returns a *TelemetryFlags whose
// pointers are locally allocated, so the API matches what
// RegisterTelemetryFlags returns — call sites deref the same way
// (`*tel.Exporter`) regardless of which helper populated it.
//
// Use from walkthroughs (runDemo) that don't otherwise call
// flag.Parse. For binaries that already call flag.Parse (any serve()
// using common.RunServer with extra example-specific flags), prefer
// RegisterTelemetryFlags so demokit.FilterArgs sees the registered
// flag set.
//
// Recognized argv shapes:
//
//	--exporter=otlp          --otlp-endpoint=localhost:4317
//	--exporter otlp          --otlp-endpoint localhost:4317
//
// Defaults match RegisterTelemetryFlags: exporter="", endpoint=
// commonotel.DefaultOTLPEndpoint.
func ExporterFromArgs() *TelemetryFlags {
	exporter := ""
	endpoint := commonotel.DefaultOTLPEndpoint

	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--exporter" && i+1 < len(args):
			exporter = args[i+1]
			i++
		case strings.HasPrefix(a, "--exporter="):
			exporter = strings.TrimPrefix(a, "--exporter=")
		case a == "--otlp-endpoint" && i+1 < len(args):
			endpoint = args[i+1]
			i++
		case strings.HasPrefix(a, "--otlp-endpoint="):
			endpoint = strings.TrimPrefix(a, "--otlp-endpoint=")
		}
	}

	return &TelemetryFlags{
		Exporter:     &exporter,
		OTLPEndpoint: &endpoint,
	}
}
