//go:build schema_verify

package commodoredb

import (
	"database/sql"
	"encoding/json"
	"testing"
)

func TestStreamFinalizationCascadeBounded_RealPG(t *testing.T) {
	verifyStreamFinalizationCascade(t, startCommodoreQueryCatalogRealPG(t), false)
}

func TestStreamFinalizationCascadeBounded_RealYugabyte(t *testing.T) {
	verifyStreamFinalizationCascade(t, startCommodoreQueryCatalogRealYugabyte(t), true)
}

func verifyStreamFinalizationCascade(t *testing.T, db *sql.DB, yugabyte bool) {
	t.Helper()
	ctx := t.Context()
	const tenant = "cf000000-0000-4000-8000-000000000001"
	const otherTenant = "cf000000-0000-4000-8000-000000000002"
	const stream = "cf000000-0000-4000-8000-000000000003"
	const otherStream = "cf000000-0000-4000-8000-000000000004"
	const backgroundRows = 4000
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	})
	if _, err := tx.ExecContext(ctx, `SET LOCAL frameworks.suppress_media_authority_refresh='on'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO commodore.streams
(id,tenant_id,user_id,stream_key,playback_id,internal_name,title,deleted_at)
VALUES ($1::uuid,$2::uuid,$2::uuid,'cascade-deleted-key','cascade-deleted-playback','cascade-deleted','Finalized',NOW()),
       ($3::uuid,$4::uuid,$4::uuid,'cascade-surviving-key','cascade-surviving-playback','cascade-surviving','Other tenant',NULL)`, stream, tenant, otherStream, otherTenant); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []struct {
		tenant, stream string
		rows           int
	}{{tenant, stream, 1}, {otherTenant, otherStream, backgroundRows}} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO commodore.push_targets(tenant_id,stream_id,platform,name,target_uri)
SELECT $1::uuid,$2::uuid,'custom','cascade-'||i,'enc:v3:test:cascade' FROM generate_series(1,$3::integer) i`, owner.tenant, owner.stream, owner.rows); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO commodore.policy_bundle_versions(tenant_id,stream_id,bundle_version,bundle_jwt,expires_at)
SELECT $1::uuid,$2::uuid,i,'cascade-fixture',NOW()+INTERVAL '1 hour' FROM generate_series(1,$3::integer) i`, owner.tenant, owner.stream, owner.rows); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Yugabyte's statistics collector must see committed rows when it sizes the
	// FK lookup plans; measuring uncommitted bulk inserts models an empty table.
	for _, table := range []string{"streams", "push_targets", "policy_bundle_versions"} {
		if _, err := db.ExecContext(ctx, "ANALYZE commodore."+table); err != nil {
			t.Fatal(err)
		}
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := New(tx).HardDeleteFinalizedStream(ctx, HardDeleteFinalizedStreamParams{StreamID: stream, TenantID: otherTenant}); err != nil || rows != 0 {
		t.Fatalf("another tenant finalized the stream: rows=%d err=%v", rows, err)
	}
	// PostgreSQL exposes the cascade's hidden SPI reads in transaction-local
	// counters; Yugabyte's DIST plan reports the whole statement's storage reads.
	pgRowsRead := func() int64 {
		var rows int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(sum(seq_tup_read+idx_tup_fetch),0)::bigint
FROM pg_stat_xact_user_tables WHERE schemaname='commodore' AND relname IN ('streams','push_targets','policy_bundle_versions')`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	var before int64
	explain := "EXPLAIN (ANALYZE, FORMAT JSON) "
	if yugabyte {
		explain = "EXPLAIN (ANALYZE, DIST, FORMAT JSON) "
	} else {
		before = pgRowsRead()
	}
	var raw []byte
	if err := tx.QueryRowContext(ctx, explain+hardDeleteFinalizedStream, stream, tenant).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var scanned float64
	if yugabyte {
		var plans []struct {
			RowsRead float64 `json:"Storage Rows Scanned"`
		}
		if err := json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
			t.Fatalf("decode cascade plan: %s (%v)", raw, err)
		}
		scanned = plans[0].RowsRead
	} else {
		scanned = float64(pgRowsRead() - before)
	}
	t.Logf("finalized stream cascade examined %.0f rows with %d unrelated rows per child table", scanned, backgroundRows)
	if scanned <= 0 || scanned > 64 {
		t.Fatalf("finalized stream cascade examined %.0f rows; want 1..64, independent of unrelated tenants:\n%s", scanned, raw)
	}
	for _, table := range []string{"streams", "push_targets", "policy_bundle_versions"} {
		key := "stream_id"
		if table == "streams" {
			key = "id"
		}
		var deleted, retained int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM commodore."+table+" WHERE tenant_id=$1::uuid AND "+key+"=$2::uuid", tenant, stream).Scan(&deleted); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM commodore."+table+" WHERE tenant_id=$1::uuid AND "+key+"=$2::uuid", otherTenant, otherStream).Scan(&retained); err != nil {
			t.Fatal(err)
		}
		wantRetained := backgroundRows
		if table == "streams" {
			wantRetained = 1
		}
		if deleted != 0 || retained != wantRetained {
			t.Fatalf("%s after finalization: deleted-owner rows=%d other-tenant rows=%d, want 0/%d", table, deleted, retained, wantRetained)
		}
	}
}
