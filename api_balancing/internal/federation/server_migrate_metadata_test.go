package federation

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	pb "frameworks/pkg/proto"
)

func TestUpsertMigratedArtifactMetadata_InsertsNewRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	artifact := &pb.ArtifactMetadata{ArtifactHash: "hash-1", ArtifactType: "clip", InternalName: "stream-a", Format: "mp4", StorageLocation: "s3", SyncStatus: "synced", S3Url: "s3://bucket/key", SizeBytes: 1024}

	mock.ExpectExec("INSERT INTO foghorn.artifacts").
		WithArgs("hash-1", "clip", "tenant-1", "stream-a", "mp4", "s3", "synced", "s3://bucket/key", uint64(1024), "cluster-origin").
		WillReturnResult(sqlmock.NewResult(1, 1))

	inserted, err := upsertMigratedArtifactMetadata(context.Background(), db, "tenant-1", "cluster-origin", artifact)
	if err != nil {
		t.Fatalf("upsertMigratedArtifactMetadata() err = %v", err)
	}
	if !inserted {
		t.Fatal("expected inserted=true")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestUpsertMigratedArtifactMetadata_BackfillsExistingOrigin(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	artifact := &pb.ArtifactMetadata{ArtifactHash: "hash-2", ArtifactType: "dvr", InternalName: "stream-b", Format: "m3u8", StorageLocation: "s3", SyncStatus: "synced", S3Url: "s3://bucket/dvr", SizeBytes: 2048}

	mock.ExpectExec("INSERT INTO foghorn.artifacts").
		WithArgs("hash-2", "dvr", "tenant-2", "stream-b", "m3u8", "s3", "synced", "s3://bucket/dvr", uint64(2048), "cluster-origin").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs("hash-2", "dvr", "tenant-2", "stream-b", "m3u8", "s3", "synced", "s3://bucket/dvr", uint64(2048), "cluster-origin").
		WillReturnResult(sqlmock.NewResult(0, 1))

	inserted, err := upsertMigratedArtifactMetadata(context.Background(), db, "tenant-2", "cluster-origin", artifact)
	if err != nil {
		t.Fatalf("upsertMigratedArtifactMetadata() err = %v", err)
	}
	if inserted {
		t.Fatal("expected inserted=false for existing row")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
