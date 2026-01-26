// Package preflight provides pre-flight checks for MCP tool execution.
// It validates billing details, balance, and rate limits before allowing billable operations.
package preflight

import (
	"context"
	"fmt"

	"frameworks/api_gateway/internal/clients"
	"frameworks/pkg/billing"
	"frameworks/pkg/logging"
)

// Blocker represents something preventing an operation.
type Blocker struct {
	Code        string        `json:"code"`
	Message     string        `json:"message"`
	Resolution  string        `json:"resolution"`
	Tool        string        `json:"tool,omitempty"`
	Required    []string      `json:"required_fields,omitempty"`
	X402Accepts []X402Accept  `json:"x402_accepts,omitempty"` // x402 payment options (for INSUFFICIENT_BALANCE)
}

// X402Accept represents an x402 payment option.
type X402Accept struct {
	Network     string `json:"network"`
	Asset       string `json:"asset"`
	PayTo       string `json:"pay_to"`
	Description string `json:"description"`
}

// Checker performs pre-flight checks for MCP operations.
type Checker struct {
	clients *clients.ServiceClients
	logger  logging.Logger
}

// NewChecker creates a new pre-flight checker.
func NewChecker(clients *clients.ServiceClients, logger logging.Logger) *Checker {
	return &Checker{
		clients: clients,
		logger:  logger,
	}
}

// GetBlockers returns all blockers preventing billable operations.
func (c *Checker) GetBlockers(ctx context.Context) ([]Blocker, error) {
	var blockers []Blocker

	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok || tenantID == "" {
		blockers = append(blockers, Blocker{
			Code:       "AUTHENTICATION_REQUIRED",
			Message:    "Not authenticated",
			Resolution: "Connect with wallet signature or API token",
		})
		return blockers, nil
	}

	// Check billing details
	billingBlocker, err := c.CheckBillingDetails(ctx)
	if err != nil {
		c.logger.WithError(err).Warn("Failed to check billing details")
	} else if billingBlocker != nil {
		blockers = append(blockers, *billingBlocker)
	}

	// Check balance (only if billing details are complete)
	if billingBlocker == nil {
		balanceBlocker, err := c.CheckBalance(ctx)
		if err != nil {
			c.logger.WithError(err).Warn("Failed to check balance")
		} else if balanceBlocker != nil {
			blockers = append(blockers, *balanceBlocker)
		}
	}

	return blockers, nil
}

// CheckBillingDetails checks if billing details are complete.
func (c *Checker) CheckBillingDetails(ctx context.Context) (*Blocker, error) {
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok || tenantID == "" {
		return nil, fmt.Errorf("no tenant ID in context")
	}

	// Get billing details from Purser
	details, err := c.clients.Purser.GetBillingDetails(ctx, tenantID)
	if err != nil {
		// No billing details yet - treat as incomplete
		c.logger.WithError(err).Debug("Failed to get billing details, treating as incomplete")
		return &Blocker{
			Code:       "BILLING_DETAILS_MISSING",
			Message:    "Billing details required before any payments",
			Resolution: "Call update_billing_details tool with address, city, postal code, and country",
			Tool:       "update_billing_details",
			Required:   []string{"address_line1", "city", "postal_code", "country"},
		}, nil
	}

	// Check if billing details are complete (IsComplete is set by Purser server)
	if !details.IsComplete {
		return &Blocker{
			Code:       "BILLING_DETAILS_MISSING",
			Message:    "Billing details incomplete - address information required",
			Resolution: "Call update_billing_details tool with address, city, postal code, and country",
			Tool:       "update_billing_details",
			Required:   []string{"address_line1", "city", "postal_code", "country"},
		}, nil
	}

	return nil, nil
}

// CheckBalance checks if the prepaid balance is sufficient.
func (c *Checker) CheckBalance(ctx context.Context) (*Blocker, error) {
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok || tenantID == "" {
		return nil, fmt.Errorf("no tenant ID in context")
	}

	// Get prepaid balance from Purser
	balance, err := c.clients.Purser.GetPrepaidBalance(ctx, tenantID, billing.DefaultCurrency())
	if err != nil {
		// If no balance record, it might be a postpaid account - check billing model
		status, statusErr := c.clients.Purser.GetTenantBillingStatus(ctx, tenantID)
		if statusErr == nil && status.BillingModel == "postpaid" {
			return nil, nil // Postpaid accounts don't need balance check
		}
		return nil, fmt.Errorf("failed to get balance: %w", err)
	}

	// Check if balance is sufficient (must be > 0 for new operations)
	if balance.BalanceCents <= 0 {
		blocker := &Blocker{
			Code:       "INSUFFICIENT_BALANCE",
			Message:    fmt.Sprintf("Balance is %d cents. Top up required to perform billable operations.", balance.BalanceCents),
			Resolution: "Call topup_balance tool to add credits, OR pay via x402 (USDC on Base/Arbitrum)",
			Tool:       "topup_balance",
		}

		// Fetch x402 payment options so agents with wallets can pay directly
			paymentReqs, err := c.clients.Purser.GetPaymentRequirements(ctx, tenantID, "graphql://operation")
		if err != nil {
			c.logger.WithError(err).Debug("Failed to get x402 payment requirements")
		} else if paymentReqs != nil {
			for _, req := range paymentReqs.Accepts {
				blocker.X402Accepts = append(blocker.X402Accepts, X402Accept{
					Network:     req.Network,
					Asset:       req.Asset,
					PayTo:       req.PayTo,
					Description: req.Description,
				})
			}
		}

		return blocker, nil
	}

	return nil, nil
}

// CheckRateLimit checks if the rate limit is exceeded.
func (c *Checker) CheckRateLimit(ctx context.Context) (*Blocker, error) {
	// Rate limiting is handled by the Gateway middleware
	// This is a placeholder for future per-operation rate limiting
	return nil, nil
}

// RequireBillingDetails checks billing details and returns an error if incomplete.
func (c *Checker) RequireBillingDetails(ctx context.Context) error {
	blocker, err := c.CheckBillingDetails(ctx)
	if err != nil {
		return err
	}
	if blocker != nil {
		return &PreflightError{Blocker: *blocker}
	}
	return nil
}

// RequireBalance checks balance and returns an error if insufficient.
func (c *Checker) RequireBalance(ctx context.Context) error {
	blocker, err := c.CheckBalance(ctx)
	if err != nil {
		return err
	}
	if blocker != nil {
		return &PreflightError{Blocker: *blocker}
	}
	return nil
}

// RequireBillingAndBalance checks both billing details and balance.
func (c *Checker) RequireBillingAndBalance(ctx context.Context) error {
	if err := c.RequireBillingDetails(ctx); err != nil {
		return err
	}
	return c.RequireBalance(ctx)
}

// PreflightError wraps a blocker as an error for tool handlers.
type PreflightError struct {
	Blocker Blocker
}

func (e *PreflightError) Error() string {
	return e.Blocker.Message
}

// IsPreflightError checks if an error is a preflight error.
func IsPreflightError(err error) (*PreflightError, bool) {
	if pfe, ok := err.(*PreflightError); ok {
		return pfe, true
	}
	return nil, false
}

// GetCapabilities returns what operations the tenant can perform right now.
func (c *Checker) GetCapabilities(ctx context.Context) map[string]bool {
	caps := map[string]bool{
		"read_streams":               true, // Free reads always work
		"read_analytics":             true,
		"read_billing":               true,
		"read_vod":                   true, // Free reads
		"update_billing_details":     true, // Always allowed
		"resolve_playback_endpoint":  true, // Free
		"validate_stream_key":        true, // Free
		"create_stream":              false,
		"update_stream":              false,
		"delete_stream":              false,
		"create_clip":                false,
		"start_dvr":                  false,
		"create_vod_upload":          false,
		"complete_vod_upload":        false,
		"delete_vod_asset":           false,
		"topup_balance":              false,
	}

	// Check billing details
	billingBlocker, err := c.CheckBillingDetails(ctx)
	if err != nil || billingBlocker != nil {
		return caps // Billing required for all billable ops
	}

	// Billing details complete - enable topup
	caps["topup_balance"] = true

	// Check balance
	balanceBlocker, err := c.CheckBalance(ctx)
	if err != nil || balanceBlocker != nil {
		return caps // Balance required for stream/clip/dvr
	}

	// Balance OK - enable all operations
	caps["create_stream"] = true
	caps["update_stream"] = true
	caps["delete_stream"] = true
	caps["create_clip"] = true
	caps["start_dvr"] = true
	caps["create_vod_upload"] = true
	caps["complete_vod_upload"] = true
	caps["delete_vod_asset"] = true

	return caps
}
