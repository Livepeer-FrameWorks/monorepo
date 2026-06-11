// Package clientstest provides fake downstream-service clients for api_gateway
// unit tests. Each Fake<Service> embeds the corresponding pkg/clients interface
// (left nil) and exposes func fields for the methods exercised by a test. The
// embedded-nil interface is deliberate: any method a test forgot to stub panics
// with a clear "not stubbed" message (for func-backed methods) or a nil-pointer
// panic (for the rest), surfacing unexpected backend calls instead of silently
// returning zero values.
//
// This is an importable (non-test) package in the style of net/http/httptest so
// the fakes can be shared across the loaders, resolvers, and mcp/tools test
// suites, which live in different packages.
package clientstest

import (
	"context"
	"io"
	"sync"

	"frameworks/api_gateway/internal/clients"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/purser"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/protobuf/types/known/emptypb"
)

// DiscardLogger returns a logger that writes nowhere — for tests that only care
// about behavior, not log output.
func DiscardLogger() logging.Logger {
	l := logging.NewLogger()
	l.SetOutput(io.Discard)
	return l
}

// Clients assembles a *clients.ServiceClients from the supplied fakes. Pass only
// the fakes a test needs; the rest stay nil and panic if unexpectedly called.
func Clients(opts ...func(*clients.ServiceClients)) *clients.ServiceClients {
	sc := &clients.ServiceClients{}
	for _, opt := range opts {
		opt(sc)
	}
	return sc
}

// AuthedCtx returns a context that passes middleware.RequirePermission for a
// JWT-authenticated tenant (not demo mode). Use it to drive resolver "real"
// (non-demo) code paths.
func AuthedCtx(tenantID string) context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)
	return ctx
}

// WithCommodore wires a FakeCommodore into the ServiceClients.
func WithCommodore(f *FakeCommodore) func(*clients.ServiceClients) {
	return func(sc *clients.ServiceClients) { sc.Commodore = f }
}

// WithPeriscope wires a FakePeriscope into the ServiceClients.
func WithPeriscope(f *FakePeriscope) func(*clients.ServiceClients) {
	return func(sc *clients.ServiceClients) { sc.Periscope = f }
}

// WithQuartermaster wires a FakeQuartermaster into the ServiceClients.
func WithQuartermaster(f *FakeQuartermaster) func(*clients.ServiceClients) {
	return func(sc *clients.ServiceClients) { sc.Quartermaster = f }
}

// WithPurser wires a FakePurser into the ServiceClients.
func WithPurser(f *FakePurser) func(*clients.ServiceClients) {
	return func(sc *clients.ServiceClients) { sc.Purser = f }
}

// SolventPurser returns a FakePurser that reports complete billing details and a
// positive prepaid balance — enough to pass preflight.RequireBalance so a test
// can reach the body of a billable tool handler.
func SolventPurser() *FakePurser {
	return &FakePurser{
		GetBillingDetailsFn: func(context.Context, string) (*purserpb.BillingDetails, error) {
			return &purserpb.BillingDetails{IsComplete: true}, nil
		},
		GetPrepaidBalanceFn: func(context.Context, string, string) (*purserpb.PrepaidBalance, error) {
			return &purserpb.PrepaidBalance{BalanceCents: 5000}, nil
		},
	}
}

// ---- Commodore ----

// FakeCommodore is a commodore.Interface whose exercised methods are backed by
// func fields. Calls counts every backed method invocation (for "cache avoided
// the backend" assertions).
type FakeCommodore struct {
	commodore.Interface
	mu    sync.Mutex
	Calls int

	GetStreamFn       func(ctx context.Context, streamID string) (*commodorepb.Stream, error)
	GetStreamsBatchFn func(ctx context.Context, streamIDs []string) (*commodorepb.GetStreamsBatchResponse, error)
	ListStreamsFn     func(ctx context.Context, pagination *commonpb.CursorPaginationRequest) (*commodorepb.ListStreamsResponse, error)
	CreateStreamFn    func(ctx context.Context, req *commodorepb.CreateStreamRequest) (*commodorepb.CreateStreamResponse, error)
	UpdateStreamFn    func(ctx context.Context, req *commodorepb.UpdateStreamRequest) (*commodorepb.Stream, error)
	DeleteStreamFn    func(ctx context.Context, streamID string) (*commodorepb.DeleteStreamResponse, error)
	RefreshKeyFn      func(ctx context.Context, streamID string) (*commodorepb.RefreshStreamKeyResponse, error)
	CreateStreamKeyFn func(ctx context.Context, streamID, keyName string) (*commodorepb.StreamKeyResponse, error)
	ListStreamKeysFn  func(ctx context.Context, streamID string, pagination *commonpb.CursorPaginationRequest) (*commodorepb.ListStreamKeysResponse, error)
	DeactivateKeyFn   func(ctx context.Context, streamID, keyID string) error

	ListSigningKeysFn   func(ctx context.Context, statusFilter string, limit int32, afterID string) (*commodorepb.ListSigningKeysResponse, error)
	CreateSigningKeyFn  func(ctx context.Context, name string) (*commodorepb.CreateSigningKeyResponse, error)
	RevokeSigningKeyFn  func(ctx context.Context, id string) (*commodorepb.SigningKey, error)
	SetPlaybackPolicyFn func(ctx context.Context, req *commodorepb.SetPlaybackPolicyRequest) (*commodorepb.SetPlaybackPolicyResponse, error)

	SetNodeModeFn   func(ctx context.Context, req *foghorncontrolpb.SetNodeModeRequest) (*foghorncontrolpb.SetNodeModeResponse, error)
	GetNodeHealthFn func(ctx context.Context, req *foghorncontrolpb.GetNodeHealthRequest) (*foghorncontrolpb.GetNodeHealthResponse, error)

	CreateClipFn func(ctx context.Context, req *sharedpb.CreateClipRequest) (*sharedpb.CreateClipResponse, error)
	DeleteClipFn func(ctx context.Context, clipHash string) error

	ValidateStreamKeyFn func(ctx context.Context, streamKey string, clusterID ...string) (*commodorepb.ValidateStreamKeyResponse, error)
	GetClipsFn          func(ctx context.Context, tenantID string, streamID *string, pagination *commonpb.CursorPaginationRequest, opts ...commodore.MediaListOptions) (*sharedpb.GetClipsResponse, error)
	GetClipFn           func(ctx context.Context, clipHash string) (*sharedpb.ClipInfo, error)
	StartDVRFn          func(ctx context.Context, req *sharedpb.StartDVRRequest) (*sharedpb.StartDVRResponse, error)
	StopDVRFn           func(ctx context.Context, dvrHash string) error
	DeleteDVRFn         func(ctx context.Context, dvrHash string) error
	ListDVRRequestsFn   func(ctx context.Context, tenantID string, streamID *string, pagination *commonpb.CursorPaginationRequest, opts ...commodore.MediaListOptions) (*sharedpb.ListDVRRecordingsResponse, error)

	CreateVodUploadFn    func(ctx context.Context, req *sharedpb.CreateVodUploadRequest) (*sharedpb.CreateVodUploadResponse, error)
	CompleteVodUploadFn  func(ctx context.Context, req *sharedpb.CompleteVodUploadRequest) (*sharedpb.CompleteVodUploadResponse, error)
	AbortVodUploadFn     func(ctx context.Context, tenantID, uploadID string) (*sharedpb.AbortVodUploadResponse, error)
	GetVodUploadStatusFn func(ctx context.Context, tenantID, uploadID string) (*sharedpb.GetVodUploadStatusResponse, error)
	GetVodAssetFn        func(ctx context.Context, tenantID, artifactHash string) (*sharedpb.VodAssetInfo, error)
	DeleteVodAssetFn     func(ctx context.Context, tenantID, artifactHash string) (*sharedpb.DeleteVodAssetResponse, error)

	GetSigningKeyFn               func(ctx context.Context, id string) (*commodorepb.SigningKey, error)
	GetMeFn                       func(ctx context.Context) (*commodorepb.User, error)
	LinkEmailFn                   func(ctx context.Context, email, password string) (*commodorepb.LinkEmailResponse, error)
	LinkWalletFn                  func(ctx context.Context, address, message, signature string) (*commodorepb.WalletIdentity, error)
	ListPullSourceEventsFn        func(ctx context.Context, req *commodorepb.ListPullSourceEventsRequest) (*commodorepb.ListPullSourceEventsResponse, error)
	ListStorageArtifactsFn        func(ctx context.Context, req *commodorepb.ListStorageArtifactsRequest) (*commodorepb.ListStorageArtifactsResponse, error)
	GetTenantUserCountFn          func(ctx context.Context, tenantID string) (*commodorepb.GetTenantUserCountResponse, error)
	ListVodAssetsFn               func(ctx context.Context, tenantID string, pagination *commonpb.CursorPaginationRequest, streamID *string, opts ...commodore.MediaListOptions) (*sharedpb.ListVodAssetsResponse, error)
	LoginFn                       func(ctx context.Context, req *commodorepb.LoginRequest) (*commodorepb.AuthResponse, error)
	RefreshTokenFn                func(ctx context.Context, refreshToken string) (*commodorepb.AuthResponse, error)
	MintMistAdminSessionFn        func(ctx context.Context, req *commodorepb.MintMistAdminSessionRequest) (*commodorepb.MintMistAdminSessionResponse, error)
	RegisterFn                    func(ctx context.Context, req *commodorepb.RegisterRequest) (*commodorepb.RegisterResponse, error)
	ResolveIngestEndpointFn       func(ctx context.Context, streamKey, viewerIP string) (*sharedpb.IngestEndpointResponse, error)
	ResolveViewerEndpointFn       func(ctx context.Context, contentID, viewerIP, viewerToken string) (*sharedpb.ViewerEndpointResponse, error)
	UnlinkWalletFn                func(ctx context.Context, walletID string) (*commodorepb.UnlinkWalletResponse, error)
	WalletLoginFn                 func(ctx context.Context, address, message, signature string, attribution *commonpb.SignupAttribution) (*commodorepb.AuthResponse, error)
	GetMediaRetentionPolicyFn     func(ctx context.Context, req *commodorepb.GetMediaRetentionPolicyRequest) (*commodorepb.GetMediaRetentionPolicyResponse, error)
	SetMediaRetentionPolicyFn     func(ctx context.Context, req *commodorepb.SetMediaRetentionPolicyRequest) (*commodorepb.SetMediaRetentionPolicyResponse, error)
	UpdateAssetRetentionFn        func(ctx context.Context, req *commodorepb.UpdateAssetRetentionRequest) (*commodorepb.UpdateAssetRetentionResponse, error)
	ResetAssetRetentionFn         func(ctx context.Context, req *commodorepb.ResetAssetRetentionRequest) (*commodorepb.UpdateAssetRetentionResponse, error)
	SetStreamRetentionOverridesFn func(ctx context.Context, req *commodorepb.SetStreamRetentionOverridesRequest) (*commodorepb.SetStreamRetentionOverridesResponse, error)
	TestPlaybackAccessFn          func(ctx context.Context, req *foghorncontrolpb.TestPlaybackAccessRequest) (*foghorncontrolpb.TestPlaybackAccessResponse, error)
	CreateAPITokenFn              func(ctx context.Context, req *commodorepb.CreateAPITokenRequest) (*commodorepb.CreateAPITokenResponse, error)
	ListAPITokensFn               func(ctx context.Context, pagination *commonpb.CursorPaginationRequest) (*commodorepb.ListAPITokensResponse, error)
	RevokeAPITokenFn              func(ctx context.Context, tokenID string) (*commodorepb.RevokeAPITokenResponse, error)
	RetrieveDVRChapterFn          func(ctx context.Context, req *foghorncontrolpb.RetrieveDVRChapterRequest) (*foghorncontrolpb.RetrieveDVRChapterResponse, error)
	ListDVRChaptersFn             func(ctx context.Context, req *foghorncontrolpb.ListDVRChaptersRequest) (*foghorncontrolpb.ListDVRChaptersResponse, error)
	CreatePushTargetFn            func(ctx context.Context, req *commodorepb.CreatePushTargetRequest) (*commodorepb.PushTarget, error)
	ListPushTargetsFn             func(ctx context.Context, streamID string) (*commodorepb.ListPushTargetsResponse, error)
	UpdatePushTargetFn            func(ctx context.Context, req *commodorepb.UpdatePushTargetRequest) (*commodorepb.PushTarget, error)
	DeletePushTargetFn            func(ctx context.Context, id string) (*commodorepb.DeletePushTargetResponse, error)
	ResolvePlaybackPolicyFn       func(ctx context.Context, playbackID string) (*commodorepb.ResolvePlaybackPolicyResponse, error)
}

func (f *FakeCommodore) GetStream(ctx context.Context, streamID string) (*commodorepb.Stream, error) {
	f.Calls++
	if f.GetStreamFn == nil {
		panic("FakeCommodore.GetStream not stubbed")
	}
	return f.GetStreamFn(ctx, streamID)
}

func (f *FakeCommodore) GetStreamsBatch(ctx context.Context, streamIDs []string) (*commodorepb.GetStreamsBatchResponse, error) {
	f.Calls++
	if f.GetStreamsBatchFn == nil {
		panic("FakeCommodore.GetStreamsBatch not stubbed")
	}
	return f.GetStreamsBatchFn(ctx, streamIDs)
}

func (f *FakeCommodore) ListStreams(ctx context.Context, pagination *commonpb.CursorPaginationRequest) (*commodorepb.ListStreamsResponse, error) {
	f.Calls++
	if f.ListStreamsFn == nil {
		panic("FakeCommodore.ListStreams not stubbed")
	}
	return f.ListStreamsFn(ctx, pagination)
}

func (f *FakeCommodore) CreateStream(ctx context.Context, req *commodorepb.CreateStreamRequest) (*commodorepb.CreateStreamResponse, error) {
	f.Calls++
	if f.CreateStreamFn == nil {
		panic("FakeCommodore.CreateStream not stubbed")
	}
	return f.CreateStreamFn(ctx, req)
}

func (f *FakeCommodore) UpdateStream(ctx context.Context, req *commodorepb.UpdateStreamRequest) (*commodorepb.Stream, error) {
	f.Calls++
	if f.UpdateStreamFn == nil {
		panic("FakeCommodore.UpdateStream not stubbed")
	}
	return f.UpdateStreamFn(ctx, req)
}

func (f *FakeCommodore) DeleteStream(ctx context.Context, streamID string) (*commodorepb.DeleteStreamResponse, error) {
	f.Calls++
	if f.DeleteStreamFn == nil {
		panic("FakeCommodore.DeleteStream not stubbed")
	}
	return f.DeleteStreamFn(ctx, streamID)
}

func (f *FakeCommodore) RefreshStreamKey(ctx context.Context, streamID string) (*commodorepb.RefreshStreamKeyResponse, error) {
	f.Calls++
	if f.RefreshKeyFn == nil {
		panic("FakeCommodore.RefreshStreamKey not stubbed")
	}
	return f.RefreshKeyFn(ctx, streamID)
}

func (f *FakeCommodore) CreateStreamKey(ctx context.Context, streamID, keyName string) (*commodorepb.StreamKeyResponse, error) {
	f.Calls++
	if f.CreateStreamKeyFn == nil {
		panic("FakeCommodore.CreateStreamKey not stubbed")
	}
	return f.CreateStreamKeyFn(ctx, streamID, keyName)
}

func (f *FakeCommodore) ListStreamKeys(ctx context.Context, streamID string, pagination *commonpb.CursorPaginationRequest) (*commodorepb.ListStreamKeysResponse, error) {
	f.Calls++
	if f.ListStreamKeysFn == nil {
		panic("FakeCommodore.ListStreamKeys not stubbed")
	}
	return f.ListStreamKeysFn(ctx, streamID, pagination)
}

func (f *FakeCommodore) DeactivateStreamKey(ctx context.Context, streamID, keyID string) error {
	f.Calls++
	if f.DeactivateKeyFn == nil {
		panic("FakeCommodore.DeactivateStreamKey not stubbed")
	}
	return f.DeactivateKeyFn(ctx, streamID, keyID)
}

func (f *FakeCommodore) ListSigningKeys(ctx context.Context, statusFilter string, limit int32, afterID string) (*commodorepb.ListSigningKeysResponse, error) {
	f.Calls++
	if f.ListSigningKeysFn == nil {
		panic("FakeCommodore.ListSigningKeys not stubbed")
	}
	return f.ListSigningKeysFn(ctx, statusFilter, limit, afterID)
}

func (f *FakeCommodore) CreateSigningKey(ctx context.Context, name string) (*commodorepb.CreateSigningKeyResponse, error) {
	f.Calls++
	if f.CreateSigningKeyFn == nil {
		panic("FakeCommodore.CreateSigningKey not stubbed")
	}
	return f.CreateSigningKeyFn(ctx, name)
}

func (f *FakeCommodore) RevokeSigningKey(ctx context.Context, id string) (*commodorepb.SigningKey, error) {
	f.Calls++
	if f.RevokeSigningKeyFn == nil {
		panic("FakeCommodore.RevokeSigningKey not stubbed")
	}
	return f.RevokeSigningKeyFn(ctx, id)
}

func (f *FakeCommodore) SetPlaybackPolicy(ctx context.Context, req *commodorepb.SetPlaybackPolicyRequest) (*commodorepb.SetPlaybackPolicyResponse, error) {
	f.Calls++
	if f.SetPlaybackPolicyFn == nil {
		panic("FakeCommodore.SetPlaybackPolicy not stubbed")
	}
	return f.SetPlaybackPolicyFn(ctx, req)
}

func (f *FakeCommodore) SetNodeMode(ctx context.Context, req *foghorncontrolpb.SetNodeModeRequest) (*foghorncontrolpb.SetNodeModeResponse, error) {
	f.Calls++
	if f.SetNodeModeFn == nil {
		panic("FakeCommodore.SetNodeMode not stubbed")
	}
	return f.SetNodeModeFn(ctx, req)
}

func (f *FakeCommodore) GetNodeHealth(ctx context.Context, req *foghorncontrolpb.GetNodeHealthRequest) (*foghorncontrolpb.GetNodeHealthResponse, error) {
	f.Calls++
	if f.GetNodeHealthFn == nil {
		panic("FakeCommodore.GetNodeHealth not stubbed")
	}
	return f.GetNodeHealthFn(ctx, req)
}

func (f *FakeCommodore) CreateClip(ctx context.Context, req *sharedpb.CreateClipRequest) (*sharedpb.CreateClipResponse, error) {
	f.Calls++
	if f.CreateClipFn == nil {
		panic("FakeCommodore.CreateClip not stubbed")
	}
	return f.CreateClipFn(ctx, req)
}

func (f *FakeCommodore) DeleteClip(ctx context.Context, clipHash string) error {
	f.Calls++
	if f.DeleteClipFn == nil {
		panic("FakeCommodore.DeleteClip not stubbed")
	}
	return f.DeleteClipFn(ctx, clipHash)
}

func (f *FakeCommodore) ValidateStreamKey(ctx context.Context, streamKey string, clusterID ...string) (*commodorepb.ValidateStreamKeyResponse, error) {
	f.Calls++
	if f.ValidateStreamKeyFn == nil {
		panic("FakeCommodore.ValidateStreamKey not stubbed")
	}
	return f.ValidateStreamKeyFn(ctx, streamKey, clusterID...)
}

func (f *FakeCommodore) GetClips(ctx context.Context, tenantID string, streamID *string, pagination *commonpb.CursorPaginationRequest, opts ...commodore.MediaListOptions) (*sharedpb.GetClipsResponse, error) {
	f.Calls++
	if f.GetClipsFn == nil {
		panic("FakeCommodore.GetClips not stubbed")
	}
	return f.GetClipsFn(ctx, tenantID, streamID, pagination, opts...)
}

func (f *FakeCommodore) GetClip(ctx context.Context, clipHash string) (*sharedpb.ClipInfo, error) {
	f.Calls++
	if f.GetClipFn == nil {
		panic("FakeCommodore.GetClip not stubbed")
	}
	return f.GetClipFn(ctx, clipHash)
}

func (f *FakeCommodore) StartDVR(ctx context.Context, req *sharedpb.StartDVRRequest) (*sharedpb.StartDVRResponse, error) {
	f.Calls++
	if f.StartDVRFn == nil {
		panic("FakeCommodore.StartDVR not stubbed")
	}
	return f.StartDVRFn(ctx, req)
}

func (f *FakeCommodore) StopDVR(ctx context.Context, dvrHash string) error {
	f.Calls++
	if f.StopDVRFn == nil {
		panic("FakeCommodore.StopDVR not stubbed")
	}
	return f.StopDVRFn(ctx, dvrHash)
}

func (f *FakeCommodore) DeleteDVR(ctx context.Context, dvrHash string) error {
	f.Calls++
	if f.DeleteDVRFn == nil {
		panic("FakeCommodore.DeleteDVR not stubbed")
	}
	return f.DeleteDVRFn(ctx, dvrHash)
}

func (f *FakeCommodore) ListDVRRequests(ctx context.Context, tenantID string, streamID *string, pagination *commonpb.CursorPaginationRequest, opts ...commodore.MediaListOptions) (*sharedpb.ListDVRRecordingsResponse, error) {
	f.Calls++
	if f.ListDVRRequestsFn == nil {
		panic("FakeCommodore.ListDVRRequests not stubbed")
	}
	return f.ListDVRRequestsFn(ctx, tenantID, streamID, pagination, opts...)
}

func (f *FakeCommodore) CreateVodUpload(ctx context.Context, req *sharedpb.CreateVodUploadRequest) (*sharedpb.CreateVodUploadResponse, error) {
	f.Calls++
	if f.CreateVodUploadFn == nil {
		panic("FakeCommodore.CreateVodUpload not stubbed")
	}
	return f.CreateVodUploadFn(ctx, req)
}

func (f *FakeCommodore) CompleteVodUpload(ctx context.Context, req *sharedpb.CompleteVodUploadRequest) (*sharedpb.CompleteVodUploadResponse, error) {
	f.Calls++
	if f.CompleteVodUploadFn == nil {
		panic("FakeCommodore.CompleteVodUpload not stubbed")
	}
	return f.CompleteVodUploadFn(ctx, req)
}

func (f *FakeCommodore) AbortVodUpload(ctx context.Context, tenantID, uploadID string) (*sharedpb.AbortVodUploadResponse, error) {
	f.Calls++
	if f.AbortVodUploadFn == nil {
		panic("FakeCommodore.AbortVodUpload not stubbed")
	}
	return f.AbortVodUploadFn(ctx, tenantID, uploadID)
}

func (f *FakeCommodore) GetVodUploadStatus(ctx context.Context, tenantID, uploadID string) (*sharedpb.GetVodUploadStatusResponse, error) {
	f.Calls++
	if f.GetVodUploadStatusFn == nil {
		panic("FakeCommodore.GetVodUploadStatus not stubbed")
	}
	return f.GetVodUploadStatusFn(ctx, tenantID, uploadID)
}

func (f *FakeCommodore) GetVodAsset(ctx context.Context, tenantID, artifactHash string) (*sharedpb.VodAssetInfo, error) {
	f.Calls++
	if f.GetVodAssetFn == nil {
		panic("FakeCommodore.GetVodAsset not stubbed")
	}
	return f.GetVodAssetFn(ctx, tenantID, artifactHash)
}

func (f *FakeCommodore) DeleteVodAsset(ctx context.Context, tenantID, artifactHash string) (*sharedpb.DeleteVodAssetResponse, error) {
	f.Calls++
	if f.DeleteVodAssetFn == nil {
		panic("FakeCommodore.DeleteVodAsset not stubbed")
	}
	return f.DeleteVodAssetFn(ctx, tenantID, artifactHash)
}

func (f *FakeCommodore) GetSigningKey(ctx context.Context, id string) (*commodorepb.SigningKey, error) {
	f.Calls++
	if f.GetSigningKeyFn == nil {
		panic("FakeCommodore.GetSigningKey not stubbed")
	}
	return f.GetSigningKeyFn(ctx, id)
}

// ---- Periscope ----

// FakePeriscope is a periscope.Interface whose exercised methods are backed by
// func fields. Calls counts every backed method invocation.
type FakePeriscope struct {
	periscope.Interface
	mu    sync.Mutex
	Calls int

	GetArtifactStatesByIDsFn func(ctx context.Context, tenantID string, requestIDs []string, contentType *string) (*periscopepb.GetArtifactStatesResponse, error)
	GetStreamStatusFn        func(ctx context.Context, tenantID, streamID string) (*periscopepb.StreamStatusResponse, error)
	GetStreamsStatusFn       func(ctx context.Context, tenantID string, streamIDs []string) (*periscopepb.StreamsStatusResponse, error)
	GetLiveNodesFn           func(ctx context.Context, tenantID string, nodeID *string, relatedTenantIDs []string) (*periscopepb.GetLiveNodesResponse, error)

	GetRebufferingEventsFn func(ctx context.Context, tenantID string, streamID *string, nodeID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetRebufferingEventsResponse, error)
	GetRoutingEventsFn     func(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts, relatedTenantIDs []string, subjectTenantID, clusterID *string) (*periscopepb.GetRoutingEventsResponse, error)
	GetStreamEventsFn      func(ctx context.Context, tenantID string, streamID string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetStreamEventsResponse, error)
	GetClientMetrics5mFn   func(ctx context.Context, tenantID string, streamID *string, nodeID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetClientMetrics5MResponse, error)

	GetStreamAnalyticsSummaryFn func(ctx context.Context, tenantID string, streamID string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetStreamAnalyticsSummaryResponse, error)
	GetStreamHealthMetricsFn    func(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetStreamHealthMetricsResponse, error)
	GetViewerMetricsFn          func(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetViewerMetricsResponse, error)
	GetViewerCountTimeSeriesFn  func(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts, interval string) (*periscopepb.GetViewerCountTimeSeriesResponse, error)
	GetGeographicDistributionFn func(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts, topN int32) (*periscopepb.GetGeographicDistributionResponse, error)
	GetConnectionEventsFn       func(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetConnectionEventsResponse, error)
	GetNodeMetricsFn            func(ctx context.Context, tenantID string, nodeID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetNodeMetricsResponse, error)
	GetPlatformOverviewFn       func(ctx context.Context, tenantID string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetPlatformOverviewResponse, error)
	GetNetworkLiveStatsFn       func(ctx context.Context) (*periscopepb.GetNetworkLiveStatsResponse, error)
	ListTenantActivityFn        func(ctx context.Context, timeRange *periscope.TimeRangeOpts, tenantIDs []string, limit int32) (*periscopepb.ListTenantActivityResponse, error)
	GetStreamHealthSummaryFn    func(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetStreamHealthSummaryResponse, error)
	GetNodePerformance5mFn      func(ctx context.Context, tenantID string, nodeID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetNodePerformance5MResponse, error)

	GetAPIUsageFn                      func(ctx context.Context, tenantID string, authType *string, operationType *string, operationName *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts, summaryOnly bool) (*periscopepb.GetAPIUsageResponse, error)
	GetArtifactStateFn                 func(ctx context.Context, tenantID string, requestID string) (*periscopepb.GetArtifactStateResponse, error)
	GetArtifactStatesFn                func(ctx context.Context, tenantID string, streamID *string, contentType *string, stage *string, opts *periscope.CursorPaginationOpts) (*periscopepb.GetArtifactStatesResponse, error)
	GetBufferEventsFn                  func(ctx context.Context, tenantID string, streamID string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetBufferEventsResponse, error)
	GetClientQoeSummaryFn              func(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetClientQoeSummaryResponse, error)
	GetClipEventsFn                    func(ctx context.Context, tenantID string, streamID *string, stage *string, contentType *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetClipEventsResponse, error)
	GetClusterBootOpsFn                func(ctx context.Context, tenantID string, clusterIDs []string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetClusterBootOpsResponse, error)
	GetClusterQoeOpsFn                 func(ctx context.Context, tenantID string, clusterIDs []string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetClusterQoeOpsResponse, error)
	GetClusterTrafficMatrixFn          func(ctx context.Context, tenantID string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetClusterTrafficMatrixResponse, error)
	GetFederationEventsFn              func(ctx context.Context, tenantID string, timeRange *periscope.TimeRangeOpts, eventType *string, limit int32) (*periscopepb.GetFederationEventsResponse, error)
	GetFederationSummaryFn             func(ctx context.Context, tenantID string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetFederationSummaryResponse, error)
	GetLiveUsageSummaryFn              func(ctx context.Context, tenantID string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetLiveUsageSummaryResponse, error)
	GetNodeMetricsAggregatedFn         func(ctx context.Context, tenantID string, nodeID *string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetNodeMetricsAggregatedResponse, error)
	GetOrchestratorFn                  func(ctx context.Context, tenantID, orchAddr string) (*periscopepb.GetOrchestratorResponse, error)
	GetOrchestratorPerformanceSeriesFn func(ctx context.Context, tenantID, orchAddr string, timeRange *periscope.TimeRangeOpts, interval *string, gatewayID, resolvedIP *string) (*periscopepb.GetOrchestratorPerformanceSeriesResponse, error)
	GetPlayerBootSummaryFn             func(ctx context.Context, tenantID string, streamID *string, artifactHash *string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetPlayerBootSummaryResponse, error)
	GetPlayerBootTimeSeriesFn          func(ctx context.Context, tenantID string, streamID *string, artifactHash *string, timeRange *periscope.TimeRangeOpts, interval string) (*periscopepb.GetPlayerBootTimeSeriesResponse, error)
	GetProcessingUsageFn               func(ctx context.Context, tenantID string, streamID *string, processType *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts, summaryOnly bool) (*periscopepb.GetProcessingUsageResponse, error)
	GetQualityTierDailyFn              func(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetQualityTierDailyResponse, error)
	GetRoutingEfficiencyFn             func(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetRoutingEfficiencyResponse, error)
	GetSessionQoeSummaryFn             func(ctx context.Context, tenantID string, streamID *string, artifactHash *string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetSessionQoeSummaryResponse, error)
	GetSessionQoeTimeSeriesFn          func(ctx context.Context, tenantID string, streamID *string, artifactHash *string, timeRange *periscope.TimeRangeOpts, interval string) (*periscopepb.GetSessionQoeTimeSeriesResponse, error)
	GetStorageEventsFn                 func(ctx context.Context, tenantID string, streamID *string, assetType *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetStorageEventsResponse, error)
	GetStorageUsageFn                  func(ctx context.Context, tenantID string, nodeID *string, storageScope *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetStorageUsageResponse, error)
	GetStreamAnalyticsDailyFn          func(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetStreamAnalyticsDailyResponse, error)
	GetStreamAnalyticsSummariesFn      func(ctx context.Context, tenantID string, timeRange *periscope.TimeRangeOpts, sortBy periscope.StreamSummarySortField, sortOrder periscope.SortOrder, opts *periscope.CursorPaginationOpts) (*periscopepb.GetStreamAnalyticsSummariesResponse, error)
	GetStreamConnectionHourlyFn        func(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetStreamConnectionHourlyResponse, error)
	GetTenantAnalyticsDailyFn          func(ctx context.Context, tenantID string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetTenantAnalyticsDailyResponse, error)
	GetTenantDailyStatsFn              func(ctx context.Context, tenantID string, days int32) (*periscopepb.GetTenantDailyStatsResponse, error)
	GetTrackListEventsFn               func(ctx context.Context, tenantID string, streamID string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetTrackListEventsResponse, error)
	GetViewerGeoHourlyFn               func(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetViewerGeoHourlyResponse, error)
	GetViewerHoursHourlyFn             func(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetViewerHoursHourlyResponse, error)
	GetVodRetentionFn                  func(ctx context.Context, tenantID string, artifactHash string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetVodRetentionResponse, error)
	ListOrchestratorInstancesFn        func(ctx context.Context, tenantID string, orchAddr *string) (*periscopepb.ListOrchestratorInstancesResponse, error)
	ListOrchestratorsFn                func(ctx context.Context, tenantID string, orchAddr *string, opts *periscope.CursorPaginationOpts) (*periscopepb.ListOrchestratorsResponse, error)
	ListOrchestratorVantagesFn         func(ctx context.Context, tenantID string, orchAddr *string) (*periscopepb.ListOrchestratorVantagesResponse, error)
	ListVodRetentionAssetsFn           func(ctx context.Context, tenantID string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.ListVodRetentionAssetsResponse, error)
}

func (f *FakePeriscope) GetArtifactStatesByIDs(ctx context.Context, tenantID string, requestIDs []string, contentType *string) (*periscopepb.GetArtifactStatesResponse, error) {
	f.Calls++
	if f.GetArtifactStatesByIDsFn == nil {
		panic("FakePeriscope.GetArtifactStatesByIDs not stubbed")
	}
	return f.GetArtifactStatesByIDsFn(ctx, tenantID, requestIDs, contentType)
}

func (f *FakePeriscope) GetStreamStatus(ctx context.Context, tenantID, streamID string) (*periscopepb.StreamStatusResponse, error) {
	f.Calls++
	if f.GetStreamStatusFn == nil {
		panic("FakePeriscope.GetStreamStatus not stubbed")
	}
	return f.GetStreamStatusFn(ctx, tenantID, streamID)
}

func (f *FakePeriscope) GetStreamsStatus(ctx context.Context, tenantID string, streamIDs []string) (*periscopepb.StreamsStatusResponse, error) {
	f.Calls++
	if f.GetStreamsStatusFn == nil {
		panic("FakePeriscope.GetStreamsStatus not stubbed")
	}
	return f.GetStreamsStatusFn(ctx, tenantID, streamIDs)
}

func (f *FakePeriscope) GetLiveNodes(ctx context.Context, tenantID string, nodeID *string, relatedTenantIDs []string) (*periscopepb.GetLiveNodesResponse, error) {
	f.Calls++
	if f.GetLiveNodesFn == nil {
		panic("FakePeriscope.GetLiveNodes not stubbed")
	}
	return f.GetLiveNodesFn(ctx, tenantID, nodeID, relatedTenantIDs)
}

func (f *FakePeriscope) GetRebufferingEvents(ctx context.Context, tenantID string, streamID *string, nodeID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetRebufferingEventsResponse, error) {
	f.Calls++
	if f.GetRebufferingEventsFn == nil {
		panic("FakePeriscope.GetRebufferingEvents not stubbed")
	}
	return f.GetRebufferingEventsFn(ctx, tenantID, streamID, nodeID, timeRange, opts)
}

func (f *FakePeriscope) GetRoutingEvents(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts, relatedTenantIDs []string, subjectTenantID, clusterID *string) (*periscopepb.GetRoutingEventsResponse, error) {
	f.Calls++
	if f.GetRoutingEventsFn == nil {
		panic("FakePeriscope.GetRoutingEvents not stubbed")
	}
	return f.GetRoutingEventsFn(ctx, tenantID, streamID, timeRange, opts, relatedTenantIDs, subjectTenantID, clusterID)
}

func (f *FakePeriscope) GetStreamEvents(ctx context.Context, tenantID string, streamID string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetStreamEventsResponse, error) {
	f.Calls++
	if f.GetStreamEventsFn == nil {
		panic("FakePeriscope.GetStreamEvents not stubbed")
	}
	return f.GetStreamEventsFn(ctx, tenantID, streamID, timeRange, opts)
}

func (f *FakePeriscope) GetClientMetrics5m(ctx context.Context, tenantID string, streamID *string, nodeID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetClientMetrics5MResponse, error) {
	f.Calls++
	if f.GetClientMetrics5mFn == nil {
		panic("FakePeriscope.GetClientMetrics5m not stubbed")
	}
	return f.GetClientMetrics5mFn(ctx, tenantID, streamID, nodeID, timeRange, opts)
}

func (f *FakePeriscope) GetStreamAnalyticsSummary(ctx context.Context, tenantID string, streamID string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetStreamAnalyticsSummaryResponse, error) {
	f.Calls++
	if f.GetStreamAnalyticsSummaryFn == nil {
		panic("FakePeriscope.GetStreamAnalyticsSummary not stubbed")
	}
	return f.GetStreamAnalyticsSummaryFn(ctx, tenantID, streamID, timeRange)
}

func (f *FakePeriscope) GetStreamHealthMetrics(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetStreamHealthMetricsResponse, error) {
	f.Calls++
	if f.GetStreamHealthMetricsFn == nil {
		panic("FakePeriscope.GetStreamHealthMetrics not stubbed")
	}
	return f.GetStreamHealthMetricsFn(ctx, tenantID, streamID, timeRange, opts)
}

func (f *FakePeriscope) GetViewerMetrics(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetViewerMetricsResponse, error) {
	f.Calls++
	if f.GetViewerMetricsFn == nil {
		panic("FakePeriscope.GetViewerMetrics not stubbed")
	}
	return f.GetViewerMetricsFn(ctx, tenantID, streamID, timeRange, opts)
}

func (f *FakePeriscope) GetViewerCountTimeSeries(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts, interval string) (*periscopepb.GetViewerCountTimeSeriesResponse, error) {
	f.Calls++
	if f.GetViewerCountTimeSeriesFn == nil {
		panic("FakePeriscope.GetViewerCountTimeSeries not stubbed")
	}
	return f.GetViewerCountTimeSeriesFn(ctx, tenantID, streamID, timeRange, interval)
}

func (f *FakePeriscope) GetGeographicDistribution(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts, topN int32) (*periscopepb.GetGeographicDistributionResponse, error) {
	f.Calls++
	if f.GetGeographicDistributionFn == nil {
		panic("FakePeriscope.GetGeographicDistribution not stubbed")
	}
	return f.GetGeographicDistributionFn(ctx, tenantID, streamID, timeRange, topN)
}

func (f *FakePeriscope) GetConnectionEvents(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetConnectionEventsResponse, error) {
	f.Calls++
	if f.GetConnectionEventsFn == nil {
		panic("FakePeriscope.GetConnectionEvents not stubbed")
	}
	return f.GetConnectionEventsFn(ctx, tenantID, streamID, timeRange, opts)
}

func (f *FakePeriscope) GetNodeMetrics(ctx context.Context, tenantID string, nodeID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetNodeMetricsResponse, error) {
	f.Calls++
	if f.GetNodeMetricsFn == nil {
		panic("FakePeriscope.GetNodeMetrics not stubbed")
	}
	return f.GetNodeMetricsFn(ctx, tenantID, nodeID, timeRange, opts)
}

func (f *FakePeriscope) GetPlatformOverview(ctx context.Context, tenantID string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetPlatformOverviewResponse, error) {
	f.Calls++
	if f.GetPlatformOverviewFn == nil {
		panic("FakePeriscope.GetPlatformOverview not stubbed")
	}
	return f.GetPlatformOverviewFn(ctx, tenantID, timeRange)
}

func (f *FakePeriscope) GetNetworkLiveStats(ctx context.Context) (*periscopepb.GetNetworkLiveStatsResponse, error) {
	f.Calls++
	if f.GetNetworkLiveStatsFn == nil {
		panic("FakePeriscope.GetNetworkLiveStats not stubbed")
	}
	return f.GetNetworkLiveStatsFn(ctx)
}

func (f *FakePeriscope) GetStreamHealthSummary(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetStreamHealthSummaryResponse, error) {
	f.Calls++
	if f.GetStreamHealthSummaryFn == nil {
		panic("FakePeriscope.GetStreamHealthSummary not stubbed")
	}
	return f.GetStreamHealthSummaryFn(ctx, tenantID, streamID, timeRange)
}

// ---- Quartermaster ----

// FakeQuartermaster is a quartermaster.Interface whose exercised methods are
// backed by func fields. Calls counts every backed method invocation.
type FakeQuartermaster struct {
	quartermaster.Interface
	mu    sync.Mutex
	Calls int

	GetNodeFn               func(ctx context.Context, nodeID string) (*quartermasterpb.NodeResponse, error)
	GetClusterFn            func(ctx context.Context, clusterID string) (*quartermasterpb.ClusterResponse, error)
	ListNodesFn             func(ctx context.Context, clusterID, nodeType, region string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListNodesResponse, error)
	ListServiceInstancesFn  func(ctx context.Context, clusterID, serviceID, nodeID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListServiceInstancesResponse, error)
	ListMySubscriptionsFn   func(ctx context.Context, req *quartermasterpb.ListMySubscriptionsRequest) (*quartermasterpb.ListClustersResponse, error)
	ListClustersByOwnerFn   func(ctx context.Context, ownerTenantID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error)
	ListClustersFn          func(ctx context.Context, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error)
	ListTenantsFn           func(ctx context.Context, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListTenantsResponse, error)
	GetTenantsByClusterFn   func(ctx context.Context, clusterID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.GetTenantsByClusterResponse, error)
	CreateEnrollmentTokenFn func(ctx context.Context, req *quartermasterpb.CreateEnrollmentTokenRequest) (*quartermasterpb.CreateBootstrapTokenResponse, error)

	GetTenantFn                  func(ctx context.Context, tenantID string) (*quartermasterpb.GetTenantResponse, error)
	GetClusterRoutingFn          func(ctx context.Context, req *quartermasterpb.GetClusterRoutingRequest) (*quartermasterpb.ClusterRoutingResponse, error)
	ListClustersForTenantFn      func(ctx context.Context, tenantID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ClustersAccessResponse, error)
	ListClustersAvailableFn      func(ctx context.Context, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ClustersAvailableResponse, error)
	ListMarketplaceClustersFn    func(ctx context.Context, req *quartermasterpb.ListMarketplaceClustersRequest) (*quartermasterpb.ListMarketplaceClustersResponse, error)
	GetMarketplaceClusterFn      func(ctx context.Context, req *quartermasterpb.GetMarketplaceClusterRequest) (*quartermasterpb.MarketplaceClusterEntry, error)
	ListClusterInvitesFn         func(ctx context.Context, req *quartermasterpb.ListClusterInvitesRequest) (*quartermasterpb.ListClusterInvitesResponse, error)
	ListMyClusterInvitesFn       func(ctx context.Context, req *quartermasterpb.ListMyClusterInvitesRequest) (*quartermasterpb.ListClusterInvitesResponse, error)
	ListPendingSubscriptionsFn   func(ctx context.Context, req *quartermasterpb.ListPendingSubscriptionsRequest) (*quartermasterpb.ListPendingSubscriptionsResponse, error)
	DiscoverServicesFn           func(ctx context.Context, serviceType, clusterID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ServiceDiscoveryResponse, error)
	ListBootstrapTokensFn        func(ctx context.Context, kind, tenantID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListBootstrapTokensResponse, error)
	AcceptClusterInviteFn        func(ctx context.Context, req *quartermasterpb.AcceptClusterInviteRequest) (*quartermasterpb.ClusterSubscription, error)
	ApproveClusterSubscriptionFn func(ctx context.Context, req *quartermasterpb.ApproveClusterSubscriptionRequest) (*quartermasterpb.ClusterSubscription, error)
	CreateBootstrapTokenFn       func(ctx context.Context, req *quartermasterpb.CreateBootstrapTokenRequest) (*quartermasterpb.CreateBootstrapTokenResponse, error)
	CreateClusterInviteFn        func(ctx context.Context, req *quartermasterpb.CreateClusterInviteRequest) (*quartermasterpb.ClusterInvite, error)
	EnableSelfHostingFn          func(ctx context.Context, req *quartermasterpb.EnableSelfHostingRequest) (*quartermasterpb.EnableSelfHostingResponse, error)
	GetNodeOwnerFn               func(ctx context.Context, nodeID string) (*quartermasterpb.NodeOwnerResponse, error)
	GetServicePoolStatusFn       func(ctx context.Context, serviceType string) (*quartermasterpb.GetServicePoolStatusResponse, error)
	ListPublicTopologyClustersFn func(ctx context.Context) (*quartermasterpb.ListClustersResponse, error)
	RejectClusterSubscriptionFn  func(ctx context.Context, req *quartermasterpb.RejectClusterSubscriptionRequest) (*quartermasterpb.ClusterSubscription, error)
	RequestClusterSubscriptionFn func(ctx context.Context, req *quartermasterpb.RequestClusterSubscriptionRequest) (*quartermasterpb.ClusterSubscription, error)
	RevokeBootstrapTokenFn       func(ctx context.Context, tokenID string) error
	RevokeClusterInviteFn        func(ctx context.Context, req *quartermasterpb.RevokeClusterInviteRequest) error
	UnsubscribeFromClusterFn     func(ctx context.Context, req *quartermasterpb.UnsubscribeFromClusterRequest) (*emptypb.Empty, error)
	UpdateClusterMarketplaceFn   func(ctx context.Context, req *quartermasterpb.UpdateClusterMarketplaceRequest) (*quartermasterpb.ClusterResponse, error)
	UpdateTenantFn               func(ctx context.Context, req *quartermasterpb.UpdateTenantRequest) (*quartermasterpb.Tenant, error)
	UpdateTenantClusterFn        func(ctx context.Context, req *quartermasterpb.UpdateTenantClusterRequest) error
	ValidateBootstrapTokenFn     func(ctx context.Context, token string) (*quartermasterpb.ValidateBootstrapTokenResponse, error)
}

func (f *FakeQuartermaster) ListClustersByOwner(ctx context.Context, ownerTenantID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
	f.Calls++
	if f.ListClustersByOwnerFn == nil {
		panic("FakeQuartermaster.ListClustersByOwner not stubbed")
	}
	return f.ListClustersByOwnerFn(ctx, ownerTenantID, pagination)
}

func (f *FakeQuartermaster) GetNode(ctx context.Context, nodeID string) (*quartermasterpb.NodeResponse, error) {
	f.Calls++
	if f.GetNodeFn == nil {
		panic("FakeQuartermaster.GetNode not stubbed")
	}
	return f.GetNodeFn(ctx, nodeID)
}

func (f *FakeQuartermaster) GetTenant(ctx context.Context, tenantID string) (*quartermasterpb.GetTenantResponse, error) {
	f.Calls++
	if f.GetTenantFn == nil {
		panic("FakeQuartermaster.GetTenant not stubbed")
	}
	return f.GetTenantFn(ctx, tenantID)
}

func (f *FakeQuartermaster) GetClusterRouting(ctx context.Context, req *quartermasterpb.GetClusterRoutingRequest) (*quartermasterpb.ClusterRoutingResponse, error) {
	f.Calls++
	if f.GetClusterRoutingFn == nil {
		panic("FakeQuartermaster.GetClusterRouting not stubbed")
	}
	return f.GetClusterRoutingFn(ctx, req)
}

func (f *FakeQuartermaster) ListClustersForTenant(ctx context.Context, tenantID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ClustersAccessResponse, error) {
	f.Calls++
	if f.ListClustersForTenantFn == nil {
		panic("FakeQuartermaster.ListClustersForTenant not stubbed")
	}
	return f.ListClustersForTenantFn(ctx, tenantID, pagination)
}

func (f *FakeQuartermaster) ListClustersAvailable(ctx context.Context, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ClustersAvailableResponse, error) {
	f.Calls++
	if f.ListClustersAvailableFn == nil {
		panic("FakeQuartermaster.ListClustersAvailable not stubbed")
	}
	return f.ListClustersAvailableFn(ctx, pagination)
}

func (f *FakeQuartermaster) ListMarketplaceClusters(ctx context.Context, req *quartermasterpb.ListMarketplaceClustersRequest) (*quartermasterpb.ListMarketplaceClustersResponse, error) {
	f.Calls++
	if f.ListMarketplaceClustersFn == nil {
		panic("FakeQuartermaster.ListMarketplaceClusters not stubbed")
	}
	return f.ListMarketplaceClustersFn(ctx, req)
}

func (f *FakeQuartermaster) GetMarketplaceCluster(ctx context.Context, req *quartermasterpb.GetMarketplaceClusterRequest) (*quartermasterpb.MarketplaceClusterEntry, error) {
	f.Calls++
	if f.GetMarketplaceClusterFn == nil {
		panic("FakeQuartermaster.GetMarketplaceCluster not stubbed")
	}
	return f.GetMarketplaceClusterFn(ctx, req)
}

func (f *FakeQuartermaster) ListClusterInvites(ctx context.Context, req *quartermasterpb.ListClusterInvitesRequest) (*quartermasterpb.ListClusterInvitesResponse, error) {
	f.Calls++
	if f.ListClusterInvitesFn == nil {
		panic("FakeQuartermaster.ListClusterInvites not stubbed")
	}
	return f.ListClusterInvitesFn(ctx, req)
}

func (f *FakeQuartermaster) ListMyClusterInvites(ctx context.Context, req *quartermasterpb.ListMyClusterInvitesRequest) (*quartermasterpb.ListClusterInvitesResponse, error) {
	f.Calls++
	if f.ListMyClusterInvitesFn == nil {
		panic("FakeQuartermaster.ListMyClusterInvites not stubbed")
	}
	return f.ListMyClusterInvitesFn(ctx, req)
}

func (f *FakeQuartermaster) ListPendingSubscriptions(ctx context.Context, req *quartermasterpb.ListPendingSubscriptionsRequest) (*quartermasterpb.ListPendingSubscriptionsResponse, error) {
	f.Calls++
	if f.ListPendingSubscriptionsFn == nil {
		panic("FakeQuartermaster.ListPendingSubscriptions not stubbed")
	}
	return f.ListPendingSubscriptionsFn(ctx, req)
}

func (f *FakeQuartermaster) DiscoverServices(ctx context.Context, serviceType, clusterID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ServiceDiscoveryResponse, error) {
	f.Calls++
	if f.DiscoverServicesFn == nil {
		panic("FakeQuartermaster.DiscoverServices not stubbed")
	}
	return f.DiscoverServicesFn(ctx, serviceType, clusterID, pagination)
}

func (f *FakeQuartermaster) ListBootstrapTokens(ctx context.Context, kind, tenantID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListBootstrapTokensResponse, error) {
	f.Calls++
	if f.ListBootstrapTokensFn == nil {
		panic("FakeQuartermaster.ListBootstrapTokens not stubbed")
	}
	return f.ListBootstrapTokensFn(ctx, kind, tenantID, pagination)
}

func (f *FakeQuartermaster) GetCluster(ctx context.Context, clusterID string) (*quartermasterpb.ClusterResponse, error) {
	f.Calls++
	if f.GetClusterFn == nil {
		panic("FakeQuartermaster.GetCluster not stubbed")
	}
	return f.GetClusterFn(ctx, clusterID)
}

func (f *FakeQuartermaster) ListNodes(ctx context.Context, clusterID, nodeType, region string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListNodesResponse, error) {
	f.Calls++
	if f.ListNodesFn == nil {
		panic("FakeQuartermaster.ListNodes not stubbed")
	}
	return f.ListNodesFn(ctx, clusterID, nodeType, region, pagination)
}

func (f *FakeQuartermaster) ListServiceInstances(ctx context.Context, clusterID, serviceID, nodeID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListServiceInstancesResponse, error) {
	f.Calls++
	if f.ListServiceInstancesFn == nil {
		panic("FakeQuartermaster.ListServiceInstances not stubbed")
	}
	return f.ListServiceInstancesFn(ctx, clusterID, serviceID, nodeID, pagination)
}

func (f *FakeQuartermaster) ListMySubscriptions(ctx context.Context, req *quartermasterpb.ListMySubscriptionsRequest) (*quartermasterpb.ListClustersResponse, error) {
	f.Calls++
	if f.ListMySubscriptionsFn == nil {
		panic("FakeQuartermaster.ListMySubscriptions not stubbed")
	}
	return f.ListMySubscriptionsFn(ctx, req)
}

func (f *FakeQuartermaster) CreateEnrollmentToken(ctx context.Context, req *quartermasterpb.CreateEnrollmentTokenRequest) (*quartermasterpb.CreateBootstrapTokenResponse, error) {
	f.Calls++
	if f.CreateEnrollmentTokenFn == nil {
		panic("FakeQuartermaster.CreateEnrollmentToken not stubbed")
	}
	return f.CreateEnrollmentTokenFn(ctx, req)
}

// ---- Purser ----

// FakePurser is a purser.Interface whose exercised methods are backed by func
// fields. Calls counts every backed method invocation.
type FakePurser struct {
	purser.Interface
	mu    sync.Mutex
	Calls int

	GetBillingDetailsFn          func(ctx context.Context, tenantID string) (*purserpb.BillingDetails, error)
	GetPrepaidBalanceFn          func(ctx context.Context, tenantID, currency string) (*purserpb.PrepaidBalance, error)
	GetTenantBillingStatusFn     func(ctx context.Context, tenantID string) (*purserpb.GetTenantBillingStatusResponse, error)
	ListTenantBillingSnapshotsFn func(ctx context.Context, tenantIDs []string, limit int32) (*purserpb.ListTenantBillingSnapshotsResponse, error)
	GetPaymentRequirementsFn     func(ctx context.Context, tenantID, resource string) (*purserpb.PaymentRequirements, error)

	GetBillingTiersFn             func(ctx context.Context, includeInactive bool, pagination *commonpb.CursorPaginationRequest) (*purserpb.GetBillingTiersResponse, error)
	GetInvoiceFn                  func(ctx context.Context, invoiceID string) (*purserpb.GetInvoiceResponse, error)
	ListInvoicesFn                func(ctx context.Context, tenantID string, status *string, pagination *commonpb.CursorPaginationRequest) (*purserpb.ListInvoicesResponse, error)
	GetBillingStatusFn            func(ctx context.Context, tenantID string) (*purserpb.BillingStatusResponse, error)
	UpdateBillingDetailsFn        func(ctx context.Context, req *purserpb.UpdateBillingDetailsRequest) (*purserpb.BillingDetails, error)
	CreateCryptoTopupFn           func(ctx context.Context, req *purserpb.CreateCryptoTopupRequest) (*purserpb.CreateCryptoTopupResponse, error)
	GetCryptoTopupFn              func(ctx context.Context, topupID string) (*purserpb.CryptoTopup, error)
	ChangeBillingTierFn           func(ctx context.Context, tenantID, tierID string) (*purserpb.ChangeBillingTierResponse, error)
	CheckClusterAccessFn          func(ctx context.Context, tenantID, clusterID string) (*purserpb.CheckClusterAccessResponse, error)
	CreateCardTopupFn             func(ctx context.Context, req *purserpb.CreateCardTopupRequest) (*purserpb.CreateCardTopupResponse, error)
	CreateClusterSubscriptionFn   func(ctx context.Context, tenantID, clusterID, inviteToken string) (*purserpb.ClusterSubscriptionResponse, error)
	CreateMollieFirstPaymentFn    func(ctx context.Context, tenantID, tierID, method, redirectURL string) (*purserpb.CreateMollieFirstPaymentResponse, error)
	CreateMollieSubscriptionFn    func(ctx context.Context, tenantID, tierID, mandateID, description string) (*purserpb.CreateMollieSubscriptionResponse, error)
	CreatePaymentFn               func(ctx context.Context, req *purserpb.PaymentRequest) (*purserpb.PaymentResponse, error)
	CreateStripeBillingPortalFn   func(ctx context.Context, tenantID, returnURL string) (*purserpb.CreateBillingPortalResponse, error)
	CreateStripeCheckoutSessionFn func(ctx context.Context, tenantID, tierID, billingPeriod, successURL, cancelURL string) (*purserpb.CreateStripeCheckoutResponse, error)
	GetBillingTierFn              func(ctx context.Context, tierID string) (*purserpb.BillingTier, error)
	GetClusterPricingFn           func(ctx context.Context, clusterID string) (*purserpb.ClusterPricing, error)
	GetClustersPricingBatchFn     func(ctx context.Context, tenantID string, clusterIDs []string) (map[string]*purserpb.ClusterPricing, error)
	GetTenantUsageFn              func(ctx context.Context, tenantID, startDate, endDate string) (*purserpb.TenantUsageResponse, error)
	GetUsageAggregatesFn          func(ctx context.Context, tenantID string, timeRange *commonpb.TimeRange, granularity string, usageTypes []string) (*purserpb.GetUsageAggregatesResponse, error)
	GetUsageRecordsFn             func(ctx context.Context, tenantID, clusterID, usageType string, timeRange *commonpb.TimeRange, pagination *commonpb.CursorPaginationRequest) (*purserpb.UsageRecordsResponse, error)
	ListBalanceTransactionsFn     func(ctx context.Context, tenantID string, transactionType *string, timeRange *commonpb.TimeRange, pagination *commonpb.CursorPaginationRequest) (*purserpb.ListBalanceTransactionsResponse, error)
	ListMollieMandatesFn          func(ctx context.Context, tenantID string) (*purserpb.ListMollieMandatesResponse, error)
	PromoteToPaidFn               func(ctx context.Context, tenantID, tierID string) (*purserpb.PromoteToPaidResponse, error)
	SetClusterPricingFn           func(ctx context.Context, req *purserpb.SetClusterPricingRequest) (*purserpb.ClusterPricing, error)
	UpdateSubscriptionFn          func(ctx context.Context, req *purserpb.UpdateSubscriptionRequest) (*purserpb.TenantSubscription, error)
}

func (f *FakePurser) GetBillingDetails(ctx context.Context, tenantID string) (*purserpb.BillingDetails, error) {
	f.Calls++
	if f.GetBillingDetailsFn == nil {
		panic("FakePurser.GetBillingDetails not stubbed")
	}
	return f.GetBillingDetailsFn(ctx, tenantID)
}

func (f *FakePurser) GetPrepaidBalance(ctx context.Context, tenantID, currency string) (*purserpb.PrepaidBalance, error) {
	f.Calls++
	if f.GetPrepaidBalanceFn == nil {
		panic("FakePurser.GetPrepaidBalance not stubbed")
	}
	return f.GetPrepaidBalanceFn(ctx, tenantID, currency)
}

func (f *FakePurser) GetTenantBillingStatus(ctx context.Context, tenantID string) (*purserpb.GetTenantBillingStatusResponse, error) {
	f.Calls++
	if f.GetTenantBillingStatusFn == nil {
		panic("FakePurser.GetTenantBillingStatus not stubbed")
	}
	return f.GetTenantBillingStatusFn(ctx, tenantID)
}

func (f *FakePurser) GetPaymentRequirements(ctx context.Context, tenantID, resource string) (*purserpb.PaymentRequirements, error) {
	f.Calls++
	if f.GetPaymentRequirementsFn == nil {
		panic("FakePurser.GetPaymentRequirements not stubbed")
	}
	return f.GetPaymentRequirementsFn(ctx, tenantID, resource)
}

func (f *FakePurser) GetBillingTiers(ctx context.Context, includeInactive bool, pagination *commonpb.CursorPaginationRequest) (*purserpb.GetBillingTiersResponse, error) {
	f.Calls++
	if f.GetBillingTiersFn == nil {
		panic("FakePurser.GetBillingTiers not stubbed")
	}
	return f.GetBillingTiersFn(ctx, includeInactive, pagination)
}

func (f *FakePurser) GetInvoice(ctx context.Context, invoiceID string) (*purserpb.GetInvoiceResponse, error) {
	f.Calls++
	if f.GetInvoiceFn == nil {
		panic("FakePurser.GetInvoice not stubbed")
	}
	return f.GetInvoiceFn(ctx, invoiceID)
}

func (f *FakePurser) ListInvoices(ctx context.Context, tenantID string, status *string, pagination *commonpb.CursorPaginationRequest) (*purserpb.ListInvoicesResponse, error) {
	f.Calls++
	if f.ListInvoicesFn == nil {
		panic("FakePurser.ListInvoices not stubbed")
	}
	return f.ListInvoicesFn(ctx, tenantID, status, pagination)
}

func (f *FakePurser) GetBillingStatus(ctx context.Context, tenantID string) (*purserpb.BillingStatusResponse, error) {
	f.Calls++
	if f.GetBillingStatusFn == nil {
		panic("FakePurser.GetBillingStatus not stubbed")
	}
	return f.GetBillingStatusFn(ctx, tenantID)
}

func (f *FakePurser) UpdateBillingDetails(ctx context.Context, req *purserpb.UpdateBillingDetailsRequest) (*purserpb.BillingDetails, error) {
	f.Calls++
	if f.UpdateBillingDetailsFn == nil {
		panic("FakePurser.UpdateBillingDetails not stubbed")
	}
	return f.UpdateBillingDetailsFn(ctx, req)
}

func (f *FakePurser) CreateCryptoTopup(ctx context.Context, req *purserpb.CreateCryptoTopupRequest) (*purserpb.CreateCryptoTopupResponse, error) {
	f.Calls++
	if f.CreateCryptoTopupFn == nil {
		panic("FakePurser.CreateCryptoTopup not stubbed")
	}
	return f.CreateCryptoTopupFn(ctx, req)
}

func (f *FakePurser) GetCryptoTopup(ctx context.Context, topupID string) (*purserpb.CryptoTopup, error) {
	f.Calls++
	if f.GetCryptoTopupFn == nil {
		panic("FakePurser.GetCryptoTopup not stubbed")
	}
	return f.GetCryptoTopupFn(ctx, topupID)
}
