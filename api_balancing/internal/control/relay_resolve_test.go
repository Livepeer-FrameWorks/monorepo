package control

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestFillUploadResolveIncludesExpectedSize(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)

	mock.ExpectQuery("SELECT vm\\.s3_key, a\\.size_bytes").
		WithArgs("upload-hash").
		WillReturnRows(sqlmock.NewRows([]string{"s3_key", "size_bytes"}).
			AddRow("uploads/tenant/upload-hash.mov", int64(21708800)))

	req := &ipcpb.RelayResolveRequest{
		AssetKind: "upload",
		AssetHash: "upload-hash",
	}
	resp := &ipcpb.RelayResolveResponse{}

	fillUploadResolve(context.Background(), req, resp, logging.NewLogger())

	if resp.GetState() != ipcpb.AssetState_ASSET_STATE_PLAYABLE {
		t.Fatalf("state = %s, want playable", resp.GetState())
	}
	if got := resp.GetExpectedSizeBytes(); got != 21708800 {
		t.Fatalf("expected_size_bytes = %d, want 21708800", got)
	}
	if resp.GetMediaPresignedUrl() == "" {
		t.Fatal("media presigned URL is empty")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
