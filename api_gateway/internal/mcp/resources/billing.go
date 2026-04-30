package resources

import (
	"context"
	"fmt"
	"sort"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/mcperrors"
	"frameworks/api_gateway/internal/resolvers"
	"frameworks/pkg/billing"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterBillingResources registers billing-related MCP resources.
func RegisterBillingResources(server *mcp.Server, clients *clients.ServiceClients, resolver *resolvers.Resolver, logger logging.Logger) {
	// billing://balance - Current prepaid balance
	server.AddResource(&mcp.Resource{
		URI:         "billing://balance",
		Name:        "Prepaid Balance",
		Description: "Current prepaid balance and usage metrics.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return handleBillingBalance(ctx, clients, logger)
	})

	// billing://pricing - Current pricing rates
	server.AddResource(&mcp.Resource{
		URI:         "billing://pricing",
		Name:        "Pricing",
		Description: "Current pricing for resources (streaming, storage, processing).",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return handleBillingPricing(ctx, clients)
	})

	// billing://transactions - Balance transaction history
	server.AddResource(&mcp.Resource{
		URI:         "billing://transactions",
		Name:        "Balance Transactions",
		Description: "Recent balance transactions (top-ups and usage deductions).",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return handleBillingTransactions(ctx, clients, logger)
	})
}

// BalanceInfo represents the billing://balance response.
type BalanceInfo struct {
	BalanceCents          int64  `json:"balance_cents"`
	Currency              string `json:"currency"`
	BillingModel          string `json:"billing_model"`
	DetailsComplete       bool   `json:"billing_details_complete"`
	LowBalanceWarning     bool   `json:"low_balance_warning"`
	LowBalanceThreshold   int64  `json:"low_balance_threshold_cents"`
	DrainRateCentsPerHour int64  `json:"drain_rate_cents_per_hour,omitempty"`
	EstimatedHoursLeft    int    `json:"estimated_hours_left,omitempty"`

	// Live metrics from Periscope. Monetary rating stays in Purser.
	LiveMetrics *LiveMetrics `json:"live_metrics,omitempty"`
}

// LiveMetrics represents current operational usage for billing context.
type LiveMetrics struct {
	ActiveStreams int32   `json:"active_streams"`
	TotalViewers  int32   `json:"total_viewers"`
	StorageGB     float64 `json:"storage_gb"`
}

func handleBillingBalance(ctx context.Context, clients *clients.ServiceClients, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, mcperrors.AuthRequired()
	}

	// Get billing model from API - fail if API fails
	tenantBillingStatus, err := clients.Purser.GetTenantBillingStatus(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to get billing status: %w", err)
	}

	info := BalanceInfo{
		BillingModel:      tenantBillingStatus.BillingModel,
		BalanceCents:      tenantBillingStatus.BalanceCents,
		LowBalanceWarning: tenantBillingStatus.IsBalanceNegative,
	}

	// Check if billing details are complete
	billingDetails, err := clients.Purser.GetBillingDetails(ctx, tenantID)
	if err != nil {
		logger.WithError(err).Debug("Failed to get billing details")
	} else {
		info.DetailsComplete = billingDetails.IsComplete
	}

	// Get detailed prepaid balance
	if info.BillingModel == "prepaid" {
		balance, err := clients.Purser.GetPrepaidBalance(ctx, tenantID, billing.DefaultCurrency())
		if err != nil {
			logger.WithError(err).Debug("Failed to get prepaid balance")
		} else {
			info.Currency = balance.Currency
			info.BalanceCents = balance.BalanceCents
			info.LowBalanceThreshold = balance.LowBalanceThresholdCents
			info.LowBalanceWarning = balance.BalanceCents < balance.LowBalanceThresholdCents
			info.DrainRateCentsPerHour = balance.DrainRateCentsPerHour
			if balance.DrainRateCentsPerHour > 0 && balance.BalanceCents > 0 {
				info.EstimatedHoursLeft = int(balance.BalanceCents / balance.DrainRateCentsPerHour)
			}
		}
	}

	liveMetrics := getLiveUsageMetrics(ctx, clients, tenantID, logger)
	if liveMetrics != nil {
		info.LiveMetrics = liveMetrics
	}

	return marshalResourceResult("billing://balance", info)
}

// getLiveUsageMetrics fetches current operational usage from Periscope.
func getLiveUsageMetrics(ctx context.Context, clients *clients.ServiceClients, tenantID string, logger logging.Logger) *LiveMetrics {
	// Get live usage summary (last hour as proxy for current usage)
	usageResp, err := clients.Periscope.GetLiveUsageSummary(ctx, tenantID, nil)
	if err != nil {
		logger.WithError(err).Debug("Failed to get live usage summary")
		return nil
	}
	if usageResp == nil || usageResp.Summary == nil {
		return nil
	}

	summary := usageResp.Summary
	return &LiveMetrics{
		ActiveStreams: summary.TotalStreams,
		TotalViewers:  summary.TotalViewers,
		StorageGB:     summary.AverageStorageGb,
	}
}

// PricingInfo represents the billing://pricing response.
type PricingInfo struct {
	TierName  string                     `json:"tier_name"`
	TierLevel int                        `json:"tier_level"`
	Resources map[string]ResourcePricing `json:"resources"`
	Currency  string                     `json:"currency"`
}

// ResourcePricing represents pricing for a single resource type.
type ResourcePricing struct {
	UnitPrice string `json:"unit_price"`
	Unit      string `json:"unit"`
}

func handleBillingPricing(ctx context.Context, clients *clients.ServiceClients) (*mcp.ReadResourceResult, error) {
	tenantID := ctxkeys.GetTenantID(ctx)

	// Fetch billing tiers from API - fail if API fails
	tiersResp, err := clients.Purser.GetBillingTiers(ctx, false, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get billing tiers: %w", err)
	}

	if len(tiersResp.Tiers) == 0 {
		return nil, fmt.Errorf("no billing tiers available")
	}

	// Find the tenant's current tier if authenticated
	var currentTier *pb.BillingTier
	var subscription *pb.TenantSubscription
	if tenantID != "" {
		sub, err := clients.Purser.GetSubscription(ctx, tenantID)
		if err != nil {
			return nil, fmt.Errorf("failed to get subscription for tier lookup: %w", err)
		} else if sub.Subscription != nil {
			subscription = sub.Subscription
			for _, tier := range tiersResp.Tiers {
				if tier.Id == sub.Subscription.TierId {
					currentTier = tier
					break
				}
			}
		}
	}

	// Use the first (default) tier if no current tier found
	if currentTier == nil {
		for _, tier := range tiersResp.Tiers {
			if tier.TierName == "payg" || tier.TierLevel == 0 {
				currentTier = tier
				break
			}
		}
		if currentTier == nil {
			for _, tier := range tiersResp.Tiers {
				if tier.IsActive {
					currentTier = tier
					break
				}
			}
		}
	}

	if currentTier == nil {
		return nil, fmt.Errorf("no applicable billing tier found")
	}

	pricing := PricingInfo{
		TierName:  currentTier.DisplayName,
		TierLevel: int(currentTier.TierLevel),
		Currency:  currentTier.Currency,
		Resources: map[string]ResourcePricing{},
	}

	for _, rule := range effectivePricingRules(currentTier.GetPricingRules(), subscription) {
		unitPrice := rule.GetUnitPrice()
		if unitPrice == "" {
			return nil, fmt.Errorf("pricing rule %q has empty unit_price", rule.GetMeter())
		}
		switch rule.GetMeter() {
		case "average_storage_gb":
			pricing.Resources[rule.GetMeter()] = ResourcePricing{UnitPrice: unitPrice, Unit: "gb"}
		case "ai_gpu_hours":
			pricing.Resources[rule.GetMeter()] = ResourcePricing{UnitPrice: unitPrice, Unit: "gpu_hours"}
		default:
			pricing.Resources[rule.GetMeter()] = ResourcePricing{UnitPrice: unitPrice, Unit: rule.GetMeter()}
		}
	}

	return marshalResourceResult("billing://pricing", pricing)
}

func effectivePricingRules(tierRules []*pb.PricingRule, subscription *pb.TenantSubscription) []*pb.PricingRule {
	if subscription == nil || len(subscription.GetPricingOverrides()) == 0 {
		return tierRules
	}
	overrides := make(map[string]*pb.PricingRule, len(subscription.GetPricingOverrides()))
	for _, override := range subscription.GetPricingOverrides() {
		if override == nil || override.GetMeter() == "" {
			continue
		}
		overrides[override.GetMeter()] = override
	}

	out := make([]*pb.PricingRule, 0, len(tierRules)+len(overrides))
	seen := make(map[string]bool, len(tierRules))
	for _, tierRule := range tierRules {
		if tierRule == nil {
			continue
		}
		meter := tierRule.GetMeter()
		seen[meter] = true
		if override, ok := overrides[meter]; ok {
			out = append(out, mergePricingRule(tierRule, override))
			continue
		}
		out = append(out, tierRule)
	}

	extraMeters := make([]string, 0, len(overrides))
	for meter := range overrides {
		if !seen[meter] {
			extraMeters = append(extraMeters, meter)
		}
	}
	sort.Strings(extraMeters)
	for _, meter := range extraMeters {
		out = append(out, overrides[meter])
	}
	return out
}

func mergePricingRule(base, override *pb.PricingRule) *pb.PricingRule {
	if base == nil {
		return override
	}
	merged := &pb.PricingRule{
		Meter:            base.GetMeter(),
		Model:            base.GetModel(),
		Currency:         base.GetCurrency(),
		IncludedQuantity: base.GetIncludedQuantity(),
		UnitPrice:        base.GetUnitPrice(),
		ConfigJson:       base.GetConfigJson(),
	}
	if override.GetMeter() != "" {
		merged.Meter = override.GetMeter()
	}
	if override.GetModel() != "" {
		merged.Model = override.GetModel()
	}
	if override.GetCurrency() != "" {
		merged.Currency = override.GetCurrency()
	}
	if override.GetIncludedQuantity() != "" {
		merged.IncludedQuantity = override.GetIncludedQuantity()
	}
	if override.GetUnitPrice() != "" {
		merged.UnitPrice = override.GetUnitPrice()
	}
	if override.GetConfigJson() != "" && override.GetConfigJson() != "{}" {
		merged.ConfigJson = override.GetConfigJson()
	}
	return merged
}

// TransactionInfo represents a balance transaction.
type TransactionInfo struct {
	ID           string `json:"id"`
	Type         string `json:"type"` // topup, usage, refund, adjustment
	AmountCents  int64  `json:"amount_cents"`
	BalanceAfter int64  `json:"balance_after_cents"`
	Description  string `json:"description"`
	CreatedAt    string `json:"created_at"`
}

// TransactionsResponse represents the billing://transactions response.
type TransactionsResponse struct {
	Transactions []TransactionInfo `json:"transactions"`
	TotalCount   int               `json:"total_count"`
	HasMore      bool              `json:"has_more"`
}

func handleBillingTransactions(ctx context.Context, clients *clients.ServiceClients, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, mcperrors.AuthRequired()
	}

	// Build pagination request for last 20 transactions
	pagination := &pb.CursorPaginationRequest{
		First: 20,
	}

	// Get recent transactions from Purser (no type filter, no time range)
	txns, err := clients.Purser.ListBalanceTransactions(ctx, tenantID, nil, nil, pagination)
	if err != nil {
		logger.WithError(err).Warn("Failed to get balance transactions")
		return nil, fmt.Errorf("failed to get balance transactions: %w", err)
	}

	transactions := make([]TransactionInfo, 0, len(txns.Transactions))
	for _, txn := range txns.Transactions {
		transactions = append(transactions, TransactionInfo{
			ID:           txn.Id,
			Type:         txn.TransactionType,
			AmountCents:  txn.AmountCents,
			BalanceAfter: txn.BalanceAfterCents,
			Description:  txn.Description,
			CreatedAt:    txn.CreatedAt.AsTime().Format("2006-01-02T15:04:05Z"),
		})
	}

	hasMore := txns.Pagination != nil && txns.Pagination.HasNextPage
	return marshalResourceResult("billing://transactions", TransactionsResponse{
		Transactions: transactions,
		TotalCount:   len(transactions),
		HasMore:      hasMore,
	})
}
