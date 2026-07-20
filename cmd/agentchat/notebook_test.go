package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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
	m := newNotebookModel(nil, nil)
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
	m := newNotebookModel(nil, nil)
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
	m := newNotebookModel(nil, nil)
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
	m := newNotebookModel(nil, nil)
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
	m := newNotebookModel(nil, nil)
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
