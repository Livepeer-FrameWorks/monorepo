package tools

import (
	"context"
	"fmt"
	"strings"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/mcperrors"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/resolvers"
	"frameworks/pkg/billing"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterBillingTools registers billing-related MCP tools.
func RegisterBillingTools(server *mcp.Server, clients *clients.ServiceClients, resolver *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) {
	// topup_balance - Request crypto top-up address
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "topup_balance",
			Description: "Request a crypto deposit address to top up your prepaid balance. Returns a locked-rate quote: send exactly the quoted token amount within 24h to be credited at the locked USD price. Supports ETH and USDC. LPT is not yet supported.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args TopupBalanceInput) (*mcp.CallToolResult, any, error) {
			return handleTopupBalance(ctx, args, clients, checker, logger)
		},
	)

	// check_topup - Check if a top-up payment was received
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "check_topup",
			Description: "Check the status of a pending crypto top-up.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args CheckTopupInput) (*mcp.CallToolResult, any, error) {
			return handleCheckTopup(ctx, args, clients, logger)
		},
	)
}

// TopupBalanceInput represents input for topup_balance tool.
type TopupBalanceInput struct {
	AmountCents int64  `json:"amount_cents" jsonschema:"required" jsonschema_description:"Amount to credit, in tenant currency cents (USD or EUR per the account). Must be positive."`
	Asset       string `json:"asset,omitempty" jsonschema_description:"Crypto asset to send: USDC or ETH. Default: USDC. (LPT is reserved and currently rejected.)"`
}

func handleTopupBalance(ctx context.Context, args TopupBalanceInput, clients *clients.ServiceClients, checker *preflight.Checker, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, nil, mcperrors.AuthRequired()
	}

	// Validate amount (must be positive)
	if args.AmountCents <= 0 {
		return toolError("amount must be positive")
	}

	// Default asset
	if args.Asset == "" {
		args.Asset = "USDC"
	}

	// Map asset string to proto enum
	var assetEnum pb.CryptoAsset
	switch strings.ToUpper(args.Asset) {
	case "ETH":
		assetEnum = pb.CryptoAsset_CRYPTO_ASSET_ETH
	case "USDC":
		assetEnum = pb.CryptoAsset_CRYPTO_ASSET_USDC
	case "LPT":
		return toolError("LPT prepaid top-ups are not yet supported. Use ETH or USDC.")
	default:
		return toolError(fmt.Sprintf("Invalid asset: %s. Valid options: USDC, ETH", args.Asset))
	}

	// Call Purser to create crypto top-up
	resp, err := clients.Purser.CreateCryptoTopup(ctx, &pb.CreateCryptoTopupRequest{
		TenantId:            tenantID,
		ExpectedAmountCents: args.AmountCents,
		Asset:               assetEnum,
		Currency:            billing.DefaultCurrency(),
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create crypto topup")
		return toolError(fmt.Sprintf("Failed to create top-up: %v", err))
	}

	message := fmt.Sprintf("Send %s to %s. Use check_topup to verify payment received.", resp.AssetSymbol, resp.DepositAddress)
	if resp.ExpectedAmountToken != "" && resp.QuotedPriceUsd != "" {
		message = fmt.Sprintf(
			"Send exactly %s %s to %s on %s (locked at $%s/%s, valid until %s). Use check_topup to verify.",
			resp.ExpectedAmountToken, resp.AssetSymbol, resp.DepositAddress, resp.Network,
			resp.QuotedPriceUsd, resp.AssetSymbol,
			resp.ExpiresAt.AsTime().Format("2006-01-02T15:04:05Z"),
		)
	}

	result := TopupResult{
		TopupID:        resp.TopupId,
		DepositAddress: resp.DepositAddress,
		Asset:          resp.AssetSymbol,
		AmountCents:    resp.ExpectedAmountCents,
		ExpiresAt:      resp.ExpiresAt.AsTime().Format("2006-01-02T15:04:05Z"),
		Message:        message,
		TokenAmount:    resp.ExpectedAmountToken,
		PriceUSD:       resp.QuotedPriceUsd,
		QuoteSource:    resp.QuoteSource,
		Network:        resp.Network,
	}

	return toolSuccess(result)
}

// CheckTopupInput represents input for check_topup tool.
type CheckTopupInput struct {
	TopupID string `json:"topup_id" jsonschema:"required" jsonschema_description:"The top-up ID returned from topup_balance"`
}

// CheckTopupResult represents the result of checking a top-up.
type CheckTopupResult struct {
	TopupID          string `json:"topup_id"`
	Status           string `json:"status"` // pending, confirming, completed, expired
	Confirmed        bool   `json:"confirmed"`
	CreditedCents    int64  `json:"credited_cents,omitempty"`
	CreditedCurrency string `json:"credited_currency,omitempty"`
	BalanceCents     int64  `json:"balance_cents,omitempty"`
	TxHash           string `json:"tx_hash,omitempty"`
	Confirmations    int32  `json:"confirmations,omitempty"`
	Message          string `json:"message"`
}

func handleCheckTopup(ctx context.Context, args CheckTopupInput, clients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}

	if args.TopupID == "" {
		return toolError("topup_id is required")
	}

	// Call Purser to check top-up status (just topupID, no tenantID)
	resp, err := clients.Purser.GetCryptoTopup(ctx, args.TopupID)
	if err != nil {
		logger.WithError(err).Warn("Failed to check topup status")
		return toolError(fmt.Sprintf("Failed to check top-up status: %v", err))
	}

	result := CheckTopupResult{
		TopupID:          resp.Id,
		Status:           resp.Status,
		Confirmed:        resp.Status == "completed",
		TxHash:           resp.TxHash,
		Confirmations:    resp.Confirmations,
		CreditedCurrency: resp.CreditedAmountCurrency,
	}

	switch resp.Status {
	case "completed":
		result.CreditedCents = resp.CreditedAmountCents
		ccy := resp.CreditedAmountCurrency
		if ccy == "" {
			ccy = "USD"
		}
		result.Message = fmt.Sprintf("Payment confirmed! %d %s cents credited to your balance (tx: %s).", resp.CreditedAmountCents, ccy, resp.TxHash)
	case "confirming":
		result.Message = fmt.Sprintf("Payment detected (tx: %s). Waiting for confirmations (%d so far).", resp.TxHash, resp.Confirmations)
	case "pending":
		result.Message = "Payment not yet received. Please complete the transfer and check again."
	case "expired":
		result.Message = "Top-up request expired. Create a new top-up request."
	default:
		result.Message = fmt.Sprintf("Top-up status: %s", resp.Status)
	}

	return toolSuccess(result)
}

// toolErrorWithResolution returns an error with resolution guidance.
func toolErrorWithResolution(blocker preflight.Blocker) (*mcp.CallToolResult, any, error) {
	message := fmt.Sprintf("%s\n\nResolution: %s", blocker.Message, blocker.Resolution)
	if blocker.Tool != "" {
		message += fmt.Sprintf("\nUse tool: %s", blocker.Tool)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: message},
		},
		IsError: true,
	}, blocker, nil
}
