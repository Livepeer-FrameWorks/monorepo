//go:build schema_verify

package grpc

import (
	"database/sql"
	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	"testing"
)

func TestFreeTierMediaFootprint_RealPG(t *testing.T) {
	testFreeTierMediaFootprint(t, startFoghornGRPCRealPG(t))
}
func TestFreeTierMediaFootprint_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "free_footprint", "foghorn")
	if !ok {
		t.Skip("requires shared Yugabyte fixture")
	}
	testFreeTierMediaFootprint(t, db)
}
func testFreeTierMediaFootprint(t *testing.T, db *sql.DB) {
	t.Helper()
	tenant, other := uuid.NewString(), uuid.NewString()
	const gib = int64(1) << 30
	for _, row := range []struct {
		hash, status string
		size         any
		days         any
		owner        string
		pointer      bool
	}{
		{"free-foot-clip", "ready", 2 * gib, 10, tenant, false},
		{"free-foot-original", "ready", 3 * gib, 10, tenant, false},
		{"free-foot-forever", "ready", gib, nil, tenant, false},
		{"free-foot-long", "ready", gib, 60, tenant, false},
		{"free-foot-remaining", "ready", gib, 29, tenant, false},
		{"free-foot-queued", "queued", nil, nil, tenant, false},
		{"free-foot-foreign", "ready", 50 * gib, 10, other, false},
		{"free-foot-warm", "ready", 50 * gib, 10, tenant, true},
		{"free-foot-failed", "failed", 50 * gib, 10, tenant, false},
	} {
		if _, err := db.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,tenant_id,artifact_type,status,size_bytes,retention_until,federated_pointer) VALUES($1,$2,'vod',$3,$4,NOW()+$5::integer*INTERVAL '1 day',$6)`, row.hash, row.owner, row.status, row.size, row.days, row.pointer); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE foghorn.artifacts SET active_object_key='original',sync_status='synced' WHERE tenant_id=$1 AND artifact_hash='free-foot-original'`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO foghorn.vod_metadata(artifact_hash,keep_original,original_s3_key,original_size_bytes) VALUES('free-foot-original',true,'original',$1)`, 3*gib); err != nil {
		t.Fatal(err)
	}
	q := foghorndb.New(db)
	got, err := q.GetTenantMediaFootprint(t.Context(), foghorndb.GetTenantMediaFootprintParams{TenantID: tenant, RetentionLimitDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	if got.StorageBytes != 8*gib || got.PendingArtifacts != 1 || got.IncompatibleRetention != 3 {
		t.Fatalf("wrong tenant/original/retention footprint: %+v", got)
	}
	uncapped, err := q.GetTenantMediaFootprint(t.Context(), foghorndb.GetTenantMediaFootprintParams{TenantID: tenant, RetentionLimitDays: 0})
	if err != nil || uncapped.IncompatibleRetention != 0 {
		t.Fatalf("explicit uncapped target changed: %+v %v", uncapped, err)
	}
	if _, err := db.Exec(`UPDATE foghorn.artifacts SET created_at=NOW()-INTERVAL '90 days' WHERE tenant_id=$1 AND artifact_hash='free-foot-remaining'`, tenant); err != nil {
		t.Fatal(err)
	}
	after, err := q.GetTenantMediaFootprint(t.Context(), foghorndb.GetTenantMediaFootprintParams{TenantID: tenant, RetentionLimitDays: 30})
	if err != nil || after.IncompatibleRetention != 3 {
		t.Fatalf("existing expiry was reinterpreted from creation: %+v %v", after, err)
	}
}
