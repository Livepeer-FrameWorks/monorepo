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
		for _, status := range []string{"ready", "failed", "deleted"} {
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
	want := map[string]int64{"dvr": 2000, "clip": 2000, "vod": 1000}
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
