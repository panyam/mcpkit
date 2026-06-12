package main

import (
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
		urlFlag   string
		jsonFlag  bool
		clientID  string
	)
	cmd := &cobra.Command{
		Use:   "inspect [url]",
		Short: "Inspect any SEP-2640-compliant MCP server",
		Long: `Connect to an MCP server, check whether it advertises the
io.modelcontextprotocol/skills capability, fetch its skill://index.json,
and verify the SHA-256 digest of every cataloged skill against the
served bytes.

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
			if err := mcp.Connect(); err != nil {
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

			idx, err := sc.ListSkills(cmd.Context())
			if err != nil {
				return fmt.Errorf("ListSkills: %w", err)
			}
			report.IndexSchema = idx.Schema
			report.Entries = make([]inspectEntry, 0, len(idx.Skills))

			for _, e := range idx.Skills {
				row := inspectEntry{
					Name:        e.Name,
					Type:        string(e.Type),
					URL:         e.URL,
					Digest:      e.Digest,
					Description: e.Description,
				}
				result, verifyErr := sc.ReadAndVerify(cmd.Context(), e.URL, e.Digest)
				switch {
				case verifyErr == nil && result.DigestVerified:
					t := true
					row.Verified = &t
				case verifyErr != nil:
					f := false
					row.Verified = &f
					row.Error = verifyErr.Error()
					report.HasFailures = true
				default:
					f := false
					row.Verified = &f
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
				return fmt.Errorf("one or more digest mismatches")
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
	IndexSchema        string         `json:"indexSchema,omitempty"`
	Entries            []inspectEntry `json:"entries,omitempty"`
	HasFailures        bool           `json:"hasFailures"`
}

type inspectEntry struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	URL         string `json:"url"`
	Digest      string `json:"digest,omitempty"`
	Description string `json:"description,omitempty"`
	// Verified is true on digest match, false on mismatch or read
	// failure. Pointer-of-bool is retained so a future SEP revision
	// that re-introduces a digest-less entry type can flow through
	// without a struct-shape change.
	Verified *bool  `json:"verified,omitempty"`
	Error    string `json:"error,omitempty"`
}

func renderText(out io.Writer, p *common.Painter, r *inspectReport) error {
	fmt.Fprintf(out, "mcpskills inspect — %s\n", p.Cyan(r.URL))
	if !r.CapabilityDeclared {
		fmt.Fprintf(out, "  capability: %s io.modelcontextprotocol/skills NOT advertised\n", p.Red("✗"))
		return nil
	}
	fmt.Fprintf(out, "  capability: %s io.modelcontextprotocol/skills declared\n", p.Green("✓"))
	if len(r.Entries) == 0 {
		fmt.Fprintf(out, "  index: empty or absent (host MAY still load skills by URI)\n")
		return nil
	}
	fmt.Fprintf(out, "  index: %s (%d %s)\n", p.Cyan("skill://index.json"), len(r.Entries), pluralize(len(r.Entries), "entry", "entries"))
	if r.IndexSchema != "" {
		fmt.Fprintf(out, "         $schema: %s\n", p.Dim(r.IndexSchema))
	}
	for _, e := range r.Entries {
		marker := p.Dim("·")
		status := p.Dim("(no digest)")
		if e.Verified != nil {
			if *e.Verified {
				marker = p.Green("✓")
				status = "digest verified"
			} else {
				marker = p.Red("✗")
				status = p.Red("digest MISMATCH")
				if e.Error != "" {
					status = p.Red("digest MISMATCH — ") + p.Dim(e.Error)
				}
			}
		}
		fmt.Fprintf(out, "    %s %-30s [%-21s] %s\n", marker, e.Name, e.Type, status)
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
