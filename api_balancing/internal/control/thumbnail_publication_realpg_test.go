//go:build schema_verify

package control

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// Exercises the thumbnail publication repo against the REAL foghorn.sql baseline: claim → verify → guarded
// transitions → the monotonic active-pointer CAS, including the stale-attempt anti-race.
func TestThumbnailPublication_RealPG(t *testing.T) {
	conn := startRealPG(t)
	ctx := context.Background()
	files := []string{"poster.jpg", "sprite.jpg", "sprite.vtt"}
	expiry := time.Now().Add(time.Hour)

	verifyAll := func(attempt, asset, tok string) {
		t.Helper()
		for _, f := range files {
			if _, err := MarkThumbnailObjectVerifiedToken(ctx, conn, attempt, f, ThumbnailVersionKey(asset, tok, f), "etag-"+f, 123, tok); err != nil {
				t.Fatalf("verify %s/%s: %v", attempt, f, err)
			}
		}
	}
	drive := func(attempt, asset string) (string, bool) {
		t.Helper()
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
		verifyAll(attempt, asset, tok)
		if entered, err := EnterThumbnailPublishingToken(ctx, conn, attempt, tok); err != nil || !entered {
			t.Fatalf("enter-publishing %s: entered=%v err=%v", attempt, entered, err)
		}
		activated, err := PublishThumbnailAttemptToken(ctx, conn, attempt, tok)
		if err != nil {
			t.Fatalf("publish %s: %v", attempt, err)
		}
		return tok, activated
	}

	t.Run("claim persists assignment + staging objects", func(t *testing.T) {
		asset := "stream-claim"
		attempt := "att-claim-1"
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-1", "cluster-a", files, expiry); err != nil || !ok {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		a, objs, found, err := LoadThumbnailAttempt(ctx, conn, attempt)
		if err != nil || !found {
			t.Fatalf("load: found=%v err=%v", found, err)
		}
		if a.Status != "assigned" || a.AssetKey != asset || a.Version != attempt || a.NodeID != "node-1" || a.DestinationCluster != "cluster-a" {
			t.Fatalf("assignment mismatch: %+v", a)
		}
		if len(objs) != 3 {
			t.Fatalf("expected 3 object rows, got %d", len(objs))
		}
		for _, o := range objs {
			if o.Verified || o.VersionKey != "" {
				t.Fatalf("object should start unverified with no version key: %+v", o)
			}
			if o.StagingKey != ThumbnailStagingKey(asset, attempt, o.FileName) {
				t.Fatalf("staging key mismatch: %+v", o)
			}
		}
	})

	t.Run("fail-closed rejects", func(t *testing.T) {
		if ok, _ := ClaimThumbnailAttempt(ctx, conn, "att-x", "", "asset", "n", "c", files, expiry); ok {
			t.Fatal("empty tenant must be rejected")
		}
		if ok, _ := ClaimThumbnailAttempt(ctx, conn, "att-x", "t", "asset", "n", "c", nil, expiry); ok {
			t.Fatal("empty file set must be rejected")
		}
		if _, _, found, _ := LoadThumbnailAttempt(ctx, conn, "ghost"); found {
			t.Fatal("unknown attempt must not be found")
		}
	})

	t.Run("happy path publishes and activates the pointer", func(t *testing.T) {
		asset := "stream-happy"
		attempt := "att-happy-1"
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-1", "cluster-a", files, expiry); err != nil || !ok {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		tok, activated := drive(attempt, asset)
		if !activated {
			t.Fatal("first publish must activate the pointer")
		}
		v, ok, err := ResolveActiveThumbnailVersion(ctx, conn, asset)
		if err != nil || !ok || v != tok {
			t.Fatalf("pointer must resolve to the token version: v=%q ok=%v err=%v", v, ok, err)
		}
	})

	t.Run("a second tenant cannot hijack another owner's asset_key pointer (ownership guard)", func(t *testing.T) {
		// asset_key is globally unique and single-owner; the pointer is keyed by asset_key alone. The tenant_id
		// on the CAS is defence-in-depth ownership attribution: an attempt carrying a DIFFERENT tenant for an
		// already-owned asset_key must NOT flip the pointer (global PK is not authorization), and settles failed.
		asset := "owned-asset-key"
		ownerAttempt, intruderAttempt := "att-owner", "att-intruder"
		one := []string{"poster.jpg"}
		if ok, err := ClaimThumbnailAttempt(ctx, conn, ownerAttempt, "tenant-owner", asset, "node-1", "cluster-a", one, expiry); err != nil || !ok {
			t.Fatalf("claim owner: %v", err)
		}
		if ok, err := ClaimThumbnailAttempt(ctx, conn, intruderAttempt, "tenant-intruder", asset, "node-2", "cluster-b", one, expiry); err != nil || !ok {
			t.Fatalf("claim intruder: %v", err)
		}
		tokOwner, aErr := AcquireThumbnailPublishLease(ctx, conn, ownerAttempt, 2*time.Minute)
		if aErr != nil || tokOwner == "" {
			t.Fatalf("acquire lease %s: %q err=%v", ownerAttempt, tokOwner, aErr)
		}
		tokIntruder, aErr := AcquireThumbnailPublishLease(ctx, conn, intruderAttempt, 2*time.Minute)
		if aErr != nil || tokIntruder == "" {
			t.Fatalf("acquire lease %s: %q err=%v", intruderAttempt, tokIntruder, aErr)
		}
		toks := map[string]string{ownerAttempt: tokOwner, intruderAttempt: tokIntruder}
		for _, at := range []string{ownerAttempt, intruderAttempt} {
			tok := toks[at]
			if _, err := MarkThumbnailObjectVerifiedToken(ctx, conn, at, "poster.jpg", ThumbnailVersionKey(asset, tok, "poster.jpg"), "etag", 10, tok); err != nil {
				t.Fatal(err)
			}
			if _, err := EnterThumbnailPublishingToken(ctx, conn, at, tok); err != nil {
				t.Fatal(err)
			}
		}
		// Owner publishes first and activates.
		if a, err := PublishThumbnailAttemptToken(ctx, conn, ownerAttempt, tokOwner); err != nil || !a {
			t.Fatalf("owner must activate: a=%v err=%v", a, err)
		}
		// Intruder (different tenant, same asset_key) must NOT activate — the ownership guard rejects the CAS.
		if a, err := PublishThumbnailAttemptToken(ctx, conn, intruderAttempt, tokIntruder); err != nil || a {
			t.Fatalf("a different tenant must not hijack the pointer: a=%v err=%v", a, err)
		}
		// The intruder is settled 'failed'; the pointer still serves the owner's version.
		var status string
		if err := conn.QueryRowContext(ctx, `SELECT status FROM foghorn.thumbnail_task_assignment WHERE attempt_id = $1`, intruderAttempt).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "failed" {
			t.Fatalf("intruder attempt must be settled failed, got %q", status)
		}
		if v, ok, _ := ResolveActiveThumbnailVersion(ctx, conn, asset); !ok || v != tokOwner {
			t.Fatalf("pointer must still serve the owner's version: v=%q ok=%v", v, ok)
		}
	})

	t.Run("has_thumbnails is deferred to the fenced projection; staging cleanup is atomic with the CAS", func(t *testing.T) {
		// DURABLE CONTRACT: the publish CAS enqueues staging cleanup atomically but does NOT flip has_thumbnails — the
		// API must never advertise a thumbnail before the deterministic served object exists. has_thumbnails flips ONLY
		// when the fenced projection (copy version->deterministic under the per-asset lock) lands. Prove both halves.
		tenant := "11111111-1111-1111-1111-111111111111"
		asset := "abcdef0123456789abcdef0123456789" // 32-char artifact_hash
		attempt := "att-converge-1"
		if _, err := conn.ExecContext(ctx, `INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id) VALUES ($1, 'vod', $2)`, asset, tenant); err != nil {
			t.Fatalf("seed artifact: %v", err)
		}
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, tenant, asset, "node-1", "cluster-a", files, expiry); err != nil || !ok {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		tok, activated := drive(attempt, asset)
		if !activated {
			t.Fatal("publish must activate the pointer")
		}
		// After the CAS alone: has_thumbnails NOT yet flipped, projection NOT yet stamped.
		var has bool
		var projectedAt *time.Time
		if err := conn.QueryRowContext(ctx, `SELECT a.has_thumbnails, t.deterministic_projected_at FROM foghorn.artifacts a, foghorn.thumbnail_task_assignment t WHERE a.artifact_hash = $1 AND t.attempt_id = $2`, asset, attempt).Scan(&has, &projectedAt); err != nil {
			t.Fatalf("read state: %v", err)
		}
		if has {
			t.Fatal("has_thumbnails must NOT be set by the publish CAS — it is deferred until the projection lands")
		}
		if projectedAt != nil {
			t.Fatal("deterministic_projected_at must be NULL until the projection lands")
		}
		// The now-superseded staging objects WERE enqueued for cleanup by the publish tx (atomic with the CAS).
		var staged int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.staging_cleanup_queue WHERE object_key = $1`, ThumbnailStagingKey(asset, attempt, "poster.jpg")).Scan(&staged); err != nil {
			t.Fatalf("count staging cleanup: %v", err)
		}
		if staged == 0 {
			t.Fatal("staging objects must be enqueued for cleanup inside the publish transaction")
		}
		// Now run the fenced projection with a working object store: it copies to the deterministic key AND flips
		// has_thumbnails + records the authoritative serving cluster — only now.
		mock := &mockS3Client{}
		marked, mErr := projectAndMarkThumbnailFromToken(ctx, conn, mock, attempt, asset, tenant, "cluster-a", tok, files, logging.NewLoggerWithService("test"))
		if mErr != nil || !marked {
			t.Fatalf("fenced projection must mark the winner: marked=%v err=%v", marked, mErr)
		}
		var servingCluster sql.NullString
		if err := conn.QueryRowContext(ctx, `SELECT a.has_thumbnails, a.thumbnail_serving_cluster_id, t.deterministic_projected_at FROM foghorn.artifacts a, foghorn.thumbnail_task_assignment t WHERE a.artifact_hash = $1 AND t.attempt_id = $2`, asset, attempt).Scan(&has, &servingCluster, &projectedAt); err != nil {
			t.Fatalf("read state after projection: %v", err)
		}
		if !has || projectedAt == nil {
			t.Fatalf("after the projection lands, has_thumbnails must be true and deterministic_projected_at set: has=%v projected=%v", has, projectedAt)
		}
		// The AUTHORITATIVE thumbnail serving cluster is the winning assignment's destination — recorded alongside
		// has_thumbnails so the catalog links the correct Chandler (not storage/origin) for a BYOC/cross-cell artifact.
		if !servingCluster.Valid || servingCluster.String != "cluster-a" {
			t.Fatalf("projection must record thumbnail_serving_cluster_id = the assignment destination, got %v", servingCluster)
		}
	})

	t.Run("publish leaves a live stream's absent artifact row untouched", func(t *testing.T) {
		// A live asset_key (stream_id) has no artifact row; the in-tx has_thumbnails flip must be a clean no-op.
		asset := "live-no-artifact-row"
		attempt := "att-live-noop"
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-1", "cluster-a", files, expiry); err != nil || !ok {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		if _, activated := drive(attempt, asset); !activated {
			t.Fatal("publish must activate the pointer even with no artifact row")
		}
	})

	t.Run("purge removes an asset's control rows + proves tenant ownership; other assets untouched", func(t *testing.T) {
		// A hard-purged artifact must not strand its thumbnail control rows. DeleteThumbnailControlRows drops the
		// active pointer + every attempt (objects cascade) for one (tenant, asset). asset_key is globally unique
		// and single-owner, so this exercises distinct assets: a WRONG-tenant purge must delete nothing (ownership
		// proof — the global PK is not authorization), and another owner's DIFFERENT asset must be untouched.
		assetA, assetB := "purge-asset-A", "purge-asset-B"
		attemptA, attemptB := "att-purge-A", "att-purge-B"
		one := []string{"poster.jpg"}
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attemptA, "tenant-A", assetA, "node-1", "cluster-a", one, expiry); err != nil || !ok {
			t.Fatalf("claim A: %v", err)
		}
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attemptB, "tenant-B", assetB, "node-2", "cluster-b", one, expiry); err != nil || !ok {
			t.Fatalf("claim B: %v", err)
		}
		tokA, aErr := AcquireThumbnailPublishLease(ctx, conn, attemptA, 2*time.Minute)
		if aErr != nil || tokA == "" {
			t.Fatalf("acquire lease %s: %q err=%v", attemptA, tokA, aErr)
		}
		tokB, aErr := AcquireThumbnailPublishLease(ctx, conn, attemptB, 2*time.Minute)
		if aErr != nil || tokB == "" {
			t.Fatalf("acquire lease %s: %q err=%v", attemptB, tokB, aErr)
		}
		for _, pair := range []struct{ at, as, tok string }{{attemptA, assetA, tokA}, {attemptB, assetB, tokB}} {
			if _, err := MarkThumbnailObjectVerifiedToken(ctx, conn, pair.at, "poster.jpg", ThumbnailVersionKey(pair.as, pair.tok, "poster.jpg"), "etag", 10, pair.tok); err != nil {
				t.Fatal(err)
			}
			if _, err := EnterThumbnailPublishingToken(ctx, conn, pair.at, pair.tok); err != nil {
				t.Fatal(err)
			}
			if a, err := PublishThumbnailAttemptToken(ctx, conn, pair.at, pair.tok); err != nil || !a {
				t.Fatalf("publish %s: a=%v err=%v", pair.at, a, err)
			}
		}
		// A WRONG-tenant purge of assetA must delete NOTHING (ownership proof).
		if err := DeleteThumbnailControlRows(ctx, conn, "tenant-WRONG", assetA); err != nil {
			t.Fatalf("wrong-tenant purge: %v", err)
		}
		var cnt int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.thumbnail_active_pointer WHERE asset_key = $1`, assetA).Scan(&cnt); err != nil || cnt != 1 {
			t.Fatalf("a wrong-tenant purge must not delete the owner's pointer: cnt=%d err=%v", cnt, err)
		}
		// The rightful owner's purge of assetA removes pointer, assignment, and objects (cascade).
		if err := DeleteThumbnailControlRows(ctx, conn, "tenant-A", assetA); err != nil {
			t.Fatalf("delete control rows: %v", err)
		}
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.thumbnail_active_pointer WHERE asset_key = $1`, assetA).Scan(&cnt); err != nil || cnt != 0 {
			t.Fatalf("assetA pointer must be gone: cnt=%d err=%v", cnt, err)
		}
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.thumbnail_task_assignment WHERE attempt_id = $1`, attemptA).Scan(&cnt); err != nil || cnt != 0 {
			t.Fatalf("assetA assignment must be gone: cnt=%d err=%v", cnt, err)
		}
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.thumbnail_task_object WHERE attempt_id = $1`, attemptA).Scan(&cnt); err != nil || cnt != 0 {
			t.Fatalf("assetA object rows must cascade-delete: cnt=%d err=%v", cnt, err)
		}
		// assetB (a different owner's different asset) is untouched.
		if v, ok, _ := ResolveActiveThumbnailVersion(ctx, conn, assetB); !ok || v != tokB {
			t.Fatalf("assetB pointer must survive assetA's purge: v=%q ok=%v", v, ok)
		}
	})

	t.Run("guarded transition rejects wrong source state", func(t *testing.T) {
		asset := "stream-guard"
		attempt := "att-guard-1"
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-1", "cluster-a", files, expiry); err != nil || !ok {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		// status is 'assigned'; a transition claiming a 'verifying' source must not fire (wrong source state).
		if moved, err := TransitionThumbnailStatus(ctx, conn, attempt, "verifying", "uploading"); err != nil || moved {
			t.Fatalf("transition from a non-current state must be a no-op: moved=%v err=%v", moved, err)
		}
		// The token-gated states are NOT reachable via the generic transition — it must error.
		if _, err := TransitionThumbnailStatus(ctx, conn, attempt, "verifying", "publishing"); err == nil {
			t.Fatal("a transition targeting 'publishing' must be rejected (token-fenced path only)")
		}
	})

	t.Run("unverified attempt cannot publish", func(t *testing.T) {
		asset := "stream-unverified"
		attempt := "att-unverified-1"
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-1", "cluster-a", files, expiry); err != nil || !ok {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		tok, aErr := AcquireThumbnailPublishLease(ctx, conn, attempt, 2*time.Minute)
		if aErr != nil || tok == "" {
			t.Fatalf("acquire lease %s: %q err=%v", attempt, tok, aErr)
		}
		// Enter publishing (token-fenced) WITHOUT verifying any object.
		if entered, err := EnterThumbnailPublishingToken(ctx, conn, attempt, tok); err != nil || !entered {
			t.Fatalf("enter-publishing: entered=%v err=%v", entered, err)
		}
		if activated, err := PublishThumbnailAttemptToken(ctx, conn, attempt, tok); err != nil || activated {
			t.Fatalf("an unverified attempt must not activate: activated=%v err=%v", activated, err)
		}
		if _, ok, _ := ResolveActiveThumbnailVersion(ctx, conn, asset); ok {
			t.Fatal("no pointer should exist for an unverified attempt")
		}
	})

	t.Run("recovery re-drives a stuck publishing attempt", func(t *testing.T) {
		asset := "stream-rec-pub"
		attempt := "att-rec-pub"
		oneFile := []string{"poster.jpg"}
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-1", "cluster-a", oneFile, expiry); err != nil || !ok {
			t.Fatalf("claim: %v", err)
		}
		tok, aErr := AcquireThumbnailPublishLease(ctx, conn, attempt, 2*time.Minute)
		if aErr != nil || tok == "" {
			t.Fatalf("acquire lease %s: %q err=%v", attempt, tok, aErr)
		}
		if _, err := MarkThumbnailObjectVerifiedToken(ctx, conn, attempt, "poster.jpg", ThumbnailVersionKey(asset, tok, "poster.jpg"), "etag", 10, tok); err != nil {
			t.Fatal(err)
		}
		// Left in 'publishing' with a verified object but never published (crash between promote and commit).
		if _, err := EnterThumbnailPublishingToken(ctx, conn, attempt, tok); err != nil {
			t.Fatal(err)
		}
		redriven, _, _, err := RecoverStuckThumbnailAttempts(ctx, conn, time.Now(), 100)
		if err != nil || redriven < 1 {
			t.Fatalf("recovery must re-drive the publishing attempt: redriven=%d err=%v", redriven, err)
		}
		// Recovery phase 1 re-drives under the PERSISTED lease token, so the served version is that token.
		if v, ok, _ := ResolveActiveThumbnailVersion(ctx, conn, asset); !ok || v != tok {
			t.Fatalf("recovery must publish+activate: v=%q ok=%v", v, ok)
		}
	})

	t.Run("recovery fails + sweeps an attempt abandoned past its lease", func(t *testing.T) {
		asset := "stream-rec-aband"
		attempt := "att-rec-aband"
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-1", "cluster-a", []string{"poster.jpg"}, time.Now().Add(-time.Hour)); err != nil || !ok {
			t.Fatalf("claim: %v", err)
		}
		_, failedN, _, err := RecoverStuckThumbnailAttempts(ctx, conn, time.Now(), 100)
		if err != nil || failedN < 1 {
			t.Fatalf("recovery must fail the abandoned attempt: failed=%d err=%v", failedN, err)
		}
		var status string
		if err := conn.QueryRowContext(ctx, `SELECT status FROM foghorn.thumbnail_task_assignment WHERE attempt_id = $1`, attempt).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "failed" {
			t.Fatalf("abandoned attempt must be marked failed, got %q", status)
		}
		var cnt int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.staging_cleanup_queue WHERE object_key = $1`, ThumbnailStagingKey(asset, attempt, "poster.jpg")).Scan(&cnt); err != nil {
			t.Fatal(err)
		}
		if cnt != 1 {
			t.Fatalf("abandoned attempt's staging object must be enqueued for cleanup, got %d", cnt)
		}
	})

	t.Run("pointer loser is settled: failed + version objects enqueued", func(t *testing.T) {
		asset := "stream-loser"
		older, newer := "att-loser-old", "att-loser-new"
		one := []string{"poster.jpg"}
		if ok, err := ClaimThumbnailAttempt(ctx, conn, older, "tenant-a", asset, "node-1", "cluster-a", one, expiry); err != nil || !ok {
			t.Fatalf("claim older: %v", err)
		}
		if ok, err := ClaimThumbnailAttempt(ctx, conn, newer, "tenant-a", asset, "node-1", "cluster-a", one, expiry); err != nil || !ok {
			t.Fatalf("claim newer: %v", err)
		}
		if _, err := conn.ExecContext(ctx, `UPDATE foghorn.thumbnail_task_assignment SET created_at = '2022-01-01T00:00:00Z' WHERE attempt_id = $1`, older); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(ctx, `UPDATE foghorn.thumbnail_task_assignment SET created_at = '2022-01-02T00:00:00Z' WHERE attempt_id = $1`, newer); err != nil {
			t.Fatal(err)
		}
		tokOlder, aErr := AcquireThumbnailPublishLease(ctx, conn, older, 2*time.Minute)
		if aErr != nil || tokOlder == "" {
			t.Fatalf("acquire lease %s: %q err=%v", older, tokOlder, aErr)
		}
		tokNewer, aErr := AcquireThumbnailPublishLease(ctx, conn, newer, 2*time.Minute)
		if aErr != nil || tokNewer == "" {
			t.Fatalf("acquire lease %s: %q err=%v", newer, tokNewer, aErr)
		}
		toks := map[string]string{older: tokOlder, newer: tokNewer}
		for _, at := range []string{older, newer} {
			tok := toks[at]
			if _, err := MarkThumbnailObjectVerifiedToken(ctx, conn, at, "poster.jpg", ThumbnailVersionKey(asset, tok, "poster.jpg"), "etag", 10, tok); err != nil {
				t.Fatal(err)
			}
			if _, err := EnterThumbnailPublishingToken(ctx, conn, at, tok); err != nil {
				t.Fatal(err)
			}
		}
		// Newer wins the pointer; older LOSES the monotonic CAS.
		if a, err := PublishThumbnailAttemptToken(ctx, conn, newer, tokNewer); err != nil || !a {
			t.Fatalf("newer must activate: a=%v err=%v", a, err)
		}
		if a, err := PublishThumbnailAttemptToken(ctx, conn, older, tokOlder); err != nil || a {
			t.Fatalf("older must lose the pointer CAS: a=%v err=%v", a, err)
		}
		// The loser must be SETTLED — terminal 'failed' with its version object enqueued (not left 'published'
		// with no superseded_at, leaking forever).
		var status string
		if err := conn.QueryRowContext(ctx, `SELECT status FROM foghorn.thumbnail_task_assignment WHERE attempt_id = $1`, older).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "failed" {
			t.Fatalf("pointer loser must be settled terminal, got %q", status)
		}
		var cnt int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.staging_cleanup_queue WHERE object_key = $1`, ThumbnailVersionKey(asset, tokOlder, "poster.jpg")).Scan(&cnt); err != nil {
			t.Fatal(err)
		}
		if cnt != 1 {
			t.Fatalf("loser's version object must be enqueued for cleanup, got %d", cnt)
		}
		// Newer stays active.
		if v, ok, _ := ResolveActiveThumbnailVersion(ctx, conn, asset); !ok || v != tokNewer {
			t.Fatalf("winner must remain active: v=%q ok=%v", v, ok)
		}
	})

	t.Run("expired attempt cannot enter publishing", func(t *testing.T) {
		asset, attempt := "stream-expired", "att-expired"
		// Claim LIVE so a lease can be minted, acquire it, THEN expire the attempt — so this exercises the
		// expiry fence at EnterThumbnailPublishing with a token that still matches the row.
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-1", "cluster-a", []string{"poster.jpg"}, time.Now().Add(time.Hour)); err != nil || !ok {
			t.Fatalf("claim: %v", err)
		}
		tok, aErr := AcquireThumbnailPublishLease(ctx, conn, attempt, 2*time.Minute)
		if aErr != nil || tok == "" {
			t.Fatalf("acquire lease %s: %q err=%v", attempt, tok, aErr)
		}
		if _, err := conn.ExecContext(ctx, `UPDATE foghorn.thumbnail_task_assignment SET expiry = NOW() - INTERVAL '1 minute' WHERE attempt_id = $1`, attempt); err != nil {
			t.Fatal(err)
		}
		if moved, err := EnterThumbnailPublishingToken(ctx, conn, attempt, tok); err != nil || moved {
			t.Fatalf("an expired attempt must not enter publishing: moved=%v err=%v", moved, err)
		}
		if _, ok, _ := ResolveActiveThumbnailVersion(ctx, conn, asset); ok {
			t.Fatal("an expired attempt must not have published")
		}
	})

	t.Run("recovery cannot sweep an attempt a completion already published", func(t *testing.T) {
		asset, attempt := "stream-race-pub", "att-race-pub"
		// A completion publishes it (a live lease); a fail-sweep applied to the now-published attempt must be a
		// no-op and never queue its live version object.
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-1", "cluster-a", []string{"poster.jpg"}, time.Now().Add(time.Hour)); err != nil || !ok {
			t.Fatalf("claim: %v", err)
		}
		tok, aErr := AcquireThumbnailPublishLease(ctx, conn, attempt, 2*time.Minute)
		if aErr != nil || tok == "" {
			t.Fatalf("acquire lease %s: %q err=%v", attempt, tok, aErr)
		}
		if _, err := MarkThumbnailObjectVerifiedToken(ctx, conn, attempt, "poster.jpg", ThumbnailVersionKey(asset, tok, "poster.jpg"), "etag", 10, tok); err != nil {
			t.Fatal(err)
		}
		if _, err := EnterThumbnailPublishingToken(ctx, conn, attempt, tok); err != nil {
			t.Fatal(err)
		}
		if _, err := PublishThumbnailAttemptToken(ctx, conn, attempt, tok); err != nil {
			t.Fatal(err)
		}
		// The guarded fail-sweep, applied to the now-published attempt, must be a no-op and NEVER queue its live
		// version object (the interleaving that previously deleted the live object).
		didFail, err := failAndSweepThumbnailAttempt(ctx, conn, attempt)
		if err != nil || didFail {
			t.Fatalf("a published attempt must not be failed/swept: didFail=%v err=%v", didFail, err)
		}
		var cnt int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.staging_cleanup_queue WHERE object_key = $1`, ThumbnailVersionKey(asset, tok, "poster.jpg")).Scan(&cnt); err != nil {
			t.Fatal(err)
		}
		if cnt != 0 {
			t.Fatalf("the live version object must NOT be enqueued for deletion, got %d", cnt)
		}
		if v, ok, _ := ResolveActiveThumbnailVersion(ctx, conn, asset); !ok || v != tok {
			t.Fatalf("pointer must remain active: v=%q ok=%v", v, ok)
		}
	})

	t.Run("recovery GCs a superseded published version", func(t *testing.T) {
		asset := "stream-superseded"
		oneFile := []string{"poster.jpg"}
		first, second := "att-sup-1", "att-sup-2"
		if ok, err := ClaimThumbnailAttempt(ctx, conn, first, "tenant-a", asset, "node-1", "cluster-a", oneFile, expiry); err != nil || !ok {
			t.Fatalf("claim first: %v", err)
		}
		if ok, err := ClaimThumbnailAttempt(ctx, conn, second, "tenant-a", asset, "node-1", "cluster-a", oneFile, expiry); err != nil || !ok {
			t.Fatalf("claim second: %v", err)
		}
		// Order the two attempts so `second` is newer and wins the pointer.
		if _, err := conn.ExecContext(ctx, `UPDATE foghorn.thumbnail_task_assignment SET created_at = '2021-01-01T00:00:00Z' WHERE attempt_id = $1`, first); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(ctx, `UPDATE foghorn.thumbnail_task_assignment SET created_at = '2021-01-02T00:00:00Z' WHERE attempt_id = $1`, second); err != nil {
			t.Fatal(err)
		}
		tokFirst, aErr := AcquireThumbnailPublishLease(ctx, conn, first, 2*time.Minute)
		if aErr != nil || tokFirst == "" {
			t.Fatalf("acquire lease %s: %q err=%v", first, tokFirst, aErr)
		}
		tokSecond, aErr := AcquireThumbnailPublishLease(ctx, conn, second, 2*time.Minute)
		if aErr != nil || tokSecond == "" {
			t.Fatalf("acquire lease %s: %q err=%v", second, tokSecond, aErr)
		}
		toks := map[string]string{first: tokFirst, second: tokSecond}
		for _, at := range []string{first, second} {
			tok := toks[at]
			if _, err := MarkThumbnailObjectVerifiedToken(ctx, conn, at, "poster.jpg", ThumbnailVersionKey(asset, tok, "poster.jpg"), "etag", 10, tok); err != nil {
				t.Fatal(err)
			}
			if _, err := EnterThumbnailPublishingToken(ctx, conn, at, tok); err != nil {
				t.Fatal(err)
			}
			if _, err := PublishThumbnailAttemptToken(ctx, conn, at, tok); err != nil {
				t.Fatal(err)
			}
		}
		// `second` is active; `first` is a superseded published version.
		if v, _, _ := ResolveActiveThumbnailVersion(ctx, conn, asset); v != tokSecond {
			t.Fatalf("expected %q active, got %q", tokSecond, v)
		}
		// A just-superseded version is held for the reader-safety horizon: GC at "now" must NOT delete it.
		if _, _, _, err := RecoverStuckThumbnailAttempts(ctx, conn, time.Now(), 100); err != nil {
			t.Fatal(err)
		}
		if _, _, found, _ := LoadThumbnailAttempt(ctx, conn, first); !found {
			t.Fatal("a just-superseded version must be held past the reader-safety horizon, not GC'd immediately")
		}
		// Past the horizon, GC removes it.
		if _, _, _, err := RecoverStuckThumbnailAttempts(ctx, conn, time.Now().Add(thumbnailReaderSafetyHorizon+time.Minute), 100); err != nil {
			t.Fatal(err)
		}
		// first's version object is enqueued, and its assignment row is deleted.
		var cnt int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.staging_cleanup_queue WHERE object_key = $1`, ThumbnailVersionKey(asset, tokFirst, "poster.jpg")).Scan(&cnt); err != nil {
			t.Fatal(err)
		}
		if cnt != 1 {
			t.Fatalf("superseded version object must be enqueued, got %d", cnt)
		}
		if _, _, found, _ := LoadThumbnailAttempt(ctx, conn, first); found {
			t.Fatal("superseded attempt row must be deleted")
		}
		// The active attempt survives and still serves.
		if v, ok, _ := ResolveActiveThumbnailVersion(ctx, conn, asset); !ok || v != tokSecond {
			t.Fatalf("active version must survive GC: v=%q ok=%v", v, ok)
		}
	})

	t.Run("claim is refused for a terminal parent artifact", func(t *testing.T) {
		tenant := "44444444-4444-4444-4444-444444444444"
		// Distinct 32-char artifact_hash per terminal state.
		hashes := map[string]string{
			"deleted": "de1e7ed0000000000000000000000000",
			"expired": "e8p1red0000000000000000000000000",
			"aborted": "ab0r7ed0000000000000000000000000",
			"failed":  "fa11ed00000000000000000000000000",
		}
		for _, st := range []string{"deleted", "expired", "aborted", "failed"} {
			asset := hashes[st]
			if _, err := conn.ExecContext(ctx, `INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status) VALUES ($1, 'vod', $2, $3)`, asset, tenant, st); err != nil {
				t.Fatalf("seed %s artifact: %v", st, err)
			}
			claimed, err := ClaimThumbnailAttempt(ctx, conn, "att-term-"+st, tenant, asset, "node-1", "cluster-a", []string{"poster.jpg"}, expiry)
			if err != nil {
				t.Fatalf("claim %s: %v", st, err)
			}
			if claimed {
				t.Fatalf("claim must be refused for a %q parent (no upload authority for terminal artifacts)", st)
			}
			var cnt int
			if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.thumbnail_task_assignment WHERE attempt_id = $1`, "att-term-"+st).Scan(&cnt); err != nil {
				t.Fatal(err)
			}
			if cnt != 0 {
				t.Fatalf("no assignment must be persisted for a %q parent, got %d", st, cnt)
			}
		}
	})

	t.Run("resolver stops serving a soft-deleted or failed parent artifact", func(t *testing.T) {
		tenant := "33333333-3333-3333-3333-333333333333"
		asset := "0011223344556677001122334455aabb" // 32-char artifact_hash
		attempt := "att-softdel"
		if _, err := conn.ExecContext(ctx, `INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status) VALUES ($1, 'vod', $2, 'ready')`, asset, tenant); err != nil {
			t.Fatalf("seed artifact: %v", err)
		}
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, tenant, asset, "node-1", "cluster-a", []string{"poster.jpg"}, expiry); err != nil || !ok {
			t.Fatalf("claim: %v", err)
		}
		tok, aErr := AcquireThumbnailPublishLease(ctx, conn, attempt, 2*time.Minute)
		if aErr != nil || tok == "" {
			t.Fatalf("acquire lease %s: %q err=%v", attempt, tok, aErr)
		}
		if _, err := MarkThumbnailObjectVerifiedToken(ctx, conn, attempt, "poster.jpg", ThumbnailVersionKey(asset, tok, "poster.jpg"), "etag", 10, tok); err != nil {
			t.Fatal(err)
		}
		if _, err := EnterThumbnailPublishingToken(ctx, conn, attempt, tok); err != nil {
			t.Fatal(err)
		}
		if a, err := PublishThumbnailAttemptToken(ctx, conn, attempt, tok); err != nil || !a {
			t.Fatalf("publish: a=%v err=%v", a, err)
		}
		// While 'ready', it resolves.
		if _, ok, _ := ResolveActiveThumbnailVersion(ctx, conn, asset); !ok {
			t.Fatal("a ready artifact's thumbnail must resolve")
		}
		// Soft-delete the parent → the resolver stops serving IMMEDIATELY (not lingering until the ~30-day purge).
		if _, err := conn.ExecContext(ctx, `UPDATE foghorn.artifacts SET status='deleted' WHERE artifact_hash=$1`, asset); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := ResolveActiveThumbnailVersion(ctx, conn, asset); ok {
			t.Fatal("a soft-deleted parent's thumbnail must stop resolving")
		}
	})

	t.Run("a tombstoned parent artifact cannot publish", func(t *testing.T) {
		tenant := "22222222-2222-2222-2222-222222222222"
		asset := "fedcba9876543210fedcba9876543210" // 32-char artifact_hash
		attempt := "att-tombstoned"
		// Claim while the parent is still 'ready' (claim-time fence would otherwise refuse it), then verify +
		// enter publishing, THEN tombstone the parent — so this exercises the PUBLISH-time fence specifically.
		if _, err := conn.ExecContext(ctx, `INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status) VALUES ($1, 'vod', $2, 'ready')`, asset, tenant); err != nil {
			t.Fatalf("seed artifact: %v", err)
		}
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, tenant, asset, "node-1", "cluster-a", []string{"poster.jpg"}, expiry); err != nil || !ok {
			t.Fatalf("claim: %v", err)
		}
		tok, aErr := AcquireThumbnailPublishLease(ctx, conn, attempt, 2*time.Minute)
		if aErr != nil || tok == "" {
			t.Fatalf("acquire lease %s: %q err=%v", attempt, tok, aErr)
		}
		if _, err := MarkThumbnailObjectVerifiedToken(ctx, conn, attempt, "poster.jpg", ThumbnailVersionKey(asset, tok, "poster.jpg"), "etag", 10, tok); err != nil {
			t.Fatal(err)
		}
		if _, err := EnterThumbnailPublishingToken(ctx, conn, attempt, tok); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(ctx, `UPDATE foghorn.artifacts SET status='deleted' WHERE artifact_hash=$1`, asset); err != nil {
			t.Fatal(err)
		}
		// Publish must NOT activate for a tombstoned parent; the attempt is settled failed and its version swept.
		if a, err := PublishThumbnailAttemptToken(ctx, conn, attempt, tok); err != nil || a {
			t.Fatalf("a tombstoned parent must not publish: a=%v err=%v", a, err)
		}
		var status string
		if err := conn.QueryRowContext(ctx, `SELECT status FROM foghorn.thumbnail_task_assignment WHERE attempt_id = $1`, attempt).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "failed" {
			t.Fatalf("tombstoned-parent attempt must be settled failed, got %q", status)
		}
		if _, ok, _ := ResolveActiveThumbnailVersion(ctx, conn, asset); ok {
			t.Fatal("no pointer must exist for a tombstoned-parent asset")
		}
		var cnt int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.staging_cleanup_queue WHERE object_key = $1`, ThumbnailVersionKey(asset, tok, "poster.jpg")).Scan(&cnt); err != nil {
			t.Fatal(err)
		}
		if cnt != 1 {
			t.Fatalf("tombstoned attempt's version object must be enqueued, got %d", cnt)
		}
	})

	t.Run("publish fences an attempt that expired while publishing", func(t *testing.T) {
		asset, attempt := "stream-pub-expired", "att-pub-expired"
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-1", "cluster-a", []string{"poster.jpg"}, time.Now().Add(time.Hour)); err != nil || !ok {
			t.Fatalf("claim: %v", err)
		}
		tok, aErr := AcquireThumbnailPublishLease(ctx, conn, attempt, 2*time.Minute)
		if aErr != nil || tok == "" {
			t.Fatalf("acquire lease %s: %q err=%v", attempt, tok, aErr)
		}
		if _, err := MarkThumbnailObjectVerifiedToken(ctx, conn, attempt, "poster.jpg", ThumbnailVersionKey(asset, tok, "poster.jpg"), "etag", 10, tok); err != nil {
			t.Fatal(err)
		}
		if _, err := EnterThumbnailPublishingToken(ctx, conn, attempt, tok); err != nil {
			t.Fatal(err)
		}
		// The lease expires AFTER entering publishing (a crash/pause between). Publish must refuse it even under a
		// still-matching token — entering 'publishing' before expiry does not license publishing after it.
		if _, err := conn.ExecContext(ctx, `UPDATE foghorn.thumbnail_task_assignment SET expiry = NOW() - INTERVAL '1 minute' WHERE attempt_id = $1`, attempt); err != nil {
			t.Fatal(err)
		}
		if a, err := PublishThumbnailAttemptToken(ctx, conn, attempt, tok); err != nil || a {
			t.Fatalf("an expired publishing attempt must not activate: a=%v err=%v", a, err)
		}
		if _, ok, _ := ResolveActiveThumbnailVersion(ctx, conn, asset); ok {
			t.Fatal("an expired publishing attempt must not have published")
		}
	})

	t.Run("recovery reconstructs an unrecorded promoted version key", func(t *testing.T) {
		// A promote landed in S3 but version_key was NEVER recorded on the row (a crash between promote and
		// MarkVerified). Recovery's fail-sweep must still reclaim it via deterministic reconstruction.
		asset, attempt := "stream-unrecorded", "att-unrecorded"
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-1", "cluster-a", []string{"poster.jpg"}, time.Now().Add(-time.Hour)); err != nil || !ok {
			t.Fatalf("claim: %v", err)
		}
		// The object row keeps version_key='' (unrecorded). Recovery fails + sweeps the abandoned attempt.
		if _, failedN, _, err := RecoverStuckThumbnailAttempts(ctx, conn, time.Now(), 100); err != nil || failedN < 1 {
			t.Fatalf("recovery must fail the abandoned attempt: failed=%d err=%v", failedN, err)
		}
		var cnt int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.staging_cleanup_queue WHERE object_key = $1`, ThumbnailVersionKey(asset, attempt, "poster.jpg")).Scan(&cnt); err != nil {
			t.Fatal(err)
		}
		if cnt != 1 {
			t.Fatalf("reconstructed version key must be enqueued even when version_key was unrecorded, got %d", cnt)
		}
	})

	t.Run("concurrent publish vs recovery-sweep never strands the pointer", func(t *testing.T) {
		// One goroutine publishes while another concurrently fail-sweeps the SAME attempt. FOR UPDATE serializes
		// them: the outcome is EITHER activated-and-published OR failed-and-not-serving, NEVER a pointer serving
		// an attempt that is 'failed' with its version queued for deletion.
		asset, attempt := "stream-concurrent", "att-concurrent"
		if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-1", "cluster-a", []string{"poster.jpg"}, time.Now().Add(time.Hour)); err != nil || !ok {
			t.Fatalf("claim: %v", err)
		}
		tok, aErr := AcquireThumbnailPublishLease(ctx, conn, attempt, 2*time.Minute)
		if aErr != nil || tok == "" {
			t.Fatalf("acquire lease %s: %q err=%v", attempt, tok, aErr)
		}
		if _, err := MarkThumbnailObjectVerifiedToken(ctx, conn, attempt, "poster.jpg", ThumbnailVersionKey(asset, tok, "poster.jpg"), "etag", 10, tok); err != nil {
			t.Fatal(err)
		}
		if _, err := EnterThumbnailPublishingToken(ctx, conn, attempt, tok); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		var pubErr, sweepErr error
		go func() { defer wg.Done(); _, pubErr = PublishThumbnailAttemptToken(ctx, conn, attempt, tok) }()
		go func() { defer wg.Done(); _, sweepErr = failAndSweepThumbnailAttempt(ctx, conn, attempt) }()
		wg.Wait()
		if pubErr != nil || sweepErr != nil {
			t.Fatalf("errors: pub=%v sweep=%v", pubErr, sweepErr)
		}
		var status string
		if err := conn.QueryRowContext(ctx, `SELECT status FROM foghorn.thumbnail_task_assignment WHERE attempt_id = $1`, attempt).Scan(&status); err != nil {
			t.Fatal(err)
		}
		v, ptrOK, _ := ResolveActiveThumbnailVersion(ctx, conn, asset)
		if ptrOK && v == tok {
			if status != "published" {
				t.Fatalf("pointer serves the attempt but status=%q (must be published)", status)
			}
			var queued int
			if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.staging_cleanup_queue WHERE object_key = $1`, ThumbnailVersionKey(asset, tok, "poster.jpg")).Scan(&queued); err != nil {
				t.Fatal(err)
			}
			if queued != 0 {
				t.Fatalf("the live pointer's version object must NOT be queued for deletion, got %d", queued)
			}
		}
	})

	t.Run("stale attempt cannot regress the pointer (monotonic anti-race)", func(t *testing.T) {
		asset := "stream-race"
		older := "att-older"
		newer := "att-newer"
		if ok, err := ClaimThumbnailAttempt(ctx, conn, older, "tenant-a", asset, "node-1", "cluster-a", files, expiry); err != nil || !ok {
			t.Fatalf("claim older: %v", err)
		}
		if ok, err := ClaimThumbnailAttempt(ctx, conn, newer, "tenant-a", asset, "node-1", "cluster-a", files, expiry); err != nil || !ok {
			t.Fatalf("claim newer: %v", err)
		}
		// Deterministic ordering: force distinct created_at (older strictly before newer).
		if _, err := conn.ExecContext(ctx, `UPDATE foghorn.thumbnail_task_assignment SET created_at = '2020-01-01T00:00:00Z' WHERE attempt_id = $1`, older); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(ctx, `UPDATE foghorn.thumbnail_task_assignment SET created_at = '2020-01-02T00:00:00Z' WHERE attempt_id = $1`, newer); err != nil {
			t.Fatal(err)
		}

		// Publish the NEWER attempt first → it activates.
		tokNewer, activated := drive(newer, asset)
		if !activated {
			t.Fatal("newer attempt must activate")
		}
		// Now publish the OLDER (stale) attempt → its objects are durable but it must NOT regress the pointer.
		if _, activated := drive(older, asset); activated {
			t.Fatal("stale older attempt must NOT re-activate the pointer")
		}
		v, ok, err := ResolveActiveThumbnailVersion(ctx, conn, asset)
		if err != nil || !ok || v != tokNewer {
			t.Fatalf("pointer must still serve the newer version: v=%q ok=%v err=%v", v, ok, err)
		}
	})

	t.Run("equal created_at does not ping-pong; claim_seq gives a total order", func(t *testing.T) {
		asset := "stream-equal-ts"
		a1, a2 := "att-eq-1", "att-eq-2"
		one := []string{"poster.jpg"}
		if ok, err := ClaimThumbnailAttempt(ctx, conn, a1, "tenant-a", asset, "node-1", "cluster-a", one, expiry); err != nil || !ok {
			t.Fatalf("claim a1: %v", err)
		}
		if ok, err := ClaimThumbnailAttempt(ctx, conn, a2, "tenant-a", asset, "node-1", "cluster-a", one, expiry); err != nil || !ok {
			t.Fatalf("claim a2: %v", err)
		}
		// Force IDENTICAL created_at so ordering must rely on claim_seq (a2 claimed later ⇒ strictly greater), which
		// keeps the replace order TOTAL even when created_at ties (a created_at-only compare would not be total).
		if _, err := conn.ExecContext(ctx, `UPDATE foghorn.thumbnail_task_assignment SET created_at = '2023-01-01T00:00:00Z' WHERE attempt_id IN ($1, $2)`, a1, a2); err != nil {
			t.Fatal(err)
		}
		tokA1, aErr := AcquireThumbnailPublishLease(ctx, conn, a1, 2*time.Minute)
		if aErr != nil || tokA1 == "" {
			t.Fatalf("acquire lease %s: %q err=%v", a1, tokA1, aErr)
		}
		tokA2, aErr := AcquireThumbnailPublishLease(ctx, conn, a2, 2*time.Minute)
		if aErr != nil || tokA2 == "" {
			t.Fatalf("acquire lease %s: %q err=%v", a2, tokA2, aErr)
		}
		toks := map[string]string{a1: tokA1, a2: tokA2}
		for _, at := range []string{a1, a2} {
			tok := toks[at]
			if _, err := MarkThumbnailObjectVerifiedToken(ctx, conn, at, "poster.jpg", ThumbnailVersionKey(asset, tok, "poster.jpg"), "etag", 10, tok); err != nil {
				t.Fatal(err)
			}
			if _, err := EnterThumbnailPublishingToken(ctx, conn, at, tok); err != nil {
				t.Fatal(err)
			}
		}
		// a2 (higher claim_seq) wins; a1 (equal created_at, lower claim_seq) must NOT activate or regress it.
		if a, err := PublishThumbnailAttemptToken(ctx, conn, a2, tokA2); err != nil || !a {
			t.Fatalf("a2 must activate: a=%v err=%v", a, err)
		}
		if a, err := PublishThumbnailAttemptToken(ctx, conn, a1, tokA1); err != nil || a {
			t.Fatalf("a1 (equal created_at, lower claim_seq) must NOT activate: a=%v err=%v", a, err)
		}
		if v, ok, _ := ResolveActiveThumbnailVersion(ctx, conn, asset); !ok || v != tokA2 {
			t.Fatalf("pointer must serve a2: v=%q ok=%v", v, ok)
		}
	})
}
