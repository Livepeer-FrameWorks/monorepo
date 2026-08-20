//nolint:errcheck // Optional response extensions are decoded/encoded on a best-effort interoperability path.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	x402 "github.com/Livepeer-FrameWorks/monorepo/pkg/x402"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterPaymentTools registers x402 payment-related MCP tools.
func RegisterPaymentTools(server *mcp.Server, clients *clients.ServiceClients, resolver *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) {
	// get_payment_options - Get tenant-bound x402 payment options.
	addTool(server,
		&mcp.Tool{
			Name:        "get_payment_options",
			Description: "Get exact x402 prepaid top-up options for the authenticated tenant. Returns tenant-bound payTo addresses, supported networks, exact amounts, and payment instructions.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args GetPaymentOptionsInput) (*mcp.CallToolResult, any, error) {
			return handleGetPaymentOptions(ctx, args, clients, logger)
		},
	)

	// submit_payment - Submit an x402 payment
	addTool(server,
		&mcp.Tool{
			Name:        "submit_payment",
			Description: "Submit an official x402 v2 PAYMENT-SIGNATURE value for an authenticated tenant top-up. Zero-value payloads are not authentication; use wallet challenge login.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args SubmitPaymentInput) (*mcp.CallToolResult, any, error) {
			return handleSubmitPayment(ctx, args, clients, logger)
		},
	)
}

// GetPaymentOptionsInput represents input for get_payment_options tool.
type GetPaymentOptionsInput struct {
	Resource string `json:"resource,omitempty" jsonschema:"Optional resource being accessed (for logging)"`
}

// PaymentOption represents a single x402 payment option.
type PaymentOption struct {
	Network     string `json:"network"`
	DisplayName string `json:"display_name"`
	Asset       string `json:"asset"`        // USDC contract address
	AssetSymbol string `json:"asset_symbol"` // "USDC"
	PayTo       string `json:"pay_to"`       // Tenant-bound payTo address
	Amount      string `json:"amount"`       // Exact USDC amount in base units
	Description string `json:"description"`
	QuoteID     string `json:"quote_id,omitempty"`
	Extra       any    `json:"extra,omitempty"`
}

// GetPaymentOptionsResult represents the result of getting payment options.
type GetPaymentOptionsResult struct {
	X402Version     int             `json:"x402_version"`
	Options         []PaymentOption `json:"options"`
	TopupURL        string          `json:"topup_url,omitempty"` // Human flow for manual top-up
	Message         string          `json:"message"`
	Instructions    string          `json:"instructions"`
	PaymentRequired string          `json:"payment_required,omitempty"`
}

func handleGetPaymentOptions(ctx context.Context, args GetPaymentOptionsInput, clients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return toolError("authentication required to create tenant-bound payment requirements")
	}
	resource := args.Resource
	if resource == "" {
		resource = "graphql://operation"
	}

	resp, err := clients.Purser.GetPaymentRequirements(ctx, tenantID, resource)
	if err != nil {
		logger.WithError(err).Warn("Failed to get payment requirements")
		return toolError(fmt.Sprintf("Failed to get payment options: %v", err))
	}

	if resp.Error != "" {
		return toolError(resp.Error)
	}

	options := make([]PaymentOption, 0, len(resp.Accepts))
	for _, accept := range resp.Accepts {
		amount := accept.Amount
		if amount == "" {
			amount = accept.MaxAmountRequired
		}
		var extra any
		if len(accept.ExtraJson) > 0 {
			_ = json.Unmarshal(accept.ExtraJson, &extra)
		}
		options = append(options, PaymentOption{
			Network:     accept.Network,
			DisplayName: networkDisplayName(accept.Network),
			Asset:       accept.Asset,
			AssetSymbol: "USDC", // All supported networks use USDC
			PayTo:       accept.PayTo,
			Amount:      amount,
			Description: accept.Description,
			QuoteID:     accept.QuoteId,
			Extra:       extra,
		})
	}
	paymentRequired, encodeErr := x402.EncodePaymentRequiredHeader(resp)
	if encodeErr != nil {
		logger.WithError(encodeErr).Warn("Failed to encode MCP payment requirements")
	}

	result := GetPaymentOptionsResult{
		X402Version:     int(resp.X402Version),
		Options:         options,
		TopupURL:        resp.TopupUrl,
		Message:         "Use one exact option to top up the authenticated tenant's prepaid balance.",
		PaymentRequired: paymentRequired,
		Instructions: `To top up balance:
1. Decode payment_required as the official x402 v2 PaymentRequired document
2. Choose one accepted requirement and create its exact EIP-3009 authorization
3. Echo that requirement in accepted, preserve its frameworks.quoteId, and sign the v2 payload
4. Base64-encode the PaymentPayload as PAYMENT-SIGNATURE and call submit_payment
5. Retry the original operation only after confirmed settlement`,
	}

	return toolSuccess(result)
}

// SubmitPaymentInput represents input for submit_payment tool.
type SubmitPaymentInput struct {
	Payment  string `json:"payment" jsonschema:"Base64-encoded official x402 v2 PAYMENT-SIGNATURE payload"`
	Resource string `json:"resource,omitempty" jsonschema:"Resource being paid for (required for non-zero payments). Supports stream_id or artifact_hash; relay IDs accepted. Use prefixes: playback: or ingest: for view/ingest keys."`
}

// SubmitPaymentResult represents the result of submitting a payment.
type SubmitPaymentResult struct {
	Success         bool   `json:"success"`
	IsAuthOnly      bool   `json:"is_auth_only"`   // Always false; retained for response compatibility
	TenantID        string `json:"tenant_id"`      // Credited tenant
	WalletAddress   string `json:"wallet_address"` // Payer wallet
	CreditedCents   int64  `json:"credited_cents"` // Amount credited
	NewBalance      int64  `json:"new_balance_cents,omitempty"`
	TxHash          string `json:"tx_hash,omitempty"` // Blockchain tx (for non-zero payments)
	TargetTenant    string `json:"target_tenant_id,omitempty"`
	SessionToken    string `json:"session_token,omitempty"`
	ExpiresAt       string `json:"expires_at,omitempty"`
	Message         string `json:"message"`
	PaymentResponse string `json:"payment_response,omitempty"`
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
		return toolError("zero-value x402 authentication is not supported; use the wallet challenge login flow")
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
	if settleResult.X402Version == 2 {
		result.PaymentResponse, _ = x402.EncodePaymentResponseHeader(settleResult, settleResult.Network)
	}

	return toolSuccess(result)
}

// networkDisplayName returns a human-readable name for an x402 network.
func networkDisplayName(network string) string {
	switch strings.ToLower(network) {
	case "eip155:8453":
		return "Base (Coinbase L2)"
	case "eip155:84532":
		return "Base Sepolia (Testnet)"
	case "eip155:42161":
		return "Arbitrum One"
	case "eip155:421614":
		return "Arbitrum Sepolia (Testnet)"
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
