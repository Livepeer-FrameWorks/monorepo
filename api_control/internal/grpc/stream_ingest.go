package grpc

import (
	"context"
	"sort"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetStreamIngestStates keeps a cell outage local to the streams routed there.
func (s *CommodoreServer) GetStreamIngestStates(ctx context.Context, req *pb.GetStreamIngestStatesRequest) (*pb.GetStreamIngestStatesResponse, error) {
	user, tenant, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	if permissionErr := requireTenantStreamPermission(ctx, "streams:read"); permissionErr != nil {
		return nil, permissionErr
	}
	if req.GetTenantId() != "" && req.GetTenantId() != tenant {
		return nil, status.Error(codes.PermissionDenied, "tenant mismatch")
	}
	if len(req.GetStreamIds()) > 500 {
		return nil, status.Error(codes.InvalidArgument, "at most 500 streams may be read")
	}
	for _, id := range req.GetStreamIds() {
		if _, parseErr := uuid.Parse(id); parseErr != nil {
			return nil, status.Error(codes.InvalidArgument, "stream_ids must contain UUIDs")
		}
	}
	response := &pb.GetStreamIngestStatesResponse{States: map[string]*pb.StreamIngest{}}
	if len(req.GetStreamIds()) == 0 {
		return response, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := commodoredb.New(s.db).GetStreamIngestRoutes(ctx, commodoredb.GetStreamIngestRoutesParams{TenantID: tenant, UserID: user, StreamIds: req.GetStreamIds()})
	if err != nil {
		return nil, status.Error(codes.Unavailable, "stream routing catalog unavailable")
	}
	groups := map[string][]string{}
	var pulls []string
	pending := map[string]bool{}
	for _, row := range rows {
		if row.IngestMode == "pull" {
			pulls = append(pulls, row.ID)
			pending[row.ID] = true
		} else if row.ClusterID != "" {
			groups[row.ClusterID] = append(groups[row.ClusterID], row.ID)
			pending[row.ID] = true
		} else {
			response.States[row.ID] = &pb.StreamIngest{State: pb.StreamIngestState_STREAM_INGEST_STATE_IDLE}
		}
	}
	if len(pulls) > 0 {
		targets, inventoryErr := s.tenantControlFanoutTargets(ctx, tenant)
		if inventoryErr != nil {
			s.logger.WithError(inventoryErr).Warn("Pull ingest inventory incomplete; returning available cell states")
		}
		for _, target := range targets {
			groups[target.clusterID] = append(groups[target.clusterID], pulls...)
		}
	}

	targets := make([]clusterFanoutTarget, 0, len(groups))
	for cluster := range groups {
		targets = append(targets, clusterFanoutTarget{clusterID: cluster})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].clusterID < targets[j].clusterID })
	deliveries := dispatchTenantControlTargets(ctx, s, targets, func(callCtx context.Context, client *foghornclient.GRPCClient, target clusterFanoutTarget) (*pb.GetStreamIngestStatesResponse, error) {
		return client.GetStreamIngestStates(callCtx, &pb.GetStreamIngestStatesRequest{TenantId: tenant, StreamIds: groups[target.clusterID]})
	})
	for index, delivery := range deliveries {
		if delivery.err != nil {
			continue
		}
		for _, id := range groups[targets[index].clusterID] {
			incoming := delivery.response.GetStates()[id]
			if incoming == nil || incoming.GetState() == pb.StreamIngestState_STREAM_INGEST_STATE_UNSPECIFIED {
				continue
			}
			current := response.States[id]
			if current == nil || incoming.GetState() > current.GetState() || (incoming.GetState() == current.GetState() && incoming.GetConnectedAt().AsTime().After(current.GetConnectedAt().AsTime())) {
				response.States[id] = incoming
			}
			delete(pending, id)
		}
	}
	for id := range pending {
		response.UnavailableStreamIds = append(response.UnavailableStreamIds, id)
	}
	sort.Strings(response.UnavailableStreamIds)
	return response, nil
}
