package agent

import (
	"context"
	"testing"

	"github.com/panyam/mcpkit/core"
)

func floatPtr(f float64) *float64 { return &f }

// genRunner builds a Runner whose config carries gen, with a one-turn script.
func genRunner(t *testing.T, gen GenerationParams) (*Runner, *StubProvider) {
	t.Helper()
	p := NewStubProvider(StubTurn{Text: "done"})
	r, err := NewRunner(RunnerConfig{Provider: p, Instructions: "be helpful", Generation: gen})
	if err != nil {
		t.Fatal(err)
	}
	return r, p
}

// TestGenerationDefaultsReachTheStepLoop is the core of issue 1239: before it,
// RunnerConfig had no way to express temperature, token cap, or tool choice, so
// three of the four ProviderRequest generation parameters were unreachable from
// a turn.
func TestGenerationDefaultsReachTheStepLoop(t *testing.T) {
	r, p := genRunner(t, GenerationParams{
		Temperature: floatPtr(0.7),
		MaxTokens:   256,
		ToolChoice:  ToolChoiceRequired,
	})

	if _, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "hi"}}, nil); err != nil {
		t.Fatal(err)
	}

	reqs := p.Requests()
	if len(reqs) != 1 {
		t.Fatalf("want 1 request, got %d", len(reqs))
	}
	got := reqs[0]
	if got.Temperature == nil || *got.Temperature != 0.7 {
		t.Errorf("Temperature = %v, want 0.7", got.Temperature)
	}
	if got.MaxTokens != 256 {
		t.Errorf("MaxTokens = %d, want 256", got.MaxTokens)
	}
	if got.ToolChoice != ToolChoiceRequired {
		t.Errorf("ToolChoice = %+v, want %+v", got.ToolChoice, ToolChoiceRequired)
	}
}

// TestGenerationUnsetSendsNothing pins the default path: a Runner with no
// Generation must build the same request it built before these fields existed,
// so an existing caller's wire shape is byte-identical.
func TestGenerationUnsetSendsNothing(t *testing.T) {
	r, p := genRunner(t, GenerationParams{})

	if _, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "hi"}}, nil); err != nil {
		t.Fatal(err)
	}

	got := p.Requests()[0]
	if got.Temperature != nil {
		t.Errorf("Temperature = %v, want nil", *got.Temperature)
	}
	if got.MaxTokens != 0 {
		t.Errorf("MaxTokens = %d, want 0", got.MaxTokens)
	}
	if !got.ToolChoice.IsZero() {
		t.Errorf("ToolChoice = %+v, want zero", got.ToolChoice)
	}
}

// TestGenerationPerTurnOverride covers the per-turn half: TurnRequest.Generation
// wins over the config for the fields it sets. Forcing a tool call on one
// proactive turn is the motivating case.
func TestGenerationPerTurnOverride(t *testing.T) {
	r, p := genRunner(t, GenerationParams{Temperature: floatPtr(0.2), MaxTokens: 100})

	_, err := r.RunTurn(context.Background(), TurnRequest{
		History:    []Message{{Role: RoleUser, Text: "hi"}},
		Generation: GenerationParams{Temperature: floatPtr(0.9)},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := p.Requests()[0]
	if got.Temperature == nil || *got.Temperature != 0.9 {
		t.Errorf("Temperature = %v, want 0.9 (turn wins)", got.Temperature)
	}
}

// TestGenerationPerTurnMergeInheritsUnsetFields is the merge contract: a zero
// field on the turn inherits the config's rather than clearing it. Without this
// a turn that only wants to force a tool call would silently drop the config's
// temperature and token cap.
func TestGenerationPerTurnMergeInheritsUnsetFields(t *testing.T) {
	r, p := genRunner(t, GenerationParams{Temperature: floatPtr(0.2), MaxTokens: 100})

	_, err := r.RunTurn(context.Background(), TurnRequest{
		History:    []Message{{Role: RoleUser, Text: "hi"}},
		Generation: GenerationParams{ToolChoice: ToolChoiceRequired},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := p.Requests()[0]
	if got.Temperature == nil || *got.Temperature != 0.2 {
		t.Errorf("Temperature = %v, want 0.2 inherited", got.Temperature)
	}
	if got.MaxTokens != 100 {
		t.Errorf("MaxTokens = %d, want 100 inherited", got.MaxTokens)
	}
	if got.ToolChoice != ToolChoiceRequired {
		t.Errorf("ToolChoice = %+v, want the turn's", got.ToolChoice)
	}
}

// TestGenerationReachesFinalizingGenerate covers the structured-output path:
// the finalizing Generate is a second model call and must carry the same
// params, or a token cap set for the turn would not apply to it.
func TestGenerationReachesFinalizingGenerate(t *testing.T) {
	p := NewStubProvider(
		StubTurn{Text: "done"},
		StubTurn{Text: `{"ok":true}`},
	)
	r, err := NewRunner(RunnerConfig{
		Provider:       p,
		Instructions:   "be helpful",
		ResponseSchema: core.NewRawJSON([]byte(`{"type":"object"}`)),
		Generation:     GenerationParams{Temperature: floatPtr(0.3), MaxTokens: 64},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "hi"}}, nil); err != nil {
		t.Fatal(err)
	}

	reqs := p.Requests()
	if len(reqs) != 2 {
		t.Fatalf("want 2 requests (step + finalize), got %d", len(reqs))
	}
	final := reqs[1]
	if final.Temperature == nil || *final.Temperature != 0.3 {
		t.Errorf("finalize Temperature = %v, want 0.3", final.Temperature)
	}
	if final.MaxTokens != 64 {
		t.Errorf("finalize MaxTokens = %d, want 64", final.MaxTokens)
	}
}

// TestGenerationToolChoiceDroppedOnFinalize guards a real trap: the finalizing
// call offers no tools, so forwarding a ToolChoice would ask the provider to
// force a call it has no tool for. OpenAI rejects that combination.
func TestGenerationToolChoiceDroppedOnFinalize(t *testing.T) {
	p := NewStubProvider(
		StubTurn{Text: "done"},
		StubTurn{Text: `{"ok":true}`},
	)
	r, err := NewRunner(RunnerConfig{
		Provider:       p,
		Instructions:   "be helpful",
		ResponseSchema: core.NewRawJSON([]byte(`{"type":"object"}`)),
		Generation:     GenerationParams{ToolChoice: ToolChoiceRequired},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "hi"}}, nil); err != nil {
		t.Fatal(err)
	}

	final := p.Requests()[1]
	if !final.ToolChoice.IsZero() {
		t.Errorf("finalize ToolChoice = %+v, want zero (no tools offered)", final.ToolChoice)
	}
}

// TestGenerationParamsMerge exercises the merge rules directly, including the
// documented corollary that a turn cannot un-set a config default.
func TestGenerationParamsMerge(t *testing.T) {
	base := GenerationParams{Temperature: floatPtr(0.2), MaxTokens: 100, ToolChoice: ToolChoiceNone}

	t.Run("zero override inherits everything", func(t *testing.T) {
		got := base.merge(GenerationParams{})
		if got.Temperature == nil || *got.Temperature != 0.2 || got.MaxTokens != 100 || got.ToolChoice != ToolChoiceNone {
			t.Errorf("merge(zero) = %+v, want base", got)
		}
	})

	t.Run("explicit zero temperature is a real value", func(t *testing.T) {
		got := base.merge(GenerationParams{Temperature: floatPtr(0)})
		if got.Temperature == nil || *got.Temperature != 0 {
			t.Errorf("Temperature = %v, want an explicit 0", got.Temperature)
		}
	})

	t.Run("merge does not mutate the receiver", func(t *testing.T) {
		_ = base.merge(GenerationParams{MaxTokens: 999})
		if base.MaxTokens != 100 {
			t.Errorf("base mutated: MaxTokens = %d, want 100", base.MaxTokens)
		}
	})
}
