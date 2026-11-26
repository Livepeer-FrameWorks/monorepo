package grpc

import (
	"context"
	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetTenantMediaFootprint reads origin-owned media, never a warm federated copy.
// Errors are uncertainty; callers must retain prepaid pricing until proof succeeds.
func (s *FoghornGRPCServer) GetTenantMediaFootprint(ctx context.Context, req *foghornpb.GetTenantMediaFootprintRequest) (*foghornpb.TenantMediaFootprint, error) {
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "media footprint requires service authentication")
	}
	if _, err := uuid.Parse(req.GetTenantId()); err != nil || req.GetRetentionLimitDays() < 0 {
		return nil, status.Error(codes.InvalidArgument, "valid tenant_id and non-negative retention limit required")
	}
	footprint, err := foghorndb.New(s.db).GetTenantMediaFootprint(ctx, foghorndb.GetTenantMediaFootprintParams{TenantID: req.GetTenantId(), RetentionLimitDays: req.GetRetentionLimitDays()})
	if err != nil {
		return nil, status.Error(codes.Unavailable, "media footprint unavailable")
	}
	viewers, err := state.DefaultTenantCapacity().CountViewersStrict(ctx, req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.Unavailable, "viewer authority unavailable")
	}
	return &foghornpb.TenantMediaFootprint{StorageBytes: footprint.StorageBytes, PendingArtifacts: footprint.PendingArtifacts, IncompatibleRetention: footprint.IncompatibleRetention, ActiveIngest: footprint.ActiveIngest, ActiveViewers: viewers}, nil
}
