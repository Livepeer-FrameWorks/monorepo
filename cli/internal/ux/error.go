package ux

import (
	"fmt"
	"io"
	"strings"

	"github.com/fatih/color"
)

var styleHint = color.New(color.Faint)

// FormatError prints an error in the CLI's standard shape:
//
//	✗ <message>
//	  Hint: <optional remediation>
//
// hint must be a short actionable remediation ("run X", "check Y"). Do NOT
// pass raw command stdout/stderr here — use ErrorWithOutput for that case
// so the blob goes below the Hint and doesn't hijack its meaning.
// JSON mode writes nothing (command emits structured error).
func FormatError(w io.Writer, err error, hint string) {
	if err == nil {
		return
	}
	m := DetectMode(w)
	if m.JSON {
		return
	}
	Fail(w, err.Error())
	if hint == "" {
		return
	}
	line := fmt.Sprintf("  Hint: %s", hint)
	if m.Color {
		_, _ = styleHint.Fprintln(w, line)
		return
	}
	fmt.Fprintln(w, line)
}

// ErrorWithOutput is FormatError plus an indented dump of a command's raw
// stdout / stderr, both rendered faint so they read as context — not as
// the remediation hint. Empty stdout/stderr are skipped.
//
// Shape:
//
//	✗ <wrapped error>
//	  Hint: <short remediation>   (when hint != "")
//	  stdout:
//	    <raw>
//	  stderr:
//	    <raw>
func ErrorWithOutput(w io.Writer, err error, hint, stdout, stderr string) {
	if err == nil {
		return
	}
	m := DetectMode(w)
	if m.JSON {
		return
	}
	FormatError(w, err, hint)
	writeDump := func(label, body string) {
		body = strings.TrimRight(body, "\n")
		if body == "" {
			return
		}
		header := fmt.Sprintf("  %s:", label)
		if m.Color {
			_, _ = styleHint.Fprintln(w, header)
		} else {
			fmt.Fprintln(w, header)
		}
		for _, line := range strings.Split(body, "\n") {
			indented := "    " + line
			if m.Color {
				_, _ = styleHint.Fprintln(w, indented)
			} else {
				fmt.Fprintln(w, indented)
			}
		}
	}
	writeDump("stdout", stdout)
	writeDump("stderr", stderr)
}
