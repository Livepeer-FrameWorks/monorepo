package grpc

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
	"time"

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

// vodAssetCols is the exact 20-column shape getVodAssetInfo / ListVodAssets
// select. Keep aligned with buildVodAssetInfo's scan order.
func vodAssetCols() []string {
	return []string{
		"id", "artifact_hash", "status", "size_bytes",
		"storage_location", "s3_url", "error_message",
		"created_at", "updated_at", "retention_until",
		"filename", "title", "description",
		"duration_ms", "resolution", "video_codec", "audio_codec", "bitrate_kbps",
		"s3_upload_id", "s3_key",
	}
}

func vodAssetRow(hash, statusStr string) []driver.Value {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return []driver.Value{
		hash, hash, statusStr, int64(2048),
		"central-primary", "s3://bucket/vod.mp4", "",
		now, now, sql.NullTime{},
		"video.mp4", "Title", "Desc",
		sql.NullInt32{}, sql.NullString{}, sql.NullString{}, sql.NullString{}, sql.NullInt32{},
		"", "vod/t1/hash/video.mp4",
	}
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

// Invariant: a successful abort aborts the S3 multipart upload (with the
// recorded s3_key + upload_id) and transitions the artifact to 'deleted'. The
// vod_metadata row is removed first.
func TestAbortVodUpload_AbortsS3AndSoftDeletes_RpcHandlers(t *testing.T) {
	s3 := &fakeVodS3Client{}
	srv, mock := newVodRpcHandlers(t, s3)

	mock.ExpectQuery(`SELECT v.artifact_hash, v.s3_key, a.user_id`).
		WithArgs("u1", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "s3_key", "user_id"}).
			AddRow("hash-1", "vod/t1/hash/video.mp4", sql.NullString{}))
	mock.ExpectExec(`DELETE FROM foghorn\.vod_metadata`).
		WithArgs("hash-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE foghorn\.artifacts`).
		WithArgs("hash-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

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
	if s3.abortKey != "vod/t1/hash/video.mp4" || s3.abortUpID != "u1" {
		t.Fatalf("expected S3 abort(key=vod/t1/hash/video.mp4, upid=u1), got (%q,%q)", s3.abortKey, s3.abortUpID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// ---- GetVodAsset: validation + NotFound + read fidelity ----

// Invariant: artifact_hash is required.
func TestGetVodAsset_RequiresArtifactHash_RpcHandlers(t *testing.T) {
	srv, _ := newVodRpcHandlers(t, &fakeVodS3Client{})

	_, err := srv.GetVodAsset(context.Background(), &sharedpb.GetVodAssetRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

// Invariant: a missing (or non-vod / deleted) artifact maps sql.ErrNoRows to
// gRPC NotFound, not Internal.
func TestGetVodAsset_NotFound_RpcHandlers(t *testing.T) {
	srv, mock := newVodRpcHandlers(t, &fakeVodS3Client{})

	mock.ExpectQuery(`FROM foghorn\.artifacts a`).
		WithArgs("missing").
		WillReturnError(sql.ErrNoRows)

	_, err := srv.GetVodAsset(context.Background(), &sharedpb.GetVodAssetRequest{ArtifactHash: "missing"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Invariant: a ready artifact row is scanned into a VodAssetInfo with the hash
// surfaced and 'synced' mapped to VOD_STATUS_READY (read-fidelity for the asset
// view).
func TestGetVodAsset_ReturnsReadyAsset_RpcHandlers(t *testing.T) {
	srv, mock := newVodRpcHandlers(t, &fakeVodS3Client{})

	mock.ExpectQuery(`FROM foghorn\.artifacts a`).
		WithArgs("hash-1").
		WillReturnRows(sqlmock.NewRows(vodAssetCols()).AddRow(vodAssetRow("hash-1", "synced")...))

	asset, err := srv.GetVodAsset(context.Background(), &sharedpb.GetVodAssetRequest{ArtifactHash: "hash-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if asset.GetArtifactHash() != "hash-1" {
		t.Fatalf("expected hash-1, got %q", asset.GetArtifactHash())
	}
	if asset.GetStatus() != sharedpb.VodStatus_VOD_STATUS_READY {
		t.Fatalf("expected READY status, got %v", asset.GetStatus())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// ---- ListVodAssets: count + page, excludes deleted ----

// Invariant: listing counts then pages vod artifacts (status != deleted) and
// returns the scanned assets with the total reflected in the cursor response.
func TestListVodAssets_CountsAndPages_RpcHandlers(t *testing.T) {
	srv, mock := newVodRpcHandlers(t, &fakeVodS3Client{})

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM foghorn\.artifacts a WHERE`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int32(1)))
	mock.ExpectQuery(`FROM foghorn\.artifacts a`).
		WillReturnRows(sqlmock.NewRows(vodAssetCols()).AddRow(vodAssetRow("hash-1", "synced")...))

	resp, err := srv.ListVodAssets(context.Background(), &sharedpb.ListVodAssetsRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetAssets()) != 1 || resp.GetAssets()[0].GetArtifactHash() != "hash-1" {
		t.Fatalf("expected one asset hash-1, got %+v", resp.GetAssets())
	}
	if resp.GetPagination().GetTotalCount() != 1 {
		t.Fatalf("expected total=1, got %d", resp.GetPagination().GetTotalCount())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Invariant: a count-query failure is surfaced as Internal (the list never
// returns a partial/empty success on infra error).
func TestListVodAssets_CountErrorIsInternal_RpcHandlers(t *testing.T) {
	srv, mock := newVodRpcHandlers(t, &fakeVodS3Client{})

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM foghorn\.artifacts a WHERE`).
		WillReturnError(sql.ErrConnDone)

	_, err := srv.ListVodAssets(context.Background(), &sharedpb.ListVodAssetsRequest{})
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal, got %v", err)
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
		"storage_cluster_id", "origin_cluster_id",
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
			sql.NullString{}, sql.NullString{},
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
			sql.NullString{}, sql.NullString{},
		))
	mock.ExpectQuery(`SELECT node_id FROM foghorn\.artifact_nodes`).
		WithArgs("hash-1").
		WillReturnRows(sqlmock.NewRows([]string{"node_id"}))
	mock.ExpectExec(`UPDATE foghorn\.artifacts SET status = 'deleted'`).
		WithArgs("hash-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

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
