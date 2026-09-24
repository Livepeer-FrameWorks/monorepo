//go:build schema_verify

package control

import (
	"database/sql"
	"testing"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
)

// A recording's parent row follows its segments into this cell's S3: the first uploaded segment moves it from
// pending to 's3'/'in_progress' on the local cluster and backend, and finalization settles it to 'synced' with the
// recording prefix as s3_url. Only then does cold-storage usage (the storageCost source) count the recording.
func TestDVRParentStorageSettles_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prevDB := db
	SetDB(conn)
	prevLocal := localClusterID
	SetLocalClusterID("cluster-eu")
	prevRetry := FinalizeRetrySeconds
	FinalizeRetrySeconds = 0
	t.Cleanup(func() {
		SetDB(prevDB)
		SetLocalClusterID(prevLocal)
		FinalizeRetrySeconds = prevRetry
	})
	const tenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	const hash = "recording-parent-storage"
	start := time.Now().UTC().Add(-90 * time.Second).Truncate(time.Second)
	if _, err := conn.ExecContext(t.Context(), `INSERT INTO foghorn.artifacts
		(artifact_hash, artifact_type, tenant_id, status, stream_internal_name, origin_cluster_id, dvr_start_dispatch, size_bytes)
		VALUES ($1, 'dvr', $2, 'recording', 'live-stream', 'cluster-eu', '{"node_id":"owner"}', 4096)`, hash, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `INSERT INTO foghorn.dvr_segments
		(artifact_hash, segment_name, sequence, media_start_ms, media_end_ms, duration_ms, s3_key, status)
		VALUES ($1, 'segment.ts', 1, $2::bigint, $2::bigint + 6000, 6000, 'dvr/segment.ts', 'pending')`, hash, start.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	type parentState struct {
		location, sync, cluster, backend, s3URL sql.NullString
		local                                   bool
	}
	readParent := func() parentState {
		t.Helper()
		var p parentState
		if err := conn.QueryRowContext(t.Context(), `SELECT storage_location, sync_status, storage_cluster_id, backend_id, s3_url, durable_backend_local
			FROM foghorn.artifacts WHERE artifact_hash = $1`, hash).Scan(&p.location, &p.sync, &p.cluster, &p.backend, &p.s3URL, &p.local); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if p := readParent(); p.sync.String != "pending" || p.local {
		t.Fatalf("fresh recording parent = %+v, want pending and not locally backed", p)
	}

	if err := MarkDVRSegmentUploaded(t.Context(), tenant, hash, "segment.ts", 2048); err != nil {
		t.Fatal(err)
	}
	p := readParent()
	if p.location.String != "s3" || p.sync.String != "in_progress" || p.cluster.String != "cluster-eu" ||
		p.backend.String != testCellBackendID || !p.local {
		t.Fatalf("parent after first upload = %+v, want s3/in_progress on cluster-eu with the cell backend", p)
	}

	if _, err := conn.ExecContext(t.Context(), `UPDATE foghorn.artifacts SET ended_at = $2 WHERE artifact_hash = $1`, hash, start.Add(80*time.Second)); err != nil {
		t.Fatal(err)
	}
	result, err := FinalizeDVR(t.Context(), hash, FinalizeOptions{ReportingNodeID: "owner", StartedAtUnix: start.Unix()})
	if err != nil || result.ArtifactStatus != "completed" {
		t.Fatalf("finalize = %+v, %v", result, err)
	}
	p = readParent()
	wantURL := "s3://bucket/" + tenant + "/live-stream/dvr/" + hash
	if p.sync.String != "synced" || p.location.String != "s3" || p.s3URL.String != wantURL || !p.local {
		t.Fatalf("parent after finalize = %+v, want synced with s3_url %q", p, wantURL)
	}

	usage, err := foghorndb.New(conn).ListColdStorageUsage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	counted := false
	for _, row := range usage {
		if row.TenantID == tenant && row.ArtifactType == "dvr" && row.TotalBytes > 0 {
			counted = true
		}
	}
	if !counted {
		t.Fatalf("cold storage usage %+v does not count the finalized recording", usage)
	}
}
