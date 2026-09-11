package datamigrate

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// Completion is scoped to the discovered set, not to every run row that ever
// existed.
//
// A run row for a scope discovery no longer returns could never satisfy a
// whole-table aggregate, so the job stayed running forever. Because the release
// preflight is fail-closed on anything not completed, that refused every future
// deploy, with manual SQL as the only escape — which this platform's operating
// model does not permit.
func TestMarkJobCompletedIfScopesCompletedIgnoresRetiredScopes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// The statement asks only about the scopes it was given; a row outside that
	// set cannot hold the job open.
	mock.ExpectExec(regexp.QuoteMeta("UPDATE _data_migrations")).
		WithArgs("job-1", string(StatusCompleted), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	completed, err := MarkJobCompletedIfScopesCompleted(context.Background(), db, "job-1",
		[]ScopeKey{{Kind: "tenant", Value: "t1"}, {Kind: "tenant", Value: "t2"}})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if !completed {
		t.Fatal("job with every discovered scope completed was left running")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// An empty scope set must never complete a job: "nothing to check" is not
// "everything is done", and the preflight treats completed as proof the work ran.
func TestMarkJobCompletedIfScopesCompletedRefusesEmptyScopeSet(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	completed, err := MarkJobCompletedIfScopesCompleted(context.Background(), db, "job-1", nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if completed {
		t.Fatal("an empty scope set completed the job")
	}
	// No statement may be issued at all.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// A scope still outstanding keeps the job running.
func TestMarkJobCompletedIfScopesCompletedWaitsForOutstandingScope(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectExec(regexp.QuoteMeta("UPDATE _data_migrations")).
		WithArgs("job-1", string(StatusCompleted), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))

	completed, err := MarkJobCompletedIfScopesCompleted(context.Background(), db, "job-1",
		[]ScopeKey{{Kind: "tenant", Value: "t1"}})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if completed {
		t.Fatal("job completed while a discovered scope had no completed run")
	}
}
