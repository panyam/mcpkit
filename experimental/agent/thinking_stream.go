package agent

import (
	"context"
	"io"
	"strings"
)

// NewThinkingProvider wraps p so inline reasoning that a model emits in its
// text stream — delimited by openTag/closeTag, e.g. "<think>…</think>" — is
// re-emitted as DeltaReasoning instead of DeltaText. That is all the Runner
// needs: its consumeStream already turns DeltaReasoning into
// thinking-begin/delta/end events, so a surface renders the reasoning
// distinctly with no Runner change.
//
// openTag empty means reasoning starts at the stream head and runs until the
// first closeTag (the "no open tag" models that stream reasoning first, then
// the answer). closeTag empty makes the hint inert: p is returned unwrapped,
// since without a terminator there is nothing to delimit.
//
// The transform is delimiter-boundary safe: a tag split across two provider
// deltas (a "<thi" / "nk>" boundary) is buffered and matched whole. Native
// DeltaReasoning from a provider that already separates reasoning passes
// through untouched — the parser only reinterprets DeltaText.
func NewThinkingProvider(p Provider, openTag, closeTag string) Provider {
	if closeTag == "" {
		return p
	}
	return &thinkingProvider{inner: p, open: openTag, close: closeTag}
}

type thinkingProvider struct {
	inner       Provider
	open, close string
}

// Stream wraps the inner stream so DeltaText carrying reasoning delimiters is
// re-split into DeltaText / DeltaReasoning.
func (t *thinkingProvider) Stream(ctx context.Context, req ProviderRequest) (Stream, error) {
	s, err := t.inner.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	return &thinkingStream{inner: s, p: newThinkParser(t.open, t.close)}, nil
}

// Generate splits inline reasoning out of the completed Text into Reasoning,
// so the non-streaming path stays consistent with Stream.
func (t *thinkingProvider) Generate(ctx context.Context, req ProviderRequest) (*ProviderResponse, error) {
	resp, err := t.inner.Generate(ctx, req)
	if err != nil || resp == nil {
		return resp, err
	}
	p := newThinkParser(t.open, t.close)
	var text, reason strings.Builder
	for _, d := range append(p.feed(resp.Text), p.flush()...) {
		if d.Kind == DeltaReasoning {
			reason.WriteString(d.Text)
		} else {
			text.WriteString(d.Text)
		}
	}
	resp.Text = text.String()
	if r := reason.String(); r != "" {
		if resp.Reasoning != "" {
			resp.Reasoning += r
		} else {
			resp.Reasoning = r
		}
	}
	return resp, nil
}

// thinkingStream reinterprets an inner stream's DeltaText through a
// thinkParser, queuing the parser's output deltas so Recv can hand them out
// one at a time.
type thinkingStream struct {
	inner Stream
	p     *thinkParser
	queue []Delta
	done  bool
}

// Recv implements Stream: it pulls from the queue, refilling from the inner
// stream (feeding DeltaText through the parser, flushing then forwarding any
// non-text delta) until a delta is available or the inner stream ends.
func (s *thinkingStream) Recv() (Delta, error) {
	for len(s.queue) == 0 {
		if s.done {
			return Delta{}, io.EOF
		}
		d, err := s.inner.Recv()
		if err == io.EOF {
			s.done = true
			s.queue = append(s.queue, s.p.flush()...)
			continue
		}
		if err != nil {
			return Delta{}, err
		}
		if d.Kind == DeltaText {
			s.queue = append(s.queue, s.p.feed(d.Text)...)
			continue
		}
		// A non-text delta ends the current text run: flush any held
		// partial-tag bytes (as the current mode) before forwarding it, so
		// ordering is preserved.
		s.queue = append(s.queue, s.p.flush()...)
		s.queue = append(s.queue, d)
	}
	d := s.queue[0]
	s.queue = s.queue[1:]
	return d, nil
}

// Close implements Stream.
func (s *thinkingStream) Close() error { return s.inner.Close() }

// thinkParser is the stateful delimiter machine. It holds a small buffer so a
// tag straddling two feeds is matched whole, and toggles inThink each time it
// crosses a boundary. Emit mode is reasoning while inThink, text otherwise.
type thinkParser struct {
	open, close string
	inThink     bool
	buf         string
}

// newThinkParser starts inThink when openTag is empty (reasoning-from-head
// models); otherwise it starts in text mode looking for openTag.
func newThinkParser(open, close string) *thinkParser {
	return &thinkParser{open: open, close: close, inThink: open == ""}
}

// feed processes the next text fragment and returns the reinterpreted deltas.
func (p *thinkParser) feed(s string) []Delta {
	p.buf += s
	var out []Delta
	for {
		tag := p.open
		if p.inThink {
			tag = p.close
		}
		// No tag to look for (empty openTag after leaving a think block):
		// the rest is plain text, nothing more to split.
		if tag == "" {
			out = appendNonEmpty(out, p.emit(p.buf))
			p.buf = ""
			break
		}
		if idx := strings.Index(p.buf, tag); idx >= 0 {
			out = appendNonEmpty(out, p.emit(p.buf[:idx]))
			p.buf = p.buf[idx+len(tag):]
			p.inThink = !p.inThink
			continue
		}
		// No full tag: emit everything except a suffix that could be the
		// start of the tag, and hold that suffix for the next feed.
		keep := prefixOverlap(p.buf, tag)
		out = appendNonEmpty(out, p.emit(p.buf[:len(p.buf)-keep]))
		p.buf = p.buf[len(p.buf)-keep:]
		break
	}
	return out
}

// flush emits whatever is buffered (at stream end or before a non-text
// delta) as the current mode; buffered bytes are literal content by then.
func (p *thinkParser) flush() []Delta {
	if p.buf == "" {
		return nil
	}
	d := p.emit(p.buf)
	p.buf = ""
	return []Delta{d}
}

func (p *thinkParser) emit(text string) Delta {
	if p.inThink {
		return Delta{Kind: DeltaReasoning, Text: text}
	}
	return Delta{Kind: DeltaText, Text: text}
}

func appendNonEmpty(out []Delta, d Delta) []Delta {
	if d.Text == "" {
		return out
	}
	return append(out, d)
}

// prefixOverlap returns the length of the longest suffix of s that is a
// (strict) prefix of tag — the partial-tag tail to hold back. Zero when no
// suffix could begin the tag.
func prefixOverlap(s, tag string) int {
	max := len(tag) - 1
	if len(s) < max {
		max = len(s)
	}
	for k := max; k > 0; k-- {
		if strings.HasPrefix(tag, s[len(s)-k:]) {
			return k
		}
	}
	return 0
}
