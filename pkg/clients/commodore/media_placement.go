package commodore

import (
	"context"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

func (c *GRPCClient) PreviewMediaPlacement(ctx context.Context, req *placementpb.PreviewRequest) (*placementpb.Preview, error) {
	return c.internal.PreviewMediaPlacement(ctx, req)
}

func (c *GRPCClient) GetMediaPlacementPolicy(ctx context.Context, req *placementpb.GetPolicyRequest) (*placementpb.PolicyState, error) {
	return c.internal.GetMediaPlacementPolicy(ctx, req)
}

func (c *GRPCClient) GetMediaPlacementOptions(ctx context.Context, req *placementpb.GetOptionsRequest) (*placementpb.Options, error) {
	return c.internal.GetMediaPlacementOptions(ctx, req)
}

func (c *GRPCClient) ReviewMediaPlacementChange(ctx context.Context, req *placementpb.ReviewChangeRequest) (*placementpb.Review, error) {
	return c.internal.ReviewMediaPlacementChange(ctx, req)
}

func (c *GRPCClient) ApplyMediaPlacementChange(ctx context.Context, req *placementpb.ApplyChangeRequest) (*placementpb.Change, error) {
	return c.internal.ApplyMediaPlacementChange(ctx, req)
}

func (c *GRPCClient) GetMediaPlacementChange(ctx context.Context, req *placementpb.GetChangeRequest) (*placementpb.Change, error) {
	return c.internal.GetMediaPlacementChange(ctx, req)
}
