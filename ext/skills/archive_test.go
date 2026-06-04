package skills_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"runtime"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/skills"
	"github.com/panyam/mcpkit/server"
)

func TestPackUnpack_TarGz_RoundTrip(t *testing.T) {
	fsys := os.DirFS("testdata/valid")
	data, err := skills.PackSkill(fsys, "pdf-processing", skills.ArchiveFormatTarGz)
	if err != nil {
		t.Fatalf("PackSkill: %v", err)
	}
	entries, err := skills.UnpackBytes(data, skills.ArchiveFormatTarGz, 0)
	if err != nil {
		t.Fatalf("UnpackBytes: %v", err)
	}

	want := map[string]string{
		"SKILL.md":            mustRead(t, "testdata/valid/pdf-processing/SKILL.md"),
		"references/FORMS.md": mustRead(t, "testdata/valid/pdf-processing/references/FORMS.md"),
		"scripts/extract.py":  mustRead(t, "testdata/valid/pdf-processing/scripts/extract.py"),
	}
	gotMap := make(map[string]string, len(entries))
	for _, e := range entries {
		gotMap[e.Path] = string(e.Body)
	}
	for path, body := range want {
		if got, ok := gotMap[path]; !ok {
			t.Errorf("missing entry %q", path)
		} else if got != body {
			t.Errorf("entry %q body mismatch", path)
		}
	}
}

func TestPackUnpack_Zip_RoundTrip(t *testing.T) {
	fsys := os.DirFS("testdata/valid")
	data, err := skills.PackSkill(fsys, "git-workflow", skills.ArchiveFormatZip)
	if err != nil {
		t.Fatalf("PackSkill: %v", err)
	}
	entries, err := skills.UnpackBytes(data, skills.ArchiveFormatZip, 0)
	if err != nil {
		t.Fatalf("UnpackBytes: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entry count = %d, want 1 (SKILL.md only)", len(entries))
	}
	if entries[0].Path != "SKILL.md" {
		t.Errorf("entry path = %q, want SKILL.md", entries[0].Path)
	}
	wantBody := mustRead(t, "testdata/valid/git-workflow/SKILL.md")
	if string(entries[0].Body) != wantBody {
		t.Errorf("body mismatch")
	}
}

func TestUnpack_RejectsPathTraversal(t *testing.T) {
	data := mustTarGz(t, map[string]string{
		"../escape.md": "outside skill root",
	})
	_, err := skills.UnpackBytes(data, skills.ArchiveFormatTarGz, 0)
	if !errors.Is(err, skills.ErrArchivePathTraversal) {
		t.Errorf("err = %v, want ErrArchivePathTraversal", err)
	}
}

func TestUnpack_RejectsAbsolutePath(t *testing.T) {
	data := mustTarGz(t, map[string]string{
		"/etc/passwd": "absolute path entry",
	})
	_, err := skills.UnpackBytes(data, skills.ArchiveFormatTarGz, 0)
	if !errors.Is(err, skills.ErrArchiveAbsolutePath) {
		t.Errorf("err = %v, want ErrArchiveAbsolutePath", err)
	}
}

func TestUnpack_RejectsSymlinkEscape(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{
		Name:     "escape",
		Linkname: "/etc/passwd",
		Typeflag: tar.TypeSymlink,
		Mode:     0o777,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	tw.Close()
	gz.Close()

	_, err := skills.UnpackBytes(buf.Bytes(), skills.ArchiveFormatTarGz, 0)
	if !errors.Is(err, skills.ErrArchiveSymlinkEscape) {
		t.Errorf("err = %v, want ErrArchiveSymlinkEscape", err)
	}
}

func TestUnpack_RejectsSizeBomb(t *testing.T) {
	data := mustTarGz(t, map[string]string{
		"big.bin": strings.Repeat("A", 4096),
	})
	_, err := skills.UnpackBytes(data, skills.ArchiveFormatTarGz, 1024)
	if !errors.Is(err, skills.ErrArchiveTooLarge) {
		t.Errorf("err = %v, want ErrArchiveTooLarge", err)
	}
}

func TestDetectArchiveFormat(t *testing.T) {
	cases := []struct {
		name string
		buf  []byte
		want skills.ArchiveFormat
	}{
		{"foo.tar.gz", nil, skills.ArchiveFormatTarGz},
		{"foo.zip", nil, skills.ArchiveFormatZip},
		{"", []byte{0x1F, 0x8B, 0x00, 0x00}, skills.ArchiveFormatTarGz},
		{"", []byte{0x50, 0x4B, 0x03, 0x04}, skills.ArchiveFormatZip},
		{"foo.txt", []byte("plain text"), skills.ArchiveFormatUnknown},
		{"", nil, skills.ArchiveFormatUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := skills.DetectArchiveFormat(tc.name, tc.buf)
			if got != tc.want {
				t.Errorf("DetectArchiveFormat(%q, %v) = %v, want %v", tc.name, tc.buf, got, tc.want)
			}
		})
	}
}

func TestProvider_ArchiveMode_RegistersArchiveResource(t *testing.T) {
	p, err := skills.NewProvider(
		skills.WithDirectory("testdata/valid"),
		skills.WithArchiveMode(skills.ArchiveFormatTarGz),
		skills.WithoutIndex(),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defs := p.Resources()
	got := urisOf(defs)
	want := []string{
		"skill://acme/billing/refunds.tar.gz",
		"skill://git-workflow.tar.gz",
		"skill://pdf-processing.tar.gz",
	}
	if !equalSlices(got, want) {
		t.Errorf("URIs = %v, want %v (file-mode entries should NOT appear)", got, want)
	}
	for _, d := range defs {
		if d.MimeType != "application/gzip" {
			t.Errorf("%s MimeType = %q, want application/gzip", d.URI, d.MimeType)
		}
	}
}

func TestProvider_ArchiveMode_HandlerServesArchive(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "skills-archive-test", Version: "0.0.1"})
	p, err := skills.NewProvider(
		skills.WithDirectory("testdata/valid"),
		skills.WithArchiveMode(skills.ArchiveFormatTarGz),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	p.RegisterWith(srv)

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "skills-archive-client", Version: "0.0.1"})
	if err := c.Connect(); err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	result, err := c.ReadResourceFull("skill://pdf-processing.tar.gz")
	if err != nil {
		t.Fatalf("ReadResourceFull: %v", err)
	}
	if len(result.Contents) != 1 {
		t.Fatalf("expected 1 content item, got %d", len(result.Contents))
	}
	if result.Contents[0].Blob == "" {
		t.Fatal("Blob is empty; archive content should be base64-encoded")
	}
	if result.Contents[0].Text != "" {
		t.Errorf("Text should be empty for binary archive, got %q", result.Contents[0].Text)
	}
	raw, err := base64.StdEncoding.DecodeString(result.Contents[0].Blob)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	entries, err := skills.UnpackBytes(raw, skills.ArchiveFormatTarGz, 0)
	if err != nil {
		t.Fatalf("UnpackBytes: %v", err)
	}
	if len(entries) < 2 {
		t.Errorf("expected pdf-processing to have multiple files in archive, got %d", len(entries))
	}
}

func TestIndexer_ArchiveEntries(t *testing.T) {
	p, err := skills.NewProvider(
		skills.WithDirectory("testdata/valid"),
		skills.WithArchiveMode(skills.ArchiveFormatTarGz),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	idx, err := skills.NewIndexer(p).Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if len(idx.Skills) == 0 {
		t.Fatal("expected populated Skills")
	}
	for _, e := range idx.Skills {
		if e.Type != skills.SkillTypeArchive {
			t.Errorf("entry %q type = %q, want archive", e.Name, e.Type)
		}
		if !strings.HasSuffix(e.URL, ".tar.gz") {
			t.Errorf("entry %q URL %q does not end in .tar.gz", e.Name, e.URL)
		}
		if !strings.HasPrefix(e.Digest, "sha256:") || len(e.Digest) != len("sha256:")+64 {
			t.Errorf("entry %q digest %q malformed", e.Name, e.Digest)
		}
	}
}

func TestIndexer_ArchiveDigest_MatchesPackOutput(t *testing.T) {
	p, err := skills.NewProvider(
		skills.WithDirectory("testdata/valid"),
		skills.WithArchiveMode(skills.ArchiveFormatTarGz),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	idx, err := skills.NewIndexer(p).Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	wantPacked, err := skills.PackSkill(os.DirFS("testdata/valid"), "git-workflow", skills.ArchiveFormatTarGz)
	if err != nil {
		t.Fatalf("PackSkill: %v", err)
	}
	sum := sha256.Sum256(wantPacked)
	wantDigest := "sha256:" + hex.EncodeToString(sum[:])
	for _, e := range idx.Skills {
		if e.URL == "skill://git-workflow.tar.gz" {
			if e.Digest != wantDigest {
				t.Errorf("digest = %q, want %q", e.Digest, wantDigest)
			}
			return
		}
	}
	t.Fatal("git-workflow archive entry not found")
}

func TestProvider_ArchiveMode_IndexResourceServesArchiveIndex(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "skills-archive-boot", Version: "0.0.1"})
	p, err := skills.NewProvider(
		skills.WithDirectory("testdata/valid"),
		skills.WithArchiveMode(skills.ArchiveFormatTarGz),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	p.RegisterWith(srv)

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "skills-archive-boot-client", Version: "0.0.1"})
	if err := c.Connect(); err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	body, err := c.ReadResource(skills.IndexURI)
	if err != nil {
		t.Fatalf("ReadResource index: %v", err)
	}
	var idx skills.Index
	if err := json.Unmarshal([]byte(body), &idx); err != nil {
		t.Fatalf("Unmarshal: %v\n%s", err, body)
	}
	if len(idx.Skills) == 0 {
		t.Fatal("empty Skills in served index")
	}
	for _, e := range idx.Skills {
		if e.Type != skills.SkillTypeArchive {
			t.Errorf("served entry %q type = %q, want archive", e.Name, e.Type)
		}
	}
}

func TestArchive_StreamingMemory(t *testing.T) {
	mfs := fstest.MapFS{
		"big/SKILL.md": &fstest.MapFile{Data: []byte(`---
name: big
description: streaming memory test fixture
---
`)},
		"big/a.bin": &fstest.MapFile{Data: bytes.Repeat([]byte("A"), 2*1024*1024)},
		"big/b.bin": &fstest.MapFile{Data: bytes.Repeat([]byte("B"), 2*1024*1024)},
		"big/c.bin": &fstest.MapFile{Data: bytes.Repeat([]byte("C"), 1*1024*1024)},
	}

	var statsBefore runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&statsBefore)

	data, err := skills.PackSkill(mfs, "big", skills.ArchiveFormatTarGz)
	if err != nil {
		t.Fatalf("PackSkill: %v", err)
	}

	var statsAfter runtime.MemStats
	runtime.ReadMemStats(&statsAfter)

	srcSize := int64(5*1024*1024) + 100
	growth := int64(statsAfter.HeapAlloc) - int64(statsBefore.HeapAlloc)
	t.Logf("source=%d packed=%d heap-growth=%d", srcSize, len(data), growth)

	entries, err := skills.UnpackBytes(data, skills.ArchiveFormatTarGz, 0)
	if err != nil {
		t.Fatalf("UnpackBytes: %v", err)
	}
	if len(entries) != 4 {
		t.Errorf("entry count = %d, want 4", len(entries))
	}
}

// --- helpers ---

func mustTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		body := []byte(files[name])
		hdr := &tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("WriteHeader: %v", err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
