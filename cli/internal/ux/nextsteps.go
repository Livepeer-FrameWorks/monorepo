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

// PrintNextSteps renders a "Next:" block. Suppressed in JSON mode and
// when Mode.Hints is false. Entries with Cmd set render as numbered
// commands; Why-only entries render as unnumbered bullets; empty entries
// are skipped.
func PrintNextSteps(w io.Writer, steps []NextStep) {
	m := DetectMode(w)
	if m.JSON || !m.Hints || len(steps) == 0 {
		return
	}

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
		line := fmt.Sprintf("  - %s", e.ns.Why)
		if m.Color {
			_, _ = styleNextWhy.Fprintln(w, line)
			continue
		}
		fmt.Fprintln(w, line)
	}
}
