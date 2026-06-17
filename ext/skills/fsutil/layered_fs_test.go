package fsutil_test

import (
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/panyam/mcpkit/ext/skills/fsutil"
)

func TestNewLayered_MountsUnderPrefix(t *testing.T) {
	a := fstest.MapFS{"SKILL.md": &fstest.MapFile{Data: []byte("a")}}
	b := fstest.MapFS{"SKILL.md": &fstest.MapFile{Data: []byte("b")}}

	lfs, err := fsutil.NewLayered(
		fsutil.Layer{Prefix: "alpha", FSys: a},
		fsutil.Layer{Prefix: "beta", FSys: b},
	)
	if err != nil {
		t.Fatalf("NewLayered: %v", err)
	}

	if got := mustReadFile(t, lfs, "alpha/SKILL.md"); got != "a" {
		t.Errorf("alpha/SKILL.md = %q, want %q", got, "a")
	}
	if got := mustReadFile(t, lfs, "beta/SKILL.md"); got != "b" {
		t.Errorf("beta/SKILL.md = %q, want %q", got, "b")
	}
}

func TestNewLayered_RejectsDuplicatePrefix(t *testing.T) {
	a := fstest.MapFS{"SKILL.md": &fstest.MapFile{Data: []byte("a")}}
	_, err := fsutil.NewLayered(
		fsutil.Layer{Prefix: "same", FSys: a},
		fsutil.Layer{Prefix: "same", FSys: a},
	)
	if err == nil {
		t.Fatalf("NewLayered with duplicate prefix succeeded; want error")
	}
	if !strings.Contains(err.Error(), "duplicate prefix") {
		t.Errorf("err = %q, want 'duplicate prefix'", err)
	}
}

func TestNewFlat_MergesAtRoot(t *testing.T) {
	a := fstest.MapFS{"foo.md": &fstest.MapFile{Data: []byte("foo")}}
	b := fstest.MapFS{"bar.md": &fstest.MapFile{Data: []byte("bar")}}

	lfs, err := fsutil.NewFlat(
		fsutil.Layer{FSys: a},
		fsutil.Layer{FSys: b},
	)
	if err != nil {
		t.Fatalf("NewFlat: %v", err)
	}

	if got := mustReadFile(t, lfs, "foo.md"); got != "foo" {
		t.Errorf("foo.md = %q", got)
	}
	if got := mustReadFile(t, lfs, "bar.md"); got != "bar" {
		t.Errorf("bar.md = %q", got)
	}
}

func TestNewFlat_RejectsRootCollision(t *testing.T) {
	a := fstest.MapFS{"clash.md": &fstest.MapFile{Data: []byte("a")}}
	b := fstest.MapFS{"clash.md": &fstest.MapFile{Data: []byte("b")}}

	_, err := fsutil.NewFlat(
		fsutil.Layer{FSys: a},
		fsutil.Layer{FSys: b},
	)
	if err == nil {
		t.Fatalf("NewFlat with root collision succeeded; want error")
	}
	if !strings.Contains(err.Error(), "root-level collision") {
		t.Errorf("err = %q, want 'root-level collision'", err)
	}
}

func TestLayeredFS_CloseInvokesLayerClosers(t *testing.T) {
	called := false
	closer := closerFn(func() error { called = true; return nil })

	lfs, err := fsutil.NewLayered(fsutil.Layer{
		Prefix: "x",
		FSys:   fstest.MapFS{},
		Closer: closer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := lfs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !called {
		t.Errorf("layer Closer not invoked")
	}
}

type closerFn func() error

func (f closerFn) Close() error { return f() }

func mustReadFile(t *testing.T, fsys fs.FS, name string) string {
	t.Helper()
	f, err := fsys.Open(name)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer f.Close()
	body, err := io.ReadAll(f)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(body)
}
