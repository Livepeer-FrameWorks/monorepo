package resolvers

import (
	"testing"
	"time"

	"frameworks/api_gateway/graph/model"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func usageRecord(usageType string, value float64, periodStart time.Time) *purserpb.UsageRecord {
	return &purserpb.UsageRecord{
		UsageType:   usageType,
		UsageValue:  value,
		PeriodStart: timestamppb.New(periodStart),
		CreatedAt:   timestamppb.New(periodStart),
	}
}

// buildUsageAggregates filters by usage type and (optional) time window, buckets
// by granularity, sums values within a bucket, and returns chronologically
// sorted aggregates. These are the read-surface invariants a client depends on;
// a regression here silently mis-reports usage.
func TestBuildUsageAggregates(t *testing.T) {
	h9 := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	h10a := time.Date(2026, 4, 1, 10, 15, 0, 0, time.UTC)
	h10b := time.Date(2026, 4, 1, 10, 45, 0, 0, time.UTC)
	h11 := time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC)

	records := []*purserpb.UsageRecord{
		usageRecord("egress_gb", 9, h9),
		usageRecord("egress_gb", 2, h10a),
		usageRecord("egress_gb", 3, h10b),
		usageRecord("storage_gb", 5, h11),
		nil, // must be skipped, not panic
	}

	t.Run("filters by usage type, buckets, sums, and sorts ascending", func(t *testing.T) {
		got := buildUsageAggregates(records, nil, "hourly", []string{"egress_gb"})
		if len(got) != 2 {
			t.Fatalf("got %d aggregates, want 2 (storage_gb filtered, 10:00 bucket merged)", len(got))
		}
		if got[0].GetPeriodStart().AsTime().Hour() != 9 || got[0].UsageValue != 9 {
			t.Errorf("bucket[0] = hour %d value %v, want hour 9 value 9", got[0].GetPeriodStart().AsTime().Hour(), got[0].UsageValue)
		}
		if got[1].GetPeriodStart().AsTime().Hour() != 10 || got[1].UsageValue != 5 {
			t.Errorf("bucket[1] = hour %d value %v, want hour 10 value 5 (2+3)", got[1].GetPeriodStart().AsTime().Hour(), got[1].UsageValue)
		}
	})

	t.Run("time window excludes records outside [start, end]", func(t *testing.T) {
		tr := &model.TimeRangeInput{
			Start: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
			End:   time.Date(2026, 4, 1, 10, 59, 0, 0, time.UTC),
		}
		got := buildUsageAggregates(records, tr, "hourly", []string{"egress_gb"})
		if len(got) != 1 {
			t.Fatalf("got %d aggregates, want 1 (only the 10:00 bucket survives the window)", len(got))
		}
		if got[0].UsageValue != 5 {
			t.Errorf("value = %v, want 5", got[0].UsageValue)
		}
	})

	t.Run("empty usage-type filter includes all types", func(t *testing.T) {
		got := buildUsageAggregates(records, nil, "hourly", nil)
		// egress buckets at 09:00 and 10:00, storage bucket at 11:00 => 3
		if len(got) != 3 {
			t.Fatalf("got %d aggregates, want 3 with no type filter", len(got))
		}
	})
}

func TestBucketForGranularity(t *testing.T) {
	ts := time.Date(2026, 4, 15, 13, 37, 12, 0, time.UTC)
	tests := []struct {
		granularity string
		wantStart   time.Time
		wantEnd     time.Time
	}{
		{granularity: "monthly", wantStart: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), wantEnd: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)},
		{granularity: "daily", wantStart: time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC), wantEnd: time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC)},
		{granularity: "hourly", wantStart: time.Date(2026, 4, 15, 13, 0, 0, 0, time.UTC), wantEnd: time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC)},
		{granularity: "unknown defaults to hourly", wantStart: time.Date(2026, 4, 15, 13, 0, 0, 0, time.UTC), wantEnd: time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC)},
	}
	for _, tc := range tests {
		t.Run(tc.granularity, func(t *testing.T) {
			start, end := bucketForGranularity(ts, tc.granularity)
			if !start.Equal(tc.wantStart) || !end.Equal(tc.wantEnd) {
				t.Errorf("bucket = [%s, %s], want [%s, %s]", start, end, tc.wantStart, tc.wantEnd)
			}
		})
	}
}
