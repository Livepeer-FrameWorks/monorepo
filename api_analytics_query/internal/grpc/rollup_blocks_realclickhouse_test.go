//go:build schema_verify

package grpc

import (
	"math"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestRollupAveragesDoNotDependOnInsertBlocks_RealClickHouse writes one
// 5-minute bucket of health and client samples in two inserts of different
// sizes, the way batched ingest does, and requires the bucket's averages,
// loss rate and distinct sessions to be those of all its samples.
func TestRollupAveragesDoNotDependOnInsertBlocks_RealClickHouse(t *testing.T) {
	db := startQueryPackClickHouse(t)
	server := NewPeriscopeServer(db, logging.NewLoggerWithService("rollup-blocks-real-ch-test"))
	ctx := serviceTestContext()
	const streamID = "5eedfeed-11fe-ca57-feed-11feca5700b1"
	bucket := time.Now().UTC().Truncate(5 * time.Minute).Add(-10 * time.Minute)

	// Three samples at 1000 kbps in one insert, one at 5000 kbps in another:
	// the bucket averages 2000, not the 3000 of the two block averages.
	healthBlock := `INSERT INTO periscope.stream_health_samples
		(timestamp, tenant_id, stream_id, internal_name, node_id, buffer_state, bitrate, fps, buffer_health)
		SELECT ?, ?, ?, 'blocks-stream', 'edge-1', 'FULL', ?, ?, ? FROM numbers(?)`
	if _, err := db.ExecContext(ctx, healthBlock, bucket.Add(10*time.Second), queryPackTenantID, streamID, 1000, 30, 0.5, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, healthBlock, bucket.Add(20*time.Second), queryPackTenantID, streamID, 5000, 60, 1.0, 1); err != nil {
		t.Fatal(err)
	}

	// Sessions s1, s1, s2 sending 1000 B/s with no loss, then s1 sending
	// 5000 B/s losing 40 of 100 packets: two distinct sessions, 2000 B/s on
	// average, and 40 of 400 packets lost.
	qoeBlock := `INSERT INTO periscope.client_qoe_samples
		(timestamp, tenant_id, stream_id, internal_name, session_id, node_id, protocol, host,
		 connection_time, bandwidth_in, bandwidth_out, bytes_downloaded, bytes_uploaded,
		 packets_sent, packets_lost, packets_retransmitted)
		SELECT ?, ?, ?, 'blocks-stream', arrayElement(?, number + 1), 'edge-1', 'HLS', 'h',
		       ?, 0, ?, 0, 0, 100, ?, 0 FROM numbers(?)`
	if _, err := db.ExecContext(ctx, qoeBlock, bucket.Add(10*time.Second), queryPackTenantID, streamID, []string{"s1", "s1", "s2"}, 10, 1000, 0, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, qoeBlock, bucket.Add(20*time.Second), queryPackTenantID, streamID, []string{"s1"}, 30, 5000, 40, 1); err != nil {
		t.Fatal(err)
	}

	timeRange := &commonpb.TimeRange{
		Start: timestamppb.New(bucket.Add(-time.Minute)),
		End:   timestamppb.New(bucket.Add(5 * time.Minute)),
	}
	near := func(got, want float64) bool { return math.Abs(got-want) < 1e-3 }

	health, err := server.GetStreamHealth5M(ctx, &periscopepb.GetStreamHealth5MRequest{
		TenantId: queryPackTenantID, StreamId: streamID, TimeRange: timeRange,
	})
	if err != nil {
		t.Fatalf("GetStreamHealth5M: %v", err)
	}
	if records := health.GetRecords(); len(records) != 1 || records[0].GetAvgBitrate() != 2000 ||
		!near(float64(records[0].GetAvgFps()), 37.5) || !near(float64(records[0].GetAvgBufferHealth()), 0.625) {
		t.Fatalf("health buckets = %v, want one bucket at 2000 kbps, 37.5 fps, 0.625 buffer health", records)
	}
	summaryStream := streamID
	healthSummary, err := server.GetStreamHealthSummary(ctx, &periscopepb.GetStreamHealthSummaryRequest{
		TenantId: queryPackTenantID, StreamId: &summaryStream, TimeRange: timeRange,
	})
	if err != nil {
		t.Fatalf("GetStreamHealthSummary: %v", err)
	}
	if s := healthSummary.GetSummary(); !near(s.GetAvgBitrate(), 2000) || s.GetSampleCount() != 1 {
		t.Fatalf("health summary avg bitrate %v over %d buckets, want 2000 over 1", s.GetAvgBitrate(), s.GetSampleCount())
	}

	qoe, err := server.GetClientMetrics5M(ctx, &periscopepb.GetClientMetrics5MRequest{
		TenantId: queryPackTenantID, StreamId: &summaryStream, TimeRange: timeRange,
	})
	if err != nil {
		t.Fatalf("GetClientMetrics5M: %v", err)
	}
	if records := qoe.GetRecords(); len(records) != 1 || records[0].GetActiveSessions() != 2 ||
		!near(records[0].GetAvgBandwidthOut(), 2000) || !near(float64(records[0].GetAvgConnectionTime()), 15) ||
		!near(float64(records[0].GetPacketLossRate()), 0.1) {
		t.Fatalf("client buckets = %v, want one bucket of 2 sessions at 2000 B/s, 15 s, 10%% loss", records)
	}
	qoeSummary, err := server.GetClientQoeSummary(ctx, &periscopepb.GetClientQoeSummaryRequest{
		TenantId: queryPackTenantID, StreamId: &summaryStream, TimeRange: timeRange,
	})
	if err != nil {
		t.Fatalf("GetClientQoeSummary: %v", err)
	}
	if s := qoeSummary.GetSummary(); s.GetTotalActiveSessions() != 2 || !near(s.GetAvgBandwidthOut(), 2000) ||
		!near(s.GetAvgPacketLossRate(), 0.1) || !near(s.GetPeakPacketLossRate(), 0.1) {
		t.Fatalf("client summary = %v, want 2 sessions, 2000 B/s, 10%% average and peak loss", s)
	}
}

// TestRebufferingTransitionsSpanInsertBlocks_RealClickHouse writes a FULL,
// DRY, RECOVER sequence as three separate inserts and requires the rebuffer
// start and end between them.
func TestRebufferingTransitionsSpanInsertBlocks_RealClickHouse(t *testing.T) {
	db := startQueryPackClickHouse(t)
	server := NewPeriscopeServer(db, logging.NewLoggerWithService("rebuffering-blocks-real-ch-test"))
	ctx := serviceTestContext()
	const streamID = "5eedfeed-11fe-ca57-feed-11feca5700c1"
	start := time.Now().UTC().Truncate(time.Second).Add(-10 * time.Minute)

	insert := `INSERT INTO periscope.stream_health_samples
		(timestamp, tenant_id, stream_id, internal_name, node_id, buffer_state)
		VALUES (?, ?, ?, 'rebuffer-stream', 'edge-1', ?)`
	for i, state := range []string{"FULL", "DRY", "RECOVER", "FULL"} {
		if _, err := db.ExecContext(ctx, insert, start.Add(time.Duration(i)*10*time.Second), queryPackTenantID, streamID, state); err != nil {
			t.Fatal(err)
		}
	}

	stream := streamID
	resp, err := server.GetRebufferingEvents(ctx, &periscopepb.GetRebufferingEventsRequest{
		TenantId: queryPackTenantID,
		StreamId: &stream,
		// The range starts after the first FULL, whose state the DRY sample
		// still transitions from.
		TimeRange: &commonpb.TimeRange{
			Start: timestamppb.New(start.Add(5 * time.Second)),
			End:   timestamppb.New(start.Add(time.Minute)),
		},
	})
	if err != nil {
		t.Fatalf("GetRebufferingEvents: %v", err)
	}
	var starts, ends []string
	for _, event := range resp.GetEvents() {
		if event.GetRebufferStart() != nil {
			starts = append(starts, event.GetPrevState()+">"+event.GetBufferState())
		}
		if event.GetRebufferEnd() != nil {
			ends = append(ends, event.GetPrevState()+">"+event.GetBufferState())
		}
	}
	if len(resp.GetEvents()) != 3 || len(starts) != 1 || starts[0] != "FULL>DRY" || len(ends) != 1 || ends[0] != "DRY>RECOVER" {
		t.Fatalf("events = %v; rebuffer starts %v, ends %v; want 3 events, one FULL>DRY start and one DRY>RECOVER end", resp.GetEvents(), starts, ends)
	}
}

// TestRebufferingTransitionsStayOnOneNode_RealClickHouse samples one stream
// on an origin and an edge, which each hold their own buffer, and samples the
// origin twice within one second the way Helmsman's periodic poll and a
// stream-update refresh do. No transition may cross nodes, and paging one
// event at a time must return every sample once.
func TestRebufferingTransitionsStayOnOneNode_RealClickHouse(t *testing.T) {
	db := startQueryPackClickHouse(t)
	server := NewPeriscopeServer(db, logging.NewLoggerWithService("rebuffering-nodes-real-ch-test"))
	ctx := serviceTestContext()
	const streamID = "5eedfeed-11fe-ca57-feed-11feca5700c2"
	start := time.Now().UTC().Truncate(time.Second).Add(-10 * time.Minute)

	insert := `INSERT INTO periscope.stream_health_samples
		(timestamp, tenant_id, stream_id, internal_name, node_id, buffer_state, ingest_id)
		VALUES (?, ?, ?, 'rebuffer-nodes', ?, ?, ?)`
	samples := []struct {
		offset      time.Duration
		node, state string
		ingestID    string
	}{
		{0, "origin", "FULL", "00000000-0000-4000-8000-000000000001"},
		{10 * time.Second, "edge", "DRY", "00000000-0000-4000-8000-000000000002"},
		{20 * time.Second, "origin", "FULL", "00000000-0000-4000-8000-000000000003"},
		{30 * time.Second, "edge", "DRY", "00000000-0000-4000-8000-000000000004"},
		{40 * time.Second, "origin", "FULL", "00000000-0000-4000-8000-000000000005"},
		{40 * time.Second, "origin", "FULL", "00000000-0000-4000-8000-000000000006"},
	}
	for _, sample := range samples {
		if _, err := db.ExecContext(ctx, insert, start.Add(sample.offset), queryPackTenantID, streamID, sample.node, sample.state, sample.ingestID); err != nil {
			t.Fatal(err)
		}
	}

	stream := streamID
	request := func(first int32, after *string) *periscopepb.GetRebufferingEventsResponse {
		t.Helper()
		resp, err := server.GetRebufferingEvents(ctx, &periscopepb.GetRebufferingEventsRequest{
			TenantId:   queryPackTenantID,
			StreamId:   &stream,
			TimeRange:  &commonpb.TimeRange{Start: timestamppb.New(start.Add(-time.Second)), End: timestamppb.New(start.Add(time.Minute))},
			Pagination: &commonpb.CursorPaginationRequest{First: first, After: after},
		})
		if err != nil {
			t.Fatalf("GetRebufferingEvents: %v", err)
		}
		return resp
	}

	var transitions []string
	ids := map[string]bool{}
	for _, event := range request(int32(len(samples)), nil).GetEvents() {
		ids[event.GetId()] = true
		if event.GetRebufferStart() != nil || event.GetRebufferEnd() != nil {
			transitions = append(transitions, event.GetNodeId()+":"+event.GetPrevState()+">"+event.GetBufferState())
		}
	}
	if len(transitions) != 0 {
		t.Fatalf("rebuffer transitions %v, want none: the edge stayed DRY and the origin FULL", transitions)
	}
	if len(ids) != len(samples) {
		t.Fatalf("%d distinct event IDs, want one per sample (%d)", len(ids), len(samples))
	}

	seen := map[string]bool{}
	var after *string
	for page := 0; page < len(samples)+2; page++ {
		resp := request(1, after)
		for _, event := range resp.GetEvents() {
			if seen[event.GetId()] {
				t.Fatalf("event %s returned twice", event.GetId())
			}
			seen[event.GetId()] = true
		}
		if !resp.GetPagination().GetHasNextPage() {
			break
		}
		after = resp.GetPagination().EndCursor
	}
	if len(seen) != len(samples) {
		t.Fatalf("paged %d distinct events, want every one of the %d samples", len(seen), len(samples))
	}
}
