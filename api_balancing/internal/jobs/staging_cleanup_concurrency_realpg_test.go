//go:build schema_verify

package jobs

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
)

const (
	concurrentCleanupRows     = 20000
	concurrentCleanupReplicas = 3
	concurrentCleanupBackend  = "local-backend"
)

// countingStagingDeleter is a thread-safe S3 stand-in that records how many times each key was deleted. Each request
// costs a fixed latency, so a worker that deletes one key per request pays it per key.
type countingStagingDeleter struct {
	mu       sync.Mutex
	deletes  map[string]int
	requests atomic.Int64
}

func newCountingStagingDeleter() *countingStagingDeleter {
	return &countingStagingDeleter{deletes: make(map[string]int)}
}

func (d *countingStagingDeleter) record(keys ...string) {
	d.requests.Add(1)
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, k := range keys {
		d.deletes[k]++
	}
}

func (d *countingStagingDeleter) Delete(_ context.Context, key string) error {
	time.Sleep(2 * time.Millisecond)
	d.record(key)
	return nil
}

func (d *countingStagingDeleter) DeleteKeys(_ context.Context, keys []string) map[string]error {
	time.Sleep(20 * time.Millisecond)
	d.record(keys...)
	return nil
}

// TestStagingCleanupConcurrentReplicas_RealPG runs three cleanup workers, as three Foghorn replicas of one cell do,
// over a 20k-row due backlog on PostgreSQL.
func TestStagingCleanupConcurrentReplicas_RealPG(t *testing.T) {
	verifyStagingCleanupConcurrentReplicas(t, startRealPGForCleanup(t), "")
}

// TestStagingCleanupConcurrentReplicas_RealYugabyte runs the same backlog on YugabyteDB, where the queue is a
// distributed table whose due index is hash-sharded, as in production.
func TestStagingCleanupConcurrentReplicas_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "foghorn", "foghorn")
	if !ok {
		t.Skip("needs the shared Yugabyte contract engine; run make verify-yugabyte-service SERVICE=foghorn")
	}
	verifyStagingCleanupConcurrentReplicas(t, db, strings.TrimSpace(os.Getenv(dockerpg.SharedYugabyteContainerEnv)))
}

// verifyStagingCleanupConcurrentReplicas proves that concurrent workers delete every queued object exactly once and
// drain the queue, and bounds the number of claim statements the drain issues. Every claim statement is a scan of the
// queue (on Yugabyte a scan of the whole table), so the bound is what keeps a backlog from turning into a scan storm.
func verifyStagingCleanupConcurrentReplicas(t *testing.T, db *sql.DB, ybContainer string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	db.SetMaxOpenConns(4 * concurrentCleanupReplicas)

	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.staging_cleanup_queue (object_key, next_attempt_at, backend_id)
SELECT 'tenant/clips/' || md5(g::text) || '.mp4.staging.att-' || g, NOW() - INTERVAL '1 hour', $1
FROM generate_series(1, $2::int) g`, concurrentCleanupBackend, concurrentCleanupRows); err != nil {
		t.Fatalf("seed queue: %v", err)
	}

	claimsBefore, claimStats := claimStatementStats(ctx, t, db)
	skipsBefore := yugabyteSkipLockingLines(t, ybContainer)

	deleter := newCountingStagingDeleter()
	start := time.Now()
	var wg sync.WaitGroup
	passes := make([]int, concurrentCleanupReplicas)
	for i := range concurrentCleanupReplicas {
		job := NewStagingCleanupJob(StagingCleanupConfig{
			DB: db, S3: deleter, Logger: logging.NewLogger(), LocalBackendID: concurrentCleanupBackend,
		})
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				job.drain()
				passes[i]++
				var claimable int
				if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.staging_cleanup_queue
					WHERE next_attempt_at <= NOW() AND (leased_until IS NULL OR leased_until <= NOW())`).Scan(&claimable); err != nil {
					t.Errorf("count claimable rows: %v", err)
					return
				}
				if claimable == 0 {
					return
				}
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	if ctx.Err() != nil {
		t.Fatalf("workers did not drain %d rows within the deadline", concurrentCleanupRows)
	}

	var remaining int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.staging_cleanup_queue`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	deleter.mu.Lock()
	distinct, doubles := len(deleter.deletes), 0
	for _, n := range deleter.deletes {
		if n > 1 {
			doubles++
		}
	}
	deleter.mu.Unlock()

	claimsAfter, _ := claimStatementStats(ctx, t, db)
	skips := yugabyteSkipLockingLines(t, ybContainer) - skipsBefore
	claims := claimsAfter - claimsBefore
	t.Logf("drained %d rows with %d replicas in %s: %d S3 requests, passes per replica %v, claim statements %d (stats available: %v), kSkipLocking log lines %d",
		concurrentCleanupRows, concurrentCleanupReplicas, elapsed.Round(time.Millisecond), deleter.requests.Load(), passes, claims, claimStats, skips)

	if doubles != 0 {
		t.Errorf("%d keys were deleted from S3 more than once", doubles)
	}
	if distinct != concurrentCleanupRows {
		t.Errorf("S3 saw %d distinct keys, want %d", distinct, concurrentCleanupRows)
	}
	if remaining != 0 {
		t.Errorf("%d queue rows remain after the drain", remaining)
	}
	// Each claim statement scans the queue. A 20k backlog drains in a few dozen claims at the production batch size;
	// one claim per handful of objects is the scan storm that pinned the production tablet.
	if claimStats && claims > 200 {
		t.Errorf("drain issued %d claim statements for %d rows, want at most 200", claims, concurrentCleanupRows)
	}
}

// claimStatementStats returns the number of claim statements this database has executed, from pg_stat_statements. The
// boolean is false when the engine does not collect statement statistics (stock PostgreSQL without the preload).
func claimStatementStats(ctx context.Context, t *testing.T, db *sql.DB) (int64, bool) {
	t.Helper()
	var calls sql.NullInt64
	err := db.QueryRowContext(ctx, `
SELECT SUM(calls) FROM pg_stat_statements
WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
  AND query LIKE '%UPDATE foghorn.staging_cleanup_queue%SET leased_until%'`).Scan(&calls)
	if err != nil {
		return 0, false
	}
	return calls.Int64, true
}

// yugabyteSkipLockingLines counts the tserver log lines for a row lock skipped under SKIP LOCKED. The tserver logs
// these sparsely, so the count is a lower bound on skipped rows; it is reported, not asserted.
func yugabyteSkipLockingLines(t *testing.T, container string) int {
	t.Helper()
	if container == "" {
		return 0
	}
	out, err := dockerpg.CLI("exec", container, "sh", "-c",
		"cat /var/lib/frameworks-yugabyte-contract-data/logs/tserver/yb-tserver.INFO /root/var/logs/tserver/yb-tserver.INFO 2>/dev/null | grep -c kSkipLocking || true")
	if err != nil {
		t.Logf("read tserver log: %v", err)
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		t.Logf("parse kSkipLocking count %q: %v", out, err)
		return 0
	}
	return n
}
