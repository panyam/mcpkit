package main

import (
	"strings"
	"testing"
)

func TestFormatStatus(t *testing.T) {
	// model + session always shown; empty session -> "no session"
	base := formatStatus("gpt-5.1", "", usageMsg{}, 0)
	if !strings.Contains(base, "model gpt-5.1") || !strings.Contains(base, "session no session") {
		t.Fatalf("base status wrong: %q", base)
	}
	// tokens appear once a turn has usage
	withTok := formatStatus("m", "sess-1", usageMsg{in: 1200, out: 80}, 0)
	if !strings.Contains(withTok, "1200↑ 80↓") || !strings.Contains(withTok, "session sess-1") {
		t.Fatalf("token status wrong: %q", withTok)
	}
	// gauge only when a window is configured
	if strings.Contains(withTok, "ctx left") {
		t.Fatalf("no window should mean no gauge: %q", withTok)
	}
	gauge := formatStatus("m", "s", usageMsg{in: 2000}, 8000)
	if !strings.Contains(gauge, "75% ctx left") { // (8000-2000)/8000
		t.Fatalf("gauge wrong: %q", gauge)
	}
	// over-budget clamps at 0, never negative
	over := formatStatus("m", "s", usageMsg{in: 9000}, 8000)
	if !strings.Contains(over, "0% ctx left") {
		t.Fatalf("over-budget should clamp to 0%%: %q", over)
	}
}
