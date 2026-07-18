package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/panyam/mcpkit/agent/host"
)

// liveMsg carries the current uncommitted turn segment (streaming text +
// tool lines) into the model's live region above the input.
type liveMsg string

// commitMsg carries a finished segment to commit into the terminal's own
// scrollback (via tea.Println), so it persists past exit, scrolls
// natively, and is copy/paste-able.
type commitMsg string

// turnDoneMsg re-enables input after a dispatched command or a turn
// finishes on its goroutine.
type turnDoneMsg struct{}

// tuiObserver is the host.Observer the App renders through in inline TUI
// mode. It forwards each HostEvent to the built-in terminal renderer
// (writing to a buffer, so all formatting is reused), then either streams
// the growing turn into the model's live region or commits a finished
// segment to the terminal scrollback. Inline (no alt-screen) keeps the
// transcript in the real terminal buffer: native scroll, copy/paste, and
// it survives exit. HostRunnerEvent accumulates live; every other event
// closes a segment and commits.
type tuiObserver struct {
	mu   sync.Mutex
	buf  *bytes.Buffer
	term host.Observer
	prog *tea.Program
}

func newTUIObserver() *tuiObserver {
	buf := &bytes.Buffer{}
	return &tuiObserver{buf: buf, term: host.NewTerminalRenderer(buf)}
}

// isBoundary reports whether an event closes a scrollback segment. Every
// discrete event commits immediately; only the streaming turn
// (HostRunnerEvent) accumulates live until a non-runner event closes it.
func isBoundary(k host.HostEventKind) bool {
	return k != host.HostRunnerEvent
}

func (s *tuiObserver) On(ev host.HostEvent) {
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
		prog.Send(commitMsg(segment))
	} else {
		prog.Send(liveMsg(segment))
	}
}

// tuiModel is the inline bubbletea Model: an editable input at the bottom
// (bubbles textarea) with up/down history and slash-command tab
// completion, plus a live region showing the in-flight turn. Finished
// segments commit to the terminal's own scrollback, not a managed
// viewport, so the terminal's native scroll and selection work. All
// behavior routes through the App (Dispatch / RunTurn); the model is pure
// presentation.
type tuiModel struct {
	app     *host.App
	surface *tuiObserver
	ta      textarea.Model
	history []string
	histIdx int // len(history) == "not navigating"
	running bool
	pending string
}

func newTUIModel(app *host.App, surface *tuiObserver) tuiModel {
	ta := textarea.New()
	ta.Placeholder = "message, or /command (Tab completes, ↑↓ history)"
	ta.Prompt = "› "
	ta.ShowLineNumbers = false
	ta.SetHeight(2)
	ta.Focus()
	m := tuiModel{app: app, surface: surface, ta: ta}
	m.histIdx = 0
	return m
}

func (m tuiModel) Init() tea.Cmd { return textarea.Blink }

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.ta.SetWidth(msg.Width)
		return m, nil

	case liveMsg:
		m.pending = string(msg)
		return m, nil

	case commitMsg:
		m.pending = ""
		text := strings.TrimRight(string(msg), "\n")
		if text == "" {
			return m, nil
		}
		return m, tea.Println(text)

	case turnDoneMsg:
		m.running = false
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit
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
			m.completeTab()
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
	}
	// the input gets the rest (editing, cursor motion)
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m, cmd
}

func (m tuiModel) View() string {
	// Only the live region + input are managed; the committed transcript
	// lives in the terminal's own scrollback above this frame.
	var b strings.Builder
	if m.pending != "" {
		b.WriteString(strings.TrimRight(m.pending, "\n"))
		b.WriteString("\n")
	}
	status := "ready"
	if m.running {
		status = "working…"
	}
	b.WriteString(lipgloss.NewStyle().Faint(true).Render(status))
	b.WriteString("\n")
	b.WriteString(m.ta.View())
	return b.String()
}

// submit routes a line to a command dispatch or a conversational turn,
// both on a goroutine so the UI stays responsive; the surface streams
// segments back and the goroutine ends with turnDoneMsg.
func (m tuiModel) submit(line string) (tea.Model, tea.Cmd) {
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

// completeTab completes a leading slash command against the registry: a
// unique command-name match is inlined; an argument prefix is completed
// via the command's Complete hook (providers, sessions, ...).
func (m *tuiModel) completeTab() {
	val := m.ta.Value()
	if !strings.HasPrefix(val, "/") {
		return
	}
	reg := m.app.Commands()
	word, rest, hasArg := strings.Cut(val, " ")
	if !hasArg {
		// completing the command name
		if names := reg.Match(word); len(names) == 1 {
			m.ta.SetValue("/" + names[0] + " ")
			m.ta.CursorEnd()
		}
		return
	}
	// completing an argument
	if cmd, ok := reg.Lookup(word); ok && cmd.Complete != nil {
		if opts := cmd.Complete(strings.TrimSpace(rest)); len(opts) == 1 {
			m.ta.SetValue(word + " " + opts[0])
			m.ta.CursorEnd()
		}
	}
}

// recallHistory replaces the input with a previous/next submitted line.
// Returns false when there is nothing to recall in that direction, so the
// key falls through to normal cursor motion.
func (m *tuiModel) recallHistory(dir int) bool {
	if len(m.history) == 0 {
		return false
	}
	// only hijack up/down when the input is a single line (else it's cursor motion)
	if strings.Contains(m.ta.Value(), "\n") {
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

// wantTUI resolves the --ui flag: "tui"/"plain" force the mode; "auto"
// (the default) picks the TUI only when stdout is an interactive
// terminal, so piped or CI runs get the scriptable scanner REPL.
func wantTUI(mode string) bool {
	switch mode {
	case "tui":
		return true
	case "plain":
		return false
	default:
		fi, err := os.Stdout.Stat()
		return err == nil && (fi.Mode()&os.ModeCharDevice) != 0
	}
}

// runTUI starts the inline bubbletea program (no alt-screen): the input
// and the live turn render at the bottom while finished segments commit to
// the terminal's own scrollback. The surface is wired to the program so
// host events stream in as the model renders.
func runTUI(app *host.App, surface *tuiObserver) error {
	prog := tea.NewProgram(newTUIModel(app, surface))
	surface.prog = prog
	_, err := prog.Run()
	return err
}
