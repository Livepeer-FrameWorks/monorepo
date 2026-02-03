package resources

import (
	"context"
	"fmt"

	"frameworks/api_gateway/internal/clients"
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
		return handleBillingPricing(ctx, clients, logger)
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

	// Live metrics (from Periscope)
	LiveMetrics *LiveMetrics `json:"live_metrics,omitempty"`

	// Pricing rates (for burn rate calculation)
	Rates *PricingRates `json:"rates,omitempty"`
}

// LiveMetrics represents current usage for burn rate calculation.
type LiveMetrics struct {
	ActiveStreams       int32   `json:"active_streams"`
	TotalViewers        int32   `json:"total_viewers"`
	StorageGB           float64 `json:"storage_gb"`
	BurnRateCentsPerMin float64 `json:"burn_rate_cents_per_min"`
	TimeToZeroMinutes   float64 `json:"time_to_zero_minutes,omitempty"`
}

// PricingRates represents the rates used for burn calculation.
type PricingRates struct {
	DeliveryCentsPerViewerMin float64 `json:"delivery_cents_per_viewer_min"`
	StorageCentsPerGBHour     float64 `json:"storage_cents_per_gb_hour"`
}

func handleBillingBalance(ctx context.Context, clients *clients.ServiceClients, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("not authenticated")
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

	// Get rates from tenant's billing tier
	rates := getTenantPricingRates(ctx, clients, tenantID, logger)
	if rates != nil {
		info.Rates = rates

		// Get live usage metrics from Periscope
		liveMetrics := getLiveUsageMetrics(ctx, clients, tenantID, rates, logger)
		if liveMetrics != nil {
			info.LiveMetrics = liveMetrics
		}
	}

	return marshalResourceResult("billing://balance", info)
}

// getTenantPricingRates fetches the tenant's billing tier and extracts rates.
func getTenantPricingRates(ctx context.Context, clients *clients.ServiceClients, tenantID string, logger logging.Logger) *PricingRates {
	tiersResp, err := clients.Purser.GetBillingTiers(ctx, false, nil)
	if err != nil {
		logger.WithError(err).Debug("Failed to get billing tiers for rates")
		return nil
	}
	if len(tiersResp.Tiers) == 0 {
		return nil
	}

	// Find tenant's current tier
	var currentTier *pb.BillingTier
	sub, err := clients.Purser.GetSubscription(ctx, tenantID)
	if err != nil {
		logger.WithError(err).Debug("Failed to get subscription for tier lookup")
	} else if sub.Subscription != nil {
		for _, tier := range tiersResp.Tiers {
			if tier.Id == sub.Subscription.TierId {
				currentTier = tier
				break
			}
		}
	}

	// Fall back to prepaid/default tier
	if currentTier == nil {
		for _, tier := range tiersResp.Tiers {
			if tier.TierName == "prepaid" || tier.TierLevel == 0 {
				currentTier = tier
				break
			}
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
	if currentTier == nil {
		return nil
	}

	rates := &PricingRates{}

	// Extract rates from OverageRates (primary source)
	if currentTier.OverageRates != nil {
		if currentTier.OverageRates.Bandwidth != nil {
			rates.DeliveryCentsPerViewerMin = currentTier.OverageRates.Bandwidth.UnitPrice
		}
		if currentTier.OverageRates.Storage != nil {
			rates.StorageCentsPerGBHour = currentTier.OverageRates.Storage.UnitPrice
		}
	}

	// Fall back to Allocations if no OverageRates
	if rates.DeliveryCentsPerViewerMin == 0 && currentTier.BandwidthAllocation != nil {
		rates.DeliveryCentsPerViewerMin = currentTier.BandwidthAllocation.UnitPrice
	}
	if rates.StorageCentsPerGBHour == 0 && currentTier.StorageAllocation != nil {
		rates.StorageCentsPerGBHour = currentTier.StorageAllocation.UnitPrice
	}

	// Only return rates if we have at least one configured
	if rates.DeliveryCentsPerViewerMin == 0 && rates.StorageCentsPerGBHour == 0 {
		return nil
	}

	return rates
}

// getLiveUsageMetrics fetches current usage from Periscope and calculates burn rate.
func getLiveUsageMetrics(ctx context.Context, clients *clients.ServiceClients, tenantID string, rates *PricingRates, logger logging.Logger) *LiveMetrics {
	if rates == nil {
		return nil
	}

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
	metrics := &LiveMetrics{
		TotalViewers: summary.TotalViewers,
		StorageGB:    summary.AverageStorageGb,
	}

	// Calculate burn rate based on actual usage and actual rates
	// Delivery: viewer-minutes × rate (convert viewer hours to minutes)
	deliveryBurnPerMin := (summary.ViewerHours / 60.0) * rates.DeliveryCentsPerViewerMin
	// Storage: GB × rate per hour / 60 (to get per-minute rate)
	storageBurnPerMin := summary.AverageStorageGb * (rates.StorageCentsPerGBHour / 60.0)

	metrics.BurnRateCentsPerMin = deliveryBurnPerMin + storageBurnPerMin

	return metrics
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
	UnitPrice float64 `json:"unit_price"`
	Unit      string  `json:"unit"`
}

func handleBillingPricing(ctx context.Context, clients *clients.ServiceClients, logger logging.Logger) (*mcp.ReadResourceResult, error) {
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
	if tenantID != "" {
		sub, err := clients.Purser.GetSubscription(ctx, tenantID)
		if err != nil {
			logger.WithError(err).Debug("Failed to get subscription for tier lookup")
		} else if sub.Subscription != nil {
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
			if tier.TierName == "prepaid" || tier.TierLevel == 0 {
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

	// Extract pricing from overage rates
	if currentTier.OverageRates != nil {
		if currentTier.OverageRates.Bandwidth != nil {
			pricing.Resources["bandwidth"] = ResourcePricing{
				UnitPrice: currentTier.OverageRates.Bandwidth.UnitPrice,
				Unit:      currentTier.OverageRates.Bandwidth.Unit,
			}
		}
		if currentTier.OverageRates.Storage != nil {
			pricing.Resources["storage"] = ResourcePricing{
				UnitPrice: currentTier.OverageRates.Storage.UnitPrice,
				Unit:      currentTier.OverageRates.Storage.Unit,
			}
		}
		if currentTier.OverageRates.Compute != nil {
			pricing.Resources["compute"] = ResourcePricing{
				UnitPrice: currentTier.OverageRates.Compute.UnitPrice,
				Unit:      currentTier.OverageRates.Compute.Unit,
			}
		}
	}

	// Extract from allocations if no overage rates
	if len(pricing.Resources) == 0 {
		if currentTier.BandwidthAllocation != nil && currentTier.BandwidthAllocation.UnitPrice > 0 {
			pricing.Resources["bandwidth"] = ResourcePricing{
				UnitPrice: currentTier.BandwidthAllocation.UnitPrice,
				Unit:      currentTier.BandwidthAllocation.Unit,
			}
		}
		if currentTier.StorageAllocation != nil && currentTier.StorageAllocation.UnitPrice > 0 {
			pricing.Resources["storage"] = ResourcePricing{
				UnitPrice: currentTier.StorageAllocation.UnitPrice,
				Unit:      currentTier.StorageAllocation.Unit,
			}
		}
		if currentTier.ComputeAllocation != nil && currentTier.ComputeAllocation.UnitPrice > 0 {
			pricing.Resources["compute"] = ResourcePricing{
				UnitPrice: currentTier.ComputeAllocation.UnitPrice,
				Unit:      currentTier.ComputeAllocation.Unit,
			}
		}
	}

	return marshalResourceResult("billing://pricing", pricing)
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
		return nil, fmt.Errorf("not authenticated")
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
