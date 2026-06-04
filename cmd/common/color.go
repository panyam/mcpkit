package common

import (
	"io"
	"os"

	"golang.org/x/term"
)

// ColorMode controls whether ANSI color escapes appear in output.
type ColorMode int

const (
	// ColorAuto enables color when stdout is a TTY, disables it when
	// piped. Standard CLI behavior.
	ColorAuto ColorMode = iota
	// ColorAlways forces color on. Useful when the caller pipes into a
	// terminal-aware pager (e.g. less -R).
	ColorAlways
	// ColorNever disables color unconditionally. Used by --no-color and
	// for capture-and-diff tests.
	ColorNever
)

// ColorEnabled reports whether w should receive ANSI color escapes
// under the given mode. ColorAuto checks whether w is a *os.File
// pointing at a TTY; non-file writers default to no color.
func ColorEnabled(mode ColorMode, w io.Writer) bool {
	switch mode {
	case ColorAlways:
		return true
	case ColorNever:
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// ANSI escape codes for the colors mcpskills uses. Kept narrow: green
// for success, red for failure, dim for de-emphasized rows.
const (
	ansiReset = "\x1b[0m"
	ansiBold  = "\x1b[1m"
	ansiDim   = "\x1b[2m"
	ansiGreen = "\x1b[32m"
	ansiRed   = "\x1b[31m"
	ansiCyan  = "\x1b[36m"
)

// Painter wraps ANSI escape application so callers can write
//
//	fmt.Fprintln(out, p.Green("✓"), "git-workflow", p.Dim("(verified)"))
//
// without sprinkling escape strings through the call sites. When color
// is disabled the methods return the input unchanged.
type Painter struct {
	enabled bool
}

// NewPainter constructs a Painter for w under the given mode.
func NewPainter(mode ColorMode, w io.Writer) *Painter {
	return &Painter{enabled: ColorEnabled(mode, w)}
}

func (p *Painter) wrap(code, s string) string {
	if !p.enabled {
		return s
	}
	return code + s + ansiReset
}

// Green renders s in ANSI green (success).
func (p *Painter) Green(s string) string { return p.wrap(ansiGreen, s) }

// Red renders s in ANSI red (failure).
func (p *Painter) Red(s string) string { return p.wrap(ansiRed, s) }

// Cyan renders s in ANSI cyan (informational headers).
func (p *Painter) Cyan(s string) string { return p.wrap(ansiCyan, s) }

// Bold renders s in ANSI bold.
func (p *Painter) Bold(s string) string { return p.wrap(ansiBold, s) }

// Dim renders s in ANSI dim (de-emphasized rows).
func (p *Painter) Dim(s string) string { return p.wrap(ansiDim, s) }
