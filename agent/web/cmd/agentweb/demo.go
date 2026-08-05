package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/panyam/mcpkit/agent"
)

// demoProvider is an inexhaustible, offline Provider for `agentweb --demo`: it
// streams a canned reply that echoes the user's last message, word by word with
// a small delay so the browser Conversation panel visibly streams. Unlike the
// test StubProvider it never exhausts, so a person can type as many turns as
// they like. It is demo-only wiring, not a library type.
type demoProvider struct{}

func (demoProvider) reply(req agent.ProviderRequest) string {
	last := ""
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == agent.RoleUser {
			last = strings.TrimSpace(req.Messages[i].Text)
			break
		}
	}
	if last == "" {
		return "Hello from agentweb — this is the demo provider. Type a message and watch it stream into the Conversation panel."
	}
	return fmt.Sprintf("You said: %q. This reply is streamed word by word from the agentweb demo provider so you can see the live turn arrive over the Watch stream.", last)
}

func (d demoProvider) Stream(_ context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	return &demoStream{words: strings.Fields(d.reply(req))}, nil
}

func (d demoProvider) Generate(_ context.Context, req agent.ProviderRequest) (*agent.ProviderResponse, error) {
	return &agent.ProviderResponse{Text: d.reply(req), FinishReason: "stop"}, nil
}

// demoStream emits one DeltaText per word (with a trailing space), pausing
// briefly between words, then a DeltaFinish.
type demoStream struct {
	words []string
	i     int
	done  bool
}

func (s *demoStream) Recv() (agent.Delta, error) {
	if s.i < len(s.words) {
		w := s.words[s.i]
		s.i++
		time.Sleep(45 * time.Millisecond)
		return agent.Delta{Kind: agent.DeltaText, Text: w + " "}, nil
	}
	if !s.done {
		s.done = true
		return agent.Delta{Kind: agent.DeltaFinish, FinishReason: "stop"}, nil
	}
	return agent.Delta{}, io.EOF
}

func (s *demoStream) Close() error { return nil }
