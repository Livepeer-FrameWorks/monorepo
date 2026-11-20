//go:build schema_verify

package control

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/identity"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestVodOriginalReadDoesNotBlockSync_RealPG(t *testing.T) {
	conn := startRealPG(t)
	ctx := t.Context()
	const tenant = "7a1e0000-0000-4000-8000-000000000053"
	const hash = "original-download-sync"
	if _, err := conn.ExecContext(ctx, `INSERT INTO foghorn.artifacts
		(artifact_hash, artifact_type, tenant_id, status, active_object_key)
		VALUES ($1, 'vod', $2::uuid, 'ready', 'processed.mkv')`, hash, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO foghorn.vod_metadata
		(artifact_hash, original_s3_key, original_size_bytes, keep_original) VALUES ($1, 'original.mov', 2000, true)`, hash); err != nil {
		t.Fatal(err)
	}
	read, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Rollback()
	if _, err := foghorndb.New(read).GetVodOriginal(ctx, foghorndb.GetVodOriginalParams{TenantID: tenant, ArtifactHash: hash}); err != nil {
		t.Fatal(err)
	}
	write, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer write.Rollback()
	if _, err := write.ExecContext(ctx, `SET LOCAL lock_timeout = '250ms'`); err != nil {
		t.Fatal(err)
	}
	if _, err := write.ExecContext(ctx, `UPDATE foghorn.artifacts SET active_object_key = 'new.mkv'
		WHERE tenant_id = $1::uuid AND artifact_hash = $2`, tenant, hash); err != nil {
		t.Fatalf("original download lookup blocked media sync: %v", err)
	}
	if _, err := write.ExecContext(ctx, `UPDATE foghorn.vod_metadata SET s3_key = 'new.mkv'
		WHERE artifact_hash = $1`, hash); err != nil {
		t.Fatalf("original download lookup blocked metadata sync: %v", err)
	}
	if err := write.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestVodOriginalSurvivesSyncPublication_RealPG(t *testing.T) {
	conn := startRealPG(t)
	_, store, _ := setupArtifactTestDeps(t)
	SetDB(conn)
	ctx := t.Context()
	const tenant = "7a1e0000-0000-4000-8000-000000000054"
	const hash = "original-survives-sync"
	const original = "uploads/original.mkv"
	const descriptor = "processed/video.mkv"
	identity.SetDefault(identity.NewResolver(identity.Config{
		RegistryArtifact: func(context.Context, string) (identity.ArtifactIdentity, error) {
			return identity.ArtifactIdentity{ArtifactHash: hash, TenantID: tenant}, nil
		},
	}))
	store.headObjectInfoFn = stagingHead(1000, false, 0)
	if _, err := conn.ExecContext(ctx, `INSERT INTO foghorn.artifacts
		(artifact_hash, artifact_type, tenant_id, status, format, active_object_key, active_dtsh_key, sync_status)
		VALUES ($1, 'vod', $2::uuid, 'ready', 'mkv', $3, 'old.dtsh', 'pending')`, hash, tenant, original); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO foghorn.vod_metadata
		(artifact_hash, s3_key, original_s3_key, original_size_bytes, keep_original)
		VALUES ($1, $2::text, $2::text, 2000, true)`, hash, original); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO foghorn.artifact_nodes
		(artifact_hash, node_id, is_complete, is_orphaned) VALUES ($1, 'edge-1', true, false)`, hash); err != nil {
		t.Fatal(err)
	}
	if claimed, err := ClaimFreezeAttempt(ctx, conn, hash, "retain-original", "edge-1", tenant, "", descriptor); err != nil || !claimed {
		t.Fatalf("claim processed rendition: claimed=%v error=%v", claimed, err)
	}
	if err := processSyncCompleteAt(&ipcpb.SyncComplete{
		AssetHash: hash, RequestId: "retain-original", Status: "success",
	}, "edge-1", time.Now().UnixMilli(), logging.NewLogger()); err != nil {
		t.Fatal(err)
	}
	var active, kept, sync string
	var originalQueued, oldIndexQueued bool
	if err := conn.QueryRowContext(ctx, `SELECT a.active_object_key, a.sync_status, v.original_s3_key,
		EXISTS(SELECT 1 FROM foghorn.staging_cleanup_queue WHERE object_key = $3),
		EXISTS(SELECT 1 FROM foghorn.staging_cleanup_queue WHERE object_key = 'old.dtsh')
		FROM foghorn.artifacts a JOIN foghorn.vod_metadata v USING (artifact_hash)
		WHERE a.tenant_id = $1::uuid AND a.artifact_hash = $2`, tenant, hash, original).
		Scan(&active, &sync, &kept, &originalQueued, &oldIndexQueued); err != nil {
		t.Fatal(err)
	}
	if active != FreezePublishKey(descriptor, "retain-original") || sync != "synced" || kept != original || originalQueued || !oldIndexQueued {
		t.Fatalf("sync lost retention or garbage collection: active=%q sync=%q kept=%q originalQueued=%v indexQueued=%v",
			active, sync, kept, originalQueued, oldIndexQueued)
	}
}
