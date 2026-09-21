package delivery

import (
	"context"
	"errors"
	"sync"
	"time"

	"frameworks/api_webhooks/internal/ledger"
	"frameworks/api_webhooks/internal/metrics"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/google/uuid"
)

const (
	// defaultConcurrency is how many deliveries one replica sends at once.
	defaultConcurrency = 32
	// defaultPollInterval is how often an idle worker looks for due
	// deliveries.
	defaultPollInterval = time.Second
	// leaseMargin is the lease time a send must leave unused, covering clock
	// skew between replicas and the database and the settlement itself.
	leaseMargin = 5 * time.Second
	// settleTimeout bounds one settlement, which runs after a shutdown began.
	settleTimeout = 10 * time.Second
)

// Worker claims due deliveries and sends them.
type Worker struct {
	Store        *ledger.Store
	Sender       *Sender
	Metrics      *metrics.Metrics
	Logger       logging.Logger
	Concurrency  int
	PollInterval time.Duration
	// Jitter returns a number in [0, 1) that spreads retry times; nil uses
	// math/rand.
	Jitter func() float64
	// Now is the local clock used to keep sends inside their lease.
	Now func() time.Time

	wg sync.WaitGroup
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// Run claims and sends deliveries until ctx ends, then waits for the sends in
// flight to settle. A send in flight is not cancelled by ctx: it is bounded by
// AttemptTimeout, and cancelling it would record a failure the endpoint did
// not cause.
func (w *Worker) Run(ctx context.Context) {
	concurrency := w.Concurrency
	if concurrency <= 0 {
		concurrency = defaultConcurrency
	}
	poll := w.PollInterval
	if poll <= 0 {
		poll = defaultPollInterval
	}
	slots := make(chan struct{}, concurrency)
	released := make(chan struct{}, concurrency)
	defer w.wg.Wait()
	for {
		if ctx.Err() != nil {
			return
		}
		free := concurrency - len(slots)
		claimed := 0
		if free > 0 {
			claimedAt := w.now()
			var claims []ledger.Claim
			err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
				var claimErr error
				claims, claimErr = w.Store.Claim(ctx, free)
				return claimErr
			})
			if err != nil && ctx.Err() == nil {
				w.Metrics.Internal("claim")
				if w.Logger != nil {
					w.Logger.WithError(err).Warn("Claiming webhook deliveries failed")
				}
			}
			claimed = len(claims)
			for _, c := range claims {
				slots <- struct{}{}
				w.wg.Add(1)
				go func(c ledger.Claim) {
					defer func() {
						<-slots
						w.wg.Done()
						select {
						case released <- struct{}{}:
						default:
						}
					}()
					w.Process(c, claimedAt.Add(ledger.Lease))
				}(c)
			}
		}
		if claimed > 0 && claimed == free {
			// Every free slot was filled; look again as soon as one frees.
			select {
			case <-ctx.Done():
				return
			case <-released:
			case <-time.After(poll):
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}

// Process sends one claimed delivery and settles it. leaseEnd is the local
// time by which the lease ends; a send that could not finish AttemptTimeout
// plus a margin before it is not started, and the delivery is left to be
// reclaimed, so a reclaimed lease never has two senders. A signing-key or
// render failure is settled by settleInternal.
func (w *Worker) Process(c ledger.Claim, leaseEnd time.Time) {
	sendBy := leaseEnd.Add(-AttemptTimeout - leaseMargin)
	if w.now().After(sendBy) {
		w.Metrics.Internal("lease_expired_before_send")
		return
	}
	lookupCtx, cancelLookup := context.WithDeadline(context.Background(), sendBy)
	keys, err := w.Store.SigningKeys(lookupCtx, c.TenantID, c.EndpointID)
	cancelLookup()
	if err != nil {
		w.settleInternal("signing_key", c, err, errors.Is(err, ledger.ErrSigningKeyUnusable))
		return
	}
	payload, err := Render(c.Event, c.APIVersion)
	if err != nil {
		w.settleInternal("render", c, err, !RenderFailureRetryable(err))
		return
	}
	if w.now().After(sendBy) {
		w.Metrics.Internal("lease_expired_before_send")
		return
	}
	sendCtx, cancelSend := context.WithTimeout(context.Background(), AttemptTimeout)
	out := w.Sender.Send(sendCtx, c.EndpointURL, c.Event.ID, payload, keys)
	cancelSend()
	w.Metrics.Attempt(ledger.KindEvent, out.Success, out.ErrorClass, out.Latency.Seconds())
	w.settle(c, out)
}

func (w *Worker) settle(c ledger.Claim, out ledger.Outcome) {
	ctx, cancel := context.WithTimeout(context.Background(), settleTimeout)
	defer cancel()
	var result ledger.SettleResult
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var settleErr error
		result, settleErr = w.Store.Settle(ctx, c, out, w.Jitter)
		return settleErr
	})
	if err != nil {
		w.internal("settle", c, err)
		return
	}
	if !result.Settled {
		w.Metrics.Internal("settle_fenced")
		return
	}
	if result.Status != ledger.DeliveryPending {
		w.Metrics.Finished(result.Status)
	}
	if result.AutoDisabled {
		w.Metrics.Disabled()
		if w.Logger != nil {
			w.Logger.WithFields(logging.Fields{
				"tenant_id":          c.TenantID,
				"endpoint_id":        c.EndpointID,
				"skipped_deliveries": result.SkippedDeliveries,
			}).Warn("Webhook endpoint disabled after sustained failure")
		}
	}
}

// settleInternal settles a delivery Bosun could not send because of err at
// stage: a permanent cause fails it now, a transient one retries it after a
// backoff (ledger.Store.SettleInternal). Either way the lease is released and
// the endpoint's failure streak is untouched. When the settlement itself
// fails, the delivery is reclaimed after its lease expires.
func (w *Worker) settleInternal(stage string, c ledger.Claim, cause error, permanent bool) {
	w.Metrics.Internal(stage)
	if w.Logger != nil {
		w.Logger.WithError(cause).WithFields(logging.Fields{
			"stage":       stage,
			"permanent":   permanent,
			"tenant_id":   c.TenantID,
			"delivery_id": c.DeliveryID,
		}).Error("Webhook delivery not sent because of a Bosun-side failure")
	}
	ctx, cancel := context.WithTimeout(context.Background(), settleTimeout)
	defer cancel()
	var result ledger.SettleResult
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var settleErr error
		result, settleErr = w.Store.SettleInternal(ctx, c, ClassInternal, permanent, w.Jitter)
		return settleErr
	})
	if err != nil {
		w.internal("settle", c, err)
		return
	}
	if !result.Settled {
		w.Metrics.Internal("settle_fenced")
		return
	}
	if result.Status != ledger.DeliveryPending {
		w.Metrics.Finished(result.Status)
	}
}

func (w *Worker) internal(stage string, c ledger.Claim, err error) {
	w.Metrics.Internal(stage)
	if w.Logger != nil {
		w.Logger.WithError(err).WithFields(logging.Fields{
			"stage":       stage,
			"tenant_id":   c.TenantID,
			"delivery_id": c.DeliveryID,
		}).Error("Webhook delivery left unsent by a Bosun-side failure; it is retried when its lease expires")
	}
}

// SendTest sends a signed webhook.test event to the endpoint, waits for the
// attempt, and records it as a finished test delivery.
func (w *Worker) SendTest(ctx context.Context, ep ledger.Endpoint) (ledger.Delivery, ledger.Attempt, error) {
	keys, err := w.Store.SigningKeys(ctx, ep.TenantID, ep.ID)
	if err != nil {
		return ledger.Delivery{}, ledger.Attempt{}, err
	}
	deliveryID := uuid.Must(uuid.NewV7()).String()
	payload, err := RenderTest(deliveryID, ep.ID, ep.APIVersion, w.now())
	if err != nil {
		return ledger.Delivery{}, ledger.Attempt{}, err
	}
	sendCtx, cancel := context.WithTimeout(ctx, AttemptTimeout)
	out := w.Sender.Send(sendCtx, ep.URL, deliveryID, payload, keys)
	cancel()
	w.Metrics.Attempt(ledger.KindTest, out.Success, out.ErrorClass, out.Latency.Seconds())
	settleCtx, cancelSettle := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancelSettle()
	return w.Store.RecordTestDelivery(settleCtx, ep.TenantID, ep.ID, deliveryID, out)
}
