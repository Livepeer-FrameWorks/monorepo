//go:build schema_verify

package jobs

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
)

func TestRecordingCaptureWatermark_RealPG(t *testing.T) {
	testRecordingCaptureWatermark(t, startRealPGForCleanup(t))
}
func TestRecordingCaptureWatermark_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "foghorn_capture_watermark", "foghorn")
	if !ok {
		t.Skip("requires the shared Yugabyte contract fixture")
	}
	testRecordingCaptureWatermark(t, db)
}
func testRecordingCaptureWatermark(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	tenant := uuid.NewString()
	hash := "capture-watermark"
	start := time.Unix(1800000000, 0).UTC()
	end := start.Add(time.Hour)
	if _, err := db.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,artifact_type,tenant_id,origin_cluster_id,status,started_at,ended_at) VALUES($1,'dvr',$2,'origin','ready',$3,$4)`, hash, tenant, start, end); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE foghorn.artifacts SET catalog_synced_rev=catalog_revision WHERE tenant_id=$1 AND artifact_hash=$2`, tenant, hash); err != nil {
		t.Fatal(err)
	}
	ack := false
	calls := 0
	client := &mockCommodoreClient{snapshotRespFn: func(request *pb.UpdateArtifactCatalogSnapshotRequest) (*pb.UpdateArtifactCatalogSnapshotResponse, error) {
		calls++
		if !request.GetRecordingTimesPresent() || !request.GetStartedAt().AsTime().Equal(start) || !request.GetEndedAt().AsTime().Equal(end) {
			t.Errorf("capture snapshot lost its lifetime: %v", request)
		}
		return &pb.UpdateArtifactCatalogSnapshotResponse{Found: true, CurrentRevision: request.GetSourceRevision(), RecordingTimesPresent: ack}, nil
	}}
	reconciler := newTestReconciler(t, db, nil, client, nil)
	reconciler.clusterID = "origin"
	advanced, scanned := reconciler.projectCommodoreArtifactState(ctx)
	if advanced != 0 || scanned != 1 || calls != 1 {
		t.Fatalf("legacy acknowledgement hid capture work: %d/%d calls=%d", advanced, scanned, calls)
	}
	if _, err := db.Exec(`UPDATE foghorn.artifacts SET catalog_next_attempt_at=NULL WHERE tenant_id=$1 AND artifact_hash=$2`, tenant, hash); err != nil {
		t.Fatal(err)
	}
	ack = true
	advanced, scanned = reconciler.projectCommodoreArtifactState(ctx)
	if advanced != 1 || scanned != 1 {
		t.Fatalf("upgraded consumer did not repair equal revision: %d/%d", advanced, scanned)
	}
	readRevision := func() (int64, int64, int64) {
		t.Helper()
		var revision, catalog, capture int64
		if err := db.QueryRow(`SELECT catalog_revision,catalog_synced_rev,catalog_capture_times_synced_rev FROM foghorn.artifacts WHERE tenant_id=$1 AND artifact_hash=$2`, tenant, hash).Scan(&revision, &catalog, &capture); err != nil {
			t.Fatal(err)
		}
		return revision, catalog, capture
	}
	revision, catalog, capture := readRevision()
	if revision != catalog || revision != capture {
		t.Fatalf("capture watermark failed to converge: %d/%d/%d", revision, catalog, capture)
	}
	_, scanned = reconciler.projectCommodoreArtifactState(ctx)
	if scanned != 0 {
		t.Fatal("watermark write re-dirtied the recording")
	}
	end = end.Add(time.Minute)
	if _, err := db.Exec(`UPDATE foghorn.artifacts SET ended_at=$1 WHERE tenant_id=$2 AND artifact_hash=$3`, end, tenant, hash); err != nil {
		t.Fatal(err)
	}
	newer, _, oldCapture := readRevision()
	if newer <= revision || oldCapture != revision {
		t.Fatalf("capture-time correction was not versioned: %d -> %d capture=%d", revision, newer, oldCapture)
	}
	if _, err := db.Exec(`UPDATE foghorn.artifacts SET catalog_synced_rev=catalog_revision WHERE tenant_id=$1 AND artifact_hash=$2`, tenant, hash); err != nil {
		t.Fatal(err)
	}
	advanced, scanned = reconciler.projectCommodoreArtifactState(ctx)
	if advanced != 1 || scanned != 1 {
		t.Fatalf("an older replica's catalog ack hid newer capture times: %d/%d", advanced, scanned)
	}
	if err := foghorndb.New(db).AdvanceCatalogWatermark(ctx, foghorndb.AdvanceCatalogWatermarkParams{CatalogSyncedRev: revision, ArtifactHash: hash}); err != nil {
		t.Fatal(err)
	}
	final, finalCatalog, finalCapture := readRevision()
	if final != newer || finalCatalog != newer || finalCapture != newer {
		t.Fatalf("stale acknowledgement regressed watermarks: %d/%d/%d", final, finalCatalog, finalCapture)
	}
}
