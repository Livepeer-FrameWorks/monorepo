package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
)

func newTestReconciler(t *testing.T, db *sql.DB, s3 ReconcilerS3Client, commodore ReconcilerCommodoreClient, freeze FreezeRequestSender) *ArtifactReconciler {
	t.Helper()
	return &ArtifactReconciler{
		db:         db,
		s3Client:   s3,
		commodore:  commodore,
		sendFreeze: freeze,
		logger:     logging.NewLogger(),
		interval:   time.Minute,
		batchSize:  50,
		stopCh:     make(chan struct{}),
		triggerCh:  make(chan struct{}, 1),
	}
}

// --- NewArtifactReconciler defaults ---

func TestNewArtifactReconciler_Defaults(t *testing.T) {
	r := NewArtifactReconciler(ArtifactReconcilerConfig{Logger: logging.NewLogger()})
	if r.interval != 5*time.Minute {
		t.Fatalf("expected default interval 5m, got %v", r.interval)
	}
	if r.batchSize != 50 {
		t.Fatalf("expected default batchSize 50, got %d", r.batchSize)
	}
}

func TestNewArtifactReconciler_CustomValues(t *testing.T) {
	r := NewArtifactReconciler(ArtifactReconcilerConfig{
		Logger:    logging.NewLogger(),
		Interval:  10 * time.Second,
		BatchSize: 5,
	})
	if r.interval != 10*time.Second {
		t.Fatalf("expected interval 10s, got %v", r.interval)
	}
	if r.batchSize != 5 {
		t.Fatalf("expected batchSize 5, got %d", r.batchSize)
	}
}

func TestArtifactReconciler_TriggerCoalesces(t *testing.T) {
	r := NewArtifactReconciler(ArtifactReconcilerConfig{Logger: logging.NewLogger()})

	r.Trigger()
	r.Trigger()

	if got := len(r.triggerCh); got != 1 {
		t.Fatalf("expected 1 queued trigger, got %d", got)
	}
}

// --- reconcile guard ---

func TestReconcile_NilS3Client_Noop(t *testing.T) {
	r := &ArtifactReconciler{
		s3Client:   nil,
		sendFreeze: func(string, *pb.FreezeRequest) error { t.Fatal("should not be called"); return nil },
		logger:     logging.NewLogger(),
	}
	r.reconcile() // should not panic
}

func TestReconcile_NilSendFreeze_Noop(t *testing.T) {
	r := &ArtifactReconciler{
		s3Client:   &mockReconcilerS3Client{},
		sendFreeze: nil,
		logger:     logging.NewLogger(),
	}
	r.reconcile() // should not panic
}

// --- retryFailed ---

func TestRetryFailed_QueriesFailedArtifacts(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	s3 := &mockReconcilerS3Client{}
	fc := &freezeCapture{}
	r := newTestReconciler(t, mockDB, s3, nil, fc.send)

	rows := sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "stream_internal_name", "tenant_id", "format", "node_id", "file_path"}).
		AddRow("hash1", "clip", "stream1", "tenant1", "mp4", "node-1", "/data/hash1.mp4")

	mock.ExpectQuery("SELECT.*FROM foghorn.artifacts.*sync_status = 'failed'").
		WithArgs(50).
		WillReturnRows(rows)

	// sendFreezeForArtifact will presign + mark freezing
	mock.ExpectExec("UPDATE foghorn.artifacts.*storage_location = 'freezing'").
		WithArgs("hash1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	count := r.retryFailed(context.Background())
	if count != 1 {
		t.Fatalf("expected 1 retried, got %d", count)
	}
	if fc.count() != 1 {
		t.Fatalf("expected 1 freeze call, got %d", fc.count())
	}
	call := fc.last()
	if call.NodeID != "node-1" {
		t.Fatalf("expected node-1, got %s", call.NodeID)
	}
	if call.Req.AssetHash != "hash1" {
		t.Fatalf("expected hash1, got %s", call.Req.AssetHash)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRetryFailed_QueryError_ReturnsZero(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, nil, func(string, *pb.FreezeRequest) error { return nil })
	mock.ExpectQuery("SELECT.*FROM foghorn.artifacts").WillReturnError(fmt.Errorf("db down"))

	count := r.retryFailed(context.Background())
	if count != 0 {
		t.Fatalf("expected 0 on query error, got %d", count)
	}
}

func TestRetryFailed_RespectsBatchLimit(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, nil, func(string, *pb.FreezeRequest) error { return nil })
	r.batchSize = 3

	mock.ExpectQuery("SELECT.*FROM foghorn.artifacts.*sync_status = 'failed'").
		WithArgs(3).
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "stream_internal_name", "tenant_id", "format", "node_id", "file_path"}))

	r.retryFailed(context.Background())
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// --- advancePending ---

func TestAdvancePending_QueriesPendingLocal(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	fc := &freezeCapture{}
	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, nil, fc.send)

	rows := sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "stream_internal_name", "tenant_id", "format", "node_id", "file_path"}).
		AddRow("hash2", "vod", "stream2", "tenant2", "mp4", "node-2", "/data/hash2.mp4")

	mock.ExpectQuery("SELECT.*FROM foghorn.artifacts.*sync_status = 'pending'.*storage_location = 'local'").
		WithArgs(50).
		WillReturnRows(rows)

	mock.ExpectExec("UPDATE foghorn.artifacts.*storage_location = 'freezing'").
		WithArgs("hash2").
		WillReturnResult(sqlmock.NewResult(0, 1))

	count := r.advancePending(context.Background())
	if count != 1 {
		t.Fatalf("expected 1 advanced, got %d", count)
	}
	if fc.count() != 1 {
		t.Fatal("expected 1 freeze call")
	}
}

func TestAdvancePending_QueryError_ReturnsZero(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, nil, func(string, *pb.FreezeRequest) error { return nil })
	mock.ExpectQuery("SELECT.*FROM foghorn.artifacts").WillReturnError(fmt.Errorf("timeout"))

	count := r.advancePending(context.Background())
	if count != 0 {
		t.Fatalf("expected 0, got %d", count)
	}
}

// --- sendFreezeForArtifact ---

func TestSendFreezeForArtifact_Clip(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	s3 := &mockReconcilerS3Client{}
	fc := &freezeCapture{}
	r := newTestReconciler(t, mockDB, s3, nil, fc.send)

	mock.ExpectExec("UPDATE foghorn.artifacts.*storage_location = 'freezing'").
		WithArgs("clip-hash").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err = r.sendFreezeForArtifact(context.Background(), "clip-hash", "clip", "stream1", "tenant1", "mp4", "node-1", "/data/clip.mp4")
	if err != nil {
		t.Fatal(err)
	}

	if len(s3.clipKeyCalls) != 1 {
		t.Fatalf("expected 1 BuildClipS3Key call, got %d", len(s3.clipKeyCalls))
	}
	if s3.clipKeyCalls[0].Format != "mp4" {
		t.Fatalf("expected format mp4, got %s", s3.clipKeyCalls[0].Format)
	}
	if len(s3.presignedPUTCalls) != 1 {
		t.Fatalf("expected 1 presign call, got %d", len(s3.presignedPUTCalls))
	}

	call := fc.last()
	if call.Req.PresignedPutUrl == "" {
		t.Fatal("expected presigned URL")
	}
	if call.Req.AssetType != "clip" {
		t.Fatalf("expected asset_type=clip, got %s", call.Req.AssetType)
	}
}

func TestSendFreezeForArtifact_ClipDefaultFormat(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	s3 := &mockReconcilerS3Client{}
	fc := &freezeCapture{}
	r := newTestReconciler(t, mockDB, s3, nil, fc.send)

	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs("clip-hash").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err = r.sendFreezeForArtifact(context.Background(), "clip-hash", "clip", "stream1", "tenant1", "", "node-1", "/data/clip")
	if err != nil {
		t.Fatal(err)
	}
	if s3.clipKeyCalls[0].Format != "mp4" {
		t.Fatalf("expected default format mp4, got %s", s3.clipKeyCalls[0].Format)
	}
}

func TestSendFreezeForArtifact_DVR(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	s3 := &mockReconcilerS3Client{}
	fc := &freezeCapture{}
	r := newTestReconciler(t, mockDB, s3, nil, fc.send)

	// DVR marks in_progress first
	mock.ExpectExec("UPDATE foghorn.artifacts.*sync_status = 'in_progress'").
		WithArgs("dvr-hash").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// Then marks freezing
	mock.ExpectExec("UPDATE foghorn.artifacts.*storage_location = 'freezing'").
		WithArgs("dvr-hash").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err = r.sendFreezeForArtifact(context.Background(), "dvr-hash", "dvr", "stream1", "tenant1", "", "node-1", "/data/dvr-hash")
	if err != nil {
		t.Fatal(err)
	}

	if len(s3.presignedPUTCalls) != 0 {
		t.Fatal("DVR should not generate presigned URLs")
	}
	call := fc.last()
	if call.Req.SegmentUrls != nil {
		t.Fatal("DVR request should have nil SegmentUrls")
	}
	if call.Req.AssetType != "dvr" {
		t.Fatalf("expected asset_type=dvr, got %s", call.Req.AssetType)
	}
}

func TestSendFreezeForArtifact_Vod(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	s3 := &mockReconcilerS3Client{}
	fc := &freezeCapture{}
	r := newTestReconciler(t, mockDB, s3, nil, fc.send)

	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs("vod-hash").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err = r.sendFreezeForArtifact(context.Background(), "vod-hash", "vod", "stream1", "tenant1", "mkv", "node-1", "/data/vod.mkv")
	if err != nil {
		t.Fatal(err)
	}

	if len(s3.vodKeyCalls) != 1 {
		t.Fatalf("expected 1 BuildVodS3Key call, got %d", len(s3.vodKeyCalls))
	}
	if !strings.Contains(s3.vodKeyCalls[0].Filename, "vod-hash.mkv") {
		t.Fatalf("expected vod filename with hash.format, got %s", s3.vodKeyCalls[0].Filename)
	}
	call := fc.last()
	if call.Req.PresignedPutUrl == "" {
		t.Fatal("VOD should have presigned URL")
	}
}

func TestSendFreezeForArtifact_VodDefaultFormat(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	s3 := &mockReconcilerS3Client{}
	fc := &freezeCapture{}
	r := newTestReconciler(t, mockDB, s3, nil, fc.send)

	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs("vod-hash").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err = r.sendFreezeForArtifact(context.Background(), "vod-hash", "vod", "stream1", "tenant1", "", "node-1", "/data/vod")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s3.vodKeyCalls[0].Filename, ".mp4") {
		t.Fatalf("expected default mp4 format, got %s", s3.vodKeyCalls[0].Filename)
	}
}

func TestSendFreezeForArtifact_UnsupportedType(t *testing.T) {
	mockDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, nil, func(string, *pb.FreezeRequest) error {
		t.Fatal("should not be called")
		return nil
	})

	err = r.sendFreezeForArtifact(context.Background(), "hash", "unknown_type", "s", "t", "", "n", "/p")
	if err == nil {
		t.Fatal("expected error for unsupported asset type")
	}
	if !strings.Contains(err.Error(), "unsupported asset type") {
		t.Fatalf("expected 'unsupported asset type' error, got: %v", err)
	}
}

func TestSendFreezeForArtifact_PresignFailure(t *testing.T) {
	mockDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	s3 := &mockReconcilerS3Client{
		generatePresignedPUTFn: func(string, time.Duration) (string, error) {
			return "", fmt.Errorf("S3 unavailable")
		},
	}
	r := newTestReconciler(t, mockDB, s3, nil, func(string, *pb.FreezeRequest) error {
		t.Fatal("should not be called after presign failure")
		return nil
	})

	err = r.sendFreezeForArtifact(context.Background(), "hash", "clip", "s", "t", "mp4", "n", "/p")
	if err == nil {
		t.Fatal("expected error on presign failure")
	}
	if !strings.Contains(err.Error(), "presign clip") {
		t.Fatalf("expected presign error, got: %v", err)
	}
}

func TestSendFreezeForArtifact_MarksFreezingBeforeSend(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	fc := &freezeCapture{}
	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, nil, fc.send)

	mock.ExpectExec("UPDATE foghorn.artifacts.*storage_location = 'freezing'.*sync_status = 'in_progress'").
		WithArgs("hash").
		WillReturnResult(sqlmock.NewResult(0, 1))

	_ = r.sendFreezeForArtifact(context.Background(), "hash", "clip", "s", "t", "mp4", "n", "/p")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// --- reconcileOrphaned ---

func TestReconcileOrphaned_NilCommodore_ReturnsZero(t *testing.T) {
	r := &ArtifactReconciler{
		commodore: nil,
		logger:    logging.NewLogger(),
	}
	count := r.reconcileOrphaned(context.Background())
	if count != 0 {
		t.Fatalf("expected 0, got %d", count)
	}
}

func TestReconcileOrphaned_NoArtifactsInState_ReturnsZero(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	mockDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, &mockCommodoreClient{}, func(string, *pb.FreezeRequest) error { return nil })
	count := r.reconcileOrphaned(context.Background())
	if count != 0 {
		t.Fatalf("expected 0 with empty state, got %d", count)
	}
}

func TestReconcileOrphaned_ExistingHashSkipped(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeArtifacts("node-1", []*pb.StoredArtifact{
		{ClipHash: "existing-hash", FilePath: "/data/existing.mp4", SizeBytes: 100, ArtifactType: pb.ArtifactEvent_ARTIFACT_TYPE_CLIP},
	})

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	commodore := &mockCommodoreClient{}
	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, commodore, func(string, *pb.FreezeRequest) error { return nil })

	// Batch check returns this hash as existing
	mock.ExpectQuery("SELECT artifact_hash FROM foghorn.artifacts").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash"}).AddRow("existing-hash"))

	count := r.reconcileOrphaned(context.Background())
	if count != 0 {
		t.Fatalf("expected 0 (hash exists), got %d", count)
	}
	if len(commodore.clipCalls) != 0 {
		t.Fatal("should not call Commodore for existing hash")
	}
}

func TestReconcileOrphaned_CreatesLifecycleRow(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeArtifacts("node-1", []*pb.StoredArtifact{
		{ClipHash: "new-hash", FilePath: "/data/new.mp4", SizeBytes: 200, ArtifactType: pb.ArtifactEvent_ARTIFACT_TYPE_CLIP},
	})

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	commodore := &mockCommodoreClient{
		resolveClipHashFn: func(_ context.Context, hash string) (*pb.ResolveClipHashResponse, error) {
			return &pb.ResolveClipHashResponse{Found: true, TenantId: "tenant-1", StreamInternalName: "stream-1"}, nil
		},
	}
	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, commodore, func(string, *pb.FreezeRequest) error { return nil })

	// Batch check — hash not found
	mock.ExpectQuery("SELECT artifact_hash FROM foghorn.artifacts").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash"}))

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO foghorn.artifacts").
		WithArgs("new-hash", "clip", "stream-1", "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.artifact_nodes").
		WithArgs("new-hash", "node-1", "/data/new.mp4", uint64(200)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	count := r.reconcileOrphaned(context.Background())
	if count != 1 {
		t.Fatalf("expected 1, got %d", count)
	}
	if len(commodore.clipCalls) != 1 {
		t.Fatalf("expected 1 Commodore call, got %d", len(commodore.clipCalls))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileOrphaned_CommodoreFails_Skips(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeArtifacts("node-1", []*pb.StoredArtifact{
		{ClipHash: "unresolvable", FilePath: "/data/unresolvable.mp4", SizeBytes: 50, ArtifactType: pb.ArtifactEvent_ARTIFACT_TYPE_CLIP},
	})

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	commodore := &mockCommodoreClient{
		resolveClipHashFn: func(_ context.Context, _ string) (*pb.ResolveClipHashResponse, error) {
			return nil, fmt.Errorf("commodore unavailable")
		},
	}
	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, commodore, func(string, *pb.FreezeRequest) error { return nil })

	mock.ExpectQuery("SELECT artifact_hash FROM foghorn.artifacts").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash"}))

	count := r.reconcileOrphaned(context.Background())
	if count != 0 {
		t.Fatalf("expected 0, got %d", count)
	}
}

func TestReconcileOrphaned_RespectsBatchSize(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeArtifacts("node-1", []*pb.StoredArtifact{
		{ClipHash: "hash-a", FilePath: "/a.mp4", SizeBytes: 1, ArtifactType: pb.ArtifactEvent_ARTIFACT_TYPE_CLIP},
		{ClipHash: "hash-b", FilePath: "/b.mp4", SizeBytes: 1, ArtifactType: pb.ArtifactEvent_ARTIFACT_TYPE_CLIP},
		{ClipHash: "hash-c", FilePath: "/c.mp4", SizeBytes: 1, ArtifactType: pb.ArtifactEvent_ARTIFACT_TYPE_CLIP},
	})

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	commodore := &mockCommodoreClient{
		resolveClipHashFn: func(_ context.Context, hash string) (*pb.ResolveClipHashResponse, error) {
			return &pb.ResolveClipHashResponse{Found: true, TenantId: "t1", StreamInternalName: "s1"}, nil
		},
	}
	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, commodore, func(string, *pb.FreezeRequest) error { return nil })
	r.batchSize = 1

	mock.ExpectQuery("SELECT artifact_hash FROM foghorn.artifacts").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash"}))

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO foghorn.artifacts").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.artifact_nodes").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	count := r.reconcileOrphaned(context.Background())
	if count != 1 {
		t.Fatalf("expected 1 (batch capped), got %d", count)
	}
}

// --- resolveArtifactContext ---

func TestResolveArtifactContext_Clip(t *testing.T) {
	commodore := &mockCommodoreClient{
		resolveClipHashFn: func(_ context.Context, hash string) (*pb.ResolveClipHashResponse, error) {
			return &pb.ResolveClipHashResponse{Found: true, TenantId: "t-clip", StreamInternalName: "s-clip"}, nil
		},
	}
	r := &ArtifactReconciler{commodore: commodore}
	tenant, stream, err := r.resolveArtifactContext(context.Background(), "hash", "clip")
	if err != nil {
		t.Fatal(err)
	}
	if tenant != "t-clip" || stream != "s-clip" {
		t.Fatalf("got tenant=%s stream=%s", tenant, stream)
	}
}

func TestResolveArtifactContext_DVR(t *testing.T) {
	commodore := &mockCommodoreClient{
		resolveDVRHashFn: func(_ context.Context, hash string) (*pb.ResolveDVRHashResponse, error) {
			return &pb.ResolveDVRHashResponse{Found: true, TenantId: "t-dvr", StreamInternalName: "s-dvr"}, nil
		},
	}
	r := &ArtifactReconciler{commodore: commodore}
	tenant, stream, err := r.resolveArtifactContext(context.Background(), "hash", "dvr")
	if err != nil {
		t.Fatal(err)
	}
	if tenant != "t-dvr" || stream != "s-dvr" {
		t.Fatalf("got tenant=%s stream=%s", tenant, stream)
	}
}

func TestResolveArtifactContext_Vod(t *testing.T) {
	commodore := &mockCommodoreClient{
		resolveVodHashFn: func(_ context.Context, hash string) (*pb.ResolveVodHashResponse, error) {
			return &pb.ResolveVodHashResponse{Found: true, TenantId: "t-vod", InternalName: "s-vod"}, nil
		},
	}
	r := &ArtifactReconciler{commodore: commodore}
	tenant, stream, err := r.resolveArtifactContext(context.Background(), "hash", "vod")
	if err != nil {
		t.Fatal(err)
	}
	if tenant != "t-vod" || stream != "s-vod" {
		t.Fatalf("got tenant=%s stream=%s", tenant, stream)
	}
}

func TestResolveArtifactContext_NotFound(t *testing.T) {
	commodore := &mockCommodoreClient{
		resolveClipHashFn: func(_ context.Context, hash string) (*pb.ResolveClipHashResponse, error) {
			return &pb.ResolveClipHashResponse{Found: false}, nil
		},
	}
	r := &ArtifactReconciler{commodore: commodore}
	_, _, err := r.resolveArtifactContext(context.Background(), "hash", "clip")
	if err == nil {
		t.Fatal("expected error for not-found")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' error, got: %v", err)
	}
}

func TestResolveArtifactContext_UnsupportedType(t *testing.T) {
	r := &ArtifactReconciler{commodore: &mockCommodoreClient{}}
	_, _, err := r.resolveArtifactContext(context.Background(), "hash", "thumbnail")
	if err == nil {
		t.Fatal("expected error for unsupported type")
	}
	if !strings.Contains(err.Error(), "cannot resolve") {
		t.Fatalf("expected 'cannot resolve' error, got: %v", err)
	}
}

// --- inferAssetType ---

func TestInferAssetType(t *testing.T) {
	r := &ArtifactReconciler{}
	tests := []struct {
		path string
		want string
	}{
		{"/data/abc123", "dvr"},
		{"/data/clip.mp4", "clip"},
		{"/data/video.mkv", "clip"},
		{"", "clip"},
	}
	for _, tc := range tests {
		got := r.inferAssetType(tc.path)
		if got != tc.want {
			t.Errorf("inferAssetType(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// --- getExtension ---

func TestGetExtension(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/data/clip.mp4", "mp4"},
		{"/data/video.mkv", "mkv"},
		{"/data/abc123", ""},
		{"/data/dir/hash", ""},
		{"file.ts", "ts"},
		{"", ""},
	}
	for _, tc := range tests {
		got := getExtension(tc.path)
		if got != tc.want {
			t.Errorf("getExtension(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// --- artifactTypeFromProto ---

func TestArtifactTypeFromProto(t *testing.T) {
	tests := []struct {
		input pb.ArtifactEvent_ArtifactType
		want  string
	}{
		{pb.ArtifactEvent_ARTIFACT_TYPE_CLIP, "clip"},
		{pb.ArtifactEvent_ARTIFACT_TYPE_DVR, "dvr"},
		{pb.ArtifactEvent_ARTIFACT_TYPE_VOD, "vod"},
		{pb.ArtifactEvent_ARTIFACT_TYPE_UNSPECIFIED, ""},
		{99, ""},
	}
	for _, tc := range tests {
		got := artifactTypeFromProto(tc.input)
		if got != tc.want {
			t.Errorf("artifactTypeFromProto(%d) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
