package control

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// ReconcileBillingAttribution reviews a BOUNDED page of (tenant, cluster) pairs PAST a durable cursor, marks
// the owned ones, and advances the cursor — so a slow/large remote prefix can never restart at the same
// position and starve later locally-owned pairs. This pins the read-cursor → bounded-scan → mark →
// advance-cursor shape, ownership classification, and the wrap-on-short-page behaviour.
func TestReconcileBillingAttribution_BoundedKeysetCursor(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	prevLocal := localClusterID
	SetLocalClusterID("official-a")
	t.Cleanup(func() {
		SetLocalClusterID(prevLocal)
	})

	// 1) Durable cursor read (start).
	mock.ExpectQuery(`SELECT last_tenant, last_cluster FROM foghorn.billing_attribution_cursor WHERE id = true`).
		WillReturnRows(sqlmock.NewRows([]string{"last_tenant", "last_cluster"}).AddRow("", ""))
	// 2) Bounded keyset page of unmarked pairs past the cursor: one pure-local (owned) + one remote (not owned).
	mock.ExpectQuery(`SELECT p.tenant, p.cluster FROM`).
		WithArgs("", "", billingAttributionBatch).
		WillReturnRows(sqlmock.NewRows([]string{"tenant", "cluster"}).
			AddRow("t1", "").
			AddRow("t2", "remote-x"))
	// 3) Only the owned pair (empty cluster ⇒ pure-local) is marked.
	mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET durable_backend_local = true`).
		WithArgs("t1", "").
		WillReturnResult(sqlmock.NewResult(0, 3))
	// 4) Short page (< batch) ⇒ cursor WRAPS to the start so the next cycle re-reviews from the top.
	mock.ExpectExec(`UPDATE foghorn.billing_attribution_cursor SET last_tenant = \$1, last_cluster = \$2 WHERE id = true`).
		WithArgs("", "").
		WillReturnResult(sqlmock.NewResult(0, 1))

	marked, err := ReconcileBillingAttribution(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if marked != 3 {
		t.Fatalf("expected 3 rows marked (the owned pair), got %d", marked)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A FULL page means more pairs remain, so the cursor advances to the last reviewed pair (not a wrap) — the
// next pass continues strictly after it instead of restarting at the same prefix.
func TestReconcileBillingAttribution_FullPageAdvancesCursor(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	prevLocal := localClusterID
	SetLocalClusterID("official-a")
	t.Cleanup(func() {
		SetLocalClusterID(prevLocal)
	})

	mock.ExpectQuery(`SELECT last_tenant, last_cluster FROM foghorn.billing_attribution_cursor WHERE id = true`).
		WillReturnRows(sqlmock.NewRows([]string{"last_tenant", "last_cluster"}).AddRow("t0", "c0"))
	// A full page of `billingAttributionBatch` remote (unowned) pairs.
	pageRows := sqlmock.NewRows([]string{"tenant", "cluster"})
	lastTenant, lastCluster := "", ""
	for i := 0; i < billingAttributionBatch; i++ {
		tn := "t-" + string(rune('a'+i%26))
		cl := "remote"
		pageRows.AddRow(tn, cl)
		lastTenant, lastCluster = tn, cl
	}
	mock.ExpectQuery(`SELECT p.tenant, p.cluster FROM`).
		WithArgs("t0", "c0", billingAttributionBatch).
		WillReturnRows(pageRows)
	// No UPDATE foghorn.artifacts (all pairs remote/unowned). Cursor advances to the LAST reviewed pair.
	mock.ExpectExec(`UPDATE foghorn.billing_attribution_cursor SET last_tenant = \$1, last_cluster = \$2 WHERE id = true`).
		WithArgs(lastTenant, lastCluster).
		WillReturnResult(sqlmock.NewResult(0, 1))

	marked, err := ReconcileBillingAttribution(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if marked != 0 {
		t.Fatalf("expected 0 rows marked (all remote), got %d", marked)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// I2 regression: changing a cluster's CURRENT advertised backing must NOT alter historical attribution. Here the
// live resolver is forced to report the row's remote cluster as locally-mintable NOW (canMintOfficialLocallyFn →
// true) — simulating a cluster that re-pointed its backing to this cell after the bytes were written elsewhere.
// The reconciler must IGNORE that (topology-now ≠ evidence-of-where-bytes-live): the remote-cluster row stays
// unmarked, no UPDATE fires. If the resolver clause is ever re-introduced this test fails.
func TestReconcileBillingAttribution_IgnoresCurrentBackingForHistoricalRows(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	prevLocal := localClusterID
	SetLocalClusterID("official-a")
	prevMint := canMintOfficialLocallyFn
	// The live resolver now says "yes, that cluster mints locally" — the exact drift I2 forbids from re-attributing.
	canMintOfficialLocallyFn = func(context.Context, string, string) bool { return true }
	t.Cleanup(func() {
		SetLocalClusterID(prevLocal)
		canMintOfficialLocallyFn = prevMint
	})

	mock.ExpectQuery(`SELECT last_tenant, last_cluster FROM foghorn.billing_attribution_cursor WHERE id = true`).
		WillReturnRows(sqlmock.NewRows([]string{"last_tenant", "last_cluster"}).AddRow("", ""))
	// One historical row on a REMOTE cluster (its recorded cluster != this cell). Recorded evidence says remote.
	mock.ExpectQuery(`SELECT p.tenant, p.cluster FROM`).
		WithArgs("", "", billingAttributionBatch).
		WillReturnRows(sqlmock.NewRows([]string{"tenant", "cluster"}).AddRow("t1", "remote-x"))
	// NO `UPDATE foghorn.artifacts` — the current backing must not re-attribute the historical row. Only the cursor
	// advances (short page ⇒ wrap to start).
	mock.ExpectExec(`UPDATE foghorn.billing_attribution_cursor SET last_tenant = \$1, last_cluster = \$2 WHERE id = true`).
		WithArgs("", "").
		WillReturnResult(sqlmock.NewResult(0, 1))

	marked, err := ReconcileBillingAttribution(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if marked != 0 {
		t.Fatalf("current backing must not re-attribute a historical remote row; marked %d", marked)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
