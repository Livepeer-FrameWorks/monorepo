package graph

import (
	"testing"
	"time"
)

func TestParsePeriodRange(t *testing.T) {
	t.Run("valid RFC3339 range", func(t *testing.T) {
		start, end := parsePeriodRange("2024-01-01T00:00:00Z/2024-01-02T00:00:00Z")
		if start == nil || end == nil {
			t.Fatalf("got start=%v end=%v, want both non-nil", start, end)
		}
		if !start.Equal(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("start = %v, want 2024-01-01T00:00:00Z", start)
		}
		if !end.Equal(time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("end = %v, want 2024-01-02T00:00:00Z", end)
		}
	})

	// Malformed input must yield (nil, nil) rather than a partial/zero range —
	// callers treat nil as "no period filter".
	bad := []string{
		"",                                // empty
		"2024-01-01T00:00:00Z",            // missing delimiter
		"not-a-time/2024-01-02T00:00:00Z", // bad start
		"2024-01-01T00:00:00Z/not-a-time", // bad end
		"2024-01-01/2024-01-02",           // not RFC3339
	}
	for _, in := range bad {
		start, end := parsePeriodRange(in)
		if start != nil || end != nil {
			t.Errorf("parsePeriodRange(%q) = (%v,%v), want (nil,nil)", in, start, end)
		}
	}
}
