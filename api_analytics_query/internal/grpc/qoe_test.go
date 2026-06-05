package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func newQoeTestServer(t *testing.T) (*PeriscopeServer, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &PeriscopeServer{
		clickhouse: db,
		logger:     logging.NewLoggerWithService("periscope-qoe-test"),
	}, mock
}

func testTimeRange() *commonpb.TimeRange {
	return &commonpb.TimeRange{
		Start: timestamppb.New(time.Now().Add(-time.Hour)),
		End:   timestamppb.New(time.Now()),
	}
}

// GetVodRetention must derive the audience-retention curve as a suffix sum of the
// per-session furthest-bucket-reached histogram (monotonic non-increasing), NOT as
// "sessions that watched this bucket". Watch density is a separate, independent series.
func TestGetVodRetention_ReachIsSuffixSumNotPerBucketViewers(t *testing.T) {
	server, mock := newQoeTestServer(t)

	// Q1: reach histogram + geometry from the session deltas (width=2s, dur=20s).
	// 1 session reached bucket 2, 2 reached 5, 1 reached 9.
	mock.ExpectQuery("max_bucket_reached").
		WillReturnRows(sqlmock.NewRows([]string{"reach", "bw", "dur", "sessions"}).
			AddRow(int64(2), int64(2), int64(20), int64(1)).
			AddRow(int64(5), int64(2), int64(20), int64(2)).
			AddRow(int64(9), int64(2), int64(20), int64(1)))

	// Q2: density — bucket 0 watched 10s, bucket 5 watched 6s (5 is also a replay hot spot).
	mock.ExpectQuery("seconds_watched").
		WillReturnRows(sqlmock.NewRows([]string{"bucket_index", "secs"}).
			AddRow(int64(0), 10.0).
			AddRow(int64(5), 6.0))

	resp, err := server.GetVodRetention(context.Background(), &periscopepb.GetVodRetentionRequest{
		TenantId:     "tenant-1",
		ArtifactHash: "abc",
		TimeRange:    testTimeRange(),
	})
	if err != nil {
		t.Fatalf("GetVodRetention: %v", err)
	}
	r := resp.GetRetention()
	if r.GetTotalSessions() != 4 {
		t.Fatalf("total_sessions = %d, want 4", r.GetTotalSessions())
	}
	if r.GetBucketWidthS() != 2 || r.GetAssetDurationS() != 20 {
		t.Fatalf("geometry = %d/%d, want 2/20", r.GetBucketWidthS(), r.GetAssetDurationS())
	}
	pts := r.GetPoints()
	if len(pts) != 10 { // 0..maxReach(9)
		t.Fatalf("len(points) = %d, want 10", len(pts))
	}

	reachedBy := map[int64]int64{}
	secsBy := map[int64]float64{}
	for _, p := range pts {
		reachedBy[p.GetBucketIndex()] = p.GetReached()
		secsBy[p.GetBucketIndex()] = p.GetSecondsWatched()
	}
	// reached = suffix sum: 0→4, 2→4, 3→3, 5→3, 6→1, 9→1.
	for bucket, want := range map[int64]int64{0: 4, 2: 4, 3: 3, 5: 3, 6: 1, 9: 1} {
		if reachedBy[bucket] != want {
			t.Errorf("reached[%d] = %d, want %d", bucket, reachedBy[bucket], want)
		}
	}
	// Monotonic non-increasing.
	for i := 1; i < len(pts); i++ {
		if pts[i].GetReached() > pts[i-1].GetReached() {
			t.Fatalf("reached not monotonic at bucket %d: %d > %d", i, pts[i].GetReached(), pts[i-1].GetReached())
		}
	}
	// Density is independent of reach: bucket 5 has watch density despite reach dropping.
	if secsBy[0] != 10.0 || secsBy[5] != 6.0 {
		t.Errorf("density wrong: bucket0=%v bucket5=%v", secsBy[0], secsBy[5])
	}
	if mock.ExpectationsWereMet() != nil {
		t.Errorf("unmet mock expectations: %v", mock.ExpectationsWereMet())
	}
}

// A session whose reach exceeds the curve cap must fold into the cap bucket — it
// stays in total_sessions, so dropping it would undercount retention everywhere.
func TestGetVodRetention_OverCapReachFoldsIntoCapBucket(t *testing.T) {
	server, mock := newQoeTestServer(t)
	mock.ExpectQuery("max_bucket_reached").
		WillReturnRows(sqlmock.NewRows([]string{"reach", "bw", "dur", "sessions"}).
			AddRow(int64(10), int64(2), int64(20000), int64(2)).
			AddRow(int64(6000), int64(2), int64(20000), int64(1))) // 6000 > 5000 cap
	mock.ExpectQuery("seconds_watched").
		WillReturnRows(sqlmock.NewRows([]string{"bucket_index", "secs"})) // no density rows

	resp, err := server.GetVodRetention(context.Background(), &periscopepb.GetVodRetentionRequest{
		TenantId: "tenant-1", ArtifactHash: "abc", TimeRange: testTimeRange(),
	})
	if err != nil {
		t.Fatalf("GetVodRetention: %v", err)
	}
	r := resp.GetRetention()
	pts := r.GetPoints()
	if r.GetTotalSessions() != 3 {
		t.Fatalf("total_sessions = %d, want 3", r.GetTotalSessions())
	}
	if int64(len(pts)) != 5001 { // 0..5000 cap
		t.Fatalf("len(points) = %d, want 5001 (capped)", len(pts))
	}
	if pts[0].GetReached() != 3 {
		t.Errorf("reached[0] = %d, want 3 (everyone)", pts[0].GetReached())
	}
	if pts[10].GetReached() != 3 {
		t.Errorf("reached[10] = %d, want 3", pts[10].GetReached())
	}
	// The over-cap session folded into the cap bucket — not dropped.
	if pts[5000].GetReached() != 1 {
		t.Errorf("reached[5000] = %d, want 1 (folded over-cap reach)", pts[5000].GetReached())
	}
}

func TestGetVodRetention_NoSessionsReturnsEmpty(t *testing.T) {
	server, mock := newQoeTestServer(t)
	// Only VOD-timeline sessions count (HAVING bw > 0); none here → empty, and the
	// density query never runs.
	mock.ExpectQuery("max_bucket_reached").
		WillReturnRows(sqlmock.NewRows([]string{"reach", "bw", "dur", "sessions"})) // empty

	resp, err := server.GetVodRetention(context.Background(), &periscopepb.GetVodRetentionRequest{
		TenantId: "tenant-1", ArtifactHash: "abc", TimeRange: testTimeRange(),
	})
	if err != nil {
		t.Fatalf("GetVodRetention: %v", err)
	}
	if len(resp.GetRetention().GetPoints()) != 0 || resp.GetRetention().GetTotalSessions() != 0 {
		t.Fatalf("expected empty retention, got %+v", resp.GetRetention())
	}
}

// GetSessionQoeSummary maps the aggregate row to the response in column order — a
// guard against the columns silently drifting against the SELECT list.
func TestGetSessionQoeSummary_MapsColumnsInOrder(t *testing.T) {
	server, mock := newQoeTestServer(t)

	mock.ExpectQuery("client_qoe_session_deltas").
		WillReturnRows(sqlmock.NewRows([]string{
			"session_count", "played_hours", "rebuffering_ratio", "rebuffers_per_hour",
			"avg_rebuffer_ms", "frame_drop_ratio", "playback_failure_rate", "ebvs_rate",
			"avg_bitrate_bps", "abr_switches_per_hour", "avg_live_edge",
		}).AddRow(int64(10), 2.5, 0.01, 1.2, 500.0, 0.002, 0.05, 0.03, 4000000.0, 3.0, 2500.0))

	resp, err := server.GetSessionQoeSummary(context.Background(), &periscopepb.GetSessionQoeSummaryRequest{
		TenantId: "tenant-1", TimeRange: testTimeRange(),
	})
	if err != nil {
		t.Fatalf("GetSessionQoeSummary: %v", err)
	}
	s := resp.GetSummary()
	if s.GetSessionCount() != 10 || s.GetRebufferingRatio() != 0.01 || s.GetAvgRebufferMs() != 500 ||
		s.GetEbvsRate() != 0.03 || s.GetAvgBitrateBps() != 4000000 || s.GetAvgLiveEdgeLatencyMs() != 2500 {
		t.Fatalf("session qoe summary mismapped: %+v", s)
	}
	if mock.ExpectationsWereMet() != nil {
		t.Errorf("unmet mock expectations: %v", mock.ExpectationsWereMet())
	}
}
