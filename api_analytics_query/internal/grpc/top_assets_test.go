package grpc

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
)

// ListTopAssets queries the client_qoe_session_deltas source columns directly
// and maps the ranked rows without gateway-side aggregation.
func TestListTopAssets_QueryContractAndMapping(t *testing.T) {
	server, mock := newQoeTestServer(t)

	mock.ExpectQuery(`argMax\(content_type, timestamp\).*sum\(played_ms\).*FROM client_qoe_session_deltas.*ORDER BY total_sessions DESC`).
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "content_type", "total_sessions", "watch_hours", "duration_s"}).
			AddRow("hash-dvr", "dvr", int64(42), 1.5, int32(120)).
			AddRow("hash-vod", "vod", int64(7), 0.25, int32(60)))

	resp, err := server.ListTopAssets(context.Background(), &periscopepb.ListTopAssetsRequest{
		TenantId: "tenant-1", TimeRange: testTimeRange(), Limit: 10,
	})
	if err != nil {
		t.Fatalf("ListTopAssets: %v", err)
	}
	if len(resp.GetAssets()) != 2 {
		t.Fatalf("assets = %d, want 2", len(resp.GetAssets()))
	}
	a := resp.GetAssets()[0]
	if a.GetArtifactHash() != "hash-dvr" || a.GetContentType() != "dvr" ||
		a.GetTotalSessions() != 42 || a.GetWatchHours() != 1.5 || a.GetDurationS() != 120 {
		t.Fatalf("top asset mismapped: %+v", a)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A missing tenant is rejected.
func TestListTopAssets_RequiresTenant(t *testing.T) {
	server, _ := newQoeTestServer(t)
	if _, err := server.ListTopAssets(context.Background(), &periscopepb.ListTopAssetsRequest{
		TimeRange: testTimeRange(), Limit: 10,
	}); err == nil {
		t.Fatal("expected error for missing tenant")
	}
}
