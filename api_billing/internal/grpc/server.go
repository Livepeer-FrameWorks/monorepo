package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/handlers"
	"frameworks/api_billing/internal/mollie"
	"frameworks/api_billing/internal/pricing"
	"frameworks/api_billing/internal/stripe"
	"frameworks/api_billing/internal/tieraccess"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	decklogclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/countries"
	fwdb "github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	meteringpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/metering_contract"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	tenantlimitspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/tenant_limits"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	sharedx402 "github.com/Livepeer-FrameWorks/monorepo/pkg/x402"
	mollielib "github.com/VictorAvelar/mollie-api-go/v4/mollie"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/shopspring/decimal"
	stripelib "github.com/stripe/stripe-go/v85"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	billingpkg "frameworks/api_billing/internal/billing"
	"frameworks/api_billing/internal/rating"
)

// loadTierPricingRules reads pricing rules for a tier and converts them into
// the protobuf shape exposed on BillingTier.
func loadTierPricingRules(ctx context.Context, db *sql.DB, tierID string) ([]*purserpb.PricingRule, error) {
	rows, err := purserdb.New(db).ListTierPricingRules(ctx, tierID)
	if err != nil {
		return nil, err
	}
	out := make([]*purserpb.PricingRule, 0, len(rows))
	for _, row := range rows {
		out = append(out, &purserpb.PricingRule{
			Meter: row.Meter, Model: row.Model, Currency: row.Currency,
			IncludedQuantity: row.IncludedQuantity, UnitPrice: row.UnitPrice, ConfigJson: row.Config,
		})
	}
	return out, nil
}

// loadTierEntitlements reads entitlements for a tier into a map of
// JSON-encoded values keyed by entitlement key.
func loadTierEntitlements(ctx context.Context, db *sql.DB, tierID string) (map[string]string, error) {
	rows, err := purserdb.New(db).ListTierEntitlements(ctx, tierID)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, row := range rows {
		out[row.Key] = row.Value
	}
	return out, nil
}

// loadSubscriptionPricingOverrides reads per-tenant pricing overrides into
// proto messages.
func loadSubscriptionPricingOverrides(ctx context.Context, db *sql.DB, subscriptionID string) ([]*purserpb.PricingRule, error) {
	rows, err := purserdb.New(db).ListSubscriptionPricingOverrides(ctx, subscriptionID)
	if err != nil {
		return nil, err
	}
	out := make([]*purserpb.PricingRule, 0, len(rows))
	for _, row := range rows {
		out = append(out, &purserpb.PricingRule{
			Meter: row.Meter, Model: row.Model.String, Currency: row.Currency.String,
			IncludedQuantity: row.IncludedQuantity.String, UnitPrice: row.UnitPrice.String,
			ConfigJson: string(row.Config),
		})
	}
	return out, nil
}

// loadSubscriptionEntitlementOverrides reads per-tenant entitlement overrides.
func loadSubscriptionEntitlementOverrides(ctx context.Context, db *sql.DB, subscriptionID string) (map[string]string, error) {
	rows, err := purserdb.New(db).ListSubscriptionEntitlementOverrides(ctx, subscriptionID)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, row := range rows {
		out[row.Key] = row.Value
	}
	return out, nil
}

func billingTierFromListRow(row purserdb.ListBillingTiersRow) *purserpb.BillingTier {
	return &purserpb.BillingTier{
		Id: row.ID.String(), TierName: row.TierName, DisplayName: row.DisplayName,
		Description: row.Description, BasePrice: row.BasePrice, Currency: row.Currency,
		BillingPeriod: row.BillingPeriod, Features: scanBillingFeatures(row.Features),
		SupportLevel: row.SupportLevel, SlaLevel: row.SlaLevel, MeteringEnabled: row.MeteringEnabled,
		IsActive: row.IsActive, TierLevel: row.TierLevel, IsEnterprise: row.IsEnterprise,
		CreatedAt: timestamppb.New(row.CreatedAt.Time), UpdatedAt: timestamppb.New(row.UpdatedAt.Time),
		IsDefaultPrepaid: row.IsDefaultPrepaid, IsDefaultPostpaid: row.IsDefaultPostpaid,
		ProcessesLive: string(row.ProcessesLive), ProcessesDvr: string(row.ProcessesDvr),
		ProcessesClip: string(row.ProcessesClip), ProcessesDvrFinalize: string(row.ProcessesDvrFinalize),
		ProcessesVod: string(row.ProcessesVod),
	}
}

func billingTierFromGetRow(row purserdb.GetBillingTierByIDRow) *purserpb.BillingTier {
	return &purserpb.BillingTier{
		Id: row.ID.String(), TierName: row.TierName, DisplayName: row.DisplayName,
		Description: row.Description, BasePrice: row.BasePrice, Currency: row.Currency,
		BillingPeriod: row.BillingPeriod, Features: scanBillingFeatures(row.Features),
		SupportLevel: row.SupportLevel, SlaLevel: row.SlaLevel, MeteringEnabled: row.MeteringEnabled,
		IsActive: row.IsActive, TierLevel: row.TierLevel, IsEnterprise: row.IsEnterprise,
		CreatedAt: timestamppb.New(row.CreatedAt.Time), UpdatedAt: timestamppb.New(row.UpdatedAt.Time),
		IsDefaultPrepaid: row.IsDefaultPrepaid, IsDefaultPostpaid: row.IsDefaultPostpaid,
		ProcessesLive: string(row.ProcessesLive), ProcessesDvr: string(row.ProcessesDvr),
		ProcessesClip: string(row.ProcessesClip), ProcessesDvrFinalize: string(row.ProcessesDvrFinalize),
		ProcessesVod: string(row.ProcessesVod),
	}
}

// scanBillingFeatures scans a JSONB column into BillingFeatures proto
func scanBillingFeatures(data []byte) *purserpb.BillingFeatures {
	if len(data) == 0 {
		return nil
	}
	var raw struct {
		Recording              bool   `json:"recording"`
		Analytics              bool   `json:"analytics"`
		CustomBranding         bool   `json:"custom_branding"`
		APIAccess              bool   `json:"api_access"`
		SupportLevel           string `json:"support_level"`
		SLA                    bool   `json:"sla"`
		ProcessingCustomizable bool   `json:"processing_customizable"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	return &purserpb.BillingFeatures{
		Recording:              raw.Recording,
		Analytics:              raw.Analytics,
		CustomBranding:         raw.CustomBranding,
		ApiAccess:              raw.APIAccess,
		SupportLevel:           raw.SupportLevel,
		Sla:                    raw.SLA,
		ProcessingCustomizable: raw.ProcessingCustomizable,
	}
}

// marshalBillingFeatures converts BillingFeatures proto to JSONB bytes
func marshalBillingFeatures(bf *purserpb.BillingFeatures) ([]byte, error) {
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

// mapToProtoStruct converts a map[string]any to protobuf Struct
func mapToProtoStruct(m map[string]any) *structpb.Struct {
	if m == nil {
		return nil
	}
	s, err := structpb.NewStruct(m)
	if err != nil {
		return nil
	}
	return s
}

// ServerMetrics holds Prometheus metrics for the gRPC server. Per-method
// counts + duration come from GRPCMetricsInterceptor; per-domain operation
// counters were removed because their {operation} label maps 1:1 to the
// gRPC method already exposed on {method}.
type ServerMetrics struct {
	GRPCRequests *prometheus.CounterVec
	GRPCDuration *prometheus.HistogramVec
}

type stripeBillingClient interface {
	CreateOrGetCustomer(ctx context.Context, info stripe.CustomerInfo) (*stripelib.Customer, error)
	CreateCheckoutSession(ctx context.Context, params stripe.CheckoutSessionParams) (*stripelib.CheckoutSession, error)
	ExpireCheckoutSession(ctx context.Context, sessionID string) error
	CreateBillingPortalSession(ctx context.Context, customerID, returnURL string) (*stripelib.BillingPortalSession, error)
	GetSubscription(ctx context.Context, subscriptionID string) (*stripelib.Subscription, error)
	CancelSubscription(ctx context.Context, subscriptionID string) (*stripelib.Subscription, error)
	ExtractSubscriptionInfo(sub *stripelib.Subscription) stripe.SubscriptionInfo
}

type mollieBillingClient interface {
	CreateOrGetCustomer(ctx context.Context, info mollie.CustomerInfo) (*mollielib.Customer, error)
	CreateFirstPayment(ctx context.Context, params mollie.FirstPaymentParams) (*mollielib.Payment, error)
	ListMandates(ctx context.Context, customerID string) ([]*mollielib.Mandate, error)
	GetMandate(ctx context.Context, customerID, mandateID string) (*mollielib.Mandate, error)
	CreateSubscription(ctx context.Context, params mollie.SubscriptionParams) (*mollielib.Subscription, error)
	CancelSubscription(ctx context.Context, customerID, subscriptionID string) error
	GetSubscription(ctx context.Context, customerID, subscriptionID string) (*mollielib.Subscription, error)
	ExtractSubscriptionInfo(sub *mollielib.Subscription, customerID string) mollie.SubscriptionInfo
	ExtractMandateInfo(mandate *mollielib.Mandate, customerID string) mollie.MandateInfo
}

// PurserServer implements the Purser gRPC services
type PurserServer struct {
	purserpb.UnimplementedBillingServiceServer
	purserpb.UnimplementedUsageServiceServer
	purserpb.UnimplementedSubscriptionServiceServer
	purserpb.UnimplementedInvoiceServiceServer
	purserpb.UnimplementedPaymentServiceServer
	purserpb.UnimplementedClusterPricingServiceServer
	purserpb.UnimplementedOperatorRevenueServiceServer
	purserpb.UnimplementedPrepaidServiceServer
	purserpb.UnimplementedWebhookServiceServer
	purserpb.UnimplementedStripeServiceServer
	purserpb.UnimplementedMollieServiceServer
	purserpb.UnimplementedX402ServiceServer
	purserpb.UnimplementedCryptoSweepServiceServer
	db                  *sql.DB
	logger              logging.Logger
	metrics             *ServerMetrics
	stripeClient        stripeBillingClient
	mollieClient        mollieBillingClient
	quartermasterClient commercialQuartermasterClient
	commodoreClient     handlers.CommodoreClient
	hdwallet            *handlers.HDWallet
	rpcClient           *handlers.RPCClient
	priceFeed           *handlers.PriceFeed
	x402handler         *handlers.X402Handler
	decklogClient       serviceEventSender
	thresholdEnforcer   *handlers.ThresholdEnforcer
	tierReconciler      tierAccessReconciler
	billing             *handlers.Service
}

type commercialQuartermasterClient interface {
	GetCluster(ctx context.Context, clusterID string) (*quartermasterpb.ClusterResponse, error)
	ListClustersByOwner(ctx context.Context, ownerTenantID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error)
	BootstrapClusterAccess(ctx context.Context, tenantID, clusterID string, resourceLimits *tenantlimitspb.TenantResourceLimits) error
	MaterializeClusterAccess(ctx context.Context, req *quartermasterpb.MaterializeClusterAccessRequest) error
	RevokeMaterializedClusterAccess(ctx context.Context, req *quartermasterpb.RevokeMaterializedClusterAccessRequest) error
}

type tierAccessReconciler interface {
	OfficialClusterIDs(ctx context.Context) (map[string]bool, error)
	Reconcile(ctx context.Context, tenantID string, tierLevel int32, tierName string) ([]string, string, error)
}

type serviceEventSender interface {
	SendServiceEvent(event *ipcpb.ServiceEvent) error
}

// NewPurserServer creates a new Purser gRPC server
func NewPurserServer(db *sql.DB, logger logging.Logger, metrics *ServerMetrics, stripeClient *stripe.Client, mollieClient *mollie.Client, qmClient *qmclient.GRPCClient, commodoreClient handlers.CommodoreClient, decklogClient *decklogclient.BatchedClient, billing *handlers.Service) *PurserServer {
	hdwallet := handlers.NewHDWallet(db, logger)
	if created, err := hdwallet.EnsureState(context.Background(), os.Getenv("HD_WALLET_XPUB")); err != nil {
		logger.WithError(err).Warn("HD wallet state not initialized; crypto deposits disabled until configured")
	} else if created {
		logger.Info("Initialized HD wallet state from HD_WALLET_XPUB")
	}
	rpcClient := handlers.NewRPCClient()
	priceFeed := handlers.NewPriceFeed(rpcClient, logger)
	return &PurserServer{
		db:                  db,
		logger:              logger,
		metrics:             metrics,
		stripeClient:        stripeClient,
		mollieClient:        mollieClient,
		quartermasterClient: qmClient,
		commodoreClient:     commodoreClient,
		hdwallet:            hdwallet,
		rpcClient:           rpcClient,
		priceFeed:           priceFeed,
		x402handler:         handlers.NewX402Handler(db, logger, hdwallet, rpcClient, commodoreClient),
		decklogClient:       decklogClient,
		thresholdEnforcer:   handlers.NewThresholdEnforcer(db, logger, commodoreClient, nil, billing),
		tierReconciler:      tieraccess.NewReconciler(db, qmClient, logger),
		billing:             billing,
	}
}

// markProviderIntentFailed advances a payment_provider_intents row to
// 'provider_call_failed' and records the error. The intent row stays in
// the table for ops audit; subsequent retries with the same idempotency
// key bump attempt_count via ON CONFLICT. Errors are logged internally
// and not propagated: the originating provider error is the user-facing
// failure; the intent-marker write is best-effort audit.
func (s *PurserServer) markProviderIntentFailed(ctx context.Context, intentID, code string, cause error) {
	if intentID == "" {
		return
	}
	errMsg := code
	if cause != nil {
		errMsg = code + ": " + cause.Error()
	}
	if err := purserdb.New(s.db).SetProviderPaymentIntentFailure(ctx, purserdb.SetProviderPaymentIntentFailureParams{
		Status:    "provider_call_failed",
		LastError: sql.NullString{String: errMsg, Valid: true},
		IntentID:  intentID,
	}); err != nil {
		s.logger.WithError(err).WithField("intent_id", intentID).Warn("Failed to mark payment_provider_intents failed")
	}
}

// GetBillingTiers returns available billing tiers with cursor pagination
func (s *PurserServer) GetBillingTiers(ctx context.Context, req *purserpb.GetBillingTiersRequest) (*purserpb.GetBillingTiersResponse, error) {
	// Parse bidirectional pagination
	params, parseErr := pagination.Parse(req.GetPagination())
	if parseErr != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid cursor: %v", parseErr)
	}

	cursorID := uuid.Nil.String()
	var cursorTierLevel int32
	if params.Cursor != nil {
		tierLevelKey := params.Cursor.GetSortKey()
		if tierLevelKey < -2147483648 || tierLevelKey > 2147483647 {
			return nil, status.Error(codes.InvalidArgument, "invalid cursor tier level")
		}
		parsedID, parseIDErr := uuid.Parse(params.Cursor.ID)
		if parseIDErr != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid cursor tier ID")
		}
		cursorTierLevel = int32(tierLevelKey)
		cursorID = parsedID.String()
	}

	rows, err := purserdb.New(s.db).ListBillingTiers(ctx, purserdb.ListBillingTiersParams{
		IncludeInactive: req.GetIncludeInactive(), HasCursor: params.Cursor != nil,
		Backward: params.Direction == pagination.Backward, CursorTierLevel: cursorTierLevel,
		CursorID: cursorID, ResultLimit: int32(params.Limit + 1),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	tiers := make([]*purserpb.BillingTier, 0, len(rows))
	for _, row := range rows {
		tier := billingTierFromListRow(row)
		rules, rulesErr := loadTierPricingRules(ctx, s.db, tier.Id)
		if rulesErr != nil {
			return nil, status.Errorf(codes.Internal, "load pricing rules for tier %q: %v", tier.TierName, rulesErr)
		}
		tier.PricingRules = rules
		ents, entsErr := loadTierEntitlements(ctx, s.db, tier.Id)
		if entsErr != nil {
			return nil, status.Errorf(codes.Internal, "load entitlements for tier %q: %v", tier.TierName, entsErr)
		}
		tier.Entitlements = ents
		tiers = append(tiers, tier)
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
	paymentMethods := s.getAvailablePaymentMethods(ctx)

	// Build cursors
	var startCursor, endCursor string
	if len(tiers) > 0 {
		first := tiers[0]
		last := tiers[len(tiers)-1]
		// Encode tier_level as sort key cursor
		startCursor = pagination.EncodeCursorWithSortKey(int64(first.TierLevel), first.Id)
		endCursor = pagination.EncodeCursorWithSortKey(int64(last.TierLevel), last.Id)
	}

	resp := &purserpb.GetBillingTiersResponse{
		Tiers:          tiers,
		PaymentMethods: paymentMethods,
		Pagination:     pagination.BuildResponse(resultsLen, params.Limit, params.Direction, int32(len(tiers)), startCursor, endCursor),
	}

	return resp, nil
}

// ListMeterDefinitions returns the active canonical meter catalog. Pricing
// consumers use this alongside tier rules so unpriced-but-itemized meters and
// their physical units remain discoverable.
func (s *PurserServer) ListMeterDefinitions(ctx context.Context, _ *emptypb.Empty) (*purserpb.ListMeterDefinitionsResponse, error) {
	rows, err := purserdb.New(s.db).ListActiveMeterDefinitions(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list meter definitions: %v", err)
	}

	resp := &purserpb.ListMeterDefinitionsResponse{Meters: make([]*purserpb.MeterDefinition, 0, len(rows))}
	for _, row := range rows {
		resp.Meters = append(resp.Meters, &purserpb.MeterDefinition{
			Meter: row.Meter, Unit: row.Unit, Aggregation: row.Aggregation,
			DisplayName: row.DisplayName, AllowedDimensions: row.AllowedDimensions,
			DefaultPriceable: row.DefaultPriceable,
		})
	}
	return resp, nil
}

// GetBillingTier returns a specific billing tier
func (s *PurserServer) GetBillingTier(ctx context.Context, req *purserpb.GetBillingTierRequest) (*purserpb.BillingTier, error) {
	tierID := req.GetTierId()
	if tierID == "" {
		return nil, status.Error(codes.InvalidArgument, "tier_id required")
	}

	var row purserdb.GetBillingTierByIDRow
	err := fwdb.RetryPostgres(ctx, fwdb.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		row, queryErr = purserdb.New(s.db).GetBillingTierByID(ctx, tierID)
		return queryErr
	})

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "Billing tier not found")
	}

	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	tier := billingTierFromGetRow(row)
	rules, rulesErr := loadTierPricingRules(ctx, s.db, tier.Id)
	if rulesErr != nil {
		return nil, status.Errorf(codes.Internal, "load pricing rules: %v", rulesErr)
	}
	tier.PricingRules = rules
	ents, entsErr := loadTierEntitlements(ctx, s.db, tier.Id)
	if entsErr != nil {
		return nil, status.Errorf(codes.Internal, "load entitlements: %v", entsErr)
	}
	tier.Entitlements = ents

	return tier, nil
}

// ============================================================================
// CROSS-SERVICE BILLING STATUS
// ============================================================================

type tenantAdmissionData struct {
	BillingModel         string
	SubscriptionStatus   string
	BalanceCents         sql.NullInt64
	ReservedBalanceCents int64
	PaymentMethod        sql.NullString
	StripeSubscriptionID sql.NullString
	MollieSubscriptionID sql.NullString
	TierName             string
	TierLevel            int32
}

func defaultTenantAdmissionStatus() *purserpb.GetTenantAdmissionStatusResponse {
	return &purserpb.GetTenantAdmissionStatusResponse{
		BillingModel:          "prepaid",
		IsBalanceNegative:     true,
		BalanceCents:          0,
		AvailableBalanceCents: 0,
	}
}

func mapTenantAdmissionStatus(row tenantAdmissionData) *purserpb.GetTenantAdmissionStatusResponse {
	model := row.BillingModel
	if model == "" {
		model = "postpaid"
	}
	balance := int64(0)
	if row.BalanceCents.Valid {
		balance = row.BalanceCents.Int64
	}
	availableBalance := balance - row.ReservedBalanceCents
	collectionProvider := ""
	collectionReady := false
	if row.PaymentMethod.String == "stripe" && row.StripeSubscriptionID.Valid && row.StripeSubscriptionID.String != "" {
		collectionProvider = "stripe"
		collectionReady = true
	} else if row.PaymentMethod.String == "mollie" && row.MollieSubscriptionID.Valid && row.MollieSubscriptionID.String != "" {
		collectionProvider = "mollie"
		collectionReady = true
	}
	return &purserpb.GetTenantAdmissionStatusResponse{
		BillingModel:          model,
		IsSuspended:           row.SubscriptionStatus == "suspended",
		IsBalanceNegative:     model == "prepaid" && availableBalance <= 0,
		BalanceCents:          balance,
		ReservedBalanceCents:  row.ReservedBalanceCents,
		AvailableBalanceCents: availableBalance,
		CollectionReady:       collectionReady,
		CollectionProvider:    collectionProvider,
		TierName:              row.TierName,
		TierLevel:             row.TierLevel,
	}
}

// GetTenantAdmissionStatus returns the bounded billing authority decision used
// by latency-sensitive admission paths. It deliberately excludes entitlements,
// allowance usage, retention, and storage pricing.
func (s *PurserServer) GetTenantAdmissionStatus(ctx context.Context, req *purserpb.GetTenantAdmissionStatusRequest) (*purserpb.GetTenantAdmissionStatusResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	queryCtx, cancel := context.WithTimeout(ctx, tenantAdmissionQueryTimeout)
	defer cancel()

	var row purserdb.GetTenantAdmissionStatusRow
	err := fwdb.RetryPostgres(queryCtx, fwdb.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		row, queryErr = purserdb.New(s.db).GetTenantAdmissionStatus(queryCtx, purserdb.GetTenantAdmissionStatusParams{
			TenantID: tenantID,
			Currency: billing.DefaultCurrency(),
		})
		return queryErr
	})
	if errors.Is(err, sql.ErrNoRows) {
		return defaultTenantAdmissionStatus(), nil
	}
	if err != nil {
		s.logger.WithFields(logging.Fields{"tenant_id": tenantID, "error": err}).Error("Database error getting tenant admission status")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	return mapTenantAdmissionStatus(tenantAdmissionData{
		BillingModel: row.BillingModel, SubscriptionStatus: row.SubscriptionStatus,
		BalanceCents: row.BalanceCents, ReservedBalanceCents: row.ReservedBalanceCents,
		PaymentMethod: row.PaymentMethod, StripeSubscriptionID: row.StripeSubscriptionID,
		MollieSubscriptionID: row.MollieSubscriptionID, TierName: row.TierName,
		TierLevel: row.TierLevel,
	}), nil
}

// Leave transport and orchestration headroom inside Commodore's 500 ms
// operation-owned admission budget. A database retry that cannot finish in
// this window fails closed; it must not consume the caller's entire budget.
const tenantAdmissionQueryTimeout = 350 * time.Millisecond

// GetTenantBillingStatus returns the full entitlement and pricing snapshot.
func (s *PurserServer) GetTenantBillingStatus(ctx context.Context, req *purserpb.GetTenantBillingStatusRequest) (*purserpb.GetTenantBillingStatusResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	currency := billing.DefaultCurrency()
	var billingStatus purserdb.GetTenantBillingStatusRow
	err := fwdb.RetryPostgres(ctx, fwdb.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		billingStatus, queryErr = purserdb.New(s.db).GetTenantBillingStatus(ctx, purserdb.GetTenantBillingStatusParams{
			TenantID: tenantID, Currency: currency, DvrKeys: []string{
				"dvr_default_window_seconds",
				"dvr_max_window_seconds",
				"dvr_default_segment_duration_seconds",
				"dvr_max_entries",
				"dvr_allow_cluster_extension",
			}, ResourceKeys: []string{
				"max_concurrent_streams",
				"max_concurrent_viewers",
			}})
		return queryErr
	})

	if errors.Is(err, sql.ErrNoRows) {
		// Missing billing provisioning must never become implicit postpaid credit.
		// Report a zero-balance prepaid state so rated admission fails closed while
		// the Gateway's non-rated onboarding and payment surfaces remain reachable.
		admission := defaultTenantAdmissionStatus()
		return &purserpb.GetTenantBillingStatusResponse{
			BillingModel: admission.BillingModel, IsBalanceNegative: admission.IsBalanceNegative,
			BalanceCents: admission.BalanceCents, AvailableBalanceCents: admission.AvailableBalanceCents,
		}, nil
	}

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Error("Database error getting billing status")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	admission := mapTenantAdmissionStatus(tenantAdmissionData{
		BillingModel: billingStatus.BillingModel, SubscriptionStatus: billingStatus.SubscriptionStatus,
		BalanceCents: billingStatus.BalanceCents, ReservedBalanceCents: billingStatus.ReservedBalanceCents,
		PaymentMethod: billingStatus.PaymentMethod, StripeSubscriptionID: billingStatus.StripeSubscriptionID,
		MollieSubscriptionID: billingStatus.MollieSubscriptionID, TierName: billingStatus.TierName,
	})

	retentionRaw := sql.NullString{String: billingStatus.RetentionValue, Valid: billingStatus.RetentionValue != ""}
	dvrEntitlements := sql.NullString{String: billingStatus.DvrEntitlements, Valid: billingStatus.DvrEntitlements != ""}
	storageLimitRaw := sql.NullString{String: billingStatus.StorageLimitValue, Valid: billingStatus.StorageLimitValue != ""}
	resourceLimitsRaw := sql.NullString{String: billingStatus.ResourceLimits, Valid: billingStatus.ResourceLimits != ""}
	retentionDays := parseRetentionDays(retentionRaw)
	dvrPolicy := parseDVRPolicy(dvrEntitlements)
	// Stamp the tier cap onto DVRPolicy.recording_retention_days so any
	// direct-Foghorn caller (bypassing Commodore's per-class cascade) still
	// gets a sensible horizon. Commodore.StartDVR overrides this with the
	// fully-resolved value via resolveInitialRetention. Tier cap of 0
	// (uncapped, paid baseline) leaves the field unset — Foghorn falls back
	// to its 30-day system default.
	if retentionDays > 0 {
		if dvrPolicy == nil {
			dvrPolicy = &sharedpb.DVRPolicy{}
		}
		v := retentionDays
		dvrPolicy.RecordingRetentionDays = &v
	}
	var allowances []*meteringpb.MeterAllowance
	var storagePricing *purserpb.StoragePricing
	if billingStatus.TierID != "" {
		periodStart, periodEnd := resolveCurrentPeriod(billingStatus.BillingPeriodStart, billingStatus.BillingPeriodEnd, time.Now().UTC())
		allowances = s.computeAllowances(ctx, tenantID, billingStatus.TierID, periodStart, periodEnd)
		storagePricing = s.loadStoragePricing(ctx, billingStatus.TierID)
	}

	return &purserpb.GetTenantBillingStatusResponse{
		BillingModel:           admission.BillingModel,
		IsSuspended:            admission.IsSuspended,
		IsBalanceNegative:      admission.IsBalanceNegative,
		BalanceCents:           admission.BalanceCents,
		ReservedBalanceCents:   admission.ReservedBalanceCents,
		AvailableBalanceCents:  admission.AvailableBalanceCents,
		RecordingRetentionDays: retentionDays,
		DvrPolicy:              dvrPolicy,
		Allowances:             allowances,
		StorageLimitBytes:      parseStorageLimitBytes(storageLimitRaw),
		TenantResourceLimits:   parseTenantResourceLimits(resourceLimitsRaw),
		StoragePricing:         storagePricing,
		CollectionReady:        admission.CollectionReady,
		CollectionProvider:     admission.CollectionProvider,
		TierName:               admission.TierName,
	}, nil
}

// loadStoragePricing returns the tenant's marginal storage pricing for the
// customer-facing cold (S3) storage product. Returns nil when the tier has
// no rule. Drives the per-asset cost projection on the storage browser.
// Both unit_price and included_quantity are in GiB-hours — the rating engine
// converts GiB-seconds → GiB-hours via toRatedUnits before subtracting the
// included allowance, so the catalog and the wire are in the same unit.
func (s *PurserServer) loadStoragePricing(ctx context.Context, tierID string) *purserpb.StoragePricing {
	const meter = "storage_gb_seconds_cold"
	row, err := purserdb.New(s.db).GetStoragePricing(ctx, purserdb.GetStoragePricingParams{TierID: tierID, Meter: meter})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"tier_id": tierID,
			"meter":   meter,
		}).Warn("Failed to load storage pricing rule")
		return nil
	}
	return &purserpb.StoragePricing{
		IncludedGbHours:    row.IncludedQuantity,
		UnitPricePerGbHour: row.UnitPrice,
		Currency:           row.Currency,
		Model:              row.Model,
	}
}

// parseStorageLimitBytes decodes the storage_limit_gb entitlement (a bare JSON
// integer, in GB) into bytes for the wire response. Returns 0 ("unlimited")
// for unset, zero, or unparseable values — paid tiers without this entitlement
// fall through here. 1 GB = 1<<30 bytes (binary GiB) to match how operators
// reason about storage allocation.
func parseStorageLimitBytes(raw sql.NullString) int64 {
	if !raw.Valid || raw.String == "" {
		return 0
	}
	var gb int64
	if err := json.Unmarshal([]byte(raw.String), &gb); err != nil || gb <= 0 {
		return 0
	}
	return gb * (1 << 30)
}

// parseTenantResourceLimits decodes Free-plan fair-use entitlements into the
// wire shape Foghorn already consumes. These values are tenant-plan policy, not
// cluster capacity; paid tiers normally omit the keys and therefore return nil.
func parseTenantResourceLimits(raw sql.NullString) *tenantlimitspb.TenantResourceLimits {
	if !raw.Valid || raw.String == "" {
		return nil
	}
	var entitlements map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw.String), &entitlements); err != nil {
		return nil
	}
	limits := &tenantlimitspb.TenantResourceLimits{
		MaxStreams: parsePositiveInt32(entitlements["max_concurrent_streams"]),
		MaxViewers: parsePositiveInt32(entitlements["max_concurrent_viewers"]),
	}
	if limits.MaxStreams == 0 && limits.MaxViewers == 0 {
		return nil
	}
	return limits
}

func parsePositiveInt32(raw json.RawMessage) int32 {
	if len(raw) == 0 {
		return 0
	}
	var v int64
	if err := json.Unmarshal(raw, &v); err != nil || v <= 0 || v > int64(^uint32(0)>>1) {
		return 0
	}
	return int32(v)
}

// resolveCurrentPeriod returns the billing period bounds, falling back to the
// current calendar month when the subscription has no period set (e.g. brand
// new free-tier signups before the first invoice cycle).
func resolveCurrentPeriod(start, end sql.NullTime, now time.Time) (time.Time, time.Time) {
	if start.Valid && end.Valid && end.Time.After(start.Time) {
		return start.Time, end.Time
	}
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	monthEnd := monthStart.AddDate(0, 1, 0)
	return monthStart, monthEnd
}

// computeAllowances loads the tenant's tier pricing rule for delivered_minutes
// and the corresponding current-period usage, returning a single allowance
// snapshot.
//
// is_free_tier is set from the tier identity (tier_name == 'free'), not from
// the meter's unit_price. A paid tier with a coincidentally zero-priced meter
// is not "free" for admission purposes.
//
// Surfaced via GetTenantBillingStatusResponse.Allowances so Foghorn
// PUSH_REWRITE can apply load-aware admission without a second RPC.
func (s *PurserServer) computeAllowances(ctx context.Context, tenantID, tierID string, periodStart, periodEnd time.Time) []*meteringpb.MeterAllowance {
	const meter = "delivered_minutes"

	var pricing purserdb.GetAllowancePricingRow
	err := fwdb.RetryPostgres(ctx, fwdb.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		pricing, queryErr = purserdb.New(s.db).GetAllowancePricing(ctx, purserdb.GetAllowancePricingParams{
			TierID: tierID, Meter: meter,
		})
		return queryErr
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"tier_id":   tierID,
			"meter":     meter,
			"error":     err,
		}).Warn("Failed to load pricing rule for allowance")
		return nil
	}

	var used float64
	if err := fwdb.RetryPostgres(ctx, fwdb.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		used, queryErr = purserdb.New(s.db).SumAllowanceUsage(ctx, purserdb.SumAllowanceUsageParams{
			TenantID: tenantID, Meter: meter,
			PeriodStart: sql.NullTime{Time: periodStart, Valid: true},
			PeriodEnd:   sql.NullTime{Time: periodEnd, Valid: true},
		})
		return queryErr
	}); err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"meter":     meter,
			"error":     err,
		}).Warn("Failed to sum current-period delivered_minutes for allowance")
		return nil
	}

	remaining := pricing.IncludedQuantity - used
	if remaining < 0 {
		remaining = 0
	}

	return []*meteringpb.MeterAllowance{
		{
			Meter:      meter,
			Included:   pricing.IncludedQuantity,
			Used:       used,
			Remaining:  remaining,
			Exhausted:  used >= pricing.IncludedQuantity,
			IsFreeTier: pricing.TierName == "free",
		},
	}
}

// parseDVRPolicy decodes the DVR-policy entitlement bundle into pb.DVRPolicy.
// Each key is JSON-encoded in tier_entitlements.value (e.g. 86400, true). Missing
// keys leave the corresponding field at its zero value; pkg/dvrpolicy.Resolve
// then applies its safety fallback.
func parseDVRPolicy(raw sql.NullString) *sharedpb.DVRPolicy {
	if !raw.Valid || raw.String == "" {
		return nil
	}
	var entitlements map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw.String), &entitlements); err != nil {
		return nil
	}
	out := &sharedpb.DVRPolicy{}
	if v, ok := entitlements["dvr_default_window_seconds"]; ok {
		var n int32
		if err := json.Unmarshal(v, &n); err == nil {
			out.DefaultWindowSeconds = n
		}
	}
	if v, ok := entitlements["dvr_max_window_seconds"]; ok {
		var n int32
		if err := json.Unmarshal(v, &n); err == nil {
			out.MaxWindowSeconds = n
		}
	}
	if v, ok := entitlements["dvr_default_segment_duration_seconds"]; ok {
		var n int32
		if err := json.Unmarshal(v, &n); err == nil {
			out.DefaultSegmentDurationSeconds = n
		}
	}
	if v, ok := entitlements["dvr_max_entries"]; ok {
		var n int32
		if err := json.Unmarshal(v, &n); err == nil {
			out.MaxEntries = n
		}
	}
	if v, ok := entitlements["dvr_allow_cluster_extension"]; ok {
		var b bool
		if err := json.Unmarshal(v, &b); err == nil {
			out.AllowClusterExtension = b
		}
	}
	return out
}

// parseRetentionDays decodes a tier_entitlements / subscription_entitlement_overrides
// value into an int32 days count. The canonical shape is a bare JSON integer
// (e.g. 90). Returns 0 when missing or unparseable so callers fall back to the
// system default.
func parseRetentionDays(raw sql.NullString) int32 {
	if !raw.Valid || raw.String == "" {
		return 0
	}
	var asInt int32
	if err := json.Unmarshal([]byte(raw.String), &asInt); err == nil && asInt > 0 {
		return asInt
	}
	return 0
}

// NOTE: Usage ingestion is handled via Kafka (billing.usage_reports topic)
// consumed by JobManager in handlers/jobs.go. No gRPC ingestion endpoint needed.
// The processUsageSummary and updateInvoiceDraft logic lives in handlers/jobs.go

// GetUsageRecords returns usage records for a tenant with cursor pagination
func (s *PurserServer) GetUsageRecords(ctx context.Context, req *purserpb.GetUsageRecordsRequest) (*purserpb.UsageRecordsResponse, error) {
	tenantID := req.GetTenantId()
	ctxTenantID := middleware.GetTenantID(ctx)
	isServiceCall := middleware.IsServiceCall(ctx)
	if !isServiceCall && ctxTenantID == "" {
		return nil, status.Error(codes.PermissionDenied, "tenant context required")
	}

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

	queryParams := purserdb.ListUsageRecordsParams{
		TenantID: tenantID, FilterCluster: req.GetClusterId() != "", ClusterID: req.GetClusterId(),
		FilterUsageType: req.GetUsageType() != "", UsageType: req.GetUsageType(),
		WindowStart: req.GetTimeRange().GetStart().AsTime(), WindowEnd: req.GetTimeRange().GetEnd().AsTime(),
		HasCursor: params.Cursor != nil, Backward: params.Direction == pagination.Backward,
		ResultLimit: int32(params.Limit + 1),
	}
	if params.Cursor != nil {
		queryParams.CursorAt = params.Cursor.Timestamp
		queryParams.CursorID = params.Cursor.ID
	}
	rows, err := purserdb.New(s.db).ListUsageRecords(ctx, queryParams)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	records := make([]*purserpb.UsageRecord, 0, len(rows))
	for _, row := range rows {
		rec := &purserpb.UsageRecord{
			Id: row.ID, TenantId: row.TenantID, ClusterId: row.ClusterID,
			UsageType: row.UsageType, UsageValue: row.UsageValue, Granularity: row.Granularity,
		}
		if row.CreatedAt.Valid {
			rec.CreatedAt = timestamppb.New(row.CreatedAt.Time)
		}
		if row.PeriodStart.Valid {
			rec.PeriodStart = timestamppb.New(row.PeriodStart.Time)
		}
		if row.PeriodEnd.Valid {
			rec.PeriodEnd = timestamppb.New(row.PeriodEnd.Time)
		}
		if len(row.UsageDetails) > 0 {
			var detailsMap map[string]any
			if json.Unmarshal(row.UsageDetails, &detailsMap) == nil {
				rec.UsageDetails = mapToProtoStruct(detailsMap)
			}
		}
		records = append(records, rec)
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

	resp := &purserpb.UsageRecordsResponse{
		UsageRecords: records,
		TenantId:     tenantID,
		Filters: &purserpb.UsageFilters{
			ClusterId: req.GetClusterId(),
			UsageType: req.GetUsageType(),
			TimeRange: req.GetTimeRange(),
		},
		Pagination: pagination.BuildResponse(resultsLen, params.Limit, params.Direction, int32(len(records)), startCursor, endCursor),
	}

	return resp, nil
}

// GetUsageAggregates rolls canonical 5-minute usage_records into chart buckets.
func (s *PurserServer) GetUsageAggregates(ctx context.Context, req *purserpb.GetUsageAggregatesRequest) (*purserpb.GetUsageAggregatesResponse, error) {
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

	rows, err := purserdb.New(s.db).ListUsageAggregates(ctx, purserdb.ListUsageAggregatesParams{
		Granularity: granularity, TenantID: tenantID, WindowStart: start, WindowEnd: end,
		FilterUsageTypes: len(req.GetUsageTypes()) > 0, UsageTypes: req.GetUsageTypes(),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	aggregates := make([]*purserpb.UsageAggregate, 0, len(rows))
	for _, row := range rows {
		agg := &purserpb.UsageAggregate{UsageType: row.UsageType, UsageValue: row.UsageValue,
			Granularity: row.Granularity, PeriodStart: timestamppb.New(row.PeriodStart), PeriodEnd: timestamppb.New(row.PeriodEnd)}
		aggregates = append(aggregates, agg)
	}

	return &purserpb.GetUsageAggregatesResponse{Aggregates: aggregates}, nil
}

// CheckUserLimit checks if a tenant can add more users
func (s *PurserServer) CheckUserLimit(ctx context.Context, req *purserpb.CheckUserLimitRequest) (*purserpb.CheckUserLimitResponse, error) {
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
			}).Error("Failed to get user count from Commodore")
			return nil, status.Error(codes.Unavailable, "failed to verify user limit")
		}
		currentUsers = userCount.ActiveCount
	} else {
		s.logger.Error("Commodore client not available for user limit check")
		return nil, status.Error(codes.Unavailable, "user limit service unavailable")
	}

	// Get tier limit
	maxUsersJSON, err := purserdb.New(s.db).GetActiveTenantMaxUsers(ctx, tenantID)

	if errors.Is(err, sql.ErrNoRows) {
		// No subscription, use default limit (10 for free tier)
		maxUsersJSON = "10"
	} else if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Error("Failed to get tier limit")
		return nil, status.Error(codes.Internal, "failed to read tier limit")
	}
	var maxUsers int32
	if unmarshalErr := json.Unmarshal([]byte(maxUsersJSON), &maxUsers); unmarshalErr != nil || maxUsers < 0 {
		s.logger.WithError(unmarshalErr).WithField("tenant_id", tenantID).Error("Invalid max_users entitlement")
		return nil, status.Error(codes.Internal, "invalid user limit configuration")
	}

	// Unlimited if max_users is 0.
	if maxUsers == 0 {
		return &purserpb.CheckUserLimitResponse{
			Allowed:      true,
			CurrentUsers: currentUsers,
			MaxUsers:     0, // 0 = unlimited
		}, nil
	}

	allowed := currentUsers < maxUsers
	resp := &purserpb.CheckUserLimitResponse{
		Allowed:      allowed,
		CurrentUsers: currentUsers,
		MaxUsers:     maxUsers,
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
func (s *PurserServer) GetSubscription(ctx context.Context, req *purserpb.GetSubscriptionRequest) (*purserpb.GetSubscriptionResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	row, err := purserdb.New(s.db).GetCurrentTenantSubscription(ctx, tenantID)

	if errors.Is(err, sql.ErrNoRows) {
		return &purserpb.GetSubscriptionResponse{
			Error: "No active subscription found",
		}, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	sub := tenantSubscriptionFromCurrentRow(row)

	// Per-tenant override rows live in their own tables; load them so the
	// returned subscription reflects the full effective state. Mutation
	// callers (UpdateSubscription) and direct reads must see the same shape.
	if overrides, err := loadSubscriptionPricingOverrides(ctx, s.db, sub.Id); err != nil {
		return nil, status.Errorf(codes.Internal, "load pricing overrides: %v", err)
	} else if len(overrides) > 0 {
		sub.PricingOverrides = overrides
	}
	if overrides, err := loadSubscriptionEntitlementOverrides(ctx, s.db, sub.Id); err != nil {
		return nil, status.Errorf(codes.Internal, "load entitlement overrides: %v", err)
	} else if len(overrides) > 0 {
		sub.EntitlementOverrides = overrides
	}

	return &purserpb.GetSubscriptionResponse{
		Subscription: sub,
	}, nil
}

func tenantSubscriptionFromCurrentRow(row purserdb.GetCurrentTenantSubscriptionRow) *purserpb.TenantSubscription {
	sub := purserpb.TenantSubscription{
		Id: row.ID.String(), TenantId: row.TenantID.String(), TierId: row.TierID.String(), Status: row.Status,
		StartedAt: timestamppb.New(row.StartedAt), CreatedAt: timestamppb.New(row.CreatedAt.Time),
		UpdatedAt: timestamppb.New(row.UpdatedAt.Time), BillingModel: row.BillingModel,
		CustomFeatures: scanBillingFeatures([]byte(row.CustomFeaturesText)),
		BillingAddress: scanBillingAddress([]byte(row.BillingAddressText)),
	}
	if row.BillingEmail.Valid {
		sub.BillingEmail = row.BillingEmail.String
	}
	if row.TrialEndsAt.Valid {
		sub.TrialEndsAt = timestamppb.New(row.TrialEndsAt.Time)
	}
	if row.NextBillingDate.Valid {
		sub.NextBillingDate = timestamppb.New(row.NextBillingDate.Time)
	}
	if row.CancelledAt.Valid {
		sub.CancelledAt = timestamppb.New(row.CancelledAt.Time)
	}
	if row.BillingPeriodStart.Valid {
		sub.BillingPeriodStart = timestamppb.New(row.BillingPeriodStart.Time)
	}
	if row.BillingPeriodEnd.Valid {
		sub.BillingPeriodEnd = timestamppb.New(row.BillingPeriodEnd.Time)
	}
	if row.PaymentMethod.Valid {
		sub.PaymentMethod = &row.PaymentMethod.String
	}
	if row.PaymentReference.Valid {
		sub.PaymentReference = &row.PaymentReference.String
	}
	if row.TaxID.Valid {
		sub.TaxId = &row.TaxID.String
	}
	if row.TaxRate.Valid {
		if taxRate, err := strconv.ParseFloat(row.TaxRate.String, 64); err == nil {
			sub.TaxRate = &taxRate
		}
	}
	if row.StripeCustomerID.Valid {
		sub.StripeCustomerId = &row.StripeCustomerID.String
	}
	if row.StripeSubscriptionID.Valid {
		sub.StripeSubscriptionId = &row.StripeSubscriptionID.String
	}
	if row.StripeSubscriptionStatus.Valid {
		sub.StripeSubscriptionStatus = &row.StripeSubscriptionStatus.String
	}
	if row.StripeCurrentPeriodEnd.Valid {
		sub.StripeCurrentPeriodEnd = timestamppb.New(row.StripeCurrentPeriodEnd.Time)
	}
	if row.DunningAttempts.Valid {
		sub.DunningAttempts = row.DunningAttempts.Int32
	}
	if row.MollieSubscriptionID.Valid {
		sub.MollieSubscriptionId = &row.MollieSubscriptionID.String
	}
	if row.PendingTierID.Valid {
		value := row.PendingTierID.UUID.String()
		sub.PendingTierId = &value
	}
	if row.PendingEffectiveAt.Valid {
		sub.PendingEffectiveAt = timestamppb.New(row.PendingEffectiveAt.Time)
	}
	if row.PendingReason.Valid {
		sub.PendingReason = &row.PendingReason.String
	}
	return &sub
}

// CreateSubscription creates a new subscription for a tenant
func (s *PurserServer) CreateSubscription(ctx context.Context, req *purserpb.CreateSubscriptionRequest) (*purserpb.TenantSubscription, error) {
	tenantID := req.GetTenantId()
	tierID := req.GetTierId()
	billingEmail := req.GetBillingEmail()

	if tenantID == "" || tierID == "" || billingEmail == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id, tier_id, and billing_email are required")
	}

	userID := middleware.GetUserID(ctx)
	// Verify tier exists
	tierExists, err := purserdb.New(s.db).ActiveBillingTierExists(ctx, tierID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to verify billing tier: %v", err)
	}
	if !tierExists {
		return nil, status.Error(codes.NotFound, "billing tier not found")
	}

	billingModel := req.GetBillingModel()
	if billingModel == "" {
		billingModel = "postpaid"
	}
	if billingModel != "postpaid" && billingModel != "prepaid" {
		return nil, status.Error(codes.InvalidArgument, "billing_model must be postpaid or prepaid")
	}
	featuresJSON, err := marshalBillingFeatures(req.GetCustomFeatures())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid custom_features: %v", err)
	}

	// Create subscription
	subUUID := uuid.New()
	subID := subUUID.String()
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

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort post-commit

	if err = purserdb.New(tx).InsertTenantSubscription(ctx, purserdb.InsertTenantSubscriptionParams{
		ID: subUUID, TenantID: tenantID, TierID: tierID,
		BillingEmail: sql.NullString{String: billingEmail, Valid: true}, BillingModel: billingModel,
		Now: now, TrialEndsAt: trialEndsAt,
		NextBillingDate:    sql.NullTime{Time: periodEnd, Valid: true},
		BillingPeriodStart: sql.NullTime{Time: periodStart, Valid: true},
		BillingPeriodEnd:   sql.NullTime{Time: periodEnd, Valid: true},
		PaymentMethod:      req.GetPaymentMethod(), CustomFeatures: featuresJSON,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create subscription: %v", err)
	}

	if _, err := s.EnqueueBillingEventTx(ctx, tx, eventSubscriptionCreated, tenantID, userID, "subscription", subID, &ipcpb.BillingEvent{
		SubscriptionId: subID,
		Status:         "active",
		Provider:       req.GetPaymentMethod(),
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "enqueue subscription_created: %v", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit subscription create: %v", err)
	}

	sub := &purserpb.TenantSubscription{
		Id:                 subID,
		TenantId:           tenantID,
		TierId:             tierID,
		Status:             "active",
		BillingEmail:       billingEmail,
		BillingModel:       billingModel,
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
	if req.GetCustomFeatures() != nil {
		sub.CustomFeatures = req.GetCustomFeatures()
	}

	return sub, nil
}

// UpdateSubscription updates an existing subscription, including per-tenant
// pricing/entitlement overrides in purser.subscription_pricing_overrides and
// purser.subscription_entitlement_overrides. The header update and override
// writes commit in a single transaction so a partial failure cannot leave
// custom_features / tier / status touched while overrides rolled back.
func (s *PurserServer) UpdateSubscription(ctx context.Context, req *purserpb.UpdateSubscriptionRequest) (*purserpb.TenantSubscription, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	userID := middleware.GetUserID(ctx)

	// Tier changes go through ChangeBillingTier so cluster access is reconciled
	// and downgrades are deferred to period end. Accept the field only when it
	// matches the current tier_id (idempotent re-state from callers); reject
	// any mismatch.
	if req.TierId != nil && *req.TierId != "" {
		currentTierID, scanErr := purserdb.New(s.db).GetTenantSubscriptionTierID(ctx, tenantID)
		if scanErr != nil {
			if errors.Is(scanErr, sql.ErrNoRows) {
				return nil, status.Error(codes.NotFound, "subscription not found")
			}
			return nil, status.Errorf(codes.Internal, "load current tier: %v", scanErr)
		}
		if *req.TierId != currentTierID {
			return nil, status.Error(codes.FailedPrecondition, "tier_id changes go through ChangeBillingTier")
		}
	}

	update := purserdb.UpdateTenantSubscriptionFieldsParams{
		TierID: uuid.Nil.String(), CustomFeatures: json.RawMessage("{}"), TenantID: tenantID,
	}
	if req.TierId != nil {
		update.SetTierID = true
		update.TierID = *req.TierId
	}
	if req.BillingEmail != nil {
		update.SetBillingEmail = true
		update.BillingEmail = *req.BillingEmail
	}
	if req.PaymentMethod != nil {
		update.SetPaymentMethod = true
		update.PaymentMethod = *req.PaymentMethod
	}
	if req.Status != nil {
		update.SetStatus = true
		update.Status = *req.Status
	}
	if req.BillingPeriodStart != nil {
		update.SetBillingPeriodStart = true
		update.BillingPeriodStart = sql.NullTime{Time: req.BillingPeriodStart.AsTime(), Valid: true}
	}
	if req.BillingPeriodEnd != nil {
		update.SetBillingPeriodEnd = true
		update.BillingPeriodEnd = sql.NullTime{Time: req.BillingPeriodEnd.AsTime(), Valid: true}
	}

	if req.CustomFeatures != nil {
		featuresJSON, err := marshalBillingFeatures(req.CustomFeatures)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid custom_features: %v", err)
		}
		update.SetCustomFeatures = true
		update.CustomFeatures = featuresJSON
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort post-commit

	rows, err := purserdb.New(tx).UpdateTenantSubscriptionFields(ctx, update)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update subscription: %v", err)
	}
	if rows == 0 {
		return nil, status.Error(codes.NotFound, "subscription not found")
	}
	if overrideErr := s.applySubscriptionOverridesTx(ctx, tx, tenantID, req); overrideErr != nil {
		return nil, overrideErr
	}

	if eventState, scanErr := purserdb.New(tx).GetUpdatedSubscriptionEventState(ctx, tenantID); scanErr == nil {
		updatedSubID := eventState.ID.String()
		if _, enqErr := s.EnqueueBillingEventTx(ctx, tx, eventSubscriptionUpdated, tenantID, userID, "subscription", updatedSubID, &ipcpb.BillingEvent{
			SubscriptionId: updatedSubID,
			Status:         eventState.Status,
			Provider:       eventState.PaymentMethod,
		}); enqErr != nil {
			return nil, status.Errorf(codes.Internal, "enqueue subscription_updated: %v", enqErr)
		}
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return nil, status.Errorf(codes.Internal, "commit subscription update: %v", commitErr)
	}

	// When tier_id changes (upgrade / downgrade), invalidate downstream caches
	// so per-tenant runtime caps + allowance state (cached in Foghorn's
	// streamContext via Commodore.ValidateStreamKey) are re-fetched on the
	// next admission. Mirrors the same pattern used for balance changes at
	// server.go:5117. Best-effort fan-out — failure is logged but does not
	// abort the subscription update; cache TTL (10 min postpaid / 1 min
	// prepaid) provides the eventual-consistency backstop.
	if req.TierId != nil && s.commodoreClient != nil {
		invalidateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, invErr := s.commodoreClient.InvalidateTenantCache(invalidateCtx, tenantID, "tier_changed"); invErr != nil {
			s.logger.WithError(invErr).WithField("tenant_id", tenantID).Warn("Failed to invalidate tenant cache on tier change")
		}
	}

	// Return updated subscription
	resp, err := s.GetSubscription(ctx, &purserpb.GetSubscriptionRequest{TenantId: tenantID})
	if err != nil {
		return nil, err
	}
	return resp.Subscription, nil
}

// applySubscriptionOverridesTx upserts per-tenant pricing/entitlement overrides
// from an UpdateSubscriptionRequest inside an existing transaction so the
// header update and override writes commit together. Pricing rules and
// entitlement keys are replaced wholesale when the request supplies them;
// ClearPricingOverrides / ClearEntitlementOverrides delete all rows for the
// subscription. Supplying both a clear flag and rows is treated as
// "clear, then insert".
func (s *PurserServer) applySubscriptionOverridesTx(ctx context.Context, tx *sql.Tx, tenantID string, req *purserpb.UpdateSubscriptionRequest) error {
	if !req.GetClearPricingOverrides() && len(req.GetPricingOverrides()) == 0 &&
		!req.GetClearEntitlementOverrides() && len(req.GetEntitlementOverrides()) == 0 {
		return nil
	}

	queries := purserdb.New(tx)
	subscriptionID, err := queries.GetActiveSubscriptionID(ctx, tenantID)
	if err != nil {
		return status.Errorf(codes.Internal, "lookup subscription id: %v", err)
	}

	if req.GetClearPricingOverrides() || len(req.GetPricingOverrides()) > 0 {
		if err := queries.DeleteSubscriptionPricingOverrides(ctx, subscriptionID); err != nil {
			return status.Errorf(codes.Internal, "clear pricing overrides: %v", err)
		}
		for _, rule := range req.GetPricingOverrides() {
			if err := validatePricingOverrideRule(rule); err != nil {
				return status.Errorf(codes.InvalidArgument, "invalid pricing override %q: %v", rule.GetMeter(), err)
			}
			currency := normalizeOverrideCurrency(rule.GetCurrency())
			if err := queries.UpsertSubscriptionPricingOverride(ctx, purserdb.UpsertSubscriptionPricingOverrideParams{
				SubscriptionID: subscriptionID, Meter: rule.GetMeter(), Model: rule.GetModel(), Currency: currency,
				IncludedQuantity: rule.GetIncludedQuantity(), UnitPrice: rule.GetUnitPrice(), Config: rule.GetConfigJson(),
			}); err != nil {
				return status.Errorf(codes.Internal, "upsert pricing override %q: %v", rule.GetMeter(), err)
			}
		}
	}

	if req.GetClearEntitlementOverrides() || len(req.GetEntitlementOverrides()) > 0 {
		if err := queries.DeleteSubscriptionEntitlementOverrides(ctx, subscriptionID); err != nil {
			return status.Errorf(codes.Internal, "clear entitlement overrides: %v", err)
		}
		for k, v := range req.GetEntitlementOverrides() {
			if k == "" {
				return status.Error(codes.InvalidArgument, "entitlement override key is required")
			}
			if !json.Valid([]byte(v)) {
				return status.Errorf(codes.InvalidArgument, "entitlement override %q must be valid JSON", k)
			}
			if err := queries.UpsertSubscriptionEntitlementOverride(ctx, purserdb.UpsertSubscriptionEntitlementOverrideParams{
				SubscriptionID: subscriptionID, Key: k, Value: json.RawMessage(v),
			}); err != nil {
				return status.Errorf(codes.Internal, "upsert entitlement override %q: %v", k, err)
			}
		}
	}
	return nil
}

func validatePricingOverrideRule(rule *purserpb.PricingRule) error {
	if rule == nil {
		return errors.New("rule is nil")
	}
	if !rating.ValidMeter(rating.Meter(rule.GetMeter())) {
		return fmt.Errorf("invalid meter %q", rule.GetMeter())
	}
	if rule.GetModel() != "" && !rating.ValidModel(rating.Model(rule.GetModel())) {
		return fmt.Errorf("unsupported model %q", rule.GetModel())
	}
	if currency := normalizeOverrideCurrency(rule.GetCurrency()); currency != "" {
		if len(currency) != 3 {
			return fmt.Errorf("currency %q must be a 3-letter code", rule.GetCurrency())
		}
		for _, ch := range currency {
			if ch < 'A' || ch > 'Z' {
				return fmt.Errorf("currency %q must use letters only", rule.GetCurrency())
			}
		}
	}
	included := decimal.Zero
	if rule.GetIncludedQuantity() != "" {
		d, err := decimal.NewFromString(rule.GetIncludedQuantity())
		if err != nil {
			return fmt.Errorf("included_quantity %q: %w", rule.GetIncludedQuantity(), err)
		}
		if d.IsNegative() {
			return fmt.Errorf("included_quantity cannot be negative")
		}
		included = d
	}
	unitPrice := decimal.Zero
	if rule.GetUnitPrice() != "" {
		d, err := decimal.NewFromString(rule.GetUnitPrice())
		if err != nil {
			return fmt.Errorf("unit_price %q: %w", rule.GetUnitPrice(), err)
		}
		if d.IsNegative() {
			return fmt.Errorf("unit_price cannot be negative")
		}
		unitPrice = d
	}
	config := rule.GetConfigJson()
	if config == "" {
		config = "{}"
	}
	if !json.Valid([]byte(config)) {
		return fmt.Errorf("config_json must be valid JSON")
	}
	var cfg map[string]any
	if config != "{}" {
		if err := json.Unmarshal([]byte(config), &cfg); err != nil {
			return fmt.Errorf("config_json must be a JSON object: %w", err)
		}
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	if rule.GetModel() != "" {
		if err := rating.ValidateRuleShape(rating.Rule{
			Meter:            rating.Meter(rule.GetMeter()),
			Model:            rating.Model(rule.GetModel()),
			IncludedQuantity: included,
			UnitPrice:        unitPrice,
			Config:           cfg,
		}); err != nil {
			return err
		}
	}
	return nil
}

func normalizeOverrideCurrency(currency string) string {
	return strings.ToUpper(strings.TrimSpace(currency))
}

// CancelSubscription cancels a tenant's subscription
func (s *PurserServer) CancelSubscription(ctx context.Context, req *purserpb.CancelSubscriptionRequest) (*emptypb.Empty, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	userID := middleware.GetUserID(ctx)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort post-commit

	queries := purserdb.New(tx)
	subscriptionID, scanErr := queries.GetCancelableSubscriptionID(ctx, tenantID)
	if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		return nil, status.Errorf(codes.Internal, "lookup subscription: %v", scanErr)
	}

	// A sub-floor balance is intentionally not customer-payable. On account
	// closure, record the waiver explicitly before clearing it so the amount
	// cannot disappear from financial reconciliation or strand a cancelled
	// tenant with an unreachable carry balance.
	if _, err = queries.RecordAccountClosureCollectionWriteoffs(ctx, tenantID); err != nil {
		return nil, status.Errorf(codes.Internal, "record collection balance writeoff: %v", err)
	}
	if _, err = queries.ClearAccountClosureCollectionBalances(ctx, tenantID); err != nil {
		return nil, status.Errorf(codes.Internal, "clear written-off collection balance: %v", err)
	}

	rows, err := queries.CancelTenantSubscriptions(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to cancel subscription: %v", err)
	}

	if rows == 0 {
		return nil, status.Error(codes.NotFound, "subscription not found")
	}

	if subscriptionID != uuid.Nil {
		subscriptionIDText := subscriptionID.String()
		if _, enqErr := s.EnqueueBillingEventTx(ctx, tx, eventSubscriptionCanceled, tenantID, userID, "subscription", subscriptionIDText, &ipcpb.BillingEvent{
			SubscriptionId: subscriptionIDText,
			Status:         "cancelled",
		}); enqErr != nil {
			return nil, status.Errorf(codes.Internal, "enqueue subscription_canceled: %v", enqErr)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit subscription cancel: %v", err)
	}

	return &emptypb.Empty{}, nil
}

// ============================================================================
// INVOICE SERVICE
// ============================================================================

// GetInvoice returns a specific invoice with tenant isolation
func (s *PurserServer) GetInvoice(ctx context.Context, req *purserpb.GetInvoiceRequest) (*purserpb.GetInvoiceResponse, error) {
	invoiceID := req.GetInvoiceId()
	if invoiceID == "" {
		return nil, status.Error(codes.InvalidArgument, "invoice_id required")
	}

	ctxTenantID := middleware.GetTenantID(ctx)
	isServiceCall := middleware.IsServiceCall(ctx)
	if !isServiceCall && ctxTenantID == "" {
		return nil, status.Error(codes.PermissionDenied, "tenant context required")
	}

	row, err := purserdb.New(s.db).GetInvoiceForCaller(ctx, purserdb.GetInvoiceForCallerParams{
		InvoiceID:     invoiceID,
		EnforceTenant: !isServiceCall,
		TenantID:      ctxTenantID,
	})

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "invoice not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	invoice := purserpb.Invoice{
		Id: row.ID, TenantId: row.TenantID, Amount: row.Amount,
		BaseAmount: row.BaseAmount, MeteredAmount: row.MeteredAmount,
		PrepaidCreditApplied: row.PrepaidCreditApplied, Currency: row.Currency,
		Status: row.Status, DueDate: timestamppb.New(row.DueDate),
		CreatedAt: timestamppb.New(row.CreatedAt.Time), UpdatedAt: timestamppb.New(row.UpdatedAt.Time),
		GrossMeteredAmount: row.GrossMeteredAmount,
	}
	if row.PaidAt.Valid {
		invoice.PaidAt = timestamppb.New(row.PaidAt.Time)
	}
	if row.PeriodStart.Valid {
		invoice.PeriodStart = timestamppb.New(row.PeriodStart.Time)
	}
	if row.PeriodEnd.Valid {
		invoice.PeriodEnd = timestamppb.New(row.PeriodEnd.Time)
	}

	// Convert usage_details JSONB to protobuf Struct.
	if len(row.UsageDetails) > 0 {
		var detailsMap map[string]any
		if json.Unmarshal(row.UsageDetails, &detailsMap) == nil {
			invoice.UsageDetails = mapToProtoStruct(detailsMap)
		}
	}
	lineItems, lineErr := s.loadInvoiceLineItems(ctx, invoice.Id, invoice.TenantId)
	if lineErr != nil {
		return nil, status.Errorf(codes.Internal, "%v", lineErr)
	}
	invoice.LineItems = lineItems

	// Get tier info. NotFound is tolerated (tier may have been removed since
	// the invoice was issued); other errors fail loud — a broken normalized
	// pricing-rules table must not return a partial invoice with nil tier.
	var tier *purserpb.BillingTier
	if row.TierID != "" {
		tierResp, tierErr := s.GetBillingTier(ctx, &purserpb.GetBillingTierRequest{TierId: row.TierID})
		switch {
		case tierErr == nil:
			tier = tierResp
		case status.Code(tierErr) == codes.NotFound:
			// leave tier nil
		default:
			return nil, tierErr
		}
	}

	return &purserpb.GetInvoiceResponse{
		Invoice: &invoice,
		Tier:    tier,
	}, nil
}

// ListInvoices returns invoices for a tenant
func (s *PurserServer) ListInvoices(ctx context.Context, req *purserpb.ListInvoicesRequest) (*purserpb.ListInvoicesResponse, error) {
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

	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid cursor: %v", err)
	}

	statusFilter := ""
	if req.Status != nil {
		statusFilter = strings.TrimSpace(*req.Status)
	}
	cursorAt := sql.NullTime{}
	cursorID := uuid.Nil.String()
	if params.Cursor != nil {
		cursorAt = sql.NullTime{Time: params.Cursor.Timestamp, Valid: true}
		cursorID = params.Cursor.ID
	}
	rows, err := purserdb.New(s.db).ListInvoicesForTenant(ctx, purserdb.ListInvoicesForTenantParams{
		TenantID: tenantID, FilterStatus: statusFilter != "", Status: statusFilter,
		HasCursor: params.Cursor != nil, Backward: params.Direction == pagination.Backward,
		CursorAt: cursorAt, CursorID: cursorID, ResultLimit: int32(params.Limit + 1),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	invoices := make([]*purserpb.Invoice, 0, len(rows))
	for _, row := range rows {
		inv := purserpb.Invoice{
			Id: row.ID, TenantId: row.TenantID, Amount: row.Amount,
			BaseAmount: row.BaseAmount, MeteredAmount: row.MeteredAmount,
			PrepaidCreditApplied: row.PrepaidCreditApplied, Currency: row.Currency,
			Status: row.Status, DueDate: timestamppb.New(row.DueDate),
			CreatedAt: timestamppb.New(row.CreatedAt.Time), UpdatedAt: timestamppb.New(row.UpdatedAt.Time),
			GrossMeteredAmount: row.GrossMeteredAmount,
		}
		if row.PaidAt.Valid {
			inv.PaidAt = timestamppb.New(row.PaidAt.Time)
		}
		if row.PeriodStart.Valid {
			inv.PeriodStart = timestamppb.New(row.PeriodStart.Time)
		}
		if row.PeriodEnd.Valid {
			inv.PeriodEnd = timestamppb.New(row.PeriodEnd.Time)
		}
		// Convert usage_details JSONB to protobuf Struct.
		if len(row.UsageDetails) > 0 {
			var details map[string]any
			if json.Unmarshal(row.UsageDetails, &details) == nil {
				inv.UsageDetails = mapToProtoStruct(details)
			}
		}
		lineItems, lineErr := s.loadInvoiceLineItems(ctx, inv.Id, inv.TenantId)
		if lineErr != nil {
			return nil, status.Errorf(codes.Internal, "%v", lineErr)
		}
		inv.LineItems = lineItems
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

	resp := &purserpb.ListInvoicesResponse{
		Invoices:   invoices,
		Pagination: pagination.BuildResponse(resultsLen, params.Limit, params.Direction, int32(len(invoices)), startCursor, endCursor),
	}

	return resp, nil
}

// ============================================================================
// PAYMENT SERVICE
// ============================================================================

type cryptoPaymentQuoteDetails struct {
	WalletAddress           string
	ExpectedAmountBaseUnits string
	ExpectedAmountToken     string
	QuotedPriceUSD          string
	QuoteSource             string
	AssetSymbol             string
	Network                 string
	QuotedAt                time.Time
	ExpiresAt               time.Time
}

type cryptoPaymentPlan struct {
	Params            handlers.DepositAddressParams
	ExpectedBaseUnits *big.Int
	TokenDecimals     int32
	NetworkName       string
	Asset             string
}

// GetPayment returns a tenant-owned invoice payment. Service callers may read
// across tenants; user callers are always constrained by authenticated context.
func (s *PurserServer) GetPayment(ctx context.Context, req *purserpb.GetPaymentRequest) (*purserpb.Payment, error) {
	paymentID := req.GetPaymentId()
	if paymentID == "" {
		return nil, status.Error(codes.InvalidArgument, "payment_id required")
	}
	ctxTenantID := middleware.GetTenantID(ctx)
	isServiceCall := middleware.IsServiceCall(ctx)
	if !isServiceCall && ctxTenantID == "" {
		return nil, status.Error(codes.PermissionDenied, "tenant context required")
	}

	row, err := purserdb.New(s.db).GetInvoicePaymentForCaller(ctx, purserdb.GetInvoicePaymentForCallerParams{
		PaymentID: paymentID, EnforceTenant: !isServiceCall, TenantID: ctxTenantID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "payment not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get payment: %v", err)
	}
	payment := &purserpb.Payment{
		Id: row.ID, InvoiceId: row.InvoiceID, Method: row.Method,
		Amount: row.Amount, Currency: row.Currency, Status: row.Status,
		CreatedAt: timestamppb.New(row.CreatedAt.Time), UpdatedAt: timestamppb.New(row.UpdatedAt.Time),
	}
	if row.TxID.Valid {
		payment.TxId = row.TxID.String
	}
	if row.ConfirmedAt.Valid {
		payment.ConfirmedAt = timestamppb.New(row.ConfirmedAt.Time)
	}
	return payment, nil
}

// ListPayments returns tenant-owned invoice payments with keyset pagination.
func (s *PurserServer) ListPayments(ctx context.Context, req *purserpb.ListPaymentsRequest) (*purserpb.ListPaymentsResponse, error) {
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
	params, parseErr := pagination.Parse(req.GetPagination())
	if parseErr != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid cursor: %v", parseErr)
	}

	invoiceFilter, statusFilter, methodFilter := "", "", ""
	if req.InvoiceId != nil {
		invoiceFilter = strings.TrimSpace(*req.InvoiceId)
	}
	if req.Status != nil {
		statusFilter = strings.TrimSpace(*req.Status)
	}
	if req.Method != nil {
		methodFilter = strings.TrimSpace(*req.Method)
	}
	queries := purserdb.New(s.db)
	filterParams := purserdb.CountInvoicePaymentsForTenantParams{
		TenantID: tenantID, FilterInvoice: invoiceFilter != "", InvoiceID: invoiceFilter,
		FilterStatus: statusFilter != "", Status: statusFilter,
		FilterMethod: methodFilter != "", Method: methodFilter,
	}
	totalRows, countErr := queries.CountInvoicePaymentsForTenant(ctx, filterParams)
	if countErr != nil {
		return nil, status.Errorf(codes.Internal, "count payments: %v", countErr)
	}
	totalCount := int32(totalRows)
	if totalRows > int64(^uint32(0)>>1) {
		totalCount = int32(^uint32(0) >> 1)
	}
	cursorAt := sql.NullTime{}
	cursorID := uuid.Nil.String()
	if params.Cursor != nil {
		cursorAt = sql.NullTime{Time: params.Cursor.Timestamp, Valid: true}
		cursorID = params.Cursor.ID
	}
	rows, err := queries.ListInvoicePaymentsForTenant(ctx, purserdb.ListInvoicePaymentsForTenantParams{
		TenantID: tenantID, FilterInvoice: invoiceFilter != "", InvoiceID: invoiceFilter,
		FilterStatus: statusFilter != "", Status: statusFilter,
		FilterMethod: methodFilter != "", Method: methodFilter,
		HasCursor: params.Cursor != nil, Backward: params.Direction == pagination.Backward,
		CursorAt: cursorAt, CursorID: cursorID, ResultLimit: int32(params.Limit + 1),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list payments: %v", err)
	}
	payments := make([]*purserpb.Payment, 0, params.Limit+1)
	for _, row := range rows {
		payment := &purserpb.Payment{
			Id: row.ID, InvoiceId: row.InvoiceID, Method: row.Method,
			Amount: row.Amount, Currency: row.Currency, Status: row.Status,
			CreatedAt: timestamppb.New(row.CreatedAt.Time), UpdatedAt: timestamppb.New(row.UpdatedAt.Time),
		}
		if row.TxID.Valid {
			payment.TxId = row.TxID.String
		}
		if row.ConfirmedAt.Valid {
			payment.ConfirmedAt = timestamppb.New(row.ConfirmedAt.Time)
		}
		payments = append(payments, payment)
	}
	resultsLen := len(payments)
	if resultsLen > params.Limit {
		payments = payments[:params.Limit]
	}
	if params.Direction == pagination.Backward {
		slices.Reverse(payments)
	}
	var startCursor, endCursor string
	if len(payments) > 0 {
		first := payments[0]
		last := payments[len(payments)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.Id)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.Id)
	}
	return &purserpb.ListPaymentsResponse{
		Payments: payments,
		Pagination: pagination.BuildResponse(
			resultsLen, params.Limit, params.Direction, totalCount, startCursor, endCursor,
		),
	}, nil
}

func (d cryptoPaymentQuoteDetails) apply(resp *purserpb.PaymentResponse) {
	resp.WalletAddress = d.WalletAddress
	resp.ExpectedAmountBaseUnits = d.ExpectedAmountBaseUnits
	resp.ExpectedAmountToken = d.ExpectedAmountToken
	resp.QuotedPriceUsd = d.QuotedPriceUSD
	resp.QuoteSource = d.QuoteSource
	resp.AssetSymbol = d.AssetSymbol
	resp.Network = d.Network
	resp.QuotedAt = timestamppb.New(d.QuotedAt)
	resp.ExpiresAt = timestamppb.New(d.ExpiresAt)
}

// CreatePayment initiates a payment for an invoice with tenant isolation
func (s *PurserServer) CreatePayment(ctx context.Context, req *purserpb.PaymentRequest) (*purserpb.PaymentResponse, error) {
	invoiceID := req.GetInvoiceId()
	method := strings.ToLower(req.GetMethod())

	if invoiceID == "" || method == "" {
		return nil, status.Error(codes.InvalidArgument, "invoice_id and method are required")
	}

	userID := middleware.GetUserID(ctx)
	ctxTenantID := middleware.GetTenantID(ctx)
	if ctxTenantID == "" {
		return nil, status.Error(codes.PermissionDenied, "tenant context required")
	}

	// Validate payment method is available
	availableMethods := s.getAvailablePaymentMethods(ctx)
	if !slices.Contains(availableMethods, method) {
		return nil, status.Errorf(codes.InvalidArgument, "payment method %s not available", method)
	}

	if err := s.expireStaleInvoicePayments(ctx, invoiceID, ctxTenantID); err != nil {
		return nil, status.Errorf(codes.Internal, "expire stale invoice payments: %v", err)
	}

	dbTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin payment transaction: %v", err)
	}
	defer dbTx.Rollback() //nolint:errcheck // rollback is best-effort

	if err = lockInvoicePaymentTx(ctx, dbTx, invoiceID); err != nil {
		return nil, status.Errorf(codes.Internal, "lock payment creation: %v", err)
	}
	balance, err := loadInvoiceBalanceTx(ctx, dbTx, invoiceID, ctxTenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "invoice not found or not payable")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load invoice balance: %v", err)
	}

	existing, err := loadActiveInvoicePaymentTx(ctx, dbTx, invoiceID, ctxTenantID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, status.Errorf(codes.Internal, "load pending invoice payment: %v", err)
	}
	if err == nil {
		if existing.ActiveCount != 1 {
			return nil, status.Error(codes.FailedPrecondition, "invoice has multiple pending payments and requires reconciliation")
		}
		if existing.Method != method {
			return nil, status.Errorf(codes.FailedPrecondition, "invoice already has a pending %s payment", existing.Method)
		}
		if !existing.Amount.Equal(balance.AmountDue) {
			return nil, status.Error(codes.FailedPrecondition, "pending payment no longer matches the invoice balance")
		}
		if err = dbTx.Commit(); err != nil {
			return nil, status.Errorf(codes.Internal, "commit payment transaction: %v", err)
		}
		return s.resumeInvoicePayment(ctx, req, balance, existing)
	}

	if !balance.AmountDue.IsPositive() {
		return nil, status.Error(codes.FailedPrecondition, "invoice has no outstanding balance")
	}

	paymentID := uuid.New().String()
	expiresAt := time.Now().Add(30 * time.Minute)
	createdAt := time.Now()
	var txID string
	resp := &purserpb.PaymentResponse{
		Id:        paymentID,
		Amount:    decimalFloat(balance.AmountDue),
		Currency:  balance.Currency,
		Status:    "pending",
		Method:    method,
		ExpiresAt: timestamppb.New(expiresAt),
		CreatedAt: timestamppb.New(createdAt),
	}

	if asset, ok := strings.CutPrefix(method, "crypto_"); ok {
		plan, planErr := s.prepareCryptoPayment(ctx, invoiceID, ctxTenantID, strings.ToUpper(asset), balance.AmountDue, balance.Currency, expiresAt)
		if planErr != nil {
			return nil, status.Errorf(codes.Internal, "prepare crypto payment: %v", planErr)
		}
		details, walletErr := s.createCryptoPaymentTx(ctx, dbTx, plan)
		if walletErr != nil {
			return nil, status.Errorf(codes.Internal, "create crypto payment: %v", walletErr)
		}
		details.apply(resp)
		txID = details.WalletAddress
	}

	if err = purserdb.New(dbTx).CreatePendingInvoicePayment(ctx, purserdb.CreatePendingInvoicePaymentParams{
		PaymentID: paymentID,
		InvoiceID: invoiceID,
		Method:    method,
		Amount:    balance.AmountDue.StringFixed(2),
		Currency:  balance.Currency,
		TxID:      txID,
		CreatedAt: sql.NullTime{Time: createdAt, Valid: true},
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "store pending payment: %v", err)
	}
	if _, err = s.EnqueueBillingEventTx(ctx, dbTx, eventPaymentCreated, ctxTenantID, userID, "payment", paymentID, &ipcpb.BillingEvent{
		PaymentId: paymentID,
		InvoiceId: invoiceID,
		Amount:    decimalFloat(balance.AmountDue),
		Currency:  balance.Currency,
		Provider:  method,
		Status:    "pending",
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "enqueue payment_created: %v", err)
	}
	if err = dbTx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit payment transaction: %v", err)
	}

	if method == "card" {
		paymentURL, providerID, checkoutErr := s.createCardInvoiceCheckout(ctx, paymentID, invoiceID, ctxTenantID, balance.AmountDue, balance.Currency, req.GetReturnUrl())
		if checkoutErr != nil {
			if markErr := s.markPaymentFailed(ctx, paymentID); markErr != nil {
				s.logger.WithError(markErr).WithField("payment_id", paymentID).Warn("Failed to release card payment reservation")
			}
			return nil, status.Errorf(codes.Internal, "create card checkout: %v", checkoutErr)
		}
		if err = s.attachCardCheckoutToPayment(ctx, paymentID, providerID, paymentURL); err != nil {
			return nil, status.Errorf(codes.Internal, "attach card checkout: %v", err)
		}
		resp.PaymentUrl = paymentURL
		resp.ExpiresAt = timestamppb.New(time.Now().Add(24 * time.Hour))
	}

	s.logger.WithFields(logging.Fields{
		"payment_id": paymentID,
		"invoice_id": invoiceID,
		"tenant_id":  ctxTenantID,
		"method":     method,
		"amount":     balance.AmountDue.StringFixed(2),
	}).Info("Payment created successfully via gRPC")

	return resp, nil
}

// getAvailablePaymentMethods returns list of configured payment methods
func (s *PurserServer) getAvailablePaymentMethods(ctx context.Context) []string {
	methods := []string{}

	if _, err := configuredInvoiceCardProvider(); err == nil {
		methods = append(methods, "card")
	}

	if s.cryptoDepositReadiness(ctx) == nil {
		methods = append(methods, "crypto_eth", "crypto_usdc")
	}

	return methods
}

func (s *PurserServer) hasHDWalletXpub(ctx context.Context) bool {
	xpub, err := purserdb.New(s.db).GetHDWalletXpub(ctx)
	if err != nil {
		s.logger.WithError(err).Debug("Failed to read hd_wallet_state")
		return false
	}
	return strings.TrimSpace(xpub) != ""
}

func (s *PurserServer) cryptoDepositReadiness(ctx context.Context) error {
	if !config.CryptoDepositsEnabled() {
		return fmt.Errorf("new crypto deposits are temporarily disabled")
	}
	if !s.hasHDWalletXpub(ctx) {
		return fmt.Errorf("HD wallet xpub is not initialized")
	}
	if config.IsProduction() {
		if strings.TrimSpace(os.Getenv("SUPPLIER_NAME")) == "" ||
			strings.TrimSpace(os.Getenv("SUPPLIER_ADDRESS")) == "" ||
			strings.TrimSpace(os.Getenv("SUPPLIER_VAT_NUMBER")) == "" ||
			strings.TrimSpace(os.Getenv("SUPPLIER_REGISTRATION_NUMBER")) == "" ||
			len(countries.Normalize(os.Getenv("SUPPLIER_COUNTRY"))) != 2 {
			return fmt.Errorf("complete supplier invoice configuration is required")
		}
	}
	for _, network := range handlers.DepositNetworks(config.X402IncludeTestnetsEnabled()) {
		if network.GetRPCEndpointWithDefault() == "" {
			return fmt.Errorf("%s is required", network.RPCEndpointEnv)
		}
		if err := handlers.ValidateCryptoScannerStart(network.Name); err != nil {
			return err
		}
		if config.IsProduction() {
			for _, asset := range []string{"ETH", "USDC"} {
				if err := handlers.ValidateCryptoCustodyNetwork(ctx, s.rpcClient, network, asset); err != nil {
					return err
				}
			}
			readiness, err := purserdb.New(s.db).GetCryptoScannerReadiness(ctx, network.Name)
			if err != nil {
				return fmt.Errorf("crypto scanner for %s has no readiness state: %w", network.Name, err)
			}
			if !readiness.ScannedAt.Valid || time.Since(readiness.ScannedAt.Time) > time.Minute {
				return fmt.Errorf("crypto scanner for %s has not committed a batch in the last minute", network.Name)
			}
			if readiness.LastError.Valid && strings.TrimSpace(readiness.LastError.String) != "" {
				return fmt.Errorf("crypto scanner for %s is unhealthy: %s", network.Name, readiness.LastError.String)
			}
			if readiness.LagBlocks.Valid && readiness.LagBlocks.Int64 > 500 {
				return fmt.Errorf("crypto scanner for %s is %d blocks behind", network.Name, readiness.LagBlocks.Int64)
			}
		}
	}
	return nil
}

func configuredInvoiceCardProvider() (handlers.CheckoutProvider, error) {
	webappReady := strings.TrimSpace(os.Getenv("WEBAPP_PUBLIC_URL")) != ""
	stripeReady := webappReady && strings.TrimSpace(os.Getenv("STRIPE_SECRET_KEY")) != "" && strings.TrimSpace(os.Getenv("STRIPE_WEBHOOK_SECRET")) != ""
	mollieReady := webappReady && strings.TrimSpace(os.Getenv("GATEWAY_PUBLIC_URL")) != "" && strings.TrimSpace(os.Getenv("MOLLIE_API_KEY")) != ""

	switch strings.ToLower(strings.TrimSpace(os.Getenv("PAYMENT_CARD_PROVIDER"))) {
	case "stripe":
		if !stripeReady {
			return "", fmt.Errorf("stripe invoice payments require STRIPE_SECRET_KEY, STRIPE_WEBHOOK_SECRET, and WEBAPP_PUBLIC_URL")
		}
		return handlers.ProviderStripe, nil
	case "mollie":
		if !mollieReady {
			return "", fmt.Errorf("mollie invoice payments require MOLLIE_API_KEY, GATEWAY_PUBLIC_URL, and WEBAPP_PUBLIC_URL")
		}
		return handlers.ProviderMollie, nil
	case "":
	default:
		return "", fmt.Errorf("PAYMENT_CARD_PROVIDER must be stripe or mollie")
	}

	if stripeReady == mollieReady {
		if stripeReady {
			return "", fmt.Errorf("PAYMENT_CARD_PROVIDER is required when Stripe and Mollie are both configured")
		}
		return "", fmt.Errorf("no card payment provider is fully configured")
	}
	if stripeReady {
		return handlers.ProviderStripe, nil
	}
	return handlers.ProviderMollie, nil
}

func (s *PurserServer) createCardInvoiceCheckout(ctx context.Context, paymentID, invoiceID, tenantID string, amount decimal.Decimal, currency, requestedReturnURL string) (string, string, error) {
	provider, err := configuredInvoiceCardProvider()
	if err != nil {
		return "", "", err
	}
	amountCents, err := invoiceAmountMinorUnits(amount, currency)
	if err != nil {
		return "", "", err
	}
	if amountCents <= 0 {
		return "", "", fmt.Errorf("payment amount must be positive")
	}

	successURL, cancelURL, err := invoicePaymentReturnURLs(requestedReturnURL)
	if err != nil {
		return "", "", err
	}
	checkoutSvc := handlers.NewCheckoutService(s.db, s.logger)
	result, err := checkoutSvc.CreateCheckout(ctx, handlers.CheckoutRequest{
		Purpose:        handlers.PurposeInvoice,
		Provider:       provider,
		TenantID:       tenantID,
		ReferenceID:    invoiceID,
		AmountCents:    amountCents,
		Currency:       strings.ToUpper(currency),
		SuccessURL:     successURL,
		CancelURL:      cancelURL,
		Description:    fmt.Sprintf("Invoice %s", invoiceID),
		IdempotencyKey: "invoice-payment-" + paymentID,
	})
	if err != nil {
		return "", "", err
	}
	if result.CheckoutURL == "" || result.SessionID == "" {
		return "", "", fmt.Errorf("provider checkout response missing URL or ID")
	}
	return result.CheckoutURL, result.SessionID, nil
}

func invoiceAmountMinorUnits(amount decimal.Decimal, currency string) (int64, error) {
	exponent := handlers.CurrencyMinorUnitExponent(currency)
	if exponent > 2 {
		return 0, fmt.Errorf("currency %s has %d minor units, but Purser invoice amounts support at most 2", strings.ToUpper(currency), exponent)
	}
	return amount.Round(int32(exponent)).Shift(int32(exponent)).IntPart(), nil
}

func invoicePaymentReturnURLs(requestedReturnURL string) (string, string, error) {
	webappBase := strings.TrimSpace(os.Getenv("WEBAPP_PUBLIC_URL"))
	if webappBase == "" {
		return "", "", fmt.Errorf("WEBAPP_PUBLIC_URL is required")
	}
	webappURL, err := url.Parse(webappBase)
	if err != nil || webappURL.Scheme == "" || webappURL.Host == "" {
		return "", "", fmt.Errorf("WEBAPP_PUBLIC_URL must be an absolute URL")
	}

	baseURL := strings.TrimRight(webappURL.String(), "/") + "/account/billing"
	trimmedReturnURL := strings.TrimSpace(requestedReturnURL)
	if trimmedReturnURL != "" {
		requestedURL, parseErr := url.Parse(trimmedReturnURL)
		if parseErr != nil {
			return "", "", fmt.Errorf("invalid return_url: %w", parseErr)
		}
		if requestedURL.IsAbs() {
			if requestedURL.Scheme != webappURL.Scheme || requestedURL.Host != webappURL.Host {
				return "", "", fmt.Errorf("return_url must use WEBAPP_PUBLIC_URL origin")
			}
			baseURL = requestedURL.String()
		} else if strings.HasPrefix(trimmedReturnURL, "/") {
			baseURL = webappURL.ResolveReference(requestedURL).String()
		} else {
			return "", "", fmt.Errorf("return_url must be absolute or root-relative")
		}
	}
	successURL, err := withPaymentQuery(baseURL, "success")
	if err != nil {
		return "", "", err
	}
	cancelURL, err := withPaymentQuery(baseURL, "cancelled")
	if err != nil {
		return "", "", err
	}
	return successURL, cancelURL, nil
}

func withPaymentQuery(rawURL, status string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid return_url: %w", err)
	}
	q := u.Query()
	q.Set("payment", status)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (s *PurserServer) attachCardCheckoutToPayment(ctx context.Context, paymentID, providerID, paymentURL string) error {
	rows, err := purserdb.New(s.db).AttachCardCheckoutToPendingPayment(ctx, purserdb.AttachCardCheckoutToPendingPaymentParams{
		ProviderID: sql.NullString{String: providerID, Valid: true},
		PaymentUrl: sql.NullString{String: paymentURL, Valid: true},
		PaymentID:  paymentID,
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("pending payment %s not found", paymentID)
	}
	return nil
}

func (s *PurserServer) markPaymentFailed(ctx context.Context, paymentID string) error {
	_, err := purserdb.New(s.db).MarkPendingInvoicePaymentFailed(ctx, paymentID)
	return err
}

func lockInvoicePaymentTx(ctx context.Context, tx *sql.Tx, invoiceID string) error {
	return purserdb.New(tx).LockInvoicePaymentCreation(ctx, invoiceID)
}

type invoiceBalance struct {
	InvoiceID string
	TenantID  string
	Currency  string
	AmountDue decimal.Decimal
}

func loadInvoiceBalanceTx(ctx context.Context, tx *sql.Tx, invoiceID, tenantID string) (*invoiceBalance, error) {
	row, err := purserdb.New(tx).GetPayableInvoiceBalance(ctx, purserdb.GetPayableInvoiceBalanceParams{
		InvoiceID: invoiceID,
		TenantID:  tenantID,
	})
	if err != nil {
		return nil, err
	}
	total, err := decimal.NewFromString(row.TotalAmount)
	if err != nil {
		return nil, fmt.Errorf("parse invoice total: %w", err)
	}
	netPaid, err := decimal.NewFromString(row.NetPaid)
	if err != nil {
		return nil, fmt.Errorf("parse confirmed payment total: %w", err)
	}
	amountDue := total.Sub(netPaid).Round(2)
	if amountDue.IsNegative() {
		amountDue = decimal.Zero
	}
	return &invoiceBalance{
		InvoiceID: invoiceID,
		TenantID:  row.TenantID,
		Currency:  row.Currency,
		AmountDue: amountDue,
	}, nil
}

func decimalFloat(value decimal.Decimal) float64 {
	result, _ := value.Float64()
	return result
}

type activeInvoicePayment struct {
	ID          string
	Method      string
	Amount      decimal.Decimal
	Currency    string
	TxID        sql.NullString
	URL         sql.NullString
	CreatedAt   time.Time
	ActiveCount int
}

func loadActiveInvoicePaymentTx(ctx context.Context, tx *sql.Tx, invoiceID, tenantID string) (*activeInvoicePayment, error) {
	row, err := purserdb.New(tx).GetActiveInvoicePayment(ctx, purserdb.GetActiveInvoicePaymentParams{
		InvoiceID: invoiceID,
		TenantID:  tenantID,
	})
	if err != nil {
		return nil, err
	}
	amount, err := decimal.NewFromString(row.Amount)
	if err != nil {
		return nil, fmt.Errorf("parse pending payment amount: %w", err)
	}
	return &activeInvoicePayment{
		ID: row.ID, Method: row.Method, Amount: amount, Currency: row.Currency,
		TxID: row.TxID, URL: row.PaymentUrl, CreatedAt: row.CreatedAt.Time,
		ActiveCount: int(row.ActiveCount),
	}, nil
}

func (s *PurserServer) resumeInvoicePayment(ctx context.Context, req *purserpb.PaymentRequest, balance *invoiceBalance, payment *activeInvoicePayment) (*purserpb.PaymentResponse, error) {
	resp := &purserpb.PaymentResponse{
		Id:        payment.ID,
		Amount:    decimalFloat(payment.Amount),
		Currency:  payment.Currency,
		Status:    "pending",
		Method:    payment.Method,
		CreatedAt: timestamppb.New(payment.CreatedAt),
	}
	if asset, ok := strings.CutPrefix(payment.Method, "crypto_"); ok {
		details, err := s.loadInvoiceCryptoPaymentQuote(ctx, balance.InvoiceID, balance.TenantID, strings.ToUpper(asset))
		if err != nil {
			return nil, status.Errorf(codes.Internal, "load crypto payment quote: %v", err)
		}
		if !payment.TxID.Valid || details.WalletAddress != payment.TxID.String {
			return nil, status.Error(codes.Internal, "pending crypto payment wallet mismatch")
		}
		details.apply(resp)
		return resp, nil
	}
	if payment.Method != "card" {
		return nil, status.Errorf(codes.InvalidArgument, "unsupported payment method: %s", payment.Method)
	}
	if payment.URL.Valid && strings.TrimSpace(payment.URL.String) != "" {
		resp.PaymentUrl = payment.URL.String
		resp.ExpiresAt = timestamppb.New(payment.CreatedAt.Add(24 * time.Hour))
		return resp, nil
	}
	paymentURL, providerID, err := s.createCardInvoiceCheckout(ctx, payment.ID, balance.InvoiceID, balance.TenantID, payment.Amount, payment.Currency, req.GetReturnUrl())
	if err != nil {
		if markErr := s.markPaymentFailed(ctx, payment.ID); markErr != nil {
			s.logger.WithError(markErr).WithField("payment_id", payment.ID).Warn("Failed to release card payment reservation")
		}
		return nil, status.Errorf(codes.Internal, "create card checkout: %v", err)
	}
	if err = s.attachCardCheckoutToPayment(ctx, payment.ID, providerID, paymentURL); err != nil {
		return nil, status.Errorf(codes.Internal, "attach card checkout: %v", err)
	}
	resp.PaymentUrl = paymentURL
	resp.ExpiresAt = timestamppb.New(time.Now().Add(24 * time.Hour))
	return resp, nil
}

func (s *PurserServer) expireStaleInvoicePayments(ctx context.Context, invoiceID, tenantID string) error {
	for _, payment := range []struct {
		asset  string
		method string
	}{
		{asset: "ETH", method: "crypto_eth"},
		{asset: "USDC", method: "crypto_usdc"},
	} {
		if err := s.expireStaleInvoiceCryptoPayments(ctx, invoiceID, tenantID, payment.asset, payment.method); err != nil {
			return err
		}
	}
	return purserdb.New(s.db).ExpireStaleCardInvoicePayments(ctx, purserdb.ExpireStaleCardInvoicePaymentsParams{
		InvoiceID: invoiceID,
		TenantID:  tenantID,
	})
}

func (s *PurserServer) expireStaleInvoiceCryptoPayments(ctx context.Context, invoiceID, tenantID, asset, method string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin expiration transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
				s.logger.WithError(rollbackErr).Warn("Failed to roll back payment expiration transaction")
			}
		}
	}()

	queries := purserdb.New(tx)
	if err = queries.FailExpiredCryptoInvoicePayments(ctx, purserdb.FailExpiredCryptoInvoicePaymentsParams{
		Method: method, InvoiceID: invoiceID, TenantID: tenantID, Asset: asset,
	}); err != nil {
		return fmt.Errorf("expire payment rows: %w", err)
	}
	if err = queries.ExpireCryptoInvoiceWallets(ctx, purserdb.ExpireCryptoInvoiceWalletsParams{
		InvoiceID: invoiceID, TenantID: tenantID, Asset: asset,
	}); err != nil {
		return fmt.Errorf("expire wallet rows: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit expiration transaction: %w", err)
	}
	committed = true
	return nil
}

// prepareCryptoPayment locks the invoice's token quote before any database writes.
func (s *PurserServer) prepareCryptoPayment(ctx context.Context, invoiceID, tenantID, asset string, amount decimal.Decimal, currency string, expiresAt time.Time) (*cryptoPaymentPlan, error) {
	switch asset {
	case "ETH", "USDC":
	default:
		return nil, fmt.Errorf("unsupported crypto asset for invoice payment: %s", asset)
	}

	normalizedCurrency := strings.ToUpper(currency)
	if normalizedCurrency != "USD" && normalizedCurrency != "EUR" {
		return nil, fmt.Errorf("unsupported invoice currency for crypto payment: %s", normalizedCurrency)
	}
	amountCents := amount.Mul(decimal.NewFromInt(100)).Round(0).IntPart()
	if amountCents <= 0 {
		return nil, fmt.Errorf("invoice amount must be positive")
	}

	networkName := defaultNetworkForAsset(asset)
	if networkName == "" {
		return nil, fmt.Errorf("no default network for asset %s", asset)
	}
	network, ok := handlers.Networks[networkName]
	if !ok {
		return nil, fmt.Errorf("unknown network %q", networkName)
	}
	tokenDecimals, ok := handlers.TokenDecimals(asset)
	if !ok {
		return nil, fmt.Errorf("unknown token decimals for %s", asset)
	}
	quote, expectedBaseUnits, err := s.buildDepositQuote(ctx, network, asset, normalizedCurrency, amountCents, tokenDecimals)
	if err != nil {
		return nil, err
	}

	return &cryptoPaymentPlan{
		Params: handlers.DepositAddressParams{
			TenantID:  tenantID,
			Purpose:   "invoice",
			Asset:     asset,
			Network:   networkName,
			ExpiresAt: expiresAt,
			InvoiceID: &invoiceID,
			Quote:     quote,
		},
		ExpectedBaseUnits: expectedBaseUnits,
		TokenDecimals:     tokenDecimals,
		NetworkName:       networkName,
		Asset:             asset,
	}, nil
}

// createCryptoPaymentTx stores the wallet row inside the payment transaction.
func (s *PurserServer) createCryptoPaymentTx(ctx context.Context, dbTx *sql.Tx, plan *cryptoPaymentPlan) (*cryptoPaymentQuoteDetails, error) {
	walletID, walletAddress, err := s.hdwallet.GenerateDepositAddressTx(ctx, dbTx, plan.Params)
	if err != nil {
		return nil, fmt.Errorf("failed to generate deposit address: %w", err)
	}

	expectedAmountToken := decimal.NewFromBigInt(plan.ExpectedBaseUnits, -plan.TokenDecimals).StringFixedBank(plan.TokenDecimals)
	invoiceID := ""
	if plan.Params.InvoiceID != nil {
		invoiceID = *plan.Params.InvoiceID
	}

	s.logger.WithFields(map[string]any{
		"wallet_id":   walletID,
		"invoice_id":  invoiceID,
		"tenant_id":   plan.Params.TenantID,
		"asset":       plan.Asset,
		"network":     plan.NetworkName,
		"address":     walletAddress,
		"quote_units": plan.ExpectedBaseUnits.String(),
	}).Info("Created crypto payment address for invoice")

	return &cryptoPaymentQuoteDetails{
		WalletAddress:           walletAddress,
		ExpectedAmountBaseUnits: plan.ExpectedBaseUnits.String(),
		ExpectedAmountToken:     expectedAmountToken,
		QuotedPriceUSD:          plan.Params.Quote.QuotedPriceUSD.String(),
		QuoteSource:             plan.Params.Quote.QuoteSource,
		AssetSymbol:             plan.Asset,
		Network:                 plan.NetworkName,
		QuotedAt:                plan.Params.Quote.QuotedAt,
		ExpiresAt:               plan.Params.ExpiresAt,
	}, nil
}

func (s *PurserServer) loadInvoiceCryptoPaymentQuote(ctx context.Context, invoiceID, tenantID, asset string) (*cryptoPaymentQuoteDetails, error) {
	row, err := purserdb.New(s.db).GetActiveInvoiceCryptoPaymentQuote(ctx, purserdb.GetActiveInvoiceCryptoPaymentQuoteParams{
		InvoiceID: invoiceID, TenantID: tenantID, Asset: asset,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("pending crypto wallet not found")
	}
	if err != nil {
		return nil, err
	}
	details := &cryptoPaymentQuoteDetails{
		WalletAddress: row.WalletAddress, ExpectedAmountBaseUnits: row.ExpectedAmountBaseUnits,
		QuotedPriceUSD: row.QuotedPriceUsd, QuoteSource: row.QuoteSource.String,
		AssetSymbol: row.Asset, Network: row.Network, QuotedAt: row.QuotedAt.Time,
		ExpiresAt: row.ExpiresAt,
	}
	tokenDecimals, ok := handlers.TokenDecimals(details.AssetSymbol)
	if !ok {
		return nil, fmt.Errorf("unknown token decimals for %s", details.AssetSymbol)
	}
	baseUnits, ok := new(big.Int).SetString(details.ExpectedAmountBaseUnits, 10)
	if !ok || baseUnits.Sign() <= 0 {
		return nil, fmt.Errorf("invalid expected_amount_base_units")
	}
	details.ExpectedAmountToken = decimal.NewFromBigInt(baseUnits, -tokenDecimals).StringFixedBank(tokenDecimals)
	return details, nil
}

// GetPaymentMethods returns available payment methods for a tenant
func (s *PurserServer) GetPaymentMethods(ctx context.Context, req *purserpb.GetPaymentMethodsRequest) (*purserpb.PaymentMethodResponse, error) {
	// Return available payment methods based on configured env vars
	return &purserpb.PaymentMethodResponse{
		Methods: s.getAvailablePaymentMethods(ctx),
	}, nil
}

// GetBillingStatus returns subscription, tier, pending invoices, recent
// payments, and available payment methods for a tenant.
func (s *PurserServer) GetBillingStatus(ctx context.Context, req *purserpb.GetBillingStatusRequest) (*purserpb.BillingStatusResponse, error) {
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
		return nil, status.Errorf(codes.Internal, "failed to get pending invoices: %v", err)
	}

	// Get recent payments
	recentPayments, err := s.getRecentPayments(ctx, tenantID, 5)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get recent payments: %v", err)
	}

	// Calculate outstanding amount
	var outstanding float64
	for _, inv := range pendingInvoices {
		outstanding += inv.Amount
	}

	billingStatus := subscription.GetStatus()
	if billingStatus == "" {
		billingStatus = "none"
	}
	currency := billing.DefaultCurrency()
	if tier.GetCurrency() != "" {
		currency = tier.GetCurrency()
	}

	// Build response
	resp := &purserpb.BillingStatusResponse{
		TenantId:          tenantID,
		Subscription:      subscription,
		Tier:              tier,
		BillingStatus:     billingStatus,
		OutstandingAmount: outstanding,
		Currency:          currency,
		PendingInvoices:   pendingInvoices,
		RecentPayments:    recentPayments,
		PaymentMethods:    s.getAvailablePaymentMethods(ctx),
		SetupProviders:    s.getAvailablePostpaidProviders(),
	}
	if subscription.GetPaymentMethod() == "stripe" && subscription.GetStripeSubscriptionId() != "" {
		resp.CollectionReady = true
		resp.CollectionProvider = "stripe"
	} else if subscription.GetPaymentMethod() == "mollie" && subscription.GetMollieSubscriptionId() != "" {
		resp.CollectionReady = true
		resp.CollectionProvider = "mollie"
	}

	if subscription.GetNextBillingDate() != nil {
		resp.NextBillingDate = subscription.GetNextBillingDate()
	}

	return resp, nil
}

func (s *PurserServer) getAvailablePostpaidProviders() []string {
	providers := make([]string, 0, 2)
	webappReady := strings.TrimSpace(os.Getenv("WEBAPP_PUBLIC_URL")) != ""
	if s.stripeClient != nil && webappReady && strings.TrimSpace(os.Getenv("STRIPE_SECRET_KEY")) != "" && strings.TrimSpace(os.Getenv("STRIPE_WEBHOOK_SECRET")) != "" {
		providers = append(providers, "stripe")
	}
	if s.mollieClient != nil && webappReady && strings.TrimSpace(os.Getenv("GATEWAY_PUBLIC_URL")) != "" && strings.TrimSpace(os.Getenv("MOLLIE_API_KEY")) != "" {
		providers = append(providers, "mollie")
	}
	return providers
}

// getSubscriptionAndTier fetches full subscription and tier details for a tenant
func (s *PurserServer) getSubscriptionAndTier(ctx context.Context, tenantID string) (*purserpb.TenantSubscription, *purserpb.BillingTier, error) {
	s.logger.WithField("tenant_id", tenantID).Info("getSubscriptionAndTier: querying subscription for tenant")

	queries := purserdb.New(s.db)
	subscriptionRow, err := queries.GetCurrentTenantSubscription(ctx, tenantID)

	if errors.Is(err, sql.ErrNoRows) {
		s.logger.WithField("tenant_id", tenantID).Warn("getSubscriptionAndTier: no active subscription")
		return nil, nil, nil
	}
	if err != nil {
		s.logger.WithError(err).WithField("tenant_id", tenantID).Error("getSubscriptionAndTier: query error")
		return nil, nil, err
	}
	subscription := tenantSubscriptionFromCurrentRow(subscriptionRow)
	tierRow, err := queries.GetBillingTierByID(ctx, subscription.TierId)
	if err != nil {
		return nil, nil, fmt.Errorf("load billing tier %s: %w", subscription.TierId, err)
	}
	tier := billingTierFromGetRow(tierRow)

	s.logger.WithFields(map[string]any{
		"tenant_id":    tenantID,
		"tier_name":    tier.TierName,
		"display_name": tier.DisplayName,
		"base_price":   tier.BasePrice,
		"status":       subscription.Status,
	}).Info("getSubscriptionAndTier: FOUND subscription")

	// Load per-tenant pricing/entitlement overrides. Errors surface — a
	// missing/broken override table must fail loudly, not silently return a
	// subscription with empty overrides.
	pricingOverrides, poErr := loadSubscriptionPricingOverrides(ctx, s.db, subscription.Id)
	if poErr != nil {
		return nil, nil, fmt.Errorf("load subscription pricing overrides: %w", poErr)
	}
	subscription.PricingOverrides = pricingOverrides
	entOverrides, eoErr := loadSubscriptionEntitlementOverrides(ctx, s.db, subscription.Id)
	if eoErr != nil {
		return nil, nil, fmt.Errorf("load subscription entitlement overrides: %w", eoErr)
	}
	subscription.EntitlementOverrides = entOverrides

	// Load normalized tier children. Errors are surfaced — a missing or broken
	// normalized table must not return a tier with silently empty pricing.
	rules, rulesErr := loadTierPricingRules(ctx, s.db, tier.Id)
	if rulesErr != nil {
		return nil, nil, fmt.Errorf("load pricing rules for tier %s: %w", tier.Id, rulesErr)
	}
	tier.PricingRules = rules
	ents, entsErr := loadTierEntitlements(ctx, s.db, tier.Id)
	if entsErr != nil {
		return nil, nil, fmt.Errorf("load entitlements for tier %s: %w", tier.Id, entsErr)
	}
	tier.Entitlements = ents
	return subscription, tier, nil
}

// getPendingInvoices fetches invoices that can still be paid by the tenant.
func (s *PurserServer) getPendingInvoices(ctx context.Context, tenantID string) ([]*purserpb.Invoice, error) {
	rows, err := purserdb.New(s.db).ListPayableInvoices(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	invoices := make([]*purserpb.Invoice, 0, len(rows))
	for _, row := range rows {
		inv := &purserpb.Invoice{
			Id: row.ID.String(), TenantId: row.TenantID.String(), Amount: row.AmountDue,
			BaseAmount: row.BaseAmount, MeteredAmount: row.MeteredAmount,
			PrepaidCreditApplied: row.PrepaidCreditApplied, GrossMeteredAmount: row.GrossMeteredAmount,
			Currency: row.Currency, Status: row.Status, DueDate: timestamppb.New(row.DueDate),
			CreatedAt: timestamppb.New(row.CreatedAt.Time), UpdatedAt: timestamppb.New(row.UpdatedAt.Time),
		}
		if row.PaidAt.Valid {
			inv.PaidAt = timestamppb.New(row.PaidAt.Time)
		}
		if row.PeriodStart.Valid {
			inv.PeriodStart = timestamppb.New(row.PeriodStart.Time)
		}
		if row.PeriodEnd.Valid {
			inv.PeriodEnd = timestamppb.New(row.PeriodEnd.Time)
		}
		if len(row.UsageDetails) > 0 {
			var details map[string]any
			if json.Unmarshal(row.UsageDetails, &details) == nil {
				inv.UsageDetails = mapToProtoStruct(details)
			}
		}
		lineItems, lineErr := s.loadInvoiceLineItems(ctx, inv.GetId(), inv.GetTenantId())
		if lineErr != nil {
			return nil, lineErr
		}
		inv.LineItems = lineItems
		invoices = append(invoices, inv)
	}

	return invoices, nil
}

// getRecentPayments fetches recent payments for a tenant
func (s *PurserServer) getRecentPayments(ctx context.Context, tenantID string, limit int) ([]*purserpb.Payment, error) {
	rows, err := purserdb.New(s.db).ListRecentPayments(ctx, purserdb.ListRecentPaymentsParams{
		TenantID: tenantID, RowLimit: int32(limit),
	})
	if err != nil {
		return nil, err
	}
	payments := make([]*purserpb.Payment, 0, len(rows))
	for _, row := range rows {
		pay := &purserpb.Payment{
			Id: row.ID.String(), InvoiceId: row.InvoiceID.String(), Method: row.Method,
			Amount: row.Amount, Currency: row.Currency, Status: row.Status,
			CreatedAt: timestamppb.New(row.CreatedAt.Time), UpdatedAt: timestamppb.New(row.UpdatedAt.Time),
		}
		if row.TxID.Valid {
			pay.TxId = row.TxID.String
		}
		if row.ConfirmedAt.Valid {
			pay.ConfirmedAt = timestamppb.New(row.ConfirmedAt.Time)
		}
		payments = append(payments, pay)
	}

	return payments, nil
}

func (s *PurserServer) getSubscriptionPeriod(ctx context.Context, tenantID string, now time.Time) (time.Time, time.Time) {
	period, err := purserdb.New(s.db).GetActiveSubscriptionPeriod(ctx, tenantID)
	if err == nil && period.BillingPeriodStart.Valid && period.BillingPeriodEnd.Valid && period.BillingPeriodEnd.Time.After(period.BillingPeriodStart.Time) {
		return period.BillingPeriodStart.Time, period.BillingPeriodEnd.Time
	}

	utcNow := now.UTC()
	periodStart := time.Date(utcNow.Year(), utcNow.Month(), 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.AddDate(0, 1, 0)
	return periodStart, periodEnd
}

// scanBillingAddress scans JSONB into BillingAddress proto
func scanBillingAddress(data []byte) *purserpb.BillingAddress {
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
	return &purserpb.BillingAddress{
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
func (s *PurserServer) GetTenantUsage(ctx context.Context, req *purserpb.TenantUsageRequest) (*purserpb.TenantUsageResponse, error) {
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
	parsedStartDate, err := time.Parse("2006-01-02", startDate)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "start_date must use YYYY-MM-DD")
	}
	parsedEndDate, err := time.Parse("2006-01-02", endDate)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "end_date must use YYYY-MM-DD")
	}

	// Aggregate canonical-meter deltas by cluster and usage_type. Cost
	// previews must use the same cluster-aware pricing resolver as invoice
	// finalization, otherwise marketplace/custom cluster pricing would
	// preview one amount and invoice another.
	queries := purserdb.New(s.db)
	rows, err := queries.ListTenantUsageTotals(ctx, purserdb.ListTenantUsageTotalsParams{
		TenantID: tenantID, StartDate: parsedStartDate, EndDate: parsedEndDate,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	usage := make(map[string]float64)
	perClusterUsage := map[string]map[string]float64{}
	for _, row := range rows {
		usage[row.UsageType] += row.Total
		if perClusterUsage[row.ClusterID] == nil {
			perClusterUsage[row.ClusterID] = map[string]float64{}
		}
		perClusterUsage[row.ClusterID][row.UsageType] = row.Total
	}

	perClusterDimensioned := map[string][]rating.DimensionedQuantity{}
	dimensionRows, dimensionErr := queries.ListTenantDimensionedUsage(ctx, purserdb.ListTenantDimensionedUsageParams{
		TenantID: tenantID, StartDate: parsedStartDate, EndDate: parsedEndDate,
	})
	if dimensionErr != nil {
		return nil, status.Errorf(codes.Internal, "query dimensioned usage: %v", dimensionErr)
	}
	for _, row := range dimensionRows {
		dimensions := map[string]string{}
		if len(row.Dimensions) > 0 {
			if decodeErr := json.Unmarshal(row.Dimensions, &dimensions); decodeErr != nil {
				return nil, status.Errorf(codes.Internal, "decode dimensions for %s: %v", row.UsageType, decodeErr)
			}
		}
		quantity, parseErr := decimal.NewFromString(row.Quantity)
		if parseErr != nil {
			return nil, status.Errorf(codes.Internal, "parse dimensioned quantity for %s: %v", row.UsageType, parseErr)
		}
		perClusterDimensioned[row.ClusterID] = append(perClusterDimensioned[row.ClusterID], rating.DimensionedQuantity{
			Meter: rating.Meter(row.UsageType), Unit: row.Unit, Dimensions: dimensions, Quantity: quantity,
		})
	}

	// Rate via the engine. Same path as monthly invoice / draft / prepaid;
	// this is the only place that turns metered usage into cost. Errors
	// surface — a broken effective-tier load or rating call must NOT silently
	// return a zero-cost response that would mask billing breakage.
	tier, err := billingpkg.LoadEffectiveTier(ctx, s.db, tenantID)
	currency := billing.DefaultCurrency()
	resp := &purserpb.TenantUsageResponse{
		TenantId:      tenantID,
		BillingPeriod: startDate + " to " + endDate,
		Usage:         usage,
		Costs:         map[string]float64{},
		Currency:      currency,
	}
	if errors.Is(err, sql.ErrNoRows) {
		// No subscription is a legitimate steady state, not a billing failure.
		return resp, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load effective tier: %v", err)
	}
	resp.Currency = tier.Currency

	if !tier.MeteringEnabled {
		return resp, nil
	}

	// Preview line_items are metered only — base subscription is a fixed
	// monthly fee separately surfaced via BaseAmount, not a per-period charge.
	usageAmount := decimal.Zero
	asOf := time.Now()
	if parsedStart, parseErr := time.Parse("2006-01-02", startDate); parseErr == nil {
		asOf = parsedStart
	}
	periodSuffix := asOf.Format("200601")
	clusterIDs := make([]string, 0, len(perClusterUsage))
	for clusterID := range perClusterUsage {
		clusterIDs = append(clusterIDs, clusterID)
	}
	slices.Sort(clusterIDs)
	for _, clusterID := range clusterIDs {
		clusterUsage := perClusterUsage[clusterID]
		var (
			resolved   *pricing.ClusterPricing
			resolveErr error
		)
		if clusterID == "" {
			resolved = &pricing.ClusterPricing{
				Model:              pricing.ModelTierInherit,
				Kind:               pricing.KindPlatformOfficial,
				Currency:           tier.Currency,
				MeteredRules:       tier.Rules,
				PricingSource:      pricing.SourceTier,
				IsPlatformOfficial: true,
			}
		} else {
			resolved, resolveErr = pricing.ResolveClusterPricing(ctx, pricing.ResolveInputs{
				DB:                s.db,
				QM:                s.quartermasterClient,
				ConsumingTenantID: tenantID,
				ClusterID:         clusterID,
				AsOf:              asOf,
				TierRules:         tier.Rules,
				TierCurrency:      tier.Currency,
			})
			if resolveErr != nil {
				return nil, status.Errorf(codes.FailedPrecondition, "resolve cluster pricing for %s: %v", clusterID, resolveErr)
			}
		}
		if resolved.Currency != resp.Currency {
			return nil, status.Errorf(codes.FailedPrecondition, "cluster %s prices in %s but response currency is %s", clusterID, resolved.Currency, resp.Currency)
		}
		in := buildRatingInputForUsage(clusterUsage, perClusterDimensioned[clusterID], resolved.Currency, decimal.Zero, resolved.MeteredRules)
		res, rateErr := rating.Rate(in)
		if rateErr != nil {
			return nil, status.Errorf(codes.Internal, "rate usage for cluster %s: %v", clusterID, rateErr)
		}
		for _, line := range res.UsageLines {
			suffixed := line
			if clusterID != "" {
				suffixed.LineKey = clusterScopedLineKey(line.LineKey, clusterID, periodSuffix)
			}
			protoLine := lineItemToProto(suffixed)
			clusterKind := ""
			if clusterID != "" {
				clusterKind = string(resolved.Kind)
				protoLine.ClusterId = clusterID
				protoLine.ClusterKind = clusterKind
				protoLine.PricingSource = string(resolved.PricingSource)
				protoLine.PricingLabel = pricingLabelFor(protoLine.PricingSource, clusterKind)
			}
			// Usage that rated to a real amount but was waived to €0 is stamped
			// beta_free regardless of attribution, so platform/unattributed lines
			// also surface the beta label and the preview matches the invoice.
			if suffixed.GrossAmount.IsPositive() && suffixed.Amount.IsZero() {
				protoLine.PricingSource = string(pricing.SourceBetaFree)
				protoLine.PricingLabel = pricingLabelFor(protoLine.PricingSource, clusterKind)
			}
			resp.LineItems = append(resp.LineItems, protoLine)
			amount, _ := suffixed.Amount.Float64()
			if suffixed.Meter != "" {
				resp.Costs[string(suffixed.Meter)] += amount
			}
			usageAmount = usageAmount.Add(suffixed.Amount)
		}
	}
	enrichLineItemClusterNames(ctx, s, resp.LineItems)
	resp.BaseAmount = tier.BasePrice.String()
	resp.UsageAmount = usageAmount.String()
	resp.TotalCost, _ = usageAmount.Float64() // = sum of line_items, matches Costs map
	return resp, nil
}

// ============================================================================
// CLUSTER PRICING SERVICE
// ============================================================================

func formatOptionalMoney(raw sql.NullString) (string, error) {
	if !raw.Valid {
		return "", nil
	}
	value, err := decimal.NewFromString(raw.String)
	if err != nil {
		return "", err
	}
	return value.Round(2).StringFixed(2), nil
}

// GetClusterPricing retrieves pricing configuration for a cluster
func (s *PurserServer) GetClusterPricing(ctx context.Context, req *purserpb.GetClusterPricingRequest) (*purserpb.ClusterPricing, error) {
	clusterID := req.GetClusterId()
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}

	row, err := purserdb.New(s.db).GetClusterPricingConfig(ctx, clusterID)
	if errors.Is(err, sql.ErrNoRows) {
		// Return default pricing for clusters without explicit config
		defaultPricing := &purserpb.ClusterPricing{
			ClusterId:         clusterID,
			PricingModel:      "tier_inherit",
			Currency:          "EUR",
			RequiredTierLevel: 0,
			AllowFreeTier:     false,
		}
		if officialIDs, qmErr := s.getOfficialClusterIDs(ctx); qmErr == nil && officialIDs != nil {
			defaultPricing.IsPlatformOfficial = officialIDs[clusterID]
		}
		return defaultPricing, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	pricing, err := clusterPricingFromFields(
		row.ID, row.ClusterID, row.PricingModel,
		row.StripeProductID, row.StripePriceIDMonthly, row.StripeMeterEventName,
		row.BasePrice, row.Currency, row.MeteredRates,
		row.RequiredTierLevel, row.AllowFreeTier, row.DefaultQuotas,
		row.CreatedAt, row.UpdatedAt,
	)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "invalid cluster pricing: %v", err)
	}

	// Enrich is_platform_official from Quartermaster (cached)
	if officialIDs, qmErr := s.getOfficialClusterIDs(ctx); qmErr == nil && officialIDs != nil {
		pricing.IsPlatformOfficial = officialIDs[clusterID]
	}

	return pricing, nil
}

func clusterPricingFromFields(
	id, clusterID, pricingModel string,
	stripeProductID, stripePriceIDMonthly, stripeMeterEventName sql.NullString,
	basePrice string, currency sql.NullString, meteredRates []byte,
	requiredTierLevel sql.NullInt32, allowFreeTier sql.NullBool, defaultQuotas []byte,
	createdAt, updatedAt sql.NullTime,
) (*purserpb.ClusterPricing, error) {
	pricing := &purserpb.ClusterPricing{
		Id: id, ClusterId: clusterID, PricingModel: pricingModel,
		RequiredTierLevel: requiredTierLevel.Int32, AllowFreeTier: allowFreeTier.Bool,
		Currency: "EUR", CreatedAt: timestamppb.New(createdAt.Time), UpdatedAt: timestamppb.New(updatedAt.Time),
	}
	if stripeProductID.Valid {
		pricing.StripeProductId = &stripeProductID.String
	}
	if stripePriceIDMonthly.Valid {
		pricing.StripePriceIdMonthly = &stripePriceIDMonthly.String
	}
	if stripeMeterEventName.Valid {
		pricing.StripeMeterEventName = &stripeMeterEventName.String
	}
	if basePrice != "" {
		formatted, err := formatOptionalMoney(sql.NullString{String: basePrice, Valid: true})
		if err != nil {
			return nil, err
		}
		pricing.BasePrice = formatted
	}
	if currency.Valid {
		pricing.Currency = currency.String
	}
	if len(meteredRates) > 0 {
		var err error
		pricing.MeteredRates, err = structpb.NewStruct(jsonToMap(meteredRates))
		if err != nil {
			return nil, fmt.Errorf("decode metered rates: %w", err)
		}
	}
	if len(defaultQuotas) > 0 {
		var err error
		pricing.DefaultQuotas, err = structpb.NewStruct(jsonToMap(defaultQuotas))
		if err != nil {
			return nil, fmt.Errorf("decode default quotas: %w", err)
		}
	}
	return pricing, nil
}

// GetClustersPricingBatch retrieves pricing configuration for multiple clusters
func (s *PurserServer) GetClustersPricingBatch(ctx context.Context, req *purserpb.GetClustersPricingBatchRequest) (*purserpb.GetClustersPricingBatchResponse, error) {
	clusterIDs := req.GetClusterIds()
	if len(clusterIDs) == 0 {
		return &purserpb.GetClustersPricingBatchResponse{
			Pricings: make(map[string]*purserpb.ClusterPricing),
		}, nil
	}

	tenantID := req.GetTenantId()

	// Resolve tenant tier level for eligibility checks (default to 0 if not found)
	var tenantTierLevel int32
	if tenantID != "" {
		var err error
		tenantTierLevel, err = purserdb.New(s.db).GetActiveTenantTierLevel(ctx, tenantID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			s.logger.WithError(err).Warn("Failed to get tenant tier level for batch pricing")
		}
	}

	rows, err := purserdb.New(s.db).ListClusterPricingConfigs(ctx, purserdb.ListClusterPricingConfigsParams{
		FilterClusters: true, ClusterIds: clusterIDs, ResultLimit: int32(len(clusterIDs)),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	result := make(map[string]*purserpb.ClusterPricing)
	for _, row := range rows {
		pricing, mapErr := clusterPricingFromFields(
			row.ID, row.ClusterID, row.PricingModel,
			row.StripeProductID, row.StripePriceIDMonthly, row.StripeMeterEventName,
			row.BasePrice, row.Currency, row.MeteredRates,
			row.RequiredTierLevel, row.AllowFreeTier, row.DefaultQuotas,
			row.CreatedAt, row.UpdatedAt,
		)
		if mapErr != nil {
			return nil, status.Errorf(codes.Internal, "invalid cluster pricing: %v", mapErr)
		}
		result[pricing.ClusterId] = pricing
	}

	// Flag clusters without explicit pricing configuration
	for _, clusterID := range clusterIDs {
		if _, found := result[clusterID]; !found {
			s.logger.WithField("cluster_id", clusterID).Warn("Missing cluster pricing configuration")
			pricing := &purserpb.ClusterPricing{
				ClusterId:    clusterID,
				PricingModel: "tier_inherit",
				IsEligible:   false,
			}
			denial := "Pricing not configured. Contact support."
			pricing.DenialReason = &denial
			result[clusterID] = pricing
		}
	}

	// Enrich is_platform_official from Quartermaster (cached)
	if officialIDs, qmErr := s.getOfficialClusterIDs(ctx); qmErr == nil && officialIDs != nil {
		for _, p := range result {
			p.IsPlatformOfficial = officialIDs[p.ClusterId]
		}
	}
	for _, p := range result {
		s.applyCommercialEligibility(ctx, tenantID, tenantTierLevel, p)
	}

	return &purserpb.GetClustersPricingBatchResponse{
		Pricings: result,
	}, nil
}

func applyEligibility(tenantID string, tenantTierLevel int32, pricing *purserpb.ClusterPricing) {
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

type commercialClusterKind string

const (
	commercialKindPlatformOfficial commercialClusterKind = "platform_official"
	commercialKindTenantPrivate    commercialClusterKind = "tenant_private"
	commercialKindThirdParty       commercialClusterKind = "third_party_marketplace"
)

func (s *PurserServer) classifyCommercialCluster(ctx context.Context, consumingTenantID, clusterID string) (commercialClusterKind, *uuid.UUID, bool, error) {
	if s.quartermasterClient == nil {
		return "", nil, false, status.Error(codes.Unavailable, "quartermaster client not configured")
	}
	resp, err := s.quartermasterClient.GetCluster(ctx, clusterID)
	if err != nil {
		return "", nil, false, status.Errorf(codes.Internal, "get cluster: %v", err)
	}
	c := resp.GetCluster()
	if c == nil {
		return "", nil, false, status.Error(codes.NotFound, "cluster not found")
	}
	if c.GetIsPlatformOfficial() {
		return commercialKindPlatformOfficial, nil, c.GetRequiresApproval(), nil
	}
	owner := c.GetOwnerTenantId()
	if owner == "" {
		return "", nil, false, status.Error(codes.FailedPrecondition, "cluster ownership is ambiguous")
	}
	ownerID, err := uuid.Parse(owner)
	if err != nil {
		return "", nil, false, status.Errorf(codes.FailedPrecondition, "invalid cluster owner_tenant_id: %v", err)
	}
	if ownerID.String() == consumingTenantID {
		return commercialKindTenantPrivate, &ownerID, c.GetRequiresApproval(), nil
	}
	return commercialKindThirdParty, &ownerID, c.GetRequiresApproval(), nil
}

func (s *PurserServer) requireMarketplaceOwnerApproved(ctx context.Context, ownerID uuid.UUID) error {
	owner, err := purserdb.New(s.db).GetMarketplaceOwnerApproval(ctx, ownerID.String())
	if errors.Is(err, sql.ErrNoRows) {
		return status.Error(codes.FailedPrecondition, "cluster operator is not approved for marketplace")
	}
	if err != nil {
		return status.Errorf(codes.Internal, "query cluster operator approval: %v", err)
	}
	if owner.Status != "approved" || !owner.PayoutEligible {
		return status.Error(codes.FailedPrecondition, "cluster operator is not payout eligible")
	}
	return nil
}

func marketplaceApprovalRequired(requiresApproval bool, pricingModel string) (bool, error) {
	if requiresApproval && pricingModel == "monthly" {
		return false, status.Error(codes.FailedPrecondition, "monthly marketplace access cannot combine checkout and owner approval in v0.3")
	}
	return requiresApproval || pricingModel == "custom", nil
}

func (s *PurserServer) applyCommercialEligibility(ctx context.Context, tenantID string, tenantTierLevel int32, pricing *purserpb.ClusterPricing) {
	if pricing != nil && !pricing.IsEligible && pricing.DenialReason != nil {
		return
	}
	applyEligibility(tenantID, tenantTierLevel, pricing)
	if pricing == nil || !pricing.IsEligible || tenantID == "" {
		return
	}
	kind, ownerID, requiresApproval, err := s.classifyCommercialCluster(ctx, tenantID, pricing.ClusterId)
	if err != nil {
		pricing.IsEligible = false
		reason := status.Convert(err).Message()
		pricing.DenialReason = &reason
		return
	}
	if kind != commercialKindThirdParty {
		return
	}
	if _, approvalErr := marketplaceApprovalRequired(requiresApproval, pricing.PricingModel); approvalErr != nil {
		pricing.IsEligible = false
		reason := status.Convert(approvalErr).Message()
		pricing.DenialReason = &reason
		return
	}
	if pricing.Id == "" {
		pricing.IsEligible = false
		reason := "third-party cluster pricing is not configured"
		pricing.DenialReason = &reason
		return
	}
	if ownerID == nil {
		pricing.IsEligible = false
		reason := "cluster operator is not approved for marketplace"
		pricing.DenialReason = &reason
		return
	}
	if err := s.requireMarketplaceOwnerApproved(ctx, *ownerID); err != nil {
		pricing.IsEligible = false
		reason := status.Convert(err).Message()
		pricing.DenialReason = &reason
	}
}

func (s *PurserServer) grantClusterAccessForKind(ctx context.Context, tenantID, clusterID string, kind commercialClusterKind) error {
	if s.quartermasterClient == nil {
		return status.Error(codes.FailedPrecondition, "quartermaster client not configured")
	}
	if kind == commercialKindPlatformOfficial {
		if err := s.quartermasterClient.BootstrapClusterAccess(ctx, tenantID, clusterID, nil); err != nil {
			return status.Errorf(codes.Internal, "grant platform cluster access: %v", err)
		}
		return nil
	}
	accessSource := clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_MARKETPLACE_SUBSCRIPTION
	if kind == commercialKindTenantPrivate {
		accessSource = clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER
	}
	if err := s.quartermasterClient.MaterializeClusterAccess(ctx, &quartermasterpb.MaterializeClusterAccessRequest{
		TenantId: tenantID, ClusterId: clusterID, AccessSource: accessSource,
		AuthorizationReference: "purser:" + string(kind),
	}); err != nil {
		return status.Errorf(codes.Internal, "materialize cluster access: %v", err)
	}
	return nil
}

func (s *PurserServer) requestMarketplaceApproval(ctx context.Context, tenantID, clusterID string) error {
	if s.quartermasterClient == nil {
		return status.Error(codes.FailedPrecondition, "quartermaster client not configured")
	}
	if err := s.quartermasterClient.MaterializeClusterAccess(ctx, &quartermasterpb.MaterializeClusterAccessRequest{
		TenantId: tenantID, ClusterId: clusterID,
		AccessSource:           clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_MARKETPLACE_SUBSCRIPTION,
		AuthorizationReference: "purser:marketplace-approval",
		SubscriptionStatus:     "pending_approval",
	}); err != nil {
		return status.Errorf(codes.Internal, "record marketplace approval request: %v", err)
	}
	return nil
}

// SetClusterPricing creates or updates pricing configuration for a cluster
func (s *PurserServer) SetClusterPricing(ctx context.Context, req *purserpb.SetClusterPricingRequest) (*purserpb.ClusterPricing, error) {
	clusterID := req.GetClusterId()
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}

	pricingModel := req.GetPricingModel()
	pricingModelProvided := pricingModel != ""
	validModels := []string{"free_unmetered", "metered", "monthly", "tier_inherit", "custom"}
	if pricingModelProvided && !slices.Contains(validModels, pricingModel) {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pricing_model: %s", pricingModel)
	}

	existingPricing := false
	existing, err := purserdb.New(s.db).GetExistingClusterPricing(ctx, clusterID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, status.Errorf(codes.Internal, "failed to read existing cluster pricing: %v", err)
	default:
		existingPricing = true
	}
	if !pricingModelProvided {
		if existingPricing {
			pricingModel = existing.PricingModel
		} else {
			pricingModel = "tier_inherit"
		}
	}
	if !slices.Contains(validModels, pricingModel) {
		return nil, status.Errorf(codes.FailedPrecondition, "existing cluster pricing has invalid pricing_model: %s", pricingModel)
	}

	// Build upsert query
	var basePrice sql.NullString
	if req.BasePrice != nil {
		parsedBasePrice, parseErr := decimal.NewFromString(*req.BasePrice)
		if parseErr != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid base_price: %v", parseErr)
		}
		basePrice = sql.NullString{String: parsedBasePrice.Round(2).StringFixed(2), Valid: true}
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

	// Marshal JSONB fields. Validate metered_rates at write time so the
	// rating engine never sees a malformed or empty metered/custom config.
	var meteredRatesBytes, defaultQuotasBytes []byte
	var ratesMap map[string]any
	if req.MeteredRates != nil {
		ratesMap = req.MeteredRates.AsMap()
		marshaled, marshalErr := json.Marshal(ratesMap)
		if marshalErr != nil {
			return nil, status.Errorf(codes.Internal, "marshal metered_rates: %v", marshalErr)
		}
		meteredRatesBytes = marshaled
	} else if existingPricing && (pricingModel == "metered" || pricingModel == "custom" || !pricingModelProvided) {
		if existing.MeteredRates != "" {
			if unmarshalErr := json.Unmarshal([]byte(existing.MeteredRates), &ratesMap); unmarshalErr != nil {
				return nil, status.Errorf(codes.FailedPrecondition, "existing metered_rates invalid JSON: %v", unmarshalErr)
			}
			meteredRatesBytes = []byte(existing.MeteredRates)
		}
	}
	if ratesMap == nil {
		ratesMap = map[string]any{}
	}
	if meteredRatesBytes == nil {
		meteredRatesBytes = []byte("{}")
	}
	if validateErr := pricing.ValidateMeteredRates(ratesMap, pricing.Model(pricingModel)); validateErr != nil {
		return nil, status.Errorf(codes.InvalidArgument, "metered_rates: %v", validateErr)
	}
	if req.DefaultQuotas != nil {
		defaultQuotasBytes, _ = json.Marshal(req.DefaultQuotas.AsMap())
	} else {
		defaultQuotasBytes = []byte("{}")
	}

	_, err = purserdb.New(s.db).UpsertClusterPricingConfig(ctx, purserdb.UpsertClusterPricingConfigParams{
		ClusterID: clusterID, PricingModel: pricingModel, BasePrice: basePrice,
		Currency:          sql.NullString{String: currency, Valid: true},
		RequiredTierLevel: sql.NullInt32{Int32: requiredTierLevel, Valid: true},
		AllowFreeTier:     sql.NullBool{Bool: allowFreeTier, Valid: true},
		MeteredRates:      meteredRatesBytes, DefaultQuotas: defaultQuotasBytes,
		StripeProductID:      sqlNullString(req.StripeProductId),
		StripePriceIDMonthly: sqlNullString(req.StripePriceIdMonthly),
		StripeMeterEventName: sqlNullString(req.StripeMeterEventName),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to set cluster pricing: %v", err)
	}

	// Return the updated pricing
	return s.GetClusterPricing(ctx, &purserpb.GetClusterPricingRequest{ClusterId: clusterID})
}

// ListClusterPricings returns pricing configs for clusters owned by a tenant
func (s *PurserServer) ListClusterPricings(ctx context.Context, req *purserpb.ListClusterPricingsRequest) (*purserpb.ListClusterPricingsResponse, error) {
	ownerTenantID := req.GetOwnerTenantId()
	var clusterIDs []string
	if ownerTenantID != "" {
		// Get cluster IDs owned by this tenant via Quartermaster gRPC (not direct DB access)
		if s.quartermasterClient == nil {
			return nil, status.Error(codes.Unavailable, "quartermaster client not configured")
		}

		paginationReq := &commonpb.CursorPaginationRequest{First: 200}

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
			paginationReq = &commonpb.CursorPaginationRequest{
				First: paginationReq.First,
				After: &endCursor,
			}
		}

		if len(clusterIDs) == 0 {
			// No clusters owned by this tenant - legitimate empty result
			return &purserpb.ListClusterPricingsResponse{Pricings: []*purserpb.ClusterPricing{}}, nil
		}
	}

	// Apply pagination
	limit := int32(50)
	if req.GetPagination() != nil && req.GetPagination().GetFirst() > 0 {
		limit = req.GetPagination().GetFirst()
	}
	rows, err := purserdb.New(s.db).ListClusterPricingConfigs(ctx, purserdb.ListClusterPricingConfigsParams{
		FilterClusters: ownerTenantID != "", ClusterIds: clusterIDs, ResultLimit: limit + 1,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	var pricings []*purserpb.ClusterPricing
	for _, row := range rows {
		pricing, mapErr := clusterPricingFromFields(
			row.ID, row.ClusterID, row.PricingModel,
			row.StripeProductID, row.StripePriceIDMonthly, row.StripeMeterEventName,
			row.BasePrice, row.Currency, row.MeteredRates,
			row.RequiredTierLevel, row.AllowFreeTier, row.DefaultQuotas,
			row.CreatedAt, row.UpdatedAt,
		)
		if mapErr != nil {
			return nil, status.Errorf(codes.Internal, "invalid cluster pricing: %v", mapErr)
		}
		pricings = append(pricings, pricing)
	}

	// Enrich is_platform_official from Quartermaster (cached)
	if officialIDs, qmErr := s.getOfficialClusterIDs(ctx); qmErr == nil && officialIDs != nil {
		for _, p := range pricings {
			p.IsPlatformOfficial = officialIDs[p.ClusterId]
		}
	}

	resp := &purserpb.ListClusterPricingsResponse{Pricings: pricings}
	if int32(len(pricings)) > limit {
		resp.Pricings = pricings[:limit]
		resp.Pagination = &commonpb.CursorPaginationResponse{HasNextPage: true}
	}

	return resp, nil
}

// CheckClusterAccess verifies if a tenant can subscribe to a cluster
func (s *PurserServer) CheckClusterAccess(ctx context.Context, req *purserpb.CheckClusterAccessRequest) (*purserpb.CheckClusterAccessResponse, error) {
	tenantID := req.GetTenantId()
	clusterID := req.GetClusterId()

	if tenantID == "" || clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and cluster_id required")
	}

	// Get tenant's billing tier level from Purser's own tables (no cross-service DB access)
	tenantTierLevel, err := purserdb.New(s.db).GetActiveTenantTierLevel(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		// No active subscription = free tier (level 0)
		tenantTierLevel = 0
	} else if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Get cluster pricing config
	pricing, err := s.GetClusterPricing(ctx, &purserpb.GetClusterPricingRequest{ClusterId: clusterID})
	if err != nil {
		return nil, err
	}

	resp := &purserpb.CheckClusterAccessResponse{
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
	kind, ownerID, _, err := s.classifyCommercialCluster(ctx, tenantID, clusterID)
	if err != nil {
		resp.Allowed = false
		resp.DenialReason = status.Convert(err).Message()
		return resp, nil
	}
	if kind == commercialKindThirdParty {
		if pricing.Id == "" {
			resp.Allowed = false
			resp.DenialReason = "third-party cluster pricing is not configured"
			return resp, nil
		}
		if ownerID == nil {
			resp.Allowed = false
			resp.DenialReason = "cluster operator is not approved for marketplace"
			return resp, nil
		}
		if err := s.requireMarketplaceOwnerApproved(ctx, *ownerID); err != nil {
			resp.Allowed = false
			resp.DenialReason = status.Convert(err).Message()
			return resp, nil
		}
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
func (s *PurserServer) CreateClusterSubscription(ctx context.Context, req *purserpb.CreateClusterSubscriptionRequest) (*purserpb.ClusterSubscriptionResponse, error) {
	tenantID := req.GetTenantId()
	clusterID := req.GetClusterId()

	if tenantID == "" || clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and cluster_id required")
	}
	kind, ownerID, requiresApproval, err := s.classifyCommercialCluster(ctx, tenantID, clusterID)
	if err != nil {
		return nil, err
	}
	resp := &purserpb.ClusterSubscriptionResponse{
		ClusterId: clusterID,
		TenantId:  tenantID,
	}

	// A tenant's own private cluster is inherent ownership, not a commercial
	// subscription. Materialize the owner provenance without consulting pricing
	// or creating provider-side billing work.
	if kind == commercialKindTenantPrivate {
		if grantErr := s.grantClusterAccessForKind(ctx, tenantID, clusterID, kind); grantErr != nil {
			return nil, grantErr
		}
		resp.Status = "active"
		return resp, nil
	}

	// Check if tenant can access this cluster
	accessResp, err := s.CheckClusterAccess(ctx, &purserpb.CheckClusterAccessRequest{
		TenantId:  tenantID,
		ClusterId: clusterID,
	})
	if err != nil {
		return nil, err
	}
	if !accessResp.Allowed {
		return nil, status.Errorf(codes.PermissionDenied, "access denied: %s", accessResp.DenialReason)
	}

	// Platform-official access is provisioned by the tenant's billing tier.
	// Cluster pricing must never turn it into a second Stripe subscription.
	if kind == commercialKindPlatformOfficial {
		if grantErr := s.grantClusterAccessForKind(ctx, tenantID, clusterID, kind); grantErr != nil {
			return nil, grantErr
		}
		resp.Status = "active"
		return resp, nil
	}

	// Get cluster pricing to determine subscription type
	pricing, err := s.GetClusterPricing(ctx, &purserpb.GetClusterPricingRequest{ClusterId: clusterID})
	if err != nil {
		return nil, err
	}
	if ownerID == nil {
		return nil, status.Error(codes.FailedPrecondition, "cluster operator is not approved for marketplace")
	}
	if err := s.requireMarketplaceOwnerApproved(ctx, *ownerID); err != nil {
		return nil, err
	}
	approvalRequired, approvalErr := marketplaceApprovalRequired(requiresApproval, pricing.PricingModel)
	if approvalErr != nil {
		return nil, approvalErr
	}
	if approvalRequired {
		if err := s.requestMarketplaceApproval(ctx, tenantID, clusterID); err != nil {
			return nil, err
		}
		resp.Status = "pending_approval"
		return resp, nil
	}

	// Resolve based on pricing model: free/tier_inherit/metered grant
	// Quartermaster access immediately; monthly redirects to Stripe
	// checkout (access on webhook); custom requires owner approval.
	switch pricing.PricingModel {
	case "free_unmetered", "tier_inherit":
		// No payment gate. Purser still grants access because this is where
		// pricing, operator vetting, and self-hosted classification are checked.
		if grantErr := s.grantClusterAccessForKind(ctx, tenantID, clusterID, kind); grantErr != nil {
			return nil, grantErr
		}
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

		// Durable intent before any Stripe side effect. Idempotency key is
		// deterministic on (tenant, cluster); repeated calls for the same
		// cluster collapse to one intent row.
		clusterCurrency := strings.ToUpper(pricing.GetCurrency())
		if clusterCurrency == "" {
			clusterCurrency = billing.DefaultCurrency()
		}
		intentKey := fmt.Sprintf("stripe-cluster-checkout:%s:%s", tenantID, clusterID)
		clusterIntentID, intentErr := purserdb.New(s.db).UpsertStripeClusterCheckoutIntent(ctx, purserdb.UpsertStripeClusterCheckoutIntentParams{
			TenantID: tenantID, Currency: clusterCurrency, IdempotencyKey: intentKey,
		})
		if intentErr != nil {
			s.logger.WithError(intentErr).Error("Failed to record Stripe cluster checkout intent")
			return nil, status.Error(codes.Internal, "failed to record checkout intent")
		}

		// Get/create Stripe customer for this tenant
		billingEmail := req.GetBillingEmail()
		if billingEmail == "" {
			billingEmail = fmt.Sprintf("tenant+%s@example.com", tenantID[:8]) // Fallback
		}
		cust, err := s.stripeClient.CreateOrGetCustomer(ctx, stripe.CustomerInfo{
			TenantID:       tenantID,
			Email:          billingEmail,
			Name:           fmt.Sprintf("Tenant %s", tenantID[:8]),
			IdempotencyKey: "stripe-customer:" + tenantID,
			Metadata: map[string]string{
				"cluster_id": clusterID,
			},
		})
		if err != nil {
			s.logger.Error("Failed to create Stripe customer", "error", err)
			s.markProviderIntentFailed(ctx, clusterIntentID, "customer_create_failed", err)
			return nil, status.Errorf(codes.Internal, "failed to setup payment: %v", err)
		}
		if cust.ID != "" {
			if updateErr := purserdb.New(s.db).SetClusterCheckoutIntentCustomer(ctx, purserdb.SetClusterCheckoutIntentCustomerParams{
				CustomerID: sql.NullString{String: cust.ID, Valid: true}, IntentID: clusterIntentID,
			}); updateErr != nil {
				s.logger.WithError(updateErr).WithField("intent_id", clusterIntentID).Warn("Failed to record provider_customer_id")
			}
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
			CustomerID:     cust.ID,
			TenantID:       tenantID,
			Purpose:        "cluster_subscription",
			ReferenceID:    clusterID,
			ClusterID:      clusterID,
			PriceID:        priceID,
			Currency:       clusterCurrency,
			SuccessURL:     successURL,
			CancelURL:      cancelURL,
			IdempotencyKey: intentKey,
		})
		if err != nil {
			s.logger.Error("Failed to create Stripe checkout session", "error", err)
			s.markProviderIntentFailed(ctx, clusterIntentID, "checkout_session_create_failed", err)
			return nil, status.Errorf(codes.Internal, "failed to create checkout: %v", err)
		}
		if sess != nil && sess.ID != "" {
			if updateErr := purserdb.New(s.db).SetClusterCheckoutIntentSessionOpen(ctx, purserdb.SetClusterCheckoutIntentSessionOpenParams{
				SessionID: sql.NullString{String: sess.ID, Valid: true}, IntentID: clusterIntentID,
			}); updateErr != nil {
				s.logger.WithError(updateErr).WithField("intent_id", clusterIntentID).Warn("Failed to record provider_session_id")
			}
		}

		subscriptionID, err := purserdb.New(s.db).UpsertPendingClusterCheckoutSubscription(ctx, purserdb.UpsertPendingClusterCheckoutSubscriptionParams{
			TenantID: tenantID, ClusterID: clusterID,
			CustomerID: sql.NullString{String: cust.ID, Valid: cust.ID != ""},
			SessionID:  sql.NullString{String: sess.ID, Valid: sess.ID != ""}, IntentID: clusterIntentID,
		})
		if err != nil {
			s.logger.WithError(err).WithField("session_id", sess.ID).Error("Failed to record cluster subscription checkout")
			if expireErr := s.stripeClient.ExpireCheckoutSession(ctx, sess.ID); expireErr != nil {
				s.logger.WithError(expireErr).WithField("session_id", sess.ID).Error("Failed to expire Stripe checkout session after local persist failure")
			}
			s.markProviderIntentFailed(ctx, clusterIntentID, "cluster_subscription_persist_failed", err)
			return nil, status.Error(codes.Internal, "failed to record cluster subscription")
		}

		resp.SubscriptionId = subscriptionID
		resp.Status = "pending_payment"
		checkoutURL := sess.URL
		resp.CheckoutUrl = &checkoutURL
		s.logger.Info("Created Stripe checkout for cluster subscription",
			"tenant", tenantID, "cluster", clusterID, "session", sess.ID)

	case "metered":
		// Metered clusters activate immediately — usage rates against the
		// cluster's metered_rates at invoice time, no upfront payment.
		if grantErr := s.grantClusterAccessForKind(ctx, tenantID, clusterID, kind); grantErr != nil {
			return nil, grantErr
		}
		resp.Status = "active"

	case "custom":
		return nil, status.Error(codes.Internal, "custom marketplace approval was not resolved")
	}

	return resp, nil
}

// CancelClusterSubscription cancels a tenant's subscription to a cluster
func (s *PurserServer) CancelClusterSubscription(ctx context.Context, req *purserpb.CancelClusterSubscriptionRequest) (*emptypb.Empty, error) {
	tenantID := req.GetTenantId()
	clusterID := req.GetClusterId()

	if tenantID == "" || clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and cluster_id required")
	}
	kind, _, _, err := s.classifyCommercialCluster(ctx, tenantID, clusterID)
	if err != nil {
		return nil, err
	}
	switch kind {
	case commercialKindPlatformOfficial:
		return nil, status.Error(codes.FailedPrecondition, "platform-official access is managed by the tenant billing tier")
	case commercialKindTenantPrivate:
		return nil, status.Error(codes.FailedPrecondition, "cluster owners cannot cancel inherent owner access")
	}

	// Get cluster pricing to check if there's a Stripe subscription to cancel
	pricing, err := s.GetClusterPricing(ctx, &purserpb.GetClusterPricingRequest{ClusterId: clusterID})
	if err != nil {
		return nil, err
	}

	if pricing.PricingModel == "monthly" {
		if s.stripeClient == nil {
			return nil, status.Error(codes.Unavailable, "Stripe not configured")
		}

		stripeSubID, lookupErr := purserdb.New(s.db).GetClusterStripeSubscriptionID(ctx, purserdb.GetClusterStripeSubscriptionIDParams{
			TenantID: tenantID, ClusterID: clusterID,
		})
		if errors.Is(lookupErr, sql.ErrNoRows) || !stripeSubID.Valid {
			return nil, status.Error(codes.NotFound, "no Stripe subscription found for cluster")
		}
		if lookupErr != nil {
			return nil, status.Error(codes.Internal, "failed to load cluster subscription")
		}

		sub, cancelErr := s.stripeClient.CancelSubscription(ctx, stripeSubID.String)
		if cancelErr != nil {
			s.logger.WithError(cancelErr).Error("Failed to cancel Stripe cluster subscription")
			return nil, status.Error(codes.Internal, "failed to cancel Stripe subscription")
		}

		var periodEnd *time.Time
		if sub.Items != nil && len(sub.Items.Data) > 0 && sub.Items.Data[0].CurrentPeriodEnd > 0 {
			t := time.Unix(sub.Items.Data[0].CurrentPeriodEnd, 0)
			periodEnd = &t
		}
		ourStatus := handlers.MapStripeSubscriptionStatus(string(sub.Status), sub.CancelAtPeriodEnd)

		periodEndValue := sql.NullTime{}
		if periodEnd != nil {
			periodEndValue = sql.NullTime{Time: *periodEnd, Valid: true}
		}
		err = purserdb.New(s.db).UpdateCancelledClusterSubscription(ctx, purserdb.UpdateCancelledClusterSubscriptionParams{
			Status: ourStatus, StripeStatus: sql.NullString{String: string(sub.Status), Valid: true},
			PeriodEnd: periodEndValue, TenantID: tenantID, ClusterID: clusterID,
		})
		if err != nil {
			s.logger.WithError(err).Warn("Failed to update cluster subscription after cancellation")
		}
	} else if s.quartermasterClient != nil {
		err = s.quartermasterClient.RevokeMaterializedClusterAccess(ctx, &quartermasterpb.RevokeMaterializedClusterAccessRequest{
			TenantId: tenantID, ClusterId: clusterID,
			AccessSource:           clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_MARKETPLACE_SUBSCRIPTION,
			AuthorizationReference: "purser:cancel",
		})
		if err != nil {
			s.logger.WithError(err).Warn("Failed to revoke cluster access for non-monthly cluster")
		}
	}

	return &emptypb.Empty{}, nil
}

// ListMarketplaceClusterPricings returns paginated cluster pricings filtered by tenant tier level.
// Gateway uses this as the primary marketplace query, then enriches with Quartermaster metadata.
func (s *PurserServer) ListMarketplaceClusterPricings(ctx context.Context, req *purserpb.ListMarketplaceClusterPricingsRequest) (*purserpb.ListMarketplaceClusterPricingsResponse, error) {
	tenantID := req.GetTenantId()

	// Get tenant's billing tier level (0 if not found or no subscription)
	var tenantTierLevel int32
	if tenantID != "" {
		var err error
		tenantTierLevel, err = purserdb.New(s.db).GetActiveTenantTierLevel(ctx, tenantID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			s.logger.WithError(err).Warn("Failed to get tenant tier level")
		}
	}

	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	totalRows, countErr := purserdb.New(s.db).CountMarketplaceClusterPricings(ctx, tenantTierLevel)
	if countErr != nil {
		return nil, status.Errorf(codes.Internal, "count query failed: %v", countErr)
	}
	total := int32(totalRows)
	if totalRows > int64(^uint32(0)>>1) {
		total = int32(^uint32(0) >> 1)
	}
	cursorAt := sql.NullTime{}
	cursorID := ""
	if params.Cursor != nil {
		cursorAt = sql.NullTime{Time: params.Cursor.Timestamp, Valid: true}
		cursorID = params.Cursor.ID
	}
	rows, err := purserdb.New(s.db).ListMarketplaceClusterPricingPage(ctx, purserdb.ListMarketplaceClusterPricingPageParams{
		TierLevel: tenantTierLevel, HasCursor: params.Cursor != nil,
		Backward: params.Direction == pagination.Backward,
		CursorAt: cursorAt, CursorID: cursorID, ResultLimit: int32(params.Limit + 1),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	var pricings []*purserpb.MarketplaceClusterPricing
	for _, row := range rows {
		p := &purserpb.MarketplaceClusterPricing{
			ClusterId: row.ClusterID, PricingModel: row.PricingModel,
			RequiredTierLevel: row.RequiredTierLevel.Int32,
			Currency:          "EUR", CreatedAt: timestamppb.New(row.CreatedAt.Time),
		}
		if row.BasePrice != "" {
			value, err := decimal.NewFromString(row.BasePrice)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "invalid base_price: %v", err)
			}
			p.MonthlyPriceCents = int32(value.Mul(decimal.NewFromInt(100)).Round(0).IntPart())
		}
		if row.Currency.Valid {
			p.Currency = row.Currency.String
		}
		pricings = append(pricings, p)
	}

	// Enrich is_platform_official from Quartermaster (cached)
	if officialIDs, qmErr := s.getOfficialClusterIDs(ctx); qmErr == nil && officialIDs != nil {
		for _, p := range pricings {
			p.IsPlatformOfficial = officialIDs[p.ClusterId]
		}
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

	return &purserpb.ListMarketplaceClusterPricingsResponse{
		Pricings:   pricings,
		Pagination: pagination.BuildResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
	}, nil
}

// jsonToMap is a helper to convert JSON bytes to map for structpb
func jsonToMap(data []byte) map[string]any {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return make(map[string]any)
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
	Billing             *handlers.Service
	CertFile            string
	KeyFile             string
	AllowInsecure       bool
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

	// GRPCMetricsInterceptor sits outermost so Unauthenticated / PermissionDenied
	// rejections from the auth interceptor still show up in
	// purser_grpc_requests_total.
	opts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(
			middleware.GRPCMetricsInterceptor(cfg.Metrics.GRPCRequests, cfg.Metrics.GRPCDuration),
			unaryInterceptor(cfg.Logger),
			authInterceptor,
		),
	}
	tlsCfg := grpcutil.ServerTLSConfig{
		CertFile:      cfg.CertFile,
		KeyFile:       cfg.KeyFile,
		AllowInsecure: cfg.AllowInsecure,
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := grpcutil.WaitForServerTLSFiles(waitCtx, tlsCfg, cfg.Logger); err != nil {
		cfg.Logger.WithError(err).Fatal("Timed out waiting for Purser gRPC TLS files")
	}
	tlsOpt, err := grpcutil.ServerTLS(tlsCfg, cfg.Logger)
	if err != nil {
		cfg.Logger.WithError(err).Fatal("Failed to configure Purser gRPC TLS")
	}
	if tlsOpt != nil {
		opts = append(opts, tlsOpt)
	}

	server := grpc.NewServer(opts...)
	purserServer := NewPurserServer(cfg.DB, cfg.Logger, cfg.Metrics, cfg.StripeClient, cfg.MollieClient, cfg.QuartermasterClient, cfg.CommodoreClient, cfg.DecklogClient, cfg.Billing)

	// Drain worker for purser.billing_event_outbox. Replaces the prior
	// async decklogClient.SendServiceEvent path so Decklog outages don't
	// drop billing/payment/subscription/x402 events. Pre-launch this runs
	// on every Purser replica; SKIP LOCKED + lease in the claim query
	// distributes work safely without leader election.
	go purserServer.runBillingOutboxWorker(context.Background())
	go purserServer.runMediaAuthorityRefreshOutboxWorker(context.Background())

	// Register all services
	purserpb.RegisterBillingServiceServer(server, purserServer)
	purserpb.RegisterUsageServiceServer(server, purserServer)
	purserpb.RegisterSubscriptionServiceServer(server, purserServer)
	purserpb.RegisterInvoiceServiceServer(server, purserServer)
	purserpb.RegisterPaymentServiceServer(server, purserServer)
	purserpb.RegisterClusterPricingServiceServer(server, purserServer)
	purserpb.RegisterOperatorRevenueServiceServer(server, purserServer)
	purserpb.RegisterPrepaidServiceServer(server, purserServer)
	purserpb.RegisterWebhookServiceServer(server, purserServer)
	purserpb.RegisterStripeServiceServer(server, purserServer)
	purserpb.RegisterMollieServiceServer(server, purserServer)
	purserpb.RegisterX402ServiceServer(server, purserServer)
	purserpb.RegisterCryptoSweepServiceServer(server, purserServer)

	// Register gRPC health checking service
	hs := health.NewServer()
	grpc_health_v1.RegisterHealthServer(server, hs)
	reflection.Register(server)

	return server
}

// unaryInterceptor logs gRPC requests
func unaryInterceptor(logger logging.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
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
func (s *PurserServer) GetPrepaidBalance(ctx context.Context, req *purserpb.GetPrepaidBalanceRequest) (*purserpb.PrepaidBalance, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	currency := req.GetCurrency()
	if currency == "" {
		currency = billing.DefaultCurrency()
	}

	row, err := purserdb.New(s.db).GetPrepaidBalance(ctx, purserdb.GetPrepaidBalanceParams{
		TenantID: tenantID, Currency: currency,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Errorf(codes.NotFound, "no prepaid balance found for tenant %s", tenantID)
	}
	if err != nil {
		s.logger.WithError(err).Error("Failed to get prepaid balance")
		return nil, status.Error(codes.Internal, "failed to get prepaid balance")
	}

	balance := purserpb.PrepaidBalance{
		Id: row.ID.String(), TenantId: row.TenantID.String(), BalanceCents: row.BalanceCents,
		Currency: row.Currency, LowBalanceThresholdCents: row.LowBalanceThresholdCents,
		ReservedBalanceCents: row.ReservedBalanceCents,
		CreatedAt:            timestamppb.New(row.CreatedAt.Time), UpdatedAt: timestamppb.New(row.UpdatedAt.Time),
	}
	balance.AvailableBalanceCents = balance.BalanceCents - balance.ReservedBalanceCents
	balance.IsLowBalance = balance.AvailableBalanceCents < balance.LowBalanceThresholdCents

	// Calculate drain rate from last hour's usage deductions
	usageLastHour, err := purserdb.New(s.db).GetPrepaidDrainRate(ctx, tenantID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).Warn("Failed to calculate drain rate, defaulting to 0")
		usageLastHour = 0
	}
	balance.DrainRateCentsPerHour = usageLastHour

	return &balance, nil
}

// InitializePrepaidBalance creates a new prepaid balance record for a tenant
func (s *PurserServer) InitializePrepaidBalance(ctx context.Context, req *purserpb.InitializePrepaidBalanceRequest) (*purserpb.PrepaidBalance, error) {
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

	id := uuid.New()
	now := time.Now()

	_, err := purserdb.New(s.db).InitializePrepaidBalanceRow(ctx, purserdb.InitializePrepaidBalanceRowParams{
		ID: id, TenantID: tenantID, BalanceCents: req.GetInitialBalanceCents(), Currency: currency,
		LowBalanceThresholdCents: sql.NullInt64{Int64: threshold, Valid: true},
		Now:                      sql.NullTime{Time: now, Valid: true},
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to initialize prepaid balance")
		return nil, status.Error(codes.Internal, "failed to initialize prepaid balance")
	}

	// Fetch and return the balance (could be existing if ON CONFLICT hit)
	return s.GetPrepaidBalance(ctx, &purserpb.GetPrepaidBalanceRequest{
		TenantId: tenantID,
		Currency: currency,
	})
}

// InitializePrepaidAccount creates subscription + prepaid balance for wallet provisioning.
// getOfficialClusterIDs returns the set of platform-official cluster IDs,
// cached for 5 minutes via the shared tieraccess.Reconciler. Returns nil when
// no reconciler is configured (test wiring or local dev without Quartermaster).
func (s *PurserServer) getOfficialClusterIDs(ctx context.Context) (map[string]bool, error) {
	if s.tierReconciler == nil {
		return nil, nil
	}
	return s.tierReconciler.OfficialClusterIDs(ctx)
}

// reconcileTierClusterAccess delegates to the shared tieraccess.Reconciler.
// See package tieraccess for the bidirectional grant/suspend logic and the
// deployment_tier stamp. Returns empty values when no reconciler is
// configured (test wiring).
func (s *PurserServer) reconcileTierClusterAccess(ctx context.Context, tenantID string, tierLevel int32, tierName string) ([]string, string, error) {
	if s.tierReconciler == nil {
		return nil, "", nil
	}
	return s.tierReconciler.Reconcile(ctx, tenantID, tierLevel, tierName)
}

// reconcileCanonicalTierClusterAccess prevents an earlier request from leaving
// cluster grants at a tier that was superseded by a concurrently committed
// subscription change. Each pass applies the current canonical tier and then
// verifies that the tier did not change while the external reconcile ran.
func (s *PurserServer) reconcileCanonicalTierClusterAccess(ctx context.Context, tenantID string) ([]string, string, error) {
	if s.tierReconciler == nil {
		return nil, "", nil
	}
	queries := purserdb.New(s.db)
	for attempt := 0; attempt < 4; attempt++ {
		canonical, err := queries.GetCanonicalSubscriptionTier(ctx, tenantID)
		if err != nil {
			return nil, "", fmt.Errorf("load canonical tier: %w", err)
		}
		eligible, primary, err := s.reconcileTierClusterAccess(ctx, tenantID, canonical.TierLevel, canonical.TierName)
		if err != nil {
			return nil, "", err
		}
		currentTierID, err := queries.GetTenantSubscriptionTierID(ctx, tenantID)
		if err != nil {
			return nil, "", fmt.Errorf("verify canonical tier after reconcile: %w", err)
		}
		if currentTierID == canonical.TierID {
			return eligible, primary, nil
		}
	}
	return nil, "", fmt.Errorf("subscription tier kept changing during cluster reconciliation")
}

func (s *PurserServer) invalidateTenantCache(ctx context.Context, tenantID, reason string) error {
	if s.commodoreClient == nil {
		return nil
	}
	invalidateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.commodoreClient.InvalidateTenantCache(invalidateCtx, tenantID, reason); err != nil {
		return err
	}
	return nil
}

func (s *PurserServer) InitializePrepaidAccount(ctx context.Context, req *purserpb.InitializePrepaidAccountRequest) (*purserpb.InitializePrepaidAccountResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	currency := req.GetCurrency()
	if currency == "" {
		currency = billing.DefaultCurrency()
	}

	subscriptionID := uuid.New()
	balanceID := uuid.New()
	now := time.Now()

	// Use a transaction to create both subscription and balance atomically
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to start transaction")
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	// 1. Resolve default prepaid tier
	queries := purserdb.New(tx)
	defaultTier, err := queries.GetDefaultBillingTier(ctx, true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.FailedPrecondition, "no default prepaid billing tier configured")
	}
	if err != nil {
		s.logger.WithError(err).Error("Failed to resolve default prepaid tier")
		return nil, status.Error(codes.Internal, "failed to resolve billing tier")
	}

	// 2. Create subscription with billing_model='prepaid', status='active'
	_, err = queries.EnsureDefaultTenantSubscription(ctx, purserdb.EnsureDefaultTenantSubscriptionParams{
		ID: subscriptionID, TenantID: tenantID, TierID: defaultTier.ID,
		BillingModel: "prepaid", Now: sql.NullTime{Time: now, Valid: true},
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to create prepaid subscription")
		return nil, status.Error(codes.Internal, "failed to create subscription")
	}

	// 3. Create prepaid balance with initial balance 0
	_, err = queries.EnsureRuntimePrepaidBalance(ctx, purserdb.EnsureRuntimePrepaidBalanceParams{
		ID: balanceID, TenantID: tenantID, Currency: currency,
		Now: sql.NullTime{Time: now, Valid: true},
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to create prepaid balance")
		return nil, status.Error(codes.Internal, "failed to create prepaid balance")
	}

	if err = tx.Commit(); err != nil {
		return nil, status.Error(codes.Internal, "failed to commit transaction")
	}

	// An email and wallet signup may converge on the same tenant. Read the
	// winning subscription after ON CONFLICT instead of returning/reconciling
	// the default tier this request attempted to insert.
	actual, err := purserdb.New(s.db).GetInitializedPrepaidAccount(ctx, purserdb.GetInitializedPrepaidAccountParams{
		Currency: currency, TenantID: tenantID,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load initialized prepaid account: %v", err)
	}

	eligibleClusters, primaryCluster, clusterErr := s.reconcileCanonicalTierClusterAccess(ctx, tenantID)
	if clusterErr != nil {
		s.logger.WithError(clusterErr).WithField("tenant_id", tenantID).Warn("Failed to provision cluster access for prepaid account")
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":       tenantID,
		"subscription_id": actual.SubscriptionID.String(),
		"balance_id":      actual.BalanceID.String(),
		"tier_level":      actual.TierLevel,
	}).Info("Initialized prepaid account")

	return &purserpb.InitializePrepaidAccountResponse{
		SubscriptionId:     actual.SubscriptionID.String(),
		BalanceId:          actual.BalanceID.String(),
		TierLevel:          actual.TierLevel,
		EligibleClusterIds: eligibleClusters,
		PrimaryClusterId:   primaryCluster,
	}, nil
}

// InitializePostpaidAccount is the compatibility entry point for older callers.
func (s *PurserServer) InitializePostpaidAccount(ctx context.Context, req *purserpb.InitializePostpaidAccountRequest) (*purserpb.InitializePostpaidAccountResponse, error) {
	return s.EnsureFreeAccount(ctx, req)
}

// EnsureFreeAccount creates the default zero-priced postpaid Free subscription
// after email verification and reconciles its cluster access.
func (s *PurserServer) EnsureFreeAccount(ctx context.Context, req *purserpb.InitializePostpaidAccountRequest) (*purserpb.InitializePostpaidAccountResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	subscriptionID := uuid.New()
	now := time.Now()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to start transaction")
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	// Resolve default postpaid tier
	queries := purserdb.New(tx)
	defaultTier, err := queries.GetDefaultBillingTier(ctx, false)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.FailedPrecondition, "no default postpaid billing tier configured")
	}
	if err != nil {
		s.logger.WithError(err).Error("Failed to resolve default postpaid tier")
		return nil, status.Error(codes.Internal, "failed to resolve billing tier")
	}

	// Create subscription with billing_model='postpaid', status='active'
	_, err = queries.EnsureDefaultTenantSubscription(ctx, purserdb.EnsureDefaultTenantSubscriptionParams{
		ID: subscriptionID, TenantID: tenantID, TierID: defaultTier.ID,
		BillingModel: "postpaid", Now: sql.NullTime{Time: now, Valid: true},
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to create postpaid subscription")
		return nil, status.Error(codes.Internal, "failed to create subscription")
	}

	if err = tx.Commit(); err != nil {
		return nil, status.Error(codes.Internal, "failed to commit transaction")
	}

	// Read the winning subscription after ON CONFLICT. A concurrent wallet
	// provisioning request may already have established prepaid billing.
	actual, err := purserdb.New(s.db).GetInitializedPostpaidAccount(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load initialized free account: %v", err)
	}

	eligibleClusters, primaryCluster, clusterErr := s.reconcileCanonicalTierClusterAccess(ctx, tenantID)
	if clusterErr != nil {
		s.logger.WithError(clusterErr).WithField("tenant_id", tenantID).Warn("Failed to provision cluster access for postpaid account")
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":       tenantID,
		"subscription_id": actual.SubscriptionID.String(),
		"tier_level":      actual.TierLevel,
	}).Info("Initialized postpaid account")

	return &purserpb.InitializePostpaidAccountResponse{
		SubscriptionId:     actual.SubscriptionID.String(),
		TierLevel:          actual.TierLevel,
		EligibleClusterIds: eligibleClusters,
		PrimaryClusterId:   primaryCluster,
	}, nil
}

// TopupBalance adds funds to a tenant's prepaid balance
func (s *PurserServer) TopupBalance(ctx context.Context, req *purserpb.TopupBalanceRequest) (*purserpb.BalanceTransaction, error) {
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
func (s *PurserServer) DeductBalance(ctx context.Context, req *purserpb.DeductBalanceRequest) (*purserpb.BalanceTransaction, error) {
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
func (s *PurserServer) AdjustBalance(ctx context.Context, req *purserpb.AdjustBalanceRequest) (*purserpb.BalanceTransaction, error) {
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

func balanceTransactionFromReferenceRow(row purserdb.GetBalanceTransactionByReferenceRow) *purserpb.BalanceTransaction {
	txn := &purserpb.BalanceTransaction{
		Id: row.ID.String(), TenantId: row.TenantID.String(), AmountCents: row.AmountCents,
		BalanceAfterCents: row.BalanceAfterCents, TransactionType: row.TransactionType,
		Description: row.Description, CreatedAt: timestamppb.New(row.CreatedAt.Time),
	}
	if row.ReferenceID.Valid {
		value := row.ReferenceID.UUID.String()
		txn.ReferenceId = &value
	}
	if row.ReferenceType.Valid {
		txn.ReferenceType = &row.ReferenceType.String
	}
	return txn
}

// recordBalanceTransaction atomically updates balance and records the transaction
func (s *PurserServer) recordBalanceTransaction(
	ctx context.Context,
	tenantID, currency string,
	amountCents int64,
	txType, description string,
	referenceID, referenceType *string,
) (*purserpb.BalanceTransaction, error) {
	userID := middleware.GetUserID(ctx)
	actorKind := "system"
	actorID := sql.NullString{}
	if userID != "" {
		actorKind = "user"
		if _, parseErr := uuid.Parse(userID); parseErr == nil {
			actorID = sql.NullString{String: userID, Valid: true}
		}
	}
	evidenceRef := sql.NullString{}
	if referenceID != nil && *referenceID != "" {
		evidenceRef = sql.NullString{String: *referenceID, Valid: true}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to begin transaction")
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	queries := purserdb.New(tx)
	if err = queries.EnsurePrepaidBalanceRow(ctx, purserdb.EnsurePrepaidBalanceRowParams{
		TenantID: tenantID, Currency: currency,
	}); err != nil {
		s.logger.WithError(err).Error("Failed to ensure prepaid balance")
		return nil, status.Error(codes.Internal, "failed to initialize balance")
	}

	txUUID := uuid.New()
	txID := txUUID.String()
	now := time.Now()
	insertedWithReference := false

	if referenceID != nil && referenceType != nil {
		// Idempotency: use ON CONFLICT DO NOTHING so we can safely query in-tx
		// without aborting the transaction.
		_, insertErr := queries.InsertReferencedBalanceTransaction(ctx, purserdb.InsertReferencedBalanceTransactionParams{
			ID: txUUID, TenantID: tenantID, AmountCents: amountCents, TransactionType: txType,
			Description: sql.NullString{String: description, Valid: true}, ReferenceID: *referenceID,
			ReferenceType: sql.NullString{String: *referenceType, Valid: true},
			ActorKind:     sql.NullString{String: actorKind, Valid: true}, ActorID: actorID,
			Reason: sql.NullString{String: description, Valid: true}, EvidenceRef: evidenceRef,
			CreatedAt: sql.NullTime{Time: now, Valid: true},
		})
		if insertErr != nil {
			if !errors.Is(insertErr, sql.ErrNoRows) {
				s.logger.WithError(insertErr).Error("Failed to insert balance transaction")
				return nil, status.Error(codes.Internal, "failed to record transaction")
			}

			// Conflict: return the existing transaction (and do NOT mutate balance again).
			existing, scanErr := queries.GetBalanceTransactionByReference(ctx, purserdb.GetBalanceTransactionByReferenceParams{
				TenantID: tenantID, ReferenceType: sql.NullString{String: *referenceType, Valid: true}, ReferenceID: *referenceID,
			})
			if scanErr != nil {
				if errors.Is(scanErr, sql.ErrNoRows) {
					return nil, status.Error(codes.Internal, "duplicate transaction detected but existing record missing")
				}
				s.logger.WithError(scanErr).Error("Failed to load existing balance transaction")
				return nil, status.Error(codes.Internal, "failed to load existing transaction")
			}

			return balanceTransactionFromReferenceRow(existing), nil
		}

		insertedWithReference = true
	}

	// Update balance and get new balance in one query
	newBalance, err := queries.AddPrepaidBalance(ctx, purserdb.AddPrepaidBalanceParams{
		AmountCents: amountCents, TenantID: tenantID, Currency: currency,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Errorf(codes.NotFound, "no prepaid balance found for tenant %s", tenantID)
	}
	if err != nil {
		s.logger.WithError(err).Error("Failed to update balance")
		return nil, status.Error(codes.Internal, "failed to update balance")
	}
	previousBalance := newBalance - amountCents

	if insertedWithReference {
		_, err = queries.SetBalanceTransactionResult(ctx, purserdb.SetBalanceTransactionResultParams{
			BalanceAfterCents: newBalance, ID: txUUID,
		})
		if err != nil {
			s.logger.WithError(err).Error("Failed to update balance transaction")
			return nil, status.Error(codes.Internal, "failed to record transaction")
		}
	} else {
		referenceIDValue := sql.NullString{}
		if referenceID != nil && *referenceID != "" {
			referenceIDValue = sql.NullString{String: *referenceID, Valid: true}
		}
		referenceTypeValue := sql.NullString{}
		if referenceType != nil && *referenceType != "" {
			referenceTypeValue = sql.NullString{String: *referenceType, Valid: true}
		}
		err = queries.InsertBalanceTransaction(ctx, purserdb.InsertBalanceTransactionParams{
			ID: txUUID, TenantID: tenantID, AmountCents: amountCents, BalanceAfterCents: newBalance,
			TransactionType: txType, Description: sql.NullString{String: description, Valid: true},
			ReferenceID: referenceIDValue, ReferenceType: referenceTypeValue,
			ActorKind: sql.NullString{String: actorKind, Valid: true}, ActorID: actorID,
			Reason: sql.NullString{String: description, Valid: true}, EvidenceRef: evidenceRef,
			CreatedAt: sql.NullTime{Time: now, Valid: true},
		})
		if err != nil {
			s.logger.WithError(err).Error("Failed to insert transaction")
			return nil, status.Error(codes.Internal, "failed to record transaction")
		}
	}

	if txType == "topup" && amountCents > 0 {
		topupID := ""
		if referenceID != nil {
			topupID = *referenceID
		}
		if _, enqErr := s.EnqueueBillingEventTx(ctx, tx, eventTopupCredited, tenantID, userID, "topup", txID, &ipcpb.BillingEvent{
			TopupId:  topupID,
			Amount:   float64(amountCents) / 100.0,
			Currency: currency,
			Status:   "credited",
		}); enqErr != nil {
			s.logger.WithError(enqErr).Error("Failed to enqueue topup_credited event")
			return nil, status.Error(codes.Internal, "failed to enqueue topup credited event")
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, status.Error(codes.Internal, "failed to commit transaction")
	}

	if s.thresholdEnforcer != nil {
		if err := s.thresholdEnforcer.EnforcePrepaidThresholds(ctx, tenantID, previousBalance, newBalance); err != nil {
			s.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to enforce prepaid thresholds after balance update")
		}
	}

	// Auto-reactivate suspended tenant if balance goes positive after top-up
	if amountCents > 0 && newBalance >= 0 {
		rowsAffected, err := purserdb.New(s.db).ReactivateFundedSubscription(ctx, tenantID)
		if err != nil {
			s.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to check/reactivate suspended subscription")
		} else if rowsAffected > 0 {
			s.logger.WithFields(map[string]any{
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
						s.logger.WithFields(map[string]any{
							"tenant_id":           tenantID,
							"entries_invalidated": resp.EntriesInvalidated,
						}).Info("Invalidated media plane cache after reactivation")
					}
				}()
			}
		}
	}

	return &purserpb.BalanceTransaction{
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
func (s *PurserServer) ListBalanceTransactions(ctx context.Context, req *purserpb.ListBalanceTransactionsRequest) (*purserpb.ListBalanceTransactionsResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	params := purserdb.ListBalanceTransactionsParams{TenantID: tenantID}
	if req.TransactionType != nil && *req.TransactionType != "" {
		params.FilterType = true
		params.TransactionType = *req.TransactionType
	}

	if req.TimeRange != nil {
		if req.TimeRange.Start != nil {
			params.FilterStart = true
			params.StartAt = sql.NullTime{Time: req.TimeRange.Start.AsTime(), Valid: true}
		}
		if req.TimeRange.End != nil {
			params.FilterEnd = true
			params.EndAt = sql.NullTime{Time: req.TimeRange.End.AsTime(), Valid: true}
		}
	}

	rows, err := purserdb.New(s.db).ListBalanceTransactions(ctx, params)
	if err != nil {
		s.logger.WithError(err).Error("Failed to list transactions")
		return nil, status.Error(codes.Internal, "failed to list transactions")
	}
	transactions := make([]*purserpb.BalanceTransaction, 0, len(rows))
	for _, row := range rows {
		transactions = append(transactions, balanceTransactionFromListRow(row))
	}

	return &purserpb.ListBalanceTransactionsResponse{
		Transactions: transactions,
	}, nil
}

func balanceTransactionFromListRow(row purserdb.ListBalanceTransactionsRow) *purserpb.BalanceTransaction {
	txn := &purserpb.BalanceTransaction{
		Id: row.ID.String(), TenantId: row.TenantID.String(), AmountCents: row.AmountCents,
		BalanceAfterCents: row.BalanceAfterCents, TransactionType: row.TransactionType,
		Description: row.Description, CreatedAt: timestamppb.New(row.CreatedAt.Time),
	}
	if row.ReferenceID.Valid {
		value := row.ReferenceID.UUID.String()
		txn.ReferenceId = &value
	}
	if row.ReferenceType.Valid {
		txn.ReferenceType = &row.ReferenceType.String
	}
	return txn
}

// ============================================================================
// CARD TOP-UP METHODS
// ============================================================================

// CreateCardTopup creates a Stripe/Mollie checkout session for prepaid balance top-up
func (s *PurserServer) CreateCardTopup(ctx context.Context, req *purserpb.CreateCardTopupRequest) (*purserpb.CreateCardTopupResponse, error) {
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
	if successURL == "" || cancelURL == "" {
		return nil, status.Error(codes.InvalidArgument, "success_url and cancel_url are required")
	}
	if provider != "stripe" && provider != "mollie" {
		return nil, status.Error(codes.InvalidArgument, "provider must be 'stripe' or 'mollie'")
	}
	if currency == "" {
		currency = billing.DefaultCurrency()
	}
	currency = strings.ToUpper(currency)
	minimumCents, minimumErr := billing.FiatTopupMinimumCents(provider, currency)
	if minimumErr != nil {
		return nil, status.Error(codes.InvalidArgument, minimumErr.Error())
	}
	if amountCents < minimumCents {
		return nil, status.Errorf(codes.InvalidArgument, "minimum %s top-up through %s is %d cents", currency, provider, minimumCents)
	}
	if amountCents > billing.MaximumTopupCents {
		return nil, status.Errorf(codes.InvalidArgument, "maximum top-up is %d cents", billing.MaximumTopupCents)
	}
	if detailsErr := s.requireCompleteBillingDetails(ctx, tenantID); detailsErr != nil {
		return nil, detailsErr
	}

	userID := middleware.GetUserID(ctx)
	topupID := uuid.New().String()
	intentKey := fmt.Sprintf("prepaid-topup:%s", topupID)
	provisionalExpiresAt := time.Now().Add(24 * time.Hour)
	providerIntentID, err := purserdb.New(s.db).UpsertPrepaidTopupProviderIntent(ctx, purserdb.UpsertPrepaidTopupProviderIntentParams{
		TenantID: tenantID, Provider: provider, TopupID: topupID, Currency: currency,
		AmountCents: amountCents, IdempotencyKey: intentKey,
		ExpiresAt: sql.NullTime{Time: provisionalExpiresAt, Valid: true},
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to record prepaid top-up intent")
		return nil, status.Error(codes.Internal, "failed to record top-up intent")
	}

	err = purserdb.New(s.db).InsertPendingCardTopup(ctx, purserdb.InsertPendingCardTopupParams{
		TopupID: topupID, TenantID: tenantID, Provider: provider,
		AmountCents: amountCents, Currency: currency, ExpiresAt: provisionalExpiresAt,
		BillingEmail: sqlNullString(req.BillingEmail), BillingName: sqlNullString(req.BillingName),
		BillingCompany: sqlNullString(req.BillingCompany), BillingVatNumber: sqlNullString(req.BillingVatNumber),
		IntentID: providerIntentID,
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to create pending topup")
		return nil, status.Error(codes.Internal, "failed to create topup record")
	}

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
		IdempotencyKey: intentKey,
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to create checkout session")
		s.markProviderIntentFailed(ctx, providerIntentID, "checkout_session_create_failed", err)
		failTx, beginErr := s.db.BeginTx(ctx, nil)
		if beginErr != nil {
			s.logger.WithError(beginErr).WithField("topup_id", topupID).Warn("Failed to begin tx for topup_failed")
			return nil, status.Errorf(codes.Internal, "failed to create checkout: %v", err)
		}
		defer failTx.Rollback() //nolint:errcheck // rollback is best-effort post-commit
		if _, markErr := purserdb.New(failTx).FailPendingCardTopup(ctx, topupID); markErr != nil {
			s.logger.WithError(markErr).WithField("topup_id", topupID).Warn("Failed to mark prepaid top-up failed")
			return nil, status.Errorf(codes.Internal, "failed to create checkout: %v", err)
		}
		if _, enqErr := s.EnqueueBillingEventTx(ctx, failTx, eventTopupFailed, tenantID, userID, "topup", topupID, &ipcpb.BillingEvent{
			TopupId:  topupID,
			Amount:   float64(amountCents) / 100.0,
			Currency: currency,
			Provider: provider,
			Status:   "failed",
		}); enqErr != nil {
			s.logger.WithError(enqErr).WithField("topup_id", topupID).Warn("Failed to enqueue topup_failed event")
			return nil, status.Errorf(codes.Internal, "failed to create checkout: %v", err)
		}
		if commitErr := failTx.Commit(); commitErr != nil {
			s.logger.WithError(commitErr).WithField("topup_id", topupID).Warn("Failed to commit topup_failed tx")
		}
		return nil, status.Errorf(codes.Internal, "failed to create checkout: %v", err)
	}

	createdTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin topup created tx: %v", err)
	}
	defer createdTx.Rollback() //nolint:errcheck // rollback is best-effort post-commit
	createdQueries := purserdb.New(createdTx)
	if err := createdQueries.OpenPrepaidTopupProviderIntent(ctx, purserdb.OpenPrepaidTopupProviderIntentParams{
		SessionID: sql.NullString{String: result.SessionID, Valid: result.SessionID != ""},
		ExpiresAt: sql.NullTime{Time: result.ExpiresAt, Valid: true}, IntentID: providerIntentID,
	}); err != nil {
		s.logger.WithError(err).WithField("intent_id", providerIntentID).Error("Failed to attach provider session to top-up intent")
		return nil, status.Error(codes.Internal, "failed to attach checkout to payment intent")
	}
	if _, err := createdQueries.AttachCheckoutToPendingTopup(ctx, purserdb.AttachCheckoutToPendingTopupParams{
		SessionID: sql.NullString{String: result.SessionID, Valid: result.SessionID != ""},
		ExpiresAt: result.ExpiresAt, TopupID: topupID,
	}); err != nil {
		s.logger.WithError(err).WithField("checkout_id", result.SessionID).Error("Failed to attach provider checkout to topup")
		return nil, status.Error(codes.Internal, "failed to attach checkout to topup")
	}
	if _, err := s.EnqueueBillingEventTx(ctx, createdTx, eventTopupCreated, tenantID, userID, "topup", topupID, &ipcpb.BillingEvent{
		TopupId:  topupID,
		Amount:   float64(amountCents) / 100.0,
		Currency: currency,
		Provider: provider,
		Status:   "pending",
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "enqueue topup_created: %v", err)
	}
	if err := createdTx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit topup created tx: %v", err)
	}

	return &purserpb.CreateCardTopupResponse{
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
func (s *PurserServer) GetPendingTopup(ctx context.Context, req *purserpb.GetPendingTopupRequest) (*purserpb.PendingTopup, error) {
	queries := purserdb.New(s.db)
	var topup *purserpb.PendingTopup
	var err error
	if req.GetTopupId() != "" {
		var row purserdb.GetPendingTopupByIDRow
		row, err = queries.GetPendingTopupByID(ctx, req.GetTopupId())
		topup = pendingTopupFromFields(row.ID, row.TenantID, row.Provider, row.CheckoutID, row.AmountCents,
			row.Currency, row.Status, row.ExpiresAt, row.CompletedAt, row.BalanceTransactionID, row.CreatedAt, row.UpdatedAt)
	} else if req.GetCheckoutId() != "" && req.GetProvider() != "" {
		var row purserdb.GetPendingTopupByCheckoutRow
		row, err = queries.GetPendingTopupByCheckout(ctx, purserdb.GetPendingTopupByCheckoutParams{
			Provider: req.GetProvider(), CheckoutID: sql.NullString{String: req.GetCheckoutId(), Valid: true},
		})
		topup = pendingTopupFromFields(row.ID, row.TenantID, row.Provider, row.CheckoutID, row.AmountCents,
			row.Currency, row.Status, row.ExpiresAt, row.CompletedAt, row.BalanceTransactionID, row.CreatedAt, row.UpdatedAt)
	} else {
		return nil, status.Error(codes.InvalidArgument, "topup_id or (provider + checkout_id) required")
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "topup not found")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to get topup")
	}

	return topup, nil
}

// ListPendingTopups returns a list of top-ups for a tenant
func (s *PurserServer) ListPendingTopups(ctx context.Context, req *purserpb.ListPendingTopupsRequest) (*purserpb.ListPendingTopupsResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	params := purserdb.ListPendingTopupsParams{TenantID: tenantID}
	if req.Status != nil && *req.Status != "" {
		params.FilterStatus = true
		params.Status = *req.Status
	}
	rows, err := purserdb.New(s.db).ListPendingTopups(ctx, params)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to list topups")
	}
	topups := make([]*purserpb.PendingTopup, 0, len(rows))
	for _, row := range rows {
		topups = append(topups, pendingTopupFromFields(row.ID, row.TenantID, row.Provider, row.CheckoutID, row.AmountCents,
			row.Currency, row.Status, row.ExpiresAt, row.CompletedAt, row.BalanceTransactionID, row.CreatedAt, row.UpdatedAt))
	}

	return &purserpb.ListPendingTopupsResponse{
		Topups: topups,
	}, nil
}

func pendingTopupFromFields(
	id, tenantID uuid.UUID,
	provider, checkoutID string,
	amountCents int64,
	currency, topupStatus string,
	expiresAt time.Time,
	completedAt sql.NullTime,
	balanceTransactionID uuid.NullUUID,
	createdAt, updatedAt sql.NullTime,
) *purserpb.PendingTopup {
	topup := &purserpb.PendingTopup{
		Id: id.String(), TenantId: tenantID.String(), Provider: provider, CheckoutId: checkoutID,
		AmountCents: amountCents, Currency: currency, Status: topupStatus,
		ExpiresAt: timestamppb.New(expiresAt), CreatedAt: timestamppb.New(createdAt.Time), UpdatedAt: timestamppb.New(updatedAt.Time),
	}
	if completedAt.Valid {
		topup.CompletedAt = timestamppb.New(completedAt.Time)
	}
	if balanceTransactionID.Valid {
		value := balanceTransactionID.UUID.String()
		topup.BalanceTransactionId = &value
	}
	return topup
}

// ============================================================================
// CRYPTO TOP-UP
// ============================================================================

// defaultNetworkForAsset returns the network on which an invoice payment or
// prepaid top-up of the given asset is settled. Arbitrum across the board:
// cheap, fast, has the Chainlink ETH/USD feed and USDC.
func defaultNetworkForAsset(asset string) string {
	switch asset {
	case "ETH", "USDC":
		return "arbitrum"
	default:
		return ""
	}
}

func (s *PurserServer) buildDepositQuote(ctx context.Context, network handlers.NetworkConfig, asset, currency string, amountCents int64, tokenDecimals int32) (*handlers.DepositQuote, *big.Int, error) {
	priceQuote, err := s.priceFeed.GetAssetUSDPrice(ctx, network, asset)
	if err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"asset":   asset,
			"network": network.Name,
		}).Error("Failed to read asset USD price")
		return nil, nil, fmt.Errorf("price feed unavailable for %s: %w", asset, err)
	}

	rate, rateErr := handlers.GetEurUsdRate(s.logger)
	if rateErr != nil {
		return nil, nil, fmt.Errorf("EUR rate unavailable: %w", rateErr)
	}
	rateDec := decimal.NewFromFloat(rate)
	quotedUSDToEURRate := &rateDec
	usdCents := decimal.NewFromInt(amountCents)
	if currency == "EUR" {
		usdCents = decimal.NewFromInt(amountCents).Div(rateDec)
	}

	amountUSD := usdCents.Div(decimal.NewFromInt(100))
	amountToken := amountUSD.Div(priceQuote.PriceUSD)
	expectedBaseUnitsDec := amountToken.Mul(decimal.New(1, tokenDecimals))
	expectedBaseUnits := expectedBaseUnitsDec.Ceil().BigInt()
	if expectedBaseUnits.Sign() <= 0 {
		return nil, nil, fmt.Errorf("computed deposit amount is zero")
	}

	return &handlers.DepositQuote{
		ExpectedAmountBaseUnits: expectedBaseUnits,
		QuotedPriceUSD:          priceQuote.PriceUSD,
		QuotedUSDToEURRate:      quotedUSDToEURRate,
		QuotedAt:                time.Now(),
		QuoteSource:             priceQuote.Source,
		CreditedAmountCurrency:  currency,
	}, expectedBaseUnits, nil
}

// CreateCryptoTopup generates an HD-derived deposit address for prepaid balance top-up.
// This is the agent-friendly payment method - no human-in-the-loop required.
//
// The price quote is locked at this call: we read the Chainlink USD price for
// non-USDC assets, persist it on the wallet row, and credit
// `received_amount × locked_price` when the deposit confirms. The user is
// quoted an exact token amount to send.
func (s *PurserServer) CreateCryptoTopup(ctx context.Context, req *purserpb.CreateCryptoTopupRequest) (*purserpb.CreateCryptoTopupResponse, error) {
	if !config.CryptoDepositsEnabled() {
		return nil, status.Error(codes.Unavailable, "new crypto deposits are temporarily disabled; existing deposits continue reconciling")
	}
	tenantID := req.GetTenantId()
	expectedAmountCents := req.GetExpectedAmountCents()
	asset := req.GetAsset()
	currency := req.GetCurrency()

	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if expectedAmountCents < billing.CryptoTopupFloorCents {
		return nil, status.Errorf(codes.InvalidArgument, "minimum crypto top-up is %d cent", billing.CryptoTopupFloorCents)
	}
	if expectedAmountCents > billing.MaximumTopupCents {
		return nil, status.Errorf(codes.InvalidArgument, "maximum top-up is %d cents", billing.MaximumTopupCents)
	}
	if currency == "" {
		currency = billing.DefaultCurrency()
	}
	normalizedCurrency := strings.ToUpper(currency)
	if normalizedCurrency != "USD" && normalizedCurrency != "EUR" {
		return nil, status.Errorf(codes.InvalidArgument, "unsupported prepaid currency for crypto top-up: %s (USD or EUR only)", normalizedCurrency)
	}
	userID := middleware.GetUserID(ctx)

	var assetStr, assetSymbol string
	switch asset {
	case purserpb.CryptoAsset_CRYPTO_ASSET_ETH:
		assetStr, assetSymbol = "ETH", "ETH"
	case purserpb.CryptoAsset_CRYPTO_ASSET_USDC:
		assetStr, assetSymbol = "USDC", "USDC"
	case purserpb.CryptoAsset_CRYPTO_ASSET_LPT:
		return nil, status.Error(codes.InvalidArgument, "LPT prepaid top-ups are not yet supported")
	default:
		return nil, status.Error(codes.InvalidArgument, "asset must be ETH or USDC")
	}

	networkName := defaultNetworkForAsset(assetStr)
	if networkName == "" {
		return nil, status.Errorf(codes.InvalidArgument, "no default network for asset %s", assetStr)
	}
	network, ok := handlers.Networks[networkName]
	if !ok {
		return nil, status.Errorf(codes.Internal, "unknown network %q", networkName)
	}
	if err := s.cryptoDepositReadiness(ctx); err != nil {
		return nil, status.Errorf(codes.Unavailable, "new crypto deposits are not ready: %v", err)
	}
	tokenDecimals, ok := handlers.TokenDecimals(assetStr)
	if !ok {
		return nil, status.Errorf(codes.Internal, "unknown token decimals for %s", assetStr)
	}

	quote, expectedBaseUnits, err := s.buildDepositQuote(ctx, network, assetStr, normalizedCurrency, expectedAmountCents, tokenDecimals)
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	amountEurCents := expectedAmountCents
	if normalizedCurrency == "USD" {
		if quote.QuotedUSDToEURRate == nil {
			return nil, status.Error(codes.Unavailable, "EUR tax conversion rate unavailable")
		}
		amountEurCents = decimal.NewFromInt(expectedAmountCents).Mul(*quote.QuotedUSDToEURRate).Round(0).IntPart()
	}
	documentRequirement, requirementErr := s.x402handler.GetCryptoDocumentRequirement(ctx, tenantID, amountEurCents)
	if requirementErr != nil {
		return nil, status.Errorf(codes.Unavailable, "determine crypto tax-document requirement: %v", requirementErr)
	}
	if documentRequirement.RequiresCompleteProfile {
		return nil, cryptoBillingProfileRequiredError(documentRequirement)
	}
	quote.TaxDocumentKind = documentRequirement.DocumentKind
	quote.TaxProfile = documentRequirement.Profile

	now := time.Now()
	expiresAt := now.Add(24 * time.Hour)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin crypto topup tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort post-commit

	walletID, address, err := s.hdwallet.GenerateDepositAddressTx(ctx, tx, handlers.DepositAddressParams{
		TenantID:            tenantID,
		Purpose:             "prepaid",
		Asset:               assetStr,
		Network:             networkName,
		ExpiresAt:           expiresAt,
		ExpectedAmountCents: &expectedAmountCents,
		ClientIP:            req.GetClientIp(),
		Quote:               quote,
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to generate deposit address")
		return nil, status.Errorf(codes.Internal, "failed to generate deposit address: %v", err)
	}

	// Re-derive token amount from the ceiled base units so the displayed
	// "send exactly X" matches what the monitor compares against.
	expectedAmountToken := decimal.NewFromBigInt(expectedBaseUnits, -tokenDecimals).StringFixedBank(tokenDecimals)

	if _, err := s.EnqueueBillingEventTx(ctx, tx, eventTopupCreated, tenantID, userID, "topup", walletID, &ipcpb.BillingEvent{
		TopupId:  walletID,
		Amount:   float64(expectedAmountCents) / 100.0,
		Currency: normalizedCurrency,
		Provider: "crypto",
		Status:   "pending",
		Asset:    assetStr,
		Network:  networkName,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "enqueue crypto topup_created: %v", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit crypto topup tx: %v", err)
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":             tenantID,
		"wallet_id":             walletID,
		"asset":                 assetStr,
		"network":               networkName,
		"address":               address,
		"expected_cents":        expectedAmountCents,
		"expected_amount_token": expectedAmountToken,
		"quoted_price_usd":      quote.QuotedPriceUSD.String(),
		"quote_source":          quote.QuoteSource,
	}).Info("Created crypto top-up deposit address")

	return &purserpb.CreateCryptoTopupResponse{
		TopupId:                 walletID,
		DepositAddress:          address,
		Asset:                   asset,
		AssetSymbol:             assetSymbol,
		ExpectedAmountCents:     expectedAmountCents,
		ExpiresAt:               timestamppb.New(expiresAt),
		ExpectedAmountBaseUnits: expectedBaseUnits.String(),
		ExpectedAmountToken:     expectedAmountToken,
		QuotedPriceUsd:          quote.QuotedPriceUSD.String(),
		QuoteSource:             quote.QuoteSource,
		QuotedAt:                timestamppb.New(quote.QuotedAt),
		Network:                 networkName,
	}, nil
}

// GetCryptoTopup returns the status of a crypto top-up
func (s *PurserServer) GetCryptoTopup(ctx context.Context, req *purserpb.GetCryptoTopupRequest) (*purserpb.CryptoTopup, error) {
	topupID := req.GetTopupId()
	if topupID == "" {
		return nil, status.Error(codes.InvalidArgument, "topup_id is required")
	}
	ctxTenantID := middleware.GetTenantID(ctx)
	isServiceCall := middleware.IsServiceCall(ctx)
	if !isServiceCall && ctxTenantID == "" {
		return nil, status.Error(codes.PermissionDenied, "tenant context required")
	}

	row, err := purserdb.New(s.db).GetPrepaidCryptoTopup(ctx, purserdb.GetPrepaidCryptoTopupParams{
		TopupID: topupID, EnforceTenant: ctxTenantID != "", TenantID: ctxTenantID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "crypto topup not found")
	}
	if err != nil {
		s.logger.WithError(err).Error("Failed to get crypto topup")
		return nil, status.Error(codes.Internal, "failed to get crypto topup")
	}

	// API-facing expired state for rows that the expiry sweep hasn't yet
	// reached: pending|confirming past expires_at look 'expired' to clients.
	topup := purserpb.CryptoTopup{
		Id: row.ID, TenantId: row.TenantID, DepositAddress: row.WalletAddress,
		AssetSymbol: row.Asset, ExpectedAmountCents: row.ExpectedAmountCents.Int64,
		Status: row.Status, Confirmations: row.Confirmations,
		ExpiresAt: timestamppb.New(row.ExpiresAt), CreatedAt: timestamppb.New(row.CreatedAt.Time),
	}
	if (topup.Status == "pending" || topup.Status == "confirming") && row.ExpiresAt.Before(time.Now()) {
		topup.Status = "expired"
	}

	switch topup.AssetSymbol {
	case "ETH":
		topup.Asset = purserpb.CryptoAsset_CRYPTO_ASSET_ETH
	case "USDC":
		topup.Asset = purserpb.CryptoAsset_CRYPTO_ASSET_USDC
	case "LPT":
		topup.Asset = purserpb.CryptoAsset_CRYPTO_ASSET_LPT
	}

	topup.Currency = billing.DefaultCurrency()
	if row.CreditedAmountCurrency.Valid && row.CreditedAmountCurrency.String != "" {
		topup.Currency = row.CreditedAmountCurrency.String
		topup.CreditedAmountCurrency = row.CreditedAmountCurrency.String
	}
	if row.TxHash.Valid {
		topup.TxHash = row.TxHash.String
	}
	if row.ReceivedAmountBaseUnits != "" {
		topup.ReceivedAmountBaseUnits = row.ReceivedAmountBaseUnits
		if dec, decErr := decimal.NewFromString(row.ReceivedAmountBaseUnits); decErr == nil {
			if td, ok := handlers.TokenDecimals(topup.AssetSymbol); ok {
				topup.ReceivedAmountToken = dec.Shift(-td).String()
			}
		}
	}
	if row.CreditedAmountCents.Valid {
		topup.CreditedAmountCents = row.CreditedAmountCents.Int64
	}
	if row.QuoteSource.Valid {
		topup.QuoteSource = row.QuoteSource.String
	}
	if row.Network != "" {
		topup.Network = row.Network
	}
	if row.DetectedAt.Valid {
		topup.DetectedAt = timestamppb.New(row.DetectedAt.Time)
	}
	if row.CompletedAt.Valid {
		topup.CompletedAt = timestamppb.New(row.CompletedAt.Time)
	}

	return &topup, nil
}

// PromoteToPaid upgrades a prepaid account to postpaid. When req.tier_id is
// provided it must reference an active postpaid tier (tier_level >= 1, not
// is_default_prepaid); empty selects is_default_postpaid as the floor.
// Prepaid balance is carried forward as credit.
func (s *PurserServer) PromoteToPaid(ctx context.Context, req *purserpb.PromoteToPaidRequest) (*purserpb.PromoteToPaidResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to begin transaction: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	queries := purserdb.New(tx)
	subscription, err := queries.LockTenantSubscriptionForPromotion(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "no subscription found for tenant")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to lock subscription: %v", err)
	}

	requestedTierID := strings.TrimSpace(req.GetTierId())
	if subscription.BillingModel == "postpaid" {
		if requestedTierID != "" && requestedTierID != subscription.TierID {
			return nil, status.Error(codes.FailedPrecondition, "already postpaid on a different tier; use changeBillingTier")
		}
		requestedTierID = subscription.TierID
	}

	tier, err := queries.GetPostpaidPromotionTier(ctx, purserdb.GetPostpaidPromotionTierParams{
		HasRequestedTier: requestedTierID != "", RequestedTierID: requestedTierID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		if requestedTierID != "" {
			return nil, status.Error(codes.NotFound, "tier not found")
		}
		return nil, status.Error(codes.FailedPrecondition, "no default postpaid billing tier configured")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to resolve tier: %v", err)
	}
	if !tier.IsActive.Bool || tier.IsDefaultPrepaid.Bool || tier.TierLevel.Int32 < 1 {
		return nil, status.Error(codes.FailedPrecondition, "tier is not active and postpaid-eligible")
	}
	if !strings.EqualFold(tier.TierName, "free") {
		address := scanBillingAddress(subscription.BillingAddress)
		profileComplete := subscription.BillingName.Valid && strings.TrimSpace(subscription.BillingName.String) != "" &&
			subscription.BillingEmail.Valid && strings.TrimSpace(subscription.BillingEmail.String) != "" && address != nil &&
			address.Street != "" && address.City != "" && address.PostalCode != "" && address.Country != ""
		if !profileComplete {
			return nil, status.Error(codes.FailedPrecondition, "customer legal name, billing email, and postal address are required for this operation")
		}
		providerReady := subscription.PaymentMethod.String == "stripe" && subscription.StripeSubscriptionID.Valid && subscription.StripeSubscriptionID.String != "" ||
			subscription.PaymentMethod.String == "mollie" && subscription.MollieSubscriptionID.Valid && subscription.MollieSubscriptionID.String != ""
		if !providerReady {
			return nil, status.Error(codes.FailedPrecondition, "complete Stripe or Mollie subscription setup before enabling paid postpaid billing")
		}
	}

	if subscription.BillingModel == "prepaid" {
		_, err = queries.PromotePrepaidTenantSubscription(ctx, purserdb.PromotePrepaidTenantSubscriptionParams{
			TierID: tier.ID, TenantID: tenantID,
		})
		if err != nil {
			return nil, status.Errorf(codes.Aborted, "subscription changed during promotion: %v", err)
		}
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, status.Errorf(codes.Internal, "failed to commit: %v", commitErr)
	}

	canonical, err := purserdb.New(s.db).GetCanonicalPromotedSubscription(ctx, purserdb.GetCanonicalPromotedSubscriptionParams{
		Currency: billing.DefaultCurrency(), TenantID: tenantID,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "promotion committed but canonical subscription read failed: %v", err)
	}
	if canonical.BillingModel != "postpaid" {
		return nil, status.Error(codes.Aborted, "promotion did not converge to postpaid billing")
	}
	eligibleClusters, primaryCluster, clusterErr := s.reconcileCanonicalTierClusterAccess(ctx, tenantID)
	if clusterErr != nil {
		s.logger.WithError(clusterErr).WithField("tenant_id", tenantID).Error("Failed to re-evaluate cluster access after promotion")
		return nil, status.Errorf(codes.Internal, "promoted to postpaid but failed to reconcile cluster access: %v", clusterErr)
	}

	// Invalidate downstream caches so the new tier's caps + allowance state
	// propagate to Foghorn's streamContext on the next admission. Same
	// mechanism as UpdateSubscription / balance-change paths.
	if invErr := s.invalidateTenantCache(ctx, tenantID, "tier_changed"); invErr != nil {
		s.logger.WithError(invErr).WithField("tenant_id", tenantID).Error("Failed to invalidate tenant cache on promote-to-paid")
		return nil, status.Errorf(codes.Internal, "promoted to postpaid but failed to invalidate tenant cache: %v", invErr)
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":      tenantID,
		"tier_id":        canonical.TierID,
		"tier_level":     canonical.TierLevel.Int32,
		"credit_balance": canonical.BalanceCents,
	}).Info("Tenant promoted from prepaid to postpaid")

	return &purserpb.PromoteToPaidResponse{
		Success:            true,
		Message:            "Switched to postpaid billing",
		NewBillingModel:    "postpaid",
		CreditBalanceCents: canonical.BalanceCents,
		SubscriptionId:     canonical.SubscriptionID,
		TierLevel:          canonical.TierLevel.Int32,
		EligibleClusterIds: eligibleClusters,
		PrimaryClusterId:   primaryCluster,
	}, nil
}

// ChangeBillingTier swaps a postpaid tenant's tier. Upgrades apply
// immediately and reconcile cluster access. Downgrades stage
// pending_tier_id/pending_effective_at and are applied by the billing-close
// job after the current invoice clears — this avoids cutting paid
// entitlements mid-period.
//
// Prepaid → postpaid transitions belong on PromoteToPaid (which carries the
// prepaid balance forward as credit). Returning to prepaid is not supported.
func (s *PurserServer) ChangeBillingTier(ctx context.Context, req *purserpb.ChangeBillingTierRequest) (*purserpb.ChangeBillingTierResponse, error) {
	tenantID := req.GetTenantId()
	targetTierID := req.GetTierId()
	if tenantID == "" || targetTierID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and tier_id are required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin tier change: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	queries := purserdb.New(tx)
	current, err := queries.LockTenantSubscriptionForTierChange(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "no subscription found for tenant")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load subscription: %v", err)
	}
	if current.BillingModel != "postpaid" {
		return nil, status.Error(codes.FailedPrecondition, "ChangeBillingTier requires postpaid billing; use PromoteToPaid for prepaid → postpaid")
	}

	target, err := queries.GetTierForBillingChange(ctx, targetTierID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "target tier not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load target tier: %v", err)
	}
	if !target.IsActive.Bool {
		return nil, status.Error(codes.FailedPrecondition, "target tier is inactive")
	}
	if target.IsDefaultPrepaid.Bool || target.TierLevel.Int32 < 1 {
		return nil, status.Error(codes.FailedPrecondition, "target tier is not postpaid-eligible")
	}
	if !strings.EqualFold(target.TierName, "free") {
		collection, collectionErr := queries.GetPostpaidCollectionSetup(ctx, tenantID)
		if collectionErr != nil {
			return nil, status.Errorf(codes.Internal, "verify postpaid collection setup: %v", collectionErr)
		}
		address := scanBillingAddress(collection.BillingAddress)
		profileComplete := collection.BillingName.Valid && strings.TrimSpace(collection.BillingName.String) != "" &&
			collection.BillingEmail.Valid && strings.TrimSpace(collection.BillingEmail.String) != "" && address != nil &&
			address.Street != "" && address.City != "" && address.PostalCode != "" && address.Country != ""
		if !profileComplete {
			return nil, status.Error(codes.FailedPrecondition, "customer legal name, billing email, and postal address are required before selecting a paid tier")
		}
		collectionReady := collection.PaymentMethod.String == "stripe" && collection.StripeSubscriptionID.Valid && collection.StripeSubscriptionID.String != "" ||
			collection.PaymentMethod.String == "mollie" && collection.MollieSubscriptionID.Valid && collection.MollieSubscriptionID.String != ""
		if !collectionReady {
			return nil, status.Error(codes.FailedPrecondition, "complete Stripe or Mollie subscription setup before selecting a paid tier")
		}
	}

	now := time.Now()
	resolvedPeriodStart, resolvedPeriodEnd, periodErr := resolveBillingPeriod(ctx, tx, tenantID, current.BillingPeriodStart, current.BillingPeriodEnd, now)
	if periodErr != nil {
		return nil, status.Errorf(codes.Internal, "resolve billing period: %v", periodErr)
	}

	if targetTierID == current.TierID {
		if periodUpdateErr := queries.BackfillTenantBillingPeriod(ctx, purserdb.BackfillTenantBillingPeriodParams{
			PeriodStart: sql.NullTime{Time: resolvedPeriodStart, Valid: true},
			PeriodEnd:   sql.NullTime{Time: resolvedPeriodEnd, Valid: true}, TenantID: tenantID,
		}); periodUpdateErr != nil {
			return nil, status.Errorf(codes.Internal, "backfill billing period: %v", periodUpdateErr)
		}
		if err := tx.Commit(); err != nil {
			return nil, status.Errorf(codes.Internal, "commit unchanged tier: %v", err)
		}
		eligibleClusters, primaryCluster, clusterErr := s.reconcileCanonicalTierClusterAccess(ctx, tenantID)
		if clusterErr != nil {
			s.logger.WithError(clusterErr).WithField("tenant_id", tenantID).Error("reconcile cluster access for unchanged tier")
			return nil, status.Errorf(codes.Internal, "failed to reconcile cluster access for current tier: %v", clusterErr)
		}
		if invErr := s.invalidateTenantCache(ctx, tenantID, "tier_changed"); invErr != nil {
			s.logger.WithError(invErr).WithField("tenant_id", tenantID).Error("invalidate tenant cache for unchanged tier")
			return nil, status.Errorf(codes.Internal, "failed to invalidate tenant cache: %v", invErr)
		}
		return &purserpb.ChangeBillingTierResponse{
			Success:            true,
			Message:            "Already on requested tier",
			AppliedTierId:      current.TierID,
			EligibleClusterIds: eligibleClusters,
			PrimaryClusterId:   primaryCluster,
		}, nil
	}

	if target.TierLevel.Int32 >= current.TierLevel.Int32 {
		// UPGRADE — apply immediately. Cluster reconcile + cache invalidation
		// happen after the DB transaction commits.
		if err := queries.ApplyTenantTierUpgrade(ctx, purserdb.ApplyTenantTierUpgradeParams{
			TierID:      targetTierID,
			PeriodStart: sql.NullTime{Time: resolvedPeriodStart, Valid: true},
			PeriodEnd:   sql.NullTime{Time: resolvedPeriodEnd, Valid: true}, TenantID: tenantID,
		}); err != nil {
			return nil, status.Errorf(codes.Internal, "update subscription tier: %v", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, status.Errorf(codes.Internal, "commit tier upgrade: %v", err)
		}

		eligibleClusters, primaryCluster, clusterErr := s.reconcileCanonicalTierClusterAccess(ctx, tenantID)
		if clusterErr != nil {
			s.logger.WithError(clusterErr).WithField("tenant_id", tenantID).Error("reconcile cluster access after tier change")
			return nil, status.Errorf(codes.Internal, "tier changed but failed to reconcile cluster access: %v", clusterErr)
		}
		if invErr := s.invalidateTenantCache(ctx, tenantID, "tier_changed"); invErr != nil {
			s.logger.WithError(invErr).WithField("tenant_id", tenantID).Error("invalidate tenant cache after tier change")
			return nil, status.Errorf(codes.Internal, "tier changed but failed to invalidate tenant cache: %v", invErr)
		}

		s.logger.WithFields(logging.Fields{
			"tenant_id":  tenantID,
			"from_tier":  current.TierID,
			"to_tier":    targetTierID,
			"tier_level": target.TierLevel.Int32,
		}).Info("Billing tier upgraded")

		return &purserpb.ChangeBillingTierResponse{
			Success:            true,
			Message:            "Tier upgrade applied",
			AppliedTierId:      targetTierID,
			EligibleClusterIds: eligibleClusters,
			PrimaryClusterId:   primaryCluster,
		}, nil
	}

	// DOWNGRADE — stage for end of period. The post-commit applier in
	// jobs.go flips tier_id and reconciles after the period's invoice clears.
	var effective time.Time
	if current.StripeCurrentPeriodEnd.Valid {
		effective = current.StripeCurrentPeriodEnd.Time
	} else if current.BillingPeriodEnd.Valid {
		effective = current.BillingPeriodEnd.Time
	} else {
		effective = resolvedPeriodEnd
	}
	if effective.IsZero() {
		return nil, status.Error(codes.FailedPrecondition, "subscription has no billing_period_end; cannot schedule downgrade")
	}
	if !resolvedPeriodStart.Before(effective) {
		resolvedPeriodStart = effective.AddDate(0, -1, 0)
	}

	if err := queries.ScheduleTenantTierDowngrade(ctx, purserdb.ScheduleTenantTierDowngradeParams{
		TierID: targetTierID, EffectiveAt: sql.NullTime{Time: effective, Valid: true},
		PeriodStart: sql.NullTime{Time: resolvedPeriodStart, Valid: true},
		PeriodEnd:   sql.NullTime{Time: effective, Valid: true}, TenantID: tenantID,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "schedule downgrade: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit scheduled downgrade: %v", err)
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":    tenantID,
		"from_tier":    current.TierID,
		"to_tier":      targetTierID,
		"effective_at": effective,
	}).Info("Billing tier downgrade scheduled")

	return &purserpb.ChangeBillingTierResponse{
		Success:       true,
		Message:       "Downgrade scheduled for end of current billing period",
		PendingTierId: targetTierID,
		EffectiveAt:   timestamppb.New(effective),
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

func currentBillingPeriod(now time.Time) (time.Time, time.Time) {
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	return start, start.AddDate(0, 1, 0)
}

func resolveBillingPeriod(ctx context.Context, queryer purserdb.DBTX, tenantID string, start, end sql.NullTime, now time.Time) (time.Time, time.Time, error) {
	if start.Valid && end.Valid && end.Time.After(start.Time) {
		return start.Time, end.Time, nil
	}

	invoice, err := purserdb.New(queryer).GetOpenInvoiceBillingPeriod(ctx, purserdb.GetOpenInvoiceBillingPeriodParams{
		TenantID: tenantID, Now: sql.NullTime{Time: now, Valid: true},
	})
	if err == nil && invoice.PeriodStart.Valid && invoice.PeriodEnd.Valid && invoice.PeriodEnd.Time.After(invoice.PeriodStart.Time) {
		return invoice.PeriodStart.Time, invoice.PeriodEnd.Time, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, time.Time{}, fmt.Errorf("load open invoice period: %w", err)
	}

	periodStart, periodEnd := currentBillingPeriod(now)
	return periodStart, periodEnd, nil
}

// Ensure unused imports don't cause errors
// ============================================================================
// WEBHOOK SERVICE IMPLEMENTATION
// ============================================================================

// ProcessWebhook handles incoming webhooks from the Gateway.
// The Gateway packages raw HTTP (body + headers) into a WebhookRequest and
// routes it here via gRPC. Signature verification happens in the handlers.
func (s *PurserServer) ProcessWebhook(ctx context.Context, req *sharedpb.WebhookRequest) (*sharedpb.WebhookResponse, error) {
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
		success, errMsg, statusCode = s.billing.ProcessStripeWebhookGRPC(body, headers)
	case "mollie":
		success, errMsg, statusCode = s.billing.ProcessMollieWebhookGRPC(body, headers)
	default:
		s.logger.WithField("provider", provider).Warn("Unknown webhook provider")
		return &sharedpb.WebhookResponse{
			Success:    false,
			Error:      "unknown provider: " + provider,
			StatusCode: 400,
		}, nil
	}

	return &sharedpb.WebhookResponse{
		Success:    success,
		Error:      errMsg,
		StatusCode: int32(statusCode),
	}, nil
}

// ============================================================================
// STRIPE SERVICE IMPLEMENTATION
// ============================================================================

// CreateCheckoutSession creates a Stripe Checkout Session for subscription
func (s *PurserServer) CreateCheckoutSession(ctx context.Context, req *purserpb.CreateStripeCheckoutRequest) (*purserpb.CreateStripeCheckoutResponse, error) {
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
	if err := s.requireCompleteBillingDetails(ctx, tenantID); err != nil {
		return nil, err
	}

	// Get billing tier to find Stripe price ID
	queries := purserdb.New(s.db)
	tier, err := queries.GetStripeTierCheckoutConfig(ctx, purserdb.GetStripeTierCheckoutConfigParams{
		Yearly: billingPeriod == "yearly", TierID: tierID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Errorf(codes.NotFound, "tier not found: %s", tierID)
	}
	if err != nil {
		s.logger.WithError(err).Error("Failed to get billing tier")
		return nil, status.Error(codes.Internal, "failed to get billing tier")
	}
	if tier.PriceID == "" {
		return nil, status.Errorf(codes.FailedPrecondition, "tier %s has no Stripe %s price configured", tier.TierName, billingPeriod)
	}
	currency := strings.ToUpper(tier.Currency)
	if currency == "" {
		currency = billing.DefaultCurrency()
	}

	// Preflight: confirm a tenant_subscriptions row exists and isn't holding
	// an unrelated staged tier change. Read-only so a Commodore / Stripe
	// failure later cannot leak a pending_tier_id write. The actual stage
	// happens after the Stripe session is accepted.
	pending, preflightErr := queries.GetTenantPendingTierState(ctx, tenantID)
	if errors.Is(preflightErr, sql.ErrNoRows) {
		return nil, status.Errorf(codes.FailedPrecondition,
			"no tenant_subscriptions row for tenant %s; bootstrap must seed one before checkout", tenantID)
	}
	if preflightErr != nil {
		s.logger.WithError(preflightErr).Error("Failed to read pending tier state")
		return nil, status.Error(codes.Internal, "failed to read pending tier state")
	}
	if pending.PendingTierID != "" && pending.PendingReason.String != "" && pending.PendingReason.String != "stripe_checkout" {
		return nil, status.Errorf(codes.FailedPrecondition,
			"a %s is already staged for this tenant (pending_tier_id=%s); cancel it before starting a new checkout",
			pending.PendingReason.String, pending.PendingTierID)
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

	// Insert a durable payment_provider_intents row before any external
	// side effect so a crash or timeout between provider calls never leaves
	// a Stripe customer/session without local audit trail. The
	// idempotency_key is deterministic on (tenant, tier) so repeated calls
	// for the same target tier collapse to one intent row.
	intentKey := fmt.Sprintf("stripe-tenant-checkout:%s:%s", tenantID, tierID)
	intentID, intentErr := queries.UpsertStripeTenantCheckoutIntent(ctx, purserdb.UpsertStripeTenantCheckoutIntentParams{
		TenantID: tenantID, TierID: tierID, Currency: currency, IdempotencyKey: intentKey,
	})
	if intentErr != nil {
		s.logger.WithError(intentErr).Error("Failed to record Stripe tenant checkout intent")
		return nil, status.Error(codes.Internal, "failed to record checkout intent")
	}

	// Get or create Stripe customer
	customer, err := s.stripeClient.CreateOrGetCustomer(ctx, stripe.CustomerInfo{
		TenantID:       tenantID,
		Email:          email,
		Name:           name,
		IdempotencyKey: "stripe-customer:" + tenantID,
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to create/get Stripe customer")
		s.markProviderIntentFailed(ctx, intentID, "customer_create_failed", err)
		return nil, status.Error(codes.Internal, "failed to create Stripe customer")
	}
	if customer.ID != "" {
		if updateErr := queries.SetProviderIntentCustomer(ctx, purserdb.SetProviderIntentCustomerParams{
			CustomerID: sql.NullString{String: customer.ID, Valid: true}, IntentID: intentID,
		}); updateErr != nil {
			s.logger.WithError(updateErr).WithField("intent_id", intentID).Warn("Failed to record provider_customer_id")
		}
	}

	// Create checkout session
	sess, err := s.stripeClient.CreateCheckoutSession(ctx, stripe.CheckoutSessionParams{
		CustomerID:     customer.ID,
		TenantID:       tenantID,
		TierID:         tierID,
		Purpose:        "subscription",
		ReferenceID:    tierID,
		PriceID:        tier.PriceID,
		Currency:       currency,
		SuccessURL:     successURL,
		CancelURL:      cancelURL,
		IdempotencyKey: intentKey,
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to create Stripe checkout session")
		s.markProviderIntentFailed(ctx, intentID, "checkout_session_create_failed", err)
		return nil, status.Error(codes.Internal, "failed to create checkout session")
	}
	if sess != nil && sess.ID != "" {
		if updateErr := queries.SetProviderIntentSessionOpen(ctx, purserdb.SetProviderIntentSessionOpenParams{
			SessionID: sql.NullString{String: sess.ID, Valid: true}, IntentID: intentID,
		}); updateErr != nil {
			s.logger.WithError(updateErr).WithField("intent_id", intentID).Warn("Failed to record provider_session_id")
		}
	}

	// Stage the target tier only after Stripe accepts the session so a
	// failed preflight call earlier in this RPC doesn't leak a half-staged
	// tier. The WHERE guard still refuses to overwrite a non-Stripe pending
	// change in case a race opened a downgrade since the preflight read.
	stageRows, stageErr := queries.StageStripeCheckoutTier(ctx, purserdb.StageStripeCheckoutTierParams{
		TierID: tierID, IntentID: intentID, TenantID: tenantID,
	})
	if stageErr != nil {
		s.logger.WithError(stageErr).WithField("session_id", sess.ID).Error("Failed to stage pending tier after Stripe checkout creation")
		if expireErr := s.stripeClient.ExpireCheckoutSession(ctx, sess.ID); expireErr != nil {
			s.logger.WithError(expireErr).WithField("session_id", sess.ID).Error("Failed to expire Stripe checkout session after local staging failure")
			return nil, status.Errorf(codes.Internal, "failed to stage pending tier and failed to expire Stripe checkout session %s: %v", sess.ID, expireErr)
		}
		return nil, status.Error(codes.Internal, "failed to stage pending tier")
	}
	if stageRows == 0 {
		// Race: a non-Stripe pending change appeared between preflight and
		// stage. Expire the live Stripe session so external checkout state
		// cannot advance while Purser refuses to stage the local target tier.
		s.logger.WithFields(logging.Fields{
			"tenant_id":  tenantID,
			"session_id": sess.ID,
		}).Warn("Stripe checkout session created but pending_tier_id stage skipped due to race; expiring session")
		if expireErr := s.stripeClient.ExpireCheckoutSession(ctx, sess.ID); expireErr != nil {
			s.logger.WithError(expireErr).WithField("session_id", sess.ID).Error("Failed to expire Stripe checkout session after pending-tier race")
			return nil, status.Errorf(codes.Internal, "pending tier changed during checkout and failed to expire Stripe checkout session %s: %v", sess.ID, expireErr)
		}
		return nil, status.Error(codes.FailedPrecondition, "pending tier changed during checkout; start a new checkout after resolving the pending change")
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":  tenantID,
		"tier_id":    tierID,
		"session_id": sess.ID,
	}).Info("Created Stripe checkout session")

	return &purserpb.CreateStripeCheckoutResponse{
		CheckoutUrl: sess.URL,
		SessionId:   sess.ID,
	}, nil
}

// CreateBillingPortalSession creates a Stripe Billing Portal session
func (s *PurserServer) CreateBillingPortalSession(ctx context.Context, req *purserpb.CreateBillingPortalRequest) (*purserpb.CreateBillingPortalResponse, error) {
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
	stripeCustomerID, err := purserdb.New(s.db).GetTenantStripeCustomerID(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) || !stripeCustomerID.Valid {
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

	return &purserpb.CreateBillingPortalResponse{
		PortalUrl: sess.URL,
	}, nil
}

// SyncSubscription syncs subscription state from Stripe (admin/debug)
func (s *PurserServer) SyncSubscription(ctx context.Context, req *purserpb.SyncStripeSubscriptionRequest) (*purserpb.TenantSubscription, error) {
	if s.stripeClient == nil {
		return nil, status.Error(codes.Unavailable, "Stripe not configured")
	}

	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	// Get Stripe subscription ID
	stripeSubID, err := purserdb.New(s.db).GetTenantStripeSubscriptionID(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) || !stripeSubID.Valid {
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

	currentPeriodStart := sql.NullTime{}
	if !info.CurrentPeriodStart.IsZero() {
		currentPeriodStart = sql.NullTime{Time: info.CurrentPeriodStart, Valid: true}
	}
	currentPeriodEnd := sql.NullTime{}
	if !info.CurrentPeriodEnd.IsZero() {
		currentPeriodEnd = sql.NullTime{Time: info.CurrentPeriodEnd, Valid: true}
	}

	err = purserdb.New(s.db).UpdateTenantStripeSubscriptionSync(ctx, purserdb.UpdateTenantStripeSubscriptionSyncParams{
		Status:    sql.NullString{String: info.Status, Valid: info.Status != ""},
		PeriodEnd: currentPeriodEnd, PeriodStart: currentPeriodStart, TenantID: tenantID,
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to update subscription from Stripe")
		return nil, status.Error(codes.Internal, "failed to update subscription")
	}

	// Return updated subscription
	resp, err := s.GetSubscription(ctx, &purserpb.GetSubscriptionRequest{TenantId: tenantID})
	if err != nil {
		return nil, err
	}
	return resp.GetSubscription(), nil
}

// ============================================================================
// MOLLIE SERVICE IMPLEMENTATION
// ============================================================================

// CreateFirstPayment creates a Mollie first payment to establish a mandate
func (s *PurserServer) CreateFirstPayment(ctx context.Context, req *purserpb.CreateMollieFirstPaymentRequest) (*purserpb.CreateMollieFirstPaymentResponse, error) {
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
	switch method {
	case "ideal", "creditcard", "bancontact":
	default:
		return nil, status.Error(codes.InvalidArgument, "method must be ideal, creditcard, or bancontact")
	}
	if redirectURL == "" {
		return nil, status.Error(codes.InvalidArgument, "redirect_url is required")
	}
	if err := s.requireCompleteBillingDetails(ctx, tenantID); err != nil {
		return nil, err
	}

	// Get tier price
	queries := purserdb.New(s.db)
	tier, err := queries.GetMollieTierPrice(ctx, tierID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Errorf(codes.NotFound, "tier not found: %s", tierID)
	}
	if err != nil {
		s.logger.WithError(err).Error("Failed to get billing tier")
		return nil, status.Error(codes.Internal, "failed to get billing tier")
	}
	basePrice, err := decimal.NewFromString(tier.BasePrice)
	if err != nil {
		s.logger.WithError(err).WithField("tier_id", tierID).Error("Failed to parse billing tier base price")
		return nil, status.Error(codes.Internal, "failed to parse billing tier price")
	}

	firstPaymentIntentKey := fmt.Sprintf("mollie-first-payment:%s:%s", tenantID, tierID)
	firstPaymentIntentID, intentErr := queries.UpsertMollieFirstPaymentIntent(ctx, purserdb.UpsertMollieFirstPaymentIntentParams{
		TenantID: tenantID, TierID: tierID, Currency: tier.Currency,
		AmountCents: basePrice.Round(2).Shift(2).IntPart(), IdempotencyKey: firstPaymentIntentKey,
	})
	if intentErr != nil {
		s.logger.WithError(intentErr).Error("Failed to record Mollie first-payment intent")
		return nil, status.Error(codes.Internal, "failed to record first-payment intent")
	}

	// Get or create Mollie customer
	mollieCustomerID, err := queries.GetMollieCustomerID(ctx, tenantID)

	if errors.Is(err, sql.ErrNoRows) {
		// Get tenant primary user info via Commodore gRPC (not direct DB access)
		primaryUser, primaryErr := s.commodoreClient.GetTenantPrimaryUser(ctx, tenantID)
		if primaryErr != nil {
			if status.Code(primaryErr) == codes.NotFound {
				s.markProviderIntentFailed(ctx, firstPaymentIntentID, "missing_billing_email", primaryErr)
				return nil, status.Error(codes.FailedPrecondition, "no billing email on account")
			}
			s.logger.WithFields(logging.Fields{
				"tenant_id": tenantID,
				"error":     primaryErr,
			}).Error("Failed to get tenant primary user from Commodore")
			s.markProviderIntentFailed(ctx, firstPaymentIntentID, "tenant_primary_user_lookup_failed", primaryErr)
			return nil, status.Error(codes.Internal, "failed to get tenant info")
		}
		email := primaryUser.Email
		name := primaryUser.Name
		if name == "" {
			name = email
		}

		customer, customerErr := s.mollieClient.CreateOrGetCustomer(ctx, mollie.CustomerInfo{
			TenantID:       tenantID,
			Email:          email,
			Name:           name,
			IdempotencyKey: "mollie-customer:" + tenantID,
		})
		if customerErr != nil {
			s.logger.WithError(customerErr).Error("Failed to create Mollie customer")
			s.markProviderIntentFailed(ctx, firstPaymentIntentID, "customer_create_failed", customerErr)
			return nil, status.Error(codes.Internal, "failed to create Mollie customer")
		}

		// Store customer mapping
		err = queries.UpsertMollieCustomer(ctx, purserdb.UpsertMollieCustomerParams{
			TenantID: tenantID, MollieCustomerID: customer.ID,
		})
		if err != nil {
			s.logger.WithError(err).Error("Failed to store Mollie customer mapping")
			s.markProviderIntentFailed(ctx, firstPaymentIntentID, "customer_mapping_failed", err)
			return nil, status.Error(codes.Internal, "failed to store customer mapping")
		}

		mollieCustomerID = customer.ID
	} else if err != nil {
		s.logger.WithError(err).Error("Failed to get Mollie customer")
		s.markProviderIntentFailed(ctx, firstPaymentIntentID, "customer_lookup_failed", err)
		return nil, status.Error(codes.Internal, "failed to get Mollie customer")
	}

	if linkErr := queries.LinkProviderIntentCustomer(ctx, purserdb.LinkProviderIntentCustomerParams{
		CustomerID: sql.NullString{String: mollieCustomerID, Valid: mollieCustomerID != ""}, IntentID: firstPaymentIntentID,
	}); linkErr != nil {
		s.logger.WithError(linkErr).WithField("intent_id", firstPaymentIntentID).Warn("Failed to link Mollie customer to first-payment intent")
		s.markProviderIntentFailed(ctx, firstPaymentIntentID, "customer_intent_link_failed", linkErr)
		return nil, status.Error(codes.Internal, "failed to link first-payment intent")
	}

	// Build webhook URL (routed through Gateway)
	webhookBaseURL := config.GetGatewayPublicURL()
	webhookURL := ""
	if webhookBaseURL != "" {
		webhookURL = webhookBaseURL + "/webhooks/billing/mollie"
	}

	// Create first payment
	payment, err := s.mollieClient.CreateFirstPayment(ctx, mollie.FirstPaymentParams{
		CustomerID:     mollieCustomerID,
		TenantID:       tenantID,
		TierID:         tierID,
		Amount:         mollie.Amount(basePrice.Round(2).StringFixed(2), tier.Currency),
		Description:    fmt.Sprintf("Subscription setup: %s", tier.TierName),
		Method:         getMolliePaymentMethod(method),
		RedirectURL:    redirectURL,
		WebhookURL:     webhookURL,
		IdempotencyKey: firstPaymentIntentKey,
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to create Mollie first payment")
		s.markProviderIntentFailed(ctx, firstPaymentIntentID, "first_payment_create_failed", err)
		return nil, status.Error(codes.Internal, "failed to create first payment")
	}
	if payment != nil && payment.ID != "" {
		if updateErr := queries.SetProviderIntentPaymentOpen(ctx, purserdb.SetProviderIntentPaymentOpenParams{
			PaymentID: sql.NullString{String: payment.ID, Valid: true}, IntentID: firstPaymentIntentID,
		}); updateErr != nil {
			s.logger.WithError(updateErr).WithField("intent_id", firstPaymentIntentID).Warn("Failed to record first-payment provider_payment_id")
		}
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":   tenantID,
		"tier_id":     tierID,
		"payment_id":  payment.ID,
		"customer_id": mollieCustomerID,
	}).Info("Created Mollie first payment")

	return &purserpb.CreateMollieFirstPaymentResponse{
		PaymentUrl:       payment.Links.Checkout.Href,
		PaymentId:        payment.ID,
		MollieCustomerId: mollieCustomerID,
	}, nil
}

// CreateSubscription creates a Mollie subscription after mandate is valid
func (s *PurserServer) CreateMollieSubscription(ctx context.Context, req *purserpb.CreateMollieSubscriptionRequest) (*purserpb.CreateMollieSubscriptionResponse, error) {
	if s.mollieClient == nil {
		return nil, status.Error(codes.Unavailable, "Mollie not configured")
	}

	tenantID := req.GetTenantId()
	tierID := req.GetTierId()
	mandateID := req.GetMandateId()

	if tenantID == "" || tierID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and tier_id are required")
	}
	if err := s.requireCompleteBillingDetails(ctx, tenantID); err != nil {
		return nil, err
	}

	// Precondition: a tenant_subscriptions row must exist before we ask
	// Mollie to create a live subscription. The post-Mollie UPDATE later
	// requires this row; if it's missing, Mollie would charge a customer
	// while Purser stayed unaware. Fail closed here so no external state
	// gets ahead of internal state.
	queries := purserdb.New(s.db)
	tenantSubExists, preflightErr := queries.TenantSubscriptionExists(ctx, tenantID)
	if preflightErr == nil && !tenantSubExists {
		return nil, status.Errorf(codes.FailedPrecondition,
			"no tenant_subscriptions row for tenant %s; bootstrap must seed one before subscribing", tenantID)
	}
	if preflightErr != nil {
		s.logger.WithError(preflightErr).Error("Failed to preflight tenant_subscriptions row")
		return nil, status.Error(codes.Internal, "failed to preflight tenant subscription")
	}

	// Get Mollie customer ID
	mollieCustomerID, err := queries.GetMollieCustomerID(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.FailedPrecondition, "no Mollie customer found - complete first payment first")
	}
	if err != nil {
		s.logger.WithError(err).Error("Failed to get Mollie customer")
		return nil, status.Error(codes.Internal, "failed to get Mollie customer")
	}

	// Resolve the mandate against this exact customer. A caller-supplied opaque
	// mandate ID is not trusted until Mollie confirms both ownership and status.
	if mandateID == "" {
		mandates, listErr := s.mollieClient.ListMandates(ctx, mollieCustomerID)
		if listErr != nil {
			s.logger.WithError(listErr).Error("Failed to list Mollie mandates")
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
	} else {
		mandate, mandateErr := s.mollieClient.GetMandate(ctx, mollieCustomerID, mandateID)
		if mandateErr != nil {
			s.logger.WithError(mandateErr).WithField("customer_id", mollieCustomerID).Warn("Failed to verify Mollie mandate")
			return nil, status.Error(codes.FailedPrecondition, "mandate is unavailable for this Mollie customer")
		}
		if mandate == nil || mandate.ID != mandateID || mandate.Status != "valid" {
			return nil, status.Error(codes.FailedPrecondition, "mandate is not valid for recurring collection")
		}
	}

	// Get tier price
	tier, err := queries.GetMollieTierPrice(ctx, tierID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Errorf(codes.NotFound, "tier not found: %s", tierID)
	}
	if err != nil {
		s.logger.WithError(err).Error("Failed to get billing tier")
		return nil, status.Error(codes.Internal, "failed to get billing tier")
	}
	basePrice, err := decimal.NewFromString(tier.BasePrice)
	if err != nil {
		s.logger.WithError(err).WithField("tier_id", tierID).Error("Failed to parse billing tier base price")
		return nil, status.Error(codes.Internal, "failed to parse billing tier price")
	}

	// Build webhook URL
	webhookBaseURL := config.GetGatewayPublicURL()
	webhookURL := ""
	if webhookBaseURL != "" {
		webhookURL = webhookBaseURL + "/webhooks/billing/mollie"
	}

	// Policy: the first payment covers period 1. The subscription's first
	// recurring charge therefore starts one interval (1 month) later, so we
	// do not double-charge the same period. Use the customer's local date
	// (UTC is fine — Mollie billing uses calendar dates, not timestamps).
	startAt := time.Now().UTC().AddDate(0, 1, 0)
	periodEnd := time.Date(startAt.Year(), startAt.Month(), startAt.Day(), 0, 0, 0, 0, time.UTC)
	periodStart := periodEnd.AddDate(0, -1, 0)
	startDate := periodEnd.Format("2006-01-02")

	// Durable intent before Mollie subscription creation. Idempotency key
	// is deterministic on (tenant, tier, mandate); a compensating cancel
	// still runs on local-persist failure, but the intent row provides
	// the audit trail to identify orphan subscriptions if both halves fail.
	subscriptionIntentKey := fmt.Sprintf("mollie-subscription:%s:%s:%s", tenantID, tierID, mandateID)
	subscriptionIntentID, intentErr := queries.UpsertMollieSubscriptionIntent(ctx, purserdb.UpsertMollieSubscriptionIntentParams{
		TenantID: tenantID, TierID: tierID,
		CustomerID: sql.NullString{String: mollieCustomerID, Valid: true},
		Currency:   tier.Currency, AmountCents: basePrice.Round(2).Shift(2).IntPart(),
		IdempotencyKey: subscriptionIntentKey,
	})
	if intentErr != nil {
		s.logger.WithError(intentErr).Error("Failed to record Mollie subscription intent")
		return nil, status.Error(codes.Internal, "failed to record subscription intent")
	}

	// Create subscription
	sub, err := s.mollieClient.CreateSubscription(ctx, mollie.SubscriptionParams{
		CustomerID:     mollieCustomerID,
		TenantID:       tenantID,
		TierID:         tierID,
		MandateID:      mandateID,
		Amount:         mollie.Amount(basePrice.Round(2).StringFixed(2), tier.Currency),
		Interval:       "1 month",
		Description:    fmt.Sprintf("Subscription: %s", tier.TierName),
		StartDate:      startDate,
		WebhookURL:     webhookURL,
		IdempotencyKey: subscriptionIntentKey,
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to create Mollie subscription")
		s.markProviderIntentFailed(ctx, subscriptionIntentID, "subscription_create_failed", err)
		return nil, status.Error(codes.Internal, "failed to create subscription")
	}
	if sub != nil && sub.ID != "" {
		if updateErr := queries.SetProviderIntentSubscriptionOpen(ctx, purserdb.SetProviderIntentSubscriptionOpenParams{
			SubscriptionID: sql.NullString{String: sub.ID, Valid: true}, IntentID: subscriptionIntentID,
		}); updateErr != nil {
			s.logger.WithError(updateErr).WithField("intent_id", subscriptionIntentID).Warn("Failed to record provider_subscription_id")
		}
	}

	nextPayment := startDate
	nextPaymentDate := periodEnd
	if sub.NextPaymentDate != nil {
		nextPayment = sub.NextPaymentDate.String()
		parsedNextPayment, parseErr := time.Parse("2006-01-02", nextPayment)
		if parseErr != nil {
			return nil, status.Errorf(codes.Internal, "invalid Mollie next payment date: %v", parseErr)
		}
		nextPaymentDate = parsedNextPayment
	}

	// Persist the Mollie subscription state on the existing tenant_subscriptions
	// row. Sets tier_id + payment_method so downstream entitlement / invoicing
	// readers see a coherent state immediately, and clears any staged
	// pending_tier_id (since this activation supersedes a pending change).
	// Mollie's authoritative next-payment date anchors the invoice period.
	//
	// If the persist fails (DB error or row vanished post-preflight), we
	// have to compensate by cancelling the Mollie subscription so the
	// customer isn't charged for a sub Purser doesn't know about. The
	// preflight earlier in the RPC catches the no-row case; this guard
	// covers transient DB failures and concurrent deletes.
	rows, err := queries.ActivateMollieTenantSubscription(ctx, purserdb.ActivateMollieTenantSubscriptionParams{
		SubscriptionID:  sql.NullString{String: sub.ID, Valid: true},
		NextPaymentDate: nextPaymentDate,
		PeriodStart:     sql.NullTime{Time: periodStart, Valid: true},
		PeriodEnd:       sql.NullTime{Time: periodEnd, Valid: true}, TierID: tierID, TenantID: tenantID,
	})
	if err != nil {
		s.logger.WithError(err).WithField("subscription_id", sub.ID).Error("Failed to persist Mollie subscription state; compensating cancel")
		if cancelErr := s.mollieClient.CancelSubscription(ctx, mollieCustomerID, sub.ID); cancelErr != nil {
			s.logger.WithError(cancelErr).WithField("subscription_id", sub.ID).Error("Compensating Mollie subscription cancel failed; orphan needs ops cleanup")
			return nil, status.Errorf(codes.Internal, "created Mollie subscription %s but failed to persist it locally and failed to cancel it: %v", sub.ID, cancelErr)
		}
		return nil, status.Error(codes.Internal, "failed to persist Mollie subscription state")
	}
	if rows == 0 {
		s.logger.WithFields(logging.Fields{
			"tenant_id":       tenantID,
			"subscription_id": sub.ID,
		}).Error("tenant_subscriptions row disappeared between preflight and persist; compensating cancel")
		if cancelErr := s.mollieClient.CancelSubscription(ctx, mollieCustomerID, sub.ID); cancelErr != nil {
			s.logger.WithError(cancelErr).WithField("subscription_id", sub.ID).Error("Compensating Mollie subscription cancel failed; orphan needs ops cleanup")
			return nil, status.Errorf(codes.Internal, "created Mollie subscription %s but local subscription row disappeared and compensating cancel failed: %v", sub.ID, cancelErr)
		}
		return nil, status.Errorf(codes.FailedPrecondition,
			"tenant_subscriptions row missing for tenant %s; Mollie subscription was cancelled to avoid an orphan", tenantID)
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":       tenantID,
		"tier_id":         tierID,
		"subscription_id": sub.ID,
		"next_payment":    nextPayment,
	}).Info("Created Mollie subscription")

	return &purserpb.CreateMollieSubscriptionResponse{
		SubscriptionId:  sub.ID,
		Status:          string(sub.Status),
		NextPaymentDate: nextPayment,
	}, nil
}

// ListMandates lists available Mollie mandates for a tenant
func (s *PurserServer) ListMandates(ctx context.Context, req *purserpb.ListMollieMandatesRequest) (*purserpb.ListMollieMandatesResponse, error) {
	if s.mollieClient == nil {
		return nil, status.Error(codes.Unavailable, "Mollie not configured")
	}

	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	// Get Mollie customer ID
	mollieCustomerID, err := purserdb.New(s.db).GetMollieCustomerID(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return &purserpb.ListMollieMandatesResponse{Mandates: []*purserpb.MollieMandate{}}, nil
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

	result := make([]*purserpb.MollieMandate, 0, len(mandates))
	for _, m := range mandates {
		info := s.mollieClient.ExtractMandateInfo(m, mollieCustomerID)
		details, _ := structpb.NewStruct(info.Details)
		result = append(result, &purserpb.MollieMandate{
			MollieMandateId:  info.MollieMandateID,
			MollieCustomerId: info.MollieCustomerID,
			Status:           info.Status,
			Method:           info.Method,
			Details:          details,
			CreatedAt:        timestamppb.New(info.CreatedAt),
		})
	}

	return &purserpb.ListMollieMandatesResponse{Mandates: result}, nil
}

// CancelSubscription cancels a Mollie subscription
func (s *PurserServer) CancelMollieSubscription(ctx context.Context, req *purserpb.CancelMollieSubscriptionRequest) (*emptypb.Empty, error) {
	if s.mollieClient == nil {
		return nil, status.Error(codes.Unavailable, "Mollie not configured")
	}

	tenantID := req.GetTenantId()
	subscriptionID := req.GetSubscriptionId()

	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	// Get Mollie customer ID
	queries := purserdb.New(s.db)
	mollieCustomerID, err := queries.GetMollieCustomerID(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "no Mollie customer found")
	}
	if err != nil {
		s.logger.WithError(err).Error("Failed to get Mollie customer")
		return nil, status.Error(codes.Internal, "failed to get Mollie customer")
	}

	// If no subscription ID provided, get from database
	if subscriptionID == "" {
		subID, subErr := queries.GetTenantMollieSubscriptionID(ctx, tenantID)
		if subErr != nil || !subID.Valid {
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
	err = queries.CancelLocalMollieSubscription(ctx, tenantID)
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
func (s *PurserServer) GetBillingDetails(ctx context.Context, req *purserpb.GetBillingDetailsRequest) (*purserpb.BillingDetails, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	row, err := purserdb.New(s.db).GetTenantBillingDetails(ctx, tenantID)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "no subscription found for tenant")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	details := &purserpb.BillingDetails{
		TenantId:  tenantID,
		UpdatedAt: timestamppb.New(row.UpdatedAt.Time),
	}

	if row.BillingEmail.Valid {
		details.Email = row.BillingEmail.String
	}
	if row.BillingName.Valid {
		details.Name = row.BillingName.String
	}
	if row.BillingCompany.Valid {
		details.Company = row.BillingCompany.String
	}
	if row.TaxID.Valid {
		details.VatNumber = row.TaxID.String
	}
	details.Address = scanBillingAddress(row.BillingAddress)

	// IsComplete means the profile can be used for a full customer invoice.
	details.IsComplete = details.Name != "" && details.Email != "" && details.Address != nil &&
		details.Address.Street != "" && details.Address.City != "" &&
		details.Address.PostalCode != "" && details.Address.Country != ""

	return details, nil
}

func (s *PurserServer) requireCompleteBillingDetails(ctx context.Context, tenantID string) error {
	details, err := s.GetBillingDetails(ctx, &purserpb.GetBillingDetailsRequest{TenantId: tenantID})
	if err != nil {
		return err
	}
	if !details.GetIsComplete() {
		return status.Error(codes.FailedPrecondition, "customer legal name, billing email, and postal address are required for this operation")
	}
	return nil
}

func cryptoBillingProfileRequiredError(requirement handlers.CryptoDocumentRequirement) error {
	st := status.New(codes.FailedPrecondition, cryptoBillingProfileRequirementMessage(requirement))
	withDetails, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason: "BILLING_PROFILE_REQUIRED",
		Domain: "billing.frameworks.network",
		Metadata: map[string]string{
			"required_fields": "name,email,street,city,postal_code,country",
			"public_message":  cryptoBillingProfileRequirementMessage(requirement),
		},
	})
	if err != nil {
		return st.Err()
	}
	return withDetails.Err()
}

func cryptoBillingProfileRequirementMessage(requirement handlers.CryptoDocumentRequirement) string {
	reason := "this payment requires a full customer tax invoice"
	if requirement.HasVATClaim {
		reason = "a submitted VAT number requires a complete customer billing profile"
	}
	return reason + "; add a legal name, billing email, and postal address before paying"
}

// UpdateBillingDetails updates billing details for a tenant
func (s *PurserServer) UpdateBillingDetails(ctx context.Context, req *purserpb.UpdateBillingDetailsRequest) (*purserpb.BillingDetails, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	if req.Email == nil && req.Name == nil && req.Company == nil && req.VatNumber == nil && req.Address == nil {
		return s.GetBillingDetails(ctx, &purserpb.GetBillingDetailsRequest{TenantId: tenantID})
	}
	addressJSON := []byte(`{}`)
	if req.Address != nil {
		// Validate and normalize country code
		countryCode := countries.Normalize(req.Address.Country)
		if !countries.IsValid(countryCode) {
			return nil, status.Errorf(codes.InvalidArgument, "invalid country code %q: must be a valid ISO 3166-1 alpha-2 code (e.g., US, DE, NL)", req.Address.Country)
		}

		// Convert proto address to JSONB
		var err error
		addressJSON, err = json.Marshal(map[string]string{
			"street":      req.Address.Street,
			"city":        req.Address.City,
			"state":       req.Address.State,
			"postal_code": req.Address.PostalCode,
			"country":     countryCode,
		})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to serialize address: %v", err)
		}
	}
	rowsAffected, err := purserdb.New(s.db).UpdateTenantBillingDetails(ctx, purserdb.UpdateTenantBillingDetailsParams{
		SetEmail: req.Email != nil, Email: derefString(req.Email),
		SetName: req.Name != nil, Name: derefString(req.Name),
		SetCompany: req.Company != nil, Company: derefString(req.Company),
		SetVatNumber: req.VatNumber != nil, VatNumber: derefString(req.VatNumber),
		SetAddress: req.Address != nil, Address: addressJSON, TenantID: tenantID,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	if rowsAffected == 0 {
		return nil, status.Error(codes.NotFound, "no active subscription found for tenant")
	}

	s.logger.WithField("tenant_id", tenantID).Info("Billing details updated")

	// Return updated billing details
	return s.GetBillingDetails(ctx, &purserpb.GetBillingDetailsRequest{TenantId: tenantID})
}

// ============================================================================
// X402 Service Methods
// ============================================================================

// GetPaymentRequirements returns the x402 payment requirements for a 402 response
// Returns multiple network options (Base + Arbitrum) so clients can choose their preferred network.
// Each requirement is bound to the tenant that will receive the prepaid credit.
//
//nolint:nilerr // x402 advertises protocol failures in the response document, not as gRPC transport errors.
func (s *PurserServer) GetPaymentRequirements(ctx context.Context, req *purserpb.GetPaymentRequirementsRequest) (*purserpb.PaymentRequirements, error) {
	if !config.X402PaymentsEnabled() {
		return &purserpb.PaymentRequirements{
			X402Version: 2,
			Error:       "x402 payments are temporarily disabled",
			TopupUrl:    "/account/billing",
		}, nil
	}
	if err := s.x402handler.Readiness(ctx); err != nil {
		s.logger.WithError(err).Warn("x402 requirements withheld because settlement is not ready")
		return &purserpb.PaymentRequirements{
			X402Version: 2,
			Error:       err.Error(),
			TopupUrl:    "/account/billing",
		}, nil
	}
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return &purserpb.PaymentRequirements{
			X402Version: 2,
			Error:       "tenant-bound payment target required",
			TopupUrl:    "/account/billing",
		}, nil
	}
	if rateLimitErr := s.x402handler.EnforceQuoteRateLimits(ctx, tenantID, req.GetClientIp()); rateLimitErr != nil {
		return &purserpb.PaymentRequirements{
			X402Version: 2,
			Error:       rateLimitErr.Error(),
			TopupUrl:    "/account/billing",
		}, nil
	}

	payToAddr, _, _, err := s.x402handler.GetOrCreateTenantX402Address(ctx, tenantID)
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"error":     err,
			"tenant_id": tenantID,
		}).Error("Failed to get tenant x402 address")
		//nolint:nilerr // error returned in response struct for client handling
		return &purserpb.PaymentRequirements{
			X402Version: 2,
			Error:       "failed to get payment address",
			TopupUrl:    "/account/billing",
		}, nil
	}

	// Get all x402-enabled networks from the registry
	networks, err := s.x402handler.GetAdvertisableX402Networks(ctx)
	if err != nil {
		return &purserpb.PaymentRequirements{
			X402Version: 2,
			Error:       "x402 facilitator is unavailable",
			TopupUrl:    "/account/billing",
		}, nil
	}
	resource := req.GetResource()

	resourceURL := x402ResourceURL(resource)
	if config.IsProduction() && config.GetGatewayPublicURL() == "" {
		return &purserpb.PaymentRequirements{
			X402Version: 2,
			Error:       "x402 not ready: GATEWAY_PUBLIC_URL",
			TopupUrl:    "/account/billing",
		}, nil
	}

	// Build immutable v2 quotes for all supported networks. Each accepted
	// requirement has its own quote because chain and asset are quote-bound.
	accepts := make([]*purserpb.PaymentRequirement, 0, len(networks))
	for index, network := range networks {
		quote, quoteErr := s.x402handler.CreatePaymentQuote(ctx, tenantID, resource, payToAddr, network)
		if quoteErr != nil {
			s.logger.WithError(quoteErr).WithField("tenant_id", tenantID).Warn("Failed to create x402 quote")
			return &purserpb.PaymentRequirements{
				X402Version: 2,
				Error:       "failed to create payment quote",
				TopupUrl:    "/account/billing",
			}, nil
		}
		if index == 0 {
			documentRequirement, requirementErr := s.x402handler.GetCryptoDocumentRequirement(ctx, tenantID, quote.CreditAmountCents)
			if requirementErr != nil {
				if expireErr := s.x402handler.ExpirePaymentQuote(ctx, quote.ID); expireErr != nil {
					s.logger.WithError(expireErr).WithField("quote_id", quote.ID).Warn("Failed to expire unusable x402 quote")
				}
				return &purserpb.PaymentRequirements{
					X402Version: 2,
					Error:       "unable to determine the tax-document requirement; retry later",
					TopupUrl:    "/account/billing",
				}, nil
			}
			if documentRequirement.RequiresCompleteProfile {
				if expireErr := s.x402handler.ExpirePaymentQuote(ctx, quote.ID); expireErr != nil {
					s.logger.WithError(expireErr).WithField("quote_id", quote.ID).Warn("Failed to expire profile-blocked x402 quote")
				}
				return &purserpb.PaymentRequirements{
					X402Version:    2,
					Error:          cryptoBillingProfileRequirementMessage(documentRequirement),
					ErrorCode:      "BILLING_PROFILE_REQUIRED",
					RequiredFields: []string{"billing_name", "billing_email", "billing_address.street", "billing_address.city", "billing_address.postal_code", "billing_address.country"},
					TopupUrl:       "/account/billing",
				}, nil
			}
		}
		accepts = append(accepts, &purserpb.PaymentRequirement{
			Scheme:            "exact",
			Network:           quote.CAIP2Network,
			MaxAmountRequired: quote.AmountAtomic,
			Amount:            quote.AmountAtomic,
			PayTo:             payToAddr,
			Asset:             network.USDCContract,
			MaxTimeoutSeconds: 60,
			Resource:          resource,
			Description:       "Streaming, transcoding & storage credits via " + network.DisplayName,
			ExtraJson:         quote.ExtraJSON,
			QuoteId:           quote.ID,
		})
	}

	response := &purserpb.PaymentRequirements{
		X402Version:         2,
		Accepts:             accepts,
		TopupUrl:            "/account/billing",
		ResourceUrl:         resourceURL,
		ResourceDescription: "FrameWorks prepaid usage credit",
		ResourceMimeType:    "application/json",
	}
	canonical, err := sharedx402.EncodePaymentRequired(response)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode x402 requirements: %v", err)
	}
	response.CanonicalJson = canonical
	return response, nil
}

func x402ResourceURL(resource string) string {
	resource = strings.TrimSpace(resource)
	if parsed, err := url.Parse(resource); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" {
		return parsed.String()
	}
	base := config.GetGatewayPublicURL()
	if base == "" {
		base = "http://localhost:18005"
	}
	if strings.HasPrefix(resource, "/") {
		return base + resource
	}
	if strings.HasPrefix(strings.ToLower(resource), "viewer://") {
		return base + "/api/viewer/resolve"
	}
	return base + "/graphql"
}

// VerifyX402Payment verifies a tenant-bound x402 payment without settling.
func (s *PurserServer) VerifyX402Payment(ctx context.Context, req *purserpb.VerifyX402PaymentRequest) (*purserpb.VerifyX402PaymentResponse, error) {
	if !config.X402PaymentsEnabled() {
		return nil, status.Error(codes.Unavailable, "x402 payments are temporarily disabled; submitted settlements continue reconciling")
	}
	if err := s.x402handler.Readiness(ctx); err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	tenantID := req.GetTenantId()

	payment := req.GetPayment()
	if payment == nil {
		return nil, status.Error(codes.InvalidArgument, "payment required")
	}

	// Convert proto to handler type
	handlerPayload := &handlers.X402PaymentPayload{
		X402Version:          int(payment.GetX402Version()),
		Scheme:               payment.GetScheme(),
		Network:              payment.GetNetwork(),
		CanonicalPayloadJSON: append([]byte(nil), payment.GetCanonicalPayloadJson()...),
		QuoteID:              payment.GetQuoteId(),
	}
	if payment.GetAccepted() != nil {
		accepted := payment.GetAccepted()
		handlerPayload.Accepted = &handlers.X402AcceptedRequirements{
			Scheme:            accepted.GetScheme(),
			Network:           accepted.GetNetwork(),
			Asset:             accepted.GetAsset(),
			Amount:            accepted.GetAmount(),
			PayTo:             accepted.GetPayTo(),
			MaxTimeoutSeconds: int(accepted.GetMaxTimeoutSeconds()),
			ExtraJSON:         append([]byte(nil), accepted.GetExtraJson()...),
		}
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

	return &purserpb.VerifyX402PaymentResponse{
		Valid:                  result.Valid,
		Error:                  result.Error,
		PayerAddress:           result.PayerAddress,
		AmountCents:            result.AmountCents,
		RequiresBillingDetails: result.RequiresBillingDetails,
		IsAuthOnly:             result.IsAuthOnly,
	}, nil
}

// SettleX402Payment settles a tenant-bound x402 payment and credits the balance.
func (s *PurserServer) SettleX402Payment(ctx context.Context, req *purserpb.SettleX402PaymentRequest) (*purserpb.SettleX402PaymentResponse, error) {
	if !config.X402PaymentsEnabled() {
		return nil, status.Error(codes.Unavailable, "x402 payments are temporarily disabled; submitted settlements continue reconciling")
	}
	if err := s.x402handler.Readiness(ctx); err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	tenantID := req.GetTenantId()

	payment := req.GetPayment()
	if payment == nil {
		return nil, status.Error(codes.InvalidArgument, "payment required")
	}

	// Convert proto to handler type
	handlerPayload := &handlers.X402PaymentPayload{
		X402Version:          int(payment.GetX402Version()),
		Scheme:               payment.GetScheme(),
		Network:              payment.GetNetwork(),
		CanonicalPayloadJSON: append([]byte(nil), payment.GetCanonicalPayloadJson()...),
		QuoteID:              payment.GetQuoteId(),
	}
	if payment.GetAccepted() != nil {
		accepted := payment.GetAccepted()
		handlerPayload.Accepted = &handlers.X402AcceptedRequirements{
			Scheme:            accepted.GetScheme(),
			Network:           accepted.GetNetwork(),
			Asset:             accepted.GetAsset(),
			Amount:            accepted.GetAmount(),
			PayTo:             accepted.GetPayTo(),
			MaxTimeoutSeconds: int(accepted.GetMaxTimeoutSeconds()),
			ExtraJSON:         append([]byte(nil), accepted.GetExtraJson()...),
		}
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
		rowsAffected, err := purserdb.New(s.db).ReactivateSuspendedTenant(ctx, tenantID)
		if err != nil {
			s.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to check/reactivate suspended subscription after x402 top-up")
		} else if rowsAffected > 0 {
			s.logger.WithFields(map[string]any{
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
						s.logger.WithFields(map[string]any{
							"tenant_id":           tenantID,
							"entries_invalidated": resp.EntriesInvalidated,
						}).Info("Invalidated media plane cache after x402 reactivation")
					}
				}()
			}
		}
	}

	return &purserpb.SettleX402PaymentResponse{
		Success:          result.Success,
		Error:            result.Error,
		TxHash:           result.TxHash,
		CreditedCents:    result.CreditedCents,
		Currency:         result.Currency,
		NewBalanceCents:  result.NewBalanceCents,
		InvoiceNumber:    result.InvoiceNumber,
		IsAuthOnly:       result.IsAuthOnly,
		PayerAddress:     result.PayerAddress,
		SettlementStatus: result.SettlementStatus,
		ErrorCode:        result.ErrorCode,
		Network:          result.Network,
	}, nil
}

// GetTenantX402Address returns the per-tenant x402 deposit address
func (s *PurserServer) GetTenantX402Address(ctx context.Context, req *purserpb.GetTenantX402AddressRequest) (*purserpb.GetTenantX402AddressResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	address, derivationIndex, newlyCreated, err := s.x402handler.GetOrCreateTenantX402Address(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get x402 address: %v", err)
	}

	return &purserpb.GetTenantX402AddressResponse{
		Address:         address,
		DerivationIndex: derivationIndex,
		NewlyCreated:    newlyCreated,
	}, nil
}

const maxX402MutationResultBytes = 2 << 20

func validSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ClaimX402MutationResult durably owns one quote-bound application mutation.
// An abandoned claim is never released for blind re-execution. Old ambiguous
// claims move to operator_review so an operator can attach the known result.
func (s *PurserServer) ClaimX402MutationResult(ctx context.Context, req *purserpb.ClaimX402MutationResultRequest) (*purserpb.ClaimX402MutationResultResponse, error) {
	tenantID := strings.TrimSpace(req.GetTenantId())
	quoteID := strings.TrimSpace(req.GetQuoteId())
	key := strings.TrimSpace(req.GetIdempotencyKey())
	fingerprint := strings.TrimSpace(req.GetRequestFingerprint())
	protocol := strings.TrimSpace(req.GetProtocol())
	operation := strings.TrimSpace(req.GetOperation())
	if _, err := uuid.Parse(tenantID); err != nil {
		return nil, status.Error(codes.InvalidArgument, "valid tenant_id required")
	}
	if _, err := uuid.Parse(quoteID); err != nil {
		return nil, status.Error(codes.InvalidArgument, "valid quote_id required")
	}
	if len(key) < 8 || len(key) > 255 {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key must contain 8-255 characters")
	}
	if !validSHA256Hex(fingerprint) {
		return nil, status.Error(codes.InvalidArgument, "request_fingerprint must be lowercase SHA-256 hex")
	}
	if protocol != "http" && protocol != "mcp" {
		return nil, status.Error(codes.InvalidArgument, "protocol must be http or mcp")
	}
	if operation == "" || len(operation) > 255 {
		return nil, status.Error(codes.InvalidArgument, "operation required")
	}

	queries := purserdb.New(s.db)
	inserted, err := queries.ClaimX402Mutation(ctx, purserdb.ClaimX402MutationParams{
		TenantID: tenantID, QuoteID: quoteID, IdempotencyKey: key,
		RequestFingerprint: fingerprint, Protocol: protocol, Operation: operation,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "claim x402 mutation result: %v", err)
	}
	stored, err := queries.GetX402MutationClaim(ctx, purserdb.GetX402MutationClaimParams{
		TenantID: tenantID, IdempotencyKey: key,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.FailedPrecondition, "quote is not a confirmed tenant-bound settlement or was already bound to another mutation")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read x402 mutation result claim: %v", err)
	}
	if stored.QuoteID != quoteID || stored.RequestFingerprint != fingerprint || stored.Protocol != protocol || stored.Operation != operation {
		return nil, status.Error(codes.AlreadyExists, "idempotency key or paid quote is already bound to a different request")
	}
	if inserted > 0 {
		return &purserpb.ClaimX402MutationResultResponse{State: "claimed"}, nil
	}
	if stored.Status == "completed" {
		return &purserpb.ClaimX402MutationResultResponse{
			State:       "completed",
			Result:      stored.Result,
			ContentType: stored.ContentType.String,
			StatusCode:  stored.StatusCode.Int32,
		}, nil
	}
	if stored.Status != "operator_review" && time.Since(stored.UpdatedAt) >= 15*time.Minute {
		if updateErr := queries.MarkX402MutationOperatorReview(ctx, purserdb.MarkX402MutationOperatorReviewParams{
			TenantID: tenantID, IdempotencyKey: key,
		}); updateErr != nil {
			return nil, status.Errorf(codes.Internal, "mark abandoned x402 mutation for review: %v", updateErr)
		}
		stored.Status = "operator_review"
	}
	if stored.Status == "operator_review" {
		return &purserpb.ClaimX402MutationResultResponse{
			State: "operator_review", Error: "mutation outcome requires operator review; do not execute it again",
		}, nil
	}
	return &purserpb.ClaimX402MutationResultResponse{State: "in_progress"}, nil
}

func (s *PurserServer) CompleteX402MutationResult(ctx context.Context, req *purserpb.CompleteX402MutationResultRequest) (*purserpb.CompleteX402MutationResultResponse, error) {
	tenantID := strings.TrimSpace(req.GetTenantId())
	quoteID := strings.TrimSpace(req.GetQuoteId())
	key := strings.TrimSpace(req.GetIdempotencyKey())
	fingerprint := strings.TrimSpace(req.GetRequestFingerprint())
	if _, err := uuid.Parse(tenantID); err != nil {
		return nil, status.Error(codes.InvalidArgument, "valid tenant_id required")
	}
	if _, err := uuid.Parse(quoteID); err != nil {
		return nil, status.Error(codes.InvalidArgument, "valid quote_id required")
	}
	if !validSHA256Hex(fingerprint) {
		return nil, status.Error(codes.InvalidArgument, "request_fingerprint must be lowercase SHA-256 hex")
	}
	if len(req.GetResult()) > maxX402MutationResultBytes {
		return nil, status.Error(codes.InvalidArgument, "mutation result exceeds 2 MiB limit")
	}
	if req.GetStatusCode() < 100 || req.GetStatusCode() > 599 {
		return nil, status.Error(codes.InvalidArgument, "valid status_code required")
	}

	queries := purserdb.New(s.db)
	updated, err := queries.CompleteX402Mutation(ctx, purserdb.CompleteX402MutationParams{
		TenantID: tenantID, QuoteID: quoteID, IdempotencyKey: key, RequestFingerprint: fingerprint,
		Result: req.GetResult(), ContentType: req.GetContentType(), StatusCode: req.GetStatusCode(),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "complete x402 mutation result: %v", err)
	}
	if updated == 0 {
		completed, readErr := queries.IsX402MutationCompleted(ctx, purserdb.IsX402MutationCompletedParams{
			TenantID: tenantID, QuoteID: quoteID, IdempotencyKey: key, RequestFingerprint: fingerprint,
		})
		if errors.Is(readErr, sql.ErrNoRows) {
			return nil, status.Error(codes.FailedPrecondition, "matching mutation claim not found")
		}
		if readErr != nil {
			return nil, status.Errorf(codes.Internal, "read x402 mutation completion: %v", readErr)
		}
		return &purserpb.CompleteX402MutationResultResponse{Completed: completed}, nil
	}
	return &purserpb.CompleteX402MutationResultResponse{Completed: true}, nil
}

// loadInvoiceLineItems reads persisted line items from purser.invoice_line_items.
// New invoices always populate this table via the rating engine; an empty result
// for an invoice is itself an integrity signal — the writer is supposed to
// upsert line items in the same transaction as the invoice header.
//
// tenantID is required: line items are tenant-scoped financial-audit rows and
// reads must filter by tenant per the cross-service tenant rule.
func (s *PurserServer) loadInvoiceLineItems(ctx context.Context, invoiceID, tenantID string) ([]*purserpb.LineItem, error) {
	rows, err := purserdb.New(s.db).ListInvoiceLineItemsForTenant(ctx, purserdb.ListInvoiceLineItemsForTenantParams{
		InvoiceID: invoiceID, TenantID: tenantID,
	})
	if err != nil {
		return nil, fmt.Errorf("query line items for invoice %s: %w", invoiceID, err)
	}
	items := make([]*purserpb.LineItem, 0, len(rows))
	for _, row := range rows {
		li := &purserpb.LineItem{
			LineKey: row.LineKey, Meter: row.Meter, Unit: row.Unit, Description: row.Description,
			Quantity: row.Quantity, IncludedQuantity: row.IncludedQuantity, BillableQuantity: row.BillableQuantity,
			UnitPrice: row.UnitPrice, Total: row.Amount, Currency: row.Currency,
			ClusterId: row.ClusterID, ClusterKind: row.ClusterKind, PricingSource: row.PricingSource,
		}
		if len(row.Dimensions) > 0 {
			li.Dimensions = mapToProtoStruct(jsonToMap(row.Dimensions))
		}
		li.PricingLabel = pricingLabelFor(li.PricingSource, li.ClusterKind)
		items = append(items, li)
	}
	// Best-effort cluster_name enrichment from Quartermaster — same shape
	// as the email path. One RPC per distinct cluster_id; failures degrade
	// silently (the dashboard renders the cluster_id as a fallback). This
	// keeps the gateway from needing its own enrichment layer.
	enrichLineItemClusterNames(ctx, s, items)
	return items, nil
}

// enrichLineItemClusterNames populates LineItem.cluster_name on each item
// with a non-empty cluster_id by looking up Quartermaster. Idempotent and
// best-effort.
func enrichLineItemClusterNames(ctx context.Context, s *PurserServer, items []*purserpb.LineItem) {
	if s.quartermasterClient == nil || len(items) == 0 {
		return
	}
	names := map[string]string{}
	for _, li := range items {
		if li.ClusterId == "" {
			continue
		}
		if _, ok := names[li.ClusterId]; ok {
			continue
		}
		resp, err := s.quartermasterClient.GetCluster(ctx, li.ClusterId)
		if err != nil || resp == nil || resp.GetCluster() == nil {
			names[li.ClusterId] = ""
			continue
		}
		names[li.ClusterId] = resp.GetCluster().GetClusterName()
	}
	for _, li := range items {
		if li.ClusterId != "" {
			li.ClusterName = names[li.ClusterId]
		}
	}
}

// pricingLabelFor maps a (pricing_source, cluster_kind) tuple to the
// human-readable badge rendered on invoice presentation surfaces (email,
// dashboard, gRPC clients without locale logic). The labels are deliberately
// neutral and short — UI may localize at render time.
func pricingLabelFor(pricingSource, clusterKind string) string {
	switch pricingSource {
	case "tier":
		return "Subscription tier"
	case "cluster_metered":
		if clusterKind == "third_party_marketplace" {
			return "Marketplace metered"
		}
		return "Cluster metered"
	case "cluster_monthly":
		return "Cluster monthly"
	case "cluster_custom":
		return "Custom contract"
	case "free_unmetered":
		return "Free (no charge)"
	case "self_hosted":
		return "Self-hosted (no charge)"
	case "included_subscription":
		return "Included in subscription"
	case "beta_free":
		return "Usage is on us during beta"
	default:
		return ""
	}
}
