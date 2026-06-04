package main

import (
	"fmt"
	"log"
	"strings"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/skills"
	"github.com/panyam/mcpkit/server"
	"github.com/spf13/cobra"
)

func newServeCmd() *cobra.Command {
	var (
		addr      string
		modeFlag  string
		skillsDir string
	)
	cmd := &cobra.Command{
		Use:   "serve [dir]",
		Short: "Serve a skills directory via MCP",
		Long: `Boot an MCP server that publishes every skill under [dir]
following SEP-2640. The server declares the
io.modelcontextprotocol/skills capability and auto-registers
skill://index.json.

The default --mode=file publishes each file as an individual MCP
resource. --mode=archive (or --mode=zip) flips to archive distribution
where each skill is one packed resource.

Examples:
  mcpskills serve ./my-skills
  mcpskills serve ./my-skills --addr :9090
  mcpskills serve ./my-skills --mode archive`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := skillsDir
			if len(args) == 1 {
				dir = args[0]
			}
			if dir == "" {
				return fmt.Errorf("serve: missing skills directory (positional or --skills)")
			}

			opts := []skills.ProviderOption{skills.WithDirectory(dir)}
			switch strings.ToLower(modeFlag) {
			case "", "file":
				// default
			case "archive", "tar.gz", "targz":
				opts = append(opts, skills.WithArchiveMode(skills.ArchiveFormatTarGz))
			case "zip":
				opts = append(opts, skills.WithArchiveMode(skills.ArchiveFormatZip))
			default:
				return fmt.Errorf("serve: invalid --mode %q (want file|archive|zip)", modeFlag)
			}

			provider, err := skills.NewProvider(opts...)
			if err != nil {
				return fmt.Errorf("serve: NewProvider: %w", err)
			}

			srv := server.NewServer(
				core.ServerInfo{Name: "mcpskills", Version: version},
				server.WithListen(addr),
			)
			provider.RegisterWith(srv)

			log.Printf("mcpskills: serving %s (mode=%s) on %s", dir, modeOrDefault(modeFlag), addr)
			return srv.ListenAndServe(server.WithStreamableHTTP(true))
		},
	}
	cmd.Flags().StringVar(&addr, "addr", ":8080", "listen address")
	cmd.Flags().StringVar(&modeFlag, "mode", "file", "distribution mode: file | archive | zip")
	cmd.Flags().StringVar(&skillsDir, "skills", "", "skills directory (alternative to positional arg)")
	return cmd
}

func modeOrDefault(m string) string {
	if m == "" {
		return "file"
	}
	return m
}
