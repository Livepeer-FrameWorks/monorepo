package grpc

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"frameworks/api_billing/internal/handlers"
	"frameworks/api_billing/internal/mollie"
	"frameworks/api_billing/internal/stripe"
	"frameworks/pkg/billing"
	decklogclient "frameworks/pkg/clients/decklog"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/countries"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/grpcutil"
	"frameworks/pkg/logging"

	"frameworks/pkg/middleware"
	"frameworks/pkg/pagination"
	pb "frameworks/pkg/proto"
	mollielib "github.com/VictorAvelar/mollie-api-go/v4/mollie"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// scanAllocationDetails scans a JSONB column into AllocationDetails proto
func scanAllocationDetails(data []byte) *pb.AllocationDetails {
	if len(data) == 0 {
		return nil
	}
	var raw struct {
		Limit     *float64 `json:"limit"`
		UnitPrice float64  `json:"unit_price"`
		Unit      string   `json:"unit"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	ad := &pb.AllocationDetails{
		UnitPrice: raw.UnitPrice,
		Unit:      raw.Unit,
	}
	if raw.Limit != nil {
		ad.Limit = raw.Limit
	}
	return ad
}

// scanBillingFeatures scans a JSONB column into BillingFeatures proto
func scanBillingFeatures(data []byte) *pb.BillingFeatures {
	if len(data) == 0 {
		return nil
	}
	var raw struct {
		Recording      bool   `json:"recording"`
		Analytics      bool   `json:"analytics"`
		CustomBranding bool   `json:"custom_branding"`
		APIAccess      bool   `json:"api_access"`
		SupportLevel   string `json:"support_level"`
		SLA            bool   `json:"sla"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	return &pb.BillingFeatures{
		Recording:      raw.Recording,
		Analytics:      raw.Analytics,
		CustomBranding: raw.CustomBranding,
		ApiAccess:      raw.APIAccess,
		SupportLevel:   raw.SupportLevel,
		Sla:            raw.SLA,
	}
}

// marshalCustomPricing converts CustomPricing proto to JSONB bytes
func marshalCustomPricing(cp *pb.CustomPricing) ([]byte, error) {
	if cp == nil {
		return []byte("{}"), nil
	}
	raw := struct {
		BasePrice    float64 `json:"base_price"`
		DiscountRate float64 `json:"discount_rate"`
	}{
		BasePrice:    cp.BasePrice,
		DiscountRate: cp.DiscountRate,
	}
	return json.Marshal(raw)
}

// marshalBillingFeatures converts BillingFeatures proto to JSONB bytes
func marshalBillingFeatures(bf *pb.BillingFeatures) ([]byte, error) {
	if bf == nil {
		return []byte("{}"), nil
	}
	raw := struct {
		Recording      bool   `json:"recording"`
		Analytics      bool   `json:"analytics"`
		CustomBranding bool   `json:"custom_branding"`
		APIAccess      bool   `json:"api_access"`
		SupportLevel   string `json:"support_level"`
		SLA            bool   `json:"sla"`
	}{
		Recording:      bf.Recording,
		Analytics:      bf.Analytics,
		CustomBranding: bf.CustomBranding,
		APIAccess:      bf.ApiAccess,
		SupportLevel:   bf.SupportLevel,
		SLA:            bf.Sla,
	}
	return json.Marshal(raw)
}

// marshalAllocationDetails converts AllocationDetails proto to JSONB bytes
func marshalAllocationDetails(ad *pb.AllocationDetails) ([]byte, error) {
	if ad == nil {
		return []byte("{}"), nil
	}
	raw := struct {
		Limit     *float64 `json:"limit,omitempty"`
		UnitPrice float64  `json:"unit_price,omitempty"`
		Unit      string   `json:"unit,omitempty"`
	}{
		Limit:     ad.Limit,
		UnitPrice: ad.UnitPrice,
		Unit:      ad.Unit,
	}
	return json.Marshal(raw)
}

// scanOverageRates scans a JSONB column into OverageRates proto
func scanOverageRates(data []byte) *pb.OverageRates {
	if len(data) == 0 {
		return nil
	}
	var raw struct {
		Bandwidth struct {
			Limit     *float64 `json:"limit"`
			UnitPrice float64  `json:"unit_price"`
			Unit      string   `json:"unit"`
		} `json:"bandwidth"`
		Storage struct {
			Limit     *float64 `json:"limit"`
			UnitPrice float64  `json:"unit_price"`
			Unit      string   `json:"unit"`
		} `json:"storage"`
		Compute struct {
			Limit     *float64 `json:"limit"`
			UnitPrice float64  `json:"unit_price"`
			Unit      string   `json:"unit"`
		} `json:"compute"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	return &pb.OverageRates{
		Bandwidth: &pb.AllocationDetails{Limit: raw.Bandwidth.Limit, UnitPrice: raw.Bandwidth.UnitPrice, Unit: raw.Bandwidth.Unit},
		Storage:   &pb.AllocationDetails{Limit: raw.Storage.Limit, UnitPrice: raw.Storage.UnitPrice, Unit: raw.Storage.Unit},
		Compute:   &pb.AllocationDetails{Limit: raw.Compute.Limit, UnitPrice: raw.Compute.UnitPrice, Unit: raw.Compute.Unit},
	}
}

// mapToProtoStruct converts a map[string]interface{} to protobuf Struct
func mapToProtoStruct(m map[string]interface{}) *structpb.Struct {
	if m == nil {
		return nil
	}
	s, err := structpb.NewStruct(m)
	if err != nil {
		return nil
	}
	return s
}

// ServerMetrics holds Prometheus metrics for the gRPC server
type ServerMetrics struct {
	BillingOperations      *prometheus.CounterVec
	UsageOperations        *prometheus.CounterVec
	SubscriptionOperations *prometheus.CounterVec
	InvoiceOperations      *prometheus.CounterVec
	GRPCRequests           *prometheus.CounterVec
	GRPCDuration           *prometheus.HistogramVec
}

// PurserServer implements the Purser gRPC services
type PurserServer struct {
	pb.UnimplementedBillingServiceServer
	pb.UnimplementedUsageServiceServer
	pb.UnimplementedSubscriptionServiceServer
	pb.UnimplementedInvoiceServiceServer
	pb.UnimplementedPaymentServiceServer
	pb.UnimplementedClusterPricingServiceServer
	pb.UnimplementedPrepaidServiceServer
	pb.UnimplementedWebhookServiceServer
	pb.UnimplementedStripeServiceServer
	pb.UnimplementedMollieServiceServer
	pb.UnimplementedX402ServiceServer
	db                  *sql.DB
	logger              logging.Logger
	metrics             *ServerMetrics
	stripeClient        *stripe.Client
	mollieClient        *mollie.Client
	quartermasterClient *qmclient.GRPCClient
	commodoreClient     handlers.CommodoreClient
	hdwallet            *handlers.HDWallet
	x402handler         *handlers.X402Handler
	decklogClient       *decklogclient.BatchedClient
}

// NewPurserServer creates a new Purser gRPC server
func NewPurserServer(db *sql.DB, logger logging.Logger, metrics *ServerMetrics, stripeClient *stripe.Client, mollieClient *mollie.Client, qmClient *qmclient.GRPCClient, commodoreClient handlers.CommodoreClient, decklogClient *decklogclient.BatchedClient) *PurserServer {
	hdwallet := handlers.NewHDWallet(db, logger)
	if created, err := hdwallet.EnsureState(os.Getenv("HD_WALLET_XPUB")); err != nil {
		logger.WithError(err).Warn("HD wallet state not initialized; crypto deposits disabled until configured")
	} else if created {
		logger.Info("Initialized HD wallet state from HD_WALLET_XPUB")
	}
	return &PurserServer{
		db:                  db,
		logger:              logger,
		metrics:             metrics,
		stripeClient:        stripeClient,
		mollieClient:        mollieClient,
		quartermasterClient: qmClient,
		commodoreClient:     commodoreClient,
		hdwallet:            hdwallet,
		x402handler:         handlers.NewX402Handler(db, logger, hdwallet, commodoreClient),
		decklogClient:       decklogClient,
	}
}

// GetBillingTiers returns available billing tiers with cursor pagination
func (s *PurserServer) GetBillingTiers(ctx context.Context, req *pb.GetBillingTiersRequest) (*pb.GetBillingTiersResponse, error) {
	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid cursor: %v", err)
	}

	// Build query based on include_inactive
	whereClause := "WHERE is_active = true"
	args := []interface{}{}
	argIdx := 1
	if req.GetIncludeInactive() {
		whereClause = ""
	}

	// Build keyset query - for billing tiers we use (tier_level, id) for stable ordering
	if params.Cursor != nil {
		// Use cursor sort key as tier_level (encoded via EncodeCursorWithSortKey)
		tierLevelKey := params.Cursor.GetSortKey()
		if whereClause != "" {
			whereClause += " AND"
		} else {
			whereClause = "WHERE"
		}
		// Direction-aware keyset condition
		if params.Direction == pagination.Backward {
			whereClause += fmt.Sprintf(" (tier_level, id) < ($%d, $%d)", argIdx, argIdx+1)
		} else {
			whereClause += fmt.Sprintf(" (tier_level, id) > ($%d, $%d)", argIdx, argIdx+1)
		}
		args = append(args, tierLevelKey, params.Cursor.ID)
		argIdx += 2
	}

	// Direction-aware ORDER BY
	orderDir := "ASC"
	if params.Direction == pagination.Backward {
		orderDir = "DESC"
	}

	query := fmt.Sprintf(`
		SELECT id, tier_name, display_name, description, base_price, currency, billing_period,
		       bandwidth_allocation, storage_allocation, compute_allocation, features,
		       support_level, sla_level, metering_enabled, overage_rates,
		       is_active, tier_level, is_enterprise,
		       created_at, updated_at
		FROM purser.billing_tiers
		%s
		ORDER BY tier_level %s, id %s
		LIMIT $%d
	`, whereClause, orderDir, orderDir, argIdx)
	args = append(args, params.Limit+1) // +1 to detect hasMore

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var tiers []*pb.BillingTier
	for rows.Next() {
		var tier pb.BillingTier
		var createdAt, updatedAt time.Time
		var bandwidthAlloc, storageAlloc, computeAlloc, features, overageRates []byte

		err := rows.Scan(
			&tier.Id, &tier.TierName, &tier.DisplayName, &tier.Description,
			&tier.BasePrice, &tier.Currency, &tier.BillingPeriod,
			&bandwidthAlloc, &storageAlloc, &computeAlloc, &features,
			&tier.SupportLevel, &tier.SlaLevel, &tier.MeteringEnabled, &overageRates,
			&tier.IsActive, &tier.TierLevel, &tier.IsEnterprise,
			&createdAt, &updatedAt,
		)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to scan billing tier")
			continue
		}

		tier.BandwidthAllocation = scanAllocationDetails(bandwidthAlloc)
		tier.StorageAllocation = scanAllocationDetails(storageAlloc)
		tier.ComputeAllocation = scanAllocationDetails(computeAlloc)
		tier.Features = scanBillingFeatures(features)
		tier.OverageRates = scanOverageRates(overageRates)
		tier.CreatedAt = timestamppb.New(createdAt)
		tier.UpdatedAt = timestamppb.New(updatedAt)
		tiers = append(tiers, &tier)
	}

	// Determine pagination info
	resultsLen := len(tiers)
	if resultsLen > params.Limit {
		tiers = tiers[:params.Limit] // Remove the extra item
	}

	// Reverse results for backward pagination to maintain consistent order
	if params.Direction == pagination.Backward {
		slices.Reverse(tiers)
	}

	// Get available payment methods (dynamically from env)
	paymentMethods := s.getAvailablePaymentMethods()

	// Build cursors
	var startCursor, endCursor string
	if len(tiers) > 0 {
		first := tiers[0]
		last := tiers[len(tiers)-1]
		// Encode tier_level as sort key cursor
		startCursor = pagination.EncodeCursorWithSortKey(int64(first.TierLevel), first.Id)
		endCursor = pagination.EncodeCursorWithSortKey(int64(last.TierLevel), last.Id)
	}

	resp := &pb.GetBillingTiersResponse{
		Tiers:          tiers,
		PaymentMethods: paymentMethods,
		Pagination:     pagination.BuildResponse(resultsLen, params.Limit, params.Direction, int32(len(tiers)), startCursor, endCursor),
	}

	return resp, nil
}

// GetBillingTier returns a specific billing tier
func (s *PurserServer) GetBillingTier(ctx context.Context, req *pb.GetBillingTierRequest) (*pb.BillingTier, error) {
	tierID := req.GetTierId()
	if tierID == "" {
		return nil, status.Error(codes.InvalidArgument, "tier_id required")
	}

	var tier pb.BillingTier
	var createdAt, updatedAt time.Time
	var bandwidthAlloc, storageAlloc, computeAlloc, features, overageRates []byte

	err := s.db.QueryRowContext(ctx, `
		SELECT id, tier_name, display_name, description, base_price, currency, billing_period,
		       bandwidth_allocation, storage_allocation, compute_allocation, features,
		       support_level, sla_level, metering_enabled, overage_rates,
		       is_active, tier_level, is_enterprise,
		       created_at, updated_at
		FROM purser.billing_tiers
		WHERE id = $1
	`, tierID).Scan(
		&tier.Id, &tier.TierName, &tier.DisplayName, &tier.Description,
		&tier.BasePrice, &tier.Currency, &tier.BillingPeriod,
		&bandwidthAlloc, &storageAlloc, &computeAlloc, &features,
		&tier.SupportLevel, &tier.SlaLevel, &tier.MeteringEnabled, &overageRates,
		&tier.IsActive, &tier.TierLevel, &tier.IsEnterprise,
		&createdAt, &updatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "Billing tier not found")
	}

	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	tier.BandwidthAllocation = scanAllocationDetails(bandwidthAlloc)
	tier.StorageAllocation = scanAllocationDetails(storageAlloc)
	tier.ComputeAllocation = scanAllocationDetails(computeAlloc)
	tier.Features = scanBillingFeatures(features)
	tier.OverageRates = scanOverageRates(overageRates)
	tier.CreatedAt = timestamppb.New(createdAt)
	tier.UpdatedAt = timestamppb.New(updatedAt)

	return &tier, nil
}

// ============================================================================
// CROSS-SERVICE BILLING STATUS
// ============================================================================

// GetTenantBillingStatus returns lightweight billing status for cross-service checks.
// Called by Commodore (ValidateStreamKey, isTenantSuspended) and Quartermaster (ValidateTenant).
// This avoids cross-service database access by providing billing info via gRPC.
func (s *PurserServer) GetTenantBillingStatus(ctx context.Context, req *pb.GetTenantBillingStatusRequest) (*pb.GetTenantBillingStatusResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	var billingModel sql.NullString
	var subscriptionStatus sql.NullString
	var balanceCents sql.NullInt64

	currency := billing.DefaultCurrency()

	// Query subscription and prepaid balance
	err := s.db.QueryRowContext(ctx, `
		SELECT
			ts.billing_model,
			ts.status,
			pb.balance_cents
		FROM purser.tenant_subscriptions ts
		LEFT JOIN purser.prepaid_balances pb
			ON pb.tenant_id = ts.tenant_id AND pb.currency = $2
		WHERE ts.tenant_id = $1 AND ts.status != 'cancelled'
		ORDER BY ts.created_at DESC
		LIMIT 1
	`, tenantID, currency).Scan(&billingModel, &subscriptionStatus, &balanceCents)

	if err == sql.ErrNoRows {
		// No subscription = assume postpaid, not suspended, not negative
		return &pb.GetTenantBillingStatusResponse{
			BillingModel:      "postpaid",
			IsSuspended:       false,
			IsBalanceNegative: false,
			BalanceCents:      0,
		}, nil
	}

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Error("Database error getting billing status")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Determine billing model (default to postpaid)
	model := "postpaid"
	if billingModel.Valid && billingModel.String != "" {
		model = billingModel.String
	}

	// Check if suspended (subscription status = 'suspended')
	isSuspended := subscriptionStatus.Valid && subscriptionStatus.String == "suspended"

	// Check if balance is negative (prepaid only)
	isBalanceNegative := false
	balance := int64(0)
	if balanceCents.Valid {
		balance = balanceCents.Int64
		if model == "prepaid" && balance <= 0 {
			isBalanceNegative = true
		}
	}

	return &pb.GetTenantBillingStatusResponse{
		BillingModel:      model,
		IsSuspended:       isSuspended,
		IsBalanceNegative: isBalanceNegative,
		BalanceCents:      balance,
	}, nil
}

// NOTE: Usage ingestion is handled via Kafka (billing.usage_reports topic)
// consumed by JobManager in handlers/jobs.go. No gRPC ingestion endpoint needed.
// The processUsageSummary and updateInvoiceDraft logic lives in handlers/jobs.go

// GetUsageRecords returns usage records for a tenant with cursor pagination
func (s *PurserServer) GetUsageRecords(ctx context.Context, req *pb.GetUsageRecordsRequest) (*pb.UsageRecordsResponse, error) {
	tenantID := req.GetTenantId()
	ctxTenantID := middleware.GetTenantID(ctx)
	isServiceCall := middleware.IsServiceCall(ctx)

	if !isServiceCall {
		if ctxTenantID == "" {
			return nil, status.Error(codes.PermissionDenied, "tenant context required")
		}
		if tenantID != "" && tenantID != ctxTenantID {
			return nil, status.Error(codes.PermissionDenied, "cross-tenant access denied")
		}
		tenantID = ctxTenantID
	}
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}
	if req.GetTimeRange() == nil || req.GetTimeRange().GetStart() == nil || req.GetTimeRange().GetEnd() == nil {
		return nil, status.Error(codes.InvalidArgument, "time_range required")
	}

	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid cursor: %v", err)
	}

	// Build WHERE clause
	args := []interface{}{tenantID}
	whereClause := "WHERE tenant_id = $1"
	argIdx := 2

	if req.GetClusterId() != "" {
		whereClause += fmt.Sprintf(" AND cluster_id = $%d", argIdx)
		args = append(args, req.GetClusterId())
		argIdx++
	}
	if req.GetUsageType() != "" {
		whereClause += fmt.Sprintf(" AND usage_type = $%d", argIdx)
		args = append(args, req.GetUsageType())
		argIdx++
	}
	if req.GetTimeRange() != nil && req.GetTimeRange().GetStart() != nil && req.GetTimeRange().GetEnd() != nil {
		start := req.GetTimeRange().GetStart().AsTime()
		end := req.GetTimeRange().GetEnd().AsTime()
		whereClause += fmt.Sprintf(" AND period_start < $%d AND period_end > $%d", argIdx, argIdx+1)
		args = append(args, end, start)
		argIdx += 2
	}

	orderExpr := "COALESCE(period_start, created_at)"

	// Add cursor condition for keyset pagination (direction-aware)
	if params.Cursor != nil {
		if params.Direction == pagination.Backward {
			whereClause += fmt.Sprintf(" AND (%s, id) > ($%d, $%d)", orderExpr, argIdx, argIdx+1)
		} else {
			whereClause += fmt.Sprintf(" AND (%s, id) < ($%d, $%d)", orderExpr, argIdx, argIdx+1)
		}
		args = append(args, params.Cursor.Timestamp, params.Cursor.ID)
		argIdx += 2
	}

	// Direction-aware ORDER BY
	orderDir := "DESC"
	if params.Direction == pagination.Backward {
		orderDir = "ASC"
	}

	// Get records with keyset pagination (including usage_details)
	query := fmt.Sprintf(`
		SELECT id, tenant_id, cluster_id, usage_type, usage_value, usage_details, created_at, period_start, period_end, granularity
		FROM purser.usage_records
		%s
		ORDER BY %s %s, id %s
		LIMIT $%d
	`, whereClause, orderExpr, orderDir, orderDir, argIdx)
	args = append(args, params.Limit+1) // +1 to detect hasMore

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var records []*pb.UsageRecord
	for rows.Next() {
		var rec pb.UsageRecord
		var clusterID sql.NullString
		var usageDetailsBytes []byte
		var createdAt time.Time
		var periodStart, periodEnd sql.NullTime
		var granularity sql.NullString

		err := rows.Scan(&rec.Id, &rec.TenantId, &clusterID, &rec.UsageType, &rec.UsageValue, &usageDetailsBytes, &createdAt, &periodStart, &periodEnd, &granularity)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to scan usage record")
			continue
		}

		if clusterID.Valid {
			rec.ClusterId = clusterID.String
		}
		rec.CreatedAt = timestamppb.New(createdAt)
		if periodStart.Valid {
			rec.PeriodStart = timestamppb.New(periodStart.Time)
		}
		if periodEnd.Valid {
			rec.PeriodEnd = timestamppb.New(periodEnd.Time)
		}
		if granularity.Valid {
			rec.Granularity = granularity.String
		}

		// Convert usage_details JSONB to protobuf Struct
		if len(usageDetailsBytes) > 0 {
			var detailsMap map[string]interface{}
			if json.Unmarshal(usageDetailsBytes, &detailsMap) == nil {
				rec.UsageDetails = mapToProtoStruct(detailsMap)
			}
		}

		records = append(records, &rec)
	}

	// Determine pagination info
	resultsLen := len(records)
	if resultsLen > params.Limit {
		records = records[:params.Limit] // Remove the extra item
	}

	// Reverse results for backward pagination to maintain consistent order
	if params.Direction == pagination.Backward {
		slices.Reverse(records)
	}

	// Build cursors
	var startCursor, endCursor string
	if len(records) > 0 {
		firstRec := records[0]
		lastRec := records[len(records)-1]
		startTime := firstRec.CreatedAt.AsTime()
		if firstRec.PeriodStart != nil {
			startTime = firstRec.PeriodStart.AsTime()
		}
		endTime := lastRec.CreatedAt.AsTime()
		if lastRec.PeriodStart != nil {
			endTime = lastRec.PeriodStart.AsTime()
		}
		startCursor = pagination.EncodeCursor(startTime, firstRec.Id)
		endCursor = pagination.EncodeCursor(endTime, lastRec.Id)
	}

	resp := &pb.UsageRecordsResponse{
		UsageRecords: records,
		TenantId:     tenantID,
		Filters: &pb.UsageFilters{
			ClusterId: req.GetClusterId(),
			UsageType: req.GetUsageType(),
			TimeRange: req.GetTimeRange(),
		},
		Pagination: pagination.BuildResponse(resultsLen, params.Limit, params.Direction, int32(len(records)), startCursor, endCursor),
	}

	return resp, nil
}

// GetUsageAggregates returns rollup-backed usage aggregates for charts
func (s *PurserServer) GetUsageAggregates(ctx context.Context, req *pb.GetUsageAggregatesRequest) (*pb.GetUsageAggregatesResponse, error) {
	tenantID := req.GetTenantId()
	ctxTenantID := middleware.GetTenantID(ctx)
	isServiceCall := middleware.IsServiceCall(ctx)

	if !isServiceCall {
		if ctxTenantID == "" {
			return nil, status.Error(codes.PermissionDenied, "tenant context required")
		}
		if tenantID != "" && tenantID != ctxTenantID {
			return nil, status.Error(codes.PermissionDenied, "cross-tenant access denied")
		}
		tenantID = ctxTenantID
	}
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}
	if req.GetTimeRange() == nil || req.GetTimeRange().GetStart() == nil || req.GetTimeRange().GetEnd() == nil {
		return nil, status.Error(codes.InvalidArgument, "time_range required")
	}

	granularity := req.GetGranularity()
	if granularity == "" {
		granularity = "daily"
	}
	switch granularity {
	case "hourly", "daily", "monthly":
	default:
		return nil, status.Error(codes.InvalidArgument, "invalid granularity")
	}

	start := req.GetTimeRange().GetStart().AsTime()
	end := req.GetTimeRange().GetEnd().AsTime()

	whereClause := "WHERE tenant_id = $1 AND period_start < $3 AND period_end > $2 AND granularity = $4"
	args := []interface{}{tenantID, start, end, granularity}
	argIdx := 5

	if len(req.GetUsageTypes()) > 0 {
		whereClause += fmt.Sprintf(" AND usage_type = ANY($%d)", argIdx)
		args = append(args, pq.Array(req.GetUsageTypes()))
	}

	query := fmt.Sprintf(`
		SELECT usage_type, period_start, period_end, usage_value, granularity
		FROM purser.usage_records
		%s
		ORDER BY period_start ASC, usage_type ASC
	`, whereClause)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var aggregates []*pb.UsageAggregate
	for rows.Next() {
		var usageType, rowGranularity string
		var usageValue float64
		var periodStart, periodEnd sql.NullTime
		if err := rows.Scan(&usageType, &periodStart, &periodEnd, &usageValue, &rowGranularity); err != nil {
			continue
		}
		agg := &pb.UsageAggregate{
			UsageType:   usageType,
			UsageValue:  usageValue,
			Granularity: rowGranularity,
		}
		if periodStart.Valid {
			agg.PeriodStart = timestamppb.New(periodStart.Time)
		}
		if periodEnd.Valid {
			agg.PeriodEnd = timestamppb.New(periodEnd.Time)
		}
		aggregates = append(aggregates, agg)
	}

	return &pb.GetUsageAggregatesResponse{Aggregates: aggregates}, nil
}

// CheckUserLimit checks if a tenant can add more users
func (s *PurserServer) CheckUserLimit(ctx context.Context, req *pb.CheckUserLimitRequest) (*pb.CheckUserLimitResponse, error) {
	tenantID := req.GetTenantId()
	ctxTenantID := middleware.GetTenantID(ctx)
	isServiceCall := middleware.IsServiceCall(ctx)

	if !isServiceCall {
		if ctxTenantID == "" {
			return nil, status.Error(codes.PermissionDenied, "tenant context required")
		}
		if tenantID != "" && tenantID != ctxTenantID {
			return nil, status.Error(codes.PermissionDenied, "cross-tenant access denied")
		}
		tenantID = ctxTenantID
	}
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	// Get current user count via Commodore gRPC (not direct DB access)
	var currentUsers int32
	if s.commodoreClient != nil {
		userCount, err := s.commodoreClient.GetTenantUserCount(ctx, tenantID)
		if err != nil {
			s.logger.WithFields(logging.Fields{
				"tenant_id": tenantID,
				"error":     err,
			}).Warn("Failed to get user count from Commodore, allowing by default")
			//nolint:nilerr // fail-open: allow by default on internal errors
			return &pb.CheckUserLimitResponse{Allowed: true}, nil
		}
		currentUsers = userCount.ActiveCount
	} else {
		s.logger.Warn("Commodore client not available, allowing by default")
		return &pb.CheckUserLimitResponse{Allowed: true}, nil
	}

	// Get tier limit
	var maxUsers sql.NullInt32
	err := s.db.QueryRowContext(ctx, `
		SELECT t.max_users
		FROM purser.tenant_subscriptions s
		JOIN purser.billing_tiers t ON s.tier_id = t.id
		WHERE s.tenant_id = $1 AND s.status = 'active'
		ORDER BY s.created_at DESC
		LIMIT 1
	`, tenantID).Scan(&maxUsers)

	if err == sql.ErrNoRows {
		// No subscription, use default limit (10 for free tier)
		maxUsers = sql.NullInt32{Int32: 10, Valid: true}
	} else if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Warn("Failed to get tier limit, allowing by default")
		//nolint:nilerr // fail-open: allow by default on internal errors
		return &pb.CheckUserLimitResponse{Allowed: true}, nil
	}

	// Unlimited if max_users is null or 0
	if !maxUsers.Valid || maxUsers.Int32 == 0 {
		return &pb.CheckUserLimitResponse{
			Allowed:      true,
			CurrentUsers: currentUsers,
			MaxUsers:     0, // 0 = unlimited
		}, nil
	}

	allowed := currentUsers < maxUsers.Int32
	resp := &pb.CheckUserLimitResponse{
		Allowed:      allowed,
		CurrentUsers: currentUsers,
		MaxUsers:     maxUsers.Int32,
	}
	if !allowed {
		resp.Error = "User limit reached for your plan"
	}

	return resp, nil
}

// ============================================================================
// SUBSCRIPTION SERVICE
// ============================================================================

// GetSubscription returns the current subscription for a tenant
func (s *PurserServer) GetSubscription(ctx context.Context, req *pb.GetSubscriptionRequest) (*pb.GetSubscriptionResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	var sub pb.TenantSubscription
	var startedAt, createdAt, updatedAt time.Time
	var trialEndsAt, nextBillingDate, cancelledAt sql.NullTime
	var billingPeriodStart, billingPeriodEnd sql.NullTime
	var paymentMethod, paymentReference, taxID sql.NullString
	var taxRate sql.NullFloat64
	var billingModel string
	var stripeCustomerID, stripeSubscriptionID, stripeSubscriptionStatus, mollieSubscriptionID sql.NullString
	var stripePeriodEnd sql.NullTime
	var dunningAttempts sql.NullInt32

	err := s.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, tier_id, status, billing_email, started_at,
		       trial_ends_at, next_billing_date, cancelled_at,
		       billing_period_start, billing_period_end,
		       payment_method, payment_reference, tax_id, tax_rate,
		       billing_model,
		       stripe_customer_id, stripe_subscription_id, stripe_subscription_status, stripe_current_period_end, dunning_attempts,
		       mollie_subscription_id,
		       created_at, updated_at
		FROM purser.tenant_subscriptions
		WHERE tenant_id = $1 AND status != 'cancelled'
		ORDER BY created_at DESC
		LIMIT 1
	`, tenantID).Scan(&sub.Id, &sub.TenantId, &sub.TierId, &sub.Status, &sub.BillingEmail,
		&startedAt, &trialEndsAt, &nextBillingDate, &cancelledAt,
		&billingPeriodStart, &billingPeriodEnd,
		&paymentMethod, &paymentReference, &taxID, &taxRate,
		&billingModel,
		&stripeCustomerID, &stripeSubscriptionID, &stripeSubscriptionStatus, &stripePeriodEnd, &dunningAttempts,
		&mollieSubscriptionID,
		&createdAt, &updatedAt)

	if err == sql.ErrNoRows {
		return &pb.GetSubscriptionResponse{
			Error: "No active subscription found",
		}, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	sub.StartedAt = timestamppb.New(startedAt)
	sub.CreatedAt = timestamppb.New(createdAt)
	sub.UpdatedAt = timestamppb.New(updatedAt)
	sub.BillingModel = billingModel
	if trialEndsAt.Valid {
		sub.TrialEndsAt = timestamppb.New(trialEndsAt.Time)
	}
	if nextBillingDate.Valid {
		sub.NextBillingDate = timestamppb.New(nextBillingDate.Time)
	}
	if cancelledAt.Valid {
		sub.CancelledAt = timestamppb.New(cancelledAt.Time)
	}
	if billingPeriodStart.Valid {
		sub.BillingPeriodStart = timestamppb.New(billingPeriodStart.Time)
	}
	if billingPeriodEnd.Valid {
		sub.BillingPeriodEnd = timestamppb.New(billingPeriodEnd.Time)
	}
	if paymentMethod.Valid {
		sub.PaymentMethod = &paymentMethod.String
	}
	if paymentReference.Valid {
		sub.PaymentReference = &paymentReference.String
	}
	if taxID.Valid {
		sub.TaxId = &taxID.String
	}
	if taxRate.Valid {
		sub.TaxRate = &taxRate.Float64
	}
	if stripeCustomerID.Valid {
		sub.StripeCustomerId = &stripeCustomerID.String
	}
	if stripeSubscriptionID.Valid {
		sub.StripeSubscriptionId = &stripeSubscriptionID.String
	}
	if stripeSubscriptionStatus.Valid {
		sub.StripeSubscriptionStatus = &stripeSubscriptionStatus.String
	}
	if stripePeriodEnd.Valid {
		sub.StripeCurrentPeriodEnd = timestamppb.New(stripePeriodEnd.Time)
	}
	if dunningAttempts.Valid {
		sub.DunningAttempts = dunningAttempts.Int32
	}
	if mollieSubscriptionID.Valid {
		sub.MollieSubscriptionId = &mollieSubscriptionID.String
	}

	return &pb.GetSubscriptionResponse{
		Subscription: &sub,
	}, nil
}

// CreateSubscription creates a new subscription for a tenant
func (s *PurserServer) CreateSubscription(ctx context.Context, req *pb.CreateSubscriptionRequest) (*pb.TenantSubscription, error) {
	tenantID := req.GetTenantId()
	tierID := req.GetTierId()
	billingEmail := req.GetBillingEmail()

	if tenantID == "" || tierID == "" || billingEmail == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id, tier_id, and billing_email are required")
	}

	userID := middleware.GetUserID(ctx)
	// Verify tier exists
	var tierExists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM purser.billing_tiers WHERE id = $1 AND is_active = true)`, tierID).Scan(&tierExists)
	if err != nil || !tierExists {
		return nil, status.Error(codes.NotFound, "billing tier not found")
	}

	// Create subscription
	subID := fmt.Sprintf("sub_%d", time.Now().UnixNano())
	now := time.Now()

	var trialEndsAt sql.NullTime
	if req.GetTrialEndsAt() != nil {
		trialEndsAt = sql.NullTime{Time: req.GetTrialEndsAt().AsTime(), Valid: true}
	}

	periodStart := now
	if req.GetBillingPeriodStart() != nil {
		periodStart = req.GetBillingPeriodStart().AsTime()
	} else {
		periodStart = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	}

	periodEnd := periodStart.AddDate(0, 1, 0)
	if req.GetBillingPeriodEnd() != nil {
		periodEnd = req.GetBillingPeriodEnd().AsTime()
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO purser.tenant_subscriptions (
			id, tenant_id, tier_id, status, billing_email, started_at,
			trial_ends_at, next_billing_date, billing_period_start, billing_period_end,
			payment_method, created_at, updated_at
		)
		VALUES ($1, $2, $3, 'active', $4, $5, $6, $7, $8, $9, $10, $5, $5)
	`, subID, tenantID, tierID, billingEmail, now, trialEndsAt, periodEnd, periodStart, periodEnd, req.GetPaymentMethod())

	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create subscription: %v", err)
	}

	sub := &pb.TenantSubscription{
		Id:                 subID,
		TenantId:           tenantID,
		TierId:             tierID,
		Status:             "active",
		BillingEmail:       billingEmail,
		StartedAt:          timestamppb.New(now),
		BillingPeriodStart: timestamppb.New(periodStart),
		BillingPeriodEnd:   timestamppb.New(periodEnd),
		CreatedAt:          timestamppb.New(now),
		UpdatedAt:          timestamppb.New(now),
	}
	if trialEndsAt.Valid {
		sub.TrialEndsAt = timestamppb.New(trialEndsAt.Time)
	}
	if req.GetPaymentMethod() != "" {
		pm := req.GetPaymentMethod()
		sub.PaymentMethod = &pm
	}

	s.emitBillingEvent(ctx, eventSubscriptionCreated, tenantID, userID, "subscription", subID, &pb.BillingEvent{
		SubscriptionId: subID,
		Status:         "active",
		Provider:       req.GetPaymentMethod(),
	})

	return sub, nil
}

// validateCustomPricing validates custom pricing fields
func validateCustomPricing(cp *pb.CustomPricing) error {
	if cp == nil {
		return nil
	}
	if cp.BasePrice < 0 {
		return fmt.Errorf("base_price cannot be negative")
	}
	if cp.DiscountRate < 0 || cp.DiscountRate > 1 {
		return fmt.Errorf("discount_rate must be between 0 and 1")
	}
	if cp.OverageRates != nil {
		if err := validateAllocationDetails(cp.OverageRates.Bandwidth); err != nil {
			return fmt.Errorf("overage_rates.bandwidth: %w", err)
		}
		if err := validateAllocationDetails(cp.OverageRates.Storage); err != nil {
			return fmt.Errorf("overage_rates.storage: %w", err)
		}
		if err := validateAllocationDetails(cp.OverageRates.Compute); err != nil {
			return fmt.Errorf("overage_rates.compute: %w", err)
		}
	}
	return nil
}

// validateAllocationDetails validates allocation detail fields
func validateAllocationDetails(ad *pb.AllocationDetails) error {
	if ad == nil {
		return nil
	}
	if ad.Limit != nil && *ad.Limit < 0 {
		return fmt.Errorf("limit cannot be negative")
	}
	if ad.UnitPrice < 0 {
		return fmt.Errorf("unit_price cannot be negative")
	}
	return nil
}

// UpdateSubscription updates an existing subscription
func (s *PurserServer) UpdateSubscription(ctx context.Context, req *pb.UpdateSubscriptionRequest) (*pb.TenantSubscription, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	userID := middleware.GetUserID(ctx)
	// Validate custom fields before saving
	if err := validateCustomPricing(req.CustomPricing); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid custom_pricing: %v", err)
	}
	if err := validateAllocationDetails(req.CustomAllocations); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid custom_allocations: %v", err)
	}

	// Build dynamic update
	updates := []string{"updated_at = NOW()"}
	args := []interface{}{}
	argIdx := 1

	if req.TierId != nil {
		updates = append(updates, fmt.Sprintf("tier_id = $%d", argIdx))
		args = append(args, *req.TierId)
		argIdx++
	}
	if req.BillingEmail != nil {
		updates = append(updates, fmt.Sprintf("billing_email = $%d", argIdx))
		args = append(args, *req.BillingEmail)
		argIdx++
	}
	if req.PaymentMethod != nil {
		updates = append(updates, fmt.Sprintf("payment_method = $%d", argIdx))
		args = append(args, *req.PaymentMethod)
		argIdx++
	}
	if req.Status != nil {
		updates = append(updates, fmt.Sprintf("status = $%d", argIdx))
		args = append(args, *req.Status)
		argIdx++
	}
	if req.BillingPeriodStart != nil {
		updates = append(updates, fmt.Sprintf("billing_period_start = $%d", argIdx))
		args = append(args, req.BillingPeriodStart.AsTime())
		argIdx++
	}
	if req.BillingPeriodEnd != nil {
		updates = append(updates, fmt.Sprintf("billing_period_end = $%d", argIdx))
		args = append(args, req.BillingPeriodEnd.AsTime())
		argIdx++
	}

	// Handle custom billing fields (JSONB)
	if req.CustomPricing != nil {
		pricingJSON, err := marshalCustomPricing(req.CustomPricing)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid custom_pricing: %v", err)
		}
		updates = append(updates, fmt.Sprintf("custom_pricing = $%d", argIdx))
		args = append(args, pricingJSON)
		argIdx++
	}
	if req.CustomFeatures != nil {
		featuresJSON, err := marshalBillingFeatures(req.CustomFeatures)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid custom_features: %v", err)
		}
		updates = append(updates, fmt.Sprintf("custom_features = $%d", argIdx))
		args = append(args, featuresJSON)
		argIdx++
	}
	if req.CustomAllocations != nil {
		allocJSON, err := marshalAllocationDetails(req.CustomAllocations)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid custom_allocations: %v", err)
		}
		updates = append(updates, fmt.Sprintf("custom_allocations = $%d", argIdx))
		args = append(args, allocJSON)
		argIdx++
	}

	query := fmt.Sprintf("UPDATE purser.tenant_subscriptions SET %s WHERE tenant_id = $%d AND status != 'cancelled'",
		strings.Join(updates, ", "), argIdx)
	args = append(args, tenantID)

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update subscription: %v", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, status.Error(codes.NotFound, "subscription not found")
	}

	// Return updated subscription
	resp, err := s.GetSubscription(ctx, &pb.GetSubscriptionRequest{TenantId: tenantID})
	if err != nil {
		return nil, err
	}
	if resp.Subscription != nil {
		paymentMethod := ""
		if resp.Subscription.PaymentMethod != nil {
			paymentMethod = *resp.Subscription.PaymentMethod
		}
		s.emitBillingEvent(ctx, eventSubscriptionUpdated, tenantID, userID, "subscription", resp.Subscription.Id, &pb.BillingEvent{
			SubscriptionId: resp.Subscription.Id,
			Status:         resp.Subscription.Status,
			Provider:       paymentMethod,
		})
	}
	return resp.Subscription, nil
}

// CancelSubscription cancels a tenant's subscription
func (s *PurserServer) CancelSubscription(ctx context.Context, req *pb.CancelSubscriptionRequest) (*emptypb.Empty, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	userID := middleware.GetUserID(ctx)
	var subscriptionID string
	_ = s.db.QueryRowContext(ctx, `
		SELECT id FROM purser.tenant_subscriptions WHERE tenant_id = $1 AND status != 'cancelled'
	`, tenantID).Scan(&subscriptionID)

	result, err := s.db.ExecContext(ctx, `
		UPDATE purser.tenant_subscriptions
		SET status = 'cancelled', cancelled_at = NOW(), updated_at = NOW()
		WHERE tenant_id = $1 AND status != 'cancelled'
	`, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to cancel subscription: %v", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, status.Error(codes.NotFound, "subscription not found")
	}

	if subscriptionID != "" {
		s.emitBillingEvent(ctx, eventSubscriptionCanceled, tenantID, userID, "subscription", subscriptionID, &pb.BillingEvent{
			SubscriptionId: subscriptionID,
			Status:         "cancelled",
		})
	}

	return &emptypb.Empty{}, nil
}

// ============================================================================
// INVOICE SERVICE
// ============================================================================

// GetInvoice returns a specific invoice with tenant isolation
func (s *PurserServer) GetInvoice(ctx context.Context, req *pb.GetInvoiceRequest) (*pb.GetInvoiceResponse, error) {
	invoiceID := req.GetInvoiceId()
	if invoiceID == "" {
		return nil, status.Error(codes.InvalidArgument, "invoice_id required")
	}

	// Get tenant_id from context (for tenant isolation on user calls)
	ctxTenantID := middleware.GetTenantID(ctx)
	isServiceCall := middleware.IsServiceCall(ctx)

	var invoice pb.Invoice
	var dueDate, createdAt, updatedAt time.Time
	var paidAt sql.NullTime
	var tierID string
	var usageDetailsBytes []byte
	var periodStart, periodEnd sql.NullTime

	// Build query with optional tenant filter for user calls
	query := `
		SELECT i.id, i.tenant_id, i.amount, i.base_amount, i.metered_amount, i.prepaid_credit_applied, i.currency, i.status,
		       i.due_date, i.paid_at, i.usage_details, i.created_at, i.updated_at, s.tier_id,
		       i.period_start, i.period_end
		FROM purser.billing_invoices i
		LEFT JOIN purser.tenant_subscriptions s ON i.tenant_id = s.tenant_id AND s.status != 'cancelled'
		WHERE i.id = $1`
	args := []interface{}{invoiceID}

	// Add tenant filter for user calls (not service-to-service)
	if !isServiceCall && ctxTenantID != "" {
		query += " AND i.tenant_id = $2"
		args = append(args, ctxTenantID)
	}

	err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&invoice.Id, &invoice.TenantId, &invoice.Amount, &invoice.BaseAmount,
		&invoice.MeteredAmount, &invoice.PrepaidCreditApplied, &invoice.Currency, &invoice.Status,
		&dueDate, &paidAt, &usageDetailsBytes, &createdAt, &updatedAt, &tierID,
		&periodStart, &periodEnd)

	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "invoice not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	invoice.DueDate = timestamppb.New(dueDate)
	invoice.CreatedAt = timestamppb.New(createdAt)
	invoice.UpdatedAt = timestamppb.New(updatedAt)
	if paidAt.Valid {
		invoice.PaidAt = timestamppb.New(paidAt.Time)
	}
	if periodStart.Valid {
		invoice.PeriodStart = timestamppb.New(periodStart.Time)
	}
	if periodEnd.Valid {
		invoice.PeriodEnd = timestamppb.New(periodEnd.Time)
	}

	// Always initialize to empty slice for GraphQL non-null list compliance
	invoice.LineItems = []*pb.LineItem{}

	// Convert usage_details JSONB to protobuf Struct and typed fields
	if len(usageDetailsBytes) > 0 {
		var detailsMap map[string]interface{}
		if json.Unmarshal(usageDetailsBytes, &detailsMap) == nil {
			invoice.UsageDetails = mapToProtoStruct(detailsMap)
			invoice.UsageSummary = parseUsageDetailsToSummary(detailsMap, invoice.TenantId, invoice.PeriodStart, invoice.PeriodEnd)
			invoice.LineItems = generateInvoiceLineItems(detailsMap, invoice.BaseAmount, invoice.MeteredAmount)
			if invoice.UsageSummary != nil {
				invoice.UsageSummary.GeoBreakdown = parseGeoBreakdown(detailsMap)
			}
		}
	}

	// Get tier info
	var tier *pb.BillingTier
	if tierID != "" {
		tierResp, _ := s.GetBillingTier(ctx, &pb.GetBillingTierRequest{TierId: tierID})
		tier = tierResp
	}

	return &pb.GetInvoiceResponse{
		Invoice: &invoice,
		Tier:    tier,
	}, nil
}

// ListInvoices returns invoices for a tenant
func (s *PurserServer) ListInvoices(ctx context.Context, req *pb.ListInvoicesRequest) (*pb.ListInvoicesResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid cursor: %v", err)
	}

	// Build query
	whereClause := "WHERE tenant_id = $1"
	args := []interface{}{tenantID}
	argIdx := 2

	if req.Status != nil && *req.Status != "" {
		whereClause += fmt.Sprintf(" AND status = $%d", argIdx)
		args = append(args, *req.Status)
		argIdx++
	}

	orderExpr := "COALESCE(period_start, created_at)"

	// Direction-aware keyset condition
	if params.Cursor != nil {
		if params.Direction == pagination.Backward {
			whereClause += fmt.Sprintf(" AND (%s, id) > ($%d, $%d)", orderExpr, argIdx, argIdx+1)
		} else {
			whereClause += fmt.Sprintf(" AND (%s, id) < ($%d, $%d)", orderExpr, argIdx, argIdx+1)
		}
		args = append(args, params.Cursor.Timestamp, params.Cursor.ID)
		argIdx += 2
	}

	// Direction-aware ORDER BY
	orderDir := "DESC"
	if params.Direction == pagination.Backward {
		orderDir = "ASC"
	}

	query := fmt.Sprintf(`
		SELECT id, tenant_id, amount, base_amount, metered_amount, prepaid_credit_applied, currency, status,
		       due_date, paid_at, usage_details, created_at, updated_at, period_start, period_end
		FROM purser.billing_invoices
		%s
		ORDER BY %s %s, id %s
		LIMIT $%d
	`, whereClause, orderExpr, orderDir, orderDir, argIdx)
	args = append(args, params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var invoices []*pb.Invoice
	for rows.Next() {
		var inv pb.Invoice
		var dueDate, createdAt, updatedAt time.Time
		var paidAt sql.NullTime
		var usageDetails []byte
		var periodStart, periodEnd sql.NullTime

		err := rows.Scan(&inv.Id, &inv.TenantId, &inv.Amount, &inv.BaseAmount,
			&inv.MeteredAmount, &inv.PrepaidCreditApplied, &inv.Currency, &inv.Status,
			&dueDate, &paidAt, &usageDetails, &createdAt, &updatedAt, &periodStart, &periodEnd)
		if err != nil {
			continue
		}

		inv.DueDate = timestamppb.New(dueDate)
		inv.CreatedAt = timestamppb.New(createdAt)
		inv.UpdatedAt = timestamppb.New(updatedAt)
		if paidAt.Valid {
			inv.PaidAt = timestamppb.New(paidAt.Time)
		}
		if periodStart.Valid {
			inv.PeriodStart = timestamppb.New(periodStart.Time)
		}
		if periodEnd.Valid {
			inv.PeriodEnd = timestamppb.New(periodEnd.Time)
		}
		// Always initialize to empty slice for GraphQL non-null list compliance
		inv.LineItems = []*pb.LineItem{}

		// Convert usage_details JSONB to protobuf Struct and typed fields
		if len(usageDetails) > 0 {
			var details map[string]interface{}
			if json.Unmarshal(usageDetails, &details) == nil {
				inv.UsageDetails = mapToProtoStruct(details)
				inv.UsageSummary = parseUsageDetailsToSummary(details, inv.TenantId, inv.PeriodStart, inv.PeriodEnd)
				inv.LineItems = generateInvoiceLineItems(details, inv.BaseAmount, inv.MeteredAmount)
				if inv.UsageSummary != nil {
					inv.UsageSummary.GeoBreakdown = parseGeoBreakdown(details)
				}
			}
		}
		invoices = append(invoices, &inv)
	}

	// Determine pagination info
	resultsLen := len(invoices)
	if resultsLen > params.Limit {
		invoices = invoices[:params.Limit]
	}

	// Reverse results for backward pagination to maintain consistent order
	if params.Direction == pagination.Backward {
		slices.Reverse(invoices)
	}

	// Build cursors
	var startCursor, endCursor string
	if len(invoices) > 0 {
		first := invoices[0]
		last := invoices[len(invoices)-1]
		startTime := first.CreatedAt.AsTime()
		if first.PeriodStart != nil {
			startTime = first.PeriodStart.AsTime()
		}
		endTime := last.CreatedAt.AsTime()
		if last.PeriodStart != nil {
			endTime = last.PeriodStart.AsTime()
		}
		startCursor = pagination.EncodeCursor(startTime, first.Id)
		endCursor = pagination.EncodeCursor(endTime, last.Id)
	}

	resp := &pb.ListInvoicesResponse{
		Invoices:   invoices,
		Pagination: pagination.BuildResponse(resultsLen, params.Limit, params.Direction, int32(len(invoices)), startCursor, endCursor),
	}

	return resp, nil
}

// ============================================================================
// PAYMENT SERVICE
// ============================================================================

// CreatePayment initiates a payment for an invoice with tenant isolation
func (s *PurserServer) CreatePayment(ctx context.Context, req *pb.PaymentRequest) (*pb.PaymentResponse, error) {
	invoiceID := req.GetInvoiceId()
	method := req.GetMethod()

	if invoiceID == "" || method == "" {
		return nil, status.Error(codes.InvalidArgument, "invoice_id and method are required")
	}

	userID := middleware.GetUserID(ctx)
	// Get tenant_id from context for isolation
	ctxTenantID := middleware.GetTenantID(ctx)
	isServiceCall := middleware.IsServiceCall(ctx)

	// Validate payment method is available
	availableMethods := s.getAvailablePaymentMethods()
	methodAvailable := false
	for _, m := range availableMethods {
		if m == method {
			methodAvailable = true
			break
		}
	}
	if !methodAvailable {
		return nil, status.Errorf(codes.InvalidArgument, "payment method %s not available", method)
	}

	// Verify invoice exists, is unpaid, and belongs to tenant (for user calls)
	var invoiceTenantID, invoiceStatus, invoiceCurrency string
	var invoiceAmount float64
	query := `SELECT tenant_id, amount, currency, status FROM purser.billing_invoices WHERE id = $1 AND status = 'pending'`
	args := []interface{}{invoiceID}

	// Add tenant filter for user calls
	if !isServiceCall && ctxTenantID != "" {
		query = `SELECT tenant_id, amount, currency, status FROM purser.billing_invoices WHERE id = $1 AND tenant_id = $2 AND status = 'pending'`
		args = append(args, ctxTenantID)
	}

	err := s.db.QueryRowContext(ctx, query, args...).Scan(&invoiceTenantID, &invoiceAmount, &invoiceCurrency, &invoiceStatus)
	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "invoice not found or already paid")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Idempotency: return existing pending payment for this invoice + method
	var existingPaymentID, existingTxID string
	var existingCreatedAt time.Time
	err = s.db.QueryRowContext(ctx, `
		SELECT id, tx_id, created_at
		FROM purser.billing_payments
		WHERE invoice_id = $1 AND method = $2 AND status = 'pending'
		ORDER BY created_at DESC
		LIMIT 1
	`, invoiceID, method).Scan(&existingPaymentID, &existingTxID, &existingCreatedAt)
	if err == nil {
		resp := &pb.PaymentResponse{
			Id:        existingPaymentID,
			Amount:    invoiceAmount,
			Currency:  invoiceCurrency,
			Status:    "pending",
			Method:    method,
			CreatedAt: timestamppb.New(existingCreatedAt),
		}
		if strings.HasPrefix(method, "crypto_") {
			resp.WalletAddress = existingTxID
			asset := strings.TrimPrefix(method, "crypto_")
			var expiresAt time.Time
			if err := s.db.QueryRowContext(ctx, `
				SELECT expires_at
				FROM purser.crypto_wallets
				WHERE invoice_id = $1 AND asset = $2 AND status = 'active'
				ORDER BY created_at DESC
				LIMIT 1
			`, invoiceID, strings.ToUpper(asset)).Scan(&expiresAt); err == nil {
				resp.ExpiresAt = timestamppb.New(expiresAt)
			}
		}
		s.logger.WithFields(logging.Fields{
			"payment_id": existingPaymentID,
			"invoice_id": invoiceID,
			"method":     method,
		}).Info("Returning existing pending payment")
		return resp, nil
	}
	if err != sql.ErrNoRows {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Generate payment ID
	paymentID := uuid.New().String()
	expiresAt := time.Now().Add(30 * time.Minute)

	resp := &pb.PaymentResponse{
		Id:        paymentID,
		Amount:    invoiceAmount,
		Currency:  invoiceCurrency,
		Status:    "pending",
		Method:    method,
		ExpiresAt: timestamppb.New(expiresAt),
		CreatedAt: timestamppb.Now(),
	}

	var txID string

	// Route to appropriate payment processor
	switch method {
	case "stripe":
		paymentURL, stripeIntentID, err := s.createStripePayment(invoiceID, invoiceTenantID, invoiceAmount, invoiceCurrency)
		if err != nil {
			s.logger.WithError(err).WithFields(logging.Fields{
				"invoice_id": invoiceID,
				"method":     method,
			}).Error("Failed to create Stripe payment")
			return nil, status.Errorf(codes.Internal, "failed to create Stripe payment: %v", err)
		}
		resp.PaymentUrl = paymentURL
		txID = stripeIntentID

	case "mollie":
		paymentURL, mollieID, err := s.createMolliePayment(invoiceID, invoiceTenantID, invoiceAmount, invoiceCurrency)
		if err != nil {
			s.logger.WithError(err).WithFields(logging.Fields{
				"invoice_id": invoiceID,
				"method":     method,
			}).Error("Failed to create Mollie payment")
			return nil, status.Errorf(codes.Internal, "failed to create Mollie payment: %v", err)
		}
		resp.PaymentUrl = paymentURL
		txID = mollieID

	case "crypto_btc", "crypto_eth", "crypto_usdc", "crypto_lpt":
		asset := strings.TrimPrefix(method, "crypto_")
		walletAddr, err := s.createCryptoPayment(invoiceID, invoiceTenantID, strings.ToUpper(asset), invoiceAmount, invoiceCurrency, expiresAt)
		if err != nil {
			s.logger.WithError(err).WithFields(logging.Fields{
				"invoice_id": invoiceID,
				"method":     method,
				"asset":      asset,
			}).Error("Failed to create crypto payment")
			return nil, status.Errorf(codes.Internal, "failed to create crypto payment: %v", err)
		}
		resp.WalletAddress = walletAddr
		txID = walletAddr // Use wallet address as tx reference for crypto

	default:
		return nil, status.Errorf(codes.InvalidArgument, "unsupported payment method: %s", method)
	}

	// Create payment record
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO purser.billing_payments (id, invoice_id, method, amount, currency, tx_id, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'pending', NOW(), NOW())
	`, paymentID, invoiceID, method, invoiceAmount, invoiceCurrency, txID)
	if err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"payment_id": paymentID,
			"invoice_id": invoiceID,
		}).Error("Failed to store payment record")
		return nil, status.Errorf(codes.Internal, "failed to create payment: %v", err)
	}

	s.emitBillingEvent(ctx, eventPaymentCreated, invoiceTenantID, userID, "payment", paymentID, &pb.BillingEvent{
		PaymentId: paymentID,
		InvoiceId: invoiceID,
		Amount:    invoiceAmount,
		Currency:  invoiceCurrency,
		Provider:  method,
		Status:    "pending",
	})

	s.logger.WithFields(logging.Fields{
		"payment_id": paymentID,
		"invoice_id": invoiceID,
		"tenant_id":  invoiceTenantID,
		"method":     method,
		"amount":     invoiceAmount,
	}).Info("Payment created successfully via gRPC")

	return resp, nil
}

// getAvailablePaymentMethods returns list of configured payment methods
func (s *PurserServer) getAvailablePaymentMethods() []string {
	methods := []string{}

	// Check Stripe
	if os.Getenv("STRIPE_SECRET_KEY") != "" {
		methods = append(methods, "stripe")
	}

	// Check Mollie
	if os.Getenv("MOLLIE_API_KEY") != "" {
		methods = append(methods, "mollie")
	}

	// Check crypto (Etherscan API for ETH/ERC-20, BlockCypher for BTC)
	if s.hasHDWalletXpub() && hasAnyExplorerKey() {
		methods = append(methods, "crypto_eth", "crypto_usdc", "crypto_lpt")
	}

	return methods
}

func (s *PurserServer) hasHDWalletXpub() bool {
	var xpub string
	err := s.db.QueryRow(`SELECT xpub FROM purser.hd_wallet_state WHERE id = 1`).Scan(&xpub)
	if err != nil {
		s.logger.WithError(err).Debug("Failed to read hd_wallet_state")
		return false
	}
	return strings.TrimSpace(xpub) != ""
}

func hasAnyExplorerKey() bool {
	return os.Getenv("ETHERSCAN_API_KEY") != "" ||
		os.Getenv("BASESCAN_API_KEY") != "" ||
		os.Getenv("ARBISCAN_API_KEY") != ""
}

// createStripePayment creates a Stripe Payment Intent
func (s *PurserServer) createStripePayment(invoiceID, tenantID string, amount float64, currency string) (string, string, error) {
	stripeKey := os.Getenv("STRIPE_SECRET_KEY")
	if stripeKey == "" {
		return "", "", fmt.Errorf("Stripe not configured")
	}

	// Create Stripe Payment Intent via API
	amountCents := int64(math.Round(amount * 100))
	normalizedCurrency := strings.ToLower(currency)
	data := strings.NewReader(fmt.Sprintf(
		"amount=%d&currency=%s&metadata[invoice_id]=%s&metadata[tenant_id]=%s",
		amountCents, normalizedCurrency, invoiceID, tenantID,
	))

	req, err := http.NewRequest("POST", "https://api.stripe.com/v1/payment_intents", data)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+stripeKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("stripe API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("stripe API returned status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		ID           string `json:"id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", fmt.Errorf("failed to decode stripe response: %w", err)
	}

	if result.ID == "" || result.ClientSecret == "" {
		return "", "", fmt.Errorf("invalid stripe response: missing payment intent ID or client secret")
	}

	webappURL := strings.TrimSpace(os.Getenv("WEBAPP_PUBLIC_URL"))
	if webappURL == "" {
		return "", "", fmt.Errorf("WEBAPP_PUBLIC_URL is required")
	}
	paymentURL := fmt.Sprintf("%s/payment/stripe?client_secret=%s", webappURL, result.ClientSecret)

	return paymentURL, result.ID, nil
}

// createMolliePayment creates a Mollie payment
func (s *PurserServer) createMolliePayment(invoiceID, tenantID string, amount float64, currency string) (string, string, error) {
	mollieKey := os.Getenv("MOLLIE_API_KEY")
	if mollieKey == "" {
		return "", "", fmt.Errorf("Mollie not configured")
	}

	webappURL := strings.TrimSpace(os.Getenv("WEBAPP_PUBLIC_URL"))
	if webappURL == "" {
		return "", "", fmt.Errorf("WEBAPP_PUBLIC_URL is required")
	}
	webhookURL := strings.TrimSpace(os.Getenv("API_PUBLIC_URL"))
	if webhookURL == "" {
		webhookURL = strings.TrimSpace(os.Getenv("GATEWAY_PUBLIC_URL"))
	}
	if webhookURL == "" {
		return "", "", fmt.Errorf("API_PUBLIC_URL or GATEWAY_PUBLIC_URL is required")
	}

	payload := map[string]interface{}{
		"amount": map[string]string{
			"currency": strings.ToUpper(currency),
			"value":    fmt.Sprintf("%.2f", amount),
		},
		"description": fmt.Sprintf("Invoice %s", invoiceID),
		"redirectUrl": fmt.Sprintf("%s/billing/payment-complete", webappURL),
		"webhookUrl":  fmt.Sprintf("%s/webhooks/billing/mollie", webhookURL),
		"metadata": map[string]string{
			"purpose":      "invoice",
			"invoice_id":   invoiceID,
			"tenant_id":    tenantID,
			"reference_id": invoiceID,
		},
	}

	payloadBytes, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", "https://api.mollie.com/v2/payments", bytes.NewReader(payloadBytes))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+mollieKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("mollie API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("mollie API returned status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		ID    string                       `json:"id"`
		Links map[string]map[string]string `json:"_links"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", fmt.Errorf("failed to decode mollie response: %w", err)
	}

	checkoutURL := ""
	if checkout, ok := result.Links["checkout"]; ok {
		checkoutURL = checkout["href"]
	}

	return checkoutURL, result.ID, nil
}

// createCryptoPayment creates a crypto wallet for invoice payment using HD wallet derivation.
// This uses BIP32/BIP44 derivation from an extended public key (xpub) stored in the database.
// Private keys NEVER touch the server - sweeps happen offline with the master seed.
func (s *PurserServer) createCryptoPayment(invoiceID, tenantID, asset string, amount float64, currency string, expiresAt time.Time) (string, error) {
	// Validate asset (ETH-network only: native ETH, USDC ERC-20, LPT ERC-20)
	switch asset {
	case "ETH", "USDC", "LPT":
		// OK - supported ETH-network assets
	default:
		return "", fmt.Errorf("unsupported crypto asset: %s (only ETH, USDC, LPT supported)", asset)
	}

	// Use HD wallet to generate deposit address for this invoice
	// This derives a unique address from the xpub and stores the mapping in crypto_wallets
	walletID, walletAddress, err := s.hdwallet.GenerateDepositAddress(
		tenantID,
		"invoice",  // purpose
		&invoiceID, // invoice_id (required for invoice purpose)
		nil,        // expected_amount_cents (not used for invoices)
		asset,
		expiresAt,
	)
	if err != nil {
		return "", fmt.Errorf("failed to generate deposit address: %w", err)
	}

	s.logger.WithFields(map[string]interface{}{
		"wallet_id":  walletID,
		"invoice_id": invoiceID,
		"tenant_id":  tenantID,
		"asset":      asset,
		"address":    walletAddress,
	}).Info("Created crypto payment address for invoice")

	return walletAddress, nil
}

// GetPaymentMethods returns available payment methods for a tenant
func (s *PurserServer) GetPaymentMethods(ctx context.Context, req *pb.GetPaymentMethodsRequest) (*pb.PaymentMethodResponse, error) {
	// Return available payment methods based on configured env vars
	return &pb.PaymentMethodResponse{
		Methods: s.getAvailablePaymentMethods(),
	}, nil
}

// GetBillingStatus returns full billing status for a tenant including subscription,
// tier, pending invoices, recent payments, and usage summary
func (s *PurserServer) GetBillingStatus(ctx context.Context, req *pb.GetBillingStatusRequest) (*pb.BillingStatusResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	// Get tenant's current subscription and tier with full details
	subscription, tier, err := s.getSubscriptionAndTier(ctx, tenantID)
	if err != nil {
		s.logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to get subscription/tier")
		return nil, status.Errorf(codes.Internal, "failed to get billing status: %v", err)
	}

	// Get pending invoices
	pendingInvoices, err := s.getPendingInvoices(ctx, tenantID)
	if err != nil {
		s.logger.WithError(err).Info("Failed to get pending invoices")
	}

	// Get recent payments
	recentPayments, err := s.getRecentPayments(ctx, tenantID, 5)
	if err != nil {
		s.logger.WithError(err).Info("Failed to get recent payments")
	}

	// Get usage summary for current month
	usageSummary, err := s.getCurrentMonthUsageSummary(ctx, tenantID)
	if err != nil {
		s.logger.WithError(err).Info("Failed to get usage summary")
	}

	// Calculate outstanding amount
	var outstanding float64
	for _, inv := range pendingInvoices {
		outstanding += inv.Amount
	}

	// Build response
	resp := &pb.BillingStatusResponse{
		TenantId:          tenantID,
		Subscription:      subscription,
		Tier:              tier,
		BillingStatus:     subscription.GetStatus(),
		OutstandingAmount: outstanding,
		Currency:          tier.GetCurrency(),
		PendingInvoices:   pendingInvoices,
		RecentPayments:    recentPayments,
		UsageSummary:      usageSummary,
	}

	if subscription.GetNextBillingDate() != nil {
		resp.NextBillingDate = subscription.GetNextBillingDate()
	}

	return resp, nil
}

// getSubscriptionAndTier fetches full subscription and tier details for a tenant
func (s *PurserServer) getSubscriptionAndTier(ctx context.Context, tenantID string) (*pb.TenantSubscription, *pb.BillingTier, error) {
	s.logger.WithField("tenant_id", tenantID).Info("getSubscriptionAndTier: querying subscription for tenant")

	var subscription pb.TenantSubscription
	var tier pb.BillingTier

	// Nullable fields
	var paymentMethod, paymentReference, taxID sql.NullString
	var taxRate sql.NullFloat64
	var trialEndsAt, nextBillingDate, cancelledAt sql.NullTime
	var billingPeriodStart, billingPeriodEnd sql.NullTime
	var subStartedAt, subCreatedAt, subUpdatedAt time.Time
	var tierCreatedAt, tierUpdatedAt time.Time
	var billingModel string
	var stripeCustomerID, stripeSubscriptionID, stripeSubscriptionStatus, mollieSubscriptionID sql.NullString
	var stripePeriodEnd sql.NullTime
	var dunningAttempts sql.NullInt32

	// JSONB fields
	var customPricing, customFeatures, customAllocations, billingAddress []byte
	var bandwidthAlloc, storageAlloc, computeAlloc, features, overageRates []byte

	err := s.db.QueryRowContext(ctx, `
		SELECT
			ts.id, ts.tenant_id, ts.tier_id, ts.status, ts.billing_email,
			ts.started_at, ts.trial_ends_at, ts.next_billing_date, ts.cancelled_at,
			ts.billing_period_start, ts.billing_period_end,
			ts.custom_pricing, ts.custom_features, ts.custom_allocations,
			ts.payment_method, ts.payment_reference, ts.billing_address,
			ts.tax_id, ts.tax_rate,
			ts.billing_model,
			ts.stripe_customer_id, ts.stripe_subscription_id, ts.stripe_subscription_status, ts.stripe_current_period_end, ts.dunning_attempts,
			ts.mollie_subscription_id,
			ts.created_at, ts.updated_at,
			bt.id, bt.tier_name, bt.display_name, bt.description,
			bt.base_price, bt.currency, bt.billing_period,
			bt.bandwidth_allocation, bt.storage_allocation, bt.compute_allocation,
			bt.features, bt.support_level, bt.sla_level,
			bt.metering_enabled, bt.overage_rates, bt.is_active,
			bt.tier_level, bt.is_enterprise, bt.created_at, bt.updated_at
		FROM purser.tenant_subscriptions ts
		JOIN purser.billing_tiers bt ON ts.tier_id = bt.id
		WHERE ts.tenant_id = $1 AND ts.status != 'cancelled'
		ORDER BY ts.created_at DESC
		LIMIT 1
	`, tenantID).Scan(
		&subscription.Id, &subscription.TenantId, &subscription.TierId, &subscription.Status, &subscription.BillingEmail,
		&subStartedAt, &trialEndsAt, &nextBillingDate, &cancelledAt,
		&billingPeriodStart, &billingPeriodEnd,
		&customPricing, &customFeatures, &customAllocations,
		&paymentMethod, &paymentReference, &billingAddress,
		&taxID, &taxRate,
		&billingModel,
		&stripeCustomerID, &stripeSubscriptionID, &stripeSubscriptionStatus, &stripePeriodEnd, &dunningAttempts,
		&mollieSubscriptionID,
		&subCreatedAt, &subUpdatedAt,
		&tier.Id, &tier.TierName, &tier.DisplayName, &tier.Description,
		&tier.BasePrice, &tier.Currency, &tier.BillingPeriod,
		&bandwidthAlloc, &storageAlloc, &computeAlloc,
		&features, &tier.SupportLevel, &tier.SlaLevel,
		&tier.MeteringEnabled, &overageRates, &tier.IsActive,
		&tier.TierLevel, &tier.IsEnterprise, &tierCreatedAt, &tierUpdatedAt)

	if err == sql.ErrNoRows {
		s.logger.WithField("tenant_id", tenantID).Warn("getSubscriptionAndTier: NO SUBSCRIPTION FOUND - returning free tier fallback")
		// Return default free tier
		return &pb.TenantSubscription{
				TenantId: tenantID,
				Status:   "none",
			}, &pb.BillingTier{
				TierName:    "free",
				DisplayName: "Free Tier",
				Currency:    billing.DefaultCurrency(),
			}, nil
	}
	if err != nil {
		s.logger.WithError(err).WithField("tenant_id", tenantID).Error("getSubscriptionAndTier: query error")
		return nil, nil, err
	}

	s.logger.WithFields(map[string]interface{}{
		"tenant_id":    tenantID,
		"tier_name":    tier.TierName,
		"display_name": tier.DisplayName,
		"base_price":   tier.BasePrice,
		"status":       subscription.Status,
	}).Info("getSubscriptionAndTier: FOUND subscription")

	// Set subscription timestamps
	subscription.StartedAt = timestamppb.New(subStartedAt)
	subscription.CreatedAt = timestamppb.New(subCreatedAt)
	subscription.UpdatedAt = timestamppb.New(subUpdatedAt)
	subscription.BillingModel = billingModel
	if trialEndsAt.Valid {
		subscription.TrialEndsAt = timestamppb.New(trialEndsAt.Time)
	}
	if nextBillingDate.Valid {
		subscription.NextBillingDate = timestamppb.New(nextBillingDate.Time)
	}
	if cancelledAt.Valid {
		subscription.CancelledAt = timestamppb.New(cancelledAt.Time)
	}
	if billingPeriodStart.Valid {
		subscription.BillingPeriodStart = timestamppb.New(billingPeriodStart.Time)
	}
	if billingPeriodEnd.Valid {
		subscription.BillingPeriodEnd = timestamppb.New(billingPeriodEnd.Time)
	}

	// Set nullable strings
	if paymentMethod.Valid {
		subscription.PaymentMethod = &paymentMethod.String
	}
	if paymentReference.Valid {
		subscription.PaymentReference = &paymentReference.String
	}
	if taxID.Valid {
		subscription.TaxId = &taxID.String
	}
	if taxRate.Valid {
		subscription.TaxRate = &taxRate.Float64
	}
	if stripeCustomerID.Valid {
		subscription.StripeCustomerId = &stripeCustomerID.String
	}
	if stripeSubscriptionID.Valid {
		subscription.StripeSubscriptionId = &stripeSubscriptionID.String
	}
	if stripeSubscriptionStatus.Valid {
		subscription.StripeSubscriptionStatus = &stripeSubscriptionStatus.String
	}
	if stripePeriodEnd.Valid {
		subscription.StripeCurrentPeriodEnd = timestamppb.New(stripePeriodEnd.Time)
	}
	if dunningAttempts.Valid {
		subscription.DunningAttempts = dunningAttempts.Int32
	}
	if mollieSubscriptionID.Valid {
		subscription.MollieSubscriptionId = &mollieSubscriptionID.String
	}

	// Parse JSONB fields for subscription
	subscription.CustomPricing = scanCustomPricing(customPricing)
	subscription.CustomFeatures = scanBillingFeatures(customFeatures)
	subscription.CustomAllocations = scanAllocationDetails(customAllocations)
	subscription.BillingAddress = scanBillingAddress(billingAddress)

	// Set tier timestamps and JSONB fields
	tier.CreatedAt = timestamppb.New(tierCreatedAt)
	tier.UpdatedAt = timestamppb.New(tierUpdatedAt)
	tier.BandwidthAllocation = scanAllocationDetails(bandwidthAlloc)
	tier.StorageAllocation = scanAllocationDetails(storageAlloc)
	tier.ComputeAllocation = scanAllocationDetails(computeAlloc)
	tier.Features = scanBillingFeatures(features)
	tier.OverageRates = scanOverageRates(overageRates)

	return &subscription, &tier, nil
}

// getPendingInvoices fetches pending invoices for a tenant
func (s *PurserServer) getPendingInvoices(ctx context.Context, tenantID string) ([]*pb.Invoice, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, tenant_id, amount, base_amount, metered_amount, currency, status,
		       due_date, paid_at, usage_details, created_at, updated_at, period_start, period_end
		FROM purser.billing_invoices
		WHERE tenant_id = $1 AND status = 'pending'
		ORDER BY due_date ASC
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var invoices []*pb.Invoice
	for rows.Next() {
		var inv pb.Invoice
		var dueDate, createdAt, updatedAt time.Time
		var paidAt sql.NullTime
		var usageDetails []byte
		var baseAmount, meteredAmount sql.NullFloat64
		var periodStart, periodEnd sql.NullTime

		err := rows.Scan(&inv.Id, &inv.TenantId, &inv.Amount, &baseAmount, &meteredAmount,
			&inv.Currency, &inv.Status, &dueDate, &paidAt, &usageDetails, &createdAt, &updatedAt, &periodStart, &periodEnd)
		if err != nil {
			continue
		}

		inv.DueDate = timestamppb.New(dueDate)
		inv.CreatedAt = timestamppb.New(createdAt)
		inv.UpdatedAt = timestamppb.New(updatedAt)
		if paidAt.Valid {
			inv.PaidAt = timestamppb.New(paidAt.Time)
		}
		if baseAmount.Valid {
			inv.BaseAmount = baseAmount.Float64
		}
		if meteredAmount.Valid {
			inv.MeteredAmount = meteredAmount.Float64
		}
		if periodStart.Valid {
			inv.PeriodStart = timestamppb.New(periodStart.Time)
		}
		if periodEnd.Valid {
			inv.PeriodEnd = timestamppb.New(periodEnd.Time)
		}
		if len(usageDetails) > 0 {
			var details map[string]interface{}
			if json.Unmarshal(usageDetails, &details) == nil {
				inv.UsageDetails = mapToProtoStruct(details)
			}
		}

		invoices = append(invoices, &inv)
	}

	return invoices, nil
}

// getRecentPayments fetches recent payments for a tenant
func (s *PurserServer) getRecentPayments(ctx context.Context, tenantID string, limit int) ([]*pb.Payment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT bp.id, bp.invoice_id, bp.method, bp.amount, bp.currency,
		       bp.tx_id, bp.status, bp.confirmed_at, bp.created_at, bp.updated_at
		FROM purser.billing_payments bp
		JOIN purser.billing_invoices bi ON bp.invoice_id = bi.id
		WHERE bi.tenant_id = $1
		ORDER BY bp.created_at DESC
		LIMIT $2
	`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var payments []*pb.Payment
	for rows.Next() {
		var pay pb.Payment
		var txID sql.NullString
		var confirmedAt sql.NullTime
		var createdAt, updatedAt time.Time

		err := rows.Scan(&pay.Id, &pay.InvoiceId, &pay.Method, &pay.Amount, &pay.Currency,
			&txID, &pay.Status, &confirmedAt, &createdAt, &updatedAt)
		if err != nil {
			continue
		}

		if txID.Valid {
			pay.TxId = txID.String
		}
		pay.CreatedAt = timestamppb.New(createdAt)
		pay.UpdatedAt = timestamppb.New(updatedAt)
		if confirmedAt.Valid {
			pay.ConfirmedAt = timestamppb.New(confirmedAt.Time)
		}

		payments = append(payments, &pay)
	}

	return payments, nil
}

func (s *PurserServer) getSubscriptionPeriod(ctx context.Context, tenantID string, now time.Time) (time.Time, time.Time) {
	var start, end sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT billing_period_start, billing_period_end
		FROM purser.tenant_subscriptions
		WHERE tenant_id = $1 AND status = 'active'
		ORDER BY created_at DESC
		LIMIT 1
	`, tenantID).Scan(&start, &end)
	if err == nil && start.Valid && end.Valid && end.Time.After(start.Time) {
		return start.Time, end.Time
	}

	periodStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	periodEnd := periodStart.AddDate(0, 1, 0)
	return periodStart, periodEnd
}

// getCurrentMonthUsageSummary gets aggregated usage for current billing period
func (s *PurserServer) getCurrentMonthUsageSummary(ctx context.Context, tenantID string) (*pb.UsageSummary, error) {
	now := time.Now()
	periodStart, periodEnd := s.getSubscriptionPeriod(ctx, tenantID, now)

	var streamHours, viewerHours, egressGb, peakBandwidthMbps, averageStorageGb float64
	var livepeerH264, livepeerVp9, livepeerAv1, livepeerHevc float64
	var nativeAvH264, nativeAvVp9, nativeAvAv1, nativeAvHevc, nativeAvAac, nativeAvOpus float64
	var totalStreams, totalViewers, maxViewers, uniqueUsers int32

	err := s.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN usage_type = 'stream_hours' THEN usage_value ELSE 0 END), 0) as stream_hours,
			COALESCE(SUM(CASE WHEN usage_type = 'viewer_hours' THEN usage_value ELSE 0 END), 0) as viewer_hours,
			COALESCE(SUM(CASE WHEN usage_type = 'egress_gb' THEN usage_value ELSE 0 END), 0) as egress_gb,
			COALESCE(MAX(CASE WHEN usage_type = 'peak_bandwidth_mbps' THEN usage_value ELSE 0 END), 0) as peak_bandwidth_mbps,
			COALESCE(AVG(CASE WHEN usage_type = 'average_storage_gb' THEN usage_value END), 0) as average_storage_gb,
			COALESCE(SUM(CASE WHEN usage_type = 'livepeer_h264_seconds' THEN usage_value ELSE 0 END), 0) as livepeer_h264_seconds,
			COALESCE(SUM(CASE WHEN usage_type = 'livepeer_vp9_seconds' THEN usage_value ELSE 0 END), 0) as livepeer_vp9_seconds,
			COALESCE(SUM(CASE WHEN usage_type = 'livepeer_av1_seconds' THEN usage_value ELSE 0 END), 0) as livepeer_av1_seconds,
			COALESCE(SUM(CASE WHEN usage_type = 'livepeer_hevc_seconds' THEN usage_value ELSE 0 END), 0) as livepeer_hevc_seconds,
			COALESCE(SUM(CASE WHEN usage_type = 'native_av_h264_seconds' THEN usage_value ELSE 0 END), 0) as native_av_h264_seconds,
			COALESCE(SUM(CASE WHEN usage_type = 'native_av_vp9_seconds' THEN usage_value ELSE 0 END), 0) as native_av_vp9_seconds,
			COALESCE(SUM(CASE WHEN usage_type = 'native_av_av1_seconds' THEN usage_value ELSE 0 END), 0) as native_av_av1_seconds,
			COALESCE(SUM(CASE WHEN usage_type = 'native_av_hevc_seconds' THEN usage_value ELSE 0 END), 0) as native_av_hevc_seconds,
			COALESCE(SUM(CASE WHEN usage_type = 'native_av_aac_seconds' THEN usage_value ELSE 0 END), 0) as native_av_aac_seconds,
			COALESCE(SUM(CASE WHEN usage_type = 'native_av_opus_seconds' THEN usage_value ELSE 0 END), 0) as native_av_opus_seconds,
			COALESCE(MAX(CASE WHEN usage_type = 'total_streams' THEN usage_value ELSE 0 END), 0)::int as total_streams,
			COALESCE(MAX(CASE WHEN usage_type = 'total_viewers' THEN usage_value ELSE 0 END), 0)::int as total_viewers,
			COALESCE(MAX(CASE WHEN usage_type = 'max_viewers' THEN usage_value ELSE 0 END), 0)::int as max_viewers,
			COALESCE(MAX(CASE WHEN usage_type = 'unique_users' THEN usage_value ELSE 0 END), 0)::int as unique_users
		FROM purser.usage_records
		WHERE tenant_id = $1 AND period_start < $3 AND period_end > $2
	`, tenantID, periodStart, periodEnd).Scan(
		&streamHours, &viewerHours, &egressGb, &peakBandwidthMbps, &averageStorageGb,
		&livepeerH264, &livepeerVp9, &livepeerAv1, &livepeerHevc,
		&nativeAvH264, &nativeAvVp9, &nativeAvAv1, &nativeAvHevc, &nativeAvAac, &nativeAvOpus,
		&totalStreams, &totalViewers, &maxViewers, &uniqueUsers,
	)
	if err != nil {
		return nil, err
	}

	period := periodStart.Format(time.RFC3339) + "/" + periodEnd.Format(time.RFC3339)
	granularity := "hourly"
	if duration := periodEnd.Sub(periodStart); duration >= 28*24*time.Hour {
		granularity = "monthly"
	} else if duration >= 24*time.Hour {
		granularity = "daily"
	}

	return &pb.UsageSummary{
		TenantId:            tenantID,
		Period:              period,
		Granularity:         granularity,
		StreamHours:         streamHours,
		EgressGb:            egressGb,
		PeakBandwidthMbps:   peakBandwidthMbps,
		AverageStorageGb:    averageStorageGb,
		LivepeerH264Seconds: livepeerH264,
		LivepeerVp9Seconds:  livepeerVp9,
		LivepeerAv1Seconds:  livepeerAv1,
		LivepeerHevcSeconds: livepeerHevc,
		NativeAvH264Seconds: nativeAvH264,
		NativeAvVp9Seconds:  nativeAvVp9,
		NativeAvAv1Seconds:  nativeAvAv1,
		NativeAvHevcSeconds: nativeAvHevc,
		NativeAvAacSeconds:  nativeAvAac,
		NativeAvOpusSeconds: nativeAvOpus,
		TotalStreams:        totalStreams,
		TotalViewers:        totalViewers,
		ViewerHours:         viewerHours,
		MaxViewers:          maxViewers,
		UniqueUsers:         uniqueUsers,
	}, nil
}

// scanCustomPricing scans JSONB into CustomPricing proto
func scanCustomPricing(data []byte) *pb.CustomPricing {
	if len(data) == 0 {
		return nil
	}
	var raw struct {
		BasePrice    float64 `json:"base_price"`
		DiscountRate float64 `json:"discount_rate"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}
	return &pb.CustomPricing{
		BasePrice:    raw.BasePrice,
		DiscountRate: raw.DiscountRate,
	}
}

// scanBillingAddress scans JSONB into BillingAddress proto
func scanBillingAddress(data []byte) *pb.BillingAddress {
	if len(data) == 0 {
		return nil
	}
	var raw struct {
		Street     string `json:"street"`
		City       string `json:"city"`
		State      string `json:"state"`
		PostalCode string `json:"postal_code"`
		Country    string `json:"country"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}
	return &pb.BillingAddress{
		Street:     raw.Street,
		City:       raw.City,
		State:      raw.State,
		PostalCode: raw.PostalCode,
		Country:    raw.Country,
	}
}

// ============================================================================
// USAGE SERVICE - GetTenantUsage
// ============================================================================

// GetTenantUsage returns aggregated usage for a tenant
func (s *PurserServer) GetTenantUsage(ctx context.Context, req *pb.TenantUsageRequest) (*pb.TenantUsageResponse, error) {
	tenantID := req.GetTenantId()
	startDate := req.GetStartDate()
	endDate := req.GetEndDate()

	ctxTenantID := middleware.GetTenantID(ctx)
	isServiceCall := middleware.IsServiceCall(ctx)
	if !isServiceCall {
		if ctxTenantID == "" {
			return nil, status.Error(codes.PermissionDenied, "tenant context required")
		}
		if tenantID != "" && tenantID != ctxTenantID {
			return nil, status.Error(codes.PermissionDenied, "cross-tenant access denied")
		}
		tenantID = ctxTenantID
	}
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}
	if startDate == "" || endDate == "" {
		return nil, status.Error(codes.InvalidArgument, "start_date and end_date required")
	}

	// Query aggregated usage by type using precise timestamp boundaries
	rows, err := s.db.QueryContext(ctx, `
		SELECT usage_type, SUM(usage_value) as total
		FROM purser.usage_records
		WHERE tenant_id = $1
		  AND period_start < ($3::date + INTERVAL '1 day')
		  AND period_end > $2::date
		GROUP BY usage_type
	`, tenantID, startDate, endDate)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	usage := make(map[string]float64)
	costs := make(map[string]float64)
	var totalCost float64

	var viewerHours, averageStorageGb, gpuHours float64

	for rows.Next() {
		var usageType string
		var total float64
		if err := rows.Scan(&usageType, &total); err != nil {
			continue
		}
		usage[usageType] = total
		switch usageType {
		case "viewer_hours":
			viewerHours = total
		case "average_storage_gb":
			averageStorageGb = total
		case "gpu_hours":
			gpuHours = total
		}
	}

	// Compute usage-based costs from tier/subscription configuration (metered only)
	if subscription, tier, err := s.getSubscriptionAndTier(ctx, tenantID); err == nil && tier != nil && tier.GetMeteringEnabled() {
		effectiveBandwidthAllocation := tier.GetBandwidthAllocation()
		effectiveStorageAllocation := tier.GetStorageAllocation()
		effectiveOverageRates := tier.GetOverageRates()

		if subscription != nil {
			if subscription.CustomAllocations != nil && subscription.CustomAllocations.Limit != nil {
				effectiveBandwidthAllocation = subscription.CustomAllocations
			}
			if subscription.CustomPricing != nil && subscription.CustomPricing.OverageRates != nil {
				if subscription.CustomPricing.OverageRates.Bandwidth != nil && subscription.CustomPricing.OverageRates.Bandwidth.UnitPrice > 0 {
					effectiveOverageRates.Bandwidth = subscription.CustomPricing.OverageRates.Bandwidth
				}
				if subscription.CustomPricing.OverageRates.Storage != nil && subscription.CustomPricing.OverageRates.Storage.UnitPrice > 0 {
					effectiveOverageRates.Storage = subscription.CustomPricing.OverageRates.Storage
				}
				if subscription.CustomPricing.OverageRates.Compute != nil && subscription.CustomPricing.OverageRates.Compute.UnitPrice > 0 {
					effectiveOverageRates.Compute = subscription.CustomPricing.OverageRates.Compute
				}
			}
		}

		// Bandwidth: billed on delivered minutes (viewer_hours * 60)
		if effectiveBandwidthAllocation != nil && effectiveOverageRates != nil && effectiveOverageRates.Bandwidth != nil {
			if effectiveBandwidthAllocation.Limit != nil && effectiveOverageRates.Bandwidth.UnitPrice > 0 {
				deliveredMinutes := viewerHours * 60
				billableMinutes := deliveredMinutes - *effectiveBandwidthAllocation.Limit
				if billableMinutes > 0 {
					cost := billableMinutes * effectiveOverageRates.Bandwidth.UnitPrice
					costs["viewer_hours"] = cost
					totalCost += cost
				}
			}
		}

		storageUsage := averageStorageGb
		if effectiveStorageAllocation != nil && effectiveOverageRates != nil && effectiveOverageRates.Storage != nil {
			if effectiveStorageAllocation.Limit != nil && effectiveOverageRates.Storage.UnitPrice > 0 {
				billableStorage := storageUsage - *effectiveStorageAllocation.Limit
				if billableStorage > 0 {
					cost := billableStorage * effectiveOverageRates.Storage.UnitPrice
					costs["average_storage_gb"] = cost
					totalCost += cost
				}
			}
		}

		// Compute: bill all GPU hours if configured
		if gpuHours > 0 && effectiveOverageRates != nil && effectiveOverageRates.Compute != nil && effectiveOverageRates.Compute.UnitPrice > 0 {
			cost := gpuHours * effectiveOverageRates.Compute.UnitPrice
			costs["gpu_hours"] = cost
			totalCost += cost
		}
	}

	return &pb.TenantUsageResponse{
		TenantId:      tenantID,
		BillingPeriod: startDate + " to " + endDate,
		Usage:         usage,
		Costs:         costs,
		TotalCost:     totalCost,
		Currency:      billing.DefaultCurrency(),
	}, nil
}

// ============================================================================
// CLUSTER PRICING SERVICE
// ============================================================================

// GetClusterPricing retrieves pricing configuration for a cluster
func (s *PurserServer) GetClusterPricing(ctx context.Context, req *pb.GetClusterPricingRequest) (*pb.ClusterPricing, error) {
	clusterID := req.GetClusterId()
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}

	var pricing pb.ClusterPricing
	var basePrice sql.NullFloat64
	var currency sql.NullString
	var stripeProductID, stripePriceIDMonthly, stripeMeterID sql.NullString
	var meteredRatesJSON, defaultQuotasJSON []byte
	var createdAt, updatedAt time.Time

	err := s.db.QueryRowContext(ctx, `
		SELECT id, cluster_id, pricing_model,
		       stripe_product_id, stripe_price_id_monthly, stripe_meter_id,
		       base_price, currency, metered_rates,
		       required_tier_level, is_platform_official, allow_free_tier,
		       default_quotas, created_at, updated_at
		FROM purser.cluster_pricing
		WHERE cluster_id = $1
	`, clusterID).Scan(
		&pricing.Id, &pricing.ClusterId, &pricing.PricingModel,
		&stripeProductID, &stripePriceIDMonthly, &stripeMeterID,
		&basePrice, &currency, &meteredRatesJSON,
		&pricing.RequiredTierLevel, &pricing.IsPlatformOfficial, &pricing.AllowFreeTier,
		&defaultQuotasJSON, &createdAt, &updatedAt,
	)
	if err == sql.ErrNoRows {
		// Return default pricing for clusters without explicit config
		return &pb.ClusterPricing{
			ClusterId:          clusterID,
			PricingModel:       "tier_inherit",
			Currency:           "EUR",
			RequiredTierLevel:  0,
			IsPlatformOfficial: false,
			AllowFreeTier:      false,
		}, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Map optional fields
	if stripeProductID.Valid {
		pricing.StripeProductId = &stripeProductID.String
	}
	if stripePriceIDMonthly.Valid {
		pricing.StripePriceIdMonthly = &stripePriceIDMonthly.String
	}
	if stripeMeterID.Valid {
		pricing.StripeMeterId = &stripeMeterID.String
	}
	if basePrice.Valid {
		pricing.BasePrice = fmt.Sprintf("%.2f", basePrice.Float64)
	}
	if currency.Valid {
		pricing.Currency = currency.String
	} else {
		pricing.Currency = "EUR"
	}

	// Parse JSONB fields
	if len(meteredRatesJSON) > 0 {
		pricing.MeteredRates, _ = structpb.NewStruct(jsonToMap(meteredRatesJSON))
	}
	if len(defaultQuotasJSON) > 0 {
		pricing.DefaultQuotas, _ = structpb.NewStruct(jsonToMap(defaultQuotasJSON))
	}

	pricing.CreatedAt = timestamppb.New(createdAt)
	pricing.UpdatedAt = timestamppb.New(updatedAt)

	return &pricing, nil
}

// GetClustersPricingBatch retrieves pricing configuration for multiple clusters
func (s *PurserServer) GetClustersPricingBatch(ctx context.Context, req *pb.GetClustersPricingBatchRequest) (*pb.GetClustersPricingBatchResponse, error) {
	clusterIDs := req.GetClusterIds()
	if len(clusterIDs) == 0 {
		return &pb.GetClustersPricingBatchResponse{
			Pricings: make(map[string]*pb.ClusterPricing),
		}, nil
	}

	tenantID := req.GetTenantId()

	// Resolve tenant tier level for eligibility checks (default to 0 if not found)
	var tenantTierLevel int32
	if tenantID != "" {
		err := s.db.QueryRowContext(ctx, `
			SELECT COALESCE(bt.tier_level, 0)
			FROM purser.tenant_subscriptions ts
			JOIN purser.billing_tiers bt ON ts.tier_id = bt.id
			WHERE ts.tenant_id = $1 AND ts.status = 'active'
		`, tenantID).Scan(&tenantTierLevel)
		if err != nil && err != sql.ErrNoRows {
			s.logger.WithError(err).Warn("Failed to get tenant tier level for batch pricing")
		}
	}

	// Build placeholder string for IN clause
	placeholders := make([]string, len(clusterIDs))
	args := make([]interface{}, len(clusterIDs))
	for i, id := range clusterIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}

	query := fmt.Sprintf(`
		SELECT id, cluster_id, pricing_model,
		       stripe_product_id, stripe_price_id_monthly, stripe_meter_id,
		       base_price, currency, metered_rates,
		       required_tier_level, is_platform_official, allow_free_tier,
		       default_quotas, created_at, updated_at
		FROM purser.cluster_pricing
		WHERE cluster_id IN (%s)
	`, strings.Join(placeholders, ", "))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	result := make(map[string]*pb.ClusterPricing)

	for rows.Next() {
		var pricing pb.ClusterPricing
		var basePrice sql.NullFloat64
		var currency sql.NullString
		var stripeProductID, stripePriceIDMonthly, stripeMeterID sql.NullString
		var meteredRatesJSON, defaultQuotasJSON []byte
		var createdAt, updatedAt time.Time

		if err := rows.Scan(
			&pricing.Id, &pricing.ClusterId, &pricing.PricingModel,
			&stripeProductID, &stripePriceIDMonthly, &stripeMeterID,
			&basePrice, &currency, &meteredRatesJSON,
			&pricing.RequiredTierLevel, &pricing.IsPlatformOfficial, &pricing.AllowFreeTier,
			&defaultQuotasJSON, &createdAt, &updatedAt,
		); err != nil {
			return nil, status.Errorf(codes.Internal, "scan error: %v", err)
		}

		// Map optional fields
		if stripeProductID.Valid {
			pricing.StripeProductId = &stripeProductID.String
		}
		if stripePriceIDMonthly.Valid {
			pricing.StripePriceIdMonthly = &stripePriceIDMonthly.String
		}
		if stripeMeterID.Valid {
			pricing.StripeMeterId = &stripeMeterID.String
		}
		if basePrice.Valid {
			pricing.BasePrice = fmt.Sprintf("%.2f", basePrice.Float64)
		}
		if currency.Valid {
			pricing.Currency = currency.String
		} else {
			pricing.Currency = "EUR"
		}

		// Parse JSONB fields
		if len(meteredRatesJSON) > 0 {
			pricing.MeteredRates, _ = structpb.NewStruct(jsonToMap(meteredRatesJSON))
		}
		if len(defaultQuotasJSON) > 0 {
			pricing.DefaultQuotas, _ = structpb.NewStruct(jsonToMap(defaultQuotasJSON))
		}

		pricing.CreatedAt = timestamppb.New(createdAt)
		pricing.UpdatedAt = timestamppb.New(updatedAt)

		applyEligibility(tenantID, tenantTierLevel, &pricing)
		result[pricing.ClusterId] = &pricing
	}

	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "rows error: %v", err)
	}

	// Flag clusters without explicit pricing configuration
	for _, clusterID := range clusterIDs {
		if _, found := result[clusterID]; !found {
			s.logger.WithField("cluster_id", clusterID).Warn("Missing cluster pricing configuration")
			pricing := &pb.ClusterPricing{
				ClusterId:    clusterID,
				PricingModel: "tier_inherit",
				IsEligible:   false,
			}
			denial := "Pricing not configured. Contact support."
			pricing.DenialReason = &denial
			result[clusterID] = pricing
		}
	}

	return &pb.GetClustersPricingBatchResponse{
		Pricings: result,
	}, nil
}

func applyEligibility(tenantID string, tenantTierLevel int32, pricing *pb.ClusterPricing) {
	if pricing == nil {
		return
	}
	if tenantID == "" {
		pricing.IsEligible = true
		pricing.DenialReason = nil
		return
	}

	if tenantTierLevel < pricing.RequiredTierLevel {
		pricing.IsEligible = false
		denial := "Requires a higher billing tier. Contact us to upgrade."
		pricing.DenialReason = &denial
		return
	}
	if pricing.IsPlatformOfficial && !pricing.AllowFreeTier && tenantTierLevel == 0 {
		pricing.IsEligible = false
		denial := "Requires a higher billing tier. Contact us to upgrade."
		pricing.DenialReason = &denial
		return
	}

	pricing.IsEligible = true
	pricing.DenialReason = nil
}

// SetClusterPricing creates or updates pricing configuration for a cluster
func (s *PurserServer) SetClusterPricing(ctx context.Context, req *pb.SetClusterPricingRequest) (*pb.ClusterPricing, error) {
	clusterID := req.GetClusterId()
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}

	pricingModel := req.GetPricingModel()
	if pricingModel == "" {
		pricingModel = "tier_inherit"
	}

	// Validate pricing model
	validModels := []string{"free_unmetered", "metered", "monthly", "tier_inherit", "custom"}
	if !slices.Contains(validModels, pricingModel) {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pricing_model: %s", pricingModel)
	}

	// Build upsert query
	var basePrice sql.NullFloat64
	if req.BasePrice != nil {
		var f float64
		if _, err := fmt.Sscanf(*req.BasePrice, "%f", &f); err == nil {
			basePrice = sql.NullFloat64{Float64: f, Valid: true}
		}
	}

	currency := "EUR"
	if req.Currency != nil {
		currency = *req.Currency
	}

	requiredTierLevel := int32(0)
	if req.RequiredTierLevel != nil {
		requiredTierLevel = *req.RequiredTierLevel
	}

	allowFreeTier := false
	if req.AllowFreeTier != nil {
		allowFreeTier = *req.AllowFreeTier
	}

	// Marshal JSONB fields
	var meteredRatesBytes, defaultQuotasBytes []byte
	if req.MeteredRates != nil {
		meteredRatesBytes, _ = json.Marshal(req.MeteredRates.AsMap())
	} else {
		meteredRatesBytes = []byte("{}")
	}
	if req.DefaultQuotas != nil {
		defaultQuotasBytes, _ = json.Marshal(req.DefaultQuotas.AsMap())
	} else {
		defaultQuotasBytes = []byte("{}")
	}

	// Upsert cluster pricing
	var pricingID string
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO purser.cluster_pricing (
			cluster_id, pricing_model, base_price, currency,
			required_tier_level, allow_free_tier, metered_rates, default_quotas,
			stripe_product_id, stripe_price_id_monthly, stripe_meter_id,
			updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NOW())
		ON CONFLICT (cluster_id) DO UPDATE SET
			pricing_model = EXCLUDED.pricing_model,
			base_price = EXCLUDED.base_price,
			currency = EXCLUDED.currency,
			required_tier_level = EXCLUDED.required_tier_level,
			allow_free_tier = EXCLUDED.allow_free_tier,
			metered_rates = EXCLUDED.metered_rates,
			default_quotas = EXCLUDED.default_quotas,
			stripe_product_id = COALESCE(EXCLUDED.stripe_product_id, purser.cluster_pricing.stripe_product_id),
			stripe_price_id_monthly = COALESCE(EXCLUDED.stripe_price_id_monthly, purser.cluster_pricing.stripe_price_id_monthly),
			stripe_meter_id = COALESCE(EXCLUDED.stripe_meter_id, purser.cluster_pricing.stripe_meter_id),
			updated_at = NOW()
		RETURNING id
	`, clusterID, pricingModel, basePrice, currency,
		requiredTierLevel, allowFreeTier, meteredRatesBytes, defaultQuotasBytes,
		req.StripeProductId, req.StripePriceIdMonthly, req.StripeMeterId,
	).Scan(&pricingID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to set cluster pricing: %v", err)
	}

	// Return the updated pricing
	return s.GetClusterPricing(ctx, &pb.GetClusterPricingRequest{ClusterId: clusterID})
}

// ListClusterPricings returns pricing configs for clusters owned by a tenant
func (s *PurserServer) ListClusterPricings(ctx context.Context, req *pb.ListClusterPricingsRequest) (*pb.ListClusterPricingsResponse, error) {
	ownerTenantID := req.GetOwnerTenantId()

	// Build query - if no owner specified, list all (admin use case)
	query := `
		SELECT id, cluster_id, pricing_model,
		       stripe_product_id, stripe_price_id_monthly, stripe_meter_id,
		       base_price, currency, metered_rates,
		       required_tier_level, is_platform_official, allow_free_tier,
		       default_quotas, created_at, updated_at
		FROM purser.cluster_pricing
	`
	var args []interface{}
	if ownerTenantID != "" {
		// Get cluster IDs owned by this tenant via Quartermaster gRPC (not direct DB access)
		if s.quartermasterClient == nil {
			return nil, status.Error(codes.Unavailable, "quartermaster client not configured")
		}

		var clusterIDs []string
		paginationReq := &pb.CursorPaginationRequest{First: 200}

		for {
			clustersResp, err := s.quartermasterClient.ListClustersByOwner(ctx, ownerTenantID, paginationReq)
			if err != nil {
				s.logger.WithFields(logging.Fields{
					"owner_tenant_id": ownerTenantID,
					"error":           err,
				}).Error("Failed to get clusters from Quartermaster")
				return nil, status.Errorf(codes.Internal, "failed to get clusters: %v", err)
			}

			for _, cluster := range clustersResp.Clusters {
				if cluster.OwnerTenantId != nil && *cluster.OwnerTenantId == ownerTenantID {
					clusterIDs = append(clusterIDs, cluster.ClusterId)
				}
			}

			pagination := clustersResp.GetPagination()
			if pagination == nil || !pagination.GetHasNextPage() || pagination.GetEndCursor() == "" {
				break
			}
			endCursor := pagination.GetEndCursor()
			paginationReq = &pb.CursorPaginationRequest{
				First: paginationReq.First,
				After: &endCursor,
			}
		}

		if len(clusterIDs) == 0 {
			// No clusters owned by this tenant - legitimate empty result
			return &pb.ListClusterPricingsResponse{Pricings: []*pb.ClusterPricing{}}, nil
		}

		query += " WHERE cluster_id = ANY($1)"
		args = append(args, pq.Array(clusterIDs))
	}

	query += " ORDER BY created_at DESC"

	// Apply pagination
	limit := int32(50)
	if req.GetPagination() != nil && req.GetPagination().GetFirst() > 0 {
		limit = req.GetPagination().GetFirst()
	}
	query += fmt.Sprintf(" LIMIT %d", limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var pricings []*pb.ClusterPricing
	for rows.Next() {
		var pricing pb.ClusterPricing
		var basePrice sql.NullFloat64
		var currency sql.NullString
		var stripeProductID, stripePriceIDMonthly, stripeMeterID sql.NullString
		var meteredRatesJSON, defaultQuotasJSON []byte
		var createdAt, updatedAt time.Time

		if err := rows.Scan(
			&pricing.Id, &pricing.ClusterId, &pricing.PricingModel,
			&stripeProductID, &stripePriceIDMonthly, &stripeMeterID,
			&basePrice, &currency, &meteredRatesJSON,
			&pricing.RequiredTierLevel, &pricing.IsPlatformOfficial, &pricing.AllowFreeTier,
			&defaultQuotasJSON, &createdAt, &updatedAt,
		); err != nil {
			continue
		}

		// Map optional fields
		if stripeProductID.Valid {
			pricing.StripeProductId = &stripeProductID.String
		}
		if stripePriceIDMonthly.Valid {
			pricing.StripePriceIdMonthly = &stripePriceIDMonthly.String
		}
		if stripeMeterID.Valid {
			pricing.StripeMeterId = &stripeMeterID.String
		}
		if basePrice.Valid {
			pricing.BasePrice = fmt.Sprintf("%.2f", basePrice.Float64)
		}
		if currency.Valid {
			pricing.Currency = currency.String
		} else {
			pricing.Currency = "EUR"
		}

		if len(meteredRatesJSON) > 0 {
			pricing.MeteredRates, _ = structpb.NewStruct(jsonToMap(meteredRatesJSON))
		}
		if len(defaultQuotasJSON) > 0 {
			pricing.DefaultQuotas, _ = structpb.NewStruct(jsonToMap(defaultQuotasJSON))
		}

		pricing.CreatedAt = timestamppb.New(createdAt)
		pricing.UpdatedAt = timestamppb.New(updatedAt)

		pricings = append(pricings, &pricing)
	}

	resp := &pb.ListClusterPricingsResponse{Pricings: pricings}
	if int32(len(pricings)) > limit {
		resp.Pricings = pricings[:limit]
		resp.Pagination = &pb.CursorPaginationResponse{HasNextPage: true}
	}

	return resp, nil
}

// CheckClusterAccess verifies if a tenant can subscribe to a cluster
func (s *PurserServer) CheckClusterAccess(ctx context.Context, req *pb.CheckClusterAccessRequest) (*pb.CheckClusterAccessResponse, error) {
	tenantID := req.GetTenantId()
	clusterID := req.GetClusterId()

	if tenantID == "" || clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and cluster_id required")
	}

	// Get tenant's billing tier level from Purser's own tables (no cross-service DB access)
	var tenantTierLevel int32
	err := s.db.QueryRowContext(ctx, `
			SELECT COALESCE(bt.tier_level, 0)
			FROM purser.tenant_subscriptions ts
			JOIN purser.billing_tiers bt ON ts.tier_id = bt.id
			WHERE ts.tenant_id = $1 AND ts.status = 'active'
		`, tenantID).Scan(&tenantTierLevel)
	if err == sql.ErrNoRows {
		// No active subscription = free tier (level 0)
		tenantTierLevel = 0
	} else if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Get cluster pricing config
	pricing, err := s.GetClusterPricing(ctx, &pb.GetClusterPricingRequest{ClusterId: clusterID})
	if err != nil {
		return nil, err
	}

	resp := &pb.CheckClusterAccessResponse{
		TenantTierLevel:   tenantTierLevel,
		RequiredTierLevel: pricing.RequiredTierLevel,
		PricingModel:      pricing.PricingModel,
	}

	// Check tier requirement
	if tenantTierLevel < pricing.RequiredTierLevel {
		resp.Allowed = false
		resp.DenialReason = fmt.Sprintf("requires tier level %d, you have %d", pricing.RequiredTierLevel, tenantTierLevel)
		return resp, nil
	}

	// Check free tier access for platform clusters
	if pricing.IsPlatformOfficial && !pricing.AllowFreeTier && tenantTierLevel == 0 {
		resp.Allowed = false
		resp.DenialReason = "this platform cluster requires a paid subscription"
		return resp, nil
	}

	resp.Allowed = true

	// Estimate cost for display
	switch pricing.PricingModel {
	case "free_unmetered":
		resp.EstimatedCost = "Free"
	case "monthly":
		resp.EstimatedCost = fmt.Sprintf("%s %s/month", pricing.BasePrice, pricing.Currency)
	case "metered":
		resp.EstimatedCost = "Usage-based pricing"
	case "tier_inherit":
		resp.EstimatedCost = "Included in your plan"
	case "custom":
		resp.EstimatedCost = "Contact for pricing"
	}

	return resp, nil
}

// CreateClusterSubscription creates a subscription for a tenant to a cluster
// Note: Invite-based subscriptions go through Quartermaster.AcceptClusterInvite instead
func (s *PurserServer) CreateClusterSubscription(ctx context.Context, req *pb.CreateClusterSubscriptionRequest) (*pb.ClusterSubscriptionResponse, error) {
	tenantID := req.GetTenantId()
	clusterID := req.GetClusterId()

	if tenantID == "" || clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and cluster_id required")
	}

	// Check if tenant can access this cluster
	accessResp, err := s.CheckClusterAccess(ctx, &pb.CheckClusterAccessRequest{
		TenantId:  tenantID,
		ClusterId: clusterID,
	})
	if err != nil {
		return nil, err
	}
	if !accessResp.Allowed {
		return nil, status.Errorf(codes.PermissionDenied, "access denied: %s", accessResp.DenialReason)
	}

	// Get cluster pricing to determine subscription type
	pricing, err := s.GetClusterPricing(ctx, &pb.GetClusterPricingRequest{ClusterId: clusterID})
	if err != nil {
		return nil, err
	}

	// For free/tier_inherit clusters, create access immediately via Quartermaster
	// For paid clusters, we'd create a Stripe subscription
	resp := &pb.ClusterSubscriptionResponse{
		ClusterId: clusterID,
		TenantId:  tenantID,
	}

	switch pricing.PricingModel {
	case "free_unmetered", "tier_inherit":
		// Create tenant_cluster_access directly (via Quartermaster call would be cleaner)
		// For now, we just return success - Gateway will call Quartermaster to create access
		resp.Status = "active"

	case "monthly":
		// For paid monthly clusters, create Stripe checkout session
		if s.stripeClient == nil {
			s.logger.Warn("Stripe client not configured for monthly cluster subscription")
			resp.Status = "pending_payment"
			break
		}

		// Get Stripe price ID from cluster pricing
		priceID := pricing.GetStripePriceIdMonthly()
		if priceID == "" {
			s.logger.Error("No Stripe price ID configured for monthly cluster", "cluster", clusterID)
			return nil, status.Error(codes.FailedPrecondition, "cluster pricing not configured in Stripe")
		}

		// Get/create Stripe customer for this tenant
		billingEmail := req.GetBillingEmail()
		if billingEmail == "" {
			billingEmail = fmt.Sprintf("tenant+%s@example.com", tenantID[:8]) // Fallback
		}
		cust, err := s.stripeClient.CreateOrGetCustomer(ctx, stripe.CustomerInfo{
			TenantID: tenantID,
			Email:    billingEmail,
			Name:     fmt.Sprintf("Tenant %s", tenantID[:8]),
			Metadata: map[string]string{
				"cluster_id": clusterID,
			},
		})
		if err != nil {
			s.logger.Error("Failed to create Stripe customer", "error", err)
			return nil, status.Errorf(codes.Internal, "failed to setup payment: %v", err)
		}

		// Create checkout session
		successURL := req.GetSuccessUrl()
		if successURL == "" {
			webappURL := strings.TrimSpace(os.Getenv("WEBAPP_PUBLIC_URL"))
			if webappURL == "" {
				return nil, status.Error(codes.FailedPrecondition, "WEBAPP_PUBLIC_URL is required")
			}
			successURL = fmt.Sprintf("%s/clusters/%s?status=success", webappURL, clusterID)
		}
		cancelURL := req.GetCancelUrl()
		if cancelURL == "" {
			webappURL := strings.TrimSpace(os.Getenv("WEBAPP_PUBLIC_URL"))
			if webappURL == "" {
				return nil, status.Error(codes.FailedPrecondition, "WEBAPP_PUBLIC_URL is required")
			}
			cancelURL = fmt.Sprintf("%s/clusters/%s?status=cancelled", webappURL, clusterID)
		}

		sess, err := s.stripeClient.CreateCheckoutSession(ctx, stripe.CheckoutSessionParams{
			CustomerID:  cust.ID,
			TenantID:    tenantID,
			TierID:      clusterID, // For backward compatibility only
			Purpose:     "cluster_subscription",
			ReferenceID: clusterID,
			ClusterID:   clusterID,
			PriceID:     priceID,
			SuccessURL:  successURL,
			CancelURL:   cancelURL,
		})
		if err != nil {
			s.logger.Error("Failed to create Stripe checkout session", "error", err)
			return nil, status.Errorf(codes.Internal, "failed to create checkout: %v", err)
		}

		var subscriptionID string
		err = s.db.QueryRowContext(ctx, `
			INSERT INTO purser.cluster_subscriptions (
				tenant_id, cluster_id, status, stripe_customer_id, checkout_session_id, created_at, updated_at
			) VALUES ($1, $2, 'pending_payment', $3, $4, NOW(), NOW())
			ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
				status = EXCLUDED.status,
				stripe_customer_id = EXCLUDED.stripe_customer_id,
				checkout_session_id = EXCLUDED.checkout_session_id,
				updated_at = NOW()
			RETURNING id
		`, tenantID, clusterID, cust.ID, sess.ID).Scan(&subscriptionID)
		if err != nil {
			s.logger.WithError(err).Error("Failed to record cluster subscription checkout")
			return nil, status.Error(codes.Internal, "failed to record cluster subscription")
		}

		resp.SubscriptionId = subscriptionID
		resp.Status = "pending_payment"
		checkoutURL := sess.URL
		resp.CheckoutUrl = &checkoutURL
		s.logger.Info("Created Stripe checkout for cluster subscription",
			"tenant", tenantID, "cluster", clusterID, "session", sess.ID)

	case "metered":
		// Metered clusters can be activated immediately, billing happens on usage
		resp.Status = "active"

	case "custom":
		// Custom requires approval
		resp.Status = "pending_approval"
	}

	return resp, nil
}

// CancelClusterSubscription cancels a tenant's subscription to a cluster
func (s *PurserServer) CancelClusterSubscription(ctx context.Context, req *pb.CancelClusterSubscriptionRequest) (*emptypb.Empty, error) {
	tenantID := req.GetTenantId()
	clusterID := req.GetClusterId()

	if tenantID == "" || clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and cluster_id required")
	}

	// Get cluster pricing to check if there's a Stripe subscription to cancel
	pricing, err := s.GetClusterPricing(ctx, &pb.GetClusterPricingRequest{ClusterId: clusterID})
	if err != nil {
		return nil, err
	}

	if pricing.PricingModel == "monthly" {
		if s.stripeClient == nil {
			return nil, status.Error(codes.Unavailable, "Stripe not configured")
		}

		var stripeSubID sql.NullString
		err = s.db.QueryRowContext(ctx, `
			SELECT stripe_subscription_id
			FROM purser.cluster_subscriptions
			WHERE tenant_id = $1 AND cluster_id = $2
		`, tenantID, clusterID).Scan(&stripeSubID)
		if err == sql.ErrNoRows || !stripeSubID.Valid {
			return nil, status.Error(codes.NotFound, "no Stripe subscription found for cluster")
		}
		if err != nil {
			return nil, status.Error(codes.Internal, "failed to load cluster subscription")
		}

		sub, err := s.stripeClient.CancelSubscription(ctx, stripeSubID.String)
		if err != nil {
			s.logger.WithError(err).Error("Failed to cancel Stripe cluster subscription")
			return nil, status.Error(codes.Internal, "failed to cancel Stripe subscription")
		}

		var periodEnd *time.Time
		if sub.Items != nil && len(sub.Items.Data) > 0 && sub.Items.Data[0].CurrentPeriodEnd > 0 {
			t := time.Unix(sub.Items.Data[0].CurrentPeriodEnd, 0)
			periodEnd = &t
		}
		ourStatus := handlers.MapStripeSubscriptionStatus(string(sub.Status), sub.CancelAtPeriodEnd)

		_, err = s.db.ExecContext(ctx, `
			UPDATE purser.cluster_subscriptions
			SET status = $1,
			    stripe_subscription_status = $2,
			    stripe_current_period_end = $3,
			    cancelled_at = CASE WHEN $1 = 'cancelled' THEN NOW() ELSE cancelled_at END,
			    updated_at = NOW()
			WHERE tenant_id = $4 AND cluster_id = $5
		`, ourStatus, sub.Status, periodEnd, tenantID, clusterID)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to update cluster subscription after cancellation")
		}
	} else if s.quartermasterClient != nil {
		_, err = s.quartermasterClient.UnsubscribeFromCluster(ctx, &pb.UnsubscribeFromClusterRequest{
			TenantId:  tenantID,
			ClusterId: clusterID,
		})
		if err != nil {
			s.logger.WithError(err).Warn("Failed to revoke cluster access for non-monthly cluster")
		}
	}

	return &emptypb.Empty{}, nil
}

// ListMarketplaceClusterPricings returns paginated cluster pricings filtered by tenant tier level.
// Gateway uses this as the primary marketplace query, then enriches with Quartermaster metadata.
func (s *PurserServer) ListMarketplaceClusterPricings(ctx context.Context, req *pb.ListMarketplaceClusterPricingsRequest) (*pb.ListMarketplaceClusterPricingsResponse, error) {
	tenantID := req.GetTenantId()

	// Get tenant's billing tier level (0 if not found or no subscription)
	var tenantTierLevel int32
	if tenantID != "" {
		err := s.db.QueryRowContext(ctx, `
			SELECT COALESCE(bt.tier_level, 0)
			FROM purser.tenant_subscriptions ts
			JOIN purser.billing_tiers bt ON ts.tier_id = bt.id
			WHERE ts.tenant_id = $1 AND ts.status = 'active'
		`, tenantID).Scan(&tenantTierLevel)
		if err != nil && err != sql.ErrNoRows {
			s.logger.WithError(err).Warn("Failed to get tenant tier level")
		}
	}

	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// Base query with tier filter
	baseWhere := "WHERE required_tier_level <= $1"
	args := []interface{}{tenantTierLevel}
	argIdx := 2

	// Get total count
	var total int32
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM purser.cluster_pricing %s", baseWhere)
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, status.Errorf(codes.Internal, "count query failed: %v", err)
	}

	// Add keyset pagination condition
	where := baseWhere
	if params.Cursor != nil {
		if params.Direction == pagination.Backward {
			where += fmt.Sprintf(" AND (created_at, cluster_id) > ($%d, $%d)", argIdx, argIdx+1)
		} else {
			where += fmt.Sprintf(" AND (created_at, cluster_id) < ($%d, $%d)", argIdx, argIdx+1)
		}
		args = append(args, params.Cursor.Timestamp, params.Cursor.ID)
		argIdx += 2
	}

	// Order by and limit
	orderBy := "ORDER BY created_at DESC, cluster_id DESC"
	if params.Direction == pagination.Backward {
		orderBy = "ORDER BY created_at ASC, cluster_id ASC"
	}

	query := fmt.Sprintf(`
		SELECT cluster_id, pricing_model, base_price, currency,
		       required_tier_level, is_platform_official, created_at
		FROM purser.cluster_pricing
		%s %s LIMIT $%d`,
		where, orderBy, argIdx)
	args = append(args, params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var pricings []*pb.MarketplaceClusterPricing
	for rows.Next() {
		var p pb.MarketplaceClusterPricing
		var basePrice sql.NullFloat64
		var currency sql.NullString
		var createdAt time.Time

		if err := rows.Scan(
			&p.ClusterId, &p.PricingModel, &basePrice, &currency,
			&p.RequiredTierLevel, &p.IsPlatformOfficial, &createdAt,
		); err != nil {
			s.logger.WithError(err).Warn("Failed to scan cluster pricing row")
			continue
		}

		if basePrice.Valid {
			p.MonthlyPriceCents = int32(basePrice.Float64 * 100)
		}
		if currency.Valid {
			p.Currency = currency.String
		} else {
			p.Currency = "EUR"
		}
		p.CreatedAt = timestamppb.New(createdAt)

		pricings = append(pricings, &p)
	}

	// Determine pagination info
	resultsLen := len(pricings)
	hasMore := resultsLen > int(params.Limit)
	if hasMore {
		pricings = pricings[:params.Limit]
		resultsLen = int(params.Limit)
	}

	// Reverse results for backward pagination
	if params.Direction == pagination.Backward {
		slices.Reverse(pricings)
	}

	// Build cursors
	var startCursor, endCursor string
	if len(pricings) > 0 {
		first := pricings[0]
		last := pricings[len(pricings)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.ClusterId)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.ClusterId)
	}

	return &pb.ListMarketplaceClusterPricingsResponse{
		Pricings:   pricings,
		Pagination: pagination.BuildResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
	}, nil
}

// jsonToMap is a helper to convert JSON bytes to map for structpb
func jsonToMap(data []byte) map[string]interface{} {
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return make(map[string]interface{})
	}
	return m
}

const (
	eventPaymentCreated       = "payment_created"
	eventPaymentSucceeded     = "payment_succeeded"
	eventPaymentFailed        = "payment_failed"
	eventSubscriptionCreated  = "subscription_created"
	eventSubscriptionUpdated  = "subscription_updated"
	eventSubscriptionCanceled = "subscription_canceled"
	eventInvoicePaid          = "invoice_paid"
	eventInvoicePaymentFailed = "invoice_payment_failed"
	eventTopupCreated         = "topup_created"
	eventTopupCredited        = "topup_credited"
	eventTopupFailed          = "topup_failed"
)

func (s *PurserServer) emitServiceEvent(ctx context.Context, event *pb.ServiceEvent) {
	if s.decklogClient == nil || event == nil {
		return
	}
	if ctxkeys.IsDemoMode(ctx) {
		return
	}

	go func(ev *pb.ServiceEvent) {
		if err := s.decklogClient.SendServiceEvent(ev); err != nil {
			s.logger.WithError(err).WithField("event_type", ev.EventType).Warn("Failed to emit service event")
		}
	}(event)
}

func (s *PurserServer) emitBillingEvent(ctx context.Context, eventType, tenantID, userID, resourceType, resourceID string, payload *pb.BillingEvent) {
	if payload == nil {
		payload = &pb.BillingEvent{}
	}
	payload.TenantId = tenantID

	event := &pb.ServiceEvent{
		EventType:    eventType,
		Timestamp:    timestamppb.Now(),
		Source:       "purser",
		TenantId:     tenantID,
		UserId:       userID,
		ResourceType: resourceType,
		ResourceId:   resourceID,
		Payload:      &pb.ServiceEvent_BillingEvent{BillingEvent: payload},
	}
	s.emitServiceEvent(ctx, event)
}

// ============================================================================
// SERVER SETUP
// ============================================================================

// GRPCServerConfig contains configuration for creating a Purser gRPC server
type GRPCServerConfig struct {
	DB                  *sql.DB
	Logger              logging.Logger
	ServiceToken        string
	JWTSecret           []byte
	Metrics             *ServerMetrics
	StripeClient        *stripe.Client
	MollieClient        *mollie.Client
	QuartermasterClient *qmclient.GRPCClient
	CommodoreClient     handlers.CommodoreClient
	DecklogClient       *decklogclient.BatchedClient
}

// NewGRPCServer creates a new gRPC server for Purser
func NewGRPCServer(cfg GRPCServerConfig) *grpc.Server {
	// Chain auth interceptor with logging interceptor
	authInterceptor := middleware.GRPCAuthInterceptor(middleware.GRPCAuthConfig{
		ServiceToken: cfg.ServiceToken,
		JWTSecret:    cfg.JWTSecret,
		Logger:       cfg.Logger,
		SkipMethods: []string{
			"/grpc.health.v1.Health/Check",
			"/grpc.health.v1.Health/Watch",
		},
	})

	opts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(authInterceptor, unaryInterceptor(cfg.Logger)),
	}

	server := grpc.NewServer(opts...)
	purserServer := NewPurserServer(cfg.DB, cfg.Logger, cfg.Metrics, cfg.StripeClient, cfg.MollieClient, cfg.QuartermasterClient, cfg.CommodoreClient, cfg.DecklogClient)

	// Register all services
	pb.RegisterBillingServiceServer(server, purserServer)
	pb.RegisterUsageServiceServer(server, purserServer)
	pb.RegisterSubscriptionServiceServer(server, purserServer)
	pb.RegisterInvoiceServiceServer(server, purserServer)
	pb.RegisterPaymentServiceServer(server, purserServer)
	pb.RegisterClusterPricingServiceServer(server, purserServer)
	pb.RegisterPrepaidServiceServer(server, purserServer)
	pb.RegisterWebhookServiceServer(server, purserServer)
	pb.RegisterStripeServiceServer(server, purserServer)
	pb.RegisterMollieServiceServer(server, purserServer)
	pb.RegisterX402ServiceServer(server, purserServer)

	// Register gRPC health checking service
	hs := health.NewServer()
	grpc_health_v1.RegisterHealthServer(server, hs)

	return server
}

// unaryInterceptor logs gRPC requests
func unaryInterceptor(logger logging.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		logger.WithFields(logging.Fields{
			"method":   info.FullMethod,
			"duration": time.Since(start),
			"error":    err,
		}).Info("gRPC request processed")
		return resp, grpcutil.SanitizeError(err)
	}
}

// ============================================================================
// PREPAID BALANCE SERVICE IMPLEMENTATION
// ============================================================================

// GetPrepaidBalance retrieves the current prepaid balance for a tenant
func (s *PurserServer) GetPrepaidBalance(ctx context.Context, req *pb.GetPrepaidBalanceRequest) (*pb.PrepaidBalance, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	currency := req.GetCurrency()
	if currency == "" {
		currency = billing.DefaultCurrency()
	}

	var balance pb.PrepaidBalance
	var createdAt, updatedAt time.Time

	err := s.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, balance_cents, currency, low_balance_threshold_cents, created_at, updated_at
		FROM purser.prepaid_balances
		WHERE tenant_id = $1 AND currency = $2
	`, tenantID, currency).Scan(
		&balance.Id,
		&balance.TenantId,
		&balance.BalanceCents,
		&balance.Currency,
		&balance.LowBalanceThresholdCents,
		&createdAt,
		&updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, status.Errorf(codes.NotFound, "no prepaid balance found for tenant %s", tenantID)
	}
	if err != nil {
		s.logger.WithError(err).Error("Failed to get prepaid balance")
		return nil, status.Error(codes.Internal, "failed to get prepaid balance")
	}

	balance.CreatedAt = timestamppb.New(createdAt)
	balance.UpdatedAt = timestamppb.New(updatedAt)
	balance.IsLowBalance = balance.BalanceCents < balance.LowBalanceThresholdCents

	// Calculate drain rate from last hour's usage deductions
	var usageLastHour int64
	err = s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(ABS(amount_cents)), 0)
		FROM purser.balance_transactions
		WHERE tenant_id = $1
		  AND transaction_type = 'usage'
		  AND amount_cents < 0
		  AND created_at >= NOW() - INTERVAL '1 hour'
	`, tenantID).Scan(&usageLastHour)
	if err != nil && err != sql.ErrNoRows {
		s.logger.WithError(err).Warn("Failed to calculate drain rate, defaulting to 0")
		usageLastHour = 0
	}
	balance.DrainRateCentsPerHour = usageLastHour

	return &balance, nil
}

// InitializePrepaidBalance creates a new prepaid balance record for a tenant
func (s *PurserServer) InitializePrepaidBalance(ctx context.Context, req *pb.InitializePrepaidBalanceRequest) (*pb.PrepaidBalance, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	currency := req.GetCurrency()
	if currency == "" {
		currency = billing.DefaultCurrency()
	}

	threshold := req.GetLowBalanceThresholdCents()
	if threshold == 0 {
		threshold = 500 // Default $5
	}

	id := uuid.New().String()
	now := time.Now()

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO purser.prepaid_balances (id, tenant_id, balance_cents, currency, low_balance_threshold_cents, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $6)
		ON CONFLICT (tenant_id, currency) DO NOTHING
	`, id, tenantID, req.GetInitialBalanceCents(), currency, threshold, now)
	if err != nil {
		s.logger.WithError(err).Error("Failed to initialize prepaid balance")
		return nil, status.Error(codes.Internal, "failed to initialize prepaid balance")
	}

	// Fetch and return the balance (could be existing if ON CONFLICT hit)
	return s.GetPrepaidBalance(ctx, &pb.GetPrepaidBalanceRequest{
		TenantId: tenantID,
		Currency: currency,
	})
}

// InitializePrepaidAccount creates subscription + prepaid balance for wallet provisioning.
// Called by Commodore during GetOrCreateWalletUser to avoid cross-service DB inserts.
// This is atomic - creates both subscription (billing_model=prepaid) and balance in one transaction.
func (s *PurserServer) InitializePrepaidAccount(ctx context.Context, req *pb.InitializePrepaidAccountRequest) (*pb.InitializePrepaidAccountResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	currency := req.GetCurrency()
	if currency == "" {
		currency = billing.DefaultCurrency()
	}

	subscriptionID := uuid.New().String()
	balanceID := uuid.New().String()
	now := time.Now()

	// Use a transaction to create both subscription and balance atomically
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to start transaction")
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	// 1. Resolve billing tier (prefer PAYG, fallback to lowest active tier)
	var tierID string
	err = tx.QueryRowContext(ctx, `
		SELECT id
		FROM purser.billing_tiers
		WHERE tier_name = 'payg' AND is_active = true
		ORDER BY created_at ASC
		LIMIT 1
	`).Scan(&tierID)
	if err == sql.ErrNoRows {
		err = tx.QueryRowContext(ctx, `
			SELECT id
			FROM purser.billing_tiers
			WHERE is_active = true
			ORDER BY tier_level ASC, base_price ASC, created_at ASC
			LIMIT 1
		`).Scan(&tierID)
		if err == sql.ErrNoRows {
			return nil, status.Error(codes.FailedPrecondition, "no active billing tiers available")
		}
		if err != nil {
			s.logger.WithError(err).Error("Failed to resolve fallback billing tier")
			return nil, status.Error(codes.Internal, "failed to resolve billing tier")
		}
		s.logger.WithField("tenant_id", tenantID).Warn("PAYG tier missing; falling back to lowest active tier")
	} else if err != nil {
		s.logger.WithError(err).Error("Failed to resolve PAYG tier")
		return nil, status.Error(codes.Internal, "failed to resolve billing tier")
	}

	// 2. Create subscription with billing_model='prepaid', status='active'
	_, err = tx.ExecContext(ctx, `
		INSERT INTO purser.tenant_subscriptions (id, tenant_id, tier_id, billing_model, status, created_at, updated_at)
		VALUES ($1, $2, $3, 'prepaid', 'active', $4, $4)
		ON CONFLICT (tenant_id) DO NOTHING
	`, subscriptionID, tenantID, tierID, now)
	if err != nil {
		s.logger.WithError(err).Error("Failed to create prepaid subscription")
		return nil, status.Error(codes.Internal, "failed to create subscription")
	}

	// 3. Create prepaid balance with initial balance 0
	_, err = tx.ExecContext(ctx, `
		INSERT INTO purser.prepaid_balances (id, tenant_id, balance_cents, currency, low_balance_threshold_cents, created_at, updated_at)
		VALUES ($1, $2, 0, $3, 500, $4, $4)
		ON CONFLICT (tenant_id, currency) DO NOTHING
	`, balanceID, tenantID, currency, now)
	if err != nil {
		s.logger.WithError(err).Error("Failed to create prepaid balance")
		return nil, status.Error(codes.Internal, "failed to create prepaid balance")
	}

	if err = tx.Commit(); err != nil {
		return nil, status.Error(codes.Internal, "failed to commit transaction")
	}

	// Get actual IDs (could be existing if ON CONFLICT hit)
	var actualSubID, actualBalID string
	_ = s.db.QueryRowContext(ctx, `SELECT id FROM purser.tenant_subscriptions WHERE tenant_id = $1`, tenantID).Scan(&actualSubID)
	_ = s.db.QueryRowContext(ctx, `SELECT id FROM purser.prepaid_balances WHERE tenant_id = $1 AND currency = $2`, tenantID, currency).Scan(&actualBalID)

	s.logger.WithFields(logging.Fields{
		"tenant_id":       tenantID,
		"subscription_id": actualSubID,
		"balance_id":      actualBalID,
	}).Info("Initialized prepaid account")

	return &pb.InitializePrepaidAccountResponse{
		SubscriptionId: actualSubID,
		BalanceId:      actualBalID,
	}, nil
}

// TopupBalance adds funds to a tenant's prepaid balance
func (s *PurserServer) TopupBalance(ctx context.Context, req *pb.TopupBalanceRequest) (*pb.BalanceTransaction, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	amountCents := req.GetAmountCents()
	if amountCents <= 0 {
		return nil, status.Error(codes.InvalidArgument, "amount_cents must be positive")
	}

	currency := req.GetCurrency()
	if currency == "" {
		currency = billing.DefaultCurrency()
	}

	return s.recordBalanceTransaction(ctx, tenantID, currency, amountCents, "topup", req.GetDescription(), req.ReferenceId, req.ReferenceType)
}

// DeductBalance removes funds from a tenant's prepaid balance
func (s *PurserServer) DeductBalance(ctx context.Context, req *pb.DeductBalanceRequest) (*pb.BalanceTransaction, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	amountCents := req.GetAmountCents()
	if amountCents <= 0 {
		return nil, status.Error(codes.InvalidArgument, "amount_cents must be positive")
	}

	currency := req.GetCurrency()
	if currency == "" {
		currency = billing.DefaultCurrency()
	}

	// Deduction is stored as negative
	return s.recordBalanceTransaction(ctx, tenantID, currency, -amountCents, "usage", req.GetDescription(), req.ReferenceId, req.ReferenceType)
}

// AdjustBalance manually adjusts a tenant's prepaid balance (admin)
func (s *PurserServer) AdjustBalance(ctx context.Context, req *pb.AdjustBalanceRequest) (*pb.BalanceTransaction, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	description := req.GetDescription()
	if description == "" {
		return nil, status.Error(codes.InvalidArgument, "description is required for adjustments")
	}

	currency := req.GetCurrency()
	if currency == "" {
		currency = billing.DefaultCurrency()
	}

	txType := "adjustment"
	if req.GetAmountCents() > 0 {
		txType = "refund" // Positive adjustments are typically refunds
	}

	return s.recordBalanceTransaction(ctx, tenantID, currency, req.GetAmountCents(), txType, description, req.ReferenceId, req.ReferenceType)
}

// recordBalanceTransaction atomically updates balance and records the transaction
func (s *PurserServer) recordBalanceTransaction(
	ctx context.Context,
	tenantID, currency string,
	amountCents int64,
	txType, description string,
	referenceID, referenceType *string,
) (*pb.BalanceTransaction, error) {
	userID := middleware.GetUserID(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to begin transaction")
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	// Update balance and get new balance in one query
	var newBalance int64
	err = tx.QueryRowContext(ctx, `
		UPDATE purser.prepaid_balances
		SET balance_cents = balance_cents + $1, updated_at = NOW()
		WHERE tenant_id = $2 AND currency = $3
		RETURNING balance_cents
	`, amountCents, tenantID, currency).Scan(&newBalance)
	if err == sql.ErrNoRows {
		return nil, status.Errorf(codes.NotFound, "no prepaid balance found for tenant %s", tenantID)
	}
	if err != nil {
		s.logger.WithError(err).Error("Failed to update balance")
		return nil, status.Error(codes.Internal, "failed to update balance")
	}

	// Insert transaction record
	txID := uuid.New().String()
	now := time.Now()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO purser.balance_transactions
		(id, tenant_id, amount_cents, balance_after_cents, transaction_type, description, reference_id, reference_type, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, txID, tenantID, amountCents, newBalance, txType, description, referenceID, referenceType, now)
	if err != nil {
		s.logger.WithError(err).Error("Failed to insert transaction")
		return nil, status.Error(codes.Internal, "failed to record transaction")
	}

	if err := tx.Commit(); err != nil {
		return nil, status.Error(codes.Internal, "failed to commit transaction")
	}

	if txType == "topup" && amountCents > 0 {
		topupID := ""
		if referenceID != nil {
			topupID = *referenceID
		}
		s.emitBillingEvent(ctx, eventTopupCredited, tenantID, userID, "topup", txID, &pb.BillingEvent{
			TopupId:  topupID,
			Amount:   float64(amountCents) / 100.0,
			Currency: currency,
			Status:   "credited",
		})
	}

	// Auto-reactivate suspended tenant if balance goes positive after top-up
	if amountCents > 0 && newBalance >= 0 {
		result, err := s.db.ExecContext(ctx, `
			UPDATE purser.tenant_subscriptions
			SET status = 'active', updated_at = NOW()
			WHERE tenant_id = $1 AND status = 'suspended'
		`, tenantID)
		if err != nil {
			s.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to check/reactivate suspended subscription")
		} else if rowsAffected, _ := result.RowsAffected(); rowsAffected > 0 {
			s.logger.WithFields(map[string]interface{}{
				"tenant_id":        tenantID,
				"new_balance":      newBalance,
				"transaction_type": txType,
			}).Info("Reactivated suspended tenant after balance top-up")

			// Immediately invalidate media plane caches so reactivation takes effect
			if s.commodoreClient != nil {
				go func() {
					invalidateCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					resp, err := s.commodoreClient.InvalidateTenantCache(invalidateCtx, tenantID, "balance_topped_up")
					if err != nil {
						s.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to invalidate tenant cache after reactivation")
					} else {
						s.logger.WithFields(map[string]interface{}{
							"tenant_id":           tenantID,
							"entries_invalidated": resp.EntriesInvalidated,
						}).Info("Invalidated media plane cache after reactivation")
					}
				}()
			}
		}
	}

	return &pb.BalanceTransaction{
		Id:                txID,
		TenantId:          tenantID,
		AmountCents:       amountCents,
		BalanceAfterCents: newBalance,
		TransactionType:   txType,
		Description:       description,
		ReferenceId:       referenceID,
		ReferenceType:     referenceType,
		CreatedAt:         timestamppb.New(now),
	}, nil
}

// ListBalanceTransactions returns transaction history for a tenant
func (s *PurserServer) ListBalanceTransactions(ctx context.Context, req *pb.ListBalanceTransactionsRequest) (*pb.ListBalanceTransactionsResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	// Build query with optional filters
	query := `
		SELECT id, tenant_id, amount_cents, balance_after_cents, transaction_type, description, reference_id, reference_type, created_at
		FROM purser.balance_transactions
		WHERE tenant_id = $1
	`
	args := []interface{}{tenantID}
	argIdx := 2

	if req.TransactionType != nil && *req.TransactionType != "" {
		query += fmt.Sprintf(" AND transaction_type = $%d", argIdx)
		args = append(args, *req.TransactionType)
		argIdx++
	}

	if req.TimeRange != nil {
		if req.TimeRange.Start != nil {
			query += fmt.Sprintf(" AND created_at >= $%d", argIdx)
			args = append(args, req.TimeRange.Start.AsTime())
			argIdx++
		}
		if req.TimeRange.End != nil {
			query += fmt.Sprintf(" AND created_at <= $%d", argIdx)
			args = append(args, req.TimeRange.End.AsTime())
		}
	}

	query += " ORDER BY created_at DESC LIMIT 100"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		s.logger.WithError(err).Error("Failed to list transactions")
		return nil, status.Error(codes.Internal, "failed to list transactions")
	}
	defer rows.Close()

	var transactions []*pb.BalanceTransaction
	for rows.Next() {
		var txn pb.BalanceTransaction
		var createdAt time.Time
		var refID, refType sql.NullString

		err := rows.Scan(
			&txn.Id,
			&txn.TenantId,
			&txn.AmountCents,
			&txn.BalanceAfterCents,
			&txn.TransactionType,
			&txn.Description,
			&refID,
			&refType,
			&createdAt,
		)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan transaction")
			continue
		}

		txn.CreatedAt = timestamppb.New(createdAt)
		if refID.Valid {
			txn.ReferenceId = &refID.String
		}
		if refType.Valid {
			txn.ReferenceType = &refType.String
		}

		transactions = append(transactions, &txn)
	}

	return &pb.ListBalanceTransactionsResponse{
		Transactions: transactions,
	}, nil
}

// ============================================================================
// CARD TOP-UP METHODS
// ============================================================================

// CreateCardTopup creates a Stripe/Mollie checkout session for prepaid balance top-up
func (s *PurserServer) CreateCardTopup(ctx context.Context, req *pb.CreateCardTopupRequest) (*pb.CreateCardTopupResponse, error) {
	tenantID := req.GetTenantId()
	amountCents := req.GetAmountCents()
	currency := req.GetCurrency()
	provider := req.GetProvider()
	successURL := req.GetSuccessUrl()
	cancelURL := req.GetCancelUrl()

	// Validation
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if amountCents <= 0 {
		return nil, status.Error(codes.InvalidArgument, "amount must be positive")
	}
	if successURL == "" || cancelURL == "" {
		return nil, status.Error(codes.InvalidArgument, "success_url and cancel_url are required")
	}
	if provider != "stripe" && provider != "mollie" {
		return nil, status.Error(codes.InvalidArgument, "provider must be 'stripe' or 'mollie'")
	}
	if currency == "" {
		currency = billing.DefaultCurrency()
	}

	userID := middleware.GetUserID(ctx)
	// Create pending_topup record first
	topupID := uuid.New().String()
	expiresAt := time.Now().Add(24 * time.Hour) // Will be updated after checkout creation

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO purser.pending_topups (
			id, tenant_id, provider, checkout_id, amount_cents, currency,
			status, expires_at, billing_email, billing_name, billing_company, billing_vat_number
		) VALUES ($1, $2, $3, $4, $5, $6, 'pending', $7, $8, $9, $10, $11)
	`, topupID, tenantID, provider, "", amountCents, currency, expiresAt,
		sqlNullString(req.BillingEmail), sqlNullString(req.BillingName),
		sqlNullString(req.BillingCompany), sqlNullString(req.BillingVatNumber))
	if err != nil {
		s.logger.WithError(err).Error("Failed to create pending topup")
		return nil, status.Error(codes.Internal, "failed to create topup record")
	}

	s.emitBillingEvent(ctx, eventTopupCreated, tenantID, userID, "topup", topupID, &pb.BillingEvent{
		TopupId:  topupID,
		Amount:   float64(amountCents) / 100.0,
		Currency: currency,
		Provider: provider,
		Status:   "pending",
	})

	// Create checkout session using unified CheckoutService
	checkoutSvc := handlers.NewCheckoutService(s.db, s.logger)
	result, err := checkoutSvc.CreateCheckout(ctx, handlers.CheckoutRequest{
		Purpose:        handlers.PurposePrepaid,
		Provider:       handlers.CheckoutProvider(provider),
		TenantID:       tenantID,
		ReferenceID:    topupID,
		AmountCents:    amountCents,
		Currency:       currency,
		SuccessURL:     successURL,
		CancelURL:      cancelURL,
		Description:    "Video streaming & infrastructure credits",
		BillingEmail:   derefString(req.BillingEmail),
		BillingName:    derefString(req.BillingName),
		BillingCompany: derefString(req.BillingCompany),
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to create checkout session")
		// Mark topup as failed
		if _, dbErr := s.db.ExecContext(ctx, `UPDATE purser.pending_topups SET status = 'failed', updated_at = NOW() WHERE id = $1`, topupID); dbErr != nil {
			s.logger.WithError(dbErr).Warn("Failed to update topup status to failed")
		}
		s.emitBillingEvent(ctx, eventTopupFailed, tenantID, userID, "topup", topupID, &pb.BillingEvent{
			TopupId:  topupID,
			Amount:   float64(amountCents) / 100.0,
			Currency: currency,
			Provider: provider,
			Status:   "failed",
		})
		return nil, status.Errorf(codes.Internal, "failed to create checkout: %v", err)
	}

	// Update pending_topup with checkout details
	_, err = s.db.ExecContext(ctx, `
		UPDATE purser.pending_topups
		SET checkout_id = $1, expires_at = $2, updated_at = NOW()
		WHERE id = $3
	`, result.SessionID, result.ExpiresAt, topupID)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to update pending topup with checkout_id")
	}

	return &pb.CreateCardTopupResponse{
		TopupId:     topupID,
		CheckoutUrl: result.CheckoutURL,
		CheckoutId:  result.SessionID,
		Provider:    provider,
		AmountCents: amountCents,
		Currency:    currency,
		ExpiresAt:   timestamppb.New(result.ExpiresAt),
	}, nil
}

// GetPendingTopup returns the status of a pending top-up
func (s *PurserServer) GetPendingTopup(ctx context.Context, req *pb.GetPendingTopupRequest) (*pb.PendingTopup, error) {
	var query string
	var args []interface{}

	if req.GetTopupId() != "" {
		query = `SELECT id, tenant_id, provider, checkout_id, amount_cents, currency,
		         status, expires_at, completed_at, balance_transaction_id, created_at, updated_at
		         FROM purser.pending_topups WHERE id = $1`
		args = []interface{}{req.GetTopupId()}
	} else if req.GetCheckoutId() != "" && req.GetProvider() != "" {
		query = `SELECT id, tenant_id, provider, checkout_id, amount_cents, currency,
		         status, expires_at, completed_at, balance_transaction_id, created_at, updated_at
		         FROM purser.pending_topups WHERE provider = $1 AND checkout_id = $2`
		args = []interface{}{req.GetProvider(), req.GetCheckoutId()}
	} else {
		return nil, status.Error(codes.InvalidArgument, "topup_id or (provider + checkout_id) required")
	}

	var topup pb.PendingTopup
	var expiresAt, createdAt, updatedAt time.Time
	var completedAt sql.NullTime
	var balanceTxID sql.NullString

	err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&topup.Id, &topup.TenantId, &topup.Provider, &topup.CheckoutId,
		&topup.AmountCents, &topup.Currency, &topup.Status,
		&expiresAt, &completedAt, &balanceTxID, &createdAt, &updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "topup not found")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to get topup")
	}

	topup.ExpiresAt = timestamppb.New(expiresAt)
	topup.CreatedAt = timestamppb.New(createdAt)
	topup.UpdatedAt = timestamppb.New(updatedAt)
	if completedAt.Valid {
		topup.CompletedAt = timestamppb.New(completedAt.Time)
	}
	if balanceTxID.Valid {
		topup.BalanceTransactionId = &balanceTxID.String
	}

	return &topup, nil
}

// ListPendingTopups returns a list of top-ups for a tenant
func (s *PurserServer) ListPendingTopups(ctx context.Context, req *pb.ListPendingTopupsRequest) (*pb.ListPendingTopupsResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	query := `SELECT id, tenant_id, provider, checkout_id, amount_cents, currency,
	          status, expires_at, completed_at, balance_transaction_id, created_at, updated_at
	          FROM purser.pending_topups WHERE tenant_id = $1`
	args := []interface{}{tenantID}

	if req.Status != nil && *req.Status != "" {
		query += " AND status = $2"
		args = append(args, *req.Status)
	}

	query += " ORDER BY created_at DESC LIMIT 50"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to list topups")
	}
	defer rows.Close()

	var topups []*pb.PendingTopup
	for rows.Next() {
		var topup pb.PendingTopup
		var expiresAt, createdAt, updatedAt time.Time
		var completedAt sql.NullTime
		var balanceTxID sql.NullString

		err := rows.Scan(
			&topup.Id, &topup.TenantId, &topup.Provider, &topup.CheckoutId,
			&topup.AmountCents, &topup.Currency, &topup.Status,
			&expiresAt, &completedAt, &balanceTxID, &createdAt, &updatedAt,
		)
		if err != nil {
			continue
		}

		topup.ExpiresAt = timestamppb.New(expiresAt)
		topup.CreatedAt = timestamppb.New(createdAt)
		topup.UpdatedAt = timestamppb.New(updatedAt)
		if completedAt.Valid {
			topup.CompletedAt = timestamppb.New(completedAt.Time)
		}
		if balanceTxID.Valid {
			topup.BalanceTransactionId = &balanceTxID.String
		}

		topups = append(topups, &topup)
	}

	return &pb.ListPendingTopupsResponse{
		Topups: topups,
	}, nil
}

// ============================================================================
// CRYPTO TOP-UP
// ============================================================================

// CreateCryptoTopup generates an HD-derived deposit address for prepaid balance top-up.
// This is the agent-friendly payment method - no human-in-the-loop required.
func (s *PurserServer) CreateCryptoTopup(ctx context.Context, req *pb.CreateCryptoTopupRequest) (*pb.CreateCryptoTopupResponse, error) {
	tenantID := req.GetTenantId()
	expectedAmountCents := req.GetExpectedAmountCents()
	asset := req.GetAsset()
	currency := req.GetCurrency()

	// Validation
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if expectedAmountCents <= 0 {
		return nil, status.Error(codes.InvalidArgument, "amount must be positive")
	}
	if currency == "" {
		currency = billing.DefaultCurrency()
	}

	userID := middleware.GetUserID(ctx)
	// Map proto enum to asset string
	var assetStr string
	var assetSymbol string
	switch asset {
	case pb.CryptoAsset_CRYPTO_ASSET_ETH:
		assetStr = "ETH"
		assetSymbol = "ETH"
	case pb.CryptoAsset_CRYPTO_ASSET_USDC:
		assetStr = "USDC"
		assetSymbol = "USDC"
	case pb.CryptoAsset_CRYPTO_ASSET_LPT:
		assetStr = "LPT"
		assetSymbol = "LPT"
	default:
		return nil, status.Error(codes.InvalidArgument, "asset must be ETH, USDC, or LPT")
	}

	// Generate deposit address using HD wallet
	expiresAt := time.Now().Add(24 * time.Hour)
	walletID, address, err := s.hdwallet.GenerateDepositAddress(
		tenantID,
		"prepaid", // purpose
		nil,       // invoiceID (not applicable for prepaid)
		&expectedAmountCents,
		assetStr,
		expiresAt,
	)
	if err != nil {
		s.logger.WithError(err).Error("Failed to generate deposit address")
		return nil, status.Errorf(codes.Internal, "failed to generate deposit address: %v", err)
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":      tenantID,
		"wallet_id":      walletID,
		"asset":          assetStr,
		"address":        address,
		"expected_cents": expectedAmountCents,
	}).Info("Created crypto top-up deposit address")

	s.emitBillingEvent(ctx, eventTopupCreated, tenantID, userID, "topup", walletID, &pb.BillingEvent{
		TopupId:  walletID,
		Amount:   float64(expectedAmountCents) / 100.0,
		Currency: currency,
		Provider: "crypto",
		Status:   "pending",
	})

	return &pb.CreateCryptoTopupResponse{
		TopupId:             walletID,
		DepositAddress:      address,
		Asset:               asset,
		AssetSymbol:         assetSymbol,
		ExpectedAmountCents: expectedAmountCents,
		ExpiresAt:           timestamppb.New(expiresAt),
		// QrCodeData left empty - can be generated client-side
	}, nil
}

// GetCryptoTopup returns the status of a crypto top-up
func (s *PurserServer) GetCryptoTopup(ctx context.Context, req *pb.GetCryptoTopupRequest) (*pb.CryptoTopup, error) {
	topupID := req.GetTopupId()
	if topupID == "" {
		return nil, status.Error(codes.InvalidArgument, "topup_id is required")
	}
	ctxTenantID := middleware.GetTenantID(ctx)
	isServiceCall := middleware.IsServiceCall(ctx)
	if !isServiceCall && ctxTenantID == "" {
		return nil, status.Error(codes.PermissionDenied, "tenant context required")
	}

	var topup pb.CryptoTopup
	var expiresAt, createdAt time.Time
	var detectedAt, completedAt sql.NullTime
	var txHash sql.NullString
	var receivedAmountWei, creditedAmountCents sql.NullInt64
	var confirmations sql.NullInt32

	query := `
		SELECT id, tenant_id, wallet_address, asset, expected_amount_cents,
		       status, tx_hash, confirmations, received_amount_wei, credited_amount_cents,
		       expires_at, detected_at, completed_at, created_at
		FROM purser.crypto_wallets
		WHERE id = $1 AND purpose = 'prepaid'
	`
	args := []interface{}{topupID}
	if ctxTenantID != "" {
		query = `
			SELECT id, tenant_id, wallet_address, asset, expected_amount_cents,
			       status, tx_hash, confirmations, received_amount_wei, credited_amount_cents,
			       expires_at, detected_at, completed_at, created_at
			FROM purser.crypto_wallets
			WHERE id = $1 AND tenant_id = $2 AND purpose = 'prepaid'
		`
		args = append(args, ctxTenantID)
	}

	err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&topup.Id, &topup.TenantId, &topup.DepositAddress, &topup.AssetSymbol,
		&topup.ExpectedAmountCents, &topup.Status, &txHash, &confirmations,
		&receivedAmountWei, &creditedAmountCents, &expiresAt, &detectedAt,
		&completedAt, &createdAt,
	)
	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "crypto topup not found")
	}
	if err != nil {
		s.logger.WithError(err).Error("Failed to get crypto topup")
		return nil, status.Error(codes.Internal, "failed to get crypto topup")
	}

	// Map asset string to enum
	switch topup.AssetSymbol {
	case "ETH":
		topup.Asset = pb.CryptoAsset_CRYPTO_ASSET_ETH
	case "USDC":
		topup.Asset = pb.CryptoAsset_CRYPTO_ASSET_USDC
	case "LPT":
		topup.Asset = pb.CryptoAsset_CRYPTO_ASSET_LPT
	}

	topup.Currency = billing.DefaultCurrency()
	topup.ExpiresAt = timestamppb.New(expiresAt)
	topup.CreatedAt = timestamppb.New(createdAt)

	if txHash.Valid {
		topup.TxHash = txHash.String
	}
	if confirmations.Valid {
		topup.Confirmations = confirmations.Int32
	}
	if receivedAmountWei.Valid {
		topup.ReceivedAmountWei = receivedAmountWei.Int64
	}
	if creditedAmountCents.Valid {
		topup.CreditedAmountCents = creditedAmountCents.Int64
	}
	if detectedAt.Valid {
		topup.DetectedAt = timestamppb.New(detectedAt.Time)
	}
	if completedAt.Valid {
		topup.CompletedAt = timestamppb.New(completedAt.Time)
	}

	return &topup, nil
}

// PromoteToPaid upgrades a prepaid account to postpaid after email verification
func (s *PurserServer) PromoteToPaid(ctx context.Context, req *pb.PromoteToPaidRequest) (*pb.PromoteToPaidResponse, error) {
	tenantID := req.GetTenantId()
	tierID := req.GetTierId()

	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if tierID == "" {
		return nil, status.Error(codes.InvalidArgument, "tier_id is required")
	}

	// Check current billing model
	var currentModel string
	err := s.db.QueryRowContext(ctx, `
		SELECT billing_model FROM purser.tenant_subscriptions WHERE tenant_id = $1
	`, tenantID).Scan(&currentModel)
	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "no subscription found for tenant")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get subscription: %v", err)
	}

	if currentModel == "postpaid" {
		return nil, status.Error(codes.FailedPrecondition, "already on postpaid billing")
	}

	// Verify the tier exists and is not enterprise
	var tierName string
	var isEnterprise bool
	err = s.db.QueryRowContext(ctx, `
		SELECT name, is_enterprise FROM purser.billing_tiers WHERE id = $1
	`, tierID).Scan(&tierName, &isEnterprise)
	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "billing tier not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get tier: %v", err)
	}
	if isEnterprise {
		return nil, status.Error(codes.FailedPrecondition, "enterprise tiers require manual assignment")
	}

	// Get current prepaid balance (to carry forward as credit)
	var creditBalanceCents int64
	err = s.db.QueryRowContext(ctx, `
		SELECT COALESCE(balance_cents, 0) FROM purser.prepaid_balances WHERE tenant_id = $1
	`, tenantID).Scan(&creditBalanceCents)
	if err != nil && err != sql.ErrNoRows {
		return nil, status.Errorf(codes.Internal, "failed to get prepaid balance: %v", err)
	}

	// Start transaction
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to begin transaction: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	// Update subscription to postpaid with selected tier
	var subscriptionID string
	err = tx.QueryRowContext(ctx, `
		UPDATE purser.tenant_subscriptions
		SET billing_model = 'postpaid', tier_id = $1, status = 'active', updated_at = NOW()
		WHERE tenant_id = $2
		RETURNING id
	`, tierID, tenantID).Scan(&subscriptionID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update subscription: %v", err)
	}

	// Note: prepaid_balances row is kept - the system uses this as credit before invoicing
	// If there's a positive balance, it will be deducted from usage before invoicing

	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to commit: %v", err)
	}

	s.logger.WithFields(map[string]interface{}{
		"tenant_id":      tenantID,
		"tier_id":        tierID,
		"tier_name":      tierName,
		"credit_balance": creditBalanceCents,
	}).Info("Tenant promoted from prepaid to postpaid")

	return &pb.PromoteToPaidResponse{
		Success:            true,
		Message:            fmt.Sprintf("Upgraded to %s tier", tierName),
		NewBillingModel:    "postpaid",
		CreditBalanceCents: creditBalanceCents,
		SubscriptionId:     subscriptionID,
	}, nil
}

// Helper functions for optional proto fields
func sqlNullString(s *string) sql.NullString {
	if s == nil || *s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: *s, Valid: true}
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Ensure unused imports don't cause errors
var _ = pq.Array

// ============================================================================
// WEBHOOK SERVICE IMPLEMENTATION
// ============================================================================

// ProcessWebhook handles incoming webhooks from the Gateway.
// The Gateway packages raw HTTP (body + headers) into a WebhookRequest and
// routes it here via gRPC. Signature verification happens in the handlers.
func (s *PurserServer) ProcessWebhook(ctx context.Context, req *pb.WebhookRequest) (*pb.WebhookResponse, error) {
	provider := req.GetProvider()
	body := req.GetBody()
	headers := req.GetHeaders()

	s.logger.WithFields(logging.Fields{
		"provider":  provider,
		"body_size": len(body),
		"source_ip": req.GetSourceIp(),
	}).Info("Processing webhook via gRPC")

	var success bool
	var errMsg string
	var statusCode int

	switch provider {
	case "stripe":
		success, errMsg, statusCode = handlers.ProcessStripeWebhookGRPC(body, headers)
	case "mollie":
		success, errMsg, statusCode = handlers.ProcessMollieWebhookGRPC(body, headers)
	default:
		s.logger.WithField("provider", provider).Warn("Unknown webhook provider")
		return &pb.WebhookResponse{
			Success:    false,
			Error:      "unknown provider: " + provider,
			StatusCode: 400,
		}, nil
	}

	return &pb.WebhookResponse{
		Success:    success,
		Error:      errMsg,
		StatusCode: int32(statusCode),
	}, nil
}

// ============================================================================
// STRIPE SERVICE IMPLEMENTATION
// ============================================================================

// CreateCheckoutSession creates a Stripe Checkout Session for subscription
func (s *PurserServer) CreateCheckoutSession(ctx context.Context, req *pb.CreateStripeCheckoutRequest) (*pb.CreateStripeCheckoutResponse, error) {
	if s.stripeClient == nil {
		return nil, status.Error(codes.Unavailable, "Stripe not configured")
	}

	tenantID := req.GetTenantId()
	tierID := req.GetTierId()
	billingPeriod := req.GetBillingPeriod()
	successURL := req.GetSuccessUrl()
	cancelURL := req.GetCancelUrl()

	if tenantID == "" || tierID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and tier_id are required")
	}
	if successURL == "" || cancelURL == "" {
		return nil, status.Error(codes.InvalidArgument, "success_url and cancel_url are required")
	}

	// Get billing tier to find Stripe price ID
	var priceID sql.NullString
	var tierName string
	priceCol := "stripe_price_id_monthly"
	if billingPeriod == "yearly" {
		priceCol = "stripe_price_id_yearly"
	}

	err := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT tier_name, %s FROM purser.billing_tiers WHERE id = $1
	`, priceCol), tierID).Scan(&tierName, &priceID)
	if err == sql.ErrNoRows {
		return nil, status.Errorf(codes.NotFound, "tier not found: %s", tierID)
	}
	if err != nil {
		s.logger.WithError(err).Error("Failed to get billing tier")
		return nil, status.Error(codes.Internal, "failed to get billing tier")
	}
	if !priceID.Valid || priceID.String == "" {
		return nil, status.Errorf(codes.FailedPrecondition, "tier %s has no Stripe %s price configured", tierName, billingPeriod)
	}

	// Get tenant primary user info via Commodore gRPC (not direct DB access)
	primaryUser, err := s.commodoreClient.GetTenantPrimaryUser(ctx, tenantID)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, status.Error(codes.FailedPrecondition, "no billing email on account")
		}
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Error("Failed to get tenant primary user from Commodore")
		return nil, status.Error(codes.Internal, "failed to get tenant info")
	}
	email := primaryUser.Email
	name := primaryUser.Name
	if name == "" {
		name = email
	}

	// Get or create Stripe customer
	customer, err := s.stripeClient.CreateOrGetCustomer(ctx, stripe.CustomerInfo{
		TenantID: tenantID,
		Email:    email,
		Name:     name,
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to create/get Stripe customer")
		return nil, status.Error(codes.Internal, "failed to create Stripe customer")
	}

	// Create checkout session
	sess, err := s.stripeClient.CreateCheckoutSession(ctx, stripe.CheckoutSessionParams{
		CustomerID:  customer.ID,
		TenantID:    tenantID,
		TierID:      tierID,
		Purpose:     "subscription",
		ReferenceID: tierID,
		PriceID:     priceID.String,
		SuccessURL:  successURL,
		CancelURL:   cancelURL,
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to create Stripe checkout session")
		return nil, status.Error(codes.Internal, "failed to create checkout session")
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":  tenantID,
		"tier_id":    tierID,
		"session_id": sess.ID,
	}).Info("Created Stripe checkout session")

	return &pb.CreateStripeCheckoutResponse{
		CheckoutUrl: sess.URL,
		SessionId:   sess.ID,
	}, nil
}

// CreateBillingPortalSession creates a Stripe Billing Portal session
func (s *PurserServer) CreateBillingPortalSession(ctx context.Context, req *pb.CreateBillingPortalRequest) (*pb.CreateBillingPortalResponse, error) {
	if s.stripeClient == nil {
		return nil, status.Error(codes.Unavailable, "Stripe not configured")
	}

	tenantID := req.GetTenantId()
	returnURL := req.GetReturnUrl()

	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if returnURL == "" {
		return nil, status.Error(codes.InvalidArgument, "return_url is required")
	}

	// Get Stripe customer ID from subscription
	var stripeCustomerID sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT stripe_customer_id FROM purser.tenant_subscriptions
		WHERE tenant_id = $1
	`, tenantID).Scan(&stripeCustomerID)
	if err == sql.ErrNoRows || !stripeCustomerID.Valid {
		return nil, status.Error(codes.NotFound, "no Stripe subscription found for tenant")
	}
	if err != nil {
		s.logger.WithError(err).Error("Failed to get Stripe customer ID")
		return nil, status.Error(codes.Internal, "failed to get subscription")
	}

	sess, err := s.stripeClient.CreateBillingPortalSession(ctx, stripeCustomerID.String, returnURL)
	if err != nil {
		s.logger.WithError(err).Error("Failed to create billing portal session")
		return nil, status.Error(codes.Internal, "failed to create billing portal session")
	}

	return &pb.CreateBillingPortalResponse{
		PortalUrl: sess.URL,
	}, nil
}

// SyncSubscription syncs subscription state from Stripe (admin/debug)
func (s *PurserServer) SyncSubscription(ctx context.Context, req *pb.SyncStripeSubscriptionRequest) (*pb.TenantSubscription, error) {
	if s.stripeClient == nil {
		return nil, status.Error(codes.Unavailable, "Stripe not configured")
	}

	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	// Get Stripe subscription ID
	var stripeSubID sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT stripe_subscription_id FROM purser.tenant_subscriptions
		WHERE tenant_id = $1
	`, tenantID).Scan(&stripeSubID)
	if err == sql.ErrNoRows || !stripeSubID.Valid {
		return nil, status.Error(codes.NotFound, "no Stripe subscription found for tenant")
	}
	if err != nil {
		s.logger.WithError(err).Error("Failed to get Stripe subscription ID")
		return nil, status.Error(codes.Internal, "failed to get subscription")
	}

	// Fetch from Stripe
	sub, err := s.stripeClient.GetSubscription(ctx, stripeSubID.String)
	if err != nil {
		s.logger.WithError(err).Error("Failed to fetch Stripe subscription")
		return nil, status.Error(codes.Internal, "failed to fetch from Stripe")
	}

	info := s.stripeClient.ExtractSubscriptionInfo(sub)

	// Update local database
	_, err = s.db.ExecContext(ctx, `
		UPDATE purser.tenant_subscriptions
		SET stripe_subscription_status = $1,
		    stripe_current_period_end = $2,
		    updated_at = NOW()
		WHERE tenant_id = $3
	`, info.Status, info.CurrentPeriodEnd, tenantID)
	if err != nil {
		s.logger.WithError(err).Error("Failed to update subscription from Stripe")
		return nil, status.Error(codes.Internal, "failed to update subscription")
	}

	// Return updated subscription
	resp, err := s.GetSubscription(ctx, &pb.GetSubscriptionRequest{TenantId: tenantID})
	if err != nil {
		return nil, err
	}
	return resp.GetSubscription(), nil
}

// ============================================================================
// MOLLIE SERVICE IMPLEMENTATION
// ============================================================================

// CreateFirstPayment creates a Mollie first payment to establish a mandate
func (s *PurserServer) CreateFirstPayment(ctx context.Context, req *pb.CreateMollieFirstPaymentRequest) (*pb.CreateMollieFirstPaymentResponse, error) {
	if s.mollieClient == nil {
		return nil, status.Error(codes.Unavailable, "Mollie not configured")
	}

	tenantID := req.GetTenantId()
	tierID := req.GetTierId()
	method := req.GetMethod()
	redirectURL := req.GetRedirectUrl()

	if tenantID == "" || tierID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and tier_id are required")
	}
	if method == "" {
		return nil, status.Error(codes.InvalidArgument, "method is required (ideal, creditcard, bancontact)")
	}
	if redirectURL == "" {
		return nil, status.Error(codes.InvalidArgument, "redirect_url is required")
	}

	// Get tier price
	var basePrice float64
	var currency, tierName string
	err := s.db.QueryRowContext(ctx, `
		SELECT tier_name, base_price, currency FROM purser.billing_tiers WHERE id = $1
	`, tierID).Scan(&tierName, &basePrice, &currency)
	if err == sql.ErrNoRows {
		return nil, status.Errorf(codes.NotFound, "tier not found: %s", tierID)
	}
	if err != nil {
		s.logger.WithError(err).Error("Failed to get billing tier")
		return nil, status.Error(codes.Internal, "failed to get billing tier")
	}

	// Get or create Mollie customer
	var mollieCustomerID string
	err = s.db.QueryRowContext(ctx, `
		SELECT mollie_customer_id FROM purser.mollie_customers WHERE tenant_id = $1
	`, tenantID).Scan(&mollieCustomerID)

	if err == sql.ErrNoRows {
		// Get tenant primary user info via Commodore gRPC (not direct DB access)
		primaryUser, err := s.commodoreClient.GetTenantPrimaryUser(ctx, tenantID)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return nil, status.Error(codes.FailedPrecondition, "no billing email on account")
			}
			s.logger.WithFields(logging.Fields{
				"tenant_id": tenantID,
				"error":     err,
			}).Error("Failed to get tenant primary user from Commodore")
			return nil, status.Error(codes.Internal, "failed to get tenant info")
		}
		email := primaryUser.Email
		name := primaryUser.Name
		if name == "" {
			name = email
		}

		customer, err := s.mollieClient.CreateOrGetCustomer(ctx, mollie.CustomerInfo{
			TenantID: tenantID,
			Email:    email,
			Name:     name,
		})
		if err != nil {
			s.logger.WithError(err).Error("Failed to create Mollie customer")
			return nil, status.Error(codes.Internal, "failed to create Mollie customer")
		}

		// Store customer mapping
		_, err = s.db.ExecContext(ctx, `
			INSERT INTO purser.mollie_customers (tenant_id, mollie_customer_id)
			VALUES ($1, $2)
			ON CONFLICT (tenant_id) DO UPDATE SET mollie_customer_id = $2
		`, tenantID, customer.ID)
		if err != nil {
			s.logger.WithError(err).Error("Failed to store Mollie customer mapping")
			return nil, status.Error(codes.Internal, "failed to store customer mapping")
		}

		mollieCustomerID = customer.ID
	} else if err != nil {
		s.logger.WithError(err).Error("Failed to get Mollie customer")
		return nil, status.Error(codes.Internal, "failed to get Mollie customer")
	}

	// Build webhook URL (routed through Gateway)
	webhookBaseURL := strings.TrimSpace(os.Getenv("API_PUBLIC_URL"))
	if webhookBaseURL == "" {
		webhookBaseURL = strings.TrimSpace(os.Getenv("GATEWAY_PUBLIC_URL"))
	}
	webhookURL := ""
	if webhookBaseURL != "" {
		webhookURL = webhookBaseURL + "/webhooks/billing/mollie"
	}

	// Create first payment
	payment, err := s.mollieClient.CreateFirstPayment(ctx, mollie.FirstPaymentParams{
		CustomerID:  mollieCustomerID,
		TenantID:    tenantID,
		TierID:      tierID,
		Amount:      mollie.Amount(fmt.Sprintf("%.2f", basePrice), currency),
		Description: fmt.Sprintf("Subscription setup: %s", tierName),
		Method:      getMolliePaymentMethod(method),
		RedirectURL: redirectURL,
		WebhookURL:  webhookURL,
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to create Mollie first payment")
		return nil, status.Error(codes.Internal, "failed to create first payment")
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":   tenantID,
		"tier_id":     tierID,
		"payment_id":  payment.ID,
		"customer_id": mollieCustomerID,
	}).Info("Created Mollie first payment")

	return &pb.CreateMollieFirstPaymentResponse{
		PaymentUrl:       payment.Links.Checkout.Href,
		PaymentId:        payment.ID,
		MollieCustomerId: mollieCustomerID,
	}, nil
}

// CreateSubscription creates a Mollie subscription after mandate is valid
func (s *PurserServer) CreateMollieSubscription(ctx context.Context, req *pb.CreateMollieSubscriptionRequest) (*pb.CreateMollieSubscriptionResponse, error) {
	if s.mollieClient == nil {
		return nil, status.Error(codes.Unavailable, "Mollie not configured")
	}

	tenantID := req.GetTenantId()
	tierID := req.GetTierId()
	mandateID := req.GetMandateId()

	if tenantID == "" || tierID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and tier_id are required")
	}

	// Get Mollie customer ID
	var mollieCustomerID string
	err := s.db.QueryRowContext(ctx, `
		SELECT mollie_customer_id FROM purser.mollie_customers WHERE tenant_id = $1
	`, tenantID).Scan(&mollieCustomerID)
	if err == sql.ErrNoRows {
		return nil, status.Error(codes.FailedPrecondition, "no Mollie customer found - complete first payment first")
	}
	if err != nil {
		s.logger.WithError(err).Error("Failed to get Mollie customer")
		return nil, status.Error(codes.Internal, "failed to get Mollie customer")
	}

	// If no mandate ID provided, find a valid one
	if mandateID == "" {
		mandates, err := s.mollieClient.ListMandates(ctx, mollieCustomerID)
		if err != nil {
			s.logger.WithError(err).Error("Failed to list Mollie mandates")
			return nil, status.Error(codes.Internal, "failed to list mandates")
		}
		for _, m := range mandates {
			if m.Status == "valid" {
				mandateID = m.ID
				break
			}
		}
		if mandateID == "" {
			return nil, status.Error(codes.FailedPrecondition, "no valid mandate found - complete first payment first")
		}
	}

	// Get tier price
	var basePrice float64
	var currency, tierName string
	err = s.db.QueryRowContext(ctx, `
		SELECT tier_name, base_price, currency FROM purser.billing_tiers WHERE id = $1
	`, tierID).Scan(&tierName, &basePrice, &currency)
	if err == sql.ErrNoRows {
		return nil, status.Errorf(codes.NotFound, "tier not found: %s", tierID)
	}
	if err != nil {
		s.logger.WithError(err).Error("Failed to get billing tier")
		return nil, status.Error(codes.Internal, "failed to get billing tier")
	}

	// Build webhook URL
	webhookBaseURL := strings.TrimSpace(os.Getenv("API_PUBLIC_URL"))
	if webhookBaseURL == "" {
		webhookBaseURL = strings.TrimSpace(os.Getenv("GATEWAY_PUBLIC_URL"))
	}
	webhookURL := ""
	if webhookBaseURL != "" {
		webhookURL = webhookBaseURL + "/webhooks/billing/mollie"
	}

	// Create subscription
	sub, err := s.mollieClient.CreateSubscription(ctx, mollie.SubscriptionParams{
		CustomerID:  mollieCustomerID,
		TenantID:    tenantID,
		TierID:      tierID,
		Amount:      mollie.Amount(fmt.Sprintf("%.2f", basePrice), currency),
		Interval:    "1 month",
		Description: fmt.Sprintf("Subscription: %s", tierName),
		WebhookURL:  webhookURL,
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to create Mollie subscription")
		return nil, status.Error(codes.Internal, "failed to create subscription")
	}

	// Update tenant subscription with Mollie subscription ID
	_, err = s.db.ExecContext(ctx, `
		UPDATE purser.tenant_subscriptions
		SET mollie_subscription_id = $1, updated_at = NOW()
		WHERE tenant_id = $2
	`, sub.ID, tenantID)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to store Mollie subscription ID")
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":       tenantID,
		"tier_id":         tierID,
		"subscription_id": sub.ID,
	}).Info("Created Mollie subscription")

	nextPayment := ""
	if sub.NextPaymentDate != nil {
		nextPayment = sub.NextPaymentDate.String()
	}

	return &pb.CreateMollieSubscriptionResponse{
		SubscriptionId:  sub.ID,
		Status:          string(sub.Status),
		NextPaymentDate: nextPayment,
	}, nil
}

// ListMandates lists available Mollie mandates for a tenant
func (s *PurserServer) ListMandates(ctx context.Context, req *pb.ListMollieMandatesRequest) (*pb.ListMollieMandatesResponse, error) {
	if s.mollieClient == nil {
		return nil, status.Error(codes.Unavailable, "Mollie not configured")
	}

	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	// Get Mollie customer ID
	var mollieCustomerID string
	err := s.db.QueryRowContext(ctx, `
		SELECT mollie_customer_id FROM purser.mollie_customers WHERE tenant_id = $1
	`, tenantID).Scan(&mollieCustomerID)
	if err == sql.ErrNoRows {
		return &pb.ListMollieMandatesResponse{Mandates: []*pb.MollieMandate{}}, nil
	}
	if err != nil {
		s.logger.WithError(err).Error("Failed to get Mollie customer")
		return nil, status.Error(codes.Internal, "failed to get Mollie customer")
	}

	mandates, err := s.mollieClient.ListMandates(ctx, mollieCustomerID)
	if err != nil {
		s.logger.WithError(err).Error("Failed to list Mollie mandates")
		return nil, status.Error(codes.Internal, "failed to list mandates")
	}

	result := make([]*pb.MollieMandate, 0, len(mandates))
	for _, m := range mandates {
		info := s.mollieClient.ExtractMandateInfo(m, mollieCustomerID)
		details, _ := structpb.NewStruct(info.Details)
		result = append(result, &pb.MollieMandate{
			MollieMandateId:  info.MollieMandateID,
			MollieCustomerId: info.MollieCustomerID,
			Status:           info.Status,
			Method:           info.Method,
			Details:          details,
			CreatedAt:        timestamppb.New(info.CreatedAt),
		})
	}

	return &pb.ListMollieMandatesResponse{Mandates: result}, nil
}

// CancelSubscription cancels a Mollie subscription
func (s *PurserServer) CancelMollieSubscription(ctx context.Context, req *pb.CancelMollieSubscriptionRequest) (*emptypb.Empty, error) {
	if s.mollieClient == nil {
		return nil, status.Error(codes.Unavailable, "Mollie not configured")
	}

	tenantID := req.GetTenantId()
	subscriptionID := req.GetSubscriptionId()

	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	// Get Mollie customer ID
	var mollieCustomerID string
	err := s.db.QueryRowContext(ctx, `
		SELECT mollie_customer_id FROM purser.mollie_customers WHERE tenant_id = $1
	`, tenantID).Scan(&mollieCustomerID)
	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "no Mollie customer found")
	}
	if err != nil {
		s.logger.WithError(err).Error("Failed to get Mollie customer")
		return nil, status.Error(codes.Internal, "failed to get Mollie customer")
	}

	// If no subscription ID provided, get from database
	if subscriptionID == "" {
		var subID sql.NullString
		err = s.db.QueryRowContext(ctx, `
			SELECT mollie_subscription_id FROM purser.tenant_subscriptions WHERE tenant_id = $1
		`, tenantID).Scan(&subID)
		if err != nil || !subID.Valid {
			return nil, status.Error(codes.NotFound, "no Mollie subscription found")
		}
		subscriptionID = subID.String
	}

	err = s.mollieClient.CancelSubscription(ctx, mollieCustomerID, subscriptionID)
	if err != nil {
		s.logger.WithError(err).Error("Failed to cancel Mollie subscription")
		return nil, status.Error(codes.Internal, "failed to cancel subscription")
	}

	// Clear subscription ID from database
	_, err = s.db.ExecContext(ctx, `
		UPDATE purser.tenant_subscriptions
		SET mollie_subscription_id = NULL, status = 'cancelled', cancelled_at = NOW(), updated_at = NOW()
		WHERE tenant_id = $1
	`, tenantID)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to update subscription after Mollie cancellation")
	}

	return &emptypb.Empty{}, nil
}

// getMolliePaymentMethod converts string to Mollie payment method
func getMolliePaymentMethod(method string) mollielib.PaymentMethod {
	return mollielib.PaymentMethod(method)
}

// ============================================================================
// BILLING DETAILS MANAGEMENT
// ============================================================================

// GetBillingDetails returns billing details for a tenant
func (s *PurserServer) GetBillingDetails(ctx context.Context, req *pb.GetBillingDetailsRequest) (*pb.BillingDetails, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	var billingEmail, billingCompany, taxID sql.NullString
	var billingAddress []byte
	var updatedAt time.Time

	err := s.db.QueryRowContext(ctx, `
		SELECT billing_email, billing_company, tax_id, billing_address, updated_at
		FROM purser.tenant_subscriptions
		WHERE tenant_id = $1 AND status != 'cancelled'
		ORDER BY created_at DESC
		LIMIT 1
	`, tenantID).Scan(&billingEmail, &billingCompany, &taxID, &billingAddress, &updatedAt)

	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "no subscription found for tenant")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	details := &pb.BillingDetails{
		TenantId:  tenantID,
		UpdatedAt: timestamppb.New(updatedAt),
	}

	if billingEmail.Valid {
		details.Email = billingEmail.String
	}
	if billingCompany.Valid {
		details.Company = billingCompany.String
	}
	if taxID.Valid {
		details.VatNumber = taxID.String
	}
	details.Address = scanBillingAddress(billingAddress)

	// IsComplete = email AND (address with at least line1, city, postalCode, country)
	details.IsComplete = details.Email != "" && details.Address != nil &&
		details.Address.Street != "" && details.Address.City != "" &&
		details.Address.PostalCode != "" && details.Address.Country != ""

	return details, nil
}

// UpdateBillingDetails updates billing details for a tenant
func (s *PurserServer) UpdateBillingDetails(ctx context.Context, req *pb.UpdateBillingDetailsRequest) (*pb.BillingDetails, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	// Build dynamic update query based on provided fields
	updates := []string{}
	args := []interface{}{}
	argIdx := 1

	if req.Email != nil {
		updates = append(updates, fmt.Sprintf("billing_email = $%d", argIdx))
		args = append(args, *req.Email)
		argIdx++
	}
	if req.Company != nil {
		updates = append(updates, fmt.Sprintf("billing_company = $%d", argIdx))
		args = append(args, *req.Company)
		argIdx++
	}
	if req.VatNumber != nil {
		updates = append(updates, fmt.Sprintf("tax_id = $%d", argIdx))
		args = append(args, *req.VatNumber)
		argIdx++
	}
	if req.Address != nil {
		// Validate and normalize country code
		countryCode := countries.Normalize(req.Address.Country)
		if !countries.IsValid(countryCode) {
			return nil, status.Errorf(codes.InvalidArgument, "invalid country code %q: must be a valid ISO 3166-1 alpha-2 code (e.g., US, DE, NL)", req.Address.Country)
		}

		// Convert proto address to JSONB
		addressJSON, err := json.Marshal(map[string]string{
			"street":      req.Address.Street,
			"city":        req.Address.City,
			"state":       req.Address.State,
			"postal_code": req.Address.PostalCode,
			"country":     countryCode,
		})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to serialize address: %v", err)
		}
		updates = append(updates, fmt.Sprintf("billing_address = $%d", argIdx))
		args = append(args, addressJSON)
		argIdx++
	}

	if len(updates) == 0 {
		// No updates provided, just return current details
		return s.GetBillingDetails(ctx, &pb.GetBillingDetailsRequest{TenantId: tenantID})
	}

	// Always update updated_at
	updates = append(updates, "updated_at = NOW()")

	// Add tenant_id as last arg for WHERE clause
	args = append(args, tenantID)

	query := fmt.Sprintf(`
		UPDATE purser.tenant_subscriptions
		SET %s
		WHERE tenant_id = $%d AND status != 'cancelled'
	`, strings.Join(updates, ", "), argIdx)

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get rows affected: %v", err)
	}
	if rowsAffected == 0 {
		return nil, status.Error(codes.NotFound, "no active subscription found for tenant")
	}

	s.logger.WithField("tenant_id", tenantID).Info("Billing details updated")

	// Return updated billing details
	return s.GetBillingDetails(ctx, &pb.GetBillingDetailsRequest{TenantId: tenantID})
}

// ============================================================================
// X402 Service Methods
// ============================================================================

// GetPaymentRequirements returns the x402 payment requirements for a 402 response
// Returns multiple network options (Base + Arbitrum) so clients can choose their preferred network.
// Per x402 spec, uses the platform-wide payTo address (HD index 0).
// tenantID is optional - the payer is identified from the signed authorization's `from` field.
func (s *PurserServer) GetPaymentRequirements(ctx context.Context, req *pb.GetPaymentRequirementsRequest) (*pb.PaymentRequirements, error) {
	// tenantID is optional - we use platform-wide payTo address per x402 spec
	// The payer's identity comes from the authorization signature, not the request

	// Get platform-wide x402 payTo address (HD index 0)
	payToAddr, err := s.x402handler.GetPlatformX402Address()
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to get platform x402 address")
		//nolint:nilerr // error returned in response struct for client handling
		return &pb.PaymentRequirements{
			X402Version: 1,
			Error:       "failed to get payment address",
			TopupUrl:    "/account/billing",
		}, nil
	}

	// Get all x402-enabled networks from the registry
	networks := s.x402handler.GetSupportedNetworks()
	resource := req.GetResource()

	// Build accepts list with all supported networks
	accepts := make([]*pb.PaymentRequirement, 0, len(networks))
	for _, network := range networks {
		accepts = append(accepts, &pb.PaymentRequirement{
			Scheme:            "exact",
			Network:           network.Name,
			MaxAmountRequired: "100000000", // 100 USDC max (6 decimals)
			PayTo:             payToAddr,
			Asset:             network.USDCContract,
			MaxTimeoutSeconds: 60,
			Resource:          resource,
			Description:       "Streaming, transcoding & storage credits via " + network.DisplayName,
		})
	}

	return &pb.PaymentRequirements{
		X402Version: 1,
		Accepts:     accepts,
		TopupUrl:    "/account/billing",
	}, nil
}

// VerifyX402Payment verifies an x402 payment without settling.
// tenantID is optional since x402 uses platform-wide payTo address.
// The payer is identified from the authorization's `from` field.
func (s *PurserServer) VerifyX402Payment(ctx context.Context, req *pb.VerifyX402PaymentRequest) (*pb.VerifyX402PaymentResponse, error) {
	// tenantID is optional - verification works without it since we use platform payTo
	tenantID := req.GetTenantId()

	payment := req.GetPayment()
	if payment == nil {
		return nil, status.Error(codes.InvalidArgument, "payment required")
	}

	// Convert proto to handler type
	handlerPayload := &handlers.X402PaymentPayload{
		X402Version: int(payment.GetX402Version()),
		Scheme:      payment.GetScheme(),
		Network:     payment.GetNetwork(),
	}
	if payment.GetPayload() != nil {
		handlerPayload.Payload = &handlers.X402ExactPayload{
			Signature: payment.GetPayload().GetSignature(),
		}
		if payment.GetPayload().GetAuthorization() != nil {
			auth := payment.GetPayload().GetAuthorization()
			handlerPayload.Payload.Authorization = &handlers.X402Authorization{
				From:        auth.GetFrom(),
				To:          auth.GetTo(),
				Value:       auth.GetValue(),
				ValidAfter:  auth.GetValidAfter(),
				ValidBefore: auth.GetValidBefore(),
				Nonce:       auth.GetNonce(),
			}
		}
	}

	result, err := s.x402handler.VerifyPayment(ctx, tenantID, handlerPayload, req.GetClientIp())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "verification failed: %v", err)
	}

	return &pb.VerifyX402PaymentResponse{
		Valid:                  result.Valid,
		Error:                  result.Error,
		PayerAddress:           result.PayerAddress,
		AmountCents:            result.AmountCents,
		RequiresBillingDetails: result.RequiresBillingDetails,
		IsAuthOnly:             result.IsAuthOnly,
	}, nil
}

// SettleX402Payment settles an x402 payment and credits the balance.
// tenantID is optional for auth-only payments (value=0) - the payer is identified from the authorization.
func (s *PurserServer) SettleX402Payment(ctx context.Context, req *pb.SettleX402PaymentRequest) (*pb.SettleX402PaymentResponse, error) {
	// tenantID is optional - for auth-only payments (value=0), the payer is identified from the authorization
	tenantID := req.GetTenantId()

	payment := req.GetPayment()
	if payment == nil {
		return nil, status.Error(codes.InvalidArgument, "payment required")
	}

	// Convert proto to handler type
	handlerPayload := &handlers.X402PaymentPayload{
		X402Version: int(payment.GetX402Version()),
		Scheme:      payment.GetScheme(),
		Network:     payment.GetNetwork(),
	}
	if payment.GetPayload() != nil {
		handlerPayload.Payload = &handlers.X402ExactPayload{
			Signature: payment.GetPayload().GetSignature(),
		}
		if payment.GetPayload().GetAuthorization() != nil {
			auth := payment.GetPayload().GetAuthorization()
			handlerPayload.Payload.Authorization = &handlers.X402Authorization{
				From:        auth.GetFrom(),
				To:          auth.GetTo(),
				Value:       auth.GetValue(),
				ValidAfter:  auth.GetValidAfter(),
				ValidBefore: auth.GetValidBefore(),
				Nonce:       auth.GetNonce(),
			}
		}
	}

	result, err := s.x402handler.SettlePayment(ctx, tenantID, handlerPayload, req.GetClientIp())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "settlement failed: %v", err)
	}

	// Auto-reactivate suspended tenant if balance goes positive after x402 top-up
	if result.Success && !result.IsAuthOnly && tenantID != "" && result.NewBalanceCents >= 0 {
		res, err := s.db.ExecContext(ctx, `
			UPDATE purser.tenant_subscriptions
			SET status = 'active', updated_at = NOW()
			WHERE tenant_id = $1 AND status = 'suspended'
		`, tenantID)
		if err != nil {
			s.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to check/reactivate suspended subscription after x402 top-up")
		} else if rowsAffected, _ := res.RowsAffected(); rowsAffected > 0 {
			s.logger.WithFields(map[string]interface{}{
				"tenant_id":   tenantID,
				"new_balance": result.NewBalanceCents,
			}).Info("Reactivated suspended tenant after x402 top-up")

			if s.commodoreClient != nil {
				go func() {
					invalidateCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					resp, err := s.commodoreClient.InvalidateTenantCache(invalidateCtx, tenantID, "balance_topped_up")
					if err != nil {
						s.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to invalidate tenant cache after x402 reactivation")
					} else {
						s.logger.WithFields(map[string]interface{}{
							"tenant_id":           tenantID,
							"entries_invalidated": resp.EntriesInvalidated,
						}).Info("Invalidated media plane cache after x402 reactivation")
					}
				}()
			}
		}
	}

	return &pb.SettleX402PaymentResponse{
		Success:         result.Success,
		Error:           result.Error,
		TxHash:          result.TxHash,
		CreditedCents:   result.CreditedCents,
		Currency:        result.Currency,
		NewBalanceCents: result.NewBalanceCents,
		InvoiceNumber:   result.InvoiceNumber,
		IsAuthOnly:      result.IsAuthOnly,
		PayerAddress:    result.PayerAddress,
	}, nil
}

// GetTenantX402Address returns the per-tenant x402 deposit address
func (s *PurserServer) GetTenantX402Address(ctx context.Context, req *pb.GetTenantX402AddressRequest) (*pb.GetTenantX402AddressResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	address, derivationIndex, newlyCreated, err := s.x402handler.GetOrCreateTenantX402Address(tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get x402 address: %v", err)
	}

	return &pb.GetTenantX402AddressResponse{
		Address:         address,
		DerivationIndex: derivationIndex,
		NewlyCreated:    newlyCreated,
	}, nil
}

// ============================================================================
// INVOICE HELPERS - Parse usage_details and generate typed fields
// ============================================================================

// parseUsageDetailsToSummary extracts typed UsageSummary from usage_details JSON
func parseUsageDetailsToSummary(usageDetails map[string]interface{}, tenantID string, periodStart, periodEnd *timestamppb.Timestamp) *pb.UsageSummary {
	if usageDetails == nil {
		return nil
	}

	summary := &pb.UsageSummary{
		TenantId:            tenantID,
		StreamHours:         floatFromUsage(usageDetails, "stream_hours"),
		EgressGb:            floatFromUsage(usageDetails, "egress_gb"),
		PeakBandwidthMbps:   floatFromUsage(usageDetails, "peak_bandwidth_mbps"),
		AverageStorageGb:    floatFromUsage(usageDetails, "average_storage_gb"),
		LivepeerH264Seconds: floatFromUsage(usageDetails, "livepeer_h264_seconds"),
		LivepeerVp9Seconds:  floatFromUsage(usageDetails, "livepeer_vp9_seconds"),
		LivepeerAv1Seconds:  floatFromUsage(usageDetails, "livepeer_av1_seconds"),
		LivepeerHevcSeconds: floatFromUsage(usageDetails, "livepeer_hevc_seconds"),
		NativeAvH264Seconds: floatFromUsage(usageDetails, "native_av_h264_seconds"),
		NativeAvVp9Seconds:  floatFromUsage(usageDetails, "native_av_vp9_seconds"),
		NativeAvAv1Seconds:  floatFromUsage(usageDetails, "native_av_av1_seconds"),
		NativeAvHevcSeconds: floatFromUsage(usageDetails, "native_av_hevc_seconds"),
		NativeAvAacSeconds:  floatFromUsage(usageDetails, "native_av_aac_seconds"),
		NativeAvOpusSeconds: floatFromUsage(usageDetails, "native_av_opus_seconds"),
		TotalStreams:        int32(floatFromUsage(usageDetails, "total_streams")),
		TotalViewers:        int32(floatFromUsage(usageDetails, "total_viewers")),
		ViewerHours:         floatFromUsage(usageDetails, "viewer_hours"),
		MaxViewers:          int32(floatFromUsage(usageDetails, "max_viewers")),
		UniqueUsers:         int32(floatFromUsage(usageDetails, "unique_users")),
	}

	if periodStart != nil && periodEnd != nil {
		start := periodStart.AsTime()
		end := periodEnd.AsTime()
		summary.Period = start.Format(time.RFC3339) + "/" + end.Format(time.RFC3339)
		summary.Granularity = deriveGranularity(start, end)
	}

	return summary
}

// generateInvoiceLineItems creates typed line items from usage and tier info
func generateInvoiceLineItems(usageDetails map[string]interface{}, baseAmount, meteredAmount float64) []*pb.LineItem {
	var items []*pb.LineItem

	// 1. Base tier line item
	tierName := "Service"
	if tierInfo, ok := usageDetails["tier_info"].(map[string]interface{}); ok {
		if dn, ok := tierInfo["display_name"].(string); ok && dn != "" {
			tierName = dn
		}
	}
	items = append(items, &pb.LineItem{
		Description: tierName + " Tier",
		Quantity:    1,
		UnitPrice:   baseAmount,
		Total:       baseAmount,
	})

	// 2. Usage metrics (informational, zero price)
	usageMetrics := []struct {
		key         string
		displayName string
		multiplier  float64
	}{
		{"viewer_hours", "Delivered Minutes", 60},
		{"average_storage_gb", "Storage (GB)", 1},
		{"stream_hours", "Stream Hours", 1},
		{"egress_gb", "Egress (GB)", 1},
	}

	for _, m := range usageMetrics {
		val := floatFromUsage(usageDetails, m.key)
		if val > 0 {
			displayVal := val * m.multiplier
			items = append(items, &pb.LineItem{
				Description: m.displayName,
				Quantity:    int32(displayVal),
				UnitPrice:   0,
				Total:       0,
			})
		}
	}

	// 3. Processing/transcoding line items (informational)
	codecMetrics := []struct {
		key         string
		displayName string
	}{
		{"livepeer_h264_seconds", "H264 Transcoding"},
		{"livepeer_vp9_seconds", "VP9 Transcoding"},
		{"livepeer_av1_seconds", "AV1 Transcoding"},
		{"livepeer_hevc_seconds", "HEVC Transcoding"},
		{"native_av_h264_seconds", "H264 Processing"},
		{"native_av_vp9_seconds", "VP9 Processing"},
		{"native_av_av1_seconds", "AV1 Processing"},
		{"native_av_hevc_seconds", "HEVC Processing"},
		{"native_av_aac_seconds", "AAC Transcoding"},
		{"native_av_opus_seconds", "Opus Transcoding"},
	}

	for _, m := range codecMetrics {
		seconds := floatFromUsage(usageDetails, m.key)
		if seconds > 0 {
			minutes := int32(seconds / 60)
			if minutes < 1 {
				minutes = 1
			}
			items = append(items, &pb.LineItem{
				Description: m.displayName,
				Quantity:    minutes,
				UnitPrice:   0,
				Total:       0,
			})
		}
	}

	// 4. Overage charges (if any)
	if meteredAmount > 0 {
		items = append(items, &pb.LineItem{
			Description: "Overage charges",
			Quantity:    1,
			UnitPrice:   meteredAmount,
			Total:       meteredAmount,
		})
	}

	return items
}

// parseGeoBreakdown extracts CountryMetrics from usage_details
func parseGeoBreakdown(usageDetails map[string]interface{}) []*pb.CountryMetrics {
	geoRaw, ok := usageDetails["geo_breakdown"]
	if !ok {
		return nil
	}

	geoList, ok := geoRaw.([]interface{})
	if !ok {
		return nil
	}

	var result []*pb.CountryMetrics
	for _, item := range geoList {
		geoMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		result = append(result, &pb.CountryMetrics{
			CountryCode: stringFromUsage(geoMap, "country_code"),
			ViewerCount: int32(floatFromUsage(geoMap, "viewer_count")),
			ViewerHours: floatFromUsage(geoMap, "viewer_hours"),
			EgressGb:    floatFromUsage(geoMap, "egress_gb"),
		})
	}
	return result
}

func floatFromUsage(m map[string]interface{}, key string) float64 {
	if m == nil {
		return 0
	}
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch val := v.(type) {
	case float64:
		return val
	case float32:
		return float64(val)
	case int:
		return float64(val)
	case int32:
		return float64(val)
	case int64:
		return float64(val)
	case json.Number:
		f, _ := val.Float64()
		return f
	default:
		return 0
	}
}

func stringFromUsage(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func deriveGranularity(start, end time.Time) string {
	duration := end.Sub(start)
	if duration >= 28*24*time.Hour {
		return "monthly"
	}
	if duration >= 24*time.Hour {
		return "daily"
	}
	return "hourly"
}
