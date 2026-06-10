package clientstest

// Batch-3 fake methods (auth/billing-mutations/cluster-lifecycle).

import (
	"context"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"google.golang.org/protobuf/types/known/emptypb"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

func (f *FakeCommodore) GetMe(ctx context.Context) (*commodorepb.User, error) {
	f.Calls++
	if f.GetMeFn == nil {
		panic("FakeCommodore.GetMe not stubbed")
	}
	return f.GetMeFn(ctx)
}

func (f *FakeCommodore) LinkEmail(ctx context.Context, email, password string) (*commodorepb.LinkEmailResponse, error) {
	f.Calls++
	if f.LinkEmailFn == nil {
		panic("FakeCommodore.LinkEmail not stubbed")
	}
	return f.LinkEmailFn(ctx, email, password)
}

func (f *FakeCommodore) LinkWallet(ctx context.Context, address, message, signature string) (*commodorepb.WalletIdentity, error) {
	f.Calls++
	if f.LinkWalletFn == nil {
		panic("FakeCommodore.LinkWallet not stubbed")
	}
	return f.LinkWalletFn(ctx, address, message, signature)
}

func (f *FakeCommodore) ListPullSourceEvents(ctx context.Context, req *commodorepb.ListPullSourceEventsRequest) (*commodorepb.ListPullSourceEventsResponse, error) {
	f.Calls++
	if f.ListPullSourceEventsFn == nil {
		panic("FakeCommodore.ListPullSourceEvents not stubbed")
	}
	return f.ListPullSourceEventsFn(ctx, req)
}

func (f *FakeCommodore) ListStorageArtifacts(ctx context.Context, req *commodorepb.ListStorageArtifactsRequest) (*commodorepb.ListStorageArtifactsResponse, error) {
	f.Calls++
	if f.ListStorageArtifactsFn == nil {
		panic("FakeCommodore.ListStorageArtifacts not stubbed")
	}
	return f.ListStorageArtifactsFn(ctx, req)
}

func (f *FakeCommodore) ListVodAssets(ctx context.Context, tenantID string, pagination *commonpb.CursorPaginationRequest, streamID *string, opts ...commodore.MediaListOptions) (*sharedpb.ListVodAssetsResponse, error) {
	f.Calls++
	if f.ListVodAssetsFn == nil {
		panic("FakeCommodore.ListVodAssets not stubbed")
	}
	return f.ListVodAssetsFn(ctx, tenantID, pagination, streamID, opts...)
}

func (f *FakeCommodore) Login(ctx context.Context, req *commodorepb.LoginRequest) (*commodorepb.AuthResponse, error) {
	f.Calls++
	if f.LoginFn == nil {
		panic("FakeCommodore.Login not stubbed")
	}
	return f.LoginFn(ctx, req)
}

func (f *FakeCommodore) RefreshToken(ctx context.Context, refreshToken string) (*commodorepb.AuthResponse, error) {
	f.Calls++
	if f.RefreshTokenFn == nil {
		panic("FakeCommodore.RefreshToken not stubbed")
	}
	return f.RefreshTokenFn(ctx, refreshToken)
}

func (f *FakeCommodore) MintMistAdminSession(ctx context.Context, req *commodorepb.MintMistAdminSessionRequest) (*commodorepb.MintMistAdminSessionResponse, error) {
	f.Calls++
	if f.MintMistAdminSessionFn == nil {
		panic("FakeCommodore.MintMistAdminSession not stubbed")
	}
	return f.MintMistAdminSessionFn(ctx, req)
}

func (f *FakeCommodore) Register(ctx context.Context, req *commodorepb.RegisterRequest) (*commodorepb.RegisterResponse, error) {
	f.Calls++
	if f.RegisterFn == nil {
		panic("FakeCommodore.Register not stubbed")
	}
	return f.RegisterFn(ctx, req)
}

func (f *FakeCommodore) ResolveIngestEndpoint(ctx context.Context, streamKey, viewerIP string) (*sharedpb.IngestEndpointResponse, error) {
	f.Calls++
	if f.ResolveIngestEndpointFn == nil {
		panic("FakeCommodore.ResolveIngestEndpoint not stubbed")
	}
	return f.ResolveIngestEndpointFn(ctx, streamKey, viewerIP)
}

func (f *FakeCommodore) ResolveViewerEndpoint(ctx context.Context, contentID, viewerIP, viewerToken string) (*sharedpb.ViewerEndpointResponse, error) {
	f.Calls++
	if f.ResolveViewerEndpointFn == nil {
		panic("FakeCommodore.ResolveViewerEndpoint not stubbed")
	}
	return f.ResolveViewerEndpointFn(ctx, contentID, viewerIP, viewerToken)
}

func (f *FakeCommodore) UnlinkWallet(ctx context.Context, walletID string) (*commodorepb.UnlinkWalletResponse, error) {
	f.Calls++
	if f.UnlinkWalletFn == nil {
		panic("FakeCommodore.UnlinkWallet not stubbed")
	}
	return f.UnlinkWalletFn(ctx, walletID)
}

func (f *FakeCommodore) WalletLogin(ctx context.Context, address, message, signature string, attribution *commonpb.SignupAttribution) (*commodorepb.AuthResponse, error) {
	f.Calls++
	if f.WalletLoginFn == nil {
		panic("FakeCommodore.WalletLogin not stubbed")
	}
	return f.WalletLoginFn(ctx, address, message, signature, attribution)
}

func (f *FakePurser) ChangeBillingTier(ctx context.Context, tenantID, tierID string) (*purserpb.ChangeBillingTierResponse, error) {
	f.Calls++
	if f.ChangeBillingTierFn == nil {
		panic("FakePurser.ChangeBillingTier not stubbed")
	}
	return f.ChangeBillingTierFn(ctx, tenantID, tierID)
}

func (f *FakePurser) CheckClusterAccess(ctx context.Context, tenantID, clusterID string) (*purserpb.CheckClusterAccessResponse, error) {
	f.Calls++
	if f.CheckClusterAccessFn == nil {
		panic("FakePurser.CheckClusterAccess not stubbed")
	}
	return f.CheckClusterAccessFn(ctx, tenantID, clusterID)
}

func (f *FakePurser) CreateCardTopup(ctx context.Context, req *purserpb.CreateCardTopupRequest) (*purserpb.CreateCardTopupResponse, error) {
	f.Calls++
	if f.CreateCardTopupFn == nil {
		panic("FakePurser.CreateCardTopup not stubbed")
	}
	return f.CreateCardTopupFn(ctx, req)
}

func (f *FakePurser) CreateClusterSubscription(ctx context.Context, tenantID, clusterID, inviteToken string) (*purserpb.ClusterSubscriptionResponse, error) {
	f.Calls++
	if f.CreateClusterSubscriptionFn == nil {
		panic("FakePurser.CreateClusterSubscription not stubbed")
	}
	return f.CreateClusterSubscriptionFn(ctx, tenantID, clusterID, inviteToken)
}

func (f *FakePurser) CreateMollieFirstPayment(ctx context.Context, tenantID, tierID, method, redirectURL string) (*purserpb.CreateMollieFirstPaymentResponse, error) {
	f.Calls++
	if f.CreateMollieFirstPaymentFn == nil {
		panic("FakePurser.CreateMollieFirstPayment not stubbed")
	}
	return f.CreateMollieFirstPaymentFn(ctx, tenantID, tierID, method, redirectURL)
}

func (f *FakePurser) CreateMollieSubscription(ctx context.Context, tenantID, tierID, mandateID, description string) (*purserpb.CreateMollieSubscriptionResponse, error) {
	f.Calls++
	if f.CreateMollieSubscriptionFn == nil {
		panic("FakePurser.CreateMollieSubscription not stubbed")
	}
	return f.CreateMollieSubscriptionFn(ctx, tenantID, tierID, mandateID, description)
}

func (f *FakePurser) CreatePayment(ctx context.Context, req *purserpb.PaymentRequest) (*purserpb.PaymentResponse, error) {
	f.Calls++
	if f.CreatePaymentFn == nil {
		panic("FakePurser.CreatePayment not stubbed")
	}
	return f.CreatePaymentFn(ctx, req)
}

func (f *FakePurser) CreateStripeBillingPortal(ctx context.Context, tenantID, returnURL string) (*purserpb.CreateBillingPortalResponse, error) {
	f.Calls++
	if f.CreateStripeBillingPortalFn == nil {
		panic("FakePurser.CreateStripeBillingPortal not stubbed")
	}
	return f.CreateStripeBillingPortalFn(ctx, tenantID, returnURL)
}

func (f *FakePurser) CreateStripeCheckoutSession(ctx context.Context, tenantID, tierID, billingPeriod, successURL, cancelURL string) (*purserpb.CreateStripeCheckoutResponse, error) {
	f.Calls++
	if f.CreateStripeCheckoutSessionFn == nil {
		panic("FakePurser.CreateStripeCheckoutSession not stubbed")
	}
	return f.CreateStripeCheckoutSessionFn(ctx, tenantID, tierID, billingPeriod, successURL, cancelURL)
}

func (f *FakePurser) GetBillingTier(ctx context.Context, tierID string) (*purserpb.BillingTier, error) {
	f.Calls++
	if f.GetBillingTierFn == nil {
		panic("FakePurser.GetBillingTier not stubbed")
	}
	return f.GetBillingTierFn(ctx, tierID)
}

func (f *FakePurser) GetClusterPricing(ctx context.Context, clusterID string) (*purserpb.ClusterPricing, error) {
	f.Calls++
	if f.GetClusterPricingFn == nil {
		panic("FakePurser.GetClusterPricing not stubbed")
	}
	return f.GetClusterPricingFn(ctx, clusterID)
}

func (f *FakePurser) GetClustersPricingBatch(ctx context.Context, tenantID string, clusterIDs []string) (map[string]*purserpb.ClusterPricing, error) {
	f.Calls++
	if f.GetClustersPricingBatchFn == nil {
		panic("FakePurser.GetClustersPricingBatch not stubbed")
	}
	return f.GetClustersPricingBatchFn(ctx, tenantID, clusterIDs)
}

func (f *FakePurser) GetTenantUsage(ctx context.Context, tenantID, startDate, endDate string) (*purserpb.TenantUsageResponse, error) {
	f.Calls++
	if f.GetTenantUsageFn == nil {
		panic("FakePurser.GetTenantUsage not stubbed")
	}
	return f.GetTenantUsageFn(ctx, tenantID, startDate, endDate)
}

func (f *FakePurser) GetUsageAggregates(ctx context.Context, tenantID string, timeRange *commonpb.TimeRange, granularity string, usageTypes []string) (*purserpb.GetUsageAggregatesResponse, error) {
	f.Calls++
	if f.GetUsageAggregatesFn == nil {
		panic("FakePurser.GetUsageAggregates not stubbed")
	}
	return f.GetUsageAggregatesFn(ctx, tenantID, timeRange, granularity, usageTypes)
}

func (f *FakePurser) GetUsageRecords(ctx context.Context, tenantID, clusterID, usageType string, timeRange *commonpb.TimeRange, pagination *commonpb.CursorPaginationRequest) (*purserpb.UsageRecordsResponse, error) {
	f.Calls++
	if f.GetUsageRecordsFn == nil {
		panic("FakePurser.GetUsageRecords not stubbed")
	}
	return f.GetUsageRecordsFn(ctx, tenantID, clusterID, usageType, timeRange, pagination)
}

func (f *FakePurser) ListBalanceTransactions(ctx context.Context, tenantID string, transactionType *string, timeRange *commonpb.TimeRange, pagination *commonpb.CursorPaginationRequest) (*purserpb.ListBalanceTransactionsResponse, error) {
	f.Calls++
	if f.ListBalanceTransactionsFn == nil {
		panic("FakePurser.ListBalanceTransactions not stubbed")
	}
	return f.ListBalanceTransactionsFn(ctx, tenantID, transactionType, timeRange, pagination)
}

func (f *FakePurser) ListMollieMandates(ctx context.Context, tenantID string) (*purserpb.ListMollieMandatesResponse, error) {
	f.Calls++
	if f.ListMollieMandatesFn == nil {
		panic("FakePurser.ListMollieMandates not stubbed")
	}
	return f.ListMollieMandatesFn(ctx, tenantID)
}

func (f *FakePurser) PromoteToPaid(ctx context.Context, tenantID, tierID string) (*purserpb.PromoteToPaidResponse, error) {
	f.Calls++
	if f.PromoteToPaidFn == nil {
		panic("FakePurser.PromoteToPaid not stubbed")
	}
	return f.PromoteToPaidFn(ctx, tenantID, tierID)
}

func (f *FakePurser) SetClusterPricing(ctx context.Context, req *purserpb.SetClusterPricingRequest) (*purserpb.ClusterPricing, error) {
	f.Calls++
	if f.SetClusterPricingFn == nil {
		panic("FakePurser.SetClusterPricing not stubbed")
	}
	return f.SetClusterPricingFn(ctx, req)
}

func (f *FakePurser) UpdateSubscription(ctx context.Context, req *purserpb.UpdateSubscriptionRequest) (*purserpb.TenantSubscription, error) {
	f.Calls++
	if f.UpdateSubscriptionFn == nil {
		panic("FakePurser.UpdateSubscription not stubbed")
	}
	return f.UpdateSubscriptionFn(ctx, req)
}

func (f *FakeQuartermaster) AcceptClusterInvite(ctx context.Context, req *quartermasterpb.AcceptClusterInviteRequest) (*quartermasterpb.ClusterSubscription, error) {
	f.Calls++
	if f.AcceptClusterInviteFn == nil {
		panic("FakeQuartermaster.AcceptClusterInvite not stubbed")
	}
	return f.AcceptClusterInviteFn(ctx, req)
}

func (f *FakeQuartermaster) ApproveClusterSubscription(ctx context.Context, req *quartermasterpb.ApproveClusterSubscriptionRequest) (*quartermasterpb.ClusterSubscription, error) {
	f.Calls++
	if f.ApproveClusterSubscriptionFn == nil {
		panic("FakeQuartermaster.ApproveClusterSubscription not stubbed")
	}
	return f.ApproveClusterSubscriptionFn(ctx, req)
}

func (f *FakeQuartermaster) CreateBootstrapToken(ctx context.Context, req *quartermasterpb.CreateBootstrapTokenRequest) (*quartermasterpb.CreateBootstrapTokenResponse, error) {
	f.Calls++
	if f.CreateBootstrapTokenFn == nil {
		panic("FakeQuartermaster.CreateBootstrapToken not stubbed")
	}
	return f.CreateBootstrapTokenFn(ctx, req)
}

func (f *FakeQuartermaster) CreateClusterInvite(ctx context.Context, req *quartermasterpb.CreateClusterInviteRequest) (*quartermasterpb.ClusterInvite, error) {
	f.Calls++
	if f.CreateClusterInviteFn == nil {
		panic("FakeQuartermaster.CreateClusterInvite not stubbed")
	}
	return f.CreateClusterInviteFn(ctx, req)
}

func (f *FakeQuartermaster) EnableSelfHosting(ctx context.Context, req *quartermasterpb.EnableSelfHostingRequest) (*quartermasterpb.EnableSelfHostingResponse, error) {
	f.Calls++
	if f.EnableSelfHostingFn == nil {
		panic("FakeQuartermaster.EnableSelfHosting not stubbed")
	}
	return f.EnableSelfHostingFn(ctx, req)
}

func (f *FakeQuartermaster) GetNodeOwner(ctx context.Context, nodeID string) (*quartermasterpb.NodeOwnerResponse, error) {
	f.Calls++
	if f.GetNodeOwnerFn == nil {
		panic("FakeQuartermaster.GetNodeOwner not stubbed")
	}
	return f.GetNodeOwnerFn(ctx, nodeID)
}

func (f *FakeQuartermaster) GetServicePoolStatus(ctx context.Context, serviceType string) (*quartermasterpb.GetServicePoolStatusResponse, error) {
	f.Calls++
	if f.GetServicePoolStatusFn == nil {
		panic("FakeQuartermaster.GetServicePoolStatus not stubbed")
	}
	return f.GetServicePoolStatusFn(ctx, serviceType)
}

func (f *FakeQuartermaster) ListPublicTopologyClusters(ctx context.Context) (*quartermasterpb.ListClustersResponse, error) {
	f.Calls++
	if f.ListPublicTopologyClustersFn == nil {
		panic("FakeQuartermaster.ListPublicTopologyClusters not stubbed")
	}
	return f.ListPublicTopologyClustersFn(ctx)
}

func (f *FakeQuartermaster) RejectClusterSubscription(ctx context.Context, req *quartermasterpb.RejectClusterSubscriptionRequest) (*quartermasterpb.ClusterSubscription, error) {
	f.Calls++
	if f.RejectClusterSubscriptionFn == nil {
		panic("FakeQuartermaster.RejectClusterSubscription not stubbed")
	}
	return f.RejectClusterSubscriptionFn(ctx, req)
}

func (f *FakeQuartermaster) RequestClusterSubscription(ctx context.Context, req *quartermasterpb.RequestClusterSubscriptionRequest) (*quartermasterpb.ClusterSubscription, error) {
	f.Calls++
	if f.RequestClusterSubscriptionFn == nil {
		panic("FakeQuartermaster.RequestClusterSubscription not stubbed")
	}
	return f.RequestClusterSubscriptionFn(ctx, req)
}

func (f *FakeQuartermaster) RevokeBootstrapToken(ctx context.Context, tokenID string) error {
	f.Calls++
	if f.RevokeBootstrapTokenFn == nil {
		panic("FakeQuartermaster.RevokeBootstrapToken not stubbed")
	}
	return f.RevokeBootstrapTokenFn(ctx, tokenID)
}

func (f *FakeQuartermaster) RevokeClusterInvite(ctx context.Context, req *quartermasterpb.RevokeClusterInviteRequest) error {
	f.Calls++
	if f.RevokeClusterInviteFn == nil {
		panic("FakeQuartermaster.RevokeClusterInvite not stubbed")
	}
	return f.RevokeClusterInviteFn(ctx, req)
}

func (f *FakeQuartermaster) UnsubscribeFromCluster(ctx context.Context, req *quartermasterpb.UnsubscribeFromClusterRequest) (*emptypb.Empty, error) {
	f.Calls++
	if f.UnsubscribeFromClusterFn == nil {
		panic("FakeQuartermaster.UnsubscribeFromCluster not stubbed")
	}
	return f.UnsubscribeFromClusterFn(ctx, req)
}

func (f *FakeQuartermaster) UpdateClusterMarketplace(ctx context.Context, req *quartermasterpb.UpdateClusterMarketplaceRequest) (*quartermasterpb.ClusterResponse, error) {
	f.Calls++
	if f.UpdateClusterMarketplaceFn == nil {
		panic("FakeQuartermaster.UpdateClusterMarketplace not stubbed")
	}
	return f.UpdateClusterMarketplaceFn(ctx, req)
}

func (f *FakeQuartermaster) UpdateTenant(ctx context.Context, req *quartermasterpb.UpdateTenantRequest) (*quartermasterpb.Tenant, error) {
	f.Calls++
	if f.UpdateTenantFn == nil {
		panic("FakeQuartermaster.UpdateTenant not stubbed")
	}
	return f.UpdateTenantFn(ctx, req)
}

func (f *FakeQuartermaster) UpdateTenantCluster(ctx context.Context, req *quartermasterpb.UpdateTenantClusterRequest) error {
	f.Calls++
	if f.UpdateTenantClusterFn == nil {
		panic("FakeQuartermaster.UpdateTenantCluster not stubbed")
	}
	return f.UpdateTenantClusterFn(ctx, req)
}

func (f *FakeQuartermaster) ValidateBootstrapToken(ctx context.Context, token string) (*quartermasterpb.ValidateBootstrapTokenResponse, error) {
	f.Calls++
	if f.ValidateBootstrapTokenFn == nil {
		panic("FakeQuartermaster.ValidateBootstrapToken not stubbed")
	}
	return f.ValidateBootstrapTokenFn(ctx, token)
}
