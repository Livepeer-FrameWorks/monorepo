//go:build schema_verify

package purserdb

import (
	"testing"
	"time"
)

func TestFinancialAuditReviewOccurrenceWindow_RealPG(t *testing.T) {
	db := startQueryCatalogRealPG(t)
	const tenant = "96000000-0000-0000-0000-000000000001"
	const other = "96000000-0000-0000-0000-000000000002"
	through := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	from := through.Add(-time.Hour)
	old := from.Add(-time.Hour)
	inside := from.Add(time.Minute)
	rows := []struct {
		id, owner   string
		first, last time.Time
		resolved    any
	}{
		{"96000000-0000-0000-0000-000000000010", tenant, old, inside, nil},
		{"96000000-0000-0000-0000-000000000011", tenant, old, old, inside},
		{"96000000-0000-0000-0000-000000000012", tenant, inside, through.Add(time.Minute), nil},
		{"96000000-0000-0000-0000-000000000013", tenant, old, through, nil},
		{"96000000-0000-0000-0000-000000000014", tenant, old, old, through},
		{"96000000-0000-0000-0000-000000000015", tenant, old, old, nil},
		{"96000000-0000-0000-0000-000000000016", other, inside, inside, nil},
	}
	for _, row := range rows {
		if _, err := db.ExecContext(t.Context(), `INSERT INTO purser.crypto_accounting_anomalies
		 (id,tenant_id,kind,network,reference_type,reference_id,detail,evidence_json,first_seen_at,last_seen_at,resolved_at,status)
		 VALUES($1::uuid,$2::uuid,'provider_tax_document_review','stripe','provider_invoice',$1::text,'Retained review','{}',$3,$4,$5,CASE WHEN $5::timestamptz IS NULL THEN 'open' ELSE 'resolved' END)`, row.id, row.owner, row.first, row.last, row.resolved); err != nil {
			t.Fatal(err)
		}
	}
	params := FinancialAuditPageParams{TenantID: tenant, Source: "billing_tax_reviews", FromAt: from, ThroughAt: through}
	page, err := New(db).FinancialAuditPage(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 3 || page[0].ID != rows[0].id || page[1].ID != rows[1].id || page[2].ID != rows[2].id {
		t.Fatalf("archive omitted an existing review updated/resolved inside the interval or escaped tenant/cutoff: %+v", page)
	}
	params.AfterID = rows[0].id
	page, err = New(db).FinancialAuditPage(t.Context(), params)
	if err != nil || len(page) != 2 || page[0].ID != rows[1].id || page[1].ID != rows[2].id {
		t.Fatalf("review occurrence filter broke UUID pagination: %+v err=%v", page, err)
	}
}
