package grpc

import (
	"context"
	"testing"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
)

func TestUpsertDVRChapterAliasDefaultsMode(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := &CommodoreServer{db: db, logger: logrus.New()}
	mock.ExpectExec("INSERT INTO commodore\\.dvr_chapter_aliases").
		WithArgs("chapter-1", "00000000-0000-0000-0000-000000000001", "dvrhash1234567890dvrhash12345678", "explicit_range", int32(0), int64(1000), int64(2000)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err = server.upsertDVRChapterAlias(context.Background(), "chapter-1", "dvrhash1234567890dvrhash12345678", "00000000-0000-0000-0000-000000000001", &pb.RetrieveDVRChapterRequest{
		StartMs: 1000,
		EndMs:   2000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
