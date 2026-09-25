package main

import (
	"strings"
	"testing"
)

// TestViewsAreSelfContained checks every View the server embeds is a
// complete HTML document carrying its own runtime, so a host can render it
// from resources/read alone. The bridge View must have the bridge inlined,
// and the three upstream Views must not carry the mcpkit bridge at all.
func TestViewsAreSelfContained(t *testing.T) {
	vs := views()
	if len(vs) != 4 {
		t.Fatalf("views = %d, want 4", len(vs))
	}
	seen := map[string]bool{}
	for _, v := range vs {
		if seen[v.tool] {
			t.Errorf("duplicate tool %s", v.tool)
		}
		seen[v.tool] = true
		if !strings.HasPrefix(v.html, "<!doctype html>") || !strings.Contains(v.html, `data-testid="swatch"`) && !strings.Contains(v.html, `id="root"`) {
			t.Errorf("%s: not a complete View document", v.tool)
		}
		hasBridge := strings.Contains(v.html, "window.MCPApp")
		if v.tool == "pick_color_bridge" && !hasBridge {
			t.Errorf("%s: mcpkit bridge not inlined", v.tool)
		}
		if v.tool != "pick_color_bridge" && hasBridge {
			t.Errorf("%s: carries the mcpkit bridge, want upstream runtime only", v.tool)
		}
	}
}

func TestDarker(t *testing.T) {
	c, err := darker("#ff7f50")
	if err != nil {
		t.Fatal(err)
	}
	if c.Hex != "#cc6540" {
		t.Errorf("darker(#ff7f50) = %s, want #cc6540", c.Hex)
	}
	if _, err := darker("teal"); err == nil {
		t.Error("darker(teal) should fail")
	}
}

func TestPickNamedAndRotating(t *testing.T) {
	calls := 0
	if c := pick("Coral", &calls); c.Hex != "#ff7f50" || calls != 0 {
		t.Errorf("pick(Coral) = %+v calls=%d", c, calls)
	}
	first, second := pick("", &calls), pick("", &calls)
	if first.Name != "teal" || second.Name != "coral" {
		t.Errorf("rotation = %s, %s, want teal, coral", first.Name, second.Name)
	}
}
