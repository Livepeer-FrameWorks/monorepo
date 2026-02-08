package grpc

import (
	"context"
	"strings"
	"testing"
	"time"

	"frameworks/pkg/ctxkeys"
	pb "frameworks/pkg/proto"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

func TestRequireTenantID(t *testing.T) {
	t.Run("missing tenant context", func(t *testing.T) {
		_, err := requireTenantID(context.Background(), "")
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected invalid argument, got %v", err)
		}
	})

	t.Run("uses context tenant when present", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-a")
		tenantID, err := requireTenantID(ctx, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tenantID != "tenant-a" {
			t.Fatalf("expected tenant-a, got %s", tenantID)
		}
	})

	t.Run("rejects mismatched tenant id", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-a")
		_, err := requireTenantID(ctx, "tenant-b")
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("expected permission denied, got %v", err)
		}
	})

	t.Run("allows request tenant for service calls", func(t *testing.T) {
		tenantID, err := requireTenantID(context.Background(), "tenant-service")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tenantID != "tenant-service" {
			t.Fatalf("expected tenant-service, got %s", tenantID)
		}
	})
}

func TestValidateRelatedTenantIDs(t *testing.T) {
	t.Run("allows empty related list", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-a")
		if err := validateRelatedTenantIDs(ctx, nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("rejects related list for authenticated tenant", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-a")
		err := validateRelatedTenantIDs(ctx, []string{"tenant-b"})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("expected permission denied, got %v", err)
		}
	})

	t.Run("allows related list for service calls", func(t *testing.T) {
		if err := validateRelatedTenantIDs(context.Background(), []string{"tenant-b"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}
