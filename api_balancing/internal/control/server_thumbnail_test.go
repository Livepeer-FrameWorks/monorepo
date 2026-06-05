package control

import (
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestProcessThumbnailUploadedMarksGeneratedArtifactHash(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	prevDB := db
	prevCommodore := CommodoreClient
	db = mockDB
	CommodoreClient = nil
	t.Cleanup(func() {
		db = prevDB
		CommodoreClient = prevCommodore
		mockDB.Close()
	})

	artifactHash := "20260519072335d237b08dd1220e3e"
	mock.ExpectQuery(`UPDATE foghorn\.artifacts\s+SET has_thumbnails = true`).
		WithArgs(artifactHash).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "artifact_type", "storage_cluster_id", "origin_cluster_id"}).
			AddRow("5eed517e-ba5e-da7a-517e-ba5eda7a0001", "clip", sql.NullString{String: "demo-media", Valid: true}, sql.NullString{}))

	processThumbnailUploaded(&ipcpb.ThumbnailUploaded{
		ThumbnailKey: artifactHash,
		S3Keys: []string{
			"thumbnails/" + artifactHash + "/poster.jpg",
			"thumbnails/" + artifactHash + "/sprite.jpg",
			"thumbnails/" + artifactHash + "/sprite.vtt",
		},
	}, "edge-node-1", logging.NewLoggerWithService("test"))

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
