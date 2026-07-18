package agent

import (
	"context"
	"fmt"
	"strings"
)

// Compactor shrinks a turn's history before the model sees it, trading some
// fidelity for a smaller context. It is the lossy counterpart to tool-result
// offloading (which is lossless, pay-on-lookup): compaction pays
// unconditionally and cannot be un-done, so it is the tool of last resort for
// keeping a long conversation under a model's context budget.
//
// The Runner calls Compact once at the top of a turn, on its own clone of the
// history, so Run stays stateless over the history it is handed. A Compactor
// MUST be a pure function of its input (no turn or session state) and MUST
// return the input unchanged when nothing needs compacting — the Runner
// detects a no-op by length and emits an EventCompaction only when the count
// actually drops.
type Compactor interface {
	Compact(ctx context.Context, history []Message) ([]Message, error)
}

// TokenEstimator estimates the token cost of a message slice so a Compactor
// can decide when history exceeds a budget. Real tokenizers are
// provider-specific and heavy; the default is a cheap character heuristic and
// a real tokenizer is a drop-in behind this seam (a filed follow-up). An
// estimate must be monotonic: more or longer messages never estimate fewer
// tokens.
type TokenEstimator interface {
	Estimate(msgs []Message) int
}

// DefaultCharsPerToken approximates English tokenization (~4 characters per
// token) for CharTokenEstimator.
const DefaultCharsPerToken = 4

// CharTokenEstimator estimates tokens as total text length divided by
// CharsPerToken. It counts message text plus tool-call argument JSON, which
// is what actually bloats a long tool-using conversation. Zero CharsPerToken
// uses DefaultCharsPerToken.
type CharTokenEstimator struct {
	CharsPerToken int
}

// Estimate implements TokenEstimator.
func (e CharTokenEstimator) Estimate(msgs []Message) int {
	per := e.CharsPerToken
	if per <= 0 {
		per = DefaultCharsPerToken
	}
	chars := 0
	for _, m := range msgs {
		chars += len(m.Text)
		for _, tc := range m.ToolCalls {
			chars += len(tc.Args.Raw())
		}
	}
	return chars / per
}

// DefaultKeepRecent is how many trailing messages SummarizingCompactor keeps
// verbatim when no KeepRecent is configured — enough to preserve the active
// exchange while still collapsing the older head.
const DefaultKeepRecent = 6

// SummarizingConfig configures a SummarizingCompactor.
type SummarizingConfig struct {
	// Provider summarizes the head of the conversation. Required.
	Provider Provider

	// Estimator decides when history is over budget. Nil uses
	// CharTokenEstimator with the default ratio.
	Estimator TokenEstimator

	// MaxTokens is the budget: compaction fires only when the estimate
	// exceeds it. Required (must be > 0).
	MaxTokens int

	// KeepRecent is how many trailing messages stay verbatim. Zero uses
	// DefaultKeepRecent. The cut is nudged earlier if it would orphan a
	// tool-result message from its call, so the kept tail is always
	// self-contained.
	KeepRecent int

	// Instructions overrides the summarizer system prompt. Empty uses a
	// built-in prompt that asks for durable facts, decisions, and open
	// threads.
	Instructions string
}

// SummarizingCompactor collapses the head of an over-budget conversation into
// a single RoleSystem summary produced by a model, keeping a recent tail
// verbatim. It is the first, heuristic-budget compaction strategy; a
// token-accurate estimator and mid-turn compaction are follow-ups.
type SummarizingCompactor struct {
	cfg SummarizingConfig
}

const defaultSummarizerInstructions = "You compress the earlier part of a conversation into a compact summary " +
	"an AI assistant will carry forward in place of the original messages. Preserve durable facts, decisions, " +
	"user preferences, names, numbers, identifiers, and any open threads or pending tasks. Write terse notes, " +
	"not prose, and do not invent anything that is not in the text."

// NewSummarizingCompactor validates cfg and returns the compactor. Provider
// is required and MaxTokens must be positive.
func NewSummarizingCompactor(cfg SummarizingConfig) (*SummarizingCompactor, error) {
	if cfg.Provider == nil {
		return nil, fmt.Errorf("agent: SummarizingCompactor needs a Provider")
	}
	if cfg.MaxTokens <= 0 {
		return nil, fmt.Errorf("agent: SummarizingCompactor needs MaxTokens > 0")
	}
	if cfg.Estimator == nil {
		cfg.Estimator = CharTokenEstimator{}
	}
	if cfg.KeepRecent <= 0 {
		cfg.KeepRecent = DefaultKeepRecent
	}
	if cfg.Instructions == "" {
		cfg.Instructions = defaultSummarizerInstructions
	}
	return &SummarizingCompactor{cfg: cfg}, nil
}

// Compact implements Compactor: a no-op under budget, otherwise the head is
// summarized into one RoleSystem message prepended to the verbatim tail.
func (c *SummarizingCompactor) Compact(ctx context.Context, history []Message) ([]Message, error) {
	if c.cfg.Estimator.Estimate(history) <= c.cfg.MaxTokens {
		return history, nil
	}

	cut := len(history) - c.cfg.KeepRecent
	// Do not orphan a tool result from the assistant call that produced it:
	// pull the cut earlier until the tail starts on a non-tool message.
	for cut > 0 && history[cut].Role == RoleTool {
		cut--
	}
	if cut <= 0 {
		// Nothing summarizable ahead of the tail (short, or all tool
		// messages); leave history untouched rather than emit an empty
		// summary.
		return history, nil
	}

	head, tail := history[:cut], history[cut:]
	summary, err := c.summarize(ctx, head)
	if err != nil {
		return nil, err
	}

	out := make([]Message, 0, 1+len(tail))
	out = append(out, Message{Role: RoleSystem, Text: summary})
	out = append(out, tail...)
	return out, nil
}

func (c *SummarizingCompactor) summarize(ctx context.Context, head []Message) (string, error) {
	req := ProviderRequest{
		Instructions: c.cfg.Instructions,
		Messages: []Message{{
			Role: RoleUser,
			Text: "Conversation excerpt to summarize:\n\n" + renderHistory(head),
		}},
	}
	resp, err := c.cfg.Provider.Generate(ctx, req)
	if err != nil {
		return "", fmt.Errorf("agent: summarizing head: %w", err)
	}
	if resp == nil || strings.TrimSpace(resp.Text) == "" {
		return "", fmt.Errorf("agent: summarizer returned no text")
	}
	return "Summary of earlier conversation:\n" + strings.TrimSpace(resp.Text), nil
}

// renderHistory flattens messages to a role-prefixed text block for the
// summarizer to read. Tool calls are shown by name + args so the summary can
// preserve what was done.
func renderHistory(msgs []Message) string {
	var b strings.Builder
	for i, m := range msgs {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(string(m.Role))
		b.WriteString(": ")
		b.WriteString(m.Text)
		for _, tc := range m.ToolCalls {
			b.WriteString("\n  [calls ")
			b.WriteString(tc.Name)
			b.WriteString(" ")
			b.WriteString(strings.TrimSpace(string(tc.Args.Raw())))
			b.WriteString("]")
		}
	}
	return b.String()
}
