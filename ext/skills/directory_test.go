package skills_test

import (
	"strings"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/skills"
)

// TestDirectoryRead_Capability_OnByDefault confirms Provider.RegisterWith
// auto-advertises directoryRead: true on the wire because Provider can
// always enumerate from its underlying fs.FS at trivial cost. The
// asymmetric default (Provider opt-out, direct SkillsExtension opt-in)
// is intentional — see provider.go's RegisterWith doc.
func TestDirectoryRead_Capability_OnByDefault(t *testing.T) {
	_, _, c := boot(t, "testdata/valid")
	cap, ok := c.ServerExtensionCapability(skills.ExtensionID)
	if !ok {
		t.Fatalf("server did not advertise %q", skills.ExtensionID)
	}
	v, _ := cap.Config[skills.CapabilityDirectoryRead].(bool)
	if !v {
		t.Errorf("Config[%q] = %v, want true", skills.CapabilityDirectoryRead, cap.Config)
	}
}

// TestDirectoryRead_SkillRoot lists a skill's root directory and expects
// SKILL.md (file) plus any sibling subdirectories (each with
// MimeTypeDirectory). pdf-processing has SKILL.md, references/, scripts/.
func TestDirectoryRead_SkillRoot(t *testing.T) {
	_, _, c := boot(t, "testdata/valid")
	result := callDirectoryRead(t, c, "skill://pdf-processing", "")
	byName := indexByName(result.Resources)
	if _, ok := byName["SKILL.md"]; !ok {
		t.Errorf("missing SKILL.md in listing; got %v", namesOf(result.Resources))
	}
	for _, dir := range []string{"references", "scripts"} {
		entry, ok := byName[dir]
		if !ok {
			t.Errorf("missing subdir %q in listing", dir)
			continue
		}
		if entry.MimeType != skills.MimeTypeDirectory {
			t.Errorf("entry %q has mimeType %q, want %q", dir, entry.MimeType, skills.MimeTypeDirectory)
		}
	}
}

// TestDirectoryRead_SubdirectoryListing exercises a non-root subtree.
// `pdf-processing/references` has FORMS.md only.
func TestDirectoryRead_SubdirectoryListing(t *testing.T) {
	_, _, c := boot(t, "testdata/valid")
	result := callDirectoryRead(t, c, "skill://pdf-processing/references", "")
	if len(result.Resources) != 1 {
		t.Fatalf("got %d entries, want 1: %v", len(result.Resources), namesOf(result.Resources))
	}
	r := result.Resources[0]
	if r.Name != "FORMS.md" {
		t.Errorf("name = %q, want FORMS.md", r.Name)
	}
	if r.URI != "skill://pdf-processing/references/FORMS.md" {
		t.Errorf("uri = %q", r.URI)
	}
	if r.MimeType != "text/markdown" {
		t.Errorf("mimeType = %q, want text/markdown", r.MimeType)
	}
}

// TestDirectoryRead_ErrorOnFileURI rejects a URI that resolves to a file
// (e.g., SKILL.md) with -32602. Matches the SEP's mandate that the
// method applies only to directory resources.
func TestDirectoryRead_ErrorOnFileURI(t *testing.T) {
	_, _, c := boot(t, "testdata/valid")
	assertCallError(t, callDirectoryReadErr(c, "skill://pdf-processing/SKILL.md", ""), "is a file resource")
}

// TestDirectoryRead_ErrorOnUnknownSkill rejects URIs whose first segments
// do not match any skill the Provider serves.
func TestDirectoryRead_ErrorOnUnknownSkill(t *testing.T) {
	_, _, c := boot(t, "testdata/valid")
	assertCallError(t, callDirectoryReadErr(c, "skill://nonexistent/foo", ""), "outside every served skill subtree")
}

// TestDirectoryRead_ErrorOnUnknownPath rejects URIs whose owning skill is
// known but the subpath does not exist.
func TestDirectoryRead_ErrorOnUnknownPath(t *testing.T) {
	_, _, c := boot(t, "testdata/valid")
	assertCallError(t, callDirectoryReadErr(c, "skill://pdf-processing/no-such-dir", ""), "does not exist")
}

// TestDirectoryRead_EmptyURIError pins the required-field rule on params.
// An empty URI is Invalid params, not a server-side enumeration failure.
func TestDirectoryRead_EmptyURIError(t *testing.T) {
	_, _, c := boot(t, "testdata/valid")
	assertCallError(t, callDirectoryReadErr(c, "", ""), "uri is required")
}

// TestDirectoryRead_NestedDirectoryURI walks two levels via two calls,
// matching the SEP's "clients descend by issuing a follow-up call"
// contract.
func TestDirectoryRead_NestedDirectoryURI(t *testing.T) {
	_, _, c := boot(t, "testdata/valid")
	// First call: refunds/ root directory of the skill. Should contain
	// SKILL.md (file) + templates (directory).
	root := callDirectoryRead(t, c, "skill://acme/billing/refunds", "")
	byName := indexByName(root.Resources)
	if _, ok := byName["SKILL.md"]; !ok {
		t.Errorf("expected SKILL.md in refunds root listing, got %v", namesOf(root.Resources))
	}
	tplEntry, ok := byName["templates"]
	if !ok {
		t.Fatalf("expected templates subdir in refunds root listing, got %v", namesOf(root.Resources))
	}
	if tplEntry.MimeType != skills.MimeTypeDirectory {
		t.Errorf("templates mimeType = %q, want %q", tplEntry.MimeType, skills.MimeTypeDirectory)
	}

	// Second call: descend into templates/. Single file (email.md).
	sub := callDirectoryRead(t, c, tplEntry.URI, "")
	if len(sub.Resources) != 1 || sub.Resources[0].Name != "email.md" {
		t.Errorf("templates listing = %v, want [email.md]", namesOf(sub.Resources))
	}
}

// TestDirectoryRead_RejectsParentTraversal pins the security boundary:
// a URI whose tail is `..` must not silently resolve to the parent of
// the matched skill. Without the segment guard in resolveDirectoryURI,
// path.Join("acme/billing/refunds", "..") would cleanly collapse to
// "acme/billing" and enumerate the sibling tree — exactly the surface
// the directoryRead capability is meant to keep scoped.
func TestDirectoryRead_RejectsParentTraversal(t *testing.T) {
	_, _, c := boot(t, "testdata/valid")
	assertCallError(t, callDirectoryReadErr(c, "skill://acme/billing/refunds/..", ""), "invalid path segment")
}

// TestDirectoryRead_RejectsDotSegment rejects `.` segments for symmetry
// with `..` — both are spec-silent at the URI level but semantically
// ambiguous for a "directory inside the skill" lookup. Easier to reject
// than to define disposal semantics.
func TestDirectoryRead_RejectsDotSegment(t *testing.T) {
	_, _, c := boot(t, "testdata/valid")
	assertCallError(t, callDirectoryReadErr(c, "skill://acme/billing/refunds/.", ""), "invalid path segment")
}

// TestDirectoryRead_RejectsDeepTraversal asserts the guard fires even
// when the cleaned path would resolve far outside the skill tree (e.g.,
// /etc/passwd via successive `..` segments). The check is per-segment,
// so this is the same code path as the single-`..` case — included as
// a documented attack pattern.
func TestDirectoryRead_RejectsDeepTraversal(t *testing.T) {
	_, _, c := boot(t, "testdata/valid")
	assertCallError(t, callDirectoryReadErr(c, "skill://acme/billing/refunds/../../../etc/passwd", ""), "invalid path segment")
}

// TestDirectoryRead_StableOrdering verifies the result is URI-sorted so
// pagination cursors are deterministic across calls.
func TestDirectoryRead_StableOrdering(t *testing.T) {
	_, _, c := boot(t, "testdata/valid")
	result := callDirectoryRead(t, c, "skill://pdf-processing", "")
	for i := 1; i < len(result.Resources); i++ {
		if result.Resources[i-1].URI >= result.Resources[i].URI {
			t.Errorf("not URI-sorted at %d: %q >= %q",
				i, result.Resources[i-1].URI, result.Resources[i].URI)
		}
	}
}

// callDirectoryRead issues resources/directory/read against the connected
// server and t.Fatal's on transport-level error. Test bodies that assert
// the server's typed response use this form.
func callDirectoryRead(t *testing.T, c *client.Client, uri, cursor string) skills.DirectoryReadResult {
	t.Helper()
	res, err := c.Call(skills.MethodResourcesDirectoryRead, skills.DirectoryReadRequest{URI: uri, Cursor: cursor})
	if err != nil {
		t.Fatalf("Call(%s, uri=%q): %v", skills.MethodResourcesDirectoryRead, uri, err)
	}
	var out skills.DirectoryReadResult
	if err := res.Unmarshal(&out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	return out
}

// callDirectoryReadErr issues the same call but returns the transport
// error directly. Test bodies that assert the error message use this
// form so they can match strings without fighting t.Fatalf.
func callDirectoryReadErr(c *client.Client, uri, cursor string) error {
	_, err := c.Call(skills.MethodResourcesDirectoryRead, skills.DirectoryReadRequest{URI: uri, Cursor: cursor})
	return err
}

func indexByName(rs []core.ResourceDef) map[string]core.ResourceDef {
	out := make(map[string]core.ResourceDef, len(rs))
	for _, r := range rs {
		out[r.Name] = r
	}
	return out
}

func namesOf(rs []core.ResourceDef) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Name
	}
	return out
}

func assertCallError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err, want)
	}
}
