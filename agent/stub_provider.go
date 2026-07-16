package agent

import (
	"context"
	"fmt"
	"io"
	"sync"
)

// StubTurn scripts one model call for StubProvider. Set Deltas to play a
// stream verbatim, or set Text/ToolCalls/FinishReason and the turn is
// composed into the canonical delta sequence (text, then
// tool-call-start+args per call, then finish).
type StubTurn struct {
	Deltas       []Delta
	Text         string
	ToolCalls    []ToolCall
	FinishReason string
	// Err, when set, fails the call instead of playing anything.
	Err error
}

// StubProvider is the deterministic Provider used by tests: it plays scripted
// turns in order and records every request it receives. Safe for concurrent
// use; turns are consumed in call order.
type StubProvider struct {
	mu       sync.Mutex
	turns    []StubTurn
	next     int
	requests []ProviderRequest
}

// NewStubProvider returns a provider that plays turns in order and errors
// when called past the script.
func NewStubProvider(turns ...StubTurn) *StubProvider {
	return &StubProvider{turns: turns}
}

// Requests returns a copy of every ProviderRequest received so far, in call
// order. Use it to assert what the Runner actually sent the model.
func (s *StubProvider) Requests() []ProviderRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ProviderRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

func (s *StubProvider) take(req ProviderRequest) (StubTurn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
	if s.next >= len(s.turns) {
		return StubTurn{}, fmt.Errorf("agent: stub provider exhausted after %d turns", len(s.turns))
	}
	t := s.turns[s.next]
	s.next++
	return t, nil
}

func (t StubTurn) deltas() []Delta {
	if t.Deltas != nil {
		return t.Deltas
	}
	var out []Delta
	if t.Text != "" {
		out = append(out, Delta{Kind: DeltaText, Text: t.Text})
	}
	for i, tc := range t.ToolCalls {
		out = append(out, Delta{Kind: DeltaToolCallStart, Index: i, ToolCallID: tc.ID, ToolName: tc.Name, Text: string(tc.Args.Raw())})
	}
	reason := t.FinishReason
	if reason == "" {
		if len(t.ToolCalls) > 0 {
			reason = "tool_calls"
		} else {
			reason = "stop"
		}
	}
	return append(out, Delta{Kind: DeltaFinish, FinishReason: reason})
}

// Stream implements Provider.
func (s *StubProvider) Stream(ctx context.Context, req ProviderRequest) (Stream, error) {
	turn, err := s.take(req)
	if err != nil {
		return nil, err
	}
	if turn.Err != nil {
		return nil, turn.Err
	}
	return &stubStream{deltas: turn.deltas()}, nil
}

// Generate implements Provider by folding the scripted turn.
func (s *StubProvider) Generate(ctx context.Context, req ProviderRequest) (*ProviderResponse, error) {
	turn, err := s.take(req)
	if err != nil {
		return nil, err
	}
	if turn.Err != nil {
		return nil, turn.Err
	}
	var acc Accumulator
	for _, d := range turn.deltas() {
		acc.Add(d)
	}
	return acc.Result(), nil
}

type stubStream struct {
	deltas []Delta
	i      int
}

// Recv implements Stream.
func (s *stubStream) Recv() (Delta, error) {
	if s.i >= len(s.deltas) {
		return Delta{}, io.EOF
	}
	d := s.deltas[s.i]
	s.i++
	return d, nil
}

// Close implements Stream.
func (s *stubStream) Close() error { return nil }
