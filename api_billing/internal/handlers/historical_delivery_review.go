package handlers

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingmollie "frameworks/api_billing/internal/mollie"
	"frameworks/api_billing/internal/subscriptions"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

// reviewHistoricalMollieDelivery records delivery evidence without authorizing
// compensation. The automatic historical pass never cancels or refunds a purchase.
func (s *Service) reviewHistoricalMollieDelivery(ctx context.Context, review checkoutReservationExpiry) (resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	release, acquired, err := database.TrySessionLock(ctx, s.db, database.NamedLock("checkout-recovery:"+review.TenantID+":"+review.IntentID))
	if err != nil {
		return err
	}
	if !acquired {
		return ErrAwaitingProviderSettlement
	}
	defer func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer releaseCancel()
		resultErr = errors.Join(resultErr, release(releaseCtx))
	}()
	var receipt purserdb.GetProviderCheckoutReservationRow
	var snapshot subscriptions.LockedSubscription
	eligible := false
	err = database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		eligible = false
		var readErr error
		snapshot, readErr = subscriptions.LockChange(ctx, tx, review.TenantID, subscriptions.PreservePricing)
		if readErr != nil {
			return readErr
		}
		receipt, readErr = purserdb.New(tx).GetProviderCheckoutReservation(ctx, purserdb.GetProviderCheckoutReservationParams{TenantID: review.TenantID, IntentID: review.IntentID})
		if errors.Is(readErr, sql.ErrNoRows) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
		eligible = receipt.Provider == "mollie" && receipt.Purpose == "mollie_first_payment" &&
			receipt.Status == "succeeded" && receipt.AmountCents > 0 && receipt.ProviderPaymentID.Valid &&
			!receipt.ProviderMandateID.Valid && !receipt.ActivatedAt.Valid && !receipt.RecoveryStartedAt.Valid
		return nil
	})
	if err != nil || !eligible {
		return err
	}
	if s.mollieClient == nil {
		return errors.New("historical delivery review requires the configured Mollie client")
	}
	q := purserdb.New(s.db)
	var subscriptionID string
	var evidence purserdb.GetHistoricalMollieFundingActivationRow
	proof := func(id string) (bool, error) {
		found, readErr := q.GetHistoricalMollieFundingActivation(ctx, purserdb.GetHistoricalMollieFundingActivationParams{TenantID: review.TenantID, IntentID: review.IntentID, SubscriptionID: id})
		if errors.Is(readErr, sql.ErrNoRows) {
			return false, nil
		}
		if readErr != nil {
			return false, readErr
		}
		subscriptionID, evidence = id, found
		return true, nil
	}
	// Inspection is read-only. Orphan provider subscriptions deliberately remain
	// untouched; finding an unactivated purchase does not authorize its refund.
	delivered, _, inspectErr := s.mollieClient.InspectFirstPaymentFunding(ctx, receipt.ProviderCustomerID.String, receipt.ProviderPaymentID.String, snapshot.Current.MollieSubscriptionID.String, proof)
	if inspectErr != nil && !errors.Is(inspectErr, billingmollie.ErrHistoricalFundingAmbiguous) {
		return inspectErr
	}
	return database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		current, lockErr := subscriptions.LockChange(ctx, tx, review.TenantID, subscriptions.PreservePricing)
		if lockErr != nil {
			return lockErr
		}
		tq := purserdb.New(tx)
		stored, readErr := tq.GetProviderCheckoutReservation(ctx, purserdb.GetProviderCheckoutReservationParams{TenantID: review.TenantID, IntentID: review.IntentID})
		if readErr != nil {
			return readErr
		}
		if !reflect.DeepEqual(snapshot, current) || !reflect.DeepEqual(receipt, stored) {
			return ErrAwaitingProviderSettlement
		}
		if !delivered {
			return tq.MarkHistoricalMollieDeliveryAmbiguous(ctx, purserdb.MarkHistoricalMollieDeliveryAmbiguousParams{TenantID: review.TenantID, IntentID: review.IntentID})
		}
		found, readErr := tq.GetHistoricalMollieFundingActivation(ctx, purserdb.GetHistoricalMollieFundingActivationParams{TenantID: review.TenantID, IntentID: review.IntentID, SubscriptionID: subscriptionID})
		if readErr != nil {
			return readErr
		}
		if !reflect.DeepEqual(found, evidence) {
			return ErrAwaitingProviderSettlement
		}
		if found.NativeID != "" {
			if err := tq.BackfillHistoricalMollieCheckoutFunding(ctx, purserdb.BackfillHistoricalMollieCheckoutFundingParams{TenantID: review.TenantID, IntentID: review.IntentID, NativeID: found.NativeID, ActivatedAt: found.ActivatedAt}); err != nil {
				return err
			}
		}
		return tq.CompleteProviderCheckoutReservation(ctx, purserdb.CompleteProviderCheckoutReservationParams{TenantID: review.TenantID, IntentID: review.IntentID})
	})
}
