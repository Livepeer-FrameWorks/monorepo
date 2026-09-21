// Package maintenance runs Bosun's periodic ledger work: retention pruning and
// the oldest-due-delivery gauge.
package maintenance

import (
	"context"
	"time"

	"frameworks/api_webhooks/internal/ledger"
	"frameworks/api_webhooks/internal/metrics"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

const (
	// PruneInterval is how often retention runs.
	PruneInterval = time.Hour
	// pruneBatch is how many rows one DELETE removes.
	pruneBatch = 1000
	// GaugeInterval is how often the oldest due delivery is sampled.
	GaugeInterval = 15 * time.Second
)

// Runner prunes the ledger and samples the backlog gauge.
type Runner struct {
	Store   *ledger.Store
	Metrics *metrics.Metrics
	Logger  logging.Logger
}

// Run works until ctx ends. Every replica runs it; concurrent prunes delete
// disjoint batches or nothing.
func (r *Runner) Run(ctx context.Context) {
	prune := time.NewTicker(PruneInterval)
	defer prune.Stop()
	gauge := time.NewTicker(GaugeInterval)
	defer gauge.Stop()
	r.sample(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-prune.C:
			r.PruneOnce(ctx)
		case <-gauge.C:
			r.sample(ctx)
		}
	}
}

// PruneOnce runs one retention pass.
func (r *Runner) PruneOnce(ctx context.Context) {
	result, err := r.Store.Prune(ctx, pruneBatch)
	r.Metrics.Prune("webhook_events", result.Events)
	r.Metrics.Prune("webhook_test_deliveries", result.TestDeliveries)
	r.Metrics.Prune("webhook_notification_outbox", result.Notifications)
	r.Metrics.Prune("webhook_endpoint_secrets", result.Secrets)
	if err != nil && ctx.Err() == nil && r.Logger != nil {
		r.Logger.WithError(err).Warn("Webhook ledger retention pass failed")
	}
}

func (r *Runner) sample(ctx context.Context) {
	seconds, err := r.Store.OldestDueSeconds(ctx)
	if err != nil {
		if ctx.Err() == nil && r.Logger != nil {
			r.Logger.WithError(err).Warn("Sampling the oldest due webhook delivery failed")
		}
		return
	}
	r.Metrics.SetOldestDue(seconds)
}
