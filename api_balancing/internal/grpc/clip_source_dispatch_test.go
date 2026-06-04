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

// Clip source selection is coverage-aware and best-effort: a source that
// fully covers the requested range wins in priority order LIVE > DVR > VOD;
// otherwise the source with the largest contiguous overlap wins (ties
// broken LIVE > DVR > VOD); zero overlap anywhere is rejected.

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

// liveCandidate / dvrCandidate / chapterCandidate build covered-interval
// candidates for the pure chooser tests.
func liveCandidate(start, end int64) clipCoverage {
	c := clipCoverage{kind: pb.ClipPullRequest_SOURCE_KIND_LIVE, covStart: start, covEnd: end}
	if end > start {
		c.streamName = "stream-1"
	}
	return c
}

func dvrCandidate(start, end int64) clipCoverage {
	c := clipCoverage{kind: pb.ClipPullRequest_SOURCE_KIND_DVR_ROLLING, covStart: start, covEnd: end, dvrHash: "dvr-h", sourceNodeID: "node-1"}
	if end > start {
		c.streamName = "dvr+dvr-internal"
	}
	return c
}

func chapterCandidate(start, end int64) clipCoverage {
	c := clipCoverage{kind: pb.ClipPullRequest_SOURCE_KIND_CHAPTER, covStart: start, covEnd: end, chapterArtifactHash: "chap-h"}
	if end > start {
		c.streamName = "vod+chap-h"
	}
	return c
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

func TestLiveCoverageRange(t *testing.T) {
	const now = int64(1_000_000_000_000)
	cases := []struct {
		name               string
		startedAtMs        int64
		live               bool
		reqStart, reqEnd   int64
		wantStart, wantEnd int64
	}{
		{
			name:     "offline contributes nothing",
			live:     false,
			reqStart: now - 30_000, reqEnd: now,
			wantStart: 0, wantEnd: 0,
		},
		{
			name:        "full coverage inside shm window",
			startedAtMs: now - 600_000, // 10m of buffer
			live:        true,
			reqStart:    now - 30_000, reqEnd: now,
			wantStart: now - 30_000, wantEnd: now,
		},
		{
			name:        "new stream with short buffer is partial",
			startedAtMs: now - 25_000, // only 25s of buffer
			live:        true,
			reqStart:    now - 60_000, reqEnd: now, // asked for 60s
			wantStart: now - 25_000, wantEnd: now, // get 25s
		},
		{
			name:        "range entirely past the shm window",
			startedAtMs: now - 3_600_000,
			live:        true,
			reqStart:    now - 300_000, reqEnd: now - 200_000, // 5m..3m20s ago, beyond 120s window
			wantStart: 0, wantEnd: 0,
		},
		{
			name:        "future end clamps to now",
			startedAtMs: now - 600_000,
			live:        true,
			reqStart:    now - 10_000, reqEnd: now + 5_000,
			wantStart: now - 10_000, wantEnd: now,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotStart, gotEnd := liveCoverageRange(tc.startedAtMs, tc.live, tc.reqStart, tc.reqEnd, now)
			if gotStart != tc.wantStart || gotEnd != tc.wantEnd {
				t.Fatalf("got [%d,%d), want [%d,%d)", gotStart, gotEnd, tc.wantStart, tc.wantEnd)
			}
		})
	}
}

func TestChooseClipSource(t *testing.T) {
	// Whole-second-scale request [0, 60s); selection ranks on aligned seconds.
	const s, e = int64(0), int64(60_000)
	cases := []struct {
		name        string
		live        clipCoverage
		dvr         clipCoverage
		chap        clipCoverage
		wantKind    pb.ClipPullRequest_SourceKind
		wantPartial bool
		wantStart   int64
		wantEnd     int64
		wantErr     bool
	}{
		{
			name:     "full live wins",
			live:     liveCandidate(s, e),
			dvr:      dvrCandidate(s, e),
			chap:     chapterCandidate(s, e),
			wantKind: pb.ClipPullRequest_SOURCE_KIND_LIVE, wantStart: s, wantEnd: e,
		},
		{
			name:     "full dvr beats full chapter, live partial",
			live:     liveCandidate(35_000, e), // partial
			dvr:      dvrCandidate(s, e),       // full
			chap:     chapterCandidate(s, e),   // full
			wantKind: pb.ClipPullRequest_SOURCE_KIND_DVR_ROLLING, wantStart: s, wantEnd: e,
		},
		{
			name:     "full chapter when no live/dvr",
			live:     liveCandidate(0, 0),
			dvr:      dvrCandidate(0, 0),
			chap:     chapterCandidate(s, e),
			wantKind: pb.ClipPullRequest_SOURCE_KIND_CHAPTER, wantStart: s, wantEnd: e,
		},
		{
			name:     "live partial when no dvr/chapter",
			live:     liveCandidate(35_000, e), // 25s
			dvr:      dvrCandidate(0, 0),
			chap:     chapterCandidate(0, 0),
			wantKind: pb.ClipPullRequest_SOURCE_KIND_LIVE, wantPartial: true, wantStart: 35_000, wantEnd: e,
		},
		{
			name:     "dvr partial beats tiny live partial",
			live:     liveCandidate(58_000, e), // 2s
			dvr:      dvrCandidate(s, 48_000),  // 48s
			chap:     chapterCandidate(0, 0),
			wantKind: pb.ClipPullRequest_SOURCE_KIND_DVR_ROLLING, wantPartial: true, wantStart: s, wantEnd: 48_000,
		},
		{
			name:     "equal partial coverage breaks toward live",
			live:     liveCandidate(s, 30_000), // 30s
			dvr:      dvrCandidate(30_000, e),  // 30s
			chap:     chapterCandidate(0, 0),
			wantKind: pb.ClipPullRequest_SOURCE_KIND_LIVE, wantPartial: true, wantStart: s, wantEnd: 30_000,
		},
		{
			name:     "chapter partial when no live/dvr",
			live:     liveCandidate(0, 0),
			dvr:      dvrCandidate(0, 0),
			chap:     chapterCandidate(12_000, 48_000),
			wantKind: pb.ClipPullRequest_SOURCE_KIND_CHAPTER, wantPartial: true, wantStart: 12_000, wantEnd: 48_000,
		},
		{
			// A larger raw overlap that collapses below 1s after hard-boundary
			// alignment must lose to a smaller second-aligned source.
			name:     "collapsing larger source loses to viable smaller",
			live:     liveCandidate(0, 0),
			dvr:      dvrCandidate(58_100, 59_950),     // 1.85s raw, hard -> ceil59,floor59 collapses
			chap:     chapterCandidate(10_000, 12_000), // 2s, viable
			wantKind: pb.ClipPullRequest_SOURCE_KIND_CHAPTER, wantPartial: true, wantStart: 10_000, wantEnd: 12_000,
		},
		{
			name:    "zero overlap fails",
			live:    liveCandidate(0, 0),
			dvr:     dvrCandidate(0, 0),
			chap:    chapterCandidate(0, 0),
			wantErr: true,
		},
		{
			name:    "sub-second-only overlap fails",
			live:    liveCandidate(59_300, 59_900), // 0.6s, collapses
			dvr:     dvrCandidate(0, 0),
			chap:    chapterCandidate(0, 0),
			wantErr: true,
		},
		{
			name:     "live zeroed for fallback falls to dvr",
			live:     clipCoverage{kind: pb.ClipPullRequest_SOURCE_KIND_LIVE}, // dropped
			dvr:      dvrCandidate(s, 48_000),
			chap:     chapterCandidate(0, 0),
			wantKind: pb.ClipPullRequest_SOURCE_KIND_DVR_ROLLING, wantPartial: true, wantStart: s, wantEnd: 48_000,
		},
		{
			name:     "dvr zeroed for fallback (unroutable node) falls to chapter",
			live:     liveCandidate(0, 0),
			dvr:      clipCoverage{kind: pb.ClipPullRequest_SOURCE_KIND_DVR_ROLLING}, // dropped
			chap:     chapterCandidate(s, e),
			wantKind: pb.ClipPullRequest_SOURCE_KIND_CHAPTER, wantStart: s, wantEnd: e,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dec, err := chooseClipSource(s, e, tc.live, tc.dvr, tc.chap)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got decision %+v", dec)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if dec.kind != tc.wantKind {
				t.Fatalf("kind: got %v want %v", dec.kind, tc.wantKind)
			}
			if dec.partial != tc.wantPartial {
				t.Fatalf("partial: got %v want %v", dec.partial, tc.wantPartial)
			}
			if dec.effectiveStartMs != tc.wantStart || dec.effectiveEndMs != tc.wantEnd {
				t.Fatalf("range: got [%d,%d) want [%d,%d)", dec.effectiveStartMs, dec.effectiveEndMs, tc.wantStart, tc.wantEnd)
			}
		})
	}
}

// expectNoDVR mocks findRecordingDVR returning no recording.
func expectNoDVR(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`SELECT artifact_hash`).
		WithArgs("stream-1", testTenantID).
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "internal_name", "started", "status"}))
}

// expectActiveDVR mocks findRecordingDVR returning an active recording on
// recording-node-1, then the rolling-window length and segment ledger walk.
func expectActiveDVR(mock sqlmock.Sqlmock, startedAtMs, windowSec, lowerBound, endMs int64, segs [][2]int64) {
	mock.ExpectQuery(`SELECT artifact_hash`).
		WithArgs("stream-1", testTenantID).
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "internal_name", "started", "status"}).
			AddRow("dvr-h", "dvr-internal", startedAtMs, "recording"))
	mock.ExpectQuery(`SELECT node_id`).
		WithArgs("dvr-h").
		WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow("recording-node-1"))
	windowRows := sqlmock.NewRows([]string{"dvr_window_seconds"})
	if windowSec > 0 {
		windowRows.AddRow(windowSec)
	} else {
		windowRows.AddRow(nil)
	}
	mock.ExpectQuery(`SELECT dvr_window_seconds FROM foghorn.artifacts`).
		WithArgs("dvr-h").
		WillReturnRows(windowRows)
	segRows := sqlmock.NewRows([]string{"seg_start", "seg_end"})
	for _, sgmt := range segs {
		segRows.AddRow(sgmt[0], sgmt[1])
	}
	mock.ExpectQuery(`SELECT GREATEST\(media_start_ms, \$2\)`).
		WithArgs("dvr-h", lowerBound, endMs).
		WillReturnRows(segRows)
}

// expectChapter mocks chapterArtifactBestOverlap. Pass hash=="" for no match.
func expectChapter(mock sqlmock.Sqlmock, reqStart, reqEnd int64, hash string, ovStart, ovEnd int64) {
	rows := sqlmock.NewRows([]string{"playback_artifact_hash", "ov_start", "ov_end"})
	if hash != "" {
		rows.AddRow(hash, ovStart, ovEnd)
	}
	mock.ExpectQuery(`SELECT COALESCE\(c.playback_artifact_hash, ''\)`).
		WithArgs("stream-1", reqStart, reqEnd, testTenantID).
		WillReturnRows(rows)
}

func TestPickClipSource_DVRFullCoverage(t *testing.T) {
	srv, mock := newDispatchServer(t)
	nowMs := time.Now().Unix() * 1000 // whole-second base so alignment is identity
	startMs := nowMs - 600_000
	endMs := nowMs - 300_000
	// Large window + old DVR start so lowerBound == startMs (deterministic).
	expectActiveDVR(mock, nowMs-3_600_000, 0 /*null window*/, startMs, endMs, [][2]int64{{startMs, endMs}})
	expectChapter(mock, startMs, endMs, "", 0, 0)

	dec, err := srv.pickClipSource(context.Background(), testTenantID, "stream-1", startMs, endMs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.kind != pb.ClipPullRequest_SOURCE_KIND_DVR_ROLLING {
		t.Fatalf("expected DVR_ROLLING, got %v", dec.kind)
	}
	if dec.partial {
		t.Fatalf("expected full coverage, got partial")
	}
	if dec.streamName != "dvr+dvr-internal" || dec.sourceNodeID != "recording-node-1" {
		t.Fatalf("unexpected dvr routing: %q node=%q", dec.streamName, dec.sourceNodeID)
	}
}

func TestPickClipSource_ChapterFullWhenNoDVR(t *testing.T) {
	srv, mock := newDispatchServer(t)
	nowMs := time.Now().Unix() * 1000 // whole-second base so alignment is identity
	startMs := nowMs - 7_200_000
	endMs := nowMs - 3_600_000
	expectNoDVR(mock)
	expectChapter(mock, startMs, endMs, "chap-art-hash", startMs, endMs)

	dec, err := srv.pickClipSource(context.Background(), testTenantID, "stream-1", startMs, endMs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.kind != pb.ClipPullRequest_SOURCE_KIND_CHAPTER {
		t.Fatalf("expected CHAPTER, got %v", dec.kind)
	}
	if dec.partial {
		t.Fatalf("expected full coverage, got partial")
	}
	if dec.streamName != "vod+chap-art-hash" || dec.chapterArtifactHash != "chap-art-hash" {
		t.Fatalf("unexpected chapter routing: %q", dec.streamName)
	}
}

func TestPickClipSource_DVRFullBeatsChapterFull(t *testing.T) {
	srv, mock := newDispatchServer(t)
	nowMs := time.Now().Unix() * 1000 // whole-second base so alignment is identity
	startMs := nowMs - 600_000
	endMs := nowMs - 300_000
	expectActiveDVR(mock, nowMs-3_600_000, 0, startMs, endMs, [][2]int64{{startMs, endMs}})
	expectChapter(mock, startMs, endMs, "chap-art-hash", startMs, endMs) // chapter also fully covers

	dec, err := srv.pickClipSource(context.Background(), testTenantID, "stream-1", startMs, endMs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.kind != pb.ClipPullRequest_SOURCE_KIND_DVR_ROLLING {
		t.Fatalf("expected DVR to win over chapter, got %v", dec.kind)
	}
}

// Regression: a request that begins before the DVR started must still use
// the DVR for the overlapping tail (the dropped started_at<=start guard).
func TestPickClipSource_DVRPartialTailStartsBeforeDVR(t *testing.T) {
	srv, mock := newDispatchServer(t)
	nowMs := time.Now().Unix() * 1000 // whole-second base so alignment is identity
	startMs := nowMs - 10_800_000     // 3h ago, before the DVR began
	endMs := nowMs - 7_200_000        // 2h ago
	dvrStartedAt := nowMs - 9_000_000 // 2.5h ago
	// Null window -> lowerBound = max(startMs, dvrStartedAt) = dvrStartedAt.
	expectActiveDVR(mock, dvrStartedAt, 0, dvrStartedAt, endMs, [][2]int64{{dvrStartedAt, endMs}})
	expectChapter(mock, startMs, endMs, "", 0, 0)

	dec, err := srv.pickClipSource(context.Background(), testTenantID, "stream-1", startMs, endMs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.kind != pb.ClipPullRequest_SOURCE_KIND_DVR_ROLLING {
		t.Fatalf("expected DVR_ROLLING tail, got %v", dec.kind)
	}
	if !dec.partial {
		t.Fatalf("expected partial coverage")
	}
	if dec.effectiveStartMs != dvrStartedAt || dec.effectiveEndMs != endMs {
		t.Fatalf("effective range got [%d,%d) want [%d,%d)", dec.effectiveStartMs, dec.effectiveEndMs, dvrStartedAt, endMs)
	}
}

func TestPickClipSource_ChapterPartialOverlap(t *testing.T) {
	srv, mock := newDispatchServer(t)
	nowMs := time.Now().Unix() * 1000 // whole-second base so alignment is identity
	startMs := nowMs - 7_200_000
	endMs := nowMs - 3_600_000
	ovStart := startMs + 600_000 // chapter covers only the inner slice
	ovEnd := endMs - 600_000
	expectNoDVR(mock)
	expectChapter(mock, startMs, endMs, "chap-art-hash", ovStart, ovEnd)

	dec, err := srv.pickClipSource(context.Background(), testTenantID, "stream-1", startMs, endMs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.kind != pb.ClipPullRequest_SOURCE_KIND_CHAPTER || !dec.partial {
		t.Fatalf("expected partial CHAPTER, got kind=%v partial=%v", dec.kind, dec.partial)
	}
	if dec.effectiveStartMs != ovStart || dec.effectiveEndMs != ovEnd {
		t.Fatalf("effective range got [%d,%d) want [%d,%d)", dec.effectiveStartMs, dec.effectiveEndMs, ovStart, ovEnd)
	}
}

func TestPickClipSource_ZeroOverlapRejected(t *testing.T) {
	srv, mock := newDispatchServer(t)
	nowMs := time.Now().Unix() * 1000 // whole-second base so alignment is identity
	startMs := nowMs - 7_200_000
	endMs := nowMs - 3_600_000
	expectNoDVR(mock)
	expectChapter(mock, startMs, endMs, "", 0, 0)

	_, err := srv.pickClipSource(context.Background(), testTenantID, "stream-1", startMs, endMs)
	if err == nil || !strings.Contains(err.Error(), "no source with at least one whole second") {
		t.Fatalf("expected zero-overlap rejection, got %v", err)
	}
}

// An active DVR whose recording origin is ambiguous (multiple non-orphaned
// nodes) is not a usable rolling source, so it contributes zero coverage and
// a covering chapter wins instead of the clip being rejected.
func TestPickClipSource_AmbiguousDVRFallsToChapter(t *testing.T) {
	srv, mock := newDispatchServer(t)
	nowMs := time.Now().Unix() * 1000 // whole-second base so alignment is identity
	startMs := nowMs - 600_000
	endMs := nowMs - 300_000
	mock.ExpectQuery(`SELECT artifact_hash`).
		WithArgs("stream-1", testTenantID).
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "internal_name", "started", "status"}).
			AddRow("dvr-h", "dvr-internal", nowMs-3_600_000, "recording"))
	mock.ExpectQuery(`SELECT node_id`).
		WithArgs("dvr-h").
		WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow("node-a").AddRow("node-b")) // ambiguous
	expectChapter(mock, startMs, endMs, "chap-art-hash", startMs, endMs)

	dec, err := srv.pickClipSource(context.Background(), testTenantID, "stream-1", startMs, endMs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.kind != pb.ClipPullRequest_SOURCE_KIND_CHAPTER {
		t.Fatalf("expected CHAPTER fallback, got %v", dec.kind)
	}
}

// An active DVR with no known recording node cannot be pulled, so it
// contributes zero coverage and the request falls through to chapter.
func TestPickClipSource_NodelessDVRFallsToChapter(t *testing.T) {
	srv, mock := newDispatchServer(t)
	nowMs := time.Now().Unix() * 1000 // whole-second base so alignment is identity
	startMs := nowMs - 600_000
	endMs := nowMs - 300_000
	mock.ExpectQuery(`SELECT artifact_hash`).
		WithArgs("stream-1", testTenantID).
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "internal_name", "started", "status"}).
			AddRow("dvr-h", "dvr-internal", nowMs-3_600_000, "recording"))
	mock.ExpectQuery(`SELECT node_id`).
		WithArgs("dvr-h").
		WillReturnRows(sqlmock.NewRows([]string{"node_id"})) // no nodes
	expectChapter(mock, startMs, endMs, "chap-art-hash", startMs, endMs)

	dec, err := srv.pickClipSource(context.Background(), testTenantID, "stream-1", startMs, endMs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.kind != pb.ClipPullRequest_SOURCE_KIND_CHAPTER {
		t.Fatalf("expected CHAPTER fallback, got %v", dec.kind)
	}
}

func TestAlignFulfilledClipSeconds(t *testing.T) {
	cases := []struct {
		name                string
		startHard           bool
		startMs, endMs      int64
		wantStart, wantStop int64
		wantOK              bool
	}{
		// Soft start (request inside the live buffer): floor both, preserving
		// duration for now-anchored ranges.
		{"soft second-aligned range unchanged", false, 5_000, 10_000, 5, 10, true},
		{"soft 30s with shared remainder preserves duration", false, 1_200, 31_200, 1, 31, true},
		{"soft sub-second slice collapses", false, 1_200, 1_800, 1, 1, false},
		// Hard start (clamped to a media boundary: segment/chapter/stream-start):
		// ceil start to stay within proven media, floor stop.
		{"hard start rounds up into covered media", true, 1_200, 5_800, 2, 5, true},
		{"hard chapter overlap at 1200ms reports 2s not 1s", true, 1_200, 9_900, 2, 9, true},
		{"hard second-aligned full range unchanged", true, 3_000, 8_000, 3, 8, true},
		{"hard sub-second slice collapses", true, 1_200, 1_900, 2, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotStart, gotStop, ok := alignFulfilledClipSeconds(tc.startHard, tc.startMs, tc.endMs)
			if ok != tc.wantOK {
				t.Fatalf("ok: got %v want %v", ok, tc.wantOK)
			}
			if ok && (gotStart != tc.wantStart || gotStop != tc.wantStop) {
				t.Fatalf("got [%d,%d)s want [%d,%d)s", gotStart, gotStop, tc.wantStart, tc.wantStop)
			}
		})
	}
}

func TestRollingDVRCoverageRange_LongestContiguousRunWithHole(t *testing.T) {
	srv, mock := newDispatchServer(t)
	const start, end = int64(1000), int64(5000)
	// Null window -> lowerBound = start. Two runs separated by a hole:
	// [1000,2000) and [3000,5000). Longest is the second (2000ms).
	mock.ExpectQuery(`SELECT dvr_window_seconds FROM foghorn.artifacts`).
		WithArgs("dvr-h").
		WillReturnRows(sqlmock.NewRows([]string{"dvr_window_seconds"}).AddRow(nil))
	mock.ExpectQuery(`SELECT GREATEST\(media_start_ms, \$2\)`).
		WithArgs("dvr-h", start, end).
		WillReturnRows(sqlmock.NewRows([]string{"seg_start", "seg_end"}).
			AddRow(1000, 1500).
			AddRow(1500, 2000). // contiguous with previous -> run [1000,2000)
			AddRow(3000, 5000)) // gap at 2000..3000 -> new run [3000,5000)

	cs, ce, err := srv.rollingDVRCoverageRange(context.Background(), "dvr-h", 0, start, end)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cs != 3000 || ce != 5000 {
		t.Fatalf("longest run got [%d,%d) want [3000,5000)", cs, ce)
	}
}
