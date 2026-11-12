//go:build schema_verify

package commodoredb

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type capCandidatePlanNode struct {
	Type      string                 `json:"Node Type"`
	Rows      float64                `json:"Actual Rows"`
	Filtered  float64                `json:"Rows Removed by Filter"`
	Rechecked float64                `json:"Rows Removed by Index Recheck"`
	Loops     float64                `json:"Actual Loops"`
	Plans     []capCandidatePlanNode `json:"Plans"`
}

func TestAuthSessionCapCandidateReadsStayBounded_RealPG(t *testing.T) {
	db := startCommodoreQueryCatalogRealPG(t)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	tenant, user := uuid.NewString(), uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,is_active,verified) VALUES($1,$2,'cap-plan@example.test',true,true)`, user, tenant); err != nil {
		t.Fatal(err)
	}
	for _, rows := range []struct {
		count   int
		expired bool
	}{{10000, true}, {250, false}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO commodore.auth_sessions(tenant_id,user_id,auth_method,client_id,created_at,last_seen_at,idle_expires_at,absolute_expires_at)
SELECT $1,$2,'unknown','unknown',NOW()-INTERVAL '3 days',
CASE WHEN $4::boolean THEN NOW()-INTERVAL '2 days' ELSE NOW()-INTERVAL '1 hour' END,
CASE WHEN $4::boolean THEN NOW()-INTERVAL '1 day' ELSE NOW()+INTERVAL '1 day' END,
NOW()+INTERVAL '2 days' FROM generate_series(1,$3)`, tenant, user, rows.count, rows.expired); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `ANALYZE commodore.auth_sessions`); err != nil {
		t.Fatal(err)
	}
	var protected string
	if err := db.QueryRowContext(ctx, `SELECT id FROM commodore.auth_sessions WHERE tenant_id=$1 AND user_id=$2 ORDER BY last_seen_at,created_at,id LIMIT 1`, tenant, user).Scan(&protected); err != nil {
		t.Fatal(err)
	}
	for _, query := range []struct {
		name, sql string
		args      []any
	}{
		{"ordinary", listOldestAuthSessions, []any{tenant, user, 20}},
		{"protected", listOldestOtherAuthSessions, []any{tenant, user, protected, 20}},
	} {
		t.Run(query.name, func(t *testing.T) {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			var raw []byte
			if err = tx.QueryRowContext(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+query.sql, query.args...).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var plans []struct{ Plan capCandidatePlanNode }
			if err = json.Unmarshal(raw, &plans); err != nil {
				t.Fatal(err)
			}
			var largest float64
			var inspect func(capCandidatePlanNode)
			inspect = func(node capCandidatePlanNode) {
				if strings.Contains(node.Type, "Scan") {
					touched := (node.Rows + node.Filtered + node.Rechecked) * node.Loops
					largest = max(largest, touched)
				}
				for _, child := range node.Plans {
					inspect(child)
				}
			}
			for _, plan := range plans {
				inspect(plan.Plan)
			}
			if largest > 21 {
				t.Fatalf("a 20-session cap step read %.0f candidate rows; expired history must not cause an unbounded scan: %s", largest, raw)
			}
		})
	}
}
