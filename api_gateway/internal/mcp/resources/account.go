// Package resources implements MCP resources for the FrameWorks platform.
package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

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
		return handleAccountStatus(ctx, clients, resolver, checker, logger)
	})
}

// AccountStatus represents the response for the account://status resource.
// ToolAccess says which MCP tools this account may call right now; Capabilities
// says which platform gates are open for the tenant.
type AccountStatus struct {
	AccountReady   bool                 `json:"account_ready"`
	RatedWorkReady bool                 `json:"rated_work_ready"`
	Blockers       []preflight.Blocker  `json:"blockers"`
	NextActions    []string             `json:"next_actions"`
	ToolAccess     map[string]bool      `json:"tool_access"`
	ServerInfo     AccountServerInfo    `json:"server_info"`
	Capabilities   *AccountCapabilities `json:"capabilities,omitempty"`
	Billing        AccountBillingInfo   `json:"billing"`
	RateLimits     AccountRateLimitInfo `json:"rate_limits"`
}

// AccountServerInfo is the platform release and the product features this
// server ships, so an agent can tell what the server supports before it tries.
type AccountServerInfo struct {
	Version  string   `json:"version"`
	Features []string `json:"features"`
}

// AccountCapabilities are the gates the platform enforces for the tenant.
// Unavailable names the sections whose source could not be read, which are
// omitted rather than guessed.
type AccountCapabilities struct {
	Tenant      *AccountTenantCapabilities `json:"tenant,omitempty"`
	Clusters    []AccountClusterCapability `json:"clusters,omitempty"`
	ObservedAt  time.Time                  `json:"observed_at"`
	Unavailable []string                   `json:"unavailable,omitempty"`
}

// AccountTenantCapabilities are the tenant-wide gates.
type AccountTenantCapabilities struct {
	PlatformOperator          bool `json:"platform_operator"`
	RecordingRetentionCapped  bool `json:"recording_retention_capped"`
	RecordingRetentionMaxDays *int `json:"recording_retention_max_days,omitempty"`
	ProcessingCustomizable    bool `json:"processing_customizable"`
	CustomSubdomain           bool `json:"custom_subdomain"`
	CustomDomain              bool `json:"custom_domain"`
}

// AccountClusterCapability is one entitled cluster and the media verbs it
// accepts for the tenant now.
type AccountClusterCapability struct {
	ClusterID   string `json:"cluster_id"`
	ClusterName string `json:"cluster_name"`
	Role        string `json:"role"`
	AccessLevel string `json:"access_level"`
	Ingest      bool   `json:"ingest"`
	Playback    bool   `json:"playback"`
	Storage     bool   `json:"storage"`
	Processing  bool   `json:"processing"`
}

// AccountBillingInfo contains billing-related account info.
type AccountBillingInfo struct {
	Model                 string `json:"model"`
	TierName              string `json:"tier_name,omitempty"`
	CollectionReady       bool   `json:"collection_ready"`
	BalanceCents          int64  `json:"balance_cents"`
	ReservedBalanceCents  int64  `json:"reserved_balance_cents"`
	AvailableBalanceCents int64  `json:"available_balance_cents"`
	DetailsComplete       bool   `json:"details_complete"`
	LowBalanceWarning     bool   `json:"low_balance_warning"`
	DrainRatePerHour      int64  `json:"drain_rate_cents_per_hour,omitempty"`
}

// AccountRateLimitInfo contains rate limit info.
type AccountRateLimitInfo struct {
	RequestsPerMinute int `json:"requests_per_minute"`
}

func handleAccountStatus(ctx context.Context, clients *clients.ServiceClients, resolver *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		// Not authenticated - return unauthenticated status. server_info is
		// public, so an agent can still see what this server supports.
		status := AccountStatus{
			AccountReady:   false,
			RatedWorkReady: false,
			NextActions:    []string{"authenticate_with_wallet_or_bearer_token"},
			ServerInfo:     accountServerInfo(resolver),
			Blockers: []preflight.Blocker{
				{
					Code:       "AUTHENTICATION_REQUIRED",
					Message:    "Not authenticated. Connect with wallet signature or API token.",
					Resolution: "Authenticate using X-Wallet-* headers or Bearer token",
				},
			},
			ToolAccess: map[string]bool{
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

	// Which MCP tools this account may call right now
	toolAccess := checker.ToolAccess(ctx)

	// Get billing info from API - fail if API fails
	tenantBillingStatus, err := clients.Purser.GetTenantBillingStatus(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to get billing status: %w", err)
	}

	billingInfo := AccountBillingInfo{
		Model:             tenantBillingStatus.BillingModel,
		TierName:          tenantBillingStatus.TierName,
		CollectionReady:   tenantBillingStatus.CollectionReady,
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
		balance, err := clients.Purser.GetPrepaidBalance(ctx, tenantID)
		if err != nil {
			logger.WithError(err).Debug("Failed to get prepaid balance")
		} else {
			billingInfo.BalanceCents = balance.BalanceCents
			billingInfo.ReservedBalanceCents = balance.ReservedBalanceCents
			billingInfo.AvailableBalanceCents = balance.AvailableBalanceCents
			billingInfo.LowBalanceWarning = balance.AvailableBalanceCents < balance.LowBalanceThresholdCents
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
		AccountReady:   true,
		RatedWorkReady: len(blockers) == 0,
		Blockers:       blockers,
		NextActions:    accountNextActions(blockers),
		ToolAccess:     toolAccess,
		ServerInfo:     accountServerInfo(resolver),
		Capabilities:   accountCapabilities(ctx, resolver, logger),
		Billing:        billingInfo,
		RateLimits:     rateLimits,
	}

	return marshalResourceResult("account://status", status)
}

func accountServerInfo(resolver *resolvers.Resolver) AccountServerInfo {
	if resolver == nil {
		return AccountServerInfo{}
	}
	info := resolver.ServerInfo()
	return AccountServerInfo{Version: info.Version, Features: info.Features}
}

// accountCapabilities reports the platform-enforced gates for the caller. A
// section whose source is unavailable is omitted and named in Unavailable, so
// an agent never reads an invented gate as fact.
func accountCapabilities(ctx context.Context, resolver *resolvers.Resolver, logger logging.Logger) *AccountCapabilities {
	if resolver == nil {
		return nil
	}
	reading, err := resolver.ReadCapabilities(ctx)
	if err != nil {
		logger.WithError(err).Warn("Failed to read capabilities for account status")
		return nil
	}

	out := &AccountCapabilities{ObservedAt: reading.ObservedAt}
	if reading.Tenant == nil {
		out.Unavailable = append(out.Unavailable, "tenant")
	} else {
		out.Tenant = &AccountTenantCapabilities{
			PlatformOperator:          reading.Tenant.PlatformOperator,
			RecordingRetentionCapped:  reading.Tenant.RecordingRetention.Capped,
			RecordingRetentionMaxDays: reading.Tenant.RecordingRetention.MaxDays,
			ProcessingCustomizable:    reading.Tenant.ProcessingCustomizable,
			CustomSubdomain:           reading.Tenant.CustomSubdomain,
			CustomDomain:              reading.Tenant.CustomDomain,
		}
	}
	if reading.ClustersErr != nil {
		out.Unavailable = append(out.Unavailable, "clusters")
	} else {
		for _, cluster := range reading.Clusters {
			out.Clusters = append(out.Clusters, AccountClusterCapability{
				ClusterID:   cluster.GetClusterId(),
				ClusterName: cluster.GetClusterName(),
				Role:        cluster.GetRole(),
				AccessLevel: cluster.GetAccessLevel(),
				Ingest:      cluster.GetMedia().GetIngest(),
				Playback:    cluster.GetMedia().GetPlayback(),
				Storage:     cluster.GetMedia().GetStorage(),
				Processing:  cluster.GetMedia().GetProcessing(),
			})
		}
	}
	return out
}

func accountNextActions(blockers []preflight.Blocker) []string {
	actions := make([]string, 0, 2)
	for _, blocker := range blockers {
		switch blocker.Code {
		case "INSUFFICIENT_BALANCE":
			actions = append(actions, "top_up_prepaid_credit", "link_and_verify_email_then_activate_free_tier")
		case "PAYMENT_SETUP_REQUIRED":
			actions = append(actions, "complete_confirmed_postpaid_provider_setup")
		case "BILLING_STATUS_UNAVAILABLE":
			actions = append(actions, "retry_rated_operation_after_billing_status_recovers")
		case "AUTHENTICATION_REQUIRED":
			actions = append(actions, "authenticate_with_wallet_or_bearer_token")
		}
	}
	return actions
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
