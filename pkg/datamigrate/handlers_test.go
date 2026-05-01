package datamigrate

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"

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
