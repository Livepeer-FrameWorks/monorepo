package foghorn

import (
	"context"
	"time"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

func (c *GRPCClient) ObserveMediaPlacementCapacity(ctx context.Context, req *placementpb.CapacityPreviewQuery) (*placementpb.CapacityPreviewObservation, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	return c.placement.ObserveMediaPlacementCapacity(ctx, req)
}

func (c *GRPCClient) ObserveMediaPlacementPushSource(ctx context.Context, req *placementpb.PushSourcePreviewQuery) (*placementpb.PushSourcePreviewObservation, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	return c.placement.ObserveMediaPlacementPushSource(ctx, req)
}
