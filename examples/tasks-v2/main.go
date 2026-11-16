// Example: MCP Tasks v2 (SEP-2557) — server-directed async tool execution.
//
// Two-process architecture:
//
//	Terminal 1:  just serve         # tasks-v2 server on :8080
//	Terminal 2:  just run           # demokit walkthrough
//
// Tools:
//   - greet:              sync-only (no Execution field = forbidden)
//   - slow_compute:       optional task support (server creates task for async)
//   - failing_job:        required task support (tool error → completed + isError)
//   - protocol_error_job: required task support (protocol error → failed + error)
//   - external_job:       required task support (TaskCallbacks proxy pattern)
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"strings"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/examples/common"
	commonotel "github.com/panyam/mcpkit/examples/common/otel"
	"github.com/panyam/mcpkit/examples/tasks-v2/mcp"
	"github.com/panyam/mcpkit/server"
)

func main() {
	for _, arg := range os.Args[1:] {
		switch strings.TrimSpace(arg) {
		case "--serve":
			serve()
			return
		}
	}
	runDemo()
}

func serve() {
	addr := flag.String("addr", ":8080", "listen address")
	tel := common.RegisterTelemetryFlags(flag.CommandLine)
	wire := common.RegisterWireFlags(flag.CommandLine)
	flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:],
		demokit.BoolFlag("--serve"),
		demokit.ValueFlag("--url"),
	))

	tp, shutdown, err := commonotel.SetupTelemetry(context.Background(),
		commonotel.WithExporter(*tel.Exporter),
		commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
		commonotel.WithServiceName("tasks-v2-demo"),
	)
	if err != nil {
		log.Fatalf("commonotel.SetupTelemetry: %v", err)
	}
	defer shutdown(context.Background())

	log.Printf("Connect: http://localhost%s/mcp", *addr)
	log.Printf("")
	log.Printf("Tools:")
	log.Printf("  greet              - sync-only")
	log.Printf("  slow_compute       - optional task (server-directed)")
	log.Printf("  failing_job        - required task (tool error → completed + isError)")
	log.Printf("  confirm_delete     - required task (input_required → tasks/update → completed)")
	log.Printf("  multi_input        - required task (two simultaneous inputRequests for partial fulfillment)")
	log.Printf("  protocol_error_job - required task (protocol error → failed + error)")
	log.Printf("  external_job       - required task (TaskCallbacks proxy)")

	if err := common.RunServer(common.ServerConfig{
		Name:           "tasks-v2-demo",
		Addr:           *addr,
		TracerProvider: tp,
		Wire:           wire,
		Register: func(srv *server.Server) {
			mcp.Register(srv)
		},
	}); err != nil {
		log.Fatal(err)
	}
}
