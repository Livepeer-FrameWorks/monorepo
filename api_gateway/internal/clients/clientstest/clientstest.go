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

// ---- Periscope ----

// FakePeriscope is a periscope.Interface whose exercised methods are backed by
// func fields. Calls counts every backed method invocation.
type FakePeriscope struct {
	periscope.Interface
	Calls int

	GetArtifactStatesByIDsFn func(ctx context.Context, tenantID string, requestIDs []string, contentType *string) (*periscopepb.GetArtifactStatesResponse, error)
	GetStreamStatusFn        func(ctx context.Context, tenantID, streamID string) (*periscopepb.StreamStatusResponse, error)
	GetStreamsStatusFn       func(ctx context.Context, tenantID string, streamIDs []string) (*periscopepb.StreamsStatusResponse, error)
	GetLiveNodesFn           func(ctx context.Context, tenantID string, nodeID *string, relatedTenantIDs []string) (*periscopepb.GetLiveNodesResponse, error)

	GetRebufferingEventsFn func(ctx context.Context, tenantID string, streamID *string, nodeID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetRebufferingEventsResponse, error)
	GetRoutingEventsFn     func(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts, relatedTenantIDs []string, subjectTenantID, clusterID *string) (*periscopepb.GetRoutingEventsResponse, error)
	GetStreamEventsFn      func(ctx context.Context, tenantID string, streamID string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetStreamEventsResponse, error)
	GetClientMetrics5mFn   func(ctx context.Context, tenantID string, streamID *string, nodeID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetClientMetrics5MResponse, error)
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

// ---- Quartermaster ----

// FakeQuartermaster is a quartermaster.Interface whose exercised methods are
// backed by func fields. Calls counts every backed method invocation.
type FakeQuartermaster struct {
	quartermaster.Interface
	Calls int

	GetNodeFn               func(ctx context.Context, nodeID string) (*quartermasterpb.NodeResponse, error)
	GetClusterFn            func(ctx context.Context, clusterID string) (*quartermasterpb.ClusterResponse, error)
	ListNodesFn             func(ctx context.Context, clusterID, nodeType, region string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListNodesResponse, error)
	ListServiceInstancesFn  func(ctx context.Context, clusterID, serviceID, nodeID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListServiceInstancesResponse, error)
	ListMySubscriptionsFn   func(ctx context.Context, req *quartermasterpb.ListMySubscriptionsRequest) (*quartermasterpb.ListClustersResponse, error)
	ListClustersByOwnerFn   func(ctx context.Context, ownerTenantID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error)
	CreateEnrollmentTokenFn func(ctx context.Context, req *quartermasterpb.CreateEnrollmentTokenRequest) (*quartermasterpb.CreateBootstrapTokenResponse, error)
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
	Calls int

	GetBillingDetailsFn      func(ctx context.Context, tenantID string) (*purserpb.BillingDetails, error)
	GetPrepaidBalanceFn      func(ctx context.Context, tenantID, currency string) (*purserpb.PrepaidBalance, error)
	GetTenantBillingStatusFn func(ctx context.Context, tenantID string) (*purserpb.GetTenantBillingStatusResponse, error)
	GetPaymentRequirementsFn func(ctx context.Context, tenantID, resource string) (*purserpb.PaymentRequirements, error)

	GetBillingTiersFn  func(ctx context.Context, includeInactive bool, pagination *commonpb.CursorPaginationRequest) (*purserpb.GetBillingTiersResponse, error)
	GetInvoiceFn       func(ctx context.Context, invoiceID string) (*purserpb.GetInvoiceResponse, error)
	ListInvoicesFn     func(ctx context.Context, tenantID string, status *string, pagination *commonpb.CursorPaginationRequest) (*purserpb.ListInvoicesResponse, error)
	GetBillingStatusFn func(ctx context.Context, tenantID string) (*purserpb.BillingStatusResponse, error)
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
