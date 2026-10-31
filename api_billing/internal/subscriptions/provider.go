package subscriptions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

// ErrOutstandingGrant requires an operator to end the arrangement before changing pricing.
var ErrOutstandingGrant = errors.New("end the outstanding operator billing grant before changing tier or billing model")

// ProviderOperation identifies the provider lifecycle action protected by the subscription lock.
type ProviderOperation int

const (
	// ProviderCheckout validates before an external checkout or mandate request.
	ProviderCheckout ProviderOperation = iota
	// StripePending binds an unsettled checkout without replacing established pricing.
	StripePending
	// StripeCheckout applies a paid subscription checkout.
	StripeCheckout
	// StripeRenewal synchronizes an established subscription without repricing a grant.
	StripeRenewal
	// MollieActivation installs a fixed-price Mollie subscription.
	MollieActivation
	// AdvanceActivation starts Purser invoicing after payment-method setup.
	AdvanceActivation
	// StripeStatus applies dunning or cancellation without replacing pricing.
	StripeStatus
)

// ProviderBoundary commits a prepared phase close under the provider/grant row lock.
type ProviderBoundary interface {
	CommitTx(context.Context, *sql.Tx) (bool, error)
}

// ProviderChange carries the arguments of one provider operation.
type ProviderChange struct {
	// MollieMandate commits payment-method setup with advance activation and its receipt.
	MollieMandate *purserdb.UpsertMollieMandateParams
	// ReceiptIntentID settles a bounded checkout atomically with its activation.
	ReceiptIntentID string
	// CheckoutSessionID binds an unsettled Stripe receipt under the subscription lock.
	CheckoutSessionID string
	TenantID          string
	Boundary          ProviderBoundary
	Operation         ProviderOperation
	Stripe            purserdb.ActivateTenantSubscriptionFromStripeParams
	Mollie            purserdb.ActivateMollieTenantSubscriptionParams
	Advance           purserdb.ActivateAdvanceBilledSubscriptionParams
	StripeStatus      purserdb.UpdateTenantStripeSubscriptionStatusParams
	// StripeCancellationReason is Stripe's cancellation_details.reason.
	StripeCancellationReason string
}

// ProviderResult reports whether a local subscription matched and its prior provider identity.
type ProviderResult struct {
	Rows                         int64
	SubscriptionID               string
	PreviousMollieSubscriptionID string
	// Status is the local status a StripeStatus change applied.
	Status string
	// Refusal is a committed recovery decision; transaction callers must commit it.
	Refusal error
}

// TransitionProvider serializes every provider tier/model change with grant writes.
// Checkout and activation refuse outstanding grants. Renewal of the same established
// Stripe subscription preserves the grant and changes only status and period dates.
// A provider callback for a missing local subscription is a no-op.
func TransitionProvider(ctx context.Context, exec purserdb.DBTX, change ProviderChange) (ProviderResult, error) {
	if tx, ok := exec.(*sql.Tx); ok {
		return transitionProviderWithRecoveryTx(ctx, tx, change)
	}
	db, ok := exec.(*sql.DB)
	if !ok {
		return ProviderResult{}, errors.New("provider transition requires a database or transaction")
	}
	var result ProviderResult
	err := database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
		var err error
		result, err = transitionProviderWithRecoveryTx(ctx, tx, change)
		return err
	})
	if err != nil {
		return result, err
	}
	return result, result.Refusal
}

func transitionProviderWithRecoveryTx(ctx context.Context, tx *sql.Tx, change ProviderChange) (ProviderResult, error) {
	result, err := transitionProviderTx(ctx, tx, change)
	if err == nil || change.ReceiptIntentID == "" || !IsPricingRefusal(err) {
		return result, err
	}
	locked, lockErr := LockChange(ctx, tx, change.TenantID, PreservePricing)
	if lockErr != nil {
		return result, lockErr
	}
	q := purserdb.New(tx)
	reservation, readErr := q.GetProviderCheckoutReservation(ctx, purserdb.GetProviderCheckoutReservationParams{TenantID: change.TenantID, IntentID: change.ReceiptIntentID})
	if readErr != nil {
		return result, readErr
	}
	established := reservation.ProviderSubscriptionID.String != "" &&
		((reservation.Provider == "stripe" && reservation.ProviderSubscriptionID.String == locked.Current.StripeSubscriptionID.String) ||
			(reservation.Provider == "mollie" && reservation.ProviderSubscriptionID.String == locked.Current.MollieSubscriptionID.String))
	if reservation.ActivatedAt.Valid || established {
		return result, err
	}
	subscriptionID := change.Stripe.SubscriptionID
	if reservation.Provider == "mollie" {
		subscriptionID = change.Mollie.SubscriptionID.String
	}
	if bindErr := q.BindProviderCheckoutReceipt(ctx, purserdb.BindProviderCheckoutReceiptParams{TenantID: change.TenantID, IntentID: reservation.ID, Provider: reservation.Provider, SubscriptionID: subscriptionID}); bindErr != nil {
		return result, bindErr
	}
	for _, id := range []string{reservation.ID, reservation.FundingIntentID} {
		if id == "" {
			continue
		}
		if latchErr := q.StartCheckoutCompensation(ctx, purserdb.StartCheckoutCompensationParams{TenantID: change.TenantID, IntentID: id}); latchErr != nil {
			return result, latchErr
		}
		if queueErr := q.RequestCheckoutRecovery(ctx, purserdb.RequestCheckoutRecoveryParams{TenantID: change.TenantID, IntentID: id, Reason: err.Error()}); queueErr != nil {
			return result, queueErr
		}
	}
	result.Refusal = errors.Join(ErrCheckoutCompensationQueued, ErrCheckoutExpired, err)
	return result, nil
}

func transitionProviderTx(ctx context.Context, tx *sql.Tx, change ProviderChange) (ProviderResult, error) {
	q := purserdb.New(tx)
	locked, err := LockChange(ctx, tx, change.TenantID, PreservePricing)
	if errors.Is(err, sql.ErrNoRows) {
		return ProviderResult{}, nil
	}
	if err != nil {
		return ProviderResult{}, fmt.Errorf("lock provider subscription: %w", err)
	}

	receipt, receiptErr := readReceipt(ctx, tx, change.TenantID, change.ReceiptIntentID)
	if receiptErr != nil {
		return ProviderResult{}, receiptErr
	}
	current := locked.Current
	if current.FreeFallbackProviderStopping && change.Operation != StripeStatus {
		return ProviderResult{}, ErrPendingFreeFallback
	}
	if change.Operation == ProviderCheckout && current.FreeFallbackProviderEndedAt.Valid && current.Status == "pending_cancellation" {
		return ProviderResult{}, ErrPaidCancellationTerm
	}
	if current.PendingReason.String == "tier_upgrade" {
		return ProviderResult{}, ErrPendingTierUpgrade
	}
	if upgradeErr := CheckPendingTierUpgrade(ctx, tx, change.TenantID); upgradeErr != nil {
		return ProviderResult{}, upgradeErr
	}
	if locked.AccountClosed {
		if change.Operation == StripeStatus || change.Operation == StripeRenewal {
			providerID, customerID := change.Stripe.SubscriptionID, change.Stripe.CustomerID
			if change.Operation == StripeStatus {
				providerID = change.StripeStatus.SubscriptionID
			}
			if providerID != "" {
				rows, queueErr := q.AppendClosedTenantStripeSubscription(ctx, purserdb.AppendClosedTenantStripeSubscriptionParams{TenantID: change.TenantID, CustomerID: customerID, SubscriptionID: providerID})
				if queueErr != nil {
					return ProviderResult{}, queueErr
				}
				if rows > 0 {
					if queueErr = q.RequeueTenantBillingClosure(ctx, change.TenantID); queueErr != nil {
						return ProviderResult{}, queueErr
					}
				}
			}
			return ProviderResult{}, nil
		}
		return ProviderResult{}, ErrTenantBillingClosed
	}

	result := ProviderResult{SubscriptionID: current.SubscriptionID, PreviousMollieSubscriptionID: current.MollieSubscriptionID.String}
	if current.FreeFallbackProviderEndedAt.Valid && current.StripeSubscriptionID.String != "" &&
		((change.Operation == StripeStatus && current.StripeSubscriptionID.String == change.StripeStatus.SubscriptionID) ||
			((change.Operation == StripeRenewal || change.Operation == StripeCheckout) && current.StripeSubscriptionID.String == change.Stripe.SubscriptionID)) {
		return result, nil
	}
	if (change.Operation == StripeRenewal || change.Operation == StripeCheckout && current.FreeFallbackProviderEndedAt.Valid) && current.StripeSubscriptionID.String != change.Stripe.SubscriptionID && change.ReceiptIntentID == "" {
		return result, nil
	}
	if receipt != nil && (change.Operation == StripeCheckout || change.Operation == StripeRenewal || change.Operation == MollieActivation) {
		matching := current.StripeSubscriptionID.String == change.Stripe.SubscriptionID && change.Stripe.SubscriptionID != ""
		if change.Operation == MollieActivation {
			matching = current.MollieSubscriptionID.String == change.Mollie.SubscriptionID.String && change.Mollie.SubscriptionID.String != ""
		}
		if receipt.Status == "succeeded" && !matching {
			return result, nil
		}
	}
	if change.Operation == StripeStatus || change.Operation == StripeRenewal {
		providerID, periodStart := change.Stripe.SubscriptionID, change.Stripe.PeriodStart
		if change.Operation == StripeStatus {
			providerID, periodStart = change.StripeStatus.SubscriptionID, change.StripeStatus.PeriodStart
		}
		if current.StripeSubscriptionID.String != "" && current.StripeSubscriptionID.String == providerID {
			boundary, readErr := q.GetStripeUpgradeBoundary(ctx, purserdb.GetStripeUpgradeBoundaryParams{TenantID: change.TenantID, SubscriptionID: providerID})
			if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
				return result, readErr
			}
			if readErr == nil {
				if !periodStart.Valid {
					// Dunning events may omit item periods. Their authenticated
					// event time still orders them against the paid boundary.
					eventAt, known := ProviderCompletion(ctx)
					if change.Operation != StripeStatus || !known || eventAt.Before(boundary.Boundary) {
						return result, nil
					}
				} else if periodStart.Time.Before(boundary.Boundary) {
					return result, nil
				}
				// Consecutive purchases can share Stripe's second-resolution anchor.
				// Only the tier bought at that anchor may replay its renewal.
				if change.Operation == StripeRenewal && periodStart.Time.Equal(boundary.Boundary) && change.Stripe.TierID != boundary.TierID {
					return result, nil
				}
			}
		}
	}
	if change.Operation == StripeStatus {
		params := change.StripeStatus
		params.TenantID = change.TenantID
		var fallback stripeFallback
		if params.Status == "cancelled" {
			fallback = planStripeFallback(ctx, current.Status, params.StripeStatus.String, change.StripeCancellationReason)
			if fallback.reason != "" {
				params.Status = fallback.status
			}
		}
		result.Status = params.Status
		result.Rows, err = q.UpdateTenantStripeSubscriptionStatus(ctx, params)
		if err == nil && result.Rows > 0 && fallback.reason != "" {
			_, err = q.StartSubscriptionFreeFallback(ctx, purserdb.StartSubscriptionFreeFallbackParams{FallbackAt: fallback.at, Reason: fallback.reason, TenantID: change.TenantID})
		}
		return result, err
	}
	if locked.HasGrant {
		if change.Operation != StripeRenewal || current.StripeSubscriptionID.String == "" ||
			current.StripeSubscriptionID.String != change.Stripe.SubscriptionID ||
			current.BillingModel != "postpaid" || current.PendingReason.String == "stripe_checkout" {
			return result, ErrOutstandingGrant
		}
		result.Rows, err = q.UpdateTenantStripeSubscriptionStatus(ctx, purserdb.UpdateTenantStripeSubscriptionStatusParams{
			TenantID: change.TenantID, Status: "active", SubscriptionID: change.Stripe.SubscriptionID,
			StripeStatus: sql.NullString{String: "active", Valid: true},
			PeriodStart:  change.Stripe.PeriodStart, PeriodEnd: change.Stripe.PeriodEnd,
		})
		return result, err
	}
	targetID, provider, incomingID := "", "", ""
	switch change.Operation {
	case StripeCheckout, StripePending:
		targetID, provider, incomingID = change.Stripe.TierID, "stripe", change.Stripe.SubscriptionID
	case StripeRenewal:
		if current.StripeSubscriptionID.String != change.Stripe.SubscriptionID || change.Stripe.TierID != "" && change.Stripe.TierID != current.TierID {
			targetID, provider, incomingID = change.Stripe.TierID, "stripe", change.Stripe.SubscriptionID
		}
	case MollieActivation:
		targetID, provider, incomingID = change.Mollie.TierID, "mollie", change.Mollie.SubscriptionID.String
	case ProviderCheckout, AdvanceActivation:
		targetID, provider = change.Advance.TierID, change.Advance.PaymentMethod
	}
	if provider != "" {
		if targetID == "" {
			targetID = current.TierID
		}
		if eligibilityErr := CheckSelfServiceActivation(ctx, tx, change.TenantID, targetID, provider, incomingID); eligibilityErr != nil {
			if IsPricingRefusal(eligibilityErr) {
				return result, errors.Join(ErrCheckoutExpired, eligibilityErr)
			}
			return result, eligibilityErr
		}
	}
	if change.Operation == AdvanceActivation && (current.StripeSubscriptionID.Valid || current.MollieSubscriptionID.Valid) {
		return result, errors.New("advance activation requires a subscription without a provider subscription")
	}
	if change.Boundary != nil {
		if change.Operation != AdvanceActivation {
			return result, errors.New("a provider phase boundary requires advance activation")
		}
		if _, boundaryErr := change.Boundary.CommitTx(ctx, tx); boundaryErr != nil {
			return result, boundaryErr
		}
	}
	switch change.Operation {
	case ProviderCheckout:
		result.Rows = 1

	case StripePending:
		err = q.StageTenantSubscriptionPendingStripe(ctx, purserdb.StageTenantSubscriptionPendingStripeParams{TenantID: change.TenantID, CustomerID: change.Stripe.CustomerID})
		if err == nil {
			err = q.LinkStripeIntentSubscription(ctx, purserdb.LinkStripeIntentSubscriptionParams{TenantID: change.TenantID, SessionID: change.CheckoutSessionID, SubscriptionID: change.Stripe.SubscriptionID})
		}
		if err == nil {
			result.Rows = 1
		}
	case StripeCheckout, StripeRenewal:
		params := change.Stripe
		params.TenantID = change.TenantID
		result.Rows, err = q.ActivateTenantSubscriptionFromStripe(ctx, params)
	case MollieActivation:
		params := change.Mollie
		params.TenantID = change.TenantID
		result.Rows, err = q.ActivateMollieTenantSubscription(ctx, params)
	case AdvanceActivation:
		if mandate := change.MollieMandate; mandate != nil {
			if mandateErr := q.UpsertMollieCustomer(ctx, purserdb.UpsertMollieCustomerParams{TenantID: change.TenantID, MollieCustomerID: mandate.MollieCustomerID}); mandateErr != nil {
				return result, mandateErr
			}
			if mandateErr := q.UpsertMollieMandate(ctx, *mandate); mandateErr != nil {
				return result, mandateErr
			}
		}
		params := change.Advance
		params.TenantID = change.TenantID
		result.Rows, err = q.ActivateAdvanceBilledSubscription(ctx, params)
	default:
		err = errors.New("unknown provider subscription operation")
	}
	if err == nil && result.Rows == 0 && change.Boundary != nil {
		return result, errors.New("provider activation did not apply; billing boundary rolled back")
	}
	if err == nil && result.Rows > 0 && change.ReceiptIntentID != "" && change.Operation != ProviderCheckout && change.Operation != StripePending {
		err = q.CompleteProviderCheckoutReservation(ctx, purserdb.CompleteProviderCheckoutReservationParams{TenantID: change.TenantID, IntentID: change.ReceiptIntentID})
	}
	return result, err
}

// ErrCheckoutCompensationQueued means the transaction queued recovery for an unactivated purchase.
// A caller must commit that transaction before acknowledging the provider event.
var ErrCheckoutCompensationQueued = errors.New("checkout refusal queued for compensation")

// ErrCheckoutExpired leaves compensation to the durable checkout recovery inbox.
var ErrCheckoutExpired = errors.New("payment-provider checkout reservation expired")
