// Package resources implements MCP resources for the FrameWorks platform.
package resources

import (
	"context"
	"encoding/json"
	"fmt"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/resolvers"
	"frameworks/pkg/billing"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterAccountResources registers account-related MCP resources.
func RegisterAccountResources(server *mcp.Server, clients *clients.ServiceClients, resolver *resolvers.Resolver, logger logging.Logger) {
	checker := preflight.NewChecker(clients, logger)

	// account://status - Agent self-awareness (critical)
	server.AddResource(&mcp.Resource{
		URI:         "account://status",
		Name:        "Account Status",
		Description: "Current account status, blockers, and capabilities. Always read this first.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return handleAccountStatus(ctx, clients, checker, logger)
	})
}

// AccountStatus represents the response for the account://status resource.
type AccountStatus struct {
	AccountReady bool                 `json:"account_ready"`
	Blockers     []preflight.Blocker  `json:"blockers"`
	Capabilities map[string]bool      `json:"capabilities"`
	Billing      AccountBillingInfo   `json:"billing"`
	RateLimits   AccountRateLimitInfo `json:"rate_limits"`
}

// AccountBillingInfo contains billing-related account info.
type AccountBillingInfo struct {
	Model             string `json:"model"`
	BalanceCents      int64  `json:"balance_cents"`
	DetailsComplete   bool   `json:"details_complete"`
	LowBalanceWarning bool   `json:"low_balance_warning"`
	DrainRatePerHour  int64  `json:"drain_rate_cents_per_hour,omitempty"`
}

// AccountRateLimitInfo contains rate limit info.
type AccountRateLimitInfo struct {
	RequestsPerMinute int `json:"requests_per_minute"`
}

func handleAccountStatus(ctx context.Context, clients *clients.ServiceClients, checker *preflight.Checker, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		// Not authenticated - return unauthenticated status
		status := AccountStatus{
			AccountReady: false,
			Blockers: []preflight.Blocker{
				{
					Code:       "AUTHENTICATION_REQUIRED",
					Message:    "Not authenticated. Connect with wallet signature or API token.",
					Resolution: "Authenticate using X-Wallet-* headers or Bearer token",
				},
			},
			Capabilities: map[string]bool{
				"read_streams":           false,
				"read_analytics":         false,
				"create_stream":          false,
				"topup_balance":          false,
				"update_billing_details": false,
			},
		}
		return marshalResourceResult("account://status", status)
	}

	userID := ctxkeys.GetUserID(ctx)

	// Get blockers
	blockers, err := checker.GetBlockers(ctx)
	if err != nil {
		logger.WithError(err).Warn("Failed to get blockers")
		blockers = []preflight.Blocker{}
	}

	// Get capabilities
	capabilities := checker.GetCapabilities(ctx)

	// Get billing info from API - fail if API fails
	tenantBillingStatus, err := clients.Purser.GetTenantBillingStatus(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to get billing status: %w", err)
	}

	billingInfo := AccountBillingInfo{
		Model:             tenantBillingStatus.BillingModel,
		BalanceCents:      tenantBillingStatus.BalanceCents,
		LowBalanceWarning: tenantBillingStatus.IsBalanceNegative,
	}

	// Check if billing details are complete
	billingDetails, err := clients.Purser.GetBillingDetails(ctx, tenantID)
	if err != nil {
		logger.WithError(err).Debug("Failed to get billing details")
	} else {
		billingInfo.DetailsComplete = billingDetails.IsComplete
	}

	// Fetch prepaid balance details if applicable
	if billingInfo.Model == "prepaid" {
		balance, err := clients.Purser.GetPrepaidBalance(ctx, tenantID, billing.DefaultCurrency())
		if err != nil {
			logger.WithError(err).Debug("Failed to get prepaid balance")
		} else {
			billingInfo.BalanceCents = balance.BalanceCents
			billingInfo.LowBalanceWarning = balance.BalanceCents < balance.LowBalanceThresholdCents
			billingInfo.DrainRatePerHour = balance.DrainRateCentsPerHour
		}
	}

	// Get rate limit info from API
	var rateLimits AccountRateLimitInfo
	if userID != "" {
		tenant, err := clients.Quartermaster.ValidateTenant(ctx, tenantID, userID)
		if err != nil {
			logger.WithError(err).Debug("Failed to get tenant info for rate limits")
		} else {
			rateLimits.RequestsPerMinute = int(tenant.RateLimitPerMinute)
		}
	}

	status := AccountStatus{
		AccountReady: len(blockers) == 0,
		Blockers:     blockers,
		Capabilities: capabilities,
		Billing:      billingInfo,
		RateLimits:   rateLimits,
	}

	return marshalResourceResult("account://status", status)
}

// marshalResourceResult marshals any value to an MCP resource result.
func marshalResourceResult(uri string, v interface{}) (*mcp.ReadResourceResult, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}

	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      uri,
			MIMEType: "application/json",
			Text:     string(data),
		}},
	}, nil
}
