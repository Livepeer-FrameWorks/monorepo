package notify

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"frameworks/api_incidents/internal/database/lookoutdb"
	"frameworks/api_incidents/internal/incidents"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// Outbox retention. Delivered rows are kept a week for delivery diagnostics;
// failed rows are kept a month so operators can still read last_error. Each
// sweep deletes at most retentionMaxBatches batches per state so one sweep
// cannot hold the database for long; a larger backlog drains over later sweeps.
const (
	deliveredRetention  = 7 * 24 * time.Hour
	failedRetention     = 30 * 24 * time.Hour
	retentionInterval   = time.Hour
	retentionBatchSize  = 500
	retentionMaxBatches = 20
)

// Retention deletes settled incident and operator-activity outbox rows past
// retention.
type Retention struct {
	DB      *sql.DB
	Metrics *incidents.Metrics
	Logger  logging.Logger
	Now     func() time.Time
}

// Run sweeps once immediately and then every retentionInterval until ctx ends.
// Every replica runs it; concurrent deletes of the same expired rows are harmless.
func (r *Retention) Run(ctx context.Context) {
	ticker := time.NewTicker(retentionInterval)
	defer ticker.Stop()
	for {
		if _, err := r.Sweep(ctx); err != nil && ctx.Err() == nil && r.Logger != nil {
			r.Logger.WithError(err).Warn("Lookout outbox retention sweep failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Sweep deletes delivered rows older than deliveredRetention and failed rows
// older than failedRetention, and returns the number of rows deleted.
func (r *Retention) Sweep(ctx context.Context) (int64, error) {
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	q := lookoutdb.New(r.DB)
	delivered, err := deleteInBatches(ctx, func(ctx context.Context) (int64, error) {
		return q.DeleteDeliveredOutboxRows(ctx, lookoutdb.DeleteDeliveredOutboxRowsParams{
			DeliveredBefore: now.Add(-deliveredRetention),
			BatchSize:       retentionBatchSize,
		})
	})
	r.Metrics.ObserveOutboxDeleted("delivered", delivered)
	if err != nil {
		return delivered, fmt.Errorf("delete delivered outbox rows: %w", err)
	}
	failed, err := deleteInBatches(ctx, func(ctx context.Context) (int64, error) {
		return q.DeleteFailedOutboxRows(ctx, lookoutdb.DeleteFailedOutboxRowsParams{
			FailedBefore: now.Add(-failedRetention),
			BatchSize:    retentionBatchSize,
		})
	})
	r.Metrics.ObserveOutboxDeleted("failed", failed)
	if err != nil {
		return delivered + failed, fmt.Errorf("delete failed outbox rows: %w", err)
	}
	activityDelivered, err := deleteInBatches(ctx, func(ctx context.Context) (int64, error) {
		return q.DeleteDeliveredOperatorActivityRows(ctx, lookoutdb.DeleteDeliveredOperatorActivityRowsParams{
			DeliveredBefore: now.Add(-deliveredRetention),
			BatchSize:       retentionBatchSize,
		})
	})
	r.Metrics.ObserveOutboxDeleted("activity_delivered", activityDelivered)
	if err != nil {
		return delivered + failed + activityDelivered, fmt.Errorf("delete delivered operator activity rows: %w", err)
	}
	activityFailed, err := deleteInBatches(ctx, func(ctx context.Context) (int64, error) {
		return q.DeleteFailedOperatorActivityRows(ctx, lookoutdb.DeleteFailedOperatorActivityRowsParams{
			FailedBefore: now.Add(-failedRetention),
			BatchSize:    retentionBatchSize,
		})
	})
	r.Metrics.ObserveOutboxDeleted("activity_failed", activityFailed)
	if err != nil {
		return delivered + failed + activityDelivered + activityFailed, fmt.Errorf("delete failed operator activity rows: %w", err)
	}
	return delivered + failed + activityDelivered + activityFailed, nil
}

// deleteInBatches repeats a bounded delete until a batch comes back short or
// retentionMaxBatches batches have run.
func deleteInBatches(ctx context.Context, deleteBatch func(context.Context) (int64, error)) (int64, error) {
	var total int64
	for batch := 0; batch < retentionMaxBatches; batch++ {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		deleted, err := deleteBatch(ctx)
		total += deleted
		if err != nil {
			return total, err
		}
		if deleted < retentionBatchSize {
			return total, nil
		}
	}
	return total, nil
}
