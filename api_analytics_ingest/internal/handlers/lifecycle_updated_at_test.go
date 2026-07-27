package handlers

import (
	"testing"
	"time"
)

// artifact_state_current collapses on the SOURCE transition time when the producer stamped it, so a
// replayed older transition keeps its original stable source_updated_at_ms instead of a fresh receipt
// time (best-effort ordering; see the helper doc). Only when no source time is stamped does it fall
// back to receipt.
func TestLifecycleUpdatedAt_PrefersStableSourceTime(t *testing.T) {
	receipt := time.UnixMilli(2_000)

	// A replayed COMPLETED (source t=1000) arriving after a DELETED (source t=1500) landed: even though
	// its receipt time (2000) is the latest, its stable source time keeps it BEHIND the newer DELETED.
	if got := lifecycleUpdatedAt(1_000, receipt); !got.Equal(time.UnixMilli(1_000)) {
		t.Fatalf("expected source time 1000ms, got %v", got.UnixMilli())
	}
	// A best-effort event with no stamped source time falls back to receipt.
	if got := lifecycleUpdatedAt(0, receipt); !got.Equal(receipt) {
		t.Fatalf("expected receipt fallback %v, got %v", receipt, got)
	}
}
