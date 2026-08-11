package outbox

import (
	"context"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

type Config struct {
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
	BatchSize   int
	PollPeriod  time.Duration
	Lease       time.Duration
	// SettleTimeout, when > 0, bounds the ENTIRE settlement retry (MarkCompleted/RecordFailure incl. all
	// RetryPostgres attempts + backoff), not one attempt — so a lease-budgeted worker can guarantee settlement
	// finishes within the claim lease. Zero leaves settlement unbounded (the worker's context).
	SettleTimeout      time.Duration
	AlertAfterAttempts int
}

type Claim[P any] struct {
	ID       string
	Attempts int
	Payload  P
	// LeaseToken, when a store sets it and implements TokenFencedStore, fences settlement: the worker passes it back
	// to MarkCompletedToken/RecordFailureToken so a stale worker (its lease expired and a peer re-claimed the row
	// with a fresh token) cannot settle a row it no longer owns. Empty ⇒ the store is not token-fenced (unchanged).
	LeaseToken string
}

type Store[P any] interface {
	ClaimBatch(ctx context.Context, batchSize int, lease time.Duration) ([]Claim[P], error)
	MarkCompleted(ctx context.Context, id string) error
	RecordFailure(ctx context.Context, id string, currentAttempts int, failedTargets []string, cause error, backoff time.Duration) error
}

// TokenFencedStore is an OPTIONAL interface a Store may also implement to fence settlement on the per-claim lease
// token (see Claim.LeaseToken). When a Store implements it, the worker calls these token-taking variants instead of
// the plain ones; stores that don't implement it are completely unaffected. Non-generic so the worker can type-assert
// the Store value regardless of its payload type.
type TokenFencedStore interface {
	MarkCompletedToken(ctx context.Context, id, leaseToken string) error
	RecordFailureToken(ctx context.Context, id string, currentAttempts int, failedTargets []string, cause error, backoff time.Duration, leaseToken string) error
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

// settleCtx bounds the WHOLE settlement (all retry attempts) when Config.SettleTimeout > 0, so the timeout cannot
// reset per RetryPostgres attempt. Zero timeout returns the context unchanged with a no-op cancel.
func (w *Worker[P]) settleCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if w.Config.SettleTimeout > 0 {
		return context.WithTimeout(ctx, w.Config.SettleTimeout)
	}
	return ctx, func() {}
}

// markCompleted settles a completion, preferring the token-fenced variant when the Store implements TokenFencedStore
// and the claim carried a lease token — so a stale worker cannot complete a row a peer re-claimed.
func (w *Worker[P]) markCompleted(ctx context.Context, id, leaseToken string) error {
	if ts, ok := w.Store.(TokenFencedStore); ok && leaseToken != "" {
		return ts.MarkCompletedToken(ctx, id, leaseToken)
	}
	return w.Store.MarkCompleted(ctx, id)
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
			sctx, scancel := w.settleCtx(ctx)
			mErr := database.RetryPostgres(sctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
				return w.markCompleted(sctx, c.ID, c.LeaseToken)
			})
			scancel()
			if mErr != nil && w.Logger != nil {
				w.Logger.WithError(mErr).WithField("outbox_id", c.ID).Warn("mark outbox completed failed")
			}
			continue
		}
		w.recordFailure(ctx, c.ID, c.Attempts, failed, dispatchErr, c.LeaseToken)
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
		sctx, scancel := w.settleCtx(ctx)
		mErr := database.RetryPostgres(sctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			return w.Store.MarkCompleted(sctx, id)
		})
		scancel()
		if mErr != nil && w.Logger != nil {
			w.Logger.WithError(mErr).WithField("outbox_id", id).Warn("mark outbox completed failed")
		}
		return
	}
	// The inline path is un-leased (freshly enqueued, not claimed) so there is no lease token to fence.
	w.recordFailure(ctx, id, currentAttempts, failed, err, "")
}

func (w *Worker[P]) recordFailure(ctx context.Context, id string, currentAttempts int, failedTargets []string, cause error, leaseToken string) {
	backoff := ComputeBackoff(w.Config, currentAttempts)
	sctx, scancel := w.settleCtx(ctx)
	defer scancel()
	err := database.RetryPostgres(sctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		if ts, ok := w.Store.(TokenFencedStore); ok && leaseToken != "" {
			return ts.RecordFailureToken(sctx, id, currentAttempts, failedTargets, cause, backoff, leaseToken)
		}
		return w.Store.RecordFailure(sctx, id, currentAttempts, failedTargets, cause, backoff)
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
