package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// TestKeys_MatchHandledKeys pins the bindings to the literal keys the models
// act on, so a rewire that changes behavior also changes the help.
func TestKeys_MatchHandledKeys(t *testing.T) {
	k := newAppKeys()
	cases := []struct {
		name string
		msg  tea.KeyMsg
		bind key.Binding
	}{
		{"send", tea.KeyMsg{Type: tea.KeyEnter}, k.Send},
		{"complete", tea.KeyMsg{Type: tea.KeyTab}, k.Complete},
		{"quit", tea.KeyMsg{Type: tea.KeyCtrlC}, k.Quit},
		{"help", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")}, k.Help},
		{"history-up", tea.KeyMsg{Type: tea.KeyUp}, k.History},
		{"fold", tea.KeyMsg{Type: tea.KeySpace}, k.Fold},
	}
	for _, c := range cases {
		if !key.Matches(c.msg, c.bind) {
			t.Errorf("%s: key.Matches(%q) = false", c.name, c.msg.String())
		}
	}
}

// TestKeys_HelpBarsHaveText guards that every binding shown in a help bar
// carries a key + description, so nothing renders blank.
func TestKeys_HelpBarsHaveText(t *testing.T) {
	k := newAppKeys()
	for _, group := range [][]key.Binding{k.insertBar(), k.navBar()} {
		for _, b := range group {
			h := b.Help()
			if h.Key == "" || h.Desc == "" {
				t.Errorf("help bar binding missing text: key=%q desc=%q", h.Key, h.Desc)
			}
		}
	}
}

// TestRenderKeyHelp_NoDriftFromBindings is the C2 contract: the /keys cheatsheet
// is generated from the same bindings, so every surface binding's key literal
// appears in it. A binding added or renamed without updating help fails here.
func TestRenderKeyHelp_NoDriftFromBindings(t *testing.T) {
	out := renderKeyHelp()
	for _, group := range newAppKeys().fullHelp() {
		for _, b := range group {
			key := b.Help().Key
			if !strings.Contains(out, key) {
				t.Errorf("renderKeyHelp() missing binding key %q:\n%s", key, out)
			}
		}
	}
}

func TestTUI_QuestionMarkTogglesHelpOnlyWhenEmpty(t *testing.T) {
	qmark := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")}

	// empty prompt: `?` toggles the full-help view
	m := newTUIModel(nil, nil, 0)
	nm, _ := m.Update(qmark)
	m = nm.(tuiModel)
	if !m.showAll {
		t.Fatal("`?` on an empty prompt should toggle full help on")
	}
	nm, _ = m.Update(qmark)
	if nm.(tuiModel).showAll {
		t.Fatal("`?` again should toggle full help off")
	}

	// non-empty prompt: `?` is a literal character, help stays off
	m2 := newTUIModel(nil, nil, 0)
	m2.ta.SetValue("hi")
	nm2, _ := m2.Update(qmark)
	m2 = nm2.(tuiModel)
	if m2.showAll {
		t.Fatal("`?` while typing should not toggle help")
	}
	if !strings.Contains(m2.ta.Value(), "?") {
		t.Fatalf("`?` while typing should be inserted, got %q", m2.ta.Value())
	}
}

func TestNotebook_QuestionMarkTogglesHelp(t *testing.T) {
	qmark := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")}
	m := newNotebookModel(nil, nil, 20, 0)
	m = send(m, tea.WindowSizeMsg{Width: 40, Height: 20})

	// insert mode, empty prompt: toggles
	m = send(m, qmark)
	if !m.showAll {
		t.Fatal("`?` on empty prompt (INS) should toggle full help")
	}
	// nav mode: `?` always toggles (no typing there)
	m.showAll = false
	m = send(m, nbCellMsg{label: "you", body: "x"}) // give nav a cell to select
	m = send(m, tea.KeyMsg{Type: tea.KeyEsc})       // enter NAV
	if !m.nav {
		t.Fatal("esc should enter NAV")
	}
	m = send(m, qmark)
	if !m.showAll {
		t.Fatal("`?` in NAV should toggle full help")
	}
}
