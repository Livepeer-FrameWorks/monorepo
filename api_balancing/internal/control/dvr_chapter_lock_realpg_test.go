//go:build schema_verify

package control

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestDVRChapterMutationLockIsNamespacedAndTransactionScoped_RealPG(t *testing.T) {
	conn := startRealPG(t)
	conn.SetMaxOpenConns(4)
	previous := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(previous) })
	const hash = "chapter-lock-domain"

	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- WithDVRChapterMutationTx(context.Background(), hash, func(*sql.Tx) error {
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("chapter mutation did not acquire its lock")
	}

	// PostgreSQL's one-key and two-key advisory spaces are disjoint. Holding
	// the chapter lock for this exact hash must not block the legacy one-key
	// ingest domain even before considering ordinary hash collisions.
	var ingestLockAcquired bool
	if err := conn.QueryRow(`SELECT pg_try_advisory_xact_lock(hashtext($1)::bigint)`, hash).Scan(&ingestLockAcquired); err != nil {
		t.Fatal(err)
	}
	if !ingestLockAcquired {
		t.Fatal("chapter mutation lock collided with the one-key ingest namespace")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	want := errors.New("callback failed")
	if err := WithDVRChapterMutationTx(context.Background(), hash, func(*sql.Tx) error { return want }); !errors.Is(err, want) {
		t.Fatalf("callback error = %v, want %v", err, want)
	}
	var reacquired bool
	if err := conn.QueryRow(`SELECT pg_try_advisory_xact_lock($1::integer, hashtext($2))`, dvrChapterMutationLockNamespace, hash).Scan(&reacquired); err != nil {
		t.Fatal(err)
	}
	if !reacquired {
		t.Fatal("callback failure leaked the chapter mutation lock")
	}

	blockedHash := hash + "-cancel"
	blocker, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.Exec(`SELECT pg_advisory_xact_lock($1::integer, hashtext($2))`, dvrChapterMutationLockNamespace, blockedHash); err != nil {
		_ = blocker.Rollback()
		t.Fatal(err)
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancelDone := make(chan error, 1)
	var callbackEntered bool
	go func() {
		cancelDone <- WithDVRChapterMutationTx(cancelCtx, blockedHash, func(*sql.Tx) error {
			callbackEntered = true
			return nil
		})
	}()
	waitDeadline := time.NewTimer(5 * time.Second)
	defer waitDeadline.Stop()
	for {
		var waiting bool
		if err := conn.QueryRow(`
SELECT EXISTS (
    SELECT 1 FROM pg_stat_activity
    WHERE datname = current_database()
      AND state = 'active'
      AND wait_event_type = 'Lock'
      AND wait_event = 'advisory'
      AND query LIKE '%pg_advisory_xact_lock%'
)`).Scan(&waiting); err != nil {
			_ = blocker.Rollback()
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case acquireErr := <-cancelDone:
			_ = blocker.Rollback()
			t.Fatalf("blocked acquisition returned before cancellation: %v", acquireErr)
		case <-waitDeadline.C:
			_ = blocker.Rollback()
			t.Fatal("chapter mutation never waited on the held advisory lock")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	acquireErr := <-cancelDone
	var sqlState interface{ SQLState() string }
	if !errors.Is(acquireErr, context.Canceled) && (!errors.As(acquireErr, &sqlState) || sqlState.SQLState() != "57014") {
		_ = blocker.Rollback()
		t.Fatalf("cancelled acquisition error = %v, want context cancellation/SQLSTATE 57014", acquireErr)
	}
	if callbackEntered {
		_ = blocker.Rollback()
		t.Fatal("callback ran without acquiring the chapter mutation lock")
	}
	if err := blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	var cancelReacquired bool
	if err := conn.QueryRow(`SELECT pg_try_advisory_xact_lock($1::integer, hashtext($2))`, dvrChapterMutationLockNamespace, blockedHash).Scan(&cancelReacquired); err != nil {
		t.Fatal(err)
	}
	if !cancelReacquired {
		t.Fatal("cancelled acquisition leaked the chapter mutation lock")
	}
}
