package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestRecordingCaptureTimesRepairSameRevisionAfterLegacyConsumer(t *testing.T) {
	server, mock, done := newMockServer(t)
	defer done()
	started := time.Unix(1800000000, 0).UTC()
	ended := started.Add(time.Hour)
	mock.ExpectBegin()
	mock.ExpectExec("pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("GetArtifactCatalogTombstoneForUpdate").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("ApplyDVRCatalogSnapshot").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("GetDVRCatalogState").WillReturnRows(sqlmock.NewRows([]string{"origin_cluster_id", "catalog_revision", "thumbnail_serving_cluster_id"}).AddRow("origin", int64(7), nil))
	mock.ExpectExec("UpdateDVRRecordingCaptureTimes").WithArgs(started, ended, "tenant", "recording", int64(7), "origin").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("GetDVRRecordingCaptureTimes").WithArgs("tenant", "recording").WillReturnRows(sqlmock.NewRows([]string{"started_at", "ended_at"}).AddRow(started, ended))
	mock.ExpectCommit()
	response, err := server.UpdateArtifactCatalogSnapshot(context.Background(), &pb.UpdateArtifactCatalogSnapshotRequest{TenantId: "tenant", AssetType: pb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_DVR, AssetKey: "recording", SourceClusterId: proto.String("origin"), SourceRevision: 7, RecordingTimesPresent: true, StartedAt: timestamppb.New(started), EndedAt: timestamppb.New(ended)})
	if err != nil || !response.GetRecordingTimesPresent() || !response.GetStartedAt().AsTime().Equal(started) || !response.GetEndedAt().AsTime().Equal(ended) {
		t.Fatalf("same-revision recording projection lost capture times: %v %v", response, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
