package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

// renderer maps the agent event stream onto the terminal, line-based so
// interleaved tool events from parallel calls stay readable. ANSI styling is
// suppressed with --plain or when NO_COLOR is set.
type renderer struct {
	out      io.Writer
	plain    bool
	thinking bool
	midText  bool
}

func newRenderer(out io.Writer) *renderer {
	return &renderer{out: out, plain: os.Getenv("NO_COLOR") != ""}
}

func (r *renderer) dim(s string) string {
	if r.plain {
		return s
	}
	return "\x1b[2m" + s + "\x1b[0m"
}

// handle is the Runner emit callback.
func (r *renderer) handle(e agent.Event) {
	switch e.Kind {
	case agent.EventThinkingBegin:
		r.breakLine()
		fmt.Fprint(r.out, r.dim("· thinking "))
	case agent.EventThinkingDelta:
		fmt.Fprint(r.out, r.dim("."))
	case agent.EventThinkingEnd:
		fmt.Fprintln(r.out)
	case agent.EventTextDelta:
		r.thinking = false
		r.midText = true
		fmt.Fprint(r.out, e.Text)
	case agent.EventToolBegin:
		r.breakLine()
		fmt.Fprintf(r.out, "%s\n", r.dim("⚙ "+e.ToolCall.Name+"("+compactJSON(e.ToolCall.Args)+")"))
	case agent.EventToolEnd:
		status := "✓"
		if e.ToolResult != nil && e.ToolResult.IsError {
			status = "✗"
		}
		fmt.Fprintf(r.out, "%s\n", r.dim("  "+status+" "+e.ToolCall.Name+": "+snippet(resultText(e.ToolResult), 100)))
	case agent.EventToolError:
		fmt.Fprintf(r.out, "%s\n", r.dim("  ✗ "+e.ToolCall.Name+" failed: "+snippet(e.Error, 120)))
	case agent.EventError:
		r.breakLine()
	}
}

func (r *renderer) breakLine() {
	if r.midText {
		fmt.Fprintln(r.out)
		r.midText = false
	}
}

func (r *renderer) turnDone(res *agent.TurnResult) {
	r.breakLine()
	fmt.Fprintf(r.out, "%s\n", r.dim(fmt.Sprintf("— %d step(s), %d in / %d out tokens", res.Steps, res.Usage.InputTokens, res.Usage.OutputTokens)))
}

func (r *renderer) turnFailed(err error) {
	r.breakLine()
	fmt.Fprintf(r.out, "%s\n", "error: "+err.Error())
}

func (r *renderer) prompt() {
	fmt.Fprint(r.out, "> ")
}

func (r *renderer) toolList(defs []core.ToolDef) {
	for _, d := range defs {
		fmt.Fprintf(r.out, "  %-28s %s\n", d.Name, snippet(d.Description, 80))
	}
	fmt.Fprintf(r.out, "%s\n", r.dim(fmt.Sprintf("— %d tool(s)", len(defs))))
}

func (r *renderer) history(msgs []agent.Message) {
	for _, m := range msgs {
		text := m.Text
		if text == "" && len(m.ToolCalls) > 0 {
			text = fmt.Sprintf("(%d tool call(s))", len(m.ToolCalls))
		}
		fmt.Fprintf(r.out, "  [%s] %s\n", m.Role, snippet(text, 100))
	}
}

func compactJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return snippet(buf.String(), 80)
}

func resultText(res *core.ToolResult) string {
	if res == nil {
		return ""
	}
	var parts []string
	for _, c := range res.Content {
		if c.Type == "text" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, " ")
}

func snippet(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
