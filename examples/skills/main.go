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
//	go run . --serve                    # file mode on :8080
//	go run . --serve --mode=archive     # archive mode on :8080
//	go run . --serve --addr=:9090       # different port
//	go run .                            # walkthrough (against --url, default localhost:8080)
//	go run . --tui                      # walkthrough in interactive TUI
//	go run . --note                     # walkthrough in notebook mode
//	go run . --doc=md                   # regenerate WALKTHROUGH.md
package main

import (
	"flag"
	"log"
	"os"
	"strings"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
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
	modeFlag := flag.String("mode", "file",
		"distribution mode: file (per-resource SKILL.md + supporting files) | archive (one .tar.gz per skill)")
	skillsDir := flag.String("skills", "skills",
		"directory of skill bundles to register (default ./skills)")
	flag.CommandLine.Parse(filterArgs(os.Args[1:], "--serve"))

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
		log.Fatalf("invalid --mode: %q (want file|archive|zip)", *modeFlag)
	}

	provider, err := skills.NewProvider(provOpts...)
	if err != nil {
		log.Fatalf("skills.NewProvider: %v", err)
	}

	srvOpts := common.MCPServerOptions(*addr, "[skills] ")
	srv := server.NewServer(
		core.ServerInfo{Name: "skills-demo", Version: "0.1.0"},
		srvOpts...,
	)
	provider.RegisterWith(srv)

	log.Printf("[skills-demo] mode=%s skills=%s listening on %s", *modeFlag, *skillsDir, *addr)
	if err := srv.ListenAndServe(server.WithStreamableHTTP(true)); err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}

// filterArgs drops dispatch-style flags like --serve before handing
// the remaining args to flag.Parse.
func filterArgs(args []string, drop ...string) []string {
	dropSet := make(map[string]struct{}, len(drop))
	for _, d := range drop {
		dropSet[d] = struct{}{}
	}
	out := make([]string, 0, len(args))
	for _, a := range args {
		if _, hit := dropSet[a]; hit {
			continue
		}
		out = append(out, a)
	}
	return out
}
