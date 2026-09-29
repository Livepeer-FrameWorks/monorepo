//go:build schema_verify

package control

import (
	"testing"
	"time"
)

// A recording's finalized duration is the media span its segments cover, not how long
// the writer ran: the push outlives the publisher for Mist's resume window, so the
// reported wall-clock duration is longer than what the recording holds.
func TestDVRFinalizedDurationIsTheRecordedSpan_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prevDB := db
	SetDB(conn)
	prevLocal := localClusterID
	SetLocalClusterID("cluster-eu")
	prevRetry := FinalizeRetrySeconds
	FinalizeRetrySeconds = 0
	t.Cleanup(func() {
		SetDB(prevDB)
		SetLocalClusterID(prevLocal)
		FinalizeRetrySeconds = prevRetry
	})
	const tenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	const hash = "recording-duration-span"
	start := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)
	if _, err := conn.ExecContext(t.Context(), `INSERT INTO foghorn.artifacts
		(artifact_hash, artifact_type, tenant_id, status, stream_internal_name, origin_cluster_id, dvr_start_dispatch, size_bytes)
		VALUES ($1, 'dvr', $2, 'recording', 'live-stream', 'cluster-eu', '{"node_id":"owner"}', 4096)`, hash, tenant); err != nil {
		t.Fatal(err)
	}
	// Two sessions of one resumed recording: 0-6 s, then 16-22 s after a reconnect.
	for i, seg := range []struct {
		name           string
		startMs, endMs int64
	}{{"a.ts", 0, 6000}, {"b.ts", 16000, 22000}} {
		if _, err := conn.ExecContext(t.Context(), `INSERT INTO foghorn.dvr_segments
			(artifact_hash, segment_name, sequence, media_start_ms, media_end_ms, duration_ms, s3_key, status)
			VALUES ($1, $2, $3, $4::bigint, $5::bigint, $5::bigint - $4::bigint, 'dvr/' || $2, 'pending')`,
			hash, seg.name, i+1, start.UnixMilli()+seg.startMs, start.UnixMilli()+seg.endMs); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"a.ts", "b.ts"} {
		if err := MarkDVRSegmentUploaded(t.Context(), tenant, hash, name, 2048); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.ExecContext(t.Context(), `UPDATE foghorn.artifacts SET ended_at = $2 WHERE artifact_hash = $1`, hash, start.Add(40*time.Second)); err != nil {
		t.Fatal(err)
	}
	result, err := FinalizeDVR(t.Context(), hash, FinalizeOptions{ReportingNodeID: "owner", StartedAtUnix: start.Unix(), DurationSeconds: 40})
	if err != nil || result.ArtifactStatus != "completed" {
		t.Fatalf("finalize = %+v, %v", result, err)
	}
	var duration int
	if err := conn.QueryRowContext(t.Context(), `SELECT duration_seconds FROM foghorn.artifacts WHERE artifact_hash = $1`, hash).Scan(&duration); err != nil {
		t.Fatal(err)
	}
	if duration != 22 {
		t.Fatalf("finalized duration = %d s, want the 22 s the segments span (the writer reported 40 s)", duration)
	}
}
