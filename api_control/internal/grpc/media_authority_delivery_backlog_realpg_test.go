//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
)

type deliveryClaimCapture struct {
	commodoredb.DBTX
	query string
	args  []any
}

func (c *deliveryClaimCapture) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	c.query, c.args = query, append([]any(nil), args...)
	return c.DBTX.QueryContext(ctx, query, args...)
}

func TestMediaPlacementDeliveryBacklog_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	capture := &deliveryClaimCapture{DBTX: db}
	params := commodoredb.ClaimMediaAuthorityDeadlineDeliveryParams{LeaseMs: mediaAuthorityDeadlineDeliveryLease.Milliseconds(), BatchSize: 1}
	if _, err := commodoredb.New(capture).ClaimMediaAuthorityDeadlineDelivery(ctx, params); err != nil {
		t.Fatal(err)
	}
	const history, pending, cells = 20000, 2000, 20
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.media_authority_versions
    (authority_kind,authority_id,authority_version,payload_schema_version,payload,payload_sha256,source_revisions,issued_at,refresh_after,valid_until)
SELECT 'tenant','load-'||n,1,2,''::bytea,decode(repeat('00',32),'hex'),'[]'::jsonb,
       NOW()-CASE WHEN n<=$2 THEN INTERVAL '1 hour' ELSE INTERVAL '0 seconds' END,
       NOW()-CASE WHEN n<=$2 THEN INTERVAL '1 hour' ELSE INTERVAL '0 seconds' END+INTERVAL '15 seconds',
       NOW()-CASE WHEN n<=$2 THEN INTERVAL '1 hour' ELSE INTERVAL '0 seconds' END+INTERVAL '30 seconds'
FROM generate_series(1,$1::integer) AS n`, history+pending, history); err != nil {
		t.Fatal(err)
	}
	// Only the current version of an authority is claimable, so the plan is only
	// meaningful against a backlog whose versions are current.
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.media_authority_current (authority_kind,authority_id,authority_version)
SELECT 'tenant','load-'||n,1 FROM generate_series(1,$1::integer) AS n`, history+pending); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.media_authority_deliveries
    (authority_kind,authority_id,authority_version,cell_id,signed_envelope,short_lease,status,next_attempt_at,created_at)
SELECT 'tenant','load-'||n,1,'cell-'||(n%$3::integer),repeat(md5(n::text),32)::bytea,TRUE,
       CASE WHEN n<=$2 THEN 'acknowledged' ELSE 'pending' END,
       NOW()-INTERVAL '1 second',NOW()-INTERVAL '1 hour'+n*INTERVAL '1 millisecond'
FROM generate_series(1,$1::integer) AS n`, history+pending, history, cells); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "ANALYZE commodore.media_authority_versions; ANALYZE commodore.media_authority_deliveries"); err != nil {
		t.Fatal(err)
	}
	planCtx, stopPlan := context.WithTimeout(ctx, time.Second)
	var encoded []byte
	err := db.QueryRowContext(planCtx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+capture.query, capture.args...).Scan(&encoded)
	stopPlan()
	if err != nil {
		t.Fatalf("claim plan exceeded worker budget or failed at %d history/%d pending: %v", history, pending, err)
	}
	var plan []struct {
		ExecutionTime float64         `json:"Execution Time"`
		Plan          json.RawMessage `json:"Plan"`
	}
	if err := json.Unmarshal(encoded, &plan); err != nil || len(plan) != 1 {
		t.Fatalf("invalid claim explain output: %s %v", encoded, err)
	}
	t.Logf("claim plan history=%d pending=%d cells=%d execution_ms=%.3f plan=%s", history, pending, cells, plan[0].ExecutionTime, plan[0].Plan)
	q := commodoredb.New(db)
	var timings []time.Duration
	for range 12 {
		claimCtx, stopClaim := context.WithTimeout(ctx, time.Second)
		started := time.Now()
		rows, err := q.ClaimMediaAuthorityDeadlineDelivery(claimCtx, params)
		timings = append(timings, time.Since(started))
		stopClaim()
		if err != nil || len(rows) != 1 {
			t.Fatalf("claim failed under backlog: rows=%d error=%v", len(rows), err)
		}
		row := rows[0]
		if _, err := q.MarkMediaAuthorityDeliveryAcknowledged(ctx, commodoredb.MarkMediaAuthorityDeliveryAcknowledgedParams{AuthorityKind: row.AuthorityKind, AuthorityID: row.AuthorityID, AuthorityVersion: row.AuthorityVersion, CellID: row.CellID}); err != nil {
			t.Fatal(err)
		}
	}
	sort.Slice(timings, func(i, j int) bool { return timings[i] < timings[j] })
	t.Logf("claim samples=%d median=%s maximum=%s worker_budget=1s", len(timings), timings[len(timings)/2], timings[len(timings)-1])
}
