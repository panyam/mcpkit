package agent

import (
	"regexp"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
)

var validProviderToolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func TestSanitizeToolName(t *testing.T) {
	cases := map[string]string{
		"read_file":       "read_file",       // already valid → identity
		"list-runs":       "list-runs",        // hyphen is allowed → identity
		"weather.current": "weather_current",  // dot → _
		"a/b:c d":         "a_b_c_d",           // a run of invalid chars → a single _
		"":                "tool",              // empty → placeholder
	}
	for in, want := range cases {
		if got := sanitizeToolName(in); got != want {
			t.Errorf("sanitizeToolName(%q) = %q, want %q", in, got, want)
		}
	}
	// over-length names are capped at 64 and stay valid
	if got := sanitizeToolName(strings.Repeat("a", 80)); len(got) != 64 || !validProviderToolName.MatchString(got) {
		t.Errorf("long name = %q (len %d), want 64 valid chars", got, len(got))
	}
}

func TestToolNameMaps_RoundTripAndCollisions(t *testing.T) {
	tools := []core.ToolDef{
		{Name: "read_file"},        // valid
		{Name: "weather.current"},  // → weather_current
		{Name: "weather/current"},  // also → weather_current → must disambiguate
	}
	realToSafe, safeToReal := toolNameMaps(tools)

	for _, td := range tools {
		safe := realToSafe[td.Name]
		if !validProviderToolName.MatchString(safe) {
			t.Errorf("safe name %q (from %q) violates the provider pattern", safe, td.Name)
		}
		if safeToReal[safe] != td.Name {
			t.Errorf("round-trip broke: %q → %q → %q", td.Name, safe, safeToReal[safe])
		}
	}
	if realToSafe["read_file"] != "read_file" {
		t.Errorf("a valid name should map to itself, got %q", realToSafe["read_file"])
	}
	if realToSafe["weather.current"] == realToSafe["weather/current"] {
		t.Fatalf("colliding names must get distinct safe names, both = %q", realToSafe["weather.current"])
	}
}

func TestRealAndSafeToolNameFallbacks(t *testing.T) {
	realToSafe, safeToReal := toolNameMaps([]core.ToolDef{{Name: "foo.bar"}})
	// a name not in the current tool set (history for a dropped tool) still
	// serializes to a valid name, though it will not reverse
	if got := safeToolName(realToSafe, "gone.tool"); got != "gone_tool" {
		t.Errorf("fallback safe name = %q, want gone_tool", got)
	}
	// an unknown response name is returned unchanged (already-valid names, too)
	if got := realToolName(safeToReal, "unmapped"); got != "unmapped" {
		t.Errorf("unknown reverse = %q, want unmapped", got)
	}
	if got := realToolName(safeToReal, "foo_bar"); got != "foo.bar" {
		t.Errorf("reverse of the mapped name = %q, want foo.bar", got)
	}
}
