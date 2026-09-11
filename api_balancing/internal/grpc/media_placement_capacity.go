package grpc

import (
	"context"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/federation"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SetPlacementCapacityObserver is configured before the server starts accepting RPCs.
func (s *FoghornGRPCServer) SetPlacementCapacityObserver(observer *balancer.PlacementCapacityObserver) {
	s.capacityObserver = observer
}

// SetPlacementPushSourceObserver is configured before accepting RPCs.
func (s *FoghornGRPCServer) SetPlacementPushSourceObserver(observer *federation.PlacementPushSourceObserver) {
	s.pushSourceObserver = observer
}

func (s *FoghornGRPCServer) ObserveMediaPlacementPushSource(ctx context.Context, req *placementpb.PushSourcePreviewQuery) (*placementpb.PushSourcePreviewObservation, error) {
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "source observations require service identity")
	}
	return s.pushSourceObserver.Observe(ctx, req)
}

func (s *FoghornGRPCServer) ObserveMediaPlacementCapacity(ctx context.Context, req *placementpb.CapacityPreviewQuery) (*placementpb.CapacityPreviewObservation, error) {
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "capacity observations require service identity")
	}
	return s.capacityObserver.Observe(ctx, req)
}
