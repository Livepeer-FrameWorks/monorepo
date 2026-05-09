package federation

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

type fakeS3Client struct {
	presignedGETResult string
	presignedGETErr    error
}

func (f *fakeS3Client) GeneratePresignedGET(_ string, _ time.Duration) (string, error) {
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

func TestPrepareArtifact_DefrostingState(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster"}).
		AddRow("clip-a", "stream-a", "clip", "mp4", "defrosting", "", 2048, nil)
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

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster"}).
		AddRow("clip-b", "stream-b", "clip", "mp4", "local", "", 4096, nil)
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

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster"}).
		AddRow("clip-c", "stream-c", "clip", "mp4", "s3", "synced", 8192, nil)
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

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster"}).
		AddRow("vod-x", "", "vod", "mp4", "s3", "synced", 65536, nil)
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

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster"}).
		AddRow("vod-y", "", "vod", "mkv", "s3", "synced", 4096, nil)
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

// dvrSegmentRowCols matches the 14 columns ListDVRSegmentsForRange selects.
var dvrSegmentRowCols = []string{
	"artifact_hash", "segment_name", "sequence",
	"media_start_ms", "media_end_ms", "duration_ms",
	"size_bytes", "s3_key", "status", "drop_reason",
	"created_at", "uploaded_at", "deleted_local_at", "dropped_at",
}

func mockDVRLedgerQuery(mock sqlmock.Sqlmock, rows ...[]driver.Value) {
	r := sqlmock.NewRows(dvrSegmentRowCols)
	for _, row := range rows {
		r.AddRow(row...)
	}
	mock.ExpectQuery("FROM foghorn.dvr_segments").WillReturnRows(r)
}

func mockDVRWindowQuery(mock sqlmock.Sqlmock, windowSeconds int64) {
	mock.ExpectQuery("SELECT dvr_window_seconds").
		WillReturnRows(sqlmock.NewRows([]string{"dvr_window_seconds"}).AddRow(windowSeconds))
}

// shareDBWithControlPackage swaps control.db to point at the same sqlmock
// db this test owns. The DVR-on-PrepareArtifact path now calls
// control.ListDVRSegmentsForRange which uses that package-level db, so
// federation tests need to share the connection or the call returns
// sql.ErrConnDone (control's db is nil in test isolation).
func shareDBWithControlPackage(t *testing.T, db *sql.DB) {
	t.Helper()
	prev := control.GetDB()
	control.SetDB(db)
	t.Cleanup(func() { control.SetDB(prev) })
}

func TestPrepareArtifact_DVRSynced_HappyPath(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	shareDBWithControlPackage(t, db)

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster"}).
		AddRow("dvr-b", "stream-b", "dvr", "m3u8", "s3", "synced", 20480, nil)
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)
	mockDVRWindowQuery(mock, 3600)

	// Ledger-driven path now derives segment URLs from foghorn.dvr_segments
	// rather than S3 listing. Two uploaded rows + one lost_local row.
	now := time.Now()
	uploaded := sql.NullTime{Time: now, Valid: true}
	mockDVRLedgerQuery(mock,
		[]driver.Value{"hash-dvr-ok", "chunk000.ts", int64(0), int64(0), int64(6000), int64(6000), int64(1024), "dvr/t/s/h/segments/chunk000.ts", "uploaded", nil, now, uploaded, nil, nil},
		[]driver.Value{"hash-dvr-ok", "chunk001.ts", int64(1), int64(6000), int64(12000), int64(6000), int64(1024), "dvr/t/s/h/segments/chunk001.ts", "uploaded", nil, now, uploaded, nil, nil},
		[]driver.Value{"hash-dvr-ok", "chunk002.ts", int64(2), int64(12000), int64(18000), int64(6000), nil, "dvr/t/s/h/segments/chunk002.ts", "lost_local", "disk_pressure", now, nil, nil, now},
	)

	fake := &fakeS3Client{presignedGETResult: "https://s3.example.com/segment?sig=ok"}

	srv := NewFederationServer(FederationServerConfig{
		Logger:   logging.NewLogger(),
		DB:       db,
		S3Client: fake,
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &pb.PrepareArtifactRequest{
		ArtifactId: "hash-dvr-ok",
		TenantId:   "tenant-a",
		DvrStartMs: 0,
		DvrEndMs:   18000,
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if !resp.GetReady() {
		t.Fatal("expected Ready=true for synced DVR")
	}
	// Two uploaded segments produce presigned URLs; lost_local does not.
	if len(resp.GetSegmentUrls()) != 2 {
		t.Fatalf("expected 2 segment URLs (uploaded only), got %d", len(resp.GetSegmentUrls()))
	}
	// All three segments surface in DvrSegments so the consumer can render
	// #EXT-X-GAP for the lost_local row.
	if len(resp.GetDvrSegments()) != 3 {
		t.Fatalf("expected 3 DvrSegments (incl. lost_local), got %d", len(resp.GetDvrSegments()))
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
	shareDBWithControlPackage(t, db)

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster"}).
		AddRow("dvr-c", "stream-c", "dvr", "m3u8", "s3", "synced", 1024, nil)
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)
	mockDVRWindowQuery(mock, 3600)

	// Ledger query returns an error path. Mock the dvr_segments query as
	// failing so the new ledger-driven minting path returns its error.
	mock.ExpectQuery("FROM foghorn.dvr_segments").WillReturnError(fmt.Errorf("ledger unavailable"))

	fake := &fakeS3Client{}

	srv := NewFederationServer(FederationServerConfig{
		Logger:   logging.NewLogger(),
		DB:       db,
		S3Client: fake,
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &pb.PrepareArtifactRequest{
		ArtifactId: "hash-dvr-err",
		TenantId:   "tenant-a",
		DvrStartMs: 0,
		DvrEndMs:   18000,
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if resp.GetError() != "failed to enumerate DVR segments" {
		t.Fatalf("expected ledger enumeration error, got %q", resp.GetError())
	}
}

func TestPrepareArtifact_DVRRejectsInvalidRange(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster"}).
		AddRow("dvr-range", "stream-range", "dvr", "m3u8", "s3", "synced", 1024, nil)
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	srv := NewFederationServer(FederationServerConfig{
		Logger:   logging.NewLogger(),
		DB:       db,
		S3Client: &fakeS3Client{},
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &pb.PrepareArtifactRequest{
		ArtifactId:   "hash-dvr-range",
		TenantId:     "tenant-a",
		DvrStartMs:   12000,
		DvrEndMs:     6000,
		ArtifactType: "dvr",
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if resp.GetError() != "DVR federation range end must be greater than start" {
		t.Fatalf("expected invalid range error, got %q", resp.GetError())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPrepareArtifact_DVRRejectsRangeBeyondWindow(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster"}).
		AddRow("dvr-wide", "stream-wide", "dvr", "m3u8", "s3", "synced", 1024, nil)
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)
	mockDVRWindowQuery(mock, 60)

	srv := NewFederationServer(FederationServerConfig{
		Logger:   logging.NewLogger(),
		DB:       db,
		S3Client: &fakeS3Client{},
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &pb.PrepareArtifactRequest{
		ArtifactId:   "hash-dvr-wide",
		TenantId:     "tenant-a",
		DvrStartMs:   0,
		DvrEndMs:     120000,
		ArtifactType: "dvr",
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if resp.GetError() != "DVR federation range exceeds maximum chapter window" {
		t.Fatalf("expected range cap error, got %q", resp.GetError())
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

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster"}).
		AddRow("clip-f", "stream-f", "clip", "mp4", "freezing", "", 4096, nil)
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

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster"}).
		AddRow("clip-legacy", "stream-l", "clip", "mp4", "s3", "synced", 2048, nil)
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

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster"}).
		AddRow("clip-s", "stream-s", "clip", "mp4", "local", "syncing", 4096, nil)
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

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster"}).
		AddRow("unknown-a", "stream-u", "thumbnail", "png", "s3", "synced", 256, nil)
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
	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster"}).
		AddRow("clip-d", "stream-d", "clip", "mp4", "s3", "pending", 1024, nil)
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
	shareDBWithControlPackage(t, db)

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster"}).
		AddRow("dvr-a", "stream-a", "dvr", "m3u8", "s3", "synced", 10240, nil)
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)
	mockDVRWindowQuery(mock, 3600)

	// Ledger returns zero rows; new error message reflects ledger semantics.
	mockDVRLedgerQuery(mock)

	fake := &fakeS3Client{}

	srv := NewFederationServer(FederationServerConfig{
		Logger:   logging.NewLogger(),
		DB:       db,
		S3Client: fake,
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &pb.PrepareArtifactRequest{
		ArtifactId: "hash-4",
		TenantId:   "tenant-a",
		DvrStartMs: 0,
		DvrEndMs:   18000,
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if resp.GetError() != "no DVR segments in ledger for requested range" {
		t.Fatalf("expected 'no DVR segments in ledger for requested range', got %q", resp.GetError())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
