package subscriptions

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
)

// ErrMollieFundingMismatch retains a payment while the caller selects its purchase.
var ErrMollieFundingMismatch = errors.New("unused payment does not match this setup; retry activation with the paid tier and mandate, or contact billing for help")

// ErrMollieFundingRequired distinguishes a new purchase from retained paid funding.
var ErrMollieFundingRequired = errors.New("no unused paid first payment is available; complete a new first payment before activation")

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
	if r.Provider == "mollie" && r.Purpose == "mollie_subscription_create" && !r.ActivatedAt.Valid {
		matches, err := purserdb.New(tx).MollieCheckoutFundingMatches(ctx, purserdb.MollieCheckoutFundingMatchesParams{
			TenantID: tenantID, IntentID: intentID,
		})
		if err != nil {
			return nil, err
		}
		if !matches {
			return nil, mollieFundingSelectionError(ctx, tx, tenantID)
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

// A missing match can mean another owned purchase is retained, or that the
// customer has no reusable funding. Only the latter should request a new payment.
func mollieFundingSelectionError(ctx context.Context, tx *sql.Tx, tenantID string) error {
	unused, err := purserdb.New(tx).HasUnusedPaidMollieFunding(ctx, tenantID)
	if err != nil {
		return err
	}
	if unused {
		return ErrMollieFundingMismatch
	}
	return ErrMollieFundingRequired
}
