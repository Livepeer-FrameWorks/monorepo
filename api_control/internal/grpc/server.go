package grpc

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/smtp"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"frameworks/pkg/auth"
	"frameworks/pkg/billing"
	decklogclient "frameworks/pkg/clients/decklog"
	foghornclient "frameworks/pkg/clients/foghorn"
	"frameworks/pkg/clients/listmonk"
	purserclient "frameworks/pkg/clients/purser"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	fieldcrypt "frameworks/pkg/crypto"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/grpcutil"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
	"frameworks/pkg/pagination"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/turnstile"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// botProtectionRequest interface for requests with bot protection fields
type botProtectionRequest interface {
	GetPhoneNumber() string
	GetHumanCheck() string
	GetBehavior() *pb.BehaviorData
}

// validateBehavior checks behavioral signals (fallback when Turnstile not configured)
func validateBehavior(req botProtectionRequest) bool {
	// Honeypot: phone_number should be empty
	if req.GetPhoneNumber() != "" {
		return false
	}
	// Human checkbox
	if req.GetHumanCheck() != "human" {
		return false
	}
	// Timing and interaction
	b := req.GetBehavior()
	if b == nil {
		return false
	}
	timeSpent := b.GetSubmittedAt() - b.GetFormShownAt()
	if timeSpent < 3000 || timeSpent > 30*60*1000 {
		return false
	}
	if !b.GetMouse() && !b.GetTyped() {
		return false
	}
	return true
}

// ServerMetrics holds Prometheus metrics for the gRPC server
type ServerMetrics struct {
	AuthOperations   *prometheus.CounterVec
	AuthDuration     *prometheus.HistogramVec
	StreamOperations *prometheus.CounterVec
}

// CommodoreServer implements the Commodore gRPC services
type CommodoreServer struct {
	pb.UnimplementedInternalServiceServer
	pb.UnimplementedUserServiceServer
	pb.UnimplementedStreamServiceServer
	pb.UnimplementedStreamKeyServiceServer
	pb.UnimplementedDeveloperServiceServer
	pb.UnimplementedClipServiceServer
	pb.UnimplementedDVRServiceServer
	pb.UnimplementedViewerServiceServer
	pb.UnimplementedVodServiceServer
	pb.UnimplementedNodeManagementServiceServer
	pb.UnimplementedPushTargetServiceServer
	db                   *sql.DB
	logger               logging.Logger
	foghornPool          *foghornclient.FoghornPool
	quartermasterClient  *qmclient.GRPCClient
	purserClient         *purserclient.GRPCClient
	listmonkClient       *listmonk.Client
	decklogClient        *decklogclient.BatchedClient
	defaultMailingListID int
	metrics              *ServerMetrics
	turnstileValidator   *turnstile.Validator
	turnstileFailOpen    bool
	passwordResetSecret  []byte
	fieldEncryptor       *fieldcrypt.FieldEncryptor
	routeCache           map[string]*clusterRoute
	routeCacheMu         sync.RWMutex
	routeCacheTTL        time.Duration
}

// clusterRoute caches the tenant -> cluster -> foghorn mapping.
type clusterRoute struct {
	clusterID   string
	foghornAddr string
	clusterSlug string
	baseURL     string
	clusterName string
	// Official cluster (from billing tier) — provides geographic coverage
	officialClusterID       string
	officialClusterSlug     string
	officialBaseURL         string
	officialClusterName     string
	officialFoghornGrpcAddr string
	clusterPeers            []*pb.TenantClusterPeer // full tenant cluster context (includes per-peer foghorn addrs)
	resolvedAt              time.Time
}

type clusterFanoutTarget struct {
	clusterID string
	addr      string
}

const activeIngestClusterFreshnessWindow = 2 * time.Minute

func buildClusterFanoutTargets(route *clusterRoute) []clusterFanoutTarget {
	if route == nil {
		return nil
	}

	seen := make(map[string]struct{})
	targets := make([]clusterFanoutTarget, 0, len(route.clusterPeers)+2)
	addTarget := func(clusterID, addr string) {
		if addr == "" {
			return
		}
		key := clusterID
		if key == "" {
			key = addr
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		targets = append(targets, clusterFanoutTarget{clusterID: clusterID, addr: addr})
	}

	addTarget(route.clusterID, route.foghornAddr)
	if route.officialClusterID != route.clusterID {
		addTarget(route.officialClusterID, route.officialFoghornGrpcAddr)
	}
	for _, peer := range route.clusterPeers {
		addTarget(peer.ClusterId, peer.FoghornGrpcAddr)
	}

	return targets
}

func foghornPoolKey(clusterID, addr string) string {
	if clusterID != "" {
		return clusterID
	}
	return addr
}

func normalizeClusterRoute(route *clusterRoute) {
	if route == nil {
		return
	}

	if route.clusterID == "" {
		switch {
		case route.officialClusterID != "":
			route.clusterID = route.officialClusterID
		default:
			for _, peer := range route.clusterPeers {
				if peer.GetClusterId() != "" {
					route.clusterID = peer.GetClusterId()
					break
				}
			}
		}
	}

	if route.foghornAddr == "" {
		if route.clusterID != "" {
			route.foghornAddr = resolveAddrFromRoute(route, route.clusterID)
		}
		if route.foghornAddr == "" {
			for _, peer := range route.clusterPeers {
				if peer.GetFoghornGrpcAddr() != "" && (route.clusterID == "" || peer.GetClusterId() == "" || peer.GetClusterId() == route.clusterID) {
					route.foghornAddr = peer.GetFoghornGrpcAddr()
					if route.clusterID == "" {
						route.clusterID = peer.GetClusterId()
					}
					break
				}
			}
		}
	}
}

func selectActiveIngestCluster(clusterID sql.NullString, updatedAt sql.NullTime, now time.Time) (string, bool) {
	if !clusterID.Valid || clusterID.String == "" {
		return "", false
	}
	if !updatedAt.Valid {
		return "", false
	}
	if now.Sub(updatedAt.Time) > activeIngestClusterFreshnessWindow {
		return "", false
	}
	return clusterID.String, true
}

type commodoreUserRecord struct {
	ID           string
	TenantID     string
	Email        string
	PasswordHash string
	FirstName    sql.NullString
	LastName     sql.NullString
	Role         string
	Permissions  []string
	IsActive     bool
	IsVerified   bool
	LastLoginAt  sql.NullTime
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func scanCommodoreUserForLogin(row *sql.Row, user *commodoreUserRecord) error {
	return row.Scan(
		&user.ID,
		&user.TenantID,
		&user.Email,
		&user.PasswordHash,
		&user.FirstName,
		&user.LastName,
		&user.Role,
		pq.Array(&user.Permissions),
		&user.IsActive,
		&user.IsVerified,
		&user.CreatedAt,
		&user.UpdatedAt,
	)
}

func scanCommodoreUserForGetMe(row *sql.Row, user *commodoreUserRecord) error {
	return row.Scan(
		&user.ID,
		&user.TenantID,
		&user.Email,
		&user.FirstName,
		&user.LastName,
		&user.Role,
		pq.Array(&user.Permissions),
		&user.IsActive,
		&user.IsVerified,
		&user.LastLoginAt,
		&user.CreatedAt,
		&user.UpdatedAt,
	)
}

func scanCommodoreUserForRefresh(row *sql.Row, user *commodoreUserRecord) error {
	return row.Scan(
		&user.Email,
		&user.Role,
		pq.Array(&user.Permissions),
		&user.FirstName,
		&user.LastName,
		&user.IsActive,
		&user.IsVerified,
		&user.CreatedAt,
		&user.UpdatedAt,
	)
}

func (u commodoreUserRecord) toProtoUser(userID, tenantID string) *pb.User {
	id := u.ID
	if userID != "" {
		id = userID
	}
	tenant := u.TenantID
	if tenantID != "" {
		tenant = tenantID
	}
	email := u.Email

	result := &pb.User{
		Id:          id,
		TenantId:    tenant,
		Email:       &email,
		FirstName:   u.FirstName.String,
		LastName:    u.LastName.String,
		Role:        u.Role,
		Permissions: u.Permissions,
		IsActive:    u.IsActive,
		IsVerified:  u.IsVerified,
		CreatedAt:   timestamppb.New(u.CreatedAt),
		UpdatedAt:   timestamppb.New(u.UpdatedAt),
	}
	if u.LastLoginAt.Valid {
		result.LastLoginAt = timestamppb.New(u.LastLoginAt.Time)
	}
	return result
}

// CommodoreServerConfig contains all dependencies for CommodoreServer
type CommodoreServerConfig struct {
	DB                   *sql.DB
	Logger               logging.Logger
	FoghornPool          *foghornclient.FoghornPool
	QuartermasterClient  *qmclient.GRPCClient
	PurserClient         *purserclient.GRPCClient
	ListmonkClient       *listmonk.Client
	DecklogClient        *decklogclient.BatchedClient
	DefaultMailingListID int
	Metrics              *ServerMetrics
	// Auth config for gRPC interceptor
	ServiceToken string
	JWTSecret    []byte
	// Bot protection
	TurnstileSecretKey string
	TurnstileFailOpen  bool
	// Password reset token signing
	PasswordResetSecret []byte
}

// NewCommodoreServer creates a new Commodore gRPC server
func NewCommodoreServer(cfg CommodoreServerConfig) *CommodoreServer {
	var tv *turnstile.Validator
	if cfg.TurnstileSecretKey != "" {
		tv = turnstile.NewValidator(cfg.TurnstileSecretKey)
	}

	// Derive field encryption key from JWT secret for encrypting sensitive fields
	// (e.g., push target URIs that contain third-party stream keys)
	fe, err := fieldcrypt.DeriveFieldEncryptor(cfg.JWTSecret, "push-target-uri")
	if err != nil {
		cfg.Logger.WithError(err).Fatal("Failed to derive field encryption key")
	}

	return &CommodoreServer{
		db:                   cfg.DB,
		logger:               cfg.Logger,
		foghornPool:          cfg.FoghornPool,
		quartermasterClient:  cfg.QuartermasterClient,
		purserClient:         cfg.PurserClient,
		listmonkClient:       cfg.ListmonkClient,
		decklogClient:        cfg.DecklogClient,
		defaultMailingListID: cfg.DefaultMailingListID,
		metrics:              cfg.Metrics,
		turnstileValidator:   tv,
		turnstileFailOpen:    cfg.TurnstileFailOpen,
		passwordResetSecret:  cfg.PasswordResetSecret,
		fieldEncryptor:       fe,
		routeCache:           make(map[string]*clusterRoute),
		routeCacheTTL:        5 * time.Minute,
	}
}

// resolveClusterRouteForTenant returns cached or fresh cluster routing data
// from Quartermaster. Never dials Foghorn. Safe to call from handlers that
// only need cluster metadata (origin_cluster_id, cluster_peers, domain slugs).
func (s *CommodoreServer) resolveClusterRouteForTenant(ctx context.Context, tenantID string) (*clusterRoute, error) {
	s.routeCacheMu.RLock()
	if route, ok := s.routeCache[tenantID]; ok && time.Since(route.resolvedAt) < s.routeCacheTTL {
		s.routeCacheMu.RUnlock()
		return route, nil
	}
	s.routeCacheMu.RUnlock()

	if s.quartermasterClient == nil {
		return nil, status.Error(codes.Unavailable, "quartermaster not available for cluster routing")
	}

	resp, err := s.quartermasterClient.GetClusterRouting(ctx, &pb.GetClusterRoutingRequest{TenantId: tenantID})
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "cluster routing failed: %v", err)
	}

	route := &clusterRoute{
		clusterID:               resp.GetClusterId(),
		foghornAddr:             resp.GetFoghornGrpcAddr(),
		clusterSlug:             resp.GetClusterSlug(),
		baseURL:                 resp.GetBaseUrl(),
		clusterName:             resp.GetClusterName(),
		officialClusterID:       resp.GetOfficialClusterId(),
		officialClusterSlug:     resp.GetOfficialClusterSlug(),
		officialBaseURL:         resp.GetOfficialBaseUrl(),
		officialClusterName:     resp.GetOfficialClusterName(),
		officialFoghornGrpcAddr: resp.GetOfficialFoghornGrpcAddr(),
		clusterPeers:            resp.GetClusterPeers(),
		resolvedAt:              time.Now(),
	}
	normalizeClusterRoute(route)

	s.routeCacheMu.Lock()
	s.routeCache[tenantID] = route
	s.routeCacheMu.Unlock()

	return route, nil
}

// resolveFoghornForTenant returns a Foghorn gRPC client for the tenant's cluster.
// Delegates to resolveClusterRouteForTenant for routing, then dials via pool.
// On any failure, evicts the cached route and retries once with a fresh lookup.
func (s *CommodoreServer) resolveFoghornForTenant(ctx context.Context, tenantID string) (*foghornclient.GRPCClient, *clusterRoute, error) {
	resolveAndDial := func() (*foghornclient.GRPCClient, *clusterRoute, error) {
		route, err := s.resolveClusterRouteForTenant(ctx, tenantID)
		if err != nil {
			return nil, nil, err
		}

		if route.foghornAddr == "" {
			return nil, route, status.Errorf(codes.Unavailable, "no foghorn registered for cluster %s", route.clusterID)
		}

		client, err := s.foghornPool.GetOrCreate(foghornPoolKey(route.clusterID, route.foghornAddr), route.foghornAddr)
		if err != nil {
			return nil, route, status.Errorf(codes.Unavailable, "foghorn connection failed for cluster %s: %v", route.clusterID, err)
		}
		return client, route, nil
	}

	client, route, err := resolveAndDial()
	if err == nil {
		return client, route, nil
	}
	if len(tenantID) == 0 {
		return nil, nil, err
	}

	s.routeCacheMu.Lock()
	delete(s.routeCache, tenantID)
	s.routeCacheMu.Unlock()

	client, route, retryErr := resolveAndDial()
	if retryErr == nil {
		return client, route, nil
	}
	return nil, nil, retryErr
}

// resolveFoghornForCluster returns a Foghorn gRPC client for a specific cluster,
// looking up the address from the tenant's cached routing data.
// Used for artifact operations where origin_cluster_id may differ from primary.
func (s *CommodoreServer) resolveFoghornForCluster(ctx context.Context, clusterID, tenantID string) (*foghornclient.GRPCClient, error) {
	route, err := s.resolveClusterRouteForTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	addr := resolveAddrFromRoute(route, clusterID)
	if addr == "" {
		// Evict cache and retry once — Foghorn may have been assigned since last fill
		s.routeCacheMu.Lock()
		delete(s.routeCache, tenantID)
		s.routeCacheMu.Unlock()

		route, err = s.resolveClusterRouteForTenant(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		addr = resolveAddrFromRoute(route, clusterID)
	}

	if addr == "" {
		return nil, status.Errorf(codes.NotFound,
			"no foghorn address for cluster %s (tenant %s has access to %d clusters)",
			clusterID, tenantID, len(route.clusterPeers))
	}

	client, err := s.foghornPool.GetOrCreate(foghornPoolKey(clusterID, addr), addr)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "foghorn connection failed for cluster %s: %v", clusterID, err)
	}
	return client, nil
}

// resolveAddrFromRoute looks up a Foghorn address for clusterID within cached route data.
func resolveAddrFromRoute(route *clusterRoute, clusterID string) string {
	if route.clusterID == clusterID && route.foghornAddr != "" {
		return route.foghornAddr
	}
	if route.officialClusterID == clusterID && route.officialFoghornGrpcAddr != "" {
		return route.officialFoghornGrpcAddr
	}
	for _, peer := range route.clusterPeers {
		if peer.ClusterId == clusterID && peer.FoghornGrpcAddr != "" {
			return peer.FoghornGrpcAddr
		}
	}
	return ""
}

// resolveFoghornForContent resolves a Foghorn client using the content_id (playback_id
// or internal_name). Used by public endpoints where no tenant context is available.
// Looks up the stream to find its active_ingest_cluster_id for a direct pool hit,
// falling back to tenant-based routing with the stream's tenant_id.
// Returns codes.NotFound (non-CB-tripping) when the content doesn't exist.
func (s *CommodoreServer) resolveFoghornForContent(ctx context.Context, contentID string) (*foghornclient.GRPCClient, *clusterRoute, error) {
	if contentID == "" {
		return nil, nil, status.Error(codes.InvalidArgument, "content_id required")
	}

	var tenantID, activeClusterID sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT tenant_id, active_ingest_cluster_id
		FROM commodore.streams WHERE playback_id = $1
	`, contentID).Scan(&tenantID, &activeClusterID)

	if errors.Is(err, sql.ErrNoRows) {
		// Try internal_name lookup (content_id may be "live+<name>")
		name := strings.TrimPrefix(contentID, "live+")
		err = s.db.QueryRowContext(ctx, `
			SELECT tenant_id, active_ingest_cluster_id
			FROM commodore.streams WHERE internal_name = $1
		`, name).Scan(&tenantID, &activeClusterID)
	}

	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, status.Errorf(codes.NotFound, "content %q not found", contentID)
	}
	if err != nil {
		return nil, nil, status.Errorf(codes.Internal, "database error resolving content: %v", err)
	}

	// Direct pool hit via active_ingest_cluster_id (set by ValidateStreamKey at ingest time)
	if activeClusterID.Valid && activeClusterID.String != "" {
		if client, ok := s.foghornPool.Get(foghornPoolKey(activeClusterID.String, "")); ok {
			return client, &clusterRoute{clusterID: activeClusterID.String}, nil
		}
	}

	// Fall back to tenant-based routing (populates pool for next time)
	if !tenantID.Valid || tenantID.String == "" {
		return nil, nil, status.Error(codes.NotFound, "content has no tenant association")
	}
	client, route, err := s.resolveFoghornForTenant(ctx, tenantID.String)
	if err != nil {
		return nil, nil, err
	}
	return client, route, nil
}

// resolveFoghornForStreamKey resolves a Foghorn client using the ingest stream key.
// Used by unauthenticated resolveIngestEndpoint where no tenant context is available.
func (s *CommodoreServer) resolveFoghornForStreamKey(ctx context.Context, streamKey string) (*foghornclient.GRPCClient, *clusterRoute, error) {
	if streamKey == "" {
		return nil, nil, status.Error(codes.InvalidArgument, "stream_key required")
	}

	var tenantID, activeClusterID sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT tenant_id, active_ingest_cluster_id
		FROM commodore.streams WHERE stream_key = $1
	`, streamKey).Scan(&tenantID, &activeClusterID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, status.Error(codes.NotFound, "stream key not found")
	}
	if err != nil {
		return nil, nil, status.Errorf(codes.Internal, "database error resolving stream key: %v", err)
	}

	// Direct pool hit via active_ingest_cluster_id (set by ValidateStreamKey at ingest time)
	if activeClusterID.Valid && activeClusterID.String != "" {
		if client, ok := s.foghornPool.Get(foghornPoolKey(activeClusterID.String, "")); ok {
			return client, &clusterRoute{clusterID: activeClusterID.String}, nil
		}
	}

	// Fall back to tenant-based routing (populates pool for next time)
	if !tenantID.Valid || tenantID.String == "" {
		return nil, nil, status.Error(codes.NotFound, "stream key has no tenant association")
	}
	client, route, err := s.resolveFoghornForTenant(ctx, tenantID.String)
	if err != nil {
		return nil, nil, err
	}
	return client, route, nil
}

func clusterInPeers(peers []*pb.TenantClusterPeer, clusterID string) bool {
	for _, p := range peers {
		if p.ClusterId == clusterID {
			return true
		}
	}
	return false
}

// resolveFoghornForArtifact returns a Foghorn client routed to the artifact's origin cluster.
// Falls back to the tenant's primary cluster if originClusterID is empty (legacy data).
func (s *CommodoreServer) resolveFoghornForArtifact(ctx context.Context, tenantID, originClusterID string) (*foghornclient.GRPCClient, error) {
	if originClusterID == "" {
		client, _, err := s.resolveFoghornForTenant(ctx, tenantID)
		return client, err
	}
	return s.resolveFoghornForCluster(ctx, originClusterID, tenantID)
}

// ============================================================================
// INTERNAL SERVICE (Foghorn, Decklog → Commodore)
// ============================================================================

// ValidateStreamKey validates a stream key for RTMP ingest (called by Foghorn on PUSH_REWRITE)
func (s *CommodoreServer) ValidateStreamKey(ctx context.Context, req *pb.ValidateStreamKeyRequest) (*pb.ValidateStreamKeyResponse, error) {
	streamKey := req.GetStreamKey()
	if streamKey == "" {
		return &pb.ValidateStreamKeyResponse{
			Valid: false,
			Error: "stream_key required",
		}, nil
	}

	// Query stream info from commodore tables only (no cross-service DB access)
	var streamID, userID, tenantID, internalName, playbackID string
	var isActive, isRecordingEnabled bool

	err := s.db.QueryRowContext(ctx, `
		SELECT
			s.id, s.user_id, s.tenant_id, s.internal_name,
			u.is_active, s.is_recording_enabled, s.playback_id
		FROM commodore.streams s
		JOIN commodore.users u ON s.user_id = u.id
		WHERE s.stream_key = $1
	`, streamKey).Scan(&streamID, &userID, &tenantID, &internalName, &isActive, &isRecordingEnabled, &playbackID)

	if errors.Is(err, sql.ErrNoRows) {
		return &pb.ValidateStreamKeyResponse{
			Valid: false,
			Error: "Invalid stream key",
		}, nil
	}

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"stream_key": streamKey,
			"error":      err,
		}).Error("Database error validating stream key")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	if !isActive {
		return &pb.ValidateStreamKeyResponse{
			Valid: false,
			Error: "User account is inactive",
		}, nil
	}

	// Get billing status via Purser gRPC (not direct DB access)
	billingModel := "postpaid"
	var isSuspended, isBalanceNegative bool

	if s.purserClient != nil {
		billingStatus, err := s.purserClient.GetTenantBillingStatus(ctx, tenantID)
		if err != nil {
			s.logger.WithFields(logging.Fields{
				"tenant_id": tenantID,
				"error":     err,
			}).Warn("Failed to get billing status from Purser, assuming postpaid/active")
			// Continue with defaults - don't fail stream validation on billing lookup failure
		} else {
			billingModel = billingStatus.BillingModel
			isSuspended = billingStatus.IsSuspended
			isBalanceNegative = billingStatus.IsBalanceNegative
		}
	}

	resp := &pb.ValidateStreamKeyResponse{
		Valid:              true,
		UserId:             userID,
		TenantId:           tenantID,
		InternalName:       internalName,
		IsRecordingEnabled: isRecordingEnabled,
		StreamId:           streamID,
		BillingModel:       billingModel,
		IsSuspended:        isSuspended,
		IsBalanceNegative:  isBalanceNegative,
		PlaybackId:         playbackID,
	}

	if route, err := s.resolveClusterRouteForTenant(ctx, tenantID); err == nil {
		resolvedOriginClusterID := route.clusterID
		if ingestClusterID := req.GetClusterId(); ingestClusterID != "" {
			if ingestClusterID == route.clusterID ||
				ingestClusterID == route.officialClusterID ||
				clusterInPeers(route.clusterPeers, ingestClusterID) {
				resolvedOriginClusterID = ingestClusterID
			}
		}
		resp.OriginClusterId = &resolvedOriginClusterID
		if route.officialClusterID != "" {
			resp.OfficialClusterId = &route.officialClusterID
		}
		resp.ClusterPeers = route.clusterPeers
	}

	// Load enabled push targets for multistreaming
	pushRows, pushErr := s.db.QueryContext(ctx, `
		SELECT id, platform, name, target_uri
		FROM commodore.push_targets
		WHERE stream_id = $1 AND tenant_id = $2 AND is_enabled = true
	`, streamID, tenantID)
	if pushErr != nil {
		s.logger.WithError(pushErr).WithField("stream_id", streamID).Warn("Failed to load push targets")
	} else {
		defer pushRows.Close()
		for pushRows.Next() {
			var t pb.PushTargetInternal
			if scanErr := pushRows.Scan(&t.Id, &t.Platform, &t.Name, &t.TargetUri); scanErr != nil {
				s.logger.WithError(scanErr).Warn("Failed to scan push target")
				continue
			}
			if decrypted, decErr := s.fieldEncryptor.Decrypt(t.TargetUri); decErr == nil {
				t.TargetUri = decrypted
			}
			resp.PushTargets = append(resp.PushTargets, &t)
		}
	}

	// Track which cluster this stream is ingesting on (Foghorn reports its own cluster_id)
	if ingestClusterID := req.GetClusterId(); ingestClusterID != "" {
		res, updateErr := s.db.ExecContext(ctx, `
			UPDATE commodore.streams
			SET active_ingest_cluster_id = $1,
				active_ingest_cluster_updated_at = NOW(),
				updated_at = NOW()
			WHERE stream_key = $2
				AND (
					active_ingest_cluster_id IS NULL
					OR active_ingest_cluster_id = ''
					OR active_ingest_cluster_id = $1
					OR active_ingest_cluster_updated_at IS NULL
					OR active_ingest_cluster_updated_at < NOW() - INTERVAL '30 seconds'
				)
		`, ingestClusterID, streamKey)
		if updateErr != nil {
			s.logger.WithError(updateErr).WithField("stream_key", streamKey).Warn("Failed to record ingest cluster")
		} else if rows, rowsErr := res.RowsAffected(); rowsErr == nil && rows == 0 {
			s.logger.WithFields(logging.Fields{
				"stream_key":        streamKey,
				"ingest_cluster_id": ingestClusterID,
			}).Debug("Skipped ingest cluster update due to active lease")
		}
	}

	return resp, nil
}

// ResolvePlaybackID resolves a playback ID to internal name for MistServer PLAY_REWRITE trigger
func (s *CommodoreServer) ResolvePlaybackID(ctx context.Context, req *pb.ResolvePlaybackIDRequest) (*pb.ResolvePlaybackIDResponse, error) {
	playbackID := req.GetPlaybackId()
	if playbackID == "" {
		return nil, status.Error(codes.InvalidArgument, "playback_id required")
	}

	var streamID, internalName, tenantID string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, internal_name, tenant_id FROM commodore.streams WHERE playback_id = $1
	`, playbackID).Scan(&streamID, &internalName, &tenantID)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "Stream not found")
	}

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"playback_id": playbackID,
			"error":       err,
		}).Error("Database error resolving playback ID")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Note: Status check removed - operational state now comes from Periscope (Data Plane)
	// Foghorn handles real-time stream state through its own state management

	resp := &pb.ResolvePlaybackIDResponse{
		InternalName: internalName,
		TenantId:     tenantID,
		PlaybackId:   playbackID,
		StreamId:     streamID,
	}

	if route, err := s.resolveClusterRouteForTenant(ctx, tenantID); err == nil {
		resp.OriginClusterId = &route.clusterID
		if route.officialClusterID != "" {
			resp.OfficialClusterId = &route.officialClusterID
		}
		resp.ClusterPeers = route.clusterPeers
	}

	return resp, nil
}

// ResolveInternalName resolves an internal_name to tenant context for event enrichment
func (s *CommodoreServer) ResolveInternalName(ctx context.Context, req *pb.ResolveInternalNameRequest) (*pb.ResolveInternalNameResponse, error) {
	internalName := req.GetInternalName()
	if internalName == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name required")
	}

	var streamID, tenantID, userID string
	var isRecordingEnabled bool
	err := s.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, user_id, is_recording_enabled FROM commodore.streams WHERE internal_name = $1
	`, internalName).Scan(&streamID, &tenantID, &userID, &isRecordingEnabled)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "Stream not found")
	}

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"error":         err,
		}).Error("Database error resolving internal name")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	resp := &pb.ResolveInternalNameResponse{
		InternalName:       internalName,
		TenantId:           tenantID,
		UserId:             userID,
		IsRecordingEnabled: isRecordingEnabled,
		StreamId:           streamID,
	}
	if route, err := s.resolveClusterRouteForTenant(ctx, tenantID); err == nil {
		resp.ClusterPeers = route.clusterPeers
		resp.OriginClusterId = route.clusterID
	}
	return resp, nil
}

// ValidateAPIToken validates a developer API token (called by Gateway middleware)
func (s *CommodoreServer) ValidateAPIToken(ctx context.Context, req *pb.ValidateAPITokenRequest) (*pb.ValidateAPITokenResponse, error) {
	token := req.GetToken()
	if token == "" {
		return &pb.ValidateAPITokenResponse{Valid: false}, nil
	}

	var tokenID, userID, tenantID string
	var permissions []string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, tenant_id, permissions
		FROM commodore.api_tokens
		WHERE token_value = $1
		  AND is_active = true
		  AND (expires_at IS NULL OR expires_at > NOW())
	`, hashToken(token)).Scan(&tokenID, &userID, &tenantID, pq.Array(&permissions))

	if errors.Is(err, sql.ErrNoRows) {
		return &pb.ValidateAPITokenResponse{Valid: false}, nil
	}

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Database error validating API token")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Update last used timestamp (best effort)
	_, _ = s.db.ExecContext(ctx, `UPDATE commodore.api_tokens SET last_used_at = NOW() WHERE id = $1`, tokenID)

	// Look up user email and role for context
	var email, role string
	err = s.db.QueryRowContext(ctx, `SELECT email, role FROM commodore.users WHERE id = $1`, userID).Scan(&email, &role)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithFields(logging.Fields{
			"user_id": userID,
			"error":   err,
		}).Warn("Failed to fetch user details for API token")
	}

	return &pb.ValidateAPITokenResponse{
		Valid:       true,
		UserId:      userID,
		TenantId:    tenantID,
		Email:       email,
		Role:        role,
		Permissions: permissions,
		TokenId:     tokenID,
	}, nil
}

// StartDVR initiates DVR recording for a stream (Gateway → Commodore → Foghorn).
func (s *CommodoreServer) StartDVR(ctx context.Context, req *pb.StartDVRRequest) (*pb.StartDVRResponse, error) {
	// Get user and tenant context from gateway metadata.
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	foghornClient, _, err := s.resolveFoghornForTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	// Check if tenant is suspended (prepaid balance < -$10)
	if suspended, suspendErr := s.isTenantSuspended(ctx, tenantID); suspendErr != nil {
		s.logger.WithError(suspendErr).Warn("Failed to check tenant suspension status")
		// Continue anyway - don't block on suspension check failure
	} else if suspended {
		return nil, status.Error(codes.PermissionDenied, "account suspended - please top up your balance to start recordings")
	}

	internalName := req.GetInternalName()
	streamID := req.GetStreamId()
	if internalName == "" {
		if streamID == "" {
			return nil, status.Error(codes.InvalidArgument, "stream_id is required")
		}
		// Resolve internal_name from stream_id (public -> internal)
		if rowErr := s.db.QueryRowContext(ctx, `
			SELECT internal_name FROM commodore.streams WHERE id = $1 AND tenant_id = $2
		`, streamID, tenantID).Scan(&internalName); rowErr != nil {
			if errors.Is(rowErr, sql.ErrNoRows) {
				return nil, status.Error(codes.NotFound, "stream not found")
			}
			return nil, status.Errorf(codes.Internal, "database error: %v", rowErr)
		}
	}

	// Verify stream exists in this tenant (tenant isolation) and resolve stream_id if needed.
	if streamID == "" {
		if rowErr := s.db.QueryRowContext(ctx, `
			SELECT id::text FROM commodore.streams WHERE internal_name = $1 AND tenant_id = $2
		`, internalName, tenantID).Scan(&streamID); rowErr != nil {
			if errors.Is(rowErr, sql.ErrNoRows) {
				return nil, status.Error(codes.NotFound, "stream not found")
			}
			return nil, status.Errorf(codes.Internal, "database error: %v", rowErr)
		}
	}

	// Enforce 30-day default retention if not specified.
	expiresAt := req.ExpiresAt
	if expiresAt == nil || *expiresAt <= 0 {
		expiry := time.Now().Add(30 * 24 * time.Hour).Unix()
		expiresAt = &expiry
	}

	foghornReq := &pb.StartDVRRequest{
		TenantId:     tenantID,
		InternalName: internalName,
		ExpiresAt:    expiresAt,
		UserId:       &userID,
	}
	if streamID != "" {
		foghornReq.StreamId = &streamID
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":     tenantID,
		"internal_name": internalName,
		"user_id":       userID,
	}).Info("Starting DVR recording via Foghorn")

	resp, trailers, err := foghornClient.StartDVR(ctx, foghornReq)
	if err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"tenant_id":     tenantID,
			"internal_name": internalName,
		}).Error("Failed to start DVR via Foghorn")
		return nil, grpcutil.PropagateError(ctx, err, trailers)
	}
	return resp, nil
}

// ============================================================================
// CLIP/DVR REGISTRY (Foghorn → Commodore)
// Business registry for clips and DVR recordings.
// See: docs/architecture/CLIP_DVR_REGISTRY.md
// ============================================================================

// RegisterClip creates a new clip in the business registry
// Called by Foghorn during the CreateClip flow
func (s *CommodoreServer) RegisterClip(ctx context.Context, req *pb.RegisterClipRequest) (*pb.RegisterClipResponse, error) {
	tenantID := req.GetTenantId()
	userID := req.GetUserId()
	streamID := req.GetStreamId()

	if tenantID == "" || userID == "" || streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id, user_id, and stream_id are required")
	}

	// Generate clip hash
	clipHash, err := generateClipHash()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate clip hash: %v", err)
	}
	clipID := uuid.New().String()
	artifactInternalName, playbackID, err := s.generateUniqueArtifactIdentifiers(ctx)
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"stream_id": streamID,
			"error":     err,
		}).Error("Failed to generate artifact identifiers for clip")
		return nil, status.Errorf(codes.Internal, "failed to generate clip identifiers: %v", err)
	}

	// Insert into business registry
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO commodore.clips (
			id, tenant_id, user_id, stream_id, clip_hash, artifact_internal_name, playback_id,
			title, description, start_time, duration, clip_mode, requested_params,
			origin_cluster_id, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, NOW(), NOW())
	`, clipID, tenantID, userID, streamID, clipHash, artifactInternalName, playbackID,
		req.GetTitle(), req.GetDescription(), req.GetStartTime(), req.GetDuration(),
		req.GetClipMode(), req.GetRequestedParams(), req.GetOriginClusterId())

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"stream_id": streamID,
			"error":     err,
		}).Error("Failed to register clip in business registry")
		return nil, status.Errorf(codes.Internal, "failed to register clip: %v", err)
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id": tenantID,
		"clip_hash": clipHash,
		"clip_id":   clipID,
	}).Info("Registered clip in business registry")

	var expiresAt *int64
	if req.GetRetentionUntil() != nil {
		ts := req.GetRetentionUntil().AsTime().Unix()
		expiresAt = &ts
	}
	s.emitArtifactEvent(ctx, eventArtifactRegistered, tenantID, userID, pb.ArtifactEvent_ARTIFACT_TYPE_CLIP, clipHash, streamID, "registered", expiresAt)

	return &pb.RegisterClipResponse{
		ClipHash:             clipHash,
		ClipId:               clipID,
		PlaybackId:           playbackID,
		ArtifactInternalName: artifactInternalName,
	}, nil
}

// RegisterDVR creates a new DVR recording in the business registry
// Called by Foghorn during the StartDVR flow
func (s *CommodoreServer) RegisterDVR(ctx context.Context, req *pb.RegisterDVRRequest) (*pb.RegisterDVRResponse, error) {
	tenantID := req.GetTenantId()
	userID := req.GetUserId()
	internalName := req.GetInternalName()

	if tenantID == "" || userID == "" || internalName == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id, user_id, and internal_name are required")
	}

	// Generate DVR hash
	dvrHash, err := generateDVRHash()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate DVR hash: %v", err)
	}
	dvrID := uuid.New().String()
	artifactInternalName, playbackID, err := s.generateUniqueArtifactIdentifiers(ctx)
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id":     tenantID,
			"internal_name": internalName,
			"error":         err,
		}).Error("Failed to generate artifact identifiers for DVR")
		return nil, status.Errorf(codes.Internal, "failed to generate DVR identifiers: %v", err)
	}

	// Look up stream_id from internal_name
	var streamID string
	err = s.db.QueryRowContext(ctx, `
		SELECT id::text FROM commodore.streams WHERE internal_name = $1 AND tenant_id = $2
	`, internalName, tenantID).Scan(&streamID)
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id":     tenantID,
			"internal_name": internalName,
			"error":         err,
		}).Error("Failed to find stream for DVR")
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "stream not found for internal_name: %s", internalName)
		}
		return nil, status.Errorf(codes.Internal, "database error looking up stream: %v", err)
	}

	// Insert into business registry
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO commodore.dvr_recordings (
			id, tenant_id, user_id, stream_id, dvr_hash, artifact_internal_name, playback_id, internal_name,
			origin_cluster_id, created_at, updated_at
		) VALUES ($1, $2, $3, $4::uuid, $5, $6, $7, $8, $9, NOW(), NOW())
	`, dvrID, tenantID, userID, streamID, dvrHash, artifactInternalName, playbackID, internalName, req.GetOriginClusterId())

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id":     tenantID,
			"internal_name": internalName,
			"error":         err,
		}).Error("Failed to register DVR in business registry")
		return nil, status.Errorf(codes.Internal, "failed to register DVR: %v", err)
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":     tenantID,
		"dvr_hash":      dvrHash,
		"dvr_id":        dvrID,
		"internal_name": internalName,
	}).Info("Registered DVR in business registry")

	var expiresAt *int64
	if req.GetRetentionUntil() != nil {
		ts := req.GetRetentionUntil().AsTime().Unix()
		expiresAt = &ts
	}
	s.emitArtifactEvent(ctx, eventArtifactRegistered, tenantID, userID, pb.ArtifactEvent_ARTIFACT_TYPE_DVR, dvrHash, streamID, "registered", expiresAt)

	return &pb.RegisterDVRResponse{
		DvrHash:              dvrHash,
		DvrId:                dvrID,
		PlaybackId:           playbackID,
		ArtifactInternalName: artifactInternalName,
		StreamId:             streamID,
	}, nil
}

// ResolveClipHash resolves a clip hash to tenant context
// Used for analytics enrichment and playback authorization
func (s *CommodoreServer) ResolveClipHash(ctx context.Context, req *pb.ResolveClipHashRequest) (*pb.ResolveClipHashResponse, error) {
	clipHash := req.GetClipHash()
	if clipHash == "" {
		return nil, status.Error(codes.InvalidArgument, "clip_hash is required")
	}

	var tenantID, userID, streamID, title, description, clipMode string
	var playbackID, artifactInternalName string
	var startTime, duration int64
	var internalName, originClusterID sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT c.tenant_id, c.user_id, c.stream_id, c.title, c.description,
			   c.start_time, c.duration, c.clip_mode, s.internal_name,
			   c.playback_id, c.artifact_internal_name, c.origin_cluster_id
		FROM commodore.clips c
		LEFT JOIN commodore.streams s ON c.stream_id = s.id
		WHERE c.clip_hash = $1
	`, clipHash).Scan(&tenantID, &userID, &streamID, &title, &description,
		&startTime, &duration, &clipMode, &internalName, &playbackID, &artifactInternalName, &originClusterID)

	if errors.Is(err, sql.ErrNoRows) {
		return &pb.ResolveClipHashResponse{
			Found: false,
		}, nil
	}

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"clip_hash": clipHash,
			"error":     err,
		}).Error("Database error resolving clip hash")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	return &pb.ResolveClipHashResponse{
		Found:                true,
		TenantId:             tenantID,
		UserId:               userID,
		StreamId:             streamID,
		InternalName:         internalName.String,
		Title:                title,
		Description:          description,
		StartTime:            startTime,
		Duration:             duration,
		ClipMode:             clipMode,
		PlaybackId:           playbackID,
		ArtifactInternalName: artifactInternalName,
		OriginClusterId:      originClusterID.String,
	}, nil
}

// ResolveDVRHash resolves a DVR hash to tenant context
// Used for analytics enrichment and playback authorization
func (s *CommodoreServer) ResolveDVRHash(ctx context.Context, req *pb.ResolveDVRHashRequest) (*pb.ResolveDVRHashResponse, error) {
	dvrHash := req.GetDvrHash()
	if dvrHash == "" {
		return nil, status.Error(codes.InvalidArgument, "dvr_hash is required")
	}

	var tenantID, userID, internalName string
	var playbackID, artifactInternalName string
	var streamID, originClusterID sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT tenant_id, user_id, stream_id, internal_name, playback_id, artifact_internal_name, origin_cluster_id
		FROM commodore.dvr_recordings
		WHERE dvr_hash = $1
	`, dvrHash).Scan(&tenantID, &userID, &streamID, &internalName, &playbackID, &artifactInternalName, &originClusterID)

	if errors.Is(err, sql.ErrNoRows) {
		return &pb.ResolveDVRHashResponse{
			Found: false,
		}, nil
	}

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"dvr_hash": dvrHash,
			"error":    err,
		}).Error("Database error resolving DVR hash")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	return &pb.ResolveDVRHashResponse{
		Found:                true,
		TenantId:             tenantID,
		UserId:               userID,
		StreamId:             streamID.String,
		InternalName:         internalName,
		PlaybackId:           playbackID,
		ArtifactInternalName: artifactInternalName,
		OriginClusterId:      originClusterID.String,
	}, nil
}

// RegisterVod registers a new VOD asset in the business registry
// Called by Foghorn during CreateVodUpload flow (mirrors DVR/clip pattern)
func (s *CommodoreServer) RegisterVod(ctx context.Context, req *pb.RegisterVodRequest) (*pb.RegisterVodResponse, error) {
	tenantID := req.GetTenantId()
	userID := req.GetUserId()
	filename := req.GetFilename()

	if tenantID == "" || userID == "" || filename == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id, user_id, and filename are required")
	}

	// Generate VOD hash
	vodHash, err := generateVodHash()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate VOD hash: %v", err)
	}
	vodID := uuid.New().String()
	artifactInternalName, playbackID, err := s.generateUniqueArtifactIdentifiers(ctx)
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"filename":  filename,
			"error":     err,
		}).Error("Failed to generate artifact identifiers for VOD")
		return nil, status.Errorf(codes.Internal, "failed to generate VOD identifiers: %v", err)
	}

	// Resolve retention (default 90 days for VOD)
	retentionUntil := time.Now().Add(90 * 24 * time.Hour)

	// Insert into business registry
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO commodore.vod_assets (
			id, tenant_id, user_id, vod_hash, artifact_internal_name, playback_id,
			title, description, filename, content_type, size_bytes,
			origin_cluster_id, retention_until, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, NOW(), NOW())
	`, vodID, tenantID, userID, vodHash, artifactInternalName, playbackID,
		req.GetTitle(), req.GetDescription(), filename, req.GetContentType(), req.GetSizeBytes(),
		req.GetOriginClusterId(), retentionUntil)

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"filename":  filename,
			"error":     err,
		}).Error("Failed to register VOD in business registry")
		return nil, status.Errorf(codes.Internal, "failed to register VOD: %v", err)
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id": tenantID,
		"vod_hash":  vodHash,
		"vod_id":    vodID,
		"filename":  filename,
	}).Info("Registered VOD in business registry")

	expiresAt := retentionUntil.Unix()
	s.emitArtifactEvent(ctx, eventArtifactRegistered, tenantID, userID, pb.ArtifactEvent_ARTIFACT_TYPE_VOD, vodHash, "", "registered", &expiresAt)

	return &pb.RegisterVodResponse{
		VodHash:              vodHash,
		VodId:                vodID,
		PlaybackId:           playbackID,
		ArtifactInternalName: artifactInternalName,
	}, nil
}

// ResolveVodHash resolves a VOD hash to tenant context
// Used for analytics enrichment, playback authorization, and lifecycle operations
func (s *CommodoreServer) ResolveVodHash(ctx context.Context, req *pb.ResolveVodHashRequest) (*pb.ResolveVodHashResponse, error) {
	vodHash := req.GetVodHash()
	if vodHash == "" {
		return nil, status.Error(codes.InvalidArgument, "vod_hash is required")
	}

	var tenantID, userID, filename string
	var playbackID, artifactInternalName string
	var title, description, originClusterID sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT tenant_id, user_id, filename, title, description, playback_id, artifact_internal_name, origin_cluster_id
		FROM commodore.vod_assets
		WHERE vod_hash = $1
	`, vodHash).Scan(&tenantID, &userID, &filename, &title, &description, &playbackID, &artifactInternalName, &originClusterID)

	if errors.Is(err, sql.ErrNoRows) {
		return &pb.ResolveVodHashResponse{
			Found: false,
		}, nil
	}

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"vod_hash": vodHash,
			"error":    err,
		}).Error("Database error resolving VOD hash")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	return &pb.ResolveVodHashResponse{
		Found:                true,
		TenantId:             tenantID,
		UserId:               userID,
		Filename:             filename,
		Title:                title.String,
		Description:          description.String,
		PlaybackId:           playbackID,
		ArtifactInternalName: artifactInternalName,
		OriginClusterId:      originClusterID.String,
	}, nil
}

// ResolveVodID resolves a VOD relay ID (commodore.vod_assets.id) to vod_hash + tenant context
func (s *CommodoreServer) ResolveVodID(ctx context.Context, req *pb.ResolveVodIDRequest) (*pb.ResolveVodIDResponse, error) {
	vodID := req.GetVodId()
	if vodID == "" {
		return nil, status.Error(codes.InvalidArgument, "vod_id is required")
	}

	var tenantID, userID, vodHash, playbackID, artifactInternalName string
	err := s.db.QueryRowContext(ctx, `
		SELECT tenant_id, user_id, vod_hash, playback_id, artifact_internal_name
		FROM commodore.vod_assets
		WHERE id = $1
	`, vodID).Scan(&tenantID, &userID, &vodHash, &playbackID, &artifactInternalName)

	if errors.Is(err, sql.ErrNoRows) {
		return &pb.ResolveVodIDResponse{
			Found: false,
		}, nil
	}
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"vod_id": vodID,
			"error":  err,
		}).Error("Database error resolving VOD id")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	return &pb.ResolveVodIDResponse{
		Found:                true,
		TenantId:             tenantID,
		UserId:               userID,
		VodHash:              vodHash,
		PlaybackId:           playbackID,
		ArtifactInternalName: artifactInternalName,
	}, nil
}

// ResolveArtifactPlaybackID resolves an artifact playback ID to artifact identity
func (s *CommodoreServer) ResolveArtifactPlaybackID(ctx context.Context, req *pb.ResolveArtifactPlaybackIDRequest) (*pb.ResolveArtifactPlaybackIDResponse, error) {
	playbackID := req.GetPlaybackId()
	if playbackID == "" {
		return nil, status.Error(codes.InvalidArgument, "playback_id is required")
	}

	// 1. Clips
	var (
		artifactHash         string
		artifactInternalName string
		tenantID             string
		userID               string
		streamID             sql.NullString
		originClusterID      sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT clip_hash, artifact_internal_name, tenant_id, user_id, stream_id::text, origin_cluster_id
		FROM commodore.clips
		WHERE playback_id = $1
	`, playbackID).Scan(&artifactHash, &artifactInternalName, &tenantID, &userID, &streamID, &originClusterID)
	if err == nil {
		resp := &pb.ResolveArtifactPlaybackIDResponse{
			Found:                true,
			ArtifactHash:         artifactHash,
			ArtifactInternalName: artifactInternalName,
			TenantId:             tenantID,
			UserId:               userID,
			StreamId:             streamID.String,
			ContentType:          "clip",
			OriginClusterId:      originClusterID.String,
		}
		s.populateArtifactClusterContext(ctx, tenantID, &resp.ClusterPeers)
		return resp, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithFields(logging.Fields{
			"playback_id": playbackID,
			"error":       err,
		}).Error("Database error resolving clip playback_id")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// 2. DVR
	originClusterID = sql.NullString{}
	err = s.db.QueryRowContext(ctx, `
		SELECT dvr_hash, artifact_internal_name, tenant_id, user_id, stream_id::text, origin_cluster_id
		FROM commodore.dvr_recordings
		WHERE playback_id = $1
	`, playbackID).Scan(&artifactHash, &artifactInternalName, &tenantID, &userID, &streamID, &originClusterID)
	if err == nil {
		resp := &pb.ResolveArtifactPlaybackIDResponse{
			Found:                true,
			ArtifactHash:         artifactHash,
			ArtifactInternalName: artifactInternalName,
			TenantId:             tenantID,
			UserId:               userID,
			StreamId:             streamID.String,
			ContentType:          "dvr",
			OriginClusterId:      originClusterID.String,
		}
		s.populateArtifactClusterContext(ctx, tenantID, &resp.ClusterPeers)
		return resp, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithFields(logging.Fields{
			"playback_id": playbackID,
			"error":       err,
		}).Error("Database error resolving DVR playback_id")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// 3. VOD
	streamID = sql.NullString{}
	originClusterID = sql.NullString{}
	err = s.db.QueryRowContext(ctx, `
		SELECT vod_hash, artifact_internal_name, tenant_id, user_id, origin_cluster_id
		FROM commodore.vod_assets
		WHERE playback_id = $1
	`, playbackID).Scan(&artifactHash, &artifactInternalName, &tenantID, &userID, &originClusterID)
	if err == nil {
		resp := &pb.ResolveArtifactPlaybackIDResponse{
			Found:                true,
			ArtifactHash:         artifactHash,
			ArtifactInternalName: artifactInternalName,
			TenantId:             tenantID,
			UserId:               userID,
			ContentType:          "vod",
			OriginClusterId:      originClusterID.String,
		}
		s.populateArtifactClusterContext(ctx, tenantID, &resp.ClusterPeers)
		return resp, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithFields(logging.Fields{
			"playback_id": playbackID,
			"error":       err,
		}).Error("Database error resolving VOD playback_id")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	return &pb.ResolveArtifactPlaybackIDResponse{Found: false}, nil
}

func (s *CommodoreServer) populateArtifactClusterContext(ctx context.Context, tenantID string, peers *[]*pb.TenantClusterPeer) {
	if tenantID == "" || peers == nil {
		return
	}
	if route, err := s.resolveClusterRouteForTenant(ctx, tenantID); err == nil {
		*peers = route.clusterPeers
	}
}

// ResolveArtifactInternalName resolves an artifact internal routing name to artifact identity
func (s *CommodoreServer) ResolveArtifactInternalName(ctx context.Context, req *pb.ResolveArtifactInternalNameRequest) (*pb.ResolveArtifactInternalNameResponse, error) {
	internalName := req.GetArtifactInternalName()
	if internalName == "" {
		return nil, status.Error(codes.InvalidArgument, "artifact_internal_name is required")
	}

	// 1. Clips
	var (
		artifactHash         string
		artifactInternalName string
		tenantID             string
		userID               string
		streamID             sql.NullString
		originClusterID      sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT clip_hash, artifact_internal_name, tenant_id, user_id, stream_id::text, origin_cluster_id
		FROM commodore.clips
		WHERE artifact_internal_name = $1
	`, internalName).Scan(&artifactHash, &artifactInternalName, &tenantID, &userID, &streamID, &originClusterID)
	if err == nil {
		resp := &pb.ResolveArtifactInternalNameResponse{
			Found:                true,
			ArtifactHash:         artifactHash,
			ArtifactInternalName: artifactInternalName,
			TenantId:             tenantID,
			UserId:               userID,
			StreamId:             streamID.String,
			ContentType:          "clip",
			OriginClusterId:      originClusterID.String,
		}
		s.populateArtifactClusterContext(ctx, tenantID, &resp.ClusterPeers)
		return resp, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithFields(logging.Fields{
			"artifact_internal_name": internalName,
			"error":                  err,
		}).Error("Database error resolving clip artifact_internal_name")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// 2. DVR
	originClusterID = sql.NullString{}
	err = s.db.QueryRowContext(ctx, `
		SELECT dvr_hash, artifact_internal_name, tenant_id, user_id, stream_id::text, origin_cluster_id
		FROM commodore.dvr_recordings
		WHERE artifact_internal_name = $1
	`, internalName).Scan(&artifactHash, &artifactInternalName, &tenantID, &userID, &streamID, &originClusterID)
	if err == nil {
		resp := &pb.ResolveArtifactInternalNameResponse{
			Found:                true,
			ArtifactHash:         artifactHash,
			ArtifactInternalName: artifactInternalName,
			TenantId:             tenantID,
			UserId:               userID,
			StreamId:             streamID.String,
			ContentType:          "dvr",
			OriginClusterId:      originClusterID.String,
		}
		s.populateArtifactClusterContext(ctx, tenantID, &resp.ClusterPeers)
		return resp, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithFields(logging.Fields{
			"artifact_internal_name": internalName,
			"error":                  err,
		}).Error("Database error resolving DVR artifact_internal_name")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// 3. VOD
	streamID = sql.NullString{}
	originClusterID = sql.NullString{}
	err = s.db.QueryRowContext(ctx, `
		SELECT vod_hash, artifact_internal_name, tenant_id, user_id, origin_cluster_id
		FROM commodore.vod_assets
		WHERE artifact_internal_name = $1
	`, internalName).Scan(&artifactHash, &artifactInternalName, &tenantID, &userID, &originClusterID)
	if err == nil {
		resp := &pb.ResolveArtifactInternalNameResponse{
			Found:                true,
			ArtifactHash:         artifactHash,
			ArtifactInternalName: artifactInternalName,
			TenantId:             tenantID,
			UserId:               userID,
			ContentType:          "vod",
			OriginClusterId:      originClusterID.String,
		}
		s.populateArtifactClusterContext(ctx, tenantID, &resp.ClusterPeers)
		return resp, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithFields(logging.Fields{
			"artifact_internal_name": internalName,
			"error":                  err,
		}).Error("Database error resolving VOD artifact_internal_name")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	return &pb.ResolveArtifactInternalNameResponse{Found: false}, nil
}

// ResolveIdentifier provides unified resolution across all Commodore registries.
// Enriches found responses with cluster context from Quartermaster.
func (s *CommodoreServer) ResolveIdentifier(ctx context.Context, req *pb.ResolveIdentifierRequest) (*pb.ResolveIdentifierResponse, error) {
	resp, err := s.resolveIdentifierLookup(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.Found && resp.TenantId != "" {
		if route, routeErr := s.resolveClusterRouteForTenant(ctx, resp.TenantId); routeErr == nil {
			resp.ClusterPeers = route.clusterPeers
			resp.OriginClusterId = route.clusterID
		}
	}
	return resp, nil
}

// resolveIdentifierLookup checks all Commodore registries for the identifier.
// Lookup order: streams (stream_id, internal_name, playback_id), clips, DVR, VOD
func (s *CommodoreServer) resolveIdentifierLookup(ctx context.Context, req *pb.ResolveIdentifierRequest) (*pb.ResolveIdentifierResponse, error) {
	identifier := req.GetIdentifier()
	if identifier == "" {
		return nil, status.Error(codes.InvalidArgument, "identifier is required")
	}

	// 0. Try streams by stream_id (UUID)
	if _, err := uuid.Parse(identifier); err == nil {
		var streamID, tenantID, userID, internalName string
		var isRecordingEnabled bool
		err := s.db.QueryRowContext(ctx, `
			SELECT id, tenant_id, user_id, internal_name, is_recording_enabled
			FROM commodore.streams WHERE id = $1
		`, identifier).Scan(&streamID, &tenantID, &userID, &internalName, &isRecordingEnabled)
		if err == nil {
			return &pb.ResolveIdentifierResponse{
				Found:              true,
				TenantId:           tenantID,
				UserId:             userID,
				InternalName:       internalName,
				IdentifierType:     "stream_id",
				IsRecordingEnabled: isRecordingEnabled,
				StreamId:           streamID,
			}, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			s.logger.WithError(err).Error("Database error checking streams by stream_id")
		}

		var vodTenantID, vodUserID string
		err = s.db.QueryRowContext(ctx, `
			SELECT tenant_id, user_id
			FROM commodore.vod_assets WHERE id = $1
		`, identifier).Scan(&vodTenantID, &vodUserID)
		if err == nil {
			return &pb.ResolveIdentifierResponse{
				Found:          true,
				TenantId:       vodTenantID,
				UserId:         vodUserID,
				IdentifierType: "vod_id",
			}, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			s.logger.WithError(err).Error("Database error checking VOD by id")
		}
	}

	// 1. Try streams by internal_name (most common for live stream events)
	var streamID, tenantID, userID string
	var isRecordingEnabled bool
	err := s.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, user_id, is_recording_enabled FROM commodore.streams WHERE internal_name = $1
	`, identifier).Scan(&streamID, &tenantID, &userID, &isRecordingEnabled)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:              true,
			TenantId:           tenantID,
			UserId:             userID,
			InternalName:       identifier,
			IdentifierType:     "stream",
			IsRecordingEnabled: isRecordingEnabled,
			StreamId:           streamID,
		}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).Error("Database error checking streams by internal_name")
	}

	// 2. Try streams by playback_id
	var internalName string
	err = s.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, user_id, internal_name, is_recording_enabled
		FROM commodore.streams WHERE playback_id = $1
	`, identifier).Scan(&streamID, &tenantID, &userID, &internalName, &isRecordingEnabled)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:              true,
			TenantId:           tenantID,
			UserId:             userID,
			InternalName:       internalName,
			IdentifierType:     "playback_id",
			IsRecordingEnabled: isRecordingEnabled,
			StreamId:           streamID,
		}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).Error("Database error checking streams by playback_id")
	}

	// 2b. Try artifact playback_id (clip)
	var parentInternalName sql.NullString
	err = s.db.QueryRowContext(ctx, `
		SELECT c.tenant_id, c.user_id, s.internal_name, c.stream_id
		FROM commodore.clips c
		LEFT JOIN commodore.streams s ON c.stream_id = s.id
		WHERE c.playback_id = $1
	`, identifier).Scan(&tenantID, &userID, &parentInternalName, &streamID)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:          true,
			TenantId:       tenantID,
			UserId:         userID,
			InternalName:   parentInternalName.String,
			IdentifierType: "clip_playback_id",
			StreamId:       streamID,
		}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).Error("Database error checking clips by playback_id")
	}

	// 2c. Try artifact playback_id (DVR)
	err = s.db.QueryRowContext(ctx, `
		SELECT tenant_id, user_id, internal_name, stream_id
		FROM commodore.dvr_recordings
		WHERE playback_id = $1
	`, identifier).Scan(&tenantID, &userID, &internalName, &streamID)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:          true,
			TenantId:       tenantID,
			UserId:         userID,
			InternalName:   internalName,
			IdentifierType: "dvr_playback_id",
			StreamId:       streamID,
		}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).Error("Database error checking DVR by playback_id")
	}

	// 2d. Try artifact playback_id (VOD)
	err = s.db.QueryRowContext(ctx, `
		SELECT tenant_id, user_id
		FROM commodore.vod_assets
		WHERE playback_id = $1
	`, identifier).Scan(&tenantID, &userID)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:          true,
			TenantId:       tenantID,
			UserId:         userID,
			IdentifierType: "vod_playback_id",
		}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).Error("Database error checking VOD by playback_id")
	}

	// 2e. Try artifact internal_name (clip)
	err = s.db.QueryRowContext(ctx, `
		SELECT c.tenant_id, c.user_id, s.internal_name, c.stream_id
		FROM commodore.clips c
		LEFT JOIN commodore.streams s ON c.stream_id = s.id
		WHERE c.artifact_internal_name = $1
	`, identifier).Scan(&tenantID, &userID, &parentInternalName, &streamID)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:          true,
			TenantId:       tenantID,
			UserId:         userID,
			InternalName:   parentInternalName.String,
			IdentifierType: "clip_internal_name",
			StreamId:       streamID,
		}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).Error("Database error checking clips by artifact_internal_name")
	}

	// 2f. Try artifact internal_name (DVR)
	err = s.db.QueryRowContext(ctx, `
		SELECT tenant_id, user_id, internal_name, stream_id
		FROM commodore.dvr_recordings
		WHERE artifact_internal_name = $1
	`, identifier).Scan(&tenantID, &userID, &internalName, &streamID)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:          true,
			TenantId:       tenantID,
			UserId:         userID,
			InternalName:   internalName,
			IdentifierType: "dvr_internal_name",
			StreamId:       streamID,
		}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).Error("Database error checking DVR by artifact_internal_name")
	}

	// 2g. Try artifact internal_name (VOD)
	err = s.db.QueryRowContext(ctx, `
		SELECT tenant_id, user_id
		FROM commodore.vod_assets
		WHERE artifact_internal_name = $1
	`, identifier).Scan(&tenantID, &userID)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:          true,
			TenantId:       tenantID,
			UserId:         userID,
			IdentifierType: "vod_internal_name",
		}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).Error("Database error checking VOD by artifact_internal_name")
	}

	// 3. Try clips by clip_hash
	err = s.db.QueryRowContext(ctx, `
		SELECT c.tenant_id, c.user_id, s.internal_name, c.stream_id
		FROM commodore.clips c
		LEFT JOIN commodore.streams s ON c.stream_id = s.id
		WHERE c.clip_hash = $1
	`, identifier).Scan(&tenantID, &userID, &parentInternalName, &streamID)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:          true,
			TenantId:       tenantID,
			UserId:         userID,
			InternalName:   parentInternalName.String,
			IdentifierType: "clip",
			StreamId:       streamID,
		}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).Error("Database error checking clips")
	}

	// 4. Try DVR by dvr_hash
	err = s.db.QueryRowContext(ctx, `
		SELECT tenant_id, user_id, internal_name, stream_id FROM commodore.dvr_recordings WHERE dvr_hash = $1
	`, identifier).Scan(&tenantID, &userID, &internalName, &streamID)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:          true,
			TenantId:       tenantID,
			UserId:         userID,
			InternalName:   internalName,
			IdentifierType: "dvr",
			StreamId:       streamID,
		}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).Error("Database error checking DVR")
	}

	// 5. Try VOD by vod_hash
	err = s.db.QueryRowContext(ctx, `
		SELECT tenant_id, user_id FROM commodore.vod_assets WHERE vod_hash = $1
	`, identifier).Scan(&tenantID, &userID)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:          true,
			TenantId:       tenantID,
			UserId:         userID,
			IdentifierType: "vod",
		}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).Error("Database error checking VOD")
	}

	// Not found in any registry
	return &pb.ResolveIdentifierResponse{
		Found: false,
	}, nil
}

// ============================================================================
// WALLET IDENTITY (x402 / Agent Access)
// ============================================================================

// GetOrCreateWalletUser looks up or creates a tenant/user for a verified wallet address.
// This is called by x402 middleware after verifying the ERC-3009 payment signature.
// If the wallet is not known, creates a new tenant (prepaid) + user (email=NULL) + wallet_identity.
func (s *CommodoreServer) GetOrCreateWalletUser(ctx context.Context, req *pb.GetOrCreateWalletUserRequest) (*pb.GetOrCreateWalletUserResponse, error) {
	chainType := req.GetChainType()
	walletAddress := req.GetWalletAddress()

	// Validate chain type
	if !auth.IsValidChainType(chainType) {
		return nil, status.Errorf(codes.InvalidArgument, "unsupported chain type: %s", chainType)
	}

	// Normalize wallet address
	normalizedAddress, err := auth.NormalizeAddress(auth.ChainType(chainType), walletAddress)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid wallet address: %v", err)
	}

	// Try to find existing wallet identity (query only commodore.* tables)
	var tenantID, userID string
	err = s.db.QueryRowContext(ctx, `
		SELECT tenant_id, user_id
		FROM commodore.wallet_identities
		WHERE chain_type = $1 AND wallet_address = $2
	`, chainType, normalizedAddress).Scan(&tenantID, &userID)

	if err == nil {
		// Existing wallet found - update last_auth_at
		_, _ = s.db.ExecContext(ctx, `
			UPDATE commodore.wallet_identities
			SET last_auth_at = NOW()
			WHERE chain_type = $1 AND wallet_address = $2
		`, chainType, normalizedAddress)

		// Get billing info via Purser gRPC (not DB JOIN)
		billingModel := "postpaid"
		if s.purserClient != nil {
			billingStatus, billingErr := s.purserClient.GetTenantBillingStatus(ctx, tenantID)
			if billingErr != nil {
				s.logger.WithFields(logging.Fields{
					"tenant_id": tenantID,
					"error":     billingErr,
				}).Warn("Failed to get billing status from Purser, using default")
			} else {
				billingModel = billingStatus.BillingModel
			}
		}

		s.logger.WithFields(logging.Fields{
			"chain_type":     chainType,
			"wallet_address": normalizedAddress,
			"tenant_id":      tenantID,
			"user_id":        userID,
		}).Info("Wallet identity found")

		return &pb.GetOrCreateWalletUserResponse{
			TenantId:      tenantID,
			UserId:        userID,
			IsNew:         false,
			BillingModel:  billingModel,
			WalletAddress: normalizedAddress,
		}, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).Error("Failed to lookup wallet identity")
		return nil, status.Error(codes.Internal, "failed to lookup wallet identity")
	}

	// Wallet not found - create new tenant, user, and wallet identity

	// 1. Create tenant via Quartermaster gRPC (not direct DB INSERT)
	if s.quartermasterClient == nil {
		return nil, status.Error(codes.Internal, "quartermaster client not available")
	}
	tenantName := "Wallet: " + normalizedAddress[:10] + "..."
	tenantResp, err := s.quartermasterClient.CreateTenant(ctx, &pb.CreateTenantRequest{
		Name:        tenantName,
		Attribution: req.GetAttribution(),
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to create tenant via Quartermaster")
		return nil, status.Error(codes.Internal, "failed to create tenant")
	}
	tenantID = tenantResp.Tenant.Id

	// 2. Initialize prepaid account via Purser gRPC (not direct DB INSERT)
	if s.purserClient == nil {
		return nil, status.Error(codes.Internal, "purser client not available")
	}
	_, err = s.purserClient.InitializePrepaidAccount(ctx, tenantID, billing.DefaultCurrency())
	if err != nil {
		s.logger.WithError(err).Error("Failed to initialize prepaid account via Purser")
		return nil, status.Error(codes.Internal, "failed to initialize prepaid account")
	}

	// 3. Create user and wallet identity in local commodore.* tables (owned by this service)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.logger.WithError(err).Error("Failed to begin transaction")
		return nil, status.Error(codes.Internal, "failed to create wallet account")
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	userID = uuid.NewString()
	shortAddr := normalizedAddress
	if len(shortAddr) >= 8 {
		shortAddr = shortAddr[2:8]
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO commodore.users (
			id, tenant_id, email, password_hash,
			role, is_active, verified,
			first_name, last_name,
			created_at, updated_at
		)
		VALUES ($1, $2, NULL, '', 'owner', true, true, $3, '', NOW(), NOW())
	`, userID, tenantID, "Wallet "+shortAddr)
	if err != nil {
		s.logger.WithError(err).Error("Failed to create user")
		return nil, status.Error(codes.Internal, "failed to create user")
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO commodore.wallet_identities (id, wallet_address, chain_type, tenant_id, user_id, created_at, last_auth_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, NOW(), NOW())
	`, normalizedAddress, chainType, tenantID, userID)
	if err != nil {
		s.logger.WithError(err).Error("Failed to create wallet identity")
		return nil, status.Error(codes.Internal, "failed to create wallet identity")
	}

	if err := tx.Commit(); err != nil {
		s.logger.WithError(err).Error("Failed to commit transaction")
		return nil, status.Error(codes.Internal, "failed to create wallet account")
	}

	s.logger.WithFields(logging.Fields{
		"chain_type":     chainType,
		"wallet_address": normalizedAddress,
		"tenant_id":      tenantID,
		"user_id":        userID,
	}).Info("Created new wallet account")

	return &pb.GetOrCreateWalletUserResponse{
		TenantId:      tenantID,
		UserId:        userID,
		IsNew:         true,
		BillingModel:  "prepaid",
		WalletAddress: normalizedAddress,
	}, nil
}

// ============================================================================
// USER SERVICE (Gateway → Commodore for auth flows)
// ============================================================================

// Login authenticates a user and returns a JWT token
func (s *CommodoreServer) Login(ctx context.Context, req *pb.LoginRequest) (*pb.AuthResponse, error) {
	email := req.GetEmail()
	password := req.GetPassword()

	if email == "" || password == "" {
		return nil, status.Error(codes.InvalidArgument, "email and password required")
	}

	// Bot protection: Turnstile (primary) or behavioral (fallback)
	if s.turnstileValidator != nil {
		clientIP := ""
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if ips := md.Get("x-client-ip"); len(ips) > 0 {
				clientIP = ips[0]
			} else if ips := md.Get("x-forwarded-for"); len(ips) > 0 {
				clientIP = strings.Split(ips[0], ",")[0]
			}
		}

		turnstileResp, err := s.turnstileValidator.Verify(ctx, req.GetTurnstileToken(), clientIP)
		if err != nil {
			s.logger.WithError(err).Warn("Turnstile verification request failed")
			if !s.turnstileFailOpen {
				return nil, status.Error(codes.InvalidArgument, "bot verification failed")
			}
		} else if !turnstileResp.Success {
			s.logger.WithFields(logging.Fields{
				"email":       email,
				"client_ip":   clientIP,
				"error_codes": turnstileResp.ErrorCodes,
			}).Warn("Login Turnstile verification failed")
			return nil, status.Error(codes.InvalidArgument, "bot verification failed")
		}
	} else {
		// Fallback: behavioral validation when Turnstile not configured
		if !validateBehavior(req) {
			s.logger.WithField("email", email).Warn("Login behavioral bot check failed")
			return nil, status.Error(codes.InvalidArgument, "bot verification failed")
		}
	}

	// Find user by email
	var user commodoreUserRecord
	err := scanCommodoreUserForLogin(s.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, email, password_hash, first_name, last_name, role, permissions, is_active, verified, created_at, updated_at
		FROM commodore.users WHERE email = $1
	`, email), &user)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.Unauthenticated, "invalid credentials")
	}
	if err != nil {
		s.logger.WithError(err).Error("Database error during login")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Check account status
	if !user.IsActive {
		s.emitAuthEvent(ctx, eventAuthLoginFailed, user.ID, user.TenantID, "password", "", "", "account_inactive")
		return nil, status.Error(codes.Unauthenticated, "account deactivated")
	}
	if !user.IsVerified {
		s.emitAuthEvent(ctx, eventAuthLoginFailed, user.ID, user.TenantID, "password", "", "", "email_not_verified")
		return nil, status.Error(codes.Unauthenticated, "email not verified")
	}

	// Verify password
	if !auth.CheckPassword(password, user.PasswordHash) {
		s.emitAuthEvent(ctx, eventAuthLoginFailed, user.ID, user.TenantID, "password", "", "", "invalid_credentials")
		return nil, status.Error(codes.Unauthenticated, "invalid credentials")
	}

	// Update last login
	_, _ = s.db.ExecContext(ctx, `UPDATE commodore.users SET last_login_at = NOW() WHERE id = $1`, user.ID)

	// Generate JWT access token
	jwtSecret := []byte(config.RequireEnv("JWT_SECRET"))
	token, err := auth.GenerateJWT(user.ID, user.TenantID, user.Email, user.Role, jwtSecret)
	if err != nil {
		s.logger.WithError(err).Error("Failed to generate JWT")
		return nil, status.Errorf(codes.Internal, "failed to generate token: %v", err)
	}

	// Generate refresh token and store in DB
	refreshToken, err := generateRandomString(40)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate refresh token: %v", err)
	}
	refreshHash := hashToken(refreshToken)
	refreshExpiry := time.Now().Add(30 * 24 * time.Hour) // 30 days

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO commodore.refresh_tokens (tenant_id, user_id, token_hash, expires_at)
		VALUES ($1, $2, $3, $4)
	`, user.TenantID, user.ID, refreshHash, refreshExpiry)
	if err != nil {
		s.logger.WithError(err).Error("Failed to store refresh token")
		return nil, status.Errorf(codes.Internal, "failed to create session: %v", err)
	}

	expiresAt := time.Now().Add(15 * time.Minute)
	s.emitAuthEvent(ctx, eventAuthLoginSucceeded, user.ID, user.TenantID, "password", "", "", "")

	return &pb.AuthResponse{
		Token:        token,
		RefreshToken: refreshToken,
		User:         user.toProtoUser("", ""),
		ExpiresAt:    timestamppb.New(expiresAt),
	}, nil
}

// Register creates a new user account
func (s *CommodoreServer) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	email := req.GetEmail()
	password := req.GetPassword()

	if email == "" || password == "" {
		return nil, status.Error(codes.InvalidArgument, "email and password required")
	}

	// Bot protection: Turnstile (primary) or behavioral (fallback)
	if s.turnstileValidator != nil {
		// Get client IP from gRPC metadata if available
		clientIP := ""
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if ips := md.Get("x-client-ip"); len(ips) > 0 {
				clientIP = ips[0]
			} else if ips := md.Get("x-forwarded-for"); len(ips) > 0 {
				clientIP = strings.Split(ips[0], ",")[0]
			}
		}

		turnstileResp, err := s.turnstileValidator.Verify(ctx, req.GetTurnstileToken(), clientIP)
		if err != nil {
			s.logger.WithError(err).Warn("Turnstile verification request failed")
			if !s.turnstileFailOpen {
				return nil, status.Error(codes.InvalidArgument, "bot verification failed")
			}
		} else if !turnstileResp.Success {
			s.logger.WithFields(logging.Fields{
				"email":       email,
				"client_ip":   clientIP,
				"error_codes": turnstileResp.ErrorCodes,
			}).Warn("Turnstile verification failed")
			return nil, status.Error(codes.InvalidArgument, "bot verification failed")
		}
	} else {
		// Fallback: behavioral validation when Turnstile not configured
		if !validateBehavior(req) {
			s.logger.WithField("email", email).Warn("Behavioral bot check failed")
			return nil, status.Error(codes.InvalidArgument, "bot verification failed")
		}
	}

	// Check if user already exists
	var existingID string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM commodore.users WHERE email = $1`, email).Scan(&existingID)
	if err == nil {
		return &pb.RegisterResponse{
			Success: false,
			Message: "user already exists",
		}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Create tenant via Quartermaster
	var tenantID string
	if s.quartermasterClient != nil {
		resp, createErr := s.quartermasterClient.CreateTenant(ctx, &pb.CreateTenantRequest{
			Name:        email, // Use email as initial tenant name
			Attribution: req.GetAttribution(),
		})
		if createErr != nil {
			s.logger.WithError(createErr).Error("Failed to create tenant via Quartermaster")
			return nil, status.Errorf(codes.Internal, "failed to create tenant: %v", createErr)
		}
		tenantID = resp.GetTenant().GetId()
	} else {
		// Fallback for testing without Quartermaster
		tenantID = uuid.New().String()
		s.logger.Warn("Quartermaster client not available, using generated tenant ID")
	}

	// Check user limit via Purser (if available)
	if s.purserClient != nil {
		limitCheck, limitErr := s.purserClient.CheckUserLimit(ctx, tenantID, email)
		if limitErr != nil {
			s.logger.WithError(limitErr).Warn("Failed to check user limit with Purser, proceeding anyway")
		} else if !limitCheck.GetAllowed() {
			return &pb.RegisterResponse{
				Success: false,
				Message: "tenant user limit reached",
			}, nil
		}
	}

	// Hash password
	hashedPassword, err := auth.HashPassword(password)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to hash password: %v", err)
	}

	// Generate verification token
	verificationToken, err := generateSecureToken(32)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate verification token: %v", err)
	}
	tokenHash := hashToken(verificationToken) // Store hash, send raw in email
	tokenExpiry := time.Now().Add(24 * time.Hour)

	// Check if this is the first user for the tenant (becomes owner)
	var userCount int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM commodore.users WHERE tenant_id = $1`, tenantID).Scan(&userCount)
	role := "member"
	if err == nil && userCount == 0 {
		role = "owner"
	}

	// Create user
	userID := uuid.New().String()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO commodore.users (id, tenant_id, email, password_hash, first_name, last_name, role, permissions, is_active, verified, verification_token, token_expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, true, false, $9, $10)
	`, userID, tenantID, email, hashedPassword, req.GetFirstName(), req.GetLastName(), role, pq.Array(getDefaultPermissions(role)), tokenHash, tokenExpiry)

	if err != nil {
		s.logger.WithError(err).Error("Failed to create user")
		return nil, status.Errorf(codes.Internal, "failed to create user: %v", err)
	}

	// Send verification email (best effort, don't fail registration)
	if err := s.sendVerificationEmail(email, verificationToken); err != nil {
		s.logger.WithFields(logging.Fields{
			"user_id":   userID,
			"tenant_id": tenantID,
			"email":     email,
			"error":     err,
		}).Error("Failed to send verification email")
	}

	// Sync to Listmonk (async, best effort)
	if s.listmonkClient != nil {
		go func(email, first, last string) {
			name := strings.TrimSpace(first + " " + last)
			if name == "" {
				name = "Friend"
			}
			if err := s.listmonkClient.Subscribe(context.Background(), email, name, s.defaultMailingListID, true); err != nil {
				s.logger.WithError(err).Warn("Failed to sync new user to Listmonk")
			}
		}(email, req.GetFirstName(), req.GetLastName())
	}

	// Initialize postpaid billing + cluster access for the new tenant
	if s.purserClient != nil && role == "owner" {
		if _, billingErr := s.purserClient.InitializePostpaidAccount(ctx, tenantID); billingErr != nil {
			s.logger.WithError(billingErr).WithField("tenant_id", tenantID).Error("Failed to initialize postpaid account")
		}
	}

	s.logger.WithFields(logging.Fields{
		"user_id":   userID,
		"tenant_id": tenantID,
		"email":     email,
		"role":      role,
	}).Info("User registered successfully via gRPC")

	s.emitAuthEvent(ctx, eventAuthRegistered, userID, tenantID, "password", "", "", "")

	return &pb.RegisterResponse{
		Success: true,
		Message: "Registration successful. Please check your email to verify your account.",
	}, nil
}

// GetMe returns the current user's profile
func (s *CommodoreServer) GetMe(ctx context.Context, req *pb.GetMeRequest) (*pb.User, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	var user commodoreUserRecord

	err = scanCommodoreUserForGetMe(s.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, email, first_name, last_name, role, permissions, is_active, verified, last_login_at, created_at, updated_at
		FROM commodore.users WHERE id = $1 AND tenant_id = $2
	`, userID, tenantID), &user)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "user not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	result := user.toProtoUser("", "")

	// Fetch linked wallets
	walletRows, err := s.db.QueryContext(ctx, `
		SELECT id, wallet_address, created_at, last_auth_at
		FROM commodore.wallet_identities
		WHERE user_id = $1
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to fetch user wallets")
		// Don't fail the whole request - just return user without wallets
	} else {
		defer func() { _ = walletRows.Close() }()
		for walletRows.Next() {
			var walletID, walletAddr string
			var walletCreatedAt time.Time
			var walletLastAuthAt sql.NullTime
			if err := walletRows.Scan(&walletID, &walletAddr, &walletCreatedAt, &walletLastAuthAt); err != nil {
				continue
			}
			wallet := &pb.WalletIdentity{
				Id:            walletID,
				WalletAddress: walletAddr,
				CreatedAt:     timestamppb.New(walletCreatedAt),
			}
			if walletLastAuthAt.Valid {
				wallet.LastAuthAt = timestamppb.New(walletLastAuthAt.Time)
			}
			result.Wallets = append(result.Wallets, wallet)
		}
	}

	return result, nil
}

// Logout invalidates user session (token blacklisting handled at Gateway)
func (s *CommodoreServer) Logout(ctx context.Context, req *pb.LogoutRequest) (*pb.LogoutResponse, error) {
	// Get user context to delete their refresh tokens
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		// Still acknowledge logout even without user context
		//nolint:nilerr // graceful logout even without user context
		return &pb.LogoutResponse{
			Success: true,
			Message: "logged out successfully",
		}, nil
	}

	// Delete all refresh tokens for this user (logs them out of all devices)
	_, err = s.db.ExecContext(ctx, `
		DELETE FROM commodore.refresh_tokens WHERE user_id = $1 AND tenant_id = $2
	`, userID, tenantID)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to delete refresh tokens during logout")
	}

	return &pb.LogoutResponse{
		Success: true,
		Message: "logged out successfully",
	}, nil
}

// RefreshToken exchanges a refresh token for a new access token
func (s *CommodoreServer) RefreshToken(ctx context.Context, req *pb.RefreshTokenRequest) (*pb.AuthResponse, error) {
	refreshToken := req.GetRefreshToken()
	if refreshToken == "" {
		return nil, status.Error(codes.InvalidArgument, "refresh token required")
	}

	// Hash the token and look it up in the database
	tokenHash := hashToken(refreshToken)

	var tokenID, userID, tenantID string
	var revoked bool
	err := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, tenant_id, revoked FROM commodore.refresh_tokens
		WHERE token_hash = $1 AND expires_at > NOW()
	`, tokenHash).Scan(&tokenID, &userID, &tenantID, &revoked)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.Unauthenticated, "invalid or expired refresh token")
	}
	if err != nil {
		s.logger.WithError(err).Error("Database error validating refresh token")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Token reuse detection: if token was already revoked, revoke ALL user tokens (security)
	if revoked {
		s.logger.WithFields(logging.Fields{
			"user_id":   userID,
			"tenant_id": tenantID,
		}).Warn("Refresh token reuse detected, revoking all user sessions")
		_, _ = s.db.ExecContext(ctx, `
			UPDATE commodore.refresh_tokens SET revoked = true
			WHERE user_id = $1 AND tenant_id = $2
		`, userID, tenantID)
		return nil, status.Error(codes.Unauthenticated, "session invalidated")
	}

	// Revoke the old refresh token (don't delete - keep for reuse detection)
	_, _ = s.db.ExecContext(ctx, `
		UPDATE commodore.refresh_tokens SET revoked = true WHERE id = $1
	`, tokenID)

	// Look up user details
	var user commodoreUserRecord
	err = scanCommodoreUserForRefresh(s.db.QueryRowContext(ctx, `
		SELECT email, role, permissions, first_name, last_name, is_active, verified, created_at, updated_at
		FROM commodore.users WHERE id = $1 AND tenant_id = $2
	`, userID, tenantID), &user)

	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "user not found")
	}

	if !user.IsActive {
		return nil, status.Error(codes.Unauthenticated, "account deactivated")
	}

	// Generate new access token
	jwtSecret := []byte(config.RequireEnv("JWT_SECRET"))
	token, err := auth.GenerateJWT(userID, tenantID, user.Email, user.Role, jwtSecret)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate token: %v", err)
	}

	// Generate new refresh token
	newRefreshToken, err := generateRandomString(40)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate refresh token: %v", err)
	}
	newRefreshHash := hashToken(newRefreshToken)
	refreshExpiry := time.Now().Add(30 * 24 * time.Hour)

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO commodore.refresh_tokens (tenant_id, user_id, token_hash, expires_at)
		VALUES ($1, $2, $3, $4)
	`, tenantID, userID, newRefreshHash, refreshExpiry)
	if err != nil {
		s.logger.WithError(err).Error("Failed to store new refresh token")
		// Don't fail - access token is still valid
	}

	expiresAt := time.Now().Add(15 * time.Minute)
	s.emitAuthEvent(ctx, eventAuthTokenRefreshed, userID, tenantID, "refresh_token", "", "", "")

	return &pb.AuthResponse{
		Token:        token,
		RefreshToken: newRefreshToken,
		User:         user.toProtoUser(userID, tenantID),
		ExpiresAt:    timestamppb.New(expiresAt),
	}, nil
}

// VerifyEmail verifies a user's email address with a token
func (s *CommodoreServer) VerifyEmail(ctx context.Context, req *pb.VerifyEmailRequest) (*pb.VerifyEmailResponse, error) {
	token := req.GetToken()
	if token == "" {
		return nil, status.Error(codes.InvalidArgument, "verification token required")
	}

	// Hash token for lookup (stored hashed in DB)
	tokenHash := hashToken(token)

	// Find user by verification token with expiry check
	var userID string
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM commodore.users
		WHERE verification_token = $1 AND verified = false AND token_expires_at > NOW()
	`, tokenHash).Scan(&userID)

	if errors.Is(err, sql.ErrNoRows) {
		return &pb.VerifyEmailResponse{
			Success: false,
			Message: "invalid or expired verification token",
		}, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Mark as verified and clear token
	_, err = s.db.ExecContext(ctx, `
		UPDATE commodore.users
		SET verified = true, verification_token = NULL, token_expires_at = NULL, updated_at = NOW()
		WHERE id = $1
	`, userID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to verify email: %v", err)
	}

	return &pb.VerifyEmailResponse{
		Success: true,
		Message: "email verified successfully",
	}, nil
}

// ResendVerification resends the email verification link
func (s *CommodoreServer) ResendVerification(ctx context.Context, req *pb.ResendVerificationRequest) (*pb.ResendVerificationResponse, error) {
	email := req.GetEmail()
	if email == "" {
		return nil, status.Error(codes.InvalidArgument, "email required")
	}

	// Optional Turnstile verification (if configured)
	if s.turnstileValidator != nil && req.GetTurnstileToken() != "" {
		clientIP := ""
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if ips := md.Get("x-client-ip"); len(ips) > 0 {
				clientIP = ips[0]
			} else if ips := md.Get("x-forwarded-for"); len(ips) > 0 {
				clientIP = strings.Split(ips[0], ",")[0]
			}
		}

		turnstileResp, err := s.turnstileValidator.Verify(ctx, req.GetTurnstileToken(), clientIP)
		if err != nil {
			s.logger.WithError(err).Warn("Turnstile verification request failed")
			if !s.turnstileFailOpen {
				return nil, status.Error(codes.InvalidArgument, "bot verification failed")
			}
		} else if !turnstileResp.Success {
			return nil, status.Error(codes.InvalidArgument, "bot verification failed")
		}
	}

	// Find user by email
	var userID string
	var isVerified bool
	var tokenExpiresAt sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT id, verified, token_expires_at FROM commodore.users WHERE email = $1
	`, email).Scan(&userID, &isVerified, &tokenExpiresAt)

	if errors.Is(err, sql.ErrNoRows) {
		// Don't reveal if email exists - return success anyway
		return &pb.ResendVerificationResponse{
			Success: true,
			Message: "if an account exists with that email and is unverified, a new verification link will be sent",
		}, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Already verified
	if isVerified {
		return &pb.ResendVerificationResponse{
			Success: false,
			Message: "email is already verified",
		}, nil
	}

	// Rate limiting: check if token was generated within last 5 minutes
	if tokenExpiresAt.Valid {
		// Token expiry is 24h from creation, so creation time is expiry - 24h
		tokenCreatedAt := tokenExpiresAt.Time.Add(-24 * time.Hour)
		if time.Since(tokenCreatedAt) < 5*time.Minute {
			return &pb.ResendVerificationResponse{
				Success: false,
				Message: "please wait a few minutes before requesting another verification email",
			}, nil
		}
	}

	// Generate new verification token
	verificationToken, err := generateSecureToken(32)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate verification token: %v", err)
	}
	tokenHash := hashToken(verificationToken)
	tokenExpiry := time.Now().Add(24 * time.Hour)

	// Update user with new token
	_, err = s.db.ExecContext(ctx, `
		UPDATE commodore.users
		SET verification_token = $1, token_expires_at = $2, updated_at = NOW()
		WHERE id = $3
	`, tokenHash, tokenExpiry, userID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate verification token: %v", err)
	}

	// Send verification email
	if err := s.sendVerificationEmail(email, verificationToken); err != nil {
		s.logger.WithFields(logging.Fields{
			"user_id": userID,
			"email":   email,
			"error":   err,
		}).Error("Failed to send verification email")
		//nolint:nilerr // error returned in response message, not as Go error
		return &pb.ResendVerificationResponse{
			Success: false,
			Message: "failed to send verification email, please try again later",
		}, nil
	}

	s.logger.WithFields(logging.Fields{
		"user_id": userID,
		"email":   email,
	}).Info("Verification email resent")

	return &pb.ResendVerificationResponse{
		Success: true,
		Message: "verification email sent",
	}, nil
}

// ForgotPassword initiates the password reset flow
func (s *CommodoreServer) ForgotPassword(ctx context.Context, req *pb.ForgotPasswordRequest) (*pb.ForgotPasswordResponse, error) {
	email := req.GetEmail()
	if email == "" {
		return nil, status.Error(codes.InvalidArgument, "email required")
	}

	// Check if user exists
	var userID string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM commodore.users WHERE email = $1`, email).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		// Don't reveal whether email exists - always return success
		return &pb.ForgotPasswordResponse{
			Success: true,
			Message: "if an account exists with that email, a reset link will be sent",
		}, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Generate reset token and hash for storage (uses HMAC if PASSWORD_RESET_SECRET is configured)
	resetToken, err := generateSecureToken(32)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate reset token: %v", err)
	}
	resetTokenHash := s.hashTokenWithSecret(resetToken)
	expiresAt := time.Now().Add(1 * time.Hour)

	// Store hashed reset token
	_, err = s.db.ExecContext(ctx, `
		UPDATE commodore.users
		SET reset_token = $1, reset_token_expires = $2, updated_at = NOW()
		WHERE id = $3
	`, resetTokenHash, expiresAt, userID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create reset token: %v", err)
	}

	// Send password reset email
	if err := s.sendPasswordResetEmail(email, resetToken); err != nil {
		s.logger.WithFields(logging.Fields{
			"user_id": userID,
			"email":   email,
			"error":   err,
		}).Error("Failed to send password reset email")
		// Don't fail - user may retry
	} else {
		s.logger.WithFields(logging.Fields{
			"user_id": userID,
			"email":   email,
		}).Info("Password reset email sent")
	}

	return &pb.ForgotPasswordResponse{
		Success: true,
		Message: "if an account exists with that email, a reset link will be sent",
	}, nil
}

// ResetPassword resets a user's password with a valid token
func (s *CommodoreServer) ResetPassword(ctx context.Context, req *pb.ResetPasswordRequest) (*pb.ResetPasswordResponse, error) {
	token := req.GetToken()
	password := req.GetPassword()

	if token == "" || password == "" {
		return nil, status.Error(codes.InvalidArgument, "token and password required")
	}

	// Hash token for lookup (uses HMAC if PASSWORD_RESET_SECRET is configured)
	tokenHash := s.hashTokenWithSecret(token)

	// Find user by reset token
	var userID string
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM commodore.users
		WHERE reset_token = $1 AND reset_token_expires > NOW()
	`, tokenHash).Scan(&userID)

	if errors.Is(err, sql.ErrNoRows) {
		return &pb.ResetPasswordResponse{
			Success: false,
			Message: "invalid or expired reset token",
		}, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Hash new password
	hashedPassword, err := auth.HashPassword(password)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to hash password: %v", err)
	}

	// Update password and clear reset token
	_, err = s.db.ExecContext(ctx, `
		UPDATE commodore.users
		SET password_hash = $1, reset_token = NULL, reset_token_expires = NULL, updated_at = NOW()
		WHERE id = $2
	`, hashedPassword, userID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update password: %v", err)
	}

	return &pb.ResetPasswordResponse{
		Success: true,
		Message: "password reset successfully",
	}, nil
}

// UpdateMe updates the current user's profile
func (s *CommodoreServer) UpdateMe(ctx context.Context, req *pb.UpdateMeRequest) (*pb.User, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Build dynamic update query
	updates := []string{}
	args := []interface{}{}
	argCount := 1

	if req.FirstName != nil {
		updates = append(updates, fmt.Sprintf("first_name = $%d", argCount))
		args = append(args, *req.FirstName)
		argCount++
	}
	if req.LastName != nil {
		updates = append(updates, fmt.Sprintf("last_name = $%d", argCount))
		args = append(args, *req.LastName)
		argCount++
	}
	if req.PhoneNumber != nil && *req.PhoneNumber != "" {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	if len(updates) == 0 {
		return nil, status.Error(codes.InvalidArgument, "no fields to update")
	}

	updates = append(updates, "updated_at = NOW()")
	query := fmt.Sprintf("UPDATE commodore.users SET %s WHERE id = $%d AND tenant_id = $%d",
		strings.Join(updates, ", "), argCount, argCount+1)
	args = append(args, userID, tenantID)

	_, err = s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update profile: %v", err)
	}

	// Return updated user
	return s.GetMe(ctx, &pb.GetMeRequest{})
}

// UpdateNewsletter updates the user's newsletter subscription in Listmonk (source of truth)
func (s *CommodoreServer) UpdateNewsletter(ctx context.Context, req *pb.UpdateNewsletterRequest) (*pb.UpdateNewsletterResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Get user email and name from DB
	var email sql.NullString
	var firstName, lastName sql.NullString
	err = s.db.QueryRowContext(ctx, `
		SELECT email, first_name, last_name FROM commodore.users WHERE id = $1 AND tenant_id = $2
	`, userID, tenantID).Scan(&email, &firstName, &lastName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to fetch user: %v", err)
	}

	if !email.Valid || email.String == "" {
		return nil, status.Error(codes.FailedPrecondition, "email required for newsletter subscription")
	}

	name := strings.TrimSpace(firstName.String + " " + lastName.String)
	if name == "" {
		name = email.String
	}

	if s.listmonkClient == nil {
		return nil, status.Error(codes.Unavailable, "newsletter service not configured")
	}

	if req.GetSubscribed() {
		// Subscribe to the newsletter list
		err = s.listmonkClient.Subscribe(ctx, email.String, name, s.defaultMailingListID, true)
	} else {
		// Unsubscribe from the newsletter list (not global blocklist)
		// First get the subscriber ID
		info, exists, lookupErr := s.listmonkClient.GetSubscriber(ctx, email.String)
		if lookupErr != nil {
			s.logger.WithError(lookupErr).WithField("email", email.String).Error("Failed to lookup subscriber in Listmonk")
			return nil, status.Errorf(codes.Internal, "failed to lookup subscriber: %v", lookupErr)
		}
		if !exists {
			// Not subscribed anyway, nothing to do
			return &pb.UpdateNewsletterResponse{
				Success: true,
				Message: "newsletter preference updated",
			}, nil
		}
		err = s.listmonkClient.Unsubscribe(ctx, info.ID, s.defaultMailingListID)
	}
	if err != nil {
		s.logger.WithError(err).WithField("email", email.String).Error("Failed to update newsletter in Listmonk")
		return nil, status.Errorf(codes.Internal, "failed to update newsletter preference: %v", err)
	}

	return &pb.UpdateNewsletterResponse{
		Success: true,
		Message: "newsletter preference updated",
	}, nil
}

// GetNewsletterStatus returns the user's current newsletter subscription status from Listmonk
func (s *CommodoreServer) GetNewsletterStatus(ctx context.Context, req *pb.GetNewsletterStatusRequest) (*pb.GetNewsletterStatusResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Get user email from DB
	var email sql.NullString
	err = s.db.QueryRowContext(ctx, `
		SELECT email FROM commodore.users WHERE id = $1 AND tenant_id = $2
	`, userID, tenantID).Scan(&email)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to fetch user: %v", err)
	}

	if !email.Valid || email.String == "" {
		// Wallet-only users can't have newsletter subscription
		return &pb.GetNewsletterStatusResponse{Subscribed: false}, nil
	}

	if s.listmonkClient == nil {
		return nil, status.Error(codes.Unavailable, "newsletter service not configured")
	}

	// Query Listmonk for subscriber info
	info, exists, err := s.listmonkClient.GetSubscriber(ctx, email.String)
	if err != nil {
		s.logger.WithError(err).WithField("email", email.String).Error("Failed to get subscriber from Listmonk")
		return nil, status.Errorf(codes.Internal, "failed to get newsletter status: %v", err)
	}

	// If subscriber doesn't exist in Listmonk, return unsubscribed
	if !exists {
		return &pb.GetNewsletterStatusResponse{Subscribed: false}, nil
	}

	// Check if subscribed to the newsletter list specifically
	return &pb.GetNewsletterStatusResponse{Subscribed: info.IsSubscribedToList(s.defaultMailingListID)}, nil
}

// ============================================================================
// WALLET AUTHENTICATION (x402 / agent access)
// ============================================================================

// WalletLogin authenticates via Ethereum wallet signature
// If the wallet is not linked to any account, creates a new one (auto-provisioning)
func (s *CommodoreServer) WalletLogin(ctx context.Context, req *pb.WalletLoginRequest) (*pb.AuthResponse, error) {
	walletAddr := req.GetWalletAddress()
	message := req.GetMessage()
	signature := req.GetSignature()

	if walletAddr == "" || message == "" || signature == "" {
		return nil, status.Error(codes.InvalidArgument, "wallet_address, message, and signature required")
	}

	// Verify the signature
	valid, err := auth.VerifyWalletAuth(auth.WalletMessage{
		Address:   walletAddr,
		Message:   message,
		Signature: signature,
	})
	if err != nil {
		s.logger.WithError(err).WithField("wallet", walletAddr).Warn("Wallet signature verification failed")
		return nil, status.Errorf(codes.InvalidArgument, "signature verification failed: %v", err)
	}
	if !valid {
		return nil, status.Error(codes.Unauthenticated, "invalid signature")
	}

	// Normalize address
	normalizedAddr, err := auth.NormalizeEthAddress(walletAddr)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid address: %v", err)
	}

	// Resolve or create wallet identity (single source of truth)
	attr := req.GetAttribution()
	if attr == nil {
		attr = &pb.SignupAttribution{
			SignupChannel: "wallet",
			SignupMethod:  "wallet_ethereum",
		}
	}
	walletResp, err := s.GetOrCreateWalletUser(ctx, &pb.GetOrCreateWalletUserRequest{
		ChainType:     string(auth.ChainEthereum),
		WalletAddress: normalizedAddr,
		Attribution:   attr,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to resolve wallet user: %v", err)
	}

	userID := walletResp.GetUserId()
	tenantID := walletResp.GetTenantId()
	isNewUser := walletResp.GetIsNew()

	var email sql.NullString
	var firstName, lastName, role string
	var isActive, isVerified bool
	var lastLoginAt sql.NullTime
	var createdAt, updatedAt time.Time

	err = s.db.QueryRowContext(ctx, `
		SELECT email, first_name, last_name, role, is_active, verified,
		       last_login_at, created_at, updated_at
		FROM commodore.users WHERE id = $1
	`, userID).Scan(&email, &firstName, &lastName, &role,
		&isActive, &isVerified, &lastLoginAt, &createdAt, &updatedAt)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to fetch user: %v", err)
	}

	// Update last_auth_at on wallet identity
	_, _ = s.db.ExecContext(ctx, `
		UPDATE commodore.wallet_identities
		SET last_auth_at = NOW()
		WHERE chain_type = 'ethereum' AND wallet_address = $1
	`, normalizedAddr)

	// Update last_login_at on user
	_, _ = s.db.ExecContext(ctx, `
		UPDATE commodore.users SET last_login_at = NOW() WHERE id = $1
	`, userID)

	// Generate JWT
	jwtSecret := []byte(config.RequireEnv("JWT_SECRET"))
	var emailStr string
	if email.Valid {
		emailStr = email.String
	}
	token, err := auth.GenerateJWT(userID, tenantID, emailStr, role, jwtSecret)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate token: %v", err)
	}
	expiresAt := time.Now().Add(15 * time.Minute)

	// Build user response
	user := &pb.User{
		Id:         userID,
		TenantId:   tenantID,
		FirstName:  firstName,
		LastName:   lastName,
		Role:       role,
		IsActive:   isActive,
		IsVerified: isVerified,
		CreatedAt:  timestamppb.New(createdAt),
		UpdatedAt:  timestamppb.New(updatedAt),
	}
	if email.Valid {
		user.Email = &email.String
	}
	if lastLoginAt.Valid {
		user.LastLoginAt = timestamppb.New(lastLoginAt.Time)
	}

	s.emitAuthEvent(ctx, eventAuthLoginSucceeded, userID, tenantID, "wallet", "", "", "")

	return &pb.AuthResponse{
		Token:     token,
		User:      user,
		ExpiresAt: timestamppb.New(expiresAt),
		IsNewUser: isNewUser,
	}, nil
}

// WalletLoginWithX402 authenticates via x402 payload and returns a session token.
// If payment value > 0, it settles the payment and credits the target tenant (or payer if none specified).
func (s *CommodoreServer) WalletLoginWithX402(ctx context.Context, req *pb.WalletLoginWithX402Request) (*pb.WalletLoginWithX402Response, error) {
	if s.purserClient == nil {
		return nil, status.Error(codes.Unavailable, "purser client not configured")
	}

	payment := req.GetPayment()
	if payment == nil {
		return nil, status.Error(codes.InvalidArgument, "payment required")
	}

	verifyResp, err := s.purserClient.VerifyX402Payment(ctx, "", payment, req.GetClientIp())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "payment verification failed: %v", err)
	}
	if !verifyResp.Valid {
		return nil, status.Errorf(codes.Unauthenticated, "payment invalid: %s", verifyResp.Error)
	}

	payerAddress := verifyResp.PayerAddress
	if payerAddress == "" {
		return nil, status.Error(codes.InvalidArgument, "payer address missing")
	}

	chainType := x402NetworkToChainType(payment.GetNetwork())
	attr := req.GetAttribution()
	if attr == nil {
		signupMethod := "x402"
		if payment.GetNetwork() != "" {
			signupMethod = "x402_" + strings.ToLower(payment.GetNetwork())
		}
		attr = &pb.SignupAttribution{
			SignupChannel: "x402",
			SignupMethod:  signupMethod,
		}
	}
	walletResp, err := s.GetOrCreateWalletUser(ctx, &pb.GetOrCreateWalletUserRequest{
		ChainType:     chainType,
		WalletAddress: payerAddress,
		Attribution:   attr,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to resolve wallet user: %v", err)
	}

	userID := walletResp.GetUserId()
	tenantID := walletResp.GetTenantId()
	isNewUser := walletResp.GetIsNew()

	var email sql.NullString
	var firstName, lastName, role string
	var isActive, isVerified bool
	var lastLoginAt sql.NullTime
	var createdAt, updatedAt time.Time

	err = s.db.QueryRowContext(ctx, `
		SELECT email, first_name, last_name, role, is_active, verified,
		       last_login_at, created_at, updated_at
		FROM commodore.users WHERE id = $1
	`, userID).Scan(&email, &firstName, &lastName, &role,
		&isActive, &isVerified, &lastLoginAt, &createdAt, &updatedAt)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to fetch user: %v", err)
	}

	// Update last_auth_at on wallet identity
	_, _ = s.db.ExecContext(ctx, `
		UPDATE commodore.wallet_identities
		SET last_auth_at = NOW()
		WHERE chain_type = $1 AND wallet_address = $2
	`, chainType, walletResp.GetWalletAddress())

	// Update last_login_at on user
	_, _ = s.db.ExecContext(ctx, `
		UPDATE commodore.users SET last_login_at = NOW() WHERE id = $1
	`, userID)

	// Generate JWT
	jwtSecret := []byte(config.RequireEnv("JWT_SECRET"))
	var emailStr string
	if email.Valid {
		emailStr = email.String
	}
	token, err := auth.GenerateJWT(userID, tenantID, emailStr, role, jwtSecret)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate token: %v", err)
	}
	expiresAt := time.Now().Add(15 * time.Minute)

	user := &pb.User{
		Id:         userID,
		TenantId:   tenantID,
		FirstName:  firstName,
		LastName:   lastName,
		Role:       role,
		IsActive:   isActive,
		IsVerified: isVerified,
		CreatedAt:  timestamppb.New(createdAt),
		UpdatedAt:  timestamppb.New(updatedAt),
	}
	if email.Valid {
		user.Email = &email.String
	}
	if lastLoginAt.Valid {
		user.LastLoginAt = timestamppb.New(lastLoginAt.Time)
	}

	s.emitAuthEvent(ctx, eventAuthLoginSucceeded, userID, tenantID, "x402", "", "", "")

	authResp := &pb.AuthResponse{
		Token:     token,
		User:      user,
		ExpiresAt: timestamppb.New(expiresAt),
		IsNewUser: isNewUser,
	}

	if verifyResp.IsAuthOnly {
		return &pb.WalletLoginWithX402Response{
			Auth:           authResp,
			IsAuthOnly:     true,
			PayerAddress:   payerAddress,
			TargetTenantId: tenantID,
		}, nil
	}

	targetTenantID := req.GetTargetTenantId()
	if targetTenantID == "" {
		targetTenantID = tenantID
	}

	settleResp, err := s.purserClient.SettleX402Payment(ctx, targetTenantID, payment, req.GetClientIp())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "payment settlement failed: %v", err)
	}
	if !settleResp.Success {
		return nil, status.Errorf(codes.Internal, "payment settlement failed: %s", settleResp.Error)
	}

	return &pb.WalletLoginWithX402Response{
		Auth:            authResp,
		IsAuthOnly:      false,
		CreditedCents:   settleResp.CreditedCents,
		NewBalanceCents: settleResp.NewBalanceCents,
		TxHash:          settleResp.TxHash,
		Currency:        settleResp.Currency,
		InvoiceNumber:   settleResp.InvoiceNumber,
		PayerAddress:    settleResp.PayerAddress,
		TargetTenantId:  targetTenantID,
	}, nil
}

func x402NetworkToChainType(network string) string {
	switch strings.ToLower(network) {
	case "base", "base-mainnet", "base-sepolia":
		return string(auth.ChainBase)
	case "arbitrum", "arbitrum-one":
		return string(auth.ChainArbitrum)
	case "ethereum", "mainnet":
		return string(auth.ChainEthereum)
	default:
		return string(auth.ChainEthereum)
	}
}

// LinkWallet links a wallet to the authenticated user's account
func (s *CommodoreServer) LinkWallet(ctx context.Context, req *pb.LinkWalletRequest) (*pb.WalletIdentity, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	walletAddr := req.GetWalletAddress()
	message := req.GetMessage()
	signature := req.GetSignature()

	if walletAddr == "" || message == "" || signature == "" {
		return nil, status.Error(codes.InvalidArgument, "wallet_address, message, and signature required")
	}

	// Verify the signature
	valid, err := auth.VerifyWalletAuth(auth.WalletMessage{
		Address:   walletAddr,
		Message:   message,
		Signature: signature,
	})
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "signature verification failed: %v", err)
	}
	if !valid {
		return nil, status.Error(codes.Unauthenticated, "invalid signature")
	}

	// Normalize address
	normalizedAddr, err := auth.NormalizeEthAddress(walletAddr)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid address: %v", err)
	}

	// Check if wallet is already linked to another user
	var existingUserID string
	err = s.db.QueryRowContext(ctx, `
		SELECT user_id FROM commodore.wallet_identities
		WHERE chain_type = 'ethereum' AND wallet_address = $1
	`, normalizedAddr).Scan(&existingUserID)
	if err == nil {
		if existingUserID == userID {
			return nil, status.Error(codes.AlreadyExists, "wallet already linked to your account")
		}
		return nil, status.Error(codes.AlreadyExists, "wallet already linked to another account")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, status.Errorf(codes.Internal, "failed to check wallet: %v", err)
	}

	// Create wallet identity
	var walletID string
	var createdAt time.Time
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO commodore.wallet_identities (tenant_id, user_id, chain_type, wallet_address)
		VALUES ($1, $2, 'ethereum', $3)
		RETURNING id, created_at
	`, tenantID, userID, normalizedAddr).Scan(&walletID, &createdAt)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to link wallet: %v", err)
	}

	s.emitAuthEvent(ctx, eventWalletLinked, userID, tenantID, "wallet", walletID, "", "")

	return &pb.WalletIdentity{
		Id:            walletID,
		WalletAddress: normalizedAddr,
		CreatedAt:     timestamppb.New(createdAt),
	}, nil
}

// UnlinkWallet removes a wallet from the user's account
func (s *CommodoreServer) UnlinkWallet(ctx context.Context, req *pb.UnlinkWalletRequest) (*pb.UnlinkWalletResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	walletID := req.GetWalletId()
	if walletID == "" {
		return nil, status.Error(codes.InvalidArgument, "wallet_id required")
	}

	// Delete only if it belongs to the user
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM commodore.wallet_identities
		WHERE id = $1 AND user_id = $2
	`, walletID, userID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to unlink wallet: %v", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return nil, status.Error(codes.NotFound, "wallet not found or not owned by you")
	}

	s.emitAuthEvent(ctx, eventWalletUnlinked, userID, tenantID, "wallet", walletID, "", "")

	return &pb.UnlinkWalletResponse{
		Success: true,
		Message: "wallet unlinked",
	}, nil
}

// ListWallets returns all wallets linked to the authenticated user
func (s *CommodoreServer) ListWallets(ctx context.Context, req *pb.ListWalletsRequest) (*pb.ListWalletsResponse, error) {
	userID, _, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, wallet_address, created_at, last_auth_at
		FROM commodore.wallet_identities
		WHERE user_id = $1
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list wallets: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var wallets []*pb.WalletIdentity
	for rows.Next() {
		var id, addr string
		var createdAt time.Time
		var lastAuthAt sql.NullTime
		if err := rows.Scan(&id, &addr, &createdAt, &lastAuthAt); err != nil {
			continue
		}
		w := &pb.WalletIdentity{
			Id:            id,
			WalletAddress: addr,
			CreatedAt:     timestamppb.New(createdAt),
		}
		if lastAuthAt.Valid {
			w.LastAuthAt = timestamppb.New(lastAuthAt.Time)
		}
		wallets = append(wallets, w)
	}

	return &pb.ListWalletsResponse{Wallets: wallets}, nil
}

// LinkEmail adds an email to a wallet-only account (for postpaid upgrade path)
func (s *CommodoreServer) LinkEmail(ctx context.Context, req *pb.LinkEmailRequest) (*pb.LinkEmailResponse, error) {
	userID, _, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	email := strings.TrimSpace(strings.ToLower(req.GetEmail()))
	password := req.GetPassword()

	if email == "" {
		return nil, status.Error(codes.InvalidArgument, "email required")
	}
	if password == "" || len(password) < 8 {
		return nil, status.Error(codes.InvalidArgument, "password must be at least 8 characters")
	}

	// Check if user already has an email
	var existingEmail sql.NullString
	err = s.db.QueryRowContext(ctx, `
		SELECT email FROM commodore.users WHERE id = $1
	`, userID).Scan(&existingEmail)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to check user: %v", err)
	}
	if existingEmail.Valid && existingEmail.String != "" {
		return nil, status.Error(codes.AlreadyExists, "email already linked to your account")
	}

	// Check if email is already used by another account
	var otherUserID string
	err = s.db.QueryRowContext(ctx, `
		SELECT id FROM commodore.users WHERE LOWER(email) = $1 AND id != $2
	`, email, userID).Scan(&otherUserID)
	if err == nil {
		return nil, status.Error(codes.AlreadyExists, "email already in use by another account")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, status.Errorf(codes.Internal, "failed to check email: %v", err)
	}

	// Hash password
	passwordHash, err := auth.HashPassword(password)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to hash password: %v", err)
	}

	// Generate verification token
	tokenBytes := make([]byte, 32)
	if _, randErr := rand.Read(tokenBytes); randErr != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate token: %v", randErr)
	}
	verificationToken := hex.EncodeToString(tokenBytes)
	tokenExpiry := time.Now().Add(24 * time.Hour)

	// Update user with email, password, and verification token
	_, err = s.db.ExecContext(ctx, `
		UPDATE commodore.users
		SET email = $1, password_hash = $2, verification_token = $3, token_expires_at = $4, updated_at = NOW()
		WHERE id = $5
	`, email, passwordHash, verificationToken, tokenExpiry, userID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to link email: %v", err)
	}

	// Send verification email
	if err := s.sendVerificationEmail(email, verificationToken); err != nil {
		s.logger.WithError(err).Warn("Failed to send verification email")
		return &pb.LinkEmailResponse{
			Success:          true,
			Message:          "Email linked. Verification email could not be sent - please use resend verification.",
			VerificationSent: false,
		}, nil
	}

	return &pb.LinkEmailResponse{
		Success:          true,
		Message:          fmt.Sprintf("Verification email sent to %s", email),
		VerificationSent: true,
	}, nil
}

// ============================================================================
// STREAM SERVICE (Gateway → Commodore for stream CRUD)
// ============================================================================

// CreateStream creates a new stream for the authenticated user
func (s *CommodoreServer) CreateStream(ctx context.Context, req *pb.CreateStreamRequest) (*pb.CreateStreamResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Check if tenant is suspended (prepaid balance < -$10)
	if suspended, suspendErr := s.isTenantSuspended(ctx, tenantID); suspendErr != nil {
		s.logger.WithError(suspendErr).Warn("Failed to check tenant suspension status")
		// Continue anyway - don't block on suspension check failure
	} else if suspended {
		return nil, status.Error(codes.PermissionDenied, "account suspended - please top up your balance to create new streams")
	}

	title := req.GetTitle()
	if title == "" {
		title = "Untitled Stream"
	}

	// Use stored procedure to create stream
	var streamID, streamKey, playbackID, internalName string
	err = s.db.QueryRowContext(ctx, `
		SELECT stream_id, stream_key, playback_id, internal_name
		FROM commodore.create_user_stream($1, $2, $3)
	`, tenantID, userID, title).Scan(&streamID, &streamKey, &playbackID, &internalName)

	if err != nil {
		s.logger.WithError(err).Error("Failed to create stream")
		return nil, status.Errorf(codes.Internal, "failed to create stream: %v", err)
	}

	// Update description if provided
	if req.GetDescription() != "" {
		_, err = s.db.ExecContext(ctx, `
			UPDATE commodore.streams SET description = $1 WHERE id = $2
		`, req.GetDescription(), streamID)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to update stream description")
		}
	}

	// Update recording setting if requested
	if req.GetIsRecording() {
		_, err = s.db.ExecContext(ctx, `
			UPDATE commodore.streams SET is_recording_enabled = true WHERE id = $1
		`, streamID)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to enable recording")
		}
	}

	changedFields := []string{"title"}
	if req.GetDescription() != "" {
		changedFields = append(changedFields, "description")
	}
	if req.GetIsRecording() {
		changedFields = append(changedFields, "is_recording_enabled")
	}
	s.emitStreamChangeEvent(ctx, eventStreamCreated, tenantID, userID, streamID, changedFields)

	resp := &pb.CreateStreamResponse{
		Id:          streamID,
		StreamKey:   streamKey,
		PlaybackId:  playbackID,
		Title:       title,
		Description: req.GetDescription(),
		Status:      "offline",
	}

	// Populate cluster-level base domains from Quartermaster routing data.
	if route, err := s.resolveClusterRouteForTenant(ctx, tenantID); err == nil && route.clusterSlug != "" && route.baseURL != "" {
		ingest := fmt.Sprintf("edge-ingest.%s.%s", route.clusterSlug, route.baseURL)
		edge := fmt.Sprintf("edge-egress.%s.%s", route.clusterSlug, route.baseURL)
		play := fmt.Sprintf("foghorn.%s.%s", route.clusterSlug, route.baseURL)
		resp.IngestDomain = &ingest
		resp.EdgeDomain = &edge
		resp.PlayDomain = &play

		if route.clusterName != "" {
			resp.PreferredClusterLabel = &route.clusterName
		}

		// Official cluster domains (geographic coverage from billing tier)
		if route.officialClusterSlug != "" && route.officialBaseURL != "" {
			offIngest := fmt.Sprintf("edge-ingest.%s.%s", route.officialClusterSlug, route.officialBaseURL)
			offEdge := fmt.Sprintf("edge-egress.%s.%s", route.officialClusterSlug, route.officialBaseURL)
			offPlay := fmt.Sprintf("foghorn.%s.%s", route.officialClusterSlug, route.officialBaseURL)
			resp.OfficialIngestDomain = &offIngest
			resp.OfficialEdgeDomain = &offEdge
			resp.OfficialPlayDomain = &offPlay
			if route.officialClusterName != "" {
				resp.OfficialClusterLabel = &route.officialClusterName
			}
		}
	}

	return resp, nil
}

// GetStream retrieves a specific stream
func (s *CommodoreServer) GetStream(ctx context.Context, req *pb.GetStreamRequest) (*pb.Stream, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	return s.queryStream(ctx, streamID, userID, tenantID)
}

// ListStreams returns all streams for the authenticated user with keyset pagination
func (s *CommodoreServer) ListStreams(ctx context.Context, req *pb.ListStreamsRequest) (*pb.ListStreamsResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// Get total count
	var total int32
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM commodore.streams WHERE user_id = $1 AND tenant_id = $2
	`, userID, tenantID).Scan(&total)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Build keyset pagination query
	builder := &pagination.KeysetBuilder{
		TimestampColumn: "created_at",
		IDColumn:        "id",
	}

	// Base query
	query := `
		SELECT id, internal_name, stream_key, playback_id, title, description,
		       is_recording_enabled, created_at, updated_at
		FROM commodore.streams
		WHERE user_id = $1 AND tenant_id = $2`
	args := []interface{}{userID, tenantID}
	argIdx := 3

	// Add keyset condition if cursor provided
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		query += " AND " + condition
		args = append(args, cursorArgs...)
	}

	// Add ORDER BY and LIMIT (fetch limit+1 to detect hasMore)
	query += " " + builder.OrderBy(params)
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var streams []*pb.Stream
	for rows.Next() {
		stream, err := scanStream(rows)
		if err != nil {
			s.logger.WithError(err).Warn("Error scanning stream")
			continue
		}
		streams = append(streams, stream)
	}

	// Detect hasMore and trim results
	hasMore := len(streams) > params.Limit
	if hasMore {
		streams = streams[:params.Limit]
	}

	// Reverse results if backward pagination
	if params.Direction == pagination.Backward && len(streams) > 0 {
		for i, j := 0, len(streams)-1; i < j; i, j = i+1, j-1 {
			streams[i], streams[j] = streams[j], streams[i]
		}
	}

	// Build cursors from results
	var startCursor, endCursor string
	if len(streams) > 0 {
		first := streams[0]
		last := streams[len(streams)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.StreamId)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.StreamId)
	}

	// Build response with proper hasNextPage/hasPreviousPage
	resp := &pb.ListStreamsResponse{
		Streams: streams,
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

// UpdateStream updates a stream's properties
func (s *CommodoreServer) UpdateStream(ctx context.Context, req *pb.UpdateStreamRequest) (*pb.Stream, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	// Verify ownership and fetch internal_name for internal ops
	var internalName string
	err = s.db.QueryRowContext(ctx, `
		SELECT internal_name FROM commodore.streams WHERE id = $1 AND user_id = $2 AND tenant_id = $3
	`, streamID, userID, tenantID).Scan(&internalName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "stream not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Build update query dynamically
	var updates []string
	var args []interface{}
	argIdx := 1
	changedFields := []string{}

	if req.Name != nil {
		updates = append(updates, fmt.Sprintf("title = $%d", argIdx))
		args = append(args, *req.Name)
		argIdx++
		changedFields = append(changedFields, "title")
	}
	if req.Description != nil {
		updates = append(updates, fmt.Sprintf("description = $%d", argIdx))
		args = append(args, *req.Description)
		argIdx++
		changedFields = append(changedFields, "description")
	}
	if req.Record != nil {
		updates = append(updates, fmt.Sprintf("is_recording_enabled = $%d", argIdx))
		args = append(args, *req.Record)
		argIdx++
		changedFields = append(changedFields, "is_recording_enabled")
	}

	if len(updates) > 0 {
		updates = append(updates, "updated_at = NOW()")
		query := fmt.Sprintf("UPDATE commodore.streams SET %s WHERE id = $%d",
			strings.Join(updates, ", "), argIdx)
		args = append(args, streamID)

		_, err = s.db.ExecContext(ctx, query, args...)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to update stream: %v", err)
		}
	}

	if len(changedFields) > 0 {
		s.emitStreamChangeEvent(ctx, eventStreamUpdated, tenantID, userID, streamID, changedFields)
	}

	return s.queryStream(ctx, streamID, userID, tenantID)
}

// DeleteStream deletes a stream
func (s *CommodoreServer) DeleteStream(ctx context.Context, req *pb.DeleteStreamRequest) (*pb.DeleteStreamResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	// Get stream details before deletion
	var internalName, title string
	err = s.db.QueryRowContext(ctx, `
		SELECT internal_name, title FROM commodore.streams
		WHERE id = $1 AND user_id = $2 AND tenant_id = $3
	`, streamID, userID, tenantID).Scan(&internalName, &title)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "stream not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Delete related clips (best-effort, don't fail stream deletion)
	if clipFoghorn, _, resolveErr := s.resolveFoghornForTenant(ctx, tenantID); resolveErr == nil {
		rows, queryErr := s.db.QueryContext(ctx, `
				SELECT clip_hash FROM commodore.clips
				WHERE stream_id = $1 AND tenant_id = $2
			`, streamID, tenantID)
		if queryErr != nil {
			s.logger.WithError(queryErr).Warn("Failed to list clips for stream deletion cleanup")
		} else {
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				var clipHash string
				if scanErr := rows.Scan(&clipHash); scanErr != nil {
					continue
				}
				if _, _, delErr := clipFoghorn.DeleteClip(ctx, clipHash, &tenantID); delErr != nil {
					s.logger.WithError(delErr).WithField("clip_hash", clipHash).Warn("Failed to delete clip during stream cleanup")
				}
			}
		}
	}

	// Begin transaction
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to begin transaction: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	// Delete related stream_keys (use UUID, not internal_name)
	_, err = tx.ExecContext(ctx, `DELETE FROM commodore.stream_keys WHERE stream_id = $1`, streamID)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to delete stream keys")
	}

	// Delete the stream
	_, err = tx.ExecContext(ctx, `DELETE FROM commodore.streams WHERE id = $1`, streamID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete stream: %v", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to commit: %v", err)
	}

	s.emitStreamChangeEvent(ctx, eventStreamDeleted, tenantID, userID, streamID, nil)

	return &pb.DeleteStreamResponse{
		Message:     "Stream deleted successfully",
		StreamId:    streamID,
		StreamTitle: title,
		DeletedAt:   timestamppb.Now(),
	}, nil
}

// RefreshStreamKey generates a new stream key
func (s *CommodoreServer) RefreshStreamKey(ctx context.Context, req *pb.RefreshStreamKeyRequest) (*pb.RefreshStreamKeyResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	// Generate new stream key
	newStreamKey, err := generateStreamKey()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate stream key: %v", err)
	}

	// Update the stream
	result, err := s.db.ExecContext(ctx, `
		UPDATE commodore.streams
		SET stream_key = $1, updated_at = NOW()
		WHERE id = $2 AND user_id = $3 AND tenant_id = $4
	`, newStreamKey, streamID, userID, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to refresh stream key: %v", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, status.Error(codes.NotFound, "stream not found")
	}

	// Get playback ID
	var playbackID string
	if err := s.db.QueryRowContext(ctx, `SELECT playback_id FROM commodore.streams WHERE id = $1`, streamID).Scan(&playbackID); err != nil {
		s.logger.WithError(err).Warn("Failed to get playback ID for refreshed stream key")
	}

	s.emitStreamChangeEvent(ctx, eventStreamUpdated, tenantID, userID, streamID, []string{"stream_key"})

	return &pb.RefreshStreamKeyResponse{
		Message:           "Stream key refreshed successfully",
		StreamId:          streamID,
		StreamKey:         newStreamKey,
		PlaybackId:        playbackID,
		OldKeyInvalidated: true,
	}, nil
}

// ============================================================================
// STREAM KEY SERVICE (Gateway → Commodore for multi-key management)
// ============================================================================

// CreateStreamKey creates a new stream key for a stream
func (s *CommodoreServer) CreateStreamKey(ctx context.Context, req *pb.CreateStreamKeyRequest) (*pb.StreamKeyResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	// Verify stream ownership
	var exists bool
	err = s.db.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM commodore.streams WHERE id = $1 AND user_id = $2 AND tenant_id = $3)
	`, streamID, userID, tenantID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "stream not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !exists {
		return nil, status.Error(codes.NotFound, "stream not found")
	}

	// Generate new key
	keyID := uuid.New().String()
	keyValue, err := generateStreamKey()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate stream key: %v", err)
	}
	keyName := req.GetKeyName()
	if keyName == "" {
		keyName = "Key " + time.Now().Format("2006-01-02 15:04")
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO commodore.stream_keys (id, tenant_id, user_id, stream_id, key_value, key_name, is_active)
		VALUES ($1, $2, $3, $4, $5, $6, true)
	`, keyID, tenantID, userID, streamID, keyValue, keyName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create stream key: %v", err)
	}

	s.emitStreamKeyEvent(ctx, eventStreamKeyCreated, tenantID, userID, streamID, keyID)

	return &pb.StreamKeyResponse{
		StreamKey: &pb.StreamKey{
			Id:        keyID,
			TenantId:  tenantID,
			UserId:    userID,
			StreamId:  streamID,
			KeyValue:  keyValue,
			KeyName:   keyName,
			IsActive:  true,
			CreatedAt: timestamppb.Now(),
			UpdatedAt: timestamppb.Now(),
		},
		Message: "Stream key created successfully",
	}, nil
}

// ListStreamKeys lists all keys for a stream
func (s *CommodoreServer) ListStreamKeys(ctx context.Context, req *pb.ListStreamKeysRequest) (*pb.ListStreamKeysResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	// Verify stream ownership
	var exists bool
	err = s.db.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM commodore.streams WHERE id = $1 AND user_id = $2 AND tenant_id = $3)
	`, streamID, userID, tenantID).Scan(&exists)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !exists {
		return nil, status.Error(codes.NotFound, "stream not found")
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

	// Get total count
	var total int32
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM commodore.stream_keys WHERE stream_id = $1
	`, streamID).Scan(&total)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Build query with keyset pagination
	query := `
		SELECT id, tenant_id, user_id, stream_id, key_value, key_name, is_active, last_used_at, created_at, updated_at
		FROM commodore.stream_keys
		WHERE stream_id = $1`
	args := []interface{}{streamID}
	argIdx := 2

	// Add keyset condition if cursor provided
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		query += " AND " + condition
		args = append(args, cursorArgs...)
	}

	// Add ORDER BY and LIMIT
	query += " " + builder.OrderBy(params)
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var keys []*pb.StreamKey
	for rows.Next() {
		var key pb.StreamKey
		var lastUsedAt sql.NullTime
		var createdAt, updatedAt time.Time

		err := rows.Scan(&key.Id, &key.TenantId, &key.UserId, &key.StreamId, &key.KeyValue, &key.KeyName,
			&key.IsActive, &lastUsedAt, &createdAt, &updatedAt)
		if err != nil {
			continue
		}

		key.CreatedAt = timestamppb.New(createdAt)
		key.UpdatedAt = timestamppb.New(updatedAt)
		if lastUsedAt.Valid {
			key.LastUsedAt = timestamppb.New(lastUsedAt.Time)
		}
		keys = append(keys, &key)
	}

	// Detect hasMore and trim results
	hasMore := len(keys) > params.Limit
	if hasMore {
		keys = keys[:params.Limit]
	}

	// Reverse results if backward pagination
	if params.Direction == pagination.Backward && len(keys) > 0 {
		for i, j := 0, len(keys)-1; i < j; i, j = i+1, j-1 {
			keys[i], keys[j] = keys[j], keys[i]
		}
	}

	// Build cursors from results
	var startCursor, endCursor string
	if len(keys) > 0 {
		first := keys[0]
		last := keys[len(keys)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.Id)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.Id)
	}

	// Build response with proper hasNextPage/hasPreviousPage
	resp := &pb.ListStreamKeysResponse{
		StreamKeys: keys,
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

// DeactivateStreamKey deactivates a stream key
func (s *CommodoreServer) DeactivateStreamKey(ctx context.Context, req *pb.DeactivateStreamKeyRequest) (*emptypb.Empty, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Verify stream ownership
	var exists bool
	err = s.db.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM commodore.streams WHERE id = $1 AND user_id = $2 AND tenant_id = $3)
	`, req.GetStreamId(), userID, tenantID).Scan(&exists)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !exists {
		return nil, status.Error(codes.NotFound, "stream not found")
	}

	result, err := s.db.ExecContext(ctx, `
		UPDATE commodore.stream_keys SET is_active = false, updated_at = NOW()
		WHERE id = $1 AND stream_id = $2
	`, req.GetKeyId(), req.GetStreamId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to deactivate key: %v", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, status.Error(codes.NotFound, "stream key not found")
	}

	s.emitStreamKeyEvent(ctx, eventStreamKeyDeleted, tenantID, userID, req.GetStreamId(), req.GetKeyId())

	return &emptypb.Empty{}, nil
}

// ============================================================================
// PUSH TARGET SERVICE (Gateway → Commodore for multistream management)
// ============================================================================

// validPushSchemes are the allowed URI schemes for push targets.
var validPushSchemes = map[string]bool{"rtmp": true, "rtmps": true, "srt": true}

// maskTargetURI masks the stream key portion of a push target URI for API responses.
// Example: rtmp://live.twitch.tv/app/live_abc123def → rtmp://live.twitch.tv/app/live_****def
func maskTargetURI(uri string) string {
	parsed, err := url.Parse(uri)
	if err != nil {
		return "****"
	}

	// Never expose credentials, query params, or fragments
	// (SRT streamid/passphrase often live in query/fragment parts).
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""

	path := parsed.Path
	if len(path) > 1 {
		parts := strings.Split(path, "/")
		if last := parts[len(parts)-1]; len(last) > 6 {
			parts[len(parts)-1] = last[:4] + "xxxx" + last[len(last)-3:]
		} else if len(last) > 0 {
			parts[len(parts)-1] = "xxxx"
		}
		parsed.Path = strings.Join(parts, "/")
	}
	return parsed.String()
}

// validatePushTargetURI checks that the URI is a valid push target.
func validatePushTargetURI(uri string) error {
	parsed, err := url.Parse(uri)
	if err != nil {
		return fmt.Errorf("invalid URI: %w", err)
	}
	if !validPushSchemes[parsed.Scheme] {
		return fmt.Errorf("unsupported scheme %q: must be rtmp, rtmps, or srt", parsed.Scheme)
	}
	if parsed.Host == "" {
		return fmt.Errorf("URI must include a host")
	}
	return nil
}

func (s *CommodoreServer) CreatePushTarget(ctx context.Context, req *pb.CreatePushTargetRequest) (*pb.PushTarget, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name required")
	}
	if req.GetTargetUri() == "" {
		return nil, status.Error(codes.InvalidArgument, "target_uri required")
	}
	if validationErr := validatePushTargetURI(req.GetTargetUri()); validationErr != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid target_uri: %v", validationErr)
	}

	// Verify stream ownership
	var exists bool
	err = s.db.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM commodore.streams WHERE id = $1 AND user_id = $2 AND tenant_id = $3)
	`, streamID, userID, tenantID).Scan(&exists)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !exists {
		return nil, status.Error(codes.NotFound, "stream not found")
	}

	id := uuid.New().String()
	platform := req.GetPlatform()
	if platform == "" {
		platform = "custom"
	}
	now := time.Now()

	encryptedURI, err := s.fieldEncryptor.Encrypt(req.GetTargetUri())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to encrypt target_uri: %v", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO commodore.push_targets (id, tenant_id, stream_id, platform, name, target_uri, is_enabled, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, true, 'idle', $7, $7)
	`, id, tenantID, streamID, platform, req.GetName(), encryptedURI, now)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create push target: %v", err)
	}

	return &pb.PushTarget{
		Id:        id,
		StreamId:  streamID,
		Platform:  platform,
		Name:      req.GetName(),
		TargetUri: maskTargetURI(req.GetTargetUri()),
		IsEnabled: true,
		Status:    "idle",
		CreatedAt: timestamppb.New(now),
		UpdatedAt: timestamppb.New(now),
	}, nil
}

func (s *CommodoreServer) ListPushTargets(ctx context.Context, req *pb.ListPushTargetsRequest) (*pb.ListPushTargetsResponse, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, stream_id, platform, name, target_uri, is_enabled, status, last_error, last_pushed_at, created_at, updated_at
		FROM commodore.push_targets
		WHERE stream_id = $1 AND tenant_id = $2
		ORDER BY created_at ASC
	`, streamID, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var targets []*pb.PushTarget
	for rows.Next() {
		var (
			t            pb.PushTarget
			targetURI    string
			lastError    sql.NullString
			lastPushedAt sql.NullTime
			createdAt    time.Time
			updatedAt    time.Time
		)
		if err := rows.Scan(&t.Id, &t.StreamId, &t.Platform, &t.Name, &targetURI, &t.IsEnabled, &t.Status, &lastError, &lastPushedAt, &createdAt, &updatedAt); err != nil {
			return nil, status.Errorf(codes.Internal, "scan error: %v", err)
		}
		decrypted, err := s.fieldEncryptor.Decrypt(targetURI)
		if err != nil {
			s.logger.WithError(err).WithField("push_target_id", t.Id).Warn("Failed to decrypt target_uri")
			decrypted = targetURI
		}
		t.TargetUri = maskTargetURI(decrypted)
		if lastError.Valid {
			t.LastError = lastError.String
		}
		if lastPushedAt.Valid {
			t.LastPushedAt = timestamppb.New(lastPushedAt.Time)
		}
		t.CreatedAt = timestamppb.New(createdAt)
		t.UpdatedAt = timestamppb.New(updatedAt)
		targets = append(targets, &t)
	}

	return &pb.ListPushTargetsResponse{PushTargets: targets}, nil
}

func (s *CommodoreServer) UpdatePushTarget(ctx context.Context, req *pb.UpdatePushTargetRequest) (*pb.PushTarget, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	id := req.GetId()
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "id required")
	}

	// Build dynamic UPDATE
	setClauses := []string{"updated_at = NOW()"}
	args := []interface{}{id, tenantID}
	argIdx := 3

	if req.Name != nil {
		setClauses = append(setClauses, fmt.Sprintf("name = $%d", argIdx))
		args = append(args, req.GetName())
		argIdx++
	}
	if req.TargetUri != nil {
		if validationErr := validatePushTargetURI(req.GetTargetUri()); validationErr != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid target_uri: %v", validationErr)
		}
		encURI, encErr := s.fieldEncryptor.Encrypt(req.GetTargetUri())
		if encErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to encrypt target_uri: %v", encErr)
		}
		setClauses = append(setClauses, fmt.Sprintf("target_uri = $%d", argIdx))
		args = append(args, encURI)
		argIdx++
	}
	if req.IsEnabled != nil {
		setClauses = append(setClauses, fmt.Sprintf("is_enabled = $%d", argIdx))
		args = append(args, req.GetIsEnabled())
	}

	var (
		t            pb.PushTarget
		targetURI    string
		lastError    sql.NullString
		lastPushedAt sql.NullTime
		createdAt    time.Time
		updatedAt    time.Time
	)

	query := fmt.Sprintf(`
		UPDATE commodore.push_targets SET %s
		WHERE id = $1 AND tenant_id = $2
		RETURNING id, stream_id, platform, name, target_uri, is_enabled, status, last_error, last_pushed_at, created_at, updated_at
	`, strings.Join(setClauses, ", "))

	err = s.db.QueryRowContext(ctx, query, args...).Scan(
		&t.Id, &t.StreamId, &t.Platform, &t.Name, &targetURI, &t.IsEnabled, &t.Status, &lastError, &lastPushedAt, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "push target not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	decryptedURI, decErr := s.fieldEncryptor.Decrypt(targetURI)
	if decErr != nil {
		s.logger.WithError(decErr).WithField("push_target_id", t.Id).Warn("Failed to decrypt target_uri")
		decryptedURI = targetURI
	}
	t.TargetUri = maskTargetURI(decryptedURI)
	if lastError.Valid {
		t.LastError = lastError.String
	}
	if lastPushedAt.Valid {
		t.LastPushedAt = timestamppb.New(lastPushedAt.Time)
	}
	t.CreatedAt = timestamppb.New(createdAt)
	t.UpdatedAt = timestamppb.New(updatedAt)

	return &t, nil
}

func (s *CommodoreServer) DeletePushTarget(ctx context.Context, req *pb.DeletePushTargetRequest) (*pb.DeletePushTargetResponse, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	id := req.GetId()
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "id required")
	}

	result, err := s.db.ExecContext(ctx, `
		DELETE FROM commodore.push_targets WHERE id = $1 AND tenant_id = $2
	`, id, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, status.Error(codes.NotFound, "push target not found")
	}

	return &pb.DeletePushTargetResponse{
		Message:   "Push target deleted",
		Id:        id,
		DeletedAt: timestamppb.Now(),
	}, nil
}

// GetStreamPushTargets is an internal RPC called by Foghorn when a stream goes live.
// Returns unmasked target URIs for Helmsman to push to.
func (s *CommodoreServer) GetStreamPushTargets(ctx context.Context, req *pb.GetStreamPushTargetsRequest) (*pb.GetStreamPushTargetsResponse, error) {
	streamID := req.GetStreamId()
	tenantID := req.GetTenantId()
	if streamID == "" || tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id and tenant_id required")
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, platform, name, target_uri
		FROM commodore.push_targets
		WHERE stream_id = $1 AND tenant_id = $2 AND is_enabled = true
	`, streamID, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var targets []*pb.PushTargetInternal
	for rows.Next() {
		var t pb.PushTargetInternal
		if err := rows.Scan(&t.Id, &t.Platform, &t.Name, &t.TargetUri); err != nil {
			return nil, status.Errorf(codes.Internal, "scan error: %v", err)
		}
		decrypted, decErr := s.fieldEncryptor.Decrypt(t.TargetUri)
		if decErr != nil {
			s.logger.WithError(decErr).WithField("push_target_id", t.Id).Warn("Failed to decrypt target_uri")
		} else {
			t.TargetUri = decrypted
		}
		targets = append(targets, &t)
	}

	return &pb.GetStreamPushTargetsResponse{PushTargets: targets}, nil
}

// UpdatePushTargetStatus is an internal RPC called by Foghorn to update push target status
// based on PUSH_OUT_START / PUSH_END trigger events.
func (s *CommodoreServer) UpdatePushTargetStatus(ctx context.Context, req *pb.UpdatePushTargetStatusRequest) (*pb.PushTarget, error) {
	id := req.GetId()
	tenantID := req.GetTenantId()
	if id == "" || tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "id and tenant_id required")
	}

	setClauses := []string{"status = $3", "updated_at = NOW()"}
	args := []interface{}{id, tenantID, req.GetStatus()}
	argIdx := 4

	if req.LastError != nil {
		setClauses = append(setClauses, fmt.Sprintf("last_error = $%d", argIdx))
		args = append(args, req.GetLastError())
	} else if req.GetStatus() != "failed" {
		setClauses = append(setClauses, "last_error = NULL")
	}

	if req.GetStatus() == "pushing" {
		setClauses = append(setClauses, "last_pushed_at = NOW()")
	}

	var (
		t            pb.PushTarget
		targetURI    string
		lastError    sql.NullString
		lastPushedAt sql.NullTime
		createdAt    time.Time
		updatedAt    time.Time
	)

	query := fmt.Sprintf(`
		UPDATE commodore.push_targets SET %s
		WHERE id = $1 AND tenant_id = $2
		RETURNING id, stream_id, platform, name, target_uri, is_enabled, status, last_error, last_pushed_at, created_at, updated_at
	`, strings.Join(setClauses, ", "))

	err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&t.Id, &t.StreamId, &t.Platform, &t.Name, &targetURI, &t.IsEnabled, &t.Status, &lastError, &lastPushedAt, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "push target not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	decryptedURI, decErr := s.fieldEncryptor.Decrypt(targetURI)
	if decErr != nil {
		s.logger.WithError(decErr).WithField("push_target_id", t.Id).Warn("Failed to decrypt target_uri")
		decryptedURI = targetURI
	}
	t.TargetUri = maskTargetURI(decryptedURI)
	if lastError.Valid {
		t.LastError = lastError.String
	}
	if lastPushedAt.Valid {
		t.LastPushedAt = timestamppb.New(lastPushedAt.Time)
	}
	t.CreatedAt = timestamppb.New(createdAt)
	t.UpdatedAt = timestamppb.New(updatedAt)

	return &t, nil
}

// ============================================================================
// DEVELOPER SERVICE (Gateway → Commodore for API token management)
// ============================================================================

// CreateAPIToken creates a new API token
func (s *CommodoreServer) CreateAPIToken(ctx context.Context, req *pb.CreateAPITokenRequest) (*pb.CreateAPITokenResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	tokenID := uuid.New().String()
	tokenSuffix, err := generateSecureToken(32)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate API token: %v", err)
	}
	tokenValue := "fw_" + tokenSuffix
	tokenHash := hashToken(tokenValue)
	tokenName := req.GetTokenName()
	if tokenName == "" {
		tokenName = "API Token " + time.Now().Format("2006-01-02")
	}

	permissions := req.GetPermissions()
	if len(permissions) == 0 {
		permissions = []string{"read"}
	}

	var expiresAt sql.NullTime
	if req.GetExpiresAt() != nil {
		expiresAt = sql.NullTime{Time: req.GetExpiresAt().AsTime(), Valid: true}
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO commodore.api_tokens (id, tenant_id, user_id, token_value, token_name, permissions, is_active, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, true, $7)
	`, tokenID, tenantID, userID, tokenHash, tokenName, pq.Array(permissions), expiresAt)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create API token: %v", err)
	}

	s.emitAuthEvent(ctx, eventTokenCreated, userID, tenantID, "api_token", "", tokenID, "")

	resp := &pb.CreateAPITokenResponse{
		Id:          tokenID,
		TokenValue:  tokenValue,
		TokenName:   tokenName,
		Permissions: permissions,
		CreatedAt:   timestamppb.Now(),
		Message:     "API token created successfully",
	}
	if expiresAt.Valid {
		resp.ExpiresAt = timestamppb.New(expiresAt.Time)
	}

	return resp, nil
}

// ListAPITokens lists all API tokens for the user
func (s *CommodoreServer) ListAPITokens(ctx context.Context, req *pb.ListAPITokensRequest) (*pb.ListAPITokensResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
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

	// Get total count
	var total int32
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM commodore.api_tokens WHERE user_id = $1 AND tenant_id = $2
	`, userID, tenantID).Scan(&total)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Build query with keyset pagination
	query := `
		SELECT id, token_name, permissions,
		       CASE WHEN is_active AND (expires_at IS NULL OR expires_at > NOW()) THEN 'active' ELSE 'inactive' END as status,
		       last_used_at, expires_at, created_at
		FROM commodore.api_tokens
		WHERE user_id = $1 AND tenant_id = $2`
	args := []interface{}{userID, tenantID}
	argIdx := 3

	// Add keyset condition if cursor provided
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		query += " AND " + condition
		args = append(args, cursorArgs...)
	}

	// Add ORDER BY and LIMIT
	query += " " + builder.OrderBy(params)
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var tokens []*pb.APITokenInfo
	for rows.Next() {
		var token pb.APITokenInfo
		var permissions []string
		var lastUsedAt, expiresAt sql.NullTime
		var createdAt time.Time

		err := rows.Scan(&token.Id, &token.TokenName, pq.Array(&permissions), &token.Status,
			&lastUsedAt, &expiresAt, &createdAt)
		if err != nil {
			continue
		}

		token.Permissions = permissions
		token.CreatedAt = timestamppb.New(createdAt)
		if lastUsedAt.Valid {
			token.LastUsedAt = timestamppb.New(lastUsedAt.Time)
		}
		if expiresAt.Valid {
			token.ExpiresAt = timestamppb.New(expiresAt.Time)
		}
		tokens = append(tokens, &token)
	}

	// Detect hasMore and trim results
	hasMore := len(tokens) > params.Limit
	if hasMore {
		tokens = tokens[:params.Limit]
	}

	// Reverse results if backward pagination
	if params.Direction == pagination.Backward && len(tokens) > 0 {
		for i, j := 0, len(tokens)-1; i < j; i, j = i+1, j-1 {
			tokens[i], tokens[j] = tokens[j], tokens[i]
		}
	}

	// Build cursors from results
	var startCursor, endCursor string
	if len(tokens) > 0 {
		first := tokens[0]
		last := tokens[len(tokens)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.Id)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.Id)
	}

	// Build response with proper hasNextPage/hasPreviousPage
	resp := &pb.ListAPITokensResponse{
		Tokens: tokens,
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

// RevokeAPIToken revokes an API token
func (s *CommodoreServer) RevokeAPIToken(ctx context.Context, req *pb.RevokeAPITokenRequest) (*pb.RevokeAPITokenResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Get token info before revoking
	var tokenName string
	err = s.db.QueryRowContext(ctx, `
		SELECT token_name FROM commodore.api_tokens WHERE id = $1 AND user_id = $2 AND tenant_id = $3
	`, req.GetTokenId(), userID, tenantID).Scan(&tokenName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "token not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Revoke the token
	_, err = s.db.ExecContext(ctx, `
		UPDATE commodore.api_tokens SET is_active = false, updated_at = NOW() WHERE id = $1
	`, req.GetTokenId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to revoke token: %v", err)
	}

	s.emitAuthEvent(ctx, eventTokenRevoked, userID, tenantID, "api_token", "", req.GetTokenId(), "")

	return &pb.RevokeAPITokenResponse{
		Message:   "Token revoked successfully",
		TokenId:   req.GetTokenId(),
		TokenName: tokenName,
		RevokedAt: timestamppb.Now(),
	}, nil
}

// ============================================================================
// HELPER FUNCTIONS
// ============================================================================

const (
	eventAuthLoginSucceeded = "auth_login_succeeded"
	eventAuthLoginFailed    = "auth_login_failed"
	eventAuthRegistered     = "auth_registered"
	eventAuthTokenRefreshed = "auth_token_refreshed"
	eventTokenCreated       = "token_created"
	eventTokenRevoked       = "token_revoked"
	eventWalletLinked       = "wallet_linked"
	eventWalletUnlinked     = "wallet_unlinked"
	eventStreamCreated      = "stream_created"
	eventStreamUpdated      = "stream_updated"
	eventStreamDeleted      = "stream_deleted"
	eventStreamKeyCreated   = "stream_key_created"
	eventStreamKeyDeleted   = "stream_key_deleted"
	eventArtifactRegistered = "artifact_registered"
	eventArtifactDeleted    = "artifact_deleted"
)

func (s *CommodoreServer) emitServiceEvent(ctx context.Context, event *pb.ServiceEvent) {
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

func (s *CommodoreServer) emitAuthEvent(ctx context.Context, eventType, userID, tenantID, authType, walletID, tokenID, errMsg string) {
	payload := &pb.AuthEvent{
		UserId:   userID,
		TenantId: tenantID,
		AuthType: authType,
		WalletId: walletID,
		TokenId:  tokenID,
		Error:    errMsg,
	}
	event := &pb.ServiceEvent{
		EventType:    eventType,
		Timestamp:    timestamppb.Now(),
		Source:       "commodore",
		TenantId:     tenantID,
		UserId:       userID,
		ResourceType: "user",
		ResourceId:   userID,
		Payload:      &pb.ServiceEvent_AuthEvent{AuthEvent: payload},
	}
	s.emitServiceEvent(ctx, event)
}

func (s *CommodoreServer) emitStreamChangeEvent(ctx context.Context, eventType, tenantID, userID, streamID string, changedFields []string) {
	payload := &pb.StreamChangeEvent{
		StreamId:      streamID,
		ChangedFields: changedFields,
	}
	event := &pb.ServiceEvent{
		EventType:    eventType,
		Timestamp:    timestamppb.Now(),
		Source:       "commodore",
		TenantId:     tenantID,
		UserId:       userID,
		ResourceType: "stream",
		ResourceId:   streamID,
		Payload:      &pb.ServiceEvent_StreamChangeEvent{StreamChangeEvent: payload},
	}
	s.emitServiceEvent(ctx, event)
}

func (s *CommodoreServer) emitArtifactEvent(ctx context.Context, eventType, tenantID, userID string, artifactType pb.ArtifactEvent_ArtifactType, artifactID, streamID, status string, expiresAt *int64) {
	if artifactID == "" || tenantID == "" {
		return
	}

	payload := &pb.ArtifactEvent{
		ArtifactType: artifactType,
		ArtifactId:   artifactID,
		StreamId:     streamID,
		Status:       status,
	}
	if expiresAt != nil {
		payload.ExpiresAt = expiresAt
	}

	event := &pb.ServiceEvent{
		EventType:    eventType,
		Timestamp:    timestamppb.Now(),
		Source:       "commodore",
		TenantId:     tenantID,
		UserId:       userID,
		ResourceType: "artifact",
		ResourceId:   artifactID,
		Payload:      &pb.ServiceEvent_ArtifactEvent{ArtifactEvent: payload},
	}
	s.emitServiceEvent(ctx, event)
}

func (s *CommodoreServer) emitStreamKeyEvent(ctx context.Context, eventType, tenantID, userID, streamID, keyID string) {
	payload := &pb.StreamKeyEvent{
		StreamId: streamID,
		KeyId:    keyID,
	}
	event := &pb.ServiceEvent{
		EventType:    eventType,
		Timestamp:    timestamppb.Now(),
		Source:       "commodore",
		TenantId:     tenantID,
		UserId:       userID,
		ResourceType: "stream_key",
		ResourceId:   keyID,
		Payload:      &pb.ServiceEvent_StreamKeyEvent{StreamKeyEvent: payload},
	}
	s.emitServiceEvent(ctx, event)
}

// GetStreamsBatch retrieves multiple streams by IDs in a single query
func (s *CommodoreServer) GetStreamsBatch(ctx context.Context, req *pb.GetStreamsBatchRequest) (*pb.GetStreamsBatchResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	streamIDs := req.GetStreamIds()
	if len(streamIDs) == 0 {
		return &pb.GetStreamsBatchResponse{}, nil
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, internal_name, stream_key, playback_id, title, description,
		       is_recording_enabled, created_at, updated_at
		FROM commodore.streams
		WHERE id = ANY($1) AND user_id = $2 AND tenant_id = $3
	`, pq.Array(streamIDs), userID, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var streams []*pb.Stream
	for rows.Next() {
		stream, err := scanStream(rows)
		if err != nil {
			s.logger.WithError(err).Warn("Error scanning stream in batch")
			continue
		}
		streams = append(streams, stream)
	}

	return &pb.GetStreamsBatchResponse{Streams: streams}, nil
}

func (s *CommodoreServer) queryStream(ctx context.Context, streamID, userID, tenantID string) (*pb.Stream, error) {
	var stream pb.Stream
	var description sql.NullString
	var createdAt, updatedAt time.Time

	// Query config only - operational state (status, started_at, ended_at) comes from Periscope Data Plane
	err := s.db.QueryRowContext(ctx, `
		SELECT id, internal_name, stream_key, playback_id, title, description,
		       is_recording_enabled, created_at, updated_at
		FROM commodore.streams
		WHERE id = $1 AND user_id = $2 AND tenant_id = $3
	`, streamID, userID, tenantID).Scan(&stream.StreamId, &stream.InternalName, &stream.StreamKey, &stream.PlaybackId,
		&stream.Title, &description, &stream.IsRecordingEnabled, &createdAt, &updatedAt)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "stream not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	if description.Valid {
		stream.Description = description.String
	}
	stream.IsRecording = stream.IsRecordingEnabled
	// Note: IsLive, Status, StartedAt, EndedAt are now set by Gateway from Periscope (Data Plane)
	stream.CreatedAt = timestamppb.New(createdAt)
	stream.UpdatedAt = timestamppb.New(updatedAt)

	return &stream, nil
}

// scanStream scans config-only stream data; operational state comes from Periscope Data Plane
func scanStream(rows *sql.Rows) (*pb.Stream, error) {
	var stream pb.Stream
	var description sql.NullString
	var createdAt, updatedAt time.Time

	err := rows.Scan(&stream.StreamId, &stream.InternalName, &stream.StreamKey, &stream.PlaybackId,
		&stream.Title, &description, &stream.IsRecordingEnabled, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}

	if description.Valid {
		stream.Description = description.String
	}
	stream.IsRecording = stream.IsRecordingEnabled
	// Note: IsLive, Status, StartedAt, EndedAt are now set by Gateway from Periscope (Data Plane)
	stream.CreatedAt = timestamppb.New(createdAt)
	stream.UpdatedAt = timestamppb.New(updatedAt)

	return &stream, nil
}

func extractUserContext(ctx context.Context) (userID, tenantID string, err error) {
	userID = middleware.GetUserID(ctx)
	tenantID = middleware.GetTenantID(ctx)
	if userID == "" || tenantID == "" {
		return "", "", status.Error(codes.Unauthenticated, "missing user context")
	}
	return userID, tenantID, nil
}

// isTenantSuspended checks if a tenant is suspended due to negative prepaid balance.
// Returns true if the tenant's subscription status is 'suspended'.
func (s *CommodoreServer) isTenantSuspended(ctx context.Context, tenantID string) (bool, error) {
	// Call Purser via gRPC instead of querying purser.* tables directly
	if s.purserClient == nil {
		// No Purser client = assume not suspended (graceful degradation)
		return false, nil
	}

	billingStatus, err := s.purserClient.GetTenantBillingStatus(ctx, tenantID)
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Warn("Failed to get billing status from Purser, assuming not suspended")
		//nolint:nilerr // fail-open: assume not suspended on internal errors
		return false, nil
	}

	return billingStatus.IsSuspended, nil
}

func generateDVRHash() (string, error) {
	token, err := generateSecureToken(8)
	if err != nil {
		return "", err
	}
	return time.Now().Format("20060102150405") + token, nil
}

func generateClipHash() (string, error) {
	token, err := generateSecureToken(8)
	if err != nil {
		return "", err
	}
	return time.Now().Format("20060102150405") + token, nil
}

func generateVodHash() (string, error) {
	token, err := generateSecureToken(8)
	if err != nil {
		return "", err
	}
	return time.Now().Format("20060102150405") + token, nil
}

func generateStreamKey() (string, error) {
	token, err := generateSecureToken(16)
	if err != nil {
		return "", err
	}
	return "sk_" + token, nil
}

const artifactInternalNameLength = 32
const artifactPlaybackIDLength = 16

const alphaNumCharset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

func generateRandomString(length int) (string, error) {
	if length <= 0 {
		return "", nil
	}
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand.Read failed: %w", err)
	}
	for i := range b {
		b[i] = alphaNumCharset[int(b[i])%len(alphaNumCharset)]
	}
	return string(b), nil
}

func generateArtifactInternalName() (string, error) {
	return generateRandomString(artifactInternalNameLength)
}

func generateArtifactPlaybackID() (string, error) {
	return generateRandomString(artifactPlaybackIDLength)
}

func (s *CommodoreServer) identifierExists(ctx context.Context, identifier string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM commodore.streams WHERE internal_name = $1 OR playback_id = $1
			UNION ALL
			SELECT 1 FROM commodore.clips WHERE artifact_internal_name = $1 OR playback_id = $1 OR clip_hash = $1
			UNION ALL
			SELECT 1 FROM commodore.dvr_recordings WHERE artifact_internal_name = $1 OR playback_id = $1 OR dvr_hash = $1
			UNION ALL
			SELECT 1 FROM commodore.vod_assets WHERE artifact_internal_name = $1 OR playback_id = $1 OR vod_hash = $1
		)
	`, identifier).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

func (s *CommodoreServer) generateUniqueArtifactIdentifiers(ctx context.Context) (string, string, error) {
	const maxAttempts = 10
	for i := 0; i < maxAttempts; i++ {
		internalName, err := generateArtifactInternalName()
		if err != nil {
			return "", "", fmt.Errorf("failed to generate internal name: %w", err)
		}
		exists, err := s.identifierExists(ctx, internalName)
		if err != nil {
			return "", "", err
		}
		if exists {
			continue
		}

		playbackID, err := generateArtifactPlaybackID()
		if err != nil {
			return "", "", fmt.Errorf("failed to generate playback ID: %w", err)
		}
		exists, err = s.identifierExists(ctx, playbackID)
		if err != nil {
			return "", "", err
		}
		if exists {
			continue
		}

		return internalName, playbackID, nil
	}
	return "", "", fmt.Errorf("failed to generate unique artifact identifiers")
}

func generateSecureToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand.Read failed: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func getDefaultPermissions(role string) []string {
	switch role {
	case "owner", "admin":
		return []string{"read", "write", "admin"}
	case "member":
		return []string{"read", "write"}
	default:
		return []string{"read"}
	}
}

// ============================================================================
// CLIP SERVICE (Commodore → Foghorn proxy)
// ============================================================================

// CreateClip registers clip in business registry and orchestrates creation via Foghorn
func (s *CommodoreServer) CreateClip(ctx context.Context, req *pb.CreateClipRequest) (*pb.CreateClipResponse, error) {
	// Get user and tenant context from metadata
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Check if tenant is suspended (prepaid balance < -$10)
	if suspended, suspendErr := s.isTenantSuspended(ctx, tenantID); suspendErr != nil {
		s.logger.WithError(suspendErr).Warn("Failed to check tenant suspension status")
	} else if suspended {
		return nil, status.Error(codes.PermissionDenied, "account suspended - please top up your balance to create clips")
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	// Resolve internal_name and active ingest cluster for routing
	var internalName string
	var activeIngestClusterID sql.NullString
	var activeIngestClusterUpdatedAt sql.NullTime
	err = s.db.QueryRowContext(ctx, `
		SELECT internal_name, active_ingest_cluster_id, active_ingest_cluster_updated_at
		FROM commodore.streams
		WHERE id = $1 AND tenant_id = $2
	`, streamID, tenantID).Scan(&internalName, &activeIngestClusterID, &activeIngestClusterUpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "stream not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Route to the cluster where the stream is ingesting (if known), else primary
	var foghornClient *foghornclient.GRPCClient
	var clipClusterID string
	if freshClusterID, ok := selectActiveIngestCluster(activeIngestClusterID, activeIngestClusterUpdatedAt, time.Now()); ok {
		foghornClient, err = s.resolveFoghornForCluster(ctx, freshClusterID, tenantID)
		if err == nil {
			clipClusterID = freshClusterID
		} else {
			s.logger.WithFields(logging.Fields{
				"tenant_id":                  tenantID,
				"stream_id":                  streamID,
				"active_ingest_cluster_id":   freshClusterID,
				"active_ingest_cluster_time": activeIngestClusterUpdatedAt.Time,
				"error":                      err,
			}).Warn("Failed to resolve active ingest cluster for clip, falling back to tenant route")
		}
	}
	if foghornClient == nil {
		var clipRoute *clusterRoute
		foghornClient, clipRoute, err = s.resolveFoghornForTenant(ctx, tenantID)
		if clipRoute != nil {
			clipClusterID = clipRoute.clusterID
		}
	}
	if err != nil {
		return nil, err
	}

	// Generate clip hash (Commodore is authoritative)
	clipHash, err := generateClipHash()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate clip hash: %v", err)
	}
	clipID := uuid.New().String()
	artifactInternalName, playbackID, err := s.generateUniqueArtifactIdentifiers(ctx)
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id":     tenantID,
			"internal_name": internalName,
			"error":         err,
		}).Error("Failed to generate artifact identifiers for clip")
		return nil, status.Errorf(codes.Internal, "failed to generate clip identifiers: %v", err)
	}

	// Resolve timing for storage
	var startTime, duration int64
	if req.StartUnix != nil {
		startTime = *req.StartUnix * 1000 // Convert to ms
	}
	if req.DurationSec != nil {
		duration = *req.DurationSec * 1000 // Convert to ms
	} else if req.StartUnix != nil && req.StopUnix != nil {
		duration = (*req.StopUnix - *req.StartUnix) * 1000
	}

	// Resolve retention
	var retentionUntil *time.Time
	if req.ExpiresAt != nil {
		t := time.Unix(*req.ExpiresAt, 0)
		retentionUntil = &t
	} else {
		t := time.Now().Add(30 * 24 * time.Hour) // Default 30 days
		retentionUntil = &t
	}

	// Store requested params as JSON for audit
	requestedParams := map[string]interface{}{}
	if req.StartUnix != nil {
		requestedParams["start_unix"] = *req.StartUnix
	}
	if req.StopUnix != nil {
		requestedParams["stop_unix"] = *req.StopUnix
	}
	if req.DurationSec != nil {
		requestedParams["duration_sec"] = *req.DurationSec
	}
	paramsJSON, _ := json.Marshal(requestedParams)

	// Register in commodore.clips (business registry)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO commodore.clips (
			id, tenant_id, user_id, stream_id, clip_hash, artifact_internal_name, playback_id,
			title, description, start_time, duration, clip_mode, requested_params,
			origin_cluster_id, retention_until, created_at, updated_at
		) VALUES ($1, $2, $3, NULLIF($4, '')::uuid, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, NOW(), NOW())
	`, clipID, tenantID, userID, streamID, clipHash, artifactInternalName, playbackID,
		req.Title, req.Description, startTime, duration, req.Mode.String(), string(paramsJSON),
		clipClusterID, retentionUntil)

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id":     tenantID,
			"internal_name": internalName,
			"error":         err,
		}).Error("Failed to register clip in business registry")
		return nil, status.Errorf(codes.Internal, "failed to register clip: %v", err)
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":     tenantID,
		"clip_hash":     clipHash,
		"clip_id":       clipID,
		"internal_name": internalName,
	}).Info("Registered clip in business registry")

	// Build Foghorn request with pre-generated hash
	foghornReq := &pb.CreateClipRequest{
		TenantId:             tenantID,
		InternalName:         internalName,
		ClipHash:             &clipHash, // Pass the hash we generated
		PlaybackId:           &playbackID,
		ArtifactInternalName: &artifactInternalName,
	}
	if streamID != "" {
		foghornReq.StreamId = &streamID
	}
	if req.Format != "" {
		foghornReq.Format = req.Format
	}
	foghornReq.StartUnix = req.StartUnix
	foghornReq.StopUnix = req.StopUnix
	foghornReq.StartMs = req.StartMs
	foghornReq.StopMs = req.StopMs
	foghornReq.DurationSec = req.DurationSec
	foghornReq.ExpiresAt = func() *int64 { t := retentionUntil.Unix(); return &t }()

	// Call Foghorn for artifact lifecycle management
	resp, trailers, err := foghornClient.CreateClip(ctx, foghornReq)
	if err != nil {
		s.logger.WithError(err).WithField("clip_hash", clipHash).Error("Failed to create clip artifact via Foghorn")
		// Don't rollback business registry - Foghorn can retry later
		return nil, grpcutil.PropagateError(ctx, err, trailers)
	}

	return resp, nil
}

// GetClips returns clips from Commodore business registry
// Lifecycle data (status, size, storage) comes from Periscope via GraphQL field resolvers
func (s *CommodoreServer) GetClips(ctx context.Context, req *pb.GetClipsRequest) (*pb.GetClipsResponse, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// Build WHERE clause with optional stream filter
	whereClause := "c.tenant_id = $1"
	args := []interface{}{tenantID}
	argIdx := 2

	if streamID := req.GetStreamId(); streamID != "" {
		whereClause += fmt.Sprintf(" AND c.stream_id = $%d", argIdx)
		args = append(args, streamID)
		argIdx++
	}

	// Get total count
	var total int32
	countQuery := fmt.Sprintf(`
		SELECT COUNT(*) FROM commodore.clips c
		WHERE %s`, whereClause)
	if countErr := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); countErr != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", countErr)
	}

	// Build keyset pagination query
	builder := &pagination.KeysetBuilder{
		TimestampColumn: "c.created_at",
		IDColumn:        "c.clip_hash",
	}

	// Base query
	query := fmt.Sprintf(`
		SELECT c.id, c.clip_hash, c.playback_id, c.stream_id::text, c.title, c.description,
		       c.start_time, c.duration, c.clip_mode, c.requested_params,
		       c.retention_until, c.created_at, c.updated_at
		FROM commodore.clips c
		WHERE %s`, whereClause)

	// Add keyset condition if cursor provided
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		query += " AND " + condition
		args = append(args, cursorArgs...)
	}

	// Add ORDER BY and LIMIT
	query += " " + builder.OrderBy(params)
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var clips []*pb.ClipInfo
	for rows.Next() {
		var (
			id, clipHash, playbackID, streamID string
			title, description                 sql.NullString
			startTime, duration                int64
			clipMode                           sql.NullString
			requestedParams                    sql.NullString
			retentionUntil                     sql.NullTime
			createdAt, updatedAt               time.Time
		)
		if err := rows.Scan(&id, &clipHash, &playbackID, &streamID, &title, &description,
			&startTime, &duration, &clipMode, &requestedParams,
			&retentionUntil, &createdAt, &updatedAt); err != nil {
			s.logger.WithError(err).Warn("Error scanning clip")
			continue
		}

		clip := &pb.ClipInfo{
			Id:         id,
			ClipHash:   clipHash,
			PlaybackId: playbackID,
			StreamId:   streamID,
			StartTime:  startTime / 1000, // Convert ms to seconds
			Duration:   duration / 1000,  // Convert ms to seconds
			Status:     "registry",       // Indicates business registry data, lifecycle from Foghorn
			CreatedAt:  timestamppb.New(createdAt),
			UpdatedAt:  timestamppb.New(updatedAt),
		}
		if title.Valid {
			clip.Title = title.String
		}
		if description.Valid {
			clip.Description = description.String
		}
		if clipMode.Valid {
			clip.ClipMode = &clipMode.String
		}
		if requestedParams.Valid {
			clip.RequestedParams = &requestedParams.String
		}
		if retentionUntil.Valid {
			expiresAt := timestamppb.New(retentionUntil.Time)
			clip.ExpiresAt = expiresAt
		}

		clips = append(clips, clip)
	}

	// Detect hasMore and trim results
	hasMore := len(clips) > params.Limit
	if hasMore {
		clips = clips[:params.Limit]
	}

	// Reverse results if backward pagination
	if params.Direction == pagination.Backward && len(clips) > 0 {
		for i, j := 0, len(clips)-1; i < j; i, j = i+1, j-1 {
			clips[i], clips[j] = clips[j], clips[i]
		}
	}

	// Build cursors from results
	var startCursor, endCursor string
	if len(clips) > 0 {
		first := clips[0]
		last := clips[len(clips)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.ClipHash)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.ClipHash)
	}

	// Build response
	resp := &pb.GetClipsResponse{
		Clips: clips,
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

// GetClip returns clip business registry metadata (no lifecycle data).
// Lifecycle/access data must come from Periscope (data plane).
func (s *CommodoreServer) GetClip(ctx context.Context, req *pb.GetClipRequest) (*pb.ClipInfo, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	clipHash := req.GetClipHash()
	if clipHash == "" {
		return nil, status.Error(codes.InvalidArgument, "clip_hash is required")
	}

	query := `
		SELECT c.id, c.clip_hash, c.playback_id, c.stream_id::text, c.title, c.description,
		       c.start_time, c.duration, c.clip_mode, c.requested_params,
		       c.retention_until, c.created_at, c.updated_at
		FROM commodore.clips c
		WHERE c.tenant_id = $1 AND c.clip_hash = $2
	`

	var (
		id, streamID, playbackID string
		title, description       sql.NullString
		startTime, duration      int64
		clipMode                 sql.NullString
		requestedParams          sql.NullString
		retentionUntil           sql.NullTime
		createdAt, updatedAt     time.Time
	)

	err = s.db.QueryRowContext(ctx, query, tenantID, clipHash).Scan(
		&id, &clipHash, &playbackID, &streamID, &title, &description,
		&startTime, &duration, &clipMode, &requestedParams,
		&retentionUntil, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "clip not found")
	} else if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	clip := &pb.ClipInfo{
		Id:         id,
		ClipHash:   clipHash,
		PlaybackId: playbackID,
		StreamId:   streamID,
		StartTime:  startTime / 1000, // Convert ms to seconds
		Duration:   duration / 1000,  // Convert ms to seconds
		Status:     "registry",
		CreatedAt:  timestamppb.New(createdAt),
		UpdatedAt:  timestamppb.New(updatedAt),
	}
	if title.Valid {
		clip.Title = title.String
	}
	if description.Valid {
		clip.Description = description.String
	}
	if clipMode.Valid {
		clip.ClipMode = &clipMode.String
	}
	if requestedParams.Valid {
		clip.RequestedParams = &requestedParams.String
	}
	if retentionUntil.Valid {
		expiresAt := timestamppb.New(retentionUntil.Time)
		clip.ExpiresAt = expiresAt
	}

	return clip, nil
}

// DeleteClip proxies clip deletion to Foghorn
func (s *CommodoreServer) DeleteClip(ctx context.Context, req *pb.DeleteClipRequest) (*pb.DeleteClipResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Look up clip info for deletion event and cluster-aware routing
	var streamID string
	var originClusterID sql.NullString
	_ = s.db.QueryRowContext(ctx, `
		SELECT stream_id::text, origin_cluster_id FROM commodore.clips
		WHERE clip_hash = $1 AND tenant_id = $2
	`, req.ClipHash, tenantID).Scan(&streamID, &originClusterID)

	foghornClient, err := s.resolveFoghornForArtifact(ctx, tenantID, originClusterID.String)
	if err != nil {
		return nil, err
	}

	resp, trailers, err := foghornClient.DeleteClip(ctx, req.ClipHash, &tenantID)
	if err != nil {
		s.logger.WithError(err).Error("Failed to delete clip via Foghorn")
		return nil, grpcutil.PropagateError(ctx, err, trailers)
	}

	// Delete from business registry (matches VOD pattern)
	if resp.Success {
		_, delErr := s.db.ExecContext(ctx, `
			DELETE FROM commodore.clips
			WHERE clip_hash = $1 AND tenant_id = $2
		`, req.ClipHash, tenantID)
		if delErr != nil {
			s.logger.WithError(delErr).WithField("clip_hash", req.ClipHash).Warn("Failed to delete clip from business registry")
		}

		s.emitArtifactEvent(ctx, eventArtifactDeleted, tenantID, userID, pb.ArtifactEvent_ARTIFACT_TYPE_CLIP, req.ClipHash, streamID, "deleted", nil)
	}

	return resp, nil
}

// ============================================================================
// DVR SERVICE (Commodore → Foghorn proxy)
// ============================================================================

// StopDVR proxies DVR stop to Foghorn
func (s *CommodoreServer) StopDVR(ctx context.Context, req *pb.StopDVRRequest) (*pb.StopDVRResponse, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	var streamID *string
	var streamIDValue string
	var originClusterID sql.NullString
	if streamErr := s.db.QueryRowContext(ctx, `
		SELECT stream_id::text, origin_cluster_id
		FROM commodore.dvr_recordings
		WHERE dvr_hash = $1 AND tenant_id = $2
	`, req.DvrHash, tenantID).Scan(&streamIDValue, &originClusterID); streamErr == nil && streamIDValue != "" {
		streamID = &streamIDValue
	}

	foghornClient, err := s.resolveFoghornForArtifact(ctx, tenantID, originClusterID.String)
	if err != nil {
		return nil, err
	}

	resp, trailers, err := foghornClient.StopDVR(ctx, req.DvrHash, &tenantID, streamID)
	if err != nil {
		s.logger.WithError(err).Error("Failed to stop DVR via Foghorn")
		return nil, grpcutil.PropagateError(ctx, err, trailers)
	}

	return resp, nil
}

// DeleteDVR proxies DVR deletion to Foghorn
func (s *CommodoreServer) DeleteDVR(ctx context.Context, req *pb.DeleteDVRRequest) (*pb.DeleteDVRResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Look up DVR info for deletion event and cluster-aware routing
	var streamID string
	var originClusterID sql.NullString
	_ = s.db.QueryRowContext(ctx, `
		SELECT stream_id::text, origin_cluster_id FROM commodore.dvr_recordings
		WHERE dvr_hash = $1 AND tenant_id = $2
	`, req.DvrHash, tenantID).Scan(&streamID, &originClusterID)

	foghornClient, err := s.resolveFoghornForArtifact(ctx, tenantID, originClusterID.String)
	if err != nil {
		return nil, err
	}

	resp, trailers, err := foghornClient.DeleteDVR(ctx, req.DvrHash, &tenantID)
	if err != nil {
		s.logger.WithError(err).Error("Failed to delete DVR via Foghorn")
		return nil, grpcutil.PropagateError(ctx, err, trailers)
	}

	// Delete from business registry (matches VOD pattern)
	if resp.Success {
		_, delErr := s.db.ExecContext(ctx, `
			DELETE FROM commodore.dvr_recordings
			WHERE dvr_hash = $1 AND tenant_id = $2
		`, req.DvrHash, tenantID)
		if delErr != nil {
			s.logger.WithError(delErr).WithField("dvr_hash", req.DvrHash).Warn("Failed to delete DVR from business registry")
		}

		s.emitArtifactEvent(ctx, eventArtifactDeleted, tenantID, userID, pb.ArtifactEvent_ARTIFACT_TYPE_DVR, req.DvrHash, streamID, "deleted", nil)
	}

	return resp, nil
}

// ListDVRRequests returns DVR recordings from Commodore business registry
// Lifecycle data (status, size, storage) comes from Periscope via GraphQL field resolvers
func (s *CommodoreServer) ListDVRRequests(ctx context.Context, req *pb.ListDVRRecordingsRequest) (*pb.ListDVRRecordingsResponse, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// Build WHERE clause with optional stream filter
	whereClause := "d.tenant_id = $1"
	args := []interface{}{tenantID}
	argIdx := 2

	if streamID := req.GetStreamId(); streamID != "" {
		whereClause += fmt.Sprintf(" AND d.stream_id = $%d", argIdx)
		args = append(args, streamID)
		argIdx++
	}

	// Get total count
	var total int32
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM commodore.dvr_recordings d WHERE %s", whereClause)
	if countErr := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); countErr != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", countErr)
	}

	// Build keyset pagination query
	builder := &pagination.KeysetBuilder{
		TimestampColumn: "d.created_at",
		IDColumn:        "d.dvr_hash",
	}

	// Base query - join with streams to get title
	query := fmt.Sprintf(`
		SELECT d.id, d.dvr_hash, d.playback_id, d.internal_name, d.stream_id::text, COALESCE(st.title, d.internal_name),
		       d.retention_until, d.created_at, d.updated_at
		FROM commodore.dvr_recordings d
		LEFT JOIN commodore.streams st ON d.stream_id = st.id
		WHERE %s`, whereClause)

	// Add keyset condition if cursor provided
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		query += " AND " + condition
		args = append(args, cursorArgs...)
	}

	// Add ORDER BY and LIMIT
	query += " " + builder.OrderBy(params)
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var recordings []*pb.DVRInfo
	for rows.Next() {
		var (
			id, dvrHash, playbackID, internalName, streamID, title string
			retentionUntil                                         sql.NullTime
			createdAt, updatedAt                                   time.Time
		)
		if err := rows.Scan(&id, &dvrHash, &playbackID, &internalName, &streamID, &title,
			&retentionUntil, &createdAt, &updatedAt); err != nil {
			s.logger.WithError(err).Warn("Error scanning DVR recording")
			continue
		}

		recording := &pb.DVRInfo{
			Id:           &id,
			DvrHash:      dvrHash,
			PlaybackId:   &playbackID,
			InternalName: internalName,
			StreamId:     &streamID,
			Title:        &title,
			CreatedAt:    timestamppb.New(createdAt),
			UpdatedAt:    timestamppb.New(updatedAt),
		}
		if retentionUntil.Valid {
			expiresAt := timestamppb.New(retentionUntil.Time)
			recording.ExpiresAt = expiresAt
		}

		recordings = append(recordings, recording)
	}

	// Detect hasMore and trim results
	hasMore := len(recordings) > params.Limit
	if hasMore {
		recordings = recordings[:params.Limit]
	}

	// Reverse results if backward pagination
	if params.Direction == pagination.Backward && len(recordings) > 0 {
		for i, j := 0, len(recordings)-1; i < j; i, j = i+1, j-1 {
			recordings[i], recordings[j] = recordings[j], recordings[i]
		}
	}

	// Build cursors from results
	var startCursor, endCursor string
	if len(recordings) > 0 {
		first := recordings[0]
		last := recordings[len(recordings)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.DvrHash)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.DvrHash)
	}

	// Build response
	resp := &pb.ListDVRRecordingsResponse{
		DvrRecordings: recordings,
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

// ============================================================================
// VIEWER SERVICE (Commodore → Foghorn proxy with enrichment)
// ============================================================================

// ResolveViewerEndpoint proxies viewer endpoint resolution to Foghorn
// and enriches the response with stream metadata from Commodore's database
func (s *CommodoreServer) ResolveViewerEndpoint(ctx context.Context, req *pb.ViewerEndpointRequest) (*pb.ViewerEndpointResponse, error) {
	tenantID := ctxkeys.GetTenantID(ctx)

	var foghornClient *foghornclient.GRPCClient
	var err error
	if tenantID == "" {
		foghornClient, _, err = s.resolveFoghornForContent(ctx, req.ContentId)
	} else {
		foghornClient, _, err = s.resolveFoghornForTenant(ctx, tenantID)
	}
	if err != nil {
		return nil, err
	}

	outCtx := ctx
	if md, ok := metadata.FromIncomingContext(ctx); ok && md != nil {
		forward := metadata.MD{}
		for _, key := range []string{"x-payment", "payment-signature", "x402-paid"} {
			if values := md.Get(key); len(values) > 0 {
				forward.Set(key, values...)
			}
		}
		if len(forward) > 0 {
			if existing, ok := metadata.FromOutgoingContext(ctx); ok {
				forward = metadata.Join(existing, forward)
			}
			outCtx = metadata.NewOutgoingContext(ctx, forward)
		}
	}

	resp, trailers, err := foghornClient.ResolveViewerEndpoint(outCtx, req.ContentId, req.ViewerIp)
	if err != nil {
		s.logger.WithError(err).Error("Failed to resolve viewer endpoint from Foghorn")
		return nil, grpcutil.PropagateError(ctx, err, trailers)
	}

	// Enrich metadata with stream info from Commodore's database
	// For live streams, Foghorn doesn't have title/description - we do
	if resp.Metadata != nil {
		isLive := resp.Metadata.GetIsLive() || strings.EqualFold(resp.Metadata.GetContentType(), "live")
		if !isLive {
			return resp, nil
		}
		streamID := resp.Metadata.GetStreamId()
		if streamID != "" {
			var title, description sql.NullString
			if tenantID := resp.Metadata.GetTenantId(); tenantID != "" {
				err := s.db.QueryRowContext(ctx, `
					SELECT title, description FROM commodore.streams WHERE id = $1 AND tenant_id = $2
				`, streamID, tenantID).Scan(&title, &description)
				if err == nil {
					if title.Valid && title.String != "" {
						resp.Metadata.Title = &title.String
					}
					if description.Valid && description.String != "" {
						resp.Metadata.Description = &description.String
					}
				}
			} else {
				err := s.db.QueryRowContext(ctx, `
					SELECT title, description FROM commodore.streams WHERE id = $1
				`, streamID).Scan(&title, &description)
				if err == nil {
					if title.Valid && title.String != "" {
						resp.Metadata.Title = &title.String
					}
					if description.Valid && description.String != "" {
						resp.Metadata.Description = &description.String
					}
				}
			}
			// Silently ignore errors - enrichment is best-effort, don't fail the request
		}
	}

	return resp, nil
}

// ResolveIngestEndpoint proxies ingest endpoint resolution to Foghorn
// and enriches the response with stream metadata from Commodore's database
func (s *CommodoreServer) ResolveIngestEndpoint(ctx context.Context, req *pb.IngestEndpointRequest) (*pb.IngestEndpointResponse, error) {
	tenantID := ctxkeys.GetTenantID(ctx)

	var foghornClient *foghornclient.GRPCClient
	var err error
	if tenantID == "" {
		foghornClient, _, err = s.resolveFoghornForStreamKey(ctx, req.StreamKey)
	} else {
		foghornClient, _, err = s.resolveFoghornForTenant(ctx, tenantID)
	}
	if err != nil {
		return nil, err
	}

	resp, trailers, err := foghornClient.ResolveIngestEndpoint(ctx, req.StreamKey, req.ViewerIp)
	if err != nil {
		s.logger.WithError(err).Error("Failed to resolve ingest endpoint from Foghorn")
		return nil, grpcutil.PropagateError(ctx, err, trailers)
	}

	// Enrich metadata with stream info from Commodore's database (best-effort)
	if resp.Metadata != nil && resp.Metadata.StreamId != "" {
		var title, description sql.NullString
		if resp.Metadata.TenantId != "" {
			err := s.db.QueryRowContext(ctx, `
				SELECT title, description FROM commodore.streams WHERE id = $1 AND tenant_id = $2
			`, resp.Metadata.StreamId, resp.Metadata.TenantId).Scan(&title, &description)
			if err == nil {
				if title.Valid && title.String != "" {
					resp.Metadata.Title = &title.String
				}
				if description.Valid && description.String != "" {
					resp.Metadata.Description = &description.String
				}
			}
		} else {
			err := s.db.QueryRowContext(ctx, `
				SELECT title, description FROM commodore.streams WHERE id = $1
			`, resp.Metadata.StreamId).Scan(&title, &description)
			if err == nil {
				if title.Valid && title.String != "" {
					resp.Metadata.Title = &title.String
				}
				if description.Valid && description.String != "" {
					resp.Metadata.Description = &description.String
				}
			}
		}
		// Silently ignore errors - enrichment is best-effort
	}

	return resp, nil
}

// ============================================================================
// NODE MANAGEMENT SERVICE (Commodore → Foghorn proxy)
// ============================================================================

// resolveFoghornForNode resolves the Foghorn managing a specific node's cluster.
// Unlike resolveFoghornForTenant (tenant's primary cluster), this resolves the
// node's cluster and validates the requesting tenant owns the node.
func (s *CommodoreServer) resolveFoghornForNode(ctx context.Context, nodeID, requestingTenantID string) (*foghornclient.GRPCClient, error) {
	if s.quartermasterClient == nil {
		return nil, status.Error(codes.Unavailable, "quartermaster not available")
	}

	owner, err := s.quartermasterClient.GetNodeOwner(ctx, nodeID)
	if err != nil {
		return nil, err
	}

	if owner.OwnerTenantId == nil || *owner.OwnerTenantId != requestingTenantID {
		return nil, status.Error(codes.PermissionDenied, "node is not owned by this tenant")
	}

	foghornAddr := owner.GetFoghornGrpcAddr()
	if foghornAddr == "" {
		return nil, status.Errorf(codes.Unavailable, "no foghorn registered for cluster %s", owner.ClusterId)
	}

	client, err := s.foghornPool.GetOrCreate(owner.ClusterId, foghornAddr)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "foghorn connection failed for cluster %s: %v", owner.ClusterId, err)
	}
	return client, nil
}

// SetNodeOperationalMode proxies mode changes to Foghorn via the node's cluster.
func (s *CommodoreServer) SetNodeOperationalMode(ctx context.Context, req *pb.SetNodeModeRequest) (*pb.SetNodeModeResponse, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	foghornClient, err := s.resolveFoghornForNode(ctx, req.GetNodeId(), tenantID)
	if err != nil {
		return nil, err
	}

	resp, trailers, err := foghornClient.SetNodeMode(ctx, req)
	if err != nil {
		return nil, grpcutil.PropagateError(ctx, err, trailers)
	}
	return resp, nil
}

// GetNodeHealth proxies health queries to Foghorn via the node's cluster.
func (s *CommodoreServer) GetNodeHealth(ctx context.Context, req *pb.GetNodeHealthRequest) (*pb.GetNodeHealthResponse, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	foghornClient, err := s.resolveFoghornForNode(ctx, req.GetNodeId(), tenantID)
	if err != nil {
		return nil, err
	}

	resp, trailers, err := foghornClient.GetNodeHealth(ctx, req)
	if err != nil {
		return nil, grpcutil.PropagateError(ctx, err, trailers)
	}
	return resp, nil
}

// ============================================================================
// SERVER SETUP
// ============================================================================

// NewGRPCServer creates a new gRPC server for Commodore with all services registered
func NewGRPCServer(cfg CommodoreServerConfig) *grpc.Server {
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
		grpc.ChainUnaryInterceptor(unaryInterceptor(cfg.Logger), authInterceptor),
	}

	server := grpc.NewServer(opts...)
	commodoreServer := NewCommodoreServer(cfg)

	// Register all services
	pb.RegisterInternalServiceServer(server, commodoreServer)
	pb.RegisterUserServiceServer(server, commodoreServer)
	pb.RegisterStreamServiceServer(server, commodoreServer)
	pb.RegisterStreamKeyServiceServer(server, commodoreServer)
	pb.RegisterDeveloperServiceServer(server, commodoreServer)
	// ClipService, DVRService, ViewerService, and VodService proxy to Foghorn via gRPC
	pb.RegisterClipServiceServer(server, commodoreServer)
	pb.RegisterDVRServiceServer(server, commodoreServer)
	pb.RegisterViewerServiceServer(server, commodoreServer)
	pb.RegisterVodServiceServer(server, commodoreServer)
	pb.RegisterNodeManagementServiceServer(server, commodoreServer)
	pb.RegisterPushTargetServiceServer(server, commodoreServer)

	// Register gRPC health checking service
	hs := health.NewServer()
	grpc_health_v1.RegisterHealthServer(server, hs)
	reflection.Register(server)

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
		return resp, grpcutil.SanitizeError(err)
	}
}

// ============================================================================
// HELPER FUNCTIONS
// ============================================================================

// hashToken creates a SHA-256 hash of a token for secure storage (fallback when no secret configured)
func hashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

// hashTokenWithSecret creates an HMAC-SHA256 hash of a token using the configured secret
// Falls back to plain SHA-256 if no secret is configured
func (s *CommodoreServer) hashTokenWithSecret(token string) string {
	if len(s.passwordResetSecret) > 0 {
		h := hmac.New(sha256.New, s.passwordResetSecret)
		h.Write([]byte(token))
		return hex.EncodeToString(h.Sum(nil))
	}
	// Fallback to plain hash if no secret configured
	return hashToken(token)
}

// sendVerificationEmail sends an email verification link
func (s *CommodoreServer) sendVerificationEmail(email, token string) error {
	smtpHost := os.Getenv("SMTP_HOST")
	smtpPort := os.Getenv("SMTP_PORT")
	smtpUser := os.Getenv("SMTP_USER")
	smtpPass := os.Getenv("SMTP_PASSWORD")

	if smtpHost == "" {
		s.logger.Warn("SMTP not configured, skipping verification email")
		return nil
	}

	if smtpPort == "" {
		smtpPort = "587"
	}

	fromEmail := os.Getenv("FROM_EMAIL")
	if fromEmail == "" {
		fromEmail = "noreply@frameworks.network"
	}

	baseURL := strings.TrimSpace(os.Getenv("WEBAPP_PUBLIC_URL"))
	if baseURL == "" {
		return fmt.Errorf("WEBAPP_PUBLIC_URL is required")
	}
	verifyURL := fmt.Sprintf("%s/verify-email?token=%s", baseURL, url.QueryEscape(token))

	subject := "Verify your FrameWorks account"
	body := fmt.Sprintf(`
<!DOCTYPE html><html><body>
  <p>Welcome to FrameWorks!</p>
  <p>Please <a href="%s">click here to verify your email address</a>.</p>
  <p>This link expires in 24 hours.</p>
  <p>If you did not create an account, you can ignore this email.</p>
</body></html>`, verifyURL)

	auth := smtp.PlainAuth("", smtpUser, smtpPass, smtpHost)
	msg := []byte(fmt.Sprintf("To: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s", email, subject, body))
	return smtp.SendMail(smtpHost+":"+smtpPort, auth, fromEmail, []string{email}, msg)
}

// sendPasswordResetEmail sends a password reset link
func (s *CommodoreServer) sendPasswordResetEmail(email, token string) error {
	smtpHost := os.Getenv("SMTP_HOST")
	smtpPort := os.Getenv("SMTP_PORT")
	smtpUser := os.Getenv("SMTP_USER")
	smtpPass := os.Getenv("SMTP_PASSWORD")

	if smtpHost == "" {
		s.logger.Warn("SMTP not configured, skipping password reset email")
		return nil
	}

	if smtpPort == "" {
		smtpPort = "587"
	}

	fromEmail := os.Getenv("FROM_EMAIL")
	if fromEmail == "" {
		fromEmail = "noreply@frameworks.network"
	}

	baseURL := strings.TrimSpace(os.Getenv("WEBAPP_PUBLIC_URL"))
	if baseURL == "" {
		return fmt.Errorf("WEBAPP_PUBLIC_URL is required")
	}
	resetURL := fmt.Sprintf("%s/reset-password?token=%s", baseURL, url.QueryEscape(token))

	subject := "Reset your FrameWorks password"
	body := fmt.Sprintf(`
<!DOCTYPE html><html><body>
  <p>We received a request to reset your password.</p>
  <p><a href="%s">Click here to reset your password</a> (valid for 1 hour).</p>
  <p>If you did not request this, you can safely ignore this email.</p>
</body></html>`, resetURL)

	auth := smtp.PlainAuth("", smtpUser, smtpPass, smtpHost)
	msg := []byte(fmt.Sprintf("To: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s", email, subject, body))
	return smtp.SendMail(smtpHost+":"+smtpPort, auth, fromEmail, []string{email}, msg)
}

// ============================================================================
// VOD SERVICE (Gateway → Commodore → Foghorn proxy)
// User-initiated video uploads (distinct from clips/DVR which are stream-derived)
// ============================================================================

// CreateVodUpload registers VOD in business registry and initiates multipart upload via Foghorn
func (s *CommodoreServer) CreateVodUpload(ctx context.Context, req *pb.CreateVodUploadRequest) (*pb.CreateVodUploadResponse, error) {
	// Get user and tenant context from metadata
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	foghornClient, vodRoute, err := s.resolveFoghornForTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	// Check if tenant is suspended (prepaid balance < -$10)
	if suspended, suspendErr := s.isTenantSuspended(ctx, tenantID); suspendErr != nil {
		s.logger.WithError(suspendErr).Warn("Failed to check tenant suspension status")
		// Continue anyway - don't block on suspension check failure
	} else if suspended {
		return nil, status.Error(codes.PermissionDenied, "account suspended - please top up your balance to upload videos")
	}

	// Generate VOD hash (Commodore is authoritative)
	vodHash, err := generateVodHash()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate VOD hash: %v", err)
	}
	vodID := uuid.New().String()
	artifactInternalName, playbackID, err := s.generateUniqueArtifactIdentifiers(ctx)
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"filename":  req.Filename,
			"error":     err,
		}).Error("Failed to generate artifact identifiers for VOD upload")
		return nil, status.Errorf(codes.Internal, "failed to generate VOD identifiers: %v", err)
	}

	// Resolve retention (default 90 days for VOD)
	retentionUntil := time.Now().Add(90 * 24 * time.Hour)

	// Register in commodore.vod_assets (business registry)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO commodore.vod_assets (
			id, tenant_id, user_id, vod_hash, artifact_internal_name, playback_id,
			title, description, filename, content_type, size_bytes,
			origin_cluster_id, retention_until, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, NOW(), NOW())
	`, vodID, tenantID, userID, vodHash, artifactInternalName, playbackID,
		req.GetTitle(), req.GetDescription(), req.Filename, req.GetContentType(), req.SizeBytes,
		vodRoute.clusterID, retentionUntil)

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"filename":  req.Filename,
			"error":     err,
		}).Error("Failed to register VOD asset in business registry")
		return nil, status.Errorf(codes.Internal, "failed to register VOD asset: %v", err)
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id": tenantID,
		"vod_hash":  vodHash,
		"vod_id":    vodID,
		"filename":  req.Filename,
	}).Info("Registered VOD asset in business registry")

	// Build Foghorn request with pre-generated hash
	foghornReq := &pb.CreateVodUploadRequest{
		TenantId:             tenantID,
		UserId:               userID,
		Filename:             req.Filename,
		SizeBytes:            req.SizeBytes,
		ContentType:          req.ContentType,
		Title:                req.Title,
		Description:          req.Description,
		VodHash:              &vodHash, // Pass the hash we generated
		PlaybackId:           &playbackID,
		ArtifactInternalName: &artifactInternalName,
	}

	// Call Foghorn for S3 multipart upload setup
	resp, trailers, err := foghornClient.CreateVodUpload(ctx, foghornReq)
	if err != nil {
		s.logger.WithError(err).WithField("vod_hash", vodHash).Error("Failed to create VOD upload via Foghorn")
		// Don't rollback business registry - can be cleaned up later
		return nil, grpcutil.PropagateError(ctx, err, trailers)
	}

	if resp != nil && resp.PlaybackId == "" {
		resp.PlaybackId = playbackID
	}
	return resp, nil
}

// CompleteVodUpload finalizes multipart upload via Foghorn
func (s *CommodoreServer) CompleteVodUpload(ctx context.Context, req *pb.CompleteVodUploadRequest) (*pb.CompleteVodUploadResponse, error) {
	// Get tenant context from metadata (for logging/verification)
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	foghornClient, _, err := s.resolveFoghornForTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	// Check if tenant is suspended (prepaid balance < -$10)
	if suspended, suspendErr := s.isTenantSuspended(ctx, tenantID); suspendErr != nil {
		s.logger.WithError(suspendErr).Warn("Failed to check tenant suspension status")
	} else if suspended {
		return nil, status.Error(codes.PermissionDenied, "account suspended - please top up your balance to complete uploads")
	}

	// Forward to Foghorn (it manages S3 multipart completion and lifecycle state)
	foghornReq := &pb.CompleteVodUploadRequest{
		TenantId: tenantID,
		UploadId: req.UploadId,
		Parts:    req.Parts,
	}

	resp, trailers, err := foghornClient.CompleteVodUpload(ctx, foghornReq)
	if err != nil {
		s.logger.WithError(err).WithField("upload_id", req.UploadId).Error("Failed to complete VOD upload via Foghorn")
		return nil, grpcutil.PropagateError(ctx, err, trailers)
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":     tenantID,
		"upload_id":     req.UploadId,
		"artifact_hash": resp.GetAsset().GetArtifactHash(),
	}).Info("Completed VOD upload")

	return resp, nil
}

// AbortVodUpload cancels multipart upload via Foghorn
func (s *CommodoreServer) AbortVodUpload(ctx context.Context, req *pb.AbortVodUploadRequest) (*pb.AbortVodUploadResponse, error) {
	// Get tenant context from metadata
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	foghornClient, _, err := s.resolveFoghornForTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	// Forward to Foghorn (it manages S3 multipart abort and lifecycle state)
	resp, trailers, err := foghornClient.AbortVodUpload(ctx, tenantID, req.UploadId)
	if err != nil {
		s.logger.WithError(err).WithField("upload_id", req.UploadId).Error("Failed to abort VOD upload via Foghorn")
		return nil, grpcutil.PropagateError(ctx, err, trailers)
	}

	// TODO: Clean up orphaned business registry entry (or let retention job handle it)

	s.logger.WithFields(logging.Fields{
		"tenant_id": tenantID,
		"upload_id": req.UploadId,
	}).Info("Aborted VOD upload")

	return resp, nil
}

// GetVodAsset returns VOD business metadata from Commodore registry
// Lifecycle data (status, size, storage) comes from Periscope via Gateway's ArtifactLifecycleLoader
func (s *CommodoreServer) GetVodAsset(ctx context.Context, req *pb.GetVodAssetRequest) (*pb.VodAssetInfo, error) {
	// Get tenant context from metadata
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Query business metadata from Commodore registry ONLY - no Foghorn call
	var (
		id                   string
		vodHash              string
		playbackID           string
		title, description   sql.NullString
		filename             string
		contentType          sql.NullString
		sizeBytes            sql.NullInt64
		retentionUntil       sql.NullTime
		createdAt, updatedAt time.Time
	)
	err = s.db.QueryRowContext(ctx, `
		SELECT id, vod_hash, playback_id, title, description, filename, content_type,
		       size_bytes, retention_until, created_at, updated_at
		FROM commodore.vod_assets
		WHERE vod_hash = $1 AND tenant_id = $2
	`, req.ArtifactHash, tenantID).Scan(
		&id, &vodHash, &playbackID, &title, &description, &filename, &contentType,
		&sizeBytes, &retentionUntil, &createdAt, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "VOD asset not found")
		}
		s.logger.WithError(err).WithField("artifact_hash", req.ArtifactHash).Error("Failed to get VOD asset")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Build response with business metadata only
	// Status/storageLocation will be enriched by Gateway via Periscope
	asset := &pb.VodAssetInfo{
		Id:           id,
		ArtifactHash: vodHash,
		PlaybackId:   &playbackID,
		Filename:     filename,
		Status:       pb.VodStatus_VOD_STATUS_UPLOADING, // Default - Gateway enriches from Periscope
		CreatedAt:    timestamppb.New(createdAt),
		UpdatedAt:    timestamppb.New(updatedAt),
	}
	if title.Valid {
		asset.Title = title.String
	}
	if description.Valid {
		asset.Description = description.String
	}
	if sizeBytes.Valid {
		size := sizeBytes.Int64
		asset.SizeBytes = &size
	}
	if retentionUntil.Valid {
		asset.ExpiresAt = timestamppb.New(retentionUntil.Time)
	}

	return asset, nil
}

// ListVodAssets returns VOD assets from Commodore business registry with pagination
// Lifecycle data (status, size, storage) comes from Periscope via Gateway's ArtifactLifecycleLoader
func (s *CommodoreServer) ListVodAssets(ctx context.Context, req *pb.ListVodAssetsRequest) (*pb.ListVodAssetsResponse, error) {
	// Get tenant context from metadata
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// Get total count
	var total int32
	countQuery := `SELECT COUNT(*) FROM commodore.vod_assets WHERE tenant_id = $1`
	if countErr := s.db.QueryRowContext(ctx, countQuery, tenantID).Scan(&total); countErr != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", countErr)
	}

	// Build keyset pagination query
	builder := &pagination.KeysetBuilder{
		TimestampColumn: "created_at",
		IDColumn:        "vod_hash",
	}

	// Base query
	query := `
		SELECT id, vod_hash, playback_id, title, description, filename, content_type,
		       size_bytes, retention_until, created_at, updated_at
		FROM commodore.vod_assets
		WHERE tenant_id = $1`
	args := []interface{}{tenantID}
	argIdx := 2

	// Add keyset condition if cursor provided
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		query += " AND " + condition
		args = append(args, cursorArgs...)
	}

	// Add ORDER BY and LIMIT
	query += " " + builder.OrderBy(params)
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var assets []*pb.VodAssetInfo
	for rows.Next() {
		var (
			id                   string
			vodHash              string
			playbackID           string
			title, description   sql.NullString
			filename             string
			contentType          sql.NullString
			sizeBytes            sql.NullInt64
			retentionUntil       sql.NullTime
			createdAt, updatedAt time.Time
		)
		if err := rows.Scan(&id, &vodHash, &playbackID, &title, &description, &filename, &contentType,
			&sizeBytes, &retentionUntil, &createdAt, &updatedAt); err != nil {
			s.logger.WithError(err).Warn("Error scanning VOD asset")
			continue
		}

		// Build asset with business metadata only
		// Status/storageLocation will be enriched by Gateway via Periscope
		asset := &pb.VodAssetInfo{
			Id:           id,
			ArtifactHash: vodHash,
			PlaybackId:   &playbackID,
			Filename:     filename,
			Status:       pb.VodStatus_VOD_STATUS_UPLOADING, // Default - Gateway enriches from Periscope
			CreatedAt:    timestamppb.New(createdAt),
			UpdatedAt:    timestamppb.New(updatedAt),
		}
		if title.Valid {
			asset.Title = title.String
		}
		if description.Valid {
			asset.Description = description.String
		}
		if sizeBytes.Valid {
			size := sizeBytes.Int64
			asset.SizeBytes = &size
		}
		if retentionUntil.Valid {
			asset.ExpiresAt = timestamppb.New(retentionUntil.Time)
		}

		assets = append(assets, asset)
	}

	// Detect hasMore and trim results
	hasMore := len(assets) > params.Limit
	if hasMore {
		assets = assets[:params.Limit]
	}

	// Reverse results if backward pagination
	if params.Direction == pagination.Backward && len(assets) > 0 {
		for i, j := 0, len(assets)-1; i < j; i, j = i+1, j-1 {
			assets[i], assets[j] = assets[j], assets[i]
		}
	}

	// Build cursors from results
	var startCursor, endCursor string
	if len(assets) > 0 {
		first := assets[0]
		last := assets[len(assets)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.ArtifactHash)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.ArtifactHash)
	}

	// Build response
	resp := &pb.ListVodAssetsResponse{
		Assets: assets,
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

// DeleteVodAsset deletes a VOD asset via Foghorn
func (s *CommodoreServer) DeleteVodAsset(ctx context.Context, req *pb.DeleteVodAssetRequest) (*pb.DeleteVodAssetResponse, error) {
	// Get tenant context from metadata
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Look up origin cluster for cluster-aware routing
	var originClusterID sql.NullString
	_ = s.db.QueryRowContext(ctx, `
		SELECT origin_cluster_id FROM commodore.vod_assets
		WHERE vod_hash = $1 AND tenant_id = $2
	`, req.ArtifactHash, tenantID).Scan(&originClusterID)

	foghornClient, err := s.resolveFoghornForArtifact(ctx, tenantID, originClusterID.String)
	if err != nil {
		return nil, err
	}

	// Forward to Foghorn (it handles S3 deletion and lifecycle state)
	resp, trailers, err := foghornClient.DeleteVodAsset(ctx, tenantID, req.ArtifactHash)
	if err != nil {
		s.logger.WithError(err).WithField("artifact_hash", req.ArtifactHash).Error("Failed to delete VOD asset via Foghorn")
		return nil, grpcutil.PropagateError(ctx, err, trailers)
	}

	// Delete from business registry
	_, delErr := s.db.ExecContext(ctx, `
		DELETE FROM commodore.vod_assets
		WHERE vod_hash = $1 AND tenant_id = $2
	`, req.ArtifactHash, tenantID)
	if delErr != nil {
		s.logger.WithError(delErr).WithField("artifact_hash", req.ArtifactHash).Warn("Failed to delete VOD from business registry (will be cleaned up by retention job)")
	}

	// Emit deletion event
	if resp.Success {
		s.emitArtifactEvent(ctx, eventArtifactDeleted, tenantID, userID, pb.ArtifactEvent_ARTIFACT_TYPE_VOD, req.ArtifactHash, "", "deleted", nil)
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":     tenantID,
		"artifact_hash": req.ArtifactHash,
	}).Info("Deleted VOD asset")

	return resp, nil
}

// TerminateTenantStreams stops all active streams for a suspended tenant.
// Called by Purser when prepaid balance drops below -$10.
// Forwards to Foghorn which sends stop_sessions to affected nodes.
func (s *CommodoreServer) TerminateTenantStreams(ctx context.Context, req *pb.TerminateTenantStreamsRequest) (*pb.TerminateTenantStreamsResponse, error) {
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id": req.TenantId,
		"reason":    req.Reason,
	}).Info("Received tenant stream termination request from Purser")

	// Fan out to ALL clusters the tenant has access to
	route, err := s.resolveClusterRouteForTenant(ctx, req.TenantId)
	if err != nil {
		return nil, err
	}

	targets := buildClusterFanoutTargets(route)
	if len(targets) == 0 {
		return nil, status.Errorf(codes.Unavailable, "no foghorn targets for tenant %s", req.TenantId)
	}

	var totalStreams, totalSessions int32
	var allStreamNames []string
	var lastErr error
	failures := 0
	for _, target := range targets {
		client, dialErr := s.foghornPool.GetOrCreate(foghornPoolKey(target.clusterID, target.addr), target.addr)
		if dialErr != nil {
			s.logger.WithError(dialErr).WithField("cluster_id", target.clusterID).Warn("Failed to connect to cluster for tenant termination")
			lastErr = dialErr
			failures++
			continue
		}
		foghornResp, _, callErr := client.TerminateTenantStreams(ctx, req.TenantId, req.Reason)
		if callErr != nil {
			s.logger.WithError(callErr).WithFields(logging.Fields{
				"tenant_id":  req.TenantId,
				"cluster_id": target.clusterID,
			}).Warn("Failed to terminate tenant streams on cluster")
			lastErr = callErr
			failures++
			continue
		}
		totalStreams += foghornResp.StreamsTerminated
		totalSessions += foghornResp.SessionsTerminated
		allStreamNames = append(allStreamNames, foghornResp.StreamNames...)
	}

	if totalStreams == 0 && totalSessions == 0 && lastErr != nil {
		return nil, status.Errorf(codes.Unavailable, "failed to terminate streams on any cluster: %v", lastErr)
	}
	if failures > 0 {
		s.logger.WithError(lastErr).WithFields(logging.Fields{
			"tenant_id":       req.TenantId,
			"clusters_failed": failures,
			"clusters_total":  len(targets),
		}).Warn("Tenant termination partially failed: some clusters unreachable")
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":           req.TenantId,
		"streams_terminated":  totalStreams,
		"sessions_terminated": totalSessions,
		"clusters_contacted":  len(targets),
	}).Info("Tenant streams terminated across all clusters")

	return &pb.TerminateTenantStreamsResponse{
		StreamsTerminated:  totalStreams,
		SessionsTerminated: totalSessions,
		StreamNames:        allStreamNames,
	}, nil
}

// InvalidateTenantCache clears cached suspension status for a tenant (called on reactivation)
func (s *CommodoreServer) InvalidateTenantCache(ctx context.Context, req *pb.InvalidateTenantCacheRequest) (*pb.InvalidateTenantCacheResponse, error) {
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id": req.TenantId,
		"reason":    req.Reason,
	}).Info("Received tenant cache invalidation request from Purser")

	// Fan out to ALL clusters the tenant has access to
	route, err := s.resolveClusterRouteForTenant(ctx, req.TenantId)
	if err != nil {
		return nil, err
	}

	targets := buildClusterFanoutTargets(route)
	if len(targets) == 0 {
		return nil, status.Errorf(codes.Unavailable, "no foghorn targets for tenant %s", req.TenantId)
	}

	var totalInvalidated int32
	var lastErr error
	failures := 0
	for _, target := range targets {
		client, dialErr := s.foghornPool.GetOrCreate(foghornPoolKey(target.clusterID, target.addr), target.addr)
		if dialErr != nil {
			s.logger.WithError(dialErr).WithField("cluster_id", target.clusterID).Warn("Failed to connect to cluster for cache invalidation")
			lastErr = dialErr
			failures++
			continue
		}
		foghornResp, _, callErr := client.InvalidateTenantCache(ctx, req.TenantId, req.Reason)
		if callErr != nil {
			s.logger.WithError(callErr).WithFields(logging.Fields{
				"tenant_id":  req.TenantId,
				"cluster_id": target.clusterID,
			}).Warn("Failed to invalidate tenant cache on cluster")
			lastErr = callErr
			failures++
			continue
		}
		totalInvalidated += foghornResp.EntriesInvalidated
	}

	if totalInvalidated == 0 && lastErr != nil {
		return nil, status.Errorf(codes.Unavailable, "failed to invalidate cache on any cluster: %v", lastErr)
	}
	if failures > 0 {
		s.logger.WithError(lastErr).WithFields(logging.Fields{
			"tenant_id":       req.TenantId,
			"clusters_failed": failures,
			"clusters_total":  len(targets),
		}).Warn("Tenant cache invalidation partially failed: some clusters unreachable")
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":           req.TenantId,
		"entries_invalidated": totalInvalidated,
		"clusters_contacted":  len(targets),
	}).Info("Tenant cache invalidated across all clusters")

	return &pb.InvalidateTenantCacheResponse{
		EntriesInvalidated: totalInvalidated,
	}, nil
}

// ============================================================================
// CROSS-SERVICE: BILLING DATA ACCESS
// Called by Purser to avoid cross-service database access.
// ============================================================================

// GetTenantUserCount returns active and total user counts for a tenant.
// Called by Purser billing job for user-based billing calculations.
func (s *CommodoreServer) GetTenantUserCount(ctx context.Context, req *pb.GetTenantUserCountRequest) (*pb.GetTenantUserCountResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	var activeCount, totalCount int32
	err := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE is_active = true),
			COUNT(*)
		FROM commodore.users
		WHERE tenant_id = $1
	`, tenantID).Scan(&activeCount, &totalCount)

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Error("Failed to get tenant user count")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	return &pb.GetTenantUserCountResponse{
		ActiveCount: activeCount,
		TotalCount:  totalCount,
	}, nil
}

// GetTenantPrimaryUser returns the primary user info for a tenant.
// Called by Purser billing job for billing notifications and invoices.
// Returns the first admin user, or the first user if no admin exists.
func (s *CommodoreServer) GetTenantPrimaryUser(ctx context.Context, req *pb.GetTenantPrimaryUserRequest) (*pb.GetTenantPrimaryUserResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	var userID, email string
	var firstName, lastName sql.NullString

	// Try to find an admin first, then fall back to any user
	err := s.db.QueryRowContext(ctx, `
		SELECT id, email, first_name, last_name
		FROM commodore.users
		WHERE tenant_id = $1 AND is_active = true AND email IS NOT NULL AND email <> ''
		ORDER BY
			CASE WHEN role = 'admin' THEN 0 ELSE 1 END,
			created_at ASC
		LIMIT 1
	`, tenantID).Scan(&userID, &email, &firstName, &lastName)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "no users found for tenant")
	}

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Error("Failed to get tenant primary user")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Build display name
	name := ""
	if firstName.Valid && firstName.String != "" {
		name = firstName.String
	}
	if lastName.Valid && lastName.String != "" {
		if name != "" {
			name += " "
		}
		name += lastName.String
	}

	return &pb.GetTenantPrimaryUserResponse{
		UserId: userID,
		Email:  email,
		Name:   name,
	}, nil
}
