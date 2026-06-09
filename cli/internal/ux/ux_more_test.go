package ux

import (
	"bytes"
	"strings"
	"testing"

	fwcfg "frameworks/cli/internal/config"
)

func TestResult_DetailDefaultsToYesNo(t *testing.T) {
	withRuntime(t, fwcfg.RuntimeOverrides{})
	var buf bytes.Buffer
	Result(&buf, []ResultField{
		{Key: "a", OK: true},               // empty detail → "yes"
		{Key: "b", OK: false},              // empty detail → "no"
		{Key: "c", OK: true, Detail: "42"}, // explicit detail wins
	})
	got := buf.String()
	if !strings.Contains(got, "[OK] yes") {
		t.Fatalf("OK + empty detail must render yes; got %q", got)
	}
	if !strings.Contains(got, "[FAIL] no") {
		t.Fatalf("fail + empty detail must render no; got %q", got)
	}
	if !strings.Contains(got, "[OK] 42") {
		t.Fatalf("explicit detail must be used verbatim; got %q", got)
	}
	if strings.Contains(got, "[OK] yes\n  c") {
		t.Fatal("explicit detail row must not fall back to yes")
	}
}

func TestResult_KeyColumnAlignment(t *testing.T) {
	withRuntime(t, fwcfg.RuntimeOverrides{})
	var buf bytes.Buffer
	// Keys of different widths: "x" (1) and "longkey" (7). The short key must
	// be padded so the colon+gap before the detail aligns to the longest key.
	Result(&buf, []ResultField{
		{Key: "x", OK: true, Detail: "v1"},
		{Key: "longkey", OK: true, Detail: "v2"},
	})
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	// lines[0] = "Result:"; find the two field lines.
	var shortLine, longLine string
	for _, l := range lines {
		if strings.Contains(l, "x:") && !strings.Contains(l, "longkey") {
			shortLine = l
		}
		if strings.Contains(l, "longkey:") {
			longLine = l
		}
	}
	if shortLine == "" || longLine == "" {
		t.Fatalf("could not locate field lines: %q", buf.String())
	}
	// The detail token starts at the same column on both lines.
	if idxShort, idxLong := strings.Index(shortLine, "v1"), strings.Index(longLine, "v2"); idxShort != idxLong {
		t.Fatalf("detail columns misaligned: short@%d long@%d\n%q", idxShort, idxLong, buf.String())
	}
	// The short key must carry padding (extra spaces between ':' and detail).
	if !strings.Contains(shortLine, "x:      ") {
		t.Fatalf("short key must be padded to longest width; got %q", shortLine)
	}
}

func TestResult_SingleFieldNoPadding(t *testing.T) {
	withRuntime(t, fwcfg.RuntimeOverrides{})
	var buf bytes.Buffer
	// Single field: maxKey == len(key), so n == 0 and no extra pad inserted.
	Result(&buf, []ResultField{{Key: "only", OK: true, Detail: "z"}})
	if !strings.Contains(buf.String(), "  only:  [OK] z") {
		t.Fatalf("single field must have no extra padding; got %q", buf.String())
	}
}

func TestNextSteps_NumberingIsSequentialFromOne(t *testing.T) {
	withRuntime(t, fwcfg.RuntimeOverrides{})
	var buf bytes.Buffer
	PrintNextSteps(&buf, []NextStep{
		{Cmd: "cmd-a"},
		{Why: "advisory between"},
		{Cmd: "cmd-b"},
		{Cmd: "cmd-c"},
	})
	got := buf.String()
	// Runnable commands numbered 1,2,3 in order regardless of interleaved bullet.
	for i, want := range []string{"  1. cmd-a", "  2. cmd-b", "  3. cmd-c"} {
		if !strings.Contains(got, want) {
			t.Fatalf("step %d: missing %q in %q", i+1, want, got)
		}
	}
	if strings.Contains(got, "0. ") || strings.Contains(got, "-1. ") {
		t.Fatalf("numbering must start at 1; got %q", got)
	}
	if strings.Contains(got, "4. ") {
		t.Fatalf("only 3 runnable steps; got %q", got)
	}
	if !strings.Contains(got, "- advisory between") {
		t.Fatalf("advisory bullet missing; got %q", got)
	}
}
