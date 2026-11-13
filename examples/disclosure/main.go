// Example: a server with 200 tools and two skills, for progressive disclosure.
//
// Two-process architecture:
//
//	Terminal 1:  just serve         # 200 tools + 2 skills on :8080
//	Terminal 2:  just demo          # walkthrough (--tui for the interactive TUI)
//
// The same binary has a check mode for the progressive-disclosure exercise
// (`make exercise-disclosure` at the repo root). It connects a plain client
// and a groups-aware client and prints one PASS / FAIL row per check:
//
//	go run . --serve --addr=127.0.0.1:18420 --wire=stateless
//	go run . --check --url http://127.0.0.1:18420 --wire=stateless
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"strings"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/examples/common"
	commonotel "github.com/panyam/mcpkit/examples/common/otel"
	"github.com/panyam/mcpkit/server"
)

func main() {
	for _, arg := range os.Args[1:] {
		switch strings.TrimSpace(arg) {
		case "--serve":
			serve()
			return
		case "--check":
			if !check() {
				os.Exit(1)
			}
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
		commonotel.WithServiceName("disclosure"),
	)
	if err != nil {
		log.Fatalf("commonotel.SetupTelemetry: %v", err)
	}
	defer shutdown(context.Background())

	if err := common.RunServer(common.ServerConfig{
		Name:           "disclosure",
		Addr:           *addr,
		TracerProvider: tp,
		Wire:           wire,
		Register: func(srv *server.Server) {
			if err := registerFixture(srv); err != nil {
				log.Fatalf("register: %v", err)
			}
			log.Printf("[disclosure] %d tools in %d themes, skills from the embedded skills/ tree", toolCount, len(themes))
		},
	}); err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}

// check runs the exercise checks against --url and reports whether all of
// them passed.
func check() bool {
	url := flag.String("url", common.DefaultServerURL, "server base URL; /mcp is appended")
	wire := common.RegisterWireFlags(flag.CommandLine)
	flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:], demokit.BoolFlag("--check")))
	var opts []client.ClientOption
	if opt, ok := wire.ClientOption(); ok {
		opts = append(opts, opt)
	}
	label := *wire.Wire
	if label == "" {
		label = "default"
	}
	return printRows(label, runChecks(context.Background(), strings.TrimSuffix(*url, "/")+"/mcp", opts...))
}
