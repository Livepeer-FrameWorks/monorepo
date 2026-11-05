//go:build schema_verify

package handlers

import (
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/google/uuid"
)

func TestStorageProviderCreditLookupKeepsUsageAndCorrectionShares_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	ctx := t.Context()
	tenant, other, providerA, providerB, invoice := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	start := time.Now().UTC().Add(-time.Hour)
	end := start.Add(time.Hour)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO purser.billing_invoices(id,tenant_id,status,amount,currency,due_date,period_start,period_end)
 VALUES($1,$2,'draft',10,'EUR',$4,$3,$4)`, invoice, tenant, start, end)
	exec(`INSERT INTO purser.invoice_line_items(invoice_id,tenant_id,line_key,meter,unit,dimensions,description,quantity,billable_quantity,unit_price,amount,currency,cluster_id)
 VALUES($1,$2,'storage','storage_gb_seconds_hot','gibibyte_second','{"storage_backend":"s3"}','Storage',250,250,0.04,10,'EUR','work')`, invoice, tenant)
	exec(`INSERT INTO purser.invoice_line_items(invoice_id,tenant_id,line_key,meter,unit,dimensions,description,quantity,billable_quantity,unit_price,amount,currency,cluster_id)
 VALUES($1,$2,'foreign-storage','storage_gb_seconds_hot','gibibyte_second','{"storage_backend":"s3"}','Foreign storage',100,100,1,100,'EUR','work')`, invoice, other)
	for _, row := range []struct{ tenant, provider string }{{tenant, providerA}, {tenant, providerB}, {tenant, tenant}, {other, providerA}} {
		exec(`INSERT INTO purser.provider_usage_records(usage_tenant_id,work_cluster_id,provider_tenant_id,provider_cluster_id,usage_type,unit,usage_value,dimensions,dimension_key,source_id,report_id,period_start,period_end)
 VALUES($1,'work',$2,'provider-cluster','storage_gb_seconds_hot','gibibyte_second',100,'{"storage_backend":"s3"}',repeat('a',64),'storage',repeat('b',64),$3,$4)`, row.tenant, row.provider, start, end)
	}
	exec(`INSERT INTO purser.usage_adjustments(tenant_id,cluster_id,usage_type,delta_value,period_start,period_end,source_system,source_id,details)
 VALUES($1,'work','storage_gb_seconds_hot',-50,$2,$3,'storage','correction',jsonb_build_object('natural_key',jsonb_build_object('storage_provider_tenant_id',$4::text,'storage_provider_cluster_id','provider-cluster','storage_backend','s3')))`, tenant, start, end, providerA)
	rows, err := purserdb.New(db).ListStorageProviderCreditAllocations(ctx, invoice)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("provider allocations=%d, want two provider usage rows and one correction", len(rows))
	}
	got := map[string]int64{}
	for _, row := range rows {
		if row.StorageProviderTenantID == tenant || row.StorageBackend != "s3" || row.Currency != "EUR" {
			t.Fatalf("unexpected allocation identity: %+v", row)
		}
		got[row.StorageProviderTenantID] += row.AllocatedGrossCents
	}
	if got[providerA] != 200 || got[providerB] != 400 {
		t.Fatalf("storage shares=%v, want A200/B400 with own usage and correction in denominator", got)
	}
}
