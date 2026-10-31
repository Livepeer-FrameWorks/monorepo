package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"frameworks/api_billing/internal/billingevents"
	"frameworks/api_billing/internal/database/purserdb"
	billingmollie "frameworks/api_billing/internal/mollie"
	"frameworks/api_billing/internal/subscriptions"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	mollieapi "github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/shopspring/decimal"
	stripeapi "github.com/stripe/stripe-go/v85"
)

type checkoutReservationExpiry struct {
	IntentID string `json:"checkout_reservation_id"`
	Force    bool   `json:"force,omitempty"`
	TenantID string `json:"tenant_id"`
}

// expireCheckoutReservation reconciles ambiguous calls independently of grant admission.
// Completed purchases reconcile into their subscription; abandoned or failed
// attempts are closed at the provider before their reservation is released.
func (s *Service) expireCheckoutReservation(ctx context.Context, expiry checkoutReservationExpiry) (resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	release, acquired, err := database.TrySessionLock(ctx, s.db, database.NamedLock("checkout-recovery:"+expiry.TenantID+":"+expiry.IntentID))
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
	return s.expireClaimedCheckoutReservation(ctx, expiry)
}

// The intent lock spans provider I/O. Subscription row locks protect only the
// durable decision before and after inspection; provider calls never run in a
// retryable subscription transaction.
func (s *Service) expireClaimedCheckoutReservation(ctx context.Context, expiry checkoutReservationExpiry) error {
	q := purserdb.New(s.db)
	var r purserdb.GetProviderCheckoutReservationRow
	var snapshot subscriptions.LockedSubscription
	complete, preCall, delivered, historical, inspect := false, false, false, false, false
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		complete, preCall, delivered, historical, inspect = false, false, false, false, false
		var err error
		snapshot, err = subscriptions.LockChange(ctx, tx, expiry.TenantID, subscriptions.PreservePricing)
		if err != nil {
			return err
		}
		tq := purserdb.New(tx)
		r, err = tq.GetProviderCheckoutReservation(ctx, purserdb.GetProviderCheckoutReservationParams{TenantID: expiry.TenantID, IntentID: expiry.IntentID})
		if errors.Is(err, sql.ErrNoRows) {
			complete = true
			return nil
		}
		if err != nil {
			return err
		}
		if r.CompensationCurrent {
			complete = true
			return nil
		}
		established := r.ProviderSubscriptionID.String != "" &&
			((r.Provider == "stripe" && snapshot.Current.StripeSubscriptionID.String == r.ProviderSubscriptionID.String) ||
				(r.Provider == "mollie" && snapshot.Current.MollieSubscriptionID.String == r.ProviderSubscriptionID.String))
		if established {
			complete = true
			return tq.CompleteProviderCheckoutReservation(ctx, purserdb.CompleteProviderCheckoutReservationParams{TenantID: r.TenantID, IntentID: r.ID})
		}
		delivered = r.Provider == "stripe" && r.SucceededAt.Valid
		if r.Provider == "mollie" && r.Purpose == "mollie_first_payment" && !r.ActivatedAt.Valid &&
			(r.Status == "succeeded" || r.SucceededAt.Valid) && (r.RecoveryStartedAt.Valid || expiry.Force) {
			pending, err := tq.HasPendingMollieFundingCheckout(ctx, purserdb.HasPendingMollieFundingCheckoutParams{TenantID: r.TenantID, FundingID: r.ID})
			if err != nil {
				return err
			}
			if pending {
				return fmt.Errorf("%w: reconcile the linked native subscription checkout before its initial payment", ErrAwaitingProviderSettlement)
			}
			obligated, err := tq.HasMollieCheckoutRefundObligation(ctx, purserdb.HasMollieCheckoutRefundObligationParams{TenantID: r.TenantID, PaymentID: r.ProviderPaymentID.String})
			if err != nil {
				return err
			}
			historical = !r.ProviderMandateID.Valid && !obligated
			// Claim funding before inspection so native activation cannot consume it.
			if latchErr := tq.StartCheckoutCompensation(ctx, purserdb.StartCheckoutCompensationParams{TenantID: r.TenantID, IntentID: r.ID}); latchErr != nil {
				return latchErr
			}
			r, err = tq.GetProviderCheckoutReservation(ctx, purserdb.GetProviderCheckoutReservationParams{TenantID: r.TenantID, IntentID: r.ID})
			return err
		}
		if r.Status == "succeeded" && (r.Purpose != "mollie_first_payment" || r.ActivatedAt.Valid || (!r.RecoveryStartedAt.Valid && !expiry.Force)) {
			complete = true
			return nil
		}
		if !delivered {
			if !r.ExpiresAt.Valid {
				return errors.New("checkout reservation has no deadline")
			}
			if !expiry.Force && time.Now().Before(r.ExpiresAt.Time) {
				return errors.New("checkout reservation deadline has not elapsed")
			}
			preCall = r.SubmissionState == "prepared" && len(r.ProviderRequest) == 0 && !hasCheckoutProviderReference(r)
			inspect = !preCall && !r.RecoveryStartedAt.Valid && r.Status != "expired" && r.Status != "cancelled" && r.Status != "terminal_failed"
		}
		return nil
	})
	if err != nil || complete {
		return err
	}
	stored := r
	if r.Provider == "mollie" && r.ProviderSubscriptionID.String != "" && r.ProviderCustomerID.String == "" {
		customerID, customerErr := q.GetMollieCustomerID(ctx, r.TenantID)
		if customerErr != nil {
			return fmt.Errorf("read customer for bound Mollie subscription: %w", customerErr)
		}
		r.ProviderCustomerID = sql.NullString{String: customerID, Valid: true}
	}
	var reconcile func() error
	var waiting error
	var completedAt time.Time
	incomingSubscriptionID := r.ProviderSubscriptionID.String
	var historySubscription string
	var historyProof purserdb.GetHistoricalMollieFundingActivationRow
	var historyOrphans []string
	historicalActivated := false
	if historical {
		if s.mollieClient == nil {
			return errors.New("mollie activation history requires its configured provider client")
		}
		activationProof := func(subscriptionID string) (bool, error) {
			proof, proofErr := q.GetHistoricalMollieFundingActivation(ctx, purserdb.GetHistoricalMollieFundingActivationParams{TenantID: r.TenantID, IntentID: r.ID, SubscriptionID: subscriptionID})
			if errors.Is(proofErr, sql.ErrNoRows) {
				return false, nil
			}
			if proofErr != nil {
				return false, proofErr
			}
			historySubscription, historyProof = subscriptionID, proof
			return true, nil
		}
		historicalActivated, historyOrphans, waiting = s.mollieClient.InspectFirstPaymentFunding(
			ctx, r.ProviderCustomerID.String, r.ProviderPaymentID.String,
			snapshot.Current.MollieSubscriptionID.String, activationProof,
		)
		if waiting != nil && !errors.Is(waiting, billingmollie.ErrHistoricalFundingAmbiguous) {
			return waiting
		}
	} else if inspect {
		reconcile, waiting = s.inspectCheckoutForRecovery(ctx, s.db, r, snapshot, &completedAt, &incomingSubscriptionID)
		if waiting != nil && !errors.Is(waiting, ErrAwaitingProviderSettlement) && !errors.Is(waiting, billingmollie.ErrPaymentSettling) {
			return waiting
		}
	}
	inspectionReconcile, inspectionWaiting := reconcile, waiting
	err = database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		complete = false
		reconcile, waiting = inspectionReconcile, inspectionWaiting
		locked, lockErr := subscriptions.LockChange(ctx, tx, r.TenantID, subscriptions.PreservePricing)
		if lockErr != nil {
			return lockErr
		}
		tq := purserdb.New(tx)
		current, readErr := tq.GetProviderCheckoutReservation(ctx, purserdb.GetProviderCheckoutReservationParams{TenantID: r.TenantID, IntentID: r.ID})
		if readErr != nil {
			return readErr
		}
		if !reflect.DeepEqual(locked, snapshot) || !reflect.DeepEqual(current, stored) {
			return ErrAwaitingProviderSettlement
		}
		if historicalActivated {
			proof, proofErr := tq.GetHistoricalMollieFundingActivation(ctx, purserdb.GetHistoricalMollieFundingActivationParams{TenantID: r.TenantID, IntentID: r.ID, SubscriptionID: historySubscription})
			if proofErr != nil {
				return proofErr
			}
			if !reflect.DeepEqual(proof, historyProof) {
				return ErrAwaitingProviderSettlement
			}
			if proof.NativeID != "" {
				if backfillErr := tq.BackfillHistoricalMollieCheckoutFunding(ctx, purserdb.BackfillHistoricalMollieCheckoutFundingParams{
					TenantID: r.TenantID, IntentID: r.ID, NativeID: proof.NativeID, ActivatedAt: proof.ActivatedAt,
				}); backfillErr != nil {
					return backfillErr
				}
			}
			complete = true
			return tq.CompleteProviderCheckoutReservation(ctx, purserdb.CompleteProviderCheckoutReservationParams{TenantID: r.TenantID, IntentID: r.ID})
		}
		if errors.Is(waiting, billingmollie.ErrHistoricalFundingAmbiguous) {
			return tq.MarkHistoricalMollieDeliveryAmbiguous(ctx, purserdb.MarkHistoricalMollieDeliveryAmbiguousParams{TenantID: r.TenantID, IntentID: r.ID})
		}
		if inspect && reconcile != nil {
			tierID, tierErr := tq.GetSelfServiceCheckoutTier(ctx, purserdb.GetSelfServiceCheckoutTierParams{TenantID: r.TenantID, IntentID: r.ID})
			if tierErr != nil && !errors.Is(tierErr, sql.ErrNoRows) {
				return tierErr
			}
			if tierID != "" {
				eligibilityErr := subscriptions.CheckSelfServiceActivation(ctx, tx, r.TenantID, tierID, r.Provider, incomingSubscriptionID)
				if eligibilityErr != nil && !subscriptions.IsPricingRefusal(eligibilityErr) {
					return eligibilityErr
				}
				if eligibilityErr != nil {
					reconcile = nil
					waiting = nil
				}
			}
		}
		if !completedAt.IsZero() {
			if completionErr := tq.MarkCheckoutCompleted(ctx, purserdb.MarkCheckoutCompletedParams{TenantID: r.TenantID, IntentID: r.ID, CompletedAt: completedAt}); completionErr != nil {
				return completionErr
			}
		}
		if waiting == nil && reconcile == nil {
			return tq.StartCheckoutCompensation(ctx, purserdb.StartCheckoutCompensationParams{TenantID: r.TenantID, IntentID: r.ID})
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Orphan renewal cancellation follows the durable delivery decision and runs
	// outside retryable transactions. A retry can safely repeat provider deletion.
	for _, id := range historyOrphans {
		if cancelErr := s.mollieClient.CancelEstablishedSubscription(ctx, r.ProviderCustomerID.String, id); cancelErr != nil {
			return cancelErr
		}
	}
	if complete {
		return nil
	}
	if waiting != nil {
		return waiting
	}
	if reconcile != nil {
		return reconcile()
	}
	var providerResult billing.CheckoutRecoveryResult
	if !preCall && hasCheckoutProviderReference(r) {
		switch {
		case delivered:
			err = s.cancelDeliveredCheckout(ctx, r)
			providerResult.Cancelled = err == nil
		case r.Provider == "stripe":
			if s.stripeClient == nil {
				return errors.New("stripe checkout recovery is not configured")
			}
			providerResult, err = s.stripeClient.RecoverReservedCheckout(ctx, r.ProviderCustomerID.String, r.ID, r.ProviderSessionID.String)
		case r.Provider == "mollie":
			if s.mollieClient == nil {
				return errors.New("mollie checkout recovery is not configured")
			}
			providerResult, err = s.mollieClient.RecoverReservedCheckout(ctx, r.ProviderCustomerID.String, r.ID, r.ProviderPaymentID.String, r.ProviderSubscriptionID.String)
		default:
			return fmt.Errorf("unsupported checkout provider %s", r.Provider)
		}
		if len(providerResult.RefundSettlements) > 0 {
			queueErr := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
				if _, lockErr := subscriptions.LockChange(ctx, tx, r.TenantID, subscriptions.PreservePricing); lockErr != nil {
					return lockErr
				}
				return enqueueRefundSettlements(ctx, purserdb.New(tx), r.TenantID, providerResult.RefundSettlements)
			})
			if queueErr != nil {
				return errors.Join(err, queueErr)
			}
		}
		if err != nil {
			return fmt.Errorf("reconcile expired checkout %s: %w", r.ID, err)
		}
	}
	references, err := json.Marshal(map[string]any{"session_id": nullCheckoutReference(r.ProviderSessionID), "payment_id": nullCheckoutReference(r.ProviderPaymentID), "subscription_id": nullCheckoutReference(r.ProviderSubscriptionID)})
	if err != nil {
		return err
	}
	rows, err := q.FinishCheckoutCompensation(ctx, purserdb.FinishCheckoutCompensationParams{TenantID: expiry.TenantID, IntentID: expiry.IntentID, Outcome: providerResult.Outcome(), ExpectedReferences: references})
	if err == nil && rows == 0 {
		return errors.New("checkout references changed during compensation; retry reconciliation")
	}
	return err
}

// bindCheckoutReceipt repairs a crash between provider acceptance and local binding.
// Metadata identifies the exact attempt; an expired attempt cannot activate later.
func (s *Service) bindCheckoutReceipt(ctx context.Context, tenantID, intentID, provider, sessionID, paymentID, subscriptionID string) (bool, error) {
	if intentID == "" {
		return false, nil
	}
	q := purserdb.New(s.db)
	if err := q.BindProviderCheckoutReceipt(ctx, purserdb.BindProviderCheckoutReceiptParams{TenantID: tenantID, IntentID: intentID, Provider: provider, SessionID: sessionID, PaymentID: paymentID, SubscriptionID: subscriptionID}); err != nil {
		return false, err
	}
	var session *stripeapi.CheckoutSession
	at, hasCompletion := subscriptions.ProviderCompletion(ctx)
	if !hasCompletion {
		at = time.Now()
	}
	if provider == "stripe" && s.stripeClient != nil {
		receipt, err := q.GetProviderCheckoutReservation(ctx, purserdb.GetProviderCheckoutReservationParams{TenantID: tenantID, IntentID: intentID})
		if err != nil {
			return false, err
		}
		if !receipt.CheckoutCompletedAt.Valid && !receipt.RecoveryStartedAt.Valid && receipt.Status != "succeeded" &&
			receipt.ExpiresAt.Valid && (at.IsZero() || at.After(receipt.ExpiresAt.Time)) {
			session, err = s.stripeClient.ReservedCheckout(ctx, receipt.ProviderCustomerID.String, receipt.ID, receipt.ProviderSessionID.String)
			if err != nil {
				return false, err
			}
		}
	}
	var r purserdb.GetProviderCheckoutReservationRow
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if _, err := subscriptions.LockChange(ctx, tx, tenantID, subscriptions.PreservePricing); err != nil {
			return err
		}
		var err error
		tq := purserdb.New(tx)
		r, err = tq.GetProviderCheckoutReservation(ctx, purserdb.GetProviderCheckoutReservationParams{TenantID: tenantID, IntentID: intentID})
		if err != nil {
			return err
		}
		if provider != "stripe" || r.CheckoutCompletedAt.Valid || r.RecoveryStartedAt.Valid || r.Status == "expired" || r.Status == "cancelled" || r.Status == "terminal_failed" || r.Status == "succeeded" {
			return nil
		}
		completion, ok := subscriptions.ProviderCompletion(ctx)
		if !ok {
			completion = time.Now()
		}
		if r.ExpiresAt.Valid && !completion.IsZero() && !completion.After(r.ExpiresAt.Time) {
			r.CheckoutCompletedAt = sql.NullTime{Time: completion, Valid: true}
		} else if session != nil {
			if completed, valid := stripeCheckoutCompletion(r, session); valid {
				r.CheckoutCompletedAt = sql.NullTime{Time: completed, Valid: true}
			}
		}
		if r.CheckoutCompletedAt.Valid {
			return tq.MarkCheckoutCompleted(ctx, purserdb.MarkCheckoutCompletedParams{TenantID: tenantID, IntentID: intentID, CompletedAt: r.CheckoutCompletedAt.Time})
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	if r.Status == "succeeded" {
		return false, nil
	}
	at, hasCompletion = subscriptions.ProviderCompletion(ctx)
	if !hasCompletion {
		at = time.Now()
	}
	expired := r.RecoveryStartedAt.Valid || r.Status == "expired" || r.Status == "cancelled" || r.Status == "terminal_failed" || !r.CheckoutCompletedAt.Valid && r.ExpiresAt.Valid && (at.IsZero() || at.After(r.ExpiresAt.Time))
	if expired {
		if err := q.RequeueExpiredCheckoutReservation(ctx, purserdb.RequeueExpiredCheckoutReservationParams{TenantID: tenantID, IntentID: intentID}); err != nil {
			return false, err
		}
	}
	return expired, nil
}

// ErrAwaitingProviderSettlement keeps normal bank-transfer settlement out of failure alerts.
var ErrAwaitingProviderSettlement = billing.ErrCheckoutSettlementPending

// inspectCheckoutForRecovery reads provider state without holding a subscription
// lock. The caller rechecks its durable snapshot before activation or compensation.
func (s *Service) inspectCheckoutForRecovery(ctx context.Context, exec purserdb.DBTX, r purserdb.GetProviderCheckoutReservationRow, locked subscriptions.LockedSubscription, completedAt *time.Time, incomingSubscriptionID *string) (func() error, error) {
	if !hasCheckoutProviderReference(r) {
		return nil, nil
	}
	tierID := ""
	if r.Purpose == "tenant_subscription_checkout" || r.Purpose == "mollie_first_payment" || r.Purpose == "mollie_subscription_create" {
		var readErr error
		tierID, readErr = purserdb.New(exec).GetSelfServiceCheckoutTier(ctx, purserdb.GetSelfServiceCheckoutTierParams{TenantID: r.TenantID, IntentID: r.ID})
		if readErr != nil {
			return nil, readErr
		}
		if tierID == "" {
			return nil, nil
		}
	}

	switch r.Provider {
	case "stripe":
		if s.stripeClient == nil {
			return nil, errors.New("stripe recovery is not configured")
		}
		sess, err := s.stripeClient.ReservedCheckout(ctx, r.ProviderCustomerID.String, r.ID, r.ProviderSessionID.String)
		if err != nil {
			return nil, err
		}
		if sess == nil {
			if len(r.ProviderRequest) == 0 && r.ProviderCustomerID.Valid && time.Now().Before(r.ExpiresAt.Time) {
				return nil, ErrAwaitingProviderSettlement
			}
			return nil, nil
		}
		if sess.Status == "open" {
			return nil, nil
		}
		if sess.Status != "complete" {
			return nil, nil
		}
		if sess.ExpiresAt <= 0 {
			return nil, errors.New("completed Stripe session has no provider expiry")
		}
		at, valid := stripeCheckoutCompletion(r, sess)
		if !valid {
			return nil, nil
		}
		*completedAt = at
		incomingID := ""
		if sess.Subscription != nil {
			incomingID = sess.Subscription.ID
			*incomingSubscriptionID = incomingID
		}
		conflict, conflictErr := s.checkoutHasConflictingSubscription(ctx, exec, r.TenantID, locked, "stripe", incomingID)
		if conflictErr != nil {
			return nil, conflictErr
		}
		if conflict {
			return nil, nil
		}
		if tierID != "" {
			if eligibilityErr := subscriptions.CheckSelfServiceActivation(ctx, exec, r.TenantID, tierID, "stripe", incomingID); eligibilityErr != nil {
				if errors.Is(eligibilityErr, sql.ErrNoRows) || subscriptions.IsPricingRefusal(eligibilityErr) {
					return nil, nil
				}
				return nil, eligibilityErr
			}
		}
		if sess.Subscription != nil && sess.Subscription.ID != "" {
			sub, subscriptionErr := s.stripeClient.GetSubscription(ctx, sess.Subscription.ID)
			if subscriptionErr != nil {
				return nil, subscriptionErr
			}
			if sub.Status == "incomplete_expired" || sub.Status == "canceled" {
				return nil, nil
			}
			if sess.PaymentStatus == "unpaid" && sess.PaymentIntent != nil && sess.PaymentIntent.ID != "" {
				pi, paymentErr := s.stripeClient.GetPaymentIntent(ctx, sess.PaymentIntent.ID)
				if paymentErr != nil {
					return nil, paymentErr
				}
				if pi.Status == "canceled" || pi.Status == "requires_payment_method" {
					return nil, nil
				}
			}
			if sess.PaymentStatus == "unpaid" {
				return nil, ErrAwaitingProviderSettlement
			}
			if sub.Status != "active" && sub.Status != "trialing" {
				return nil, ErrAwaitingProviderSettlement
			}
		}
		metadata := sess.Metadata
		if metadata == nil {
			metadata = map[string]string{}
		}
		metadata["tenant_id"] = r.TenantID
		metadata["checkout_reservation_id"] = r.ID
		customer, subscription, setup := "", "", ""
		if sess.Customer != nil {
			customer = sess.Customer.ID
		}
		if sess.Subscription != nil {
			subscription = sess.Subscription.ID
		}
		if sess.SetupIntent != nil {
			setup = sess.SetupIntent.ID
		}
		raw, err := json.Marshal(map[string]any{"id": sess.ID, "customer": customer, "subscription": subscription, "setup_intent": setup, "payment_status": sess.PaymentStatus, "mode": sess.Mode, "metadata": metadata})
		if err != nil {
			return nil, err
		}
		return func() error { return s.reconcileRecoveredStripeCheckout(ctx, r, raw, at, string(sess.PaymentStatus)) }, nil

	case "mollie":
		if r.Purpose != "mollie_first_payment" {
			return nil, nil
		}
		if s.mollieClient == nil {
			return nil, errors.New("mollie recovery is not configured")
		}
		payment, err := s.mollieClient.ReservedPayment(ctx, r.ProviderCustomerID.String, r.ID, r.ProviderPaymentID.String)
		if err != nil {
			return nil, err
		}
		if payment == nil {
			if len(r.ProviderRequest) == 0 && r.ProviderCustomerID.Valid && time.Now().Before(r.ExpiresAt.Time) {
				return nil, ErrAwaitingProviderSettlement
			}
			return nil, nil
		}
		conflict, conflictErr := s.checkoutHasConflictingSubscription(ctx, exec, r.TenantID, locked, "mollie", "")
		if conflictErr != nil {
			return nil, conflictErr
		}
		if conflict {
			return nil, nil
		}
		if tierID != "" {
			if eligibilityErr := subscriptions.CheckSelfServiceActivation(ctx, exec, r.TenantID, tierID, "mollie", ""); eligibilityErr != nil {
				if errors.Is(eligibilityErr, sql.ErrNoRows) || subscriptions.IsPricingRefusal(eligibilityErr) {
					return nil, nil
				}
				return nil, eligibilityErr
			}
		}
		if at, completed := mollieCheckoutCompletion(r, payment); completed {
			*completedAt = at
			if payment.Status != "paid" {
				return nil, ErrAwaitingProviderSettlement
			}
			if payment.PaidAt == nil {
				return nil, errors.New("paid Mollie checkout has no paidAt")
			}
			return func() error { return s.reconcileRecoveredMollieCheckout(ctx, r, payment.ID) }, nil
		}
		if payment.Status == "canceled" || payment.Status == "expired" || payment.Status == "failed" {
			return nil, nil
		}
		if !payment.IsCancelable {
			return nil, billingmollie.ErrPaymentSettling
		}
		return nil, nil
	}
	return nil, nil
}

// ErrCheckoutReservationNotFound identifies a missing tenant-owned subscription checkout.
var ErrCheckoutReservationNotFound = errors.New("tenant checkout reservation not found")

// RecoverCheckout durably requests an immediate recovery and attempts it within the caller's deadline.
func (s *Service) RecoverCheckout(ctx context.Context, tenantID, intentID, reason string) error {
	_, err := s.RecoverCheckoutResult(ctx, tenantID, intentID, reason)
	return err
}

// RecoverCheckoutResult records the authenticated request before any provider action.
func (s *Service) RecoverCheckoutResult(ctx context.Context, tenantID, intentID, reason string) (*purserpb.AdminRecoverCheckoutResponse, error) {
	actor := middleware.GetUserID(ctx)
	if actor == "" {
		return nil, errors.New("checkout recovery requires an operator identity")
	}
	var auditID string
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if _, err := subscriptions.LockChange(ctx, tx, tenantID, subscriptions.PreservePricing); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrCheckoutReservationNotFound
			}
			return err
		}
		q := purserdb.New(tx)
		row, err := q.GetProviderCheckoutReservation(ctx, purserdb.GetProviderCheckoutReservationParams{TenantID: tenantID, IntentID: intentID})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrCheckoutReservationNotFound
			}
			return err
		}
		if !isSubscriptionCheckout(row.Purpose) {
			return ErrCheckoutReservationNotFound
		}
		auditID, err = q.InsertCheckoutRecoveryAudit(ctx, purserdb.InsertCheckoutRecoveryAuditParams{TenantID: tenantID, IntentID: intentID, ActorID: actor, Reason: reason})
		if err != nil {
			return err
		}
		if err = billingevents.NewAndEnqueue(ctx, tx, tenantID, intentID, &internalv1.CheckoutRecoveryAction{AuditId: auditID, ReservationId: intentID, Outcome: "requested", Reason: reason}, events.Actor{AuthType: "jwt", UserID: actor}); err != nil {
			return err
		}
		return q.RequestCheckoutRecovery(ctx, purserdb.RequestCheckoutRecoveryParams{TenantID: tenantID, IntentID: intentID, Reason: reason})
	})
	if err != nil {
		return nil, err
	}
	recoveryErr := s.expireCheckoutReservation(ctx, checkoutReservationExpiry{TenantID: tenantID, IntentID: intentID, Force: true})
	if recoveryErr == nil {
		recoveryErr = s.finishCheckoutRecoveryAudits(ctx, tenantID, intentID)
	}
	out, readErr := s.checkoutRecoveryState(ctx, tenantID, intentID)
	if readErr != nil {
		return nil, readErr
	}
	out.AuditId = auditID
	if recoveryErr != nil {
		out.Outcome = "pending"
		out.Detail = recoveryErr.Error()
	}
	if err = s.recordCheckoutRecoveryPending(ctx, tenantID, auditID, out); err != nil {
		return nil, err
	}
	return out, nil
}

func isSubscriptionCheckout(purpose string) bool {
	return purpose == "tenant_subscription_checkout" || purpose == "mollie_first_payment" || purpose == "mollie_subscription_create"
}

func (s *Service) checkoutRecoveryState(ctx context.Context, tenantID, intentID string) (*purserpb.AdminRecoverCheckoutResponse, error) {
	row, err := purserdb.New(s.db).GetProviderCheckoutReservation(ctx, purserdb.GetProviderCheckoutReservationParams{TenantID: tenantID, IntentID: intentID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCheckoutReservationNotFound
	}
	if err != nil {
		return nil, err
	}
	if !isSubscriptionCheckout(row.Purpose) {
		return nil, ErrCheckoutReservationNotFound
	}
	out := &purserpb.AdminRecoverCheckoutResponse{Provider: row.Provider, Status: row.Status, AmountCents: row.AmountCents, Currency: row.Currency, AmountKnown: row.Provider == "mollie", Outcome: "pending"}
	switch row.Status {
	case "succeeded":
		out.Outcome = "activated"
		if row.RecoveryOutcome.String == "delivered" {
			out.Outcome = "delivered"
		}
	case "expired", "cancelled", "terminal_failed":
		out.Outcome = row.RecoveryOutcome.String
		if out.Outcome == "" {
			out.Outcome = row.Status
		}
	}
	return out, nil
}

// PreviewCheckoutRecovery reads provider state without changing a reservation or moving money.
func (s *Service) PreviewCheckoutRecovery(ctx context.Context, tenantID, intentID string) (*purserpb.AdminRecoverCheckoutResponse, error) {
	out, err := s.checkoutRecoveryState(ctx, tenantID, intentID)
	if err != nil {
		return nil, err
	}
	row, err := purserdb.New(s.db).GetProviderCheckoutReservation(ctx, purserdb.GetProviderCheckoutReservationParams{TenantID: tenantID, IntentID: intentID})
	if err != nil {
		return nil, err
	}
	if row.Provider == "stripe" && s.stripeClient != nil && hasCheckoutProviderReference(row) {
		sess, err := s.stripeClient.ReservedCheckout(ctx, row.ProviderCustomerID.String, row.ID, row.ProviderSessionID.String)
		if err != nil {
			return nil, err
		}
		if sess != nil {
			out.AmountCents = sess.AmountTotal
			out.AmountKnown = true
			out.Currency = strings.ToUpper(string(sess.Currency))
			out.Detail = fmt.Sprintf("provider checkout %s; payment %s", sess.Status, sess.PaymentStatus)
		}
	}
	if row.Provider == "mollie" && s.mollieClient != nil && row.Purpose == "mollie_first_payment" && hasCheckoutProviderReference(row) {
		payment, err := s.mollieClient.ReservedPayment(ctx, row.ProviderCustomerID.String, row.ID, row.ProviderPaymentID.String)
		if err != nil {
			return nil, err
		}
		if payment != nil && payment.Amount != nil {
			amount, err := decimal.NewFromString(payment.Amount.Value)
			if err != nil {
				return nil, err
			}
			out.AmountCents = amount.Shift(2).IntPart()
			out.AmountKnown = true
			out.Currency = payment.Amount.Currency
			out.Detail = "provider payment " + string(payment.Status)
		}
	}
	out.Outcome = "preview"
	return out, nil
}

func (s *Service) recordCheckoutRecoveryPending(ctx context.Context, tenantID, auditID string, out *purserpb.AdminRecoverCheckoutResponse) error {
	if out.Outcome != "pending" {
		return nil
	}
	return purserdb.New(s.db).FinishCheckoutRecoveryAudit(ctx, purserdb.FinishCheckoutRecoveryAuditParams{TenantID: tenantID, ID: auditID, Outcome: "pending", Detail: out.Detail})
}

func (s *Service) finishCheckoutRecoveryAudits(ctx context.Context, tenantID, intentID string) error {
	return database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		q := purserdb.New(tx)
		row, err := q.GetProviderCheckoutReservation(ctx, purserdb.GetProviderCheckoutReservationParams{TenantID: tenantID, IntentID: intentID})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		outcome := row.RecoveryOutcome.String
		if row.Status == "succeeded" {
			outcome = "activated"
			if row.RecoveryOutcome.String == "delivered" {
				outcome = "delivered"
			}
		} else if row.Status != "expired" && row.Status != "cancelled" && row.Status != "terminal_failed" {
			return nil
		}
		if outcome == "" {
			outcome = row.Status
		}
		audits, err := q.PendingCheckoutRecoveryAudits(ctx, purserdb.PendingCheckoutRecoveryAuditsParams{TenantID: tenantID, IntentID: intentID})
		if err != nil {
			return err
		}
		for _, a := range audits {
			if err = q.FinishCheckoutRecoveryAudit(ctx, purserdb.FinishCheckoutRecoveryAuditParams{TenantID: tenantID, ID: a.ID, Outcome: outcome, Detail: "reservation " + row.Status}); err != nil {
				return err
			}
			if err = billingevents.NewAndEnqueue(ctx, tx, tenantID, intentID, &internalv1.CheckoutRecoveryAction{AuditId: a.ID, ReservationId: intentID, Outcome: outcome, Reason: a.Reason, Detail: "reservation " + row.Status}, events.Actor{AuthType: "jwt", UserID: a.ActorID}); err != nil {
				return err
			}
		}
		return nil
	})
}

// reconcileRecoveredStripeCheckout retains the first durable observation as its
// metering boundary. Provider completion bounds admission; receipt time bounds
// the local billing phase, and neither advances on a metering retry.
func (s *Service) reconcileRecoveredStripeCheckout(ctx context.Context, r purserdb.GetProviderCheckoutReservationRow, sessionJSON []byte, completedBy time.Time, paymentStatus string) error {
	key := "recovered-checkout:" + r.ID + ":" + paymentStatus
	var event StripeWebhookPayload
	event.ID = key
	event.Type = "checkout.session.completed"
	event.Created = completedBy.Unix()
	event.Data.Object = sessionJSON
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if ok, message, _ := s.enqueueProviderWebhook(ctx, "stripe", key, nil, raw); !ok {
		return errors.New(message)
	}
	receivedAt, err := purserdb.New(s.db).ProviderWebhookReceivedAt(ctx, purserdb.ProviderWebhookReceivedAtParams{Provider: "stripe", EventKey: key, TenantID: r.TenantID})
	if err != nil {
		return err
	}
	ctx = context.WithValue(ctx, providerReceiptTimeKey{}, receivedAt)
	if ok, message, _ := s.processStripeWebhookPayload(ctx, event, "", raw); !ok {
		return errors.New(message)
	}
	receipt, err := purserdb.New(s.db).GetProviderCheckoutReservation(ctx, purserdb.GetProviderCheckoutReservationParams{TenantID: r.TenantID, IntentID: r.ID})
	if err != nil {
		return err
	}
	if receipt.Status != "succeeded" {
		return ErrAwaitingProviderSettlement
	}
	return nil
}

func hasCheckoutProviderReference(r purserdb.GetProviderCheckoutReservationRow) bool {
	return r.ProviderCustomerID.String != "" || r.ProviderSessionID.String != "" || r.ProviderPaymentID.String != "" || r.ProviderSubscriptionID.String != ""
}

// stripeCheckoutCompletion proves completion before the provider closes Checkout,
// independently of how many days payment settlement or webhook delivery takes.
func stripeCheckoutCompletion(r purserdb.GetProviderCheckoutReservationRow, sess *stripeapi.CheckoutSession) (time.Time, bool) {
	if sess == nil || sess.Status != "complete" || sess.ExpiresAt <= 0 {
		return time.Time{}, false
	}
	bound := time.Unix(sess.ExpiresAt, 0)
	deadline := r.ExpiresAt.Time
	if !r.ProviderDeadline.Valid {
		deadline = time.Unix(sess.Created, 0).Add(24 * time.Hour)
		if sess.Created <= 0 {
			return time.Time{}, false
		}
	}
	if bound.After(deadline) {
		return time.Time{}, false
	}
	if r.CheckoutCompletedAt.Valid {
		return r.CheckoutCompletedAt.Time, true
	}
	return bound, true
}

func nullCheckoutReference(value sql.NullString) any {
	if value.Valid {
		return value.String
	}
	return nil
}

// Mollie only expires open payments. A provider-fetched pending, authorized or
// paid payment has already left that window; paidAt describes settlement.
// The stored deadline is an upper bound on completion, not its timestamp.
func mollieCheckoutCompletion(r purserdb.GetProviderCheckoutReservationRow, payment *mollieapi.Payment) (time.Time, bool) {
	if payment == nil || (payment.Status != "pending" && payment.Status != "authorized" && payment.Status != "paid") {
		return time.Time{}, false
	}
	if r.CheckoutCompletedAt.Valid {
		return r.CheckoutCompletedAt.Time, true
	}
	if !r.ExpiresAt.Valid {
		return time.Time{}, false
	}
	bound := r.ExpiresAt.Time
	if payment.PaidAt != nil && payment.PaidAt.Before(bound) {
		bound = *payment.PaidAt
	}
	if payment.AuthorizedAt != nil && payment.AuthorizedAt.Before(bound) {
		bound = *payment.AuthorizedAt
	}
	return bound, true
}

func (s *Service) recordMollieCheckoutCompletion(ctx context.Context, tenantID, intentID string, payment *mollieapi.Payment) error {
	if intentID == "" {
		return nil
	}
	return database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if _, err := subscriptions.LockChange(ctx, tx, tenantID, subscriptions.PreservePricing); err != nil {
			return err
		}
		q := purserdb.New(tx)
		r, err := q.GetProviderCheckoutReservation(ctx, purserdb.GetProviderCheckoutReservationParams{TenantID: tenantID, IntentID: intentID})
		if err != nil {
			return err
		}
		if r.RecoveryStartedAt.Valid || r.Status == "expired" || r.Status == "cancelled" || r.Status == "terminal_failed" || r.Status == "succeeded" {
			return nil
		}
		if at, ok := mollieCheckoutCompletion(r, payment); ok {
			return q.MarkCheckoutCompleted(ctx, purserdb.MarkCheckoutCompletedParams{TenantID: tenantID, IntentID: intentID, CompletedAt: at})
		}
		return nil
	})
}

// Recovered payments retain their first durable observation for metering retries.
func (s *Service) reconcileRecoveredMollieCheckout(ctx context.Context, r purserdb.GetProviderCheckoutReservationRow, paymentID string) error {
	key := "recovered-mollie-checkout:" + r.ID + ":paid"
	raw, err := json.Marshal(map[string]string{"id": paymentID, "tenant_id": r.TenantID})
	if err != nil {
		return err
	}
	if ok, message, _ := s.enqueueProviderWebhook(ctx, "mollie", key, map[string]string{"Content-Type": "application/json"}, raw); !ok {
		return errors.New(message)
	}
	receivedAt, err := purserdb.New(s.db).ProviderWebhookReceivedAt(ctx, purserdb.ProviderWebhookReceivedAtParams{Provider: "mollie", EventKey: key, TenantID: r.TenantID})
	if err != nil {
		return err
	}
	ctx = context.WithValue(ctx, providerReceiptTimeKey{}, receivedAt)
	if _, callbackErr := s.handleMolliePaymentWebhook(ctx, paymentID, raw); callbackErr != nil {
		return callbackErr
	}
	receipt, err := purserdb.New(s.db).GetProviderCheckoutReservation(ctx, purserdb.GetProviderCheckoutReservationParams{TenantID: r.TenantID, IntentID: r.ID})
	if err != nil {
		return err
	}
	if receipt.Status != "succeeded" {
		return ErrAwaitingProviderSettlement
	}
	return nil
}

// A cancelled local status also represents Stripe's recoverable unpaid state.
// Probe such references before treating them as either live or safely replaceable.
func (s *Service) checkoutHasConflictingSubscription(ctx context.Context, exec purserdb.DBTX, tenantID string, locked subscriptions.LockedSubscription, provider, incomingID string) (bool, error) {
	current := locked.Current
	if locked.AccountClosed || locked.HasGrant {
		return true, nil
	}
	if id := current.StripeSubscriptionID.String; id != "" && (provider != "stripe" || id != incomingID) {
		if current.Status != "cancelled" {
			return true, nil
		}
		if s.stripeClient == nil {
			return false, errors.New("stripe client is required to inspect the previous subscription")
		}
		live, err := s.stripeClient.HasLiveSubscription(ctx, id)
		if err != nil || live {
			return live, err
		}
	}
	if id := current.MollieSubscriptionID.String; id != "" && (provider != "mollie" || id != incomingID) {
		if current.Status != "cancelled" {
			return true, nil
		}
		if s.mollieClient == nil {
			return false, errors.New("mollie client is required to inspect the previous subscription")
		}
		customerID, err := purserdb.New(exec).GetMollieCustomerID(ctx, tenantID)
		if err != nil {
			return false, err
		}
		live, readErr := s.mollieClient.HasLiveSubscription(ctx, customerID, id)
		if readErr != nil || live {
			return live, readErr
		}
	}
	return false, nil
}

// Known-delivered receipts may outlive a replaced subscription. Stop an orphan
// collector without refunding any fee that funded service.
func (s *Service) cancelDeliveredCheckout(ctx context.Context, r purserdb.GetProviderCheckoutReservationRow) error {
	subscriptionID := r.ProviderSubscriptionID.String
	if r.Provider == "stripe" && subscriptionID == "" {
		if s.stripeClient == nil {
			return errors.New("stripe cancellation is not configured")
		}
		session, err := s.stripeClient.ReservedCheckout(ctx, r.ProviderCustomerID.String, r.ID, r.ProviderSessionID.String)
		if err != nil {
			return err
		}
		if session == nil || session.Subscription == nil {
			return nil
		}
		subscriptionID = session.Subscription.ID
	}
	if subscriptionID == "" {
		return nil
	}
	return s.cancelClosedTenantProvider(ctx, closedTenantProviderSubscription{Provider: r.Provider, CustomerID: r.ProviderCustomerID.String, SubscriptionID: subscriptionID})
}
