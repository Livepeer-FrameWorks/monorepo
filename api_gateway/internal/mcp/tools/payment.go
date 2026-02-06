package tools

import (
	"context"
	"fmt"
	"strings"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/api_gateway/internal/resolvers"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	x402 "frameworks/pkg/x402"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterPaymentTools registers x402 payment-related MCP tools.
func RegisterPaymentTools(server *mcp.Server, clients *clients.ServiceClients, resolver *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) {
	// get_payment_options - Get x402 payment options (works without auth)
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "get_payment_options",
			Description: "Get x402 payment options for authentication and balance top-up. Returns the platform payTo address, supported networks, and payment instructions. Can be called without authentication.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args GetPaymentOptionsInput) (*mcp.CallToolResult, any, error) {
			return handleGetPaymentOptions(ctx, args, clients, logger)
		},
	)

	// submit_payment - Submit an x402 payment
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "submit_payment",
			Description: "Submit an x402 payment. With value=0, this authenticates via wallet signature (proves ownership). With value>0, this settles payment for the specified resource and credits the billable tenant. The payment header should be base64-encoded JSON per the x402 spec.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args SubmitPaymentInput) (*mcp.CallToolResult, any, error) {
			return handleSubmitPayment(ctx, args, clients, logger)
		},
	)
}

// GetPaymentOptionsInput represents input for get_payment_options tool.
type GetPaymentOptionsInput struct {
	Resource string `json:"resource,omitempty" jsonschema_description:"Optional resource being accessed (for logging)"`
}

// PaymentOption represents a single x402 payment option.
type PaymentOption struct {
	Network     string `json:"network"`
	DisplayName string `json:"display_name"`
	Asset       string `json:"asset"`        // USDC contract address
	AssetSymbol string `json:"asset_symbol"` // "USDC"
	PayTo       string `json:"pay_to"`       // Platform payTo address
	Description string `json:"description"`
}

// GetPaymentOptionsResult represents the result of getting payment options.
type GetPaymentOptionsResult struct {
	X402Version  int             `json:"x402_version"`
	Options      []PaymentOption `json:"options"`
	TopupURL     string          `json:"topup_url,omitempty"` // Human flow for manual top-up
	Message      string          `json:"message"`
	Instructions string          `json:"instructions"`
}

func handleGetPaymentOptions(ctx context.Context, args GetPaymentOptionsInput, clients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	// This works without authentication - anyone can get payment options
	resource := args.Resource
	if resource == "" {
		resource = "graphql://operation"
	}

	// Get payment requirements from Purser (no tenantID needed)
	resp, err := clients.Purser.GetPaymentRequirements(ctx, "", resource)
	if err != nil {
		logger.WithError(err).Warn("Failed to get payment requirements")
		return toolError(fmt.Sprintf("Failed to get payment options: %v", err))
	}

	if resp.Error != "" {
		return toolError(resp.Error)
	}

	options := make([]PaymentOption, 0, len(resp.Accepts))
	for _, accept := range resp.Accepts {
		options = append(options, PaymentOption{
			Network:     accept.Network,
			DisplayName: networkDisplayName(accept.Network),
			Asset:       accept.Asset,
			AssetSymbol: "USDC", // All supported networks use USDC
			PayTo:       accept.PayTo,
			Description: accept.Description,
		})
	}

	result := GetPaymentOptionsResult{
		X402Version: int(resp.X402Version),
		Options:     options,
		TopupURL:    resp.TopupUrl,
		Message:     "Use these options to authenticate via x402 or top up your balance.",
		Instructions: `To authenticate (zero-value payment):
1. Create an EIP-3009 authorization with value=0
2. Sign it with your wallet
3. Base64-encode the JSON payload
4. Call submit_payment with the encoded payload

To top up balance:
1. Create an EIP-3009 authorization with your desired USDC amount (6 decimals)
2. Sign it with your wallet
3. Base64-encode the JSON payload
4. Call submit_payment with the encoded payload`,
	}

	return toolSuccessJSON(result)
}

// SubmitPaymentInput represents input for submit_payment tool.
type SubmitPaymentInput struct {
	Payment  string `json:"payment" jsonschema:"required" jsonschema_description:"Base64-encoded x402 payment payload (JSON with x402Version scheme network and payload with signature and authorization)"`
	Resource string `json:"resource,omitempty" jsonschema_description:"Resource being paid for (required for non-zero payments). Supports stream_id or artifact_hash; relay IDs accepted. Use prefixes: playback: or ingest: for view/ingest keys."`
}

// SubmitPaymentResult represents the result of submitting a payment.
type SubmitPaymentResult struct {
	Success       bool   `json:"success"`
	IsAuthOnly    bool   `json:"is_auth_only"`   // True if value=0 (authentication only)
	TenantID      string `json:"tenant_id"`      // Credited tenant (for payment) or wallet tenant (for auth)
	WalletAddress string `json:"wallet_address"` // Payer wallet
	CreditedCents int64  `json:"credited_cents"` // Amount credited (0 for auth-only)
	NewBalance    int64  `json:"new_balance_cents,omitempty"`
	TxHash        string `json:"tx_hash,omitempty"` // Blockchain tx (for non-zero payments)
	TargetTenant  string `json:"target_tenant_id,omitempty"`
	SessionToken  string `json:"session_token,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
	Message       string `json:"message"`
}

func handleSubmitPayment(ctx context.Context, args SubmitPaymentInput, clients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if args.Payment == "" {
		return toolError("payment is required (base64-encoded x402 payload)")
	}

	// Parse the payment payload
	payload, err := middleware.ParseX402PaymentHeader(args.Payment)
	if err != nil {
		return toolError(fmt.Sprintf("Invalid payment payload: %v", err))
	}

	// Extract payer address
	payerAddress := ""
	if payload.Payload != nil && payload.Payload.Authorization != nil {
		payerAddress = payload.Payload.Authorization.From
	}
	if payerAddress == "" {
		return toolError("Payment payload missing payer address (authorization.from)")
	}

	// Get client IP for VAT evidence
	clientIP := ctxkeys.GetClientIP(ctx)

	if x402.IsAuthOnlyPayment(payload) {
		resp, err := clients.Commodore.WalletLoginWithX402(ctx, payload, clientIP, "", nil)
		if err != nil {
			logger.WithError(err).Warn("x402 login failed")
			return toolError(fmt.Sprintf("x402 login failed: %v", err))
		}
		if resp.Auth == nil || resp.Auth.User == nil {
			return toolError("x402 login failed: missing auth response")
		}

		walletAddress := resp.PayerAddress
		if walletAddress == "" {
			walletAddress = payerAddress
		}

		expiresAt := ""
		if resp.Auth.ExpiresAt != nil {
			expiresAt = resp.Auth.ExpiresAt.AsTime().Format("2006-01-02T15:04:05Z")
		}

		result := SubmitPaymentResult{
			Success:       true,
			IsAuthOnly:    true,
			TenantID:      resp.Auth.User.TenantId,
			WalletAddress: walletAddress,
			CreditedCents: 0,
			SessionToken:  resp.Auth.Token,
			ExpiresAt:     expiresAt,
			Message:       "Authentication successful. Your wallet is now linked to your account.",
		}
		if resp.Auth.IsNewUser {
			result.Message = "Account created and authenticated. Your wallet is now linked."
		}
		return toolSuccessJSON(result)
	}

	authTenantID := ctxkeys.GetTenantID(ctx)
	resource := strings.TrimSpace(args.Resource)
	settleResult, settleErr := x402.SettleX402Payment(ctx, x402.SettlementOptions{
		Payload:                payload,
		PaymentHeader:          args.Payment,
		Resource:               resource,
		AuthTenantID:           authTenantID,
		ClientIP:               clientIP,
		Purser:                 clients.Purser,
		Commodore:              clients.Commodore,
		AllowUnresolvedCreator: false,
		Logger:                 logger,
	})
	if settleErr != nil {
		return toolError(settleErr.Message)
	}
	if settleResult == nil || settleResult.Settle == nil || !settleResult.Settle.Success {
		return toolError("payment settlement failed")
	}

	walletAddress := settleResult.PayerAddress
	if walletAddress == "" {
		walletAddress = payerAddress
	}

	result := SubmitPaymentResult{
		Success:       true,
		IsAuthOnly:    false,
		TenantID:      settleResult.TargetTenantID,
		WalletAddress: walletAddress,
		CreditedCents: settleResult.Settle.CreditedCents,
		NewBalance:    settleResult.Settle.NewBalanceCents,
		TxHash:        settleResult.Settle.TxHash,
		TargetTenant:  settleResult.TargetTenantID,
		Message:       fmt.Sprintf("Payment successful! %d cents credited to tenant %s.", settleResult.Settle.CreditedCents, settleResult.TargetTenantID),
	}

	return toolSuccessJSON(result)
}

// networkDisplayName returns a human-readable name for an x402 network.
func networkDisplayName(network string) string {
	switch strings.ToLower(network) {
	case "base", "base-mainnet":
		return "Base (Coinbase L2)"
	case "base-sepolia":
		return "Base Sepolia (Testnet)"
	case "arbitrum", "arbitrum-one":
		return "Arbitrum One"
	case "ethereum", "mainnet":
		return "Ethereum Mainnet"
	default:
		return network
	}
}
