package handlers

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresLedgerLeaseAcquiresAndReleasesDedicatedSessionLock(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key := ledgerLeaseNamespace + "ledger:delivery_usage_5m"
	mock.ExpectQuery(`SELECT pg_try_advisory_lock\(hashtext\(\$1\)\)`).
		WithArgs(key).
		WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(true))
	mock.ExpectQuery(`SELECT pg_advisory_unlock\(hashtext\(\$1\)\)`).
		WithArgs(key).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(true))

	release, acquired, err := NewPostgresLedgerLease(db).TryAcquire(context.Background(), "ledger:delivery_usage_5m")
	if err != nil || !acquired || release == nil {
		t.Fatalf("acquired=%v release=%v err=%v", acquired, release != nil, err)
	}
	if err := release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresLedgerLeaseReportsAnotherReplicaAsLeader(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(`SELECT pg_try_advisory_lock\(hashtext\(\$1\)\)`).
		WithArgs(ledgerLeaseNamespace + "ledger:viewer_usage_5m").
		WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(false))

	release, acquired, err := NewPostgresLedgerLease(db).TryAcquire(context.Background(), "ledger:viewer_usage_5m")
	if err != nil || acquired || release != nil {
		t.Fatalf("acquired=%v release=%v err=%v", acquired, release != nil, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
