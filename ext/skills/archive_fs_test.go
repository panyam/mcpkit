package skills_test

import (
	"errors"
	"io/fs"
	"os"
	"sort"
	"testing"
	"testing/fstest"

	"github.com/panyam/mcpkit/ext/skills"
)

func TestArchiveFS_FeedsProvider(t *testing.T) {
	// Pack a known skill, mount the archive as fs.FS via NewArchiveFS,
	// and feed the result into a Provider after relocating the archive
	// contents under a parent path (since ArchiveFS contains the skill
	// files at its root). Provider should produce the same URIs as a
	// direct walk of the on-disk fixture.
	data, err := skills.PackSkill(os.DirFS("testdata/valid"), "pdf-processing", skills.ArchiveFormatTarGz)
	if err != nil {
		t.Fatalf("PackSkill: %v", err)
	}
	afs, err := skills.NewArchiveFS(data, skills.ArchiveFormatTarGz, 0)
	if err != nil {
		t.Fatalf("NewArchiveFS: %v", err)
	}

	relocated := fstest.MapFS{}
	walkErr := fs.WalkDir(afs, ".", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		body, err := fs.ReadFile(afs, p)
		if err != nil {
			return err
		}
		relocated["pdf-processing/"+p] = &fstest.MapFile{Data: body}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("WalkDir(ArchiveFS): %v", walkErr)
	}

	p, err := skills.NewProvider(skills.WithFS(relocated))
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	got := urisOf(p.Resources())
	sort.Strings(got)
	want := []string{
		"skill://pdf-processing/SKILL.md",
		"skill://pdf-processing/references/FORMS.md",
		"skill://pdf-processing/scripts/extract.py",
	}
	if !equalSlices(got, want) {
		t.Errorf("URIs = %v, want %v", got, want)
	}
}

func TestArchiveFS_RejectsUnsafeArchive(t *testing.T) {
	data := mustTarGz(t, map[string]string{
		"../escape.md": "out of bounds",
	})
	_, err := skills.NewArchiveFS(data, skills.ArchiveFormatTarGz, 0)
	if !errors.Is(err, skills.ErrArchivePathTraversal) {
		t.Errorf("err = %v, want ErrArchivePathTraversal", err)
	}
}

func TestArchiveFS_WalkDirVisitsFiles(t *testing.T) {
	data := mustTarGz(t, map[string]string{
		"SKILL.md":            "---\nname: x\ndescription: y\n---\n",
		"references/A.md":     "ref a",
		"references/sub/B.md": "ref b nested",
		"scripts/run.py":      "print('hi')\n",
	})
	afs, err := skills.NewArchiveFS(data, skills.ArchiveFormatTarGz, 0)
	if err != nil {
		t.Fatalf("NewArchiveFS: %v", err)
	}

	var visited []string
	walkErr := fs.WalkDir(afs, ".", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		visited = append(visited, p)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("WalkDir: %v", walkErr)
	}
	sort.Strings(visited)
	want := []string{
		"SKILL.md",
		"references/A.md",
		"references/sub/B.md",
		"scripts/run.py",
	}
	if !equalSlices(visited, want) {
		t.Errorf("visited = %v, want %v", visited, want)
	}
}

func TestArchiveFS_ReadFileReturnsBody(t *testing.T) {
	data := mustTarGz(t, map[string]string{
		"hello.txt": "hello world",
	})
	afs, err := skills.NewArchiveFS(data, skills.ArchiveFormatTarGz, 0)
	if err != nil {
		t.Fatalf("NewArchiveFS: %v", err)
	}
	body, err := fs.ReadFile(afs, "hello.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(body) != "hello world" {
		t.Errorf("body = %q, want %q", body, "hello world")
	}
}

func TestArchiveFS_FormatAutoDetect(t *testing.T) {
	data := mustTarGz(t, map[string]string{"a.txt": "alpha"})
	afs, err := skills.NewArchiveFS(data, skills.ArchiveFormatUnknown, 0)
	if err != nil {
		t.Fatalf("NewArchiveFS auto-detect: %v", err)
	}
	body, err := fs.ReadFile(afs, "a.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(body) != "alpha" {
		t.Errorf("body = %q", body)
	}
}
