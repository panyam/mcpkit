package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/panyam/mcpkit/agent/host"
)

// The notebook surface (--ui notebook) is the opt-in alt-screen alternative to
// the default inline TUI. It trades the inline surface's native terminal scroll
// and copy/paste for what only a managed alt-screen can offer: a viewport with
// its own scroll (fixing the inline-mode scrollback limitation), and a
// transcript of collapsible cells. Like tui.go it is presentation only — every
// action routes through the App (Dispatch / RunTurn / Commands).

// nbCellMsg appends a finished cell (a boundary segment: an assistant turn, a
// command result, an info line) to the notebook.
type nbCellMsg nbCell

// nbLiveMsg carries the growing in-flight turn (the not-yet-finished assistant
// cell) so it renders live at the bottom.
type nbLiveMsg string

// nbCell is one collapsible block in the transcript.
type nbCell struct {
	label     string // "you" / "assistant" / "command" / "info" / "error"
	body      string
	collapsed bool
}

// nbObserver is the host.Observer for notebook mode. It reuses the terminal
// renderer (so cell bodies format identically to the other surfaces) by
// writing into a buffer, then splits the stream into cells at HostEvent
// boundaries: the streaming turn (HostRunnerEvent) accumulates and renders live
// until a non-runner event closes it into a labeled cell.
type nbObserver struct {
	mu   sync.Mutex
	buf  *bytes.Buffer
	term host.Observer
	prog *tea.Program
}

func newNBObserver() *nbObserver {
	buf := &bytes.Buffer{}
	return &nbObserver{buf: buf, term: host.NewTerminalRenderer(buf)}
}

func (s *nbObserver) On(ev host.HostEvent) {
	s.mu.Lock()
	s.term.On(ev)
	segment := s.buf.String()
	boundary := isBoundary(ev.Kind)
	if boundary {
		s.buf.Reset()
	}
	prog := s.prog
	s.mu.Unlock()
	if prog == nil {
		return
	}
	if boundary {
		prog.Send(nbCellMsg{label: nbLabelFor(ev.Kind), body: strings.TrimRight(segment, "\n")})
	} else {
		prog.Send(nbLiveMsg(strings.TrimRight(segment, "\n")))
	}
}

// nbLabelFor maps a boundary event kind to a cell label. The streaming turn
// closes on HostTurnDone, so that is the "assistant" cell; failures are their
// own error cell; commands and the rest are informational.
func nbLabelFor(k host.HostEventKind) string {
	switch k {
	case host.HostTurnDone:
		return "assistant"
	case host.HostTurnFailed:
		return "error"
	case host.HostCommandResult:
		return "command"
	default:
		return "info"
	}
}

// notebookModel is the alt-screen bubbletea model: a scrollable viewport of
// cells above an input, with two modes. Insert mode (default) types into the
// input; Nav mode (Esc) moves a selection cursor over cells and folds them.
type notebookModel struct {
	app     *host.App
	surface *nbObserver

	vp    viewport.Model
	ta    textarea.Model
	cells []nbCell
	live  string // the in-flight turn, always shown expanded at the bottom

	nav      bool // false = insert, true = nav
	sel      int  // selected cell index in nav mode
	running  bool
	atBottom bool // whether the viewport is scrolled to the end (for auto-follow)

	history []string
	histIdx int

	width, height int
	ready         bool
}

func newNotebookModel(app *host.App, surface *nbObserver) notebookModel {
	m := notebookModel{app: app, surface: surface, ta: newPromptArea(), atBottom: true}
	m.histIdx = 0
	return m
}

func (m notebookModel) Init() tea.Cmd { return textarea.Blink }

// chromeHeight is the rows the input + status bar consume; the viewport gets
// the rest.
const chromeHeight = 4

func (m *notebookModel) layout() {
	m.ta.SetWidth(m.width)
	vpH := m.height - chromeHeight
	if vpH < 1 {
		vpH = 1
	}
	if !m.ready {
		m.vp = viewport.New(m.width, vpH)
		m.ready = true
	} else {
		m.vp.Width = m.width
		m.vp.Height = vpH
	}
}

func (m notebookModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		m.refresh()
		return m, nil

	case nbLiveMsg:
		m.live = string(msg)
		m.refresh()
		return m, nil

	case nbCellMsg:
		if msg.body != "" {
			m.cells = append(m.cells, nbCell(msg))
		}
		m.live = ""
		m.refresh()
		return m, nil

	case turnDoneMsg:
		m.running = false
		return m, nil

	case tea.MouseMsg:
		m.vp, _ = m.vp.Update(msg)
		m.atBottom = m.vp.AtBottom()
		return m, nil

	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}
		if m.nav {
			return m.updateNav(msg)
		}
		return m.updateInsert(msg)
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m, cmd
}

// updateNav handles keys while the selection cursor is active: move the
// selection, fold/unfold, jump to ends, or return to insert mode.
func (m notebookModel) updateNav(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "i":
		m.nav = false
	case "up", "k":
		if m.sel > 0 {
			m.sel--
		}
	case "down", "j":
		if m.sel < len(m.cells)-1 {
			m.sel++
		}
	case "g":
		m.sel = 0
	case "G":
		m.sel = len(m.cells) - 1
	case " ", "enter":
		if m.sel >= 0 && m.sel < len(m.cells) {
			m.cells[m.sel].collapsed = !m.cells[m.sel].collapsed
		}
	case "pgup":
		m.vp.HalfPageUp()
	case "pgdown":
		m.vp.HalfPageDown()
	}
	m.refresh()
	return m, nil
}

// updateInsert handles keys while typing: submit, complete, history, scroll the
// viewport, or enter nav mode; anything else edits the input.
func (m notebookModel) updateInsert(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		if len(m.cells) > 0 {
			m.nav = true
			m.sel = len(m.cells) - 1
			m.refresh()
		}
		return m, nil
	case tea.KeyEnter:
		if m.running {
			return m, nil
		}
		line := strings.TrimSpace(m.ta.Value())
		if line == "" {
			return m, nil
		}
		m.ta.Reset()
		m.history = append(m.history, line)
		m.histIdx = len(m.history)
		return m.submit(line)
	case tea.KeyTab:
		tabComplete(&m.ta, m.app)
		return m, nil
	case tea.KeyPgUp, tea.KeyPgDown:
		m.vp, _ = m.vp.Update(msg)
		m.atBottom = m.vp.AtBottom()
		return m, nil
	case tea.KeyUp:
		if m.recallHistory(-1) {
			return m, nil
		}
	case tea.KeyDown:
		if m.recallHistory(1) {
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m, cmd
}

func (m notebookModel) View() string {
	if !m.ready {
		return "starting…"
	}
	return m.vp.View() + "\n" + m.statusBar() + "\n" + m.ta.View()
}

func (m notebookModel) statusBar() string {
	var hint string
	if m.nav {
		hint = "NAV  ↑↓/jk select · space fold · g/G ends · esc/i insert"
	} else {
		hint = "INS  esc nav · pgup/pgdn scroll · /keys editing · enter send"
	}
	if m.running {
		hint = "working…  " + hint
	}
	return lipgloss.NewStyle().Faint(true).Render(hint)
}

// refresh rebuilds the viewport content from the cells (+ the live turn) and,
// when the view was already at the bottom, follows the new content down.
func (m *notebookModel) refresh() {
	if !m.ready {
		return
	}
	follow := m.atBottom
	m.vp.SetContent(m.renderCells())
	if follow {
		m.vp.GotoBottom()
		m.atBottom = true
	}
}

// renderCells lays out every cell (a fold marker + label header, and the
// indented body unless collapsed), highlighting the selected cell in nav mode,
// then appends the in-flight turn.
func (m notebookModel) renderCells() string {
	var b strings.Builder
	for i, c := range m.cells {
		marker := "▾"
		if c.collapsed {
			marker = "▸"
		}
		header := marker + " " + c.label
		if c.collapsed {
			header += " · " + snippet(firstLine(c.body), 70)
		}
		if m.nav && i == m.sel {
			header = lipgloss.NewStyle().Reverse(true).Render(header)
		}
		b.WriteString(header)
		b.WriteString("\n")
		if !c.collapsed && c.body != "" {
			b.WriteString(indentBlock(c.body))
			b.WriteString("\n")
		}
	}
	if m.live != "" {
		b.WriteString("▾ assistant\n")
		b.WriteString(indentBlock(m.live))
	}
	return b.String()
}

func indentBlock(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// submit adds the user's line as a cell and runs it (command or turn) on a
// goroutine, mirroring the inline TUI. /keys is a terminal-only cheatsheet.
func (m notebookModel) submit(line string) (tea.Model, tea.Cmd) {
	if line == "/keys" {
		m.cells = append(m.cells, nbCell{label: "info", body: keyHelp()})
		m.refresh()
		return m, nil
	}
	m.cells = append(m.cells, nbCell{label: "you", body: line})
	m.refresh()
	m.running = true
	app, surface := m.app, m.surface
	ctx := context.Background()
	if strings.HasPrefix(line, "/") {
		go func() {
			res, err := app.Dispatch(ctx, line)
			switch {
			case errors.Is(err, host.ErrUnknownCommand):
				surface.On(host.HostEvent{Kind: host.HostTurnFailed, Err: "unknown command " + line})
			case err != nil:
				surface.On(host.HostEvent{Kind: host.HostTurnFailed, Err: err.Error()})
			default:
				surface.On(host.HostEvent{Kind: host.HostCommandResult, Command: res})
				if res.Quit {
					surface.prog.Quit()
					return
				}
			}
			surface.prog.Send(turnDoneMsg{})
		}()
	} else {
		go func() {
			_ = app.RunTurn(ctx, line)
			surface.prog.Send(turnDoneMsg{})
		}()
	}
	return m, nil
}

// recallHistory mirrors the inline TUI's history recall for the notebook input.
func (m *notebookModel) recallHistory(dir int) bool {
	if len(m.history) == 0 || strings.Contains(m.ta.Value(), "\n") {
		return false
	}
	idx := m.histIdx + dir
	if idx < 0 || idx > len(m.history) {
		return false
	}
	m.histIdx = idx
	if idx == len(m.history) {
		m.ta.Reset()
	} else {
		m.ta.SetValue(m.history[idx])
		m.ta.CursorEnd()
	}
	return true
}

// runNotebook starts the alt-screen notebook program with mouse support.
func runNotebook(app *host.App, surface *nbObserver) error {
	prog := tea.NewProgram(newNotebookModel(app, surface), tea.WithAltScreen(), tea.WithMouseCellMotion())
	surface.prog = prog
	_, err := prog.Run()
	return err
}
