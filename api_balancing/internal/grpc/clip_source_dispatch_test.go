package grpc

import (
	"context"
	"strings"
	"testing"
	"time"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
)

// pickClipSource picks LIVE / DVR_ROLLING / CHAPTER based on where the
// clip's range falls. Chapter coverage takes precedence over the
// rolling-DVR fallback so historical chapters remain clippable after
// the source DVR has stopped.

const testTenantID = "00000000-0000-0000-0000-000000000001"

func newDispatchServer(t *testing.T) (*FoghornGRPCServer, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &FoghornGRPCServer{db: db, logger: logrus.New()}, mock
}

func TestPickClipSource_InShmWindowIsLive(t *testing.T) {
	srv, _ := newDispatchServer(t)
	nowMs := time.Now().UnixMilli()
	dec, err := srv.pickClipSource(context.Background(), testTenantID, "stream-1", nowMs-30_000, nowMs-5_000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.kind != pb.ClipPullRequest_SOURCE_KIND_LIVE {
		t.Fatalf("expected LIVE, got %v", dec.kind)
	}
	if dec.streamName != "stream-1" {
		t.Fatalf("expected streamName=stream-1, got %q", dec.streamName)
	}
}

func TestResolveClipAbsoluteRangeMs_ClipNowUsesNegativeStartAsStartOffset(t *testing.T) {
	durationSec := int64(30)
	startUnix := -durationSec
	req := &pb.CreateClipRequest{
		Mode:        pb.ClipMode_CLIP_MODE_CLIP_NOW,
		StartUnix:   &startUnix,
		DurationSec: &durationSec,
	}
	before := time.Now().UnixMilli()
	startMs, endMs, err := resolveClipAbsoluteRangeMs(req, "stream-1")
	after := time.Now().UnixMilli()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := endMs - startMs; got != durationSec*1000 {
		t.Fatalf("expected duration %dms, got %dms", durationSec*1000, got)
	}
	if startMs < before-durationSec*1000 || startMs > after-durationSec*1000 {
		t.Fatalf("expected start around now-duration, got start=%d before=%d after=%d", startMs, before, after)
	}
}

func TestPickClipSource_CrossesShmBoundary_Rejected(t *testing.T) {
	srv, _ := newDispatchServer(t)
	nowMs := time.Now().UnixMilli()
	_, err := srv.pickClipSource(context.Background(), testTenantID, "stream-1",
		nowMs-240_000, // 4 min ago (past shm)
		nowMs-60_000)  // 1 min ago (in shm)
	if err == nil {
		t.Fatal("expected rejection for cross-boundary range")
	}
	if !strings.Contains(err.Error(), "live/dvr boundary") {
		t.Fatalf("expected boundary error, got %v", err)
	}
}

func TestPickClipSource_DVRRollingWhenPastShmAndActive(t *testing.T) {
	srv, mock := newDispatchServer(t)
	nowMs := time.Now().UnixMilli()
	startMs := nowMs - 600_000
	endMs := nowMs - 300_000

	// No covering chapter.
	mock.ExpectQuery(`SELECT COALESCE\(c.playback_artifact_hash, ''\)`).
		WithArgs("stream-1", startMs, endMs, testTenantID).
		WillReturnRows(sqlmock.NewRows([]string{"playback_artifact_hash"}))
	// Recording DVR exists.
	mock.ExpectQuery(`SELECT artifact_hash`).
		WithArgs("stream-1", testTenantID).
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "internal_name", "started", "status"}).
			AddRow("dvr-h", "dvr-internal", nowMs-3_600_000, "recording"))
	mock.ExpectQuery(`SELECT node_id`).
		WithArgs("dvr-h").
		WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow("recording-node-1"))
	// Rolling manifest scope: window length, then continuity sum over the segment ledger.
	mock.ExpectQuery(`SELECT dvr_window_seconds FROM foghorn.artifacts`).
		WithArgs("dvr-h").
		WillReturnRows(sqlmock.NewRows([]string{"dvr_window_seconds"}).AddRow(3600))
	mock.ExpectQuery(`SELECT GREATEST\(media_start_ms, \$2\)`).
		WithArgs("dvr-h", startMs, endMs).
		WillReturnRows(sqlmock.NewRows([]string{"seg_start", "seg_end"}).
			AddRow(startMs, endMs))

	dec, err := srv.pickClipSource(context.Background(), testTenantID, "stream-1", startMs, endMs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.kind != pb.ClipPullRequest_SOURCE_KIND_DVR_ROLLING {
		t.Fatalf("expected DVR_ROLLING, got %v", dec.kind)
	}
	if dec.streamName != "dvr+dvr-internal" {
		t.Fatalf("expected dvr+ stream, got %q", dec.streamName)
	}
	if dec.sourceNodeID != "recording-node-1" {
		t.Fatalf("expected recording node, got %q", dec.sourceNodeID)
	}
}

func TestPickClipSource_ChapterArtifactCovers(t *testing.T) {
	srv, mock := newDispatchServer(t)
	nowMs := time.Now().UnixMilli()
	startMs := nowMs - 7_200_000
	endMs := nowMs - 3_600_000

	mock.ExpectQuery(`SELECT COALESCE\(c.playback_artifact_hash, ''\)`).
		WithArgs("stream-1", startMs, endMs, testTenantID).
		WillReturnRows(sqlmock.NewRows([]string{"playback_artifact_hash"}).AddRow("chap-art-hash"))

	dec, err := srv.pickClipSource(context.Background(), testTenantID, "stream-1", startMs, endMs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.kind != pb.ClipPullRequest_SOURCE_KIND_CHAPTER {
		t.Fatalf("expected CHAPTER, got %v", dec.kind)
	}
	if dec.chapterArtifactHash != "chap-art-hash" {
		t.Fatalf("expected chapter hash, got %q", dec.chapterArtifactHash)
	}
	if dec.streamName != "vod+chap-art-hash" {
		t.Fatalf("expected vod+ stream, got %q", dec.streamName)
	}
}

func TestPickClipSource_RejectsStoppedDVRWithoutChapter(t *testing.T) {
	srv, mock := newDispatchServer(t)
	nowMs := time.Now().UnixMilli()
	startMs := nowMs - 600_000
	endMs := nowMs - 300_000

	mock.ExpectQuery(`SELECT COALESCE\(c.playback_artifact_hash, ''\)`).
		WithArgs("stream-1", startMs, endMs, testTenantID).
		WillReturnRows(sqlmock.NewRows([]string{"playback_artifact_hash"}))
	mock.ExpectQuery(`SELECT artifact_hash`).
		WithArgs("stream-1", testTenantID).
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "internal_name", "started", "status"}).
			AddRow("dvr-h", "dvr-internal", nowMs-3_600_000, "completed"))

	_, err := srv.pickClipSource(context.Background(), testTenantID, "stream-1", startMs, endMs)
	if err == nil || !strings.Contains(err.Error(), "no longer active") {
		t.Fatalf("expected stopped-DVR rejection, got %v", err)
	}
}

func TestPickClipSource_RejectsRangeBeforeDVRStart(t *testing.T) {
	srv, mock := newDispatchServer(t)
	nowMs := time.Now().UnixMilli()
	startMs := nowMs - 10_800_000
	endMs := nowMs - 7_200_000

	mock.ExpectQuery(`SELECT COALESCE\(c.playback_artifact_hash, ''\)`).
		WithArgs("stream-1", startMs, endMs, testTenantID).
		WillReturnRows(sqlmock.NewRows([]string{"playback_artifact_hash"}))
	mock.ExpectQuery(`SELECT artifact_hash`).
		WithArgs("stream-1", testTenantID).
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "internal_name", "started", "status"}).
			AddRow("dvr-h", "dvr-internal", nowMs-5_400_000, "recording"))
	mock.ExpectQuery(`SELECT node_id`).
		WithArgs("dvr-h").
		WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow("recording-node-1"))

	_, err := srv.pickClipSource(context.Background(), testTenantID, "stream-1", startMs, endMs)
	if err == nil || !strings.Contains(err.Error(), "before DVR recording") {
		t.Fatalf("expected pre-DVR rejection, got %v", err)
	}
}
