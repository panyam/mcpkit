package files

import (
	"context"
	"strings"
	"testing"
)

func TestExtensionContributesToolsAndPrompt(t *testing.T) {
	e, err := New(Config{Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	if e.Name() != "files" {
		t.Errorf("Name() = %q, want files", e.Name())
	}

	src, err := e.Tools()
	if err != nil {
		t.Fatal(err)
	}
	defs, err := src.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 5 {
		t.Errorf("got %d tools, want 5", len(defs))
	}

	sections := e.PromptSections()
	if len(sections) != 1 {
		t.Fatalf("got %d prompt sections, want 1", len(sections))
	}
	if sections[0].Section(context.Background()) == "" {
		t.Error("the prompt section is empty")
	}
}

// TestPromptStatesTheRulesTheToolsEnforce guards against the two drifting
// apart. A refusal the prompt never warned about reaches the model as a
// surprise it can only learn by failing.
func TestPromptStatesTheRulesTheToolsEnforce(t *testing.T) {
	e, err := New(Config{Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	prompt := e.PromptSections()[0].Section(context.Background())
	for _, rule := range []string{"expect_hash", "exactly once", "together or none"} {
		if !strings.Contains(prompt, rule) {
			t.Errorf("prompt does not mention %q, which the tools enforce:\n%s", rule, prompt)
		}
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New should refuse an empty Root")
	}
	if _, err := New(Config{Roots: []string{"/no/such/directory/anywhere"}}); err == nil {
		t.Fatal("New should refuse a Root that does not exist")
	}
}
