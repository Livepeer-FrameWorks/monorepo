package grpc

import (
	"database/sql"
	"errors"
	"testing"

	"frameworks/api_billing/internal/subscriptions"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestMollieDefinitiveRefusalKeepsItsDomainCause(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cause      error
		definitive bool
	}{
		{"eligibility", subscriptions.ErrSelfServiceTier, true},
		{"closed account", subscriptions.ErrTenantBillingClosed, true},
		{"grant", subscriptions.ErrOutstandingGrant, true},
		{"pending fallback", subscriptions.ErrPendingFreeFallback, false},
		{"pending checkout", subscriptions.ErrPendingCheckout, false},
		{"pending upgrade", subscriptions.ErrPendingTierUpgrade, false},
		{"changed journal", subscriptions.ErrCheckoutRequestChanged, false},
		{"expired receipt", subscriptions.ErrCheckoutExpired, false},
		{"unrelated missing row", sql.ErrNoRows, false},
		{"repairable billing details", errors.New("billing details incomplete"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkoutPolicyStatus(codes.FailedPrecondition, tc.cause.Error(), tc.cause)
			if !errors.Is(err, tc.cause) || status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != tc.cause.Error() {
				t.Fatalf("domain cause or public status lost: %v", err)
			}
			if got := definitiveMollieActivationRefusal(err); got != tc.definitive {
				t.Fatalf("compensates=%v want=%v", got, tc.definitive)
			}
		})
	}
	for _, code := range []codes.Code{codes.FailedPrecondition, codes.NotFound} {
		if definitiveMollieActivationRefusal(status.Error(code, "repairable condition")) {
			t.Fatalf("status %s alone triggers compensation", code)
		}
	}
}
