//go:build schema_verify

package control

import (
	"database/sql"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// A Livepeer-to-local fallback persists the local ladder on the dispatched job
// row. The update binds the job's own UUID and its owning node, so a report
// from another node or with an id that is not the job's changes nothing.
func TestProcessConfigCacheUpdatePersistsForOwningJobOnly_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })

	const (
		tenant   = "00000000-0000-0000-0000-00000000c0de"
		artifact = "cachecfg0000000000000000000000aa"
		jobID    = "00000000-0000-0000-0000-0000000c0b01"
		livepeer = `[{"process":"Livepeer"}]`
		local    = `[{"process":"AV","codec":"H264"}]`
	)
	if _, err := conn.Exec(`INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status) VALUES ($1, 'vod', $2, 'processing')`, artifact, tenant); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs (job_id, tenant_id, artifact_hash, job_type, status, processing_node_id, processes_json)
		VALUES ($1, $2, $3, 'transcode', 'processing', 'node-owner', $4)`, jobID, tenant, artifact, livepeer); err != nil {
		t.Fatalf("seed job: %v", err)
	}

	var updates []string
	prevHook := onProcessConfigCacheUpdate
	SetProcessConfigCacheUpdater(func(hash, processes string) { updates = append(updates, hash+"="+processes) })
	t.Cleanup(func() { onProcessConfigCacheUpdate = prevHook })

	report := func(id, node string) {
		processProcessingJobResult(&ipcpb.ProcessingJobResult{
			JobId:   id,
			Status:  "cache_update",
			Outputs: map[string]string{"artifact_hash": artifact, "processes_json": local},
		}, node, logging.NewLogger())
	}
	stored := func() string {
		var got sql.NullString
		if err := conn.QueryRow(`SELECT processes_json FROM foghorn.processing_jobs WHERE job_id = $1`, jobID).Scan(&got); err != nil {
			t.Fatalf("read job: %v", err)
		}
		return got.String
	}

	report(jobID, "node-foreign")
	report("00000000-0000-0000-0000-0000000c0b99", "node-owner")
	// The id Helmsman used to send is not the job's; it can never persist.
	report("cache_update:"+artifact, "node-owner")
	if got := stored(); got != livepeer {
		t.Fatalf("processes_json changed by a foreign node or unknown job: %q", got)
	}
	if len(updates) != 0 {
		t.Fatalf("cache updated for a report that matched no job: %v", updates)
	}

	report(jobID, "node-owner")
	if got := stored(); got != local {
		t.Fatalf("processes_json = %q, want the local ladder %q", got, local)
	}
	if len(updates) != 1 || updates[0] != artifact+"="+local {
		t.Fatalf("cache updates = %v, want one for %s", updates, artifact)
	}
}
