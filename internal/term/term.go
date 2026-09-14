// Package term decides whether output goes to a terminal that wants ANSI
// colours, and paints text accordingly.
package term

import (
	"io"
	"os"
)

// Style is an ANSI SGR escape sequence.
type Style string

// Styles used across loop's output.
const (
	Reset   Style = "\x1b[0m"
	Bold    Style = "\x1b[1m"
	Dim     Style = "\x1b[2m"
	Red     Style = "\x1b[31m"
	Green   Style = "\x1b[32m"
	Yellow  Style = "\x1b[33m"
	Blue    Style = "\x1b[34m"
	Magenta Style = "\x1b[35m"
	Cyan    Style = "\x1b[36m"
)

// Painter colours text when enabled. The zero value paints nothing, so
// writers that never asked for colours (log files, tests, pipes) get
// plain text.
type Painter bool

// Paint wraps s in the styles and a reset; it returns s unchanged when the
// painter is off, when no style is given, or when s is empty.
func (p Painter) Paint(s string, styles ...Style) string {
	if !p || len(styles) == 0 || s == "" {
		return s
	}
	out := ""
	for _, st := range styles {
		out += string(st)
	}
	return out + s + string(Reset)
}

// Detect reports whether w wants colours: NO_COLOR (no-color.org) turns
// them off, CLICOLOR_FORCE (bixense.com/clicolors) turns them on for any
// writer, and otherwise w has to be a terminal with a TERM other than
// "dumb".
func Detect(w io.Writer) Painter {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if f := os.Getenv("CLICOLOR_FORCE"); f != "" && f != "0" {
		return true
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok || f == nil {
		return false
	}
	st, err := f.Stat()
	if err != nil || st.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	return Painter(enableVT(f))
}
