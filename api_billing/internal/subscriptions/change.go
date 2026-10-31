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

// ChangePolicy controls whether a caller will change or preserve granted pricing.
type ChangePolicy int

const (
	// ChangePricing refuses every outstanding grant, including one awaiting expiry.
	ChangePricing ChangePolicy = iota
	// PreservePricing lets renewal and bootstrap observe a grant without repricing it.
	PreservePricing
)

// LockedSubscription holds billing state read after acquiring the subscription row lock.
type LockedSubscription struct {
	Current  purserdb.LockTenantSubscriptionForProviderChangeRow
	HasGrant bool
	// AccountClosed stays true after final billing completes.
	AccountClosed bool
}

// LockChange serializes pricing decisions with all grant and checkout writes.
// PreservePricing callers must retain the granted tier and model; only the grant
// lifecycle may replace that arrangement through a prepared billing boundary.
func LockChange(ctx context.Context, tx *sql.Tx, tenantID string, policy ChangePolicy) (LockedSubscription, error) {
	q := purserdb.New(tx)
	current, err := q.LockTenantSubscriptionForProviderChange(ctx, tenantID)
	if err != nil {
		return LockedSubscription{}, err
	}
	closed, err := q.IsTenantBillingClosed(ctx, tenantID)
	if err != nil {
		return LockedSubscription{}, fmt.Errorf("read locked account closure: %w", err)
	}
	_, err = q.GetSubscriptionOperatorGrant(ctx, tenantID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return LockedSubscription{}, fmt.Errorf("read locked subscription grant: %w", err)
	}
	state := LockedSubscription{Current: current, HasGrant: err == nil, AccountClosed: closed}
	if closed && policy == ChangePricing {
		return state, ErrTenantBillingClosed
	}
	if state.HasGrant && policy == ChangePricing {
		return state, ErrOutstandingGrant
	}
	if current.FreeFallbackProviderStopping && policy == ChangePricing {
		return state, ErrPendingFreeFallback
	}
	if current.PendingReason.String == "tier_upgrade" && policy == ChangePricing {
		return state, ErrPendingTierUpgrade
	}
	if policy == ChangePricing {
		if err := CheckPendingTierUpgrade(ctx, tx, tenantID); err != nil {
			return state, err
		}
	}
	return state, nil
}

// CheckPendingTierUpgrade protects collection after pricing has been applied and
// the subscription's scheduled-change marker has cleared. Call under its row lock.
func CheckPendingTierUpgrade(ctx context.Context, tx *sql.Tx, tenantID string) error {
	pending, err := purserdb.New(tx).HasPendingTierUpgrade(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("read pending tier upgrade: %w", err)
	}
	if pending {
		return ErrPendingTierUpgrade
	}
	return nil
}

// WithOpenAccount serializes non-tier purchases with permanent account closure.
// An existing grant keeps its pricing while the separate purchase is recorded.
func WithOpenAccount(ctx context.Context, db *sql.DB, tenantID string, write func(*sql.Tx) error) error {
	return database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
		locked, err := LockChange(ctx, tx, tenantID, PreservePricing)
		if err != nil {
			return err
		}
		if locked.AccountClosed {
			return ErrTenantBillingClosed
		}
		return write(tx)
	})
}

// ReserveCheckout writes the durable provider intent while excluding grant creation.
// The reservation exists before contacting a provider, including payment-method setup.
func ReserveCheckout(ctx context.Context, db *sql.DB, tenantID string, reserve func(*sql.Tx) (string, error)) (string, error) {
	var id string
	err := database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
		if _, err := LockChange(ctx, tx, tenantID, ChangePricing); err != nil {
			return err
		}
		var err error
		id, err = reserve(tx)
		if err != nil {
			return err
		}
		if err := checkPendingCheckout(ctx, tx, tenantID, id); err != nil {
			return err
		}
		return purserdb.New(tx).EnqueueCheckoutReservationExpiry(ctx, purserdb.EnqueueCheckoutReservationExpiryParams{TenantID: tenantID, IntentID: id})
	})
	return id, err
}

// CheckGrantCheckout refuses grants while a provider can still activate pricing.
// Callers hold the subscription lock before this read; ReserveCheckout takes the
// same lock before creating an intent, closing both orderings of the race.
func CheckGrantCheckout(ctx context.Context, tx *sql.Tx, tenantID string) error {
	return checkPendingCheckout(ctx, tx, tenantID, "")
}

// ErrPaidCancellationTerm protects the cancelled subscription's already-paid term.
var ErrPaidCancellationTerm = errors.New("the cancelled subscription's paid term must end before starting a new checkout")

// ErrPendingCheckout requires an unresolved purchase to reach a terminal outcome.
var ErrPendingCheckout = errors.New("an unresolved payment-provider checkout blocks another checkout or a billing grant")

func checkPendingCheckout(ctx context.Context, tx *sql.Tx, tenantID, excludeID string) error {
	row, err := purserdb.New(tx).GetPendingProviderCheckout(ctx, purserdb.GetPendingProviderCheckoutParams{TenantID: tenantID, ExcludeID: excludeID})
	if err != nil {
		return fmt.Errorf("read pending provider checkout: %w", err)
	}
	if !row.Pending {
		return nil
	}
	if row.Kind == "free_fallback" {
		return ErrPendingFreeFallback
	}
	if row.Kind == "tier_upgrade" {
		return ErrPendingTierUpgrade
	}
	return fmt.Errorf("%w; checkout deadline: %s; completed purchases remain reserved during settlement. Inspect admin billing recovery list or run admin billing recovery checkout", ErrPendingCheckout, row.Deadline.UTC().Format(time.RFC3339))
}

// ErrTenantBillingClosed prevents a permanent account closure from acquiring new pricing or purchases.
var ErrTenantBillingClosed = errors.New("tenant account is closed")

// ErrPendingTierUpgrade preserves pricing until an accepted paid upgrade is reconciled.
var ErrPendingTierUpgrade = errors.New("a paid tier upgrade is being reconciled; retry the same tier change to inspect its outcome")

// ErrPendingFreeFallback keeps pricing unchanged while provider cancellation is durable and in flight.
var ErrPendingFreeFallback = errors.New("the move to Free is reconciling its provider subscription; inspect Purser recovery logs and retry")

// IsPricingRefusal identifies an account or grant that requires a caller-visible precondition.
func IsPricingRefusal(err error) bool {
	return errors.Is(err, ErrPaidCancellationTerm) || errors.Is(err, ErrPendingFreeFallback) || errors.Is(err, ErrOutstandingGrant) || errors.Is(err, ErrTenantBillingClosed) || errors.Is(err, ErrPendingTierUpgrade)
}
