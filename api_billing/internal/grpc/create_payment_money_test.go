package grpc

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type capturedPaymentIDArgument struct{ paymentID *string }

func (argument capturedPaymentIDArgument) Match(value driver.Value) bool {
	actual, ok := value.(string)
	return ok && argument.paymentID != nil && *argument.paymentID != "" && actual == *argument.paymentID
}

const (
	paymentInvoiceID = "81000000-0000-4000-8000-000000000001"
	paymentTenantID  = "82000000-0000-4000-8000-000000000001"
	paymentID        = "83000000-0000-4000-8000-000000000001"
)

func configureCardOnlyPayments(t *testing.T) {
	t.Helper()
	t.Setenv("CRYPTO_DEPOSITS_ENABLED", "false")
	t.Setenv("PAYMENT_CARD_PROVIDER", "stripe")
	t.Setenv("WEBAPP_PUBLIC_URL", "https://app.example.test")
	t.Setenv("STRIPE_SECRET_KEY", "sk_test")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")
	t.Setenv("MOLLIE_API_KEY", "")
	t.Setenv("GATEWAY_PUBLIC_URL", "")
}

func paymentTenantContext() context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, paymentTenantID)
	return context.WithValue(ctx, ctxkeys.KeyUserID, "user-1")
}

func expectInvoicePaymentExpiration(mock sqlmock.Sqlmock) {
	for _, payment := range []struct {
		asset  string
		method string
	}{{asset: "ETH", method: "crypto_eth"}, {asset: "USDC", method: "crypto_usdc"}} {
		mock.ExpectBegin()
		mock.ExpectExec("-- name: FailExpiredCryptoInvoicePayments").
			WithArgs(payment.method, paymentInvoiceID, paymentTenantID, payment.asset).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec("-- name: ExpireCryptoInvoiceWallets").
			WithArgs(paymentInvoiceID, paymentTenantID, payment.asset).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectCommit()
	}
	mock.ExpectExec("-- name: ExpireStaleCardInvoicePayments").
		WithArgs(paymentInvoiceID, paymentTenantID).
		WillReturnResult(sqlmock.NewResult(0, 0))
}

func expectInvoicePaymentLockAndBalance(mock sqlmock.Sqlmock, total, paid string) {
	mock.ExpectBegin()
	mock.ExpectExec("-- name: LockInvoicePaymentCreation").WithArgs(paymentInvoiceID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("-- name: GetPayableInvoiceBalance").WithArgs(paymentInvoiceID, paymentTenantID).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "total_amount", "currency", "net_paid"}).AddRow(paymentTenantID, total, "EUR", paid))
}

func activeInvoicePaymentRows(method, amount string, paymentURL any, count int32) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "method", "amount", "currency", "tx_id", "payment_url", "created_at", "active_count"}).
		AddRow(paymentID, method, amount, "EUR", nil, paymentURL, time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC), count)
}

func TestCreatePaymentReplaysExistingCardIntentWithoutDuplicatingMoneyMovement(t *testing.T) {
	configureCardOnlyPayments(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	expectInvoicePaymentExpiration(mock)
	expectInvoicePaymentLockAndBalance(mock, "20.00", "5.00")
	mock.ExpectQuery("-- name: GetActiveInvoicePayment").WithArgs(paymentInvoiceID, paymentTenantID).
		WillReturnRows(activeInvoicePaymentRows("card", "15.00", "https://checkout.example.test/session", 1))
	mock.ExpectCommit()

	server := &PurserServer{db: db, logger: logging.NewLogger()}
	response, err := server.CreatePayment(paymentTenantContext(), &purserpb.PaymentRequest{InvoiceId: paymentInvoiceID, Method: "CARD"})
	if err != nil {
		t.Fatal(err)
	}
	if response.GetId() != paymentID || response.GetAmount() != 15 || response.GetCurrency() != "EUR" || response.GetStatus() != "pending" || response.GetPaymentUrl() != "https://checkout.example.test/session" {
		t.Fatalf("replayed payment = %+v", response)
	}
	if response.GetExpiresAt().AsTime().Sub(response.GetCreatedAt().AsTime()) != 24*time.Hour {
		t.Fatalf("card intent expiry = %v", response.GetExpiresAt().AsTime().Sub(response.GetCreatedAt().AsTime()))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func expectNewPendingCardPayment(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("-- name: GetActiveInvoicePayment").WithArgs(paymentInvoiceID, paymentTenantID).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("-- name: CreatePendingInvoicePayment").
		WithArgs(sqlmock.AnyArg(), paymentInvoiceID, "card", "15.00", "EUR", "", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("-- name: EnqueueBillingEventOutbox").
		WithArgs(sqlmock.AnyArg(), "payment_created", paymentTenantID, "user-1", "payment", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("84000000-0000-4000-8000-000000000001"))
	mock.ExpectCommit()
}

func TestCreatePaymentPersistsIntentAndAuditEventBeforeProviderCall(t *testing.T) {
	configureCardOnlyPayments(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	expectInvoicePaymentExpiration(mock)
	expectInvoicePaymentLockAndBalance(mock, "20.00", "5.00")
	expectNewPendingCardPayment(mock)

	var providerPaymentID string
	server := &PurserServer{
		db: db, logger: logging.NewLogger(),
		invoiceCardCheckout: func(_ context.Context, paymentID, invoiceID, tenantID string, amount decimal.Decimal, currency, returnURL string) (string, string, error) {
			providerPaymentID = paymentID
			if invoiceID != paymentInvoiceID || tenantID != paymentTenantID || !amount.Equal(decimal.RequireFromString("15.00")) || currency != "EUR" || returnURL != "/account/billing/return" {
				t.Fatalf("provider checkout arguments = %q/%q/%s/%s/%q", invoiceID, tenantID, amount, currency, returnURL)
			}
			return "https://checkout.example.test/new", "cs_new", nil
		},
	}
	mock.ExpectExec("-- name: AttachCardCheckoutToPendingPayment").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), capturedPaymentIDArgument{paymentID: &providerPaymentID}).
		WillReturnResult(sqlmock.NewResult(0, 1))

	response, err := server.CreatePayment(paymentTenantContext(), &purserpb.PaymentRequest{InvoiceId: paymentInvoiceID, Method: "card", ReturnUrl: "/account/billing/return"})
	if err != nil {
		t.Fatal(err)
	}
	if providerPaymentID == "" || response.GetId() != providerPaymentID || response.GetPaymentUrl() != "https://checkout.example.test/new" || response.GetAmount() != 15 || response.GetMethod() != "card" {
		t.Fatalf("created payment = %+v, provider id=%q", response, providerPaymentID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreatePaymentProviderFailureReleasesDurableReservation(t *testing.T) {
	configureCardOnlyPayments(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	expectInvoicePaymentExpiration(mock)
	expectInvoicePaymentLockAndBalance(mock, "20.00", "5.00")
	expectNewPendingCardPayment(mock)

	var providerPaymentID string
	server := &PurserServer{
		db: db, logger: logging.NewLogger(),
		invoiceCardCheckout: func(_ context.Context, paymentID, _, _ string, _ decimal.Decimal, _, _ string) (string, string, error) {
			providerPaymentID = paymentID
			return "", "", errors.New("provider unavailable")
		},
	}
	mock.ExpectExec("-- name: MarkPendingInvoicePaymentFailed").
		WithArgs(capturedPaymentIDArgument{paymentID: &providerPaymentID}).
		WillReturnResult(sqlmock.NewResult(0, 1))

	_, err = server.CreatePayment(paymentTenantContext(), &purserpb.PaymentRequest{InvoiceId: paymentInvoiceID, Method: "card"})
	if status.Code(err) != codes.Internal || providerPaymentID == "" {
		t.Fatalf("provider failure = %v, payment id=%q", err, providerPaymentID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreatePaymentRejectsAmbiguousOrStalePendingIntents(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		amount   string
		count    int32
		wantCode codes.Code
	}{
		{name: "multiple active intents", method: "card", amount: "15.00", count: 2, wantCode: codes.FailedPrecondition},
		{name: "different method already reserved", method: "crypto_eth", amount: "15.00", count: 1, wantCode: codes.FailedPrecondition},
		{name: "reserved amount is stale", method: "card", amount: "14.99", count: 1, wantCode: codes.FailedPrecondition},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configureCardOnlyPayments(t)
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			expectInvoicePaymentExpiration(mock)
			expectInvoicePaymentLockAndBalance(mock, "20.00", "5.00")
			mock.ExpectQuery("-- name: GetActiveInvoicePayment").WithArgs(paymentInvoiceID, paymentTenantID).
				WillReturnRows(activeInvoicePaymentRows(test.method, test.amount, nil, test.count))
			mock.ExpectRollback()

			_, err = (&PurserServer{db: db, logger: logging.NewLogger()}).CreatePayment(paymentTenantContext(), &purserpb.PaymentRequest{InvoiceId: paymentInvoiceID, Method: "card"})
			if status.Code(err) != test.wantCode {
				t.Fatalf("code = %v, want %v (err=%v)", status.Code(err), test.wantCode, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCreatePaymentRejectsSettledAndForeignInvoices(t *testing.T) {
	t.Run("no outstanding balance", func(t *testing.T) {
		configureCardOnlyPayments(t)
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		expectInvoicePaymentExpiration(mock)
		expectInvoicePaymentLockAndBalance(mock, "10.00", "10.00")
		mock.ExpectQuery("-- name: GetActiveInvoicePayment").WithArgs(paymentInvoiceID, paymentTenantID).WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()
		_, err = (&PurserServer{db: db, logger: logging.NewLogger()}).CreatePayment(paymentTenantContext(), &purserpb.PaymentRequest{InvoiceId: paymentInvoiceID, Method: "card"})
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("code = %v (err=%v)", status.Code(err), err)
		}
	})

	t.Run("invoice not tenant payable", func(t *testing.T) {
		configureCardOnlyPayments(t)
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		expectInvoicePaymentExpiration(mock)
		mock.ExpectBegin()
		mock.ExpectExec("-- name: LockInvoicePaymentCreation").WithArgs(paymentInvoiceID).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery("-- name: GetPayableInvoiceBalance").WithArgs(paymentInvoiceID, paymentTenantID).WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()
		_, err = (&PurserServer{db: db, logger: logging.NewLogger()}).CreatePayment(paymentTenantContext(), &purserpb.PaymentRequest{InvoiceId: paymentInvoiceID, Method: "card"})
		if status.Code(err) != codes.NotFound {
			t.Fatalf("code = %v (err=%v)", status.Code(err), err)
		}
	})
}

func TestCreatePaymentRequiresAuthenticatedTenantAndAvailableMethod(t *testing.T) {
	configureCardOnlyPayments(t)
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &PurserServer{db: db, logger: logging.NewLogger()}
	_, err = server.CreatePayment(context.Background(), &purserpb.PaymentRequest{InvoiceId: paymentInvoiceID, Method: "card"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("missing tenant code = %v", status.Code(err))
	}
	_, err = server.CreatePayment(paymentTenantContext(), &purserpb.PaymentRequest{InvoiceId: paymentInvoiceID, Method: "bank_transfer"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unavailable method code = %v", status.Code(err))
	}
}
