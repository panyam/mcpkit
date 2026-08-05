package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/panyam/mcpkit/agent"
)

// demoProvider is an inexhaustible, offline Provider for `agentweb --demo`. It
// is scripted so the whole surface — not just the Conversation panel — visibly
// populates: on a top-level turn that has delegate tools available it fans out
// to the sub-agent personas (producing sub-agent-tree activity, tool calls, and
// an offloaded result), reports token usage so the budget gauges move, then on
// the follow-up turn streams a final answer word by word. A persona's own turn
// (no tools in its request) just streams text, so there is no recursion. Unlike
// the test StubProvider it never exhausts, so a person can drive many turns.
type demoProvider struct{}

// personaNames are the delegate tools the scripted top-level turn calls when
// they are present in the request. Kept in sync with the SubAgentConfig names
// wired in buildApp.
var personaNames = []string{"researcher", "analyst"}

// lastUserText returns the most recent user-role message text.
func lastUserText(req agent.ProviderRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == agent.RoleUser {
			return strings.TrimSpace(req.Messages[i].Text)
		}
	}
	return ""
}

// lastRole reports the role of the final message, which tells a top-level first
// pass (RoleUser) apart from the follow-up after tool results (RoleTool).
func lastRole(req agent.ProviderRequest) agent.Role {
	if len(req.Messages) == 0 {
		return agent.RoleUser
	}
	return req.Messages[len(req.Messages)-1].Role
}

// availablePersonas returns the persona tools present in the request, in
// personaNames order, so the script only calls tools that actually exist.
func availablePersonas(req agent.ProviderRequest) []string {
	have := map[string]bool{}
	for _, t := range req.Tools {
		have[t.Name] = true
	}
	var out []string
	for _, name := range personaNames {
		if have[name] {
			out = append(out, name)
		}
	}
	return out
}

// personaReply is what a sub-agent persona streams back. The researcher returns
// a long passage (over the demo offload threshold, so it comes back as an
// offloaded stub in the tool inspector); the analyst returns a short note.
func personaReply(task string) string {
	lower := strings.ToLower(task)
	if strings.Contains(lower, "analyz") || strings.Contains(lower, "risk") {
		return "Risk note: the rollout is low risk. The web surface reuses the terminal host verbatim, so its behavior is already exercised by the agentchat tests."
	}
	if strings.Contains(lower, "research") {
		return "Research summary: agentweb is the browser analogue of the terminal agentchat, a thin surface over the same agent/host.App. " +
			"It streams host events over a Connect Watch stream into a DockView workspace of Solid islands. The observability panels project " +
			"that one event stream five ways. A sub-agent tree, a filterable activity timeline, a memory inspector, a tool and offload " +
			"inspector, and token and budget gauges let an operator watch a turn fan out across delegated agents, see each tool call and " +
			"its result, and track token spend, all without leaving the page. This passage is deliberately long so it trips the demo offload " +
			"threshold and shows up as a stored stub with a ref in the tool inspector panel."
	}
	return "Sub-agent reply: " + task
}

func (d demoProvider) reply(req agent.ProviderRequest) string {
	last := lastUserText(req)
	if last == "" {
		return "Hello from agentweb — the demo provider. Ask me to research or analyze the agentweb mission and watch the sub-agent, tool, timeline, and budget panels populate."
	}
	return fmt.Sprintf("Done. I delegated your request (%q) to the researcher and analyst sub-agents; their findings streamed into the observability panels. Ask another question to drive another turn.", last)
}

func (d demoProvider) Stream(_ context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	// Three cases, keyed on the last message role and whether delegate tools are
	// present: a top-level first pass (RoleUser + persona tools) fans out to the
	// personas; a persona's own turn (RoleUser, no tools) streams its persona
	// reply (the researcher's is long enough to be offloaded); the follow-up
	// after tool results (RoleTool) streams the top-level answer.
	if lastRole(req) == agent.RoleUser {
		if personas := availablePersonas(req); len(personas) > 0 {
			return &demoStream{delegateTo: personas, task: lastUserText(req)}, nil
		}
		return &demoStream{words: strings.Fields(personaReply(lastUserText(req)))}, nil
	}
	return &demoStream{words: strings.Fields(d.reply(req))}, nil
}

func (d demoProvider) Generate(_ context.Context, req agent.ProviderRequest) (*agent.ProviderResponse, error) {
	return &agent.ProviderResponse{Text: d.reply(req), FinishReason: "stop", Usage: &agent.Usage{InputTokens: 900, OutputTokens: 60}}, nil
}

// demoStream drives one of two scripts. When delegateTo is set it emits a short
// reasoning fragment then one tool call per named persona (finishing with
// "tool_calls" and reporting usage); otherwise it streams words (a text reply)
// then finishes with "stop". Both report usage so the budget gauges move.
type demoStream struct {
	// text-reply script
	words []string
	i     int

	// delegation script
	delegateTo []string
	task       string
	reasoned   bool
	cursor     int  // which persona is being emitted
	needArgs   bool // the current persona's start is out; its args are next

	usageSent bool
	done      bool
}

func (s *demoStream) Recv() (agent.Delta, error) {
	if len(s.delegateTo) > 0 {
		return s.recvDelegate()
	}
	return s.recvText()
}

// personaTask renders the task text a persona is seeded with, tagged so its
// reply length differs (research = long/offloaded, analyst = short).
func (s *demoStream) personaTask(name string) string {
	task := s.task
	if task == "" {
		task = "the agentweb mission"
	}
	if name == "analyst" {
		return "analyze the risks of " + task
	}
	return "research " + task
}

// recvText streams the reply word by word, then a usage delta and a stop finish.
func (s *demoStream) recvText() (agent.Delta, error) {
	if s.i < len(s.words) {
		w := s.words[s.i]
		s.i++
		time.Sleep(35 * time.Millisecond)
		return agent.Delta{Kind: agent.DeltaText, Text: w + " "}, nil
	}
	if !s.usageSent {
		s.usageSent = true
		return agent.Delta{Kind: agent.DeltaUsage, Usage: &agent.Usage{InputTokens: 320 + len(s.words)*4, OutputTokens: len(s.words)}}, nil
	}
	if !s.done {
		s.done = true
		return agent.Delta{Kind: agent.DeltaFinish, FinishReason: "stop"}, nil
	}
	return agent.Delta{}, io.EOF
}

// recvDelegate emits a reasoning fragment, then for each persona a tool-call
// start followed by its args fragment, then a usage delta and a tool_calls
// finish so the Runner dispatches the sub-agents.
func (s *demoStream) recvDelegate() (agent.Delta, error) {
	if !s.reasoned {
		s.reasoned = true
		time.Sleep(35 * time.Millisecond)
		return agent.Delta{Kind: agent.DeltaReasoning, Text: "This needs both a research pass and a risk analysis, so I'll delegate to both sub-agents in parallel."}, nil
	}
	if s.cursor < len(s.delegateTo) {
		name := s.delegateTo[s.cursor]
		if !s.needArgs {
			s.needArgs = true
			time.Sleep(35 * time.Millisecond)
			return agent.Delta{Kind: agent.DeltaToolCallStart, Index: s.cursor, ToolCallID: "call-" + name, ToolName: name}, nil
		}
		s.needArgs = false
		idx := s.cursor
		s.cursor++
		return agent.Delta{Kind: agent.DeltaToolCallArgs, Index: idx, Text: fmt.Sprintf(`{"task":%q}`, s.personaTask(name))}, nil
	}
	if !s.usageSent {
		s.usageSent = true
		return agent.Delta{Kind: agent.DeltaUsage, Usage: &agent.Usage{InputTokens: 1200, OutputTokens: 48}}, nil
	}
	if !s.done {
		s.done = true
		return agent.Delta{Kind: agent.DeltaFinish, FinishReason: "tool_calls"}, nil
	}
	return agent.Delta{}, io.EOF
}

func (s *demoStream) Close() error { return nil }
