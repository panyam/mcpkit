package main

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
)

// appKeys centralizes the surface-level key bindings — the ones the models act
// on before handing the event to the textarea. The one-line help bar, the
// notebook status hints, the `?` full-help view, and the /keys cheatsheet all
// render from these bindings, so the documented keys can never drift from the
// handled keys (issue 1063 C2). The inline surface matches events against these
// bindings directly (key.Matches), making them the single source of truth for
// both behavior and help.
type appKeys struct {
	Send     key.Binding
	Newline  key.Binding
	Complete key.Binding
	History  key.Binding
	Help     key.Binding
	Quit     key.Binding

	// notebook NAV mode
	Select key.Binding
	Fold   key.Binding
	Ends   key.Binding
	Insert key.Binding
	Nav    key.Binding
	Scroll key.Binding
}

func newAppKeys() appKeys {
	return appKeys{
		Send:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
		Newline:  key.NewBinding(key.WithKeys("ctrl+j"), key.WithHelp("ctrl+j", "newline")),
		Complete: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "complete")),
		History:  key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "history")),
		Help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:     key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),

		Select: key.NewBinding(key.WithKeys("up", "down", "k", "j"), key.WithHelp("↑↓/jk", "select")),
		Fold:   key.NewBinding(key.WithKeys(" ", "enter"), key.WithHelp("space", "fold")),
		Ends:   key.NewBinding(key.WithKeys("g", "G"), key.WithHelp("g/G", "ends")),
		Insert: key.NewBinding(key.WithKeys("esc", "i"), key.WithHelp("esc/i", "type")),
		Nav:    key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "select cells")),
		Scroll: key.NewBinding(key.WithKeys("pgup", "pgdown"), key.WithHelp("pgup/dn", "scroll")),
	}
}

// insertBar is the one-line help shown while typing (inline surface + notebook
// INS mode); navBar is the notebook NAV mode line.
func (k appKeys) insertBar() []key.Binding {
	return []key.Binding{k.Send, k.Complete, k.History, k.Help, k.Quit}
}

func (k appKeys) navBar() []key.Binding {
	return []key.Binding{k.Select, k.Fold, k.Ends, k.Insert, k.Help}
}

// fullHelp groups every surface binding for the `?` view.
func (k appKeys) fullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Send, k.Newline, k.Complete},
		{k.History, k.Help, k.Quit},
		{k.Nav, k.Select, k.Fold, k.Ends, k.Insert, k.Scroll},
	}
}

// renderKeyHelp is the /keys cheatsheet. The surface-keys section is generated
// from the bindings above (so it never drifts); the prompt-editing section is
// the readline keys the bubbles textarea owns, whose descriptions the textarea
// does not carry, so they stay hand-written here.
func renderKeyHelp() string {
	var b strings.Builder
	b.WriteString("Surface keys:\n")
	for _, grp := range newAppKeys().fullHelp() {
		for _, bnd := range grp {
			h := bnd.Help()
			b.WriteString("  " + padRight(h.Key, 12) + h.Desc + "\n")
		}
	}
	b.WriteString("\nPrompt editing (readline):\n")
	b.WriteString(strings.Join([]string{
		"  ← / →       char back / forward",
		"  ↑ / ↓       recall history on a single-line prompt; move line to line in a multi-line prompt",
		"  ctrl+← / →  word back / forward",
		"  ctrl+a / e  start / end of line   (also Home / End)",
		"  ctrl+w      delete previous word",
		"  ctrl+k / u  delete to end / start of line",
		"  ctrl+j      insert a newline (also shift+enter / alt+enter)",
		"  With Option-as-Meta on: alt+←/→ or alt+b/f word nav, alt+d delete-word-forward.",
	}, "\n"))
	b.WriteString("\n\nNotebook modes (--ui notebook):\n")
	b.WriteString(strings.Join([]string{
		"  INS   typing (default). esc enters NAV.",
		"  NAV   cell selection. i or esc returns to INS; the NAV keys above",
		"        (↑↓/jk select, space fold, g/G ends, pgup/dn scroll) act only here.",
		"  The inline surface (--ui tui) has no modes; it is always in INS.",
	}, "\n"))
	return b.String()
}

func padRight(s string, n int) string {
	if len([]rune(s)) >= n {
		return s + " "
	}
	return s + strings.Repeat(" ", n-len([]rune(s)))
}
