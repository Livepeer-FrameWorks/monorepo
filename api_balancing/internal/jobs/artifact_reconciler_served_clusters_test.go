package jobs

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestCatalogProjectionUsesAssignedOriginAuthority(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := &mockCommodoreClient{}
	r := newTestReconciler(t, db, nil, client, nil)
	r.clusterID = "control-cell"
	r.batchSize = 10
	assigned := []string{"media-a", "media-b"}
	r.servedClusterIDs = func() []string { return assigned }
	for _, origin := range assigned {
		lifecycle := "ready"
		if origin == "media-b" {
			lifecycle = "deleted"
		}
		mock.ExpectQuery("FROM foghorn.artifacts").WithArgs(10, origin).
			WillReturnRows(sqlmock.NewRows(projectionRowCols).AddRow(
				origin+"-artifact", "vod", "tenant", origin, true, int64(128), int64(6000), nil,
				"synced", true, "s3", int64(7), lifecycle, nil, nil, nil))
		mock.ExpectExec("UPDATE foghorn.artifacts SET catalog_synced_rev").
			WithArgs(int64(7), origin+"-artifact").WillReturnResult(sqlmock.NewResult(0, 1))
	}
	advanced, scanned := r.projectCommodoreArtifactState(context.Background())
	if advanced != 2 || scanned != 2 || len(client.snapshotCalls) != 2 {
		t.Fatalf("projection advanced=%d scanned=%d calls=%d", advanced, scanned, len(client.snapshotCalls))
	}
	for i, origin := range assigned {
		snapshot := client.snapshotCalls[i]
		if snapshot.GetSourceClusterId() != origin || snapshot.GetAssetKey() != origin+"-artifact" {
			t.Fatalf("wrong origin authority: %v", snapshot)
		}
		if snapshot.GetDeleted() != (origin == "media-b") {
			t.Fatalf("wrong deletion projection: %v", snapshot)
		}
	}
	// Revocation is read on the next pass; the control cell is not a fallback origin.
	assigned = nil
	advanced, scanned = r.projectCommodoreArtifactState(context.Background())
	if advanced != 0 || scanned != 0 || len(client.snapshotCalls) != 2 {
		t.Fatal("revoked assignments still projected")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogBackfillRespectsAssignedOrigins(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r := newTestReconciler(t, db, nil, nil, nil)
	r.clusterID = "control-cell"
	r.servedClusterIDs = func() []string { return []string{"media-a", "media-b"} }
	// No origin can be guessed for an unattributed row in a multi-cluster cell.
	r.backfillOriginCluster(context.Background())
	for _, origin := range []string{"media-a", "media-b"} {
		mock.ExpectExec("UPDATE foghorn.artifacts").WithArgs(catalogBackfillBatch, origin).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	if got := r.backfillCatalogRevisions(context.Background()); got != 2 {
		t.Fatalf("seeded %d rows, want 2", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
