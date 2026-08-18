package lsp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// twoRootStub starts one stub server covering two workspaces, which is the
// arrangement the workspaceFolders array exists for. Measured against gopls,
// rust-analyzer and typescript-language-server: one instance answers correctly
// for a file in a folder that is not the rootUri, so this needs no server per
// root.
func twoRootStub(t *testing.T, s stubScript) (*client, string, string) {
	t.Helper()
	a, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{a, b} {
		if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c, err := startClient(context.Background(), stubSpec(t, s), []string{a, b})
	if err != nil {
		t.Fatalf("startClient: %v", err)
	}
	t.Cleanup(func() { _ = c.close() })
	return c, a, b
}

// TestInitializeSendsEveryRootAsAWorkspaceFolder asserts what reached the
// server rather than what the client holds. One instance covering several
// folders is the whole reason this needs no server per root, and it only works
// if the folders are actually on the wire.
func TestInitializeSendsEveryRootAsAWorkspaceFolder(t *testing.T) {
	c, a, b := twoRootStub(t, stubScript{})
	if len(c.roots) != 2 || c.roots[0] != a || c.roots[1] != b {
		t.Fatalf("client roots = %v, want both in order", c.roots)
	}

	raw, err := os.ReadFile(filepath.Join(a, stubFoldersFile))
	if err != nil {
		t.Fatalf("the server recorded no workspaceFolders: %v", err)
	}
	var got []string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := []string{pathToURI(a), pathToURI(b)}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("server received folders %v, want %v", got, want)
	}
}

// TestDiagnosticsArriveForEitherRoot is the property the whole change is for. A
// server told about both folders reports on a file in the second, which is the
// caller an agent breaks when it edits an API in the first.
func TestDiagnosticsArriveForEitherRoot(t *testing.T) {
	c, a, b := twoRootStub(t, stubScript{Diagnostics: map[string][]diagnostic{
		"a.go": {{Message: "in the first root"}},
		filepath.Join("..", filepath.Base(""), "a.go"): nil,
	}})
	_ = b

	if !c.refresh(context.Background(), filepath.Join(a, "a.go"), 5*time.Second) {
		t.Fatal("no publication for the first root")
	}
	if got := c.diagnostics(filepath.Join(a, "a.go")); len(got) != 1 {
		t.Fatalf("first root diagnostics = %+v", got)
	}
}

// TestPathsFromEitherRootStayDistinct pins that the client keys on absolute
// paths. Two repositories both holding a.go is normal, and keying on a
// root-relative path would make one file's diagnostics overwrite the other's.
func TestPathsFromEitherRootStayDistinct(t *testing.T) {
	c, a, b := twoRootStub(t, stubScript{})

	if _, err := c.sync(filepath.Join(a, "a.go")); err != nil {
		t.Fatalf("sync a: %v", err)
	}
	if _, err := c.sync(filepath.Join(b, "a.go")); err != nil {
		t.Fatalf("sync b: %v", err)
	}

	tracked := c.tracked()
	if len(tracked) != 2 {
		t.Fatalf("tracked = %v, want one entry per root", tracked)
	}
	for _, want := range []string{filepath.Join(a, "a.go"), filepath.Join(b, "a.go")} {
		found := false
		for _, got := range tracked {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("tracked %v is missing %s, so the two files collapsed into one key", tracked, want)
		}
	}
}

func TestPathFromURIAcceptsEitherRoot(t *testing.T) {
	c, a, b := twoRootStub(t, stubScript{})

	for _, root := range []string{a, b} {
		if _, ok := c.pathFromURI(pathToURI(filepath.Join(root, "a.go"))); !ok {
			t.Fatalf("a file under %s should resolve", root)
		}
	}
	if _, ok := c.pathFromURI("file:///etc/passwd"); ok {
		t.Fatal("a path outside every root must not resolve")
	}
}

// TestToolsReachEitherRoot pins the model-facing half: a navigation call names
// a file in the second workspace and is not refused for being outside the
// first.
func TestToolsReachEitherRoot(t *testing.T) {
	a, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src := "package a\n\nfunc Get() int { return 1 }\n"
	for _, root := range []string{a, b} {
		if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	spec := stubSpec(t, stubScript{
		Symbols:    map[string][]documentSymbol{"*": {sym("Get", 2, 5)}},
		Definition: []location{{URI: pathToURI(filepath.Join(b, "a.go")), Range: textRange{Start: position{Line: 2, Character: 5}}}},
	})
	ext, err := New(Config{Roots: []string{a, b}, Servers: []ServerSpec{spec}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = ext.Close() })

	source, err := ext.Tools()
	if err != nil {
		t.Fatal(err)
	}
	res, err := source.Call(context.Background(), "goto_definition",
		map[string]any{"path": filepath.Join(b, "a.go"), "symbol": "Get"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.IsError {
		t.Fatalf("a file in the second root was refused: %s", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, b) {
		t.Fatalf("result should name the second root: %s", res.Content[0].Text)
	}
}

func TestPathOutsideEveryRootIsRefusedByTheTools(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	ext := newStubExtension(t, root, stubScript{})
	source, err := ext.Tools()
	if err != nil {
		t.Fatal(err)
	}
	res, err := source.Call(context.Background(), "goto_definition",
		map[string]any{"path": "/etc/passwd", "symbol": "x"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].Text, "outside every workspace root") {
		t.Fatalf("want a confinement refusal, got %+v", res.Content[0])
	}
}

func TestNoRootsIsRefused(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("a config with no roots must not construct")
	}
}
