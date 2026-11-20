package grpc

import (
	"context"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *FoghornGRPCServer) GetStreamIngestStates(ctx context.Context, req *pb.GetStreamIngestStatesRequest) (*pb.GetStreamIngestStatesResponse, error) {
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "ingest-state reads require service authentication")
	}
	if _, err := uuid.Parse(req.GetTenantId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	if len(req.GetStreamIds()) > 500 {
		return nil, status.Error(codes.InvalidArgument, "at most 500 streams may be read")
	}
	response := &pb.GetStreamIngestStatesResponse{States: make(map[string]*pb.StreamIngest, len(req.GetStreamIds()))}
	for _, id := range req.GetStreamIds() {
		if _, err := uuid.Parse(id); err != nil {
			return nil, status.Error(codes.InvalidArgument, "stream_ids must contain UUIDs")
		}
		response.States[id] = &pb.StreamIngest{State: pb.StreamIngestState_STREAM_INGEST_STATE_IDLE}
	}
	if len(response.States) == 0 {
		return response, nil
	}
	rows, err := foghorndb.New(s.db).GetStreamIngestStates(ctx, foghorndb.GetStreamIngestStatesParams{TenantID: req.GetTenantId(), StreamIds: req.GetStreamIds()})
	if err != nil {
		return nil, status.Error(codes.Unavailable, "ingest session catalog unavailable")
	}
	for _, row := range rows {
		view := &pb.StreamIngest{State: pb.StreamIngestState_STREAM_INGEST_STATE_CONNECTED, SessionId: row.ID, ConnectedAt: timestamppb.New(row.StartedAt)}
		if row.PlayableAt.Valid {
			view.LiveSince = timestamppb.New(row.PlayableAt.Time)
			view.State = pb.StreamIngestState_STREAM_INGEST_STATE_LIVE
			view.IsLive = true
		}
		if row.EndedAt.Valid {
			view.State = pb.StreamIngestState_STREAM_INGEST_STATE_IDLE
			view.IsLive = false
			view.EndedAt = timestamppb.New(row.EndedAt.Time)
			if !row.IsPull {
				view.ResumableUntil = timestamppb.New(row.EndedAt.Time.Add(mist.LiveInputTimeout))
			}
		}
		if _, requested := response.States[row.StreamID.String]; requested {
			response.States[row.StreamID.String] = view
		}
	}
	return response, nil
}
