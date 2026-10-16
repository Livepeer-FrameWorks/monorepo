//go:build schema_verify

package jobs

import (
	"context"
	"sort"
	"testing"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

const backfillTenant = "dddddddd-dddd-dddd-dddd-dddddddddddd"

// The staging state behind this test: DVR chapter VODs and a clip published days ago, ready and synced, with no
// thumbnail and no thumbnail attempt, because the one processing run that publishes an asset lost its thumbnail.
// The backfill queues a thumbnail job for each, within the cluster-wide slot limit, under the Thumbs policy of the
// lifecycle the asset was published under; records a policy without Thumbs as a skipped attempt; leaves alone every
// asset that has or is producing a thumbnail, is not a local published asset, is too young, or has used its
// attempts; fails a queued thumbnail job whose asset was deleted; and the queued jobs are dispatchable although
// their assets are ready.
func TestThumbnailBackfillQueuesPublishedAssetsWithoutThumbnails_RealPG(t *testing.T) { //nolint:funlen // One fixture covers every selected and excluded class.
	conn := startRealPGForCleanup(t)
	ctx := context.Background()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, query)
		}
	}
	asset := func(hash, kind, age string, extra string) {
		t.Helper()
		exec(`INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status, sync_status, storage_location,
				format, s3_url, durable_backend_local, origin_cluster_id, created_at, updated_at)
			VALUES ($1::text, $2, $3::uuid, 'ready', 'synced', 's3', 'mkv', 's3://bucket/' || $1::text || '.mkv', true, 'cell-a',
				NOW() - $4::interval, NOW())`, hash, kind, backfillTenant, age)
		if extra != "" {
			exec(`UPDATE foghorn.artifacts SET `+extra+` WHERE artifact_hash = $1`, hash)
		}
	}
	thumbJob := func(hash, status, age string) {
		t.Helper()
		exec(`INSERT INTO foghorn.processing_jobs (tenant_id, artifact_hash, job_type, status, created_at, updated_at)
			VALUES ($1::uuid, $2, 'thumbnail', $3, NOW() - $4::interval, NOW() - $4::interval)`, backfillTenant, hash, status, age)
	}

	// Selected, oldest first.
	asset("bfvodnothumbs0000000000000000001", "vod", "5 days", "")
	asset("bfchapter00000000000000000000001", "vod", "4 days", "")
	exec(`INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status) VALUES ('bfdvrparent0000000000000000001', 'dvr', $1::uuid, 'completed')`, backfillTenant)
	exec(`INSERT INTO foghorn.dvr_chapters (chapter_id, artifact_hash, mode, start_ms, end_ms, state, playback_artifact_hash)
		VALUES ('bfchapterid00000000000000000001', 'bfdvrparent0000000000000000001', 'fixed_interval', 0, 600000, 'reclaimed', 'bfchapter00000000000000000000001')`)
	asset("bfclip00000000000000000000000001", "clip", "3 days", "")
	// Excluded.
	asset("bfhasthumbs000000000000000000001", "vod", "5 days", "has_thumbnails = true")
	asset("bfliveattempt0000000000000000001", "vod", "5 days", "")
	exec(`INSERT INTO foghorn.thumbnail_task_assignment (attempt_id, tenant_id, asset_key, node_id, destination_cluster, status, claim_seq, expiry)
		VALUES ('attempt-live', $1, 'bfliveattempt0000000000000000001', 'edge-1', 'cell-a', 'uploading', 4503599627370497, NOW() + INTERVAL '10 minutes')`, backfillTenant)
	asset("bfactivejob000000000000000000001", "vod", "5 days", "")
	exec(`INSERT INTO foghorn.processing_jobs (tenant_id, artifact_hash, job_type, status) VALUES ($1::uuid, 'bfactivejob000000000000000000001', 'process', 'queued')`, backfillTenant)
	asset("bffederated000000000000000000001", "vod", "5 days", "federated_pointer = true")
	asset("bfremotebytes0000000000000000001", "vod", "5 days", "durable_backend_local = false")
	asset("bfyoung0000000000000000000000001", "vod", "10 minutes", "")
	asset("bfexhausted00000000000000000001", "vod", "5 days", "")
	for range thumbnailBackfillMaxAttempts {
		thumbJob("bfexhausted00000000000000000001", "failed", "2 days")
	}
	asset("bfrecentattempt000000000000001", "vod", "5 days", "")
	thumbJob("bfrecentattempt000000000000001", "failed", "1 hour")
	asset("bfdeleted0000000000000000000001", "vod", "5 days", "status = 'deleted'")
	thumbJob("bfdeleted0000000000000000000001", "queued", "1 day")

	var lifecycles []string
	job := &ThumbnailBackfillJob{
		db:     conn,
		logger: logging.NewLogger(),
		resolveProcesses: func(_ context.Context, tenantID, lifecycle, clusterID, _ string) (string, error) {
			if tenantID != backfillTenant || clusterID != "cell-a" {
				t.Fatalf("policy resolved for tenant %s cluster %s", tenantID, clusterID)
			}
			lifecycles = append(lifecycles, lifecycle)
			if lifecycle == "vod" {
				return `[{"process":"AV","codec":"AAC"}]`, nil
			}
			return `[{"process":"Livepeer","target_profiles":[]},{"process":"Thumbs","track_select":"video=maxbps"}]`, nil
		},
		stopCh: make(chan struct{}),
	}

	queued, err := job.backfill(ctx)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if queued != 2 {
		t.Fatalf("queued %d thumbnail jobs, want 2", queued)
	}
	if want := []string{"vod", "dvr_finalize", "clip"}; len(lifecycles) != 3 || lifecycles[0] != want[0] || lifecycles[1] != want[1] || lifecycles[2] != want[2] {
		t.Fatalf("resolved lifecycles %v, want %v", lifecycles, want)
	}

	type jobRow struct{ hash, status, processes, errMsg string }
	rows, err := conn.QueryContext(ctx, `SELECT artifact_hash, status, COALESCE(processes_json, ''), COALESCE(error_message, '')
		FROM foghorn.processing_jobs WHERE job_type = 'thumbnail'
		  AND artifact_hash NOT IN ('bfexhausted00000000000000000001', 'bfrecentattempt000000000000001')
		ORDER BY artifact_hash`)
	if err != nil {
		t.Fatal(err)
	}
	var got []jobRow
	for rows.Next() {
		var r jobRow
		if err := rows.Scan(&r.hash, &r.status, &r.processes, &r.errMsg); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	_ = rows.Close()
	want := map[string]string{
		"bfchapter00000000000000000000001": "queued",
		"bfclip00000000000000000000000001": "queued",
		"bfdeleted0000000000000000000001":  "failed",
		"bfvodnothumbs0000000000000000001": "failed",
	}
	if len(got) != len(want) {
		t.Fatalf("thumbnail jobs %+v, want one per %v", got, want)
	}
	for _, r := range got {
		if want[r.hash] != r.status {
			t.Fatalf("thumbnail job for %s is %s (%s), want %s", r.hash, r.status, r.errMsg, want[r.hash])
		}
		if r.status == "queued" && r.processes != `[{"process":"Thumbs","track_select":"video=maxbps"}]` {
			t.Fatalf("thumbnail job for %s carries processes %s, want only the Thumbs process", r.hash, r.processes)
		}
	}

	// Both slots are taken: a second pass queues nothing.
	if queued, err := job.backfill(ctx); err != nil || queued != 0 {
		t.Fatalf("second pass queued %d (err %v), want 0 while both slots are active", queued, err)
	}

	claimed, err := foghorndb.New(conn).ClaimQueuedProcessingJobs(ctx)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	var claimedHashes []string
	for _, c := range claimed {
		if c.JobType != mist.ProcessingJobTypeThumbnail {
			t.Fatalf("claimed %s job for %s; the queued process job of a ready asset must stay unclaimed", c.JobType, c.ArtifactHash.String)
		}
		if !c.S3Url.Valid || c.S3Url.String == "" || !c.DurableBackendLocal || c.OriginClusterID != "cell-a" {
			t.Fatalf("claimed job for %s lacks its stored source: %+v", c.ArtifactHash.String, c)
		}
		claimedHashes = append(claimedHashes, c.ArtifactHash.String)
	}
	sort.Strings(claimedHashes)
	if len(claimedHashes) != 2 || claimedHashes[0] != "bfchapter00000000000000000000001" || claimedHashes[1] != "bfclip00000000000000000000000001" {
		t.Fatalf("claimed %v, want the two queued thumbnail jobs", claimedHashes)
	}
	var status string
	if err := conn.QueryRowContext(ctx, `SELECT status FROM foghorn.artifacts WHERE artifact_hash = 'bfclip00000000000000000000000001'`).Scan(&status); err != nil || status != "ready" {
		t.Fatalf("clip status after claim = %q (err %v), want ready", status, err)
	}
}
