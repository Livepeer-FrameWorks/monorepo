package purser

import (
	"context"
	"fmt"
	"time"

	"frameworks/pkg/clients"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// GRPCClient is the gRPC client for Purser
type GRPCClient struct {
	conn           *grpc.ClientConn
	billing        pb.BillingServiceClient
	subscription   pb.SubscriptionServiceClient
	invoice        pb.InvoiceServiceClient
	payment        pb.PaymentServiceClient
	usage          pb.UsageServiceClient
	clusterPricing pb.ClusterPricingServiceClient
	prepaid        pb.PrepaidServiceClient
	webhook        pb.WebhookServiceClient
	stripe         pb.StripeServiceClient
	mollie         pb.MollieServiceClient
	x402           pb.X402ServiceClient
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
	ServiceToken string
}

// authInterceptor propagates authentication to gRPC metadata.
// This reads user_id, tenant_id, and jwt_token from the Go context (set by Gateway middleware)
// and adds them to outgoing gRPC metadata for downstream services.
// If no user JWT is available, it falls back to the service token for service-to-service calls.
func authInterceptor(serviceToken string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
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

	// Connect to gRPC server with auth interceptor for user context and service token fallback
	conn, err := grpc.NewClient(
		config.GRPCAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
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
		billing:        pb.NewBillingServiceClient(conn),
		subscription:   pb.NewSubscriptionServiceClient(conn),
		invoice:        pb.NewInvoiceServiceClient(conn),
		payment:        pb.NewPaymentServiceClient(conn),
		usage:          pb.NewUsageServiceClient(conn),
		clusterPricing: pb.NewClusterPricingServiceClient(conn),
		prepaid:        pb.NewPrepaidServiceClient(conn),
		webhook:        pb.NewWebhookServiceClient(conn),
		stripe:         pb.NewStripeServiceClient(conn),
		mollie:         pb.NewMollieServiceClient(conn),
		x402:           pb.NewX402ServiceClient(conn),
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
func (c *GRPCClient) GetTenantBillingStatus(ctx context.Context, tenantID string) (*pb.GetTenantBillingStatusResponse, error) {
	return c.billing.GetTenantBillingStatus(ctx, &pb.GetTenantBillingStatusRequest{
		TenantId: tenantID,
	})
}

// ============================================================================
// BILLING TIER OPERATIONS
// ============================================================================

// GetBillingTiers returns available billing tiers with cursor pagination
func (c *GRPCClient) GetBillingTiers(ctx context.Context, includeInactive bool, pagination *pb.CursorPaginationRequest) (*pb.GetBillingTiersResponse, error) {
	return c.billing.GetBillingTiers(ctx, &pb.GetBillingTiersRequest{
		IncludeInactive: includeInactive,
		Pagination:      pagination,
	})
}

// GetBillingTier returns a specific billing tier
func (c *GRPCClient) GetBillingTier(ctx context.Context, tierID string) (*pb.BillingTier, error) {
	return c.billing.GetBillingTier(ctx, &pb.GetBillingTierRequest{
		TierId: tierID,
	})
}

// CreateBillingTier creates a new billing tier
func (c *GRPCClient) CreateBillingTier(ctx context.Context, req *pb.CreateBillingTierRequest) (*pb.BillingTier, error) {
	return c.billing.CreateBillingTier(ctx, req)
}

// UpdateBillingTier updates a billing tier
func (c *GRPCClient) UpdateBillingTier(ctx context.Context, req *pb.UpdateBillingTierRequest) (*pb.BillingTier, error) {
	return c.billing.UpdateBillingTier(ctx, req)
}

// ============================================================================
// SUBSCRIPTION OPERATIONS
// ============================================================================

// GetSubscription gets a tenant's subscription
func (c *GRPCClient) GetSubscription(ctx context.Context, tenantID string) (*pb.GetSubscriptionResponse, error) {
	return c.subscription.GetSubscription(ctx, &pb.GetSubscriptionRequest{
		TenantId: tenantID,
	})
}

// CreateSubscription creates a new subscription
func (c *GRPCClient) CreateSubscription(ctx context.Context, req *pb.CreateSubscriptionRequest) (*pb.TenantSubscription, error) {
	return c.subscription.CreateSubscription(ctx, req)
}

// UpdateSubscription updates a subscription
func (c *GRPCClient) UpdateSubscription(ctx context.Context, req *pb.UpdateSubscriptionRequest) (*pb.TenantSubscription, error) {
	return c.subscription.UpdateSubscription(ctx, req)
}

// CancelSubscription cancels a subscription
func (c *GRPCClient) CancelSubscription(ctx context.Context, tenantID string) error {
	_, err := c.subscription.CancelSubscription(ctx, &pb.CancelSubscriptionRequest{
		TenantId: tenantID,
	})
	return err
}

// ============================================================================
// INVOICE OPERATIONS
// ============================================================================

// GetInvoice gets an invoice by ID
func (c *GRPCClient) GetInvoice(ctx context.Context, invoiceID string) (*pb.GetInvoiceResponse, error) {
	return c.invoice.GetInvoice(ctx, &pb.GetInvoiceRequest{
		InvoiceId: invoiceID,
	})
}

// ListInvoices lists invoices for a tenant with cursor pagination
func (c *GRPCClient) ListInvoices(ctx context.Context, tenantID string, status *string, pagination *pb.CursorPaginationRequest) (*pb.ListInvoicesResponse, error) {
	req := &pb.ListInvoicesRequest{
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
func (c *GRPCClient) CreatePayment(ctx context.Context, req *pb.PaymentRequest) (*pb.PaymentResponse, error) {
	return c.payment.CreatePayment(ctx, req)
}

// GetPaymentMethods returns available payment methods
func (c *GRPCClient) GetPaymentMethods(ctx context.Context, tenantID string) (*pb.PaymentMethodResponse, error) {
	return c.payment.GetPaymentMethods(ctx, &pb.GetPaymentMethodsRequest{
		TenantId: tenantID,
	})
}

// GetBillingStatus returns billing status for a tenant
func (c *GRPCClient) GetBillingStatus(ctx context.Context, tenantID string) (*pb.BillingStatusResponse, error) {
	return c.payment.GetBillingStatus(ctx, &pb.GetBillingStatusRequest{
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
func (c *GRPCClient) GetUsageRecords(ctx context.Context, tenantID, clusterID, usageType string, timeRange *pb.TimeRange, pagination *pb.CursorPaginationRequest) (*pb.UsageRecordsResponse, error) {
	return c.usage.GetUsageRecords(ctx, &pb.GetUsageRecordsRequest{
		TenantId:   tenantID,
		ClusterId:  clusterID,
		UsageType:  usageType,
		TimeRange:  timeRange,
		Pagination: pagination,
	})
}

// CheckUserLimit checks if a tenant can add more users
func (c *GRPCClient) CheckUserLimit(ctx context.Context, tenantID, email string) (*pb.CheckUserLimitResponse, error) {
	return c.usage.CheckUserLimit(ctx, &pb.CheckUserLimitRequest{
		TenantId: tenantID,
		Email:    email,
	})
}

// GetTenantUsage returns usage summary for a tenant over a date range
func (c *GRPCClient) GetTenantUsage(ctx context.Context, tenantID, startDate, endDate string) (*pb.TenantUsageResponse, error) {
	return c.usage.GetTenantUsage(ctx, &pb.TenantUsageRequest{
		TenantId:  tenantID,
		StartDate: startDate,
		EndDate:   endDate,
	})
}

// GetUsageAggregates returns rollup-backed usage aggregates for charts
func (c *GRPCClient) GetUsageAggregates(ctx context.Context, tenantID string, timeRange *pb.TimeRange, granularity string, usageTypes []string) (*pb.GetUsageAggregatesResponse, error) {
	return c.usage.GetUsageAggregates(ctx, &pb.GetUsageAggregatesRequest{
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
func (c *GRPCClient) GetClusterPricing(ctx context.Context, clusterID string) (*pb.ClusterPricing, error) {
	return c.clusterPricing.GetClusterPricing(ctx, &pb.GetClusterPricingRequest{
		ClusterId: clusterID,
	})
}

// GetClustersPricingBatch returns pricing configs for multiple clusters with eligibility info.
func (c *GRPCClient) GetClustersPricingBatch(ctx context.Context, tenantID string, clusterIDs []string) (map[string]*pb.ClusterPricing, error) {
	req := &pb.GetClustersPricingBatchRequest{ClusterIds: clusterIDs}
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
func (c *GRPCClient) SetClusterPricing(ctx context.Context, req *pb.SetClusterPricingRequest) (*pb.ClusterPricing, error) {
	return c.clusterPricing.SetClusterPricing(ctx, req)
}

// ListClusterPricings returns pricing configs for clusters owned by a tenant
func (c *GRPCClient) ListClusterPricings(ctx context.Context, ownerTenantID string, pagination *pb.CursorPaginationRequest) (*pb.ListClusterPricingsResponse, error) {
	return c.clusterPricing.ListClusterPricings(ctx, &pb.ListClusterPricingsRequest{
		OwnerTenantId: ownerTenantID,
		Pagination:    pagination,
	})
}

// CheckClusterAccess checks if a tenant can subscribe to a cluster
func (c *GRPCClient) CheckClusterAccess(ctx context.Context, tenantID, clusterID string) (*pb.CheckClusterAccessResponse, error) {
	return c.clusterPricing.CheckClusterAccess(ctx, &pb.CheckClusterAccessRequest{
		TenantId:  tenantID,
		ClusterId: clusterID,
	})
}

// CreateClusterSubscription creates a subscription for a tenant to a cluster
func (c *GRPCClient) CreateClusterSubscription(ctx context.Context, tenantID, clusterID, inviteToken string) (*pb.ClusterSubscriptionResponse, error) {
	req := &pb.CreateClusterSubscriptionRequest{
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
	_, err := c.clusterPricing.CancelClusterSubscription(ctx, &pb.CancelClusterSubscriptionRequest{
		TenantId:  tenantID,
		ClusterId: clusterID,
	})
	return err
}

// ListMarketplaceClusterPricings returns paginated cluster pricings filtered by tenant tier level.
// Used as the primary marketplace query - Gateway enriches with Quartermaster metadata.
func (c *GRPCClient) ListMarketplaceClusterPricings(ctx context.Context, req *pb.ListMarketplaceClusterPricingsRequest) (*pb.ListMarketplaceClusterPricingsResponse, error) {
	return c.clusterPricing.ListMarketplaceClusterPricings(ctx, req)
}

// ============================================================================
// PREPAID BALANCE OPERATIONS
// ============================================================================

// GetPrepaidBalance returns the current prepaid balance for a tenant
func (c *GRPCClient) GetPrepaidBalance(ctx context.Context, tenantID string, currency string) (*pb.PrepaidBalance, error) {
	return c.prepaid.GetPrepaidBalance(ctx, &pb.GetPrepaidBalanceRequest{
		TenantId: tenantID,
		Currency: currency,
	})
}

// InitializePrepaidBalance creates a new prepaid balance for a tenant
func (c *GRPCClient) InitializePrepaidBalance(ctx context.Context, tenantID string, currency string, initialBalanceCents, thresholdCents int64) (*pb.PrepaidBalance, error) {
	return c.prepaid.InitializePrepaidBalance(ctx, &pb.InitializePrepaidBalanceRequest{
		TenantId:                 tenantID,
		Currency:                 currency,
		InitialBalanceCents:      initialBalanceCents,
		LowBalanceThresholdCents: thresholdCents,
	})
}

// InitializePrepaidAccount creates subscription + prepaid balance for wallet provisioning.
// Called by Commodore during GetOrCreateWalletUser to avoid cross-service DB inserts.
// Creates: 1) subscription with billing_model='prepaid', 2) prepaid balance at 0.
func (c *GRPCClient) InitializePrepaidAccount(ctx context.Context, tenantID, currency string) (*pb.InitializePrepaidAccountResponse, error) {
	return c.prepaid.InitializePrepaidAccount(ctx, &pb.InitializePrepaidAccountRequest{
		TenantId: tenantID,
		Currency: currency,
	})
}

// InitializePostpaidAccount creates a postpaid subscription for email registration.
// Resolves the default postpaid tier and provisions cluster access.
func (c *GRPCClient) InitializePostpaidAccount(ctx context.Context, tenantID string) (*pb.InitializePostpaidAccountResponse, error) {
	return c.prepaid.InitializePostpaidAccount(ctx, &pb.InitializePostpaidAccountRequest{
		TenantId: tenantID,
	})
}

// TopupBalance adds funds to a tenant's prepaid balance
func (c *GRPCClient) TopupBalance(ctx context.Context, tenantID string, amountCents int64, currency, description string, referenceID, referenceType *string) (*pb.BalanceTransaction, error) {
	return c.prepaid.TopupBalance(ctx, &pb.TopupBalanceRequest{
		TenantId:      tenantID,
		AmountCents:   amountCents,
		Currency:      currency,
		Description:   description,
		ReferenceId:   referenceID,
		ReferenceType: referenceType,
	})
}

// DeductBalance removes funds from a tenant's prepaid balance
func (c *GRPCClient) DeductBalance(ctx context.Context, tenantID string, amountCents int64, currency, description string, referenceID, referenceType *string) (*pb.BalanceTransaction, error) {
	return c.prepaid.DeductBalance(ctx, &pb.DeductBalanceRequest{
		TenantId:      tenantID,
		AmountCents:   amountCents,
		Currency:      currency,
		Description:   description,
		ReferenceId:   referenceID,
		ReferenceType: referenceType,
	})
}

// AdjustBalance manually adjusts a tenant's prepaid balance (admin only)
func (c *GRPCClient) AdjustBalance(ctx context.Context, tenantID string, amountCents int64, currency, description string, referenceID, referenceType *string) (*pb.BalanceTransaction, error) {
	return c.prepaid.AdjustBalance(ctx, &pb.AdjustBalanceRequest{
		TenantId:      tenantID,
		AmountCents:   amountCents,
		Currency:      currency,
		Description:   description,
		ReferenceId:   referenceID,
		ReferenceType: referenceType,
	})
}

// ListBalanceTransactions returns transaction history for a tenant
func (c *GRPCClient) ListBalanceTransactions(ctx context.Context, tenantID string, transactionType *string, timeRange *pb.TimeRange, pagination *pb.CursorPaginationRequest) (*pb.ListBalanceTransactionsResponse, error) {
	return c.prepaid.ListBalanceTransactions(ctx, &pb.ListBalanceTransactionsRequest{
		TenantId:        tenantID,
		TransactionType: transactionType,
		TimeRange:       timeRange,
		Pagination:      pagination,
	})
}

// CreateCardTopup creates a pending card top-up and returns a checkout URL
func (c *GRPCClient) CreateCardTopup(ctx context.Context, req *pb.CreateCardTopupRequest) (*pb.CreateCardTopupResponse, error) {
	return c.prepaid.CreateCardTopup(ctx, req)
}

// CreateCryptoTopup generates a crypto deposit address for prepaid balance top-up
func (c *GRPCClient) CreateCryptoTopup(ctx context.Context, req *pb.CreateCryptoTopupRequest) (*pb.CreateCryptoTopupResponse, error) {
	return c.prepaid.CreateCryptoTopup(ctx, req)
}

// GetCryptoTopup returns the status of a crypto top-up
func (c *GRPCClient) GetCryptoTopup(ctx context.Context, topupID string) (*pb.CryptoTopup, error) {
	return c.prepaid.GetCryptoTopup(ctx, &pb.GetCryptoTopupRequest{TopupId: topupID})
}

// PromoteToPaid upgrades a prepaid account to postpaid billing
func (c *GRPCClient) PromoteToPaid(ctx context.Context, tenantID, tierID string) (*pb.PromoteToPaidResponse, error) {
	return c.prepaid.PromoteToPaid(ctx, &pb.PromoteToPaidRequest{
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
func (c *GRPCClient) ProcessWebhook(ctx context.Context, req *pb.WebhookRequest) (*pb.WebhookResponse, error) {
	return c.webhook.ProcessWebhook(ctx, req)
}

// ============================================================================
// STRIPE OPERATIONS
// ============================================================================

// CreateStripeCheckoutSession creates a Stripe Checkout Session for subscription setup
func (c *GRPCClient) CreateStripeCheckoutSession(ctx context.Context, tenantID, tierID, billingPeriod, successURL, cancelURL string) (*pb.CreateStripeCheckoutResponse, error) {
	return c.stripe.CreateCheckoutSession(ctx, &pb.CreateStripeCheckoutRequest{
		TenantId:      tenantID,
		TierId:        tierID,
		BillingPeriod: billingPeriod,
		SuccessUrl:    successURL,
		CancelUrl:     cancelURL,
	})
}

// CreateStripeBillingPortal creates a Stripe Billing Portal session for subscription management
func (c *GRPCClient) CreateStripeBillingPortal(ctx context.Context, tenantID, returnURL string) (*pb.CreateBillingPortalResponse, error) {
	return c.stripe.CreateBillingPortalSession(ctx, &pb.CreateBillingPortalRequest{
		TenantId:  tenantID,
		ReturnUrl: returnURL,
	})
}

// SyncStripeSubscription syncs subscription state from Stripe (admin/debug)
func (c *GRPCClient) SyncStripeSubscription(ctx context.Context, tenantID string) (*pb.TenantSubscription, error) {
	return c.stripe.SyncSubscription(ctx, &pb.SyncStripeSubscriptionRequest{
		TenantId: tenantID,
	})
}

// ============================================================================
// MOLLIE OPERATIONS
// ============================================================================

// CreateMollieFirstPayment creates a Mollie first payment to establish a mandate.
// After successful payment, a SEPA DD mandate is created for recurring billing.
func (c *GRPCClient) CreateMollieFirstPayment(ctx context.Context, tenantID, tierID, method, redirectURL string) (*pb.CreateMollieFirstPaymentResponse, error) {
	return c.mollie.CreateFirstPayment(ctx, &pb.CreateMollieFirstPaymentRequest{
		TenantId:    tenantID,
		TierId:      tierID,
		Method:      method,
		RedirectUrl: redirectURL,
	})
}

// CreateMollieSubscription creates a Mollie subscription after mandate is valid
func (c *GRPCClient) CreateMollieSubscription(ctx context.Context, tenantID, tierID, mandateID, description string) (*pb.CreateMollieSubscriptionResponse, error) {
	return c.mollie.CreateMollieSubscription(ctx, &pb.CreateMollieSubscriptionRequest{
		TenantId:    tenantID,
		TierId:      tierID,
		MandateId:   mandateID,
		Description: description,
	})
}

// ListMollieMandates lists available payment mandates for a tenant
func (c *GRPCClient) ListMollieMandates(ctx context.Context, tenantID string) (*pb.ListMollieMandatesResponse, error) {
	return c.mollie.ListMandates(ctx, &pb.ListMollieMandatesRequest{
		TenantId: tenantID,
	})
}

// CancelMollieSubscription cancels a Mollie subscription
func (c *GRPCClient) CancelMollieSubscription(ctx context.Context, tenantID, subscriptionID string) error {
	_, err := c.mollie.CancelMollieSubscription(ctx, &pb.CancelMollieSubscriptionRequest{
		TenantId:       tenantID,
		SubscriptionId: subscriptionID,
	})
	return err
}

// ============================================================================
// BILLING DETAILS OPERATIONS
// ============================================================================

// GetBillingDetails returns billing details for a tenant
func (c *GRPCClient) GetBillingDetails(ctx context.Context, tenantID string) (*pb.BillingDetails, error) {
	return c.subscription.GetBillingDetails(ctx, &pb.GetBillingDetailsRequest{
		TenantId: tenantID,
	})
}

// UpdateBillingDetails updates billing details for a tenant
func (c *GRPCClient) UpdateBillingDetails(ctx context.Context, req *pb.UpdateBillingDetailsRequest) (*pb.BillingDetails, error) {
	return c.subscription.UpdateBillingDetails(ctx, req)
}

// ============================================================================
// X402 PAYMENT OPERATIONS
// ============================================================================

// GetPaymentRequirements returns x402 payment requirements for a 402 response.
// Called by Gateway when returning HTTP 402 to include payment options.
func (c *GRPCClient) GetPaymentRequirements(ctx context.Context, tenantID, resource string) (*pb.PaymentRequirements, error) {
	return c.x402.GetPaymentRequirements(ctx, &pb.GetPaymentRequirementsRequest{
		TenantId: tenantID,
		Resource: resource,
	})
}

// VerifyX402Payment verifies an x402 payment payload without settling.
// Returns validity status, payer address, amount, and whether billing details are required.
func (c *GRPCClient) VerifyX402Payment(ctx context.Context, tenantID string, payment *pb.X402PaymentPayload, clientIP string) (*pb.VerifyX402PaymentResponse, error) {
	return c.x402.VerifyX402Payment(ctx, &pb.VerifyX402PaymentRequest{
		TenantId: tenantID,
		Payment:  payment,
		ClientIp: clientIP,
	})
}

// SettleX402Payment settles an x402 payment on-chain and credits the tenant's balance.
// Returns transaction hash, credited amount, and new balance.
func (c *GRPCClient) SettleX402Payment(ctx context.Context, tenantID string, payment *pb.X402PaymentPayload, clientIP string) (*pb.SettleX402PaymentResponse, error) {
	return c.x402.SettleX402Payment(ctx, &pb.SettleX402PaymentRequest{
		TenantId: tenantID,
		Payment:  payment,
		ClientIp: clientIP,
	})
}

// GetTenantX402Address returns the per-tenant x402 deposit address.
// Creates a new address on first call for a tenant.
func (c *GRPCClient) GetTenantX402Address(ctx context.Context, tenantID string) (*pb.GetTenantX402AddressResponse, error) {
	return c.x402.GetTenantX402Address(ctx, &pb.GetTenantX402AddressRequest{
		TenantId: tenantID,
	})
}
