//go:build schema_verify

package grpc

import (
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestStreamHealth5mUnknownHeightIsNotSD_RealClickHouse inserts health
// samples the way ingest writes them: one block per sample, with NULL height
// when a trigger carried no video dimensions (buffer edges, stream end). Those
// blocks must project as 'Unknown', never as SD, and must not become the
// stream's current tier.
func TestStreamHealth5mUnknownHeightIsNotSD_RealClickHouse(t *testing.T) {
	db := startQueryPackClickHouse(t)
	server := NewPeriscopeServer(db, logging.NewLoggerWithService("stream-health-tier-real-ch-test"))
	ctx := serviceTestContext()
	const streamID = "5eedfeed-11fe-ca57-feed-11feca5700a1"
	bucket := time.Now().UTC().Truncate(5 * time.Minute).Add(-10 * time.Minute)

	insert := `INSERT INTO periscope.stream_health_samples
		(timestamp, tenant_id, stream_id, internal_name, node_id, buffer_state, width, height, fps, codec)
		VALUES (?, ?, ?, 'tier-stream', 'edge-1', ?, ?, ?, ?, ?)`
	known := func(at time.Time) {
		t.Helper()
		w, h, fps, codec := uint16(1920), uint16(1080), float32(30), "H264"
		if _, err := db.ExecContext(ctx, insert, at, queryPackTenantID, streamID, "FULL", &w, &h, &fps, &codec); err != nil {
			t.Fatal(err)
		}
	}
	unknown := func(at time.Time, state string) {
		t.Helper()
		var w, h *uint16
		var fps *float32
		var codec *string
		if _, err := db.ExecContext(ctx, insert, at, queryPackTenantID, streamID, state, w, h, fps, codec); err != nil {
			t.Fatal(err)
		}
	}
	known(bucket.Add(10 * time.Second))
	known(bucket.Add(20 * time.Second))
	unknown(bucket.Add(30*time.Second), "")
	known(bucket.Add(40 * time.Second))
	unknown(bucket.Add(50*time.Second), "EMPTY")
	unknown(bucket.Add(60*time.Second), "EMPTY")

	timeRange := &commonpb.TimeRange{
		Start: timestamppb.New(bucket.Add(-time.Minute)),
		End:   timestamppb.New(bucket.Add(5 * time.Minute)),
	}
	resp, err := server.GetStreamHealth5M(ctx, &periscopepb.GetStreamHealth5MRequest{
		TenantId:  queryPackTenantID,
		StreamId:  streamID,
		TimeRange: timeRange,
	})
	if err != nil {
		t.Fatalf("GetStreamHealth5M: %v", err)
	}
	// The six single-sample blocks share one bucket, which reports the tier of
	// its latest sample with a known height.
	if records := resp.GetRecords(); len(records) != 1 || records[0].GetQualityTier() != "1080p" {
		tiers := []string{}
		for _, record := range records {
			tiers = append(tiers, record.GetQualityTier())
		}
		t.Fatalf("bucket tiers = %v, want one 1080p bucket", tiers)
	}

	summaryStreamID := streamID
	summary, err := server.GetStreamHealthSummary(ctx, &periscopepb.GetStreamHealthSummaryRequest{
		TenantId:  queryPackTenantID,
		StreamId:  &summaryStreamID,
		TimeRange: timeRange,
	})
	if err != nil {
		t.Fatalf("GetStreamHealthSummary: %v", err)
	}
	if got := summary.GetSummary().GetCurrentQualityTier(); got != "1080p" {
		t.Fatalf("current quality tier = %q, want 1080p", got)
	}
}
