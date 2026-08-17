package lsp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/panyam/mcpkit/core"
)

// ServerSpec describes one language server and the files it answers for.
type ServerSpec struct {
	// Command is the server to spawn, argv style. Required.
	//
	// It comes from configuration and is deliberately unreachable from a tool
	// argument. A model's instructions can come from content it read rather
	// than from the user, so an agent that could name the subprocess it starts
	// would be one injected instruction away from running anything.
	Command []string

	// Extensions are the file suffixes this server handles, including the dot
	// (".go", ".ts"). A file matching none is not sent to any server.
	Extensions []string

	// LanguageID is the LSP languageId for didOpen ("go", "typescript").
	// Servers that handle one language mostly ignore it; ones that handle
	// several do not.
	LanguageID string

	// SettleDelay is how long to keep waiting for further diagnostics after a
	// publication arrives, before treating the last set as the answer. Zero
	// picks a default from the server's reported name; see settleFor.
	//
	// Set it when a server computes diagnostics in phases and the default does
	// not already know about it. The tell is a broken file reported as clean:
	// the server published an empty set on the change and its real answer
	// arrived after we had stopped listening.
	SettleDelay time.Duration
}

// settleFor picks a settle delay from the name a server reports at initialize.
//
// Most servers publish once, with what they found, so a short delay costs
// almost nothing and is enough. The exceptions are servers that publish an
// empty set immediately and their real answer after a slow pass, where the
// delay has to outlast that gap or a broken file gets reported as clean.
//
// A table of known servers is not elegant, and it is what every LSP client
// ends up with, because nothing in the protocol lets a server say "that answer
// was provisional". Keying on the reported name rather than on the command
// means a server renamed or wrapped by a launcher still matches. A server that
// reports no name at all (typescript-language-server and pyright both do)
// falls through to the default, which is correct for them.
func settleFor(serverName string) time.Duration {
	switch serverName {
	case "rust-analyzer":
		// Measured: an empty publication on the change, the real diagnostics
		// about 2.4s later once cargo has run.
		return 3 * time.Second
	default:
		return DefaultSettleDelay
	}
}

// WriteSpec declares that a tool writes files and says which of its arguments
// name them.
//
// Declared rather than detected, and shaped like checkpoint.WriteSpec on
// purpose: both packages need the same fact about the same tools, and neither
// should import the other to get it. files.PathArg satisfies Paths for every
// tool in that package, so the wiring layer joins the three with no import
// edge between them (constraint C4).
type WriteSpec struct {
	// Tool is the tool name to watch, as the model calls it.
	Tool string

	// Paths returns the files the call is about to write, given its arguments.
	Paths func(args map[string]any) []string
}

// Config configures the extension.
type Config struct {
	// Root is the workspace the servers are started in. Required, and should
	// match files.Config.Root: a server rooted somewhere else resolves imports
	// against a tree the agent cannot edit.
	Root string

	// Servers are the language servers to run. Empty means the extension
	// contributes nothing, so a surface can wire it unconditionally.
	Servers []ServerSpec

	// Writes declares the file-writing tools whose effects should be
	// re-checked. A tool absent from this list is invisible here, and its
	// edits reach the model's diagnostics only on the next turn.
	Writes []WriteSpec

	// DiagnosticsTimeout bounds the wait for the server to re-check a file
	// after a write. Zero means DefaultDiagnosticsTimeout.
	//
	// Expiring reports nothing rather than what we last knew. Presenting
	// stale problems as the result of the edit that just ran would send the
	// model to fix something it has already fixed.
	DiagnosticsTimeout time.Duration

	// MaxDiagnostics caps how many problems one injected block carries. Zero
	// means DefaultMaxDiagnostics. What the cap drops is always reported.
	MaxDiagnostics int
}

// Defaults for the bounds in Config and ServerSpec.
const (
	// DefaultDiagnosticsTimeout bounds the whole wait after a write, settle
	// included. It has to exceed the slowest known settle (rust-analyzer's
	// three seconds) with room for the server's own work, or the timeout
	// would cut off the very publication the settle exists to catch.
	DefaultDiagnosticsTimeout = 8 * time.Second

	// DefaultSettleDelay is the quiet period for a server that publishes once
	// and means it, which is most of them. Short enough that a clean edit is
	// not noticeably slower.
	DefaultSettleDelay = 250 * time.Millisecond

	DefaultMaxDiagnostics = 50
)

// startTimeout bounds the initialize handshake for one server.
//
// Not a Config field, because nothing has needed to tune it and a knob that
// exists before its first caller is a knob whose right value nobody knows. It
// is generous enough for a cold gopls on a large module and short enough that
// a binary which is not a language server fails construction instead of
// hanging it.
const startTimeout = 20 * time.Second

// pool holds the running servers and routes a file to the one that handles it.
type pool struct {
	root    string
	clients []*client
	byExt   map[string]*client
}

func startPool(ctx context.Context, cfg Config) (*pool, error) {
	p := &pool{root: cfg.Root, byExt: map[string]*client{}}
	for _, spec := range cfg.Servers {
		c, err := startClient(ctx, spec, cfg.Root)
		if err != nil {
			// Everything started so far is shut down: a half-started pool
			// would leave orphaned subprocesses behind a constructor that
			// reported failure.
			_ = p.close()
			return nil, err
		}
		p.clients = append(p.clients, c)
		for _, ext := range spec.Extensions {
			p.byExt[strings.ToLower(ext)] = c
		}
	}
	return p, nil
}

func (p *pool) forPath(rel string) *client {
	return p.byExt[strings.ToLower(filepath.Ext(rel))]
}

func (p *pool) close() error {
	var firstErr error
	for _, c := range p.clients {
		if err := c.close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// source serves the two navigation tools.
type source struct {
	pool *pool
	defs []core.ToolDef
}

func (s *source) Tools(context.Context) ([]core.ToolDef, error) {
	out := make([]core.ToolDef, len(s.defs))
	copy(out, s.defs)
	return out, nil
}

func (s *source) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	switch name {
	case "goto_definition":
		return s.navigate(ctx, args, "textDocument/definition", false), nil
	case "find_references":
		return s.navigate(ctx, args, "textDocument/references", true), nil
	default:
		return nil, fmt.Errorf("lsp: unknown tool %q", name)
	}
}

// navigate resolves the named symbol and asks the server where it is used or
// declared, which is one code path because the two requests differ only in
// their method name and one parameter.
func (s *source) navigate(ctx context.Context, args map[string]any, method string, references bool) *core.ToolResult {
	path, _ := args["path"].(string)
	symbol, _ := args["symbol"].(string)
	if path == "" || symbol == "" {
		return toolError("needs both path and symbol")
	}
	rel, err := s.pool.rel(path)
	if err != nil {
		return toolError(err.Error())
	}
	c := s.pool.forPath(rel)
	if c == nil {
		return toolError(fmt.Sprintf("no language server configured for %s", filepath.Ext(rel)))
	}
	if _, err := c.sync(rel); err != nil {
		return toolError(fmt.Sprintf("%s: %v", path, err))
	}

	var syms []documentSymbol
	if err := c.conn.call(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(filepath.Join(c.root, rel))},
	}, &syms); err != nil {
		return toolError(fmt.Sprintf("%s: %v", path, err))
	}
	sym, err := findSymbol(syms, symbol)
	if err != nil {
		return toolError(fmt.Sprintf("%s: %v", path, err))
	}

	params := map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(filepath.Join(c.root, rel))},
		"position":     sym.SelectionRange.Start,
	}
	if references {
		includeDecl, ok := args["include_declaration"].(bool)
		params["context"] = map[string]any{"includeDeclaration": ok && includeDecl}
	}
	var locs []location
	if err := c.conn.call(ctx, method, params, &locs); err != nil {
		return toolError(fmt.Sprintf("%s: %v", path, err))
	}
	if len(locs) == 0 {
		what := "definition"
		if references {
			what = "references"
		}
		return &core.ToolResult{Content: []core.Content{{Type: "text",
			Text: fmt.Sprintf("no %s found for %s in %s", what, symbol, path)}}}
	}
	return &core.ToolResult{Content: []core.Content{{Type: "text", Text: s.render(c, locs)}}}
}

// render turns locations into the path:line: text shape search_files already
// uses, so a model reading both is reading one format.
//
// A location outside the workspace is reported by absolute path with no source
// line. Those are real answers, since a definition often lives in a dependency,
// and reading the file to quote it would reach outside the root that confines
// everything else this agent touches.
func (s *source) render(c *client, locs []location) string {
	var b strings.Builder
	shown := 0
	for _, loc := range locs {
		if shown >= DefaultMaxDiagnostics {
			fmt.Fprintf(&b, "\nshowing %d of %d; narrow the query", shown, len(locs))
			break
		}
		shown++
		rel, inRoot := c.relFromURI(loc.URI)
		if !inRoot {
			fmt.Fprintf(&b, "%s:%d: (outside the workspace)\n", loc.URI, loc.Range.Start.Line+1)
			continue
		}
		line := s.lineText(rel, loc.Range.Start.Line)
		col := byteColumn(line, loc.Range.Start.Character, c.encoding)
		fmt.Fprintf(&b, "%s:%d:%d: %s\n", rel, loc.Range.Start.Line+1, col+1, strings.TrimSpace(line))
	}
	return strings.TrimRight(b.String(), "\n")
}

func (s *source) lineText(rel string, line int) string {
	body, err := os.ReadFile(filepath.Join(s.pool.root, rel))
	if err != nil {
		return ""
	}
	lines := strings.Split(string(body), "\n")
	if line < 0 || line >= len(lines) {
		return ""
	}
	return strings.TrimRight(lines[line], "\r")
}

// rel resolves a caller-supplied path against the workspace root and refuses
// anything that leaves it.
func (p *pool) rel(path string) (string, error) {
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(p.root, path)
	}
	rel, err := filepath.Rel(p.root, filepath.Clean(abs))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("refusing %s: outside the workspace root", path)
	}
	return rel, nil
}

func toolDefs() []core.ToolDef {
	symbolArg := map[string]any{
		"type": "string",
		"description": "Name of the symbol, as it is declared. Use the qualified form (Type.Method) " +
			"when a bare name appears more than once in the file; an ambiguous name is refused rather than guessed.",
	}
	pathArg := map[string]any{
		"type":        "string",
		"description": "Path to the file the symbol is declared or used in, relative to the workspace root.",
	}
	return []core.ToolDef{
		{
			Name:  "goto_definition",
			Title: "Go to definition",
			Description: "Find where a symbol is defined, using the language server rather than a text search. " +
				"Use this instead of search_files when you need the declaration of a name and not every mention of it.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"path": pathArg, "symbol": symbolArg},
				"required":   []string{"path", "symbol"},
			},
		},
		{
			Name:  "find_references",
			Title: "Find references",
			Description: "Find every use of a symbol across the workspace, using the language server. " +
				"Unlike search_files this returns real uses: a comment mentioning the name is not a reference, " +
				"and a different symbol with the same name is not either. Use it before renaming or deleting something.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":   pathArg,
					"symbol": symbolArg,
					"include_declaration": map[string]any{
						"type":        "boolean",
						"description": "Include the declaration itself among the results. Defaults to false.",
					},
				},
				"required": []string{"path", "symbol"},
			},
		},
	}
}

func toolError(msg string) *core.ToolResult {
	return &core.ToolResult{
		Content: []core.Content{{Type: "text", Text: "lsp: " + msg}},
		IsError: true,
	}
}

// sortedKeys keeps rendered output stable across runs, since map iteration
// order would otherwise reorder a diagnostics block that did not change.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
