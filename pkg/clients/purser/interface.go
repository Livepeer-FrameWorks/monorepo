package purser

import (
	"context"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	x402pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/x402"
)

// Interface is the full method surface of the concrete client, extracted so
// that api_gateway can inject fakes for resolver real-path tests. The concrete
// client satisfies it (asserted below).
type Interface interface {
	Close() error
	GetTenantBillingStatus(ctx context.Context, tenantID string) (*purserpb.GetTenantBillingStatusResponse, error)
	GetBillingTiers(ctx context.Context, includeInactive bool, pagination *commonpb.CursorPaginationRequest) (*purserpb.GetBillingTiersResponse, error)
	GetBillingTier(ctx context.Context, tierID string) (*purserpb.BillingTier, error)
	CreateBillingTier(ctx context.Context, req *purserpb.CreateBillingTierRequest) (*purserpb.BillingTier, error)
	UpdateBillingTier(ctx context.Context, req *purserpb.UpdateBillingTierRequest) (*purserpb.BillingTier, error)
	GetSubscription(ctx context.Context, tenantID string) (*purserpb.GetSubscriptionResponse, error)
	CreateSubscription(ctx context.Context, req *purserpb.CreateSubscriptionRequest) (*purserpb.TenantSubscription, error)
	UpdateSubscription(ctx context.Context, req *purserpb.UpdateSubscriptionRequest) (*purserpb.TenantSubscription, error)
	CancelSubscription(ctx context.Context, tenantID string) error
	GetInvoice(ctx context.Context, invoiceID string) (*purserpb.GetInvoiceResponse, error)
	ListInvoices(ctx context.Context, tenantID string, status *string, pagination *commonpb.CursorPaginationRequest) (*purserpb.ListInvoicesResponse, error)
	CreatePayment(ctx context.Context, req *purserpb.PaymentRequest) (*purserpb.PaymentResponse, error)
	GetPaymentMethods(ctx context.Context, tenantID string) (*purserpb.PaymentMethodResponse, error)
	GetBillingStatus(ctx context.Context, tenantID string) (*purserpb.BillingStatusResponse, error)
	GetUsageRecords(ctx context.Context, tenantID, clusterID, usageType string, timeRange *commonpb.TimeRange, pagination *commonpb.CursorPaginationRequest) (*purserpb.UsageRecordsResponse, error)
	CheckUserLimit(ctx context.Context, tenantID, email string) (*purserpb.CheckUserLimitResponse, error)
	GetTenantUsage(ctx context.Context, tenantID, startDate, endDate string) (*purserpb.TenantUsageResponse, error)
	GetUsageAggregates(ctx context.Context, tenantID string, timeRange *commonpb.TimeRange, granularity string, usageTypes []string) (*purserpb.GetUsageAggregatesResponse, error)
	GetClusterPricing(ctx context.Context, clusterID string) (*purserpb.ClusterPricing, error)
	GetClustersPricingBatch(ctx context.Context, tenantID string, clusterIDs []string) (map[string]*purserpb.ClusterPricing, error)
	SetClusterPricing(ctx context.Context, req *purserpb.SetClusterPricingRequest) (*purserpb.ClusterPricing, error)
	ListClusterPricings(ctx context.Context, ownerTenantID string, pagination *commonpb.CursorPaginationRequest) (*purserpb.ListClusterPricingsResponse, error)
	CheckClusterAccess(ctx context.Context, tenantID, clusterID string) (*purserpb.CheckClusterAccessResponse, error)
	CreateClusterSubscription(ctx context.Context, tenantID, clusterID, inviteToken string) (*purserpb.ClusterSubscriptionResponse, error)
	CancelClusterSubscription(ctx context.Context, tenantID, clusterID string) error
	ListMarketplaceClusterPricings(ctx context.Context, req *purserpb.ListMarketplaceClusterPricingsRequest) (*purserpb.ListMarketplaceClusterPricingsResponse, error)
	GetPrepaidBalance(ctx context.Context, tenantID string, currency string) (*purserpb.PrepaidBalance, error)
	InitializePrepaidBalance(ctx context.Context, tenantID string, currency string, initialBalanceCents, thresholdCents int64) (*purserpb.PrepaidBalance, error)
	InitializePrepaidAccount(ctx context.Context, tenantID, currency string) (*purserpb.InitializePrepaidAccountResponse, error)
	InitializePostpaidAccount(ctx context.Context, tenantID string) (*purserpb.InitializePostpaidAccountResponse, error)
	TopupBalance(ctx context.Context, tenantID string, amountCents int64, currency, description string, referenceID, referenceType *string) (*purserpb.BalanceTransaction, error)
	DeductBalance(ctx context.Context, tenantID string, amountCents int64, currency, description string, referenceID, referenceType *string) (*purserpb.BalanceTransaction, error)
	AdjustBalance(ctx context.Context, tenantID string, amountCents int64, currency, description string, referenceID, referenceType *string) (*purserpb.BalanceTransaction, error)
	ListBalanceTransactions(ctx context.Context, tenantID string, transactionType *string, timeRange *commonpb.TimeRange, pagination *commonpb.CursorPaginationRequest) (*purserpb.ListBalanceTransactionsResponse, error)
	CreateCardTopup(ctx context.Context, req *purserpb.CreateCardTopupRequest) (*purserpb.CreateCardTopupResponse, error)
	CreateCryptoTopup(ctx context.Context, req *purserpb.CreateCryptoTopupRequest) (*purserpb.CreateCryptoTopupResponse, error)
	GetCryptoTopup(ctx context.Context, topupID string) (*purserpb.CryptoTopup, error)
	PromoteToPaid(ctx context.Context, tenantID, tierID string) (*purserpb.PromoteToPaidResponse, error)
	ChangeBillingTier(ctx context.Context, tenantID, tierID string) (*purserpb.ChangeBillingTierResponse, error)
	ProcessWebhook(ctx context.Context, req *sharedpb.WebhookRequest) (*sharedpb.WebhookResponse, error)
	CreateStripeCheckoutSession(ctx context.Context, tenantID, tierID, billingPeriod, successURL, cancelURL string) (*purserpb.CreateStripeCheckoutResponse, error)
	CreateStripeBillingPortal(ctx context.Context, tenantID, returnURL string) (*purserpb.CreateBillingPortalResponse, error)
	SyncStripeSubscription(ctx context.Context, tenantID string) (*purserpb.TenantSubscription, error)
	CreateMollieFirstPayment(ctx context.Context, tenantID, tierID, method, redirectURL string) (*purserpb.CreateMollieFirstPaymentResponse, error)
	CreateMollieSubscription(ctx context.Context, tenantID, tierID, mandateID, description string) (*purserpb.CreateMollieSubscriptionResponse, error)
	ListMollieMandates(ctx context.Context, tenantID string) (*purserpb.ListMollieMandatesResponse, error)
	CancelMollieSubscription(ctx context.Context, tenantID, subscriptionID string) error
	GetBillingDetails(ctx context.Context, tenantID string) (*purserpb.BillingDetails, error)
	UpdateBillingDetails(ctx context.Context, req *purserpb.UpdateBillingDetailsRequest) (*purserpb.BillingDetails, error)
	GetPaymentRequirements(ctx context.Context, tenantID, resource string) (*purserpb.PaymentRequirements, error)
	VerifyX402Payment(ctx context.Context, tenantID string, payment *x402pb.X402PaymentPayload, clientIP string) (*purserpb.VerifyX402PaymentResponse, error)
	SettleX402Payment(ctx context.Context, tenantID string, payment *x402pb.X402PaymentPayload, clientIP string) (*purserpb.SettleX402PaymentResponse, error)
	GetTenantX402Address(ctx context.Context, tenantID string) (*purserpb.GetTenantX402AddressResponse, error)
}

var _ Interface = (*GRPCClient)(nil)
