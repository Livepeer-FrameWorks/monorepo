package preflight

import (
	"strings"
	"testing"
)

func TestParseDFKilobytes_FieldsBoundaryAndScaling(t *testing.T) {
	t.Parallel()
	// Exactly 5 fields on the data line is the minimum valid form.
	free, total, err := parseDFKilobytes("Filesystem 1024-blocks Used Available Capacity\n/dev/sda1 1000 100 900 10%")
	if err != nil {
		t.Fatalf("5-field line must parse; got %v", err)
	}
	// avail*1024, total*1024 conversion.
	if free != 900*1024 {
		t.Fatalf("free=%d, want %d", free, 900*1024)
	}
	if total != 1000*1024 {
		t.Fatalf("total=%d, want %d", total, 1000*1024)
	}

	// 4 fields → rejected.
	if _, _, err := parseDFKilobytes("h\na b c d"); err == nil {
		t.Fatal("4-field data line must be rejected")
	}

	// Only a header (1 line) → rejected.
	if _, _, err := parseDFKilobytes("just-a-header"); err == nil {
		t.Fatal("single-line df must be rejected")
	}
}

func TestEvaluateDiskSpace_PercentArithmetic(t *testing.T) {
	t.Parallel()
	// free=250, total=1000 → exactly 25.0%.
	c := evaluateDiskSpace("d", "/x", 250, 1000, 0, 0)
	if !c.OK {
		t.Fatalf("no thresholds → OK; got %#v", c)
	}
	if !strings.Contains(c.Detail, "25.0%") {
		t.Fatalf("expected 25.0%% free; got %q", c.Detail)
	}
}

func TestEvaluateDiskSpace_BytesThresholdBoundary(t *testing.T) {
	t.Parallel()
	// freeBytes exactly == minFreeBytes must PASS (>=, not >).
	atBytes := evaluateDiskSpace("d", "/x", 500, 1000, 500, 0)
	if !atBytes.OK {
		t.Fatalf("free==minBytes must pass; got %#v", atBytes)
	}
	// one below must FAIL.
	below := evaluateDiskSpace("d", "/x", 499, 1000, 500, 0)
	if below.OK {
		t.Fatalf("free<minBytes must fail; got %#v", below)
	}
}

func TestEvaluateDiskSpace_PercentThresholdBoundary(t *testing.T) {
	t.Parallel()
	// freePercent exactly == minFreePercent must PASS.
	// free=300,total=1000 → 30.0%.
	at := evaluateDiskSpace("d", "/x", 300, 1000, 0, 30)
	if !at.OK {
		t.Fatalf("freePercent==min must pass; got %#v", at)
	}
	// 29.9% < 30 must fail. free=299,total=1000 → 29.9%.
	below := evaluateDiskSpace("d", "/x", 299, 1000, 0, 30)
	if below.OK {
		t.Fatalf("freePercent<min must fail; got %#v", below)
	}
}

func TestEvaluateDiskSpace_ZeroTotal(t *testing.T) {
	t.Parallel()
	c := evaluateDiskSpace("d", "/x", 0, 0, 0, 0)
	if c.OK || !strings.Contains(c.Detail, "0") {
		t.Fatalf("zero total must fail; got %#v", c)
	}
}

func TestEvaluateDiskSpace_ThresholdDetailGate(t *testing.T) {
	t.Parallel()
	// No thresholds → no "(min ...)" suffix.
	none := evaluateDiskSpace("d", "/x", 500, 1000, 0, 0)
	if strings.Contains(none.Detail, "min ") {
		t.Fatalf("no thresholds must omit min suffix; got %q", none.Detail)
	}
	// Only a byte threshold → suffix present.
	withBytes := evaluateDiskSpace("d", "/x", 500, 1000, 100, 0)
	if !strings.Contains(withBytes.Detail, "min ") {
		t.Fatalf("byte threshold must add min suffix; got %q", withBytes.Detail)
	}
	// Only a percent threshold → suffix present.
	withPct := evaluateDiskSpace("d", "/x", 500, 1000, 0, 10)
	if !strings.Contains(withPct.Detail, "min ") {
		t.Fatalf("percent threshold must add min suffix; got %q", withPct.Detail)
	}
}
