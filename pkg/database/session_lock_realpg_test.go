//go:build schema_verify

package database

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/yugabyte/pgx/v5/stdlib"
)

// TestSessionLeader_RealPG runs two replicas' leader elections against one
// PostgreSQL: exactly one leads, leadership moves on release and on a lost
// session, and WithSessionLock waits for a holder.
func TestSessionLeader_RealPG(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-session-leader-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, runErr := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); runErr != nil {
		t.Fatalf("docker run: %v\n%s", runErr, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	dsn := fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port)
	open := func(params ...string) *sql.DB {
		db, openErr := sql.Open("pgx", dsn+strings.Join(params, ""))
		if openErr != nil {
			t.Fatal(openErr)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	replicaA, replicaB, admin := open(), open(), open()
	if err := dockerpg.WaitReady(admin, name); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	lead := func(l *SessionLeader) bool {
		t.Helper()
		ok, leadErr := l.Lead(ctx)
		if leadErr != nil {
			t.Fatalf("Lead: %v", leadErr)
		}
		return ok
	}

	a, b := NewSessionLeader(replicaA, "health-poller"), NewSessionLeader(replicaB, "health-poller")
	if !lead(a) || lead(b) {
		t.Fatal("want replica A leading and B following")
	}
	if !lead(a) || lead(b) {
		t.Fatal("leadership must stay with A while its session lives")
	}
	if other := NewSessionLeader(replicaB, "invoice-sync"); !lead(other) {
		t.Fatal("a different lock name must have its own leader")
	}

	a.Release()
	if !lead(b) || lead(a) {
		t.Fatal("want B leading after A released")
	}

	// B's session is terminated server-side, as when its host dies: the lock
	// goes with it, A takes over, and B's next Lead reports the lost lock and
	// follows from then on. The timeout form waits until the backend has exited
	// and released its locks; without it A's next Lead races the backend's
	// shutdown.
	if _, err := admin.ExecContext(ctx, `SELECT pg_terminate_backend(pid, 5000) FROM pg_locks WHERE locktype = 'advisory' AND granted AND pid <> pg_backend_pid()`); err != nil {
		t.Fatal(err)
	}
	if !lead(a) {
		t.Fatal("A did not take over the lock of the terminated session")
	}
	if ok, err := b.Lead(ctx); ok || err == nil {
		t.Fatalf("B's Lead after its session was terminated = %v, %v; want the lost lock reported", ok, err)
	}
	if lead(b) {
		t.Fatal("B leads while A holds the lock")
	}

	held := make(chan struct{})
	releaseHolder := make(chan struct{})
	go func() {
		_ = WithSessionLock(ctx, replicaA, "stripe-sync", func() error {
			close(held)
			<-releaseHolder
			return nil
		})
	}()
	<-held
	// The waiter's sessions carry a statement_timeout shorter than the hold, as the runtime roles' sessions carry one
	// shorter than a long holder: the wait must outlast it.
	waiter := open("&statement_timeout=200")
	type waitResult struct {
		took time.Duration
		err  error
	}
	waited := make(chan waitResult, 1)
	start := time.Now()
	go func() {
		err := WithSessionLock(ctx, waiter, "stripe-sync", func() error { return nil })
		waited <- waitResult{took: time.Since(start), err: err}
	}()
	time.Sleep(600 * time.Millisecond)
	close(releaseHolder)
	select {
	case result := <-waited:
		if result.err != nil {
			t.Fatalf("second WithSessionLock failed while waiting for the holder: %v", result.err)
		}
		if result.took < 600*time.Millisecond {
			t.Fatalf("second WithSessionLock ran after %s while the first held the lock", result.took)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second WithSessionLock never ran after the first released")
	}

	// A discarded session leaves the pool and ends on the server, taking its
	// lock with it, so another replica can take the lock and this pool never
	// hands that backend out again.
	single := open()
	single.SetMaxOpenConns(1)
	holder, err := single.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var holderPID int
	if err := holder.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatal(err)
	}
	var took bool
	if err := holder.QueryRowContext(ctx, tryLockSQL, sessionLockClass, "discarded").Scan(&took); err != nil || !took {
		t.Fatalf("holder take = %v, %v", took, err)
	}
	DiscardSession(holder)
	var nextPID int
	if err := single.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&nextPID); err != nil {
		t.Fatal(err)
	}
	if nextPID == holderPID {
		t.Fatal("the pool handed the discarded session out again")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		release, acquired, err := TrySessionLock(ctx, replicaB, NamedLock("discarded"))
		if err != nil {
			t.Fatal(err)
		}
		if acquired {
			if err := release(ctx); err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the discarded session's lock was never released")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
