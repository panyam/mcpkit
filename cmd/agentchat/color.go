package main

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// resolveColorEnabled decides whether the interactive surfaces emit color/ANSI
// styling, honoring the precedence the TUI track calls for (issue 1063 E2):
// the --no-color flag wins, then NO_COLOR (present at any value, per
// no-color.org), then a dumb terminal (TERM=dumb). Absent all three, color is
// on and termenv is left to auto-detect the profile so it degrades
// truecolor→256→16→mono on its own.
//
// look is os.LookupEnv in production; tests pass a stub map so the precedence
// is verifiable without mutating the process environment.
func resolveColorEnabled(flagNoColor bool, look func(string) (string, bool)) bool {
	if flagNoColor {
		return false
	}
	if _, ok := look("NO_COLOR"); ok {
		return false
	}
	if v, _ := look("TERM"); v == "dumb" {
		return false
	}
	return true
}

// applyLipglossProfile pins lipgloss to a plain (Ascii) color profile when color
// is disabled, so every Faint/Bold/Reverse/Foreground style across the overlay,
// status line, and notebook cells renders as plain text deterministically. When
// color is enabled it leaves the default renderer's auto-detected profile in
// place, which is what gives termenv's truecolor→256→16→mono degradation for
// free. Call once at startup, before any surface renders.
func applyLipglossProfile(colorEnabled bool) {
	if !colorEnabled {
		lipgloss.SetColorProfile(termenv.Ascii)
	}
}

// accentColor is the one true foreground color in the TUI (overlay title +
// selected row). AdaptiveColor picks per terminal background (issue 1063 E1):
// blue reads better on light terminals, cyan on dark. Both are 16-color-safe
// ANSI indices, so the accent survives a low-capability terminal.
var accentColor = lipgloss.AdaptiveColor{Light: "4", Dark: "6"}
