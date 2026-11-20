package grpc

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *CommodoreServer) resolveKeepVodOriginal(ctx context.Context, tenantID string, override *bool) (bool, error) {
	if override != nil {
		return *override, nil
	}
	keep, err := commodoredb.New(s.db).GetVodOriginalPolicy(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return !keep.Valid || keep.Bool, nil
}

func (s *CommodoreServer) SetVodOriginalPolicy(ctx context.Context, req *commodorepb.SetVodOriginalPolicyRequest) (*commodorepb.GetMediaRetentionPolicyResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetTenantId() != "" && req.GetTenantId() != tenantID {
		return nil, status.Error(codes.PermissionDenied, "tenant mismatch")
	}
	if authErr := requireRetentionManagement(ctx, tenantID); authErr != nil {
		return nil, authErr
	}
	err = s.withEventTx(ctx, func(tx *sql.Tx) error {
		if writeErr := commodoredb.New(tx).UpsertVodOriginalPolicy(ctx, commodoredb.UpsertVodOriginalPolicyParams{TenantID: tenantID, KeepVodOriginals: sql.NullBool{Bool: req.GetKeepVodOriginals(), Valid: req.KeepVodOriginals != nil}, UpdatedBy: userID}); writeErr != nil {
			return writeErr
		}
		return s.enqueueEventTx(ctx, tx, s.buildRetentionPolicyEvent(tenantID, userID, "keep_vod_originals"), nil)
	})
	if err != nil {
		return nil, status.Error(codes.Internal, "original policy write failed")
	}
	policy, err := s.GetMediaRetentionPolicy(ctx, &commodorepb.GetMediaRetentionPolicyRequest{TenantId: tenantID})
	if err != nil {
		return nil, err
	}
	return policy, nil
}

func (s *CommodoreServer) originalTarget(ctx context.Context, requestedTenant, identifier string) (string, *assetRetentionTarget, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return "", nil, err
	}
	if requestedTenant != "" && requestedTenant != tenantID {
		return "", nil, status.Error(codes.PermissionDenied, "tenant mismatch")
	}
	row, err := commodoredb.New(s.db).ResolveVODTenantArtifact(ctx, commodoredb.ResolveVODTenantArtifactParams{TenantID: tenantID, Identifier: identifier})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, status.Error(codes.NotFound, "VOD not found")
	}
	if err != nil {
		return "", nil, status.Error(codes.Internal, "VOD lookup failed")
	}
	return tenantID, &assetRetentionTarget{hash: row.VodHash, originClusterID: row.OriginClusterID, artifactType: "vod"}, nil
}

func (s *CommodoreServer) CreateVodOriginalDownload(ctx context.Context, req *sharedpb.CreateVodOriginalDownloadRequest) (*sharedpb.CreateVodOriginalDownloadResponse, error) {
	tenantID, target, err := s.originalTarget(ctx, req.GetTenantId(), req.GetArtifactHash())
	if err != nil {
		return nil, err
	}
	// The database bucket is shared by every gateway and Commodore replica.
	// Three-second spacing with twelve seconds of debt allows a burst of five.
	grantErr := database.RetryPostgres(ctx, 5, 10*time.Millisecond, func() error {
		_, consumeErr := commodoredb.New(s.db).ConsumeVodOriginalDownloadGrant(ctx, commodoredb.ConsumeVodOriginalDownloadGrantParams{
			TenantID: tenantID, IntervalSeconds: 3, BurstSeconds: 12,
		})
		return consumeErr
	})
	if errors.Is(grantErr, sql.ErrNoRows) {
		return nil, status.Error(codes.ResourceExhausted, "Too many original download requests; try again shortly")
	} else if grantErr != nil {
		return nil, status.Error(codes.Unavailable, "original download admission unavailable")
	}
	client, err := s.resolveFoghornForArtifact(ctx, tenantID, target.originClusterID)
	if err != nil {
		return nil, err
	}
	resp, err := client.CreateVodOriginalDownload(ctx, &sharedpb.CreateVodOriginalDownloadRequest{TenantId: tenantID, ArtifactHash: target.hash, Actor: s.requestActor(ctx)})
	if err != nil {
		return nil, err
	}
	err = s.withEventTx(ctx, func(tx *sql.Tx) error {
		return s.enqueueEventTx(ctx, tx, s.buildRetentionArtifactEvent("media.vod_original.download_issued", tenantID, ctxkeys.GetUserID(ctx), target, time.Time{}, true), nil)
	})
	if err != nil {
		return nil, status.Error(codes.Internal, "record original download failed")
	}
	return resp, nil
}

func (s *CommodoreServer) DeleteVodOriginal(ctx context.Context, req *sharedpb.DeleteVodOriginalRequest) (*sharedpb.DeleteVodOriginalResponse, error) {
	tenantID, target, err := s.originalTarget(ctx, req.GetTenantId(), req.GetArtifactHash())
	if err != nil {
		return nil, err
	}
	client, err := s.resolveFoghornForArtifact(ctx, tenantID, target.originClusterID)
	if err != nil {
		return nil, err
	}
	resp, err := client.DeleteVodOriginal(ctx, &sharedpb.DeleteVodOriginalRequest{TenantId: tenantID, ArtifactHash: target.hash, Actor: s.requestActor(ctx)})
	if err != nil {
		return nil, err
	}
	if !resp.GetSuccess() {
		return resp, nil
	}

	err = s.withEventTx(ctx, func(tx *sql.Tx) error {
		return s.enqueueEventTx(ctx, tx, s.buildRetentionArtifactEvent("media.vod_original.deleted", tenantID, ctxkeys.GetUserID(ctx), target, time.Time{}, true), nil)
	})
	if err != nil {
		return nil, status.Error(codes.Internal, "record original deletion failed")
	}
	return resp, nil
}
