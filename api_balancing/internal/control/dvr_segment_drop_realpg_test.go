//go:build schema_verify

package control

import (
	"testing"
	"time"
)

// A drop report is a loss only when it changes the ledger. An uploaded
// segment is held by S3, a reclaimed one was removed on purpose, and a
// segment already recorded as lost stays so; reports about them, repeated on
// every Helmsman reconnect, change nothing and report no loss.
func TestDVRSegmentDropChangesOnlyLiveRows_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prevDB := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prevDB) })
	const tenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	const hash = "recording-segment-drops"
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Second).UnixMilli()
	if _, err := conn.ExecContext(t.Context(), `INSERT INTO foghorn.artifacts
		(artifact_hash, artifact_type, tenant_id, status, stream_internal_name, origin_cluster_id, dvr_start_dispatch)
		VALUES ($1, 'dvr', $2, 'completed', 'live-stream', 'cluster-eu', '{"node_id":"owner"}')`, hash, tenant); err != nil {
		t.Fatal(err)
	}
	for i, row := range []struct{ name, status string }{
		{"uploaded.ts", "uploaded"}, {"reclaimed.ts", "reclaimed"}, {"pending.ts", "pending"},
	} {
		if _, err := conn.ExecContext(t.Context(), `INSERT INTO foghorn.dvr_segments
			(artifact_hash, segment_name, sequence, media_start_ms, media_end_ms, duration_ms, s3_key, status)
			VALUES ($1, $2, $3, $4::bigint, $4::bigint + 6000, 6000, 'dvr/' || $2, $5)`,
			hash, row.name, i+1, start+int64(i)*6000, row.status); err != nil {
			t.Fatal(err)
		}
	}
	status := func(name string) string {
		t.Helper()
		var s string
		if err := conn.QueryRowContext(t.Context(), `SELECT status FROM foghorn.dvr_segments WHERE artifact_hash = $1 AND segment_name = $2`, hash, name).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	drop := func(name string, offset int64) bool {
		t.Helper()
		changed, err := MarkDVRSegmentDropped(t.Context(), tenant, hash, name, "missing_at_startup", false, start+offset, start+offset+6000, 6000, 0)
		if err != nil {
			t.Fatal(err)
		}
		return changed
	}

	for _, name := range []string{"uploaded.ts", "reclaimed.ts"} {
		before := status(name)
		if drop(name, 0) || status(name) != before {
			t.Fatalf("a drop of a %s segment changed it to %s", before, status(name))
		}
	}
	if !drop("pending.ts", 12000) || status("pending.ts") != "lost_local" {
		t.Fatalf("a pending segment's loss was not recorded: %s", status("pending.ts"))
	}
	if drop("pending.ts", 12000) {
		t.Fatal("the same loss reported again counted as a new loss")
	}
	if !drop("never-recorded.ts", 18000) || status("never-recorded.ts") != "lost_local" {
		t.Fatal("a loss of an unrecorded segment was not recorded")
	}
	if drop("never-recorded.ts", 18000) {
		t.Fatal("the recorded tombstone was reported as a new loss")
	}
}
