package grpc

import (
	"context"
	"database/sql"
	"strings"
	"testing"

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
