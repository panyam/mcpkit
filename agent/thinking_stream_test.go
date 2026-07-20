package agent

import (
	"context"
	"io"
	"strings"
	"testing"
)

// feedAll runs chunks through a parser and returns the concatenated text and
// reasoning (robust to how the parser splits deltas across the boundary).
func feedAll(open, close string, chunks ...string) (text, reason string) {
	p := newThinkParser(open, close)
	var t, r strings.Builder
	emit := func(ds []Delta) {
		for _, d := range ds {
			if d.Kind == DeltaReasoning {
				r.WriteString(d.Text)
			} else {
				t.WriteString(d.Text)
			}
		}
	}
	for _, c := range chunks {
		emit(p.feed(c))
	}
	emit(p.flush())
	return t.String(), r.String()
}

func TestThinkParser_BasicSplit(t *testing.T) {
	text, reason := feedAll("<think>", "</think>", "hello <think>weighing options</think> world")
	if text != "hello  world" {
		t.Fatalf("text = %q", text)
	}
	if reason != "weighing options" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestThinkParser_TagStraddlesChunks(t *testing.T) {
	// Every tag is split across a chunk boundary — the boundary-safe case.
	text, reason := feedAll("<think>", "</think>",
		"answer: <thi", "nk>because rea", "sons</thi", "nk>42")
	if text != "answer: 42" {
		t.Fatalf("text = %q", text)
	}
	if reason != "because reasons" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestThinkParser_EmptyOpenTag(t *testing.T) {
	// No open tag: reasoning runs from the head until closeTag, then text.
	text, reason := feedAll("", "</think>", "step one, step two</think>the answer is 5")
	if text != "the answer is 5" {
		t.Fatalf("text = %q", text)
	}
	if reason != "step one, step two" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestThinkParser_MultipleBlocks(t *testing.T) {
	text, reason := feedAll("<think>", "</think>",
		"a<think>r1</think>b<think>r2</think>c")
	if text != "abc" {
		t.Fatalf("text = %q", text)
	}
	if reason != "r1r2" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestThinkParser_NoTags(t *testing.T) {
	text, reason := feedAll("<think>", "</think>", "just a plain answer")
	if text != "just a plain answer" || reason != "" {
		t.Fatalf("text=%q reason=%q", text, reason)
	}
}

type fakeStream struct {
	deltas []Delta
	i      int
}

func (f *fakeStream) Recv() (Delta, error) {
	if f.i >= len(f.deltas) {
		return Delta{}, io.EOF
	}
	d := f.deltas[f.i]
	f.i++
	return d, nil
}
func (f *fakeStream) Close() error { return nil }

type fakeProvider struct {
	stream *fakeStream
	resp   *ProviderResponse
}

func (p *fakeProvider) Stream(context.Context, ProviderRequest) (Stream, error) {
	return p.stream, nil
}
func (p *fakeProvider) Generate(context.Context, ProviderRequest) (*ProviderResponse, error) {
	return p.resp, nil
}

func TestThinkingStream_ReinterpretsAndPreservesOtherDeltas(t *testing.T) {
	inner := &fakeStream{deltas: []Delta{
		{Kind: DeltaText, Text: "ok <thi"},
		{Kind: DeltaText, Text: "nk>hmm</think>done"},
		{Kind: DeltaToolCallStart, Text: "", ToolCallID: "c1", ToolName: "search"},
		{Kind: DeltaFinish, FinishReason: "tool_calls"},
	}}
	p := NewThinkingProvider(&fakeProvider{stream: inner}, "<think>", "</think>")
	s, err := p.Stream(context.Background(), ProviderRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var text, reason string
	var sawToolStart, sawFinish bool
	for {
		d, err := s.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch d.Kind {
		case DeltaText:
			text += d.Text
		case DeltaReasoning:
			reason += d.Text
		case DeltaToolCallStart:
			sawToolStart = true
			if d.ToolName != "search" {
				t.Fatalf("tool name = %q", d.ToolName)
			}
		case DeltaFinish:
			sawFinish = true
		}
	}
	if text != "ok done" {
		t.Fatalf("text = %q", text)
	}
	if reason != "hmm" {
		t.Fatalf("reason = %q", reason)
	}
	if !sawToolStart || !sawFinish {
		t.Fatalf("non-text deltas dropped: toolStart=%v finish=%v", sawToolStart, sawFinish)
	}
}

func TestThinkingProvider_GenerateSplitsReasoning(t *testing.T) {
	inner := &fakeProvider{resp: &ProviderResponse{Text: "prefix <think>deliberation</think>final"}}
	p := NewThinkingProvider(inner, "<think>", "</think>")
	resp, err := p.Generate(context.Background(), ProviderRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "prefix final" {
		t.Fatalf("text = %q", resp.Text)
	}
	if resp.Reasoning != "deliberation" {
		t.Fatalf("reasoning = %q", resp.Reasoning)
	}
}

func TestNewThinkingProvider_InertWhenNoCloseTag(t *testing.T) {
	inner := &fakeProvider{}
	if got := NewThinkingProvider(inner, "<think>", ""); got != Provider(inner) {
		t.Fatal("empty closeTag should return the inner provider unwrapped")
	}
}
