package datamigrations

import (
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
)

func TestRunClusterAccessProvenanceBatchesOnlyDerivableRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec("WITH candidates AS").WithArgs(2).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("WITH batch AS").
		WithArgs(2).
		WillReturnRows(sqlmock.NewRows([]string{"scanned", "changed", "unresolved"}).AddRow(2, 2, 0))

	progress, err := runClusterAccessProvenance(context.Background(), db, datamigrate.RunOptions{BatchSize: 2})
	if err != nil {
		t.Fatalf("runClusterAccessProvenance: %v", err)
	}
	if progress.Scanned != 2 || progress.Changed != 3 || progress.Done {
		t.Fatalf("unexpected progress: %+v", progress)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClusterAccessProvenanceDoesNotInferMarketplacePayment(t *testing.T) {
	if strings.Contains(clusterAccessProvenanceBatchSQL, "third_party_marketplace") ||
		strings.Contains(clusterAccessProvenanceBatchSQL, "marketplace_subscription") {
		t.Fatal("Quartermaster-only backfill must not infer a paid marketplace subscription")
	}
}

func TestVerifyClusterAccessProvenanceRejectsDerivableUnknownRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("SELECT COUNT\\(\\*\\)").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	if err := verifyClusterAccessProvenance(context.Background(), db); err == nil {
		t.Fatal("expected verification failure")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyClusterAccessProvenanceRejectsMissingDerivedRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("SELECT COUNT\\(\\*\\)").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("FROM \\(").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	if err := verifyClusterAccessProvenance(context.Background(), db); err == nil {
		t.Fatal("expected missing derived grant verification failure")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterClusterAccessProvenance(t *testing.T) {
	Register()
	migration := datamigrate.Lookup(ClusterAccessProvenanceID)
	if migration == nil {
		t.Fatal("migration was not registered")
	}
	if migration.Service != "quartermaster" || migration.RequiredBeforePhase != "postdeploy" {
		t.Fatalf("unexpected migration metadata: %+v", migration)
	}
}
