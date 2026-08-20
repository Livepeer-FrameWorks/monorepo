package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestEnqueueInvoiceEmailOnlyForPermanentInvoice(t *testing.T) {
	db, mock, setupErr := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if setupErr != nil {
		t.Fatalf("sqlmock: %v", setupErr)
	}
	defer db.Close()

	for _, status := range []string{"draft", "manual_review"} {
		mock.ExpectBegin()
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("begin %s: %v", status, err)
		}
		if err := enqueueInvoiceEmailTx(context.Background(), tx, "invoice-1", "tenant-1", "billing@example.com", status); err != nil {
			t.Fatalf("enqueue %s: %v", status, err)
		}
		mock.ExpectCommit()
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit %s: %v", status, err)
		}
	}

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO purser\.invoice_email_outbox`).
		WithArgs("invoice-1", "tenant-1", "billing@example.com").
		WillReturnResult(sqlmock.NewResult(0, 1))
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin pending: %v", err)
	}
	if err := enqueueInvoiceEmailTx(context.Background(), tx, "invoice-1", "tenant-1", " billing@example.com ", "pending"); err != nil {
		t.Fatalf("enqueue pending: %v", err)
	}
	mock.ExpectCommit()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit pending: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestInvoiceEmailDispatcherReadsPermanentHeaderAndLineItems(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	dueDate := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`FROM purser\.billing_invoices\s+WHERE id = \$1 AND tenant_id = \$2`).
		WithArgs("invoice-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"amount", "metered_amount", "gross_metered_amount", "currency", "due_date", "status",
		}).AddRow(19.75, 9.75, 9.75, "EUR", dueDate, "pending"))
	mock.ExpectQuery(`FROM purser\.invoice_line_items\s+WHERE invoice_id = \$1 AND tenant_id = \$2`).
		WithArgs("invoice-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"description", "unit", "dimensions", "cluster_id", "cluster_kind",
			"quantity", "unit_price", "amount", "currency", "pricing_source",
		}).AddRow("H.264 transcode", "second", []byte(`{"output_codec":"h264"}`), "cluster-a", "platform_official", "3600", "0.0025", "9.00", "EUR", "cluster_metered"))

	var delivered []EmailInvoiceLineItem
	dispatcher := &invoiceEmailDispatcher{
		jobs: &JobManager{db: db},
		send: func(recipient, invoiceID string, amount, meteredAmount, grossMeteredAmount float64, currency string, gotDueDate time.Time, lineItems []EmailInvoiceLineItem) error {
			if recipient != "billing@example.com" || invoiceID != "invoice-1" || amount != 19.75 || currency != "EUR" || !gotDueDate.Equal(dueDate) {
				t.Errorf("unexpected email header: recipient=%s invoice=%s amount=%v currency=%s due=%s", recipient, invoiceID, amount, currency, gotDueDate)
			}
			delivered = lineItems
			return nil
		},
	}
	failed, err := dispatcher.Dispatch(context.Background(), invoiceEmailPayload{
		InvoiceID: "invoice-1", TenantID: "tenant-1", Recipient: "billing@example.com",
	})
	if err != nil || len(failed) != 0 {
		t.Fatalf("Dispatch = (%v, %v)", failed, err)
	}
	if len(delivered) != 1 || delivered[0].Unit != "second" || delivered[0].DimensionLabel != "output codec: h264" {
		t.Fatalf("delivered line items = %+v", delivered)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestInvoiceEmailOutboxClaimLeasesRowsForHorizontalWorkers(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM purser\.invoice_email_outbox[\s\S]+FOR UPDATE SKIP LOCKED`).
		WithArgs(int64(120000), 10).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "invoice_id", "tenant_id", "recipient", "notification_type", "reminder_stage", "attempts",
		}).AddRow("outbox-1", "invoice-1", "tenant-1", "billing@example.com", "overdue_reminder", 7, 2))
	mock.ExpectExec(`UPDATE purser\.invoice_email_outbox[\s\S]+WHERE id = \$1 AND tenant_id = \$2`).
		WithArgs("outbox-1", "tenant-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	claims, err := (&invoiceEmailOutboxStore{db: db}).ClaimBatch(context.Background(), 10, 2*time.Minute)
	if err != nil {
		t.Fatalf("ClaimBatch: %v", err)
	}
	if len(claims) != 1 || claims[0].ID != "tenant-1/outbox-1" || claims[0].LeaseToken == "" || claims[0].Attempts != 2 {
		t.Fatalf("claims = %+v", claims)
	}
	if claims[0].Payload.InvoiceID != "invoice-1" || claims[0].Payload.Recipient != "billing@example.com" ||
		claims[0].Payload.NotificationType != overdueReminderNotification || claims[0].Payload.ReminderStage != 7 {
		t.Fatalf("claim payload = %+v", claims[0].Payload)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestInvoiceEmailDispatcherSendsOutstandingOverdueAmount(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	dueDate := time.Now().Add(-8 * 24 * time.Hour)
	mock.ExpectQuery(`SELECT GREATEST[\s\S]+FROM purser\.billing_invoices bi[\s\S]+WHERE bi\.id = \$1 AND bi\.tenant_id = \$2`).
		WithArgs("invoice-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"amount_due", "currency", "due_date", "status", "latest_reminder_stage"}).
			AddRow(4.25, "EUR", dueDate, "overdue", 7))

	called := false
	dispatcher := &invoiceEmailDispatcher{
		jobs: &JobManager{db: db},
		sendReminder: func(recipient, invoiceID string, amount float64, currency string, daysPastDue int) error {
			called = true
			if recipient != "billing@example.com" || invoiceID != "invoice-1" || amount != 4.25 || currency != "EUR" || daysPastDue < 7 {
				t.Fatalf("unexpected reminder: recipient=%s invoice=%s amount=%v currency=%s days=%d", recipient, invoiceID, amount, currency, daysPastDue)
			}
			return nil
		},
	}
	failed, err := dispatcher.Dispatch(context.Background(), invoiceEmailPayload{
		InvoiceID:        "invoice-1",
		TenantID:         "tenant-1",
		Recipient:        "billing@example.com",
		NotificationType: overdueReminderNotification,
		ReminderStage:    7,
	})
	if err != nil || len(failed) != 0 || !called {
		t.Fatalf("Dispatch = (%v, %v), called=%v", failed, err, called)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestInvoiceEmailDispatcherSkipsSettledReminder(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT GREATEST[\s\S]+FROM purser\.billing_invoices bi`).
		WithArgs("invoice-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"amount_due", "currency", "due_date", "status", "latest_reminder_stage"}).
			AddRow(0, "EUR", time.Now().Add(-8*24*time.Hour), "paid", 7))
	dispatcher := &invoiceEmailDispatcher{
		jobs: &JobManager{db: db},
		sendReminder: func(string, string, float64, string, int) error {
			t.Fatal("settled reminder must not be sent")
			return nil
		},
	}
	failed, err := dispatcher.Dispatch(context.Background(), invoiceEmailPayload{
		InvoiceID: "invoice-1", TenantID: "tenant-1", NotificationType: overdueReminderNotification, ReminderStage: 7,
	})
	if err != nil || len(failed) != 0 {
		t.Fatalf("Dispatch = (%v, %v)", failed, err)
	}
}

func TestInvoiceEmailDispatcherSkipsSupersededReminderStage(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery(`SELECT GREATEST[\s\S]+FROM purser\.billing_invoices bi`).
		WithArgs("invoice-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"amount_due", "currency", "due_date", "status", "latest_reminder_stage"}).
			AddRow(4.25, "EUR", time.Now().Add(-8*24*time.Hour), "overdue", 7))
	dispatcher := &invoiceEmailDispatcher{
		jobs: &JobManager{db: db},
		sendReminder: func(string, string, float64, string, int) error {
			t.Fatal("superseded stage must not be sent")
			return nil
		},
	}
	failed, err := dispatcher.Dispatch(context.Background(), invoiceEmailPayload{
		InvoiceID: "invoice-1", TenantID: "tenant-1", NotificationType: overdueReminderNotification, ReminderStage: 1,
	})
	if err != nil || len(failed) != 0 {
		t.Fatalf("Dispatch = (%v, %v)", failed, err)
	}
}
