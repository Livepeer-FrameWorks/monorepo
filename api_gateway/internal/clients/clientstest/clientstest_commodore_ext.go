package clientstest

// FakeCommodore push-target / token / retention / playback / DVR-chapter methods
// (generated to match pkg/clients/commodore.Interface).

import (
	"context"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"

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

func (f *FakeCommodore) GetTranscodeLadderPolicy(ctx context.Context, req *commodorepb.GetTranscodeLadderPolicyRequest) (*commodorepb.TranscodeLadderPolicy, error) {
	f.Calls++
	if f.GetTranscodeLadderPolicyFn == nil {
		panic("FakeCommodore.GetTranscodeLadderPolicy not stubbed")
	}
	return f.GetTranscodeLadderPolicyFn(ctx, req)
}

func (f *FakeCommodore) SetTenantTranscodeLadder(ctx context.Context, req *commodorepb.SetTenantTranscodeLadderRequest) (*commodorepb.TranscodeLadderPolicy, error) {
	f.Calls++
	if f.SetTenantTranscodeLadderFn == nil {
		panic("FakeCommodore.SetTenantTranscodeLadder not stubbed")
	}
	return f.SetTenantTranscodeLadderFn(ctx, req)
}

func (f *FakeCommodore) SetVodOriginalPolicy(ctx context.Context, req *commodorepb.SetVodOriginalPolicyRequest) (*commodorepb.GetMediaRetentionPolicyResponse, error) {
	f.Calls++
	if f.SetVodOriginalPolicyFn == nil {
		panic("FakeCommodore.SetVodOriginalPolicy not stubbed")
	}
	return f.SetVodOriginalPolicyFn(ctx, req)
}

func (f *FakeCommodore) CreateVodOriginalDownload(ctx context.Context, req *sharedpb.CreateVodOriginalDownloadRequest) (*sharedpb.CreateVodOriginalDownloadResponse, error) {
	f.Calls++
	if f.CreateVodOriginalDownloadFn == nil {
		panic("FakeCommodore.CreateVodOriginalDownload not stubbed")
	}
	return f.CreateVodOriginalDownloadFn(ctx, req)
}

func (f *FakeCommodore) DeleteVodOriginal(ctx context.Context, req *sharedpb.DeleteVodOriginalRequest) (*sharedpb.DeleteVodOriginalResponse, error) {
	f.Calls++
	if f.DeleteVodOriginalFn == nil {
		panic("FakeCommodore.DeleteVodOriginal not stubbed")
	}
	return f.DeleteVodOriginalFn(ctx, req)
}

func (f *FakeCommodore) GetStreamIngestStates(ctx context.Context, ids []string) (*foghorncontrolpb.GetStreamIngestStatesResponse, error) {
	f.mu.Lock()
	f.Calls++
	f.mu.Unlock()
	if f.GetStreamIngestStatesFn == nil {
		panic("FakeCommodore.GetStreamIngestStates not stubbed")
	}
	return f.GetStreamIngestStatesFn(ctx, ids)
}

func (f *FakeCommodore) ListStreamArtifacts(ctx context.Context, req *commodorepb.ListStreamArtifactsRequest) (*commodorepb.ListStreamArtifactsResponse, error) {
	f.mu.Lock()
	f.Calls++
	f.mu.Unlock()
	if f.ListStreamArtifactsFn == nil {
		panic("FakeCommodore media API not stubbed")
	}
	return f.ListStreamArtifactsFn(ctx, req)
}
func (f *FakeCommodore) GetDVRRecording(ctx context.Context, id string) (*sharedpb.DVRInfo, error) {
	f.mu.Lock()
	f.Calls++
	f.mu.Unlock()
	if f.GetDVRRecordingFn == nil {
		panic("FakeCommodore media API not stubbed")
	}
	return f.GetDVRRecordingFn(ctx, id)
}

func (f *FakeCommodore) RefreshStreamKeyAndEndIngest(ctx context.Context, id string) (*commodorepb.RefreshStreamKeyResponse, error) {
	f.mu.Lock()
	f.Calls++
	f.mu.Unlock()
	if f.RefreshKeyAndEndFn == nil {
		panic("FakeCommodore.RefreshStreamKeyAndEndIngest not stubbed")
	}
	return f.RefreshKeyAndEndFn(ctx, id)
}
func (f *FakeCommodore) DisconnectIngest(ctx context.Context, id string) (*commodorepb.DisconnectIngestResponse, error) {
	f.mu.Lock()
	f.Calls++
	f.mu.Unlock()
	if f.DisconnectIngestFn == nil {
		panic("FakeCommodore.DisconnectIngest not stubbed")
	}
	return f.DisconnectIngestFn(ctx, id)
}
