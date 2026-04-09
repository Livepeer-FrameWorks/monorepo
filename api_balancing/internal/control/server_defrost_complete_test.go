package control

import (
	"testing"

	"frameworks/api_balancing/internal/state"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestProcessDefrostComplete_Success(t *testing.T) {
	mock, _, repo := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec("UPDATE foghorn.artifacts.*SET storage_location = 'local'.*defrost_node_id = NULL.*WHERE artifact_hash").
		WithArgs("hash-1", "node-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	processDefrostComplete(&pb.DefrostComplete{
		RequestId: "req-1",
		AssetHash: "hash-1",
		Status:    "success",
		LocalPath: "/data/hash-1.mp4",
		SizeBytes: 2048,
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.addCachedNodePathCalls) != 1 {
		t.Fatalf("expected 1 AddCachedNodeWithPath call, got %d", len(repo.addCachedNodePathCalls))
	}
	call := repo.addCachedNodePathCalls[0]
	if call.Hash != "hash-1" || call.NodeID != "node-1" || call.Path != "/data/hash-1.mp4" || call.Size != 2048 {
		t.Fatalf("unexpected call: %+v", call)
	}
}

func TestProcessDefrostComplete_Success_AddsToInMemoryState(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs("hash-mem", "node-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	processDefrostComplete(&pb.DefrostComplete{
		AssetHash: "hash-mem",
		Status:    "success",
		LocalPath: "/data/hash-mem.mkv",
		SizeBytes: 500,
	}, "node-1", logger)

	sm := state.DefaultManager()
	snap := sm.GetAllNodesSnapshot()
	found := false
	for _, n := range snap.Nodes {
		if n.NodeID == "node-1" {
			for _, a := range n.Artifacts {
				if a.ClipHash == "hash-mem" {
					found = true
					if a.Format != "mkv" {
						t.Fatalf("expected format mkv, got %s", a.Format)
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("artifact not found in in-memory state after defrost")
	}
}

func TestProcessDefrostComplete_Success_UsesReportingNodeID(t *testing.T) {
	mock, _, repo := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs("hash-1", "reporting-node").
		WillReturnResult(sqlmock.NewResult(0, 1))

	processDefrostComplete(&pb.DefrostComplete{
		AssetHash: "hash-1",
		Status:    "success",
		LocalPath: "/data/file.mp4",
		SizeBytes: 100,
		NodeId:    "reporting-node",
	}, "fallback-node", logger)

	repo.mu.Lock()
	if repo.addCachedNodePathCalls[0].NodeID != "reporting-node" {
		t.Fatalf("expected reporting-node, got %s", repo.addCachedNodePathCalls[0].NodeID)
	}
	repo.mu.Unlock()
}

func TestProcessDefrostComplete_Success_ZeroRowsSkipsStateWrite(t *testing.T) {
	mock, _, repo := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs("hash-stale", "node-1").
		WillReturnResult(sqlmock.NewResult(0, 0))

	processDefrostComplete(&pb.DefrostComplete{
		AssetHash: "hash-stale",
		Status:    "success",
		LocalPath: "/data/stale.mp4",
		SizeBytes: 100,
	}, "node-1", logger)

	repo.mu.Lock()
	if len(repo.addCachedNodePathCalls) != 0 {
		t.Fatal("should not call AddCachedNodeWithPath when zero rows updated")
	}
	repo.mu.Unlock()
}

func TestProcessDefrostComplete_Failure_RevertsToS3(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec("UPDATE foghorn.artifacts.*SET storage_location = 's3'.*defrost_node_id = NULL").
		WithArgs("hash-fail", "node-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	processDefrostComplete(&pb.DefrostComplete{
		AssetHash: "hash-fail",
		Status:    "failed",
		Error:     "disk full",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessDefrostComplete_Failure_NilRepo_NoRepoPanic(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	artifactRepo = nil
	logger := logging.NewLogger()

	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs("hash-1", "node-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	processDefrostComplete(&pb.DefrostComplete{
		AssetHash: "hash-1",
		Status:    "success",
		LocalPath: "/data/file.mp4",
		SizeBytes: 100,
	}, "node-1", logger)

	// Should not panic even with nil repo
}
