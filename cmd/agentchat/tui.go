package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/panyam/mcpkit/agent"
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

// usageMsg carries the finished turn's token usage so the status line can show
// a live context gauge. Sent by the observers on HostTurnDone.
type usageMsg struct{ in, out int }

// statusLine renders the persistent status: model · session · last-turn tokens,
// plus a "N% context left" gauge when a context window is configured (issue
// 1063 D1/D2). Kept out of the transcript — it lives only in the managed live
// region so it is never committed to scrollback.
//
// session is passed in (cached by the surface model), NOT read live from
// app.RunID(): RunID takes App.turnMu, which RunTurn holds for the whole turn,
// so calling it from View() would block every render until the turn finished
// and freeze the UI. ModelLabel only touches the connections mutex (never held
// during a turn) so it stays a live read.
func statusLine(app *host.App, session string, u usageMsg, window int) string {
	if app == nil {
		return ""
	}
	return formatStatus(app.ModelLabel(), session, u, window)
}

// formatStatus is the pure status-line renderer (model · session · tokens ·
// gauge), split out so it is testable without a live App.
func formatStatus(model, session string, u usageMsg, window int) string {
	if session == "" {
		session = "no session"
	}
	parts := []string{"model " + model, "session " + session}
	if u.in > 0 || u.out > 0 {
		parts = append(parts, fmt.Sprintf("ctx %d↑ %d↓ tok", u.in, u.out))
	}
	if window > 0 && u.in > 0 {
		left := 100 * (window - u.in) / window
		if left < 0 {
			left = 0
		}
		parts = append(parts, fmt.Sprintf("%d%% ctx left", left))
	}
	return strings.Join(parts, " · ")
}

// tuiObserver is the host.Observer the App renders through in inline TUI
// mode. It forwards each HostEvent to the built-in terminal renderer
// (writing to a buffer, so all formatting is reused), then either streams
// the growing turn into the model's live region or commits a finished
// segment to the terminal scrollback. Inline (no alt-screen) keeps the
// transcript in the real terminal buffer: native scroll, copy/paste, and
// it survives exit. HostRunnerEvent accumulates live; every other event
// closes a segment and commits.
//
// The committed segment is split into blocks so assistant prose renders
// through glamour once at commit (issue 1063 B2) while tool/thinking lines
// pass through verbatim: glamour-ing the whole turn would mangle the dim
// ⚙/✓ tool lines. Streaming stays raw in the live region — glamour only
// touches a finished block.
type tuiObserver struct {
	mu   sync.Mutex
	buf  *bytes.Buffer
	term host.Observer
	prog *tea.Program

	md       *mdRenderer
	renderMD func(string) string // == md.render; swapped in tests
	blocks   []segBlock          // the current turn's committed blocks
	prose    strings.Builder     // the open assistant-prose run (raw markdown)
}

// segBlock is one span of a committed turn: assistant prose (raw markdown,
// rendered through glamour at commit) or a meta line (tool/thinking output,
// already terminal-formatted, passed through verbatim).
type segBlock struct {
	prose bool
	text  string
}

func newTUIObserver() *tuiObserver {
	buf := &bytes.Buffer{}
	md := newMDRenderer()
	return &tuiObserver{buf: buf, term: host.NewTerminalRenderer(buf), md: md, renderMD: md.render}
}

// setWidth fans the terminal width to the markdown renderer so committed
// blocks word-wrap to the current terminal (called from tea.WindowSizeMsg).
func (s *tuiObserver) setWidth(w int) { s.md.setWidth(w) }

// flushProse closes any open assistant-prose run into a block.
func (s *tuiObserver) flushProse() {
	if s.prose.Len() > 0 {
		s.blocks = append(s.blocks, segBlock{prose: true, text: s.prose.String()})
		s.prose.Reset()
	}
}

// renderBlocks builds the committed segment: prose blocks through glamour,
// meta blocks verbatim, joined in order with blank-trimmed edges.
func (s *tuiObserver) renderBlocks() string {
	var out []string
	for _, b := range s.blocks {
		text := b.text
		if b.prose {
			text = s.renderMD(text)
		}
		if text = strings.TrimRight(text, "\n"); text != "" {
			out = append(out, text)
		}
	}
	return strings.Join(out, "\n")
}

// isBoundary reports whether an event closes a scrollback segment. Every
// discrete event commits immediately; only the streaming turn
// (HostRunnerEvent) accumulates live until a non-runner event closes it.
func isBoundary(k host.HostEventKind) bool {
	return k != host.HostRunnerEvent
}

func (s *tuiObserver) On(ev host.HostEvent) {
	msgs := s.fold(ev)
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

// fold turns one HostEvent into the tea messages the model should receive (a
// live update mid-turn, or a usage + commit pair at a boundary). Separated from
// On so tests can assert the prose/meta split without a running program.
func (s *tuiObserver) fold(ev host.HostEvent) []tea.Msg {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.buf.Len()
	s.term.On(ev)
	live := s.buf.String()
	var msgs []tea.Msg

	switch {
	case isBoundary(ev.Kind):
		// The boundary event's own output (turn footer, command result, info
		// line) is meta; close any open prose, fold it in, then render the turn.
		s.flushProse()
		if tail := live[before:]; tail != "" {
			s.blocks = append(s.blocks, segBlock{text: tail})
		}
		commit := s.renderBlocks()
		s.blocks = nil
		s.buf.Reset()
		if ev.Kind == host.HostTurnDone && ev.Result != nil {
			msgs = append(msgs, usageMsg{in: ev.Result.Usage.InputTokens, out: ev.Result.Usage.OutputTokens})
		}
		msgs = append(msgs, commitMsg(commit))
	case ev.Kind == host.HostRunnerEvent && ev.RunnerEvent.Kind == agent.EventTextDelta:
		// Assistant prose accumulates raw; glamour is applied once at commit.
		s.prose.WriteString(ev.RunnerEvent.Text)
		msgs = append(msgs, liveMsg(live))
	default:
		// A tool / thinking line: close open prose, keep the formatted line verbatim.
		s.flushProse()
		s.blocks = append(s.blocks, segBlock{text: live[before:]})
		msgs = append(msgs, liveMsg(live))
	}
	return msgs
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
	usage   usageMsg // last turn's tokens, for the status line
	window  int      // context window for the "N% left" gauge (0 = off)
	session string   // cached RunID for the status line (refreshed off the render path)
}

// newPromptArea builds the shared input textarea used by both TUI surfaces
// (inline and notebook): the readline keybindings (issue #1065 — Ctrl word-nav
// so it works without Option-as-Meta) live here so both get them.
func newPromptArea() textarea.Model {
	ta := textarea.New()
	ta.Placeholder = "message, or /command (Tab completes, ↑↓ history, /keys for editing keys)"
	ta.Prompt = "› "
	ta.ShowLineNumbers = false
	ta.SetHeight(2)
	// The default KeyMap binds word navigation to Meta only (alt+←/→, alt+b/f),
	// which does nothing on terminals that don't send Option as Meta (the macOS
	// default). Add the Ctrl variants so word motion works out of the box; the
	// alt bindings stay for those who have Meta enabled.
	ta.KeyMap.WordForward.SetKeys(append(ta.KeyMap.WordForward.Keys(), "ctrl+right")...)
	ta.KeyMap.WordBackward.SetKeys(append(ta.KeyMap.WordBackward.Keys(), "ctrl+left")...)
	// Enter submits (both surfaces intercept it), so rebind "insert newline" off
	// Enter and onto keys that reach the textarea. ctrl+j (keyLF) is the
	// reliable one everywhere; shift+enter works only in terminals that
	// disambiguate it (kitty keyboard protocol), alt+enter in most others.
	ta.KeyMap.InsertNewline.SetKeys("ctrl+j", "shift+enter", "alt+enter")
	ta.Focus()
	return ta
}

func newTUIModel(app *host.App, surface *tuiObserver, window int) tuiModel {
	m := tuiModel{app: app, surface: surface, ta: newPromptArea(), window: window}
	m.histIdx = 0
	if app != nil {
		// Safe here: construction runs before the first turn, so turnMu is free.
		m.session = app.RunID()
	}
	return m
}

func (m tuiModel) Init() tea.Cmd { return textarea.Blink }

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.ta.SetWidth(msg.Width)
		if m.surface != nil {
			m.surface.setWidth(msg.Width)
		}
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
		// The turn (or dispatched command) has fully returned, so turnMu is
		// free and a session-changing command like /sessions has landed.
		if m.app != nil {
			m.session = m.app.RunID()
		}
		return m, nil

	case usageMsg:
		m.usage = msg
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
	status := statusLine(m.app, m.session, m.usage, m.window)
	if m.running {
		status = "working… · " + status
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
	// /keys is a terminal-only cheatsheet (prompt editing is a TUI concern, so
	// it is not a surface-agnostic host command); print it without a turn.
	if line == "/keys" {
		return m, tea.Println(keyHelp())
	}
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

// keyHelp is the /keys cheatsheet: the prompt-editing bindings, grouped by
// what works without a Meta key (the top rows) versus what needs the
// terminal's Option-as-Meta (the alt-* bindings).
func keyHelp() string {
	return strings.Join([]string{
		"Prompt editing keys:",
		"  ← / →                 char back / forward",
		"  ctrl+← / ctrl+→       word back / forward",
		"  ctrl+a / ctrl+e       start / end of line   (also Home / End)",
		"  ctrl+w                delete previous word",
		"  ctrl+k / ctrl+u       delete to end / start of line",
		"  ctrl+home / ctrl+end  start / end of input",
		"  ctrl+j               insert a newline (also shift+enter / alt+enter)",
		"  ↑ / ↓  history    Tab  complete    Enter  send    ctrl+c  quit",
		"  With Option-as-Meta on: alt+←/→ or alt+b/f word nav, alt+d delete-word-forward, alt+</> input ends.",
	}, "\n")
}

func (m *tuiModel) completeTab() { tabComplete(&m.ta, m.app) }

// tabComplete completes a leading slash command against the registry: a
// unique command-name match is inlined; an argument prefix is completed via
// the command's Complete hook (providers, sessions, ...). Shared by both TUI
// surfaces.
func tabComplete(ta *textarea.Model, app *host.App) {
	val := ta.Value()
	if !strings.HasPrefix(val, "/") {
		return
	}
	reg := app.Commands()
	word, rest, hasArg := strings.Cut(val, " ")
	if !hasArg {
		// completing the command name
		if names := reg.Match(word); len(names) == 1 {
			ta.SetValue("/" + names[0] + " ")
			ta.CursorEnd()
		}
		return
	}
	// completing an argument
	if cmd, ok := reg.Lookup(word); ok && cmd.Complete != nil {
		if opts := cmd.Complete(strings.TrimSpace(rest)); len(opts) == 1 {
			ta.SetValue(word + " " + opts[0])
			ta.CursorEnd()
		}
	}
}

// snippet trims s to one line of at most n runes with an ellipsis, for
// collapsed cell headers.
func snippet(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
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

// uiMode resolves the --ui flag to a concrete surface: "plain" (the scriptable
// scanner REPL), "tui" (the default inline bubbletea surface), or "notebook"
// (the opt-in alt-screen surface). "auto" (the default flag value) picks "tui"
// when stdout is an interactive terminal and "plain" otherwise (pipes / CI).
func uiMode(flag string) string {
	switch flag {
	case "plain", "tui", "notebook":
		return flag
	default:
		fi, err := os.Stdout.Stat()
		if err == nil && (fi.Mode()&os.ModeCharDevice) != 0 {
			return "tui"
		}
		return "plain"
	}
}

// wantTUI reports whether --ui resolves to an interactive bubbletea surface
// (inline tui or notebook) rather than the plain REPL.
func wantTUI(mode string) bool { return uiMode(mode) != "plain" }

// runTUI starts the inline bubbletea program (no alt-screen): the input
// and the live turn render at the bottom while finished segments commit to
// the terminal's own scrollback. The surface is wired to the program so
// host events stream in as the model renders.
func runTUI(app *host.App, surface *tuiObserver, window int) error {
	prog := tea.NewProgram(newTUIModel(app, surface, window))
	surface.prog = prog
	_, err := prog.Run()
	return err
}
