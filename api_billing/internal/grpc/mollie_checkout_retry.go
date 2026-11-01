package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/subscriptions"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// reserveMollieCheckout resolves ended native receipts before retrying a refused
// reservation. Unknown or live provider subscriptions retain their checkout fence.
func (s *PurserServer) reserveMollieCheckout(ctx context.Context, tenantID string, reserve func(*sql.Tx) (string, error)) (string, error) {
	id, err := subscriptions.ReserveCheckout(ctx, s.db, tenantID, reserve)
	if !errors.Is(err, subscriptions.ErrPendingCheckout) && !errors.Is(err, subscriptions.ErrCheckoutExpired) {
		return id, err
	}
	q := purserdb.New(s.db)
	candidates, readErr := q.ListUnresolvedMollieNativeCheckouts(ctx, tenantID)
	if readErr != nil {
		return "", fmt.Errorf("read unresolved Mollie checkouts: %w", readErr)
	}
	resolved := false
	for _, candidate := range candidates {
		receipt, receiptErr := q.GetProviderCheckoutReservation(ctx, purserdb.GetProviderCheckoutReservationParams{TenantID: tenantID, IntentID: candidate})
		if receiptErr != nil {
			return "", receiptErr
		}
		// The candidate may have entered activation or recovery since discovery.
		if receipt.Provider != "mollie" || receipt.Purpose != "mollie_subscription_create" ||
			receipt.ActivatedAt.Valid || receipt.RecoveryStartedAt.Valid ||
			(receipt.Status != "pending" && receipt.Status != "provider_open" && receipt.Status != "provider_call_failed") ||
			!receipt.ProviderCustomerID.Valid || !receipt.ProviderSubscriptionID.Valid ||
			!receipt.ExpiresAt.Valid || receipt.ExpiresAt.Time.After(time.Now()) {
			continue
		}
		sub, providerErr := s.mollieClient.GetSubscription(ctx, receipt.ProviderCustomerID.String, receipt.ProviderSubscriptionID.String)
		if providerErr != nil {
			return "", status.Error(codes.Unavailable, "cannot confirm the previous Mollie setup; retry the same request")
		}
		if sub == nil || sub.ID != receipt.ProviderSubscriptionID.String || (sub.Status != "canceled" && sub.Status != "completed") {
			continue
		}
		if writeErr := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
			locked, lockErr := subscriptions.LockChange(ctx, tx, tenantID, subscriptions.PreservePricing)
			if lockErr != nil {
				return lockErr
			}
			queries := purserdb.New(tx)
			current, currentErr := queries.GetProviderCheckoutReservation(ctx, purserdb.GetProviderCheckoutReservationParams{TenantID: tenantID, IntentID: candidate})
			if currentErr != nil {
				return currentErr
			}
			if locked.Current.MollieSubscriptionID.String == sub.ID || !reflect.DeepEqual(current, receipt) {
				return subscriptions.ErrPendingCheckout
			}
			return queries.FinishProviderCheckoutIntent(ctx, purserdb.FinishProviderCheckoutIntentParams{
				TenantID: tenantID, IntentID: candidate, Status: "cancelled",
				SubscriptionID: receipt.ProviderSubscriptionID,
			})
		}); writeErr != nil {
			return "", writeErr
		}
		resolved = true
	}
	if !resolved {
		return id, err
	}
	return subscriptions.ReserveCheckout(ctx, s.db, tenantID, reserve)
}
