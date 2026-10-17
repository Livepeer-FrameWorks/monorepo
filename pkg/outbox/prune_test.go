package outbox

import (
	"context"
	"errors"
	"testing"
)

func TestPruneCompletedBatchesDrainsUntilShortBatch(t *testing.T) {
	remaining := int64(2*PruneBatch + 5)
	calls := 0
	deleted, err := PruneCompletedBatches(context.Background(), func(_ context.Context, retention int64, limit int32) (int64, error) {
		calls++
		if retention != int64(CompletedRetention.Seconds()) || limit != PruneBatch {
			t.Fatalf("retention=%d limit=%d, want %d and %d", retention, limit, int64(CompletedRetention.Seconds()), PruneBatch)
		}
		n := min(remaining, int64(limit))
		remaining -= n
		return n, nil
	})
	if err != nil || deleted != 2*PruneBatch+5 || calls != 3 {
		t.Fatalf("deleted=%d calls=%d err=%v, want %d in 3 batches", deleted, calls, err, 2*PruneBatch+5)
	}
}

func TestPruneCompletedBatchesCapsOnePass(t *testing.T) {
	calls := 0
	deleted, err := PruneCompletedBatches(context.Background(), func(context.Context, int64, int32) (int64, error) {
		calls++
		return PruneBatch, nil
	})
	if err != nil || calls != MaxPruneBatchesPerPass || deleted != int64(MaxPruneBatchesPerPass)*PruneBatch {
		t.Fatalf("calls=%d deleted=%d err=%v, want the pass capped at %d batches", calls, deleted, err, MaxPruneBatchesPerPass)
	}
}

func TestPruneCompletedBatchesStopsOnError(t *testing.T) {
	calls := 0
	boom := errors.New("boom")
	if _, err := PruneCompletedBatches(context.Background(), func(context.Context, int64, int32) (int64, error) {
		calls++
		return 0, boom
	}); !errors.Is(err, boom) || calls != 1 {
		t.Fatalf("err=%v calls=%d, want boom after one call", err, calls)
	}
}
