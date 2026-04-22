package ux

import (
	"fmt"
	"io"

	"github.com/fatih/color"
)

var (
	styleHeading    = color.New(color.Bold)
	styleSubheading = color.New(color.Bold, color.Faint)
)

// Heading prints a single-line top-level heading for a command.
func Heading(w io.Writer, text string) {
	m := DetectMode(w)
	if m.JSON {
		return
	}
	if m.Color {
		_, _ = styleHeading.Fprintln(w, text)
		return
	}
	fmt.Fprintln(w, text)
}

// Subheading prints a less-prominent section heading (e.g. within a larger report).
func Subheading(w io.Writer, text string) {
	m := DetectMode(w)
	if m.JSON {
		return
	}
	if m.Color {
		_, _ = styleSubheading.Fprintln(w, text)
		return
	}
	fmt.Fprintln(w, text)
}
