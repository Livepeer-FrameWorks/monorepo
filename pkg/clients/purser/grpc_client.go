package purser

import (
	"context"
	"fmt"
	"time"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// GRPCClient is the gRPC client for Purser
type GRPCClient struct {
	conn         *grpc.ClientConn
	billing      pb.BillingServiceClient
	subscription pb.SubscriptionServiceClient
	invoice      pb.InvoiceServiceClient
	payment      pb.PaymentServiceClient
	usage        pb.UsageServiceClient
	logger       logging.Logger
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

		if userID, ok := ctx.Value("user_id").(string); ok && userID != "" {
			md.Set("x-user-id", userID)
		}
		if tenantID, ok := ctx.Value("tenant_id").(string); ok && tenantID != "" {
			md.Set("x-tenant-id", tenantID)
		}

		// Use user's JWT from context if available, otherwise fall back to service token
		if jwtToken, ok := ctx.Value("jwt_token").(string); ok && jwtToken != "" {
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
		grpc.WithUnaryInterceptor(authInterceptor(config.ServiceToken)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Purser gRPC: %w", err)
	}

	return &GRPCClient{
		conn:         conn,
		billing:      pb.NewBillingServiceClient(conn),
		subscription: pb.NewSubscriptionServiceClient(conn),
		invoice:      pb.NewInvoiceServiceClient(conn),
		payment:      pb.NewPaymentServiceClient(conn),
		usage:        pb.NewUsageServiceClient(conn),
		logger:       config.Logger,
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
func (c *GRPCClient) GetUsageRecords(ctx context.Context, tenantID, clusterID, usageType, billingMonth string, pagination *pb.CursorPaginationRequest) (*pb.UsageRecordsResponse, error) {
	return c.usage.GetUsageRecords(ctx, &pb.GetUsageRecordsRequest{
		TenantId:     tenantID,
		ClusterId:    clusterID,
		UsageType:    usageType,
		BillingMonth: billingMonth,
		Pagination:   pagination,
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
