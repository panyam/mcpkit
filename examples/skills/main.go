// Example: SEP-2640 skills extension — server fixture + demokit walkthrough.
//
// Two-process architecture (matches the events/discord and tasks examples):
//
//	Terminal 1:  make serve         # skills server on :8080 in file mode
//	Terminal 2:  make demo          # walkthrough (--tui for interactive TUI)
//
// Walks the bundled skills/ directory through ext/skills.SkillProvider
// and exposes each skill either as individual file resources (file
// mode, default) or as one archive resource per skill (archive mode,
// --mode=archive). The skill://index.json discovery resource is
// auto-registered. The io.modelcontextprotocol/skills capability is
// declared in the initialize response.
//
// Run modes:
//
//	go run . --serve                      # file mode on :8080
//	go run . --serve --bundle=archive     # one .tar.gz per skill
//	go run . --serve --bundle=zip         # one .zip per skill
//	go run . --serve --addr=:9090         # different port
//	go run .                              # walkthrough (against --url, default localhost:8080)
//	go run . --tui                        # walkthrough in interactive TUI
//	go run . --note                       # walkthrough in notebook mode
//	go run . --doc=md                     # regenerate WALKTHROUGH.md
//
// The --bundle name is deliberate: demokit's FilterArgs reserves the
// bare --mode flag for its own renderer selection (tui / plain /
// notebook). Naming the example's distribution flag --mode silently
// strips it before flag.Parse, leaving the server in file mode no
// matter what the operator passed.
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
	modeFlag := flag.String("bundle", "file",
		"distribution shape: file (per-resource SKILL.md + supporting files) | archive (one .tar.gz per skill) | zip (one .zip per skill)")
	skillsDir := flag.String("skills", "skills",
		"directory of skill bundles to register (default ./skills)")
	tel := common.RegisterTelemetryFlags(flag.CommandLine)
	flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:],
		demokit.BoolFlag("--serve"),  // dual-mode dispatch; override demokit's value-form default
		demokit.ValueFlag("--url"),   // walkthrough-side flag; strip from serve args
		demokit.ValueFlag("--wire"),  // walkthrough-side flag; strip from serve args
	))

	tp, shutdown, err := commonotel.SetupTelemetry(context.Background(),
		commonotel.WithExporter(*tel.Exporter),
		commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
		commonotel.WithServiceName("skills-demo"),
	)
	if err != nil {
		log.Fatalf("commonotel.SetupTelemetry: %v", err)
	}
	defer shutdown(context.Background())

	provOpts := []skills.ProviderOption{
		skills.WithDirectory(*skillsDir),
	}
	switch strings.ToLower(*modeFlag) {
	case "file":
		// default
	case "archive", "tar.gz", "targz":
		provOpts = append(provOpts, skills.WithArchiveMode(skills.ArchiveFormatTarGz))
	case "zip":
		provOpts = append(provOpts, skills.WithArchiveMode(skills.ArchiveFormatZip))
	default:
		log.Fatalf("invalid --bundle: %q (want file|archive|zip)", *modeFlag)
	}

	provider, err := skills.NewProvider(provOpts...)
	if err != nil {
		log.Fatalf("skills.NewProvider: %v", err)
	}

	log.Printf("[skills-demo] mode=%s skills=%s", *modeFlag, *skillsDir)
	if err := common.RunServer(common.ServerConfig{
		Name:           "skills-demo",
		Addr:           *addr,
		LogPrefix:      "[skills] ",
		TracerProvider: tp,
		Register: func(srv *server.Server) {
			provider.RegisterWith(srv)
		},
	}); err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}

