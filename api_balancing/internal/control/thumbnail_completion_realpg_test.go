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

		v, ok, err := ResolveActiveThumbnailVersion(ctx, conn, asset)
		if err != nil || !ok || v != attempt {
			t.Fatalf("pointer must be active at the attempt version: v=%q ok=%v err=%v", v, ok, err)
		}
		if len(mock.promoteCalls) != 3 {
			t.Fatalf("expected 3 staging->version promotions, got %d (%v)", len(mock.promoteCalls), mock.promoteCalls)
		}
		_, objs, _, _ := LoadThumbnailAttempt(ctx, conn, attempt)
		for _, o := range objs {
			if !o.Verified || o.VersionKey != ThumbnailVersionKey(asset, attempt, o.FileName) {
				t.Fatalf("object must be verified + version-keyed: %+v", o)
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
		// A replayed confirmation for the already-published attempt must be an idempotent no-op.
		completeThumbnailPublication(ctx, attempt, asset, "node-1", logger)

		if len(mock.promoteCalls) != firstPromotes {
			t.Fatalf("replay must not re-promote: %d -> %d", firstPromotes, len(mock.promoteCalls))
		}
		if v, ok, _ := ResolveActiveThumbnailVersion(ctx, conn, asset); !ok || v != attempt {
			t.Fatalf("replay must leave the attempt published+active: v=%q ok=%v", v, ok)
		}
	})

	t.Run("a completion that loses the publishing race does not queue the live version (F2)", func(t *testing.T) {
		asset, attempt := "stream-race-dup", "att-race-dup"
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-1", "cluster-a", files, expiry); err != nil || !ok {
			t.Fatalf("claim: %v", err)
		}
		// Verify all objects and move to 'publishing' — as if a CONCURRENT completion of the same attempt is
		// mid-publish holding the deterministic version keys.
		for _, f := range files {
			if _, err := MarkThumbnailObjectVerified(ctx, conn, attempt, f, ThumbnailVersionKey(asset, attempt, f), "etag", 10); err != nil {
				t.Fatal(err)
			}
		}
		if ok, err := EnterThumbnailPublishing(ctx, conn, attempt); err != nil || !ok {
			t.Fatalf("enter publishing: %v", err)
		}
		mock := &mockS3Client{}
		prevS3 := s3Client
		s3Client = mock
		t.Cleanup(func() { s3Client = prevS3 })

		// This completion promotes, then LOSES the enter-publishing race (already 'publishing'). It must NOT
		// enqueue the version keys — a concurrent publisher owns them; enqueuing would delete a newly-live object.
		completeThumbnailPublication(ctx, attempt, asset, "node-1", logger)

		for _, f := range files {
			var cnt int
			if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.staging_cleanup_queue WHERE object_key = $1`, ThumbnailVersionKey(asset, attempt, f)).Scan(&cnt); err != nil {
				t.Fatal(err)
			}
			if cnt != 0 {
				t.Fatalf("a completion that lost the publishing race must NOT queue version key %s for deletion, got %d", f, cnt)
			}
		}
		// The promoted version is still publishable and becomes live.
		if a, err := PublishThumbnailAttempt(ctx, conn, attempt); err != nil || !a {
			t.Fatalf("the promoted version must still publish: a=%v err=%v", a, err)
		}
		if v, ok, _ := ResolveActiveThumbnailVersion(ctx, conn, asset); !ok || v != attempt {
			t.Fatalf("pointer must serve the attempt: v=%q ok=%v", v, ok)
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

	t.Run("recovery completes a stuck attempt whose ThumbnailUploaded was lost (F4a)", func(t *testing.T) {
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
		if len(mock.promoteCalls) != 3 {
			t.Fatalf("recovery must promote all objects, got %d", len(mock.promoteCalls))
		}
		if v, ok, _ := ResolveActiveThumbnailVersion(ctx, conn, asset); !ok || v != attempt {
			t.Fatalf("recovery must publish + activate the stuck attempt: v=%q ok=%v", v, ok)
		}
	})
}
