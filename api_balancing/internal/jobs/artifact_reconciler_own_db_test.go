package jobs

import (
	"testing"

	"frameworks/api_balancing/internal/control"

	"github.com/DATA-DOG/go-sqlmock"
)

// The reconciler's repair and attribution passes run on the reconciler's own
// database, never on a database the control package was handed elsewhere.
func TestReconcilerRepairPassesUseTheReconcilersDatabase(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	prev := control.GetDB()
	control.SetDB(nil)
	t.Cleanup(func() { control.SetDB(prev) })

	mock.ExpectQuery(`FROM foghorn.artifacts p`).WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "tenant_id"}))
	r := newTestReconciler(t, db, nil, nil, nil)
	if n := r.repairDeletedDVRChildren(t.Context()); n != 0 {
		t.Fatalf("repaired %d parents from an empty candidate set", n)
	}

	mock.ExpectQuery(`pg_try_advisory_lock`).WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(true))
	mock.ExpectQuery(`FROM foghorn.billing_attribution_cursor`).WillReturnRows(sqlmock.NewRows([]string{"last_tenant", "last_cluster"}).AddRow("", ""))
	mock.ExpectQuery(`SELECT p.tenant, p.cluster FROM`).WillReturnRows(sqlmock.NewRows([]string{"tenant", "cluster"}))
	mock.ExpectExec(`UPDATE foghorn.billing_attribution_cursor`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`pg_advisory_unlock`).WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(true))
	r.reconcileBillingAttribution()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
