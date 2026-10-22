package handlers

import (
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/fakesession"
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

func TestPostgresLedgerLeaseDiscardsASessionThatCannotTakeTheLock(t *testing.T) {
	db, server := fakesession.New(func(session int, _ string) (bool, error) {
		if session == 1 {
			return false, fakesession.ExpiredSessionError()
		}
		return true, nil
	})
	defer db.Close()
	lease := NewPostgresLedgerLease(db)
	ctx := context.Background()
	if _, acquired, err := lease.TryAcquire(ctx, "stale-close:viewer_sessions_anomalous"); acquired || err == nil {
		t.Fatalf("TryAcquire on the expired session = %v, %v; want the failure", acquired, err)
	}
	release, acquired, err := lease.TryAcquire(ctx, "stale-close:viewer_sessions_anomalous")
	if !acquired || err != nil {
		t.Fatalf("TryAcquire after the expired session = %v, %v; the pool handed the expired session back", acquired, err)
	}
	if err := release(ctx); err != nil {
		t.Fatal(err)
	}
	if server.Opened() != 2 || !server.Closed(1) {
		t.Fatalf("opened=%d closed(1)=%v; want the expired session closed and a second one opened", server.Opened(), server.Closed(1))
	}
}

func TestPostgresLedgerLeaseDiscardsASessionThatDidNotHoldTheLock(t *testing.T) {
	db, server := fakesession.New(func(_ int, query string) (bool, error) {
		return !strings.Contains(query, "pg_advisory_unlock"), nil
	})
	defer db.Close()
	release, acquired, err := NewPostgresLedgerLease(db).TryAcquire(context.Background(), "ledger:delivery_usage_5m")
	if !acquired || err != nil {
		t.Fatalf("TryAcquire = %v, %v", acquired, err)
	}
	if err := release(context.Background()); err == nil {
		t.Fatal("release reported success for a lock the session did not hold")
	}
	if !server.Closed(1) {
		t.Fatal("a session whose lock state is unknown went back to the pool")
	}
}
