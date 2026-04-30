package resolvers

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/pkg/billing"
	periscope "frameworks/pkg/clients/periscope"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/pagination"
	pb "frameworks/pkg/proto"
	x402 "frameworks/pkg/x402"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ============================================================================
// CONNECTION RESOLVERS (Relay-style pagination)
// ============================================================================

// DoGetInvoicesConnection returns a Relay-style connection for invoices
func (r *Resolver) DoGetInvoicesConnection(ctx context.Context, first *int, after *string, last *int, before *string) (*model.InvoicesConnection, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic invoices connection")
		invoices := demo.GenerateInvoices()
		return r.buildInvoicesConnection(invoices, first, after), nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// Build pagination request
	paginationReq := buildBillingPaginationRequest(first, after, last, before)

	r.Logger.WithField("tenant_id", tenantID).Info("Fetching invoices connection from Purser")

	resp, err := r.Clients.Purser.ListInvoices(ctx, tenantID, nil, paginationReq)
	if err != nil {
		r.Logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to load invoices")
		return nil, fmt.Errorf("failed to load invoices: %w", err)
	}

	// Build edges with keyset cursors
	edges := make([]*model.InvoiceEdge, len(resp.Invoices))
	for i, invoice := range resp.Invoices {
		cursorTime := invoice.CreatedAt.AsTime()
		if invoice.PeriodStart != nil {
			cursorTime = invoice.PeriodStart.AsTime()
		}
		cursor := pagination.EncodeCursor(cursorTime, invoice.Id)
		edges[i] = &model.InvoiceEdge{
			Cursor: cursor,
			Node:   invoice,
		}
	}

	// Build page info from response pagination
	pageInfo := &model.PageInfo{
		HasPreviousPage: after != nil && *after != "",
		HasNextPage:     resp.Pagination.GetHasNextPage(),
	}
	if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
	}

	edgeNodes := make([]*pb.Invoice, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.InvoicesConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: int(resp.Pagination.GetTotalCount()),
	}, nil
}

// DoGetUsageRecordsConnection returns a Relay-style connection for usage records
func (r *Resolver) DoGetUsageRecordsConnection(ctx context.Context, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string) (*model.UsageRecordsConnection, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic usage records connection")
		records := demo.GenerateUsageRecords()
		return r.buildUsageRecordsConnection(records, first, after), nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// Build time range (required by Purser)
	tr := buildUsageTimeRange(timeRange, 30*24*time.Hour)

	// Build pagination request
	paginationReq := buildBillingPaginationRequest(first, after, last, before)

	r.Logger.WithField("tenant_id", tenantID).Info("Fetching usage records connection from Purser")

	resp, err := r.Clients.Purser.GetUsageRecords(ctx, tenantID, "", "", tr, paginationReq)
	if err != nil {
		r.Logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to load usage records")
		return nil, fmt.Errorf("failed to load usage records: %w", err)
	}

	// Build edges with keyset cursors
	edges := make([]*model.UsageRecordEdge, len(resp.UsageRecords))
	for i, record := range resp.UsageRecords {
		cursorTime := record.CreatedAt.AsTime()
		if record.PeriodStart != nil {
			cursorTime = record.PeriodStart.AsTime()
		}
		cursor := pagination.EncodeCursor(cursorTime, record.Id)
		edges[i] = &model.UsageRecordEdge{
			Cursor: cursor,
			Node:   record,
		}
	}

	// Build page info from response pagination
	pageInfo := &model.PageInfo{
		HasPreviousPage: after != nil && *after != "",
		HasNextPage:     resp.Pagination.GetHasNextPage(),
	}
	if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
	}

	edgeNodes := make([]*pb.UsageRecord, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.UsageRecordsConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: int(resp.Pagination.GetTotalCount()),
	}, nil
}

// buildBillingPaginationRequest creates a proto pagination request from GraphQL params
func buildBillingPaginationRequest(first *int, after *string, last *int, before *string) *pb.CursorPaginationRequest {
	req := &pb.CursorPaginationRequest{}

	if first != nil {
		limit := pagination.ClampLimit(*first)
		req.First = int32(limit)
	} else if last == nil {
		req.First = int32(pagination.DefaultLimit)
	}

	if after != nil && *after != "" {
		req.After = after
	}

	if last != nil {
		limit := pagination.ClampLimit(*last)
		req.Last = int32(limit)
	}

	if before != nil && *before != "" {
		req.Before = before
	}

	return req
}

// buildInvoicesConnection is a helper for demo mode
func (r *Resolver) buildInvoicesConnection(invoices []*pb.Invoice, first *int, after *string) *model.InvoicesConnection {
	limit := pagination.DefaultLimit
	if first != nil {
		limit = pagination.ClampLimit(*first)
	}

	if len(invoices) > limit {
		invoices = invoices[:limit]
	}

	edges := make([]*model.InvoiceEdge, len(invoices))
	for i, invoice := range invoices {
		cursorTime := invoice.CreatedAt.AsTime()
		if invoice.PeriodStart != nil {
			cursorTime = invoice.PeriodStart.AsTime()
		}
		cursor := pagination.EncodeCursor(cursorTime, invoice.Id)
		edges[i] = &model.InvoiceEdge{
			Cursor: cursor,
			Node:   invoice,
		}
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: after != nil && *after != "",
		HasNextPage:     false,
	}
	if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
	}

	edgeNodes := make([]*pb.Invoice, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.InvoicesConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: len(invoices),
	}
}

// buildUsageRecordsConnection is a helper for demo mode
func (r *Resolver) buildUsageRecordsConnection(records []*pb.UsageRecord, first *int, after *string) *model.UsageRecordsConnection {
	limit := pagination.DefaultLimit
	if first != nil {
		limit = pagination.ClampLimit(*first)
	}

	if len(records) > limit {
		records = records[:limit]
	}

	edges := make([]*model.UsageRecordEdge, len(records))
	for i, record := range records {
		cursorTime := record.CreatedAt.AsTime()
		if record.PeriodStart != nil {
			cursorTime = record.PeriodStart.AsTime()
		}
		cursor := pagination.EncodeCursor(cursorTime, record.Id)
		edges[i] = &model.UsageRecordEdge{
			Cursor: cursor,
			Node:   record,
		}
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: after != nil && *after != "",
		HasNextPage:     false,
	}
	if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
	}

	edgeNodes := make([]*pb.UsageRecord, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.UsageRecordsConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: len(records),
	}
}

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
	copy(result, resp.Tiers)

	return result, nil
}

// DoGetInvoices returns tenant invoices
func (r *Resolver) DoGetInvoices(ctx context.Context) ([]*pb.Invoice, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic invoices")
		return demo.GenerateInvoices(), nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	r.Logger.WithField("tenant_id", tenantID).Info("Fetching invoices from Purser")

	resp, err := r.Clients.Purser.ListInvoices(ctx, tenantID, nil, nil)
	if err != nil {
		r.Logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to load invoices")
		return nil, fmt.Errorf("failed to load invoices: %w", err)
	}

	result := make([]*pb.Invoice, len(resp.Invoices))
	copy(result, resp.Invoices)

	return result, nil
}

// DoGetInvoice returns a specific invoice by ID
func (r *Resolver) DoGetInvoice(ctx context.Context, id string) (*pb.Invoice, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
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

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
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

// DoGetInvoicePreview returns the current draft invoice for the tenant (authoritative preview)
func (r *Resolver) DoGetInvoicePreview(ctx context.Context) (*pb.Invoice, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic invoice preview")
		return demo.GenerateInvoicePreview(), nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	status := "draft"
	resp, err := r.Clients.Purser.ListInvoices(ctx, tenantID, &status, &pb.CursorPaginationRequest{First: 1})
	if err != nil {
		r.Logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to load invoice preview")
		return nil, fmt.Errorf("failed to load invoice preview: %w", err)
	}

	if resp == nil || len(resp.Invoices) == 0 {
		return nil, nil
	}

	return resp.Invoices[0], nil
}

// DoGetLiveUsageSummary returns near-real-time usage summary for the current period.
func (r *Resolver) DoGetLiveUsageSummary(ctx context.Context, periodStart, periodEnd *time.Time) (*pb.LiveUsageSummary, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic live usage summary")
		return demo.GenerateLiveUsageSummary(), nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	now := time.Now()
	start := periodStart
	if start == nil {
		s := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		start = &s
	}
	end := periodEnd
	if end == nil || end.After(now) {
		end = &now
	}

	resp, err := r.Clients.Periscope.GetLiveUsageSummary(ctx, tenantID, &periscope.TimeRangeOpts{
		StartTime: *start,
		EndTime:   *end,
	})
	if err != nil {
		r.Logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to fetch live usage summary")
		return nil, fmt.Errorf("failed to fetch live usage summary: %w", err)
	}

	return resp.GetSummary(), nil
}

// DoGetTenantUsage returns full tenant usage with maps converted to arrays
func (r *Resolver) DoGetTenantUsage(ctx context.Context, timeRange *model.TimeRangeInput) (*model.TenantUsage, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic tenant usage")
		return demoTenantUsageFromInvoicePreview(demo.GenerateInvoicePreview())
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
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
		LineItems:     usage.GetLineItems(),
		BaseAmount:    usage.GetBaseAmount(),
		UsageAmount:   usage.GetUsageAmount(),
	}, nil
}

func demoTenantUsageFromInvoicePreview(preview *pb.Invoice) (*model.TenantUsage, error) {
	if preview == nil {
		return nil, fmt.Errorf("demo invoice preview missing")
	}

	billingPeriod := time.Now().Format("2006-01")
	if preview.PeriodStart != nil && preview.PeriodEnd != nil {
		billingPeriod = preview.PeriodStart.AsTime().Format("2006-01-02") + " to " + preview.PeriodEnd.AsTime().Format("2006-01-02")
	}

	lineItems := make([]*pb.LineItem, 0, len(preview.GetLineItems()))
	var usageEntries []*model.UsageEntry
	var costEntries []*model.CostEntry
	totalCost := 0.0
	for _, item := range preview.GetLineItems() {
		if item.GetLineKey() == "base_subscription" || item.GetMeter() == "" {
			continue
		}
		quantity, err := strconv.ParseFloat(item.GetQuantity(), 64)
		if err != nil {
			return nil, fmt.Errorf("demo line item %q quantity: %w", item.GetLineKey(), err)
		}
		total, err := strconv.ParseFloat(item.GetTotal(), 64)
		if err != nil {
			return nil, fmt.Errorf("demo line item %q total: %w", item.GetLineKey(), err)
		}
		lineItems = append(lineItems, item)
		usageEntries = append(usageEntries, &model.UsageEntry{ResourceType: item.GetMeter(), Amount: quantity})
		costEntries = append(costEntries, &model.CostEntry{ResourceType: item.GetMeter(), Cost: total})
		totalCost += total
	}

	return &model.TenantUsage{
		BillingPeriod: billingPeriod,
		Usage:         usageEntries,
		Costs:         costEntries,
		TotalCost:     totalCost,
		Currency:      preview.GetCurrency(),
		LineItems:     lineItems,
		BaseAmount:    strconv.FormatFloat(preview.GetBaseAmount(), 'f', 2, 64),
		UsageAmount:   strconv.FormatFloat(preview.GetMeteredAmount(), 'f', 2, 64),
	}, nil
}

// DoGetUsageRecords returns usage records for tenant
func (r *Resolver) DoGetUsageRecords(ctx context.Context, timeRange *model.TimeRangeInput) ([]*pb.UsageRecord, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic usage records")
		return demo.GenerateUsageRecords(), nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	r.Logger.WithField("tenant_id", tenantID).Info("Getting usage records")

	// Build time range for usage records
	tr := buildUsageTimeRange(timeRange, 30*24*time.Hour)

	resp, err := r.Clients.Purser.GetUsageRecords(ctx, tenantID, "", "", tr, &pb.CursorPaginationRequest{First: 500})
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get usage records")
		return nil, fmt.Errorf("failed to get usage records: %w", err)
	}

	return resp.UsageRecords, nil
}

// DoGetUsageAggregates returns rollup-backed aggregates for usage charts
func (r *Resolver) DoGetUsageAggregates(ctx context.Context, timeRange *model.TimeRangeInput, granularity string, usageTypes []string) ([]*pb.UsageAggregate, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic usage aggregates")
		records := demo.GenerateUsageRecords()
		return buildUsageAggregates(records, timeRange, granularity, usageTypes), nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	tr := buildUsageTimeRange(timeRange, 30*24*time.Hour)

	resp, err := r.Clients.Purser.GetUsageAggregates(ctx, tenantID, tr, granularity, usageTypes)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get usage aggregates")
		return nil, fmt.Errorf("failed to get usage aggregates: %w", err)
	}

	return resp.Aggregates, nil
}

func buildUsageTimeRange(timeRange *model.TimeRangeInput, defaultWindow time.Duration) *pb.TimeRange {
	if timeRange == nil {
		end := time.Now()
		start := end.Add(-defaultWindow)
		return &pb.TimeRange{
			Start: timestamppb.New(start),
			End:   timestamppb.New(end),
		}
	}
	return &pb.TimeRange{
		Start: timestamppb.New(timeRange.Start),
		End:   timestamppb.New(timeRange.End),
	}
}

func buildUsageAggregates(records []*pb.UsageRecord, timeRange *model.TimeRangeInput, granularity string, usageTypes []string) []*pb.UsageAggregate {
	type key struct {
		usageType string
		start     time.Time
	}

	usageTypeFilter := map[string]bool{}
	for _, t := range usageTypes {
		usageTypeFilter[t] = true
	}

	startTime := time.Time{}
	endTime := time.Time{}
	if timeRange != nil {
		startTime = timeRange.Start
		endTime = timeRange.End
	}

	buckets := map[key]*pb.UsageAggregate{}

	for _, record := range records {
		if record == nil {
			continue
		}
		if len(usageTypeFilter) > 0 && !usageTypeFilter[record.UsageType] {
			continue
		}

		ts := record.CreatedAt.AsTime()
		if record.PeriodStart != nil {
			ts = record.PeriodStart.AsTime()
		}

		if !startTime.IsZero() && ts.Before(startTime) {
			continue
		}
		if !endTime.IsZero() && ts.After(endTime) {
			continue
		}

		bucketStart, bucketEnd := bucketForGranularity(ts, granularity)
		k := key{usageType: record.UsageType, start: bucketStart}
		if _, ok := buckets[k]; !ok {
			buckets[k] = &pb.UsageAggregate{
				UsageType:   record.UsageType,
				UsageValue:  0,
				Granularity: granularity,
				PeriodStart: timestamppb.New(bucketStart),
				PeriodEnd:   timestamppb.New(bucketEnd),
			}
		}
		buckets[k].UsageValue += record.UsageValue
	}

	result := make([]*pb.UsageAggregate, 0, len(buckets))
	for _, agg := range buckets {
		result = append(result, agg)
	}
	sort.Slice(result, func(i, j int) bool {
		a := result[i].GetPeriodStart()
		b := result[j].GetPeriodStart()
		if a == nil || b == nil {
			return result[i].UsageType < result[j].UsageType
		}
		return a.AsTime().Before(b.AsTime())
	})

	return result
}

func bucketForGranularity(ts time.Time, granularity string) (time.Time, time.Time) {
	switch granularity {
	case "monthly":
		start := time.Date(ts.Year(), ts.Month(), 1, 0, 0, 0, 0, ts.Location())
		return start, start.AddDate(0, 1, 0)
	case "daily":
		start := time.Date(ts.Year(), ts.Month(), ts.Day(), 0, 0, 0, 0, ts.Location())
		return start, start.Add(24 * time.Hour)
	default:
		start := ts.Truncate(time.Hour)
		return start, start.Add(time.Hour)
	}
}

// DoCreatePayment processes a payment
func (r *Resolver) DoCreatePayment(ctx context.Context, input model.CreatePaymentInput) (*pb.PaymentResponse, error) {
	purserMethod, methodErr := purserPaymentMethod(input.Method)
	if methodErr != nil {
		return nil, methodErr
	}

	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic payment")
		amount, cur, ok := demoInvoicePaymentAmount(input.InvoiceID)
		if !ok {
			return nil, fmt.Errorf("invoice not found: %s", input.InvoiceID)
		}
		resp := &pb.PaymentResponse{
			Id:        "payment_demo_" + time.Now().Format("20060102150405"),
			Amount:    amount,
			Currency:  cur,
			Status:    "pending",
			Method:    purserMethod,
			CreatedAt: timestamppb.Now(),
			ExpiresAt: timestamppb.New(time.Now().Add(30 * time.Minute)),
		}
		switch purserMethod {
		case "crypto_usdc":
			resp.WalletAddress = "0x000000000000000000000000000000000000dEaD"
			baseUnits := demoCryptoBaseUnits(amount, "1", 6)
			resp.ExpectedAmountBaseUnits = baseUnits.String()
			resp.ExpectedAmountToken = demoTokenAmount(baseUnits, 6)
			resp.QuotedPriceUsd = "1.00"
			resp.QuoteSource = "demo"
			resp.AssetSymbol = "USDC"
			resp.Network = "arbitrum"
			resp.QuotedAt = resp.CreatedAt
		case "crypto_eth":
			resp.WalletAddress = "0x000000000000000000000000000000000000dEaD"
			baseUnits := demoCryptoBaseUnits(amount, "3000", 18)
			resp.ExpectedAmountBaseUnits = baseUnits.String()
			resp.ExpectedAmountToken = demoTokenAmount(baseUnits, 18)
			resp.QuotedPriceUsd = "3000.00"
			resp.QuoteSource = "demo"
			resp.AssetSymbol = "ETH"
			resp.Network = "arbitrum"
			resp.QuotedAt = resp.CreatedAt
		case "card":
			resp.PaymentUrl = "/account/billing?payment=demo"
		}
		return resp, nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	r.Logger.WithField("tenant_id", tenantID).
		WithField("method", input.Method).
		Info("Creating payment")

	returnURL := ""
	if input.ReturnURL != nil {
		returnURL = *input.ReturnURL
	}

	paymentReq := &pb.PaymentRequest{
		InvoiceId: input.InvoiceID,
		Method:    purserMethod,
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

	userID := userIDFromContext(ctx)
	provider := resp.Method
	if provider == "" {
		provider = purserMethod
	}
	r.sendServiceEvent(ctx, &pb.ServiceEvent{
		EventType:    apiEventPaymentCreated,
		ResourceType: "payment",
		ResourceId:   resp.Id,
		Payload: &pb.ServiceEvent_BillingEvent{
			BillingEvent: &pb.BillingEvent{
				TenantId:  tenantID,
				PaymentId: resp.Id,
				InvoiceId: input.InvoiceID,
				Amount:    resp.Amount,
				Currency:  resp.Currency,
				Provider:  provider,
				Status:    resp.Status,
			},
		},
		UserId: userID,
	})

	return resp, nil
}

func demoCryptoBaseUnits(amount float64, priceUSD string, tokenDecimals int32) *big.Int {
	price := new(big.Rat)
	if _, ok := price.SetString(priceUSD); !ok || price.Sign() <= 0 {
		return big.NewInt(0)
	}
	amountRat := new(big.Rat)
	if _, ok := amountRat.SetString(strconv.FormatFloat(amount, 'f', 2, 64)); !ok || amountRat.Sign() <= 0 {
		return big.NewInt(0)
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(tokenDecimals)), nil)
	baseUnits := new(big.Rat).Mul(new(big.Rat).Quo(amountRat, price), new(big.Rat).SetInt(scale))
	return ceilRat(baseUnits)
}

func demoTokenAmount(baseUnits *big.Int, tokenDecimals int32) string {
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(tokenDecimals)), nil)
	return new(big.Rat).SetFrac(baseUnits, scale).FloatString(int(tokenDecimals))
}

func ceilRat(v *big.Rat) *big.Int {
	quotient, remainder := new(big.Int).QuoRem(v.Num(), v.Denom(), new(big.Int))
	if remainder.Sign() > 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	return quotient
}

func purserPaymentMethod(method model.PaymentMethod) (string, error) {
	switch strings.ToUpper(string(method)) {
	case "CARD":
		return "card", nil
	case "CRYPTO_ETH":
		return "crypto_eth", nil
	case "CRYPTO_USDC":
		return "crypto_usdc", nil
	case "BANK_TRANSFER":
		return "bank_transfer", nil
	default:
		return "", fmt.Errorf("unsupported payment method %q", method)
	}
}

func demoInvoicePaymentAmount(invoiceID string) (float64, string, bool) {
	for _, inv := range demo.GenerateInvoices() {
		if inv.GetId() == invoiceID {
			currency := inv.GetCurrency()
			if currency == "" {
				currency = billing.DefaultCurrency()
			}
			return inv.GetAmount(), currency, true
		}
	}
	return 0, "", false
}

// DoSubmitX402Payment settles an x402 payment payload and credits the billable tenant.
func (r *Resolver) DoSubmitX402Payment(ctx context.Context, payment string, resource *string) (model.SubmitX402PaymentResult, error) {
	payment = strings.TrimSpace(payment)
	if payment == "" {
		return &model.ValidationError{Message: "payment is required"}, nil
	}
	if r.Clients == nil || r.Clients.Purser == nil {
		return &model.ValidationError{Message: "x402 settlement unavailable"}, nil
	}

	authTenantID := ctxkeys.GetTenantID(ctx)
	resourceValue := ""
	if resource != nil {
		resourceValue = strings.TrimSpace(*resource)
	}

	clientIP := ""
	if ginCtx, ok := ctx.Value(ctxkeys.KeyGinContext).(*gin.Context); ok && ginCtx != nil {
		clientIP = ginCtx.ClientIP()
	}

	settleResult, settleErr := x402.SettleX402Payment(ctx, x402.SettlementOptions{
		PaymentHeader:          payment,
		Resource:               resourceValue,
		AuthTenantID:           authTenantID,
		ClientIP:               clientIP,
		Purser:                 r.Clients.Purser,
		Commodore:              r.Clients.Commodore,
		AllowUnresolvedCreator: false,
		Logger:                 r.Logger,
	})
	if settleErr != nil {
		switch settleErr.Code {
		case x402.ErrAuthRequired, x402.ErrTargetMismatch:
			return &model.AuthError{Message: settleErr.Message}, nil
		case x402.ErrResourceNotFound:
			resourceType := "Resource"
			if settleErr.ResourceType != "" {
				resourceType = settleErr.ResourceType
			}
			resourceID := ""
			if settleErr.ResourceID != "" {
				resourceID = settleErr.ResourceID
			}
			return &model.NotFoundError{
				Message:      settleErr.Message,
				Code:         strPtr("NOT_FOUND"),
				ResourceType: resourceType,
				ResourceID:   resourceID,
			}, nil
		case x402.ErrBillingDetailsRequired:
			return &model.ValidationError{Message: settleErr.Message, Code: strPtr("BILLING_DETAILS_REQUIRED")}, nil
		default:
			return &model.ValidationError{Message: settleErr.Message}, nil
		}
	}
	if settleResult == nil || settleResult.Settle == nil || !settleResult.Settle.Success {
		return &model.ValidationError{Message: "payment settlement failed"}, nil
	}

	credited := int(settleResult.Settle.CreditedCents)
	newBalance := int(settleResult.Settle.NewBalanceCents)
	txHash := strings.TrimSpace(settleResult.Settle.TxHash)
	var txHashPtr *string
	if txHash != "" {
		txHashPtr = &txHash
	}

	return &model.X402PaymentResult{
		Success:         true,
		IsAuthOnly:      false,
		TenantID:        settleResult.TargetTenantID,
		WalletAddress:   settleResult.PayerAddress,
		CreditedCents:   credited,
		NewBalanceCents: &newBalance,
		TxHash:          txHashPtr,
		Message:         fmt.Sprintf("Payment successful! %d cents credited to tenant %s.", settleResult.Settle.CreditedCents, settleResult.TargetTenantID),
	}, nil
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

	// Pricing rule overrides — pass-through to Purser. The header update and
	// override writes commit in a single subscription-update transaction.
	if input.PricingOverrides != nil {
		if len(input.PricingOverrides) == 0 {
			req.ClearPricingOverrides = true
		} else {
			rules := make([]*pb.PricingRule, 0, len(input.PricingOverrides))
			for _, r := range input.PricingOverrides {
				rule := &pb.PricingRule{
					Meter:            r.Meter,
					Model:            r.Model,
					Currency:         r.Currency,
					IncludedQuantity: r.IncludedQuantity,
					UnitPrice:        r.UnitPrice,
				}
				if r.ConfigJSON != nil {
					rule.ConfigJson = *r.ConfigJSON
				}
				rules = append(rules, rule)
			}
			req.PricingOverrides = rules
		}
	}

	if input.EntitlementOverrides != nil {
		if len(input.EntitlementOverrides) == 0 {
			req.ClearEntitlementOverrides = true
		} else {
			ents := make(map[string]string, len(input.EntitlementOverrides))
			for _, e := range input.EntitlementOverrides {
				ents[e.Key] = e.Value
			}
			req.EntitlementOverrides = ents
		}
	}

	// Call Purser to update the subscription
	subscription, err := r.Clients.Purser.UpdateSubscription(ctx, req)
	if err != nil {
		r.Logger.WithError(err).
			WithField("tenant_id", tenantID).
			Error("Failed to update subscription custom terms")
		return nil, fmt.Errorf("failed to update subscription: %w", err)
	}

	userID := userIDFromContext(ctx)
	r.sendServiceEvent(ctx, &pb.ServiceEvent{
		EventType:    apiEventSubscriptionUpdated,
		ResourceType: "subscription",
		ResourceId:   subscription.Id,
		TenantId:     tenantID,
		Payload: &pb.ServiceEvent_BillingEvent{
			BillingEvent: &pb.BillingEvent{
				TenantId:       tenantID,
				SubscriptionId: subscription.Id,
				Status:         subscription.Status,
			},
		},
		UserId: userID,
	})

	return subscription, nil
}

// ============================================================================
// PREPAID BALANCE RESOLVERS
// ============================================================================

// DoGetPrepaidBalance returns the current prepaid balance for the tenant
func (r *Resolver) DoGetPrepaidBalance(ctx context.Context, currency *string) (*model.PrepaidBalance, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic prepaid balance")
		return &model.PrepaidBalance{
			ID:                       "demo-balance-001",
			TenantID:                 "demo-tenant",
			BalanceCents:             4523,
			Currency:                 billing.DefaultCurrency(),
			LowBalanceThresholdCents: 500,
			IsLowBalance:             false,
			DrainRateCentsPerHour:    12,
			CreatedAt:                time.Now().Add(-30 * 24 * time.Hour),
			UpdatedAt:                time.Now().Add(-1 * time.Hour),
		}, nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}

	curr := billing.DefaultCurrency()
	if currency != nil && *currency != "" {
		curr = *currency
	}

	resp, err := r.Clients.Purser.GetPrepaidBalance(ctx, tenantID, curr)
	if err != nil {
		// NotFound is expected for tenants without prepaid balance - return nil, not an error
		if status.Code(err) == codes.NotFound {
			return nil, nil
		}
		r.Logger.WithError(err).Error("Failed to get prepaid balance")
		return nil, err
	}

	return &model.PrepaidBalance{
		ID:                       resp.Id,
		TenantID:                 resp.TenantId,
		BalanceCents:             int(resp.BalanceCents),
		Currency:                 resp.Currency,
		LowBalanceThresholdCents: int(resp.LowBalanceThresholdCents),
		IsLowBalance:             resp.IsLowBalance,
		DrainRateCentsPerHour:    int(resp.DrainRateCentsPerHour),
		CreatedAt:                resp.CreatedAt.AsTime(),
		UpdatedAt:                resp.UpdatedAt.AsTime(),
	}, nil
}

// DoGetBalanceTransactionsConnection returns paginated balance transactions for the tenant
func (r *Resolver) DoGetBalanceTransactionsConnection(ctx context.Context, page *model.ConnectionInput, transactionType *string, timeRange *model.TimeRangeInput) (*model.BalanceTransactionsConnection, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic balance transactions")
		now := time.Now()
		desc1 := "Crypto top-up: 0.05 ETH"
		desc2 := "Usage: 2.3 viewer-hours @ $0.01/hr"
		return &model.BalanceTransactionsConnection{
			Edges: []*model.BalanceTransactionEdge{
				{Cursor: "tx-001", Node: &model.BalanceTransaction{
					ID: "tx-001", TenantID: "demo-tenant", AmountCents: 5000, BalanceAfterCents: 4523,
					TransactionType: "topup", Description: &desc1, CreatedAt: now.Add(-24 * time.Hour),
				}},
				{Cursor: "tx-002", Node: &model.BalanceTransaction{
					ID: "tx-002", TenantID: "demo-tenant", AmountCents: -23, BalanceAfterCents: 4523,
					TransactionType: "usage", Description: &desc2, CreatedAt: now.Add(-1 * time.Hour),
				}},
			},
			Nodes: []*model.BalanceTransaction{},
			PageInfo: &model.PageInfo{
				HasNextPage:     false,
				HasPreviousPage: false,
			},
			TotalCount: 2,
		}, nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}

	// Convert time range
	var pbTimeRange *pb.TimeRange
	if timeRange != nil {
		pbTimeRange = &pb.TimeRange{
			Start: timestamppb.New(timeRange.Start),
			End:   timestamppb.New(timeRange.End),
		}
	}

	// Build bidirectional pagination request
	var first, last *int
	var after, before *string
	if page != nil {
		first, after, last, before = page.First, page.After, page.Last, page.Before
	}
	paginationReq := buildBillingPaginationRequest(first, after, last, before)

	resp, err := r.Clients.Purser.ListBalanceTransactions(ctx, tenantID, transactionType, pbTimeRange, paginationReq)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to list balance transactions")
		return nil, err
	}

	// Convert to GraphQL types
	edges := make([]*model.BalanceTransactionEdge, 0, len(resp.Transactions))
	nodes := make([]*model.BalanceTransaction, 0, len(resp.Transactions))

	for _, tx := range resp.Transactions {
		node := &model.BalanceTransaction{
			ID:                tx.Id,
			TenantID:          tx.TenantId,
			AmountCents:       int(tx.AmountCents),
			BalanceAfterCents: int(tx.BalanceAfterCents),
			TransactionType:   tx.TransactionType,
			CreatedAt:         tx.CreatedAt.AsTime(),
		}
		if tx.Description != "" {
			node.Description = &tx.Description
		}
		if tx.ReferenceId != nil {
			node.ReferenceID = tx.ReferenceId
		}
		if tx.ReferenceType != nil {
			node.ReferenceType = tx.ReferenceType
		}

		edges = append(edges, &model.BalanceTransactionEdge{
			Cursor: tx.Id,
			Node:   node,
		})
		nodes = append(nodes, node)
	}

	// Build page info from backend response
	pag := resp.GetPagination()
	pageInfo := &model.PageInfo{
		HasPreviousPage: pag.GetHasPreviousPage(),
		HasNextPage:     pag.GetHasNextPage(),
	}
	if pag.GetStartCursor() != "" {
		sc := pag.GetStartCursor()
		pageInfo.StartCursor = &sc
	}
	if pag.GetEndCursor() != "" {
		ec := pag.GetEndCursor()
		pageInfo.EndCursor = &ec
	}

	return &model.BalanceTransactionsConnection{
		Edges:      edges,
		Nodes:      nodes,
		PageInfo:   pageInfo,
		TotalCount: int(pag.GetTotalCount()),
	}, nil
}

// NOTE: DoAdjustPrepaidBalance is NOT exposed in GraphQL.
// Admin balance adjustments go through CLI → direct gRPC to Purser.
// The gRPC AdjustBalance method exists in Purser for CLI use.

// ============================================================================
// STRIPE CHECKOUT OPERATIONS
// ============================================================================

// DoCreateStripeCheckout creates a Stripe Checkout Session for subscription setup
func (r *Resolver) DoCreateStripeCheckout(ctx context.Context, tierID, billingPeriod, successURL, cancelURL string) (model.StripeCheckoutResult, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic Stripe checkout")
		return &model.StripeCheckoutSession{
			SessionID:   "cs_demo_" + time.Now().Format("20060102150405"),
			CheckoutURL: "https://checkout.stripe.com/demo",
		}, nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return &model.AuthError{Message: "Authentication required"}, nil
	}

	r.Logger.WithField("tenant_id", tenantID).WithField("tier_id", tierID).Info("Creating Stripe checkout session")

	resp, err := r.Clients.Purser.CreateStripeCheckoutSession(ctx, tenantID, tierID, billingPeriod, successURL, cancelURL)
	if err != nil {
		r.Logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to create Stripe checkout")
		return &model.ValidationError{Message: "Failed to create checkout session: " + err.Error()}, nil
	}

	return &model.StripeCheckoutSession{
		SessionID:   resp.SessionId,
		CheckoutURL: resp.CheckoutUrl,
	}, nil
}

// DoCreateStripeBillingPortal creates a Stripe Billing Portal session
func (r *Resolver) DoCreateStripeBillingPortal(ctx context.Context, returnURL string) (model.StripeBillingPortalResult, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic Stripe billing portal")
		return &model.StripeBillingPortalSession{
			PortalURL: "https://billing.stripe.com/demo",
		}, nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return &model.AuthError{Message: "Authentication required"}, nil
	}

	r.Logger.WithField("tenant_id", tenantID).Info("Creating Stripe billing portal session")

	resp, err := r.Clients.Purser.CreateStripeBillingPortal(ctx, tenantID, returnURL)
	if err != nil {
		r.Logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to create Stripe billing portal")
		return &model.ValidationError{Message: "Failed to create billing portal: " + err.Error()}, nil
	}

	return &model.StripeBillingPortalSession{
		PortalURL: resp.PortalUrl,
	}, nil
}

// ============================================================================
// MOLLIE CHECKOUT OPERATIONS
// ============================================================================

// DoCreateMollieFirstPayment creates a Mollie first payment to establish a mandate
func (r *Resolver) DoCreateMollieFirstPayment(ctx context.Context, tierID, method, redirectURL string) (model.MollieFirstPaymentResult, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic Mollie first payment")
		ts := time.Now().Format("20060102150405")
		return &model.MollieFirstPayment{
			PaymentID:  "tr_demo" + ts[:8],
			CustomerID: "cst_demo" + ts[:8],
			PaymentURL: "https://www.mollie.com/demo/checkout",
		}, nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return &model.AuthError{Message: "Authentication required"}, nil
	}

	r.Logger.WithField("tenant_id", tenantID).WithField("tier_id", tierID).WithField("method", method).Info("Creating Mollie first payment")

	resp, err := r.Clients.Purser.CreateMollieFirstPayment(ctx, tenantID, tierID, method, redirectURL)
	if err != nil {
		r.Logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to create Mollie first payment")
		return &model.ValidationError{Message: "Failed to create payment: " + err.Error()}, nil
	}

	userID := userIDFromContext(ctx)
	r.sendServiceEvent(ctx, &pb.ServiceEvent{
		EventType:    apiEventPaymentCreated,
		ResourceType: "payment",
		ResourceId:   resp.PaymentId,
		Payload: &pb.ServiceEvent_BillingEvent{
			BillingEvent: &pb.BillingEvent{
				TenantId:  tenantID,
				PaymentId: resp.PaymentId,
				Provider:  "mollie",
			},
		},
		UserId: userID,
	})

	return &model.MollieFirstPayment{
		PaymentID:  resp.PaymentId,
		CustomerID: resp.MollieCustomerId,
		PaymentURL: resp.PaymentUrl,
	}, nil
}

// DoCreateMollieSubscription creates a Mollie subscription after mandate is valid
func (r *Resolver) DoCreateMollieSubscription(ctx context.Context, tierID, mandateID string, description *string) (model.MollieSubscriptionResult, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic Mollie subscription")
		ts := time.Now().Format("20060102150405")
		return &model.MollieSubscription{
			SubscriptionID:  "sub_demo" + ts[:8],
			Status:          "active",
			NextPaymentDate: nil,
		}, nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return &model.AuthError{Message: "Authentication required"}, nil
	}

	desc := ""
	if description != nil {
		desc = *description
	}

	r.Logger.WithField("tenant_id", tenantID).WithField("tier_id", tierID).WithField("mandate_id", mandateID).Info("Creating Mollie subscription")

	resp, err := r.Clients.Purser.CreateMollieSubscription(ctx, tenantID, tierID, mandateID, desc)
	if err != nil {
		r.Logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to create Mollie subscription")
		return &model.ValidationError{Message: "Failed to create subscription: " + err.Error()}, nil
	}

	userID := userIDFromContext(ctx)
	r.sendServiceEvent(ctx, &pb.ServiceEvent{
		EventType:    apiEventSubscriptionCreated,
		ResourceType: "subscription",
		ResourceId:   resp.SubscriptionId,
		Payload: &pb.ServiceEvent_BillingEvent{
			BillingEvent: &pb.BillingEvent{
				TenantId:       tenantID,
				SubscriptionId: resp.SubscriptionId,
				Provider:       "mollie",
				Status:         resp.Status,
			},
		},
		UserId: userID,
	})

	var nextPaymentDate *string
	if resp.NextPaymentDate != "" {
		nextPaymentDate = &resp.NextPaymentDate
	}

	return &model.MollieSubscription{
		SubscriptionID:  resp.SubscriptionId,
		Status:          resp.Status,
		NextPaymentDate: nextPaymentDate,
	}, nil
}

// DoListMollieMandates lists Mollie mandates for the current tenant
func (r *Resolver) DoListMollieMandates(ctx context.Context) ([]*pb.MollieMandate, error) {
	if middleware.IsDemoMode(ctx) {
		ts := time.Now().AddDate(0, -1, 0)
		details := map[string]interface{}{
			"consumer_name":    "Demo User",
			"consumer_account": "NL00DEMO0000000000",
		}
		structDetails, _ := structpb.NewStruct(details)
		return []*pb.MollieMandate{
			{
				MollieMandateId:  "mdt_demo_123",
				MollieCustomerId: "cst_demo_123",
				Status:           "valid",
				Method:           "directdebit",
				Details:          structDetails,
				CreatedAt:        timestamppb.New(ts),
			},
		}, nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("authentication required")
	}

	resp, err := r.Clients.Purser.ListMollieMandates(ctx, tenantID)
	if err != nil {
		r.Logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to list Mollie mandates")
		return nil, err
	}

	if resp != nil && resp.Mandates != nil {
		return resp.Mandates, nil
	}

	return []*pb.MollieMandate{}, nil
}

// ============================================================================
// CARD TOP-UP OPERATIONS (PREPAID)
// ============================================================================

// DoCreateCardTopup creates a card-based top-up checkout session for prepaid balance
func (r *Resolver) DoCreateCardTopup(ctx context.Context, input model.CreateCardTopupInput) (*model.CardTopupResult, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic card top-up")
		return &model.CardTopupResult{
			TopupID:     "topup_demo_" + time.Now().Format("20060102150405"),
			CheckoutURL: "https://checkout.stripe.com/demo-topup",
			ExpiresAt:   time.Now().Add(30 * time.Minute),
		}, nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("authentication required")
	}

	// Map GraphQL provider enum to proto
	var provider string
	switch input.Provider {
	case model.CardPaymentProviderStripe:
		provider = "stripe"
	case model.CardPaymentProviderMollie:
		provider = "mollie"
	default:
		return nil, fmt.Errorf("unsupported payment provider: %s", input.Provider)
	}

	currency := billing.DefaultCurrency()
	if input.Currency != nil && *input.Currency != "" {
		currency = *input.Currency
	}

	r.Logger.WithField("tenant_id", tenantID).
		WithField("amount_cents", input.AmountCents).
		WithField("provider", provider).
		Info("Creating card top-up checkout")

	// Build the gRPC request
	req := &pb.CreateCardTopupRequest{
		TenantId:    tenantID,
		AmountCents: int64(input.AmountCents),
		Currency:    currency,
		Provider:    provider,
		SuccessUrl:  input.SuccessURL,
		CancelUrl:   input.CancelURL,
	}

	// Optional billing details - proto uses optional string (pointers)
	req.BillingEmail = input.BillingEmail
	req.BillingName = input.BillingName
	req.BillingCompany = input.BillingCompany
	req.BillingVatNumber = input.BillingVatNumber

	resp, err := r.Clients.Purser.CreateCardTopup(ctx, req)
	if err != nil {
		r.Logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to create card top-up")
		return nil, fmt.Errorf("failed to create top-up: %w", err)
	}

	userID := userIDFromContext(ctx)
	amount := float64(input.AmountCents) / 100.0
	r.sendServiceEvent(ctx, &pb.ServiceEvent{
		EventType:    apiEventTopupCreated,
		ResourceType: "topup",
		ResourceId:   resp.TopupId,
		Payload: &pb.ServiceEvent_BillingEvent{
			BillingEvent: &pb.BillingEvent{
				TenantId: tenantID,
				TopupId:  resp.TopupId,
				Amount:   amount,
				Currency: currency,
				Provider: provider,
				Status:   "pending",
			},
		},
		UserId: userID,
	})

	return &model.CardTopupResult{
		TopupID:     resp.TopupId,
		CheckoutURL: resp.CheckoutUrl,
		ExpiresAt:   resp.ExpiresAt.AsTime(),
	}, nil
}

// ============================================================================
// CRYPTO TOP-UP OPERATIONS (PREPAID - Agent Payment Method)
// ============================================================================

// demoCryptoTopup builds a per-asset synthetic crypto top-up for demo mode so
// the webapp/MCP path renders end-to-end without backend RPC.
func demoCryptoTopup(input model.CreateCryptoTopupInput) *model.CryptoTopupResult {
	now := time.Now()
	asset := input.Asset
	if asset == pb.CryptoAsset_CRYPTO_ASSET_UNSPECIFIED {
		asset = pb.CryptoAsset_CRYPTO_ASSET_ETH
	}
	var symbol, priceUSD, baseUnits, tokenAmt string
	switch asset {
	case pb.CryptoAsset_CRYPTO_ASSET_ETH:
		symbol, priceUSD = "ETH", "3300.00"
		// $cents/100 / 3300 ETH × 1e18 wei
		token := float64(input.AmountCents) / 100.0 / 3300.0
		tokenAmt = fmt.Sprintf("%.18f", token)
		baseUnits = fmt.Sprintf("%.0f", token*1e18)
	default:
		symbol, priceUSD = "USDC", "1.00"
		// USDC has 6 decimals; cents × 1e4 = base units
		token := float64(input.AmountCents) / 100.0
		tokenAmt = fmt.Sprintf("%.6f", token)
		baseUnits = fmt.Sprintf("%d", int64(input.AmountCents)*10_000)
	}
	return &model.CryptoTopupResult{
		TopupID:                 "topup_demo_" + now.Format("20060102150405"),
		DepositAddress:          "0x742d35cc6634c0532925a3b844bc9e7595f8ab00",
		Asset:                   asset,
		AssetSymbol:             symbol,
		ExpectedAmountCents:     input.AmountCents,
		ExpiresAt:               now.Add(24 * time.Hour),
		ExpectedAmountBaseUnits: baseUnits,
		ExpectedAmountToken:     tokenAmt,
		QuotedPriceUsd:          priceUSD,
		QuoteSource:             "chainlink",
		QuotedAt:                now,
		Network:                 "arbitrum",
	}
}

// DoCreateCryptoTopup creates a crypto deposit address for prepaid balance top-up
func (r *Resolver) DoCreateCryptoTopup(ctx context.Context, input model.CreateCryptoTopupInput) (*model.CryptoTopupResult, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic crypto top-up")
		return demoCryptoTopup(input), nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("authentication required")
	}

	// Validate asset (already a proto enum from gqlgen)
	protoAsset := input.Asset
	if protoAsset == pb.CryptoAsset_CRYPTO_ASSET_UNSPECIFIED {
		return nil, fmt.Errorf("unsupported crypto asset: %s", input.Asset)
	}

	currency := billing.DefaultCurrency()
	if input.Currency != nil && *input.Currency != "" {
		currency = *input.Currency
	}

	r.Logger.WithField("tenant_id", tenantID).
		WithField("amount_cents", input.AmountCents).
		WithField("asset", input.Asset).
		Info("Creating crypto top-up deposit address")

	req := &pb.CreateCryptoTopupRequest{
		TenantId:            tenantID,
		ExpectedAmountCents: int64(input.AmountCents),
		Asset:               protoAsset,
		Currency:            currency,
	}

	resp, err := r.Clients.Purser.CreateCryptoTopup(ctx, req)
	if err != nil {
		r.Logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to create crypto top-up")
		return nil, fmt.Errorf("failed to create crypto top-up: %w", err)
	}

	userID := userIDFromContext(ctx)
	amount := float64(input.AmountCents) / 100.0
	provider := "crypto"
	if resp.AssetSymbol != "" {
		provider = "crypto_" + strings.ToLower(resp.AssetSymbol)
	}
	r.sendServiceEvent(ctx, &pb.ServiceEvent{
		EventType:    apiEventTopupCreated,
		ResourceType: "topup",
		ResourceId:   resp.TopupId,
		Payload: &pb.ServiceEvent_BillingEvent{
			BillingEvent: &pb.BillingEvent{
				TenantId: tenantID,
				TopupId:  resp.TopupId,
				Amount:   amount,
				Currency: currency,
				Provider: provider,
				Status:   "pending",
				Asset:    resp.AssetSymbol,
				Network:  resp.Network,
			},
		},
		UserId: userID,
	})

	result := &model.CryptoTopupResult{
		TopupID:                 resp.TopupId,
		DepositAddress:          resp.DepositAddress,
		Asset:                   resp.Asset, // proto enum is used directly by gqlgen
		AssetSymbol:             resp.AssetSymbol,
		ExpectedAmountCents:     int(resp.ExpectedAmountCents),
		ExpiresAt:               resp.ExpiresAt.AsTime(),
		ExpectedAmountBaseUnits: resp.ExpectedAmountBaseUnits,
		ExpectedAmountToken:     resp.ExpectedAmountToken,
		QuotedPriceUsd:          resp.QuotedPriceUsd,
		QuoteSource:             resp.QuoteSource,
		Network:                 resp.Network,
	}
	if resp.QuotedAt != nil {
		result.QuotedAt = resp.QuotedAt.AsTime()
	}
	return result, nil
}

// DoGetCryptoTopupStatus returns the status of a crypto top-up for polling
func (r *Resolver) DoGetCryptoTopupStatus(ctx context.Context, topupID string) (*model.CryptoTopupStatus, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic crypto top-up status")
		expiresAt := time.Now().Add(23 * time.Hour)
		return &model.CryptoTopupStatus{
			ID:             topupID,
			DepositAddress: "0x742d35cc6634c0532925a3b844bc9e7595f8ab00",
			Asset:          pb.CryptoAsset_CRYPTO_ASSET_ETH,
			Status:         "pending",
			Confirmations:  0,
			ExpiresAt:      expiresAt,
		}, nil
	}

	resp, err := r.Clients.Purser.GetCryptoTopup(ctx, topupID)
	if err != nil {
		r.Logger.WithError(err).WithField("topup_id", topupID).Error("Failed to get crypto top-up status")
		return nil, fmt.Errorf("failed to get crypto top-up status: %w", err)
	}

	result := &model.CryptoTopupStatus{
		ID:             resp.Id,
		DepositAddress: resp.DepositAddress,
		Asset:          resp.Asset, // proto enum is used directly by gqlgen
		Status:         resp.Status,
		Confirmations:  int(resp.Confirmations),
		ExpiresAt:      resp.ExpiresAt.AsTime(),
	}

	if resp.TxHash != "" {
		result.TxHash = &resp.TxHash
	}
	if resp.ReceivedAmountBaseUnits != "" {
		base := resp.ReceivedAmountBaseUnits
		result.ReceivedAmountBaseUnits = &base
	}
	if resp.ReceivedAmountToken != "" {
		token := resp.ReceivedAmountToken
		result.ReceivedAmountToken = &token
	}
	if resp.CreditedAmountCents > 0 {
		cents := int(resp.CreditedAmountCents)
		result.CreditedAmountCents = &cents
	}
	if resp.CreditedAmountCurrency != "" {
		cc := resp.CreditedAmountCurrency
		result.CreditedAmountCurrency = &cc
	}
	if resp.QuoteSource != "" {
		qs := resp.QuoteSource
		result.QuoteSource = &qs
	}
	if resp.Network != "" {
		nw := resp.Network
		result.Network = &nw
	}
	if resp.DetectedAt != nil {
		t := resp.DetectedAt.AsTime()
		result.DetectedAt = &t
	}
	if resp.CompletedAt != nil {
		t := resp.CompletedAt.AsTime()
		result.CompletedAt = &t
	}

	return result, nil
}

// ============================================================================
// PROMOTION FLOW
// ============================================================================

// DoPromoteToPaid upgrades a wallet-only prepaid account to postpaid billing
func (r *Resolver) DoPromoteToPaid(ctx context.Context, tierID string) (model.PromoteToPaidResult, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic promotion result")
		return &model.PromoteToPaidPayload{
			Success:            true,
			Message:            "Upgraded to postpaid billing (demo mode)",
			NewBillingModel:    "postpaid",
			CreditBalanceCents: 1000, // $10 demo credit
		}, nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return &model.ValidationError{
			Message: "Tenant context required",
			Code:    ptrStr("TENANT_REQUIRED"),
			Field:   ptrStr("tenant_id"),
		}, nil
	}

	r.Logger.WithField("tenant_id", tenantID).WithField("tier_id", tierID).Info("Processing promotion to postpaid")

	resp, err := r.Clients.Purser.PromoteToPaid(ctx, tenantID, tierID)
	if err != nil {
		r.Logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to promote to postpaid")
		return &model.ValidationError{
			Message: err.Error(),
			Code:    ptrStr("PROMOTION_FAILED"),
			Field:   ptrStr("tier_id"),
		}, nil
	}

	return &model.PromoteToPaidPayload{
		Success:            resp.Success,
		Message:            resp.Message,
		NewBillingModel:    resp.NewBillingModel,
		CreditBalanceCents: int(resp.CreditBalanceCents),
		SubscriptionID:     resp.SubscriptionId,
	}, nil
}

// ============================================================================
// BILLING DETAILS RESOLVERS
// ============================================================================

// DoGetBillingDetails returns billing details for the current tenant
func (r *Resolver) DoGetBillingDetails(ctx context.Context) (*pb.BillingDetails, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic billing details")
		now := time.Now()
		return &pb.BillingDetails{
			TenantId:  "demo-tenant",
			Email:     "billing@example.com",
			Company:   "Demo Company Inc.",
			VatNumber: "DE123456789",
			Address: &pb.BillingAddress{
				Street:     "123 Demo Street",
				City:       "Berlin",
				State:      "",
				PostalCode: "10115",
				Country:    "DE",
			},
			IsComplete: true,
			UpdatedAt:  timestamppb.New(now),
		}, nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}

	resp, err := r.Clients.Purser.GetBillingDetails(ctx, tenantID)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get billing details")
		return nil, err
	}

	return resp, nil
}

// DoUpdateBillingDetails updates billing details for the current tenant
func (r *Resolver) DoUpdateBillingDetails(ctx context.Context, input model.UpdateBillingDetailsInput) (*pb.BillingDetails, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic billing details after update")
		now := time.Now()
		details := &pb.BillingDetails{
			TenantId:   "demo-tenant",
			IsComplete: false,
			UpdatedAt:  timestamppb.New(now),
		}
		if input.Email != nil {
			details.Email = *input.Email
		}
		if input.Company != nil {
			details.Company = *input.Company
		}
		if input.VatNumber != nil {
			details.VatNumber = *input.VatNumber
		}
		if input.Address != nil {
			details.Address = &pb.BillingAddress{
				Street:     input.Address.Street,
				City:       input.Address.City,
				PostalCode: input.Address.PostalCode,
				Country:    input.Address.Country,
			}
			if input.Address.State != nil {
				details.Address.State = *input.Address.State
			}
		}
		// Check completeness
		details.IsComplete = details.Email != "" && details.Address != nil &&
			details.Address.Street != "" && details.Address.City != "" &&
			details.Address.PostalCode != "" && details.Address.Country != ""
		return details, nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}

	// Build proto request
	req := &pb.UpdateBillingDetailsRequest{
		TenantId: tenantID,
	}
	if input.Email != nil {
		req.Email = input.Email
	}
	if input.Company != nil {
		req.Company = input.Company
	}
	if input.VatNumber != nil {
		req.VatNumber = input.VatNumber
	}
	if input.Address != nil {
		req.Address = &pb.BillingAddress{
			Street:     input.Address.Street,
			City:       input.Address.City,
			PostalCode: input.Address.PostalCode,
			Country:    input.Address.Country,
		}
		if input.Address.State != nil {
			req.Address.State = *input.Address.State
		}
	}

	resp, err := r.Clients.Purser.UpdateBillingDetails(ctx, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to update billing details")
		return nil, err
	}

	r.sendServiceEvent(ctx, &pb.ServiceEvent{
		EventType:    apiEventBillingDetailsUpdated,
		ResourceType: "billing_details",
		ResourceId:   tenantID,
		Payload: &pb.ServiceEvent_TenantEvent{
			TenantEvent: &pb.TenantEvent{
				TenantId:      tenantID,
				ChangedFields: []string{"billing_details"},
			},
		},
	})

	r.Logger.WithField("tenant_id", tenantID).Info("Billing details updated")
	return resp, nil
}

// ptrStr returns a pointer to the given string (local helper to avoid import conflicts)
func ptrStr(s string) *string {
	return &s
}
