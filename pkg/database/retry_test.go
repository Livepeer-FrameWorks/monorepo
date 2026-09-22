package database

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
)

// fixedTextStatus mimics a gRPC status wrapper whose text omits its database cause.
type fixedTextStatus struct{ cause error }

func (e fixedTextStatus) Error() string { return "rpc error: code = Internal desc = database error" }
func (e fixedTextStatus) Unwrap() error { return e.cause }

func TestIsRetryablePostgresError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"serialization", &pq.Error{Code: "40001"}, true},
		{"deadlock", &pq.Error{Code: "40P01"}, true},
		{"schema version text", errors.New("pq: schema version mismatch for table x: expected 31, got 30 (40001)"), true},
		{"catalog version text", errors.New("pq: Catalog Version Mismatch: A DDL occurred while processing this query. Try Again."), true},
		{"mismatched schema text", errors.New("ERROR: The catalog snapshot used for this transaction has been invalidated: expected: 4, got: 5: MISMATCHED_SCHEMA"), true},
		{"wrapped grpc schema version text", errors.New("rpc error: code = Internal desc = database error: pq: schema version mismatch for table x: expected 31, got 30 (40001)"), true},
		{"read restart text", errors.New("pq: restart transaction: read restart required (40001)"), true},
		{"yugabyte restart read text", errors.New("pq: Restart read required (query layer retry isn't possible because this is not the first command in the transaction.) (40001)"), true},
		{"catalog mismatch under fixed-text status", fixedTextStatus{cause: errors.New("ERROR: Catalog Version Mismatch: A DDL occurred while processing this query")}, true},
		{"catalog mismatch inside errors.Join", errors.Join(errors.New("rollback failed"), errors.New("catalog version mismatch")), true},
		{"fixed-text status over a permanent error", fixedTextStatus{cause: errors.New("duplicate key value")}, false},
		{"syntax", &pq.Error{Code: "42601"}, false},
		{"non-transient 40001 text", errors.New("application error 40001"), false},
		{"ordinary", errors.New("boom"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRetryablePostgresError(tt.err); got != tt.want {
				t.Fatalf("IsRetryablePostgresError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRetryPostgresRetriesThenSucceeds(t *testing.T) {
	attempts := 0
	err := RetryPostgres(context.Background(), 3, time.Nanosecond, func() error {
		attempts++
		if attempts < 2 {
			return &pq.Error{Code: "40001"}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RetryPostgres returned error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestRetryPostgresWithHookRunsBeforeRetry(t *testing.T) {
	attempts := 0
	hookCalls := 0
	err := RetryPostgresWithHook(context.Background(), 3, time.Nanosecond, func(err error, attempt int) {
		hookCalls++
		if attempt != 1 {
			t.Fatalf("hook attempt = %d, want 1", attempt)
		}
		if !IsRetryablePostgresError(err) {
			t.Fatalf("hook err = %v, want retryable", err)
		}
	}, func() error {
		attempts++
		if attempts < 2 {
			return &pq.Error{Code: "40001"}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RetryPostgresWithHook returned error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if hookCalls != 1 {
		t.Fatalf("hookCalls = %d, want 1", hookCalls)
	}
}

func TestRetryPostgresDoesNotRetryPermanentError(t *testing.T) {
	attempts := 0
	wantErr := errors.New("permanent")
	err := RetryPostgres(context.Background(), 3, time.Nanosecond, func() error {
		attempts++
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("RetryPostgres error = %v, want %v", err, wantErr)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestWithRetryablePostgresTxRetriesBodyError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectRollback()
	mock.ExpectBegin()
	mock.ExpectCommit()

	attempts := 0
	err = WithRetryablePostgresTx(context.Background(), db, nil, func(tx *sql.Tx) error {
		attempts++
		if attempts == 1 {
			return &pq.Error{Code: "40001"}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithRetryablePostgresTx returned error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestWithRetryablePostgresTxRetriesCommitError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectCommit().WillReturnError(&pq.Error{Code: "40001"})
	mock.ExpectBegin()
	mock.ExpectCommit()

	attempts := 0
	err = WithRetryablePostgresTx(context.Background(), db, nil, func(tx *sql.Tx) error {
		attempts++
		return nil
	})
	if err != nil {
		t.Fatalf("WithRetryablePostgresTx returned error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestRetryDelayIsCapped proves the backoff doubles from the base delay and never exceeds the cap, so the default
// budget stays near 2.6s.
func TestRetryDelayIsCapped(t *testing.T) {
	var total time.Duration
	for attempt := 0; attempt < DefaultRetryAttempts-1; attempt++ {
		delay := retryDelay(25*time.Millisecond, attempt)
		if delay > maxRetryDelay {
			t.Fatalf("attempt %d delay %s exceeds cap %s", attempt, delay, maxRetryDelay)
		}
		total += delay
	}
	if total < 2*time.Second || total > 3*time.Second {
		t.Fatalf("default retry budget = %s, want about 2.6s", total)
	}
	if got := retryDelay(25*time.Millisecond, 62); got != maxRetryDelay {
		t.Fatalf("overflowing shift delay = %s, want cap", got)
	}
}

func TestTryInSavepointRollsBackATolerableFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec(`SAVEPOINT frameworks_best_effort`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO attribution`).WillReturnError(&pq.Error{Code: "23505"})
	mock.ExpectExec(`ROLLBACK TO SAVEPOINT frameworks_best_effort`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`UPDATE next`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	failed, abort := TryInSavepoint(context.Background(), tx, func() error {
		_, execErr := tx.ExecContext(context.Background(), "INSERT INTO attribution VALUES (1)")
		return execErr
	})
	if abort != nil || SQLState(failed) != "23505" {
		t.Fatalf("failed=%v abort=%v, want the unique violation as failed", failed, abort)
	}
	if _, err := tx.ExecContext(context.Background(), "UPDATE next SET x = 1"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestTryInSavepointAbortsOnARetryableFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec(`SAVEPOINT frameworks_best_effort`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO attribution`).WillReturnError(&pq.Error{Code: "40001"})
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	failed, abort := TryInSavepoint(context.Background(), tx, func() error {
		_, execErr := tx.ExecContext(context.Background(), "INSERT INTO attribution VALUES (1)")
		return execErr
	})
	if failed != nil || !IsRetryablePostgresError(abort) {
		t.Fatalf("failed=%v abort=%v, want the serialization failure as abort", failed, abort)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
