//go:build schema_verify

package purserdb

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func assertCheckoutRecoveryQueryPlan(t *testing.T, db *sql.DB) {
	t.Helper()
	tenant := uuid.NewString()
	ctx := t.Context()
	// Terminal history dominates the inventory; the queue must visit the small
	// unresolved subset through the same partial predicate used in production.
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.payment_provider_intents(tenant_id,provider,purpose,status,currency,idempotency_key,created_at)
 SELECT $1::text::uuid,'stripe','tenant_subscription_checkout',CASE WHEN n<=24 THEN 'provider_open' ELSE 'succeeded' END,'EUR',$1::text||':'||n::text,NOW()-n*INTERVAL '1 minute'
 FROM generate_series(1,12000) n`, tenant); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.Exec(`DELETE FROM purser.payment_provider_intents WHERE tenant_id=$1`, tenant); err != nil {
			t.Error(err)
		}
	}()
	if _, err := db.ExecContext(ctx, `ANALYZE purser.payment_provider_intents`); err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx, "EXPLAIN (COSTS OFF) "+enqueueUnresolvedCheckoutRecovery)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(lines, "\n")
	t.Logf("checkout recovery inventory plan:\n%s", plan)
	if !strings.Contains(plan, "idx_provider_checkout_unresolved") {
		t.Fatalf("recovery inventory did not use its partial index:\n%s", plan)
	}
}
