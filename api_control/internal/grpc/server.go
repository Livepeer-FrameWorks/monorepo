package grpc

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"frameworks/api_control/internal/clusterurls"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	decklogclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/listmonk"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/navigator"
	purserclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/purser"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	fieldcrypt "github.com/Livepeer-FrameWorks/monorepo/pkg/crypto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	fwdb "github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
	emailpkg "github.com/Livepeer-FrameWorks/monorepo/pkg/email"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pullsource"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/turnstile"

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

// ServerMetrics holds Prometheus metrics for the gRPC server. Per-method
// request count + duration are captured by GRPCMetricsInterceptor and
// emitted on the GRPCRequests / GRPCDuration vectors below.
type ServerMetrics struct {
	GRPCRequests *prometheus.CounterVec
	GRPCDuration *prometheus.HistogramVec
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
	pb.UnimplementedPlaybackAccessControlServiceServer
	db                   *sql.DB
	dbMaxIdleConns       int
	logger               logging.Logger
	foghornPool          *foghornclient.FoghornPool
	quartermasterClient  *qmclient.GRPCClient
	navigatorClient      *navigator.Client
	purserClient         *purserclient.GRPCClient
	listmonkClient       *listmonk.Client
	decklogClient        *decklogclient.BatchedClient
	defaultMailingListID int
	metrics              *ServerMetrics
	turnstileValidator   *turnstile.Validator
	turnstileFailOpen    bool
	passwordResetSecret  []byte
	fieldEncryptor       *fieldcrypt.FieldEncryptor
	// Separate FieldEncryptor for playback webhook secrets so HKDF purpose
	// isolation prevents cross-feature key reuse.
	playbackWebhookEncryptor *fieldcrypt.FieldEncryptor
	// Separate FieldEncryptor for pull-input source URIs (purpose
	// "pull-source-uri"). Used by ResolvePullSourceByInternalName and the
	// commodore bootstrap reconciler when persisting stream_pull_sources.
	pullSourceEncryptor  *fieldcrypt.FieldEncryptor
	routeCache           map[string]*clusterRoute
	routeCacheMu         sync.RWMutex
	routeCacheTTL        time.Duration
	foghornCandidateMu   sync.Mutex
	foghornCandidateNext map[string]int
	// clusterURLs resolves cluster_id → Chandler base URL from an in-process
	// snapshot refreshed off Quartermaster. Used by list/get handlers to
	// project thumbnailAssets onto Stream/Clip/DVR/VOD rows without per-row
	// network calls.
	clusterURLs *clusterurls.Resolver
}

func (s *CommodoreServer) retryPostgres(ctx context.Context, fn func() error) error {
	return fwdb.RetryPostgresWithHook(ctx, fwdb.DefaultRetryAttempts, 25*time.Millisecond, func(error, int) {
		s.recycleIdlePostgresConns()
	}, fn)
}

func (s *CommodoreServer) recycleIdlePostgresConns() {
	if s.db == nil || s.dbMaxIdleConns < 0 {
		return
	}
	maxIdleConns := s.dbMaxIdleConns
	if maxIdleConns <= 0 {
		maxIdleConns = fwdb.DefaultConfig().MaxIdleConns
	}
	// database/sql has no CloseIdleConnections; dropping the idle limit forces
	// stale Yugabyte catalog-cache connections out before the retry is replayed.
	s.db.SetMaxIdleConns(0)
	s.db.SetMaxIdleConns(maxIdleConns)
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
	foghornAddrsByCluster   map[string][]string
	clusterPeers            []*pb.TenantClusterPeer  // full tenant cluster context (includes per-peer foghorn addrs)
	tenantResourceLimits    *pb.TenantResourceLimits // access-specific cap override; nil = use Purser tier entitlement
	resolvedAt              time.Time
}

type clusterFanoutTarget struct {
	clusterID string
	addr      string
}

const activeIngestClusterFreshnessWindow = 2 * time.Minute

func dedupeAddrs(addrs ...string) []string {
	seen := make(map[string]struct{}, len(addrs))
	out := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}
		out = append(out, addr)
	}
	return out
}

func serviceInstanceAddr(inst *pb.ServiceInstance) string {
	if inst == nil || inst.GetHost() == "" || inst.GetPort() <= 0 {
		return ""
	}
	return net.JoinHostPort(inst.GetHost(), strconv.Itoa(int(inst.GetPort())))
}

func (s *CommodoreServer) discoverFoghornAddrs(ctx context.Context, clusterID string) []string {
	if s.quartermasterClient == nil || clusterID == "" {
		return nil
	}
	resp, err := s.quartermasterClient.DiscoverServices(ctx, "foghorn", clusterID, &pb.CursorPaginationRequest{First: 20})
	if err != nil {
		s.logger.WithError(err).WithField("cluster_id", clusterID).Debug("Foghorn candidate discovery failed")
		return nil
	}
	addrs := make([]string, 0, len(resp.GetInstances()))
	for _, inst := range resp.GetInstances() {
		if inst.GetStatus() != "running" && inst.GetStatus() != "active" {
			continue
		}
		if health := inst.GetHealthStatus(); health != "" && health != "healthy" {
			continue
		}
		if addr := serviceInstanceAddr(inst); addr != "" {
			addrs = append(addrs, addr)
		}
	}
	return dedupeAddrs(addrs...)
}

func (s *CommodoreServer) nextFoghornAddr(clusterID string, candidates []string) string {
	candidates = dedupeAddrs(candidates...)
	if len(candidates) == 0 {
		return ""
	}
	if len(candidates) == 1 || clusterID == "" {
		return candidates[0]
	}
	s.foghornCandidateMu.Lock()
	idx := s.foghornCandidateNext[clusterID] % len(candidates)
	s.foghornCandidateNext[clusterID] = idx + 1
	s.foghornCandidateMu.Unlock()
	return candidates[idx]
}

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
	for _, addr := range route.foghornAddrsByCluster[route.clusterID] {
		addTarget(route.clusterID, addr)
	}
	if route.officialClusterID != route.clusterID {
		addTarget(route.officialClusterID, route.officialFoghornGrpcAddr)
		for _, addr := range route.foghornAddrsByCluster[route.officialClusterID] {
			addTarget(route.officialClusterID, addr)
		}
	}
	for _, peer := range route.clusterPeers {
		addTarget(peer.ClusterId, peer.FoghornGrpcAddr)
		for _, addr := range route.foghornAddrsByCluster[peer.ClusterId] {
			addTarget(peer.ClusterId, addr)
		}
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

func foghornCandidatesFromRoute(route *clusterRoute, clusterID string) []string {
	if route == nil || clusterID == "" {
		return nil
	}
	candidates := make([]string, 0, 4)
	if route.clusterID == clusterID {
		candidates = append(candidates, route.foghornAddr)
	}
	if route.officialClusterID == clusterID {
		candidates = append(candidates, route.officialFoghornGrpcAddr)
	}
	for _, peer := range route.clusterPeers {
		if peer.GetClusterId() == clusterID {
			candidates = append(candidates, peer.GetFoghornGrpcAddr())
		}
	}
	candidates = append(candidates, route.foghornAddrsByCluster[clusterID]...)
	return dedupeAddrs(candidates...)
}

func routeClusterIDs(route *clusterRoute) []string {
	if route == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var ids []string
	add := func(id string) {
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	add(route.clusterID)
	add(route.officialClusterID)
	for _, peer := range route.clusterPeers {
		add(peer.GetClusterId())
	}
	return ids
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
	DBMaxIdleConns       int
	Logger               logging.Logger
	FoghornPool          *foghornclient.FoghornPool
	QuartermasterClient  *qmclient.GRPCClient
	NavigatorClient      *navigator.Client
	PurserClient         *purserclient.GRPCClient
	ListmonkClient       *listmonk.Client
	DecklogClient        *decklogclient.BatchedClient
	ClusterURLs          *clusterurls.Resolver
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
	CertFile            string
	KeyFile             string
	AllowInsecure       bool
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
	// Separate purpose for playback webhook secrets so HKDF key isolation
	// prevents cross-feature key reuse if one purpose is ever compromised.
	pwe, err := fieldcrypt.DeriveFieldEncryptor(cfg.JWTSecret, "playback-webhook-secret")
	if err != nil {
		cfg.Logger.WithError(err).Fatal("Failed to derive playback webhook field encryption key")
	}
	// Separate purpose for pull-input source URIs (HKDF isolation as above).
	// Bootstrap reconciler must derive with the SAME purpose string.
	pse, err := fieldcrypt.DeriveFieldEncryptor(cfg.JWTSecret, "pull-source-uri")
	if err != nil {
		cfg.Logger.WithError(err).Fatal("Failed to derive pull source URI field encryption key")
	}

	return &CommodoreServer{
		db:                       cfg.DB,
		dbMaxIdleConns:           cfg.DBMaxIdleConns,
		logger:                   cfg.Logger,
		foghornPool:              cfg.FoghornPool,
		quartermasterClient:      cfg.QuartermasterClient,
		navigatorClient:          cfg.NavigatorClient,
		purserClient:             cfg.PurserClient,
		listmonkClient:           cfg.ListmonkClient,
		decklogClient:            cfg.DecklogClient,
		clusterURLs:              cfg.ClusterURLs,
		defaultMailingListID:     cfg.DefaultMailingListID,
		metrics:                  cfg.Metrics,
		turnstileValidator:       tv,
		turnstileFailOpen:        cfg.TurnstileFailOpen,
		passwordResetSecret:      cfg.PasswordResetSecret,
		fieldEncryptor:           fe,
		playbackWebhookEncryptor: pwe,
		pullSourceEncryptor:      pse,
		routeCache:               make(map[string]*clusterRoute),
		routeCacheTTL:            5 * time.Minute,
		foghornCandidateNext:     make(map[string]int),
	}
}

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

	allowedClasses := s.allowedClusterClassesForTenant(ctx, tenantID)
	filteredPeers := filterPeersByPolicy(resp.GetClusterPeers(), allowedClasses)

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
		foghornAddrsByCluster:   make(map[string][]string),
		clusterPeers:            filteredPeers,
		tenantResourceLimits:    resp.GetTenantResourceLimits(),
		resolvedAt:              time.Now(),
	}
	for _, cid := range routeClusterIDs(route) {
		discovered := s.discoverFoghornAddrs(ctx, cid)
		route.foghornAddrsByCluster[cid] = dedupeAddrs(append(foghornCandidatesFromRoute(route, cid), discovered...)...)
	}
	normalizeClusterRoute(route)

	s.routeCacheMu.Lock()
	s.routeCache[tenantID] = route
	s.routeCacheMu.Unlock()

	return route, nil
}

func (s *CommodoreServer) allowedClusterClassesForTenant(ctx context.Context, tenantID string) map[string]struct{} {
	free := map[string]struct{}{"platform_official": {}}
	if s.purserClient == nil {
		return free
	}
	subResp, err := s.purserClient.GetSubscription(ctx, tenantID)
	if err != nil || subResp == nil || subResp.GetSubscription() == nil {
		return free
	}
	tier, err := s.purserClient.GetBillingTier(ctx, subResp.GetSubscription().GetTierId())
	if err != nil || tier == nil {
		return free
	}
	out := map[string]struct{}{"platform_official": {}}
	switch level := tier.GetTierLevel(); {
	case level >= 4:
		out["third_party_marketplace"] = struct{}{}
		out["tenant_private"] = struct{}{}
	case level >= 2:
		out["third_party_marketplace"] = struct{}{}
	}
	return out
}

func findPeerByClusterID(peers []*pb.TenantClusterPeer, clusterID string) *pb.TenantClusterPeer {
	for _, p := range peers {
		if p != nil && p.GetClusterId() == clusterID {
			return p
		}
	}
	return nil
}

func streamOriginRegionForRoute(route *clusterRoute, activeClusterID string) string {
	if route == nil {
		return ""
	}
	clusterID := strings.TrimSpace(activeClusterID)
	if clusterID == "" {
		clusterID = route.clusterID
	}
	if clusterID == "" {
		return ""
	}
	if peer := findPeerByClusterID(route.clusterPeers, clusterID); peer != nil {
		return peer.GetRegionId()
	}
	return ""
}

func filterPeersByPolicy(peers []*pb.TenantClusterPeer, allowedClasses map[string]struct{}) []*pb.TenantClusterPeer {
	if len(peers) == 0 {
		return peers
	}
	out := make([]*pb.TenantClusterPeer, 0, len(peers))
	for _, peer := range peers {
		if peer == nil {
			continue
		}
		class := peer.GetClusterClass()
		if class != "" && !isSelfHostedPeer(peer) {
			if _, ok := allowedClasses[class]; !ok {
				continue
			}
		}
		switch peer.GetHealthStatus() {
		case "offline", "degraded":
			continue
		}
		out = append(out, peer)
	}
	return out
}

func isSelfHostedPeer(peer *pb.TenantClusterPeer) bool {
	if peer == nil {
		return false
	}
	return strings.EqualFold(peer.GetClusterType(), "self-hosted")
}

const (
	processLifecycleLive        = "live"
	processLifecycleDVR         = "dvr"
	processLifecycleClip        = "clip"
	processLifecycleDVRFinalize = "dvr_finalize"
	processLifecycleVOD         = "vod"
)

func normalizeProcessLifecycle(lifecycle string) string {
	switch strings.TrimSpace(strings.ToLower(lifecycle)) {
	case processLifecycleLive:
		return processLifecycleLive
	case processLifecycleDVR:
		return processLifecycleDVR
	case processLifecycleClip:
		return processLifecycleClip
	case processLifecycleDVRFinalize:
		return processLifecycleDVRFinalize
	case processLifecycleVOD:
		return processLifecycleVOD
	default:
		return processLifecycleLive
	}
}

func validProcessLifecycle(lifecycle string) bool {
	switch strings.TrimSpace(strings.ToLower(lifecycle)) {
	case processLifecycleLive, processLifecycleDVR, processLifecycleClip, processLifecycleDVRFinalize, processLifecycleVOD:
		return true
	default:
		return false
	}
}

func processConfigColumn(lifecycle string) string {
	switch normalizeProcessLifecycle(lifecycle) {
	case processLifecycleDVR:
		return "processes_dvr"
	case processLifecycleClip:
		return "processes_clip"
	case processLifecycleDVRFinalize:
		return "processes_dvr_finalize"
	case processLifecycleVOD:
		return "processes_vod"
	default:
		return "processes_live"
	}
}

func tierProcessesForLifecycle(tier *pb.BillingTier, lifecycle string) string {
	switch normalizeProcessLifecycle(lifecycle) {
	case processLifecycleDVR:
		return tier.GetProcessesDvr()
	case processLifecycleClip:
		return tier.GetProcessesClip()
	case processLifecycleDVRFinalize:
		return tier.GetProcessesDvrFinalize()
	case processLifecycleVOD:
		return tier.GetProcessesVod()
	default:
		return tier.GetProcessesLive()
	}
}

// resolveProcessesJSON returns the MistServer process config JSON for a
// lifecycle. Resolution order: per-stream override → tenant override (if tier
// allows) → tier default. streamID may be empty for tenant-scoped lookups.
func (s *CommodoreServer) resolveProcessesJSON(ctx context.Context, tenantID, streamID, clusterID, lifecycle string) string {
	if s.purserClient == nil {
		return "[]"
	}
	lifecycle = normalizeProcessLifecycle(lifecycle)

	// Get tenant's subscription → tier
	subResp, err := s.purserClient.GetSubscription(ctx, tenantID)
	if err != nil {
		s.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to get subscription for process config")
		return "[]"
	}
	sub := subResp.GetSubscription()
	if sub == nil {
		return "[]"
	}

	tier, err := s.purserClient.GetBillingTier(ctx, sub.GetTierId())
	if err != nil {
		s.logger.WithError(err).WithField("tier_id", sub.GetTierId()).Warn("Failed to get billing tier for process config")
		return "[]"
	}

	processesJSON := tierProcessesForLifecycle(tier, lifecycle)

	// Per-stream override always wins: operator-supplied bootstrap policy
	// (stream_processing_config) must not be silently dropped by a tier
	// flag, otherwise an operator-owned mist_native stream's thumbnails-only
	// policy can be ignored when the system tenant tier isn't marked
	// customizable. Tenant-wide override (tenant_processing_config) stays
	// gated on tier.processing_customizable so paid-tier features cannot be
	// opted into by tenants on a locked tier.
	// Overrides are validated at the read boundary too, not only on write: a
	// stale/manually-edited/migrated row could hold a config that bypasses
	// encodeProcessPolicy (e.g. a Livepeer process with no usable
	// target_profiles). An invalid override is skipped so the next source (tenant
	// override, then the catalog tier default) applies, rather than serving a bad
	// config straight to MistServer.
	validOverride := func(override string) bool {
		if override == "" {
			return false
		}
		if err := mist.ValidateProcessConfigShape(override); err != nil {
			s.logger.WithError(err).Warn("Ignoring invalid persisted process override; falling back to next source")
			return false
		}
		return true
	}
	if streamID != "" {
		if override := s.getStreamProcessingOverride(ctx, streamID, lifecycle); validOverride(override) {
			processesJSON = override
		} else if tier.GetFeatures().GetProcessingCustomizable() {
			if tenantOverride := s.getTenantProcessingOverride(ctx, tenantID, lifecycle); validOverride(tenantOverride) {
				processesJSON = tenantOverride
			}
		}
	} else if tier.GetFeatures().GetProcessingCustomizable() {
		if override := s.getTenantProcessingOverride(ctx, tenantID, lifecycle); validOverride(override) {
			processesJSON = override
		}
	}

	if processesJSON == "" || processesJSON == "[]" {
		return "[]"
	}

	// Livepeer entries carry no hardcoded_broadcasters — Foghorn fills the
	// broadcaster list from its cluster's Livepeer gateway instances at
	// cache/dispatch time.
	return mist.NormalizeProcessConfigSelectors(processesJSON)
}

// getStreamProcessingOverride checks commodore.stream_processing_config for a
// per-stream override. Returns "" when no row exists or the column is NULL.
func (s *CommodoreServer) getStreamProcessingOverride(ctx context.Context, streamID, lifecycle string) string {
	col := processConfigColumn(lifecycle)
	var override sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT `+col+` FROM commodore.stream_processing_config WHERE stream_id = $1`,
		streamID,
	).Scan(&override)
	if err != nil || !override.Valid {
		return ""
	}
	return override.String
}

// getTenantProcessingOverride checks commodore.tenant_processing_config for a tenant override.
func (s *CommodoreServer) getTenantProcessingOverride(ctx context.Context, tenantID, lifecycle string) string {
	col := processConfigColumn(lifecycle)
	var override sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT `+col+` FROM commodore.tenant_processing_config WHERE tenant_id = $1`,
		tenantID,
	).Scan(&override)
	if err != nil || !override.Valid {
		return ""
	}
	return override.String
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

		candidates := foghornCandidatesFromRoute(route, route.clusterID)
		addr := s.nextFoghornAddr(route.clusterID, candidates)
		if addr == "" {
			return nil, route, status.Errorf(codes.Unavailable, "no foghorn registered for cluster %s", route.clusterID)
		}

		client, err := s.foghornPool.GetOrCreate(foghornPoolKey(route.clusterID, addr), addr)
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

	candidates := foghornCandidatesFromRoute(route, clusterID)
	addr := s.nextFoghornAddr(clusterID, candidates)
	if addr == "" {
		// Evict cache and retry once — Foghorn may have been assigned since last fill
		s.routeCacheMu.Lock()
		delete(s.routeCache, tenantID)
		s.routeCacheMu.Unlock()

		route, err = s.resolveClusterRouteForTenant(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		candidates = foghornCandidatesFromRoute(route, clusterID)
		addr = s.nextFoghornAddr(clusterID, candidates)
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
		var found bool
		found, tenantID, activeClusterID, err = s.resolveArtifactRouteForContent(ctx, contentID)
		if !found && err == nil {
			err = sql.ErrNoRows
		}
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
		if tenantID.Valid && tenantID.String != "" {
			if client, clusterErr := s.resolveFoghornForCluster(ctx, activeClusterID.String, tenantID.String); clusterErr == nil {
				return client, &clusterRoute{clusterID: activeClusterID.String}, nil
			}
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
		if tenantID.Valid && tenantID.String != "" {
			if client, clusterErr := s.resolveFoghornForCluster(ctx, activeClusterID.String, tenantID.String); clusterErr == nil {
				return client, &clusterRoute{clusterID: activeClusterID.String}, nil
			}
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

func (s *CommodoreServer) resolveArtifactRouteForContent(ctx context.Context, contentID string) (bool, sql.NullString, sql.NullString, error) {
	var tenantID, clusterID sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT tenant_id, cluster_id
		  FROM (
			SELECT tenant_id,
			       COALESCE(NULLIF(storage_cluster_id, ''), NULLIF(origin_cluster_id, '')) AS cluster_id
			  FROM commodore.clips
			 WHERE playback_id = $1 OR clip_hash = $1
			UNION ALL
			SELECT tenant_id,
			       COALESCE(NULLIF(storage_cluster_id, ''), NULLIF(origin_cluster_id, '')) AS cluster_id
			  FROM commodore.vod_assets
			 WHERE playback_id = $1 OR vod_hash = $1
			UNION ALL
			SELECT tenant_id,
			       COALESCE(NULLIF(storage_cluster_id, ''), NULLIF(origin_cluster_id, '')) AS cluster_id
			  FROM commodore.dvr_recordings
			 WHERE playback_id = $1 OR dvr_hash = $1
			UNION ALL
			SELECT cp.tenant_id,
			       COALESCE(NULLIF(va.storage_cluster_id, ''), NULLIF(va.origin_cluster_id, '')) AS cluster_id
			  FROM commodore.dvr_chapter_playback cp
			  LEFT JOIN commodore.vod_assets va ON va.vod_hash = cp.artifact_hash
			 WHERE cp.playback_id = $1
		  ) resolved
		 LIMIT 1
	`, contentID).Scan(&tenantID, &clusterID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, tenantID, clusterID, nil
	}
	if err != nil {
		return false, tenantID, clusterID, status.Errorf(codes.Internal, "database error resolving artifact content: %v", err)
	}
	return true, tenantID, clusterID, nil
}

func clusterInPeers(peers []*pb.TenantClusterPeer, clusterID string) bool {
	for _, p := range peers {
		if p.ClusterId == clusterID {
			return true
		}
	}
	return false
}

func canOwnLiveIngest(clusterType string) bool {
	switch strings.ToLower(strings.TrimSpace(clusterType)) {
	case "", "edge", "media", "selfhosted", "self-hosted":
		return true
	default:
		return false
	}
}

func resolveLiveIngestClusterID(route *clusterRoute, requestedClusterID string) string {
	if route == nil {
		return requestedClusterID
	}

	resolvedClusterID := route.clusterID
	if requestedClusterID == "" {
		return resolvedClusterID
	}
	if requestedClusterID == route.clusterID || requestedClusterID == route.officialClusterID {
		return requestedClusterID
	}
	for _, peer := range route.clusterPeers {
		if peer.GetClusterId() == requestedClusterID && canOwnLiveIngest(peer.GetClusterType()) {
			return requestedClusterID
		}
	}
	return resolvedClusterID
}

func hasTenantResourceLimits(limits *pb.TenantResourceLimits) bool {
	return limits != nil && (limits.GetMaxStreams() > 0 || limits.GetMaxViewers() > 0)
}

func mergeTenantResourceLimits(base, override *pb.TenantResourceLimits) *pb.TenantResourceLimits {
	if !hasTenantResourceLimits(base) {
		if hasTenantResourceLimits(override) {
			return override
		}
		return nil
	}
	merged := &pb.TenantResourceLimits{
		MaxStreams: base.GetMaxStreams(),
		MaxViewers: base.GetMaxViewers(),
	}
	if override.GetMaxStreams() > 0 {
		merged.MaxStreams = override.GetMaxStreams()
	}
	if override.GetMaxViewers() > 0 {
		merged.MaxViewers = override.GetMaxViewers()
	}
	return merged
}

// resolveFoghornForArtifact returns a Foghorn client routed to the artifact's
// origin cluster, or the tenant's primary cluster when the origin is unknown.
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
	var streamID, userID, tenantID, internalName, playbackID, ingestMode string
	var isActive, isRecordingEnabled bool

	err := s.db.QueryRowContext(ctx, `
		SELECT
			s.id, s.user_id, s.tenant_id, s.internal_name,
			u.is_active, s.is_recording_enabled, s.playback_id, s.ingest_mode
		FROM commodore.streams s
		JOIN commodore.users u ON s.user_id = u.id
		WHERE s.stream_key = $1
	`, streamKey).Scan(&streamID, &userID, &tenantID, &internalName, &isActive, &isRecordingEnabled, &playbackID, &ingestMode)

	if errors.Is(err, sql.ErrNoRows) {
		return &pb.ValidateStreamKeyResponse{
			Valid:           false,
			Error:           "Invalid stream key",
			RejectionReason: pb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_INVALID_KEY,
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
			Valid:           false,
			Error:           "User account is inactive",
			RejectionReason: pb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_USER_INACTIVE,
		}, nil
	}
	if ingestMode == "pull" {
		return &pb.ValidateStreamKeyResponse{
			Valid:           false,
			Error:           "Pull streams do not accept push ingest",
			RejectionReason: pb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_PULL_MODE,
		}, nil
	}

	// Plan-aware cluster admission. The route here is already filtered by
	// allowedClusterClassesForTenant against the peer's cluster_class
	// metadata and excludes degraded/offline peers. Confirm the requested
	// ingest cluster is in the filtered set and reject with a structured
	// reason otherwise. Quartermaster dial failures fall through so a
	// transient route-lookup failure doesn't block ingest; cluster_id is
	// still recorded for placement.
	requestedClusterID := strings.TrimSpace(req.GetClusterId())
	if requestedClusterID != "" {
		if route, routeErr := s.resolveClusterRouteForTenant(ctx, tenantID); routeErr == nil {
			peer := findPeerByClusterID(route.clusterPeers, requestedClusterID)
			if peer == nil {
				s.logger.WithFields(logging.Fields{
					"tenant_id":  tenantID,
					"cluster_id": requestedClusterID,
				}).Warn("ValidateStreamKey rejected: cluster not entitled or filtered by plan policy")
				return &pb.ValidateStreamKeyResponse{
					Valid:           false,
					Error:           "Tenant not entitled to ingest cluster " + requestedClusterID,
					RejectionReason: pb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_NOT_ENTITLED,
				}, nil
			}
			if status := peer.GetHealthStatus(); status == "offline" || status == "degraded" {
				return &pb.ValidateStreamKeyResponse{
					Valid:           false,
					Error:           "Ingest cluster " + requestedClusterID + " is " + status,
					RejectionReason: pb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_UNHEALTHY,
				}, nil
			}
		}
	}

	// Get billing status via Purser gRPC (not direct DB access)
	billingModel := "postpaid"
	var isSuspended, isBalanceNegative bool
	var dvrPolicy *pb.DVRPolicy
	var allowances []*pb.MeterAllowance
	var tenantResourceLimits *pb.TenantResourceLimits

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
			dvrPolicy = billingStatus.DvrPolicy
			allowances = billingStatus.Allowances
			tenantResourceLimits = billingStatus.GetTenantResourceLimits()
		}
	}

	resp := &pb.ValidateStreamKeyResponse{
		Valid:                true,
		UserId:               userID,
		TenantId:             tenantID,
		InternalName:         internalName,
		IsRecordingEnabled:   isRecordingEnabled,
		StreamId:             streamID,
		BillingModel:         billingModel,
		IsSuspended:          isSuspended,
		IsBalanceNegative:    isBalanceNegative,
		PlaybackId:           playbackID,
		DvrPolicy:            dvrPolicy,
		Allowances:           allowances,
		TenantResourceLimits: tenantResourceLimits,
	}

	if route, err := s.resolveClusterRouteForTenant(ctx, tenantID); err == nil {
		resolvedOriginClusterID := resolveLiveIngestClusterID(route, req.GetClusterId())
		resp.OriginClusterId = &resolvedOriginClusterID
		if route.officialClusterID != "" {
			resp.OfficialClusterId = &route.officialClusterID
		}
		resp.ClusterPeers = route.clusterPeers
		if hasTenantResourceLimits(route.tenantResourceLimits) {
			resp.TenantResourceLimits = mergeTenantResourceLimits(resp.TenantResourceLimits, route.tenantResourceLimits)
		}
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

	// Resolve MistServer process config from tenant's billing tier
	processClusterID := ""
	if resp.OriginClusterId != nil {
		processClusterID = *resp.OriginClusterId
	}
	resp.ProcessesJson = s.resolveProcessesJSON(ctx, tenantID, streamID, processClusterID, "live")
	resp.DvrProcessesJson = s.resolveProcessesJSON(ctx, tenantID, streamID, processClusterID, "dvr")

	// Track the media cluster this stream ingests on.
	activeIngestClusterID := req.GetClusterId()
	if originClusterID := resp.GetOriginClusterId(); originClusterID != "" {
		activeIngestClusterID = originClusterID
	}
	if activeIngestClusterID != "" {
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
		`, activeIngestClusterID, streamKey)
		if updateErr != nil {
			s.logger.WithError(updateErr).WithField("stream_key", streamKey).Warn("Failed to record ingest cluster")
		} else if rows, rowsErr := res.RowsAffected(); rowsErr == nil && rows == 0 {
			// Concurrent-claim guard: a fresh lease exists held by some
			// cluster. If it's not the cluster trying to ingest now, reject
			// the claim — single-active-ingest per stream is a hard
			// invariant. Belt-and-suspenders to the PeerChannel
			// StreamAdvertisement broadcast (which surfaces the same fact
			// at federation cadence ~10s; this gate fires synchronously at
			// admission time).
			var heldCluster sql.NullString
			if scanErr := s.db.QueryRowContext(ctx, `
				SELECT active_ingest_cluster_id
				FROM commodore.streams
				WHERE stream_key = $1
			`, streamKey).Scan(&heldCluster); scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
				s.logger.WithError(scanErr).WithField("stream_key", streamKey).Warn("ValidateStreamKey: active-ingest lookup failed")
			}
			if heldCluster.Valid && heldCluster.String != "" && heldCluster.String != activeIngestClusterID {
				s.logger.WithFields(logging.Fields{
					"stream_key":            streamKey,
					"requesting_cluster_id": activeIngestClusterID,
					"active_ingest_cluster": heldCluster.String,
				}).Warn("ValidateStreamKey rejected: duplicate ingest claim against fresh lease on another cluster")
				return &pb.ValidateStreamKeyResponse{
					Valid:           false,
					Error:           "Stream is already ingesting on cluster " + heldCluster.String,
					RejectionReason: pb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_DUPLICATE_INGEST,
				}, nil
			}
			s.logger.WithFields(logging.Fields{
				"stream_key":        streamKey,
				"ingest_cluster_id": activeIngestClusterID,
			}).Debug("Skipped ingest cluster update due to active lease (same cluster)")
		}
	}

	return resp, nil
}

// ResolveStreamContext returns the materialization fact set as ValidateStreamKey
// but is keyed by stream identifier (stream_id / playback_id / internal_name)
// rather than stream key. Used by Foghorn's managed-stream reconciler for
// ingest modes that bypass PUSH_REWRITE — notably mist_native — so the same
// cache writes happen without a stream key.
//
// Admission semantics: `admitted` rolls user-active, cluster entitlement,
// suspension, and negative-balance into a single boolean. Free-tier-load and
// per-tenant-cap are NOT enforced here (they live in Foghorn's PUSH_REWRITE
// path); the facts needed to layer those checks caller-side are returned in
// the response. Today this RPC is only invoked for operator/system-tenant
// managed streams (see cli/pkg/bootstrap/render.go: mistNativeStreamToRendered
// rejects non-system tenants), so the missing caller-side gates do not
// affect customer billing. Widening managed-stream ownership to tenants
// requires implementing those gates before relaxing the render-layer
// constraint.
func (s *CommodoreServer) ResolveStreamContext(ctx context.Context, req *pb.ResolveStreamContextRequest) (*pb.ResolveStreamContextResponse, error) {
	var streamID, userID, tenantID, internalName, playbackID, ingestMode string
	var isActive, isRecordingEnabled bool

	const baseSelect = `
		SELECT s.id, s.user_id, s.tenant_id, s.internal_name,
		       u.is_active, s.is_recording_enabled, s.playback_id, s.ingest_mode
		FROM commodore.streams s
		JOIN commodore.users u ON s.user_id = u.id
		WHERE `

	var (
		query string
		arg   string
		field string
	)
	switch id := req.GetIdentifier().(type) {
	case *pb.ResolveStreamContextRequest_StreamId:
		field, arg = "stream_id", id.StreamId
		query = baseSelect + "s.id = $1"
	case *pb.ResolveStreamContextRequest_PlaybackId:
		field, arg = "playback_id", id.PlaybackId
		query = baseSelect + "s.playback_id = $1"
	case *pb.ResolveStreamContextRequest_InternalName:
		field, arg = "internal_name", id.InternalName
		query = baseSelect + "s.internal_name = $1"
	default:
		return nil, status.Error(codes.InvalidArgument, "identifier required (stream_id | playback_id | internal_name)")
	}
	if arg == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s must be non-empty", field)
	}

	err := s.db.QueryRowContext(ctx, query, arg).Scan(
		&streamID, &userID, &tenantID, &internalName,
		&isActive, &isRecordingEnabled, &playbackID, &ingestMode,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return &pb.ResolveStreamContextResponse{
			Admitted:        false,
			AdmissionReason: "stream not found",
			RejectionReason: pb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_INVALID_KEY,
		}, nil
	}
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"identifier_field": field,
			"identifier_value": arg,
			"error":            err,
		}).Error("Database error resolving stream context")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	resp := &pb.ResolveStreamContextResponse{
		StreamId:           streamID,
		PlaybackId:         playbackID,
		InternalName:       internalName,
		IngestMode:         ingestMode,
		TenantId:           tenantID,
		UserId:             userID,
		IsRecordingEnabled: isRecordingEnabled,
	}

	if !isActive {
		resp.Admitted = false
		resp.AdmissionReason = "User account is inactive"
		resp.RejectionReason = pb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_USER_INACTIVE
		return resp, nil
	}

	// Cluster admission (only if caller supplied cluster_id — mist_native streams
	// without allowed_cluster_ids skip this gate and rely on placement scoping).
	// Fail-closed-as-transient: when a cluster_id is supplied the route MUST
	// resolve so entitlement and health can be checked. A Quartermaster blip
	// returns codes.Unavailable, which the caller (Foghorn's managed-stream
	// reconciler) treats as transient — preserve existing applied state, do
	// not newly Apply onto an unverified cluster.
	requestedClusterID := strings.TrimSpace(req.GetClusterId())
	if requestedClusterID != "" {
		route, routeErr := s.resolveClusterRouteForTenant(ctx, tenantID)
		if routeErr != nil {
			s.logger.WithError(routeErr).WithFields(logging.Fields{
				"tenant_id":  tenantID,
				"cluster_id": requestedClusterID,
			}).Warn("ResolveStreamContext: cluster route lookup failed; failing closed as transient")
			return nil, status.Errorf(codes.Unavailable, "cluster route lookup failed: %v", routeErr)
		}
		peer := findPeerByClusterID(route.clusterPeers, requestedClusterID)
		if peer == nil {
			resp.Admitted = false
			resp.AdmissionReason = "Tenant not entitled to cluster " + requestedClusterID
			resp.RejectionReason = pb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_NOT_ENTITLED
			return resp, nil
		}
		if peerStatus := peer.GetHealthStatus(); peerStatus == "offline" || peerStatus == "degraded" {
			resp.Admitted = false
			resp.AdmissionReason = "Cluster " + requestedClusterID + " is " + peerStatus
			resp.RejectionReason = pb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_UNHEALTHY
			return resp, nil
		}
	}

	// Billing status: same Purser call ValidateStreamKey makes, but
	// fail-closed-as-transient instead of defaulting to postpaid/active.
	// ValidateStreamKey can tolerate Purser absence/blips because
	// PUSH_REWRITE admits on every encoder reconnect and re-evaluates; the
	// managed-stream reconciler runs every 30s on always-on streams and
	// must NOT keep a suspended/negative-balance tenant's stream alive
	// just because Purser was momentarily unreachable or wasn't wired up.
	// codes.Unavailable here maps to materializeTransient at the caller,
	// preserving any previously-applied state without committing fresh
	// state on unverified billing.
	if s.purserClient == nil {
		s.logger.WithField("tenant_id", tenantID).
			Warn("ResolveStreamContext: purser client not configured; failing closed as transient")
		return nil, status.Error(codes.Unavailable, "billing status: purser client not configured")
	}
	billingStatus, err := s.purserClient.GetTenantBillingStatus(ctx, tenantID)
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Warn("ResolveStreamContext: billing status lookup failed; failing closed as transient")
		return nil, status.Errorf(codes.Unavailable, "billing status lookup failed: %v", err)
	}
	resp.BillingModel = billingStatus.BillingModel
	resp.IsSuspended = billingStatus.IsSuspended
	resp.IsBalanceNegative = billingStatus.IsBalanceNegative
	resp.DvrPolicy = billingStatus.DvrPolicy
	resp.Allowances = billingStatus.Allowances
	resp.TenantResourceLimits = billingStatus.GetTenantResourceLimits()

	// Routing fields (origin/official cluster, peers, resource-limit merge) —
	// same shape ValidateStreamKey returns.
	if route, err := s.resolveClusterRouteForTenant(ctx, tenantID); err == nil {
		resolvedOriginClusterID := resolveLiveIngestClusterID(route, requestedClusterID)
		resp.OriginClusterId = &resolvedOriginClusterID
		if route.officialClusterID != "" {
			resp.OfficialClusterId = &route.officialClusterID
		}
		resp.ClusterPeers = route.clusterPeers
		if hasTenantResourceLimits(route.tenantResourceLimits) {
			resp.TenantResourceLimits = mergeTenantResourceLimits(resp.TenantResourceLimits, route.tenantResourceLimits)
		}
	}

	// Processes JSON via the same tier/override resolution path ValidateStreamKey
	// uses. Stream type is "live" for mist_native (it serves a live manifest).
	processClusterID := ""
	if resp.OriginClusterId != nil {
		processClusterID = *resp.OriginClusterId
	}
	resp.ProcessesJson = s.resolveProcessesJSON(ctx, tenantID, streamID, processClusterID, "live")
	resp.DvrProcessesJson = s.resolveProcessesJSON(ctx, tenantID, streamID, processClusterID, "dvr")

	// Final admission decision: facts above were collected; now collapse the
	// billing gates that PUSH_REWRITE applies (lines 1092-1110 in
	// triggers/processor.go) into the admitted boolean. Free-tier-load and
	// per-tenant-cap remain caller-side because they require Foghorn's local
	// capacity state.
	switch {
	case resp.IsSuspended:
		resp.Admitted = false
		resp.AdmissionReason = "Tenant is suspended"
		resp.RejectionReason = pb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_TENANT_SUSPENDED
	case resp.IsBalanceNegative:
		resp.Admitted = false
		resp.AdmissionReason = "Tenant balance is negative"
		resp.RejectionReason = pb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_BALANCE_NEGATIVE
	default:
		resp.Admitted = true
	}

	return resp, nil
}

// ListManagedStreams returns every mist_native always_on stream whose single
// allowed source cluster includes the requested cluster_id. Foghorn's managed-
// stream reconciler calls this each tick to build desired state; per-stream
// admission/cache writes then go through ResolveStreamContext. Stable ordering
// by stream_id keeps reconciler diffs deterministic across calls.
func (s *CommodoreServer) ListManagedStreams(ctx context.Context, req *pb.ListManagedStreamsRequest) (*pb.ListManagedStreamsResponse, error) {
	clusterID := strings.TrimSpace(req.GetClusterId())
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}

	// allowed_cluster_ids is stored as an array for pull-stream symmetry, but
	// mist_native currently allows one source cluster. A row is eligible when
	// the requested cluster_id is that source cluster.
	const querySQL = `
		SELECT s.id::text,
		       s.playback_id,
		       s.internal_name,
		       s.tenant_id::text,
		       s.ingest_mode,
		       mn.source_spec,
		       mn.source_kind,
		       s.always_on,
		       mn.placement_count,
		       COALESCE(mn.allowed_cluster_ids, '{}')
		FROM commodore.streams s
		JOIN commodore.stream_mist_sources mn ON mn.stream_id = s.id
		WHERE s.ingest_mode = 'mist_native'
		  AND s.always_on   = TRUE
		  AND $1 = ANY(mn.allowed_cluster_ids)
		ORDER BY s.id::text`

	rows, err := s.db.QueryContext(ctx, querySQL, clusterID)
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"cluster_id": clusterID,
			"error":      err,
		}).Error("Database error listing managed streams")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	resp := &pb.ListManagedStreamsResponse{}
	for rows.Next() {
		var (
			row        pb.ManagedStreamRow
			placement  sql.NullInt32
			allowedArr pq.StringArray
		)
		if scanErr := rows.Scan(
			&row.StreamId, &row.PlaybackId, &row.InternalName, &row.TenantId,
			&row.IngestMode, &row.SourceSpec, &row.SourceKind, &row.AlwaysOn,
			&placement, &allowedArr,
		); scanErr != nil {
			s.logger.WithError(scanErr).Warn("Failed to scan managed stream row")
			continue
		}
		row.PlacementCount = 1
		if placement.Valid && placement.Int32 > 0 {
			row.PlacementCount = placement.Int32
		}
		row.AllowedClusterIds = []string(allowedArr)
		resp.Streams = append(resp.Streams, &row)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "iterate managed streams: %v", err)
	}
	return resp, nil
}

// RecordStreamActiveCluster updates commodore.streams.active_ingest_cluster_id
// for a managed stream. Mirrors the contended-update guard from
// ValidateStreamKey's push-ingest path so a stale claim cannot overwrite a
// fresh lease held by a different cluster.
func (s *CommodoreServer) RecordStreamActiveCluster(ctx context.Context, req *pb.RecordStreamActiveClusterRequest) (*pb.RecordStreamActiveClusterResponse, error) {
	streamID := strings.TrimSpace(req.GetStreamId())
	clusterID := strings.TrimSpace(req.GetClusterId())
	if streamID == "" || clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id and cluster_id required")
	}
	const updateSQL = `
		UPDATE commodore.streams
		SET active_ingest_cluster_id = $1,
		    active_ingest_cluster_updated_at = NOW(),
		    updated_at = NOW()
		WHERE id = $2::uuid
		  AND (
		    active_ingest_cluster_id IS NULL
		    OR active_ingest_cluster_id = ''
		    OR active_ingest_cluster_id = $1
		    OR active_ingest_cluster_updated_at IS NULL
		    OR active_ingest_cluster_updated_at < NOW() - INTERVAL '30 seconds'
		  )`
	res, err := s.db.ExecContext(ctx, updateSQL, clusterID, streamID)
	if err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"stream_id":  streamID,
			"cluster_id": clusterID,
		}).Error("RecordStreamActiveCluster: update failed")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "rows affected: %v", err)
	}
	return &pb.RecordStreamActiveClusterResponse{Updated: rows > 0}, nil
}

// ClearStreamActiveCluster clears commodore.streams.active_ingest_cluster_id
// for a managed stream once Foghorn has confirmed via heartbeat snapshot
// that Mist no longer has the config. expected_cluster_id guards against
// clobbering a fresher claim from a peer cluster — the column only
// transitions to NULL when the recorded value matches the caller.
func (s *CommodoreServer) ClearStreamActiveCluster(ctx context.Context, req *pb.ClearStreamActiveClusterRequest) (*pb.ClearStreamActiveClusterResponse, error) {
	streamID := strings.TrimSpace(req.GetStreamId())
	expected := strings.TrimSpace(req.GetExpectedClusterId())
	if streamID == "" || expected == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id and expected_cluster_id required")
	}
	const clearSQL = `
		UPDATE commodore.streams
		SET active_ingest_cluster_id = NULL,
		    active_ingest_cluster_updated_at = NOW(),
		    updated_at = NOW()
		WHERE id = $1::uuid
		  AND active_ingest_cluster_id = $2`
	res, err := s.db.ExecContext(ctx, clearSQL, streamID, expected)
	if err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"stream_id":  streamID,
			"cluster_id": expected,
		}).Error("ClearStreamActiveCluster: update failed")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "rows affected: %v", err)
	}
	return &pb.ClearStreamActiveClusterResponse{Cleared: rows > 0}, nil
}

// ResolvePlaybackID resolves a playback ID to internal name for MistServer PLAY_REWRITE trigger
func (s *CommodoreServer) ResolvePlaybackID(ctx context.Context, req *pb.ResolvePlaybackIDRequest) (*pb.ResolvePlaybackIDResponse, error) {
	playbackID := req.GetPlaybackId()
	if playbackID == "" {
		return nil, status.Error(codes.InvalidArgument, "playback_id required")
	}

	// playback_id is globally UNIQUE (commodore.sql), so no tenant_id filter needed
	var streamID, internalName, tenantID, ingestMode string
	var requiresAuth bool
	var activeIngestClusterID sql.NullString
	err := s.retryPostgres(ctx, func() error {
		return s.db.QueryRowContext(ctx, `
			SELECT id, internal_name, tenant_id, requires_auth, ingest_mode, active_ingest_cluster_id
			FROM commodore.streams WHERE playback_id = $1
		`, playbackID).Scan(&streamID, &internalName, &tenantID, &requiresAuth, &ingestMode, &activeIngestClusterID)
	})

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

	resp := &pb.ResolvePlaybackIDResponse{
		InternalName: internalName,
		TenantId:     tenantID,
		PlaybackId:   playbackID,
		StreamId:     streamID,
		RequiresAuth: requiresAuth,
		IngestMode:   ingestMode,
	}

	if route, err := s.resolveClusterRouteForTenant(ctx, tenantID); err == nil {
		resp.OriginClusterId = &route.clusterID
		if route.officialClusterID != "" {
			resp.OfficialClusterId = &route.officialClusterID
		}
		resp.ClusterPeers = route.clusterPeers
	}
	// Managed (mist_native) streams may be placed in a cluster other than the
	// tenant's default route; active_ingest_cluster_id is the verified-applied
	// source cluster recorded by Foghorn. When set, it overrides the tenant
	// default as origin so PLAY_REWRITE / federation / artifact attribution
	// follow the active source. Peers and official cluster stay tenant-routed.
	if activeIngestClusterID.Valid && activeIngestClusterID.String != "" {
		active := activeIngestClusterID.String
		resp.OriginClusterId = &active
	}

	return resp, nil
}

// ResolveInternalName resolves an internal_name to tenant context for event enrichment
func (s *CommodoreServer) ResolveInternalName(ctx context.Context, req *pb.ResolveInternalNameRequest) (*pb.ResolveInternalNameResponse, error) {
	internalName := req.GetInternalName()
	if internalName == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name required")
	}

	// internal_name is globally UNIQUE (commodore.sql), so no tenant_id filter needed
	var streamID, tenantID, userID string
	var isRecordingEnabled bool
	var requiresAuth bool
	var activeIngestClusterID sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, user_id, is_recording_enabled, requires_auth, active_ingest_cluster_id
		FROM commodore.streams WHERE internal_name = $1
	`, internalName).Scan(&streamID, &tenantID, &userID, &isRecordingEnabled, &requiresAuth, &activeIngestClusterID)

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
		RequiresAuth:       requiresAuth,
	}
	if route, err := s.resolveClusterRouteForTenant(ctx, tenantID); err == nil {
		resp.ClusterPeers = route.clusterPeers
		resp.OriginClusterId = route.clusterID
	}
	// Managed (mist_native) streams may be placed in a cluster other than the
	// tenant's default route; active_ingest_cluster_id is the verified-applied
	// source cluster recorded by Foghorn. When set, it is the authoritative
	// origin — federation/thumbnail/storage attribution must follow the
	// active source, not the tenant default. Peers stay tenant-routed.
	if activeIngestClusterID.Valid && activeIngestClusterID.String != "" {
		resp.OriginClusterId = activeIngestClusterID.String
	}
	return resp, nil
}

// ResolvePullSourceByInternalName returns the configured upstream pull URI for a
// pull-mode stream, decrypted. Foghorn calls this from STREAM_SOURCE handling
// and /source origin selection for pull+<internal_name> streams.
func (s *CommodoreServer) ResolvePullSourceByInternalName(ctx context.Context, req *pb.ResolvePullSourceByInternalNameRequest) (*pb.ResolvePullSourceByInternalNameResponse, error) {
	internalName := req.GetInternalName()
	if internalName == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name required")
	}

	var (
		streamID          string
		tenantID          string
		ingestMode        string
		sourceURIEnc      string
		enabled           bool
		allowedClusterIDs pq.StringArray
	)
	err := s.retryPostgres(ctx, func() error {
		return s.db.QueryRowContext(ctx, `
				SELECT s.id, s.tenant_id, s.ingest_mode,
				       p.source_uri_enc, p.enabled, COALESCE(p.allowed_cluster_ids, '{}')
				FROM commodore.streams s
				JOIN commodore.stream_pull_sources p ON p.stream_id = s.id
				WHERE s.internal_name = $1
			`, internalName).Scan(&streamID, &tenantID, &ingestMode, &sourceURIEnc, &enabled, &allowedClusterIDs)
	})

	if errors.Is(err, sql.ErrNoRows) {
		return &pb.ResolvePullSourceByInternalNameResponse{Found: false}, nil
	}
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"error":         err,
		}).Error("Database error resolving pull source")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	if ingestMode != "pull" {
		// Stream exists but isn't a pull stream — refuse to leak any URI.
		return &pb.ResolvePullSourceByInternalNameResponse{Found: false}, nil
	}

	sourceURI, err := s.pullSourceEncryptor.Decrypt(sourceURIEnc)
	if err != nil {
		s.logger.WithError(err).WithField("internal_name", internalName).Warn("Failed to decrypt pull source_uri")
		return nil, status.Error(codes.Internal, "failed to decrypt pull source")
	}

	return &pb.ResolvePullSourceByInternalNameResponse{
		Found:             true,
		SourceUri:         sourceURI,
		Enabled:           enabled,
		TenantId:          tenantID,
		StreamId:          streamID,
		AllowedClusterIds: []string(allowedClusterIDs),
	}, nil
}

func normalizeIngestMode(mode string) string {
	return strings.ToLower(strings.TrimSpace(mode))
}

func buildPullSourceView(rawURI string, enabled bool, class pullsource.Class, allowedClusterIDs []string) *pb.PullSourceView {
	if rawURI == "" {
		return nil
	}
	return &pb.PullSourceView{
		SourceUriRedacted: pullsource.Redact(rawURI),
		Enabled:           enabled,
		Class:             class.String(),
		AllowedClusterIds: allowedClusterIDs,
	}
}

func pullSourceEnabled(input *pb.PullSourceInput) bool {
	if input == nil || input.Enabled == nil {
		return true
	}
	return input.GetEnabled()
}

// validatePullSourceEligibility validates a runtime CRUD pull-source input:
// classifies the URI, then enforces per-source placement via
// FilterPlacementClusters against Quartermaster's registered edge clusters.
// Returns the canonical (sorted, deduped) allowed_cluster_ids the caller
// should persist.
func (s *CommodoreServer) validatePullSourceEligibility(ctx context.Context, rawURI string, allowedClusterIDs []string) (pullsource.Class, []string, error) {
	class, err := pullsource.Classify(rawURI)
	if class == pullsource.ClassBlocked {
		if err == nil {
			err = errors.New("source_uri rejected")
		}
		return class, nil, status.Errorf(codes.InvalidArgument, "invalid pull source: %v", err)
	}
	if s.quartermasterClient == nil {
		return class, nil, status.Error(codes.FailedPrecondition, "cannot validate pull source eligibility: Quartermaster unavailable")
	}
	candidates, err := s.listPullSourceClusterCapabilities(ctx)
	if err != nil {
		return class, nil, err
	}
	if len(candidates) == 0 {
		return class, nil, status.Error(codes.FailedPrecondition, "no eligible edge cluster is registered for pull streams")
	}
	normalized := normalizeAllowedClusterIDs(allowedClusterIDs)
	_, rejects := pullsource.FilterPlacementClusters(class, normalized, candidates)
	if len(rejects) > 0 {
		return class, nil, status.Errorf(codes.InvalidArgument, "pull source placement rejected: %s", formatRuntimePlacementRejects(rejects, pullsource.Redact(rawURI)))
	}
	return class, normalized, nil
}

func (s *CommodoreServer) listPullSourceClusterCapabilities(ctx context.Context) ([]pullsource.ClusterCapability, error) {
	var (
		out   []pullsource.ClusterCapability
		after *string
	)
	for {
		resp, err := s.quartermasterClient.ListClusters(ctx, &pb.CursorPaginationRequest{
			First: int32(pagination.MaxLimit),
			After: after,
		})
		if err != nil {
			return nil, status.Errorf(codes.FailedPrecondition, "cannot validate pull source eligibility: %v", err)
		}
		for _, c := range resp.GetClusters() {
			if c.GetClusterType() != "edge" {
				continue
			}
			out = append(out, pullsource.ClusterCapability{
				ID:                      c.GetClusterId(),
				AllowPrivatePullSources: c.GetAllowPrivatePullSources(),
			})
		}
		page := resp.GetPagination()
		if page == nil || !page.GetHasNextPage() {
			break
		}
		next := page.GetEndCursor()
		if next == "" {
			return nil, status.Error(codes.FailedPrecondition, "cannot validate pull source eligibility: Quartermaster pagination cursor missing")
		}
		after = &next
	}
	return out, nil
}

// loadPullSourceState returns the decrypted URI, enabled flag, and
// allowed_cluster_ids for an existing pull stream owned by (userID, tenantID).
// UpdateStream needs all three so it can apply per-field preserve semantics
// (a request can touch any subset without wiping the others).
func (s *CommodoreServer) loadPullSourceState(ctx context.Context, streamID, userID, tenantID string) (string, bool, []string, error) {
	var enc string
	var enabled bool
	var allowed pq.StringArray
	err := s.db.QueryRowContext(ctx, `
		SELECT p.source_uri_enc, p.enabled, COALESCE(p.allowed_cluster_ids, '{}')
		FROM commodore.streams s
		JOIN commodore.stream_pull_sources p ON p.stream_id = s.id
		WHERE s.id = $1 AND s.user_id = $2 AND s.tenant_id = $3
	`, streamID, userID, tenantID).Scan(&enc, &enabled, &allowed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil, status.Error(codes.NotFound, "pull source not found")
	}
	if err != nil {
		return "", false, nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	plain, err := s.pullSourceEncryptor.Decrypt(enc)
	if err != nil {
		return "", false, nil, status.Errorf(codes.Internal, "failed to decrypt pull source: %v", err)
	}
	return plain, enabled, []string(allowed), nil
}

// formatRuntimePlacementRejects renders FilterPlacementClusters rejects as a
// single API error string for CreateStream/UpdateStream callers.
func formatRuntimePlacementRejects(rejects []pullsource.PlacementReject, redactedURI string) string {
	parts := make([]string, 0, len(rejects))
	for _, r := range rejects {
		switch r.Reason {
		case pullsource.PlacementRejectEmptyForPrivate:
			parts = append(parts, fmt.Sprintf("source_uri %s is private/multicast and requires explicit allowed_cluster_ids", redactedURI))
		case pullsource.PlacementRejectUnknownCluster:
			parts = append(parts, fmt.Sprintf("allowed_cluster_ids entry %q is not a registered media (edge) cluster", r.ClusterID))
		case pullsource.PlacementRejectMissingPrivateCapability:
			parts = append(parts, fmt.Sprintf("allowed_cluster_ids entry %q does not have allow_private_pull_sources=true", r.ClusterID))
		default:
			parts = append(parts, fmt.Sprintf("allowed_cluster_ids entry %q rejected: %s", r.ClusterID, r.Reason))
		}
	}
	return strings.Join(parts, "; ")
}

// normalizeAllowedClusterIDs mirrors the bootstrap reconciler helper. Kept
// local to the gRPC package so CreateStream / UpdateStream call sites use the
// same canonical form (sorted, deduped, trimmed).
func normalizeAllowedClusterIDs(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, id := range in {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
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
	if _, updateErr := s.db.ExecContext(ctx, `UPDATE commodore.api_tokens SET last_used_at = NOW() WHERE id = $1`, tokenID); updateErr != nil {
		s.logger.WithError(updateErr).Debug("Failed to update API token last_used_at")
	}

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

// Quartermaster lookup functions are package variables so tests can exercise
// the ownership policy without a real Quartermaster gRPC server.
var (
	mistAdminGetNodeOwner = func(s *CommodoreServer, ctx context.Context, nodeID string) (*pb.NodeOwnerResponse, error) {
		if s.quartermasterClient == nil {
			return nil, fmt.Errorf("quartermaster client unavailable")
		}
		return s.quartermasterClient.GetNodeOwner(ctx, nodeID)
	}
	mistAdminGetCluster = func(s *CommodoreServer, ctx context.Context, clusterID string) (*pb.ClusterResponse, error) {
		if s.quartermasterClient == nil {
			return nil, fmt.Errorf("quartermaster client unavailable")
		}
		return s.quartermasterClient.GetCluster(ctx, clusterID)
	}
)

// MintMistAdminSession returns a short-TTL JWT authorizing the caller to
// open the Mist admin UI on the named edge node.
//
// The Gateway resolver is the primary policy enforcer; this RPC is the
// second wall. Mist admin can read local files and run processes, so
// cluster ownership is the gate — not just a role string. Two
// non-negotiables here:
//
//  1. Identity comes from TRUSTED gRPC context (set by the gateway auth
//     middleware), not from the request body. The proto carries only the
//     target node_id; user_id / tenant_id / role are extracted server-
//     side. Callers cannot lift privileges by claiming a different
//     tenant in the request.
//
//  2. Ownership is verified against Quartermaster. The caller must be an
//     owner/admin in the cluster's owner tenant; the reserved system
//     tenant is allowed as platform break-glass.
func (s *CommodoreServer) MintMistAdminSession(ctx context.Context, req *pb.MintMistAdminSessionRequest) (*pb.MintMistAdminSessionResponse, error) {
	nodeID := strings.TrimSpace(req.GetNodeId())
	if nodeID == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id is required")
	}

	trustedUserID, trustedTenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	trustedRole := strings.TrimSpace(ctxkeys.GetRole(ctx))

	ownerResp, err := mistAdminGetNodeOwner(s, ctx, nodeID)
	if err != nil {
		s.logger.WithError(err).WithField("node_id", nodeID).Warn("MintMistAdminSession: GetNodeOwner failed")
		return nil, status.Errorf(codes.Internal, "resolve node owner: %v", err)
	}
	clusterID := strings.TrimSpace(ownerResp.GetClusterId())
	if clusterID == "" {
		return nil, status.Error(codes.NotFound, "node has no cluster")
	}
	clusterResp, err := mistAdminGetCluster(s, ctx, clusterID)
	if err != nil || clusterResp == nil || clusterResp.GetCluster() == nil {
		s.logger.WithError(err).WithField("cluster_id", clusterID).Warn("MintMistAdminSession: GetCluster failed")
		return nil, status.Errorf(codes.Internal, "resolve cluster: %v", err)
	}
	isPlatformOfficial := clusterResp.GetCluster().GetIsPlatformOfficial()
	ownerTenantID := strings.TrimSpace(ownerResp.GetOwnerTenantId())

	if !auth.CanAdminMistNode(ownerTenantID, trustedTenantID, trustedRole) {
		s.logger.WithFields(logging.Fields{
			"node_id":              nodeID,
			"cluster_id":           clusterID,
			"is_platform_official": isPlatformOfficial,
			"trusted_user_id":      trustedUserID,
			"trusted_tenant_id":    trustedTenantID,
			"trusted_role":         trustedRole,
		}).Warn("MintMistAdminSession denied: caller does not own node")
		return nil, status.Error(codes.PermissionDenied, "node admin access denied")
	}

	secret := []byte(config.RequireEnv("JWT_SECRET"))
	token, exp, err := auth.GenerateMistAdminSessionJWT(
		trustedUserID,
		trustedTenantID,
		trustedRole,
		nodeID,
		clusterID,
		0, // default 5min TTL
		secret,
	)
	if err != nil {
		s.logger.WithError(err).Warn("MintMistAdminSession failed")
		return nil, status.Errorf(codes.Internal, "mint session: %v", err)
	}

	// Compose the public edge FQDN the same way Foghorn does, so the
	// gateway/webapp don't reinvent the string format. cluster_slug is
	// derived from cluster_id via SanitizeLabel — single source of truth
	// is pkg/dns.
	edgeDomain := pkgdns.EdgeNodeFQDN(
		nodeID,
		pkgdns.SanitizeLabel(clusterID),
		mistAdminRootDomain(),
	)

	s.logger.WithFields(logging.Fields{
		"user_id":              trustedUserID,
		"tenant_id":            trustedTenantID,
		"role":                 trustedRole,
		"node_id":              nodeID,
		"cluster_id":           clusterID,
		"is_platform_official": isPlatformOfficial,
		"edge_domain":          edgeDomain,
		"expires_at":           exp.Unix(),
	}).Info("Minted mist admin session token")
	s.emitMistAdminSessionMintedEvent(ctx, trustedUserID, trustedTenantID, nodeID, clusterID)
	return &pb.MintMistAdminSessionResponse{
		Token:      token,
		ExpiresAt:  exp.Unix(),
		EdgeDomain: edgeDomain,
	}, nil
}

// mistAdminRootDomain resolves the platform root domain via the same
// env precedence the rest of Commodore uses (populateTieredDomains).
func mistAdminRootDomain() string {
	rootDomain := strings.TrimSpace(os.Getenv("PLATFORM_ROOT_DOMAIN"))
	if rootDomain == "" {
		rootDomain = strings.TrimSpace(os.Getenv("BRAND_DOMAIN"))
	}
	if rootDomain == "" {
		rootDomain = "frameworks.network"
	}
	return rootDomain
}

// ValidateMistAdminSession verifies a session token against the node the
// caller (Foghorn) says is the connected Helmsman's nodeID. Bound-node
// enforcement lives in pkg/auth so every validation path uses the same
// node-binding rule.
func (s *CommodoreServer) ValidateMistAdminSession(ctx context.Context, req *pb.ValidateMistAdminSessionRequest) (*pb.ValidateMistAdminSessionResponse, error) {
	if req.GetToken() == "" || req.GetExpectedNodeId() == "" {
		return &pb.ValidateMistAdminSessionResponse{Valid: false}, nil
	}
	secret := []byte(config.RequireEnv("JWT_SECRET"))
	claims, err := auth.ValidateMistAdminSessionJWT(req.GetToken(), secret, req.GetExpectedNodeId())
	if err != nil {
		s.logger.WithError(err).WithField("expected_node_id", req.GetExpectedNodeId()).
			Debug("mist admin session validation rejected")
		return &pb.ValidateMistAdminSessionResponse{Valid: false}, nil
	}
	return &pb.ValidateMistAdminSessionResponse{
		Valid:     true,
		UserId:    claims.UserID,
		TenantId:  claims.TenantID,
		Role:      claims.Role,
		NodeId:    claims.NodeID,
		ClusterId: claims.ClusterID,
		ExpiresAt: claims.ExpiresAt.Unix(),
	}, nil
}

// StartDVR initiates DVR recording for a stream (Gateway → Commodore → Foghorn).
func (s *CommodoreServer) StartDVR(ctx context.Context, req *pb.StartDVRRequest) (*pb.StartDVRResponse, error) {
	// Get user and tenant context from gateway metadata.
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	foghornClient, dvrRoute, err := s.resolveFoghornForTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	// One Purser RPC for both suspension AND retention. Avoids a per-DVR-start
	// GetSubscription + GetBillingTier roundtrip since GetTenantBillingStatus
	// returns recording_retention_days alongside is_suspended.
	var billingStatus *pb.GetTenantBillingStatusResponse
	if s.purserClient != nil {
		var bsErr error
		billingStatus, bsErr = s.purserClient.GetTenantBillingStatus(ctx, tenantID)
		if bsErr != nil {
			s.logger.WithError(bsErr).Warn("Failed to fetch billing status; continuing fail-open")
		}
	}
	if billingStatus != nil && billingStatus.IsSuspended {
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

	// Retention is post-end semantics: the artifact's retention_until is
	// computed at FinalizeDVR-time as ended_at + dvr_retention_days*24h
	// (using the persisted snapshot from session start so the policy that
	// was in force at start time is what applies, even if the tenant's
	// plan has since changed). For 24/7 streams that may run for months,
	// computing expires_at at start time would mark active recordings as
	// expired while the stream is still live; we leave it nil here and
	// Foghorn back-fills commodore.dvr_recordings.retention_until after
	// FinalizeDVR.

	processClusterID := ""
	if dvrRoute != nil {
		processClusterID = dvrRoute.clusterID
	}
	foghornReq := &pb.StartDVRRequest{
		TenantId:      tenantID,
		InternalName:  internalName,
		UserId:        &userID,
		ProcessesJson: s.resolveProcessesJSON(ctx, tenantID, streamID, processClusterID, "dvr"),
	}
	if streamID != "" {
		foghornReq.StreamId = &streamID
	}
	if billingStatus != nil && billingStatus.DvrPolicy != nil {
		foghornReq.DvrPolicy = billingStatus.DvrPolicy
	}
	// Run the per-class cascade (per-stream → tenant per-class → system
	// default) clamped by the tier cap. Resolved value carries 0 = "keep
	// forever" (NULL retention_until at finalize); >0 sets that many days.
	// Per-asset override doesn't apply at start (artifact doesn't exist yet);
	// it kicks in via UpdateAssetRetention after finalize.
	if dvrDays, dvrErr := s.resolveInitialRetention(ctx, pb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_DVR, tenantID, streamID); dvrErr == nil {
		if foghornReq.DvrPolicy == nil {
			foghornReq.DvrPolicy = &pb.DVRPolicy{}
		}
		days := dvrDays
		foghornReq.DvrPolicy.RecordingRetentionDays = &days
	} else {
		s.logger.WithError(dvrErr).WithFields(logging.Fields{
			"tenant_id": tenantID,
			"stream_id": streamID,
		}).Warn("DVR retention resolution failed; Foghorn falls back to its 30-day default")
	}
	// Forward caller-supplied window so dvrpolicy.Resolve can clamp it
	// against tier and cluster live-window bounds inside Foghorn.
	if w := req.GetDvrWindowSeconds(); w > 0 {
		foghornReq.DvrWindowSeconds = &w
	}

	// Snapshot Stream-level chapter config onto the DVR artifact. Reads
	// happen on the same row we just resolved internal_name from; one
	// extra query keeps the snapshot inside this critical section so
	// concurrent Stream.dvrChapterMode mutations don't race the recording.
	var chapterMode sql.NullString
	var chapterIntervalSeconds sql.NullInt32
	if scanErr := s.db.QueryRowContext(ctx, `
		SELECT dvr_chapter_mode, dvr_chapter_interval_seconds
		  FROM commodore.streams
		 WHERE id = $1::uuid AND tenant_id = $2::uuid
	`, streamID, tenantID).Scan(&chapterMode, &chapterIntervalSeconds); scanErr == nil {
		if chapterMode.Valid && chapterMode.String != "" {
			mode := chapterMode.String
			foghornReq.DvrChapterMode = &mode
		}
		if chapterIntervalSeconds.Valid && chapterIntervalSeconds.Int32 > 0 {
			iv := chapterIntervalSeconds.Int32
			foghornReq.DvrChapterIntervalSeconds = &iv
		}
	} else if !errors.Is(scanErr, sql.ErrNoRows) {
		s.logger.WithError(scanErr).WithField("stream_id", streamID).Warn("Failed to read Stream chapter config; recording starts without chapters")
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
// See: docs/architecture/clips-dvr.md
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

	var (
		sourceRequiresAuth bool
		sourcePolicyJSON   sql.NullString
		sourceSecretEnc    sql.NullString
	)
	err = s.db.QueryRowContext(ctx, `
		SELECT requires_auth, playback_policy::text, playback_webhook_secret_enc
		FROM commodore.streams
		WHERE id = $1 AND tenant_id = $2
	`, streamID, tenantID).Scan(&sourceRequiresAuth, &sourcePolicyJSON, &sourceSecretEnc)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "stream not found")
		}
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Insert into business registry
	storageClusterID := sql.NullString{String: req.GetStorageClusterId(), Valid: req.GetStorageClusterId() != ""}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO commodore.clips (
			id, tenant_id, user_id, stream_id, clip_hash, internal_name, playback_id,
			title, description, start_time, duration, clip_mode, requested_params,
			origin_cluster_id, storage_cluster_id, requires_auth, playback_policy, playback_webhook_secret_enc,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17::jsonb, $18, NOW(), NOW())
	`, clipID, tenantID, userID, streamID, clipHash, artifactInternalName, playbackID,
		req.GetTitle(), req.GetDescription(), req.GetStartTime(), req.GetDuration(),
		req.GetClipMode(), req.GetRequestedParams(), req.GetOriginClusterId(), storageClusterID,
		sourceRequiresAuth, sourcePolicyJSON, sourceSecretEnc)

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
		ClipHash:     clipHash,
		ClipId:       clipID,
		PlaybackId:   playbackID,
		InternalName: artifactInternalName,
	}, nil
}

// RegisterDVR creates a new DVR recording in the business registry
// Called by Foghorn during the StartDVR flow
func (s *CommodoreServer) RegisterDVR(ctx context.Context, req *pb.RegisterDVRRequest) (*pb.RegisterDVRResponse, error) {
	tenantID := req.GetTenantId()
	userID := req.GetUserId()
	internalName := req.GetStreamInternalName()

	if tenantID == "" || userID == "" || internalName == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id, user_id, and stream_internal_name are required")
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

	// Insert into business registry. DVR callers normally leave
	// retention_until NULL at start; Foghorn back-fills it at FinalizeDVR
	// after the stream session ends.
	var retentionUntilArg any
	if req.GetRetentionUntil() != nil {
		retentionUntilArg = req.GetRetentionUntil().AsTime()
	}
	storageClusterID := sql.NullString{String: req.GetStorageClusterId(), Valid: req.GetStorageClusterId() != ""}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO commodore.dvr_recordings (
			id, tenant_id, user_id, stream_id, dvr_hash, internal_name, playback_id, stream_internal_name,
			origin_cluster_id, storage_cluster_id, retention_until, created_at, updated_at
		) VALUES ($1, $2, $3, $4::uuid, $5, $6, $7, $8, $9, $10, $11, NOW(), NOW())
	`, dvrID, tenantID, userID, streamID, dvrHash, artifactInternalName, playbackID, internalName, req.GetOriginClusterId(), storageClusterID, retentionUntilArg)

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
		DvrHash:      dvrHash,
		DvrId:        dvrID,
		PlaybackId:   playbackID,
		InternalName: artifactInternalName,
		StreamId:     streamID,
	}, nil
}

// UpdateDVRRetention back-fills commodore.dvr_recordings.retention_until from
// Foghorn at finalize time. Foghorn computes the value from the persisted
// dvr_retention_days snapshot (ended_at + days*24h), so the business
// registry's expires_at reflects post-end retention rather than a synthetic
// start-time projection. Active recordings carry NULL until they finalize.
func (s *CommodoreServer) UpdateDVRRetention(ctx context.Context, req *pb.UpdateDVRRetentionRequest) (*pb.UpdateDVRRetentionResponse, error) {
	dvrHash := req.GetDvrHash()
	if dvrHash == "" {
		return nil, status.Error(codes.InvalidArgument, "dvr_hash is required")
	}
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	var retentionArg any
	if req.GetRetentionUntil() != nil {
		retentionArg = req.GetRetentionUntil().AsTime()
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE commodore.dvr_recordings
		   SET retention_until = $1,
		       updated_at      = NOW()
		 WHERE dvr_hash = $2
		   AND tenant_id::text = $3
	`, retentionArg, dvrHash, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "update retention failed: %v", err)
	}
	affected, rowsErr := res.RowsAffected()
	if rowsErr != nil {
		return nil, status.Errorf(codes.Internal, "update retention affected rows failed: %v", rowsErr)
	}
	return &pb.UpdateDVRRetentionResponse{Updated: affected > 0}, nil
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
			   c.playback_id, c.internal_name, c.origin_cluster_id
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
		Found:              true,
		TenantId:           tenantID,
		UserId:             userID,
		StreamId:           streamID,
		StreamInternalName: internalName.String,
		Title:              title,
		Description:        description,
		StartTime:          startTime,
		Duration:           duration,
		ClipMode:           clipMode,
		PlaybackId:         playbackID,
		InternalName:       artifactInternalName,
		OriginClusterId:    originClusterID.String,
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

	err := s.retryPostgres(ctx, func() error {
		return s.db.QueryRowContext(ctx, `
			SELECT tenant_id, user_id, stream_id, stream_internal_name, playback_id, internal_name, origin_cluster_id
			FROM commodore.dvr_recordings
			WHERE dvr_hash = $1
		`, dvrHash).Scan(&tenantID, &userID, &streamID, &internalName, &playbackID, &artifactInternalName, &originClusterID)
	})

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
		Found:              true,
		TenantId:           tenantID,
		UserId:             userID,
		StreamId:           streamID.String,
		StreamInternalName: internalName,
		PlaybackId:         playbackID,
		InternalName:       artifactInternalName,
		OriginClusterId:    originClusterID.String,
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
	storageClusterID := sql.NullString{String: req.GetStorageClusterId(), Valid: req.GetStorageClusterId() != ""}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO commodore.vod_assets (
			id, tenant_id, user_id, vod_hash, internal_name, playback_id,
			title, description, filename, content_type, size_bytes,
			origin_cluster_id, storage_cluster_id, retention_until, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, NOW(), NOW())
	`, vodID, tenantID, userID, vodHash, artifactInternalName, playbackID,
		req.GetTitle(), req.GetDescription(), filename, req.GetContentType(), req.GetSizeBytes(),
		req.GetOriginClusterId(), storageClusterID, retentionUntil)

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
		VodHash:      vodHash,
		VodId:        vodID,
		PlaybackId:   playbackID,
		InternalName: artifactInternalName,
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

	err := s.retryPostgres(ctx, func() error {
		return s.db.QueryRowContext(ctx, `
			SELECT tenant_id, user_id, filename, title, description, playback_id, internal_name, origin_cluster_id
			FROM commodore.vod_assets
			WHERE vod_hash = $1
		`, vodHash).Scan(&tenantID, &userID, &filename, &title, &description, &playbackID, &artifactInternalName, &originClusterID)
	})

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

	resp := &pb.ResolveVodHashResponse{
		Found:           true,
		TenantId:        tenantID,
		UserId:          userID,
		Filename:        filename,
		Title:           title.String,
		Description:     description.String,
		PlaybackId:      playbackID,
		InternalName:    artifactInternalName,
		OriginClusterId: originClusterID.String,
	}
	// Carry the tenant's cluster peers so a cross-cluster relay resolve can
	// enforce the federation allowlist on the origin (and any storage redirect).
	s.populateArtifactClusterContext(ctx, tenantID, &resp.ClusterPeers)
	return resp, nil
}

// ResolveVodID resolves a VOD relay ID (commodore.vod_assets.id) to vod_hash + tenant context
func (s *CommodoreServer) ResolveVodID(ctx context.Context, req *pb.ResolveVodIDRequest) (*pb.ResolveVodIDResponse, error) {
	vodID := req.GetVodId()
	if vodID == "" {
		return nil, status.Error(codes.InvalidArgument, "vod_id is required")
	}

	var tenantID, userID, vodHash, playbackID, artifactInternalName string
	err := s.retryPostgres(ctx, func() error {
		return s.db.QueryRowContext(ctx, `
			SELECT tenant_id, user_id, vod_hash, playback_id, internal_name
			FROM commodore.vod_assets
			WHERE id = $1
		`, vodID).Scan(&tenantID, &userID, &vodHash, &playbackID, &artifactInternalName)
	})

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
		Found:        true,
		TenantId:     tenantID,
		UserId:       userID,
		VodHash:      vodHash,
		PlaybackId:   playbackID,
		InternalName: artifactInternalName,
	}, nil
}

// MintChapterPlaybackID mints (or returns the existing) public playback_id
// for a hidden chapter artifact. Called by Foghorn at chapter finalization
// dispatch. Idempotent on chapter_id — repeat calls return the same
// playback_id even across finalization retries; artifact_hash is upserted
// because retries may reuse the same hash via the deterministic
// chapterPlaybackArtifactHash() derivation, but tenants change rarely.
func (s *CommodoreServer) MintChapterPlaybackID(ctx context.Context, req *pb.MintChapterPlaybackIDRequest) (*pb.MintChapterPlaybackIDResponse, error) {
	chapterID := req.GetChapterId()
	tenantID := req.GetTenantId()
	artifactHash := req.GetArtifactHash()
	userID := req.GetUserId()
	if chapterID == "" || tenantID == "" || artifactHash == "" || userID == "" {
		return nil, status.Error(codes.InvalidArgument, "chapter_id, tenant_id, artifact_hash, and user_id are required")
	}

	// Mint a fresh playback_id for the INSERT path. ON CONFLICT returns
	// the existing row's playback_id; the freshly-generated value is
	// discarded.
	playbackID, err := generateArtifactPlaybackID()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate playback ID: %v", err)
	}

	var stored string
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO commodore.dvr_chapter_playback (
			chapter_id, tenant_id, playback_id, artifact_hash, created_at, updated_at
		) VALUES ($1, $2::uuid, $3, $4, NOW(), NOW())
		ON CONFLICT (chapter_id) DO UPDATE
			SET artifact_hash = EXCLUDED.artifact_hash,
			    updated_at    = NOW()
		RETURNING playback_id
	`, chapterID, tenantID, playbackID, artifactHash).Scan(&stored)
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"chapter_id":    chapterID,
			"tenant_id":     tenantID,
			"artifact_hash": artifactHash,
			"error":         err,
		}).Error("Failed to mint chapter playback id")
		return nil, status.Errorf(codes.Internal, "mint chapter playback id: %v", err)
	}

	filename := req.GetFilename()
	if filename == "" {
		filename = "dvr-chapter-" + chapterID + ".mkv"
	}
	title := req.GetTitle()
	if title == "" {
		title = "DVR chapter"
	}
	description := req.GetDescription()
	contentType := "video/x-matroska"
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO commodore.vod_assets (
			id, tenant_id, user_id, stream_id, vod_hash, internal_name, playback_id,
			title, description, filename, content_type,
			origin_cluster_id, storage_cluster_id,
			library_visible, origin_type, origin_id,
			created_at, updated_at
		) VALUES (
			$1, $2::uuid, $3::uuid, NULLIF($4, '')::uuid, $5, $6, $7,
			$8, NULLIF($9, ''), $10, $11,
			NULLIF($12, ''), NULLIF($13, ''),
			false, 'dvr_chapter', $14,
			NOW(), NOW()
		)
		ON CONFLICT (vod_hash) DO UPDATE SET
			user_id            = EXCLUDED.user_id,
			stream_id          = EXCLUDED.stream_id,
			internal_name      = EXCLUDED.internal_name,
			playback_id        = EXCLUDED.playback_id,
			title              = EXCLUDED.title,
			description        = EXCLUDED.description,
			filename           = EXCLUDED.filename,
			content_type       = EXCLUDED.content_type,
			origin_cluster_id  = EXCLUDED.origin_cluster_id,
			storage_cluster_id = EXCLUDED.storage_cluster_id,
			library_visible    = false,
			origin_type        = 'dvr_chapter',
			origin_id          = EXCLUDED.origin_id,
			updated_at         = NOW()
	`, uuid.New().String(), tenantID, userID, req.GetStreamId(), artifactHash, artifactHash, stored,
		title, description, filename, contentType,
		req.GetOriginClusterId(), req.GetStorageClusterId(), chapterID)
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"chapter_id":    chapterID,
			"tenant_id":     tenantID,
			"artifact_hash": artifactHash,
			"error":         err,
		}).Error("Failed to register chapter VOD asset")
		return nil, status.Errorf(codes.Internal, "register chapter VOD asset: %v", err)
	}

	return &pb.MintChapterPlaybackIDResponse{PlaybackId: stored}, nil
}

// GetTenantProcessesJSON exposes Commodore's resolved MistServer process config
// for a given tenant/stream/lifecycle. Foghorn-internal pipelines store and
// apply the returned snapshot without deriving local lifecycle subsets.
func (s *CommodoreServer) GetTenantProcessesJSON(ctx context.Context, req *pb.GetTenantProcessesJSONRequest) (*pb.GetTenantProcessesJSONResponse, error) {
	tenantID := req.GetTenantId()
	lifecycle := req.GetLifecycle()
	if lifecycle == "" {
		lifecycle = req.GetStreamType()
	}
	if tenantID == "" || lifecycle == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and lifecycle are required")
	}
	if !validProcessLifecycle(lifecycle) {
		return nil, status.Error(codes.InvalidArgument, `lifecycle must be "live", "dvr", "clip", "dvr_finalize", or "vod"`)
	}
	lifecycle = normalizeProcessLifecycle(lifecycle)
	processesJSON := s.resolveProcessesJSON(ctx, tenantID, req.GetStreamId(), req.GetClusterId(), lifecycle)
	return &pb.GetTenantProcessesJSONResponse{ProcessesJson: processesJSON}, nil
}

// ResolveChapterPlaybackID maps a public chapter playback_id back to its
// internal (chapter_id, tenant_id, artifact_hash). Foghorn's playback
// resolver calls this to bridge the public ID into the artifact-hash
// path that handles policy walk + artifact serving.
func (s *CommodoreServer) ResolveChapterPlaybackID(ctx context.Context, req *pb.ResolveChapterPlaybackIDRequest) (*pb.ResolveChapterPlaybackIDResponse, error) {
	playbackID := req.GetPlaybackId()
	if playbackID == "" {
		return nil, status.Error(codes.InvalidArgument, "playback_id is required")
	}

	var (
		chapterID, artifactHash string
		tenantID                string
	)
	err := s.retryPostgres(ctx, func() error {
		return s.db.QueryRowContext(ctx, `
			SELECT chapter_id, tenant_id::text, artifact_hash
			  FROM commodore.dvr_chapter_playback
			 WHERE lower(playback_id::text) = lower($1)
		`, playbackID).Scan(&chapterID, &tenantID, &artifactHash)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return &pb.ResolveChapterPlaybackIDResponse{Found: false}, nil
	}
	if err != nil {
		s.logger.WithError(err).WithField("playback_id", playbackID).Error("Failed to resolve chapter playback id")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	return &pb.ResolveChapterPlaybackIDResponse{
		Found:        true,
		ChapterId:    chapterID,
		TenantId:     tenantID,
		ArtifactHash: artifactHash,
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
		requiresAuth         bool
	)
	err := s.retryPostgres(ctx, func() error {
		return s.db.QueryRowContext(ctx, `
			SELECT clip_hash, internal_name, tenant_id, user_id, stream_id::text, origin_cluster_id, requires_auth
			FROM commodore.clips
			WHERE playback_id = $1
		`, playbackID).Scan(&artifactHash, &artifactInternalName, &tenantID, &userID, &streamID, &originClusterID, &requiresAuth)
	})
	if err == nil {
		resp := &pb.ResolveArtifactPlaybackIDResponse{
			Found:           true,
			ArtifactHash:    artifactHash,
			InternalName:    artifactInternalName,
			TenantId:        tenantID,
			UserId:          userID,
			StreamId:        streamID.String,
			ContentType:     "clip",
			OriginClusterId: originClusterID.String,
			RequiresAuth:    requiresAuth,
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

	// 2. DVR — inherits requires_auth from the source stream at lookup time.
	// dvr_recordings has no requires_auth column; we LEFT JOIN streams to read
	// the source stream's marker. No row in streams (rare cleanup race) means
	// we treat as protected (fail closed) for safety.
	originClusterID = sql.NullString{}
	requiresAuth = false
	var dvrSourceRequiresAuth sql.NullBool
	err = s.retryPostgres(ctx, func() error {
		return s.db.QueryRowContext(ctx, `
			SELECT d.dvr_hash, d.internal_name, d.tenant_id, d.user_id, d.stream_id::text,
			       d.origin_cluster_id, s.requires_auth
			FROM commodore.dvr_recordings d
			LEFT JOIN commodore.streams s ON s.id = d.stream_id
			WHERE d.playback_id = $1
		`, playbackID).Scan(&artifactHash, &artifactInternalName, &tenantID, &userID, &streamID, &originClusterID, &dvrSourceRequiresAuth)
	})
	if err == nil {
		// Missing source stream → treat as protected so a deleted-stream race
		// does not silently expose what was once gated content.
		requiresAuth = !dvrSourceRequiresAuth.Valid || dvrSourceRequiresAuth.Bool
		resp := &pb.ResolveArtifactPlaybackIDResponse{
			Found:           true,
			ArtifactHash:    artifactHash,
			InternalName:    artifactInternalName,
			TenantId:        tenantID,
			UserId:          userID,
			StreamId:        streamID.String,
			ContentType:     "dvr",
			OriginClusterId: originClusterID.String,
			RequiresAuth:    requiresAuth,
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
	requiresAuth = false
	err = s.retryPostgres(ctx, func() error {
		return s.db.QueryRowContext(ctx, `
			SELECT vod_hash, internal_name, tenant_id, user_id, origin_cluster_id, requires_auth
			FROM commodore.vod_assets
			WHERE playback_id = $1
		`, playbackID).Scan(&artifactHash, &artifactInternalName, &tenantID, &userID, &originClusterID, &requiresAuth)
	})
	if err == nil {
		resp := &pb.ResolveArtifactPlaybackIDResponse{
			Found:           true,
			ArtifactHash:    artifactHash,
			InternalName:    artifactInternalName,
			TenantId:        tenantID,
			UserId:          userID,
			ContentType:     "vod",
			OriginClusterId: originClusterID.String,
			RequiresAuth:    requiresAuth,
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
	internalName := req.GetInternalName()
	if internalName == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name is required")
	}

	// 1. Clips
	var (
		artifactHash         string
		artifactInternalName string
		tenantID             string
		userID               string
		streamID             sql.NullString
		originClusterID      sql.NullString
		requiresAuth         bool
	)
	err := s.retryPostgres(ctx, func() error {
		return s.db.QueryRowContext(ctx, `
			SELECT clip_hash, internal_name, tenant_id, user_id, stream_id::text, origin_cluster_id, requires_auth
			FROM commodore.clips
			WHERE internal_name = $1
		`, internalName).Scan(&artifactHash, &artifactInternalName, &tenantID, &userID, &streamID, &originClusterID, &requiresAuth)
	})
	if err == nil {
		resp := &pb.ResolveArtifactInternalNameResponse{
			Found:           true,
			ArtifactHash:    artifactHash,
			InternalName:    artifactInternalName,
			TenantId:        tenantID,
			UserId:          userID,
			StreamId:        streamID.String,
			ContentType:     "clip",
			OriginClusterId: originClusterID.String,
			RequiresAuth:    requiresAuth,
		}
		s.populateArtifactClusterContext(ctx, tenantID, &resp.ClusterPeers)
		return resp, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"error":         err,
		}).Error("Database error resolving clip internal_name")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// 2. DVR
	originClusterID = sql.NullString{}
	var dvrSourceRequiresAuth sql.NullBool
	err = s.retryPostgres(ctx, func() error {
		return s.db.QueryRowContext(ctx, `
			SELECT d.dvr_hash, d.internal_name, d.tenant_id, d.user_id, d.stream_id::text,
			       d.origin_cluster_id, s.requires_auth
			FROM commodore.dvr_recordings d
			LEFT JOIN commodore.streams s ON s.id = d.stream_id
			WHERE d.internal_name = $1
		`, internalName).Scan(&artifactHash, &artifactInternalName, &tenantID, &userID, &streamID, &originClusterID, &dvrSourceRequiresAuth)
	})
	if err == nil {
		resp := &pb.ResolveArtifactInternalNameResponse{
			Found:           true,
			ArtifactHash:    artifactHash,
			InternalName:    artifactInternalName,
			TenantId:        tenantID,
			UserId:          userID,
			StreamId:        streamID.String,
			ContentType:     "dvr",
			OriginClusterId: originClusterID.String,
			RequiresAuth:    !dvrSourceRequiresAuth.Valid || dvrSourceRequiresAuth.Bool,
		}
		s.populateArtifactClusterContext(ctx, tenantID, &resp.ClusterPeers)
		return resp, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"error":         err,
		}).Error("Database error resolving DVR internal_name")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// 3. VOD
	streamID = sql.NullString{}
	originClusterID = sql.NullString{}
	err = s.retryPostgres(ctx, func() error {
		return s.db.QueryRowContext(ctx, `
			SELECT vod_hash, internal_name, tenant_id, user_id, origin_cluster_id, requires_auth
			FROM commodore.vod_assets
			WHERE internal_name = $1
		`, internalName).Scan(&artifactHash, &artifactInternalName, &tenantID, &userID, &originClusterID, &requiresAuth)
	})
	if err == nil {
		resp := &pb.ResolveArtifactInternalNameResponse{
			Found:           true,
			ArtifactHash:    artifactHash,
			InternalName:    artifactInternalName,
			TenantId:        tenantID,
			UserId:          userID,
			ContentType:     "vod",
			OriginClusterId: originClusterID.String,
			RequiresAuth:    requiresAuth,
		}
		s.populateArtifactClusterContext(ctx, tenantID, &resp.ClusterPeers)
		return resp, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"error":         err,
		}).Error("Database error resolving VOD internal_name")
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
		var requiresAuth bool
		err := s.retryPostgres(ctx, func() error {
			return s.db.QueryRowContext(ctx, `
				SELECT id, tenant_id, user_id, internal_name, is_recording_enabled, requires_auth
				FROM commodore.streams WHERE id = $1
			`, identifier).Scan(&streamID, &tenantID, &userID, &internalName, &isRecordingEnabled, &requiresAuth)
		})
		if err == nil {
			return &pb.ResolveIdentifierResponse{
				Found:              true,
				TenantId:           tenantID,
				UserId:             userID,
				InternalName:       internalName,
				IdentifierType:     "stream_id",
				IsRecordingEnabled: isRecordingEnabled,
				StreamId:           streamID,
				RequiresAuth:       requiresAuth,
			}, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			s.logger.WithError(err).Error("Database error checking streams by stream_id")
		}

		var vodTenantID, vodUserID string
		err = s.retryPostgres(ctx, func() error {
			return s.db.QueryRowContext(ctx, `
				SELECT tenant_id, user_id, requires_auth
				FROM commodore.vod_assets WHERE id = $1
			`, identifier).Scan(&vodTenantID, &vodUserID, &requiresAuth)
		})
		if err == nil {
			return &pb.ResolveIdentifierResponse{
				Found:          true,
				TenantId:       vodTenantID,
				UserId:         vodUserID,
				IdentifierType: "vod_id",
				RequiresAuth:   requiresAuth,
			}, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			s.logger.WithError(err).Error("Database error checking VOD by id")
		}
	}

	// 1. Try streams by internal_name (most common for live stream events)
	var streamID, tenantID, userID string
	var isRecordingEnabled bool
	var requiresAuth bool
	err := s.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, user_id, is_recording_enabled, requires_auth FROM commodore.streams WHERE internal_name = $1
	`, identifier).Scan(&streamID, &tenantID, &userID, &isRecordingEnabled, &requiresAuth)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:              true,
			TenantId:           tenantID,
			UserId:             userID,
			InternalName:       identifier,
			IdentifierType:     "stream",
			IsRecordingEnabled: isRecordingEnabled,
			StreamId:           streamID,
			RequiresAuth:       requiresAuth,
		}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).Error("Database error checking streams by internal_name")
	}

	// 2. Try streams by playback_id
	var internalName string
	err = s.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, user_id, internal_name, is_recording_enabled, requires_auth
		FROM commodore.streams WHERE playback_id = $1
	`, identifier).Scan(&streamID, &tenantID, &userID, &internalName, &isRecordingEnabled, &requiresAuth)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:              true,
			TenantId:           tenantID,
			UserId:             userID,
			InternalName:       internalName,
			IdentifierType:     "playback_id",
			IsRecordingEnabled: isRecordingEnabled,
			StreamId:           streamID,
			RequiresAuth:       requiresAuth,
		}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).Error("Database error checking streams by playback_id")
	}

	// 2b. Try artifact playback_id (clip)
	var parentInternalName sql.NullString
	var clipRequiresAuth bool
	err = s.db.QueryRowContext(ctx, `
		SELECT c.tenant_id, c.user_id, s.internal_name, c.stream_id, c.requires_auth
		FROM commodore.clips c
		LEFT JOIN commodore.streams s ON c.stream_id = s.id
		WHERE c.playback_id = $1
	`, identifier).Scan(&tenantID, &userID, &parentInternalName, &streamID, &clipRequiresAuth)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:          true,
			TenantId:       tenantID,
			UserId:         userID,
			InternalName:   parentInternalName.String,
			IdentifierType: "clip_playback_id",
			StreamId:       streamID,
			RequiresAuth:   clipRequiresAuth,
		}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).Error("Database error checking clips by playback_id")
	}

	// 2c. Try artifact playback_id (DVR)
	var dvrRequiresAuth sql.NullBool
	err = s.db.QueryRowContext(ctx, `
		SELECT d.tenant_id, d.user_id, d.internal_name, d.stream_id, s.requires_auth
		FROM commodore.dvr_recordings d
		LEFT JOIN commodore.streams s ON s.id = d.stream_id
		WHERE d.playback_id = $1
	`, identifier).Scan(&tenantID, &userID, &internalName, &streamID, &dvrRequiresAuth)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:          true,
			TenantId:       tenantID,
			UserId:         userID,
			InternalName:   internalName,
			IdentifierType: "dvr_playback_id",
			StreamId:       streamID,
			RequiresAuth:   !dvrRequiresAuth.Valid || dvrRequiresAuth.Bool,
		}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).Error("Database error checking DVR by playback_id")
	}

	// 2d. Try artifact playback_id (VOD)
	err = s.db.QueryRowContext(ctx, `
		SELECT tenant_id, user_id, requires_auth
		FROM commodore.vod_assets
		WHERE playback_id = $1
	`, identifier).Scan(&tenantID, &userID, &requiresAuth)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:          true,
			TenantId:       tenantID,
			UserId:         userID,
			IdentifierType: "vod_playback_id",
			RequiresAuth:   requiresAuth,
		}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).Error("Database error checking VOD by playback_id")
	}

	// 2e. Try artifact internal_name (clip)
	err = s.db.QueryRowContext(ctx, `
		SELECT c.tenant_id, c.user_id, s.internal_name, c.stream_id, c.requires_auth
		FROM commodore.clips c
		LEFT JOIN commodore.streams s ON c.stream_id = s.id
		WHERE c.internal_name = $1
	`, identifier).Scan(&tenantID, &userID, &parentInternalName, &streamID, &clipRequiresAuth)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:          true,
			TenantId:       tenantID,
			UserId:         userID,
			InternalName:   parentInternalName.String,
			IdentifierType: "clip_internal_name",
			StreamId:       streamID,
			RequiresAuth:   clipRequiresAuth,
		}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).Error("Database error checking clips by internal_name")
	}

	// 2f. Try artifact internal_name (DVR)
	dvrRequiresAuth = sql.NullBool{}
	err = s.db.QueryRowContext(ctx, `
		SELECT d.tenant_id, d.user_id, d.internal_name, d.stream_id, s.requires_auth
		FROM commodore.dvr_recordings d
		LEFT JOIN commodore.streams s ON s.id = d.stream_id
		WHERE d.internal_name = $1
	`, identifier).Scan(&tenantID, &userID, &internalName, &streamID, &dvrRequiresAuth)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:          true,
			TenantId:       tenantID,
			UserId:         userID,
			InternalName:   internalName,
			IdentifierType: "dvr_internal_name",
			StreamId:       streamID,
			RequiresAuth:   !dvrRequiresAuth.Valid || dvrRequiresAuth.Bool,
		}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).Error("Database error checking DVR by internal_name")
	}

	// 2g. Try artifact internal_name (VOD)
	err = s.db.QueryRowContext(ctx, `
		SELECT tenant_id, user_id, requires_auth
		FROM commodore.vod_assets
		WHERE internal_name = $1
	`, identifier).Scan(&tenantID, &userID, &requiresAuth)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:          true,
			TenantId:       tenantID,
			UserId:         userID,
			IdentifierType: "vod_internal_name",
			RequiresAuth:   requiresAuth,
		}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).Error("Database error checking VOD by internal_name")
	}

	// 3. Try clips by clip_hash
	err = s.db.QueryRowContext(ctx, `
		SELECT c.tenant_id, c.user_id, s.internal_name, c.stream_id, c.requires_auth
		FROM commodore.clips c
		LEFT JOIN commodore.streams s ON c.stream_id = s.id
		WHERE c.clip_hash = $1
	`, identifier).Scan(&tenantID, &userID, &parentInternalName, &streamID, &clipRequiresAuth)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:          true,
			TenantId:       tenantID,
			UserId:         userID,
			InternalName:   parentInternalName.String,
			IdentifierType: "clip",
			StreamId:       streamID,
			RequiresAuth:   clipRequiresAuth,
		}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).Error("Database error checking clips")
	}

	// 4. Try DVR by dvr_hash
	dvrRequiresAuth = sql.NullBool{}
	err = s.db.QueryRowContext(ctx, `
		SELECT d.tenant_id, d.user_id, d.internal_name, d.stream_id, s.requires_auth
		FROM commodore.dvr_recordings d
		LEFT JOIN commodore.streams s ON s.id = d.stream_id
		WHERE d.dvr_hash = $1
	`, identifier).Scan(&tenantID, &userID, &internalName, &streamID, &dvrRequiresAuth)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:          true,
			TenantId:       tenantID,
			UserId:         userID,
			InternalName:   internalName,
			IdentifierType: "dvr",
			StreamId:       streamID,
			RequiresAuth:   !dvrRequiresAuth.Valid || dvrRequiresAuth.Bool,
		}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).Error("Database error checking DVR")
	}

	// 5. Try VOD by vod_hash
	err = s.db.QueryRowContext(ctx, `
		SELECT tenant_id, user_id, requires_auth FROM commodore.vod_assets WHERE vod_hash = $1
	`, identifier).Scan(&tenantID, &userID, &requiresAuth)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:          true,
			TenantId:       tenantID,
			UserId:         userID,
			IdentifierType: "vod",
			RequiresAuth:   requiresAuth,
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
				return nil, status.Error(codes.PermissionDenied, "bot verification failed")
			}
		} else if !turnstileResp.Success {
			s.logger.WithFields(logging.Fields{
				"email":       email,
				"client_ip":   clientIP,
				"error_codes": turnstileResp.ErrorCodes,
			}).Warn("Login Turnstile verification failed")
			return nil, status.Error(codes.PermissionDenied, "bot verification failed")
		}
	} else {
		// Fallback: behavioral validation when Turnstile not configured
		if !validateBehavior(req) {
			s.logger.WithField("email", email).Warn("Login behavioral bot check failed")
			return nil, status.Error(codes.PermissionDenied, "bot verification failed")
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

	// Verify password
	if !auth.CheckPassword(password, user.PasswordHash) {
		s.emitAuthEvent(ctx, eventAuthLoginFailed, user.ID, user.TenantID, "password", "", "", "invalid_credentials")
		return nil, status.Error(codes.Unauthenticated, "invalid credentials")
	}

	// Check account status only after proving the password, so login does not
	// leak account state for incorrect credentials.
	if !user.IsActive {
		s.emitAuthEvent(ctx, eventAuthLoginFailed, user.ID, user.TenantID, "password", "", "", "account_inactive")
		return nil, status.Error(codes.Unauthenticated, "account deactivated")
	}
	if !user.IsVerified {
		s.emitAuthEvent(ctx, eventAuthLoginFailed, user.ID, user.TenantID, "password", "", "", "email_not_verified")
		return nil, status.Error(codes.Unauthenticated, "email not verified")
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
				return nil, status.Error(codes.PermissionDenied, "bot verification failed")
			}
		} else if !turnstileResp.Success {
			s.logger.WithFields(logging.Fields{
				"email":       email,
				"client_ip":   clientIP,
				"error_codes": turnstileResp.ErrorCodes,
			}).Warn("Turnstile verification failed")
			return nil, status.Error(codes.PermissionDenied, "bot verification failed")
		}
	} else {
		// Fallback: behavioral validation when Turnstile not configured
		if !validateBehavior(req) {
			s.logger.WithField("email", email).Warn("Behavioral bot check failed")
			return nil, status.Error(codes.PermissionDenied, "bot verification failed")
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
	var rotatedAt sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, tenant_id, revoked, rotated_at FROM commodore.refresh_tokens
		WHERE token_hash = $1 AND expires_at > NOW()
	`, tokenHash).Scan(&tokenID, &userID, &tenantID, &revoked, &rotatedAt)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.Unauthenticated, "invalid or expired refresh token")
	}
	if err != nil {
		s.logger.WithError(err).Error("Database error validating refresh token")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Near-simultaneous browser refreshes can reuse the just-rotated token.
	// Treat older reuse as a stolen-token signal and revoke the session family.
	if revoked {
		if rotatedAt.Valid && time.Since(rotatedAt.Time) <= refreshTokenReuseGracePeriod {
			s.logger.WithFields(logging.Fields{
				"user_id":    userID,
				"tenant_id":  tenantID,
				"rotated_at": rotatedAt.Time,
			}).Warn("Refresh token reuse detected during rotation grace period")
			return nil, status.Error(codes.Unauthenticated, "invalid or expired refresh token")
		}
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
	result, err := s.db.ExecContext(ctx, `
		UPDATE commodore.refresh_tokens SET revoked = true, rotated_at = NOW()
		WHERE id = $1 AND revoked = false
	`, tokenID)
	if err != nil {
		s.logger.WithError(err).Error("Failed to rotate refresh token")
		return nil, status.Errorf(codes.Internal, "failed to rotate refresh token: %v", err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr == nil && rows == 0 {
		s.logger.WithFields(logging.Fields{
			"user_id":   userID,
			"tenant_id": tenantID,
		}).Warn("Refresh token was already rotated")
		return nil, status.Error(codes.Unauthenticated, "invalid or expired refresh token")
	}

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

// ============================================================================
// BROWSER-HANDOFF + DEVICE LOGIN (Tray / CLI)
// ============================================================================
// The tray uses RFC 7636 PKCE over a RFC 8252 loopback redirect. The CLI uses
// the RFC 8628 Device Authorization Grant. Both flows return the same
// AuthResponse shape as Login — short-lived access token + refresh token —
// so the native client holds a real user session, not a long-lived API key.
//
// Identity-bearing fields (user_id, tenant_id) on session-protected RPCs are
// sourced from the gateway's verified JWT context, never from the client body.
// ============================================================================

const (
	authorizationCodeTTL   = 10 * time.Minute
	deviceCodeTTL          = 10 * time.Minute
	deviceCodePollInterval = 5 * time.Second
)

// validateAuthorizationClient checks that the (client_id, redirect_uri) pair
// is one of the known native-client configurations. Fails closed for any
// unknown client_id so callers can't request tokens for clients we don't
// recognize.
func validateAuthorizationClient(clientID, redirectURI string) error {
	switch clientID {
	case "tray-mac":
		u, err := url.Parse(redirectURI)
		if err != nil || u.Scheme != "http" {
			return status.Error(codes.InvalidArgument, "redirect_uri must be an http loopback URL")
		}
		host := u.Hostname()
		if host != "127.0.0.1" && host != "::1" {
			return status.Error(codes.InvalidArgument, "redirect_uri host must be 127.0.0.1 or ::1")
		}
		if u.Path != "/callback" {
			return status.Error(codes.InvalidArgument, "redirect_uri path must be /callback")
		}
		return nil
	default:
		return status.Error(codes.PermissionDenied, "unknown client_id")
	}
}

// CompleteAuthorization persists a single-use PKCE authorization code bound
// to the caller's code_challenge. Called by the gateway on behalf of the
// webapp /authorize page after the signed-in user approves the request.
func (s *CommodoreServer) CompleteAuthorization(ctx context.Context, req *pb.CompleteAuthorizationRequest) (*pb.CompleteAuthorizationResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetClientId() == "" || req.GetRedirectUri() == "" || req.GetCodeChallenge() == "" {
		return nil, status.Error(codes.InvalidArgument, "client_id, redirect_uri and code_challenge required")
	}
	if req.GetCodeChallengeMethod() != "S256" {
		return nil, status.Error(codes.InvalidArgument, "code_challenge_method must be S256")
	}
	if validateErr := validateAuthorizationClient(req.GetClientId(), req.GetRedirectUri()); validateErr != nil {
		return nil, validateErr
	}

	scope := req.GetScope()
	if scope == "" {
		scope = "account"
	}

	code, err := generateSecureToken(32)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate authorization code: %v", err)
	}
	codeHash := hashToken(code)
	expiresAt := time.Now().Add(authorizationCodeTTL)

	var state sql.NullString
	if reqState := req.GetState(); reqState != "" {
		state = sql.NullString{String: reqState, Valid: true}
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO commodore.auth_authorization_codes
			(tenant_id, user_id, client_id, code_hash, code_challenge, code_challenge_method,
			 redirect_uri, scope, state, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, tenantID, userID, req.GetClientId(), codeHash,
		req.GetCodeChallenge(), req.GetCodeChallengeMethod(),
		req.GetRedirectUri(), scope, state, expiresAt)
	if err != nil {
		s.logger.WithError(err).Error("Failed to persist authorization code")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	return &pb.CompleteAuthorizationResponse{
		Code:      code,
		ExpiresAt: timestamppb.New(expiresAt),
	}, nil
}

// ExchangeAuthorizationCode redeems a one-time PKCE authorization code for a
// session (access + refresh tokens). The code_verifier is hashed with SHA-256
// and constant-time compared against the stored code_challenge. The code is
// marked consumed in the same transaction that issues the refresh token, so
// a successful exchange is atomic and a second exchange returns AlreadyExists.
func (s *CommodoreServer) ExchangeAuthorizationCode(ctx context.Context, req *pb.ExchangeAuthorizationCodeRequest) (*pb.AuthResponse, error) {
	if req.GetCode() == "" || req.GetCodeVerifier() == "" || req.GetClientId() == "" || req.GetRedirectUri() == "" {
		return nil, status.Error(codes.InvalidArgument, "code, code_verifier, client_id and redirect_uri required")
	}

	codeHash := hashToken(req.GetCode())

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to begin transaction: %v", err)
	}
	defer s.rollbackTx(tx)

	var rowID, userID, tenantID string
	var storedChallenge, challengeMethod, storedClientID, storedRedirectURI string
	var consumedAt sql.NullTime
	err = tx.QueryRowContext(ctx, `
		SELECT id, user_id, tenant_id, code_challenge, code_challenge_method,
		       client_id, redirect_uri, consumed_at
		FROM commodore.auth_authorization_codes
		WHERE code_hash = $1 AND expires_at > NOW()
		FOR UPDATE
	`, codeHash).Scan(&rowID, &userID, &tenantID, &storedChallenge, &challengeMethod,
		&storedClientID, &storedRedirectURI, &consumedAt)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.Unauthenticated, "invalid or expired authorization code")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if consumedAt.Valid {
		return nil, status.Error(codes.AlreadyExists, "authorization code already used")
	}
	if storedClientID != req.GetClientId() || storedRedirectURI != req.GetRedirectUri() {
		return nil, status.Error(codes.PermissionDenied, "client_id or redirect_uri mismatch")
	}
	if challengeMethod != "S256" {
		return nil, status.Error(codes.Internal, "unsupported code_challenge_method")
	}

	h := sha256.Sum256([]byte(req.GetCodeVerifier()))
	computed := base64.RawURLEncoding.EncodeToString(h[:])
	if subtle.ConstantTimeCompare([]byte(computed), []byte(storedChallenge)) != 1 {
		return nil, status.Error(codes.PermissionDenied, "code_verifier mismatch")
	}

	if _, execErr := tx.ExecContext(ctx, `
		UPDATE commodore.auth_authorization_codes
		SET consumed_at = NOW() WHERE id = $1
	`, rowID); execErr != nil {
		return nil, status.Errorf(codes.Internal, "failed to mark code consumed: %v", execErr)
	}

	resp, err := s.issueUserSessionTx(ctx, tx, userID, tenantID, "pkce")
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to commit: %v", err)
	}
	return resp, nil
}

func (s *CommodoreServer) rollbackTx(tx *sql.Tx) {
	if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
		s.logger.WithError(rollbackErr).Debug("transaction rollback failed")
	}
}

// issueUserSessionTx issues a new access + refresh token pair for the given
// user inside an open transaction. The caller is responsible for committing.
// Returns the same AuthResponse shape as Login.
func (s *CommodoreServer) issueUserSessionTx(ctx context.Context, tx *sql.Tx, userID, tenantID, authType string) (*pb.AuthResponse, error) {
	var user commodoreUserRecord
	err := scanCommodoreUserForRefresh(tx.QueryRowContext(ctx, `
		SELECT email, role, permissions, first_name, last_name, is_active, verified, created_at, updated_at
		FROM commodore.users WHERE id = $1 AND tenant_id = $2
	`, userID, tenantID), &user)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.Unauthenticated, "user not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !user.IsActive {
		return nil, status.Error(codes.Unauthenticated, "account deactivated")
	}

	jwtSecret := []byte(config.RequireEnv("JWT_SECRET"))
	token, err := auth.GenerateJWT(userID, tenantID, user.Email, user.Role, jwtSecret)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate token: %v", err)
	}

	refreshToken, err := generateRandomString(40)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate refresh token: %v", err)
	}
	refreshHash := hashToken(refreshToken)
	refreshExpiry := time.Now().Add(30 * 24 * time.Hour)

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO commodore.refresh_tokens (tenant_id, user_id, token_hash, expires_at)
		VALUES ($1, $2, $3, $4)
	`, tenantID, userID, refreshHash, refreshExpiry); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to store refresh token: %v", err)
	}

	expiresAt := time.Now().Add(15 * time.Minute)
	s.emitAuthEvent(ctx, eventAuthLoginSucceeded, userID, tenantID, authType, "", "", "")

	return &pb.AuthResponse{
		Token:        token,
		RefreshToken: refreshToken,
		User:         user.toProtoUser(userID, tenantID),
		ExpiresAt:    timestamppb.New(expiresAt),
	}, nil
}

// Crockford-style base32 alphabet (drops I, L, O, U) so user_codes can be
// read aloud without ambiguity. 32 chars = no modulo bias from random bytes.
var userCodeEncoding = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// generateUserCode returns an 8-character dash-formatted code (e.g. "9XKM-3PNZ").
func generateUserCode() (string, error) {
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	enc := userCodeEncoding.EncodeToString(b[:])
	if len(enc) < 8 {
		return "", fmt.Errorf("unexpected user_code length %d", len(enc))
	}
	return enc[:4] + "-" + enc[4:8], nil
}

// normalizeUserCode strips non-alphanumeric characters, uppercases, and
// re-inserts the canonical dash so a user typing "abcd efgh" or "abcdefgh"
// matches the stored "ABCD-EFGH".
func normalizeUserCode(input string) string {
	var clean strings.Builder
	for _, r := range strings.ToUpper(input) {
		if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') {
			clean.WriteRune(r)
		}
	}
	s := clean.String()
	if len(s) != 8 {
		return ""
	}
	return s[:4] + "-" + s[4:]
}

// StartDeviceAuthorization initiates a device-code grant for a CLI/headless
// client. Returns a (device_code, user_code) pair plus the verification URL.
// No session required — the user authenticates in a browser at /device.
func (s *CommodoreServer) StartDeviceAuthorization(ctx context.Context, req *pb.StartDeviceAuthorizationRequest) (*pb.StartDeviceAuthorizationResponse, error) {
	clientID := req.GetClientId()
	if clientID == "" {
		return nil, status.Error(codes.InvalidArgument, "client_id required")
	}
	// Known device-grant clients. Add new ones explicitly; fail closed for unknowns.
	if clientID != "cli" && clientID != "tray-mac" {
		return nil, status.Error(codes.PermissionDenied, "unknown client_id")
	}

	scope := req.GetScope()
	if scope == "" {
		scope = "account"
	}

	deviceCode, err := generateSecureToken(32)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate device_code: %v", err)
	}
	deviceCodeHash := hashToken(deviceCode)
	expiresAt := time.Now().Add(deviceCodeTTL)

	// Retry up to 5 times on user_code unique violation. Collision odds at
	// 32^8 are vanishingly small, but be defensive in case of clock skew /
	// long-lived pending codes.
	var userCode string
	const maxAttempts = 5
	for range maxAttempts {
		userCode, err = generateUserCode()
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to generate user_code: %v", err)
		}
		_, err = s.db.ExecContext(ctx, `
			INSERT INTO commodore.auth_device_codes
				(client_id, device_code_hash, user_code, scope, status,
				 poll_interval_seconds, expires_at)
			VALUES ($1, $2, $3, $4, 'pending', $5, $6)
		`, clientID, deviceCodeHash, userCode, scope,
			int(deviceCodePollInterval.Seconds()), expiresAt)
		if err == nil {
			break
		}
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" {
			continue
		}
		return nil, status.Errorf(codes.Internal, "failed to persist device_code: %v", err)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to allocate unique user_code: %v", err)
	}

	verificationURI, err := s.deviceVerificationBaseURL()
	if err != nil {
		return nil, err
	}
	verificationURIComplete := verificationURI + "?user_code=" + url.QueryEscape(userCode)

	return &pb.StartDeviceAuthorizationResponse{
		DeviceCode:              deviceCode,
		UserCode:                userCode,
		VerificationUri:         verificationURI,
		VerificationUriComplete: verificationURIComplete,
		ExpiresInSeconds:        int32(deviceCodeTTL.Seconds()),
		IntervalSeconds:         int32(deviceCodePollInterval.Seconds()),
	}, nil
}

// deviceVerificationBaseURL returns the URL the user visits to approve a
// device code.
func (s *CommodoreServer) deviceVerificationBaseURL() (string, error) {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("DEVICE_VERIFICATION_URL")), "/"); v != "" {
		return v, nil
	}
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("WEBAPP_PUBLIC_URL")), "/")
	if baseURL == "" {
		return "", status.Error(codes.FailedPrecondition, "WEBAPP_PUBLIC_URL required")
	}
	return baseURL + "/device", nil
}

// PollDeviceAuthorization is called by the CLI on the returned interval. While
// the user_code is unapproved, returns one of the RFC 8628 §3.5 markers:
// AUTHORIZATION_PENDING, SLOW_DOWN, ACCESS_DENIED, EXPIRED_TOKEN. On approval
// returns a normal AuthResponse and consumes the device_code row.
func (s *CommodoreServer) PollDeviceAuthorization(ctx context.Context, req *pb.PollDeviceAuthorizationRequest) (*pb.AuthResponse, error) {
	if req.GetDeviceCode() == "" || req.GetClientId() == "" {
		return nil, status.Error(codes.InvalidArgument, "device_code and client_id required")
	}

	deviceCodeHash := hashToken(req.GetDeviceCode())

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to begin transaction: %v", err)
	}
	defer s.rollbackTx(tx)

	var rowID, storedClientID, dbStatus string
	var userID, tenantID sql.NullString
	var expiresAt time.Time
	var lastPolledAt sql.NullTime
	var pollInterval int
	err = tx.QueryRowContext(ctx, `
		SELECT id, client_id, status, user_id, tenant_id, expires_at, last_polled_at, poll_interval_seconds
		FROM commodore.auth_device_codes
		WHERE device_code_hash = $1
		FOR UPDATE
	`, deviceCodeHash).Scan(&rowID, &storedClientID, &dbStatus, &userID, &tenantID,
		&expiresAt, &lastPolledAt, &pollInterval)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.PermissionDenied, "ACCESS_DENIED")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if storedClientID != req.GetClientId() {
		return nil, status.Error(codes.PermissionDenied, "ACCESS_DENIED")
	}

	now := time.Now()
	if now.After(expiresAt) || dbStatus == "expired" {
		if _, execErr := tx.ExecContext(ctx, `UPDATE commodore.auth_device_codes SET status = 'expired' WHERE id = $1`, rowID); execErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to expire device_code: %v", execErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to commit: %v", commitErr)
		}
		return nil, status.Error(codes.FailedPrecondition, "EXPIRED_TOKEN")
	}
	if dbStatus == "denied" {
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to commit: %v", commitErr)
		}
		return nil, status.Error(codes.PermissionDenied, "ACCESS_DENIED")
	}
	if dbStatus == "pending" {
		// SLOW_DOWN: client polled before its returned interval elapsed.
		if lastPolledAt.Valid && now.Sub(lastPolledAt.Time) < time.Duration(pollInterval)*time.Second {
			if _, execErr := tx.ExecContext(ctx, `UPDATE commodore.auth_device_codes SET last_polled_at = NOW() WHERE id = $1`, rowID); execErr != nil {
				return nil, status.Errorf(codes.Internal, "failed to record poll: %v", execErr)
			}
			if commitErr := tx.Commit(); commitErr != nil {
				return nil, status.Errorf(codes.Internal, "failed to commit: %v", commitErr)
			}
			return nil, status.Error(codes.FailedPrecondition, "SLOW_DOWN")
		}
		if _, execErr := tx.ExecContext(ctx, `UPDATE commodore.auth_device_codes SET last_polled_at = NOW() WHERE id = $1`, rowID); execErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to record poll: %v", execErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to commit: %v", commitErr)
		}
		return nil, status.Error(codes.FailedPrecondition, "AUTHORIZATION_PENDING")
	}
	if dbStatus != "approved" || !userID.Valid || !tenantID.Valid {
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to commit: %v", commitErr)
		}
		return nil, status.Error(codes.FailedPrecondition, "AUTHORIZATION_PENDING")
	}

	// Approved — issue session and consume the row (DELETE so a re-poll
	// returns ACCESS_DENIED on missing row).
	resp, err := s.issueUserSessionTx(ctx, tx, userID.String, tenantID.String, "device_code")
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM commodore.auth_device_codes WHERE id = $1`, rowID); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to consume device_code: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to commit: %v", err)
	}
	return resp, nil
}

// LookupDeviceAuthorization returns pending device-code metadata for the
// consent page without approving it.
func (s *CommodoreServer) LookupDeviceAuthorization(ctx context.Context, req *pb.LookupDeviceAuthorizationRequest) (*pb.LookupDeviceAuthorizationResponse, error) {
	if _, _, err := extractUserContext(ctx); err != nil {
		return nil, err
	}
	normalized := normalizeUserCode(req.GetUserCode())
	if normalized == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid user_code")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to begin transaction: %v", err)
	}
	defer s.rollbackTx(tx)

	var rowID, clientID, scope, dbStatus string
	var expiresAt time.Time
	err = tx.QueryRowContext(ctx, `
		SELECT id, client_id, scope, status, expires_at
		FROM commodore.auth_device_codes
		WHERE user_code = $1
		FOR UPDATE
	`, normalized).Scan(&rowID, &clientID, &scope, &dbStatus, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "user_code not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	if time.Now().After(expiresAt) {
		if _, execErr := tx.ExecContext(ctx, `UPDATE commodore.auth_device_codes SET status = 'expired' WHERE id = $1`, rowID); execErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to expire device_code: %v", execErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to commit: %v", commitErr)
		}
		return nil, status.Error(codes.FailedPrecondition, "user_code expired")
	}
	if dbStatus != "pending" {
		return nil, status.Error(codes.FailedPrecondition, "user_code already resolved")
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, status.Errorf(codes.Internal, "failed to commit: %v", commitErr)
	}

	return &pb.LookupDeviceAuthorizationResponse{
		ClientId:  clientID,
		Scope:     scope,
		ExpiresAt: timestamppb.New(expiresAt),
	}, nil
}

// ApproveDeviceAuthorization marks a pending device-code row as approved and
// stamps the calling user's identity onto it. Called by the gateway on behalf
// of the webapp /device page after the signed-in user confirms the displayed
// user_code. user_id / tenant_id MUST come from the gateway session.
func (s *CommodoreServer) ApproveDeviceAuthorization(ctx context.Context, req *pb.ApproveDeviceAuthorizationRequest) (*pb.ApproveDeviceAuthorizationResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	normalized := normalizeUserCode(req.GetUserCode())
	if normalized == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid user_code")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to begin transaction: %v", err)
	}
	defer s.rollbackTx(tx)

	var rowID, clientID, dbStatus string
	var expiresAt time.Time
	err = tx.QueryRowContext(ctx, `
		SELECT id, client_id, status, expires_at
		FROM commodore.auth_device_codes
		WHERE user_code = $1
		FOR UPDATE
	`, normalized).Scan(&rowID, &clientID, &dbStatus, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "user_code not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	if time.Now().After(expiresAt) {
		if _, execErr := tx.ExecContext(ctx, `UPDATE commodore.auth_device_codes SET status = 'expired' WHERE id = $1`, rowID); execErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to expire device_code: %v", execErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to commit: %v", commitErr)
		}
		return nil, status.Error(codes.FailedPrecondition, "user_code expired")
	}
	if dbStatus != "pending" {
		return nil, status.Error(codes.FailedPrecondition, "user_code already resolved")
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE commodore.auth_device_codes
		SET user_id = $1, tenant_id = $2, status = 'approved', approved_at = NOW()
		WHERE id = $3
	`, userID, tenantID, rowID); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to approve device_code: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to commit: %v", err)
	}

	return &pb.ApproveDeviceAuthorizationResponse{
		Success:  true,
		ClientId: clientID,
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
				return nil, status.Error(codes.PermissionDenied, "bot verification failed")
			}
		} else if !turnstileResp.Success {
			return nil, status.Error(codes.PermissionDenied, "bot verification failed")
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
	args := []any{}
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
		s.logger.WithError(err).WithField("email", email.String).Warn("Failed to get subscriber from Listmonk")
		return &pb.GetNewsletterStatusResponse{Subscribed: false}, nil
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
	ingestMode := normalizeIngestMode(req.GetIngestMode())
	if ingestMode == "" {
		ingestMode = "push"
	}
	var pullClass pullsource.Class
	var pullAllowedClusterIDs []string
	if ingestMode == "pull" {
		if req.GetPullSource() == nil || strings.TrimSpace(req.GetPullSource().GetSourceUri()) == "" {
			return nil, status.Error(codes.InvalidArgument, "pull_source.source_uri required for pull streams")
		}
		// CreateStream: unwrap allowed_clusters; nil ⇒ no pin (rejected later
		// by FilterPlacementClusters for private/multicast classes).
		var requestedAllowed []string
		if w := req.GetPullSource().GetAllowedClusters(); w != nil {
			requestedAllowed = w.GetClusterIds()
		}
		pullClass, pullAllowedClusterIDs, err = s.validatePullSourceEligibility(ctx, req.GetPullSource().GetSourceUri(), requestedAllowed)
		if err != nil {
			return nil, err
		}
	} else if ingestMode != "push" {
		return nil, status.Errorf(codes.InvalidArgument, "unsupported ingest_mode %q", req.GetIngestMode())
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to begin transaction: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort after Commit

	// Keep stream creation and requested initial state atomic. Pull streams
	// must not leak as push streams if source persistence fails.
	var streamID, streamKey, playbackID, internalName string
	err = tx.QueryRowContext(ctx, `
			SELECT stream_id, stream_key, playback_id, internal_name
			FROM commodore.create_user_stream($1, $2, $3)
		`, tenantID, userID, title).Scan(&streamID, &streamKey, &playbackID, &internalName)

	if err != nil {
		s.logger.WithError(err).Error("Failed to create stream")
		return nil, status.Errorf(codes.Internal, "failed to create stream: %v", err)
	}
	if ingestMode == "pull" {
		var encURI string
		encURI, err = s.pullSourceEncryptor.Encrypt(strings.TrimSpace(req.GetPullSource().GetSourceUri()))
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to encrypt pull source: %v", err)
		}
		_, err = tx.ExecContext(ctx, `
				UPDATE commodore.streams
				SET ingest_mode = 'pull', updated_at = NOW()
				WHERE id = $1::uuid AND tenant_id = $2::uuid;
			INSERT INTO commodore.stream_pull_sources
				(stream_id, source_uri_enc, enabled, allowed_cluster_ids, created_at, updated_at)
			VALUES ($1::uuid, $3, $4, $5, NOW(), NOW())
		`, streamID, tenantID, encURI, pullSourceEnabled(req.GetPullSource()), pq.Array(pullAllowedClusterIDs))
		if err != nil {
			s.logger.WithError(err).WithField("stream_id", streamID).Error("Failed to persist pull source")
			return nil, status.Errorf(codes.Internal, "failed to persist pull source: %v", err)
		}
	}

	// Update description if provided
	if req.GetDescription() != "" {
		_, err = tx.ExecContext(ctx, `
				UPDATE commodore.streams SET description = $1 WHERE id = $2
			`, req.GetDescription(), streamID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to update stream description: %v", err)
		}
	}

	// Update recording setting if requested
	if req.GetIsRecording() {
		_, err = tx.ExecContext(ctx, `
				UPDATE commodore.streams SET is_recording_enabled = true WHERE id = $1
			`, streamID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to enable recording: %v", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to commit stream creation: %v", err)
	}

	changedFields := []string{"title"}
	if req.GetDescription() != "" {
		changedFields = append(changedFields, "description")
	}
	if req.GetIsRecording() {
		changedFields = append(changedFields, "is_recording_enabled")
	}
	if ingestMode == "pull" {
		changedFields = append(changedFields, "ingest_mode", "pull_source")
	}
	s.emitStreamChangeEvent(ctx, eventStreamCreated, tenantID, userID, streamID, changedFields)

	resp := &pb.CreateStreamResponse{
		Id:          streamID,
		StreamKey:   streamKey,
		PlaybackId:  playbackID,
		Title:       title,
		Description: req.GetDescription(),
		Status:      "offline",
		IngestMode:  ingestMode,
	}
	if ingestMode == "pull" {
		resp.PullSource = buildPullSourceView(req.GetPullSource().GetSourceUri(), pullSourceEnabled(req.GetPullSource()), pullClass, pullAllowedClusterIDs)
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

	// Add global root and tenant alias domain fields.
	s.populateTieredDomains(ctx, tenantID, resp)

	return resp, nil
}

// populateTieredDomains adds the three-tier URL surface to a
// CreateStreamResponse: global root entrypoints (default for free /
// platform-official tier) and per-tenant alias entrypoints (paid tier
// with active alias). Cluster-concrete fields are populated upstream;
// this function leaves them alone.
func (s *CommodoreServer) populateTieredDomains(ctx context.Context, tenantID string, resp *pb.CreateStreamResponse) {
	rootDomain := strings.TrimSpace(os.Getenv("PLATFORM_ROOT_DOMAIN"))
	if rootDomain == "" {
		rootDomain = strings.TrimSpace(os.Getenv("BRAND_DOMAIN"))
	}
	if rootDomain == "" {
		return
	}
	// Global root entrypoints: always populated when configured.
	gIngest := "edge-ingest." + rootDomain
	gEdge := "edge-egress." + rootDomain
	gPlay := "foghorn." + rootDomain
	gChandler := "chandler." + rootDomain
	gLivepeer := "livepeer." + rootDomain
	resp.GlobalIngestDomain = &gIngest
	resp.GlobalEdgeDomain = &gEdge
	resp.GlobalPlayDomain = &gPlay
	resp.GlobalChandlerDomain = &gChandler
	resp.GlobalLivepeerDomain = &gLivepeer

	// Tenant alias entrypoints are only safe once Navigator has at
	// least one DNS member published for the alias. A cert_issued row
	// alone only means ACME finished.
	if s.navigatorClient == nil {
		return
	}
	aliasCtx, aliasCancel := context.WithTimeout(ctx, 2*time.Second)
	defer aliasCancel()
	aliasResp, aliasErr := s.navigatorClient.GetTenantAliasStatus(aliasCtx, &pb.GetTenantAliasStatusRequest{TenantId: tenantID})
	if aliasErr != nil || aliasResp == nil || !aliasResp.GetFound() || aliasResp.GetStatus() != "cert_issued" || !aliasResp.GetDnsReady() {
		return
	}
	tenantZone := pkgdns.TenantAliasZoneLabel + "." + rootDomain
	apex := aliasResp.GetSubdomain() + "." + tenantZone
	tIngest := "edge-ingest." + apex
	tEdge := "edge-egress." + apex
	tPlay := "foghorn." + apex
	tChandler := "chandler." + apex
	tLivepeer := "livepeer." + apex
	resp.TenantIngestDomain = &tIngest
	resp.TenantEdgeDomain = &tEdge
	resp.TenantPlayDomain = &tPlay
	resp.TenantChandlerDomain = &tChandler
	resp.TenantLivepeerDomain = &tLivepeer
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
		TimestampColumn: "s.created_at",
		IDColumn:        "s.id",
	}

	// Base query
	query := `
		SELECT s.id, s.internal_name, s.stream_key, s.playback_id, s.title, s.description,
		       s.is_recording_enabled, s.created_at, s.updated_at, s.ingest_mode,
		       p.source_uri_enc, p.enabled, COALESCE(p.allowed_cluster_ids, '{}'),
		       s.active_ingest_cluster_id,
		       s.dvr_retention_days_override, s.clip_retention_days_override
		FROM commodore.streams s
		LEFT JOIN commodore.stream_pull_sources p ON p.stream_id = s.id
		WHERE s.user_id = $1 AND s.tenant_id = $2`
	args := []any{userID, tenantID}
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
	var route *clusterRoute
	routeAttempted := false
	for rows.Next() {
		stream, err := s.scanStream(rows)
		if err != nil {
			s.logger.WithError(err).Warn("Error scanning stream")
			continue
		}
		if !routeAttempted {
			routeAttempted = true
			if resolved, routeErr := s.resolveClusterRouteForTenant(ctx, tenantID); routeErr == nil {
				route = resolved
			}
		}
		if route != nil {
			s.populateStreamOriginRegion(ctx, tenantID, stream, route)
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

	// Verify ownership and fetch immutable ingest mode for validation.
	var internalName, currentIngestMode string
	var currentRecordingEnabled bool
	err = s.db.QueryRowContext(ctx, `
		SELECT internal_name, ingest_mode, is_recording_enabled FROM commodore.streams WHERE id = $1 AND user_id = $2 AND tenant_id = $3
	`, streamID, userID, tenantID).Scan(&internalName, &currentIngestMode, &currentRecordingEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "stream not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if req.IngestMode != nil {
		requestedMode := normalizeIngestMode(req.GetIngestMode())
		if requestedMode == "" {
			requestedMode = "push"
		}
		if requestedMode != currentIngestMode {
			return nil, status.Error(codes.InvalidArgument, "ingest_mode cannot be changed after stream creation")
		}
	}

	pullSource := req.GetPullSource()
	// Pull-source update intent: we resolve the target state by per-field
	// preserve-or-replace, so the gRPC + GraphQL surface can express "only
	// change enabled" without wiping placement, "only repin clusters" without
	// touching the URI, etc.
	type pullSourceWritePlan struct {
		writeURI        bool
		encryptedURI    string
		writeEnabled    bool
		enabledValue    bool
		writeAllowed    bool
		allowedClusters []string
	}
	var pullPlan pullSourceWritePlan
	if pullSource != nil {
		if currentIngestMode != "pull" {
			return nil, status.Error(codes.InvalidArgument, "pull_source can only be updated on pull streams")
		}

		// Load current pull-source row once so every "field unset = preserve"
		// branch can fall back to a real stored value.
		currentURI, currentEnabled, currentAllowed, loadErr := s.loadPullSourceState(ctx, streamID, userID, tenantID)
		if loadErr != nil {
			return nil, loadErr
		}

		rawSourceURI := strings.TrimSpace(pullSource.GetSourceUri())
		sourceURIChanged := rawSourceURI != ""
		newURI := currentURI
		if sourceURIChanged {
			newURI = rawSourceURI
		}

		newEnabled := currentEnabled
		enabledChanged := false
		if pullSource.Enabled != nil && pullSource.GetEnabled() != currentEnabled {
			newEnabled = pullSource.GetEnabled()
			enabledChanged = true
		}

		newAllowed := currentAllowed
		allowedChanged := false
		if w := pullSource.GetAllowedClusters(); w != nil {
			newAllowed = w.GetClusterIds()
			allowedChanged = true
		}

		// Re-validate placement only when URI or pin actually changes. A
		// pure enabled toggle never re-runs Quartermaster lookups.
		if sourceURIChanged || allowedChanged {
			_, normalized, vErr := s.validatePullSourceEligibility(ctx, newURI, newAllowed)
			if vErr != nil {
				return nil, vErr
			}
			newAllowed = normalized
		}

		if sourceURIChanged {
			encURI, encErr := s.pullSourceEncryptor.Encrypt(newURI)
			if encErr != nil {
				return nil, status.Errorf(codes.Internal, "failed to encrypt pull source: %v", encErr)
			}
			pullPlan.writeURI = true
			pullPlan.encryptedURI = encURI
		}
		if enabledChanged {
			pullPlan.writeEnabled = true
			pullPlan.enabledValue = newEnabled
		}
		if allowedChanged {
			pullPlan.writeAllowed = true
			pullPlan.allowedClusters = newAllowed
		}
	}

	// Build update query dynamically
	var updates []string
	var args []any
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
	// Cross-field validation: fixed_interval requires interval >= 3600s.
	// Sub-hour intervals would explode finalization-job count and storage
	// churn; the DB CHECK enforces the floor but rejecting here gives a
	// clean InvalidArgument surface to callers.
	const minChapterIntervalSeconds int32 = 3600
	if req.DvrChapterIntervalSeconds != nil && req.GetDvrChapterIntervalSeconds() > 0 &&
		req.GetDvrChapterIntervalSeconds() < minChapterIntervalSeconds {
		return nil, status.Errorf(codes.InvalidArgument,
			"dvr_chapter_interval_seconds must be >= %d (1 hour minimum)", minChapterIntervalSeconds)
	}
	if req.DvrChapterMode != nil {
		normalized := strings.ToLower(strings.TrimSpace(req.GetDvrChapterMode()))
		if normalized == "fixed_interval" {
			// Interval must come in this request at >= the floor or
			// already be on the row at >= the floor.
			supplied := req.DvrChapterIntervalSeconds != nil && req.GetDvrChapterIntervalSeconds() >= minChapterIntervalSeconds
			if !supplied {
				var existing sql.NullInt32
				lookupErr := s.db.QueryRowContext(ctx,
					`SELECT dvr_chapter_interval_seconds FROM commodore.streams WHERE id = $1::uuid AND tenant_id = $2::uuid`,
					streamID, tenantID,
				).Scan(&existing)
				if lookupErr != nil || !existing.Valid || existing.Int32 < minChapterIntervalSeconds {
					return nil, status.Errorf(codes.InvalidArgument,
						"dvr_chapter_mode='fixed_interval' requires dvr_chapter_interval_seconds >= %d", minChapterIntervalSeconds)
				}
			}
		}
	}
	if req.DvrChapterMode != nil {
		// Empty/NONE → set NULL so the CHECK constraint accepts no-chapter
		// streams. Validated values land verbatim; the CHECK enforces the
		// allowed set on write.
		var modeArg any
		if mode := strings.TrimSpace(req.GetDvrChapterMode()); mode != "" && strings.ToLower(mode) != "none" {
			modeArg = mode
		}
		updates = append(updates, fmt.Sprintf("dvr_chapter_mode = $%d", argIdx))
		args = append(args, modeArg)
		argIdx++
		changedFields = append(changedFields, "dvr_chapter_mode")
	}
	if req.DvrChapterIntervalSeconds != nil {
		var ivArg any
		if iv := req.GetDvrChapterIntervalSeconds(); iv > 0 {
			ivArg = iv
		}
		updates = append(updates, fmt.Sprintf("dvr_chapter_interval_seconds = $%d", argIdx))
		args = append(args, ivArg)
		argIdx++
		changedFields = append(changedFields, "dvr_chapter_interval_seconds")
	}

	if len(updates) == 0 && pullSource == nil {
		return s.queryStream(ctx, streamID, userID, tenantID)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to begin transaction: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort after Commit

	if len(updates) > 0 {
		updates = append(updates, "updated_at = NOW()")
		query := fmt.Sprintf("UPDATE commodore.streams SET %s WHERE id = $%d AND user_id = $%d AND tenant_id = $%d",
			strings.Join(updates, ", "), argIdx, argIdx+1, argIdx+2)
		args = append(args, streamID, userID, tenantID)

		var res sql.Result
		res, err = tx.ExecContext(ctx, query, args...)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to update stream: %v", err)
		}
		if rows, rErr := res.RowsAffected(); rErr == nil && rows == 0 {
			return nil, status.Error(codes.NotFound, "stream not found")
		}
	}

	if pullSource != nil {
		// Build a dynamic SET clause from pullPlan so we touch only columns
		// whose new value actually differs from the stored one — this keeps
		// the URI ciphertext stable when only enabled or placement changes
		// (fieldcrypt re-encryption uses a fresh nonce, so blind rewrites
		// would churn the column without semantic change).
		setClauses := []string{}
		setArgs := []any{streamID}
		setIdx := 2
		if pullPlan.writeURI {
			setClauses = append(setClauses, fmt.Sprintf("source_uri_enc = $%d", setIdx))
			setArgs = append(setArgs, pullPlan.encryptedURI)
			setIdx++
		}
		if pullPlan.writeEnabled {
			setClauses = append(setClauses, fmt.Sprintf("enabled = $%d", setIdx))
			setArgs = append(setArgs, pullPlan.enabledValue)
			setIdx++
		}
		if pullPlan.writeAllowed {
			setClauses = append(setClauses, fmt.Sprintf("allowed_cluster_ids = $%d", setIdx))
			setArgs = append(setArgs, pq.Array(pullPlan.allowedClusters))
		}
		if len(setClauses) > 0 {
			setClauses = append(setClauses, "updated_at = NOW()")
			query := fmt.Sprintf(
				"UPDATE commodore.stream_pull_sources SET %s WHERE stream_id = $1::uuid",
				strings.Join(setClauses, ", "),
			)
			var res sql.Result
			res, err = tx.ExecContext(ctx, query, setArgs...)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "failed to update pull source: %v", err)
			}
			if rows, rErr := res.RowsAffected(); rErr == nil && rows == 0 {
				return nil, status.Error(codes.NotFound, "pull source not found")
			}
			changedFields = append(changedFields, "pull_source")
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to commit stream update: %v", err)
	}

	if len(changedFields) > 0 {
		s.emitStreamChangeEvent(ctx, eventStreamUpdated, tenantID, userID, streamID, changedFields)
	}

	if req.Record != nil && req.GetRecord() && !currentRecordingEnabled {
		go s.startDVRAfterStreamUpdate(userID, tenantID, streamID, internalName)
	}

	return s.queryStream(ctx, streamID, userID, tenantID)
}

func (s *CommodoreServer) startDVRAfterStreamUpdate(userID, tenantID, streamID, internalName string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, userID)
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)

	_, err := s.StartDVR(ctx, &pb.StartDVRRequest{
		StreamId:     &streamID,
		InternalName: internalName,
	})
	if err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"tenant_id":     tenantID,
			"stream_id":     streamID,
			"internal_name": internalName,
		}).Warn("DVR start after stream update failed")
	}
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
	args := []any{streamID}
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
	s.emitStreamChangeEvent(ctx, eventStreamUpdated, tenantID, userID, streamID, []string{"push_targets"})

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
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	id := req.GetId()
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "id required")
	}

	// Build dynamic UPDATE
	setClauses := []string{"updated_at = NOW()"}
	args := []any{id, tenantID}
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

	s.emitStreamChangeEvent(ctx, eventStreamUpdated, tenantID, userID, t.GetStreamId(), []string{"push_targets"})

	return &t, nil
}

func (s *CommodoreServer) DeletePushTarget(ctx context.Context, req *pb.DeletePushTargetRequest) (*pb.DeletePushTargetResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	id := req.GetId()
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "id required")
	}

	var streamID string
	err = s.db.QueryRowContext(ctx, `
		DELETE FROM commodore.push_targets
		WHERE id = $1 AND tenant_id = $2
		RETURNING stream_id
	`, id, tenantID).Scan(&streamID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "push target not found")
		}
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	s.emitStreamChangeEvent(ctx, eventStreamUpdated, tenantID, userID, streamID, []string{"push_targets"})

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
	args := []any{id, tenantID, req.GetStatus()}
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
	args := []any{userID, tenantID}
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
	refreshTokenReuseGracePeriod = 2 * time.Minute

	eventAuthLoginSucceeded     = "auth_login_succeeded"
	eventAuthLoginFailed        = "auth_login_failed"
	eventAuthRegistered         = "auth_registered"
	eventAuthTokenRefreshed     = "auth_token_refreshed"
	eventMistAdminSessionMinted = "mist_admin_session_minted"
	eventTokenCreated           = "token_created"
	eventTokenRevoked           = "token_revoked"
	eventWalletLinked           = "wallet_linked"
	eventWalletUnlinked         = "wallet_unlinked"
	eventStreamCreated          = "stream_created"
	eventStreamUpdated          = "stream_updated"
	eventStreamDeleted          = "stream_deleted"
	eventStreamKeyCreated       = "stream_key_created"
	eventStreamKeyDeleted       = "stream_key_deleted"
	eventArtifactRegistered     = "artifact_registered"
	eventArtifactDeleted        = "artifact_deleted"
	eventPlaybackPolicyChanged  = "playback_policy_changed"
)

// emitServiceEvent enqueues a service event into
// commodore.service_event_outbox. The drain worker (started in
// NewGRPCServer via runServiceEventOutboxWorker) dispatches pending rows
// to Decklog with exponential backoff. Replaces the previous async
// fire-and-forget SendServiceEvent path so a Decklog outage no longer
// drops stream/policy mutation events. For strict atomicity with a
// caller-held state-mutation tx, use EnqueueServiceEventTx(ctx, tx, event).
func (s *CommodoreServer) emitServiceEvent(ctx context.Context, event *pb.ServiceEvent) {
	if event == nil {
		return
	}
	if ctxkeys.IsDemoMode(ctx) {
		return
	}
	s.enqueueServiceEvent(ctx, event)
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

func (s *CommodoreServer) emitMistAdminSessionMintedEvent(ctx context.Context, userID, tenantID, nodeID, clusterID string) {
	payload := &pb.AuthEvent{
		UserId:   userID,
		TenantId: tenantID,
		AuthType: "mist_admin_session",
	}
	event := &pb.ServiceEvent{
		EventType:       eventMistAdminSessionMinted,
		Timestamp:       timestamppb.Now(),
		Source:          "commodore",
		TenantId:        tenantID,
		UserId:          userID,
		ResourceType:    "infrastructure_node",
		ResourceId:      nodeID,
		SourceClusterId: clusterID,
		Payload:         &pb.ServiceEvent_AuthEvent{AuthEvent: payload},
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
		SELECT s.id, s.internal_name, s.stream_key, s.playback_id, s.title, s.description,
		       s.is_recording_enabled, s.created_at, s.updated_at, s.ingest_mode,
		       p.source_uri_enc, p.enabled, COALESCE(p.allowed_cluster_ids, '{}'),
		       s.active_ingest_cluster_id,
		       s.dvr_retention_days_override, s.clip_retention_days_override
		FROM commodore.streams s
		LEFT JOIN commodore.stream_pull_sources p ON p.stream_id = s.id
		WHERE s.id = ANY($1) AND s.user_id = $2 AND s.tenant_id = $3
	`, pq.Array(streamIDs), userID, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var streams []*pb.Stream
	var route *clusterRoute
	routeAttempted := false
	for rows.Next() {
		stream, err := s.scanStream(rows)
		if err != nil {
			s.logger.WithError(err).Warn("Error scanning stream in batch")
			continue
		}
		if !routeAttempted {
			routeAttempted = true
			if resolved, routeErr := s.resolveClusterRouteForTenant(ctx, tenantID); routeErr == nil {
				route = resolved
			}
		}
		if route != nil {
			s.populateStreamOriginRegion(ctx, tenantID, stream, route)
		}
		streams = append(streams, stream)
	}

	return &pb.GetStreamsBatchResponse{Streams: streams}, nil
}

func (s *CommodoreServer) queryStream(ctx context.Context, streamID, userID, tenantID string) (*pb.Stream, error) {
	var stream pb.Stream
	var description, sourceURIEnc, activeIngest, chapterMode sql.NullString
	var pullEnabled sql.NullBool
	var pullAllowedClusters pq.StringArray
	var createdAt, updatedAt time.Time
	var chapterInterval, dvrRetOverride, clipRetOverride sql.NullInt32

	// Query config only - operational state (status, started_at, ended_at) comes from Periscope Data Plane.
	err := s.db.QueryRowContext(ctx, `
		SELECT s.id, s.internal_name, s.stream_key, s.playback_id, s.title, s.description,
		       s.is_recording_enabled, s.created_at, s.updated_at, s.ingest_mode,
		       p.source_uri_enc, p.enabled, COALESCE(p.allowed_cluster_ids, '{}'),
		       s.active_ingest_cluster_id,
		       s.dvr_chapter_mode, s.dvr_chapter_interval_seconds,
		       s.dvr_retention_days_override, s.clip_retention_days_override
		FROM commodore.streams s
		LEFT JOIN commodore.stream_pull_sources p ON p.stream_id = s.id
		WHERE s.id = $1 AND s.user_id = $2 AND s.tenant_id = $3
	`, streamID, userID, tenantID).Scan(&stream.StreamId, &stream.InternalName, &stream.StreamKey, &stream.PlaybackId,
		&stream.Title, &description, &stream.IsRecordingEnabled, &createdAt, &updatedAt,
		&stream.IngestMode, &sourceURIEnc, &pullEnabled, &pullAllowedClusters,
		&activeIngest, &chapterMode, &chapterInterval,
		&dvrRetOverride, &clipRetOverride)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "stream not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	if description.Valid {
		stream.Description = description.String
	}
	if activeIngest.Valid {
		stream.ActiveIngestClusterId = activeIngest.String
	}
	stream.IsRecording = stream.IsRecordingEnabled
	stream.CreatedAt = timestamppb.New(createdAt)
	stream.UpdatedAt = timestamppb.New(updatedAt)
	if stream.IngestMode == "" {
		stream.IngestMode = "push"
	}
	if stream.IngestMode == "pull" && sourceURIEnc.Valid {
		sourceURI, err := s.pullSourceEncryptor.Decrypt(sourceURIEnc.String)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to decrypt pull source: %v", err)
		}
		class, classErr := pullsource.Classify(sourceURI)
		if classErr != nil {
			s.logger.WithError(classErr).WithField("stream_id", stream.StreamId).Debug("pull source classification failed")
		}
		stream.PullSource = buildPullSourceView(sourceURI, pullEnabled.Bool, class, []string(pullAllowedClusters))
	}

	stream.ThumbnailAssets = s.buildStreamThumbnailAssets(activeIngest, stream.StreamId)
	if chapterMode.Valid {
		stream.DvrChapterMode = chapterMode.String
	}
	if chapterInterval.Valid && chapterInterval.Int32 > 0 {
		iv := chapterInterval.Int32
		stream.DvrChapterIntervalSeconds = &iv
	}
	if dvrRetOverride.Valid {
		v := dvrRetOverride.Int32
		stream.DvrRetentionDaysOverride = &v
	}
	if clipRetOverride.Valid {
		v := clipRetOverride.Int32
		stream.ClipRetentionDaysOverride = &v
	}
	s.populateStreamOriginRegion(ctx, tenantID, &stream, nil)

	return &stream, nil
}

func (s *CommodoreServer) populateStreamOriginRegion(ctx context.Context, tenantID string, stream *pb.Stream, route *clusterRoute) {
	if stream == nil {
		return
	}
	if route == nil {
		var err error
		route, err = s.resolveClusterRouteForTenant(ctx, tenantID)
		if err != nil {
			return
		}
	}
	stream.StreamOriginRegion = streamOriginRegionForRoute(route, stream.GetActiveIngestClusterId())
}

// buildStreamThumbnailAssets projects ThumbnailAssets for a live stream from
// (active_ingest_cluster_id, stream_id) via the in-process clusterurls
// resolver. Returns nil when the stream has never been live or the cluster
// is unknown to the resolver. No I/O: the resolver is a map lookup.
func (s *CommodoreServer) buildStreamThumbnailAssets(activeIngest sql.NullString, streamID string) *pb.ThumbnailAssets {
	if s.clusterURLs == nil || !activeIngest.Valid || activeIngest.String == "" || streamID == "" {
		return nil
	}
	return s.clusterURLs.BuildThumbnailAssets(activeIngest.String, streamID)
}

// buildArtifactThumbnailAssets projects ThumbnailAssets for a clip/DVR/VOD
// artifact. Returns nil unless has_thumbnails is TRUE and a cluster is
// known. Caller supplies COALESCE(storage_cluster_id, origin_cluster_id)
// as the authoritative thumbnail cluster.
func (s *CommodoreServer) buildArtifactThumbnailAssets(hasThumbnails bool, cluster sql.NullString, assetKey string) *pb.ThumbnailAssets {
	if !hasThumbnails || s.clusterURLs == nil || !cluster.Valid || cluster.String == "" || assetKey == "" {
		return nil
	}
	return s.clusterURLs.BuildThumbnailAssets(cluster.String, assetKey)
}

func posterOnlyThumbnailAssets(assets *pb.ThumbnailAssets) *pb.ThumbnailAssets {
	if assets == nil || assets.GetPosterUrl() == "" {
		return nil
	}
	return &pb.ThumbnailAssets{
		PosterUrl: assets.GetPosterUrl(),
		AssetKey:  assets.GetAssetKey(),
	}
}

// scanStream scans config-only stream data; operational state comes from Periscope Data Plane
func (s *CommodoreServer) scanStream(rows *sql.Rows) (*pb.Stream, error) {
	var stream pb.Stream
	var description, sourceURIEnc, activeIngest sql.NullString
	var pullEnabled sql.NullBool
	var pullAllowedClusters pq.StringArray
	var createdAt, updatedAt time.Time
	var dvrRetOverride, clipRetOverride sql.NullInt32

	err := rows.Scan(&stream.StreamId, &stream.InternalName, &stream.StreamKey, &stream.PlaybackId,
		&stream.Title, &description, &stream.IsRecordingEnabled, &createdAt, &updatedAt,
		&stream.IngestMode, &sourceURIEnc, &pullEnabled, &pullAllowedClusters, &activeIngest,
		&dvrRetOverride, &clipRetOverride)
	if err != nil {
		return nil, err
	}

	if description.Valid {
		stream.Description = description.String
	}
	if activeIngest.Valid {
		stream.ActiveIngestClusterId = activeIngest.String
	}
	stream.IsRecording = stream.IsRecordingEnabled
	stream.CreatedAt = timestamppb.New(createdAt)
	stream.UpdatedAt = timestamppb.New(updatedAt)
	if stream.IngestMode == "" {
		stream.IngestMode = "push"
	}
	if stream.IngestMode == "pull" && sourceURIEnc.Valid {
		sourceURI, err := s.pullSourceEncryptor.Decrypt(sourceURIEnc.String)
		if err != nil {
			return nil, err
		}
		class, classErr := pullsource.Classify(sourceURI)
		if classErr != nil {
			s.logger.WithError(classErr).WithField("stream_id", stream.StreamId).Debug("pull source classification failed")
		}
		stream.PullSource = buildPullSourceView(sourceURI, pullEnabled.Bool, class, []string(pullAllowedClusters))
	}

	stream.ThumbnailAssets = s.buildStreamThumbnailAssets(activeIngest, stream.StreamId)
	if dvrRetOverride.Valid {
		v := dvrRetOverride.Int32
		stream.DvrRetentionDaysOverride = &v
	}
	if clipRetOverride.Valid {
		v := clipRetOverride.Int32
		stream.ClipRetentionDaysOverride = &v
	}

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
			SELECT 1 FROM commodore.clips WHERE internal_name = $1 OR playback_id = $1 OR clip_hash = $1
			UNION ALL
			SELECT 1 FROM commodore.dvr_recordings WHERE internal_name = $1 OR playback_id = $1 OR dvr_hash = $1
			UNION ALL
			SELECT 1 FROM commodore.vod_assets WHERE internal_name = $1 OR playback_id = $1 OR vod_hash = $1
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
	var sourceRequiresAuth bool
	var sourcePolicyJSON sql.NullString
	var sourceSecretEnc sql.NullString
	err = s.db.QueryRowContext(ctx, `
		SELECT internal_name, active_ingest_cluster_id, active_ingest_cluster_updated_at,
		       requires_auth, playback_policy::text, playback_webhook_secret_enc
		FROM commodore.streams
		WHERE id = $1 AND tenant_id = $2
	`, streamID, tenantID).Scan(
		&internalName, &activeIngestClusterID, &activeIngestClusterUpdatedAt,
		&sourceRequiresAuth, &sourcePolicyJSON, &sourceSecretEnc,
	)
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

	// Resolve retention via the per-class cascade (per-stream override →
	// tenant per-class default → system default), clamped by the tier cap.
	// User-supplied expires_at is treated as a per-asset override and
	// clamped to the same cap.
	resolvedDays, retentionErr := s.resolveInitialRetention(ctx, pb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_CLIP, tenantID, streamID)
	if retentionErr != nil {
		s.logger.WithError(retentionErr).WithFields(logging.Fields{
			"tenant_id": tenantID,
			"stream_id": streamID,
		}).Warn("Clip retention resolution failed; falling back to 30-day system default")
		resolvedDays = 30
	}
	var retentionUntil *time.Time
	if req.ExpiresAt != nil {
		t := time.Unix(*req.ExpiresAt, 0)
		// Clamp user-supplied expiry to the same cap the cascade applies.
		if resolvedDays > 0 {
			ceiling := time.Now().Add(time.Duration(resolvedDays) * 24 * time.Hour)
			if t.After(ceiling) {
				t = ceiling
			}
		}
		retentionUntil = &t
	} else if resolvedDays > 0 {
		t := time.Now().Add(time.Duration(resolvedDays) * 24 * time.Hour)
		retentionUntil = &t
	}
	// resolvedDays == 0 + no expires_at → infinite (retentionUntil stays nil).

	// Store the original request as JSON for audit. Includes the media-time
	// fields (start_ms/stop_ms) so relative-mode requests are fully captured.
	// The clip's stored start_time/duration hold the fulfilled range Foghorn
	// harvested (written once, after the call); requested_params preserves what
	// was asked for.
	requestedParams := map[string]any{"mode": req.Mode.String()}
	if req.StartUnix != nil {
		requestedParams["start_unix"] = *req.StartUnix
	}
	if req.StopUnix != nil {
		requestedParams["stop_unix"] = *req.StopUnix
	}
	if req.StartMs != nil {
		requestedParams["start_ms"] = *req.StartMs
	}
	if req.StopMs != nil {
		requestedParams["stop_ms"] = *req.StopMs
	}
	if req.DurationSec != nil {
		requestedParams["duration_sec"] = *req.DurationSec
	}
	paramsJSON, _ := json.Marshal(requestedParams)

	// Build Foghorn request with pre-generated hash
	foghornReq := &pb.CreateClipRequest{
		TenantId:           tenantID,
		StreamInternalName: internalName,
		ClipHash:           &clipHash, // Pass the hash we generated
		PlaybackId:         &playbackID,
		InternalName:       &artifactInternalName,
		Mode:               req.GetMode(),
		ProcessesJson:      s.resolveProcessesJSON(ctx, tenantID, streamID, clipClusterID, "clip"),
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
	if retentionUntil != nil {
		t := retentionUntil.Unix()
		foghornReq.ExpiresAt = &t
	}
	// Carry the resolved per-class horizon so Foghorn writes retention_until
	// from the same value Commodore registered. 0 = no auto-expire (infinite).
	{
		days := resolvedDays
		foghornReq.RetentionDays = &days
	}

	// Call Foghorn for artifact lifecycle management. Nothing is written to
	// commodore.clips until Foghorn succeeds, so a rejection needs no cleanup.
	resp, trailers, err := foghornClient.CreateClip(ctx, foghornReq)
	if err != nil {
		s.logger.WithError(err).WithField("clip_hash", clipHash).Error("Failed to create clip artifact via Foghorn")
		return nil, grpcutil.PropagateError(ctx, err, trailers)
	}

	// Foghorn has now created the artifact and queued its processing job. If we
	// cannot complete the registry write, compensate by deleting the Foghorn
	// artifact (and its job) so an invisible clip never lingers or processes,
	// then surface the failure rather than returning an unseeable success.
	cleanupOrphanedClip := func(reason string) {
		// Detached context: the registry write may have failed because the
		// request context was canceled/expired, and reusing it would skip the
		// cleanup too. A short independent deadline lets compensation run.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, _, delErr := foghornClient.DeleteClip(cleanupCtx, clipHash, &tenantID); delErr != nil {
			s.logger.WithError(delErr).WithField("clip_hash", clipHash).Error("Failed to delete orphaned Foghorn clip; artifact is retention-bounded")
		}
		s.logger.WithFields(logging.Fields{
			"tenant_id":     tenantID,
			"clip_hash":     clipHash,
			"internal_name": internalName,
			"reason":        reason,
		}).Error("Clip created in Foghorn but registry not written; compensated")
	}

	// Register in commodore.clips (business registry) with the fulfilled range
	// Foghorn harvested. Foghorn is the only place that resolves relative /
	// media-time and best-effort timing into a wall-clock range, so it is the
	// single authoritative source of start_time/duration; a successful clip
	// that reports none is a contract violation we fail closed on.
	startTime, duration, haveTiming := fulfilledClipTiming(resp)
	if !haveTiming {
		cleanupOrphanedClip("foghorn returned no fulfilled timing range")
		return nil, status.Error(codes.Internal, "clip source returned no fulfilled timing range")
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO commodore.clips (
			id, tenant_id, user_id, stream_id, clip_hash, internal_name, playback_id,
			title, description, start_time, duration, clip_mode, requested_params,
			origin_cluster_id, retention_until, requires_auth, playback_policy,
			playback_webhook_secret_enc, created_at, updated_at
		) VALUES ($1, $2, $3, NULLIF($4, '')::uuid, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17::jsonb, $18, NOW(), NOW())
	`, clipID, tenantID, userID, streamID, clipHash, artifactInternalName, playbackID,
		req.Title, req.Description, startTime, duration, req.Mode.String(), string(paramsJSON),
		clipClusterID, retentionUntil, sourceRequiresAuth, sourcePolicyJSON, sourceSecretEnc)
	if err != nil {
		cleanupOrphanedClip("registry write failed")
		return nil, status.Errorf(codes.Internal, "failed to register clip: %v", err)
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":     tenantID,
		"clip_hash":     clipHash,
		"clip_id":       clipID,
		"internal_name": internalName,
		"start_time":    startTime,
		"duration":      duration,
		"partial":       resp.GetPartial(),
	}).Info("Registered clip in business registry")

	return resp, nil
}

// fulfilledClipTiming returns the authoritative start_time/duration (ms) Foghorn
// harvested for the clip. Foghorn always reports a fulfilled range for a
// successful clip (it alone resolves relative/media-time anchors and best-effort
// coverage), so ok=false means the fields are absent — a contract violation the
// caller fails closed on rather than persisting request-derived timing.
func fulfilledClipTiming(resp *pb.CreateClipResponse) (startTimeMs, durationMs int64, ok bool) {
	startTimeMs, durationMs = resp.GetEffectiveStartMs(), resp.GetEffectiveDurationMs()
	return startTimeMs, durationMs, startTimeMs > 0 && durationMs > 0
}

func mediaListSortDirection(raw string) string {
	if strings.EqualFold(raw, "asc") {
		return "ASC"
	}
	return "DESC"
}

func clipListSortColumn(raw string) string {
	switch raw {
	case "title":
		return "COALESCE(c.title, '')"
	case "size_bytes":
		return "c.size_bytes"
	case "expires_at":
		return "c.retention_until"
	default:
		return "c.created_at"
	}
}

func dvrListSortColumn(raw string) string {
	switch raw {
	case "title":
		return "COALESCE(st.title, d.internal_name, '')"
	case "size_bytes":
		return "d.size_bytes"
	case "expires_at":
		return "d.retention_until"
	default:
		return "d.created_at"
	}
}

func vodListSortColumn(raw string) string {
	switch raw {
	case "title":
		return "COALESCE(title, filename, '')"
	case "size_bytes":
		return "size_bytes"
	case "expires_at":
		return "retention_until"
	default:
		return "created_at"
	}
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
	args := []any{tenantID}
	argIdx := 2

	if streamID := req.GetStreamId(); streamID != "" {
		whereClause += fmt.Sprintf(" AND c.stream_id = $%d", argIdx)
		args = append(args, streamID)
		argIdx++
	}
	if search := strings.TrimSpace(req.GetSearch()); search != "" {
		whereClause += fmt.Sprintf(" AND (LOWER(COALESCE(c.title, '')) LIKE $%d OR LOWER(COALESCE(c.description, '')) LIKE $%d OR LOWER(c.clip_hash) LIKE $%d)", argIdx, argIdx, argIdx)
		args = append(args, "%"+strings.ToLower(search)+"%")
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

	// Base query. Artifact thumbnails are served from storage_cluster_id when
	// present; origin_cluster_id is the storage owner for rows without a
	// separate storage cluster.
	query := fmt.Sprintf(`
		SELECT c.id, c.clip_hash, c.playback_id, c.stream_id::text, c.title, c.description,
		       c.start_time, c.duration, c.clip_mode, c.requested_params,
		       c.size_bytes, c.retention_until, COALESCE(c.retention_source, ''), c.created_at, c.updated_at,
		       COALESCE(c.storage_cluster_id, c.origin_cluster_id), c.has_thumbnails
		FROM commodore.clips c
		WHERE %s`, whereClause)

	offsetMode := req.Offset != nil || req.GetSortField() != "" || req.GetSortDirection() != ""
	offset := int32(0)
	if req.Offset != nil && req.GetOffset() > 0 {
		offset = req.GetOffset()
	}
	if offsetMode {
		query += fmt.Sprintf(" ORDER BY %s %s NULLS LAST, c.created_at DESC, c.clip_hash DESC LIMIT %d OFFSET %d",
			clipListSortColumn(req.GetSortField()), mediaListSortDirection(req.GetSortDirection()), params.Limit+1, offset)
	} else {
		// Add keyset condition if cursor provided
		if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
			query += " AND " + condition
			args = append(args, cursorArgs...)
		}
		query += " " + builder.OrderBy(params)
		query += fmt.Sprintf(" LIMIT %d", params.Limit+1)
	}

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
			sizeBytes                          sql.NullInt64
			retentionUntil                     sql.NullTime
			retentionSource                    string
			createdAt, updatedAt               time.Time
			thumbnailCluster                   sql.NullString
			hasThumbnails                      bool
		)
		if err := rows.Scan(&id, &clipHash, &playbackID, &streamID, &title, &description,
			&startTime, &duration, &clipMode, &requestedParams,
			&sizeBytes, &retentionUntil, &retentionSource, &createdAt, &updatedAt,
			&thumbnailCluster, &hasThumbnails); err != nil {
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
		if retentionSource != "" {
			src := retentionSource
			clip.RetentionSource = &src
		}
		if sizeBytes.Valid {
			size := sizeBytes.Int64
			clip.SizeBytes = &size
		}
		if retentionUntil.Valid {
			expiresAt := timestamppb.New(retentionUntil.Time)
			clip.ExpiresAt = expiresAt
		}
		clip.ThumbnailAssets = s.buildArtifactThumbnailAssets(hasThumbnails, thumbnailCluster, clipHash)

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
	if offsetMode {
		resp.Pagination.HasNextPage = hasMore
		resp.Pagination.HasPreviousPage = offset > 0
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
		       c.size_bytes, c.retention_until, COALESCE(c.retention_source, ''), c.created_at, c.updated_at,
		       COALESCE(c.storage_cluster_id, c.origin_cluster_id), c.has_thumbnails
		FROM commodore.clips c
		WHERE c.tenant_id = $1 AND c.clip_hash = $2
	`

	var (
		id, streamID, playbackID string
		title, description       sql.NullString
		startTime, duration      int64
		clipMode                 sql.NullString
		requestedParams          sql.NullString
		sizeBytes                sql.NullInt64
		retentionUntil           sql.NullTime
		retentionSource          string
		createdAt, updatedAt     time.Time
		thumbnailCluster         sql.NullString
		hasThumbnails            bool
	)

	err = s.db.QueryRowContext(ctx, query, tenantID, clipHash).Scan(
		&id, &clipHash, &playbackID, &streamID, &title, &description,
		&startTime, &duration, &clipMode, &requestedParams,
		&sizeBytes, &retentionUntil, &retentionSource, &createdAt, &updatedAt,
		&thumbnailCluster, &hasThumbnails,
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
	if retentionSource != "" {
		src := retentionSource
		clip.RetentionSource = &src
	}
	if sizeBytes.Valid {
		size := sizeBytes.Int64
		clip.SizeBytes = &size
	}
	if retentionUntil.Valid {
		expiresAt := timestamppb.New(retentionUntil.Time)
		clip.ExpiresAt = expiresAt
	}
	clip.ThumbnailAssets = s.buildArtifactThumbnailAssets(hasThumbnails, thumbnailCluster, clipHash)

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
	whereClause := "d.tenant_id = $1 AND (d.retention_until IS NULL OR d.retention_until > NOW())"
	args := []any{tenantID}
	argIdx := 2

	if streamID := req.GetStreamId(); streamID != "" {
		whereClause += fmt.Sprintf(" AND d.stream_id = $%d", argIdx)
		args = append(args, streamID)
		argIdx++
	}
	if search := strings.TrimSpace(req.GetSearch()); search != "" {
		whereClause += fmt.Sprintf(" AND (LOWER(COALESCE(st.title, '')) LIKE $%d OR LOWER(d.dvr_hash) LIKE $%d OR LOWER(d.internal_name) LIKE $%d)", argIdx, argIdx, argIdx)
		args = append(args, "%"+strings.ToLower(search)+"%")
		argIdx++
	}

	// Get total count
	var total int32
	countQuery := fmt.Sprintf(`
		SELECT COUNT(*)
		  FROM commodore.dvr_recordings d
		  LEFT JOIN commodore.streams st ON d.stream_id = st.id
		 WHERE %s`, whereClause)
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
			       d.size_bytes, d.retention_until, COALESCE(d.retention_source, ''), d.created_at, d.updated_at,
			       COALESCE(d.storage_cluster_id, d.origin_cluster_id), d.has_thumbnails,
			       st.active_ingest_cluster_id
			FROM commodore.dvr_recordings d
			LEFT JOIN commodore.streams st ON d.stream_id = st.id
			WHERE %s`, whereClause)

	offsetMode := req.Offset != nil || req.GetSortField() != "" || req.GetSortDirection() != ""
	offset := int32(0)
	if req.Offset != nil && req.GetOffset() > 0 {
		offset = req.GetOffset()
	}
	if offsetMode {
		query += fmt.Sprintf(" ORDER BY %s %s NULLS LAST, d.created_at DESC, d.dvr_hash DESC LIMIT %d OFFSET %d",
			dvrListSortColumn(req.GetSortField()), mediaListSortDirection(req.GetSortDirection()), params.Limit+1, offset)
	} else {
		// Add keyset condition if cursor provided
		if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
			query += " AND " + condition
			args = append(args, cursorArgs...)
		}
		query += " " + builder.OrderBy(params)
		query += fmt.Sprintf(" LIMIT %d", params.Limit+1)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var recordings []*pb.DVRInfo
	for rows.Next() {
		var (
			id, dvrHash, playbackID, internalName, streamID, title string
			retentionSource                                        string
			sizeBytes                                              sql.NullInt64
			retentionUntil                                         sql.NullTime
			createdAt, updatedAt                                   time.Time
			thumbnailCluster                                       sql.NullString
			hasThumbnails                                          bool
			activeIngestCluster                                    sql.NullString
		)
		if err := rows.Scan(&id, &dvrHash, &playbackID, &internalName, &streamID, &title,
			&sizeBytes, &retentionUntil, &retentionSource, &createdAt, &updatedAt,
			&thumbnailCluster, &hasThumbnails, &activeIngestCluster); err != nil {
			s.logger.WithError(err).Warn("Error scanning DVR recording")
			continue
		}

		recording := &pb.DVRInfo{
			Id:              &id,
			DvrHash:         dvrHash,
			PlaybackId:      &playbackID,
			InternalName:    internalName,
			StreamId:        &streamID,
			Title:           &title,
			RetentionSource: &retentionSource,
			CreatedAt:       timestamppb.New(createdAt),
			UpdatedAt:       timestamppb.New(updatedAt),
		}
		if retentionUntil.Valid {
			expiresAt := timestamppb.New(retentionUntil.Time)
			recording.ExpiresAt = expiresAt
		}
		if sizeBytes.Valid {
			size := sizeBytes.Int64
			recording.SizeBytes = &size
		}
		recording.ThumbnailAssets = s.buildArtifactThumbnailAssets(hasThumbnails, thumbnailCluster, dvrHash)
		if recording.ThumbnailAssets == nil {
			recording.ThumbnailAssets = posterOnlyThumbnailAssets(s.buildStreamThumbnailAssets(activeIngestCluster, streamID))
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
	if offsetMode {
		resp.Pagination.HasNextPage = hasMore
		resp.Pagination.HasPreviousPage = offset > 0
	}

	return resp, nil
}

// ResolveViewerEndpoint proxies viewer endpoint resolution to Foghorn
// and enriches the response with stream metadata from Commodore's database
func (s *CommodoreServer) ResolveViewerEndpoint(ctx context.Context, req *pb.ViewerEndpointRequest) (*pb.ViewerEndpointResponse, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	contentID := req.GetContentId()
	if normalized, ok, err := s.normalizeArtifactPlaybackID(ctx, contentID); err != nil {
		return nil, err
	} else if ok {
		contentID = normalized
	}

	var foghornClient *foghornclient.GRPCClient
	var err error
	if tenantID == "" {
		foghornClient, _, err = s.resolveFoghornForContent(ctx, contentID)
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

	resp, trailers, err := foghornClient.ResolveViewerEndpoint(outCtx, contentID, req.ViewerIp, req.ViewerToken)
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

func (s *CommodoreServer) normalizeArtifactPlaybackID(ctx context.Context, contentID string) (string, bool, error) {
	if s == nil || s.db == nil || contentID == "" {
		return "", false, nil
	}
	var playbackID string
	err := s.db.QueryRowContext(ctx, `
		SELECT playback_id
		  FROM (
			SELECT playback_id
			  FROM commodore.clips
			 WHERE clip_hash = $1
			UNION ALL
			SELECT playback_id
			  FROM commodore.vod_assets
			 WHERE vod_hash = $1
			UNION ALL
			SELECT playback_id
			  FROM commodore.dvr_recordings
			 WHERE dvr_hash = $1
		  ) resolved
		 WHERE playback_id IS NOT NULL
		   AND playback_id != ''
		 LIMIT 1
	`, contentID).Scan(&playbackID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, status.Errorf(codes.Internal, "database error normalizing artifact playback id: %v", err)
	}
	return playbackID, true, nil
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

	// GRPCMetricsInterceptor sits outermost so Unauthenticated /
	// PermissionDenied rejections from authInterceptor still show up in
	// commodore_grpc_requests_total.
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
		cfg.Logger.WithError(err).Fatal("Timed out waiting for Commodore gRPC TLS files")
	}
	tlsOpt, err := grpcutil.ServerTLS(tlsCfg, cfg.Logger)
	if err != nil {
		cfg.Logger.WithError(err).Fatal("Failed to configure Commodore gRPC TLS")
	}
	if tlsOpt != nil {
		opts = append(opts, tlsOpt)
	}

	server := grpc.NewServer(opts...)
	commodoreServer := NewCommodoreServer(cfg)

	// Background worker that replays per-cluster invalidation rows whose
	// synchronous Foghorn dispatch failed or returned a partial-success
	// response (NodesFailed > 0). Runs for the lifetime of the binary.
	go commodoreServer.runInvalidationOutboxWorker(context.Background())

	// Drain commodore.service_event_outbox to Decklog. Replaces the previous
	// async fire-and-forget go-routine — a Decklog outage now degrades to
	// outbox-backlog growth rather than dropped events.
	go commodoreServer.runServiceEventOutboxWorker(context.Background())

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
	pb.RegisterPlaybackAccessControlServiceServer(server, commodoreServer)

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

	sender := emailpkg.NewSender(emailpkg.Config{
		Host:     smtpHost,
		Port:     smtpPort,
		User:     smtpUser,
		Password: smtpPass,
		From:     fromEmail,
		FromName: os.Getenv("FROM_NAME"),
	})
	return sender.SendMail(context.Background(), email, subject, body)
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

	sender := emailpkg.NewSender(emailpkg.Config{
		Host:     smtpHost,
		Port:     smtpPort,
		User:     smtpUser,
		Password: smtpPass,
		From:     fromEmail,
		FromName: os.Getenv("FROM_NAME"),
	})
	return sender.SendMail(context.Background(), email, subject, body)
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

	// Resolve retention via the per-class cascade. VOD has no per-stream
	// override (uploads aren't bound to a stream); cascade collapses to
	// tenant per-class default → system default (keep forever), clamped
	// by the tier cap (Free=30d, paid=uncapped).
	resolvedDays, retentionErr := s.resolveInitialRetention(ctx, pb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_VOD, tenantID, "")
	if retentionErr != nil {
		s.logger.WithError(retentionErr).WithField("tenant_id", tenantID).Warn("VOD retention resolution failed; falling back to 30-day horizon for safety")
		resolvedDays = 30
	}
	var retentionUntil sql.NullTime
	if resolvedDays > 0 {
		retentionUntil = sql.NullTime{Valid: true, Time: time.Now().UTC().Add(time.Duration(resolvedDays) * 24 * time.Hour)}
	}

	// Register in commodore.vod_assets (business registry)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO commodore.vod_assets (
			id, tenant_id, user_id, vod_hash, internal_name, playback_id,
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
		TenantId:      tenantID,
		UserId:        userID,
		Filename:      req.Filename,
		SizeBytes:     req.SizeBytes,
		ContentType:   req.ContentType,
		Title:         req.Title,
		Description:   req.Description,
		VodHash:       &vodHash, // Pass the hash we generated
		PlaybackId:    &playbackID,
		InternalName:  &artifactInternalName,
		ClusterId:     vodRoute.clusterID,
		RetentionDays: &resolvedDays,
	}

	// Call Foghorn for S3 multipart upload setup
	resp, trailers, err := foghornClient.CreateVodUpload(ctx, foghornReq)
	if err != nil {
		s.logger.WithError(err).WithField("vod_hash", vodHash).Error("Failed to create VOD upload via Foghorn")
		if _, cleanupErr := s.db.ExecContext(context.Background(), `
			DELETE FROM commodore.vod_assets
			WHERE id = $1 AND tenant_id = $2
		`, vodID, tenantID); cleanupErr != nil {
			s.logger.WithError(cleanupErr).WithField("vod_hash", vodHash).Error("Failed to remove VOD registry row after Foghorn rejection")
		}
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

	foghornClient, vodRoute, err := s.resolveFoghornForTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	// Check if tenant is suspended (prepaid balance < -$10)
	if suspended, suspendErr := s.isTenantSuspended(ctx, tenantID); suspendErr != nil {
		s.logger.WithError(suspendErr).Warn("Failed to check tenant suspension status")
	} else if suspended {
		return nil, status.Error(codes.PermissionDenied, "account suspended - please top up your balance to complete uploads")
	}

	processesJSON := s.resolveProcessesJSON(ctx, tenantID, "", vodRoute.clusterID, "vod")

	// Forward to Foghorn (it manages S3 multipart completion and lifecycle state)
	foghornReq := &pb.CompleteVodUploadRequest{
		TenantId:      tenantID,
		UploadId:      req.UploadId,
		Parts:         req.Parts,
		ProcessesJson: processesJSON,
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

// GetVodUploadStatus reads media upload state from Foghorn, then validates and
// enriches the response with Commodore-owned VOD registry metadata.
func (s *CommodoreServer) GetVodUploadStatus(ctx context.Context, req *pb.GetVodUploadStatusRequest) (*pb.GetVodUploadStatusResponse, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetUploadId() == "" {
		return nil, status.Error(codes.InvalidArgument, "upload_id is required")
	}

	foghornClient, _, err := s.resolveFoghornForTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	resp, trailers, err := foghornClient.GetVodUploadStatus(ctx, tenantID, req.UploadId)
	if err != nil {
		s.logger.WithError(err).WithField("upload_id", req.UploadId).Warn("Failed to read VOD upload status via Foghorn")
		return nil, grpcutil.PropagateError(ctx, err, trailers)
	}
	if resp == nil || resp.ArtifactHash == "" {
		return nil, status.Error(codes.NotFound, "upload not found")
	}

	var playbackID string
	err = s.db.QueryRowContext(ctx, `
		SELECT playback_id
		FROM commodore.vod_assets
		WHERE tenant_id = $1 AND vod_hash = $2
	`, tenantID, resp.ArtifactHash).Scan(&playbackID)
	if errors.Is(err, sql.ErrNoRows) {
		s.logger.WithFields(logging.Fields{
			"tenant_id":      tenantID,
			"upload_id":      req.UploadId,
			"artifact_hash":  resp.ArtifactHash,
			"foghorn_status": resp.State.String(),
		}).Warn("Foghorn VOD upload status has no matching Commodore VOD registry row")
		return nil, status.Error(codes.NotFound, "upload not found")
	}
	if err != nil {
		s.logger.WithError(err).WithField("artifact_hash", resp.ArtifactHash).Error("Failed to enrich VOD upload status")
		return nil, status.Error(codes.Internal, "failed to enrich upload status")
	}
	resp.PlaybackId = playbackID
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
		streamID             string
		originType           string
		originID             string
		title, description   sql.NullString
		filename             string
		contentType          sql.NullString
		sizeBytes            sql.NullInt64
		retentionUntil       sql.NullTime
		retentionSource      sql.NullString
		createdAt, updatedAt time.Time
	)
	var (
		thumbnailCluster sql.NullString
		hasThumbnails    bool
	)
	err = s.db.QueryRowContext(ctx, `
		SELECT id, vod_hash, playback_id, COALESCE(stream_id::text, ''), COALESCE(origin_type, ''), COALESCE(origin_id, ''),
		       title, description, filename, content_type,
		       size_bytes, retention_until, retention_source, created_at, updated_at,
		       COALESCE(storage_cluster_id, origin_cluster_id), has_thumbnails
		FROM commodore.vod_assets
		WHERE vod_hash = $1 AND tenant_id = $2 AND library_visible = true
	`, req.ArtifactHash, tenantID).Scan(
		&id, &vodHash, &playbackID, &streamID, &originType, &originID, &title, &description, &filename, &contentType,
		&sizeBytes, &retentionUntil, &retentionSource, &createdAt, &updatedAt,
		&thumbnailCluster, &hasThumbnails,
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
	if streamID != "" {
		asset.StreamId = &streamID
	}
	if originType != "" {
		asset.OriginType = &originType
	}
	if originID != "" {
		asset.OriginId = &originID
	}
	if sizeBytes.Valid {
		size := sizeBytes.Int64
		asset.SizeBytes = &size
	}
	if retentionUntil.Valid {
		asset.ExpiresAt = timestamppb.New(retentionUntil.Time)
	}
	if retentionSource.Valid && retentionSource.String != "" {
		src := retentionSource.String
		asset.RetentionSource = &src
	}
	asset.ThumbnailAssets = s.buildArtifactThumbnailAssets(hasThumbnails, thumbnailCluster, vodHash)

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

	whereClause := "tenant_id = $1"
	args := []any{tenantID}
	argIdx := 2
	if streamID := req.GetStreamId(); streamID != "" {
		whereClause += fmt.Sprintf(" AND stream_id = $%d::uuid", argIdx)
		args = append(args, streamID)
		argIdx++
	} else {
		whereClause += " AND library_visible = true"
	}
	if search := strings.TrimSpace(req.GetSearch()); search != "" {
		whereClause += fmt.Sprintf(" AND (LOWER(COALESCE(title, '')) LIKE $%d OR LOWER(COALESCE(filename, '')) LIKE $%d OR LOWER(vod_hash) LIKE $%d)", argIdx, argIdx, argIdx)
		args = append(args, "%"+strings.ToLower(search)+"%")
		argIdx++
	}

	// Get total count
	var total int32
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM commodore.vod_assets WHERE %s", whereClause)
	if countErr := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); countErr != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", countErr)
	}

	// Build keyset pagination query
	builder := &pagination.KeysetBuilder{
		TimestampColumn: "created_at",
		IDColumn:        "vod_hash",
	}

	// Base query
	query := fmt.Sprintf(`
		SELECT id, vod_hash, playback_id, COALESCE(stream_id::text, ''), COALESCE(origin_type, ''), COALESCE(origin_id, ''),
		       title, description, filename, content_type,
		       size_bytes, retention_until, retention_source, created_at, updated_at,
		       COALESCE(storage_cluster_id, origin_cluster_id), has_thumbnails
		FROM commodore.vod_assets
		WHERE %s`, whereClause)

	offsetMode := req.Offset != nil || req.GetSortField() != "" || req.GetSortDirection() != ""
	offset := int32(0)
	if req.Offset != nil && req.GetOffset() > 0 {
		offset = req.GetOffset()
	}
	if offsetMode {
		query += fmt.Sprintf(" ORDER BY %s %s NULLS LAST, created_at DESC, vod_hash DESC LIMIT %d OFFSET %d",
			vodListSortColumn(req.GetSortField()), mediaListSortDirection(req.GetSortDirection()), params.Limit+1, offset)
	} else {
		// Add keyset condition if cursor provided
		if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
			query += " AND " + condition
			args = append(args, cursorArgs...)
		}
		query += " " + builder.OrderBy(params)
		query += fmt.Sprintf(" LIMIT %d", params.Limit+1)
	}

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
			streamID             string
			originType           string
			originID             string
			title, description   sql.NullString
			filename             string
			contentType          sql.NullString
			sizeBytes            sql.NullInt64
			retentionUntil       sql.NullTime
			retentionSource      sql.NullString
			createdAt, updatedAt time.Time
			thumbnailCluster     sql.NullString
			hasThumbnails        bool
		)
		if err := rows.Scan(&id, &vodHash, &playbackID, &streamID, &originType, &originID, &title, &description, &filename, &contentType,
			&sizeBytes, &retentionUntil, &retentionSource, &createdAt, &updatedAt,
			&thumbnailCluster, &hasThumbnails); err != nil {
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
		if streamID != "" {
			asset.StreamId = &streamID
		}
		if originType != "" {
			asset.OriginType = &originType
		}
		if originID != "" {
			asset.OriginId = &originID
		}
		if sizeBytes.Valid {
			size := sizeBytes.Int64
			asset.SizeBytes = &size
		}
		if retentionUntil.Valid {
			asset.ExpiresAt = timestamppb.New(retentionUntil.Time)
		}
		if retentionSource.Valid && retentionSource.String != "" {
			src := retentionSource.String
			asset.RetentionSource = &src
		}
		asset.ThumbnailAssets = s.buildArtifactThumbnailAssets(hasThumbnails, thumbnailCluster, vodHash)

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
	if offsetMode {
		resp.Pagination.HasNextPage = hasMore
		resp.Pagination.HasPreviousPage = offset > 0
	}

	return resp, nil
}

// ListStorageArtifacts returns the account storage browser's canonical
// registry projection. This is intentionally served from Commodore instead of
// Bridge joining one page each of clips/DVR/VOD, so search, sorting, and
// pagination run against the full tenant dataset.
func (s *CommodoreServer) ListStorageArtifacts(ctx context.Context, req *pb.ListStorageArtifactsRequest) (*pb.ListStorageArtifactsResponse, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetTenantId() != "" && req.GetTenantId() != tenantID {
		return nil, status.Error(codes.PermissionDenied, "tenant mismatch")
	}

	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}
	offset := int(req.GetOffset())
	if offset < 0 {
		offset = 0
	}

	args := []any{tenantID}
	filters := []string{"TRUE"}
	argIdx := 2

	if streamID := req.GetStreamId(); streamID != "" {
		filters = append(filters, fmt.Sprintf("stream_id = $%d", argIdx))
		args = append(args, streamID)
		argIdx++
	}

	if len(req.GetKinds()) > 0 {
		var placeholders []string
		for _, kind := range req.GetKinds() {
			normalized := strings.ToLower(strings.TrimSpace(kind))
			switch normalized {
			case "vod", "dvr", "chapter", "clip":
				placeholders = append(placeholders, fmt.Sprintf("$%d", argIdx))
				args = append(args, normalized)
				argIdx++
			}
		}
		if len(placeholders) > 0 {
			filters = append(filters, fmt.Sprintf("kind IN (%s)", strings.Join(placeholders, ", ")))
		}
	}

	if search := strings.TrimSpace(req.GetSearch()); search != "" {
		filters = append(filters, fmt.Sprintf("(LOWER(title) LIKE $%d OR LOWER(artifact_hash) LIKE $%d OR LOWER(stream_title) LIKE $%d OR LOWER(secondary_label) LIKE $%d)", argIdx, argIdx, argIdx, argIdx))
		args = append(args, "%"+strings.ToLower(search)+"%")
	}

	sortField := "created_at"
	switch req.GetSortField() {
	case "title", "kind", "size_bytes", "expires_at":
		sortField = req.GetSortField()
	}
	sortDirection := "DESC"
	if strings.EqualFold(req.GetSortDirection(), "asc") {
		sortDirection = "ASC"
	}
	nulls := "NULLS LAST"
	if sortField == "created_at" && sortDirection == "DESC" {
		nulls = ""
	}

	baseQuery := `
		SELECT kind, id, artifact_hash, playback_id, stream_id, stream_title, title, secondary_label,
		       size_bytes, status, storage_location, is_frozen, created_at, updated_at, expires_at,
		       retention_source, origin_type, origin_id, storage_cluster_id, has_thumbnails
		FROM (
			SELECT
				CASE WHEN COALESCE(v.origin_type, '') = 'dvr_chapter' THEN 'chapter' ELSE 'vod' END AS kind,
				v.id::text AS id,
				v.vod_hash AS artifact_hash,
				COALESCE(v.playback_id, '') AS playback_id,
				COALESCE(v.stream_id::text, '') AS stream_id,
				COALESCE(st.title, '') AS stream_title,
				COALESCE(NULLIF(v.title, ''), NULLIF(v.filename, ''), v.vod_hash) AS title,
				COALESCE(NULLIF(v.filename, ''), v.content_type, '') AS secondary_label,
				v.size_bytes AS size_bytes,
				'registry' AS status,
				NULL::text AS storage_location,
				NULL::boolean AS is_frozen,
				v.created_at AS created_at,
				v.updated_at AS updated_at,
				v.retention_until AS expires_at,
				COALESCE(v.retention_source, '') AS retention_source,
				COALESCE(v.origin_type, '') AS origin_type,
				COALESCE(v.origin_id, '') AS origin_id,
				COALESCE(v.storage_cluster_id, v.origin_cluster_id, '') AS storage_cluster_id,
				v.has_thumbnails AS has_thumbnails
			FROM commodore.vod_assets v
			LEFT JOIN commodore.streams st ON st.id = v.stream_id AND st.tenant_id = v.tenant_id
			WHERE v.tenant_id = $1
			  AND (v.library_visible = true OR COALESCE(v.origin_type, '') = 'dvr_chapter')

			UNION ALL

			SELECT
				'dvr' AS kind,
				d.id::text AS id,
				d.dvr_hash AS artifact_hash,
				COALESCE(d.playback_id, '') AS playback_id,
				COALESCE(d.stream_id::text, '') AS stream_id,
				COALESCE(st.title, '') AS stream_title,
				COALESCE(st.title, d.internal_name, d.dvr_hash) AS title,
				COALESCE(d.internal_name, '') AS secondary_label,
				d.size_bytes AS size_bytes,
				'registry' AS status,
				NULL::text AS storage_location,
				NULL::boolean AS is_frozen,
				d.created_at AS created_at,
				d.updated_at AS updated_at,
				d.retention_until AS expires_at,
				COALESCE(d.retention_source, '') AS retention_source,
				'' AS origin_type,
				'' AS origin_id,
				COALESCE(d.storage_cluster_id, d.origin_cluster_id, '') AS storage_cluster_id,
				d.has_thumbnails AS has_thumbnails
			FROM commodore.dvr_recordings d
			LEFT JOIN commodore.streams st ON st.id = d.stream_id AND st.tenant_id = d.tenant_id
			WHERE d.tenant_id = $1 AND (d.retention_until IS NULL OR d.retention_until > NOW())

			UNION ALL

			SELECT
				'clip' AS kind,
				c.id::text AS id,
				c.clip_hash AS artifact_hash,
				COALESCE(c.playback_id, '') AS playback_id,
				COALESCE(c.stream_id::text, '') AS stream_id,
				COALESCE(st.title, '') AS stream_title,
				COALESCE(NULLIF(c.title, ''), c.clip_hash) AS title,
				COALESCE(c.clip_mode, '') AS secondary_label,
				c.size_bytes AS size_bytes,
				'registry' AS status,
				NULL::text AS storage_location,
				NULL::boolean AS is_frozen,
				c.created_at AS created_at,
				c.updated_at AS updated_at,
				c.retention_until AS expires_at,
				COALESCE(c.retention_source, '') AS retention_source,
				'' AS origin_type,
				'' AS origin_id,
				COALESCE(c.storage_cluster_id, c.origin_cluster_id, '') AS storage_cluster_id,
				c.has_thumbnails AS has_thumbnails
			FROM commodore.clips c
			LEFT JOIN commodore.streams st ON st.id = c.stream_id AND st.tenant_id = c.tenant_id
			WHERE c.tenant_id = $1
		) artifacts`

	whereClause := strings.Join(filters, " AND ")
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM (%s WHERE %s) counted", baseQuery, whereClause)
	var total int32
	if countErr := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); countErr != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", countErr)
	}

	dataArgs := append([]any{}, args...)
	limitArg := len(dataArgs) + 1
	offsetArg := len(dataArgs) + 2
	dataArgs = append(dataArgs, limit+1, offset)
	dataQuery := fmt.Sprintf(`%s WHERE %s ORDER BY %s %s %s, created_at DESC, artifact_hash DESC LIMIT $%d OFFSET $%d`,
		baseQuery, whereClause, sortField, sortDirection, nulls, limitArg, offsetArg)

	rows, err := s.db.QueryContext(ctx, dataQuery, dataArgs...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var artifacts []*pb.StorageArtifactInfo
	for rows.Next() {
		var (
			kind, id, hash, playbackID, streamID, streamTitle, title, secondary string
			sizeBytes                                                           sql.NullInt64
			statusText, storageLocation                                         sql.NullString
			isFrozen                                                            sql.NullBool
			createdAt, updatedAt                                                time.Time
			expiresAt                                                           sql.NullTime
			retentionSource, originType, originID, storageClusterID             string
			hasThumbnails                                                       bool
		)
		if err := rows.Scan(&kind, &id, &hash, &playbackID, &streamID, &streamTitle, &title, &secondary,
			&sizeBytes, &statusText, &storageLocation, &isFrozen, &createdAt, &updatedAt, &expiresAt,
			&retentionSource, &originType, &originID, &storageClusterID, &hasThumbnails); err != nil {
			s.logger.WithError(err).Warn("Error scanning storage artifact")
			continue
		}

		artifact := &pb.StorageArtifactInfo{
			Kind:             kind,
			Id:               id,
			ArtifactHash:     hash,
			StreamTitle:      streamTitle,
			Title:            title,
			SecondaryLabel:   secondary,
			Status:           statusText.String,
			CreatedAt:        timestamppb.New(createdAt),
			UpdatedAt:        timestamppb.New(updatedAt),
			StorageClusterId: storageClusterID,
			HasThumbnails:    hasThumbnails,
		}
		if playbackID != "" {
			artifact.PlaybackId = &playbackID
		}
		if streamID != "" {
			artifact.StreamId = &streamID
		}
		if sizeBytes.Valid {
			value := sizeBytes.Int64
			artifact.SizeBytes = &value
		}
		if storageLocation.Valid && storageLocation.String != "" {
			value := storageLocation.String
			artifact.StorageLocation = &value
		}
		if isFrozen.Valid {
			value := isFrozen.Bool
			artifact.IsFrozen = &value
		}
		if expiresAt.Valid {
			artifact.ExpiresAt = timestamppb.New(expiresAt.Time)
		}
		if retentionSource != "" {
			artifact.RetentionSource = &retentionSource
		}
		if originType != "" {
			artifact.OriginType = &originType
		}
		if originID != "" {
			artifact.OriginId = &originID
		}
		artifact.ThumbnailAssets = s.buildArtifactThumbnailAssets(hasThumbnails, sql.NullString{String: storageClusterID, Valid: storageClusterID != ""}, hash)

		artifacts = append(artifacts, artifact)
	}

	hasNext := len(artifacts) > limit
	if hasNext {
		artifacts = artifacts[:limit]
	}

	return &pb.ListStorageArtifactsResponse{
		Artifacts:   artifacts,
		TotalCount:  total,
		HasNextPage: hasNext,
	}, nil
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
// Returns the first owner/admin user, or the first user if no privileged user exists.
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
			CASE role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 ELSE 2 END,
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

// CreateUserInTenant creates a user in an existing tenant without triggering
// tenant creation or billing initialization. SERVICE_TOKEN auth only.
func (s *CommodoreServer) CreateUserInTenant(ctx context.Context, req *pb.CreateUserInTenantRequest) (*pb.CreateUserInTenantResponse, error) {
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "CreateUserInTenant requires service token auth")
	}

	tenantID := req.GetTenantId()
	email := req.GetEmail()
	password := req.GetPassword()

	if tenantID == "" || email == "" || password == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id, email, and password are required")
	}

	role := req.GetRole()
	if role == "" {
		role = "owner"
	}
	allowedRoles := map[string]bool{"owner": true, "member": true}
	if !allowedRoles[role] {
		return nil, status.Errorf(codes.InvalidArgument, "role must be 'owner' or 'member', got %q", role)
	}

	// Verify tenant exists via Quartermaster
	if s.quartermasterClient == nil {
		return nil, status.Error(codes.FailedPrecondition, "Quartermaster client not available, cannot verify tenant exists")
	}
	if _, tenantErr := s.quartermasterClient.GetTenant(ctx, tenantID); tenantErr != nil {
		return nil, status.Errorf(codes.NotFound, "tenant %s not found in Quartermaster: %v", tenantID, tenantErr)
	}

	// Check email uniqueness
	var existingID string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM commodore.users WHERE email = $1`, email).Scan(&existingID)
	if err == nil {
		return nil, status.Error(codes.AlreadyExists, "user with this email already exists")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	hashedPassword, err := auth.HashPassword(password)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to hash password: %v", err)
	}

	userID := uuid.New().String()
	now := time.Now()
	permissions := getDefaultPermissions(role)

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO commodore.users (id, tenant_id, email, password_hash, first_name, last_name, role, permissions, is_active, verified, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, true, true, $9, $9)
	`, userID, tenantID, email, hashedPassword, req.GetFirstName(), req.GetLastName(), role, pq.Array(permissions), now)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create user: %v", err)
	}

	s.logger.WithFields(logging.Fields{
		"user_id":   userID,
		"tenant_id": tenantID,
		"email":     email,
		"role":      role,
	}).Info("User created in existing tenant via CreateUserInTenant")

	return &pb.CreateUserInTenantResponse{
		User: &pb.User{
			Id:          userID,
			TenantId:    tenantID,
			Email:       &email,
			FirstName:   req.GetFirstName(),
			LastName:    req.GetLastName(),
			Role:        role,
			Permissions: permissions,
			IsActive:    true,
			IsVerified:  true,
			CreatedAt:   timestamppb.New(now),
			UpdatedAt:   timestamppb.New(now),
		},
	}, nil
}
