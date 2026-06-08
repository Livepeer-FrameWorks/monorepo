package handlers

import "testing"

// adjustmentDeltasFromProjectionDivergence is the per-meter unit-conversion
// core of usage-correction. The existing suite only exercises the storage
// branch and a viewer cluster-migration. These cases pin the remaining
// single-field conversions and the non-migration error guards, where a wrong
// divisor (seconds vs minutes, bytes vs GiB) or a swapped sign would silently
// over- or under-bill a corrected period.

func deltaFor(t *testing.T, deltas []projectionAdjustmentDelta, usageType, clusterID string) projectionAdjustmentDelta {
	t.Helper()
	for _, d := range deltas {
		if d.usageType == usageType && d.clusterID == clusterID {
			return d
		}
	}
	t.Fatalf("no delta for usageType=%q cluster=%q in %+v", usageType, clusterID, deltas)
	return projectionAdjustmentDelta{}
}

func TestAdjustmentDeltas_ViewerSingleFields(t *testing.T) {
	nk := map[string]any{"cluster_id": "c1"}

	// duration_seconds -> delivered_minutes, divided by 60.
	d, err := adjustmentDeltasFromProjectionDivergence("viewer_sessions_final", "duration_seconds", nk, 600.0, 900.0, "c1")
	if err != nil {
		t.Fatalf("duration_seconds: %v", err)
	}
	if got := deltaFor(t, d, "delivered_minutes", "c1"); got.deltaValue != 5 {
		t.Errorf("delivered_minutes delta = %v, want 5 ((900-600)/60)", got.deltaValue)
	}

	// uploaded_bytes -> ingress_gb, divided by 2^30.
	d, err = adjustmentDeltasFromProjectionDivergence("viewer_sessions_final", "uploaded_bytes", nk, float64(gibibyte), float64(3*gibibyte), "c1")
	if err != nil {
		t.Fatalf("uploaded_bytes: %v", err)
	}
	if got := deltaFor(t, d, "ingress_gb", "c1"); got.deltaValue != 2 {
		t.Errorf("ingress_gb delta = %v, want 2 GiB", got.deltaValue)
	}

	// downloaded_bytes -> egress_gb, divided by 2^30.
	d, err = adjustmentDeltasFromProjectionDivergence("viewer_sessions_final", "downloaded_bytes", nk, float64(5*gibibyte), float64(gibibyte), "c1")
	if err != nil {
		t.Fatalf("downloaded_bytes: %v", err)
	}
	if got := deltaFor(t, d, "egress_gb", "c1"); got.deltaValue != -4 {
		t.Errorf("egress_gb delta = %v, want -4 GiB (shrunk usage credits back)", got.deltaValue)
	}
}

func TestAdjustmentDeltas_StreamRuntimeAndMigration(t *testing.T) {
	nk := map[string]any{"cluster_id": "c1"}

	// runtime_seconds maps 1:1 to stream_runtime_seconds (no unit conversion).
	d, err := adjustmentDeltasFromProjectionDivergence("stream_sessions_final", "runtime_seconds", nk, 100.0, 250.0, "c1")
	if err != nil {
		t.Fatalf("runtime_seconds: %v", err)
	}
	if got := deltaFor(t, d, "stream_runtime_seconds", "c1"); got.deltaValue != 150 {
		t.Errorf("stream_runtime_seconds delta = %v, want 150", got.deltaValue)
	}

	// A stream cluster migration relocates runtime as paired signed deltas
	// (old debited, new credited); equal quantities net to zero per meter.
	prior := map[string]any{"cluster_id": "old", "runtime_seconds": 100.0}
	next := map[string]any{"cluster_id": "new", "runtime_seconds": 100.0}
	d, err = adjustmentDeltasFromProjectionDivergence("stream_sessions_final", "cluster_id", nk, prior, next, "ignored")
	if err != nil {
		t.Fatalf("stream cluster migration: %v", err)
	}
	if len(d) != 2 {
		t.Fatalf("got %d deltas, want 2", len(d))
	}
	if got := deltaFor(t, d, "stream_runtime_seconds", "old"); got.deltaValue != -100 {
		t.Errorf("old cluster delta = %v, want -100", got.deltaValue)
	}
	if got := deltaFor(t, d, "stream_runtime_seconds", "new"); got.deltaValue != 100 {
		t.Errorf("new cluster delta = %v, want 100", got.deltaValue)
	}
}

func TestAdjustmentDeltas_ProcessingMediaSecondsCarriesCodecIdentity(t *testing.T) {
	// process_type/output_codec ride on the natural key for single-field
	// media_seconds corrections — losing them would mis-price the codec meter.
	nk := map[string]any{"cluster_id": "c1", "process_type": "transcode", "output_codec": "h265"}
	d, err := adjustmentDeltasFromProjectionDivergence("processing_segments_final", "media_seconds", nk, 10.0, 30.0, "c1")
	if err != nil {
		t.Fatalf("media_seconds: %v", err)
	}
	got := deltaFor(t, d, "media_seconds", "c1")
	if got.deltaValue != 20 {
		t.Errorf("media_seconds delta = %v, want 20", got.deltaValue)
	}
	if got.processType != "transcode" || got.outputCodec != "h265" {
		t.Errorf("codec identity = (%q,%q), want (transcode,h265)", got.processType, got.outputCodec)
	}
}

func TestAdjustmentDeltas_ProcessingClusterMigrationPreservesCodec(t *testing.T) {
	nk := map[string]any{"cluster_id": "c1"}
	prior := map[string]any{"cluster_id": "old", "media_seconds": 50.0, "process_type": "transcode", "output_codec": "h264"}
	next := map[string]any{"cluster_id": "new", "media_seconds": 50.0, "process_type": "transcode", "output_codec": "h264"}
	d, err := adjustmentDeltasFromProjectionDivergence("processing_segments_final", "cluster_id", nk, prior, next, "ignored")
	if err != nil {
		t.Fatalf("processing cluster migration: %v", err)
	}
	if len(d) != 2 {
		t.Fatalf("got %d deltas, want 2", len(d))
	}
	oldD := deltaFor(t, d, "media_seconds", "old")
	newD := deltaFor(t, d, "media_seconds", "new")
	if oldD.deltaValue != -50 || newD.deltaValue != 50 {
		t.Errorf("migration deltas = (%v,%v), want (-50,50)", oldD.deltaValue, newD.deltaValue)
	}
	if oldD.outputCodec != "h264" || newD.outputCodec != "h264" {
		t.Errorf("codec lost across migration: old=%q new=%q", oldD.outputCodec, newD.outputCodec)
	}
}

func TestAdjustmentDeltas_ErrorGuards(t *testing.T) {
	nk := map[string]any{"cluster_id": "c1"}

	if _, err := adjustmentDeltasFromProjectionDivergence("unknown_table", "x", nk, 1.0, 2.0, "c1"); err == nil {
		t.Error("unsupported table: want error")
	}
	if _, err := adjustmentDeltasFromProjectionDivergence("viewer_sessions_final", "bogus_field", nk, 1.0, 2.0, "c1"); err == nil {
		t.Error("unsupported viewer field: want error")
	}
	if _, err := adjustmentDeltasFromProjectionDivergence("stream_sessions_final", "bogus", nk, 1.0, 2.0, "c1"); err == nil {
		t.Error("unsupported stream field: want error")
	}
	if _, err := adjustmentDeltasFromProjectionDivergence("processing_segments_final", "bogus", nk, 1.0, 2.0, "c1"); err == nil {
		t.Error("unsupported processing field: want error")
	}
	// cluster_id migration requires both values to be JSON objects.
	if _, err := adjustmentDeltasFromProjectionDivergence("viewer_sessions_final", "cluster_id", nk, 1.0, 2.0, "c1"); err == nil {
		t.Error("viewer cluster migration with scalar values: want error")
	}
}
