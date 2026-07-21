package main

import (
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/agent/host"
)

func send(m notebookModel, msg tea.Msg) notebookModel {
	nm, _ := m.Update(msg)
	return nm.(notebookModel)
}

func TestNBLabelFor(t *testing.T) {
	cases := map[host.HostEventKind]string{
		host.HostTurnDone:      "assistant",
		host.HostTurnFailed:    "error",
		host.HostCommandResult: "command",
		host.HostSkillsLoaded:  "info",
	}
	for k, want := range cases {
		if got := nbLabelFor(k); got != want {
			t.Errorf("nbLabelFor(%v) = %q, want %q", k, got, want)
		}
	}
}

func TestNotebook_CellAppendedAndRendered(t *testing.T) {
	m := newNotebookModel(nil, nil, 20)
	m = send(m, nbCellMsg{label: "assistant", body: "hi\nthere"})
	if len(m.cells) != 1 {
		t.Fatalf("cells = %d, want 1", len(m.cells))
	}
	out := m.renderCells()
	if !strings.Contains(out, "▾ assistant") {
		t.Fatalf("expanded header missing:\n%s", out)
	}
	if !strings.Contains(out, "  hi") || !strings.Contains(out, "  there") {
		t.Fatalf("body not indented:\n%s", out)
	}
}

func TestNotebook_FoldToggleInNav(t *testing.T) {
	m := newNotebookModel(nil, nil, 20)
	m = send(m, nbCellMsg{label: "assistant", body: "long answer here"})
	// Esc enters nav mode selecting the last cell.
	m = send(m, tea.KeyMsg{Type: tea.KeyEsc})
	if !m.nav || m.sel != 0 {
		t.Fatalf("esc did not enter nav on last cell: nav=%v sel=%d", m.nav, m.sel)
	}
	// space folds the selected cell.
	m = send(m, tea.KeyMsg{Type: tea.KeySpace})
	if !m.cells[0].collapsed {
		t.Fatal("space did not collapse the selected cell")
	}
	out := m.renderCells()
	if !strings.Contains(out, "▸ assistant · long answer here") {
		t.Fatalf("collapsed header/snippet wrong:\n%s", out)
	}
	// esc/i returns to insert.
	m = send(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	if m.nav {
		t.Fatal("i did not return to insert mode")
	}
}

func TestNotebook_KeysCommandAddsInfoCell(t *testing.T) {
	m := newNotebookModel(nil, nil, 20)
	nm, _ := m.submit("/keys")
	m = nm.(notebookModel)
	if len(m.cells) != 1 || m.cells[0].label != "info" {
		t.Fatalf("/keys did not add an info cell: %+v", m.cells)
	}
	if !strings.Contains(m.cells[0].body, "ctrl+w") {
		t.Fatalf("/keys cell body is not the cheatsheet:\n%s", m.cells[0].body)
	}
}

func TestNotebook_LiveRendersAtBottom(t *testing.T) {
	m := newNotebookModel(nil, nil, 20)
	m = send(m, nbLiveMsg("streaming answer"))
	out := m.renderCells()
	if !strings.Contains(out, "▾ assistant") || !strings.Contains(out, "  streaming answer") {
		t.Fatalf("live turn not rendered:\n%s", out)
	}
}

func TestUIMode(t *testing.T) {
	for flag, want := range map[string]string{"plain": "plain", "tui": "tui", "notebook": "notebook"} {
		if got := uiMode(flag); got != want {
			t.Errorf("uiMode(%q) = %q, want %q", flag, got, want)
		}
	}
	// auto under test (stdout not a char device) resolves to plain
	if got := uiMode("auto"); got != "plain" {
		t.Errorf("uiMode(auto) under test = %q, want plain", got)
	}
}

func TestNotebook_EnterSubmitsAndViewRenders(t *testing.T) {
	m := newNotebookModel(nil, nil, 20)
	m = send(m, tea.WindowSizeMsg{Width: 80, Height: 24}) // sets ready + viewport size
	m.ta.SetValue("/keys")
	m = send(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.cells) != 1 || m.cells[0].label != "info" {
		t.Fatalf("Enter did not submit /keys into a cell: %+v", m.cells)
	}
	if !strings.Contains(m.View(), "delete previous word") {
		t.Fatalf("submitted cheatsheet not visible in View():\n%s", m.View())
	}
}

func TestNotebook_UpArrowScrollsWhenNoHistory(t *testing.T) {
	m := newNotebookModel(nil, nil, 20)
	m = send(m, tea.WindowSizeMsg{Width: 60, Height: 8}) // small viewport (~4 rows)
	// add enough content to overflow the viewport
	for i := 0; i < 6; i++ {
		m = send(m, nbCellMsg{label: "assistant", body: "line one\nline two\nline three"})
	}
	if !m.atBottom {
		t.Fatal("should auto-follow to bottom after new cells")
	}
	// up-arrow with no history scrolls the transcript up (off the bottom)
	m = send(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.atBottom {
		t.Fatal("up-arrow did not scroll the transcript (still at bottom)")
	}
}

func TestNotebook_RuleBetweenCells(t *testing.T) {
	m := newNotebookModel(nil, nil, 20)
	m = send(m, tea.WindowSizeMsg{Width: 40, Height: 20})
	m = send(m, nbCellMsg{label: "you", body: "hi"})
	if strings.Contains(m.renderCells(), "─") {
		t.Fatal("a single cell should have no delimiter")
	}
	m = send(m, nbCellMsg{label: "assistant", body: "hello"})
	if !strings.Contains(m.renderCells(), "─") {
		t.Fatalf("two cells should have a delimiter between them:\n%s", m.renderCells())
	}
}

func TestPromptArea_NewlineOffEnter(t *testing.T) {
	keys := newPromptArea().KeyMap.InsertNewline.Keys()
	if !slices.Contains(keys, "ctrl+j") {
		t.Fatalf("InsertNewline keys = %v, want ctrl+j", keys)
	}
	if slices.Contains(keys, "enter") {
		t.Fatalf("InsertNewline still on enter (would break submit): %v", keys)
	}
}

func TestNotebook_CtrlJInsertsNewline(t *testing.T) {
	m := newNotebookModel(nil, nil, 20)
	m = send(m, tea.WindowSizeMsg{Width: 40, Height: 20})
	m.ta.SetValue("abc")
	m.ta.CursorEnd()
	m = send(m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if !strings.Contains(m.ta.Value(), "\n") {
		t.Fatalf("ctrl+j did not insert a newline: %q", m.ta.Value())
	}
}

func TestNotebook_UpMovesWithinPromptFirst(t *testing.T) {
	m := newNotebookModel(nil, nil, 20)
	m = send(m, tea.WindowSizeMsg{Width: 40, Height: 20})
	m.ta.SetValue("line1\nline2")
	m.ta.CursorEnd()
	if m.ta.Line() != 1 {
		t.Fatalf("setup: cursor row = %d, want 1", m.ta.Line())
	}
	m = send(m, tea.KeyMsg{Type: tea.KeyUp}) // moves within prompt, not history/scroll
	if m.ta.Line() != 0 {
		t.Fatalf("up did not move the cursor up within the prompt: row = %d", m.ta.Line())
	}
}

func TestNotebook_PromptAutoGrowsAndClamps(t *testing.T) {
	m := newNotebookModel(nil, nil, 4) // maxLines = 4
	m = send(m, tea.WindowSizeMsg{Width: 40, Height: 30})
	if h := m.ta.Height(); h != 1 {
		t.Fatalf("empty prompt height = %d, want 1", h)
	}
	// add newlines with ctrl+j; the box grows with line count
	m = send(m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = send(m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if h := m.ta.Height(); h != 3 {
		t.Fatalf("after 2 newlines height = %d, want 3", h)
	}
	// grow past the cap — clamps at maxLines
	for i := 0; i < 10; i++ {
		m = send(m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	}
	if h := m.ta.Height(); h != 4 {
		t.Fatalf("height should clamp at maxLines=4, got %d", h)
	}
	// clearing shrinks it back
	m.ta.SetValue("")
	m = send(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if h := m.ta.Height(); h != 1 {
		t.Fatalf("after clearing, height = %d, want 1", h)
	}
}

func TestNotebook_FirstPromptLineStaysVisible(t *testing.T) {
	m := newNotebookModel(nil, nil, 20)
	m = send(m, tea.WindowSizeMsg{Width: 40, Height: 30})
	typ := func(s string) { m = send(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}) }
	nl := func() { m = send(m, tea.KeyMsg{Type: tea.KeyCtrlJ}) }
	typ("FIRSTLINE")
	nl()
	typ("second")
	nl()
	typ("third")
	// within the maxLines budget, the first line must not have scrolled off
	if !strings.Contains(m.ta.View(), "FIRSTLINE") {
		t.Fatalf("first prompt line scrolled off within budget:\n%s", m.ta.View())
	}
}

func nbCollectCells(evs ...host.HostEvent) []nbCell {
	obs := newNBObserver()
	var cells []nbCell
	for _, ev := range evs {
		for _, m := range obs.render(ev) {
			if c, ok := m.(nbCellMsg); ok {
				cells = append(cells, nbCell(c))
			}
		}
	}
	return cells
}

func runnerEv(e agent.Event) host.HostEvent {
	return host.HostEvent{Kind: host.HostRunnerEvent, RunnerEvent: e}
}

func turnDoneEv() host.HostEvent {
	return host.HostEvent{Kind: host.HostTurnDone, Result: &agent.TurnResult{Steps: 1}}
}

func TestNBObserver_ToolBecomesOwnCell(t *testing.T) {
	cells := nbCollectCells(
		runnerEv(agent.Event{Kind: agent.EventTextDelta, Text: "greeting you"}),
		runnerEv(agent.Event{Kind: agent.EventToolBegin, ToolCall: &agent.ToolCall{Name: "greet"}}),
		runnerEv(agent.Event{Kind: agent.EventToolEnd, ToolCall: &agent.ToolCall{Name: "greet"}}),
		runnerEv(agent.Event{Kind: agent.EventTextDelta, Text: "all done"}),
		turnDoneEv(),
	)
	if len(cells) != 3 {
		t.Fatalf("want 3 cells (text / tool / text), got %d: %+v", len(cells), cells)
	}
	if cells[0].label != "assistant" || !strings.Contains(cells[0].body, "greeting you") {
		t.Fatalf("cell 0 = %+v, want assistant text-before-tool", cells[0])
	}
	if cells[1].label != "⚙ greet" || !strings.Contains(cells[1].body, "greet") {
		t.Fatalf("cell 1 = %+v, want its own ⚙ greet cell", cells[1])
	}
	if cells[2].label != "assistant" || !strings.Contains(cells[2].body, "all done") {
		t.Fatalf("cell 2 = %+v, want trailing assistant cell", cells[2])
	}
}

func TestNBObserver_TextOnlyTurnIsOneCell(t *testing.T) {
	cells := nbCollectCells(
		runnerEv(agent.Event{Kind: agent.EventTextDelta, Text: "just a plain answer"}),
		turnDoneEv(),
	)
	if len(cells) != 1 || cells[0].label != "assistant" {
		t.Fatalf("text-only turn should be one assistant cell, got %+v", cells)
	}
}

func TestNBObserver_ParallelToolsGroupIntoOneCell(t *testing.T) {
	cells := nbCollectCells(
		runnerEv(agent.Event{Kind: agent.EventToolBegin, ToolCall: &agent.ToolCall{Name: "A"}}),
		runnerEv(agent.Event{Kind: agent.EventToolBegin, ToolCall: &agent.ToolCall{Name: "B"}}),
		runnerEv(agent.Event{Kind: agent.EventToolEnd, ToolCall: &agent.ToolCall{Name: "A"}}),
		runnerEv(agent.Event{Kind: agent.EventToolEnd, ToolCall: &agent.ToolCall{Name: "B"}}),
		turnDoneEv(),
	)
	var tool *nbCell
	for i := range cells {
		if strings.HasPrefix(cells[i].label, "⚙") {
			tool = &cells[i]
		}
	}
	if tool == nil || tool.label != "⚙ tools" {
		t.Fatalf("parallel tools should be one '⚙ tools' cell, got %+v", cells)
	}
	if !strings.Contains(tool.body, "A") || !strings.Contains(tool.body, "B") {
		t.Fatalf("batch cell should contain both tools: %q", tool.body)
	}
}
