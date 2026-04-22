// Package ux provides shared output primitives for the FrameWorks CLI.
//
// Helpers take an io.Writer (typically cobra's cmd.OutOrStdout()) and emit
// colored, consistent output that degrades cleanly on non-TTY writers, CI
// runs, and JSON output mode.
package ux

import (
	"io"
	"os"

	fwcfg "frameworks/cli/internal/config"

	"github.com/mattn/go-isatty"
)

// Mode describes how output should be rendered for a given writer.
type Mode struct {
	Color   bool
	Unicode bool
	Hints   bool
	JSON    bool
}

// DetectMode inspects the global runtime overrides and the writer to pick
// an output style. JSON mode forces all helpers into no-op so the command
// can emit structured output on the same writer without interleaving.
func DetectMode(w io.Writer) Mode {
	rt := fwcfg.GetRuntimeOverrides()
	if rt.OutputJSON {
		return Mode{JSON: true}
	}
	tty := isTerminal(w)
	return Mode{
		Color:   tty && os.Getenv("NO_COLOR") == "",
		Unicode: tty,
		Hints:   !rt.NoHints,
	}
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fd := f.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}
