package purser

import (
	"context"
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const DefaultServerName = "purser.internal"

// GRPCClient is the gRPC client for Purser
type GRPCClient struct {
	conn           *grpc.ClientConn
	billing        purserpb.BillingServiceClient
	subscription   purserpb.SubscriptionServiceClient
	invoice        purserpb.InvoiceServiceClient
	payment        purserpb.PaymentServiceClient
	usage          purserpb.UsageServiceClient
	clusterPricing purserpb.ClusterPricingServiceClient
	prepaid        purserpb.PrepaidServiceClient
	webhook        purserpb.WebhookServiceClient
	stripe         purserpb.StripeServiceClient
	mollie         purserpb.MollieServiceClient
	x402           purserpb.X402ServiceClient
	logger         logging.Logger
}

// GRPCConfig represents the configuration for the gRPC client
type GRPCConfig struct {
	// GRPCAddr is the gRPC server address (host:port, no scheme)
	GRPCAddr string
	// Timeout for gRPC calls
	Timeout time.Duration
	// Logger for the client
	Logger logging.Logger
	// ServiceToken for service-to-service authentication (fallback when no user JWT)
	ServiceToken  string
	AllowInsecure bool
	CACertFile    string
	CACertPEM     string
	ServerName    string
}

// authInterceptor propagates authentication to gRPC metadata.
// This reads user_id, tenant_id, and jwt_token from the Go context (set by Gateway middleware)
// and adds them to outgoing gRPC metadata for downstream services.
// If no user JWT is available, it falls back to the service token for service-to-service calls.
func authInterceptor(serviceToken string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		// Extract user context from Go context and add to gRPC metadata
		md := metadata.MD{}

		if userID := ctxkeys.GetUserID(ctx); userID != "" {
			md.Set("x-user-id", userID)
		}
		if tenantID := ctxkeys.GetTenantID(ctx); tenantID != "" {
			md.Set("x-tenant-id", tenantID)
		}
		if ctxkeys.IsDemoMode(ctx) {
			md.Set("x-demo-mode", "true")
		}

		// Use user's JWT from context if available, otherwise fall back to service token
		if jwtToken := ctxkeys.GetJWTToken(ctx); jwtToken != "" {
			md.Set("authorization", "Bearer "+jwtToken)
		} else if serviceToken != "" {
			md.Set("authorization", "Bearer "+serviceToken)
		}

		// Merge with existing outgoing metadata if any
		if existingMD, ok := metadata.FromOutgoingContext(ctx); ok {
			md = metadata.Join(existingMD, md)
		}

		ctx = metadata.NewOutgoingContext(ctx, md)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// NewGRPCClient creates a new gRPC client for Purser
func NewGRPCClient(config GRPCConfig) (*GRPCClient, error) {
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}

	tlsCfg := grpcutil.ClientTLSConfig{
		CACertFile:        config.CACertFile,
		CACertPEM:         config.CACertPEM,
		ServerName:        config.ServerName,
		DefaultServerName: DefaultServerName,
		AllowInsecure:     config.AllowInsecure,
	}
	transport, err := grpcutil.ClientTLS(tlsCfg, config.Logger)
	if err != nil {
		return nil, fmt.Errorf("configure Purser gRPC TLS: %w", err)
	}

	// Connect to gRPC server with auth interceptor for user context and service token fallback
	conn, err := grpc.NewClient(
		config.GRPCAddr,
		transport,
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true)),
		grpc.WithChainUnaryInterceptor(
			authInterceptor(config.ServiceToken),
			clients.FailsafeUnaryInterceptor("purser", config.Logger),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Purser gRPC: %w", err)
	}

	return &GRPCClient{
		conn:           conn,
		billing:        purserpb.NewBillingServiceClient(conn),
		subscription:   purserpb.NewSubscriptionServiceClient(conn),
		invoice:        purserpb.NewInvoiceServiceClient(conn),
		payment:        purserpb.NewPaymentServiceClient(conn),
		usage:          purserpb.NewUsageServiceClient(conn),
		clusterPricing: purserpb.NewClusterPricingServiceClient(conn),
		prepaid:        purserpb.NewPrepaidServiceClient(conn),
		webhook:        purserpb.NewWebhookServiceClient(conn),
		stripe:         purserpb.NewStripeServiceClient(conn),
		mollie:         purserpb.NewMollieServiceClient(conn),
		x402:           purserpb.NewX402ServiceClient(conn),
		logger:         config.Logger,
	}, nil
}

// Close closes the gRPC connection
func (c *GRPCClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// ============================================================================
// CROSS-SERVICE BILLING STATUS
// ============================================================================

// GetTenantBillingStatus returns lightweight billing status for cross-service checks.
// Used by Commodore (ValidateStreamKey, isTenantSuspended) and Quartermaster (ValidateTenant).
// Returns: billing_model, is_suspended, is_balance_negative, balance_cents
func (c *GRPCClient) GetTenantBillingStatus(ctx context.Context, tenantID string) (*purserpb.GetTenantBillingStatusResponse, error) {
	return c.billing.GetTenantBillingStatus(ctx, &purserpb.GetTenantBillingStatusRequest{
		TenantId: tenantID,
	})
}

// ============================================================================
// BILLING TIER OPERATIONS
// ============================================================================

// GetBillingTiers returns available billing tiers with cursor pagination
func (c *GRPCClient) GetBillingTiers(ctx context.Context, includeInactive bool, pagination *commonpb.CursorPaginationRequest) (*purserpb.GetBillingTiersResponse, error) {
	return c.billing.GetBillingTiers(ctx, &purserpb.GetBillingTiersRequest{
		IncludeInactive: includeInactive,
		Pagination:      pagination,
	})
}

// GetBillingTier returns a specific billing tier
func (c *GRPCClient) GetBillingTier(ctx context.Context, tierID string) (*purserpb.BillingTier, error) {
	return c.billing.GetBillingTier(ctx, &purserpb.GetBillingTierRequest{
		TierId: tierID,
	})
}

// CreateBillingTier creates a new billing tier
func (c *GRPCClient) CreateBillingTier(ctx context.Context, req *purserpb.CreateBillingTierRequest) (*purserpb.BillingTier, error) {
	return c.billing.CreateBillingTier(ctx, req)
}

// UpdateBillingTier updates a billing tier
func (c *GRPCClient) UpdateBillingTier(ctx context.Context, req *purserpb.UpdateBillingTierRequest) (*purserpb.BillingTier, error) {
	return c.billing.UpdateBillingTier(ctx, req)
}

// ============================================================================
// SUBSCRIPTION OPERATIONS
// ============================================================================

// GetSubscription gets a tenant's subscription
func (c *GRPCClient) GetSubscription(ctx context.Context, tenantID string) (*purserpb.GetSubscriptionResponse, error) {
	return c.subscription.GetSubscription(ctx, &purserpb.GetSubscriptionRequest{
		TenantId: tenantID,
	})
}

// CreateSubscription creates a new subscription
func (c *GRPCClient) CreateSubscription(ctx context.Context, req *purserpb.CreateSubscriptionRequest) (*purserpb.TenantSubscription, error) {
	return c.subscription.CreateSubscription(ctx, req)
}

// UpdateSubscription updates a subscription
func (c *GRPCClient) UpdateSubscription(ctx context.Context, req *purserpb.UpdateSubscriptionRequest) (*purserpb.TenantSubscription, error) {
	return c.subscription.UpdateSubscription(ctx, req)
}

// CancelSubscription cancels a subscription
func (c *GRPCClient) CancelSubscription(ctx context.Context, tenantID string) error {
	_, err := c.subscription.CancelSubscription(ctx, &purserpb.CancelSubscriptionRequest{
		TenantId: tenantID,
	})
	return err
}

// ============================================================================
// INVOICE OPERATIONS
// ============================================================================

// GetInvoice gets an invoice by ID
func (c *GRPCClient) GetInvoice(ctx context.Context, invoiceID string) (*purserpb.GetInvoiceResponse, error) {
	return c.invoice.GetInvoice(ctx, &purserpb.GetInvoiceRequest{
		InvoiceId: invoiceID,
	})
}

// ListInvoices lists invoices for a tenant with cursor pagination
func (c *GRPCClient) ListInvoices(ctx context.Context, tenantID string, status *string, pagination *commonpb.CursorPaginationRequest) (*purserpb.ListInvoicesResponse, error) {
	req := &purserpb.ListInvoicesRequest{
		TenantId:   tenantID,
		Pagination: pagination,
	}
	if status != nil {
		req.Status = status
	}
	return c.invoice.ListInvoices(ctx, req)
}

// ============================================================================
// PAYMENT OPERATIONS
// ============================================================================

// CreatePayment initiates a payment for an invoice
func (c *GRPCClient) CreatePayment(ctx context.Context, req *purserpb.PaymentRequest) (*purserpb.PaymentResponse, error) {
	return c.payment.CreatePayment(ctx, req)
}

// GetPaymentMethods returns available payment methods
func (c *GRPCClient) GetPaymentMethods(ctx context.Context, tenantID string) (*purserpb.PaymentMethodResponse, error) {
	return c.payment.GetPaymentMethods(ctx, &purserpb.GetPaymentMethodsRequest{
		TenantId: tenantID,
	})
}

// GetBillingStatus returns billing status for a tenant
func (c *GRPCClient) GetBillingStatus(ctx context.Context, tenantID string) (*purserpb.BillingStatusResponse, error) {
	return c.payment.GetBillingStatus(ctx, &purserpb.GetBillingStatusRequest{
		TenantId: tenantID,
	})
}

// ============================================================================
// USAGE OPERATIONS
// NOTE: Usage ingestion is handled via Kafka (billing.usage_reports topic),
// not gRPC. Periscope sends usage summaries to Kafka, and Purser's JobManager
// consumes them.
// ============================================================================

// GetUsageRecords returns usage records for a tenant with cursor pagination
func (c *GRPCClient) GetUsageRecords(ctx context.Context, tenantID, clusterID, usageType string, timeRange *commonpb.TimeRange, pagination *commonpb.CursorPaginationRequest) (*purserpb.UsageRecordsResponse, error) {
	return c.usage.GetUsageRecords(ctx, &purserpb.GetUsageRecordsRequest{
		TenantId:   tenantID,
		ClusterId:  clusterID,
		UsageType:  usageType,
		TimeRange:  timeRange,
		Pagination: pagination,
	})
}

// CheckUserLimit checks if a tenant can add more users
func (c *GRPCClient) CheckUserLimit(ctx context.Context, tenantID, email string) (*purserpb.CheckUserLimitResponse, error) {
	return c.usage.CheckUserLimit(ctx, &purserpb.CheckUserLimitRequest{
		TenantId: tenantID,
		Email:    email,
	})
}

// GetTenantUsage returns usage summary for a tenant over a date range
func (c *GRPCClient) GetTenantUsage(ctx context.Context, tenantID, startDate, endDate string) (*purserpb.TenantUsageResponse, error) {
	return c.usage.GetTenantUsage(ctx, &purserpb.TenantUsageRequest{
		TenantId:  tenantID,
		StartDate: startDate,
		EndDate:   endDate,
	})
}

// GetUsageAggregates returns rollup-backed usage aggregates for charts
func (c *GRPCClient) GetUsageAggregates(ctx context.Context, tenantID string, timeRange *commonpb.TimeRange, granularity string, usageTypes []string) (*purserpb.GetUsageAggregatesResponse, error) {
	return c.usage.GetUsageAggregates(ctx, &purserpb.GetUsageAggregatesRequest{
		TenantId:    tenantID,
		TimeRange:   timeRange,
		Granularity: granularity,
		UsageTypes:  usageTypes,
	})
}

// ============================================================================
// CLUSTER PRICING OPERATIONS
// ============================================================================

// GetClusterPricing returns pricing config for a cluster
func (c *GRPCClient) GetClusterPricing(ctx context.Context, clusterID string) (*purserpb.ClusterPricing, error) {
	return c.clusterPricing.GetClusterPricing(ctx, &purserpb.GetClusterPricingRequest{
		ClusterId: clusterID,
	})
}

// GetClustersPricingBatch returns pricing configs for multiple clusters with eligibility info.
func (c *GRPCClient) GetClustersPricingBatch(ctx context.Context, tenantID string, clusterIDs []string) (map[string]*purserpb.ClusterPricing, error) {
	req := &purserpb.GetClustersPricingBatchRequest{ClusterIds: clusterIDs}
	if tenantID != "" {
		req.TenantId = &tenantID
	}
	resp, err := c.clusterPricing.GetClustersPricingBatch(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Pricings, nil
}

// SetClusterPricing creates or updates pricing config for a cluster
func (c *GRPCClient) SetClusterPricing(ctx context.Context, req *purserpb.SetClusterPricingRequest) (*purserpb.ClusterPricing, error) {
	return c.clusterPricing.SetClusterPricing(ctx, req)
}

// ListClusterPricings returns pricing configs for clusters owned by a tenant
func (c *GRPCClient) ListClusterPricings(ctx context.Context, ownerTenantID string, pagination *commonpb.CursorPaginationRequest) (*purserpb.ListClusterPricingsResponse, error) {
	return c.clusterPricing.ListClusterPricings(ctx, &purserpb.ListClusterPricingsRequest{
		OwnerTenantId: ownerTenantID,
		Pagination:    pagination,
	})
}

// CheckClusterAccess checks if a tenant can subscribe to a cluster
func (c *GRPCClient) CheckClusterAccess(ctx context.Context, tenantID, clusterID string) (*purserpb.CheckClusterAccessResponse, error) {
	return c.clusterPricing.CheckClusterAccess(ctx, &purserpb.CheckClusterAccessRequest{
		TenantId:  tenantID,
		ClusterId: clusterID,
	})
}

// CreateClusterSubscription creates a subscription for a tenant to a cluster
func (c *GRPCClient) CreateClusterSubscription(ctx context.Context, tenantID, clusterID, inviteToken string) (*purserpb.ClusterSubscriptionResponse, error) {
	req := &purserpb.CreateClusterSubscriptionRequest{
		TenantId:  tenantID,
		ClusterId: clusterID,
	}
	if inviteToken != "" {
		req.InviteToken = &inviteToken
	}
	return c.clusterPricing.CreateClusterSubscription(ctx, req)
}

// CancelClusterSubscription cancels a tenant's subscription to a cluster
func (c *GRPCClient) CancelClusterSubscription(ctx context.Context, tenantID, clusterID string) error {
	_, err := c.clusterPricing.CancelClusterSubscription(ctx, &purserpb.CancelClusterSubscriptionRequest{
		TenantId:  tenantID,
		ClusterId: clusterID,
	})
	return err
}

// ListMarketplaceClusterPricings returns paginated cluster pricings filtered by tenant tier level.
// Used as the primary marketplace query - Gateway enriches with Quartermaster metadata.
func (c *GRPCClient) ListMarketplaceClusterPricings(ctx context.Context, req *purserpb.ListMarketplaceClusterPricingsRequest) (*purserpb.ListMarketplaceClusterPricingsResponse, error) {
	return c.clusterPricing.ListMarketplaceClusterPricings(ctx, req)
}

// ============================================================================
// PREPAID BALANCE OPERATIONS
// ============================================================================

// GetPrepaidBalance returns the current prepaid balance for a tenant
func (c *GRPCClient) GetPrepaidBalance(ctx context.Context, tenantID string, currency string) (*purserpb.PrepaidBalance, error) {
	return c.prepaid.GetPrepaidBalance(ctx, &purserpb.GetPrepaidBalanceRequest{
		TenantId: tenantID,
		Currency: currency,
	})
}

// InitializePrepaidBalance creates a new prepaid balance for a tenant
func (c *GRPCClient) InitializePrepaidBalance(ctx context.Context, tenantID string, currency string, initialBalanceCents, thresholdCents int64) (*purserpb.PrepaidBalance, error) {
	return c.prepaid.InitializePrepaidBalance(ctx, &purserpb.InitializePrepaidBalanceRequest{
		TenantId:                 tenantID,
		Currency:                 currency,
		InitialBalanceCents:      initialBalanceCents,
		LowBalanceThresholdCents: thresholdCents,
	})
}

// InitializePrepaidAccount creates subscription + prepaid balance for wallet provisioning.
// Called by Commodore during GetOrCreateWalletUser to avoid cross-service DB inserts.
// Creates: 1) subscription with billing_model='prepaid', 2) prepaid balance at 0.
func (c *GRPCClient) InitializePrepaidAccount(ctx context.Context, tenantID, currency string) (*purserpb.InitializePrepaidAccountResponse, error) {
	return c.prepaid.InitializePrepaidAccount(ctx, &purserpb.InitializePrepaidAccountRequest{
		TenantId: tenantID,
		Currency: currency,
	})
}

// InitializePostpaidAccount creates a postpaid subscription for email registration.
// Resolves the default postpaid tier and provisions cluster access.
func (c *GRPCClient) InitializePostpaidAccount(ctx context.Context, tenantID string) (*purserpb.InitializePostpaidAccountResponse, error) {
	return c.prepaid.InitializePostpaidAccount(ctx, &purserpb.InitializePostpaidAccountRequest{
		TenantId: tenantID,
	})
}

// TopupBalance adds funds to a tenant's prepaid balance
func (c *GRPCClient) TopupBalance(ctx context.Context, tenantID string, amountCents int64, currency, description string, referenceID, referenceType *string) (*purserpb.BalanceTransaction, error) {
	return c.prepaid.TopupBalance(ctx, &purserpb.TopupBalanceRequest{
		TenantId:      tenantID,
		AmountCents:   amountCents,
		Currency:      currency,
		Description:   description,
		ReferenceId:   referenceID,
		ReferenceType: referenceType,
	})
}

// DeductBalance removes funds from a tenant's prepaid balance
func (c *GRPCClient) DeductBalance(ctx context.Context, tenantID string, amountCents int64, currency, description string, referenceID, referenceType *string) (*purserpb.BalanceTransaction, error) {
	return c.prepaid.DeductBalance(ctx, &purserpb.DeductBalanceRequest{
		TenantId:      tenantID,
		AmountCents:   amountCents,
		Currency:      currency,
		Description:   description,
		ReferenceId:   referenceID,
		ReferenceType: referenceType,
	})
}

// AdjustBalance manually adjusts a tenant's prepaid balance (admin only)
func (c *GRPCClient) AdjustBalance(ctx context.Context, tenantID string, amountCents int64, currency, description string, referenceID, referenceType *string) (*purserpb.BalanceTransaction, error) {
	return c.prepaid.AdjustBalance(ctx, &purserpb.AdjustBalanceRequest{
		TenantId:      tenantID,
		AmountCents:   amountCents,
		Currency:      currency,
		Description:   description,
		ReferenceId:   referenceID,
		ReferenceType: referenceType,
	})
}

// ListBalanceTransactions returns transaction history for a tenant
func (c *GRPCClient) ListBalanceTransactions(ctx context.Context, tenantID string, transactionType *string, timeRange *commonpb.TimeRange, pagination *commonpb.CursorPaginationRequest) (*purserpb.ListBalanceTransactionsResponse, error) {
	return c.prepaid.ListBalanceTransactions(ctx, &purserpb.ListBalanceTransactionsRequest{
		TenantId:        tenantID,
		TransactionType: transactionType,
		TimeRange:       timeRange,
		Pagination:      pagination,
	})
}

// CreateCardTopup creates a pending card top-up and returns a checkout URL
func (c *GRPCClient) CreateCardTopup(ctx context.Context, req *purserpb.CreateCardTopupRequest) (*purserpb.CreateCardTopupResponse, error) {
	return c.prepaid.CreateCardTopup(ctx, req)
}

// CreateCryptoTopup generates a crypto deposit address for prepaid balance top-up
func (c *GRPCClient) CreateCryptoTopup(ctx context.Context, req *purserpb.CreateCryptoTopupRequest) (*purserpb.CreateCryptoTopupResponse, error) {
	return c.prepaid.CreateCryptoTopup(ctx, req)
}

// GetCryptoTopup returns the status of a crypto top-up
func (c *GRPCClient) GetCryptoTopup(ctx context.Context, topupID string) (*purserpb.CryptoTopup, error) {
	return c.prepaid.GetCryptoTopup(ctx, &purserpb.GetCryptoTopupRequest{TopupId: topupID})
}

// PromoteToPaid upgrades a prepaid account to postpaid billing
func (c *GRPCClient) PromoteToPaid(ctx context.Context, tenantID, tierID string) (*purserpb.PromoteToPaidResponse, error) {
	return c.prepaid.PromoteToPaid(ctx, &purserpb.PromoteToPaidRequest{
		TenantId: tenantID,
		TierId:   tierID,
	})
}

// ChangeBillingTier changes a postpaid tenant's tier. Upgrades apply
// immediately; downgrades are scheduled by Purser for period close.
func (c *GRPCClient) ChangeBillingTier(ctx context.Context, tenantID, tierID string) (*purserpb.ChangeBillingTierResponse, error) {
	return c.prepaid.ChangeBillingTier(ctx, &purserpb.ChangeBillingTierRequest{
		TenantId: tenantID,
		TierId:   tierID,
	})
}

// ============================================================================
// WEBHOOK OPERATIONS
// ============================================================================

// ProcessWebhook forwards an incoming webhook to Purser for processing.
// This is called by the Gateway's webhook router - signature verification
// happens in Purser (secrets stay there).
func (c *GRPCClient) ProcessWebhook(ctx context.Context, req *sharedpb.WebhookRequest) (*sharedpb.WebhookResponse, error) {
	return c.webhook.ProcessWebhook(ctx, req)
}

// ============================================================================
// STRIPE OPERATIONS
// ============================================================================

// CreateStripeCheckoutSession creates a Stripe Checkout Session for subscription setup
func (c *GRPCClient) CreateStripeCheckoutSession(ctx context.Context, tenantID, tierID, billingPeriod, successURL, cancelURL string) (*purserpb.CreateStripeCheckoutResponse, error) {
	return c.stripe.CreateCheckoutSession(ctx, &purserpb.CreateStripeCheckoutRequest{
		TenantId:      tenantID,
		TierId:        tierID,
		BillingPeriod: billingPeriod,
		SuccessUrl:    successURL,
		CancelUrl:     cancelURL,
	})
}

// CreateStripeBillingPortal creates a Stripe Billing Portal session for subscription management
func (c *GRPCClient) CreateStripeBillingPortal(ctx context.Context, tenantID, returnURL string) (*purserpb.CreateBillingPortalResponse, error) {
	return c.stripe.CreateBillingPortalSession(ctx, &purserpb.CreateBillingPortalRequest{
		TenantId:  tenantID,
		ReturnUrl: returnURL,
	})
}

// SyncStripeSubscription syncs subscription state from Stripe (admin/debug)
func (c *GRPCClient) SyncStripeSubscription(ctx context.Context, tenantID string) (*purserpb.TenantSubscription, error) {
	return c.stripe.SyncSubscription(ctx, &purserpb.SyncStripeSubscriptionRequest{
		TenantId: tenantID,
	})
}

// ============================================================================
// MOLLIE OPERATIONS
// ============================================================================

// CreateMollieFirstPayment creates a Mollie first payment to establish a mandate.
// After successful payment, a SEPA DD mandate is created for recurring billing.
func (c *GRPCClient) CreateMollieFirstPayment(ctx context.Context, tenantID, tierID, method, redirectURL string) (*purserpb.CreateMollieFirstPaymentResponse, error) {
	return c.mollie.CreateFirstPayment(ctx, &purserpb.CreateMollieFirstPaymentRequest{
		TenantId:    tenantID,
		TierId:      tierID,
		Method:      method,
		RedirectUrl: redirectURL,
	})
}

// CreateMollieSubscription creates a Mollie subscription after mandate is valid
func (c *GRPCClient) CreateMollieSubscription(ctx context.Context, tenantID, tierID, mandateID, description string) (*purserpb.CreateMollieSubscriptionResponse, error) {
	return c.mollie.CreateMollieSubscription(ctx, &purserpb.CreateMollieSubscriptionRequest{
		TenantId:    tenantID,
		TierId:      tierID,
		MandateId:   mandateID,
		Description: description,
	})
}

// ListMollieMandates lists available payment mandates for a tenant
func (c *GRPCClient) ListMollieMandates(ctx context.Context, tenantID string) (*purserpb.ListMollieMandatesResponse, error) {
	return c.mollie.ListMandates(ctx, &purserpb.ListMollieMandatesRequest{
		TenantId: tenantID,
	})
}

// CancelMollieSubscription cancels a Mollie subscription
func (c *GRPCClient) CancelMollieSubscription(ctx context.Context, tenantID, subscriptionID string) error {
	_, err := c.mollie.CancelMollieSubscription(ctx, &purserpb.CancelMollieSubscriptionRequest{
		TenantId:       tenantID,
		SubscriptionId: subscriptionID,
	})
	return err
}

// ============================================================================
// BILLING DETAILS OPERATIONS
// ============================================================================

// GetBillingDetails returns billing details for a tenant
func (c *GRPCClient) GetBillingDetails(ctx context.Context, tenantID string) (*purserpb.BillingDetails, error) {
	return c.subscription.GetBillingDetails(ctx, &purserpb.GetBillingDetailsRequest{
		TenantId: tenantID,
	})
}

// UpdateBillingDetails updates billing details for a tenant
func (c *GRPCClient) UpdateBillingDetails(ctx context.Context, req *purserpb.UpdateBillingDetailsRequest) (*purserpb.BillingDetails, error) {
	return c.subscription.UpdateBillingDetails(ctx, req)
}

// ============================================================================
// X402 PAYMENT OPERATIONS
// ============================================================================

// GetPaymentRequirements returns x402 payment requirements for a 402 response.
// Called by Gateway when returning HTTP 402 to include payment options.
func (c *GRPCClient) GetPaymentRequirements(ctx context.Context, tenantID, resource string) (*purserpb.PaymentRequirements, error) {
	return c.x402.GetPaymentRequirements(ctx, &purserpb.GetPaymentRequirementsRequest{
		TenantId: tenantID,
		Resource: resource,
	})
}

// VerifyX402Payment verifies an x402 payment payload without settling.
// Returns validity status, payer address, amount, and whether billing details are required.
func (c *GRPCClient) VerifyX402Payment(ctx context.Context, tenantID string, payment *purserpb.X402PaymentPayload, clientIP string) (*purserpb.VerifyX402PaymentResponse, error) {
	return c.x402.VerifyX402Payment(ctx, &purserpb.VerifyX402PaymentRequest{
		TenantId: tenantID,
		Payment:  payment,
		ClientIp: clientIP,
	})
}

// SettleX402Payment settles an x402 payment on-chain and credits the tenant's balance.
// Returns transaction hash, credited amount, and new balance.
func (c *GRPCClient) SettleX402Payment(ctx context.Context, tenantID string, payment *purserpb.X402PaymentPayload, clientIP string) (*purserpb.SettleX402PaymentResponse, error) {
	return c.x402.SettleX402Payment(ctx, &purserpb.SettleX402PaymentRequest{
		TenantId: tenantID,
		Payment:  payment,
		ClientIp: clientIP,
	})
}

// GetTenantX402Address returns the per-tenant x402 deposit address.
// Creates a new address on first call for a tenant.
func (c *GRPCClient) GetTenantX402Address(ctx context.Context, tenantID string) (*purserpb.GetTenantX402AddressResponse, error) {
	return c.x402.GetTenantX402Address(ctx, &purserpb.GetTenantX402AddressRequest{
		TenantId: tenantID,
	})
}
