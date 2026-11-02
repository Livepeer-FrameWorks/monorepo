package grpc

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"frameworks/api_billing/internal/subscriptions"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// operatorVocabulary is text that belongs to operator runbooks and must not
// reach a tenant's dashboard or MCP client.
var operatorVocabulary = []string{"admin", "recovery", "purser", "inspect", "operator", "grant"}

func assertTenantRefusal(t *testing.T, err error, reason string) {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.FailedPrecondition {
		t.Fatalf("refusal = %v, want FailedPrecondition", err)
	}
	var info *errdetails.ErrorInfo
	for _, detail := range st.Details() {
		if candidate, isInfo := detail.(*errdetails.ErrorInfo); isInfo && candidate.GetDomain() == billingErrorDomain {
			info = candidate
		}
	}
	if info == nil || info.GetReason() != reason {
		t.Fatalf("refusal %q carries reason %v, want %s", st.Message(), info, reason)
	}
	if info.GetMetadata()["public_message"] != st.Message() {
		t.Fatalf("public_message %q differs from status message %q", info.GetMetadata()["public_message"], st.Message())
	}
	lowered := strings.ToLower(st.Message())
	for _, word := range operatorVocabulary {
		if strings.Contains(lowered, word) {
			t.Fatalf("tenant refusal %q contains operator text %q", st.Message(), word)
		}
	}
}

func TestPricingRefusalsReachTenantsTypedAndWithoutOperatorText(t *testing.T) {
	for _, tc := range []struct {
		cause  error
		reason string
	}{
		{fmt.Errorf("%w; checkout deadline: 2026-11-02T10:00:00Z; Inspect admin billing recovery list or run admin billing recovery checkout", subscriptions.ErrPendingCheckout), RefusalPendingCheckout},
		{subscriptions.ErrPendingFreeFallback, RefusalPendingFreeFallback},
		{subscriptions.ErrPendingTierUpgrade, RefusalPendingUpgrade},
		{subscriptions.ErrOutstandingGrant, RefusalOutstandingGrant},
		{subscriptions.ErrTenantBillingClosed, RefusalAccountClosed},
		{subscriptions.ErrPaidCancellationTerm, RefusalPaidTermEnding},
		{subscriptions.ErrSelfServiceSubscription, RefusalProviderSubscription},
		{subscriptions.ErrSelfServiceTierChange, RefusalProviderSubscription},
		{subscriptions.ErrSelfServiceTier, RefusalTierNotSelfService},
		{subscriptions.ErrUnusedMollieFunding, RefusalUnusedFirstPayment},
		{subscriptions.ErrMollieFundingMismatch, RefusalUnusedFirstPayment},
		{subscriptions.ErrMollieFundingRequired, RefusalFirstPaymentRequired},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			err := tenantPricingRefusal(tc.cause)
			assertTenantRefusal(t, err, tc.reason)
			if !errors.Is(err, tc.cause) {
				t.Fatalf("refusal lost its domain cause %v", tc.cause)
			}
		})
	}
	if _, ok := pricingRefusalStatus(errors.New("billing details incomplete")); ok {
		t.Fatal("an untyped error was classified as a pricing refusal")
	}
}

func TestMollieActivationRefusalCarriesItsReason(t *testing.T) {
	assertTenantRefusal(t, mollieActivationStatus(subscriptions.ErrOutstandingGrant), RefusalOutstandingGrant)
	queued := mollieActivationStatus(errors.Join(subscriptions.ErrCheckoutCompensationQueued, subscriptions.ErrSelfServiceTier))
	assertTenantRefusal(t, queued, RefusalTierNotSelfService)
}
