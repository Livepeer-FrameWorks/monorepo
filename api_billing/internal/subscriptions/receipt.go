package subscriptions

import (
	"context"
	"database/sql"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
)

type completionTimeKey struct{}

// WithProviderCompletion carries the provider's authenticated payment/completion time.
func WithProviderCompletion(ctx context.Context, at time.Time) context.Context {
	return context.WithValue(ctx, completionTimeKey{}, at)
}

// CheckReceipt validates a checkout while the caller holds the subscription lock.
// A provider completion before the deadline remains valid through delivery retries.
func CheckReceipt(ctx context.Context, tx *sql.Tx, tenantID, intentID string) error {
	_, err := readReceipt(ctx, tx, tenantID, intentID)
	return err
}

func readReceipt(ctx context.Context, tx *sql.Tx, tenantID, intentID string) (*purserdb.GetProviderCheckoutReservationRow, error) {
	if intentID == "" {
		return nil, nil
	}
	r, err := purserdb.New(tx).GetProviderCheckoutReservation(ctx, purserdb.GetProviderCheckoutReservationParams{TenantID: tenantID, IntentID: intentID})
	if err != nil {
		return nil, err
	}
	if r.FundingIntentID != "" {
		funding, err := purserdb.New(tx).GetProviderCheckoutReservation(ctx, purserdb.GetProviderCheckoutReservationParams{TenantID: tenantID, IntentID: r.FundingIntentID})
		if err != nil {
			return nil, err
		}
		if funding.Status != "succeeded" || funding.RecoveryStartedAt.Valid || funding.ActivatedAt.Valid && !r.ActivatedAt.Valid {
			return nil, ErrCheckoutExpired
		}
	}
	if r.Status == "succeeded" && !r.RecoveryStartedAt.Valid {
		return &r, nil
	}
	if r.RecoveryStartedAt.Valid || r.Status == "expired" || r.Status == "cancelled" || r.Status == "terminal_failed" {
		return nil, ErrCheckoutExpired
	}
	if r.CheckoutCompletedAt.Valid {
		return &r, nil
	}
	at, ok := ctx.Value(completionTimeKey{}).(time.Time)
	// Synchronous provider creation completes in the request; callbacks supply their provider timestamp.
	if !ok {
		at = time.Now()
	}
	if r.ExpiresAt.Valid && (at.IsZero() || at.After(r.ExpiresAt.Time)) {
		return nil, ErrCheckoutExpired
	}
	return &r, nil
}

// ProviderCompletion returns a callback's authoritative completion time, if present.
func ProviderCompletion(ctx context.Context) (time.Time, bool) {
	at, ok := ctx.Value(completionTimeKey{}).(time.Time)
	return at, ok
}
