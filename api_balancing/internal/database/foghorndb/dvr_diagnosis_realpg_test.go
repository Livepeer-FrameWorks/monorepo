//go:build schema_verify

package foghorndb

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestDVRDiagnosis_RealPG(t *testing.T) {
	verifyDVRDiagnosis(t, startFoghornCatalogPostgres(t))
}

func TestDVRDiagnosis_RealYugabyte(t *testing.T) {
	verifyDVRDiagnosis(t, startFoghornCatalogYugabyte(t))
}

func verifyDVRDiagnosis(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	const hash = "diagdvr0000000000000000000000001"
	exec(`INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status, sync_status, retention_until,
			dvr_chapter_mode, dvr_chapter_interval, dvr_start_dispatch)
		VALUES ($1, 'dvr', '00000000-0000-0000-0000-00000000000b', 'recording', 'failed', NOW() + INTERVAL '1 day',
			'fixed_interval', 3600, '{"state":"pending","node_id":"node-1"}'::jsonb)`, hash)
	// A clip sharing no hash must not be read as a DVR.
	exec(`INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id) VALUES ('diagclip000000000000000000000001', 'clip', '00000000-0000-0000-0000-00000000000b')`)

	// Segments: [0,2000) [1500,4000) overlap, then a hole to 5000, then [5000,6000), hole to 8000, [8000,9000).
	for i, seg := range []struct {
		start, end int64
		status     string
	}{{0, 2000, "uploaded"}, {1500, 4000, "uploaded"}, {5000, 6000, "lost_local"}, {8000, 9000, "pending"}} {
		exec(`INSERT INTO foghorn.dvr_segments (artifact_hash, segment_name, sequence, media_start_ms, media_end_ms, duration_ms, size_bytes, s3_key, status)
			VALUES ($1, $2, $3, $4, $5, $6, 100, 'k', $7)`, hash, "seg"+string(rune('a'+i)), int64(i+1), seg.start, seg.end, seg.end-seg.start, seg.status)
	}
	exec(`INSERT INTO foghorn.dvr_chapters (chapter_id, artifact_hash, mode, interval_seconds, start_ms, end_ms, state, finalize_attempts, last_failure_reason, created_at)
		VALUES ('diag-ch-1', $1, 'fixed_interval', 3600, 0, 3600000, 'failed_permanent', 4, 'source missing', NOW() - INTERVAL '3 hours')`, hash)
	exec(`INSERT INTO foghorn.dvr_chapters (chapter_id, artifact_hash, mode, interval_seconds, start_ms, end_ms, state, finalize_attempts, finalize_node_id, finalize_started_at, created_at)
		VALUES ('diag-ch-2', $1, 'fixed_interval', 3600, 3600000, 7200000, 'finalizing', 2, 'node-1', NOW(), NOW() - INTERVAL '2 hours')`, hash)
	exec(`INSERT INTO foghorn.dvr_chapters (chapter_id, artifact_hash, mode, interval_seconds, start_ms, end_ms, state, created_at)
		VALUES ('diag-ch-3', $1, 'fixed_interval', 3600, 7200000, 10800000, 'closed', NOW() - INTERVAL '1 hour')`, hash)
	exec(`INSERT INTO foghorn.dvr_chapters (chapter_id, artifact_hash, mode, interval_seconds, start_ms, end_ms, state, is_current, created_at)
		VALUES ('diag-ch-4', $1, 'fixed_interval', 3600, 10800000, 14400000, 'open', true, NOW())`, hash)

	q := New(db)
	if _, err := q.GetDVRDiagnosisRecording(ctx, "diagclip000000000000000000000001"); err != sql.ErrNoRows {
		t.Fatalf("clip read as DVR: %v", err)
	}
	rec, err := q.GetDVRDiagnosisRecording(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	if rec.TenantID != "00000000-0000-0000-0000-00000000000b" || rec.SyncStatus != "failed" || rec.StartDispatchState != "pending" ||
		!rec.RetentionUntil.Valid || rec.ChapterMode != "fixed_interval" || rec.ChapterIntervalSeconds != 3600 || rec.EndedAt.Valid {
		t.Fatalf("recording = %+v", rec)
	}

	summary, err := q.GetDVRDiagnosisSegmentSummary(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Count != 4 || summary.FirstMediaStartMs != 0 || summary.LastMediaEndMs != 9000 || summary.TotalDurationMs != 6500 || summary.TotalSizeBytes != 400 {
		t.Fatalf("summary = %+v", summary)
	}
	counts, err := q.ListDVRDiagnosisSegmentStatusCounts(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 3 || counts[0] != (DVRDiagnosisStatusCount{"lost_local", 1}) || counts[2] != (DVRDiagnosisStatusCount{"uploaded", 2}) {
		t.Fatalf("status counts = %+v", counts)
	}

	gaps, total, err := q.ListDVRDiagnosisSegmentGaps(ctx, hash, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := []DVRDiagnosisGap{{StartMs: 4000, EndMs: 5000, NextSequence: 3}, {StartMs: 6000, EndMs: 8000, NextSequence: 4}}
	if total != 2 || len(gaps) != 2 || gaps[0] != want[0] || gaps[1] != want[1] {
		t.Fatalf("gaps = %+v total %d", gaps, total)
	}
	capped, total, err := q.ListDVRDiagnosisSegmentGaps(ctx, hash, 1)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(capped) != 1 || capped[0] != want[0] {
		t.Fatalf("capped gaps = %+v total %d", capped, total)
	}

	chapters, err := q.ListDVRDiagnosisChapters(ctx, hash, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(chapters) != 3 || chapters[0].ChapterID != "diag-ch-2" || chapters[2].ChapterID != "diag-ch-4" || !chapters[2].IsCurrent {
		t.Fatalf("chapters (latest 3, ascending) = %+v", chapters)
	}
	pending, err := q.ListDVRDiagnosisPendingFinalize(ctx, hash, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 || pending[0].ChapterID != "diag-ch-2" || pending[0].FinalizeNodeID != "node-1" || pending[0].FinalizeAttempts != 2 ||
		!pending[0].FinalizeStartedAt.Valid || pending[1].ChapterID != "diag-ch-3" {
		t.Fatalf("pending = %+v", pending)
	}
}
