//go:build lsp_live

// Multi-language conformance probe. Every server here is optional and skipped
// when its binary is absent, so this costs nothing on a machine with none.
//
// It exists because #1301 shipped with gopls as the only server ever tried,
// and running this found that rust-analyzer's first publication after a change
// is an empty placeholder. That bug (#1303) was reported to the model as "no
// problems reported" on an edit that broke the build. Adding a server here is
// the cheapest way to find the next such assumption.
//
//	go test -tags lsp_live ./experimental/agent/ext/lsp/ -run TestServers -v
package lsp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type probeCase struct {
	name    string
	command []string
	langID  string
	ext     string
	files   map[string]string
	symbol  string
	file    string
}

// TestServers checks each installed server end to end: that it starts, that
// its documentSymbol shape decodes, that a symbol resolves by name, and that
// diagnostics for a genuinely broken file reach us.
//
// Assertions rather than logs where the answer is knowable. The encoding and
// the raw symbol shape stay logs, since those legitimately differ per server
// and the point of recording them is that the next reader can see the spread.
func TestServers(t *testing.T) {
	cases := []probeCase{
		{
			name: "typescript", command: []string{"typescript-language-server", "--stdio"},
			langID: "typescript", ext: ".ts", file: "a.ts", symbol: "Cache.get",
			files: map[string]string{
				"a.ts": "export class Cache {\n  get(): number { return 1 }\n}\n\nexport function use(): number {\n  return new Cache().get()\n}\n\nexport function broken(): number { return notDefined() }\n",
			},
		},
		{
			name: "python", command: []string{"pyright-langserver", "--stdio"},
			langID: "python", ext: ".py", file: "a.py", symbol: "Cache.get",
			files: map[string]string{
				"a.py": "class Cache:\n    def get(self):\n        return 1\n\n\ndef use():\n    return Cache().get()\n\n\ndef broken():\n    return not_defined()\n",
			},
		},
		{
			name: "rust", command: []string{"rust-analyzer"},
			langID: "rust", ext: ".rs", file: "src/main.rs", symbol: "get",
			files: map[string]string{
				"Cargo.toml":  "[package]\nname = \"probe\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
				"src/main.rs": "fn get() -> i32 { 1 }\n\nfn main() {\n    let _ = get();\n    let _ = not_defined();\n}\n",
			},
		},
		{
			name: "c", command: []string{"clangd"},
			langID: "c", ext: ".c", file: "a.c", symbol: "get",
			files: map[string]string{
				"a.c": "int get(void) { return 1; }\n\nint use(void) { return get(); }\n\nint broken(void) { return not_defined(); }\n",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := exec.LookPath(tc.command[0]); err != nil {
				t.Skipf("%s not installed", tc.command[0])
			}
			root := t.TempDir()
			resolved, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			for name, body := range tc.files {
				full := filepath.Join(resolved, name)
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			spec := ServerSpec{Command: tc.command, Extensions: []string{tc.ext}, LanguageID: tc.langID}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			c, err := startClient(ctx, spec, []string{resolved})
			if err != nil {
				t.Fatalf("FAIL start: %v", err)
			}
			defer func() { _ = c.close() }()

			t.Logf("positionEncoding: %q", c.encoding)

			// Raw documentSymbol, to see which of the two shapes came back.
			target := filepath.Join(resolved, tc.file)
			if _, err := c.sync(target); err != nil {
				t.Fatalf("sync: %v", err)
			}
			var raw json.RawMessage
			symCtx, symCancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer symCancel()
			if err := c.conn.call(symCtx, "textDocument/documentSymbol", map[string]any{
				"textDocument": map[string]any{"uri": pathToURI(target)},
			}, &raw); err != nil {
				t.Fatalf("documentSymbol: %v", err)
			}
			t.Logf("documentSymbol raw (first 600): %.600s", string(raw))

			var syms []documentSymbol
			if err := json.Unmarshal(raw, &syms); err != nil {
				t.Fatalf("documentSymbol does not decode into our shape: %v", err)
			}
			var names []string
			var walk func(prefix string, list []documentSymbol)
			walk = func(prefix string, list []documentSymbol) {
				for _, s := range list {
					q := s.Name
					if prefix != "" {
						q = prefix + "." + s.Name
					}
					names = append(names, q)
					walk(q, s.Children)
				}
			}
			walk("", syms)
			t.Logf("qualified names seen: %v", names)

			// The silent-failure check: a flat SymbolInformation array decodes
			// into documentSymbol with a zero SelectionRange, which resolves
			// every symbol to line 0 column 0.
			zero := 0
			for _, s := range syms {
				if s.SelectionRange == (textRange{}) {
					zero++
				}
			}
			if zero > 0 {
				t.Logf("WARNING: %d/%d top-level symbols have a zero selectionRange", zero, len(syms))
			}

			sym, err := findSymbol(syms, tc.symbol)
			if err != nil {
				t.Fatalf("findSymbol(%q): %v", tc.symbol, err)
			}
			t.Logf("findSymbol(%q) -> line %d col %d", tc.symbol, sym.SelectionRange.Start.Line, sym.SelectionRange.Start.Character)
			if sym.SelectionRange == (textRange{}) {
				t.Fatalf("%s resolved %q to a zero range, so every symbol would point at line 0", tc.name, tc.symbol)
			}

			// Every fixture calls something undefined, so a server that
			// reports nothing here is the #1303 failure: a broken file
			// reported as clean.
			start := time.Now()
			got := c.refresh(context.Background(), target, 30*time.Second)
			diags := c.diagnostics(target)
			t.Logf("diagnostics published=%v after %s: %+v", got, time.Since(start).Round(time.Millisecond), diags)
			if len(diags) == 0 {
				t.Fatalf("%s reported no problems for a file that does not compile", tc.name)
			}
		})
	}
}

// TestServersSpanTwoWorkspaceFolders is the measurement #1314's design rests
// on: one server instance, two roots, and a correct answer for a file in the
// folder that is not the rootUri.
//
// It decides the shape rather than confirming it. The issue originally assumed
// an instance per root per language, which would have meant N servers, N
// indexes, and N cold starts for a session spanning N repositories.
func TestServersSpanTwoWorkspaceFolders(t *testing.T) {
	cases := []struct {
		name    string
		command []string
		langID  string
		ext     string
		file    string
		a, b    map[string]string
	}{
		{
			name: "gopls", command: []string{"gopls"}, langID: "go", ext: ".go", file: "x.go",
			a: map[string]string{"go.mod": "module a\n\ngo 1.21\n", "x.go": "package a\n\nfunc A() int { return undefinedA() }\n"},
			b: map[string]string{"go.mod": "module b\n\ngo 1.21\n", "x.go": "package b\n\nfunc B() int { return undefinedB() }\n"},
		},
		{
			name: "typescript", command: []string{"typescript-language-server", "--stdio"}, langID: "typescript", ext: ".ts", file: "x.ts",
			a: map[string]string{"x.ts": "export function A(): number { return undefinedA() }\n"},
			b: map[string]string{"x.ts": "export function B(): number { return undefinedB() }\n"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := exec.LookPath(tc.command[0]); err != nil {
				t.Skipf("%s not installed", tc.command[0])
			}
			mk := func(files map[string]string) string {
				dir, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				for n, c := range files {
					if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				return dir
			}
			a, b := mk(tc.a), mk(tc.b)

			c, err := startClient(context.Background(),
				ServerSpec{Command: tc.command, Extensions: []string{tc.ext}, LanguageID: tc.langID},
				[]string{a, b})
			if err != nil {
				t.Fatalf("startClient: %v", err)
			}
			defer func() { _ = c.close() }()

			for _, root := range []string{a, b} {
				path := filepath.Join(root, tc.file)
				if !c.refresh(context.Background(), path, 30*time.Second) {
					t.Fatalf("%s published nothing for %s", tc.name, path)
				}
				diags := c.diagnostics(path)
				t.Logf("%s %s: %d diagnostic(s)", tc.name, path, len(diags))
				if len(diags) == 0 {
					t.Fatalf("%s reported nothing for a file in %s, so one instance does not cover both folders", tc.name, root)
				}
			}
		})
	}
}
