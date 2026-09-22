package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"frameworks/api_billing/internal/billingevents"
	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
)

// recordMollieFirstPaymentOpen stores the Mollie payment on its first-payment
// intent and, when the intent did not already name that payment, records
// billing.payment_created in the same transaction. A retried request that
// Mollie answers with the same payment records nothing. Purser keeps no
// payment row for a first payment, so the event is keyed by the Mollie payment
// ID and carries it as the provider reference.
func (s *PurserServer) recordMollieFirstPaymentOpen(ctx context.Context, tenantID, intentID, molliePaymentID string, amount *publicv1.Money) error {
	return database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		queries := purserdb.New(tx)
		prior, err := queries.LockProviderIntentPaymentID(ctx, intentID)
		if err != nil {
			return fmt.Errorf("lock first-payment intent: %w", err)
		}
		if err := queries.SetProviderIntentPaymentOpen(ctx, purserdb.SetProviderIntentPaymentOpenParams{
			PaymentID: sql.NullString{String: molliePaymentID, Valid: true}, IntentID: intentID,
		}); err != nil {
			return fmt.Errorf("record provider payment: %w", err)
		}
		if prior != molliePaymentID {
			if err := billingevents.NewAndEnqueue(ctx, tx, tenantID, molliePaymentID, &internalv1.PaymentCreated{
				Amount: amount, Provider: "mollie", ProviderReferenceId: molliePaymentID,
			}, s.domainActor(ctx)); err != nil {
				return err
			}
		}
		return nil
	})
}

// activateMollieSubscription writes the Mollie subscription onto the tenant's
// subscription row and, when the row did not already name that Mollie
// subscription, records billing.subscription_created in the same
// transaction. It returns the rows updated; zero means the tenant has no
// subscription row.
func (s *PurserServer) activateMollieSubscription(ctx context.Context, params purserdb.ActivateMollieTenantSubscriptionParams) (int64, error) {
	var rows int64
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		rows = 0
		queries := purserdb.New(tx)
		prior, err := queries.LockTenantSubscriptionMollieID(ctx, params.TenantID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock tenant subscription: %w", err)
		}
		rows, err = queries.ActivateMollieTenantSubscription(ctx, params)
		if err != nil {
			return err
		}
		if rows > 0 && prior.MollieSubscriptionID != params.SubscriptionID.String {
			if err := billingevents.NewAndEnqueue(ctx, tx, params.TenantID, prior.ID, &internalv1.SubscriptionCreated{
				SubscriptionId: prior.ID, TierId: params.TierID, Status: "active",
			}, s.domainActor(ctx)); err != nil {
				return err
			}
		}
		return nil
	})
	return rows, err
}
