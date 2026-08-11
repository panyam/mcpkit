package files

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newSourceWith(t *testing.T, cfg Config) (*Source, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = root
	s, err := NewSource(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

func TestListReturnsFilesRecursively(t *testing.T) {
	s, root := newTestSource(t)
	write(t, root, "a.txt", "one")
	write(t, root, "sub/b.txt", "two")
	write(t, root, "sub/deep/c.txt", "three")

	text, isErr := call(t, s, "list_files", nil)
	if isErr {
		t.Fatalf("list_files failed: %s", text)
	}
	for _, want := range []string{"a.txt", "sub/b.txt", "sub/deep/c.txt"} {
		if !strings.Contains(text, want) {
			t.Errorf("listing is missing %q:\n%s", want, text)
		}
	}
}

func TestListScopedToDir(t *testing.T) {
	s, root := newTestSource(t)
	write(t, root, "top.txt", "x")
	write(t, root, "sub/inner.txt", "y")

	text, isErr := call(t, s, "list_files", map[string]any{"dir": "sub"})
	if isErr {
		t.Fatalf("list_files failed: %s", text)
	}
	if !strings.Contains(text, "sub/inner.txt") {
		t.Errorf("scoped listing missing its own file:\n%s", text)
	}
	if strings.Contains(text, "top.txt") {
		t.Errorf("scoped listing leaked a file outside dir:\n%s", text)
	}
}

// TestListSaysWhatItLeftOut is the no-silent-caps rule. A capped listing that
// does not admit it reads as the complete contents of the directory, and the
// caller concludes a file it wanted is not there.
func TestListSaysWhatItLeftOut(t *testing.T) {
	s, root := newTestSource(t)
	for i := range 10 {
		write(t, root, fmt.Sprintf("f%02d.txt", i), "x")
	}

	text, isErr := call(t, s, "list_files", map[string]any{"limit": 3})
	if isErr {
		t.Fatalf("list_files failed: %s", text)
	}
	if !strings.Contains(text, "showing 3 of 10") {
		t.Errorf("a truncated listing must say so:\n%s", text)
	}
}

func TestListReportsExcludedDirectories(t *testing.T) {
	s, root := newTestSource(t)
	write(t, root, "keep.txt", "x")
	write(t, root, "node_modules/dep/index.js", "noise")

	text, isErr := call(t, s, "list_files", nil)
	if isErr {
		t.Fatalf("list_files failed: %s", text)
	}
	if strings.Contains(text, "index.js") {
		t.Errorf("excluded directory was listed:\n%s", text)
	}
	if !strings.Contains(text, "node_modules") {
		t.Errorf("an applied exclusion must be reported, not silent:\n%s", text)
	}
}

// TestEmptyExcludeMeansExcludeNothing pins the nil-versus-empty distinction.
// Collapsing them would make "list everything" unexpressible.
func TestEmptyExcludeMeansExcludeNothing(t *testing.T) {
	s, root := newSourceWith(t, Config{Exclude: []string{}})
	write(t, root, "node_modules/dep/index.js", "noise")

	text, isErr := call(t, s, "list_files", nil)
	if isErr {
		t.Fatalf("list_files failed: %s", text)
	}
	if !strings.Contains(text, "index.js") {
		t.Errorf("an explicitly empty Exclude should exclude nothing:\n%s", text)
	}
}

// TestWalkDoesNotDescendIntoASymlinkedDirectory is the confinement property on
// the walk, which is a different code path from the open. A traversal that
// followed links would both escape the root and be able to loop forever.
func TestWalkDoesNotDescendIntoASymlinkedDirectory(t *testing.T) {
	s, root := newTestSource(t)
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const sentinel = "OUTSIDE-FILE-SENTINEL"
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	write(t, root, "inside.txt", "fine")

	text, isErr := call(t, s, "list_files", nil)
	if isErr {
		t.Fatalf("list_files failed: %s", text)
	}
	if strings.Contains(text, "secret.txt") {
		t.Fatalf("walk descended into a symlinked directory:\n%s", text)
	}
	if !strings.Contains(text, "inside.txt") {
		t.Errorf("walk should still list real files:\n%s", text)
	}
}

func TestSearchFindsMatchesWithPathAndLine(t *testing.T) {
	s, root := newTestSource(t)
	write(t, root, "a.go", "package a\n\nfunc Target() {}\n")
	write(t, root, "b.go", "package b\n")

	text, isErr := call(t, s, "search_files", map[string]any{"query": "func Target"})
	if isErr {
		t.Fatalf("search_files failed: %s", text)
	}
	if !strings.Contains(text, "a.go:3:") {
		t.Errorf("match should carry path and line number:\n%s", text)
	}
	if strings.Contains(text, "b.go") {
		t.Errorf("non-matching file appeared:\n%s", text)
	}
}

func TestSearchTreatsQueryAsRegex(t *testing.T) {
	s, root := newTestSource(t)
	write(t, root, "a.go", "func GetUser() {}\nfunc GetOrder() {}\nfunc Delete() {}\n")

	text, isErr := call(t, s, "search_files", map[string]any{"query": `func Get\w+`})
	if isErr {
		t.Fatalf("search_files failed: %s", text)
	}
	if !strings.Contains(text, "GetUser") || !strings.Contains(text, "GetOrder") {
		t.Errorf("regex should have matched both:\n%s", text)
	}
	if strings.Contains(text, "Delete") {
		t.Errorf("regex matched something it should not:\n%s", text)
	}
}

// TestSearchLiteralEscapesMetacharacters covers the escape hatch for the regex
// default. Without it, text containing regex syntax is unsearchable except by
// hand-escaping.
func TestSearchLiteralEscapesMetacharacters(t *testing.T) {
	s, root := newTestSource(t)
	write(t, root, "a.go", "call foo(bar)\ncall foobar\n")

	text, isErr := call(t, s, "search_files", map[string]any{"query": "foo(bar)", "literal": true})
	if isErr {
		t.Fatalf("search_files failed: %s", text)
	}
	if !strings.Contains(text, "a.go:1:") {
		t.Errorf("literal search should match the parenthesised text:\n%s", text)
	}
	if strings.Contains(text, "a.go:2:") {
		t.Errorf("literal search should not match foobar:\n%s", text)
	}
}

// TestMalformedRegexIsRefusedNotEmpty is the failure the regex default makes
// likely. Reporting a broken pattern as "no matches" sends the caller looking
// for code that is sitting right there.
func TestMalformedRegexIsRefusedNotEmpty(t *testing.T) {
	s, root := newTestSource(t)
	write(t, root, "a.go", "func Target() {}\n")

	text, isErr := call(t, s, "search_files", map[string]any{"query": "func Target([a-z"})
	if !isErr {
		t.Fatalf("a malformed regex must be refused, got: %s", text)
	}
	if strings.Contains(text, "no matches") {
		t.Errorf("a broken pattern must not read as zero results: %s", text)
	}
	if !strings.Contains(text, "literal:true") {
		t.Errorf("refusal should name the way out: %s", text)
	}
}

func TestSearchSaysWhatItLeftOut(t *testing.T) {
	s, root := newTestSource(t)
	var b strings.Builder
	for range 20 {
		b.WriteString("needle here\n")
	}
	write(t, root, "many.txt", b.String())

	text, isErr := call(t, s, "search_files", map[string]any{"query": "needle", "limit": 5})
	if isErr {
		t.Fatalf("search_files failed: %s", text)
	}
	if !strings.Contains(text, "showing 5 of 20") {
		t.Errorf("a truncated search must say so:\n%s", text)
	}
}

func TestSearchSkipsAndCountsBinaryFiles(t *testing.T) {
	s, root := newTestSource(t)
	write(t, root, "text.txt", "needle\n")
	if err := os.WriteFile(filepath.Join(root, "blob.bin"), []byte("needle\x00\x01\x02"), 0o644); err != nil {
		t.Fatal(err)
	}

	text, isErr := call(t, s, "search_files", map[string]any{"query": "needle"})
	if isErr {
		t.Fatalf("search_files failed: %s", text)
	}
	if strings.Contains(text, "blob.bin") {
		t.Errorf("binary file was searched:\n%s", text)
	}
	if !strings.Contains(text, "1 binary") {
		t.Errorf("skipped binary files must be counted, not dropped silently:\n%s", text)
	}
}

// TestSearchDoesNotReadThroughASymlink is the search-side half of the
// confinement. The walk lists the link as a name, which is useful; reading its
// target would not be.
func TestSearchDoesNotReadThroughASymlink(t *testing.T) {
	s, root := newTestSource(t)
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const sentinel = "NEEDLE-OUTSIDE-THE-ROOT"
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte(sentinel+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "innocent.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	text, isErr := call(t, s, "search_files", map[string]any{"query": "NEEDLE"})
	if isErr && !strings.Contains(text, "no matches") {
		t.Fatalf("search_files failed unexpectedly: %s", text)
	}
	if strings.Contains(text, sentinel) {
		t.Fatalf("search read through a symlink out of the root:\n%s", text)
	}
}

func TestSearchNoMatchesIsNotAnError(t *testing.T) {
	s, root := newTestSource(t)
	write(t, root, "a.go", "package a\n")

	text, isErr := call(t, s, "search_files", map[string]any{"query": "definitelyNotPresent"})
	if isErr {
		t.Fatalf("an honest zero-result search is not an error: %s", text)
	}
	if !strings.Contains(text, "no matches") {
		t.Errorf("should say there were no matches:\n%s", text)
	}
}

func TestDiscoveryRefusesToEscapeTheRoot(t *testing.T) {
	s, _ := newTestSource(t)
	for _, tool := range []string{"list_files", "search_files"} {
		t.Run(tool, func(t *testing.T) {
			args := map[string]any{"dir": "../.."}
			if tool == "search_files" {
				args["query"] = "x"
			}
			text, isErr := call(t, s, tool, args)
			if !isErr {
				t.Fatalf("%s should refuse a dir outside the root, got:\n%s", tool, text)
			}
			if !strings.Contains(text, "outside the workspace root") {
				t.Errorf("refusal should say why: %s", text)
			}
		})
	}
}
