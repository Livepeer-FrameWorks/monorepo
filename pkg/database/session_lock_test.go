package database

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestSessionLeaderKeepsLeadingOnItsLiveSession(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("pg_try_advisory_lock").WithArgs(sessionLockClass, "job").
		WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(true))
	mock.ExpectPing()
	mock.ExpectExec("pg_advisory_unlock").WithArgs(sessionLockClass, "job").WillReturnResult(sqlmock.NewResult(0, 1))

	leader := NewSessionLeader(db, "job")
	for i := range 2 {
		lead, leadErr := leader.Lead(context.Background())
		if !lead || leadErr != nil {
			t.Fatalf("Lead #%d = %v, %v; want the held lock", i+1, lead, leadErr)
		}
	}
	leader.Release()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionLeaderFollowsWhileAnotherReplicaLeads(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("pg_try_advisory_lock").WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(false))

	lead, err := NewSessionLeader(db, "job").Lead(context.Background())
	if lead || err != nil {
		t.Fatalf("Lead = %v, %v; want follower with no error", lead, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionLeaderRetakesTheLockAfterItsSessionDies(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("pg_try_advisory_lock").WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(true))
	mock.ExpectPing().WillReturnError(errors.New("connection reset by peer"))
	mock.ExpectQuery("pg_try_advisory_lock").WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(false))

	leader := NewSessionLeader(db, "job")
	if lead, _ := leader.Lead(context.Background()); !lead {
		t.Fatal("first Lead did not take the free lock")
	}
	// The lost session released the lock server-side and another replica took
	// it, so this replica must not keep acting as leader.
	if lead, leadErr := leader.Lead(context.Background()); lead || leadErr != nil {
		t.Fatalf("Lead after a dead session = %v, %v; want follower", lead, leadErr)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionLeaderReportsWhyItCouldNotAsk(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("pg_try_advisory_lock").WillReturnError(errors.New("database is starting up"))
	if lead, leadErr := NewSessionLeader(db, "job").Lead(context.Background()); lead || leadErr == nil {
		t.Fatalf("Lead = %v, %v; want the failure reason", lead, leadErr)
	}
}

func TestWithSessionLockRunsFnBetweenLockAndUnlock(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Another session holds the lock on the first ask.
	mock.ExpectQuery(`pg_try_advisory_lock\(`).WithArgs(sessionLockClass, "sync").
		WillReturnRows(sqlmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(false))
	mock.ExpectQuery(`pg_try_advisory_lock\(`).WithArgs(sessionLockClass, "sync").
		WillReturnRows(sqlmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(true))
	mock.ExpectExec("UPDATE marker").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("pg_advisory_unlock").WithArgs(sessionLockClass, "sync").WillReturnResult(sqlmock.NewResult(0, 1))

	err = WithSessionLock(context.Background(), db, "sync", func() error {
		_, execErr := db.ExecContext(context.Background(), "UPDATE marker")
		return execErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWithSessionLockSkipsFnWhenTheLockIsNotTaken(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(`pg_try_advisory_lock\(`).WillReturnError(context.DeadlineExceeded)
	ran := false
	err = WithSessionLock(context.Background(), db, "sync", func() error { ran = true; return nil })
	if err == nil || ran {
		t.Fatalf("WithSessionLock = %v, ran=%v; want an error and fn not run", err, ran)
	}
}
