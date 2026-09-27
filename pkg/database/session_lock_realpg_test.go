//go:build schema_verify

package database

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
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
	open := func() *sql.DB {
		db, openErr := sql.Open("pgx", dsn)
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
	// goes with it, A takes over, and B stops acting as leader on its next Lead.
	if _, err := admin.ExecContext(ctx, `SELECT pg_terminate_backend(pid) FROM pg_locks WHERE locktype = 'advisory' AND granted AND pid <> pg_backend_pid()`); err != nil {
		t.Fatal(err)
	}
	if !lead(a) {
		t.Fatal("A did not take over the lock of the terminated session")
	}
	if lead(b) {
		t.Fatal("B still leads after its session was terminated")
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
	waited := make(chan time.Duration, 1)
	start := time.Now()
	go func() {
		_ = WithSessionLock(ctx, replicaB, "stripe-sync", func() error { return nil })
		waited <- time.Since(start)
	}()
	time.Sleep(300 * time.Millisecond)
	close(releaseHolder)
	select {
	case d := <-waited:
		if d < 300*time.Millisecond {
			t.Fatalf("second WithSessionLock ran after %s while the first held the lock", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second WithSessionLock never ran after the first released")
	}
}
