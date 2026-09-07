package quartermasterdb

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestListTenantClusterAccessPageExcludesInactiveGrants(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT COUNT\(\*\).*a\.tenant_id = \$1 AND a\.is_active = true AND c\.is_active = true`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT c\.cluster_id.*a\.tenant_id = \$1 AND a\.is_active = true AND c\.is_active = true`).
		WithArgs("tenant-1", 21).
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "cluster_name", "access_level", "resource_limits", "created_at", "id"}))

	rows, total, err := New(db).ListTenantClusterAccessPage(context.Background(), SimplePageFilter{ScopeID: "tenant-1", Limit: 21})
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || len(rows) != 0 {
		t.Fatalf("inactive-only result = total %d rows %d, want empty", total, len(rows))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
