package files

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/panyam/mcpkit/experimental/agent"
	"github.com/panyam/mcpkit/experimental/agent/host"
)

// Extension contributes the file tools and the prompt that tells a model how
// to drive them.
//
// The two travel together because neither works alone. The tools refuse an
// edit whose anchor is not unique, and a model that has not been told that
// will keep sending single-word anchors and keep being refused. Registering
// the tools without the prompt would ship a mechanism whose contract is
// discoverable only by failing.
type Extension struct {
	host.BaseExtension
	src *Source
}

// New builds the extension. Root confines every path the tools will touch and
// is required; see Config.
func New(cfg Config) (*Extension, error) {
	src, err := NewSource(cfg)
	if err != nil {
		return nil, err
	}
	return &Extension{src: src}, nil
}

// Name identifies the extension, and is the source id its tools register
// under.
func (e *Extension) Name() string { return "files" }

// Tools returns the workspace tool set: read_file, edit_file, write_file,
// list_files, and search_files.
func (e *Extension) Tools() (agent.ToolSource, error) { return e.src, nil }

// PromptSections states the edit discipline the tools enforce.
func (e *Extension) PromptSections() []host.PromptSection {
	return []host.PromptSection{host.PromptSectionFunc(func(context.Context) string {
		return `## Editing files

Read a file before editing it, and pass the hash read_file returned as expect_hash.
An edit whose expect_hash no longer matches is refused, because the file changed
after you read it and your edit was written against content that is gone. Read it
again and redo the edit against what is there now.

Each edit replaces an exact snippet. The text in ` + "`old`" + ` must appear exactly once in
the file and is matched byte for byte, including indentation. If it appears more
than once the edit is refused rather than applied to a guess: add surrounding
lines until it is unique. All edits in one call apply together or none do.`
	})}
}

// ApprovalRenderers shows an edit as the change it makes, rather than as its
// arguments.
//
// The default host prompt renders a call's JSON trimmed to 200 characters. For
// most tools that is right: the arguments are an opaque payload and the
// question is really "do you trust this tool with roughly this". For an edit
// the arguments ARE the change, and 200 characters of JSON truncates the one
// thing the user is being asked to judge, so they end up approving a diff they
// could not read.
func (e *Extension) ApprovalRenderers() []host.ApprovalRenderer {
	return []host.ApprovalRenderer{renderEditApproval}
}

// renderEditApproval claims edit_file and write_file, and declines everything
// else so the default still covers tools this package does not own.
func renderEditApproval(_ context.Context, info agent.ToolCallInfo) (string, bool) {
	args := map[string]any{}
	if raw := info.Call.Args.Raw(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			// Unparseable arguments are exactly when the generic rendering is
			// the honest one: it shows the raw text rather than a summary
			// built from a guess.
			return "", false
		}
	}
	path, _ := args["path"].(string)
	if path == "" {
		return "", false
	}

	switch info.Call.Name {
	case "edit_file":
		hunks, err := parseHunks(args["edits"])
		if err != nil || len(hunks) == 0 {
			return "", false
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Apply %d change(s) to %s?\n", len(hunks), path)
		for _, h := range hunks {
			b.WriteString("\n")
			writeDiff(&b, h.Old, h.New)
		}
		return b.String(), true

	case "write_file":
		content, ok := args["content"].(string)
		if !ok {
			return "", false
		}
		verb := "Create"
		if _, replacing := args["expect_hash"].(string); replacing {
			verb = "Replace"
		}
		return fmt.Sprintf("%s %s (%d bytes, %d lines)?\n\n%s",
			verb, path, len(content), strings.Count(content, "\n")+1, indent(preview(content))), true
	}
	return "", false
}

// writeDiff renders one hunk as removed and added lines.
func writeDiff(b *strings.Builder, old, nw string) {
	for _, line := range strings.Split(strings.TrimRight(old, "\n"), "\n") {
		fmt.Fprintf(b, "  - %s\n", line)
	}
	if nw == "" {
		return
	}
	for _, line := range strings.Split(strings.TrimRight(nw, "\n"), "\n") {
		fmt.Fprintf(b, "  + %s\n", line)
	}
}

// preview caps a whole-file write at a readable length. This is the renderer
// deciding its own truncation, which is the point of the seam: the cap that
// suits an opaque payload is not the cap that suits a diff.
func preview(s string) string {
	const maxLines = 40
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	return strings.Join(lines[:maxLines], "\n") + fmt.Sprintf("\n… %d more line(s)", len(lines)-maxLines)
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(s, "\n", "\n  ")
}

var _ host.Extension = (*Extension)(nil)
