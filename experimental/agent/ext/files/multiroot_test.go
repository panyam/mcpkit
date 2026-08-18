package files

import (
	"path/filepath"
	"strings"
	"testing"
)

// twoRoots builds a source over two independent workspaces, which is the
// ordinary case rather than the exotic one: a coding session that stays inside
// a single repository is the exception (issue 1314).
func twoRoots(t *testing.T) (*Source, string, string) {
	t.Helper()
	a, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSource(Config{Roots: []string{a, b}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, a, b
}

func TestBothRootsAreReadable(t *testing.T) {
	s, a, b := twoRoots(t)
	write(t, a, "one.txt", "from a")
	write(t, b, "two.txt", "from b")

	for path, want := range map[string]string{
		filepath.Join(a, "one.txt"): "from a",
		filepath.Join(b, "two.txt"): "from b",
	} {
		got, isErr := call(t, s, "read_file", map[string]any{"path": path})
		if isErr {
			t.Fatalf("read %s refused: %s", path, got)
		}
		if !strings.Contains(got, want) {
			t.Fatalf("read %s = %q, want %q", path, got, want)
		}
	}
}

func TestWriteReachesTheSecondRoot(t *testing.T) {
	s, _, b := twoRoots(t)
	target := filepath.Join(b, "new.txt")

	got, isErr := call(t, s, "write_file", map[string]any{"path": target, "content": "hello"})
	if isErr {
		t.Fatalf("write refused: %s", got)
	}
	if readFile(t, target) != "hello" {
		t.Fatalf("file not written to the second root")
	}
}

func TestListSpansEveryRoot(t *testing.T) {
	s, a, b := twoRoots(t)
	write(t, a, "one.txt", "x")
	write(t, b, "two.txt", "y")

	got, isErr := call(t, s, "list_files", map[string]any{})
	if isErr {
		t.Fatalf("list refused: %s", got)
	}
	if !strings.Contains(got, filepath.Join(a, "one.txt")) || !strings.Contains(got, filepath.Join(b, "two.txt")) {
		t.Fatalf("a listing that covers only one root reads as the whole workspace:\n%s", got)
	}
}

func TestSearchSpansEveryRoot(t *testing.T) {
	s, a, b := twoRoots(t)
	write(t, a, "one.go", "func Target() {}")
	write(t, b, "two.go", "var _ = Target")

	got, isErr := call(t, s, "search_files", map[string]any{"query": "Target"})
	if isErr {
		t.Fatalf("search refused: %s", got)
	}
	if !strings.Contains(got, filepath.Join(a, "one.go")) || !strings.Contains(got, filepath.Join(b, "two.go")) {
		t.Fatalf("search missed a root, which is the failure this change exists to prevent:\n%s", got)
	}
}

// TestSameRelativePathInTwoRootsStaysDistinct is why results are absolute. Two
// repositories both holding src/main.go is normal, and a bare relative path
// would name either.
func TestSameRelativePathInTwoRootsStaysDistinct(t *testing.T) {
	s, a, b := twoRoots(t)
	write(t, a, "src/main.go", "package a")
	write(t, b, "src/main.go", "package b")

	got, isErr := call(t, s, "list_files", map[string]any{})
	if isErr {
		t.Fatalf("list refused: %s", got)
	}
	if !strings.Contains(got, filepath.Join(a, "src/main.go")) || !strings.Contains(got, filepath.Join(b, "src/main.go")) {
		t.Fatalf("both files should appear under their own root:\n%s", got)
	}

	fromA, _ := call(t, s, "read_file", map[string]any{"path": filepath.Join(a, "src/main.go")})
	fromB, _ := call(t, s, "read_file", map[string]any{"path": filepath.Join(b, "src/main.go")})
	if !strings.Contains(fromA, "package a") || !strings.Contains(fromB, "package b") {
		t.Fatalf("absolute paths did not distinguish the two files:\n%s\n%s", fromA, fromB)
	}
}

// TestRelativePathResolvesAgainstThePrimaryRoot pins the rule that keeps a
// single-root workspace behaving exactly as it did before there were several.
func TestRelativePathResolvesAgainstThePrimaryRoot(t *testing.T) {
	s, a, b := twoRoots(t)
	write(t, a, "only-in-a.txt", "primary")
	write(t, b, "only-in-b.txt", "secondary")

	got, isErr := call(t, s, "read_file", map[string]any{"path": "only-in-a.txt"})
	if isErr || !strings.Contains(got, "primary") {
		t.Fatalf("a relative path should resolve against the first root: %s", got)
	}
	if got, isErr := call(t, s, "read_file", map[string]any{"path": "only-in-b.txt"}); !isErr {
		t.Fatalf("a relative path must not search the other roots: %s", got)
	}
}

// TestPathOutsideEveryRootIsRefused is the confinement property, restated for
// more than one root. Adding roots widens the workspace to exactly those
// directories and to nothing between or around them.
func TestPathOutsideEveryRootIsRefused(t *testing.T) {
	s, a, _ := twoRoots(t)
	outside := filepath.Join(filepath.Dir(a), "not-a-root.txt")

	for _, tool := range []string{"read_file", "list_files", "search_files"} {
		args := map[string]any{"path": outside, "dir": outside, "query": "x"}
		got, isErr := call(t, s, tool, args)
		if !isErr {
			t.Fatalf("%s reached outside every root: %s", tool, got)
		}
		if !strings.Contains(got, "outside every workspace root") {
			t.Fatalf("%s refusal should name the boundary: %s", tool, got)
		}
	}
}

// TestParentOfTheRootsIsNotImplicitlyIncluded pins that naming two siblings
// does not quietly admit their shared parent, which is the shortcut the design
// rejects: it would widen confinement to everything else under that parent.
func TestParentOfTheRootsIsNotImplicitlyIncluded(t *testing.T) {
	s, a, b := twoRoots(t)
	parent := filepath.Dir(a)
	if parent != filepath.Dir(b) {
		t.Skip("temp dirs do not share a parent on this platform")
	}
	write(t, parent, "sibling.txt", "should be unreachable")

	got, isErr := call(t, s, "read_file", map[string]any{"path": filepath.Join(parent, "sibling.txt")})
	if !isErr {
		t.Fatalf("the shared parent of two roots is not itself a root: %s", got)
	}
}

// TestListLimitAppliesAcrossRootsNotPerRoot pins that the cap does not scale
// with the number of roots, which would make a listing quietly larger every
// time a repository was added.
func TestListLimitAppliesAcrossRootsNotPerRoot(t *testing.T) {
	s, a, b := twoRoots(t)
	for i := range 5 {
		write(t, a, string(rune('a'+i))+".txt", "x")
		write(t, b, string(rune('a'+i))+".txt", "x")
	}

	got, isErr := call(t, s, "list_files", map[string]any{"limit": float64(6)})
	if isErr {
		t.Fatalf("list refused: %s", got)
	}
	shown := 0
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		if strings.HasSuffix(line, ".txt") {
			shown++
		}
	}
	if shown > 6 {
		t.Fatalf("limit 6 produced %d paths, so the cap is per root rather than overall:\n%s", shown, got)
	}
	if !strings.Contains(got, "of 10") {
		t.Fatalf("the total should count every root: %s", got)
	}
}

func TestNoRootsIsRefused(t *testing.T) {
	if _, err := NewSource(Config{}); err == nil {
		t.Fatal("a source with no roots must not construct")
	}
	if _, err := NewSource(Config{Roots: []string{""}}); err == nil {
		t.Fatal("an empty root path must not construct")
	}
}
