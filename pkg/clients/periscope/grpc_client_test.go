package periscope

import (
	"strings"
	"testing"
	"time"

	"frameworks/pkg/pagination"
)

func TestBuildTimeRange(t *testing.T) {
	t.Run("nil opts returns nil", func(t *testing.T) {
		if got := buildTimeRange(nil); got != nil {
			t.Fatalf("expected nil time range, got %#v", got)
		}
	})

	t.Run("builds proto range from opts", func(t *testing.T) {
		start := time.Date(2026, 2, 1, 1, 2, 3, 0, time.UTC)
		end := time.Date(2026, 2, 1, 2, 3, 4, 0, time.UTC)
		got := buildTimeRange(&TimeRangeOpts{
			StartTime: start,
			EndTime:   end,
		})
		if got == nil {
			t.Fatal("expected non-nil time range")
		}
		if !got.Start.AsTime().Equal(start) {
			t.Fatalf("expected start %s, got %s", start, got.Start.AsTime())
		}
		if !got.End.AsTime().Equal(end) {
			t.Fatalf("expected end %s, got %s", end, got.End.AsTime())
		}
	})
}

func TestBuildCursorPagination(t *testing.T) {
	after := "after-cursor"
	before := "before-cursor"

	t.Run("nil opts use default first limit", func(t *testing.T) {
		got := buildCursorPagination(nil)
		if got.First != int32(pagination.DefaultLimit) {
			t.Fatalf("expected first=%d, got %d", pagination.DefaultLimit, got.First)
		}
		if got.Last != 0 || got.After != nil || got.Before != nil {
			t.Fatalf("unexpected default pagination: %#v", got)
		}
	})

	t.Run("copies explicit cursor fields", func(t *testing.T) {
		got := buildCursorPagination(&CursorPaginationOpts{
			First:  25,
			After:  &after,
			Last:   5,
			Before: &before,
		})
		if got.First != 25 || got.Last != 5 {
			t.Fatalf("unexpected limits: first=%d last=%d", got.First, got.Last)
		}
		if got.GetAfter() != after || got.GetBefore() != before {
			t.Fatalf("unexpected cursor fields: after=%q before=%q", got.GetAfter(), got.GetBefore())
		}
	})
}

func TestRequireTenantID(t *testing.T) {
	cases := []struct {
		name      string
		tenantID  string
		wantError bool
	}{
		{name: "empty tenant", tenantID: "", wantError: true},
		{name: "non-empty tenant", tenantID: "tenant-1", wantError: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := requireTenantID(tc.tenantID)
			if tc.wantError {
				if err == nil {
					t.Fatal("expected error")
				}
				if !strings.Contains(err.Error(), "tenantID required") {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
