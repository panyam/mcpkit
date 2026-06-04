// Example: SEP-2640 skills extension — server fixture for the
// conformance suite and minimal worked example.
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
//
// The walkthrough.go scripted demo is deferred — see mcpkit#566 for
// the demokit walkthrough sweep that will turn this into a fully
// documented example.
package main

import (
	"flag"
	"fmt"
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
	fmt.Println("examples/skills — see --serve to run the MCP server.")
	fmt.Println("Conformance: cd ../../conformance && make testconf-skills")
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
