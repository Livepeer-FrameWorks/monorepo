package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/mcperrors"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"

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
		Description: "Canonical meter catalog and effective tier pricing rules, including zero-priced, API, AI, processing, and dimension-selected meters.",
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

	server.AddResource(&mcp.Resource{
		URI:         "billing://invoices",
		Name:        "Invoices",
		Description: "Recent draft and permanent invoices with itemized meter quantities, units, dimensions, and cluster attribution.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return handleBillingInvoices(ctx, clients, logger)
	})

	server.AddResource(&mcp.Resource{
		URI:         "billing://payments",
		Name:        "Invoice Payments",
		Description: "Recent invoice payment attempts and their confirmation state. Retry a failed payment with pay_invoice; pending calls are resumed idempotently.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return handleBillingPayments(ctx, clients, logger)
	})

	server.AddResource(&mcp.Resource{
		URI:         "billing://documents",
		Name:        "Billing Documents",
		Description: "Retained tenant-owned invoices, simplified invoices, confirmed-payment receipts, and credit notes.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return handleBillingDocuments(ctx, clients)
	})

	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "billing://invoices/{invoice_id}",
		Name:        "Invoice Details",
		Description: "A specific itemized invoice; finalized lines are an immutable snapshot while draft lines are a current preview.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return handleBillingInvoice(ctx, req.Params.URI, clients, logger)
	})

	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "billing://payments/{payment_id}",
		Name:        "Invoice Payment Details",
		Description: "One tenant-owned invoice payment with safe provider reference and confirmation timestamps.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return handleBillingPayment(ctx, req.Params.URI, clients, logger)
	})

	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "billing://documents/{kind}/{document_id}",
		Name:        "Billing Document Download",
		Description: "One retained tenant-owned printable billing document with its integrity hash.",
		MIMEType:    "text/html",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return handleBillingDocument(ctx, req.Params.URI, clients)
	})
}

type BillingDocumentInfo struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	DocumentNumber string `json:"document_number"`
	AmountCents    int64  `json:"amount_cents"`
	Currency       string `json:"currency"`
	Status         string `json:"status"`
	IssuedAt       string `json:"issued_at"`
	RetentionUntil string `json:"retention_until"`
	DownloadURI    string `json:"download_uri"`
}

func handleBillingDocuments(ctx context.Context, clients *clients.ServiceClients) (*mcp.ReadResourceResult, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, mcperrors.AuthRequired()
	}
	response, err := clients.Purser.ListBillingDocuments(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to list billing documents: %w", err)
	}
	documents := make([]BillingDocumentInfo, 0, len(response.GetDocuments()))
	for _, document := range response.GetDocuments() {
		if document == nil {
			continue
		}
		info := BillingDocumentInfo{
			ID: document.GetId(), Kind: document.GetKind(), DocumentNumber: document.GetDocumentNumber(),
			AmountCents: document.GetAmountCents(), Currency: document.GetCurrency(), Status: document.GetStatus(),
			DownloadURI: "billing://documents/" + document.GetKind() + "/" + document.GetId(),
		}
		if document.GetIssuedAt() != nil {
			info.IssuedAt = document.GetIssuedAt().AsTime().UTC().Format(time.RFC3339)
		}
		if document.GetRetentionUntil() != nil {
			info.RetentionUntil = document.GetRetentionUntil().AsTime().UTC().Format(time.RFC3339)
		}
		documents = append(documents, info)
	}
	return marshalResourceResult("billing://documents", map[string]any{"documents": documents})
}

func handleBillingDocument(ctx context.Context, uri string, clients *clients.ServiceClients) (*mcp.ReadResourceResult, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, mcperrors.AuthRequired()
	}
	path := strings.TrimPrefix(uri, "billing://documents/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return nil, fmt.Errorf("valid billing document kind and id required")
	}
	response, err := clients.Purser.GetBillingDocument(ctx, tenantID, parts[0], parts[1])
	if err != nil {
		return nil, fmt.Errorf("failed to read billing document: %w", err)
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
		URI: uri, MIMEType: response.GetContentType(), Text: string(response.GetContent()),
		Meta: mcp.Meta{"sha256": response.GetSha256()},
	}}}, nil
}

// BalanceInfo represents the billing://balance response.
type BalanceInfo struct {
	BalanceCents          int64  `json:"balance_cents"`
	ReservedBalanceCents  int64  `json:"reserved_balance_cents"`
	AvailableBalanceCents int64  `json:"available_balance_cents"`
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
			info.ReservedBalanceCents = balance.ReservedBalanceCents
			info.AvailableBalanceCents = balance.AvailableBalanceCents
			info.LowBalanceThreshold = balance.LowBalanceThresholdCents
			info.LowBalanceWarning = balance.AvailableBalanceCents < balance.LowBalanceThresholdCents
			info.DrainRateCentsPerHour = balance.DrainRateCentsPerHour
			if balance.DrainRateCentsPerHour > 0 && balance.AvailableBalanceCents > 0 {
				info.EstimatedHoursLeft = int(balance.AvailableBalanceCents / balance.DrainRateCentsPerHour)
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
		StorageGB:     summary.DisplayStorageGb,
	}
}

// PricingInfo represents the billing://pricing response.
type PricingInfo struct {
	TierName    string                     `json:"tier_name"`
	DisplayName string                     `json:"display_name"`
	TierLevel   int                        `json:"tier_level"`
	Resources   map[string]ResourcePricing `json:"resources"`
	Currency    string                     `json:"currency"`
	Scope       string                     `json:"scope"`
}

// ResourcePricing represents pricing for a single resource type.
type ResourcePricing struct {
	DisplayName       string   `json:"display_name"`
	Unit              string   `json:"unit"`
	SourceUnit        string   `json:"source_unit"`
	Aggregation       string   `json:"aggregation"`
	AllowedDimensions []string `json:"allowed_dimensions"`
	DefaultPriceable  bool     `json:"default_priceable"`
	Configured        bool     `json:"configured"`
	Model             string   `json:"model,omitempty"`
	Currency          string   `json:"currency,omitempty"`
	IncludedQuantity  string   `json:"included_quantity,omitempty"`
	UnitPrice         string   `json:"unit_price,omitempty"`
	Config            any      `json:"config,omitempty"`
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
	meterResp, err := clients.Purser.ListMeterDefinitions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get meter definitions: %w", err)
	}

	// Find the tenant's current tier if authenticated
	var currentTier *purserpb.BillingTier
	var subscription *purserpb.TenantSubscription
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
		TierName:    currentTier.TierName,
		DisplayName: currentTier.DisplayName,
		TierLevel:   int(currentTier.TierLevel),
		Currency:    currentTier.Currency,
		Scope:       "tenant tier; cluster-specific pricing is reflected in usage previews and invoices",
		Resources:   map[string]ResourcePricing{},
	}

	for _, definition := range meterResp.GetMeters() {
		if definition == nil || definition.GetMeter() == "" {
			continue
		}
		pricing.Resources[definition.GetMeter()] = ResourcePricing{
			DisplayName:       definition.GetDisplayName(),
			Unit:              definition.GetUnit(),
			SourceUnit:        definition.GetUnit(),
			Aggregation:       definition.GetAggregation(),
			AllowedDimensions: append([]string(nil), definition.GetAllowedDimensions()...),
			DefaultPriceable:  definition.GetDefaultPriceable(),
		}
	}

	for _, rule := range effectivePricingRules(currentTier.GetPricingRules(), subscription) {
		if rule == nil || rule.GetMeter() == "" {
			continue
		}
		unitPrice := rule.GetUnitPrice()
		if unitPrice == "" {
			return nil, fmt.Errorf("pricing rule %q has empty unit_price", rule.GetMeter())
		}
		resource, ok := pricing.Resources[rule.GetMeter()]
		if !ok {
			return nil, fmt.Errorf("pricing rule %q has no active meter definition", rule.GetMeter())
		}
		resource.Configured = true
		resource.Model = rule.GetModel()
		resource.Currency = rule.GetCurrency()
		if resource.Currency == "" {
			resource.Currency = currentTier.GetCurrency()
		}
		resource.IncludedQuantity = rule.GetIncludedQuantity()
		resource.UnitPrice = unitPrice
		if rawConfig := strings.TrimSpace(rule.GetConfigJson()); rawConfig != "" && rawConfig != "{}" {
			var config any
			if decodeErr := json.Unmarshal([]byte(rawConfig), &config); decodeErr != nil {
				return nil, fmt.Errorf("pricing rule %q has invalid config_json: %w", rule.GetMeter(), decodeErr)
			}
			resource.Config = config
		}
		resource.Unit = pricingRuleUnit(rule.GetMeter(), resource.SourceUnit, resource.Config)
		pricing.Resources[rule.GetMeter()] = resource
	}

	return marshalResourceResult("billing://pricing", pricing)
}

func pricingRuleUnit(meter, sourceUnit string, config any) string {
	if configMap, ok := config.(map[string]any); ok {
		if configured, ok := configMap["rated_unit"].(string); ok && strings.TrimSpace(configured) != "" {
			return strings.TrimSpace(configured)
		}
	}
	if meter == "storage_gb_seconds_hot" || meter == "storage_gb_seconds_cold" {
		return "gibibyte_hour"
	}
	return sourceUnit
}

// InvoiceLineInfo is the MCP representation of Purser's immutable line-item
// snapshot. Decimal fields remain strings to avoid precision loss.
type InvoiceLineInfo struct {
	LineKey          string         `json:"line_key"`
	Meter            string         `json:"meter,omitempty"`
	Description      string         `json:"description"`
	Quantity         string         `json:"quantity"`
	IncludedQuantity string         `json:"included_quantity"`
	BillableQuantity string         `json:"billable_quantity"`
	UnitPrice        string         `json:"unit_price"`
	Total            string         `json:"total"`
	Currency         string         `json:"currency"`
	Unit             string         `json:"unit"`
	Dimensions       map[string]any `json:"dimensions"`
	ClusterID        string         `json:"cluster_id,omitempty"`
	ClusterName      string         `json:"cluster_name,omitempty"`
	ClusterKind      string         `json:"cluster_kind,omitempty"`
	PricingSource    string         `json:"pricing_source"`
	PricingLabel     string         `json:"pricing_label,omitempty"`
}

type InvoiceInfo struct {
	ID                   string            `json:"id"`
	Status               string            `json:"status"`
	Amount               float64           `json:"amount"`
	BaseAmount           float64           `json:"base_amount"`
	MeteredAmount        float64           `json:"metered_amount"`
	GrossMeteredAmount   float64           `json:"gross_metered_amount"`
	PrepaidCreditApplied float64           `json:"prepaid_credit_applied"`
	Currency             string            `json:"currency"`
	PeriodStart          string            `json:"period_start,omitempty"`
	PeriodEnd            string            `json:"period_end,omitempty"`
	DueDate              string            `json:"due_date,omitempty"`
	PaidAt               string            `json:"paid_at,omitempty"`
	CreatedAt            string            `json:"created_at,omitempty"`
	UpdatedAt            string            `json:"updated_at,omitempty"`
	UsageDetails         map[string]any    `json:"usage_details,omitempty"`
	LineItems            []InvoiceLineInfo `json:"line_items"`
}

type InvoicesResponse struct {
	Invoices []InvoiceInfo `json:"invoices"`
	HasMore  bool          `json:"has_more"`
}

type PaymentInfo struct {
	ID          string  `json:"id"`
	InvoiceID   string  `json:"invoice_id"`
	Method      string  `json:"method"`
	Amount      float64 `json:"amount"`
	Currency    string  `json:"currency"`
	Status      string  `json:"status"`
	ProviderRef string  `json:"provider_reference,omitempty"`
	ConfirmedAt string  `json:"confirmed_at,omitempty"`
	CreatedAt   string  `json:"created_at,omitempty"`
	UpdatedAt   string  `json:"updated_at,omitempty"`
}

type PaymentsResponse struct {
	OutstandingAmount float64       `json:"outstanding_amount"`
	Currency          string        `json:"currency"`
	PaymentMethods    []string      `json:"payment_methods"`
	Payments          []PaymentInfo `json:"payments"`
	HasMore           bool          `json:"has_more"`
}

func invoiceLineInfo(line *purserpb.LineItem) InvoiceLineInfo {
	dimensions := map[string]any{}
	if line.GetDimensions() != nil {
		dimensions = line.GetDimensions().AsMap()
	}
	return InvoiceLineInfo{
		LineKey:          line.GetLineKey(),
		Meter:            line.GetMeter(),
		Description:      line.GetDescription(),
		Quantity:         line.GetQuantity(),
		IncludedQuantity: line.GetIncludedQuantity(),
		BillableQuantity: line.GetBillableQuantity(),
		UnitPrice:        line.GetUnitPrice(),
		Total:            line.GetTotal(),
		Currency:         line.GetCurrency(),
		Unit:             line.GetUnit(),
		Dimensions:       dimensions,
		ClusterID:        line.GetClusterId(),
		ClusterName:      line.GetClusterName(),
		ClusterKind:      line.GetClusterKind(),
		PricingSource:    line.GetPricingSource(),
		PricingLabel:     line.GetPricingLabel(),
	}
}

func invoiceInfo(invoice *purserpb.Invoice) InvoiceInfo {
	info := InvoiceInfo{
		ID:                   invoice.GetId(),
		Status:               invoice.GetStatus(),
		Amount:               invoice.GetAmount(),
		BaseAmount:           invoice.GetBaseAmount(),
		MeteredAmount:        invoice.GetMeteredAmount(),
		GrossMeteredAmount:   invoice.GetGrossMeteredAmount(),
		PrepaidCreditApplied: invoice.GetPrepaidCreditApplied(),
		Currency:             invoice.GetCurrency(),
		LineItems:            make([]InvoiceLineInfo, 0, len(invoice.GetLineItems())),
	}
	if invoice.GetPeriodStart() != nil {
		info.PeriodStart = invoice.GetPeriodStart().AsTime().UTC().Format("2006-01-02T15:04:05Z")
	}
	if invoice.GetPeriodEnd() != nil {
		info.PeriodEnd = invoice.GetPeriodEnd().AsTime().UTC().Format("2006-01-02T15:04:05Z")
	}
	if invoice.GetDueDate() != nil {
		info.DueDate = invoice.GetDueDate().AsTime().UTC().Format("2006-01-02T15:04:05Z")
	}
	if invoice.GetPaidAt() != nil {
		info.PaidAt = invoice.GetPaidAt().AsTime().UTC().Format("2006-01-02T15:04:05Z")
	}
	if invoice.GetCreatedAt() != nil {
		info.CreatedAt = invoice.GetCreatedAt().AsTime().UTC().Format("2006-01-02T15:04:05Z")
	}
	if invoice.GetUpdatedAt() != nil {
		info.UpdatedAt = invoice.GetUpdatedAt().AsTime().UTC().Format("2006-01-02T15:04:05Z")
	}
	if invoice.GetUsageDetails() != nil {
		info.UsageDetails = invoice.GetUsageDetails().AsMap()
	}
	for _, line := range invoice.GetLineItems() {
		if line != nil {
			info.LineItems = append(info.LineItems, invoiceLineInfo(line))
		}
	}
	return info
}

func handleBillingInvoices(ctx context.Context, clients *clients.ServiceClients, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, mcperrors.AuthRequired()
	}
	resp, err := clients.Purser.ListInvoices(ctx, tenantID, nil, &commonpb.CursorPaginationRequest{First: 20})
	if err != nil {
		logger.WithError(err).Warn("Failed to list invoices")
		return nil, fmt.Errorf("failed to list invoices: %w", err)
	}
	invoices := make([]InvoiceInfo, 0, len(resp.GetInvoices()))
	for _, invoice := range resp.GetInvoices() {
		if invoice != nil {
			invoices = append(invoices, invoiceInfo(invoice))
		}
	}
	hasMore := resp.GetPagination() != nil && resp.GetPagination().GetHasNextPage()
	return marshalResourceResult("billing://invoices", InvoicesResponse{Invoices: invoices, HasMore: hasMore})
}

func handleBillingPayments(ctx context.Context, clients *clients.ServiceClients, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, mcperrors.AuthRequired()
	}
	status, err := clients.Purser.GetBillingStatus(ctx, tenantID)
	if err != nil {
		logger.WithError(err).Warn("Failed to get invoice payments")
		return nil, fmt.Errorf("failed to get invoice payments: %w", err)
	}
	paymentPage, err := clients.Purser.ListPayments(ctx, &purserpb.ListPaymentsRequest{
		TenantId:   tenantID,
		Pagination: &commonpb.CursorPaginationRequest{First: 50},
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to list invoice payment records")
		return nil, fmt.Errorf("failed to list invoice payment records: %w", err)
	}
	result := PaymentsResponse{
		OutstandingAmount: status.GetOutstandingAmount(),
		Currency:          status.GetCurrency(),
		PaymentMethods:    append([]string(nil), status.GetPaymentMethods()...),
		Payments:          make([]PaymentInfo, 0, len(paymentPage.GetPayments())),
		HasMore:           paymentPage.GetPagination().GetHasNextPage(),
	}
	for _, payment := range paymentPage.GetPayments() {
		if payment == nil {
			continue
		}
		info := PaymentInfo{
			ID:          payment.GetId(),
			InvoiceID:   payment.GetInvoiceId(),
			Method:      payment.GetMethod(),
			Amount:      payment.GetAmount(),
			Currency:    payment.GetCurrency(),
			Status:      payment.GetStatus(),
			ProviderRef: payment.GetTxId(),
		}
		if payment.GetConfirmedAt() != nil {
			info.ConfirmedAt = payment.GetConfirmedAt().AsTime().UTC().Format(time.RFC3339)
		}
		if payment.GetCreatedAt() != nil {
			info.CreatedAt = payment.GetCreatedAt().AsTime().UTC().Format(time.RFC3339)
		}
		if payment.GetUpdatedAt() != nil {
			info.UpdatedAt = payment.GetUpdatedAt().AsTime().UTC().Format(time.RFC3339)
		}
		result.Payments = append(result.Payments, info)
	}
	return marshalResourceResult("billing://payments", result)
}

func handleBillingPayment(ctx context.Context, uri string, clients *clients.ServiceClients, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, mcperrors.AuthRequired()
	}
	paymentID := strings.TrimPrefix(uri, "billing://payments/")
	if paymentID == "" || paymentID == uri {
		return nil, fmt.Errorf("invalid payment id")
	}
	payment, err := clients.Purser.GetPayment(ctx, paymentID)
	if err != nil {
		logger.WithError(err).Warn("Failed to get invoice payment")
		return nil, fmt.Errorf("failed to get invoice payment: %w", err)
	}
	info := PaymentInfo{
		ID: payment.GetId(), InvoiceID: payment.GetInvoiceId(), Method: payment.GetMethod(),
		Amount: payment.GetAmount(), Currency: payment.GetCurrency(), Status: payment.GetStatus(),
		ProviderRef: payment.GetTxId(),
	}
	if payment.GetConfirmedAt() != nil {
		info.ConfirmedAt = payment.GetConfirmedAt().AsTime().UTC().Format(time.RFC3339)
	}
	if payment.GetCreatedAt() != nil {
		info.CreatedAt = payment.GetCreatedAt().AsTime().UTC().Format(time.RFC3339)
	}
	if payment.GetUpdatedAt() != nil {
		info.UpdatedAt = payment.GetUpdatedAt().AsTime().UTC().Format(time.RFC3339)
	}
	return marshalResourceResult(uri, info)
}

func handleBillingInvoice(ctx context.Context, uri string, clients *clients.ServiceClients, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, mcperrors.AuthRequired()
	}
	invoiceID := strings.TrimPrefix(uri, "billing://invoices/")
	if invoiceID == "" || invoiceID == uri {
		return nil, fmt.Errorf("invalid invoice id")
	}
	resp, err := clients.Purser.GetInvoice(ctx, invoiceID)
	if err != nil {
		logger.WithError(err).Warn("Failed to get invoice")
		return nil, fmt.Errorf("failed to get invoice: %w", err)
	}
	if resp.GetInvoice() == nil {
		return nil, fmt.Errorf("invoice not found")
	}
	return marshalResourceResult(uri, invoiceInfo(resp.GetInvoice()))
}

func effectivePricingRules(tierRules []*purserpb.PricingRule, subscription *purserpb.TenantSubscription) []*purserpb.PricingRule {
	if subscription == nil || len(subscription.GetPricingOverrides()) == 0 {
		return tierRules
	}
	overrides := make(map[string]*purserpb.PricingRule, len(subscription.GetPricingOverrides()))
	for _, override := range subscription.GetPricingOverrides() {
		if override == nil || override.GetMeter() == "" {
			continue
		}
		overrides[override.GetMeter()] = override
	}

	out := make([]*purserpb.PricingRule, 0, len(tierRules)+len(overrides))
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

func mergePricingRule(base, override *purserpb.PricingRule) *purserpb.PricingRule {
	if base == nil {
		return override
	}
	merged := &purserpb.PricingRule{
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
	pagination := &commonpb.CursorPaginationRequest{
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
