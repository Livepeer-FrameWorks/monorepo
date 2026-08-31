package datamigrate

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestHandleRunDryRunDoesNotWriteState(t *testing.T) {
	resetForTest()
	called := false
	Register(Migration{
		ID:           "dry",
		Service:      "purser",
		IntroducedIn: "v0.5.0",
		Run: func(_ context.Context, _ DB, opts RunOptions) (Progress, error) {
			called = true
			if !opts.DryRun {
				t.Fatal("Run called without DryRun")
			}
			return Progress{Scanned: 10, Changed: 3, Done: true}, nil
		},
	})

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("_data_migration_runs").
		WithArgs("dry", "", "").
		WillReturnError(errors.New(`pq: relation "_data_migration_runs" does not exist`))
	mock.ExpectBegin()
	mock.ExpectRollback()

	var out bytes.Buffer
	err = HandleRun(context.Background(), func() (*sql.DB, error) { return db, nil }, &out, []string{"dry", "--dry-run"})
	if err != nil {
		t.Fatalf("HandleRun dry-run returned error: %v", err)
	}
	if !called {
		t.Fatal("migration Run was not called")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected database operation: %v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte("dry-run")) {
		t.Fatalf("expected dry-run output, got %q", out.String())
	}
}

func TestRunScopeDoesNotCompleteWhenVerificationFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Now()
	mock.ExpectQuery("FROM _data_migration_runs").
		WithArgs("verified", "", "").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "scope_kind", "scope_value", "status", "checkpoint", "lease_owner", "lease_expires_at",
			"attempt_count", "scanned_count", "changed_count", "skipped_count", "error_count",
			"last_error", "started_at", "updated_at", "completed_at",
		}).AddRow("verified", "", "", string(StatusPending), []byte(`{}`), nil, nil, 0, 0, 0, 0, 0, "", nil, now, nil))
	mock.ExpectExec("UPDATE _data_migration_runs").
		WithArgs("verified", "", "", sqlmock.AnyArg(), float64(120), string(StatusRunning), string(StatusCompleted), string(StatusPaused)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE _data_migration_runs").
		WithArgs("verified", "", "", string(StatusRunning), sqlmock.AnyArg(), int64(0), int64(0), int64(0), int64(0)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT migration_invariant").WillReturnError(errors.New("invariant not satisfied"))
	mock.ExpectRollback()
	// No completed-state update may occur. The deferred lease release is the
	// only write after verification fails.
	mock.ExpectExec(regexp.QuoteMeta("UPDATE _data_migration_runs")).
		WithArgs("verified", "", "", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	migration := &Migration{
		ID: "verified", Service: "test",
		Run: func(context.Context, DB, RunOptions) (Progress, error) {
			return Progress{Done: true}, nil
		},
		Verify: func(ctx context.Context, db DB) error {
			var value int
			return db.QueryRowContext(ctx, "SELECT migration_invariant").Scan(&value)
		},
	}
	var out bytes.Buffer
	if err := runScope(context.Background(), db, &out, migration, migration.ID, ScopeKey{}, 100, false, true); err == nil {
		t.Fatal("runScope completed despite failed verification")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected database operation: %v", err)
	}
}
