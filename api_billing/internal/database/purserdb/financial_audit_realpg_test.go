//go:build schema_verify

package purserdb

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func assertFinancialAuditRetainsUndatedEvidence(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	const tenant = "93000000-0000-0000-0000-000000000001"
	const otherTenant = "93000000-0000-0000-0000-000000000002"
	const undated = "93000000-0000-0000-0000-000000000003"
	for _, record := range []struct{ id, tenant string }{
		{undated, tenant},
		{"93000000-0000-0000-0000-000000000004", otherTenant},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO purser.balance_transactions
			(id, tenant_id, amount_cents, balance_after_cents, transaction_type, created_at)
			VALUES ($1, $2, 100, 100, 'topup', NULL)`, record.id, record.tenant); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.balance_transactions
		(id, tenant_id, amount_cents, balance_after_cents, transaction_type, created_at)
		VALUES ($1, $2, 100, 100, 'topup', '2000-01-01')`, "93000000-0000-0000-0000-000000000005", tenant); err != nil {
		t.Fatal(err)
	}
	rows, err := New(db).FinancialAuditPage(ctx, FinancialAuditPageParams{
		TenantID: tenant, Source: "balance_transactions", FromAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), ThroughAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != undated {
		t.Fatalf("audit must retain undated evidence within tenant scope; got %+v", rows)
	}
}

func TestFinancialAuditRetainsUndatedEvidence_RealPG(t *testing.T) {
	assertFinancialAuditRetainsUndatedEvidence(t, startQueryCatalogRealPG(t))
}
