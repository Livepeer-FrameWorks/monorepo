package grpc

import (
	"context"
	"database/sql"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MarkArtifactThumbnailsReady flips has_thumbnails to TRUE and stamps
// storage_cluster_id on the matching artifact row. Called by Foghorn from
// processThumbnailUploaded — the confirmation site that knows the bytes
// actually landed. Idempotent at the DB level via the WHERE clause.
func (s *CommodoreServer) MarkArtifactThumbnailsReady(ctx context.Context, req *pb.MarkArtifactThumbnailsReadyRequest) (*pb.MarkArtifactThumbnailsReadyResponse, error) {
	tenantID := req.GetTenantId()
	assetKey := req.GetAssetKey()
	cluster := req.GetStorageClusterId()
	if tenantID == "" || assetKey == "" || cluster == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id, asset_key, and storage_cluster_id are required")
	}

	table, keyCol, err := artifactAssetTable(req.GetAssetType())
	if err != nil {
		return nil, err
	}

	// Idempotent: only write when something would actually change.
	query := `UPDATE ` + table + `
		SET has_thumbnails = TRUE,
		    storage_cluster_id = $1,
		    updated_at = NOW()
		WHERE tenant_id = $2::uuid
		  AND ` + keyCol + ` = $3
		  AND (has_thumbnails = FALSE OR storage_cluster_id IS DISTINCT FROM $1)`
	res, execErr := s.db.ExecContext(ctx, query, cluster, tenantID, assetKey)
	if execErr != nil {
		s.logger.WithError(execErr).WithFields(logging.Fields{
			"tenant_id":  tenantID,
			"asset_type": req.GetAssetType().String(),
			"asset_key":  assetKey,
		}).Error("MarkArtifactThumbnailsReady failed")
		return nil, status.Errorf(codes.Internal, "update failed: %v", execErr)
	}
	rows, _ := res.RowsAffected()
	return &pb.MarkArtifactThumbnailsReadyResponse{Updated: rows > 0}, nil
}

// UpdateArtifactStorageCluster updates storage_cluster_id only. It never
// flips has_thumbnails — a storage move on a thumbnail-less artifact must
// not falsely flip readiness.
func (s *CommodoreServer) UpdateArtifactStorageCluster(ctx context.Context, req *pb.UpdateArtifactStorageClusterRequest) (*pb.UpdateArtifactStorageClusterResponse, error) {
	tenantID := req.GetTenantId()
	assetKey := req.GetAssetKey()
	cluster := req.GetStorageClusterId()
	if tenantID == "" || assetKey == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and asset_key are required")
	}

	table, keyCol, err := artifactAssetTable(req.GetAssetType())
	if err != nil {
		return nil, err
	}

	storageArg := sql.NullString{String: cluster, Valid: cluster != ""}
	query := `UPDATE ` + table + `
		SET storage_cluster_id = $1,
		    updated_at = NOW()
		WHERE tenant_id = $2::uuid
		  AND ` + keyCol + ` = $3
		  AND storage_cluster_id IS DISTINCT FROM $1`
	res, execErr := s.db.ExecContext(ctx, query, storageArg, tenantID, assetKey)
	if execErr != nil {
		s.logger.WithError(execErr).WithFields(logging.Fields{
			"tenant_id":  tenantID,
			"asset_type": req.GetAssetType().String(),
			"asset_key":  assetKey,
		}).Error("UpdateArtifactStorageCluster failed")
		return nil, status.Errorf(codes.Internal, "update failed: %v", execErr)
	}
	rows, _ := res.RowsAffected()
	return &pb.UpdateArtifactStorageClusterResponse{Updated: rows > 0}, nil
}

// artifactAssetTable maps the proto enum to the registry table and its
// hash column. clip_hash, dvr_hash, vod_hash are the asset keys used at
// the Foghorn thumbnail-upload path.
func artifactAssetTable(t pb.ArtifactAssetType) (table, keyCol string, err error) {
	switch t {
	case pb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_CLIP:
		return "commodore.clips", "clip_hash", nil
	case pb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_DVR:
		return "commodore.dvr_recordings", "dvr_hash", nil
	case pb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD:
		return "commodore.vod_assets", "vod_hash", nil
	default:
		return "", "", status.Errorf(codes.InvalidArgument, "unsupported asset_type: %s", t.String())
	}
}
