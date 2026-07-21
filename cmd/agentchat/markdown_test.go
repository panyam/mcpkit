package main

import (
	"strings"
	"testing"
)

func TestMDRenderer_DisabledPassthrough(t *testing.T) {
	r := &mdRenderer{disabled: true}
	in := "# Heading\n\n- a\n- b"
	if got := r.render(in); got != in {
		t.Fatalf("disabled render should pass through unchanged:\n got %q\nwant %q", got, in)
	}
}

func TestMDRenderer_RendersMarkdown(t *testing.T) {
	r := &mdRenderer{} // enabled (NO_COLOR gate bypassed by direct construction)
	r.setWidth(80)

	got := r.render("# Heading")
	if got == "# Heading" {
		t.Fatalf("enabled render left markdown untouched: %q", got)
	}
	if !strings.Contains(got, "Heading") {
		t.Fatalf("rendered output dropped the heading text:\n%s", got)
	}

	// a bulleted list becomes glyph bullets — proves structural rendering, not
	// just whitespace, independent of the terminal's color capability
	if list := r.render("- a\n- b"); !strings.Contains(list, "•") {
		t.Fatalf("list not rendered to bullets:\n%s", list)
	}
}

func TestMDRenderer_EmptyStaysEmpty(t *testing.T) {
	if got := (&mdRenderer{}).render(""); got != "" {
		t.Fatalf("empty render = %q, want empty", got)
	}
}
