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

func TestHandleRunDryRunPrintsFindings(t *testing.T) {
	resetForTest()
	Register(Migration{
		ID: "findings", Service: "commodore", IntroducedIn: "v0.5.0",
		Run: func(_ context.Context, _ DB, _ RunOptions) (Progress, error) {
			return Progress{Scanned: 3, Errors: 3, Summary: []string{"scanned rows=3"}, Findings: []string{"row=a", "row=b"}, FindingsTotal: 3}, nil
		},
	})
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery("_data_migration_runs").
		WithArgs("findings", "", "").
		WillReturnError(errors.New(`pq: relation "_data_migration_runs" does not exist`))
	mock.ExpectBegin()
	mock.ExpectRollback()

	var out bytes.Buffer
	if err := HandleRun(context.Background(), func() (*sql.DB, error) { return db, nil }, &out, []string{"findings", "--dry-run"}); err != nil {
		t.Fatalf("HandleRun dry-run returned error: %v", err)
	}
	for _, want := range []string{"  scanned rows=3\n", "findings: showing 2 of 3", "    row=a\n", "    row=b\n"} {
		if !bytes.Contains(out.Bytes(), []byte(want)) {
			t.Fatalf("dry-run output missing %q:\n%s", want, out.String())
		}
	}
}

func TestHandleVerifyPrintsReportBeforeFailure(t *testing.T) {
	resetForTest()
	verifyErr := errors.New("invariant broken")
	Register(Migration{
		ID: "reported", Service: "commodore", IntroducedIn: "v0.5.0",
		Run:    func(context.Context, DB, RunOptions) (Progress, error) { return Progress{Done: true}, nil },
		Verify: func(context.Context, DB) error { return verifyErr },
		Report: func(context.Context, DB) ([]string, error) { return []string{"row=a broken"}, nil },
	})
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectRollback()
	mock.ExpectBegin()
	mock.ExpectRollback()

	var out bytes.Buffer
	err = HandleVerify(context.Background(), func() (*sql.DB, error) { return db, nil }, &out, []string{"reported"})
	if !errors.Is(err, verifyErr) {
		t.Fatalf("HandleVerify error = %v, want %v", err, verifyErr)
	}
	if out.String() != "row=a broken\n" {
		t.Fatalf("verify output = %q", out.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected database operation: %v", err)
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

func TestRunScopePersistsCumulativeProgressAcrossBatchesAndResume(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Now()
	mock.ExpectQuery("FROM _data_migration_runs").WithArgs("cumulative", "", "").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "scope_kind", "scope_value", "status", "checkpoint", "lease_owner", "lease_expires_at",
			"attempt_count", "scanned_count", "changed_count", "skipped_count", "error_count",
			"last_error", "started_at", "updated_at", "completed_at",
		}).AddRow("cumulative", "", "", string(StatusRunning), []byte(`{"page":1}`), nil, nil,
			1, 10, 4, 1, 2, "", now, now, nil))
	mock.ExpectExec("UPDATE _data_migration_runs").
		WithArgs("cumulative", "", "", sqlmock.AnyArg(), float64(120), string(StatusRunning), string(StatusCompleted), string(StatusPaused)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE _data_migration_runs").
		WithArgs("cumulative", "", "", string(StatusRunning), []byte(`{"page":2}`), int64(13), int64(6), int64(2), int64(3)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE _data_migration_runs").
		WithArgs("cumulative", "", "", float64(120), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE _data_migration_runs").
		WithArgs("cumulative", "", "", string(StatusRunning), []byte(`{"page":3}`), int64(15), int64(7), int64(2), int64(5)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE _data_migration_runs").
		WithArgs("cumulative", "", "", string(StatusCompleted)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE _data_migration_runs").
		WithArgs("cumulative", "", "", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	calls := 0
	migration := &Migration{ID: "cumulative", Service: "test", Run: func(_ context.Context, _ DB, opts RunOptions) (Progress, error) {
		calls++
		switch calls {
		case 1:
			if string(opts.Checkpoint) != `{"page":1}` {
				t.Fatalf("first checkpoint=%s", opts.Checkpoint)
			}
			return Progress{Scanned: 3, Changed: 2, Skipped: 1, Errors: 1, Checkpoint: []byte(`{"page":2}`)}, nil
		case 2:
			if string(opts.Checkpoint) != `{"page":2}` {
				t.Fatalf("second checkpoint=%s", opts.Checkpoint)
			}
			return Progress{Scanned: 2, Changed: 1, Errors: 2, Checkpoint: []byte(`{"page":3}`), Done: true}, nil
		default:
			t.Fatalf("unexpected batch %d", calls)
			return Progress{}, nil
		}
	}}
	var out bytes.Buffer
	if err := runScope(context.Background(), db, &out, migration, migration.ID, ScopeKey{}, 100, false, true); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("scanned=15 changed=7 errors=5")) {
		t.Fatalf("completion did not report cumulative totals: %q", out.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
