package grpc

import (
	"context"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/authz"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"

	grpcpkg "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var apiTokenBillingScopes = map[string]string{
	// Admission is an internal preflight for every scoped API operation. It
	// still requires a validated, tenant-bound API token, but does not require
	// the token to expose the tenant's billing ledger directly.
	"/purser.BillingService/GetTenantAdmissionStatus":              "",
	"/purser.BillingService/GetTenantBillingStatus":                "billing:read",
	"/purser.BillingService/ListMeterDefinitions":                  "billing:read",
	"/purser.BillingService/GetBillingTiers":                       "billing:read",
	"/purser.BillingService/GetBillingTier":                        "billing:read",
	"/purser.SubscriptionService/GetSubscription":                  "billing:read",
	"/purser.SubscriptionService/GetBillingDetails":                "billing:read",
	"/purser.InvoiceService/GetInvoice":                            "billing:read",
	"/purser.InvoiceService/ListInvoices":                          "billing:read",
	"/purser.InvoiceService/ListBillingDocuments":                  "billing:read",
	"/purser.InvoiceService/GetBillingDocument":                    "billing:read",
	"/purser.PaymentService/GetPayment":                            "billing:read",
	"/purser.PaymentService/ListPayments":                          "billing:read",
	"/purser.PaymentService/GetPaymentMethods":                     "billing:read",
	"/purser.PaymentService/GetBillingStatus":                      "billing:read",
	"/purser.UsageService/GetUsageRecords":                         "billing:read",
	"/purser.UsageService/GetTenantUsage":                          "billing:read",
	"/purser.UsageService/GetUsageAggregates":                      "billing:read",
	"/purser.ClusterPricingService/GetClusterPricing":              "billing:read",
	"/purser.ClusterPricingService/GetClustersPricingBatch":        "billing:read",
	"/purser.ClusterPricingService/ListClusterPricings":            "billing:read",
	"/purser.ClusterPricingService/CheckClusterAccess":             "billing:read",
	"/purser.ClusterPricingService/ListMarketplaceClusterPricings": "billing:read",
	"/purser.ClusterPricingService/SetClusterPricing":              "infrastructure:write",
	"/purser.PrepaidService/GetPrepaidBalance":                     "billing:read",
	"/purser.PrepaidService/ListBalanceTransactions":               "billing:read",
	"/purser.PrepaidService/GetPendingTopup":                       "billing:read",
	"/purser.PrepaidService/ListPendingTopups":                     "billing:read",
	"/purser.PrepaidService/GetCryptoTopup":                        "billing:read",
	"/purser.MollieService/ListMandates":                           "billing:read",
	"/purser.SubscriptionService/CreateSubscription":               "billing:write",
	"/purser.SubscriptionService/UpdateSubscription":               "billing:write",
	"/purser.SubscriptionService/CancelSubscription":               "billing:write",
	"/purser.SubscriptionService/UpdateBillingDetails":             "billing:write",
	"/purser.PaymentService/CreatePayment":                         "billing:write",
	"/purser.ClusterPricingService/CreateClusterSubscription":      "infrastructure:write",
	"/purser.ClusterPricingService/CancelClusterSubscription":      "infrastructure:write",
	"/purser.PrepaidService/CreateCardTopup":                       "billing:write",
	"/purser.PrepaidService/CreateCryptoTopup":                     "billing:write",
	"/purser.PrepaidService/PromoteToPaid":                         "billing:write",
	"/purser.PrepaidService/ChangeBillingTier":                     "billing:write",
	"/purser.StripeService/CreateCheckoutSession":                  "billing:write",
	"/purser.StripeService/CreateBillingPortalSession":             "billing:write",
	"/purser.MollieService/CreateFirstPayment":                     "billing:write",
	"/purser.MollieService/CreateMollieSubscription":               "billing:write",
	"/purser.MollieService/CancelMollieSubscription":               "billing:write",
}

type tenantBoundRequest interface {
	GetTenantId() string
}

var privilegedBillingMutationMethods = map[string]struct{}{
	"/purser.PrepaidService/TopupBalance":              {},
	"/purser.PrepaidService/DeductBalance":             {},
	"/purser.PrepaidService/AdjustBalance":             {},
	"/purser.BillingService/CreateBillingTier":         {},
	"/purser.BillingService/UpdateBillingTier":         {},
	"/purser.PrepaidService/InitializePrepaidBalance":  {},
	"/purser.PrepaidService/InitializePrepaidAccount":  {},
	"/purser.PrepaidService/InitializePostpaidAccount": {},
	"/purser.PrepaidService/EnsureFreeAccount":         {},
	"/purser.StripeService/SyncSubscription":           {},
}

var contextBoundBillingMutationMethods = map[string]struct{}{
	"/purser.PaymentService/CreatePayment":            {},
	"/purser.ClusterPricingService/SetClusterPricing": {},
}

var infrastructureMutationMethods = map[string]struct{}{
	"/purser.ClusterPricingService/CreateClusterSubscription": {},
	"/purser.ClusterPricingService/CancelClusterSubscription": {},
	"/purser.ClusterPricingService/SetClusterPricing":         {},
}

func billingMutationAuthorizationInterceptor() grpcpkg.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpcpkg.UnaryServerInfo, handler grpcpkg.UnaryHandler) (any, error) {
		requiredScope, scopedMutation := apiTokenBillingScopes[info.FullMethod]
		_, privilegedMutation := privilegedBillingMutationMethods[info.FullMethod]
		_, infrastructureMutation := infrastructureMutationMethods[info.FullMethod]
		if (!scopedMutation || requiredScope != "billing:write") && !privilegedMutation && !infrastructureMutation {
			return handler(ctx, req)
		}
		if middleware.IsServiceCall(ctx) || ctxkeys.IsPlatformOperator(ctx) {
			return handler(ctx, req)
		}
		if privilegedMutation {
			return nil, status.Error(codes.PermissionDenied, "billing ledger mutation requires service or platform-operator authentication")
		}
		resourceTenantID := ""
		tenantReq, ok := req.(tenantBoundRequest)
		if ok {
			resourceTenantID = strings.TrimSpace(tenantReq.GetTenantId())
		} else if _, contextBound := contextBoundBillingMutationMethods[info.FullMethod]; contextBound {
			// These requests deliberately derive their preliminary ownership
			// boundary from the authenticated context because they have no
			// caller-controlled tenant field. Their handlers still bind the invoice
			// or cluster to its authoritative tenant before writing.
			resourceTenantID = strings.TrimSpace(ctxkeys.GetTenantID(ctx))
		} else {
			return nil, status.Error(codes.PermissionDenied, "billing mutation requires a tenant-bound request")
		}
		if resourceTenantID == "" {
			return nil, status.Error(codes.PermissionDenied, "billing mutation requires tenant context")
		}
		identity := authz.Identity{
			UserID:           ctxkeys.GetUserID(ctx),
			TenantID:         ctxkeys.GetTenantID(ctx),
			Role:             ctxkeys.GetRole(ctx),
			Permissions:      ctxkeys.GetPermissions(ctx),
			PlatformOperator: ctxkeys.IsPlatformOperator(ctx),
		}
		action := authz.ActionManageBilling
		if infrastructureMutation {
			action = authz.ActionManageEdgeCluster
		}
		decision := authz.Default.Can(ctx, identity, action, authz.Resource{OwnerTenantID: resourceTenantID})
		if !decision.Allow {
			return nil, status.Error(codes.PermissionDenied, decision.Reason)
		}
		return handler(ctx, req)
	}
}

func apiTokenAuthorizationInterceptor() grpcpkg.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpcpkg.UnaryServerInfo, handler grpcpkg.UnaryHandler) (any, error) {
		if ctxkeys.GetAuthType(ctx) != "api_token" {
			return handler(ctx, req)
		}

		required, ok := apiTokenBillingScopes[info.FullMethod]
		if !ok {
			return nil, status.Error(codes.PermissionDenied, "API token is not authorized for this RPC")
		}
		if required != "" && !hasDelegatedPermission(ctxkeys.GetPermissions(ctx), required) {
			return nil, status.Errorf(codes.PermissionDenied, "API token requires %s scope", required)
		}
		callerTenant := strings.TrimSpace(ctxkeys.GetTenantID(ctx))
		if callerTenant == "" {
			return nil, status.Error(codes.PermissionDenied, "API token tenant is required")
		}
		if tenantRequest, ok := req.(tenantBoundRequest); ok {
			targetTenant := strings.TrimSpace(tenantRequest.GetTenantId())
			if targetTenant != "" && targetTenant != callerTenant {
				return nil, status.Error(codes.PermissionDenied, "API token cannot access another tenant")
			}
		}
		return handler(ctx, req)
	}
}

func hasDelegatedPermission(granted []string, required string) bool {
	for _, permission := range granted {
		if strings.TrimSpace(permission) == required {
			return true
		}
	}
	return false
}
