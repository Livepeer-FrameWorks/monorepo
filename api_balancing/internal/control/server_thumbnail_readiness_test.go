package control

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/streamident"

	"github.com/DATA-DOG/go-sqlmock"
)

// A clip of a bare mist_native source can reach thumbnail readiness with no
// cluster stamped on its artifact row. The uploading node's cluster is ground
// truth then: the row must be backfilled so freeze resolution, playback URL
// construction, and the Commodore projection all heal. The reporting node is
// authorized for the artifact tenant (dedicated node), so the confirmation is
// honored.
func TestMarkArtifactHasThumbnails_BackfillsClusterFromUploadingNode(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	stubClusterEntitlements(t, map[string][]string{"tenant-1": {"media-test-1"}})
	state.DefaultManager().SetNodeConnectionInfo(context.Background(), "edge-test-1", "edge-test-1.example", "tenant-1", "media-test-1", nil)

	mock.ExpectQuery(`SELECT tenant_id::text, artifact_type, storage_cluster_id, origin_cluster_id, COALESCE\(has_thumbnails, false\)\s+FROM foghorn\.artifacts`).
		WithArgs("clip-hash-1").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "artifact_type", "storage_cluster_id", "origin_cluster_id", "has_thumbnails"}).
			AddRow("tenant-1", "clip", nil, nil, false))
	mock.ExpectExec(`UPDATE foghorn\.artifacts\s+SET has_thumbnails = true`).
		WithArgs("clip-hash-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE foghorn\.artifacts.*SET origin_cluster_id = \$2.*WHERE artifact_hash = \$1 AND origin_cluster_id IS NULL`).
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

	stubClusterEntitlements(t, map[string][]string{"tenant-1": {"media-eu-1"}})
	state.DefaultManager().SetNodeConnectionInfo(context.Background(), "edge-test-2", "edge-test-2.example", "tenant-1", "media-eu-1", nil)

	mock.ExpectQuery(`SELECT tenant_id::text, artifact_type, storage_cluster_id, origin_cluster_id, COALESCE\(has_thumbnails, false\)\s+FROM foghorn\.artifacts`).
		WithArgs("clip-hash-2").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "artifact_type", "storage_cluster_id", "origin_cluster_id", "has_thumbnails"}).
			AddRow("tenant-1", "clip", nil, "media-eu-1", false))
	mock.ExpectExec(`UPDATE foghorn\.artifacts\s+SET has_thumbnails = true`).
		WithArgs("clip-hash-2").
		WillReturnResult(sqlmock.NewResult(0, 1))

	markArtifactHasThumbnails("clip-hash-2", "edge-test-2", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// nodeProducesThumbnailResource is the per-resource task authorization for thumbnail minting: a node may only
// mint a fixed-key PUT for an artifact it holds a copy of OR is the assigned processing node for. Fail closed
// on empty inputs and on a node that neither holds nor processes the artifact.
func TestNodeProducesThumbnailResource_ArtifactOwnership(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	ctx := context.Background()

	if nodeProducesThumbnailResource(ctx, "", streamident.KindArtifactVOD, false, "", "art-1", "tenant-1") {
		t.Fatal("empty node must be denied")
	}
	if nodeProducesThumbnailResource(ctx, "node-1", streamident.KindArtifactVOD, false, "", "", "tenant-1") {
		t.Fatal("empty resource key must be denied")
	}
	if nodeProducesThumbnailResource(ctx, "node-1", streamident.KindArtifactVOD, false, "", "art-1", "") {
		t.Fatal("empty tenant must be denied")
	}

	// Owned (holds a complete copy OR is the assigned processing node), tenant-scoped → authorized.
	mock.ExpectQuery(`SELECT EXISTS.*foghorn.artifact_nodes an.*is_complete = true.*OR EXISTS.*foghorn.processing_jobs`).
		WithArgs("art-1", "node-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	if !nodeProducesThumbnailResource(ctx, "node-1", streamident.KindArtifactVOD, false, "", "art-1", "tenant-1") {
		t.Fatal("a node that holds/processes the artifact must be authorized")
	}

	// Neither holds nor processes → denied.
	mock.ExpectQuery(`SELECT EXISTS.*foghorn.artifact_nodes an.*OR EXISTS.*foghorn.processing_jobs`).
		WithArgs("art-2", "stranger", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	if nodeProducesThumbnailResource(ctx, "stranger", streamident.KindArtifactVOD, false, "", "art-2", "tenant-1") {
		t.Fatal("a node that neither holds nor processes the artifact must be denied")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A node NOT entitled to the artifact tenant is denied: no flip, no backfill, and markArtifactHasThumbnails
// returns false so processThumbnailUploaded skips the Chandler cache invalidation (no side effect on denial).
func TestMarkArtifactHasThumbnails_UnauthorizedReturnsFalseNoWrite(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	// A node dedicated to a DIFFERENT tenant than the artifact.
	state.DefaultManager().SetNodeConnectionInfo(context.Background(), "attacker-node", "attacker.example", "tenant-other", "media-x", nil)

	mock.ExpectQuery(`SELECT tenant_id::text, artifact_type, storage_cluster_id, origin_cluster_id, COALESCE\(has_thumbnails, false\)\s+FROM foghorn\.artifacts`).
		WithArgs("clip-hash-denied").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "artifact_type", "storage_cluster_id", "origin_cluster_id", "has_thumbnails"}).
			AddRow("tenant-1", "clip", nil, nil, false))
	// No flip, no backfill expected.

	if markArtifactHasThumbnails("clip-hash-denied", "attacker-node", logger) {
		t.Fatal("unauthorized confirmation must return false (so the cache bust is skipped)")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A node whose virtual cluster is unresolved (empty) is DENIED fail-closed under the cluster↔tenant entitlement
// model — even when its (server-stamped) node tenant matches the artifact. The authorization SELECT runs, then
// the confirmation is refused: no flip, no backfill, returns false. This is the QM-outage / pre-resolution
// window where authority cannot be proven.
func TestMarkArtifactHasThumbnails_UnresolvedClusterDenied(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	stubClusterEntitlements(t, map[string][]string{"tenant-1": {"media-test-1"}})
	state.DefaultManager().SetNodeConnectionInfo(context.Background(), "node-nocluster", "node-nocluster.example", "tenant-1", "", nil)

	mock.ExpectQuery(`SELECT tenant_id::text, artifact_type, storage_cluster_id, origin_cluster_id, COALESCE\(has_thumbnails, false\)\s+FROM foghorn\.artifacts`).
		WithArgs("clip-hash-3").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "artifact_type", "storage_cluster_id", "origin_cluster_id", "has_thumbnails"}).
			AddRow("tenant-1", "clip", nil, nil, false))
	// No flip/backfill: the node's empty cluster fails the entitlement check.

	if markArtifactHasThumbnails("clip-hash-3", "node-nocluster", logger) {
		t.Fatal("a node with an unresolved cluster must be denied (fail-closed), returning false")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
