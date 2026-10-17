package outbox

import (
	"context"
	"time"
)

// Delivered rows are kept a week for inspection, then deleted in bounded batches. A pass stops after
// MaxPruneBatchesPerPass so one backlog cannot hold the worker loop; the next pass continues where it left off.
const (
	CompletedRetention     = 7 * 24 * time.Hour
	PruneBatch             = 1000
	MaxPruneBatchesPerPass = 100
)

// PurgeBatchFunc deletes up to batchLimit rows delivered more than retentionSeconds ago and returns how many it
// deleted. Undelivered rows must never match.
type PurgeBatchFunc func(ctx context.Context, retentionSeconds int64, batchLimit int32) (int64, error)

// PruneCompletedBatches is the shared body of a Store's PruneCompleted: it calls purge with CompletedRetention and
// PruneBatch until a batch comes back short, the pass cap is reached, or ctx ends.
func PruneCompletedBatches(ctx context.Context, purge PurgeBatchFunc) (int64, error) {
	var total int64
	for range MaxPruneBatchesPerPass {
		n, err := purge(ctx, int64(CompletedRetention.Seconds()), PruneBatch)
		total += n
		if err != nil {
			return total, err
		}
		if n < PruneBatch || ctx.Err() != nil {
			return total, ctx.Err()
		}
	}
	return total, nil
}
