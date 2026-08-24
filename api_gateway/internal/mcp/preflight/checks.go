// Package preflight provides operation-aware checks for MCP tool execution.
package preflight

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"frameworks/api_gateway/internal/clients"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/accesspolicy"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

// Blocker represents something preventing an operation.
type Blocker struct {
	Code        string       `json:"code"`
	Message     string       `json:"message"`
	Resolution  string       `json:"resolution"`
	Tool        string       `json:"tool,omitempty"`
	Required    []string     `json:"required_fields,omitempty"`
	X402Accepts []X402Accept `json:"x402_accepts,omitempty"` // x402 payment options (for INSUFFICIENT_BALANCE)
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

// GetBlockers returns blockers for rated work. It intentionally does not make
// billing-profile completeness a session-wide prerequisite.
func (c *Checker) GetBlockers(ctx context.Context) ([]Blocker, error) {
	return c.GetOperationBlockers(ctx, accesspolicy.Rated)
}

// GetOperationBlockers returns the blockers relevant to one access class.
func (c *Checker) GetOperationBlockers(ctx context.Context, class accesspolicy.Class) ([]Blocker, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return []Blocker{{
			Code:       "AUTHENTICATION_REQUIRED",
			Message:    "Not authenticated",
			Resolution: "Connect with wallet signature or API token",
		}}, nil
	}
	if class.UnfundedAllowed() {
		return nil, nil
	}

	balanceBlocker, err := c.CheckBalance(ctx)
	if err != nil {
		c.logger.WithError(err).Warn("Failed to check rated-work billing status")
		return []Blocker{{
			Code:       "BILLING_STATUS_UNAVAILABLE",
			Message:    "Billing status is temporarily unavailable",
			Resolution: "Retry the rated operation safely",
		}}, nil
	}
	if balanceBlocker == nil {
		return nil, nil
	}
	return []Blocker{*balanceBlocker}, nil
}

// CheckBillingDetails checks if billing details are complete.
func (c *Checker) CheckBillingDetails(ctx context.Context) (*Blocker, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("no tenant ID in context")
	}

	// Get billing details from Purser
	details, err := c.clients.Purser.GetBillingDetails(ctx, tenantID)
	if err != nil {
		// No billing details yet - treat as incomplete
		c.logger.WithError(err).Debug("Failed to get billing details, treating as incomplete")
		return &Blocker{
			Code:       "BILLING_DETAILS_MISSING",
			Message:    "Billing details are required for paid postpaid setup and full customer invoices",
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
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("no tenant ID in context")
	}

	billingStatus, err := c.clients.Purser.GetTenantAdmissionStatus(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to get billing status: %w", err)
	}
	if billingStatus.GetBillingModel() == "postpaid" {
		if strings.EqualFold(billingStatus.GetTierName(), "free") || billingStatus.GetCollectionReady() {
			return nil, nil
		}
		return &Blocker{
			Code:       "PAYMENT_SETUP_REQUIRED",
			Message:    "Postpaid billing is not collectible because no confirmed Stripe or Mollie subscription is configured.",
			Resolution: "Complete payment-provider setup in Billing before running billable operations.",
		}, nil
	}
	if billingStatus.GetBillingModel() != "prepaid" {
		return nil, fmt.Errorf("unknown billing model %q", billingStatus.GetBillingModel())
	}

	balance, err := c.clients.Purser.GetPrepaidBalance(ctx, tenantID, billing.DefaultCurrency())
	if err != nil {
		// A prepaid balance row is created on first funding, so absence is zero.
		balance = &purserpb.PrepaidBalance{BalanceCents: 0, AvailableBalanceCents: 0}
	}

	// Check if balance is sufficient (must be > 0 for new operations)
	if balance.AvailableBalanceCents <= 0 {
		blocker := &Blocker{
			Code:       "INSUFFICIENT_BALANCE",
			Message:    fmt.Sprintf("Available balance is %d cents (%d settled, %d reserved). Top up required to perform billable operations.", balance.AvailableBalanceCents, balance.BalanceCents, balance.ReservedBalanceCents),
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
	var pfe *PreflightError
	if errors.As(err, &pfe) {
		return pfe, true
	}
	return nil, false
}

// GetCapabilities returns what operations the tenant can perform right now.
func (c *Checker) GetCapabilities(ctx context.Context) map[string]bool {
	caps := make(map[string]bool)
	for name, class := range accesspolicy.MCPToolClasses() {
		caps[name] = class.UnfundedAllowed()
	}

	// Billing/tax details are evaluated by the selected payment path, not as a
	// prerequisite for discovery or control-plane tools.
	balanceBlocker, err := c.CheckBalance(ctx)
	if err != nil || balanceBlocker != nil {
		return caps
	}

	for name := range caps {
		caps[name] = true
	}

	return caps
}
