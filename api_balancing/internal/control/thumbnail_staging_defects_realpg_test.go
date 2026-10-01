//go:build schema_verify

package control

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/streamident"
)

// A DVR chapter finalization runs as processing+{chapter playback hash} on the chapter's finalize node and produces
// thumbnails there, without a processing_jobs row. The thumbnail producer check accepts exactly that node, for that
// tenant, while the chapter is finalizing.
func TestThumbnailProducerIncludesChapterFinalizeNode_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	const (
		tenant      = "11111111-1111-1111-1111-111111111111"
		otherTenant = "22222222-2222-2222-2222-222222222222"
		dvrHash     = "20261001023446f66c37951e680a3c"
		chapterHash = "1c099a63412fb7f66aea2ad49a5d9754"
	)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status) VALUES ($1, 'dvr', $2::uuid, 'recording')`, dvrHash, tenant)
	exec(`INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status) VALUES ($1, 'vod', $2::uuid, 'processing')`, chapterHash, tenant)
	exec(`INSERT INTO foghorn.dvr_chapters (chapter_id, artifact_hash, mode, start_ms, end_ms, state, playback_artifact_hash, finalize_attempts, finalize_node_id, finalize_started_at)
		VALUES ('80eefe25dcd5f6a74fa1f31f92943119', $1, 'fixed_interval', 0, 60000, 'finalizing', $2, 1, 'edge-eu', NOW())`, dvrHash, chapterHash)

	produces := func(node, tenantID string) bool {
		t.Helper()
		ok, err := nodeProducesThumbnailResource(ctx, node, streamident.KindArtifactProcessing, false, "", chapterHash, tenantID)
		if err != nil {
			t.Fatalf("producer lookup: %v", err)
		}
		return ok
	}
	if !produces("edge-eu", tenant) {
		t.Fatal("the chapter's finalize node must be accepted as the thumbnail producer while the chapter is finalizing")
	}
	if produces("edge-us", tenant) {
		t.Fatal("a node that is not the chapter's finalize node must be denied")
	}
	if produces("edge-eu", otherTenant) {
		t.Fatal("the finalize node must be denied under another tenant")
	}
	exec(`UPDATE foghorn.dvr_chapters SET state = 'finalized', finalize_node_id = NULL WHERE playback_artifact_hash = $1`, chapterHash)
	if produces("edge-eu", tenant) {
		t.Fatal("a finalized chapter no longer authorizes its former finalize node")
	}
}

// A claim refused because the asset is being deleted reports why, with no error; only a failed claim transaction
// returns an error.
func TestThumbnailClaimOutcome_RealPG(t *testing.T) {
	conn := startRealPG(t)
	ctx := context.Background()
	files := []string{"poster.jpg", "sprite.jpg", "sprite.vtt"}
	expiry := time.Now().Add(time.Hour)
	const tenant = "33333333-3333-3333-3333-333333333333"

	claim := func(attempt, asset string) ThumbnailClaimOutcome {
		t.Helper()
		outcome, err := ClaimThumbnailAttemptOutcome(ctx, conn, attempt, tenant, asset, "edge-eu", "cluster-eu", files, expiry)
		if err != nil {
			t.Fatalf("claim %s: %v", attempt, err)
		}
		return outcome
	}

	if got := claim("att-live", "e3895147-4104-487f-b83e-8ee17e898b32"); got != ThumbnailClaimed {
		t.Fatalf("a live stream claim must be recorded, got %s", got)
	}

	deletedStream := "be2790b0-62c2-4350-9177-b10310d15215"
	if err := RecordStreamCleanupObligation(ctx, conn, tenant, deletedStream); err != nil {
		t.Fatalf("tombstone stream: %v", err)
	}
	if got := claim("att-deleted-stream", deletedStream); got != ThumbnailClaimAssetTombstoned {
		t.Fatalf("a deleted live stream must be refused as tombstoned, got %s", got)
	}

	deletedArtifact := "2026093012443441641776786eda7e"
	if _, err := conn.ExecContext(ctx, `INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status) VALUES ($1, 'clip', $2::uuid, 'deleted')`, deletedArtifact, tenant); err != nil {
		t.Fatal(err)
	}
	if got := claim("att-deleted-artifact", deletedArtifact); got != ThumbnailClaimParentTerminal {
		t.Fatalf("a deleted artifact must be refused as parent-terminal, got %s", got)
	}

	if outcome, err := ClaimThumbnailAttemptOutcome(ctx, conn, "att-no-cluster", tenant, "f1f1f1f1-0000-0000-0000-000000000000", "edge-eu", "", files, expiry); err != nil || outcome != ThumbnailClaimIncompleteIdentity {
		t.Fatalf("a claim without a destination cluster must be refused as incomplete: outcome=%s err=%v", outcome, err)
	}

	var assignments int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.thumbnail_task_assignment WHERE attempt_id LIKE 'att-%'`).Scan(&assignments); err != nil {
		t.Fatal(err)
	}
	if assignments != 1 {
		t.Fatalf("only the recorded claim may leave an assignment, got %d", assignments)
	}
}

// A staging HEAD the object store never answers is abandoned at its attempt timeout and retried, so the completion
// still publishes within its deadline instead of failing with "context deadline exceeded".
func TestThumbnailCompletionRetriesAStalledStagingHead_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prevDB := db
	db = conn
	t.Cleanup(func() { db = prevDB })
	ctx := context.Background()
	asset, attempt := "stream-stalled-head", "att-stalled-head"
	if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-1", "cluster-a", []string{"poster.jpg"}, time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	var mu sync.Mutex
	heads := 0
	mock := &mockS3Client{headObjectInfoFn: func(ctx context.Context, _ string) (bool, int64, string, error) {
		mu.Lock()
		heads++
		first := heads == 1
		mu.Unlock()
		if first {
			<-ctx.Done()
			return false, 0, "", ctx.Err()
		}
		return true, verifiedMockObjectSize, "etag-mock", nil
	}}
	prevS3 := s3Client
	s3Client = mock
	t.Cleanup(func() { s3Client = prevS3 })

	// Larger than one attempt timeout, far smaller than the 2-minute completion deadline.
	completeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	completeThumbnailPublication(completeCtx, attempt, asset, "node-1", logging.NewLoggerWithService("test"))

	if v, ok, err := ResolveActiveThumbnailVersion(ctx, conn, asset); err != nil || !ok || v == "" {
		t.Fatalf("a completion whose first staging HEAD stalled must still publish: v=%q ok=%v err=%v", v, ok, err)
	}
}

// A completion HEADs each staged object once; the projection copies the promoted candidates under the ETag their copy
// returned instead of HEADing them again.
func TestThumbnailCompletionHeadsOnlyStagedObjects_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prevDB := db
	db = conn
	t.Cleanup(func() { db = prevDB })
	ctx := context.Background()
	files := []string{"poster.jpg", "sprite.jpg", "sprite.vtt"}
	asset, attempt := "stream-head-count", "att-head-count"
	if ok, err := ClaimThumbnailAttempt(ctx, conn, attempt, "tenant-a", asset, "node-1", "cluster-a", files, time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	var mu sync.Mutex
	var projectionIfMatch []string
	mock := &mockS3Client{promoteETag: "etag-candidate", promoteObjectFn: func(_ context.Context, _, dst, ifMatch string) error {
		if !strings.Contains(dst, "/v/") {
			mu.Lock()
			projectionIfMatch = append(projectionIfMatch, ifMatch)
			mu.Unlock()
		}
		return nil
	}}
	prevS3 := s3Client
	s3Client = mock
	t.Cleanup(func() { s3Client = prevS3 })

	completeThumbnailPublication(ctx, attempt, asset, "node-1", logging.NewLoggerWithService("test"))

	if _, ok, err := ResolveActiveThumbnailVersion(ctx, conn, asset); err != nil || !ok {
		t.Fatalf("completion must publish: ok=%v err=%v", ok, err)
	}
	for _, k := range mock.headCalls {
		if !strings.Contains(k, "/.staging/") {
			t.Fatalf("only staged objects may be HEADed, got %v", mock.headCalls)
		}
	}
	if len(mock.headCalls) != len(files) {
		t.Fatalf("want one HEAD per staged object, got %v", mock.headCalls)
	}
	if len(projectionIfMatch) != len(files) {
		t.Fatalf("want one projection copy per file, got %v", projectionIfMatch)
	}
	for _, e := range projectionIfMatch {
		if e != "etag-candidate" {
			t.Fatalf("projection must copy under the candidate's copy-result ETag, got %v", projectionIfMatch)
		}
	}
	_, objs, _, _ := LoadThumbnailAttempt(ctx, conn, attempt)
	for _, o := range objs {
		if o.ETag != "etag-candidate" {
			t.Fatalf("the verified object must record the candidate's ETag: %+v", o)
		}
	}
}
