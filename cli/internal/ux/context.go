package ux

import (
	"fmt"
	"io"

	"github.com/fatih/color"
)

var styleContextNotice = color.New(color.Faint)

// ContextNotice prints "Using <key> from context: <value>" before a
// command acts on a context-derived default. Suppressed in JSON mode
// only; NoHints/CI do not suppress it.
func ContextNotice(w io.Writer, key, value string) {
	m := DetectMode(w)
	if m.JSON {
		return
	}
	msg := fmt.Sprintf("Using %s from context: %s", key, value)
	if m.Color {
		_, _ = styleContextNotice.Fprintln(w, msg)
		return
	}
	fmt.Fprintln(w, msg)
}
