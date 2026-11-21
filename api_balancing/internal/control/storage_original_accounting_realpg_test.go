//go:build schema_verify

package control

import (
	"fmt"
	"testing"

	"frameworks/api_balancing/internal/database/foghorndb"
)

func TestRetainedOriginalIsBilledAndCountedOnceWhileStillActive_RealPG(t *testing.T) {
	conn := startRealPG(t)
	ctx := t.Context()
	for i, phase := range []struct {
		name, status, sync, active             string
		size, expectedCold, files, expectedCap int64
	}{
		{"original-processing", "processing", "synced", "original", 1000, 1000, 1, 1000},
		{"original-size-placeholder", "processing", "synced", "original", 0, 1000, 1, 1000},
		{"original-size-stale", "processing", "synced", "original", 600, 1000, 1, 1000},
		{"original-ready", "ready", "synced", "original", 1000, 1000, 1, 1000},
		{"original-ready-larger", "ready", "synced", "original", 1200, 1200, 1, 1200},
		{"original-pending", "processing", "pending", "original", 1000, 1000, 1, 1000},
		{"processed-local-original-key", "ready", "pending", "original", 600, 1000, 1, 1600},
		{"processed-pending", "processing", "pending", "processed", 600, 1000, 1, 1600},
		{"processed-synced", "ready", "synced", "processed", 600, 1600, 2, 1600},
	} {
		tenant := fmt.Sprintf("7a1e0000-0000-4000-8000-%012d", 700+i)
		hash := fmt.Sprintf("retained-phase-%02d", i)
		if _, err := conn.ExecContext(ctx, `INSERT INTO foghorn.artifacts(artifact_hash,artifact_type,tenant_id,status,sync_status,durable_backend_local,size_bytes,active_object_key)VALUES($1,'vod',$2::uuid,$3,$4,true,$5,$6)`, hash, tenant, phase.status, phase.sync, phase.size, phase.active); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO foghorn.vod_metadata(artifact_hash,s3_key,original_s3_key,original_size_bytes,keep_original)VALUES($1,$2,'original',1000,true)`, hash, phase.active); err != nil {
			t.Fatal(err)
		}
		rows, err := foghorndb.New(conn).ListColdStorageUsage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range rows {
			if row.TenantID != tenant {
				continue
			}
			found = true
			if row.TotalBytes != phase.expectedCold || row.FileCount != phase.files {
				t.Errorf("%s billed %d bytes/%d files; want %d/%d", phase.name, row.TotalBytes, row.FileCount, phase.expectedCold, phase.files)
			}
		}
		if !found {
			t.Errorf("%s original omitted from billing", phase.name)
		}
		counted, err := foghorndb.New(conn).SumTenantActiveArtifactBytes(ctx, tenant)
		if err != nil {
			t.Fatal(err)
		}
		if counted != phase.expectedCap {
			t.Errorf("%s storage cap counted %d bytes; want %d", phase.name, counted, phase.expectedCap)
		}
	}
}
