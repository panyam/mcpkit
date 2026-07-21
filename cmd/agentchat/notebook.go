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

	"github.com/panyam/mcpkit/agent"
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

	// openTools/toolLabel track the current tool cell: a turn is split so each
	// tool call (or a parallel batch) folds on its own, with the text before/
	// after as separate assistant cells. openTools counts in-flight calls so
	// concurrent tool-begins group into one cell.
	openTools int
	toolLabel string
}

func newNBObserver() *nbObserver {
	buf := &bytes.Buffer{}
	return &nbObserver{buf: buf, term: host.NewTerminalRenderer(buf)}
}

func (s *nbObserver) On(ev host.HostEvent) {
	msgs := s.render(ev)
	s.mu.Lock()
	prog := s.prog
	s.mu.Unlock()
	if prog == nil {
		return
	}
	for _, m := range msgs {
		prog.Send(m)
	}
}

// render folds one HostEvent into the tea messages the model should receive
// (cells committed at boundaries, live updates in between). Separated from On
// so tests can assert the cell split without a running program.
func (s *nbObserver) render(ev host.HostEvent) []tea.Msg {
	s.mu.Lock()
	defer s.mu.Unlock()
	var msgs []tea.Msg
	seg := func() string { return strings.TrimRight(s.buf.String(), "\n") }

	if ev.Kind == host.HostRunnerEvent {
		re := ev.RunnerEvent
		// A tool-begin cuts the accumulated text into its own assistant cell
		// (the first begin of a batch), then the tool renders into a fresh
		// buffer that a matching tool-end flushes as a ⚙ cell.
		if re.Kind == agent.EventToolBegin {
			if s.openTools == 0 {
				if txt := seg(); txt != "" {
					msgs = append(msgs, nbCellMsg{label: "assistant", body: txt})
				}
				s.buf.Reset()
				s.toolLabel = "⚙"
				if re.ToolCall != nil {
					s.toolLabel = "⚙ " + re.ToolCall.Name
				}
			} else {
				s.toolLabel = "⚙ tools"
			}
			s.openTools++
		}

		s.term.On(ev) // render this event into buf

		switch re.Kind {
		case agent.EventToolEnd, agent.EventToolError, agent.EventToolDenied, agent.EventToolCancelled:
			if s.openTools > 0 {
				s.openTools--
			}
			if s.openTools == 0 {
				msgs = append(msgs, nbCellMsg{label: s.toolLabel, body: seg()})
				s.buf.Reset()
			} else {
				msgs = append(msgs, nbLiveMsg(seg()))
			}
		default:
			msgs = append(msgs, nbLiveMsg(seg()))
		}
	} else {
		// non-runner event = a discrete boundary (turn done, command, info)
		s.term.On(ev)
		msgs = append(msgs, nbCellMsg{label: nbLabelFor(ev.Kind), body: seg()})
		s.buf.Reset()
		if ev.Kind == host.HostTurnDone && ev.Result != nil {
			msgs = append(msgs, usageMsg{in: ev.Result.Usage.InputTokens, out: ev.Result.Usage.OutputTokens})
		}
	}
	return msgs
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

	maxLines      int      // input auto-grows up to this many rows (0 = default)
	usage         usageMsg // last turn's tokens, for the status line
	window        int      // context window for the "N% left" gauge (0 = off)
	session       string   // cached RunID for the status line (refreshed off the render path)
	width, height int
	ready         bool
}

// defaultNotebookMaxLines caps the auto-growing prompt when --notebook-max-lines
// is unset.
const defaultNotebookMaxLines = 20

func newNotebookModel(app *host.App, surface *nbObserver, maxLines, window int) notebookModel {
	if maxLines <= 0 {
		maxLines = defaultNotebookMaxLines
	}
	m := notebookModel{app: app, surface: surface, ta: newPromptArea(), atBottom: true, maxLines: maxLines, window: window}
	m.histIdx = 0
	if app != nil {
		// Safe here: construction runs before the first turn, so turnMu is free.
		m.session = app.RunID()
	}
	return m
}

func (m notebookModel) Init() tea.Cmd { return textarea.Blink }

// relayout recomputes the split between the input and the viewport. The input
// auto-grows with its line count (1 line when empty, up to maxLines, shrinking
// back), and the viewport takes the rest — so a long multi-line draft expands
// the prompt while a short one keeps the transcript tall.
func (m *notebookModel) relayout() {
	if m.width == 0 {
		return
	}
	ih := m.ta.LineCount()
	if ih < 1 {
		ih = 1
	}
	if ih > m.maxLines {
		ih = m.maxLines
	}
	m.ta.SetWidth(m.width)
	m.ta.SetHeight(ih)
	vpH := m.height - ih - 3 // 2-line status + separator
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
		m.relayout()
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
		// The turn (or dispatched command) has fully returned, so turnMu is
		// free and a session-changing command like /sessions has landed.
		if m.app != nil {
			m.session = m.app.RunID()
		}
		return m, nil

	case usageMsg:
		m.usage = msg
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
		// Give the input its full height budget before the edit so the
		// textarea's internal reposition only scrolls when content genuinely
		// exceeds maxLines. Otherwise a just-inserted newline transiently
		// over-scrolls (the old, smaller height is in effect during Update) and
		// hides line 1 even though the grown box has room. relayout then shrinks
		// the box back to the actual line count.
		m.ta.SetHeight(m.maxLines)
		nm, cmd := m.updateInsert(msg)
		n := nm.(notebookModel)
		n.relayout() // the input may have grown or shrunk a line
		n.refresh()
		return n, cmd
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
		// Move within the prompt lines first; only at the top line do up-arrow
		// history recall / transcript scroll kick in.
		if m.ta.Line() > 0 {
			break
		}
		if m.recallHistory(-1) {
			return m, nil
		}
		m.vp.ScrollUp(1)
		m.atBottom = m.vp.AtBottom()
		return m, nil
	case tea.KeyDown:
		if m.ta.Line() < m.ta.LineCount()-1 {
			break
		}
		if m.recallHistory(1) {
			return m, nil
		}
		m.vp.ScrollDown(1)
		m.atBottom = m.vp.AtBottom()
		return m, nil
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
		hint = "NAV  ↑↓/jk select · space fold · g/G ends · esc/i type"
	} else {
		hint = "INS  enter send · ctrl+j newline · ↑↓ scroll · esc fold"
	}
	if m.running {
		hint = "working…  " + hint
	}
	// Two-line status: the persistent model/session/context line, then keys.
	return lipgloss.NewStyle().Faint(true).Render(statusLine(m.app, m.session, m.usage, m.window) + "\n" + hint)
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
	rule := m.hrule()
	for i, c := range m.cells {
		if i > 0 {
			b.WriteString(rule)
			b.WriteString("\n")
		}
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
		if len(m.cells) > 0 {
			b.WriteString(rule)
			b.WriteString("\n")
		}
		b.WriteString("▾ assistant\n")
		b.WriteString(indentBlock(m.live))
	}
	return b.String()
}

// hrule is a faint full-width horizontal delimiter drawn between cells.
func (m notebookModel) hrule() string {
	w := m.vp.Width
	if w <= 0 {
		w = m.width
	}
	if w <= 0 {
		w = 40
	}
	return lipgloss.NewStyle().Faint(true).Render(strings.Repeat("─", w))
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
// maxLines caps the auto-growing prompt.
func runNotebook(app *host.App, surface *nbObserver, maxLines, window int) error {
	prog := tea.NewProgram(newNotebookModel(app, surface, maxLines, window), tea.WithAltScreen(), tea.WithMouseCellMotion())
	surface.prog = prog
	_, err := prog.Run()
	return err
}
