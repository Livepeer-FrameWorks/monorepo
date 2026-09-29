package grpc

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func nullableScanTimeRange() (time.Time, *commonpb.TimeRange) {
	start := time.Date(2026, 9, 29, 5, 0, 0, 0, time.UTC)
	return start, &commonpb.TimeRange{Start: timestamppb.New(start), End: timestamppb.New(start.Add(time.Hour))}
}

func TestStreamHealthMetricsReturnsSamplesWithUnmeasuredFields(t *testing.T) {
	db, server, mock := newLiveUsageSummaryServer(t)
	t.Cleanup(func() { _ = db.Close() })
	start, timeRange := nullableScanTimeRange()
	columns := []string{
		"timestamp", "tenant_id", "stream_id", "node_id",
		"bitrate", "fps", "gop_size", "frame_ms_max", "frame_ms_min", "frames_max", "frames_min", "keyframe_ms_max", "keyframe_ms_min", "frame_jitter_ms", "width", "height",
		"buffer_size", "buffer_health", "buffer_state",
		"codec", "quality_tier", "track_metadata",
		"has_issues", "issues_description", "track_count",
		"audio_channels", "audio_sample_rate", "audio_codec", "audio_bitrate",
	}
	// The first sample precedes the second keyframe (no GOP); the second is
	// an audio-only stream with no video measurements at all.
	mock.ExpectQuery(`(?s)FROM stream_health_samples`).
		WillReturnRows(sqlmock.NewRows(columns).
			AddRow(start, "tenant-1", "stream-1", "edge-1",
				uint32(3500000), float32(30), nil, nil, nil, nil, nil, nil, nil, float32(12), uint16(1920), uint16(1080),
				uint32(4000), float32(1), "FULL",
				"H264", "1080p", "{}",
				uint8(0), nil, uint16(2),
				uint8(2), uint32(48000), "AAC", uint32(128000)).
			AddRow(start.Add(10*time.Second), "tenant-1", "stream-1", "edge-1",
				nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
				nil, nil, "",
				nil, nil, "{}",
				uint8(0), nil, uint16(1),
				uint8(2), uint32(48000), "AAC", uint32(128000)))
	mock.ExpectQuery(`(?s)SELECT count\(\*\) FROM stream_health_samples`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int32(2)))

	streamID := "stream-1"
	resp, err := server.GetStreamHealthMetrics(context.Background(), &periscopepb.GetStreamHealthMetricsRequest{
		TenantId: "tenant-1", StreamId: &streamID, TimeRange: timeRange,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Metrics) != 2 {
		t.Fatalf("returned %d of 2 samples", len(resp.Metrics))
	}
	if got := resp.Metrics[0]; got.Bitrate != 3500000 || got.Width != 1920 || got.GopSize != 0 || got.Codec != "H264" {
		t.Fatalf("measured sample = %+v", got)
	}
	if got := resp.Metrics[1]; got.Bitrate != 0 || got.Width != 0 || got.Codec != "" || got.GetPrimaryAudioCodec() != "AAC" {
		t.Fatalf("audio-only sample = %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStreamEventsReturnsStreamEndWithoutStatus(t *testing.T) {
	db, server, mock := newLiveUsageSummaryServer(t)
	t.Cleanup(func() { _ = db.Close() })
	start, timeRange := nullableScanTimeRange()
	columns := []string{
		"event_id", "timestamp", "event_type", "status", "node_id", "event_data", "stream_id",
		"buffer_state", "has_issues", "track_count", "quality_tier",
		"primary_width", "primary_height", "primary_fps", "primary_codec", "primary_bitrate",
		"downloaded_bytes", "uploaded_bytes", "total_viewers", "total_inputs", "total_outputs", "viewer_seconds",
		"request_url", "protocol", "latitude", "longitude", "location", "country_code", "city",
		"source_region", "cluster_id", "stream_origin_region", "stream_origin_cluster_id", "schema_version",
	}
	row := func(eventID, eventType string, status driver.Value, at time.Time) []driver.Value {
		return []driver.Value{
			eventID, at, eventType, status, "edge-1", "", "stream-1",
			nil, nil, nil, nil,
			nil, nil, nil, nil, nil,
			nil, nil, nil, nil, nil, nil,
			nil, nil, nil, nil, nil, nil, nil,
			"eu", "cluster-eu", "eu", "cluster-eu", uint8(1),
		}
	}
	mock.ExpectQuery(`(?s)FROM periscope\.stream_event_log`).
		WillReturnRows(sqlmock.NewRows(columns).
			AddRow(row("event-1", "stream_start", "live", start)...).
			AddRow(row("event-2", "stream_end", nil, start.Add(time.Minute))...))
	mock.MatchExpectationsInOrder(false)
	mock.ExpectQuery(`(?s)SELECT count`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int32(2)))

	resp, err := server.GetStreamEvents(context.Background(), &periscopepb.GetStreamEventsRequest{
		TenantId: "tenant-1", StreamId: "stream-1", TimeRange: timeRange,
	})
	if err != nil {
		t.Fatal(err)
	}
	types := make([]string, 0, len(resp.Events))
	for _, event := range resp.Events {
		types = append(types, event.EventType)
	}
	if len(types) != 2 || types[1] != "stream_end" || resp.Events[1].Status != "" {
		t.Fatalf("events = %v", types)
	}
}
