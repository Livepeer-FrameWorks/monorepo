package grpc

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"frameworks/api_balancing/internal/control"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornrelaypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_relay"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"

	"github.com/DATA-DOG/go-sqlmock"
	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// newVodRpcHandlers builds a server with sqlmock + a recording S3 fake. The
// registry is seeded empty so any incidental control.Send* (which reads the
// nil-until-Init control.registry global) returns ErrNotConnected instead of
// panicking, and the cleanup restores it for the next test.
func newVodRpcHandlers(t *testing.T, s3 *fakeVodS3Client) (*FoghornGRPCServer, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	t.Cleanup(control.SetupTestRegistry("", nil))
	srv := NewFoghornGRPCServer(db, logging.NewLogger(), nil, nil, nil, nil, s3, nil)
	return srv, mock
}

// ---- AbortVodUpload: validation + S3 precondition + tenant-scoped lifecycle ----

// Invariant: upload_id is mandatory; without it the RPC is InvalidArgument and
// never touches S3 or the DB.
func TestAbortVodUpload_RequiresUploadID_RpcHandlers(t *testing.T) {
	srv, _ := newVodRpcHandlers(t, &fakeVodS3Client{})

	_, err := srv.AbortVodUpload(context.Background(), &sharedpb.AbortVodUploadRequest{TenantId: "t1"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for missing upload_id, got %v", err)
	}
}

// Invariant: aborting requires durable storage; with no S3 client the RPC fails
// FailedPrecondition rather than silently dropping the multipart upload.
func TestAbortVodUpload_NoS3IsFailedPrecondition_RpcHandlers(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// s3Client nil on purpose.
	srv := NewFoghornGRPCServer(db, logging.NewLogger(), nil, nil, nil, nil, nil, nil)

	_, err = srv.AbortVodUpload(context.Background(), &sharedpb.AbortVodUploadRequest{
		TenantId: "t1",
		UploadId: "u1",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition for missing S3, got %v", err)
	}
}

// Invariant: the upload lookup is tenant-scoped (upload_id + tenant_id) and an
// uploading-status row for another tenant yields NotFound, never another
// tenant's in-flight upload.
func TestAbortVodUpload_NotFoundForWrongTenant_RpcHandlers(t *testing.T) {
	srv, mock := newVodRpcHandlers(t, &fakeVodS3Client{})

	mock.ExpectQuery(`SELECT v.artifact_hash, v.s3_key, a.user_id`).
		WithArgs("u1", "wrong-tenant").
		WillReturnError(sql.ErrNoRows)

	_, err := srv.AbortVodUpload(context.Background(), &sharedpb.AbortVodUploadRequest{
		TenantId: "wrong-tenant",
		UploadId: "u1",
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Aborting-claim-wins: the guarded 'uploading'->'aborting' claim commits BEFORE any S3 call, then the
// S3 multipart upload is aborted (with the recorded s3_key + upload_id) and the row transitions
// 'aborting'->'deleted', deleting vod_metadata and emitting the DELETED event in ONE transaction.
func TestAbortVodUpload_AbortsS3AndSoftDeletes_RpcHandlers(t *testing.T) {
	s3 := &fakeVodS3Client{}
	srv, mock := newVodRpcHandlers(t, s3)

	mock.ExpectQuery(`SELECT v.artifact_hash, v.s3_key, a.user_id`).
		WithArgs("u1", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "s3_key", "user_id", "backend_id"}).
			AddRow("hash-1", "vod/t1/hash/video.mp4", sql.NullString{}, testStubBackendID))
	// Durable claim FIRST (tenant-scoped, 'uploading'->'aborting'), BEFORE any S3 call.
	mock.ExpectExec(`UPDATE foghorn\.artifacts\s+SET status = 'aborting'.*WHERE artifact_hash = \$1 AND tenant_id = \$2::uuid AND status = 'uploading'`).
		WithArgs("hash-1", "t1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// After winning the claim, the finalize tx transitions 'aborting'->'deleted', deletes metadata, and
	// emits the DELETED lifecycle event.
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE foghorn\.artifacts\s+SET status = 'deleted'.*WHERE artifact_hash = \$1 AND tenant_id = \$2::uuid AND status = 'aborting'`).
		WithArgs("hash-1", "t1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM foghorn\.vod_metadata`).
		WithArgs("hash-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO foghorn\.artifact_event_outbox`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	resp, err := srv.AbortVodUpload(context.Background(), &sharedpb.AbortVodUploadRequest{
		TenantId: "t1",
		UploadId: "u1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected Success=true, got %+v", resp)
	}
	if s3.abortCalls != 1 || s3.abortKey != "vod/t1/hash/video.mp4" || s3.abortUpID != "u1" {
		t.Fatalf("expected exactly one S3 abort(key=vod/t1/hash/video.mp4, upid=u1), got calls=%d (%q,%q)", s3.abortCalls, s3.abortKey, s3.abortUpID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Lost race: a concurrent completion already moved the upload off 'uploading', so the guarded
// 'uploading'->'aborting' claim affects 0 rows. Abort must NOT call AbortMultipartUpload at all
// (S3 untouched — the winner owns the upload) and returns FailedPrecondition, not a destructive success.
func TestAbortVodUpload_LostRaceLeavesS3Untouched_RpcHandlers(t *testing.T) {
	s3 := &fakeVodS3Client{}
	srv, mock := newVodRpcHandlers(t, s3)

	mock.ExpectQuery(`SELECT v.artifact_hash, v.s3_key, a.user_id`).
		WithArgs("u1", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "s3_key", "user_id", "backend_id"}).
			AddRow("hash-1", "vod/t1/hash/video.mp4", sql.NullString{}, testStubBackendID))
	// Guarded claim matches nothing (status is no longer 'uploading'); no S3 call, no tx.
	mock.ExpectExec(`UPDATE foghorn\.artifacts\s+SET status = 'aborting'.*status = 'uploading'`).
		WithArgs("hash-1", "t1").
		WillReturnResult(sqlmock.NewResult(0, 0))

	_, err := srv.AbortVodUpload(context.Background(), &sharedpb.AbortVodUploadRequest{TenantId: "t1", UploadId: "u1"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition when the abort loses the claim race, got %v", err)
	}
	if s3.abortCalls != 0 {
		t.Fatalf("expected AbortMultipartUpload to NEVER be called when the claim loses the race, got %d calls", s3.abortCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// S3 abort fails (non-NoSuchUpload) after the claim wins: the row is left 'aborting' for the recovery
// worker — no metadata delete, no DELETED event, and the RPC surfaces the error.
func TestAbortVodUpload_S3AbortFailureLeavesAborting_RpcHandlers(t *testing.T) {
	s3 := &fakeVodS3Client{abortErr: errors.New("s3 unreachable")}
	srv, mock := newVodRpcHandlers(t, s3)

	mock.ExpectQuery(`SELECT v.artifact_hash, v.s3_key, a.user_id`).
		WithArgs("u1", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "s3_key", "user_id", "backend_id"}).
			AddRow("hash-1", "vod/t1/hash/video.mp4", sql.NullString{}, testStubBackendID))
	mock.ExpectExec(`UPDATE foghorn\.artifacts\s+SET status = 'aborting'.*status = 'uploading'`).
		WithArgs("hash-1", "t1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// No finalize tx — the abort errored, so the row stays 'aborting'.

	_, err := srv.AbortVodUpload(context.Background(), &sharedpb.AbortVodUploadRequest{TenantId: "t1", UploadId: "u1"})
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal when the S3 abort fails, got %v", err)
	}
	if s3.abortCalls != 1 {
		t.Fatalf("expected exactly one S3 abort attempt, got %d", s3.abortCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// ---- DeleteVodAsset: validation + idempotency + tenant scoping + soft delete ----

// Invariant: artifact_hash is required.
func TestDeleteVodAsset_RequiresArtifactHash_RpcHandlers(t *testing.T) {
	srv, _ := newVodRpcHandlers(t, &fakeVodS3Client{})

	_, err := srv.DeleteVodAsset(context.Background(), &sharedpb.DeleteVodAssetRequest{TenantId: "t1"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

// deleteLookupCols matches the DeleteVodAsset status lookup projection.
func deleteLookupCols() []string {
	return []string{
		"status", "s3_key", "s3_url", "format",
		"size_bytes", "retention_until", "user_id",
		"storage_cluster_id", "origin_cluster_id", "origin_type",
		"active_object_key", "active_dtsh_key", "sync_object_key", "durable_backend_local", "backend_id",
	}
}

// Invariant: the delete status lookup is tenant-scoped (artifact_hash +
// tenant_id). When no local row exists and federation is unwired, the RPC is
// NotFound — it must not delete across tenants.
func TestDeleteVodAsset_NotFoundForWrongTenant_RpcHandlers(t *testing.T) {
	srv, mock := newVodRpcHandlers(t, &fakeVodS3Client{})

	mock.ExpectQuery(`FROM foghorn\.artifacts a`).
		WithArgs("hash-1", "wrong-tenant").
		WillReturnError(sql.ErrNoRows)

	_, err := srv.DeleteVodAsset(context.Background(), &sharedpb.DeleteVodAssetRequest{
		ArtifactHash: "hash-1",
		TenantId:     "wrong-tenant",
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Invariant: deleting an already-deleted asset is an idempotent no-op
// (Success=false, no state mutation), not an error.
func TestDeleteVodAsset_AlreadyDeletedIsNoOp_RpcHandlers(t *testing.T) {
	srv, mock := newVodRpcHandlers(t, &fakeVodS3Client{})

	mock.ExpectQuery(`FROM foghorn\.artifacts a`).
		WithArgs("hash-1", "t1").
		WillReturnRows(sqlmock.NewRows(deleteLookupCols()).AddRow(
			"deleted", "", sql.NullString{}, sql.NullString{},
			sql.NullInt64{}, sql.NullTime{}, sql.NullString{},
			sql.NullString{}, sql.NullString{}, "",
			sql.NullString{}, sql.NullString{}, sql.NullString{}, false, sql.NullString{},
		))

	resp, err := srv.DeleteVodAsset(context.Background(), &sharedpb.DeleteVodAssetRequest{
		ArtifactHash: "hash-1",
		TenantId:     "t1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatalf("expected Success=false for already-deleted, got %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Invariant: deleting a ready VOD fans out node-cleanup (no cached nodes here),
// soft-deletes the artifact row (status -> 'deleted'), and reports success. The
// final UPDATE is the durable state transition.
func TestDeleteVodAsset_SoftDeletesReadyAsset_RpcHandlers(t *testing.T) {
	srv, mock := newVodRpcHandlers(t, &fakeVodS3Client{})

	mock.ExpectQuery(`FROM foghorn\.artifacts a`).
		WithArgs("hash-1", "t1").
		WillReturnRows(sqlmock.NewRows(deleteLookupCols()).AddRow(
			"synced", "vod/t1/hash/video.mp4", sql.NullString{}, sql.NullString{},
			sql.NullInt64{Int64: 2048, Valid: true}, sql.NullTime{}, sql.NullString{},
			sql.NullString{}, sql.NullString{}, "",
			sql.NullString{}, sql.NullString{}, sql.NullString{}, false, sql.NullString{},
		))
	// DURABLE STATE FIRST: soft-delete (guarded, tenant-scoped) + DELETED lifecycle event commit
	// atomically BEFORE any physical cleanup.
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE foghorn\.artifacts SET status = 'deleted'`).
		WithArgs("hash-1", "t1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO foghorn\.artifact_event_outbox`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	// Physical cleanup runs only after the commit: fan out node-cleanup (no cached nodes here).
	mock.ExpectQuery(`SELECT node_id FROM foghorn\.artifact_nodes`).
		WithArgs("hash-1").
		WillReturnRows(sqlmock.NewRows([]string{"node_id"}))

	resp, err := srv.DeleteVodAsset(context.Background(), &sharedpb.DeleteVodAssetRequest{
		ArtifactHash: "hash-1",
		TenantId:     "t1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected Success=true, got %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// ---- RelayServer.RegisterServices: wiring decision ----

// Invariant: RegisterServices registers the FoghornRelay service descriptor on
// the provided gRPC server (the HA command-forwarding surface is actually
// exposed, not silently omitted).
func TestRelayRegisterServices_RegistersFoghornRelay_RpcHandlers(t *testing.T) {
	relay := NewRelayServer(logging.NewLogger())
	srv := grpclib.NewServer()

	relay.RegisterServices(srv)

	svcInfo := srv.GetServiceInfo()
	if _, ok := svcInfo[foghornrelaypb.FoghornRelay_ServiceDesc.ServiceName]; !ok {
		t.Fatalf("FoghornRelay service not registered; have %v", keysOf(svcInfo))
	}
}

func keysOf(m map[string]grpclib.ServiceInfo) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
