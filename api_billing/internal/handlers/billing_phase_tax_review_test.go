package handlers

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	billingtax "frameworks/api_billing/internal/tax"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

func TestPhaseReviewIsRecordedExplicitlyAfterTransactionReturns(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	refusal := &billingPhaseTaxReviewError{
		BillingBoundaryCondition: &BillingBoundaryCondition{Reason: "requires tax review"},
		db:                       db, tenant: "tenant", start: time.Now().UTC(), net: 100,
		review: billingtax.ReviewError{Code: "billing_profile_required", Message: "Complete billing details"},
	}
	mock.ExpectBegin()
	mock.ExpectRollback()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err = database.WithPostgresTx(ctx, db, nil, func(*sql.Tx) error { return refusal })
	if !errors.Is(err, refusal) {
		t.Fatalf("transaction lost the business refusal: %v", err)
	}
	if checkErr := mock.ExpectationsWereMet(); checkErr != nil {
		t.Fatalf("transaction performed unexpected business writes: %v", checkErr)
	}
	cancel()
	mock.ExpectExec("-- name: RecordCryptoAccountingAnomaly").WillReturnResult(sqlmock.NewResult(0, 1))
	if err = RecordBillingPhaseTaxReview(ctx, err); !errors.Is(err, refusal) {
		t.Fatalf("explicit caller lost the refusal: %v", err)
	}
	if checkErr := mock.ExpectationsWereMet(); checkErr != nil {
		t.Fatal(checkErr)
	}
}

func TestPhaseReviewRecordPreservesFailureWithoutHandlingOtherErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	noticeFailure := errors.New("record unavailable")
	refusal := &billingPhaseTaxReviewError{BillingBoundaryCondition: &BillingBoundaryCondition{Reason: "requires tax review"}, db: db}
	mock.ExpectExec("-- name: RecordCryptoAccountingAnomaly").WillReturnError(noticeFailure)
	result := RecordBillingPhaseTaxReview(context.Background(), refusal)
	if !errors.Is(result, refusal) || !errors.Is(result, noticeFailure) {
		t.Fatalf("record failure lost either cause: %v", result)
	}
	other := errors.New("other refusal")
	if !errors.Is(RecordBillingPhaseTaxReview(t.Context(), other), other) || RecordBillingPhaseTaxReview(t.Context(), nil) != nil {
		t.Fatal("phase-review handler changed unrelated transaction outcomes")
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
