package resolvers

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// DoGetBillingTiers returns available billing tiers
func (r *Resolver) DoGetBillingTiers(ctx context.Context) ([]*pb.BillingTier, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic billing tiers")
		return demo.GenerateBillingTiers(), nil
	}

	r.Logger.Info("Fetching billing tiers from Purser")

	resp, err := r.Clients.Purser.GetBillingTiers(ctx, false, nil)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to load billing tiers from Purser")
		return nil, fmt.Errorf("failed to load billing tiers: %w", err)
	}

	result := make([]*pb.BillingTier, len(resp.Tiers))
	for i := range resp.Tiers {
		result[i] = resp.Tiers[i]
	}

	return result, nil
}

// DoGetInvoices returns tenant invoices
func (r *Resolver) DoGetInvoices(ctx context.Context) ([]*pb.Invoice, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic invoices")
		return demo.GenerateInvoices(), nil
	}

	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("tenant context required")
	}

	r.Logger.WithField("tenant_id", tenantID).Info("Fetching invoices from Purser")

	resp, err := r.Clients.Purser.ListInvoices(ctx, tenantID, nil, nil)
	if err != nil {
		r.Logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to load invoices")
		return nil, fmt.Errorf("failed to load invoices: %w", err)
	}

	result := make([]*pb.Invoice, len(resp.Invoices))
	for i := range resp.Invoices {
		result[i] = resp.Invoices[i]
	}

	return result, nil
}

// DoGetInvoice returns a specific invoice by ID
func (r *Resolver) DoGetInvoice(ctx context.Context, id string) (*pb.Invoice, error) {
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("tenant context required")
	}

	r.Logger.WithField("tenant_id", tenantID).WithField("invoice_id", id).Info("Fetching invoice from Purser")

	// Get the specific invoice by ID
	resp, err := r.Clients.Purser.GetInvoice(ctx, id)
	if err != nil {
		r.Logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to load invoice")
		return nil, fmt.Errorf("failed to load invoice: %w", err)
	}

	if resp.Invoice == nil {
		return nil, fmt.Errorf("invoice not found")
	}

	return resp.Invoice, nil
}

// DoGetBillingStatus returns current billing status for tenant
func (r *Resolver) DoGetBillingStatus(ctx context.Context) (*pb.BillingStatusResponse, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic billing status")
		return demo.GenerateBillingStatus(), nil
	}

	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("tenant context required")
	}

	r.Logger.WithField("tenant_id", tenantID).Info("Getting billing status")

	// Get full billing status from Purser
	status, err := r.Clients.Purser.GetBillingStatus(ctx, tenantID)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get billing status from Purser")
		return nil, fmt.Errorf("failed to get billing status: %w", err)
	}

	if status == nil {
		return nil, fmt.Errorf("failed to get billing status: empty response")
	}

	// Ensure tenant ID is set
	if status.TenantId == "" {
		status.TenantId = tenantID
	}

	// Normalize billing status
	if status.BillingStatus == "" {
		status.BillingStatus = "active"
	}

	// NextBillingDate is already in the response from Purser

	return status, nil
}

// DoGetTenantUsage returns full tenant usage with maps converted to arrays
func (r *Resolver) DoGetTenantUsage(ctx context.Context, timeRange *model.TimeRangeInput) (*model.TenantUsage, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic tenant usage")
		return &model.TenantUsage{
			BillingPeriod: time.Now().Format("2006-01"),
			Usage: []*model.UsageEntry{
				{ResourceType: "stream_hours", Amount: 42.5},
				{ResourceType: "egress_gb", Amount: 15.2},
				{ResourceType: "storage_gb", Amount: 5.0},
			},
			Costs: []*model.CostEntry{
				{ResourceType: "stream_hours", Cost: 4.25},
				{ResourceType: "egress_gb", Cost: 0.76},
				{ResourceType: "storage_gb", Cost: 0.10},
			},
			TotalCost: 5.11,
			Currency:  "EUR",
		}, nil
	}

	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("tenant context required")
	}

	r.Logger.WithField("tenant_id", tenantID).Info("Getting tenant usage")

	// Determine date range
	var startDate, endDate string
	if timeRange != nil {
		startDate = timeRange.Start.Format("2006-01-02")
		endDate = timeRange.End.Format("2006-01-02")
	} else {
		// Default to current month
		now := time.Now()
		startDate = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		endDate = now.Format("2006-01-02")
	}

	// Get usage from Purser
	usage, err := r.Clients.Purser.GetTenantUsage(ctx, tenantID, startDate, endDate)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get tenant usage")
		return nil, fmt.Errorf("failed to get tenant usage: %w", err)
	}

	// Convert proto maps to model arrays
	var usageEntries []*model.UsageEntry
	for resourceType, amount := range usage.Usage {
		usageEntries = append(usageEntries, &model.UsageEntry{
			ResourceType: resourceType,
			Amount:       amount,
		})
	}

	var costEntries []*model.CostEntry
	for resourceType, cost := range usage.Costs {
		costEntries = append(costEntries, &model.CostEntry{
			ResourceType: resourceType,
			Cost:         cost,
		})
	}

	return &model.TenantUsage{
		BillingPeriod: usage.BillingPeriod,
		Usage:         usageEntries,
		Costs:         costEntries,
		TotalCost:     usage.TotalCost,
		Currency:      usage.Currency,
	}, nil
}

// DoGetUsageRecords returns usage records for tenant
func (r *Resolver) DoGetUsageRecords(ctx context.Context, timeRange *model.TimeRangeInput) ([]*pb.UsageRecord, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic usage records")
		return demo.GenerateUsageRecords(), nil
	}

	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("tenant context required")
	}

	r.Logger.WithField("tenant_id", tenantID).Info("Getting usage records")

	// Determine date range
	var startDate, endDate string
	if timeRange != nil {
		startDate = timeRange.Start.Format("2006-01-02")
		endDate = timeRange.End.Format("2006-01-02")
	} else {
		// Default to last 30 days
		now := time.Now()
		endDate = now.Format("2006-01-02")
		startDate = now.AddDate(0, 0, -30).Format("2006-01-02")
	}

	// Get usage from Purser
	usage, err := r.Clients.Purser.GetTenantUsage(ctx, tenantID, startDate, endDate)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get usage records")
		return nil, fmt.Errorf("failed to get usage records: %w", err)
	}

	// Convert usage response to records
	var records []*pb.UsageRecord
	for resourceType, amount := range usage.Usage {
		cost := float64(0)
		if c, exists := usage.Costs[resourceType]; exists {
			cost = c
		}

		record := &pb.UsageRecord{
			Id:           fmt.Sprintf("%s_%s_%s", tenantID, resourceType, usage.BillingPeriod),
			TenantId:     tenantID,
			UsageType:    resourceType,
			UsageValue:   amount,
			BillingMonth: usage.BillingPeriod,
		}

		// Store cost info in usage details via structpb
		if cost > 0 {
			unitPrice := float64(0)
			if amount > 0 {
				unitPrice = cost / amount
			}
			details := map[string]interface{}{
				"cost": map[string]interface{}{
					"quantity":   amount,
					"unit_price": unitPrice,
					"unit":       usage.Currency,
				},
			}
			if detailsStruct, err := structpb.NewStruct(details); err == nil {
				record.UsageDetails = detailsStruct
			}
		}

		records = append(records, record)
	}

	return records, nil
}

// DoCreatePayment processes a payment
func (r *Resolver) DoCreatePayment(ctx context.Context, input model.CreatePaymentInput) (*pb.PaymentResponse, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic payment")
		cur := "EUR"
		if input.Currency != nil {
			cur = *input.Currency
		}
		return &pb.PaymentResponse{
			Id:        "payment_demo_" + time.Now().Format("20060102150405"),
			Amount:    input.Amount,
			Currency:  cur,
			Status:    "completed",
			Method:    string(input.Method),
			CreatedAt: timestamppb.Now(),
		}, nil
	}

	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("tenant context required")
	}

	r.Logger.WithField("tenant_id", tenantID).
		WithField("amount", input.Amount).
		WithField("method", input.Method).
		Info("Creating payment")

	cur := "EUR"
	if input.Currency != nil {
		cur = *input.Currency
	}
	returnURL := ""
	if input.ReturnURL != nil {
		returnURL = *input.ReturnURL
	}

	paymentReq := &pb.PaymentRequest{
		InvoiceId: input.InvoiceID,
		Method:    string(input.Method),
		Amount:    input.Amount,
		Currency:  cur,
		ReturnUrl: returnURL,
	}

	resp, err := r.Clients.Purser.CreatePayment(ctx, paymentReq)
	if err != nil {
		r.Logger.WithError(err).
			WithField("tenant_id", tenantID).
			WithField("invoice_id", input.InvoiceID).
			Error("Failed to create payment")
		return nil, fmt.Errorf("failed to create payment: %w", err)
	}

	return resp, nil
}

// DoUpdateSubscriptionCustomTerms updates custom billing terms for a tenant subscription
func (r *Resolver) DoUpdateSubscriptionCustomTerms(ctx context.Context, tenantID string, input model.UpdateSubscriptionCustomTermsInput) (*pb.TenantSubscription, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic subscription update")
		return demo.GenerateBillingStatus().Subscription, nil
	}

	r.Logger.WithField("tenant_id", tenantID).Info("Updating subscription custom terms")

	// Build the update request
	req := &pb.UpdateSubscriptionRequest{
		TenantId: tenantID,
	}

	// Convert custom pricing input to proto
	if input.CustomPricing != nil {
		pricing := &pb.CustomPricing{}
		if input.CustomPricing.BasePrice != nil {
			pricing.BasePrice = *input.CustomPricing.BasePrice
		}
		if input.CustomPricing.DiscountRate != nil {
			pricing.DiscountRate = *input.CustomPricing.DiscountRate
		}
		if input.CustomPricing.OverageRates != nil {
			pricing.OverageRates = convertOverageRatesInput(input.CustomPricing.OverageRates)
		}
		req.CustomPricing = pricing
	}

	// Convert custom features input to proto
	if input.CustomFeatures != nil {
		features := &pb.BillingFeatures{}
		if input.CustomFeatures.Recording != nil {
			features.Recording = *input.CustomFeatures.Recording
		}
		if input.CustomFeatures.Analytics != nil {
			features.Analytics = *input.CustomFeatures.Analytics
		}
		if input.CustomFeatures.CustomBranding != nil {
			features.CustomBranding = *input.CustomFeatures.CustomBranding
		}
		if input.CustomFeatures.APIAccess != nil {
			features.ApiAccess = *input.CustomFeatures.APIAccess
		}
		if input.CustomFeatures.SupportLevel != nil {
			features.SupportLevel = *input.CustomFeatures.SupportLevel
		}
		if input.CustomFeatures.SLA != nil {
			features.Sla = *input.CustomFeatures.SLA
		}
		req.CustomFeatures = features
	}

	// Convert custom allocations input to proto
	if input.CustomAllocations != nil {
		req.CustomAllocations = convertAllocationDetailsInput(input.CustomAllocations)
	}

	// Call Purser to update the subscription
	subscription, err := r.Clients.Purser.UpdateSubscription(ctx, req)
	if err != nil {
		r.Logger.WithError(err).
			WithField("tenant_id", tenantID).
			Error("Failed to update subscription custom terms")
		return nil, fmt.Errorf("failed to update subscription: %w", err)
	}

	return subscription, nil
}

// Helper to convert AllocationDetailsInput to proto
func convertAllocationDetailsInput(input *model.AllocationDetailsInput) *pb.AllocationDetails {
	if input == nil {
		return nil
	}
	ad := &pb.AllocationDetails{}
	if input.Limit != nil {
		ad.Limit = input.Limit
	}
	if input.UnitPrice != nil {
		ad.UnitPrice = *input.UnitPrice
	}
	if input.Unit != nil {
		ad.Unit = *input.Unit
	}
	return ad
}

// Helper to convert OverageRatesInput to proto
func convertOverageRatesInput(input *model.OverageRatesInput) *pb.OverageRates {
	if input == nil {
		return nil
	}
	rates := &pb.OverageRates{}
	if input.Bandwidth != nil {
		rates.Bandwidth = convertAllocationDetailsInput(input.Bandwidth)
	}
	if input.Storage != nil {
		rates.Storage = convertAllocationDetailsInput(input.Storage)
	}
	if input.Compute != nil {
		rates.Compute = convertAllocationDetailsInput(input.Compute)
	}
	return rates
}
