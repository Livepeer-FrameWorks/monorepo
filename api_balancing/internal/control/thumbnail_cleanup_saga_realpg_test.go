//go:build schema_verify

package control

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// Exercises the stream-deletion saga's tombstone fences (invariant I4) against the REAL foghorn.sql baseline: a
// durable cleanup obligation for a live stream (which has NO artifacts row) must (1) mark the asset tombstoned,
// (2) make the pointer-state introspection report GONE even with an active pointer, (3) fence a new claim, and (4)
// settle a racing publish 'failed' with no pointer flip. It also proves the record is idempotent and snapshots
// the single backend fingerprint before any control row is deleted.
func TestStreamCleanupSaga_TombstoneFences_RealPG(t *testing.T) {
	conn := startRealPG(t)
	ctx := context.Background()
	files := []string{"poster.jpg", "sprite.jpg", "sprite.vtt"}
	expiry := time.Now().Add(time.Hour)

	driveToPublishing := func(attempt, asset string) string {
		t.Helper()
		tok, aErr := AcquireThumbnailPublishLease(ctx, conn, attempt, 2*time.Minute)
		if aErr != nil || tok == "" {
			t.Fatalf("acquire lease %s: %q err=%v", attempt, tok, aErr)
		}
		for _, from := range []struct{ a, b string }{{"assigned", "uploading"}, {"uploading", "verifying"}} {
			if ok, err := TransitionThumbnailStatus(ctx, conn, attempt, from.a, from.b); err != nil || !ok {
				t.Fatalf("%s→%s %s: ok=%v err=%v", from.a, from.b, attempt, ok, err)
			}
		}
		for _, f := range files {
			if _, err := MarkThumbnailObjectVerifiedToken(ctx, conn, attempt, f, ThumbnailVersionKey(asset, tok, f), "etag", 1, tok); err != nil {
				t.Fatalf("verify %s/%s: %v", attempt, f, err)
			}
		}
		if entered, err := EnterThumbnailPublishingToken(ctx, conn, attempt, tok); err != nil || !entered {
			t.Fatalf("enter-publishing %s: entered=%v err=%v", attempt, entered, err)
		}
		return tok
	}

	t.Run("record snapshots backend, tombstones, and is idempotent", func(t *testing.T) {
		asset := "stream-record"
		if ok, err := ClaimThumbnailAttempt(ctx, conn, "att-rec-a", "tenant-a", asset, "node-1", "cluster-a", files, expiry); err != nil || !ok {
			t.Fatalf("claim a: ok=%v err=%v", ok, err)
		}
		if ok, err := ClaimThumbnailAttempt(ctx, conn, "att-rec-b", "tenant-a", asset, "node-1", "cluster-b", files, expiry); err != nil || !ok {
			t.Fatalf("claim b: ok=%v err=%v", ok, err)
		}
		if err := RecordStreamCleanupObligation(ctx, conn, "tenant-a", asset); err != nil {
			t.Fatalf("record: %v", err)
		}
		if ts, err := AssetTombstoned(ctx, conn, asset); err != nil || !ts {
			t.Fatalf("AssetTombstoned = %v, err=%v; want true", ts, err)
		}
		var status string
		if err := conn.QueryRowContext(ctx,
			`SELECT status FROM foghorn.stream_cleanup_obligation WHERE asset_key = $1`, asset).
			Scan(&status); err != nil {
			t.Fatalf("read obligation: %v", err)
		}
		if status != "pending" {
			t.Fatalf("status = %q, want pending", status)
		}
		// One Foghorn DB owns one immutable backend, so the obligation snapshots a SINGLE backend_id fingerprint on the
		// parent (no per-cluster child rows). Both attempts recorded THIS cell's fingerprint at claim, so the obligation
		// snapshots it (never NULL — an unattributable obligation fails closed rather than settling on a guessed store).
		var backendID sql.NullString
		if err := conn.QueryRowContext(ctx,
			`SELECT backend_id FROM foghorn.stream_cleanup_obligation WHERE asset_key = $1`, asset).Scan(&backendID); err != nil {
			t.Fatalf("read obligation backend_id: %v", err)
		}
		if !backendID.Valid || backendID.String != testCellBackendID {
			t.Fatalf("backend_id snapshot = %v/%q, want the cell fingerprint %q", backendID.Valid, backendID.String, testCellBackendID)
		}
		// Idempotent: a second record is a no-op that keeps ONE row.
		if err := RecordStreamCleanupObligation(ctx, conn, "tenant-a", asset); err != nil {
			t.Fatalf("record again: %v", err)
		}
		var n int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.stream_cleanup_obligation WHERE asset_key = $1`, asset).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		if n != 1 {
			t.Fatalf("obligation rows = %d, want 1 (idempotent)", n)
		}
	})

	t.Run("resolve returns GONE for a tombstoned live stream with an active pointer", func(t *testing.T) {
		asset := "stream-resolve"
		attempt := "att-res-1"
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-1", "cluster-a", files, expiry); err != nil || !ok {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		tok := driveToPublishing(attempt, asset)
		if activated, err := PublishThumbnailAttemptToken(ctx, conn, attempt, tok); err != nil || !activated {
			t.Fatalf("publish: activated=%v err=%v", activated, err)
		}
		if _, state, err := IntrospectThumbnailPointerState(ctx, conn, asset); err != nil || state != ThumbnailActive {
			t.Fatalf("pre-delete resolve state = %v err=%v; want Active", state, err)
		}
		if err := RecordStreamCleanupObligation(ctx, conn, "tenant-a", asset); err != nil {
			t.Fatalf("record: %v", err)
		}
		if _, state, err := IntrospectThumbnailPointerState(ctx, conn, asset); err != nil || state != ThumbnailGone {
			t.Fatalf("post-delete resolve state = %v err=%v; want Gone", state, err)
		}
	})

	t.Run("claim is fenced by the tombstone", func(t *testing.T) {
		asset := "stream-claimfence"
		if err := RecordStreamCleanupObligation(ctx, conn, "tenant-a", asset); err != nil {
			t.Fatalf("record: %v", err)
		}
		if ok, err := ClaimThumbnailAttempt(ctx, conn, "att-cf-1", "tenant-a", asset, "node-1", "cluster-a", files, expiry); err != nil {
			t.Fatalf("claim err: %v", err)
		} else if ok {
			t.Fatal("claim on a tombstoned asset must be fenced (claimed=false)")
		}
	})

	t.Run("racing publish settles failed with no pointer flip", func(t *testing.T) {
		asset := "stream-pubfence"
		attempt := "att-pf-1"
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-1", "cluster-a", files, expiry); err != nil || !ok {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		tok := driveToPublishing(attempt, asset)
		// Delete races in AFTER the attempt reached 'publishing' but BEFORE its pointer CAS.
		if err := RecordStreamCleanupObligation(ctx, conn, "tenant-a", asset); err != nil {
			t.Fatalf("record: %v", err)
		}
		if activated, err := PublishThumbnailAttemptToken(ctx, conn, attempt, tok); err != nil {
			t.Fatalf("publish err: %v", err)
		} else if activated {
			t.Fatal("publish racing a tombstone must not activate the pointer")
		}
		var st string
		if err := conn.QueryRowContext(ctx, `SELECT status FROM foghorn.thumbnail_task_assignment WHERE attempt_id = $1`, attempt).Scan(&st); err != nil {
			t.Fatalf("read attempt: %v", err)
		}
		if st != "failed" {
			t.Fatalf("attempt status = %q, want failed (settled)", st)
		}
		var pointerN int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.thumbnail_active_pointer WHERE asset_key = $1`, asset).Scan(&pointerN); err != nil {
			t.Fatalf("count pointer: %v", err)
		}
		if pointerN != 0 {
			t.Fatalf("no pointer must exist for a tombstoned asset, got %d", pointerN)
		}
	})
}

// Closes the promote-vs-delete leak (a completion promotes a version object after the drainer's prefix-list
// snapshot / after the assignment is deleted). Part (a): DeleteThumbnailControlRows enqueues the deterministic
// staging + version keys for every attempt BEFORE deleting the rows, so a to-be-deleted attempt's promoted object
// is still swept. Part (b): EnqueueThumbnailVersionOrphansIfGone lets a completion clean its own promoted object
// when it discovers the asset is GONE after promoting — but only when GONE, never for a live/retryable asset.
func TestThumbnailPromoteVsDeleteLeak_RealPG(t *testing.T) {
	conn := startRealPG(t)
	ctx := context.Background()
	files := []string{"poster.jpg", "sprite.jpg", "sprite.vtt"}
	queued := func(key string) bool {
		t.Helper()
		var n int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.staging_cleanup_queue WHERE object_key = $1`, key).Scan(&n); err != nil {
			t.Fatalf("queue lookup: %v", err)
		}
		return n > 0
	}

	t.Run("DeleteThumbnailControlRows enqueues deterministic keys before deleting", func(t *testing.T) {
		asset, attempt := "stream-del", "att-del"
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-1", "cluster-a", files, time.Now().Add(time.Hour)); err != nil || !ok {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		if err := DeleteThumbnailControlRows(ctx, conn, "tenant-a", asset); err != nil {
			t.Fatalf("delete control rows: %v", err)
		}
		for _, f := range files {
			if !queued(ThumbnailVersionKey(asset, attempt, f)) {
				t.Fatalf("version key for %s must be enqueued before the assignment is deleted", f)
			}
			if !queued(ThumbnailStagingKey(asset, attempt, f)) {
				t.Fatalf("staging key for %s must be enqueued", f)
			}
		}
	})

	t.Run("orphan cleanup enqueues promoted objects when the asset is gone OR the attempt is dead", func(t *testing.T) {
		asset, attempt := "stream-orphan", "att-orphan"
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-1", "cluster-a", files, time.Now().Add(time.Hour)); err != nil || !ok {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		// Live asset + live attempt: must NOT enqueue (a legitimate re-drive may publish the same deterministic key).
		if enq, err := EnqueueThumbnailVersionOrphansIfDead(ctx, conn, attempt, asset, attempt, files); err != nil || enq {
			t.Fatalf("live attempt must not enqueue orphans; enq=%v err=%v", enq, err)
		}
		// The attempt is failed (e.g. recovery swept it mid-promote) while the ASSET is still live → must enqueue.
		if _, err := conn.ExecContext(ctx, `UPDATE foghorn.thumbnail_task_assignment SET status = 'failed' WHERE attempt_id = $1`, attempt); err != nil {
			t.Fatalf("fail attempt: %v", err)
		}
		if enq, err := EnqueueThumbnailVersionOrphansIfDead(ctx, conn, attempt, asset, attempt, files); err != nil || !enq {
			t.Fatalf("failed attempt must enqueue orphans even with a live asset; enq=%v err=%v", enq, err)
		}
		for _, f := range files {
			if !queued(ThumbnailVersionKey(asset, attempt, f)) {
				t.Fatalf("version key for %s must be enqueued once the attempt is dead", f)
			}
		}
	})
}

// The publication lease single-flights the HEAD/promote per attempt and is honored by the recovery fail-sweep, so
// a concurrent completion cannot double-promote and recovery cannot expire an attempt out from under a live
// promotion.
func TestThumbnailPublishLease_RealPG(t *testing.T) {
	conn := startRealPG(t)
	ctx := context.Background()
	files := []string{"poster.jpg"}

	t.Run("lease is single-flight", func(t *testing.T) {
		if ok, err := ClaimThumbnailAttempt(ctx, conn, "att-lease", "tenant-a", "asset-lease", "node-1", "cluster-a", files, time.Now().Add(time.Hour)); err != nil || !ok {
			t.Fatalf("claim: %v", err)
		}
		first, err := AcquireThumbnailPublishLease(ctx, conn, "att-lease", 2*time.Minute)
		if err != nil || first == "" {
			t.Fatalf("first acquire must mint a token; got %q err=%v", first, err)
		}
		second, err := AcquireThumbnailPublishLease(ctx, conn, "att-lease", 2*time.Minute)
		if err != nil {
			t.Fatalf("second acquire err: %v", err)
		}
		if second != "" {
			t.Fatalf("a second completion must NOT acquire a held publication lease; got token %q", second)
		}
	})

	t.Run("recovery fail-sweep honors a live publication lease", func(t *testing.T) {
		// Attempt is claimed non-expired (so the lease can be acquired), then EXPIRES while the lease is live.
		if ok, err := ClaimThumbnailAttempt(ctx, conn, "att-slow", "tenant-a", "asset-slow", "node-1", "cluster-a", files, time.Now().Add(time.Hour)); err != nil || !ok {
			t.Fatalf("claim: %v", err)
		}
		if leased, err := AcquireThumbnailPublishLease(ctx, conn, "att-slow", 2*time.Minute); err != nil || leased == "" {
			t.Fatalf("acquire lease: %q err=%v", leased, err)
		}
		// Simulate the attempt's own expiry passing while the publication is still in flight.
		if _, err := conn.ExecContext(ctx, `UPDATE foghorn.thumbnail_task_assignment SET expiry = NOW() - INTERVAL '1 minute' WHERE attempt_id = $1`, "att-slow"); err != nil {
			t.Fatalf("expire attempt: %v", err)
		}
		if _, _, _, err := RecoverStuckThumbnailAttempts(ctx, conn, time.Now(), 100); err != nil {
			t.Fatalf("recovery pass: %v", err)
		}
		var status string
		if err := conn.QueryRowContext(ctx, `SELECT status FROM foghorn.thumbnail_task_assignment WHERE attempt_id = $1`, "att-slow").Scan(&status); err != nil {
			t.Fatalf("read status: %v", err)
		}
		if status == "failed" {
			t.Fatal("recovery must NOT fail an expired attempt whose publication lease is still live")
		}
		// Once the publication lease is released/expired, recovery may fail it.
		if _, err := conn.ExecContext(ctx, `UPDATE foghorn.thumbnail_task_assignment SET publish_leased_until = NULL WHERE attempt_id = $1`, "att-slow"); err != nil {
			t.Fatalf("clear lease: %v", err)
		}
		if _, _, _, err := RecoverStuckThumbnailAttempts(ctx, conn, time.Now(), 100); err != nil {
			t.Fatalf("recovery pass 2: %v", err)
		}
		if err := conn.QueryRowContext(ctx, `SELECT status FROM foghorn.thumbnail_task_assignment WHERE attempt_id = $1`, "att-slow").Scan(&status); err != nil {
			t.Fatalf("read status 2: %v", err)
		}
		if status != "failed" {
			t.Fatalf("recovery must fail an expired attempt once its lease is released, got %q", status)
		}
	})
}
