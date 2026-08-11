//go:build schema_verify

package control

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// publishThumbnailToActive drives a claimed attempt all the way to 'published' + active pointer (unprojected), returning
// its lease token. It replicates the completion's verify → promote-candidate → publish CAS, WITHOUT the projection, so a
// test can then exercise the fenced projection in isolation.
func publishThumbnailToActive(t *testing.T, ctx context.Context, conn *sql.DB, tenant, asset, attempt string, files []string, expiry time.Time) string {
	t.Helper()
	if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, tenant, asset, "node-1", "cluster-a", files, expiry); err != nil || !ok {
		t.Fatalf("claim %s: ok=%v err=%v", attempt, ok, err)
	}
	tok, aErr := AcquireThumbnailPublishLease(ctx, conn, attempt, 2*time.Minute)
	if aErr != nil || tok == "" {
		t.Fatalf("acquire lease %s: %q err=%v", attempt, tok, aErr)
	}
	if ok, err := TransitionThumbnailStatus(ctx, conn, attempt, "assigned", "uploading"); err != nil || !ok {
		t.Fatalf("assigned→uploading %s: ok=%v err=%v", attempt, ok, err)
	}
	if ok, err := TransitionThumbnailStatus(ctx, conn, attempt, "uploading", "verifying"); err != nil || !ok {
		t.Fatalf("uploading→verifying %s: ok=%v err=%v", attempt, ok, err)
	}
	for _, f := range files {
		if _, err := MarkThumbnailObjectVerifiedToken(ctx, conn, attempt, f, ThumbnailVersionKey(asset, tok, f), "etag-"+f, 123, tok); err != nil {
			t.Fatalf("verify %s/%s: %v", attempt, f, err)
		}
	}
	if entered, err := EnterThumbnailPublishingToken(ctx, conn, attempt, tok); err != nil || !entered {
		t.Fatalf("enter-publishing %s: entered=%v err=%v", attempt, entered, err)
	}
	if activated, err := PublishThumbnailAttemptToken(ctx, conn, attempt, tok); err != nil || !activated {
		t.Fatalf("publish %s: activated=%v err=%v", attempt, activated, err)
	}
	return tok
}

// The projection CLAIM gate must reject a superseded / tombstoned / terminal-parent asset BEFORE the copy runs, so a
// doomed projection never calls PromoteObject on the shared deterministic key. Driven by ORDERING against the
// controlled fake S3 (no MinIO). (Straggler overwrites that slip past the gate are handled by the reassert, tested in
// TestThumbnailProjectionReassert_RealPG.)
func TestThumbnailProjectionFence_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prevDB := db
	db = conn
	t.Cleanup(func() { db = prevDB })
	ctx := context.Background()
	logger := logging.NewLoggerWithService("test")
	files := []string{"poster.jpg", "sprite.jpg", "sprite.vtt"}
	expiry := time.Now().Add(time.Hour)

	t.Run("a superseded attempt never overwrites the winner at the deterministic key", func(t *testing.T) {
		asset := "stream-supersede"
		tokA := publishThumbnailToActive(t, ctx, conn, "tenant-a", asset, "att-A", files, expiry)
		// B publishes on the same asset and supersedes A (monotonic claim_seq CAS): B now owns the active pointer.
		publishThumbnailToActive(t, ctx, conn, "tenant-a", asset, "att-B", files, expiry)

		// A's late projection: the fence sees A is no longer the active pointer and returns WITHOUT copying, so A's
		// stale bytes can never reach the shared deterministic key.
		mock := &mockS3Client{}
		marked, err := projectAndMarkThumbnailFromToken(ctx, conn, mock, "att-A", asset, "tenant-a", "cluster-a", tokA, files, logger)
		if err != nil {
			t.Fatalf("project A: %v", err)
		}
		if marked {
			t.Fatal("a superseded attempt must NOT mark projected (it lost the pointer)")
		}
		mock.mu.Lock()
		n := len(mock.promoteCalls)
		mock.mu.Unlock()
		if n != 0 {
			t.Fatalf("a superseded attempt must NOT copy to the deterministic key, got %d promotes (%v)", n, mock.promoteCalls)
		}
	})

	t.Run("a tombstoned live stream is not resurrected by a late projection", func(t *testing.T) {
		asset := "stream-tombstone"
		tok := publishThumbnailToActive(t, ctx, conn, "tenant-a", asset, "att-tomb", files, expiry)
		// Deletion records the durable cleanup obligation (the tombstone) under the same per-asset lock.
		if err := RecordStreamCleanupObligation(ctx, conn, "tenant-a", asset); err != nil {
			t.Fatalf("record obligation: %v", err)
		}
		mock := &mockS3Client{}
		marked, err := projectAndMarkThumbnailFromToken(ctx, conn, mock, "att-tomb", asset, "tenant-a", "cluster-a", tok, files, logger)
		if err != nil {
			t.Fatalf("project tombstoned: %v", err)
		}
		if marked {
			t.Fatal("a tombstoned asset must NOT mark projected")
		}
		mock.mu.Lock()
		n := len(mock.promoteCalls)
		mock.mu.Unlock()
		if n != 0 {
			t.Fatalf("a tombstoned asset must NOT copy to the deterministic key, got %d promotes", n)
		}
	})

	t.Run("a terminal artifact parent blocks projection", func(t *testing.T) {
		tenant := "11111111-1111-1111-1111-111111111111"
		asset := "abcdef0123456789abcdef0123456701" // 32-char artifact_hash
		if _, err := conn.ExecContext(ctx, `INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id) VALUES ($1, 'vod', $2)`, asset, tenant); err != nil {
			t.Fatalf("seed artifact: %v", err)
		}
		tok := publishThumbnailToActive(t, ctx, conn, tenant, asset, "att-terminal", files, expiry)
		if _, err := conn.ExecContext(ctx, `UPDATE foghorn.artifacts SET status = 'deleted' WHERE artifact_hash = $1`, asset); err != nil {
			t.Fatalf("mark artifact deleted: %v", err)
		}
		mock := &mockS3Client{}
		marked, err := projectAndMarkThumbnailFromToken(ctx, conn, mock, "att-terminal", asset, tenant, "cluster-a", tok, files, logger)
		if err != nil {
			t.Fatalf("project terminal: %v", err)
		}
		if marked {
			t.Fatal("a terminal-parent asset must NOT mark projected")
		}
		mock.mu.Lock()
		n := len(mock.promoteCalls)
		mock.mu.Unlock()
		if n != 0 {
			t.Fatalf("a terminal-parent asset must NOT copy, got %d promotes", n)
		}
		var has bool
		if err := conn.QueryRowContext(ctx, `SELECT has_thumbnails FROM foghorn.artifacts WHERE artifact_hash = $1`, asset).Scan(&has); err != nil {
			t.Fatalf("read has_thumbnails: %v", err)
		}
		if has {
			t.Fatal("has_thumbnails must never be exposed for a terminal artifact")
		}
	})
}

// The bounded-reassert convergence: because the deterministic copy is not strictly serial, the CURRENT winner re-copies
// its bytes once past the max-copy window (correcting a straggler overwrite), then clears its reassert clock. A
// superseded winner does NOT re-copy — the newer winner owns the key — but still clears its own clock (one-shot).
func TestThumbnailProjectionReassert_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prevDB := db
	db = conn
	t.Cleanup(func() { db = prevDB })
	ctx := context.Background()
	logger := logging.NewLoggerWithService("test")
	files := []string{"poster.jpg", "sprite.jpg", "sprite.vtt"}
	expiry := time.Now().Add(time.Hour)

	dueNow := func(attempt string) {
		if _, err := conn.ExecContext(ctx, `UPDATE foghorn.thumbnail_task_assignment SET deterministic_reassert_at = NOW() - INTERVAL '1 second' WHERE attempt_id = $1`, attempt); err != nil {
			t.Fatal(err)
		}
	}
	reassertClear := func(attempt string) bool {
		var at *time.Time
		if err := conn.QueryRowContext(ctx, `SELECT deterministic_reassert_at FROM foghorn.thumbnail_task_assignment WHERE attempt_id = $1`, attempt).Scan(&at); err != nil {
			t.Fatalf("read reassert_at: %v", err)
		}
		return at == nil
	}

	t.Run("the live winner re-copies then clears its clock", func(t *testing.T) {
		asset := "stream-reassert"
		tok := publishThumbnailToActive(t, ctx, conn, "tenant-a", asset, "att-ra", files, expiry)
		mock := &mockS3Client{}
		prevS3 := s3Client
		s3Client = mock
		t.Cleanup(func() { s3Client = prevS3 })
		if marked, err := projectAndMarkThumbnailFromToken(ctx, conn, mock, "att-ra", asset, "tenant-a", "cluster-a", tok, files, logger); err != nil || !marked {
			t.Fatalf("initial projection must mark: marked=%v err=%v", marked, err)
		}
		beforeReassert := len(mock.promoteCalls) // 3 version->deterministic copies so far
		dueNow("att-ra")

		progressed, err := ReassertThumbnailProjection(ctx, "att-ra")
		if err != nil || !progressed {
			t.Fatalf("reassert of the live winner must progress: progressed=%v err=%v", progressed, err)
		}
		if len(mock.promoteCalls) != beforeReassert+len(files) {
			t.Fatalf("reassert must RE-COPY the winner's %d objects to the deterministic key, got %d new promotes", len(files), len(mock.promoteCalls)-beforeReassert)
		}
		if !reassertClear("att-ra") {
			t.Fatal("reassert must clear the clock after a successful re-copy (one-shot)")
		}
	})

	t.Run("a superseded winner clears its clock without re-copying", func(t *testing.T) {
		asset := "stream-reassert-superseded"
		tokA := publishThumbnailToActive(t, ctx, conn, "tenant-a", asset, "att-rb-A", files, expiry)
		mock := &mockS3Client{}
		prevS3 := s3Client
		s3Client = mock
		t.Cleanup(func() { s3Client = prevS3 })
		if marked, err := projectAndMarkThumbnailFromToken(ctx, conn, mock, "att-rb-A", asset, "tenant-a", "cluster-a", tokA, files, logger); err != nil || !marked {
			t.Fatalf("project A: marked=%v err=%v", marked, err)
		}
		// B supersedes A. A's reassert clock is still set from its own projection.
		publishThumbnailToActive(t, ctx, conn, "tenant-a", asset, "att-rb-B", files, expiry)
		dueNow("att-rb-A")
		copiesBefore := len(mock.promoteCalls)

		progressed, err := ReassertThumbnailProjection(ctx, "att-rb-A")
		if err != nil || !progressed {
			t.Fatalf("reassert of a superseded winner must still clear (progress): progressed=%v err=%v", progressed, err)
		}
		if len(mock.promoteCalls) != copiesBefore {
			t.Fatalf("a superseded winner must NOT re-copy, got %d new promotes", len(mock.promoteCalls)-copiesBefore)
		}
		if !reassertClear("att-rb-A") {
			t.Fatal("a superseded winner must still clear its own one-shot clock")
		}
	})
}

// The leased projection recovery must isolate a poison row (source object still absent / copy persistently failing): it
// counts only real progress and BACKS OFF a non-progressing row so it is not re-selected at the head, while a healthy
// row still projects.
func TestThumbnailProjectionRecoveryPoison_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prevDB := db
	db = conn
	t.Cleanup(func() { db = prevDB })
	ctx := context.Background()
	files := []string{"poster.jpg", "sprite.jpg", "sprite.vtt"}
	expiry := time.Now().Add(time.Hour)

	// Two published-but-unprojected attempts on two assets. Age them past the DUE window, with the POISON row strictly
	// OLDEST so a limit-1 claim (due-ordered oldest-first) picks it deterministically ahead of the healthy row.
	publishThumbnailToActive(t, ctx, conn, "tenant-a", "stream-poison", "att-poison", files, expiry)
	publishThumbnailToActive(t, ctx, conn, "tenant-a", "stream-healthy", "att-healthy", files, expiry)
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.thumbnail_task_assignment SET updated_at = NOW() - INTERVAL '11 minutes' WHERE attempt_id = 'att-poison'`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.thumbnail_task_assignment SET updated_at = NOW() - INTERVAL '10 minutes' WHERE attempt_id = 'att-healthy'`); err != nil {
		t.Fatal(err)
	}
	staleBefore := time.Now().Add(-2 * time.Minute)

	// The poison attempt's source version object is ABSENT → the copy reports incomplete → progressed=false.
	poisonS3 := &mockS3Client{headObjectInfoFn: func(context.Context, string) (bool, int64, string, error) { return false, 0, "", nil }}
	prevS3 := s3Client
	s3Client = poisonS3
	t.Cleanup(func() { s3Client = prevS3 })

	// Claim ONE (the worker processes a leased batch and settles/backs off each row; here batch=1 leases only the
	// oldest = poison, so the healthy row is left unleased and due). Drive it: no progress → back it off.
	claimed, err := ClaimUnprojectedPublishedThumbnailAttempts(ctx, conn, staleBefore, 10*time.Minute, 1)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 1 || claimed[0].AttemptID != "att-poison" {
		t.Fatalf("limit-1 claim must lease exactly the oldest (poison) row; got %+v", claimed)
	}
	progressed, rErr := ReprojectPublishedThumbnailAttempt(ctx, "att-poison")
	if rErr != nil {
		t.Fatalf("reproject poison: %v", rErr)
	}
	if progressed {
		t.Fatal("a projection with an absent source must NOT report progress")
	}
	if err := BackoffThumbnailRecovery(ctx, conn, "att-poison", claimed[0].Token, 30*time.Second, "source absent"); err != nil {
		t.Fatalf("backoff: %v", err)
	}

	// Re-claim immediately: the backed-off poison row is NOT due (recovery_next_attempt_at in the future) and must be
	// excluded, so the still-due HEALTHY row is now returned — proving the poison row cannot occupy the slot forever.
	reclaimed, err := ClaimUnprojectedPublishedThumbnailAttempts(ctx, conn, staleBefore, 10*time.Minute, 1)
	if err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].AttemptID != "att-healthy" {
		t.Fatalf("with the poison row backed off, the healthy row must be claimable next; got %+v", reclaimed)
	}
}

// ClaimDueReassertThumbnailAttempts must lease EXACTLY `limit` due rows for the FULL lease duration — a regression that
// swapped the LIMIT and lease-seconds placeholders would lease every row for a few seconds, breaking the batch budget
// and letting peers reclaim mid-batch. This asserts both cardinality and lease length so that swap is caught.
func TestThumbnailReassertClaim_LeaseAndLimit_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prevDB := db
	db = conn
	t.Cleanup(func() { db = prevDB })
	ctx := context.Background()
	files := []string{"poster.jpg"}
	expiry := time.Now().Add(time.Hour)

	// Three projected winners, all with a DUE reassert clock.
	for _, id := range []string{"att-r1", "att-r2", "att-r3"} {
		publishThumbnailToActive(t, ctx, conn, "tenant-a", "stream-"+id, id, files, expiry)
	}
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.thumbnail_task_assignment SET deterministic_reassert_at = NOW() - INTERVAL '1 second' WHERE attempt_id IN ('att-r1','att-r2','att-r3')`); err != nil {
		t.Fatal(err)
	}

	const leaseTTL = 600 * time.Second
	claimed, err := ClaimDueReassertThumbnailAttempts(ctx, conn, time.Time{}, leaseTTL, 2)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	// Cardinality: exactly the limit (2), NOT all three (which a LIMIT=leaseSeconds swap would produce).
	if len(claimed) != 2 {
		t.Fatalf("limit=2 must lease exactly 2 rows, got %d (%+v) — a swapped LIMIT/lease would lease all", len(claimed), claimed)
	}
	// Lease duration: each claimed row's lease must be ~leaseTTL in the future (not ~2s from a swapped placeholder).
	for _, c := range claimed {
		var secondsOut float64
		if qErr := conn.QueryRowContext(ctx, `
			SELECT EXTRACT(EPOCH FROM (recovery_leased_until - NOW())) FROM foghorn.thumbnail_task_assignment WHERE attempt_id = $1
		`, c.AttemptID).Scan(&secondsOut); qErr != nil {
			t.Fatalf("read lease for %s: %v", c.AttemptID, qErr)
		}
		if secondsOut < 300 {
			t.Fatalf("lease for %s is %.0fs in the future, want ~%.0fs — LIMIT and lease seconds are swapped", c.AttemptID, secondsOut, leaseTTL.Seconds())
		}
	}
}

// The catalog-projection revision trigger must bump catalog_revision when ONLY thumbnail_serving_cluster_id changes on
// an artifact that is ALREADY has_thumbnails=true — otherwise the corrected/first-stamped serving cluster never
// re-projects and Commodore keeps the wrong (or missing) Chandler hostname. This is the trigger fix.
func TestThumbnailServingClusterTriggersReprojection_RealPG(t *testing.T) {
	conn := startRealPG(t)
	ctx := context.Background()
	tenant := "11111111-1111-1111-1111-111111111111"
	asset := "abcdef0123456789abcdef0123456799" // 32-char artifact_hash

	// Seed an artifact already advertising thumbnails, with NO serving cluster yet (the pre-field state).
	if _, err := conn.ExecContext(ctx, `INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, has_thumbnails) VALUES ($1, 'vod', $2, true)`, asset, tenant); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	var rev1 int64
	if err := conn.QueryRowContext(ctx, `SELECT catalog_revision FROM foghorn.artifacts WHERE artifact_hash = $1`, asset).Scan(&rev1); err != nil {
		t.Fatalf("read rev1: %v", err)
	}

	// Change ONLY the serving cluster (has_thumbnails already true, catalog_synced_rev untouched).
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.artifacts SET thumbnail_serving_cluster_id = 'media-official' WHERE artifact_hash = $1`, asset); err != nil {
		t.Fatalf("update serving cluster: %v", err)
	}
	var rev2 int64
	if err := conn.QueryRowContext(ctx, `SELECT catalog_revision FROM foghorn.artifacts WHERE artifact_hash = $1`, asset).Scan(&rev2); err != nil {
		t.Fatalf("read rev2: %v", err)
	}
	if rev2 <= rev1 {
		t.Fatalf("a serving-cluster-only change must bump catalog_revision (%d -> %d) so it re-projects", rev1, rev2)
	}
}
