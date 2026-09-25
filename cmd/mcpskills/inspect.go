package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/cmd/common"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/skills"
	"github.com/spf13/cobra"
)

func newInspectCmd() *cobra.Command {
	var (
		urlFlag  string
		jsonFlag bool
		clientID string
	)
	cmd := &cobra.Command{
		Use:   "inspect [url]",
		Short: "Inspect any SEP-2640-compliant MCP server",
		Long: `Connect to an MCP server, check whether it advertises the
io.modelcontextprotocol/skills capability, enumerate its skills via
skills/list, and verify every file each skill pins (SHA-256 digest,
byte size, and SKILL.md frontmatter) against the served bytes.

Works against any spec-compliant server: mcpkit, the TypeScript SDK
reference impl, the PHP SDK, anything that follows SEP-2640's wire
shape.

URL precedence (highest first):
  1. positional [url] argument
  2. --url flag
  3. $MCPSKILLS_INSPECT_URL env var
  4. http://localhost:8080/mcp

Examples:
  mcpskills inspect http://localhost:8080/mcp
  mcpskills inspect http://localhost:8080/mcp --json
  MCPSKILLS_INSPECT_URL=http://other-impl.example.com/mcp mcpskills inspect`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			positional := ""
			if len(args) == 1 {
				positional = args[0]
			}
			url := common.LookupURL(firstNonEmpty(positional, urlFlag), "MCPSKILLS_INSPECT_URL", "http://localhost:8080/mcp")

			out := cmd.OutOrStdout()
			painter := common.NewPainter(parseColorMode(colorFlag), out)

			mcp := client.NewClient(url, core.ClientInfo{Name: clientID, Version: version})
			if err := mcp.Connect(context.Background()); err != nil {
				return fmt.Errorf("connect %s: %w", url, err)
			}
			defer mcp.Close()

			sc := skills.NewClient(mcp)
			report := &inspectReport{URL: url, ClientInfo: clientID}

			report.CapabilityDeclared = sc.SupportsSkills()
			if !report.CapabilityDeclared {
				if jsonFlag {
					return writeJSON(out, report)
				}
				return renderText(out, painter, report)
			}

			entries, err := sc.ListSkillEntries(cmd.Context())
			if err != nil {
				return fmt.Errorf("skills/list: %w", err)
			}
			report.Entries = make([]inspectEntry, 0, len(entries))
			for _, e := range entries {
				row := inspectSkill(cmd.Context(), sc, e)
				if row.Verified != nil && !*row.Verified {
					report.HasFailures = true
				}
				report.Entries = append(report.Entries, row)
			}

			if jsonFlag {
				if err := writeJSON(out, report); err != nil {
					return err
				}
			} else {
				if err := renderText(out, painter, report); err != nil {
					return err
				}
			}
			if report.HasFailures {
				return fmt.Errorf("one or more skills failed verification")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&urlFlag, "url", "", "server URL (overridden by positional arg; falls back to $MCPSKILLS_INSPECT_URL)")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "emit a JSON report instead of human-readable output")
	cmd.Flags().StringVar(&clientID, "client-id", "mcpskills-inspect", "ClientInfo.Name advertised in the initialize handshake")
	return cmd
}

type inspectReport struct {
	URL                string         `json:"url"`
	ClientInfo         string         `json:"clientInfo"`
	CapabilityDeclared bool           `json:"capabilityDeclared"`
	Entries            []inspectEntry `json:"entries,omitempty"`
	HasFailures        bool           `json:"hasFailures"`
}

type inspectEntry struct {
	Name string `json:"name"`
	// URL is the skill's SKILL.md URI (the entry's uri on the wire). The
	// JSON key predates skills/list and is kept so existing scripts parse.
	URL string `json:"url"`
	// Digest is the pinned digest of the SKILL.md itself.
	Digest      string `json:"digest,omitempty"`
	Description string `json:"description,omitempty"`
	// Files counts the entry's pinned resources, SKILL.md included. Zero
	// for a dynamic entry.
	Files int `json:"files"`
	// Dynamic is true when the server publishes the "dynamic" sentinel
	// instead of a resource list, so there is nothing to verify against.
	Dynamic bool `json:"dynamic,omitempty"`
	// Verified is true when every pinned file matched, false on any
	// mismatch or read failure, and nil for a dynamic entry.
	Verified *bool  `json:"verified,omitempty"`
	Error    string `json:"error,omitempty"`
}

// inspectSkill verifies every file an entry pins through ReadFromEntry,
// stopping at the first failure. The SKILL.md is always checked, so a
// manifest that omits it fails with ErrURINotInResources.
func inspectSkill(ctx context.Context, sc *skills.Client, e skills.SkillEntry) inspectEntry {
	row := inspectEntry{
		Name:        e.Name(),
		URL:         e.URI,
		Description: e.Description(),
		Files:       len(e.Resources.Files),
		Dynamic:     e.Resources.Dynamic,
	}
	if e.Resources.Dynamic {
		return row
	}
	uris := []string{e.URI}
	for _, f := range e.Resources.Files {
		if f.URI == e.URI {
			row.Digest = f.Digest
			continue
		}
		uris = append(uris, f.URI)
	}
	ok := true
	for _, uri := range uris {
		if _, err := sc.ReadFromEntry(ctx, e, uri); err != nil {
			ok = false
			row.Error = err.Error()
			break
		}
	}
	row.Verified = &ok
	return row
}

func renderText(out io.Writer, p *common.Painter, r *inspectReport) error {
	fmt.Fprintf(out, "mcpskills inspect — %s\n", p.Cyan(r.URL))
	if !r.CapabilityDeclared {
		fmt.Fprintf(out, "  capability: %s io.modelcontextprotocol/skills NOT advertised\n", p.Red("✗"))
		return nil
	}
	fmt.Fprintf(out, "  capability: %s io.modelcontextprotocol/skills declared\n", p.Green("✓"))
	if len(r.Entries) == 0 {
		fmt.Fprintf(out, "  skills/list: empty (not proof of absence, a host MAY still fetch skills by URI via skills/get)\n")
		return nil
	}
	fmt.Fprintf(out, "  skills/list: %d %s\n", len(r.Entries), pluralize(len(r.Entries), "skill", "skills"))
	for _, e := range r.Entries {
		marker := p.Dim("·")
		status := p.Dim("(dynamic, nothing pinned)")
		shape := "dynamic"
		if !e.Dynamic {
			shape = fmt.Sprintf("%d %s", e.Files, pluralize(e.Files, "file", "files"))
		}
		if e.Verified != nil {
			if *e.Verified {
				marker = p.Green("✓")
				status = "digest verified"
			} else {
				marker = p.Red("✗")
				status = p.Red("verification FAILED")
				if e.Error != "" {
					status = p.Red("verification FAILED — ") + p.Dim(e.Error)
				}
			}
		}
		fmt.Fprintf(out, "    %s %-30s [%-9s] %s\n", marker, e.Name, shape, status)
		fmt.Fprintf(out, "        %s\n", p.Dim(e.URL))
	}
	return nil
}

func writeJSON(out io.Writer, r *inspectReport) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func parseColorMode(s string) common.ColorMode {
	switch strings.ToLower(s) {
	case "always":
		return common.ColorAlways
	case "never":
		return common.ColorNever
	}
	return common.ColorAuto
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

// Used only to satisfy the linter when os import isn't otherwise present.
var _ = os.Stdin
