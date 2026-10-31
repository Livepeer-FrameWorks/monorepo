package grpc

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_billing/internal/mollie"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	stripelib "github.com/stripe/stripe-go/v85"
)

func TestCheckoutFailureProposesTerminalForUnsubmittedReservation(t *testing.T) {
	for _, code := range []string{"checkout_preflight_failed", "missing_billing_email", "tenant_primary_user_lookup_failed", "customer_create_failed", "customer_mapping_failed", "customer_lookup_failed", "customer_intent_link_failed"} {
		t.Run(code, func(t *testing.T) {
			db, m, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			m.ExpectExec("SetProviderPaymentIntentFailure").WithArgs("terminal_failed", false, sqlmock.AnyArg(), "intent", "tenant").WillReturnResult(sqlmock.NewResult(0, 1))
			s := &PurserServer{db: db, logger: logging.NewLogger()}
			s.markProviderIntentFailed(context.Background(), "tenant", "intent", code, errors.New("preflight failure"))
			if err = m.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCheckoutDefiniteStripeRejectionProposesTerminal(t *testing.T) {
	db, m, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m.ExpectExec("SetProviderPaymentIntentFailure").WithArgs("terminal_failed", true, sqlmock.AnyArg(), "intent", "tenant").WillReturnResult(sqlmock.NewResult(0, 1))
	s := &PurserServer{db: db, logger: logging.NewLogger()}
	s.markProviderIntentFailed(context.Background(), "tenant", "intent", "checkout_session_create_failed", &stripelib.Error{HTTPStatusCode: 400})
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCheckoutFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		code     int
		expected string
	}{{400, "terminal_failed"}, {422, "terminal_failed"}, {408, "provider_call_failed"}, {409, "provider_call_failed"}, {429, "provider_call_failed"}, {500, "provider_call_failed"}} {
		if got := checkoutFailureStatus("first_payment_create_failed", &mollie.APIError{StatusCode: tc.code}); got != tc.expected {
			t.Fatalf("Mollie %d: got %s want %s", tc.code, got, tc.expected)
		}
	}
	if got := checkoutFailureStatus("subscription_create_failed", context.DeadlineExceeded); got != "provider_call_failed" {
		t.Fatalf("ambiguous timeout settled as %s", got)
	}
}

func TestCheckoutPreflightFailureSettlesAfterCallerCancellation(t *testing.T) {
	db, m, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m.ExpectExec("SetProviderPaymentIntentFailure").WithArgs("terminal_failed", false, sqlmock.AnyArg(), "intent", "tenant").WillReturnResult(sqlmock.NewResult(0, 1))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	server := &PurserServer{db: db, logger: logging.NewLogger()}
	server.markProviderIntentFailed(ctx, "tenant", "intent", "checkout_preflight_failed", context.Canceled)
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatalf("cancelled pre-provider request left its reservation outstanding: %v", err)
	}
}
