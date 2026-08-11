//go:build schema_verify

package control

import (
	"context"
	"testing"
	"time"
)

// TestThumbnailPublishTokenFence_RealPG exercises the token-fenced publication lease against the REAL
// foghorn.sql baseline, for the exact interleaving the design names: A-lease → expiry → B-publish → late-A.
//
// Two completions race the SAME attempt. A acquires the publication lease (token A) and then stalls; its lease
// expires and B re-acquires it (token B, distinct). B promotes to its private candidate key (`v/{tokenB}/…`) and
// publishes — the pointer serves token B's candidate. When the stale holder A wakes and tries to advance under its
// dead token A, every post-claim settlement CAS rejects it: it can neither record a verified object, enter
// publishing, nor flip the pointer. Because A's candidate segment is its OWN token, it can only ever write its
// private object and can never overwrite the winner's. The served version stays token B throughout.
func TestThumbnailPublishTokenFence_RealPG(t *testing.T) {
	conn := startRealPG(t)
	ctx := context.Background()
	files := []string{"poster.jpg"}
	asset := "asset-tokenfence"
	attempt := "att-tokenfence"
	expiry := time.Now().Add(time.Hour)

	if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-1", "cluster-a", files, expiry); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}

	// A acquires the publication lease → token A.
	tokenA, err := AcquireThumbnailPublishLease(ctx, conn, attempt, 2*time.Minute)
	if err != nil || tokenA == "" {
		t.Fatalf("A acquire: %q err=%v", tokenA, err)
	}

	// A's lease EXPIRES (its holder stalled), so a peer may re-acquire.
	if _, err := conn.ExecContext(ctx,
		`UPDATE foghorn.thumbnail_task_assignment SET publish_leased_until = NOW() - INTERVAL '1 minute' WHERE attempt_id = $1`, attempt); err != nil {
		t.Fatalf("expire A lease: %v", err)
	}

	// B re-acquires → token B, which MUST differ from A.
	tokenB, err := AcquireThumbnailPublishLease(ctx, conn, attempt, 2*time.Minute)
	if err != nil || tokenB == "" {
		t.Fatalf("B acquire: %q err=%v", tokenB, err)
	}
	if tokenA == tokenB {
		t.Fatalf("re-acquire must mint a NEW token; got %q twice", tokenA)
	}

	// ISOLATED TOKEN FENCE: while the attempt is still eligible (non-terminal, unexpired), stale holder A cannot
	// record a verified object — ONLY the token CAS rejects it here, since status/expiry still match.
	if moved, err := MarkThumbnailObjectVerifiedToken(ctx, conn, attempt, "poster.jpg", ThumbnailVersionKey(asset, tokenA, "poster.jpg"), "etagA", 10, tokenA); err != nil {
		t.Fatalf("isolated token fence err: %v", err)
	} else if moved {
		t.Fatal("stale holder A must be rejected by the token CAS while the attempt is still eligible")
	}

	// B settles under token B: verify → enter publishing → publish CAS.
	for _, f := range files {
		if moved, err := MarkThumbnailObjectVerifiedToken(ctx, conn, attempt, f, ThumbnailVersionKey(asset, tokenB, f), "etagB", 10, tokenB); err != nil || !moved {
			t.Fatalf("B verify %s: moved=%v err=%v", f, moved, err)
		}
	}
	if entered, err := EnterThumbnailPublishingToken(ctx, conn, attempt, tokenB); err != nil || !entered {
		t.Fatalf("B enter-publishing: entered=%v err=%v", entered, err)
	}
	if activated, err := PublishThumbnailAttemptToken(ctx, conn, attempt, tokenB); err != nil || !activated {
		t.Fatalf("B publish: activated=%v err=%v", activated, err)
	}
	// The pointer now serves token B's candidate segment.
	if v, state, err := IntrospectThumbnailPointerState(ctx, conn, asset); err != nil || state != ThumbnailActive || v != tokenB {
		t.Fatalf("resolve after B = (%q,%v) err=%v; want (%q, Active)", v, state, err, tokenB)
	}

	// LATE-A: the stale holder wakes and tries to advance under its dead token. Each settlement must reject it and
	// leave the winner untouched.
	if entered, err := EnterThumbnailPublishingToken(ctx, conn, attempt, tokenA); err != nil {
		t.Fatalf("late-A enter err: %v", err)
	} else if entered {
		t.Fatal("stale holder A must NOT enter publishing under its expired token")
	}
	if activated, err := PublishThumbnailAttemptToken(ctx, conn, attempt, tokenA); err != nil {
		t.Fatalf("late-A publish err: %v", err)
	} else if activated {
		t.Fatal("stale holder A must NOT flip the pointer under its expired token")
	}
	// The served version is STILL token B — A never overwrote the winner.
	if v, state, err := IntrospectThumbnailPointerState(ctx, conn, asset); err != nil || state != ThumbnailActive || v != tokenB {
		t.Fatalf("resolve after late-A = (%q,%v) err=%v; want (%q, Active) unchanged", v, state, err, tokenB)
	}
}

// TestThumbnailRecoveryRedrivesTokenizedPublishing_RealPG proves the crash-recovery reconciler re-drives a
// PRODUCTION 'publishing' row — which carries a publication token — to 'published'. A regression guard: re-driving
// through the no-token compatibility wrapper matched zero rows (the token guard), leaving the attempt to expire and
// fail (a lost thumbnail). Recovery must carry the row's persisted token and serve the objects promoted under it.
func TestThumbnailRecoveryRedrivesTokenizedPublishing_RealPG(t *testing.T) {
	conn := startRealPG(t)
	ctx := context.Background()
	files := []string{"poster.jpg"}
	asset := "asset-recover-token"
	attempt := "att-recover-token"

	if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-1", "cluster-a", files, time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	token, err := AcquireThumbnailPublishLease(ctx, conn, attempt, 2*time.Minute)
	if err != nil || token == "" {
		t.Fatalf("acquire: %q err=%v", token, err)
	}
	for _, f := range files {
		if moved, err := MarkThumbnailObjectVerifiedToken(ctx, conn, attempt, f, ThumbnailVersionKey(asset, token, f), "etag", 10, token); err != nil || !moved {
			t.Fatalf("verify %s: moved=%v err=%v", f, moved, err)
		}
	}
	if entered, err := EnterThumbnailPublishingToken(ctx, conn, attempt, token); err != nil || !entered {
		t.Fatalf("enter-publishing: entered=%v err=%v", entered, err)
	}

	// Simulate a crash AFTER entering 'publishing' but BEFORE the pointer CAS: the row is 'publishing' with a
	// persisted token and an unexpired lease. Recovery phase 1 must re-drive it under that token.
	redriven, _, _, rErr := RecoverStuckThumbnailAttempts(ctx, conn, time.Now(), 100)
	if rErr != nil {
		t.Fatalf("recovery: %v", rErr)
	}
	if redriven == 0 {
		t.Fatal("recovery must re-drive the tokenized publishing attempt")
	}
	var status string
	if err := conn.QueryRowContext(ctx, `SELECT status FROM foghorn.thumbnail_task_assignment WHERE attempt_id = $1`, attempt).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "published" {
		t.Fatalf("recovery must publish the tokenized attempt; status = %q", status)
	}
	if v, ok, _ := ResolveActiveThumbnailVersion(ctx, conn, asset); !ok || v != token {
		t.Fatalf("recovery must serve the persisted token: v=%q ok=%v want %q", v, ok, token)
	}
}

// TestThumbnailPublishingRequiresToken_RealPG proves the DB CHECK constraint enforces the token contract: a row
// cannot enter a token-gated state ('publishing'/'published') without a nonblank publish_lease_token, so no code
// path (not even a raw UPDATE / the generic status transition) can create tokenless publication state.
func TestThumbnailPublishingRequiresToken_RealPG(t *testing.T) {
	conn := startRealPG(t)
	ctx := context.Background()
	attempt := "att-checkcon"
	if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", "asset-checkcon", "node-1", "cluster-a", []string{"poster.jpg"}, time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	// Forcing 'publishing' with NO token must be rejected by chk_foghorn_thumbnail_publishing_requires_token.
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.thumbnail_task_assignment SET status = 'publishing' WHERE attempt_id = $1`, attempt); err == nil {
		t.Fatal("a tokenless 'publishing' row must be rejected by the CHECK constraint")
	}
	// And 'published'.
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.thumbnail_task_assignment SET status = 'published' WHERE attempt_id = $1`, attempt); err == nil {
		t.Fatal("a tokenless 'published' row must be rejected by the CHECK constraint")
	}
	// Blank token is also rejected (must be nonblank).
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.thumbnail_task_assignment SET publish_lease_token = '', status = 'publishing' WHERE attempt_id = $1`, attempt); err == nil {
		t.Fatal("a blank-token 'publishing' row must be rejected by the CHECK constraint")
	}
	// With a nonblank token, 'publishing' is permitted.
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.thumbnail_task_assignment SET publish_lease_token = 'tok-abc', status = 'publishing' WHERE attempt_id = $1`, attempt); err != nil {
		t.Fatalf("a tokened 'publishing' row must be allowed: %v", err)
	}
}
