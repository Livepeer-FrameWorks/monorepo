package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/mcperrors"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterBillingTools registers billing-related MCP tools.
func RegisterBillingTools(server *mcp.Server, clients *clients.ServiceClients, resolver *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) {
	// topup_balance - Request crypto top-up address
	addTool(server,
		&mcp.Tool{
			Name:        "topup_balance",
			Description: "Request a crypto deposit address to top up your prepaid balance. Returns a locked-rate quote: send exactly the quoted token amount within 24h to be credited at the locked USD price. Supports ETH and USDC. LPT is not yet supported.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args TopupBalanceInput) (*mcp.CallToolResult, any, error) {
			return handleTopupBalance(ctx, args, clients, checker, logger)
		},
	)

	// check_topup - Check if a top-up payment was received
	addTool(server,
		&mcp.Tool{
			Name:        "check_topup",
			Description: "Check the status of a pending crypto top-up.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args CheckTopupInput) (*mcp.CallToolResult, any, error) {
			return handleCheckTopup(ctx, args, clients, logger)
		},
	)

	addTool(server,
		&mcp.Tool{
			Name:        "pay_invoice",
			Description: "Create or resume payment for one invoice. Charges only the current outstanding balance. Repeating the same call resumes the pending checkout or crypto quote; a different method is rejected while payment is pending.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args PayInvoiceInput) (*mcp.CallToolResult, any, error) {
			return handlePayInvoice(ctx, args, clients, checker, logger)
		},
	)

	addTool(server,
		&mcp.Tool{
			Name:        "start_postpaid_setup",
			Description: "Start a provider-hosted postpaid setup for an eligible tier. Billing email and address must be complete. Returns an action URL; postpaid activates only after provider confirmation.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args StartPostpaidSetupInput) (*mcp.CallToolResult, any, error) {
			return handleStartPostpaidSetup(ctx, args, clients, checker, logger)
		},
	)

	addTool(server,
		&mcp.Tool{
			Name:        "complete_mollie_postpaid_setup",
			Description: "After the Mollie first-payment return, verify the tenant's valid mandate and create the recurring subscription for the selected tier. Safe to repeat.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args CompleteMolliePostpaidSetupInput) (*mcp.CallToolResult, any, error) {
			return handleCompleteMolliePostpaidSetup(ctx, args, clients, checker, logger)
		},
	)
}

type StartPostpaidSetupInput struct {
	Provider      string `json:"provider" jsonschema:"Configured provider: stripe or mollie"`
	TierID        string `json:"tier_id" jsonschema:"Eligible paid billing tier ID"`
	BillingPeriod string `json:"billing_period,omitempty" jsonschema:"Stripe billing period: monthly (default) or yearly"`
	SuccessURL    string `json:"success_url,omitempty" jsonschema:"Stripe success return URL"`
	CancelURL     string `json:"cancel_url,omitempty" jsonschema:"Stripe cancellation return URL"`
	ReturnURL     string `json:"return_url,omitempty" jsonschema:"Mollie first-payment return URL"`
	Method        string `json:"method,omitempty" jsonschema:"Mollie first-payment method: creditcard (default), ideal, or bancontact"`
}

type PostpaidSetupResult struct {
	Provider  string `json:"provider"`
	TierID    string `json:"tier_id"`
	Status    string `json:"status"`
	SetupID   string `json:"setup_id,omitempty"`
	ActionURL string `json:"action_url,omitempty"`
	Message   string `json:"message"`
}

func handleStartPostpaidSetup(ctx context.Context, args StartPostpaidSetupInput, clients *clients.ServiceClients, checker *preflight.Checker, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	args.Provider = strings.ToLower(strings.TrimSpace(args.Provider))
	args.TierID = strings.TrimSpace(args.TierID)
	if args.TierID == "" {
		return toolError("tier_id is required")
	}
	if args.Provider != "stripe" && args.Provider != "mollie" {
		return toolError("provider must be stripe or mollie")
	}
	if err := checker.RequireBillingDetails(ctx); err != nil {
		if pfe, ok := preflight.IsPreflightError(err); ok {
			return toolErrorWithResolution(pfe.Blocker)
		}
		return toolError(fmt.Sprintf("failed to verify billing details: %v", err))
	}
	statusResp, err := clients.Purser.GetBillingStatus(ctx, tenantID)
	if err != nil {
		return toolError(fmt.Sprintf("failed to read payment setup options: %v", err))
	}
	providerAvailable := false
	for _, provider := range statusResp.GetSetupProviders() {
		if strings.EqualFold(provider, args.Provider) {
			providerAvailable = true
			break
		}
	}
	if !providerAvailable {
		return toolError(fmt.Sprintf("provider %s is not configured for postpaid setup", args.Provider))
	}

	result := PostpaidSetupResult{Provider: args.Provider, TierID: args.TierID, Status: "action_required"}
	if args.Provider == "stripe" {
		period := strings.ToLower(strings.TrimSpace(args.BillingPeriod))
		if period == "" {
			period = "monthly"
		}
		if period != "monthly" && period != "yearly" {
			return toolError("billing_period must be monthly or yearly")
		}
		if strings.TrimSpace(args.SuccessURL) == "" || strings.TrimSpace(args.CancelURL) == "" {
			return toolError("success_url and cancel_url are required for Stripe setup")
		}
		resp, createErr := clients.Purser.CreateStripeCheckoutSession(ctx, tenantID, args.TierID, period, args.SuccessURL, args.CancelURL)
		if createErr != nil {
			logger.WithError(createErr).Warn("Failed to start Stripe postpaid setup")
			return toolError(fmt.Sprintf("failed to start Stripe setup: %v", createErr))
		}
		result.SetupID = resp.GetSessionId()
		result.ActionURL = resp.GetCheckoutUrl()
		result.Message = "Open action_url. The Stripe webhook confirms activation; a browser redirect is not proof of success."
		return toolSuccess(result)
	}

	method := strings.ToLower(strings.TrimSpace(args.Method))
	if method == "" {
		method = "creditcard"
	}
	if method != "creditcard" && method != "ideal" && method != "bancontact" {
		return toolError("method must be creditcard, ideal, or bancontact")
	}
	if strings.TrimSpace(args.ReturnURL) == "" {
		return toolError("return_url is required for Mollie setup")
	}
	resp, createErr := clients.Purser.CreateMollieFirstPayment(ctx, tenantID, args.TierID, method, args.ReturnURL)
	if createErr != nil {
		logger.WithError(createErr).Warn("Failed to start Mollie postpaid setup")
		return toolError(fmt.Sprintf("failed to start Mollie setup: %v", createErr))
	}
	result.SetupID = resp.GetPaymentId()
	result.ActionURL = resp.GetPaymentUrl()
	result.Message = "Open action_url. After the first payment returns, call complete_mollie_postpaid_setup; provider confirmation is authoritative."
	return toolSuccess(result)
}

type CompleteMolliePostpaidSetupInput struct {
	TierID string `json:"tier_id" jsonschema:"The same eligible tier used for start_postpaid_setup"`
}

func handleCompleteMolliePostpaidSetup(ctx context.Context, args CompleteMolliePostpaidSetupInput, clients *clients.ServiceClients, checker *preflight.Checker, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	args.TierID = strings.TrimSpace(args.TierID)
	if args.TierID == "" {
		return toolError("tier_id is required")
	}
	if err := checker.RequireBillingDetails(ctx); err != nil {
		if pfe, ok := preflight.IsPreflightError(err); ok {
			return toolErrorWithResolution(pfe.Blocker)
		}
		return toolError(fmt.Sprintf("failed to verify billing details: %v", err))
	}
	resp, err := clients.Purser.CreateMollieSubscription(ctx, tenantID, args.TierID, "", "FrameWorks postpaid subscription")
	if err != nil {
		logger.WithError(err).Warn("Failed to complete Mollie postpaid setup")
		return toolError(fmt.Sprintf("failed to complete Mollie setup: %v", err))
	}
	return toolSuccess(PostpaidSetupResult{
		Provider: "mollie", TierID: args.TierID, SetupID: resp.GetSubscriptionId(),
		Status: "confirmed", Message: "Mollie mandate and recurring subscription confirmed; postpaid billing is active.",
	})
}

type PayInvoiceInput struct {
	InvoiceID string `json:"invoice_id" jsonschema:"Invoice id from billing://invoices"`
	Method    string `json:"method" jsonschema:"Payment method: card, crypto_usdc, or crypto_eth"`
	ReturnURL string `json:"return_url,omitempty" jsonschema:"Optional same-origin web application URL used after card checkout"`
}

type PayInvoiceResult struct {
	PaymentID              string  `json:"payment_id"`
	InvoiceID              string  `json:"invoice_id"`
	Status                 string  `json:"status"`
	Method                 string  `json:"method"`
	Amount                 float64 `json:"amount"`
	Currency               string  `json:"currency"`
	PaymentURL             string  `json:"payment_url,omitempty"`
	WalletAddress          string  `json:"wallet_address,omitempty"`
	ExpectedAmountToken    string  `json:"expected_amount_token,omitempty"`
	ExpectedAmountBaseUnit string  `json:"expected_amount_base_units,omitempty"`
	Asset                  string  `json:"asset,omitempty"`
	Network                string  `json:"network,omitempty"`
	ExpiresAt              string  `json:"expires_at,omitempty"`
	Message                string  `json:"message"`
}

func handlePayInvoice(ctx context.Context, args PayInvoiceInput, clients *clients.ServiceClients, checker *preflight.Checker, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	args.InvoiceID = strings.TrimSpace(args.InvoiceID)
	args.Method = strings.ToLower(strings.TrimSpace(args.Method))
	if args.InvoiceID == "" {
		return toolError("invoice_id is required")
	}
	switch args.Method {
	case "card", "crypto_usdc", "crypto_eth":
	default:
		return toolError("method must be card, crypto_usdc, or crypto_eth")
	}
	if err := checker.RequireBillingDetails(ctx); err != nil {
		if pfe, ok := preflight.IsPreflightError(err); ok {
			return toolErrorWithResolution(pfe.Blocker)
		}
		return toolError(fmt.Sprintf("failed to verify billing details: %v", err))
	}

	resp, err := clients.Purser.CreatePayment(ctx, &purserpb.PaymentRequest{
		InvoiceId: args.InvoiceID,
		Method:    args.Method,
		ReturnUrl: args.ReturnURL,
	})
	if err != nil {
		logger.WithError(err).WithField("invoice_id", args.InvoiceID).Warn("Failed to create or resume invoice payment")
		return toolError(fmt.Sprintf("Failed to pay invoice: %v", err))
	}
	result := PayInvoiceResult{
		PaymentID:              resp.GetId(),
		InvoiceID:              args.InvoiceID,
		Status:                 resp.GetStatus(),
		Method:                 resp.GetMethod(),
		Amount:                 resp.GetAmount(),
		Currency:               resp.GetCurrency(),
		PaymentURL:             resp.GetPaymentUrl(),
		WalletAddress:          resp.GetWalletAddress(),
		ExpectedAmountToken:    resp.GetExpectedAmountToken(),
		ExpectedAmountBaseUnit: resp.GetExpectedAmountBaseUnits(),
		Asset:                  resp.GetAssetSymbol(),
		Network:                resp.GetNetwork(),
		Message:                "Payment is pending confirmation.",
	}
	if resp.GetExpiresAt() != nil {
		result.ExpiresAt = resp.GetExpiresAt().AsTime().UTC().Format(time.RFC3339)
	}
	if result.PaymentURL != "" {
		result.Message = "Open payment_url to complete the card payment. Reuse pay_invoice to resume this checkout."
	} else if result.WalletAddress != "" {
		result.Message = fmt.Sprintf("Send exactly %s %s to the wallet address on %s before expiry.", result.ExpectedAmountToken, result.Asset, result.Network)
	}
	return toolSuccess(result)
}

// TopupBalanceInput represents input for topup_balance tool.
type TopupBalanceInput struct {
	AmountCents int64  `json:"amount_cents" jsonschema:"Amount to credit, in tenant currency cents (USD or EUR per the account). Minimum: 1 cent; maximum: 10000000 cents."`
	Asset       string `json:"asset,omitempty" jsonschema:"Crypto asset to send: USDC or ETH. Default: USDC. (LPT is reserved and currently rejected.)"`
}

func handleTopupBalance(ctx context.Context, args TopupBalanceInput, clients *clients.ServiceClients, checker *preflight.Checker, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, nil, mcperrors.AuthRequired()
	}

	if args.AmountCents <= 0 {
		return toolError("amount must be positive; minimum crypto top-up is 1 cent")
	}
	if args.AmountCents < billing.CryptoTopupFloorCents {
		return toolError(fmt.Sprintf("minimum crypto top-up is %d cent", billing.CryptoTopupFloorCents))
	}
	if args.AmountCents > billing.MaximumTopupCents {
		return toolError(fmt.Sprintf("maximum crypto top-up is %d cents", billing.MaximumTopupCents))
	}

	// Default asset
	if args.Asset == "" {
		args.Asset = "USDC"
	}

	// Map asset string to proto enum
	var assetEnum purserpb.CryptoAsset
	switch strings.ToUpper(args.Asset) {
	case "ETH":
		assetEnum = purserpb.CryptoAsset_CRYPTO_ASSET_ETH
	case "USDC":
		assetEnum = purserpb.CryptoAsset_CRYPTO_ASSET_USDC
	case "LPT":
		return toolError("LPT prepaid top-ups are not yet supported. Use ETH or USDC.")
	default:
		return toolError(fmt.Sprintf("Invalid asset: %s. Valid options: USDC, ETH", args.Asset))
	}

	// Call Purser to create crypto top-up
	resp, err := clients.Purser.CreateCryptoTopup(ctx, &purserpb.CreateCryptoTopupRequest{
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
	TopupID string `json:"topup_id" jsonschema:"The top-up ID returned from topup_balance"`
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
