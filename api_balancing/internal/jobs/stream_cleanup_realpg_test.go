//go:build schema_verify

package jobs

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/api_balancing/internal/artifacts"
	"frameworks/api_balancing/internal/control"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// fakeThumbS3 records DeletePrefix calls and can be told to fail, so the drainer's sweep + retry paths are
// observable without a real bucket.
type fakeThumbS3 struct {
	mu       sync.Mutex
	prefixes []string
	failNext bool
}

func (f *fakeThumbS3) Delete(context.Context, string) error { return nil }
func (f *fakeThumbS3) DeletePrefix(_ context.Context, prefix string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		return 0, errors.New("simulated s3 failure")
	}
	f.prefixes = append(f.prefixes, prefix)
	return 1, nil
}
func (f *fakeThumbS3) ParseS3URL(s string) (string, error) { return s, nil }

// Exercises the stream-cleanup drainer (invariant I3: durable convergence) against the REAL foghorn.sql baseline.
// A pending obligation left behind (as if the RPC recorded it and then crashed) is drained: the drainer sweeps
// the thumbnail prefix on the snapshotted destination cluster, deletes the control rows, and marks the row
// 'cleaned' — and a transient S3 failure is retried from the durable row rather than lost.
func TestStreamCleanupDrainer_ConvergesFromDurableRow_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	ctx := context.Background()
	files := []string{"poster.jpg", "sprite.jpg", "sprite.vtt"}
	expiry := time.Now().Add(time.Hour)

	// A live stream with a claimed attempt on the LOCAL cluster, then a durable obligation (the tombstone).
	if ok, err := control.ClaimThumbnailAttempt(ctx, conn, "att-drain-1", "tenant-a", "stream-drain", "node-1", "local", files, expiry); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if err := control.RecordStreamCleanupObligation(ctx, conn, "tenant-a", "stream-drain"); err != nil {
		t.Fatalf("record: %v", err)
	}
	// Backdate the tombstone past the max-copy window so the sweep marks 'cleaned' on its first success (this test
	// exercises convergence, not the delayed-resweep timing — see TestStreamCleanupDrainer_DelayedResweep_RealPG).
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.stream_cleanup_obligation SET enqueued_at = NOW() - INTERVAL '30 minutes' WHERE asset_key = 'stream-drain'`); err != nil {
		t.Fatal(err)
	}

	fake := &fakeThumbS3{failNext: true} // first pass fails the S3 sweep
	job := NewStreamCleanupJob(StreamCleanupConfig{
		DB:          conn,
		Cleaner:     &artifacts.Cleaner{LocalCluster: "local", S3: fake, LocalBackendID: testCellBackendID},
		Logger:      logging.NewLogger(),
		Interval:    time.Hour,
		BackoffBase: time.Millisecond, // so the retry becomes due immediately
	})

	// Pass 1: the sweep fails → obligation stays pending, its attempts increment (backoff), control rows retained.
	job.drain()
	var status string
	var attempts int
	if err := conn.QueryRowContext(ctx, `SELECT status, attempts FROM foghorn.stream_cleanup_obligation WHERE asset_key = $1`, "stream-drain").Scan(&status, &attempts); err != nil {
		t.Fatalf("read obligation after fail: %v", err)
	}
	if status != "pending" || attempts != 1 {
		t.Fatalf("after failed sweep: status=%q attempts=%d; want pending/1", status, attempts)
	}
	var assignN int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.thumbnail_task_assignment WHERE asset_key = $1`, "stream-drain").Scan(&assignN); err != nil {
		t.Fatalf("count assignments: %v", err)
	}
	if assignN == 0 {
		t.Fatal("control rows must be RETAINED when the sweep fails")
	}

	// Pass 2: the sweep succeeds → bytes swept, control rows dropped, row marked cleaned.
	fake.failNext = false
	time.Sleep(5 * time.Millisecond) // clear the tiny backoff so the row is due again
	job.drain()

	if err := conn.QueryRowContext(ctx, `SELECT status FROM foghorn.stream_cleanup_obligation WHERE asset_key = $1`, "stream-drain").Scan(&status); err != nil {
		t.Fatalf("read after success: %v", err)
	}
	if status != "cleaned" {
		t.Fatalf("after successful sweep: status=%q; want cleaned", status)
	}
	fake.mu.Lock()
	sweptStream := false
	for _, p := range fake.prefixes {
		if strings.Contains(p, "stream-drain") {
			sweptStream = true
		}
	}
	fake.mu.Unlock()
	if !sweptStream {
		t.Fatalf("expected a thumbnail prefix sweep for stream-drain, got %v", fake.prefixes)
	}
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.thumbnail_task_assignment WHERE asset_key = $1`, "stream-drain").Scan(&assignN); err != nil {
		t.Fatalf("count assignments after clean: %v", err)
	}
	if assignN != 0 {
		t.Fatalf("control rows must be dropped after a successful sweep, got %d", assignN)
	}
	// The tombstone row PERSISTS (cleaned) so late reads still resolve GONE.
	if ts, err := control.AssetTombstoned(ctx, conn, "stream-drain"); err != nil || !ts {
		t.Fatalf("tombstone must persist after cleanup: ts=%v err=%v", ts, err)
	}
}

// I2 regression: a thumbnail whose official destination is an ALIAS cluster (id != this cell's) but is backed by
// THIS cell's local S3 must sweep LOCALLY. The target is OWNED by its recorded backend_id (this cell's store), and
// the sweep routes local via backend_local — NOT by the alias cluster id. The Cleaner has NO federation delegate, so
// if cleanup misrouted to remote (the pre-fix cluster-id-compare bug) the sweep would fail ErrDelegateMissing and the
// obligation would stay pending forever — the leak this guards against.
func TestStreamCleanupDrainer_LocallyBackedAliasSweepsLocally_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	ctx := context.Background()
	files := []string{"poster.jpg", "sprite.jpg", "sprite.vtt"}

	// destination_cluster "official-alias" != Cleaner.LocalCluster "local", but the mint recorded THIS cell's local
	// backend_id (bytes are local) — so the target is owned by backend_id and the sweep routes local.
	if ok, err := control.ClaimThumbnailAttempt(ctx, conn, "att-alias", "tenant-a", "stream-alias", "node-1", "official-alias", files, time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.thumbnail_task_assignment SET backend_id = 'BE-local' WHERE attempt_id = 'att-alias'`); err != nil {
		t.Fatalf("set backend_id: %v", err)
	}
	if err := control.RecordStreamCleanupObligation(ctx, conn, "tenant-a", "stream-alias"); err != nil {
		t.Fatalf("record: %v", err)
	}
	// Past the max-copy window so the first successful sweep reclaims the target + finalizes (delayed-resweep timing
	// tested separately).
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.stream_cleanup_obligation SET enqueued_at = NOW() - INTERVAL '30 minutes' WHERE asset_key = 'stream-alias'`); err != nil {
		t.Fatal(err)
	}

	fake := &fakeThumbS3{}
	job := NewStreamCleanupJob(StreamCleanupConfig{
		DB:       conn,
		Cleaner:  &artifacts.Cleaner{LocalCluster: "local", S3: fake, LocalBackendID: "BE-local"}, // matches the seeded assignment backend; Delegate nil on purpose
		Logger:   logging.NewLogger(),
		Interval: time.Hour,
	})
	job.drain()

	var status string
	if err := conn.QueryRowContext(ctx, `SELECT status FROM foghorn.stream_cleanup_obligation WHERE asset_key = $1`, "stream-alias").Scan(&status); err != nil {
		t.Fatalf("read obligation: %v", err)
	}
	if status != "cleaned" {
		t.Fatalf("locally-backed alias must sweep locally + clean (no federation misroute), got status=%q", status)
	}
	fake.mu.Lock()
	swept := false
	for _, p := range fake.prefixes {
		if p == "thumbnails/stream-alias/" {
			swept = true
		}
	}
	fake.mu.Unlock()
	if !swept {
		t.Fatalf("expected a LOCAL prefix sweep for the alias-backed-locally asset, got %v", fake.prefixes)
	}
}

// I2 repoint guard: a locally-backed obligation whose RECORDED backend_id no longer matches the cell's currently
// configured local store (a repoint since the write) must FAIL CLOSED — never sweep the current (wrong) store — so
// the operator keeps the old backend until cleanup drains.
func TestStreamCleanupDrainer_RepointGuardFailsClosed_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	ctx := context.Background()
	files := []string{"poster.jpg"}

	if ok, err := control.ClaimThumbnailAttempt(ctx, conn, "att-repoint", "tenant-a", "stream-repoint", "node-1", "official-alias", files, time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	// The attempt recorded it wrote to backend OLD-backend.
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.thumbnail_task_assignment SET backend_id = 'OLD-backend' WHERE attempt_id = 'att-repoint'`); err != nil {
		t.Fatalf("set backend_id: %v", err)
	}
	if err := control.RecordStreamCleanupObligation(ctx, conn, "tenant-a", "stream-repoint"); err != nil {
		t.Fatalf("record: %v", err)
	}

	fake := &fakeThumbS3{}
	// The cell's CURRENT local store is a DIFFERENT backend — a repoint since the write.
	job := NewStreamCleanupJob(StreamCleanupConfig{
		DB:       conn,
		Cleaner:  &artifacts.Cleaner{LocalCluster: "local", S3: fake, LocalBackendID: "NEW-backend"},
		Logger:   logging.NewLogger(),
		Interval: time.Hour,
	})
	job.drain()

	var status string
	if err := conn.QueryRowContext(ctx, `SELECT status FROM foghorn.stream_cleanup_obligation WHERE asset_key = $1`, "stream-repoint").Scan(&status); err != nil {
		t.Fatalf("read obligation: %v", err)
	}
	if status != "pending" {
		t.Fatalf("a repointed backend must fail closed (stay pending for the operator), got status=%q", status)
	}
	fake.mu.Lock()
	sweptAny := len(fake.prefixes) > 0
	fake.mu.Unlock()
	if sweptAny {
		t.Fatalf("a repointed backend must NOT sweep the current (wrong) store, swept %v", fake.prefixes)
	}
}

// The deterministic-projection copy is a non-transactional S3 op whose destination write is unconditional, so a copy
// accepted just before a deletion's tombstone became visible can complete AFTER the first sweep and RESURRECT the
// object. The drainer therefore performs a DELAYED SECOND sweep: within DeterministicCopyWindow of the tombstone the
// obligation stays 'pending' (re-swept once more past the last possible straggler); only past the window is it 'cleaned'.
func TestStreamCleanupDrainer_DelayedResweep_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	ctx := context.Background()
	files := []string{"poster.jpg"}

	if ok, err := control.ClaimThumbnailAttempt(ctx, conn, "att-resweep", "tenant-a", "stream-resweep", "node-1", "local", files, time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if err := control.RecordStreamCleanupObligation(ctx, conn, "tenant-a", "stream-resweep"); err != nil {
		t.Fatalf("record: %v", err)
	}

	fake := &fakeThumbS3{}
	job := NewStreamCleanupJob(StreamCleanupConfig{
		DB:       conn,
		Cleaner:  &artifacts.Cleaner{LocalCluster: "local", S3: fake, LocalBackendID: testCellBackendID},
		Logger:   logging.NewLogger(),
		Interval: time.Hour,
	})

	// Pass 1 (fresh tombstone, WITHIN the window): sweep the prefix but DO NOT finalize — arm the second sweep. The
	// obligation stays pending, records first_swept_at, and is rescheduled to enqueued_at + window.
	job.drain()
	var status string
	var sweptFirst, dueAfterWindow bool
	if err := conn.QueryRowContext(ctx, `
		SELECT status,
		       first_swept_at IS NOT NULL,
		       next_attempt_at >= enqueued_at + ($2 * INTERVAL '1 second') - INTERVAL '1 second'
		  FROM foghorn.stream_cleanup_obligation
		 WHERE asset_key = $1`,
		"stream-resweep", int64(control.DeterministicCopyWindow.Seconds())).Scan(&status, &sweptFirst, &dueAfterWindow); err != nil {
		t.Fatalf("read obligation after pass 1: %v", err)
	}
	if status != "pending" {
		t.Fatalf("within the max-copy window the obligation must stay pending (resweep scheduled), got %q", status)
	}
	if !sweptFirst {
		t.Fatal("the first sweep must record first_swept_at")
	}
	if !dueAfterWindow {
		t.Fatal("the second sweep must be scheduled at ~enqueued_at + DeterministicCopyWindow")
	}
	fake.mu.Lock()
	firstSweeps := len(fake.prefixes)
	fake.mu.Unlock()
	if firstSweeps != 1 {
		t.Fatalf("the first pass must sweep the prefix once, got %d", firstSweeps)
	}

	// Simulate the window elapsing: backdate the tombstone AND make the obligation due for its second sweep.
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.stream_cleanup_obligation SET enqueued_at = NOW() - INTERVAL '30 minutes', next_attempt_at = NOW() WHERE asset_key = 'stream-resweep'`); err != nil {
		t.Fatal(err)
	}

	// Pass 2 (past the window): the delayed second sweep runs and finalizes → cleaned.
	job.drain()
	if err := conn.QueryRowContext(ctx, `SELECT status FROM foghorn.stream_cleanup_obligation WHERE asset_key = $1`, "stream-resweep").Scan(&status); err != nil {
		t.Fatalf("read after pass 2: %v", err)
	}
	if status != "cleaned" {
		t.Fatalf("past the window the obligation must be cleaned, got %q", status)
	}
	fake.mu.Lock()
	totalSweeps := len(fake.prefixes)
	fake.mu.Unlock()
	if totalSweeps != 2 {
		t.Fatalf("the delayed resweep must sweep the prefix a second time (catching a straggler), got %d total", totalSweeps)
	}
}

// Finalize atomicity: the window-gated mark-cleaned and the control-row cleanup are ONE transaction, so a crash
// between them can never drop the control rows without marking cleaned (or vice versa). We inject a failure squarely
// at the control-row cleanup by installing a BEFORE DELETE trigger on thumbnail_task_assignment that raises: the
// finalize tx marks the obligation cleaned, then tries to drop the control rows, the trigger aborts it, and the WHOLE
// tx must roll back. Post-failure the obligation is STILL pending and the control rows STILL present — no partial
// finalize. Dropping the trigger and re-draining then converges cleanly.
func TestStreamCleanupDrainer_FinalizeAtomicOnControlCleanupFailure_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	ctx := context.Background()
	files := []string{"poster.jpg"}

	if ok, err := control.ClaimThumbnailAttempt(ctx, conn, "att-atomic", "tenant-a", "stream-atomic", "node-1", "local", files, time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if err := control.RecordStreamCleanupObligation(ctx, conn, "tenant-a", "stream-atomic"); err != nil {
		t.Fatalf("record: %v", err)
	}
	// Past the max-copy window so a single drain reaches the finalize (not the delayed second sweep).
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.stream_cleanup_obligation SET enqueued_at = NOW() - INTERVAL '30 minutes' WHERE asset_key = 'stream-atomic'`); err != nil {
		t.Fatal(err)
	}

	// Injection: abort the control-row DELETE inside the finalize tx, AFTER the mark-cleaned in that same tx. Scoped to
	// this test's connection lifetime; dropped before the successful re-drain.
	if _, err := conn.ExecContext(ctx, `
		CREATE OR REPLACE FUNCTION foghorn._test_block_assignment_delete() RETURNS trigger AS $$
		BEGIN RAISE EXCEPTION 'injected: control-row cleanup blocked mid-finalize'; END; $$ LANGUAGE plpgsql`); err != nil {
		t.Fatalf("create trigger fn: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `
		CREATE TRIGGER _test_block_assignment_delete BEFORE DELETE ON foghorn.thumbnail_task_assignment
		FOR EACH ROW EXECUTE FUNCTION foghorn._test_block_assignment_delete()`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	fake := &fakeThumbS3{}
	job := NewStreamCleanupJob(StreamCleanupConfig{DB: conn, Logger: logging.NewLogger(), Interval: time.Hour,
		Cleaner:     &artifacts.Cleaner{LocalCluster: "local", S3: fake, LocalBackendID: testCellBackendID},
		BackoffBase: time.Millisecond})

	// Drain with the trigger armed: the finalize tx must abort and roll back ENTIRELY.
	job.drain()

	var status string
	var assignN int
	if err := conn.QueryRowContext(ctx, `SELECT status FROM foghorn.stream_cleanup_obligation WHERE asset_key = $1`, "stream-atomic").Scan(&status); err != nil {
		t.Fatalf("read status after injected failure: %v", err)
	}
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM foghorn.thumbnail_task_assignment WHERE asset_key = $1`, "stream-atomic").Scan(&assignN); err != nil {
		t.Fatalf("count assignments after injected failure: %v", err)
	}
	if status != "pending" || assignN == 0 {
		t.Fatalf("finalize tx must roll back atomically: status=%q assignments=%d; want pending/>0", status, assignN)
	}

	// Remove the fault and make the obligation due again (the failed settle armed a backoff), then drain: converges.
	if _, err := conn.ExecContext(ctx, `DROP TRIGGER _test_block_assignment_delete ON foghorn.thumbnail_task_assignment`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.stream_cleanup_obligation SET next_attempt_at = NOW(), leased_until = NULL WHERE asset_key = 'stream-atomic'`); err != nil {
		t.Fatalf("re-arm obligation: %v", err)
	}
	job.drain()

	if err := conn.QueryRowContext(ctx, `SELECT status FROM foghorn.stream_cleanup_obligation WHERE asset_key = $1`, "stream-atomic").Scan(&status); err != nil {
		t.Fatalf("read status after recovery: %v", err)
	}
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM foghorn.thumbnail_task_assignment WHERE asset_key = $1`, "stream-atomic").Scan(&assignN); err != nil {
		t.Fatalf("count assignments after recovery: %v", err)
	}
	if status != "cleaned" || assignN != 0 {
		t.Fatalf("post-recovery must fully converge: status=%q assignments=%d; want cleaned/0", status, assignN)
	}
	if ts, err := control.AssetTombstoned(ctx, conn, "stream-atomic"); err != nil || !ts {
		t.Fatalf("tombstone must persist after cleanup: ts=%v err=%v", ts, err)
	}
}
