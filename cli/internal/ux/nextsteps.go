package ux

import (
	"fmt"
	"io"

	"github.com/fatih/color"
)

// NextStep is a suggested follow-up command plus an optional reason.
type NextStep struct {
	Cmd string
	Why string
}

var (
	styleNextHeading = color.New(color.Bold)
	styleNextCmd     = color.New(color.FgCyan)
	styleNextWhy     = color.New(color.Faint)
)

// PrintNextSteps renders a "Next:" block. Suppressed when Mode.Hints is
// false (CI/NoHints) and in JSON output mode.
//
// Entries split by shape:
//   - Cmd set (Why optional): rendered as a numbered command + reason.
//   - Cmd empty, Why set: rendered as an unnumbered advisory bullet under
//     the same heading — it's still operator-facing guidance, but it's
//     not executable so we don't pretend it is.
//   - Both empty: skipped.
func PrintNextSteps(w io.Writer, steps []NextStep) {
	m := DetectMode(w)
	if m.JSON || !m.Hints || len(steps) == 0 {
		return
	}

	// Filter empty-empty entries and split by shape so executables get
	// the numbered-command treatment and why-only entries don't pretend
	// to be runnable.
	type entry struct {
		ns       NextStep
		runnable bool
	}
	entries := make([]entry, 0, len(steps))
	for _, s := range steps {
		if s.Cmd == "" && s.Why == "" {
			continue
		}
		entries = append(entries, entry{ns: s, runnable: s.Cmd != ""})
	}
	if len(entries) == 0 {
		return
	}

	fmt.Fprintln(w)
	if m.Color {
		_, _ = styleNextHeading.Fprintln(w, "Next:")
	} else {
		fmt.Fprintln(w, "Next:")
	}

	cmdIdx := 0
	for _, e := range entries {
		if e.runnable {
			cmdIdx++
			prefix := fmt.Sprintf("  %d. ", cmdIdx)
			if m.Color {
				fmt.Fprint(w, prefix)
				_, _ = styleNextCmd.Fprintln(w, e.ns.Cmd)
				if e.ns.Why != "" {
					_, _ = styleNextWhy.Fprintf(w, "     %s\n", e.ns.Why)
				}
				continue
			}
			fmt.Fprintf(w, "%s%s\n", prefix, e.ns.Cmd)
			if e.ns.Why != "" {
				fmt.Fprintf(w, "     %s\n", e.ns.Why)
			}
			continue
		}
		// Why-only: unnumbered advisory bullet.
		line := fmt.Sprintf("  - %s", e.ns.Why)
		if m.Color {
			_, _ = styleNextWhy.Fprintln(w, line)
			continue
		}
		fmt.Fprintln(w, line)
	}
}
