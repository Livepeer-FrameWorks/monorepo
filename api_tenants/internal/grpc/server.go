package grpc

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	decklogclient "frameworks/pkg/clients/decklog"
	"frameworks/pkg/clients/navigator"
	purserclient "frameworks/pkg/clients/purser"
	"frameworks/pkg/config"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
	"frameworks/pkg/pagination"
	pb "frameworks/pkg/proto"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// QuartermasterServer implements the Quartermaster gRPC services
type QuartermasterServer struct {
	pb.UnimplementedTenantServiceServer
	pb.UnimplementedBootstrapServiceServer
	pb.UnimplementedNodeServiceServer
	pb.UnimplementedClusterServiceServer
	pb.UnimplementedMeshServiceServer
	pb.UnimplementedServiceRegistryServiceServer
	db              *sql.DB
	logger          logging.Logger
	navigatorClient *navigator.Client
	decklogClient   *decklogclient.BatchedClient
	purserClient    *purserclient.GRPCClient // For billing status lookups (cross-service via gRPC, not DB)
	metrics         *ServerMetrics
}

// NewQuartermasterServer creates a new Quartermaster gRPC server
func NewQuartermasterServer(db *sql.DB, logger logging.Logger, navigatorClient *navigator.Client, decklogClient *decklogclient.BatchedClient, purserClient *purserclient.GRPCClient, metrics *ServerMetrics) *QuartermasterServer {
	return &QuartermasterServer{
		db:              db,
		logger:          logger,
		navigatorClient: navigatorClient,
		decklogClient:   decklogClient,
		purserClient:    purserClient,
		metrics:         metrics,
	}
}

// mapToStruct converts a map[string]interface{} to a protobuf Struct
func mapToStruct(m map[string]interface{}) *structpb.Struct {
	if m == nil {
		return nil
	}
	s, err := structpb.NewStruct(m)
	if err != nil {
		return nil
	}
	return s
}

// ValidateTenant validates a tenant and returns its features/limits
// Billing info is fetched via Purser gRPC (no cross-service DB access)
func (s *QuartermasterServer) ValidateTenant(ctx context.Context, req *pb.ValidateTenantRequest) (*pb.ValidateTenantResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return &pb.ValidateTenantResponse{
			Valid: false,
			Error: "tenant_id required",
		}, nil
	}

	var name string
	var isActive bool
	var rateLimitPerMinute, rateLimitBurst int32

	// Query ONLY quartermaster.tenants (no cross-service DB access)
	err := s.db.QueryRowContext(ctx, `
		SELECT name, is_active, rate_limit_per_minute, rate_limit_burst
		FROM quartermaster.tenants
		WHERE id = $1
	`, tenantID).Scan(&name, &isActive, &rateLimitPerMinute, &rateLimitBurst)

	if errors.Is(err, sql.ErrNoRows) {
		return &pb.ValidateTenantResponse{
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

	return &pb.ValidateTenantResponse{
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
func (s *QuartermasterServer) GetTenant(ctx context.Context, req *pb.GetTenantRequest) (*pb.GetTenantResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	var tenant pb.Tenant
	var subdomain, customDomain, logoURL, primaryClusterID, kafkaTopicPrefix, databaseURL sql.NullString
	var kafkaBrokers []string
	var createdAt, updatedAt time.Time

	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, subdomain, custom_domain, logo_url, primary_color, secondary_color,
		       deployment_tier, deployment_model,
		       primary_cluster_id, kafka_topic_prefix, kafka_brokers, database_url,
		       is_active, created_at, updated_at,
		       rate_limit_per_minute, rate_limit_burst
		FROM quartermaster.tenants
		WHERE id = $1
	`, tenantID).Scan(
		&tenant.Id, &tenant.Name, &subdomain, &customDomain, &logoURL,
		&tenant.PrimaryColor, &tenant.SecondaryColor, &tenant.DeploymentTier,
		&tenant.DeploymentModel,
		&primaryClusterID, &kafkaTopicPrefix, pq.Array(&kafkaBrokers), &databaseURL,
		&tenant.IsActive, &createdAt, &updatedAt,
		&tenant.RateLimitPerMinute, &tenant.RateLimitBurst,
	)

	if errors.Is(err, sql.ErrNoRows) {
		return &pb.GetTenantResponse{Error: "Tenant not found"}, nil
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
	if kafkaTopicPrefix.Valid {
		tenant.KafkaTopicPrefix = &kafkaTopicPrefix.String
	}
	if databaseURL.Valid {
		tenant.DatabaseUrl = &databaseURL.String
	}
	tenant.KafkaBrokers = kafkaBrokers
	tenant.CreatedAt = timestamppb.New(createdAt)
	tenant.UpdatedAt = timestamppb.New(updatedAt)

	return &pb.GetTenantResponse{Tenant: &tenant}, nil
}

// GetClusterRouting returns the best cluster for a tenant's stream.
// Validates cluster has capacity (max_streams, max_bandwidth_mbps) before returning.
func (s *QuartermasterServer) GetClusterRouting(ctx context.Context, req *pb.GetClusterRoutingRequest) (*pb.ClusterRoutingResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	// Get tenant's primary cluster and deployment tier
	var primaryClusterID, deploymentTier string
	err := s.db.QueryRowContext(ctx, `
		SELECT primary_cluster_id, deployment_tier
		FROM quartermaster.tenants
		WHERE id = $1 AND is_active = true
	`, tenantID).Scan(&primaryClusterID, &deploymentTier)

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

	estimatedMbps := req.GetEstimatedMbps()

	// Get cluster info with capacity validation
	// max_streams = 0 means unlimited
	// max_bandwidth_mbps = 0 means unlimited
	var resp pb.ClusterRoutingResponse
	var kafkaBrokers []string
	var databaseURL, periscopeURL sql.NullString
	var topicPrefix string
	err = s.db.QueryRowContext(ctx, `
		SELECT
			c.cluster_id, c.cluster_name, c.cluster_type, c.base_url,
			c.kafka_brokers, c.database_url, c.periscope_url,
			COALESCE(tca.kafka_topic_prefix, t.kafka_topic_prefix, '') as topic_prefix,
			c.max_concurrent_streams, c.current_stream_count, c.health_status
		FROM quartermaster.infrastructure_clusters c
		JOIN quartermaster.tenants t ON t.id = $2
		LEFT JOIN quartermaster.tenant_cluster_assignments tca ON tca.tenant_id = t.id AND tca.cluster_id = c.cluster_id
		WHERE c.cluster_id = $1
		  AND c.is_active = true
		  AND (
		    c.max_concurrent_streams = 0 OR
		    c.current_stream_count < c.max_concurrent_streams
		  )
		  AND (
		    $3 = 0 OR
		    c.max_bandwidth_mbps = 0 OR
		    c.current_bandwidth_mbps + $3 <= c.max_bandwidth_mbps
		  )
	`, primaryClusterID, tenantID, estimatedMbps).Scan(
		&resp.ClusterId, &resp.ClusterName, &resp.ClusterType, &resp.BaseUrl,
		pq.Array(&kafkaBrokers), &databaseURL, &periscopeURL,
		&topicPrefix,
		&resp.MaxStreams, &resp.CurrentStreams, &resp.HealthStatus,
	)

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

	// Check tenant-specific resource limits from tenant_cluster_access
	var tenantResourceLimits []byte
	err = s.db.QueryRowContext(ctx, `
		SELECT resource_limits
		FROM quartermaster.tenant_cluster_access
		WHERE tenant_id = $1 AND cluster_id = $2 AND is_active = TRUE
	`, tenantID, primaryClusterID).Scan(&tenantResourceLimits)

	if err == nil && len(tenantResourceLimits) > 0 {
		var limits map[string]interface{}
		if json.Unmarshal(tenantResourceLimits, &limits) == nil {
			// Check max_streams tenant limit
			if maxStreams, ok := limits["max_streams"].(float64); ok && maxStreams > 0 {
				// Count tenant's current streams on this cluster
				var currentTenantStreams int
				if err := s.db.QueryRowContext(ctx, `
					SELECT COUNT(*) FROM quartermaster.service_instances
					WHERE cluster_id = $1
					  AND service_id = 'stream'
					  AND status = 'running'
					  AND node_id IN (
					    SELECT node_id FROM quartermaster.infrastructure_nodes WHERE cluster_id = $1
					  )
				`, primaryClusterID).Scan(&currentTenantStreams); err != nil {
					s.logger.WithError(err).Warn("Failed to get current stream count for limit check")
				}

				// Note: This is a simplified check. In production, you'd want to track
				// streams per tenant, not total streams on cluster.
				// For now, we'll just log a warning if limits are configured.
				if currentTenantStreams >= int(maxStreams) {
					s.logger.WithFields(logging.Fields{
						"tenant_id":       tenantID,
						"cluster_id":      primaryClusterID,
						"max_streams":     maxStreams,
						"current_streams": currentTenantStreams,
					}).Warn("Tenant approaching stream limit")
				}
			}

			// Check max_bandwidth_mbps tenant limit
			if maxBandwidth, ok := limits["max_bandwidth_mbps"].(float64); ok && maxBandwidth > 0 && estimatedMbps > 0 {
				// If tenant has bandwidth limit and would exceed it, warn
				// Full enforcement would require tracking per-tenant bandwidth usage
				s.logger.WithFields(logging.Fields{
					"tenant_id":      tenantID,
					"max_bandwidth":  maxBandwidth,
					"estimated_mbps": estimatedMbps,
				}).Debug("Tenant has bandwidth limit configured")
			}
		}
	}

	return &resp, nil
}

// BootstrapService handles service registration with idempotent instance management
func (s *QuartermasterServer) BootstrapService(ctx context.Context, req *pb.BootstrapServiceRequest) (*pb.BootstrapServiceResponse, error) {
	serviceType := req.GetType()
	if serviceType == "" {
		return nil, status.Error(codes.InvalidArgument, "type required")
	}

	// 1. Resolve cluster from token, request, or fallback (single cluster only)
	var clusterID string
	var tokenBoundClusterID string

	token := req.GetToken()
	if token != "" {
		var kind string
		var expiresAt time.Time
		err := s.db.QueryRowContext(ctx, `
			SELECT kind, COALESCE(cluster_id, ''), expires_at
			FROM quartermaster.bootstrap_tokens
			WHERE token = $1 AND used_at IS NULL
		`, token).Scan(&kind, &tokenBoundClusterID, &expiresAt)
		if errors.Is(err, sql.ErrNoRows) || kind != "service" || time.Now().After(expiresAt) {
			return nil, status.Error(codes.Unauthenticated, "invalid bootstrap token")
		}
		// Mark token used
		_, _ = s.db.ExecContext(ctx, `
			UPDATE quartermaster.bootstrap_tokens
			SET used_at = NOW(), usage_count = usage_count + 1
			WHERE token = $1
		`, token)
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
		err := s.db.QueryRowContext(ctx, `
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
		rows, err := s.db.QueryContext(ctx, `
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

	// 2. Get or create service record (service_id/type default to request type)
	var serviceID string
	err := s.db.QueryRowContext(ctx, `
		SELECT service_id FROM quartermaster.services WHERE service_id = $1 OR name = $1
	`, serviceType).Scan(&serviceID)

	if errors.Is(err, sql.ErrNoRows) {
		serviceID = serviceType
		defaultProtocol := strings.ToLower(strings.TrimSpace(req.GetProtocol()))
		if defaultProtocol == "" {
			defaultProtocol = "http"
		}
		_, err = s.db.ExecContext(ctx, `
			INSERT INTO quartermaster.services (service_id, name, plane, type, protocol, is_active, created_at, updated_at)
			VALUES ($1, $2, 'control', $3, $4, true, NOW(), NOW())
		`, serviceID, serviceType, serviceType, defaultProtocol)
		if err != nil {
			s.logger.WithError(err).WithField("service_type", serviceType).Error("Failed to create service")
			return nil, status.Errorf(codes.Internal, "failed to create service: %v", err)
		}
	} else if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// 3. Normalize service ID for instance naming
	sluggedID := strings.ToLower(strings.TrimSpace(serviceID))
	sluggedID = strings.ReplaceAll(sluggedID, " ", "-")
	sluggedID = strings.ReplaceAll(sluggedID, "_", "-")
	instanceID := fmt.Sprintf("inst-%s-%s", sluggedID, uuid.NewString()[:8])

	// 4. Derive protocol and advertise host
	proto := strings.ToLower(strings.TrimSpace(req.GetProtocol()))
	if proto == "" {
		proto = "http"
	}
	advHost := req.GetAdvertiseHost()
	if advHost == "" {
		advHost = req.GetHost()
	}
	if advHost == "" {
		// In gRPC we can't get client IP easily, require it to be set
		return nil, status.Error(codes.InvalidArgument, "advertise_host or host required")
	}

	healthEndpoint := req.HealthEndpoint
	port := req.GetPort()

	// 5. Idempotent registration: check for existing instance on same (service_id, cluster_id, protocol, port)
	var existingID, existingInstanceID string
	_ = s.db.QueryRowContext(ctx, `
		SELECT id::text, instance_id FROM quartermaster.service_instances
		WHERE service_id = $1 AND cluster_id = $2 AND protocol = $3 AND port = $4
		ORDER BY updated_at DESC NULLS LAST, started_at DESC NULLS LAST LIMIT 1
	`, serviceID, clusterID, proto, port).Scan(&existingID, &existingInstanceID)

	if existingID != "" {
		// Update existing row
		_, err = s.db.ExecContext(ctx, `
			UPDATE quartermaster.service_instances
			SET advertise_host = $1,
			    health_endpoint_override = $2,
			    version = $3,
			    status = 'running',
			    health_status = 'unknown',
			    started_at = COALESCE(started_at, NOW()),
			    last_health_check = NULL,
			    updated_at = NOW()
			WHERE id = $4::uuid
		`, advHost, healthEndpoint, req.GetVersion(), existingID)
		if err != nil {
			s.logger.WithError(err).Error("Failed to update service instance")
			return nil, status.Errorf(codes.Internal, "failed to update service instance: %v", err)
		}
		instanceID = existingInstanceID
	} else {
		// Insert new row
		_, err = s.db.ExecContext(ctx, `
			INSERT INTO quartermaster.service_instances
				(instance_id, cluster_id, service_id, protocol, advertise_host, health_endpoint_override, version, port, status, health_status, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'running', 'unknown', NOW(), NOW())
		`, instanceID, clusterID, serviceID, proto, advHost, healthEndpoint, req.GetVersion(), port)
		if err != nil {
			s.logger.WithError(err).Error("Failed to create service instance")
			return nil, status.Errorf(codes.Internal, "failed to create service instance: %v", err)
		}
	}

	// 6. Cleanup stale/duplicate instances
	_, _ = s.db.ExecContext(ctx, `
		UPDATE quartermaster.service_instances
		SET status = 'stopped', updated_at = NOW()
		WHERE service_id = $1 AND cluster_id = $2 AND instance_id != $3
		  AND (
		    last_health_check IS NULL OR
		    last_health_check < NOW() - INTERVAL '10 minutes' OR
		    (COALESCE(advertise_host, '') = $4 AND COALESCE(protocol, '') = $5 AND COALESCE(port, 0) = $6)
		  )
	`, serviceID, clusterID, instanceID, advHost, proto, port)

	// 7. Look up cluster owner tenant for dual-tenant attribution
	var ownerTenantID sql.NullString
	_ = s.db.QueryRowContext(ctx, `
		SELECT owner_tenant_id FROM quartermaster.infrastructure_clusters WHERE cluster_id = $1
	`, clusterID).Scan(&ownerTenantID)

	resp := &pb.BootstrapServiceResponse{
		ServiceId:  serviceID,
		InstanceId: instanceID,
		ClusterId:  clusterID,
	}
	if ownerTenantID.Valid && ownerTenantID.String != "" {
		resp.OwnerTenantId = &ownerTenantID.String
	}
	return resp, nil
}

// GetNodeOwner returns the owner tenant for a node
func (s *QuartermasterServer) GetNodeOwner(ctx context.Context, req *pb.GetNodeOwnerRequest) (*pb.NodeOwnerResponse, error) {
	nodeID := req.GetNodeId()
	if nodeID == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id required")
	}

	var resp pb.NodeOwnerResponse
	var ownerTenantID, tenantName sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT n.node_id, n.cluster_id, c.cluster_name, c.owner_tenant_id, t.name
		FROM quartermaster.infrastructure_nodes n
		JOIN quartermaster.infrastructure_clusters c ON n.cluster_id = c.cluster_id
		LEFT JOIN quartermaster.tenants t ON c.owner_tenant_id = t.id
		WHERE n.node_id = $1
	`, nodeID).Scan(&resp.NodeId, &resp.ClusterId, &resp.ClusterName, &ownerTenantID, &tenantName)

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

	return &resp, nil
}

// DiscoverServices finds instances of a service type with cursor pagination
func (s *QuartermasterServer) DiscoverServices(ctx context.Context, req *pb.ServiceDiscoveryRequest) (*pb.ServiceDiscoveryResponse, error) {
	serviceType := req.GetServiceType()
	if serviceType == "" {
		return nil, status.Error(codes.InvalidArgument, "service_type required")
	}

	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid cursor: %v", err)
	}

	tenantID := middleware.GetTenantID(ctx)

	// Build dynamic query
	args := []interface{}{serviceType}
	argIdx := 2

	whereClause := "WHERE s.type = $1 AND si.status IN ('running','starting','active')"

	if tenantID != "" {
		// Authenticated: Filter by subscription OR ownership
		whereClause += fmt.Sprintf(` AND (si.cluster_id IN (
			SELECT tca.cluster_id FROM quartermaster.tenant_cluster_access tca
			WHERE tca.tenant_id = $%d AND tca.is_active = true
		) OR si.cluster_id IN (
			SELECT ic.cluster_id FROM quartermaster.infrastructure_clusters ic
			WHERE ic.owner_tenant_id = $%d
		))`, argIdx, argIdx)
		args = append(args, tenantID)
		argIdx++
	} else {
		// Unauthenticated: Filter by default cluster only
		whereClause += ` AND si.cluster_id IN (
			SELECT ic.cluster_id FROM quartermaster.infrastructure_clusters ic
			WHERE ic.is_default_cluster = true
		)`
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

	query := fmt.Sprintf(`
		SELECT si.id, si.instance_id, si.service_id, si.cluster_id, si.node_id,
		       si.protocol, si.advertise_host, si.port, si.health_endpoint_override, si.status,
		       si.last_health_check, si.created_at, si.updated_at
		FROM quartermaster.service_instances si
		JOIN quartermaster.services s ON si.service_id = s.service_id
		%s
		ORDER BY si.created_at %s, si.id %s
		LIMIT $%d
	`, whereClause, orderDir, orderDir, argIdx)
	args = append(args, params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var instances []*pb.ServiceInstance
	for rows.Next() {
		var inst pb.ServiceInstance
		var nodeID, host, healthEndpoint sql.NullString
		var lastHealthCheck sql.NullTime
		var createdAt, updatedAt time.Time

		err := rows.Scan(
			&inst.Id, &inst.InstanceId, &inst.ServiceId, &inst.ClusterId, &nodeID,
			&inst.Protocol, &host, &inst.Port, &healthEndpoint, &inst.Status,
			&lastHealthCheck, &createdAt, &updatedAt,
		)
		if err != nil {
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
		if lastHealthCheck.Valid {
			inst.LastHealthCheck = timestamppb.New(lastHealthCheck.Time)
		}
		inst.CreatedAt = timestamppb.New(createdAt)
		inst.UpdatedAt = timestamppb.New(updatedAt)

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
	resp := &pb.ServiceDiscoveryResponse{
		Instances:  instances,
		Pagination: pagination.BuildResponse(resultsLen, params.Limit, params.Direction, int32(len(instances)), startCursor, endCursor),
	}

	return resp, nil
}

// ============================================================================
// TENANT SERVICE - Additional Methods
// ============================================================================

// ResolveTenant resolves a tenant by subdomain or platform-managed domain (no BYO)
func (s *QuartermasterServer) ResolveTenant(ctx context.Context, req *pb.ResolveTenantRequest) (*pb.ResolveTenantResponse, error) {
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
		return &pb.ResolveTenantResponse{Found: false, Error: "Tenant not found"}, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	resp := &pb.ResolveTenantResponse{
		Found:      true,
		TenantId:   tenantID,
		TenantName: tenantName,
	}
	if primaryClusterID.Valid {
		resp.PrimaryClusterId = primaryClusterID.String
	}

	return resp, nil
}

// ListTenants lists all tenants with pagination
func (s *QuartermasterServer) ListTenants(ctx context.Context, req *pb.ListTenantsRequest) (*pb.ListTenantsResponse, error) {
	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid cursor: %v", err)
	}

	// Build dynamic query
	args := []interface{}{}
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
		       primary_cluster_id, kafka_topic_prefix, kafka_brokers, database_url,
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

	var tenants []*pb.Tenant
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

	resp := &pb.ListTenantsResponse{
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
func (s *QuartermasterServer) ListActiveTenants(ctx context.Context, req *pb.ListActiveTenantsRequest) (*pb.ListActiveTenantsResponse, error) {
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

	return &pb.ListActiveTenantsResponse{
		TenantIds: tenantIDs,
	}, nil
}

// CreateTenant creates a new tenant
func (s *QuartermasterServer) CreateTenant(ctx context.Context, req *pb.CreateTenantRequest) (*pb.CreateTenantResponse, error) {
	name := req.GetName()
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "name required")
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
	`, tenantID, name, req.Subdomain, req.CustomDomain, req.LogoUrl,
		req.GetPrimaryColor(), req.GetSecondaryColor(),
		req.GetDeploymentTier(), req.GetDeploymentModel(), now)

	if err != nil {
		s.logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to create tenant")
		return nil, status.Errorf(codes.Internal, "failed to create tenant: %v", err)
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
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		s.logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to commit transaction for tenant creation and auto-subscription")
		return nil, status.Errorf(codes.Internal, "failed to commit tenant creation: %v", err)
	}

	changedFields := []string{"name"}
	if req.GetSubdomain() != "" {
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

	s.emitTenantEvent(ctx, eventTenantCreated, tenantID, userID, changedFields)
	if defaultClusterID.Valid {
		s.emitClusterEvent(ctx, eventTenantClusterAssigned, tenantID, userID, defaultClusterID.String, "cluster", defaultClusterID.String, "", "", "")
	}

	tenant := &pb.Tenant{
		Id:                    tenantID,
		Name:                  name,
		Subdomain:             req.Subdomain,
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

	return &pb.CreateTenantResponse{Tenant: tenant}, nil
}

// UpdateTenant updates a tenant's properties
func (s *QuartermasterServer) UpdateTenant(ctx context.Context, req *pb.UpdateTenantRequest) (*pb.Tenant, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	userID := middleware.GetUserID(ctx)
	var updates []string
	var args []interface{}
	argIdx := 1
	changedFields := []string{}
	var previousClusterID sql.NullString
	if req.PrimaryClusterId != nil {
		_ = s.db.QueryRowContext(ctx, `SELECT primary_cluster_id FROM quartermaster.tenants WHERE id = $1`, tenantID).Scan(&previousClusterID)
	}

	if req.Name != nil {
		updates = append(updates, fmt.Sprintf("name = $%d", argIdx))
		args = append(args, *req.Name)
		argIdx++
		changedFields = append(changedFields, "name")
	}
	if req.Subdomain != nil {
		updates = append(updates, fmt.Sprintf("subdomain = $%d", argIdx))
		args = append(args, *req.Subdomain)
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

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update tenant: %v", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, status.Error(codes.NotFound, "tenant not found")
	}

	if len(changedFields) > 0 {
		s.emitTenantEvent(ctx, eventTenantUpdated, tenantID, userID, changedFields)
	}
	if req.PrimaryClusterId != nil {
		newCluster := strings.TrimSpace(*req.PrimaryClusterId)
		if newCluster != "" && (!previousClusterID.Valid || previousClusterID.String != newCluster) {
			s.emitClusterEvent(ctx, eventTenantClusterAssigned, tenantID, userID, newCluster, "cluster", newCluster, "", "", "")
		} else if newCluster == "" && previousClusterID.Valid {
			s.emitClusterEvent(ctx, eventTenantClusterUnassigned, tenantID, userID, previousClusterID.String, "cluster", previousClusterID.String, "", "", "")
		}
	}

	// Fetch updated tenant
	resp, err := s.GetTenant(ctx, &pb.GetTenantRequest{TenantId: tenantID})
	if err != nil {
		return nil, err
	}
	return resp.Tenant, nil
}

// DeleteTenant soft deletes a tenant
func (s *QuartermasterServer) DeleteTenant(ctx context.Context, req *pb.DeleteTenantRequest) (*emptypb.Empty, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	userID := middleware.GetUserID(ctx)
	result, err := s.db.ExecContext(ctx, `
		UPDATE quartermaster.tenants SET is_active = FALSE, updated_at = NOW()
		WHERE id = $1 AND is_active = TRUE
	`, tenantID)
	if err != nil {
		s.logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to delete tenant")
		return nil, status.Errorf(codes.Internal, "failed to delete tenant: %v", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, status.Error(codes.NotFound, "tenant not found")
	}

	s.logger.WithField("tenant_id", tenantID).Info("Deleted tenant successfully")
	s.emitTenantEvent(ctx, eventTenantDeleted, tenantID, userID, nil)
	return &emptypb.Empty{}, nil
}

// GetTenantCluster returns cluster/deployment info for a tenant
func (s *QuartermasterServer) GetTenantCluster(ctx context.Context, req *pb.GetTenantClusterRequest) (*pb.GetTenantResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	var tenant pb.Tenant
	var createdAt, updatedAt time.Time
	var subdomain, customDomain, logoURL, primaryClusterID, kafkaTopicPrefix, databaseURL sql.NullString
	var kafkaBrokers []string

	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, subdomain, custom_domain, logo_url, primary_color, secondary_color,
		       deployment_tier, deployment_model,
		       primary_cluster_id, kafka_topic_prefix, kafka_brokers, database_url,
		       is_active, created_at, updated_at
		FROM quartermaster.tenants
		WHERE id = $1 AND is_active = TRUE
	`, tenantID).Scan(
		&tenant.Id, &tenant.Name, &subdomain, &customDomain, &logoURL,
		&tenant.PrimaryColor, &tenant.SecondaryColor,
		&tenant.DeploymentTier, &tenant.DeploymentModel,
		&primaryClusterID, &kafkaTopicPrefix,
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
	if kafkaTopicPrefix.Valid {
		tenant.KafkaTopicPrefix = &kafkaTopicPrefix.String
	}
	if databaseURL.Valid {
		tenant.DatabaseUrl = &databaseURL.String
	}
	tenant.KafkaBrokers = kafkaBrokers
	tenant.CreatedAt = timestamppb.New(createdAt)
	tenant.UpdatedAt = timestamppb.New(updatedAt)

	return &pb.GetTenantResponse{Tenant: &tenant}, nil
}

// UpdateTenantCluster updates the cluster routing info for a tenant
func (s *QuartermasterServer) UpdateTenantCluster(ctx context.Context, req *pb.UpdateTenantClusterRequest) (*emptypb.Empty, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	userID := middleware.GetUserID(ctx)
	var updates []string
	var args []interface{}
	argIdx := 1
	changedFields := []string{}
	var previousClusterID sql.NullString
	if req.PrimaryClusterId != nil {
		_ = s.db.QueryRowContext(ctx, `SELECT primary_cluster_id FROM quartermaster.tenants WHERE id = $1`, tenantID).Scan(&previousClusterID)
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

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update tenant cluster: %v", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, status.Error(codes.NotFound, "tenant not found")
	}

	s.logger.WithField("tenant_id", tenantID).Info("Updated tenant cluster successfully")
	if len(changedFields) > 0 {
		s.emitTenantEvent(ctx, eventTenantUpdated, tenantID, userID, changedFields)
	}
	if req.PrimaryClusterId != nil {
		newCluster := strings.TrimSpace(*req.PrimaryClusterId)
		if newCluster != "" && (!previousClusterID.Valid || previousClusterID.String != newCluster) {
			s.emitClusterEvent(ctx, eventTenantClusterAssigned, tenantID, userID, newCluster, "cluster", newCluster, "", "", "")
		} else if newCluster == "" && previousClusterID.Valid {
			s.emitClusterEvent(ctx, eventTenantClusterUnassigned, tenantID, userID, previousClusterID.String, "cluster", previousClusterID.String, "", "", "")
		}
	}
	return &emptypb.Empty{}, nil
}

// GetTenantsBatch retrieves multiple tenants by IDs
func (s *QuartermasterServer) GetTenantsBatch(ctx context.Context, req *pb.GetTenantsBatchRequest) (*pb.ListTenantsResponse, error) {
	tenantIDs := req.GetTenantIds()
	if len(tenantIDs) == 0 {
		return nil, status.Error(codes.InvalidArgument, "tenant_ids required")
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, subdomain, custom_domain, logo_url, primary_color, secondary_color,
		       deployment_tier, deployment_model,
		       primary_cluster_id, kafka_topic_prefix, kafka_brokers, database_url,
		       is_active, created_at, updated_at
		FROM quartermaster.tenants
		WHERE id = ANY($1) AND is_active = TRUE
	`, pq.Array(tenantIDs))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var tenants []*pb.Tenant
	for rows.Next() {
		var tenant pb.Tenant
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

	return &pb.ListTenantsResponse{Tenants: tenants}, nil
}

// GetTenantsByCluster retrieves all tenants assigned to a specific cluster
func (s *QuartermasterServer) GetTenantsByCluster(ctx context.Context, req *pb.GetTenantsByClusterRequest) (*pb.GetTenantsByClusterResponse, error) {
	clusterID := req.GetClusterId()
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.name, t.subdomain, t.custom_domain, t.logo_url, t.primary_color, t.secondary_color,
		       t.deployment_tier, t.deployment_model,
		       t.primary_cluster_id, t.kafka_topic_prefix, t.kafka_brokers, t.database_url,
		       t.is_active, t.created_at, t.updated_at
		FROM quartermaster.tenants t
		LEFT JOIN quartermaster.tenant_cluster_assignments tca ON t.id = tca.tenant_id
		WHERE (t.primary_cluster_id = $1 OR tca.cluster_id = $1) AND t.is_active = TRUE
	`, clusterID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var tenants []*pb.Tenant
	for rows.Next() {
		var tenant pb.Tenant
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

	return &pb.GetTenantsByClusterResponse{
		ClusterId: clusterID,
		Tenants:   tenants,
	}, nil
}

// ============================================================================
// CLUSTER SERVICE
// ============================================================================

// GetCluster returns a specific cluster
func (s *QuartermasterServer) GetCluster(ctx context.Context, req *pb.GetClusterRequest) (*pb.ClusterResponse, error) {
	clusterID := req.GetClusterId()
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}

	cluster, err := s.queryCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}

	return &pb.ClusterResponse{Cluster: cluster}, nil
}

// ListClusters returns all clusters with pagination
func (s *QuartermasterServer) ListClusters(ctx context.Context, req *pb.ListClustersRequest) (*pb.ListClustersResponse, error) {
	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	tenantID := middleware.GetTenantID(ctx)
	ownerTenantID := strings.TrimSpace(req.GetOwnerTenantId())

	builder := &pagination.KeysetBuilder{
		TimestampColumn: "created_at",
		IDColumn:        "id",
	}

	// Base WHERE clause for filtering by subscription or ownership
	baseWhere := ""
	baseCountArgs := []interface{}{}

	if ownerTenantID != "" {
		baseWhere = `
			WHERE c.owner_tenant_id = $1
		`
		baseCountArgs = append(baseCountArgs, ownerTenantID)
	} else if tenantID != "" {
		baseWhere = `
			WHERE (c.cluster_id IN (
				SELECT tca.cluster_id FROM quartermaster.tenant_cluster_access tca
				WHERE tca.tenant_id = $1 AND tca.is_active = true
			) OR c.owner_tenant_id = $1)
		`
		baseCountArgs = append(baseCountArgs, tenantID)
	} else {
		baseWhere = `
			WHERE c.is_default_cluster = true
		`
	}

	// Build WHERE clause for filters
	where := ""
	countWhere := ""
	args := append([]interface{}{}, baseCountArgs...)
	countArgs := append([]interface{}{}, baseCountArgs...)
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

	// Get total count
	var total int32
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM quartermaster.infrastructure_clusters c %s %s`, baseWhere, countWhere)
	if countErr := s.db.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total); countErr != nil {
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
		       c.current_stream_count, c.current_viewer_count, c.current_bandwidth_mbps,
		       c.health_status, c.is_active, c.is_default_cluster, c.created_at, c.updated_at
		FROM quartermaster.infrastructure_clusters c
		%s %s
		%s
		LIMIT $%d
	`, baseWhere, where, builder.OrderBy(params), argIdx+len(args)-len(countArgs)) // Adjusted argIdx for LIMIT

	// Append limit arg
	args = append(args, params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var clusters []*pb.InfrastructureCluster
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
	resp := &pb.ListClustersResponse{
		Clusters: clusters,
		Pagination: &pb.CursorPaginationResponse{
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
func (s *QuartermasterServer) CreateCluster(ctx context.Context, req *pb.CreateClusterRequest) (*pb.ClusterResponse, error) {
	clusterID := req.GetClusterId()
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
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

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO quartermaster.infrastructure_clusters (id, cluster_id, cluster_name, cluster_type, deployment_model,
		                                                   owner_tenant_id, base_url, database_url, periscope_url, kafka_brokers,
		                                                   max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps,
		                                                   health_status, is_active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, '')::uuid, $7, $8, $9, $10, $11, $12, $13, 'healthy', true, $14, $14)
	`, id, clusterID, req.GetClusterName(), req.GetClusterType(), deploymentModel,
		ownerTenantID, req.GetBaseUrl(),
		req.DatabaseUrl, req.PeriscopeUrl, pq.Array(req.GetKafkaBrokers()),
		req.GetMaxConcurrentStreams(), req.GetMaxConcurrentViewers(), req.GetMaxBandwidthMbps(), now)

	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create cluster: %v", err)
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

	return &pb.ClusterResponse{Cluster: cluster}, nil
}

// UpdateCluster updates a cluster's properties
func (s *QuartermasterServer) UpdateCluster(ctx context.Context, req *pb.UpdateClusterRequest) (*pb.ClusterResponse, error) {
	clusterID := req.GetClusterId()
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}

	userID := middleware.GetUserID(ctx)
	var updates []string
	var args []interface{}
	argIdx := 1

	if req.ClusterName != nil {
		updates = append(updates, fmt.Sprintf("cluster_name = $%d", argIdx))
		args = append(args, *req.ClusterName)
		argIdx++
	}
	if req.BaseUrl != nil {
		updates = append(updates, fmt.Sprintf("base_url = $%d", argIdx))
		args = append(args, *req.BaseUrl)
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
	if req.CurrentStreamCount != nil {
		updates = append(updates, fmt.Sprintf("current_stream_count = $%d", argIdx))
		args = append(args, *req.CurrentStreamCount)
		argIdx++
	}
	if req.CurrentViewerCount != nil {
		updates = append(updates, fmt.Sprintf("current_viewer_count = $%d", argIdx))
		args = append(args, *req.CurrentViewerCount)
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

	rows, _ := result.RowsAffected()
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

	return &pb.ClusterResponse{Cluster: cluster}, nil
}

// ListClustersForTenant returns clusters accessible to a tenant
func (s *QuartermasterServer) ListClustersForTenant(ctx context.Context, req *pb.ListClustersForTenantRequest) (*pb.ClustersAccessResponse, error) {
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
	args := []interface{}{tenantID}
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

	var clusters []*pb.ClusterAccessEntry
	type entryWithCursor struct {
		entry     *pb.ClusterAccessEntry
		createdAt time.Time
		id        string
	}
	var entries []entryWithCursor
	for rows.Next() {
		var entry pb.ClusterAccessEntry
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

	return &pb.ClustersAccessResponse{
		Clusters:   clusters,
		Pagination: pagination.BuildResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
	}, nil
}

// ListClustersAvailable returns clusters available for tenant onboarding
func (s *QuartermasterServer) ListClustersAvailable(ctx context.Context, req *pb.ListClustersAvailableRequest) (*pb.ClustersAvailableResponse, error) {
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
	var args []interface{}
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
		entry     *pb.AvailableClusterEntry
		createdAt time.Time
		clusterID string
	}
	var entries []entryWithCursor
	for rows.Next() {
		var entry pb.AvailableClusterEntry
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
	var clusters []*pb.AvailableClusterEntry
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

	return &pb.ClustersAvailableResponse{
		Clusters:   clusters,
		Pagination: pagination.BuildResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
	}, nil
}

// GrantClusterAccess grants a tenant access to a cluster
func (s *QuartermasterServer) GrantClusterAccess(ctx context.Context, req *pb.GrantClusterAccessRequest) (*emptypb.Empty, error) {
	tenantID := req.GetTenantId()
	clusterID := req.GetClusterId()

	if tenantID == "" || clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and cluster_id required")
	}

	accessLevel := req.GetAccessLevel()
	if accessLevel == "" {
		accessLevel = "read"
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO quartermaster.tenant_cluster_access (tenant_id, cluster_id, access_level, expires_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NOW(), NOW())
		ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
			access_level = EXCLUDED.access_level,
			expires_at = EXCLUDED.expires_at,
			updated_at = NOW()
	`, tenantID, clusterID, accessLevel, req.GetExpiresAt())

	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to grant access: %v", err)
	}

	return &emptypb.Empty{}, nil
}

// SubscribeToCluster subscribes a tenant to a public/shared cluster
func (s *QuartermasterServer) SubscribeToCluster(ctx context.Context, req *pb.SubscribeToClusterRequest) (*emptypb.Empty, error) {
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

	// Create subscription
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO quartermaster.tenant_cluster_access
		(tenant_id, cluster_id, access_level, is_active, created_at, updated_at)
		VALUES ($1, $2, 'subscriber', true, NOW(), NOW())
		ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
			is_active = true,
			updated_at = NOW()
	`, tenantID, clusterID)

	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to subscribe: %v", err)
	}

	return &emptypb.Empty{}, nil
}

// UnsubscribeFromCluster unsubscribes a tenant from a cluster
func (s *QuartermasterServer) UnsubscribeFromCluster(ctx context.Context, req *pb.UnsubscribeFromClusterRequest) (*emptypb.Empty, error) {
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

	_, err := s.db.ExecContext(ctx, `
		UPDATE quartermaster.tenant_cluster_access
		SET is_active = false, updated_at = NOW()
		WHERE tenant_id = $1 AND cluster_id = $2
	`, tenantID, clusterID)

	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to unsubscribe: %v", err)
	}

	return &emptypb.Empty{}, nil
}

// ListMySubscriptions lists clusters the tenant is subscribed to
func (s *QuartermasterServer) ListMySubscriptions(ctx context.Context, req *pb.ListMySubscriptionsRequest) (*pb.ListClustersResponse, error) {
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
		TimestampColumn: "created_at",
		IDColumn:        "id",
	}

	baseWhere := `
		WHERE c.cluster_id IN (
			SELECT cluster_id FROM quartermaster.tenant_cluster_access
			WHERE tenant_id = $1 AND is_active = true
		)
	`
	args := []interface{}{tenantID}
	argIdx := 2

	// Get total count
	var total int32
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM quartermaster.infrastructure_clusters c %s`, baseWhere)
	if countErr := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); countErr != nil {
		s.logger.WithError(countErr).WithField("tenant_id", tenantID).Error("ListMySubscriptions: count query failed")
		return nil, status.Errorf(codes.Internal, "database error: %v", countErr)
	}
	s.logger.WithFields(map[string]interface{}{
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
		       c.current_stream_count, c.current_viewer_count, c.current_bandwidth_mbps,
		       c.health_status, c.is_active, c.is_default_cluster, c.created_at, c.updated_at
		FROM quartermaster.infrastructure_clusters c
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

	var clusters []*pb.InfrastructureCluster
	for rows.Next() {
		cluster, err := scanCluster(rows)
		if err != nil {
			continue
		}
		clusters = append(clusters, cluster)
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

	return &pb.ListClustersResponse{
		Clusters:   clusters,
		Pagination: pagination.BuildResponse(resultsLen, params.Limit, params.Direction, int32(len(clusters)), startCursor, endCursor),
	}, nil
}

// GetNode returns a specific node
func (s *QuartermasterServer) GetNode(ctx context.Context, req *pb.GetNodeRequest) (*pb.NodeResponse, error) {
	nodeID := req.GetNodeId()
	if nodeID == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id required")
	}

	node, err := s.queryNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}

	return &pb.NodeResponse{Node: node}, nil
}

// GetNodeByLogicalName resolves a node by its logical name (node_id string like "edge-node-1")
// Used by Foghorn to get the database UUID for subscription enrichment
func (s *QuartermasterServer) GetNodeByLogicalName(ctx context.Context, req *pb.GetNodeByLogicalNameRequest) (*pb.NodeResponse, error) {
	nodeID := req.GetNodeId()
	if nodeID == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id required")
	}

	node, err := s.queryNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}

	return &pb.NodeResponse{Node: node}, nil
}

// UpdateNodeHardware updates the hardware specs for a node (detected at startup by Helmsman)
// Called by Foghorn when processing Register message with hardware info
func (s *QuartermasterServer) UpdateNodeHardware(ctx context.Context, req *pb.UpdateNodeHardwareRequest) (*emptypb.Empty, error) {
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

	rows, _ := result.RowsAffected()
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

// ListNodes returns nodes with optional filters
func (s *QuartermasterServer) ListNodes(ctx context.Context, req *pb.ListNodesRequest) (*pb.ListNodesResponse, error) {
	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	tenantID := middleware.GetTenantID(ctx)

	builder := &pagination.KeysetBuilder{
		TimestampColumn: "created_at",
		IDColumn:        "id",
	}

	// Base WHERE clause to secure visibility
	baseWhere := ""
	baseArgs := []interface{}{}

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
	} else {
		// Unauthenticated: Default cluster only
		baseWhere = `
			WHERE n.cluster_id IN (
				SELECT c.cluster_id FROM quartermaster.infrastructure_clusters c
				WHERE c.is_default_cluster = true
			)
		`
	}

	// Build WHERE clause for filters
	where := baseWhere
	countWhere := baseWhere
	args := append([]interface{}{}, baseArgs...)
	countArgs := append([]interface{}{}, baseArgs...)
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
		       wireguard_ip, wireguard_public_key, region, availability_zone,
		       latitude, longitude, cpu_cores, memory_gb, disk_gb,
		       last_heartbeat, created_at, updated_at
		FROM quartermaster.infrastructure_nodes n
		%s
		%s
		LIMIT $%d
	`, where, builder.OrderBy(params), argIdx+len(args)-len(countArgs)) // Use next available index for limit

	// Append limit
	args = append(args, params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var nodes []*pb.InfrastructureNode
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to scan node")
			continue
		}
		nodes = append(nodes, node)
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
	resp := &pb.ListNodesResponse{
		Nodes:     nodes,
		ClusterId: req.GetClusterId(),
		NodeType:  req.GetNodeType(),
		Region:    req.GetRegion(),
		Pagination: &pb.CursorPaginationResponse{
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

// ListHealthyNodesForDNS returns healthy nodes for DNS sync
func (s *QuartermasterServer) ListHealthyNodesForDNS(ctx context.Context, req *pb.ListHealthyNodesForDNSRequest) (*pb.ListHealthyNodesForDNSResponse, error) {
	tenantID := middleware.GetTenantID(ctx)

	baseWhere := ""
	baseArgs := []interface{}{}

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
	} else {
		baseWhere = `
			WHERE n.cluster_id IN (
				SELECT c.cluster_id FROM quartermaster.infrastructure_clusters c
				WHERE c.is_default_cluster = true
			)
		`
	}

	staleThreshold := req.GetStaleThresholdSeconds()
	if staleThreshold <= 0 {
		staleThreshold = 300
	}

	where := baseWhere
	args := append([]interface{}{}, baseArgs...)
	argIdx := len(baseArgs) + 1

	if req.GetNodeType() != "" {
		where += fmt.Sprintf(" AND n.node_type = $%d", argIdx)
		args = append(args, req.GetNodeType())
		argIdx++
	}

	where += " AND n.external_ip IS NOT NULL AND n.external_ip <> ''"

	totalQuery := fmt.Sprintf(`SELECT COUNT(*) FROM quartermaster.infrastructure_nodes n %s`, where)
	var totalNodes int32
	if err := s.db.QueryRowContext(ctx, totalQuery, args...).Scan(&totalNodes); err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	healthArgs := append([]interface{}{}, args...)
	healthArgs = append(healthArgs, staleThreshold)
	healthArgIdx := argIdx
	healthWhere := where + fmt.Sprintf(" AND si.health_status = 'healthy' AND si.last_health_check > NOW() - ($%d * INTERVAL '1 second')", healthArgIdx)

	healthyCountQuery := fmt.Sprintf(`
		SELECT COUNT(DISTINCT n.id)
		FROM quartermaster.infrastructure_nodes n
		JOIN quartermaster.service_instances si
			ON si.cluster_id = n.cluster_id
			AND (
				si.node_id = n.node_id
				OR si.advertise_host = n.external_ip
				OR si.advertise_host = n.internal_ip
			)
		%s
	`, healthWhere)

	var healthyNodes int32
	if err := s.db.QueryRowContext(ctx, healthyCountQuery, healthArgs...).Scan(&healthyNodes); err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	query := fmt.Sprintf(`
		SELECT DISTINCT n.id, n.node_id, n.cluster_id, n.node_name, n.node_type, n.internal_ip, n.external_ip,
		       n.wireguard_ip, n.wireguard_public_key, n.region, n.availability_zone,
		       n.latitude, n.longitude, n.cpu_cores, n.memory_gb, n.disk_gb,
		       n.last_heartbeat, n.created_at, n.updated_at
		FROM quartermaster.infrastructure_nodes n
		JOIN quartermaster.service_instances si
			ON si.cluster_id = n.cluster_id
			AND (
				si.node_id = n.node_id
				OR si.advertise_host = n.external_ip
				OR si.advertise_host = n.internal_ip
			)
		%s
	`, healthWhere)

	rows, err := s.db.QueryContext(ctx, query, healthArgs...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var nodes []*pb.InfrastructureNode
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to scan node")
			continue
		}
		nodes = append(nodes, node)
	}

	return &pb.ListHealthyNodesForDNSResponse{
		Nodes:        nodes,
		TotalNodes:   totalNodes,
		HealthyNodes: healthyNodes,
	}, nil
}

// CreateNode creates a new node
func (s *QuartermasterServer) CreateNode(ctx context.Context, req *pb.CreateNodeRequest) (*pb.NodeResponse, error) {
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

	id := uuid.New().String()
	now := time.Now()

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO quartermaster.infrastructure_nodes (id, node_id, cluster_id, node_name, node_type,
		                                                internal_ip, external_ip, wireguard_ip, wireguard_public_key,
		                                                region, availability_zone, latitude, longitude,
		                                                cpu_cores, memory_gb, disk_gb, status,
		                                                created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, 'active', $17, $17)
	`, id, nodeID, clusterID, req.GetNodeName(), req.GetNodeType(),
		req.InternalIp, req.ExternalIp, req.WireguardIp, req.WireguardPublicKey,
		req.Region, req.AvailabilityZone, req.Latitude, req.Longitude,
		req.CpuCores, req.MemoryGb, req.DiskGb, now)

	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create node: %v", err)
	}

	node, err := s.queryNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}

	// Trigger DNS sync for the relevant service type (async, best-effort)
	nodeType := req.GetNodeType()
	if s.navigatorClient != nil && nodeType != "" {
		syncReq := &pb.SyncDNSRequest{
			ServiceType: nodeType,
			RootDomain:  config.GetEnv("BRAND_DOMAIN", "frameworks.network"),
		}
		go func() {
			syncCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if resp, err := s.navigatorClient.SyncDNS(syncCtx, syncReq); err != nil {
				s.logger.WithError(err).WithField("service_type", nodeType).Error("Failed to trigger DNS sync")
			} else if !resp.GetSuccess() {
				s.logger.WithFields(logging.Fields{
					"service_type": nodeType,
					"message":      resp.GetMessage(),
					"errors":       resp.GetErrors(),
				}).Error("DNS sync failed via Navigator")
			} else {
				s.logger.WithField("service_type", nodeType).Info("DNS sync triggered successfully")
			}
		}()
	}

	return &pb.NodeResponse{Node: node}, nil
}

// UpdateNodeHealth removed - health tracking moved to Lookout

// ResolveNodeFingerprint resolves a node identity from fingerprint data.
// Lookup priority:
// 1. Exact match by machine_id_sha256
// 2. Match by macs_sha256
// 3. Match by peer_ip in seen_ips array
// On match, updates seen_ips with current peer_ip.
// Returns NotFound if no match - does not create new mappings to avoid bypassing enrollment.
func (s *QuartermasterServer) ResolveNodeFingerprint(ctx context.Context, req *pb.ResolveNodeFingerprintRequest) (*pb.ResolveNodeFingerprintResponse, error) {
	peerIP := req.GetPeerIp()
	if peerIP == "" {
		return nil, status.Error(codes.InvalidArgument, "peer_ip required")
	}

	var tenantID, nodeID string

	// 1) Try exact match by machine_id_sha256
	machineIDSHA := req.GetMachineIdSha256()
	if machineIDSHA != "" {
		err := s.db.QueryRowContext(ctx, `
			SELECT tenant_id::text, node_id
			FROM quartermaster.node_fingerprints
			WHERE fingerprint_machine_sha256 = $1
		`, machineIDSHA).Scan(&tenantID, &nodeID)
		if err == nil {
			_ = s.upsertSeenIP(ctx, nodeID, peerIP)
			return &pb.ResolveNodeFingerprintResponse{
				TenantId:        tenantID,
				CanonicalNodeId: nodeID,
			}, nil
		}
	}

	// 2) Match by macs_sha256
	macsSHA := req.GetMacsSha256()
	if macsSHA != "" {
		err := s.db.QueryRowContext(ctx, `
			SELECT tenant_id::text, node_id
			FROM quartermaster.node_fingerprints
			WHERE fingerprint_macs_sha256 = $1
		`, macsSHA).Scan(&tenantID, &nodeID)
		if err == nil {
			_ = s.upsertSeenIP(ctx, nodeID, peerIP)
			return &pb.ResolveNodeFingerprintResponse{
				TenantId:        tenantID,
				CanonicalNodeId: nodeID,
			}, nil
		}
	}

	// 3) Match by peer_ip in seen_ips array
	err := s.db.QueryRowContext(ctx, `
		SELECT tenant_id::text, node_id
		FROM quartermaster.node_fingerprints
		WHERE $1::inet = ANY(seen_ips)
		ORDER BY last_seen DESC
		LIMIT 1
	`, peerIP).Scan(&tenantID, &nodeID)
	if err == nil {
		_ = s.upsertSeenIP(ctx, nodeID, peerIP)
		return &pb.ResolveNodeFingerprintResponse{
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

// ============================================================================
// BOOTSTRAP SERVICE - Additional Methods
// ============================================================================

// BootstrapEdgeNode registers an edge node using a bootstrap token
func (s *QuartermasterServer) BootstrapEdgeNode(ctx context.Context, req *pb.BootstrapEdgeNodeRequest) (*pb.BootstrapEdgeNodeResponse, error) {
	token := req.GetToken()
	if token == "" {
		return nil, status.Error(codes.InvalidArgument, "token required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to begin transaction: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort after commit

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
		WHERE token = $1 AND kind = 'edge_node'
		  AND (
		    (usage_limit IS NULL AND used_at IS NULL) OR
		    (usage_limit IS NOT NULL AND usage_count < usage_limit)
		  )
		FOR UPDATE
	`, token).Scan(&tokenID, &tenantID, &clusterID, &usageLimit, &usageCount, &expiresAt, &expectedIP)

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

	// Generate node ID with UUID suffix for uniqueness
	nodeID := "edge-" + uuid.New().String()[:12]
	hostname := req.GetHostname()
	if hostname == "" {
		hostname = nodeID
	}

	// Create node
	_, err = tx.ExecContext(ctx, `
		INSERT INTO quartermaster.infrastructure_nodes (id, node_id, cluster_id, node_name, node_type, tags, metadata, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'edge', '{}', '{}', NOW(), NOW())
	`, uuid.New().String(), nodeID, resolvedClusterID, hostname)

	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create node: %v", err)
	}

	// Insert node fingerprint if fingerprint data provided
	machineIDSHA := req.GetMachineIdSha256()
	macsSHA := req.GetMacsSha256()
	ips := req.GetIps()
	labels := req.GetLabels()

	hasLabels := labels != nil && len(labels.GetFields()) > 0
	if machineIDSHA != "" || macsSHA != "" || len(ips) > 0 || hasLabels {
		attrsJSON := "{}"
		if hasLabels {
			if attrsBytes, marshalErr := json.Marshal(labels.AsMap()); marshalErr == nil {
				attrsJSON = string(attrsBytes)
			}
		}

		var ipsLiteral interface{} = nil
		if len(ips) > 0 {
			ipsLiteral = "{" + strings.Join(ips, ",") + "}"
		}

		_, err = tx.ExecContext(ctx, `
			INSERT INTO quartermaster.node_fingerprints (tenant_id, node_id, fingerprint_machine_sha256, fingerprint_macs_sha256, seen_ips, attrs)
			VALUES ($1, $2, NULLIF($3,''), NULLIF($4,''), $5::inet[], $6)
			ON CONFLICT (node_id) DO UPDATE SET
				last_seen = NOW(),
				seen_ips = quartermaster.node_fingerprints.seen_ips || EXCLUDED.seen_ips
		`, tenantID.String, nodeID, machineIDSHA, macsSHA, ipsLiteral, attrsJSON)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to upsert node fingerprint: %v", err)
		}
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

	nodeType := "edge"
	if s.navigatorClient != nil {
		syncReq := &pb.SyncDNSRequest{
			ServiceType: nodeType,
			RootDomain:  config.GetEnv("BRAND_DOMAIN", "frameworks.network"),
		}
		go func() {
			syncCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if resp, err := s.navigatorClient.SyncDNS(syncCtx, syncReq); err != nil {
				s.logger.WithError(err).WithField("service_type", nodeType).Error("Failed to trigger DNS sync after bootstrap")
			} else if !resp.GetSuccess() {
				s.logger.WithFields(logging.Fields{
					"service_type": nodeType,
					"message":      resp.GetMessage(),
					"errors":       resp.GetErrors(),
				}).Error("DNS sync failed via Navigator after bootstrap")
			} else {
				s.logger.WithField("service_type", nodeType).Info("DNS sync triggered successfully after bootstrap")
			}
		}()
	}

	return &pb.BootstrapEdgeNodeResponse{
		NodeId:    nodeID,
		TenantId:  tenantID.String,
		ClusterId: resolvedClusterID,
	}, nil
}

// BootstrapInfrastructureNode registers a general infrastructure node using a bootstrap token
func (s *QuartermasterServer) BootstrapInfrastructureNode(ctx context.Context, req *pb.BootstrapInfrastructureNodeRequest) (*pb.BootstrapInfrastructureNodeResponse, error) {
	token := req.GetToken()
	if token == "" {
		return nil, status.Error(codes.InvalidArgument, "token required")
	}
	if req.GetNodeType() == "" {
		return nil, status.Error(codes.InvalidArgument, "node_type required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to begin transaction: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort after commit

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
		WHERE token = $1 AND kind = 'infrastructure_node'
		  AND (
		    (usage_limit IS NULL AND used_at IS NULL) OR
		    (usage_limit IS NOT NULL AND usage_count < usage_limit)
		  )
		FOR UPDATE
	`, token).Scan(&tokenID, &tenantID, &clusterID, &usageLimit, &usageCount, &expiresAt, &expectedIP)
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

	// Idempotent: if node already exists, return existing cluster binding
	var existingClusterID string
	err = tx.QueryRowContext(ctx, `
		SELECT cluster_id FROM quartermaster.infrastructure_nodes WHERE node_id = $1
	`, nodeID).Scan(&existingClusterID)
	if err == nil {
		if existingClusterID != resolvedClusterID {
			return nil, status.Errorf(codes.FailedPrecondition, "node already exists in cluster %s", existingClusterID)
		}
		var tenantResp *string
		if tenantID.Valid && tenantID.String != "" {
			tenantResp = &tenantID.String
		}
		return &pb.BootstrapInfrastructureNodeResponse{
			NodeId:    nodeID,
			TenantId:  tenantResp,
			ClusterId: resolvedClusterID,
		}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Create node with 'active' status
	var extIP interface{} = nil
	if req.ExternalIp != nil && *req.ExternalIp != "" {
		extIP = *req.ExternalIp
	}
	var intIP interface{} = nil
	if req.InternalIp != nil && *req.InternalIp != "" {
		intIP = *req.InternalIp
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO quartermaster.infrastructure_nodes (id, node_id, cluster_id, node_name, node_type, external_ip, internal_ip, tags, metadata, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6::inet, $7::inet, '{}', '{}', NOW(), NOW())
	`, uuid.New().String(), nodeID, resolvedClusterID, hostname, req.GetNodeType(), extIP, intIP)
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

	nodeType := req.GetNodeType()
	if s.navigatorClient != nil && nodeType != "" {
		syncReq := &pb.SyncDNSRequest{
			ServiceType: nodeType,
			RootDomain:  config.GetEnv("BRAND_DOMAIN", "frameworks.network"),
		}
		go func() {
			syncCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if resp, err := s.navigatorClient.SyncDNS(syncCtx, syncReq); err != nil {
				s.logger.WithError(err).WithField("service_type", nodeType).Error("Failed to trigger DNS sync after bootstrap")
			} else if !resp.GetSuccess() {
				s.logger.WithFields(logging.Fields{
					"service_type": nodeType,
					"message":      resp.GetMessage(),
					"errors":       resp.GetErrors(),
				}).Error("DNS sync failed via Navigator after bootstrap")
			} else {
				s.logger.WithField("service_type", nodeType).Info("DNS sync triggered successfully after bootstrap")
			}
		}()
	}

	return &pb.BootstrapInfrastructureNodeResponse{
		NodeId:    nodeID,
		TenantId:  tenantResp,
		ClusterId: resolvedClusterID,
	}, nil
}

// CreateBootstrapToken creates a new bootstrap token
func (s *QuartermasterServer) CreateBootstrapToken(ctx context.Context, req *pb.CreateBootstrapTokenRequest) (*pb.CreateBootstrapTokenResponse, error) {
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
	tokenValue := "bt_" + generateSecureToken(32)

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

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO quartermaster.bootstrap_tokens (id, name, token, kind, tenant_id, cluster_id, expected_ip, usage_limit, usage_count, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 0, $9, NOW())
	`, tokenID, name, tokenValue, kind, req.TenantId, req.ClusterId, req.ExpectedIp, req.UsageLimit, expiresAt)

	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create token: %v", err)
	}

	return &pb.CreateBootstrapTokenResponse{
		Token: &pb.BootstrapToken{
			Id:         tokenID,
			Name:       name,
			Token:      tokenValue,
			Kind:       kind,
			TenantId:   req.TenantId,
			ClusterId:  req.ClusterId,
			ExpectedIp: req.ExpectedIp,
			UsageLimit: req.UsageLimit,
			UsageCount: 0,
			ExpiresAt:  timestamppb.New(expiresAt),
			CreatedAt:  timestamppb.Now(),
		},
	}, nil
}

// ListBootstrapTokens returns bootstrap tokens with optional filters
func (s *QuartermasterServer) ListBootstrapTokens(ctx context.Context, req *pb.ListBootstrapTokensRequest) (*pb.ListBootstrapTokensResponse, error) {
	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid cursor: %v", err)
	}

	where := "WHERE 1=1"
	args := []interface{}{}
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
		SELECT id, name, token, kind, tenant_id, cluster_id, expected_ip, usage_limit, usage_count, expires_at, used_at, created_by, created_at
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

	var tokens []*pb.BootstrapToken
	for rows.Next() {
		var token pb.BootstrapToken
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

	return &pb.ListBootstrapTokensResponse{
		Tokens:     tokens,
		Pagination: pagination.BuildResponse(resultsLen, params.Limit, params.Direction, int32(len(tokens)), startCursor, endCursor),
	}, nil
}

// RevokeBootstrapToken revokes a bootstrap token
func (s *QuartermasterServer) RevokeBootstrapToken(ctx context.Context, req *pb.RevokeBootstrapTokenRequest) (*emptypb.Empty, error) {
	tokenID := req.GetTokenId()
	if tokenID == "" {
		return nil, status.Error(codes.InvalidArgument, "token_id required")
	}

	result, err := s.db.ExecContext(ctx, `DELETE FROM quartermaster.bootstrap_tokens WHERE id = $1`, tokenID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to revoke token: %v", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, status.Error(codes.NotFound, "token not found")
	}

	return &emptypb.Empty{}, nil
}

// ============================================================================
// MESH SERVICE
// ============================================================================

// SyncMesh handles WireGuard mesh synchronization
func (s *QuartermasterServer) SyncMesh(ctx context.Context, req *pb.InfrastructureSyncRequest) (*pb.InfrastructureSyncResponse, error) {
	nodeID := req.GetNodeId()
	publicKey := req.GetPublicKey()
	if nodeID == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id required")
	}

	// 1. Check if node exists and get current WireGuard IP
	var currentWgIP sql.NullString
	var externalIP, internalIP sql.NullString
	var clusterID string
	err := s.db.QueryRowContext(ctx, `
		SELECT wireguard_ip::text, external_ip::text, internal_ip::text, cluster_id
		FROM quartermaster.infrastructure_nodes
		WHERE node_id = $1
	`, nodeID).Scan(&currentWgIP, &externalIP, &internalIP, &clusterID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "node not found - please register the node first")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get node info: %v", err)
	}

	// 2. Update public key, listen port, and heartbeat if provided
	if publicKey != "" || req.GetListenPort() > 0 {
		_, err = s.db.ExecContext(ctx, `
			UPDATE quartermaster.infrastructure_nodes
			SET wireguard_public_key = COALESCE(NULLIF($1, ''), wireguard_public_key),
			    wireguard_listen_port = COALESCE(NULLIF($2, 0), wireguard_listen_port),
			    last_heartbeat = NOW(),
			    updated_at = NOW()
			WHERE node_id = $3
		`, publicKey, req.GetListenPort(), nodeID)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to update node public key")
		}
	}

	// 3. Allocate WireGuard IP if missing
	wireguardIP := ""
	if currentWgIP.Valid && currentWgIP.String != "" {
		wireguardIP = currentWgIP.String
	} else {
		// Allocate next IP in 10.200.0.0/16 range (transaction + advisory lock)
		tx, txErr := s.db.BeginTx(ctx, nil)
		if txErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to start allocation tx: %v", txErr)
		}
		defer tx.Rollback() //nolint:errcheck // rollback is best-effort after commit
		if _, lockErr := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "quartermaster_wireguard_ip"); lockErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to acquire allocation lock: %v", lockErr)
		}
		newIP, allocErr := s.allocateWireGuardIPTx(ctx, tx)
		if allocErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to allocate WireGuard IP: %v", allocErr)
		}
		if _, execErr := tx.ExecContext(ctx, `
			UPDATE quartermaster.infrastructure_nodes
			SET wireguard_ip = $1::inet, updated_at = NOW()
			WHERE node_id = $2
		`, newIP, nodeID); execErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to save WireGuard IP: %v", execErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to commit WireGuard IP allocation: %v", commitErr)
		}
		wireguardIP = newIP
	}

	// 4. Get all peer nodes (same cluster, active, with WireGuard configured)
	rows, err := s.db.QueryContext(ctx, `
		SELECT n.node_name, n.wireguard_public_key, n.external_ip::text, n.internal_ip::text, n.wireguard_ip::text, n.wireguard_listen_port
		FROM quartermaster.infrastructure_nodes n
		WHERE n.node_id != $1
		  AND n.wireguard_public_key IS NOT NULL
		  AND n.wireguard_ip IS NOT NULL
		  AND n.cluster_id = (SELECT cluster_id FROM quartermaster.infrastructure_nodes WHERE node_id = $1)
		  AND n.status = 'active'
	`, nodeID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var peers []*pb.InfrastructurePeer
	for rows.Next() {
		var peer pb.InfrastructurePeer
		var peerExtIP, peerIntIP, peerWgIP sql.NullString
		var peerListenPort sql.NullInt32
		if scanErr := rows.Scan(&peer.NodeName, &peer.PublicKey, &peerExtIP, &peerIntIP, &peerWgIP, &peerListenPort); scanErr != nil {
			continue
		}
		// Prefer external IP, fall back to internal IP
		endpoint := ""
		if peerExtIP.Valid && peerExtIP.String != "" {
			endpoint = peerExtIP.String
		} else if peerIntIP.Valid && peerIntIP.String != "" {
			endpoint = peerIntIP.String
		}
		if endpoint != "" && peerWgIP.Valid {
			port := int32(51820)
			if peerListenPort.Valid && peerListenPort.Int32 > 0 {
				port = peerListenPort.Int32
			}
			peer.Endpoint = fmt.Sprintf("%s:%d", endpoint, port)
			peer.AllowedIps = []string{peerWgIP.String + "/32"}
			peer.KeepAlive = 25
			peers = append(peers, &peer)
		}
	}

	// 5. Fetch service endpoints for DNS aliases
	serviceEndpoints := make(map[string]*pb.ServiceEndpoints)
	svcRows, err := s.db.QueryContext(ctx, `
		SELECT s.name, n.wireguard_ip::text
		FROM quartermaster.services s
		JOIN quartermaster.service_instances si ON si.service_id = s.service_id
		JOIN quartermaster.infrastructure_nodes n ON n.node_id = si.node_id
		WHERE si.status IN ('running', 'active')
		  AND n.wireguard_ip IS NOT NULL
		  AND n.status = 'active'
		  AND n.cluster_id = $1
	`, clusterID)
	if err == nil {
		defer func() { _ = svcRows.Close() }()
		for svcRows.Next() {
			var svcName, svcIP string
			if scanErr := svcRows.Scan(&svcName, &svcIP); scanErr == nil && svcIP != "" {
				if serviceEndpoints[svcName] == nil {
					serviceEndpoints[svcName] = &pb.ServiceEndpoints{Ips: []string{}}
				}
				serviceEndpoints[svcName].Ips = append(serviceEndpoints[svcName].Ips, svcIP)
			}
		}
	} else {
		s.logger.WithError(err).Warn("Failed to fetch service endpoints for DNS")
	}

	wireguardPort := int32(51820)
	if req.GetListenPort() > 0 {
		wireguardPort = req.GetListenPort()
	}

	return &pb.InfrastructureSyncResponse{
		WireguardIp:      wireguardIP,
		WireguardPort:    wireguardPort,
		Peers:            peers,
		ServiceEndpoints: serviceEndpoints,
	}, nil
}

// allocateWireGuardIPTx finds the next available IP in the 10.200.0.0/16 range
func (s *QuartermasterServer) allocateWireGuardIPTx(ctx context.Context, tx *sql.Tx) (string, error) {
	var maxIP sql.NullString
	err := tx.QueryRowContext(ctx, `
		SELECT wireguard_ip::text
		FROM quartermaster.infrastructure_nodes
		WHERE wireguard_ip IS NOT NULL
		ORDER BY wireguard_ip DESC
		LIMIT 1
	`).Scan(&maxIP)

	if errors.Is(err, sql.ErrNoRows) || !maxIP.Valid || maxIP.String == "" {
		return "10.200.0.1", nil
	}
	if err != nil {
		return "", err
	}

	// Parse and increment IP
	ip := net.ParseIP(maxIP.String)
	if ip == nil {
		return "", fmt.Errorf("invalid IP in database: %s", maxIP.String)
	}
	ip = ip.To4()
	if ip == nil {
		return "", fmt.Errorf("not an IPv4 address: %s", maxIP.String)
	}

	// Increment
	intIP := uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
	intIP++
	newIP := net.IPv4(byte(intIP>>24), byte(intIP>>16), byte(intIP>>8), byte(intIP))
	return newIP.String(), nil
}

// ============================================================================
// SERVICE REGISTRY SERVICE
// ============================================================================

// ListServices returns all services in the catalog
func (s *QuartermasterServer) ListServices(ctx context.Context, req *pb.ListServicesRequest) (*pb.ListServicesResponse, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, service_id, name, plane, description, default_port,
		       health_check_path, docker_image, version, dependencies,
		       tags, is_active, type, protocol, created_at, updated_at
		FROM quartermaster.services
		ORDER BY name
	`)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var services []*pb.Service
	for rows.Next() {
		var svc pb.Service
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
			var tagsMap map[string]interface{}
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

	return &pb.ListServicesResponse{Services: services}, nil
}

// ListClusterServices returns services assigned to a cluster
func (s *QuartermasterServer) ListClusterServices(ctx context.Context, req *pb.ListClusterServicesRequest) (*pb.ListClusterServicesResponse, error) {
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

	var services []*pb.ClusterServiceAssignment
	for rows.Next() {
		var svc pb.ClusterServiceAssignment
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
			var configMap map[string]interface{}
			if err := json.Unmarshal([]byte(configBlob.String), &configMap); err == nil {
				svc.ConfigBlob = mapToStruct(configMap)
			}
		}
		if envVars.Valid && envVars.String != "" {
			var envMap map[string]interface{}
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

	return &pb.ListClusterServicesResponse{
		ClusterId: clusterID,
		Services:  services,
	}, nil
}

// ListServiceInstances returns running service instances
func (s *QuartermasterServer) ListServiceInstances(ctx context.Context, req *pb.ListServiceInstancesRequest) (*pb.ListServiceInstancesResponse, error) {
	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	builder := &pagination.KeysetBuilder{
		TimestampColumn: "created_at",
		IDColumn:        "id",
	}

	// Build WHERE clause for filters
	where := "WHERE 1=1"
	countWhere := "WHERE 1=1"
	args := []interface{}{}
	countArgs := []interface{}{}
	argIdx := 1

	if req.GetClusterId() != "" {
		where += fmt.Sprintf(" AND cluster_id = $%d", argIdx)
		countWhere += fmt.Sprintf(" AND cluster_id = $%d", argIdx)
		args = append(args, req.GetClusterId())
		countArgs = append(countArgs, req.GetClusterId())
		argIdx++
	}
	if req.GetServiceId() != "" {
		where += fmt.Sprintf(" AND service_id = $%d", argIdx)
		countWhere += fmt.Sprintf(" AND service_id = $%d", argIdx)
		args = append(args, req.GetServiceId())
		countArgs = append(countArgs, req.GetServiceId())
		argIdx++
	}
	if req.GetNodeId() != "" {
		where += fmt.Sprintf(" AND node_id = $%d", argIdx)
		countWhere += fmt.Sprintf(" AND node_id = $%d", argIdx)
		args = append(args, req.GetNodeId())
		countArgs = append(countArgs, req.GetNodeId())
		argIdx++
	}

	// Get total count
	var total int32
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM quartermaster.service_instances %s`, countWhere)
	if countErr := s.db.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total); countErr != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", countErr)
	}

	// Add keyset condition if cursor provided
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		where += " AND " + condition
		args = append(args, cursorArgs...)
	}

	query := fmt.Sprintf(`
		SELECT id, instance_id, service_id, cluster_id, node_id, protocol, advertise_host, port,
		       health_endpoint_override, version, process_id, container_id, status, health_status,
		       started_at, stopped_at, last_health_check, created_at, updated_at
		FROM quartermaster.service_instances
		%s
		%s
		LIMIT %d
	`, where, builder.OrderBy(params), params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var instances []*pb.ServiceInstance
	for rows.Next() {
		var inst pb.ServiceInstance
		var nodeID, host, healthEndpoint, version, containerID sql.NullString
		var processID sql.NullInt32
		var startedAt, stoppedAt, lastHealthCheck sql.NullTime
		var createdAt, updatedAt time.Time

		err := rows.Scan(&inst.Id, &inst.InstanceId, &inst.ServiceId, &inst.ClusterId, &nodeID,
			&inst.Protocol, &host, &inst.Port, &healthEndpoint, &version, &processID, &containerID,
			&inst.Status, &inst.HealthStatus, &startedAt, &stoppedAt, &lastHealthCheck, &createdAt, &updatedAt)
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
	resp := &pb.ListServiceInstancesResponse{
		Instances: instances,
		ClusterId: req.GetClusterId(),
		ServiceId: req.GetServiceId(),
		NodeId:    req.GetNodeId(),
		Pagination: &pb.CursorPaginationResponse{
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

// ListServicesHealth returns health of all service instances
func (s *QuartermasterServer) ListServicesHealth(ctx context.Context, req *pb.ListServicesHealthRequest) (*pb.ListServicesHealthResponse, error) {
	return s.getServicesHealth(ctx, "")
}

// GetServiceHealth returns health of specific service instances
func (s *QuartermasterServer) GetServiceHealth(ctx context.Context, req *pb.GetServiceHealthRequest) (*pb.ListServicesHealthResponse, error) {
	return s.getServicesHealth(ctx, req.GetServiceId())
}

func (s *QuartermasterServer) getServicesHealth(ctx context.Context, serviceID string) (*pb.ListServicesHealthResponse, error) {
	where := "WHERE 1=1"
	args := []interface{}{}
	if serviceID != "" {
		where = "WHERE service_id = $1"
		args = append(args, serviceID)
	}

	query := fmt.Sprintf(`
		SELECT instance_id, service_id, cluster_id, protocol, advertise_host, port, health_endpoint_override, status, last_health_check
		FROM quartermaster.service_instances
		%s
		ORDER BY service_id, instance_id
	`, where)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var instances []*pb.ServiceInstanceHealth
	for rows.Next() {
		var inst pb.ServiceInstanceHealth
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

	return &pb.ListServicesHealthResponse{Instances: instances}, nil
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

func (s *QuartermasterServer) emitServiceEvent(ctx context.Context, event *pb.ServiceEvent) {
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

func (s *QuartermasterServer) emitTenantEvent(ctx context.Context, eventType, tenantID, userID string, changedFields []string) {
	payload := &pb.TenantEvent{
		TenantId:      tenantID,
		ChangedFields: changedFields,
	}
	event := &pb.ServiceEvent{
		EventType:    eventType,
		Timestamp:    timestamppb.Now(),
		Source:       "quartermaster",
		TenantId:     tenantID,
		UserId:       userID,
		ResourceType: "tenant",
		ResourceId:   tenantID,
		Payload:      &pb.ServiceEvent_TenantEvent{TenantEvent: payload},
	}
	s.emitServiceEvent(ctx, event)
}

func (s *QuartermasterServer) emitClusterEvent(ctx context.Context, eventType, tenantID, userID, clusterID, resourceType, resourceID, inviteID, subscriptionID, reason string) {
	payload := &pb.ClusterEvent{
		ClusterId:      clusterID,
		TenantId:       tenantID,
		InviteId:       inviteID,
		SubscriptionId: subscriptionID,
		Reason:         reason,
	}
	event := &pb.ServiceEvent{
		EventType:    eventType,
		Timestamp:    timestamppb.Now(),
		Source:       "quartermaster",
		TenantId:     tenantID,
		UserId:       userID,
		ResourceType: resourceType,
		ResourceId:   resourceID,
		Payload:      &pb.ServiceEvent_ClusterEvent{ClusterEvent: payload},
	}
	s.emitServiceEvent(ctx, event)
}

func scanTenant(rows *sql.Rows) (*pb.Tenant, error) {
	var tenant pb.Tenant
	var subdomain, customDomain, logoURL, primaryClusterID, kafkaTopicPrefix, databaseURL sql.NullString
	var kafkaBrokers []string
	var createdAt, updatedAt time.Time

	err := rows.Scan(
		&tenant.Id, &tenant.Name, &subdomain, &customDomain, &logoURL,
		&tenant.PrimaryColor, &tenant.SecondaryColor, &tenant.DeploymentTier,
		&tenant.DeploymentModel,
		&primaryClusterID, &kafkaTopicPrefix, pq.Array(&kafkaBrokers), &databaseURL,
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

func scanCluster(rows *sql.Rows) (*pb.InfrastructureCluster, error) {
	var cluster pb.InfrastructureCluster
	var ownerTenantID, databaseURL, periscopeURL sql.NullString
	var kafkaBrokers []string
	var createdAt, updatedAt time.Time

	err := rows.Scan(
		&cluster.Id, &cluster.ClusterId, &cluster.ClusterName, &cluster.ClusterType,
		&ownerTenantID, &cluster.DeploymentModel, &cluster.BaseUrl, &databaseURL, &periscopeURL,
		pq.Array(&kafkaBrokers), &cluster.MaxConcurrentStreams, &cluster.MaxConcurrentViewers,
		&cluster.MaxBandwidthMbps, &cluster.CurrentStreamCount, &cluster.CurrentViewerCount,
		&cluster.CurrentBandwidthMbps, &cluster.HealthStatus, &cluster.IsActive, &cluster.IsDefaultCluster,
		&createdAt, &updatedAt,
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

func scanNode(rows *sql.Rows) (*pb.InfrastructureNode, error) {
	var node pb.InfrastructureNode
	var internalIP, externalIP, wireguardIP, wireguardPubKey, region, az sql.NullString
	var latitude, longitude sql.NullFloat64
	var cpuCores, memoryGB, diskGB sql.NullInt32
	var lastHeartbeat sql.NullTime
	var createdAt, updatedAt time.Time

	err := rows.Scan(
		&node.Id, &node.NodeId, &node.ClusterId, &node.NodeName, &node.NodeType,
		&internalIP, &externalIP, &wireguardIP, &wireguardPubKey, &region, &az,
		&latitude, &longitude, &cpuCores, &memoryGB, &diskGB,
		&lastHeartbeat, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
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
	if latitude.Valid {
		node.Latitude = &latitude.Float64
	}
	if longitude.Valid {
		node.Longitude = &longitude.Float64
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

func (s *QuartermasterServer) queryCluster(ctx context.Context, clusterID string) (*pb.InfrastructureCluster, error) {
	// Note: pricing_model, monthly_price_cents, required_billing_tier now in Purser
	// Column order must match the Scan() call below!
	row := s.db.QueryRowContext(ctx, `
		SELECT id, cluster_id, cluster_name, cluster_type, owner_tenant_id, deployment_model,
		       base_url, database_url, periscope_url, kafka_brokers,
		       max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps,
		       current_stream_count, current_viewer_count, current_bandwidth_mbps,
		       health_status, is_active, is_default_cluster, created_at, updated_at,
		       visibility, requires_approval, short_description
		FROM quartermaster.infrastructure_clusters
		WHERE cluster_id = $1
	`, clusterID)

	var cluster pb.InfrastructureCluster
	var ownerTenantID, databaseURL, periscopeURL sql.NullString
	var kafkaBrokers []string
	var createdAt, updatedAt time.Time
	// Marketplace fields (pricing now in Purser)
	var visibility string
	var shortDescription sql.NullString
	var requiresApproval bool

	err := row.Scan(
		&cluster.Id, &cluster.ClusterId, &cluster.ClusterName, &cluster.ClusterType,
		&ownerTenantID, &cluster.DeploymentModel, &cluster.BaseUrl, &databaseURL, &periscopeURL,
		pq.Array(&kafkaBrokers), &cluster.MaxConcurrentStreams, &cluster.MaxConcurrentViewers,
		&cluster.MaxBandwidthMbps, &cluster.CurrentStreamCount, &cluster.CurrentViewerCount,
		&cluster.CurrentBandwidthMbps, &cluster.HealthStatus, &cluster.IsActive, &cluster.IsDefaultCluster,
		&createdAt, &updatedAt,
		&visibility, &requiresApproval, &shortDescription,
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

	return &cluster, nil
}

// visibilityStringToProto converts DB string to proto enum
func visibilityStringToProto(s string) pb.ClusterVisibility {
	switch s {
	case "public":
		return pb.ClusterVisibility_CLUSTER_VISIBILITY_PUBLIC
	case "unlisted":
		return pb.ClusterVisibility_CLUSTER_VISIBILITY_UNLISTED
	case "private":
		return pb.ClusterVisibility_CLUSTER_VISIBILITY_PRIVATE
	default:
		return pb.ClusterVisibility_CLUSTER_VISIBILITY_PRIVATE
	}
}

// visibilityProtoToString converts proto enum to DB string
func visibilityProtoToString(v pb.ClusterVisibility) string {
	switch v {
	case pb.ClusterVisibility_CLUSTER_VISIBILITY_PUBLIC:
		return "public"
	case pb.ClusterVisibility_CLUSTER_VISIBILITY_UNLISTED:
		return "unlisted"
	case pb.ClusterVisibility_CLUSTER_VISIBILITY_PRIVATE:
		return "private"
	default:
		return "private"
	}
}

// Note: Pricing model helpers moved to Purser service

// subscriptionStatusStringToProto converts DB string to proto enum
func subscriptionStatusStringToProto(s string) pb.ClusterSubscriptionStatus {
	switch s {
	case "pending_approval":
		return pb.ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_PENDING_APPROVAL
	case "active":
		return pb.ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_ACTIVE
	case "suspended":
		return pb.ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_SUSPENDED
	case "rejected":
		return pb.ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_REJECTED
	default:
		return pb.ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_UNSPECIFIED
	}
}

func (s *QuartermasterServer) queryNode(ctx context.Context, nodeID string) (*pb.InfrastructureNode, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, node_id, cluster_id, node_name, node_type, internal_ip, external_ip,
		       wireguard_ip, wireguard_public_key, region, availability_zone,
		       latitude, longitude, cpu_cores, memory_gb, disk_gb,
		       last_heartbeat, created_at, updated_at
		FROM quartermaster.infrastructure_nodes
		WHERE node_id = $1
	`, nodeID)

	var node pb.InfrastructureNode
	var internalIP, externalIP, wireguardIP, wireguardPubKey, region, az sql.NullString
	var latitude, longitude sql.NullFloat64
	var cpuCores, memoryGB, diskGB sql.NullInt32
	var lastHeartbeat sql.NullTime
	var createdAt, updatedAt time.Time

	err := row.Scan(
		&node.Id, &node.NodeId, &node.ClusterId, &node.NodeName, &node.NodeType,
		&internalIP, &externalIP, &wireguardIP, &wireguardPubKey, &region, &az,
		&latitude, &longitude, &cpuCores, &memoryGB, &diskGB,
		&lastHeartbeat, &createdAt, &updatedAt,
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
	if region.Valid {
		node.Region = &region.String
	}
	if az.Valid {
		node.AvailabilityZone = &az.String
	}
	if latitude.Valid {
		node.Latitude = &latitude.Float64
	}
	if longitude.Valid {
		node.Longitude = &longitude.Float64
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

func generateSecureToken(n int) string {
	b := make([]byte, n)
	rand.Read(b) //nolint:errcheck // crypto/rand.Read rarely fails
	return hex.EncodeToString(b)
}

// ============================================================================
// CLUSTER MARKETPLACE RPCs
// ============================================================================

// ListMarketplaceClusters returns clusters visible to the requesting tenant
func (s *QuartermasterServer) ListMarketplaceClusters(ctx context.Context, req *pb.ListMarketplaceClustersRequest) (*pb.ListMarketplaceClustersResponse, error) {
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
	var args []interface{}
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
			       c.current_stream_count, c.max_concurrent_streams,
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
			       c.current_stream_count, c.max_concurrent_streams,
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
		entry     *pb.MarketplaceClusterEntry
		createdAt time.Time
		clusterID string
	}
	var entries []entryWithCursor
	for rows.Next() {
		var entry pb.MarketplaceClusterEntry
		var visibility string
		var ownerName, shortDesc, subscriptionStatus sql.NullString
		var currentStreams, maxStreams int32
		var createdAt time.Time

		if err := rows.Scan(
			&entry.ClusterId, &entry.ClusterName, &shortDesc, &visibility, &entry.RequiresApproval,
			&entry.MaxConcurrentStreams, &entry.MaxConcurrentViewers,
			&currentStreams, &maxStreams,
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

		// Calculate utilization percentage
		if maxStreams > 0 {
			utilization := float64(currentStreams) / float64(maxStreams) * 100
			entry.CurrentUtilization = &utilization
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
	var clusters []*pb.MarketplaceClusterEntry
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

	return &pb.ListMarketplaceClustersResponse{
		Clusters:   clusters,
		Pagination: pagination.BuildResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
	}, nil
}

// GetMarketplaceCluster returns a single marketplace cluster entry
func (s *QuartermasterServer) GetMarketplaceCluster(ctx context.Context, req *pb.GetMarketplaceClusterRequest) (*pb.MarketplaceClusterEntry, error) {
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
			       c.current_stream_count, c.max_concurrent_streams,
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
			       c.current_stream_count, c.max_concurrent_streams,
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

	var entry pb.MarketplaceClusterEntry
	var visibility string
	var ownerName, shortDesc, subscriptionStatus sql.NullString
	var currentStreams, maxStreams int32
	var createdAt time.Time

	err := row.Scan(
		&entry.ClusterId, &entry.ClusterName, &shortDesc, &visibility, &entry.RequiresApproval,
		&entry.MaxConcurrentStreams, &entry.MaxConcurrentViewers,
		&currentStreams, &maxStreams,
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
	if maxStreams > 0 {
		utilization := float64(currentStreams) / float64(maxStreams) * 100
		entry.CurrentUtilization = &utilization
	}
	entry.CreatedAt = timestamppb.New(createdAt)

	return &entry, nil
}

// UpdateClusterMarketplace updates marketplace settings for a cluster (owner only)
func (s *QuartermasterServer) UpdateClusterMarketplace(ctx context.Context, req *pb.UpdateClusterMarketplaceRequest) (*pb.ClusterResponse, error) {
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
	var args []interface{}
	argIdx := 1

	if req.Visibility != nil {
		// Non-providers can only set private visibility
		if !isProvider && *req.Visibility != pb.ClusterVisibility_CLUSTER_VISIBILITY_PRIVATE {
			return nil, status.Error(codes.PermissionDenied, "only providers can set public/unlisted visibility")
		}
		updates = append(updates, fmt.Sprintf("visibility = $%d", argIdx))
		args = append(args, visibilityProtoToString(*req.Visibility))
		argIdx++
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

	_, err = s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update cluster: %v", err)
	}

	cluster, err := s.queryCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}

	s.emitClusterEvent(ctx, eventClusterUpdated, tenantID, userID, clusterID, "cluster", clusterID, "", "", "")

	return &pb.ClusterResponse{Cluster: cluster}, nil
}

// GetClusterMetadataBatch returns metadata for multiple clusters (for Gateway enrichment).
// Used by Gateway to enrich Purser's marketplace pricing data with cluster operational info.
func (s *QuartermasterServer) GetClusterMetadataBatch(ctx context.Context, req *pb.GetClusterMetadataBatchRequest) (*pb.GetClusterMetadataBatchResponse, error) {
	clusterIDs := req.GetClusterIds()
	if len(clusterIDs) == 0 {
		return &pb.GetClusterMetadataBatchResponse{Clusters: map[string]*pb.ClusterMetadata{}}, nil
	}

	requestingTenantID := req.GetRequestingTenantId()

	query := `
		SELECT c.cluster_id, c.cluster_name, c.short_description, c.visibility,
		       c.requires_approval, t.name AS owner_name,
		       c.max_concurrent_streams, c.max_concurrent_viewers,
		       c.current_stream_count,
		       COALESCE(a.id IS NOT NULL, false) AS is_subscribed,
		       COALESCE(a.subscription_status, 'none') AS subscription_status
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

	clusters := make(map[string]*pb.ClusterMetadata)
	for rows.Next() {
		var meta pb.ClusterMetadata
		var shortDescription, ownerName sql.NullString
		var visibility, subscriptionStatus string
		var maxStreams, maxViewers, currentStreams int32
		var isSubscribed bool

		if err := rows.Scan(
			&meta.ClusterId, &meta.ClusterName, &shortDescription, &visibility,
			&meta.RequiresApproval, &ownerName,
			&maxStreams, &maxViewers, &currentStreams,
			&isSubscribed, &subscriptionStatus,
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
		if maxStreams > 0 {
			utilization := float64(currentStreams) / float64(maxStreams) * 100
			meta.CurrentUtilization = &utilization
		}
		meta.IsSubscribed = isSubscribed
		meta.SubscriptionStatus = subscriptionStatus

		clusters[meta.ClusterId] = &meta
	}

	return &pb.GetClusterMetadataBatchResponse{Clusters: clusters}, nil
}

// CreatePrivateCluster creates a private cluster for self-hosted edge
func (s *QuartermasterServer) CreatePrivateCluster(ctx context.Context, req *pb.CreatePrivateClusterRequest) (*pb.CreatePrivateClusterResponse, error) {
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
	clusterID = fmt.Sprintf("%s-%s", clusterID, generateSecureToken(4))

	id := uuid.New().String()
	now := time.Now()

	// Create the cluster with private visibility
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO quartermaster.infrastructure_clusters (
			id, cluster_id, cluster_name, cluster_type, deployment_model,
			owner_tenant_id, base_url,
			max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps,
			visibility, pricing_model, short_description,
			health_status, is_active, created_at, updated_at
		) VALUES (
			$1, $2, $3, 'edge', 'self-hosted',
			$4, '',
			100, 10000, 1000,
			'private', 'free_unmetered', $5,
			'unknown', true, $6, $6
		)
	`, id, clusterID, clusterName, tenantID, req.ShortDescription, now)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create cluster: %v", err)
	}

	// Auto-subscribe the owner
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO quartermaster.tenant_cluster_access (
			tenant_id, cluster_id, access_level, subscription_status, is_active, created_at, updated_at
		) VALUES ($1, $2, 'owner', 'active', true, NOW(), NOW())
	`, tenantID, clusterID)
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id":  tenantID,
			"cluster_id": clusterID,
			"error":      err,
		}).Error("Failed to auto-subscribe owner to cluster")
	}

	// Create a bootstrap token for edge node registration
	tokenID := uuid.New().String()
	token := generateSecureToken(32)
	expiresAt := now.Add(30 * 24 * time.Hour) // 30 days

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO quartermaster.bootstrap_tokens (
			id, token, kind, name, tenant_id, cluster_id, expires_at, created_by, created_at
		) VALUES ($1, $2, 'edge_node', $3, $4, $5, $6, $4, NOW())
	`, tokenID, token, fmt.Sprintf("Bootstrap token for %s", clusterName), tenantID, clusterID, expiresAt)
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id":  tenantID,
			"cluster_id": clusterID,
			"error":      err,
		}).Error("Failed to create bootstrap token for new cluster")
	}

	cluster, err := s.queryCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}

	s.emitClusterEvent(ctx, eventClusterCreated, tenantID, userID, clusterID, "cluster", clusterID, "", "", "")
	s.emitClusterEvent(ctx, eventTenantClusterAssigned, tenantID, userID, clusterID, "cluster", clusterID, "", "", "")

	return &pb.CreatePrivateClusterResponse{
		Cluster: cluster,
		BootstrapToken: &pb.BootstrapToken{
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
func (s *QuartermasterServer) CreateClusterInvite(ctx context.Context, req *pb.CreateClusterInviteRequest) (*pb.ClusterInvite, error) {
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
	token := generateSecureToken(32)
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

	return &pb.ClusterInvite{
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
func (s *QuartermasterServer) RevokeClusterInvite(ctx context.Context, req *pb.RevokeClusterInviteRequest) (*emptypb.Empty, error) {
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
func (s *QuartermasterServer) ListClusterInvites(ctx context.Context, req *pb.ListClusterInvitesRequest) (*pb.ListClusterInvitesResponse, error) {
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
	args := []interface{}{clusterID}
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

	var invites []*pb.ClusterInvite
	for rows.Next() {
		var invite pb.ClusterInvite
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
			var limitsMap map[string]interface{}
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

	return &pb.ListClusterInvitesResponse{
		Invites:    invites,
		Pagination: pagination.BuildResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
	}, nil
}

// ListMyClusterInvites lists invites received by a tenant
func (s *QuartermasterServer) ListMyClusterInvites(ctx context.Context, req *pb.ListMyClusterInvitesRequest) (*pb.ListClusterInvitesResponse, error) {
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
	args := []interface{}{tenantID}
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

	var invites []*pb.ClusterInvite
	for rows.Next() {
		var invite pb.ClusterInvite
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
			var limitsMap map[string]interface{}
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

	return &pb.ListClusterInvitesResponse{
		Invites:    invites,
		Pagination: pagination.BuildResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
	}, nil
}

// RequestClusterSubscription requests access to a cluster
func (s *QuartermasterServer) RequestClusterSubscription(ctx context.Context, req *pb.RequestClusterSubscriptionRequest) (*pb.ClusterSubscription, error) {
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
	err := s.db.QueryRowContext(ctx, `
		SELECT visibility, pricing_model, requires_approval, owner_tenant_id
		FROM quartermaster.infrastructure_clusters
		WHERE cluster_id = $1 AND is_active = true
	`, clusterID).Scan(&visibility, &pricingModel, &requiresApproval, &ownerTenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "cluster not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
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
	var inviteAccessLevel string
	var inviteResourceLimits sql.NullString
	if inviteToken != nil && *inviteToken != "" {
		var inviteID, inviteClusterID, inviteTenantID string
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

		// Mark invite as accepted
		_, err = s.db.ExecContext(ctx, `
			UPDATE quartermaster.cluster_invites SET status = 'accepted', accepted_at = NOW()
			WHERE id = $1
		`, inviteID)
		if err != nil {
			s.logger.WithFields(logging.Fields{
				"invite_id": inviteID,
				"error":     err,
			}).Error("Failed to mark invite as accepted")
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

	// Create or update subscription
	_, err = s.db.ExecContext(ctx, `
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
		inviteResourceLimits, now)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create subscription: %v", err)
	}

	// Fetch the created subscription
	sub, err := s.getClusterSubscription(ctx, tenantID, clusterID)
	if err != nil {
		return nil, err
	}

	eventType := eventClusterSubscriptionRequested
	if sub.SubscriptionStatus == pb.ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_ACTIVE {
		eventType = eventClusterSubscriptionApproved
	}
	s.emitClusterEvent(ctx, eventType, tenantID, userID, clusterID, "cluster_subscription", sub.Id, "", sub.Id, "")

	return sub, nil
}

// AcceptClusterInvite accepts a cluster invite using the token
func (s *QuartermasterServer) AcceptClusterInvite(ctx context.Context, req *pb.AcceptClusterInviteRequest) (*pb.ClusterSubscription, error) {
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
	var inviteID, clusterID, invitedTenantID, accessLevel string
	var resourceLimits sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, cluster_id, invited_tenant_id, access_level, resource_limits
		FROM quartermaster.cluster_invites
		WHERE invite_token = $1 AND status = 'pending'
		  AND (expires_at IS NULL OR expires_at > NOW())
	`, inviteToken).Scan(&inviteID, &clusterID, &invitedTenantID, &accessLevel, &resourceLimits)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "invalid or expired invite token")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	if invitedTenantID != tenantID {
		return nil, status.Error(codes.PermissionDenied, "invite is for a different tenant")
	}

	// Mark invite as accepted
	_, err = s.db.ExecContext(ctx, `
		UPDATE quartermaster.cluster_invites SET status = 'accepted', accepted_at = NOW()
		WHERE id = $1
	`, inviteID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to accept invite: %v", err)
	}

	now := time.Now()
	id := uuid.New().String()

	// Create subscription (active since it's via invite)
	_, err = s.db.ExecContext(ctx, `
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
	`, id, tenantID, clusterID, accessLevel, resourceLimits, now)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create subscription: %v", err)
	}

	sub, err := s.getClusterSubscription(ctx, tenantID, clusterID)
	if err != nil {
		return nil, err
	}

	s.emitClusterEvent(ctx, eventClusterSubscriptionApproved, tenantID, userID, clusterID, "cluster_subscription", sub.Id, inviteID, sub.Id, "")

	return sub, nil
}

// ListPendingSubscriptions lists pending subscription requests for a cluster
func (s *QuartermasterServer) ListPendingSubscriptions(ctx context.Context, req *pb.ListPendingSubscriptionsRequest) (*pb.ListPendingSubscriptionsResponse, error) {
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
	args := []interface{}{clusterID}
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

	var subscriptions []*pb.ClusterSubscription
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

	return &pb.ListPendingSubscriptionsResponse{
		Subscriptions: subscriptions,
		Pagination:    pagination.BuildResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
	}, nil
}

// ApproveClusterSubscription approves a pending subscription
func (s *QuartermasterServer) ApproveClusterSubscription(ctx context.Context, req *pb.ApproveClusterSubscriptionRequest) (*pb.ClusterSubscription, error) {
	subscriptionID := req.GetSubscriptionId()
	ownerTenantID := req.GetOwnerTenantId()

	if subscriptionID == "" || ownerTenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "subscription_id and owner_tenant_id required")
	}

	userID := middleware.GetUserID(ctx)
	// Get subscription and verify ownership
	var tenantID, clusterID string
	var dbOwnerID sql.NullString
	err := s.db.QueryRowContext(ctx, `
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
		return nil, status.Error(codes.PermissionDenied, "only cluster owner can approve subscriptions")
	}

	_, err = s.db.ExecContext(ctx, `
		UPDATE quartermaster.tenant_cluster_access
		SET subscription_status = 'active', approved_at = NOW(), approved_by = $2, updated_at = NOW()
		WHERE id = $1
	`, subscriptionID, ownerTenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to approve subscription: %v", err)
	}

	sub, err := s.getClusterSubscription(ctx, tenantID, clusterID)
	if err != nil {
		return nil, err
	}

	s.emitClusterEvent(ctx, eventClusterSubscriptionApproved, tenantID, userID, clusterID, "cluster_subscription", subscriptionID, "", subscriptionID, "")

	return sub, nil
}

// RejectClusterSubscription rejects a pending subscription
func (s *QuartermasterServer) RejectClusterSubscription(ctx context.Context, req *pb.RejectClusterSubscriptionRequest) (*pb.ClusterSubscription, error) {
	subscriptionID := req.GetSubscriptionId()
	ownerTenantID := req.GetOwnerTenantId()

	if subscriptionID == "" || ownerTenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "subscription_id and owner_tenant_id required")
	}

	userID := middleware.GetUserID(ctx)
	// Get subscription and verify ownership
	var tenantID, clusterID string
	var dbOwnerID sql.NullString
	err := s.db.QueryRowContext(ctx, `
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
	_, err = s.db.ExecContext(ctx, `
		UPDATE quartermaster.tenant_cluster_access
		SET subscription_status = 'rejected', rejection_reason = $2, is_active = false, updated_at = NOW()
		WHERE id = $1
	`, subscriptionID, reason)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to reject subscription: %v", err)
	}

	sub, err := s.getClusterSubscription(ctx, tenantID, clusterID)
	if err != nil {
		return nil, err
	}

	s.emitClusterEvent(ctx, eventClusterSubscriptionRejected, tenantID, userID, clusterID, "cluster_subscription", subscriptionID, "", subscriptionID, reason)

	return sub, nil
}

// getClusterSubscription is a helper to fetch a subscription by tenant and cluster
func (s *QuartermasterServer) getClusterSubscription(ctx context.Context, tenantID, clusterID string) (*pb.ClusterSubscription, error) {
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
func scanClusterSubscription(rows *sql.Rows) (*pb.ClusterSubscription, error) {
	var sub pb.ClusterSubscription
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
		var limitsMap map[string]interface{}
		if json.Unmarshal([]byte(resourceLimits.String), &limitsMap) == nil {
			sub.ResourceLimits = mapToStruct(limitsMap)
		}
	}

	return &sub, nil
}

// scanClusterSubscriptionRow scans a ClusterSubscription from a single row
func scanClusterSubscriptionRow(row *sql.Row) (*pb.ClusterSubscription, error) {
	var sub pb.ClusterSubscription
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
		var limitsMap map[string]interface{}
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
	Metrics         *ServerMetrics
}

// ServerMetrics holds Prometheus metrics for the gRPC server
type ServerMetrics struct {
	TenantOperations  *prometheus.CounterVec
	ClusterOperations *prometheus.CounterVec
	NodeOperations    *prometheus.CounterVec
	ServiceOperations *prometheus.CounterVec
	GRPCRequests      *prometheus.CounterVec
	GRPCDuration      *prometheus.HistogramVec
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

	opts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(authInterceptor, unaryInterceptor(cfg.Logger)),
	}

	server := grpc.NewServer(opts...)
	qmServer := NewQuartermasterServer(cfg.DB, cfg.Logger, cfg.NavigatorClient, cfg.DecklogClient, cfg.PurserClient, cfg.Metrics)

	// Register all services
	pb.RegisterTenantServiceServer(server, qmServer)
	pb.RegisterBootstrapServiceServer(server, qmServer)
	pb.RegisterNodeServiceServer(server, qmServer)
	pb.RegisterClusterServiceServer(server, qmServer)
	pb.RegisterMeshServiceServer(server, qmServer)
	pb.RegisterServiceRegistryServiceServer(server, qmServer)

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
		}).Debug("gRPC request processed")
		return resp, err
	}
}
