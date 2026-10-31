package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_billing/internal/billingevents"
	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/subscriptions"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	mollieapi "github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/shopspring/decimal"
)

// ErrCheckoutDeliveryDecisionNotAllowed prevents an attestation from suppressing
// an existing refund obligation or replacing normal checkout activation.
var ErrCheckoutDeliveryDecisionNotAllowed = errors.New("only a paid historical Mollie first payment without a refund obligation can be retained as delivered")

func historicalDeliveryCandidate(row purserdb.GetProviderCheckoutReservationRow) bool {
	return row.Provider == "mollie" && row.Purpose == "mollie_first_payment" &&
		!row.ProviderMandateID.Valid && row.ProviderPaymentID.String != "" && row.SucceededAt.Valid
}

func checkHistoricalDeliveryDecision(ctx context.Context, q *purserdb.Queries, tenantID, intentID string) (purserdb.GetProviderCheckoutReservationRow, error) {
	row, err := q.GetProviderCheckoutReservation(ctx, purserdb.GetProviderCheckoutReservationParams{TenantID: tenantID, IntentID: intentID})
	if errors.Is(err, sql.ErrNoRows) {
		return row, ErrCheckoutReservationNotFound
	}
	if err != nil {
		return row, err
	}
	if !historicalDeliveryCandidate(row) || row.CompensationCurrent {
		return row, ErrCheckoutDeliveryDecisionNotAllowed
	}
	obligated, err := q.HasMollieCheckoutRefundObligation(ctx, purserdb.HasMollieCheckoutRefundObligationParams{TenantID: tenantID, PaymentID: row.ProviderPaymentID.String})
	if err != nil {
		return row, err
	}
	if obligated {
		return row, ErrCheckoutDeliveryDecisionNotAllowed
	}
	return row, nil
}

// PreviewRetainDeliveredCheckout describes an operator attestation without
// changing the checkout or asking the provider to move money.
func (s *Service) PreviewRetainDeliveredCheckout(ctx context.Context, tenantID, intentID string) (*purserpb.AdminRecoverCheckoutResponse, error) {
	if _, err := checkHistoricalDeliveryDecision(ctx, purserdb.New(s.db), tenantID, intentID); err != nil {
		return nil, err
	}
	out, err := s.PreviewCheckoutRecovery(ctx, tenantID, intentID)
	if err != nil {
		return nil, err
	}
	out.Detail = "Attest that this historical first payment funded delivered service. Retain the payment without refund; no subscription or access is activated."
	return out, nil
}

// RetainDeliveredCheckout records an authenticated operator's delivery decision.
// The same intent lock as automatic compensation excludes a concurrent refund.
func (s *Service) RetainDeliveredCheckout(ctx context.Context, tenantID, intentID, reason string) (out *purserpb.AdminRecoverCheckoutResponse, resultErr error) {
	actor := middleware.GetUserID(ctx)
	if actor == "" || strings.TrimSpace(reason) == "" {
		return nil, ErrCheckoutDeliveryDecisionNotAllowed
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	release, acquired, err := database.TrySessionLock(ctx, s.db, database.NamedLock("checkout-recovery:"+tenantID+":"+intentID))
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, ErrAwaitingProviderSettlement
	}
	defer func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer releaseCancel()
		resultErr = errors.Join(resultErr, release(releaseCtx))
	}()
	row, err := checkHistoricalDeliveryDecision(ctx, purserdb.New(s.db), tenantID, intentID)
	if err != nil {
		return nil, err
	}
	if s.mollieClient == nil {
		return nil, errors.New("mollie delivery verification requires its configured provider client")
	}
	payment, err := s.mollieClient.GetPayment(ctx, row.ProviderPaymentID.String)
	if err != nil {
		return nil, err
	}
	if payment.Status != "paid" || payment.CustomerID != row.ProviderCustomerID.String {
		return nil, ErrCheckoutDeliveryDecisionNotAllowed
	}
	for _, returned := range []*mollieapi.Amount{payment.AmountRefunded, payment.AmountChargedBack} {
		if returned == nil {
			continue
		}
		amount, parseErr := decimal.NewFromString(returned.Value)
		if parseErr != nil || !amount.IsZero() {
			return nil, ErrCheckoutDeliveryDecisionNotAllowed
		}
	}
	var auditID string
	err = database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if _, lockErr := subscriptions.LockChange(ctx, tx, tenantID, subscriptions.PreservePricing); lockErr != nil {
			return lockErr
		}
		q := purserdb.New(tx)
		if _, checkErr := checkHistoricalDeliveryDecision(ctx, q, tenantID, intentID); checkErr != nil {
			return checkErr
		}
		auditID, err = q.InsertCheckoutRecoveryAudit(ctx, purserdb.InsertCheckoutRecoveryAuditParams{TenantID: tenantID, IntentID: intentID, ActorID: actor, Reason: reason})
		if err != nil {
			return err
		}
		if err = q.RetainDeliveredHistoricalMollieCheckout(ctx, purserdb.RetainDeliveredHistoricalMollieCheckoutParams{TenantID: tenantID, IntentID: intentID}); err != nil {
			return err
		}
		const detail = "Operator attested delivered service; payment retained without refund."
		if err = q.FinishCheckoutRecoveryAudit(ctx, purserdb.FinishCheckoutRecoveryAuditParams{TenantID: tenantID, ID: auditID, Outcome: "delivered", Detail: detail}); err != nil {
			return err
		}
		return billingevents.NewAndEnqueue(ctx, tx, tenantID, intentID, &internalv1.CheckoutRecoveryAction{AuditId: auditID, ReservationId: intentID, Outcome: "delivered", Reason: reason, Detail: detail}, events.Actor{AuthType: "jwt", UserID: actor})
	})
	if err != nil {
		return nil, fmt.Errorf("retain delivered checkout: %w", err)
	}
	out, err = s.checkoutRecoveryState(ctx, tenantID, intentID)
	if err == nil {
		out.AuditId, out.Detail = auditID, "Payment retained for delivered service; no refund requested."
	}
	return out, err
}
