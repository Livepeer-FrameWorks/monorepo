package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/api_billing/internal/handlers"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

const grantNoticeID = "00000000-0000-0000-0000-0000000000a1"

func expectClaimGrantNotice(mock sqlmock.Sqlmock, attempted, ends time.Time, email string) {
	mock.ExpectQuery(`-- name: ClaimOperatorGrantNotice`).WithArgs(grantNoticeID).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "kind", "ends_at", "attempted_at", "billing_email", "billing_name", "plan_name"}).
			AddRow("tenant-a", handlers.OperatorGrantNoticeEnding, ends, attempted, email, "Acme", "Developer"))
}

// A claimed notice goes to the billing email and is acknowledged with the
// claim's attempt time.
func TestDeliverOperatorGrantNoticeSendsAndCompletes(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	attempted, ends := time.Now().UTC(), time.Now().UTC().Add(72*time.Hour)
	expectClaimGrantNotice(mock, attempted, ends, "billing@example.com")
	mock.ExpectExec(`-- name: CompleteOperatorGrantNotice`).WithArgs(grantNoticeID, attempted).WillReturnResult(sqlmock.NewResult(0, 1))
	var sent []handlers.OperatorGrantNotice
	server := &PurserServer{db: db, logger: logging.NewLogger(), operatorGrantNoticeSender: func(n handlers.OperatorGrantNotice) error {
		sent = append(sent, n)
		return nil
	}}
	if err = server.deliverOperatorGrantNotice(context.Background(), grantNoticeID); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0].Recipient != "billing@example.com" || sent[0].Kind != handlers.OperatorGrantNoticeEnding ||
		!sent[0].EndsAt.Equal(ends) || sent[0].PlanName != "Developer" {
		t.Fatalf("sent = %+v", sent)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A failed send records its error against the same attempt and stays queued.
func TestDeliverOperatorGrantNoticeRecordsFailure(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	attempted := time.Now().UTC()
	expectClaimGrantNotice(mock, attempted, attempted.Add(time.Hour), "billing@example.com")
	mock.ExpectExec(`-- name: RecordOperatorGrantNoticeFailure`).WithArgs("recipient rejected", grantNoticeID, attempted).WillReturnResult(sqlmock.NewResult(0, 1))
	server := &PurserServer{db: db, logger: logging.NewLogger(), operatorGrantNoticeSender: func(handlers.OperatorGrantNotice) error {
		return errors.New("recipient rejected")
	}}
	if err = server.deliverOperatorGrantNotice(context.Background(), grantNoticeID); err == nil {
		t.Fatal("failed send reported success")
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A tenant without a billing email has no recipient; the notice completes
// without a send, as Free fallback notices do.
func TestDeliverOperatorGrantNoticeWithoutBillingEmailCompletes(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	attempted := time.Now().UTC()
	expectClaimGrantNotice(mock, attempted, attempted.Add(time.Hour), "")
	mock.ExpectExec(`-- name: CompleteOperatorGrantNotice`).WithArgs(grantNoticeID, attempted).WillReturnResult(sqlmock.NewResult(0, 1))
	server := &PurserServer{db: db, logger: logging.NewLogger(), operatorGrantNoticeSender: func(handlers.OperatorGrantNotice) error {
		t.Fatal("sent without a recipient")
		return nil
	}}
	if err = server.deliverOperatorGrantNotice(context.Background(), grantNoticeID); err != nil {
		t.Fatal(err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
