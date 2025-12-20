package grpc

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
	"frameworks/pkg/pagination"
	pb "frameworks/pkg/proto"

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
	if data == nil || len(data) == 0 {
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
	if data == nil || len(data) == 0 {
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
	if data == nil || len(data) == 0 {
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
	db      *sql.DB
	logger  logging.Logger
	metrics *ServerMetrics
}

// NewPurserServer creates a new Purser gRPC server
func NewPurserServer(db *sql.DB, logger logging.Logger, metrics *ServerMetrics) *PurserServer {
	return &PurserServer{
		db:      db,
		logger:  logger,
		metrics: metrics,
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

	// Build keyset query - for billing tiers we use (sort_order, id) for stable ordering
	if params.Cursor != nil {
		// Use cursor timestamp as sort_order proxy (convert to int for comparison)
		sortOrder := params.Cursor.Timestamp.UnixMilli()
		if whereClause != "" {
			whereClause += " AND"
		} else {
			whereClause = "WHERE"
		}
		// Direction-aware keyset condition
		if params.Direction == pagination.Backward {
			whereClause += fmt.Sprintf(" (sort_order, id) < ($%d, $%d)", argIdx, argIdx+1)
		} else {
			whereClause += fmt.Sprintf(" (sort_order, id) > ($%d, $%d)", argIdx, argIdx+1)
		}
		args = append(args, sortOrder, params.Cursor.ID)
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
		       is_active, sort_order, is_enterprise,
		       created_at, updated_at
		FROM purser.billing_tiers
		%s
		ORDER BY sort_order %s, id %s
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
			&tier.IsActive, &tier.SortOrder, &tier.IsEnterprise,
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
		// Use sort_order as timestamp for cursor (convert int32 to milliseconds)
		startCursor = pagination.EncodeCursor(time.UnixMilli(int64(first.SortOrder)), first.Id)
		endCursor = pagination.EncodeCursor(time.UnixMilli(int64(last.SortOrder)), last.Id)
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
		       is_active, sort_order, is_enterprise,
		       created_at, updated_at
		FROM purser.billing_tiers
		WHERE id = $1
	`, tierID).Scan(
		&tier.Id, &tier.TierName, &tier.DisplayName, &tier.Description,
		&tier.BasePrice, &tier.Currency, &tier.BillingPeriod,
		&bandwidthAlloc, &storageAlloc, &computeAlloc, &features,
		&tier.SupportLevel, &tier.SlaLevel, &tier.MeteringEnabled, &overageRates,
		&tier.IsActive, &tier.SortOrder, &tier.IsEnterprise,
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

// NOTE: Usage ingestion is handled via Kafka (billing.usage_reports topic)
// consumed by JobManager in handlers/jobs.go. No gRPC ingestion endpoint needed.
// The processUsageSummary and updateInvoiceDraft logic lives in handlers/jobs.go

// GetUsageRecords returns usage records for a tenant with cursor pagination
func (s *PurserServer) GetUsageRecords(ctx context.Context, req *pb.GetUsageRecordsRequest) (*pb.UsageRecordsResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
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
	if req.GetBillingMonth() != "" {
		// Convert billing_month (YYYY-MM) to timestamp range for consistency
		// Parse as first of month, query for that month's period boundaries
		monthStart, err := time.Parse("2006-01", req.GetBillingMonth())
		if err == nil {
			monthEnd := monthStart.AddDate(0, 1, 0)
			whereClause += fmt.Sprintf(" AND period_start >= $%d AND period_end < $%d", argIdx, argIdx+1)
			args = append(args, monthStart, monthEnd)
			argIdx += 2
		} else {
			// Fallback to string match if parse fails
			whereClause += fmt.Sprintf(" AND billing_month = $%d", argIdx)
			args = append(args, req.GetBillingMonth())
			argIdx++
		}
	}

	// Add cursor condition for keyset pagination (direction-aware)
	if params.Cursor != nil {
		if params.Direction == pagination.Backward {
			whereClause += fmt.Sprintf(" AND (created_at, id) > ($%d, $%d)", argIdx, argIdx+1)
		} else {
			whereClause += fmt.Sprintf(" AND (created_at, id) < ($%d, $%d)", argIdx, argIdx+1)
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
		SELECT id, tenant_id, cluster_id, usage_type, usage_value, usage_details, billing_month, created_at
		FROM purser.usage_records
		%s
		ORDER BY created_at %s, id %s
		LIMIT $%d
	`, whereClause, orderDir, orderDir, argIdx)
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

		err := rows.Scan(&rec.Id, &rec.TenantId, &clusterID, &rec.UsageType, &rec.UsageValue, &usageDetailsBytes, &rec.BillingMonth, &createdAt)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to scan usage record")
			continue
		}

		if clusterID.Valid {
			rec.ClusterId = clusterID.String
		}
		rec.CreatedAt = timestamppb.New(createdAt)

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
		startCursor = pagination.EncodeCursor(firstRec.CreatedAt.AsTime(), firstRec.Id)
		endCursor = pagination.EncodeCursor(lastRec.CreatedAt.AsTime(), lastRec.Id)
	}

	resp := &pb.UsageRecordsResponse{
		UsageRecords: records,
		TenantId:     tenantID,
		Filters: &pb.UsageFilters{
			ClusterId:    req.GetClusterId(),
			UsageType:    req.GetUsageType(),
			BillingMonth: req.GetBillingMonth(),
		},
		Pagination: pagination.BuildResponse(resultsLen, params.Limit, params.Direction, int32(len(records)), startCursor, endCursor),
	}

	return resp, nil
}

// CheckUserLimit checks if a tenant can add more users
func (s *PurserServer) CheckUserLimit(ctx context.Context, req *pb.CheckUserLimitRequest) (*pb.CheckUserLimitResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	// Get current user count from commodore
	var currentUsers int32
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM commodore.users WHERE tenant_id = $1 AND is_active = true
	`, tenantID).Scan(&currentUsers)
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Warn("Failed to count users, allowing by default")
		return &pb.CheckUserLimitResponse{Allowed: true}, nil
	}

	// Get tier limit
	var maxUsers sql.NullInt32
	err = s.db.QueryRowContext(ctx, `
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
	var paymentMethod, paymentReference, taxID sql.NullString
	var taxRate sql.NullFloat64

	err := s.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, tier_id, status, billing_email, started_at,
		       trial_ends_at, next_billing_date, cancelled_at,
		       payment_method, payment_reference, tax_id, tax_rate,
		       created_at, updated_at
		FROM purser.tenant_subscriptions
		WHERE tenant_id = $1 AND status != 'cancelled'
		ORDER BY created_at DESC
		LIMIT 1
	`, tenantID).Scan(&sub.Id, &sub.TenantId, &sub.TierId, &sub.Status, &sub.BillingEmail,
		&startedAt, &trialEndsAt, &nextBillingDate, &cancelledAt,
		&paymentMethod, &paymentReference, &taxID, &taxRate,
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
	if trialEndsAt.Valid {
		sub.TrialEndsAt = timestamppb.New(trialEndsAt.Time)
	}
	if nextBillingDate.Valid {
		sub.NextBillingDate = timestamppb.New(nextBillingDate.Time)
	}
	if cancelledAt.Valid {
		sub.CancelledAt = timestamppb.New(cancelledAt.Time)
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

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO purser.tenant_subscriptions (id, tenant_id, tier_id, status, billing_email, started_at, trial_ends_at, payment_method, created_at, updated_at)
		VALUES ($1, $2, $3, 'active', $4, $5, $6, $7, $5, $5)
	`, subID, tenantID, tierID, billingEmail, now, trialEndsAt, req.GetPaymentMethod())

	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create subscription: %v", err)
	}

	sub := &pb.TenantSubscription{
		Id:           subID,
		TenantId:     tenantID,
		TierId:       tierID,
		Status:       "active",
		BillingEmail: billingEmail,
		StartedAt:    timestamppb.New(now),
		CreatedAt:    timestamppb.New(now),
		UpdatedAt:    timestamppb.New(now),
	}
	if trialEndsAt.Valid {
		sub.TrialEndsAt = timestamppb.New(trialEndsAt.Time)
	}
	if req.GetPaymentMethod() != "" {
		pm := req.GetPaymentMethod()
		sub.PaymentMethod = &pm
	}

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
	return resp.Subscription, nil
}

// CancelSubscription cancels a tenant's subscription
func (s *PurserServer) CancelSubscription(ctx context.Context, req *pb.CancelSubscriptionRequest) (*emptypb.Empty, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

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

	// Build query with optional tenant filter for user calls
	query := `
		SELECT i.id, i.tenant_id, i.amount, i.base_amount, i.metered_amount, i.currency, i.status,
		       i.due_date, i.paid_at, i.usage_details, i.created_at, i.updated_at, s.tier_id
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
		&invoice.MeteredAmount, &invoice.Currency, &invoice.Status,
		&dueDate, &paidAt, &usageDetailsBytes, &createdAt, &updatedAt, &tierID)

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

	// Convert usage_details JSONB to protobuf Struct
	if len(usageDetailsBytes) > 0 {
		var detailsMap map[string]interface{}
		if json.Unmarshal(usageDetailsBytes, &detailsMap) == nil {
			invoice.UsageDetails = mapToProtoStruct(detailsMap)
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

	// Direction-aware keyset condition
	if params.Cursor != nil {
		if params.Direction == pagination.Backward {
			whereClause += fmt.Sprintf(" AND (created_at, id) > ($%d, $%d)", argIdx, argIdx+1)
		} else {
			whereClause += fmt.Sprintf(" AND (created_at, id) < ($%d, $%d)", argIdx, argIdx+1)
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
		SELECT id, tenant_id, amount, base_amount, metered_amount, currency, status,
		       due_date, paid_at, usage_details, created_at, updated_at
		FROM purser.billing_invoices
		%s
		ORDER BY created_at %s, id %s
		LIMIT $%d
	`, whereClause, orderDir, orderDir, argIdx)
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

		err := rows.Scan(&inv.Id, &inv.TenantId, &inv.Amount, &inv.BaseAmount,
			&inv.MeteredAmount, &inv.Currency, &inv.Status,
			&dueDate, &paidAt, &usageDetails, &createdAt, &updatedAt)
		if err != nil {
			continue
		}

		inv.DueDate = timestamppb.New(dueDate)
		inv.CreatedAt = timestamppb.New(createdAt)
		inv.UpdatedAt = timestamppb.New(updatedAt)
		if paidAt.Valid {
			inv.PaidAt = timestamppb.New(paidAt.Time)
		}
		// Convert usage_details JSONB to protobuf Struct
		if len(usageDetails) > 0 {
			var details map[string]interface{}
			if json.Unmarshal(usageDetails, &details) == nil {
				inv.UsageDetails = mapToProtoStruct(details)
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
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.Id)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.Id)
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

	// Generate payment ID
	paymentID := fmt.Sprintf("pay_%d", time.Now().UnixNano())
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
	if os.Getenv("ETHERSCAN_API_KEY") != "" {
		methods = append(methods, "crypto_eth", "crypto_usdc", "crypto_lpt")
	}
	if os.Getenv("BLOCKCYPHER_API_KEY") != "" || true { // BlockCypher has free tier
		methods = append(methods, "crypto_btc")
	}

	return methods
}

// createStripePayment creates a Stripe Payment Intent
func (s *PurserServer) createStripePayment(invoiceID, tenantID string, amount float64, currency string) (string, string, error) {
	stripeKey := os.Getenv("STRIPE_SECRET_KEY")
	if stripeKey == "" {
		return "", "", fmt.Errorf("Stripe not configured")
	}

	// Create Stripe Payment Intent via API
	data := strings.NewReader(fmt.Sprintf(
		"amount=%d&currency=%s&metadata[invoice_id]=%s&metadata[tenant_id]=%s",
		int64(amount*100), currency, invoiceID, tenantID,
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
		return "", "", fmt.Errorf("stripe API request failed: %v", err)
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
		return "", "", fmt.Errorf("failed to decode stripe response: %v", err)
	}

	if result.ID == "" || result.ClientSecret == "" {
		return "", "", fmt.Errorf("invalid stripe response: missing payment intent ID or client secret")
	}

	webappURL := os.Getenv("WEBAPP_PUBLIC_URL")
	if webappURL == "" {
		webappURL = "https://app.example.com"
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

	webappURL := os.Getenv("WEBAPP_PUBLIC_URL")
	if webappURL == "" {
		webappURL = "https://app.example.com"
	}
	webhookURL := os.Getenv("API_PUBLIC_URL")
	if webhookURL == "" {
		webhookURL = "https://api.example.com"
	}

	payload := map[string]interface{}{
		"amount": map[string]string{
			"currency": strings.ToUpper(currency),
			"value":    fmt.Sprintf("%.2f", amount),
		},
		"description": fmt.Sprintf("Invoice %s", invoiceID),
		"redirectUrl": fmt.Sprintf("%s/billing/payment-complete", webappURL),
		"webhookUrl":  fmt.Sprintf("%s/webhooks/mollie", webhookURL),
		"metadata": map[string]string{
			"invoice_id": invoiceID,
			"tenant_id":  tenantID,
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
		return "", "", fmt.Errorf("mollie API request failed: %v", err)
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
		return "", "", fmt.Errorf("failed to decode mollie response: %v", err)
	}

	checkoutURL := ""
	if checkout, ok := result.Links["checkout"]; ok {
		checkoutURL = checkout["href"]
	}

	return checkoutURL, result.ID, nil
}

// createCryptoPayment creates a crypto wallet for payment
func (s *PurserServer) createCryptoPayment(invoiceID, tenantID, asset string, amount float64, currency string, expiresAt time.Time) (string, error) {
	// Generate a wallet address for the payment
	var walletAddress string
	var err error

	switch asset {
	case "BTC":
		walletAddress, err = generateBitcoinAddress()
	case "ETH", "USDC", "LPT":
		walletAddress, err = generateEthereumAddress()
	default:
		return "", fmt.Errorf("unsupported crypto asset: %s", asset)
	}

	if err != nil {
		return "", fmt.Errorf("failed to generate wallet address: %v", err)
	}

	// Store crypto wallet record for monitoring
	_, err = s.db.ExecContext(context.Background(), `
		INSERT INTO purser.crypto_wallets (id, tenant_id, invoice_id, asset, wallet_address, status, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, 'active', $6, NOW())
	`, fmt.Sprintf("cw_%d", time.Now().UnixNano()), tenantID, invoiceID, asset, walletAddress, expiresAt)

	if err != nil {
		s.logger.WithError(err).Warn("Failed to store crypto wallet record")
		// Continue anyway - payment can still work
	}

	return walletAddress, nil
}

// generateBitcoinAddress generates a random Bitcoin address (simplified)
func generateBitcoinAddress() (string, error) {
	bytes := make([]byte, 20)
	_, err := rand.Read(bytes)
	if err != nil {
		return "", err
	}
	// P2PKH format (starts with 1)
	return "1" + hex.EncodeToString(bytes)[:33], nil
}

// generateEthereumAddress generates a random Ethereum address
func generateEthereumAddress() (string, error) {
	bytes := make([]byte, 20)
	_, err := rand.Read(bytes)
	if err != nil {
		return "", err
	}
	return "0x" + hex.EncodeToString(bytes), nil
}

// GetPaymentMethods returns available payment methods for a tenant
func (s *PurserServer) GetPaymentMethods(ctx context.Context, req *pb.GetPaymentMethodsRequest) (*pb.PaymentMethodResponse, error) {
	// Return available payment methods based on configured env vars
	// TODO: Could also check tenant location, tier restrictions, etc.
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
	var subStartedAt, subCreatedAt, subUpdatedAt time.Time
	var tierCreatedAt, tierUpdatedAt time.Time

	// JSONB fields
	var customPricing, customFeatures, customAllocations, billingAddress []byte
	var bandwidthAlloc, storageAlloc, computeAlloc, features, overageRates []byte

	err := s.db.QueryRowContext(ctx, `
		SELECT
			ts.id, ts.tenant_id, ts.tier_id, ts.status, ts.billing_email,
			ts.started_at, ts.trial_ends_at, ts.next_billing_date, ts.cancelled_at,
			ts.custom_pricing, ts.custom_features, ts.custom_allocations,
			ts.payment_method, ts.payment_reference, ts.billing_address,
			ts.tax_id, ts.tax_rate, ts.created_at, ts.updated_at,
			bt.id, bt.tier_name, bt.display_name, bt.description,
			bt.base_price, bt.currency, bt.billing_period,
			bt.bandwidth_allocation, bt.storage_allocation, bt.compute_allocation,
			bt.features, bt.support_level, bt.sla_level,
			bt.metering_enabled, bt.overage_rates, bt.is_active,
			bt.sort_order, bt.is_enterprise, bt.created_at, bt.updated_at
		FROM purser.tenant_subscriptions ts
		JOIN purser.billing_tiers bt ON ts.tier_id = bt.id
		WHERE ts.tenant_id = $1 AND ts.status != 'cancelled'
		ORDER BY ts.created_at DESC
		LIMIT 1
	`, tenantID).Scan(
		&subscription.Id, &subscription.TenantId, &subscription.TierId, &subscription.Status, &subscription.BillingEmail,
		&subStartedAt, &trialEndsAt, &nextBillingDate, &cancelledAt,
		&customPricing, &customFeatures, &customAllocations,
		&paymentMethod, &paymentReference, &billingAddress,
		&taxID, &taxRate, &subCreatedAt, &subUpdatedAt,
		&tier.Id, &tier.TierName, &tier.DisplayName, &tier.Description,
		&tier.BasePrice, &tier.Currency, &tier.BillingPeriod,
		&bandwidthAlloc, &storageAlloc, &computeAlloc,
		&features, &tier.SupportLevel, &tier.SlaLevel,
		&tier.MeteringEnabled, &overageRates, &tier.IsActive,
		&tier.SortOrder, &tier.IsEnterprise, &tierCreatedAt, &tierUpdatedAt)

	if err == sql.ErrNoRows {
		s.logger.WithField("tenant_id", tenantID).Warn("getSubscriptionAndTier: NO SUBSCRIPTION FOUND - returning free tier fallback")
		// Return default free tier
		return &pb.TenantSubscription{
				TenantId: tenantID,
				Status:   "none",
			}, &pb.BillingTier{
				TierName:    "free",
				DisplayName: "Free Tier",
				Currency:    "USD",
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
	if trialEndsAt.Valid {
		subscription.TrialEndsAt = timestamppb.New(trialEndsAt.Time)
	}
	if nextBillingDate.Valid {
		subscription.NextBillingDate = timestamppb.New(nextBillingDate.Time)
	}
	if cancelledAt.Valid {
		subscription.CancelledAt = timestamppb.New(cancelledAt.Time)
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
		       due_date, paid_at, usage_details, created_at, updated_at
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

		err := rows.Scan(&inv.Id, &inv.TenantId, &inv.Amount, &baseAmount, &meteredAmount,
			&inv.Currency, &inv.Status, &dueDate, &paidAt, &usageDetails, &createdAt, &updatedAt)
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

// getCurrentMonthUsageSummary gets aggregated usage for current billing month
func (s *PurserServer) getCurrentMonthUsageSummary(ctx context.Context, tenantID string) (*pb.UsageSummary, error) {
	// Use precise timestamp boundaries instead of billing_month string
	now := time.Now()
	periodStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	periodEnd := periodStart.AddDate(0, 1, 0) // First of next month (exclusive)

	var streamHours, viewerHours, egressGb, recordingGb, storageGb, peakBandwidthMbps float64
	var averageStorageGb, clipStorageAddedGb, clipStorageDeletedGb float64
	var dvrStorageAddedGb, dvrStorageDeletedGb, vodStorageAddedGb, vodStorageDeletedGb float64
	var totalStreams, totalViewers, peakViewers, clipsAdded, clipsDeleted int32
	var dvrAdded, dvrDeleted, vodAdded, vodDeleted int32
	var usageDetailsJSON []byte

	// Query aggregated metrics + most recent usage_details JSONB (for geo breakdown etc.)
	err := s.db.QueryRowContext(ctx, `
		WITH aggregated AS (
			SELECT
				COALESCE(SUM(CASE WHEN usage_type = 'stream_hours' THEN usage_value ELSE 0 END), 0) as stream_hours,
				COALESCE(SUM(CASE WHEN usage_type = 'viewer_hours' THEN usage_value ELSE 0 END), 0) as viewer_hours,
				COALESCE(SUM(CASE WHEN usage_type = 'egress_gb' THEN usage_value ELSE 0 END), 0) as egress_gb,
				COALESCE(SUM(CASE WHEN usage_type = 'recording_gb' THEN usage_value ELSE 0 END), 0) as recording_gb,
				COALESCE(SUM(CASE WHEN usage_type = 'storage_gb' THEN usage_value ELSE 0 END), 0) as storage_gb,
				COALESCE(MAX(CASE WHEN usage_type = 'peak_bandwidth_mbps' THEN usage_value ELSE 0 END), 0) as peak_bandwidth_mbps,
				COALESCE(SUM(CASE WHEN usage_type = 'total_streams' THEN usage_value ELSE 0 END), 0)::int as total_streams,
				COALESCE(SUM(CASE WHEN usage_type = 'total_viewers' THEN usage_value ELSE 0 END), 0)::int as total_viewers,
				COALESCE(MAX(CASE WHEN usage_type = 'peak_viewers' THEN usage_value ELSE 0 END), 0)::int as peak_viewers,
				-- Storage lifecycle metrics
				COALESCE(AVG(CASE WHEN usage_type = 'average_storage_gb' THEN usage_value END), 0) as average_storage_gb,
				COALESCE(SUM(CASE WHEN usage_type = 'clips_added' THEN usage_value ELSE 0 END), 0)::int as clips_added,
				COALESCE(SUM(CASE WHEN usage_type = 'clips_deleted' THEN usage_value ELSE 0 END), 0)::int as clips_deleted,
				COALESCE(SUM(CASE WHEN usage_type = 'clip_storage_added_gb' THEN usage_value ELSE 0 END), 0) as clip_storage_added_gb,
				COALESCE(SUM(CASE WHEN usage_type = 'clip_storage_deleted_gb' THEN usage_value ELSE 0 END), 0) as clip_storage_deleted_gb,
				-- DVR lifecycle metrics
				COALESCE(SUM(CASE WHEN usage_type = 'dvr_added' THEN usage_value ELSE 0 END), 0)::int as dvr_added,
				COALESCE(SUM(CASE WHEN usage_type = 'dvr_deleted' THEN usage_value ELSE 0 END), 0)::int as dvr_deleted,
				COALESCE(SUM(CASE WHEN usage_type = 'dvr_storage_added_gb' THEN usage_value ELSE 0 END), 0) as dvr_storage_added_gb,
				COALESCE(SUM(CASE WHEN usage_type = 'dvr_storage_deleted_gb' THEN usage_value ELSE 0 END), 0) as dvr_storage_deleted_gb,
				-- VOD lifecycle metrics
				COALESCE(SUM(CASE WHEN usage_type = 'vod_added' THEN usage_value ELSE 0 END), 0)::int as vod_added,
				COALESCE(SUM(CASE WHEN usage_type = 'vod_deleted' THEN usage_value ELSE 0 END), 0)::int as vod_deleted,
				COALESCE(SUM(CASE WHEN usage_type = 'vod_storage_added_gb' THEN usage_value ELSE 0 END), 0) as vod_storage_added_gb,
				COALESCE(SUM(CASE WHEN usage_type = 'vod_storage_deleted_gb' THEN usage_value ELSE 0 END), 0) as vod_storage_deleted_gb
			FROM purser.usage_records
			WHERE tenant_id = $1 AND period_start >= $2 AND period_end < $3
		),
		latest_details AS (
			SELECT usage_details
			FROM purser.usage_records
			WHERE tenant_id = $1 AND period_start >= $2 AND period_end < $3
			  AND usage_details IS NOT NULL
			  AND usage_details ? 'geo_breakdown'
			ORDER BY created_at DESC
			LIMIT 1
		)
		SELECT a.*, COALESCE(d.usage_details, '{}'::jsonb)
		FROM aggregated a
		LEFT JOIN latest_details d ON true
	`, tenantID, periodStart, periodEnd).Scan(
		&streamHours, &viewerHours, &egressGb, &recordingGb,
		&storageGb, &peakBandwidthMbps, &totalStreams, &totalViewers, &peakViewers,
		&averageStorageGb, &clipsAdded, &clipsDeleted, &clipStorageAddedGb, &clipStorageDeletedGb,
		&dvrAdded, &dvrDeleted, &dvrStorageAddedGb, &dvrStorageDeletedGb,
		&vodAdded, &vodDeleted, &vodStorageAddedGb, &vodStorageDeletedGb,
		&usageDetailsJSON,
	)

	if err != nil {
		return nil, err
	}

	// Parse usage_details for rich metrics (geo_breakdown, unique_countries, etc.)
	var details struct {
		MaxViewers      int                      `json:"max_viewers"`
		UniqueUsers     int                      `json:"unique_users"`
		AvgViewers      float64                  `json:"avg_viewers"`
		UniqueCountries int                      `json:"unique_countries"`
		UniqueCities    int                      `json:"unique_cities"`
		GeoBreakdown    []map[string]interface{} `json:"geo_breakdown"`
		// Storage lifecycle metrics (fallback from usage_details if not in usage_records)
		AverageStorageGb     float64 `json:"average_storage_gb"`
		ClipsAdded           int     `json:"clips_added"`
		ClipsDeleted         int     `json:"clips_deleted"`
		ClipStorageAddedGb   float64 `json:"clip_storage_added_gb"`
		ClipStorageDeletedGb float64 `json:"clip_storage_deleted_gb"`
		DvrAdded             int     `json:"dvr_added"`
		DvrDeleted           int     `json:"dvr_deleted"`
		DvrStorageAddedGb    float64 `json:"dvr_storage_added_gb"`
		DvrStorageDeletedGb  float64 `json:"dvr_storage_deleted_gb"`
		VodAdded             int     `json:"vod_added"`
		VodDeleted           int     `json:"vod_deleted"`
		VodStorageAddedGb    float64 `json:"vod_storage_added_gb"`
		VodStorageDeletedGb  float64 `json:"vod_storage_deleted_gb"`
	}
	if len(usageDetailsJSON) > 0 {
		_ = json.Unmarshal(usageDetailsJSON, &details)
	}

	// Convert geo breakdown to proto format
	var geoBreakdown []*pb.CountryMetrics
	for _, g := range details.GeoBreakdown {
		cm := &pb.CountryMetrics{}
		if code, ok := g["country_code"].(string); ok {
			cm.CountryCode = code
		}
		if count, ok := g["viewer_count"].(float64); ok {
			cm.ViewerCount = int32(count)
		}
		if hours, ok := g["viewer_hours"].(float64); ok {
			cm.ViewerHours = hours
		}
		if pct, ok := g["percentage"].(float64); ok {
			cm.Percentage = pct
		}
		if egress, ok := g["egress_gb"].(float64); ok {
			cm.EgressGb = egress
		}
		geoBreakdown = append(geoBreakdown, cm)
	}

	// Use SQL values, fall back to usage_details if SQL returns zero
	finalAvgStorageGb := averageStorageGb
	if finalAvgStorageGb == 0 {
		finalAvgStorageGb = details.AverageStorageGb
	}
	finalClipsAdded := clipsAdded
	if finalClipsAdded == 0 {
		finalClipsAdded = int32(details.ClipsAdded)
	}
	finalClipsDeleted := clipsDeleted
	if finalClipsDeleted == 0 {
		finalClipsDeleted = int32(details.ClipsDeleted)
	}
	finalClipStorageAdded := clipStorageAddedGb
	if finalClipStorageAdded == 0 {
		finalClipStorageAdded = details.ClipStorageAddedGb
	}
	finalClipStorageDeleted := clipStorageDeletedGb
	if finalClipStorageDeleted == 0 {
		finalClipStorageDeleted = details.ClipStorageDeletedGb
	}

	return &pb.UsageSummary{
		TenantId:          tenantID,
		BillingMonth:      periodStart.Format("2006-01"), // For display/backwards compat
		Period:            "month",
		StreamHours:       streamHours,
		ViewerHours:       viewerHours,
		EgressGb:          egressGb,
		RecordingGb:       recordingGb,
		StorageGb:         storageGb,
		PeakBandwidthMbps: peakBandwidthMbps,
		TotalStreams:      totalStreams,
		TotalViewers:      totalViewers,
		PeakViewers:       peakViewers,
		MaxViewers:        int32(details.MaxViewers),
		UniqueUsers:       int32(details.UniqueUsers),
		AvgViewers:        details.AvgViewers,
		UniqueCountries:   int32(details.UniqueCountries),
		UniqueCities:      int32(details.UniqueCities),
		GeoBreakdown:      geoBreakdown,
		// Storage lifecycle metrics
		AverageStorageGb:     finalAvgStorageGb,
		ClipsAdded:           finalClipsAdded,
		ClipsDeleted:         finalClipsDeleted,
		ClipStorageAddedGb:   finalClipStorageAdded,
		ClipStorageDeletedGb: finalClipStorageDeleted,
		// DVR lifecycle metrics
		DvrAdded:            dvrAdded,
		DvrDeleted:          dvrDeleted,
		DvrStorageAddedGb:   dvrStorageAddedGb,
		DvrStorageDeletedGb: dvrStorageDeletedGb,
		// VOD lifecycle metrics
		VodAdded:            vodAdded,
		VodDeleted:          vodDeleted,
		VodStorageAddedGb:   vodStorageAddedGb,
		VodStorageDeletedGb: vodStorageDeletedGb,
	}, nil
}

// scanCustomPricing scans JSONB into CustomPricing proto
func scanCustomPricing(data []byte) *pb.CustomPricing {
	if data == nil || len(data) == 0 {
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
	if data == nil || len(data) == 0 {
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
		  AND period_start >= $2::date
		  AND period_end <= ($3::date + INTERVAL '1 day')
		GROUP BY usage_type
	`, tenantID, startDate, endDate)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	usage := make(map[string]float64)
	costs := make(map[string]float64)
	var totalCost float64

	// Unit prices (simplified - in production would come from tier/subscription)
	unitPrices := map[string]float64{
		"stream_hours":        0.01,
		"egress_gb":           0.05,
		"recording_gb":        0.02,
		"peak_bandwidth_mbps": 0.001,
	}

	for rows.Next() {
		var usageType string
		var total float64
		if err := rows.Scan(&usageType, &total); err != nil {
			continue
		}
		usage[usageType] = total
		if price, ok := unitPrices[usageType]; ok {
			cost := total * price
			costs[usageType] = cost
			totalCost += cost
		}
	}

	return &pb.TenantUsageResponse{
		TenantId:      tenantID,
		BillingPeriod: startDate + " to " + endDate,
		Usage:         usage,
		Costs:         costs,
		TotalCost:     totalCost,
		Currency:      "USD",
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

		result[pricing.ClusterId] = &pricing
	}

	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "rows error: %v", err)
	}

	// Fill in default pricing for clusters not found in DB
	for _, clusterID := range clusterIDs {
		if _, found := result[clusterID]; !found {
			result[clusterID] = &pb.ClusterPricing{
				ClusterId:          clusterID,
				PricingModel:       "tier_inherit",
				Currency:           "EUR",
				RequiredTierLevel:  0,
				IsPlatformOfficial: false,
				AllowFreeTier:      false,
			}
		}
	}

	return &pb.GetClustersPricingBatchResponse{
		Pricings: result,
	}, nil
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
		// Join with quartermaster to filter by owner
		// Note: This requires cross-schema access which may need adjustment
		query = `
			SELECT p.id, p.cluster_id, p.pricing_model,
			       p.stripe_product_id, p.stripe_price_id_monthly, p.stripe_meter_id,
			       p.base_price, p.currency, p.metered_rates,
			       p.required_tier_level, p.is_platform_official, p.allow_free_tier,
			       p.default_quotas, p.created_at, p.updated_at
			FROM purser.cluster_pricing p
			JOIN quartermaster.infrastructure_clusters c ON p.cluster_id = c.cluster_id
			WHERE c.owner_tenant_id = $1
		`
		args = append(args, ownerTenantID)
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

	// Get tenant's billing tier level
	var tenantTierLevel int32
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(bt.tier_level, 0)
		FROM quartermaster.tenants t
		LEFT JOIN purser.tenant_subscriptions ts ON t.id = ts.tenant_id AND ts.status = 'active'
		LEFT JOIN purser.billing_tiers bt ON ts.tier_id = bt.id
		WHERE t.id = $1
	`, tenantID).Scan(&tenantTierLevel)
	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "tenant not found")
	}
	if err != nil {
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
func (s *PurserServer) CreateClusterSubscription(ctx context.Context, req *pb.CreateClusterSubscriptionRequest) (*pb.ClusterSubscriptionResponse, error) {
	tenantID := req.GetTenantId()
	clusterID := req.GetClusterId()
	inviteToken := req.GetInviteToken()

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
		// TODO: Create Stripe subscription
		// For now, mark as pending
		resp.Status = "pending_payment"
		// resp.CheckoutUrl = would be Stripe checkout session URL

	case "metered":
		// Metered clusters can be activated immediately, billing happens on usage
		resp.Status = "active"

	case "custom":
		// Custom requires approval
		resp.Status = "pending_approval"
	}

	// Handle invite token if provided
	if inviteToken != "" {
		// Validate and consume invite token
		// This would update quartermaster.cluster_invites
		s.logger.Info("invite token provided", "token", inviteToken[:8]+"...", "cluster", clusterID)
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
		// TODO: Cancel Stripe subscription
		s.logger.Info("cancelling paid cluster subscription", "tenant", tenantID, "cluster", clusterID)
	}

	// The actual tenant_cluster_access removal is handled by Quartermaster
	// This service just handles the billing side

	return &emptypb.Empty{}, nil
}

// jsonToMap is a helper to convert JSON bytes to map for structpb
func jsonToMap(data []byte) map[string]interface{} {
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return make(map[string]interface{})
	}
	return m
}

// ============================================================================
// SERVER SETUP
// ============================================================================

// GRPCServerConfig contains configuration for creating a Purser gRPC server
type GRPCServerConfig struct {
	DB           *sql.DB
	Logger       logging.Logger
	ServiceToken string
	JWTSecret    []byte
	Metrics      *ServerMetrics
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
	purserServer := NewPurserServer(cfg.DB, cfg.Logger, cfg.Metrics)

	// Register all services
	pb.RegisterBillingServiceServer(server, purserServer)
	pb.RegisterUsageServiceServer(server, purserServer)
	pb.RegisterSubscriptionServiceServer(server, purserServer)
	pb.RegisterInvoiceServiceServer(server, purserServer)
	pb.RegisterPaymentServiceServer(server, purserServer)
	pb.RegisterClusterPricingServiceServer(server, purserServer)

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
		return resp, err
	}
}

// Ensure unused imports don't cause errors
var _ = pq.Array
