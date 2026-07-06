package skills_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestNoCodeExecutionSurface locks the mcpkit reference posture for the
// SEP-2640 code-execution concern raised in the June 2026 core-maintainer
// review: a skill is data delivered over resource primitives, never code to
// run, and skill content is never staged to a real filesystem.
//
// It fails if any non-test source file in this package imports a
// process-execution / dynamic-load package (os/exec, plugin) or calls a
// filesystem-write function (os.WriteFile, os.Create, os.Mkdir,
// os.MkdirAll, ioutil.WriteFile). A regression that widens the attack
// surface breaks the build rather than passing silently.
//
// Scope note: this guards ext/skills itself. Hosts remain free to stage
// skills to disk or run tools; the point is that the library does not, so
// mcpkit cannot be the dangerous host by construction.
func TestNoCodeExecutionSurface(t *testing.T) {
	forbiddenImports := map[string]string{
		"os/exec": "process execution",
		"plugin":  "dynamic code loading",
	}
	// package import path -> set of forbidden selector names. os/exec is the
	// friendly wrapper; the guard also names the lower-level spawn primitives
	// (os.StartProcess, syscall.Exec/ForkExec/StartProcess) so it cannot be
	// bypassed by dropping below os/exec. syscall itself is NOT a forbidden
	// import — it is pulled in for benign constants — so we gate the specific
	// spawn calls, not the package.
	forbiddenCalls := map[string]map[string]bool{
		"os":        {"WriteFile": true, "Create": true, "Mkdir": true, "MkdirAll": true, "StartProcess": true},
		"io/ioutil": {"WriteFile": true},
		"syscall":   {"Exec": true, "ForkExec": true, "StartProcess": true},
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	fset := token.NewFileSet()
	var scanned int
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		// alias -> import path, for resolving selector receivers below.
		aliasToPath := map[string]string{}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if reason, bad := forbiddenImports[path]; bad {
				pos := fset.Position(imp.Pos())
				t.Errorf("%s:%d: forbidden import %q (%s) — ext/skills must not run code", name, pos.Line, path, reason)
			}
			alias := path[strings.LastIndex(path, "/")+1:]
			if imp.Name != nil {
				alias = imp.Name.Name
			}
			aliasToPath[alias] = path
		}

		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkgIdent, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			path := aliasToPath[pkgIdent.Name]
			if names, watched := forbiddenCalls[path]; watched && names[sel.Sel.Name] {
				pos := fset.Position(sel.Pos())
				t.Errorf("%s:%d: forbidden call %s.%s — ext/skills must not run code or stage skill content to disk",
					name, pos.Line, pkgIdent.Name, sel.Sel.Name)
			}
			return true
		})
	}

	if scanned == 0 {
		t.Fatalf("no non-test .go files scanned in %s — test wired incorrectly", mustAbs(t))
	}
}

func mustAbs(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(".")
	if err != nil {
		return "."
	}
	return abs
}
