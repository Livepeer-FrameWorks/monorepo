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
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
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
	// defaultPhysicalEndpointStaleSeconds matches Navigator's code default for
	// NAVIGATOR_DNS_HEALTH_STALE_SECONDS; the configured value (e.g. base.env's 90)
	// overrides it via SetPhysicalEndpointStaleSeconds.
	defaultPhysicalEndpointStaleSeconds = 300
)

func retryQueryContext(ctx context.Context, db *sql.DB, query string, args ...any) (*sql.Rows, error) {
	var rows *sql.Rows
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var err error
		rows, err = db.QueryContext(ctx, query, args...) //nolint:sqlclosecheck // caller owns rows after retry succeeds
		return err
	})
	return rows, err
}

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

	var name string
	var isActive bool
	var rateLimitPerMinute, rateLimitBurst int32

	// Query ONLY quartermaster.tenants (no cross-service DB access)
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.db.QueryRowContext(ctx, `
			SELECT name, is_active, rate_limit_per_minute, rate_limit_burst
			FROM quartermaster.tenants
			WHERE id = $1
		`, tenantID).Scan(&name, &isActive, &rateLimitPerMinute, &rateLimitBurst)
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

	// Get billing info via Purser gRPC (cross-service API call, not DB join)
	var billingModel string
	var isSuspended, isBalanceNegative bool

	if s.purserClient != nil {
		billingStatus, err := s.purserClient.GetTenantBillingStatus(ctx, tenantID)
		if err != nil {
			// Log but don't fail - billing info is supplementary
			s.logger.WithFields(logging.Fields{
				"tenant_id": tenantID,
				"error":     err,
			}).Warn("Failed to get billing status from Purser, using defaults")
			billingModel = "postpaid"
		} else {
			billingModel = billingStatus.BillingModel
			isSuspended = billingStatus.IsSuspended
			isBalanceNegative = billingStatus.IsBalanceNegative
		}
	} else {
		// No Purser client configured - use defaults
		billingModel = "postpaid"
	}

	return &quartermasterpb.ValidateTenantResponse{
		Valid:              isActive,
		TenantId:           tenantID,
		TenantName:         name,
		IsActive:           isActive,
		RateLimitPerMinute: rateLimitPerMinute,
		RateLimitBurst:     rateLimitBurst,
		BillingModel:       billingModel,
		IsSuspended:        isSuspended,
		IsBalanceNegative:  isBalanceNegative,
	}, nil
}

// GetTenant retrieves tenant details by ID
func (s *QuartermasterServer) GetTenant(ctx context.Context, req *quartermasterpb.GetTenantRequest) (*quartermasterpb.GetTenantResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	var tenant quartermasterpb.Tenant
	var subdomain, customDomain, logoURL, primaryClusterID, officialClusterID, kafkaTopicPrefix, databaseURL sql.NullString
	var kafkaBrokers []string
	var createdAt, updatedAt time.Time

	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, subdomain, custom_domain, logo_url, primary_color, secondary_color,
		       deployment_tier, deployment_model,
		       primary_cluster_id, official_cluster_id, kafka_topic_prefix, kafka_brokers, database_url,
		       is_active, created_at, updated_at,
		       rate_limit_per_minute, rate_limit_burst
		FROM quartermaster.tenants
		WHERE id = $1
	`, tenantID).Scan(
		&tenant.Id, &tenant.Name, &subdomain, &customDomain, &logoURL,
		&tenant.PrimaryColor, &tenant.SecondaryColor, &tenant.DeploymentTier,
		&tenant.DeploymentModel,
		&primaryClusterID, &officialClusterID, &kafkaTopicPrefix, pq.Array(&kafkaBrokers), &databaseURL,
		&tenant.IsActive, &createdAt, &updatedAt,
		&tenant.RateLimitPerMinute, &tenant.RateLimitBurst,
	)

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

	// Set optional fields
	if subdomain.Valid {
		tenant.Subdomain = &subdomain.String
	}
	if customDomain.Valid {
		tenant.CustomDomain = &customDomain.String
	}
	if logoURL.Valid {
		tenant.LogoUrl = &logoURL.String
	}
	if primaryClusterID.Valid {
		tenant.PrimaryClusterId = &primaryClusterID.String
	}
	if officialClusterID.Valid {
		tenant.OfficialClusterId = &officialClusterID.String
	}
	if kafkaTopicPrefix.Valid {
		tenant.KafkaTopicPrefix = &kafkaTopicPrefix.String
	}
	if databaseURL.Valid {
		tenant.DatabaseUrl = &databaseURL.String
	}
	tenant.KafkaBrokers = kafkaBrokers
	tenant.CreatedAt = timestamppb.New(createdAt)
	tenant.UpdatedAt = timestamppb.New(updatedAt)

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
	var primaryClusterID, deploymentTier string
	var officialClusterID sql.NullString
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.db.QueryRowContext(ctx, `
			SELECT primary_cluster_id, COALESCE(official_cluster_id, ''), deployment_tier
			FROM quartermaster.tenants
			WHERE id = $1 AND is_active = true
		`, tenantID).Scan(&primaryClusterID, &officialClusterID, &deploymentTier)
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

	// Get cluster info with capacity validation
	// max_streams = 0 means unlimited
	// max_bandwidth_mbps = 0 means unlimited
	var resp quartermasterpb.ClusterRoutingResponse
	var kafkaBrokers []string
	var databaseURL, periscopeURL sql.NullString
	var topicPrefix string
	err = database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.db.QueryRowContext(ctx, `
			SELECT
				c.cluster_id, c.cluster_name, c.cluster_type, c.base_url,
				c.kafka_brokers, c.database_url, c.periscope_url,
				COALESCE(tca.kafka_topic_prefix, t.kafka_topic_prefix, '') as topic_prefix,
				c.max_concurrent_streams, c.health_status
			FROM quartermaster.infrastructure_clusters c
			JOIN quartermaster.tenants t ON t.id = $2
			LEFT JOIN quartermaster.tenant_cluster_assignments tca ON tca.tenant_id = t.id AND tca.cluster_id = c.cluster_id
			WHERE c.cluster_id = $1
			  AND c.is_active = true
		`, primaryClusterID, tenantID).Scan(
			&resp.ClusterId, &resp.ClusterName, &resp.ClusterType, &resp.BaseUrl,
			pq.Array(&kafkaBrokers), &databaseURL, &periscopeURL,
			&topicPrefix,
			&resp.MaxStreams, &resp.HealthStatus,
		)
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

	resp.KafkaBrokers = kafkaBrokers
	resp.TopicPrefix = topicPrefix
	if databaseURL.Valid {
		resp.DatabaseUrl = &databaseURL.String
	}
	if periscopeURL.Valid {
		resp.PeriscopeUrl = &periscopeURL.String
	}

	// Surface access-specific runtime cap overrides so Foghorn can enforce
	// them at trigger time. Plan-level Free caps come from Purser tier
	// entitlements; an empty tenant_cluster_access.resource_limits column
	// means "no cluster override". Bandwidth caps (max_bandwidth_mbps) are
	// not enforced runtime today and intentionally not surfaced on the typed
	// response — they live in the JSONB column as a future hook.
	var tenantResourceLimits []byte
	if err := s.db.QueryRowContext(ctx, `
		SELECT resource_limits
		FROM quartermaster.tenant_cluster_access
		WHERE tenant_id = $1 AND cluster_id = $2 AND is_active = TRUE
	`, tenantID, primaryClusterID).Scan(&tenantResourceLimits); err == nil && len(tenantResourceLimits) > 0 {
		var limits map[string]any
		if json.Unmarshal(tenantResourceLimits, &limits) == nil {
			caps := &quartermasterpb.TenantResourceLimits{}
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
	var foghornHost sql.NullString
	var foghornPort sql.NullInt32
	_ = s.db.QueryRowContext(ctx, `
		SELECT si.advertise_host, si.port
		FROM quartermaster.service_cluster_assignments sca
		JOIN quartermaster.service_instances si ON si.id = sca.service_instance_id
		JOIN quartermaster.services svc ON svc.service_id = si.service_id
		WHERE sca.cluster_id = $1
		  AND sca.is_active = true
		  AND svc.type = 'foghorn'
		  AND si.status = 'running'
		  AND si.health_status = 'healthy'
		  AND (si.metadata->>'foghorn_listener' = 'internal_control' OR si.port = 18019 OR si.metadata->>'foghorn_listener' = 'control')
		  AND COALESCE(si.advertise_host, '') <> ''
		  AND COALESCE(si.port, 0) > 0
		ORDER BY CASE WHEN si.metadata->>'foghorn_listener' = 'internal_control' THEN 0 WHEN si.port = 18019 THEN 1 WHEN si.metadata->>'foghorn_listener' = 'control' THEN 2 ELSE 3 END, CASE WHEN si.protocol = 'grpc' THEN 0 ELSE 1 END, si.updated_at DESC, si.id ASC
		LIMIT 1
	`, primaryClusterID).Scan(&foghornHost, &foghornPort)
	if addr, ok := buildAdvertiseAddr(foghornHost, foghornPort); ok {
		resp.FoghornGrpcAddr = &addr
	}

	slug := dns.SanitizeLabel(resp.ClusterId)
	resp.ClusterSlug = &slug

	// Resolve official cluster info when it differs from primary (best-effort)
	if officialClusterID.Valid && officialClusterID.String != "" && officialClusterID.String != primaryClusterID {
		var offClusterName, offBaseURL sql.NullString
		_ = s.db.QueryRowContext(ctx, `
			SELECT cluster_name, base_url
			FROM quartermaster.infrastructure_clusters
			WHERE cluster_id = $1 AND is_active = true
		`, officialClusterID.String).Scan(&offClusterName, &offBaseURL)

		if offBaseURL.Valid {
			resp.OfficialClusterId = &officialClusterID.String
			offSlug := dns.SanitizeLabel(officialClusterID.String)
			resp.OfficialClusterSlug = &offSlug
			resp.OfficialBaseUrl = &offBaseURL.String
			if offClusterName.Valid {
				resp.OfficialClusterName = &offClusterName.String
			}

			// Resolve official cluster's Foghorn address via assignments
			var offFoghornHost sql.NullString
			var offFoghornPort sql.NullInt32
			_ = s.db.QueryRowContext(ctx, `
				SELECT si.advertise_host, si.port
				FROM quartermaster.service_cluster_assignments sca
				JOIN quartermaster.service_instances si ON si.id = sca.service_instance_id
				JOIN quartermaster.services svc ON svc.service_id = si.service_id
				WHERE sca.cluster_id = $1
				  AND sca.is_active = true
				  AND svc.type = 'foghorn'
				  AND si.status = 'running'
				  AND si.health_status = 'healthy'
				  AND (si.metadata->>'foghorn_listener' = 'internal_control' OR si.port = 18019 OR si.metadata->>'foghorn_listener' = 'control')
				  AND COALESCE(si.advertise_host, '') <> ''
				  AND COALESCE(si.port, 0) > 0
				ORDER BY CASE WHEN si.metadata->>'foghorn_listener' = 'internal_control' THEN 0 WHEN si.port = 18019 THEN 1 WHEN si.metadata->>'foghorn_listener' = 'control' THEN 2 ELSE 3 END, CASE WHEN si.protocol = 'grpc' THEN 0 ELSE 1 END, si.updated_at DESC, si.id ASC
				LIMIT 1
			`, officialClusterID.String).Scan(&offFoghornHost, &offFoghornPort)
			if addr, ok := buildAdvertiseAddr(offFoghornHost, offFoghornPort); ok {
				resp.OfficialFoghornGrpcAddr = &addr
			}
		}
	}

	// Build cluster_peers: all clusters this tenant has access to (best-effort).
	// Resolves Foghorn gRPC address per peer so Commodore can route commands to
	// any cluster. region_id / cell_id / cluster_class / health_status ride
	// along so Commodore's plan-aware route filter can reject ineligible peers
	// without a second round-trip.
	peerRows, peerErr := s.db.QueryContext(ctx, `
		SELECT ic.cluster_id, ic.cluster_name, ic.cluster_type, ic.base_url,
		       COALESCE(ic.s3_bucket, ''), COALESCE(ic.s3_endpoint, ''), COALESCE(ic.s3_region, ''),
		       COALESCE(ic.region_id, ''), COALESCE(ic.cell_id, ''),
		       COALESCE(ic.cluster_class, ''),
		       COALESCE(ic.health_status, ''),
		       COALESCE(
		           (SELECT si.advertise_host
		            FROM quartermaster.service_cluster_assignments sca
		            JOIN quartermaster.service_instances si ON si.id = sca.service_instance_id
		            JOIN quartermaster.services svc ON svc.service_id = si.service_id
		            WHERE sca.cluster_id = ic.cluster_id
		              AND sca.is_active = TRUE
		              AND svc.type = 'foghorn'
		              AND si.status = 'running'
		              AND si.health_status = 'healthy'
		              AND (si.metadata->>'foghorn_listener' = 'internal_control' OR si.port = 18019 OR si.metadata->>'foghorn_listener' = 'control')
		              AND COALESCE(si.advertise_host, '') <> ''
		              AND COALESCE(si.port, 0) > 0
		            ORDER BY CASE WHEN si.metadata->>'foghorn_listener' = 'internal_control' THEN 0 WHEN si.port = 18019 THEN 1 WHEN si.metadata->>'foghorn_listener' = 'control' THEN 2 ELSE 3 END, CASE WHEN si.protocol = 'grpc' THEN 0 ELSE 1 END, si.updated_at DESC, si.id ASC
		            LIMIT 1),
		           ''
		       ) AS foghorn_advertise_host,
		       COALESCE(
		           (SELECT si.port
		            FROM quartermaster.service_cluster_assignments sca
		            JOIN quartermaster.service_instances si ON si.id = sca.service_instance_id
		            JOIN quartermaster.services svc ON svc.service_id = si.service_id
		            WHERE sca.cluster_id = ic.cluster_id
		              AND sca.is_active = TRUE
		              AND svc.type = 'foghorn'
		              AND si.status = 'running'
		              AND si.health_status = 'healthy'
		              AND (si.metadata->>'foghorn_listener' = 'internal_control' OR si.port = 18019 OR si.metadata->>'foghorn_listener' = 'control')
		              AND COALESCE(si.advertise_host, '') <> ''
		              AND COALESCE(si.port, 0) > 0
		            ORDER BY CASE WHEN si.metadata->>'foghorn_listener' = 'internal_control' THEN 0 WHEN si.port = 18019 THEN 1 WHEN si.metadata->>'foghorn_listener' = 'control' THEN 2 ELSE 3 END, CASE WHEN si.protocol = 'grpc' THEN 0 ELSE 1 END, si.updated_at DESC, si.id ASC
		            LIMIT 1),
		           0
		       ) AS foghorn_port
		FROM quartermaster.tenant_cluster_access tca
		JOIN quartermaster.infrastructure_clusters ic ON ic.cluster_id = tca.cluster_id
		WHERE tca.tenant_id = $1
		  AND tca.is_active = TRUE
		  AND tca.subscription_status = 'active'
		  AND ic.is_active = TRUE
		ORDER BY ic.cluster_id ASC
	`, tenantID)
	if peerErr == nil {
		defer peerRows.Close()
		officialID := ""
		if officialClusterID.Valid {
			officialID = officialClusterID.String
		}
		for peerRows.Next() {
			var cID, cName, cType, cBaseURL, s3Bucket, s3Endpoint, s3Region string
			var regionID, cellID, clusterClass, healthStatus string
			var foghornHost sql.NullString
			var foghornPort sql.NullInt32
			if err := peerRows.Scan(
				&cID, &cName, &cType, &cBaseURL,
				&s3Bucket, &s3Endpoint, &s3Region,
				&regionID, &cellID, &clusterClass,
				&healthStatus,
				&foghornHost, &foghornPort,
			); err != nil {
				continue
			}
			foghornGrpcAddr, _ := buildAdvertiseAddr(foghornHost, foghornPort)
			var role string
			switch cID {
			case primaryClusterID:
				role = "preferred"
			case officialID:
				role = "official"
			default:
				role = "subscribed"
			}
			resp.ClusterPeers = append(resp.ClusterPeers, &quartermasterpb.TenantClusterPeer{
				ClusterId:       cID,
				ClusterSlug:     dns.SanitizeLabel(cID),
				BaseUrl:         cBaseURL,
				ClusterName:     cName,
				Role:            role,
				ClusterType:     cType,
				FoghornGrpcAddr: foghornGrpcAddr,
				S3Bucket:        s3Bucket,
				S3Endpoint:      s3Endpoint,
				S3Region:        s3Region,
				RegionId:        regionID,
				CellId:          cellID,
				ClusterClass:    clusterClass,
				HealthStatus:    healthStatus,
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
		// Advisory lock keyed on service type — second caller blocks until first commits
		_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, serviceType)
		if err != nil {
			return status.Errorf(codes.Internal, "failed to acquire advisory lock: %v", err)
		}

		err = tx.QueryRowContext(ctx, `
		SELECT service_id FROM quartermaster.services WHERE service_id = $1 OR name = $1
	`, serviceType).Scan(&serviceID)

		if errors.Is(err, sql.ErrNoRows) {
			serviceID = serviceType
			_, err = tx.ExecContext(ctx, `
			INSERT INTO quartermaster.services (service_id, name, plane, type, protocol, is_active, created_at, updated_at)
			VALUES ($1, $2, 'control', $3, $4, true, NOW(), NOW())
		`, serviceID, serviceType, serviceType, protocol)
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
	type queryExecutor interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
		QueryRowContext(context.Context, string, ...any) *sql.Row
	}

	serviceType := req.GetType()
	if serviceType == "" {
		return nil, status.Error(codes.InvalidArgument, "type required")
	}

	exec := queryExecutor(s.db)
	var tx *sql.Tx
	token := req.GetToken()
	if token != "" {
		var err error
		tx, err = s.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to begin transaction: %v", err)
		}
		exec = tx
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
		var kind string
		var expiresAt time.Time
		err := exec.QueryRowContext(ctx, `
			SELECT kind, COALESCE(cluster_id, ''), expires_at
			FROM quartermaster.bootstrap_tokens
			WHERE token_hash = $1 AND used_at IS NULL
			FOR UPDATE
		`, hashBootstrapToken(token)).Scan(&kind, &tokenBoundClusterID, &expiresAt)
		if errors.Is(err, sql.ErrNoRows) || kind != "service" || time.Now().After(expiresAt) {
			return nil, status.Error(codes.Unauthenticated, "invalid bootstrap token")
		}
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
		var isActive bool
		err := exec.QueryRowContext(ctx, `
			SELECT is_active FROM quartermaster.infrastructure_clusters WHERE cluster_id = $1
		`, requestClusterID).Scan(&isActive)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "cluster '%s' not found", requestClusterID)
		}
		if err != nil {
			return nil, status.Errorf(codes.Internal, "database error: %v", err)
		}
		if !isActive {
			return nil, status.Errorf(codes.FailedPrecondition, "cluster '%s' is not active", requestClusterID)
		}
		clusterID = requestClusterID
	} else {
		// No token-bound cluster and no request cluster_id
		// Fallback: only allow if exactly 1 active cluster exists (dev convenience)
		var activeClusters []string
		rows, err := exec.QueryContext(ctx, `
			SELECT cluster_id FROM quartermaster.infrastructure_clusters WHERE is_active = true
		`)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "database error: %v", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var cid string
			if err := rows.Scan(&cid); err != nil {
				return nil, status.Errorf(codes.Internal, "database error: %v", err)
			}
			activeClusters = append(activeClusters, cid)
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
		var nodeClusterID string
		var resolvedNodeIP sql.NullString
		err := exec.QueryRowContext(ctx, `
			SELECT cluster_id,
			       COALESCE(host(wireguard_ip), host(internal_ip), host(external_ip))
			FROM quartermaster.infrastructure_nodes
			WHERE node_id = $1
		`, *req.NodeId).Scan(&nodeClusterID, &resolvedNodeIP)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "node '%s' not found", *req.NodeId)
		}
		if err != nil {
			return nil, status.Errorf(codes.Internal, "database error: %v", err)
		}
		var nodeClusterActive bool
		err = exec.QueryRowContext(ctx, `
			SELECT is_active FROM quartermaster.infrastructure_clusters WHERE cluster_id = $1
		`, nodeClusterID).Scan(&nodeClusterActive)
		if errors.Is(err, sql.ErrNoRows) || !nodeClusterActive {
			return nil, status.Errorf(codes.InvalidArgument, "node '%s' belongs to inactive or unknown cluster '%s'", *req.NodeId, nodeClusterID)
		}
		if err != nil {
			return nil, status.Errorf(codes.Internal, "database error: %v", err)
		}
		if !dns.IsPoolAssignedServiceType(serviceType) && nodeClusterID != clusterID {
			return nil, status.Errorf(codes.InvalidArgument, "node '%s' belongs to cluster '%s', not '%s'", *req.NodeId, nodeClusterID, clusterID)
		}
		if nodeClusterID != "" {
			registrationClusterID = nodeClusterID
		}
		if resolvedNodeIP.Valid {
			nodeIP = strings.TrimSpace(resolvedNodeIP.String)
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
			var matchedNodeID string
			_ = exec.QueryRowContext(ctx, `
				SELECT node_id FROM quartermaster.infrastructure_nodes
				WHERE cluster_id = $1
				  AND (wireguard_ip = $2::inet OR internal_ip = $2::inet OR external_ip = $2::inet)
				LIMIT 1
			`, registrationClusterID, matchIP).Scan(&matchedNodeID)
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
	var existingRow *sql.Row
	if isFoghornControlListener && requestedInstanceID != "" {
		existingRow = exec.QueryRowContext(ctx, `
			SELECT id::text, instance_id FROM quartermaster.service_instances
			WHERE service_id = $1 AND cluster_id = $2 AND protocol = $3 AND instance_id = $4
			ORDER BY updated_at DESC NULLS LAST, started_at DESC NULLS LAST LIMIT 1
		`, serviceID, registrationClusterID, proto, requestedInstanceID)
	} else if resolvedNodeID != nil && isFoghornControlListener {
		existingRow = exec.QueryRowContext(ctx, `
			SELECT id::text, instance_id FROM quartermaster.service_instances
			WHERE service_id = $1 AND cluster_id = $2 AND protocol = $3 AND port = $4
			  AND (node_id = $5 OR node_id IS NULL) AND advertise_host = $6
			ORDER BY updated_at DESC NULLS LAST, started_at DESC NULLS LAST LIMIT 1
		`, serviceID, registrationClusterID, proto, port, *resolvedNodeID, advHost)
	} else if resolvedNodeID != nil {
		existingRow = exec.QueryRowContext(ctx, `
			SELECT id::text, instance_id FROM quartermaster.service_instances
			WHERE service_id = $1 AND cluster_id = $2 AND protocol = $3 AND port = $4
			  AND (node_id = $5 OR node_id IS NULL)
			ORDER BY updated_at DESC NULLS LAST, started_at DESC NULLS LAST LIMIT 1
		`, serviceID, registrationClusterID, proto, port, *resolvedNodeID)
	} else {
		existingRow = exec.QueryRowContext(ctx, `
			SELECT id::text, instance_id FROM quartermaster.service_instances
			WHERE service_id = $1 AND cluster_id = $2 AND protocol = $3 AND port = $4 AND advertise_host = $5
			ORDER BY updated_at DESC NULLS LAST, started_at DESC NULLS LAST LIMIT 1
		`, serviceID, registrationClusterID, proto, port, advHost)
	}
	if scanErr := existingRow.Scan(&existingID, &existingInstanceID); scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		return nil, status.Errorf(codes.Internal, "failed to lookup existing service instance: %v", scanErr)
	}
	registeredNodeID := ""
	if resolvedNodeID != nil {
		registeredNodeID = *resolvedNodeID
	}

	if existingID != "" {
		// Update existing row
		_, err = exec.ExecContext(ctx, `
			UPDATE quartermaster.service_instances
			SET advertise_host = $1,
			    health_endpoint_override = $2,
			    version = $3,
			    node_id = COALESCE($4, node_id),
			    metadata = COALESCE($5::jsonb, metadata),
			    protocol = $6,
			    port = $7,
			    status = 'running',
			    health_status = 'unknown',
			    started_at = COALESCE(started_at, NOW()),
			    stopped_at = NULL,
			    last_health_check = NULL,
			    updated_at = NOW()
			WHERE id = $8::uuid
		`, advHost, healthEndpoint, req.GetVersion(), resolvedNodeID, metadataJSON, proto, port, existingID)
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
		_, err = exec.ExecContext(ctx, `
			INSERT INTO quartermaster.service_instances
				(instance_id, cluster_id, node_id, service_id, protocol, advertise_host, health_endpoint_override, version, port, metadata, status, health_status, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, COALESCE($10::jsonb, '{}'::jsonb), 'running', 'unknown', NOW(), NOW())
		`, instanceID, registrationClusterID, resolvedNodeID, serviceID, proto, advHost, healthEndpoint, req.GetVersion(), port, metadataJSON)
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
	var ownerTenantID sql.NullString
	_ = exec.QueryRowContext(ctx, `
		SELECT owner_tenant_id FROM quartermaster.infrastructure_clusters WHERE cluster_id = $1
	`, clusterID).Scan(&ownerTenantID)

	if token != "" {
		result, err := exec.ExecContext(ctx, `
			UPDATE quartermaster.bootstrap_tokens
			SET used_at = NOW(), usage_count = usage_count + 1
			WHERE token_hash = $1 AND used_at IS NULL
		`, hashBootstrapToken(token))
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to consume bootstrap token: %v", err)
		}
		rowsAffected, rowsErr := result.RowsAffected()
		if rowsErr != nil || rowsAffected != 1 {
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
	_, _ = s.db.ExecContext(ctx, `
		UPDATE quartermaster.service_instances
		SET status = 'stopped', stopped_at = NOW(), updated_at = NOW()
		WHERE service_id = $1 AND cluster_id = $2 AND instance_id != $3
		  AND protocol = $5
		  AND status != 'stopped'
		  AND COALESCE(advertise_host, '') = $4
		  AND COALESCE(port, 0) = $6
	`, serviceID, registrationClusterID, instanceID, advHost, proto, port)
	if isFoghornControlListener {
		_, _ = s.db.ExecContext(ctx, `
			UPDATE quartermaster.service_instances
			SET status = 'stopped', stopped_at = NOW(), updated_at = NOW()
			WHERE service_id = $1 AND cluster_id = $2 AND instance_id != $3
			  AND protocol = $4
			  AND status != 'stopped'
			  AND COALESCE(advertise_host, '') = $5
			  AND COALESCE(port, 0) != $6
		`, serviceID, registrationClusterID, instanceID, proto, advHost, port)
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

	var resp quartermasterpb.NodeOwnerResponse
	var ownerTenantID, tenantName, foghornHost sql.NullString
	var foghornPort sql.NullInt32
	err := s.db.QueryRowContext(ctx, `
		SELECT n.node_id, n.cluster_id, c.cluster_name, c.owner_tenant_id, t.name,
			(SELECT si.advertise_host
			 FROM quartermaster.service_cluster_assignments sca
			 JOIN quartermaster.service_instances si ON si.id = sca.service_instance_id
			 JOIN quartermaster.services svc ON svc.service_id = si.service_id
			 WHERE sca.cluster_id = n.cluster_id AND sca.is_active = true
			   AND svc.type = 'foghorn' AND si.status = 'running'
			   AND si.health_status = 'healthy'
			   AND si.protocol = 'grpc'
			   AND (si.metadata->>'foghorn_listener' = 'internal_control' OR si.port = 18019 OR si.metadata->>'foghorn_listener' = 'control')
			   AND COALESCE(si.advertise_host, '') <> '' AND COALESCE(si.port, 0) > 0
			 ORDER BY CASE WHEN si.metadata->>'foghorn_listener' = 'internal_control' THEN 0 WHEN si.port = 18019 THEN 1 WHEN si.metadata->>'foghorn_listener' = 'control' THEN 2 ELSE 3 END, si.updated_at DESC, si.id ASC LIMIT 1),
			(SELECT si.port
			 FROM quartermaster.service_cluster_assignments sca
			 JOIN quartermaster.service_instances si ON si.id = sca.service_instance_id
			 JOIN quartermaster.services svc ON svc.service_id = si.service_id
			 WHERE sca.cluster_id = n.cluster_id AND sca.is_active = true
			   AND svc.type = 'foghorn' AND si.status = 'running'
			   AND si.health_status = 'healthy'
			   AND si.protocol = 'grpc'
			   AND (si.metadata->>'foghorn_listener' = 'internal_control' OR si.port = 18019 OR si.metadata->>'foghorn_listener' = 'control')
			   AND COALESCE(si.advertise_host, '') <> '' AND COALESCE(si.port, 0) > 0
			 ORDER BY CASE WHEN si.metadata->>'foghorn_listener' = 'internal_control' THEN 0 WHEN si.port = 18019 THEN 1 WHEN si.metadata->>'foghorn_listener' = 'control' THEN 2 ELSE 3 END, si.updated_at DESC, si.id ASC LIMIT 1)
		FROM quartermaster.infrastructure_nodes n
		JOIN quartermaster.infrastructure_clusters c ON n.cluster_id = c.cluster_id
		LEFT JOIN quartermaster.tenants t ON c.owner_tenant_id = t.id
		WHERE n.node_id = $1
	`, nodeID).Scan(&resp.NodeId, &resp.ClusterId, &resp.ClusterName, &ownerTenantID, &tenantName, &foghornHost, &foghornPort)

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

	if ownerTenantID.Valid {
		resp.OwnerTenantId = &ownerTenantID.String
	}
	if tenantName.Valid {
		resp.TenantName = &tenantName.String
	}
	if addr, ok := buildAdvertiseAddr(foghornHost, foghornPort); ok {
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

	// For pool-assigned services (foghorn/chandler/livepeer-gateway) the
	// logical media cluster lives in service_cluster_assignments; the physical
	// si.cluster_id stays bound to the host. Tenant access and the requested
	// cluster filter therefore both apply against sca.cluster_id, not si.
	clusterCol := "si.cluster_id"
	extraJoin := ""
	if isPool {
		clusterCol = "sca.cluster_id"
		extraJoin = "\n\t\tJOIN quartermaster.service_cluster_assignments sca ON sca.service_instance_id = si.id AND sca.is_active = TRUE\n\t\tJOIN quartermaster.infrastructure_clusters c ON c.cluster_id = sca.cluster_id"
	}

	// Build dynamic query
	args := []any{serviceType}
	argIdx := 2

	whereClause := "WHERE s.type = $1 AND si.status IN ('running','starting','active')"
	if serviceType == "foghorn" {
		whereClause += " AND si.protocol = 'grpc' AND (si.metadata->>'foghorn_listener' = 'internal_control' OR si.port = 18019 OR si.metadata->>'foghorn_listener' = 'control')"
	}

	switch {
	case ctxkeys.GetAuthType(ctx) == "service":
		// Trusted service-to-service caller (e.g. Foghorn discovering the
		// gateways assigned to a media cluster). Service tokens carry no
		// tenant_id, so without this branch the empty-tenant path below would
		// restrict to the default cluster and return nothing for every other
		// media cluster — silently defeating per-cluster discovery. The explicit
		// cluster_id filter below scopes the result.
	case tenantID != "":
		// Authenticated: Filter by subscription OR ownership
		whereClause += fmt.Sprintf(` AND (%s IN (
			SELECT tca.cluster_id FROM quartermaster.tenant_cluster_access tca
			WHERE tca.tenant_id = $%d AND tca.is_active = true
		) OR %s IN (
			SELECT ic.cluster_id FROM quartermaster.infrastructure_clusters ic
			WHERE ic.owner_tenant_id = $%d
		))`, clusterCol, argIdx, clusterCol, argIdx)
		args = append(args, tenantID)
		argIdx++
	default:
		// Unauthenticated: Filter by default cluster only
		whereClause += fmt.Sprintf(` AND %s IN (
			SELECT ic.cluster_id FROM quartermaster.infrastructure_clusters ic
			WHERE ic.is_default_cluster = true
		)`, clusterCol)
	}

	// Optional: scope to specific cluster
	if clusterID := req.GetClusterId(); clusterID != "" {
		whereClause += fmt.Sprintf(" AND %s = $%d", clusterCol, argIdx)
		args = append(args, clusterID)
		argIdx++
	}

	// Direction-aware keyset condition
	if params.Cursor != nil {
		if params.Direction == pagination.Backward {
			whereClause += fmt.Sprintf(" AND (si.created_at, si.id) > ($%d, $%d)", argIdx, argIdx+1)
		} else {
			whereClause += fmt.Sprintf(" AND (si.created_at, si.id) < ($%d, $%d)", argIdx, argIdx+1)
		}
		args = append(args, params.Cursor.Timestamp, params.Cursor.ID)
		argIdx += 2
	}

	// Direction-aware ORDER BY
	orderDir := "DESC"
	if params.Direction == pagination.Backward {
		orderDir = "ASC"
	}

	// public_instance_host is per-node physical infrastructure identity, surfaced
	// only to service callers (e.g. Foghorn's broadcaster fanout) — the same
	// boundary ListServiceInstancesByType enforces. Tenant/user discovery gets only
	// the pooled public_host.
	isServiceCaller := ctxkeys.GetAuthType(ctx) == "service"
	physicalSynthesis := isServiceCaller && isPool && dns.IsPhysicalEndpointServiceType(serviceType)

	selectClause := `si.id, si.instance_id, si.service_id, si.cluster_id, si.node_id,
		       si.protocol, si.advertise_host, si.port, si.health_endpoint_override, si.status, si.health_status, COALESCE(si.metadata, '{}'::jsonb),
		       si.last_health_check, si.created_at, si.updated_at`
	if isPool {
		// Override cluster_id with the assignment cluster, and pull cluster
		// metadata so the handler can synthesize per-cluster public_host.
		selectClause = `si.id, si.instance_id, si.service_id, sca.cluster_id, si.node_id,
		       si.protocol, si.advertise_host, si.port, si.health_endpoint_override, si.status, si.health_status, COALESCE(si.metadata, '{}'::jsonb),
		       si.last_health_check, si.created_at, si.updated_at, c.cluster_name, c.base_url`
	}
	if physicalSynthesis {
		// Compute health freshness against the DB clock (NOW()), the SAME predicate
		// Navigator's ListServiceInstancesByType uses, so app/DB clock skew can't make
		// discovery and publication disagree at the freshness boundary. The seconds
		// value is a server-config int (not user input), so direct interpolation is
		// injection-safe. Only added when public_instance_host can actually be
		// synthesized, so other discovery paths keep their column shape.
		selectClause += fmt.Sprintf(",\n\t\t       (si.last_health_check IS NOT NULL AND si.last_health_check > NOW() - INTERVAL '%d seconds') AS health_fresh", s.physicalEndpointStaleSeconds)
	}

	query := fmt.Sprintf(`
		SELECT %s
		FROM quartermaster.service_instances si
		JOIN quartermaster.services s ON si.service_id = s.service_id%s
		%s
		ORDER BY si.created_at %s, si.id %s
		LIMIT $%d
	`, selectClause, extraJoin, whereClause, orderDir, orderDir, argIdx)
	args = append(args, params.Limit+1)

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

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var instances []*quartermasterpb.ServiceInstance
	for rows.Next() {
		var inst quartermasterpb.ServiceInstance
		var nodeID, host, healthEndpoint sql.NullString
		var lastHealthCheck sql.NullTime
		var metadataJSON []byte
		var createdAt, updatedAt time.Time
		var clusterName, clusterBaseURL sql.NullString
		var healthFresh bool

		scanTargets := []any{
			&inst.Id, &inst.InstanceId, &inst.ServiceId, &inst.ClusterId, &nodeID,
			&inst.Protocol, &host, &inst.Port, &healthEndpoint, &inst.Status, &inst.HealthStatus, &metadataJSON,
			&lastHealthCheck, &createdAt, &updatedAt,
		}
		if isPool {
			scanTargets = append(scanTargets, &clusterName, &clusterBaseURL)
		}
		if physicalSynthesis {
			scanTargets = append(scanTargets, &healthFresh) // last column: health_fresh
		}
		if err := rows.Scan(scanTargets...); err != nil {
			// Fail closed: a scan/conversion error skipped here would yield a
			// truncated-but-"successful" set that Foghorn caches as the gateway
			// fanout. rows.Err() does not cover an already-swallowed Scan error.
			return nil, status.Errorf(codes.Internal, "service discovery scan error: %v", err)
		}

		if nodeID.Valid {
			inst.NodeId = &nodeID.String
		}
		if host.Valid {
			inst.Host = &host.String
		}
		if healthEndpoint.Valid {
			inst.HealthEndpoint = &healthEndpoint.String
		}
		if lastHealthCheck.Valid {
			inst.LastHealthCheck = timestamppb.New(lastHealthCheck.Time)
		}
		inst.Metadata = unmarshalStringMapJSON(metadataJSON)
		inst.CreatedAt = timestamppb.New(createdAt)
		inst.UpdatedAt = timestamppb.New(updatedAt)

		if isPool {
			// Synthesize the per-assignment public_host. For an M:N pool, the
			// same physical instance returns a different public_host per
			// requested cluster, so this cannot be stored as static metadata.
			if publicHost := synthesizePublicHost(serviceType, inst.ClusterId, clusterName.String, clusterBaseURL.String); publicHost != "" {
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
				inst.HealthStatus == "healthy" && healthFresh
			if isServiceCaller && physicallyEligible && nodeID.Valid && dns.IsPhysicalEndpointServiceType(serviceType) {
				if instanceHost, ok := dns.InfraInstanceFQDN(serviceType, nodeID.String, s.platformRootDomain); ok {
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
	// Fail closed on a partial read: a truncated-but-"successful" gateway set
	// would be cached by Foghorn and skew the broadcaster fanout. Same class as
	// the inventory/list reads.
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "service discovery iteration error: %v", err)
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

	rows, err := s.db.QueryContext(ctx, `
		SELECT si.id, si.instance_id, COALESCE(si.advertise_host, '') AS host,
		       COALESCE(si.port, 0) AS port, si.status, si.created_at,
		       COALESCE(sca.cluster_id, '') AS assigned_cluster
		FROM quartermaster.service_instances si
		JOIN quartermaster.services svc ON svc.service_id = si.service_id
		LEFT JOIN quartermaster.service_cluster_assignments sca
		  ON sca.service_instance_id = si.id AND sca.is_active = true
		WHERE svc.type = $1
		ORDER BY assigned_cluster, si.started_at ASC
	`, serviceType)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	clusterMap := make(map[string]*quartermasterpb.ServicePoolClusterEntry)
	seenInstances := make(map[string]bool)
	var total, unassigned, assigned int32
	var assignments []*quartermasterpb.ServiceInstanceAssignment

	for rows.Next() {
		var id, instanceID, host, instStatus, assignedCluster string
		var port int32
		var createdAt time.Time
		if err := rows.Scan(&id, &instanceID, &host, &port, &instStatus, &createdAt, &assignedCluster); err != nil {
			// Fail closed: a skipped row yields a truncated-but-"successful" pool
			// status (wrong counts/assignments), consistent with the discovery and
			// DNS readers.
			return nil, status.Errorf(codes.Internal, "service pool status scan error: %v", err)
		}

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

	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "service pool status iteration error: %v", err)
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
	if ids := req.GetInstanceIds(); len(ids) > 0 {
		// DELETE ... RETURNING captures the affected clusters atomically with the
		// mutation, so a failed read can't commit the change without a wake.
		rows, qErr := s.db.QueryContext(ctx, `
			DELETE FROM quartermaster.service_cluster_assignments
			WHERE service_instance_id IN (
				SELECT si.id FROM quartermaster.service_instances si
				JOIN quartermaster.services svc ON svc.service_id = si.service_id
				WHERE si.id = ANY($1) AND svc.type = $2
			)
			RETURNING cluster_id
		`, pq.Array(ids), serviceType)
		if qErr != nil {
			return nil, status.Errorf(codes.Internal, "database error: %v", qErr)
		}
		affectedClusters, released, err = scanDeletedClusters(rows)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "read released clusters: %v", err)
		}
	} else if req.GetCount() > 0 && req.GetFromClusterId() != "" {
		affectedClusters = []string{req.GetFromClusterId()}
		// Remove N oldest assignments from a specific cluster.
		res, eErr := s.db.ExecContext(ctx, `
			DELETE FROM quartermaster.service_cluster_assignments
			WHERE id IN (
				SELECT sca.id
				FROM quartermaster.service_cluster_assignments sca
				JOIN quartermaster.service_instances si ON si.id = sca.service_instance_id
				JOIN quartermaster.services svc ON svc.service_id = si.service_id
				WHERE svc.type = $3
				  AND sca.cluster_id = $1
				  AND sca.is_active = true
				  AND si.status = 'running'
				ORDER BY si.started_at ASC
				LIMIT $2
			)
		`, req.GetFromClusterId(), req.GetCount(), serviceType)
		if eErr != nil {
			return nil, status.Errorf(codes.Internal, "database error: %v", eErr)
		}
		if released, err = res.RowsAffected(); err != nil {
			return nil, status.Errorf(codes.Internal, "released count: %v", err)
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
	rows, err := s.db.QueryContext(ctx, `
		DELETE FROM quartermaster.service_cluster_assignments
		WHERE service_instance_id = (
			SELECT si.id FROM quartermaster.service_instances si
			JOIN quartermaster.services svc ON svc.service_id = si.service_id
			WHERE si.id = $1 AND svc.type = $2
		)
		RETURNING cluster_id
	`, instanceID, serviceType)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	drainedClusters, _, err := scanDeletedClusters(rows)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read drained clusters: %v", err)
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

	var exists bool
	if err := s.db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM quartermaster.infrastructure_clusters WHERE cluster_id = $1 AND is_active = true)",
		clusterID).Scan(&exists); err != nil || !exists {
		return nil, status.Error(codes.NotFound, "cluster not found or inactive")
	}

	if ids := req.GetInstanceIds(); len(ids) > 0 {
		for _, instID := range ids {
			// ON CONFLICT preserves the existing source: a runtime
			// AssignServiceToCluster against a GitOps-owned row reactivates
			// without flipping ownership. Only explicit adopt/unmanage
			// operations flip provenance.
			res, err := s.db.ExecContext(ctx, `
				INSERT INTO quartermaster.service_cluster_assignments (service_instance_id, cluster_id, source)
				SELECT si.id, $1, 'runtime'
				FROM quartermaster.service_instances si
				JOIN quartermaster.services svc ON svc.service_id = si.service_id
				WHERE si.id = $2::uuid AND svc.type = $3 AND si.status = 'running'
				ON CONFLICT (service_instance_id, cluster_id) DO UPDATE SET is_active = true, updated_at = NOW()
			`, clusterID, instID, serviceType)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "failed to assign instance %s: %v", instID, err)
			}
			if affected, rowsErr := res.RowsAffected(); rowsErr != nil {
				return nil, status.Errorf(codes.Internal, "failed to confirm assignment for instance %s: %v", instID, rowsErr)
			} else if affected == 0 {
				return nil, status.Errorf(codes.NotFound, "%s instance %s not found or not running", serviceType, instID)
			}
		}
	} else if count := req.GetCount(); count > 0 {
		// Same ON CONFLICT contract as the instance-ids branch: preserve
		// existing source on reactivation.
		res, err := s.db.ExecContext(ctx, `
			INSERT INTO quartermaster.service_cluster_assignments (service_instance_id, cluster_id, source)
			SELECT si.id, $1, 'runtime'
			FROM quartermaster.service_instances si
			JOIN quartermaster.services svc ON svc.service_id = si.service_id
			LEFT JOIN quartermaster.service_cluster_assignments sca
			  ON sca.service_instance_id = si.id AND sca.is_active = true
			WHERE svc.type = $3 AND si.status = 'running'
			GROUP BY si.id
			ORDER BY COUNT(sca.id) ASC, si.started_at ASC, si.id ASC
			LIMIT $2
			ON CONFLICT (service_instance_id, cluster_id) DO UPDATE SET is_active = true, updated_at = NOW()
		`, clusterID, count, serviceType)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to assign %s instances: %v", serviceType, err)
		}
		if affected, rowsErr := res.RowsAffected(); rowsErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to confirm %s assignment: %v", serviceType, rowsErr)
		} else if affected < int64(count) {
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

	_, err = s.db.ExecContext(ctx, `
		DELETE FROM quartermaster.service_cluster_assignments
		WHERE cluster_id = $1
		  AND service_instance_id IN (
			SELECT si.id FROM quartermaster.service_instances si
			JOIN quartermaster.services svc ON svc.service_id = si.service_id
			WHERE si.id = ANY($2::uuid[]) AND svc.type = $3
		  )
	`, clusterID, pq.Array(ids), serviceType)
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
	var maxOwnedClusters, currentOwnedClusters int
	var isProvider bool
	err := s.db.QueryRowContext(ctx, `
		SELECT max_owned_clusters, is_provider,
		       (SELECT COUNT(*) FROM quartermaster.infrastructure_clusters WHERE owner_tenant_id = $1)
		FROM quartermaster.tenants WHERE id = $1
	`, tenantID).Scan(&maxOwnedClusters, &isProvider, &currentOwnedClusters)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "tenant not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !isProvider && currentOwnedClusters >= maxOwnedClusters {
		return nil, status.Errorf(codes.ResourceExhausted, "tenant has reached maximum owned clusters limit (%d)", maxOwnedClusters)
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
		var preferredRegion sql.NullString
		if regionErr := s.db.QueryRowContext(ctx, `
			SELECT pc.region_id
			FROM quartermaster.tenants t
			JOIN quartermaster.infrastructure_clusters pc
			  ON pc.cluster_id = t.primary_cluster_id
			 AND pc.is_active = true
			WHERE t.id = $1
		`, tenantID).Scan(&preferredRegion); regionErr != nil && !errors.Is(regionErr, sql.ErrNoRows) {
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

	if _, err = tx.ExecContext(ctx, `
		INSERT INTO quartermaster.infrastructure_clusters (
			id, cluster_id, cluster_name, cluster_type, deployment_model,
			owner_tenant_id, base_url,
			max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps,
			visibility, pricing_model, short_description,
			region_id, cell_id, cluster_class, control_cell_id, eligible_serving_cell_ids,
			health_status, is_active, created_at, updated_at
		) VALUES (
			$1, $2, $3, 'edge', 'self-hosted',
			$4, $9,
			0, 0, 0,
			'private', 'free_unmetered', $5,
			NULLIF($8::text, ''), $2, 'tenant_private', $7::text, ARRAY[$7::text]::TEXT[],
			'unknown', true, $6, $6
		)
	`, id, clusterID, clusterName, tenantID, req.ShortDescription, now, controlCell.controlCellID, regionForRow, strings.TrimSpace(controlCell.baseURL)); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create cluster: %v", err)
	}

	if _, err = tx.ExecContext(ctx, `
		INSERT INTO quartermaster.tenant_cluster_access (
			tenant_id, cluster_id, access_level, subscription_status, is_active, created_at, updated_at
		) VALUES ($1, $2, 'owner', 'active', true, NOW(), NOW())
	`, tenantID, clusterID); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to auto-subscribe owner to cluster: %v", err)
	}

	// EnableSelfHosting attaches a tenant's Foghorn to a cluster. New rows
	// are runtime-owned; ON CONFLICT preserves source (a GitOps default for
	// this Foghorn would not be silently demoted to runtime here).
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO quartermaster.service_cluster_assignments (service_instance_id, cluster_id, source)
		SELECT si.id, $2, 'runtime'
		FROM quartermaster.service_instances si
		JOIN quartermaster.services svc ON svc.service_id = si.service_id
		WHERE si.id = $1::uuid AND svc.type = 'foghorn'
		ON CONFLICT (service_instance_id, cluster_id) DO UPDATE SET is_active = true, updated_at = NOW()
	`, controlCell.instanceID, clusterID); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to assign Foghorn to cluster: %v", err)
	}

	// Create bootstrap token
	tokenID := uuid.New().String()
	token, err := generateSecureToken(32)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate token: %v", err)
	}
	expiresAt := now.Add(30 * 24 * time.Hour)

	if _, err = tx.ExecContext(ctx, `
		INSERT INTO quartermaster.bootstrap_tokens (
			id, token_hash, token_prefix, kind, name, tenant_id, cluster_id, expires_at, created_by, created_at
		) VALUES ($1, $2, $3, 'edge_node', $4, $5, $6, $7, $5, NOW())
	`, tokenID, hashBootstrapToken(token), tokenPrefix(token), fmt.Sprintf("Bootstrap token for %s", clusterName), tenantID, clusterID, expiresAt); err != nil {
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
	if lifecycleActor {
		err = database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			return s.db.QueryRowContext(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM quartermaster.infrastructure_clusters
					WHERE cluster_id = $1 AND is_active = true
				)
			`, clusterID).Scan(&authorized)
		})
	} else {
		err = database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			return s.db.QueryRowContext(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM quartermaster.infrastructure_clusters
					WHERE cluster_id = $1 AND owner_tenant_id = $2 AND is_active = true
					UNION
					SELECT 1 FROM quartermaster.tenant_cluster_access
					WHERE cluster_id = $1
					  AND tenant_id = $2
					  AND access_level = 'owner'
					  AND subscription_status = 'active'
					  AND is_active = true
				)
			`, clusterID, tenantID).Scan(&authorized)
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
		_, execErr := s.db.ExecContext(ctx, `
				INSERT INTO quartermaster.bootstrap_tokens (
					id, token_hash, token_prefix, kind, name, tenant_id, cluster_id, expires_at, created_by, created_at
				) VALUES ($1, $2, $3, 'edge_node', $4, $5, $6, $7, $5, NOW())
			`, tokenID, hashBootstrapToken(token), tokenPrefix(token), tokenName, tenantID, clusterID, expiresAt)
		return execErr
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

	query := `SELECT id, name, primary_cluster_id FROM quartermaster.tenants WHERE is_active = true AND `
	var arg string
	if subdomain != "" {
		query += `subdomain = $1`
		arg = subdomain
	} else {
		query += `custom_domain = $1`
		arg = domain
	}

	err := s.db.QueryRowContext(ctx, query, arg).Scan(&tenantID, &tenantName, &primaryClusterID)
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

	rows, err := s.db.QueryContext(ctx, `
		SELECT alias, tenant_id::text
		FROM quartermaster.bootstrap_tenant_aliases
		WHERE alias = ANY($1)
	`, pq.Array(aliases))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "query alias map: %v", err)
	}
	defer func() { _ = rows.Close() }()

	mapping := make(map[string]string, len(aliases))
	for rows.Next() {
		var alias, id string
		if scanErr := rows.Scan(&alias, &id); scanErr != nil {
			return nil, status.Errorf(codes.Internal, "scan alias row: %v", scanErr)
		}
		mapping[alias] = id
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, status.Errorf(codes.Internal, "iterate alias rows: %v", rowsErr)
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

	// Build dynamic query
	args := []any{}
	argIdx := 1
	whereClause := ""

	// Direction-aware keyset condition
	if params.Cursor != nil {
		if params.Direction == pagination.Backward {
			whereClause = fmt.Sprintf("WHERE (created_at, id) > ($%d, $%d)", argIdx, argIdx+1)
		} else {
			whereClause = fmt.Sprintf("WHERE (created_at, id) < ($%d, $%d)", argIdx, argIdx+1)
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
		SELECT id, name, subdomain, custom_domain, logo_url, primary_color, secondary_color,
		       deployment_tier, deployment_model,
		       primary_cluster_id, official_cluster_id, kafka_topic_prefix, kafka_brokers, database_url,
		       is_active, created_at, updated_at
		FROM quartermaster.tenants
		%s
		ORDER BY created_at %s, id %s
		LIMIT $%d
	`, whereClause, orderDir, orderDir, argIdx)
	args = append(args, params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var tenants []*quartermasterpb.Tenant
	for rows.Next() {
		tenant, err := scanTenant(rows)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to scan tenant")
			continue
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

// ListActiveTenants returns all active tenant IDs for billing batch processing.
// Called by Purser billing job to avoid cross-service DB access.
func (s *QuartermasterServer) ListActiveTenants(ctx context.Context, req *quartermasterpb.ListActiveTenantsRequest) (*quartermasterpb.ListActiveTenantsResponse, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM quartermaster.tenants WHERE is_active = true
	`)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var tenantIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			s.logger.WithError(err).Warn("Failed to scan tenant ID")
			continue
		}
		tenantIDs = append(tenantIDs, id)
	}

	return &quartermasterpb.ListActiveTenantsResponse{
		TenantIds: tenantIDs,
	}, nil
}

// CreateTenant creates a new tenant
func (s *QuartermasterServer) CreateTenant(ctx context.Context, req *quartermasterpb.CreateTenantRequest) (*quartermasterpb.CreateTenantResponse, error) {
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

	// 1. Insert into quartermaster.tenants
	_, err = tx.ExecContext(ctx, `
		INSERT INTO quartermaster.tenants (id, name, subdomain, custom_domain, logo_url, primary_color, secondary_color,
		                                   deployment_tier, deployment_model,
		                                   is_active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, true, $10, $10)
	`, tenantID, name, subdomain, req.CustomDomain, req.LogoUrl,
		req.GetPrimaryColor(), req.GetSecondaryColor(),
		req.GetDeploymentTier(), req.GetDeploymentModel(), now)

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
		_, err = tx.ExecContext(ctx, `
			INSERT INTO quartermaster.tenant_attribution (
				tenant_id, signup_channel, signup_method,
				utm_source, utm_medium, utm_campaign, utm_content, utm_term,
				http_referer, landing_page, referral_code, is_agent, metadata
			) VALUES (
				$1, $2, $3,
				$4, $5, $6, $7, $8,
				$9, $10, $11, $12, $13
			)
			ON CONFLICT (tenant_id) DO NOTHING
		`, tenantID, attribution.GetSignupChannel(), attribution.GetSignupMethod(),
			attribution.GetUtmSource(), attribution.GetUtmMedium(), attribution.GetUtmCampaign(), attribution.GetUtmContent(), attribution.GetUtmTerm(),
			attribution.GetHttpReferer(), attribution.GetLandingPage(), attribution.GetReferralCode(), attribution.GetIsAgent(), metadataJSON)
		if err != nil {
			s.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to insert tenant attribution")
		}
		if attribution.GetReferralCode() != "" {
			if _, refErr := tx.ExecContext(ctx, `
				UPDATE quartermaster.referral_codes
				SET current_uses = current_uses + 1
				WHERE code = $1 AND is_active = true
			`, attribution.GetReferralCode()); refErr != nil {
				s.logger.WithError(refErr).WithField("referral_code", attribution.GetReferralCode()).Warn("Failed to increment referral code usage")
			}
		}
	}

	// 2. Find the default cluster for auto-subscription
	var defaultClusterID sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT cluster_id FROM quartermaster.infrastructure_clusters
		WHERE is_default_cluster = true AND is_active = true LIMIT 1
	`).Scan(&defaultClusterID)

	if errors.Is(err, sql.ErrNoRows) {
		s.logger.WithField("tenant_id", tenantID).Warn("No default cluster found for auto-subscription. Tenant created without default cluster access.")
		// This is not a fatal error for tenant creation, just a warning. Continue without subscription.
	} else if err != nil {
		s.logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to query default cluster during tenant creation")
		return nil, status.Errorf(codes.Internal, "failed to find default cluster for auto-subscription: %v", err)
	} else if defaultClusterID.Valid {
		// 3. Auto-subscribe the new tenant to the default cluster
		_, err = tx.ExecContext(ctx, `
			INSERT INTO quartermaster.tenant_cluster_access
			(tenant_id, cluster_id, access_level, is_active, created_at, updated_at)
			VALUES ($1, $2, 'subscriber', true, $3, $3)
			ON CONFLICT (tenant_id, cluster_id) DO NOTHING
		`, tenantID, defaultClusterID.String, now)
		if err != nil {
			s.logger.WithError(err).WithFields(logging.Fields{
				"tenant_id":  tenantID,
				"cluster_id": defaultClusterID.String,
			}).Error("Failed to auto-subscribe tenant to default cluster")
			return nil, status.Errorf(codes.Internal, "failed to auto-subscribe tenant to default cluster: %v", err)
		}

		// 4. Set official_cluster_id to the default cluster (billing-tier coverage)
		if _, clusterErr := tx.ExecContext(ctx, `
			UPDATE quartermaster.tenants SET official_cluster_id = $1 WHERE id = $2
		`, defaultClusterID.String, tenantID); clusterErr != nil {
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
	var updates []string
	var args []any
	argIdx := 1
	changedFields := []string{}
	var previousClusterID sql.NullString
	var previousCustomDomain sql.NullString
	var previousSubdomain sql.NullString

	if req.Name != nil {
		updates = append(updates, fmt.Sprintf("name = $%d", argIdx))
		args = append(args, *req.Name)
		argIdx++
		changedFields = append(changedFields, "name")
	}
	if req.Subdomain != nil {
		subdomain := strings.ToLower(strings.TrimSpace(*req.Subdomain))
		if subdomain != "" && dns.IsReservedTenantSlug(subdomain, s.activeClusterSlugs(ctx)) {
			return nil, status.Errorf(codes.InvalidArgument, "subdomain %q is reserved or invalid", subdomain)
		}
		updates = append(updates, fmt.Sprintf("subdomain = $%d", argIdx))
		if subdomain == "" {
			args = append(args, nil)
		} else {
			args = append(args, subdomain)
		}
		argIdx++
		changedFields = append(changedFields, "subdomain")
	}
	if req.CustomDomain != nil {
		updates = append(updates, fmt.Sprintf("custom_domain = $%d", argIdx))
		args = append(args, *req.CustomDomain)
		argIdx++
		changedFields = append(changedFields, "custom_domain")
	}
	if req.LogoUrl != nil {
		updates = append(updates, fmt.Sprintf("logo_url = $%d", argIdx))
		args = append(args, *req.LogoUrl)
		argIdx++
		changedFields = append(changedFields, "logo_url")
	}
	if req.PrimaryColor != nil {
		updates = append(updates, fmt.Sprintf("primary_color = $%d", argIdx))
		args = append(args, *req.PrimaryColor)
		argIdx++
		changedFields = append(changedFields, "primary_color")
	}
	if req.SecondaryColor != nil {
		updates = append(updates, fmt.Sprintf("secondary_color = $%d", argIdx))
		args = append(args, *req.SecondaryColor)
		argIdx++
		changedFields = append(changedFields, "secondary_color")
	}
	if req.DeploymentTier != nil {
		updates = append(updates, fmt.Sprintf("deployment_tier = $%d", argIdx))
		args = append(args, strings.TrimSpace(*req.DeploymentTier))
		argIdx++
		changedFields = append(changedFields, "deployment_tier")
	}
	if req.DeploymentModel != nil {
		updates = append(updates, fmt.Sprintf("deployment_model = $%d", argIdx))
		args = append(args, strings.TrimSpace(*req.DeploymentModel))
		argIdx++
		changedFields = append(changedFields, "deployment_model")
	}
	if req.PrimaryClusterId != nil {
		updates = append(updates, fmt.Sprintf("primary_cluster_id = $%d", argIdx))
		args = append(args, *req.PrimaryClusterId)
		argIdx++
		changedFields = append(changedFields, "primary_cluster_id")
	}
	if req.IsActive != nil {
		updates = append(updates, fmt.Sprintf("is_active = $%d", argIdx))
		args = append(args, *req.IsActive)
		argIdx++
		changedFields = append(changedFields, "is_active")
	}

	if len(updates) == 0 {
		return nil, status.Error(codes.InvalidArgument, "no fields to update")
	}

	updates = append(updates, "updated_at = NOW()")
	query := fmt.Sprintf("UPDATE quartermaster.tenants SET %s WHERE id = $%d", strings.Join(updates, ", "), argIdx)
	args = append(args, tenantID)

	// Wrap the tenant UPDATE and its outbox emits in one transaction so a
	// crash between mutation and emit can't leave the row updated without
	// the corresponding service-event durable.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Read previous values FOR UPDATE inside the tx so concurrent updates to
	// the same tenant serialize. Reading the previous subdomain before the
	// transaction (and without a lock) would let a concurrent a->b and a->c
	// race both observe "a", enqueuing a retire only for "a" and orphaning
	// the intermediate label.
	if req.PrimaryClusterId != nil || req.CustomDomain != nil || req.Subdomain != nil {
		if scanErr := tx.QueryRowContext(ctx, `
			SELECT primary_cluster_id, custom_domain, subdomain
			FROM quartermaster.tenants
			WHERE id = $1
			FOR UPDATE
		`, tenantID).Scan(&previousClusterID, &previousCustomDomain, &previousSubdomain); scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
			return nil, status.Errorf(codes.Internal, "previous-value lookup: %v", scanErr)
		}
	}

	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update tenant: %v", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to inspect tenant update: %v", err)
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
		downgrade := (req.DeploymentTier != nil && strings.TrimSpace(*req.DeploymentTier) == "free") ||
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
// domain is non-empty AND the tenant is paid + active — the drain worker
// double-checks plan/state at dispatch time so a plan change between
// enqueue and dispatch can't escape.
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
	var tier string
	var isActive bool
	if err := tx.QueryRowContext(ctx, `
		SELECT deployment_tier, is_active
		FROM quartermaster.tenants
		WHERE id = $1::uuid
	`, tenantID).Scan(&tier, &isActive); err != nil {
		return fmt.Errorf("lookup tier: %w", err)
	}
	if !isActive || tier == "" || tier == "free" {
		return nil
	}
	if _, err := s.EnqueueNavigatorCustomDomainTx(ctx, tx, tenantID, newDomain, "ensure"); err != nil {
		return fmt.Errorf("enqueue ensure: %w", err)
	}
	return nil
}

// enqueueTenantAliasEnsureTx enqueues a durable Navigator ensure for the
// tenant's subdomain alias inside the caller's tx — but only when the tenant
// warrants one: active, paid tier, and holding at least one active cluster
// subscription. This is the same condition the backstop reconciler uses, so
// ensure never creates an alias the backstop would then reap. The resolved
// decision rides the row, so the drain worker never re-derives it. When
// eligible but missing a subdomain and generateIfMissing is set, a DNS-safe
// label is generated and persisted first. The tenant row is locked FOR UPDATE
// so concurrent generators serialize.
func (s *QuartermasterServer) enqueueTenantAliasEnsureTx(ctx context.Context, tx *sql.Tx, tenantID string, generateIfMissing bool) error {
	var name string
	var subdomain sql.NullString
	var tier string
	var isActive, hasCluster bool
	if err := tx.QueryRowContext(ctx, `
		SELECT t.name, t.subdomain, t.deployment_tier, t.is_active,
		       EXISTS (SELECT 1 FROM quartermaster.tenant_cluster_access tca
		               WHERE tca.tenant_id = t.id AND tca.is_active = TRUE) AS has_cluster
		FROM quartermaster.tenants t
		WHERE t.id = $1::uuid
		FOR UPDATE
	`, tenantID).Scan(&name, &subdomain, &tier, &isActive, &hasCluster); err != nil {
		return fmt.Errorf("lookup tenant for alias ensure: %w", err)
	}
	if !isActive || tier == "" || tier == "free" || !hasCluster {
		return nil // not eligible for an alias (matches the backstop's "want")
	}
	label := strings.TrimSpace(subdomain.String)
	if label == "" {
		if !generateIfMissing {
			return nil
		}
		generated, genErr := s.generateAvailableTenantSubdomain(ctx, name)
		if genErr != nil {
			return fmt.Errorf("generate subdomain: %w", genErr)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE quartermaster.tenants
			SET subdomain = $1, updated_at = NOW()
			WHERE id = $2::uuid
		`, generated, tenantID); err != nil {
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
// alias: it is active, on a paid tier, and holds at least one active cluster
// subscription. A downgrade/deactivation only tears the alias down when this
// is false — a tenant can keep the alias via another paid cluster. The
// tenant's own is_active is part of the gate so deactivating a tenant tears
// the alias down even while paid cluster-access rows linger.
func (s *QuartermasterServer) tenantHasPaidClusterAccessTx(ctx context.Context, tx *sql.Tx, tenantID string) (bool, error) {
	var has bool
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM quartermaster.tenant_cluster_access tca
			JOIN quartermaster.tenants t ON t.id = tca.tenant_id
			WHERE tca.tenant_id = $1::uuid
			  AND tca.is_active = TRUE
			  AND t.is_active = TRUE
			  AND t.deployment_tier <> 'free'
		)
	`, tenantID).Scan(&has)
	return has, err
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

	var previousCustomDomain sql.NullString
	var previousSubdomain sql.NullString
	// FOR UPDATE locks the tenant row so a concurrent UpdateTenant cannot
	// commit a new custom_domain/ensure between this read and the teardown
	// enqueue below — otherwise delete would tear down the stale domain only.
	if scanErr := tx.QueryRowContext(ctx, `
		SELECT custom_domain, subdomain
		FROM quartermaster.tenants
		WHERE id = $1 AND is_active = TRUE
		FOR UPDATE
	`, tenantID).Scan(&previousCustomDomain, &previousSubdomain); scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tenant not found")
		}
		return nil, status.Errorf(codes.Internal, "lookup tenant domains: %v", scanErr)
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE quartermaster.tenants SET is_active = FALSE, updated_at = NOW()
		WHERE id = $1 AND is_active = TRUE
	`, tenantID)
	if err != nil {
		s.logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to delete tenant")
		return nil, status.Errorf(codes.Internal, "failed to delete tenant: %v", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to inspect tenant delete: %v", err)
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

	var tenant quartermasterpb.Tenant
	var createdAt, updatedAt time.Time
	var subdomain, customDomain, logoURL, primaryClusterID, officialClusterID, kafkaTopicPrefix, databaseURL sql.NullString
	var kafkaBrokers []string

	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, subdomain, custom_domain, logo_url, primary_color, secondary_color,
		       deployment_tier, deployment_model,
		       primary_cluster_id, official_cluster_id, kafka_topic_prefix, kafka_brokers, database_url,
		       is_active, created_at, updated_at
		FROM quartermaster.tenants
		WHERE id = $1 AND is_active = TRUE
	`, tenantID).Scan(
		&tenant.Id, &tenant.Name, &subdomain, &customDomain, &logoURL,
		&tenant.PrimaryColor, &tenant.SecondaryColor,
		&tenant.DeploymentTier, &tenant.DeploymentModel,
		&primaryClusterID, &officialClusterID, &kafkaTopicPrefix,
		pq.Array(&kafkaBrokers), &databaseURL, &tenant.IsActive, &createdAt, &updatedAt,
	)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "tenant not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	if subdomain.Valid {
		tenant.Subdomain = &subdomain.String
	}
	if customDomain.Valid {
		tenant.CustomDomain = &customDomain.String
	}
	if logoURL.Valid {
		tenant.LogoUrl = &logoURL.String
	}
	if primaryClusterID.Valid {
		tenant.PrimaryClusterId = &primaryClusterID.String
	}
	if officialClusterID.Valid {
		tenant.OfficialClusterId = &officialClusterID.String
	}
	if kafkaTopicPrefix.Valid {
		tenant.KafkaTopicPrefix = &kafkaTopicPrefix.String
	}
	if databaseURL.Valid {
		tenant.DatabaseUrl = &databaseURL.String
	}
	tenant.KafkaBrokers = kafkaBrokers
	tenant.CreatedAt = timestamppb.New(createdAt)
	tenant.UpdatedAt = timestamppb.New(updatedAt)

	return &quartermasterpb.GetTenantResponse{Tenant: &tenant}, nil
}

// UpdateTenantCluster updates the cluster routing info for a tenant
func (s *QuartermasterServer) UpdateTenantCluster(ctx context.Context, req *quartermasterpb.UpdateTenantClusterRequest) (*emptypb.Empty, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	userID := middleware.GetUserID(ctx)
	var updates []string
	var args []any
	argIdx := 1
	changedFields := []string{}
	var previousClusterID sql.NullString
	if req.PrimaryClusterId != nil {
		_ = s.db.QueryRowContext(ctx, `SELECT primary_cluster_id FROM quartermaster.tenants WHERE id = $1`, tenantID).Scan(&previousClusterID)
	}

	if req.PrimaryClusterId != nil {
		newClusterID := strings.TrimSpace(*req.PrimaryClusterId)
		if newClusterID != "" {
			var exists bool
			err := s.db.QueryRowContext(ctx, `
				SELECT EXISTS(
					SELECT 1 FROM quartermaster.tenant_cluster_access
					WHERE tenant_id = $1 AND cluster_id = $2
					  AND is_active = TRUE
					  AND (subscription_status = 'active' OR access_level = 'owner')
				)
			`, tenantID, newClusterID).Scan(&exists)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "failed to verify cluster subscription: %v", err)
			}
			if !exists {
				return nil, status.Error(codes.FailedPrecondition, "cluster is not an active subscription for this tenant")
			}

			var clusterType string
			if err := s.db.QueryRowContext(ctx, `SELECT cluster_type FROM quartermaster.infrastructure_clusters WHERE cluster_id = $1`, newClusterID).Scan(&clusterType); err != nil {
				return nil, status.Errorf(codes.Internal, "failed to look up cluster type: %v", err)
			}
			if !models.ClusterTypeCanBePreferred(clusterType) {
				return nil, status.Error(codes.FailedPrecondition, "only edge clusters can be set as preferred")
			}
		}
	}

	if req.PrimaryClusterId != nil {
		updates = append(updates, fmt.Sprintf("primary_cluster_id = $%d", argIdx))
		args = append(args, *req.PrimaryClusterId)
		argIdx++
		changedFields = append(changedFields, "primary_cluster_id")
	}
	if req.DeploymentModel != nil {
		updates = append(updates, fmt.Sprintf("deployment_model = $%d", argIdx))
		args = append(args, *req.DeploymentModel)
		argIdx++
		changedFields = append(changedFields, "deployment_model")
	}

	if len(updates) == 0 {
		return nil, status.Error(codes.InvalidArgument, "no fields to update")
	}

	updates = append(updates, "updated_at = NOW()")
	query := fmt.Sprintf("UPDATE quartermaster.tenants SET %s WHERE id = $%d AND is_active = TRUE", strings.Join(updates, ", "), argIdx)
	args = append(args, tenantID)

	// Mutation + outbox emits ride in one tx so a crash between them can't
	// leave the cluster assignment durable without the corresponding event.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update tenant cluster: %v", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to inspect tenant cluster update: %v", err)
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

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, subdomain, custom_domain, logo_url, primary_color, secondary_color,
		       deployment_tier, deployment_model,
		       primary_cluster_id, official_cluster_id, kafka_topic_prefix, kafka_brokers, database_url,
		       is_active, created_at, updated_at
		FROM quartermaster.tenants
		WHERE id = ANY($1) AND is_active = TRUE
	`, pq.Array(tenantIDs))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var tenants []*quartermasterpb.Tenant
	for rows.Next() {
		var tenant quartermasterpb.Tenant
		var createdAt, updatedAt time.Time
		var subdomain, customDomain, logoURL, primaryClusterID, kafkaTopicPrefix, databaseURL sql.NullString
		var kafkaBrokers []string

		if err := rows.Scan(
			&tenant.Id, &tenant.Name, &subdomain, &customDomain, &logoURL,
			&tenant.PrimaryColor, &tenant.SecondaryColor,
			&tenant.DeploymentTier, &tenant.DeploymentModel,
			&primaryClusterID, &kafkaTopicPrefix,
			pq.Array(&kafkaBrokers), &databaseURL, &tenant.IsActive, &createdAt, &updatedAt,
		); err != nil {
			s.logger.WithError(err).Warn("Failed to scan tenant in batch")
			continue
		}

		if subdomain.Valid {
			tenant.Subdomain = &subdomain.String
		}
		if customDomain.Valid {
			tenant.CustomDomain = &customDomain.String
		}
		if logoURL.Valid {
			tenant.LogoUrl = &logoURL.String
		}
		if primaryClusterID.Valid {
			tenant.PrimaryClusterId = &primaryClusterID.String
		}
		if kafkaTopicPrefix.Valid {
			tenant.KafkaTopicPrefix = &kafkaTopicPrefix.String
		}
		if databaseURL.Valid {
			tenant.DatabaseUrl = &databaseURL.String
		}
		tenant.KafkaBrokers = kafkaBrokers
		tenant.CreatedAt = timestamppb.New(createdAt)
		tenant.UpdatedAt = timestamppb.New(updatedAt)
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

	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.name, t.subdomain, t.custom_domain, t.logo_url, t.primary_color, t.secondary_color,
		       t.deployment_tier, t.deployment_model,
		       t.primary_cluster_id, t.official_cluster_id, t.kafka_topic_prefix, t.kafka_brokers, t.database_url,
		       t.is_active, t.created_at, t.updated_at
		FROM quartermaster.tenants t
		LEFT JOIN quartermaster.tenant_cluster_assignments tca ON t.id = tca.tenant_id
		WHERE (t.primary_cluster_id = $1 OR tca.cluster_id = $1) AND t.is_active = TRUE
	`, clusterID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var tenants []*quartermasterpb.Tenant
	for rows.Next() {
		var tenant quartermasterpb.Tenant
		var createdAt, updatedAt time.Time
		var subdomain, customDomain, logoURL, primaryClusterID, kafkaTopicPrefix, databaseURL sql.NullString
		var kafkaBrokers []string

		if err := rows.Scan(
			&tenant.Id, &tenant.Name, &subdomain, &customDomain, &logoURL,
			&tenant.PrimaryColor, &tenant.SecondaryColor,
			&tenant.DeploymentTier, &tenant.DeploymentModel,
			&primaryClusterID, &kafkaTopicPrefix,
			pq.Array(&kafkaBrokers), &databaseURL, &tenant.IsActive, &createdAt, &updatedAt,
		); err != nil {
			s.logger.WithError(err).Warn("Failed to scan tenant by cluster")
			continue
		}

		if subdomain.Valid {
			tenant.Subdomain = &subdomain.String
		}
		if customDomain.Valid {
			tenant.CustomDomain = &customDomain.String
		}
		if logoURL.Valid {
			tenant.LogoUrl = &logoURL.String
		}
		if primaryClusterID.Valid {
			tenant.PrimaryClusterId = &primaryClusterID.String
		}
		if kafkaTopicPrefix.Valid {
			tenant.KafkaTopicPrefix = &kafkaTopicPrefix.String
		}
		if databaseURL.Valid {
			tenant.DatabaseUrl = &databaseURL.String
		}
		tenant.KafkaBrokers = kafkaBrokers
		tenant.CreatedAt = timestamppb.New(createdAt)
		tenant.UpdatedAt = timestamppb.New(updatedAt)
		tenants = append(tenants, &tenant)
	}

	return &quartermasterpb.GetTenantsByClusterResponse{
		ClusterId: clusterID,
		Tenants:   tenants,
	}, nil
}

// activeClusterSlugs returns the slug values of all active clusters so
// they can be excluded from the tenant subdomain namespace. Errors are
// swallowed: on failure we return an empty list rather than blocking
// tenant signups. Static reserved labels still apply via
// dns.IsReservedTenantSlug.
func (s *QuartermasterServer) activeClusterSlugs(ctx context.Context) []string {
	rows, err := s.db.QueryContext(ctx, `
		SELECT cluster_id FROM quartermaster.infrastructure_clusters
		WHERE is_active = TRUE
	`)
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil && id != "" {
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
	var exists bool
	if err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM quartermaster.tenants
			WHERE subdomain = $1
		)
	`, candidate).Scan(&exists); err != nil {
		return false, err
	}
	return !exists, nil
}

// ListAliasedTenantsForCluster returns tenants on a paid tier with
// active access to the cluster. Used by Foghorn cert refresh to know
// which per-tenant TLS bundles to include in ConfigSeed for edges in
// this cluster. Filters:
//   - tenant_cluster_access.is_active = TRUE
//   - tenants.deployment_tier <> 'free' (paid tier)
//   - tenants.is_active = TRUE
//   - tenants.subdomain IS NOT NULL AND <> ”
//
// Cert readiness happens at the caller via Navigator; this method
// returns candidates without crossing service boundaries.
func (s *QuartermasterServer) ListAliasedTenantsForCluster(ctx context.Context, req *quartermasterpb.ListAliasedTenantsForClusterRequest) (*quartermasterpb.ListAliasedTenantsForClusterResponse, error) {
	clusterID := req.GetClusterId()
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.subdomain
		FROM quartermaster.tenants t
		JOIN quartermaster.tenant_cluster_access tca ON tca.tenant_id = t.id
		WHERE tca.cluster_id = $1
		  AND tca.is_active = TRUE
		  AND t.is_active = TRUE
		  AND t.deployment_tier <> 'free'
		  AND t.subdomain IS NOT NULL
		  AND t.subdomain <> ''
		ORDER BY t.id
	`, clusterID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var out []*quartermasterpb.AliasedTenantRef
	for rows.Next() {
		var tenantID string
		var subdomain sql.NullString
		if err := rows.Scan(&tenantID, &subdomain); err != nil {
			s.logger.WithError(err).Warn("Failed to scan aliased tenant row")
			continue
		}
		if !subdomain.Valid || subdomain.String == "" {
			continue
		}
		out = append(out, &quartermasterpb.AliasedTenantRef{
			TenantId:  tenantID,
			Subdomain: subdomain.String,
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

	builder := &pagination.KeysetBuilder{
		TimestampColumn: "c.created_at",
		IDColumn:        "c.id",
	}

	// Base WHERE clause for filtering by subscription or ownership
	baseWhere := ""
	baseCountArgs := []any{}

	if ownerTenantID != "" {
		baseWhere = `
			WHERE c.owner_tenant_id = $1
		`
		baseCountArgs = append(baseCountArgs, ownerTenantID)
	} else if publicPlatformOfficialScope {
		baseWhere = `
			WHERE c.is_platform_official = true
		`
	} else if publicTopologyScope {
		baseWhere = `
			WHERE c.public_topology = true AND c.is_active = true
		`
	} else if tenantID != "" {
		baseWhere = `
			WHERE (c.cluster_id IN (
				SELECT tca.cluster_id FROM quartermaster.tenant_cluster_access tca
				WHERE tca.tenant_id = $1 AND tca.is_active = true
			) OR c.owner_tenant_id = $1)
		`
		baseCountArgs = append(baseCountArgs, tenantID)
	} else if ctxkeys.GetAuthType(ctx) == "service" {
		// Service-to-service calls (e.g. Navigator) see all active clusters.
		baseWhere = `
			WHERE c.is_active = true
		`
	} else {
		baseWhere = `
			WHERE c.is_default_cluster = true
		`
	}

	// Build WHERE clause for filters
	where := ""
	countWhere := ""
	args := append([]any{}, baseCountArgs...)
	countArgs := append([]any{}, baseCountArgs...)
	argIdx := len(baseCountArgs) + 1

	// Add any additional filters from the request
	if req.GetClusterId() != "" {
		where += fmt.Sprintf(" AND c.cluster_id = $%d", argIdx)
		countWhere += fmt.Sprintf(" AND c.cluster_id = $%d", argIdx)
		args = append(args, req.GetClusterId())
		countArgs = append(countArgs, req.GetClusterId())
		argIdx++
	}
	if req.GetClusterName() != "" {
		where += fmt.Sprintf(" AND c.cluster_name ILIKE '%%' || $%d || '%%'", argIdx)
		countWhere += fmt.Sprintf(" AND c.cluster_name ILIKE '%%' || $%d || '%%'", argIdx)
		args = append(args, req.GetClusterName())
		countArgs = append(countArgs, req.GetClusterName())
		argIdx++
	}
	if req.GetClusterType() != "" {
		where += fmt.Sprintf(" AND c.cluster_type = $%d", argIdx)
		countWhere += fmt.Sprintf(" AND c.cluster_type = $%d", argIdx)
		args = append(args, req.GetClusterType())
		countArgs = append(countArgs, req.GetClusterType())
		argIdx++
	}
	if req.GetDeploymentModel() != "" {
		where += fmt.Sprintf(" AND c.deployment_model = $%d", argIdx)
		countWhere += fmt.Sprintf(" AND c.deployment_model = $%d", argIdx)
		args = append(args, req.GetDeploymentModel())
		countArgs = append(countArgs, req.GetDeploymentModel())
		argIdx++
	}
	if req.IsPlatformOfficial != nil && !publicPlatformOfficialScope {
		where += fmt.Sprintf(" AND c.is_platform_official = $%d", argIdx)
		countWhere += fmt.Sprintf(" AND c.is_platform_official = $%d", argIdx)
		args = append(args, *req.IsPlatformOfficial)
		countArgs = append(countArgs, *req.IsPlatformOfficial)
		argIdx++
	}
	if req.PublicTopology != nil && !publicTopologyScope {
		where += fmt.Sprintf(" AND c.public_topology = $%d", argIdx)
		countWhere += fmt.Sprintf(" AND c.public_topology = $%d", argIdx)
		args = append(args, *req.PublicTopology)
		countArgs = append(countArgs, *req.PublicTopology)
		argIdx++
	}

	// Get total count
	var total int32
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM quartermaster.infrastructure_clusters c %s %s`, baseWhere, countWhere)
	if countErr := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.db.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total)
	}); countErr != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", countErr)
	}

	// Add keyset condition if cursor provided
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		where += " AND " + condition
		args = append(args, cursorArgs...)
	}

	// Build main query with keyset pagination
	// NOTE: Column order must match scanCluster() exactly!
	query := fmt.Sprintf(`
		SELECT c.id, c.cluster_id, c.cluster_name, c.cluster_type, c.owner_tenant_id, c.deployment_model,
		       c.base_url, c.database_url, c.periscope_url, c.kafka_brokers,
		       c.max_concurrent_streams, c.max_concurrent_viewers, c.max_bandwidth_mbps,
		       c.health_status, c.is_active, c.is_default_cluster, c.is_platform_official, c.public_topology, c.allow_private_pull_sources, c.created_at, c.updated_at
		FROM quartermaster.infrastructure_clusters c
		%s %s
		%s
		LIMIT $%d
	`, baseWhere, where, builder.OrderBy(params), argIdx+len(args)-len(countArgs)) // Adjusted argIdx for LIMIT

	// Append limit arg
	args = append(args, params.Limit+1)

	rows, err := retryQueryContext(ctx, s.db, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var clusters []*quartermasterpb.InfrastructureCluster
	for rows.Next() {
		cluster, err := scanCluster(rows) // scanCluster needs to be updated for is_default_cluster
		if err != nil {
			s.logger.WithError(err).Warn("Failed to scan cluster")
			continue
		}
		clusters = append(clusters, cluster)
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
	// Determine deployment model (default to 'managed')
	deploymentModel := req.GetDeploymentModel()
	if deploymentModel == "" {
		deploymentModel = "managed"
	}

	// Validate owner_tenant_id if provided
	ownerTenantID := ""
	if req.OwnerTenantId != nil && *req.OwnerTenantId != "" {
		var exists bool
		err := s.db.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM quartermaster.tenants WHERE id = $1)",
			*req.OwnerTenantId).Scan(&exists)
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
		if _, err := s.db.ExecContext(ctx, `UPDATE quartermaster.infrastructure_clusters SET is_default_cluster = false WHERE is_default_cluster = true`); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to clear existing default cluster: %v", err)
		}
	}

	baseURL := dns.NormalizeDomainScope(req.GetBaseUrl())
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO quartermaster.infrastructure_clusters (id, cluster_id, cluster_name, cluster_type, deployment_model,
		                                                   owner_tenant_id, base_url, database_url, periscope_url, kafka_brokers,
		                                                   max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps,
		                                                   health_status, is_active, is_platform_official, is_default_cluster,
		                                                   public_topology, allow_private_pull_sources, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, '')::uuid, $7, $8, $9, $10, $11, $12, $13, 'healthy', true, $14, $15, $16, $17, $18, $18)
	`, id, clusterID, req.GetClusterName(), clusterType, deploymentModel,
		ownerTenantID, baseURL,
		req.DatabaseUrl, req.PeriscopeUrl, pq.Array(req.GetKafkaBrokers()),
		req.GetMaxConcurrentStreams(), req.GetMaxConcurrentViewers(), req.GetMaxBandwidthMbps(),
		isPlatformOfficial, isDefaultCluster, publicTopology, allowPrivatePullSources, now)

	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create cluster: %v", err)
	}

	// Assign idle Foghorn instances to this cluster via service_cluster_assignments.
	// "Idle" = Foghorn with zero active assignments in the junction table.
	if foghornCount := req.GetFoghornCount(); foghornCount > 0 {
		res, claimErr := s.db.ExecContext(ctx, `
			INSERT INTO quartermaster.service_cluster_assignments (service_instance_id, cluster_id)
			SELECT si.id, $1
			FROM quartermaster.service_instances si
			JOIN quartermaster.services svc ON svc.service_id = si.service_id
			LEFT JOIN quartermaster.service_cluster_assignments sca
			  ON sca.service_instance_id = si.id AND sca.is_active = true
			WHERE svc.type = 'foghorn'
			  AND si.status = 'running'
			  AND si.health_status = 'healthy'
			  AND si.protocol = 'grpc'
			  AND (si.metadata->>'foghorn_listener' = 'internal_control' OR si.port = 18019 OR si.metadata->>'foghorn_listener' = 'control')
			  AND sca.id IS NULL
			ORDER BY si.started_at ASC
			LIMIT $2
			ON CONFLICT DO NOTHING
		`, clusterID, foghornCount)
		if claimErr != nil {
			s.logger.WithError(claimErr).Warn("Failed to assign Foghorn instances to cluster")
			if _, hsErr := s.db.ExecContext(ctx, `UPDATE quartermaster.infrastructure_clusters SET health_status = 'provisioning' WHERE cluster_id = $1`, clusterID); hsErr != nil {
				s.logger.WithError(hsErr).WithField("cluster_id", clusterID).Warn("Failed to update cluster health_status to provisioning")
			}
		} else if claimed, _ := res.RowsAffected(); claimed < int64(foghornCount) {
			s.logger.WithFields(logging.Fields{
				"cluster_id": clusterID,
				"requested":  foghornCount,
				"claimed":    claimed,
			}).Warn("Assigned fewer Foghorn instances than requested")
			if _, hsErr := s.db.ExecContext(ctx, `UPDATE quartermaster.infrastructure_clusters SET health_status = 'provisioning' WHERE cluster_id = $1`, clusterID); hsErr != nil {
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
	var updates []string
	var args []any
	argIdx := 1

	if req.ClusterName != nil {
		updates = append(updates, fmt.Sprintf("cluster_name = $%d", argIdx))
		args = append(args, *req.ClusterName)
		argIdx++
	}
	if req.BaseUrl != nil {
		updates = append(updates, fmt.Sprintf("base_url = $%d", argIdx))
		args = append(args, dns.NormalizeDomainScope(*req.BaseUrl))
		argIdx++
	}
	if req.HealthStatus != nil {
		updates = append(updates, fmt.Sprintf("health_status = $%d", argIdx))
		args = append(args, *req.HealthStatus)
		argIdx++
	}
	if req.IsActive != nil {
		updates = append(updates, fmt.Sprintf("is_active = $%d", argIdx))
		args = append(args, *req.IsActive)
		argIdx++
	}
	// Handle owner_tenant_id (empty string clears ownership)
	if req.OwnerTenantId != nil {
		if *req.OwnerTenantId != "" {
			// Validate the tenant exists
			var exists bool
			err := s.db.QueryRowContext(ctx,
				"SELECT EXISTS(SELECT 1 FROM quartermaster.tenants WHERE id = $1)",
				*req.OwnerTenantId).Scan(&exists)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "failed to validate owner_tenant_id: %v", err)
			}
			if !exists {
				return nil, status.Error(codes.InvalidArgument, "owner_tenant_id does not exist")
			}
		}
		updates = append(updates, fmt.Sprintf("owner_tenant_id = NULLIF($%d, '')::uuid", argIdx))
		args = append(args, *req.OwnerTenantId)
		argIdx++
	}
	if req.DeploymentModel != nil {
		updates = append(updates, fmt.Sprintf("deployment_model = $%d", argIdx))
		args = append(args, *req.DeploymentModel)
		argIdx++
	}
	if req.IsPlatformOfficial != nil {
		updates = append(updates, fmt.Sprintf("is_platform_official = $%d", argIdx))
		args = append(args, *req.IsPlatformOfficial)
		argIdx++
	}
	if req.IsDefaultCluster != nil {
		if *req.IsDefaultCluster {
			// At most one cluster can be the default — clear existing before setting.
			if _, err := s.db.ExecContext(ctx, `UPDATE quartermaster.infrastructure_clusters SET is_default_cluster = false WHERE is_default_cluster = true`); err != nil {
				return nil, status.Errorf(codes.Internal, "failed to clear existing default cluster: %v", err)
			}
		}
		updates = append(updates, fmt.Sprintf("is_default_cluster = $%d", argIdx))
		args = append(args, *req.IsDefaultCluster)
		argIdx++
	}
	if req.AllowPrivatePullSources != nil {
		updates = append(updates, fmt.Sprintf("allow_private_pull_sources = $%d", argIdx))
		args = append(args, *req.AllowPrivatePullSources)
		argIdx++
	}
	if req.PublicTopology != nil {
		updates = append(updates, fmt.Sprintf("public_topology = $%d", argIdx))
		args = append(args, *req.PublicTopology)
		argIdx++
	}

	if len(updates) == 0 {
		return nil, status.Error(codes.InvalidArgument, "no fields to update")
	}

	updates = append(updates, "updated_at = NOW()")
	query := fmt.Sprintf("UPDATE quartermaster.infrastructure_clusters SET %s WHERE cluster_id = $%d", strings.Join(updates, ", "), argIdx)
	args = append(args, clusterID)

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update cluster: %v", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to inspect cluster update: %v", err)
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

	result, err := s.db.ExecContext(ctx, `
		UPDATE quartermaster.infrastructure_clusters
		SET wg_mesh_cidr = $1, wg_listen_port = $2, updated_at = NOW()
		WHERE cluster_id = $3
	`, meshCIDR, port, clusterID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update mesh config: %v", err)
	}
	rows, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		return nil, status.Errorf(codes.Internal, "check rows affected: %v", rowsErr)
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

	builder := &pagination.KeysetBuilder{
		TimestampColumn: "a.created_at",
		IDColumn:        "a.id",
	}

	baseWhere := "WHERE a.tenant_id = $1 AND c.is_active = true"
	args := []any{tenantID}
	argIdx := 2

	// Get total count
	var total int32
	countQuery := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM quartermaster.infrastructure_clusters c
		JOIN quartermaster.tenant_cluster_access a ON c.cluster_id = a.cluster_id
		%s
	`, baseWhere)
	if countErr := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); countErr != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", countErr)
	}

	// Add keyset condition
	where := baseWhere
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		where += " AND " + condition
		args = append(args, cursorArgs...)
	}

	query := fmt.Sprintf(`
		SELECT c.cluster_id, c.cluster_name, a.access_level, a.resource_limits, a.created_at, a.id
		FROM quartermaster.infrastructure_clusters c
		JOIN quartermaster.tenant_cluster_access a ON c.cluster_id = a.cluster_id
		%s
		%s
		LIMIT $%d
	`, where, builder.OrderBy(params), len(args)+1)

	args = append(args, params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var clusters []*quartermasterpb.ClusterAccessEntry
	type entryWithCursor struct {
		entry     *quartermasterpb.ClusterAccessEntry
		createdAt time.Time
		id        string
	}
	var entries []entryWithCursor
	for rows.Next() {
		var entry quartermasterpb.ClusterAccessEntry
		var resourceLimits sql.NullString
		var createdAt time.Time
		var id string
		if err := rows.Scan(&entry.ClusterId, &entry.ClusterName, &entry.AccessLevel, &resourceLimits, &createdAt, &id); err != nil {
			continue
		}
		entries = append(entries, entryWithCursor{entry: &entry, createdAt: createdAt, id: id})
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

	builder := &pagination.KeysetBuilder{
		TimestampColumn: "created_at",
		IDColumn:        "cluster_id",
	}

	baseWhere := "WHERE is_active = true AND deployment_model = 'shared'"
	var args []any
	argIdx := 1

	// Get total count
	var total int32
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM quartermaster.infrastructure_clusters %s`, baseWhere)
	if countErr := s.db.QueryRowContext(ctx, countQuery).Scan(&total); countErr != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", countErr)
	}

	// Add keyset condition
	where := baseWhere
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		where += " AND " + condition
		args = append(args, cursorArgs...)
	}

	query := fmt.Sprintf(`
		SELECT cluster_id, cluster_name, cluster_type, true as auto_enroll, created_at
		FROM quartermaster.infrastructure_clusters
		%s
		%s
		LIMIT $%d
	`, where, builder.OrderBy(params), len(args)+1)

	args = append(args, params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	type entryWithCursor struct {
		entry     *quartermasterpb.AvailableClusterEntry
		createdAt time.Time
		clusterID string
	}
	var entries []entryWithCursor
	for rows.Next() {
		var entry quartermasterpb.AvailableClusterEntry
		var clusterType string
		var createdAt time.Time
		if err := rows.Scan(&entry.ClusterId, &entry.ClusterName, &clusterType, &entry.AutoEnroll, &createdAt); err != nil {
			continue
		}
		entry.Tiers = []string{clusterType}
		entries = append(entries, entryWithCursor{entry: &entry, createdAt: createdAt, clusterID: entry.ClusterId})
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

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO quartermaster.tenant_cluster_access (tenant_id, cluster_id, access_level, expires_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NOW(), NOW())
		ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
			access_level = EXCLUDED.access_level,
			expires_at = EXCLUDED.expires_at,
			updated_at = NOW()
	`, tenantID, clusterID, accessLevel, req.GetExpiresAt()); err != nil {
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
	var tenantExists bool
	if err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM quartermaster.tenants WHERE id = $1::uuid)`, tenantID,
	).Scan(&tenantExists); err != nil {
		return nil, status.Errorf(codes.Internal, "probe tenant: %v", err)
	}
	if !tenantExists {
		return nil, status.Errorf(codes.NotFound, "tenant %q not found", tenantID)
	}

	var isPlatformOfficial, isActive bool
	err := s.db.QueryRowContext(ctx, `
		SELECT is_platform_official, is_active
		FROM quartermaster.infrastructure_clusters
		WHERE cluster_id = $1
	`, clusterID).Scan(&isPlatformOfficial, &isActive)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Errorf(codes.NotFound, "cluster %q not found", clusterID)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "probe cluster: %v", err)
	}
	if !isPlatformOfficial {
		return nil, status.Errorf(codes.FailedPrecondition, "cluster %q is not platform-official", clusterID)
	}
	if !isActive {
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

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO quartermaster.tenant_cluster_access
			(tenant_id, cluster_id, access_level, subscription_status, resource_limits, is_active, granted_at, created_at, updated_at)
		VALUES ($1::uuid, $2, 'shared', 'active', COALESCE($3::jsonb, '{}'::jsonb), true, NOW(), NOW(), NOW())
		ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
			subscription_status = 'active',
			is_active = true,
			resource_limits = COALESCE(NULLIF(quartermaster.tenant_cluster_access.resource_limits, '{}'::jsonb), EXCLUDED.resource_limits),
			updated_at = NOW()
	`, tenantID, clusterID, resourceLimitsJSON); err != nil {
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

	if _, err := tx.ExecContext(ctx, `
		UPDATE quartermaster.tenant_cluster_access
		SET is_active = false,
		    subscription_status = 'suspended',
		    updated_at = NOW()
		WHERE tenant_id = $1::uuid AND cluster_id = $2 AND is_active = true
	`, tenantID, clusterID); err != nil {
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
	rows, err := s.db.QueryContext(ctx, `
		SELECT tca.cluster_id, tca.is_active, tca.subscription_status, COALESCE(ic.is_platform_official, false)
		FROM quartermaster.tenant_cluster_access tca
		LEFT JOIN quartermaster.infrastructure_clusters ic ON ic.cluster_id = tca.cluster_id
		WHERE tca.tenant_id = $1::uuid
	`, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list tenant_cluster_access: %v", err)
	}
	defer rows.Close()
	out := &quartermasterpb.ListTenantClusterAccessResponse{}
	for rows.Next() {
		var r quartermasterpb.TenantClusterAccessRow
		if err := rows.Scan(&r.ClusterId, &r.IsActive, &r.SubscriptionStatus, &r.IsPlatformOfficial); err != nil {
			return nil, status.Errorf(codes.Internal, "scan tenant_cluster_access: %v", err)
		}
		out.Rows = append(out.Rows, &r)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "iterate tenant_cluster_access: %v", err)
	}
	return out, nil
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
	var deploymentModel string
	err := s.db.QueryRowContext(ctx, `SELECT deployment_model FROM quartermaster.infrastructure_clusters WHERE cluster_id = $1`, clusterID).Scan(&deploymentModel)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "cluster not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	if deploymentModel != "shared" {
		return nil, status.Error(codes.PermissionDenied, "cannot subscribe to non-shared cluster")
	}

	// Create subscription + durable alias ensure in one tx so a Navigator
	// outage can't leave the tenant subscribed without an alias hand-off.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO quartermaster.tenant_cluster_access
		(tenant_id, cluster_id, access_level, is_active, created_at, updated_at)
		VALUES ($1, $2, 'subscriber', true, NOW(), NOW())
		ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
			is_active = true,
			updated_at = NOW()
	`, tenantID, clusterID); err != nil {
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

	if _, err := tx.ExecContext(ctx, `
		UPDATE quartermaster.tenant_cluster_access
		SET is_active = false, updated_at = NOW()
		WHERE tenant_id = $1 AND cluster_id = $2
	`, tenantID, clusterID); err != nil {
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

	builder := &pagination.KeysetBuilder{
		TimestampColumn: "c.created_at",
		IDColumn:        "c.id",
	}

	baseWhere := `
		WHERE c.cluster_id IN (
			SELECT cluster_id FROM quartermaster.tenant_cluster_access
			WHERE tenant_id = $1 AND is_active = true
		)
	`
	args := []any{tenantID}
	argIdx := 2

	// Get total count
	var total int32
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM quartermaster.infrastructure_clusters c %s`, baseWhere)
	if countErr := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); countErr != nil {
		s.logger.WithError(countErr).WithField("tenant_id", tenantID).Error("ListMySubscriptions: count query failed")
		return nil, status.Errorf(codes.Internal, "database error: %v", countErr)
	}
	s.logger.WithFields(map[string]any{
		"tenant_id":   tenantID,
		"total_count": total,
	}).Info("ListMySubscriptions: found subscribed clusters")

	// Add keyset condition
	where := baseWhere
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		where += " AND " + condition
		args = append(args, cursorArgs...)
	}

	// NOTE: Column order must match scanCluster() exactly!
	query := fmt.Sprintf(`
			SELECT c.id, c.cluster_id, c.cluster_name, c.cluster_type, c.owner_tenant_id, c.deployment_model,
			       c.base_url, c.database_url, c.periscope_url, c.kafka_brokers,
			       c.max_concurrent_streams, c.max_concurrent_viewers, c.max_bandwidth_mbps,
			       c.health_status, c.is_active,
			       (c.cluster_id = COALESCE(t.primary_cluster_id, '') OR (t.primary_cluster_id IS NULL AND c.is_default_cluster)) AS is_default_cluster,
			       c.is_platform_official, c.public_topology, c.allow_private_pull_sources, c.created_at, c.updated_at
			FROM quartermaster.infrastructure_clusters c
			LEFT JOIN quartermaster.tenants t ON t.id = $1
			%s
			%s
			LIMIT $%d
		`, where, builder.OrderBy(params), len(args)+1)

	args = append(args, params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var clusters []*quartermasterpb.InfrastructureCluster
	for rows.Next() {
		cluster, err := scanCluster(rows)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "database error: %v", err)
		}
		clusters = append(clusters, cluster)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
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

	where := []string{"(n.node_id = $1 OR n.id::text = $1)"}
	args := []any{nodeID, nextStatus}
	if expectedClusterID := strings.TrimSpace(req.GetExpectedClusterId()); expectedClusterID != "" {
		args = append(args, expectedClusterID)
		where = append(where, fmt.Sprintf("n.cluster_id = $%d", len(args)))
	}
	if tenantID != "" {
		providerActor, err := s.hasProviderLifecycleAuthority(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		if providerActor {
			where = append(where, `n.cluster_id IN (
				SELECT c.cluster_id FROM quartermaster.infrastructure_clusters c
				WHERE c.is_active = true
			)`)
		} else {
			args = append(args, tenantID)
			tenantArg := len(args)
			where = append(where, fmt.Sprintf(`n.cluster_id IN (
				SELECT c.cluster_id FROM quartermaster.infrastructure_clusters c
				WHERE c.owner_tenant_id = $%d AND c.is_active = true
				UNION
				SELECT tca.cluster_id FROM quartermaster.tenant_cluster_access tca
				WHERE tca.tenant_id = $%d
				  AND tca.access_level = 'owner'
				  AND tca.subscription_status = 'active'
				  AND tca.is_active = true
			)`, tenantArg, tenantArg))
		}
	} else {
		where = append(where, `n.cluster_id IN (
			SELECT c.cluster_id FROM quartermaster.infrastructure_clusters c
			WHERE c.is_active = true
		)`)
	}

	query := fmt.Sprintf(`
		UPDATE quartermaster.infrastructure_nodes n
		SET status = $2, updated_at = NOW()
		WHERE %s
		RETURNING n.node_id, n.cluster_id
	`, strings.Join(where, " AND "))
	var canonicalNodeID, canonicalClusterID string
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&canonicalNodeID, &canonicalClusterID); errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "node not found")
	} else if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update node status: %v", err)
	}

	// Any non-active status transition makes the node ineligible for DNS.
	// Both aggregate `edge` and the edge-* subtypes gate on
	// service_instances.health_status (plus n.status='active'). Flip every
	// edge service instance to 'unhealthy' so the polling Navigator reconcile
	// would converge even if our targeted wake-up below fails.
	if nextStatus != "active" {
		if _, err := s.db.ExecContext(ctx, `
			UPDATE quartermaster.service_instances si
			SET health_status = 'unhealthy',
			    status = 'offline',
			    stopped_at = COALESCE(si.stopped_at, NOW()),
			    last_health_check = NOW(),
			    updated_at = NOW()
			FROM quartermaster.services svc
			WHERE svc.service_id = si.service_id
			  AND (svc.type = 'edge' OR svc.type LIKE 'edge-%')
			  AND si.node_id = $1
		`, canonicalNodeID); err != nil {
			s.logger.WithError(err).WithField("node_id", canonicalNodeID).
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

	var isProvider bool
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(is_provider, false)
		FROM quartermaster.tenants
		WHERE id = $1
	`, tenantID).Scan(&isProvider)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, status.Errorf(codes.Internal, "provider authority check failed: %v", err)
	}
	return isProvider, nil
}

func (s *QuartermasterServer) ListEdgeReleases(ctx context.Context, req *quartermasterpb.ListEdgeReleasesRequest) (*quartermasterpb.ListEdgeReleasesResponse, error) {
	where := []string{"TRUE"}
	args := []any{}
	if strings.TrimSpace(req.GetChannel()) != "" {
		channel, err := normalizeReleaseTargetChannel(req.GetChannel())
		if err != nil {
			return nil, err
		}
		args = append(args, channel)
		where = append(where, fmt.Sprintf("channel = $%d", len(args)))
	}
	if version := strings.TrimSpace(req.GetVersion()); version != "" {
		args = append(args, version)
		where = append(where, fmt.Sprintf("version = $%d", len(args)))
	}
	var releases []*quartermasterpb.EdgeRelease
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		rows, err := s.listEdgeReleasesNoRetry(ctx, where, args)
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

func (s *QuartermasterServer) listEdgeReleasesNoRetry(ctx context.Context, where []string, args []any) ([]*quartermasterpb.EdgeRelease, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
			SELECT channel, version, components::text, published_at
		FROM quartermaster.edge_releases
		WHERE %s
		ORDER BY channel, published_at DESC, version DESC
	`, strings.Join(where, " AND ")), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var releases []*quartermasterpb.EdgeRelease
	for rows.Next() {
		release, err := scanEdgeRelease(rows)
		if err != nil {
			return nil, fmt.Errorf("scan release: %w", err)
		}
		releases = append(releases, release)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read releases: %w", err)
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
		row := s.db.QueryRowContext(ctx, `
			INSERT INTO quartermaster.edge_releases (channel, version, components, published_at)
			VALUES ($1, $2, $3::jsonb, $4)
			ON CONFLICT (channel, version) DO UPDATE SET
				components = EXCLUDED.components,
				published_at = EXCLUDED.published_at
			RETURNING channel, version, components::text, published_at
		`, channel, version, components, publishedAt)
		var scanErr error
		saved, scanErr = scanEdgeRelease(row)
		return scanErr
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
	where := []string{"TRUE"}
	args := []any{}
	if clusterID := strings.TrimSpace(req.GetClusterId()); clusterID != "" {
		if err := s.authorizeClusterReleaseTarget(ctx, clusterID); err != nil {
			return nil, err
		}
		args = append(args, clusterID)
		where = append(where, fmt.Sprintf("cluster_id = $%d", len(args)))
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
		rows, err := s.listClusterReleaseTargetsNoRetry(ctx, where, args)
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

func (s *QuartermasterServer) listClusterReleaseTargetsNoRetry(ctx context.Context, where []string, args []any) ([]*quartermasterpb.ClusterReleaseTarget, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
			SELECT cluster_id, channel, COALESCE(target_version, ''), rollout_plan::text, COALESCE(paused, false), updated_at
		FROM quartermaster.cluster_release_targets
		WHERE %s
		ORDER BY updated_at DESC, cluster_id
	`, strings.Join(where, " AND ")), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var targets []*quartermasterpb.ClusterReleaseTarget
	for rows.Next() {
		target, err := scanClusterReleaseTarget(rows)
		if err != nil {
			return nil, fmt.Errorf("scan release target: %w", err)
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read release targets: %w", err)
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
		row := s.db.QueryRowContext(ctx, `
				INSERT INTO quartermaster.cluster_release_targets (cluster_id, channel, target_version, rollout_plan, paused, updated_at)
				VALUES ($1, $2, NULLIF($3, ''), $4::jsonb, $5, NOW())
			ON CONFLICT (cluster_id) DO UPDATE SET
				channel = EXCLUDED.channel,
				target_version = EXCLUDED.target_version,
				rollout_plan = EXCLUDED.rollout_plan,
				paused = EXCLUDED.paused,
				updated_at = NOW()
			RETURNING cluster_id, channel, COALESCE(target_version, ''), rollout_plan::text, COALESCE(paused, false), updated_at
		`, clusterID, channel, targetVersion, rolloutPlan, target.GetPaused())
		var scanErr error
		saved, scanErr = scanClusterReleaseTarget(row)
		return scanErr
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
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		if strings.TrimSpace(version) == "" {
			return s.db.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM quartermaster.edge_releases
				WHERE channel = $1
			)
		`, channel).Scan(&exists)
		}
		return s.db.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM quartermaster.edge_releases
				WHERE channel = $1 AND version = $2
			)
		`, channel, version).Scan(&exists)
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
	var authorized bool
	err = s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM quartermaster.infrastructure_clusters
			WHERE cluster_id = $1 AND owner_tenant_id = $2 AND is_active = true
			UNION
			SELECT 1 FROM quartermaster.tenant_cluster_access
			WHERE cluster_id = $1
			  AND tenant_id = $2
			  AND access_level = 'owner'
			  AND subscription_status = 'active'
			  AND is_active = true
		)
	`, clusterID, tenantID).Scan(&authorized)
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
	row := s.db.QueryRowContext(ctx, `
			SELECT cluster_id, channel, COALESCE(target_version, ''), rollout_plan::text, COALESCE(paused, false), updated_at
		FROM quartermaster.cluster_release_targets
		WHERE cluster_id = $1
	`, clusterID)
	return scanClusterReleaseTarget(row)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanEdgeRelease(row rowScanner) (*quartermasterpb.EdgeRelease, error) {
	var release quartermasterpb.EdgeRelease
	var publishedAt time.Time
	if err := row.Scan(&release.Channel, &release.Version, &release.ComponentsJson, &publishedAt); err != nil {
		return nil, err
	}
	release.PublishedAt = timestamppb.New(publishedAt)
	return &release, nil
}

func scanClusterReleaseTarget(row rowScanner) (*quartermasterpb.ClusterReleaseTarget, error) {
	var target quartermasterpb.ClusterReleaseTarget
	var updatedAt time.Time
	if err := row.Scan(&target.ClusterId, &target.Channel, &target.TargetVersion, &target.RolloutPlanJson, &target.Paused, &updatedAt); err != nil {
		return nil, err
	}
	target.UpdatedAt = timestamppb.New(updatedAt)
	return &target, nil
}

func normalizeJSONObject(raw, field string) (string, error) {
	raw = strings.TrimSpace(raw)
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
	result, err := s.db.ExecContext(ctx, `
		UPDATE quartermaster.infrastructure_nodes
		SET cpu_cores = COALESCE($2, cpu_cores),
		    memory_gb = COALESCE($3, memory_gb),
		    disk_gb = COALESCE($4, disk_gb),
		    last_heartbeat = NOW(),
		    updated_at = NOW()
		WHERE node_id = $1`,
		nodeID, req.CpuCores, req.MemoryGb, req.DiskGb)
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"node_id": nodeID,
			"error":   err,
		}).Error("Failed to update node hardware specs")
		return nil, status.Errorf(codes.Internal, "failed to update hardware specs: %v", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"node_id": nodeID,
			"error":   err,
		}).Warn("Could not determine updated node hardware row count")
		return nil, status.Errorf(codes.Internal, "failed to inspect node hardware update: %v", err)
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

func scanPriorNodes(rows *sql.Rows) (map[string]nodeBefore, error) {
	defer rows.Close() //nolint:errcheck // best-effort cleanup
	out := map[string]nodeBefore{}
	for rows.Next() {
		var id string
		var nb nodeBefore
		if err := rows.Scan(&id, &nb.clusterID, &nb.externalIP); err != nil {
			return nil, err
		}
		out[id] = nb
	}
	return out, rows.Err()
}

func scanPriorInst(rows *sql.Rows) (map[string]instBefore, error) {
	defer rows.Close() //nolint:errcheck // best-effort cleanup
	out := map[string]instBefore{}
	for rows.Next() {
		var nodeID, svcType string
		ib := instBefore{exists: true}
		if err := rows.Scan(&nodeID, &svcType, &ib.clusterID, &ib.health); err != nil {
			return nil, err
		}
		out[nodeID+"|"+svcType] = ib
	}
	return out, rows.Err()
}

// ReportAliveNodes ingests Foghorn's per-node DNS-relevant state. For each
// reported node it:
//
//   - Refreshes infrastructure_nodes.last_heartbeat only when is_healthy=true;
//     heartbeat remains mesh liveness, while DNS membership is carried by
//     service_instances.health_status.
//   - Updates external_ip when Foghorn sent a valid IP literal.
//   - UPSERTs the aggregate edge service_instance plus edge-* capability rows,
//     marking them healthy when the derivation predicate and node health both
//     allow it. Derived services that drop false on an existing row become
//     'unhealthy' (present-but-unhealthy, not deleted).
//
// When any (cluster_id, edge-service_type) pair's DNS-visible state changes,
// from service_instances health, external_ip, or cluster_id changes,
// Quartermaster fires Navigator.SyncDNS for that pair after commit. The 60s
// Navigator reconcile loop is the backstop.
func (s *QuartermasterServer) ReportAliveNodes(ctx context.Context, req *quartermasterpb.ReportAliveNodesRequest) (*emptypb.Empty, error) {
	nodes := req.GetNodes()
	if len(nodes) == 0 {
		return &emptypb.Empty{}, nil
	}
	if len(nodes) > 500 {
		return nil, status.Errorf(codes.InvalidArgument, "too many nodes (%d), max 500", len(nodes))
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
		nodeRows, err := tx.QueryContext(ctx, `
		SELECT node_id, cluster_id, COALESCE(host(external_ip), '')
		FROM quartermaster.infrastructure_nodes
		WHERE node_id = ANY($1)
	`, pq.Array(nodeIDs))
		if err != nil {
			return fmt.Errorf("read prior node state: %w", err)
		}
		priorNodes, err = scanPriorNodes(nodeRows)
		if err != nil {
			return fmt.Errorf("scan prior node: %w", err)
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
		siRows, err := tx.QueryContext(ctx, `
		SELECT si.node_id, svc.type, si.cluster_id, COALESCE(si.health_status, '')
		FROM quartermaster.service_instances si
		JOIN quartermaster.services svc ON svc.service_id = si.service_id
		WHERE si.node_id = ANY($1)
		  AND (svc.type = 'edge' OR svc.type LIKE 'edge-%')
	`, pq.Array(nodeIDs))
		if err != nil {
			return fmt.Errorf("failed to read prior edge service health: %w", err)
		}
		priorInst, err = scanPriorInst(siRows)
		if err != nil {
			return fmt.Errorf("scan prior si: %w", err)
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
		if _, execErr := tx.ExecContext(ctx, `
		UPDATE quartermaster.infrastructure_nodes n
		SET last_heartbeat = CASE WHEN payload.refresh_hb THEN NOW() ELSE n.last_heartbeat END,
		    updated_at = NOW(),
		    external_ip = COALESCE(NULLIF(payload.ip, '')::inet, n.external_ip)
		FROM unnest($1::text[], $2::text[], $3::boolean[]) AS payload(node_id, ip, refresh_hb)
		WHERE n.node_id = payload.node_id
	`, pq.Array(upNodeIDs), pq.Array(upExternalIPs), pq.Array(upRefreshHB)); execErr != nil {
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
				if _, execErr := tx.ExecContext(ctx, `
				INSERT INTO quartermaster.service_instances
					(instance_id, cluster_id, node_id, service_id, protocol,
					 advertise_host, port, status, health_status, started_at,
					 last_health_check, created_at, updated_at)
				SELECT $1::varchar(100), n.cluster_id, $2::varchar(100), $3::varchar(100), 'http',
				       COALESCE(host(n.external_ip), ''), 18008, 'running', 'healthy',
				       COALESCE((SELECT started_at FROM quartermaster.service_instances WHERE instance_id = $1::varchar(100)), NOW()),
				       NOW(), NOW(), NOW()
				FROM quartermaster.infrastructure_nodes n
				WHERE n.node_id = $2::varchar(100)
				  AND n.status = 'active'
				  AND n.node_type = 'edge'
				ON CONFLICT (instance_id) DO UPDATE
				SET node_id = EXCLUDED.node_id,
				    service_id = EXCLUDED.service_id,
				    health_status = 'healthy',
				    status = 'running',
				    advertise_host = EXCLUDED.advertise_host,
				    last_health_check = NOW(),
				    updated_at = NOW()
			`, instanceID, c.nodeID, c.serviceType); execErr != nil {
					return fmt.Errorf("upsert healthy edge instance: %w", execErr)
				}
			case !c.healthy && hadRow:
				if _, execErr := tx.ExecContext(ctx, `
				UPDATE quartermaster.service_instances si
				SET health_status = 'unhealthy',
				    last_health_check = NOW(),
				    updated_at = NOW()
				FROM quartermaster.services svc
				WHERE svc.service_id = si.service_id
				  AND svc.type = $1
				  AND si.node_id = $2
			`, c.serviceType, c.nodeID); execErr != nil {
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
func (s *QuartermasterServer) servedClusters(ctx context.Context, query string, args ...any) []string {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to resolve served clusters for DNS wake; reconcile loop will converge")
		return nil
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var c string
		if scanErr := rows.Scan(&c); scanErr != nil {
			s.logger.WithError(scanErr).Warn("Scan error resolving served clusters for DNS wake; reconcile loop will converge")
			return out
		}
		if strings.TrimSpace(c) != "" {
			out = append(out, c)
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		s.logger.WithError(rowsErr).Warn("Iteration error resolving served clusters for DNS wake; reconcile loop will converge")
	}
	return out
}

// scanDeletedClusters drains a DELETE ... RETURNING cluster_id result into the
// distinct non-empty clusters plus the total deleted row count.
func scanDeletedClusters(rows *sql.Rows) ([]string, int64, error) {
	defer func() { _ = rows.Close() }()
	var clusters []string
	var count int64
	seen := map[string]struct{}{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, 0, err
		}
		count++
		if _, ok := seen[c]; !ok && strings.TrimSpace(c) != "" {
			seen[c] = struct{}{}
			clusters = append(clusters, c)
		}
	}
	return clusters, count, rows.Err()
}

func (s *QuartermasterServer) servedClustersForInstanceName(ctx context.Context, instanceName, serviceType string) []string {
	return s.servedClusters(ctx, `
		SELECT DISTINCT sca.cluster_id
		FROM quartermaster.service_cluster_assignments sca
		JOIN quartermaster.service_instances si ON si.id = sca.service_instance_id
		JOIN quartermaster.services svc ON svc.service_id = si.service_id
		WHERE si.instance_id = $1 AND svc.type = $2 AND sca.is_active = true
	`, instanceName, serviceType)
}

func (s *QuartermasterServer) servedClustersForNodeType(ctx context.Context, nodeID, serviceType string) []string {
	return s.servedClusters(ctx, `
		SELECT DISTINCT sca.cluster_id
		FROM quartermaster.service_cluster_assignments sca
		JOIN quartermaster.service_instances si ON si.id = sca.service_instance_id
		JOIN quartermaster.services svc ON svc.service_id = si.service_id
		WHERE si.node_id = $1 AND svc.type = $2 AND sca.is_active = true
	`, nodeID, serviceType)
}

// ListNodes returns nodes with optional filters
func (s *QuartermasterServer) ListNodes(ctx context.Context, req *quartermasterpb.ListNodesRequest) (*quartermasterpb.ListNodesResponse, error) {
	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	tenantID := middleware.GetTenantID(ctx)

	builder := &pagination.KeysetBuilder{
		TimestampColumn: "n.created_at",
		IDColumn:        "n.id",
	}

	// Base WHERE clause to secure visibility
	baseWhere := ""
	baseArgs := []any{}

	if tenantID != "" {
		// Authenticated: Subscribed or Owned
		baseWhere = `
			WHERE n.cluster_id IN (
				SELECT c.cluster_id FROM quartermaster.infrastructure_clusters c
				WHERE c.owner_tenant_id = $1
				UNION
				SELECT tca.cluster_id FROM quartermaster.tenant_cluster_access tca
				WHERE tca.tenant_id = $1 AND tca.is_active = true
			)
		`
		baseArgs = append(baseArgs, tenantID)
	} else if ctxkeys.GetAuthType(ctx) == "service" {
		baseWhere = `
			WHERE n.cluster_id IN (
				SELECT c.cluster_id FROM quartermaster.infrastructure_clusters c
				WHERE c.is_active = true
			)
		`
	} else {
		// Unauthenticated: clusters explicitly published to public topology.
		baseWhere = `
			WHERE n.cluster_id IN (
				SELECT c.cluster_id FROM quartermaster.infrastructure_clusters c
				WHERE c.public_topology = true AND c.is_active = true
			)
		`
	}

	// Build WHERE clause for filters
	where := baseWhere
	countWhere := baseWhere
	args := append([]any{}, baseArgs...)
	countArgs := append([]any{}, baseArgs...)
	argIdx := len(baseArgs) + 1

	if req.GetClusterId() != "" {
		where += fmt.Sprintf(" AND n.cluster_id = $%d", argIdx)
		countWhere += fmt.Sprintf(" AND n.cluster_id = $%d", argIdx)
		args = append(args, req.GetClusterId())
		countArgs = append(countArgs, req.GetClusterId())
		argIdx++
	}
	if req.GetNodeType() != "" {
		where += fmt.Sprintf(" AND n.node_type = $%d", argIdx)
		countWhere += fmt.Sprintf(" AND n.node_type = $%d", argIdx)
		args = append(args, req.GetNodeType())
		countArgs = append(countArgs, req.GetNodeType())
		argIdx++
	}
	if req.GetRegion() != "" {
		where += fmt.Sprintf(" AND n.region = $%d", argIdx)
		countWhere += fmt.Sprintf(" AND n.region = $%d", argIdx)
		args = append(args, req.GetRegion())
		countArgs = append(countArgs, req.GetRegion())
		argIdx++
	}

	// Get total count
	var total int32
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM quartermaster.infrastructure_nodes n %s`, countWhere)
	if countErr := s.db.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total); countErr != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", countErr)
	}

	// Add keyset condition if cursor provided
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		where += " AND " + condition
		args = append(args, cursorArgs...)
	}

	query := fmt.Sprintf(`
		SELECT id, node_id, cluster_id, node_name, node_type, internal_ip, external_ip,
		       wireguard_ip, wireguard_public_key, wireguard_listen_port, region, availability_zone,
		       latitude, longitude,
		       cpu_cores, memory_gb, disk_gb,
		       last_heartbeat, enrollment_origin, applied_mesh_revision, status, created_at, updated_at%s%s
		FROM quartermaster.infrastructure_nodes n
		%s
		%s
		LIMIT $%d
	`, prefixedNodeOwnerColumn, prefixedNodeSnapshotColumns, where, builder.OrderBy(params), argIdx+len(args)-len(countArgs)) // Use next available index for limit

	// Append limit
	args = append(args, params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var nodes []*quartermasterpb.InfrastructureNode
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			// Fail closed: a swallowed scan error returns a truncated node list that
			// a caller treats as complete — for the DNS readers Navigator would prune
			// healthy records against it. Same class as the DiscoverServices and
			// ListServiceInstancesByType inventory reads.
			return nil, status.Errorf(codes.Internal, "scan node: %v", err)
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "iterate nodes: %v", err)
	}

	// Detect hasMore and trim results
	hasMore := len(nodes) > params.Limit
	if hasMore {
		nodes = nodes[:params.Limit]
	}

	// Reverse results if backward pagination
	if params.Direction == pagination.Backward && len(nodes) > 0 {
		for i, j := 0, len(nodes)-1; i < j; i, j = i+1, j-1 {
			nodes[i], nodes[j] = nodes[j], nodes[i]
		}
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

	baseWhere := ""
	baseArgs := []any{}

	if tenantID != "" {
		baseWhere = `
			WHERE n.cluster_id IN (
				SELECT c.cluster_id FROM quartermaster.infrastructure_clusters c
				WHERE c.owner_tenant_id = $1
				UNION
				SELECT tca.cluster_id FROM quartermaster.tenant_cluster_access tca
				WHERE tca.tenant_id = $1 AND tca.is_active = true
			)
		`
		baseArgs = append(baseArgs, tenantID)
	} else if ctxkeys.GetAuthType(ctx) == "service" {
		baseWhere = `
			WHERE n.cluster_id IN (
				SELECT c.cluster_id FROM quartermaster.infrastructure_clusters c
				WHERE c.is_active = true
			)
		`
	} else {
		baseWhere = `
			WHERE n.cluster_id IN (
				SELECT c.cluster_id FROM quartermaster.infrastructure_clusters c
				WHERE c.public_topology = true AND c.is_active = true
			)
		`
	}
	if clusterID := strings.TrimSpace(req.GetClusterId()); clusterID != "" {
		baseArgs = append(baseArgs, clusterID)
		baseWhere += fmt.Sprintf(" AND n.cluster_id = $%d", len(baseArgs))
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
	if serviceLookupType == "foghorn" {
		return s.listHealthyFoghornServingCellNodes(ctx, baseWhere, baseArgs, staleThreshold)
	}
	if dns.IsPoolAssignedServiceType(serviceLookupType) || serviceTypeFilter == "telemetry" {
		return s.listHealthyAssignedServiceNodes(ctx, baseWhere, baseArgs, serviceLookupType, staleThreshold)
	}
	return s.listHealthyServiceNodes(ctx, baseWhere, baseArgs, serviceLookupType, staleThreshold)
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
	rows, err := retryQueryContext(ctx, s.db, `
		SELECT si.domains
		FROM quartermaster.ingress_sites si
		JOIN quartermaster.infrastructure_nodes n ON n.node_id = si.node_id
		WHERE si.kind = 'physical' AND n.status = 'active' AND n.external_ip IS NOT NULL
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]struct{}{}
	for rows.Next() {
		var domainsJSON []byte
		if err := rows.Scan(&domainsJSON); err != nil {
			// Fail closed: a skipped/unreadable gate row would yield a truncated
			// "provisioned" set, suppressing public_instance_host for a node that
			// is actually provisioned. The caller turns this into a discovery
			// error so Foghorn retries rather than caching an empty fanout.
			return nil, fmt.Errorf("scan physical ingress row: %w", err)
		}
		domains, err := decodeIngressDomainsStrict(domainsJSON)
		if err != nil {
			return nil, fmt.Errorf("decode physical ingress domains: %w", err)
		}
		for _, d := range domains {
			out[strings.ToLower(strings.TrimSpace(d))] = struct{}{}
		}
	}
	return out, rows.Err()
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

// listHealthyServiceNodes returns nodes with healthy service instances matching the type.
// Used for platform services (bridge, foghorn, chartroom, etc.) that register
// via BootstrapService and have service_instance health tracking.
func (s *QuartermasterServer) listHealthyServiceNodes(ctx context.Context, baseWhere string, baseArgs []any, serviceTypeFilter string, staleThreshold int32) (*quartermasterpb.ListHealthyNodesForDNSResponse, error) {
	where := baseWhere
	args := append([]any{}, baseArgs...)
	argIdx := len(baseArgs) + 1

	if serviceTypeFilter != "" {
		where += fmt.Sprintf(" AND s.type = $%d", argIdx)
		args = append(args, serviceTypeFilter)
		argIdx++
	}

	// Operator-controlled gate: maintenance/offline/retired/evicted nodes are
	// excluded from DNS even when a recent Foghorn heartbeat marked the
	// service_instances row healthy.
	where += " AND n.external_ip IS NOT NULL AND n.status = 'active'"

	// Edge service names (aggregate `edge` and the edge-* subtypes) must only
	// ever surface edge nodes. The writer is Foghorn-only, but the read path
	// stays defensive: a stray instance row can never route a non-edge node
	// under an edge name.
	if serviceTypeFilter == models.NodeTypeEdge || strings.HasPrefix(serviceTypeFilter, "edge-") {
		where += " AND n.node_type = 'edge'"
	}

	servicesJoin := "\n\t\tJOIN quartermaster.services s ON si.service_id = s.service_id"
	siJoin := `
		JOIN quartermaster.service_instances si
			ON si.cluster_id = n.cluster_id
			AND (si.node_id = n.node_id OR si.advertise_host = host(n.external_ip) OR si.advertise_host = host(n.internal_ip) OR si.advertise_host = host(n.wireguard_ip))`

	var totalNodes int32
	totalQuery := fmt.Sprintf(`SELECT COUNT(DISTINCT n.id) FROM quartermaster.infrastructure_nodes n %s %s %s`, siJoin, servicesJoin, where)
	if err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.db.QueryRowContext(ctx, totalQuery, args...).Scan(&totalNodes)
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	healthArgs := append([]any{}, args...)
	healthArgs = append(healthArgs, staleThreshold)
	healthWhere := where + fmt.Sprintf(" AND si.health_status = 'healthy' AND si.last_health_check > NOW() - ($%d * INTERVAL '1 second')", argIdx)

	var healthyNodes int32
	healthyQuery := fmt.Sprintf(`
		SELECT COUNT(DISTINCT n.id) FROM quartermaster.infrastructure_nodes n %s %s %s
	`, siJoin, servicesJoin, healthWhere)
	if err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.db.QueryRowContext(ctx, healthyQuery, healthArgs...).Scan(&healthyNodes)
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	rows, err := retryQueryContext(ctx, s.db, fmt.Sprintf(`
		SELECT DISTINCT n.id, n.node_id, n.cluster_id, n.node_name, n.node_type, n.internal_ip, n.external_ip,
		       n.wireguard_ip, n.wireguard_public_key, n.wireguard_listen_port, n.region, n.availability_zone,
		       n.latitude, n.longitude,
		       n.cpu_cores, n.memory_gb, n.disk_gb,
		       n.last_heartbeat, n.enrollment_origin, n.applied_mesh_revision, n.status, n.created_at, n.updated_at%s%s
		FROM quartermaster.infrastructure_nodes n
		%s
		%s
		%s
	`, prefixedNodeOwnerColumn, prefixedNodeSnapshotColumns, siJoin, servicesJoin, healthWhere), healthArgs...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var nodes []*quartermasterpb.InfrastructureNode
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			// Fail closed: a swallowed scan error returns a truncated node list that
			// a caller treats as complete — for the DNS readers Navigator would prune
			// healthy records against it. Same class as the DiscoverServices and
			// ListServiceInstancesByType inventory reads.
			return nil, status.Errorf(codes.Internal, "scan node: %v", err)
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "iterate nodes: %v", err)
	}

	return &quartermasterpb.ListHealthyNodesForDNSResponse{
		Nodes:        nodes,
		TotalNodes:   totalNodes,
		HealthyNodes: healthyNodes,
	}, nil
}

// listHealthyAssignedServiceNodes resolves a cluster-scoped Bunny service's
// healthy nodes via service_cluster_assignments. service_instances.cluster_id
// stays the physical/runtime cluster (FK-bound to the host); the logical media
// cluster comes from sca.cluster_id. The same physical instance can therefore
// surface under multiple media-cluster DNS records (M:N).
func (s *QuartermasterServer) listHealthyAssignedServiceNodes(ctx context.Context, baseWhere string, baseArgs []any, serviceTypeFilter string, staleThreshold int32) (*quartermasterpb.ListHealthyNodesForDNSResponse, error) {
	where := strings.ReplaceAll(baseWhere, "n.cluster_id", "sca.cluster_id")
	args := append([]any{}, baseArgs...)
	argIdx := len(args) + 1

	if where == "" {
		where = "WHERE TRUE"
	}
	// n.status='active' mirrors the non-pool path (listHealthyServiceNodes) and the
	// physical inventory: a pool-assigned service (livepeer-gateway, foghorn,
	// chandler) on an operator-offlined node must drop out of the pooled cluster
	// record (livepeer.<media-cluster>, …). Without it, planned maintenance can keep
	// routing public clients to a non-active node, since UpdateNodeStatus only flips
	// EDGE instance health — pool instances stay healthy until the poller catches up.
	where += fmt.Sprintf(" AND sca.is_active = TRUE AND s.type = $%d AND n.external_ip IS NOT NULL AND n.status = 'active'", argIdx)
	args = append(args, serviceTypeFilter)
	argIdx++

	joins := `
		JOIN quartermaster.service_cluster_assignments sca ON sca.service_instance_id = si.id
		JOIN quartermaster.services s ON si.service_id = s.service_id
		JOIN quartermaster.infrastructure_nodes n
			ON si.node_id = n.node_id
			OR si.advertise_host = host(n.external_ip)
			OR si.advertise_host = host(n.internal_ip)
			OR si.advertise_host = host(n.wireguard_ip)`

	var totalNodes int32
	totalQuery := fmt.Sprintf(`SELECT COUNT(DISTINCT (n.id, sca.cluster_id)) FROM quartermaster.service_instances si %s %s`, joins, where)
	if err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.db.QueryRowContext(ctx, totalQuery, args...).Scan(&totalNodes)
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	healthArgs := append([]any{}, args...)
	healthArgs = append(healthArgs, staleThreshold)
	healthWhere := where + fmt.Sprintf(" AND si.health_status = 'healthy' AND si.last_health_check > NOW() - ($%d * INTERVAL '1 second')", argIdx)

	var healthyNodes int32
	healthyQuery := fmt.Sprintf(`SELECT COUNT(DISTINCT (n.id, sca.cluster_id)) FROM quartermaster.service_instances si %s %s`, joins, healthWhere)
	if err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.db.QueryRowContext(ctx, healthyQuery, healthArgs...).Scan(&healthyNodes)
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	rows, err := retryQueryContext(ctx, s.db, fmt.Sprintf(`
		SELECT DISTINCT n.id, n.node_id, sca.cluster_id, n.node_name, n.node_type, n.internal_ip, n.external_ip,
		       n.wireguard_ip, n.wireguard_public_key, n.wireguard_listen_port, n.region, n.availability_zone,
		       n.latitude, n.longitude,
		       n.cpu_cores, n.memory_gb, n.disk_gb,
		       n.last_heartbeat, n.enrollment_origin, n.applied_mesh_revision, n.status, n.created_at, n.updated_at%s%s
			FROM quartermaster.service_instances si
			%s
			%s
		`, prefixedAssignedNodeOwnerColumn, prefixedNodeSnapshotColumns, joins, healthWhere), healthArgs...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var nodes []*quartermasterpb.InfrastructureNode
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			// Fail closed: a swallowed scan error returns a truncated node list that
			// a caller treats as complete — for the DNS readers Navigator would prune
			// healthy records against it. Same class as the DiscoverServices and
			// ListServiceInstancesByType inventory reads.
			return nil, status.Errorf(codes.Internal, "scan node: %v", err)
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "iterate nodes: %v", err)
	}

	return &quartermasterpb.ListHealthyNodesForDNSResponse{
		Nodes:        nodes,
		TotalNodes:   totalNodes,
		HealthyNodes: healthyNodes,
	}, nil
}

// listHealthyFoghornServingCellNodes resolves Foghorn DNS through the logical
// cluster's serving-cell policy. A target cluster may be served by every healthy
// Foghorn in its eligible cells, so DNS failover does not depend on a single
// service_cluster_assignments row.
func (s *QuartermasterServer) listHealthyFoghornServingCellNodes(ctx context.Context, baseWhere string, baseArgs []any, staleThreshold int32) (*quartermasterpb.ListHealthyNodesForDNSResponse, error) {
	where := strings.ReplaceAll(baseWhere, "n.cluster_id", "target.cluster_id")
	args := append([]any{}, baseArgs...)
	argIdx := len(args) + 1

	if where == "" {
		where = "WHERE TRUE"
	}
	where += fmt.Sprintf(" AND s.type = $%d AND n.external_ip IS NOT NULL AND n.status = 'active' AND target.is_active = true", argIdx)
	args = append(args, "foghorn")
	argIdx++

	joins := `
		JOIN quartermaster.services s ON si.service_id = s.service_id
		JOIN quartermaster.infrastructure_nodes n
			ON si.node_id = n.node_id
			OR si.advertise_host = host(n.external_ip)
			OR si.advertise_host = host(n.internal_ip)
			OR si.advertise_host = host(n.wireguard_ip)
		JOIN quartermaster.infrastructure_clusters host_cluster ON host_cluster.cluster_id = n.cluster_id
		JOIN quartermaster.infrastructure_clusters target
			ON COALESCE(NULLIF(host_cluster.cell_id, ''), host_cluster.cluster_id) = ANY (
				CASE
					WHEN cardinality(target.eligible_serving_cell_ids) > 0 THEN target.eligible_serving_cell_ids
					WHEN COALESCE(target.control_cell_id, '') <> '' THEN ARRAY[target.control_cell_id]::TEXT[]
					ELSE ARRAY[COALESCE(NULLIF(target.cell_id, ''), target.cluster_id)]::TEXT[]
				END
			)`

	var totalNodes int32
	totalQuery := fmt.Sprintf(`SELECT COUNT(DISTINCT (n.id, target.cluster_id)) FROM quartermaster.service_instances si %s %s`, joins, where)
	if err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.db.QueryRowContext(ctx, totalQuery, args...).Scan(&totalNodes)
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	healthArgs := append([]any{}, args...)
	healthArgs = append(healthArgs, staleThreshold)
	healthWhere := where + fmt.Sprintf(" AND si.status = 'running' AND si.health_status = 'healthy' AND si.last_health_check > NOW() - ($%d * INTERVAL '1 second')", argIdx)

	var healthyNodes int32
	healthyQuery := fmt.Sprintf(`SELECT COUNT(DISTINCT (n.id, target.cluster_id)) FROM quartermaster.service_instances si %s %s`, joins, healthWhere)
	if err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.db.QueryRowContext(ctx, healthyQuery, healthArgs...).Scan(&healthyNodes)
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	rows, err := retryQueryContext(ctx, s.db, fmt.Sprintf(`
		SELECT DISTINCT n.id, n.node_id, target.cluster_id, n.node_name, n.node_type, n.internal_ip, n.external_ip,
		       n.wireguard_ip, n.wireguard_public_key, n.wireguard_listen_port, n.region, n.availability_zone,
		       n.latitude, n.longitude,
		       n.cpu_cores, n.memory_gb, n.disk_gb,
		       n.last_heartbeat, n.enrollment_origin, n.applied_mesh_revision, n.status, n.created_at, n.updated_at%s%s
			FROM quartermaster.service_instances si
			%s
			%s
		`, prefixedTargetNodeOwnerColumn, prefixedNodeSnapshotColumns, joins, healthWhere), healthArgs...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var nodes []*quartermasterpb.InfrastructureNode
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "scan node: %v", err)
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "iterate nodes: %v", err)
	}

	return &quartermasterpb.ListHealthyNodesForDNSResponse{
		Nodes:        nodes,
		TotalNodes:   totalNodes,
		HealthyNodes: healthyNodes,
	}, nil
}

// CreateNode creates a new node
func (s *QuartermasterServer) CreateNode(ctx context.Context, req *quartermasterpb.CreateNodeRequest) (*quartermasterpb.NodeResponse, error) {
	nodeID := req.GetNodeId()
	clusterID := req.GetClusterId()
	if nodeID == "" || clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id and cluster_id required")
	}

	// Verify cluster exists
	var clusterExists bool
	err := s.db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM quartermaster.infrastructure_clusters WHERE cluster_id = $1)",
		clusterID,
	).Scan(&clusterExists)
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
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO quartermaster.infrastructure_nodes (id, node_id, cluster_id, node_name, node_type,
		                                                internal_ip, external_ip, wireguard_ip, wireguard_public_key,
		                                                wireguard_listen_port,
		                                                region, availability_zone,
		                                                latitude, longitude,
		                                                cpu_cores, memory_gb, disk_gb, status,
		                                                created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, 'active', $17, $17)
		ON CONFLICT (node_id) DO UPDATE SET
			cluster_id            = EXCLUDED.cluster_id,
			node_name             = EXCLUDED.node_name,
			node_type             = EXCLUDED.node_type,
			internal_ip           = COALESCE(EXCLUDED.internal_ip, quartermaster.infrastructure_nodes.internal_ip),
			external_ip           = COALESCE(EXCLUDED.external_ip, quartermaster.infrastructure_nodes.external_ip),
			wireguard_ip          = COALESCE(EXCLUDED.wireguard_ip, quartermaster.infrastructure_nodes.wireguard_ip),
			wireguard_public_key  = COALESCE(EXCLUDED.wireguard_public_key, quartermaster.infrastructure_nodes.wireguard_public_key),
			wireguard_listen_port = COALESCE(EXCLUDED.wireguard_listen_port, quartermaster.infrastructure_nodes.wireguard_listen_port),
			region                = COALESCE(EXCLUDED.region, quartermaster.infrastructure_nodes.region),
			availability_zone     = COALESCE(EXCLUDED.availability_zone, quartermaster.infrastructure_nodes.availability_zone),
			latitude              = COALESCE(EXCLUDED.latitude, quartermaster.infrastructure_nodes.latitude),
			longitude             = COALESCE(EXCLUDED.longitude, quartermaster.infrastructure_nodes.longitude),
			cpu_cores             = COALESCE(EXCLUDED.cpu_cores, quartermaster.infrastructure_nodes.cpu_cores),
			memory_gb             = COALESCE(EXCLUDED.memory_gb, quartermaster.infrastructure_nodes.memory_gb),
			disk_gb               = COALESCE(EXCLUDED.disk_gb, quartermaster.infrastructure_nodes.disk_gb),
			status                = 'active',
			updated_at            = EXCLUDED.updated_at
	`, nodeID, clusterID, req.GetNodeName(), req.GetNodeType(),
		req.InternalIp, req.ExternalIp, req.WireguardIp, req.WireguardPublicKey, wgPort,
		req.Region, req.AvailabilityZone,
		lat, lng,
		req.CpuCores, req.MemoryGb, req.DiskGb, now)

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

	args := []any{}
	where := `
			WHERE svc.type = 'foghorn'
			  AND si.status = 'running'
			  AND si.health_status = 'healthy'
			  AND si.protocol = 'grpc'
			  AND (si.metadata->>'foghorn_listener' = 'internal_control' OR si.port = 18019 OR si.metadata->>'foghorn_listener' = 'control')
			  AND ic.cluster_class = 'platform_official'
			  AND ic.is_active = true`
	if explicitControlClusterID != "" {
		args = append(args, explicitControlClusterID)
		where += fmt.Sprintf("\n			  AND ic.cluster_id = $%d", len(args))
	} else if requestedRegion != "" && !hasClientGeo {
		args = append(args, requestedRegion)
		where += fmt.Sprintf("\n			  AND ic.region_id = $%d", len(args))
	}

	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT si.id::text AS instance_id,
		       ic.cluster_id AS control_cell,
		       COALESCE(ic.region_id, '') AS control_region,
		       COALESCE(ic.base_url, '') AS control_base_url,
		       COUNT(sca.id) AS load,
		       n.latitude,
		       n.longitude,
		       si.started_at
		FROM quartermaster.service_instances si
		JOIN quartermaster.services svc ON svc.service_id = si.service_id
		JOIN quartermaster.service_cluster_assignments primary_sca
		  ON primary_sca.service_instance_id = si.id AND primary_sca.is_active = true
		JOIN quartermaster.infrastructure_clusters ic
		  ON ic.cluster_id = primary_sca.cluster_id
		LEFT JOIN quartermaster.service_cluster_assignments sca
		  ON sca.service_instance_id = si.id AND sca.is_active = true
		LEFT JOIN quartermaster.infrastructure_nodes n
		  ON n.node_id = si.node_id
		%s
		GROUP BY si.id, ic.cluster_id, ic.region_id, ic.base_url, n.latitude, n.longitude, si.started_at
	`, where), args...)
	if err != nil {
		return foghornControlCellCandidate{}, status.Errorf(codes.Internal, "failed to find Foghorn control cell: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var candidates []foghornControlCellCandidate
	for rows.Next() {
		var c foghornControlCellCandidate
		if err := rows.Scan(&c.instanceID, &c.controlCellID, &c.regionID, &c.baseURL, &c.load, &c.latitude, &c.longitude, &c.startedAt); err != nil {
			return foghornControlCellCandidate{}, status.Errorf(codes.Internal, "failed to scan Foghorn control cell: %v", err)
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return foghornControlCellCandidate{}, status.Errorf(codes.Internal, "failed to iterate Foghorn control cells: %v", err)
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

	var tenantID, nodeID string

	// 1) Try exact match by machine_id_sha256
	machineIDSHA := req.GetMachineIdSha256()
	if machineIDSHA != "" {
		err := s.db.QueryRowContext(ctx, `
			SELECT nf.tenant_id::text, nf.node_id
			FROM quartermaster.node_fingerprints nf
			JOIN quartermaster.infrastructure_nodes n ON n.node_id = nf.node_id
			JOIN quartermaster.infrastructure_clusters c ON c.cluster_id = n.cluster_id
			WHERE nf.fingerprint_machine_sha256 = $1
			  AND c.is_active = TRUE
		`, machineIDSHA).Scan(&tenantID, &nodeID)
		if err == nil {
			if upsertErr := s.upsertSeenIP(ctx, nodeID, peerIP); upsertErr != nil {
				s.logger.WithError(upsertErr).WithField("node_id", nodeID).Warn("Failed to update fingerprint seen IP")
			}
			return &quartermasterpb.ResolveNodeFingerprintResponse{
				TenantId:        tenantID,
				CanonicalNodeId: nodeID,
			}, nil
		}
	}

	// 2) Match by macs_sha256
	macsSHA := req.GetMacsSha256()
	if macsSHA != "" {
		err := s.db.QueryRowContext(ctx, `
			SELECT nf.tenant_id::text, nf.node_id
			FROM quartermaster.node_fingerprints nf
			JOIN quartermaster.infrastructure_nodes n ON n.node_id = nf.node_id
			JOIN quartermaster.infrastructure_clusters c ON c.cluster_id = n.cluster_id
			WHERE nf.fingerprint_macs_sha256 = $1
			  AND c.is_active = TRUE
		`, macsSHA).Scan(&tenantID, &nodeID)
		if err == nil {
			if upsertErr := s.upsertSeenIP(ctx, nodeID, peerIP); upsertErr != nil {
				s.logger.WithError(upsertErr).WithField("node_id", nodeID).Warn("Failed to update fingerprint seen IP")
			}
			return &quartermasterpb.ResolveNodeFingerprintResponse{
				TenantId:        tenantID,
				CanonicalNodeId: nodeID,
			}, nil
		}
	}

	// 3) Match by peer_ip in seen_ips array
	err := s.db.QueryRowContext(ctx, `
		SELECT nf.tenant_id::text, nf.node_id
		FROM quartermaster.node_fingerprints nf
		JOIN quartermaster.infrastructure_nodes n ON n.node_id = nf.node_id
		JOIN quartermaster.infrastructure_clusters c ON c.cluster_id = n.cluster_id
		WHERE $1::inet = ANY(nf.seen_ips)
		  AND c.is_active = TRUE
		ORDER BY nf.last_seen DESC
		LIMIT 1
	`, peerIP).Scan(&tenantID, &nodeID)
	if err == nil {
		if upsertErr := s.upsertSeenIP(ctx, nodeID, peerIP); upsertErr != nil {
			s.logger.WithError(upsertErr).WithField("node_id", nodeID).Warn("Failed to update fingerprint seen IP")
		}
		return &quartermasterpb.ResolveNodeFingerprintResponse{
			TenantId:        tenantID,
			CanonicalNodeId: nodeID,
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
	_, err := s.db.ExecContext(ctx, `
		UPDATE quartermaster.node_fingerprints
		SET last_seen = NOW(), seen_ips = array_append(seen_ips, $1::inet)
		WHERE node_id = $2 AND NOT ($1::inet = ANY(seen_ips))
	`, peerIP, nodeID)
	return err
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
	var tokenID string
	var tenantID, clusterID sql.NullString
	var expectedIP sql.NullString
	var usageLimit sql.NullInt32
	var usageCount int32
	var expiresAt time.Time

	err = tx.QueryRowContext(ctx, `
		SELECT id, tenant_id::text, COALESCE(cluster_id, ''), usage_limit, usage_count, expires_at, expected_ip::text
		FROM quartermaster.bootstrap_tokens
		WHERE token_hash = $1 AND kind = 'edge_node'
		  AND (
		    (usage_limit IS NULL AND used_at IS NULL) OR
		    (usage_limit IS NOT NULL AND usage_count < usage_limit)
		  )
		FOR UPDATE
	`, hashBootstrapToken(token)).Scan(&tokenID, &tenantID, &clusterID, &usageLimit, &usageCount, &expiresAt, &expectedIP)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.Unauthenticated, "invalid or already used token")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Check expiration
	if time.Now().After(expiresAt) {
		return nil, status.Error(codes.Unauthenticated, "token expired")
	}

	clientIP := extractClientIP(ctx)
	if !validateExpectedIP(expectedIP, clientIP) {
		return nil, status.Error(codes.PermissionDenied, "client IP does not match token expected_ip")
	}

	// Validate tenant ID is present for edge_node tokens
	if !tenantID.Valid || tenantID.String == "" {
		return nil, status.Error(codes.InvalidArgument, "token missing tenant_id")
	}

	// Cluster enforcement: if token has a cluster_id binding, validate against caller's served set
	targetClusterID := req.GetTargetClusterId()
	tokenClusterID := clusterID.String
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
		err = tx.QueryRowContext(ctx, `
			SELECT cluster_id FROM quartermaster.infrastructure_clusters
			WHERE is_active = true
			ORDER BY cluster_name LIMIT 1
		`).Scan(&resolvedClusterID)
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
	var existingClusterID string
	err = tx.QueryRowContext(ctx, `
		SELECT cluster_id FROM quartermaster.infrastructure_nodes WHERE node_id = $1
	`, nodeID).Scan(&existingClusterID)
	if err == nil {
		if existingClusterID != resolvedClusterID {
			return nil, status.Errorf(codes.FailedPrecondition,
				"node %s already exists in cluster %s", nodeID, existingClusterID)
		}
		if upsertErr := upsertEdgeNodeFingerprint(ctx, tx, tenantID.String, nodeID, req); upsertErr != nil {
			return nil, upsertErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to commit: %v", commitErr)
		}
		return &quartermasterpb.BootstrapEdgeNodeResponse{
			NodeId:    nodeID,
			TenantId:  tenantID.String,
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

	_, err = tx.ExecContext(ctx, `
		INSERT INTO quartermaster.infrastructure_nodes (id, node_id, cluster_id, node_name, node_type, external_ip, latitude, longitude, tags, metadata, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'edge', $5::inet, $6, $7, '{}', '{}', NOW(), NOW())
	`, uuid.New().String(), nodeID, resolvedClusterID, hostname, extIP, lat, lng)

	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create node: %v", err)
	}

	if upsertErr := upsertEdgeNodeFingerprint(ctx, tx, tenantID.String, nodeID, req); upsertErr != nil {
		return nil, upsertErr
	}

	// Update token usage
	_, err = tx.ExecContext(ctx, `
		UPDATE quartermaster.bootstrap_tokens
		SET usage_count = usage_count + 1, used_at = NOW()
		WHERE id = $1
	`, tokenID)
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
		TenantId:  tenantID.String,
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
	var tokenID string
	var tenantID, clusterID sql.NullString
	var expectedIP sql.NullString
	var usageLimit sql.NullInt32
	var usageCount int32
	var expiresAt time.Time
	var tokenMetadata string

	err = tx.QueryRowContext(ctx, `
		SELECT id, tenant_id::text, COALESCE(cluster_id, ''), usage_limit, usage_count, expires_at, expected_ip::text, COALESCE(metadata::text, '{}')
		FROM quartermaster.bootstrap_tokens
		WHERE token_hash = $1 AND kind = 'infrastructure_node'
		  AND (
		    (usage_limit IS NULL AND used_at IS NULL) OR
		    (usage_limit IS NOT NULL AND usage_count < usage_limit)
		  )
		FOR UPDATE
	`, hashBootstrapToken(token)).Scan(&tokenID, &tenantID, &clusterID, &usageLimit, &usageCount, &expiresAt, &expectedIP, &tokenMetadata)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.Unauthenticated, "invalid or already used token")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	if time.Now().After(expiresAt) {
		return nil, status.Error(codes.Unauthenticated, "token expired")
	}

	clientIP := extractClientIP(ctx)
	if !validateExpectedIP(expectedIP, clientIP) {
		return nil, status.Error(codes.PermissionDenied, "client IP does not match token expected_ip")
	}

	// Cluster enforcement: if token has a cluster_id binding, validate against target
	targetClusterID := req.GetTargetClusterId()
	tokenClusterID := clusterID.String
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
		err = tx.QueryRowContext(ctx, `
			SELECT cluster_id FROM quartermaster.infrastructure_clusters
			WHERE is_active = true
			ORDER BY cluster_name LIMIT 1
		`).Scan(&resolvedClusterID)
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
	var existingClusterID string
	var existingWGIP sql.NullString
	var existingWGPort sql.NullInt32
	err = tx.QueryRowContext(ctx, `
		SELECT cluster_id, host(wireguard_ip), wireguard_listen_port
		FROM quartermaster.infrastructure_nodes
		WHERE node_id = $1
	`, nodeID).Scan(&existingClusterID, &existingWGIP, &existingWGPort)
	if err == nil {
		if existingClusterID != resolvedClusterID {
			return nil, status.Errorf(codes.FailedPrecondition, "node already exists in cluster %s", existingClusterID)
		}
		if strings.TrimSpace(tokenMetadata) != "" && tokenMetadata != "{}" {
			if _, updateErr := tx.ExecContext(ctx, `
					UPDATE quartermaster.infrastructure_nodes
					SET metadata = COALESCE(metadata, '{}'::jsonb) || $2::jsonb,
					    updated_at = NOW()
					WHERE node_id = $1
				`, nodeID, tokenMetadata); updateErr != nil {
				return nil, status.Errorf(codes.Internal, "update node metadata: %v", updateErr)
			}
		}
		// Commit the tx so subsequent reads see a consistent view even
		// though we didn't mutate anything.
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, status.Errorf(codes.Internal, "commit: %v", commitErr)
		}

		existingMeshCIDR, existingMeshPort := loadClusterMeshConfig(ctx, s.db, resolvedClusterID)
		if existingWGPort.Valid && existingWGPort.Int32 > 0 {
			existingMeshPort = existingWGPort.Int32
		}
		seedPeers, seedSvc := s.collectBootstrapSeed(ctx, resolvedClusterID, nodeID)
		wgIP := ""
		if existingWGIP.Valid {
			wgIP = existingWGIP.String
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
		if tenantID.Valid && tenantID.String != "" {
			t := tenantID.String
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
	var clusterMeshCIDR sql.NullString
	var clusterWGPort sql.NullInt32
	if cfgErr := tx.QueryRowContext(ctx, `
		SELECT wg_mesh_cidr, wg_listen_port
		FROM quartermaster.infrastructure_clusters
		WHERE cluster_id = $1
	`, resolvedClusterID).Scan(&clusterMeshCIDR, &clusterWGPort); cfgErr != nil {
		return nil, status.Errorf(codes.Internal, "load cluster mesh config: %v", cfgErr)
	}

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
	_, err = tx.ExecContext(ctx, `
			INSERT INTO quartermaster.infrastructure_nodes (id, node_id, cluster_id, node_name, node_type, external_ip, internal_ip, wireguard_ip, wireguard_public_key, wireguard_listen_port, enrollment_origin, latitude, longitude, tags, metadata, last_heartbeat, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6::inet, $7::inet, $8::inet, $9, $10, 'runtime_enrolled', $11, $12, '{}', $13::jsonb, NOW(), NOW(), NOW())
		`, uuid.New().String(), nodeID, resolvedClusterID, hostname, nodeType, extIP, intIP, assignedIP, wgPubStr, assignedPort, lat, lng, tokenMetadata)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create node: %v", err)
	}

	// Update token usage
	_, err = tx.ExecContext(ctx, `
		UPDATE quartermaster.bootstrap_tokens
		SET usage_count = usage_count + 1, used_at = NOW()
		WHERE id = $1
	`, tokenID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update token usage: %v", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to commit bootstrap: %v", err)
	}

	var tenantResp *string
	if tenantID.Valid && tenantID.String != "" {
		tenantResp = &tenantID.String
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

	_, err := tx.ExecContext(ctx, `
			INSERT INTO quartermaster.node_fingerprints (tenant_id, node_id, fingerprint_machine_sha256, fingerprint_macs_sha256, seen_ips, attrs)
			VALUES ($1, $2, NULLIF($3,''), NULLIF($4,''), $5::inet[], $6)
			ON CONFLICT (node_id) DO UPDATE SET
				tenant_id = EXCLUDED.tenant_id,
				fingerprint_machine_sha256 = COALESCE(EXCLUDED.fingerprint_machine_sha256, quartermaster.node_fingerprints.fingerprint_machine_sha256),
				fingerprint_macs_sha256 = COALESCE(EXCLUDED.fingerprint_macs_sha256, quartermaster.node_fingerprints.fingerprint_macs_sha256),
				attrs = CASE
					WHEN EXCLUDED.attrs IS NULL OR EXCLUDED.attrs = '{}'::jsonb THEN quartermaster.node_fingerprints.attrs
					ELSE EXCLUDED.attrs
				END,
				last_seen = NOW(),
				seen_ips = quartermaster.node_fingerprints.seen_ips || EXCLUDED.seen_ips
		`, tenantID, nodeID, machineIDSHA, macsSHA, pq.Array(ips), attrsJSON)
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

	var current string
	if err := tx.QueryRowContext(ctx, `
		SELECT enrollment_origin
		FROM quartermaster.infrastructure_nodes
		WHERE node_id = $1
	`, nodeID).Scan(&current); err != nil {
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

	if _, err := tx.ExecContext(ctx, `
		UPDATE quartermaster.infrastructure_nodes
		SET enrollment_origin = $1, updated_at = NOW()
		WHERE node_id = $2
	`, newOrigin, nodeID); err != nil {
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
	var tokenClusterID sql.NullString
	var expectedIP sql.NullString
	var expiresAt time.Time
	err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(cluster_id, ''), expires_at, expected_ip::text
		FROM quartermaster.bootstrap_tokens
		WHERE token_hash = $1 AND kind = 'infrastructure_node'
	`, hashBootstrapToken(token)).Scan(&tokenClusterID, &expiresAt, &expectedIP)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "replay: token lookup: %v", err)
	}
	if time.Now().After(expiresAt) {
		return nil, status.Error(codes.Unauthenticated, "token expired")
	}
	if !validateExpectedIP(expectedIP, extractClientIP(ctx)) {
		return nil, status.Error(codes.PermissionDenied, "client IP does not match token expected_ip")
	}

	var existingClusterID, existingPubKey sql.NullString
	var existingWGIP sql.NullString
	var existingWGPort sql.NullInt32
	var existingTenantID sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT
			n.cluster_id,
			n.wireguard_public_key,
			host(n.wireguard_ip),
			n.wireguard_listen_port,
			c.owner_tenant_id::text
		FROM quartermaster.infrastructure_nodes n
		JOIN quartermaster.infrastructure_clusters c ON c.cluster_id = n.cluster_id
		WHERE n.node_id = $1
	`, nodeID).Scan(&existingClusterID, &existingPubKey, &existingWGIP, &existingWGPort, &existingTenantID)
	if errors.Is(err, sql.ErrNoRows) {
		// No existing row — this is not a replay. Fall through to the
		// normal create path.
		return nil, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "replay: node lookup: %v", err)
	}

	if !existingPubKey.Valid || existingPubKey.String != wgPub {
		// Node exists but with a different public key. This is either a
		// conflict or an attacker guessing. Refuse — the non-replay path
		// would also refuse because node_id is already taken.
		return nil, status.Error(codes.FailedPrecondition, "node_id already registered with a different wireguard_public_key")
	}

	// Enforce the token's cluster binding against the stored row too: a
	// token scoped to cluster A must not retrieve an assignment in B.
	if tokenClusterID.Valid && tokenClusterID.String != "" && existingClusterID.Valid && tokenClusterID.String != existingClusterID.String {
		return nil, status.Errorf(codes.PermissionDenied, "token is bound to cluster %s, node is in %s", tokenClusterID.String, existingClusterID.String)
	}

	clusterIDStr := ""
	if existingClusterID.Valid {
		clusterIDStr = existingClusterID.String
	}
	wgIP := ""
	if existingWGIP.Valid {
		wgIP = existingWGIP.String
	}
	wgPort := int32(0)
	if existingWGPort.Valid {
		wgPort = existingWGPort.Int32
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
	if existingTenantID.Valid && existingTenantID.String != "" {
		t := existingTenantID.String
		resp.TenantId = &t
	}
	return resp, nil
}

// loadClusterMeshConfig returns the cluster's wg_mesh_cidr and default
// wg_listen_port. Failures degrade to zero values so the caller surfaces a
// sensible error rather than stalling the bootstrap flow.
func loadClusterMeshConfig(ctx context.Context, db *sql.DB, clusterID string) (string, int32) {
	var cidr sql.NullString
	var port sql.NullInt32
	row := db.QueryRowContext(ctx, `
		SELECT wg_mesh_cidr, wg_listen_port
		FROM quartermaster.infrastructure_clusters
		WHERE cluster_id = $1
	`, clusterID)
	// Scan errors surface as empty return values, which the caller treats as
	// "cluster mesh config missing" — FailedPrecondition with a remediation
	// hint. Logging the raw error here would be noisy on cold caches.
	_ = row.Scan(&cidr, &port) //nolint:errcheck
	cidrStr := ""
	if cidr.Valid {
		cidrStr = cidr.String
	}
	portVal := int32(0)
	if port.Valid {
		portVal = port.Int32
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

	rows, err := s.db.QueryContext(ctx, `
		SELECT n.node_name, n.wireguard_public_key, host(n.external_ip), host(n.internal_ip), host(n.wireguard_ip), n.wireguard_listen_port
		FROM quartermaster.infrastructure_nodes n
		WHERE n.node_id != $1
		  AND (n.cluster_id = $2 OR n.node_id = ANY($3))
		  AND n.wireguard_public_key IS NOT NULL
		  AND n.wireguard_ip IS NOT NULL
		  AND n.status = 'active'
	`, excludeNodeID, clusterID, pq.Array(sortedStringKeys(requiredPeerNodeIDs)))
	if err != nil {
		s.logger.WithError(err).Warn("collectBootstrapSeed: peer query failed")
		return nil, endpoints
	}
	defer func() { _ = rows.Close() }()

	var peers []*quartermasterpb.InfrastructurePeer
	for rows.Next() {
		var p quartermasterpb.InfrastructurePeer
		var extIP, intIP, wgIP sql.NullString
		var listenPort sql.NullInt32
		if scanErr := rows.Scan(&p.NodeName, &p.PublicKey, &extIP, &intIP, &wgIP, &listenPort); scanErr != nil {
			continue
		}
		endpoint := ""
		if extIP.Valid && extIP.String != "" {
			endpoint = extIP.String
		} else if intIP.Valid && intIP.String != "" {
			endpoint = intIP.String
		}
		if endpoint == "" || !wgIP.Valid {
			continue
		}
		port := int32(51820)
		if listenPort.Valid && listenPort.Int32 > 0 {
			port = listenPort.Int32
		}
		p.Endpoint = fmt.Sprintf("%s:%d", endpoint, port)
		p.AllowedIps = []string{wgIP.String + "/32"}
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
		rows, err := s.db.QueryContext(ctx, `
			SELECT DISTINCT service_type
			FROM (
				SELECT s.type AS service_type
				FROM quartermaster.service_instances si
				JOIN quartermaster.services s ON s.service_id = si.service_id
				WHERE si.node_id = $1
				  AND si.status IN ('running', 'active')
				  AND s.type IS NOT NULL
				  AND s.type <> ''
				UNION ALL
				SELECT jsonb_array_elements_text(
					CASE
						WHEN jsonb_typeof(n.metadata->'desired_service_types') = 'array' THEN n.metadata->'desired_service_types'
						ELSE '[]'::jsonb
					END
				) AS service_type
				FROM quartermaster.infrastructure_nodes n
				WHERE n.node_id = $1
				UNION ALL
				SELECT jsonb_array_elements_text(
					CASE
						WHEN jsonb_typeof(n.metadata->'service_types') = 'array' THEN n.metadata->'service_types'
						ELSE '[]'::jsonb
					END
				) AS service_type
				FROM quartermaster.infrastructure_nodes n
				WHERE n.node_id = $1
			) local_services
			WHERE service_type IS NOT NULL AND service_type <> ''
		`, nodeID)
		if err != nil {
			return fmt.Errorf("local service query: %w", err)
		}
		defer func() { _ = rows.Close() }()

		var services []string
		for rows.Next() {
			var serviceType string
			if scanErr := rows.Scan(&serviceType); scanErr != nil {
				return fmt.Errorf("scan local service: %w", scanErr)
			}
			if serviceType != "" {
				services = append(services, serviceType)
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate local services: %w", err)
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
		svcRows, svcErr := s.db.QueryContext(ctx, `
			WITH request_contexts AS (
				SELECT $1::text AS cluster_id WHERE $1 <> ''
				UNION
				SELECT si.cluster_id
				FROM quartermaster.service_instances si
				WHERE si.node_id = $2
				  AND si.status IN ('running', 'active')
				  AND si.cluster_id IS NOT NULL
				  AND si.cluster_id <> ''
				UNION
				SELECT sca.cluster_id
				FROM quartermaster.service_instances si
				JOIN quartermaster.service_cluster_assignments sca
				  ON sca.service_instance_id = si.id AND sca.is_active = true
				WHERE si.node_id = $2
				  AND si.status IN ('running', 'active')
				  AND sca.cluster_id IS NOT NULL
				  AND sca.cluster_id <> ''
				UNION
				SELECT jsonb_array_elements_text(
					CASE
						WHEN jsonb_typeof(n.metadata->'desired_cluster_ids') = 'array' THEN n.metadata->'desired_cluster_ids'
						ELSE '[]'::jsonb
					END
				) AS cluster_id
				FROM quartermaster.infrastructure_nodes n
				WHERE n.node_id = $2
				UNION
				SELECT jsonb_array_elements_text(
					CASE
						WHEN jsonb_typeof(n.metadata->'service_cluster_ids') = 'array' THEN n.metadata->'service_cluster_ids'
						ELSE '[]'::jsonb
					END
				) AS cluster_id
				FROM quartermaster.infrastructure_nodes n
				WHERE n.node_id = $2
				UNION
				SELECT jsonb_array_elements_text(
					CASE
						WHEN jsonb_typeof(n.metadata->'logical_cluster_ids') = 'array' THEN n.metadata->'logical_cluster_ids'
						ELSE '[]'::jsonb
					END
				) AS cluster_id
				FROM quartermaster.infrastructure_nodes n
				WHERE n.node_id = $2
			),
			eligible AS (
				SELECT s.type,
				       si.node_id,
				       host(n.wireguard_ip) AS wireguard_ip,
				       COALESCE(NULLIF(sca.cluster_id, ''), NULLIF(si.cluster_id, ''), n.cluster_id) AS provider_cluster
				FROM quartermaster.services s
				JOIN quartermaster.service_instances si ON si.service_id = s.service_id
				JOIN quartermaster.infrastructure_nodes n ON n.node_id = si.node_id
				LEFT JOIN quartermaster.service_cluster_assignments sca
				  ON sca.service_instance_id = si.id AND sca.is_active = true
				WHERE si.status IN ('running', 'active')
				  AND n.wireguard_ip IS NOT NULL
				  AND n.status = 'active'
				  AND (s.type = ANY($3::text[]) OR s.type = ANY($4::text[]))
			),
			service_scope AS (
				SELECT type, COUNT(DISTINCT provider_cluster) AS provider_cluster_count
				FROM eligible
				WHERE provider_cluster IS NOT NULL AND provider_cluster <> ''
				GROUP BY type
			)
			SELECT DISTINCT e.type, e.node_id, e.wireguard_ip
			FROM eligible e
			JOIN service_scope ss ON ss.type = e.type
			WHERE e.type = ANY($4::text[])
			   OR e.provider_cluster IN (SELECT cluster_id FROM request_contexts)
			   OR ss.provider_cluster_count = 1
		`, clusterID, nodeID, pq.Array(peerTypes), pq.Array(globalPeerTypes))
		if svcErr != nil {
			return fmt.Errorf("service endpoint query: %w", svcErr)
		}
		defer func() { _ = svcRows.Close() }()

		nextEndpoints := map[string]*quartermasterpb.ServiceEndpoints{}
		nextRequiredPeerNodeIDs := map[string]struct{}{}
		for svcRows.Next() {
			var svcType, svcNodeID, svcIP string
			if scanErr := svcRows.Scan(&svcType, &svcNodeID, &svcIP); scanErr != nil {
				return fmt.Errorf("scan service endpoint: %w", scanErr)
			}
			if svcIP == "" {
				continue
			}
			if svcNodeID != "" {
				nextRequiredPeerNodeIDs[svcNodeID] = struct{}{}
			}
			if _, ok := dnsRequired[svcType]; !ok {
				continue
			}
			if nextEndpoints[svcType] == nil {
				nextEndpoints[svcType] = &quartermasterpb.ServiceEndpoints{Ips: []string{}}
			}
			nextEndpoints[svcType].Ips = append(nextEndpoints[svcType].Ips, svcIP)
		}
		if err := svcRows.Err(); err != nil {
			return fmt.Errorf("iterate service endpoints: %w", err)
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
	for _, dep := range dedupeInfraDependencies(infraRequired) {
		var depPeerNodeIDs []string
		err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			rows, err := s.db.QueryContext(ctx, `
				WITH request_contexts AS (
					SELECT $1::text AS cluster_id WHERE $1 <> ''
					UNION
					SELECT si.cluster_id
					FROM quartermaster.service_instances si
					WHERE si.node_id = $2
					  AND si.status IN ('running', 'active')
					  AND si.cluster_id IS NOT NULL
					  AND si.cluster_id <> ''
					UNION
					SELECT sca.cluster_id
					FROM quartermaster.service_instances si
						JOIN quartermaster.service_cluster_assignments sca
						  ON sca.service_instance_id = si.id AND sca.is_active = true
						WHERE si.node_id = $2
						  AND si.status IN ('running', 'active')
						  AND sca.cluster_id IS NOT NULL
						  AND sca.cluster_id <> ''
						UNION
						SELECT jsonb_array_elements_text(
							CASE
								WHEN jsonb_typeof(n.metadata->'desired_cluster_ids') = 'array' THEN n.metadata->'desired_cluster_ids'
								ELSE '[]'::jsonb
							END
						) AS cluster_id
						FROM quartermaster.infrastructure_nodes n
						WHERE n.node_id = $2
						UNION
						SELECT jsonb_array_elements_text(
							CASE
								WHEN jsonb_typeof(n.metadata->'service_cluster_ids') = 'array' THEN n.metadata->'service_cluster_ids'
								ELSE '[]'::jsonb
							END
						) AS cluster_id
						FROM quartermaster.infrastructure_nodes n
						WHERE n.node_id = $2
						UNION
						SELECT jsonb_array_elements_text(
							CASE
								WHEN jsonb_typeof(n.metadata->'logical_cluster_ids') = 'array' THEN n.metadata->'logical_cluster_ids'
								ELSE '[]'::jsonb
							END
						) AS cluster_id
						FROM quartermaster.infrastructure_nodes n
						WHERE n.node_id = $2
					),
				eligible AS (
					SELECT si.node_id,
					       COALESCE(NULLIF(sca.cluster_id, ''), NULLIF(si.cluster_id, ''), n.cluster_id) AS provider_cluster,
					       COALESCE(si.metadata->>'infra_role', '') AS infra_role,
					       COALESCE(si.metadata->>'infra_name', '') AS infra_name
					FROM quartermaster.services svc
					JOIN quartermaster.service_instances si ON si.service_id = svc.service_id
					JOIN quartermaster.infrastructure_nodes n ON n.node_id = si.node_id
					LEFT JOIN quartermaster.service_cluster_assignments sca
					  ON sca.service_instance_id = si.id AND sca.is_active = true
					WHERE si.status IN ('running', 'active')
					  AND n.wireguard_ip IS NOT NULL
					  AND n.status = 'active'
					  AND svc.type = $3
					  AND svc.plane = 'infra'
				),
				service_scope AS (
					SELECT COUNT(DISTINCT provider_cluster) AS provider_cluster_count
					FROM eligible
					WHERE provider_cluster IS NOT NULL AND provider_cluster <> ''
				)
				SELECT DISTINCT e.node_id
				FROM eligible e
				CROSS JOIN service_scope ss
				WHERE (
					$4 = 'primary' AND e.infra_role = 'primary'
				) OR (
					$4 = 'aggregator' AND e.infra_role = 'aggregator'
				) OR (
					$4 = 'named' AND e.infra_name = $5 AND (
						e.provider_cluster IN (SELECT cluster_id FROM request_contexts)
						OR ss.provider_cluster_count = 1
					)
				) OR (
					$4 = 'regional' AND e.infra_role = 'regional' AND (
						e.provider_cluster IN (SELECT cluster_id FROM request_contexts)
						OR ss.provider_cluster_count = 1
					)
				)
			`, clusterID, nodeID, dep.Kind, dep.Provider, dep.Name)
			if err != nil {
				return fmt.Errorf("infra provider query kind=%s provider=%s name=%s: %w", dep.Kind, dep.Provider, dep.Name, err)
			}
			defer func() { _ = rows.Close() }()

			var nextPeerNodeIDs []string
			for rows.Next() {
				var peerNodeID string
				if scanErr := rows.Scan(&peerNodeID); scanErr != nil {
					return fmt.Errorf("scan infra provider kind=%s provider=%s name=%s: %w", dep.Kind, dep.Provider, dep.Name, scanErr)
				}
				if peerNodeID != "" {
					nextPeerNodeIDs = append(nextPeerNodeIDs, peerNodeID)
				}
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("iterate infra provider kind=%s provider=%s name=%s: %w", dep.Kind, dep.Provider, dep.Name, err)
			}
			depPeerNodeIDs = nextPeerNodeIDs
			return nil
		})
		if err != nil {
			return nil, err
		}
		for _, peerNodeID := range depPeerNodeIDs {
			out[peerNodeID] = struct{}{}
		}
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

	var dependentPeerNodeIDs []string
	queryErr := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		rows, queryErr := s.db.QueryContext(ctx, `
				WITH dependency_input AS (
					SELECT provided_type, dependent_type, is_global
					FROM unnest($3::text[], $4::text[], $5::boolean[]) AS t(provided_type, dependent_type, is_global)
				),
				provided AS (
					SELECT DISTINCT di.provided_type,
					       COALESCE(NULLIF(sca.cluster_id, ''), NULLIF(si.cluster_id, ''), n.cluster_id, $2) AS provider_cluster
					FROM dependency_input di
					JOIN quartermaster.services svc ON svc.type = di.provided_type
					JOIN quartermaster.service_instances si ON si.service_id = svc.service_id
					JOIN quartermaster.infrastructure_nodes n ON n.node_id = si.node_id
					LEFT JOIN quartermaster.service_cluster_assignments sca
					  ON sca.service_instance_id = si.id AND sca.is_active = true
					WHERE si.node_id = $1
					  AND si.status IN ('running', 'active')
					UNION
					SELECT DISTINCT di.provided_type, $2::text AS provider_cluster
					FROM dependency_input di
				),
				service_scope AS (
					SELECT di.provided_type,
					       COUNT(DISTINCT COALESCE(NULLIF(sca.cluster_id, ''), NULLIF(si.cluster_id, ''), n.cluster_id)) AS provider_cluster_count
					FROM dependency_input di
					JOIN quartermaster.services svc ON svc.type = di.provided_type
					JOIN quartermaster.service_instances si ON si.service_id = svc.service_id
					JOIN quartermaster.infrastructure_nodes n ON n.node_id = si.node_id
					LEFT JOIN quartermaster.service_cluster_assignments sca
					  ON sca.service_instance_id = si.id AND sca.is_active = true
					WHERE si.status IN ('running', 'active')
					  AND n.wireguard_ip IS NOT NULL
					  AND n.status = 'active'
					GROUP BY di.provided_type
				),
				node_services AS (
					SELECT si.node_id, svc.type AS service_type
					FROM quartermaster.service_instances si
					JOIN quartermaster.services svc ON svc.service_id = si.service_id
					WHERE si.status IN ('running', 'active')
					  AND svc.type IS NOT NULL
					  AND svc.type <> ''
					UNION
					SELECT n.node_id, jsonb_array_elements_text(
						CASE
							WHEN jsonb_typeof(n.metadata->'desired_service_types') = 'array' THEN n.metadata->'desired_service_types'
							ELSE '[]'::jsonb
						END
					) AS service_type
					FROM quartermaster.infrastructure_nodes n
					UNION
					SELECT n.node_id, jsonb_array_elements_text(
						CASE
							WHEN jsonb_typeof(n.metadata->'service_types') = 'array' THEN n.metadata->'service_types'
							ELSE '[]'::jsonb
						END
					) AS service_type
					FROM quartermaster.infrastructure_nodes n
				),
				consumer_contexts AS (
					SELECT node_id, cluster_id
					FROM quartermaster.infrastructure_nodes
					WHERE cluster_id IS NOT NULL AND cluster_id <> ''
					UNION
					SELECT si.node_id, si.cluster_id
					FROM quartermaster.service_instances si
					WHERE si.status IN ('running', 'active')
					  AND si.cluster_id IS NOT NULL
					  AND si.cluster_id <> ''
					UNION
					SELECT si.node_id, sca.cluster_id
					FROM quartermaster.service_instances si
					JOIN quartermaster.service_cluster_assignments sca
					  ON sca.service_instance_id = si.id AND sca.is_active = true
					WHERE si.status IN ('running', 'active')
					  AND sca.cluster_id IS NOT NULL
					  AND sca.cluster_id <> ''
					UNION
					SELECT n.node_id, jsonb_array_elements_text(
						CASE
							WHEN jsonb_typeof(n.metadata->'desired_cluster_ids') = 'array' THEN n.metadata->'desired_cluster_ids'
							ELSE '[]'::jsonb
						END
					) AS cluster_id
					FROM quartermaster.infrastructure_nodes n
					UNION
					SELECT n.node_id, jsonb_array_elements_text(
						CASE
							WHEN jsonb_typeof(n.metadata->'service_cluster_ids') = 'array' THEN n.metadata->'service_cluster_ids'
							ELSE '[]'::jsonb
						END
					) AS cluster_id
					FROM quartermaster.infrastructure_nodes n
					UNION
					SELECT n.node_id, jsonb_array_elements_text(
						CASE
							WHEN jsonb_typeof(n.metadata->'logical_cluster_ids') = 'array' THEN n.metadata->'logical_cluster_ids'
							ELSE '[]'::jsonb
						END
					) AS cluster_id
					FROM quartermaster.infrastructure_nodes n
				)
				SELECT DISTINCT n.node_id
				FROM quartermaster.infrastructure_nodes n
				JOIN node_services ns ON ns.node_id = n.node_id
				JOIN dependency_input di ON di.dependent_type = ns.service_type
				LEFT JOIN service_scope ss ON ss.provided_type = di.provided_type
				WHERE n.node_id <> $1
				  AND n.wireguard_public_key IS NOT NULL
				  AND n.wireguard_ip IS NOT NULL
				  AND n.status = 'active'
				  AND (
					di.is_global
					OR
					COALESCE(ss.provider_cluster_count, 0) <= 1
					OR EXISTS (
						SELECT 1
						FROM provided p
						JOIN consumer_contexts cc ON cc.node_id = n.node_id AND cc.cluster_id = p.provider_cluster
						WHERE p.provided_type = di.provided_type
					)
				  )
			`, nodeID, clusterID, pq.Array(providedTypes), pq.Array(dependentTypes), pq.Array(globalFlags))
		if queryErr != nil {
			return fmt.Errorf("dependent node query: %w", queryErr)
		}
		defer func() { _ = rows.Close() }()

		var nextPeerNodeIDs []string
		for rows.Next() {
			var peerNodeID string
			if scanErr := rows.Scan(&peerNodeID); scanErr != nil {
				return fmt.Errorf("scan dependent node: %w", scanErr)
			}
			if peerNodeID != "" {
				nextPeerNodeIDs = append(nextPeerNodeIDs, peerNodeID)
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate dependent nodes: %w", err)
		}
		dependentPeerNodeIDs = nextPeerNodeIDs
		return nil
	})
	if queryErr != nil {
		return nil, queryErr
	}
	for _, peerNodeID := range dependentPeerNodeIDs {
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
		rows, err := s.db.QueryContext(ctx, `
			SELECT DISTINCT service_type
			FROM (
				SELECT svc.type AS service_type
				FROM quartermaster.service_instances si
				JOIN quartermaster.services svc ON svc.service_id = si.service_id
				WHERE si.node_id = $1
				  AND si.status IN ('running', 'active')
				  AND svc.type IS NOT NULL
				  AND svc.type <> ''
				UNION ALL
				SELECT jsonb_array_elements_text(
					CASE
						WHEN jsonb_typeof(n.metadata->'desired_service_types') = 'array' THEN n.metadata->'desired_service_types'
						ELSE '[]'::jsonb
					END
				) AS service_type
				FROM quartermaster.infrastructure_nodes n
				WHERE n.node_id = $1
				UNION ALL
				SELECT jsonb_array_elements_text(
					CASE
						WHEN jsonb_typeof(n.metadata->'service_types') = 'array' THEN n.metadata->'service_types'
						ELSE '[]'::jsonb
					END
				) AS service_type
				FROM quartermaster.infrastructure_nodes n
				WHERE n.node_id = $1
			) local_services
			WHERE service_type IS NOT NULL AND service_type <> ''
		`, nodeID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()

		var next []string
		for rows.Next() {
			var serviceType string
			if scanErr := rows.Scan(&serviceType); scanErr != nil {
				return scanErr
			}
			if serviceType != "" {
				next = append(next, serviceType)
			}
		}
		if err := rows.Err(); err != nil {
			return err
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

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO quartermaster.bootstrap_tokens (id, name, token_hash, token_prefix, kind, tenant_id, cluster_id, expected_ip, metadata, usage_limit, usage_count, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, COALESCE($9::jsonb, '{}'::jsonb), $10, 0, $11, NOW())
	`, tokenID, name, hashBootstrapToken(tokenValue), tokenPrefix(tokenValue), kind, req.TenantId, req.ClusterId, req.ExpectedIp, metadataJSON, req.UsageLimit, expiresAt)

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

	where := "WHERE 1=1"
	args := []any{}
	argIdx := 1

	if req.GetKind() != "" {
		where += fmt.Sprintf(" AND kind = $%d", argIdx)
		args = append(args, req.GetKind())
		argIdx++
	}
	if req.GetTenantId() != "" {
		where += fmt.Sprintf(" AND tenant_id = $%d", argIdx)
		args = append(args, req.GetTenantId())
		argIdx++
	}

	// Direction-aware keyset condition
	if params.Cursor != nil {
		if params.Direction == pagination.Backward {
			where += fmt.Sprintf(" AND (created_at, id) > ($%d, $%d)", argIdx, argIdx+1)
		} else {
			where += fmt.Sprintf(" AND (created_at, id) < ($%d, $%d)", argIdx, argIdx+1)
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
		SELECT id, name, token_prefix, kind, tenant_id, cluster_id, expected_ip, usage_limit, usage_count, expires_at, used_at, created_by, created_at
		FROM quartermaster.bootstrap_tokens
		%s
		ORDER BY created_at %s, id %s
		LIMIT $%d
	`, where, orderDir, orderDir, argIdx)
	args = append(args, params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var tokens []*quartermasterpb.BootstrapToken
	for rows.Next() {
		var token quartermasterpb.BootstrapToken
		var tenantID, clusterID, expectedIP, createdBy sql.NullString
		var usageLimit sql.NullInt32
		var usedAt sql.NullTime
		var expiresAt, createdAt time.Time

		err := rows.Scan(&token.Id, &token.Name, &token.Token, &token.Kind,
			&tenantID, &clusterID, &expectedIP, &usageLimit, &token.UsageCount,
			&expiresAt, &usedAt, &createdBy, &createdAt)
		if err != nil {
			continue
		}

		if tenantID.Valid {
			token.TenantId = &tenantID.String
		}
		if clusterID.Valid {
			token.ClusterId = &clusterID.String
		}
		if expectedIP.Valid {
			token.ExpectedIp = &expectedIP.String
		}
		if usageLimit.Valid {
			token.UsageLimit = &usageLimit.Int32
		}
		if usedAt.Valid {
			token.UsedAt = timestamppb.New(usedAt.Time)
		}
		if createdBy.Valid {
			token.CreatedBy = &createdBy.String
		}
		token.ExpiresAt = timestamppb.New(expiresAt)
		token.CreatedAt = timestamppb.New(createdAt)

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

	result, err := s.db.ExecContext(ctx, `DELETE FROM quartermaster.bootstrap_tokens WHERE id = $1`, tokenID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to revoke token: %v", err)
	}

	rows, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		return nil, status.Errorf(codes.Internal, "failed to inspect revoke result: %v", rowsErr)
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

	var kind string
	var tenantID, clusterID sql.NullString
	var expectedIP sql.NullString
	var expiresAt time.Time
	var usageLimit sql.NullInt32
	var usageCount int32
	var usedAt sql.NullTime
	var metadataJSON []byte

	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.db.QueryRowContext(ctx, `
			SELECT kind, tenant_id, cluster_id, expected_ip::text, expires_at, usage_limit, usage_count, used_at, COALESCE(metadata, '{}'::jsonb)
			FROM quartermaster.bootstrap_tokens
			WHERE token_hash = $1
		`, hashBootstrapToken(token)).Scan(&kind, &tenantID, &clusterID, &expectedIP, &expiresAt, &usageLimit, &usageCount, &usedAt, &metadataJSON)
	})

	if errors.Is(err, sql.ErrNoRows) {
		return &quartermasterpb.ValidateBootstrapTokenResponse{Valid: false, Reason: "not_found"}, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Single-use tokens (usage_limit IS NULL) are consumed when used_at is set
	if !usageLimit.Valid && usedAt.Valid {
		return &quartermasterpb.ValidateBootstrapTokenResponse{Valid: false, Kind: kind, Reason: "already_used"}, nil
	}

	// Multi-use tokens: reject when usage_count >= usage_limit
	if usageLimit.Valid && usageLimit.Int32 > 0 && usageCount >= usageLimit.Int32 {
		return &quartermasterpb.ValidateBootstrapTokenResponse{Valid: false, Kind: kind, Reason: "usage_exceeded"}, nil
	}

	if time.Now().After(expiresAt) {
		return &quartermasterpb.ValidateBootstrapTokenResponse{Valid: false, Kind: kind, Reason: "expired"}, nil
	}

	// IP binding: if client_ip is provided and token has expected_ip, validate match
	if clientIP := req.GetClientIp(); clientIP != "" {
		if !validateExpectedIP(expectedIP, clientIP) {
			return &quartermasterpb.ValidateBootstrapTokenResponse{Valid: false, Kind: kind, Reason: "ip_mismatch"}, nil
		}
	}

	// Consume: increment usage_count if requested (PreRegisterEdge uses this)
	if req.GetConsume() {
		var result sql.Result
		updateErr := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			var err error
			result, err = s.db.ExecContext(ctx, `
				UPDATE quartermaster.bootstrap_tokens
				SET usage_count = usage_count + 1, used_at = NOW()
				WHERE token_hash = $1
				  AND expires_at > NOW()
				  AND (
					(usage_limit IS NULL AND used_at IS NULL) OR
					(usage_limit IS NOT NULL AND usage_count < usage_limit)
				  )
			`, hashBootstrapToken(token))
			return err
		})
		if updateErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to consume bootstrap token: %v", updateErr)
		}
		rowsAffected, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to verify bootstrap token consumption: %v", rowsErr)
		}
		if rowsAffected == 0 {
			return &quartermasterpb.ValidateBootstrapTokenResponse{Valid: false, Kind: kind, Reason: "already_used"}, nil
		}
	}

	resp := &quartermasterpb.ValidateBootstrapTokenResponse{
		Valid: true,
		Kind:  kind,
	}
	if tenantID.Valid {
		resp.TenantId = tenantID.String
	}
	if clusterID.Valid {
		resp.ClusterId = clusterID.String
		// Bootstrap tokens are consumed before an edge joins the mesh, so expose
		// the public Foghorn edge listener rather than the internal control
		// assignment used by service-to-service clients.
		if addr, lookupErr := s.lookupClusterPublicFoghornGRPC(ctx, clusterID.String); lookupErr == nil {
			resp.FoghornGrpcAddr = addr
		}
	}
	if len(metadataJSON) > 0 {
		var metadataMap map[string]any
		if json.Unmarshal(metadataJSON, &metadataMap) == nil && len(metadataMap) > 0 {
			resp.Metadata = mapToStruct(metadataMap)
		}
	}
	return resp, nil
}

func (s *QuartermasterServer) lookupClusterPublicFoghornGRPC(ctx context.Context, clusterID string) (string, error) {
	var rootDomain string
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.db.QueryRowContext(ctx, `
			SELECT COALESCE(NULLIF(c.base_url, ''), NULLIF(control.base_url, ''), '')
			FROM quartermaster.infrastructure_clusters c
			LEFT JOIN quartermaster.infrastructure_clusters control
			  ON control.cluster_id = c.control_cell_id AND control.is_active = true
			WHERE c.cluster_id = $1 AND c.is_active = true
		`, clusterID).Scan(&rootDomain)
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
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.db.QueryRowContext(ctx, `
			SELECT si.advertise_host || ':' || si.port
			FROM quartermaster.service_instances si
			JOIN quartermaster.service_cluster_assignments sca ON sca.service_instance_id = si.id
			JOIN quartermaster.services svc ON svc.service_id = si.service_id
			WHERE sca.cluster_id = $1
			  AND sca.is_active = true
			  AND si.status = 'running'
			  AND si.health_status = 'healthy'
			  AND si.protocol = 'grpc'
			  AND (si.metadata->>'foghorn_listener' = 'internal_control' OR si.port = 18019 OR si.metadata->>'foghorn_listener' = 'control')
			  AND svc.type = 'foghorn'
			ORDER BY CASE WHEN si.metadata->>'foghorn_listener' = 'internal_control' THEN 0 WHEN si.port = 18019 THEN 1 WHEN si.metadata->>'foghorn_listener' = 'control' THEN 2 ELSE 3 END, si.updated_at DESC, si.id ASC
			LIMIT 1
		`, clusterID).Scan(&addr)
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
		}
		for key, val := range phaseDurations {
			fields[key] = val
		}
		s.logger.WithFields(fields).Warn("Slow SyncMesh")
	}()

	// 1. Check if node exists and get current GitOps WireGuard identity.
	var currentWgIP sql.NullString
	var storedPublicKey sql.NullString
	var externalIP, internalIP sql.NullString
	var storedListenPort sql.NullInt32
	phaseStarted := time.Now()
	err := s.db.QueryRowContext(ctx, `
		SELECT host(wireguard_ip), wireguard_public_key, host(external_ip), host(internal_ip), wireguard_listen_port, cluster_id
		FROM quartermaster.infrastructure_nodes
		WHERE node_id = $1
	`, nodeID).Scan(&currentWgIP, &storedPublicKey, &externalIP, &internalIP, &storedListenPort, &clusterID)
	recordPhase("identity_lookup", phaseStarted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "node not found - please register the node first")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get node info: %v", err)
	}

	if !currentWgIP.Valid || strings.TrimSpace(currentWgIP.String) == "" {
		return nil, status.Errorf(codes.FailedPrecondition, "node %q has no recorded wireguard_ip; gitops_seed nodes need `frameworks mesh wg generate` + provision, runtime_enrolled nodes need `frameworks mesh join`", nodeID)
	}
	wireguardIP := currentWgIP.String
	if !storedPublicKey.Valid || strings.TrimSpace(storedPublicKey.String) == "" {
		return nil, status.Errorf(codes.FailedPrecondition, "node %q has no recorded wireguard_public_key; gitops_seed nodes need `frameworks mesh wg generate` + provision, runtime_enrolled nodes need `frameworks mesh join`", nodeID)
	}
	if publicKey == "" {
		return nil, status.Error(codes.InvalidArgument, "public_key required")
	}
	if storedPublicKey.String != publicKey {
		return nil, status.Errorf(codes.FailedPrecondition, "node %q public key does not match the recorded value", nodeID)
	}
	if !storedListenPort.Valid || storedListenPort.Int32 <= 0 {
		return nil, status.Errorf(codes.FailedPrecondition, "node %q has no recorded wireguard_listen_port", nodeID)
	}
	if req.GetListenPort() > 0 && req.GetListenPort() != storedListenPort.Int32 {
		return nil, status.Errorf(codes.FailedPrecondition, "node %q listen port %d does not match the recorded value %d", nodeID, req.GetListenPort(), storedListenPort.Int32)
	}
	wireguardPort := storedListenPort.Int32

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
	// Snapshot is conditionally written: old Privateer clients send no
	// snapshot, and a single failed collection on a new client should not
	// blank a previously-good row. Freshness is based on Quartermaster receipt
	// time so node clock skew cannot make a stale node look healthy.
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
		_, err = s.db.ExecContext(ctx, `
			UPDATE quartermaster.infrastructure_nodes
			SET last_heartbeat = NOW(),
			    applied_mesh_revision = $2,
			    status = 'active',
			    snapshot_cpu_percent = $3,
			    snapshot_ram_used_bytes = $4,
			    snapshot_ram_total_bytes = $5,
			    snapshot_disk_used_bytes = $6,
			    snapshot_disk_total_bytes = $7,
			    snapshot_uptime_seconds = $8,
			    snapshot_at = $9,
			    updated_at = NOW()
			WHERE node_id = $1
		`, nodeID, appliedRev, snapCPU, snapRamUsed, snapRamTotal, snapDiskUsed, snapDiskTotal, snapUptime, snapAt)
	} else {
		_, err = s.db.ExecContext(ctx, `
			UPDATE quartermaster.infrastructure_nodes
			SET last_heartbeat = NOW(),
			    applied_mesh_revision = $2,
			    status = 'active',
			    updated_at = NOW()
			WHERE node_id = $1
		`, nodeID, appliedRev)
	}
	recordPhase("heartbeat_update", phaseStarted)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to update node heartbeat")
	}

	phaseStarted = time.Now()
	dnsRequired, peerRequired, globalPeerRequired, infraRequired, reqErr := s.meshServiceRequirements(ctx, nodeID)
	recordPhase("service_requirements", phaseStarted)
	if reqErr != nil {
		return nil, status.Errorf(codes.Internal, "mesh service requirements unavailable: %v", reqErr)
	}
	phaseStarted = time.Now()
	serviceEndpoints, requiredPeerNodeIDs, endpointErr := s.collectMeshServiceEndpoints(ctx, clusterID, nodeID, dnsRequired, peerRequired, globalPeerRequired)
	recordPhase("service_endpoints", phaseStarted)
	if endpointErr != nil {
		return nil, status.Errorf(codes.Internal, "mesh service endpoints unavailable: %v", endpointErr)
	}
	serviceEndpointTypeCount = len(serviceEndpoints)
	phaseStarted = time.Now()
	infraPeers, infraErr := s.collectInfraPeerNodeIDs(ctx, clusterID, nodeID, infraRequired)
	recordPhase("infra_peers", phaseStarted)
	if infraErr != nil {
		return nil, status.Errorf(codes.Internal, "mesh infra peers unavailable: %v", infraErr)
	}
	for peerNodeID := range infraPeers {
		requiredPeerNodeIDs[peerNodeID] = struct{}{}
	}
	phaseStarted = time.Now()
	reciprocalPeers, reciprocalErr := s.collectReciprocalServicePeerNodeIDs(ctx, clusterID, nodeID)
	recordPhase("reciprocal_peers", phaseStarted)
	if reciprocalErr != nil {
		return nil, status.Errorf(codes.Internal, "mesh reciprocal peers unavailable: %v", reciprocalErr)
	}
	for peerNodeID := range reciprocalPeers {
		requiredPeerNodeIDs[peerNodeID] = struct{}{}
	}
	requiredPeerCount = len(requiredPeerNodeIDs)

	// 3. Get active peer nodes with WireGuard configured. Nodes always see
	// same-cluster peers; cross-cluster peers are limited to required service
	// endpoints and direct federation targets.
	phaseStarted = time.Now()
	rows, err := s.db.QueryContext(ctx, `
		SELECT n.node_name, n.wireguard_public_key, host(n.external_ip), host(n.internal_ip), host(n.wireguard_ip), n.wireguard_listen_port
		FROM quartermaster.infrastructure_nodes n
		WHERE n.node_id != $1
		  AND (n.cluster_id = $2 OR n.node_id = ANY($3))
		  AND n.wireguard_public_key IS NOT NULL
		  AND n.wireguard_ip IS NOT NULL
		  AND n.status = 'active'
	`, nodeID, clusterID, pq.Array(sortedStringKeys(requiredPeerNodeIDs)))
	if err != nil {
		recordPhase("peer_query", phaseStarted)
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

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
	for rows.Next() {
		var peer quartermasterpb.InfrastructurePeer
		var peerExtIP, peerIntIP, peerWgIP sql.NullString
		var peerListenPort sql.NullInt32
		if scanErr := rows.Scan(&peer.NodeName, &peer.PublicKey, &peerExtIP, &peerIntIP, &peerWgIP, &peerListenPort); scanErr != nil {
			// peer.NodeName is unset because Scan failed; log it as empty so
			// the field is still present and queryable.
			excludePeer(peer.NodeName, "scan_error", scanErr)
			continue
		}
		// Prefer external IP, fall back to internal IP.
		endpoint := ""
		if peerExtIP.Valid && peerExtIP.String != "" {
			endpoint = peerExtIP.String
		} else if peerIntIP.Valid && peerIntIP.String != "" {
			endpoint = peerIntIP.String
		}
		if endpoint == "" {
			excludePeer(peer.NodeName, "missing_endpoint", nil)
			continue
		}
		if !peerWgIP.Valid {
			// Defense-in-depth: the SQL filter guarantees wireguard_ip IS NOT
			// NULL. Reaching this branch means the schema or filter changed
			// without updating the read path.
			excludePeer(peer.NodeName, "missing_wireguard_ip", nil)
			continue
		}
		port := int32(51820)
		if peerListenPort.Valid && peerListenPort.Int32 > 0 {
			port = peerListenPort.Int32
		}
		peer.Endpoint = fmt.Sprintf("%s:%d", endpoint, port)
		peer.AllowedIps = []string{peerWgIP.String + "/32"}
		peer.KeepAlive = 25
		peers = append(peers, &peer)
	}
	if err := rows.Err(); err != nil {
		recordPhase("peer_query", phaseStarted)
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	recordPhase("peer_query", phaseStarted)
	peerCount = len(peers)

	return &quartermasterpb.InfrastructureSyncResponse{
		WireguardIp:      wireguardIP,
		WireguardPort:    wireguardPort,
		Peers:            peers,
		ServiceEndpoints: serviceEndpoints,
		MeshRevision:     computeMeshRevision(peers, serviceEndpoints, wireguardIP, wireguardPort),
	}, nil
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
// payload, not by which service wrote the row. Returns NotFound when the
// event is nil, InvalidArgument when event.tenant_id is empty.
func (s *QuartermasterServer) EnqueueServiceEvent(ctx context.Context, req *quartermasterpb.EnqueueServiceEventRequest) (*quartermasterpb.EnqueueServiceEventResponse, error) {
	event := req.GetEvent()
	if event == nil {
		return nil, status.Error(codes.InvalidArgument, "event is required")
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
	rows, err := s.db.QueryContext(ctx, `
			SELECT id, service_id, name, plane, description, default_port,
			       health_check_path, docker_image, version, dependencies,
			       tags, is_active, type, protocol, created_at, updated_at
			FROM quartermaster.services
			WHERE COALESCE(plane, '') <> 'infra'
			ORDER BY name
		`)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var services []*quartermasterpb.Service
	for rows.Next() {
		var svc quartermasterpb.Service
		var createdAt, updatedAt time.Time
		var serviceID, plane, description, healthCheckPath, dockerImage, version sql.NullString
		var serviceType, serviceProtocol sql.NullString
		var defaultPort sql.NullInt32
		var dependencies []string
		var tagsJSON []byte

		if err := rows.Scan(
			&svc.Id, &serviceID, &svc.Name, &plane, &description, &defaultPort,
			&healthCheckPath, &dockerImage, &version, pq.Array(&dependencies),
			&tagsJSON, &svc.IsActive, &serviceType, &serviceProtocol, &createdAt, &updatedAt,
		); err != nil {
			s.logger.WithError(err).Warn("Failed to scan service row")
			continue
		}

		if serviceID.Valid {
			svc.ServiceId = serviceID.String
		}
		if plane.Valid {
			svc.Plane = plane.String
		}
		if description.Valid {
			svc.Description = &description.String
		}
		if defaultPort.Valid {
			port := defaultPort.Int32
			svc.DefaultPort = &port
		}
		if healthCheckPath.Valid {
			svc.HealthCheckPath = &healthCheckPath.String
		}
		if dockerImage.Valid {
			svc.DockerImage = &dockerImage.String
		}
		if version.Valid {
			svc.Version = &version.String
		}
		if len(dependencies) > 0 {
			svc.Dependencies = dependencies
		}
		if len(tagsJSON) > 0 {
			// Parse tags as JSON into Struct
			var tagsMap map[string]any
			if err := json.Unmarshal(tagsJSON, &tagsMap); err == nil {
				svc.Tags = mapToStruct(tagsMap)
			}
		}
		if serviceType.Valid {
			svc.Type = serviceType.String
		}
		if serviceProtocol.Valid {
			svc.Protocol = serviceProtocol.String
		}

		svc.CreatedAt = timestamppb.New(createdAt)
		svc.UpdatedAt = timestamppb.New(updatedAt)
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

	rows, err := s.db.QueryContext(ctx, `
		SELECT cs.id, cs.cluster_id, cs.service_id, cs.desired_state, cs.desired_replicas,
		       cs.current_replicas, cs.config_blob, cs.environment_vars,
		       cs.cpu_limit, cs.memory_limit_mb, cs.health_status, cs.last_deployed,
		       cs.created_at, cs.updated_at,
		       s.name as service_name, s.plane as service_plane
		FROM quartermaster.cluster_services cs
		LEFT JOIN quartermaster.services s ON s.service_id = cs.service_id
		WHERE cs.cluster_id = $1
	`, clusterID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var services []*quartermasterpb.ClusterServiceAssignment
	for rows.Next() {
		var svc quartermasterpb.ClusterServiceAssignment
		var createdAt, updatedAt time.Time
		var configBlob, envVars sql.NullString
		var cpuLimit sql.NullFloat64
		var memoryLimitMb sql.NullInt32
		var healthStatus sql.NullString
		var lastDeployed sql.NullTime
		var serviceName, servicePlane sql.NullString

		if err := rows.Scan(
			&svc.Id, &svc.ClusterId, &svc.ServiceId, &svc.DesiredState, &svc.DesiredReplicas,
			&svc.CurrentReplicas, &configBlob, &envVars,
			&cpuLimit, &memoryLimitMb, &healthStatus, &lastDeployed,
			&createdAt, &updatedAt,
			&serviceName, &servicePlane,
		); err != nil {
			s.logger.WithError(err).Warn("Failed to scan cluster service row")
			continue
		}

		if configBlob.Valid && configBlob.String != "" {
			var configMap map[string]any
			if err := json.Unmarshal([]byte(configBlob.String), &configMap); err == nil {
				svc.ConfigBlob = mapToStruct(configMap)
			}
		}
		if envVars.Valid && envVars.String != "" {
			var envMap map[string]any
			if err := json.Unmarshal([]byte(envVars.String), &envMap); err == nil {
				svc.EnvironmentVars = mapToStruct(envMap)
			}
		}
		if cpuLimit.Valid {
			cpu := cpuLimit.Float64
			svc.CpuLimit = &cpu
		}
		if memoryLimitMb.Valid {
			mem := memoryLimitMb.Int32
			svc.MemoryLimitMb = &mem
		}
		if healthStatus.Valid {
			svc.HealthStatus = healthStatus.String
		}
		if lastDeployed.Valid {
			svc.LastDeployed = timestamppb.New(lastDeployed.Time)
		}
		if serviceName.Valid {
			svc.ServiceName = serviceName.String
		}
		if servicePlane.Valid {
			svc.ServicePlane = servicePlane.String
		}

		svc.CreatedAt = timestamppb.New(createdAt)
		svc.UpdatedAt = timestamppb.New(updatedAt)
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

	builder := &pagination.KeysetBuilder{
		TimestampColumn: "si.created_at",
		IDColumn:        "si.id",
	}

	// Build WHERE clause for filters
	where := "WHERE COALESCE(s.plane, '') <> 'infra'"
	countWhere := "WHERE COALESCE(s.plane, '') <> 'infra'"
	args := []any{}
	countArgs := []any{}
	argIdx := 1

	if req.GetClusterId() != "" {
		where += fmt.Sprintf(" AND si.cluster_id = $%d", argIdx)
		countWhere += fmt.Sprintf(" AND si.cluster_id = $%d", argIdx)
		args = append(args, req.GetClusterId())
		countArgs = append(countArgs, req.GetClusterId())
		argIdx++
	}
	if req.GetServiceId() != "" {
		where += fmt.Sprintf(" AND si.service_id = $%d", argIdx)
		countWhere += fmt.Sprintf(" AND si.service_id = $%d", argIdx)
		args = append(args, req.GetServiceId())
		countArgs = append(countArgs, req.GetServiceId())
		argIdx++
	}
	if req.GetNodeId() != "" {
		where += fmt.Sprintf(" AND si.node_id = $%d", argIdx)
		countWhere += fmt.Sprintf(" AND si.node_id = $%d", argIdx)
		args = append(args, req.GetNodeId())
		countArgs = append(countArgs, req.GetNodeId())
		argIdx++
	}

	// Get total count
	var total int32
	countQuery := fmt.Sprintf(`SELECT COUNT(*)
			FROM quartermaster.service_instances si
			JOIN quartermaster.services s ON s.service_id = si.service_id
			%s`, countWhere)
	if countErr := s.db.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total); countErr != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", countErr)
	}

	// Add keyset condition if cursor provided
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		where += " AND " + condition
		args = append(args, cursorArgs...)
	}

	query := fmt.Sprintf(`
			SELECT si.id, si.instance_id, si.service_id, si.cluster_id, si.node_id, si.protocol, si.advertise_host, si.port,
			       si.health_endpoint_override, si.version, si.process_id, si.container_id, si.status, si.health_status, COALESCE(si.metadata, '{}'::jsonb),
			       si.started_at, si.stopped_at, si.last_health_check, si.created_at, si.updated_at
			FROM quartermaster.service_instances si
			JOIN quartermaster.services s ON s.service_id = si.service_id
		%s
		%s
		LIMIT %d
	`, where, builder.OrderBy(params), params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var instances []*quartermasterpb.ServiceInstance
	for rows.Next() {
		var inst quartermasterpb.ServiceInstance
		var nodeID, host, healthEndpoint, version, containerID sql.NullString
		var processID sql.NullInt32
		var startedAt, stoppedAt, lastHealthCheck sql.NullTime
		var metadataJSON []byte
		var createdAt, updatedAt time.Time

		err := rows.Scan(&inst.Id, &inst.InstanceId, &inst.ServiceId, &inst.ClusterId, &nodeID,
			&inst.Protocol, &host, &inst.Port, &healthEndpoint, &version, &processID, &containerID,
			&inst.Status, &inst.HealthStatus, &metadataJSON, &startedAt, &stoppedAt, &lastHealthCheck, &createdAt, &updatedAt)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to scan service instance row")
			continue
		}

		if nodeID.Valid {
			inst.NodeId = &nodeID.String
		}
		if host.Valid {
			inst.Host = &host.String
		}
		if healthEndpoint.Valid {
			inst.HealthEndpoint = &healthEndpoint.String
		}
		if version.Valid {
			inst.Version = &version.String
		}
		if processID.Valid {
			inst.ProcessId = &processID.Int32
		}
		inst.Metadata = unmarshalStringMapJSON(metadataJSON)
		if containerID.Valid {
			inst.ContainerId = &containerID.String
		}
		if startedAt.Valid {
			inst.StartedAt = timestamppb.New(startedAt.Time)
		}
		if stoppedAt.Valid {
			inst.StoppedAt = timestamppb.New(stoppedAt.Time)
		}
		if lastHealthCheck.Valid {
			inst.LastHealthCheck = timestamppb.New(lastHealthCheck.Time)
		}
		inst.CreatedAt = timestamppb.New(createdAt)
		inst.UpdatedAt = timestamppb.New(updatedAt)

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

	args := []any{serviceType}
	argIdx := 2
	// Only healthy, operator-active instances are eligible for a public infra
	// A record: a starting/unhealthy gateway must never receive routable DNS.
	// Mirrors listHealthyServiceNodes' health gate.
	where := "WHERE s.type = $1 AND si.status IN ('running','active') AND si.health_status = 'healthy' AND si.node_id IS NOT NULL AND n.external_ip IS NOT NULL AND n.status = 'active'"

	if clusterID := strings.TrimSpace(req.GetClusterId()); clusterID != "" {
		where += fmt.Sprintf(" AND si.cluster_id = $%d", argIdx)
		args = append(args, clusterID)
		argIdx++
	}
	if stale := req.GetStaleThresholdSeconds(); stale > 0 {
		where += fmt.Sprintf(" AND si.last_health_check > NOW() - ($%d * INTERVAL '1 second')", argIdx)
		args = append(args, stale)
	}

	query := fmt.Sprintf(`
		SELECT si.instance_id, si.service_id, si.cluster_id, si.node_id,
		       host(n.external_ip), si.status, si.health_status, si.port, si.protocol
		FROM quartermaster.service_instances si
		JOIN quartermaster.services s ON s.service_id = si.service_id
		JOIN quartermaster.infrastructure_nodes n
			ON si.node_id = n.node_id AND si.cluster_id = n.cluster_id
		%s
		ORDER BY si.node_id
	`, where)

	rows, err := retryQueryContext(ctx, s.db, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var instances []*quartermasterpb.PhysicalServiceInstance
	for rows.Next() {
		var inst quartermasterpb.PhysicalServiceInstance
		var externalIP sql.NullString
		if err := rows.Scan(&inst.InstanceId, &inst.ServiceId, &inst.ClusterId, &inst.NodeId,
			&externalIP, &inst.Status, &inst.HealthStatus, &inst.Port, &inst.Protocol); err != nil {
			// Fail closed: a skipped row would shrink the physical inventory and
			// Navigator could prune a valid record (allowPrune=true).
			return nil, status.Errorf(codes.Internal, "physical instance scan error: %v", err)
		}
		inst.ExternalIp = externalIP.String
		if host, ok := dns.InfraInstanceFQDN(serviceType, inst.NodeId, s.platformRootDomain); ok {
			inst.PublicInstanceHost = host
		}
		instances = append(instances, &inst)
	}
	// A partial read (iteration aborted by a row error) would otherwise look
	// like a smaller-but-complete inventory, and Navigator would prune the
	// "missing" records. Fail closed so the caller suppresses pruning instead.
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "physical instance iteration error: %v", err)
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

	query := `
		INSERT INTO quartermaster.tls_bundles (bundle_id, cluster_id, domains, issuer, email, metadata, updated_at)
		VALUES ($1, $2, COALESCE($3, '[]')::jsonb, $4, $5, COALESCE($6, '{}')::jsonb, NOW())
		ON CONFLICT (bundle_id) DO UPDATE SET
			cluster_id = EXCLUDED.cluster_id,
			domains = EXCLUDED.domains,
			issuer = EXCLUDED.issuer,
			email = EXCLUDED.email,
			metadata = EXCLUDED.metadata,
			updated_at = NOW()
		RETURNING id, created_at, updated_at
	`

	var id string
	var createdAt, updatedAt time.Time
	if err := s.db.QueryRowContext(ctx, query,
		bundle.GetBundleId(), bundle.GetClusterId(), domainsJSON, bundle.GetIssuer(), bundle.GetEmail(), metadataJSON,
	).Scan(&id, &createdAt, &updatedAt); err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	return &quartermasterpb.TLSBundleResponse{
		Bundle: &quartermasterpb.TLSBundle{
			Id:        id,
			BundleId:  bundle.GetBundleId(),
			ClusterId: bundle.GetClusterId(),
			Domains:   domains,
			Issuer:    bundle.GetIssuer(),
			Email:     bundle.GetEmail(),
			Metadata:  bundle.GetMetadata(),
			CreatedAt: timestamppb.New(createdAt),
			UpdatedAt: timestamppb.New(updatedAt),
		},
	}, nil
}

func (s *QuartermasterServer) ListTLSBundles(ctx context.Context, req *quartermasterpb.ListTLSBundlesRequest) (*quartermasterpb.ListTLSBundlesResponse, error) {
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	builder := &pagination.KeysetBuilder{
		TimestampColumn: "created_at",
		IDColumn:        "id",
	}

	where := "WHERE 1=1"
	countWhere := "WHERE 1=1"
	args := []any{}
	countArgs := []any{}
	argIdx := 1

	if req.GetClusterId() != "" {
		where += fmt.Sprintf(" AND cluster_id = $%d", argIdx)
		countWhere += fmt.Sprintf(" AND cluster_id = $%d", argIdx)
		args = append(args, req.GetClusterId())
		countArgs = append(countArgs, req.GetClusterId())
		argIdx++
	}

	var total int32
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM quartermaster.tls_bundles %s`, countWhere)
	if retryErr := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.db.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total)
	}); retryErr != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", retryErr)
	}

	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		where += " AND " + condition
		args = append(args, cursorArgs...)
	}

	query := fmt.Sprintf(`
		SELECT id, bundle_id, cluster_id, domains, issuer, email, COALESCE(metadata, '{}'::jsonb), created_at, updated_at
		FROM quartermaster.tls_bundles
		%s
		%s
		LIMIT %d
	`, where, builder.OrderBy(params), params.Limit+1)

	rows, err := retryQueryContext(ctx, s.db, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var bundles []*quartermasterpb.TLSBundle
	for rows.Next() {
		var bundle quartermasterpb.TLSBundle
		var domainsJSON, metadataJSON []byte
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&bundle.Id, &bundle.BundleId, &bundle.ClusterId, &domainsJSON, &bundle.Issuer, &bundle.Email, &metadataJSON, &createdAt, &updatedAt); err != nil {
			return nil, status.Errorf(codes.Internal, "scan error: %v", err)
		}
		bundle.Domains = unmarshalStringSliceJSON(domainsJSON)
		if len(metadataJSON) > 0 {
			var metadataMap map[string]any
			if json.Unmarshal(metadataJSON, &metadataMap) == nil {
				bundle.Metadata = mapToStruct(metadataMap)
			}
		}
		bundle.CreatedAt = timestamppb.New(createdAt)
		bundle.UpdatedAt = timestamppb.New(updatedAt)
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

	query := `
		INSERT INTO quartermaster.ingress_sites (site_id, cluster_id, node_id, domains, tls_bundle_id, kind, upstream, metadata, updated_at)
		VALUES ($1, $2, $3, COALESCE($4, '[]')::jsonb, $5, $6, $7, COALESCE($8, '{}')::jsonb, NOW())
		ON CONFLICT (site_id) DO UPDATE SET
			cluster_id = EXCLUDED.cluster_id,
			node_id = EXCLUDED.node_id,
			domains = EXCLUDED.domains,
			tls_bundle_id = EXCLUDED.tls_bundle_id,
			kind = EXCLUDED.kind,
			upstream = EXCLUDED.upstream,
			metadata = EXCLUDED.metadata,
			updated_at = NOW()
		RETURNING id, created_at, updated_at
	`

	var id string
	var createdAt, updatedAt time.Time
	if err := s.db.QueryRowContext(ctx, query,
		site.GetSiteId(), site.GetClusterId(), site.GetNodeId(), domainsJSON, site.GetTlsBundleId(), site.GetKind(), site.GetUpstream(), metadataJSON,
	).Scan(&id, &createdAt, &updatedAt); err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	return &quartermasterpb.IngressSiteResponse{
		Site: &quartermasterpb.IngressSite{
			Id:          id,
			SiteId:      site.GetSiteId(),
			ClusterId:   site.GetClusterId(),
			NodeId:      site.GetNodeId(),
			Domains:     domains,
			TlsBundleId: site.GetTlsBundleId(),
			Kind:        site.GetKind(),
			Upstream:    site.GetUpstream(),
			Metadata:    site.GetMetadata(),
			CreatedAt:   timestamppb.New(createdAt),
			UpdatedAt:   timestamppb.New(updatedAt),
		},
	}, nil
}

func (s *QuartermasterServer) ListIngressSites(ctx context.Context, req *quartermasterpb.ListIngressSitesRequest) (*quartermasterpb.ListIngressSitesResponse, error) {
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	builder := &pagination.KeysetBuilder{
		TimestampColumn: "created_at",
		IDColumn:        "id",
	}

	where := "WHERE 1=1"
	countWhere := "WHERE 1=1"
	args := []any{}
	countArgs := []any{}
	argIdx := 1

	if req.GetClusterId() != "" {
		where += fmt.Sprintf(" AND cluster_id = $%d", argIdx)
		countWhere += fmt.Sprintf(" AND cluster_id = $%d", argIdx)
		args = append(args, req.GetClusterId())
		countArgs = append(countArgs, req.GetClusterId())
		argIdx++
	}
	if req.GetNodeId() != "" {
		where += fmt.Sprintf(" AND node_id = $%d", argIdx)
		countWhere += fmt.Sprintf(" AND node_id = $%d", argIdx)
		args = append(args, req.GetNodeId())
		countArgs = append(countArgs, req.GetNodeId())
		argIdx++
	}

	var total int32
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM quartermaster.ingress_sites %s`, countWhere)
	if retryErr := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.db.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total)
	}); retryErr != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", retryErr)
	}

	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		where += " AND " + condition
		args = append(args, cursorArgs...)
	}

	query := fmt.Sprintf(`
		SELECT id, site_id, cluster_id, node_id, domains, tls_bundle_id, kind, upstream, COALESCE(metadata, '{}'::jsonb), created_at, updated_at
		FROM quartermaster.ingress_sites
		%s
		%s
		LIMIT %d
	`, where, builder.OrderBy(params), params.Limit+1)

	rows, err := retryQueryContext(ctx, s.db, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var sites []*quartermasterpb.IngressSite
	for rows.Next() {
		var site quartermasterpb.IngressSite
		var domainsJSON, metadataJSON []byte
		var createdAt, updatedAt time.Time
		if err := rows.Scan(
			&site.Id, &site.SiteId, &site.ClusterId, &site.NodeId, &domainsJSON, &site.TlsBundleId,
			&site.Kind, &site.Upstream, &metadataJSON, &createdAt, &updatedAt,
		); err != nil {
			return nil, status.Errorf(codes.Internal, "scan error: %v", err)
		}
		// Fail closed on a malformed domains row: silently nil-ing it would make
		// Navigator's physical-endpoint gate read a provisioned site as having no
		// matching domain, and prune a valid infra A record.
		if len(domainsJSON) > 0 {
			domains, err := decodeIngressDomainsStrict(domainsJSON)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "decode ingress site domains: %v", err)
			}
			site.Domains = domains
		}
		if len(metadataJSON) > 0 {
			var metadataMap map[string]any
			if json.Unmarshal(metadataJSON, &metadataMap) == nil {
				site.Metadata = mapToStruct(metadataMap)
			}
		}
		site.CreatedAt = timestamppb.New(createdAt)
		site.UpdatedAt = timestamppb.New(updatedAt)
		sites = append(sites, &site)
	}
	// Fail closed on a partial read: Navigator's physical-endpoint gate must not
	// mistake a truncated ingress-site list for "this node has no physical site"
	// and prune a valid record.
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "ingress site iteration error: %v", err)
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
	where := "WHERE 1=1"
	args := []any{}
	if serviceID != "" {
		where = "WHERE service_id = $1"
		args = append(args, serviceID)
	}

	query := fmt.Sprintf(`
		SELECT instance_id, service_id, cluster_id, protocol, advertise_host, port, health_endpoint_override, health_status, last_health_check
		FROM quartermaster.service_instances
		%s
		ORDER BY service_id, instance_id
	`, where)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var instances []*quartermasterpb.ServiceInstanceHealth
	for rows.Next() {
		var inst quartermasterpb.ServiceInstanceHealth
		var host, healthEndpoint sql.NullString
		var lastHealthCheck sql.NullTime

		err := rows.Scan(&inst.InstanceId, &inst.ServiceId, &inst.ClusterId, &inst.Protocol,
			&host, &inst.Port, &healthEndpoint, &inst.Status, &lastHealthCheck)
		if err != nil {
			continue
		}

		if host.Valid {
			inst.Host = &host.String
		}
		if healthEndpoint.Valid {
			inst.HealthEndpoint = &healthEndpoint.String
		}
		if lastHealthCheck.Valid {
			inst.LastHealthCheck = timestamppb.New(lastHealthCheck.Time)
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

func scanTenant(rows *sql.Rows) (*quartermasterpb.Tenant, error) {
	var tenant quartermasterpb.Tenant
	var subdomain, customDomain, logoURL, primaryClusterID, officialClusterID, kafkaTopicPrefix, databaseURL sql.NullString
	var kafkaBrokers []string
	var createdAt, updatedAt time.Time

	err := rows.Scan(
		&tenant.Id, &tenant.Name, &subdomain, &customDomain, &logoURL,
		&tenant.PrimaryColor, &tenant.SecondaryColor, &tenant.DeploymentTier,
		&tenant.DeploymentModel,
		&primaryClusterID, &officialClusterID, &kafkaTopicPrefix, pq.Array(&kafkaBrokers), &databaseURL,
		&tenant.IsActive, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}

	if subdomain.Valid {
		tenant.Subdomain = &subdomain.String
	}
	if customDomain.Valid {
		tenant.CustomDomain = &customDomain.String
	}
	if logoURL.Valid {
		tenant.LogoUrl = &logoURL.String
	}
	if primaryClusterID.Valid {
		tenant.PrimaryClusterId = &primaryClusterID.String
	}
	if officialClusterID.Valid {
		tenant.OfficialClusterId = &officialClusterID.String
	}
	if kafkaTopicPrefix.Valid {
		tenant.KafkaTopicPrefix = &kafkaTopicPrefix.String
	}
	if databaseURL.Valid {
		tenant.DatabaseUrl = &databaseURL.String
	}
	tenant.KafkaBrokers = kafkaBrokers
	tenant.CreatedAt = timestamppb.New(createdAt)
	tenant.UpdatedAt = timestamppb.New(updatedAt)

	return &tenant, nil
}

func scanCluster(rows *sql.Rows) (*quartermasterpb.InfrastructureCluster, error) {
	var cluster quartermasterpb.InfrastructureCluster
	var ownerTenantID, databaseURL, periscopeURL sql.NullString
	var kafkaBrokers []string
	var createdAt, updatedAt time.Time

	err := rows.Scan(
		&cluster.Id, &cluster.ClusterId, &cluster.ClusterName, &cluster.ClusterType,
		&ownerTenantID, &cluster.DeploymentModel, &cluster.BaseUrl, &databaseURL, &periscopeURL,
		pq.Array(&kafkaBrokers), &cluster.MaxConcurrentStreams, &cluster.MaxConcurrentViewers,
		&cluster.MaxBandwidthMbps, &cluster.HealthStatus, &cluster.IsActive, &cluster.IsDefaultCluster,
		&cluster.IsPlatformOfficial, &cluster.PublicTopology, &cluster.AllowPrivatePullSources, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}

	if ownerTenantID.Valid {
		cluster.OwnerTenantId = &ownerTenantID.String
	}
	if databaseURL.Valid {
		cluster.DatabaseUrl = &databaseURL.String
	}
	if periscopeURL.Valid {
		cluster.PeriscopeUrl = &periscopeURL.String
	}
	cluster.KafkaBrokers = kafkaBrokers
	cluster.CreatedAt = timestamppb.New(createdAt)
	cluster.UpdatedAt = timestamppb.New(updatedAt)

	return &cluster, nil
}

// prefixedNodeSnapshotColumns is the SELECT-list suffix for the latest resource
// snapshot Privateer reports via SyncMesh. Append this after the existing
// "...status, created_at, updated_at" tail of any SELECT that feeds scanNode /
// queryNode so both the scan order and column count stay in sync.
const prefixedNodeSnapshotColumns = ", n.snapshot_cpu_percent, n.snapshot_ram_used_bytes, n.snapshot_ram_total_bytes, n.snapshot_disk_used_bytes, n.snapshot_disk_total_bytes, n.snapshot_uptime_seconds, n.snapshot_at"
const prefixedNodeOwnerColumn = ", (SELECT c.owner_tenant_id::text FROM quartermaster.infrastructure_clusters c WHERE c.cluster_id = n.cluster_id)"
const prefixedAssignedNodeOwnerColumn = ", (SELECT c.owner_tenant_id::text FROM quartermaster.infrastructure_clusters c WHERE c.cluster_id = sca.cluster_id)"
const prefixedTargetNodeOwnerColumn = ", (SELECT c.owner_tenant_id::text FROM quartermaster.infrastructure_clusters c WHERE c.cluster_id = target.cluster_id)"

func resourceSnapshotComplete(snap *quartermasterpb.NodeResourceSnapshot) bool {
	return snap != nil &&
		snap.GetRamTotalBytes() > 0 &&
		snap.GetDiskTotalBytes() > 0 &&
		snap.GetUptimeSeconds() > 0
}

// nodeSnapshotProto builds a NodeResourceSnapshot from the seven nullable
// columns appended by nodeSnapshotColumns. Returns nil when no agent has
// ever reported a snapshot for the row (snapshot_at IS NULL).
func nodeSnapshotProto(cpu sql.NullFloat64, ramUsed, ramTotal, diskUsed, diskTotal, uptime sql.NullInt64, at sql.NullTime) *quartermasterpb.NodeResourceSnapshot {
	if !at.Valid {
		return nil
	}
	snap := &quartermasterpb.NodeResourceSnapshot{CollectedAt: timestamppb.New(at.Time)}
	if cpu.Valid {
		snap.CpuPercent = float32(cpu.Float64)
	}
	if ramUsed.Valid {
		snap.RamUsedBytes = uint64(ramUsed.Int64)
	}
	if ramTotal.Valid {
		snap.RamTotalBytes = uint64(ramTotal.Int64)
	}
	if diskUsed.Valid {
		snap.DiskUsedBytes = uint64(diskUsed.Int64)
	}
	if diskTotal.Valid {
		snap.DiskTotalBytes = uint64(diskTotal.Int64)
	}
	if uptime.Valid {
		snap.UptimeSeconds = uint64(uptime.Int64)
	}
	return snap
}

func scanNode(rows *sql.Rows) (*quartermasterpb.InfrastructureNode, error) {
	var node quartermasterpb.InfrastructureNode
	var internalIP, externalIP, wireguardIP, wireguardPubKey, region, az, appliedRev, ownerTenantID sql.NullString
	var wgPort, cpuCores, memoryGB, diskGB sql.NullInt32
	var lat, lon sql.NullFloat64
	var lastHeartbeat sql.NullTime
	var createdAt, updatedAt time.Time
	var enrollmentOrigin, nodeStatus string
	var snapCPU sql.NullFloat64
	var snapRamUsed, snapRamTotal, snapDiskUsed, snapDiskTotal, snapUptime sql.NullInt64
	var snapAt sql.NullTime

	err := rows.Scan(
		&node.Id, &node.NodeId, &node.ClusterId, &node.NodeName, &node.NodeType,
		&internalIP, &externalIP, &wireguardIP, &wireguardPubKey, &wgPort, &region, &az,
		&lat, &lon,
		&cpuCores, &memoryGB, &diskGB,
		&lastHeartbeat, &enrollmentOrigin, &appliedRev, &nodeStatus, &createdAt, &updatedAt,
		&ownerTenantID,
		&snapCPU, &snapRamUsed, &snapRamTotal, &snapDiskUsed, &snapDiskTotal, &snapUptime, &snapAt,
	)
	if err != nil {
		return nil, err
	}
	node.ResourceSnapshot = nodeSnapshotProto(snapCPU, snapRamUsed, snapRamTotal, snapDiskUsed, snapDiskTotal, snapUptime, snapAt)
	node.EnrollmentOrigin = enrollmentOrigin
	node.Status = nodeStatus
	if appliedRev.Valid {
		node.AppliedMeshRevision = &appliedRev.String
	}
	if ownerTenantID.Valid {
		node.OwnerTenantId = &ownerTenantID.String
	}
	if wgPort.Valid {
		node.WireguardPort = &wgPort.Int32
	}

	if internalIP.Valid {
		node.InternalIp = &internalIP.String
	}
	if externalIP.Valid {
		node.ExternalIp = &externalIP.String
	}
	if wireguardIP.Valid {
		node.WireguardIp = &wireguardIP.String
	}
	if wireguardPubKey.Valid {
		node.WireguardPublicKey = &wireguardPubKey.String
	}
	if region.Valid {
		node.Region = &region.String
	}
	if az.Valid {
		node.AvailabilityZone = &az.String
	}
	if lat.Valid {
		node.Latitude = &lat.Float64
	}
	if lon.Valid {
		node.Longitude = &lon.Float64
	}
	if cpuCores.Valid {
		node.CpuCores = &cpuCores.Int32
	}
	if memoryGB.Valid {
		node.MemoryGb = &memoryGB.Int32
	}
	if diskGB.Valid {
		node.DiskGb = &diskGB.Int32
	}
	if lastHeartbeat.Valid {
		node.LastHeartbeat = timestamppb.New(lastHeartbeat.Time)
	}
	node.CreatedAt = timestamppb.New(createdAt)
	node.UpdatedAt = timestamppb.New(updatedAt)

	return &node, nil
}

func (s *QuartermasterServer) queryCluster(ctx context.Context, clusterID string) (*quartermasterpb.InfrastructureCluster, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, cluster_id, cluster_name, cluster_type, owner_tenant_id, deployment_model,
		       base_url, database_url, periscope_url, kafka_brokers,
		       max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps,
		       health_status, is_active, is_default_cluster, is_platform_official, public_topology, created_at, updated_at,
		       visibility, requires_approval, short_description,
		       COALESCE(s3_bucket, ''), COALESCE(s3_endpoint, ''), COALESCE(s3_region, ''),
		       COALESCE(region_id, ''), COALESCE(cell_id, ''), COALESCE(cluster_class, ''),
		       COALESCE(control_cell_id, ''), COALESCE(eligible_serving_cell_ids, ARRAY[]::TEXT[])
		FROM quartermaster.infrastructure_clusters
		WHERE cluster_id = $1
	`, clusterID)

	var cluster quartermasterpb.InfrastructureCluster
	var ownerTenantID, databaseURL, periscopeURL sql.NullString
	var kafkaBrokers []string
	var createdAt, updatedAt time.Time
	var visibility string
	var shortDescription sql.NullString
	var requiresApproval bool
	var eligibleCells []string

	err := row.Scan(
		&cluster.Id, &cluster.ClusterId, &cluster.ClusterName, &cluster.ClusterType,
		&ownerTenantID, &cluster.DeploymentModel, &cluster.BaseUrl, &databaseURL, &periscopeURL,
		pq.Array(&kafkaBrokers), &cluster.MaxConcurrentStreams, &cluster.MaxConcurrentViewers,
		&cluster.MaxBandwidthMbps, &cluster.HealthStatus, &cluster.IsActive, &cluster.IsDefaultCluster,
		&cluster.IsPlatformOfficial, &cluster.PublicTopology, &createdAt, &updatedAt,
		&visibility, &requiresApproval, &shortDescription,
		&cluster.S3Bucket, &cluster.S3Endpoint, &cluster.S3Region,
		&cluster.RegionId, &cluster.CellId, &cluster.ClusterClass,
		&cluster.ControlCellId, pq.Array(&eligibleCells),
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "cluster not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	if ownerTenantID.Valid {
		cluster.OwnerTenantId = &ownerTenantID.String
	}
	if databaseURL.Valid {
		cluster.DatabaseUrl = &databaseURL.String
	}
	if periscopeURL.Valid {
		cluster.PeriscopeUrl = &periscopeURL.String
	}
	cluster.KafkaBrokers = kafkaBrokers
	cluster.CreatedAt = timestamppb.New(createdAt)
	cluster.UpdatedAt = timestamppb.New(updatedAt)

	// Set marketplace fields (pricing now in Purser)
	cluster.Visibility = visibilityStringToProto(visibility)
	cluster.RequiresApproval = requiresApproval
	if shortDescription.Valid {
		cluster.ShortDescription = &shortDescription.String
	}
	cluster.EligibleServingCellIds = eligibleCells

	return &cluster, nil
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

func (s *QuartermasterServer) queryNode(ctx context.Context, nodeID string) (*quartermasterpb.InfrastructureNode, error) {
	row := s.db.QueryRowContext(ctx, `
			SELECT n.id, n.node_id, n.cluster_id, n.node_name, n.node_type, n.internal_ip, n.external_ip,
			       n.wireguard_ip, n.wireguard_public_key, n.wireguard_listen_port, n.region, n.availability_zone,
			       n.latitude, n.longitude,
			       n.cpu_cores, n.memory_gb, n.disk_gb,
			       n.last_heartbeat, n.enrollment_origin, n.applied_mesh_revision, n.status, n.created_at, n.updated_at,
			       (SELECT c.owner_tenant_id::text FROM quartermaster.infrastructure_clusters c WHERE c.cluster_id = n.cluster_id),
			       n.snapshot_cpu_percent, n.snapshot_ram_used_bytes, n.snapshot_ram_total_bytes,
			       n.snapshot_disk_used_bytes, n.snapshot_disk_total_bytes, n.snapshot_uptime_seconds, n.snapshot_at
			FROM quartermaster.infrastructure_nodes n
			WHERE n.node_id = $1 OR n.id::text = $1
		`, nodeID)

	var node quartermasterpb.InfrastructureNode
	var internalIP, externalIP, wireguardIP, wireguardPubKey, region, az, appliedRev, ownerTenantID sql.NullString
	var wgPort, cpuCores, memoryGB, diskGB sql.NullInt32
	var lat, lon sql.NullFloat64
	var lastHeartbeat sql.NullTime
	var createdAt, updatedAt time.Time
	var enrollmentOrigin, nodeStatus string
	var snapCPU sql.NullFloat64
	var snapRamUsed, snapRamTotal, snapDiskUsed, snapDiskTotal, snapUptime sql.NullInt64
	var snapAt sql.NullTime

	err := row.Scan(
		&node.Id, &node.NodeId, &node.ClusterId, &node.NodeName, &node.NodeType,
		&internalIP, &externalIP, &wireguardIP, &wireguardPubKey, &wgPort, &region, &az,
		&lat, &lon,
		&cpuCores, &memoryGB, &diskGB,
		&lastHeartbeat, &enrollmentOrigin, &appliedRev, &nodeStatus, &createdAt, &updatedAt,
		&ownerTenantID,
		&snapCPU, &snapRamUsed, &snapRamTotal, &snapDiskUsed, &snapDiskTotal, &snapUptime, &snapAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "node not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	if internalIP.Valid {
		node.InternalIp = &internalIP.String
	}
	if externalIP.Valid {
		node.ExternalIp = &externalIP.String
	}
	if wireguardIP.Valid {
		node.WireguardIp = &wireguardIP.String
	}
	if wireguardPubKey.Valid {
		node.WireguardPublicKey = &wireguardPubKey.String
	}
	if wgPort.Valid {
		node.WireguardPort = &wgPort.Int32
	}
	if region.Valid {
		node.Region = &region.String
	}
	if az.Valid {
		node.AvailabilityZone = &az.String
	}
	if lat.Valid {
		node.Latitude = &lat.Float64
	}
	if lon.Valid {
		node.Longitude = &lon.Float64
	}
	if cpuCores.Valid {
		node.CpuCores = &cpuCores.Int32
	}
	if memoryGB.Valid {
		node.MemoryGb = &memoryGB.Int32
	}
	if diskGB.Valid {
		node.DiskGb = &diskGB.Int32
	}
	if lastHeartbeat.Valid {
		node.LastHeartbeat = timestamppb.New(lastHeartbeat.Time)
	}
	node.EnrollmentOrigin = enrollmentOrigin
	node.Status = nodeStatus
	if appliedRev.Valid {
		node.AppliedMeshRevision = &appliedRev.String
	}
	if ownerTenantID.Valid {
		node.OwnerTenantId = &ownerTenantID.String
	}
	node.CreatedAt = timestamppb.New(createdAt)
	node.UpdatedAt = timestamppb.New(updatedAt)
	node.ResourceSnapshot = nodeSnapshotProto(snapCPU, snapRamUsed, snapRamTotal, snapDiskUsed, snapDiskTotal, snapUptime, snapAt)

	return &node, nil
}

// loadTakenMeshIPs returns the set of wireguard_ip values currently allocated
// within a cluster, keyed by dotted-quad string. Used by BootstrapInfrastructureNode
// to avoid colliding with already-assigned mesh addresses when allocating
// a new one for an enrolling node.
func loadTakenMeshIPs(ctx context.Context, tx *sql.Tx, clusterID string) (map[string]struct{}, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT host(wireguard_ip)
		FROM quartermaster.infrastructure_nodes
		WHERE cluster_id = $1 AND wireguard_ip IS NOT NULL
	`, clusterID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	taken := map[string]struct{}{}
	for rows.Next() {
		var ip string
		if scanErr := rows.Scan(&ip); scanErr != nil {
			return nil, scanErr
		}
		taken[ip] = struct{}{}
	}
	return taken, rows.Err()
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
	publicOnly := tenantID == ""

	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	builder := &pagination.KeysetBuilder{
		TimestampColumn: "c.created_at",
		IDColumn:        "c.cluster_id",
	}

	// Build base WHERE with visibility filtering
	var baseWhere string
	var args []any
	argIdx := 1
	if publicOnly {
		baseWhere = `
			WHERE c.is_active = true
			  AND c.visibility = 'public'
		`
	} else {
		baseWhere = `
			WHERE c.is_active = true
			  AND (
			      c.visibility = 'public'
			      OR c.owner_tenant_id = $1
			      OR ((c.visibility = 'unlisted' OR c.visibility = 'private') AND a.id IS NOT NULL AND a.is_active = true)
			  )
		`
		args = append(args, tenantID)
		argIdx = 2
	}

	// Get total count
	var total int32
	var countQuery string
	if publicOnly {
		countQuery = fmt.Sprintf(`
			SELECT COUNT(*)
			FROM quartermaster.infrastructure_clusters c
			%s
		`, baseWhere)
	} else {
		countQuery = fmt.Sprintf(`
			SELECT COUNT(*)
			FROM quartermaster.infrastructure_clusters c
			LEFT JOIN quartermaster.tenant_cluster_access a ON c.cluster_id = a.cluster_id AND a.tenant_id = $1
			%s
		`, baseWhere)
	}
	if countErr := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); countErr != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", countErr)
	}

	// Add keyset condition
	where := baseWhere
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		where += " AND " + condition
		args = append(args, cursorArgs...)
	}

	var query string
	if publicOnly {
		query = fmt.Sprintf(`
			SELECT c.cluster_id, c.cluster_name, c.short_description, c.visibility, c.requires_approval,
			       c.max_concurrent_streams, c.max_concurrent_viewers,
			       t.name as owner_name,
			       '' as subscription_status,
			       false as is_subscribed,
			       c.created_at
			FROM quartermaster.infrastructure_clusters c
			LEFT JOIN quartermaster.tenants t ON c.owner_tenant_id = t.id
			%s
			%s
			LIMIT $%d
		`, where, builder.OrderBy(params), len(args)+1)
	} else {
		query = fmt.Sprintf(`
			SELECT c.cluster_id, c.cluster_name, c.short_description, c.visibility, c.requires_approval,
			       c.max_concurrent_streams, c.max_concurrent_viewers,
			       t.name as owner_name,
			       COALESCE(a.subscription_status, '') as subscription_status,
			       CASE WHEN a.id IS NOT NULL AND a.is_active THEN true ELSE false END as is_subscribed,
			       c.created_at
			FROM quartermaster.infrastructure_clusters c
			LEFT JOIN quartermaster.tenants t ON c.owner_tenant_id = t.id
			LEFT JOIN quartermaster.tenant_cluster_access a ON c.cluster_id = a.cluster_id AND a.tenant_id = $1
			%s
			%s
			LIMIT $%d
		`, where, builder.OrderBy(params), len(args)+1)
	}

	args = append(args, params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	type entryWithCursor struct {
		entry     *quartermasterpb.MarketplaceClusterEntry
		createdAt time.Time
		clusterID string
	}
	var entries []entryWithCursor
	for rows.Next() {
		var entry quartermasterpb.MarketplaceClusterEntry
		var visibility string
		var ownerName, shortDesc, subscriptionStatus sql.NullString
		var createdAt time.Time

		if err := rows.Scan(
			&entry.ClusterId, &entry.ClusterName, &shortDesc, &visibility, &entry.RequiresApproval,
			&entry.MaxConcurrentStreams, &entry.MaxConcurrentViewers,
			&ownerName, &subscriptionStatus, &entry.IsSubscribed,
			&createdAt,
		); err != nil {
			continue
		}

		entry.Visibility = visibilityStringToProto(visibility)
		if shortDesc.Valid {
			entry.ShortDescription = &shortDesc.String
		}
		if ownerName.Valid {
			entry.OwnerName = &ownerName.String
		}
		if subscriptionStatus.Valid && subscriptionStatus.String != "" {
			entry.SubscriptionStatus = subscriptionStatusStringToProto(subscriptionStatus.String)
		}

		entry.CreatedAt = timestamppb.New(createdAt)

		entries = append(entries, entryWithCursor{entry: &entry, createdAt: createdAt, clusterID: entry.ClusterId})
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
	publicOnly := tenantID == ""

	// Note: Pricing fields are fetched from Purser, not Quartermaster
	var row *sql.Row
	if publicOnly {
		row = s.db.QueryRowContext(ctx, `
			SELECT c.cluster_id, c.cluster_name, c.short_description, c.visibility, c.requires_approval,
			       c.max_concurrent_streams, c.max_concurrent_viewers,
			       t.name as owner_name,
			       '' as subscription_status,
			       false as is_subscribed,
			       c.created_at
			FROM quartermaster.infrastructure_clusters c
			LEFT JOIN quartermaster.tenants t ON c.owner_tenant_id = t.id
			WHERE c.cluster_id = $1 AND c.is_active = true AND c.visibility = 'public'
		`, clusterID)
	} else {
		row = s.db.QueryRowContext(ctx, `
			SELECT c.cluster_id, c.cluster_name, c.short_description, c.visibility, c.requires_approval,
			       c.max_concurrent_streams, c.max_concurrent_viewers,
			       t.name as owner_name,
			       COALESCE(a.subscription_status, '') as subscription_status,
			       CASE WHEN a.id IS NOT NULL AND a.is_active THEN true ELSE false END as is_subscribed,
			       c.created_at
			FROM quartermaster.infrastructure_clusters c
			LEFT JOIN quartermaster.tenants t ON c.owner_tenant_id = t.id
			LEFT JOIN quartermaster.tenant_cluster_access a ON c.cluster_id = a.cluster_id AND a.tenant_id = $2
			WHERE c.cluster_id = $1 AND c.is_active = true
			  AND (
			      c.visibility = 'public'
			      OR c.owner_tenant_id = $2
			      OR ((c.visibility = 'unlisted' OR c.visibility = 'private') AND a.id IS NOT NULL AND a.is_active = true)
			  )
		`, clusterID, tenantID)
	}

	var entry quartermasterpb.MarketplaceClusterEntry
	var visibility string
	var ownerName, shortDesc, subscriptionStatus sql.NullString
	var createdAt time.Time

	err := row.Scan(
		&entry.ClusterId, &entry.ClusterName, &shortDesc, &visibility, &entry.RequiresApproval,
		&entry.MaxConcurrentStreams, &entry.MaxConcurrentViewers,
		&ownerName, &subscriptionStatus, &entry.IsSubscribed,
		&createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "cluster not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	entry.Visibility = visibilityStringToProto(visibility)
	if shortDesc.Valid {
		entry.ShortDescription = &shortDesc.String
	}
	if ownerName.Valid {
		entry.OwnerName = &ownerName.String
	}
	if subscriptionStatus.Valid && subscriptionStatus.String != "" {
		entry.SubscriptionStatus = subscriptionStatusStringToProto(subscriptionStatus.String)
	}
	entry.CreatedAt = timestamppb.New(createdAt)

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
	var ownerTenantID sql.NullString
	var isProvider bool
	err := s.db.QueryRowContext(ctx, `
		SELECT c.owner_tenant_id, COALESCE(t.is_provider, false) as is_provider
		FROM quartermaster.infrastructure_clusters c
		LEFT JOIN quartermaster.tenants t ON t.id = $2
		WHERE c.cluster_id = $1
	`, clusterID, tenantID).Scan(&ownerTenantID, &isProvider)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "cluster not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Only owner can update marketplace settings (unless admin/provider with platform cluster)
	if !ownerTenantID.Valid || ownerTenantID.String != tenantID {
		return nil, status.Error(codes.PermissionDenied, "only cluster owner can update marketplace settings")
	}

	// Build update query
	var updates []string
	var args []any
	argIdx := 1

	if req.Visibility != nil {
		// Non-providers can only set private visibility
		if !isProvider && *req.Visibility != quartermasterpb.ClusterVisibility_CLUSTER_VISIBILITY_PRIVATE {
			return nil, status.Error(codes.PermissionDenied, "only providers can set public/unlisted visibility")
		}
		newVisibility := visibilityProtoToString(*req.Visibility)
		updates = append(updates, fmt.Sprintf("visibility = $%d", argIdx))
		args = append(args, newVisibility)
		argIdx++
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
			updates = append(updates, fmt.Sprintf(
				"cluster_class = CASE WHEN cluster_class = 'platform_official' THEN cluster_class ELSE $%d END",
				argIdx))
			args = append(args, newClass)
			argIdx++
		}
	}
	// Note: Pricing fields are managed via Purser, not Quartermaster
	if req.RequiresApproval != nil {
		updates = append(updates, fmt.Sprintf("requires_approval = $%d", argIdx))
		args = append(args, *req.RequiresApproval)
		argIdx++
	}
	if req.ShortDescription != nil {
		updates = append(updates, fmt.Sprintf("short_description = NULLIF($%d, '')", argIdx))
		args = append(args, *req.ShortDescription)
		argIdx++
	}

	if len(updates) == 0 {
		return nil, status.Error(codes.InvalidArgument, "no fields to update")
	}

	updates = append(updates, "updated_at = NOW()")
	query := fmt.Sprintf("UPDATE quartermaster.infrastructure_clusters SET %s WHERE cluster_id = $%d",
		strings.Join(updates, ", "), argIdx)
	args = append(args, clusterID)

	// Marketplace UPDATE + cluster_updated outbox emit ride one tx so the
	// dashboard view and downstream consumers can't see a divergent state
	// after a crash between mutation and emit.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err = tx.ExecContext(ctx, query, args...); err != nil {
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

	query := `
		SELECT c.cluster_id, c.cluster_name, c.short_description, c.visibility,
		       c.requires_approval, t.name AS owner_name,
		       c.max_concurrent_streams, c.max_concurrent_viewers,
		       COALESCE(a.id IS NOT NULL, false) AS is_subscribed,
		       COALESCE(a.subscription_status, 'none') AS subscription_status,
		       c.is_platform_official
		FROM quartermaster.infrastructure_clusters c
		LEFT JOIN quartermaster.tenants t ON c.owner_tenant_id = t.id
		LEFT JOIN quartermaster.tenant_cluster_access a
		    ON c.cluster_id = a.cluster_id AND a.tenant_id = $1
		WHERE c.cluster_id = ANY($2) AND c.is_active = true`

	rows, err := s.db.QueryContext(ctx, query, requestingTenantID, pq.Array(clusterIDs))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	clusters := make(map[string]*quartermasterpb.ClusterMetadata)
	for rows.Next() {
		var meta quartermasterpb.ClusterMetadata
		var shortDescription, ownerName sql.NullString
		var visibility, subscriptionStatus string
		var maxStreams, maxViewers int32
		var isSubscribed bool

		if err := rows.Scan(
			&meta.ClusterId, &meta.ClusterName, &shortDescription, &visibility,
			&meta.RequiresApproval, &ownerName,
			&maxStreams, &maxViewers,
			&isSubscribed, &subscriptionStatus,
			&meta.IsPlatformOfficial,
		); err != nil {
			s.logger.WithError(err).Warn("Failed to scan cluster metadata row")
			continue
		}

		if shortDescription.Valid {
			meta.ShortDescription = &shortDescription.String
		}
		meta.Visibility = visibility
		if ownerName.Valid {
			meta.OwnerName = &ownerName.String
		}
		meta.MaxConcurrentStreams = maxStreams
		meta.MaxConcurrentViewers = maxViewers
		meta.IsSubscribed = isSubscribed
		meta.SubscriptionStatus = subscriptionStatus

		clusters[meta.ClusterId] = &meta
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
	var maxOwnedClusters, currentOwnedClusters int
	var isProvider bool
	err := s.db.QueryRowContext(ctx, `
		SELECT max_owned_clusters, is_provider,
		       (SELECT COUNT(*) FROM quartermaster.infrastructure_clusters WHERE owner_tenant_id = $1)
		FROM quartermaster.tenants WHERE id = $1
	`, tenantID).Scan(&maxOwnedClusters, &isProvider, &currentOwnedClusters)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "tenant not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Non-providers are limited to max_owned_clusters (default 1)
	if !isProvider && currentOwnedClusters >= maxOwnedClusters {
		return nil, status.Errorf(codes.ResourceExhausted, "tenant has reached maximum owned clusters limit (%d)", maxOwnedClusters)
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

	if _, err = tx.ExecContext(ctx, `
		INSERT INTO quartermaster.infrastructure_clusters (
			id, cluster_id, cluster_name, cluster_type, deployment_model,
			owner_tenant_id, base_url,
			max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps,
			visibility, pricing_model, short_description,
			region_id, cell_id, cluster_class, control_cell_id, eligible_serving_cell_ids,
			health_status, is_active, created_at, updated_at
		) VALUES (
			$1, $2, $3, 'edge', 'self-hosted',
			$4, $9,
			0, 0, 0,
			'private', 'free_unmetered', $5,
			NULLIF($8::text, ''), $2, 'tenant_private', $7::text, ARRAY[$7::text]::TEXT[],
			'unknown', true, $6, $6
		)
	`, id, clusterID, clusterName, tenantID, req.ShortDescription, now, controlCell.controlCellID, regionForRow, strings.TrimSpace(controlCell.baseURL)); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create cluster: %v", err)
	}

	if _, err = tx.ExecContext(ctx, `
		INSERT INTO quartermaster.tenant_cluster_access (
			tenant_id, cluster_id, access_level, subscription_status, is_active, created_at, updated_at
		) VALUES ($1, $2, 'owner', 'active', true, NOW(), NOW())
	`, tenantID, clusterID); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to auto-subscribe owner to cluster: %v", err)
	}

	// Junction row binding the chosen Foghorn to this private cluster.
	// Without it, ConfigSeed delivery has no service_instance to dial.
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO quartermaster.service_cluster_assignments (service_instance_id, cluster_id)
		VALUES ($1::uuid, $2)
		ON CONFLICT (service_instance_id, cluster_id) DO UPDATE SET is_active = true, updated_at = NOW()
	`, controlCell.instanceID, clusterID); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to assign Foghorn to cluster: %v", err)
	}

	// Create a bootstrap token for edge node registration
	tokenID := uuid.New().String()
	token, err := generateSecureToken(32)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate token: %v", err)
	}
	expiresAt := now.Add(30 * 24 * time.Hour) // 30 days

	if _, err = tx.ExecContext(ctx, `
		INSERT INTO quartermaster.bootstrap_tokens (
			id, token_hash, token_prefix, kind, name, tenant_id, cluster_id, expires_at, created_by, created_at
		) VALUES ($1, $2, $3, 'edge_node', $4, $5, $6, $7, $5, NOW())
	`, tokenID, hashBootstrapToken(token), tokenPrefix(token), fmt.Sprintf("Bootstrap token for %s", clusterName), tenantID, clusterID, expiresAt); err != nil {
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
	var dbOwnerID sql.NullString
	var clusterName string
	err := s.db.QueryRowContext(ctx,
		"SELECT owner_tenant_id, cluster_name FROM quartermaster.infrastructure_clusters WHERE cluster_id = $1",
		clusterID).Scan(&dbOwnerID, &clusterName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "cluster not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !dbOwnerID.Valid || dbOwnerID.String != ownerTenantID {
		return nil, status.Error(codes.PermissionDenied, "only cluster owner can create invites")
	}

	// Verify invited tenant exists
	var invitedTenantName string
	err = s.db.QueryRowContext(ctx,
		"SELECT name FROM quartermaster.tenants WHERE id = $1",
		invitedTenantID).Scan(&invitedTenantName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "invited tenant not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Check for existing invite
	var existingID string
	err = s.db.QueryRowContext(ctx, `
		SELECT id FROM quartermaster.cluster_invites
		WHERE cluster_id = $1 AND invited_tenant_id = $2 AND status = 'pending'
	`, clusterID, invitedTenantID).Scan(&existingID)
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

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO quartermaster.cluster_invites (
			id, cluster_id, invited_tenant_id, invite_token, access_level,
			resource_limits, status, created_by, created_at, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, 'pending', $7, $8, $9)
	`, id, clusterID, invitedTenantID, token, accessLevel,
		resourceLimitsJSON, ownerTenantID, now, expiresAt)
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
		ClusterName:       &clusterName,
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
	var clusterID string
	var dbOwnerID sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT i.cluster_id, c.owner_tenant_id
		FROM quartermaster.cluster_invites i
		JOIN quartermaster.infrastructure_clusters c ON i.cluster_id = c.cluster_id
		WHERE i.id = $1
	`, inviteID).Scan(&clusterID, &dbOwnerID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "invite not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !dbOwnerID.Valid || dbOwnerID.String != ownerTenantID {
		return nil, status.Error(codes.PermissionDenied, "only cluster owner can revoke invites")
	}

	_, err = s.db.ExecContext(ctx, `
		UPDATE quartermaster.cluster_invites SET status = 'revoked' WHERE id = $1
	`, inviteID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to revoke invite: %v", err)
	}

	s.emitClusterEvent(ctx, eventClusterInviteRevoked, ownerTenantID, userID, clusterID, "cluster_invite", inviteID, inviteID, "", "")

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
	var dbOwnerID sql.NullString
	err := s.db.QueryRowContext(ctx,
		"SELECT owner_tenant_id FROM quartermaster.infrastructure_clusters WHERE cluster_id = $1",
		clusterID).Scan(&dbOwnerID)
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

	builder := &pagination.KeysetBuilder{
		TimestampColumn: "i.created_at",
		IDColumn:        "i.id",
	}

	baseWhere := "WHERE i.cluster_id = $1"
	args := []any{clusterID}
	argIdx := 2

	// Get total count
	var total int32
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM quartermaster.cluster_invites i %s`, baseWhere)
	if countErr := s.db.QueryRowContext(ctx, countQuery, clusterID).Scan(&total); countErr != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", countErr)
	}

	// Add keyset condition
	where := baseWhere
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		where += " AND " + condition
		args = append(args, cursorArgs...)
	}

	query := fmt.Sprintf(`
		SELECT i.id, i.cluster_id, i.invited_tenant_id, i.invite_token, i.access_level,
		       i.resource_limits, i.status, i.created_by, i.created_at, i.expires_at, i.accepted_at,
		       t.name as invited_tenant_name, c.cluster_name
		FROM quartermaster.cluster_invites i
		LEFT JOIN quartermaster.tenants t ON i.invited_tenant_id = t.id
		LEFT JOIN quartermaster.infrastructure_clusters c ON i.cluster_id = c.cluster_id
		%s
		%s
		LIMIT $%d
	`, where, builder.OrderBy(params), len(args)+1)

	args = append(args, params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var invites []*quartermasterpb.ClusterInvite
	for rows.Next() {
		var invite quartermasterpb.ClusterInvite
		var resourceLimits sql.NullString
		var createdAt time.Time
		var expiresAt, acceptedAt sql.NullTime
		var invitedTenantName, clusterName sql.NullString

		if err := rows.Scan(
			&invite.Id, &invite.ClusterId, &invite.InvitedTenantId, &invite.InviteToken,
			&invite.AccessLevel, &resourceLimits, &invite.Status, &invite.CreatedBy,
			&createdAt, &expiresAt, &acceptedAt, &invitedTenantName, &clusterName,
		); err != nil {
			continue
		}

		invite.CreatedAt = timestamppb.New(createdAt)
		if expiresAt.Valid {
			invite.ExpiresAt = timestamppb.New(expiresAt.Time)
		}
		if acceptedAt.Valid {
			invite.AcceptedAt = timestamppb.New(acceptedAt.Time)
		}
		if invitedTenantName.Valid {
			invite.InvitedTenantName = &invitedTenantName.String
		}
		if clusterName.Valid {
			invite.ClusterName = &clusterName.String
		}
		if resourceLimits.Valid {
			var limitsMap map[string]any
			if json.Unmarshal([]byte(resourceLimits.String), &limitsMap) == nil {
				invite.ResourceLimits = mapToStruct(limitsMap)
			}
		}

		invites = append(invites, &invite)
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

	builder := &pagination.KeysetBuilder{
		TimestampColumn: "i.created_at",
		IDColumn:        "i.id",
	}

	baseWhere := "WHERE i.invited_tenant_id = $1 AND i.status = 'pending' AND (i.expires_at IS NULL OR i.expires_at > NOW())"
	args := []any{tenantID}
	argIdx := 2

	// Get total count
	var total int32
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM quartermaster.cluster_invites i %s`, baseWhere)
	if countErr := s.db.QueryRowContext(ctx, countQuery, tenantID).Scan(&total); countErr != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", countErr)
	}

	// Add keyset condition
	where := baseWhere
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		where += " AND " + condition
		args = append(args, cursorArgs...)
	}

	query := fmt.Sprintf(`
		SELECT i.id, i.cluster_id, i.invited_tenant_id, i.invite_token, i.access_level,
		       i.resource_limits, i.status, i.created_by, i.created_at, i.expires_at, i.accepted_at,
		       c.cluster_name
		FROM quartermaster.cluster_invites i
		JOIN quartermaster.infrastructure_clusters c ON i.cluster_id = c.cluster_id
		%s
		%s
		LIMIT $%d
	`, where, builder.OrderBy(params), len(args)+1)

	args = append(args, params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var invites []*quartermasterpb.ClusterInvite
	for rows.Next() {
		var invite quartermasterpb.ClusterInvite
		var resourceLimits sql.NullString
		var createdAt time.Time
		var expiresAt, acceptedAt sql.NullTime
		var clusterName string

		if err := rows.Scan(
			&invite.Id, &invite.ClusterId, &invite.InvitedTenantId, &invite.InviteToken,
			&invite.AccessLevel, &resourceLimits, &invite.Status, &invite.CreatedBy,
			&createdAt, &expiresAt, &acceptedAt, &clusterName,
		); err != nil {
			continue
		}

		invite.CreatedAt = timestamppb.New(createdAt)
		if expiresAt.Valid {
			invite.ExpiresAt = timestamppb.New(expiresAt.Time)
		}
		if acceptedAt.Valid {
			invite.AcceptedAt = timestamppb.New(acceptedAt.Time)
		}
		if clusterName != "" {
			invite.ClusterName = &clusterName
		}
		if resourceLimits.Valid {
			var limitsMap map[string]any
			if json.Unmarshal([]byte(resourceLimits.String), &limitsMap) == nil {
				invite.ResourceLimits = mapToStruct(limitsMap)
			}
		}

		invites = append(invites, &invite)
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
	var visibility, pricingModel string
	var requiresApproval bool
	var ownerTenantID sql.NullString
	var isPlatformOfficial bool
	err := s.db.QueryRowContext(ctx, `
		SELECT visibility, pricing_model, requires_approval, owner_tenant_id, is_platform_official
		FROM quartermaster.infrastructure_clusters
		WHERE cluster_id = $1 AND is_active = true
	`, clusterID).Scan(&visibility, &pricingModel, &requiresApproval, &ownerTenantID, &isPlatformOfficial)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "cluster not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if commercialErr := rejectDirectCommercialClusterAccess(tenantID, isPlatformOfficial, ownerTenantID, pricingModel, "requested"); commercialErr != nil {
		return nil, commercialErr
	}

	// Check visibility rules
	inviteToken := req.InviteToken

	switch visibility {
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
		var inviteClusterID, inviteTenantID string
		inviteErr := s.db.QueryRowContext(ctx, `
			SELECT id, cluster_id, invited_tenant_id, access_level, resource_limits
			FROM quartermaster.cluster_invites
			WHERE invite_token = $1 AND status = 'pending'
			  AND (expires_at IS NULL OR expires_at > NOW())
		`, *inviteToken).Scan(&inviteID, &inviteClusterID, &inviteTenantID, &inviteAccessLevel, &inviteResourceLimits)
		if errors.Is(inviteErr, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "invalid or expired invite token")
		}
		if inviteErr != nil {
			return nil, status.Errorf(codes.Internal, "database error: %v", inviteErr)
		}
		if inviteClusterID != clusterID {
			return nil, status.Error(codes.InvalidArgument, "invite token is for a different cluster")
		}
		if inviteTenantID != tenantID {
			return nil, status.Error(codes.PermissionDenied, "invite token is for a different tenant")
		}
	}

	// Determine subscription status
	subscriptionStatus := "active"
	if requiresApproval && (inviteToken == nil || *inviteToken == "") {
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

	if inviteID != "" {
		if _, err = tx.ExecContext(ctx, `
			UPDATE quartermaster.cluster_invites SET status = 'accepted', accepted_at = NOW()
			WHERE id = $1
		`, inviteID); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to accept invite: %v", err)
		}
	}

	var subscriptionID string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO quartermaster.tenant_cluster_access (
			id, tenant_id, cluster_id, access_level, subscription_status,
			resource_limits, requested_at, is_active, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, true, $7, $7)
		ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
			access_level = EXCLUDED.access_level,
			subscription_status = EXCLUDED.subscription_status,
			resource_limits = COALESCE(EXCLUDED.resource_limits, quartermaster.tenant_cluster_access.resource_limits),
			requested_at = COALESCE(quartermaster.tenant_cluster_access.requested_at, EXCLUDED.requested_at),
			is_active = true,
			updated_at = NOW()
		RETURNING id
	`, id, tenantID, clusterID, accessLevel, subscriptionStatus,
		inviteResourceLimits, now).Scan(&subscriptionID)
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
	var inviteID, clusterID, invitedTenantID, accessLevel, pricingModel string
	var resourceLimits sql.NullString
	var ownerTenantID sql.NullString
	var isPlatformOfficial bool
	err := s.db.QueryRowContext(ctx, `
		SELECT i.id, i.cluster_id, i.invited_tenant_id, i.access_level, i.resource_limits,
		       c.pricing_model, c.owner_tenant_id, c.is_platform_official
		FROM quartermaster.cluster_invites i
		JOIN quartermaster.infrastructure_clusters c ON c.cluster_id = i.cluster_id
		WHERE i.invite_token = $1 AND i.status = 'pending'
		  AND (i.expires_at IS NULL OR i.expires_at > NOW())
	`, inviteToken).Scan(&inviteID, &clusterID, &invitedTenantID, &accessLevel, &resourceLimits, &pricingModel, &ownerTenantID, &isPlatformOfficial)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "invalid or expired invite token")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	if invitedTenantID != tenantID {
		return nil, status.Error(codes.PermissionDenied, "invite is for a different tenant")
	}
	if commercialErr := rejectDirectCommercialClusterAccess(tenantID, isPlatformOfficial, ownerTenantID, pricingModel, "accepted"); commercialErr != nil {
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

	if _, err = tx.ExecContext(ctx, `
		UPDATE quartermaster.cluster_invites SET status = 'accepted', accepted_at = NOW()
		WHERE id = $1
	`, inviteID); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to accept invite: %v", err)
	}

	now := time.Now()
	id := uuid.New().String()

	var subscriptionID string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO quartermaster.tenant_cluster_access (
			id, tenant_id, cluster_id, access_level, subscription_status,
			resource_limits, approved_at, is_active, created_at, updated_at
		) VALUES ($1, $2, $3, $4, 'active', $5, $6, true, $6, $6)
		ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
			access_level = EXCLUDED.access_level,
			subscription_status = 'active',
			resource_limits = COALESCE(EXCLUDED.resource_limits, quartermaster.tenant_cluster_access.resource_limits),
			approved_at = NOW(),
			is_active = true,
			updated_at = NOW()
		RETURNING id
	`, id, tenantID, clusterID, accessLevel, resourceLimits, now).Scan(&subscriptionID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create subscription: %v", err)
	}

	if enqErr := s.emitClusterEventTx(ctx, tx, eventClusterSubscriptionApproved, tenantID, userID, clusterID, "cluster_subscription", subscriptionID, inviteID, subscriptionID, ""); enqErr != nil {
		return nil, status.Errorf(codes.Internal, "enqueue cluster subscription event: %v", enqErr)
	}
	if enqErr := s.enqueueTenantAliasEnsureTx(ctx, tx, tenantID, true); enqErr != nil {
		return nil, status.Errorf(codes.Internal, "enqueue tenant-alias ensure: %v", enqErr)
	}

	if err = tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit invite acceptance: %v", err)
	}

	sub, err := s.getClusterSubscription(ctx, tenantID, clusterID)
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
	var dbOwnerID sql.NullString
	err := s.db.QueryRowContext(ctx,
		"SELECT owner_tenant_id FROM quartermaster.infrastructure_clusters WHERE cluster_id = $1",
		clusterID).Scan(&dbOwnerID)
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

	builder := &pagination.KeysetBuilder{
		TimestampColumn: "a.created_at",
		IDColumn:        "a.id",
	}

	baseWhere := "WHERE a.cluster_id = $1 AND a.subscription_status = 'pending_approval'"
	args := []any{clusterID}
	argIdx := 2

	// Get total count
	var total int32
	countQuery := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM quartermaster.tenant_cluster_access a
		%s
	`, baseWhere)
	if countErr := s.db.QueryRowContext(ctx, countQuery, clusterID).Scan(&total); countErr != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", countErr)
	}

	// Add keyset condition
	where := baseWhere
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		where += " AND " + condition
		args = append(args, cursorArgs...)
	}

	query := fmt.Sprintf(`
		SELECT a.id, a.tenant_id, a.cluster_id, a.access_level, a.subscription_status,
		       a.resource_limits, a.requested_at, a.approved_at, a.approved_by,
		       a.rejection_reason, a.expires_at, a.created_at, a.updated_at,
		       c.cluster_name, t.name as tenant_name
		FROM quartermaster.tenant_cluster_access a
		JOIN quartermaster.infrastructure_clusters c ON a.cluster_id = c.cluster_id
		JOIN quartermaster.tenants t ON a.tenant_id = t.id
		%s
		%s
		LIMIT $%d
	`, where, builder.OrderBy(params), len(args)+1)

	args = append(args, params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var subscriptions []*quartermasterpb.ClusterSubscription
	for rows.Next() {
		sub, err := scanClusterSubscription(rows)
		if err != nil {
			continue
		}
		subscriptions = append(subscriptions, sub)
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
	var tenantID, clusterID, pricingModel string
	var dbOwnerID sql.NullString
	var isPlatformOfficial bool
	err = tx.QueryRowContext(ctx, `
		SELECT a.tenant_id, a.cluster_id, c.owner_tenant_id, c.pricing_model, c.is_platform_official
		FROM quartermaster.tenant_cluster_access a
		JOIN quartermaster.infrastructure_clusters c ON a.cluster_id = c.cluster_id
		WHERE a.id = $1
	`, subscriptionID).Scan(&tenantID, &clusterID, &dbOwnerID, &pricingModel, &isPlatformOfficial)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "subscription not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !dbOwnerID.Valid || dbOwnerID.String != ownerTenantID {
		return nil, status.Error(codes.PermissionDenied, "only cluster owner can approve subscriptions")
	}
	if commercialErr := rejectDirectCommercialClusterAccess(tenantID, isPlatformOfficial, dbOwnerID, pricingModel, "approved"); commercialErr != nil {
		return nil, commercialErr
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE quartermaster.tenant_cluster_access
		SET subscription_status = 'active', approved_at = NOW(), approved_by = $2, updated_at = NOW()
		WHERE id = $1
	`, subscriptionID, ownerTenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to approve subscription: %v", err)
	}

	if enqErr := s.emitClusterEventTx(ctx, tx, eventClusterSubscriptionApproved, tenantID, userID, clusterID, "cluster_subscription", subscriptionID, "", subscriptionID, ""); enqErr != nil {
		return nil, status.Errorf(codes.Internal, "enqueue cluster subscription event: %v", enqErr)
	}
	if enqErr := s.enqueueTenantAliasEnsureTx(ctx, tx, tenantID, true); enqErr != nil {
		return nil, status.Errorf(codes.Internal, "enqueue tenant-alias ensure: %v", enqErr)
	}

	if err = tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit subscription approval: %v", err)
	}

	sub, err := s.getClusterSubscription(ctx, tenantID, clusterID)
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
	var tenantID, clusterID string
	var dbOwnerID sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT a.tenant_id, a.cluster_id, c.owner_tenant_id
		FROM quartermaster.tenant_cluster_access a
		JOIN quartermaster.infrastructure_clusters c ON a.cluster_id = c.cluster_id
		WHERE a.id = $1
	`, subscriptionID).Scan(&tenantID, &clusterID, &dbOwnerID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "subscription not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !dbOwnerID.Valid || dbOwnerID.String != ownerTenantID {
		return nil, status.Error(codes.PermissionDenied, "only cluster owner can reject subscriptions")
	}

	reason := ""
	if req.Reason != nil {
		reason = *req.Reason
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE quartermaster.tenant_cluster_access
		SET subscription_status = 'rejected', rejection_reason = $2, is_active = false, updated_at = NOW()
		WHERE id = $1
	`, subscriptionID, reason)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to reject subscription: %v", err)
	}

	if enqErr := s.emitClusterEventTx(ctx, tx, eventClusterSubscriptionRejected, tenantID, userID, clusterID, "cluster_subscription", subscriptionID, "", subscriptionID, reason); enqErr != nil {
		return nil, status.Errorf(codes.Internal, "enqueue cluster subscription event: %v", enqErr)
	}

	if err = tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit subscription rejection: %v", err)
	}

	sub, err := s.getClusterSubscription(ctx, tenantID, clusterID)
	if err != nil {
		return nil, err
	}

	return sub, nil
}

// ListPeers returns clusters that share at least one tenant with the requesting cluster.
// Used by Foghorn federation to discover peers for cross-cluster stream routing.
func (s *QuartermasterServer) ListPeers(ctx context.Context, req *quartermasterpb.ListPeersRequest) (*quartermasterpb.ListPeersResponse, error) {
	clusterID := req.GetClusterId()
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}

	// Find all clusters that share at least one active tenant with the requesting cluster.
	// For each peer, aggregate the shared tenant IDs and resolve the Foghorn gRPC address
	// from service_instances.
	rows, err := s.db.QueryContext(ctx, `
		WITH my_tenants AS (
			SELECT DISTINCT tenant_id
			FROM quartermaster.tenant_cluster_access
			WHERE cluster_id = $1 AND is_active = TRUE AND subscription_status = 'active'
		),
		peer_clusters AS (
			SELECT tca.cluster_id,
			       array_agg(DISTINCT tca.tenant_id::text) AS shared_tenant_ids
			FROM quartermaster.tenant_cluster_access tca
			JOIN my_tenants mt ON tca.tenant_id = mt.tenant_id
			WHERE tca.cluster_id != $1
			  AND tca.is_active = TRUE
			  AND tca.subscription_status = 'active'
			GROUP BY tca.cluster_id
		)
		SELECT pc.cluster_id,
		       pc.shared_tenant_ids,
		       ic.cluster_name,
		       ic.cluster_type,
		       COALESCE(
		           (SELECT si.advertise_host || ':' || si.port
		            FROM quartermaster.service_cluster_assignments sca
		            JOIN quartermaster.service_instances si ON si.id = sca.service_instance_id
		            JOIN quartermaster.services svc ON svc.service_id = si.service_id
		            WHERE sca.cluster_id = pc.cluster_id
		              AND sca.is_active = TRUE
		              AND svc.type = 'foghorn'
		              AND si.status = 'running'
		              AND si.health_status = 'healthy'
		              AND si.protocol = 'grpc'
		            ORDER BY si.updated_at DESC, si.id ASC
		            LIMIT 1),
		           ''
		       ) AS foghorn_addr
		FROM peer_clusters pc
		JOIN quartermaster.infrastructure_clusters ic ON ic.cluster_id = pc.cluster_id
		WHERE ic.is_active = TRUE
	`, clusterID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "query peers: %v", err)
	}
	defer rows.Close()

	var peers []*quartermasterpb.PeerCluster
	for rows.Next() {
		var peer quartermasterpb.PeerCluster
		var sharedTenantIDs []string
		if err := rows.Scan(&peer.ClusterId, pq.Array(&sharedTenantIDs), &peer.ClusterName, &peer.ClusterType, &peer.FoghornAddr); err != nil {
			return nil, status.Errorf(codes.Internal, "scan peer: %v", err)
		}
		peer.SharedTenantIds = sharedTenantIDs
		peers = append(peers, &peer)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "iterate peers: %v", err)
	}

	return &quartermasterpb.ListPeersResponse{Peers: peers}, nil
}

// getClusterSubscription is a helper to fetch a subscription by tenant and cluster
func (s *QuartermasterServer) getClusterSubscription(ctx context.Context, tenantID, clusterID string) (*quartermasterpb.ClusterSubscription, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT a.id, a.tenant_id, a.cluster_id, a.access_level, a.subscription_status,
		       a.resource_limits, a.requested_at, a.approved_at, a.approved_by,
		       a.rejection_reason, a.expires_at, a.created_at, a.updated_at,
		       c.cluster_name, t.name as tenant_name
		FROM quartermaster.tenant_cluster_access a
		JOIN quartermaster.infrastructure_clusters c ON a.cluster_id = c.cluster_id
		JOIN quartermaster.tenants t ON a.tenant_id = t.id
		WHERE a.tenant_id = $1 AND a.cluster_id = $2
	`, tenantID, clusterID)

	return scanClusterSubscriptionRow(row)
}

// scanClusterSubscription scans a ClusterSubscription from rows
func scanClusterSubscription(rows *sql.Rows) (*quartermasterpb.ClusterSubscription, error) {
	var sub quartermasterpb.ClusterSubscription
	var resourceLimits sql.NullString
	var requestedAt, approvedAt, expiresAt sql.NullTime
	var approvedBy, rejectionReason, clusterName, tenantName sql.NullString
	var createdAt, updatedAt time.Time
	var subscriptionStatus string

	err := rows.Scan(
		&sub.Id, &sub.TenantId, &sub.ClusterId, &sub.AccessLevel, &subscriptionStatus,
		&resourceLimits, &requestedAt, &approvedAt, &approvedBy,
		&rejectionReason, &expiresAt, &createdAt, &updatedAt,
		&clusterName, &tenantName,
	)
	if err != nil {
		return nil, err
	}

	sub.SubscriptionStatus = subscriptionStatusStringToProto(subscriptionStatus)
	sub.CreatedAt = timestamppb.New(createdAt)
	sub.UpdatedAt = timestamppb.New(updatedAt)

	if requestedAt.Valid {
		sub.RequestedAt = timestamppb.New(requestedAt.Time)
	}
	if approvedAt.Valid {
		sub.ApprovedAt = timestamppb.New(approvedAt.Time)
	}
	if approvedBy.Valid {
		sub.ApprovedBy = &approvedBy.String
	}
	if rejectionReason.Valid {
		sub.RejectionReason = &rejectionReason.String
	}
	if expiresAt.Valid {
		sub.ExpiresAt = timestamppb.New(expiresAt.Time)
	}
	if clusterName.Valid {
		sub.ClusterName = &clusterName.String
	}
	if tenantName.Valid {
		sub.TenantName = &tenantName.String
	}
	if resourceLimits.Valid {
		var limitsMap map[string]any
		if json.Unmarshal([]byte(resourceLimits.String), &limitsMap) == nil {
			sub.ResourceLimits = mapToStruct(limitsMap)
		}
	}

	return &sub, nil
}

// scanClusterSubscriptionRow scans a ClusterSubscription from a single row
func scanClusterSubscriptionRow(row *sql.Row) (*quartermasterpb.ClusterSubscription, error) {
	var sub quartermasterpb.ClusterSubscription
	var resourceLimits sql.NullString
	var requestedAt, approvedAt, expiresAt sql.NullTime
	var approvedBy, rejectionReason, clusterName, tenantName sql.NullString
	var createdAt, updatedAt time.Time
	var subscriptionStatus string

	err := row.Scan(
		&sub.Id, &sub.TenantId, &sub.ClusterId, &sub.AccessLevel, &subscriptionStatus,
		&resourceLimits, &requestedAt, &approvedAt, &approvedBy,
		&rejectionReason, &expiresAt, &createdAt, &updatedAt,
		&clusterName, &tenantName,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "subscription not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	sub.SubscriptionStatus = subscriptionStatusStringToProto(subscriptionStatus)
	sub.CreatedAt = timestamppb.New(createdAt)
	sub.UpdatedAt = timestamppb.New(updatedAt)

	if requestedAt.Valid {
		sub.RequestedAt = timestamppb.New(requestedAt.Time)
	}
	if approvedAt.Valid {
		sub.ApprovedAt = timestamppb.New(approvedAt.Time)
	}
	if approvedBy.Valid {
		sub.ApprovedBy = &approvedBy.String
	}
	if rejectionReason.Valid {
		sub.RejectionReason = &rejectionReason.String
	}
	if expiresAt.Valid {
		sub.ExpiresAt = timestamppb.New(expiresAt.Time)
	}
	if clusterName.Valid {
		sub.ClusterName = &clusterName.String
	}
	if tenantName.Valid {
		sub.TenantName = &tenantName.String
	}
	if resourceLimits.Valid {
		var limitsMap map[string]any
		if json.Unmarshal([]byte(resourceLimits.String), &limitsMap) == nil {
			sub.ResourceLimits = mapToStruct(limitsMap)
		}
	}

	return &sub, nil
}

// GRPCServerConfig contains configuration for creating a Quartermaster gRPC server
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
