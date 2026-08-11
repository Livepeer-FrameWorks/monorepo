package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
)

func newTestReconciler(t *testing.T, db *sql.DB, s3 ReconcilerS3Client, commodore ReconcilerCommodoreClient, freeze FreezeRequestSender) *ArtifactReconciler {
	t.Helper()
	return &ArtifactReconciler{
		db:         db,
		s3Client:   s3,
		commodore:  commodore,
		sendFreeze: freeze,
		// Default: a successful assignment (server-minted attempt + staging URL). Tests that exercise
		// denial / not-eligible override this.
		prepareFreeze: func(_ context.Context, _, hash, _, _, _, _, _ string, _ time.Duration) (control.FreezeAssignment, string, bool) {
			return control.FreezeAssignment{AttemptID: "att-" + hash, StagingURL: "https://s3/staging/" + hash, CanonicalKey: "k/" + hash, DestCluster: "official"}, "", true
		},
		logger:    logging.NewLogger(),
		interval:  time.Minute,
		batchSize: 50,
		stopCh:    make(chan struct{}),
		triggerCh: make(chan struct{}, 1),
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
		sendFreeze: func(string, *ipcpb.FreezeRequest) error { t.Fatal("should not be called"); return nil },
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

func TestRetryFailed_RetriesSchemaVersionMismatch(t *testing.T) {
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
		WillReturnError(&pq.Error{Code: "40001", Message: "schema version mismatch for table x: expected 73, got 72"})
	mock.ExpectQuery("SELECT.*FROM foghorn.artifacts.*sync_status = 'failed'").
		WithArgs(50).
		WillReturnRows(rows)

	count := r.retryFailed(context.Background())
	if count != 1 {
		t.Fatalf("expected 1 retried after schema retry, got %d", count)
	}
	if fc.count() != 1 {
		t.Fatalf("expected 1 freeze call, got %d", fc.count())
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

	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, nil, func(string, *ipcpb.FreezeRequest) error { return nil })
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

	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, nil, func(string, *ipcpb.FreezeRequest) error { return nil })
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

	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, nil, func(string, *ipcpb.FreezeRequest) error { return nil })
	mock.ExpectQuery("SELECT.*FROM foghorn.artifacts").WillReturnError(fmt.Errorf("timeout"))

	count := r.advancePending(context.Background())
	if count != 0 {
		t.Fatalf("expected 0, got %d", count)
	}
}

// --- sendFreezeForArtifact ---

// A proactive freeze goes through the shared assignment and dispatches the STAGING URL + server-minted
// attempt id (the node echoes the attempt id at completion).
func TestSendFreezeForArtifact_Clip(t *testing.T) {
	mockDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	fc := &freezeCapture{}
	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, nil, fc.send)

	dispatched, err := r.sendFreezeForArtifact(context.Background(), "clip-hash", "clip", "stream1", "tenant1", "mp4", "node-1", "/data/clip.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if !dispatched {
		t.Fatal("a successful send must report dispatched=true")
	}

	call := fc.last()
	if call.Req.PresignedPutUrl != "https://s3/staging/clip-hash" {
		t.Fatalf("expected the staging URL from the assignment, got %q", call.Req.PresignedPutUrl)
	}
	if call.Req.RequestId != "att-clip-hash" {
		t.Fatalf("expected the server-minted attempt id as request id, got %q", call.Req.RequestId)
	}
	if call.Req.AssetType != "clip" {
		t.Fatalf("expected asset_type=clip, got %s", call.Req.AssetType)
	}
}

// When the wire send fails, no completion will ever arrive for the attempt, so the row must be
// returned to a retryable 'failed' state immediately, guarded by the SERVER-MINTED attempt id.
func TestSendFreezeForArtifact_SendFailureRevertsToFailed(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()
	// The revert re-enqueues staging garbage, which now requires a local backend fingerprint — wire the control S3
	// client, matching a production cell that always has a local store.
	control.SetS3Client(controlS3Stub{})
	t.Cleanup(func() { control.SetS3Client(nil) })

	failingSend := func(nodeID string, req *ipcpb.FreezeRequest) error { return fmt.Errorf("stream send failed") }
	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, nil, failingSend)

	// After the send fails, the row reverts to 'failed' with the attempt identity cleared AND the ambiguous
	// staging object is durably enqueued for cleanup — both in ONE transaction. Guarded by the attempt id
	// ($2 = "att-clip-hash").
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE foghorn.artifacts.*sync_status = 'failed'.*sync_request_id = NULL.*WHERE artifact_hash = \\$1 AND sync_status = 'in_progress'.*status = 'ready'.*sync_request_id = \\$2 AND sync_node_id = \\$3").
		WithArgs("clip-hash", "att-clip-hash", "node-1", "tenant1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").
		WithArgs(control.FreezeStagingKey("k/clip-hash", "att-clip-hash"), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	dispatched, err := r.sendFreezeForArtifact(context.Background(), "clip-hash", "clip", "stream1", "tenant1", "mp4", "node-1", "/data/clip.mp4")
	if err == nil {
		t.Fatal("expected error from failing send")
	}
	if dispatched {
		t.Fatal("a failed send must not report dispatched=true")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// When the shared assignment denies (not authorized / not eligible / official storage remote), the freeze
// is NOT dispatched and it is not an error for the reconciler loop.
func TestSendFreezeForArtifact_NotAssignedIsNoOp(t *testing.T) {
	mockDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	fc := &freezeCapture{}
	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, nil, fc.send)
	r.prepareFreeze = func(_ context.Context, _, _, _, _, _, _, _ string, _ time.Duration) (control.FreezeAssignment, string, bool) {
		return control.FreezeAssignment{}, "cluster_not_authorized", false
	}

	dispatched, err := r.sendFreezeForArtifact(context.Background(), "clip-hash", "clip", "stream1", "tenant1", "mp4", "node-1", "/data/clip.mp4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dispatched {
		t.Fatal("a denied assignment must report dispatched=false so the reconciler does not count it as retried/advanced")
	}
	if fc.count() != 0 {
		t.Fatalf("must not dispatch a freeze when the assignment is denied, sent %d", fc.count())
	}
}

// An empty format defaults to mp4 BEFORE the shared assignment is asked for a canonical key.
func TestSendFreezeForArtifact_ClipDefaultFormat(t *testing.T) {
	mockDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	var gotFormat string
	fc := &freezeCapture{}
	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, nil, fc.send)
	r.prepareFreeze = func(_ context.Context, _, hash, _, _, format, _, _ string, _ time.Duration) (control.FreezeAssignment, string, bool) {
		gotFormat = format
		return control.FreezeAssignment{AttemptID: "att-" + hash, StagingURL: "https://s3/staging/" + hash}, "", true
	}

	if _, err := r.sendFreezeForArtifact(context.Background(), "clip-hash", "clip", "stream1", "tenant1", "", "node-1", "/data/clip"); err != nil {
		t.Fatal(err)
	}
	if gotFormat != "mp4" {
		t.Fatalf("expected default format mp4, got %s", gotFormat)
	}
}

func TestSendFreezeForArtifact_Vod(t *testing.T) {
	mockDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	var gotType, gotFormat string
	fc := &freezeCapture{}
	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, nil, fc.send)
	r.prepareFreeze = func(_ context.Context, assetType, hash, _, _, format, _, _ string, _ time.Duration) (control.FreezeAssignment, string, bool) {
		gotType, gotFormat = assetType, format
		return control.FreezeAssignment{AttemptID: "att-" + hash, StagingURL: "https://s3/staging/" + hash}, "", true
	}

	if _, err := r.sendFreezeForArtifact(context.Background(), "vod-hash", "vod", "stream1", "tenant1", "mkv", "node-1", "/data/vod.mkv"); err != nil {
		t.Fatal(err)
	}
	if gotType != "vod" || gotFormat != "mkv" {
		t.Fatalf("expected (vod, mkv) passed to assignment, got (%s, %s)", gotType, gotFormat)
	}
	if fc.last().Req.PresignedPutUrl != "https://s3/staging/vod-hash" {
		t.Fatalf("VOD should dispatch the staging URL, got %q", fc.last().Req.PresignedPutUrl)
	}
}

func TestSendFreezeForArtifact_VodDefaultFormat(t *testing.T) {
	mockDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	var gotFormat string
	fc := &freezeCapture{}
	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, nil, fc.send)
	r.prepareFreeze = func(_ context.Context, _, hash, _, _, format, _, _ string, _ time.Duration) (control.FreezeAssignment, string, bool) {
		gotFormat = format
		return control.FreezeAssignment{AttemptID: "att-" + hash, StagingURL: "u"}, "", true
	}

	if _, err := r.sendFreezeForArtifact(context.Background(), "vod-hash", "vod", "stream1", "tenant1", "", "node-1", "/data/vod"); err != nil {
		t.Fatal(err)
	}
	if gotFormat != "mp4" {
		t.Fatalf("expected default mp4 format, got %s", gotFormat)
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

	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, &mockCommodoreClient{}, func(string, *ipcpb.FreezeRequest) error { return nil })
	count := r.reconcileOrphaned(context.Background())
	if count != 0 {
		t.Fatalf("expected 0 with empty state, got %d", count)
	}
}

func TestReconcileOrphaned_ExistingHashSkipped(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{
		{ClipHash: "existing-hash", FilePath: "/data/existing.mp4", SizeBytes: 100, ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP},
	}, state.ArtifactReportOrder{Fence: 1, Seq: 1})

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	commodore := &mockCommodoreClient{}
	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, commodore, func(string, *ipcpb.FreezeRequest) error { return nil })

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

	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{
		{ClipHash: "new-hash", FilePath: "/data/new.mp4", SizeBytes: 200, ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP},
	}, state.ArtifactReportOrder{Fence: 1, Seq: 1})

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	commodore := &mockCommodoreClient{
		resolveClipHashFn: func(_ context.Context, hash string) (*commodorepb.ResolveClipHashResponse, error) {
			return &commodorepb.ResolveClipHashResponse{Found: true, TenantId: "tenant-1", StreamInternalName: "stream-1"}, nil
		},
	}
	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, commodore, func(string, *ipcpb.FreezeRequest) error { return nil })

	// Batch check — hash not found
	mock.ExpectQuery("SELECT artifact_hash FROM foghorn.artifacts").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash"}))

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO foghorn.artifacts").
		WithArgs("new-hash", "clip", "stream-1", "tenant-1", "").
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

func TestReconcileOrphaned_DVROrphanSkipped(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{
		{ClipHash: "dvr-hash", FilePath: "/data/dvr/abc", SizeBytes: 1024, ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_DVR},
	}, state.ArtifactReportOrder{Fence: 1, Seq: 1})

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	commodore := &mockCommodoreClient{}
	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, commodore, func(string, *ipcpb.FreezeRequest) error { return nil })

	// Batch check returns no existing rows; DVR candidate should be skipped
	// before any INSERT into foghorn.artifacts.
	mock.ExpectQuery("SELECT artifact_hash FROM foghorn.artifacts").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash"}))

	count := r.reconcileOrphaned(context.Background())
	if count != 0 {
		t.Fatalf("expected 0 (DVR skipped), got %d", count)
	}
	if len(commodore.clipCalls) != 0 {
		t.Fatalf("commodore must not be called for DVR orphans; got %d calls", len(commodore.clipCalls))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRetryFailed_SQLExcludesDVR(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, nil, func(string, *ipcpb.FreezeRequest) error { return nil })

	// Match SQL containing the DVR filter clause; sqlmock uses regex.
	mock.ExpectQuery(`artifact_type != 'dvr'`).
		WithArgs(50).
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "stream_internal_name", "tenant_id", "format", "node_id", "file_path"}))

	r.retryFailed(context.Background())
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdvancePending_SQLExcludesDVR(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, nil, func(string, *ipcpb.FreezeRequest) error { return nil })

	mock.ExpectQuery(`artifact_type != 'dvr'`).
		WithArgs(50).
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "stream_internal_name", "tenant_id", "format", "node_id", "file_path"}))

	r.advancePending(context.Background())
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileOrphaned_CommodoreFails_Skips(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{
		{ClipHash: "unresolvable", FilePath: "/data/unresolvable.mp4", SizeBytes: 50, ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP},
	}, state.ArtifactReportOrder{Fence: 1, Seq: 1})

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	commodore := &mockCommodoreClient{
		resolveClipHashFn: func(_ context.Context, _ string) (*commodorepb.ResolveClipHashResponse, error) {
			return nil, fmt.Errorf("commodore unavailable")
		},
	}
	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, commodore, func(string, *ipcpb.FreezeRequest) error { return nil })

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

	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{
		{ClipHash: "hash-a", FilePath: "/a.mp4", SizeBytes: 1, ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP},
		{ClipHash: "hash-b", FilePath: "/b.mp4", SizeBytes: 1, ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP},
		{ClipHash: "hash-c", FilePath: "/c.mp4", SizeBytes: 1, ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP},
	}, state.ArtifactReportOrder{Fence: 1, Seq: 1})

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	commodore := &mockCommodoreClient{
		resolveClipHashFn: func(_ context.Context, hash string) (*commodorepb.ResolveClipHashResponse, error) {
			return &commodorepb.ResolveClipHashResponse{Found: true, TenantId: "t1", StreamInternalName: "s1"}, nil
		},
	}
	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, commodore, func(string, *ipcpb.FreezeRequest) error { return nil })
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

func TestProjectCommodoreArtifactStateRepairsStorageAndThumbnailProjection(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	commodore := &mockCommodoreClient{}
	r := newTestReconciler(t, mockDB, nil, commodore, nil)
	r.batchSize = 10
	r.clusterID = "media-us-1"

	// Nullable columns: nil clears; a null tracks column → tracks_present false.
	rows := sqlmock.NewRows(projectionRowCols).
		AddRow("vod-hash", "vod", "tenant-1", "media-us-1", true, int64(1024),
			int64(120500), `[{"type":"video","codec":"h264"}]`, "synced", true, "local", int64(7), "ready", int64(1800000000), nil, "media-official").
		AddRow("clip-hash", "clip", "tenant-1", nil, nil, nil,
			nil, nil, "processing", nil, nil, int64(9), "processing", nil, nil, nil)

	mock.ExpectQuery("FROM foghorn.artifacts").
		WithArgs(10, "media-us-1").
		WillReturnRows(rows)
	// Confirmed coverage (mock returns Found:true) → one watermark advance per row, keyed on
	// the source revision.
	mock.ExpectExec("UPDATE foghorn.artifacts SET catalog_synced_rev").
		WithArgs(int64(7), "vod-hash").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE foghorn.artifacts SET catalog_synced_rev").
		WithArgs(int64(9), "clip-hash").WillReturnResult(sqlmock.NewResult(0, 1))

	count, _ := r.projectCommodoreArtifactState(context.Background())
	if count != 2 {
		t.Fatalf("expected 2 snapshot projections, got %d", count)
	}
	if len(commodore.snapshotCalls) != 2 {
		t.Fatalf("expected 2 snapshot calls, got %d", len(commodore.snapshotCalls))
	}
	vod := commodore.snapshotCalls[0]
	if vod.GetAssetKey() != "vod-hash" || vod.GetDurationMs() != 120500 || !vod.GetTracksPresent() ||
		!vod.GetIsSynced() || vod.GetSourceRevision() != 7 || vod.GetStorageClusterId() != "media-us-1" {
		t.Fatalf("unexpected vod snapshot: %+v", vod)
	}
	// The AUTHORITATIVE thumbnail serving cluster projects INDEPENDENTLY of storage_cluster_id (here "media-official"
	// != the byte-storage "media-us-1"), so a BYOC/cross-cell artifact links the cluster that holds the thumbnail.
	if vod.GetThumbnailServingClusterId() != "media-official" {
		t.Fatalf("expected thumbnail_serving_cluster_id=media-official, got %q", vod.GetThumbnailServingClusterId())
	}
	// Source authority: the snapshot asserts this cluster as the projection source so Commodore
	// can enforce origin ownership.
	if vod.GetSourceClusterId() != "media-us-1" {
		t.Fatalf("expected source_cluster_id=media-us-1, got %q", vod.GetSourceClusterId())
	}
	clip := commodore.snapshotCalls[1]
	// clip has null size/duration/tracks/cluster → cleared (absent), tracks not present.
	if clip.GetAssetKey() != "clip-hash" || clip.GetTracksPresent() || clip.GetIsSynced() ||
		clip.SizeBytes != nil || clip.DurationMs != nil || clip.GetSourceRevision() != 9 {
		t.Fatalf("unexpected clip snapshot: %+v", clip)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// backfillCatalogRevisions seeds catalog_revision=0 rows scoped to this cluster
// and returns the number seeded so reconcile() can decide whether to self-trigger.
func TestBackfillCatalogRevisions_SeedsAndReportsCount(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()
	r := newTestReconciler(t, mockDB, nil, nil, nil)
	r.clusterID = "c1"

	mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET catalog_revision = nextval`).
		WithArgs(catalogBackfillBatch, "c1").
		WillReturnResult(sqlmock.NewResult(0, 3))
	if n := r.backfillCatalogRevisions(context.Background()); n != 3 {
		t.Fatalf("seeded = %d, want 3", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Convergence: once every revision-0 row has a revision, the backfill UPDATE matches nothing
// and returns 0 — the steady-state no-op.
func TestBackfillCatalogRevisions_NoOpWhenConverged(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()
	r := newTestReconciler(t, mockDB, nil, nil, nil)
	r.clusterID = "c1"

	mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET catalog_revision = nextval`).
		WithArgs(catalogBackfillBatch, "c1").
		WillReturnResult(sqlmock.NewResult(0, 0))
	if n := r.backfillCatalogRevisions(context.Background()); n != 0 {
		t.Fatalf("converged seeded = %d, want 0", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// projectCommodoreArtifactState returns scanned = number of eligible rows pulled, so a full
// batch (scanned == batchSize) tells reconcile() more work is pending.
func TestProjectCommodoreArtifactState_ReportsScannedForBatchFull(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	commodore := &mockCommodoreClient{}
	r := newTestReconciler(t, mockDB, nil, commodore, nil)
	r.batchSize = 2
	r.clusterID = "c1"

	rows := sqlmock.NewRows(projectionRowCols).
		AddRow("h1", "vod", "t1", "c1", true, int64(1), int64(1000), nil, "synced", true, "local", int64(5), "ready", nil, nil, nil).
		AddRow("h2", "vod", "t1", "c1", true, int64(1), int64(1000), nil, "synced", true, "local", int64(6), "ready", nil, nil, nil)
	mock.ExpectQuery("FROM foghorn.artifacts").WithArgs(2, "c1").WillReturnRows(rows)
	mock.ExpectExec("UPDATE foghorn.artifacts SET catalog_synced_rev").
		WithArgs(int64(5), "h1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE foghorn.artifacts SET catalog_synced_rev").
		WithArgs(int64(6), "h2").WillReturnResult(sqlmock.NewResult(0, 1))

	count, scanned := r.projectCommodoreArtifactState(context.Background())
	if count != 2 || scanned != 2 {
		t.Fatalf("count=%d scanned=%d, want 2/2 (batch full)", count, scanned)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A deleted foghorn row projects a DELETION (Deleted=true) so the catalog row is removed —
// otherwise a retention-expired / deleted asset shows Ready in /library forever.
func TestProjectCommodoreArtifactState_ProjectsDeletion(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	commodore := &mockCommodoreClient{}
	r := newTestReconciler(t, mockDB, nil, commodore, nil)
	r.batchSize = 10
	r.clusterID = "c1"

	// status='deleted' row (the projection scan now includes deleted rows).
	mock.ExpectQuery("FROM foghorn.artifacts").WithArgs(10, "c1").
		WillReturnRows(sqlmock.NewRows(projectionRowCols).AddRow(
			"gone-hash", "vod", "t1", "c1", true, int64(1),
			int64(1000), nil, "synced", true, "local", int64(8), "deleted", nil, nil, nil))
	// Watermark advances once the deletion is projected (mock returns Found:true, rev=8).
	mock.ExpectExec("UPDATE foghorn.artifacts SET catalog_synced_rev").
		WithArgs(int64(8), "gone-hash").WillReturnResult(sqlmock.NewResult(0, 1))

	count, _ := r.projectCommodoreArtifactState(context.Background())
	if count != 1 {
		t.Fatalf("expected 1 deletion projected, got %d", count)
	}
	if len(commodore.snapshotCalls) != 1 || !commodore.snapshotCalls[0].GetDeleted() {
		t.Fatalf("expected a Deleted=true snapshot, got %+v", commodore.snapshotCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A deletion is covered only by a PRESENT tombstone: if Commodore reports the catalog row absent
// (Found=false) the watermark must NOT advance — advancing on a bare absence would let purge reap
// the foghorn row while no surviving guard blocks a lagging writer from resurrecting the asset. The
// row is backed off and retried instead.
func TestProjectCommodoreArtifactState_DeletionAbsentDoesNotAdvance(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	commodore := &mockCommodoreClient{
		snapshotRespFn: func(req *commodorepb.UpdateArtifactCatalogSnapshotRequest) (*commodorepb.UpdateArtifactCatalogSnapshotResponse, error) {
			// Deletion projection, but the catalog has no row to tombstone → NOT covered.
			return &commodorepb.UpdateArtifactCatalogSnapshotResponse{Found: false}, nil
		},
	}
	r := newTestReconciler(t, mockDB, nil, commodore, nil)
	r.batchSize = 10
	r.clusterID = "c1"

	mock.ExpectQuery("FROM foghorn.artifacts").WithArgs(10, "c1").
		WillReturnRows(sqlmock.NewRows(projectionRowCols).AddRow(
			"gone-hash", "vod", "t1", "c1", true, int64(1),
			int64(1000), nil, "synced", true, "local", int64(8), "deleted", nil, nil, nil))
	// No catalog_synced_rev advance — the absent (uncovered) deletion is backed off.
	mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET catalog_projection_attempts = catalog_projection_attempts \+ 1`).
		WithArgs("gone-hash", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	count, _ := r.projectCommodoreArtifactState(context.Background())
	if count != 0 {
		t.Fatalf("absent deletion must not count as covered, got %d", count)
	}
	if len(commodore.snapshotCalls) != 1 || !commodore.snapshotCalls[0].GetDeleted() {
		t.Fatalf("expected one Deleted=true snapshot attempt, got %+v", commodore.snapshotCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// projectionRowCols is the column list the projection scan selects, in order.
var projectionRowCols = []string{
	"artifact_hash", "artifact_type", "tenant_id", "storage_cluster_id", "has_thumbnails", "size_bytes",
	"duration_ms", "tracks", "sync_status", "dtsh_synced", "storage_location", "catalog_revision",
	"lifecycle_status", "retention_unix", "error_message", "thumbnail_serving_cluster_id",
}

// Fairness: the scan must order by catalog_synced_rev (projection age), not catalog_revision
// (mutation age), so a continuously-mutating cohort can't starve rows behind it.
func TestProjectCommodoreArtifactState_OrdersByProjectionAge(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	commodore := &mockCommodoreClient{}
	r := newTestReconciler(t, mockDB, nil, commodore, nil)
	r.batchSize = 5
	r.clusterID = "c1"

	mock.ExpectQuery(`ORDER BY catalog_synced_rev ASC, catalog_revision ASC`).
		WithArgs(5, "c1").
		WillReturnRows(sqlmock.NewRows(projectionRowCols))

	r.projectCommodoreArtifactState(context.Background())
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A poison row (unsupported artifact_type) records a quarantine watermark — NOT a
// catalog_synced_rev advance — and never calls Commodore, so it can't be falsely confirmed.
func TestProjectCommodoreArtifactState_QuarantinesUnsupportedType(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	commodore := &mockCommodoreClient{}
	r := newTestReconciler(t, mockDB, nil, commodore, nil)
	r.batchSize = 10
	r.clusterID = "c1"

	rows := sqlmock.NewRows(projectionRowCols).AddRow(
		"bad-hash", "weird-type", "t1", nil, nil, nil,
		nil, nil, "processing", nil, nil, int64(3), false, nil, nil, nil)
	mock.ExpectQuery("FROM foghorn.artifacts").WithArgs(10, "c1").WillReturnRows(rows)
	mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET catalog_quarantined_rev = \$1, catalog_quarantine_error = \$3`).
		WithArgs(int64(3), "bad-hash", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	count, _ := r.projectCommodoreArtifactState(context.Background())
	if count != 0 {
		t.Fatalf("poison row must not count as projected, got %d", count)
	}
	if len(commodore.snapshotCalls) != 0 {
		t.Fatalf("poison row must never call Commodore, got %d calls", len(commodore.snapshotCalls))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Anti-starvation: a row that can't be projected (found=false) must NOT block a later valid row
// in the same batch, and must be backed off so it can't refill the batch and head-of-line block
// future passes. This is the progress-past-failure the ordering-only fairness test can't prove.
func TestProjectCommodoreArtifactState_FailingRowDoesNotBlockValidRow(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	commodore := &mockCommodoreClient{
		snapshotRespFn: func(req *commodorepb.UpdateArtifactCatalogSnapshotRequest) (*commodorepb.UpdateArtifactCatalogSnapshotResponse, error) {
			if req.GetAssetKey() == "stuck-hash" {
				return &commodorepb.UpdateArtifactCatalogSnapshotResponse{Found: false}, nil // never registered
			}
			return &commodorepb.UpdateArtifactCatalogSnapshotResponse{Found: true, CurrentRevision: req.GetSourceRevision()}, nil
		},
	}
	r := newTestReconciler(t, mockDB, nil, commodore, nil)
	r.batchSize = 10
	r.clusterID = "c1"

	rows := sqlmock.NewRows(projectionRowCols).
		AddRow("stuck-hash", "vod", "t1", "c1", true, int64(1), int64(1), `[]`, "pending", false, "local", int64(5), "processing", nil, nil, nil).
		AddRow("good-hash", "vod", "t1", "c1", true, int64(2), int64(2), `[]`, "synced", true, "local", int64(6), "ready", nil, nil, nil)
	mock.ExpectQuery("FROM foghorn.artifacts").WithArgs(10, "c1").WillReturnRows(rows)
	// stuck-hash backs off (not-found), good-hash advances — the failing row doesn't block it.
	mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET catalog_projection_attempts = catalog_projection_attempts \+ 1`).
		WithArgs("stuck-hash", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET catalog_synced_rev`).
		WithArgs(int64(6), "good-hash").
		WillReturnResult(sqlmock.NewResult(0, 1))

	count, _ := r.projectCommodoreArtifactState(context.Background())
	if count != 1 {
		t.Fatalf("expected the valid row to project past the failing one, got count=%d", count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Coverage guard: a row that comes back found but with a stored revision BEHIND the projected
// source revision (concurrent insert / stale guard-reject) must NOT advance the watermark.
func TestProjectCommodoreArtifactState_NotCoveredDoesNotAdvance(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	commodore := &mockCommodoreClient{
		snapshotRespFn: func(req *commodorepb.UpdateArtifactCatalogSnapshotRequest) (*commodorepb.UpdateArtifactCatalogSnapshotResponse, error) {
			return &commodorepb.UpdateArtifactCatalogSnapshotResponse{Found: true, CurrentRevision: req.GetSourceRevision() - 1}, nil
		},
	}
	r := newTestReconciler(t, mockDB, nil, commodore, nil)
	r.batchSize = 10
	r.clusterID = "c1"

	rows := sqlmock.NewRows(projectionRowCols).AddRow(
		"vod-hash", "vod", "t1", "c1", true, int64(1024),
		int64(1000), `[]`, "synced", true, "local", int64(7), "ready", nil, nil, nil)
	mock.ExpectQuery("FROM foghorn.artifacts").WithArgs(10, "c1").WillReturnRows(rows)
	// No watermark advance — but the uncovered row is backed off (exponential) so it can't
	// head-of-line block.
	mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET catalog_projection_attempts = catalog_projection_attempts \+ 1`).
		WithArgs("vod-hash", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	count, _ := r.projectCommodoreArtifactState(context.Background())
	if count != 0 {
		t.Fatalf("uncovered row must not count as projected, got %d", count)
	}
	if len(commodore.snapshotCalls) != 1 {
		t.Fatalf("expected exactly one snapshot attempt, got %d", len(commodore.snapshotCalls))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// --- resolveArtifactContext ---

func TestResolveArtifactContext_Clip(t *testing.T) {
	commodore := &mockCommodoreClient{
		resolveClipHashFn: func(_ context.Context, hash string) (*commodorepb.ResolveClipHashResponse, error) {
			return &commodorepb.ResolveClipHashResponse{Found: true, TenantId: "t-clip", StreamInternalName: "s-clip"}, nil
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
		resolveDVRHashFn: func(_ context.Context, hash string) (*commodorepb.ResolveDVRHashResponse, error) {
			return &commodorepb.ResolveDVRHashResponse{Found: true, TenantId: "t-dvr", StreamInternalName: "s-dvr"}, nil
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
		resolveVodHashFn: func(_ context.Context, hash string) (*commodorepb.ResolveVodHashResponse, error) {
			return &commodorepb.ResolveVodHashResponse{Found: true, TenantId: "t-vod", InternalName: "s-vod"}, nil
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
		resolveClipHashFn: func(_ context.Context, hash string) (*commodorepb.ResolveClipHashResponse, error) {
			return &commodorepb.ResolveClipHashResponse{Found: false}, nil
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
		input ipcpb.ArtifactEvent_ArtifactType
		want  string
	}{
		{ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP, "clip"},
		{ipcpb.ArtifactEvent_ARTIFACT_TYPE_DVR, "dvr"},
		{ipcpb.ArtifactEvent_ARTIFACT_TYPE_VOD, "vod"},
		{ipcpb.ArtifactEvent_ARTIFACT_TYPE_UNSPECIFIED, ""},
		{99, ""},
	}
	for _, tc := range tests {
		got := artifactTypeFromProto(tc.input)
		if got != tc.want {
			t.Errorf("artifactTypeFromProto(%d) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// --- artifactAssetTypeFromString ---

func TestArtifactAssetTypeFromString(t *testing.T) {
	tests := []struct {
		input  string
		want   commodorepb.ArtifactAssetType
		wantOK bool
	}{
		{"clip", commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_CLIP, true},
		{"dvr", commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_DVR, true},
		{"dvr_segment", commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_DVR, true},
		{"dvr_manifest", commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_DVR, true},
		{"vod", commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD, true},
		{"unknown", commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_UNSPECIFIED, false},
		{"", commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_UNSPECIFIED, false},
	}
	for _, tc := range tests {
		got, ok := artifactAssetTypeFromString(tc.input)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("artifactAssetTypeFromString(%q) = (%v, %v), want (%v, %v)", tc.input, got, ok, tc.want, tc.wantOK)
		}
	}
}

// backfillActiveObjectKey populates active_object_key prefix-aware from s3_url for LOCALLY-BACKED legacy synced
// clip/vod rows, tenant-scoped, over a durable keyset cursor. A row whose s3_url is under a FOREIGN bucket
// (a federation-adopted remote pointer that slipped past the durable_backend_local gate) is SKIPPED — never
// rewritten as a local key — and the cursor still advances so it cannot starve later rows.
func TestBackfillActiveObjectKey(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	s3 := &mockReconcilerS3Client{} // default ParseLocalS3URL accepts only s3://bucket/
	r := newTestReconciler(t, mockDB, s3, nil, nil)

	// 1) Durable cursor read (start).
	mock.ExpectQuery(`SELECT last_hash FROM foghorn.active_object_key_backfill_cursor WHERE id = true`).
		WillReturnRows(sqlmock.NewRows([]string{"last_hash"}).AddRow(""))
	// 2) Keyset scan of locally-backed legacy rows past the cursor: two local + one foreign-bucket row.
	mock.ExpectQuery(`SELECT artifact_hash, tenant_id::text, s3_url\s+FROM foghorn.artifacts`).
		WithArgs("", 500).
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "tenant_id", "s3_url"}).
			AddRow("clip-legacy", "tenant-1", "s3://bucket/tenant-1/clips/clip-legacy.mp4").
			AddRow("remote-legacy", "tenant-1", "s3://otherbucket/tenant-1/vods/remote.mp4").
			AddRow("vod-legacy", "tenant-1", "s3://bucket/tenant-1/vods/vod-legacy/movie.mp4"))
	// 3) The two LOCAL rows update (tenant-scoped, raw key); the foreign-bucket row is SKIPPED (no UPDATE).
	mock.ExpectExec(`UPDATE foghorn.artifacts SET active_object_key = \$3\s+WHERE artifact_hash = \$1 AND tenant_id::text = \$2`).
		WithArgs("clip-legacy", "tenant-1", "tenant-1/clips/clip-legacy.mp4").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE foghorn.artifacts SET active_object_key = \$3\s+WHERE artifact_hash = \$1 AND tenant_id::text = \$2`).
		WithArgs("vod-legacy", "tenant-1", "tenant-1/vods/vod-legacy/movie.mp4").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// 4) Short page (< 500) ⇒ cursor WRAPS to the start.
	mock.ExpectExec(`UPDATE foghorn.active_object_key_backfill_cursor SET last_hash = \$1 WHERE id = true`).
		WithArgs("").
		WillReturnResult(sqlmock.NewResult(0, 1))

	r.backfillActiveObjectKey(context.Background())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// reconcileFreezePublicationLedger is the durable backstop for freeze publication. It (1) SKIPS a row whose
// attempt is still on the artifact (retrying), (2) drops the ledger row WITHOUT deleting a guarded candidate
// that is the live pointer, and (3) enqueues + drops everything else (staging, or an orphaned candidate).
func TestReconcileFreezePublicationLedger(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()
	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, nil, nil)

	// Durable keyset cursor read (start).
	mock.ExpectQuery(`SELECT last_key FROM foghorn.freeze_publication_ledger_cursor WHERE id = true`).
		WillReturnRows(sqlmock.NewRows([]string{"last_key"}).AddRow(""))
	// Three aged ledger rows for one artifact: a staging object, a LIVE candidate, and an ORPHANED candidate.
	mock.ExpectQuery(`SELECT object_key, artifact_hash, tenant_id, request_id, guarded, COALESCE\(backend_id, ''\)\s+FROM foghorn.freeze_publication_ledger`).
		WithArgs("", 500).
		WillReturnRows(sqlmock.NewRows([]string{"object_key", "artifact_hash", "tenant_id", "request_id", "guarded", "backend_id"}).
			AddRow("obj.staging.reqOLD", "hash-1", "t1", "reqOLD", false, "").
			AddRow("obj.att-reqOLD", "hash-1", "t1", "reqOLD", true, "").
			AddRow("obj.dtsh.att-reqOLD", "hash-1", "t1", "reqOLD", true, ""))
	// Per-row artifact re-read: attempt reqOLD is NO LONGER on the row (a newer/blank attempt), active pointers
	// name only the media candidate → the .dtsh candidate is orphaned.
	reread := func() {
		mock.ExpectQuery(`SELECT COALESCE\(sync_request_id,''\), COALESCE\(dtsh_sync_request_id,''\),\s+COALESCE\(active_object_key,''\), COALESCE\(active_dtsh_key,''\)\s+FROM foghorn.artifacts`).
			WithArgs("hash-1", "t1").
			WillReturnRows(sqlmock.NewRows([]string{"sync_request_id", "dtsh_sync_request_id", "active_object_key", "active_dtsh_key"}).
				AddRow("", "", "obj.att-reqOLD", ""))
	}
	// Row 1 (staging): enqueue + drop, in one tx.
	reread()
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO foghorn.staging_cleanup_queue`).WithArgs("obj.staging.reqOLD", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM foghorn.freeze_publication_ledger`).WithArgs("obj.staging.reqOLD").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	// Row 2 (LIVE candidate == active_object_key): drop the ledger row only, KEEP the object.
	reread()
	mock.ExpectExec(`DELETE FROM foghorn.freeze_publication_ledger WHERE object_key = \$1`).WithArgs("obj.att-reqOLD").WillReturnResult(sqlmock.NewResult(0, 1))
	// Row 3 (ORPHANED .dtsh candidate): enqueue + drop.
	reread()
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO foghorn.staging_cleanup_queue`).WithArgs("obj.dtsh.att-reqOLD", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM foghorn.freeze_publication_ledger`).WithArgs("obj.dtsh.att-reqOLD").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	// Short page (< batch) ⇒ cursor WRAPS to the start.
	mock.ExpectExec(`UPDATE foghorn.freeze_publication_ledger_cursor SET last_key = \$1 WHERE id = true`).
		WithArgs("").WillReturnResult(sqlmock.NewResult(0, 1))

	r.reconcileFreezePublicationLedger(context.Background())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A ledger row whose attempt is STILL on the artifact is retrying — the sweep must leave it untouched.
func TestReconcileFreezePublicationLedger_SkipsRetryingAttempt(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()
	r := newTestReconciler(t, mockDB, &mockReconcilerS3Client{}, nil, nil)

	mock.ExpectQuery(`SELECT last_key FROM foghorn.freeze_publication_ledger_cursor WHERE id = true`).
		WillReturnRows(sqlmock.NewRows([]string{"last_key"}).AddRow(""))
	mock.ExpectQuery(`SELECT object_key, artifact_hash, tenant_id, request_id, guarded, COALESCE\(backend_id, ''\)\s+FROM foghorn.freeze_publication_ledger`).
		WithArgs("", 500).
		WillReturnRows(sqlmock.NewRows([]string{"object_key", "artifact_hash", "tenant_id", "request_id", "guarded", "backend_id"}).
			AddRow("obj.att-reqLIVE", "hash-1", "t1", "reqLIVE", true, ""))
	// The attempt reqLIVE is STILL the row's sync_request_id → retrying → no cleanup, no ledger delete.
	mock.ExpectQuery(`FROM foghorn.artifacts`).
		WithArgs("hash-1", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"sync_request_id", "dtsh_sync_request_id", "active_object_key", "active_dtsh_key"}).
			AddRow("reqLIVE", "", "", ""))
	// Short page ⇒ cursor wraps.
	mock.ExpectExec(`UPDATE foghorn.freeze_publication_ledger_cursor SET last_key = \$1 WHERE id = true`).
		WithArgs("").WillReturnResult(sqlmock.NewResult(0, 1))

	r.reconcileFreezePublicationLedger(context.Background())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// When Foghorn projects a NON-EMPTY serving cluster but Commodore does not echo it back (a Commodore that ignores
// field 21), the row must NOT be marked covered — it is backed off and re-projected until the value is echoed, so
// thumbnail_serving_cluster_id is never permanently lost. A row with no serving cluster (NULL) needs no ack and
// advances normally.
func TestProjectCommodoreArtifactState_ThumbnailServingClusterAck(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	// Compatibility case: a Commodore that acks the snapshot (Found + covered revision) but does not echo
	// thumbnail_serving_cluster_id in its response — the reconciler must not treat the serving cluster as acknowledged.
	commodore := &mockCommodoreClient{
		snapshotRespFn: func(req *commodorepb.UpdateArtifactCatalogSnapshotRequest) (*commodorepb.UpdateArtifactCatalogSnapshotResponse, error) {
			return &commodorepb.UpdateArtifactCatalogSnapshotResponse{Found: true, CurrentRevision: req.GetSourceRevision()}, nil
		},
	}
	r := newTestReconciler(t, mockDB, nil, commodore, nil)
	r.batchSize = 10
	r.clusterID = "c1"

	// One row WITH a serving cluster (must NOT advance against the non-echoing Commodore) and one WITHOUT (advances).
	rows := sqlmock.NewRows(projectionRowCols).
		AddRow("with-serving", "vod", "t1", "c1", true, int64(1), int64(1000), nil, "synced", true, "local", int64(7), "ready", nil, nil, "media-official").
		AddRow("no-serving", "vod", "t1", "c1", true, int64(1), int64(1000), nil, "synced", true, "local", int64(8), "ready", nil, nil, nil)
	mock.ExpectQuery("FROM foghorn.artifacts").WithArgs(10, "c1").WillReturnRows(rows)
	// with-serving: NOT acked → backed off (no watermark advance).
	mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET catalog_projection_attempts = catalog_projection_attempts \+ 1`).
		WithArgs("with-serving", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// no-serving: needs no ack → watermark advances.
	mock.ExpectExec("UPDATE foghorn.artifacts SET catalog_synced_rev").
		WithArgs(int64(8), "no-serving").WillReturnResult(sqlmock.NewResult(0, 1))

	count, _ := r.projectCommodoreArtifactState(context.Background())
	if count != 1 {
		t.Fatalf("only the no-serving row should count as covered against a non-echoing Commodore, got %d", count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
