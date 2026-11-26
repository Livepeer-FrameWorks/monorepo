package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"frameworks/api_control/internal/database/commodoredb"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"math"
	"sort"
	"strings"
	"time"
)

type tenantFreeResourceSource interface {
	OwnerCells(context.Context, string) ([]string, error)
	Footprint(context.Context, string, string, int32) (*foghornpb.TenantMediaFootprint, error)
}
type liveTenantFreeResources struct{ server *CommodoreServer }

func (l liveTenantFreeResources) OwnerCells(ctx context.Context, tenantID string) ([]string, error) {
	q := commodoredb.New(l.server.db)
	cells, err := q.ListTenantMediaOwnerCells(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	historical, err := q.ListAccountIngestCells(ctx, commodoredb.ListAccountIngestCellsParams{TenantID: tenantID, StreamID: ""})
	if err != nil {
		return nil, err
	}
	cells = append(cells, historical...)
	route, err := l.server.resolveClusterRouteForTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if route == nil || route.clusterID == "" {
		return nil, errors.New("tenant media route unavailable")
	}
	return append(cells, route.clusterID), nil
}
func (l liveTenantFreeResources) Footprint(ctx context.Context, cell, tenant string, days int32) (*foghornpb.TenantMediaFootprint, error) {
	client, err := l.server.resolveFoghornForClusterDirect(ctx, cell)
	if err != nil {
		return nil, err
	}
	return client.GetTenantMediaFootprint(ctx, tenant, days)
}

// GetTenantFreeEligibility never rewrites an asset or ends a session. Every
// recorded owner must answer; a partial or older fleet keeps the paid model.
func (s *CommodoreServer) GetTenantFreeEligibility(ctx context.Context, req *commodorepb.GetTenantFreeEligibilityRequest) (*commodorepb.GetTenantFreeEligibilityResponse, error) {
	if err := requireServiceAuthentication(ctx, "GetTenantFreeEligibility"); err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(req.GetTenantId()); err != nil || req.GetStorageLimitBytes() < 0 || req.GetRetentionLimitDays() < 0 {
		return nil, status.Error(codes.InvalidArgument, "valid tenant and non-negative Free limits required")
	}
	identity, err := commodoredb.New(s.db).GetTenantPrimaryUser(ctx, req.GetTenantId())
	if errors.Is(err, sql.ErrNoRows) || err == nil && !identity.FreeTierEligible {
		return &commodorepb.GetTenantFreeEligibilityResponse{Reason: "VERIFIED_EMAIL_REQUIRED"}, nil
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "billing identity unavailable")
	}
	pending, err := commodoredb.New(s.db).HasPendingTenantMediaCreation(ctx, req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.Unavailable, "media creation state unavailable")
	}
	if pending {
		return &commodorepb.GetTenantFreeEligibilityResponse{Reason: "MEDIA_WORK_PENDING"}, nil
	}
	source := s.freeTierResources
	if source == nil {
		source = liveTenantFreeResources{server: s}
	}
	result, err := checkTenantFreeResources(ctx, req, source)
	if err != nil {
		s.logger.WithError(err).WithField("tenant_id", req.GetTenantId()).Warn("Free resource proof unavailable")
		return nil, status.Error(codes.Unavailable, "cannot verify Free resource limits")
	}
	return result, nil
}

func checkTenantFreeResources(ctx context.Context, req *commodorepb.GetTenantFreeEligibilityRequest, source tenantFreeResourceSource) (*commodorepb.GetTenantFreeEligibilityResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cells, err := source.OwnerCells(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	unique := map[string]bool{}
	for _, cell := range cells {
		cell = strings.TrimSpace(cell)
		if cell == "" {
			return &commodorepb.GetTenantFreeEligibilityResponse{Reason: "MEDIA_OWNERSHIP_UNKNOWN"}, nil
		}
		unique[cell] = true
	}
	if len(unique) == 0 {
		return nil, errors.New("no media owner proof")
	}
	cells = cells[:0]
	for cell := range unique {
		cells = append(cells, cell)
	}
	sort.Strings(cells)
	footprints := make([]*foghornpb.TenantMediaFootprint, len(cells))
	group, callCtx := errgroup.WithContext(ctx)
	group.SetLimit(4)
	for i, cell := range cells {
		group.Go(func() error {
			cellCtx, cancel := context.WithTimeout(callCtx, 5*time.Second)
			defer cancel()
			footprint, readErr := source.Footprint(cellCtx, cell, req.GetTenantId(), req.GetRetentionLimitDays())
			if readErr != nil {
				return fmt.Errorf("read owner %s: %w", cell, readErr)
			}
			if footprint == nil {
				return errors.New("owner returned no footprint")
			}
			footprints[i] = footprint
			return nil
		})
	}
	if err = group.Wait(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := &commodorepb.GetTenantFreeEligibilityResponse{}
	pending, retention, busy := false, false, false
	for _, f := range footprints {
		if f.StorageBytes < 0 || f.PendingArtifacts < 0 || f.IncompatibleRetention < 0 || f.ActiveIngest < 0 || f.ActiveViewers < 0 || f.StorageBytes > math.MaxInt64-result.StorageBytes {
			return nil, errors.New("invalid resource footprint")
		}
		result.StorageBytes += f.StorageBytes
		pending = pending || f.PendingArtifacts > 0
		retention = retention || f.IncompatibleRetention > 0
		busy = busy || f.ActiveIngest > 0 || f.ActiveViewers > 0
	}
	switch {
	case req.GetStorageLimitBytes() > 0 && result.StorageBytes > req.GetStorageLimitBytes():
		result.Reason = "STORAGE_LIMIT_REACHED"
	case retention:
		result.Reason = "RETENTION_LIMIT_REACHED"
	case pending:
		result.Reason = "MEDIA_WORK_PENDING"
	case busy:
		result.Reason = "MEDIA_SESSIONS_ACTIVE"
	default:
		result.Eligible = true
	}
	return result, nil
}
