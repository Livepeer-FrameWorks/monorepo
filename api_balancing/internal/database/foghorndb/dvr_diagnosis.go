package foghorndb

import (
	"context"
	"database/sql"
	"fmt"
)

// ChapterFinalizeJobID is the job ID of one chapter finalize attempt. Chapter
// finalization does not use processing_jobs; the attempt number in the ID is
// what fences progress, results, and Livepeer job claims to one dispatch.
func ChapterFinalizeJobID(attempt int32, chapterID string) string {
	return fmt.Sprintf("chapter-finalize-v2-%d-%s", attempt, chapterID)
}

// DVRDiagnosisChapterLimit bounds the chapter and finalize-queue lists of one
// diagnosis; a recording of an always-on stream accumulates chapters without
// end.
const DVRDiagnosisChapterLimit = 1000

type DVRDiagnosisRecording struct {
	ArtifactHash            string
	TenantID                string
	StreamInternalName      string
	InternalName            string
	Status                  string
	ErrorMessage            string
	OriginClusterID         string
	StorageClusterID        string
	FederatedPointer        bool
	StorageLocation         string
	SyncStatus              string
	SyncError               string
	LastSyncAttempt         sql.NullTime
	SyncNodeID              string
	FailureCount            int32
	DtshSynced              bool
	DtshStatus              string
	DtshFailureCount        int32
	StartedAt               sql.NullTime
	EndedAt                 sql.NullTime
	DurationSeconds         int64
	SizeBytes               int64
	RetentionUntil          sql.NullTime
	FrozenAt                sql.NullTime
	ChapterMode             string
	ChapterIntervalSeconds  int32
	ChapterBackfillComplete bool
	IngestGeneration        string
	StartDispatchState      string
}

// GetDVRDiagnosisRecording reads one DVR row by hash without a tenant filter;
// its only caller is the platform-operator DiagnoseDVR RPC.
func (q *Queries) GetDVRDiagnosisRecording(ctx context.Context, dvrHash string) (DVRDiagnosisRecording, error) {
	var r DVRDiagnosisRecording
	err := q.db.QueryRowContext(ctx, `
		SELECT artifact_hash, tenant_id::text,
		       COALESCE(stream_internal_name, ''), COALESCE(internal_name, ''),
		       COALESCE(status, ''), COALESCE(error_message, ''),
		       COALESCE(origin_cluster_id, ''), COALESCE(storage_cluster_id, ''), federated_pointer,
		       COALESCE(storage_location, ''), COALESCE(sync_status, ''), COALESCE(sync_error, ''),
		       last_sync_attempt, COALESCE(sync_node_id, ''), failure_count,
		       COALESCE(dtsh_synced, false), COALESCE(dtsh_status, ''), dtsh_failure_count,
		       started_at, ended_at, COALESCE(duration_seconds, 0)::bigint, COALESCE(size_bytes, 0),
		       retention_until, frozen_at,
		       COALESCE(dvr_chapter_mode, ''), COALESCE(dvr_chapter_interval, 0), dvr_chapter_backfill_complete,
		       COALESCE(ingest_generation::text, ''), COALESCE(dvr_start_dispatch->>'state', '')
		FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND artifact_type = 'dvr'
	`, dvrHash).Scan(
		&r.ArtifactHash, &r.TenantID,
		&r.StreamInternalName, &r.InternalName,
		&r.Status, &r.ErrorMessage,
		&r.OriginClusterID, &r.StorageClusterID, &r.FederatedPointer,
		&r.StorageLocation, &r.SyncStatus, &r.SyncError,
		&r.LastSyncAttempt, &r.SyncNodeID, &r.FailureCount,
		&r.DtshSynced, &r.DtshStatus, &r.DtshFailureCount,
		&r.StartedAt, &r.EndedAt, &r.DurationSeconds, &r.SizeBytes,
		&r.RetentionUntil, &r.FrozenAt,
		&r.ChapterMode, &r.ChapterIntervalSeconds, &r.ChapterBackfillComplete,
		&r.IngestGeneration, &r.StartDispatchState,
	)
	return r, err
}

type DVRDiagnosisSegmentSummary struct {
	Count             int64
	FirstMediaStartMs int64
	LastMediaEndMs    int64
	TotalDurationMs   int64
	TotalSizeBytes    int64
}

func (q *Queries) GetDVRDiagnosisSegmentSummary(ctx context.Context, dvrHash string) (DVRDiagnosisSegmentSummary, error) {
	var s DVRDiagnosisSegmentSummary
	err := q.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(MIN(media_start_ms), 0), COALESCE(MAX(media_end_ms), 0),
		       COALESCE(SUM(duration_ms), 0)::bigint, COALESCE(SUM(size_bytes), 0)::bigint
		FROM foghorn.dvr_segments
		WHERE artifact_hash = $1
	`, dvrHash).Scan(&s.Count, &s.FirstMediaStartMs, &s.LastMediaEndMs, &s.TotalDurationMs, &s.TotalSizeBytes)
	return s, err
}

type DVRDiagnosisStatusCount struct {
	Status string
	Count  int64
}

func (q *Queries) ListDVRDiagnosisSegmentStatusCounts(ctx context.Context, dvrHash string) ([]DVRDiagnosisStatusCount, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT status, COUNT(*)
		FROM foghorn.dvr_segments
		WHERE artifact_hash = $1
		GROUP BY status
		ORDER BY status
	`, dvrHash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DVRDiagnosisStatusCount
	for rows.Next() {
		var c DVRDiagnosisStatusCount
		if err := rows.Scan(&c.Status, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type DVRDiagnosisGap struct {
	StartMs      int64
	EndMs        int64
	NextSequence int64
}

// ListDVRDiagnosisSegmentGaps returns up to limit gaps in media order and the
// total gap count. A gap starts at the furthest end of every earlier segment,
// so overlapping segments never report a false gap.
func (q *Queries) ListDVRDiagnosisSegmentGaps(ctx context.Context, dvrHash string, limit int32) ([]DVRDiagnosisGap, int64, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT prior_end, media_start_ms, sequence, COUNT(*) OVER ()
		FROM (
			SELECT media_start_ms, sequence,
			       MAX(media_end_ms) OVER (
			           ORDER BY media_start_ms, sequence
			           ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING
			       ) AS prior_end
			FROM foghorn.dvr_segments
			WHERE artifact_hash = $1
		) ordered
		WHERE prior_end IS NOT NULL AND media_start_ms > prior_end
		ORDER BY media_start_ms, sequence
		LIMIT $2
	`, dvrHash, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []DVRDiagnosisGap
	var total int64
	for rows.Next() {
		var g DVRDiagnosisGap
		if err := rows.Scan(&g.StartMs, &g.EndMs, &g.NextSequence, &total); err != nil {
			return nil, 0, err
		}
		out = append(out, g)
	}
	return out, total, rows.Err()
}

type DVRDiagnosisChapter struct {
	ChapterID            string
	Mode                 string
	State                string
	StartMs              int64
	EndMs                int64
	IsCurrent            bool
	FinalizeAttempts     int32
	LastFailureReason    string
	FinalizeNodeID       string
	FinalizeStartedAt    sql.NullTime
	FrozenAt             sql.NullTime
	ReclaimStartedAt     sql.NullTime
	CreatedAt            sql.NullTime
	SegmentCount         int32
	HasGaps              bool
	PlaybackArtifactHash string
	ActualMediaStartMs   sql.NullInt64
	ActualMediaEndMs     sql.NullInt64
}

const dvrDiagnosisChapterColumns = `
	chapter_id, mode, state, start_ms, end_ms, is_current, finalize_attempts,
	COALESCE(last_failure_reason, ''), COALESCE(finalize_node_id, ''),
	finalize_started_at, frozen_at, reclaim_started_at, created_at,
	segment_count, has_gaps, COALESCE(playback_artifact_hash, ''),
	actual_media_start_ms, actual_media_end_ms`

// ListDVRDiagnosisChapters returns the latest limit chapters of a recording,
// ordered by start_ms.
func (q *Queries) ListDVRDiagnosisChapters(ctx context.Context, dvrHash string, limit int32) ([]DVRDiagnosisChapter, error) {
	return q.listDVRDiagnosisChapters(ctx, `
		SELECT * FROM (
			SELECT `+dvrDiagnosisChapterColumns+`
			FROM foghorn.dvr_chapters
			WHERE artifact_hash = $1
			ORDER BY start_ms DESC, chapter_id DESC
			LIMIT $2
		) latest
		ORDER BY start_ms, chapter_id
	`, dvrHash, limit)
}

// ListDVRDiagnosisPendingFinalize returns the recording's chapters in the
// finalize queue (closed or finalizing), oldest first as the queue drains them.
func (q *Queries) ListDVRDiagnosisPendingFinalize(ctx context.Context, dvrHash string, limit int32) ([]DVRDiagnosisChapter, error) {
	return q.listDVRDiagnosisChapters(ctx, `
		SELECT `+dvrDiagnosisChapterColumns+`
		FROM foghorn.dvr_chapters
		WHERE artifact_hash = $1 AND state IN ('closed', 'finalizing')
		ORDER BY created_at, chapter_id
		LIMIT $2
	`, dvrHash, limit)
}

func (q *Queries) listDVRDiagnosisChapters(ctx context.Context, query, dvrHash string, limit int32) ([]DVRDiagnosisChapter, error) {
	rows, err := q.db.QueryContext(ctx, query, dvrHash, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DVRDiagnosisChapter
	for rows.Next() {
		var c DVRDiagnosisChapter
		if err := rows.Scan(
			&c.ChapterID, &c.Mode, &c.State, &c.StartMs, &c.EndMs, &c.IsCurrent, &c.FinalizeAttempts,
			&c.LastFailureReason, &c.FinalizeNodeID,
			&c.FinalizeStartedAt, &c.FrozenAt, &c.ReclaimStartedAt, &c.CreatedAt,
			&c.SegmentCount, &c.HasGaps, &c.PlaybackArtifactHash,
			&c.ActualMediaStartMs, &c.ActualMediaEndMs,
		); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
