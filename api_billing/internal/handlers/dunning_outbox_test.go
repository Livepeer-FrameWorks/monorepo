package handlers

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestSendPaymentRemindersStagesDurableDunning(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(`UPDATE purser\.billing_invoices[\s\S]+status = 'pending' AND due_date < NOW\(\)`).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(`INSERT INTO purser\.invoice_email_outbox[\s\S]+UNNEST\(ARRAY\[1, 7, 14, 30\]\)[\s\S]+ON CONFLICT \(invoice_id, notification_type, reminder_stage\) DO NOTHING`).
		WillReturnResult(sqlmock.NewResult(0, 1))

	jm := &JobManager{db: db, logger: logging.NewLogger()}
	jm.sendPaymentReminders(context.Background())
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
