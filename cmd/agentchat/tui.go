package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/panyam/mcpkit/agent/host"
)

// transcriptMsg carries the freshly formatted scrollback (the whole
// transcript so far) from the surface goroutine into the model.
type transcriptMsg string

// turnDoneMsg re-enables input after a dispatched command or a turn
// finishes on its goroutine.
type turnDoneMsg struct{}

// tuiSurface is the host.Surface the App renders through in TUI mode. It
// forwards each UIEvent to the built-in terminal renderer (writing to a
// buffer, so all formatting is reused) and pushes the accumulated
// transcript into the bubbletea program. UIPrompt is dropped — the
// textarea is the prompt.
type tuiSurface struct {
	mu   sync.Mutex
	buf  *bytes.Buffer
	term host.Surface
	prog *tea.Program
}

func newTUISurface() *tuiSurface {
	buf := &bytes.Buffer{}
	return &tuiSurface{buf: buf, term: host.NewTerminalSurface(buf)}
}

func (s *tuiSurface) Emit(ev host.UIEvent) {
	if ev.Kind == host.UIPrompt {
		return
	}
	s.mu.Lock()
	s.term.Emit(ev)
	content := s.buf.String()
	prog := s.prog
	s.mu.Unlock()
	if prog != nil {
		prog.Send(transcriptMsg(content))
	}
}

// tuiModel is the bubbletea Model: a scrollback viewport over the
// transcript plus an editable input, with slash-command tab-completion
// and up/down history recall. All behavior routes through the App
// (Dispatch / RunTurn); the model is pure presentation.
type tuiModel struct {
	app     *host.App
	surface *tuiSurface
	ta      textarea.Model
	vp      viewport.Model
	history []string
	histIdx int // len(history) == "not navigating"
	running bool
	ready   bool
	status  string
}

func newTUIModel(app *host.App, surface *tuiSurface) tuiModel {
	ta := textarea.New()
	ta.Placeholder = "message, or /command (Tab completes, ↑↓ history)"
	ta.Prompt = "› "
	ta.ShowLineNumbers = false
	ta.SetHeight(3)
	ta.Focus()
	m := tuiModel{app: app, surface: surface, ta: ta, status: "ready"}
	m.histIdx = 0
	return m
}

func (m tuiModel) Init() tea.Cmd { return textarea.Blink }

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		inputH := 4
		if !m.ready {
			m.vp = viewport.New(msg.Width, msg.Height-inputH)
			m.ready = true
		} else {
			m.vp.Width = msg.Width
			m.vp.Height = msg.Height - inputH
		}
		m.ta.SetWidth(msg.Width)
		return m, nil

	case transcriptMsg:
		m.vp.SetContent(string(msg))
		m.vp.GotoBottom()
		return m, nil

	case turnDoneMsg:
		m.running = false
		m.status = "ready"
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
	// input + viewport get the rest (scrolling, editing, cursor motion)
	var tcmd, vcmd tea.Cmd
	m.ta, tcmd = m.ta.Update(msg)
	m.vp, vcmd = m.vp.Update(msg)
	cmds = append(cmds, tcmd, vcmd)
	return m, tea.Batch(cmds...)
}

func (m tuiModel) View() string {
	if !m.ready {
		return "starting…"
	}
	status := lipgloss.NewStyle().Faint(true).Render(m.status)
	return m.vp.View() + "\n" + status + "\n" + m.ta.View()
}

// submit routes a line to a command dispatch or a conversational turn,
// both on a goroutine so the UI stays responsive; the surface streams
// results back as transcriptMsg and the goroutine ends with turnDoneMsg.
func (m tuiModel) submit(line string) (tea.Model, tea.Cmd) {
	m.running = true
	m.status = "working…"
	app, surface := m.app, m.surface
	ctx := context.Background()
	if strings.HasPrefix(line, "/") {
		go func() {
			res, err := app.Dispatch(ctx, line)
			switch {
			case errors.Is(err, host.ErrUnknownCommand):
				surface.Emit(host.UIEvent{Kind: host.UITurnFailed, Err: "unknown command " + line})
			case err != nil:
				surface.Emit(host.UIEvent{Kind: host.UITurnFailed, Err: err.Error()})
			default:
				surface.Emit(host.UIEvent{Kind: host.UICommand, Command: res})
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

// runTUI starts the bubbletea program: the alt-screen scrollback UI over
// the App. The surface is wired to the program so host UIEvents stream in
// as the model renders.
func runTUI(app *host.App, surface *tuiSurface) error {
	prog := tea.NewProgram(newTUIModel(app, surface), tea.WithAltScreen())
	surface.prog = prog
	_, err := prog.Run()
	return err
}
