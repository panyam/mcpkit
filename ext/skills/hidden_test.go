package skills_test

import (
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/skills"
	"github.com/panyam/mcpkit/server"
)

const hiddenSkillManifest = `---
name: deploy
description: A skill whose directory also holds dotfiles
---

body
`

// writeHiddenTree lays out one skill under root that carries a .env file,
// a .git directory and a hidden notes directory next to its real content.
// It also hides a whole skill under .cache, which discovery must not find.
// The tree is written at test time because git refuses to track a path
// containing .git.
func writeHiddenTree(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"deploy/SKILL.md":           hiddenSkillManifest,
		"deploy/scripts/run.sh":     "echo run\n",
		"deploy/.env":               "TOKEN=secret\n",
		"deploy/.git/config":        "[core]\n",
		"deploy/.notes/todo.md":     "later\n",
		"deploy/scripts/.cache.tmp": "x\n",
		".cache/ghost/SKILL.md":     "---\nname: ghost\ndescription: hidden skill\n---\n",
	}
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func bootWithOpts(t *testing.T, opts ...skills.ProviderOption) *client.Client {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "skills-hidden", Version: "0.0.1"})
	p, err := skills.NewProvider(opts...)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	p.RegisterWith(srv)
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "skills-hidden-client", Version: "0.0.1"})
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func hasHidden(uris []string) []string {
	var out []string
	for _, u := range uris {
		rest := strings.TrimPrefix(u, "skill://")
		for _, seg := range strings.Split(rest, "/") {
			if len(seg) > 1 && seg[0] == '.' && seg != ".." {
				out = append(out, u)
				break
			}
		}
	}
	return out
}

func entryFileURIs(t *testing.T, p *skills.Provider) []string {
	t.Helper()
	entries, err := skills.NewIndexer(p).Entries()
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	var out []string
	for _, e := range entries {
		for _, f := range e.Resources.Files {
			out = append(out, f.URI)
		}
	}
	return out
}

// TestProvider_SkipsHiddenByDefault checks every surface that enumerates a
// skill: resources/list, the skills/list manifest, discovery, and both
// reads. A .env next to a skill must not reach a client.
func TestProvider_SkipsHiddenByDefault(t *testing.T) {
	root := t.TempDir()
	writeHiddenTree(t, root)

	p, err := skills.NewProvider(skills.WithDirectory(root))
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	res := urisOf(p.Resources())
	if leaked := hasHidden(res); len(leaked) > 0 {
		t.Errorf("resources/list served hidden entries: %v", leaked)
	}
	if !slices.Contains(res, "skill://deploy/scripts/run.sh") {
		t.Errorf("resources/list lost a regular file: %v", res)
	}
	if slices.ContainsFunc(res, func(u string) bool { return strings.Contains(u, "ghost") }) {
		t.Errorf("discovery found a skill under a hidden directory: %v", res)
	}
	manifest := entryFileURIs(t, p)
	if leaked := hasHidden(manifest); len(leaked) > 0 {
		t.Errorf("skills/list resources pinned hidden entries: %v", leaked)
	}
	if len(manifest) != 2 {
		t.Errorf("skills/list resources = %v, want SKILL.md and scripts/run.sh", manifest)
	}

	c := bootWithOpts(t, skills.WithDirectory(root))
	if _, err := c.ReadResource(t.Context(), "skill://deploy/.env"); err == nil {
		t.Error("resources/read returned skill://deploy/.env")
	}
	listing := callDirectoryRead(t, c, "skill://deploy", "")
	for _, r := range listing.Resources {
		if strings.HasPrefix(r.Name, ".") {
			t.Errorf("resources/directory/read listed %q", r.URI)
		}
	}
	sub := callDirectoryRead(t, c, "skill://deploy/scripts", "")
	if names := namesOf(sub.Resources); !slices.Equal(names, []string{"run.sh"}) {
		t.Errorf("scripts listing = %v, want [run.sh]", names)
	}
	assertCallError(t, callDirectoryReadErr(c, "skill://deploy/.git", ""), "does not exist")
}

// TestProvider_WithHiddenFiles serves the same tree with hidden entries
// included, which is the opt-in for a skill that really ships a dotfile.
func TestProvider_WithHiddenFiles(t *testing.T) {
	root := t.TempDir()
	writeHiddenTree(t, root)

	p, err := skills.NewProvider(skills.WithDirectory(root), skills.WithHiddenFiles())
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	res := urisOf(p.Resources())
	for _, want := range []string{
		"skill://deploy/.env",
		"skill://deploy/.git/config",
		"skill://deploy/.notes/todo.md",
		"skill://deploy/scripts/.cache.tmp",
		"skill://.cache/ghost/SKILL.md",
	} {
		if !slices.Contains(res, want) {
			t.Errorf("resources/list missing %s with WithHiddenFiles: %v", want, res)
		}
	}
	if got := len(entryFileURIs(t, p)); got != 7 {
		t.Errorf("skills/list resources = %d files, want 7 with WithHiddenFiles", got)
	}

	c := bootWithOpts(t, skills.WithDirectory(root), skills.WithHiddenFiles())
	listing := callDirectoryRead(t, c, "skill://deploy/.git", "")
	if names := namesOf(listing.Resources); !slices.Equal(names, []string{"config"}) {
		t.Errorf(".git listing = %v, want [config]", names)
	}
}

// TestProvider_HiddenRootIsServed covers the common ~/.claude/skills
// layout. Only entries below the root are filtered.
func TestProvider_HiddenRootIsServed(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".claude", "skills")
	writeHiddenTree(t, root)

	p, err := skills.NewProvider(skills.WithDirectory(root))
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if res := urisOf(p.Resources()); !slices.Contains(res, "skill://deploy/SKILL.md") {
		t.Errorf("skill under a hidden root was not served: %v", res)
	}

	// The same through WithFS, where the hidden root is a path inside the FS.
	fsys, err := fs.Sub(os.DirFS(filepath.Dir(filepath.Dir(root))), ".claude/skills")
	if err != nil {
		t.Fatal(err)
	}
	p, err = skills.NewProvider(skills.WithFS(fsys))
	if err != nil {
		t.Fatalf("NewProvider(WithFS): %v", err)
	}
	if res := urisOf(p.Resources()); !slices.Contains(res, "skill://deploy/SKILL.md") {
		t.Errorf("skill under a hidden fs.Sub root was not served: %v", res)
	}
}

// TestPackSkill_SkipsHidden keeps a packed archive in line with what the
// directory walk serves, so a zip never ships the .git the walk hides.
func TestPackSkill_SkipsHidden(t *testing.T) {
	mfs := fstest.MapFS{
		"deploy/SKILL.md":    {Data: []byte(hiddenSkillManifest)},
		"deploy/run.sh":      {Data: []byte("echo\n")},
		"deploy/.env":        {Data: []byte("TOKEN=secret\n")},
		"deploy/.git/config": {Data: []byte("[core]\n")},
	}
	data, err := skills.PackSkill(mfs, "deploy", skills.ArchiveFormatZip)
	if err != nil {
		t.Fatalf("PackSkill: %v", err)
	}
	entries, err := skills.UnpackBytes(data, skills.ArchiveFormatZip, 1<<20)
	if err != nil {
		t.Fatalf("UnpackBytes: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Path)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"SKILL.md", "run.sh"}) {
		t.Errorf("archive entries = %v, want [SKILL.md run.sh]", names)
	}
}
