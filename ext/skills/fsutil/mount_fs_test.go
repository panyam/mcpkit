package fsutil_test

import (
	"errors"
	"io"
	"io/fs"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/panyam/mcpkit/ext/skills/fsutil"
)

func TestNewMountFS_SubMountUnderPath(t *testing.T) {
	a := fstest.MapFS{"SKILL.md": &fstest.MapFile{Data: []byte("a")}}
	b := fstest.MapFS{"SKILL.md": &fstest.MapFile{Data: []byte("b")}}

	m, err := fsutil.NewMountFS(
		fsutil.Mount{Path: "alpha", FSys: a},
		fsutil.Mount{Path: "beta", FSys: b},
	)
	if err != nil {
		t.Fatalf("NewMountFS: %v", err)
	}

	if got := readFile(t, m, "alpha/SKILL.md"); got != "a" {
		t.Errorf("alpha/SKILL.md = %q, want %q", got, "a")
	}
	if got := readFile(t, m, "beta/SKILL.md"); got != "b" {
		t.Errorf("beta/SKILL.md = %q, want %q", got, "b")
	}
}

func TestNewMountFS_RejectsDuplicateSubMountPath(t *testing.T) {
	a := fstest.MapFS{"SKILL.md": &fstest.MapFile{Data: []byte("a")}}
	_, err := fsutil.NewMountFS(
		fsutil.Mount{Path: "same", FSys: a},
		fsutil.Mount{Path: "same", FSys: a},
	)
	if err == nil {
		t.Fatalf("duplicate Path succeeded; want error")
	}
	if !strings.Contains(err.Error(), "duplicate Path") {
		t.Errorf("err = %q, want 'duplicate Path'", err)
	}
}

func TestNewMountFS_RejectsPathWithSlash(t *testing.T) {
	a := fstest.MapFS{"SKILL.md": &fstest.MapFile{Data: []byte("a")}}
	_, err := fsutil.NewMountFS(
		fsutil.Mount{Path: "acme/billing", FSys: a},
	)
	if err == nil {
		t.Fatalf("Path with '/' succeeded; want error")
	}
}

func TestNewMountFS_RootMount(t *testing.T) {
	local := fstest.MapFS{
		"pdf-processing/SKILL.md": &fstest.MapFile{Data: []byte("local")},
	}
	m, err := fsutil.NewMountFS(fsutil.Mount{FSys: local})
	if err != nil {
		t.Fatalf("NewMountFS root: %v", err)
	}
	// Root mount serves at the FS root (no prefix needed).
	if got := readFile(t, m, "pdf-processing/SKILL.md"); got != "local" {
		t.Errorf("root mount serve = %q, want %q", got, "local")
	}
}

func TestNewMountFS_HybridRootPlusSubMount(t *testing.T) {
	local := fstest.MapFS{
		"pdf-processing/SKILL.md": &fstest.MapFile{Data: []byte("local")},
	}
	github := fstest.MapFS{
		"web-scraper/SKILL.md": &fstest.MapFile{Data: []byte("gh")},
	}

	m, err := fsutil.NewMountFS(
		fsutil.Mount{FSys: local},                       // root
		fsutil.Mount{Path: "github", FSys: github},      // sub-mount
	)
	if err != nil {
		t.Fatalf("hybrid: %v", err)
	}

	// Root paths resolve unprefixed.
	if got := readFile(t, m, "pdf-processing/SKILL.md"); got != "local" {
		t.Errorf("root path = %q, want %q", got, "local")
	}
	// Sub-mount paths resolve via prefix.
	if got := readFile(t, m, "github/web-scraper/SKILL.md"); got != "gh" {
		t.Errorf("sub-mount path = %q, want %q", got, "gh")
	}
}

func TestNewMountFS_MultipleRootsFlatMerge(t *testing.T) {
	science := fstest.MapFS{
		"photosynthesis/SKILL.md": &fstest.MapFile{Data: []byte("sci")},
	}
	math := fstest.MapFS{
		"algebra/SKILL.md": &fstest.MapFile{Data: []byte("math")},
	}

	m, err := fsutil.NewMountFS(
		fsutil.Mount{FSys: science},
		fsutil.Mount{FSys: math},
	)
	if err != nil {
		t.Fatalf("multi-root: %v", err)
	}

	if got := readFile(t, m, "photosynthesis/SKILL.md"); got != "sci" {
		t.Errorf("science root path = %q", got)
	}
	if got := readFile(t, m, "algebra/SKILL.md"); got != "math" {
		t.Errorf("math root path = %q", got)
	}
}

func TestNewMountFS_RejectsRootCollision(t *testing.T) {
	a := fstest.MapFS{"clash/SKILL.md": &fstest.MapFile{Data: []byte("a")}}
	b := fstest.MapFS{"clash/SKILL.md": &fstest.MapFile{Data: []byte("b")}}

	_, err := fsutil.NewMountFS(
		fsutil.Mount{FSys: a},
		fsutil.Mount{FSys: b},
	)
	if err == nil {
		t.Fatalf("root collision succeeded; want error")
	}
	if !strings.Contains(err.Error(), "root-level collision") {
		t.Errorf("err = %q, want 'root-level collision'", err)
	}
}

func TestNewMountFS_RejectsSubMountVsRootCollision(t *testing.T) {
	root := fstest.MapFS{"github/SKILL.md": &fstest.MapFile{Data: []byte("conflict")}}
	gh := fstest.MapFS{"foo/SKILL.md": &fstest.MapFile{Data: []byte("x")}}

	_, err := fsutil.NewMountFS(
		fsutil.Mount{FSys: root},                       // contains "github" at root
		fsutil.Mount{Path: "github", FSys: gh},         // sub-mount also "github"
	)
	if err == nil {
		t.Fatalf("cross-collision succeeded; want error")
	}
	if !strings.Contains(err.Error(), "collides with") {
		t.Errorf("err = %q, want 'collides with'", err)
	}
}

func TestNewMountFS_Hierarchical_NestedComposition(t *testing.T) {
	// Three levels: leaf inside mid inside top.
	leafA := fstest.MapFS{"refunds/SKILL.md": &fstest.MapFile{Data: []byte("acme-refunds")}}
	leafB := fstest.MapFS{"users/SKILL.md": &fstest.MapFile{Data: []byte("globex-users")}}

	mid, err := fsutil.NewMountFS(
		fsutil.Mount{Path: "acme", FSys: leafA},
		fsutil.Mount{Path: "globex", FSys: leafB},
	)
	if err != nil {
		t.Fatal(err)
	}

	bundled := fstest.MapFS{"git-workflow/SKILL.md": &fstest.MapFile{Data: []byte("bundled")}}
	top, err := fsutil.NewMountFS(
		fsutil.Mount{FSys: bundled},                          // bundled at root
		fsutil.Mount{Path: "tenants", FSys: mid, Closer: mid}, // nested MountFS
	)
	if err != nil {
		t.Fatal(err)
	}

	// Three-level path: top → tenants → acme → refunds/SKILL.md
	if got := readFile(t, top, "tenants/acme/refunds/SKILL.md"); got != "acme-refunds" {
		t.Errorf("nested path = %q, want %q", got, "acme-refunds")
	}
	if got := readFile(t, top, "tenants/globex/users/SKILL.md"); got != "globex-users" {
		t.Errorf("nested path = %q, want %q", got, "globex-users")
	}
	// Bundled root still resolves at the top level.
	if got := readFile(t, top, "git-workflow/SKILL.md"); got != "bundled" {
		t.Errorf("bundled path = %q, want %q", got, "bundled")
	}
}

func TestNewMountFS_CloseRecursesThroughNesting(t *testing.T) {
	var leafClosed, midClosed atomic.Bool

	leafCloser := closerFn(func() error { leafClosed.Store(true); return nil })
	leaf := fstest.MapFS{"x/SKILL.md": &fstest.MapFile{Data: []byte("x")}}

	mid, err := fsutil.NewMountFS(
		fsutil.Mount{Path: "leaf", FSys: leaf, Closer: leafCloser},
	)
	if err != nil {
		t.Fatal(err)
	}
	midWrapper := closerFn(func() error {
		midClosed.Store(true)
		return mid.Close()
	})

	top, err := fsutil.NewMountFS(
		fsutil.Mount{Path: "mid", FSys: mid, Closer: midWrapper},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := top.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !midClosed.Load() {
		t.Errorf("mid not closed")
	}
	if !leafClosed.Load() {
		t.Errorf("leaf not closed (closer did not recurse)")
	}
}

func TestNewMountFS_CloseInvokesEveryMountCloser(t *testing.T) {
	var aCount, bCount atomic.Int32
	a := closerFn(func() error { aCount.Add(1); return nil })
	b := closerFn(func() error { bCount.Add(1); return nil })

	m, err := fsutil.NewMountFS(
		fsutil.Mount{Path: "alpha", FSys: fstest.MapFS{}, Closer: a},
		fsutil.Mount{Path: "beta", FSys: fstest.MapFS{}, Closer: b},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if aCount.Load() != 1 || bCount.Load() != 1 {
		t.Errorf("Close did not invoke each Closer once: a=%d b=%d", aCount.Load(), bCount.Load())
	}
}

type closerFn func() error

func (f closerFn) Close() error { return f() }

func readFile(t *testing.T, fsys fs.FS, name string) string {
	t.Helper()
	f, err := fsys.Open(name)
	if err != nil {
		t.Fatalf("open %q: %v", name, err)
	}
	defer f.Close()
	body, err := io.ReadAll(f)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("read %q: %v", name, err)
	}
	return string(body)
}
