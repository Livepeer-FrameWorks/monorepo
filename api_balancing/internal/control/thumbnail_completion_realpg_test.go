//go:build schema_verify

package control

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// End-to-end completion (Workstream H) against the REAL foghorn.sql schema + the mockS3Client seam (no external
// object store): the verify -> promote -> publish -> active-pointer composition, plus the adversarial drops that
// the sqlmock unit tests only exercise at the bind layer.
func TestThumbnailCompletion_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prevDB := db
	db = conn
	t.Cleanup(func() { db = prevDB })
	ctx := context.Background()
	logger := logging.NewLoggerWithService("test")
	files := []string{"poster.jpg", "sprite.jpg", "sprite.vtt"}
	expiry := time.Now().Add(time.Hour)

	t.Run("happy path verifies, promotes, and activates the pointer", func(t *testing.T) {
		asset, attempt := "stream-complete", "att-complete"
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-1", "cluster-a", files, expiry); err != nil || !ok {
			t.Fatalf("claim: %v", err)
		}
		mock := &mockS3Client{}
		prevS3 := s3Client
		s3Client = mock
		t.Cleanup(func() { s3Client = prevS3 })

		completeThumbnailPublication(ctx, attempt, asset, "node-1", logger)

		// The served version is the completion's minted lease TOKEN (a per-token candidate segment), not the
		// attempt id — so it is non-empty and distinct from the attempt.
		v, ok, err := ResolveActiveThumbnailVersion(ctx, conn, asset)
		if err != nil || !ok || v == "" || v == attempt {
			t.Fatalf("pointer must serve the minted token version (not the attempt id): v=%q ok=%v err=%v", v, ok, err)
		}
		// Six promotes: 3 staging->version candidate promotions, then 3 fenced version->deterministic projections to
		// the served key (thumbnails/{asset}/{file}) the completion now performs under the per-asset lock.
		staging, deterministic := 0, 0
		for _, o := range files {
			for _, c := range mock.promoteCalls {
				if c == ThumbnailVersionKey(asset, v, o)+"->"+ThumbnailDeterministicKey(asset, o) {
					deterministic++
				}
			}
		}
		staging = len(mock.promoteCalls) - deterministic
		if staging != 3 || deterministic != 3 {
			t.Fatalf("expected 3 staging->version + 3 version->deterministic promotes, got staging=%d deterministic=%d (%v)", staging, deterministic, mock.promoteCalls)
		}
		// The fenced projection stamped deterministic_projected_at (the durable boundary the API gates has_thumbnails on).
		var projectedAt *time.Time
		if err := conn.QueryRowContext(ctx, `SELECT deterministic_projected_at FROM foghorn.thumbnail_task_assignment WHERE attempt_id = $1`, attempt).Scan(&projectedAt); err != nil {
			t.Fatalf("read deterministic_projected_at: %v", err)
		}
		if projectedAt == nil {
			t.Fatal("a successful completion must stamp deterministic_projected_at (projection landed under the fence)")
		}
		_, objs, _, _ := LoadThumbnailAttempt(ctx, conn, attempt)
		for _, o := range objs {
			if !o.Verified || o.VersionKey != ThumbnailVersionKey(asset, v, o.FileName) {
				t.Fatalf("object must be verified + keyed to the served token %q: %+v", v, o)
			}
		}
		// The winner de-registered its now-live candidate objects (enqueued before promotion) from the cleanup queue.
		for _, o := range objs {
			var cnt int
			if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.staging_cleanup_queue WHERE object_key = $1`, ThumbnailVersionKey(asset, v, o.FileName)).Scan(&cnt); err != nil {
				t.Fatal(err)
			}
			if cnt != 0 {
				t.Fatalf("winner must de-register its live candidate %s from the cleanup queue, still queued", o.FileName)
			}
		}
	})

	t.Run("foreign node completion is dropped before any S3 work", func(t *testing.T) {
		asset, attempt := "stream-foreign", "att-foreign"
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-assigned", "cluster-a", files, expiry); err != nil || !ok {
			t.Fatalf("claim: %v", err)
		}
		mock := &mockS3Client{}
		prevS3 := s3Client
		s3Client = mock
		t.Cleanup(func() { s3Client = prevS3 })

		// A different node than the assignment's node_id completes → must be dropped.
		completeThumbnailPublication(ctx, attempt, asset, "node-attacker", logger)

		if _, ok, _ := ResolveActiveThumbnailVersion(ctx, conn, asset); ok {
			t.Fatal("a foreign node must not publish")
		}
		if len(mock.promoteCalls) != 0 {
			t.Fatalf("a foreign node must not reach promote, got %v", mock.promoteCalls)
		}
	})

	t.Run("mismatched echoed key is dropped", func(t *testing.T) {
		asset, attempt := "stream-key", "att-key"
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-1", "cluster-a", files, expiry); err != nil || !ok {
			t.Fatalf("claim: %v", err)
		}
		mock := &mockS3Client{}
		prevS3 := s3Client
		s3Client = mock
		t.Cleanup(func() { s3Client = prevS3 })

		// The node echoes an attempt id whose assignment asset != the key it names → drop (can't retarget).
		completeThumbnailPublication(ctx, attempt, "some-other-asset", "node-1", logger)

		if _, ok, _ := ResolveActiveThumbnailVersion(ctx, conn, asset); ok {
			t.Fatal("a mismatched echoed key must not publish")
		}
		if len(mock.promoteCalls) != 0 {
			t.Fatal("a mismatched echoed key must not reach promote")
		}
	})

	t.Run("duplicate completion (replay) is idempotent", func(t *testing.T) {
		asset, attempt := "stream-dup", "att-dup"
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-1", "cluster-a", files, expiry); err != nil || !ok {
			t.Fatalf("claim: %v", err)
		}
		mock := &mockS3Client{}
		prevS3 := s3Client
		s3Client = mock
		t.Cleanup(func() { s3Client = prevS3 })

		completeThumbnailPublication(ctx, attempt, asset, "node-1", logger)
		firstPromotes := len(mock.promoteCalls)
		firstV, ok, _ := ResolveActiveThumbnailVersion(ctx, conn, asset)
		if !ok || firstV == "" {
			t.Fatalf("first completion must publish a token version: v=%q ok=%v", firstV, ok)
		}
		// A replayed confirmation for the already-published attempt must be an idempotent no-op: the attempt is
		// terminal so no new lease is minted, nothing re-promotes, and the served token is unchanged.
		completeThumbnailPublication(ctx, attempt, asset, "node-1", logger)

		if len(mock.promoteCalls) != firstPromotes {
			t.Fatalf("replay must not re-promote: %d -> %d", firstPromotes, len(mock.promoteCalls))
		}
		if v, ok, _ := ResolveActiveThumbnailVersion(ctx, conn, asset); !ok || v != firstV {
			t.Fatalf("replay must leave the same published token active: v=%q first=%q ok=%v", v, firstV, ok)
		}
	})

	t.Run("a losing holder leaves only its OWN candidate queued; the winner's live version is never queued", func(t *testing.T) {
		asset, attempt := "stream-race-dup", "att-race-dup"
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-1", "cluster-a", files, expiry); err != nil || !ok {
			t.Fatalf("claim: %v", err)
		}
		// Holder A acquires the lease, records its private candidate (enqueued BEFORE promotion), then stalls.
		tokenA, err := AcquireThumbnailPublishLease(ctx, conn, attempt, 2*time.Minute)
		if err != nil || tokenA == "" {
			t.Fatalf("A acquire: %q err=%v", tokenA, err)
		}
		aKeys := make([]string, 0, len(files))
		for _, f := range files {
			aKeys = append(aKeys, ThumbnailVersionKey(asset, tokenA, f))
		}
		if err := EnqueueThumbnailCleanup(ctx, conn, aKeys); err != nil {
			t.Fatalf("A pre-promotion candidate enqueue: %v", err)
		}
		for _, f := range files {
			if moved, err := MarkThumbnailObjectVerifiedToken(ctx, conn, attempt, f, ThumbnailVersionKey(asset, tokenA, f), "etagA", 10, tokenA); err != nil || !moved {
				t.Fatalf("A verify %s: moved=%v err=%v", f, moved, err)
			}
		}

		// A's lease expires; holder B re-acquires and drives to publish (wins).
		if _, err := conn.ExecContext(ctx, `UPDATE foghorn.thumbnail_task_assignment SET publish_leased_until = NOW() - INTERVAL '1 minute' WHERE attempt_id = $1`, attempt); err != nil {
			t.Fatalf("expire A lease: %v", err)
		}
		tokenB, err := AcquireThumbnailPublishLease(ctx, conn, attempt, 2*time.Minute)
		if err != nil || tokenB == "" || tokenB == tokenA {
			t.Fatalf("B acquire: %q (A=%q) err=%v", tokenB, tokenA, err)
		}
		bKeys := make([]string, 0, len(files))
		for _, f := range files {
			bKeys = append(bKeys, ThumbnailVersionKey(asset, tokenB, f))
		}
		if err := EnqueueThumbnailCleanup(ctx, conn, bKeys); err != nil {
			t.Fatalf("B pre-promotion candidate enqueue: %v", err)
		}
		for _, f := range files {
			if moved, err := MarkThumbnailObjectVerifiedToken(ctx, conn, attempt, f, ThumbnailVersionKey(asset, tokenB, f), "etagB", 10, tokenB); err != nil || !moved {
				t.Fatalf("B verify %s: moved=%v err=%v", f, moved, err)
			}
		}
		if entered, err := EnterThumbnailPublishingToken(ctx, conn, attempt, tokenB); err != nil || !entered {
			t.Fatalf("B enter-publishing: entered=%v err=%v", entered, err)
		}
		if a, err := PublishThumbnailAttemptToken(ctx, conn, attempt, tokenB); err != nil || !a {
			t.Fatalf("B publish must win: a=%v err=%v", a, err)
		}

		// The winner's live candidate (token B) is de-registered inside the publish CAS; the stale holder's
		// private candidate (token A) stays queued and is reclaimed — the live object is never deleted.
		queued := func(key string) int {
			var cnt int
			if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.staging_cleanup_queue WHERE object_key = $1`, key).Scan(&cnt); err != nil {
				t.Fatal(err)
			}
			return cnt
		}
		for _, f := range files {
			if queued(ThumbnailVersionKey(asset, tokenB, f)) != 0 {
				t.Fatalf("winner's live candidate %s (token B) must NOT remain queued for deletion", f)
			}
			if queued(ThumbnailVersionKey(asset, tokenA, f)) != 1 {
				t.Fatalf("stale holder's orphan candidate %s (token A) must remain queued for cleanup", f)
			}
		}
		if v, ok, _ := ResolveActiveThumbnailVersion(ctx, conn, asset); !ok || v != tokenB {
			t.Fatalf("pointer must serve token B: v=%q ok=%v", v, tokenB)
		}
	})

	t.Run("unknown attempt id is dropped", func(t *testing.T) {
		mock := &mockS3Client{}
		prevS3 := s3Client
		s3Client = mock
		t.Cleanup(func() { s3Client = prevS3 })
		completeThumbnailPublication(ctx, "att-ghost", "stream-ghost", "node-1", logger)
		if len(mock.promoteCalls) != 0 {
			t.Fatal("an unknown attempt must not reach promote")
		}
	})

	t.Run("recovery completes a stuck attempt whose ThumbnailUploaded was lost", func(t *testing.T) {
		// The node uploaded (staging exists via the mock) but the completion never ran (dropped send / crash
		// before 'publishing'), leaving the attempt in 'assigned'. Recovery re-drives the completion, so a
		// one-shot VOD thumbnail is not orphaned.
		asset, attempt := "stream-recover-complete", "att-recover-complete"
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-1", "cluster-a", files, time.Now().Add(time.Hour)); err != nil || !ok {
			t.Fatalf("claim: %v", err)
		}
		mock := &mockS3Client{}
		prevS3 := s3Client
		s3Client = mock
		t.Cleanup(func() { s3Client = prevS3 })

		// The attempt is stuck in 'assigned' (no completion). Recovery's stuck-incomplete query finds it once
		// idle past the staleness window.
		if _, err := conn.ExecContext(ctx, `UPDATE foghorn.thumbnail_task_assignment SET updated_at = NOW() - INTERVAL '10 minutes' WHERE attempt_id = $1`, attempt); err != nil {
			t.Fatal(err)
		}
		ids, err := StuckIncompleteThumbnailAttemptIDs(ctx, conn, time.Now(), time.Now().Add(-2*time.Minute), 100)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, id := range ids {
			if id == attempt {
				found = true
			}
		}
		if !found {
			t.Fatalf("stuck-incomplete query must surface the abandoned attempt; got %v", ids)
		}
		// Re-drive it: verify -> promote -> publish -> active. progressed must be true (it reached 'published').
		if progressed, cErr := CompleteThumbnailAttemptForRecovery(ctx, attempt, logger); cErr != nil || !progressed {
			t.Fatalf("recovery completion: progressed=%v err=%v", progressed, cErr)
		}
		// 3 staging->version promotes + 3 version->deterministic projections (recovery drives the full completion).
		if len(mock.promoteCalls) != 6 {
			t.Fatalf("recovery must promote all objects and project to the deterministic key, got %d (%v)", len(mock.promoteCalls), mock.promoteCalls)
		}
		if v, ok, _ := ResolveActiveThumbnailVersion(ctx, conn, asset); !ok || v == "" || v == attempt {
			t.Fatalf("recovery must publish + activate the stuck attempt at a minted token: v=%q ok=%v", v, ok)
		}
	})
}
