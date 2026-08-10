package files

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestSource(t *testing.T) (*Source, string) {
	t.Helper()
	root := t.TempDir()
	// t.TempDir on darwin hands back a /var path that is itself a symlink to
	// /private/var, and the source resolves symlinks, so compare against the
	// resolved form or every containment assertion is testing the wrong path.
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSource(Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func call(t *testing.T, s *Source, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := s.Call(context.Background(), name, args)
	if err != nil {
		t.Fatalf("Call(%s) dispatch error = %v", name, err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("Call(%s) returned no content", name)
	}
	return res.Content[0].Text, res.IsError
}

func TestReadReturnsContentAndHash(t *testing.T) {
	s, root := newTestSource(t)
	write(t, root, "notes.md", "hello\n")

	text, isErr := call(t, s, "read_file", map[string]any{"path": "notes.md"})
	if isErr {
		t.Fatalf("read_file failed: %s", text)
	}
	if !strings.Contains(text, Hash("hello\n")) {
		t.Errorf("read_file did not return the hash, got:\n%s", text)
	}
	if !strings.Contains(text, "hello") {
		t.Errorf("read_file did not return the content, got:\n%s", text)
	}
}

func TestEditAppliesAndReportsTheNewHash(t *testing.T) {
	s, root := newTestSource(t)
	p := write(t, root, "notes.md", "hello world\n")

	text, isErr := call(t, s, "edit_file", map[string]any{
		"path":        "notes.md",
		"expect_hash": Hash("hello world\n"),
		"edits":       []any{map[string]any{"old": "world", "new": "there"}},
	})
	if isErr {
		t.Fatalf("edit_file failed: %s", text)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello there\n" {
		t.Errorf("file = %q, want %q", got, "hello there\n")
	}
	if !strings.Contains(text, Hash("hello there\n")) {
		t.Errorf("edit_file should report the new hash so a follow-up edit needs no re-read, got:\n%s", text)
	}
}

// TestStaleEditLeavesTheFileAlone is the end-to-end form of the property the
// engine provides. The anchor is present and unique in the file on disk, so
// only the hash stands between the edit and a silent overwrite.
func TestStaleEditLeavesTheFileAlone(t *testing.T) {
	s, root := newTestSource(t)
	p := write(t, root, "notes.md", "hello world\n")

	text, isErr := call(t, s, "edit_file", map[string]any{
		"path":        "notes.md",
		"expect_hash": Hash("a different file entirely"),
		"edits":       []any{map[string]any{"old": "world", "new": "there"}},
	})
	if !isErr {
		t.Fatal("edit_file should have refused a stale edit")
	}
	if !strings.Contains(text, "changed since it was read") {
		t.Errorf("refusal should say why, got: %s", text)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "hello world\n" {
		t.Errorf("file was modified despite the refusal: %q", got)
	}
}

// TestRefusalIsNotADispatchError pins the ToolSource contract. A refusal has
// to reach the model as an IsError result so it can correct and retry; a
// returned error would abort the turn instead.
func TestRefusalIsNotADispatchError(t *testing.T) {
	s, root := newTestSource(t)
	write(t, root, "notes.md", "a a\n")

	res, err := s.Call(context.Background(), "edit_file", map[string]any{
		"path":        "notes.md",
		"expect_hash": Hash("a a\n"),
		"edits":       []any{map[string]any{"old": "a", "new": "b"}},
	})
	if err != nil {
		t.Fatalf("an ambiguous anchor must not be a dispatch error, got %v", err)
	}
	if !res.IsError {
		t.Fatal("an ambiguous anchor should be an IsError result")
	}
	if !strings.Contains(res.Content[0].Text, "not unique") {
		t.Errorf("message should name the problem, got: %s", res.Content[0].Text)
	}
}

func TestUnknownToolIsADispatchError(t *testing.T) {
	s, _ := newTestSource(t)
	if _, err := s.Call(context.Background(), "delete_everything", nil); err == nil {
		t.Fatal("an unknown tool should be a dispatch error")
	}
}

func TestEditRequiresExpectHash(t *testing.T) {
	s, root := newTestSource(t)
	write(t, root, "notes.md", "hello\n")

	text, isErr := call(t, s, "edit_file", map[string]any{
		"path":  "notes.md",
		"edits": []any{map[string]any{"old": "hello", "new": "hi"}},
	})
	if !isErr {
		t.Fatal("edit_file should refuse without expect_hash")
	}
	if !strings.Contains(text, "read the file first") {
		t.Errorf("refusal should say how to fix it, got: %s", text)
	}
}

// TestEscapingTheRootIsRefused covers the lexical case and the symlink case
// together. A purely string-based containment check passes the second one.
func TestEscapingTheRootIsRefused(t *testing.T) {
	s, root := newTestSource(t)
	outside := t.TempDir()
	outside, err := filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret")
	// A sentinel that cannot occur in a filesystem path, because the refusal
	// message quotes the path back and darwin's temp dirs live under
	// /private/var. A plainer word here matches the path and passes for the
	// wrong reason.
	const sentinel = "SECRET-FILE-CONTENT-SENTINEL"
	if err := os.WriteFile(secret, []byte(sentinel+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	for _, path := range []string{
		filepath.Join("..", filepath.Base(outside), "secret"),
		secret,
		filepath.Join("link", "secret"),
	} {
		t.Run(path, func(t *testing.T) {
			text, isErr := call(t, s, "read_file", map[string]any{"path": path})
			if !isErr {
				t.Fatalf("read_file should have refused %s, got:\n%s", path, text)
			}
			if strings.Contains(text, sentinel) {
				t.Fatalf("read_file leaked content from outside the root: %s", text)
			}
		})
	}
}

func TestRootIsRequired(t *testing.T) {
	if _, err := NewSource(Config{}); err == nil {
		t.Fatal("NewSource should refuse an empty Root")
	}
}

func TestEditPreservesFileMode(t *testing.T) {
	s, root := newTestSource(t)
	p := write(t, root, "script.sh", "echo one\n")
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}

	if text, isErr := call(t, s, "edit_file", map[string]any{
		"path":        "script.sh",
		"expect_hash": Hash("echo one\n"),
		"edits":       []any{map[string]any{"old": "one", "new": "two"}},
	}); isErr {
		t.Fatalf("edit_file failed: %s", text)
	}

	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("mode = %v, want 0755; an edit should not disarm an executable", got)
	}
}

func TestMissingNewIsADeletion(t *testing.T) {
	s, root := newTestSource(t)
	p := write(t, root, "notes.md", "keep DROP keep\n")

	if text, isErr := call(t, s, "edit_file", map[string]any{
		"path":        "notes.md",
		"expect_hash": Hash("keep DROP keep\n"),
		"edits":       []any{map[string]any{"old": "DROP "}},
	}); isErr {
		t.Fatalf("edit_file failed: %s", text)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "keep keep\n" {
		t.Errorf("file = %q, want %q", got, "keep keep\n")
	}
}

func TestMalformedEditsAreReported(t *testing.T) {
	s, root := newTestSource(t)
	write(t, root, "notes.md", "hello\n")

	cases := map[string]any{
		"not an array":       "old: hello",
		"empty array":        []any{},
		"element not object": []any{"hello"},
		"no old key":         []any{map[string]any{"new": "hi"}},
	}
	for name, edits := range cases {
		t.Run(name, func(t *testing.T) {
			text, isErr := call(t, s, "edit_file", map[string]any{
				"path":        "notes.md",
				"expect_hash": Hash("hello\n"),
				"edits":       edits,
			})
			if !isErr {
				t.Fatalf("edit_file should have refused, got: %s", text)
			}
		})
	}
}

// TestOneMalformedEditRejectsTheWholeCall is the case an all-malformed list
// does not reach. Dropping the bad element leaves the good one to apply, so
// the call reports success for an edit the model believes it made twice. That
// is the same failure as a stale write, arriving by a different route.
func TestOneMalformedEditRejectsTheWholeCall(t *testing.T) {
	s, root := newTestSource(t)
	p := write(t, root, "notes.md", "alpha beta\n")

	text, isErr := call(t, s, "edit_file", map[string]any{
		"path":        "notes.md",
		"expect_hash": Hash("alpha beta\n"),
		"edits": []any{
			map[string]any{"old": "alpha", "new": "ALPHA"},
			"this is not an edit object",
		},
	})
	if !isErr {
		t.Fatalf("a partially malformed edits list must be refused, got: %s", text)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "alpha beta\n" {
		t.Errorf("the valid edit was applied anyway: %q", got)
	}
}

func TestToolsAreDeclared(t *testing.T) {
	s, _ := newTestSource(t)
	defs, err := s.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 2 {
		t.Fatalf("got %d tools, want 2", len(defs))
	}
	for _, d := range defs {
		if d.Name == "" || d.Description == "" || d.InputSchema == nil {
			t.Errorf("tool %+v is missing a name, description, or schema", d)
		}
	}
}

func TestEditPathsFeedsACheckpointWriteSpec(t *testing.T) {
	if got := EditPaths(map[string]any{"path": "notes.md"}); len(got) != 1 || got[0] != "notes.md" {
		t.Errorf("EditPaths = %v, want [notes.md]", got)
	}
	if got := EditPaths(map[string]any{}); got != nil {
		t.Errorf("EditPaths with no path = %v, want nil", got)
	}
}
