package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// seedLiveStreamRpcMore registers a live, input-bearing stream for a tenant on a
// node so GetStreamsByTenant/GetStreamInstances surface it. GetStreamsByTenant
// requires Status=="live" && Inputs>0 (UpdateNodeStats sets Inputs); a bare
// UpdateStreamFromBuffer is not enough.
func seedLiveStreamRpcMore(t *testing.T, sm *state.StreamStateManager, internalName, nodeID, tenantID string) {
	t.Helper()
	if err := sm.UpdateStreamFromBuffer(internalName, internalName, nodeID, tenantID, "FULL", ""); err != nil {
		t.Fatalf("seed %s: %v", internalName, err)
	}
	sm.UpdateNodeStats(internalName, nodeID, 1, 1, 1024, 0, false)
}

// TestTerminateTenantStreams_OnlyTargetTenant locks the tenant-isolation
// invariant of the suspension path: TerminateTenantStreams must enumerate and
// stop ONLY the requested tenant's live streams, never a sibling tenant's, and
// must count one terminated session per node it successfully reached. With both
// the target node and a foreign-tenant node connected to the control registry,
// the response must report exactly the target tenant's stream and one session.
func TestTerminateTenantStreams_OnlyTargetTenant(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	// victim tenant has one live stream on node-victim; bystander tenant has a
	// live stream on node-bystander that must be left untouched.
	seedLiveStreamRpcMore(t, sm, "victim-stream", "node-victim", "tenant-victim")
	seedLiveStreamRpcMore(t, sm, "bystander-stream", "node-bystander", "tenant-bystander")

	// Connect node-victim so SendLocalStopSessions succeeds (returns nil) and the
	// session counter increments. node-bystander is intentionally NOT wired.
	victimStream := setupLocalRegistry(t, "node-victim")

	lb := balancer.NewLoadBalancer(logging.NewLogger())
	srv := NewFoghornGRPCServer(nil, logging.NewLogger(), lb, nil, nil, nil, nil, nil)

	resp, err := srv.TerminateTenantStreams(context.Background(), &foghorncontrolpb.TerminateTenantStreamsRequest{
		TenantId: "tenant-victim",
		Reason:   "insufficient_balance",
	})
	if err != nil {
		t.Fatalf("TerminateTenantStreams: %v", err)
	}

	if resp.StreamsTerminated != 1 {
		t.Fatalf("StreamsTerminated = %d, want exactly 1 (only the victim tenant)", resp.StreamsTerminated)
	}
	if len(resp.StreamNames) != 1 || resp.StreamNames[0] != "victim-stream" {
		t.Fatalf("StreamNames = %v, want [victim-stream] (bystander must not leak)", resp.StreamNames)
	}
	if resp.SessionsTerminated != 1 {
		t.Fatalf("SessionsTerminated = %d, want 1 (one reachable node)", resp.SessionsTerminated)
	}

	// The stop_sessions command must carry the requesting tenant + reason and the
	// victim stream name — and nothing about the bystander.
	if len(victimStream.sent) != 1 {
		t.Fatalf("expected one control message to node-victim, got %d", len(victimStream.sent))
	}
	stop := victimStream.sent[0].GetStopSessionsRequest()
	if stop == nil {
		t.Fatalf("expected StopSessionsRequest payload, got %+v", victimStream.sent[0])
	}
	if stop.TenantId != "tenant-victim" || stop.Reason != "insufficient_balance" {
		t.Fatalf("stop carried tenant=%q reason=%q, want tenant-victim/insufficient_balance", stop.TenantId, stop.Reason)
	}
	if len(stop.StreamNames) != 1 || stop.StreamNames[0] != "victim-stream" {
		t.Fatalf("stop.StreamNames = %v, want [victim-stream]", stop.StreamNames)
	}
}

// TestTerminateTenantStreams_RequiresTenantID pins the input-contract guard:
// an empty tenant_id is rejected with InvalidArgument before any state read,
// so a suspension RPC cannot accidentally fan out across all tenants.
func TestTerminateTenantStreams_RequiresTenantID(t *testing.T) {
	srv := NewFoghornGRPCServer(nil, logging.NewLogger(), balancer.NewLoadBalancer(logging.NewLogger()), nil, nil, nil, nil, nil)

	_, err := srv.TerminateTenantStreams(context.Background(), &foghorncontrolpb.TerminateTenantStreamsRequest{})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("empty tenant_id: expected InvalidArgument, got %s", got)
	}
}

// TestTerminateTenantStreams_NoActiveStreamsIsZero pins the no-op terminal
// state: a tenant with no live streams yields a zeroed response (not an error,
// not a nil slice), so the suspension caller treats "nothing to stop" as success.
func TestTerminateTenantStreams_NoActiveStreamsIsZero(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	srv := NewFoghornGRPCServer(nil, logging.NewLogger(), balancer.NewLoadBalancer(logging.NewLogger()), nil, nil, nil, nil, nil)

	resp, err := srv.TerminateTenantStreams(context.Background(), &foghorncontrolpb.TerminateTenantStreamsRequest{
		TenantId: "tenant-empty",
		Reason:   "suspended",
	})
	if err != nil {
		t.Fatalf("TerminateTenantStreams: %v", err)
	}
	if resp.StreamsTerminated != 0 || resp.SessionsTerminated != 0 {
		t.Fatalf("expected zeroed counters, got streams=%d sessions=%d", resp.StreamsTerminated, resp.SessionsTerminated)
	}
	if resp.StreamNames == nil || len(resp.StreamNames) != 0 {
		t.Fatalf("expected empty (non-nil) StreamNames, got %v", resp.StreamNames)
	}
}

// chapterRowColsRpcMore mirrors the column projection of scanChapterRow so the
// mocked chapter read returns a row the RPC can map field-for-field.
func chapterRowColsRpcMore() []string {
	return []string{
		"chapter_id", "artifact_hash", "mode", "interval_seconds",
		"start_ms", "end_ms", "is_current",
		"state", "playback_artifact_hash", "playback_id", "finalize_attempts",
		"finalize_started_at", "frozen_at",
		"last_failure_reason", "reclaim_started_at",
		"segment_count", "has_gaps",
		"actual_media_start_ms", "actual_media_end_ms",
		"created_at",
	}
}

// withControlDBRpcMore points BOTH the server DB and the control package's
// package-global DB at one sqlmock so the chapter RPCs (which split reads across
// s.db and control.GetDB) see a single coherent mock. Restores the previous
// control DB on cleanup.
func withControlDBRpcMore(t *testing.T) (*FoghornGRPCServer, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	prev := control.GetDB()
	control.SetDB(db)
	t.Cleanup(func() { control.SetDB(prev) })
	srv := NewFoghornGRPCServer(db, logging.NewLogger(), nil, nil, nil, nil, nil, nil)
	return srv, mock
}

// TestRetrieveDVRChapter_HappyPathReturnsRow drives RetrieveDVRChapter all the
// way past the validation/mode arms into the chapter read. Invariant: with a
// concrete fixed_interval mode at/above the floor, an empty tenant_id (the
// internal-caller bypass) and a materialized chapter row, the RPC returns the
// row mapped field-for-field including the nullable playback_id and the actual
// media span. The chapter_id queried must be the canonical BuildChapterID hash.
func TestRetrieveDVRChapter_HappyPathReturnsRow(t *testing.T) {
	srv, mock := withControlDBRpcMore(t)

	const (
		artifactID = "dvr-artifact-1"
		startMs    = int64(0)
		endMs      = int64(3600000)
		interval   = int32(3600)
	)
	wantChapterID := control.BuildChapterID(artifactID, control.ChapterModeFixedInterval, interval, startMs, endMs)

	mock.ExpectQuery(`FROM foghorn\.dvr_chapters`).
		WithArgs(wantChapterID).
		WillReturnRows(sqlmock.NewRows(chapterRowColsRpcMore()).AddRow(
			wantChapterID, artifactID, control.ChapterModeFixedInterval, sql.NullInt32{Int32: interval, Valid: true},
			startMs, endMs, false,
			"finalized", sql.NullString{String: "playback-art-hash", Valid: true}, sql.NullString{String: "pb-123", Valid: true}, int32(0),
			sql.NullTime{}, sql.NullTime{},
			sql.NullString{}, sql.NullTime{},
			int32(7), false,
			sql.NullInt64{Int64: 10, Valid: true}, sql.NullInt64{Int64: 3590000, Valid: true},
			time.Unix(0, 0),
		))

	resp, err := srv.RetrieveDVRChapter(context.Background(), &foghorncontrolpb.RetrieveDVRChapterRequest{
		DvrArtifactId:   artifactID,
		Mode:            control.ChapterModeFixedInterval,
		IntervalSeconds: interval,
		StartMs:         startMs,
		EndMs:           endMs,
		// TenantId empty: internal-caller bypass, no assertChapterTenant query.
	})
	if err != nil {
		t.Fatalf("RetrieveDVRChapter: %v", err)
	}
	if resp.ChapterId != wantChapterID {
		t.Fatalf("ChapterId = %q, want canonical %q", resp.ChapterId, wantChapterID)
	}
	if resp.State != "finalized" || resp.SegmentCount != 7 {
		t.Fatalf("metadata mismatch: state=%q segments=%d", resp.State, resp.SegmentCount)
	}
	if resp.PlaybackId != "pb-123" || resp.PlaybackArtifactHash != "playback-art-hash" {
		t.Fatalf("nullable playback fields not mapped: pb=%q hash=%q", resp.PlaybackId, resp.PlaybackArtifactHash)
	}
	if resp.ActualMediaStartMs != 10 || resp.ActualMediaEndMs != 3590000 {
		t.Fatalf("actual media span not mapped: [%d,%d]", resp.ActualMediaStartMs, resp.ActualMediaEndMs)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestRetrieveDVRChapter_NotFound pins the not-found arm of the chapter read:
// when GetChapter finds no row, the RPC surfaces codes.NotFound (not Internal),
// so the caller can distinguish "chapter not yet materialized" from a failure.
func TestRetrieveDVRChapter_NotFound(t *testing.T) {
	srv, mock := withControlDBRpcMore(t)

	const interval = int32(3600)
	wantChapterID := control.BuildChapterID("dvr-x", control.ChapterModeFixedInterval, interval, 0, 3600000)

	mock.ExpectQuery(`FROM foghorn\.dvr_chapters`).
		WithArgs(wantChapterID).
		WillReturnError(sql.ErrNoRows)

	_, err := srv.RetrieveDVRChapter(context.Background(), &foghorncontrolpb.RetrieveDVRChapterRequest{
		DvrArtifactId:   "dvr-x",
		Mode:            control.ChapterModeFixedInterval,
		IntervalSeconds: interval,
		StartMs:         0,
		EndMs:           3600000,
	})
	if got := status.Code(err); got != codes.NotFound {
		t.Fatalf("missing chapter: expected NotFound, got %s", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestListDVRChapters_VirtualWindowPagination drives ListDVRChapters past the
// validation/tenant arms into the virtual-chapter enumerator. Invariant: with a
// window_sized policy spanning exactly two intervals and an explicit forward
// range, the RPC computes both virtual chapters (anchored at started_at),
// overlays any materialized rows, and returns them in ascending order with
// canonical IDs. Empty tenant_id is the internal bypass (no tenant query).
//
// Query order on this path: ReadDVRChapterPolicy, DVRArtifactStillRecording,
// then getChaptersByID(ANY) for the overlay.
func TestListDVRChapters_VirtualWindowPagination(t *testing.T) {
	srv, mock := withControlDBRpcMore(t)

	const (
		artifactID   = "dvr-window-1"
		startedAtMs  = int64(1_000_000_000_000)
		intervalSecs = int32(3600)
		intervalMs   = int64(3600) * 1000
	)
	endedAtMs := startedAtMs + 2*intervalMs // exactly two windows

	// ReadDVRChapterPolicy: mode/interval/started/ended/window. window_sized with a
	// non-zero window yields EffectiveIntervalSeconds == window, so the policy is
	// "valid" (mode set, started>0, effective interval>0).
	mock.ExpectQuery(`FROM foghorn\.artifacts`).
		WithArgs(artifactID).
		WillReturnRows(sqlmock.NewRows([]string{
			"dvr_chapter_mode", "dvr_chapter_interval", "started_at_ms", "ended_at_ms", "dvr_window_seconds",
		}).AddRow(
			control.ChapterModeWindowSized, int32(0), startedAtMs, endedAtMs, intervalSecs,
		))

	// DVRArtifactStillRecording: report a finalized DVR so IsCurrent stays false
	// (deterministic, independent of wall clock).
	mock.ExpectQuery(`SELECT status FROM foghorn\.artifacts`).
		WithArgs(artifactID).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("completed"))

	// overlayMaterializedChapters -> getChaptersByID(ANY): no materialized rows,
	// so the virtual chapters pass through unchanged.
	mock.ExpectQuery(`WHERE chapter_id = ANY\(\$1\)`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows(chapterRowColsRpcMore()))

	resp, err := srv.ListDVRChapters(context.Background(), &foghorncontrolpb.ListDVRChaptersRequest{
		DvrArtifactId: artifactID,
		RangeStartMs:  startedAtMs,
		RangeEndMs:    endedAtMs,
		PageSize:      200,
		// TenantId empty: internal bypass, no assertChapterTenant query.
	})
	if err != nil {
		t.Fatalf("ListDVRChapters: %v", err)
	}
	if len(resp.Chapters) != 2 {
		t.Fatalf("expected 2 virtual chapters across two windows, got %d", len(resp.Chapters))
	}
	// Ascending order, anchored at started_at, each one interval wide.
	first, second := resp.Chapters[0], resp.Chapters[1]
	if first.StartMs != startedAtMs || first.EndMs != startedAtMs+intervalMs {
		t.Fatalf("first chapter bounds = [%d,%d], want [%d,%d]", first.StartMs, first.EndMs, startedAtMs, startedAtMs+intervalMs)
	}
	if second.StartMs != startedAtMs+intervalMs || second.EndMs != endedAtMs {
		t.Fatalf("second chapter bounds = [%d,%d], want [%d,%d]", second.StartMs, second.EndMs, startedAtMs+intervalMs, endedAtMs)
	}
	wantFirstID := control.BuildChapterID(artifactID, control.ChapterModeWindowSized, intervalSecs, startedAtMs, startedAtMs+intervalMs)
	if first.ChapterId != wantFirstID {
		t.Fatalf("first ChapterId = %q, want canonical %q", first.ChapterId, wantFirstID)
	}
	if first.IsCurrent || second.IsCurrent {
		t.Fatal("a finalized DVR must not mark any chapter is_current")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestListDVRChapters_NoPolicyReturnsEmpty pins the empty-overlay terminal: when
// the artifact has no usable chapter policy, the enumerator short-circuits to an
// empty list (no error, no enumeration), and the RPC returns zero chapters. This
// is the internal-bypass tenant path reaching ListVirtualChaptersForArtifact.
func TestListDVRChapters_NoPolicyReturnsEmpty(t *testing.T) {
	srv, mock := withControlDBRpcMore(t)

	const artifactID = "dvr-nopolicy"
	// Policy row with empty mode -> ReadDVRChapterPolicy reports !ok ->
	// ListVirtualChaptersForArtifact returns (nil, "", nil) before any further query.
	mock.ExpectQuery(`FROM foghorn\.artifacts`).
		WithArgs(artifactID).
		WillReturnRows(sqlmock.NewRows([]string{
			"dvr_chapter_mode", "dvr_chapter_interval", "started_at_ms", "ended_at_ms", "dvr_window_seconds",
		}).AddRow("", int32(0), int64(0), int64(0), int32(0)))

	resp, err := srv.ListDVRChapters(context.Background(), &foghorncontrolpb.ListDVRChaptersRequest{
		DvrArtifactId: artifactID,
		RangeStartMs:  1,
		RangeEndMs:    2,
	})
	if err != nil {
		t.Fatalf("ListDVRChapters: %v", err)
	}
	if len(resp.Chapters) != 0 {
		t.Fatalf("expected 0 chapters for policy-less artifact, got %d", len(resp.Chapters))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
