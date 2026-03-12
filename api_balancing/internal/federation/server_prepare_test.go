package federation

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"frameworks/api_balancing/internal/storage"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
)

func TestPrepareArtifact_DefrostingState(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes"}).
		AddRow("clip-a", "stream-a", "clip", "mp4", "defrosting", "", 2048)
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	srv := NewFederationServer(FederationServerConfig{
		Logger:   logging.NewLogger(),
		DB:       db,
		S3Client: &storage.S3Client{},
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &pb.PrepareArtifactRequest{
		ArtifactId: "hash-1",
		TenantId:   "tenant-a",
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if resp.GetReady() {
		t.Fatalf("expected Ready=false for defrosting artifact, got true")
	}
	if resp.GetEstReadySeconds() != 15 {
		t.Fatalf("expected EstReadySeconds=15, got %d", resp.GetEstReadySeconds())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPrepareArtifact_LocalState_TriggersFreeze(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes"}).
		AddRow("clip-b", "stream-b", "clip", "mp4", "local", "", 4096)
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	srv := NewFederationServer(FederationServerConfig{
		Logger:   logging.NewLogger(),
		DB:       db,
		S3Client: &storage.S3Client{},
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &pb.PrepareArtifactRequest{
		ArtifactId: "hash-2",
		TenantId:   "tenant-a",
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if resp.GetReady() {
		t.Fatalf("expected Ready=false for local artifact, got true")
	}
	if resp.GetEstReadySeconds() != 30 {
		t.Fatalf("expected EstReadySeconds=30, got %d", resp.GetEstReadySeconds())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPrepareArtifact_RequiresAuth(t *testing.T) {
	srv := NewFederationServer(FederationServerConfig{
		Logger:   logging.NewLogger(),
		S3Client: &storage.S3Client{},
	})

	_, err := srv.PrepareArtifact(context.Background(), &pb.PrepareArtifactRequest{
		ArtifactId: "hash-1",
		TenantId:   "tenant-a",
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestPrepareArtifact_ArtifactNotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("FROM foghorn.artifacts").WithArgs("hash-1", "tenant-a").WillReturnError(sql.ErrNoRows)

	srv := NewFederationServer(FederationServerConfig{
		Logger:   logging.NewLogger(),
		DB:       db,
		S3Client: &storage.S3Client{},
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &pb.PrepareArtifactRequest{
		ArtifactId: "hash-1",
		TenantId:   "tenant-a",
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if resp.GetError() != "artifact not found" {
		t.Fatalf("expected error %q, got %q", "artifact not found", resp.GetError())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPrepareArtifact_MissingArtifactID(t *testing.T) {
	srv := NewFederationServer(FederationServerConfig{
		Logger:   logging.NewLogger(),
		S3Client: &storage.S3Client{},
	})

	_, err := srv.PrepareArtifact(serviceAuthContext(), &pb.PrepareArtifactRequest{
		ArtifactId: "",
		ClipHash:   "",
		TenantId:   "tenant-a",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestPrepareArtifact_MissingTenantID(t *testing.T) {
	srv := NewFederationServer(FederationServerConfig{
		Logger:   logging.NewLogger(),
		S3Client: &storage.S3Client{},
	})

	_, err := srv.PrepareArtifact(serviceAuthContext(), &pb.PrepareArtifactRequest{
		ArtifactId: "hash-1",
		TenantId:   "",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}
