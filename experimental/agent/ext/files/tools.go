package files

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/agent"
)

// Config configures the tool source.
type Config struct {
	// Roots confine every path these tools will touch. At least one is
	// required, and the first is primary: a relative path resolves against it.
	//
	// A set rather than a single directory because a session that stays inside
	// one repository is the exception. The ordinary task spans repositories,
	// and an agent confined to one of them edits an API and reports success
	// while the caller it broke sits in another (issue 1314).
	//
	// There is no "unset means anywhere" mode on purpose. These tools are
	// driven by a model, and a model's instructions can come from content it
	// read rather than from the user (see agent.Spotlight for why that is not
	// distinguishable after the fact). An unconfined editor turns any such
	// instruction into a write to an arbitrary path.
	//
	// Naming a common parent instead of listing the roots is not the same
	// thing and is not a shortcut: it silently widens confinement to
	// everything else under that parent, which is the property this exists to
	// deny.
	Roots []string

	// Exclude names directories that list_files and search_files skip, by
	// base name or by a path.Match pattern. Nil means DefaultExclude; an
	// explicitly empty non-nil slice means exclude nothing.
	//
	// This is not .gitignore and deliberately does not read one. See
	// DefaultExclude for why the two questions are different.
	Exclude []string
}

// Source serves the workspace tools over agent.ToolSource: read_file,
// edit_file, write_file, list_files, and search_files.
//
// The pairing is deliberate. edit_file's staleness check needs a hash the
// caller can only have obtained by reading, so a read tool that did not
// return one would make the precondition unusable and leave the whole
// mechanism as decoration.
type Source struct {
	roots   []*workspaceRoot
	exclude []string
	defs    []core.ToolDef
}

// workspaceRoot is one confined directory: the handle that enforces it and the
// absolute path used to render results back to the model.
type workspaceRoot struct {
	path string
	h    *os.Root
}

// NewSource builds the source, opening Root as a confined directory handle.
//
// The handle is the confinement. Checking a path and then opening it by name
// are two operations, and every escape this package cares about lives in the
// gap between them: a symlink is followed at open time, not at check time, and
// the filesystem can change in between. os.Root resolves each component at
// open time against the directory it holds, so the check and the use are the
// same act.
func NewSource(cfg Config) (*Source, error) {
	if len(cfg.Roots) == 0 {
		return nil, fmt.Errorf("files: Config.Roots needs at least one directory")
	}
	var roots []*workspaceRoot
	for _, r := range cfg.Roots {
		if r == "" {
			return nil, fmt.Errorf("files: Config.Roots contains an empty path")
		}
		abs, err := filepath.Abs(r)
		if err != nil {
			return nil, fmt.Errorf("files: resolve root %s: %w", r, err)
		}
		h, err := os.OpenRoot(abs)
		if err != nil {
			return nil, fmt.Errorf("files: open root %s: %w", r, err)
		}
		roots = append(roots, &workspaceRoot{path: abs, h: h})
	}
	// Nil means "the caller did not choose", empty means "the caller chose
	// nothing". Collapsing the two would make excluding nothing impossible.
	exclude := cfg.Exclude
	if exclude == nil {
		exclude = DefaultExclude
	}
	return &Source{roots: roots, exclude: exclude, defs: toolDefs()}, nil
}

// Close releases every root directory handle. A long-lived host need not call
// it; it exists so a test or a short-lived process does not leak a descriptor.
func (s *Source) Close() error {
	var firstErr error
	for _, r := range s.roots {
		if err := r.h.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
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
		{
			Name:  "write_file",
			Title: "Write a whole file",
			Description: "Create a new file, or replace an existing one entirely. " +
				"Omit expect_hash to create: the call is refused if the path already exists. " +
				"To replace an existing file, pass expect_hash from the read_file that produced your view of it. " +
				"Prefer edit_file for changing part of a file.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path to the file, relative to the workspace root.",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "The complete new contents of the file.",
					},
					"expect_hash": map[string]any{
						"type":        "string",
						"description": "The hash read_file returned for the content being replaced. Omit only when creating a file that does not exist yet.",
					},
				},
				"required": []string{"path", "content"},
			},
		},
		{
			Name:        "list_files",
			Title:       "List files",
			Description: "List files in the workspace, recursively. Use this to find what is there before reading or editing.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"dir": map[string]any{
						"type":        "string",
						"description": "Directory to list, relative to the workspace root. Omit for the whole workspace.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": fmt.Sprintf("Maximum paths to return. Default %d. The reply says how many were left out.", DefaultListLimit),
					},
				},
			},
		},
		{
			Name:  "search_files",
			Title: "Search file contents",
			Description: "Search the workspace for a regular expression, returning matching lines as path:line: text. " +
				"The query is a regex (RE2 syntax): escape it, or pass literal:true, to search for text containing regex characters.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Regular expression to match against each line.",
					},
					"literal": map[string]any{
						"type":        "boolean",
						"description": "Treat query as plain text rather than a regex. Use this when searching for something containing ( ) [ ] . * + ? | \\ or $.",
					},
					"dir": map[string]any{
						"type":        "string",
						"description": "Directory to search, relative to the workspace root. Omit for the whole workspace.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": fmt.Sprintf("Maximum matches to return. Default %d. The reply says how many were left out.", DefaultSearchLimit),
					},
				},
				"required": []string{"query"},
			},
		},
	}
}

// Tools returns the tool definitions, in a stable order.
func (s *Source) Tools(context.Context) ([]core.ToolDef, error) {
	out := make([]core.ToolDef, len(s.defs))
	copy(out, s.defs)
	return out, nil
}

// Call dispatches every tool Tools declares.
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
	case "write_file":
		return s.write(args), nil
	case "list_files":
		return s.list(args), nil
	case "search_files":
		return s.search(args), nil
	default:
		return nil, fmt.Errorf("files: unknown tool %q", name)
	}
}

func (s *Source) read(args map[string]any) *core.ToolResult {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return toolError("read_file needs a path")
	}
	wr, rel, err := s.resolve(path)
	if err != nil {
		return toolError(err.Error())
	}
	b, err := wr.h.ReadFile(rel)
	if err != nil {
		return toolError(escapeMessage(path, err))
	}
	content := string(b)
	hash := Hash(content)
	shown := wr.abs(rel)
	return &core.ToolResult{
		Content: []core.Content{{
			Type: "text",
			Text: fmt.Sprintf("path: %s\nhash: %s\n\n%s", shown, hash, content),
		}},
		StructuredContent: map[string]any{"path": shown, "hash": hash, "content": content},
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
	wr, rel, err := s.resolve(path)
	if err != nil {
		return toolError(err.Error())
	}

	info, err := wr.h.Stat(rel)
	if err != nil {
		return toolError(escapeMessage(path, err))
	}
	b, err := wr.h.ReadFile(rel)
	if err != nil {
		return toolError(escapeMessage(path, err))
	}

	out, err := (Edit{ExpectHash: expect, Hunks: hunks}).Apply(string(b))
	if err != nil {
		return toolError(fmt.Sprintf("%s: %v", path, err))
	}
	if err := s.writeThroughRoot(wr, rel, out, info.Mode().Perm()); err != nil {
		return toolError(fmt.Sprintf("cannot write %s: %v", path, err))
	}

	hash := Hash(out)
	return &core.ToolResult{
		Content: []core.Content{{
			Type: "text",
			Text: fmt.Sprintf("edited %s, %d edit(s) applied\nhash: %s", wr.abs(rel), len(hunks), hash),
		}},
		StructuredContent: map[string]any{"path": wr.abs(rel), "hash": hash, "edits": len(hunks)},
	}
}

// write replaces a file's entire contents, or creates it.
//
// The two cases are told apart by expect_hash rather than by a separate flag,
// and the rule is that there is no way to spell "overwrite whatever is there".
//
//	no expect_hash  -> create; refused if the path exists
//	expect_hash set -> replace, only if the content still hashes to it
//
// A create-or-overwrite mode would undo the whole module. edit_file exists to
// stop a change landing on content nobody looked at, and a write_file that
// clobbered on request would be the same hole with a different name, reachable
// by a model that found the anchor matching awkward.
func (s *Source) write(args map[string]any) *core.ToolResult {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return toolError("write_file needs a path")
	}
	content, ok := args["content"].(string)
	if !ok {
		return toolError("write_file needs content (pass an empty string to write an empty file)")
	}
	expect, _ := args["expect_hash"].(string)

	wr, rel, err := s.resolve(path)
	if err != nil {
		return toolError(err.Error())
	}

	info, statErr := wr.h.Stat(rel)
	switch {
	case statErr == nil && !info.Mode().IsRegular():
		return toolError(fmt.Sprintf("refusing %s: it is not a regular file", path))

	case statErr == nil && expect == "":
		return toolError(fmt.Sprintf(
			"refusing %s: it already exists. Read it first and pass expect_hash to replace it, or use edit_file to change part of it.", path))

	case statErr == nil:
		b, err := wr.h.ReadFile(rel)
		if err != nil {
			return toolError(escapeMessage(path, err))
		}
		if got := Hash(string(b)); got != expect {
			return toolError(fmt.Sprintf(
				"%s: %v: expected %s, found %s; re-read it before writing", path, ErrStale, expect, got))
		}

	case !errors.Is(statErr, os.ErrNotExist):
		// A path that escapes the root lands here rather than in the create
		// branch, so a refusal is never mistaken for "does not exist yet".
		return toolError(escapeMessage(path, statErr))

	case expect != "":
		return toolError(fmt.Sprintf(
			"refusing %s: expect_hash was given but the file does not exist. Omit it to create the file.", path))
	}

	mode := os.FileMode(0o644)
	if info != nil {
		mode = info.Mode().Perm()
	}
	// Creating a file in a directory that does not exist yet is an ordinary
	// thing to want, and MkdirAll goes through the root so it cannot climb
	// out. Only reached on the create path: replacing a file means its
	// directory is already there.
	if dir := filepath.Dir(rel); statErr != nil && dir != "." {
		if err := wr.h.MkdirAll(dir, 0o755); err != nil {
			return toolError(escapeMessage(path, err))
		}
	}
	if err := s.writeThroughRoot(wr, rel, content, mode); err != nil {
		return toolError(fmt.Sprintf("cannot write %s: %v", path, err))
	}

	verb := "created"
	if statErr == nil {
		verb = "replaced"
	}
	hash := Hash(content)
	return &core.ToolResult{
		Content: []core.Content{{
			Type: "text",
			Text: fmt.Sprintf("%s %s, %d byte(s)\nhash: %s", verb, wr.abs(rel), len(content), hash),
		}},
		StructuredContent: map[string]any{"path": wr.abs(rel), "hash": hash, "bytes": len(content), "created": statErr != nil},
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

// rel turns a tool-supplied path into one relative to the root, for handing
// to an os.Root method.
//
// It does not enforce containment and must not be relied on to: os.Root does
// that, at open time, per component. What this adds is a readable refusal for
// the two shapes a model actually produces by mistake, before any syscall.
// Anything subtler, notably a symlink pointing out of the root, is caught by
// the open itself.
// resolve maps a caller-supplied path to the root that contains it and the
// path relative to that root, refusing anything that falls outside every root.
//
// An absolute path is matched against each root in turn. A relative path
// resolves against the primary root, which is what keeps a single-root
// workspace behaving exactly as it did before there were several.
//
// Matching is on the cleaned path text, which decides only WHICH root handle
// to use. The handle then does the real containment work at open time, so a
// symlink pointing out of a root is still refused even though its path text
// looked fine here. See NewSource for why that split matters.
func (s *Source) resolve(path string) (*workspaceRoot, string, error) {
	if path == "" {
		return nil, "", fmt.Errorf("empty path")
	}
	if filepath.IsAbs(path) {
		clean := filepath.Clean(path)
		for _, r := range s.roots {
			rel, err := filepath.Rel(r.path, clean)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			return r, rel, nil
		}
		return nil, "", fmt.Errorf("refusing %s: outside every workspace root", path)
	}
	rel := filepath.Clean(path)
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, "", fmt.Errorf("refusing %s: outside every workspace root", path)
	}
	return s.roots[0], rel, nil
}

// abs renders a resolved path back to the model. Results are absolute because
// a bare relative path is ambiguous once there is more than one root: two
// repositories can both hold src/main.go, and a path the model cannot resolve
// unambiguously is one it will read from the wrong tree.
func (r *workspaceRoot) abs(rel string) string { return filepath.Join(r.path, rel) }

// escapeMessage renders a filesystem error for the model, translating
// os.Root's containment refusal into the same sentence rel produces.
//
// The standard library exports no sentinel for it, so this matches on the
// message os.Root produces. A missed match degrades to the raw error, which is
// already self-explanatory ("path escapes from parent"); it does not become an
// allowed operation, because the refusal happened in os.Root regardless.
func escapeMessage(path string, err error) string {
	if strings.Contains(err.Error(), "escapes from parent") {
		return fmt.Sprintf("refusing %s: outside every workspace root", path)
	}
	return fmt.Sprintf("cannot access %s: %v", path, err)
}

// writeThroughRoot replaces a file's contents via a temp file and a rename,
// both inside the root, so a failure partway through leaves the original
// intact rather than truncated. The engine guarantees all-or-nothing for the
// edit; this is the same promise at the filesystem.
//
// The mode is applied to the open descriptor rather than by path. Chmod by
// name is a second resolution of that name and is documented as racy even on
// os.Root; fchmod on a descriptor we just created with O_EXCL cannot be
// redirected at anything else. It also restores bits the umask cleared at
// create time.
func (s *Source) writeThroughRoot(wr *workspaceRoot, rel, content string, mode os.FileMode) error {
	dir := filepath.Dir(rel)
	var f *os.File
	var tmp string
	for attempt := 0; ; attempt++ {
		tmp = filepath.Join(dir, fmt.Sprintf(".files-%d.tmp", rand.Uint64()))
		var err error
		f, err = wr.h.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) || attempt >= 10 {
			return err
		}
	}
	defer wr.h.Remove(tmp)

	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return wr.h.Rename(tmp, rel)
}

func toolError(msg string) *core.ToolResult {
	return &core.ToolResult{
		Content: []core.Content{{Type: "text", Text: msg}},
		IsError: true,
	}
}

// PathArg reports the file a call is about to write, in the shape a
// checkpoint WriteSpec wants. Every writing tool here names its target in the
// same `path` argument, so one function serves them all:
//
//	checkpoint.WriteSpec{Tool: "edit_file", Paths: files.PathArg}
//	checkpoint.WriteSpec{Tool: "write_file", Paths: files.PathArg}
//
// It is a plain function rather than a WriteSpec so that this package does
// not import the checkpoint package and checkpoint does not import this one.
// The two features compose at the wiring layer, keyed by tool name, which is
// what keeps either free to change without the other's API stabilizing around
// it.
func PathArg(args map[string]any) []string {
	p, ok := args["path"].(string)
	if !ok || p == "" {
		return nil
	}
	return []string{p}
}

var _ agent.ToolSource = (*Source)(nil)
