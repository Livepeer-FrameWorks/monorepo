package grpc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	quartermasterdb "frameworks/api_tenants/internal/database/quartermasterdb"
	geobucket "frameworks/api_tenants/internal/geo"

	decklogclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/navigator"
	purserclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/purser"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/geoip"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pkgmesh "github.com/Livepeer-FrameWorks/monorepo/pkg/mesh"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	tenantlimitspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/tenant_limits"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// QuartermasterServer implements the Quartermaster gRPC services
type QuartermasterServer struct {
	quartermasterpb.UnimplementedTenantServiceServer
	quartermasterpb.UnimplementedBootstrapServiceServer
	quartermasterpb.UnimplementedNodeServiceServer
	quartermasterpb.UnimplementedClusterServiceServer
	quartermasterpb.UnimplementedMeshServiceServer
	quartermasterpb.UnimplementedServiceRegistryServiceServer
	quartermasterpb.UnimplementedIngressServiceServer
	db              *sql.DB
	logger          logging.Logger
	navigatorClient *navigator.Client
	decklogClient   *decklogclient.BatchedClient
	purserClient    *purserclient.GRPCClient // For billing status lookups (cross-service via gRPC, not DB)
	geoipReader     *geoip.Reader
	metrics         *ServerMetrics

	// quartermasterGRPCAddr is the address enrolling nodes should use to
	// reach this Quartermaster once they have mesh connectivity. Returned in
	// BootstrapInfrastructureNodeResponse so enrolling nodes can persist it
	// alongside their private key.
	quartermasterGRPCAddr string

	// platformRootDomain is the physical/platform DNS root (BRAND_DOMAIN),
	// used to synthesize per-instance physical endpoints
	// (<service>.<node>.infra.<root>). It is independent of any media-cluster
	// base_url so physical identity does not vary per logical assignment.
	platformRootDomain string

	// physicalEndpointStaleSeconds is the health-freshness window DiscoverServices
	// uses to gate public_instance_host, sourced from the SAME config Navigator uses
	// for physical DNS publication (NAVIGATOR_DNS_HEALTH_STALE_SECONDS) so the two
	// can't drift and hand Foghorn a hostname Navigator has already pruned.
	physicalEndpointStaleSeconds int
}

const (
	foghornListenerInternalControl = "internal_control"
	foghornInternalGRPCPort        = 18019
	foghornExternalGRPCPort        = 18029
	navigatorDNSSyncTimeout        = 30 * time.Second
	navigatorDNSSyncConcurrency    = 4
	syncMeshSlowLogThreshold       = time.Second
	meshTopologyWarmInterval       = 15 * time.Second
	meshTopologyPlannerVersion     = "mesh-v1"
	// defaultPhysicalEndpointStaleSeconds matches Navigator's code default for
	// NAVIGATOR_DNS_HEALTH_STALE_SECONDS; the configured value (e.g. base.env's 90)
	// overrides it via SetPhysicalEndpointStaleSeconds.
	defaultPhysicalEndpointStaleSeconds = 300
)

// SetQuartermasterGRPCAddr configures the gRPC address this Quartermaster
// advertises to freshly-enrolled nodes via BootstrapInfrastructureNodeResponse.
// Called during startup once the listener address is known.
func (s *QuartermasterServer) SetQuartermasterGRPCAddr(addr string) {
	s.quartermasterGRPCAddr = addr
}

// SetPlatformRootDomain configures the physical/platform DNS root used to
// synthesize per-instance physical endpoints. The value is normalized to a bare
// hostname (scheme/path stripped).
func (s *QuartermasterServer) SetPlatformRootDomain(rootDomain string) {
	s.platformRootDomain = dns.NormalizeDomainScope(rootDomain)
}

// SetPhysicalEndpointStaleSeconds configures the health-freshness window used to
// gate public_instance_host, sourced from Navigator's NAVIGATOR_DNS_HEALTH_STALE_SECONDS
// so the two stay in lockstep. Non-positive values keep the default.
func (s *QuartermasterServer) SetPhysicalEndpointStaleSeconds(seconds int) {
	if seconds > 0 {
		s.physicalEndpointStaleSeconds = seconds
	}
}

// NewQuartermasterServer creates a new Quartermaster gRPC server
func NewQuartermasterServer(db *sql.DB, logger logging.Logger, navigatorClient *navigator.Client, decklogClient *decklogclient.BatchedClient, purserClient *purserclient.GRPCClient, geoipReader *geoip.Reader, metrics *ServerMetrics) *QuartermasterServer {
	return &QuartermasterServer{
		db:                           db,
		logger:                       logger,
		navigatorClient:              navigatorClient,
		decklogClient:                decklogClient,
		purserClient:                 purserClient,
		geoipReader:                  geoipReader,
		metrics:                      metrics,
		physicalEndpointStaleSeconds: defaultPhysicalEndpointStaleSeconds,
	}
}

// mapToStruct converts a map[string]any to a protobuf Struct
func mapToStruct(m map[string]any) *structpb.Struct {
	if m == nil {
		return nil
	}
	s, err := structpb.NewStruct(m)
	if err != nil {
		return nil
	}
	return s
}

func marshalStringMapJSON(m map[string]string) (*string, error) {
	if len(m) == 0 {
		return nil, nil
	}

	encoded, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}

	value := string(encoded)
	return &value, nil
}

func marshalStringSliceJSON(values []string) (*string, error) {
	if len(values) == 0 {
		return nil, nil
	}

	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}

	value := string(encoded)
	return &value, nil
}

func validString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: true}
}

func optionalString(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return validString(*value)
}

func validBool(value bool) sql.NullBool {
	return sql.NullBool{Bool: value, Valid: true}
}

func validInt32(value int32) sql.NullInt32 {
	return sql.NullInt32{Int32: value, Valid: true}
}

func unmarshalStringMapJSON(raw []byte) map[string]string {
	if len(raw) == 0 {
		return nil
	}

	var metadata map[string]string
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}

func unmarshalStringSliceJSON(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}

	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil
	}
	return values
}

// decodeIngressDomainsStrict unmarshals a JSONB domains column, failing closed on
// a malformed value. A literal JSON `null` unmarshals into a nil slice with no
// error, so it would read as "no domains" and silently suppress a physical
// endpoint's public_instance_host synthesis or mislead Navigator's prune — the
// same fail-open an object-shaped value already avoids (it errors). A
// desired-state ingress row must carry a domains array; absent/empty (SQL NULL or
// `[]`) is legitimately no domains, but a non-array value is rejected.
func decodeIngressDomainsStrict(raw []byte) ([]string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, nil
	}
	if trimmed == "null" {
		return nil, fmt.Errorf("domains is JSON null, expected an array")
	}
	var domains []string
	if err := json.Unmarshal([]byte(trimmed), &domains); err != nil {
		return nil, err
	}
	return domains, nil
}

func normalizeStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	normalized := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		normalized = append(normalized, trimmed)
	}
	slices.Sort(normalized)
	return slices.Compact(normalized)
}

func buildAdvertiseAddr(host sql.NullString, port sql.NullInt32) (string, bool) {
	if !host.Valid || !port.Valid {
		return "", false
	}

	cleanHost := strings.TrimSpace(host.String)
	if cleanHost == "" {
		return "", false
	}
	if strings.HasPrefix(cleanHost, "[") && strings.HasSuffix(cleanHost, "]") {
		cleanHost = strings.TrimPrefix(strings.TrimSuffix(cleanHost, "]"), "[")
	}
	if port.Int32 <= 0 || port.Int32 > 65535 {
		return "", false
	}

	return net.JoinHostPort(cleanHost, fmt.Sprintf("%d", port.Int32)), true
}

func isLoopbackAddress(host string) bool {
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.IsLoopback()
}

// ValidateTenant validates a tenant and returns its features/limits
// Billing info is fetched via Purser gRPC (no cross-service DB access)
func (s *QuartermasterServer) ValidateTenant(ctx context.Context, req *quartermasterpb.ValidateTenantRequest) (*quartermasterpb.ValidateTenantResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return &quartermasterpb.ValidateTenantResponse{
			Valid: false,
			Error: "tenant_id required",
		}, nil
	}

	// Query ONLY quartermaster.tenants (no cross-service DB access)
	queries := quartermasterdb.New(s.db)
	var tenant quartermasterdb.ValidateTenantRecordRow
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		tenant, queryErr = queries.ValidateTenantRecord(ctx, tenantID)
		return queryErr
	})

	if errors.Is(err, sql.ErrNoRows) {
		return &quartermasterpb.ValidateTenantResponse{
			Valid: false,
			Error: "Tenant not found",
		}, nil
	}

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Error("Database error validating tenant")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !tenant.IsActive.Valid {
		return nil, status.Error(codes.Internal, "database error: tenant is_active is NULL")
	}

	// Get billing info via Purser gRPC (cross-service API call, not DB join)
	var billingModel, collectionProvider, tierName string
	var isSuspended, isBalanceNegative, billingStatusUnavailable bool
	var collectionReady bool

	if s.purserClient != nil {
		billingStatus, err := s.purserClient.GetTenantBillingStatus(ctx, tenantID)
		if err != nil {
			s.logger.WithFields(logging.Fields{
				"tenant_id": tenantID,
				"error":     err,
			}).Warn("Failed to get billing status from Purser")
			billingStatusUnavailable = true
		} else {
			billingModel = billingStatus.BillingModel
			isSuspended = billingStatus.IsSuspended
			isBalanceNegative = billingStatus.IsBalanceNegative
			collectionReady = billingStatus.CollectionReady
			collectionProvider = billingStatus.CollectionProvider
			tierName = billingStatus.TierName
		}
	} else {
		billingStatusUnavailable = true
	}

	return &quartermasterpb.ValidateTenantResponse{
		Valid:                    tenant.IsActive.Bool,
		TenantId:                 tenantID,
		TenantName:               tenant.Name,
		IsActive:                 tenant.IsActive.Bool,
		RateLimitPerMinute:       tenant.RateLimitPerMinute,
		RateLimitBurst:           tenant.RateLimitBurst,
		BillingModel:             billingModel,
		IsSuspended:              isSuspended,
		IsBalanceNegative:        isBalanceNegative,
		BillingStatusUnavailable: billingStatusUnavailable,
		CollectionReady:          collectionReady,
		CollectionProvider:       collectionProvider,
		TierName:                 tierName,
	}, nil
}

// GetTenant retrieves tenant details by ID
func (s *QuartermasterServer) GetTenant(ctx context.Context, req *quartermasterpb.GetTenantRequest) (*quartermasterpb.GetTenantResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	row, err := quartermasterdb.New(s.db).GetTenantRecord(ctx, tenantID)

	if errors.Is(err, sql.ErrNoRows) {
		return &quartermasterpb.GetTenantResponse{Error: "Tenant not found"}, nil
	}

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Error("Database error getting tenant")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !row.PrimaryColor.Valid || !row.SecondaryColor.Valid || !row.DeploymentTier.Valid ||
		!row.DeploymentModel.Valid || !row.IsActive.Valid || !row.CreatedAt.Valid || !row.UpdatedAt.Valid {
		return nil, status.Error(codes.Internal, "database error: tenant has NULL required fields")
	}

	tenant := quartermasterpb.Tenant{
		Id:                 row.ID,
		Name:               row.Name,
		PrimaryColor:       row.PrimaryColor.String,
		SecondaryColor:     row.SecondaryColor.String,
		DeploymentTier:     row.DeploymentTier.String,
		DeploymentModel:    row.DeploymentModel.String,
		KafkaBrokers:       row.KafkaBrokers,
		IsActive:           row.IsActive.Bool,
		MonitoringEnabled:  row.MonitoringEnabled,
		CreatedAt:          timestamppb.New(row.CreatedAt.Time),
		UpdatedAt:          timestamppb.New(row.UpdatedAt.Time),
		RateLimitPerMinute: row.RateLimitPerMinute,
		RateLimitBurst:     row.RateLimitBurst,
	}

	// Set optional fields
	if row.Subdomain.Valid {
		tenant.Subdomain = &row.Subdomain.String
	}
	if row.CustomDomain.Valid {
		tenant.CustomDomain = &row.CustomDomain.String
	}
	if row.LogoUrl.Valid {
		tenant.LogoUrl = &row.LogoUrl.String
	}
	if row.PrimaryClusterID.Valid {
		tenant.PrimaryClusterId = &row.PrimaryClusterID.String
	}
	if row.OfficialClusterID.Valid {
		tenant.OfficialClusterId = &row.OfficialClusterID.String
	}
	if row.KafkaTopicPrefix.Valid {
		tenant.KafkaTopicPrefix = &row.KafkaTopicPrefix.String
	}
	if row.DatabaseUrl.Valid {
		tenant.DatabaseUrl = &row.DatabaseUrl.String
	}

	return &quartermasterpb.GetTenantResponse{Tenant: &tenant}, nil
}

// GetClusterRouting returns the best cluster for a tenant's stream.
// Validates cluster has capacity (max_streams, max_bandwidth_mbps) before returning.
func (s *QuartermasterServer) GetClusterRouting(ctx context.Context, req *quartermasterpb.GetClusterRoutingRequest) (*quartermasterpb.ClusterRoutingResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	// Get tenant's primary (preferred) cluster, official cluster, and deployment tier
	queries := quartermasterdb.New(s.db)
	var routingSelection quartermasterdb.GetTenantRoutingSelectionRow
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		routingSelection, queryErr = queries.GetTenantRoutingSelection(ctx, tenantID)
		return queryErr
	})

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "Tenant not found")
	}

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Error("Database error getting tenant cluster info")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !routingSelection.PrimaryClusterID.Valid || !routingSelection.DeploymentTier.Valid {
		return nil, status.Error(codes.Internal, "database error: tenant routing fields are NULL")
	}
	primaryClusterID := routingSelection.PrimaryClusterID.String
	officialClusterID := routingSelection.OfficialClusterID

	// Get cluster info with capacity validation
	// max_streams = 0 means unlimited
	// max_bandwidth_mbps = 0 means unlimited
	var cluster quartermasterdb.GetTenantPrimaryClusterRoutingRow
	err = database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		cluster, queryErr = queries.GetTenantPrimaryClusterRouting(ctx, quartermasterdb.GetTenantPrimaryClusterRoutingParams{
			TenantID: tenantID, ClusterID: primaryClusterID,
		})
		return queryErr
	})

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "No suitable cluster found (capacity exceeded or inactive)")
	}

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"cluster_id": primaryClusterID,
			"error":      err,
		}).Error("Database error getting cluster routing")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !cluster.MaxConcurrentStreams.Valid || !cluster.HealthStatus.Valid {
		return nil, status.Error(codes.Internal, "database error: cluster routing fields are NULL")
	}

	resp := quartermasterpb.ClusterRoutingResponse{
		ClusterId:    cluster.ClusterID,
		ClusterName:  cluster.ClusterName,
		ClusterType:  cluster.ClusterType,
		BaseUrl:      cluster.BaseUrl,
		KafkaBrokers: cluster.KafkaBrokers,
		TopicPrefix:  cluster.TopicPrefix,
		MaxStreams:   cluster.MaxConcurrentStreams.Int32,
		HealthStatus: cluster.HealthStatus.String,
	}
	if cluster.DatabaseUrl.Valid {
		resp.DatabaseUrl = &cluster.DatabaseUrl.String
	}
	if cluster.PeriscopeUrl.Valid {
		resp.PeriscopeUrl = &cluster.PeriscopeUrl.String
	}

	// Surface access-specific runtime cap overrides so Foghorn can enforce
	// them at trigger time. Plan-level Free caps come from Purser tier
	// entitlements; an empty tenant_cluster_access.resource_limits column
	// means "no cluster override". Bandwidth caps (max_bandwidth_mbps) are
	// not enforced runtime today and intentionally not surfaced on the typed
	// response — they live in the JSONB column as a future hook.
	tenantResourceLimits, limitsErr := queries.GetTenantClusterResourceLimits(ctx, quartermasterdb.GetTenantClusterResourceLimitsParams{
		TenantID: tenantID, ClusterID: primaryClusterID,
	})
	if limitsErr == nil && len(tenantResourceLimits) > 0 {
		var limits map[string]any
		if json.Unmarshal(tenantResourceLimits, &limits) == nil {
			caps := &tenantlimitspb.TenantResourceLimits{}
			if v, ok := limits["max_streams"].(float64); ok && v > 0 {
				caps.MaxStreams = int32(v)
			}
			if v, ok := limits["max_viewers"].(float64); ok && v > 0 {
				caps.MaxViewers = int32(v)
			}
			if caps.MaxStreams > 0 || caps.MaxViewers > 0 {
				resp.TenantResourceLimits = caps
			}
		}
	}

	// Resolve Foghorn gRPC address via service_cluster_assignments (best-effort)
	foghorn, foghornErr := queries.GetHealthyFoghornAddressForCluster(ctx, primaryClusterID)
	if foghornErr == nil {
		if addr, ok := buildAdvertiseAddr(foghorn.AdvertiseHost, foghorn.Port); ok {
			resp.FoghornGrpcAddr = &addr
		}
	} else if !errors.Is(foghornErr, sql.ErrNoRows) {
		s.logger.WithError(foghornErr).WithField("cluster_id", primaryClusterID).Warn("Failed to resolve primary Foghorn address")
	}

	slug := dns.SanitizeLabel(resp.ClusterId)
	resp.ClusterSlug = &slug

	// Resolve official cluster info when it differs from primary (best-effort)
	if officialClusterID != "" && officialClusterID != primaryClusterID {
		officialCluster, officialErr := queries.GetActiveClusterIdentity(ctx, officialClusterID)

		if officialErr == nil {
			resp.OfficialClusterId = &officialClusterID
			offSlug := dns.SanitizeLabel(officialClusterID)
			resp.OfficialClusterSlug = &offSlug
			resp.OfficialBaseUrl = &officialCluster.BaseUrl
			resp.OfficialClusterName = &officialCluster.ClusterName

			// Resolve official cluster's Foghorn address via assignments
			offFoghorn, offFoghornErr := queries.GetHealthyFoghornAddressForCluster(ctx, officialClusterID)
			if offFoghornErr == nil {
				if addr, ok := buildAdvertiseAddr(offFoghorn.AdvertiseHost, offFoghorn.Port); ok {
					resp.OfficialFoghornGrpcAddr = &addr
				}
			} else if !errors.Is(offFoghornErr, sql.ErrNoRows) {
				s.logger.WithError(offFoghornErr).WithField("cluster_id", officialClusterID).Warn("Failed to resolve official Foghorn address")
			}
		}
	}

	// Build cluster_peers: all clusters this tenant has access to (best-effort).
	// Resolves Foghorn gRPC address per peer so Commodore can route commands to
	// any cluster. region_id / cell_id / cluster_class / health_status ride
	// along so Commodore's plan-aware route filter can reject ineligible peers
	// without a second round-trip.
	peerRows, peerErr := queries.ListTenantClusterRoutingPeers(ctx, tenantID)
	if peerErr == nil {
		for _, peerRow := range peerRows {
			foghornGrpcAddr, _ := buildAdvertiseAddr(peerRow.FoghornAdvertiseHost, peerRow.FoghornPort)
			var role string
			switch peerRow.ClusterID {
			case primaryClusterID:
				role = "preferred"
			case officialClusterID:
				role = "official"
			default:
				role = "subscribed"
			}
			resp.ClusterPeers = append(resp.ClusterPeers, &clusterpeerpb.TenantClusterPeer{
				ClusterId:       peerRow.ClusterID,
				ClusterSlug:     dns.SanitizeLabel(peerRow.ClusterID),
				BaseUrl:         peerRow.BaseUrl,
				ClusterName:     peerRow.ClusterName,
				Role:            role,
				ClusterType:     peerRow.ClusterType,
				FoghornGrpcAddr: foghornGrpcAddr,
				S3Bucket:        peerRow.S3Bucket,
				S3Endpoint:      peerRow.S3Endpoint,
				S3Region:        peerRow.S3Region,
				S3Prefix:        peerRow.S3Prefix,
				S3PrefixPresent: peerRow.S3PrefixPresent,
				RegionId:        peerRow.RegionID,
				CellId:          peerRow.CellID,
				ClusterClass:    peerRow.ClusterClass,
				HealthStatus:    peerRow.HealthStatus,
			})
		}
	}

	return &resp, nil
}

// ensureServiceExists atomically gets or creates a service catalog entry.
// Uses pg_advisory_xact_lock to serialize concurrent callers for the same
// service type, preventing the TOCTOU race where two instances both see
// "no rows" and both try to INSERT.
func (s *QuartermasterServer) ensureServiceExists(ctx context.Context, serviceType, protocol string) (string, error) {
	var serviceID string
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		queries := quartermasterdb.New(tx)
		// Advisory lock keyed on service type — second caller blocks until first commits
		err := queries.LockServiceType(ctx, serviceType)
		if err != nil {
			return status.Errorf(codes.Internal, "failed to acquire advisory lock: %v", err)
		}

		serviceID, err = queries.FindServiceID(ctx, serviceType)

		if errors.Is(err, sql.ErrNoRows) {
			serviceID = serviceType
			err = queries.CreateServiceCatalogEntry(ctx, quartermasterdb.CreateServiceCatalogEntryParams{
				ServiceID: serviceID, Name: serviceType, ServiceType: serviceType, Protocol: protocol,
			})
			if err != nil {
				s.logger.WithError(err).WithField("service_type", serviceType).Error("Failed to create service")
				return status.Errorf(codes.Internal, "failed to create service: %v", err)
			}
		} else if err != nil {
			return status.Errorf(codes.Internal, "database error: %v", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return serviceID, nil
}

// BootstrapService handles service registration with idempotent instance management
func (s *QuartermasterServer) BootstrapService(ctx context.Context, req *quartermasterpb.BootstrapServiceRequest) (*quartermasterpb.BootstrapServiceResponse, error) {
	serviceType := req.GetType()
	if serviceType == "" {
		return nil, status.Error(codes.InvalidArgument, "type required")
	}

	queries := quartermasterdb.New(s.db)
	var tx *sql.Tx
	token := req.GetToken()
	if token != "" {
		var err error
		tx, err = s.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to begin transaction: %v", err)
		}
		queries = queries.WithTx(tx)
		defer func() {
			if tx != nil {
				_ = tx.Rollback()
			}
		}()
	}

	// 1. Resolve cluster from token, request, or fallback (single cluster only)
	var clusterID string
	var tokenBoundClusterID string

	if token != "" {
		tokenRow, err := queries.LockServiceBootstrapToken(ctx, hashBootstrapToken(token))
		if errors.Is(err, sql.ErrNoRows) || tokenRow.Kind != "service" || time.Now().After(tokenRow.ExpiresAt) {
			return nil, status.Error(codes.Unauthenticated, "invalid bootstrap token")
		}
		if err != nil {
			return nil, status.Errorf(codes.Internal, "database error: %v", err)
		}
		tokenBoundClusterID = tokenRow.ClusterID
	}

	// Priority: token-bound cluster > request cluster_id > single active cluster fallback
	requestClusterID := req.GetClusterId()

	if tokenBoundClusterID != "" {
		// Token is bound to a cluster - use it (and validate request match if provided)
		if requestClusterID != "" && requestClusterID != tokenBoundClusterID {
			return nil, status.Errorf(codes.InvalidArgument, "request cluster_id '%s' does not match token-bound cluster '%s'", requestClusterID, tokenBoundClusterID)
		}
		clusterID = tokenBoundClusterID
	} else if requestClusterID != "" {
		// No token-bound cluster, but request provides cluster_id - validate it exists and is active
		isActive, err := queries.GetClusterActiveState(ctx, requestClusterID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "cluster '%s' not found", requestClusterID)
		}
		if err != nil {
			return nil, status.Errorf(codes.Internal, "database error: %v", err)
		}
		if !isActive.Valid {
			return nil, status.Errorf(codes.Internal, "database error: cluster '%s' is_active is NULL", requestClusterID)
		}
		if !isActive.Bool {
			return nil, status.Errorf(codes.FailedPrecondition, "cluster '%s' is not active", requestClusterID)
		}
		clusterID = requestClusterID
	} else {
		// No token-bound cluster and no request cluster_id
		// Fallback: only allow if exactly 1 active cluster exists (dev convenience)
		activeClusters, err := queries.ListActiveClusterIDs(ctx)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "database error: %v", err)
		}
		if len(activeClusters) == 0 {
			return nil, status.Error(codes.Unavailable, "no active cluster available")
		}
		if len(activeClusters) > 1 {
			return nil, status.Errorf(codes.InvalidArgument, "cluster_id required: multiple active clusters exist (%d)", len(activeClusters))
		}
		// Exactly 1 active cluster - use it (dev/single-cluster convenience)
		clusterID = activeClusters[0]
		s.logger.WithField("cluster_id", clusterID).Debug("Auto-selected single active cluster for bootstrap")
	}

	// 2. Derive protocol and advertise host
	proto := strings.ToLower(strings.TrimSpace(req.GetProtocol()))
	if proto == "" {
		proto = "http"
	}

	var nodeIP string
	registrationClusterID := clusterID
	if req.NodeId == nil && dns.IsPoolAssignedServiceType(serviceType) {
		return nil, status.Errorf(codes.InvalidArgument, "node_id required for pool-assigned service %q", serviceType)
	}
	if req.NodeId != nil {
		node, err := queries.GetNodeBootstrapLocation(ctx, *req.NodeId)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "node '%s' not found", *req.NodeId)
		}
		if err != nil {
			return nil, status.Errorf(codes.Internal, "database error: %v", err)
		}
		nodeClusterID := node.ClusterID
		nodeClusterActive, err := queries.GetClusterActiveState(ctx, nodeClusterID)
		if errors.Is(err, sql.ErrNoRows) || (nodeClusterActive.Valid && !nodeClusterActive.Bool) {
			return nil, status.Errorf(codes.InvalidArgument, "node '%s' belongs to inactive or unknown cluster '%s'", *req.NodeId, nodeClusterID)
		}
		if err != nil {
			return nil, status.Errorf(codes.Internal, "database error: %v", err)
		}
		if !nodeClusterActive.Valid {
			return nil, status.Errorf(codes.Internal, "database error: cluster '%s' is_active is NULL", nodeClusterID)
		}
		if !dns.IsPoolAssignedServiceType(serviceType) && nodeClusterID != clusterID {
			return nil, status.Errorf(codes.InvalidArgument, "node '%s' belongs to cluster '%s', not '%s'", *req.NodeId, nodeClusterID, clusterID)
		}
		if nodeClusterID != "" {
			registrationClusterID = nodeClusterID
		}
		if node.NodeIP.Valid {
			nodeIP = strings.TrimSpace(node.NodeIP.String)
		}
	}

	requestedAdvertiseHost := req.GetAdvertiseHost()
	if requestedAdvertiseHost == "" {
		requestedAdvertiseHost = req.GetHost()
	}

	advHost := ""
	// Loopback node addresses are namespace-local; prefer an explicit service
	// host so local Docker services remain reachable from Quartermaster.
	if req.NodeId != nil && nodeIP != "" && !isLoopbackAddress(nodeIP) {
		advHost = nodeIP
	} else {
		advHost = requestedAdvertiseHost
	}
	if advHost == "" {
		advHost = nodeIP
	}
	if advHost == "" {
		return nil, status.Error(codes.InvalidArgument, "advertise_host or host required (or provide node_id with a registered node address)")
	}

	// 3. Get or create service record (serialized via advisory lock to prevent TOCTOU races)
	defaultProtocol := strings.ToLower(strings.TrimSpace(req.GetProtocol()))
	if defaultProtocol == "" {
		defaultProtocol = "http"
	}
	serviceID, err := s.ensureServiceExists(ctx, serviceType, defaultProtocol)
	if err != nil {
		return nil, err
	}

	// 4. Normalize service ID for instance naming
	sluggedID := strings.ToLower(strings.TrimSpace(serviceID))
	sluggedID = strings.ReplaceAll(sluggedID, " ", "-")
	sluggedID = strings.ReplaceAll(sluggedID, "_", "-")
	instanceID := fmt.Sprintf("inst-%s-%s", sluggedID, uuid.NewString()[:8])

	healthEndpoint := req.HealthEndpoint
	port := req.GetPort()
	metadataJSON, err := marshalStringMapJSON(req.GetMetadata())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid metadata: %v", err)
	}
	if req.GetClearMetadata() {
		emptyMetadata := "{}"
		metadataJSON = &emptyMetadata
	}
	metadata := req.GetMetadata()
	isFoghornControlListener := serviceID == "foghorn" && proto == "grpc" && (port == foghornInternalGRPCPort || metadata["foghorn_listener"] == foghornListenerInternalControl)
	requestedInstanceID := strings.TrimSpace(metadata["instance_id"])
	if isFoghornControlListener && requestedInstanceID != "" {
		instanceID = requestedInstanceID
	}

	// 5a. Auto-associate with node by IP when no explicit node_id provided.
	// If advHost is a hostname, resolve it to an IP first.
	resolvedNodeID := req.NodeId
	if resolvedNodeID == nil && advHost != "" {
		matchIP := advHost
		if net.ParseIP(matchIP) == nil {
			if addrs, lookupErr := net.DefaultResolver.LookupHost(ctx, matchIP); lookupErr == nil && len(addrs) > 0 {
				matchIP = addrs[0]
			}
		}
		if net.ParseIP(matchIP) != nil {
			matchedNodeID, matchErr := queries.FindNodeByClusterIP(ctx, quartermasterdb.FindNodeByClusterIPParams{
				ClusterID: registrationClusterID, Ip: matchIP,
			})
			if matchErr != nil && !errors.Is(matchErr, sql.ErrNoRows) {
				s.logger.WithError(matchErr).WithField("cluster_id", registrationClusterID).Debug("Failed to auto-associate service with node")
			}
			if matchedNodeID != "" {
				resolvedNodeID = &matchedNodeID
				s.logger.WithFields(logging.Fields{
					"service_type": serviceType,
					"node_id":      matchedNodeID,
					"advHost":      advHost,
					"resolvedIP":   matchIP,
				}).Debug("Auto-associated service with node via IP match")
			}
		}
	}

	// 5b. Idempotent registration: check for existing instance
	var existingID, existingInstanceID string
	var existingErr error
	if isFoghornControlListener && requestedInstanceID != "" {
		row, queryErr := queries.FindServiceInstanceByRequestedID(ctx, quartermasterdb.FindServiceInstanceByRequestedIDParams{
			ServiceID: serviceID, ClusterID: registrationClusterID, Protocol: proto, InstanceID: requestedInstanceID,
		})
		existingID, existingInstanceID, existingErr = row.ID, row.InstanceID, queryErr
	} else if resolvedNodeID != nil && isFoghornControlListener {
		row, queryErr := queries.FindServiceInstanceByNodeHost(ctx, quartermasterdb.FindServiceInstanceByNodeHostParams{
			ServiceID: serviceID, ClusterID: registrationClusterID, Protocol: proto, Port: port,
			NodeID: *resolvedNodeID, AdvertiseHost: advHost,
		})
		existingID, existingInstanceID, existingErr = row.ID, row.InstanceID, queryErr
	} else if resolvedNodeID != nil {
		row, queryErr := queries.FindServiceInstanceByNode(ctx, quartermasterdb.FindServiceInstanceByNodeParams{
			ServiceID: serviceID, ClusterID: registrationClusterID, Protocol: proto, Port: port, NodeID: *resolvedNodeID,
		})
		existingID, existingInstanceID, existingErr = row.ID, row.InstanceID, queryErr
	} else {
		row, queryErr := queries.FindServiceInstanceByHost(ctx, quartermasterdb.FindServiceInstanceByHostParams{
			ServiceID: serviceID, ClusterID: registrationClusterID, Protocol: proto, Port: port, AdvertiseHost: advHost,
		})
		existingID, existingInstanceID, existingErr = row.ID, row.InstanceID, queryErr
	}
	if existingErr != nil && !errors.Is(existingErr, sql.ErrNoRows) {
		return nil, status.Errorf(codes.Internal, "failed to lookup existing service instance: %v", existingErr)
	}
	registeredNodeID := ""
	if resolvedNodeID != nil {
		registeredNodeID = *resolvedNodeID
	}

	if existingID != "" {
		// Update existing row
		err = queries.UpdateBootstrappedServiceInstance(ctx, quartermasterdb.UpdateBootstrappedServiceInstanceParams{
			AdvertiseHost: advHost, HealthEndpoint: healthEndpoint, Version: req.GetVersion(),
			NodeID: resolvedNodeID, Metadata: metadataJSON, Protocol: proto, Port: port, ID: existingID,
		})
		if err != nil {
			s.logger.WithError(err).Error("Failed to update service instance")
			return nil, status.Errorf(codes.Internal, "failed to update service instance: %v", err)
		}
		instanceID = existingInstanceID
		s.logger.WithFields(logging.Fields{
			"service_type":     serviceType,
			"service_id":       serviceID,
			"instance_id":      instanceID,
			"cluster_id":       registrationClusterID,
			"logical_cluster":  clusterID,
			"node_id":          registeredNodeID,
			"protocol":         proto,
			"advertise_host":   advHost,
			"port":             port,
			"health_endpoint":  req.GetHealthEndpoint(),
			"registration_op":  "update",
			"health_status":    "unknown",
			"last_check_reset": true,
		}).Info("Service instance registered")
	} else {
		// Insert new row
		err = queries.CreateBootstrappedServiceInstance(ctx, quartermasterdb.CreateBootstrappedServiceInstanceParams{
			InstanceID: instanceID, ClusterID: registrationClusterID, NodeID: resolvedNodeID,
			ServiceID: serviceID, Protocol: proto, AdvertiseHost: advHost, HealthEndpoint: healthEndpoint,
			Version: req.GetVersion(), Port: port, Metadata: metadataJSON,
		})
		if err != nil {
			s.logger.WithError(err).Error("Failed to create service instance")
			return nil, status.Errorf(codes.Internal, "failed to create service instance: %v", err)
		}
		s.logger.WithFields(logging.Fields{
			"service_type":    serviceType,
			"service_id":      serviceID,
			"instance_id":     instanceID,
			"cluster_id":      registrationClusterID,
			"logical_cluster": clusterID,
			"node_id":         registeredNodeID,
			"protocol":        proto,
			"advertise_host":  advHost,
			"port":            port,
			"health_endpoint": req.GetHealthEndpoint(),
			"registration_op": "create",
			"health_status":   "unknown",
		}).Info("Service instance registered")
	}

	// 6. Look up cluster owner tenant for dual-tenant attribution
	ownerTenantID, ownerErr := queries.GetClusterOwnerTenantID(ctx, clusterID)
	if ownerErr != nil && !errors.Is(ownerErr, sql.ErrNoRows) {
		s.logger.WithError(ownerErr).WithField("cluster_id", clusterID).Warn("Failed to resolve cluster owner for service attribution")
	}

	if token != "" {
		rowsAffected, err := queries.ConsumeServiceBootstrapToken(ctx, hashBootstrapToken(token))
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to consume bootstrap token: %v", err)
		}
		if rowsAffected != 1 {
			return nil, status.Error(codes.Unauthenticated, "invalid bootstrap token")
		}
	}

	if tx != nil {
		if err := tx.Commit(); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to commit bootstrap transaction: %v", err)
		}
		tx = nil
	}

	// Best-effort cleanup — runs outside the transaction so failures
	// don't abort the already-committed bootstrap.
	cleanupQueries := quartermasterdb.New(s.db)
	if cleanupErr := cleanupQueries.StopDuplicateServiceInstances(ctx, quartermasterdb.StopDuplicateServiceInstancesParams{
		ServiceID: serviceID, ClusterID: registrationClusterID, InstanceID: instanceID,
		Protocol: proto, AdvertiseHost: advHost, Port: port,
	}); cleanupErr != nil {
		s.logger.WithError(cleanupErr).WithField("instance_id", instanceID).Warn("Failed to stop duplicate service instances")
	}
	if isFoghornControlListener {
		if cleanupErr := cleanupQueries.StopStaleFoghornControlListeners(ctx, quartermasterdb.StopStaleFoghornControlListenersParams{
			ServiceID: serviceID, ClusterID: registrationClusterID, InstanceID: instanceID,
			Protocol: proto, AdvertiseHost: advHost, Port: port,
		}); cleanupErr != nil {
			s.logger.WithError(cleanupErr).WithField("instance_id", instanceID).Warn("Failed to stop stale Foghorn listeners")
		}
	}

	resp := &quartermasterpb.BootstrapServiceResponse{
		ServiceId:  serviceID,
		InstanceId: instanceID,
		ClusterId:  clusterID,
	}
	if ownerTenantID.Valid && ownerTenantID.String != "" {
		resp.OwnerTenantId = &ownerTenantID.String
	}
	if resolvedNodeID != nil {
		resp.NodeId = resolvedNodeID
		if node, nodeErr := s.queryNode(ctx, *resolvedNodeID); nodeErr == nil {
			resp.Node = node
		}
	}
	if advHost != "" && port > 0 {
		addr := net.JoinHostPort(advHost, strconv.Itoa(int(port)))
		resp.AdvertiseAddr = &addr
	}

	// A (re)registered pool/physical instance changes DNS membership: the node-keyed
	// infra record and the pooled record of every media cluster it is assigned to
	// serve. Wake by served cluster (with a physical-only fallback for a brand-new
	// gateway that has no assignment yet) instead of waiting for the reconcile tick.
	if dns.IsPhysicalEndpointServiceType(serviceType) || dns.IsPoolAssignedServiceType(serviceType) {
		s.fireNavigatorSyncForPoolClusters(serviceType, s.servedClustersForInstanceName(ctx, instanceID, serviceType))
	}
	return resp, nil
}

// GetNodeOwner returns the owner tenant for a node
func (s *QuartermasterServer) GetNodeOwner(ctx context.Context, req *quartermasterpb.GetNodeOwnerRequest) (*quartermasterpb.NodeOwnerResponse, error) {
	nodeID := req.GetNodeId()
	if nodeID == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id required")
	}

	row, err := quartermasterdb.New(s.db).GetNodeOwnerRecord(ctx, nodeID)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "Node not found")
	}

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"node_id": nodeID,
			"error":   err,
		}).Error("Database error getting node owner")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	resp := quartermasterpb.NodeOwnerResponse{
		NodeId: row.NodeID, ClusterId: row.ClusterID, ClusterName: row.ClusterName,
	}

	if row.OwnerTenantID.Valid {
		resp.OwnerTenantId = &row.OwnerTenantID.String
	}
	if row.Name.Valid {
		resp.TenantName = &row.Name.String
	}
	if addr, ok := buildAdvertiseAddr(row.FoghornHost, row.FoghornPort); ok {
		resp.FoghornGrpcAddr = &addr
	}

	return &resp, nil
}

// DiscoverServices finds instances of a service type with cursor pagination
func (s *QuartermasterServer) DiscoverServices(ctx context.Context, req *quartermasterpb.ServiceDiscoveryRequest) (*quartermasterpb.ServiceDiscoveryResponse, error) {
	serviceType := req.GetServiceType()
	if serviceType == "" {
		return nil, status.Error(codes.InvalidArgument, "service_type required")
	}
	if topology.IsInfraKind(serviceType) {
		return nil, status.Error(codes.InvalidArgument, "infra providers are not service-discoverable")
	}

	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid cursor: %v", err)
	}

	tenantID := middleware.GetTenantID(ctx)
	isPool := dns.IsPoolAssignedServiceType(serviceType)
	if isPool && req.GetClusterId() == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required for pool-assigned service discovery")
	}

	// public_instance_host is per-node physical infrastructure identity, surfaced
	// only to service callers (e.g. Foghorn's broadcaster fanout) — the same
	// boundary ListServiceInstancesByType enforces. Tenant/user discovery gets only
	// the pooled public_host.
	isServiceCaller := ctxkeys.GetAuthType(ctx) == "service"
	physicalSynthesis := isServiceCaller && isPool && dns.IsPhysicalEndpointServiceType(serviceType)

	// For physical-endpoint service types, gate public_instance_host
	// advertisement on a DESIRED physical ingress site existing for the exact
	// FQDN — so a consumer (Foghorn) is not handed a per-instance hostname before
	// the node is even provisioned for it (this is desired ingress state, not
	// proof the cert is applied). Loaded before the main query so it doesn't run
	// on an open rows handle. A gate-read failure propagates as a discovery error
	// (below) so the caller retries rather than caching an empty fanout.
	var provisionedPhysical map[string]struct{}
	if physicalSynthesis {
		var pErr error
		if provisionedPhysical, pErr = s.provisionedPhysicalEndpointFQDNs(ctx); pErr != nil {
			// Don't quietly suppress public_instance_host on a transient gate
			// read failure — that looks like "no physical gateway" to Foghorn,
			// which would cache the empty set and force local AV for the TTL.
			// Surface the error so the caller retries instead of caching.
			return nil, status.Errorf(codes.Internal, "physical endpoint gate lookup failed: %v", pErr)
		}
	}

	scope := quartermasterdb.DiscoveryScopeDefault
	if isServiceCaller {
		scope = quartermasterdb.DiscoveryScopeService
	} else if tenantID != "" {
		scope = quartermasterdb.DiscoveryScopeTenant
	}
	filter := quartermasterdb.ServiceDiscoveryFilter{ServiceType: serviceType, TenantID: tenantID, ClusterID: req.GetClusterId(),
		Scope: scope, Pool: isPool, Physical: physicalSynthesis, StaleThreshold: int32(s.physicalEndpointStaleSeconds),
		Backward: params.Direction == pagination.Backward, Limit: params.Limit + 1}
	if params.Cursor != nil {
		filter.CursorTime, filter.CursorID = &params.Cursor.Timestamp, params.Cursor.ID
	}
	rows, err := quartermasterdb.New(s.db).DiscoverServicesPage(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	var instances []*quartermasterpb.ServiceInstance
	for _, row := range rows {
		inst := quartermasterpb.ServiceInstance{Id: row.ID, InstanceId: row.InstanceID, ServiceId: row.ServiceID,
			ClusterId: row.ClusterID, Protocol: row.Protocol, Status: row.Status, HealthStatus: row.HealthStatus,
			Metadata: unmarshalStringMapJSON(row.Metadata), CreatedAt: timestamppb.New(row.CreatedAt), UpdatedAt: timestamppb.New(row.UpdatedAt)}
		if row.NodeID.Valid {
			inst.NodeId = &row.NodeID.String
		}
		if row.AdvertiseHost.Valid {
			inst.Host = &row.AdvertiseHost.String
		}
		if row.Port.Valid {
			inst.Port = &row.Port.Int32
		}
		if row.HealthEndpoint.Valid {
			inst.HealthEndpoint = &row.HealthEndpoint.String
		}
		if row.LastHealthCheck.Valid {
			inst.LastHealthCheck = timestamppb.New(row.LastHealthCheck.Time)
		}

		if isPool {
			// Synthesize the per-assignment public_host. For an M:N pool, the
			// same physical instance returns a different public_host per
			// requested cluster, so this cannot be stored as static metadata.
			if publicHost := synthesizePublicHost(serviceType, inst.ClusterId, row.ClusterName.String, row.ClusterBaseURL.String); publicHost != "" {
				if inst.Metadata == nil {
					inst.Metadata = map[string]string{}
				}
				inst.Metadata[servicedefs.LivepeerGatewayMetadataPublicHost] = publicHost
			}
			// Synthesize the physical endpoint <service>.<node>.infra.<root>.
			// Unlike public_host, this is anchored on the physical node, not the
			// logical assignment, so a consumer (e.g. Foghorn broadcaster fanout)
			// can address this specific instance for failover. Only emitted for
			// service types that actually provision an infra DNS/ingress/TLS
			// contract — otherwise the metadata would advertise a non-routable
			// name.
			// Advertise public_instance_host only for an instance Navigator will
			// actually publish a physical A record for, mirroring
			// ListServiceInstancesByType's eligibility contract: running/active +
			// healthy + fresh (window from the same config Navigator uses) and
			// node-active + external_ip + desired ingress (the
			// provisionedPhysical gate). Without this, Foghorn could fan out to a
			// hostname with no DNS record (a non-routable broadcaster).
			physicallyEligible := (inst.Status == "running" || inst.Status == "active") &&
				inst.HealthStatus == "healthy" && row.HealthFresh
			if isServiceCaller && physicallyEligible && row.NodeID.Valid && dns.IsPhysicalEndpointServiceType(serviceType) {
				if instanceHost, ok := dns.InfraInstanceFQDN(serviceType, row.NodeID.String, s.platformRootDomain); ok {
					if _, provisioned := provisionedPhysical[strings.ToLower(instanceHost)]; provisioned {
						if inst.Metadata == nil {
							inst.Metadata = map[string]string{}
						}
						inst.Metadata[servicedefs.LivepeerGatewayMetadataPublicInstanceHost] = instanceHost
					}
				}
			}
		}

		instances = append(instances, &inst)
	}
	// Determine pagination info
	resultsLen := len(instances)
	if resultsLen > params.Limit {
		instances = instances[:params.Limit] // Remove the extra item
	}

	// Reverse results for backward pagination to maintain consistent order
	if params.Direction == pagination.Backward {
		slices.Reverse(instances)
	}

	// Build cursors
	var startCursor, endCursor string
	if len(instances) > 0 {
		first := instances[0]
		last := instances[len(instances)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.Id)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.Id)
	}

	// Build response with cursor pagination
	resp := &quartermasterpb.ServiceDiscoveryResponse{
		Instances:  instances,
		Pagination: pagination.BuildResponse(resultsLen, params.Limit, params.Direction, int32(len(instances)), startCursor, endCursor),
	}

	return resp, nil
}

// ============================================================================
// SERVICE POOL MANAGEMENT
// ============================================================================

func (s *QuartermasterServer) GetServicePoolStatus(ctx context.Context, req *quartermasterpb.GetServicePoolStatusRequest) (*quartermasterpb.GetServicePoolStatusResponse, error) {
	serviceType, err := resolveAssignmentServiceType(req.GetServiceType())
	if err != nil {
		return nil, err
	}

	rows, err := quartermasterdb.New(s.db).ListServicePoolStatus(ctx, serviceType)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	clusterMap := make(map[string]*quartermasterpb.ServicePoolClusterEntry)
	seenInstances := make(map[string]bool)
	var total, unassigned, assigned int32
	var assignments []*quartermasterpb.ServiceInstanceAssignment

	for _, row := range rows {
		id, instanceID, host, instStatus, assignedCluster := row.ID, row.InstanceID, row.Host, row.Status, row.AssignedCluster
		port, createdAt := row.Port, row.CreatedAt

		// Count unique instances
		if !seenInstances[id] {
			seenInstances[id] = true
			total++
			if assignedCluster == "" {
				unassigned++
			}
		}

		// Track assignments
		if assignedCluster != "" {
			if !seenInstances[id+":counted"] {
				seenInstances[id+":counted"] = true
				assigned++
			}
			assignments = append(assignments, &quartermasterpb.ServiceInstanceAssignment{
				InstanceId: id,
				ClusterId:  assignedCluster,
				IsActive:   true,
				CreatedAt:  timestamppb.New(createdAt),
			})
		}

		// Group assignments by logical cluster for the pool-status response.
		clusterID := assignedCluster
		entry, ok := clusterMap[clusterID]
		if !ok {
			entry = &quartermasterpb.ServicePoolClusterEntry{ClusterId: clusterID}
			clusterMap[clusterID] = entry
		}
		entry.InstanceCount++
		entry.Instances = append(entry.Instances, &quartermasterpb.ServiceInstance{
			Id:         id,
			InstanceId: instanceID,
			ClusterId:  clusterID,
			Host:       &host,
			Port:       &port,
			Status:     instStatus,
			CreatedAt:  timestamppb.New(createdAt),
		})
	}

	clusters := make([]*quartermasterpb.ServicePoolClusterEntry, 0, len(clusterMap))
	for _, entry := range clusterMap {
		clusters = append(clusters, entry)
	}

	return &quartermasterpb.GetServicePoolStatusResponse{
		Total:       total,
		Unassigned:  unassigned,
		Assigned:    assigned,
		Clusters:    clusters,
		Assignments: assignments,
	}, nil
}

func resolveAssignmentServiceType(svcType string) (string, error) {
	if t := strings.TrimSpace(svcType); t != "" {
		if !dns.IsPoolAssignedServiceType(t) {
			return "", status.Errorf(codes.InvalidArgument, "service_type %q is not pool-assigned", t)
		}
		return t, nil
	}
	return "", status.Error(codes.InvalidArgument, "service_type required")
}

func (s *QuartermasterServer) AddToServicePool(ctx context.Context, req *quartermasterpb.AddToServicePoolRequest) (*quartermasterpb.AddToServicePoolResponse, error) {
	serviceType, err := resolveAssignmentServiceType(req.GetServiceType())
	if err != nil {
		return nil, err
	}

	var affectedClusters []string
	var released int64
	var poolErr error
	if ids := req.GetInstanceIds(); len(ids) > 0 {
		// DELETE ... RETURNING captures the affected clusters atomically with the
		// mutation, so a failed read can't commit the change without a wake.
		affectedClusters, released, poolErr = quartermasterdb.New(s.db).ReleaseServicePoolInstances(ctx, quartermasterdb.ServicePoolInstancesParams{
			InstanceIDs: ids, ServiceType: serviceType,
		})
		if poolErr != nil {
			return nil, status.Errorf(codes.Internal, "database error: %v", poolErr)
		}
	} else if req.GetCount() > 0 && req.GetFromClusterId() != "" {
		affectedClusters = []string{req.GetFromClusterId()}
		// Remove N oldest assignments from a specific cluster.
		released, poolErr = quartermasterdb.New(s.db).ReleaseOldestServicePoolInstances(ctx, quartermasterdb.ReleaseOldestServicePoolParams{
			ClusterID: req.GetFromClusterId(), Count: req.GetCount(), ServiceType: serviceType,
		})
		if poolErr != nil {
			return nil, status.Errorf(codes.Internal, "database error: %v", poolErr)
		}
	} else {
		return nil, status.Error(codes.InvalidArgument, "provide instance_ids or (count + from_cluster_id)")
	}

	// Pool membership shrank for these clusters; wake Navigator to drop them now.
	s.fireNavigatorSyncForPoolClusters(serviceType, affectedClusters)
	return &quartermasterpb.AddToServicePoolResponse{Released: int32(released)}, nil
}

func (s *QuartermasterServer) DrainServiceInstance(ctx context.Context, req *quartermasterpb.DrainServiceInstanceRequest) (*quartermasterpb.DrainServiceInstanceResponse, error) {
	instanceID := req.GetInstanceId()
	if instanceID == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	serviceType, err := resolveAssignmentServiceType(req.GetServiceType())
	if err != nil {
		return nil, err
	}

	// DELETE ... RETURNING captures the served clusters atomically with the mutation,
	// so a failed read can never commit the drain without waking those clusters.
	drainedClusters, _, err := quartermasterdb.New(s.db).DrainServicePoolInstance(ctx, quartermasterdb.ServicePoolInstanceParams{
		InstanceID: instanceID, ServiceType: serviceType,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if len(drainedClusters) == 0 {
		return nil, status.Errorf(codes.NotFound, "instance not found or not a %s instance", serviceType)
	}

	// Wake every cluster this instance served so their pooled records drop it now.
	s.fireNavigatorSyncForPoolClusters(serviceType, drainedClusters)
	return &quartermasterpb.DrainServiceInstanceResponse{PreviousClusterId: drainedClusters[0]}, nil
}

func (s *QuartermasterServer) AssignServiceToCluster(ctx context.Context, req *quartermasterpb.AssignServiceToClusterRequest) (*emptypb.Empty, error) {
	clusterID := req.GetClusterId()
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}
	serviceType, err := resolveAssignmentServiceType(req.GetServiceType())
	if err != nil {
		return nil, err
	}

	queries := quartermasterdb.New(s.db)
	exists, existsErr := queries.ServicePoolClusterActive(ctx, clusterID)
	if existsErr != nil || !exists {
		return nil, status.Error(codes.NotFound, "cluster not found or inactive")
	}

	if ids := req.GetInstanceIds(); len(ids) > 0 {
		for _, instID := range ids {
			// ON CONFLICT preserves the existing source: a runtime
			// AssignServiceToCluster against a GitOps-owned row reactivates
			// without flipping ownership. Only explicit adopt/unmanage
			// operations flip provenance.
			affected, err := queries.AssignServicePoolInstance(ctx, quartermasterdb.AssignServicePoolInstanceParams{
				ClusterID: clusterID, InstanceID: instID, ServiceType: serviceType,
			})
			if err != nil {
				return nil, status.Errorf(codes.Internal, "failed to assign instance %s: %v", instID, err)
			}
			if affected == 0 {
				return nil, status.Errorf(codes.NotFound, "%s instance %s not found or not running", serviceType, instID)
			}
		}
	} else if count := req.GetCount(); count > 0 {
		// Same ON CONFLICT contract as the instance-ids branch: preserve
		// existing source on reactivation.
		affected, err := queries.AssignServicePoolCount(ctx, quartermasterdb.AssignServicePoolCountParams{
			ClusterID: clusterID, Count: count, ServiceType: serviceType,
		})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to assign %s instances: %v", serviceType, err)
		}
		if affected < int64(count) {
			return nil, status.Errorf(codes.FailedPrecondition, "assigned %d %s instances, requested %d", affected, serviceType, count)
		}
	} else {
		return nil, status.Error(codes.InvalidArgument, "provide instance_ids or count")
	}

	// SCA is the pooled-DNS membership for this media cluster; wake Navigator so
	// livepeer.<cluster>/foghorn.<cluster>/… (and the node-keyed physical records)
	// converge on the assignment immediately, not on the next reconcile tick.
	s.fireNavigatorSyncForPoolClusters(serviceType, []string{clusterID})
	return &emptypb.Empty{}, nil
}

func (s *QuartermasterServer) UnassignServiceFromCluster(ctx context.Context, req *quartermasterpb.UnassignServiceFromClusterRequest) (*emptypb.Empty, error) {
	clusterID := req.GetClusterId()
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}

	ids := req.GetInstanceIds()
	if len(ids) == 0 {
		return nil, status.Error(codes.InvalidArgument, "instance_ids required")
	}
	serviceType, err := resolveAssignmentServiceType(req.GetServiceType())
	if err != nil {
		return nil, err
	}

	err = quartermasterdb.New(s.db).UnassignServicePoolInstances(ctx, quartermasterdb.UnassignServicePoolParams{
		ClusterID: clusterID, InstanceIDs: ids, ServiceType: serviceType,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to unassign: %v", err)
	}

	// The cluster lost pooled-DNS members; wake Navigator to drop them now.
	s.fireNavigatorSyncForPoolClusters(serviceType, []string{clusterID})
	return &emptypb.Empty{}, nil
}

// EnableSelfHosting creates a tenant's private cluster, assigns it to a shared
// Foghorn (least-loaded running instance), and returns an enrollment token.
func (s *QuartermasterServer) EnableSelfHosting(ctx context.Context, req *quartermasterpb.EnableSelfHostingRequest) (*quartermasterpb.EnableSelfHostingResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		tenantID = middleware.GetTenantID(ctx)
	}
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	clusterName := req.GetClusterName()
	if clusterName == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_name required")
	}

	userID := middleware.GetUserID(ctx)

	// Check tenant's cluster ownership limit
	queries := quartermasterdb.New(s.db)
	ownership, err := queries.GetTenantClusterOwnershipLimit(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "tenant not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !ownership.MaxOwnedClusters.Valid || !ownership.IsProvider.Valid {
		return nil, status.Error(codes.Internal, "database error: required tenant ownership field is NULL")
	}
	if !ownership.IsProvider.Bool && ownership.CurrentOwnedClusters >= int64(ownership.MaxOwnedClusters.Int32) {
		return nil, status.Errorf(codes.ResourceExhausted, "tenant has reached maximum owned clusters limit (%d)", ownership.MaxOwnedClusters.Int32)
	}

	// Generate cluster ID from name
	clusterID := strings.ToLower(strings.ReplaceAll(clusterName, " ", "-"))
	suffix, err := generateSecureToken(4)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate cluster ID suffix: %v", err)
	}
	clusterID = fmt.Sprintf("%s-%s", clusterID, suffix)

	id := uuid.New().String()
	now := time.Now()
	requestedRegion := strings.TrimSpace(req.GetRegion())
	clientIPForSelection := strings.TrimSpace(req.GetClientIp())
	if requestedRegion != "" {
		clientIPForSelection = ""
	}
	if requestedRegion == "" {
		preferredRegion, regionErr := queries.GetTenantPreferredClusterRegion(ctx, tenantID)
		if regionErr != nil && !errors.Is(regionErr, sql.ErrNoRows) {
			return nil, status.Errorf(codes.Internal, "failed to resolve tenant preferred cluster region: %v", regionErr)
		}
		requestedRegion = strings.TrimSpace(preferredRegion.String)
	}

	controlCell, err := s.selectFoghornControlCell(ctx, req.GetControlClusterId(), requestedRegion, clientIPForSelection)
	if err != nil {
		return nil, err
	}
	regionForRow := strings.TrimSpace(controlCell.regionID)

	// One transaction wraps every write that makes up a self-hosted cluster:
	// the cluster row, the owner's tenant_cluster_access grant, the Foghorn
	// service_cluster_assignments junction, the bootstrap token, and the
	// service-event outbox emits. A failure on any of these rolls the whole
	// thing back so we never publish a tenant_private cluster without owner
	// access or without a Foghorn assignment.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	txQueries := quartermasterdb.New(tx)
	if err = txQueries.CreatePrivateInfrastructureCluster(ctx, quartermasterdb.CreatePrivateInfrastructureClusterParams{
		ID: id, ClusterID: clusterID, ClusterName: clusterName, OwnerTenantID: tenantID,
		BaseURL: strings.TrimSpace(controlCell.baseURL), ShortDescription: req.ShortDescription,
		RegionID: regionForRow, ControlCellID: controlCell.controlCellID, CreatedAt: now,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create cluster: %v", err)
	}

	if err = txQueries.GrantPrivateClusterOwnerAccess(ctx, quartermasterdb.GrantPrivateClusterOwnerAccessParams{
		TenantID: tenantID, ClusterID: clusterID,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to auto-subscribe owner to cluster: %v", err)
	}

	// EnableSelfHosting attaches a tenant's Foghorn to a cluster. New rows
	// are runtime-owned; ON CONFLICT preserves source (a GitOps default for
	// this Foghorn would not be silently demoted to runtime here).
	if err = txQueries.AssignRuntimeFoghornToPrivateCluster(ctx, quartermasterdb.AssignRuntimeFoghornToPrivateClusterParams{
		ClusterID: clusterID, ServiceInstanceID: controlCell.instanceID,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to assign Foghorn to cluster: %v", err)
	}

	// Create bootstrap token
	tokenID := uuid.New().String()
	token, err := generateSecureToken(32)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate token: %v", err)
	}
	expiresAt := now.Add(30 * 24 * time.Hour)

	if err = txQueries.CreateEdgeBootstrapTokenRecord(ctx, quartermasterdb.CreateEdgeBootstrapTokenRecordParams{
		ID: tokenID, TokenHash: hashBootstrapToken(token), TokenPrefix: tokenPrefix(token),
		Name: fmt.Sprintf("Bootstrap token for %s", clusterName), TenantID: tenantID,
		ClusterID: validString(clusterID), ExpiresAt: expiresAt,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create bootstrap token: %v", err)
	}

	if enqErr := s.emitClusterEventTx(ctx, tx, eventClusterCreated, tenantID, userID, clusterID, "cluster", clusterID, "", "", ""); enqErr != nil {
		return nil, status.Errorf(codes.Internal, "enqueue cluster_created: %v", enqErr)
	}
	if enqErr := s.emitClusterEventTx(ctx, tx, eventTenantClusterAssigned, tenantID, userID, clusterID, "cluster", clusterID, "", "", ""); enqErr != nil {
		return nil, status.Errorf(codes.Internal, "enqueue tenant_cluster_assigned: %v", enqErr)
	}
	if enqErr := s.enqueueTenantAliasEnsureTx(ctx, tx, tenantID, true); enqErr != nil {
		return nil, status.Errorf(codes.Internal, "enqueue tenant-alias ensure: %v", enqErr)
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return nil, status.Errorf(codes.Internal, "commit self-hosting enable: %v", commitErr)
	}

	// The cluster now has a pooled foghorn assignment; wake foghorn.<cluster> now.
	s.fireNavigatorSyncForPoolClusters("foghorn", []string{clusterID})

	cluster, err := s.queryCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}

	return &quartermasterpb.EnableSelfHostingResponse{
		Cluster: cluster,
		BootstrapToken: &quartermasterpb.BootstrapToken{
			Id:        tokenID,
			Token:     token,
			Kind:      "edge_node",
			Name:      fmt.Sprintf("Bootstrap token for %s", clusterName),
			TenantId:  &tenantID,
			ClusterId: &clusterID,
			ExpiresAt: timestamppb.New(expiresAt),
			CreatedAt: timestamppb.New(now),
		},
		FoghornAddr: publicFoghornGRPCAddr(clusterID, controlCell.baseURL),
	}, nil
}

// CreateEnrollmentToken creates a bootstrap token for a cluster lifecycle actor.
func (s *QuartermasterServer) CreateEnrollmentToken(ctx context.Context, req *quartermasterpb.CreateEnrollmentTokenRequest) (*quartermasterpb.CreateBootstrapTokenResponse, error) {
	clusterID := req.GetClusterId()
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}

	callerTenantID := middleware.GetTenantID(ctx)
	serviceAuth := ctxkeys.GetAuthType(ctx) == "service"
	tenantID := req.GetTenantId()
	if tenantID == "" {
		tenantID = callerTenantID
	}
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}
	providerActor, err := s.hasProviderLifecycleAuthority(ctx, callerTenantID)
	if err != nil {
		return nil, err
	}
	lifecycleActor := serviceAuth || providerActor
	if callerTenantID != "" && tenantID != callerTenantID && !lifecycleActor {
		return nil, status.Error(codes.PermissionDenied, "tenant_id does not match caller tenant")
	}

	var authorized bool
	queries := quartermasterdb.New(s.db)
	if lifecycleActor {
		err = database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			var queryErr error
			authorized, queryErr = queries.ActiveClusterExists(ctx, clusterID)
			return queryErr
		})
	} else {
		err = database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			var queryErr error
			authorized, queryErr = queries.TenantHasClusterLifecycleAccess(ctx, quartermasterdb.TenantHasClusterLifecycleAccessParams{
				ClusterID: clusterID, TenantID: tenantID,
			})
			return queryErr
		})
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !authorized {
		return nil, status.Error(codes.PermissionDenied, "cluster lifecycle access required")
	}

	// Parse TTL (default 30 days)
	ttl := 30 * 24 * time.Hour
	if req.GetTtl() != "" {
		parsed, parseErr := time.ParseDuration(req.GetTtl())
		if parseErr != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid ttl: %v", parseErr)
		}
		ttl = parsed
	}

	tokenName := req.GetName()
	if tokenName == "" {
		tokenName = fmt.Sprintf("Enrollment token for %s", clusterID)
	}

	tokenID := uuid.New().String()
	token, err := generateSecureToken(32)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate token: %v", err)
	}
	now := time.Now()
	expiresAt := now.Add(ttl)

	err = database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return queries.CreateEdgeBootstrapTokenRecord(ctx, quartermasterdb.CreateEdgeBootstrapTokenRecordParams{
			ID: tokenID, TokenHash: hashBootstrapToken(token), TokenPrefix: tokenPrefix(token),
			Name: tokenName, TenantID: tenantID, ClusterID: validString(clusterID), ExpiresAt: expiresAt,
		})
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create token: %v", err)
	}

	return &quartermasterpb.CreateBootstrapTokenResponse{
		Token: &quartermasterpb.BootstrapToken{
			Id:        tokenID,
			Token:     token,
			Kind:      "edge_node",
			Name:      tokenName,
			TenantId:  &tenantID,
			ClusterId: &clusterID,
			ExpiresAt: timestamppb.New(expiresAt),
			CreatedAt: timestamppb.New(now),
		},
	}, nil
}

// ============================================================================
// TENANT SERVICE - Additional Methods
// ============================================================================

// ResolveTenant resolves a tenant by subdomain or platform-managed domain (no BYO)
func (s *QuartermasterServer) ResolveTenant(ctx context.Context, req *quartermasterpb.ResolveTenantRequest) (*quartermasterpb.ResolveTenantResponse, error) {
	subdomain := req.GetSubdomain()
	domain := req.GetDomain()

	if subdomain == "" && domain == "" {
		return nil, status.Error(codes.InvalidArgument, "subdomain or domain required")
	}

	var tenantID, tenantName string
	var primaryClusterID sql.NullString
	queries := quartermasterdb.New(s.db)
	var err error
	if subdomain != "" {
		row, queryErr := queries.ResolveTenantBySubdomain(ctx, subdomain)
		tenantID, tenantName, primaryClusterID, err = row.ID, row.Name, row.PrimaryClusterID, queryErr
	} else {
		row, queryErr := queries.ResolveTenantByCustomDomain(ctx, domain)
		tenantID, tenantName, primaryClusterID, err = row.ID, row.Name, row.PrimaryClusterID, queryErr
	}
	if errors.Is(err, sql.ErrNoRows) {
		return &quartermasterpb.ResolveTenantResponse{Found: false, Error: "Tenant not found"}, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	resp := &quartermasterpb.ResolveTenantResponse{
		Found:      true,
		TenantId:   tenantID,
		TenantName: tenantName,
	}
	if primaryClusterID.Valid {
		resp.PrimaryClusterId = primaryClusterID.String
	}

	return resp, nil
}

// ResolveTenantAliases maps each requested bootstrap alias to its persisted
// tenant UUID via quartermaster.bootstrap_tenant_aliases. Aliases that do not
// have a row yet are returned in the `unknown` list rather than failing the
// whole call — callers (Purser/Commodore bootstrap) render a precise error
// telling the operator to run quartermaster bootstrap first.
//
// SERVICE_TOKEN auth: the alias→UUID handoff is service-to-service only.
func (s *QuartermasterServer) ResolveTenantAliases(ctx context.Context, req *quartermasterpb.ResolveTenantAliasesRequest) (*quartermasterpb.ResolveTenantAliasesResponse, error) {
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "ResolveTenantAliases requires service token auth")
	}
	aliases := req.GetAliases()
	if len(aliases) == 0 {
		return &quartermasterpb.ResolveTenantAliasesResponse{Mapping: map[string]string{}}, nil
	}

	rows, err := quartermasterdb.New(s.db).ResolveBootstrapTenantAliases(ctx, aliases)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "query alias map: %v", err)
	}
	mapping := make(map[string]string, len(aliases))
	for _, row := range rows {
		mapping[row.Alias] = row.TenantID
	}

	var unknown []string
	for _, a := range aliases {
		if _, ok := mapping[a]; !ok {
			unknown = append(unknown, a)
		}
	}
	return &quartermasterpb.ResolveTenantAliasesResponse{Mapping: mapping, Unknown: unknown}, nil
}

// ListTenants lists all tenants with pagination
func (s *QuartermasterServer) ListTenants(ctx context.Context, req *quartermasterpb.ListTenantsRequest) (*quartermasterpb.ListTenantsResponse, error) {
	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid cursor: %v", err)
	}

	filter := quartermasterdb.TenantListFilter{Backward: params.Direction == pagination.Backward, Limit: params.Limit + 1}
	if params.Cursor != nil {
		filter.CursorTime, filter.CursorID = &params.Cursor.Timestamp, params.Cursor.ID
	}
	rows, err := quartermasterdb.New(s.db).ListTenantsPage(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	var tenants []*quartermasterpb.Tenant
	for _, row := range rows {
		tenant := &quartermasterpb.Tenant{Id: row.ID, Name: row.Name, PrimaryColor: row.PrimaryColor,
			SecondaryColor: row.SecondaryColor, DeploymentTier: row.DeploymentTier, DeploymentModel: row.DeploymentModel,
			KafkaBrokers: row.KafkaBrokers, IsActive: row.IsActive, MonitoringEnabled: row.MonitoringEnabled,
			CreatedAt: timestamppb.New(row.CreatedAt), UpdatedAt: timestamppb.New(row.UpdatedAt)}
		if row.Subdomain.Valid {
			tenant.Subdomain = &row.Subdomain.String
		}
		if row.CustomDomain.Valid {
			tenant.CustomDomain = &row.CustomDomain.String
		}
		if row.LogoURL.Valid {
			tenant.LogoUrl = &row.LogoURL.String
		}
		if row.PrimaryClusterID.Valid {
			tenant.PrimaryClusterId = &row.PrimaryClusterID.String
		}
		if row.OfficialClusterID.Valid {
			tenant.OfficialClusterId = &row.OfficialClusterID.String
		}
		if row.KafkaTopicPrefix.Valid {
			tenant.KafkaTopicPrefix = &row.KafkaTopicPrefix.String
		}
		if row.DatabaseURL.Valid {
			tenant.DatabaseUrl = &row.DatabaseURL.String
		}
		tenants = append(tenants, tenant)
	}

	// Determine pagination info
	resultsLen := len(tenants)
	if resultsLen > params.Limit {
		tenants = tenants[:params.Limit]
	}

	// Reverse results for backward pagination to maintain consistent order
	if params.Direction == pagination.Backward {
		slices.Reverse(tenants)
	}

	// Build cursors
	var startCursor, endCursor string
	if len(tenants) > 0 {
		first := tenants[0]
		last := tenants[len(tenants)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.Id)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.Id)
	}

	resp := &quartermasterpb.ListTenantsResponse{
		Tenants:    tenants,
		Pagination: pagination.BuildResponse(resultsLen, params.Limit, params.Direction, int32(len(tenants)), startCursor, endCursor),
	}

	return resp, nil
}

// ============================================================================
// CROSS-SERVICE: BILLING BATCH PROCESSING
// ============================================================================

// ListActiveTenants returns active tenants for cross-service batch processing.
// Purser consumes tenant_ids; Skipper consumes tenants for monitoring policy.
func (s *QuartermasterServer) ListActiveTenants(ctx context.Context, req *quartermasterpb.ListActiveTenantsRequest) (*quartermasterpb.ListActiveTenantsResponse, error) {
	rows, err := quartermasterdb.New(s.db).ListActiveTenantRecords(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	var tenantIDs []string
	var tenants []*quartermasterpb.ActiveTenant
	for _, row := range rows {
		tenantIDs = append(tenantIDs, row.ID)
		tenants = append(tenants, &quartermasterpb.ActiveTenant{
			TenantId:          row.ID,
			MonitoringEnabled: row.MonitoringEnabled,
		})
	}

	return &quartermasterpb.ListActiveTenantsResponse{
		TenantIds: tenantIDs,
		Tenants:   tenants,
	}, nil
}

// CreateTenant creates a new tenant
func (s *QuartermasterServer) CreateTenant(ctx context.Context, req *quartermasterpb.CreateTenantRequest) (*quartermasterpb.CreateTenantResponse, error) { //nolint:govet // Provisioning transaction branches use local error scopes.
	name := req.GetName()
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "name required")
	}

	subdomain := strings.ToLower(strings.TrimSpace(req.GetSubdomain()))
	if subdomain != "" {
		if dns.IsReservedTenantSlug(subdomain, s.activeClusterSlugs(ctx)) {
			return nil, status.Errorf(codes.InvalidArgument, "subdomain %q is reserved or invalid", subdomain)
		}
	} else {
		generatedSubdomain, genErr := s.generateAvailableTenantSubdomain(ctx, name)
		if genErr != nil {
			return nil, status.Errorf(codes.Internal, "generate tenant subdomain: %v", genErr)
		}
		subdomain = generatedSubdomain
	}

	userID := middleware.GetUserID(ctx)
	tenantID := uuid.New().String()
	now := time.Now()

	// Start a transaction to ensure atomicity for tenant creation and auto-subscription
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.logger.WithError(err).Error("Failed to begin transaction for tenant creation")
		return nil, status.Errorf(codes.Internal, "failed to begin transaction: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort
	queries := quartermasterdb.New(tx)

	provisioningKey := strings.TrimSpace(req.GetProvisioningKey())
	if len(provisioningKey) > 255 {
		return nil, status.Error(codes.InvalidArgument, "provisioning_key is too long")
	}
	if provisioningKey != "" {
		// Serialize all replicas on the caller's durable idempotency identity.
		// A retry after a lost response returns the original tenant rather than
		// creating an orphan in this cross-service signup saga.
		if lockErr := queries.LockTenantProvisioningKey(ctx, provisioningKey); lockErr != nil {
			return nil, status.Errorf(codes.Internal, "lock tenant provisioning: %v", lockErr)
		}
		var existingTenantID string
		existingTenantID, lookupErr := queries.FindTenantIDByProvisioningKey(ctx, validString(provisioningKey))
		if lookupErr == nil {
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				return nil, status.Errorf(codes.Internal, "finish tenant provisioning lookup: %v", rollbackErr)
			}
			existing, getErr := s.GetTenant(ctx, &quartermasterpb.GetTenantRequest{TenantId: existingTenantID})
			if getErr != nil {
				return nil, getErr
			}
			return &quartermasterpb.CreateTenantResponse{Tenant: existing.GetTenant()}, nil
		}
		if !errors.Is(lookupErr, sql.ErrNoRows) {
			return nil, status.Errorf(codes.Internal, "lookup tenant provisioning key: %v", lookupErr)
		}
	}

	// Self-signup paths omit the tier; default to 'free' so the column never
	// holds '' (Purser stamps the billing-derived tier afterwards).
	deploymentTier := strings.ToLower(strings.TrimSpace(req.GetDeploymentTier()))
	if deploymentTier == "" {
		deploymentTier = "free"
	}

	// 1. Insert into quartermaster.tenants
	createParams := quartermasterdb.CreateTenantRecordParams{
		ID: tenantID, Name: name, Subdomain: validString(subdomain), CustomDomain: optionalString(req.CustomDomain),
		LogoUrl: optionalString(req.LogoUrl), PrimaryColor: validString(req.GetPrimaryColor()),
		SecondaryColor: validString(req.GetSecondaryColor()), DeploymentTier: validString(deploymentTier),
		DeploymentModel: validString(req.GetDeploymentModel()), CreatedAt: now,
	}
	if provisioningKey == "" {
		err = queries.CreateTenantRecord(ctx, createParams)
	} else {
		err = queries.CreateTenantRecordWithProvisioningKey(ctx, quartermasterdb.CreateTenantRecordWithProvisioningKeyParams{
			ID: createParams.ID, Name: createParams.Name, Subdomain: createParams.Subdomain,
			CustomDomain: createParams.CustomDomain, LogoUrl: createParams.LogoUrl,
			PrimaryColor: createParams.PrimaryColor, SecondaryColor: createParams.SecondaryColor,
			DeploymentTier: createParams.DeploymentTier, DeploymentModel: createParams.DeploymentModel,
			CreatedAt: createParams.CreatedAt, ProvisioningKey: validString(provisioningKey),
		})
	}

	if err != nil {
		s.logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to create tenant")
		return nil, status.Errorf(codes.Internal, "failed to create tenant: %v", err)
	}

	attribution := req.GetAttribution()
	if attribution != nil && attribution.GetSignupChannel() != "" {
		metadataJSON := attribution.GetMetadataJson()
		if metadataJSON == "" || !json.Valid([]byte(metadataJSON)) {
			metadataJSON = "{}"
		}
		err = queries.CreateTenantAttribution(ctx, quartermasterdb.CreateTenantAttributionParams{
			TenantID: tenantID, SignupChannel: attribution.GetSignupChannel(), SignupMethod: validString(attribution.GetSignupMethod()),
			UtmSource: validString(attribution.GetUtmSource()), UtmMedium: validString(attribution.GetUtmMedium()),
			UtmCampaign: validString(attribution.GetUtmCampaign()), UtmContent: validString(attribution.GetUtmContent()),
			UtmTerm: validString(attribution.GetUtmTerm()), HttpReferer: validString(attribution.GetHttpReferer()),
			LandingPage: validString(attribution.GetLandingPage()), ReferralCode: validString(attribution.GetReferralCode()),
			IsAgent: validBool(attribution.GetIsAgent()), Metadata: metadataJSON,
		})
		if err != nil {
			s.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to insert tenant attribution")
		}
		if attribution.GetReferralCode() != "" {
			if refErr := queries.IncrementReferralCodeUsage(ctx, attribution.GetReferralCode()); refErr != nil {
				s.logger.WithError(refErr).WithField("referral_code", attribution.GetReferralCode()).Warn("Failed to increment referral code usage")
			}
		}
	}

	// 2. Find the default cluster for auto-subscription
	var defaultClusterID sql.NullString
	defaultCluster, defaultClusterErr := queries.GetDefaultActiveClusterID(ctx)
	err = defaultClusterErr
	if err == nil {
		defaultClusterID = validString(defaultCluster)
	}

	if errors.Is(err, sql.ErrNoRows) {
		s.logger.WithField("tenant_id", tenantID).Warn("No default cluster found for auto-subscription. Tenant created without default cluster access.")
		// This is not a fatal error for tenant creation, just a warning. Continue without subscription.
	} else if err != nil {
		s.logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to query default cluster during tenant creation")
		return nil, status.Errorf(codes.Internal, "failed to find default cluster for auto-subscription: %v", err)
	} else if defaultClusterID.Valid {
		// 3. Auto-subscribe the new tenant to the default cluster
		err = queries.GrantDefaultClusterAccess(ctx, quartermasterdb.GrantDefaultClusterAccessParams{
			TenantID: tenantID, ClusterID: defaultClusterID.String, CreatedAt: now,
		})
		if err != nil {
			s.logger.WithError(err).WithFields(logging.Fields{
				"tenant_id":  tenantID,
				"cluster_id": defaultClusterID.String,
			}).Error("Failed to auto-subscribe tenant to default cluster")
			return nil, status.Errorf(codes.Internal, "failed to auto-subscribe tenant to default cluster: %v", err)
		}

		// 4. Set official_cluster_id to the default cluster (billing-tier coverage)
		if clusterErr := queries.SetTenantOfficialCluster(ctx, quartermasterdb.SetTenantOfficialClusterParams{
			ClusterID: validString(defaultClusterID.String), TenantID: tenantID,
		}); clusterErr != nil {
			s.logger.WithError(clusterErr).WithFields(logging.Fields{
				"tenant_id":  tenantID,
				"cluster_id": defaultClusterID.String,
			}).Error("Failed to set official_cluster_id for new tenant")
			return nil, status.Errorf(codes.Internal, "failed to set official_cluster_id: %v", clusterErr)
		}
	}

	if domain := strings.TrimSpace(req.GetCustomDomain()); domain != "" {
		if enqErr := s.enqueueCustomDomainTransition(ctx, tx, tenantID, "", domain); enqErr != nil {
			return nil, status.Errorf(codes.Internal, "enqueue navigator custom-domain transition: %v", enqErr)
		}
	}

	// Durable subdomain-alias ensure for paid+active tenants (no-op for free).
	// The subdomain was generated/validated above; generate-if-missing covers
	// the case a paid tenant was created without one.
	if enqErr := s.enqueueTenantAliasEnsureTx(ctx, tx, tenantID, true); enqErr != nil {
		return nil, status.Errorf(codes.Internal, "enqueue tenant-alias ensure: %v", enqErr)
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		s.logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to commit transaction for tenant creation and auto-subscription")
		return nil, status.Errorf(codes.Internal, "failed to commit tenant creation: %v", err)
	}

	changedFields := []string{"name"}
	if subdomain != "" {
		changedFields = append(changedFields, "subdomain")
	}
	if req.GetCustomDomain() != "" {
		changedFields = append(changedFields, "custom_domain")
	}
	if req.GetLogoUrl() != "" {
		changedFields = append(changedFields, "logo_url")
	}
	if req.GetPrimaryColor() != "" {
		changedFields = append(changedFields, "primary_color")
	}
	if req.GetSecondaryColor() != "" {
		changedFields = append(changedFields, "secondary_color")
	}
	if req.GetDeploymentTier() != "" {
		changedFields = append(changedFields, "deployment_tier")
	}
	if req.GetDeploymentModel() != "" {
		changedFields = append(changedFields, "deployment_model")
	}

	s.emitTenantEvent(ctx, eventTenantCreated, tenantID, userID, changedFields, req.GetAttribution())
	if defaultClusterID.Valid {
		s.emitClusterEvent(ctx, eventTenantClusterAssigned, tenantID, userID, defaultClusterID.String, "cluster", defaultClusterID.String, "", "", "")
	}

	tenant := &quartermasterpb.Tenant{
		Id:                    tenantID,
		Name:                  name,
		Subdomain:             &subdomain,
		CustomDomain:          req.CustomDomain,
		LogoUrl:               req.LogoUrl,
		PrimaryColor:          req.GetPrimaryColor(),
		SecondaryColor:        req.GetSecondaryColor(),
		DeploymentTier:        req.GetDeploymentTier(),
		DeploymentModel:       req.GetDeploymentModel(),
		PrimaryDeploymentTier: req.GetPrimaryDeploymentTier(),
		IsActive:              true,
		CreatedAt:             timestamppb.New(now),
		UpdatedAt:             timestamppb.New(now),
	}

	return &quartermasterpb.CreateTenantResponse{Tenant: tenant}, nil
}

// UpdateTenant updates a tenant's properties
func (s *QuartermasterServer) UpdateTenant(ctx context.Context, req *quartermasterpb.UpdateTenantRequest) (*quartermasterpb.Tenant, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	userID := middleware.GetUserID(ctx)
	var tenantUpdate quartermasterdb.TenantUpdate
	changedFields := []string{}
	var previousClusterID sql.NullString
	var previousCustomDomain sql.NullString
	var previousSubdomain sql.NullString

	if req.Name != nil {
		tenantUpdate.Name = req.Name
		changedFields = append(changedFields, "name")
	}
	if req.Subdomain != nil {
		subdomain := strings.ToLower(strings.TrimSpace(*req.Subdomain))
		if subdomain != "" && dns.IsReservedTenantSlug(subdomain, s.activeClusterSlugs(ctx)) {
			return nil, status.Errorf(codes.InvalidArgument, "subdomain %q is reserved or invalid", subdomain)
		}
		tenantUpdate.SubdomainSet = true
		if subdomain == "" {
			tenantUpdate.Subdomain = nil
		} else {
			tenantUpdate.Subdomain = &subdomain
		}
		changedFields = append(changedFields, "subdomain")
	}
	if req.CustomDomain != nil {
		tenantUpdate.CustomDomain = req.CustomDomain
		changedFields = append(changedFields, "custom_domain")
	}
	if req.LogoUrl != nil {
		tenantUpdate.LogoURL = req.LogoUrl
		changedFields = append(changedFields, "logo_url")
	}
	if req.PrimaryColor != nil {
		tenantUpdate.PrimaryColor = req.PrimaryColor
		changedFields = append(changedFields, "primary_color")
	}
	if req.SecondaryColor != nil {
		tenantUpdate.SecondaryColor = req.SecondaryColor
		changedFields = append(changedFields, "secondary_color")
	}
	if req.DeploymentTier != nil {
		deploymentTier := strings.TrimSpace(*req.DeploymentTier)
		tenantUpdate.DeploymentTier = &deploymentTier
		changedFields = append(changedFields, "deployment_tier")
	}
	if req.DeploymentModel != nil {
		deploymentModel := strings.TrimSpace(*req.DeploymentModel)
		tenantUpdate.DeploymentModel = &deploymentModel
		changedFields = append(changedFields, "deployment_model")
	}
	if req.PrimaryClusterId != nil {
		tenantUpdate.PrimaryClusterID = req.PrimaryClusterId
		changedFields = append(changedFields, "primary_cluster_id")
	}
	if req.IsActive != nil {
		tenantUpdate.IsActive = req.IsActive
		changedFields = append(changedFields, "is_active")
	}
	if req.MonitoringEnabled != nil {
		tenantUpdate.MonitoringEnabled = req.MonitoringEnabled
		changedFields = append(changedFields, "monitoring_enabled")
	}

	if len(changedFields) == 0 {
		return nil, status.Error(codes.InvalidArgument, "no fields to update")
	}

	// Wrap the tenant UPDATE and its outbox emits in one transaction so a
	// crash between mutation and emit can't leave the row updated without
	// the corresponding service-event durable.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	queries := quartermasterdb.New(tx)

	// Read previous values FOR UPDATE inside the tx so concurrent updates to
	// the same tenant serialize. Reading the previous subdomain before the
	// transaction (and without a lock) would let a concurrent a->b and a->c
	// race both observe "a", enqueuing a retire only for "a" and orphaning
	// the intermediate label.
	if req.PrimaryClusterId != nil || req.CustomDomain != nil || req.Subdomain != nil {
		previous, scanErr := queries.LockTenantPreviousValues(ctx, tenantID)
		if scanErr == nil {
			previousClusterID, previousCustomDomain, previousSubdomain = previous.PrimaryClusterID, previous.CustomDomain, previous.Subdomain
		}
		if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
			return nil, status.Errorf(codes.Internal, "previous-value lookup: %v", scanErr)
		}
	}

	rows, err := queries.UpdateTenantFields(ctx, tenantID, tenantUpdate)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update tenant: %v", err)
	}

	if rows == 0 {
		return nil, status.Error(codes.NotFound, "tenant not found")
	}

	if len(changedFields) > 0 {
		if enqErr := s.emitTenantEventTx(ctx, tx, eventTenantUpdated, tenantID, userID, changedFields, nil); enqErr != nil {
			return nil, status.Errorf(codes.Internal, "enqueue tenant_updated: %v", enqErr)
		}
	}
	if req.PrimaryClusterId != nil {
		newCluster := strings.TrimSpace(*req.PrimaryClusterId)
		if newCluster != "" && (!previousClusterID.Valid || previousClusterID.String != newCluster) {
			if enqErr := s.emitClusterEventTx(ctx, tx, eventTenantClusterAssigned, tenantID, userID, newCluster, "cluster", newCluster, "", "", ""); enqErr != nil {
				return nil, status.Errorf(codes.Internal, "enqueue tenant_cluster_assigned: %v", enqErr)
			}
		} else if newCluster == "" && previousClusterID.Valid {
			if enqErr := s.emitClusterEventTx(ctx, tx, eventTenantClusterUnassigned, tenantID, userID, previousClusterID.String, "cluster", previousClusterID.String, "", "", ""); enqErr != nil {
				return nil, status.Errorf(codes.Internal, "enqueue tenant_cluster_unassigned: %v", enqErr)
			}
		}
	}
	// Custom-domain lifecycle hand-off is durable: enqueue the desired
	// Navigator action inside the same tx as the tenants UPDATE so a
	// Navigator outage cannot leave QM saying the tenant has a custom
	// domain while Navigator has no verification/cert lifecycle row. Free
	// tenants still skip; the drain worker will replay until Navigator
	// accepts. Tear-down of an old domain rides the outbox too so it never
	// stays mounted on Navigator after QM clears it.
	if req.CustomDomain != nil {
		if enqErr := s.enqueueCustomDomainTransition(ctx, tx, tenantID, previousCustomDomain.String, strings.TrimSpace(*req.CustomDomain)); enqErr != nil {
			return nil, status.Errorf(codes.Internal, "enqueue navigator custom-domain transition: %v", enqErr)
		}
	}

	// Enqueue the desired Navigator alias action(s) in the same tx so a
	// Navigator outage can't lose the intent. On a rename the old label is
	// retired so its Bunny records are cleared (Navigator overwrites the alias
	// label in place and keeps no memory of the old one).
	if req.Subdomain != nil {
		if enqErr := s.enqueueTenantAliasForSubdomainUpdate(ctx, tx, tenantID, previousSubdomain.String, *req.Subdomain); enqErr != nil {
			return nil, status.Errorf(codes.Internal, "enqueue tenant-alias subdomain change: %v", enqErr)
		}
	} else if req.DeploymentTier != nil || req.IsActive != nil {
		downgrade := (req.DeploymentTier != nil && !models.DeploymentTierAliasEligible(*req.DeploymentTier)) ||
			(req.IsActive != nil && !*req.IsActive)
		if enqErr := s.enqueueTenantAliasForTierChange(ctx, tx, tenantID, downgrade); enqErr != nil {
			return nil, status.Errorf(codes.Internal, "enqueue tenant-alias tier change: %v", enqErr)
		}
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return nil, status.Errorf(codes.Internal, "commit tenant update: %v", commitErr)
	}

	// Fetch updated tenant
	resp, err := s.GetTenant(ctx, &quartermasterpb.GetTenantRequest{TenantId: tenantID})
	if err != nil {
		return nil, err
	}
	return resp.Tenant, nil
}

// enqueueCustomDomainTransition writes the desired Navigator action(s) into
// quartermaster.navigator_custom_domain_outbox inside the caller's
// transaction. Tear-down of the previous domain runs whenever the value
// changes, independent of plan tier (so an expired-to-free tenant still
// gets the old Navigator state unwound). Ensure runs only when the new
// domain is non-empty AND the tenant is active on an alias-eligible monthly
// tier (models.DeploymentTierAliasEligible).
func (s *QuartermasterServer) enqueueCustomDomainTransition(ctx context.Context, tx *sql.Tx, tenantID, previousDomain, newDomain string) error {
	previousDomain = strings.TrimSpace(previousDomain)
	newDomain = strings.TrimSpace(newDomain)
	if previousDomain != "" && previousDomain != newDomain {
		if _, err := s.EnqueueNavigatorCustomDomainTx(ctx, tx, tenantID, previousDomain, "remove"); err != nil {
			return fmt.Errorf("enqueue remove: %w", err)
		}
	}
	if newDomain == "" {
		return nil
	}
	row, err := quartermasterdb.New(tx).GetTenantCustomDomainEligibility(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("lookup tier: %w", err)
	}
	if !row.DeploymentTier.Valid || !row.IsActive.Valid {
		return fmt.Errorf("lookup tier: tenant eligibility fields are NULL")
	}
	if !row.IsActive.Bool || !models.DeploymentTierAliasEligible(row.DeploymentTier.String) {
		return nil
	}
	if _, err := s.EnqueueNavigatorCustomDomainTx(ctx, tx, tenantID, newDomain, "ensure"); err != nil {
		return fmt.Errorf("enqueue ensure: %w", err)
	}
	return nil
}

// enqueueTenantAliasEnsureTx enqueues a durable Navigator ensure for the
// tenant's subdomain alias inside the caller's tx — but only when the tenant
// warrants one: active, on an alias-eligible monthly tier, and holding at
// least one active cluster subscription. This is the same condition the
// backstop reconciler uses, so
// ensure never creates an alias the backstop would then reap. The resolved
// decision rides the row, so the drain worker never re-derives it. When
// eligible but missing a subdomain and generateIfMissing is set, a DNS-safe
// label is generated and persisted first. The tenant row is locked FOR UPDATE
// so concurrent generators serialize.
func (s *QuartermasterServer) enqueueTenantAliasEnsureTx(ctx context.Context, tx *sql.Tx, tenantID string, generateIfMissing bool) error {
	queries := quartermasterdb.New(tx)
	row, err := queries.LockTenantAliasEligibility(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("lookup tenant for alias ensure: %w", err)
	}
	if !row.DeploymentTier.Valid || !row.IsActive.Valid {
		return fmt.Errorf("lookup tenant for alias ensure: eligibility fields are NULL")
	}
	if !row.IsActive.Bool || !models.DeploymentTierAliasEligible(row.DeploymentTier.String) || !row.HasCluster {
		return nil // not eligible for an alias (matches the backstop's "want")
	}
	label := strings.TrimSpace(row.Subdomain.String)
	if label == "" {
		if !generateIfMissing {
			return nil
		}
		generated, genErr := s.generateAvailableTenantSubdomain(ctx, row.Name)
		if genErr != nil {
			return fmt.Errorf("generate subdomain: %w", genErr)
		}
		if err := queries.SetGeneratedTenantSubdomain(ctx, quartermasterdb.SetGeneratedTenantSubdomainParams{
			Subdomain: validString(generated), TenantID: tenantID,
		}); err != nil {
			return fmt.Errorf("persist generated subdomain: %w", err)
		}
		label = generated
	}
	if _, err := s.EnqueueNavigatorTenantAliasTx(ctx, tx, tenantID, label, "ensure", "", ""); err != nil {
		return fmt.Errorf("enqueue ensure: %w", err)
	}
	return nil
}

// enqueueTenantAliasRemoveTx enqueues a durable full alias teardown (current
// label + intent row). auditSubdomain is recorded for traceability only;
// Navigator tears down whatever active label it holds.
func (s *QuartermasterServer) enqueueTenantAliasRemoveTx(ctx context.Context, tx *sql.Tx, tenantID, auditSubdomain string) error {
	if _, err := s.EnqueueNavigatorTenantAliasTx(ctx, tx, tenantID, auditSubdomain, "remove", "", ""); err != nil {
		return fmt.Errorf("enqueue remove: %w", err)
	}
	return nil
}

// enqueueTenantAliasRetireTx enqueues a durable retirement of one old label
// (the rename source). Navigator clears that label's records without touching
// the active alias.
func (s *QuartermasterServer) enqueueTenantAliasRetireTx(ctx context.Context, tx *sql.Tx, tenantID, label string) error {
	label = strings.TrimSpace(label)
	if label == "" {
		return nil
	}
	if _, err := s.EnqueueNavigatorTenantAliasTx(ctx, tx, tenantID, label, "retire", "", ""); err != nil {
		return fmt.Errorf("enqueue retire: %w", err)
	}
	return nil
}

// tenantHasPaidClusterAccessTx reports whether the tenant still warrants an
// alias: it is active, on an alias-eligible monthly tier, and holds at least
// one active cluster subscription. A downgrade/deactivation only tears the
// alias down when this is false — a tenant can keep the alias via another
// paid cluster. The tenant's own is_active is part of the gate so
// deactivating a tenant tears the alias down even while paid cluster-access
// rows linger.
func (s *QuartermasterServer) tenantHasPaidClusterAccessTx(ctx context.Context, tx *sql.Tx, tenantID string) (bool, error) {
	return quartermasterdb.New(tx).TenantHasPaidClusterAccess(ctx, tenantID)
}

// enqueueTenantAliasForSubdomainChange enqueues the durable Navigator alias
// actions for a subdomain field change. Clearing the label tears the alias
// down; a rename retires the old label (so its records are cleared) and
// ensures the new one. retire is enqueued before ensure so it gets the lower
// BIGSERIAL seq and the worker dispatches it first.
func (s *QuartermasterServer) enqueueTenantAliasForSubdomainChange(ctx context.Context, tx *sql.Tx, tenantID, prevSubdomain, newSubdomain string) error {
	prevSubdomain = strings.TrimSpace(prevSubdomain)
	newSubdomain = strings.ToLower(strings.TrimSpace(newSubdomain))
	if newSubdomain == "" {
		// Subdomain cleared → tear the alias down entirely.
		return s.enqueueTenantAliasRemoveTx(ctx, tx, tenantID, prevSubdomain)
	}
	if prevSubdomain != "" && prevSubdomain != newSubdomain {
		if err := s.enqueueTenantAliasRetireTx(ctx, tx, tenantID, prevSubdomain); err != nil {
			return err
		}
	}
	return s.enqueueTenantAliasEnsureTx(ctx, tx, tenantID, false)
}

// enqueueTenantAliasForSubdomainUpdate handles a subdomain-field update after
// the tenant row mutation has landed in the caller's tx. If that same update
// made the tenant ineligible for aliases, enqueue a full remove instead of a
// rename; otherwise Navigator would keep its active alias row.
func (s *QuartermasterServer) enqueueTenantAliasForSubdomainUpdate(ctx context.Context, tx *sql.Tx, tenantID, prevSubdomain, newSubdomain string) error {
	hasPaid, err := s.tenantHasPaidClusterAccessTx(ctx, tx, tenantID)
	if err != nil {
		return fmt.Errorf("check paid cluster access: %w", err)
	}
	if !hasPaid {
		return s.enqueueTenantAliasRemoveTx(ctx, tx, tenantID, prevSubdomain)
	}
	return s.enqueueTenantAliasForSubdomainChange(ctx, tx, tenantID, prevSubdomain, newSubdomain)
}

// enqueueTenantAliasForTierChange enqueues alias intent for a tier/active
// change with no subdomain change. A downgrade tears the alias down only when
// no paid cluster access remains; otherwise it (re-)ensures the current label.
func (s *QuartermasterServer) enqueueTenantAliasForTierChange(ctx context.Context, tx *sql.Tx, tenantID string, downgrade bool) error {
	if downgrade {
		hasPaid, err := s.tenantHasPaidClusterAccessTx(ctx, tx, tenantID)
		if err != nil {
			return fmt.Errorf("check paid cluster access: %w", err)
		}
		if !hasPaid {
			return s.enqueueTenantAliasRemoveTx(ctx, tx, tenantID, "")
		}
		return nil
	}
	return s.enqueueTenantAliasEnsureTx(ctx, tx, tenantID, true)
}

// DeleteTenant soft deletes a tenant
func (s *QuartermasterServer) DeleteTenant(ctx context.Context, req *quartermasterpb.DeleteTenantRequest) (*emptypb.Empty, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	userID := middleware.GetUserID(ctx)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	queries := quartermasterdb.New(tx)

	var previousCustomDomain sql.NullString
	var previousSubdomain sql.NullString
	// FOR UPDATE locks the tenant row so a concurrent UpdateTenant cannot
	// commit a new custom_domain/ensure between this read and the teardown
	// enqueue below — otherwise delete would tear down the stale domain only.
	previous, scanErr := queries.LockActiveTenantDomains(ctx, tenantID)
	if scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tenant not found")
		}
		return nil, status.Errorf(codes.Internal, "lookup tenant domains: %v", scanErr)
	}
	previousCustomDomain, previousSubdomain = previous.CustomDomain, previous.Subdomain

	rows, err := queries.DeactivateTenant(ctx, tenantID)
	if err != nil {
		s.logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to delete tenant")
		return nil, status.Errorf(codes.Internal, "failed to delete tenant: %v", err)
	}

	if rows == 0 {
		return nil, status.Error(codes.NotFound, "tenant not found")
	}

	if previousCustomDomain.Valid && strings.TrimSpace(previousCustomDomain.String) != "" {
		if _, enqErr := s.EnqueueNavigatorCustomDomainTx(ctx, tx, tenantID, previousCustomDomain.String, "remove"); enqErr != nil {
			return nil, status.Errorf(codes.Internal, "enqueue navigator custom-domain removal: %v", enqErr)
		}
	}
	// Tear the platform alias down too — the tenant is gone.
	if enqErr := s.enqueueTenantAliasRemoveTx(ctx, tx, tenantID, strings.TrimSpace(previousSubdomain.String)); enqErr != nil {
		return nil, status.Errorf(codes.Internal, "enqueue navigator tenant-alias removal: %v", enqErr)
	}
	if enqErr := s.emitTenantEventTx(ctx, tx, eventTenantDeleted, tenantID, userID, nil, nil); enqErr != nil {
		return nil, status.Errorf(codes.Internal, "enqueue tenant_deleted: %v", enqErr)
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return nil, status.Errorf(codes.Internal, "commit tenant delete: %v", commitErr)
	}

	s.logger.WithField("tenant_id", tenantID).Info("Deleted tenant successfully")
	return &emptypb.Empty{}, nil
}

// GetTenantCluster returns cluster/deployment info for a tenant
func (s *QuartermasterServer) GetTenantCluster(ctx context.Context, req *quartermasterpb.GetTenantClusterRequest) (*quartermasterpb.GetTenantResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	row, err := quartermasterdb.New(s.db).GetActiveTenantClusterRecord(ctx, tenantID)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "tenant not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !row.PrimaryColor.Valid || !row.SecondaryColor.Valid || !row.DeploymentTier.Valid ||
		!row.DeploymentModel.Valid || !row.IsActive.Valid || !row.CreatedAt.Valid || !row.UpdatedAt.Valid {
		return nil, status.Error(codes.Internal, "database error: tenant has NULL required fields")
	}
	tenant := quartermasterpb.Tenant{
		Id: row.ID, Name: row.Name, PrimaryColor: row.PrimaryColor.String,
		SecondaryColor: row.SecondaryColor.String, DeploymentTier: row.DeploymentTier.String,
		DeploymentModel: row.DeploymentModel.String, KafkaBrokers: row.KafkaBrokers,
		IsActive: row.IsActive.Bool, MonitoringEnabled: row.MonitoringEnabled,
		CreatedAt: timestamppb.New(row.CreatedAt.Time), UpdatedAt: timestamppb.New(row.UpdatedAt.Time),
	}

	if row.Subdomain.Valid {
		tenant.Subdomain = &row.Subdomain.String
	}
	if row.CustomDomain.Valid {
		tenant.CustomDomain = &row.CustomDomain.String
	}
	if row.LogoUrl.Valid {
		tenant.LogoUrl = &row.LogoUrl.String
	}
	if row.PrimaryClusterID.Valid {
		tenant.PrimaryClusterId = &row.PrimaryClusterID.String
	}
	if row.OfficialClusterID.Valid {
		tenant.OfficialClusterId = &row.OfficialClusterID.String
	}
	if row.KafkaTopicPrefix.Valid {
		tenant.KafkaTopicPrefix = &row.KafkaTopicPrefix.String
	}
	if row.DatabaseUrl.Valid {
		tenant.DatabaseUrl = &row.DatabaseUrl.String
	}

	return &quartermasterpb.GetTenantResponse{Tenant: &tenant}, nil
}

// UpdateTenantCluster updates the cluster routing info for a tenant
func (s *QuartermasterServer) UpdateTenantCluster(ctx context.Context, req *quartermasterpb.UpdateTenantClusterRequest) (*emptypb.Empty, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	userID := middleware.GetUserID(ctx)
	changedFields := []string{}
	queries := quartermasterdb.New(s.db)
	var previousClusterID sql.NullString
	if req.PrimaryClusterId != nil {
		var previousErr error
		previousClusterID, previousErr = queries.GetTenantPrimaryClusterID(ctx, tenantID)
		if previousErr != nil && !errors.Is(previousErr, sql.ErrNoRows) {
			return nil, status.Errorf(codes.Internal, "failed to read current tenant cluster: %v", previousErr)
		}
	}

	if req.PrimaryClusterId != nil {
		newClusterID := strings.TrimSpace(*req.PrimaryClusterId)
		if newClusterID != "" {
			exists, err := queries.TenantHasActiveClusterAccess(ctx, quartermasterdb.TenantHasActiveClusterAccessParams{
				TenantID: tenantID, ClusterID: newClusterID,
			})
			if err != nil {
				return nil, status.Errorf(codes.Internal, "failed to verify cluster subscription: %v", err)
			}
			if !exists {
				return nil, status.Error(codes.FailedPrecondition, "cluster is not an active subscription for this tenant")
			}

			clusterType, err := queries.GetInfrastructureClusterType(ctx, newClusterID)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "failed to look up cluster type: %v", err)
			}
			if !models.ClusterTypeCanBePreferred(clusterType) {
				return nil, status.Error(codes.FailedPrecondition, "only edge clusters can be set as preferred")
			}
		}
	}

	if req.PrimaryClusterId != nil {
		changedFields = append(changedFields, "primary_cluster_id")
	}
	if req.DeploymentModel != nil {
		changedFields = append(changedFields, "deployment_model")
	}

	if len(changedFields) == 0 {
		return nil, status.Error(codes.InvalidArgument, "no fields to update")
	}

	// Mutation + outbox emits ride in one tx so a crash between them can't
	// leave the cluster assignment durable without the corresponding event.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	txQueries := quartermasterdb.New(tx)
	var rows int64
	switch {
	case req.PrimaryClusterId != nil && req.DeploymentModel != nil:
		rows, err = txQueries.UpdateTenantClusterAndDeploymentModel(ctx, quartermasterdb.UpdateTenantClusterAndDeploymentModelParams{
			PrimaryClusterID: validString(*req.PrimaryClusterId), DeploymentModel: validString(*req.DeploymentModel), TenantID: tenantID,
		})
	case req.PrimaryClusterId != nil:
		rows, err = txQueries.UpdateTenantPrimaryCluster(ctx, quartermasterdb.UpdateTenantPrimaryClusterParams{
			PrimaryClusterID: validString(*req.PrimaryClusterId), TenantID: tenantID,
		})
	default:
		rows, err = txQueries.UpdateTenantDeploymentModel(ctx, quartermasterdb.UpdateTenantDeploymentModelParams{
			DeploymentModel: validString(*req.DeploymentModel), TenantID: tenantID,
		})
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update tenant cluster: %v", err)
	}

	if rows == 0 {
		return nil, status.Error(codes.NotFound, "tenant not found")
	}

	if len(changedFields) > 0 {
		if enqErr := s.emitTenantEventTx(ctx, tx, eventTenantUpdated, tenantID, userID, changedFields, nil); enqErr != nil {
			return nil, status.Errorf(codes.Internal, "enqueue tenant_updated: %v", enqErr)
		}
	}
	if req.PrimaryClusterId != nil {
		newCluster := strings.TrimSpace(*req.PrimaryClusterId)
		if newCluster != "" && (!previousClusterID.Valid || previousClusterID.String != newCluster) {
			if enqErr := s.emitClusterEventTx(ctx, tx, eventTenantClusterAssigned, tenantID, userID, newCluster, "cluster", newCluster, "", "", ""); enqErr != nil {
				return nil, status.Errorf(codes.Internal, "enqueue tenant_cluster_assigned: %v", enqErr)
			}
		} else if newCluster == "" && previousClusterID.Valid {
			if enqErr := s.emitClusterEventTx(ctx, tx, eventTenantClusterUnassigned, tenantID, userID, previousClusterID.String, "cluster", previousClusterID.String, "", "", ""); enqErr != nil {
				return nil, status.Errorf(codes.Internal, "enqueue tenant_cluster_unassigned: %v", enqErr)
			}
		}
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, status.Errorf(codes.Internal, "commit tenant cluster update: %v", commitErr)
	}

	s.logger.WithField("tenant_id", tenantID).Info("Updated tenant cluster successfully")
	return &emptypb.Empty{}, nil
}

// GetTenantsBatch retrieves multiple tenants by IDs
func (s *QuartermasterServer) GetTenantsBatch(ctx context.Context, req *quartermasterpb.GetTenantsBatchRequest) (*quartermasterpb.ListTenantsResponse, error) {
	tenantIDs := req.GetTenantIds()
	if len(tenantIDs) == 0 {
		return nil, status.Error(codes.InvalidArgument, "tenant_ids required")
	}

	rows, err := quartermasterdb.New(s.db).ListActiveTenantsByIDs(ctx, tenantIDs)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	var tenants []*quartermasterpb.Tenant
	for _, row := range rows {
		if !row.PrimaryColor.Valid || !row.SecondaryColor.Valid || !row.DeploymentTier.Valid || !row.DeploymentModel.Valid ||
			!row.IsActive.Valid || !row.CreatedAt.Valid || !row.UpdatedAt.Valid {
			return nil, status.Error(codes.Internal, "scan error: tenant has NULL required fields")
		}
		tenant := quartermasterpb.Tenant{
			Id: row.ID, Name: row.Name, PrimaryColor: row.PrimaryColor.String, SecondaryColor: row.SecondaryColor.String,
			DeploymentTier: row.DeploymentTier.String, DeploymentModel: row.DeploymentModel.String,
			KafkaBrokers: row.KafkaBrokers, IsActive: row.IsActive.Bool, MonitoringEnabled: row.MonitoringEnabled,
			CreatedAt: timestamppb.New(row.CreatedAt.Time), UpdatedAt: timestamppb.New(row.UpdatedAt.Time),
		}
		if row.Subdomain.Valid {
			tenant.Subdomain = &row.Subdomain.String
		}
		if row.CustomDomain.Valid {
			tenant.CustomDomain = &row.CustomDomain.String
		}
		if row.LogoUrl.Valid {
			tenant.LogoUrl = &row.LogoUrl.String
		}
		if row.PrimaryClusterID.Valid {
			tenant.PrimaryClusterId = &row.PrimaryClusterID.String
		}
		if row.OfficialClusterID.Valid {
			tenant.OfficialClusterId = &row.OfficialClusterID.String
		}
		if row.KafkaTopicPrefix.Valid {
			tenant.KafkaTopicPrefix = &row.KafkaTopicPrefix.String
		}
		if row.DatabaseUrl.Valid {
			tenant.DatabaseUrl = &row.DatabaseUrl.String
		}
		tenants = append(tenants, &tenant)
	}

	return &quartermasterpb.ListTenantsResponse{Tenants: tenants}, nil
}

// GetTenantsByCluster retrieves all tenants assigned to a specific cluster
func (s *QuartermasterServer) GetTenantsByCluster(ctx context.Context, req *quartermasterpb.GetTenantsByClusterRequest) (*quartermasterpb.GetTenantsByClusterResponse, error) {
	clusterID := req.GetClusterId()
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}

	// Limit-only pagination: cursor params are rejected loudly rather than
	// silently returning the first page forever, and the limit clamps to the
	// same ceiling the keyset-paginated list endpoints use.
	limit := int32(100)
	if p := req.GetPagination(); p != nil {
		if p.GetAfter() != "" || p.GetBefore() != "" || p.GetLast() > 0 {
			return nil, status.Error(codes.InvalidArgument, "GetTenantsByCluster supports first-page limits only (after/before/last are not implemented)")
		}
		if p.GetFirst() > 0 {
			limit = min(p.GetFirst(), int32(pagination.MaxLimit))
		}
	}
	// DISTINCT runs in the inner query so a tenant with multiple assignment
	// rows neither duplicates nor inflates the windowed total_count.
	rows, err := quartermasterdb.New(s.db).ListActiveTenantsByCluster(ctx, quartermasterdb.ListActiveTenantsByClusterParams{
		ClusterID: validString(clusterID), ResultLimit: limit,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	var tenants []*quartermasterpb.Tenant
	var totalCount int32
	for _, row := range rows {
		if !row.PrimaryColor.Valid || !row.SecondaryColor.Valid || !row.DeploymentTier.Valid || !row.DeploymentModel.Valid ||
			!row.IsActive.Valid || !row.CreatedAt.Valid || !row.UpdatedAt.Valid {
			return nil, status.Error(codes.Internal, "scan error: tenant has NULL required fields")
		}
		totalCount = int32(row.TotalCount)
		tenant := quartermasterpb.Tenant{
			Id: row.ID, Name: row.Name, PrimaryColor: row.PrimaryColor.String, SecondaryColor: row.SecondaryColor.String,
			DeploymentTier: row.DeploymentTier.String, DeploymentModel: row.DeploymentModel.String,
			KafkaBrokers: row.KafkaBrokers, IsActive: row.IsActive.Bool, MonitoringEnabled: row.MonitoringEnabled,
			CreatedAt: timestamppb.New(row.CreatedAt.Time), UpdatedAt: timestamppb.New(row.UpdatedAt.Time),
		}
		if row.OfficialClusterID.Valid {
			tenant.OfficialClusterId = &row.OfficialClusterID.String
		}
		if row.Subdomain.Valid {
			tenant.Subdomain = &row.Subdomain.String
		}
		if row.CustomDomain.Valid {
			tenant.CustomDomain = &row.CustomDomain.String
		}
		if row.LogoUrl.Valid {
			tenant.LogoUrl = &row.LogoUrl.String
		}
		if row.PrimaryClusterID.Valid {
			tenant.PrimaryClusterId = &row.PrimaryClusterID.String
		}
		if row.KafkaTopicPrefix.Valid {
			tenant.KafkaTopicPrefix = &row.KafkaTopicPrefix.String
		}
		if row.DatabaseUrl.Valid {
			tenant.DatabaseUrl = &row.DatabaseUrl.String
		}
		tenants = append(tenants, &tenant)
	}

	return &quartermasterpb.GetTenantsByClusterResponse{
		ClusterId: clusterID,
		Tenants:   tenants,
		Pagination: &commonpb.CursorPaginationResponse{
			TotalCount:  totalCount,
			HasNextPage: totalCount > int32(len(tenants)),
		},
	}, nil
}

// activeClusterSlugs returns the slug values of all active clusters so
// they can be excluded from the tenant subdomain namespace. Errors are
// swallowed: on failure we return an empty list rather than blocking
// tenant signups. Static reserved labels still apply via
// dns.IsReservedTenantSlug.
func (s *QuartermasterServer) activeClusterSlugs(ctx context.Context) []string {
	rows, err := quartermasterdb.New(s.db).ListActiveClusterSlugs(ctx)
	if err != nil {
		return nil
	}
	var out []string
	for _, id := range rows {
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}

const tenantSubdomainLabelMaxLen = 63

func (s *QuartermasterServer) generateAvailableTenantSubdomain(ctx context.Context, name string) (string, error) {
	extraReserved := s.activeClusterSlugs(ctx)
	base := dns.SanitizeLabel(name)
	if base == "default" || dns.IsReservedTenantSlug(base, extraReserved) {
		base = "tenant"
	}

	for i := 0; i < 100; i++ {
		suffix := "-" + uuid.NewString()[:8]
		candidate := tenantSubdomainCandidate(base, suffix)
		if dns.IsReservedTenantSlug(candidate, extraReserved) {
			continue
		}
		available, err := s.tenantSubdomainAvailable(ctx, candidate)
		if err != nil {
			return "", err
		}
		if available {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no available tenant subdomain for %q", name)
}

func tenantSubdomainCandidate(base, suffix string) string {
	maxBaseLen := tenantSubdomainLabelMaxLen - len(suffix)
	if maxBaseLen < 1 {
		maxBaseLen = 1
	}
	if len(base) > maxBaseLen {
		base = base[:maxBaseLen]
	}
	base = strings.Trim(base, "-")
	if base == "" {
		base = "tenant"
	}
	return base + suffix
}

func (s *QuartermasterServer) tenantSubdomainAvailable(ctx context.Context, candidate string) (bool, error) {
	exists, err := quartermasterdb.New(s.db).TenantSubdomainExists(ctx, validString(candidate))
	if err != nil {
		return false, err
	}
	return !exists, nil
}

// ListAliasedTenantsForCluster returns tenants on an alias-eligible monthly
// tier with active access to the cluster. Used by Foghorn cert refresh to
// know which per-tenant TLS bundles to include in ConfigSeed for edges in
// this cluster. Filters:
//   - tenant_cluster_access.is_active = TRUE
//   - tenants.deployment_tier alias-eligible (sqlAliasTierEligible)
//   - tenants.is_active = TRUE
//   - tenants.subdomain IS NOT NULL and is not the empty string
//
// Cert readiness happens at the caller via Navigator; this method
// returns candidates without crossing service boundaries.
func (s *QuartermasterServer) ListAliasedTenantsForCluster(ctx context.Context, req *quartermasterpb.ListAliasedTenantsForClusterRequest) (*quartermasterpb.ListAliasedTenantsForClusterResponse, error) {
	clusterID := req.GetClusterId()
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}

	rows, err := quartermasterdb.New(s.db).ListAliasedTenantsForCluster(ctx, clusterID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	var out []*quartermasterpb.AliasedTenantRef
	for _, row := range rows {
		if !row.Subdomain.Valid || row.Subdomain.String == "" {
			continue
		}
		out = append(out, &quartermasterpb.AliasedTenantRef{
			TenantId:  row.TenantID,
			Subdomain: row.Subdomain.String,
		})
	}
	return &quartermasterpb.ListAliasedTenantsForClusterResponse{
		ClusterId: clusterID,
		Tenants:   out,
	}, nil
}

// ============================================================================
// CLUSTER SERVICE
// ============================================================================

// GetCluster returns a specific cluster
func (s *QuartermasterServer) GetCluster(ctx context.Context, req *quartermasterpb.GetClusterRequest) (*quartermasterpb.ClusterResponse, error) {
	clusterID := req.GetClusterId()
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}

	cluster, err := s.queryCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}

	return &quartermasterpb.ClusterResponse{Cluster: cluster}, nil
}

// ListClusters returns all clusters with pagination
func (s *QuartermasterServer) ListClusters(ctx context.Context, req *quartermasterpb.ListClustersRequest) (*quartermasterpb.ListClustersResponse, error) {
	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	tenantID := middleware.GetTenantID(ctx)
	ownerTenantID := strings.TrimSpace(req.GetOwnerTenantId())
	publicPlatformOfficialScope := ownerTenantID == "" && req.IsPlatformOfficial != nil && req.GetIsPlatformOfficial()
	publicTopologyScope := ownerTenantID == "" && req.PublicTopology != nil && req.GetPublicTopology()
	scope, scopeID := quartermasterdb.ClusterScopeDefault, ""
	switch {
	case ownerTenantID != "":
		scope, scopeID = quartermasterdb.ClusterScopeOwner, ownerTenantID
	case publicPlatformOfficialScope:
		scope = quartermasterdb.ClusterScopePlatform
	case publicTopologyScope:
		scope = quartermasterdb.ClusterScopePublicTopology
	case tenantID != "":
		scope, scopeID = quartermasterdb.ClusterScopeTenant, tenantID
	case ctxkeys.GetAuthType(ctx) == "service":
		scope = quartermasterdb.ClusterScopeService
	}
	filter := quartermasterdb.ClusterListFilter{Scope: scope, ScopeID: scopeID, ClusterID: req.GetClusterId(), ClusterName: req.GetClusterName(),
		ClusterType: req.GetClusterType(), DeploymentModel: req.GetDeploymentModel(), Backward: params.Direction == pagination.Backward, Limit: params.Limit + 1}
	if req.IsPlatformOfficial != nil && !publicPlatformOfficialScope {
		filter.IsPlatformOfficial = req.IsPlatformOfficial
	}
	if req.PublicTopology != nil && !publicTopologyScope {
		filter.PublicTopology = req.PublicTopology
	}
	if params.Cursor != nil {
		filter.CursorTime, filter.CursorID = &params.Cursor.Timestamp, params.Cursor.ID
	}

	var rows []quartermasterdb.ClusterListRow
	var total int32
	if countErr := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		rows, total, queryErr = quartermasterdb.New(s.db).ListClustersPage(ctx, filter)
		return queryErr
	}); countErr != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", countErr)
	}

	var clusters []*quartermasterpb.InfrastructureCluster
	for _, row := range rows {
		clusters = append(clusters, clusterFromListRow(row))
	}

	// Detect hasMore and trim results
	hasMore := len(clusters) > params.Limit
	if hasMore {
		clusters = clusters[:params.Limit]
	}

	// Reverse results if backward pagination
	if params.Direction == pagination.Backward && len(clusters) > 0 {
		for i, j := 0, len(clusters)-1; i < j; i, j = i+1, j-1 {
			clusters[i], clusters[j] = clusters[j], clusters[i]
		}
	}

	// Build cursors from results
	var startCursor, endCursor string
	if len(clusters) > 0 {
		first := clusters[0]
		last := clusters[len(clusters)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.Id)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.Id)
	}

	// Build response with proper hasNextPage/hasPreviousPage
	resp := &quartermasterpb.ListClustersResponse{
		Clusters: clusters,
		Pagination: &commonpb.CursorPaginationResponse{
			TotalCount: total,
		},
	}
	if startCursor != "" {
		resp.Pagination.StartCursor = &startCursor
	}
	if endCursor != "" {
		resp.Pagination.EndCursor = &endCursor
	}
	if params.Direction == pagination.Forward {
		resp.Pagination.HasNextPage = hasMore
		resp.Pagination.HasPreviousPage = params.Cursor != nil
	} else {
		resp.Pagination.HasPreviousPage = hasMore
		resp.Pagination.HasNextPage = params.Cursor != nil
	}

	return resp, nil
}

// CreateCluster creates a new cluster
func (s *QuartermasterServer) CreateCluster(ctx context.Context, req *quartermasterpb.CreateClusterRequest) (*quartermasterpb.ClusterResponse, error) {
	clusterID := req.GetClusterId()
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}
	clusterType := strings.TrimSpace(req.GetClusterType())
	if clusterType == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_type required")
	}
	if !models.IsValidClusterType(clusterType) {
		return nil, status.Errorf(codes.InvalidArgument, "cluster_type must be one of [%s], got %q", strings.Join(models.ClusterTypeValues(), ", "), clusterType)
	}

	userID := middleware.GetUserID(ctx)
	queries := quartermasterdb.New(s.db)
	// Determine deployment model (default to 'managed')
	deploymentModel := req.GetDeploymentModel()
	if deploymentModel == "" {
		deploymentModel = "managed"
	}

	// Validate owner_tenant_id if provided
	ownerTenantID := ""
	if req.OwnerTenantId != nil && *req.OwnerTenantId != "" {
		exists, err := queries.TenantExists(ctx, *req.OwnerTenantId)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to validate owner_tenant_id: %v", err)
		}
		if !exists {
			return nil, status.Error(codes.InvalidArgument, "owner_tenant_id does not exist")
		}
		ownerTenantID = *req.OwnerTenantId
	}

	id := uuid.New().String()
	now := time.Now()

	isPlatformOfficial := false
	if req.IsPlatformOfficial != nil {
		isPlatformOfficial = *req.IsPlatformOfficial
	}

	isDefaultCluster := false
	if req.IsDefaultCluster != nil {
		isDefaultCluster = *req.IsDefaultCluster
	}

	allowPrivatePullSources := false
	if req.AllowPrivatePullSources != nil {
		allowPrivatePullSources = *req.AllowPrivatePullSources
	}
	publicTopology := false
	if req.PublicTopology != nil {
		publicTopology = *req.PublicTopology
	}

	// At most one cluster can be the default — clear existing before setting.
	if isDefaultCluster {
		if err := queries.ClearDefaultCluster(ctx); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to clear existing default cluster: %v", err)
		}
	}

	baseURL := dns.NormalizeDomainScope(req.GetBaseUrl())
	err := queries.CreateInfrastructureCluster(ctx, quartermasterdb.CreateInfrastructureClusterParams{
		ID: id, ClusterID: clusterID, ClusterName: req.GetClusterName(), ClusterType: clusterType,
		DeploymentModel: validString(deploymentModel), OwnerTenantID: ownerTenantID, BaseUrl: baseURL,
		DatabaseUrl: optionalString(req.DatabaseUrl), PeriscopeUrl: optionalString(req.PeriscopeUrl),
		KafkaBrokers: req.GetKafkaBrokers(), MaxConcurrentStreams: validInt32(req.GetMaxConcurrentStreams()),
		MaxConcurrentViewers: validInt32(req.GetMaxConcurrentViewers()), MaxBandwidthMbps: validInt32(req.GetMaxBandwidthMbps()),
		IsPlatformOfficial: validBool(isPlatformOfficial), IsDefaultCluster: validBool(isDefaultCluster),
		PublicTopology: publicTopology, AllowPrivatePullSources: allowPrivatePullSources, CreatedAt: now,
	})

	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create cluster: %v", err)
	}

	// Assign idle Foghorn instances to this cluster via service_cluster_assignments.
	// "Idle" = Foghorn with zero active assignments in the junction table.
	if foghornCount := req.GetFoghornCount(); foghornCount > 0 {
		claimed, claimErr := queries.ClaimIdleFoghornsForCluster(ctx, quartermasterdb.ClaimIdleFoghornsForClusterParams{
			ClusterID: clusterID, FoghornCount: foghornCount,
		})
		if claimErr != nil {
			s.logger.WithError(claimErr).Warn("Failed to assign Foghorn instances to cluster")
			if hsErr := queries.MarkClusterProvisioning(ctx, clusterID); hsErr != nil {
				s.logger.WithError(hsErr).WithField("cluster_id", clusterID).Warn("Failed to update cluster health_status to provisioning")
			}
		} else if claimed < int64(foghornCount) {
			s.logger.WithFields(logging.Fields{
				"cluster_id": clusterID,
				"requested":  foghornCount,
				"claimed":    claimed,
			}).Warn("Assigned fewer Foghorn instances than requested")
			if hsErr := queries.MarkClusterProvisioning(ctx, clusterID); hsErr != nil {
				s.logger.WithError(hsErr).WithField("cluster_id", clusterID).Warn("Failed to update cluster health_status to provisioning")
			}
		} else {
			s.logger.WithFields(logging.Fields{
				"cluster_id": clusterID,
				"requested":  foghornCount,
				"claimed":    claimed,
			}).Info("Assigned Foghorn instances to cluster")
		}
		// New cluster gained pooled foghorn members; wake foghorn.<cluster> now.
		s.fireNavigatorSyncForPoolClusters("foghorn", []string{clusterID})
	}

	cluster, err := s.queryCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}

	tenantID := ownerTenantID
	if cluster.OwnerTenantId != nil && *cluster.OwnerTenantId != "" {
		tenantID = *cluster.OwnerTenantId
	}
	s.emitClusterEvent(ctx, eventClusterCreated, tenantID, userID, clusterID, "cluster", clusterID, "", "", "")

	return &quartermasterpb.ClusterResponse{Cluster: cluster}, nil
}

// UpdateCluster updates a cluster's properties
func (s *QuartermasterServer) UpdateCluster(ctx context.Context, req *quartermasterpb.UpdateClusterRequest) (*quartermasterpb.ClusterResponse, error) {
	clusterID := req.GetClusterId()
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}

	userID := middleware.GetUserID(ctx)
	queries := quartermasterdb.New(s.db)
	var clusterUpdate quartermasterdb.ClusterUpdate
	hasUpdates := false

	if req.ClusterName != nil {
		clusterUpdate.ClusterName = req.ClusterName
		hasUpdates = true
	}
	if req.BaseUrl != nil {
		baseURL := dns.NormalizeDomainScope(*req.BaseUrl)
		clusterUpdate.BaseURL = &baseURL
		hasUpdates = true
	}
	if req.HealthStatus != nil {
		clusterUpdate.HealthStatus = req.HealthStatus
		hasUpdates = true
	}
	if req.IsActive != nil {
		clusterUpdate.IsActive = req.IsActive
		hasUpdates = true
	}
	// Handle owner_tenant_id (empty string clears ownership)
	if req.OwnerTenantId != nil {
		if *req.OwnerTenantId != "" {
			// Validate the tenant exists
			exists, err := queries.TenantExists(ctx, *req.OwnerTenantId)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "failed to validate owner_tenant_id: %v", err)
			}
			if !exists {
				return nil, status.Error(codes.InvalidArgument, "owner_tenant_id does not exist")
			}
		}
		clusterUpdate.OwnerTenantIDSet = true
		clusterUpdate.OwnerTenantID = req.OwnerTenantId
		hasUpdates = true
	}
	if req.DeploymentModel != nil {
		clusterUpdate.DeploymentModel = req.DeploymentModel
		hasUpdates = true
	}
	if req.IsPlatformOfficial != nil {
		clusterUpdate.IsPlatformOfficial = req.IsPlatformOfficial
		hasUpdates = true
	}
	if req.IsDefaultCluster != nil {
		if *req.IsDefaultCluster {
			// At most one cluster can be the default — clear existing before setting.
			if err := queries.ClearDefaultCluster(ctx); err != nil {
				return nil, status.Errorf(codes.Internal, "failed to clear existing default cluster: %v", err)
			}
		}
		clusterUpdate.IsDefaultCluster = req.IsDefaultCluster
		hasUpdates = true
	}
	if req.AllowPrivatePullSources != nil {
		clusterUpdate.AllowPrivatePullSources = req.AllowPrivatePullSources
		hasUpdates = true
	}
	if req.PublicTopology != nil {
		clusterUpdate.PublicTopology = req.PublicTopology
		hasUpdates = true
	}

	if !hasUpdates {
		return nil, status.Error(codes.InvalidArgument, "no fields to update")
	}

	rows, err := queries.UpdateClusterFields(ctx, clusterID, clusterUpdate)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update cluster: %v", err)
	}

	if rows == 0 {
		return nil, status.Error(codes.NotFound, "cluster not found")
	}

	cluster, err := s.queryCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}

	tenantID := ""
	if cluster.OwnerTenantId != nil && *cluster.OwnerTenantId != "" {
		tenantID = *cluster.OwnerTenantId
	}
	s.emitClusterEvent(ctx, eventClusterUpdated, tenantID, userID, clusterID, "cluster", clusterID, "", "", "")

	return &quartermasterpb.ClusterResponse{Cluster: cluster}, nil
}

// UpdateClusterMeshConfig stores the WireGuard mesh parameters for a cluster
// so BootstrapInfrastructureNode can allocate mesh IPs for enrolling nodes.
// Sourced from the manifest's wireguard.* block during cluster provision.
func (s *QuartermasterServer) UpdateClusterMeshConfig(ctx context.Context, req *quartermasterpb.UpdateClusterMeshConfigRequest) (*quartermasterpb.UpdateClusterMeshConfigResponse, error) {
	clusterID := req.GetClusterId()
	meshCIDR := strings.TrimSpace(req.GetMeshCidr())
	port := req.GetWgListenPort()

	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}
	if meshCIDR == "" {
		return nil, status.Error(codes.InvalidArgument, "mesh_cidr required")
	}
	if _, _, err := net.ParseCIDR(meshCIDR); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid mesh_cidr: %v", err)
	}
	if port <= 0 || port > 65535 {
		return nil, status.Error(codes.InvalidArgument, "wg_listen_port must be 1-65535")
	}

	rows, err := quartermasterdb.New(s.db).UpdateClusterMeshConfig(ctx, quartermasterdb.UpdateClusterMeshConfigParams{
		MeshCidr: meshCIDR, WgListenPort: validInt32(port), ClusterID: clusterID,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update mesh config: %v", err)
	}
	if rows == 0 {
		return nil, status.Error(codes.NotFound, "cluster not found")
	}

	return &quartermasterpb.UpdateClusterMeshConfigResponse{
		ClusterId:    clusterID,
		MeshCidr:     meshCIDR,
		WgListenPort: port,
	}, nil
}

// ListClustersForTenant returns clusters accessible to a tenant
func (s *QuartermasterServer) ListClustersForTenant(ctx context.Context, req *quartermasterpb.ListClustersForTenantRequest) (*quartermasterpb.ClustersAccessResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	filter := quartermasterdb.SimplePageFilter{ScopeID: tenantID, Backward: params.Direction == pagination.Backward, Limit: params.Limit + 1}
	if params.Cursor != nil {
		filter.CursorTime, filter.CursorID = &params.Cursor.Timestamp, params.Cursor.ID
	}
	rows, total, err := quartermasterdb.New(s.db).ListTenantClusterAccessPage(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	var clusters []*quartermasterpb.ClusterAccessEntry
	type entryWithCursor struct {
		entry     *quartermasterpb.ClusterAccessEntry
		createdAt time.Time
		id        string
	}
	var entries []entryWithCursor
	for _, row := range rows {
		entry := &quartermasterpb.ClusterAccessEntry{ClusterId: row.ClusterID, ClusterName: row.ClusterName, AccessLevel: row.AccessLevel}
		entries = append(entries, entryWithCursor{entry: entry, createdAt: row.CreatedAt, id: row.ID})
	}

	// Determine pagination info
	resultsLen := len(entries)
	if resultsLen > params.Limit {
		entries = entries[:params.Limit]
	}

	// Reverse results for backward pagination
	if params.Direction == pagination.Backward {
		slices.Reverse(entries)
	}

	// Build cursors and extract entries
	var startCursor, endCursor string
	for _, e := range entries {
		clusters = append(clusters, e.entry)
	}
	if len(entries) > 0 {
		first := entries[0]
		last := entries[len(entries)-1]
		startCursor = pagination.EncodeCursor(first.createdAt, first.id)
		endCursor = pagination.EncodeCursor(last.createdAt, last.id)
	}

	return &quartermasterpb.ClustersAccessResponse{
		Clusters:   clusters,
		Pagination: pagination.BuildResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
	}, nil
}

// ListClustersAvailable returns clusters available for tenant onboarding
func (s *QuartermasterServer) ListClustersAvailable(ctx context.Context, req *quartermasterpb.ListClustersAvailableRequest) (*quartermasterpb.ClustersAvailableResponse, error) {
	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	filter := quartermasterdb.SimplePageFilter{Backward: params.Direction == pagination.Backward, Limit: params.Limit + 1}
	if params.Cursor != nil {
		filter.CursorTime, filter.CursorID = &params.Cursor.Timestamp, params.Cursor.ID
	}
	rows, total, err := quartermasterdb.New(s.db).ListAvailableClustersPage(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	type entryWithCursor struct {
		entry     *quartermasterpb.AvailableClusterEntry
		createdAt time.Time
		clusterID string
	}
	var entries []entryWithCursor
	for _, row := range rows {
		entry := &quartermasterpb.AvailableClusterEntry{ClusterId: row.ClusterID, ClusterName: row.ClusterName, AutoEnroll: row.AutoEnroll, Tiers: []string{row.ClusterType}}
		entries = append(entries, entryWithCursor{entry: entry, createdAt: row.CreatedAt, clusterID: row.ClusterID})
	}

	// Determine pagination info
	resultsLen := len(entries)
	if resultsLen > params.Limit {
		entries = entries[:params.Limit]
	}

	// Reverse results for backward pagination
	if params.Direction == pagination.Backward {
		slices.Reverse(entries)
	}

	// Build cursors and extract entries
	var clusters []*quartermasterpb.AvailableClusterEntry
	var startCursor, endCursor string
	for _, e := range entries {
		clusters = append(clusters, e.entry)
	}
	if len(entries) > 0 {
		first := entries[0]
		last := entries[len(entries)-1]
		startCursor = pagination.EncodeCursor(first.createdAt, first.clusterID)
		endCursor = pagination.EncodeCursor(last.createdAt, last.clusterID)
	}

	return &quartermasterpb.ClustersAvailableResponse{
		Clusters:   clusters,
		Pagination: pagination.BuildResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
	}, nil
}

// GrantClusterAccess grants a tenant access to a cluster
func (s *QuartermasterServer) GrantClusterAccess(ctx context.Context, req *quartermasterpb.GrantClusterAccessRequest) (*emptypb.Empty, error) {
	tenantID := req.GetTenantId()
	clusterID := req.GetClusterId()

	if tenantID == "" || clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and cluster_id required")
	}

	accessLevel := req.GetAccessLevel()
	if accessLevel == "" {
		accessLevel = "read"
	}

	// Grant + durable alias ensure in one tx (ensure is gated, so it no-ops
	// unless the tenant now warrants an alias).
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	expiresAt := sql.NullTime{}
	if req.ExpiresAt != nil {
		expiresAt = sql.NullTime{Time: req.ExpiresAt.AsTime(), Valid: true}
	}
	if err := quartermasterdb.New(tx).GrantTenantClusterAccess(ctx, quartermasterdb.GrantTenantClusterAccessParams{
		TenantID: tenantID, ClusterID: clusterID, AccessLevel: validString(accessLevel), ExpiresAt: expiresAt,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to grant access: %v", err)
	}

	if enqErr := s.enqueueTenantAliasEnsureTx(ctx, tx, tenantID, true); enqErr != nil {
		return nil, status.Errorf(codes.Internal, "enqueue tenant-alias ensure: %v", enqErr)
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return nil, status.Errorf(codes.Internal, "commit grant access: %v", commitErr)
	}

	return &emptypb.Empty{}, nil
}

// BootstrapClusterAccess is the service-token bootstrap entitlement entry
// point. Unlike SubscribeToCluster, it does not require a user/tenant session —
// the calling service (Purser bootstrap, today) supplies tenant_id directly.
// The server still enforces the is_platform_official boundary so a private
// customer cluster's pricing rows can never be turned into entitlements via
// this path.
func (s *QuartermasterServer) BootstrapClusterAccess(ctx context.Context, req *quartermasterpb.BootstrapClusterAccessRequest) (*emptypb.Empty, error) {
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "BootstrapClusterAccess requires service token auth")
	}
	tenantID := req.GetTenantId()
	clusterID := req.GetClusterId()
	if tenantID == "" || clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and cluster_id required")
	}

	// Validate tenant exists. tenant_cluster_access has no FK on tenant_id
	// (its UUID type is unconstrained at the schema level), so without this
	// check a typo'd UUID would silently produce an orphan access row.
	queries := quartermasterdb.New(s.db)
	tenantExists, err := queries.TenantExists(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "probe tenant: %v", err)
	}
	if !tenantExists {
		return nil, status.Errorf(codes.NotFound, "tenant %q not found", tenantID)
	}

	clusterState, err := queries.GetClusterOfficialState(ctx, clusterID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Errorf(codes.NotFound, "cluster %q not found", clusterID)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "probe cluster: %v", err)
	}
	if !clusterState.IsPlatformOfficial.Valid || !clusterState.IsActive.Valid {
		return nil, status.Error(codes.Internal, "probe cluster: state fields are NULL")
	}
	if !clusterState.IsPlatformOfficial.Bool {
		return nil, status.Errorf(codes.FailedPrecondition, "cluster %q is not platform-official", clusterID)
	}
	if !clusterState.IsActive.Bool {
		return nil, status.Errorf(codes.FailedPrecondition, "cluster %q is not active", clusterID)
	}

	// resource_limits is only for tenant/cluster-specific overrides. Plan
	// defaults are resolved by Purser tier entitlements during admission.
	// COALESCE preserves any prior override on re-bootstrap.
	var resourceLimitsJSON []byte
	if lim := req.GetResourceLimits(); lim != nil {
		// Only encode when at least one cap is set; an all-zero struct means
		// "no caps configured" — leave the column at its default.
		if lim.GetMaxStreams() > 0 || lim.GetMaxViewers() > 0 {
			payload := map[string]int32{}
			if v := lim.GetMaxStreams(); v > 0 {
				payload["max_streams"] = v
			}
			if v := lim.GetMaxViewers(); v > 0 {
				payload["max_viewers"] = v
			}
			b, marshalErr := json.Marshal(payload)
			if marshalErr != nil {
				return nil, status.Errorf(codes.Internal, "marshal resource_limits: %v", marshalErr)
			}
			resourceLimitsJSON = b
		}
	}

	// Wrap the access upsert and the durable alias ensure in one tx so a
	// crash can't leave the tenant subscribed without an alias hand-off.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	resourceLimits := sql.NullString{}
	if len(resourceLimitsJSON) > 0 {
		resourceLimits = validString(string(resourceLimitsJSON))
	}
	if err := quartermasterdb.New(tx).BootstrapTenantClusterAccess(ctx, quartermasterdb.BootstrapTenantClusterAccessParams{
		TenantID: tenantID, ClusterID: clusterID, ResourceLimits: resourceLimits,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "upsert tenant_cluster_access: %v", err)
	}

	// Trigger Navigator alias provisioning durably. A Navigator outage MUST
	// NOT block billing/tier mutations — the outbox replays until it lands.
	if enqErr := s.enqueueTenantAliasEnsureTx(ctx, tx, tenantID, true); enqErr != nil {
		return nil, status.Errorf(codes.Internal, "enqueue tenant-alias ensure: %v", enqErr)
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return nil, status.Errorf(codes.Internal, "commit bootstrap cluster access: %v", commitErr)
	}

	return &emptypb.Empty{}, nil
}

// DeactivateClusterAccess soft-suspends a tenant_cluster_access row.
// Service-token only. Idempotent: a no-op if the row is already inactive or
// absent. Purser calls this from tier downgrade reconciliation; the row is
// retained so a future upgrade can re-activate it without losing any
// resource_limits override or audit history.
func (s *QuartermasterServer) DeactivateClusterAccess(ctx context.Context, req *quartermasterpb.DeactivateClusterAccessRequest) (*emptypb.Empty, error) {
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "DeactivateClusterAccess requires service token auth")
	}
	tenantID := req.GetTenantId()
	clusterID := req.GetClusterId()
	if tenantID == "" || clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and cluster_id required")
	}
	// All Navigator hand-offs are durable and ride one tx with the access
	// flip. remove_cluster is enqueued first so it gets the lower seq and is
	// dispatched before any full teardown below. The removal is durable, not
	// synchronous: Navigator drops the cluster's DNS membership when the
	// worker drains, which may land after the access row flips inactive — we
	// accept async durable removal here.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := s.EnqueueNavigatorTenantAliasTx(ctx, tx, tenantID, "", "remove_cluster", clusterID, "cluster_access_deactivated"); err != nil {
		return nil, status.Errorf(codes.Internal, "enqueue tenant-alias remove_cluster: %v", err)
	}

	if err := quartermasterdb.New(tx).DeactivateTenantClusterAccess(ctx, quartermasterdb.DeactivateTenantClusterAccessParams{
		TenantID: tenantID, ClusterID: clusterID,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "deactivate tenant_cluster_access: %v", err)
	}

	// If the tenant now has zero active paid cluster access rows, tear the
	// full alias down too (enqueued after remove_cluster, so higher seq).
	hasPaid, accErr := s.tenantHasPaidClusterAccessTx(ctx, tx, tenantID)
	if accErr != nil {
		return nil, status.Errorf(codes.Internal, "check paid cluster access: %v", accErr)
	}
	if !hasPaid {
		if enqErr := s.enqueueTenantAliasRemoveTx(ctx, tx, tenantID, ""); enqErr != nil {
			return nil, status.Errorf(codes.Internal, "enqueue tenant-alias remove: %v", enqErr)
		}
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return nil, status.Errorf(codes.Internal, "commit deactivate cluster access: %v", commitErr)
	}

	return &emptypb.Empty{}, nil
}

// ListTenantClusterAccess returns every tenant_cluster_access row joined with
// infrastructure_clusters.is_platform_official. Service-token only. Distinct
// from ListClustersForTenant, which is a user-facing RPC with a minimal entry
// shape and does not surface the is_active / subscription_status fields needed
// for tier reconciliation diffs.
func (s *QuartermasterServer) ListTenantClusterAccess(ctx context.Context, req *quartermasterpb.ListTenantClusterAccessRequest) (*quartermasterpb.ListTenantClusterAccessResponse, error) {
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "ListTenantClusterAccess requires service token auth")
	}
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}
	rows, err := quartermasterdb.New(s.db).ListTenantClusterAccessRows(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list tenant_cluster_access: %v", err)
	}
	out := &quartermasterpb.ListTenantClusterAccessResponse{}
	for _, row := range rows {
		if !row.IsActive.Valid || !row.SubscriptionStatus.Valid {
			return nil, status.Error(codes.Internal, "scan tenant_cluster_access: required fields are NULL")
		}
		r := quartermasterpb.TenantClusterAccessRow{ClusterId: row.ClusterID, IsActive: row.IsActive.Bool,
			SubscriptionStatus: row.SubscriptionStatus.String, IsPlatformOfficial: row.IsPlatformOfficial}
		out.Rows = append(out.Rows, &r)
	}
	return out, nil
}

// GetTenantEntitlement returns the cluster IDs a tenant is entitled to serve on
// (active + subscribed) and the coarse plan class (the primary cluster's
// cluster_class). Service-token only. This owns the entitlement predicates on
// Quartermaster's side so Commodore can mint signed policy bundles without
// reading quartermaster.* directly. Cluster lookup is fail-closed; the plan
// class is fail-open (a lookup error yields an empty class, bundle still issued).
func (s *QuartermasterServer) GetTenantEntitlement(ctx context.Context, req *quartermasterpb.GetTenantEntitlementRequest) (*quartermasterpb.GetTenantEntitlementResponse, error) {
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "GetTenantEntitlement requires service token auth")
	}
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	queries := quartermasterdb.New(s.db)
	clusterIDs, err := queries.ListTenantEntitledClusterIDs(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "tenant cluster access lookup: %v", err)
	}
	out := &quartermasterpb.GetTenantEntitlementResponse{AllowedClusterIds: clusterIDs}

	var planClass sql.NullString
	planClass, scanErr := queries.GetTenantPrimaryClusterClass(ctx, tenantID)
	if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		s.logger.WithError(scanErr).WithField("tenant_id", tenantID).
			Warn("plan class lookup failed; entitlement returned without plan_class")
	}
	out.PlanClass = planClass.String
	return out, nil
}

// ListServiceClusterAssignments returns the distinct cluster IDs a specific
// running service instance is actively assigned to serve. Service-token only.
// This owns the service_cluster_assignments join on Quartermaster's side so
// pool members (Foghorn) can load their served-cluster set without reading
// quartermaster.* directly.
func (s *QuartermasterServer) ListServiceClusterAssignments(ctx context.Context, req *quartermasterpb.ListServiceClusterAssignmentsRequest) (*quartermasterpb.ListServiceClusterAssignmentsResponse, error) {
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "ListServiceClusterAssignments requires service token auth")
	}
	instanceID := req.GetInstanceId()
	if instanceID == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	serviceType := req.GetServiceType()
	if serviceType == "" {
		return nil, status.Error(codes.InvalidArgument, "service_type required")
	}

	clusterIDs, err := quartermasterdb.New(s.db).ListRunningServiceClusterAssignments(ctx, quartermasterdb.ListRunningServiceClusterAssignmentsParams{
		InstanceID: instanceID, ServiceType: validString(serviceType),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list service cluster assignments: %v", err)
	}
	return &quartermasterpb.ListServiceClusterAssignmentsResponse{ClusterIds: clusterIDs}, nil
}

// SubscribeToCluster subscribes a tenant to a public/shared cluster
func (s *QuartermasterServer) SubscribeToCluster(ctx context.Context, req *quartermasterpb.SubscribeToClusterRequest) (*emptypb.Empty, error) {
	tenantID := middleware.GetTenantID(ctx)
	if tenantID == "" {
		return nil, status.Error(codes.Unauthenticated, "tenant_id required")
	}

	// Allow admin override (if tenant_id is provided in request and differs)
	if req.GetTenantId() != "" && req.GetTenantId() != tenantID {
		role := ctxkeys.GetRole(ctx)
		if role == "admin" || role == "provider" {
			tenantID = req.GetTenantId()
		} else {
			return nil, status.Error(codes.PermissionDenied, "cannot subscribe other tenants")
		}
	}

	clusterID := req.GetClusterId()
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}

	// Verify cluster exists and is 'shared'
	queries := quartermasterdb.New(s.db)
	deploymentModel, err := queries.GetClusterDeploymentModel(ctx, clusterID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "cluster not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	if !deploymentModel.Valid {
		return nil, status.Error(codes.Internal, "database error: cluster deployment_model is NULL")
	}
	if deploymentModel.String != "shared" {
		return nil, status.Error(codes.PermissionDenied, "cannot subscribe to non-shared cluster")
	}

	// Create subscription + durable alias ensure in one tx so a Navigator
	// outage can't leave the tenant subscribed without an alias hand-off.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if err := quartermasterdb.New(tx).SubscribeTenantToCluster(ctx, quartermasterdb.SubscribeTenantToClusterParams{
		TenantID: tenantID, ClusterID: clusterID,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to subscribe: %v", err)
	}

	if enqErr := s.enqueueTenantAliasEnsureTx(ctx, tx, tenantID, true); enqErr != nil {
		return nil, status.Errorf(codes.Internal, "enqueue tenant-alias ensure: %v", enqErr)
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return nil, status.Errorf(codes.Internal, "commit subscribe: %v", commitErr)
	}

	return &emptypb.Empty{}, nil
}

// UnsubscribeFromCluster unsubscribes a tenant from a cluster
func (s *QuartermasterServer) UnsubscribeFromCluster(ctx context.Context, req *quartermasterpb.UnsubscribeFromClusterRequest) (*emptypb.Empty, error) {
	tenantID := middleware.GetTenantID(ctx)
	if tenantID == "" {
		return nil, status.Error(codes.Unauthenticated, "tenant_id required")
	}

	// Allow admin override
	if req.GetTenantId() != "" && req.GetTenantId() != tenantID {
		role := ctxkeys.GetRole(ctx)
		if role == "admin" || role == "provider" {
			tenantID = req.GetTenantId()
		} else {
			return nil, status.Error(codes.PermissionDenied, "cannot unsubscribe other tenants")
		}
	}

	clusterID := req.GetClusterId()
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}

	// Deactivation + durable alias hand-off in one tx. remove_cluster is
	// enqueued first (lower seq, dispatched first); a full teardown follows
	// only when no paid cluster access remains.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := s.EnqueueNavigatorTenantAliasTx(ctx, tx, tenantID, "", "remove_cluster", clusterID, "cluster_unsubscribed"); err != nil {
		return nil, status.Errorf(codes.Internal, "enqueue tenant-alias remove_cluster: %v", err)
	}

	if err := quartermasterdb.New(tx).UnsubscribeTenantFromCluster(ctx, quartermasterdb.UnsubscribeTenantFromClusterParams{
		TenantID: tenantID, ClusterID: clusterID,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to unsubscribe: %v", err)
	}

	hasPaid, accErr := s.tenantHasPaidClusterAccessTx(ctx, tx, tenantID)
	if accErr != nil {
		return nil, status.Errorf(codes.Internal, "check paid cluster access: %v", accErr)
	}
	if !hasPaid {
		if enqErr := s.enqueueTenantAliasRemoveTx(ctx, tx, tenantID, ""); enqErr != nil {
			return nil, status.Errorf(codes.Internal, "enqueue tenant-alias remove: %v", enqErr)
		}
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return nil, status.Errorf(codes.Internal, "commit unsubscribe: %v", commitErr)
	}

	return &emptypb.Empty{}, nil
}

// ListMySubscriptions lists clusters the tenant is subscribed to
func (s *QuartermasterServer) ListMySubscriptions(ctx context.Context, req *quartermasterpb.ListMySubscriptionsRequest) (*quartermasterpb.ListClustersResponse, error) {
	tenantID := middleware.GetTenantID(ctx)
	s.logger.WithField("tenant_id", tenantID).Info("ListMySubscriptions: called")
	if tenantID == "" {
		s.logger.Warn("ListMySubscriptions: tenant_id is empty - rejecting")
		return nil, status.Error(codes.Unauthenticated, "tenant_id required")
	}

	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	filter := quartermasterdb.SimplePageFilter{ScopeID: tenantID, Backward: params.Direction == pagination.Backward, Limit: params.Limit + 1}
	if params.Cursor != nil {
		filter.CursorTime, filter.CursorID = &params.Cursor.Timestamp, params.Cursor.ID
	}
	rows, total, err := quartermasterdb.New(s.db).ListSubscribedClustersPage(ctx, filter)
	if err != nil {
		s.logger.WithError(err).WithField("tenant_id", tenantID).Error("ListMySubscriptions: query failed")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	s.logger.WithFields(map[string]any{
		"tenant_id":   tenantID,
		"total_count": total,
	}).Info("ListMySubscriptions: found subscribed clusters")

	var clusters []*quartermasterpb.InfrastructureCluster
	for _, row := range rows {
		clusters = append(clusters, clusterFromListRow(row))
	}

	// Determine pagination info
	resultsLen := len(clusters)
	if resultsLen > params.Limit {
		clusters = clusters[:params.Limit]
	}

	// Reverse results for backward pagination
	if params.Direction == pagination.Backward {
		slices.Reverse(clusters)
	}

	// Build cursors
	var startCursor, endCursor string
	if len(clusters) > 0 {
		first := clusters[0]
		last := clusters[len(clusters)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.Id)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.Id)
	}

	return &quartermasterpb.ListClustersResponse{
		Clusters:   clusters,
		Pagination: pagination.BuildResponse(resultsLen, params.Limit, params.Direction, int32(len(clusters)), startCursor, endCursor),
	}, nil
}

// GetNode returns a specific node
func (s *QuartermasterServer) GetNode(ctx context.Context, req *quartermasterpb.GetNodeRequest) (*quartermasterpb.NodeResponse, error) {
	nodeID := req.GetNodeId()
	if nodeID == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id required")
	}

	node, err := s.queryNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}

	return &quartermasterpb.NodeResponse{Node: node}, nil
}

// GetNodeByLogicalName resolves a node by its logical name (node_id string like "edge-node-1")
// Used by Foghorn to get the database UUID for subscription enrichment
func (s *QuartermasterServer) GetNodeByLogicalName(ctx context.Context, req *quartermasterpb.GetNodeByLogicalNameRequest) (*quartermasterpb.NodeResponse, error) {
	nodeID := req.GetNodeId()
	if nodeID == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id required")
	}

	node, err := s.queryNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}

	return &quartermasterpb.NodeResponse{Node: node}, nil
}

// UpdateNodeStatus changes routing-visible node state for lifecycle actions.
func (s *QuartermasterServer) UpdateNodeStatus(ctx context.Context, req *quartermasterpb.UpdateNodeStatusRequest) (*quartermasterpb.NodeResponse, error) {
	nodeID := strings.TrimSpace(req.GetNodeId())
	nextStatus := strings.TrimSpace(req.GetStatus())
	if nodeID == "" || nextStatus == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id and status required")
	}
	switch nextStatus {
	case "active", "offline", "maintenance", "retired", "evicted":
	default:
		return nil, status.Errorf(codes.InvalidArgument, "unsupported node status %q", nextStatus)
	}

	tenantID := middleware.GetTenantID(ctx)
	authType := ctxkeys.GetAuthType(ctx)
	if tenantID == "" && authType != "service" {
		return nil, status.Error(codes.Unauthenticated, "authentication required")
	}

	statusScope := quartermasterdb.NodeStatusScopeActiveClusters
	if tenantID != "" {
		providerActor, err := s.hasProviderLifecycleAuthority(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		if !providerActor {
			statusScope = quartermasterdb.NodeStatusScopeTenantOwner
		}
	}
	queries := quartermasterdb.New(s.db)
	updated, err := queries.UpdateNodeStatus(ctx, quartermasterdb.UpdateNodeStatusParams{
		NodeID: nodeID, Status: nextStatus, ExpectedClusterID: strings.TrimSpace(req.GetExpectedClusterId()),
		Scope: statusScope, TenantID: tenantID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "node not found")
	} else if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update node status: %v", err)
	}
	canonicalNodeID, canonicalClusterID := updated.NodeID, updated.ClusterID

	// Any non-active status transition makes the node ineligible for DNS.
	// Both aggregate `edge` and the edge-* subtypes gate on
	// service_instances.health_status (plus n.status='active'). Flip every
	// edge service instance to 'unhealthy' so the polling Navigator reconcile
	// would converge even if our targeted wake-up below fails.
	if nextStatus != "active" {
		if markErr := queries.MarkNodeEdgeInstancesOffline(ctx, validString(canonicalNodeID)); markErr != nil {
			s.logger.WithError(markErr).WithField("node_id", canonicalNodeID).
				Warn("Failed to mark edge service_instances unhealthy after UpdateNodeStatus")
		}
		if canonicalClusterID != "" {
			pairs := map[dnsPairKey]struct{}{}
			for _, mapping := range edgeServiceTypeDerivations {
				pairs[dnsPairKey{canonicalClusterID, mapping.serviceType}] = struct{}{}
			}
			s.fireNavigatorSyncForPairs(pairs)
		}
	}

	// A node status transition changes DNS eligibility for every pool-assigned service
	// it hosts (foghorn, chandler, livepeer-gateway, vmauth/telemetry), gated on
	// n.status='active': the pooled record of each media cluster those instances serve
	// AND, for physical types, the node-keyed infra record. Wake by served cluster so
	// both prune on leaving active and republish on returning, not at the reconcile
	// tick.
	for _, poolType := range dns.PoolAssignedServiceTypes() {
		s.fireNavigatorSyncForPoolClusters(poolType, s.servedClustersForNodeType(ctx, canonicalNodeID, poolType))
	}

	node, err := s.queryNode(ctx, canonicalNodeID)
	if err != nil {
		return nil, err
	}
	return &quartermasterpb.NodeResponse{Node: node}, nil
}

func (s *QuartermasterServer) hasProviderLifecycleAuthority(ctx context.Context, tenantID string) (bool, error) {
	if ctxkeys.GetAuthType(ctx) == "service" {
		return true, nil
	}
	if ctxkeys.GetRole(ctx) != "provider" || strings.TrimSpace(tenantID) == "" {
		return false, nil
	}
	parsedTenantID := uuid.Nil
	if value, parseErr := uuid.Parse(tenantID); parseErr == nil {
		parsedTenantID = value
	}
	if parsedTenantID == uuid.Nil {
		return false, nil
	}

	isProvider, err := quartermasterdb.New(s.db).TenantIsProvider(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, status.Errorf(codes.Internal, "provider authority check failed: %v", err)
	}
	return isProvider, nil
}

func (s *QuartermasterServer) ListEdgeReleases(ctx context.Context, req *quartermasterpb.ListEdgeReleasesRequest) (*quartermasterpb.ListEdgeReleasesResponse, error) {
	filter := quartermasterdb.EdgeReleaseFilter{}
	if strings.TrimSpace(req.GetChannel()) != "" {
		channel, err := normalizeReleaseTargetChannel(req.GetChannel())
		if err != nil {
			return nil, err
		}
		filter.Channel = &channel
	}
	if version := strings.TrimSpace(req.GetVersion()); version != "" {
		filter.Version = &version
	}
	var releases []*quartermasterpb.EdgeRelease
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		rows, err := s.listEdgeReleasesNoRetry(ctx, filter)
		if err == nil {
			releases = rows
		}
		return err
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	return &quartermasterpb.ListEdgeReleasesResponse{Releases: releases}, nil
}

func (s *QuartermasterServer) listEdgeReleasesNoRetry(ctx context.Context, filter quartermasterdb.EdgeReleaseFilter) ([]*quartermasterpb.EdgeRelease, error) {
	rows, err := quartermasterdb.New(s.db).ListEdgeReleases(ctx, filter)
	if err != nil {
		return nil, err
	}
	releases := make([]*quartermasterpb.EdgeRelease, 0, len(rows))
	for _, row := range rows {
		releases = append(releases, &quartermasterpb.EdgeRelease{
			Channel: row.Channel, Version: row.Version, ComponentsJson: row.ComponentsJSON,
			PublishedAt: timestamppb.New(row.PublishedAt),
		})
	}
	return releases, nil
}

func (s *QuartermasterServer) UpsertEdgeRelease(ctx context.Context, req *quartermasterpb.UpsertEdgeReleaseRequest) (*quartermasterpb.EdgeReleaseResponse, error) {
	tenantID := middleware.GetTenantID(ctx)
	ok, err := s.hasProviderLifecycleAuthority(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, status.Error(codes.PermissionDenied, "provider authority required")
	}
	release := req.GetRelease()
	if release == nil || strings.TrimSpace(release.GetChannel()) == "" || strings.TrimSpace(release.GetVersion()) == "" {
		return nil, status.Error(codes.InvalidArgument, "release channel and version required")
	}
	channel, err := normalizeReleaseTargetChannel(release.GetChannel())
	if err != nil {
		return nil, err
	}
	version := strings.TrimSpace(release.GetVersion())
	components, err := normalizeJSONObject(release.GetComponentsJson(), "components_json")
	if err != nil {
		return nil, err
	}
	if validateErr := validateEdgeReleaseComponents(components); validateErr != nil {
		return nil, validateErr
	}
	publishedAt := time.Now()
	if release.GetPublishedAt() != nil {
		publishedAt = release.GetPublishedAt().AsTime()
	}
	var saved *quartermasterpb.EdgeRelease
	err = database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		row, queryErr := quartermasterdb.New(s.db).UpsertEdgeRelease(ctx, quartermasterdb.UpsertEdgeReleaseParams{
			Channel: channel, Version: version, ComponentsJSON: components, PublishedAt: publishedAt,
		})
		if queryErr == nil {
			saved = &quartermasterpb.EdgeRelease{Channel: row.Channel, Version: row.Version,
				ComponentsJson: row.ComponentsJSON, PublishedAt: timestamppb.New(row.PublishedAt)}
		}
		return queryErr
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "upsert release: %v", err)
	}
	return &quartermasterpb.EdgeReleaseResponse{Release: saved}, nil
}

func validateEdgeReleaseComponents(raw string) error {
	type releaseArtifact struct {
		ArtifactURL string `json:"artifact_url"`
		Checksum    string `json:"checksum"`
	}
	var components map[string]struct {
		Version   string                     `json:"version"`
		Artifacts map[string]releaseArtifact `json:"artifacts"`
	}
	if err := json.Unmarshal([]byte(raw), &components); err != nil {
		return status.Errorf(codes.InvalidArgument, "components_json must be an object: %v", err)
	}
	hasUpdateableComponent := false
	for component, values := range components {
		if !validEdgeReleaseComponent(component) {
			return status.Errorf(codes.InvalidArgument, "unsupported release component %q", component)
		}
		version := strings.TrimSpace(values.Version)
		if version == "" {
			return status.Errorf(codes.InvalidArgument, "%s version required", component)
		}
		if !envLineValueSafe(version) {
			return status.Errorf(codes.InvalidArgument, "%s version contains unsupported control characters", component)
		}
		if component == "config_schema" {
			continue
		}
		hasUpdateableComponent = true
		if len(values.Artifacts) == 0 {
			return status.Errorf(codes.InvalidArgument, "%s artifacts required", component)
		}
		for platform, artifact := range values.Artifacts {
			if !validReleasePlatformKey(platform) {
				return status.Errorf(codes.InvalidArgument, "%s artifact platform %q invalid", component, platform)
			}
			if strings.TrimSpace(artifact.ArtifactURL) == "" {
				return status.Errorf(codes.InvalidArgument, "%s artifact_url required for %s", component, platform)
			}
			if strings.TrimSpace(artifact.Checksum) == "" {
				return status.Errorf(codes.InvalidArgument, "%s checksum required for %s", component, platform)
			}
			if err := validateReleaseChecksum(artifact.Checksum); err != nil {
				return status.Errorf(codes.InvalidArgument, "%s checksum invalid for %s: %v", component, platform, err)
			}
		}
	}
	if !hasUpdateableComponent {
		return status.Error(codes.InvalidArgument, "components_json must include at least one updateable edge component")
	}
	return nil
}

func validReleasePlatformKey(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	if strings.Contains(value, "/") {
		parts := strings.Split(value, "/")
		return len(parts) == 2 && parts[0] != "" && parts[1] != ""
	}
	parts := strings.SplitN(value, "-", 2)
	return len(parts) == 2 && parts[0] != "" && parts[1] != ""
}

func validateReleaseChecksum(value string) error {
	value = strings.TrimSpace(value)
	algo, digest, ok := strings.Cut(value, ":")
	if !ok {
		algo, digest = "sha256", value
	}
	var hexLen int
	switch strings.ToLower(strings.TrimSpace(algo)) {
	case "sha256":
		hexLen = sha256.Size * 2
	case "sha512":
		hexLen = sha512.Size * 2
	default:
		return fmt.Errorf("unsupported checksum algorithm %q", algo)
	}
	digest = strings.TrimSpace(digest)
	if len(digest) != hexLen {
		return fmt.Errorf("digest must be %d hex characters", hexLen)
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return fmt.Errorf("digest must be hex: %w", err)
	}
	return nil
}

func validEdgeReleaseComponent(component string) bool {
	switch component {
	case "helmsman", "mist", "caddy", "config_schema":
		return true
	default:
		return false
	}
}

func envLineValueSafe(value string) bool {
	return !strings.ContainsAny(value, "\r\n\x00")
}

func (s *QuartermasterServer) GetClusterReleaseTarget(ctx context.Context, req *quartermasterpb.GetClusterReleaseTargetRequest) (*quartermasterpb.ClusterReleaseTargetResponse, error) {
	clusterID := strings.TrimSpace(req.GetClusterId())
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}
	if err := s.authorizeClusterReleaseTarget(ctx, clusterID); err != nil {
		return nil, err
	}
	target, err := s.queryClusterReleaseTarget(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	return &quartermasterpb.ClusterReleaseTargetResponse{Target: target}, nil
}

func (s *QuartermasterServer) ListClusterReleaseTargets(ctx context.Context, req *quartermasterpb.ListClusterReleaseTargetsRequest) (*quartermasterpb.ListClusterReleaseTargetsResponse, error) {
	filter := quartermasterdb.ClusterReleaseTargetFilter{}
	if clusterID := strings.TrimSpace(req.GetClusterId()); clusterID != "" {
		if err := s.authorizeClusterReleaseTarget(ctx, clusterID); err != nil {
			return nil, err
		}
		filter.ClusterID = &clusterID
	} else {
		tenantID := middleware.GetTenantID(ctx)
		ok, err := s.hasProviderLifecycleAuthority(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, status.Error(codes.InvalidArgument, "cluster_id required")
		}
	}
	var targets []*quartermasterpb.ClusterReleaseTarget
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		rows, err := s.listClusterReleaseTargetsNoRetry(ctx, filter)
		if err == nil {
			targets = rows
		}
		return err
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	return &quartermasterpb.ListClusterReleaseTargetsResponse{Targets: targets}, nil
}

func (s *QuartermasterServer) listClusterReleaseTargetsNoRetry(ctx context.Context, filter quartermasterdb.ClusterReleaseTargetFilter) ([]*quartermasterpb.ClusterReleaseTarget, error) {
	rows, err := quartermasterdb.New(s.db).ListClusterReleaseTargets(ctx, filter)
	if err != nil {
		return nil, err
	}
	targets := make([]*quartermasterpb.ClusterReleaseTarget, 0, len(rows))
	for _, row := range rows {
		targets = append(targets, releaseTargetProto(row))
	}
	return targets, nil
}

func (s *QuartermasterServer) SetClusterReleaseTarget(ctx context.Context, req *quartermasterpb.SetClusterReleaseTargetRequest) (*quartermasterpb.ClusterReleaseTargetResponse, error) {
	target := req.GetTarget()
	if target == nil || strings.TrimSpace(target.GetClusterId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster target required")
	}
	clusterID := strings.TrimSpace(target.GetClusterId())
	if err := s.authorizeClusterReleaseTarget(ctx, clusterID); err != nil {
		return nil, err
	}
	rolloutPlan, err := normalizeJSONObject(firstNonEmptyString(target.GetRolloutPlanJson(), "{}"), "rollout_plan_json")
	if err != nil {
		return nil, err
	}
	if validateErr := validateRolloutPlanJSON(rolloutPlan); validateErr != nil {
		return nil, validateErr
	}
	channel, err := normalizeReleaseTargetChannel(target.GetChannel())
	if err != nil {
		return nil, err
	}
	targetVersion := strings.TrimSpace(target.GetTargetVersion())
	var saved *quartermasterpb.ClusterReleaseTarget
	err = database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		if existsErr := s.ensureEdgeReleaseTargetExists(ctx, channel, targetVersion); existsErr != nil {
			return existsErr
		}
		row, queryErr := quartermasterdb.New(s.db).UpsertClusterReleaseTarget(ctx, quartermasterdb.UpsertClusterReleaseTargetParams{
			ClusterID: clusterID, Channel: channel, TargetVersion: targetVersion,
			RolloutPlanJSON: rolloutPlan, Paused: target.GetPaused(),
		})
		if queryErr == nil {
			saved = releaseTargetProto(row)
		}
		return queryErr
	})
	if err != nil {
		if _, ok := status.FromError(err); ok {
			return nil, err
		}
		return nil, status.Errorf(codes.Internal, "set release target: %v", err)
	}
	return &quartermasterpb.ClusterReleaseTargetResponse{Target: saved}, nil
}

func (s *QuartermasterServer) ensureEdgeReleaseTargetExists(ctx context.Context, channel, version string) error {
	var exists bool
	queries := quartermasterdb.New(s.db)
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		exists, queryErr = queries.EdgeReleaseTargetExists(ctx, channel, version)
		return queryErr
	})
	if err != nil {
		return status.Errorf(codes.Internal, "check edge release target: %v", err)
	}
	if exists {
		return nil
	}
	if strings.TrimSpace(version) == "" {
		return status.Errorf(codes.InvalidArgument, "edge release channel %s has no published releases", channel)
	}
	return status.Errorf(codes.InvalidArgument, "edge release %s:%s is not published", channel, version)
}

func (s *QuartermasterServer) authorizeClusterReleaseTarget(ctx context.Context, clusterID string) error {
	tenantID := middleware.GetTenantID(ctx)
	ok, err := s.hasProviderLifecycleAuthority(ctx, tenantID)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	if tenantID == "" {
		return status.Error(codes.Unauthenticated, "authentication required")
	}
	authorized, err := quartermasterdb.New(s.db).TenantHasClusterLifecycleAccess(ctx, quartermasterdb.TenantHasClusterLifecycleAccessParams{
		ClusterID: clusterID, TenantID: tenantID,
	})
	if err != nil {
		return status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !authorized {
		return status.Error(codes.PermissionDenied, "cluster owner access required")
	}
	return nil
}

func (s *QuartermasterServer) queryClusterReleaseTarget(ctx context.Context, clusterID string) (*quartermasterpb.ClusterReleaseTarget, error) {
	var target *quartermasterpb.ClusterReleaseTarget
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		rowTarget, err := s.queryClusterReleaseTargetNoRetry(ctx, clusterID)
		if err == nil {
			target = rowTarget
		}
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "cluster release target not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get release target: %v", err)
	}
	return target, nil
}

func (s *QuartermasterServer) queryClusterReleaseTargetNoRetry(ctx context.Context, clusterID string) (*quartermasterpb.ClusterReleaseTarget, error) {
	row, err := quartermasterdb.New(s.db).GetClusterReleaseTarget(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	return releaseTargetProto(row), nil
}

func releaseTargetProto(row quartermasterdb.ClusterReleaseTargetRow) *quartermasterpb.ClusterReleaseTarget {
	return &quartermasterpb.ClusterReleaseTarget{
		ClusterId: row.ClusterID, Channel: row.Channel, TargetVersion: row.TargetVersion,
		RolloutPlanJson: row.RolloutPlanJSON, Paused: row.Paused, UpdatedAt: timestamppb.New(row.UpdatedAt),
	}
}

func normalizeJSONObject(raw, field string) (string, error) {
	if raw == "" {
		raw = "{}"
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return "", status.Errorf(codes.InvalidArgument, "%s must be a JSON object", field)
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return "", status.Errorf(codes.InvalidArgument, "%s must be a JSON object", field)
	}
	return string(encoded), nil
}

type rolloutPlanConfig struct {
	Canary               bool   `json:"canary"`
	CanaryCount          int    `json:"canary_count"`
	BatchSize            int    `json:"batch_size"`
	CapacityFloor        int    `json:"capacity_floor"`
	CapacityFloorPercent int    `json:"capacity_floor_percent"`
	MaxFailed            int    `json:"max_failed"`
	ErrorAbort           bool   `json:"error_abort"`
	DrainDeadline        string `json:"drain_deadline"`
	Force                bool   `json:"force"`
}

func validateRolloutPlanJSON(raw string) error {
	var plan rolloutPlanConfig
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&plan); err != nil {
		return status.Errorf(codes.InvalidArgument, "rollout_plan_json invalid: %v", err)
	}
	if plan.CapacityFloor != 0 || plan.CapacityFloorPercent != 0 {
		return status.Error(codes.InvalidArgument, "rollout_plan_json capacity_floor fields are not supported for edge release targets")
	}
	if strings.TrimSpace(plan.DrainDeadline) != "" {
		if _, err := time.ParseDuration(plan.DrainDeadline); err != nil {
			return status.Errorf(codes.InvalidArgument, "rollout_plan_json drain_deadline must be a Go duration")
		}
	}
	return nil
}

func normalizeReleaseTargetChannel(channel string) (string, error) {
	channel = strings.ToLower(strings.TrimSpace(channel))
	switch channel {
	case "stable", "rc":
		return channel, nil
	default:
		return "", status.Errorf(codes.InvalidArgument, "unsupported release channel %q", channel)
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// UpdateNodeHardware updates the hardware specs for a node (detected at startup by Helmsman)
// Called by Foghorn when processing Register message with hardware info
func (s *QuartermasterServer) UpdateNodeHardware(ctx context.Context, req *quartermasterpb.UpdateNodeHardwareRequest) (*emptypb.Empty, error) {
	nodeID := req.GetNodeId()
	if nodeID == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id required")
	}

	// Update hardware specs and last_heartbeat timestamp
	rows, err := quartermasterdb.New(s.db).UpdateNodeHardwareRecord(ctx, quartermasterdb.UpdateNodeHardwareRecordParams{
		NodeID: nodeID, CpuCores: req.CpuCores, MemoryGB: req.MemoryGb, DiskGB: req.DiskGb,
	})
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"node_id": nodeID,
			"error":   err,
		}).Error("Failed to update node hardware specs")
		return nil, status.Errorf(codes.Internal, "failed to update hardware specs: %v", err)
	}

	if rows == 0 {
		// Node not found - this is OK, it might not be enrolled yet
		s.logger.WithField("node_id", nodeID).Debug("Node not found for hardware update (may not be enrolled yet)")
		return &emptypb.Empty{}, nil
	}

	s.logger.WithFields(logging.Fields{
		"node_id":   nodeID,
		"cpu_cores": req.GetCpuCores(),
		"memory_gb": req.GetMemoryGb(),
		"disk_gb":   req.GetDiskGb(),
	}).Debug("Updated node hardware specs")

	return &emptypb.Empty{}, nil
}

// edgeServiceTypeDerivations maps each edge services.type to the predicate that
// decides whether a reporting node advertises it. The four capability subtypes
// gate on their NodeAliveness flag; aggregate `edge` is unconditional: any
// healthy edge node is an `edge` member regardless of capability. The list is
// closed; new edge service types must land here and in pkg/dns/public_services.go
// in the same change.
var edgeServiceTypeDerivations = []struct {
	serviceType string
	cap         func(c *quartermasterpb.EdgeCapabilities) bool
}{
	{"edge", func(c *quartermasterpb.EdgeCapabilities) bool { return true }},
	{"edge-ingest", func(c *quartermasterpb.EdgeCapabilities) bool { return c.GetIngest() }},
	{"edge-egress", func(c *quartermasterpb.EdgeCapabilities) bool { return c.GetEgress() }},
	{"edge-storage", func(c *quartermasterpb.EdgeCapabilities) bool { return c.GetStorage() }},
	{"edge-processing", func(c *quartermasterpb.EdgeCapabilities) bool { return c.GetProcessing() }},
}

// dnsPairKey identifies the (cluster, service_type) tuple that scopes a
// Navigator.SyncDNS wakeup — for edge, pool-assigned, and physical services alike
// (cluster may be empty for a physical-only refresh).
type dnsPairKey struct {
	clusterID   string
	serviceType string
}

// nodeBefore captures the persisted node fields ReportAliveNodes diffs against
// the incoming payload.
type nodeBefore struct {
	clusterID, externalIP string
}

// instBefore captures the persisted service_instance state ReportAliveNodes
// diffs against per (node, edge-service_type).
type instBefore struct {
	clusterID, health string
	exists            bool
}

func (s *QuartermasterServer) ReportAliveNodes(ctx context.Context, req *quartermasterpb.ReportAliveNodesRequest) (*emptypb.Empty, error) {
	nodes := req.GetNodes()
	if len(nodes) == 0 {
		return &emptypb.Empty{}, nil
	}

	type capState struct {
		nodeID, clusterID, serviceType string
		healthy                        bool
	}
	type nodeUpdate struct {
		nodeID, clusterID, externalIP string
		isHealthy                     bool
		hasExternalIP                 bool
	}

	updates := make([]nodeUpdate, 0, len(nodes))
	caps := make([]capState, 0, len(nodes)*len(edgeServiceTypeDerivations))

	for _, n := range nodes {
		id := strings.TrimSpace(n.GetNodeId())
		if id == "" {
			continue
		}
		u := nodeUpdate{
			nodeID:    id,
			clusterID: strings.TrimSpace(n.GetClusterId()),
			isHealthy: n.GetIsHealthy(),
		}
		if raw := strings.TrimSpace(n.GetExternalIp()); raw != "" {
			if parsed := net.ParseIP(raw); parsed != nil {
				u.externalIP = parsed.String()
				u.hasExternalIP = true
			} else {
				s.logger.WithFields(logging.Fields{
					"node_id":     id,
					"external_ip": raw,
				}).Warn("Rejecting non-IP external_ip from Foghorn ReportAliveNodes payload")
			}
		}
		updates = append(updates, u)

		for _, mapping := range edgeServiceTypeDerivations {
			caps = append(caps, capState{
				nodeID:      id,
				clusterID:   u.clusterID,
				serviceType: mapping.serviceType,
				healthy:     u.isHealthy && mapping.cap(n.GetCapabilities()),
			})
		}
	}
	if len(updates) == 0 {
		return &emptypb.Empty{}, nil
	}

	// Ensure edge service rows exist BEFORE the main tx: ensureServiceExists
	// uses its own transaction with an advisory lock.
	for _, mapping := range edgeServiceTypeDerivations {
		if _, err := s.ensureServiceExists(ctx, mapping.serviceType, "http"); err != nil {
			return nil, err
		}
	}

	var priorNodes map[string]nodeBefore
	var priorInst map[string]instBefore
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer func() {
			if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
				s.logger.WithError(rbErr).Debug("ReportAliveNodes rollback")
			}
		}()

		// Pre-read prior node state (external_ip, cluster_id) so we can detect
		// IP/cluster deltas that need Navigator wakeups even when health doesn't flip.
		nodeIDs := make([]string, 0, len(updates))
		for _, u := range updates {
			nodeIDs = append(nodeIDs, u.nodeID)
		}
		txQueries := quartermasterdb.New(tx)
		nodeRows, err := txQueries.ListPriorNodeStates(ctx, nodeIDs)
		if err != nil {
			return fmt.Errorf("read prior node state: %w", err)
		}
		priorNodes = make(map[string]nodeBefore, len(nodeRows))
		for _, row := range nodeRows {
			priorNodes[row.NodeID] = nodeBefore{clusterID: row.ClusterID, externalIP: row.ExternalIP}
		}

		// Warn, but don't mutate, when Foghorn's view of cluster_id disagrees
		// with the persisted value. Cluster moves require FK-deferred row moves
		// of dependent service_instances (see api_tenants/internal/bootstrap/nodes.go).
		// ReportAliveNodes is the wrong authority for that; only operator paths
		// move clusters.
		for _, u := range updates {
			if u.clusterID == "" {
				continue
			}
			if prior, ok := priorNodes[u.nodeID]; ok && prior.clusterID != "" && prior.clusterID != u.clusterID {
				s.logger.WithFields(logging.Fields{
					"node_id":           u.nodeID,
					"reported_cluster":  u.clusterID,
					"persisted_cluster": prior.clusterID,
				}).Warn("Foghorn reported cluster_id differs from persisted; cluster moves must go through operator path, not heartbeat")
			}
		}

		// Pre-read prior service_instances state before writes. Yugabyte can
		// only query-layer-retry read restarts when no earlier statement in the
		// transaction has changed the snapshot; keeping both prior reads first
		// avoids turning a harmless retryable read into a failed transaction.
		siRows, err := txQueries.ListPriorEdgeInstanceStates(ctx, nodeIDs)
		if err != nil {
			return fmt.Errorf("failed to read prior edge service health: %w", err)
		}
		priorInst = make(map[string]instBefore, len(siRows))
		for _, row := range siRows {
			priorInst[row.NodeID+"|"+row.ServiceType] = instBefore{clusterID: row.ClusterID, health: row.HealthStatus, exists: true}
		}

		// Per-node heartbeat + external_ip. Heartbeat is refreshed only for
		// healthy nodes; edge DNS membership reads service_instances.health_status
		// set below, so last_heartbeat stays a mesh liveness signal.
		upNodeIDs := make([]string, 0, len(updates))
		upExternalIPs := make([]string, 0, len(updates))
		upRefreshHB := make([]bool, 0, len(updates))
		for _, u := range updates {
			upNodeIDs = append(upNodeIDs, u.nodeID)
			ip := ""
			if u.hasExternalIP {
				ip = u.externalIP
			}
			upExternalIPs = append(upExternalIPs, ip)
			upRefreshHB = append(upRefreshHB, u.isHealthy)
		}
		if execErr := txQueries.UpdateReportedNodes(ctx, quartermasterdb.UpdateReportedNodesParams{
			NodeIDs: upNodeIDs, ExternalIPs: upExternalIPs, RefreshHeartbeat: upRefreshHB,
		}); execErr != nil {
			return fmt.Errorf("failed to update node state: %w", execErr)
		}

		// UPSERT derived edge service rows. We only INSERT when the derivation is true:
		// don't materialise rows for caps a node has never advertised.
		// For caps that flip false on an existing row we UPDATE to 'unhealthy'.
		for _, c := range caps {
			key := c.nodeID + "|" + c.serviceType
			_, hadRow := priorInst[key]
			switch {
			case c.healthy:
				// Operator status is authoritative over capability flags: a node in
				// maintenance/offline/retired/evicted must not be re-enabled by a
				// stale Foghorn heartbeat. The INSERT...SELECT only emits a row when
				// the persisted node row is active, so the same upsert can revive an
				// edge capability after the operator marks the node active again.
				instanceID := fmt.Sprintf("edge-cap-%s-%s", c.nodeID, c.serviceType)
				if execErr := txQueries.UpsertHealthyEdgeInstance(ctx, quartermasterdb.UpsertHealthyEdgeInstanceParams{
					InstanceID: instanceID, NodeID: c.nodeID, ServiceType: c.serviceType,
				}); execErr != nil {
					return fmt.Errorf("upsert healthy edge instance: %w", execErr)
				}
			case !c.healthy && hadRow:
				if execErr := txQueries.MarkEdgeInstanceUnhealthy(ctx, quartermasterdb.MarkEdgeInstanceUnhealthyParams{
					ServiceType: c.serviceType, NodeID: c.nodeID,
				}); execErr != nil {
					return fmt.Errorf("mark edge instance unhealthy: %w", execErr)
				}
			}
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "report alive nodes: %v", err)
	}

	// Compute dirty (cluster, type) pairs across two change axes:
	//   - health_status flip on an edge service instance
	//   - external_ip change on the underlying node (A record value changes)
	//
	// Cluster moves are not mutated here, so the dirty cluster is always the
	// persisted cluster from prior row state or the node row.
	dirty := map[dnsPairKey]struct{}{}
	for _, c := range caps {
		prior, hadRow := priorInst[c.nodeID+"|"+c.serviceType]
		newHealth := "unhealthy"
		if c.healthy {
			newHealth = "healthy"
		}
		clusterForDirty := ""
		if hadRow {
			clusterForDirty = prior.clusterID
		} else if pn, ok := priorNodes[c.nodeID]; ok {
			clusterForDirty = pn.clusterID
		}
		if clusterForDirty == "" {
			continue
		}
		switch {
		case !hadRow && c.healthy:
			dirty[dnsPairKey{clusterForDirty, c.serviceType}] = struct{}{}
		case hadRow && prior.health != newHealth:
			dirty[dnsPairKey{clusterForDirty, c.serviceType}] = struct{}{}
		}
	}
	// IP deltas at the node level: wake every edge service pair for the persisted cluster.
	for _, u := range updates {
		prior, ok := priorNodes[u.nodeID]
		if !ok || prior.clusterID == "" {
			continue
		}
		if !u.hasExternalIP || prior.externalIP == u.externalIP {
			continue
		}
		for _, mapping := range edgeServiceTypeDerivations {
			dirty[dnsPairKey{prior.clusterID, mapping.serviceType}] = struct{}{}
		}
		// A node IP change moves the A record value of every pool-assigned service it
		// hosts: the node-keyed physical record (<service>.<node>.infra.<root>) and the
		// pooled record of each SERVED media cluster (livepeer.<cluster>, …). These are
		// node-keyed/SCA-keyed, not edge caps, so the loops above never wake them — wake
		// by served cluster (physical-only fallback) per pool type.
		for _, poolType := range dns.PoolAssignedServiceTypes() {
			s.fireNavigatorSyncForPoolClusters(poolType, s.servedClustersForNodeType(ctx, u.nodeID, poolType))
		}
	}
	if len(dirty) > 0 {
		s.fireNavigatorSyncForPairs(dirty)
	}

	return &emptypb.Empty{}, nil
}

// fireNavigatorSyncForPairs dispatches Navigator.SyncDNS for each dirty
// (cluster, service_type) pair. Each pair gets an independent context so a slow
// Bunny/Cloudflare write cannot cancel the rest of the batch.
func (s *QuartermasterServer) fireNavigatorSyncForPairs(pairs map[dnsPairKey]struct{}) {
	if s.navigatorClient == nil || len(pairs) == 0 {
		return
	}
	ordered := make([]dnsPairKey, 0, len(pairs))
	for p := range pairs {
		ordered = append(ordered, p)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].clusterID == ordered[j].clusterID {
			return ordered[i].serviceType < ordered[j].serviceType
		}
		return ordered[i].clusterID < ordered[j].clusterID
	})

	go func(pairs []dnsPairKey) {
		sem := make(chan struct{}, navigatorDNSSyncConcurrency)
		var wg sync.WaitGroup
		for _, p := range pairs {
			p := p
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()

				ctx, cancel := context.WithTimeout(context.Background(), navigatorDNSSyncTimeout)
				defer cancel()

				clusterID := p.clusterID
				req := &dnspb.SyncDNSRequest{
					ServiceType: p.serviceType,
					ClusterId:   &clusterID,
				}
				started := time.Now()
				resp, err := s.navigatorClient.SyncDNS(ctx, req)
				if err != nil {
					s.logger.WithError(err).WithFields(logging.Fields{
						"cluster_id":   p.clusterID,
						"service_type": p.serviceType,
					}).Warn("Navigator SyncDNS notify failed; 60s repair loop will converge")
					return
				}
				if !resp.GetSuccess() {
					s.logger.WithFields(logging.Fields{
						"cluster_id":   p.clusterID,
						"service_type": p.serviceType,
						"message":      resp.GetMessage(),
						"errors":       resp.GetErrors(),
					}).Warn("Navigator SyncDNS notify completed with errors; 60s repair loop will converge")
					return
				}
				s.logger.WithFields(logging.Fields{
					"cluster_id":   p.clusterID,
					"service_type": p.serviceType,
					"duration_ms":  time.Since(started).Milliseconds(),
				}).Debug("Navigator SyncDNS notify completed")
			}()
		}
		wg.Wait()
	}(ordered)
}

// fireNavigatorSyncForPoolClusters wakes Navigator for a pool-assigned/physical
// service across the SERVED media clusters — pooled DNS (livepeer.<media-cluster>,
// …) is keyed by service_cluster_assignments.cluster_id, not the instance's physical
// host cluster, so waking by served cluster is what keeps pooled DNS event-driven.
// Each cluster-scoped SyncDNS also refreshes the node-keyed physical records; with
// no served clusters (e.g. an unassigned gateway) it falls back to a physical-only
// refresh so the infra records still update. No-op for non-pool/physical types.
func (s *QuartermasterServer) fireNavigatorSyncForPoolClusters(serviceType string, clusters []string) {
	if !dns.IsPoolAssignedServiceType(serviceType) && !dns.IsPhysicalEndpointServiceType(serviceType) {
		return
	}
	// serviceType is the INSTANCE type (svc.type); Navigator.SyncDNS keys its pooled
	// record by the DNS-facing name (vmauth -> telemetry, others identity).
	wakeType := dns.PoolDNSWakeServiceType(serviceType)
	pairs := map[dnsPairKey]struct{}{}
	for _, c := range clusters {
		if strings.TrimSpace(c) != "" {
			pairs[dnsPairKey{c, wakeType}] = struct{}{}
		}
	}
	if len(pairs) == 0 {
		if !dns.IsPhysicalEndpointServiceType(serviceType) {
			return
		}
		pairs[dnsPairKey{"", wakeType}] = struct{}{} // physical-only
	}
	s.fireNavigatorSyncForPairs(pairs)
}

// servedClusters runs a SCA-resolution query for a read-side wake (node status,
// register, health poll). It is best-effort — the reconcile loop is the backstop —
// but it must not SILENTLY drop a partial read: any query/scan/iteration error is
// logged at Warn so a recurring resolution failure is visible, not invisible. The
// mutation paths that change membership (AddToServicePool, DrainServiceInstance) do
// NOT use this; they capture the affected clusters atomically via DELETE ...
// RETURNING so a failed read can never commit a mutation without a wake.
func (s *QuartermasterServer) servedClusters(ctx context.Context, query func(context.Context) ([]string, error)) []string {
	rows, err := query(ctx)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to resolve served clusters for DNS wake; reconcile loop will converge")
		return nil
	}
	out := make([]string, 0, len(rows))
	for _, clusterID := range rows {
		if strings.TrimSpace(clusterID) != "" {
			out = append(out, clusterID)
		}
	}
	return out
}

// scanDeletedClusters drains a DELETE ... RETURNING cluster_id result into the
// distinct non-empty clusters plus the total deleted row count.
func (s *QuartermasterServer) servedClustersForInstanceName(ctx context.Context, instanceName, serviceType string) []string {
	queries := quartermasterdb.New(s.db)
	return s.servedClusters(ctx, func(ctx context.Context) ([]string, error) {
		return queries.ListServedClustersForInstance(ctx, quartermasterdb.ListServedClustersForInstanceParams{InstanceID: instanceName, ServiceType: serviceType})
	})
}

func (s *QuartermasterServer) servedClustersForNodeType(ctx context.Context, nodeID, serviceType string) []string {
	queries := quartermasterdb.New(s.db)
	return s.servedClusters(ctx, func(ctx context.Context) ([]string, error) {
		return queries.ListServedClustersForNode(ctx, quartermasterdb.ServedClustersForNodeParams{Identity: nodeID, ServiceType: serviceType})
	})
}

func resourceSnapshotComplete(snap *quartermasterpb.NodeResourceSnapshot) bool {
	return snap != nil && snap.GetRamTotalBytes() > 0 && snap.GetDiskTotalBytes() > 0 && snap.GetUptimeSeconds() > 0
}

func nodeSnapshotProto(cpu sql.NullFloat64, ramUsed, ramTotal, diskUsed, diskTotal, uptime sql.NullInt64, at sql.NullTime) *quartermasterpb.NodeResourceSnapshot {
	if !at.Valid {
		return nil
	}
	snapshot := &quartermasterpb.NodeResourceSnapshot{CollectedAt: timestamppb.New(at.Time)}
	if cpu.Valid {
		snapshot.CpuPercent = float32(cpu.Float64)
	}
	if ramUsed.Valid {
		snapshot.RamUsedBytes = uint64(ramUsed.Int64)
	}
	if ramTotal.Valid {
		snapshot.RamTotalBytes = uint64(ramTotal.Int64)
	}
	if diskUsed.Valid {
		snapshot.DiskUsedBytes = uint64(diskUsed.Int64)
	}
	if diskTotal.Valid {
		snapshot.DiskTotalBytes = uint64(diskTotal.Int64)
	}
	if uptime.Valid {
		snapshot.UptimeSeconds = uint64(uptime.Int64)
	}
	return snapshot
}

func (s *QuartermasterServer) queryNode(ctx context.Context, nodeID string) (*quartermasterpb.InfrastructureNode, error) {
	row, err := quartermasterdb.New(s.db).GetInfrastructureNode(ctx, nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "node not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	return nodeFromRecord(row), nil
}

func nodeFromRecord(row quartermasterdb.GetInfrastructureNodeRow) *quartermasterpb.InfrastructureNode {
	node := &quartermasterpb.InfrastructureNode{
		Id: row.ID, NodeId: row.NodeID, ClusterId: row.ClusterID, NodeName: row.NodeName, NodeType: row.NodeType,
		EnrollmentOrigin: row.EnrollmentOrigin,
		ResourceSnapshot: nodeSnapshotProto(row.SnapshotCpuPercent, row.SnapshotRamUsedBytes, row.SnapshotRamTotalBytes,
			row.SnapshotDiskUsedBytes, row.SnapshotDiskTotalBytes, row.SnapshotUptimeSeconds, row.SnapshotAt),
	}
	if row.InternalIp.Valid {
		node.InternalIp = &row.InternalIp.String
	}
	if row.ExternalIp.Valid {
		node.ExternalIp = &row.ExternalIp.String
	}
	if row.WireguardIp.Valid {
		node.WireguardIp = &row.WireguardIp.String
	}
	if row.WireguardPublicKey.Valid {
		node.WireguardPublicKey = &row.WireguardPublicKey.String
	}
	if row.WireguardListenPort.Valid {
		node.WireguardPort = &row.WireguardListenPort.Int32
	}
	if row.Region.Valid {
		node.Region = &row.Region.String
	}
	if row.AvailabilityZone.Valid {
		node.AvailabilityZone = &row.AvailabilityZone.String
	}
	if row.Latitude.Valid {
		node.Latitude = &row.Latitude.Float64
	}
	if row.Longitude.Valid {
		node.Longitude = &row.Longitude.Float64
	}
	if row.CpuCores.Valid {
		node.CpuCores = &row.CpuCores.Int32
	}
	if row.MemoryGb.Valid {
		node.MemoryGb = &row.MemoryGb.Int32
	}
	if row.DiskGb.Valid {
		node.DiskGb = &row.DiskGb.Int32
	}
	if row.LastHeartbeat.Valid {
		node.LastHeartbeat = timestamppb.New(row.LastHeartbeat.Time)
	}
	if row.AppliedMeshRevision.Valid {
		node.AppliedMeshRevision = &row.AppliedMeshRevision.String
	}
	if row.Status.Valid {
		node.Status = row.Status.String
	}
	if row.CreatedAt.Valid {
		node.CreatedAt = timestamppb.New(row.CreatedAt.Time)
	}
	if row.UpdatedAt.Valid {
		node.UpdatedAt = timestamppb.New(row.UpdatedAt.Time)
	}
	if row.OwnerTenantID != "" {
		node.OwnerTenantId = &row.OwnerTenantID
	}
	return node
}

func clusterFromListRow(row quartermasterdb.ClusterListRow) *quartermasterpb.InfrastructureCluster {
	cluster := &quartermasterpb.InfrastructureCluster{
		Id: row.ID, ClusterId: row.ClusterID, ClusterName: row.ClusterName, ClusterType: row.ClusterType,
		DeploymentModel: row.DeploymentModel, BaseUrl: row.BaseURL, KafkaBrokers: row.KafkaBrokers,
		MaxConcurrentStreams: row.MaxConcurrentStreams, MaxConcurrentViewers: row.MaxConcurrentViewers,
		MaxBandwidthMbps: row.MaxBandwidthMbps, HealthStatus: row.HealthStatus, IsActive: row.IsActive,
		IsDefaultCluster: row.IsDefaultCluster, IsPlatformOfficial: row.IsPlatformOfficial, PublicTopology: row.PublicTopology,
		AllowPrivatePullSources: row.AllowPrivatePullSources, CreatedAt: timestamppb.New(row.CreatedAt), UpdatedAt: timestamppb.New(row.UpdatedAt),
	}
	if row.OwnerTenantID.Valid {
		cluster.OwnerTenantId = &row.OwnerTenantID.String
	}
	if row.DatabaseURL.Valid {
		cluster.DatabaseUrl = &row.DatabaseURL.String
	}
	if row.PeriscopeURL.Valid {
		cluster.PeriscopeUrl = &row.PeriscopeURL.String
	}
	return cluster
}

// ListNodes returns nodes with optional filters
func (s *QuartermasterServer) ListNodes(ctx context.Context, req *quartermasterpb.ListNodesRequest) (*quartermasterpb.ListNodesResponse, error) {
	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	tenantID := middleware.GetTenantID(ctx)

	scope := quartermasterdb.NodeScopePublic
	if tenantID != "" {
		scope = quartermasterdb.NodeScopeTenant
	} else if ctxkeys.GetAuthType(ctx) == "service" {
		scope = quartermasterdb.NodeScopeService
	}
	filter := quartermasterdb.NodeListFilter{Scope: scope, TenantID: tenantID, ClusterID: req.GetClusterId(), NodeType: req.GetNodeType(), Region: req.GetRegion(), Backward: params.Direction == pagination.Backward, Limit: params.Limit + 1}
	if params.Cursor != nil {
		filter.CursorTime, filter.CursorID = &params.Cursor.Timestamp, params.Cursor.ID
	}
	var rows []quartermasterdb.GetInfrastructureNodeRow
	var total int32
	if queryErr := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var err error
		rows, total, err = quartermasterdb.New(s.db).ListInfrastructureNodesPage(ctx, filter)
		return err
	}); queryErr != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", queryErr)
	}
	nodes := make([]*quartermasterpb.InfrastructureNode, 0, len(rows))
	for _, row := range rows {
		nodes = append(nodes, nodeFromRecord(row))
	}
	resultsLen := len(nodes)
	hasMore := resultsLen > params.Limit
	if hasMore {
		nodes = nodes[:params.Limit]
	}
	if params.Direction == pagination.Backward {
		slices.Reverse(nodes)
	}

	// Build cursors from results
	var startCursor, endCursor string
	if len(nodes) > 0 {
		first := nodes[0]
		last := nodes[len(nodes)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.Id)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.Id)
	}

	// Build response with proper hasNextPage/hasPreviousPage
	resp := &quartermasterpb.ListNodesResponse{
		Nodes:     nodes,
		ClusterId: req.GetClusterId(),
		NodeType:  req.GetNodeType(),
		Region:    req.GetRegion(),
		Pagination: &commonpb.CursorPaginationResponse{
			TotalCount: total,
		},
	}
	if startCursor != "" {
		resp.Pagination.StartCursor = &startCursor
	}
	if endCursor != "" {
		resp.Pagination.EndCursor = &endCursor
	}
	if params.Direction == pagination.Forward {
		resp.Pagination.HasNextPage = hasMore
		resp.Pagination.HasPreviousPage = params.Cursor != nil
	} else {
		resp.Pagination.HasPreviousPage = hasMore
		resp.Pagination.HasNextPage = params.Cursor != nil
	}

	return resp, nil
}

// ListHealthyNodesForDNS returns infrastructure nodes eligible for DNS records.
//
// Most service types resolve through service_instances: a node is healthy when
// it has a matching row with health_status='healthy' and a recent
// last_health_check. Edge services use the same path; Foghorn's ReportAliveNodes
// owns those rows. Pool-assigned media services resolve their logical cluster
// through service_cluster_assignments.
//
// All paths require: accessible cluster, non-empty external_ip.
func (s *QuartermasterServer) ListHealthyNodesForDNS(ctx context.Context, req *quartermasterpb.ListHealthyNodesForDNSRequest) (*quartermasterpb.ListHealthyNodesForDNSResponse, error) {
	tenantID := middleware.GetTenantID(ctx)
	scope := quartermasterdb.NodeScopePublic
	if tenantID != "" {
		scope = quartermasterdb.NodeScopeTenant
	} else if ctxkeys.GetAuthType(ctx) == "service" {
		scope = quartermasterdb.NodeScopeService
	}

	staleThreshold := req.GetStaleThresholdSeconds()
	if staleThreshold <= 0 {
		staleThreshold = 300
	}

	serviceTypeFilter := req.GetServiceType()
	serviceLookupType := serviceTypeFilter
	if serviceTypeFilter == "telemetry" {
		// telemetry.<cluster> is the public remote-write ingress name; the
		// backing service registered in Quartermaster is vmauth.
		serviceLookupType = "vmauth"
	}

	// Aggregate `edge` and the edge-* capability subtypes both resolve through
	// the standard service_instances path: Foghorn's ReportAliveNodes writes a
	// durable health row per (node, edge service type), so DNS membership and
	// targeted Navigator wakeups react in seconds. The edge node is the media
	// cluster physically, so si.cluster_id == the logical media cluster.
	// Pool-style media services (foghorn, chandler, livepeer-gateway) resolve
	// their logical media-cluster identity via service_cluster_assignments.
	// Public telemetry DNS is backed by vmauth instances, but it has the same
	// logical-cluster shape: vmauth runs on observability hosts and receives one
	// assignment per media cluster it serves.
	// The physical service_instances row stays bound to the host cluster, so
	// reads must follow the assignment table to surface the right cluster_id.
	if dns.IsPoolAssignedServiceType(serviceLookupType) || serviceTypeFilter == "telemetry" {
		return s.listHealthyNodes(ctx, scope, tenantID, strings.TrimSpace(req.GetClusterId()), serviceLookupType, staleThreshold, true)
	}
	return s.listHealthyNodes(ctx, scope, tenantID, strings.TrimSpace(req.GetClusterId()), serviceLookupType, staleThreshold, false)
}

// provisionedPhysicalEndpointFQDNs returns the set of physical-endpoint FQDNs that
// have a desired physical ingress site (kind='physical') on an ACTIVE node.
// DiscoverServices gates public_instance_host advertisement on this so a consumer
// (Foghorn) is not handed a per-instance hostname before the node is provisioned
// for it, nor for a node the operator has taken non-active (whose infra A record
// Navigator prunes — the node-active predicate mirrors ListServiceInstancesByType).
// It is a DESIRED-state signal: it does not prove DNS is published or that the real
// TLS bundle has been synced (Navigator's reconcile and Privateer's cert sync still
// lag). Match is by exact FQDN, so it stays correct if other services gain physical
// endpoints.
func (s *QuartermasterServer) provisionedPhysicalEndpointFQDNs(ctx context.Context) (map[string]struct{}, error) {
	// Require the node be active, matching Navigator's physical inventory contract
	// (ListServiceInstancesByType gates on n.status='active'). Without this an
	// operator-offlined node would still hand Foghorn a public_instance_host whose
	// infra A record Navigator has already pruned — a non-routable broadcaster.
	// Health/freshness stays the consumer's filter; node status is the operator-
	// controlled, stable gate.
	var rows [][]byte
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		rows, queryErr = quartermasterdb.New(s.db).ListProvisionedPhysicalIngressDomains(ctx)
		return queryErr
	})
	if err != nil {
		return nil, err
	}
	out := map[string]struct{}{}
	for _, domainsJSON := range rows {
		domains, err := decodeIngressDomainsStrict(domainsJSON)
		if err != nil {
			return nil, fmt.Errorf("decode physical ingress domains: %w", err)
		}
		for _, d := range domains {
			out[strings.ToLower(strings.TrimSpace(d))] = struct{}{}
		}
	}
	return out, nil
}

// synthesizePublicHost builds the per-assignment FQDN for a pool-assigned
// service from DB cluster metadata. The same physical instance assigned to
// multiple media clusters returns a different public_host per requested
// cluster — so this is computed at lookup time rather than stored as static
// service_instances metadata.
func synthesizePublicHost(serviceType, clusterID, clusterName, baseURL string) string {
	root := dns.NormalizeDomainScope(baseURL)
	if root == "" {
		return ""
	}
	slug := dns.ClusterSlug(clusterID, clusterName)
	if slug == "" {
		return ""
	}
	scope := slug + "." + root
	fqdn, ok := dns.ServiceFQDN(serviceType, scope)
	if !ok {
		return ""
	}
	return fqdn
}

func (s *QuartermasterServer) listHealthyNodes(ctx context.Context, scope quartermasterdb.NodeListScope, tenantID, clusterID, serviceType string, staleThreshold int32, assigned bool) (*quartermasterpb.ListHealthyNodesForDNSResponse, error) {
	var rows []quartermasterdb.GetInfrastructureNodeRow
	var totalNodes, healthyNodes int32
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		rows, totalNodes, healthyNodes, queryErr = quartermasterdb.New(s.db).ListHealthyServiceNodes(ctx, quartermasterdb.HealthyNodeFilter{
			Scope: scope, TenantID: tenantID, ClusterID: clusterID, ServiceType: serviceType, StaleThreshold: staleThreshold, Assigned: assigned,
		})
		return queryErr
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	nodes := make([]*quartermasterpb.InfrastructureNode, 0, len(rows))
	for _, row := range rows {
		nodes = append(nodes, nodeFromRecord(row))
	}
	return &quartermasterpb.ListHealthyNodesForDNSResponse{Nodes: nodes, TotalNodes: totalNodes, HealthyNodes: healthyNodes}, nil
}

// CreateNode creates a new node
func (s *QuartermasterServer) CreateNode(ctx context.Context, req *quartermasterpb.CreateNodeRequest) (*quartermasterpb.NodeResponse, error) {
	nodeID := req.GetNodeId()
	clusterID := req.GetClusterId()
	if nodeID == "" || clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id and cluster_id required")
	}

	// Verify cluster exists
	clusterExists, err := quartermasterdb.New(s.db).InfrastructureClusterExists(ctx, clusterID)
	if err != nil {
		s.logger.WithError(err).Error("Failed to check cluster existence")
		return nil, status.Errorf(codes.Internal, "failed to validate cluster: %v", err)
	}
	if !clusterExists {
		return nil, status.Error(codes.InvalidArgument, "cluster not found")
	}

	now := time.Now()

	var wgPort any
	if req.WireguardPort != nil && *req.WireguardPort > 0 {
		wgPort = *req.WireguardPort
	}

	lat, lng := s.geoForExternalIP(req.ExternalIp)
	err = quartermasterdb.New(s.db).UpsertInfrastructureNode(ctx, quartermasterdb.UpsertInfrastructureNodeParams{
		NodeID: nodeID, ClusterID: clusterID, NodeName: req.GetNodeName(), NodeType: req.GetNodeType(),
		InternalIP: req.InternalIp, ExternalIP: req.ExternalIp, WireguardIP: req.WireguardIp,
		WireguardPublicKey: req.WireguardPublicKey, WireguardListenPort: wgPort,
		Region: req.Region, AvailabilityZone: req.AvailabilityZone, Latitude: lat, Longitude: lng,
		CPUCores: req.CpuCores, MemoryGB: req.MemoryGb, DiskGB: req.DiskGb, Now: now,
	})

	if err != nil {
		s.logger.WithError(err).WithField("node_id", nodeID).Error("Failed to upsert node")
		return nil, status.Errorf(codes.Internal, "failed to upsert node: %v", err)
	}

	node, err := s.queryNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}

	// DNS sync is handled by Navigator's periodic reconciler. Triggering here
	// would be premature: no services are deployed on a freshly-created node,
	// and node_type (e.g. "core") is not a valid service type for DNS lookup.

	return &quartermasterpb.NodeResponse{Node: node}, nil
}

func (s *QuartermasterServer) geoForExternalIP(externalIP *string) (any, any) {
	if externalIP == nil || *externalIP == "" || s.geoipReader == nil {
		return nil, nil
	}

	geo := s.geoipReader.Lookup(*externalIP)
	if geo == nil {
		return nil, nil
	}
	geobucket.BucketGeoData(geo)
	return geo.Latitude, geo.Longitude
}

type foghornControlCellCandidate struct {
	instanceID    string
	controlCellID string
	regionID      string
	baseURL       string
	load          int
	latitude      sql.NullFloat64
	longitude     sql.NullFloat64
	startedAt     sql.NullTime
}

func (s *QuartermasterServer) geoCoordinatesForIP(ip string) (float64, float64, bool) {
	if strings.TrimSpace(ip) == "" || s.geoipReader == nil {
		return 0, 0, false
	}
	geo := s.geoipReader.Lookup(ip)
	if geo == nil || !geoip.IsValidLatLon(geo.Latitude, geo.Longitude) {
		return 0, 0, false
	}
	geobucket.BucketGeoData(geo)
	return geo.Latitude, geo.Longitude, true
}

func (s *QuartermasterServer) selectFoghornControlCell(ctx context.Context, explicitControlClusterID, requestedRegion, clientIP string) (foghornControlCellCandidate, error) {
	explicitControlClusterID = strings.TrimSpace(explicitControlClusterID)
	requestedRegion = strings.TrimSpace(requestedRegion)
	clientLat, clientLon, hasClientGeo := s.geoCoordinatesForIP(clientIP)

	rows, err := quartermasterdb.New(s.db).ListFoghornControlCells(ctx, explicitControlClusterID, requestedRegion, !hasClientGeo)
	if err != nil {
		return foghornControlCellCandidate{}, status.Errorf(codes.Internal, "failed to find Foghorn control cell: %v", err)
	}
	var candidates []foghornControlCellCandidate
	for _, row := range rows {
		candidates = append(candidates, foghornControlCellCandidate{instanceID: row.InstanceID, controlCellID: row.ControlCellID,
			regionID: row.RegionID, baseURL: row.BaseURL, load: row.Load, latitude: row.Latitude, longitude: row.Longitude, startedAt: row.StartedAt})
	}
	if len(candidates) == 0 {
		if explicitControlClusterID != "" {
			return foghornControlCellCandidate{}, status.Errorf(codes.Unavailable, "no healthy Foghorn in control cluster %q", explicitControlClusterID)
		}
		if requestedRegion != "" && !hasClientGeo {
			return foghornControlCellCandidate{}, status.Errorf(codes.Unavailable, "no healthy Foghorn in region %q", requestedRegion)
		}
		return foghornControlCellCandidate{}, status.Error(codes.Unavailable, "no healthy platform-official Foghorn control cells available")
	}

	return pickFoghornControlCellCandidate(candidates, clientLat, clientLon, hasClientGeo), nil
}

func pickFoghornControlCellCandidate(candidates []foghornControlCellCandidate, clientLat, clientLon float64, hasClientGeo bool) foghornControlCellCandidate {
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if hasClientGeo {
			ad := geoDistanceKm(clientLat, clientLon, a.latitude, a.longitude)
			bd := geoDistanceKm(clientLat, clientLon, b.latitude, b.longitude)
			if ad != bd {
				return ad < bd
			}
		}
		if a.load != b.load {
			return a.load < b.load
		}
		if a.startedAt.Valid != b.startedAt.Valid {
			return a.startedAt.Valid
		}
		if a.startedAt.Valid && !a.startedAt.Time.Equal(b.startedAt.Time) {
			return a.startedAt.Time.Before(b.startedAt.Time)
		}
		if a.controlCellID != b.controlCellID {
			return a.controlCellID < b.controlCellID
		}
		return a.instanceID < b.instanceID
	})
	return candidates[0]
}

func geoDistanceKm(lat, lon float64, candidateLat, candidateLon sql.NullFloat64) float64 {
	if !candidateLat.Valid || !candidateLon.Valid || !geoip.IsValidLatLon(candidateLat.Float64, candidateLon.Float64) {
		return math.Inf(1)
	}
	const earthRadiusKm = 6371.0
	lat1 := lat * math.Pi / 180
	lat2 := candidateLat.Float64 * math.Pi / 180
	dLat := (candidateLat.Float64 - lat) * math.Pi / 180
	dLon := (candidateLon.Float64 - lon) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1)*math.Cos(lat2)*math.Sin(dLon/2)*math.Sin(dLon/2)
	if a > 1 {
		a = 1
	}
	return 2 * earthRadiusKm * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// ResolveNodeFingerprint resolves a node identity from fingerprint data.
// Lookup priority:
// 1. Exact match by machine_id_sha256
// 2. Match by macs_sha256
// 3. Match by peer_ip in seen_ips array
// On match, updates seen_ips with current peer_ip.
// Returns NotFound if no match - does not create new mappings to avoid bypassing enrollment.
func (s *QuartermasterServer) ResolveNodeFingerprint(ctx context.Context, req *quartermasterpb.ResolveNodeFingerprintRequest) (*quartermasterpb.ResolveNodeFingerprintResponse, error) {
	peerIP := req.GetPeerIp()
	if peerIP == "" {
		return nil, status.Error(codes.InvalidArgument, "peer_ip required")
	}

	queries := quartermasterdb.New(s.db)
	var resolved quartermasterdb.NodeFingerprintRow

	// 1) Try exact match by machine_id_sha256
	machineIDSHA := req.GetMachineIdSha256()
	if machineIDSHA != "" {
		var err error
		resolved, err = queries.ResolveNodeFingerprint(ctx, quartermasterdb.NodeFingerprintByMachineID, machineIDSHA)
		if err == nil {
			if upsertErr := s.upsertSeenIP(ctx, resolved.NodeID, peerIP); upsertErr != nil {
				s.logger.WithError(upsertErr).WithField("node_id", resolved.NodeID).Warn("Failed to update fingerprint seen IP")
			}
			return &quartermasterpb.ResolveNodeFingerprintResponse{
				TenantId:        resolved.TenantID,
				CanonicalNodeId: resolved.NodeID,
			}, nil
		}
	}

	// 2) Match by macs_sha256
	macsSHA := req.GetMacsSha256()
	if macsSHA != "" {
		var err error
		resolved, err = queries.ResolveNodeFingerprint(ctx, quartermasterdb.NodeFingerprintByMACs, macsSHA)
		if err == nil {
			if upsertErr := s.upsertSeenIP(ctx, resolved.NodeID, peerIP); upsertErr != nil {
				s.logger.WithError(upsertErr).WithField("node_id", resolved.NodeID).Warn("Failed to update fingerprint seen IP")
			}
			return &quartermasterpb.ResolveNodeFingerprintResponse{
				TenantId:        resolved.TenantID,
				CanonicalNodeId: resolved.NodeID,
			}, nil
		}
	}

	// 3) Match by peer_ip in seen_ips array
	resolved, err := queries.ResolveNodeFingerprint(ctx, quartermasterdb.NodeFingerprintBySeenIP, peerIP)
	if err == nil {
		if upsertErr := s.upsertSeenIP(ctx, resolved.NodeID, peerIP); upsertErr != nil {
			s.logger.WithError(upsertErr).WithField("node_id", resolved.NodeID).Warn("Failed to update fingerprint seen IP")
		}
		return &quartermasterpb.ResolveNodeFingerprintResponse{
			TenantId:        resolved.TenantID,
			CanonicalNodeId: resolved.NodeID,
		}, nil
	}

	// No match: do not create mappings here to avoid bypassing enrollment.
	// Fingerprint mappings must be provisioned/admin-created.
	return nil, status.Error(codes.NotFound, "fingerprint not recognized")
}

// upsertSeenIP updates the node_fingerprints with the current peer IP if not already present
func (s *QuartermasterServer) upsertSeenIP(ctx context.Context, nodeID, peerIP string) error {
	if peerIP == "" {
		return nil
	}
	return quartermasterdb.New(s.db).UpsertFingerprintSeenIP(ctx, nodeID, peerIP)
}

func extractClientIP(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(p.Addr.String())
	if err != nil {
		return p.Addr.String()
	}
	return host
}

func validateExpectedIP(expectedIP sql.NullString, clientIP string) bool {
	if !expectedIP.Valid || expectedIP.String == "" {
		return true
	}
	clientAddr := net.ParseIP(clientIP)
	if clientAddr == nil {
		return false
	}
	if strings.Contains(expectedIP.String, "/") {
		_, network, err := net.ParseCIDR(expectedIP.String)
		if err != nil {
			return false
		}
		return network.Contains(clientAddr)
	}
	expectedAddr := net.ParseIP(expectedIP.String)
	return expectedAddr != nil && expectedAddr.Equal(clientAddr)
}

var edgeNodeIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,99}$`)

func deriveEdgeNodeID(hostname string) string {
	hostname = strings.ToLower(strings.TrimSpace(hostname))
	if hostname == "" {
		return ""
	}
	if idx := strings.Index(hostname, "."); idx > 0 {
		hostname = hostname[:idx]
	}
	if !edgeNodeIDPattern.MatchString(hostname) {
		return ""
	}
	return hostname
}

// ============================================================================
// BOOTSTRAP SERVICE - Additional Methods
// ============================================================================

// BootstrapEdgeNode registers an edge node using a bootstrap token
func (s *QuartermasterServer) BootstrapEdgeNode(ctx context.Context, req *quartermasterpb.BootstrapEdgeNodeRequest) (*quartermasterpb.BootstrapEdgeNodeResponse, error) {
	var resp *quartermasterpb.BootstrapEdgeNodeResponse
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var err error
		resp, err = s.bootstrapEdgeNodeOnce(ctx, req)
		return err
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (s *QuartermasterServer) bootstrapEdgeNodeOnce(ctx context.Context, req *quartermasterpb.BootstrapEdgeNodeRequest) (*quartermasterpb.BootstrapEdgeNodeResponse, error) {
	token := req.GetToken()
	if token == "" {
		return nil, status.Error(codes.InvalidArgument, "token required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to begin transaction: %v", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			s.logger.WithError(rollbackErr).Debug("transaction rollback failed")
		}
	}()

	// Validate token - check for single-use (used_at IS NULL) OR multi-use (usage_count < usage_limit)
	txQueries := quartermasterdb.New(tx)
	tokenRow, err := txQueries.LockEdgeEnrollmentToken(ctx, hashBootstrapToken(token))

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.Unauthenticated, "invalid or already used token")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Check expiration
	if time.Now().After(tokenRow.ExpiresAt) {
		return nil, status.Error(codes.Unauthenticated, "token expired")
	}

	clientIP := extractClientIP(ctx)
	if !validateExpectedIP(tokenRow.ExpectedIP, clientIP) {
		return nil, status.Error(codes.PermissionDenied, "client IP does not match token expected_ip")
	}

	// Validate tenant ID is present for edge_node tokens
	if !tokenRow.TenantID.Valid || tokenRow.TenantID.String == "" {
		return nil, status.Error(codes.InvalidArgument, "token missing tenant_id")
	}

	// Cluster enforcement: if token has a cluster_id binding, validate against caller's served set
	targetClusterID := req.GetTargetClusterId()
	tokenClusterID := tokenRow.ClusterID.String
	servedClusters := req.GetServedClusterIds()

	if tokenClusterID != "" && len(servedClusters) > 0 {
		if !slices.Contains(servedClusters, tokenClusterID) {
			return nil, status.Errorf(codes.PermissionDenied,
				"token is bound to cluster %s, not served by this instance", tokenClusterID)
		}
	}

	// Cluster resolution priority: token binding > request target > fallback
	resolvedClusterID := tokenClusterID
	if resolvedClusterID == "" {
		resolvedClusterID = targetClusterID
	}
	if resolvedClusterID == "" {
		// Fallback: pick any active cluster
		resolvedClusterID, err = txQueries.FirstActiveCluster(ctx)
		if err != nil || resolvedClusterID == "" {
			return nil, status.Error(codes.Unavailable, "no active cluster available")
		}
	}

	hostname := strings.TrimSpace(req.GetHostname())
	nodeID := deriveEdgeNodeID(hostname)
	if nodeID == "" {
		nodeID = "edge-" + uuid.New().String()[:12]
	}
	if hostname == "" {
		hostname = nodeID
	}

	// Idempotent: if node already exists with same cluster, return it
	existingClusterID, err := txQueries.GetNodeCluster(ctx, nodeID)
	if err == nil {
		if existingClusterID != resolvedClusterID {
			return nil, status.Errorf(codes.FailedPrecondition,
				"node %s already exists in cluster %s", nodeID, existingClusterID)
		}
		if upsertErr := upsertEdgeNodeFingerprint(ctx, tx, tokenRow.TenantID.String, nodeID, req); upsertErr != nil {
			return nil, upsertErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to commit: %v", commitErr)
		}
		return &quartermasterpb.BootstrapEdgeNodeResponse{
			NodeId:    nodeID,
			TenantId:  tokenRow.TenantID.String,
			ClusterId: resolvedClusterID,
		}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Create node
	var extIP any = nil
	if ips := req.GetIps(); len(ips) > 0 {
		extIP = ips[0]
	}

	var lat, lng any
	if ipStr, ok := extIP.(string); ok && s.geoipReader != nil {
		if geo := s.geoipReader.Lookup(ipStr); geo != nil {
			geobucket.BucketGeoData(geo)
			lat = geo.Latitude
			lng = geo.Longitude
		}
	}

	err = txQueries.CreateEdgeNode(ctx, quartermasterdb.CreateEdgeNodeParams{
		ID: uuid.New().String(), NodeID: nodeID, ClusterID: resolvedClusterID, Hostname: hostname,
		ExternalIP: extIP, Latitude: lat, Longitude: lng,
	})

	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create node: %v", err)
	}

	if upsertErr := upsertEdgeNodeFingerprint(ctx, tx, tokenRow.TenantID.String, nodeID, req); upsertErr != nil {
		return nil, upsertErr
	}

	// Update token usage
	err = txQueries.IncrementBootstrapTokenUsage(ctx, tokenRow.ID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update token usage: %v", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to commit bootstrap: %v", err)
	}

	// DNS sync is handled by Navigator's periodic reconciler. Edge health
	// is determined by mesh heartbeats (SyncMesh), not by service_instance
	// status, so there's nothing to resolve until the mesh agent checks in.

	return &quartermasterpb.BootstrapEdgeNodeResponse{
		NodeId:    nodeID,
		TenantId:  tokenRow.TenantID.String,
		ClusterId: resolvedClusterID,
	}, nil
}

// BootstrapInfrastructureNode registers a general infrastructure node using a bootstrap token
func (s *QuartermasterServer) BootstrapInfrastructureNode(ctx context.Context, req *quartermasterpb.BootstrapInfrastructureNodeRequest) (*quartermasterpb.BootstrapInfrastructureNodeResponse, error) {
	token := req.GetToken()
	if token == "" {
		return nil, status.Error(codes.InvalidArgument, "token required")
	}
	nodeType := req.GetNodeType()
	if nodeType == "" {
		return nil, status.Error(codes.InvalidArgument, "node_type required")
	}
	if !models.IsValidNodeType(nodeType) {
		return nil, status.Errorf(codes.InvalidArgument, "node_type must be one of [%s], got %q", strings.Join(models.NodeTypeValues(), ", "), nodeType)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to begin transaction: %v", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			s.logger.WithError(rollbackErr).Debug("transaction rollback failed")
		}
	}()

	// Replay short-circuit: if the caller supplies node_id + public_key and
	// a matching row already exists for this token, this is a retry after a
	// previous RPC committed server-side but the client failed to persist
	// the response. Return the same assignment without re-checking the
	// token's usage budget. Possession of the original token (by hash
	// match, even if spent), the client-chosen node_id, and the locally-
	// generated public_key together prove identity — none of which an
	// attacker can forge without access to the original node.
	if idRaw := strings.TrimSpace(req.GetNodeId()); idRaw != "" && req.WireguardPublicKey != nil {
		pubRaw := strings.TrimSpace(*req.WireguardPublicKey)
		if pubRaw != "" {
			replayResp, replayErr := s.bootstrapReplay(ctx, tx, token, idRaw, pubRaw)
			if replayErr != nil {
				return nil, replayErr
			}
			if replayResp != nil {
				return replayResp, nil
			}
		}
	}

	// Validate token - check for single-use (used_at IS NULL) OR multi-use (usage_count < usage_limit)
	txQueries := quartermasterdb.New(tx)
	tokenRow, err := txQueries.LockInfrastructureEnrollmentToken(ctx, hashBootstrapToken(token))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.Unauthenticated, "invalid or already used token")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	if time.Now().After(tokenRow.ExpiresAt) {
		return nil, status.Error(codes.Unauthenticated, "token expired")
	}

	clientIP := extractClientIP(ctx)
	if !validateExpectedIP(tokenRow.ExpectedIP, clientIP) {
		return nil, status.Error(codes.PermissionDenied, "client IP does not match token expected_ip")
	}

	// Cluster enforcement: if token has a cluster_id binding, validate against target
	targetClusterID := req.GetTargetClusterId()
	tokenClusterID := tokenRow.ClusterID.String
	if tokenClusterID != "" && targetClusterID != "" && tokenClusterID != targetClusterID {
		return nil, status.Errorf(codes.PermissionDenied,
			"token is bound to cluster %s, cannot use for cluster %s", tokenClusterID, targetClusterID)
	}

	// Cluster resolution priority: token binding > request target > fallback
	resolvedClusterID := tokenClusterID
	if resolvedClusterID == "" {
		resolvedClusterID = targetClusterID
	}
	if resolvedClusterID == "" {
		resolvedClusterID, err = txQueries.FirstActiveCluster(ctx)
		if err != nil || resolvedClusterID == "" {
			return nil, status.Error(codes.Unavailable, "no active cluster available")
		}
	}

	nodeID := req.GetNodeId()
	if nodeID == "" {
		nodeID = "node-" + uuid.New().String()[:12]
	}
	hostname := req.GetHostname()
	if hostname == "" {
		hostname = nodeID
	}

	// Idempotent: if the node already exists we return its full assigned
	// identity — not just the IDs — so a client recovering from a mid-flight
	// failure can resume without needing to delete anything server-side.
	existingNode, err := txQueries.GetExistingInfrastructureNode(ctx, nodeID)
	if err == nil {
		if existingNode.ClusterID != resolvedClusterID {
			return nil, status.Errorf(codes.FailedPrecondition, "node already exists in cluster %s", existingNode.ClusterID)
		}
		if strings.TrimSpace(tokenRow.Metadata) != "" && tokenRow.Metadata != "{}" {
			if updateErr := txQueries.MergeInfrastructureNodeMetadata(ctx, nodeID, tokenRow.Metadata); updateErr != nil {
				return nil, status.Errorf(codes.Internal, "update node metadata: %v", updateErr)
			}
		}
		// Commit the tx so subsequent reads see a consistent view even
		// though we didn't mutate anything.
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, status.Errorf(codes.Internal, "commit: %v", commitErr)
		}

		existingMeshCIDR, existingMeshPort := loadClusterMeshConfig(ctx, s.db, resolvedClusterID)
		if existingNode.WireguardPort.Valid && existingNode.WireguardPort.Int32 > 0 {
			existingMeshPort = existingNode.WireguardPort.Int32
		}
		seedPeers, seedSvc := s.collectBootstrapSeed(ctx, resolvedClusterID, nodeID)
		wgIP := ""
		if existingNode.WireguardIP.Valid {
			wgIP = existingNode.WireguardIP.String
		}

		resp := &quartermasterpb.BootstrapInfrastructureNodeResponse{
			NodeId:                nodeID,
			ClusterId:             resolvedClusterID,
			WireguardIp:           wgIP,
			WireguardPort:         existingMeshPort,
			MeshCidr:              existingMeshCIDR,
			QuartermasterGrpcAddr: s.quartermasterGRPCAddr,
			SeedPeers:             seedPeers,
			SeedServiceEndpoints:  seedSvc,
		}
		if tokenRow.TenantID.Valid && tokenRow.TenantID.String != "" {
			t := tokenRow.TenantID.String
			resp.TenantId = &t
		}
		return resp, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Server never generates private keys. A new mesh enrollment must carry
	// its own public key; the private half stays on the node.
	wgPubStr := ""
	if req.WireguardPublicKey != nil {
		wgPubStr = strings.TrimSpace(*req.WireguardPublicKey)
	}
	if wgPubStr == "" {
		return nil, status.Error(codes.InvalidArgument, "wireguard_public_key required: the node must generate its keypair locally and send only the public half")
	}

	// Resolve cluster mesh config so we can assign an IP/port when the
	// request omits them.
	meshConfig, cfgErr := txQueries.GetClusterMeshConfig(ctx, resolvedClusterID)
	if cfgErr != nil {
		return nil, status.Errorf(codes.Internal, "load cluster mesh config: %v", cfgErr)
	}
	clusterMeshCIDR, clusterWGPort := meshConfig.CIDR, meshConfig.Port

	// Determine the node's mesh IP. A client-supplied value is trusted (the
	// GitOps-rendered seed path). Empty means allocate from the cluster CIDR.
	assignedIP := ""
	if req.WireguardIp != nil {
		assignedIP = strings.TrimSpace(*req.WireguardIp)
	}
	if assignedIP == "" {
		if !clusterMeshCIDR.Valid || clusterMeshCIDR.String == "" {
			return nil, status.Errorf(codes.FailedPrecondition, "cluster %q has no wg_mesh_cidr configured; run `frameworks cluster provision` to sync it from the manifest", resolvedClusterID)
		}
		_, cidr, parseErr := net.ParseCIDR(clusterMeshCIDR.String)
		if parseErr != nil {
			return nil, status.Errorf(codes.Internal, "cluster has invalid wg_mesh_cidr %q: %v", clusterMeshCIDR.String, parseErr)
		}
		taken, takenErr := loadTakenMeshIPs(ctx, tx, resolvedClusterID)
		if takenErr != nil {
			return nil, status.Errorf(codes.Internal, "load taken mesh IPs: %v", takenErr)
		}
		allocated, allocErr := pkgmesh.AllocateMeshIP(resolvedClusterID, hostname, cidr, taken)
		if allocErr != nil {
			return nil, status.Errorf(codes.ResourceExhausted, "allocate mesh IP: %v", allocErr)
		}
		assignedIP = allocated.String()
	}

	// Listen port: client-supplied > cluster default > 51820.
	assignedPort := int32(0)
	if req.WireguardPort != nil && *req.WireguardPort > 0 {
		assignedPort = *req.WireguardPort
	} else if clusterWGPort.Valid && clusterWGPort.Int32 > 0 {
		assignedPort = clusterWGPort.Int32
	} else {
		assignedPort = 51820
	}

	// Create node with 'active' status
	var extIP any = nil
	if req.ExternalIp != nil && *req.ExternalIp != "" {
		extIP = *req.ExternalIp
	}
	var intIP any = nil
	if req.InternalIp != nil && *req.InternalIp != "" {
		intIP = *req.InternalIp
	}

	var lat, lng any
	if ipStr, ok := extIP.(string); ok && s.geoipReader != nil {
		if geo := s.geoipReader.Lookup(ipStr); geo != nil {
			geobucket.BucketGeoData(geo)
			lat = geo.Latitude
			lng = geo.Longitude
		}
	}

	// New row via the token/enrollment path → enrollment_origin=runtime_enrolled.
	// The idempotent early return above preserves existing origins.
	err = txQueries.CreateInfrastructureNode(ctx, quartermasterdb.CreateInfrastructureNodeParams{
		ID: uuid.New().String(), NodeID: nodeID, ClusterID: resolvedClusterID, Hostname: hostname, NodeType: nodeType,
		ExternalIP: extIP, InternalIP: intIP, WireguardIP: assignedIP, WireguardPublicKey: wgPubStr,
		WireguardPort: assignedPort, Latitude: lat, Longitude: lng, Metadata: tokenRow.Metadata,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create node: %v", err)
	}

	// Update token usage
	err = txQueries.IncrementBootstrapTokenUsage(ctx, tokenRow.ID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update token usage: %v", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to commit bootstrap: %v", err)
	}

	var tenantResp *string
	if tokenRow.TenantID.Valid && tokenRow.TenantID.String != "" {
		tenantResp = &tokenRow.TenantID.String
	}

	// DNS sync is handled by Navigator's periodic reconciler. Triggering here
	// would be premature: no services are deployed on a freshly-created node,
	// and node_type (e.g. "core") is not a valid service type for DNS lookup.

	// Gather seed state the new node needs to bring up wg0 and start talking
	// to Quartermaster over the mesh. Errors here degrade gracefully — the
	// node can re-fetch via SyncMesh once its interface is up.
	seedPeers, seedSvc := s.collectBootstrapSeed(ctx, resolvedClusterID, nodeID)

	meshCIDR := ""
	if clusterMeshCIDR.Valid {
		meshCIDR = clusterMeshCIDR.String
	}

	return &quartermasterpb.BootstrapInfrastructureNodeResponse{
		NodeId:                nodeID,
		TenantId:              tenantResp,
		ClusterId:             resolvedClusterID,
		WireguardIp:           assignedIP,
		WireguardPort:         assignedPort,
		MeshCidr:              meshCIDR,
		QuartermasterGrpcAddr: s.quartermasterGRPCAddr,
		SeedPeers:             seedPeers,
		SeedServiceEndpoints:  seedSvc,
		// CaBundle left empty: enrolled nodes fetch the internal CA via
		// Navigator after their first successful SyncMesh, matching the
		// existing Privateer cert-sync loop. SERVICE_TOKEN is not returned
		// here — operators deliver it to enrolling nodes via `mesh join`.
	}, nil
}

func upsertEdgeNodeFingerprint(ctx context.Context, tx *sql.Tx, tenantID, nodeID string, req *quartermasterpb.BootstrapEdgeNodeRequest) error {
	machineIDSHA := req.GetMachineIdSha256()
	macsSHA := req.GetMacsSha256()
	ips := req.GetIps()
	labels := req.GetLabels()

	hasLabels := labels != nil && len(labels.GetFields()) > 0
	if machineIDSHA == "" && macsSHA == "" && len(ips) == 0 && !hasLabels {
		return nil
	}

	attrsJSON := "{}"
	if hasLabels {
		attrsBytes, err := json.Marshal(labels.AsMap())
		if err != nil {
			return status.Errorf(codes.Internal, "marshal node fingerprint labels: %v", err)
		}
		attrsJSON = string(attrsBytes)
	}

	err := quartermasterdb.New(tx).UpsertEdgeNodeFingerprint(ctx, quartermasterdb.UpsertEdgeNodeFingerprintParams{
		TenantID: tenantID, NodeID: nodeID, MachineIDSHA: machineIDSHA, MACsSHA: macsSHA, IPs: ips, AttrsJSON: attrsJSON,
	})
	if err != nil {
		return status.Errorf(codes.Internal, "failed to upsert node fingerprint: %v", err)
	}
	return nil
}

// SetNodeEnrollmentOrigin flips a node's enrollment_origin column. Used by
// `frameworks mesh reconcile --write-gitops` to promote runtime_enrolled
// nodes to adopted_local, and by the rotate-on-promotion flow to finalize
// adopted_local → gitops_seed.
func (s *QuartermasterServer) SetNodeEnrollmentOrigin(ctx context.Context, req *quartermasterpb.SetNodeEnrollmentOriginRequest) (*quartermasterpb.SetNodeEnrollmentOriginResponse, error) {
	nodeID := strings.TrimSpace(req.GetNodeId())
	newOrigin := strings.TrimSpace(req.GetEnrollmentOrigin())
	if nodeID == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id required")
	}
	switch newOrigin {
	case "gitops_seed", "runtime_enrolled", "adopted_local":
		// valid
	default:
		return nil, status.Errorf(codes.InvalidArgument, "enrollment_origin must be one of gitops_seed|runtime_enrolled|adopted_local, got %q", newOrigin)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	txQueries := quartermasterdb.New(tx)
	current, err := txQueries.GetNodeEnrollmentOrigin(ctx, nodeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "node %q not found", nodeID)
		}
		return nil, status.Errorf(codes.Internal, "read current origin: %v", err)
	}

	if exp := strings.TrimSpace(req.GetExpectedCurrent()); exp != "" && exp != current {
		return nil, status.Errorf(codes.FailedPrecondition, "node %q enrollment_origin is %q, not the expected %q", nodeID, current, exp)
	}

	if current == newOrigin {
		// Already at desired state; return success without writing.
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, status.Errorf(codes.Internal, "commit: %v", commitErr)
		}
		return &quartermasterpb.SetNodeEnrollmentOriginResponse{NodeId: nodeID, EnrollmentOrigin: current}, nil
	}

	if err := txQueries.UpdateNodeEnrollmentOrigin(ctx, nodeID, newOrigin); err != nil {
		return nil, status.Errorf(codes.Internal, "update origin: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}

	s.logger.WithFields(logging.Fields{
		"node_id":           nodeID,
		"previous_origin":   current,
		"enrollment_origin": newOrigin,
	}).Info("Node enrollment_origin updated")

	return &quartermasterpb.SetNodeEnrollmentOriginResponse{NodeId: nodeID, EnrollmentOrigin: newOrigin}, nil
}

// bootstrapReplay resolves a retry of a previously-committed infrastructure
// enrollment. Returns (response, nil) if the (token, node_id, public_key)
// tuple matches an already-persisted row — in that case the caller returns
// immediately without consuming a fresh token. Returns (nil, nil) if no
// replay match; the caller falls through to the normal token-validation +
// create-or-update path. Non-nil error is propagated.
//
// Replay requires:
//   - token_hash exists in bootstrap_tokens (even if spent)
//   - token not expired, and client IP passes expected_ip gate
//   - infrastructure_node row with node_id exists, wireguard_public_key
//     matches the request
//   - if the token carries a cluster binding, the stored row's cluster_id
//     must match
func (s *QuartermasterServer) bootstrapReplay(ctx context.Context, tx *sql.Tx, token, nodeID, wgPub string) (*quartermasterpb.BootstrapInfrastructureNodeResponse, error) {
	txQueries := quartermasterdb.New(tx)
	tokenRow, err := txQueries.GetBootstrapReplayToken(ctx, hashBootstrapToken(token))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "replay: token lookup: %v", err)
	}
	if time.Now().After(tokenRow.ExpiresAt) {
		return nil, status.Error(codes.Unauthenticated, "token expired")
	}
	if !validateExpectedIP(tokenRow.ExpectedIP, extractClientIP(ctx)) {
		return nil, status.Error(codes.PermissionDenied, "client IP does not match token expected_ip")
	}

	nodeRow, err := txQueries.GetBootstrapReplayNode(ctx, nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		// No existing row — this is not a replay. Fall through to the
		// normal create path.
		return nil, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "replay: node lookup: %v", err)
	}

	if !nodeRow.PublicKey.Valid || nodeRow.PublicKey.String != wgPub {
		// Node exists but with a different public key. This is either a
		// conflict or an attacker guessing. Refuse — the non-replay path
		// would also refuse because node_id is already taken.
		return nil, status.Error(codes.FailedPrecondition, "node_id already registered with a different wireguard_public_key")
	}

	// Enforce the token's cluster binding against the stored row too: a
	// token scoped to cluster A must not retrieve an assignment in B.
	if tokenRow.ClusterID.Valid && tokenRow.ClusterID.String != "" && nodeRow.ClusterID.Valid && tokenRow.ClusterID.String != nodeRow.ClusterID.String {
		return nil, status.Errorf(codes.PermissionDenied, "token is bound to cluster %s, node is in %s", tokenRow.ClusterID.String, nodeRow.ClusterID.String)
	}

	clusterIDStr := ""
	if nodeRow.ClusterID.Valid {
		clusterIDStr = nodeRow.ClusterID.String
	}
	wgIP := ""
	if nodeRow.WireguardIP.Valid {
		wgIP = nodeRow.WireguardIP.String
	}
	wgPort := int32(0)
	if nodeRow.WireguardPort.Valid {
		wgPort = nodeRow.WireguardPort.Int32
	}

	// Rebuild the full response the same way the first-successful call did,
	// so the client receives identical seed state.
	meshCIDR, meshPort := loadClusterMeshConfig(ctx, s.db, clusterIDStr)
	if wgPort > 0 {
		meshPort = wgPort
	}
	seedPeers, seedSvc := s.collectBootstrapSeed(ctx, clusterIDStr, nodeID)

	resp := &quartermasterpb.BootstrapInfrastructureNodeResponse{
		NodeId:                nodeID,
		ClusterId:             clusterIDStr,
		WireguardIp:           wgIP,
		WireguardPort:         meshPort,
		MeshCidr:              meshCIDR,
		QuartermasterGrpcAddr: s.quartermasterGRPCAddr,
		SeedPeers:             seedPeers,
		SeedServiceEndpoints:  seedSvc,
	}
	if nodeRow.TenantID.Valid && nodeRow.TenantID.String != "" {
		t := nodeRow.TenantID.String
		resp.TenantId = &t
	}
	return resp, nil
}

// loadClusterMeshConfig returns the cluster's wg_mesh_cidr and default
// wg_listen_port. Failures degrade to zero values so the caller surfaces a
// sensible error rather than stalling the bootstrap flow.
func loadClusterMeshConfig(ctx context.Context, db *sql.DB, clusterID string) (string, int32) {
	// Scan errors surface as empty return values, which the caller treats as
	// "cluster mesh config missing" — FailedPrecondition with a remediation
	// hint. Logging the raw error here would be noisy on cold caches.
	config, _ := quartermasterdb.New(db).GetClusterMeshConfig(ctx, clusterID) //nolint:errcheck
	cidrStr := ""
	if config.CIDR.Valid {
		cidrStr = config.CIDR.String
	}
	portVal := int32(0)
	if config.Port.Valid {
		portVal = config.Port.Int32
	}
	return cidrStr, portVal
}

// collectBootstrapSeed returns the seed peer set and service endpoints a
// freshly-enrolled node should apply before its first SyncMesh. Excludes the
// enrolling node itself. Errors are logged and produce empty results so
// bootstrap never fails on auxiliary data: the node will rediscover via
// SyncMesh once connected.
func (s *QuartermasterServer) collectBootstrapSeed(ctx context.Context, clusterID, excludeNodeID string) ([]*quartermasterpb.InfrastructurePeer, map[string]*quartermasterpb.ServiceEndpoints) {
	dnsRequired, peerRequired, globalPeerRequired, infraRequired, reqErr := s.meshServiceRequirements(ctx, excludeNodeID)
	if reqErr != nil {
		s.logger.WithError(reqErr).Warn("collectBootstrapSeed: service requirements unavailable")
		return nil, nil
	}
	endpoints, requiredPeerNodeIDs, endpointErr := s.collectMeshServiceEndpoints(ctx, clusterID, excludeNodeID, dnsRequired, peerRequired, globalPeerRequired)
	if endpointErr != nil {
		s.logger.WithError(endpointErr).Warn("collectBootstrapSeed: service endpoints unavailable")
		return nil, endpoints
	}
	if infraPeers, infraErr := s.collectInfraPeerNodeIDs(ctx, clusterID, excludeNodeID, infraRequired); infraErr != nil {
		s.logger.WithError(infraErr).Warn("collectBootstrapSeed: infra peers unavailable")
	} else {
		for nodeID := range infraPeers {
			requiredPeerNodeIDs[nodeID] = struct{}{}
		}
	}
	if reciprocalPeers, reciprocalErr := s.collectReciprocalServicePeerNodeIDs(ctx, clusterID, excludeNodeID); reciprocalErr != nil {
		s.logger.WithError(reciprocalErr).Warn("collectBootstrapSeed: reciprocal peers unavailable")
	} else {
		for nodeID := range reciprocalPeers {
			requiredPeerNodeIDs[nodeID] = struct{}{}
		}
	}

	rows, err := quartermasterdb.New(s.db).ListMeshPeerCandidates(ctx, quartermasterdb.ListMeshPeerCandidatesParams{
		NodeID: excludeNodeID, ClusterID: clusterID, RequiredNodeIDs: sortedStringKeys(requiredPeerNodeIDs),
	})
	if err != nil {
		s.logger.WithError(err).Warn("collectBootstrapSeed: peer query failed")
		return nil, endpoints
	}
	var peers []*quartermasterpb.InfrastructurePeer
	for _, row := range rows {
		if row.ScanErr != nil {
			continue
		}
		p := quartermasterpb.InfrastructurePeer{NodeName: row.NodeName, PublicKey: row.PublicKey}
		endpoint := ""
		if row.ExternalIP.Valid && row.ExternalIP.String != "" {
			endpoint = row.ExternalIP.String
		} else if row.InternalIP.Valid && row.InternalIP.String != "" {
			endpoint = row.InternalIP.String
		}
		if endpoint == "" || !row.WireguardIP.Valid {
			continue
		}
		port := int32(51820)
		if row.ListenPort.Valid && row.ListenPort.Int32 > 0 {
			port = row.ListenPort.Int32
		}
		p.Endpoint = fmt.Sprintf("%s:%d", endpoint, port)
		p.AllowedIps = []string{row.WireguardIP.String + "/32"}
		p.KeepAlive = 25
		peers = append(peers, &p)
	}
	return peers, endpoints
}

func (s *QuartermasterServer) meshServiceRequirements(ctx context.Context, nodeID string) (map[string]struct{}, map[string]struct{}, map[string]struct{}, []topology.InfraDependency, error) {
	dnsRequired := map[string]struct{}{"navigator": {}, "quartermaster": {}}
	peerRequired := map[string]struct{}{"navigator": {}, "quartermaster": {}}
	globalPeerRequired := map[string]struct{}{}
	var infraRequired []topology.InfraDependency
	var localServices []string
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		services, queryErr := quartermasterdb.New(s.db).ListLocalMeshServiceTypes(ctx, nodeID)
		if queryErr != nil {
			return fmt.Errorf("local service query: %w", queryErr)
		}
		localServices = services
		return nil
	})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	for _, dep := range topology.DNSDependenciesForServices(localServices) {
		dnsRequired[dep] = struct{}{}
		peerRequired[dep] = struct{}{}
	}
	for _, dep := range topology.GlobalDNSDependenciesForServices(localServices) {
		dnsRequired[dep] = struct{}{}
		peerRequired[dep] = struct{}{}
		globalPeerRequired[dep] = struct{}{}
	}
	for _, serviceType := range localServices {
		for _, dep := range topology.InfraDependencies(serviceType) {
			if dep.Kind != "" {
				infraRequired = append(infraRequired, dep)
			}
		}
		for _, peerService := range topology.FederationPeerServices(serviceType) {
			globalPeerRequired[peerService] = struct{}{}
		}
	}
	return dnsRequired, peerRequired, globalPeerRequired, infraRequired, nil
}

func (s *QuartermasterServer) collectMeshServiceEndpoints(ctx context.Context, clusterID, nodeID string, dnsRequired, peerRequired, globalPeerRequired map[string]struct{}) (map[string]*quartermasterpb.ServiceEndpoints, map[string]struct{}, error) {
	endpoints := map[string]*quartermasterpb.ServiceEndpoints{}
	requiredPeerNodeIDs := map[string]struct{}{}
	peerTypes := sortedStringKeys(peerRequired)
	globalPeerTypes := sortedStringKeys(globalPeerRequired)
	if len(peerTypes) == 0 && len(globalPeerTypes) == 0 {
		return endpoints, requiredPeerNodeIDs, nil
	}
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		svcRows, svcErr := quartermasterdb.New(s.db).ListMeshServiceEndpoints(ctx, quartermasterdb.MeshServiceEndpointParams{
			ClusterID: clusterID, NodeID: nodeID, PeerTypes: peerTypes, GlobalPeerTypes: globalPeerTypes,
		})
		if svcErr != nil {
			return fmt.Errorf("service endpoint query: %w", svcErr)
		}
		nextEndpoints := map[string]*quartermasterpb.ServiceEndpoints{}
		nextRequiredPeerNodeIDs := map[string]struct{}{}
		for _, row := range svcRows {
			if row.WireguardIP == "" {
				continue
			}
			if row.NodeID != "" {
				nextRequiredPeerNodeIDs[row.NodeID] = struct{}{}
			}
			if _, ok := dnsRequired[row.ServiceType]; !ok {
				continue
			}
			if nextEndpoints[row.ServiceType] == nil {
				nextEndpoints[row.ServiceType] = &quartermasterpb.ServiceEndpoints{Ips: []string{}}
			}
			nextEndpoints[row.ServiceType].Ips = append(nextEndpoints[row.ServiceType].Ips, row.WireguardIP)
		}
		endpoints = nextEndpoints
		requiredPeerNodeIDs = nextRequiredPeerNodeIDs
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return endpoints, requiredPeerNodeIDs, nil
}

func (s *QuartermasterServer) collectInfraPeerNodeIDs(ctx context.Context, clusterID, nodeID string, infraRequired []topology.InfraDependency) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	deps := dedupeInfraDependencies(infraRequired)
	if len(deps) == 0 {
		return out, nil
	}
	kinds := make([]string, 0, len(deps))
	providers := make([]string, 0, len(deps))
	names := make([]string, 0, len(deps))
	for _, dep := range deps {
		kinds = append(kinds, dep.Kind)
		providers = append(providers, dep.Provider)
		names = append(names, dep.Name)
	}

	var peerNodeIDs []string
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		rows, queryErr := quartermasterdb.New(s.db).ListInfraMeshPeerNodeIDs(ctx, quartermasterdb.InfraMeshPeerParams{
			ClusterID: clusterID, NodeID: nodeID, Kinds: kinds, Providers: providers, Names: names,
		})
		if queryErr != nil {
			return fmt.Errorf("infra provider query: %w", queryErr)
		}
		peerNodeIDs = rows
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, peerNodeID := range peerNodeIDs {
		out[peerNodeID] = struct{}{}
	}
	return out, nil
}

func (s *QuartermasterServer) collectReciprocalServicePeerNodeIDs(ctx context.Context, clusterID, nodeID string) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	provided, err := s.meshProvidedServiceTypes(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("provided service query: %w", err)
	}
	inputs := reciprocalServiceDependencyInputs(provided)
	if len(inputs) == 0 {
		return out, nil
	}

	providedTypes := make([]string, 0, len(inputs))
	dependentTypes := make([]string, 0, len(inputs))
	globalFlags := make([]bool, 0, len(inputs))
	for _, input := range inputs {
		providedTypes = append(providedTypes, input.providedType)
		dependentTypes = append(dependentTypes, input.dependentType)
		globalFlags = append(globalFlags, input.global)
	}

	var peerNodeIDs []string
	err = database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		peerNodeIDs, queryErr = quartermasterdb.New(s.db).ListReciprocalMeshPeerNodeIDs(ctx, quartermasterdb.ReciprocalMeshPeerParams{
			NodeID:         nodeID,
			ClusterID:      clusterID,
			ProvidedTypes:  providedTypes,
			DependentTypes: dependentTypes,
			GlobalFlags:    globalFlags,
		})
		if queryErr != nil {
			return fmt.Errorf("dependent node query: %w", queryErr)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, peerNodeID := range peerNodeIDs {
		out[peerNodeID] = struct{}{}
	}
	return out, nil
}

type reciprocalServiceDependencyInput struct {
	providedType  string
	dependentType string
	global        bool
}

func reciprocalServiceDependencyInputs(provided []string) []reciprocalServiceDependencyInput {
	var out []reciprocalServiceDependencyInput
	seen := map[string]struct{}{}
	for _, providedType := range provided {
		if providedType == "" {
			continue
		}
		dependents := topology.ServiceDependents([]string{providedType})
		if len(dependents) == 0 {
			continue
		}
		globalDependents := map[string]struct{}{}
		for _, dependentType := range topology.GlobalDNSServiceDependents([]string{providedType}) {
			globalDependents[dependentType] = struct{}{}
		}
		for _, dependentType := range dependents {
			key := providedType + "\x00" + dependentType
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			_, isGlobal := globalDependents[dependentType]
			out = append(out, reciprocalServiceDependencyInput{
				providedType:  providedType,
				dependentType: dependentType,
				global:        isGlobal,
			})
		}
	}
	return out
}

func (s *QuartermasterServer) meshProvidedServiceTypes(ctx context.Context, nodeID string) ([]string, error) {
	var out []string
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		next, queryErr := quartermasterdb.New(s.db).ListLocalMeshServiceTypes(ctx, nodeID)
		if queryErr != nil {
			return queryErr
		}
		out = next
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func dedupeInfraDependencies(in []topology.InfraDependency) []topology.InfraDependency {
	seen := map[string]struct{}{}
	out := make([]topology.InfraDependency, 0, len(in))
	for _, dep := range in {
		key := dep.Kind + "\x00" + dep.Provider + "\x00" + dep.Name
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, dep)
	}
	return out
}

func sortedStringKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CreateBootstrapToken creates a new bootstrap token
func (s *QuartermasterServer) CreateBootstrapToken(ctx context.Context, req *quartermasterpb.CreateBootstrapTokenRequest) (*quartermasterpb.CreateBootstrapTokenResponse, error) {
	name := req.GetName()
	kind := req.GetKind()
	if name == "" || kind == "" {
		return nil, status.Error(codes.InvalidArgument, "name and kind required")
	}

	// Validate kind must be "edge_node", "service", or "infrastructure_node"
	if kind != "edge_node" && kind != "service" && kind != "infrastructure_node" {
		return nil, status.Error(codes.InvalidArgument, "kind must be 'edge_node', 'service', or 'infrastructure_node'")
	}

	// edge_node tokens MUST have tenant_id
	if kind == "edge_node" && (req.TenantId == nil || *req.TenantId == "") {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required for edge_node tokens")
	}

	tokenID := uuid.New().String()
	tokenValue, err := generateSecureToken(32)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate token: %v", err)
	}
	tokenValue = "bt_" + tokenValue

	// Parse TTL
	ttl := req.GetTtl()
	if ttl == "" {
		ttl = "24h"
	}
	duration, err := time.ParseDuration(ttl)
	if err != nil {
		duration = 24 * time.Hour
	}
	expiresAt := time.Now().Add(duration)

	var metadataJSON any = nil
	if metadata := req.GetMetadata(); metadata != nil && len(metadata.GetFields()) > 0 {
		encoded, marshalErr := json.Marshal(metadata.AsMap())
		if marshalErr != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid metadata: %v", marshalErr)
		}
		metadataJSON = string(encoded)
	}

	err = quartermasterdb.New(s.db).CreateBootstrapToken(ctx, quartermasterdb.CreateBootstrapTokenParams{
		ID: tokenID, Name: name, TokenHash: hashBootstrapToken(tokenValue), TokenPrefix: tokenPrefix(tokenValue),
		Kind: kind, TenantID: req.TenantId, ClusterID: req.ClusterId, ExpectedIP: req.ExpectedIp,
		Metadata: metadataJSON, UsageLimit: req.UsageLimit, ExpiresAt: expiresAt,
	})

	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create token: %v", err)
	}

	return &quartermasterpb.CreateBootstrapTokenResponse{
		Token: &quartermasterpb.BootstrapToken{
			Id:         tokenID,
			Name:       name,
			Token:      tokenValue,
			Kind:       kind,
			TenantId:   req.TenantId,
			ClusterId:  req.ClusterId,
			ExpectedIp: req.ExpectedIp,
			Metadata:   req.GetMetadata(),
			UsageLimit: req.UsageLimit,
			UsageCount: 0,
			ExpiresAt:  timestamppb.New(expiresAt),
			CreatedAt:  timestamppb.Now(),
		},
	}, nil
}

// ListBootstrapTokens returns bootstrap tokens with optional filters
func (s *QuartermasterServer) ListBootstrapTokens(ctx context.Context, req *quartermasterpb.ListBootstrapTokensRequest) (*quartermasterpb.ListBootstrapTokensResponse, error) {
	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid cursor: %v", err)
	}

	filter := quartermasterdb.BootstrapTokenFilter{Backward: params.Direction == pagination.Backward, Limit: params.Limit + 1}
	if req.GetKind() != "" {
		kind := req.GetKind()
		filter.Kind = &kind
	}
	if req.GetTenantId() != "" {
		tenantID := req.GetTenantId()
		filter.TenantID = &tenantID
	}
	if params.Cursor != nil {
		filter.CursorTime = &params.Cursor.Timestamp
		filter.CursorID = params.Cursor.ID
	}
	rows, err := quartermasterdb.New(s.db).ListBootstrapTokens(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	var tokens []*quartermasterpb.BootstrapToken
	for _, row := range rows {
		token := quartermasterpb.BootstrapToken{Id: row.ID, Name: row.Name, Token: row.TokenPrefix, Kind: row.Kind,
			UsageCount: row.UsageCount, ExpiresAt: timestamppb.New(row.ExpiresAt), CreatedAt: timestamppb.New(row.CreatedAt)}
		if row.TenantID.Valid {
			token.TenantId = &row.TenantID.String
		}
		if row.ClusterID.Valid {
			token.ClusterId = &row.ClusterID.String
		}
		if row.ExpectedIP.Valid {
			token.ExpectedIp = &row.ExpectedIP.String
		}
		if row.UsageLimit.Valid {
			token.UsageLimit = &row.UsageLimit.Int32
		}
		if row.UsedAt.Valid {
			token.UsedAt = timestamppb.New(row.UsedAt.Time)
		}
		if row.CreatedBy.Valid {
			token.CreatedBy = &row.CreatedBy.String
		}
		tokens = append(tokens, &token)
	}

	// Determine pagination info
	resultsLen := len(tokens)
	if resultsLen > params.Limit {
		tokens = tokens[:params.Limit]
	}

	// Reverse results for backward pagination to maintain consistent order
	if params.Direction == pagination.Backward {
		slices.Reverse(tokens)
	}

	// Build cursors
	var startCursor, endCursor string
	if len(tokens) > 0 {
		first := tokens[0]
		last := tokens[len(tokens)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.Id)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.Id)
	}

	return &quartermasterpb.ListBootstrapTokensResponse{
		Tokens:     tokens,
		Pagination: pagination.BuildResponse(resultsLen, params.Limit, params.Direction, int32(len(tokens)), startCursor, endCursor),
	}, nil
}

// RevokeBootstrapToken revokes a bootstrap token
func (s *QuartermasterServer) RevokeBootstrapToken(ctx context.Context, req *quartermasterpb.RevokeBootstrapTokenRequest) (*emptypb.Empty, error) {
	tokenID := req.GetTokenId()
	if tokenID == "" {
		return nil, status.Error(codes.InvalidArgument, "token_id required")
	}

	rows, err := quartermasterdb.New(s.db).RevokeBootstrapToken(ctx, tokenID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to revoke token: %v", err)
	}

	if rows == 0 {
		return nil, status.Error(codes.NotFound, "token not found")
	}

	return &emptypb.Empty{}, nil
}

// ValidateBootstrapToken checks a bootstrap token's validity.
// When client_ip is set, validates against the token's expected_ip.
// When consume is true, increments usage_count (used by PreRegisterEdge).
func (s *QuartermasterServer) ValidateBootstrapToken(ctx context.Context, req *quartermasterpb.ValidateBootstrapTokenRequest) (*quartermasterpb.ValidateBootstrapTokenResponse, error) {
	token := strings.TrimSpace(req.GetToken())
	if token == "" {
		return nil, status.Error(codes.InvalidArgument, "token required")
	}

	var tokenRow quartermasterdb.ValidatedBootstrapTokenRow
	queries := quartermasterdb.New(s.db)
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		tokenRow, queryErr = queries.GetBootstrapTokenForValidation(ctx, hashBootstrapToken(token))
		return queryErr
	})

	if errors.Is(err, sql.ErrNoRows) {
		return &quartermasterpb.ValidateBootstrapTokenResponse{Valid: false, Reason: "not_found"}, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Single-use tokens (usage_limit IS NULL) are consumed when used_at is set
	if !tokenRow.UsageLimit.Valid && tokenRow.UsedAt.Valid {
		return &quartermasterpb.ValidateBootstrapTokenResponse{Valid: false, Kind: tokenRow.Kind, Reason: "already_used"}, nil
	}

	// Multi-use tokens: reject when usage_count >= usage_limit
	if tokenRow.UsageLimit.Valid && tokenRow.UsageLimit.Int32 > 0 && tokenRow.UsageCount >= tokenRow.UsageLimit.Int32 {
		return &quartermasterpb.ValidateBootstrapTokenResponse{Valid: false, Kind: tokenRow.Kind, Reason: "usage_exceeded"}, nil
	}

	if time.Now().After(tokenRow.ExpiresAt) {
		return &quartermasterpb.ValidateBootstrapTokenResponse{Valid: false, Kind: tokenRow.Kind, Reason: "expired"}, nil
	}

	// IP binding: if client_ip is provided and token has expected_ip, validate match
	if clientIP := req.GetClientIp(); clientIP != "" {
		if !validateExpectedIP(tokenRow.ExpectedIP, clientIP) {
			return &quartermasterpb.ValidateBootstrapTokenResponse{Valid: false, Kind: tokenRow.Kind, Reason: "ip_mismatch"}, nil
		}
	}

	// Consume: increment usage_count if requested (PreRegisterEdge uses this)
	if req.GetConsume() {
		var rowsAffected int64
		updateErr := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			var updateErr error
			rowsAffected, updateErr = queries.ConsumeBootstrapToken(ctx, hashBootstrapToken(token))
			return updateErr
		})
		if updateErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to consume bootstrap token: %v", updateErr)
		}
		if rowsAffected == 0 {
			return &quartermasterpb.ValidateBootstrapTokenResponse{Valid: false, Kind: tokenRow.Kind, Reason: "already_used"}, nil
		}
	}

	resp := &quartermasterpb.ValidateBootstrapTokenResponse{
		Valid: true,
		Kind:  tokenRow.Kind,
	}
	if tokenRow.TenantID.Valid {
		resp.TenantId = tokenRow.TenantID.String
	}
	if tokenRow.ClusterID.Valid {
		resp.ClusterId = tokenRow.ClusterID.String
		// Bootstrap tokens are consumed before an edge joins the mesh, so expose
		// the public Foghorn edge listener rather than the internal control
		// assignment used by service-to-service clients.
		if addr, lookupErr := s.lookupClusterPublicFoghornGRPC(ctx, tokenRow.ClusterID.String); lookupErr == nil {
			resp.FoghornGrpcAddr = addr
		}
	}
	if len(tokenRow.Metadata) > 0 {
		var metadataMap map[string]any
		if json.Unmarshal(tokenRow.Metadata, &metadataMap) == nil && len(metadataMap) > 0 {
			resp.Metadata = mapToStruct(metadataMap)
		}
	}
	return resp, nil
}

func (s *QuartermasterServer) lookupClusterPublicFoghornGRPC(ctx context.Context, clusterID string) (string, error) {
	var rootDomain string
	queries := quartermasterdb.New(s.db)
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		rootDomain, queryErr = queries.GetClusterPublicRootDomain(ctx, clusterID)
		return queryErr
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return publicFoghornGRPCAddr(clusterID, rootDomain), nil
}

func publicFoghornGRPCAddr(clusterID, baseURL string) string {
	rootDomain := dns.NormalizeDomainScope(baseURL)
	clusterSlug := dns.SanitizeLabel(clusterID)
	if rootDomain == "" || clusterSlug == "" {
		return ""
	}
	host, ok := dns.ServiceFQDN("foghorn", clusterSlug+"."+rootDomain)
	if !ok || host == "" {
		return ""
	}
	return net.JoinHostPort(host, strconv.Itoa(foghornExternalGRPCPort))
}

// lookupClusterFoghornGRPC returns the internal gRPC advertise addr of the
// Foghorn instance currently assigned to the given cluster. Returns an empty
// string with nil error when no active assignment exists yet.
func (s *QuartermasterServer) lookupClusterFoghornGRPC(ctx context.Context, clusterID string) (string, error) {
	var addr string
	queries := quartermasterdb.New(s.db)
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		addr, queryErr = queries.GetClusterInternalFoghornGRPC(ctx, clusterID)
		return queryErr
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return addr, nil
}

// ============================================================================
// MESH SERVICE
// ============================================================================

// SyncMesh handles WireGuard mesh synchronization
func (s *QuartermasterServer) SyncMesh(ctx context.Context, req *quartermasterpb.InfrastructureSyncRequest) (*quartermasterpb.InfrastructureSyncResponse, error) {
	nodeID := req.GetNodeId()
	publicKey := req.GetPublicKey()
	if nodeID == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id required")
	}
	var clusterID string
	var peerCount, requiredPeerCount, serviceEndpointTypeCount int
	cacheResult := "not_checked"
	started := time.Now()
	phaseDurations := logging.Fields{}
	recordPhase := func(phase string, phaseStarted time.Time) {
		elapsed := time.Since(phaseStarted)
		phaseDurations[phase+"_duration_ms"] = elapsed.Milliseconds()
		if s.metrics != nil && s.metrics.SyncMeshPhaseDuration != nil {
			s.metrics.SyncMeshPhaseDuration.WithLabelValues(phase).Observe(elapsed.Seconds())
		}
	}
	defer func() {
		total := time.Since(started)
		if total < syncMeshSlowLogThreshold {
			return
		}
		fields := logging.Fields{
			"node_id":                nodeID,
			"cluster_id":             clusterID,
			"duration_ms":            total.Milliseconds(),
			"required_peer_count":    requiredPeerCount,
			"peer_count":             peerCount,
			"service_endpoint_types": serviceEndpointTypeCount,
			"cache_result":           cacheResult,
		}
		for key, val := range phaseDurations {
			fields[key] = val
		}
		s.logger.WithFields(fields).Warn("Slow SyncMesh")
	}()

	// 1. Check if node exists and get current GitOps WireGuard identity.
	phaseStarted := time.Now()
	queries := quartermasterdb.New(s.db)
	identity, err := queries.GetMeshNodeIdentity(ctx, nodeID)
	clusterID = identity.ClusterID
	recordPhase("identity_lookup", phaseStarted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "node not found - please register the node first")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get node info: %v", err)
	}

	if !identity.WireguardIP.Valid || strings.TrimSpace(identity.WireguardIP.String) == "" {
		return nil, status.Errorf(codes.FailedPrecondition, "node %q has no recorded wireguard_ip; gitops_seed nodes need `frameworks mesh wg generate` + provision, runtime_enrolled nodes need `frameworks mesh join`", nodeID)
	}
	wireguardIP := identity.WireguardIP.String
	if !identity.PublicKey.Valid || strings.TrimSpace(identity.PublicKey.String) == "" {
		return nil, status.Errorf(codes.FailedPrecondition, "node %q has no recorded wireguard_public_key; gitops_seed nodes need `frameworks mesh wg generate` + provision, runtime_enrolled nodes need `frameworks mesh join`", nodeID)
	}
	if publicKey == "" {
		return nil, status.Error(codes.InvalidArgument, "public_key required")
	}
	if identity.PublicKey.String != publicKey {
		return nil, status.Errorf(codes.FailedPrecondition, "node %q public key does not match the recorded value", nodeID)
	}
	if !identity.ListenPort.Valid || identity.ListenPort.Int32 <= 0 {
		return nil, status.Errorf(codes.FailedPrecondition, "node %q has no recorded wireguard_listen_port", nodeID)
	}
	if req.GetListenPort() > 0 && req.GetListenPort() != identity.ListenPort.Int32 {
		return nil, status.Errorf(codes.FailedPrecondition, "node %q listen port %d does not match the recorded value %d", nodeID, req.GetListenPort(), identity.ListenPort.Int32)
	}
	wireguardPort := identity.ListenPort.Int32

	// 2. Update heartbeat every sync. WireGuard identity is set by either
	// CreateNode (gitops_seed) or BootstrapInfrastructureNode
	// (runtime_enrolled); SyncMesh only reads it. The applied revision is
	// persisted as the agent reports it — empty string is stored as NULL
	// so 'mesh wg audit' can distinguish "never reported" from "reported
	// nothing yet".
	var appliedRev sql.NullString
	if rev := strings.TrimSpace(req.GetAppliedMeshRevision()); rev != "" {
		appliedRev = sql.NullString{String: rev, Valid: true}
	}
	// Snapshot is conditionally written so a failed collection does not blank
	// a previously-good row. Freshness is based on Quartermaster receipt time
	// so node clock skew cannot make a stale node look healthy.
	snap := req.GetResourceSnapshot()
	var (
		snapCPU                                 sql.NullFloat64
		snapRamUsed, snapRamTotal               sql.NullInt64
		snapDiskUsed, snapDiskTotal, snapUptime sql.NullInt64
		snapAt                                  sql.NullTime
		snapPresent                             bool
	)
	if resourceSnapshotComplete(snap) {
		snapPresent = true
		snapCPU = sql.NullFloat64{Float64: float64(snap.GetCpuPercent()), Valid: true}
		snapRamUsed = sql.NullInt64{Int64: int64(snap.GetRamUsedBytes()), Valid: true}
		snapRamTotal = sql.NullInt64{Int64: int64(snap.GetRamTotalBytes()), Valid: true}
		snapDiskUsed = sql.NullInt64{Int64: int64(snap.GetDiskUsedBytes()), Valid: true}
		snapDiskTotal = sql.NullInt64{Int64: int64(snap.GetDiskTotalBytes()), Valid: true}
		snapUptime = sql.NullInt64{Int64: int64(snap.GetUptimeSeconds()), Valid: true}
		snapAt = sql.NullTime{Time: time.Now().UTC(), Valid: true}
	}
	phaseStarted = time.Now()
	if snapPresent {
		err = queries.UpdateMeshHeartbeatWithSnapshot(ctx, quartermasterdb.UpdateMeshHeartbeatParams{
			NodeID: nodeID, AppliedRevision: appliedRev, SnapshotCPU: snapCPU,
			SnapshotRamUsed: snapRamUsed, SnapshotRamTotal: snapRamTotal,
			SnapshotDiskUsed: snapDiskUsed, SnapshotDiskTotal: snapDiskTotal,
			SnapshotUptime: snapUptime, SnapshotAt: snapAt,
		})
	} else {
		err = queries.UpdateMeshHeartbeat(ctx, nodeID, appliedRev)
	}
	recordPhase("heartbeat_update", phaseStarted)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to update node heartbeat")
	}

	phaseStarted = time.Now()
	cfg, currentTopologySourceHash, cacheOK, cacheErr := s.loadMeshNodeConfig(ctx, nodeID)
	recordPhase("mesh_config_cache", phaseStarted)
	if cacheErr != nil {
		cacheResult = "unavailable"
		s.logger.WithError(cacheErr).WithField("node_id", nodeID).Warn("Mesh config cache unavailable; recomputing")
		cacheOK = false
	}
	if cacheOK && cfg.TopologySourceHash == currentTopologySourceHash {
		cacheResult = "hit"
	} else if cacheOK {
		cacheResult = "stale_served"
	} else {
		if cacheResult != "unavailable" {
			cacheResult = "miss"
		}
		currentHash, hashErr := s.currentMeshTopologySourceHash(ctx)
		if hashErr != nil {
			return nil, status.Errorf(codes.Internal, "mesh topology revision unavailable: %v", hashErr)
		}
		currentTopologySourceHash = currentHash

		phaseStarted = time.Now()
		var buildErr error
		cfg, requiredPeerCount, buildErr = s.buildMeshNodeConfig(ctx, nodeID, clusterID, wireguardIP, wireguardPort, currentTopologySourceHash, recordPhase)
		recordPhase("mesh_config_recompute", phaseStarted)
		if buildErr != nil {
			return nil, status.Errorf(codes.Internal, "mesh config rebuild failed: %v", buildErr)
		}
		phaseStarted = time.Now()
		if storeErr := s.storeMeshNodeConfig(ctx, cfg); storeErr != nil {
			recordPhase("mesh_config_store", phaseStarted)
			cacheResult += "_store_failed"
			s.logger.WithError(storeErr).WithField("node_id", nodeID).Warn("Mesh config cache write failed")
		} else {
			recordPhase("mesh_config_store", phaseStarted)
		}
	}
	if requiredPeerCount == 0 {
		requiredPeerCount = len(cfg.Peers)
	}
	peerCount = len(cfg.Peers)
	serviceEndpointTypeCount = len(cfg.ServiceEndpoints)

	return &quartermasterpb.InfrastructureSyncResponse{
		WireguardIp:      wireguardIP,
		WireguardPort:    wireguardPort,
		Peers:            cfg.Peers,
		ServiceEndpoints: cfg.ServiceEndpoints,
		MeshRevision:     cfg.MeshRevision,
	}, nil
}

type meshPhaseRecorder func(phase string, started time.Time)

type meshNodeConfig struct {
	NodeID             string
	ClusterID          string
	MeshRevision       string
	TopologySourceHash string
	WireguardIP        string
	WireguardPort      int32
	Peers              []*quartermasterpb.InfrastructurePeer
	ServiceEndpoints   map[string]*quartermasterpb.ServiceEndpoints
}

type storedMeshPeer struct {
	NodeName   string   `json:"node_name"`
	PublicKey  string   `json:"public_key"`
	Endpoint   string   `json:"endpoint"`
	AllowedIPs []string `json:"allowed_ips"`
	KeepAlive  int32    `json:"keep_alive"`
}

type storedMeshServiceEndpoints struct {
	IPs []string `json:"ips"`
}

func recordMeshPhase(record meshPhaseRecorder, phase string, started time.Time) {
	if record != nil {
		record(phase, started)
	}
}

func meshTopologySourceHash(revision int64) string {
	return fmt.Sprintf("%s:%d", meshTopologyPlannerVersion, revision)
}

func (s *QuartermasterServer) currentMeshTopologySourceHash(ctx context.Context) (string, error) {
	revision, err := quartermasterdb.New(s.db).CurrentMeshTopologyRevision(ctx)
	if err != nil {
		return "", err
	}
	return meshTopologySourceHash(revision), nil
}

func (s *QuartermasterServer) loadMeshNodeConfig(ctx context.Context, nodeID string) (meshNodeConfig, string, bool, error) {
	row, err := quartermasterdb.New(s.db).GetMeshNodeConfig(ctx, nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return meshNodeConfig{}, "", false, nil
	}
	if err != nil {
		return meshNodeConfig{}, "", false, err
	}
	peers, err := decodeStoredMeshPeers(row.Peers)
	if err != nil {
		return meshNodeConfig{}, "", false, fmt.Errorf("decode mesh peers: %w", err)
	}
	endpoints, err := decodeStoredMeshServiceEndpoints(row.ServiceEndpoints)
	if err != nil {
		return meshNodeConfig{}, "", false, fmt.Errorf("decode mesh service endpoints: %w", err)
	}
	cfg := meshNodeConfig{NodeID: nodeID, ClusterID: row.ClusterID, MeshRevision: row.MeshRevision,
		TopologySourceHash: row.TopologySourceHash, WireguardIP: row.WireguardIP,
		WireguardPort: row.WireguardPort, Peers: peers, ServiceEndpoints: endpoints}
	return cfg, meshTopologySourceHash(row.CurrentTopologyRevision), true, nil
}

func (s *QuartermasterServer) storeMeshNodeConfig(ctx context.Context, cfg meshNodeConfig) error {
	peersJSON, err := encodeStoredMeshPeers(cfg.Peers)
	if err != nil {
		return fmt.Errorf("encode mesh peers: %w", err)
	}
	endpointsJSON, err := encodeStoredMeshServiceEndpoints(cfg.ServiceEndpoints)
	if err != nil {
		return fmt.Errorf("encode mesh service endpoints: %w", err)
	}
	return quartermasterdb.New(s.db).StoreMeshNodeConfig(ctx, quartermasterdb.StoreMeshNodeConfigParams{
		NodeID: cfg.NodeID, ClusterID: cfg.ClusterID, MeshRevision: cfg.MeshRevision,
		TopologySourceHash: cfg.TopologySourceHash, WireguardIP: cfg.WireguardIP,
		WireguardPort: cfg.WireguardPort, PeersJSON: string(peersJSON), ServiceEndpointsJSON: string(endpointsJSON),
	})
}

func (s *QuartermasterServer) runMeshTopologyConfigWarmer(ctx context.Context) {
	s.warmMeshTopologyConfigs(ctx)
	ticker := time.NewTicker(meshTopologyWarmInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.warmMeshTopologyConfigs(ctx)
		}
	}
}

func (s *QuartermasterServer) warmMeshTopologyConfigs(ctx context.Context) {
	revision, claimed, err := s.claimMeshTopologyWarm(ctx)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to claim mesh topology warm")
		return
	}
	if !claimed {
		return
	}
	topologySourceHash := meshTopologySourceHash(revision)
	count, warmErr := s.refreshActiveMeshNodeConfigs(ctx, topologySourceHash)
	if warmErr != nil {
		if finishErr := s.finishMeshTopologyWarm(ctx, revision, false); finishErr != nil {
			s.logger.WithError(finishErr).WithField("topology_source_hash", topologySourceHash).Warn("Failed to release mesh topology warm claim")
		}
		s.logger.WithError(warmErr).WithFields(logging.Fields{
			"topology_source_hash": topologySourceHash,
			"refreshed_nodes":      count,
		}).Warn("Mesh topology warm failed")
		return
	}
	if err := s.finishMeshTopologyWarm(ctx, revision, true); err != nil {
		s.logger.WithError(err).WithField("topology_source_hash", topologySourceHash).Warn("Failed to mark mesh topology warm complete")
		return
	}
	s.logger.WithFields(logging.Fields{
		"topology_source_hash": topologySourceHash,
		"refreshed_nodes":      count,
	}).Debug("Mesh topology warm complete")
}

func (s *QuartermasterServer) claimMeshTopologyWarm(ctx context.Context) (int64, bool, error) {
	revision, err := quartermasterdb.New(s.db).ClaimMeshTopologyWarm(ctx, meshTopologyPlannerVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return revision, true, nil
}

func (s *QuartermasterServer) finishMeshTopologyWarm(ctx context.Context, revision int64, success bool) error {
	if success {
		return quartermasterdb.New(s.db).CompleteMeshTopologyWarm(ctx, revision, meshTopologyPlannerVersion)
	}
	return quartermasterdb.New(s.db).ReleaseMeshTopologyWarm(ctx)
}

func (s *QuartermasterServer) refreshActiveMeshNodeConfigs(ctx context.Context, topologySourceHash string) (int, error) {
	rows, err := quartermasterdb.New(s.db).ListActiveMeshNodes(ctx)
	if err != nil {
		return 0, err
	}

	var failures []string
	refreshed := 0
	for _, node := range rows {
		cfg, _, buildErr := s.buildMeshNodeConfig(ctx, node.NodeID, node.ClusterID, node.WireguardIP, node.WireguardPort, topologySourceHash, nil)
		if buildErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", node.NodeID, buildErr))
			continue
		}
		if storeErr := s.storeMeshNodeConfig(ctx, cfg); storeErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", node.NodeID, storeErr))
			continue
		}
		refreshed++
	}
	if len(failures) > 0 {
		return refreshed, fmt.Errorf("refresh mesh configs: %s", strings.Join(failures, "; "))
	}
	return refreshed, nil
}

func (s *QuartermasterServer) buildMeshNodeConfig(ctx context.Context, nodeID, clusterID, wireguardIP string, wireguardPort int32, topologySourceHash string, record meshPhaseRecorder) (meshNodeConfig, int, error) {
	phaseStarted := time.Now()
	dnsRequired, peerRequired, globalPeerRequired, infraRequired, reqErr := s.meshServiceRequirements(ctx, nodeID)
	recordMeshPhase(record, "service_requirements", phaseStarted)
	if reqErr != nil {
		return meshNodeConfig{}, 0, fmt.Errorf("mesh service requirements unavailable: %w", reqErr)
	}
	phaseStarted = time.Now()
	serviceEndpoints, requiredPeerNodeIDs, endpointErr := s.collectMeshServiceEndpoints(ctx, clusterID, nodeID, dnsRequired, peerRequired, globalPeerRequired)
	recordMeshPhase(record, "service_endpoints", phaseStarted)
	if endpointErr != nil {
		return meshNodeConfig{}, 0, fmt.Errorf("mesh service endpoints unavailable: %w", endpointErr)
	}
	phaseStarted = time.Now()
	infraPeers, infraErr := s.collectInfraPeerNodeIDs(ctx, clusterID, nodeID, infraRequired)
	recordMeshPhase(record, "infra_peers", phaseStarted)
	if infraErr != nil {
		return meshNodeConfig{}, 0, fmt.Errorf("mesh infra peers unavailable: %w", infraErr)
	}
	for peerNodeID := range infraPeers {
		requiredPeerNodeIDs[peerNodeID] = struct{}{}
	}
	phaseStarted = time.Now()
	reciprocalPeers, reciprocalErr := s.collectReciprocalServicePeerNodeIDs(ctx, clusterID, nodeID)
	recordMeshPhase(record, "reciprocal_peers", phaseStarted)
	if reciprocalErr != nil {
		return meshNodeConfig{}, 0, fmt.Errorf("mesh reciprocal peers unavailable: %w", reciprocalErr)
	}
	for peerNodeID := range reciprocalPeers {
		requiredPeerNodeIDs[peerNodeID] = struct{}{}
	}
	requiredPeerCount := len(requiredPeerNodeIDs)

	phaseStarted = time.Now()
	rows, err := quartermasterdb.New(s.db).ListMeshPeerCandidates(ctx, quartermasterdb.ListMeshPeerCandidatesParams{
		NodeID: nodeID, ClusterID: clusterID, RequiredNodeIDs: sortedStringKeys(requiredPeerNodeIDs),
	})
	if err != nil {
		recordMeshPhase(record, "peer_query", phaseStarted)
		return meshNodeConfig{}, 0, fmt.Errorf("database error: %w", err)
	}
	excludePeer := func(peerName, reason string, cause error) {
		entry := s.logger.WithFields(logging.Fields{
			"requesting_node_id": nodeID,
			"cluster_id":         clusterID,
			"node_name":          peerName,
			"reason":             reason,
		})
		if cause != nil {
			entry = entry.WithError(cause)
		}
		entry.Warn("Excluding peer from mesh sync")
	}

	var peers []*quartermasterpb.InfrastructurePeer
	for _, row := range rows {
		peer := quartermasterpb.InfrastructurePeer{NodeName: row.NodeName, PublicKey: row.PublicKey}
		if row.ScanErr != nil {
			excludePeer(peer.NodeName, "scan_error", row.ScanErr)
			continue
		}
		endpoint := ""
		if row.ExternalIP.Valid && row.ExternalIP.String != "" {
			endpoint = row.ExternalIP.String
		} else if row.InternalIP.Valid && row.InternalIP.String != "" {
			endpoint = row.InternalIP.String
		}
		if endpoint == "" {
			excludePeer(peer.NodeName, "missing_endpoint", nil)
			continue
		}
		if !row.WireguardIP.Valid {
			excludePeer(peer.NodeName, "missing_wireguard_ip", nil)
			continue
		}
		port := int32(51820)
		if row.ListenPort.Valid && row.ListenPort.Int32 > 0 {
			port = row.ListenPort.Int32
		}
		peer.Endpoint = fmt.Sprintf("%s:%d", endpoint, port)
		peer.AllowedIps = []string{row.WireguardIP.String + "/32"}
		peer.KeepAlive = 25
		peers = append(peers, &peer)
	}
	recordMeshPhase(record, "peer_query", phaseStarted)

	cfg := meshNodeConfig{
		NodeID:             nodeID,
		ClusterID:          clusterID,
		TopologySourceHash: topologySourceHash,
		WireguardIP:        wireguardIP,
		WireguardPort:      wireguardPort,
		Peers:              peers,
		ServiceEndpoints:   serviceEndpoints,
	}
	cfg.MeshRevision = computeMeshRevision(cfg.Peers, cfg.ServiceEndpoints, cfg.WireguardIP, cfg.WireguardPort)
	return cfg, requiredPeerCount, nil
}

func encodeStoredMeshPeers(peers []*quartermasterpb.InfrastructurePeer) ([]byte, error) {
	stored := make([]storedMeshPeer, 0, len(peers))
	for _, peer := range peers {
		if peer == nil {
			continue
		}
		stored = append(stored, storedMeshPeer{
			NodeName:   peer.GetNodeName(),
			PublicKey:  peer.GetPublicKey(),
			Endpoint:   peer.GetEndpoint(),
			AllowedIPs: append([]string(nil), peer.GetAllowedIps()...),
			KeepAlive:  peer.GetKeepAlive(),
		})
	}
	return json.Marshal(stored)
}

func decodeStoredMeshPeers(raw []byte) ([]*quartermasterpb.InfrastructurePeer, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var stored []storedMeshPeer
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, err
	}
	peers := make([]*quartermasterpb.InfrastructurePeer, 0, len(stored))
	for _, peer := range stored {
		peers = append(peers, &quartermasterpb.InfrastructurePeer{
			NodeName:   peer.NodeName,
			PublicKey:  peer.PublicKey,
			Endpoint:   peer.Endpoint,
			AllowedIps: append([]string(nil), peer.AllowedIPs...),
			KeepAlive:  peer.KeepAlive,
		})
	}
	return peers, nil
}

func encodeStoredMeshServiceEndpoints(endpoints map[string]*quartermasterpb.ServiceEndpoints) ([]byte, error) {
	stored := make(map[string]storedMeshServiceEndpoints, len(endpoints))
	for serviceType, endpoint := range endpoints {
		if endpoint == nil {
			continue
		}
		stored[serviceType] = storedMeshServiceEndpoints{IPs: append([]string(nil), endpoint.GetIps()...)}
	}
	return json.Marshal(stored)
}

func decodeStoredMeshServiceEndpoints(raw []byte) (map[string]*quartermasterpb.ServiceEndpoints, error) {
	if len(raw) == 0 {
		return map[string]*quartermasterpb.ServiceEndpoints{}, nil
	}
	var stored map[string]storedMeshServiceEndpoints
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, err
	}
	endpoints := make(map[string]*quartermasterpb.ServiceEndpoints, len(stored))
	for serviceType, endpoint := range stored {
		endpoints[serviceType] = &quartermasterpb.ServiceEndpoints{Ips: append([]string(nil), endpoint.IPs...)}
	}
	return endpoints, nil
}

// computeMeshRevision is a stable hex-sha256 fingerprint of the peer set plus
// this node's own mesh identity. Agents persist it into last_known_mesh.json
// so a reboot can tell whether the managed overlay matches what QM would
// return now.
func computeMeshRevision(peers []*quartermasterpb.InfrastructurePeer, serviceEndpoints map[string]*quartermasterpb.ServiceEndpoints, selfIP string, selfPort int32) string {
	sorted := make([]*quartermasterpb.InfrastructurePeer, len(peers))
	copy(sorted, peers)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].GetPublicKey() < sorted[j].GetPublicKey() })
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%d\n", selfIP, selfPort)
	for _, p := range sorted {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%d\n",
			p.GetPublicKey(), p.GetEndpoint(), strings.Join(p.GetAllowedIps(), ","), p.GetKeepAlive())
	}
	endpointNames := make([]string, 0, len(serviceEndpoints))
	for name := range serviceEndpoints {
		endpointNames = append(endpointNames, name)
	}
	sort.Strings(endpointNames)
	for _, name := range endpointNames {
		ips := append([]string(nil), serviceEndpoints[name].GetIps()...)
		sort.Strings(ips)
		fmt.Fprintf(h, "svc\x00%s\x00%s\n", name, strings.Join(ips, ","))
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:8])
}

// ============================================================================
// SERVICE REGISTRY SERVICE
// ============================================================================

// EnqueueServiceEvent persists a service event from a stateless emitter
// (e.g. Deckhand) into quartermaster.service_event_outbox so the drain
// worker can dispatch it to Decklog with exponential backoff. event.source
// stays as the originating service's name; the dispatcher routes by
// payload, not by which service wrote the row. The event arrives as a
// binary-marshaled helmsmancontrol.ServiceEvent. Returns InvalidArgument when
// those bytes are empty, fail to decode, or carry an empty tenant_id.
func (s *QuartermasterServer) EnqueueServiceEvent(ctx context.Context, req *quartermasterpb.EnqueueServiceEventRequest) (*quartermasterpb.EnqueueServiceEventResponse, error) {
	raw := req.GetEvent()
	if len(raw) == 0 {
		return nil, status.Error(codes.InvalidArgument, "event is required")
	}
	// The request carries a marshaled helmsmancontrol.ServiceEvent as bytes so
	// quartermasterpb doesn't depend on ipcpb; decode it here.
	event := &ipcpb.ServiceEvent{}
	if err := proto.Unmarshal(raw, event); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode service event: %v", err)
	}
	if event.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "event.tenant_id is required")
	}
	id, err := s.EnqueueServiceEventTx(ctx, s.db, event)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "enqueue service event: %v", err)
	}
	return &quartermasterpb.EnqueueServiceEventResponse{OutboxId: id}, nil
}

// ListServices returns all services in the catalog
func (s *QuartermasterServer) ListServices(ctx context.Context, req *quartermasterpb.ListServicesRequest) (*quartermasterpb.ListServicesResponse, error) {
	rows, err := quartermasterdb.New(s.db).ListServiceCatalog(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	var services []*quartermasterpb.Service
	for _, row := range rows {
		svc := quartermasterpb.Service{Id: row.ID, Name: row.Name, IsActive: row.IsActive}
		if row.ServiceID.Valid {
			svc.ServiceId = row.ServiceID.String
		}
		if row.Plane.Valid {
			svc.Plane = row.Plane.String
		}
		if row.Description.Valid {
			svc.Description = &row.Description.String
		}
		if row.DefaultPort.Valid {
			port := row.DefaultPort.Int32
			svc.DefaultPort = &port
		}
		if row.HealthCheckPath.Valid {
			svc.HealthCheckPath = &row.HealthCheckPath.String
		}
		if row.DockerImage.Valid {
			svc.DockerImage = &row.DockerImage.String
		}
		if row.Version.Valid {
			svc.Version = &row.Version.String
		}
		if len(row.Dependencies) > 0 {
			svc.Dependencies = row.Dependencies
		}
		if len(row.Tags) > 0 {
			// Parse tags as JSON into Struct
			var tagsMap map[string]any
			if err := json.Unmarshal(row.Tags, &tagsMap); err == nil {
				svc.Tags = mapToStruct(tagsMap)
			}
		}
		if row.Type.Valid {
			svc.Type = row.Type.String
		}
		if row.Protocol.Valid {
			svc.Protocol = row.Protocol.String
		}

		svc.CreatedAt = timestamppb.New(row.CreatedAt)
		svc.UpdatedAt = timestamppb.New(row.UpdatedAt)
		services = append(services, &svc)
	}

	return &quartermasterpb.ListServicesResponse{Services: services}, nil
}

// ListClusterServices returns services assigned to a cluster
func (s *QuartermasterServer) ListClusterServices(ctx context.Context, req *quartermasterpb.ListClusterServicesRequest) (*quartermasterpb.ListClusterServicesResponse, error) {
	clusterID := req.GetClusterId()
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}

	rows, err := quartermasterdb.New(s.db).ListClusterServiceAssignments(ctx, clusterID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	var services []*quartermasterpb.ClusterServiceAssignment
	for _, row := range rows {
		svc := quartermasterpb.ClusterServiceAssignment{Id: row.ID, ClusterId: row.ClusterID, ServiceId: row.ServiceID,
			DesiredState: row.DesiredState, DesiredReplicas: row.DesiredReplicas, CurrentReplicas: row.CurrentReplicas}
		if row.ConfigBlob.Valid && row.ConfigBlob.String != "" {
			var configMap map[string]any
			if err := json.Unmarshal([]byte(row.ConfigBlob.String), &configMap); err == nil {
				svc.ConfigBlob = mapToStruct(configMap)
			}
		}
		if row.EnvironmentVars.Valid && row.EnvironmentVars.String != "" {
			var envMap map[string]any
			if err := json.Unmarshal([]byte(row.EnvironmentVars.String), &envMap); err == nil {
				svc.EnvironmentVars = mapToStruct(envMap)
			}
		}
		if row.CPULimit.Valid {
			cpu := row.CPULimit.Float64
			svc.CpuLimit = &cpu
		}
		if row.MemoryLimitMB.Valid {
			mem := row.MemoryLimitMB.Int32
			svc.MemoryLimitMb = &mem
		}
		if row.HealthStatus.Valid {
			svc.HealthStatus = row.HealthStatus.String
		}
		if row.LastDeployed.Valid {
			svc.LastDeployed = timestamppb.New(row.LastDeployed.Time)
		}
		if row.ServiceName.Valid {
			svc.ServiceName = row.ServiceName.String
		}
		if row.ServicePlane.Valid {
			svc.ServicePlane = row.ServicePlane.String
		}

		svc.CreatedAt = timestamppb.New(row.CreatedAt)
		svc.UpdatedAt = timestamppb.New(row.UpdatedAt)
		services = append(services, &svc)
	}

	return &quartermasterpb.ListClusterServicesResponse{
		ClusterId: clusterID,
		Services:  services,
	}, nil
}

// ListServiceInstances returns running service instances
func (s *QuartermasterServer) ListServiceInstances(ctx context.Context, req *quartermasterpb.ListServiceInstancesRequest) (*quartermasterpb.ListServiceInstancesResponse, error) {
	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	filter := quartermasterdb.ServiceInstancePageFilter{ClusterID: req.GetClusterId(), ServiceID: req.GetServiceId(), NodeID: req.GetNodeId(), Backward: params.Direction == pagination.Backward, Limit: params.Limit + 1}
	if params.Cursor != nil {
		filter.CursorTime, filter.CursorID = &params.Cursor.Timestamp, params.Cursor.ID
	}
	rows, total, err := quartermasterdb.New(s.db).ListServiceInstancesPage(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	var instances []*quartermasterpb.ServiceInstance
	for _, row := range rows {
		inst := quartermasterpb.ServiceInstance{Id: row.ID, InstanceId: row.InstanceID, ServiceId: row.ServiceID, ClusterId: row.ClusterID,
			Protocol: row.Protocol, Status: row.Status, HealthStatus: row.HealthStatus,
			Metadata: unmarshalStringMapJSON(row.Metadata), CreatedAt: timestamppb.New(row.CreatedAt), UpdatedAt: timestamppb.New(row.UpdatedAt)}
		if row.NodeID.Valid {
			inst.NodeId = &row.NodeID.String
		}
		if row.Port.Valid {
			inst.Port = &row.Port.Int32
		}
		if row.AdvertiseHost.Valid {
			inst.Host = &row.AdvertiseHost.String
		}
		if row.HealthEndpoint.Valid {
			inst.HealthEndpoint = &row.HealthEndpoint.String
		}
		if row.Version.Valid {
			inst.Version = &row.Version.String
		}
		if row.ProcessID.Valid {
			inst.ProcessId = &row.ProcessID.Int32
		}
		if row.ContainerID.Valid {
			inst.ContainerId = &row.ContainerID.String
		}
		if row.StartedAt.Valid {
			inst.StartedAt = timestamppb.New(row.StartedAt.Time)
		}
		if row.StoppedAt.Valid {
			inst.StoppedAt = timestamppb.New(row.StoppedAt.Time)
		}
		if row.LastHealthCheck.Valid {
			inst.LastHealthCheck = timestamppb.New(row.LastHealthCheck.Time)
		}

		instances = append(instances, &inst)
	}

	// Detect hasMore and trim results
	hasMore := len(instances) > params.Limit
	if hasMore {
		instances = instances[:params.Limit]
	}

	// Reverse results if backward pagination
	if params.Direction == pagination.Backward && len(instances) > 0 {
		for i, j := 0, len(instances)-1; i < j; i, j = i+1, j-1 {
			instances[i], instances[j] = instances[j], instances[i]
		}
	}

	// Build cursors from results
	var startCursor, endCursor string
	if len(instances) > 0 {
		first := instances[0]
		last := instances[len(instances)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.Id)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.Id)
	}

	// Build response with proper hasNextPage/hasPreviousPage
	resp := &quartermasterpb.ListServiceInstancesResponse{
		Instances: instances,
		ClusterId: req.GetClusterId(),
		ServiceId: req.GetServiceId(),
		NodeId:    req.GetNodeId(),
		Pagination: &commonpb.CursorPaginationResponse{
			TotalCount: total,
		},
	}
	if startCursor != "" {
		resp.Pagination.StartCursor = &startCursor
	}
	if endCursor != "" {
		resp.Pagination.EndCursor = &endCursor
	}
	if params.Direction == pagination.Forward {
		resp.Pagination.HasNextPage = hasMore
		resp.Pagination.HasPreviousPage = params.Cursor != nil
	} else {
		resp.Pagination.HasPreviousPage = hasMore
		resp.Pagination.HasNextPage = params.Cursor != nil
	}

	return resp, nil
}

// ListServiceInstancesByType returns the concrete physical instances of a
// service type, each joined to its node for the external IP and stamped with
// the physical endpoint <service>.<node>.infra.<root>. It deliberately does NOT
// route through service_cluster_assignments: cluster_id stays the physical host
// cluster so Navigator can publish one infra A record per running instance/node.
func (s *QuartermasterServer) ListServiceInstancesByType(ctx context.Context, req *quartermasterpb.ListServiceInstancesByTypeRequest) (*quartermasterpb.ListServiceInstancesByTypeResponse, error) {
	// Physical inventory (node IDs + external IPs) is infrastructure-internal:
	// only SERVICE_TOKEN callers (Navigator) may read it, never tenant/user JWTs.
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "ListServiceInstancesByType requires service token auth")
	}
	serviceType := strings.TrimSpace(req.GetServiceType())
	if serviceType == "" {
		return nil, status.Error(codes.InvalidArgument, "service_type required")
	}
	// This RPC exists solely to publish per-instance infra endpoints; reject
	// service types that have no physical-endpoint contract.
	if !dns.IsPhysicalEndpointServiceType(serviceType) {
		return nil, status.Errorf(codes.InvalidArgument, "service_type %q has no physical endpoints", serviceType)
	}

	// Only healthy, operator-active instances are eligible for a public infra
	// A record: a starting/unhealthy gateway must never receive routable DNS.
	// Mirrors listHealthyServiceNodes' health gate.
	var rows []quartermasterdb.PhysicalServiceInstanceRow
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		rows, queryErr = quartermasterdb.New(s.db).ListPhysicalServiceInstances(ctx, quartermasterdb.PhysicalServiceInstanceFilter{
			ServiceType: serviceType, ClusterID: strings.TrimSpace(req.GetClusterId()), StaleThreshold: req.GetStaleThresholdSeconds(),
		})
		return queryErr
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	var instances []*quartermasterpb.PhysicalServiceInstance
	for _, row := range rows {
		inst := quartermasterpb.PhysicalServiceInstance{InstanceId: row.InstanceID, ServiceId: row.ServiceID, ClusterId: row.ClusterID,
			NodeId: row.NodeID, ExternalIp: row.ExternalIP.String, Status: row.Status, HealthStatus: row.HealthStatus, Port: row.Port, Protocol: row.Protocol}
		if host, ok := dns.InfraInstanceFQDN(serviceType, inst.NodeId, s.platformRootDomain); ok {
			inst.PublicInstanceHost = host
		}
		instances = append(instances, &inst)
	}
	return &quartermasterpb.ListServiceInstancesByTypeResponse{
		Instances:   instances,
		ServiceType: serviceType,
	}, nil
}

// ListServicesHealth returns health of all service instances
func (s *QuartermasterServer) ListServicesHealth(ctx context.Context, req *quartermasterpb.ListServicesHealthRequest) (*quartermasterpb.ListServicesHealthResponse, error) {
	return s.getServicesHealth(ctx, "")
}

// GetServiceHealth returns health of specific service instances
func (s *QuartermasterServer) GetServiceHealth(ctx context.Context, req *quartermasterpb.GetServiceHealthRequest) (*quartermasterpb.ListServicesHealthResponse, error) {
	return s.getServicesHealth(ctx, req.GetServiceId())
}

func (s *QuartermasterServer) UpsertTLSBundle(ctx context.Context, req *quartermasterpb.UpsertTLSBundleRequest) (*quartermasterpb.TLSBundleResponse, error) {
	if req.GetBundle() == nil {
		return nil, status.Error(codes.InvalidArgument, "bundle is required")
	}

	bundle := req.GetBundle()
	domains := normalizeStringSlice(bundle.GetDomains())
	if strings.TrimSpace(bundle.GetBundleId()) == "" || strings.TrimSpace(bundle.GetClusterId()) == "" || len(domains) == 0 || strings.TrimSpace(bundle.GetEmail()) == "" {
		return nil, status.Error(codes.InvalidArgument, "bundle_id, cluster_id, domains, and email are required")
	}

	domainsJSON, err := marshalStringSliceJSON(domains)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode domains: %v", err)
	}

	var metadataJSON *string
	if bundle.GetMetadata() != nil {
		encoded, marshalErr := json.Marshal(bundle.GetMetadata().AsMap())
		if marshalErr != nil {
			return nil, status.Errorf(codes.InvalidArgument, "encode metadata: %v", marshalErr)
		}
		value := string(encoded)
		metadataJSON = &value
	}

	row, err := quartermasterdb.New(s.db).UpsertTLSBundle(ctx, quartermasterdb.UpsertTLSBundleParams{
		BundleID: bundle.GetBundleId(), ClusterID: bundle.GetClusterId(), DomainsJSON: domainsJSON,
		Issuer: bundle.GetIssuer(), Email: bundle.GetEmail(), MetadataJSON: metadataJSON,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	return &quartermasterpb.TLSBundleResponse{
		Bundle: &quartermasterpb.TLSBundle{
			Id:        row.ID,
			BundleId:  bundle.GetBundleId(),
			ClusterId: bundle.GetClusterId(),
			Domains:   domains,
			Issuer:    bundle.GetIssuer(),
			Email:     bundle.GetEmail(),
			Metadata:  bundle.GetMetadata(),
			CreatedAt: timestamppb.New(row.CreatedAt),
			UpdatedAt: timestamppb.New(row.UpdatedAt),
		},
	}, nil
}

func (s *QuartermasterServer) ListTLSBundles(ctx context.Context, req *quartermasterpb.ListTLSBundlesRequest) (*quartermasterpb.ListTLSBundlesResponse, error) {
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	filter := quartermasterdb.ResourcePageFilter{ClusterID: req.GetClusterId(), Backward: params.Direction == pagination.Backward, Limit: params.Limit + 1}
	if params.Cursor != nil {
		filter.CursorTime, filter.CursorID = &params.Cursor.Timestamp, params.Cursor.ID
	}
	var rows []quartermasterdb.TLSBundleListRow
	var total int32
	err = database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		rows, total, queryErr = quartermasterdb.New(s.db).ListTLSBundlesPage(ctx, filter)
		return queryErr
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	var bundles []*quartermasterpb.TLSBundle
	for _, row := range rows {
		bundle := quartermasterpb.TLSBundle{Id: row.ID, BundleId: row.BundleID, ClusterId: row.ClusterID,
			Issuer: row.Issuer, Email: row.Email, Domains: unmarshalStringSliceJSON(row.Domains),
			CreatedAt: timestamppb.New(row.CreatedAt), UpdatedAt: timestamppb.New(row.UpdatedAt)}
		if len(row.Metadata) > 0 {
			var metadataMap map[string]any
			if json.Unmarshal(row.Metadata, &metadataMap) == nil {
				bundle.Metadata = mapToStruct(metadataMap)
			}
		}
		bundles = append(bundles, &bundle)
	}

	hasMore := len(bundles) > params.Limit
	if hasMore {
		bundles = bundles[:params.Limit]
	}
	if params.Direction == pagination.Backward && len(bundles) > 0 {
		for i, j := 0, len(bundles)-1; i < j; i, j = i+1, j-1 {
			bundles[i], bundles[j] = bundles[j], bundles[i]
		}
	}

	var startCursor, endCursor string
	if len(bundles) > 0 {
		startCursor = pagination.EncodeCursor(bundles[0].CreatedAt.AsTime(), bundles[0].Id)
		endCursor = pagination.EncodeCursor(bundles[len(bundles)-1].CreatedAt.AsTime(), bundles[len(bundles)-1].Id)
	}

	resp := &quartermasterpb.ListTLSBundlesResponse{
		Bundles:    bundles,
		ClusterId:  req.GetClusterId(),
		Pagination: &commonpb.CursorPaginationResponse{TotalCount: total},
	}
	if startCursor != "" {
		resp.Pagination.StartCursor = &startCursor
	}
	if endCursor != "" {
		resp.Pagination.EndCursor = &endCursor
	}
	if params.Direction == pagination.Forward {
		resp.Pagination.HasNextPage = hasMore
		resp.Pagination.HasPreviousPage = params.Cursor != nil
	} else {
		resp.Pagination.HasPreviousPage = hasMore
		resp.Pagination.HasNextPage = params.Cursor != nil
	}

	return resp, nil
}

func (s *QuartermasterServer) UpsertIngressSite(ctx context.Context, req *quartermasterpb.UpsertIngressSiteRequest) (*quartermasterpb.IngressSiteResponse, error) {
	if req.GetSite() == nil {
		return nil, status.Error(codes.InvalidArgument, "site is required")
	}

	site := req.GetSite()
	domains := normalizeStringSlice(site.GetDomains())
	if strings.TrimSpace(site.GetSiteId()) == "" || strings.TrimSpace(site.GetClusterId()) == "" || strings.TrimSpace(site.GetNodeId()) == "" || len(domains) == 0 || strings.TrimSpace(site.GetTlsBundleId()) == "" || strings.TrimSpace(site.GetKind()) == "" || strings.TrimSpace(site.GetUpstream()) == "" {
		return nil, status.Error(codes.InvalidArgument, "site_id, cluster_id, node_id, domains, tls_bundle_id, kind, and upstream are required")
	}

	domainsJSON, err := marshalStringSliceJSON(domains)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode domains: %v", err)
	}

	var metadataJSON *string
	if site.GetMetadata() != nil {
		encoded, marshalErr := json.Marshal(site.GetMetadata().AsMap())
		if marshalErr != nil {
			return nil, status.Errorf(codes.InvalidArgument, "encode metadata: %v", marshalErr)
		}
		value := string(encoded)
		metadataJSON = &value
	}

	row, err := quartermasterdb.New(s.db).UpsertIngressSite(ctx, quartermasterdb.UpsertIngressSiteParams{
		SiteID: site.GetSiteId(), ClusterID: site.GetClusterId(), NodeID: site.GetNodeId(), DomainsJSON: domainsJSON,
		TLSBundleID: site.GetTlsBundleId(), Kind: site.GetKind(), Upstream: site.GetUpstream(), MetadataJSON: metadataJSON,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	return &quartermasterpb.IngressSiteResponse{
		Site: &quartermasterpb.IngressSite{
			Id:          row.ID,
			SiteId:      site.GetSiteId(),
			ClusterId:   site.GetClusterId(),
			NodeId:      site.GetNodeId(),
			Domains:     domains,
			TlsBundleId: site.GetTlsBundleId(),
			Kind:        site.GetKind(),
			Upstream:    site.GetUpstream(),
			Metadata:    site.GetMetadata(),
			CreatedAt:   timestamppb.New(row.CreatedAt),
			UpdatedAt:   timestamppb.New(row.UpdatedAt),
		},
	}, nil
}

func (s *QuartermasterServer) ListIngressSites(ctx context.Context, req *quartermasterpb.ListIngressSitesRequest) (*quartermasterpb.ListIngressSitesResponse, error) {
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	filter := quartermasterdb.ResourcePageFilter{ClusterID: req.GetClusterId(), NodeID: req.GetNodeId(), Backward: params.Direction == pagination.Backward, Limit: params.Limit + 1}
	if params.Cursor != nil {
		filter.CursorTime, filter.CursorID = &params.Cursor.Timestamp, params.Cursor.ID
	}
	var rows []quartermasterdb.IngressSiteListRow
	var total int32
	err = database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		rows, total, queryErr = quartermasterdb.New(s.db).ListIngressSitesPage(ctx, filter)
		return queryErr
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	var sites []*quartermasterpb.IngressSite
	for _, row := range rows {
		site := quartermasterpb.IngressSite{Id: row.ID, SiteId: row.SiteID, ClusterId: row.ClusterID, NodeId: row.NodeID,
			TlsBundleId: row.TLSBundleID, Kind: row.Kind, Upstream: row.Upstream,
			CreatedAt: timestamppb.New(row.CreatedAt), UpdatedAt: timestamppb.New(row.UpdatedAt)}
		// Fail closed on a malformed domains row: silently nil-ing it would make
		// Navigator's physical-endpoint gate read a provisioned site as having no
		// matching domain, and prune a valid infra A record.
		if len(row.Domains) > 0 {
			domains, err := decodeIngressDomainsStrict(row.Domains)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "decode ingress site domains: %v", err)
			}
			site.Domains = domains
		}
		if len(row.Metadata) > 0 {
			var metadataMap map[string]any
			if json.Unmarshal(row.Metadata, &metadataMap) == nil {
				site.Metadata = mapToStruct(metadataMap)
			}
		}
		sites = append(sites, &site)
	}

	hasMore := len(sites) > params.Limit
	if hasMore {
		sites = sites[:params.Limit]
	}
	if params.Direction == pagination.Backward && len(sites) > 0 {
		for i, j := 0, len(sites)-1; i < j; i, j = i+1, j-1 {
			sites[i], sites[j] = sites[j], sites[i]
		}
	}

	var startCursor, endCursor string
	if len(sites) > 0 {
		startCursor = pagination.EncodeCursor(sites[0].CreatedAt.AsTime(), sites[0].Id)
		endCursor = pagination.EncodeCursor(sites[len(sites)-1].CreatedAt.AsTime(), sites[len(sites)-1].Id)
	}

	resp := &quartermasterpb.ListIngressSitesResponse{
		Sites:      sites,
		ClusterId:  req.GetClusterId(),
		NodeId:     req.GetNodeId(),
		Pagination: &commonpb.CursorPaginationResponse{TotalCount: total},
	}
	if startCursor != "" {
		resp.Pagination.StartCursor = &startCursor
	}
	if endCursor != "" {
		resp.Pagination.EndCursor = &endCursor
	}
	if params.Direction == pagination.Forward {
		resp.Pagination.HasNextPage = hasMore
		resp.Pagination.HasPreviousPage = params.Cursor != nil
	} else {
		resp.Pagination.HasPreviousPage = hasMore
		resp.Pagination.HasNextPage = params.Cursor != nil
	}

	return resp, nil
}

func (s *QuartermasterServer) getServicesHealth(ctx context.Context, serviceID string) (*quartermasterpb.ListServicesHealthResponse, error) {
	rows, err := quartermasterdb.New(s.db).ListServiceHealth(ctx, serviceID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	var instances []*quartermasterpb.ServiceInstanceHealth
	for _, row := range rows {
		inst := quartermasterpb.ServiceInstanceHealth{InstanceId: row.InstanceID, ServiceId: row.ServiceID,
			ClusterId: row.ClusterID, Protocol: row.Protocol, Port: row.Port, Status: row.Status}
		if row.Host.Valid {
			inst.Host = &row.Host.String
		}
		if row.HealthEndpoint.Valid {
			inst.HealthEndpoint = &row.HealthEndpoint.String
		}
		if row.LastHealthCheck.Valid {
			inst.LastHealthCheck = timestamppb.New(row.LastHealthCheck.Time)
		}

		instances = append(instances, &inst)
	}

	return &quartermasterpb.ListServicesHealthResponse{Instances: instances}, nil
}

// ============================================================================
// HELPER FUNCTIONS
// ============================================================================

const (
	eventTenantCreated                = "tenant_created"
	eventTenantUpdated                = "tenant_updated"
	eventTenantDeleted                = "tenant_deleted"
	eventTenantClusterAssigned        = "tenant_cluster_assigned"
	eventTenantClusterUnassigned      = "tenant_cluster_unassigned"
	eventClusterCreated               = "cluster_created"
	eventClusterUpdated               = "cluster_updated"
	eventClusterDeleted               = "cluster_deleted"
	eventClusterInviteCreated         = "cluster_invite_created"
	eventClusterInviteRevoked         = "cluster_invite_revoked"
	eventClusterSubscriptionRequested = "cluster_subscription_requested"
	eventClusterSubscriptionApproved  = "cluster_subscription_approved"
	eventClusterSubscriptionRejected  = "cluster_subscription_rejected"
)

// emitServiceEvent enqueues a service event into
// quartermaster.service_event_outbox. The drain worker (started in
// NewGRPCServer) dispatches pending rows to Decklog with exponential
// backoff. Replaces the previous async fire-and-forget SendServiceEvent
// path so Decklog outage no longer drops tenant/cluster mutation events.
// Best-effort durability: helper uses its own short tx for the INSERT
// (not strictly atomic with upstream state mutation). For strict
// atomicity, callers that hold a tx can switch to
// EnqueueServiceEventTx(ctx, tx, event).
func (s *QuartermasterServer) emitServiceEvent(ctx context.Context, event *ipcpb.ServiceEvent) {
	if ctxkeys.IsDemoMode(ctx) {
		return
	}
	s.enqueueServiceEvent(ctx, event)
}

func (s *QuartermasterServer) buildTenantEvent(eventType, tenantID, userID string, changedFields []string, attribution *commonpb.SignupAttribution) *ipcpb.ServiceEvent {
	payload := &ipcpb.TenantEvent{
		TenantId:      tenantID,
		ChangedFields: changedFields,
		Attribution:   attribution,
	}
	return &ipcpb.ServiceEvent{
		EventType:    eventType,
		Timestamp:    timestamppb.Now(),
		Source:       "quartermaster",
		TenantId:     tenantID,
		UserId:       userID,
		ResourceType: "tenant",
		ResourceId:   tenantID,
		Payload:      &ipcpb.ServiceEvent_TenantEvent{TenantEvent: payload},
	}
}

func (s *QuartermasterServer) emitTenantEvent(ctx context.Context, eventType, tenantID, userID string, changedFields []string, attribution *commonpb.SignupAttribution) {
	s.emitServiceEvent(ctx, s.buildTenantEvent(eventType, tenantID, userID, changedFields, attribution))
}

// emitTenantEventTx writes the tenant-event outbox row inside the caller's
// transaction. Use when the state mutation that justifies the event runs in
// the same tx — guarantees the mutation and the event become durable
// atomically. Falls back to the short-tx variant on tx==nil.
func (s *QuartermasterServer) emitTenantEventTx(ctx context.Context, tx *sql.Tx, eventType, tenantID, userID string, changedFields []string, attribution *commonpb.SignupAttribution) error {
	event := s.buildTenantEvent(eventType, tenantID, userID, changedFields, attribution)
	if tx == nil {
		s.emitServiceEvent(ctx, event)
		return nil
	}
	if ctxkeys.IsDemoMode(ctx) || event.GetTenantId() == "" {
		return nil
	}
	_, err := s.EnqueueServiceEventTx(ctx, tx, event)
	return err
}

func (s *QuartermasterServer) buildClusterEvent(eventType, tenantID, userID, clusterID, resourceType, resourceID, inviteID, subscriptionID, reason string) *ipcpb.ServiceEvent {
	payload := &ipcpb.ClusterEvent{
		ClusterId:      clusterID,
		TenantId:       tenantID,
		InviteId:       inviteID,
		SubscriptionId: subscriptionID,
		Reason:         reason,
	}
	return &ipcpb.ServiceEvent{
		EventType:    eventType,
		Timestamp:    timestamppb.Now(),
		Source:       "quartermaster",
		TenantId:     tenantID,
		UserId:       userID,
		ResourceType: resourceType,
		ResourceId:   resourceID,
		Payload:      &ipcpb.ServiceEvent_ClusterEvent{ClusterEvent: payload},
	}
}

func (s *QuartermasterServer) emitClusterEvent(ctx context.Context, eventType, tenantID, userID, clusterID, resourceType, resourceID, inviteID, subscriptionID, reason string) {
	s.emitServiceEvent(ctx, s.buildClusterEvent(eventType, tenantID, userID, clusterID, resourceType, resourceID, inviteID, subscriptionID, reason))
}

// emitClusterEventTx writes the cluster-event outbox row inside the
// caller's transaction. See emitTenantEventTx for semantics.
func (s *QuartermasterServer) emitClusterEventTx(ctx context.Context, tx *sql.Tx, eventType, tenantID, userID, clusterID, resourceType, resourceID, inviteID, subscriptionID, reason string) error {
	event := s.buildClusterEvent(eventType, tenantID, userID, clusterID, resourceType, resourceID, inviteID, subscriptionID, reason)
	if tx == nil {
		s.emitServiceEvent(ctx, event)
		return nil
	}
	if ctxkeys.IsDemoMode(ctx) || event.GetTenantId() == "" {
		return nil
	}
	_, err := s.EnqueueServiceEventTx(ctx, tx, event)
	return err
}

func (s *QuartermasterServer) queryCluster(ctx context.Context, clusterID string) (*quartermasterpb.InfrastructureCluster, error) {
	row, err := quartermasterdb.New(s.db).GetInfrastructureCluster(ctx, clusterID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "cluster not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !row.DeploymentModel.Valid || !row.MaxConcurrentStreams.Valid || !row.MaxConcurrentViewers.Valid ||
		!row.MaxBandwidthMbps.Valid || !row.HealthStatus.Valid || !row.IsActive.Valid ||
		!row.IsDefaultCluster.Valid || !row.IsPlatformOfficial.Valid || !row.CreatedAt.Valid ||
		!row.UpdatedAt.Valid || !row.Visibility.Valid || !row.RequiresApproval.Valid {
		return nil, status.Error(codes.Internal, "database error: required cluster field is NULL")
	}
	cluster := &quartermasterpb.InfrastructureCluster{
		Id: row.ID, ClusterId: row.ClusterID, ClusterName: row.ClusterName, ClusterType: row.ClusterType,
		DeploymentModel: row.DeploymentModel.String, BaseUrl: row.BaseUrl, KafkaBrokers: row.KafkaBrokers,
		MaxConcurrentStreams: row.MaxConcurrentStreams.Int32, MaxConcurrentViewers: row.MaxConcurrentViewers.Int32,
		MaxBandwidthMbps: row.MaxBandwidthMbps.Int32, HealthStatus: row.HealthStatus.String,
		IsActive: row.IsActive.Bool, IsDefaultCluster: row.IsDefaultCluster.Bool,
		IsPlatformOfficial: row.IsPlatformOfficial.Bool, PublicTopology: row.PublicTopology,
		CreatedAt: timestamppb.New(row.CreatedAt.Time), UpdatedAt: timestamppb.New(row.UpdatedAt.Time),
		Visibility: visibilityStringToProto(row.Visibility.String), RequiresApproval: row.RequiresApproval.Bool,
		S3Bucket: row.S3Bucket, S3Endpoint: row.S3Endpoint, S3Region: row.S3Region, S3Prefix: row.S3Prefix,
		S3PrefixPresent: row.S3PrefixPresent, RegionId: row.RegionID, CellId: row.CellID,
		ClusterClass: row.ClusterClass, ControlCellId: row.ControlCellID,
		EligibleServingCellIds: row.EligibleServingCellIds,
	}
	if row.OwnerTenantID.Valid {
		cluster.OwnerTenantId = &row.OwnerTenantID.String
	}
	if row.DatabaseUrl.Valid {
		cluster.DatabaseUrl = &row.DatabaseUrl.String
	}
	if row.ShortDescription.Valid {
		cluster.ShortDescription = &row.ShortDescription.String
	}
	return cluster, nil
}

// visibilityStringToProto converts DB string to proto enum
func visibilityStringToProto(s string) quartermasterpb.ClusterVisibility {
	switch s {
	case "public":
		return quartermasterpb.ClusterVisibility_CLUSTER_VISIBILITY_PUBLIC
	case "unlisted":
		return quartermasterpb.ClusterVisibility_CLUSTER_VISIBILITY_UNLISTED
	case "private":
		return quartermasterpb.ClusterVisibility_CLUSTER_VISIBILITY_PRIVATE
	default:
		return quartermasterpb.ClusterVisibility_CLUSTER_VISIBILITY_PRIVATE
	}
}

// visibilityProtoToString converts proto enum to DB string
func visibilityProtoToString(v quartermasterpb.ClusterVisibility) string {
	switch v {
	case quartermasterpb.ClusterVisibility_CLUSTER_VISIBILITY_PUBLIC:
		return "public"
	case quartermasterpb.ClusterVisibility_CLUSTER_VISIBILITY_UNLISTED:
		return "unlisted"
	case quartermasterpb.ClusterVisibility_CLUSTER_VISIBILITY_PRIVATE:
		return "private"
	default:
		return "private"
	}
}

// Note: Pricing model helpers moved to Purser service

// subscriptionStatusStringToProto converts DB string to proto enum
func subscriptionStatusStringToProto(s string) quartermasterpb.ClusterSubscriptionStatus {
	switch s {
	case "pending_approval":
		return quartermasterpb.ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_PENDING_APPROVAL
	case "active":
		return quartermasterpb.ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_ACTIVE
	case "suspended":
		return quartermasterpb.ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_SUSPENDED
	case "rejected":
		return quartermasterpb.ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_REJECTED
	default:
		return quartermasterpb.ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_UNSPECIFIED
	}
}

// loadTakenMeshIPs returns the set of wireguard_ip values currently allocated
// within a cluster, keyed by dotted-quad string. Used by BootstrapInfrastructureNode
// to avoid colliding with already-assigned mesh addresses when allocating
// a new one for an enrolling node.
func loadTakenMeshIPs(ctx context.Context, tx *sql.Tx, clusterID string) (map[string]struct{}, error) {
	ips, err := quartermasterdb.New(tx).ListTakenMeshIPs(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	taken := make(map[string]struct{}, len(ips))
	for _, ip := range ips {
		taken[ip] = struct{}{}
	}
	return taken, nil
}

func generateSecureToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func hashBootstrapToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func tokenPrefix(token string) string {
	if len(token) <= 12 {
		return token
	}
	return token[:12] + "..."
}

// ============================================================================
// CLUSTER MARKETPLACE RPCs
// ============================================================================

// ListMarketplaceClusters returns clusters visible to the requesting tenant
func (s *QuartermasterServer) ListMarketplaceClusters(ctx context.Context, req *quartermasterpb.ListMarketplaceClustersRequest) (*quartermasterpb.ListMarketplaceClustersResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		tenantID = middleware.GetTenantID(ctx)
	}
	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	filter := quartermasterdb.MarketplacePageFilter{TenantID: tenantID, Backward: params.Direction == pagination.Backward, Limit: params.Limit + 1}
	if params.Cursor != nil {
		filter.CursorTime, filter.CursorID = &params.Cursor.Timestamp, params.Cursor.ID
	}
	rows, total, err := quartermasterdb.New(s.db).ListMarketplaceClustersPage(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	type entryWithCursor struct {
		entry     *quartermasterpb.MarketplaceClusterEntry
		createdAt time.Time
		clusterID string
	}
	var entries []entryWithCursor
	for _, row := range rows {
		entry := quartermasterpb.MarketplaceClusterEntry{ClusterId: row.ClusterID, ClusterName: row.ClusterName,
			Visibility: visibilityStringToProto(row.Visibility), RequiresApproval: row.RequiresApproval,
			MaxConcurrentStreams: row.MaxConcurrentStreams, MaxConcurrentViewers: row.MaxConcurrentViewers,
			IsSubscribed: row.IsSubscribed, CreatedAt: timestamppb.New(row.CreatedAt)}
		if row.ShortDescription.Valid {
			entry.ShortDescription = &row.ShortDescription.String
		}
		if row.OwnerName.Valid {
			entry.OwnerName = &row.OwnerName.String
		}
		if row.SubscriptionStatus.Valid && row.SubscriptionStatus.String != "" {
			entry.SubscriptionStatus = subscriptionStatusStringToProto(row.SubscriptionStatus.String)
		}
		entries = append(entries, entryWithCursor{entry: &entry, createdAt: row.CreatedAt, clusterID: row.ClusterID})
	}

	// Determine pagination info
	resultsLen := len(entries)
	if resultsLen > params.Limit {
		entries = entries[:params.Limit]
	}

	// Reverse results for backward pagination
	if params.Direction == pagination.Backward {
		slices.Reverse(entries)
	}

	// Build cursors and extract entries
	var clusters []*quartermasterpb.MarketplaceClusterEntry
	var startCursor, endCursor string
	for _, e := range entries {
		clusters = append(clusters, e.entry)
	}
	if len(entries) > 0 {
		first := entries[0]
		last := entries[len(entries)-1]
		startCursor = pagination.EncodeCursor(first.createdAt, first.clusterID)
		endCursor = pagination.EncodeCursor(last.createdAt, last.clusterID)
	}

	return &quartermasterpb.ListMarketplaceClustersResponse{
		Clusters:   clusters,
		Pagination: pagination.BuildResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
	}, nil
}

// GetMarketplaceCluster returns a single marketplace cluster entry
func (s *QuartermasterServer) GetMarketplaceCluster(ctx context.Context, req *quartermasterpb.GetMarketplaceClusterRequest) (*quartermasterpb.MarketplaceClusterEntry, error) {
	clusterID := req.GetClusterId()
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}

	tenantID := req.GetTenantId()
	if tenantID == "" {
		tenantID = middleware.GetTenantID(ctx)
	}
	row, err := quartermasterdb.New(s.db).GetMarketplaceCluster(ctx, clusterID, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "cluster not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	entry := quartermasterpb.MarketplaceClusterEntry{ClusterId: row.ClusterID, ClusterName: row.ClusterName,
		Visibility: visibilityStringToProto(row.Visibility), RequiresApproval: row.RequiresApproval,
		MaxConcurrentStreams: row.MaxConcurrentStreams, MaxConcurrentViewers: row.MaxConcurrentViewers,
		IsSubscribed: row.IsSubscribed, CreatedAt: timestamppb.New(row.CreatedAt)}
	if row.ShortDescription.Valid {
		entry.ShortDescription = &row.ShortDescription.String
	}
	if row.OwnerName.Valid {
		entry.OwnerName = &row.OwnerName.String
	}
	if row.SubscriptionStatus.Valid && row.SubscriptionStatus.String != "" {
		entry.SubscriptionStatus = subscriptionStatusStringToProto(row.SubscriptionStatus.String)
	}

	return &entry, nil
}

// UpdateClusterMarketplace updates marketplace settings for a cluster (owner only)
func (s *QuartermasterServer) UpdateClusterMarketplace(ctx context.Context, req *quartermasterpb.UpdateClusterMarketplaceRequest) (*quartermasterpb.ClusterResponse, error) {
	clusterID := req.GetClusterId()
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}

	tenantID := req.GetTenantId()
	if tenantID == "" {
		tenantID = middleware.GetTenantID(ctx)
	}
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}
	userID := middleware.GetUserID(ctx)

	// Verify ownership
	owner, err := quartermasterdb.New(s.db).GetMarketplaceOwner(ctx, clusterID, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "cluster not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Only owner can update marketplace settings (unless admin/provider with platform cluster)
	if !owner.OwnerTenantID.Valid || owner.OwnerTenantID.String != tenantID {
		return nil, status.Error(codes.PermissionDenied, "only cluster owner can update marketplace settings")
	}

	// Build update query
	update := quartermasterdb.UpdateMarketplaceParams{ClusterID: clusterID}

	if req.Visibility != nil {
		// Non-providers can only set private visibility
		if !owner.IsProvider && *req.Visibility != quartermasterpb.ClusterVisibility_CLUSTER_VISIBILITY_PRIVATE {
			return nil, status.Error(codes.PermissionDenied, "only providers can set public/unlisted visibility")
		}
		newVisibility := visibilityProtoToString(*req.Visibility)
		update.Visibility = &newVisibility
		// Keep cluster_class aligned with the new visibility. Platform-
		// official capacity never changes class through this surface. For
		// owner-owned clusters: private → tenant_private; public/unlisted →
		// third_party_marketplace. Plan-tier admission keys off cluster_class
		// (free→platform_official, premium→+marketplace, enterprise→+private)
		// so a class that drifts from visibility silently mis-routes a paid
		// cluster onto the wrong plan bucket. Per the three-cluster-kinds
		// invariant in pkg/database/sql/schema/quartermaster.sql.
		var newClass string
		switch newVisibility {
		case "private":
			newClass = "tenant_private"
		case "public", "unlisted":
			newClass = "third_party_marketplace"
		}
		if newClass != "" {
			update.ClusterClass = &newClass
		}
	}
	// Note: Pricing fields are managed via Purser, not Quartermaster
	if req.RequiresApproval != nil {
		update.RequiresApproval = req.RequiresApproval
	}
	if req.ShortDescription != nil {
		update.ShortDescription = req.ShortDescription
	}

	if update.Visibility == nil && update.RequiresApproval == nil && update.ShortDescription == nil {
		return nil, status.Error(codes.InvalidArgument, "no fields to update")
	}

	// Marketplace UPDATE + cluster_updated outbox emit ride one tx so the
	// dashboard view and downstream consumers can't see a divergent state
	// after a crash between mutation and emit.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if err = quartermasterdb.New(tx).UpdateMarketplace(ctx, update); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update cluster: %v", err)
	}

	if enqErr := s.emitClusterEventTx(ctx, tx, eventClusterUpdated, tenantID, userID, clusterID, "cluster", clusterID, "", "", ""); enqErr != nil {
		return nil, status.Errorf(codes.Internal, "enqueue cluster_updated: %v", enqErr)
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return nil, status.Errorf(codes.Internal, "commit cluster update: %v", commitErr)
	}

	cluster, err := s.queryCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}

	return &quartermasterpb.ClusterResponse{Cluster: cluster}, nil
}

// GetClusterMetadataBatch returns metadata for multiple clusters (for Gateway enrichment).
// Used by Gateway to enrich Purser's marketplace pricing data with cluster operational info.
func (s *QuartermasterServer) GetClusterMetadataBatch(ctx context.Context, req *quartermasterpb.GetClusterMetadataBatchRequest) (*quartermasterpb.GetClusterMetadataBatchResponse, error) {
	clusterIDs := req.GetClusterIds()
	if len(clusterIDs) == 0 {
		return &quartermasterpb.GetClusterMetadataBatchResponse{Clusters: map[string]*quartermasterpb.ClusterMetadata{}}, nil
	}

	requestingTenantID := req.GetRequestingTenantId()

	rows, err := quartermasterdb.New(s.db).ListClusterMetadata(ctx, requestingTenantID, clusterIDs)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	clusters := make(map[string]*quartermasterpb.ClusterMetadata)
	for _, row := range rows {
		meta := quartermasterpb.ClusterMetadata{ClusterId: row.ClusterID, ClusterName: row.ClusterName,
			Visibility: row.Visibility, RequiresApproval: row.RequiresApproval,
			MaxConcurrentStreams: row.MaxConcurrentStreams, MaxConcurrentViewers: row.MaxConcurrentViewers,
			IsSubscribed: row.IsSubscribed, SubscriptionStatus: row.SubscriptionStatus, IsPlatformOfficial: row.IsPlatformOfficial}
		if row.ShortDescription.Valid {
			meta.ShortDescription = &row.ShortDescription.String
		}
		if row.OwnerName.Valid {
			meta.OwnerName = &row.OwnerName.String
		}
		clusters[row.ClusterID] = &meta
	}

	return &quartermasterpb.GetClusterMetadataBatchResponse{Clusters: clusters}, nil
}

// CreatePrivateCluster creates a private cluster for self-hosted edge
func (s *QuartermasterServer) CreatePrivateCluster(ctx context.Context, req *quartermasterpb.CreatePrivateClusterRequest) (*quartermasterpb.CreatePrivateClusterResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		tenantID = middleware.GetTenantID(ctx)
	}
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	userID := middleware.GetUserID(ctx)
	clusterName := req.GetClusterName()
	if clusterName == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_name required")
	}

	// Check tenant's cluster ownership limit
	queries := quartermasterdb.New(s.db)
	ownership, err := queries.GetTenantClusterOwnershipLimit(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "tenant not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !ownership.MaxOwnedClusters.Valid || !ownership.IsProvider.Valid {
		return nil, status.Error(codes.Internal, "database error: required tenant ownership field is NULL")
	}

	// Non-providers are limited to max_owned_clusters (default 1)
	if !ownership.IsProvider.Bool && ownership.CurrentOwnedClusters >= int64(ownership.MaxOwnedClusters.Int32) {
		return nil, status.Errorf(codes.ResourceExhausted, "tenant has reached maximum owned clusters limit (%d)", ownership.MaxOwnedClusters.Int32)
	}

	// Generate cluster ID from name (sanitized)
	clusterID := strings.ToLower(strings.ReplaceAll(clusterName, " ", "-"))
	suffix, err := generateSecureToken(4)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate cluster ID suffix: %v", err)
	}
	clusterID = fmt.Sprintf("%s-%s", clusterID, suffix)

	id := uuid.New().String()
	now := time.Now()
	requestedRegion := strings.TrimSpace(req.GetRegion())

	controlCell, err := s.selectFoghornControlCell(ctx, "", requestedRegion, "")
	if err != nil {
		return nil, err
	}
	regionForRow := strings.TrimSpace(controlCell.regionID)

	// Atomicity contract: every row that makes a private cluster usable —
	// the cluster row itself, the owner's tenant_cluster_access grant, the
	// Foghorn assignment, and the bootstrap token — must commit together.
	// Otherwise a Foghorn-assignment failure leaves a tenant_private cluster
	// the owner can't actually use, or a token failure leaves a cluster
	// without enrollment material. The service-event outbox emits ride in
	// the same tx so the downstream cache invalidations also can't drop.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	txQueries := quartermasterdb.New(tx)
	if err = txQueries.CreatePrivateInfrastructureCluster(ctx, quartermasterdb.CreatePrivateInfrastructureClusterParams{
		ID: id, ClusterID: clusterID, ClusterName: clusterName, OwnerTenantID: tenantID,
		BaseURL: strings.TrimSpace(controlCell.baseURL), ShortDescription: req.ShortDescription,
		RegionID: regionForRow, ControlCellID: controlCell.controlCellID, CreatedAt: now,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create cluster: %v", err)
	}

	if err = txQueries.GrantPrivateClusterOwnerAccess(ctx, quartermasterdb.GrantPrivateClusterOwnerAccessParams{
		TenantID: tenantID, ClusterID: clusterID,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to auto-subscribe owner to cluster: %v", err)
	}

	// Junction row binding the chosen Foghorn to this private cluster.
	// Without it, ConfigSeed delivery has no service_instance to dial.
	if err = txQueries.AssignFoghornToPrivateCluster(ctx, quartermasterdb.AssignFoghornToPrivateClusterParams{
		ServiceInstanceID: controlCell.instanceID, ClusterID: clusterID,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to assign Foghorn to cluster: %v", err)
	}

	// Create a bootstrap token for edge node registration
	tokenID := uuid.New().String()
	token, err := generateSecureToken(32)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate token: %v", err)
	}
	expiresAt := now.Add(30 * 24 * time.Hour) // 30 days

	if err = txQueries.CreateEdgeBootstrapTokenRecord(ctx, quartermasterdb.CreateEdgeBootstrapTokenRecordParams{
		ID: tokenID, TokenHash: hashBootstrapToken(token), TokenPrefix: tokenPrefix(token),
		Name: fmt.Sprintf("Bootstrap token for %s", clusterName), TenantID: tenantID,
		ClusterID: validString(clusterID), ExpiresAt: expiresAt,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create bootstrap token: %v", err)
	}

	if enqErr := s.emitClusterEventTx(ctx, tx, eventClusterCreated, tenantID, userID, clusterID, "cluster", clusterID, "", "", ""); enqErr != nil {
		return nil, status.Errorf(codes.Internal, "enqueue cluster_created: %v", enqErr)
	}
	if enqErr := s.emitClusterEventTx(ctx, tx, eventTenantClusterAssigned, tenantID, userID, clusterID, "cluster", clusterID, "", "", ""); enqErr != nil {
		return nil, status.Errorf(codes.Internal, "enqueue tenant_cluster_assigned: %v", enqErr)
	}
	if enqErr := s.enqueueTenantAliasEnsureTx(ctx, tx, tenantID, true); enqErr != nil {
		return nil, status.Errorf(codes.Internal, "enqueue tenant-alias ensure: %v", enqErr)
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return nil, status.Errorf(codes.Internal, "commit private cluster create: %v", commitErr)
	}

	// The private cluster now has its controlling foghorn assigned; wake
	// foghorn.<cluster> so the pooled record publishes immediately.
	s.fireNavigatorSyncForPoolClusters("foghorn", []string{clusterID})

	cluster, err := s.queryCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}

	return &quartermasterpb.CreatePrivateClusterResponse{
		Cluster: cluster,
		BootstrapToken: &quartermasterpb.BootstrapToken{
			Id:        tokenID,
			Token:     token,
			Kind:      "edge_node",
			Name:      fmt.Sprintf("Bootstrap token for %s", clusterName),
			TenantId:  &tenantID,
			ClusterId: &clusterID,
			ExpiresAt: timestamppb.New(expiresAt),
			CreatedAt: timestamppb.New(now),
		},
	}, nil
}

// CreateClusterInvite creates an invite for a tenant to join a cluster
func (s *QuartermasterServer) CreateClusterInvite(ctx context.Context, req *quartermasterpb.CreateClusterInviteRequest) (*quartermasterpb.ClusterInvite, error) {
	clusterID := req.GetClusterId()
	ownerTenantID := req.GetOwnerTenantId()
	invitedTenantID := req.GetInvitedTenantId()

	if clusterID == "" || ownerTenantID == "" || invitedTenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id, owner_tenant_id, and invited_tenant_id required")
	}

	userID := middleware.GetUserID(ctx)
	// Verify ownership and get cluster name
	queries := quartermasterdb.New(s.db)
	clusterRow, err := queries.GetClusterOwnerAndName(ctx, clusterID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "cluster not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !clusterRow.OwnerTenantID.Valid || clusterRow.OwnerTenantID.String != ownerTenantID {
		return nil, status.Error(codes.PermissionDenied, "only cluster owner can create invites")
	}

	// Verify invited tenant exists
	invitedTenantName, err := queries.GetTenantName(ctx, invitedTenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "invited tenant not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Check for existing invite
	_, err = queries.FindPendingClusterInviteID(ctx, quartermasterdb.FindPendingClusterInviteIDParams{
		ClusterID: clusterID, InvitedTenantID: invitedTenantID,
	})
	if err == nil {
		return nil, status.Error(codes.AlreadyExists, "pending invite already exists for this tenant")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	id := uuid.New().String()
	token, err := generateSecureToken(32)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate token: %v", err)
	}
	now := time.Now()

	accessLevel := req.GetAccessLevel()
	if accessLevel == "" {
		accessLevel = "subscriber"
	}

	expiresInDays := req.GetExpiresInDays()
	if expiresInDays <= 0 {
		expiresInDays = 30
	}
	expiresAt := now.Add(time.Duration(expiresInDays) * 24 * time.Hour)

	// Serialize resource limits
	var resourceLimitsJSON []byte
	if req.GetResourceLimits() != nil {
		resourceLimitsJSON, _ = json.Marshal(req.GetResourceLimits().AsMap())
	}

	resourceLimits := sql.NullString{}
	if len(resourceLimitsJSON) > 0 {
		resourceLimits = validString(string(resourceLimitsJSON))
	}
	err = queries.CreateClusterInviteRecord(ctx, quartermasterdb.CreateClusterInviteRecordParams{
		ID: id, ClusterID: clusterID, InvitedTenantID: invitedTenantID, InviteToken: token,
		AccessLevel: validString(accessLevel), ResourceLimits: resourceLimits, CreatedBy: ownerTenantID,
		CreatedAt: sql.NullTime{Time: now, Valid: true}, ExpiresAt: sql.NullTime{Time: expiresAt, Valid: true},
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create invite: %v", err)
	}

	s.emitClusterEvent(ctx, eventClusterInviteCreated, ownerTenantID, userID, clusterID, "cluster_invite", id, id, "", "")

	return &quartermasterpb.ClusterInvite{
		Id:                id,
		ClusterId:         clusterID,
		InvitedTenantId:   invitedTenantID,
		InviteToken:       token,
		AccessLevel:       accessLevel,
		ResourceLimits:    req.GetResourceLimits(),
		Status:            "pending",
		CreatedBy:         ownerTenantID,
		CreatedAt:         timestamppb.New(now),
		ExpiresAt:         timestamppb.New(expiresAt),
		InvitedTenantName: &invitedTenantName,
		ClusterName:       &clusterRow.ClusterName,
	}, nil
}

// RevokeClusterInvite revokes a pending cluster invite
func (s *QuartermasterServer) RevokeClusterInvite(ctx context.Context, req *quartermasterpb.RevokeClusterInviteRequest) (*emptypb.Empty, error) {
	inviteID := req.GetInviteId()
	ownerTenantID := req.GetOwnerTenantId()

	if inviteID == "" || ownerTenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "invite_id and owner_tenant_id required")
	}

	userID := middleware.GetUserID(ctx)
	// Verify invite exists and owner is correct
	queries := quartermasterdb.New(s.db)
	inviteOwner, err := queries.GetClusterInviteOwner(ctx, inviteID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "invite not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !inviteOwner.OwnerTenantID.Valid || inviteOwner.OwnerTenantID.String != ownerTenantID {
		return nil, status.Error(codes.PermissionDenied, "only cluster owner can revoke invites")
	}

	err = queries.RevokeClusterInviteRecord(ctx, inviteID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to revoke invite: %v", err)
	}

	s.emitClusterEvent(ctx, eventClusterInviteRevoked, ownerTenantID, userID, inviteOwner.ClusterID, "cluster_invite", inviteID, inviteID, "", "")

	return &emptypb.Empty{}, nil
}

// ListClusterInvites lists invites for a cluster (owner only)
func (s *QuartermasterServer) ListClusterInvites(ctx context.Context, req *quartermasterpb.ListClusterInvitesRequest) (*quartermasterpb.ListClusterInvitesResponse, error) {
	clusterID := req.GetClusterId()
	ownerTenantID := req.GetOwnerTenantId()

	if clusterID == "" || ownerTenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id and owner_tenant_id required")
	}

	// Verify ownership
	dbOwnerID, err := quartermasterdb.New(s.db).GetClusterOwner(ctx, clusterID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "cluster not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !dbOwnerID.Valid || dbOwnerID.String != ownerTenantID {
		return nil, status.Error(codes.PermissionDenied, "only cluster owner can list invites")
	}

	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	filter := quartermasterdb.MembershipPageFilter{ScopeID: clusterID, Backward: params.Direction == pagination.Backward, Limit: params.Limit + 1}
	if params.Cursor != nil {
		filter.CursorTime, filter.CursorID = &params.Cursor.Timestamp, params.Cursor.ID
	}
	rows, total, err := quartermasterdb.New(s.db).ListClusterInvitesPage(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	var invites []*quartermasterpb.ClusterInvite
	for _, row := range rows {
		invites = append(invites, clusterInviteFromListRow(row))
	}

	// Determine pagination info
	resultsLen := len(invites)
	if resultsLen > params.Limit {
		invites = invites[:params.Limit]
	}

	// Reverse results for backward pagination
	if params.Direction == pagination.Backward {
		slices.Reverse(invites)
	}

	// Build cursors
	var startCursor, endCursor string
	if len(invites) > 0 {
		first := invites[0]
		last := invites[len(invites)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.Id)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.Id)
	}

	return &quartermasterpb.ListClusterInvitesResponse{
		Invites:    invites,
		Pagination: pagination.BuildResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
	}, nil
}

// ListMyClusterInvites lists invites received by a tenant
func (s *QuartermasterServer) ListMyClusterInvites(ctx context.Context, req *quartermasterpb.ListMyClusterInvitesRequest) (*quartermasterpb.ListClusterInvitesResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		tenantID = middleware.GetTenantID(ctx)
	}
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	filter := quartermasterdb.MembershipPageFilter{ScopeID: tenantID, Backward: params.Direction == pagination.Backward, Limit: params.Limit + 1}
	if params.Cursor != nil {
		filter.CursorTime, filter.CursorID = &params.Cursor.Timestamp, params.Cursor.ID
	}
	rows, total, err := quartermasterdb.New(s.db).ListReceivedClusterInvitesPage(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	var invites []*quartermasterpb.ClusterInvite
	for _, row := range rows {
		invites = append(invites, clusterInviteFromListRow(row))
	}

	// Determine pagination info
	resultsLen := len(invites)
	if resultsLen > params.Limit {
		invites = invites[:params.Limit]
	}

	// Reverse results for backward pagination
	if params.Direction == pagination.Backward {
		slices.Reverse(invites)
	}

	// Build cursors
	var startCursor, endCursor string
	if len(invites) > 0 {
		first := invites[0]
		last := invites[len(invites)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.Id)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.Id)
	}

	return &quartermasterpb.ListClusterInvitesResponse{
		Invites:    invites,
		Pagination: pagination.BuildResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
	}, nil
}

func rejectDirectCommercialClusterAccess(tenantID string, isPlatformOfficial bool, ownerTenantID sql.NullString, pricingModel, action string) error {
	if pricingModel == "monthly" {
		return status.Errorf(codes.FailedPrecondition, "monthly clusters require paid checkout before access can be %s", action)
	}
	if isPlatformOfficial {
		return nil
	}
	if !ownerTenantID.Valid || ownerTenantID.String == "" {
		return status.Error(codes.FailedPrecondition, "cluster ownership is ambiguous")
	}
	if ownerTenantID.String == tenantID {
		return nil
	}
	return status.Error(codes.FailedPrecondition, "third-party cluster access must be started through billing")
}

// RequestClusterSubscription requests access to a cluster
func (s *QuartermasterServer) RequestClusterSubscription(ctx context.Context, req *quartermasterpb.RequestClusterSubscriptionRequest) (*quartermasterpb.ClusterSubscription, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		tenantID = middleware.GetTenantID(ctx)
	}
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	userID := middleware.GetUserID(ctx)
	clusterID := req.GetClusterId()
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}

	// Get cluster info
	queries := quartermasterdb.New(s.db)
	policy, err := queries.GetActiveClusterSubscriptionPolicy(ctx, clusterID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "cluster not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !policy.Visibility.Valid || !policy.PricingModel.Valid || !policy.RequiresApproval.Valid || !policy.IsPlatformOfficial.Valid {
		return nil, status.Error(codes.Internal, "database error: required cluster policy field is NULL")
	}
	if commercialErr := rejectDirectCommercialClusterAccess(tenantID, policy.IsPlatformOfficial.Bool, policy.OwnerTenantID, policy.PricingModel.String, "requested"); commercialErr != nil {
		return nil, commercialErr
	}

	// Check visibility rules
	inviteToken := req.InviteToken

	switch policy.Visibility.String {
	case "private":
		// Private clusters require an invite
		if inviteToken == nil || *inviteToken == "" {
			return nil, status.Error(codes.PermissionDenied, "private cluster requires invite token")
		}
	case "unlisted":
		// Unlisted clusters require an invite
		if inviteToken == nil || *inviteToken == "" {
			return nil, status.Error(codes.PermissionDenied, "unlisted cluster requires invite token")
		}
	case "public":
		// Public clusters are open (invite optional for resource limits)
	}

	// Validate invite token if provided
	var inviteID string
	var inviteAccessLevel string
	var inviteResourceLimits sql.NullString
	if inviteToken != nil && *inviteToken != "" {
		inviteRow, inviteErr := queries.GetPendingInviteByToken(ctx, *inviteToken)
		if errors.Is(inviteErr, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "invalid or expired invite token")
		}
		if inviteErr != nil {
			return nil, status.Errorf(codes.Internal, "database error: %v", inviteErr)
		}
		inviteID = inviteRow.ID
		inviteAccessLevel = inviteRow.AccessLevel.String
		if len(inviteRow.ResourceLimits) > 0 {
			inviteResourceLimits = validString(string(inviteRow.ResourceLimits))
		}
		if inviteRow.ClusterID != clusterID {
			return nil, status.Error(codes.InvalidArgument, "invite token is for a different cluster")
		}
		if inviteRow.InvitedTenantID != tenantID {
			return nil, status.Error(codes.PermissionDenied, "invite token is for a different tenant")
		}
	}

	// Determine subscription status
	subscriptionStatus := "active"
	if policy.RequiresApproval.Bool && (inviteToken == nil || *inviteToken == "") {
		subscriptionStatus = "pending_approval"
	}

	// Set access level
	accessLevel := "subscriber"
	if inviteAccessLevel != "" {
		accessLevel = inviteAccessLevel
	}

	now := time.Now()
	id := uuid.New().String()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin transaction: %v", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			s.logger.WithError(rollbackErr).Debug("transaction rollback failed")
		}
	}()

	txQueries := quartermasterdb.New(tx)
	if inviteID != "" {
		if err = txQueries.AcceptClusterInviteRecord(ctx, inviteID); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to accept invite: %v", err)
		}
	}

	subscriptionID, err := txQueries.UpsertRequestedClusterSubscription(ctx, quartermasterdb.UpsertRequestedClusterSubscriptionParams{
		ID: id, TenantID: tenantID, ClusterID: clusterID, AccessLevel: validString(accessLevel),
		SubscriptionStatus: validString(subscriptionStatus), ResourceLimits: inviteResourceLimits,
		RequestedAt: sql.NullTime{Time: now, Valid: true},
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create subscription: %v", err)
	}

	eventType := eventClusterSubscriptionRequested
	if subscriptionStatus == "active" {
		eventType = eventClusterSubscriptionApproved
	}
	if enqErr := s.emitClusterEventTx(ctx, tx, eventType, tenantID, userID, clusterID, "cluster_subscription", subscriptionID, inviteID, subscriptionID, ""); enqErr != nil {
		return nil, status.Errorf(codes.Internal, "enqueue cluster subscription event: %v", enqErr)
	}

	if err = tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit subscription: %v", err)
	}

	// Fetch the created subscription
	sub, err := s.getClusterSubscription(ctx, tenantID, clusterID)
	if err != nil {
		return nil, err
	}

	return sub, nil
}

// AcceptClusterInvite accepts a cluster invite using the token
func (s *QuartermasterServer) AcceptClusterInvite(ctx context.Context, req *quartermasterpb.AcceptClusterInviteRequest) (*quartermasterpb.ClusterSubscription, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		tenantID = middleware.GetTenantID(ctx)
	}
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	userID := middleware.GetUserID(ctx)
	inviteToken := req.GetInviteToken()
	if inviteToken == "" {
		return nil, status.Error(codes.InvalidArgument, "invite_token required")
	}

	// Look up the invite
	inviteRow, err := quartermasterdb.New(s.db).GetPendingInviteWithClusterPolicy(ctx, inviteToken)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "invalid or expired invite token")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !inviteRow.AccessLevel.Valid || !inviteRow.PricingModel.Valid || !inviteRow.IsPlatformOfficial.Valid {
		return nil, status.Error(codes.Internal, "database error: required invite field is NULL")
	}
	if inviteRow.InvitedTenantID != tenantID {
		return nil, status.Error(codes.PermissionDenied, "invite is for a different tenant")
	}
	if commercialErr := rejectDirectCommercialClusterAccess(tenantID, inviteRow.IsPlatformOfficial.Bool, inviteRow.OwnerTenantID, inviteRow.PricingModel.String, "accepted"); commercialErr != nil {
		return nil, commercialErr
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin transaction: %v", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			s.logger.WithError(rollbackErr).Debug("transaction rollback failed")
		}
	}()

	txQueries := quartermasterdb.New(tx)
	if err = txQueries.AcceptClusterInviteRecord(ctx, inviteRow.ID); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to accept invite: %v", err)
	}

	now := time.Now()
	id := uuid.New().String()

	resourceLimits := sql.NullString{}
	if len(inviteRow.ResourceLimits) > 0 {
		resourceLimits = validString(string(inviteRow.ResourceLimits))
	}
	subscriptionID, err := txQueries.UpsertAcceptedClusterSubscription(ctx, quartermasterdb.UpsertAcceptedClusterSubscriptionParams{
		ID: id, TenantID: tenantID, ClusterID: inviteRow.ClusterID, AccessLevel: inviteRow.AccessLevel,
		ResourceLimits: resourceLimits, ApprovedAt: sql.NullTime{Time: now, Valid: true},
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create subscription: %v", err)
	}

	if enqErr := s.emitClusterEventTx(ctx, tx, eventClusterSubscriptionApproved, tenantID, userID, inviteRow.ClusterID, "cluster_subscription", subscriptionID, inviteRow.ID, subscriptionID, ""); enqErr != nil {
		return nil, status.Errorf(codes.Internal, "enqueue cluster subscription event: %v", enqErr)
	}
	if enqErr := s.enqueueTenantAliasEnsureTx(ctx, tx, tenantID, true); enqErr != nil {
		return nil, status.Errorf(codes.Internal, "enqueue tenant-alias ensure: %v", enqErr)
	}

	if err = tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit invite acceptance: %v", err)
	}

	sub, err := s.getClusterSubscription(ctx, tenantID, inviteRow.ClusterID)
	if err != nil {
		return nil, err
	}

	return sub, nil
}

// ListPendingSubscriptions lists pending subscription requests for a cluster
func (s *QuartermasterServer) ListPendingSubscriptions(ctx context.Context, req *quartermasterpb.ListPendingSubscriptionsRequest) (*quartermasterpb.ListPendingSubscriptionsResponse, error) {
	clusterID := req.GetClusterId()
	ownerTenantID := req.GetOwnerTenantId()

	if clusterID == "" || ownerTenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id and owner_tenant_id required")
	}

	// Verify ownership
	dbOwnerID, err := quartermasterdb.New(s.db).GetClusterOwner(ctx, clusterID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "cluster not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !dbOwnerID.Valid || dbOwnerID.String != ownerTenantID {
		return nil, status.Error(codes.PermissionDenied, "only cluster owner can view pending subscriptions")
	}

	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	filter := quartermasterdb.MembershipPageFilter{ScopeID: clusterID, Backward: params.Direction == pagination.Backward, Limit: params.Limit + 1}
	if params.Cursor != nil {
		filter.CursorTime, filter.CursorID = &params.Cursor.Timestamp, params.Cursor.ID
	}
	rows, total, err := quartermasterdb.New(s.db).ListPendingSubscriptionsPage(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	var subscriptions []*quartermasterpb.ClusterSubscription
	for _, row := range rows {
		subscriptions = append(subscriptions, clusterSubscriptionFromListRow(row))
	}

	// Determine pagination info
	resultsLen := len(subscriptions)
	if resultsLen > params.Limit {
		subscriptions = subscriptions[:params.Limit]
	}

	// Reverse results for backward pagination
	if params.Direction == pagination.Backward {
		slices.Reverse(subscriptions)
	}

	// Build cursors
	var startCursor, endCursor string
	if len(subscriptions) > 0 {
		first := subscriptions[0]
		last := subscriptions[len(subscriptions)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.Id)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.Id)
	}

	return &quartermasterpb.ListPendingSubscriptionsResponse{
		Subscriptions: subscriptions,
		Pagination:    pagination.BuildResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
	}, nil
}

// ApproveClusterSubscription approves a pending subscription
func (s *QuartermasterServer) ApproveClusterSubscription(ctx context.Context, req *quartermasterpb.ApproveClusterSubscriptionRequest) (*quartermasterpb.ClusterSubscription, error) {
	subscriptionID := req.GetSubscriptionId()
	ownerTenantID := req.GetOwnerTenantId()

	if subscriptionID == "" || ownerTenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "subscription_id and owner_tenant_id required")
	}

	userID := middleware.GetUserID(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin transaction: %v", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			s.logger.WithError(rollbackErr).Debug("transaction rollback failed")
		}
	}()

	// Get subscription and verify ownership
	txQueries := quartermasterdb.New(tx)
	subscriptionRow, err := txQueries.GetSubscriptionOwnerPolicy(ctx, subscriptionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "subscription not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !subscriptionRow.OwnerTenantID.Valid || subscriptionRow.OwnerTenantID.String != ownerTenantID {
		return nil, status.Error(codes.PermissionDenied, "only cluster owner can approve subscriptions")
	}
	if !subscriptionRow.PricingModel.Valid || !subscriptionRow.IsPlatformOfficial.Valid {
		return nil, status.Error(codes.Internal, "database error: required subscription policy field is NULL")
	}
	if commercialErr := rejectDirectCommercialClusterAccess(subscriptionRow.TenantID, subscriptionRow.IsPlatformOfficial.Bool, subscriptionRow.OwnerTenantID, subscriptionRow.PricingModel.String, "approved"); commercialErr != nil {
		return nil, commercialErr
	}

	err = txQueries.ApproveClusterSubscriptionRecord(ctx, quartermasterdb.ApproveClusterSubscriptionRecordParams{
		ApprovedBy: ownerTenantID, SubscriptionID: subscriptionID,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to approve subscription: %v", err)
	}

	if enqErr := s.emitClusterEventTx(ctx, tx, eventClusterSubscriptionApproved, subscriptionRow.TenantID, userID, subscriptionRow.ClusterID, "cluster_subscription", subscriptionID, "", subscriptionID, ""); enqErr != nil {
		return nil, status.Errorf(codes.Internal, "enqueue cluster subscription event: %v", enqErr)
	}
	if enqErr := s.enqueueTenantAliasEnsureTx(ctx, tx, subscriptionRow.TenantID, true); enqErr != nil {
		return nil, status.Errorf(codes.Internal, "enqueue tenant-alias ensure: %v", enqErr)
	}

	if err = tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit subscription approval: %v", err)
	}

	sub, err := s.getClusterSubscription(ctx, subscriptionRow.TenantID, subscriptionRow.ClusterID)
	if err != nil {
		return nil, err
	}

	return sub, nil
}

// RejectClusterSubscription rejects a pending subscription
func (s *QuartermasterServer) RejectClusterSubscription(ctx context.Context, req *quartermasterpb.RejectClusterSubscriptionRequest) (*quartermasterpb.ClusterSubscription, error) {
	subscriptionID := req.GetSubscriptionId()
	ownerTenantID := req.GetOwnerTenantId()

	if subscriptionID == "" || ownerTenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "subscription_id and owner_tenant_id required")
	}

	userID := middleware.GetUserID(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin transaction: %v", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			s.logger.WithError(rollbackErr).Debug("transaction rollback failed")
		}
	}()

	// Get subscription and verify ownership
	txQueries := quartermasterdb.New(tx)
	subscriptionRow, err := txQueries.GetSubscriptionOwner(ctx, subscriptionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "subscription not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !subscriptionRow.OwnerTenantID.Valid || subscriptionRow.OwnerTenantID.String != ownerTenantID {
		return nil, status.Error(codes.PermissionDenied, "only cluster owner can reject subscriptions")
	}

	reason := ""
	if req.Reason != nil {
		reason = *req.Reason
	}
	err = txQueries.RejectClusterSubscriptionRecord(ctx, quartermasterdb.RejectClusterSubscriptionRecordParams{
		RejectionReason: validString(reason), SubscriptionID: subscriptionID,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to reject subscription: %v", err)
	}

	if enqErr := s.emitClusterEventTx(ctx, tx, eventClusterSubscriptionRejected, subscriptionRow.TenantID, userID, subscriptionRow.ClusterID, "cluster_subscription", subscriptionID, "", subscriptionID, reason); enqErr != nil {
		return nil, status.Errorf(codes.Internal, "enqueue cluster subscription event: %v", enqErr)
	}

	if err = tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit subscription rejection: %v", err)
	}

	sub, err := s.getClusterSubscription(ctx, subscriptionRow.TenantID, subscriptionRow.ClusterID)
	if err != nil {
		return nil, err
	}

	return sub, nil
}

// ListPeers returns clusters that share at least one tenant with the requesting cluster.
// Used only by provider-operated Foghorns authenticated with the internal service credential. The request's
// cluster ID is routing attribution within that trust boundary; third-party callers require cluster-bound
// identity from the service-identity RFC.
func (s *QuartermasterServer) ListPeers(ctx context.Context, req *quartermasterpb.ListPeersRequest) (*quartermasterpb.ListPeersResponse, error) {
	clusterID := req.GetClusterId()
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}

	// Find all clusters that share at least one active tenant with the requesting cluster.
	// For each peer, aggregate the shared tenant IDs and resolve the Foghorn gRPC address
	// from service_instances.
	rows, err := quartermasterdb.New(s.db).ListPeerClusters(ctx, clusterID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "query peers: %v", err)
	}
	peers := make([]*quartermasterpb.PeerCluster, 0, len(rows))
	for _, row := range rows {
		peers = append(peers, &quartermasterpb.PeerCluster{
			ClusterId: row.ClusterID, SharedTenantIds: row.SharedTenantIds,
			ClusterName: row.ClusterName, ClusterType: row.ClusterType, FoghornAddr: row.FoghornAddr,
		})
	}

	return &quartermasterpb.ListPeersResponse{Peers: peers}, nil
}

// getClusterSubscription is a helper to fetch a subscription by tenant and cluster
func (s *QuartermasterServer) getClusterSubscription(ctx context.Context, tenantID, clusterID string) (*quartermasterpb.ClusterSubscription, error) {
	row, err := quartermasterdb.New(s.db).GetClusterSubscriptionRecord(ctx, quartermasterdb.GetClusterSubscriptionRecordParams{
		TenantID: tenantID, ClusterID: clusterID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "subscription not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !row.AccessLevel.Valid || !row.SubscriptionStatus.Valid || !row.CreatedAt.Valid || !row.UpdatedAt.Valid {
		return nil, status.Error(codes.Internal, "database error: required subscription field is NULL")
	}
	sub := &quartermasterpb.ClusterSubscription{
		Id: row.ID, TenantId: row.TenantID, ClusterId: row.ClusterID, AccessLevel: row.AccessLevel.String,
		SubscriptionStatus: subscriptionStatusStringToProto(row.SubscriptionStatus.String),
		CreatedAt:          timestamppb.New(row.CreatedAt.Time), UpdatedAt: timestamppb.New(row.UpdatedAt.Time),
		ClusterName: &row.ClusterName, TenantName: &row.TenantName,
	}
	if row.RequestedAt.Valid {
		sub.RequestedAt = timestamppb.New(row.RequestedAt.Time)
	}
	if row.ApprovedAt.Valid {
		sub.ApprovedAt = timestamppb.New(row.ApprovedAt.Time)
	}
	if row.ApprovedBy.Valid {
		sub.ApprovedBy = &row.ApprovedBy.String
	}
	if row.RejectionReason.Valid {
		sub.RejectionReason = &row.RejectionReason.String
	}
	if row.ExpiresAt.Valid {
		sub.ExpiresAt = timestamppb.New(row.ExpiresAt.Time)
	}
	if len(row.ResourceLimits) > 0 {
		var limitsMap map[string]any
		if json.Unmarshal(row.ResourceLimits, &limitsMap) == nil {
			sub.ResourceLimits = mapToStruct(limitsMap)
		}
	}
	return sub, nil
}

// scanClusterSubscription scans a ClusterSubscription from rows
func clusterInviteFromListRow(row quartermasterdb.ClusterInviteListRow) *quartermasterpb.ClusterInvite {
	invite := &quartermasterpb.ClusterInvite{Id: row.ID, ClusterId: row.ClusterID, InvitedTenantId: row.InvitedTenantID,
		InviteToken: row.InviteToken, AccessLevel: row.AccessLevel, Status: row.Status, CreatedBy: row.CreatedBy,
		CreatedAt: timestamppb.New(row.CreatedAt)}
	if row.ExpiresAt.Valid {
		invite.ExpiresAt = timestamppb.New(row.ExpiresAt.Time)
	}
	if row.AcceptedAt.Valid {
		invite.AcceptedAt = timestamppb.New(row.AcceptedAt.Time)
	}
	if row.InvitedTenantName.Valid {
		invite.InvitedTenantName = &row.InvitedTenantName.String
	}
	if row.ClusterName.Valid {
		invite.ClusterName = &row.ClusterName.String
	}
	if row.ResourceLimits.Valid {
		var limits map[string]any
		if json.Unmarshal([]byte(row.ResourceLimits.String), &limits) == nil {
			invite.ResourceLimits = mapToStruct(limits)
		}
	}
	return invite
}

func clusterSubscriptionFromListRow(row quartermasterdb.ClusterSubscriptionListRow) *quartermasterpb.ClusterSubscription {
	sub := &quartermasterpb.ClusterSubscription{Id: row.ID, TenantId: row.TenantID, ClusterId: row.ClusterID,
		AccessLevel: row.AccessLevel, SubscriptionStatus: subscriptionStatusStringToProto(row.SubscriptionStatus),
		CreatedAt: timestamppb.New(row.CreatedAt), UpdatedAt: timestamppb.New(row.UpdatedAt)}
	if row.RequestedAt.Valid {
		sub.RequestedAt = timestamppb.New(row.RequestedAt.Time)
	}
	if row.ApprovedAt.Valid {
		sub.ApprovedAt = timestamppb.New(row.ApprovedAt.Time)
	}
	if row.ApprovedBy.Valid {
		sub.ApprovedBy = &row.ApprovedBy.String
	}
	if row.RejectionReason.Valid {
		sub.RejectionReason = &row.RejectionReason.String
	}
	if row.ExpiresAt.Valid {
		sub.ExpiresAt = timestamppb.New(row.ExpiresAt.Time)
	}
	if row.ClusterName.Valid {
		sub.ClusterName = &row.ClusterName.String
	}
	if row.TenantName.Valid {
		sub.TenantName = &row.TenantName.String
	}
	if row.ResourceLimits.Valid {
		var limits map[string]any
		if json.Unmarshal([]byte(row.ResourceLimits.String), &limits) == nil {
			sub.ResourceLimits = mapToStruct(limits)
		}
	}
	return sub
}

type GRPCServerConfig struct {
	DB              *sql.DB
	Logger          logging.Logger
	ServiceToken    string
	JWTSecret       []byte
	NavigatorClient *navigator.Client
	DecklogClient   *decklogclient.BatchedClient
	PurserClient    *purserclient.GRPCClient // For billing status lookups (cross-service via gRPC)
	GeoIPReader     *geoip.Reader
	Metrics         *ServerMetrics
	CertFile        string
	KeyFile         string
	AllowInsecure   bool
	// AdvertiseGRPCAddr is the "how nodes reach me" address that gets returned
	// to freshly-enrolled nodes via BootstrapInfrastructureNodeResponse. Empty
	// means enrollment will tell the node to rediscover via DNS aliases.
	AdvertiseGRPCAddr string
	// PlatformRootDomain is the physical/platform DNS root (BRAND_DOMAIN) used
	// to synthesize per-instance physical endpoints.
	PlatformRootDomain string
	// PhysicalEndpointStaleSeconds is Navigator's NAVIGATOR_DNS_HEALTH_STALE_SECONDS,
	// reused so DiscoverServices' public_instance_host freshness gate stays in
	// lockstep with Navigator's physical-DNS publish freshness.
	PhysicalEndpointStaleSeconds int
}

// ServerMetrics holds Prometheus metrics for the gRPC server. Per-method
// counts + duration come from GRPCMetricsInterceptor.
type ServerMetrics struct {
	GRPCRequests          *prometheus.CounterVec
	GRPCDuration          *prometheus.HistogramVec
	SyncMeshPhaseDuration *prometheus.HistogramVec
}

// NewGRPCServer creates a new gRPC server for Quartermaster
func NewGRPCServer(cfg GRPCServerConfig) *grpc.Server {
	// Chain auth interceptor with logging interceptor
	authInterceptor := middleware.GRPCAuthInterceptor(middleware.GRPCAuthConfig{
		ServiceToken: cfg.ServiceToken,
		JWTSecret:    cfg.JWTSecret,
		Logger:       cfg.Logger,
		SkipMethods: []string{
			"/grpc.health.v1.Health/Check",
			"/grpc.health.v1.Health/Watch",
			// Bootstrap is pre-auth (uses bootstrap tokens)
			"/quartermaster.BootstrapService/BootstrapEdgeNode",
			"/quartermaster.BootstrapService/BootstrapInfrastructureNode",
		},
	})

	// GRPCMetricsInterceptor sits outermost so Unauthenticated / PermissionDenied
	// rejections from the auth interceptor still show up in
	// quartermaster_grpc_requests_total.
	opts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(
			middleware.GRPCMetricsInterceptor(cfg.Metrics.GRPCRequests, cfg.Metrics.GRPCDuration),
			authInterceptor,
			unaryInterceptor(cfg.Logger),
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
		cfg.Logger.WithError(err).Fatal("Timed out waiting for Quartermaster gRPC TLS files")
	}
	tlsOpt, err := grpcutil.ServerTLS(tlsCfg, cfg.Logger)
	if err != nil {
		cfg.Logger.WithError(err).Fatal("Failed to configure Quartermaster gRPC TLS")
	}
	if tlsOpt != nil {
		opts = append(opts, tlsOpt)
	}

	server := grpc.NewServer(opts...)
	qmServer := NewQuartermasterServer(cfg.DB, cfg.Logger, cfg.NavigatorClient, cfg.DecklogClient, cfg.PurserClient, cfg.GeoIPReader, cfg.Metrics)
	qmServer.SetQuartermasterGRPCAddr(cfg.AdvertiseGRPCAddr)
	qmServer.SetPlatformRootDomain(cfg.PlatformRootDomain)
	qmServer.SetPhysicalEndpointStaleSeconds(cfg.PhysicalEndpointStaleSeconds)

	// Drain worker for quartermaster.service_event_outbox. SKIP LOCKED +
	// lease let this run safely on every Quartermaster replica.
	go qmServer.runMeshTopologyConfigWarmer(context.Background())
	go qmServer.runServiceEventOutboxWorker(context.Background())
	go qmServer.runTenantPrivateBaseURLRepair(context.Background())
	// Drain the Navigator custom-domain outbox so a Navigator outage at the
	// moment UpdateTenant lands can't leave QM saying the tenant has a
	// custom_domain while Navigator never spun up the verification + cert
	// lifecycle row.
	go qmServer.runNavigatorCustomDomainOutboxWorker(context.Background())
	// Drain the Navigator tenant-alias outbox. Every subdomain-alias hand-off
	// (create/rename/tier change/cluster add-remove) is durable here, so a
	// Navigator outage can't lose the intent; per-tenant seq ordering keeps a
	// newer remove from overtaking an older ensure.
	go qmServer.runNavigatorTenantAliasOutboxWorker(context.Background())
	// Backstop: periodically reconcile tenant intent against Navigator's
	// applied alias state and enqueue any missing/drifted transitions.
	go qmServer.runTenantAliasBackstop(context.Background())

	// Register all services
	quartermasterpb.RegisterTenantServiceServer(server, qmServer)
	quartermasterpb.RegisterBootstrapServiceServer(server, qmServer)
	quartermasterpb.RegisterNodeServiceServer(server, qmServer)
	quartermasterpb.RegisterClusterServiceServer(server, qmServer)
	quartermasterpb.RegisterMeshServiceServer(server, qmServer)
	quartermasterpb.RegisterServiceRegistryServiceServer(server, qmServer)
	quartermasterpb.RegisterIngressServiceServer(server, qmServer)

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
		}).Debug("gRPC request processed")
		return resp, err
	}
}
