package middleware

import (
	"testing"
	"time"
)

func TestUsageTrackerFlushWithNilDecklogDoesNotResetAggregates(t *testing.T) {
	tracker := NewUsageTracker(UsageTrackerConfig{
		Decklog:       nil,
		FlushInterval: time.Hour,
	})
	defer tracker.Stop()

	startedAt := time.Now()
	tracker.Record(startedAt, "tenant-1", "jwt", "query", "GetStreams", "user-1", 0, 100, 5, 0)

	tracker.flush()

	var requestCount uint32
	tracker.aggregates.Range(func(_, value any) bool {
		agg := value.(*aggregate)
		agg.mu.Lock()
		requestCount = agg.RequestCount
		agg.mu.Unlock()
		return false
	})

	if requestCount != 1 {
		t.Fatalf("expected request_count to remain 1 when Decklog is nil, got %d", requestCount)
	}
}
