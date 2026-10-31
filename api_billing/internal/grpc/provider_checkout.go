package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/mollie"
	"frameworks/api_billing/internal/subscriptions"

	stripelib "github.com/stripe/stripe-go/v85"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// requireProviderCheckout checks the same locked policy used when the provider activates.
func (s *PurserServer) requireProviderCheckout(ctx context.Context, tenantID string) error {
	result, err := subscriptions.TransitionProvider(ctx, s.db, subscriptions.ProviderChange{TenantID: tenantID, Operation: subscriptions.ProviderCheckout})
	if subscriptions.IsPricingRefusal(err) {
		return checkoutPolicyStatus(codes.FailedPrecondition, err.Error(), err)
	}
	if err != nil {
		return status.Errorf(codes.Internal, "check provider checkout: %v", err)
	}
	if result.Rows == 0 {
		return status.Error(codes.FailedPrecondition, "assign a subscription before starting provider checkout")
	}
	return s.requireSettledOnFree(ctx, purserdb.New(s.db), tenantID)
}

// requireSettledOnFree keeps a tenant on Free from buying a paid tier while it
// still owes invoices. Paying those invoices stays available.
func (s *PurserServer) requireSettledOnFree(ctx context.Context, queries *purserdb.Queries, tenantID string) error {
	owes, err := queries.TenantOwesInvoicesOnFree(ctx, tenantID)
	if err != nil {
		return status.Errorf(codes.Internal, "check outstanding invoices: %v", err)
	}
	if owes {
		return status.Error(codes.FailedPrecondition, "settle your outstanding invoices before selecting a paid tier")
	}
	return nil
}

// finishProviderCheckout records confirmed cancellation without reopening a completed receipt.
func (s *PurserServer) finishProviderCheckout(ctx context.Context, tenantID, intentID, state, sessionID, subscriptionID string) error {
	return purserdb.New(s.db).FinishProviderCheckoutIntent(ctx, purserdb.FinishProviderCheckoutIntentParams{
		TenantID: tenantID, IntentID: intentID, Status: state,
		SessionID:      sql.NullString{String: sessionID, Valid: sessionID != ""},
		SubscriptionID: sql.NullString{String: subscriptionID, Valid: subscriptionID != ""},
	})
}

// expireProviderCheckout keeps a confirmed provider expiry and its local reservation together.
func (s *PurserServer) expireProviderCheckout(ctx context.Context, tenantID, intentID, sessionID string) error {
	if err := s.stripeClient.ExpireCheckoutSession(ctx, sessionID); err != nil {
		return err
	}
	if err := s.finishProviderCheckout(ctx, tenantID, intentID, "expired", sessionID, ""); err != nil {
		return fmt.Errorf("checkout expired at Stripe but its local receipt could not be updated: %w", err)
	}
	return nil
}

// checkoutFailureStatus proposes a failure class; the repository preserves payable reservations.
func checkoutFailureStatus(code string, cause error) string {
	switch code {
	case "checkout_preflight_failed", "missing_billing_email", "tenant_primary_user_lookup_failed", "customer_create_failed", "customer_mapping_failed", "customer_lookup_failed", "customer_intent_link_failed":
		return "terminal_failed"
	}
	if definiteCheckoutRejection(code, cause) {
		return "terminal_failed"
	}
	return "provider_call_failed"
}

// Only the failed checkout-create call can prove that no payable object exists.
// A failure while looking up a customer says nothing about an earlier checkout.
func definiteCheckoutRejection(code string, cause error) bool {
	switch code {
	case "checkout_session_create_failed", "first_payment_create_failed", "subscription_create_failed":
	default:
		return false
	}
	var stripeErr *stripelib.Error
	var mollieErr *mollie.APIError
	statusCode := 0
	if errors.As(cause, &stripeErr) {
		statusCode = stripeErr.HTTPStatusCode
	}
	if errors.As(cause, &mollieErr) {
		statusCode = mollieErr.StatusCode
	}
	if statusCode >= 400 && statusCode < 500 && statusCode != 408 && statusCode != 409 && statusCode != 429 {
		return true
	}
	return false
}

// checkoutRequestError maps only the journal's domain refusals to a precondition.
func checkoutRequestError(err error) error {
	code := codes.Internal
	if errors.Is(err, subscriptions.ErrCheckoutExpired) || subscriptions.IsPricingRefusal(err) || errors.Is(err, subscriptions.ErrCheckoutRequestChanged) {
		code = codes.FailedPrecondition
	}
	return checkoutPolicyStatus(code, fmt.Sprintf("retain checkout request: %v", err), err)
}
