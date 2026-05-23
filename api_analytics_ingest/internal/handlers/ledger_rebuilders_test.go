package handlers

import (
	"testing"
	"time"
)

// windowsForSpan is the bucketing primitive every ledger rebuilder uses
// to split a source-time span across 5-minute windows. These fixtures
// cover the cases the contract calls out: boundary-crossing spans,
// degenerate spans, and zero-length cases.
func TestWindowsForSpan_SplitsAcrossBoundary(t *testing.T) {
	// 7-minute span starting at 12:03:00 and ending at 12:10:00.
	// 12:03 → 12:05  =  2 min in window starting 12:00
	// 12:05 → 12:10  =  5 min in window starting 12:05
	start := time.Date(2026, 5, 23, 12, 3, 0, 0, time.UTC).UnixMilli()
	end := time.Date(2026, 5, 23, 12, 10, 0, 0, time.UTC).UnixMilli()
	got := windowsForSpan(start, end)

	if len(got) != 2 {
		t.Fatalf("expected 2 windows for a span crossing one 5-min boundary, got %d: %v", len(got), got)
	}
	w12_00 := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC).UnixMilli()
	w12_05 := time.Date(2026, 5, 23, 12, 5, 0, 0, time.UTC).UnixMilli()
	if got[w12_00] != 2*60*1000 {
		t.Fatalf("expected 120000 ms in 12:00 window, got %d", got[w12_00])
	}
	if got[w12_05] != 5*60*1000 {
		t.Fatalf("expected 300000 ms in 12:05 window, got %d", got[w12_05])
	}
	// Sum across windows must equal total span — meter-contracts invariant.
	if got[w12_00]+got[w12_05] != end-start {
		t.Fatalf("overlap sum %d != span %d", got[w12_00]+got[w12_05], end-start)
	}
}

func TestWindowsForSpan_SpansManyWindows(t *testing.T) {
	// A 24-hour span starting at 00:00:00 should produce 24*12 = 288
	// windows, each 5 minutes long.
	start := time.Date(2026, 5, 23, 0, 0, 0, 0, time.UTC).UnixMilli()
	end := start + 24*60*60*1000
	got := windowsForSpan(start, end)

	expected := 24 * 12
	if len(got) != expected {
		t.Fatalf("expected %d windows for a 24-hour span, got %d", expected, len(got))
	}
	for w, ms := range got {
		if ms != 5*60*1000 {
			t.Fatalf("window %d has overlap %d ms, expected 300000", w, ms)
		}
	}
}

func TestWindowsForSpan_DegenerateAndZero(t *testing.T) {
	start := time.Date(2026, 5, 23, 12, 3, 0, 0, time.UTC).UnixMilli()

	// Zero-length span — no windows.
	if got := windowsForSpan(start, start); len(got) != 0 {
		t.Fatalf("zero-length span should produce no windows, got %v", got)
	}

	// Negative span — no windows.
	if got := windowsForSpan(start, start-1); len(got) != 0 {
		t.Fatalf("negative span should produce no windows, got %v", got)
	}

	// Span that fits entirely inside one window.
	end := start + 30*1000 // 30 seconds
	got := windowsForSpan(start, end)
	if len(got) != 1 {
		t.Fatalf("sub-window span should produce one window, got %d", len(got))
	}
	for _, ms := range got {
		if ms != 30*1000 {
			t.Fatalf("expected 30000 ms overlap, got %d", ms)
		}
	}
}

func TestWindowsForSpan_AlignedBoundary(t *testing.T) {
	// Span starting exactly on a 5-min boundary and ending exactly on
	// the next — single window, full 5 minutes.
	start := time.Date(2026, 5, 23, 12, 5, 0, 0, time.UTC).UnixMilli()
	end := time.Date(2026, 5, 23, 12, 10, 0, 0, time.UTC).UnixMilli()
	got := windowsForSpan(start, end)

	if len(got) != 1 {
		t.Fatalf("boundary-aligned 5-min span should produce 1 window, got %d", len(got))
	}
	if got[start] != 5*60*1000 {
		t.Fatalf("expected full 5-min overlap in the start window, got %d", got[start])
	}
	// The 12:10 window itself must not appear (half-open).
	if _, ok := got[end]; ok {
		t.Fatalf("end boundary should be exclusive, got an entry at %d", end)
	}
}
