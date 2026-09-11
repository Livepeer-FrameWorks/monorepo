//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// seedHalfPaidInvoice creates an invoice whose two pending payments together
// cover it exactly, and neither covers it alone — so the invoice settles only
// once both are confirmed, which is what makes the ordering matter.
func seedHalfPaidInvoice(t *testing.T, db *sql.DB) (invoiceID string, txIDs [2]string) {
	t.Helper()
	ctx := context.Background()
	invoiceID = uuid.NewString()
	tenantID := uuid.NewString()

	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.billing_invoices (id, tenant_id, status, currency, amount, due_date)
		VALUES ($1::uuid, $2::uuid, 'pending', 'EUR', 100.00, NOW() + INTERVAL '14 days')`, invoiceID, tenantID); err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	for i := range txIDs {
		txIDs[i] = uuid.NewString()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.billing_payments (invoice_id, method, amount, currency, tx_id, status)
			VALUES ($1::uuid, 'card', 50.00, 'EUR', $2, 'pending')`, invoiceID, txIDs[i]); err != nil {
			t.Fatalf("seed payment %d: %v", i, err)
		}
	}
	return invoiceID, txIDs
}

func invoiceStatus(t *testing.T, db *sql.DB, invoiceID string) string {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM purser.billing_invoices WHERE id=$1::uuid`, invoiceID).Scan(&status); err != nil {
		t.Fatalf("read invoice status: %v", err)
	}
	return status
}

// confirmInTx confirms one payment, then hands back the settlement step and the
// commit step separately so the test can hold a transaction open across another
// one — which is how two concurrent webhook handlers actually interleave.
func confirmInTx(t *testing.T, svc *Service, db *sql.DB, invoiceID, txID string) (settle, commit func() error) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE purser.billing_payments SET status='confirmed', confirmed_at=NOW(), updated_at=NOW()
		WHERE invoice_id=$1::uuid AND tx_id=$2`, invoiceID, txID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("confirm payment: %v", err)
	}
	queries := purserdb.New(tx)
	settle = func() error {
		if err := queries.LockInvoicePaymentCreation(ctx, invoiceID); err != nil {
			return err
		}
		return svc.settleInvoiceIfCovered(ctx, tx, queries, invoiceID, time.Now())
	}
	return settle, tx.Commit
}

// Two payment confirmations for the same invoice land concurrently, each in its
// own READ COMMITTED transaction — the ordinary case, because provider webhooks
// are separate HTTP handlers and Purser is multi-replica.
//
// Settlement asks an aggregate question over sibling payment rows. Unserialized,
// each transaction sees the other still pending, each concludes the invoice is
// short, the UPDATE's subselect guard matches zero rows so neither even takes a
// row lock, and both commit. The customer has paid in full and the invoice stays
// pending, then overdue, with nothing scheduled to look again and no error
// anywhere. This asserts it settles.
func TestInvoiceSettlementSurvivesConcurrentConfirmations_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	svc := &Service{db: db, logger: logrus.New()}
	invoiceID, txIDs := seedHalfPaidInvoice(t, db)

	settleA, commitA := confirmInTx(t, svc, db, invoiceID, txIDs[0])
	settleB, commitB := confirmInTx(t, svc, db, invoiceID, txIDs[1])

	// A settles first and keeps its transaction open, so it still holds the
	// invoice lock. At this point only half the invoice is confirmed.
	if err := settleA(); err != nil {
		t.Fatalf("first settlement: %v", err)
	}

	settledB := make(chan error, 1)
	go func() { settledB <- settleB() }()

	// B must not settle while A is mid-flight; if it can, both are reading a
	// partial picture and both will conclude the invoice is short.
	select {
	case err := <-settledB:
		t.Fatalf("second confirmation settled concurrently with the first (err=%v)", err)
	case <-time.After(500 * time.Millisecond):
	}

	if err := commitA(); err != nil {
		t.Fatalf("commit first confirmation: %v", err)
	}
	select {
	case err := <-settledB:
		if err != nil {
			t.Fatalf("second settlement: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second settlement never unblocked after the first committed")
	}
	if err := commitB(); err != nil {
		t.Fatalf("commit second confirmation: %v", err)
	}

	if status := invoiceStatus(t, db, invoiceID); status != "paid" {
		t.Fatalf("invoice status = %q after both payments confirmed; the customer paid in full", status)
	}
}

// A partially paid invoice must not settle. Without this the test above would
// also pass if settlement simply marked everything paid.
func TestInvoiceSettlementWaitsForFullCoverage_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	svc := &Service{db: db, logger: logrus.New()}
	invoiceID, txIDs := seedHalfPaidInvoice(t, db)

	settleA, commitA := confirmInTx(t, svc, db, invoiceID, txIDs[0])
	if err := settleA(); err != nil {
		t.Fatalf("first settlement: %v", err)
	}
	if err := commitA(); err != nil {
		t.Fatalf("commit first confirmation: %v", err)
	}
	if status := invoiceStatus(t, db, invoiceID); status != "pending" {
		t.Fatalf("invoice settled on half payment: status=%q", status)
	}

	settleB, commitB := confirmInTx(t, svc, db, invoiceID, txIDs[1])
	if err := settleB(); err != nil {
		t.Fatalf("second settlement: %v", err)
	}
	if err := commitB(); err != nil {
		t.Fatalf("commit second confirmation: %v", err)
	}
	if status := invoiceStatus(t, db, invoiceID); status != "paid" {
		t.Fatalf("invoice did not settle once fully covered: status=%q", status)
	}
}

// An invoice left unpaid by a lost settlement is rescued by the next redelivery
// of a webhook for a payment that is already confirmed — the only event that
// recurs for a settled invoice. Before, that path returned early without
// recomputing, so redelivery could not heal anything.
func TestInvoiceSettlementRecomputesOnAlreadyConfirmedReplay_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	svc := &Service{db: db, logger: logrus.New()}
	invoiceID, txIDs := seedHalfPaidInvoice(t, db)
	ctx := context.Background()

	// Both payments confirmed, invoice never settled: the stranded state.
	for _, txID := range txIDs {
		if _, err := db.ExecContext(ctx, `
			UPDATE purser.billing_payments SET status='confirmed', confirmed_at=NOW()
			WHERE invoice_id=$1::uuid AND tx_id=$2`, invoiceID, txID); err != nil {
			t.Fatalf("confirm payment: %v", err)
		}
	}
	if status := invoiceStatus(t, db, invoiceID); status != "pending" {
		t.Fatalf("fixture is not stranded: status=%q", status)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	queries := purserdb.New(tx)
	if err := queries.LockInvoicePaymentCreation(ctx, invoiceID); err != nil {
		t.Fatalf("lock invoice: %v", err)
	}
	if err := svc.settleInvoiceIfCovered(ctx, tx, queries, invoiceID, time.Now()); err != nil {
		t.Fatalf("recompute: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if status := invoiceStatus(t, db, invoiceID); status != "paid" {
		t.Fatalf("replay did not rescue the stranded invoice: status=%q", status)
	}
}
