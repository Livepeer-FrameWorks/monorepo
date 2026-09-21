//go:build schema_verify

package quartermasterdb

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestMediaAuthorityRefreshCoalescing_RealPG(t *testing.T) {
	testMediaAuthorityRefreshCoalescing(t, startQuartermasterQueryCatalogRealPG(t))
}

func TestMediaAuthorityRefreshCoalescing_RealYugabyte(t *testing.T) {
	testMediaAuthorityRefreshCoalescing(t, startQuartermasterQueryCatalogRealYugabyte(t))
}

func testMediaAuthorityRefreshCoalescing(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	const tenantID = "73000000-0000-0000-0000-000000000001"
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	outbox := func(reason string) (rows int, revision int64, status string) {
		t.Helper()
		if err := db.QueryRowContext(ctx, `
			SELECT COUNT(*), COALESCE(MAX(revision), 0), COALESCE(MAX(status), '')
			FROM quartermaster.media_authority_refresh_outbox
			WHERE tenant_id = $1::uuid AND reason = $2 AND status <> 'completed'`, tenantID, reason).Scan(&rows, &revision, &status); err != nil {
			t.Fatal(err)
		}
		return rows, revision, status
	}

	exec(`INSERT INTO quartermaster.tenants (id, name) VALUES ($1::uuid, 'Coalescing tenant')`, tenantID)
	if rows, revision, _ := outbox("tenant_authority_changed"); rows != 1 || revision != 1 {
		t.Fatalf("tenant insert: rows=%d revision=%d, want one obligation", rows, revision)
	}

	// Rewriting a row with its own values is not an authority change.
	exec(`UPDATE quartermaster.tenants SET is_active = is_active, updated_at = NOW() WHERE id = $1::uuid`, tenantID)
	if rows, revision, _ := outbox("tenant_authority_changed"); rows != 1 || revision != 1 {
		t.Fatalf("no-op tenant update requested a refresh: rows=%d revision=%d", rows, revision)
	}

	// Real changes of one (tenant, reason) fold into the single unfinished row.
	for _, active := range []bool{false, true, false} {
		exec(`UPDATE quartermaster.tenants SET is_active = $2 WHERE id = $1::uuid`, tenantID, active)
	}
	if rows, revision, _ := outbox("tenant_authority_changed"); rows != 1 || revision != 4 {
		t.Fatalf("three changes: rows=%d revision=%d, want one row at revision 4", rows, revision)
	}

	// A change that lands while the row is being delivered must survive that
	// delivery's completion.
	queries := New(db)
	claimed, err := queries.ClaimMediaAuthorityRefreshBatch(ctx, ClaimMediaAuthorityRefreshBatchParams{LeaseMs: 60_000, BatchSize: 8})
	if err != nil || len(claimed) != 1 || claimed[0].Revision != 4 {
		t.Fatalf("claim = %+v, %v; want the single row at revision 4", claimed, err)
	}
	exec(`UPDATE quartermaster.tenants SET is_active = TRUE WHERE id = $1::uuid`, tenantID)
	completed, err := queries.CompleteMediaAuthorityRefresh(ctx, CompleteMediaAuthorityRefreshParams{ID: claimed[0].ID, Revision: claimed[0].Revision})
	if err != nil || completed != 0 {
		t.Fatalf("stale-revision completion settled %d rows (err %v); it would erase the newer change", completed, err)
	}
	released, err := queries.ReleaseSupersededMediaAuthorityRefresh(ctx, ReleaseSupersededMediaAuthorityRefreshParams{ID: claimed[0].ID, Revision: claimed[0].Revision})
	if err != nil || released != 1 {
		t.Fatalf("release superseded = %d (err %v), want 1", released, err)
	}
	if rows, revision, status := outbox("tenant_authority_changed"); rows != 1 || revision != 5 || status != "pending" {
		t.Fatalf("after fold during delivery: rows=%d revision=%d status=%q, want one pending row at revision 5", rows, revision, status)
	}
	reclaimed, err := queries.ClaimMediaAuthorityRefreshBatch(ctx, ClaimMediaAuthorityRefreshBatchParams{LeaseMs: 60_000, BatchSize: 8})
	if err != nil || len(reclaimed) != 1 || reclaimed[0].Revision != 5 {
		t.Fatalf("newer revision not claimable after release: %+v, %v", reclaimed, err)
	}
	if done, doneErr := queries.CompleteMediaAuthorityRefresh(ctx, CompleteMediaAuthorityRefreshParams{ID: reclaimed[0].ID, Revision: reclaimed[0].Revision}); doneErr != nil || done != 1 {
		t.Fatalf("complete current revision = %d (err %v), want 1", done, doneErr)
	}

	// A completed obligation does not absorb later changes; the next one starts
	// a new row.
	exec(`UPDATE quartermaster.tenants SET is_active = FALSE WHERE id = $1::uuid`, tenantID)
	if rows, revision, _ := outbox("tenant_authority_changed"); rows != 1 || revision != 1 {
		t.Fatalf("change after completion: rows=%d revision=%d, want a fresh obligation", rows, revision)
	}
}
