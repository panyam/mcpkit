package checkpoint

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	work := t.TempDir()
	s, err := NewStore(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	return s, work
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRestoreReturnsModifiedContent(t *testing.T) {
	s, work := newStore(t)
	f := filepath.Join(work, "a.txt")
	write(t, f, "original")

	cp, err := s.Open("turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.Add(f); err != nil {
		t.Fatal(err)
	}
	write(t, f, "clobbered")

	if _, err := cp.Restore(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, f); got != "original" {
		t.Fatalf("restore gave %q, want %q", got, "original")
	}
}

// TestRestoreDeletesCreatedFile pins the absent sentinel. Undoing a creation
// means the file is gone, and a store that only wrote back what it had seen
// would leave it in place while reporting success.
func TestRestoreDeletesCreatedFile(t *testing.T) {
	s, work := newStore(t)
	f := filepath.Join(work, "new.txt")

	cp, err := s.Open("turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.Add(f); err != nil {
		t.Fatal(err)
	}
	write(t, f, "created by the agent")

	if _, err := cp.Restore(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatalf("created file survived restore: err=%v", err)
	}
}

func TestRestoreRecreatesDeletedFile(t *testing.T) {
	s, work := newStore(t)
	f := filepath.Join(work, "nested", "deep", "a.txt")
	write(t, f, "original")

	cp, err := s.Open("turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.Add(f); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(work, "nested")); err != nil {
		t.Fatal(err)
	}

	if _, err := cp.Restore(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, f); got != "original" {
		t.Fatalf("restore gave %q, want %q", got, "original")
	}
}

// TestAddFirstCaptureWins pins that a checkpoint holds the state at the start
// of the turn. Re-capturing on each write would build a restore point that
// undoes only the last edit, which looks correct until a turn edits one file
// twice.
func TestAddFirstCaptureWins(t *testing.T) {
	s, work := newStore(t)
	f := filepath.Join(work, "a.txt")
	write(t, f, "v1")

	cp, err := s.Open("turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.Add(f); err != nil {
		t.Fatal(err)
	}
	write(t, f, "v2")
	if err := cp.Add(f); err != nil {
		t.Fatal(err)
	}
	write(t, f, "v3")

	if _, err := cp.Restore(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, f); got != "v1" {
		t.Fatalf("restore gave %q, want the turn-start content %q", got, "v1")
	}
}

// TestRestoreIsIdempotent is the property the docs offer in place of
// atomicity: a half-applied restore is fixed by running it again.
func TestRestoreIsIdempotent(t *testing.T) {
	s, work := newStore(t)
	kept := filepath.Join(work, "kept.txt")
	created := filepath.Join(work, "created.txt")
	write(t, kept, "original")

	cp, err := s.Open("turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.Add(kept, created); err != nil {
		t.Fatal(err)
	}
	write(t, kept, "clobbered")
	write(t, created, "new")

	for i := range 3 {
		if _, err := cp.Restore(); err != nil {
			t.Fatalf("restore %d: %v", i, err)
		}
		if got := read(t, kept); got != "original" {
			t.Fatalf("restore %d gave %q", i, got)
		}
		if _, err := os.Stat(created); !os.IsNotExist(err) {
			t.Fatalf("restore %d left the created file: %v", i, err)
		}
	}
}

// TestBlobsDedupeAcrossCheckpoints pins the reason content addressing was
// chosen: a file captured every turn and edited once costs two blobs, not one
// per turn.
func TestBlobsDedupeAcrossCheckpoints(t *testing.T) {
	s, work := newStore(t)
	f := filepath.Join(work, "a.txt")
	write(t, f, "unchanged")

	for _, id := range []string{"t1", "t2", "t3", "t4", "t5"} {
		cp, err := s.Open(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := cp.Add(f); err != nil {
			t.Fatal(err)
		}
	}
	if n := countBlobs(t, s); n != 1 {
		t.Fatalf("5 captures of identical content stored %d blobs, want 1", n)
	}

	write(t, f, "changed")
	cp, err := s.Open("t6")
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.Add(f); err != nil {
		t.Fatal(err)
	}
	if n := countBlobs(t, s); n != 2 {
		t.Fatalf("after an edit, %d blobs, want 2", n)
	}
}

func countBlobs(t *testing.T, s *Store) int {
	t.Helper()
	n := 0
	err := filepath.Walk(filepath.Join(s.root, "blobs"), func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestReopenAccumulates pins that a turn spanning several tool calls builds
// one restore point rather than several.
func TestReopenAccumulates(t *testing.T) {
	s, work := newStore(t)
	a, b := filepath.Join(work, "a.txt"), filepath.Join(work, "b.txt")
	write(t, a, "a1")
	write(t, b, "b1")

	first, err := s.Open("turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Add(a); err != nil {
		t.Fatal(err)
	}
	second, err := s.Open("turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Add(b); err != nil {
		t.Fatal(err)
	}
	if got := len(second.Paths()); got != 2 {
		t.Fatalf("reopened checkpoint has %d paths, want 2", got)
	}

	write(t, a, "a2")
	write(t, b, "b2")
	if _, err := second.Restore(); err != nil {
		t.Fatal(err)
	}
	if read(t, a) != "a1" || read(t, b) != "b1" {
		t.Fatalf("restore missed a path: a=%q b=%q", read(t, a), read(t, b))
	}
}

func TestListNewestFirst(t *testing.T) {
	s, work := newStore(t)
	f := filepath.Join(work, "a.txt")
	write(t, f, "x")
	for _, id := range []string{"t1", "t2", "t3"} {
		cp, err := s.Open(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := cp.Add(f); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("listed %d checkpoints, want 3", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Created.Before(got[i].Created) {
			t.Fatalf("List is not newest-first: %+v", got)
		}
	}
}

func TestLoadCorruptManifestErrors(t *testing.T) {
	s, _ := newStore(t)
	if err := os.WriteFile(s.manifestPath("bad"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := s.Load("bad")
	if err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("corrupt manifest gave err=%v, want a corruption error", err)
	}
}

func TestLoadMissingIsNotExist(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Load("nope"); !os.IsNotExist(err) {
		t.Fatalf("Load of an unknown id gave %v, want an IsNotExist error", err)
	}
}

// TestConcurrentAdd pins safety under the Runner's parallel tool dispatch,
// where two calls in one turn capture at the same time.
func TestConcurrentAdd(t *testing.T) {
	s, work := newStore(t)
	cp, err := s.Open("turn-1")
	if err != nil {
		t.Fatal(err)
	}

	const n = 16
	paths := make([]string, n)
	for i := range n {
		paths[i] = filepath.Join(work, string(rune('a'+i))+".txt")
		write(t, paths[i], "v1")
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = cp.Add(paths[i])
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent Add %d: %v", i, err)
		}
	}
	if got := len(cp.Paths()); got != n {
		t.Fatalf("captured %d paths, want %d", got, n)
	}
}

// TestReversalIsRestoreOnly pins the seam boundary: a file checkpoint offers
// nothing the harness must ask a human about, because nothing it does reaches
// off the machine.
func TestReversalIsRestoreOnly(t *testing.T) {
	s, work := newStore(t)
	f := filepath.Join(work, "a.txt")
	write(t, f, "original")
	cp, err := s.Open("turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.Add(f); err != nil {
		t.Fatal(err)
	}

	rev := cp.Reversal()
	if !rev.Reversible() {
		t.Fatal("a file checkpoint should be automatically reversible")
	}
	if rev.Compensate != nil {
		t.Fatalf("a file checkpoint should offer no compensation, got %+v", rev.Compensate)
	}
	if rev.IsZero() {
		t.Fatal("a populated checkpoint should not be a zero Reversal")
	}
}
