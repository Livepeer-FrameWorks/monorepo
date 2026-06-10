package clientstest

// FakeCommodore push-target / token / retention / playback / DVR-chapter methods
// (generated to match pkg/clients/commodore.Interface).

import (
	"context"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
)

func (f *FakeCommodore) GetMediaRetentionPolicy(ctx context.Context, req *commodorepb.GetMediaRetentionPolicyRequest) (*commodorepb.GetMediaRetentionPolicyResponse, error) {
	f.Calls++
	if f.GetMediaRetentionPolicyFn == nil {
		panic("FakeCommodore.GetMediaRetentionPolicy not stubbed")
	}
	return f.GetMediaRetentionPolicyFn(ctx, req)
}

func (f *FakeCommodore) SetMediaRetentionPolicy(ctx context.Context, req *commodorepb.SetMediaRetentionPolicyRequest) (*commodorepb.SetMediaRetentionPolicyResponse, error) {
	f.Calls++
	if f.SetMediaRetentionPolicyFn == nil {
		panic("FakeCommodore.SetMediaRetentionPolicy not stubbed")
	}
	return f.SetMediaRetentionPolicyFn(ctx, req)
}

func (f *FakeCommodore) UpdateAssetRetention(ctx context.Context, req *commodorepb.UpdateAssetRetentionRequest) (*commodorepb.UpdateAssetRetentionResponse, error) {
	f.Calls++
	if f.UpdateAssetRetentionFn == nil {
		panic("FakeCommodore.UpdateAssetRetention not stubbed")
	}
	return f.UpdateAssetRetentionFn(ctx, req)
}

func (f *FakeCommodore) ResetAssetRetention(ctx context.Context, req *commodorepb.ResetAssetRetentionRequest) (*commodorepb.UpdateAssetRetentionResponse, error) {
	f.Calls++
	if f.ResetAssetRetentionFn == nil {
		panic("FakeCommodore.ResetAssetRetention not stubbed")
	}
	return f.ResetAssetRetentionFn(ctx, req)
}

func (f *FakeCommodore) SetStreamRetentionOverrides(ctx context.Context, req *commodorepb.SetStreamRetentionOverridesRequest) (*commodorepb.SetStreamRetentionOverridesResponse, error) {
	f.Calls++
	if f.SetStreamRetentionOverridesFn == nil {
		panic("FakeCommodore.SetStreamRetentionOverrides not stubbed")
	}
	return f.SetStreamRetentionOverridesFn(ctx, req)
}

func (f *FakeCommodore) TestPlaybackAccess(ctx context.Context, req *foghorncontrolpb.TestPlaybackAccessRequest) (*foghorncontrolpb.TestPlaybackAccessResponse, error) {
	f.Calls++
	if f.TestPlaybackAccessFn == nil {
		panic("FakeCommodore.TestPlaybackAccess not stubbed")
	}
	return f.TestPlaybackAccessFn(ctx, req)
}

func (f *FakeCommodore) CreateAPIToken(ctx context.Context, req *commodorepb.CreateAPITokenRequest) (*commodorepb.CreateAPITokenResponse, error) {
	f.Calls++
	if f.CreateAPITokenFn == nil {
		panic("FakeCommodore.CreateAPIToken not stubbed")
	}
	return f.CreateAPITokenFn(ctx, req)
}

func (f *FakeCommodore) ListAPITokens(ctx context.Context, pagination *commonpb.CursorPaginationRequest) (*commodorepb.ListAPITokensResponse, error) {
	f.Calls++
	if f.ListAPITokensFn == nil {
		panic("FakeCommodore.ListAPITokens not stubbed")
	}
	return f.ListAPITokensFn(ctx, pagination)
}

func (f *FakeCommodore) RevokeAPIToken(ctx context.Context, tokenID string) (*commodorepb.RevokeAPITokenResponse, error) {
	f.Calls++
	if f.RevokeAPITokenFn == nil {
		panic("FakeCommodore.RevokeAPIToken not stubbed")
	}
	return f.RevokeAPITokenFn(ctx, tokenID)
}

func (f *FakeCommodore) RetrieveDVRChapter(ctx context.Context, req *foghorncontrolpb.RetrieveDVRChapterRequest) (*foghorncontrolpb.RetrieveDVRChapterResponse, error) {
	f.Calls++
	if f.RetrieveDVRChapterFn == nil {
		panic("FakeCommodore.RetrieveDVRChapter not stubbed")
	}
	return f.RetrieveDVRChapterFn(ctx, req)
}

func (f *FakeCommodore) ListDVRChapters(ctx context.Context, req *foghorncontrolpb.ListDVRChaptersRequest) (*foghorncontrolpb.ListDVRChaptersResponse, error) {
	f.Calls++
	if f.ListDVRChaptersFn == nil {
		panic("FakeCommodore.ListDVRChapters not stubbed")
	}
	return f.ListDVRChaptersFn(ctx, req)
}

func (f *FakeCommodore) CreatePushTarget(ctx context.Context, req *commodorepb.CreatePushTargetRequest) (*commodorepb.PushTarget, error) {
	f.Calls++
	if f.CreatePushTargetFn == nil {
		panic("FakeCommodore.CreatePushTarget not stubbed")
	}
	return f.CreatePushTargetFn(ctx, req)
}

func (f *FakeCommodore) ListPushTargets(ctx context.Context, streamID string) (*commodorepb.ListPushTargetsResponse, error) {
	f.Calls++
	if f.ListPushTargetsFn == nil {
		panic("FakeCommodore.ListPushTargets not stubbed")
	}
	return f.ListPushTargetsFn(ctx, streamID)
}

func (f *FakeCommodore) UpdatePushTarget(ctx context.Context, req *commodorepb.UpdatePushTargetRequest) (*commodorepb.PushTarget, error) {
	f.Calls++
	if f.UpdatePushTargetFn == nil {
		panic("FakeCommodore.UpdatePushTarget not stubbed")
	}
	return f.UpdatePushTargetFn(ctx, req)
}

func (f *FakeCommodore) DeletePushTarget(ctx context.Context, id string) (*commodorepb.DeletePushTargetResponse, error) {
	f.Calls++
	if f.DeletePushTargetFn == nil {
		panic("FakeCommodore.DeletePushTarget not stubbed")
	}
	return f.DeletePushTargetFn(ctx, id)
}

func (f *FakeCommodore) ResolvePlaybackPolicy(ctx context.Context, playbackID string) (*commodorepb.ResolvePlaybackPolicyResponse, error) {
	f.Calls++
	if f.ResolvePlaybackPolicyFn == nil {
		panic("FakeCommodore.ResolvePlaybackPolicy not stubbed")
	}
	return f.ResolvePlaybackPolicyFn(ctx, playbackID)
}
