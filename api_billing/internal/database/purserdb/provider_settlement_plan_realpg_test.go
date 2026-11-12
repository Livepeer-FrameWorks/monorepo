//go:build schema_verify

package purserdb

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

func TestSettlementValuationQueueHasBoundedWork_RealPG(t *testing.T) {
	assertSettlementValuationQueueBounded(t, startQueryCatalogRealPG(t), false)
}

func TestSettlementValuationQueueHasBoundedWork_RealYugabyte(t *testing.T) {
	assertSettlementValuationQueueBounded(t, startQueryCatalogRealYugabyte(t), true)
}

func assertSettlementValuationQueueBounded(t *testing.T, db *sql.DB, distributed bool) {
	t.Helper()
	tenant := uuid.NewString()
	_, err := db.ExecContext(t.Context(), `INSERT INTO purser.provider_settlements
 (tenant_id,provider,provider_payment_id,provider_balance_transaction_id,charge_amount_cents,charge_currency,
 settled_amount_cents,fee_cents,net_cents,settlement_currency,exchange_rate,status,settled_at,last_error_code,updated_at)
 SELECT $1::text::uuid,'stripe',$1::text||':payment:'||n,$1::text||':transaction:'||n,100,'EUR',120,5,115,
 CASE WHEN n<=4096 THEN 'CAD' ELSE 'USD' END,1.2,'blocked',NOW(),'unsupported_currency',NOW()-(9000-n)*INTERVAL '1 second'
 FROM generate_series(1,8192)n`, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `ANALYZE purser.provider_settlements`); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now()
	currencies := []string{"EUR", "USD", "GBP"}
	explain := "EXPLAIN (ANALYZE, FORMAT JSON) "
	if distributed {
		explain = "EXPLAIN (ANALYZE, DIST, FORMAT JSON) "
	}
	var raw []byte
	if err := db.QueryRowContext(t.Context(), explain+listRecordedProviderSettlements, cutoff, pq.Array(currencies)).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plans []struct{ Plan settlementQueuePlan }
	if err := json.Unmarshal(raw, &plans); err != nil {
		t.Fatal(err)
	}
	var scanned float64
	var visit func(settlementQueuePlan)
	visit = func(node settlementQueuePlan) {
		if node.Relation == "provider_settlements" {
			rows := (node.Rows + node.Removed) * node.Loops
			storage := (node.TableRows + node.IndexRows) * node.Loops
			if storage > rows {
				rows = storage
			}
			scanned += rows
		}
		for _, child := range node.Plans {
			visit(child)
		}
	}
	for _, plan := range plans {
		visit(plan.Plan)
	}
	if scanned > 400 {
		t.Fatalf("valuation batch examined %.0f rows for 100 supported settlements; want <=400:\n%s", scanned, raw)
	}
	rows, err := New(db).ListRecordedProviderSettlements(t.Context(), ListRecordedProviderSettlementsParams{UpdatedBefore: cutoff, SupportedCurrencies: currencies})
	if err != nil || len(rows) != 100 {
		t.Fatalf("supported work behind unsupported history: %d %v", len(rows), err)
	}
}

type settlementQueuePlan struct {
	Relation  string                `json:"Relation Name"`
	Rows      float64               `json:"Actual Rows"`
	Removed   float64               `json:"Rows Removed by Filter"`
	Loops     float64               `json:"Actual Loops"`
	TableRows float64               `json:"Storage Table Rows Scanned"`
	IndexRows float64               `json:"Storage Index Rows Scanned"`
	Plans     []settlementQueuePlan `json:"Plans"`
}
