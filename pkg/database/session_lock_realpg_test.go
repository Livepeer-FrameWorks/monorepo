//go:build schema_verify

package database

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/yugabyte/pgx/v5/stdlib"
)

// TestSessionLeader_RealPG runs the session lock contract against one
// PostgreSQL.
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
	open := sessionLockOpener(t, fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err := dockerpg.WaitReady(open(), name); err != nil {
		t.Fatal(err)
	}
	runSessionLockContract(t, open)
}

// TestSessionLeader_RealYugabyte runs the session lock contract against
// YugabyteDB, and shows that a session whose lock transaction YugabyteDB
// expired stops leading while another replica takes over.
func TestSessionLeader_RealYugabyte(t *testing.T) {
	admin, dsn, ok := dockerpg.OpenSharedYugabyteBaselineWithDSN(t, "session_lock", "bosun")
	if !ok {
		t.Fatal("run make verify-session-lock-engines to provide the isolated Yugabyte fixture")
	}
	open := sessionLockOpener(t, dsn)
	runSessionLockContract(t, open)

	// YugabyteDB keeps a session's advisory locks in a session-level
	// transaction. Aborting it, as an expiry during a storage stall does, drops
	// the locks while ordinary statements on that backend keep succeeding.
	ctx := context.Background()
	a, b := NewSessionLeader(open(), "expiring"), NewSessionLeader(open(), "expiring")
	if ok, err := a.Lead(ctx); !ok || err != nil {
		t.Fatalf("A's first Lead = %v, %v", ok, err)
	}
	var transaction string
	if err := a.conn.QueryRowContext(ctx, `SELECT yb_get_current_transaction()::text`).Scan(&transaction); err != nil {
		t.Fatal(err)
	}
	var cancelled bool
	if err := admin.QueryRowContext(ctx, `SELECT yb_cancel_transaction($1::uuid)`, transaction).Scan(&cancelled); err != nil || !cancelled {
		t.Fatalf("cancel A's lock transaction = %v, %v", cancelled, err)
	}
	// The backend learns of the abort asynchronously; until then the session
	// still answers its own lock statements as the holder.
	deadline := time.Now().Add(15 * time.Second)
	for {
		ok, err := a.Lead(ctx)
		if !ok {
			if err == nil {
				t.Fatal("A's Lead after its lock transaction expired followed without reporting the lost lock")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("A still leads 15s after its lock transaction expired")
		}
		time.Sleep(100 * time.Millisecond)
	}
	eventuallyLeads(t, b, "B never took the lock of the expired session")
	b.Release()
}

// sessionLockOpener returns a function that opens a pool on dsn with extra
// connection parameters, closed when the test ends.
func sessionLockOpener(t *testing.T, dsn string) func(params ...string) *sql.DB {
	t.Helper()
	return func(params ...string) *sql.DB {
		t.Helper()
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		query := parsed.Query()
		for _, param := range params {
			key, value, _ := strings.Cut(param, "=")
			query.Set(key, value)
		}
		parsed.RawQuery = query.Encode()
		db, err := sql.Open("pgx", parsed.String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
}

// eventuallyLeads waits for l to take its lock. A lock ends with its session
// on the server; YugabyteDB frees it shortly after the session ends.
func eventuallyLeads(t *testing.T, l *SessionLeader, failure string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		ok, err := l.Lead(context.Background())
		if err != nil {
			t.Fatalf("Lead: %v", err)
		}
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(failure)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// runSessionLockContract runs two replicas' leader elections against one
// database: exactly one leads, leadership moves on release and on a lost
// session, a session that lost its lock stops leading, WithSessionLock waits
// for a holder, and a discarded session takes its lock with it.
func runSessionLockContract(t *testing.T, open func(params ...string) *sql.DB) {
	t.Helper()
	replicaA, replicaB, admin := open(), open(), open()
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

	// A's session drops its locks without an error while no other session
	// takes the lock: A must report the loss rather than keep leading.
	if _, err := a.conn.ExecContext(ctx, `SELECT pg_advisory_unlock_all()`); err != nil {
		t.Fatal(err)
	}
	if ok, err := a.Lead(ctx); ok || err == nil {
		t.Fatalf("A's Lead after its session lost the lock = %v, %v; want the lost lock reported", ok, err)
	}
	if !lead(a) || lead(b) {
		t.Fatal("want A leading again on a new session and B following")
	}

	a.Release()
	eventuallyLeads(t, b, "B never took the lock A released")
	if lead(a) {
		t.Fatal("A leads after releasing to B")
	}

	// B's session is terminated server-side, as when its host dies: the lock
	// goes with it, A takes over, and B's next Lead reports the lost lock and
	// follows from then on. The timeout form waits until the backend has exited.
	var pid int
	if err := b.conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, `SELECT pg_terminate_backend($1, 5000)`, pid); err != nil {
		t.Fatal(err)
	}
	eventuallyLeads(t, a, "A did not take over the lock of the terminated session")
	if ok, err := b.Lead(ctx); ok || err == nil {
		t.Fatalf("B's Lead after its session was terminated = %v, %v; want the lost lock reported", ok, err)
	}
	if lead(b) {
		t.Fatal("B leads while A holds the lock")
	}
	a.Release()

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
	waiter := open("statement_timeout=200")
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
	deadline := time.Now().Add(15 * time.Second)
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
