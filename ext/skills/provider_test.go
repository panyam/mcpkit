package skills_test

import (
	"errors"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/skills"
	"github.com/panyam/mcpkit/server"
)

func TestProvider_HappyPath(t *testing.T) {
	p, err := skills.NewProvider(skills.WithDirectory("testdata/valid"))
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	defs := p.Resources()
	got := make([]string, len(defs))
	for i, d := range defs {
		got[i] = d.URI
	}
	want := []string{
		"skill://acme/billing/refunds/SKILL.md",
		"skill://acme/billing/refunds/templates/email.md",
		"skill://git-workflow/SKILL.md",
		"skill://pdf-processing/SKILL.md",
		"skill://pdf-processing/references/FORMS.md",
		"skill://pdf-processing/scripts/extract.py",
	}
	if !equalSlices(got, want) {
		t.Errorf("URIs (sorted) =\n  got:  %v\n  want: %v", got, want)
	}
}

func TestProvider_ManifestMetadata(t *testing.T) {
	p, err := skills.NewProvider(skills.WithDirectory("testdata/valid"))
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	defs := p.Resources()
	var refundsManifest *core.ResourceDef
	for i, d := range defs {
		if d.URI == "skill://acme/billing/refunds/SKILL.md" {
			refundsManifest = &defs[i]
			break
		}
	}
	if refundsManifest == nil {
		t.Fatal("refunds SKILL.md not in resources")
	}
	if refundsManifest.Name != "refunds" {
		t.Errorf("Name = %q, want %q (from frontmatter)", refundsManifest.Name, "refunds")
	}
	if !strings.Contains(refundsManifest.Description, "refund") {
		t.Errorf("Description = %q, want frontmatter description", refundsManifest.Description)
	}
	if refundsManifest.MimeType != "text/markdown" {
		t.Errorf("MimeType = %q, want text/markdown", refundsManifest.MimeType)
	}

	// pdf-processing has Extra frontmatter (version, tags) that should
	// surface under the configured meta prefix.
	var pdfManifest *core.ResourceDef
	for i, d := range defs {
		if d.URI == "skill://pdf-processing/SKILL.md" {
			pdfManifest = &defs[i]
			break
		}
	}
	if pdfManifest == nil {
		t.Fatal("pdf-processing SKILL.md not in resources")
	}
	if pdfManifest.Annotations == nil {
		t.Fatal("Annotations nil; want meta-prefixed extras")
	}
	versionKey := skills.MetaPrefix + "version"
	if v, ok := pdfManifest.Annotations[versionKey]; !ok || v != "0.2.0" {
		t.Errorf("Annotations[%q] = %v, want 0.2.0", versionKey, v)
	}
	tagsKey := skills.MetaPrefix + "tags"
	if _, ok := pdfManifest.Annotations[tagsKey]; !ok {
		t.Errorf("Annotations missing %q", tagsKey)
	}
}

func TestProvider_NonManifestResourceMetadata(t *testing.T) {
	p, err := skills.NewProvider(skills.WithDirectory("testdata/valid"))
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	for _, d := range p.Resources() {
		if d.URI != "skill://pdf-processing/scripts/extract.py" {
			continue
		}
		if d.Name != "extract.py" {
			t.Errorf("Name = %q, want extract.py", d.Name)
		}
		if d.MimeType != "text/x-python" {
			t.Errorf("MimeType = %q, want text/x-python", d.MimeType)
		}
		if d.Description != "" {
			t.Errorf("Description = %q, want empty for non-manifest files", d.Description)
		}
		return
	}
	t.Fatal("extract.py not found")
}

func TestProvider_URIPrefix(t *testing.T) {
	p, err := skills.NewProvider(
		skills.WithDirectory("testdata/valid"),
		skills.WithURIPrefix("org/example"),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defs := p.Resources()
	for _, d := range defs {
		if !strings.HasPrefix(d.URI, "skill://org/example/") {
			t.Errorf("URI %q missing prefix", d.URI)
		}
	}
	// Spot-check one specific URI.
	var found bool
	for _, d := range defs {
		if d.URI == "skill://org/example/acme/billing/refunds/SKILL.md" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("prefixed URI not found in resource set: %v", urisOf(defs))
	}
}

func TestProvider_Catalog(t *testing.T) {
	p, err := skills.NewProvider(skills.WithDirectory("testdata/valid"))
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	cat := p.Catalog()
	if len(cat) != 3 {
		t.Fatalf("catalog len = %d, want 3", len(cat))
	}
	for _, e := range cat {
		if e.Type != skills.SkillTypeSkillMD {
			t.Errorf("entry %q type = %q, want skill-md", e.Name, e.Type)
		}
		if e.Name == "" || e.Description == "" || e.URL == "" {
			t.Errorf("entry has empty required field: %+v", e)
		}
		if e.Digest != "" {
			t.Errorf("Digest should be empty pre-560: %q", e.Digest)
		}
	}
	// Stable URL-sorted order.
	urls := make([]string, len(cat))
	for i, e := range cat {
		urls[i] = e.URL
	}
	sorted := append([]string(nil), urls...)
	sort.Strings(sorted)
	if !equalSlices(urls, sorted) {
		t.Errorf("catalog not URL-sorted: %v", urls)
	}
}

func TestProvider_AcceptsNonStrictPrefixSiblings(t *testing.T) {
	// Regression: the no-nesting check (strings.HasPrefix(cur, prev+"/"))
	// must not flag two sibling skill directories whose names share a
	// textual prefix without a separating slash. "foo" and "foo-bar" are
	// independent skills, not parent and child.
	p, err := skills.NewProvider(skills.WithDirectory("testdata/valid-prefix-pair"))
	if err != nil {
		t.Fatalf("NewProvider on prefix-pair siblings: %v (must NOT trip ErrNestedSkill)", err)
	}

	got := urisOf(p.Resources())
	want := []string{
		"skill://foo-bar/SKILL.md",
		"skill://foo/SKILL.md",
	}
	if !equalSlices(got, want) {
		t.Errorf("URIs = %v, want %v", got, want)
	}

	cat := p.Catalog()
	if len(cat) != 2 {
		t.Errorf("catalog len = %d, want 2 (foo + foo-bar)", len(cat))
	}
}

func TestProvider_RejectsNameMismatch(t *testing.T) {
	_, err := skills.NewProvider(skills.WithDirectory("testdata/bad-name-mismatch"))
	if !errors.Is(err, skills.ErrSkillNameMismatch) {
		t.Errorf("err = %v, want ErrSkillNameMismatch", err)
	}
}

func TestProvider_RejectsNestedSkill(t *testing.T) {
	_, err := skills.NewProvider(skills.WithDirectory("testdata/bad-nested"))
	if !errors.Is(err, skills.ErrNestedSkill) {
		t.Errorf("err = %v, want ErrNestedSkill", err)
	}
}

func TestProvider_RejectsMissingFS(t *testing.T) {
	_, err := skills.NewProvider()
	if !errors.Is(err, skills.ErrProviderMissingFS) {
		t.Errorf("err = %v, want ErrProviderMissingFS", err)
	}
}

func TestProvider_EmptyFS(t *testing.T) {
	p, err := skills.NewProvider(skills.WithFS(fstest.MapFS{}))
	if err != nil {
		t.Fatalf("NewProvider on empty FS: %v", err)
	}
	if len(p.Resources()) != 0 {
		t.Errorf("empty FS produced resources: %v", urisOf(p.Resources()))
	}
	if len(p.Catalog()) != 0 {
		t.Errorf("empty FS produced catalog: %+v", p.Catalog())
	}
}

func TestProvider_MapFS(t *testing.T) {
	// fstest.MapFS is the canonical alternative-FS test target. The
	// provider should treat it identically to os.DirFS.
	mfs := fstest.MapFS{
		"hello/SKILL.md": &fstest.MapFile{Data: []byte(`---
name: hello
description: A skill from an in-memory filesystem
---

body
`)},
	}
	p, err := skills.NewProvider(skills.WithFS(mfs))
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	got := urisOf(p.Resources())
	want := []string{"skill://hello/SKILL.md"}
	if !equalSlices(got, want) {
		t.Errorf("URIs = %v, want %v", got, want)
	}
}

func TestProvider_RejectsRootSKILLmd(t *testing.T) {
	mfs := fstest.MapFS{
		"SKILL.md": &fstest.MapFile{Data: []byte(`---
name: rooted
description: SKILL.md at FS root is invalid
---
`)},
	}
	_, err := skills.NewProvider(skills.WithFS(mfs))
	if !errors.Is(err, skills.ErrManifestNotInRoot) {
		t.Errorf("err = %v, want ErrManifestNotInRoot", err)
	}
}

func TestProvider_Integration(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "skills-test", Version: "0.0.1"})

	p, err := skills.NewProvider(skills.WithDirectory("testdata/valid"))
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	p.RegisterWith(srv)

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "skills-test-client", Version: "0.0.1"})
	if err := c.Connect(); err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	// resources/list returns every cataloged URI.
	defs, err := c.ListResources(t.Context())
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	got := urisOf(defs)
	want := []string{
		"skill://acme/billing/refunds/SKILL.md",
		"skill://acme/billing/refunds/templates/email.md",
		"skill://git-workflow/SKILL.md",
		"skill://index.json",
		"skill://pdf-processing/SKILL.md",
		"skill://pdf-processing/references/FORMS.md",
		"skill://pdf-processing/scripts/extract.py",
	}
	sort.Strings(got)
	if !equalSlices(got, want) {
		t.Errorf("URIs = %v, want %v", got, want)
	}

	// resources/read on the git-workflow manifest returns the file contents.
	body, err := c.ReadResource("skill://git-workflow/SKILL.md")
	if err != nil {
		t.Fatalf("ReadResource manifest: %v", err)
	}
	wantBytes, err := os.ReadFile("testdata/valid/git-workflow/SKILL.md")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	if body != string(wantBytes) {
		t.Errorf("manifest body mismatch:\n  got:  %q\n  want: %q", body, string(wantBytes))
	}

	// resources/read on a supporting file works the same way.
	body, err = c.ReadResource("skill://pdf-processing/references/FORMS.md")
	if err != nil {
		t.Fatalf("ReadResource supporting file: %v", err)
	}
	if !strings.Contains(body, "AcroForm") {
		t.Errorf("supporting file body missing expected content: %q", body)
	}
}

func urisOf(defs []core.ResourceDef) []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.URI
	}
	return out
}
