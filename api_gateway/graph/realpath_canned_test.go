package graph

import (
	"context"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"frameworks/api_gateway/internal/demo"
)

// Canned client responses for the real-path sweep. Reusing the demo generators
// gives realistic, non-empty protos so each resolver's response→GraphQL mapping
// runs (rather than panicking at the nil-interface call). Add overrides here to
// extend real-path coverage to more resolver families.

func (fakeCommodore) ListStreams(_ context.Context, _ *commonpb.CursorPaginationRequest) (*commodorepb.ListStreamsResponse, error) {
	return &commodorepb.ListStreamsResponse{Streams: demo.GenerateStreams()}, nil
}

func (fakeCommodore) GetStream(_ context.Context, _ string) (*commodorepb.Stream, error) {
	return demo.GenerateStreams()[0], nil
}

func (fakePurser) GetBillingTiers(_ context.Context, _ bool, _ *commonpb.CursorPaginationRequest) (*purserpb.GetBillingTiersResponse, error) {
	return &purserpb.GetBillingTiersResponse{Tiers: demo.GenerateBillingTiers()}, nil
}

func (fakePurser) GetBillingStatus(_ context.Context, _ string) (*purserpb.BillingStatusResponse, error) {
	return demo.GenerateBillingStatus(), nil
}

func (fakeQuartermaster) ListClustersByOwner(_ context.Context, _ string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
	return &quartermasterpb.ListClustersResponse{Clusters: demo.GenerateInfrastructureClusters()}, nil
}
