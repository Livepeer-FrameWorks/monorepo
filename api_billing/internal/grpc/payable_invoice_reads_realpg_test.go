//go:build schema_verify

package grpc

import (
	"context"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/google/uuid"
)

func TestPayableInvoicesDeductOnlyOwnedConfirmedCurrencyPayments_RealPG(t *testing.T) {
	db := startPurserTransitionRealPG(t)
	ctx := context.Background()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	tenant, other := uuid.NewString(), uuid.NewString()
	due := time.Now().UTC().Truncate(time.Second)
	invoice := func(owner, state string, amount int64) string {
		t.Helper()
		id := uuid.NewString()
		exec(`INSERT INTO purser.billing_invoices(id,tenant_id,status,currency,amount,due_date)
 VALUES($1,$2,$3,'EUR',$4,$5)`, id, owner, state, amount, due)
		return id
	}
	payment := func(id, currency, state string, amount, reversed int64) string {
		t.Helper()
		pid := uuid.NewString()
		exec(`INSERT INTO purser.billing_payments(id,invoice_id,method,amount,currency,status,reversed_amount_cents,
 original_amount_cents,original_currency,eur_amount_cents,fx_units_per_eur,fx_source,fx_reference_date)
 VALUES($1::uuid,$2::uuid,'card',$3::bigint,$4::text,$5::text,$6::bigint,
 $3::bigint*100,$4::text,$3::bigint*100,1,
 CASE WHEN $4::text='EUR' THEN 'identity' ELSE 'ecb' END,CURRENT_DATE)`, pid, id, amount, currency, state, reversed)
		return pid
	}
	partPaid := invoice(tenant, "pending", 100)
	untouched := invoice(tenant, "overdue", 75)
	legacy := invoice(tenant, "pending", 28)
	payment(partPaid, "EUR", "confirmed", 12, 200)
	payment(partPaid, "EUR", "failed", 22, 0)
	payment(partPaid, "USD", "confirmed", 120, 0)
	legacyPayment := payment(legacy, "EUR", "confirmed", 8, 0)
	payment(invoice(tenant, "paid", 50), "EUR", "confirmed", 50, 0)
	payment(invoice(other, "pending", 900), "EUR", "confirmed", 500, 0)

	// Between expand and backfill, historical payments still have no owner column.
	exec(`ALTER TABLE purser.billing_payments DROP CONSTRAINT billing_payment_tenant_required`)
	exec(`ALTER TABLE purser.billing_payments DISABLE TRIGGER billing_payment_tenant_guard`)
	exec(`UPDATE purser.billing_payments SET tenant_id=NULL WHERE id=$1`, legacyPayment)
	exec(`ALTER TABLE purser.billing_payments ENABLE TRIGGER billing_payment_tenant_guard`)
	exec(`ALTER TABLE purser.billing_payments ADD CONSTRAINT billing_payment_tenant_required CHECK(tenant_id IS NOT NULL) NOT VALID`)

	rows, err := purserdb.New(db).ListPayableInvoices(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{partPaid: 90, untouched: 75, legacy: 20}
	if len(rows) != len(want) {
		t.Fatalf("got %d payable invoices, want %d", len(rows), len(want))
	}
	for i, row := range rows {
		expected, ok := want[row.ID.String()]
		if !ok || row.AmountDue != expected || row.TenantID.String() != tenant {
			t.Fatalf("incorrect payable invoice: %+v, expected amount %v", row, expected)
		}
		if i > 0 && rows[i-1].ID.String() > row.ID.String() {
			t.Fatal("equal-due-date invoices are not ordered by UUID")
		}
	}
}
