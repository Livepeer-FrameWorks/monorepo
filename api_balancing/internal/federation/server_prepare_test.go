package federation

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"frameworks/api_balancing/internal/storage"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
)

type fakeS3Client struct {
	presignedGETResult string
	presignedGETErr    error
	dvrSegmentURLs     map[string]string
	dvrSegmentErr      error
}

func (f *fakeS3Client) GeneratePresignedGET(_ string, _ time.Duration) (string, error) {
	return f.presignedGETResult, f.presignedGETErr
}
func (f *fakeS3Client) GeneratePresignedURLsForDVR(_ string, _ bool, _ time.Duration) (map[string]string, error) {
	return f.dvrSegmentURLs, f.dvrSegmentErr
}
func (f *fakeS3Client) BuildClipS3Key(tenantID, streamName, clipHash, format string) string {
	return fmt.Sprintf("clips/%s/%s/%s.%s", tenantID, streamName, clipHash, format)
}
func (f *fakeS3Client) BuildDVRS3Key(tenantID, internalName, dvrHash string) string {
	return fmt.Sprintf("dvr/%s/%s/%s/", tenantID, internalName, dvrHash)
}
func (f *fakeS3Client) BuildVodS3Key(tenantID, artifactHash, filename string) string {
	return fmt.Sprintf("vod/%s/%s/%s", tenantID, artifactHash, filename)
}

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

func TestPrepareArtifact_ClipSynced_HappyPath(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes"}).
		AddRow("clip-c", "stream-c", "clip", "mp4", "s3", "synced", 8192)
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	fake := &fakeS3Client{presignedGETResult: "https://s3.example.com/clip-c.mp4?X-Amz-Signature=abc"}

	srv := NewFederationServer(FederationServerConfig{
		Logger:   logging.NewLogger(),
		DB:       db,
		S3Client: fake,
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &pb.PrepareArtifactRequest{
		ArtifactId: "hash-3",
		TenantId:   "tenant-a",
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if !resp.GetReady() {
		t.Fatal("expected Ready=true for synced clip")
	}
	if resp.GetUrl() != "https://s3.example.com/clip-c.mp4?X-Amz-Signature=abc" {
		t.Fatalf("unexpected URL: %s", resp.GetUrl())
	}
	if resp.GetSizeBytes() != 8192 {
		t.Fatalf("expected SizeBytes=8192, got %d", resp.GetSizeBytes())
	}
	if resp.GetFormat() != "mp4" {
		t.Fatalf("expected Format=mp4, got %s", resp.GetFormat())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPrepareArtifact_VodSynced_HappyPath(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes"}).
		AddRow("vod-x", "", "vod", "mp4", "s3", "synced", 65536)
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	fake := &fakeS3Client{presignedGETResult: "https://s3.example.com/vod/hash-vod.mp4?sig=xyz"}

	srv := NewFederationServer(FederationServerConfig{
		Logger:   logging.NewLogger(),
		DB:       db,
		S3Client: fake,
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &pb.PrepareArtifactRequest{
		ArtifactId: "hash-vod",
		TenantId:   "tenant-a",
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if !resp.GetReady() {
		t.Fatal("expected Ready=true for synced VOD")
	}
	if resp.GetUrl() != "https://s3.example.com/vod/hash-vod.mp4?sig=xyz" {
		t.Fatalf("unexpected URL: %s", resp.GetUrl())
	}
	if resp.GetSizeBytes() != 65536 {
		t.Fatalf("expected 65536, got %d", resp.GetSizeBytes())
	}
	if resp.GetFormat() != "mp4" {
		t.Fatalf("expected mp4, got %q", resp.GetFormat())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPrepareArtifact_VodSynced_PresignError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes"}).
		AddRow("vod-y", "", "vod", "mkv", "s3", "synced", 4096)
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	fake := &fakeS3Client{presignedGETErr: fmt.Errorf("S3 unavailable")}

	srv := NewFederationServer(FederationServerConfig{
		Logger:   logging.NewLogger(),
		DB:       db,
		S3Client: fake,
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &pb.PrepareArtifactRequest{
		ArtifactId: "hash-vod-err",
		TenantId:   "tenant-a",
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if resp.GetError() != "failed to generate download URL" {
		t.Fatalf("expected download URL error, got %q", resp.GetError())
	}
}

func TestPrepareArtifact_DVRSynced_HappyPath(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes"}).
		AddRow("dvr-b", "stream-b", "dvr", "m3u8", "s3", "synced", 20480)
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	fake := &fakeS3Client{
		dvrSegmentURLs: map[string]string{
			"manifest.m3u8": "https://s3.example.com/manifest?sig=a",
			"chunk000.ts":   "https://s3.example.com/chunk000?sig=b",
			"chunk001.ts":   "https://s3.example.com/chunk001?sig=c",
		},
	}

	srv := NewFederationServer(FederationServerConfig{
		Logger:   logging.NewLogger(),
		DB:       db,
		S3Client: fake,
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &pb.PrepareArtifactRequest{
		ArtifactId: "hash-dvr-ok",
		TenantId:   "tenant-a",
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if !resp.GetReady() {
		t.Fatal("expected Ready=true for synced DVR")
	}
	if len(resp.GetSegmentUrls()) != 3 {
		t.Fatalf("expected 3 segment URLs, got %d", len(resp.GetSegmentUrls()))
	}
	if resp.GetSizeBytes() != 20480 {
		t.Fatalf("expected 20480, got %d", resp.GetSizeBytes())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPrepareArtifact_DVRSynced_PresignError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes"}).
		AddRow("dvr-c", "stream-c", "dvr", "m3u8", "s3", "synced", 1024)
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	fake := &fakeS3Client{dvrSegmentErr: fmt.Errorf("S3 list failed")}

	srv := NewFederationServer(FederationServerConfig{
		Logger:   logging.NewLogger(),
		DB:       db,
		S3Client: fake,
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &pb.PrepareArtifactRequest{
		ArtifactId: "hash-dvr-err",
		TenantId:   "tenant-a",
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if resp.GetError() != "failed to generate DVR segment URLs" {
		t.Fatalf("expected DVR URL error, got %q", resp.GetError())
	}
}

func TestPrepareArtifact_FreezingState(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes"}).
		AddRow("clip-f", "stream-f", "clip", "mp4", "freezing", "", 4096)
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	srv := NewFederationServer(FederationServerConfig{
		Logger:   logging.NewLogger(),
		DB:       db,
		S3Client: &fakeS3Client{},
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &pb.PrepareArtifactRequest{
		ArtifactId: "hash-freezing",
		TenantId:   "tenant-a",
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if resp.GetReady() {
		t.Fatal("expected Ready=false for freezing artifact")
	}
	if resp.GetEstReadySeconds() != 30 {
		t.Fatalf("expected 30 seconds, got %d", resp.GetEstReadySeconds())
	}
}

func TestPrepareArtifact_LegacyClipHash(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes"}).
		AddRow("clip-legacy", "stream-l", "clip", "mp4", "s3", "synced", 2048)
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	fake := &fakeS3Client{presignedGETResult: "https://s3.example.com/legacy.mp4?sig=legacy"}

	srv := NewFederationServer(FederationServerConfig{
		Logger:   logging.NewLogger(),
		DB:       db,
		S3Client: fake,
	})

	// Use ClipHash instead of ArtifactId
	resp, err := srv.PrepareArtifact(serviceAuthContext(), &pb.PrepareArtifactRequest{
		ClipHash: "hash-legacy",
		TenantId: "tenant-a",
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if !resp.GetReady() {
		t.Fatal("expected Ready=true for legacy clip hash")
	}
}

func TestPrepareArtifact_DBError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnError(fmt.Errorf("connection refused"))

	srv := NewFederationServer(FederationServerConfig{
		Logger:   logging.NewLogger(),
		DB:       db,
		S3Client: &fakeS3Client{},
	})

	_, err = srv.PrepareArtifact(serviceAuthContext(), &pb.PrepareArtifactRequest{
		ArtifactId: "hash-db-err",
		TenantId:   "tenant-a",
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal error, got %v", err)
	}
}

func TestPrepareArtifact_SyncingState(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes"}).
		AddRow("clip-s", "stream-s", "clip", "mp4", "local", "syncing", 4096)
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	srv := NewFederationServer(FederationServerConfig{
		Logger:   logging.NewLogger(),
		DB:       db,
		S3Client: &fakeS3Client{},
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &pb.PrepareArtifactRequest{
		ArtifactId: "hash-syncing",
		TenantId:   "tenant-a",
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if resp.GetReady() {
		t.Fatal("expected Ready=false for syncing artifact")
	}
	if resp.GetEstReadySeconds() != 30 {
		t.Fatalf("expected 30 seconds, got %d", resp.GetEstReadySeconds())
	}
}

func TestPrepareArtifact_UnknownArtifactType(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes"}).
		AddRow("unknown-a", "stream-u", "thumbnail", "png", "s3", "synced", 256)
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	srv := NewFederationServer(FederationServerConfig{
		Logger:   logging.NewLogger(),
		DB:       db,
		S3Client: &fakeS3Client{},
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &pb.PrepareArtifactRequest{
		ArtifactId: "hash-unknown",
		TenantId:   "tenant-a",
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if resp.GetError() == "" {
		t.Fatal("expected error for unknown artifact type")
	}
}

func TestPrepareArtifact_MetadataDrift(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// storage_location=s3 but sync_status NOT "synced" — metadata drift
	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes"}).
		AddRow("clip-d", "stream-d", "clip", "mp4", "s3", "pending", 1024)
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	srv := NewFederationServer(FederationServerConfig{
		Logger:   logging.NewLogger(),
		DB:       db,
		S3Client: &fakeS3Client{},
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &pb.PrepareArtifactRequest{
		ArtifactId: "hash-drift",
		TenantId:   "tenant-a",
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if resp.GetError() == "" {
		t.Fatal("expected error for metadata drift")
	}
}

func TestPrepareArtifact_NilDBAndS3(t *testing.T) {
	srv := NewFederationServer(FederationServerConfig{
		Logger: logging.NewLogger(),
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &pb.PrepareArtifactRequest{
		ArtifactId: "hash-no-storage",
		TenantId:   "tenant-a",
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if resp.GetError() != "origin storage not configured" {
		t.Fatalf("expected 'origin storage not configured', got %q", resp.GetError())
	}
}

func TestPrepareArtifact_EmptyDVRSegments(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes"}).
		AddRow("dvr-a", "stream-a", "dvr", "m3u8", "s3", "synced", 10240)
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	fake := &fakeS3Client{dvrSegmentURLs: map[string]string{}} // empty

	srv := NewFederationServer(FederationServerConfig{
		Logger:   logging.NewLogger(),
		DB:       db,
		S3Client: fake,
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &pb.PrepareArtifactRequest{
		ArtifactId: "hash-4",
		TenantId:   "tenant-a",
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if resp.GetError() != "no DVR segments found in S3" {
		t.Fatalf("expected 'no DVR segments found in S3', got %q", resp.GetError())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
