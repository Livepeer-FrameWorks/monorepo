//go:build schema_verify

package jobs

import (
	"database/sql"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// A restarted sidecar registers without the jobs its previous process ran.
// The work assigned to it before the registration and not reported comes back
// at once: the processing job is requeued and the chapter finalization becomes
// re-claimable. Reported work, work of other nodes, and work assigned after
// the registration are untouched.
func TestReconcileNodeJobInventoryRedispatchesUnreportedWork_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)

	const (
		tenant  = "00000000-0000-4000-8000-0000000000ab"
		node    = "edge-restarted"
		lostJob = "00000000-0000-4000-8000-00000000a001"
		keptJob = "00000000-0000-4000-8000-00000000a002"
		newJob  = "00000000-0000-4000-8000-00000000a003"
		otherJb = "00000000-0000-4000-8000-00000000a004"
	)
	registeredAt := time.Now()
	old := registeredAt.Add(-10 * time.Minute)

	for _, hash := range []string{"inventoryvod000000000000000000a1", "inventoryvod000000000000000000a2", "inventoryvod000000000000000000a3", "inventoryvod000000000000000000a4"} {
		if _, err := conn.Exec(`INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status) VALUES ($1, 'vod', $2, 'processing')`, hash, tenant); err != nil {
			t.Fatal(err)
		}
	}
	seedJob := func(id, hash, jobNode string, updated time.Time) {
		t.Helper()
		if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs (job_id, tenant_id, artifact_hash, job_type, status, processing_node_id, updated_at)
			VALUES ($1, $2, $3, 'transcode', 'processing', $4, $5)`, id, tenant, hash, jobNode, updated); err != nil {
			t.Fatal(err)
		}
	}
	seedJob(lostJob, "inventoryvod000000000000000000a1", node, old)
	seedJob(keptJob, "inventoryvod000000000000000000a2", node, old)
	seedJob(newJob, "inventoryvod000000000000000000a3", node, registeredAt.Add(time.Second))
	seedJob(otherJb, "inventoryvod000000000000000000a4", "edge-other", old)

	const dvr = "inventorydvr000000000000000000b1"
	if _, err := conn.Exec(`INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status) VALUES ($1, 'dvr', $2, 'completed')`, dvr, tenant); err != nil {
		t.Fatal(err)
	}
	seedChapter := func(id string, start int64, attempts int32) {
		t.Helper()
		if _, err := conn.Exec(`INSERT INTO foghorn.dvr_chapters (chapter_id, artifact_hash, mode, interval_seconds, start_ms, end_ms, state, finalize_node_id, finalize_attempts, finalize_started_at)
			VALUES ($1, $2, 'window_sized_chapters', 60, $3::bigint, $3::bigint + 60000, 'finalizing', $4, $5, $6)`, id, dvr, start, node, attempts, old); err != nil {
			t.Fatal(err)
		}
	}
	seedChapter("inventorychapterlost000000000001", 0, 1)
	seedChapter("inventorychapterkept000000000001", 60000, 2)
	seedChapter("inventorychapterstale00000000001", 120000, 3)

	reported := []string{
		keptJob,
		"chapter-finalize-v2-2-inventorychapterkept000000000001",
		// An older attempt still running does not cover the current one.
		"chapter-finalize-v2-2-inventorychapterstale00000000001",
	}
	if err := ReconcileNodeJobInventory(t.Context(), conn, node, reported, registeredAt, 3, logging.NewLogger()); err != nil {
		t.Fatal(err)
	}

	jobState := func(id string) (string, sql.NullString) {
		t.Helper()
		var status string
		var assigned sql.NullString
		if err := conn.QueryRow(`SELECT status, processing_node_id FROM foghorn.processing_jobs WHERE job_id = $1`, id).Scan(&status, &assigned); err != nil {
			t.Fatal(err)
		}
		return status, assigned
	}
	if status, assigned := jobState(lostJob); status != "queued" || assigned.Valid {
		t.Fatalf("lost job = %s on %v, want queued and unassigned", status, assigned)
	}
	for _, id := range []string{keptJob, newJob, otherJb} {
		if status, _ := jobState(id); status != "processing" {
			t.Fatalf("job %s = %s, want untouched", id, status)
		}
	}

	expired := func(id string) bool {
		t.Helper()
		var started time.Time
		if err := conn.QueryRow(`SELECT finalize_started_at FROM foghorn.dvr_chapters WHERE chapter_id = $1`, id).Scan(&started); err != nil {
			t.Fatal(err)
		}
		return started.Before(time.Unix(1, 0))
	}
	if !expired("inventorychapterlost000000000001") || !expired("inventorychapterstale00000000001") {
		t.Fatal("unreported chapter finalizations were not made re-claimable")
	}
	if expired("inventorychapterkept000000000001") {
		t.Fatal("a chapter finalization the node reported running was expired")
	}
}

// A job still reporting progress when its sidecar restarts has a fresh
// updated_at; its assignment is what decides whether the registration could
// have carried it, so it is requeued at once instead of by the stale sweep.
func TestReconcileNodeJobInventoryRequeuesRunningJob_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)

	const (
		tenant = "00000000-0000-4000-8000-0000000000ac"
		node   = "edge-restarted-mid-job"
		job    = "00000000-0000-4000-8000-00000000b001"
		hash   = "inventoryvod000000000000000000b1"
	)
	registeredAt := time.Now()
	if _, err := conn.Exec(`INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status) VALUES ($1, 'vod', $2, 'processing')`, hash, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs (job_id, tenant_id, artifact_hash, job_type, status, processing_node_id, started_at, updated_at)
		VALUES ($1, $2, $3, 'transcode', 'processing', $4, $5, $6)`,
		job, tenant, hash, node, registeredAt.Add(-10*time.Minute), registeredAt.Add(-500*time.Millisecond)); err != nil {
		t.Fatal(err)
	}

	if err := ReconcileNodeJobInventory(t.Context(), conn, node, nil, registeredAt, 3, logging.NewLogger()); err != nil {
		t.Fatal(err)
	}
	var status string
	var assigned sql.NullString
	if err := conn.QueryRow(`SELECT status, processing_node_id FROM foghorn.processing_jobs WHERE job_id = $1`, job).Scan(&status, &assigned); err != nil {
		t.Fatal(err)
	}
	if status != "queued" || assigned.Valid {
		t.Fatalf("running job the restarted node did not report: status %q node %v, want queued and unassigned", status, assigned)
	}
}
