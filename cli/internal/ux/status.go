package ux

import (
	"fmt"
	"io"

	"github.com/fatih/color"
)

var (
	styleSuccess = color.New(color.FgGreen)
	styleWarn    = color.New(color.FgYellow)
	styleFail    = color.New(color.FgRed, color.Bold)
)

// Success prints "✓ <text>" in green. Degrades to "[OK] <text>" on non-TTY.
func Success(w io.Writer, text string) {
	mark, style := pickMark(w, "✓", "[OK]", styleSuccess)
	emit(w, style, mark, text)
}

// Warn prints "⚠ <text>" in yellow. Degrades to "[WARN] <text>" on non-TTY.
func Warn(w io.Writer, text string) {
	mark, style := pickMark(w, "⚠", "[WARN]", styleWarn)
	emit(w, style, mark, text)
}

// Fail prints "✗ <text>" in red + bold. Degrades to "[FAIL] <text>" on non-TTY.
func Fail(w io.Writer, text string) {
	mark, style := pickMark(w, "✗", "[FAIL]", styleFail)
	emit(w, style, mark, text)
}

func pickMark(w io.Writer, uni, ascii string, style *color.Color) (string, *color.Color) {
	m := DetectMode(w)
	if m.JSON {
		return "", nil
	}
	if m.Unicode {
		if m.Color {
			return uni, style
		}
		return uni, nil
	}
	return ascii, nil
}

func emit(w io.Writer, style *color.Color, mark, text string) {
	if mark == "" {
		return
	}
	if style != nil {
		_, _ = style.Fprint(w, mark)
		fmt.Fprintf(w, " %s\n", text)
		return
	}
	fmt.Fprintf(w, "%s %s\n", mark, text)
}
