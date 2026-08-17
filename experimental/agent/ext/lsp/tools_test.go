package lsp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func toolsFor(t *testing.T, root string, s stubScript) *source {
	t.Helper()
	ext := newStubExtension(t, root, s)
	src, err := ext.Tools()
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if src == nil {
		t.Fatal("no tool source")
	}
	return src.(*source)
}

func loc(root, rel string, line, char int) location {
	return location{
		URI:   pathToURI(filepath.Join(root, rel)),
		Range: textRange{Start: position{Line: line, Character: char}},
	}
}

func TestToolsAreTheTwoNavigationTools(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	defs, err := toolsFor(t, root, stubScript{}).Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range defs {
		names = append(names, d.Name)
	}
	want := []string{"goto_definition", "find_references"}
	if len(names) != len(want) || names[0] != want[0] || names[1] != want[1] {
		t.Fatalf("tools = %v, want exactly %v (document_symbols is the repo map's question)", names, want)
	}
}

func TestGotoDefinitionResolvesBySymbolName(t *testing.T) {
	src := "package a\n\nfunc Get() int { return 1 }\n\nfunc use() int { return Get() }\n"
	root := workspace(t, map[string]string{"a.go": src})
	s := toolsFor(t, root, stubScript{
		Symbols:    map[string][]documentSymbol{"a.go": {sym("Get", 2, 5)}},
		Definition: []location{loc(root, "a.go", 2, 5)},
	})

	res, err := s.Call(context.Background(), "goto_definition", map[string]any{"path": "a.go", "symbol": "Get"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.IsError {
		t.Fatalf("refused: %s", res.Content[0].Text)
	}
	got := res.Content[0].Text
	if !strings.Contains(got, "a.go:3:6:") {
		t.Fatalf("location not rendered 1-based as path:line:col: %q", got)
	}
	if !strings.Contains(got, "func Get() int") {
		t.Fatalf("source line not quoted: %q", got)
	}
}

func TestFindReferencesRendersEveryUse(t *testing.T) {
	src := "package a\n\nfunc Get() int { return 1 }\n\nfunc one() int { return Get() }\nfunc two() int { return Get() }\n"
	root := workspace(t, map[string]string{"a.go": src})
	s := toolsFor(t, root, stubScript{
		Symbols:    map[string][]documentSymbol{"a.go": {sym("Get", 2, 5)}},
		References: []location{loc(root, "a.go", 4, 23), loc(root, "a.go", 5, 23)},
	})

	res, err := s.Call(context.Background(), "find_references", map[string]any{"path": "a.go", "symbol": "Get"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	got := res.Content[0].Text
	if !strings.Contains(got, "a.go:5:") || !strings.Contains(got, "a.go:6:") {
		t.Fatalf("both references should be listed: %q", got)
	}
}

// TestReferencesOutsideTheWorkspaceAreNamedButNotQuoted pins the confinement.
// A definition in a dependency is a real answer, and reading that file to quote
// it would reach outside the root that confines everything else the agent does.
func TestReferencesOutsideTheWorkspaceAreNamedButNotQuoted(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n\nfunc Get() int { return 1 }\n"})
	s := toolsFor(t, root, stubScript{
		Symbols:    map[string][]documentSymbol{"a.go": {sym("Get", 2, 5)}},
		Definition: []location{{URI: "file:///usr/lib/go/src/fmt/print.go", Range: textRange{Start: position{Line: 9}}}},
	})

	res, err := s.Call(context.Background(), "goto_definition", map[string]any{"path": "a.go", "symbol": "Get"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	got := res.Content[0].Text
	if !strings.Contains(got, "outside the workspace") {
		t.Fatalf("an out-of-root location should say so: %q", got)
	}
	if !strings.Contains(got, "print.go") {
		t.Fatalf("the location should still be named: %q", got)
	}
}

// TestAmbiguousSymbolIsRefusedAtTheTool carries findSymbol's discipline out to
// the model: an ambiguous name comes back as an IsError result it can correct,
// rather than as a confident answer about the wrong symbol.
func TestAmbiguousSymbolIsRefusedAtTheTool(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	s := toolsFor(t, root, stubScript{Symbols: map[string][]documentSymbol{"a.go": {
		sym("Reader", 1, 5, sym("Close", 3, 5)),
		sym("Writer", 8, 5, sym("Close", 10, 5)),
	}}})

	res, err := s.Call(context.Background(), "find_references", map[string]any{"path": "a.go", "symbol": "Close"})
	if err != nil {
		t.Fatalf("an ambiguous name is the model's problem to fix, not a dispatch failure: %v", err)
	}
	if !res.IsError {
		t.Fatalf("want a refusal, got %q", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "ambiguous") {
		t.Fatalf("refusal should say why: %q", res.Content[0].Text)
	}
}

func TestUnconfiguredExtensionIsRefused(t *testing.T) {
	root := workspace(t, map[string]string{"a.py": "x = 1\n"})
	s := toolsFor(t, root, stubScript{})

	res, err := s.Call(context.Background(), "goto_definition", map[string]any{"path": "a.py", "symbol": "x"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].Text, "no language server configured") {
		t.Fatalf("want a refusal naming the gap, got %+v", res.Content[0])
	}
}

func TestPathOutsideTheRootIsRefused(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	s := toolsFor(t, root, stubScript{})

	res, err := s.Call(context.Background(), "goto_definition", map[string]any{"path": "../../etc/passwd", "symbol": "x"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].Text, "outside the workspace root") {
		t.Fatalf("want a confinement refusal, got %+v", res.Content[0])
	}
}

func TestMissingArgumentsAreRefused(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	s := toolsFor(t, root, stubScript{})

	res, err := s.Call(context.Background(), "goto_definition", map[string]any{"path": "a.go"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !res.IsError {
		t.Fatal("want a refusal for a call with no symbol")
	}
}

// TestUnknownToolIsADispatchError pins the split ToolSource declares: a tool
// that ran and failed reports through IsError, while a name this source does
// not serve is a dispatch failure.
func TestUnknownToolIsADispatchError(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	s := toolsFor(t, root, stubScript{})

	if _, err := s.Call(context.Background(), "rename_symbol", map[string]any{}); err == nil {
		t.Fatal("want a dispatch error for an unknown tool")
	}
}

func TestNoResultsIsNotAnError(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n\nfunc Get() int { return 1 }\n"})
	s := toolsFor(t, root, stubScript{Symbols: map[string][]documentSymbol{"a.go": {sym("Get", 2, 5)}}})

	res, err := s.Call(context.Background(), "find_references", map[string]any{"path": "a.go", "symbol": "Get"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.IsError {
		t.Fatalf("a symbol with no references is an answer, not a failure: %q", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "no references found") {
		t.Fatalf("want the empty answer stated, got %q", res.Content[0].Text)
	}
}
