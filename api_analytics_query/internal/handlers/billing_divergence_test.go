package handlers

import (
	"fmt"
	"testing"
	"time"
)

// A viewer-session cluster migration must move usage from the old cluster to
// the new one as PAIRED signed deltas: the old cluster is debited the full
// prior amount and the new cluster credited the full new amount. When the
// quantities are unchanged the per-meter net across clusters is zero — the
// correction relocates usage without inventing or destroying it.
func TestUsageAdjustments_ClusterMigrationNetZero(t *testing.T) {
	periodStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.Add(time.Hour)
	naturalKey := `{"cluster_id":"cluster-a","session_id":"s1"}`
	// 600s -> 10 delivered minutes; 2 GiB ingress; 3 GiB egress. Equal prior/new.
	const giB = 1024 * 1024 * 1024
	prior := fmt.Sprintf(`{"cluster_id":"cluster-old","duration_seconds":600,"uploaded_bytes":%d,"downloaded_bytes":%d}`, 2*giB, 3*giB)
	next := fmt.Sprintf(`{"cluster_id":"cluster-new","duration_seconds":600,"uploaded_bytes":%d,"downloaded_bytes":%d}`, 2*giB, 3*giB)

	adjustments, err := usageAdjustmentsFromProjectionDivergence(
		"viewer_sessions_final", "delivered_minutes", "cluster_id",
		naturalKey, prior, next, "evt-1", 1234, periodStart, periodEnd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(adjustments) != 6 {
		t.Fatalf("got %d adjustments, want 6 (3 meters x 2 clusters)", len(adjustments))
	}

	netByMeter := map[string]float64{}
	for _, a := range adjustments {
		netByMeter[a.UsageType] += a.DeltaValue
		switch a.ClusterID {
		case "cluster-old":
			if a.DeltaValue >= 0 {
				t.Errorf("old cluster %s delta %v, want negative (debit)", a.UsageType, a.DeltaValue)
			}
		case "cluster-new":
			if a.DeltaValue <= 0 {
				t.Errorf("new cluster %s delta %v, want positive (credit)", a.UsageType, a.DeltaValue)
			}
		default:
			t.Errorf("unexpected cluster id %q", a.ClusterID)
		}
		if a.PeriodStart != periodStart || a.PeriodEnd != periodEnd {
			t.Errorf("adjustment period = [%s,%s], want [%s,%s]", a.PeriodStart, a.PeriodEnd, periodStart, periodEnd)
		}
	}
	for meter, net := range netByMeter {
		if net != 0 {
			t.Errorf("meter %s net delta = %v, want 0 (relocation conserves usage)", meter, net)
		}
	}
	// Spot-check a magnitude: delivered_minutes old cluster = -10.
	for _, a := range adjustments {
		if a.UsageType == "delivered_minutes" && a.ClusterID == "cluster-old" && a.DeltaValue != -10 {
			t.Errorf("delivered_minutes old delta = %v, want -10", a.DeltaValue)
		}
	}
}

// SourceID is a content hash of the divergence's source material so a replayed
// projection correction collapses to the same adjustment row (idempotency).
// Distinct deltas within one divergence must still get distinct SourceIDs.
func TestUsageAdjustments_SourceIDIsStableAndPerDelta(t *testing.T) {
	periodStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.Add(time.Hour)
	naturalKey := `{"cluster_id":"cluster-a","session_id":"s1"}`
	const giB = 1024 * 1024 * 1024
	prior := fmt.Sprintf(`{"cluster_id":"cluster-old","duration_seconds":600,"uploaded_bytes":%d,"downloaded_bytes":%d}`, 2*giB, 3*giB)
	next := fmt.Sprintf(`{"cluster_id":"cluster-new","duration_seconds":900,"uploaded_bytes":%d,"downloaded_bytes":%d}`, 4*giB, 5*giB)

	call := func() []string {
		adj, err := usageAdjustmentsFromProjectionDivergence(
			"viewer_sessions_final", "delivered_minutes", "cluster_id",
			naturalKey, prior, next, "evt-1", 1234, periodStart, periodEnd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		ids := make([]string, len(adj))
		for i, a := range adj {
			ids[i] = a.SourceID
		}
		return ids
	}

	first := call()
	second := call()
	if len(first) != len(second) {
		t.Fatalf("non-deterministic count %d vs %d", len(first), len(second))
	}
	seen := map[string]bool{}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("SourceID[%d] not stable across replays: %s vs %s", i, first[i], second[i])
		}
		if seen[first[i]] {
			t.Errorf("duplicate SourceID %s across distinct deltas", first[i])
		}
		seen[first[i]] = true
	}
}

func TestAdjustmentDeltas_StorageGBSeconds(t *testing.T) {
	naturalKey := map[string]any{"window_start": "2026-04-01T00:00:00Z", "storage_scope": "cold"}
	prior := map[string]any{"gb_seconds": 100.0}
	next := map[string]any{"gb_seconds": 160.0}

	deltas, err := adjustmentDeltasFromProjectionDivergence("storage_gb_seconds_5m", "", naturalKey, prior, next, "cluster-a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deltas) != 1 {
		t.Fatalf("got %d deltas, want 1", len(deltas))
	}
	d := deltas[0]
	if d.usageType != "storage_gb_seconds_cold" {
		t.Errorf("usageType = %q, want storage_gb_seconds_cold", d.usageType)
	}
	if d.deltaValue != 60 {
		t.Errorf("deltaValue = %v, want 60 (160-100)", d.deltaValue)
	}
	wantStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	if !d.sourcePeriodStart.Equal(wantStart) || !d.sourcePeriodEnd.Equal(wantStart.Add(5*time.Minute)) {
		t.Errorf("source period = [%s,%s], want 5-minute window from %s", d.sourcePeriodStart, d.sourcePeriodEnd, wantStart)
	}
}

func TestStorageUsageType(t *testing.T) {
	if got := storageUsageType("cold"); got != "storage_gb_seconds_cold" {
		t.Errorf("cold -> %q", got)
	}
	if got := storageUsageType("hot"); got != "storage_gb_seconds_hot" {
		t.Errorf("hot -> %q", got)
	}
	if got := storageUsageType(""); got != "storage_gb_seconds_hot" {
		t.Errorf("empty defaults to hot, got %q", got)
	}
}
