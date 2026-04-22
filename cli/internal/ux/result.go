package ux

import (
	"fmt"
	"io"

	"github.com/fatih/color"
)

// ResultField is one row in a structured result block.
type ResultField struct {
	Key    string
	OK     bool
	Detail string
}

var styleResultKey = color.New(color.Bold)

// Result prints a structured multi-line summary. Order is preserved.
// JSON mode is a no-op (command emits structured output separately).
func Result(w io.Writer, fields []ResultField) {
	m := DetectMode(w)
	if m.JSON || len(fields) == 0 {
		return
	}

	maxKey := 0
	for _, f := range fields {
		if len(f.Key) > maxKey {
			maxKey = len(f.Key)
		}
	}

	Heading(w, "Result:")
	for _, f := range fields {
		pad := ""
		if n := maxKey - len(f.Key); n > 0 {
			pad = padSpaces(n)
		}
		detail := f.Detail
		if detail == "" {
			if f.OK {
				detail = "yes"
			} else {
				detail = "no"
			}
		}
		if m.Color {
			_, _ = styleResultKey.Fprintf(w, "  %s:%s  ", f.Key, pad)
		} else {
			fmt.Fprintf(w, "  %s:%s  ", f.Key, pad)
		}
		if f.OK {
			Success(w, detail)
		} else {
			Fail(w, detail)
		}
	}
}

func padSpaces(n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = ' '
	}
	return string(out)
}
