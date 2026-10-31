package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"frameworks/api_billing/internal/database/purserdb"
	billingmollie "frameworks/api_billing/internal/mollie"
	"frameworks/api_billing/internal/subscriptions"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

// completeMollieFirstPayment stores the mandate and paid receipt together, excluding recovery.
func (s *Service) completeMollieFirstPayment(ctx context.Context, tenantID, paymentID, intentID string, info billingmollie.MandateInfo) error {
	details, err := json.Marshal(info.Details)
	if err != nil {
		return err
	}
	return database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if locked, err := subscriptions.LockChange(ctx, tx, tenantID, subscriptions.PreservePricing); err != nil {
			return err
		} else if locked.AccountClosed {
			return subscriptions.ErrTenantBillingClosed
		}
		q := purserdb.New(tx)
		id := intentID
		if id == "" {
			var err error
			id, err = q.FindProviderCheckoutReservation(ctx, purserdb.FindProviderCheckoutReservationParams{TenantID: tenantID, Provider: "mollie", PaymentID: paymentID})
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if err := subscriptions.CheckReceipt(ctx, tx, tenantID, id); err != nil {
			return err
		}
		if err := q.UpsertMollieCustomer(ctx, purserdb.UpsertMollieCustomerParams{TenantID: tenantID, MollieCustomerID: info.MollieCustomerID}); err != nil {
			return err
		}
		if err := q.UpsertMollieMandate(ctx, purserdb.UpsertMollieMandateParams{TenantID: tenantID, MollieCustomerID: info.MollieCustomerID, MollieMandateID: info.MollieMandateID, Status: info.Status, Method: info.Method, Details: details, CreatedAt: sql.NullTime{Time: info.CreatedAt, Valid: !info.CreatedAt.IsZero()}}); err != nil {
			return err
		}
		return q.ResolveMollieFirstPaymentIntent(ctx, purserdb.ResolveMollieFirstPaymentIntentParams{TenantID: tenantID, ProviderPaymentID: optionalSQLString(paymentID), Status: "succeeded"})
	})
}
