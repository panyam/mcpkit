package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestCodeBlocksUseClassesNotInlineStyles(t *testing.T) {
	var out bytes.Buffer
	src := "```go\npackage main\n\nfunc main() { println(\"hi\") }\n```\n"
	if err := markdown.Convert([]byte(src), &out); err != nil {
		t.Fatal(err)
	}
	html := out.String()
	if !strings.Contains(html, `class="chroma"`) {
		t.Fatalf("code block has no chroma class:\n%s", html)
	}
	if strings.Contains(html, "style=") {
		t.Fatalf("code block carries inline styles, which pin it to the light theme:\n%s", html)
	}
}

func TestCodeThemeCSSFollowsTheSiteDarkMode(t *testing.T) {
	css := string(codeThemeCSS())
	for _, want := range []string{
		"\n.chroma .k {",                        // light keyword, unscoped
		"@media (prefers-color-scheme: dark) {", // OS preference...
		`:root:not([data-theme]) .chroma .k {`,  // ...when no theme is picked
		`:root[data-theme="dark"] .chroma .k {`, // the explicit toggle
	} {
		if !strings.Contains("\n"+css, want) {
			t.Errorf("code theme CSS lacks %q", want)
		}
	}
	// The block background comes from the site's --bg-code, in both themes.
	blockBG := regexp.MustCompile(`(^|\s)\.(chroma|bg) \{[^}]*background-color`)
	for _, line := range strings.Split(css, "\n") {
		if blockBG.MatchString(line) {
			t.Errorf("code theme CSS sets the block background: %s", line)
		}
	}
}
