package host

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/client"
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
	case agent.EventToolDenied:
		fmt.Fprintf(r.out, "%s\n", r.dim("  ⃠ "+e.ToolCall.Name+" not permitted: "+snippet(e.Reason, 120)))
	case agent.EventToolCancelled:
		fmt.Fprintf(r.out, "%s\n", r.dim("  ◼ "+e.ToolCall.Name+" cancelled: "+snippet(e.Reason, 120)))
	case agent.EventError:
		r.breakLine()
	}
}

// approvalMode reports the host's current approval disposition (the /approve
// command). A nil policy means the gate is off (every call runs).
func (r *renderer) approvalMode(p *agent.TieredApproval) {
	if p == nil {
		fmt.Fprintf(r.out, "%s\n", r.dim("approval: off (every tool call runs)"))
		return
	}
	fmt.Fprintf(r.out, "%s\n", r.dim("approval: "+approvalModeName(p.DefaultMode())))
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

func compactJSON(raw core.RawJSON) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw.Raw()); err != nil {
		return string(raw.Raw())
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

func (r *renderer) skillsLoaded(serverID string, ok, skipped int) {
	line := fmt.Sprintf("skills: %d loaded from %s", ok, serverID)
	if skipped > 0 {
		line += fmt.Sprintf(", %d skipped", skipped)
	}
	fmt.Fprintf(r.out, "%s\n", r.dim(line))
}

func (r *renderer) skillSkipped(serverID, uri string, err error) {
	fmt.Fprintf(r.out, "warning: skill %s from %s not injected: %v\n", uri, serverID, err)
}

func (r *renderer) health(f *agent.FailoverProvider) {
	if f == nil {
		fmt.Fprintf(r.out, "%s\n", r.dim("health: single provider, no failover configured"))
		return
	}
	h := f.Health()
	line := fmt.Sprintf("health: active=%s consecutive_failures=%d", h.Active, h.ConsecutiveFailures)
	if h.LastError != "" {
		line += " last_error=" + snippet(h.LastError, 80)
	}
	fmt.Fprintf(r.out, "%s\n", r.dim(line))
}

func (r *renderer) triggerFired(label string) {
	fmt.Fprintf(r.out, "\n%s\n", r.dim("· trigger: "+label))
}

func (r *renderer) eventDropped(serverID, name string) {
	fmt.Fprintf(r.out, "%s\n", r.dim("warning: event buffer full, dropped "+name+" from "+serverID))
}

func (r *renderer) taskStatus(dt *core.DetailedTask) {
	if dt.Status == core.TaskWorking {
		return // polling noise; begin/end and pauses are the signal
	}
	fmt.Fprintf(r.out, "%s\n", r.dim("  · task "+dt.TaskID+": "+string(dt.Status)))
}

func (r *renderer) taskDetached(bt *client.BackgroundTask) {
	fmt.Fprintf(r.out, "%s\n", r.dim("· task "+bt.TaskID+" ("+bt.Tool+") moved to background; /tasks to manage"))
}

func (r *renderer) taskCompleted(bt *client.BackgroundTask) {
	dt, err := bt.Result()
	switch {
	case err != nil:
		fmt.Fprintf(r.out, "%s\n", r.dim("· task "+bt.TaskID+" ("+bt.Tool+") ended: "+snippet(err.Error(), 80)))
	case dt != nil && dt.Status == core.TaskFailed && dt.Error != nil:
		fmt.Fprintf(r.out, "%s\n", r.dim("· task "+bt.TaskID+" ("+bt.Tool+") failed: "+snippet(dt.Error.Message, 80)))
	case dt != nil && dt.Result != nil:
		fmt.Fprintf(r.out, "%s\n", r.dim("· task "+bt.TaskID+" ("+bt.Tool+") completed: "+snippet(resultText(dt.Result), 80)))
	default:
		fmt.Fprintf(r.out, "%s\n", r.dim("· task "+bt.TaskID+" ("+bt.Tool+") "+string(dt.Status)))
	}
}

func (r *renderer) taskList(tasks []*client.BackgroundTask) {
	if len(tasks) == 0 {
		fmt.Fprintf(r.out, "%s\n", r.dim("no background tasks"))
		return
	}
	for _, bt := range tasks {
		fmt.Fprintf(r.out, "  %-12s %-20s %-16s %s\n", bt.TaskID, bt.Tool, bt.Status(), time.Since(bt.StartedAt).Round(time.Second))
	}
}

func (r *renderer) session(runID string) {
	if runID == "" {
		fmt.Fprintf(r.out, "%s\n", r.dim("session: persistence off (no RunStore configured or no turn yet)"))
		return
	}
	fmt.Fprintf(r.out, "%s\n", r.dim("session: "+runID))
}

func (r *renderer) sessionWarn(err error) {
	fmt.Fprintf(r.out, "%s\n", r.dim("session: persistence degraded: "+err.Error()))
}
