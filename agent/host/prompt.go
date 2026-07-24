package host

import (
	"context"
	"strings"
)

// PromptSection contributes one section of the per-turn system prompt. Returning
// "" omits the section (no stray blank lines). Sections run every turn, so a
// section may read live/dynamic state (late-connecting servers' skills, memory).
type PromptSection interface {
	Section(ctx context.Context) string
}

// PromptSectionFunc adapts a plain function to PromptSection (like
// http.HandlerFunc adapts a function to http.Handler).
type PromptSectionFunc func(ctx context.Context) string

// Section implements PromptSection.
func (f PromptSectionFunc) Section(ctx context.Context) string { return f(ctx) }

// SystemPromptBuilder assembles the system prompt from ordered sections. It is
// the host-layer composition over the Runner's low-level InstructionsFunc hook:
// Build runs each turn and joins the non-empty sections, so prompt assembly is
// composable — reorder, insert profile/domain-guide sections, or replace the
// section list — instead of being hard-coded. The default builder mirrors the
// previous fixed assembly (base instructions, then per-server skill blocks in
// config order); WithSystemPromptBuilder lets a caller customize it.
type SystemPromptBuilder struct {
	Sections []PromptSection
}

// Build joins every non-empty section in order, separated by a blank line, and
// runs each turn (wired as RunnerConfig.InstructionsFunc). A section that
// returns "" contributes nothing, so empty base instructions or a server with
// no skills leave no blank gap.
func (b *SystemPromptBuilder) Build(ctx context.Context) string {
	var parts []string
	for _, s := range b.Sections {
		if p := strings.Trim(s.Section(ctx), "\n"); p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "\n\n")
}

// Append adds a section after the existing ones.
func (b *SystemPromptBuilder) Append(s PromptSection) { b.Sections = append(b.Sections, s) }

// Prepend adds a section before the existing ones.
func (b *SystemPromptBuilder) Prepend(s PromptSection) {
	b.Sections = append([]PromptSection{s}, b.Sections...)
}
