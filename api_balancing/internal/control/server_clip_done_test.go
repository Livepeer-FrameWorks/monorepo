package control

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
)

// handleClipDone with status=success must stamp the writing node as
// origin for the clip's artifact_hash so cross-cluster peer-relay can
// serve the file before it syncs to S3. Mirrors the VOD processing
// finalize path at server.go's processProcessingJobResult.
func TestHandleClipDone_SuccessRegistersOrigin(t *testing.T) {
	mock, _, repo := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	// projectArtifactSizeByRequestID's lookup (resolves request_id → artifact_hash).
	// projectArtifactSizeToCommodore bails early because CommodoreClient
	// is nil in tests, so no follow-up query fires from that path.
	mock.ExpectQuery("SELECT artifact_hash.*FROM foghorn.artifacts.*WHERE request_id").
		WithArgs("req-clip-1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash"}).AddRow("clip-hash-1"))
	// registerClipOriginByRequestID's lookup
	mock.ExpectQuery("SELECT artifact_hash.*FROM foghorn.artifacts.*WHERE request_id").
		WithArgs("req-clip-1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash"}).AddRow("clip-hash-1"))

	handleClipDone(&pb.ClipDone{
		RequestId: "req-clip-1",
		FilePath:  "/data/clips/clip.mp4",
		SizeBytes: 2048,
		Status:    "success",
	}, "edge-node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.originArtifactCalls) != 1 {
		t.Fatalf("expected 1 RegisterOriginArtifact, got %d", len(repo.originArtifactCalls))
	}
	c := repo.originArtifactCalls[0]
	if c.Hash != "clip-hash-1" || c.NodeID != "edge-node-1" || c.Path != "/data/clips/clip.mp4" || c.Size != 2048 || !c.Complete {
		t.Fatalf("unexpected origin registration: %+v", c)
	}
}

// Failed clips must NOT stamp an origin row — the file isn't there.
func TestHandleClipDone_FailedSkipsOriginRegistration(t *testing.T) {
	_, _, repo := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	handleClipDone(&pb.ClipDone{
		RequestId: "req-clip-2",
		Status:    "failed",
		Error:     "encoder died",
	}, "edge-node-1", logger)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.originArtifactCalls) != 0 {
		t.Fatalf("expected 0 RegisterOriginArtifact on failed clip, got %d", len(repo.originArtifactCalls))
	}
}
