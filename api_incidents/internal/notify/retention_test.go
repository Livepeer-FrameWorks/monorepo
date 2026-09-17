package notify

import (
	"context"
	"errors"
	"testing"
)

func TestDeleteInBatchesStopsAtShortBatch(t *testing.T) {
	calls := 0
	total, err := deleteInBatches(context.Background(), func(context.Context) (int64, error) {
		calls++
		if calls < 3 {
			return retentionBatchSize, nil
		}
		return 7, nil
	})
	if err != nil || total != 2*retentionBatchSize+7 || calls != 3 {
		t.Fatalf("total = %d, calls = %d, err = %v", total, calls, err)
	}
}

func TestDeleteInBatchesBoundsOneSweep(t *testing.T) {
	calls := 0
	total, err := deleteInBatches(context.Background(), func(context.Context) (int64, error) {
		calls++
		return retentionBatchSize, nil
	})
	if err != nil || calls != retentionMaxBatches || total != int64(retentionMaxBatches*retentionBatchSize) {
		t.Fatalf("total = %d, calls = %d, err = %v", total, calls, err)
	}
}

func TestDeleteInBatchesReturnsDeletedRowsWithError(t *testing.T) {
	boom := errors.New("database unavailable")
	calls := 0
	total, err := deleteInBatches(context.Background(), func(context.Context) (int64, error) {
		calls++
		if calls == 2 {
			return 0, boom
		}
		return retentionBatchSize, nil
	})
	if !errors.Is(err, boom) || total != retentionBatchSize {
		t.Fatalf("total = %d, err = %v", total, err)
	}
}

func TestDeleteInBatchesStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	total, err := deleteInBatches(ctx, func(context.Context) (int64, error) {
		t.Fatal("delete ran after cancellation")
		return 0, nil
	})
	if !errors.Is(err, context.Canceled) || total != 0 {
		t.Fatalf("total = %d, err = %v", total, err)
	}
}
