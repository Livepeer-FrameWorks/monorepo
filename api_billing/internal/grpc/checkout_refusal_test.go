package grpc

import (
	"database/sql"
	"errors"
	"testing"

	"frameworks/api_billing/internal/subscriptions"

	"github.com/DATA-DOG/go-sqlmock"

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
		{"invalid mandate", subscriptions.ErrInvalidMollieMandate, true},
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
			if got := subscriptions.IsDefinitiveMollieActivationRefusal(err); got != tc.definitive {
				t.Fatalf("compensates=%v want=%v", got, tc.definitive)
			}
		})
	}
	for _, code := range []codes.Code{codes.FailedPrecondition, codes.NotFound} {
		if subscriptions.IsDefinitiveMollieActivationRefusal(status.Error(code, "repairable condition")) {
			t.Fatalf("status %s alone triggers compensation", code)
		}
	}
}

func TestSelfServiceCheckoutMissingSubscriptionIsRepairable(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("GetTierForBillingChange").WithArgs("tier").WillReturnRows(sqlmock.NewRows([]string{"tier_level", "tier_name", "is_default_prepaid", "is_active", "is_enterprise"}).AddRow(2, "pro", false, true, false))
	mock.ExpectQuery("SelfServiceAdvanceTierChange").WillReturnRows(sqlmock.NewRows([]string{"changing"}).AddRow(false))
	mock.ExpectQuery("GetSelfServiceNativeSubscriptions").WithArgs("tenant").WillReturnError(sql.ErrNoRows)
	err = (&PurserServer{db: db}).requireSelfServiceCheckout(t.Context(), "tenant", "tier", "mollie")
	if status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != "tenant subscription is missing; bootstrap it before checkout" || subscriptions.IsDefinitiveMollieActivationRefusal(err) {
		t.Fatalf("missing subscription misclassified: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQueuedMollieRefusalDoesNotGainASecondCompensationOwner(t *testing.T) {
	for _, cause := range []error{subscriptions.ErrCheckoutExpired, subscriptions.ErrSelfServiceTier} {
		err := mollieActivationStatus(errors.Join(subscriptions.ErrCheckoutCompensationQueued, cause))
		if status.Code(err) != codes.FailedPrecondition || subscriptions.IsDefinitiveMollieActivationRefusal(err) {
			t.Fatalf("committed refusal was offered to the RPC compensation owner again: %v", err)
		}
	}
}
