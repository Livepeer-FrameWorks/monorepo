package main

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"frameworks/api_tenants/internal/bootstrap"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/lib/pq"
)

// TestBootstrapTransactionReplaysSerializationFailures proves the bootstrap reconcile replays in a fresh transaction
// when a statement or the commit fails with SQLSTATE 40001 or 40P01 behind a section prefix, and returns the sections
// of the attempt that committed.
func TestBootstrapTransactionReplaysSerializationFailures(t *testing.T) {
	cases := []struct {
		name          string
		failStatement error
		failCommit    error
	}{
		{name: "statement serialization failure", failStatement: fmt.Errorf("nodes: %w", &pq.Error{Code: "40001"})},
		{name: "statement deadlock", failStatement: fmt.Errorf("clusters: %w", &pq.Error{Code: "40P01"})},
		{name: "commit serialization failure", failCommit: &pq.Error{Code: "40001"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectBegin()
			if tc.failStatement != nil {
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit().WillReturnError(tc.failCommit)
			}
			mock.ExpectBegin()
			mock.ExpectCommit()

			attempts := 0
			first, second := &bootstrap.Sections{}, &bootstrap.Sections{}
			out, err := runReconcileTransaction(context.Background(), db, false, func(*sql.Tx) (*bootstrap.Sections, error) {
				attempts++
				if attempts == 1 {
					return first, tc.failStatement
				}
				return second, nil
			})
			if err != nil {
				t.Fatalf("runReconcileTransaction: %v", err)
			}
			if attempts != 2 || out != second {
				t.Fatalf("attempts=%d out=%p, want 2 attempts returning the committed attempt's sections %p", attempts, out, second)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestBootstrapTransactionStopsOnPermanentFailure proves a constraint violation is not replayed.
func TestBootstrapTransactionStopsOnPermanentFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectRollback()
	attempts := 0
	_, err = runReconcileTransaction(context.Background(), db, false, func(*sql.Tx) (*bootstrap.Sections, error) {
		attempts++
		return nil, fmt.Errorf("nodes: %w", &pq.Error{Code: "23505"})
	})
	if err == nil || attempts != 1 || database.IsRetryablePostgresError(err) {
		t.Fatalf("attempts=%d err=%v, want one attempt and the permanent error", attempts, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
