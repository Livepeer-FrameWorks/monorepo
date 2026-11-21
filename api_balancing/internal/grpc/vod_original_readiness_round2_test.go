package grpc

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestActivatedOriginalRetentionDoesNotDependOnHeartbeats(t *testing.T) {
	server, mock, done := newStatusServer(t, &fakeVodS3Client{})
	defer done()
	mock.ExpectQuery("IsVodOriginalRetentionActivated").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	if err := server.requireOriginalRetentionReady(context.Background()); err != nil {
		t.Fatalf("durably activated retention must survive stale capability heartbeats: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateVodUploadRetentionDatabaseErrorIsVisible(t *testing.T) {
	server, mock, done := newStatusServer(t, &fakeVodS3Client{})
	defer done()
	mock.ExpectQuery(`FROM foghorn\.artifacts a`).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("IsVodOriginalRetentionActivated").WillReturnError(sql.ErrConnDone)
	name, hash := "vod-test", "hash-1"
	_, err := server.CreateVodUpload(context.Background(), &sharedpb.CreateVodUploadRequest{TenantId: "tenant", Filename: "phone.mov", SizeBytes: 1024, VodHash: &hash, InternalName: &name, ClusterId: "central-primary", KeepOriginal: proto.Bool(true)})
	if status.Code(err) != codes.Unavailable || !strings.Contains(status.Convert(err).Message(), "original retention activation") {
		t.Fatalf("activation read error must be visible without dropping retention: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCommittedUploadReplayIgnoresRetentionActivationOutage(t *testing.T) {
	server, mock, done := newStatusServer(t, &fakeVodS3Client{})
	defer done()
	const tenant = "tenant"
	name, hash, request := "vod-test", "hash-1", "request-1"
	mock.ExpectQuery("GetArtifactCreationCommandStatus").WithArgs(request, tenant).WillReturnRows(sqlmock.NewRows([]string{"status", "catalog_revision", "kind", "artifact_hash"}).AddRow("committed", int64(1), "vod", hash))
	mock.ExpectQuery(`FROM foghorn\.artifacts a`).WithArgs(hash, tenant).WillReturnRows(sqlmock.NewRows([]string{"s3_upload_id", "s3_key", "total_parts", "upload_expires_at", "backend_id", "keep_original"}).AddRow("existing-upload", "source.mov", 1, time.Now().Add(time.Hour), testStubBackendID, false))
	response, err := server.CreateVodUpload(t.Context(), &sharedpb.CreateVodUploadRequest{TenantId: tenant, Filename: "phone.mov", SizeBytes: 1024, VodHash: &hash, InternalName: &name, ClusterId: "central-primary", RequestId: &request, KeepOriginal: proto.Bool(true)})
	if err != nil {
		t.Fatalf("committed replay was blocked by an unrelated retention activation read: %v", err)
	}
	if response.GetUploadId() != "existing-upload" || response.KeepOriginal == nil || response.GetKeepOriginal() {
		t.Fatalf("replay did not preserve the original accepted retention answer: %+v", response)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
