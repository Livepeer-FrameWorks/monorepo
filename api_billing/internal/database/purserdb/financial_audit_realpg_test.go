//go:build schema_verify

package purserdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
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

// financialAuditQueryCapture records the actual generated query and argument
// order so the plan scenario measures the same query used by the RPC.
type financialAuditQueryCapture struct {
	*sql.DB
	query string
	args  []any
}

func (c *financialAuditQueryCapture) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	c.query = query
	c.args = append([]any(nil), args...)
	return c.DB.QueryContext(ctx, query, args...)
}

func assertFinancialAuditSeeksBoundedUUIDPage(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	const tenant = "94000000-0000-0000-0000-000000000001"
	const other = "94000000-0000-0000-0000-000000000002"
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.balance_transactions
 (id,tenant_id,amount_cents,balance_after_cents,transaction_type,created_at)
 SELECT md5('financial-audit-plan-' || n::text)::uuid,
 CASE WHEN n<=200000 THEN $1::uuid ELSE $2::uuid END,100,100,'topup','2026-01-01'
 FROM generate_series(1,201000) n`, tenant, other); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `ANALYZE purser.balance_transactions`); err != nil {
		t.Fatal(err)
	}
	var cursor string
	if err := db.QueryRowContext(ctx, `SELECT id::text FROM purser.balance_transactions WHERE tenant_id=$1 ORDER BY id OFFSET 100000 LIMIT 1`, tenant).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	capture := &financialAuditQueryCapture{DB: db}
	rows, err := New(capture).FinancialAuditPage(ctx, FinancialAuditPageParams{TenantID: tenant, Source: "balance_transactions", AfterID: cursor, FromAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), ThroughAt: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil || len(rows) != 101 {
		t.Fatalf("audit page rows=%d err=%v", len(rows), err)
	}
	for _, r := range rows {
		var record struct {
			Tenant string `json:"tenant_id"`
		}
		if err := json.Unmarshal([]byte(r.Payload), &record); err != nil || record.Tenant != tenant || r.ID <= cursor {
			t.Fatalf("page escaped tenant/cursor: id=%s tenant=%s err=%v", r.ID, record.Tenant, err)
		}
	}
	var raw []byte
	if err := db.QueryRowContext(ctx, "EXPLAIN (ANALYZE, VERBOSE, BUFFERS, FORMAT JSON) "+capture.query, capture.args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plan []map[string]any
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	seek := false
	maxSerialization := float64(0)
	var visit func(any)
	visit = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			loops, _ := v["Actual Loops"].(float64)
			count, _ := v["Actual Rows"].(float64)
			condition, _ := v["Index Cond"].(string)
			if loops > 0 && strings.Contains(condition, "tenant_id") && strings.Contains(condition, "id >=") && !strings.Contains(condition, "id)::text") && count <= 102 {
				seek = true
			}
			if output, ok := v["Output"]; ok && strings.Contains(fmt.Sprint(output), "to_jsonb") && loops*count > maxSerialization {
				maxSerialization = loops * count
			}
			for _, child := range v {
				visit(child)
			}
		case []any:
			for _, child := range v {
				visit(child)
			}
		}
	}
	visit(plan[0]["Plan"])
	t.Logf("200k-row tenant: indexed UUID seek=%t; maximum rows at JSON serialization=%g", seek, maxSerialization)
	if !seek || maxSerialization > 101 {
		t.Fatalf("audit must seek an indexed UUID page and serialize only returned rows: seek=%t serialized=%g plan=%s", seek, maxSerialization, raw)
	}
}

func TestFinancialAuditSeeksBoundedUUIDPage_RealPG(t *testing.T) {
	assertFinancialAuditSeeksBoundedUUIDPage(t, startQueryCatalogRealPG(t))
}

func TestFinancialAuditPaymentsHaveBoundedWork_RealPG(t *testing.T) {
	db := startQueryCatalogRealPG(t)
	ctx := context.Background()
	const tenant = "95000000-0000-0000-0000-000000000001"
	const other = "95000000-0000-0000-0000-000000000002"
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.billing_invoices(id,tenant_id,due_date,status) SELECT md5('audit-payment-invoice-'||n::text)::uuid,CASE WHEN n<=200 THEN $1::uuid ELSE $2::uuid END,NOW(),'paid' FROM generate_series(1,20200)n`, tenant, other); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.billing_payments(id,invoice_id,method,amount,currency,status,created_at,original_amount_cents,original_currency,eur_amount_cents,fx_units_per_eur,fx_source,fx_reference_date) SELECT md5('audit-payment-'||n::text)::uuid,md5('audit-payment-invoice-'||(CASE WHEN n%100=0 THEN 1+(n/100-1)%200 ELSE 201+(n-1)%20000 END)::text)::uuid,'card',1,'EUR','confirmed','2026-01-01',100,'EUR',100,1,'identity',DATE '2026-01-01' FROM generate_series(1,202000)n`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"billing_invoices", "billing_payments"} {
		if _, err := db.ExecContext(ctx, "ANALYZE purser."+table); err != nil {
			t.Fatal(err)
		}
	}
	var invoiceID, paymentID string
	if err := db.QueryRowContext(ctx, `SELECT i.id::text,p.id::text FROM purser.billing_invoices i JOIN purser.billing_payments p ON p.invoice_id=i.id WHERE i.tenant_id=$1 ORDER BY p.id OFFSET 1000 LIMIT 1`, tenant).Scan(&invoiceID, &paymentID); err != nil {
		t.Fatal(err)
	}
	cursor := paymentID
	capture := &financialAuditQueryCapture{DB: db}
	rows, err := New(capture).FinancialAuditPage(ctx, FinancialAuditPageParams{TenantID: tenant, Source: "payments", AfterID: cursor, FromAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), ThroughAt: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil || len(rows) != 101 {
		t.Fatalf("payments page: count=%d err=%v", len(rows), err)
	}
	var raw []byte
	if err := db.QueryRowContext(ctx, "EXPLAIN (ANALYZE,VERBOSE,BUFFERS,FORMAT JSON) "+capture.query, capture.args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plans []map[string]any
	if err := json.Unmarshal(raw, &plans); err != nil {
		t.Fatal(err)
	}
	maxPaymentReads, maxSerialization := float64(0), float64(0)
	var visit func(any)
	visit = func(value any) {
		switch node := value.(type) {
		case map[string]any:
			loops, _ := node["Actual Loops"].(float64)
			count, _ := node["Actual Rows"].(float64)
			removed, _ := node["Rows Removed by Filter"].(float64)
			if node["Relation Name"] == "billing_payments" && loops*(count+removed) > maxPaymentReads {
				maxPaymentReads = loops * (count + removed)
			}
			if output, ok := node["Output"]; ok && strings.Contains(fmt.Sprint(output), "to_jsonb") && loops*count > maxSerialization {
				maxSerialization = loops * count
			}
			for _, child := range node {
				visit(child)
			}
		case []any:
			for _, child := range node {
				visit(child)
			}
		}
	}
	visit(plans[0]["Plan"])
	t.Logf("sparse tenant amid 202k interleaved payments: maximum payment reads=%g serialized=%g", maxPaymentReads, maxSerialization)
	if maxPaymentReads > 102 || maxSerialization > 101 {
		t.Fatalf("payments page still scans/sorts the tenant's full payment set: reads=%g serialized=%g", maxPaymentReads, maxSerialization)
	}
}
