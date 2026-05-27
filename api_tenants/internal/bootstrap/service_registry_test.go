package bootstrap

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestEnsureServiceCatalogRowRepairsExistingTypeDrift(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	entry := ServiceRegistryEntry{
		ServiceName: "vmauth",
		Type:        "vmauth",
	}

	mock.ExpectQuery(regexp.QuoteMeta("SELECT service_id FROM quartermaster.services WHERE service_id = $1 OR name = $1")).
		WithArgs("vmauth").
		WillReturnRows(sqlmock.NewRows([]string{"service_id"}).AddRow("vmauth"))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE quartermaster.services")).
		WithArgs("vmauth", "vmauth", "control", "vmauth", "http").
		WillReturnResult(sqlmock.NewResult(0, 1))

	serviceID, err := ensureServiceCatalogRow(context.Background(), db, entry)
	if err != nil {
		t.Fatalf("ensureServiceCatalogRow: %v", err)
	}
	if serviceID != "vmauth" {
		t.Fatalf("serviceID = %q, want vmauth", serviceID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
