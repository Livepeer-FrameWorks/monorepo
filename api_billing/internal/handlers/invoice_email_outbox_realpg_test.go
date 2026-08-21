//go:build schema_verify

package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestInvoiceEmailOutboxLifecycleAndReads_RealPG(t *testing.T) { //nolint:funlen // One engine fixture proves the full lease and dispatch contract.
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	tenantID := uuid.NewString()
	invoiceID := uuid.NewString()
	dueDate := time.Now().UTC().Add(-8 * 24 * time.Hour).Truncate(time.Second)

	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.billing_invoices (
			id, tenant_id, status, currency, amount, metered_amount,
			gross_metered_amount, due_date
		) VALUES ($1, $2, 'overdue', 'EUR', 19.75, 9.75, 10.25, $3)
	`, invoiceID, tenantID, dueDate); err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.invoice_line_items (
			id, invoice_id, tenant_id, line_key, meter, unit, dimensions,
			description, quantity, billable_quantity, unit_price, amount,
			currency, cluster_id, cluster_kind, pricing_source
		) VALUES (
			$1, $2, $3, 'transcode:h264', 'transcode_seconds', 'second',
			'{"output_codec":"h264"}'::jsonb, 'H.264 transcode', 3600, 3600,
			0.0025, 9.00, 'EUR', 'cluster-a', 'platform_official', 'cluster_metered'
		)
	`, uuid.NewString(), invoiceID, tenantID); err != nil {
		t.Fatalf("seed line item: %v", err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := enqueueInvoiceEmailTx(ctx, tx, invoiceID, tenantID, " billing@example.com ", "pending"); err != nil {
			_ = tx.Rollback()
			t.Fatalf("enqueue attempt %d: %v", attempt, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	var rowCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.invoice_email_outbox WHERE invoice_id = $1`, invoiceID).Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != 1 {
		t.Fatalf("idempotent enqueue count = %d, want 1", rowCount)
	}

	store := &invoiceEmailOutboxStore{db: db}
	claims, err := store.ClaimBatch(ctx, 10, 2*time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claims) != 1 || claims[0].Payload.InvoiceID != invoiceID || claims[0].Payload.TenantID != tenantID || claims[0].LeaseToken == "" {
		t.Fatalf("claims = %+v", claims)
	}
	if duplicate, err := store.ClaimBatch(ctx, 10, 2*time.Minute); err != nil || len(duplicate) != 0 {
		t.Fatalf("leased duplicate claim = (%+v, %v)", duplicate, err)
	}
	if err := store.MarkCompletedToken(ctx, claims[0].ID, uuid.NewString()); err == nil {
		t.Fatal("stale lease token completed an owned row")
	}
	if err := store.RecordFailureToken(ctx, claims[0].ID, claims[0].Attempts, nil, errors.New("smtp unavailable"), 0, claims[0].LeaseToken); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	var attempts int
	var lastError string
	if err := db.QueryRowContext(ctx, `
		SELECT attempts, last_error FROM purser.invoice_email_outbox WHERE invoice_id = $1
	`, invoiceID).Scan(&attempts, &lastError); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || lastError != "smtp unavailable" {
		t.Fatalf("attempts/error = %d/%q", attempts, lastError)
	}

	reclaimed, err := store.ClaimBatch(ctx, 10, 2*time.Minute)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].Attempts != 1 || reclaimed[0].LeaseToken == claims[0].LeaseToken {
		t.Fatalf("reclaimed = %+v", reclaimed)
	}

	jobs := &JobManager{db: db}
	var sentItems []EmailInvoiceLineItem
	dispatcher := &invoiceEmailDispatcher{
		jobs: jobs,
		send: func(recipient, gotInvoiceID string, amount, meteredAmount, grossMeteredAmount float64, currency string, gotDueDate time.Time, lineItems []EmailInvoiceLineItem) error {
			if recipient != "billing@example.com" || gotInvoiceID != invoiceID || amount != 19.75 || meteredAmount != 9.75 || grossMeteredAmount != 10.25 || currency != "EUR" || !gotDueDate.Equal(dueDate) {
				t.Fatalf("email header = %s/%s/%v/%v/%v/%s/%s", recipient, gotInvoiceID, amount, meteredAmount, grossMeteredAmount, currency, gotDueDate)
			}
			sentItems = lineItems
			return nil
		},
	}
	failed, err := dispatcher.Dispatch(ctx, reclaimed[0].Payload)
	if err != nil || len(failed) != 0 {
		t.Fatalf("dispatch = (%v, %v)", failed, err)
	}
	if len(sentItems) != 1 || sentItems[0].DimensionLabel != "output codec: h264" || sentItems[0].ClusterID != "cluster-a" {
		t.Fatalf("email line items = %+v", sentItems)
	}
	if err := store.MarkCompletedToken(ctx, reclaimed[0].ID, reclaimed[0].LeaseToken); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if completed, err := store.ClaimBatch(ctx, 10, 0); err != nil || len(completed) != 0 {
		t.Fatalf("completed claim = (%+v, %v)", completed, err)
	}
}

func TestInvoiceEmailOverdueBalanceRead_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	tenantID := uuid.NewString()
	invoiceID := uuid.NewString()
	dueDate := time.Now().UTC().Add(-8 * 24 * time.Hour)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.billing_invoices (id, tenant_id, status, currency, amount, due_date)
		VALUES ($1, $2, 'overdue', 'EUR', 19.75, $3)
	`, invoiceID, tenantID, dueDate); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.billing_payments (
			id, invoice_id, method, amount, currency, tx_id, status
		) VALUES ($1, $2, 'card', 15.50, 'EUR', $3, 'confirmed')
	`, uuid.NewString(), invoiceID, "payment-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}

	called := false
	dispatcher := &invoiceEmailDispatcher{
		jobs: &JobManager{db: db},
		sendReminder: func(recipient, gotInvoiceID string, amount float64, currency string, daysPastDue int) error {
			called = true
			if recipient != "billing@example.com" || gotInvoiceID != invoiceID || amount != 4.25 || currency != "EUR" || daysPastDue < 7 {
				t.Fatalf("reminder = %s/%s/%v/%s/%d", recipient, gotInvoiceID, amount, currency, daysPastDue)
			}
			return nil
		},
	}
	failed, err := dispatcher.Dispatch(ctx, invoiceEmailPayload{
		InvoiceID: invoiceID, TenantID: tenantID, Recipient: "billing@example.com",
		NotificationType: overdueReminderNotification, ReminderStage: 7,
	})
	if err != nil || len(failed) != 0 || !called {
		t.Fatalf("overdue dispatch = (%v, %v), called=%v", failed, err, called)
	}
}
