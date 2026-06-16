package skills_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/skills"
	"github.com/panyam/mcpkit/server"
)

// TestClient_ReadDirectory_HappyPath drives the ext/skills.Client wrapper
// against a real Provider-backed server. Confirms the SDK shape matches
// the wire shape: the result returned by Client.ReadDirectory is the
// typed view of what the server emitted.
func TestClient_ReadDirectory_HappyPath(t *testing.T) {
	_, _, c := boot(t, "testdata/valid")
	sc := skills.NewClient(c)
	if !sc.SupportsDirectoryRead() {
		t.Fatalf("server should advertise directoryRead capability")
	}
	result, err := sc.ReadDirectory(context.Background(), "skill://pdf-processing/references")
	if err != nil {
		t.Fatalf("ReadDirectory: %v", err)
	}
	if len(result.Resources) != 1 || result.Resources[0].Name != "FORMS.md" {
		t.Errorf("got %v, want [FORMS.md]", namesOf(result.Resources))
	}
	if result.NextCursor != "" {
		t.Errorf("NextCursor = %q, want empty", result.NextCursor)
	}
}

// TestClient_ReadDirectory_PreCallGuard verifies the SDK enforces the
// SEP's normative "Clients MUST NOT call ..." wording — when the server
// has not advertised directoryRead, ReadDirectory returns the typed
// sentinel without issuing a network call.
func TestClient_ReadDirectory_PreCallGuard(t *testing.T) {
	// Boot a server with directoryRead deliberately suppressed.
	srv := newServerWithoutDirectoryRead(t)
	c := newConnectedClient(t, srv)

	sc := skills.NewClient(c)
	if sc.SupportsDirectoryRead() {
		t.Fatalf("server should NOT advertise directoryRead in this setup")
	}
	_, err := sc.ReadDirectory(context.Background(), "skill://pdf-processing")
	if !errors.Is(err, skills.ErrDirectoryReadNotSupported) {
		t.Errorf("err = %v, want ErrDirectoryReadNotSupported", err)
	}
}

// TestClient_ReadDirectory_RecurseManually walks a two-level subtree by
// hand-rolling recursion at the call site, matching the walkthrough's
// usage pattern in examples/skills/walkthrough.go.
func TestClient_ReadDirectory_RecurseManually(t *testing.T) {
	_, _, c := boot(t, "testdata/valid")
	sc := skills.NewClient(c)
	root, err := sc.ReadDirectory(context.Background(), "skill://pdf-processing")
	if err != nil {
		t.Fatalf("ReadDirectory(root): %v", err)
	}
	seen := map[string]bool{}
	for _, r := range root.Resources {
		seen[r.Name] = true
		if r.MimeType != skills.MimeTypeDirectory {
			continue
		}
		sub, err := sc.ReadDirectory(context.Background(), r.URI)
		if err != nil {
			t.Errorf("ReadDirectory(%s): %v", r.URI, err)
			continue
		}
		for _, e := range sub.Resources {
			seen[e.Name] = true
		}
	}
	// pdf-processing skill has SKILL.md, references/FORMS.md,
	// scripts/extract.py — one level of descent should reveal all three
	// child files plus the two subdir markers.
	for _, want := range []string{"SKILL.md", "FORMS.md", "extract.py", "references", "scripts"} {
		if !seen[want] {
			t.Errorf("hand-rolled recursion missed %q", want)
		}
	}
}

// newServerWithoutDirectoryRead builds a Provider-backed server that
// explicitly suppresses directoryRead — exercises the WithoutDirectoryRead
// opt-out and gives the pre-call-guard test a server that legitimately
// lacks the capability.
func newServerWithoutDirectoryRead(t *testing.T) *server.Server {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "skills-no-dirread", Version: "0.0.1"})
	p, err := skills.NewProvider(
		skills.WithDirectory("testdata/valid"),
		skills.WithoutDirectoryRead(),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	p.RegisterWith(srv)
	return srv
}

// newConnectedClient spins up an httptest server fronted by srv's
// Handler, connects a client, and registers cleanup. Shares the wiring
// pattern with boot() in indexer_test.go but takes a caller-supplied
// server so tests can configure Provider options.
func newConnectedClient(t *testing.T, srv *server.Server) *client.Client {
	t.Helper()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "dirread-probe", Version: "0.0.1"})
	if err := c.Connect(); err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
