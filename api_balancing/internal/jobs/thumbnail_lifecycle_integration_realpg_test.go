//go:build schema_verify

package jobs

import (
	"context"
	"sync"
	"testing"
	"time"

	"frameworks/api_balancing/internal/artifacts"
	"frameworks/api_balancing/internal/control"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// Cross-component behavioral test (real Postgres + mock S3). It drives ONE live-stream thumbnail through
// its whole lifecycle across the REAL publication state machine, the REAL deletion saga (control), the REAL
// publication pointer/tombstone introspection, and the REAL StreamCleanupJob drainer (jobs), asserting the I3/I4
// interleavings the per-stage unit tests exercise only in isolation:
//
//   - publish → the publication pointer state is ACTIVE;
//   - stream deletion records the durable tombstone → the pointer state flips to GONE (I4 fence for a rowless stream);
//   - a completion racing the deletion is settled failed with no pointer flip (I4 publish-vs-delete race);
//   - the drainer sweeps the bytes + drops the control rows and the tombstone PERSISTS → still GONE (I3 durable
//     convergence, incl. crash-after-commit: the obligation is recorded, then a SEPARATE drainer run converges);
//   - two concurrent drainers do not double-sweep (lease fencing).
//
// SCOPE: this exercises the Foghorn-side control + jobs code paths directly. It does NOT drive the gRPC
// DeleteStreamThumbnails handler, the Commodore delivery outbox, the Chandler serving surface, or an external object
// store; those are covered by the per-component tests (grpc handler, stream_cleanup_outbox_claim, Chandler
// assets_test). This is a cross-component gate for the deletion state machine, not an end-to-end wired proof.
func TestThumbnailLifecycleIntegration_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	ctx := context.Background()
	files := []string{"poster.jpg", "sprite.jpg", "sprite.vtt"}
	stream := "stream-integration"

	// Drive an attempt to 'publishing' through the PRODUCTION token-fenced path: acquire a real publication lease
	// (minting the token that fences every settlement), verify each object under that token to its per-token
	// candidate key, then enter publishing under the token. Returns the token for the caller's publish CAS.
	driveToPublishing := func(attempt string) string {
		t.Helper()
		token, err := control.AcquireThumbnailPublishLease(ctx, conn, attempt, 2*time.Minute)
		if err != nil || token == "" {
			t.Fatalf("acquire publication lease %s: %q err=%v", attempt, token, err)
		}
		for _, f := range files {
			if moved, err := control.MarkThumbnailObjectVerifiedToken(ctx, conn, attempt, f, control.ThumbnailVersionKey(stream, token, f), "etag", 1, token); err != nil || !moved {
				t.Fatalf("verify %s/%s: moved=%v err=%v", attempt, f, moved, err)
			}
		}
		if entered, err := control.EnterThumbnailPublishingToken(ctx, conn, attempt, token); err != nil || !entered {
			t.Fatalf("enter-publishing %s: entered=%v err=%v", attempt, entered, err)
		}
		return token
	}
	resolveState := func() control.ThumbnailResolveState {
		t.Helper()
		_, state, err := control.IntrospectThumbnailPointerState(ctx, conn, stream)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		return state
	}

	// 1) Publish the first version → the publication pointer resolves ACTIVE.
	if ok, err := control.ClaimThumbnailAttempt(ctx, conn, "att-1", "tenant-a", stream, "node-1", "local", files, time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatalf("claim att-1: ok=%v err=%v", ok, err)
	}
	token1 := driveToPublishing("att-1")
	if activated, err := control.PublishThumbnailAttemptToken(ctx, conn, "att-1", token1); err != nil || !activated {
		t.Fatalf("publish att-1: activated=%v err=%v", activated, err)
	}
	if s := resolveState(); s != control.ThumbnailActive {
		t.Fatalf("after publish, resolve = %v, want Active", s)
	}

	// 2) A newer completion is in-flight ('publishing') when the stream is deleted.
	if ok, err := control.ClaimThumbnailAttempt(ctx, conn, "att-2", "tenant-a", stream, "node-1", "local", files, time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatalf("claim att-2: ok=%v err=%v", ok, err)
	}
	token2 := driveToPublishing("att-2")

	// 3) Delete the stream = record the durable tombstone (what the DeleteStreamThumbnails RPC does). Resolve GONE.
	if err := control.RecordStreamCleanupObligation(ctx, conn, "tenant-a", stream); err != nil {
		t.Fatalf("record obligation: %v", err)
	}
	// Backdate past the max-copy window so the drain below marks 'cleaned' in one pass (the delayed second sweep that
	// guards against a straggler resurrection is exercised by TestStreamCleanupDrainer_DelayedResweep_RealPG).
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.stream_cleanup_obligation SET enqueued_at = NOW() - INTERVAL '30 minutes' WHERE asset_key = $1`, stream); err != nil {
		t.Fatal(err)
	}
	if s := resolveState(); s != control.ThumbnailGone {
		t.Fatalf("after deletion, resolve = %v, want Gone", s)
	}

	// 4) The racing in-flight publish must be fenced: settled failed, no pointer flip.
	if activated, err := control.PublishThumbnailAttemptToken(ctx, conn, "att-2", token2); err != nil {
		t.Fatalf("publish att-2: %v", err)
	} else if activated {
		t.Fatal("a publish racing the deletion tombstone must NOT activate the pointer")
	}
	var att2Status string
	if err := conn.QueryRowContext(ctx, `SELECT status FROM foghorn.thumbnail_task_assignment WHERE attempt_id = 'att-2'`).Scan(&att2Status); err != nil {
		t.Fatalf("read att-2: %v", err)
	}
	if att2Status != "failed" {
		t.Fatalf("racing att-2 status = %q, want failed", att2Status)
	}

	// 5) The REAL drainer converges the deletion (crash-after-commit: the obligation was recorded above; this is a
	// FRESH drainer run, as if the process had restarted). It sweeps the bytes + drops control rows; tombstone stays.
	fake := &fakeThumbS3{}
	job := NewStreamCleanupJob(StreamCleanupConfig{
		DB:       conn,
		Cleaner:  &artifacts.Cleaner{LocalCluster: "local", S3: fake, LocalBackendID: testCellBackendID},
		Logger:   logging.NewLogger(),
		Interval: time.Hour,
	})
	job.drain()

	var swept bool
	fake.mu.Lock()
	for _, p := range fake.prefixes {
		if p == "thumbnails/"+stream+"/" {
			swept = true
		}
	}
	fake.mu.Unlock()
	if !swept {
		t.Fatalf("drainer must sweep the stream's thumbnail prefix, got %v", fake.prefixes)
	}
	var controlRows int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.thumbnail_task_assignment WHERE asset_key = $1`, stream).Scan(&controlRows); err != nil {
		t.Fatalf("count control rows: %v", err)
	}
	if controlRows != 0 {
		t.Fatalf("drainer must drop all control rows, got %d", controlRows)
	}
	var oblStatus string
	if err := conn.QueryRowContext(ctx, `SELECT status FROM foghorn.stream_cleanup_obligation WHERE asset_key = $1`, stream).Scan(&oblStatus); err != nil {
		t.Fatalf("read obligation: %v", err)
	}
	if oblStatus != "cleaned" {
		t.Fatalf("obligation status = %q, want cleaned", oblStatus)
	}
	// The tombstone persists (cleaned) so the asset stays GONE forever — a late read never serves stale bytes.
	if s := resolveState(); s != control.ThumbnailGone {
		t.Fatalf("after cleanup, resolve = %v, want Gone (tombstone persists)", s)
	}

	// 6) Two concurrent drainers on a fresh deletion do not double-sweep (lease/token fencing).
	stream2 := "stream-concurrent"
	if ok, err := control.ClaimThumbnailAttempt(ctx, conn, "att-c", "tenant-a", stream2, "node-1", "local", files, time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatalf("claim att-c: ok=%v err=%v", ok, err)
	}
	if err := control.RecordStreamCleanupObligation(ctx, conn, "tenant-a", stream2); err != nil {
		t.Fatalf("record obligation 2: %v", err)
	}
	fake2 := &fakeThumbS3{}
	cleaner2 := &artifacts.Cleaner{LocalCluster: "local", S3: fake2, LocalBackendID: testCellBackendID}
	var wg sync.WaitGroup
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j := NewStreamCleanupJob(StreamCleanupConfig{DB: conn, Cleaner: cleaner2, Logger: logging.NewLogger(), Interval: time.Hour})
			j.drain()
		}()
	}
	wg.Wait()
	fake2.mu.Lock()
	n := 0
	for _, p := range fake2.prefixes {
		if p == "thumbnails/"+stream2+"/" {
			n++
		}
	}
	fake2.mu.Unlock()
	if n != 1 {
		t.Fatalf("two concurrent drainers swept the prefix %d times, want exactly 1 (lease fencing)", n)
	}
}
