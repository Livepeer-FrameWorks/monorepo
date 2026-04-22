package ux

import (
	"fmt"
	"io"
	"strings"

	"github.com/fatih/color"
)

var styleHint = color.New(color.Faint)

// FormatError prints:
//
//	✗ <message>
//	  Hint: <remediation>   (when hint != "")
//
// hint must be a short actionable remediation. For raw command output
// use ErrorWithOutput. No-op in JSON mode.
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

// ErrorWithOutput is FormatError plus indented stdout/stderr dumps.
// Empty stdout/stderr are skipped.
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
		for line := range strings.SplitSeq(body, "\n") {
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
