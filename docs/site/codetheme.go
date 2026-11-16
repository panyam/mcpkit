package main

import (
	"html/template"
	"regexp"
	"strings"
	"sync"

	chromahtml "github.com/alecthomas/chroma/formatters/html"
	"github.com/alecthomas/chroma/styles"
)

// The highlighter emits CSS classes, not inline styles, so code blocks can
// follow the site's light and dark themes. Inline styles pinned every block
// to a white background with dark-on-light token colors.
const (
	codeStyleLight = "github"
	codeStyleDark  = "monokai"
)

// codeDarkScopes are the two selectors static/css/main.css uses for dark
// mode: the OS preference when the visitor hasn't picked a theme, and the
// explicit toggle.
var codeDarkScopes = []struct{ media, root string }{
	{"(prefers-color-scheme: dark)", `:root:not([data-theme])`},
	{"", `:root[data-theme="dark"]`},
}

var (
	codeCSSOnce sync.Once
	codeCSS     template.CSS
)

// codeThemeCSS returns the token colors for highlighted code: the light
// style as is, and the dark style under each dark-mode selector. It drops
// the styles' own block background, so code blocks keep the site's
// --bg-code in both themes.
func codeThemeCSS() template.CSS {
	codeCSSOnce.Do(func() {
		var b strings.Builder
		b.WriteString(styleCSS(codeStyleLight, ""))
		for _, s := range codeDarkScopes {
			rules := styleCSS(codeStyleDark, s.root+" ")
			if s.media != "" {
				rules = "@media " + s.media + " {\n" + rules + "}\n"
			}
			b.WriteString(rules)
		}
		codeCSS = template.CSS(b.String())
	})
	return codeCSS
}

// cssRule splits one line of chroma's WriteCSS output, such as
// "/* Keyword */ .chroma .k { color: #66d9ef }", into selector and body.
var cssRule = regexp.MustCompile(`^(?:/\*.*?\*/\s*)?([^{]+?)\s*\{(.*)\}\s*$`)

// blockBackground matches the background-color declaration that the .bg
// and .chroma rules carry for the whole block.
var blockBackground = regexp.MustCompile(`background-color:\s*#[0-9a-fA-F]+;?`)

func styleCSS(name, scope string) string {
	var css strings.Builder
	if err := chromahtml.New(chromahtml.WithClasses(true)).WriteCSS(&css, styles.Get(name)); err != nil {
		return ""
	}
	var out strings.Builder
	for _, line := range strings.Split(css.String(), "\n") {
		m := cssRule.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		selector, body := m[1], m[2]
		if selector == ".bg" || selector == ".chroma" {
			body = blockBackground.ReplaceAllString(body, "")
		}
		out.WriteString(scope + selector + " {" + body + "}\n")
	}
	return out.String()
}
