//go:build schema_verify

package control

import (
	"testing"

	"frameworks/api_balancing/internal/database/foghorndb"
)

func TestColdStorageUsageKeepsFailedRecordingBytes_RealPG(t *testing.T) {
	conn := startRealPG(t)
	ctx := t.Context()
	const tenant = "7a1e0000-0000-4000-8000-000000000055"
	for _, kind := range []string{"dvr", "clip", "vod"} {
		for _, status := range []string{"ready", "failed", "expired", "aborted", "deleted"} {
			if _, err := conn.ExecContext(ctx, `INSERT INTO foghorn.artifacts
				(artifact_hash, artifact_type, tenant_id, status, sync_status, durable_backend_local, size_bytes)
				VALUES ($1, $2, $3::uuid, $4, 'synced', true, 1000)`, kind+"-"+status, kind, tenant, status); err != nil {
				t.Fatal(err)
			}
		}
	}
	rows, err := foghorndb.New(conn).ListColdStorageUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"dvr": 4000, "clip": 4000, "vod": 1000}
	for _, row := range rows {
		if row.TenantID != tenant {
			continue
		}
		if row.TotalBytes != want[row.ArtifactType] || row.FileCount != want[row.ArtifactType]/1000 {
			t.Fatalf("%s billed %d bytes / %d files, want %d bytes", row.ArtifactType, row.TotalBytes, row.FileCount, want[row.ArtifactType])
		}
		delete(want, row.ArtifactType)
	}
	if len(want) != 0 {
		t.Fatalf("storage usage omitted durable artifacts: %v", want)
	}
}

func TestKeptSourceIsCountedOnceWhileProcessing_RealPG(t *testing.T) {
	conn := startRealPG(t)
	const tenant = "7a1e0000-0000-4000-8000-000000000056"
	const hash = "original-alias-processing"
	if _, err := conn.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,artifact_type,tenant_id,status,sync_status,durable_backend_local,size_bytes,active_object_key) VALUES($1,'vod',$2::uuid,'processing','synced',true,2000,'source.mp4')`, hash, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO foghorn.vod_metadata(artifact_hash,s3_key,keep_original,original_s3_key,original_size_bytes) VALUES($1,'source.mp4',true,'source.mp4',2000)`, hash); err != nil {
		t.Fatal(err)
	}
	q := foghorndb.New(conn)
	assertUsage := func(wantBytes, wantFiles int64) {
		t.Helper()
		rows, err := q.ListColdStorageUsage(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.TenantID == tenant {
				if row.TotalBytes != wantBytes || row.FileCount != wantFiles {
					t.Fatalf("source and retained original billed twice: bytes=%d files=%d, want %d/%d", row.TotalBytes, row.FileCount, wantBytes, wantFiles)
				}
				return
			}
		}
		t.Fatal("durable original was not billed")
	}
	assertUsage(2000, 1)
	current, err := q.SumTenantActiveArtifactBytes(t.Context(), tenant)
	if err != nil || current != 2000 {
		t.Fatalf("same-object source charged twice against storage cap: bytes=%d error=%v", current, err)
	}
	if _, err = conn.Exec(`UPDATE foghorn.artifacts SET status='ready',size_bytes=1000,sync_status='pending' WHERE artifact_hash=$1 AND tenant_id=$2::uuid`, hash, tenant); err != nil {
		t.Fatal(err)
	}
	assertUsage(2000, 1)
	current, err = q.SumTenantActiveArtifactBytes(t.Context(), tenant)
	if err != nil || current != 3000 {
		t.Fatalf("processed local output or retained source omitted from storage cap: bytes=%d error=%v", current, err)
	}
	if _, err = conn.Exec(`UPDATE foghorn.artifacts SET sync_status='synced',active_object_key='processed.mkv' WHERE artifact_hash=$1 AND tenant_id=$2::uuid`, hash, tenant); err != nil {
		t.Fatal(err)
	}
	assertUsage(3000, 2)
}
