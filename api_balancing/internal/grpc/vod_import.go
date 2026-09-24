package grpc

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path"
	"strings"
	"time"

	"frameworks/api_balancing/internal/artifactoutbox"
	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/jobs"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// ImportVodAsset records a VOD imported from a URL and queues its processing.
// Foghorn stores only the source URL: the processing node's MistServer reads
// the asset through its Helmsman relay, whose RelayResolve for the upload
// returns the source URL (fillUploadResolve) instead of an S3 object. The
// attempt is recorded in the creation ledger like CreateVodUpload.
func (s *FoghornGRPCServer) ImportVodAsset(ctx context.Context, req *sharedpb.ImportVodAssetRequest) (resp *sharedpb.ImportVodAssetResponse, err error) {
	var prog creationLedgerProgress
	defer func() {
		err = s.finalizeCreationCommand(req.GetRequestId(), req.GetTenantId(), "vod", req.GetVodHash(), &prog, err)
	}()
	return s.importVodAssetImpl(ctx, req, &prog)
}

func (s *FoghornGRPCServer) importVodAssetImpl(ctx context.Context, req *sharedpb.ImportVodAssetRequest, prog *creationLedgerProgress) (*sharedpb.ImportVodAssetResponse, error) {
	tenantID, artifactHash := req.GetTenantId(), strings.TrimSpace(req.GetVodHash())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if artifactHash == "" || req.GetInternalName() == "" {
		return nil, status.Error(codes.InvalidArgument, "vod_hash and internal_name are required")
	}
	source, err := url.Parse(strings.TrimSpace(req.GetSourceUrl()))
	if err != nil || (source.Scheme != "http" && source.Scheme != "https") || source.Hostname() == "" || source.User != nil {
		return nil, status.Error(codes.InvalidArgument, "source_url must be an http or https URL without credentials")
	}
	filename := strings.TrimSpace(req.GetFilename())
	format := strings.TrimPrefix(strings.ToLower(path.Ext(filename)), ".")
	if filename == "" || format == "" {
		return nil, status.Error(codes.InvalidArgument, "filename with a video extension is required")
	}

	acceptState, acceptErr := s.recordCreationCommandAcceptedDurable(ctx, req.GetRequestId(), tenantID, "vod", artifactHash, prog)
	if acceptErr != nil {
		if errors.Is(acceptErr, errCreationCommandIdentityMismatch) {
			return nil, status.Error(codes.FailedPrecondition, "request_id already used for a different artifact")
		}
		return nil, status.Errorf(codes.Unavailable, "failed to record VOD import attempt: %v", acceptErr)
	}
	switch acceptState {
	case creationCommandRejected:
		return nil, status.Error(codes.FailedPrecondition, "VOD import was terminally rejected")
	case creationCommandCommitted:
		return s.importedVodResult(ctx, artifactHash)
	}
	// A retry after a lost response finds the import already recorded.
	if _, getErr := foghorndb.New(s.db).GetImportedVodForTenant(ctx, foghorndb.GetImportedVodForTenantParams{
		ArtifactHash: artifactHash, TenantID: tenantID,
	}); getErr == nil {
		if ensErr := s.ensureCreationCommandCommitted(ctx, req.GetRequestId(), tenantID, "vod", artifactHash, prog); ensErr != nil {
			return nil, status.Errorf(codes.Unavailable, "failed to finalize VOD import command: %v", ensErr)
		}
		return s.importedVodResult(ctx, artifactHash)
	} else if !errors.Is(getErr, sql.ErrNoRows) {
		return nil, status.Errorf(codes.Internal, "failed to check existing VOD import: %v", getErr)
	}

	// The size is unknown until processing, so only the at-or-over-cap rule
	// applies here; the processed output is metered like any upload's.
	if capErr := s.checkStorageEntitlement(ctx, tenantID, 0); capErr != nil {
		return nil, capErr
	}
	// The processed output is stored on the cluster that accepted the import (req.ClusterId, persisted as
	// origin_cluster_id); the processing job only runs on that cluster's nodes.
	if err := s.vodOriginStorage(ctx, tenantID, req.GetClusterId()); err != nil {
		return nil, err
	}
	if s.s3Client == nil {
		return nil, status.Error(codes.FailedPrecondition, "S3 storage not configured")
	}
	retentionUntil := resolveArtifactInitialRetention(ctx, s.purserClient, tenantID, req.RetentionDays, 0 /* infinite VOD default */, s.logger)
	contentType := importContentType(format)

	requested := &ipcpb.VodLifecycleData{
		Status:      ipcpb.VodLifecycleData_STATUS_REQUESTED,
		VodHash:     artifactHash,
		Filename:    &filename,
		ContentType: &contentType,
		TenantId:    &tenantID,
		StartedAt:   proto.Int64(time.Now().Unix()),
	}
	processing := &ipcpb.VodLifecycleData{
		Status:      ipcpb.VodLifecycleData_STATUS_PROCESSING,
		VodHash:     artifactHash,
		TenantId:    &tenantID,
		CompletedAt: proto.Int64(time.Now().Unix()),
	}
	if userID := req.GetUserId(); userID != "" {
		requested.UserId = &userID
		processing.UserId = &userID
	}
	if cid := req.GetClusterId(); cid != "" {
		requested.OriginClusterId = &cid
		requested.ServingClusterId = &cid
	}

	// The artifact, its source, the processing job, and both upload events
	// commit together: an import is either fully queued or not recorded.
	if txErr := s.withArtifactLifecycleTx(ctx, func(tx *sql.Tx) error {
		queries := foghorndb.New(tx)
		if execErr := queries.InsertImportedVodArtifact(ctx, foghorndb.InsertImportedVodArtifactParams{
			ArtifactHash: artifactHash, InternalName: sql.NullString{String: req.GetInternalName(), Valid: true},
			TenantID: tenantID, UserID: req.GetUserId(), Format: sql.NullString{String: format, Valid: true},
			OriginClusterID: sql.NullString{String: req.GetClusterId(), Valid: true},
			RetentionUntil:  retentionUntil,
		}); execErr != nil {
			return execErr
		}
		if execErr := queries.InsertVodImportMetadata(ctx, foghorndb.InsertVodImportMetadataParams{
			ArtifactHash: artifactHash, Filename: sql.NullString{String: filename, Valid: true},
			Title: sql.NullString{String: req.GetTitle(), Valid: true}, Description: sql.NullString{String: req.GetDescription(), Valid: true},
			ContentType: sql.NullString{String: contentType, Valid: true}, SourceUrl: sql.NullString{String: source.String(), Valid: true},
		}); execErr != nil {
			return execErr
		}
		if _, jobErr := jobs.InsertProcessingJobWithSourceParamsTx(ctx, tx, tenantID, artifactHash, "process", nil, req.GetProcessesJson(), "", nil, ""); jobErr != nil {
			return jobErr
		}
		if cmdErr := recordCreationCommandCommitted(ctx, tx, req.GetRequestId(), tenantID, "vod", artifactHash); cmdErr != nil {
			return cmdErr
		}
		actor := requestedByActor(req.GetActor())
		if enqErr := artifactoutbox.EnqueueVodTransitionTx(ctx, tx, requested, &publicv1.UploadCreated{
			Artifact: artifactoutbox.UploadArtifact(artifactHash),
			Filename: filename,
		}, actor); enqErr != nil {
			return enqErr
		}
		return artifactoutbox.EnqueueVodTransitionTx(ctx, tx, processing, &publicv1.UploadCompleted{
			Artifact: artifactoutbox.UploadArtifact(artifactHash),
		}, actor)
	}); txErr != nil {
		s.logger.WithError(txErr).WithField("artifact_hash", artifactHash).Error("Failed to record VOD import")
		return nil, status.Error(codes.Internal, "failed to record import")
	}
	prog.committed = true
	jobs.NotifyProcessingJobQueued()

	s.logger.WithFields(logging.Fields{
		"artifact_hash": artifactHash,
		"tenant_id":     tenantID,
		"source_host":   source.Host,
	}).Info("Recorded VOD import and queued processing")
	return s.importedVodResult(ctx, artifactHash)
}

// importedVodResult returns the import's current asset view.
func (s *FoghornGRPCServer) importedVodResult(ctx context.Context, artifactHash string) (*sharedpb.ImportVodAssetResponse, error) {
	asset, err := s.getVodAssetInfo(ctx, artifactHash)
	if err != nil {
		s.logger.WithError(err).WithField("artifact_hash", artifactHash).Warn("VOD import recorded but its asset re-read failed; returning PROCESSING")
		return &sharedpb.ImportVodAssetResponse{Asset: &sharedpb.VodAssetInfo{
			ArtifactHash: artifactHash,
			Status:       sharedpb.VodStatus_VOD_STATUS_PROCESSING,
		}}, nil
	}
	return &sharedpb.ImportVodAssetResponse{Asset: asset}, nil
}

// importContentType is the MIME type recorded for an imported file, by its
// extension.
func importContentType(format string) string {
	switch format {
	case "mov":
		return "video/quicktime"
	case "mkv":
		return "video/x-matroska"
	case "webm":
		return "video/webm"
	case "ts":
		return "video/mp2t"
	default:
		return "video/mp4"
	}
}
