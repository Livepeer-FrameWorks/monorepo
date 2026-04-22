package ux

import (
	"fmt"
	"io"

	"github.com/fatih/color"
)

var styleContextNotice = color.New(color.Faint)

// ContextNotice announces that a command is using a default pulled from
// context/env/saved state. It's the antidote to silent magic: the operator
// always sees "Using <key> from context: <value>" before the command acts.
//
// Suppressed only in JSON output mode (where the caller is responsible for
// serializing the defaults into the structured payload). CI and NoHints do
// NOT suppress it — a command quietly picking up a saved tenant-id in a
// pipeline is a correctness issue, not a presentation issue.
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
