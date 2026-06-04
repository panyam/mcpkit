package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPackUnpack_RoundTrip packs the pdf-processing testdata skill,
// unpacks the archive into a sibling directory, and asserts every
// source file reappears with byte-identical content.
func TestPackUnpack_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	src, err := filepath.Abs("../../ext/skills/testdata/valid/pdf-processing")
	if err != nil {
		t.Fatalf("abs(src): %v", err)
	}

	// Pack.
	archivePath := filepath.Join(tmp, "pdf-processing.tar.gz")
	out := &bytes.Buffer{}
	root := newRoot()
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"pack", src, "-o", archivePath, "--color", "never"})
	if err := root.Execute(); err != nil {
		t.Fatalf("pack Execute: %v\noutput:\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "wrote") {
		t.Errorf("pack output missing success line\noutput:\n%s", out.String())
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		t.Fatalf("stat archive: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("archive is empty")
	}

	// Unpack.
	unpackDir := filepath.Join(tmp, "unpacked")
	out.Reset()
	root = newRoot()
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"unpack", archivePath, "-o", unpackDir, "--color", "never"})
	if err := root.Execute(); err != nil {
		t.Fatalf("unpack Execute: %v\noutput:\n%s", err, out.String())
	}

	// Round-trip check: every file in the source appears under
	// unpackDir with identical bytes.
	if err := filepath.Walk(src, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		srcBytes, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		gotBytes, err := os.ReadFile(filepath.Join(unpackDir, rel))
		if err != nil {
			return err
		}
		if !bytes.Equal(srcBytes, gotBytes) {
			t.Errorf("%s: bytes differ after round-trip", rel)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk src: %v", err)
	}
}

// TestPackUnpack_Zip exercises the same round-trip in zip mode so the
// --format flag and DetectArchiveFormat both light up.
func TestPackUnpack_Zip(t *testing.T) {
	tmp := t.TempDir()
	src, err := filepath.Abs("../../ext/skills/testdata/valid/git-workflow")
	if err != nil {
		t.Fatalf("abs(src): %v", err)
	}
	archivePath := filepath.Join(tmp, "git-workflow.zip")

	out := &bytes.Buffer{}
	root := newRoot()
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"pack", src, "-o", archivePath, "--format", "zip", "--color", "never"})
	if err := root.Execute(); err != nil {
		t.Fatalf("pack zip Execute: %v\noutput:\n%s", err, out.String())
	}

	unpackDir := filepath.Join(tmp, "unpacked")
	out.Reset()
	root = newRoot()
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"unpack", archivePath, "-o", unpackDir, "--color", "never"})
	if err := root.Execute(); err != nil {
		t.Fatalf("unpack zip Execute: %v\noutput:\n%s", err, out.String())
	}

	if _, err := os.Stat(filepath.Join(unpackDir, "SKILL.md")); err != nil {
		t.Fatalf("unpacked SKILL.md missing: %v", err)
	}
}

// TestPack_BadFormat asserts pack rejects an unknown --format value
// rather than producing a garbage archive.
func TestPack_BadFormat(t *testing.T) {
	out := &bytes.Buffer{}
	root := newRoot()
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"pack", "../../ext/skills/testdata/valid/pdf-processing", "--format", "rar", "--color", "never"})
	if err := root.Execute(); err == nil {
		t.Fatalf("Execute: want non-nil error for bad format, got nil")
	}
}
