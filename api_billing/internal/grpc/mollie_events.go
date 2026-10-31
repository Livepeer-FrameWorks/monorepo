package grpc

import (
	"context"
	"database/sql"
	"fmt"

	"frameworks/api_billing/internal/billingevents"
	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/subscriptions"
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
		prior, err := queries.LockProviderIntentPaymentID(ctx, purserdb.LockProviderIntentPaymentIDParams{TenantID: tenantID, IntentID: intentID})
		if err != nil {
			return fmt.Errorf("lock first-payment intent: %w", err)
		}
		if err := queries.SetProviderIntentPaymentOpen(ctx, purserdb.SetProviderIntentPaymentOpenParams{TenantID: tenantID,
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
// subscription row. Checkout reference binding and settlement commit with the activation.
func (s *PurserServer) activateMollieSubscription(ctx context.Context, params purserdb.ActivateMollieTenantSubscriptionParams, intentID string) (int64, error) {
	var rows int64
	var refusal error
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		rows = 0
		refusal = nil
		result, err := subscriptions.TransitionProvider(ctx, tx, subscriptions.ProviderChange{TenantID: params.TenantID, Operation: subscriptions.MollieActivation, Mollie: params, ReceiptIntentID: intentID})
		if err != nil {
			return err
		}
		refusal = result.Refusal
		rows = result.Rows
		if refusal == nil && rows > 0 {
			if err = purserdb.New(tx).SetProviderIntentSubscriptionOpen(ctx, purserdb.SetProviderIntentSubscriptionOpenParams{TenantID: params.TenantID, SubscriptionID: params.SubscriptionID, IntentID: intentID}); err != nil {
				return err
			}
			if err = purserdb.New(tx).CompleteMollieSubscriptionCheckout(ctx, purserdb.CompleteMollieSubscriptionCheckoutParams{TenantID: params.TenantID, SubscriptionID: params.SubscriptionID.String}); err != nil {
				return err
			}
		}

		if refusal == nil && rows > 0 && result.PreviousMollieSubscriptionID != params.SubscriptionID.String {
			if err := billingevents.NewAndEnqueue(ctx, tx, params.TenantID, result.SubscriptionID, &internalv1.SubscriptionCreated{
				SubscriptionId: result.SubscriptionID, TierId: params.TierID, Status: "active",
			}, s.domainActor(ctx)); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		err = refusal
	}
	return rows, err
}
