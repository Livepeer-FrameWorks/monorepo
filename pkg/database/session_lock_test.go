package database

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/fakesession"
)

func TestSessionLeaderKeepsLeadingOnItsLiveSession(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("pg_try_advisory_lock").WithArgs(sessionLockClass, "job").
		WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(true))
	// The second Lead proves the hold with a re-entrant re-take and its unlock.
	mock.ExpectQuery("pg_try_advisory_lock").WithArgs(sessionLockClass, "job").
		WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(true))
	mock.ExpectQuery("pg_advisory_unlock").WithArgs(sessionLockClass, "job").
		WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(true))
	mock.ExpectQuery("pg_advisory_unlock").WithArgs(sessionLockClass, "job").
		WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(true))

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
	var tries int
	db, server := fakesession.New(func(session int, query string) (bool, error) {
		if session == 1 {
			tries++
			if tries == 1 {
				return true, nil
			}
			return false, errors.New("connection reset by peer")
		}
		// The lost session released the lock server-side and another replica
		// took it.
		return false, nil
	})
	defer db.Close()

	leader := NewSessionLeader(db, "job")
	if lead, _ := leader.Lead(context.Background()); !lead {
		t.Fatal("first Lead did not take the free lock")
	}
	if lead, leadErr := leader.Lead(context.Background()); lead || leadErr == nil {
		t.Fatalf("Lead after a dead session = %v, %v; want the loss reported", lead, leadErr)
	}
	if lead, leadErr := leader.Lead(context.Background()); lead || leadErr != nil {
		t.Fatalf("Lead while another replica holds the lock = %v, %v; want follower", lead, leadErr)
	}
	if server.Opened() != 2 || !server.Closed(1) {
		t.Fatalf("opened=%d closed(1)=%v; want the dead session closed and a new one asking", server.Opened(), server.Closed(1))
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
	mock.ExpectQuery("pg_advisory_unlock").WithArgs(sessionLockClass, "sync").
		WillReturnRows(sqlmock.NewRows([]string{"pg_advisory_unlock"}).AddRow(true))

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

// expiredFirstSession answers every statement on session 1 the way YugabyteDB
// answers on a backend whose session-level transaction it expired; later
// sessions take and drop the lock.
func expiredFirstSession(session int, _ string) (bool, error) {
	if session == 1 {
		return false, fakesession.ExpiredSessionError()
	}
	return true, nil
}

func TestWithSessionLockDiscardsASessionThatCannotTakeTheLock(t *testing.T) {
	db, server := fakesession.New(expiredFirstSession)
	defer db.Close()
	ctx := context.Background()
	if err := WithSessionLock(ctx, db, "sync", func() error { return nil }); err == nil {
		t.Fatal("WithSessionLock on the expired session succeeded")
	}
	if err := WithSessionLock(ctx, db, "sync", func() error { return nil }); err != nil {
		t.Fatalf("WithSessionLock after the expired session = %v; the pool handed the expired session back", err)
	}
	if server.Opened() != 2 || !server.Closed(1) {
		t.Fatalf("opened=%d closed(1)=%v; want the expired session closed and a second one opened", server.Opened(), server.Closed(1))
	}
}

func TestWithSessionLockDiscardsASessionThatCannotDropTheLock(t *testing.T) {
	db, server := fakesession.New(func(session int, query string) (bool, error) {
		if session == 1 && strings.Contains(query, "pg_advisory_unlock") {
			return false, fakesession.ExpiredSessionError()
		}
		return true, nil
	})
	defer db.Close()
	ctx := context.Background()
	if err := WithSessionLock(ctx, db, "sync", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !server.Closed(1) {
		t.Fatal("a session whose unlock failed went back to the pool still holding the lock")
	}
	if err := WithSessionLock(ctx, db, "sync", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if server.Opened() != 2 {
		t.Fatalf("opened=%d; want a fresh session after the discarded one", server.Opened())
	}
}

func TestSessionLeaderDiscardsASessionThatCannotTakeTheLock(t *testing.T) {
	db, server := fakesession.New(expiredFirstSession)
	defer db.Close()
	leader := NewSessionLeader(db, "job")
	if lead, err := leader.Lead(context.Background()); lead || err == nil {
		t.Fatalf("Lead on the expired session = %v, %v; want the failure", lead, err)
	}
	if lead, err := leader.Lead(context.Background()); !lead || err != nil {
		t.Fatalf("Lead after the expired session = %v, %v; the pool handed the expired session back", lead, err)
	}
	if server.Opened() != 2 || !server.Closed(1) {
		t.Fatalf("opened=%d closed(1)=%v; want the expired session closed and a second one opened", server.Opened(), server.Closed(1))
	}
}

// YugabyteDB expires a session-level transaction during a storage stall and
// drops the session's advisory locks with it while the connection still
// answers pings.
func TestSessionLeaderStopsLeadingWhenItsSessionExpired(t *testing.T) {
	var tries int
	db, server := fakesession.New(func(session int, query string) (bool, error) {
		if session == 1 {
			if strings.Contains(query, "pg_try_advisory_lock") {
				tries++
				if tries == 1 {
					return true, nil
				}
			}
			return false, fakesession.ExpiredSessionError()
		}
		return true, nil
	})
	defer db.Close()
	leader := NewSessionLeader(db, "job")
	ctx := context.Background()
	if lead, err := leader.Lead(ctx); !lead || err != nil {
		t.Fatalf("first Lead = %v, %v; want the free lock", lead, err)
	}
	if lead, _ := leader.Lead(ctx); lead {
		t.Fatal("Lead still reports leadership on a session whose lock expired")
	}
	if !server.Closed(1) {
		t.Fatal("the expired session went back to the pool")
	}
	if lead, err := leader.Lead(ctx); !lead || err != nil || server.Opened() != 2 {
		t.Fatalf("Lead after dropping the expired session = %v, %v, opened=%d; want the lock on a new session", lead, err, server.Opened())
	}
}

func TestSessionLeaderStopsLeadingWhenItsSessionNoLongerHoldsTheLock(t *testing.T) {
	var tries int
	db, server := fakesession.New(func(session int, query string) (bool, error) {
		if session == 1 && strings.Contains(query, "pg_try_advisory_lock") {
			tries++
			return tries == 1, nil
		}
		return true, nil
	})
	defer db.Close()
	leader := NewSessionLeader(db, "job")
	ctx := context.Background()
	if lead, err := leader.Lead(ctx); !lead || err != nil {
		t.Fatalf("first Lead = %v, %v; want the free lock", lead, err)
	}
	if lead, _ := leader.Lead(ctx); lead {
		t.Fatal("Lead still reports leadership after another session took the lock")
	}
	if !server.Closed(1) {
		t.Fatal("the session that lost the lock went back to the pool")
	}
}

func TestTrySessionLockPoolsOnlySessionsWithACleanLockState(t *testing.T) {
	ctx := context.Background()
	t.Run("expired session", func(t *testing.T) {
		db, server := fakesession.New(expiredFirstSession)
		defer db.Close()
		if _, acquired, err := TrySessionLock(ctx, db, NamedLock("job")); acquired || err == nil {
			t.Fatalf("TrySessionLock on the expired session = %v, %v; want the failure", acquired, err)
		}
		release, acquired, err := TrySessionLock(ctx, db, NamedLock("job"))
		if !acquired || err != nil {
			t.Fatalf("TrySessionLock after the expired session = %v, %v", acquired, err)
		}
		if err := release(ctx); err != nil {
			t.Fatal(err)
		}
		if server.Opened() != 2 || !server.Closed(1) || server.Closed(2) {
			t.Fatalf("opened=%d closed(1)=%v closed(2)=%v; want the expired session discarded and the clean one pooled", server.Opened(), server.Closed(1), server.Closed(2))
		}
	})
	t.Run("unlock finds the lock not held", func(t *testing.T) {
		db, server := fakesession.New(func(_ int, query string) (bool, error) {
			return !strings.Contains(query, "pg_advisory_unlock"), nil
		})
		defer db.Close()
		release, acquired, err := TrySessionLock(ctx, db, HashtextLock("job"))
		if !acquired || err != nil {
			t.Fatalf("TrySessionLock = %v, %v", acquired, err)
		}
		if err := release(ctx); err == nil {
			t.Fatal("release reported success for a lock the session did not hold")
		}
		if !server.Closed(1) {
			t.Fatal("a session that did not hold its lock went back to the pool")
		}
	})
	t.Run("another session holds the lock", func(t *testing.T) {
		db, server := fakesession.New(func(int, string) (bool, error) { return false, nil })
		defer db.Close()
		for range 2 {
			if _, acquired, err := TrySessionLock(ctx, db, NamedLock("job")); acquired || err != nil {
				t.Fatalf("TrySessionLock = %v, %v; want not acquired", acquired, err)
			}
		}
		if server.Opened() != 1 || server.Closed(1) {
			t.Fatalf("opened=%d closed(1)=%v; a clean not-acquired session belongs in the pool", server.Opened(), server.Closed(1))
		}
	})
}

// The leader's hold check must leave the session holding the lock exactly
// once, so one unlock on Release frees it.
func TestSessionLeaderHoldCheckKeepsOneHold(t *testing.T) {
	db, server := fakesession.New(func(int, string) (bool, error) { return true, nil })
	defer db.Close()
	leader := NewSessionLeader(db, "job")
	for range 3 {
		if lead, err := leader.Lead(context.Background()); !lead || err != nil {
			t.Fatalf("Lead = %v, %v", lead, err)
		}
	}
	leader.Release()
	var takes, unlocks int
	for _, q := range server.Statements(1) {
		switch {
		case strings.Contains(q, "pg_try_advisory_lock"):
			takes++
		case strings.Contains(q, "pg_advisory_unlock"):
			unlocks++
		}
	}
	if takes != 3 || unlocks != 3 || server.Closed(1) {
		t.Fatalf("takes=%d unlocks=%d closed=%v; want every take matched by an unlock and the session pooled", takes, unlocks, server.Closed(1))
	}
}
