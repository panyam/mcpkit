package skills_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/skills"
	"github.com/panyam/mcpkit/server"
)

// connectSkillsClient boots a mcpkit server with a SkillProvider rooted
// at the named testdata directory, returns the wrapped client plus the
// underlying mcpkit client.
func connectSkillsClient(t *testing.T, dir string, opts ...skills.ProviderOption) (*skills.Client, *client.Client) {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "skills-client-test", Version: "0.0.1"})
	provOpts := append([]skills.ProviderOption{skills.WithDirectory(dir)}, opts...)
	p, err := skills.NewProvider(provOpts...)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	p.RegisterWith(srv)

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "skills-client-test", Version: "0.0.1"})
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return skills.NewClient(c), c
}

// connectSkillsClientWithClientOpts is the sibling of
// connectSkillsClient that wires SEP-414 P7 (#748) Client options
// (WithTracerProvider, WithActivationHook, ...) instead of provider
// options. Used by client_trace_test.go.
// connectSkillsClientFS is connectSkillsClient over an in-memory fs.FS, for
// cases (an empty catalog) that no testdata directory can express, since git
// cannot carry an empty directory.
func connectSkillsClientFS(t *testing.T, fsys fs.FS) (*skills.Client, *client.Client) {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "skills-client-test", Version: "0.0.1"})
	p, err := skills.NewProvider(skills.WithFS(fsys))
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	p.RegisterWith(srv)

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "skills-client-test", Version: "0.0.1"})
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return skills.NewClient(c), c
}

func connectSkillsClientWithClientOpts(t *testing.T, dir string, clientOpts ...skills.Option) (*skills.Client, *client.Client) {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "skills-client-test", Version: "0.0.1"})
	p, err := skills.NewProvider(skills.WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	p.RegisterWith(srv)

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "skills-client-test", Version: "0.0.1"})
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return skills.NewClient(c, clientOpts...), c
}

func TestClient_SupportsSkills_DeclaredServer(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")
	if !sc.SupportsSkills() {
		t.Errorf("SupportsSkills = false, want true for a Provider-backed server")
	}
}

func TestClient_SupportsSkills_PlainServer(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "plain-server", Version: "0.0.1"})
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "plain-client", Version: "0.0.1"})
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	sc := skills.NewClient(c)
	if sc.SupportsSkills() {
		t.Errorf("SupportsSkills = true, want false for a server without the extension")
	}
}

func TestClient_ListSkillEntries_Populated(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")
	entries, err := sc.ListSkillEntries(context.Background())
	if err != nil {
		t.Fatalf("ListSkillEntries: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("entry count = %d, want 3", len(entries))
	}
}

// An empty listing is conformant: SEP-2640 lets a server return one, and
// hosts MUST NOT read it as proof the server has no skills.
func TestClient_ListSkillEntries_Empty(t *testing.T) {
	sc, _ := connectSkillsClientFS(t, fstest.MapFS{})
	entries, err := sc.ListSkillEntries(context.Background())
	if err != nil {
		t.Fatalf("an empty listing is conformant and must not error, got: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected an empty listing, got %d entries", len(entries))
	}
}

func TestClient_GetSkill_Hit(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")
	entry, err := sc.GetSkill(context.Background(), "skill://git-workflow/SKILL.md")
	if err != nil {
		t.Fatalf("GetSkill: %v", err)
	}
	if entry.Name() != "git-workflow" {
		t.Errorf("entry name = %q, want git-workflow", entry.Name())
	}
}

func TestClient_GetSkill_Miss(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")
	if _, err := sc.GetSkill(context.Background(), "skill://nonexistent/SKILL.md"); err == nil {
		t.Error("GetSkill on an unserved URI succeeded, want -32602")
	}
}

func TestClient_ReadSkillURI(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")
	body, err := sc.ReadSkillURI(context.Background(), "skill://git-workflow/SKILL.md")
	if err != nil {
		t.Fatalf("ReadSkillURI: %v", err)
	}
	if !strings.Contains(string(body), "name: git-workflow") {
		t.Errorf("body missing frontmatter name field; got: %s", body)
	}
}

func TestClient_ReadSkillManifest(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")
	m, err := sc.ReadSkillManifest(context.Background(), "skill://pdf-processing/SKILL.md")
	if err != nil {
		t.Fatalf("ReadSkillManifest: %v", err)
	}
	if m.Frontmatter.Name != "pdf-processing" {
		t.Errorf("Frontmatter.Name = %q", m.Frontmatter.Name)
	}
	if len(m.Body) == 0 {
		t.Errorf("Body is empty")
	}
	if len(m.Raw) == 0 {
		t.Errorf("Raw is empty")
	}
	if !strings.HasPrefix(string(m.Raw), "---") {
		t.Errorf("Raw should start with frontmatter fence: %s", m.Raw[:20])
	}
}

func TestClient_ReadSkillManifest_RejectsNonManifestURI(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")
	_, err := sc.ReadSkillManifest(context.Background(), "skill://pdf-processing/references/FORMS.md")
	if !errors.Is(err, skills.ErrNotManifestURI) {
		t.Errorf("err = %v, want ErrNotManifestURI", err)
	}
}

func TestClient_ReadSkillFile(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")
	m, err := sc.ReadSkillManifest(context.Background(), "skill://pdf-processing/SKILL.md")
	if err != nil {
		t.Fatalf("ReadSkillManifest: %v", err)
	}
	body, err := sc.ReadSkillFile(context.Background(), m, "references/FORMS.md")
	if err != nil {
		t.Fatalf("ReadSkillFile: %v", err)
	}
	if !strings.Contains(string(body), "AcroForm") {
		t.Errorf("expected FORMS.md content, got: %s", body)
	}
}

func TestClient_ReadAndVerify_Match(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")
	entries, err := sc.ListSkillEntries(context.Background())
	if err != nil {
		t.Fatalf("ListSkillEntries: %v", err)
	}
	entry := entryFor(t, entries, "git-workflow")
	self := entry.Resources.Files[0]
	result, err := sc.ReadAndVerify(context.Background(), self.URI, self.Digest)
	if err != nil {
		t.Fatalf("ReadAndVerify match: %v", err)
	}
	if !result.DigestVerified {
		t.Errorf("DigestVerified = false on a known-good digest")
	}
	if len(result.Bytes) == 0 {
		t.Errorf("Bytes empty")
	}
}

func TestClient_ReadAndVerify_Mismatch(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")
	_, err := sc.ReadAndVerify(
		context.Background(),
		"skill://git-workflow/SKILL.md",
		"sha256:"+strings.Repeat("0", 64), // deliberately wrong
	)
	if !errors.Is(err, skills.ErrDigestMismatch) {
		t.Errorf("err = %v, want ErrDigestMismatch", err)
	}
}

func TestClient_ReadAndVerify_EmptyDigestDisables(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")
	result, err := sc.ReadAndVerify(context.Background(), "skill://git-workflow/SKILL.md", "")
	if err != nil {
		t.Fatalf("ReadAndVerify empty digest: %v", err)
	}
	if result.DigestVerified {
		t.Errorf("DigestVerified = true with empty expectedDigest, want false")
	}
	if len(result.Bytes) == 0 {
		t.Errorf("Bytes empty")
	}
}

func TestClient_RoundTrip_CatalogVerify(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")
	entries, err := sc.ListSkillEntries(context.Background())
	if err != nil {
		t.Fatalf("ListSkillEntries: %v", err)
	}
	verified := 0
	for _, e := range entries {
		result, err := sc.ReadFromEntry(context.Background(), e, e.URI)
		if err != nil {
			t.Errorf("ReadFromEntry %s: %v", e.URI, err)
			continue
		}
		if !result.DigestVerified {
			t.Errorf("%s: DigestVerified = false", e.URI)
			continue
		}
		// Recompute locally as a belt-and-braces check against the entry's
		// own pin for its SKILL.md.
		var pin string
		for _, f := range e.Resources.Files {
			if f.URI == e.URI {
				pin = f.Digest
			}
		}
		sum := sha256.Sum256(result.Bytes)
		got := "sha256:" + hex.EncodeToString(sum[:])
		if got != pin {
			t.Errorf("%s: local digest %s != manifest pin %s", e.URI, got, pin)
		}
		verified++
	}
	if verified == 0 {
		t.Fatal("no entries verified end-to-end")
	}
}

func TestReadResourceTool_Schema(t *testing.T) {
	if skills.ReadResourceToolName != "read_resource" {
		t.Errorf("ToolName = %q, want read_resource", skills.ReadResourceToolName)
	}
	if !strings.Contains(skills.ReadResourceToolDescription, "MCP resource") {
		t.Errorf("Description should mention 'MCP resource': %q", skills.ReadResourceToolDescription)
	}
	schema := skills.ReadResourceToolInputSchema
	if schema["type"] != "object" {
		t.Errorf("schema.type = %v, want object", schema["type"])
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema.properties not a map: %T", schema["properties"])
	}
	for _, key := range []string{"server", "uri"} {
		field, ok := props[key].(map[string]any)
		if !ok {
			t.Errorf("schema.properties[%q] missing or wrong type: %T", key, props[key])
			continue
		}
		if field["type"] != "string" {
			t.Errorf("schema.properties[%q].type = %v, want string", key, field["type"])
		}
	}
	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatalf("schema.required not []string: %T", schema["required"])
	}
	if len(required) != 2 || required[0] != "server" || required[1] != "uri" {
		t.Errorf("schema.required = %v, want [server uri]", required)
	}
}
