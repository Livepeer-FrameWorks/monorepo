package control

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// A freeze upload can land just before its clip is deleted, so the node's success report reaches Foghorn after the
// deletion committed. The attempt row still carries the attempt identity, but the artifact is deleted: the completion
// must not verify or copy anything in object storage for it.
func TestProcessSyncComplete_DeletedArtifactPublishesNothing(t *testing.T) {
	mock, s3Mock, _ := setupArtifactTestDeps(t)
	mock.MatchExpectationsInOrder(false)
	var heads int
	s3Mock.headObjectInfoFn = func(context.Context, string) (bool, int64, string, error) {
		heads++
		return true, 806686, "etag-main", nil
	}

	const hash = "202610010245238df6112ab7d37d6f"
	obj := "tenant-1/clips/stream-a/" + hash + ".mkv"
	mock.ExpectQuery("SELECT status\\s+FROM foghorn.artifacts").WithArgs(hash, "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("deleted"))
	expectMetadata(mock, hash, "req-1", "node-1", "clip", "stream-a", "mkv", "tenant-1", "", "", "in_progress", obj)

	processSyncComplete(&ipcpb.SyncComplete{AssetHash: hash, Status: "success", RequestId: "req-1"}, "node-1", logging.NewLogger())

	s3Mock.mu.Lock()
	defer s3Mock.mu.Unlock()
	if heads != 0 || len(s3Mock.promoteCalls) != 0 {
		t.Fatalf("completion for a deleted clip touched object storage: %d HEADs, promotions %v", heads, s3Mock.promoteCalls)
	}
}
