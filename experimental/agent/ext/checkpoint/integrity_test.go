package checkpoint

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// symlinkOrSkip creates a link, skipping the test where the platform will not
// allow one rather than reporting a failure that says nothing about the code.
func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func captured(t *testing.T, s *Store, id string, paths ...string) *Checkpoint {
	t.Helper()
	cp, err := s.Open(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.Add(paths...); err != nil {
		t.Fatal(err)
	}
	return cp
}

// TestRestoreRefusesToWriteThroughASymlink is the case this file exists for.
// The gap between capture and /undo is a person deciding to type it, so a path
// has minutes or hours in which to become a link pointing somewhere else. The
// content is right there in the blob store and the rename would succeed.
func TestRestoreRefusesToWriteThroughASymlink(t *testing.T) {
	s, work := newStore(t)
	outside := filepath.Join(t.TempDir(), "important.conf")
	write(t, outside, "do not touch\n")

	f := filepath.Join(work, "a.txt")
	write(t, f, "original")
	cp := captured(t, s, "turn-1", f)

	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, outside, f)

	res, err := cp.Restore()
	if err != nil {
		t.Fatalf("Restore() error = %v, want a refusal rather than an error", err)
	}
	if len(res.Refused) != 1 {
		t.Fatalf("Refused = %+v, want exactly one", res.Refused)
	}
	if got := read(t, outside); got != "do not touch\n" {
		t.Errorf("wrote through the symlink: %q", got)
	}
	if !strings.Contains(res.Refused[0].Reason, "symlink") {
		t.Errorf("reason should name what it found, got %q", res.Refused[0].Reason)
	}
}

// TestRestoreRefusesToDeleteThroughASymlink covers the absent case, which is
// the more destructive of the two: undoing a creation means os.Remove, and a
// path that became a link would delete someone else's file outright.
func TestRestoreRefusesToDeleteThroughASymlink(t *testing.T) {
	s, work := newStore(t)
	outside := filepath.Join(t.TempDir(), "important.conf")
	write(t, outside, "do not delete\n")

	created := filepath.Join(work, "new.txt")
	cp := captured(t, s, "turn-1", created) // absent at capture

	symlinkOrSkip(t, outside, created)

	res, err := cp.Restore()
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if len(res.Refused) != 1 {
		t.Fatalf("Refused = %+v, want exactly one", res.Refused)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("deleted through the symlink: %v", err)
	}
}

// TestOneRefusalDoesNotBlockTheRest is the partial-restore choice. A single
// tampered path should not cost the user the rest of the turn.
func TestOneRefusalDoesNotBlockTheRest(t *testing.T) {
	s, work := newStore(t)
	outside := filepath.Join(t.TempDir(), "elsewhere")
	write(t, outside, "untouched\n")

	good := filepath.Join(work, "good.txt")
	bad := filepath.Join(work, "bad.txt")
	write(t, good, "original good")
	write(t, bad, "original bad")
	cp := captured(t, s, "turn-1", good, bad)

	write(t, good, "clobbered good")
	if err := os.Remove(bad); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, outside, bad)

	res, err := cp.Restore()
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if got := read(t, good); got != "original good" {
		t.Errorf("the untampered path should still be restored, got %q", got)
	}
	if len(res.Restored) != 1 || res.Restored[0] != good {
		t.Errorf("Restored = %v, want just the good path", res.Restored)
	}
	if len(res.Refused) != 1 || res.Refused[0].Path != bad {
		t.Errorf("Refused = %+v, want just the tampered path", res.Refused)
	}
	if res.Complete() {
		t.Error("Complete() must be false when anything was refused")
	}
}

// TestSymlinkAtCaptureIsNotFollowed guards the other end. Capturing through a
// link would store the target's content and set up a restore that writes back
// through it, so the link is recorded as unsupported instead.
func TestSymlinkAtCaptureIsNotFollowed(t *testing.T) {
	s, work := newStore(t)
	outside := filepath.Join(t.TempDir(), "target")
	write(t, outside, "target content\n")

	link := filepath.Join(work, "link.txt")
	symlinkOrSkip(t, outside, link)

	cp := captured(t, s, "turn-1", link)
	write(t, outside, "changed since capture\n")

	res, err := cp.Restore()
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if len(res.Refused) != 1 {
		t.Fatalf("Refused = %+v, want exactly one", res.Refused)
	}
	if got := read(t, outside); got != "changed since capture\n" {
		t.Errorf("restored through a link captured as a link: %q", got)
	}
}

// TestCaptureThroughASymlinkDoesNotStoreTheTarget isolates the capture side.
// Its sibling above cannot: with a link still in place at restore time the
// restore-side guard refuses either way, so that test passes even if capture
// followed the link.
//
// Replacing the link with a real file removes that guard. If capture followed
// it, the checkpoint holds the target's content under a regular-file kind, and
// the restore writes a file the caller never named into a path that now looks
// perfectly ordinary.
func TestCaptureThroughASymlinkDoesNotStoreTheTarget(t *testing.T) {
	s, work := newStore(t)
	outside := filepath.Join(t.TempDir(), "target")
	write(t, outside, "secret target content\n")

	p := filepath.Join(work, "innocent.txt")
	symlinkOrSkip(t, outside, p)

	cp := captured(t, s, "turn-1", p)

	// The link becomes an ordinary file, so nothing at restore time looks
	// suspicious any more.
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	write(t, p, "a real file now\n")

	res, err := cp.Restore()
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if got := read(t, p); got == "secret target content\n" {
		t.Fatal("capture followed the symlink and restored the target's content")
	}
	if got := read(t, p); got != "a real file now\n" {
		t.Errorf("file should have been left alone, got %q", got)
	}
	if len(res.Refused) != 1 {
		t.Errorf("Refused = %+v, want the unsupported capture refused", res.Refused)
	}
}

// TestReversalReportsARefusalAsAnError pins the seam's half of A11. The
// harness runs a Reversal unattended and has nowhere to put detail, so a
// partial restore has to reach it as a failure rather than as a silent
// success.
func TestReversalReportsARefusalAsAnError(t *testing.T) {
	s, work := newStore(t)
	outside := filepath.Join(t.TempDir(), "elsewhere")
	write(t, outside, "untouched\n")

	f := filepath.Join(work, "a.txt")
	write(t, f, "original")
	cp := captured(t, s, "turn-1", f)

	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, outside, f)

	rev := cp.Reversal()
	if !rev.Reversible() {
		t.Fatal("a checkpoint reversal must still offer a Restore")
	}
	if err := rev.Restore(context.Background()); err == nil {
		t.Fatal("Reversal.Restore must report a refusal as an error")
	}
}

func TestReversalSucceedsWhenNothingIsRefused(t *testing.T) {
	s, work := newStore(t)
	f := filepath.Join(work, "a.txt")
	write(t, f, "original")
	cp := captured(t, s, "turn-1", f)
	write(t, f, "clobbered")

	if err := cp.Reversal().Restore(context.Background()); err != nil {
		t.Fatalf("Reversal.Restore() error = %v, want nil", err)
	}
	if got := read(t, f); got != "original" {
		t.Errorf("restore gave %q", got)
	}
}

// TestUndoReportNamesRefusedPaths is A11's corollary at the surface a person
// actually reads. A refusal that only reached the Restored count would be an
// unreported hole, and an unreported hole stops being checked.
func TestUndoReportNamesRefusedPaths(t *testing.T) {
	e := &Extension{}
	cp := &Checkpoint{id: "turn-7"}
	res := RestoreResult{
		Restored: []string{"/work/a.txt", "/work/b.txt", "/work/c.txt"},
		Refused: []Refusal{{
			Path:   "/work/notes.md",
			Reason: "is now a symlink, was regular at capture",
		}},
	}

	report := e.undoReport(cp, res)
	if !strings.Contains(report, "3 file(s) restored") {
		t.Errorf("report should count what was actually restored:\n%s", report)
	}
	for _, want := range []string{"REFUSED", "/work/notes.md", "is now a symlink"} {
		if !strings.Contains(report, want) {
			t.Errorf("report is missing %q:\n%s", want, report)
		}
	}
}

// TestRestoredCountExcludesRefusals is the specific lie worth preventing: the
// report used to count captured paths, so a refused path was announced as
// restored.
func TestRestoredCountExcludesRefusals(t *testing.T) {
	e := &Extension{}
	cp := &Checkpoint{id: "turn-7"}
	res := RestoreResult{
		Restored: []string{"/work/a.txt"},
		Refused:  []Refusal{{Path: "/work/b.txt", Reason: "is now a directory, was regular at capture"}},
	}
	if report := e.undoReport(cp, res); !strings.Contains(report, "1 file(s) restored") {
		t.Errorf("want 1 restored, not 2:\n%s", report)
	}
}

// TestOldManifestIsNamedNotCalledCorrupt keeps an upgrade from sending someone
// to look for a truncated write. The previous format stored a bare
// path-to-hash map, which unmarshals into the current one as a type error.
func TestOldManifestIsNamedNotCalledCorrupt(t *testing.T) {
	s, _ := newStore(t)
	old := map[string]any{
		"id":      "turn-1",
		"created": "2026-08-10T00:00:00Z",
		"files":   map[string]string{"/work/a.txt": "deadbeef"},
	}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.manifestPath("turn-1"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = s.Load("turn-1")
	if err == nil {
		t.Fatal("loading a v0 manifest should fail")
	}
	if strings.Contains(err.Error(), "corrupt") {
		t.Errorf("an old format is not corruption: %v", err)
	}
	if !strings.Contains(err.Error(), "format v0") {
		t.Errorf("error should name the format found: %v", err)
	}
}

func TestManifestRoundTripsKinds(t *testing.T) {
	s, work := newStore(t)
	present := filepath.Join(work, "present.txt")
	write(t, present, "hello")
	missing := filepath.Join(work, "missing.txt")

	cp := captured(t, s, "turn-1", present, missing)
	_ = cp

	reloaded, err := s.Load("turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.files[present].Kind; got != kindRegular {
		t.Errorf("present path kind = %q, want %q", got, kindRegular)
	}
	if got := reloaded.files[missing].Kind; got != kindAbsent {
		t.Errorf("missing path kind = %q, want %q", got, kindAbsent)
	}
	if reloaded.files[present].Hash == "" {
		t.Error("a regular capture should carry a blob hash")
	}
	if reloaded.files[missing].Hash != "" {
		t.Error("an absent capture should carry no blob hash")
	}
}
