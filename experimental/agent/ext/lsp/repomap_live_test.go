//go:build lsp_live

package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveRepoMapRanksTheCentralFile builds the map from a real gopls over a
// real module, which is the half a stub cannot check: that documentSymbol
// returns what the ranking assumes across a whole tree.
func TestLiveRepoMapRanksTheCentralFile(t *testing.T) {
	root, spec := liveWorkspace(t)

	// core is referenced by both leaves and should come out on top.
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("core.go", "package livetest\n\nfunc CentralHelper() int { return 1 }\n\nfunc AnotherCentral() int { return 2 }\n")
	write("leaf_one.go", "package livetest\n\nfunc LeafOne() int { return CentralHelper() + AnotherCentral() }\n")
	write("leaf_two.go", "package livetest\n\nfunc LeafTwo() int { return CentralHelper() }\n")

	ext, err := New(Config{
		Roots:   []string{root},
		Servers: []ServerSpec{spec},
		RepoMap: &RepoMapConfig{TokenBudget: 500},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = ext.Close() }()

	var out string
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if out = ext.repoMap.get(); out != "" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if out == "" {
		t.Fatal("the background index produced no map within the deadline")
	}
	t.Logf("map:\n%s", out)

	if !strings.Contains(out, "CentralHelper") {
		t.Fatalf("the map should name what the tree declares:\n%s", out)
	}
	corePos := strings.Index(out, "core.go")
	if corePos < 0 {
		t.Fatalf("core.go missing from the map:\n%s", out)
	}
	for _, leaf := range []string{"leaf_one.go", "leaf_two.go"} {
		if p := strings.Index(out, leaf); p >= 0 && p < corePos {
			t.Fatalf("%s outranked the file both leaves reference:\n%s", leaf, out)
		}
	}
}
