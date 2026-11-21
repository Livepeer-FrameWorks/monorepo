//go:build schema_verify

package commodoredb

import "testing"

func TestProcessingCatalogFilterIncludesQueuedReason_RealPG(t *testing.T) {
	conn := startCommodoreQueryCatalogRealPG(t)
	const tenant = "11000000-0000-0000-0000-000000000081"
	const user = "22000000-0000-0000-0000-000000000081"
	if _, err := conn.Exec(`INSERT INTO commodore.users(id,tenant_id,email,password_hash) VALUES($1,$2,'waiting@example.test','x')`, user, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO commodore.vod_assets(tenant_id,user_id,vod_hash,internal_name,playback_id,filename,lifecycle_status,processing_wait_reason) VALUES($1,$2,'waiting-vod','waiting-runtime','waiting-play','source.mov','queued','memory')`, tenant, user); err != nil {
		t.Fatal(err)
	}
	q := New(conn)
	catalog, err := q.ListStorageArtifactCatalog(t.Context(), StorageArtifactFilter{TenantID: tenant, Status: "processing", SortField: "created_at", SortDirection: "DESC", Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Rows) != 1 || catalog.Rows[0].Status.String != "queued" || catalog.Rows[0].ProcessingWaitReason != "memory" {
		t.Fatalf("Processing filter lost its queued upload: %+v", catalog)
	}
	other, err := q.ListStorageArtifactCatalog(t.Context(), StorageArtifactFilter{TenantID: "11000000-0000-0000-0000-000000000082", Status: "processing", SortField: "created_at", SortDirection: "DESC", Limit: 25})
	if err != nil || len(other.Rows) != 0 {
		t.Fatalf("foreign tenant saw queued upload: %+v %v", other, err)
	}
}
