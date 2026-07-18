// Example: SEP-2640 skills — the MINIMAL shape (skills file + tool handling).
//
// This is the scoped-down core the Skills WG blessed on 2026-06-30
// (discussion 2994): serve a skills file over the Resources primitive, plus
// tool handling, consumed load-on-demand. Deliberately NO archives, NO
// remote sources, NO fsnotify — those are the deferred / extended surface
// and live in examples/skills.
//
// Two-process architecture (matches the other non-UI examples):
//
//	Terminal 1:  just serve   # skills-core server on :8080 (file mode)
//	Terminal 2:  just demo    # walkthrough (--tui for interactive TUI)
//
// The server serves the bundled skills/ directory as skill:// resources,
// auto-registers skill://index.json, declares the io.modelcontextprotocol/skills
// capability, and registers the format_commit tool that the commit-helper
// skill instructs the host to call.
//
// Run modes:
//
//	go run . --serve            # file mode on :8080
//	go run . --serve --addr=:9090
//	go run .                    # walkthrough (against --url, default localhost:8080)
//	go run . --tui              # walkthrough in interactive TUI
//	go run . --doc=md           # regenerate WALKTHROUGH.md
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	commonotel "github.com/panyam/mcpkit/examples/common/otel"
	"github.com/panyam/mcpkit/ext/skills"
	"github.com/panyam/mcpkit/server"
)

func main() {
	for _, arg := range os.Args[1:] {
		if strings.TrimSpace(arg) == "--serve" {
			serve()
			return
		}
	}
	runDemo()
}

func serve() {
	addr := flag.String("addr", ":8080", "listen address")
	skillsDir := flag.String("skills", "skills", "directory of skills to serve (file mode)")
	tel := common.RegisterTelemetryFlags(flag.CommandLine)
	wire := common.RegisterWireFlags(flag.CommandLine)
	flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:],
		demokit.BoolFlag("--serve"), // dual-mode dispatch; override demokit's value-form default
		demokit.ValueFlag("--url"),  // walkthrough-side flag; strip from serve args
	))

	tp, shutdown, err := commonotel.SetupTelemetry(context.Background(),
		commonotel.WithExporter(*tel.Exporter),
		commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
		commonotel.WithServiceName("skills-core"),
	)
	if err != nil {
		log.Fatalf("commonotel.SetupTelemetry: %v", err)
	}
	defer shutdown(context.Background())

	// File mode only: each file under a skill directory becomes a skill://
	// resource, and skill://index.json enumerates them with a SHA-256
	// digest. This is the whole blessed minimal shape — no WithArchiveMode,
	// no remote source adapters, no WithFSWatcher.
	provider, err := skills.NewProvider(skills.WithDirectory(*skillsDir))
	if err != nil {
		log.Fatalf("skills.NewProvider: %v", err)
	}

	log.Printf("[skills-core] file mode, skills=%s", *skillsDir)
	if err := common.RunServer(common.ServerConfig{
		Name:           "skills-core",
		Addr:           *addr,
		LogPrefix:      "[skills-core] ",
		TracerProvider: tp,
		Wire:           wire,
		Register: func(srv *server.Server) {
			provider.RegisterWith(srv)
			registerTools(srv)
		},
	}); err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}

// commitInput is the argument envelope for format_commit — the tool the
// commit-helper skill tells the host to call. A skill is guidance; the tool
// is the capability the guidance points at. Together they are the "skills
// file + tool handling" minimal shape.
type commitInput struct {
	Type    string `json:"type"`
	Scope   string `json:"scope,omitempty"`
	Summary string `json:"summary"`
}

// registerTools registers the tool the bundled skill references. In a real
// server these are the ordinary MCP tools you already expose; the skill just
// teaches the host when and how to use them well.
func registerTools(srv *server.Server) {
	tool := core.TextTool[commitInput]("format_commit",
		"Format a change description as a Conventional Commits subject line. The commit-helper skill instructs the host to call this.",
		func(_ core.ToolContext, in commitInput) (string, error) {
			if in.Type == "" || in.Summary == "" {
				return "", fmt.Errorf("type and summary are required")
			}
			if in.Scope != "" {
				return fmt.Sprintf("%s(%s): %s", in.Type, in.Scope, in.Summary), nil
			}
			return fmt.Sprintf("%s: %s", in.Type, in.Summary), nil
		},
	)
	srv.RegisterTool(tool.ToolDef, tool.Handler)
}
