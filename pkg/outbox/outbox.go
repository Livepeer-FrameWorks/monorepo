package outbox

import (
	"context"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

type Config struct {
	BaseBackoff        time.Duration
	MaxBackoff         time.Duration
	BatchSize          int
	PollPeriod         time.Duration
	Lease              time.Duration
	AlertAfterAttempts int
}

type Claim[P any] struct {
	ID       string
	Attempts int
	Payload  P
}

type Store[P any] interface {
	ClaimBatch(ctx context.Context, batchSize int, lease time.Duration) ([]Claim[P], error)
	MarkCompleted(ctx context.Context, id string) error
	RecordFailure(ctx context.Context, id string, currentAttempts int, failedTargets []string, cause error, backoff time.Duration) error
}

type Dispatcher[P any] interface {
	Dispatch(ctx context.Context, payload P) (failedTargets []string, err error)
}

type Worker[P any] struct {
	Config     Config
	Store      Store[P]
	Dispatcher Dispatcher[P]
	Logger     logging.Logger
	// AlertLabel is the prefix used in the Error log line that fires past
	// Config.AlertAfterAttempts so on-call alerting can route by domain.
	AlertLabel string
}

func (w *Worker[P]) Run(ctx context.Context) {
	if w.Dispatcher == nil || w.Store == nil {
		if w.Logger != nil {
			w.Logger.Info("outbox worker disabled: missing store or dispatcher")
		}
		return
	}
	ticker := time.NewTicker(w.Config.PollPeriod)
	defer ticker.Stop()
	for {
		w.ProcessBatch(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker[P]) ProcessBatch(ctx context.Context) {
	var claims []Claim[P]
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var claimErr error
		claims, claimErr = w.Store.ClaimBatch(ctx, w.Config.BatchSize, w.Config.Lease)
		return claimErr
	})
	if err != nil {
		if w.Logger != nil {
			w.Logger.WithError(err).Warn("claim outbox batch failed")
		}
		return
	}
	for _, c := range claims {
		failed, dispatchErr := w.Dispatcher.Dispatch(ctx, c.Payload)
		if dispatchErr == nil && len(failed) == 0 {
			mErr := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
				return w.Store.MarkCompleted(ctx, c.ID)
			})
			if mErr != nil && w.Logger != nil {
				w.Logger.WithError(mErr).WithField("outbox_id", c.ID).Warn("mark outbox completed failed")
			}
			continue
		}
		w.recordFailure(ctx, c.ID, c.Attempts, failed, dispatchErr)
	}
}

// TryDispatch runs a single dispatch attempt synchronously, intended for the
// caller to invoke right after enqueue. On full success the row is marked
// completed so the poll worker has nothing to retry. Any failure (transport,
// partial fanout) records the failure with current attempts so the worker
// picks it up on its next tick.
func (w *Worker[P]) TryDispatch(ctx context.Context, id string, currentAttempts int, payload P) {
	if id == "" || w.Dispatcher == nil || w.Store == nil {
		return
	}
	failed, err := w.Dispatcher.Dispatch(ctx, payload)
	if err == nil && len(failed) == 0 {
		mErr := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			return w.Store.MarkCompleted(ctx, id)
		})
		if mErr != nil && w.Logger != nil {
			w.Logger.WithError(mErr).WithField("outbox_id", id).Warn("mark outbox completed failed")
		}
		return
	}
	w.recordFailure(ctx, id, currentAttempts, failed, err)
}

func (w *Worker[P]) recordFailure(ctx context.Context, id string, currentAttempts int, failedTargets []string, cause error) {
	backoff := ComputeBackoff(w.Config, currentAttempts)
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return w.Store.RecordFailure(ctx, id, currentAttempts, failedTargets, cause, backoff)
	})
	if err != nil {
		if w.Logger != nil {
			w.Logger.WithError(err).WithField("outbox_id", id).Warn("record outbox failure failed")
		}
		return
	}
	nextAttempts := currentAttempts + 1
	if w.Config.AlertAfterAttempts > 0 && nextAttempts >= w.Config.AlertAfterAttempts && w.Logger != nil {
		causeStr := ""
		if cause != nil {
			causeStr = cause.Error()
		}
		label := w.AlertLabel
		if label == "" {
			label = "outbox"
		}
		w.Logger.WithFields(logging.Fields{
			"outbox_id":      id,
			"attempts":       nextAttempts,
			"failed_targets": failedTargets,
			"backoff_ms":     backoff.Milliseconds(),
			"cause":          causeStr,
		}).Errorf("%s has been failing for many attempts; backend likely partitioned. Worker will keep retrying — investigate.", label)
	}
}

// ComputeBackoff doubles the base backoff per attempt, capping at MaxBackoff.
// A non-positive result (overflow when attempts is large enough that the shift
// wraps) also clamps to MaxBackoff. There is no terminal abandon path:
// callers keep retrying so a partitioned target catches up when it returns.
func ComputeBackoff(cfg Config, attempts int) time.Duration {
	if cfg.BaseBackoff <= 0 {
		return cfg.MaxBackoff
	}
	if attempts < 0 {
		attempts = 0
	}
	backoff := cfg.BaseBackoff << uint(attempts)
	if backoff > cfg.MaxBackoff || backoff <= 0 {
		backoff = cfg.MaxBackoff
	}
	return backoff
}
