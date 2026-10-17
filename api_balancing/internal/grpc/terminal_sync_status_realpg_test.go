//go:build schema_verify

package grpc

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/google/uuid"
)

// Staging rc10: an aborted upload stayed deleted|in_progress. A multipart
// upload row is sync_status='in_progress' from creation; a terminal transition
// (abort, or a delete during a freeze) must settle it, since no sync can run
// for a terminal artifact.
func TestTerminalArtifactSettlesSyncStatus_RealPG(t *testing.T) {
	conn := startFoghornGRPCRealPG(t)
	t.Cleanup(control.SetupTestRegistry("", nil))
	s3 := &fakeVodS3Client{}
	srv := NewFoghornGRPCServer(conn, logging.NewLogger(), nil, nil, nil, nil, s3, nil)
	srv.SetClusterID("central-primary")
	srv.SetStorageResolverFactory(func(_ context.Context, _ string) *storage.ClusterResolver {
		return &storage.ClusterResolver{LocalClusterID: "central-primary", LocalS3ClientPresent: true}
	})
	ctx := context.Background()
	tenantID, userID := uuid.NewString(), uuid.NewString()
	create := func(t *testing.T, hash, uploadID string) {
		t.Helper()
		s3.createID = uploadID
		internalName := "vod-" + hash
		if _, err := srv.CreateVodUpload(ctx, &sharedpb.CreateVodUploadRequest{
			TenantId: tenantID, UserId: userID, Filename: "video.mp4", SizeBytes: 1024,
			VodHash: &hash, InternalName: &internalName, ClusterId: "central-primary",
		}); err != nil {
			t.Fatal(err)
		}
	}
	syncState := func(t *testing.T, hash string) (status, syncStatus, location string) {
		t.Helper()
		if err := conn.QueryRow(`SELECT status, COALESCE(sync_status, ''), COALESCE(storage_location, '')
			FROM foghorn.artifacts WHERE artifact_hash = $1`, hash).Scan(&status, &syncStatus, &location); err != nil {
			t.Fatal(err)
		}
		return status, syncStatus, location
	}

	t.Run("an aborted upload settles its sync status", func(t *testing.T) {
		const hash = "vodterminalabort000000000000001"
		create(t, hash, "up-terminal-abort")
		if _, sync, _ := syncState(t, hash); sync != "in_progress" {
			t.Fatalf("created upload sync_status = %q, want in_progress", sync)
		}
		if _, err := srv.AbortVodUpload(ctx, &sharedpb.AbortVodUploadRequest{TenantId: tenantID, UploadId: "up-terminal-abort"}); err != nil {
			t.Fatal(err)
		}
		if status, sync, _ := syncState(t, hash); status != "deleted" || sync != "failed" {
			t.Fatalf("aborted upload = %s|%s, want deleted|failed", status, sync)
		}
	})

	t.Run("a delete during a freeze settles the freeze", func(t *testing.T) {
		const hash = "vodterminalfreeze00000000000001"
		create(t, hash, "up-terminal-freeze")
		if _, err := conn.Exec(`UPDATE foghorn.artifacts
			SET status = 'ready', storage_location = 'freezing', sync_object_key = 'vod/k.mp4',
			    sync_request_id = 'freeze-1', sync_node_id = 'edge-1'
			WHERE artifact_hash = $1`, hash); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(`UPDATE foghorn.artifacts SET status = 'deleted' WHERE artifact_hash = $1`, hash); err != nil {
			t.Fatal(err)
		}
		if status, sync, location := syncState(t, hash); status != "deleted" || sync != "failed" || location != "local" {
			t.Fatalf("deleted freezing artifact = %s|%s|%s, want deleted|failed|local", status, sync, location)
		}
	})

	t.Run("the data migration settles rows aborted before the trigger", func(t *testing.T) {
		const hash = "vodterminallegacy00000000000001"
		create(t, hash, "up-terminal-legacy")
		if _, err := conn.Exec(`ALTER TABLE foghorn.artifacts DISABLE TRIGGER artifact_settle_sync_status_on_terminal`); err != nil {
			t.Fatal(err)
		}
		_, abortErr := srv.AbortVodUpload(ctx, &sharedpb.AbortVodUploadRequest{TenantId: tenantID, UploadId: "up-terminal-legacy"})
		if _, err := conn.Exec(`ALTER TABLE foghorn.artifacts ENABLE TRIGGER artifact_settle_sync_status_on_terminal`); err != nil {
			t.Fatal(err)
		}
		if abortErr != nil {
			t.Fatal(abortErr)
		}
		queries := foghorndb.New(conn)
		if remaining, err := queries.CountTerminalArtifactsWithSyncInProgress(ctx); err != nil || remaining != 1 {
			t.Fatalf("terminal rows in progress before the backfill = %d (%v), want 1", remaining, err)
		}
		progress, err := queries.BackfillTerminalSyncStatusBatch(ctx, 1000)
		if err != nil || progress.ChangedCount != 1 {
			t.Fatalf("backfill = %+v (%v), want one row changed", progress, err)
		}
		if status, sync, _ := syncState(t, hash); status != "deleted" || sync != "failed" {
			t.Fatalf("backfilled upload = %s|%s, want deleted|failed", status, sync)
		}
		if remaining, err := queries.CountTerminalArtifactsWithSyncInProgress(ctx); err != nil || remaining != 0 {
			t.Fatalf("terminal rows in progress after the backfill = %d (%v), want 0", remaining, err)
		}
	})
}
