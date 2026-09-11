package clientstest

import (
	"context"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

func (f *FakeCommodore) PreviewMediaPlacement(ctx context.Context, req *placementpb.PreviewRequest) (*placementpb.Preview, error) {
	f.Calls++
	if f.PreviewMediaPlacementFn == nil {
		panic("FakeCommodore.PreviewMediaPlacement not stubbed")
	}
	return f.PreviewMediaPlacementFn(ctx, req)
}

func (f *FakeCommodore) GetMediaPlacementOptions(ctx context.Context, req *placementpb.GetOptionsRequest) (*placementpb.Options, error) {
	f.Calls++
	if f.GetMediaPlacementOptionsFn == nil {
		panic("FakeCommodore.GetMediaPlacementOptions not stubbed")
	}
	return f.GetMediaPlacementOptionsFn(ctx, req)
}

func (f *FakeCommodore) GetMediaPlacementPolicy(ctx context.Context, req *placementpb.GetPolicyRequest) (*placementpb.PolicyState, error) {
	f.Calls++
	if f.GetMediaPlacementPolicyFn == nil {
		panic("FakeCommodore.GetMediaPlacementPolicy not stubbed")
	}
	return f.GetMediaPlacementPolicyFn(ctx, req)
}

func (f *FakeCommodore) ReviewMediaPlacementChange(ctx context.Context, req *placementpb.ReviewChangeRequest) (*placementpb.Review, error) {
	f.Calls++
	if f.ReviewMediaPlacementChangeFn == nil {
		panic("FakeCommodore.ReviewMediaPlacementChange not stubbed")
	}
	return f.ReviewMediaPlacementChangeFn(ctx, req)
}

func (f *FakeCommodore) ApplyMediaPlacementChange(ctx context.Context, req *placementpb.ApplyChangeRequest) (*placementpb.Change, error) {
	f.Calls++
	if f.ApplyMediaPlacementChangeFn == nil {
		panic("FakeCommodore.ApplyMediaPlacementChange not stubbed")
	}
	return f.ApplyMediaPlacementChangeFn(ctx, req)
}

func (f *FakeCommodore) GetMediaPlacementChange(ctx context.Context, req *placementpb.GetChangeRequest) (*placementpb.Change, error) {
	f.Calls++
	if f.GetMediaPlacementChangeFn == nil {
		panic("FakeCommodore.GetMediaPlacementChange not stubbed")
	}
	return f.GetMediaPlacementChangeFn(ctx, req)
}
