package files

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteCreatesANewFile(t *testing.T) {
	s, root := newTestSource(t)

	text, isErr := call(t, s, "write_file", map[string]any{
		"path": "new.txt", "content": "hello\n",
	})
	if isErr {
		t.Fatalf("write_file failed: %s", text)
	}
	if got := readFile(t, filepath.Join(root, "new.txt")); got != "hello\n" {
		t.Errorf("file = %q, want %q", got, "hello\n")
	}
	if !strings.Contains(text, Hash("hello\n")) {
		t.Errorf("write_file should report the new hash:\n%s", text)
	}
	if !strings.Contains(text, "created") {
		t.Errorf("reply should distinguish a create from a replace:\n%s", text)
	}
}

// TestWriteRefusesAnExistingPathWithoutAHash is the whole discipline. Without
// it, write_file is a way to clobber content nobody looked at, which is the
// hole edit_file exists to close, reachable by a model that found anchoring
// awkward.
func TestWriteRefusesAnExistingPathWithoutAHash(t *testing.T) {
	s, root := newTestSource(t)
	p := write(t, root, "notes.md", "original\n")

	text, isErr := call(t, s, "write_file", map[string]any{
		"path": "notes.md", "content": "clobbered\n",
	})
	if !isErr {
		t.Fatalf("write_file must refuse an existing path with no expect_hash, got: %s", text)
	}
	if got := readFile(t, p); got != "original\n" {
		t.Errorf("file was modified despite the refusal: %q", got)
	}
	for _, want := range []string{"already exists", "expect_hash", "edit_file"} {
		if !strings.Contains(text, want) {
			t.Errorf("refusal should mention %q so the caller can correct: %s", want, text)
		}
	}
}

func TestWriteReplacesWhenTheHashMatches(t *testing.T) {
	s, root := newTestSource(t)
	p := write(t, root, "notes.md", "original\n")

	text, isErr := call(t, s, "write_file", map[string]any{
		"path": "notes.md", "content": "replaced\n", "expect_hash": Hash("original\n"),
	})
	if isErr {
		t.Fatalf("write_file failed: %s", text)
	}
	if got := readFile(t, p); got != "replaced\n" {
		t.Errorf("file = %q, want %q", got, "replaced\n")
	}
	if !strings.Contains(text, "replaced") {
		t.Errorf("reply should say it replaced rather than created:\n%s", text)
	}
}

func TestWriteRefusesAStaleHash(t *testing.T) {
	s, root := newTestSource(t)
	p := write(t, root, "notes.md", "original\n")

	text, isErr := call(t, s, "write_file", map[string]any{
		"path": "notes.md", "content": "clobbered\n", "expect_hash": Hash("something else"),
	})
	if !isErr {
		t.Fatalf("write_file must refuse a stale hash, got: %s", text)
	}
	if got := readFile(t, p); got != "original\n" {
		t.Errorf("file was modified despite the refusal: %q", got)
	}
	if !strings.Contains(text, "re-read it") {
		t.Errorf("refusal should say how to fix it: %s", text)
	}
}

// TestWriteRefusesAHashForAFileThatDoesNotExist catches the caller who thinks
// they are replacing something. Creating it instead would succeed while their
// belief about the world was wrong, which is the failure mode in miniature.
func TestWriteRefusesAHashForAFileThatDoesNotExist(t *testing.T) {
	s, root := newTestSource(t)

	text, isErr := call(t, s, "write_file", map[string]any{
		"path": "ghost.txt", "content": "x", "expect_hash": Hash("anything"),
	})
	if !isErr {
		t.Fatalf("write_file should refuse a hash for a missing file, got: %s", text)
	}
	if _, err := os.Stat(filepath.Join(root, "ghost.txt")); err == nil {
		t.Error("the file should not have been created")
	}
}

func TestWriteCreatesMissingParentDirectories(t *testing.T) {
	s, root := newTestSource(t)

	text, isErr := call(t, s, "write_file", map[string]any{
		"path": "a/b/c/deep.txt", "content": "nested\n",
	})
	if isErr {
		t.Fatalf("write_file failed: %s", text)
	}
	if got := readFile(t, filepath.Join(root, "a/b/c/deep.txt")); got != "nested\n" {
		t.Errorf("file = %q", got)
	}
}

func TestWriteRefusesToEscapeTheRoot(t *testing.T) {
	s, _ := newTestSource(t)
	text, isErr := call(t, s, "write_file", map[string]any{
		"path": "../escaped.txt", "content": "x",
	})
	if !isErr {
		t.Fatalf("write_file should refuse a path outside the root, got: %s", text)
	}
	if !strings.Contains(text, "outside the workspace root") {
		t.Errorf("refusal should say why: %s", text)
	}
}

// TestWriteRefusesToWriteThroughASymlink mirrors the edit_file guarantee. A
// symlink is not a regular file, so replacing "it" would mean writing to
// whatever it points at.
func TestWriteRefusesToWriteThroughASymlink(t *testing.T) {
	s, root := newTestSource(t)
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "important.conf")
	const original = "keep me\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "innocent.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	text, isErr := call(t, s, "write_file", map[string]any{
		"path": "innocent.txt", "content": "CLOBBERED\n", "expect_hash": Hash(original),
	})
	if !isErr {
		t.Fatalf("write_file should refuse to write through a symlink, got: %s", text)
	}
	if got := readFile(t, target); got != original {
		t.Fatalf("wrote outside the workspace root: %q", got)
	}
}

// TestWriteToADirectorySaysItIsNotAFile pins a message, not a safety property.
// Without the check the write still fails, because os.Root refuses to rename
// over a directory, but the caller is told the path "already exists" and is
// invited to pass expect_hash, which can never work. A refusal that suggests
// an impossible fix costs the model a turn to discover that.
func TestWriteToADirectorySaysItIsNotAFile(t *testing.T) {
	s, root := newTestSource(t)
	if err := os.MkdirAll(filepath.Join(root, "somedir"), 0o755); err != nil {
		t.Fatal(err)
	}

	text, isErr := call(t, s, "write_file", map[string]any{
		"path": "somedir", "content": "x",
	})
	if !isErr {
		t.Fatalf("write_file should refuse a directory, got: %s", text)
	}
	if !strings.Contains(text, "not a regular file") {
		t.Errorf("refusal should say what it found, not offer expect_hash: %s", text)
	}
}

// TestSymlinkOutOfRootReadsAsAnEscapeNotACreate is the same kind of pin. The
// path resolves lexically, so the escape is only discovered at Stat. Without
// that branch the call falls through to the create path and the eventual
// refusal comes from the write, phrased as though the file merely could not be
// written rather than as a workspace-boundary refusal.
func TestSymlinkOutOfRootReadsAsAnEscapeNotACreate(t *testing.T) {
	s, root := newTestSource(t)
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "important.conf")
	if err := os.WriteFile(target, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "innocent.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	text, isErr := call(t, s, "write_file", map[string]any{
		"path": "innocent.txt", "content": "CLOBBERED\n",
	})
	if !isErr {
		t.Fatalf("write_file should refuse, got: %s", text)
	}
	if !strings.Contains(text, "outside the workspace root") {
		t.Errorf("refusal should name the boundary, not read as a write failure: %s", text)
	}
	if got := readFile(t, target); got != "keep me\n" {
		t.Fatalf("wrote outside the workspace root: %q", got)
	}
}

func TestWriteAcceptsEmptyContent(t *testing.T) {
	s, root := newTestSource(t)

	if text, isErr := call(t, s, "write_file", map[string]any{
		"path": "empty.txt", "content": "",
	}); isErr {
		t.Fatalf("an empty file is a legitimate thing to write: %s", text)
	}
	if got := readFile(t, filepath.Join(root, "empty.txt")); got != "" {
		t.Errorf("file = %q, want empty", got)
	}
}

func TestWriteNeedsContent(t *testing.T) {
	s, _ := newTestSource(t)
	text, isErr := call(t, s, "write_file", map[string]any{"path": "x.txt"})
	if !isErr {
		t.Fatalf("write_file should refuse a missing content argument, got: %s", text)
	}
}

func TestWritePreservesModeOnReplace(t *testing.T) {
	s, root := newTestSource(t)
	p := write(t, root, "script.sh", "echo one\n")
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}

	if text, isErr := call(t, s, "write_file", map[string]any{
		"path": "script.sh", "content": "echo two\n", "expect_hash": Hash("echo one\n"),
	}); isErr {
		t.Fatalf("write_file failed: %s", text)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("mode = %v, want 0755; a replace should not disarm an executable", got)
	}
}

// TestPathArgServesEveryWritingTool is why the rename happened: one function
// feeds a checkpoint WriteSpec for both write tools, so neither package has to
// import the other.
func TestPathArgServesEveryWritingTool(t *testing.T) {
	for _, args := range []map[string]any{
		{"path": "notes.md", "content": "x"},
		{"path": "notes.md", "expect_hash": "abc", "edits": []any{}},
	} {
		got := PathArg(args)
		if len(got) != 1 || got[0] != "notes.md" {
			t.Errorf("PathArg(%v) = %v, want [notes.md]", args, got)
		}
	}
}
