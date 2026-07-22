package main

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/agent/host"
	"github.com/panyam/mcpkit/client"
)

func kmsg(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func TestOverlay_EscDismisses(t *testing.T) {
	o := newOverlay("t", []overlayItem{{Label: "a"}})
	if out := o.handleKey(kmsg("esc")); !out.Dismiss {
		t.Fatalf("esc = %+v, want Dismiss", out)
	}
}

func TestOverlay_MoveClampsAndScrolls(t *testing.T) {
	items := make([]overlayItem, 25)
	for i := range items {
		items[i] = overlayItem{Label: string(rune('a' + i))}
	}
	o := newOverlay("t", items) // maxRows 10

	o.move(-1) // clamp at top
	if o.cursor != 0 || o.top != 0 {
		t.Fatalf("up at top: cursor=%d top=%d", o.cursor, o.top)
	}
	for i := 0; i < 12; i++ {
		o.move(1)
	}
	if o.cursor != 12 {
		t.Fatalf("cursor = %d, want 12", o.cursor)
	}
	if o.cursor < o.top || o.cursor >= o.top+o.maxRows {
		t.Fatalf("cursor %d outside window [%d,%d)", o.cursor, o.top, o.top+o.maxRows)
	}
	for i := 0; i < 50; i++ {
		o.move(1) // clamp at bottom
	}
	if o.cursor != len(items)-1 {
		t.Fatalf("cursor = %d, want %d", o.cursor, len(items)-1)
	}
}

func TestOverlay_EnterFiresPrimary(t *testing.T) {
	o := newOverlay("t", []overlayItem{
		{Label: "a", Actions: []overlayAction{{Label: "go", Line: "/resume a"}}},
	})
	if out := o.handleKey(kmsg("enter")); out.Line != "/resume a" {
		t.Fatalf("enter = %+v, want Line /resume a", out)
	}
}

func TestOverlay_SecondaryKeyFires(t *testing.T) {
	o := newOverlay("t", []overlayItem{
		{Label: "a", Actions: []overlayAction{
			{Label: "reconnect", Line: "/servers reconnect a"},
			{Key: "l", Label: "login", Line: "/login a"},
		}},
	})
	if out := o.handleKey(kmsg("l")); out.Line != "/login a" {
		t.Fatalf("l = %+v, want Line /login a", out)
	}
}

func TestOverlay_DisabledActionDoesNothing(t *testing.T) {
	o := newOverlay("t", []overlayItem{
		{Label: "a", Actions: []overlayAction{{Label: "reconnect"}}}, // empty Line = n/a
	})
	if out := o.handleKey(kmsg("enter")); out != (overlayOutcome{}) {
		t.Fatalf("enter on disabled primary = %+v, want no-op", out)
	}
}

func TestOverlay_EmptyItemsOnlyEscActs(t *testing.T) {
	o := newOverlay("t", nil)
	if out := o.handleKey(kmsg("enter")); out != (overlayOutcome{}) {
		t.Fatalf("enter on empty = %+v, want no-op", out)
	}
	if out := o.handleKey(kmsg("j")); out != (overlayOutcome{}) {
		t.Fatalf("j on empty = %+v, want no-op", out)
	}
	if out := o.handleKey(kmsg("esc")); !out.Dismiss {
		t.Fatal("esc on empty should dismiss")
	}
	_ = o.View() // must not panic on an empty list
}

func TestServersOverlay_ReconnectOnlyWhenStuck(t *testing.T) {
	res := host.CmdResult{Kind: host.CmdServers, Servers: []client.MemberStatus{
		{ID: "ready", State: client.StateReady},
		{ID: "down", State: client.StateFailed, Err: errors.New("refused")},
		{ID: "auth", State: client.StateNeedsLogin},
	}}
	o := serversOverlay(res)

	// ready: reconnect present but unavailable (empty Line)
	if got := primaryLine(o.items[0]); got != "" {
		t.Fatalf("ready primary line = %q, want empty (n/a)", got)
	}
	// failed + needs-login: reconnect is the primary action
	if got := primaryLine(o.items[1]); got != "/servers reconnect down" {
		t.Fatalf("failed primary = %q", got)
	}
	if got := primaryLine(o.items[2]); got != "/servers reconnect auth" {
		t.Fatalf("needs-login primary = %q", got)
	}
	// the error surfaces in the detail
	if !strings.Contains(o.items[1].Detail, "refused") {
		t.Fatalf("failed detail = %q, want the error", o.items[1].Detail)
	}
}

func TestSessionsOverlay_ResumeAndActiveCursor(t *testing.T) {
	res := host.CmdResult{Kind: host.CmdSessions, RunID: "s2", Sessions: []agent.RunInfo{
		{ID: "s1", MessageCount: 3},
		{ID: "s2", MessageCount: 5},
	}}
	o := sessionsOverlay(res)
	if o.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 (the active run)", o.cursor)
	}
	if got := primaryLine(o.items[1]); got != "/resume s2" {
		t.Fatalf("resume line = %q", got)
	}
	if !strings.Contains(o.items[1].Detail, "active") {
		t.Fatalf("active row not marked: %q", o.items[1].Detail)
	}
}

func TestOverlayFor_OnlyInteractiveKinds(t *testing.T) {
	if overlayFor(host.CmdResult{Kind: host.CmdServers}) == nil {
		t.Fatal("CmdServers should open an overlay")
	}
	if overlayFor(host.CmdResult{Kind: host.CmdSessions}) == nil {
		t.Fatal("CmdSessions should open an overlay")
	}
	if overlayFor(host.CmdResult{Kind: host.CmdMessage, Message: "hi"}) != nil {
		t.Fatal("CmdMessage should not open an overlay")
	}
}

// primaryLine is the test helper for the selected row's Enter action line.
func primaryLine(it overlayItem) string {
	for _, a := range it.Actions {
		if a.Key == "" {
			return a.Line
		}
	}
	return ""
}
