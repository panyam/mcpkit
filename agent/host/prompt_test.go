package host

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/agent"
)

func sec(s string) PromptSection { return PromptSectionFunc(func(context.Context) string { return s }) }

func TestSystemPromptBuilder_JoinsNonEmptyInOrder(t *testing.T) {
	b := &SystemPromptBuilder{Sections: []PromptSection{sec("first"), sec(""), sec("third")}}
	if got := b.Build(context.Background()); got != "first\n\nthird" {
		t.Fatalf("Build = %q, want the non-empty sections joined by a blank line", got)
	}
}

func TestSystemPromptBuilder_AppendPrepend(t *testing.T) {
	b := &SystemPromptBuilder{Sections: []PromptSection{sec("mid")}}
	b.Append(sec("last"))
	b.Prepend(sec("head"))
	if got := b.Build(context.Background()); got != "head\n\nmid\n\nlast" {
		t.Fatalf("Build = %q, want head/mid/last", got)
	}
}

func TestSystemPromptBuilder_AllEmptyIsEmpty(t *testing.T) {
	b := &SystemPromptBuilder{Sections: []PromptSection{sec(""), sec("\n")}}
	if got := b.Build(context.Background()); got != "" {
		t.Fatalf("Build = %q, want empty when every section is blank", got)
	}
}

// TestSystemPromptBuilder_TrimsSurroundingNewlines pins that a section's own
// leading/trailing newlines don't produce extra blank lines in the join.
func TestSystemPromptBuilder_TrimsSurroundingNewlines(t *testing.T) {
	b := &SystemPromptBuilder{Sections: []PromptSection{sec("\na\n"), sec("b")}}
	if got := b.Build(context.Background()); got != "a\n\nb" {
		t.Fatalf("Build = %q, want a\\n\\nb", got)
	}
}

// TestWithSystemPromptBuilder_MutatesDefault pins the customization seam: the
// mutator receives the assembled default builder and can insert a section that
// then appears in Build's output alongside the base instructions.
func TestWithSystemPromptBuilder_MutatesDefault(t *testing.T) {
	ts := startTestServer(t)
	var out strings.Builder
	app, err := NewApp(testConfig(ts.URL), &out, strings.NewReader(""),
		WithProvider(agent.NewStubProvider(agent.StubTurn{Text: "hi"})),
		WithSystemPromptBuilder(func(b *SystemPromptBuilder) {
			b.Prepend(sec("DOMAIN GUIDE"))
		}))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	got := app.promptBuilder.Build(context.Background())
	if !strings.HasPrefix(got, "DOMAIN GUIDE") {
		t.Fatalf("prepended section missing from the built prompt:\n%s", got)
	}
}
