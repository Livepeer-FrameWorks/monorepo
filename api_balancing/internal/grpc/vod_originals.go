package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const originalDownloadExpiry = 15 * time.Minute

type originalPresigner interface {
	GeneratePresignedGETAttachment(context.Context, string, string, time.Duration) (string, error)
}

func (s *FoghornGRPCServer) originalForTenant(ctx context.Context, tenant, hash string) (foghorndb.GetVodOriginalRow, error) {
	if tenant == "" || hash == "" {
		return foghorndb.GetVodOriginalRow{}, status.Error(codes.InvalidArgument, "tenant and artifact are required")
	}
	row, err := foghorndb.New(s.db).GetVodOriginal(ctx, foghorndb.GetVodOriginalParams{TenantID: tenant, ArtifactHash: hash})
	if errors.Is(err, sql.ErrNoRows) {
		return row, status.Error(codes.NotFound, "VOD not found")
	}
	if err != nil {
		return row, status.Error(codes.Internal, "original lookup failed")
	}
	if row.Status.String == "deleted" || row.Status.String == "failed" || row.Status.String == "expired" || row.Status.String == "aborted" {
		return row, status.Error(codes.NotFound, "VOD not found")
	}
	if row.OriginalS3Key.String == "" || row.OriginalSizeBytes.Int64 <= 0 {
		return row, status.Error(codes.FailedPrecondition, "original_not_kept")
	}
	if !row.DurableBackendLocal {
		return row, status.Error(codes.FailedPrecondition, "original is not stored on this backend")
	}
	if backendErr := requireOriginalBackend(row.BackendID, s.localBackendID()); backendErr != nil {
		return row, backendErr
	}
	return row, nil
}

func requireOriginalBackend(recorded, local string) error {
	if recorded == "" || local == "" || recorded != local {
		return status.Error(codes.FailedPrecondition, "original storage backend mismatch")
	}
	return nil
}

func (s *FoghornGRPCServer) CreateVodOriginalDownload(ctx context.Context, req *sharedpb.CreateVodOriginalDownloadRequest) (*sharedpb.CreateVodOriginalDownloadResponse, error) {
	row, err := s.originalForTenant(ctx, req.GetTenantId(), req.GetArtifactHash())
	if err != nil {
		return nil, err
	}
	if s.s3Client == nil {
		return nil, status.Error(codes.Unavailable, "original storage unavailable")
	}
	exists, size, _, err := s.s3Client.HeadObjectInfo(ctx, row.OriginalS3Key.String)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "verify original failed")
	}
	if !exists || size != row.OriginalSizeBytes.Int64 {
		return nil, status.Error(codes.FailedPrecondition, "original object missing or changed")
	}
	signer, ok := s.s3Client.(originalPresigner)
	if !ok {
		return nil, status.Error(codes.Unavailable, "original download unavailable")
	}
	url, err := signer.GeneratePresignedGETAttachment(ctx, row.OriginalS3Key.String, row.Filename, originalDownloadExpiry)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "original download unavailable")
	}
	return &sharedpb.CreateVodOriginalDownloadResponse{Url: url, ExpiresAt: timestamppb.New(time.Now().Add(originalDownloadExpiry)), SizeBytes: size, Filename: row.Filename}, nil
}

func (s *FoghornGRPCServer) DeleteVodOriginal(ctx context.Context, req *sharedpb.DeleteVodOriginalRequest) (*sharedpb.DeleteVodOriginalResponse, error) {
	initial, err := s.originalForTenant(ctx, req.GetTenantId(), req.GetArtifactHash())
	noOriginal := status.Code(err) == codes.FailedPrecondition && status.Convert(err).Message() == "original_not_kept"
	if err != nil && !noOriginal {
		return nil, err
	}
	if initial.Status.Valid && initial.Status.String != "ready" {
		return nil, status.Error(codes.FailedPrecondition, "processing must finish before deleting its original")
	}

	deleted := false
	err = database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		deleted = false
		q := foghorndb.New(tx)
		// Lock the artifact before clearing its original, the same order used by processing and sync.
		hash, lockErr := q.LockVodOriginalArtifact(ctx, foghorndb.LockVodOriginalArtifactParams{TenantID: req.GetTenantId(), ArtifactHash: req.GetArtifactHash()})
		if lockErr != nil {
			return lockErr
		}

		row, readErr := q.GetVodOriginal(ctx, foghorndb.GetVodOriginalParams{TenantID: req.GetTenantId(), ArtifactHash: hash})
		if readErr != nil {
			return readErr
		}
		if row.Status.String != "ready" {
			return status.Error(codes.FailedPrecondition, "processing must finish before deleting its original")
		}
		if row.OriginalS3Key.String == "" {
			return nil
		}
		if row.ActiveObjectKey.String == row.OriginalS3Key.String {
			return status.Error(codes.FailedPrecondition, "processed video must finish syncing before deleting its original")
		}
		affected, clearErr := q.ClearVodOriginal(ctx, foghorndb.ClearVodOriginalParams{TenantID: req.GetTenantId(), ArtifactHash: hash, ObjectKey: row.OriginalS3Key.String})
		if clearErr != nil {
			return clearErr
		}
		if affected != 1 {
			return fmt.Errorf("original changed during deletion")
		}
		if cleanupErr := control.EnqueueStagingCleanupTx(ctx, tx, row.OriginalS3Key.String); cleanupErr != nil {
			return cleanupErr
		}
		if catalogErr := q.TouchVodOriginalCatalog(ctx, foghorndb.TouchVodOriginalCatalogParams{TenantID: req.GetTenantId(), ArtifactHash: hash}); catalogErr != nil {
			return catalogErr
		}
		deleted = true
		return nil
	})
	if err != nil {
		if status.Code(err) == codes.FailedPrecondition {
			return nil, err
		}
		return nil, status.Error(codes.Internal, "delete original failed")
	}
	return &sharedpb.DeleteVodOriginalResponse{Success: deleted}, nil
}

func (s *FoghornGRPCServer) requireOriginalRetentionReady(ctx context.Context) error {
	q := foghorndb.New(s.db)
	activated, err := q.IsVodOriginalRetentionActivated(ctx)
	if err != nil {
		return status.Error(codes.Unavailable, "could not read original retention activation; retry the upload")
	}
	if !activated {
		return status.Error(codes.FailedPrecondition, "original retention is waiting for release postdeploy activation")
	}
	return nil
}

// A stored acceptance owns its retention choice. Replaying it does not require
// the activation gate for a new upload, nor create another command or multipart.
func (s *FoghornGRPCServer) replayVodOriginalUpload(ctx context.Context, req *sharedpb.CreateVodUploadRequest) (*sharedpb.CreateVodUploadResponse, bool, error) {
	if req.GetVodHash() == "" {
		return nil, false, nil
	}
	committed := false
	if req.GetRequestId() != "" {
		row, err := foghorndb.New(s.db).GetArtifactCreationCommandStatus(ctx, foghorndb.GetArtifactCreationCommandStatusParams{RequestID: req.GetRequestId(), TenantID: req.GetTenantId()})
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, status.Error(codes.Unavailable, "could not read original upload retry state; retry the upload")
		}
		if row.Kind != "vod" || row.ArtifactHash != req.GetVodHash() {
			return nil, false, status.Error(codes.FailedPrecondition, "request_id already used for a different artifact")
		}
		switch row.Status {
		case "rejected":
			return nil, false, status.Error(codes.FailedPrecondition, "VOD creation was terminally rejected")
		case "committed":
			committed = true
		default:
			return nil, false, nil
		}
	}
	partSize, _ := storage.CalculatePartSize(req.GetSizeBytes())
	replay, found, err := s.existingVodUploadResult(ctx, req.GetTenantId(), req.GetVodHash(), req.GetPlaybackId(), partSize)
	if err != nil {
		return nil, true, err
	}
	if !found {
		if committed {
			return nil, true, status.Error(codes.Internal, "committed VOD command has no artifact row")
		}
		return nil, true, nil
	}
	return replay, true, nil
}
