package files

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

// Config configures the tool source.
type Config struct {
	// Root confines every path these tools will touch. Required.
	//
	// It has no "unset means anywhere" mode on purpose. These tools are
	// driven by a model, and a model's instructions can come from content it
	// read rather than from the user (see agent.Spotlight for why that is not
	// distinguishable after the fact). An unconfined editor turns any such
	// instruction into a write to an arbitrary path, so the confinement is
	// the tool's, not a policy the caller can forget to add.
	Root string
}

// Source serves read_file and edit_file over agent.ToolSource.
//
// The pairing is deliberate. edit_file's staleness check needs a hash the
// caller can only have obtained by reading, so a read tool that did not
// return one would make the precondition unusable and leave the whole
// mechanism as decoration.
type Source struct {
	root string
	defs []core.ToolDef
}

// NewSource builds the source, resolving and verifying Root.
func NewSource(cfg Config) (*Source, error) {
	if cfg.Root == "" {
		return nil, fmt.Errorf("files: Config.Root is required")
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return nil, fmt.Errorf("files: resolve root %s: %w", cfg.Root, err)
	}
	if root, err = filepath.EvalSymlinks(root); err != nil {
		return nil, fmt.Errorf("files: resolve root %s: %w", cfg.Root, err)
	}
	return &Source{root: root, defs: toolDefs()}, nil
}

func toolDefs() []core.ToolDef {
	return []core.ToolDef{
		{
			Name:        "read_file",
			Title:       "Read a file",
			Description: "Read a text file. Returns its content and a hash of that content. Pass the hash to edit_file as expect_hash so the edit is refused if the file changed in between.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path to the file, relative to the workspace root.",
					},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:  "edit_file",
			Title: "Edit a file",
			Description: "Replace exact snippets of text in a file. Each edit's `old` must appear exactly once; " +
				"include surrounding lines to make it unique. All edits apply together or none do. " +
				"Pass expect_hash from the read_file that produced your view of the file.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path to the file, relative to the workspace root.",
					},
					"expect_hash": map[string]any{
						"type":        "string",
						"description": "The hash returned by read_file for the content these edits were written against.",
					},
					"edits": map[string]any{
						"type":        "array",
						"minItems":    1,
						"description": "Replacements to apply together.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"old": map[string]any{
									"type":        "string",
									"description": "Exact text to replace. Must appear exactly once. Matched byte-for-byte: whitespace and indentation must be reproduced exactly.",
								},
								"new": map[string]any{
									"type":        "string",
									"description": "Replacement text. Empty deletes the matched text.",
								},
							},
							"required": []string{"old", "new"},
						},
					},
				},
				"required": []string{"path", "expect_hash", "edits"},
			},
		},
	}
}

// Tools returns the two definitions, in a stable order.
func (s *Source) Tools(context.Context) ([]core.ToolDef, error) {
	out := make([]core.ToolDef, len(s.defs))
	copy(out, s.defs)
	return out, nil
}

// Call dispatches read_file and edit_file.
//
// A tool that ran and refused reports through ToolResult.IsError rather than
// a returned error, so the Runner feeds the refusal back to the model instead
// of aborting the turn. That distinction is the whole point here: "your
// anchor matched twice, add context" is a message the model can act on, and
// the ToolSource contract reserves a returned error for a dispatch that never
// happened.
func (s *Source) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	switch name {
	case "read_file":
		return s.read(args), nil
	case "edit_file":
		return s.edit(args), nil
	default:
		return nil, fmt.Errorf("files: unknown tool %q", name)
	}
}

func (s *Source) read(args map[string]any) *core.ToolResult {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return toolError("read_file needs a path")
	}
	abs, err := s.resolve(path)
	if err != nil {
		return toolError(err.Error())
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return toolError(fmt.Sprintf("cannot read %s: %v", path, err))
	}
	content := string(b)
	hash := Hash(content)
	return &core.ToolResult{
		Content: []core.Content{{
			Type: "text",
			Text: fmt.Sprintf("path: %s\nhash: %s\n\n%s", path, hash, content),
		}},
		StructuredContent: map[string]any{"path": path, "hash": hash, "content": content},
	}
}

func (s *Source) edit(args map[string]any) *core.ToolResult {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return toolError("edit_file needs a path")
	}
	expect, ok := args["expect_hash"].(string)
	if !ok || expect == "" {
		return toolError("edit_file needs expect_hash; read the file first and pass the hash it returned")
	}
	hunks, err := parseHunks(args["edits"])
	if err != nil {
		return toolError(err.Error())
	}
	abs, err := s.resolve(path)
	if err != nil {
		return toolError(err.Error())
	}

	info, err := os.Stat(abs)
	if err != nil {
		return toolError(fmt.Sprintf("cannot read %s: %v", path, err))
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return toolError(fmt.Sprintf("cannot read %s: %v", path, err))
	}

	out, err := (Edit{ExpectHash: expect, Hunks: hunks}).Apply(string(b))
	if err != nil {
		return toolError(fmt.Sprintf("%s: %v", path, err))
	}
	if err := writeFile(abs, out, info.Mode().Perm()); err != nil {
		return toolError(fmt.Sprintf("cannot write %s: %v", path, err))
	}

	hash := Hash(out)
	return &core.ToolResult{
		Content: []core.Content{{
			Type: "text",
			Text: fmt.Sprintf("edited %s, %d edit(s) applied\nhash: %s", path, len(hunks), hash),
		}},
		StructuredContent: map[string]any{"path": path, "hash": hash, "edits": len(hunks)},
	}
}

// parseHunks converts the edits argument, which arrives as decoded JSON, into
// hunks. A malformed edits list is reported rather than silently skipped: an
// edit the model believes it made and that never applied is the same class of
// failure as a stale write.
func parseHunks(raw any) ([]Hunk, error) {
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil, fmt.Errorf("edit_file needs a non-empty edits array")
	}
	hunks := make([]Hunk, 0, len(list))
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("edit %d is not an object", i)
		}
		old, ok := m["old"].(string)
		if !ok {
			return nil, fmt.Errorf("edit %d has no `old` string", i)
		}
		// A missing `new` is a deletion, which is a legitimate edit, so it is
		// not required to be present. A non-string `new` is still malformed.
		nw, _ := m["new"].(string)
		if v, present := m["new"]; present {
			if _, isString := v.(string); !isString && v != nil {
				return nil, fmt.Errorf("edit %d has a non-string `new`", i)
			}
		}
		hunks = append(hunks, Hunk{Old: old, New: nw})
	}
	return hunks, nil
}

// resolve turns a tool-supplied path into an absolute one inside Root, or
// refuses.
//
// Symlinks are resolved before the containment check, because a link inside
// the root pointing out of it would otherwise pass a purely lexical test. The
// check is applied to the parent directory rather than the file so that a
// path whose final component does not exist yet still resolves.
func (s *Source) resolve(path string) (string, error) {
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(s.root, abs)
	}
	abs = filepath.Clean(abs)

	dir, base := filepath.Split(abs)
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("cannot resolve %s: %v", path, err)
	}
	resolved := filepath.Join(realDir, base)

	rel, err := filepath.Rel(s.root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("refusing %s: outside the workspace root", path)
	}
	return resolved, nil
}

// writeFile replaces a file's contents via a temp file and a rename, so a
// failure partway through leaves the original intact rather than truncated.
// The engine guarantees all-or-nothing for the edit; this is the same promise
// at the filesystem.
func writeFile(path, content string, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".files-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func toolError(msg string) *core.ToolResult {
	return &core.ToolResult{
		Content: []core.Content{{Type: "text", Text: msg}},
		IsError: true,
	}
}

// EditPaths reports the files an edit_file call is about to write, in the
// shape a checkpoint WriteSpec wants:
//
//	checkpoint.WriteSpec{Tool: "edit_file", Paths: files.EditPaths}
//
// It is a plain function rather than a WriteSpec so that this package does
// not import the checkpoint package and checkpoint does not import this one.
// The two features compose at the wiring layer, keyed by tool name, which is
// what keeps either free to change without the other's API stabilizing around
// it.
func EditPaths(args map[string]any) []string {
	p, ok := args["path"].(string)
	if !ok || p == "" {
		return nil
	}
	return []string{p}
}

var _ agent.ToolSource = (*Source)(nil)
