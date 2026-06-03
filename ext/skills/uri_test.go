package skills_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/ext/skills"
)

func TestParseURI_SEPExamples(t *testing.T) {
	// Cases drawn from SEP-2640's Examples table (Resource Mapping).
	cases := []struct {
		uri        string
		skillPath  []string
		filePath   []string
		skillName  string
		isManifest bool
	}{
		{
			uri:        "skill://git-workflow/SKILL.md",
			skillPath:  []string{"git-workflow"},
			filePath:   []string{"SKILL.md"},
			skillName:  "git-workflow",
			isManifest: true,
		},
		{
			uri:        "skill://acme/billing/refunds/SKILL.md",
			skillPath:  []string{"acme", "billing", "refunds"},
			filePath:   []string{"SKILL.md"},
			skillName:  "refunds",
			isManifest: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.uri, func(t *testing.T) {
			parts, err := skills.ParseURI(tc.uri)
			if err != nil {
				t.Fatalf("ParseURI(%q) error: %v", tc.uri, err)
			}
			if !equalSlices(parts.SkillPath, tc.skillPath) {
				t.Errorf("SkillPath = %v, want %v", parts.SkillPath, tc.skillPath)
			}
			if !equalSlices(parts.FilePath, tc.filePath) {
				t.Errorf("FilePath = %v, want %v", parts.FilePath, tc.filePath)
			}
			if parts.SkillName != tc.skillName {
				t.Errorf("SkillName = %q, want %q", parts.SkillName, tc.skillName)
			}
			if parts.IsManifest != tc.isManifest {
				t.Errorf("IsManifest = %v, want %v", parts.IsManifest, tc.isManifest)
			}
			if parts.Scheme != "skill" {
				t.Errorf("Scheme = %q, want %q", parts.Scheme, "skill")
			}
		})
	}
}

func TestParseURI_NonManifest(t *testing.T) {
	// Non-manifest URIs from the SEP's Examples table. The skill/file
	// boundary is not recoverable from the URI alone, so SkillPath and
	// FilePath are left empty. AllSegments carries the parsed segments.
	cases := []struct {
		uri      string
		segments []string
	}{
		{
			uri:      "skill://pdf-processing/references/FORMS.md",
			segments: []string{"pdf-processing", "references", "FORMS.md"},
		},
		{
			uri:      "skill://pdf-processing/scripts/extract.py",
			segments: []string{"pdf-processing", "scripts", "extract.py"},
		},
		{
			uri:      "skill://acme/billing/refunds/templates/email.md",
			segments: []string{"acme", "billing", "refunds", "templates", "email.md"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.uri, func(t *testing.T) {
			parts, err := skills.ParseURI(tc.uri)
			if err != nil {
				t.Fatalf("ParseURI(%q) error: %v", tc.uri, err)
			}
			if !equalSlices(parts.AllSegments, tc.segments) {
				t.Errorf("AllSegments = %v, want %v", parts.AllSegments, tc.segments)
			}
			if parts.IsManifest {
				t.Errorf("IsManifest = true, want false for non-manifest URI")
			}
			if len(parts.SkillPath) != 0 || len(parts.FilePath) != 0 {
				t.Errorf("expected unsplit SkillPath/FilePath, got SkillPath=%v FilePath=%v", parts.SkillPath, parts.FilePath)
			}
		})
	}
}

func TestParseURI_SplitAt(t *testing.T) {
	parts, err := skills.ParseURI("skill://acme/billing/refunds/templates/email.md")
	if err != nil {
		t.Fatalf("ParseURI: %v", err)
	}
	split, err := parts.SplitAt(3) // acme/billing/refunds is the skill path
	if err != nil {
		t.Fatalf("SplitAt(3): %v", err)
	}
	if !equalSlices(split.SkillPath, []string{"acme", "billing", "refunds"}) {
		t.Errorf("SkillPath after SplitAt = %v", split.SkillPath)
	}
	if !equalSlices(split.FilePath, []string{"templates", "email.md"}) {
		t.Errorf("FilePath after SplitAt = %v", split.FilePath)
	}
	if split.SkillName != "refunds" {
		t.Errorf("SkillName after SplitAt = %q", split.SkillName)
	}
	if split.IsManifest {
		t.Errorf("IsManifest = true, want false")
	}
}

func TestParseURI_SplitAt_Manifest(t *testing.T) {
	parts, err := skills.ParseURI("skill://acme/billing/refunds/SKILL.md")
	if err != nil {
		t.Fatalf("ParseURI: %v", err)
	}
	// Already determined by ParseURI; SplitAt with the same boundary
	// should reproduce the same shape.
	split, err := parts.SplitAt(3)
	if err != nil {
		t.Fatalf("SplitAt(3): %v", err)
	}
	if !split.IsManifest {
		t.Errorf("IsManifest = false, want true")
	}
}

func TestParseURI_Errors(t *testing.T) {
	cases := []struct {
		name string
		uri  string
		want error
	}{
		{"empty", "", skills.ErrInvalidScheme},
		{"wrong scheme", "https://example.com/skill", skills.ErrInvalidScheme},
		{"http scheme", "http://example.com/x", skills.ErrInvalidScheme},
		{"no path", "skill://", skills.ErrEmptySkillPath},
		{"consecutive slashes", "skill://foo//SKILL.md", skills.ErrEmptyPathSegment},
		{"nested SKILL.md", "skill://outer/SKILL.md/inner/something.md", skills.ErrManifestNotInRoot},
		{"middle SKILL.md", "skill://outer/inner/SKILL.md/x", skills.ErrManifestNotInRoot},
		{"uppercase skill name", "skill://Git-Workflow/SKILL.md", skills.ErrInvalidSkillName},
		{"trailing hyphen", "skill://refunds-/SKILL.md", skills.ErrInvalidSkillName},
		{"leading hyphen", "skill://-refunds/SKILL.md", skills.ErrInvalidSkillName},
		{"double hyphen", "skill://re--funds/SKILL.md", skills.ErrInvalidSkillName},
		{"underscore", "skill://my_skill/SKILL.md", skills.ErrInvalidSkillName},
		{"only manifest", "skill://SKILL.md", skills.ErrEmptySkillPath},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := skills.ParseURI(tc.uri)
			if !errors.Is(err, tc.want) {
				t.Errorf("ParseURI(%q) err = %v, want %v", tc.uri, err, tc.want)
			}
		})
	}
}

func TestParseURI_TrailingSlash(t *testing.T) {
	// A trailing slash after the skill path is tolerated; it does not
	// produce an empty trailing segment.
	parts, err := skills.ParseURI("skill://git-workflow/")
	if err != nil {
		t.Fatalf("ParseURI: %v", err)
	}
	if len(parts.AllSegments) != 1 || parts.AllSegments[0] != "git-workflow" {
		t.Errorf("AllSegments = %v", parts.AllSegments)
	}
	if parts.IsManifest {
		t.Errorf("IsManifest = true, want false for non-manifest URI")
	}
}

func TestParseURI_PercentEncoded(t *testing.T) {
	// Path-component segments may carry percent-encoded characters per
	// RFC 3986; the parser MUST decode them before exposing them in
	// AllSegments. (Authority-component percent encoding is more
	// restricted and is not exercised here.)
	parts, err := skills.ParseURI("skill://my-skill/folder%20a/file.md")
	if err != nil {
		t.Fatalf("ParseURI: %v", err)
	}
	want := []string{"my-skill", "folder a", "file.md"}
	if !equalSlices(parts.AllSegments, want) {
		t.Errorf("AllSegments = %v, want %v", parts.AllSegments, want)
	}
}

func TestResolveRelative(t *testing.T) {
	root, err := skills.ParseURI("skill://acme/billing/refunds/SKILL.md")
	if err != nil {
		t.Fatalf("ParseURI: %v", err)
	}
	cases := []struct {
		rel  string
		want string
	}{
		// SEP-2640 reference: references/GUIDE.md within
		// skill://acme/billing/refunds resolves to
		// skill://acme/billing/refunds/references/GUIDE.md.
		{"references/GUIDE.md", "skill://acme/billing/refunds/references/GUIDE.md"},
		{"templates/email.md", "skill://acme/billing/refunds/templates/email.md"},
		{"scripts/extract.py", "skill://acme/billing/refunds/scripts/extract.py"},
		// path.Clean handles . and ..
		{"./references/GUIDE.md", "skill://acme/billing/refunds/references/GUIDE.md"},
		{"references/../references/GUIDE.md", "skill://acme/billing/refunds/references/GUIDE.md"},
	}
	for _, tc := range cases {
		t.Run(tc.rel, func(t *testing.T) {
			got, err := skills.ResolveRelative(root, tc.rel)
			if err != nil {
				t.Fatalf("ResolveRelative(%q): %v", tc.rel, err)
			}
			if got.String() != tc.want {
				t.Errorf("ResolveRelative(%q) = %q, want %q", tc.rel, got.String(), tc.want)
			}
		})
	}
}

func TestResolveRelative_Escape(t *testing.T) {
	root, err := skills.ParseURI("skill://acme/billing/refunds/SKILL.md")
	if err != nil {
		t.Fatalf("ParseURI: %v", err)
	}
	cases := []string{
		"../GUIDE.md",
		"../../GUIDE.md",
		"../../../etc/passwd",
		"references/../../GUIDE.md",
	}
	for _, rel := range cases {
		t.Run(rel, func(t *testing.T) {
			_, err := skills.ResolveRelative(root, rel)
			if !errors.Is(err, skills.ErrRelativeEscapesSkill) {
				t.Errorf("ResolveRelative(%q) err = %v, want ErrRelativeEscapesSkill", rel, err)
			}
		})
	}
}

func TestResolveRelative_Absolute(t *testing.T) {
	root, err := skills.ParseURI("skill://git-workflow/SKILL.md")
	if err != nil {
		t.Fatalf("ParseURI: %v", err)
	}
	_, err = skills.ResolveRelative(root, "/etc/passwd")
	if !errors.Is(err, skills.ErrRelativeEscapesSkill) {
		t.Errorf("ResolveRelative(absolute) err = %v, want ErrRelativeEscapesSkill", err)
	}
}

func TestResolveRelative_IdempotentManifest(t *testing.T) {
	// Resolving "SKILL.md" from the manifest URI itself is a no-op that
	// lands back on the same SKILL.md. Allowed by SEP-2640 (no nesting
	// happens) and exercised here so the deeper-SKILL.md check does not
	// over-reject.
	root, err := skills.ParseURI("skill://acme/billing/refunds/SKILL.md")
	if err != nil {
		t.Fatalf("ParseURI: %v", err)
	}
	got, err := skills.ResolveRelative(root, "SKILL.md")
	if err != nil {
		t.Fatalf("ResolveRelative(SKILL.md): %v", err)
	}
	if !got.IsManifest {
		t.Errorf("IsManifest = false, want true")
	}
	if got.String() != "skill://acme/billing/refunds/SKILL.md" {
		t.Errorf("String() = %q", got.String())
	}
}

func TestResolveRelative_RejectsResolveToRoot(t *testing.T) {
	// "." and "./" resolve back to the skill root directory with no file
	// referenced. The caller almost certainly has a bug; reject rather
	// than silently return an empty FilePath.
	root, err := skills.ParseURI("skill://git-workflow/SKILL.md")
	if err != nil {
		t.Fatalf("ParseURI: %v", err)
	}
	for _, rel := range []string{".", "./", "references/.."} {
		t.Run(rel, func(t *testing.T) {
			_, err := skills.ResolveRelative(root, rel)
			if !errors.Is(err, skills.ErrEmptyPathSegment) {
				t.Errorf("ResolveRelative(%q) err = %v, want ErrEmptyPathSegment", rel, err)
			}
		})
	}
}

func TestResolveRelative_RejectsCrossOrigin(t *testing.T) {
	// A reference that carries its own scheme or authority would
	// short-circuit RFC 3986 reference resolution to a different origin.
	root, err := skills.ParseURI("skill://git-workflow/SKILL.md")
	if err != nil {
		t.Fatalf("ParseURI: %v", err)
	}
	for _, rel := range []string{
		"https://evil.example.com/payload",
		"skill://other-skill/SKILL.md",
		"//other/path",
	} {
		t.Run(rel, func(t *testing.T) {
			_, err := skills.ResolveRelative(root, rel)
			if !errors.Is(err, skills.ErrRelativeEscapesSkill) {
				t.Errorf("ResolveRelative(%q) err = %v, want ErrRelativeEscapesSkill", rel, err)
			}
		})
	}
}

func TestResolveRelative_RejectsNestedManifest(t *testing.T) {
	root, err := skills.ParseURI("skill://git-workflow/SKILL.md")
	if err != nil {
		t.Fatalf("ParseURI: %v", err)
	}
	_, err = skills.ResolveRelative(root, "references/SKILL.md")
	if !errors.Is(err, skills.ErrManifestNotInRoot) {
		t.Errorf("ResolveRelative(refs/SKILL.md) err = %v, want ErrManifestNotInRoot", err)
	}
}

func TestResolveRelative_RequiresKnownSkillRoot(t *testing.T) {
	// A URIParts produced from a non-manifest URI has no SkillPath set.
	parts, err := skills.ParseURI("skill://pdf-processing/references/FORMS.md")
	if err != nil {
		t.Fatalf("ParseURI: %v", err)
	}
	_, err = skills.ResolveRelative(parts, "scripts/extract.py")
	if !errors.Is(err, skills.ErrNotManifestURI) {
		t.Errorf("ResolveRelative without skill root err = %v, want ErrNotManifestURI", err)
	}
}

func TestURIParts_String(t *testing.T) {
	parts, err := skills.ParseURI("skill://acme/billing/refunds/SKILL.md")
	if err != nil {
		t.Fatalf("ParseURI: %v", err)
	}
	if got := parts.String(); got != "skill://acme/billing/refunds/SKILL.md" {
		t.Errorf("String() = %q", got)
	}
	if got := parts.SkillRootURI(); got != "skill://acme/billing/refunds/" {
		t.Errorf("SkillRootURI() = %q", got)
	}
	if got := parts.ManifestURI(); got != "skill://acme/billing/refunds/SKILL.md" {
		t.Errorf("ManifestURI() = %q", got)
	}
}

func TestIsIndexURI(t *testing.T) {
	if !skills.IsIndexURI("skill://index.json") {
		t.Errorf("IsIndexURI(skill://index.json) = false, want true")
	}
	if skills.IsIndexURI("skill://index.json/") {
		t.Errorf("trailing slash should not match reserved URI")
	}
	if skills.IsIndexURI("skill://other/index.json") {
		t.Errorf("non-root index.json should not match reserved URI")
	}
}

func TestValidateSkillName(t *testing.T) {
	good := []string{"a", "abc", "git-workflow", "acme-billing-refunds", "h2g2", "a1"}
	for _, n := range good {
		if err := skills.ValidateSkillName(n); err != nil {
			t.Errorf("ValidateSkillName(%q) = %v, want nil", n, err)
		}
	}
	bad := []string{"", "A", "Git", "x_y", "a--b", "-a", "a-", "index.json"}
	for _, n := range bad {
		if err := skills.ValidateSkillName(n); err == nil {
			t.Errorf("ValidateSkillName(%q) = nil, want error", n)
		}
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Ensure errors.As works on wrapped errors with formatted context.
func TestErrors_Wrapped(t *testing.T) {
	_, err := skills.ParseURI("skill://BAD/SKILL.md")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, skills.ErrInvalidSkillName) {
		t.Errorf("err = %v, want wrapping ErrInvalidSkillName", err)
	}
	if !strings.Contains(err.Error(), "BAD") {
		t.Errorf("err message %q, expected to include offending name", err.Error())
	}
}
