package grpc

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/DATA-DOG/go-sqlmock"
)

// Wave 5 (suffix -ClipDvrVod): exercises the VOD post-upload pipeline state
// machine (vod_pipeline.go, previously 0%) plus the remaining clip-source
// dispatch arms (live-fully-covers short circuit, tenant scoping, no-DVR
// lookup, db-not-configured guards). Every test pins a real invariant: the
// vod-only result gate, the success/failure state transitions, the metadata
// null-coalescing UPDATE, and tenant_id filtering on the recording lookup.

const vodPipelineTenantID = "00000000-0000-0000-0000-0000000000ab"

func newVodPipeline(t *testing.T) (*VodPipeline, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Nil decklog client keeps the lifecycle-emit goroutine branches off, so
	// the state-transition asserts stay deterministic (no async outbox).
	return &VodPipeline{db: db, logger: logging.NewLogger(), decklogClient: nil}, mock
}

// expectJobLookup mocks the processing_jobs -> artifacts join that
// HandleJobResult reads first to learn the artifact type. Empty artifactType
// or "vod" both reach the gate; pass scanErr to drive the lookup-failure arm.
func expectJobLookup(mock sqlmock.Sqlmock, jobID, artifactHash, tenantID, artifactType string, scanErr error) {
	q := mock.ExpectQuery(`SELECT pj.artifact_hash, pj.tenant_id`).WithArgs(jobID)
	if scanErr != nil {
		q.WillReturnError(scanErr)
		return
	}
	q.WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "tenant_id", "artifact_type"}).
		AddRow(artifactHash, tenantID, artifactType))
}

// HandleJobResult must short-circuit on any non-vod artifact type: a 'clip' or
// 'dvr' result belongs to a different pipeline and must NOT mutate vod state.
func TestHandleJobResult_NonVodArtifactIsIgnored_ClipDvrVod(t *testing.T) {
	p, mock := newVodPipeline(t)
	expectJobLookup(mock, "job-1", "art-1", vodPipelineTenantID, "clip", nil)
	// No further UPDATE expectations: a non-vod result must touch nothing else.

	p.HandleJobResult(context.Background(), "job-1", "completed", map[string]string{"width": "1920"}, "")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected DB activity for non-vod result: %v", err)
	}
}

// A lookup error aborts before any state change.
func TestHandleJobResult_LookupErrorAborts_ClipDvrVod(t *testing.T) {
	p, mock := newVodPipeline(t)
	expectJobLookup(mock, "job-x", "", "", "", errors.New("db down"))

	p.HandleJobResult(context.Background(), "job-x", "completed", nil, "")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expected only the failing lookup: %v", err)
	}
}

// failed result -> markArtifactFailed flips the artifact to 'failed' (and
// nothing else, since decklog is nil). State transition invariant.
func TestHandleJobResult_FailedTransitionsArtifactToFailed_ClipDvrVod(t *testing.T) {
	p, mock := newVodPipeline(t)
	expectJobLookup(mock, "job-f", "art-f", vodPipelineTenantID, "vod", nil)
	mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET status = 'failed'`).
		WithArgs("art-f").
		WillReturnResult(sqlmock.NewResult(0, 1))

	p.HandleJobResult(context.Background(), "job-f", "failed", nil, "transcode exploded")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("failed-path expectations: %v", err)
	}
}

// completed result WITH outputs -> updateVodMetadata then markArtifactReady.
// Asserts ordering (metadata before ready) and the null-coalescing UPDATE:
// present keys pass through, absent keys become NULL.
func TestHandleJobResult_SuccessWritesMetadataThenMarksReady_ClipDvrVod(t *testing.T) {
	p, mock := newVodPipeline(t)
	expectJobLookup(mock, "job-ok", "art-ok", vodPipelineTenantID, "vod", nil)
	// duration_ms/width/height present; the remaining columns coalesce to NULL.
	mock.ExpectExec(`UPDATE foghorn.vod_metadata`).
		WithArgs(
			"art-ok",
			"5000", // duration_ms present
			nil,    // resolution absent
			"h264", // video_codec present
			nil,    // audio_codec absent
			nil,    // bitrate_kbps absent
			"1920", // width present
			"1080", // height present
			nil,    // fps absent
			nil,    // audio_channels absent
			nil,    // audio_sample_rate absent
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET status = 'ready'`).
		WithArgs("art-ok").
		WillReturnResult(sqlmock.NewResult(0, 1))

	p.HandleJobResult(context.Background(), "job-ok", "completed",
		map[string]string{
			"duration_ms": "5000",
			"video_codec": "h264",
			"width":       "1920",
			"height":      "1080",
		}, "")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("success-path expectations: %v", err)
	}
}

// completed result with NO outputs skips the metadata UPDATE entirely and goes
// straight to ready — empty outputs must not issue a vod_metadata write.
func TestHandleJobResult_SuccessNoOutputsSkipsMetadata_ClipDvrVod(t *testing.T) {
	p, mock := newVodPipeline(t)
	expectJobLookup(mock, "job-bare", "art-bare", vodPipelineTenantID, "vod", nil)
	mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET status = 'ready'`).
		WithArgs("art-bare").
		WillReturnResult(sqlmock.NewResult(0, 1))

	p.HandleJobResult(context.Background(), "job-bare", "completed", map[string]string{}, "")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("no-outputs path must skip metadata UPDATE: %v", err)
	}
}

// markArtifactReady tolerates a DB error on the status UPDATE without panicking
// and without proceeding to emit (decklog nil anyway): the early return arm.
func TestMarkArtifactReady_UpdateErrorReturnsCleanly_ClipDvrVod(t *testing.T) {
	p, mock := newVodPipeline(t)
	mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET status = 'ready'`).
		WithArgs("art-err").
		WillReturnError(errors.New("write conflict"))

	p.markArtifactReady(context.Background(), p.logger.WithField("t", "x"), "art-err", vodPipelineTenantID)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("ready update error path: %v", err)
	}
}

// markArtifactFailed tolerates a DB error on the status UPDATE.
func TestMarkArtifactFailed_UpdateErrorReturnsCleanly_ClipDvrVod(t *testing.T) {
	p, mock := newVodPipeline(t)
	mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET status = 'failed'`).
		WithArgs("art-err").
		WillReturnError(errors.New("write conflict"))

	p.markArtifactFailed(context.Background(), p.logger.WithField("t", "x"), "art-err", vodPipelineTenantID, "boom")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("failed update error path: %v", err)
	}
}

// updateVodMetadata logs but does not propagate a DB error (best-effort).
func TestUpdateVodMetadata_ErrorIsSwallowed_ClipDvrVod(t *testing.T) {
	p, mock := newVodPipeline(t)
	mock.ExpectExec(`UPDATE foghorn.vod_metadata`).
		WithArgs("art-m", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil).
		WillReturnError(errors.New("no such row"))

	p.updateVodMetadata(context.Background(), p.logger.WithField("t", "x"), "art-m", map[string]string{})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("metadata error path: %v", err)
	}
}

// StartPipeline enqueues a single 'process' job for the VOD via the
// per-artifact serialized insert (advisory lock + dedupe SELECT + INSERT +
// clip-status UPDATE inside one tx). Pins that the queued job carries the
// tenant, artifact, and job_type 'process'.
func TestStartPipeline_EnqueuesProcessJob_ClipDvrVod(t *testing.T) {
	p, mock := newVodPipeline(t)
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs("art-start", "process").
		WillReturnResult(sqlmock.NewResult(0, 0))
	// No existing active job for this (artifact, job_type) -> insert a new one.
	mock.ExpectQuery(`SELECT job_id\s+FROM foghorn.processing_jobs`).
		WithArgs("art-start", "process").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO foghorn.processing_jobs`).
		WithArgs(
			sqlmock.AnyArg(), // generated job_id (uuid)
			vodPipelineTenantID,
			"art-start",
			"process",
			nil,  // parent_job_id
			"[]", // processes_json
			nil,  // source_url
			nil,  // source_params
			nil,  // preferred_node_id
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// The clip-status reconcile UPDATE is tenant-scoped; a VOD artifact matches
	// nothing (it is not a clip) but the statement still runs in the tx.
	mock.ExpectExec(`UPDATE foghorn.artifacts`).
		WithArgs("art-start", vodPipelineTenantID).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	if err := p.StartPipeline(context.Background(), vodPipelineTenantID, "art-start", "[]"); err != nil {
		t.Fatalf("StartPipeline: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("enqueue expectations: %v", err)
	}
}

// computeClipCoverages on an offline stream contributes no live coverage and
// proceeds to the DB-backed recorded sources, tenant-scoped. With both recorded
// lookups empty the result has no candidates at all (caller then rejects).
func TestComputeClipCoverages_OfflineFallsToRecordedLookups_ClipDvrVod(t *testing.T) {
	srv, mock := newDispatchServer(t)
	// No live stream seeded -> GetStreamState returns nil -> live is empty.
	state.ResetDefaultManagerForTests()
	streamName := "stream-offline-ClipDvrVod"

	startMs := int64(1_000_000)
	endMs := int64(1_600_000)
	mock.ExpectQuery(`SELECT artifact_hash`).
		WithArgs(streamName, testTenantID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT COALESCE\(c.playback_artifact_hash, ''\)`).
		WithArgs(streamName, startMs, endMs, testTenantID).
		WillReturnRows(sqlmock.NewRows([]string{"playback_artifact_hash", "ov_start", "ov_end"}))

	live, dvr, chap, err := srv.computeClipCoverages(context.Background(), testTenantID, streamName, startMs, endMs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if live.streamName != "" || dvr.streamName != "" || chap.streamName != "" {
		t.Fatalf("expected no candidates: live=%q dvr=%q chap=%q", live.streamName, dvr.streamName, chap.streamName)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("recorded lookups must run tenant-scoped: %v", err)
	}
}

// computeClipCoverages rejects an empty tenant_id before any source lookup:
// clip dispatch must be tenant-scoped.
func TestComputeClipCoverages_RequiresTenant_ClipDvrVod(t *testing.T) {
	srv, _ := newDispatchServer(t)
	_, _, _, err := srv.computeClipCoverages(context.Background(), "", "stream-1", 1000, 2000)
	if err == nil {
		t.Fatalf("expected tenant_id requirement error")
	}
}

// computeClipCoverages rejects a non-positive range up front.
func TestComputeClipCoverages_RejectsInvalidRange_ClipDvrVod(t *testing.T) {
	srv, _ := newDispatchServer(t)
	_, _, _, err := srv.computeClipCoverages(context.Background(), testTenantID, "stream-1", 2000, 2000)
	if err == nil {
		t.Fatalf("expected invalid-range error")
	}
}

// findRecordingDVR returns no DVR (not an error) when the artifacts lookup finds
// no row — the caller then falls through to chapter/live. Also pins the
// tenant_id filter on the lookup.
func TestFindRecordingDVR_NoRowsReturnsEmpty_ClipDvrVod(t *testing.T) {
	srv, mock := newDispatchServer(t)
	mock.ExpectQuery(`SELECT artifact_hash`).
		WithArgs("stream-1", testTenantID).
		WillReturnError(sql.ErrNoRows)

	hash, _, _, status, node, err := srv.findRecordingDVR(context.Background(), testTenantID, "stream-1")
	if err != nil {
		t.Fatalf("no-rows must not be an error: %v", err)
	}
	if hash != "" || status != "" || node != "" {
		t.Fatalf("expected empty DVR, got hash=%q status=%q node=%q", hash, status, node)
	}
}

// findRecordingDVR rejects an empty tenant_id: the recording lookup is
// tenant-scoped and must never run unfiltered.
func TestFindRecordingDVR_RequiresTenant_ClipDvrVod(t *testing.T) {
	srv, _ := newDispatchServer(t)
	_, _, _, _, _, err := srv.findRecordingDVR(context.Background(), "", "stream-1")
	if err == nil {
		t.Fatalf("expected tenant_id requirement error")
	}
}

// findRecordingDVR returns the row but a blank node when the DVR exists yet is
// not in an active status — a 'finalizing' DVR is no longer the rolling clip
// source, so no recording node is resolved.
func TestFindRecordingDVR_InactiveStatusYieldsNoNode_ClipDvrVod(t *testing.T) {
	srv, mock := newDispatchServer(t)
	mock.ExpectQuery(`SELECT artifact_hash`).
		WithArgs("stream-1", testTenantID).
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "internal_name", "started", "status"}).
			AddRow("dvr-h", "dvr-internal", int64(1000), "finalizing"))
	// No artifact_nodes query expected: inactive status short-circuits.

	hash, internal, _, status, node, err := srv.findRecordingDVR(context.Background(), testTenantID, "stream-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hash != "dvr-h" || internal != "dvr-internal" || status != "finalizing" {
		t.Fatalf("expected the inactive DVR row, got hash=%q internal=%q status=%q", hash, internal, status)
	}
	if node != "" {
		t.Fatalf("inactive DVR must resolve no recording node, got %q", node)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("inactive status must not query artifact_nodes: %v", err)
	}
}

// rollingDVRCoverageRange and chapterArtifactBestOverlap guard against a nil
// db; computeRecordedCoverages bubbles those. With s.db == nil the dispatcher
// cannot assess recorded sources and must error rather than claim no media.
func TestRecordedCoverages_DBNilErrors_ClipDvrVod(t *testing.T) {
	srv := &FoghornGRPCServer{db: nil, logger: logging.NewLogger()}
	// findRecordingDVR with nil db:
	_, _, _, _, _, fErr := srv.findRecordingDVR(context.Background(), testTenantID, "stream-1")
	if fErr == nil {
		t.Fatalf("expected db-not-configured error from findRecordingDVR")
	}
	// chapterArtifactBestOverlap with nil db:
	_, _, _, cErr := srv.chapterArtifactBestOverlap(context.Background(), testTenantID, "stream-1", 1000, 2000)
	if cErr == nil {
		t.Fatalf("expected db-not-configured error from chapterArtifactBestOverlap")
	}
	// rollingDVRCoverageRange with nil db:
	_, _, rErr := srv.rollingDVRCoverageRange(context.Background(), "dvr-h", 0, 1000, 2000)
	if rErr == nil {
		t.Fatalf("expected db-not-configured error from rollingDVRCoverageRange")
	}
}
