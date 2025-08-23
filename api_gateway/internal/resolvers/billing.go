package resolvers

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/pkg/api/purser"
	"frameworks/pkg/models"
)

// DoGetBillingTiers returns available billing tiers
func (r *Resolver) DoGetBillingTiers(ctx context.Context) ([]*models.BillingTier, error) {
	// Check for demo mode
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo billing tiers data")
		return demo.GenerateBillingTiers(), nil
	}

	r.Logger.Info("Getting billing tiers")

	// TODO: Add GetBillingTiers method to Purser client
	// For now, return empty slice until method is added
	return []*models.BillingTier{}, nil
}

// DoGetInvoices returns tenant invoices
func (r *Resolver) DoGetInvoices(ctx context.Context) ([]*models.Invoice, error) {
	// Check for demo mode
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo invoices data")
		return demo.GenerateInvoices(), nil
	}

	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("tenant context required")
	}

	r.Logger.WithField("tenant_id", tenantID).Info("Getting invoices")

	// TODO: Add GetInvoices method to Purser client
	return []*models.Invoice{}, nil
}

// DoGetInvoice returns a specific invoice by ID
func (r *Resolver) DoGetInvoice(ctx context.Context, id string) (*models.Invoice, error) {
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("tenant context required")
	}

	r.Logger.WithField("tenant_id", tenantID).WithField("invoice_id", id).Info("Getting invoice")

	// TODO: Add GetInvoice method to Purser client
	return nil, fmt.Errorf("invoice not found")
}

// DoGetBillingStatus returns current billing status for tenant
func (r *Resolver) DoGetBillingStatus(ctx context.Context) (*models.BillingStatus, error) {
	// Check for demo mode
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo billing status data")
		return demo.GenerateBillingStatus(), nil
	}

	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("tenant context required")
	}

	r.Logger.WithField("tenant_id", tenantID).Info("Getting billing status")

	// Get subscription info from Purser
	subscription, err := r.Clients.Purser.GetSubscription(ctx, tenantID)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get subscription")
		return nil, fmt.Errorf("failed to get billing status: %w", err)
	}

	// Build BillingStatus from available subscription data
	// Default next billing date to beginning of next month
	now := time.Now()
	nextMonth := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, now.Location())

	status := &models.BillingStatus{
		TenantID:        tenantID,
		Status:          "active",
		NextBillingDate: &nextMonth,
	}

	if subscription.Subscription != nil {
		status.Status = subscription.Subscription.Status
		// Convert subscription info to tenant subscription
		status.Subscription = models.TenantSubscription{
			ID:       subscription.Subscription.ID,
			TenantID: subscription.Subscription.TenantID,
			TierID:   subscription.Subscription.TierID,
			Status:   subscription.Subscription.Status,
		}

		// Calculate next billing date from subscription start date and billing period
		if subscription.Subscription.StartDate != "" {
			if startDate, err := time.Parse("2006-01-02", subscription.Subscription.StartDate); err == nil {
				switch subscription.Subscription.BillingPeriod {
				case "monthly":
					// Find next month from start date that's in the future
					nextBilling := startDate
					for nextBilling.Before(now) {
						nextBilling = nextBilling.AddDate(0, 1, 0)
					}
					status.NextBillingDate = &nextBilling
				case "yearly":
					// Find next year from start date that's in the future
					nextBilling := startDate
					for nextBilling.Before(now) {
						nextBilling = nextBilling.AddDate(1, 0, 0)
					}
					status.NextBillingDate = &nextBilling
				}
			}
		}
	}

	return status, nil
}

// DoGetUsageRecords returns usage records for tenant
func (r *Resolver) DoGetUsageRecords(ctx context.Context, timeRange *model.TimeRangeInput) ([]*models.UsageRecord, error) {
	// Check for demo mode
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo usage records data")
		return demo.GenerateUsageRecords(), nil
	}

	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("tenant context required")
	}

	r.Logger.WithField("tenant_id", tenantID).Info("Getting usage records")

	// Build request for Purser
	req := &purser.TenantUsageRequest{
		TenantID: tenantID,
	}

	if timeRange != nil {
		req.StartDate = timeRange.Start.Format("2006-01-02")
		req.EndDate = timeRange.End.Format("2006-01-02")
	} else {
		// Default to last 30 days
		now := time.Now()
		req.EndDate = now.Format("2006-01-02")
		req.StartDate = now.AddDate(0, 0, -30).Format("2006-01-02")
	}

	// Get usage from Purser
	usage, err := r.Clients.Purser.GetTenantUsage(ctx, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get usage records")
		return nil, fmt.Errorf("failed to get usage records: %w", err)
	}

	// Convert usage map to UsageRecord models
	var records []*models.UsageRecord
	for resourceType, amount := range usage.Usage {
		cost := 0.0
		if c, exists := usage.Costs[resourceType]; exists {
			cost = c
		}

		record := &models.UsageRecord{
			ID:           fmt.Sprintf("%s_%s_%s", tenantID, resourceType, usage.BillingPeriod),
			TenantID:     tenantID,
			UsageType:    resourceType,
			UsageValue:   amount,
			BillingMonth: usage.BillingPeriod,
			CreatedAt:    time.Now(),
		}

		// Store cost in usage details
		record.UsageDetails = models.UsageDetails{
			"cost": {
				Quantity:  amount,
				UnitPrice: cost / amount, // Calculate unit price
				Unit:      usage.Currency,
			},
		}

		records = append(records, record)
	}

	return records, nil
}

// DoCreatePayment processes a payment
func (r *Resolver) DoCreatePayment(ctx context.Context, input model.CreatePaymentInput) (*models.Payment, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo payment creation")
		cur := "EUR"
		if input.Currency != nil {
			cur = string(*input.Currency)
		}
		return &models.Payment{
			ID:        "payment_demo_" + time.Now().Format("20060102150405"),
			Amount:    input.Amount,
			Currency:  cur,
			Method:    string(input.Method),
			Status:    "completed",
			CreatedAt: time.Now(),
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

	// TODO: Add CreatePayment method to Purser client
	// For now, return a mock payment
	cur := "EUR"
	if input.Currency != nil {
		cur = *input.Currency
	}
	return &models.Payment{
		ID:       "payment_" + tenantID,
		Amount:   input.Amount,
		Currency: cur,
		Method:   string(input.Method),
		Status:   "pending",
	}, nil
}

// DoUpdateBillingTier changes the tenant's billing tier
func (r *Resolver) DoUpdateBillingTier(ctx context.Context, tierID string) (*models.BillingStatus, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo billing tier update")
		return demo.GenerateBillingStatus(), nil
	}

	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("tenant context required")
	}

	r.Logger.WithField("tenant_id", tenantID).
		WithField("tier_id", tierID).
		Info("Updating billing tier")

	// TODO: Add UpdateBillingTier method to Purser client
	// For now, return current status
	return r.DoGetBillingStatus(ctx)
}
