package host

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/experimental/agent"
)

// critiqueTestProvider drives one turn that proposes a tool call, and answers
// every critique pass with a refusal.
//
// The two are told apart by ResponseSchema: the critique gate is the only
// caller here that asks for a structured verdict. A scripted StubProvider
// cannot express this, because the critique call is interleaved into the same
// provider and would consume a scripted turn.
type critiqueTestProvider struct{ turns int }

func (p *critiqueTestProvider) Name() string { return "critique-test" }

func (p *critiqueTestProvider) Generate(_ context.Context, req agent.ProviderRequest) (*agent.ProviderResponse, error) {
	if req.ResponseSchema.Raw() != nil {
		return &agent.ProviderResponse{Text: `{"allow":false,"reason":"violates the stated principles"}`}, nil
	}
	return &agent.ProviderResponse{Text: "done"}, nil
}

func (p *critiqueTestProvider) Stream(_ context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	if req.ResponseSchema.Raw() != nil {
		return &scriptedStream{deltas: []agent.Delta{
			{Kind: agent.DeltaText, Text: `{"allow":false,"reason":"violates the stated principles"}`},
			{Kind: agent.DeltaFinish, FinishReason: "stop"},
		}}, nil
	}
	p.turns++
	if p.turns == 1 {
		return &scriptedStream{deltas: []agent.Delta{
			{Kind: agent.DeltaToolCallStart, Index: 0, ToolCallID: "c1", ToolName: "danger"},
			{Kind: agent.DeltaToolCallArgs, Index: 0, Text: `{}`},
			{Kind: agent.DeltaFinish, FinishReason: "tool_calls"},
		}}, nil
	}
	return &scriptedStream{deltas: []agent.Delta{
		{Kind: agent.DeltaText, Text: "done"},
		{Kind: agent.DeltaFinish, FinishReason: "stop"},
	}}, nil
}

type scriptedStream struct {
	deltas []agent.Delta
	i      int
}

func (s *scriptedStream) Recv() (agent.Delta, error) {
	if s.i >= len(s.deltas) {
		return agent.Delta{}, io.EOF
	}
	d := s.deltas[s.i]
	s.i++
	return d, nil
}

func (s *scriptedStream) Close() error { return nil }

// dangerExt contributes the one tool the turn tries to call.
type dangerExt struct {
	BaseExtension
	ran *bool
}

func (dangerExt) Name() string { return "danger-ext" }

func (e dangerExt) Tools() (agent.ToolSource, error) {
	src := agent.NewFuncSource()
	err := agent.AddFunc(src, "danger", "does something risky",
		func(context.Context, struct{}) (string, error) {
			*e.ran = true
			return "did it", nil
		})
	return src, err
}

// TestCritiqueConfigRefusesARealCall is the end-to-end wiring proof: a host
// config with a Critique block installs the gate, and a turn that proposes a
// gated call has it refused before the tool runs.
//
// Issue 1293's lesson was that a seam nothing registers is a seam nobody has
// validated, so this drives a real turn rather than asserting on construction.
func TestCritiqueConfigRefusesARealCall(t *testing.T) {
	var ran bool
	cfg := &Config{
		Model:        ModelConfig{BaseURL: "http://127.0.0.1:1", Model: "test"},
		Instructions: "test",
		Critique:     &CritiqueConfig{Principles: "never do something risky"},
	}
	var out strings.Builder
	app, err := NewApp(cfg, &out, strings.NewReader(""),
		WithProvider(&critiqueTestProvider{}),
		WithExtension(dangerExt{ran: &ran}),
	)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	defer app.Close()

	if err := app.RunTurn(context.Background(), "do the risky thing"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if ran {
		t.Fatal("the critique gate did not refuse: the tool ran")
	}
	if !strings.Contains(out.String(), "stated principles") {
		t.Errorf("the critique's reason should surface to the user:\n%s", out.String())
	}
}

// TestCritiqueConfigOffByDefault pins that no Critique block installs nothing.
func TestCritiqueConfigOffByDefault(t *testing.T) {
	mw, err := (*CritiqueConfig)(nil).build(&critiqueTestProvider{})
	if err != nil {
		t.Fatalf("nil config should build cleanly: %v", err)
	}
	if mw != nil {
		t.Error("nil Critique config must install no middleware")
	}
}

// TestCritiqueConfigRejectsEmptyPrinciples pins that a misconfigured block
// fails App construction rather than installing a gate that judges nothing.
func TestCritiqueConfigRejectsEmptyPrinciples(t *testing.T) {
	if _, err := (&CritiqueConfig{}).build(&critiqueTestProvider{}); err == nil {
		t.Fatal("a Critique block with no principles should fail to build")
	}
	cfg := &Config{
		Model:        ModelConfig{BaseURL: "http://127.0.0.1:1", Model: "test"},
		Instructions: "test",
		Critique:     &CritiqueConfig{},
	}
	if _, err := NewApp(cfg, &strings.Builder{}, strings.NewReader(""),
		WithProvider(&critiqueTestProvider{}),
	); err == nil {
		t.Error("NewApp should fail on a Critique block with no principles")
	} else if !strings.Contains(err.Error(), "Principles") {
		t.Errorf("the error should name what is missing, got %v", err)
	}
}
