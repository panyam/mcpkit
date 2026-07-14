// Command check-snippets guards the Get Started guide's code against drift.
//
// docs/GETTING_STARTED.md embeds two Go snippets, each marked with a
// `// server/main.go` or `// client/main.go` first line. Those snippets are
// meant to be verbatim excerpts of examples/getting-started/{server,client}/
// main.go — which compiles in the example module. This tool parses both the
// doc snippet and the example source, drops comments, gofmt-normalizes, and
// fails if the executable code differs. Comments may legitimately differ (the
// doc is terser); the invariant is that the *code* a reader copies is the code
// that actually compiles.
//
// Run via `make check-snippets`; wired into CI so a doc edit that diverges
// from the example (or vice versa) fails the build. Mirrors the
// conformance-staleness gate.
package main

import (
	"bytes"
	"fmt"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strings"
)

var (
	blockRe  = regexp.MustCompile("(?s)```go\\r?\\n(.*?)\\r?\\n```")
	markerRe = regexp.MustCompile(`^//\s*(server|client)/main\.go\b`)
)

// normalize parses Go source WITHOUT comments (default parser mode) and
// re-prints it gofmt-style, so two files with identical code but different
// comments/formatting compare equal.
func normalize(src string) (string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, 0)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, f); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func main() {
	const doc = "docs/GETTING_STARTED.md"
	data, err := os.ReadFile(doc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "check-snippets: %v\n", err)
		os.Exit(2)
	}

	// Collect doc snippets by their marker (server / client).
	snippets := map[string]string{}
	for _, m := range blockRe.FindAllStringSubmatch(string(data), -1) {
		body := m[1]
		first := strings.SplitN(strings.TrimSpace(body), "\n", 2)[0]
		if mk := markerRe.FindStringSubmatch(strings.TrimSpace(first)); mk != nil {
			snippets[mk[1]] = body
		}
	}

	fail := false
	for _, name := range []string{"server", "client"} {
		block, ok := snippets[name]
		if !ok {
			fmt.Printf("MISSING: no `// %s/main.go` snippet found in %s\n", name, doc)
			fail = true
			continue
		}
		srcPath := fmt.Sprintf("examples/getting-started/%s/main.go", name)
		src, err := os.ReadFile(srcPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "check-snippets: %v\n", err)
			fail = true
			continue
		}
		nDoc, errD := normalize(block)
		if errD != nil {
			fmt.Printf("DOES NOT COMPILE: the %s snippet in %s failed to parse: %v\n", name, doc, errD)
			fail = true
			continue
		}
		nSrc, errS := normalize(string(src))
		if errS != nil {
			fmt.Fprintf(os.Stderr, "check-snippets: parse %s: %v\n", srcPath, errS)
			fail = true
			continue
		}
		if nDoc != nSrc {
			fmt.Printf("MISMATCH: %s %s snippet code differs from %s (comments aside).\n", doc, name, srcPath)
			fail = true
		}
	}

	if fail {
		fmt.Fprintf(os.Stderr, "\nGet Started snippets are out of sync with the compiled example.\n"+
			"Reconcile docs/GETTING_STARTED.md with examples/getting-started/ (the example is the compiled source of truth).\n")
		os.Exit(1)
	}
	fmt.Println("check-snippets: Get Started snippets match examples/getting-started/ (comment-stripped code).")
}
