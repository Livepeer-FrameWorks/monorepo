package control

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/DATA-DOG/go-sqlmock"
)

// A clip of a bare mist_native source can reach thumbnail readiness with no
// cluster stamped on its artifact row. The uploading node's cluster is ground
// truth then: the row must be backfilled so freeze resolution, playback URL
// construction, and the Commodore projection all heal.
func TestMarkArtifactHasThumbnails_BackfillsClusterFromUploadingNode(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	state.DefaultManager().SetNodeConnectionInfo(context.Background(), "edge-test-1", "edge-test-1.example", "", "media-test-1", nil)

	mock.ExpectQuery("UPDATE foghorn.artifacts.*SET has_thumbnails = true.*RETURNING").
		WithArgs("clip-hash-1").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "artifact_type", "storage_cluster_id", "origin_cluster_id"}).
			AddRow("tenant-1", "clip", nil, nil))

	mock.ExpectExec(`UPDATE foghorn.artifacts.*SET origin_cluster_id = \$2.*WHERE artifact_hash = \$1 AND origin_cluster_id IS NULL`).
		WithArgs("clip-hash-1", "media-test-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	// CommodoreClient is nil in tests: the projection is skipped, but the
	// foghorn-side backfill above must still have happened.
	markArtifactHasThumbnails("clip-hash-1", "edge-test-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// With a cluster already on the row, no backfill UPDATE runs.
func TestMarkArtifactHasThumbnails_NoBackfillWhenClusterPresent(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectQuery("UPDATE foghorn.artifacts.*SET has_thumbnails = true.*RETURNING").
		WithArgs("clip-hash-2").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "artifact_type", "storage_cluster_id", "origin_cluster_id"}).
			AddRow("tenant-1", "clip", nil, "media-eu-1"))

	markArtifactHasThumbnails("clip-hash-2", "edge-test-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Unknown node and no local cluster id: nothing to backfill with; no extra
// SQL beyond the readiness flip.
func TestMarkArtifactHasThumbnails_NoClusterResolvable(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	prevLocal := localClusterID
	localClusterID = ""
	t.Cleanup(func() { localClusterID = prevLocal })

	mock.ExpectQuery("UPDATE foghorn.artifacts.*SET has_thumbnails = true.*RETURNING").
		WithArgs("clip-hash-3").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "artifact_type", "storage_cluster_id", "origin_cluster_id"}).
			AddRow("tenant-1", "clip", nil, nil))

	markArtifactHasThumbnails("clip-hash-3", "node-unknown", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
