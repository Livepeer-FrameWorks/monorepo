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
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
)

type fakeS3Client struct {
	presignedGETResult string
	presignedGETErr    error
	lastPresignGETKey  string // the exact key the read path passed — pins the recorded-key consumer invariant
}

func (f *fakeS3Client) GeneratePresignedGET(key string, _ time.Duration) (string, error) {
	f.lastPresignGETKey = key
	return f.presignedGETResult, f.presignedGETErr
}
func (f *fakeS3Client) GeneratePresignedPUT(_ string, _ time.Duration) (string, error) {
	return "", nil
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
func (f *fakeS3Client) Delete(_ context.Context, _ string) error {
	return nil
}
func (f *fakeS3Client) DeletePrefix(_ context.Context, _ string) (int, error) {
	return 0, nil
}

func TestPrepareArtifact_LocalState_NotReady(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster", "recorded_object_key"}).
		AddRow("clip-b", "stream-b", "clip", "mp4", "local", "", 4096, nil, "")
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	srv := NewFederationServer(FederationServerConfig{
		AllowFederationMutations: true,
		Logger:                   logging.NewLogger(),
		DB:                       db,
		S3Client:                 &storage.S3Client{},
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &foghornfederationpb.PrepareArtifactRequest{
		ArtifactId: "hash-2",
		TenantId:   "tenant-a",
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if resp.GetReady() {
		t.Fatalf("expected Ready=false for local artifact, got true")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPrepareArtifact_RequiresAuth(t *testing.T) {
	srv := NewFederationServer(FederationServerConfig{
		AllowFederationMutations: true,
		Logger:                   logging.NewLogger(),
		S3Client:                 &storage.S3Client{},
	})

	_, err := srv.PrepareArtifact(context.Background(), &foghornfederationpb.PrepareArtifactRequest{
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
		AllowFederationMutations: true,
		Logger:                   logging.NewLogger(),
		DB:                       db,
		S3Client:                 &storage.S3Client{},
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &foghornfederationpb.PrepareArtifactRequest{
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
		AllowFederationMutations: true,
		Logger:                   logging.NewLogger(),
		S3Client:                 &storage.S3Client{},
	})

	_, err := srv.PrepareArtifact(serviceAuthContext(), &foghornfederationpb.PrepareArtifactRequest{
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
		AllowFederationMutations: true,
		Logger:                   logging.NewLogger(),
		S3Client:                 &storage.S3Client{},
	})

	_, err := srv.PrepareArtifact(serviceAuthContext(), &foghornfederationpb.PrepareArtifactRequest{
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

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster", "recorded_object_key"}).
		AddRow("clip-c", "stream-c", "clip", "mp4", "s3", "synced", 8192, nil, "clips/tenant-a/stream-c/clip-c.mp4")
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	fake := &fakeS3Client{presignedGETResult: "https://s3.example.com/clip-c.mp4?X-Amz-Signature=abc"}

	srv := NewFederationServer(FederationServerConfig{
		AllowFederationMutations: true,
		Logger:                   logging.NewLogger(),
		DB:                       db,
		S3Client:                 fake,
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &foghornfederationpb.PrepareArtifactRequest{
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
	// The read path presigns the EXACT recorded object key, not a reconstruction.
	if fake.lastPresignGETKey != "clips/tenant-a/stream-c/clip-c.mp4" {
		t.Fatalf("expected presign against the recorded key, got %q", fake.lastPresignGETKey)
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

// A synced clip/VOD MUST carry a recorded object key (vod_metadata.s3_key / sync_object_key). The read
// path consumes that exact object rather than reconstructing one, so a synced row with no recorded key is
// inconsistent and PrepareArtifact fails closed instead of presigning a guessed object.
func TestPrepareArtifact_SyncedWithoutRecordedKeyFailsClosed(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster", "recorded_object_key"}).
		AddRow("clip-nokey", "stream-nokey", "clip", "mp4", "s3", "synced", 8192, nil, "") // no recorded key
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	fake := &fakeS3Client{presignedGETResult: "https://s3.example.com/should-not-be-used"}
	srv := NewFederationServer(FederationServerConfig{Logger: logging.NewLogger(), DB: db, S3Client: fake, AllowFederationMutations: true})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &foghornfederationpb.PrepareArtifactRequest{
		ArtifactId: "hash-nokey",
		TenantId:   "tenant-a",
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if resp.GetReady() || resp.GetUrl() != "" {
		t.Fatalf("expected fail-closed (not ready, no URL), got ready=%v url=%q", resp.GetReady(), resp.GetUrl())
	}
	if resp.GetError() == "" {
		t.Fatal("expected a structured error for the missing recorded key")
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

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster", "recorded_object_key"}).
		AddRow("vod-x", "", "vod", "mp4", "s3", "synced", 65536, nil, "vod/tenant-a/vod-x/vod-x.mp4")
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	fake := &fakeS3Client{presignedGETResult: "https://s3.example.com/vod/hash-vod.mp4?sig=xyz"}

	srv := NewFederationServer(FederationServerConfig{
		AllowFederationMutations: true,
		Logger:                   logging.NewLogger(),
		DB:                       db,
		S3Client:                 fake,
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &foghornfederationpb.PrepareArtifactRequest{
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
	if fake.lastPresignGETKey != "vod/tenant-a/vod-x/vod-x.mp4" {
		t.Fatalf("expected presign against the recorded key, got %q", fake.lastPresignGETKey)
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

func TestPrepareArtifact_ChapterRequestUsesStoredVODBytes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster", "recorded_object_key"}).
		AddRow("chapter-hash", "source-stream", "vod", "mkv", "s3", "synced", 32768, nil, "vod/tenant-a/chapter-hash/chapter.mkv")
	mock.ExpectQuery("FROM foghorn.artifacts").WithArgs("chapter-hash", "tenant-a").WillReturnRows(rows)

	fake := &fakeS3Client{presignedGETResult: "https://s3.example.com/chapter.mkv?sig=chapter"}
	srv := NewFederationServer(FederationServerConfig{
		AllowFederationMutations: true,
		Logger:                   logging.NewLogger(),
		DB:                       db,
		S3Client:                 fake,
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &foghornfederationpb.PrepareArtifactRequest{
		ArtifactId:   "chapter-hash",
		TenantId:     "tenant-a",
		ArtifactType: "chapter",
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if !resp.GetReady() || resp.GetUrl() == "" {
		t.Fatalf("chapter request must resolve the stored VOD object, got %+v", resp)
	}
	if fake.lastPresignGETKey != "vod/tenant-a/chapter-hash/chapter.mkv" {
		t.Fatalf("presigned key = %q, want recorded chapter VOD key", fake.lastPresignGETKey)
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

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster", "recorded_object_key"}).
		AddRow("vod-y", "", "vod", "mkv", "s3", "synced", 4096, nil, "vod/tenant-a/vod-y/vod-y.mkv")
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	fake := &fakeS3Client{presignedGETErr: fmt.Errorf("S3 unavailable")}

	srv := NewFederationServer(FederationServerConfig{
		AllowFederationMutations: true,
		Logger:                   logging.NewLogger(),
		DB:                       db,
		S3Client:                 fake,
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &foghornfederationpb.PrepareArtifactRequest{
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

func TestPrepareArtifact_DVRRejected(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster", "recorded_object_key"}).
		AddRow("dvr-b", "stream-b", "dvr", "m3u8", "s3", "synced", 20480, nil, "")
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	srv := NewFederationServer(FederationServerConfig{
		AllowFederationMutations: true,
		Logger:                   logging.NewLogger(),
		DB:                       db,
		S3Client:                 &fakeS3Client{},
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &foghornfederationpb.PrepareArtifactRequest{
		ArtifactId: "hash-dvr-ok",
		TenantId:   "tenant-a",
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if resp.GetError() != "DVR playback is per-chapter; query dvrChapters and PrepareArtifact each chapter's VOD artifact_hash" {
		t.Fatalf("expected DVR rejection, got %q", resp.GetError())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPrepareArtifact_FreezingState(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster", "recorded_object_key"}).
		AddRow("clip-f", "stream-f", "clip", "mp4", "freezing", "", 4096, nil, "")
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	srv := NewFederationServer(FederationServerConfig{
		AllowFederationMutations: true,
		Logger:                   logging.NewLogger(),
		DB:                       db,
		S3Client:                 &fakeS3Client{},
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &foghornfederationpb.PrepareArtifactRequest{
		ArtifactId: "hash-freezing",
		TenantId:   "tenant-a",
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if resp.GetReady() {
		t.Fatal("expected Ready=false for freezing artifact")
	}
}

func TestPrepareArtifact_ClipHashFallback(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster", "recorded_object_key"}).
		AddRow("clip-fallback", "stream-l", "clip", "mp4", "s3", "synced", 2048, nil, "clips/tenant-a/stream-l/clip-fallback.mp4")
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	fake := &fakeS3Client{presignedGETResult: "https://s3.example.com/fallback.mp4?sig=fallback"}

	srv := NewFederationServer(FederationServerConfig{
		AllowFederationMutations: true,
		Logger:                   logging.NewLogger(),
		DB:                       db,
		S3Client:                 fake,
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &foghornfederationpb.PrepareArtifactRequest{
		ClipHash: "hash-fallback",
		TenantId: "tenant-a",
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if !resp.GetReady() {
		t.Fatal("expected Ready=true for clip hash fallback")
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
		AllowFederationMutations: true,
		Logger:                   logging.NewLogger(),
		DB:                       db,
		S3Client:                 &fakeS3Client{},
	})

	_, err = srv.PrepareArtifact(serviceAuthContext(), &foghornfederationpb.PrepareArtifactRequest{
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

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster", "recorded_object_key"}).
		AddRow("clip-s", "stream-s", "clip", "mp4", "local", "syncing", 4096, nil, "")
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	srv := NewFederationServer(FederationServerConfig{
		AllowFederationMutations: true,
		Logger:                   logging.NewLogger(),
		DB:                       db,
		S3Client:                 &fakeS3Client{},
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &foghornfederationpb.PrepareArtifactRequest{
		ArtifactId: "hash-syncing",
		TenantId:   "tenant-a",
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if resp.GetReady() {
		t.Fatal("expected Ready=false for syncing artifact")
	}
}

func TestPrepareArtifact_UnknownArtifactType(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster", "recorded_object_key"}).
		AddRow("unknown-a", "stream-u", "thumbnail", "png", "s3", "synced", 256, nil, "")
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	srv := NewFederationServer(FederationServerConfig{
		AllowFederationMutations: true,
		Logger:                   logging.NewLogger(),
		DB:                       db,
		S3Client:                 &fakeS3Client{},
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &foghornfederationpb.PrepareArtifactRequest{
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
	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster", "recorded_object_key"}).
		AddRow("clip-d", "stream-d", "clip", "mp4", "s3", "pending", 1024, nil, "")
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	srv := NewFederationServer(FederationServerConfig{
		AllowFederationMutations: true,
		Logger:                   logging.NewLogger(),
		DB:                       db,
		S3Client:                 &fakeS3Client{},
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &foghornfederationpb.PrepareArtifactRequest{
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
		AllowFederationMutations: true,
		Logger:                   logging.NewLogger(),
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &foghornfederationpb.PrepareArtifactRequest{
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
