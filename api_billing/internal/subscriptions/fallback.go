package subscriptions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

// PaymentFailureGrace is how long a tenant keeps its paid tier after its
// payment provider gave up, before moving to Free.
const PaymentFailureGrace = 14 * 24 * time.Hour

const (
	// FallbackPaymentFailed moves the tenant after PaymentFailureGrace.
	FallbackPaymentFailed = "payment_failed"
	// FallbackCancelled moves the tenant when its paid period ends.
	FallbackCancelled = "cancelled"
)

// stripeFallback is the local outcome of a Stripe subscription ending. An empty
// reason keeps the row cancelled: nothing was ever activated to fall back from.
type stripeFallback struct {
	status string
	reason string
	at     time.Time
}

// planStripeFallback keeps an ended subscription out of the "cancelled" status,
// which billing and admission queries treat as no subscription at all.
func planStripeFallback(ctx context.Context, currentStatus, stripeStatus, cancellationReason string) stripeFallback {
	if stripeStatus == "incomplete_expired" || currentStatus == "pending" {
		return stripeFallback{}
	}
	at, known := ProviderCompletion(ctx)
	if !known {
		at = time.Now()
	}
	at = at.UTC()
	if stripeStatus == "canceled" && (currentStatus == "pending_cancellation" || cancellationReason == "cancellation_requested") {
		return stripeFallback{status: "pending_cancellation", reason: FallbackCancelled, at: at}
	}
	return stripeFallback{status: "past_due", reason: FallbackPaymentFailed, at: at.Add(PaymentFailureGrace)}
}

// StartPaymentFailureGrace starts the grace period for a provider that reports
// no subscription end of its own. A grant holds the move while the provider
// obligation remains recorded. It reports whether the tenant is in grace.
func StartPaymentFailureGrace(ctx context.Context, db *sql.DB, tenantID string, now time.Time) (bool, error) {
	return startPaymentFailureGrace(ctx, db, tenantID, "", now)
}

// StartMolliePaymentFailureGrace ignores delayed failures from a replaced subscription.
func StartMolliePaymentFailureGrace(ctx context.Context, db *sql.DB, tenantID, subscriptionID string, now time.Time) (bool, error) {
	if subscriptionID == "" {
		return false, errors.New("mollie subscription identity is required for payment failure")
	}
	return startPaymentFailureGrace(ctx, db, tenantID, subscriptionID, now)
}

func startPaymentFailureGrace(ctx context.Context, db *sql.DB, tenantID, mollieSubscriptionID string, now time.Time) (bool, error) {
	started := false
	err := database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
		started = false
		locked, lockErr := LockChange(ctx, tx, tenantID, PreservePricing)
		if errors.Is(lockErr, sql.ErrNoRows) {
			return nil
		}
		if lockErr != nil {
			return fmt.Errorf("lock subscription for payment failure: %w", lockErr)
		}
		if locked.AccountClosed || locked.Current.FreeFallbackProviderEndedAt.Valid ||
			(mollieSubscriptionID != "" && locked.Current.MollieSubscriptionID.String != mollieSubscriptionID) {
			return nil
		}
		q := purserdb.New(tx)
		rows, updateErr := q.MarkSubscriptionPaymentFailed(ctx, tenantID)
		if updateErr != nil || rows == 0 {
			return updateErr
		}
		rows, updateErr = q.StartSubscriptionFreeFallback(ctx, purserdb.StartSubscriptionFreeFallbackParams{
			FallbackAt: now.UTC().Add(PaymentFailureGrace), Reason: FallbackPaymentFailed, TenantID: tenantID,
		})
		started = rows > 0
		return updateErr
	})
	return started, err
}
