// Package mcp holds the skills-core server: the commit-helper skill and the
// format_commit tool it points at. It imports neither demokit nor otel, so
// the browser build in ../web stays small (#1714).
package mcp

import (
	"embed"
	"fmt"
	"io/fs"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/skills"
	"github.com/panyam/mcpkit/server"
)

//go:embed skills
var skillsFS embed.FS

// Register serves the skills and the format_commit tool on srv. An empty
// skillsDir serves the skills embedded from ./skills. Any other value reads
// them from that directory on disk, which a browser build can't do.
func Register(srv *server.Server, skillsDir string) error {
	opt := skills.WithDirectory(skillsDir)
	if skillsDir == "" {
		sub, err := fs.Sub(skillsFS, "skills")
		if err != nil {
			return err
		}
		opt = skills.WithFS(sub)
	}
	provider, err := skills.NewProvider(opt)
	if err != nil {
		return fmt.Errorf("skills.NewProvider: %w", err)
	}
	provider.RegisterWith(srv)
	registerTools(srv)
	return nil
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
