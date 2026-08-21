//go:build schema_verify

package handlers

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestInvoiceCollectionMinimumSerializesAndPersists_RealPG(t *testing.T) { //nolint:funlen // The concurrent transaction is the contract under test.
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	tenantID := uuid.NewString()
	invoiceIDs := []string{uuid.NewString(), uuid.NewString()}
	for _, invoiceID := range invoiceIDs {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.billing_invoices (id, tenant_id, status, currency, amount, due_date)
			VALUES ($1, $2, 'pending', 'EUR', 3.00, $3)
		`, invoiceID, tenantID, time.Now().UTC().Add(14*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	start := make(chan struct{})
	decisions := make([]invoiceCollectionDecision, len(invoiceIDs))
	errs := make([]error, len(invoiceIDs))
	var wg sync.WaitGroup
	for index, invoiceID := range invoiceIDs {
		wg.Add(1)
		go func(index int, invoiceID string) {
			defer wg.Done()
			<-start
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				errs[index] = err
				return
			}
			decision, err := applyInvoiceCollectionMinimumTx(ctx, tx, tenantID, "stripe", "eur", 300)
			if err == nil {
				err = persistInvoiceCollectionDecisionTx(ctx, tx, invoiceID, tenantID, decision)
			}
			if err == nil {
				err = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
			decisions[index] = decision
			errs[index] = err
		}(index, invoiceID)
	}
	close(start)
	wg.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("transaction %d: %v", index, err)
		}
	}

	outcomes := []string{decisions[0].Outcome, decisions[1].Outcome}
	sort.Strings(outcomes)
	if outcomes[0] != "collected" || outcomes[1] != "deferred" {
		t.Fatalf("outcomes = %v, decisions = %+v", outcomes, decisions)
	}
	var balance int64
	if err := db.QueryRowContext(ctx, `
		SELECT balance_cents FROM purser.billing_collection_balances
		WHERE tenant_id = $1 AND currency = 'EUR'
	`, tenantID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 0 {
		t.Fatalf("closing carry balance = %d, want 0", balance)
	}

	var entryCount int
	var collectedTotal, mathResidual int64
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*), SUM(collected_cents),
		       SUM(opening_balance_cents + current_charge_cents - collected_cents - closing_balance_cents)
		FROM purser.billing_collection_entries
		WHERE tenant_id = $1
	`, tenantID).Scan(&entryCount, &collectedTotal, &mathResidual); err != nil {
		t.Fatal(err)
	}
	if entryCount != 2 || collectedTotal != 600 || mathResidual != 0 {
		t.Fatalf("entries count/collected/residual = %d/%d/%d", entryCount, collectedTotal, mathResidual)
	}

	var lineCount int
	var lineTotal string
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*), SUM(amount)::text
		FROM purser.invoice_line_items
		WHERE tenant_id = $1 AND line_key IN ('collection_balance_deferred', 'collection_balance_opening')
	`, tenantID).Scan(&lineCount, &lineTotal); err != nil {
		t.Fatal(err)
	}
	if lineCount != 2 || lineTotal != "0.00" {
		t.Fatalf("collection lines count/total = %d/%s", lineCount, lineTotal)
	}
}
