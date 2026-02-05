package grpc

import (
	"strings"
	"testing"
	"time"

	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestParseEventPayload(t *testing.T) {
	t.Run("empty payload", func(t *testing.T) {
		if got := parseEventPayload(""); got != nil {
			t.Fatal("expected nil for empty payload")
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		if got := parseEventPayload("not-json"); got != nil {
			t.Fatal("expected nil for invalid json")
		}
	})

	t.Run("valid json", func(t *testing.T) {
		payload := `{"viewer_id":"viewer-1","count":3}`
		got := parseEventPayload(payload)
		if got == nil {
			t.Fatal("expected struct for valid json")
		}
		if got.Fields["viewer_id"].GetStringValue() != "viewer-1" {
			t.Fatalf("unexpected viewer_id: %v", got.Fields["viewer_id"])
		}
		if got.Fields["count"].GetNumberValue() != 3 {
			t.Fatalf("unexpected count: %v", got.Fields["count"])
		}
	})
}

func TestValidateTimeRangeProto(t *testing.T) {
	t.Run("nil range defaults", func(t *testing.T) {
		start, end, err := validateTimeRangeProto(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if end.Before(start) {
			t.Fatal("expected end after start")
		}
		delta := end.Sub(start)
		if delta < 23*time.Hour || delta > 25*time.Hour {
			t.Fatalf("expected ~24h range, got %s", delta)
		}
	})

	t.Run("zero timestamps default", func(t *testing.T) {
		rangeProto := &pb.TimeRange{
			Start: timestamppb.New(time.Time{}),
			End:   timestamppb.New(time.Time{}),
		}
		start, end, err := validateTimeRangeProto(rangeProto)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !end.After(start) {
			t.Fatal("expected end after start")
		}
		if !strings.Contains(end.Sub(start).String(), "24h") {
			t.Fatalf("expected 24h range, got %s", end.Sub(start))
		}
	})

	t.Run("explicit timestamps", func(t *testing.T) {
		startTime := time.Now().Add(-2 * time.Hour).UTC()
		endTime := time.Now().Add(-time.Hour).UTC()
		rangeProto := &pb.TimeRange{
			Start: timestamppb.New(startTime),
			End:   timestamppb.New(endTime),
		}
		start, end, err := validateTimeRangeProto(rangeProto)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !start.Equal(startTime) {
			t.Fatalf("expected start %v, got %v", startTime, start)
		}
		if !end.Equal(endTime) {
			t.Fatalf("expected end %v, got %v", endTime, end)
		}
	})
}
