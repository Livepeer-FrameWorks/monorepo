package grpc

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fwdb "github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

type recordedStringArgument struct{ values *[]string }

func (argument recordedStringArgument) Match(value driver.Value) bool {
	actual, ok := value.(string)
	if ok {
		*argument.values = append(*argument.values, actual)
	}
	return ok
}

func TestCreatePaymentReplaysSerializationFailureAndStartsCheckoutOnce(t *testing.T) {
	configureCardOnlyPayments(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var attemptedPaymentIDs []string
	expectInvoicePaymentExpiration(mock)

	expectInvoicePaymentLockAndBalance(mock, "20.00", "5.00")
	mock.ExpectQuery("-- name: GetActiveInvoicePayment").WithArgs(paymentInvoiceID, paymentTenantID).WillReturnError(sql.ErrNoRows)
	expectInvoicePaymentFXBasis(mock)
	mock.ExpectExec("-- name: CreatePendingInvoicePayment").
		WithArgs(recordedStringArgument{values: &attemptedPaymentIDs}, paymentInvoiceID, "card", "15.00", "EUR", "", sqlmock.AnyArg(),
			int64(1500), int64(1500), "1", "identity", sqlmock.AnyArg()).
		WillReturnError(&pq.Error{Code: "40001", Message: "restart read required"})
	mock.ExpectRollback()

	expectInvoicePaymentLockAndBalance(mock, "20.00", "5.00")
	mock.ExpectQuery("-- name: GetActiveInvoicePayment").WithArgs(paymentInvoiceID, paymentTenantID).WillReturnError(sql.ErrNoRows)
	expectInvoicePaymentFXBasis(mock)
	mock.ExpectExec("-- name: CreatePendingInvoicePayment").
		WithArgs(recordedStringArgument{values: &attemptedPaymentIDs}, paymentInvoiceID, "card", "15.00", "EUR", "", sqlmock.AnyArg(),
			int64(1500), int64(1500), "1", "identity", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	created := expectDomainEvent(mock, "billing.payment_created", paymentTenantID)
	mock.ExpectQuery("-- name: EnqueueBillingEventOutbox").
		WithArgs(sameEventID{created}, "payment_created", paymentTenantID, "user-1", "payment", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("84000000-0000-4000-8000-000000000001"))
	mock.ExpectCommit()

	// capturedPaymentIDArgument reads committedPaymentID at match time, which
	// is after the provider call has recorded the committed attempt's id.
	var committedPaymentID string
	mock.ExpectExec("-- name: AttachCardCheckoutToPendingPayment").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), capturedPaymentIDArgument{paymentID: &committedPaymentID}).
		WillReturnResult(sqlmock.NewResult(0, 1))

	var checkoutPaymentIDs []string
	server := &PurserServer{
		db: db, logger: logging.NewLogger(),
		invoiceCardCheckout: func(_ context.Context, paymentID, _, _ string, _ decimal.Decimal, _, _ string) (string, string, error) {
			checkoutPaymentIDs = append(checkoutPaymentIDs, paymentID)
			committedPaymentID = attemptedPaymentIDs[len(attemptedPaymentIDs)-1]
			return "https://checkout.example.test/retried", "cs_retried", nil
		},
	}

	response, err := server.CreatePayment(paymentTenantContext(), &purserpb.PaymentRequest{InvoiceId: paymentInvoiceID, Method: "card"})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if len(attemptedPaymentIDs) != 2 {
		t.Fatalf("payment insert attempts = %d, want 2", len(attemptedPaymentIDs))
	}
	if len(checkoutPaymentIDs) != 1 {
		t.Fatalf("provider checkout calls = %d, want exactly 1", len(checkoutPaymentIDs))
	}
	if checkoutPaymentIDs[0] != attemptedPaymentIDs[1] || response.GetId() != attemptedPaymentIDs[1] {
		t.Fatalf("checkout payment %q / response %q, want committed attempt %q", checkoutPaymentIDs[0], response.GetId(), attemptedPaymentIDs[1])
	}
	if response.GetPaymentUrl() != "https://checkout.example.test/retried" {
		t.Fatalf("payment url = %q", response.GetPaymentUrl())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRecordBalanceTransactionExistingReferenceRollsBack(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const tenantID = "85000000-0000-4000-8000-000000000001"
	referenceID := "86000000-0000-4000-8000-000000000001"
	referenceType := "stripe_checkout"
	existingID := "87000000-0000-4000-8000-000000000001"

	mock.ExpectBegin()
	mock.ExpectExec("-- name: EnsurePrepaidBalanceRow").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("-- name: InsertReferencedBalanceTransaction").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("-- name: GetBalanceTransactionByReference").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "amount_cents", "balance_after_cents", "transaction_type", "description",
			"reference_id", "reference_type", "created_at",
		}).AddRow(existingID, tenantID, 500, 1500, "topup", "Top-up", referenceID, referenceType, nil))
	mock.ExpectRollback()

	server := &PurserServer{db: db, logger: logging.NewLogger()}
	txn, err := server.recordBalanceTransaction(context.Background(), tenantID, "EUR", 500, "topup", "Top-up", &referenceID, &referenceType)
	if err != nil {
		t.Fatalf("recordBalanceTransaction: %v", err)
	}
	if txn.GetId() != existingID || txn.GetBalanceAfterCents() != 1500 {
		t.Fatalf("existing transaction = %+v", txn)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestTxStatusErrorKeepsRetryCauseAndGRPCStatus(t *testing.T) {
	cause := &pq.Error{Code: "40001"}
	err := txStatusErrorf(cause, codes.Internal, "store pending payment: %v", cause)
	if !fwdb.IsRetryablePostgresError(err) {
		t.Fatal("txStatusError hides the retryable SQLSTATE from the retry helper")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Internal || st.Message() != "store pending payment: "+cause.Error() {
		t.Fatalf("status = %v (ok=%v)", st, ok)
	}
	if !errors.Is(err, cause) {
		t.Fatal("txStatusError does not unwrap to its cause")
	}
}
