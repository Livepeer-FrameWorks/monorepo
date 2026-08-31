package datamigrations

import (
	"context"
	"database/sql"
	"slices"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestPointerEligibilityDependsOnLifecycleConversion(t *testing.T) {
	Register()
	for _, migration := range datamigrate.Registry() {
		if migration.ID == FederatedPointerPurgeEligibilityID {
			if !slices.Contains(migration.DependsOn, FederatedArtifactLifecycleID) {
				t.Fatalf("eligibility dependencies = %v, want %q", migration.DependsOn, FederatedArtifactLifecycleID)
			}
			return
		}
	}
	t.Fatalf("migration %q was not registered", FederatedPointerPurgeEligibilityID)
}

func TestRunFederatedArtifactLifecycleBatchesAndCompletes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("WITH batch AS").WithArgs(25).
		WillReturnRows(sqlmock.NewRows([]string{"scanned", "changed"}).AddRow(int64(4), int64(4)))

	progress, err := runFederatedArtifactLifecycle(context.Background(), db, datamigrate.RunOptions{BatchSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if progress.Scanned != 4 || progress.Changed != 4 || !progress.Done {
		t.Fatalf("unexpected progress: %+v", progress)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyFederatedArtifactLifecycleRejectsLegacyPointers(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT COUNT\\(\\*\\)").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(2)))
	if err := verifyFederatedArtifactLifecycle(context.Background(), db); err == nil {
		t.Fatal("verification accepted legacy active federation pointers")
	}
}

func TestRunFederatedPointerPurgeEligibilityBatchesAndCompletes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("WITH batch AS").WithArgs(25).
		WillReturnRows(sqlmock.NewRows([]string{"scanned", "changed"}).AddRow(int64(4), int64(4)))

	progress, err := runFederatedPointerPurgeEligibility(context.Background(), db, datamigrate.RunOptions{BatchSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if progress.Scanned != 4 || progress.Changed != 4 || !progress.Done {
		t.Fatalf("unexpected progress: %+v", progress)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyFederatedPointerPurgeEligibilityRejectsResetAge(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT COUNT\\(\\*\\)").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(2)))
	if err := verifyFederatedPointerPurgeEligibility(context.Background(), db); err == nil {
		t.Fatal("verification accepted federated pointers whose purge age was reset")
	}
}

func TestRunBackgroundStopsOnlyAfterCellLocalVerification(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	prior := runRegisteredBackground
	t.Cleanup(func() { runRegisteredBackground = prior })
	calls := 0
	runRegisteredBackground = func(context.Context, *sql.DB, int) error {
		calls++
		return nil
	}
	RunBackground(context.Background(), db, logging.NewLogger())
	if calls != 1 {
		t.Fatalf("background runner calls = %d, want 1", calls)
	}
}
