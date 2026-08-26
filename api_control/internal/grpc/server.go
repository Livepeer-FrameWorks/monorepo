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
	"frameworks/api_control/internal/database/commodoredb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	commodoreclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
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
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	meteringpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/metering_contract"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	tenantlimitspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/tenant_limits"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pullsource"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/streamident"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/turnstile"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
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
	GetBehavior() *commodorepb.BehaviorData
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

type streamAdmissionBilling interface {
	GetTenantBillingStatus(ctx context.Context, tenantID string) (*purserpb.GetTenantBillingStatusResponse, error)
}

// CommodoreServer implements the Commodore gRPC services
type CommodoreServer struct {
	commodorepb.UnimplementedInternalServiceServer
	commodorepb.UnimplementedUserServiceServer
	commodorepb.UnimplementedStreamServiceServer
	commodorepb.UnimplementedStreamKeyServiceServer
	commodorepb.UnimplementedDeveloperServiceServer
	commodorepb.UnimplementedClipServiceServer
	commodorepb.UnimplementedDVRServiceServer
	commodorepb.UnimplementedViewerServiceServer
	commodorepb.UnimplementedVodServiceServer
	commodorepb.UnimplementedNodeManagementServiceServer
	commodorepb.UnimplementedPushTargetServiceServer
	commodorepb.UnimplementedPlaybackAccessControlServiceServer
	db                  *sql.DB
	dbMaxIdleConns      int
	logger              logging.Logger
	foghornPool         *foghornclient.FoghornPool
	quartermasterClient *qmclient.GRPCClient
	// qmEntitlements is the narrow Quartermaster surface used by the signed-
	// policy-bundle path (tenant entitlement lookups). Nil when no
	// Quartermaster client is configured.
	qmEntitlements         tenantEntitlementAPI
	navigatorClient        *navigator.Client
	purserClient           *purserclient.GRPCClient
	streamAdmissionBilling streamAdmissionBilling
	listmonkClient         *listmonk.Client
	decklogClient          *decklogclient.BatchedClient
	defaultMailingListID   int
	metrics                *ServerMetrics
	turnstileValidator     *turnstile.Validator
	turnstileFailOpen      bool
	passwordResetSecret    []byte
	fieldEncryptor         *fieldcrypt.FieldEncryptor
	// Separate FieldEncryptor for playback webhook secrets so HKDF purpose
	// isolation prevents cross-feature key reuse.
	playbackWebhookEncryptor *fieldcrypt.FieldEncryptor
	// Separate FieldEncryptor for pull-input source URIs (purpose
	// "pull-source-uri"). Used by ResolvePullSourceByInternalName and the
	// commodore bootstrap reconciler when persisting stream_pull_sources.
	pullSourceEncryptor *fieldcrypt.FieldEncryptor
	routeCache          map[string]*clusterRoute
	routeCacheMu        sync.RWMutex
	routeCacheTTL       time.Duration
	// admissionRefresh collapses concurrent admission-state refreshes for the
	// same tenant into one Quartermaster/Purser round trip.
	admissionRefresh singleflight.Group
	// routeBuild collapses concurrent full route constructions for the same
	// tenant; each one fans out to Quartermaster, Purser, and Foghorn discovery.
	routeBuild           singleflight.Group
	foghornCandidateMu   sync.Mutex
	foghornCandidateNext map[string]int
	// clusterURLs resolves cluster_id → Chandler base URL from an in-process
	// snapshot refreshed off Quartermaster. Used by list/get handlers to
	// project thumbnailAssets onto Stream/Clip/DVR/VOD rows without per-row
	// network calls.
	clusterURLs *clusterurls.Resolver
	// childArtifactDeleteFn is a test seam for the cascade delete of a single stream-owned child artifact
	// (clip/dvr). Production leaves it nil and deleteStreamChildMedia routes through the origin-cluster Foghorn;
	// wired tests inject a deterministic fake to exercise the fail→retry→ack coordination without a live Foghorn.
	childArtifactDeleteFn func(ctx context.Context, kind, hash, cluster, tenantID string) error
	// streamThumbnailDeleteFn is a test seam for the parent stream's Foghorn thumbnail-cleanup RPC, invoked ONCE PER
	// recorded owning cell (clusterID is the target cell). Production leaves it nil and deleteStreamThumbnails resolves
	// each cell via Quartermaster service discovery (resolveFoghornForClusterDirect); wired tests inject a deterministic
	// fake so the full claim→dispatch→retry→finalize loop can run over real Postgres without a live Foghorn.
	streamThumbnailDeleteFn func(ctx context.Context, streamID, tenantID, clusterID string) error
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
	clusterPeers            []*clusterpeerpb.TenantClusterPeer   // healthy peers eligible for routing
	admissionPeers          []*clusterpeerpb.TenantClusterPeer   // plan-entitled peers, including unhealthy peers needed for structured admission denials
	tenantResourceLimits    *tenantlimitspb.TenantResourceLimits // access-specific cap override; nil = use Purser tier entitlement
	resolvedAt              time.Time
	// admissionResolvedAt ages the peer sets separately from the rest of the
	// route. Addresses and slugs change rarely; peer health and plan
	// entitlement decide whether a publish is admitted, and must not be served
	// from a route-length cache.
	admissionResolvedAt time.Time
}

type clusterFanoutTarget struct {
	clusterID string
	addr      string
}

// activeIngestLease is how long a cluster's claim on a stream's ingest holds.
//
// One constant governs every side: the SQL claim, after which another cluster
// may take the placement; the freshness window discovery uses to decide whether
// a recorded placement still means anything; and Foghorn's renewal cadence,
// which must re-assert inside it. They must be the same value, or a cluster can
// hold the lease while publishers are routed elsewhere — so it is shared rather
// than restated here.
const activeIngestLease = streamident.ActiveIngestLease

// activeIngestClusterFreshnessWindow is the discovery-side view of the lease.
const activeIngestClusterFreshnessWindow = activeIngestLease

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

func serviceInstanceAddr(inst *quartermasterpb.ServiceInstance) string {
	if inst == nil || inst.GetHost() == "" || inst.GetPort() <= 0 {
		return ""
	}
	return net.JoinHostPort(inst.GetHost(), strconv.Itoa(int(inst.GetPort())))
}

func (s *CommodoreServer) discoverFoghornAddrs(ctx context.Context, clusterID string) []string {
	if s.quartermasterClient == nil || clusterID == "" {
		return nil
	}
	resp, err := s.quartermasterClient.DiscoverServices(ctx, "foghorn", clusterID, &commonpb.CursorPaginationRequest{First: 20})
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

// resolveFoghornForClusterDirect resolves a cluster's Foghorn via Quartermaster SERVICE DISCOVERY, independent of any
// tenant plan/entitlement. Stream-cleanup delivery must reach a DURABLY-RECORDED owning cell even after tenant routing
// changed (a tenant de-entitled from a cluster it once ingested on would drop out of resolveFoghornForCluster's
// tenant route, stranding that cell's tombstone). This is an async control path — never the request/serving path.
func (s *CommodoreServer) resolveFoghornForClusterDirect(ctx context.Context, clusterID string) (*foghornclient.GRPCClient, error) {
	if strings.TrimSpace(clusterID) == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}
	addr := s.nextFoghornAddr(clusterID, s.discoverFoghornAddrs(ctx, clusterID))
	if addr == "" {
		return nil, status.Errorf(codes.NotFound, "no foghorn instance discovered for cluster %s", clusterID)
	}
	client, err := s.foghornPool.GetOrCreate(foghornPoolKey(clusterID, addr), addr)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "foghorn connection failed for cluster %s: %v", clusterID, err)
	}
	return client, nil
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
	ID               string
	TenantID         string
	Email            string
	PasswordHash     string
	FirstName        sql.NullString
	LastName         sql.NullString
	Role             string
	Permissions      []string
	IsActive         bool
	IsVerified       bool
	PlatformOperator bool
	LastLoginAt      sql.NullTime
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// platformRoles maps a user's grants to the RFC 9068 roles slice carried in
// the session token; the platform_operator grant is the only role today.
func platformRoles(platformOperator bool) []string {
	if platformOperator {
		return []string{auth.RolePlatformOperator}
	}
	return nil
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
		fwdb.ArrayScan(&user.Permissions),
		&user.IsActive,
		&user.IsVerified,
		&user.CreatedAt,
		&user.UpdatedAt,
		&user.PlatformOperator,
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
		fwdb.ArrayScan(&user.Permissions),
		&user.IsActive,
		&user.IsVerified,
		&user.LastLoginAt,
		&user.CreatedAt,
		&user.UpdatedAt,
		&user.PlatformOperator,
	)
}

func scanCommodoreUserForRefresh(row *sql.Row, user *commodoreUserRecord) error {
	return row.Scan(
		&user.Email,
		&user.Role,
		fwdb.ArrayScan(&user.Permissions),
		&user.FirstName,
		&user.LastName,
		&user.IsActive,
		&user.IsVerified,
		&user.CreatedAt,
		&user.UpdatedAt,
		&user.PlatformOperator,
	)
}

func commodoreUserFromLoginRow(row commodoredb.GetLoginUserByEmailRow) (commodoreUserRecord, error) {
	if !row.CreatedAt.Valid || !row.UpdatedAt.Valid {
		return commodoreUserRecord{}, errors.New("user is missing required timestamps")
	}
	return commodoreUserRecord{
		ID:               row.ID,
		TenantID:         row.TenantID,
		Email:            row.Email,
		PasswordHash:     row.PasswordHash,
		FirstName:        row.FirstName,
		LastName:         row.LastName,
		Role:             row.Role,
		Permissions:      row.Permissions,
		IsActive:         row.IsActive,
		IsVerified:       row.Verified,
		CreatedAt:        row.CreatedAt.Time,
		UpdatedAt:        row.UpdatedAt.Time,
		PlatformOperator: row.PlatformOperator,
	}, nil
}

func commodoreUserFromProfileRow(row commodoredb.GetUserProfileRow) (commodoreUserRecord, error) {
	if !row.CreatedAt.Valid || !row.UpdatedAt.Valid {
		return commodoreUserRecord{}, errors.New("user is missing required timestamps")
	}
	return commodoreUserRecord{
		ID:               row.ID,
		TenantID:         row.TenantID,
		Email:            row.Email,
		FirstName:        row.FirstName,
		LastName:         row.LastName,
		Role:             row.Role,
		Permissions:      row.Permissions,
		IsActive:         row.IsActive,
		IsVerified:       row.Verified,
		LastLoginAt:      row.LastLoginAt,
		CreatedAt:        row.CreatedAt.Time,
		UpdatedAt:        row.UpdatedAt.Time,
		PlatformOperator: row.PlatformOperator,
	}, nil
}

func commodoreUserFromRefreshRow(row commodoredb.GetRefreshUserRow) (commodoreUserRecord, error) {
	if !row.CreatedAt.Valid || !row.UpdatedAt.Valid {
		return commodoreUserRecord{}, errors.New("user is missing required timestamps")
	}
	return commodoreUserRecord{
		Email:            row.Email,
		Role:             row.Role,
		Permissions:      row.Permissions,
		FirstName:        row.FirstName,
		LastName:         row.LastName,
		IsActive:         row.IsActive,
		IsVerified:       row.Verified,
		CreatedAt:        row.CreatedAt.Time,
		UpdatedAt:        row.UpdatedAt.Time,
		PlatformOperator: row.PlatformOperator,
	}, nil
}

func (u commodoreUserRecord) toProtoUser(userID, tenantID string) *commodorepb.User {
	id := u.ID
	if userID != "" {
		id = userID
	}
	tenant := u.TenantID
	if tenantID != "" {
		tenant = tenantID
	}
	email := u.Email

	result := &commodorepb.User{
		Id:               id,
		TenantId:         tenant,
		Email:            &email,
		FirstName:        u.FirstName.String,
		LastName:         u.LastName.String,
		Role:             u.Role,
		Permissions:      u.Permissions,
		IsActive:         u.IsActive,
		IsVerified:       u.IsVerified,
		PlatformOperator: u.PlatformOperator,
		CreatedAt:        timestamppb.New(u.CreatedAt),
		UpdatedAt:        timestamppb.New(u.UpdatedAt),
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

	srv := &CommodoreServer{
		db:                       cfg.DB,
		dbMaxIdleConns:           cfg.DBMaxIdleConns,
		logger:                   cfg.Logger,
		foghornPool:              cfg.FoghornPool,
		quartermasterClient:      cfg.QuartermasterClient,
		navigatorClient:          cfg.NavigatorClient,
		purserClient:             cfg.PurserClient,
		streamAdmissionBilling:   cfg.PurserClient,
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
	// Assign conditionally so the interface field stays an untyped nil when no
	// client is configured (a nil *qmclient.GRPCClient stored in the interface
	// would defeat the nil-guard in lookupTenantClusterEntitlement).
	if cfg.QuartermasterClient != nil {
		srv.qmEntitlements = cfg.QuartermasterClient
	}
	return srv
}

// resolveClusterRouteForTenant returns the tenant's cached route, building it
// when absent or expired.
//
// The build is collapsed per tenant for the same reason the admission refresh
// is, and more so: it fans out to Quartermaster, Purser, and one Foghorn
// discovery per cluster in the route. At expiry every concurrent request for
// that tenant would otherwise repeat all of it.
func (s *CommodoreServer) resolveClusterRouteForTenant(ctx context.Context, tenantID string) (*clusterRoute, error) {
	if route, ok := s.freshCachedClusterRoute(tenantID); ok {
		return route, nil
	}

	if s.quartermasterClient == nil {
		return nil, status.Error(codes.Unavailable, "quartermaster not available for cluster routing")
	}

	// DoChan, not Do: the shared build must not die with whichever caller
	// happened to lead it. See resolveAdmissionRouteForTenant.
	shared := s.routeBuild.DoChan(tenantID, func() (any, error) {
		// A follower that joined just as the leader finished would otherwise
		// trigger a second identical build.
		if route, ok := s.freshCachedClusterRoute(tenantID); ok {
			return route, nil
		}
		buildCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), routeBuildTimeout)
		defer cancel()
		return s.buildClusterRoute(buildCtx, tenantID)
	})

	select {
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	case result := <-shared:
		if result.Err != nil {
			return nil, result.Err
		}
		route, ok := result.Val.(*clusterRoute)
		if !ok {
			return nil, status.Error(codes.Internal, "cluster route build returned an unexpected type")
		}
		return route, nil
	}
}

// routeBuildTimeout bounds the shared route build, which is detached from any
// one caller's deadline. It is longer than the admission refresh's because the
// build additionally runs Foghorn discovery per cluster.
const routeBuildTimeout = 15 * time.Second

func (s *CommodoreServer) freshCachedClusterRoute(tenantID string) (*clusterRoute, bool) {
	s.routeCacheMu.RLock()
	defer s.routeCacheMu.RUnlock()
	route, ok := s.routeCache[tenantID]
	if !ok || time.Since(route.resolvedAt) >= s.routeCacheTTL {
		return nil, false
	}
	return route, true
}

func (s *CommodoreServer) buildClusterRoute(ctx context.Context, tenantID string) (*clusterRoute, error) {
	resp, allowedClasses, err := s.loadAdmissionRouteInputs(ctx, tenantID, "cluster routing")
	if err != nil {
		return nil, err
	}
	admissionPeers := filterPeersByPolicy(resp.GetClusterPeers(), allowedClasses)
	routingPeers := filterHealthyPeers(admissionPeers)

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
		clusterPeers:            routingPeers,
		admissionPeers:          admissionPeers,
		tenantResourceLimits:    resp.GetTenantResourceLimits(),
		resolvedAt:              time.Now(),
		admissionResolvedAt:     time.Now(),
	}
	for _, cid := range routeClusterIDs(route) {
		discovered := s.discoverFoghornAddrs(ctx, cid)
		route.foghornAddrsByCluster[cid] = dedupeAddrs(append(foghornCandidatesFromRoute(route, cid), discovered...)...)
	}
	normalizeClusterRoute(route)

	// Unconditional, unlike an admission refresh: this resolved every field
	// rather than patching two of them, and singleflight means no second build
	// for this tenant ran alongside it.
	s.routeCacheMu.Lock()
	s.routeCache[tenantID] = route
	s.routeCacheMu.Unlock()

	return route, nil
}

// admissionRouteFreshness bounds how stale peer health and plan entitlement may
// be when they gate a publish.
//
// The route cache exists for addresses and slugs, which change rarely; a
// cluster's health and a tenant's plan do not. Served at route-cache age, a
// cluster that has degraded stays admissible and one that has recovered stays
// rejected for minutes, and a plan change lands just as late. Admission
// re-resolves those two facts on its own, much shorter, clock.
const admissionRouteFreshness = 15 * time.Second

// resolveAdmissionRouteForTenant returns a route whose peer sets are fresh
// enough to decide admission. The static parts stay cached.
//
// Refreshes are collapsed per tenant: when an entry ages out, every concurrent
// admission would otherwise issue its own Quartermaster and Purser lookups for
// the same answer.
func (s *CommodoreServer) resolveAdmissionRouteForTenant(ctx context.Context, tenantID string) (*clusterRoute, error) {
	route, err := s.resolveClusterRouteForTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if time.Since(route.admissionResolvedAt) <= admissionRouteFreshness {
		return route, nil
	}

	// The shared lookup runs on a bounded context of its own, not on whichever
	// caller happened to arrive first: with Do the leader's context is the one
	// captured, so its cancellation would abort the lookup every follower is
	// waiting on. DoChan lets each caller stop waiting when its own context
	// ends, while the shared work continues for the others.
	shared := s.admissionRefresh.DoChan(tenantID, func() (any, error) {
		refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), admissionRefreshTimeout)
		defer cancel()
		return s.refreshAdmissionRoute(refreshCtx, tenantID, route)
	})

	select {
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	case result := <-shared:
		if result.Err != nil {
			return nil, result.Err
		}
		refreshed, ok := result.Val.(*clusterRoute)
		if !ok {
			return nil, status.Error(codes.Internal, "admission route refresh returned an unexpected type")
		}
		return refreshed, nil
	}
}

// admissionRefreshTimeout bounds the shared admission lookup. It is detached
// from any one caller's deadline, so it needs its own.
const admissionRefreshTimeout = 500 * time.Millisecond

func (s *CommodoreServer) refreshAdmissionRoute(ctx context.Context, tenantID string, route *clusterRoute) (*clusterRoute, error) {
	if s.quartermasterClient == nil {
		return nil, status.Error(codes.Unavailable, "quartermaster not available for cluster routing")
	}

	resp, allowedClasses, err := s.loadAdmissionRouteInputs(ctx, tenantID, "cluster routing refresh")
	if err != nil {
		return nil, err
	}

	// Copy rather than mutate: the cached route is shared with in-flight
	// readers, and a torn peer slice would be worse than a stale one.
	refreshed := *route
	refreshed.admissionPeers = filterPeersByPolicy(resp.GetClusterPeers(), allowedClasses)
	refreshed.clusterPeers = filterHealthyPeers(refreshed.admissionPeers)
	refreshed.tenantResourceLimits = resp.GetTenantResourceLimits()
	refreshed.admissionResolvedAt = time.Now()

	return s.installRefreshedAdmissionRoute(tenantID, route, &refreshed), nil
}

// loadAdmissionRouteInputs resolves independent routing and billing-tier facts
// concurrently under the caller's single operation budget. Neither authority
// is a fallback for the other: admission needs both, and any failure denies the
// refresh without caching a partial decision.
func (s *CommodoreServer) loadAdmissionRouteInputs(ctx context.Context, tenantID, routingStage string) (*quartermasterpb.ClusterRoutingResponse, map[string]struct{}, error) {
	var (
		routing        *quartermasterpb.ClusterRoutingResponse
		allowedClasses map[string]struct{}
	)
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		resp, err := s.quartermasterClient.GetClusterRouting(groupCtx, &quartermasterpb.GetClusterRoutingRequest{TenantId: tenantID})
		if err != nil {
			return status.Errorf(codes.Unavailable, "%s failed: %v", routingStage, err)
		}
		routing = resp
		return nil
	})
	group.Go(func() error {
		classes, err := s.allowedClusterClassesForTenant(groupCtx, tenantID)
		if err != nil {
			return err
		}
		allowedClasses = classes
		return nil
	})
	if err := group.Wait(); err != nil {
		return nil, nil, err
	}
	return routing, allowedClasses, nil
}

// installRefreshedAdmissionRoute publishes a refreshed route only over the
// exact entry the refresh started from, and returns the route the caller should
// use.
//
// Two things can have happened while the lookups ran. Another refresh may have
// finished first, in which case overwriting would replace current health with
// older state and stamp it fresh for another window. Or a failed dial may have
// deliberately evicted the entry — reinserting the route it just invalidated
// would undo that. In both cases the cache is left alone and the caller simply
// uses this result.
func (s *CommodoreServer) installRefreshedAdmissionRoute(tenantID string, from, refreshed *clusterRoute) *clusterRoute {
	s.routeCacheMu.Lock()
	defer s.routeCacheMu.Unlock()

	current, ok := s.routeCache[tenantID]
	if !ok {
		// Deliberately evicted while this refresh ran; do not reinsert.
		return refreshed
	}
	if current != from {
		// Something replaced the entry: a newer full-route build, or another
		// refresh. It wins outright rather than on timestamp — these stamp
		// completion, not when Quartermaster and Purser produced the snapshot,
		// so a slow reader of old state can finish last and look newer.
		return current
	}
	s.routeCache[tenantID] = refreshed
	return refreshed
}

// allowedClusterClassesForTenant resolves which cluster classes a tenant's
// plan permits.
//
// A Purser *failure* is not a free tier. Returning the free set on a transport
// error would silently demote a paying tenant, and because the caller caches
// the resulting route, that demotion would stick for the cache TTL and surface
// as CLUSTER_NOT_ENTITLED — a permanent-looking denial produced by a transient
// blip. Errors propagate instead, so the caller can fail closed as transient.
//
// A tenant with no subscription is a real answer, not a failure: Purser's
// bounded admission response returns tier level zero for that case.
func (s *CommodoreServer) allowedClusterClassesForTenant(ctx context.Context, tenantID string) (map[string]struct{}, error) {
	free := map[string]struct{}{"platform_official": {}}
	// Purser not wired up at all is a deployment shape (dev stacks), not a
	// failed lookup.
	if s.purserClient == nil {
		return free, nil
	}
	admission, err := s.purserClient.GetTenantAdmissionStatus(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "tenant admission lookup failed: %v", err)
	}
	if admission == nil {
		return free, nil
	}
	out := map[string]struct{}{"platform_official": {}}
	switch level := admission.GetTierLevel(); {
	case level >= 4:
		out["third_party_marketplace"] = struct{}{}
		out["tenant_private"] = struct{}{}
	case level >= 2:
		out["third_party_marketplace"] = struct{}{}
	}
	return out, nil
}

func findPeerByClusterID(peers []*clusterpeerpb.TenantClusterPeer, clusterID string) *clusterpeerpb.TenantClusterPeer {
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

func filterPeersByPolicy(peers []*clusterpeerpb.TenantClusterPeer, allowedClasses map[string]struct{}) []*clusterpeerpb.TenantClusterPeer {
	if len(peers) == 0 {
		return peers
	}
	out := make([]*clusterpeerpb.TenantClusterPeer, 0, len(peers))
	for _, peer := range peers {
		if peer == nil || !peer.GetAccessActive() || peer.GetSubscriptionStatus() != "active" {
			continue
		}
		if expiry := peer.GetAccessExpiresAt(); expiry != nil && !expiry.AsTime().After(time.Now()) {
			continue
		}
		class := strings.ToLower(strings.TrimSpace(peer.GetClusterClass()))
		switch peer.GetAccessSource() {
		case clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER,
			clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OPERATOR_OVERRIDE:
		case clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PRIVATE_INVITE:
			// Invites are valid provenance only for private capacity. This is a
			// consumer-side backstop against legacy or forged rows attempting to
			// relabel marketplace access and skip its billing-tier policy.
			if class != "tenant_private" {
				continue
			}
		case clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER,
			clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_MARKETPLACE_SUBSCRIPTION:
			if _, ok := allowedClasses[class]; !ok {
				continue
			}
		default:
			continue
		}
		out = append(out, peer)
	}
	return out
}

func filterHealthyPeers(peers []*clusterpeerpb.TenantClusterPeer) []*clusterpeerpb.TenantClusterPeer {
	if len(peers) == 0 {
		return peers
	}
	out := make([]*clusterpeerpb.TenantClusterPeer, 0, len(peers))
	for _, peer := range peers {
		if peer != nil && peerIsHealthy(peer) {
			out = append(out, peer)
		}
	}
	return out
}

func peerIsHealthy(peer *clusterpeerpb.TenantClusterPeer) bool {
	return peer != nil && strings.EqualFold(strings.TrimSpace(peer.GetHealthStatus()), "healthy")
}

func clusterAdmissionPeer(route *clusterRoute, clusterID string) (*clusterpeerpb.TenantClusterPeer, commodorepb.StreamKeyRejectionReason) {
	if route == nil {
		return nil, commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_NOT_ENTITLED
	}
	peer := findPeerByClusterID(route.admissionPeers, clusterID)
	if peer == nil {
		return nil, commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_NOT_ENTITLED
	}
	if !peerIsHealthy(peer) {
		return peer, commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_UNHEALTHY
	}
	return peer, commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_UNSPECIFIED
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

func tierProcessesForLifecycle(tier *purserpb.BillingTier, lifecycle string) string {
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
	row, err := commodoredb.New(s.db).GetStreamProcessingOverrides(ctx, streamID)
	if err != nil {
		return ""
	}
	return processingOverrideForLifecycle(
		lifecycle, row.ProcessesLive, row.ProcessesDvr, row.ProcessesClip,
		row.ProcessesDvrFinalize, row.ProcessesVod,
	)
}

// getTenantProcessingOverride checks commodore.tenant_processing_config for a tenant override.
func (s *CommodoreServer) getTenantProcessingOverride(ctx context.Context, tenantID, lifecycle string) string {
	row, err := commodoredb.New(s.db).GetTenantProcessingOverrides(ctx, tenantID)
	if err != nil {
		return ""
	}
	return processingOverrideForLifecycle(
		lifecycle, row.ProcessesLive, row.ProcessesDvr, row.ProcessesClip,
		row.ProcessesDvrFinalize, row.ProcessesVod,
	)
}

func processingOverrideForLifecycle(lifecycle string, live, dvr, clip, dvrFinalize, vod json.RawMessage) string {
	var override json.RawMessage
	switch normalizeProcessLifecycle(lifecycle) {
	case processLifecycleDVR:
		override = dvr
	case processLifecycleClip:
		override = clip
	case processLifecycleDVRFinalize:
		override = dvrFinalize
	case processLifecycleVOD:
		override = vod
	default:
		override = live
	}
	return string(override)
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

	queries := commodoredb.New(s.db)
	// deleted_at IS NULL: a soft-deleted (two-phase deletion-pending) stream must not resolve for serving.
	streamRoute, err := queries.GetStreamRouteByPlaybackID(ctx, contentID)
	tenantID := sql.NullString{String: streamRoute.TenantID, Valid: streamRoute.TenantID != ""}
	activeClusterID := streamRoute.ActiveIngestClusterID

	if errors.Is(err, sql.ErrNoRows) {
		// Try internal_name lookup (content_id may be "live+<name>")
		name := strings.TrimPrefix(contentID, "live+")
		internalRoute, internalErr := queries.GetStreamRouteByInternalName(ctx, name)
		err = internalErr
		tenantID = sql.NullString{String: internalRoute.TenantID, Valid: internalRoute.TenantID != ""}
		activeClusterID = internalRoute.ActiveIngestClusterID
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

// resolveFoghornForStreamKey resolves a Foghorn client from the ingest stream
// key: it honours an active ingest lease, and otherwise falls back to the
// placement of the tenant that owns the key — not the caller's.
func (s *CommodoreServer) resolveFoghornForStreamKey(ctx context.Context, streamKey string) (*foghornclient.GRPCClient, *clusterRoute, error) {
	if streamKey == "" {
		return nil, nil, status.Error(codes.InvalidArgument, "stream_key required")
	}

	// Freshness is computed by the database, in the same clock domain that
	// writes and expires the claim.
	streamRoute, err := commodoredb.New(s.db).GetStreamRouteByKey(ctx, commodoredb.GetStreamRouteByKeyParams{
		StreamKey: streamKey, Column2: int64(activeIngestLease.Seconds()),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, status.Error(codes.NotFound, "stream key not found")
	}
	if err != nil {
		return nil, nil, status.Errorf(codes.Internal, "database error resolving stream key: %v", err)
	}

	// The placement field is a lease, not a permanent route. Once it expires,
	// resolve through the tenant route rather than pinning endpoint discovery to
	// a cluster that no longer ingests the stream.
	//
	// While it holds, though, it is authoritative: that cluster owns ingest and
	// any other cluster would reject the publish as a duplicate. So a failure to
	// reach it is transient, not a reason to hand back a different cluster's
	// endpoint that we already know the publisher cannot use.
	tenantID := streamRoute.TenantID
	activeClusterID := streamRoute.ActiveIngestClusterID
	if activeClusterID.Valid && activeClusterID.String != "" && streamRoute.LeaseFresh {
		leasedClusterID := activeClusterID.String
		if client, ok := s.foghornPool.Get(foghornPoolKey(leasedClusterID, "")); ok {
			return client, &clusterRoute{clusterID: leasedClusterID}, nil
		}
		if tenantID == "" {
			return nil, nil, status.Errorf(codes.Unavailable,
				"stream holds an active ingest lease on cluster %s but its tenant is unknown", leasedClusterID)
		}
		client, clusterErr := s.resolveFoghornForCluster(ctx, leasedClusterID, tenantID)
		if clusterErr != nil {
			return nil, nil, status.Errorf(codes.Unavailable,
				"ingest is leased to cluster %s, which could not be resolved: %v", leasedClusterID, clusterErr)
		}
		return client, &clusterRoute{clusterID: leasedClusterID}, nil
	}

	// Fall back to tenant-based routing (populates pool for next time)
	if tenantID == "" {
		return nil, nil, status.Error(codes.NotFound, "stream key has no tenant association")
	}
	client, route, err := s.resolveFoghornForTenant(ctx, tenantID)
	if err != nil {
		return nil, nil, err
	}
	return client, route, nil
}

func (s *CommodoreServer) resolveArtifactRouteForContent(ctx context.Context, contentID string) (bool, sql.NullString, sql.NullString, error) {
	row, err := commodoredb.New(s.db).GetArtifactRouteByContent(ctx, contentID)
	tenantID := sql.NullString{String: row.TenantID, Valid: row.TenantID != ""}
	clusterID := sql.NullString{String: row.ClusterID, Valid: row.ClusterID != ""}
	if errors.Is(err, sql.ErrNoRows) {
		return false, tenantID, clusterID, nil
	}
	if err != nil {
		return false, tenantID, clusterID, status.Errorf(codes.Internal, "database error resolving artifact content: %v", err)
	}
	return true, tenantID, clusterID, nil
}

func clusterInPeers(peers []*clusterpeerpb.TenantClusterPeer, clusterID string) bool {
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

// liveIngestClusterID answers which media cluster a publish for this stream
// belongs to.
//
// A caller that named a cluster is speaking for the cluster it runs, and that
// answer stands (subject to entitlement) — a Foghorn handling the stream knows
// where it landed better than any recorded column. Only when nothing was named
// does the recorded ingest lease decide, and only while it is fresh: the
// publisher is mid-session on that cluster, so routing a reconnect to the
// tenant's default instead would hand them an endpoint PUSH_REWRITE refuses as
// a duplicate. With no lease and no declaration, the tenant's route decides.
func liveIngestClusterID(route *clusterRoute, requestedClusterID string, leasedClusterID sql.NullString, leaseFresh sql.NullBool) string {
	if requestedClusterID == "" && leaseFresh.Valid && leaseFresh.Bool && leasedClusterID.Valid {
		if leased := strings.TrimSpace(leasedClusterID.String); leased != "" {
			return leased
		}
	}
	return resolveLiveIngestClusterID(route, requestedClusterID)
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

func hasTenantResourceLimits(limits *tenantlimitspb.TenantResourceLimits) bool {
	return limits != nil && (limits.GetMaxStreams() > 0 || limits.GetMaxViewers() > 0)
}

func mergeTenantResourceLimits(base, override *tenantlimitspb.TenantResourceLimits) *tenantlimitspb.TenantResourceLimits {
	if !hasTenantResourceLimits(base) {
		if hasTenantResourceLimits(override) {
			return override
		}
		return nil
	}
	merged := &tenantlimitspb.TenantResourceLimits{
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
func (s *CommodoreServer) ValidateStreamKey(ctx context.Context, req *commodorepb.ValidateStreamKeyRequest) (*commodorepb.ValidateStreamKeyResponse, error) {
	streamKey := req.GetStreamKey()
	if streamKey == "" {
		return &commodorepb.ValidateStreamKeyResponse{
			Valid: false,
			Error: "stream_key required",
		}, nil
	}
	// A claim must have an owner. Declaring a cluster is what takes the
	// placement claim, so a caller that names one without naming the publisher
	// connection would take a claim nobody can give back: release is
	// owner-fenced, so an unowned claim can only expire, holding the stream's
	// placement for the rest of the lease window. Refused before any write.
	// Callers that name no cluster (the GraphQL/MCP key check) take no claim and
	// need no token.
	if strings.TrimSpace(req.GetClusterId()) != "" && strings.TrimSpace(req.GetClaimToken()) == "" {
		return nil, status.Error(codes.InvalidArgument, "claim_token is required when cluster_id is set")
	}

	queries := commodoredb.New(s.db)
	admission, err := queries.GetStreamAdmissionByKey(ctx, streamKey)

	if errors.Is(err, sql.ErrNoRows) {
		return &commodorepb.ValidateStreamKeyResponse{
			Valid:           false,
			Error:           "Invalid stream key",
			RejectionReason: commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_INVALID_KEY,
		}, nil
	}

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"stream_key": logging.RedactSecret(streamKey),
			"error":      err,
		}).Error("Database error validating stream key")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	if !admission.IsActive.Bool {
		return &commodorepb.ValidateStreamKeyResponse{
			Valid:           false,
			Error:           "User account is inactive",
			RejectionReason: commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_USER_INACTIVE,
		}, nil
	}
	if admission.IngestMode == "pull" {
		return &commodorepb.ValidateStreamKeyResponse{
			Valid:           false,
			Error:           "Pull streams do not accept push ingest",
			RejectionReason: commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_PULL_MODE,
		}, nil
	}

	resp := &commodorepb.ValidateStreamKeyResponse{
		Valid:              true,
		UserId:             admission.UserID,
		TenantId:           admission.TenantID,
		InternalName:       admission.InternalName,
		IsRecordingEnabled: admission.IsRecordingEnabled.Bool,
		StreamId:           admission.ID,
		PlaybackId:         admission.PlaybackID,
	}

	// A request without a cluster is an identity-only key check. It is used to
	// scope Foghorn's open-session lookup and by the public validation tools;
	// neither caller needs tenant routing, billing/config snapshots, decrypted
	// push targets, process JSON, or a placement write.
	requestedClusterID := strings.TrimSpace(req.GetClusterId())
	if requestedClusterID == "" {
		return resp, nil
	}

	// Plan-aware cluster admission. The admission envelope is filtered by
	// allowedClusterClassesForTenant against the peer's cluster_class metadata,
	// but retains health state so an entitled unhealthy cluster can be
	// distinguished from an unentitled cluster.
	//
	// A route that will not resolve is a transient failure, not an admission:
	// falling through would let a direct RTMP/SRT/WHIP push bypass the
	// entitlement gate the resolver applies — and then claim the placement
	// lease on an unverified cluster — for as long as the control plane is
	// down. Unavailable instead, before any placement write.
	route, routeErr := s.resolveAdmissionRouteForTenant(ctx, admission.TenantID)
	if routeErr != nil {
		s.logger.WithError(routeErr).WithFields(logging.Fields{
			"tenant_id":  admission.TenantID,
			"cluster_id": requestedClusterID,
		}).Warn("ValidateStreamKey: cluster route lookup failed; failing closed as transient")
		return nil, status.Errorf(codes.Unavailable, "cluster route lookup failed: %v", routeErr)
	}
	{
		peer, rejectionReason := clusterAdmissionPeer(route, requestedClusterID)
		if rejectionReason == commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_NOT_ENTITLED {
			s.logger.WithFields(logging.Fields{
				"tenant_id":  admission.TenantID,
				"cluster_id": requestedClusterID,
			}).Warn("ValidateStreamKey rejected: cluster not entitled or filtered by plan policy")
			return &commodorepb.ValidateStreamKeyResponse{
				Valid:           false,
				Error:           "Tenant not entitled to ingest cluster " + requestedClusterID,
				RejectionReason: commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_NOT_ENTITLED,
			}, nil
		}
		if rejectionReason == commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_UNHEALTHY {
			peerStatus := strings.TrimSpace(peer.GetHealthStatus())
			if peerStatus == "" {
				peerStatus = "unknown"
			}
			return &commodorepb.ValidateStreamKeyResponse{
				Valid:           false,
				Error:           "Ingest cluster " + requestedClusterID + " is " + peerStatus,
				RejectionReason: commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_UNHEALTHY,
			}, nil
		}
	}

	// Get billing status via Purser gRPC (not direct DB access)
	// Rated ingest fails closed when billing authority is unavailable. This
	// still allows the Gateway's explicit onboarding/configuration operations,
	// but it never turns a provisioning gap or Purser outage into postpaid use.
	billingModel := "prepaid"
	var isSuspended bool
	isBalanceNegative := true
	var dvrPolicy *sharedpb.DVRPolicy
	var allowances []*meteringpb.MeterAllowance
	var tenantResourceLimits *tenantlimitspb.TenantResourceLimits

	if s.streamAdmissionBilling != nil {
		billingStatus, err := s.streamAdmissionBilling.GetTenantBillingStatus(ctx, admission.TenantID)
		if err != nil {
			s.logger.WithFields(logging.Fields{
				"tenant_id": admission.TenantID,
				"error":     err,
			}).Warn("Failed to get billing status from Purser; rated ingest remains blocked")
		} else {
			billingModel = billingStatus.BillingModel
			isSuspended = billingStatus.IsSuspended
			isBalanceNegative = billingStatus.IsBalanceNegative
			dvrPolicy = billingStatus.DvrPolicy
			allowances = billingStatus.Allowances
			tenantResourceLimits = billingStatus.GetTenantResourceLimits()
		}
	}

	resp.BillingModel = billingModel
	resp.IsSuspended = isSuspended
	resp.IsBalanceNegative = isBalanceNegative
	resp.DvrPolicy = dvrPolicy
	resp.Allowances = allowances
	resp.TenantResourceLimits = tenantResourceLimits

	if route, err := s.resolveClusterRouteForTenant(ctx, admission.TenantID); err == nil {
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
	pushRows, pushErr := queries.ListEnabledPushTargets(ctx, commodoredb.ListEnabledPushTargetsParams{
		StreamID: admission.ID, TenantID: admission.TenantID,
	})
	if pushErr != nil {
		s.logger.WithError(pushErr).WithField("stream_id", admission.ID).Warn("Failed to load push targets")
	} else {
		for _, row := range pushRows {
			t := commodorepb.PushTargetInternal{
				Id: row.ID, Platform: row.Platform.String, Name: row.Name, TargetUri: row.TargetUri,
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
	resp.ProcessesJson = s.resolveProcessesJSON(ctx, admission.TenantID, admission.ID, processClusterID, "live")
	resp.DvrProcessesJson = s.resolveProcessesJSON(ctx, admission.TenantID, admission.ID, processClusterID, "dvr")

	// Track the media cluster this stream ingests on.
	//
	// Only a caller that names its ingest cluster claims placement. This RPC is
	// also used for plain key validation — the GraphQL and MCP validate tools,
	// and x402 resource resolution — which pass no cluster; deriving one from
	// the tenant's route would let a validation or payment lookup take the
	// 30-second lease and make the real ingest fail with DUPLICATE_INGEST on
	// another cluster. PUSH_REWRITE is the only push-ingest caller that names
	// its cluster and therefore claims through this RPC.
	// The value written is still the resolved origin (a Foghorn on a central
	// cluster records the media cluster ingest actually lands on); only the
	// decision to write at all is gated on the caller declaring one.
	activeIngestClusterID := ""
	if strings.TrimSpace(req.GetClusterId()) != "" {
		activeIngestClusterID = req.GetClusterId()
		if originClusterID := resp.GetOriginClusterId(); originClusterID != "" {
			activeIngestClusterID = originClusterID
		}
	}
	if activeIngestClusterID != "" {
		// The claim is stamped with its owner — the publisher connection that
		// took it — so a later release can prove the claim is its own.
		//
		// Same cluster is NOT the same owner. A second connection reaching this
		// cluster must not overwrite the live owner's token: it would then be
		// told it holds the claim, and its own rejection would hand back
		// placement belonging to the publisher that is actually streaming. So a
		// claim that is currently held may only be refreshed BY ITS OWNER;
		// anyone else has to wait for it to lapse, exactly as they would in
		// another cluster. A live claim naming no owner is not adoptable either
		// — every writer names one, so an unowned live claim is an anomaly, and
		// adopting it is how a rejected adopter would gain licence to release
		// somebody else's placement.
		//
		// The pre-state is captured in the same statement, under the row lock,
		// so claim_acquired can distinguish RESERVING a free claim from
		// REFRESHING one already held. Only a reservation may be released on
		// rejection: giving back a refresh would unpin the live session that
		// re-fired PUSH_REWRITE.
		claimToken := strings.TrimSpace(req.GetClaimToken())
		claim, claimErr := queries.AcquireIngestClaim(ctx, commodoredb.AcquireIngestClaimParams{
			ClusterID:    sql.NullString{String: activeIngestClusterID, Valid: true},
			ClaimToken:   sql.NullString{String: claimToken, Valid: true},
			LeaseSeconds: int64(activeIngestLease.Seconds()), LookupStreamKey: streamKey,
		})
		switch {
		case claimErr != nil && !errors.Is(claimErr, sql.ErrNoRows):
			s.logger.WithError(claimErr).WithField("stream_key", logging.RedactSecret(streamKey)).Warn("Failed to record ingest cluster")
		case claimErr == nil:
			// Reserved rather than refreshed when nothing live held it, or when
			// the live claim was in another cluster's name or ownerless.
			reserved := !claim.PrevFresh ||
				claim.PrevCluster.String != activeIngestClusterID ||
				claim.PrevToken.String != claimToken
			resp.ClaimAcquired = reserved
		default:
			// Concurrent-claim guard: a live claim exists and it is not this
			// connection's. Single-active-ingest per stream is a hard
			// invariant, and the owner is what decides that — a second
			// publisher landing in the SAME cluster is just as much a
			// duplicate as one in a peer, and must be told so rather than
			// handed valid=true with no claim. Belt-and-suspenders to the
			// PeerChannel StreamAdvertisement broadcast (which surfaces the
			// same fact at federation cadence ~10s; this gate fires
			// synchronously at admission time).
			held, scanErr := queries.GetActiveIngestClaim(ctx, streamKey)
			if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
				s.logger.WithError(scanErr).WithField("stream_key", logging.RedactSecret(streamKey)).Warn("ValidateStreamKey: active-ingest lookup failed")
			}
			if held.ActiveIngestClusterID.Valid && held.ActiveIngestClusterID.String != "" && held.ActiveIngestClaimID.String != claimToken {
				where := "cluster " + held.ActiveIngestClusterID.String
				if held.ActiveIngestClusterID.String == activeIngestClusterID {
					where = "this cluster by another publisher"
				}
				s.logger.WithFields(logging.Fields{
					"stream_key":            logging.RedactSecret(streamKey),
					"requesting_cluster_id": activeIngestClusterID,
					"active_ingest_cluster": held.ActiveIngestClusterID.String,
				}).Warn("ValidateStreamKey rejected: duplicate ingest claim against a live claim held by another publisher")
				return &commodorepb.ValidateStreamKeyResponse{
					Valid:           false,
					Error:           "Stream is already ingesting on " + where,
					RejectionReason: commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_DUPLICATE_INGEST,
				}, nil
			}
			s.logger.WithFields(logging.Fields{
				"stream_key":        logging.RedactSecret(streamKey),
				"ingest_cluster_id": activeIngestClusterID,
			}).Debug("Skipped ingest cluster update: this connection already holds the claim")
		}
	}

	return resp, nil
}

// ResolveStreamContext returns the same materialization fact set as
// ValidateStreamKey, keyed by any of stream_id / playback_id / internal_name /
// stream_key. Unlike ValidateStreamKey it claims no ingest placement, which is
// what makes it usable for resolution rather than admission.
//
// Callers: Foghorn's managed-stream reconciler for ingest modes that bypass
// PUSH_REWRITE (notably mist_native); Foghorn's stream-registry hydrate on the
// live PLAY_REWRITE playback-resolve path, where it also supplies requires_auth
// and cluster_peers; and ingest-endpoint resolution (HTTP front door and gRPC),
// which uses the stream_key identifier.
//
// Admission semantics: `admitted` rolls user-active, cluster entitlement,
// suspension, and negative-balance into a single boolean. Free-tier-load and
// per-tenant-cap are NOT enforced here (they live in Foghorn's PUSH_REWRITE
// path); the facts needed to layer those checks caller-side are returned in
// the response. Those two missing gates are INGEST-time admission concerns and
// do not apply to playback resolution, so the playback caller is unaffected;
// the playback-relevant gates (suspension, negative-balance) are already inside
// `admitted`. Widening *managed-stream* ownership to tenants still requires
// implementing those ingest gates (see cli/pkg/bootstrap/render.go:
// mistNativeStreamToRendered, which rejects non-system tenants) before relaxing
// the render-layer constraint.
func (s *CommodoreServer) ResolveStreamContext(ctx context.Context, req *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
	var (
		arg   string
		field string
		// A stream key is a publishing credential, not an identifier: it must
		// never reach a log line.
		identifierIsSecret bool
		// Only a publisher holds the stream key, so a resolve by key is asking
		// where to publish rather than reporting where a stream already landed.
		identifierIsPublishIntent bool
	)
	switch id := req.GetIdentifier().(type) {
	case *commodorepb.ResolveStreamContextRequest_StreamId:
		field, arg = "stream_id", id.StreamId
	case *commodorepb.ResolveStreamContextRequest_PlaybackId:
		field, arg = "playback_id", id.PlaybackId
	case *commodorepb.ResolveStreamContextRequest_InternalName:
		field, arg = "internal_name", id.InternalName
	case *commodorepb.ResolveStreamContextRequest_StreamKey:
		// Soft-deleted streams are excluded here: a stream key is held by the
		// publisher and outlives deletion in encoder configs, so a deleted
		// stream must stop accepting it. Matches ValidateStreamKey's lookup.
		field, arg = "stream_key", id.StreamKey
		identifierIsSecret = true
		identifierIsPublishIntent = true
	default:
		return nil, status.Error(codes.InvalidArgument, "identifier required (stream_id | playback_id | internal_name | stream_key)")
	}
	if arg == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s must be non-empty", field)
	}

	resolved, err := commodoredb.New(s.db).ResolveStreamContextByIdentifier(ctx, commodoredb.ResolveStreamContextByIdentifierParams{
		LeaseSeconds: int64(activeIngestLease.Seconds()), IdentifierKind: field, IdentifierValue: arg,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return &commodorepb.ResolveStreamContextResponse{
			Admitted:        false,
			AdmissionReason: "stream not found",
			RejectionReason: commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_INVALID_KEY,
		}, nil
	}
	if err != nil {
		loggedIdentifier := arg
		if identifierIsSecret {
			loggedIdentifier = "<redacted>"
		}
		s.logger.WithFields(logging.Fields{
			"identifier_field": field,
			"identifier_value": loggedIdentifier,
			"error":            err,
		}).Error("Database error resolving stream context")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	resp := &commodorepb.ResolveStreamContextResponse{
		StreamId:           resolved.ID,
		PlaybackId:         resolved.PlaybackID,
		InternalName:       resolved.InternalName,
		IngestMode:         resolved.IngestMode,
		TenantId:           resolved.TenantID,
		UserId:             resolved.UserID,
		IsRecordingEnabled: resolved.IsRecordingEnabled.Bool,
		RequiresAuth:       resolved.RequiresAuth,
	}

	if !resolved.IsActive.Bool {
		resp.Admitted = false
		resp.AdmissionReason = "User account is inactive"
		resp.RejectionReason = commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_USER_INACTIVE
		return resp, nil
	}

	// Cluster admission. Two caller shapes reach here.
	//
	// A caller that names a cluster is asserting where this stream is landing —
	// every Foghorn trigger, reconciler, and registry path, each speaking for
	// the cluster it runs. That named cluster is gated.
	//
	// A publish-intent resolve names nothing, because it cannot: it holds a
	// stream key and is asking where to publish. It is gated on the envelope
	// instead of on one cluster. Picking a cluster here and gating that would
	// reject a publisher whose default cluster is degraded while another
	// authorized cluster is healthy — the caller ranks across the whole
	// envelope, so admission has to ask the same question the caller will.
	// A publisher that already holds the stream is the exception: their
	// reconnect can only go back to the claiming cluster, so that one is gated.
	//
	// mist_native streams without allowed_cluster_ids name nothing and are not
	// publish-intent; they skip the gate and rely on placement scoping.
	//
	// Fail-closed-as-transient: whenever there is anything to gate, the route
	// MUST resolve so entitlement and health can be checked. A Quartermaster
	// blip returns codes.Unavailable, which callers treat as transient —
	// preserve existing applied state, do not newly Apply onto an unverified
	// cluster.
	requestedClusterID := strings.TrimSpace(req.GetClusterId())
	admissionClusterID := requestedClusterID
	var admissionRoute *clusterRoute
	if admissionClusterID != "" || identifierIsPublishIntent {
		route, routeErr := s.resolveAdmissionRouteForTenant(ctx, resolved.TenantID)
		if routeErr != nil {
			s.logger.WithError(routeErr).WithFields(logging.Fields{
				"tenant_id":  resolved.TenantID,
				"cluster_id": requestedClusterID,
			}).Warn("ResolveStreamContext: cluster route lookup failed; failing closed as transient")
			return nil, status.Errorf(codes.Unavailable, "cluster route lookup failed: %v", routeErr)
		}
		admissionRoute = route
		if admissionClusterID == "" && resolved.LeaseFresh && resolved.ActiveIngestClusterID.Valid {
			admissionClusterID = strings.TrimSpace(resolved.ActiveIngestClusterID.String)
		}

		if admissionClusterID != "" {
			peer, rejectionReason := clusterAdmissionPeer(route, admissionClusterID)
			if rejectionReason == commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_NOT_ENTITLED {
				resp.Admitted = false
				resp.AdmissionReason = "Tenant not entitled to cluster " + admissionClusterID
				resp.RejectionReason = commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_NOT_ENTITLED
				return resp, nil
			}
			if rejectionReason == commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_UNHEALTHY {
				peerStatus := strings.TrimSpace(peer.GetHealthStatus())
				if peerStatus == "" {
					peerStatus = "unknown"
				}
				resp.Admitted = false
				resp.AdmissionReason = "Cluster " + admissionClusterID + " is " + peerStatus
				resp.RejectionReason = commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_UNHEALTHY
				return resp, nil
			}
		} else if len(route.admissionPeers) == 0 {
			// No cluster the tenant's plan permits at all.
			resp.Admitted = false
			resp.AdmissionReason = "Tenant is not entitled to any ingest cluster"
			resp.RejectionReason = commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_NOT_ENTITLED
			return resp, nil
		} else if len(route.clusterPeers) == 0 {
			// Entitled, but nothing healthy to publish into. Reported as its
			// own reason: a degraded fleet is not a plan problem.
			resp.Admitted = false
			resp.AdmissionReason = "No healthy ingest cluster available for tenant"
			resp.RejectionReason = commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_UNHEALTHY
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
		s.logger.WithField("tenant_id", resolved.TenantID).
			Warn("ResolveStreamContext: purser client not configured; failing closed as transient")
		return nil, status.Error(codes.Unavailable, "billing status: purser client not configured")
	}
	billingStatus, err := s.purserClient.GetTenantBillingStatus(ctx, resolved.TenantID)
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": resolved.TenantID,
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

	// A live claim pins placement; see the field's contract. Populated
	// regardless of identifier, since every caller routes on the same fact.
	if resolved.LeaseFresh && resolved.ActiveIngestClusterID.Valid {
		if leased := strings.TrimSpace(resolved.ActiveIngestClusterID.String); leased != "" {
			resp.ActiveIngestClusterId = &leased
		}
	}

	// Routing fields (origin/official cluster, peers, resource-limit merge) —
	// same shape ValidateStreamKey returns.
	//
	// The admission route is reused when one was resolved. Looking it up again
	// would let the response route on a different envelope than the one that
	// passed the gate: an eviction between the two (a failed Foghorn dial does
	// that) makes the second lookup fail, and the response would otherwise be
	// admitted carrying no routing at all.
	route := admissionRoute
	if route == nil {
		if cached, err := s.resolveClusterRouteForTenant(ctx, resolved.TenantID); err == nil {
			route = cached
		}
	}
	if route != nil {
		resolvedOriginClusterID := liveIngestClusterID(
			route, requestedClusterID, resolved.ActiveIngestClusterID,
			sql.NullBool{Bool: resolved.LeaseFresh, Valid: true},
		)
		resp.OriginClusterId = &resolvedOriginClusterID
		if route.officialClusterID != "" {
			resp.OfficialClusterId = &route.officialClusterID
		}
		resp.ClusterPeers = route.clusterPeers
		if hasTenantResourceLimits(route.tenantResourceLimits) {
			resp.TenantResourceLimits = mergeTenantResourceLimits(resp.TenantResourceLimits, route.tenantResourceLimits)
		}
	}

	// A publish-intent resolve that reached here without routing has nothing to
	// hand a publisher: the cluster that passed the gate could not be named.
	// Fail closed as transient rather than admit an unroutable publish.
	if identifierIsPublishIntent && strings.TrimSpace(resp.GetOriginClusterId()) == "" {
		return nil, status.Error(codes.Unavailable, "cluster routing unavailable for publish")
	}

	// Processes JSON via the same tier/override resolution path ValidateStreamKey
	// uses. Stream type is "live" for mist_native (it serves a live manifest).
	processClusterID := ""
	if resp.OriginClusterId != nil {
		processClusterID = *resp.OriginClusterId
	}
	resp.ProcessesJson = s.resolveProcessesJSON(ctx, resolved.TenantID, resolved.ID, processClusterID, "live")
	resp.DvrProcessesJson = s.resolveProcessesJSON(ctx, resolved.TenantID, resolved.ID, processClusterID, "dvr")

	// Final admission decision: facts above were collected; now collapse the
	// billing gates that PUSH_REWRITE applies (lines 1092-1110 in
	// triggers/processor.go) into the admitted boolean. Free-tier-load and
	// per-tenant-cap remain caller-side because they require Foghorn's local
	// capacity state.
	switch {
	case resp.IsSuspended:
		resp.Admitted = false
		resp.AdmissionReason = "Tenant is suspended"
		resp.RejectionReason = commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_TENANT_SUSPENDED
	case resp.IsBalanceNegative:
		resp.Admitted = false
		resp.AdmissionReason = "Tenant balance is negative"
		resp.RejectionReason = commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_BALANCE_NEGATIVE
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
func (s *CommodoreServer) ListManagedStreams(ctx context.Context, req *commodorepb.ListManagedStreamsRequest) (*commodorepb.ListManagedStreamsResponse, error) {
	clusterID := strings.TrimSpace(req.GetClusterId())
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}

	rows, err := commodoredb.New(s.db).ListManagedStreams(ctx, clusterID)
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"cluster_id": clusterID,
			"error":      err,
		}).Error("Database error listing managed streams")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	resp := &commodorepb.ListManagedStreamsResponse{}
	for _, dbRow := range rows {
		row := &commodorepb.ManagedStreamRow{
			StreamId: dbRow.StreamID, PlaybackId: dbRow.PlaybackID,
			InternalName: dbRow.InternalName, TenantId: dbRow.TenantID,
			IngestMode: dbRow.IngestMode, SourceSpec: dbRow.SourceSpec,
			SourceKind: dbRow.SourceKind, AlwaysOn: dbRow.AlwaysOn,
			PlacementCount: dbRow.PlacementCount, AllowedClusterIds: dbRow.AllowedClusterIds,
		}
		if row.PlacementCount <= 0 {
			row.PlacementCount = 1
		}
		resp.Streams = append(resp.Streams, row)
	}
	return resp, nil
}

// ListStreamMonitoring returns a tenant's streams with their per-stream
// Skipper monitoring toggle. Service-to-service (Skipper). Skipper keys its
// monitored set and scoped Periscope reads on stream_id (the public UUID =
// commodore.streams.id); internal_name is returned only for logging. The
// nullable column maps: NULL maps to INHERIT, TRUE maps to ON, FALSE to OFF.
func (s *CommodoreServer) ListStreamMonitoring(ctx context.Context, req *commodorepb.ListStreamMonitoringRequest) (*commodorepb.ListStreamMonitoringResponse, error) {
	tenantID := strings.TrimSpace(req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	rows, err := commodoredb.New(s.db).ListStreamMonitoring(ctx, tenantID)
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Error("Database error listing stream monitoring")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	resp := &commodorepb.ListStreamMonitoringResponse{}
	for _, dbRow := range rows {
		row := &commodorepb.StreamMonitoringRow{
			StreamId: dbRow.StreamID, InternalName: dbRow.InternalName,
			MonitoringToggle: monitoringToggleFromNullBool(dbRow.MonitoringEnabled),
		}
		resp.Streams = append(resp.Streams, row)
	}
	return resp, nil
}

// managedClaimToken is the owner a managed stream's placement claim is stamped
// with. Push ingest is owned by the publisher connection that took the claim;
// a managed stream has no connection, and its reconciler is the stream's single
// managed writer, so the stream itself is the owner. Namespaced so it can never
// collide with a Mist trigger UUID.
func managedClaimToken(streamID string) string { return "managed:" + streamID }

// pullClaimToken is the same idea for a pull stream, whose placement is written
// by its source resolver rather than by a publisher connection.
func pullClaimToken(streamID string) string { return "pull:" + streamID }

// RecordStreamActiveCluster updates commodore.streams.active_ingest_cluster_id
// for a managed stream. Mirrors the contended-update guard from
// ValidateStreamKey's push-ingest path so a stale claim cannot overwrite a
// fresh lease held by a different cluster.
func (s *CommodoreServer) RecordStreamActiveCluster(ctx context.Context, req *commodorepb.RecordStreamActiveClusterRequest) (*commodorepb.RecordStreamActiveClusterResponse, error) {
	// SERVICE-TOKEN ONLY: this mutates cluster placement from a caller-supplied stream_id + cluster_id. Only Foghorn
	// (service auth) records active ingest; a JWT caller must not steer placement.
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "RecordStreamActiveCluster requires service token auth")
	}
	streamID := strings.TrimSpace(req.GetStreamId())
	clusterID := strings.TrimSpace(req.GetClusterId())
	tenantID := strings.TrimSpace(req.GetTenantId())
	if streamID == "" || clusterID == "" || tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id, cluster_id and tenant_id required")
	}
	// Records ONLY active ingest placement — NOT thumbnail_serving_cluster_ids, whose sole writer is the service-fenced
	// RegisterStreamThumbnailServingCell (register-before-mint). Tenant-scoped per the repo tenant-filter rule.
	//
	// Ownership is the same invariant push ingest holds: a claim that is live may be refreshed only by the owner that
	// took it, so a managed reconciler cannot take over a push publisher's placement (nor another managed writer's) by
	// virtue of naming the same cluster. A managed placement is owned by its stream — the reconciler is that stream's
	// single managed writer — so the token is derived from the stream id rather than a connection.
	rows, err := commodoredb.New(s.db).RecordManagedStreamActiveCluster(ctx, commodoredb.RecordManagedStreamActiveClusterParams{
		ClusterID:    sql.NullString{String: clusterID, Valid: true},
		ClaimToken:   sql.NullString{String: managedClaimToken(streamID), Valid: true},
		StreamID:     streamID,
		TenantID:     tenantID,
		LeaseSeconds: int64(activeIngestLease.Seconds()),
	})
	if err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"stream_id":  streamID,
			"cluster_id": clusterID,
		}).Error("RecordStreamActiveCluster: update failed")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	return &commodorepb.RecordStreamActiveClusterResponse{Updated: rows > 0}, nil
}

// RegisterStreamThumbnailServingCell durably records a media cluster as a thumbnail-serving cell for a LIVE stream,
// the fence Foghorn takes BEFORE minting a live-thumbnail upload URL. The update unions the cell into
// thumbnail_serving_cluster_ids and is fenced by `deleted_at IS NULL`, so it serializes with DeleteStream's soft-delete
// on the same row: a registration that commits first is included in the deletion's cleanup fan-out; if deletion wins,
// this matches zero rows and returns registered=false, and Foghorn refuses to mint (no orphan bytes). Idempotent — a
// re-register of an already-recorded cell on a live stream still matches the row (registered=true). Tenant-scoped.
func (s *CommodoreServer) RegisterStreamThumbnailServingCell(ctx context.Context, req *commodorepb.RegisterStreamThumbnailServingCellRequest) (*commodorepb.RegisterStreamThumbnailServingCellResponse, error) {
	// SERVICE-TOKEN ONLY: this mutates durable cleanup authority from a request-supplied tenant_id + cluster_id. The
	// shared gRPC server also accepts JWTs, so without this a JWT caller could poison a stream's ownership with a bogus
	// cluster and block deletion convergence. Only Foghorn (service auth) registers a serving cell.
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "RegisterStreamThumbnailServingCell requires service token auth")
	}
	streamID := strings.TrimSpace(req.GetStreamId())
	tenantID := strings.TrimSpace(req.GetTenantId())
	clusterID := strings.TrimSpace(req.GetClusterId())
	if streamID == "" || tenantID == "" || clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id, tenant_id and cluster_id required")
	}
	// Single UPDATE + RowsAffected so registration LINEARIZES with DeleteStream: a concurrent soft-delete either commits
	// first (this UPDATE's re-check sees deleted_at set → 0 rows → registered=false) or after (this appends the cell →
	// registered=true and the cell is in the deletion's cleanup fan-out). A split UPDATE-then-SELECT would race (the
	// UPDATE's EvalPlanQual re-check and the SELECT's statement snapshot can disagree under a concurrent delete). The
	// per-mint write frequency is bounded by Foghorn's registration cache, so re-writing updated_at on an
	// already-recorded cell is negligible. Tenant-scoped.
	rows, err := commodoredb.New(s.db).RegisterStreamThumbnailServingCell(ctx, commodoredb.RegisterStreamThumbnailServingCellParams{
		ClusterID: clusterID,
		StreamID:  streamID,
		TenantID:  tenantID,
	})
	if err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"stream_id":  streamID,
			"cluster_id": clusterID,
		}).Error("RegisterStreamThumbnailServingCell: update failed")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	return &commodorepb.RegisterStreamThumbnailServingCellResponse{Registered: rows > 0}, nil
}

// ClearStreamActiveCluster clears commodore.streams.active_ingest_cluster_id
// for a managed stream once Foghorn has confirmed via heartbeat snapshot
// that Mist no longer has the config. expected_cluster_id guards against
// clobbering a fresher claim from a peer cluster — the column only
// transitions to NULL when the recorded value matches the caller.
func (s *CommodoreServer) ClearStreamActiveCluster(ctx context.Context, req *commodorepb.ClearStreamActiveClusterRequest) (*commodorepb.ClearStreamActiveClusterResponse, error) {
	// SERVICE-TOKEN ONLY: same reasoning as RecordStreamActiveCluster, whose
	// guard this mirrors. Clearing placement from caller-supplied identifiers
	// is a placement mutation, not a user operation.
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "ClearStreamActiveCluster requires service token auth")
	}
	streamID := strings.TrimSpace(req.GetStreamId())
	expected := strings.TrimSpace(req.GetExpectedClusterId())
	tenantID := strings.TrimSpace(req.GetTenantId())
	if streamID == "" || expected == "" || tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id, expected_cluster_id and tenant_id required")
	}
	// Tenant-scoped per the repo tenant-filter rule, like the Record path, and
	// owner-fenced like every other release: a retract may only clear the
	// placement its own reconciler recorded. Without that, a delayed retract
	// arriving after a push publisher took the same cluster would clear that
	// publisher's claim.
	rows, err := commodoredb.New(s.db).ClearManagedStreamActiveCluster(ctx, commodoredb.ClearManagedStreamActiveClusterParams{
		StreamID:          streamID,
		TenantID:          tenantID,
		ExpectedClusterID: sql.NullString{String: expected, Valid: true},
		ClaimToken:        sql.NullString{String: managedClaimToken(streamID), Valid: true},
	})
	if err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"stream_id":  streamID,
			"cluster_id": expected,
		}).Error("ClearStreamActiveCluster: update failed")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	return &commodorepb.ClearStreamActiveClusterResponse{Cleared: rows > 0}, nil
}

// maxActiveIngestPlacementSync bounds one sync's payload. A cluster with more
// live pushes than this syncs across several calls rather than issuing one
// unbounded UPDATE.
const maxActiveIngestPlacementSync = 500

// SyncActiveIngestPlacement re-asserts active_ingest_cluster_id for ordinary
// push ingest from the publisher liveness Foghorn holds, the way the
// managed-stream reconciler does for mist_native. Callers sync on a cadence
// inside the claim's lease window.
//
// renew applies the same contention rule PUSH_REWRITE applies: it refreshes a
// claim this cluster holds, and takes one only where the row holds none or
// holds one that has already lapsed. A fresh claim held by another cluster is
// never disturbed, so this cannot move a live publisher's placement; it can
// establish placement for one that has none.
//
// release is stricter: it clears only a claim this cluster still holds, so a
// close arriving after the publisher moved to a peer cannot unpin them.
func (s *CommodoreServer) SyncActiveIngestPlacement(ctx context.Context, req *commodorepb.SyncActiveIngestPlacementRequest) (*commodorepb.SyncActiveIngestPlacementResponse, error) {
	// SERVICE-TOKEN ONLY, as with every other placement mutation: the cluster,
	// tenant, and stream all come from the caller. The shared interceptor also
	// accepts JWTs, so without this a logged-in user could renew or release
	// any stream's ingest placement.
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "SyncActiveIngestPlacement requires service token auth")
	}
	clusterID := strings.TrimSpace(req.GetClusterId())
	if len(req.GetRenew())+len(req.GetRelease()) > maxActiveIngestPlacementSync {
		return nil, status.Errorf(codes.InvalidArgument, "at most %d streams per sync", maxActiveIngestPlacementSync)
	}

	// Renewal is owner-fenced exactly like the direct claim: naming a cluster
	// is not proof of owning what is in it. A live claim may only be refreshed
	// by its owner; anything else must wait for it to lapse. Only an absent or
	// expired claim is acquirable, and then the renewing owner is stamped on it.
	//
	// The cluster comes from each entry, so one call can carry every cluster a
	// Foghorn serves — a call per cluster cannot re-assert a large fleet inside
	// one lease window.
	renewed, renewRefused, err := s.applyActiveIngestPlacement(ctx, clusterID, req.GetRenew(), true)
	if err != nil {
		return nil, err
	}

	// Release matches the OWNER as well as the cluster. Without that, an
	// attempt that never took the claim — a different node in this cluster, or
	// a Foghorn admitting from its validation cache while Commodore was down —
	// would clear a live publisher's placement.
	released, releaseRefused, err := s.applyActiveIngestPlacement(ctx, clusterID, req.GetRelease(), false)
	if err != nil {
		return nil, err
	}

	return &commodorepb.SyncActiveIngestPlacementResponse{
		Renewed:        int32(renewed),
		Released:       int32(released),
		RenewRefused:   renewRefused,
		ReleaseRefused: releaseRefused,
	}, nil
}

// applyActiveIngestPlacement runs one placement statement over a batch. Every
// column is unnested as a tuple rather than passed as independent ANY() lists:
// with separate lists any tenant in the batch would match any internal name in
// it, and any entry would match any cluster.
//
// defaultClusterID fills in for entries that name no cluster, so a caller that
// only ever speaks for one still works unchanged.
func (s *CommodoreServer) applyActiveIngestPlacement(ctx context.Context, defaultClusterID string, streams []*commodorepb.ActiveIngestStream, renew bool) (int64, []*commodorepb.ActiveIngestStream, error) {
	tenantIDs := make([]string, 0, len(streams))
	internalNames := make([]string, 0, len(streams))
	claimTokens := make([]string, 0, len(streams))
	clusterIDs := make([]string, 0, len(streams))
	for _, stream := range streams {
		tenantID := strings.TrimSpace(stream.GetTenantId())
		internalName := strings.TrimSpace(stream.GetInternalName())
		claimToken := strings.TrimSpace(stream.GetClaimToken())
		clusterID := strings.TrimSpace(stream.GetClusterId())
		if clusterID == "" {
			clusterID = defaultClusterID
		}
		if tenantID == "" || internalName == "" {
			return 0, nil, status.Error(codes.InvalidArgument, "each stream requires tenant_id and internal_name")
		}
		// Both halves are owner-fenced, and an empty token matches no owner, so
		// a tokenless entry could only ever be a caller trying to act on a claim
		// it cannot name. Refused rather than silently matching nothing.
		if claimToken == "" {
			return 0, nil, status.Error(codes.InvalidArgument, "each stream requires claim_token")
		}
		if clusterID == "" {
			return 0, nil, status.Error(codes.InvalidArgument, "each stream requires cluster_id (or a request-level default)")
		}
		tenantIDs = append(tenantIDs, tenantID)
		internalNames = append(internalNames, internalName)
		claimTokens = append(claimTokens, claimToken)
		clusterIDs = append(clusterIDs, clusterID)
	}
	if len(tenantIDs) == 0 {
		return 0, nil, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, status.Errorf(codes.Internal, "begin placement sync: %v", err)
	}
	defer s.rollbackTx(tx)
	rows, refusedRows, err := commodoredb.New(tx).ApplyActiveIngestPlacementBatch(ctx, commodoredb.ActiveIngestPlacementBatchParams{
		TenantIDs:     tenantIDs,
		InternalNames: internalNames,
		ClaimTokens:   claimTokens,
		ClusterIDs:    clusterIDs,
		LeaseSeconds:  int64(activeIngestLease.Seconds()),
		Renew:         renew,
	})
	if err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"default_cluster_id": defaultClusterID,
			"streams":            len(tenantIDs),
		}).Error("SyncActiveIngestPlacement: update failed")
		return 0, nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	refused := make([]*commodorepb.ActiveIngestStream, 0, len(refusedRows))
	for _, row := range refusedRows {
		refused = append(refused, &commodorepb.ActiveIngestStream{
			TenantId:     row.TenantID,
			InternalName: row.InternalName,
			ClaimToken:   row.ClaimToken,
			ClusterId:    row.ClusterID,
		})
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, status.Errorf(codes.Internal, "commit placement sync: %v", err)
	}
	return rows, refused, nil
}

// ResolvePlaybackID resolves a playback ID to internal name for MistServer PLAY_REWRITE trigger
func (s *CommodoreServer) ResolvePlaybackID(ctx context.Context, req *commodorepb.ResolvePlaybackIDRequest) (*commodorepb.ResolvePlaybackIDResponse, error) {
	playbackID := req.GetPlaybackId()
	if playbackID == "" {
		return nil, status.Error(codes.InvalidArgument, "playback_id required")
	}

	// playback_id is globally UNIQUE (commodore.sql), so no tenant_id filter needed
	var stream commodoredb.ResolveStreamByPlaybackIDRow
	err := s.retryPostgres(ctx, func() error {
		var queryErr error
		stream, queryErr = commodoredb.New(s.db).ResolveStreamByPlaybackID(ctx, playbackID)
		return queryErr
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

	resp := &commodorepb.ResolvePlaybackIDResponse{
		InternalName: stream.InternalName,
		TenantId:     stream.TenantID,
		PlaybackId:   playbackID,
		StreamId:     stream.ID,
		RequiresAuth: stream.RequiresAuth,
		IngestMode:   stream.IngestMode,
	}

	if route, err := s.resolveClusterRouteForTenant(ctx, stream.TenantID); err == nil {
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
	if stream.ActiveIngestClusterID.Valid && stream.ActiveIngestClusterID.String != "" {
		active := stream.ActiveIngestClusterID.String
		resp.OriginClusterId = &active
	}

	return resp, nil
}

// ResolveInternalName resolves an internal_name to tenant context for event enrichment
func (s *CommodoreServer) ResolveInternalName(ctx context.Context, req *commodorepb.ResolveInternalNameRequest) (*commodorepb.ResolveInternalNameResponse, error) {
	internalName := req.GetInternalName()
	if internalName == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name required")
	}

	// internal_name is globally UNIQUE (commodore.sql), so no tenant_id filter needed
	stream, err := commodoredb.New(s.db).ResolveStreamByInternalName(ctx, internalName)

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

	resp := &commodorepb.ResolveInternalNameResponse{
		InternalName:       internalName,
		TenantId:           stream.TenantID,
		UserId:             stream.UserID,
		IsRecordingEnabled: stream.IsRecordingEnabled.Bool,
		StreamId:           stream.ID,
		RequiresAuth:       stream.RequiresAuth,
	}
	if route, err := s.resolveClusterRouteForTenant(ctx, stream.TenantID); err == nil {
		resp.ClusterPeers = route.clusterPeers
		resp.OriginClusterId = route.clusterID
	}
	// Managed (mist_native) streams may be placed in a cluster other than the
	// tenant's default route; active_ingest_cluster_id is the verified-applied
	// source cluster recorded by Foghorn. When set, it is the authoritative
	// origin — federation/thumbnail/storage attribution must follow the
	// active source, not the tenant default. Peers stay tenant-routed.
	if stream.ActiveIngestClusterID.Valid && stream.ActiveIngestClusterID.String != "" {
		resp.OriginClusterId = stream.ActiveIngestClusterID.String
	}
	return resp, nil
}

// ResolvePullSourceByInternalName returns the configured upstream pull URI for a
// pull-mode stream, decrypted. Foghorn calls this from STREAM_SOURCE handling
// and /source origin selection for pull+<internal_name> streams.
func (s *CommodoreServer) ResolvePullSourceByInternalName(ctx context.Context, req *commodorepb.ResolvePullSourceByInternalNameRequest) (*commodorepb.ResolvePullSourceByInternalNameResponse, error) {
	internalName := req.GetInternalName()
	if internalName == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name required")
	}

	var source commodoredb.ResolvePullSourceByInternalNameRow
	err := s.retryPostgres(ctx, func() error {
		var queryErr error
		source, queryErr = commodoredb.New(s.db).ResolvePullSourceByInternalName(ctx, internalName)
		return queryErr
	})

	if errors.Is(err, sql.ErrNoRows) {
		return &commodorepb.ResolvePullSourceByInternalNameResponse{Found: false}, nil
	}
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"error":         err,
		}).Error("Database error resolving pull source")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	if source.IngestMode != "pull" {
		// Stream exists but isn't a pull stream — refuse to leak any URI.
		return &commodorepb.ResolvePullSourceByInternalNameResponse{Found: false}, nil
	}

	sourceURI, err := s.pullSourceEncryptor.Decrypt(source.SourceUriEnc)
	if err != nil {
		s.logger.WithError(err).WithField("internal_name", internalName).Warn("Failed to decrypt pull source_uri")
		return nil, status.Error(codes.Internal, "failed to decrypt pull source")
	}

	return &commodorepb.ResolvePullSourceByInternalNameResponse{
		Found:             true,
		SourceUri:         sourceURI,
		Enabled:           source.Enabled,
		TenantId:          source.TenantID,
		StreamId:          source.ID,
		AllowedClusterIds: source.AllowedClusterIds,
	}, nil
}

func normalizeIngestMode(mode string) string {
	return strings.ToLower(strings.TrimSpace(mode))
}

func buildPullSourceView(rawURI string, enabled bool, class pullsource.Class, allowedClusterIDs []string) *commodorepb.PullSourceView {
	if rawURI == "" {
		return nil
	}
	return &commodorepb.PullSourceView{
		SourceUriRedacted: pullsource.Redact(rawURI),
		Enabled:           enabled,
		Class:             class.String(),
		AllowedClusterIds: allowedClusterIDs,
	}
}

func pullSourceEnabled(input *commodorepb.PullSourceInput) bool {
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
		resp, err := s.quartermasterClient.ListClusters(ctx, &commonpb.CursorPaginationRequest{
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
	state, err := commodoredb.New(s.db).GetOwnedPullSourceState(ctx, commodoredb.GetOwnedPullSourceStateParams{
		ID:       streamID,
		UserID:   userID,
		TenantID: tenantID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil, status.Error(codes.NotFound, "pull source not found")
	}
	if err != nil {
		return "", false, nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	plain, err := s.pullSourceEncryptor.Decrypt(state.SourceUriEnc)
	if err != nil {
		return "", false, nil, status.Errorf(codes.Internal, "failed to decrypt pull source: %v", err)
	}
	return plain, state.Enabled, state.AllowedClusterIds, nil
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
func (s *CommodoreServer) ValidateAPIToken(ctx context.Context, req *commodorepb.ValidateAPITokenRequest) (*commodorepb.ValidateAPITokenResponse, error) {
	token := req.GetToken()
	if token == "" {
		return &commodorepb.ValidateAPITokenResponse{Valid: false}, nil
	}

	queries := commodoredb.New(s.db)
	tokenRow, err := queries.ValidateAPITokenHash(ctx, hashToken(token))

	if errors.Is(err, sql.ErrNoRows) {
		return &commodorepb.ValidateAPITokenResponse{Valid: false}, nil
	}

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Database error validating API token")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Update last used timestamp (best effort)
	if updateErr := queries.TouchAPITokenLastUsed(ctx, tokenRow.ID); updateErr != nil {
		s.logger.WithError(updateErr).Debug("Failed to update API token last_used_at")
	}

	// Look up user email, role, and platform-operator grant for context.
	// Filter by the token's tenant too (defense in depth: the user must belong
	// to the tenant the token is scoped to).
	userRow, err := queries.GetAPITokenUserContext(ctx, commodoredb.GetAPITokenUserContextParams{
		UserID: tokenRow.UserID, TenantID: tokenRow.TenantID,
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithFields(logging.Fields{
			"user_id": tokenRow.UserID,
			"error":   err,
		}).Warn("Failed to fetch user details for API token")
	}

	return &commodorepb.ValidateAPITokenResponse{
		Valid:            true,
		UserId:           tokenRow.UserID,
		TenantId:         tokenRow.TenantID,
		Email:            userRow.Email,
		Role:             userRow.Role,
		Permissions:      tokenRow.Permissions,
		TokenId:          tokenRow.ID,
		PlatformOperator: userRow.PlatformOperator,
	}, nil
}

// Quartermaster lookup functions are package variables so tests can exercise
// the ownership policy without a real Quartermaster gRPC server.
var (
	mistAdminGetNodeOwner = func(s *CommodoreServer, ctx context.Context, nodeID string) (*quartermasterpb.NodeOwnerResponse, error) {
		if s.quartermasterClient == nil {
			return nil, fmt.Errorf("quartermaster client unavailable")
		}
		return s.quartermasterClient.GetNodeOwner(ctx, nodeID)
	}
	mistAdminGetCluster = func(s *CommodoreServer, ctx context.Context, clusterID string) (*quartermasterpb.ClusterResponse, error) {
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
//     owner/admin in the cluster's owner tenant; holders of the
//     platform_operator grant are allowed as break-glass.
func (s *CommodoreServer) MintMistAdminSession(ctx context.Context, req *commodorepb.MintMistAdminSessionRequest) (*commodorepb.MintMistAdminSessionResponse, error) {
	nodeID := strings.TrimSpace(req.GetNodeId())
	if nodeID == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id is required")
	}

	trustedUserID, trustedTenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	trustedRole := strings.TrimSpace(ctxkeys.GetRole(ctx))
	// The platform-operator grant rides the JWT that this service's gRPC
	// interceptor validated itself (same trust basis as role/tenant), so the
	// break-glass arm reads it from the verified claims, not a forwarded bool.
	trustedPlatformOperator := ctxkeys.IsPlatformOperator(ctx)

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

	if !auth.CanAdminMistNode(ctx, ownerTenantID, trustedTenantID, trustedRole, trustedPlatformOperator) {
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
	return &commodorepb.MintMistAdminSessionResponse{
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
func (s *CommodoreServer) ValidateMistAdminSession(ctx context.Context, req *commodorepb.ValidateMistAdminSessionRequest) (*commodorepb.ValidateMistAdminSessionResponse, error) {
	if req.GetToken() == "" || req.GetExpectedNodeId() == "" {
		return &commodorepb.ValidateMistAdminSessionResponse{Valid: false}, nil
	}
	secret := []byte(config.RequireEnv("JWT_SECRET"))
	claims, err := auth.ValidateMistAdminSessionJWT(req.GetToken(), secret, req.GetExpectedNodeId())
	if err != nil {
		s.logger.WithError(err).WithField("expected_node_id", req.GetExpectedNodeId()).
			Debug("mist admin session validation rejected")
		return &commodorepb.ValidateMistAdminSessionResponse{Valid: false}, nil
	}
	return &commodorepb.ValidateMistAdminSessionResponse{
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
func (s *CommodoreServer) StartDVR(ctx context.Context, req *sharedpb.StartDVRRequest) (*sharedpb.StartDVRResponse, error) {
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
	var billingStatus *purserpb.GetTenantBillingStatusResponse
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
		// Resolve internal_name from stream_id (public -> internal). A soft-deleted (deletion-pending) stream is not
		// actionable — StartDVR must not record against a stream the deletion saga is tearing down.
		var rowErr error
		internalName, rowErr = commodoredb.New(s.db).GetLiveStreamInternalName(ctx, commodoredb.GetLiveStreamInternalNameParams{
			ID: streamID, TenantID: tenantID,
		})
		if rowErr != nil {
			if errors.Is(rowErr, sql.ErrNoRows) {
				return nil, status.Error(codes.NotFound, "stream not found")
			}
			return nil, status.Errorf(codes.Internal, "database error: %v", rowErr)
		}
	}

	// Verify stream exists in this tenant (tenant isolation) and resolve stream_id if needed.
	if streamID == "" {
		var rowErr error
		streamID, rowErr = commodoredb.New(s.db).GetLiveStreamIDByInternalName(ctx, commodoredb.GetLiveStreamIDByInternalNameParams{
			InternalName: internalName, TenantID: tenantID,
		})
		if rowErr != nil {
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
	foghornReq := &sharedpb.StartDVRRequest{
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
	if dvrDays, dvrErr := s.resolveInitialRetention(ctx, commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_DVR, tenantID, streamID); dvrErr == nil {
		if foghornReq.DvrPolicy == nil {
			foghornReq.DvrPolicy = &sharedpb.DVRPolicy{}
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
	chapterConfig, scanErr := commodoredb.New(s.db).GetStreamDVRChapterConfig(ctx, commodoredb.GetStreamDVRChapterConfigParams{
		StreamID: streamID, TenantID: tenantID,
	})
	if scanErr == nil {
		if chapterConfig.DvrChapterMode.Valid && chapterConfig.DvrChapterMode.String != "" {
			mode := chapterConfig.DvrChapterMode.String
			foghornReq.DvrChapterMode = &mode
		}
		if chapterConfig.DvrChapterIntervalSeconds.Valid && chapterConfig.DvrChapterIntervalSeconds.Int32 > 0 {
			iv := chapterConfig.DvrChapterIntervalSeconds.Int32
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
		// Ambiguous/failed start: the RegisterDVR-minted creation intent stays
		// pending. The sweep resolves it from Foghorn's command ledger — committed if
		// the DVR artifact was persisted, otherwise the catalog-only row is removed
		// and the intent aborted.
		s.logger.WithError(err).WithFields(logging.Fields{
			"tenant_id":     tenantID,
			"internal_name": internalName,
		}).Error("Failed to start DVR via Foghorn")
		return nil, grpcutil.PropagateError(ctx, err, trailers)
	}
	// A successful response means Foghorn durably inserted its DVR artifact, so
	// the creation intent has reached its live-catalog outcome (the business row
	// already exists). Terminalize it inline so the common path never waits on the
	// sweep.
	if resp.GetDvrHash() != "" {
		if cErr := s.commitCreationIntent(ctx,
			creationIntentRow{tenantID: tenantID, kind: creationIntentKindDVR, artifactHash: resp.GetDvrHash()},
			"", nil); cErr != nil && !errors.Is(cErr, errIntentCASMiss) {
			s.logger.WithError(cErr).WithField("dvr_hash", resp.GetDvrHash()).Warn("Failed to mark DVR creation intent committed")
		}
	}
	return resp, nil
}

// ============================================================================
// CLIP/DVR REGISTRY (Foghorn → Commodore)
// Business registry for clips and DVR recordings.
// See: docs/architecture/clips-dvr.md
// ============================================================================

// RegisterDVR creates a new DVR recording in the business registry
// Called by Foghorn during the StartDVR flow
func (s *CommodoreServer) RegisterDVR(ctx context.Context, req *commodorepb.RegisterDVRRequest) (*commodorepb.RegisterDVRResponse, error) {
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
	streamID, err := commodoredb.New(s.db).GetStreamIDForDVRRegistration(ctx, commodoredb.GetStreamIDForDVRRegistrationParams{
		InternalName: internalName, TenantID: tenantID,
	})
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
	var retentionUntilArg sql.NullTime
	if req.GetRetentionUntil() != nil {
		retentionUntilArg = sql.NullTime{Time: req.GetRetentionUntil().AsTime(), Valid: true}
	}
	storageClusterID := sql.NullString{String: req.GetStorageClusterId(), Valid: req.GetStorageClusterId() != ""}
	// Register the business row and its durable creation intent ATOMICALLY. Foghorn
	// inserts its own artifact row AFTER this call returns, so the intent is what
	// lets the convergence sweep resolve a DVR whose media-plane start then fails:
	// no Foghorn artifact ever appears → the sweep removes this catalog-only row, a
	// clean absence, instead of the old "retention_until=now" that left a dangling,
	// artifact-less DVR. request_id ties the intent to this registration.
	intentRequestID := uuid.New().String()
	dvrErr := fwdb.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		// FENCE the parent against a concurrent deletion IN this tx — a DVR must not register behind a stream that
		// is being torn down.
		if fErr := fenceParentStreamLive(ctx, tx, tenantID, streamID); fErr != nil {
			return fErr
		}
		if execErr := commodoredb.New(tx).InsertDVRRegistration(ctx, commodoredb.InsertDVRRegistrationParams{
			ID:                 dvrID,
			TenantID:           tenantID,
			UserID:             userID,
			StreamID:           streamID,
			DvrHash:            dvrHash,
			InternalName:       artifactInternalName,
			PlaybackID:         playbackID,
			StreamInternalName: internalName,
			OriginClusterID:    sql.NullString{String: req.GetOriginClusterId(), Valid: req.GetOriginClusterId() != ""},
			StorageClusterID:   storageClusterID,
			RetentionUntil:     retentionUntilArg,
		}); execErr != nil {
			return execErr
		}
		// Use the PERSISTED request_id (a fresh dvr_hash makes this the freshly minted
		// one, but the caller must never key Foghorn on a value the intent did not store).
		persisted, upErr := upsertCreationIntent(ctx, tx, tenantID, creationIntentKindDVR, dvrHash, intentRequestID, req.GetOriginClusterId(), nil)
		if upErr != nil {
			return upErr
		}
		intentRequestID = persisted
		return nil
	})
	if dvrErr != nil {
		if errors.Is(dvrErr, errParentStreamDeleted) {
			return nil, status.Error(codes.FailedPrecondition, "stream is being deleted")
		}
		s.logger.WithFields(logging.Fields{
			"tenant_id":     tenantID,
			"internal_name": internalName,
			"error":         dvrErr,
		}).Error("Failed to register DVR in business registry")
		return nil, status.Errorf(codes.Internal, "failed to register DVR: %v", dvrErr)
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
	s.emitArtifactEvent(ctx, eventArtifactRegistered, tenantID, userID, ipcpb.ArtifactEvent_ARTIFACT_TYPE_DVR, dvrHash, streamID, "registered", expiresAt)

	return &commodorepb.RegisterDVRResponse{
		DvrHash:      dvrHash,
		DvrId:        dvrID,
		PlaybackId:   playbackID,
		InternalName: artifactInternalName,
		StreamId:     streamID,
		RequestId:    intentRequestID,
	}, nil
}

// UpdateDVRRetention back-fills commodore.dvr_recordings.retention_until from
// Foghorn at finalize time. Foghorn computes the value from the persisted
// dvr_retention_days snapshot (ended_at + days*24h), so the business
// registry's expires_at reflects post-end retention rather than a synthetic
// start-time projection. Active recordings carry NULL until they finalize.
func (s *CommodoreServer) UpdateDVRRetention(ctx context.Context, req *commodorepb.UpdateDVRRetentionRequest) (*commodorepb.UpdateDVRRetentionResponse, error) {
	dvrHash := req.GetDvrHash()
	if dvrHash == "" {
		return nil, status.Error(codes.InvalidArgument, "dvr_hash is required")
	}
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	var retentionArg sql.NullTime
	if req.GetRetentionUntil() != nil {
		retentionArg = sql.NullTime{Time: req.GetRetentionUntil().AsTime(), Valid: true}
	}
	affected, err := commodoredb.New(s.db).UpdateDVRRetention(ctx, commodoredb.UpdateDVRRetentionParams{
		RetentionUntil: retentionArg, DvrHash: dvrHash, TenantID: tenantID,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "update retention failed: %v", err)
	}
	return &commodorepb.UpdateDVRRetentionResponse{Updated: affected > 0}, nil
}

// ResolveClipHash resolves a clip hash to tenant context
// Used for analytics enrichment and playback authorization
func (s *CommodoreServer) ResolveClipHash(ctx context.Context, req *commodorepb.ResolveClipHashRequest) (*commodorepb.ResolveClipHashResponse, error) {
	clipHash := req.GetClipHash()
	if clipHash == "" {
		return nil, status.Error(codes.InvalidArgument, "clip_hash is required")
	}

	clip, err := commodoredb.New(s.db).ResolveClipByHash(ctx, clipHash)

	if errors.Is(err, sql.ErrNoRows) {
		return &commodorepb.ResolveClipHashResponse{
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

	return &commodorepb.ResolveClipHashResponse{
		Found:              true,
		TenantId:           clip.TenantID,
		UserId:             clip.UserID,
		StreamId:           clip.StreamID,
		StreamInternalName: clip.StreamInternalName.String,
		Title:              clip.Title.String,
		Description:        clip.Description.String,
		StartTime:          clip.StartTime,
		Duration:           clip.Duration,
		ClipMode:           clip.ClipMode.String,
		PlaybackId:         clip.PlaybackID,
		InternalName:       clip.InternalName,
		OriginClusterId:    clip.OriginClusterID.String,
	}, nil
}

// ResolveDVRHash resolves a DVR hash to tenant context
// Used for analytics enrichment and playback authorization
func (s *CommodoreServer) ResolveDVRHash(ctx context.Context, req *commodorepb.ResolveDVRHashRequest) (*commodorepb.ResolveDVRHashResponse, error) {
	dvrHash := req.GetDvrHash()
	if dvrHash == "" {
		return nil, status.Error(codes.InvalidArgument, "dvr_hash is required")
	}

	var dvr commodoredb.ResolveDVRByHashRow
	err := s.retryPostgres(ctx, func() error {
		var queryErr error
		dvr, queryErr = commodoredb.New(s.db).ResolveDVRByHash(ctx, dvrHash)
		return queryErr
	})

	if errors.Is(err, sql.ErrNoRows) {
		return &commodorepb.ResolveDVRHashResponse{
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

	return &commodorepb.ResolveDVRHashResponse{
		Found:              true,
		TenantId:           dvr.TenantID,
		UserId:             dvr.UserID,
		StreamId:           dvr.StreamID.String,
		StreamInternalName: dvr.StreamInternalName,
		PlaybackId:         dvr.PlaybackID,
		InternalName:       dvr.InternalName,
		OriginClusterId:    dvr.OriginClusterID.String,
	}, nil
}

// ResolveVodHash resolves a VOD hash to tenant context
// Used for analytics enrichment, playback authorization, and lifecycle operations
func (s *CommodoreServer) ResolveVodHash(ctx context.Context, req *commodorepb.ResolveVodHashRequest) (*commodorepb.ResolveVodHashResponse, error) {
	vodHash := req.GetVodHash()
	if vodHash == "" {
		return nil, status.Error(codes.InvalidArgument, "vod_hash is required")
	}

	var vod commodoredb.ResolveVODByHashRow
	err := s.retryPostgres(ctx, func() error {
		var queryErr error
		vod, queryErr = commodoredb.New(s.db).ResolveVODByHash(ctx, vodHash)
		return queryErr
	})

	if errors.Is(err, sql.ErrNoRows) {
		return &commodorepb.ResolveVodHashResponse{
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

	resp := &commodorepb.ResolveVodHashResponse{
		Found:           true,
		TenantId:        vod.TenantID,
		UserId:          vod.UserID,
		Filename:        vod.Filename,
		Title:           vod.Title.String,
		Description:     vod.Description.String,
		PlaybackId:      vod.PlaybackID,
		InternalName:    vod.InternalName,
		OriginClusterId: vod.OriginClusterID.String,
	}
	// Carry the tenant's cluster peers so a cross-cluster relay resolve can
	// enforce the federation allowlist on the origin (and any storage redirect).
	s.populateArtifactClusterContext(ctx, vod.TenantID, &resp.ClusterPeers)
	return resp, nil
}

// ResolveVodID resolves a VOD relay ID (commodore.vod_assets.id) to vod_hash + tenant context
func (s *CommodoreServer) ResolveVodID(ctx context.Context, req *commodorepb.ResolveVodIDRequest) (*commodorepb.ResolveVodIDResponse, error) {
	vodID := req.GetVodId()
	if vodID == "" {
		return nil, status.Error(codes.InvalidArgument, "vod_id is required")
	}

	var vod commodoredb.ResolveVODByIDRow
	err := s.retryPostgres(ctx, func() error {
		var queryErr error
		vod, queryErr = commodoredb.New(s.db).ResolveVODByID(ctx, vodID)
		return queryErr
	})

	if errors.Is(err, sql.ErrNoRows) {
		return &commodorepb.ResolveVodIDResponse{
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

	return &commodorepb.ResolveVodIDResponse{
		Found:        true,
		TenantId:     vod.TenantID,
		UserId:       vod.UserID,
		VodHash:      vod.VodHash,
		PlaybackId:   vod.PlaybackID,
		InternalName: vod.InternalName,
	}, nil
}

// MintChapterPlaybackID mints (or returns the existing) public playback_id
// for a hidden chapter artifact. Called by Foghorn at chapter finalization
// dispatch. Idempotent on chapter_id — repeat calls return the same
// playback_id even across finalization retries; artifact_hash is upserted
// because retries may reuse the same hash via the deterministic
// chapterPlaybackArtifactHash() derivation, but tenants change rarely.
func (s *CommodoreServer) MintChapterPlaybackID(ctx context.Context, req *commodorepb.MintChapterPlaybackIDRequest) (*commodorepb.MintChapterPlaybackIDResponse, error) {
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

	// The marker check and both upserts run in ONE transaction under a per-artifact advisory lock that
	// the catalog delete projection also takes, so a concurrent delete cannot interleave between the
	// check and the writes — closing the absent-row TOCTOU that a FOR UPDATE on the maybe-absent business
	// row could not (an absent row locks nothing). A present tombstone marker means the chapter's
	// vod_hash was deleted (chapters are content-addressed — a retry reuses the same vod_hash), so refuse
	// with FailedPrecondition and write nothing rather than resurrect a deleted asset. No marker → proceed.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "mint chapter begin: %v", err)
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback() //nolint:errcheck // best-effort rollback of an uncommitted tx
		}
	}()

	chapterQueries := commodoredb.New(tx)
	if lErr := chapterQueries.LockArtifactCatalogKey(ctx, tenantID+":vod:"+artifactHash); lErr != nil {
		return nil, status.Errorf(codes.Internal, "mint chapter lock: %v", lErr)
	}
	_, tErr := chapterQueries.GetVODTombstoneForUpdate(ctx, commodoredb.GetVODTombstoneForUpdateParams{
		TenantID: tenantID, ArtifactHash: artifactHash,
	})
	switch {
	case tErr == nil:
		return nil, status.Error(codes.FailedPrecondition, "chapter artifact was deleted; not resurrecting catalog row")
	case errors.Is(tErr, sql.ErrNoRows):
		// No tombstone marker → the chapter is live or fresh; proceed to register it.
	default:
		return nil, status.Errorf(codes.Internal, "chapter tombstone check failed: %v", tErr)
	}

	stored, err := chapterQueries.UpsertChapterPlaybackID(ctx, commodoredb.UpsertChapterPlaybackIDParams{
		ChapterID: chapterID, TenantID: tenantID, PlaybackID: playbackID,
		ArtifactHash: artifactHash, DvrHash: req.GetDvrHash(),
	})
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"chapter_id":    chapterID,
			"tenant_id":     tenantID,
			"artifact_hash": artifactHash,
			"error":         err,
		}).Error("Failed to mint chapter playback id")
		return nil, status.Errorf(codes.Internal, "mint chapter playback id: %v", err)
	}

	err = chapterQueries.UpsertChapterVODAsset(ctx, commodoredb.UpsertChapterVODAssetParams{
		ID:               uuid.New().String(),
		TenantID:         tenantID,
		UserID:           userID,
		StreamID:         req.GetStreamId(),
		VodHash:          artifactHash,
		InternalName:     artifactHash,
		PlaybackID:       stored,
		Title:            sql.NullString{String: title, Valid: true},
		Description:      description,
		Filename:         filename,
		ContentType:      sql.NullString{String: contentType, Valid: true},
		OriginClusterID:  req.GetOriginClusterId(),
		StorageClusterID: req.GetStorageClusterId(),
		OriginID:         sql.NullString{String: chapterID, Valid: true},
	})
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"chapter_id":    chapterID,
			"tenant_id":     tenantID,
			"artifact_hash": artifactHash,
			"error":         err,
		}).Error("Failed to register chapter VOD asset")
		return nil, status.Errorf(codes.Internal, "register chapter VOD asset: %v", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "mint chapter commit: %v", err)
	}
	committed = true

	return &commodorepb.MintChapterPlaybackIDResponse{PlaybackId: stored}, nil
}

// GetTenantProcessesJSON exposes Commodore's resolved MistServer process config
// for a given tenant/stream/lifecycle. Foghorn-internal pipelines store and
// apply the returned snapshot without deriving local lifecycle subsets.
func (s *CommodoreServer) GetTenantProcessesJSON(ctx context.Context, req *commodorepb.GetTenantProcessesJSONRequest) (*commodorepb.GetTenantProcessesJSONResponse, error) {
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
	return &commodorepb.GetTenantProcessesJSONResponse{ProcessesJson: processesJSON}, nil
}

// ResolveChapterPlaybackID maps a public chapter playback_id back to its
// internal (chapter_id, tenant_id, artifact_hash). Foghorn's playback
// resolver calls this to bridge the public ID into the artifact-hash
// path that handles policy walk + artifact serving.
func (s *CommodoreServer) ResolveChapterPlaybackID(ctx context.Context, req *commodorepb.ResolveChapterPlaybackIDRequest) (*commodorepb.ResolveChapterPlaybackIDResponse, error) {
	playbackID := req.GetPlaybackId()
	if playbackID == "" {
		return nil, status.Error(codes.InvalidArgument, "playback_id is required")
	}

	var chapter commodoredb.ResolveChapterByPlaybackIDRow
	// Join the parent VOD row and require it live: a chapter of a tombstoned asset must not resolve
	// (its mapping is removed on deletion, but the join closes the window before that lands).
	err := s.retryPostgres(ctx, func() error {
		var queryErr error
		chapter, queryErr = commodoredb.New(s.db).ResolveChapterByPlaybackID(ctx, playbackID)
		return queryErr
	})
	if errors.Is(err, sql.ErrNoRows) {
		return &commodorepb.ResolveChapterPlaybackIDResponse{Found: false}, nil
	}
	if err != nil {
		s.logger.WithError(err).WithField("playback_id", playbackID).Error("Failed to resolve chapter playback id")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	return &commodorepb.ResolveChapterPlaybackIDResponse{
		Found:        true,
		ChapterId:    chapter.ChapterID,
		TenantId:     chapter.TenantID,
		ArtifactHash: chapter.ArtifactHash,
	}, nil
}

// ResolveArtifactPlaybackID resolves an artifact playback ID to artifact identity
func (s *CommodoreServer) ResolveArtifactPlaybackID(ctx context.Context, req *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
	playbackID := req.GetPlaybackId()
	if playbackID == "" {
		return nil, status.Error(codes.InvalidArgument, "playback_id is required")
	}

	// 1. Clips
	var clip commodoredb.ResolveClipByPlaybackIDRow
	err := s.retryPostgres(ctx, func() error {
		var queryErr error
		clip, queryErr = commodoredb.New(s.db).ResolveClipByPlaybackID(ctx, playbackID)
		return queryErr
	})
	if err == nil {
		resp := &commodorepb.ResolveArtifactPlaybackIDResponse{
			Found:           true,
			ArtifactHash:    clip.ClipHash,
			InternalName:    clip.InternalName,
			TenantId:        clip.TenantID,
			UserId:          clip.UserID,
			StreamId:        clip.StreamID,
			ContentType:     "clip",
			OriginClusterId: clip.OriginClusterID.String,
			RequiresAuth:    clip.RequiresAuth,
		}
		s.populateArtifactClusterContext(ctx, clip.TenantID, &resp.ClusterPeers)
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
	var dvr commodoredb.ResolveDVRByPlaybackIDRow
	err = s.retryPostgres(ctx, func() error {
		var queryErr error
		dvr, queryErr = commodoredb.New(s.db).ResolveDVRByPlaybackID(ctx, playbackID)
		return queryErr
	})
	if err == nil {
		// Missing source stream → treat as protected so a deleted-stream race
		// does not silently expose what was once gated content.
		resp := &commodorepb.ResolveArtifactPlaybackIDResponse{
			Found:           true,
			ArtifactHash:    dvr.DvrHash,
			InternalName:    dvr.InternalName,
			TenantId:        dvr.TenantID,
			UserId:          dvr.UserID,
			StreamId:        dvr.DStreamID,
			ContentType:     "dvr",
			OriginClusterId: dvr.OriginClusterID.String,
			RequiresAuth:    !dvr.RequiresAuth.Valid || dvr.RequiresAuth.Bool,
		}
		s.populateArtifactClusterContext(ctx, dvr.TenantID, &resp.ClusterPeers)
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
	var vod commodoredb.ResolveVODByPlaybackIDRow
	err = s.retryPostgres(ctx, func() error {
		var queryErr error
		vod, queryErr = commodoredb.New(s.db).ResolveVODByPlaybackID(ctx, playbackID)
		return queryErr
	})
	if err == nil {
		resp := &commodorepb.ResolveArtifactPlaybackIDResponse{
			Found:           true,
			ArtifactHash:    vod.VodHash,
			InternalName:    vod.InternalName,
			TenantId:        vod.TenantID,
			UserId:          vod.UserID,
			ContentType:     "vod",
			OriginClusterId: vod.OriginClusterID.String,
			RequiresAuth:    vod.RequiresAuth,
		}
		s.populateArtifactClusterContext(ctx, vod.TenantID, &resp.ClusterPeers)
		return resp, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithFields(logging.Fields{
			"playback_id": playbackID,
			"error":       err,
		}).Error("Database error resolving VOD playback_id")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	return &commodorepb.ResolveArtifactPlaybackIDResponse{Found: false}, nil
}

func (s *CommodoreServer) populateArtifactClusterContext(ctx context.Context, tenantID string, peers *[]*clusterpeerpb.TenantClusterPeer) {
	if tenantID == "" || peers == nil {
		return
	}
	if route, err := s.resolveClusterRouteForTenant(ctx, tenantID); err == nil {
		*peers = route.clusterPeers
	}
}

// ResolveArtifactInternalName resolves an artifact internal routing name to artifact identity
func (s *CommodoreServer) ResolveArtifactInternalName(ctx context.Context, req *commodorepb.ResolveArtifactInternalNameRequest) (*commodorepb.ResolveArtifactInternalNameResponse, error) {
	internalName := req.GetInternalName()
	if internalName == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name is required")
	}

	// 1. Clips
	var clip commodoredb.ResolveClipByInternalNameRow
	err := s.retryPostgres(ctx, func() error {
		var queryErr error
		clip, queryErr = commodoredb.New(s.db).ResolveClipByInternalName(ctx, internalName)
		return queryErr
	})
	if err == nil {
		resp := &commodorepb.ResolveArtifactInternalNameResponse{
			Found:           true,
			ArtifactHash:    clip.ClipHash,
			InternalName:    clip.InternalName,
			TenantId:        clip.TenantID,
			UserId:          clip.UserID,
			StreamId:        clip.StreamID,
			ContentType:     "clip",
			OriginClusterId: clip.OriginClusterID.String,
			RequiresAuth:    clip.RequiresAuth,
		}
		s.populateArtifactClusterContext(ctx, clip.TenantID, &resp.ClusterPeers)
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
	var dvr commodoredb.ResolveDVRByInternalNameRow
	err = s.retryPostgres(ctx, func() error {
		var queryErr error
		dvr, queryErr = commodoredb.New(s.db).ResolveDVRByInternalName(ctx, internalName)
		return queryErr
	})
	if err == nil {
		resp := &commodorepb.ResolveArtifactInternalNameResponse{
			Found:           true,
			ArtifactHash:    dvr.DvrHash,
			InternalName:    dvr.InternalName,
			TenantId:        dvr.TenantID,
			UserId:          dvr.UserID,
			StreamId:        dvr.DStreamID,
			ContentType:     "dvr",
			OriginClusterId: dvr.OriginClusterID.String,
			RequiresAuth:    !dvr.RequiresAuth.Valid || dvr.RequiresAuth.Bool,
		}
		s.populateArtifactClusterContext(ctx, dvr.TenantID, &resp.ClusterPeers)
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
	var vod commodoredb.ResolveVODByInternalNameRow
	err = s.retryPostgres(ctx, func() error {
		var queryErr error
		vod, queryErr = commodoredb.New(s.db).ResolveVODByInternalName(ctx, internalName)
		return queryErr
	})
	if err == nil {
		resp := &commodorepb.ResolveArtifactInternalNameResponse{
			Found:           true,
			ArtifactHash:    vod.VodHash,
			InternalName:    vod.InternalName,
			TenantId:        vod.TenantID,
			UserId:          vod.UserID,
			ContentType:     "vod",
			OriginClusterId: vod.OriginClusterID.String,
			RequiresAuth:    vod.RequiresAuth,
		}
		s.populateArtifactClusterContext(ctx, vod.TenantID, &resp.ClusterPeers)
		return resp, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"error":         err,
		}).Error("Database error resolving VOD internal_name")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	return &commodorepb.ResolveArtifactInternalNameResponse{Found: false}, nil
}

// ResolveIdentifier provides unified resolution across all Commodore registries.
// Enriches found responses with cluster context from Quartermaster.
func (s *CommodoreServer) ResolveIdentifier(ctx context.Context, req *commodorepb.ResolveIdentifierRequest) (*commodorepb.ResolveIdentifierResponse, error) {
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
func (s *CommodoreServer) resolveIdentifierLookup(ctx context.Context, req *commodorepb.ResolveIdentifierRequest) (*commodorepb.ResolveIdentifierResponse, error) {
	identifier := req.GetIdentifier()
	if identifier == "" {
		return nil, status.Error(codes.InvalidArgument, "identifier is required")
	}

	_, uuidErr := uuid.Parse(identifier)
	var resolved commodoredb.ResolveIdentifierCatalogRow
	err := s.retryPostgres(ctx, func() error {
		var queryErr error
		resolved, queryErr = commodoredb.New(s.db).ResolveIdentifierCatalog(ctx, commodoredb.ResolveIdentifierCatalogParams{
			IncludeIds: uuidErr == nil,
			Identifier: identifier,
		})
		return queryErr
	})
	if errors.Is(err, sql.ErrNoRows) {
		return &commodorepb.ResolveIdentifierResponse{Found: false}, nil
	}
	if err != nil {
		s.logger.WithError(err).Error("Database error resolving identifier catalog")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	return &commodorepb.ResolveIdentifierResponse{
		Found:              true,
		TenantId:           resolved.TenantID,
		UserId:             resolved.UserID,
		InternalName:       resolved.InternalName,
		IdentifierType:     resolved.IdentifierType,
		IsRecordingEnabled: resolved.IsRecordingEnabled,
		StreamId:           resolved.StreamID,
		RequiresAuth:       resolved.RequiresAuth,
	}, nil
}

// ============================================================================
// WALLET IDENTITY
// ============================================================================

// GetOrCreateWalletUser looks up or creates a tenant/user for a verified wallet address.
// This is called after a wallet-auth challenge signature has been verified.
// If the wallet is not known, creates a new tenant (prepaid) + user (email=NULL) + wallet_identity.
func (s *CommodoreServer) GetOrCreateWalletUser(ctx context.Context, req *commodorepb.GetOrCreateWalletUserRequest) (*commodorepb.GetOrCreateWalletUserResponse, error) {
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
	queries := commodoredb.New(s.db)

	// Try to find existing wallet identity (query only commodore.* tables)
	var tenantID, userID string
	walletIdentity, err := queries.GetWalletIdentityByAddress(ctx, commodoredb.GetWalletIdentityByAddressParams{
		ChainType:     chainType,
		WalletAddress: normalizedAddress,
	})

	if err == nil {
		tenantID = walletIdentity.TenantID
		userID = walletIdentity.UserID
		// Existing wallet found - update last_auth_at
		if touchErr := queries.TouchWalletIdentityAuth(ctx, commodoredb.TouchWalletIdentityAuthParams{
			ChainType:     chainType,
			WalletAddress: normalizedAddress,
		}); touchErr != nil {
			s.logger.WithError(touchErr).Debug("Failed to update wallet last_auth_at")
		}

		// Get billing info via Purser gRPC (not DB JOIN)
		billingModel := "prepaid"
		if s.purserClient != nil {
			billingStatus, billingErr := s.purserClient.GetTenantAdmissionStatus(ctx, tenantID)
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

		return &commodorepb.GetOrCreateWalletUserResponse{
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
	provisioningKey := "wallet:" + chainType + ":" + normalizedAddress
	tenantResp, err := s.quartermasterClient.CreateTenant(ctx, &quartermasterpb.CreateTenantRequest{
		Name:            tenantName,
		Attribution:     req.GetAttribution(),
		ProvisioningKey: &provisioningKey,
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
	txQueries := commodoredb.New(tx)

	userID = uuid.NewString()
	shortAddr := normalizedAddress
	if len(shortAddr) >= 8 {
		shortAddr = shortAddr[2:8]
	}
	err = txQueries.InsertWalletUser(ctx, commodoredb.InsertWalletUserParams{
		ID:        userID,
		TenantID:  tenantID,
		FirstName: sql.NullString{String: "Wallet " + shortAddr, Valid: true},
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to create user")
		return nil, status.Error(codes.Internal, "failed to create user")
	}

	err = txQueries.InsertWalletIdentity(ctx, commodoredb.InsertWalletIdentityParams{
		WalletAddress: normalizedAddress,
		ChainType:     chainType,
		TenantID:      tenantID,
		UserID:        userID,
	})
	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" {
			// Another replica completed the same wallet signup after both callers
			// converged on Quartermaster's provisioning key. Roll back this local
			// user and return the winner's canonical identity.
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				return nil, status.Error(codes.Internal, "failed to resolve concurrent wallet signup")
			}
			var existingTenantID, existingUserID string
			winner, lookupErr := queries.GetWalletIdentityByAddress(ctx, commodoredb.GetWalletIdentityByAddressParams{
				ChainType:     chainType,
				WalletAddress: normalizedAddress,
			})
			if lookupErr == nil {
				existingTenantID = winner.TenantID
				existingUserID = winner.UserID
				return &commodorepb.GetOrCreateWalletUserResponse{
					TenantId: existingTenantID, UserId: existingUserID, IsNew: false,
					BillingModel: "prepaid", WalletAddress: normalizedAddress,
				}, nil
			}
		}
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

	return &commodorepb.GetOrCreateWalletUserResponse{
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

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// Login authenticates a user and returns a JWT token
func (s *CommodoreServer) Login(ctx context.Context, req *commodorepb.LoginRequest) (*commodorepb.AuthResponse, error) {
	email := normalizeEmail(req.GetEmail())
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
	queries := commodoredb.New(s.db)
	userRow, err := queries.GetLoginUserByEmail(ctx, sql.NullString{String: email, Valid: true})

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.Unauthenticated, "invalid credentials")
	}
	if err != nil {
		s.logger.WithError(err).Error("Database error during login")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	user, err := commodoreUserFromLoginRow(userRow)
	if err != nil {
		s.logger.WithError(err).Error("Invalid user record during login")
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

	// Update last login (best effort)
	if updateErr := queries.TouchUserLastLogin(ctx, commodoredb.TouchUserLastLoginParams{ID: user.ID, TenantID: user.TenantID}); updateErr != nil {
		s.logger.WithError(updateErr).Debug("Failed to update last_login_at")
	}

	// Generate JWT access token
	jwtSecret := []byte(config.RequireEnv("JWT_SECRET"))
	token, err := auth.GenerateSessionJWT(user.ID, user.TenantID, user.Email, user.Role, platformRoles(user.PlatformOperator), time.Now(), jwtSecret)
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

	err = queries.InsertRefreshToken(ctx, commodoredb.InsertRefreshTokenParams{
		TenantID:  user.TenantID,
		UserID:    user.ID,
		TokenHash: refreshHash,
		ExpiresAt: refreshExpiry,
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to store refresh token")
		return nil, status.Errorf(codes.Internal, "failed to create session: %v", err)
	}

	expiresAt := time.Now().Add(15 * time.Minute)
	s.emitAuthEvent(ctx, eventAuthLoginSucceeded, user.ID, user.TenantID, "password", "", "", "")

	return &commodorepb.AuthResponse{
		Token:        token,
		RefreshToken: refreshToken,
		User:         user.toProtoUser("", ""),
		ExpiresAt:    timestamppb.New(expiresAt),
	}, nil
}

// Register creates a new user account
func (s *CommodoreServer) Register(ctx context.Context, req *commodorepb.RegisterRequest) (*commodorepb.RegisterResponse, error) {
	email := normalizeEmail(req.GetEmail())
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
	queries := commodoredb.New(s.db)
	_, err := queries.FindUserIDByEmail(ctx, sql.NullString{String: email, Valid: true})
	if err == nil {
		return &commodorepb.RegisterResponse{
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
		provisioningKey := "email:" + email
		resp, createErr := s.quartermasterClient.CreateTenant(ctx, &quartermasterpb.CreateTenantRequest{
			Name:            email, // Use email as initial tenant name
			Attribution:     req.GetAttribution(),
			ProvisioningKey: &provisioningKey,
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
			return &commodorepb.RegisterResponse{
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
	userCount, err := queries.CountUsersForTenant(ctx, tenantID)
	role := "member"
	if err == nil && userCount == 0 {
		role = "owner"
	}

	// Create user
	userID := uuid.New().String()
	err = queries.InsertRegisteredUser(ctx, commodoredb.InsertRegisteredUserParams{
		ID:                userID,
		TenantID:          tenantID,
		Email:             sql.NullString{String: email, Valid: true},
		PasswordHash:      sql.NullString{String: hashedPassword, Valid: true},
		FirstName:         sql.NullString{String: req.GetFirstName(), Valid: true},
		LastName:          sql.NullString{String: req.GetLastName(), Valid: true},
		Role:              role,
		Permissions:       getDefaultPermissions(role),
		VerificationToken: sql.NullString{String: tokenHash, Valid: true},
		TokenExpiresAt:    sql.NullTime{Time: tokenExpiry, Valid: true},
	})

	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" {
			// Concurrent retries converge on Quartermaster's provisioning key and
			// the unique normalized email. The winning request owns the token/email.
			return &commodorepb.RegisterResponse{
				Success: true,
				Message: "Registration received. Check your email or request a new verification link.",
			}, nil
		}
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

	s.logger.WithFields(logging.Fields{
		"user_id":   userID,
		"tenant_id": tenantID,
		"email":     email,
		"role":      role,
	}).Info("User registered successfully via gRPC")

	s.emitAuthEvent(ctx, eventAuthRegistered, userID, tenantID, "password", "", "", "")

	return &commodorepb.RegisterResponse{
		Success: true,
		Message: "Registration successful. Please check your email to verify your account.",
	}, nil
}

// GetMe returns the current user's profile
func (s *CommodoreServer) GetMe(ctx context.Context, req *commodorepb.GetMeRequest) (*commodorepb.User, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	queries := commodoredb.New(s.db)
	userRow, err := queries.GetUserProfile(ctx, commodoredb.GetUserProfileParams{ID: userID, TenantID: tenantID})

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "user not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	user, err := commodoreUserFromProfileRow(userRow)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	result := user.toProtoUser("", "")

	// Fetch linked wallets
	walletRows, err := queries.ListUserWallets(ctx, userID)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to fetch user wallets")
		// Don't fail the whole request - just return user without wallets
	} else {
		for _, walletRow := range walletRows {
			if !walletRow.CreatedAt.Valid {
				continue
			}
			wallet := &commodorepb.WalletIdentity{
				Id:            walletRow.ID,
				WalletAddress: walletRow.WalletAddress,
				CreatedAt:     timestamppb.New(walletRow.CreatedAt.Time),
			}
			if walletRow.LastAuthAt.Valid {
				wallet.LastAuthAt = timestamppb.New(walletRow.LastAuthAt.Time)
			}
			result.Wallets = append(result.Wallets, wallet)
		}
	}

	return result, nil
}

// Logout invalidates user session (token blacklisting handled at Gateway)
func (s *CommodoreServer) Logout(ctx context.Context, req *commodorepb.LogoutRequest) (*commodorepb.LogoutResponse, error) {
	// Get user context to delete their refresh tokens
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		// Still acknowledge logout even without user context
		//nolint:nilerr // graceful logout even without user context
		return &commodorepb.LogoutResponse{
			Success: true,
			Message: "logged out successfully",
		}, nil
	}

	// Delete all refresh tokens for this user (logs them out of all devices)
	err = commodoredb.New(s.db).DeleteRefreshTokensForUser(ctx, commodoredb.DeleteRefreshTokensForUserParams{
		UserID: userID, TenantID: tenantID,
	})
	if err != nil {
		s.logger.WithError(err).Warn("Failed to delete refresh tokens during logout")
	}

	return &commodorepb.LogoutResponse{
		Success: true,
		Message: "logged out successfully",
	}, nil
}

// RefreshToken exchanges a refresh token for a new access token. Rotation is
// transactional (lock -> issue -> rotate -> commit) so a request can never
// leave a half-rotated session behind. Replay of a rotated token within the
// grace period is a legitimate concurrent-tab race and issues a fresh pair;
// replay after the grace period is theft UNLESS the recorded successor was
// never used, which means our rotation response never reached the client
// (e.g. laptop slept mid-refresh) and the session is recovered instead.
func (s *CommodoreServer) RefreshToken(ctx context.Context, req *commodorepb.RefreshTokenRequest) (*commodorepb.AuthResponse, error) {
	refreshToken := req.GetRefreshToken()
	if refreshToken == "" {
		return nil, status.Error(codes.InvalidArgument, "refresh token required")
	}

	tokenHash := hashToken(refreshToken)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to begin transaction: %v", err)
	}
	defer s.rollbackTx(tx)
	txQueries := commodoredb.New(tx)

	refreshRow, err := txQueries.LockRefreshTokenByHash(ctx, tokenHash)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.Unauthenticated, "invalid or expired refresh token")
	}
	if err != nil {
		s.logger.WithError(err).Error("Database error validating refresh token")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	tokenID := refreshRow.ID
	userID := refreshRow.UserID
	tenantID := refreshRow.TenantID
	revoked := refreshRow.Revoked
	rotatedAt := refreshRow.RotatedAt
	replacedBy := refreshRow.ReplacedBy

	rotateCurrent := !revoked
	var staleSuccessorID string

	if revoked {
		if rotatedAt.Valid && time.Since(rotatedAt.Time) <= refreshTokenReuseGracePeriod {
			// Concurrent refreshes (multiple tabs, timer + 401-retry) replay
			// the just-rotated token; issue a fresh pair instead of failing.
			s.logger.WithFields(logging.Fields{
				"user_id":    userID,
				"tenant_id":  tenantID,
				"rotated_at": rotatedAt.Time,
			}).Info("Refresh token replayed within rotation grace period; issuing fresh session")
		} else {
			successorUsed := true
			if replacedBy.Valid {
				successorRevoked, successorErr := txQueries.GetRefreshTokenSuccessorState(ctx, replacedBy.String)
				switch {
				case successorErr == nil:
					successorUsed = successorRevoked
				case errors.Is(successorErr, sql.ErrNoRows):
					// Successor gone (e.g. explicit logout); treat as used.
				default:
					s.logger.WithError(successorErr).Error("Database error checking refresh token successor")
					return nil, status.Errorf(codes.Internal, "database error: %v", successorErr)
				}
			}
			if successorUsed {
				// Old token and its replacement are both in play: two parties
				// share this session line. Revoke everything.
				s.logger.WithFields(logging.Fields{
					"user_id":   userID,
					"tenant_id": tenantID,
				}).Warn("Refresh token reuse detected, revoking all user sessions")
				if revokeErr := txQueries.RevokeRefreshTokensForUser(ctx, commodoredb.RevokeRefreshTokensForUserParams{
					UserID: userID, TenantID: tenantID,
				}); revokeErr != nil {
					s.logger.WithError(revokeErr).WithFields(logging.Fields{
						"user_id":   userID,
						"tenant_id": tenantID,
					}).Error("Failed to revoke sessions after refresh token reuse detection")
				} else if commitErr := tx.Commit(); commitErr != nil {
					s.logger.WithError(commitErr).Error("Failed to commit session revocation")
				}
				return nil, status.Error(codes.Unauthenticated, "session invalidated")
			}
			// The replacement we issued was never used: the rotation response
			// never reached the client. Recover the session instead of
			// treating the client as an attacker.
			staleSuccessorID = replacedBy.String
			s.logger.WithFields(logging.Fields{
				"user_id":   userID,
				"tenant_id": tenantID,
			}).Warn("Refresh token rotation response was lost; recovering session with fresh tokens")
		}
	}

	userRow, err := txQueries.GetRefreshUser(ctx, commodoredb.GetRefreshUserParams{ID: userID, TenantID: tenantID})

	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "user not found")
	}
	user, err := commodoreUserFromRefreshRow(userRow)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "user not found")
	}

	if !user.IsActive {
		return nil, status.Error(codes.Unauthenticated, "account deactivated")
	}

	// Generate new access token
	jwtSecret := []byte(config.RequireEnv("JWT_SECRET"))
	token, err := auth.GenerateSessionJWT(userID, tenantID, user.Email, user.Role, platformRoles(user.PlatformOperator), time.Now(), jwtSecret)
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

	newTokenID, err := txQueries.InsertRotatedRefreshToken(ctx, commodoredb.InsertRotatedRefreshTokenParams{
		TenantID: tenantID, UserID: userID, TokenHash: newRefreshHash, ExpiresAt: refreshExpiry,
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to store new refresh token")
		return nil, status.Errorf(codes.Internal, "failed to store refresh token: %v", err)
	}

	if rotateCurrent {
		// Revoke the old refresh token (don't delete - keep for reuse detection)
		if err := txQueries.RotateRefreshToken(ctx, commodoredb.RotateRefreshTokenParams{
			ID: tokenID, ReplacedBy: sql.NullString{String: newTokenID, Valid: true},
		}); err != nil {
			s.logger.WithError(err).Error("Failed to rotate refresh token")
			return nil, status.Errorf(codes.Internal, "failed to rotate refresh token: %v", err)
		}
	} else if staleSuccessorID != "" {
		// Retire the undelivered successor and re-point the presented token
		// at its actual replacement so a repeated lost response still
		// resolves as recovery, not theft.
		if err := txQueries.RevokeRefreshTokenByID(ctx, staleSuccessorID); err != nil {
			s.logger.WithError(err).Error("Failed to retire undelivered refresh token successor")
			return nil, status.Errorf(codes.Internal, "failed to rotate refresh token: %v", err)
		}
		if err := txQueries.RelinkRefreshToken(ctx, commodoredb.RelinkRefreshTokenParams{
			ID: tokenID, ReplacedBy: sql.NullString{String: newTokenID, Valid: true},
		}); err != nil {
			s.logger.WithError(err).Error("Failed to re-link recovered refresh token")
			return nil, status.Errorf(codes.Internal, "failed to rotate refresh token: %v", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to commit: %v", err)
	}

	expiresAt := time.Now().Add(15 * time.Minute)
	s.emitAuthEvent(ctx, eventAuthTokenRefreshed, userID, tenantID, "refresh_token", "", "", "")

	return &commodorepb.AuthResponse{
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
func (s *CommodoreServer) CompleteAuthorization(ctx context.Context, req *commodorepb.CompleteAuthorizationRequest) (*commodorepb.CompleteAuthorizationResponse, error) {
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

	err = commodoredb.New(s.db).InsertAuthorizationCode(ctx, commodoredb.InsertAuthorizationCodeParams{
		TenantID:            tenantID,
		UserID:              userID,
		ClientID:            req.GetClientId(),
		CodeHash:            codeHash,
		CodeChallenge:       req.GetCodeChallenge(),
		CodeChallengeMethod: req.GetCodeChallengeMethod(),
		RedirectUri:         req.GetRedirectUri(),
		Scope:               scope,
		State:               state,
		ExpiresAt:           expiresAt,
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to persist authorization code")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	return &commodorepb.CompleteAuthorizationResponse{
		Code:      code,
		ExpiresAt: timestamppb.New(expiresAt),
	}, nil
}

// ExchangeAuthorizationCode redeems a one-time PKCE authorization code for a
// session (access + refresh tokens). The code_verifier is hashed with SHA-256
// and constant-time compared against the stored code_challenge. The code is
// marked consumed in the same transaction that issues the refresh token, so
// a successful exchange is atomic and a second exchange returns AlreadyExists.
func (s *CommodoreServer) ExchangeAuthorizationCode(ctx context.Context, req *commodorepb.ExchangeAuthorizationCodeRequest) (*commodorepb.AuthResponse, error) {
	if req.GetCode() == "" || req.GetCodeVerifier() == "" || req.GetClientId() == "" || req.GetRedirectUri() == "" {
		return nil, status.Error(codes.InvalidArgument, "code, code_verifier, client_id and redirect_uri required")
	}

	codeHash := hashToken(req.GetCode())

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to begin transaction: %v", err)
	}
	defer s.rollbackTx(tx)
	txQueries := commodoredb.New(tx)

	authorizationRow, err := txQueries.LockAuthorizationCode(ctx, codeHash)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.Unauthenticated, "invalid or expired authorization code")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if authorizationRow.ConsumedAt.Valid {
		return nil, status.Error(codes.AlreadyExists, "authorization code already used")
	}
	if authorizationRow.ClientID != req.GetClientId() || authorizationRow.RedirectUri != req.GetRedirectUri() {
		return nil, status.Error(codes.PermissionDenied, "client_id or redirect_uri mismatch")
	}
	if authorizationRow.CodeChallengeMethod != "S256" {
		return nil, status.Error(codes.Internal, "unsupported code_challenge_method")
	}

	h := sha256.Sum256([]byte(req.GetCodeVerifier()))
	computed := base64.RawURLEncoding.EncodeToString(h[:])
	if subtle.ConstantTimeCompare([]byte(computed), []byte(authorizationRow.CodeChallenge)) != 1 {
		return nil, status.Error(codes.PermissionDenied, "code_verifier mismatch")
	}

	if execErr := txQueries.ConsumeAuthorizationCode(ctx, authorizationRow.ID); execErr != nil {
		return nil, status.Errorf(codes.Internal, "failed to mark code consumed: %v", execErr)
	}

	resp, err := s.issueUserSessionTx(ctx, tx, authorizationRow.UserID, authorizationRow.TenantID, "pkce")
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
func (s *CommodoreServer) issueUserSessionTx(ctx context.Context, tx *sql.Tx, userID, tenantID, authType string) (*commodorepb.AuthResponse, error) {
	queries := commodoredb.New(tx)
	userRow, err := queries.GetRefreshUser(ctx, commodoredb.GetRefreshUserParams{ID: userID, TenantID: tenantID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.Unauthenticated, "user not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	user, err := commodoreUserFromRefreshRow(userRow)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !user.IsActive {
		return nil, status.Error(codes.Unauthenticated, "account deactivated")
	}

	jwtSecret := []byte(config.RequireEnv("JWT_SECRET"))
	token, err := auth.GenerateSessionJWT(userID, tenantID, user.Email, user.Role, platformRoles(user.PlatformOperator), time.Now(), jwtSecret)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate token: %v", err)
	}

	refreshToken, err := generateRandomString(40)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate refresh token: %v", err)
	}
	refreshHash := hashToken(refreshToken)
	refreshExpiry := time.Now().Add(30 * 24 * time.Hour)

	if err := queries.InsertRefreshToken(ctx, commodoredb.InsertRefreshTokenParams{
		TenantID: tenantID, UserID: userID, TokenHash: refreshHash, ExpiresAt: refreshExpiry,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to store refresh token: %v", err)
	}

	expiresAt := time.Now().Add(15 * time.Minute)
	s.emitAuthEvent(ctx, eventAuthLoginSucceeded, userID, tenantID, authType, "", "", "")

	return &commodorepb.AuthResponse{
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
func (s *CommodoreServer) StartDeviceAuthorization(ctx context.Context, req *commodorepb.StartDeviceAuthorizationRequest) (*commodorepb.StartDeviceAuthorizationResponse, error) {
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
	queries := commodoredb.New(s.db)
	const maxAttempts = 5
	for range maxAttempts {
		userCode, err = generateUserCode()
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to generate user_code: %v", err)
		}
		err = queries.InsertDeviceAuthorization(ctx, commodoredb.InsertDeviceAuthorizationParams{
			ClientID:            clientID,
			DeviceCodeHash:      deviceCodeHash,
			UserCode:            userCode,
			Scope:               scope,
			PollIntervalSeconds: int32(deviceCodePollInterval.Seconds()),
			ExpiresAt:           expiresAt,
		})
		if err == nil {
			break
		}
		if fwdb.SQLState(err) == "23505" {
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

	return &commodorepb.StartDeviceAuthorizationResponse{
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
func (s *CommodoreServer) PollDeviceAuthorization(ctx context.Context, req *commodorepb.PollDeviceAuthorizationRequest) (*commodorepb.AuthResponse, error) {
	if req.GetDeviceCode() == "" || req.GetClientId() == "" {
		return nil, status.Error(codes.InvalidArgument, "device_code and client_id required")
	}

	deviceCodeHash := hashToken(req.GetDeviceCode())

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to begin transaction: %v", err)
	}
	defer s.rollbackTx(tx)
	txQueries := commodoredb.New(tx)

	deviceRow, err := txQueries.LockDeviceAuthorizationByHash(ctx, deviceCodeHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.PermissionDenied, "ACCESS_DENIED")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if deviceRow.ClientID != req.GetClientId() {
		return nil, status.Error(codes.PermissionDenied, "ACCESS_DENIED")
	}

	now := time.Now()
	if now.After(deviceRow.ExpiresAt) || deviceRow.Status == "expired" {
		if execErr := txQueries.ExpireDeviceAuthorization(ctx, deviceRow.ID); execErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to expire device_code: %v", execErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to commit: %v", commitErr)
		}
		return nil, status.Error(codes.FailedPrecondition, "EXPIRED_TOKEN")
	}
	if deviceRow.Status == "denied" {
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to commit: %v", commitErr)
		}
		return nil, status.Error(codes.PermissionDenied, "ACCESS_DENIED")
	}
	if deviceRow.Status == "pending" {
		// SLOW_DOWN: client polled before its returned interval elapsed.
		if deviceRow.LastPolledAt.Valid && now.Sub(deviceRow.LastPolledAt.Time) < time.Duration(deviceRow.PollIntervalSeconds)*time.Second {
			if execErr := txQueries.TouchDeviceAuthorizationPoll(ctx, deviceRow.ID); execErr != nil {
				return nil, status.Errorf(codes.Internal, "failed to record poll: %v", execErr)
			}
			if commitErr := tx.Commit(); commitErr != nil {
				return nil, status.Errorf(codes.Internal, "failed to commit: %v", commitErr)
			}
			return nil, status.Error(codes.FailedPrecondition, "SLOW_DOWN")
		}
		if execErr := txQueries.TouchDeviceAuthorizationPoll(ctx, deviceRow.ID); execErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to record poll: %v", execErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to commit: %v", commitErr)
		}
		return nil, status.Error(codes.FailedPrecondition, "AUTHORIZATION_PENDING")
	}
	if deviceRow.Status != "approved" || !deviceRow.UserID.Valid || !deviceRow.TenantID.Valid {
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to commit: %v", commitErr)
		}
		return nil, status.Error(codes.FailedPrecondition, "AUTHORIZATION_PENDING")
	}

	// Approved — issue session and consume the row (DELETE so a re-poll
	// returns ACCESS_DENIED on missing row).
	resp, err := s.issueUserSessionTx(ctx, tx, deviceRow.UserID.String, deviceRow.TenantID.String, "device_code")
	if err != nil {
		return nil, err
	}
	if err := txQueries.DeleteDeviceAuthorization(ctx, deviceRow.ID); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to consume device_code: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to commit: %v", err)
	}
	return resp, nil
}

// LookupDeviceAuthorization returns pending device-code metadata for the
// consent page without approving it.
func (s *CommodoreServer) LookupDeviceAuthorization(ctx context.Context, req *commodorepb.LookupDeviceAuthorizationRequest) (*commodorepb.LookupDeviceAuthorizationResponse, error) {
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
	txQueries := commodoredb.New(tx)

	deviceRow, err := txQueries.LockDeviceAuthorizationByUserCode(ctx, normalized)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "user_code not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	if time.Now().After(deviceRow.ExpiresAt) {
		if execErr := txQueries.ExpireDeviceAuthorization(ctx, deviceRow.ID); execErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to expire device_code: %v", execErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to commit: %v", commitErr)
		}
		return nil, status.Error(codes.FailedPrecondition, "user_code expired")
	}
	if deviceRow.Status != "pending" {
		return nil, status.Error(codes.FailedPrecondition, "user_code already resolved")
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, status.Errorf(codes.Internal, "failed to commit: %v", commitErr)
	}

	return &commodorepb.LookupDeviceAuthorizationResponse{
		ClientId:  deviceRow.ClientID,
		Scope:     deviceRow.Scope,
		ExpiresAt: timestamppb.New(deviceRow.ExpiresAt),
	}, nil
}

// ApproveDeviceAuthorization marks a pending device-code row as approved and
// stamps the calling user's identity onto it. Called by the gateway on behalf
// of the webapp /device page after the signed-in user confirms the displayed
// user_code. user_id / tenant_id MUST come from the gateway session.
func (s *CommodoreServer) ApproveDeviceAuthorization(ctx context.Context, req *commodorepb.ApproveDeviceAuthorizationRequest) (*commodorepb.ApproveDeviceAuthorizationResponse, error) {
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
	txQueries := commodoredb.New(tx)

	deviceRow, err := txQueries.LockDeviceAuthorizationByUserCode(ctx, normalized)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "user_code not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	if time.Now().After(deviceRow.ExpiresAt) {
		if execErr := txQueries.ExpireDeviceAuthorization(ctx, deviceRow.ID); execErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to expire device_code: %v", execErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to commit: %v", commitErr)
		}
		return nil, status.Error(codes.FailedPrecondition, "user_code expired")
	}
	if deviceRow.Status != "pending" {
		return nil, status.Error(codes.FailedPrecondition, "user_code already resolved")
	}

	if err := txQueries.ApproveDeviceAuthorization(ctx, commodoredb.ApproveDeviceAuthorizationParams{
		UserID:   sql.NullString{String: userID, Valid: true},
		TenantID: sql.NullString{String: tenantID, Valid: true},
		ID:       deviceRow.ID,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to approve device_code: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to commit: %v", err)
	}

	return &commodorepb.ApproveDeviceAuthorizationResponse{
		Success:  true,
		ClientId: deviceRow.ClientID,
	}, nil
}

// VerifyEmail verifies a user's email address with a token
func (s *CommodoreServer) VerifyEmail(ctx context.Context, req *commodorepb.VerifyEmailRequest) (*commodorepb.VerifyEmailResponse, error) {
	token := req.GetToken()
	if token == "" {
		return nil, status.Error(codes.InvalidArgument, "verification token required")
	}

	// Hash token for lookup (stored hashed in DB)
	tokenHash := hashToken(token)

	// Find user by verification token with expiry check
	queries := commodoredb.New(s.db)
	verificationUser, err := queries.GetVerificationUser(ctx, sql.NullString{String: tokenHash, Valid: true})

	if errors.Is(err, sql.ErrNoRows) {
		return &commodorepb.VerifyEmailResponse{
			Success: false,
			Message: "invalid or expired verification token",
		}, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	if s.purserClient == nil {
		return nil, status.Error(codes.Unavailable, "billing setup is temporarily unavailable; retry verification")
	}
	if _, err = s.purserClient.EnsureFreeAccount(ctx, verificationUser.TenantID); err != nil {
		s.logger.WithError(err).WithField("tenant_id", verificationUser.TenantID).Warn("Free activation failed during email verification")
		return nil, status.Error(codes.Unavailable, "Free account setup is temporarily unavailable; retry verification")
	}

	// Mark verified only after the idempotent Free account exists. If this
	// update fails, the still-valid token can safely retry the same tenant.
	err = queries.VerifyUserEmail(ctx, commodoredb.VerifyUserEmailParams(verificationUser))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to verify email: %v", err)
	}

	return &commodorepb.VerifyEmailResponse{
		Success: true,
		Message: "email verified successfully",
	}, nil
}

// ResendVerification resends the email verification link
func (s *CommodoreServer) ResendVerification(ctx context.Context, req *commodorepb.ResendVerificationRequest) (*commodorepb.ResendVerificationResponse, error) {
	email := normalizeEmail(req.GetEmail())
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
	queries := commodoredb.New(s.db)
	resendUser, err := queries.GetVerificationResendUser(ctx, sql.NullString{String: email, Valid: true})

	if errors.Is(err, sql.ErrNoRows) {
		// Don't reveal if email exists - return success anyway
		return &commodorepb.ResendVerificationResponse{
			Success: true,
			Message: "if an account exists with that email and is unverified, a new verification link will be sent",
		}, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Already verified
	if resendUser.Verified {
		return &commodorepb.ResendVerificationResponse{
			Success: false,
			Message: "email is already verified",
		}, nil
	}

	// Rate limiting: check if token was generated within last 5 minutes
	if resendUser.TokenExpiresAt.Valid {
		// Token expiry is 24h from creation, so creation time is expiry - 24h
		tokenCreatedAt := resendUser.TokenExpiresAt.Time.Add(-24 * time.Hour)
		if time.Since(tokenCreatedAt) < 5*time.Minute {
			return &commodorepb.ResendVerificationResponse{
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
	err = queries.UpdateVerificationToken(ctx, commodoredb.UpdateVerificationTokenParams{
		VerificationToken: sql.NullString{String: tokenHash, Valid: true},
		TokenExpiresAt:    sql.NullTime{Time: tokenExpiry, Valid: true},
		ID:                resendUser.ID,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate verification token: %v", err)
	}

	// Send verification email
	if err := s.sendVerificationEmail(email, verificationToken); err != nil {
		s.logger.WithFields(logging.Fields{
			"user_id": resendUser.ID,
			"email":   email,
			"error":   err,
		}).Error("Failed to send verification email")
		//nolint:nilerr // error returned in response message, not as Go error
		return &commodorepb.ResendVerificationResponse{
			Success: false,
			Message: "failed to send verification email, please try again later",
		}, nil
	}

	s.logger.WithFields(logging.Fields{
		"user_id": resendUser.ID,
		"email":   email,
	}).Info("Verification email resent")

	return &commodorepb.ResendVerificationResponse{
		Success: true,
		Message: "verification email sent",
	}, nil
}

// ForgotPassword initiates the password reset flow
func (s *CommodoreServer) ForgotPassword(ctx context.Context, req *commodorepb.ForgotPasswordRequest) (*commodorepb.ForgotPasswordResponse, error) {
	email := normalizeEmail(req.GetEmail())
	if email == "" {
		return nil, status.Error(codes.InvalidArgument, "email required")
	}

	// Check if user exists
	queries := commodoredb.New(s.db)
	userID, err := queries.FindUserIDByEmail(ctx, sql.NullString{String: email, Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		// Don't reveal whether email exists - always return success
		return &commodorepb.ForgotPasswordResponse{
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
	err = queries.SetPasswordResetToken(ctx, commodoredb.SetPasswordResetTokenParams{
		ResetToken:        sql.NullString{String: resetTokenHash, Valid: true},
		ResetTokenExpires: sql.NullTime{Time: expiresAt, Valid: true},
		ID:                userID,
	})
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

	return &commodorepb.ForgotPasswordResponse{
		Success: true,
		Message: "if an account exists with that email, a reset link will be sent",
	}, nil
}

// ResetPassword resets a user's password with a valid token
func (s *CommodoreServer) ResetPassword(ctx context.Context, req *commodorepb.ResetPasswordRequest) (*commodorepb.ResetPasswordResponse, error) {
	token := req.GetToken()
	password := req.GetPassword()

	if token == "" || password == "" {
		return nil, status.Error(codes.InvalidArgument, "token and password required")
	}

	// Hash token for lookup (uses HMAC if PASSWORD_RESET_SECRET is configured)
	tokenHash := s.hashTokenWithSecret(token)

	// Find user by reset token
	queries := commodoredb.New(s.db)
	userID, err := queries.FindUserByResetToken(ctx, sql.NullString{String: tokenHash, Valid: true})

	if errors.Is(err, sql.ErrNoRows) {
		return &commodorepb.ResetPasswordResponse{
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
	err = queries.ResetUserPassword(ctx, commodoredb.ResetUserPasswordParams{
		PasswordHash: sql.NullString{String: hashedPassword, Valid: true}, ID: userID,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update password: %v", err)
	}

	return &commodorepb.ResetPasswordResponse{
		Success: true,
		Message: "password reset successfully",
	}, nil
}

// UpdateMe updates the current user's profile
func (s *CommodoreServer) UpdateMe(ctx context.Context, req *commodorepb.UpdateMeRequest) (*commodorepb.User, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	if req.PhoneNumber != nil && *req.PhoneNumber != "" {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	if req.FirstName == nil && req.LastName == nil {
		return nil, status.Error(codes.InvalidArgument, "no fields to update")
	}
	queries := commodoredb.New(s.db)
	switch {
	case req.FirstName != nil && req.LastName != nil:
		err = queries.UpdateUserName(ctx, commodoredb.UpdateUserNameParams{
			FirstName: sql.NullString{String: *req.FirstName, Valid: true},
			LastName:  sql.NullString{String: *req.LastName, Valid: true},
			ID:        userID, TenantID: tenantID,
		})
	case req.FirstName != nil:
		err = queries.UpdateUserFirstName(ctx, commodoredb.UpdateUserFirstNameParams{
			FirstName: sql.NullString{String: *req.FirstName, Valid: true}, ID: userID, TenantID: tenantID,
		})
	case req.LastName != nil:
		err = queries.UpdateUserLastName(ctx, commodoredb.UpdateUserLastNameParams{
			LastName: sql.NullString{String: *req.LastName, Valid: true}, ID: userID, TenantID: tenantID,
		})
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update profile: %v", err)
	}

	// Return updated user
	return s.GetMe(ctx, &commodorepb.GetMeRequest{})
}

// UpdateNewsletter updates the user's newsletter subscription in Listmonk (source of truth)
func (s *CommodoreServer) UpdateNewsletter(ctx context.Context, req *commodorepb.UpdateNewsletterRequest) (*commodorepb.UpdateNewsletterResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Get user email and name from DB
	newsletterUser, err := commodoredb.New(s.db).GetNewsletterUser(ctx, commodoredb.GetNewsletterUserParams{
		ID: userID, TenantID: tenantID,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to fetch user: %v", err)
	}

	if !newsletterUser.Email.Valid || newsletterUser.Email.String == "" {
		return nil, status.Error(codes.FailedPrecondition, "email required for newsletter subscription")
	}

	name := strings.TrimSpace(newsletterUser.FirstName.String + " " + newsletterUser.LastName.String)
	if name == "" {
		name = newsletterUser.Email.String
	}

	if s.listmonkClient == nil {
		return nil, status.Error(codes.Unavailable, "newsletter service not configured")
	}

	if req.GetSubscribed() {
		// Subscribe to the newsletter list
		err = s.listmonkClient.Subscribe(ctx, newsletterUser.Email.String, name, s.defaultMailingListID, true)
	} else {
		// Unsubscribe from the newsletter list (not global blocklist)
		// First get the subscriber ID
		info, exists, lookupErr := s.listmonkClient.GetSubscriber(ctx, newsletterUser.Email.String)
		if lookupErr != nil {
			s.logger.WithError(lookupErr).WithField("email", newsletterUser.Email.String).Error("Failed to lookup subscriber in Listmonk")
			return nil, status.Errorf(codes.Internal, "failed to lookup subscriber: %v", lookupErr)
		}
		if !exists {
			// Not subscribed anyway, nothing to do
			return &commodorepb.UpdateNewsletterResponse{
				Success: true,
				Message: "newsletter preference updated",
			}, nil
		}
		err = s.listmonkClient.Unsubscribe(ctx, info.ID, s.defaultMailingListID)
	}
	if err != nil {
		s.logger.WithError(err).WithField("email", newsletterUser.Email.String).Error("Failed to update newsletter in Listmonk")
		return nil, status.Errorf(codes.Internal, "failed to update newsletter preference: %v", err)
	}

	return &commodorepb.UpdateNewsletterResponse{
		Success: true,
		Message: "newsletter preference updated",
	}, nil
}

// GetNewsletterStatus returns the user's current newsletter subscription status from Listmonk
func (s *CommodoreServer) GetNewsletterStatus(ctx context.Context, req *commodorepb.GetNewsletterStatusRequest) (*commodorepb.GetNewsletterStatusResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Get user email from DB
	email, err := commodoredb.New(s.db).GetUserEmail(ctx, commodoredb.GetUserEmailParams{ID: userID, TenantID: tenantID})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to fetch user: %v", err)
	}

	if !email.Valid || email.String == "" {
		// Wallet-only users can't have newsletter subscription
		return &commodorepb.GetNewsletterStatusResponse{Subscribed: false}, nil
	}

	if s.listmonkClient == nil {
		return nil, status.Error(codes.Unavailable, "newsletter service not configured")
	}

	// Query Listmonk for subscriber info
	info, exists, err := s.listmonkClient.GetSubscriber(ctx, email.String)
	if err != nil {
		s.logger.WithError(err).WithField("email", email.String).Warn("Failed to get subscriber from Listmonk")
		return &commodorepb.GetNewsletterStatusResponse{Subscribed: false}, nil
	}

	// If subscriber doesn't exist in Listmonk, return unsubscribed
	if !exists {
		return &commodorepb.GetNewsletterStatusResponse{Subscribed: false}, nil
	}

	// Check if subscribed to the newsletter list specifically
	return &commodorepb.GetNewsletterStatusResponse{Subscribed: info.IsSubscribedToList(s.defaultMailingListID)}, nil
}

// ============================================================================
// WALLET AUTHENTICATION (x402 / agent access)
// ============================================================================

func (s *CommodoreServer) IssueWalletChallenge(ctx context.Context, req *commodorepb.IssueWalletChallengeRequest) (*commodorepb.IssueWalletChallengeResponse, error) {
	normalizedAddr, err := auth.NormalizeEthAddress(req.GetWalletAddress())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid address: %v", err)
	}
	chainID := req.GetChainId()
	switch chainID {
	case 1, 8453, 42161:
	default:
		return nil, status.Error(codes.InvalidArgument, "unsupported wallet login chain")
	}

	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("WEBAPP_PUBLIC_URL")), "/")
	parsedURL, err := url.Parse(baseURL)
	if err != nil || parsedURL.Host == "" || !walletChallengeOriginAllowed(parsedURL) {
		return nil, status.Error(codes.FailedPrecondition, "WEBAPP_PUBLIC_URL must be an absolute HTTPS URL (HTTP loopback is allowed in development)")
	}
	nonce, err := generateRandomString(24)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to generate wallet challenge")
	}
	issuedAt := time.Now().UTC().Truncate(time.Second)
	expiresAt := issuedAt.Add(5 * time.Minute)
	message := fmt.Sprintf("%s wants you to sign in with your Ethereum account:\n%s\n\nSign in to FrameWorks.\n\nURI: %s\nVersion: 1\nChain ID: %d\nNonce: %s\nIssued At: %s\nExpiration Time: %s",
		parsedURL.Host, normalizedAddr, baseURL, chainID, nonce, issuedAt.Format(time.RFC3339), expiresAt.Format(time.RFC3339))
	messageHash := sha256.Sum256([]byte(message))
	if err := commodoredb.New(s.db).InsertWalletChallenge(ctx, commodoredb.InsertWalletChallengeParams{
		WalletAddress: normalizedAddr, ChainID: int64(chainID), MessageHash: messageHash[:], ExpiresAt: expiresAt,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to store wallet challenge: %v", err)
	}
	return &commodorepb.IssueWalletChallengeResponse{
		Message:   message,
		ExpiresAt: timestamppb.New(expiresAt),
	}, nil
}

func walletChallengeOriginAllowed(origin *url.URL) bool {
	if origin == nil || origin.Host == "" {
		return false
	}
	if origin.Scheme == "https" {
		return true
	}
	if origin.Scheme != "http" || !config.IsDevelopment() {
		return false
	}
	host := strings.ToLower(origin.Hostname())
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func (s *CommodoreServer) consumeWalletChallenge(ctx context.Context, walletAddress, message string) error {
	messageHash := sha256.Sum256([]byte(message))
	_, err := commodoredb.New(s.db).ConsumeWalletChallenge(ctx, commodoredb.ConsumeWalletChallengeParams{
		WalletAddress: walletAddress, MessageHash: messageHash[:],
	})
	if errors.Is(err, sql.ErrNoRows) {
		return status.Error(codes.Unauthenticated, "wallet challenge is invalid, expired, or already used")
	}
	if err != nil {
		return status.Errorf(codes.Internal, "failed to consume wallet challenge: %v", err)
	}
	return nil
}

// WalletLogin authenticates via Ethereum wallet signature
// If the wallet is not linked to any account, creates a new one (auto-provisioning)
func (s *CommodoreServer) WalletLogin(ctx context.Context, req *commodorepb.WalletLoginRequest) (*commodorepb.AuthResponse, error) { //nolint:govet // Challenge consumption is an independent failure scope.
	walletAddr := req.GetWalletAddress()
	message := req.GetMessage()
	signature := req.GetSignature()

	if walletAddr == "" || message == "" || signature == "" {
		return nil, status.Error(codes.InvalidArgument, "wallet_address, message, and signature required")
	}

	// Normalize before signature verification and challenge consumption so all
	// callers share one address identity regardless of checksum casing.
	normalizedAddr, err := auth.NormalizeEthAddress(walletAddr)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid address: %v", err)
	}
	valid, err := auth.VerifyEthSignature(normalizedAddr, message, signature)
	if err != nil {
		s.logger.WithError(err).WithField("wallet", normalizedAddr).Warn("Wallet signature verification failed")
		return nil, status.Errorf(codes.InvalidArgument, "signature verification failed: %v", err)
	}
	if !valid {
		return nil, status.Error(codes.Unauthenticated, "invalid signature")
	}
	if challengeErr := s.consumeWalletChallenge(ctx, normalizedAddr, message); challengeErr != nil {
		return nil, challengeErr
	}

	// Resolve or create wallet identity (single source of truth)
	attr := req.GetAttribution()
	if attr == nil {
		attr = &commonpb.SignupAttribution{
			SignupChannel: "wallet",
			SignupMethod:  "wallet_ethereum",
		}
	}
	walletResp, err := s.GetOrCreateWalletUser(ctx, &commodorepb.GetOrCreateWalletUserRequest{
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

	queries := commodoredb.New(s.db)
	profileRow, err := queries.GetUserProfile(ctx, commodoredb.GetUserProfileParams{ID: userID, TenantID: tenantID})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to fetch user: %v", err)
	}
	profile, err := commodoreUserFromProfileRow(profileRow)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to fetch user: %v", err)
	}

	// Update last_auth_at on wallet identity
	if touchErr := queries.TouchWalletIdentityAuth(ctx, commodoredb.TouchWalletIdentityAuthParams{
		ChainType: string(auth.ChainEthereum), WalletAddress: normalizedAddr,
	}); touchErr != nil {
		s.logger.WithError(touchErr).Debug("Failed to update wallet last_auth_at")
	}

	// Update last_login_at on user
	if touchErr := queries.TouchUserLastLogin(ctx, commodoredb.TouchUserLastLoginParams{ID: userID, TenantID: tenantID}); touchErr != nil {
		s.logger.WithError(touchErr).Debug("Failed to update wallet user last_login_at")
	}

	// Generate JWT
	jwtSecret := []byte(config.RequireEnv("JWT_SECRET"))
	token, err := auth.GenerateSessionJWT(userID, tenantID, profile.Email, profile.Role, platformRoles(profile.PlatformOperator), time.Now(), jwtSecret)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate token: %v", err)
	}
	if !profile.IsActive {
		s.emitAuthEvent(ctx, eventAuthLoginFailed, userID, tenantID, "wallet", "", "", "account_inactive")
		return nil, status.Error(codes.Unauthenticated, "account deactivated")
	}

	refreshToken, err := generateRandomString(40)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate refresh token: %v", err)
	}
	refreshHash := hashToken(refreshToken)
	refreshExpiry := time.Now().Add(30 * 24 * time.Hour)
	if err := queries.InsertRefreshToken(ctx, commodoredb.InsertRefreshTokenParams{
		TenantID: tenantID, UserID: userID, TokenHash: refreshHash, ExpiresAt: refreshExpiry,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create wallet session: %v", err)
	}
	expiresAt := time.Now().Add(15 * time.Minute)

	// Build user response
	user := profile.toProtoUser(userID, tenantID)

	s.emitAuthEvent(ctx, eventAuthLoginSucceeded, userID, tenantID, "wallet", "", "", "")

	return &commodorepb.AuthResponse{
		Token:        token,
		RefreshToken: refreshToken,
		User:         user,
		ExpiresAt:    timestamppb.New(expiresAt),
		IsNewUser:    isNewUser,
	}, nil
}

// LinkWallet links a wallet to the authenticated user's account
func (s *CommodoreServer) LinkWallet(ctx context.Context, req *commodorepb.LinkWalletRequest) (*commodorepb.WalletIdentity, error) { //nolint:govet // Challenge consumption is an independent failure scope.
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

	normalizedAddr, err := auth.NormalizeEthAddress(walletAddr)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid address: %v", err)
	}
	valid, err := auth.VerifyEthSignature(normalizedAddr, message, signature)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "signature verification failed: %v", err)
	}
	if !valid {
		return nil, status.Error(codes.Unauthenticated, "invalid signature")
	}
	if challengeErr := s.consumeWalletChallenge(ctx, normalizedAddr, message); challengeErr != nil {
		return nil, challengeErr
	}

	// Check if wallet is already linked to another user
	queries := commodoredb.New(s.db)
	existingWallet, err := queries.GetWalletIdentityByAddress(ctx, commodoredb.GetWalletIdentityByAddressParams{
		ChainType: string(auth.ChainEthereum), WalletAddress: normalizedAddr,
	})
	if err == nil {
		if existingWallet.UserID == userID {
			return nil, status.Error(codes.AlreadyExists, "wallet already linked to your account")
		}
		return nil, status.Error(codes.AlreadyExists, "wallet already linked to another account")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, status.Errorf(codes.Internal, "failed to check wallet: %v", err)
	}

	// Create wallet identity
	linkedWallet, err := queries.InsertLinkedWallet(ctx, commodoredb.InsertLinkedWalletParams{
		TenantID: tenantID, UserID: userID, WalletAddress: normalizedAddr,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to link wallet: %v", err)
	}
	if !linkedWallet.CreatedAt.Valid {
		return nil, status.Error(codes.Internal, "linked wallet is missing created_at")
	}

	s.emitAuthEvent(ctx, eventWalletLinked, userID, tenantID, "wallet", linkedWallet.ID, "", "")

	return &commodorepb.WalletIdentity{
		Id:            linkedWallet.ID,
		WalletAddress: normalizedAddr,
		CreatedAt:     timestamppb.New(linkedWallet.CreatedAt.Time),
	}, nil
}

// UnlinkWallet removes a wallet from the user's account
func (s *CommodoreServer) UnlinkWallet(ctx context.Context, req *commodorepb.UnlinkWalletRequest) (*commodorepb.UnlinkWalletResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	walletID := req.GetWalletId()
	if walletID == "" {
		return nil, status.Error(codes.InvalidArgument, "wallet_id required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to begin wallet unlink")
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort after commit or an early return
	txQueries := commodoredb.New(tx)

	// Locking the user serializes concurrent unlink attempts. Without this lock,
	// two requests could each observe another wallet and remove both, locking a
	// wallet-only user out of the account.
	hasPasswordSignin, lockErr := txQueries.LockUserAuthenticationMethods(ctx, commodoredb.LockUserAuthenticationMethodsParams{
		ID: userID, TenantID: tenantID,
	})
	if errors.Is(lockErr, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "user not found")
	} else if lockErr != nil {
		return nil, status.Error(codes.Internal, "failed to verify account authentication methods")
	}

	owned, ownedErr := txQueries.UserOwnsWallet(ctx, commodoredb.UserOwnsWalletParams{
		ID: walletID, UserID: userID, TenantID: tenantID,
	})
	if ownedErr != nil {
		return nil, status.Error(codes.Internal, "failed to verify wallet ownership")
	}
	if !owned {
		return nil, status.Error(codes.NotFound, "wallet not found or not owned by you")
	}

	if !hasPasswordSignin.Valid || !hasPasswordSignin.Bool {
		walletCount, countErr := txQueries.CountUserWallets(ctx, commodoredb.CountUserWalletsParams{
			UserID: userID, TenantID: tenantID,
		})
		if countErr != nil {
			return nil, status.Error(codes.Internal, "failed to verify account authentication methods")
		}
		if walletCount <= 1 {
			return nil, status.Error(codes.FailedPrecondition, "cannot unlink the final wallet until another sign-in method is configured")
		}
	}

	_, err = txQueries.DeleteUserWallet(ctx, commodoredb.DeleteUserWalletParams{
		ID: walletID, UserID: userID, TenantID: tenantID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "wallet not found or not owned by you")
		}
		return nil, status.Error(codes.Internal, "failed to unlink wallet")
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Error(codes.Internal, "failed to commit wallet unlink")
	}

	s.emitAuthEvent(ctx, eventWalletUnlinked, userID, tenantID, "wallet", walletID, "", "")

	return &commodorepb.UnlinkWalletResponse{
		Success: true,
		Message: "wallet unlinked",
	}, nil
}

// ListWallets returns all wallets linked to the authenticated user
func (s *CommodoreServer) ListWallets(ctx context.Context, req *commodorepb.ListWalletsRequest) (*commodorepb.ListWalletsResponse, error) {
	userID, _, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	rows, err := commodoredb.New(s.db).ListUserWallets(ctx, userID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list wallets: %v", err)
	}

	var wallets []*commodorepb.WalletIdentity
	for _, row := range rows {
		if !row.CreatedAt.Valid {
			continue
		}
		w := &commodorepb.WalletIdentity{
			Id:            row.ID,
			WalletAddress: row.WalletAddress,
			CreatedAt:     timestamppb.New(row.CreatedAt.Time),
		}
		if row.LastAuthAt.Valid {
			w.LastAuthAt = timestamppb.New(row.LastAuthAt.Time)
		}
		wallets = append(wallets, w)
	}

	return &commodorepb.ListWalletsResponse{Wallets: wallets}, nil
}

// LinkEmail adds an email to a wallet-only account (for postpaid upgrade path)
func (s *CommodoreServer) LinkEmail(ctx context.Context, req *commodorepb.LinkEmailRequest) (*commodorepb.LinkEmailResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	email := normalizeEmail(req.GetEmail())
	password := req.GetPassword()

	if email == "" {
		return nil, status.Error(codes.InvalidArgument, "email required")
	}
	if password == "" || len(password) < 8 {
		return nil, status.Error(codes.InvalidArgument, "password must be at least 8 characters")
	}

	// Check if user already has an email
	queries := commodoredb.New(s.db)
	existingEmail, err := queries.GetUserEmail(ctx, commodoredb.GetUserEmailParams{ID: userID, TenantID: tenantID})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to check user: %v", err)
	}
	if existingEmail.Valid && existingEmail.String != "" {
		return nil, status.Error(codes.AlreadyExists, "email already linked to your account")
	}

	// Check if email is already used by another account
	_, err = queries.FindOtherUserIDByEmail(ctx, commodoredb.FindOtherUserIDByEmailParams{
		Email: sql.NullString{String: email, Valid: true}, ID: userID,
	})
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
	verificationTokenHash := hashToken(verificationToken)
	tokenExpiry := time.Now().Add(24 * time.Hour)

	// Update user with email, password, and verification token
	err = queries.LinkUserEmail(ctx, commodoredb.LinkUserEmailParams{
		Email:             sql.NullString{String: email, Valid: true},
		PasswordHash:      sql.NullString{String: passwordHash, Valid: true},
		VerificationToken: sql.NullString{String: verificationTokenHash, Valid: true},
		TokenExpiresAt:    sql.NullTime{Time: tokenExpiry, Valid: true},
		ID:                userID,
		TenantID:          tenantID,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to link email: %v", err)
	}

	// Send verification email
	if err := s.sendVerificationEmail(email, verificationToken); err != nil {
		s.logger.WithError(err).Warn("Failed to send verification email")
		return &commodorepb.LinkEmailResponse{
			Success:          true,
			Message:          "Email linked. Verification email could not be sent - please use resend verification.",
			VerificationSent: false,
		}, nil
	}

	return &commodorepb.LinkEmailResponse{
		Success:          true,
		Message:          fmt.Sprintf("Verification email sent to %s", email),
		VerificationSent: true,
	}, nil
}

// ============================================================================
// STREAM SERVICE (Gateway → Commodore for stream CRUD)
// ============================================================================

// CreateStream creates a new stream for the authenticated user
func (s *CommodoreServer) CreateStream(ctx context.Context, req *commodorepb.CreateStreamRequest) (*commodorepb.CreateStreamResponse, error) {
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
	txQueries := commodoredb.New(tx)

	// Keep stream creation and requested initial state atomic. Pull streams
	// must not leak as push streams if source persistence fails.
	created, err := txQueries.CreateUserStreamProcedure(ctx, commodoredb.CreateUserStreamProcedureParams{
		TenantID: tenantID, UserID: userID, Title: title,
	})
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
		err = txQueries.MarkCreatedStreamPull(ctx, commodoredb.MarkCreatedStreamPullParams{
			ID: created.StreamID, TenantID: tenantID,
		})
		if err == nil {
			err = txQueries.InsertCreatedPullSource(ctx, commodoredb.InsertCreatedPullSourceParams{
				StreamID:          created.StreamID,
				SourceUriEnc:      encURI,
				Enabled:           pullSourceEnabled(req.GetPullSource()),
				AllowedClusterIds: pullAllowedClusterIDs,
			})
		}
		if err != nil {
			s.logger.WithError(err).WithField("stream_id", created.StreamID).Error("Failed to persist pull source")
			return nil, status.Errorf(codes.Internal, "failed to persist pull source: %v", err)
		}
	}

	// Update description if provided
	if req.GetDescription() != "" {
		err = txQueries.SetCreatedStreamDescription(ctx, commodoredb.SetCreatedStreamDescriptionParams{
			Description: sql.NullString{String: req.GetDescription(), Valid: true}, ID: created.StreamID,
		})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to update stream description: %v", err)
		}
	}

	// Update recording setting if requested
	if req.GetIsRecording() {
		err = txQueries.EnableCreatedStreamRecording(ctx, created.StreamID)
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
	s.emitStreamChangeEvent(ctx, eventStreamCreated, tenantID, userID, created.StreamID, changedFields)

	resp := &commodorepb.CreateStreamResponse{
		Id:          created.StreamID,
		StreamKey:   created.StreamKey,
		PlaybackId:  created.PlaybackID,
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
func (s *CommodoreServer) populateTieredDomains(ctx context.Context, tenantID string, resp *commodorepb.CreateStreamResponse) {
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
	aliasResp, aliasErr := s.navigatorClient.GetTenantAliasStatus(aliasCtx, &dnspb.GetTenantAliasStatusRequest{TenantId: tenantID})
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
func (s *CommodoreServer) GetStream(ctx context.Context, req *commodorepb.GetStreamRequest) (*commodorepb.Stream, error) {
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
func (s *CommodoreServer) ListStreams(ctx context.Context, req *commodorepb.ListStreamsRequest) (*commodorepb.ListStreamsResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// Optional name search — makes the streams picker account-wide (not just the
	// first page), filtering by title/internal_name server-side.
	var searchLike string
	if search := strings.TrimSpace(req.GetSearch()); search != "" {
		searchLike = "%" + strings.ToLower(search) + "%"
	}

	queries := commodoredb.New(s.db)
	var total int32
	if searchLike != "" {
		total, err = queries.CountStreamsForUserSearch(ctx, commodoredb.CountStreamsForUserSearchParams{
			UserID: userID, TenantID: tenantID, Title: searchLike,
		})
	} else {
		total, err = queries.CountStreamsForUser(ctx, commodoredb.CountStreamsForUserParams{
			UserID: userID, TenantID: tenantID,
		})
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	var streams []*commodorepb.Stream
	applySearch := searchLike != ""
	rowLimit := int32(params.Limit + 1)
	appendConfig := func(config commodoredb.StreamConfigRow) error {
		stream, mapErr := s.streamFromConfigRow(config)
		if mapErr != nil {
			return mapErr
		}
		streams = append(streams, stream)
		return nil
	}
	if params.Direction == pagination.Forward {
		if params.Cursor == nil {
			rows, queryErr := queries.ListStreamsForward(ctx, commodoredb.ListStreamsForwardParams{
				UserID: userID, TenantID: tenantID, ApplySearch: applySearch,
				SearchLike: searchLike, RowLimit: rowLimit,
			})
			if queryErr != nil {
				return nil, status.Errorf(codes.Internal, "database error: %v", queryErr)
			}
			for _, row := range rows {
				if mapErr := appendConfig(row.Config()); mapErr != nil {
					return nil, status.Errorf(codes.Internal, "scan stream: %v", mapErr)
				}
			}
		} else {
			rows, queryErr := queries.ListStreamsForwardAfter(ctx, commodoredb.ListStreamsForwardAfterParams{
				UserID: userID, TenantID: tenantID, ApplySearch: applySearch, SearchLike: searchLike,
				CursorTime: params.Cursor.Timestamp, CursorID: params.Cursor.ID, RowLimit: rowLimit,
			})
			if queryErr != nil {
				return nil, status.Errorf(codes.Internal, "database error: %v", queryErr)
			}
			for _, row := range rows {
				if mapErr := appendConfig(row.Config()); mapErr != nil {
					return nil, status.Errorf(codes.Internal, "scan stream: %v", mapErr)
				}
			}
		}
	} else if params.Cursor == nil {
		rows, queryErr := queries.ListStreamsBackward(ctx, commodoredb.ListStreamsBackwardParams{
			UserID: userID, TenantID: tenantID, ApplySearch: applySearch,
			SearchLike: searchLike, RowLimit: rowLimit,
		})
		if queryErr != nil {
			return nil, status.Errorf(codes.Internal, "database error: %v", queryErr)
		}
		for _, row := range rows {
			if mapErr := appendConfig(row.Config()); mapErr != nil {
				return nil, status.Errorf(codes.Internal, "scan stream: %v", mapErr)
			}
		}
	} else {
		rows, queryErr := queries.ListStreamsBackwardBefore(ctx, commodoredb.ListStreamsBackwardBeforeParams{
			UserID: userID, TenantID: tenantID, ApplySearch: applySearch, SearchLike: searchLike,
			CursorTime: params.Cursor.Timestamp, CursorID: params.Cursor.ID, RowLimit: rowLimit,
		})
		if queryErr != nil {
			return nil, status.Errorf(codes.Internal, "database error: %v", queryErr)
		}
		for _, row := range rows {
			if mapErr := appendConfig(row.Config()); mapErr != nil {
				return nil, status.Errorf(codes.Internal, "scan stream: %v", mapErr)
			}
		}
	}

	var route *clusterRoute
	routeAttempted := false
	for _, stream := range streams {
		if !routeAttempted {
			routeAttempted = true
			if resolved, routeErr := s.resolveClusterRouteForTenant(ctx, tenantID); routeErr == nil {
				route = resolved
			}
		}
		if route != nil {
			s.populateStreamOriginRegion(ctx, tenantID, stream, route)
		}
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
	resp := &commodorepb.ListStreamsResponse{
		Streams: streams,
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

// UpdateStream updates a stream's properties
func (s *CommodoreServer) UpdateStream(ctx context.Context, req *commodorepb.UpdateStreamRequest) (*commodorepb.Stream, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	// Verify ownership and fetch immutable ingest mode for validation.
	queries := commodoredb.New(s.db)
	currentState, err := queries.GetStreamUpdateState(ctx, commodoredb.GetStreamUpdateStateParams{
		ID: streamID, UserID: userID, TenantID: tenantID,
	})
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
		if requestedMode != currentState.IngestMode {
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
		if currentState.IngestMode != "pull" {
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

	updateParams := commodoredb.UpdateStreamFieldsParams{
		StreamID: streamID, UserID: userID, TenantID: tenantID,
	}
	applyStreamUpdate := false
	changedFields := []string{}

	if req.Name != nil {
		updateParams.ApplyTitle = true
		updateParams.Title = *req.Name
		applyStreamUpdate = true
		changedFields = append(changedFields, "title")
	}
	if req.Description != nil {
		updateParams.ApplyDescription = true
		updateParams.Description = sql.NullString{String: *req.Description, Valid: true}
		applyStreamUpdate = true
		changedFields = append(changedFields, "description")
	}
	if req.Record != nil {
		updateParams.ApplyRecording = true
		updateParams.RecordingEnabled = *req.Record
		applyStreamUpdate = true
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
				existing, lookupErr := queries.GetStreamDVRChapterInterval(ctx, commodoredb.GetStreamDVRChapterIntervalParams{
					ID: streamID, TenantID: tenantID,
				})
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
		updateParams.ApplyChapterMode = true
		if mode := strings.TrimSpace(req.GetDvrChapterMode()); mode != "" && strings.ToLower(mode) != "none" {
			updateParams.ChapterMode = sql.NullString{String: mode, Valid: true}
		}
		applyStreamUpdate = true
		changedFields = append(changedFields, "dvr_chapter_mode")
	}
	if req.DvrChapterIntervalSeconds != nil {
		updateParams.ApplyChapterInterval = true
		if iv := req.GetDvrChapterIntervalSeconds(); iv > 0 {
			updateParams.ChapterInterval = sql.NullInt32{Int32: iv, Valid: true}
		}
		applyStreamUpdate = true
		changedFields = append(changedFields, "dvr_chapter_interval_seconds")
	}
	if req.Monitoring != nil {
		// Tri-state nullable column: INHERIT -> NULL, ON -> true, OFF -> false.
		updateParams.ApplyMonitoring = true
		switch req.GetMonitoring() {
		case commodorepb.MonitoringToggle_MONITORING_TOGGLE_ON:
			updateParams.MonitoringEnabled = sql.NullBool{Bool: true, Valid: true}
		case commodorepb.MonitoringToggle_MONITORING_TOGGLE_OFF:
			updateParams.MonitoringEnabled = sql.NullBool{Bool: false, Valid: true}
		}
		applyStreamUpdate = true
		changedFields = append(changedFields, "monitoring_enabled")
	}

	if !applyStreamUpdate && pullSource == nil {
		return s.queryStream(ctx, streamID, userID, tenantID)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to begin transaction: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort after Commit
	txQueries := commodoredb.New(tx)

	if applyStreamUpdate {
		var rows int64
		rows, err = txQueries.UpdateStreamFields(ctx, updateParams)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to update stream: %v", err)
		}
		if rows == 0 {
			return nil, status.Error(codes.NotFound, "stream not found")
		}
	}

	if pullSource != nil {
		if pullPlan.writeURI || pullPlan.writeEnabled || pullPlan.writeAllowed {
			var rows int64
			rows, err = txQueries.UpdatePullSourceFields(ctx, commodoredb.UpdatePullSourceFieldsParams{
				ApplyUri:          pullPlan.writeURI,
				SourceUriEnc:      pullPlan.encryptedURI,
				ApplyEnabled:      pullPlan.writeEnabled,
				Enabled:           pullPlan.enabledValue,
				ApplyAllowed:      pullPlan.writeAllowed,
				AllowedClusterIds: pullPlan.allowedClusters,
				StreamID:          streamID,
			})
			if err != nil {
				return nil, status.Errorf(codes.Internal, "failed to update pull source: %v", err)
			}
			if rows == 0 {
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

	if req.Record != nil && req.GetRecord() && (!currentState.IsRecordingEnabled.Valid || !currentState.IsRecordingEnabled.Bool) {
		go s.startDVRAfterStreamUpdate(userID, tenantID, streamID, currentState.InternalName)
	}

	return s.queryStream(ctx, streamID, userID, tenantID)
}

func (s *CommodoreServer) startDVRAfterStreamUpdate(userID, tenantID, streamID, internalName string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, userID)
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)

	_, err := s.StartDVR(ctx, &sharedpb.StartDVRRequest{
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
func (s *CommodoreServer) DeleteStream(ctx context.Context, req *commodorepb.DeleteStreamRequest) (*commodorepb.DeleteStreamResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	queries := commodoredb.New(s.db)
	stream, err := queries.GetStreamForDeletion(ctx, commodoredb.GetStreamForDeletionParams{
		ID: streamID, UserID: userID, TenantID: tenantID,
	})

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "stream not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// PHASE 1 (deleting): in ONE local tx, SOFT-delete the stream (deleted_at set → excluded from all serving/
	// listing/resolve reads) + stop ingest (drop stream_keys) + enqueue the thumbnail-cleanup obligation. We do NOT
	// hard-delete the stream row yet: the deletion is not "done" until the SERVING authority (Foghorn) durably holds
	// the tombstone. No RPC is held across this tx.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to begin transaction: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	txQueries := commodoredb.New(tx)
	if err = txQueries.DeleteStreamKeysForDeletion(ctx, commodoredb.DeleteStreamKeysForDeletionParams{
		StreamID: streamID, TenantID: tenantID,
	}); err != nil {
		s.logger.WithError(err).Warn("Failed to delete stream keys")
	}
	if err = txQueries.SoftDeleteStream(ctx, commodoredb.SoftDeleteStreamParams{ID: streamID, TenantID: tenantID}); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to soft-delete stream: %v", err)
	}

	// Enqueue the cleanup obligation ATOMICALLY with the soft-delete (idempotent on re-delete). The stream-cleanup
	// outbox worker durably delivers it to Foghorn and finalizes the deletion on a positive ack.
	if oErr := s.enqueueStreamCleanupOutbox(ctx, tx, streamID, tenantID); oErr != nil {
		return nil, status.Errorf(codes.Internal, "failed to record stream cleanup obligation: %v", oErr)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to commit: %v", err)
	}

	// The TERMINAL stream_deleted event is emitted by finalizeStreamDeletion (below, or by the outbox convergence)
	// — ONLY after Foghorn acks the tombstone — never here at soft-delete time, so a pending deletion is never
	// reported as done. Child-media (clip/DVR) deletion is NOT done here best-effort anymore: it is part of the
	// durable stream-cleanup obligation (dispatchStreamCleanupOutboxRow → deleteStreamChildMedia), so finalization
	// is gated on every child being gone and a delivery outage can never strand surviving clips/DVR.

	// PHASE 2 (tombstoned → deleted): best-effort SYNCHRONOUS delivery so the common case finalizes promptly. Only a
	// POSITIVE Foghorn ack of the FULL cascade (thumbnail tombstone + every child clip/DVR) lets us FINALIZE —
	// hard-delete the row + mark the outbox completed — and report "deleted". Otherwise the row stays soft-deleted
	// and we return deletion_pending; the outbox worker converges it. We NEVER report "deleted" while any child (or
	// the tombstone) is unacknowledged.
	deletionStatus := "deletion_pending"
	message := "Stream deletion pending: awaiting cleanup acknowledgement from the serving cell"
	// Route through the SAME multi-cell dispatcher the outbox uses — live thumbnails live on the ingest cell(s), so a
	// tenant-primary-only sync delivery would finalize while an ingest cell's objects survive. deleteStreamThumbnails
	// returns nil only once EVERY recorded owning cell has acked.
	if tErr := s.deleteStreamThumbnails(ctx, streamID, tenantID); tErr == nil {
		if cErr := s.deleteStreamChildMedia(ctx, streamID, tenantID); cErr != nil {
			s.logger.WithError(cErr).WithField("stream_id", streamID).Info("stream child-media cleanup incomplete; leaving pending for the outbox worker")
		} else if fErr := s.finalizeStreamDeletion(ctx, streamID, tenantID, ""); fErr != nil {
			s.logger.WithError(fErr).WithField("stream_id", streamID).Warn("stream deletion finalize failed after ack; outbox worker will retry")
		} else {
			deletionStatus = "deleted"
			message = "Stream deleted successfully"
		}
	} else {
		s.logger.WithError(tErr).WithField("stream_id", streamID).Info("stream thumbnail cleanup incomplete; leaving pending for the outbox worker")
	}

	return &commodorepb.DeleteStreamResponse{
		Message:        message,
		StreamId:       streamID,
		StreamTitle:    stream.Title,
		DeletedAt:      timestamppb.Now(),
		DeletionStatus: deletionStatus,
	}, nil
}

// errParentStreamDeleted fences child-media creation (clip/DVR) against a concurrent stream deletion.
var errParentStreamDeleted = errors.New("parent stream is being deleted")

// fenceParentStreamLive LOCKS the parent stream row FOR UPDATE inside the caller's transaction and returns
// errParentStreamDeleted if the stream is soft-deleted or already gone. DeleteStream's phase-1 UPDATE locks the same
// row, so relative to THIS Commodore transaction the child either commits before the delete or observes the tombstone
// and is refused. This synchronous fence does not span the cross-service creation (Commodore's business row commits
// separately from Foghorn's artifact insert); a child that slips through — created against a stream deleted after this
// check — is durably compensated by the stream-cleanup obligation, whose child-media cascade deletes every enumerated
// clip/DVR before finalization. streamID may be empty (a parentless VOD artifact) → no fence; tenant_id scopes the
// lookup per the repo tenant-isolation rule.
func fenceParentStreamLive(ctx context.Context, tx *sql.Tx, tenantID, streamID string) error {
	if strings.TrimSpace(streamID) == "" {
		return nil
	}
	live, err := commodoredb.New(tx).FenceParentStreamLive(ctx, commodoredb.FenceParentStreamLiveParams{
		ID: streamID, TenantID: tenantID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return errParentStreamDeleted
	}
	if err != nil {
		return err
	}
	if !live {
		return errParentStreamDeleted
	}
	return nil
}

// finalizeStreamDeletion completes a two-phase stream deletion after the serving authority (Foghorn) has acked the
// thumbnail-cleanup obligation. It runs the hard-delete, the outbox completion, and the TERMINAL stream_deleted
// event ENQUEUE in ONE transaction, so a crash cannot leave a deleted stream without its event (or an event without
// the delete). Idempotent — a re-run after the row is gone deletes zero rows, marks nothing, and (guarded on
// RowsAffected) does not re-emit. The event is enqueued via EnqueueServiceEventTx so it commits atomically.
// finalizeStreamDeletion runs the hard-delete + outbox completion + terminal event in one transaction. leaseToken
// FENCES the outbox-completion when set (the outbox-worker convergence path): a stale worker whose lease was
// re-claimed cannot complete a row a peer owns. An empty token is the synchronous DeleteStream fast-path (not a
// claimed worker), which is not fenced.
// claimTenantID is the tenant of the CLAIMED outbox row (decoded from the opaque claim identity on the worker path, or
// the request tenant on the synchronous path); it fences the ownership CAS on tenant in addition to stream_id + lease
// token. It is REQUIRED: an empty tenant fails the finalize (leaving the row for lease-expiry/retry) rather than
// settling tenantlessly — the tenant is NOT NULL on the row and always present in a well-formed claim.
func (s *CommodoreServer) finalizeStreamDeletion(ctx context.Context, streamID, claimTenantID, leaseToken string) error {
	if claimTenantID == "" {
		return fmt.Errorf("finalize stream deletion for %s: missing tenant in claim identity", streamID)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin finalize tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // best-effort on non-commit paths
	// OWNERSHIP GATE: settle the outbox row FIRST, token-fenced, and REQUIRE it to affect a row.
	// A leased worker whose lease lapsed (the row was re-claimed with a NEW token) — or any duplicate finalize after a
	// peer already completed the row — matches zero rows here and must NOT hard-delete the stream or emit the terminal
	// event. Only the tx that flips this pending row to completed owns the finalization; everything below runs for
	// that single winner, atomically with the settlement. ($2 = '' is the synchronous DeleteStream fast-path, which
	// still requires a pending row so a converged re-run is a clean no-op.)
	// RETURNING the obligation's tenant_id makes it the authoritative attribution for the rest of this tx — the
	// value captured at enqueue time — and lets every query below fence on the owning tenant, not just the (globally
	// unique) stream_id. Zero rows → sql.ErrNoRows → not our obligation, commit nothing.
	queries := commodoredb.New(tx)
	tenantID, err := queries.SettleStreamCleanupForFinalization(ctx, commodoredb.SettleStreamCleanupForFinalizationParams{
		StreamID:   streamID,
		LeaseToken: leaseToken,
		TenantID:   claimTenantID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		// Not our obligation (lease lost, or a peer/fast-path already finalized) — commit nothing.
		return nil
	}
	if err != nil {
		return fmt.Errorf("settle stream cleanup outbox: %w", err)
	}

	// We own the finalization. Read attribution (user) from the still-soft-deleted row (present until the DELETE
	// below), tenant-fenced, so the terminal event carries tenant/user, then hard-delete and emit — all atomic with
	// the settlement above.
	userID, sErr := queries.GetStreamFinalizationUser(ctx, commodoredb.GetStreamFinalizationUserParams{
		StreamID: streamID,
		TenantID: tenantID,
	})
	if sErr != nil && !errors.Is(sErr, sql.ErrNoRows) {
		return fmt.Errorf("read finalize attribution: %w", sErr)
	}
	n, err := queries.HardDeleteFinalizedStream(ctx, commodoredb.HardDeleteFinalizedStreamParams{
		StreamID: streamID,
		TenantID: tenantID,
	})
	if err != nil {
		return fmt.Errorf("hard-delete finalized stream: %w", err)
	}
	// Enqueue the terminal event ONLY when THIS call performed the hard-delete (RowsAffected > 0), atomically with
	// it — so a converged re-run never double-emits and a crash never suppresses it. Emitting at soft-delete time
	// would tell consumers "deleted" while the saga still reports pending.
	if n > 0 && tenantID != "" {
		if _, err := s.EnqueueServiceEventTx(ctx, tx, s.buildStreamChangeEvent(eventStreamDeleted, tenantID, userID, streamID, nil)); err != nil {
			return fmt.Errorf("enqueue terminal stream_deleted event: %w", err)
		}
	}
	return tx.Commit()
}

// RefreshStreamKey generates a new stream key
func (s *CommodoreServer) RefreshStreamKey(ctx context.Context, req *commodorepb.RefreshStreamKeyRequest) (*commodorepb.RefreshStreamKeyResponse, error) {
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

	// Update the stream (a deletion-pending stream is not actionable — do not rotate its key).
	queries := commodoredb.New(s.db)
	rows, err := queries.RefreshPrimaryStreamKey(ctx, commodoredb.RefreshPrimaryStreamKeyParams{
		StreamKey: newStreamKey, ID: streamID, UserID: userID, TenantID: tenantID,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to refresh stream key: %v", err)
	}
	if rows == 0 {
		return nil, status.Error(codes.NotFound, "stream not found")
	}

	// Get playback ID
	playbackID, err := queries.GetStreamPlaybackID(ctx, commodoredb.GetStreamPlaybackIDParams{
		ID: streamID, UserID: userID, TenantID: tenantID,
	})
	if err != nil {
		s.logger.WithError(err).Warn("Failed to get playback ID for refreshed stream key")
	}

	s.emitStreamChangeEvent(ctx, eventStreamUpdated, tenantID, userID, streamID, []string{"stream_key"})

	return &commodorepb.RefreshStreamKeyResponse{
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
func (s *CommodoreServer) CreateStreamKey(ctx context.Context, req *commodorepb.CreateStreamKeyRequest) (*commodorepb.StreamKeyResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	queries := commodoredb.New(s.db)
	exists, err := queries.StreamExistsForUser(ctx, commodoredb.StreamExistsForUserParams{
		ID: streamID, UserID: userID, TenantID: tenantID,
	})
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

	err = queries.InsertStreamKey(ctx, commodoredb.InsertStreamKeyParams{
		ID: keyID, TenantID: tenantID,
		UserID: sql.NullString{String: userID, Valid: true}, StreamID: streamID,
		KeyValue: keyValue, KeyName: sql.NullString{String: keyName, Valid: true},
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create stream key: %v", err)
	}

	s.emitStreamKeyEvent(ctx, eventStreamKeyCreated, tenantID, userID, streamID, keyID)

	return &commodorepb.StreamKeyResponse{
		StreamKey: &commodorepb.StreamKey{
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
func (s *CommodoreServer) ListStreamKeys(ctx context.Context, req *commodorepb.ListStreamKeysRequest) (*commodorepb.ListStreamKeysResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	queries := commodoredb.New(s.db)
	exists, err := queries.StreamExistsForUser(ctx, commodoredb.StreamExistsForUserParams{
		ID: streamID, UserID: userID, TenantID: tenantID,
	})
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

	// Get total count
	keyUserID := sql.NullString{String: userID, Valid: true}
	total, err := queries.CountStreamKeys(ctx, commodoredb.CountStreamKeysParams{
		StreamID: streamID, UserID: keyUserID, TenantID: tenantID,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	limit := int32(params.Limit + 1)
	var keyRows []commodoredb.CommodoreStreamKey
	if params.Direction == pagination.Forward {
		if params.Cursor == nil {
			keyRows, err = queries.ListStreamKeysForward(ctx, commodoredb.ListStreamKeysForwardParams{
				StreamID: streamID, UserID: keyUserID, TenantID: tenantID, Limit: limit,
			})
		} else {
			keyRows, err = queries.ListStreamKeysForwardAfter(ctx, commodoredb.ListStreamKeysForwardAfterParams{
				StreamID: streamID, UserID: keyUserID, TenantID: tenantID,
				Column4: params.Cursor.Timestamp, Column5: params.Cursor.ID, Limit: limit,
			})
		}
	} else if params.Cursor == nil {
		keyRows, err = queries.ListStreamKeysBackward(ctx, commodoredb.ListStreamKeysBackwardParams{
			StreamID: streamID, UserID: keyUserID, TenantID: tenantID, Limit: limit,
		})
	} else {
		keyRows, err = queries.ListStreamKeysBackwardBefore(ctx, commodoredb.ListStreamKeysBackwardBeforeParams{
			StreamID: streamID, UserID: keyUserID, TenantID: tenantID,
			Column4: params.Cursor.Timestamp, Column5: params.Cursor.ID, Limit: limit,
		})
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	keys := make([]*commodorepb.StreamKey, 0, len(keyRows))
	for _, row := range keyRows {
		if !row.CreatedAt.Valid || !row.UpdatedAt.Valid {
			return nil, status.Error(codes.Internal, "database returned stream key without timestamps")
		}
		key := &commodorepb.StreamKey{
			Id: row.ID, TenantId: row.TenantID, UserId: row.UserID.String,
			StreamId: row.StreamID, KeyValue: row.KeyValue, KeyName: row.KeyName.String,
			IsActive: row.IsActive.Bool, CreatedAt: timestamppb.New(row.CreatedAt.Time),
			UpdatedAt: timestamppb.New(row.UpdatedAt.Time),
		}
		if row.LastUsedAt.Valid {
			key.LastUsedAt = timestamppb.New(row.LastUsedAt.Time)
		}
		keys = append(keys, key)
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
	resp := &commodorepb.ListStreamKeysResponse{
		StreamKeys: keys,
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

// DeactivateStreamKey deactivates a stream key
func (s *CommodoreServer) DeactivateStreamKey(ctx context.Context, req *commodorepb.DeactivateStreamKeyRequest) (*emptypb.Empty, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	queries := commodoredb.New(s.db)
	exists, err := queries.StreamExistsForUser(ctx, commodoredb.StreamExistsForUserParams{
		ID: req.GetStreamId(), UserID: userID, TenantID: tenantID,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !exists {
		return nil, status.Error(codes.NotFound, "stream not found")
	}

	rows, err := queries.DeactivateStreamKey(ctx, commodoredb.DeactivateStreamKeyParams{
		ID: req.GetKeyId(), StreamID: req.GetStreamId(),
		UserID: sql.NullString{String: userID, Valid: true}, TenantID: tenantID,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to deactivate key: %v", err)
	}

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

func (s *CommodoreServer) pushTargetResponse(
	id, streamID string,
	platform sql.NullString,
	name, targetURI string,
	isEnabled sql.NullBool,
	targetStatus, lastError sql.NullString,
	lastPushedAt, createdAt, updatedAt sql.NullTime,
) (*commodorepb.PushTarget, error) {
	if !createdAt.Valid || !updatedAt.Valid {
		return nil, fmt.Errorf("push target %s has NULL timestamps", id)
	}
	decryptedURI, err := s.fieldEncryptor.Decrypt(targetURI)
	if err != nil {
		s.logger.WithError(err).WithField("push_target_id", id).Warn("Failed to decrypt target_uri")
		decryptedURI = targetURI
	}
	target := &commodorepb.PushTarget{
		Id: id, StreamId: streamID, Platform: platform.String, Name: name,
		TargetUri: maskTargetURI(decryptedURI), IsEnabled: isEnabled.Bool,
		Status: targetStatus.String, CreatedAt: timestamppb.New(createdAt.Time),
		UpdatedAt: timestamppb.New(updatedAt.Time),
	}
	if lastError.Valid {
		target.LastError = lastError.String
	}
	if lastPushedAt.Valid {
		target.LastPushedAt = timestamppb.New(lastPushedAt.Time)
	}
	return target, nil
}

func (s *CommodoreServer) CreatePushTarget(ctx context.Context, req *commodorepb.CreatePushTargetRequest) (*commodorepb.PushTarget, error) {
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

	queries := commodoredb.New(s.db)
	exists, err := queries.StreamExistsForUser(ctx, commodoredb.StreamExistsForUserParams{
		ID: streamID, UserID: userID, TenantID: tenantID,
	})
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

	err = queries.InsertPushTarget(ctx, commodoredb.InsertPushTargetParams{
		ID: id, TenantID: tenantID, StreamID: streamID,
		Platform: sql.NullString{String: platform, Valid: true}, Name: req.GetName(),
		TargetUri: encryptedURI, CreatedAt: sql.NullTime{Time: now, Valid: true},
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create push target: %v", err)
	}
	s.emitStreamChangeEvent(ctx, eventStreamUpdated, tenantID, userID, streamID, []string{"push_targets"})

	return &commodorepb.PushTarget{
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

func (s *CommodoreServer) ListPushTargets(ctx context.Context, req *commodorepb.ListPushTargetsRequest) (*commodorepb.ListPushTargetsResponse, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	rows, err := commodoredb.New(s.db).ListPushTargets(ctx, commodoredb.ListPushTargetsParams{
		StreamID: streamID, TenantID: tenantID,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	targets := make([]*commodorepb.PushTarget, 0, len(rows))
	for _, row := range rows {
		target, mapErr := s.pushTargetResponse(
			row.ID, row.StreamID, row.Platform, row.Name, row.TargetUri, row.IsEnabled,
			row.Status, row.LastError, row.LastPushedAt, row.CreatedAt, row.UpdatedAt,
		)
		if mapErr != nil {
			return nil, status.Errorf(codes.Internal, "database error: %v", mapErr)
		}
		targets = append(targets, target)
	}

	return &commodorepb.ListPushTargetsResponse{PushTargets: targets}, nil
}

func (s *CommodoreServer) UpdatePushTarget(ctx context.Context, req *commodorepb.UpdatePushTargetRequest) (*commodorepb.PushTarget, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	id := req.GetId()
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "id required")
	}

	params := commodoredb.UpdatePushTargetFieldsParams{ID: id, TenantID: tenantID}
	if req.Name != nil {
		params.ApplyName = true
		params.Name = req.GetName()
	}
	if req.TargetUri != nil {
		if validationErr := validatePushTargetURI(req.GetTargetUri()); validationErr != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid target_uri: %v", validationErr)
		}
		encURI, encErr := s.fieldEncryptor.Encrypt(req.GetTargetUri())
		if encErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to encrypt target_uri: %v", encErr)
		}
		params.ApplyTargetUri = true
		params.TargetUri = encURI
	}
	if req.IsEnabled != nil {
		params.ApplyEnabled = true
		params.IsEnabled = req.GetIsEnabled()
	}

	row, err := commodoredb.New(s.db).UpdatePushTargetFields(ctx, params)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "push target not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	target, err := s.pushTargetResponse(
		row.ID, row.StreamID, row.Platform, row.Name, row.TargetUri, row.IsEnabled,
		row.Status, row.LastError, row.LastPushedAt, row.CreatedAt, row.UpdatedAt,
	)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	s.emitStreamChangeEvent(ctx, eventStreamUpdated, tenantID, userID, target.GetStreamId(), []string{"push_targets"})

	return target, nil
}

func (s *CommodoreServer) DeletePushTarget(ctx context.Context, req *commodorepb.DeletePushTargetRequest) (*commodorepb.DeletePushTargetResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	id := req.GetId()
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "id required")
	}

	streamID, err := commodoredb.New(s.db).DeletePushTarget(ctx, commodoredb.DeletePushTargetParams{
		ID: id, TenantID: tenantID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "push target not found")
		}
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	s.emitStreamChangeEvent(ctx, eventStreamUpdated, tenantID, userID, streamID, []string{"push_targets"})

	return &commodorepb.DeletePushTargetResponse{
		Message:   "Push target deleted",
		Id:        id,
		DeletedAt: timestamppb.Now(),
	}, nil
}

// GetStreamPushTargets is an internal RPC called by Foghorn when a stream goes live.
// Returns unmasked target URIs for Helmsman to push to.
func (s *CommodoreServer) GetStreamPushTargets(ctx context.Context, req *commodorepb.GetStreamPushTargetsRequest) (*commodorepb.GetStreamPushTargetsResponse, error) {
	streamID := req.GetStreamId()
	tenantID := req.GetTenantId()
	if streamID == "" || tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id and tenant_id required")
	}

	rows, err := commodoredb.New(s.db).ListEnabledPushTargets(ctx, commodoredb.ListEnabledPushTargetsParams{
		StreamID: streamID, TenantID: tenantID,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	targets := make([]*commodorepb.PushTargetInternal, 0, len(rows))
	for _, row := range rows {
		t := commodorepb.PushTargetInternal{
			Id: row.ID, Platform: row.Platform.String, Name: row.Name, TargetUri: row.TargetUri,
		}
		decrypted, decErr := s.fieldEncryptor.Decrypt(t.TargetUri)
		if decErr != nil {
			s.logger.WithError(decErr).WithField("push_target_id", t.Id).Warn("Failed to decrypt target_uri")
		} else {
			t.TargetUri = decrypted
		}
		targets = append(targets, &t)
	}

	return &commodorepb.GetStreamPushTargetsResponse{PushTargets: targets}, nil
}

// UpdatePushTargetStatus is an internal RPC called by Foghorn to update push target status
// based on PUSH_OUT_START / PUSH_END trigger events.
func (s *CommodoreServer) UpdatePushTargetStatus(ctx context.Context, req *commodorepb.UpdatePushTargetStatusRequest) (*commodorepb.PushTarget, error) {
	id := req.GetId()
	tenantID := req.GetTenantId()
	if id == "" || tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "id and tenant_id required")
	}

	params := commodoredb.UpdatePushTargetStatusParams{
		ID: id, TenantID: tenantID,
		Status:     sql.NullString{String: req.GetStatus(), Valid: true},
		MarkPushed: req.GetStatus() == "pushing",
	}
	if req.LastError != nil {
		params.ApplyLastError = true
		params.LastError = sql.NullString{String: req.GetLastError(), Valid: true}
	} else if req.GetStatus() != "failed" {
		params.ApplyLastError = true
	}

	row, err := commodoredb.New(s.db).UpdatePushTargetStatus(ctx, params)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "push target not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	target, err := s.pushTargetResponse(
		row.ID, row.StreamID, row.Platform, row.Name, row.TargetUri, row.IsEnabled,
		row.Status, row.LastError, row.LastPushedAt, row.CreatedAt, row.UpdatedAt,
	)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	return target, nil
}

// ============================================================================
// DEVELOPER SERVICE (Gateway → Commodore for API token management)
// ============================================================================

// CreateAPIToken creates a new API token
func (s *CommodoreServer) CreateAPIToken(ctx context.Context, req *commodorepb.CreateAPITokenRequest) (*commodorepb.CreateAPITokenResponse, error) {
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

	err = commodoredb.New(s.db).InsertAPIToken(ctx, commodoredb.InsertAPITokenParams{
		TokenID: tokenID, TenantID: tenantID, UserID: userID, TokenHash: tokenHash,
		TokenName: tokenName, Permissions: permissions, ExpiresAt: expiresAt,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create API token: %v", err)
	}

	s.emitAuthEvent(ctx, eventTokenCreated, userID, tenantID, "api_token", "", tokenID, "")

	resp := &commodorepb.CreateAPITokenResponse{
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
func (s *CommodoreServer) ListAPITokens(ctx context.Context, req *commodorepb.ListAPITokensRequest) (*commodorepb.ListAPITokensResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	queries := commodoredb.New(s.db)
	total, err := queries.CountAPITokensForUser(ctx, commodoredb.CountAPITokensForUserParams{UserID: userID, TenantID: tenantID})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	var tokens []*commodorepb.APITokenInfo
	appendToken := func(id, name string, permissions []string, tokenStatus string, lastUsedAt, expiresAt, createdAt sql.NullTime) error {
		if !createdAt.Valid {
			return fmt.Errorf("API token %s has NULL created_at", id)
		}
		token := &commodorepb.APITokenInfo{
			Id: id, TokenName: name, Permissions: permissions, Status: tokenStatus,
			CreatedAt: timestamppb.New(createdAt.Time),
		}
		if lastUsedAt.Valid {
			token.LastUsedAt = timestamppb.New(lastUsedAt.Time)
		}
		if expiresAt.Valid {
			token.ExpiresAt = timestamppb.New(expiresAt.Time)
		}
		tokens = append(tokens, token)
		return nil
	}
	rowLimit := int32(params.Limit + 1)
	if params.Direction == pagination.Forward {
		if params.Cursor == nil {
			rows, queryErr := queries.ListAPITokensForward(ctx, commodoredb.ListAPITokensForwardParams{UserID: userID, TenantID: tenantID, RowLimit: rowLimit})
			if queryErr != nil {
				return nil, status.Errorf(codes.Internal, "database error: %v", queryErr)
			}
			for _, row := range rows {
				if rowErr := appendToken(row.ID, row.TokenName, row.Permissions, row.Status, row.LastUsedAt, row.ExpiresAt, row.CreatedAt); rowErr != nil {
					return nil, status.Errorf(codes.Internal, "database error: %v", rowErr)
				}
			}
		} else {
			rows, queryErr := queries.ListAPITokensForwardAfter(ctx, commodoredb.ListAPITokensForwardAfterParams{
				UserID: userID, TenantID: tenantID, CursorTime: params.Cursor.Timestamp, CursorID: params.Cursor.ID, RowLimit: rowLimit,
			})
			if queryErr != nil {
				return nil, status.Errorf(codes.Internal, "database error: %v", queryErr)
			}
			for _, row := range rows {
				if rowErr := appendToken(row.ID, row.TokenName, row.Permissions, row.Status, row.LastUsedAt, row.ExpiresAt, row.CreatedAt); rowErr != nil {
					return nil, status.Errorf(codes.Internal, "database error: %v", rowErr)
				}
			}
		}
	} else if params.Cursor == nil {
		rows, queryErr := queries.ListAPITokensBackward(ctx, commodoredb.ListAPITokensBackwardParams{UserID: userID, TenantID: tenantID, RowLimit: rowLimit})
		if queryErr != nil {
			return nil, status.Errorf(codes.Internal, "database error: %v", queryErr)
		}
		for _, row := range rows {
			if rowErr := appendToken(row.ID, row.TokenName, row.Permissions, row.Status, row.LastUsedAt, row.ExpiresAt, row.CreatedAt); rowErr != nil {
				return nil, status.Errorf(codes.Internal, "database error: %v", rowErr)
			}
		}
	} else {
		rows, queryErr := queries.ListAPITokensBackwardBefore(ctx, commodoredb.ListAPITokensBackwardBeforeParams{
			UserID: userID, TenantID: tenantID, CursorTime: params.Cursor.Timestamp, CursorID: params.Cursor.ID, RowLimit: rowLimit,
		})
		if queryErr != nil {
			return nil, status.Errorf(codes.Internal, "database error: %v", queryErr)
		}
		for _, row := range rows {
			if rowErr := appendToken(row.ID, row.TokenName, row.Permissions, row.Status, row.LastUsedAt, row.ExpiresAt, row.CreatedAt); rowErr != nil {
				return nil, status.Errorf(codes.Internal, "database error: %v", rowErr)
			}
		}
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
	resp := &commodorepb.ListAPITokensResponse{
		Tokens: tokens,
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

// RevokeAPIToken revokes an API token
func (s *CommodoreServer) RevokeAPIToken(ctx context.Context, req *commodorepb.RevokeAPITokenRequest) (*commodorepb.RevokeAPITokenResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	tokenName, err := commodoredb.New(s.db).RevokeAPIToken(ctx, commodoredb.RevokeAPITokenParams{
		TokenID: req.GetTokenId(), UserID: userID, TenantID: tenantID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "token not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	s.emitAuthEvent(ctx, eventTokenRevoked, userID, tenantID, "api_token", "", req.GetTokenId(), "")

	return &commodorepb.RevokeAPITokenResponse{
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
	// Replays of a rotated refresh token within this window are treated as
	// concurrent-tab races (fresh tokens issued), not theft. Access tokens
	// are 15-minute bearer tokens, so this is not a weaker exposure.
	refreshTokenReuseGracePeriod = 10 * time.Minute

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
// to Decklog with exponential backoff, so a Decklog outage degrades to
// outbox-backlog growth rather than dropped stream/policy mutation events.
// For strict atomicity with a caller-held state-mutation tx, use
// EnqueueServiceEventTx(ctx, tx, event).
func (s *CommodoreServer) emitServiceEvent(ctx context.Context, event *ipcpb.ServiceEvent) {
	if event == nil {
		return
	}
	if ctxkeys.IsDemoMode(ctx) {
		return
	}
	s.enqueueServiceEvent(ctx, event)
}

func (s *CommodoreServer) emitAuthEvent(ctx context.Context, eventType, userID, tenantID, authType, walletID, tokenID, errMsg string) {
	payload := &ipcpb.AuthEvent{
		UserId:   userID,
		TenantId: tenantID,
		AuthType: authType,
		WalletId: walletID,
		TokenId:  tokenID,
		Error:    errMsg,
	}
	event := &ipcpb.ServiceEvent{
		EventType:    eventType,
		Timestamp:    timestamppb.Now(),
		Source:       "commodore",
		TenantId:     tenantID,
		UserId:       userID,
		ResourceType: "user",
		ResourceId:   userID,
		Payload:      &ipcpb.ServiceEvent_AuthEvent{AuthEvent: payload},
	}
	s.emitServiceEvent(ctx, event)
}

func (s *CommodoreServer) emitMistAdminSessionMintedEvent(ctx context.Context, userID, tenantID, nodeID, clusterID string) {
	payload := &ipcpb.AuthEvent{
		UserId:   userID,
		TenantId: tenantID,
		AuthType: "mist_admin_session",
	}
	event := &ipcpb.ServiceEvent{
		EventType:       eventMistAdminSessionMinted,
		Timestamp:       timestamppb.Now(),
		Source:          "commodore",
		TenantId:        tenantID,
		UserId:          userID,
		ResourceType:    "infrastructure_node",
		ResourceId:      nodeID,
		SourceClusterId: clusterID,
		Payload:         &ipcpb.ServiceEvent_AuthEvent{AuthEvent: payload},
	}
	s.emitServiceEvent(ctx, event)
}

// buildStreamChangeEvent constructs the stream service event; shared by the best-effort emitter and the
// transactional finalize enqueue so both produce an identical event.
func (s *CommodoreServer) buildStreamChangeEvent(eventType, tenantID, userID, streamID string, changedFields []string) *ipcpb.ServiceEvent {
	return &ipcpb.ServiceEvent{
		EventType:    eventType,
		Timestamp:    timestamppb.Now(),
		Source:       "commodore",
		TenantId:     tenantID,
		UserId:       userID,
		ResourceType: "stream",
		ResourceId:   streamID,
		Payload:      &ipcpb.ServiceEvent_StreamChangeEvent{StreamChangeEvent: &ipcpb.StreamChangeEvent{StreamId: streamID, ChangedFields: changedFields}},
	}
}

func (s *CommodoreServer) emitStreamChangeEvent(ctx context.Context, eventType, tenantID, userID, streamID string, changedFields []string) {
	s.emitServiceEvent(ctx, s.buildStreamChangeEvent(eventType, tenantID, userID, streamID, changedFields))
}

func (s *CommodoreServer) emitArtifactEvent(ctx context.Context, eventType, tenantID, userID string, artifactType ipcpb.ArtifactEvent_ArtifactType, artifactID, streamID, status string, expiresAt *int64) {
	if artifactID == "" || tenantID == "" {
		return
	}

	payload := &ipcpb.ArtifactEvent{
		ArtifactType: artifactType,
		ArtifactId:   artifactID,
		StreamId:     streamID,
		Status:       status,
	}
	if expiresAt != nil {
		payload.ExpiresAt = expiresAt
	}

	event := &ipcpb.ServiceEvent{
		EventType:    eventType,
		Timestamp:    timestamppb.Now(),
		Source:       "commodore",
		TenantId:     tenantID,
		UserId:       userID,
		ResourceType: "artifact",
		ResourceId:   artifactID,
		Payload:      &ipcpb.ServiceEvent_ArtifactEvent{ArtifactEvent: payload},
	}
	s.emitServiceEvent(ctx, event)
}

func (s *CommodoreServer) emitStreamKeyEvent(ctx context.Context, eventType, tenantID, userID, streamID, keyID string) {
	payload := &ipcpb.StreamKeyEvent{
		StreamId: streamID,
		KeyId:    keyID,
	}
	event := &ipcpb.ServiceEvent{
		EventType:    eventType,
		Timestamp:    timestamppb.Now(),
		Source:       "commodore",
		TenantId:     tenantID,
		UserId:       userID,
		ResourceType: "stream_key",
		ResourceId:   keyID,
		Payload:      &ipcpb.ServiceEvent_StreamKeyEvent{StreamKeyEvent: payload},
	}
	s.emitServiceEvent(ctx, event)
}

// GetStreamsBatch retrieves multiple streams by IDs in a single query
func (s *CommodoreServer) GetStreamsBatch(ctx context.Context, req *commodorepb.GetStreamsBatchRequest) (*commodorepb.GetStreamsBatchResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	streamIDs := req.GetStreamIds()
	if len(streamIDs) == 0 {
		return &commodorepb.GetStreamsBatchResponse{}, nil
	}

	rows, err := commodoredb.New(s.db).GetStreamsConfigBatch(ctx, commodoredb.GetStreamsConfigBatchParams{
		Column1: streamIDs, UserID: userID, TenantID: tenantID,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	streams := make([]*commodorepb.Stream, 0, len(rows))
	var route *clusterRoute
	routeAttempted := false
	for _, row := range rows {
		stream, err := s.streamFromConfigRow(row.Config())
		if err != nil {
			s.logger.WithError(err).Error("Error scanning stream in batch")
			return nil, status.Errorf(codes.Internal, "scan stream batch: %v", err)
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
	return &commodorepb.GetStreamsBatchResponse{Streams: streams}, nil
}

func (s *CommodoreServer) queryStream(ctx context.Context, streamID, userID, tenantID string) (*commodorepb.Stream, error) {
	row, err := commodoredb.New(s.db).GetStreamConfig(ctx, commodoredb.GetStreamConfigParams{
		ID: streamID, UserID: userID, TenantID: tenantID,
	})

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "stream not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	stream, err := s.streamFromConfigRow(row.Config())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "map stream: %v", err)
	}
	s.populateStreamOriginRegion(ctx, tenantID, stream, nil)
	return stream, nil
}

func (s *CommodoreServer) populateStreamOriginRegion(ctx context.Context, tenantID string, stream *commodorepb.Stream, route *clusterRoute) {
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
//
// active_ingest is authoritative for a live stream's thumbnail: Foghorn stores
// LIVE thumbnails on the INGEST cell (a local mint — they are ephemeral
// derivatives served while the stream is live, not durable media), so the
// object always lives on active_ingest's Chandler. There is no official-cluster
// resolution or remote drop for live, so a cross-cell-ingest stream still gets a
// working URL. (Artifacts differ: they persist durably on the tenant's official
// cluster and record thumbnail_serving_cluster_id for their URL.)
func (s *CommodoreServer) buildStreamThumbnailAssets(activeIngest sql.NullString, streamID string) *sharedpb.ThumbnailAssets {
	if s.clusterURLs == nil || !activeIngest.Valid || activeIngest.String == "" || streamID == "" {
		return nil
	}
	return s.clusterURLs.BuildThumbnailAssets(activeIngest.String, streamID)
}

// buildArtifactThumbnailAssets projects ThumbnailAssets for a clip/DVR/VOD
// artifact. Returns nil unless has_thumbnails is TRUE and a cluster is
// known. Caller supplies COALESCE(thumbnail_serving_cluster_id,
// storage_cluster_id, origin_cluster_id) — the authoritative serving cluster
// where the thumbnail bytes actually live, falling back to byte-storage/origin
// only for legacy rows not yet projected with the serving cluster.
func (s *CommodoreServer) buildArtifactThumbnailAssets(hasThumbnails bool, cluster sql.NullString, assetKey string) *sharedpb.ThumbnailAssets {
	if !hasThumbnails || s.clusterURLs == nil || !cluster.Valid || cluster.String == "" || assetKey == "" {
		return nil
	}
	return s.clusterURLs.BuildThumbnailAssets(cluster.String, assetKey)
}

func posterOnlyThumbnailAssets(assets *sharedpb.ThumbnailAssets) *sharedpb.ThumbnailAssets {
	if assets == nil || assets.GetPosterUrl() == "" {
		return nil
	}
	return &sharedpb.ThumbnailAssets{
		PosterUrl: assets.GetPosterUrl(),
		AssetKey:  assets.GetAssetKey(),
	}
}

// monitoringToggleFromNullBool maps the nullable commodore.streams.monitoring_
// enabled column to the wire enum: NULL -> INHERIT, true -> ON, false -> OFF.
func monitoringToggleFromNullBool(b sql.NullBool) commodorepb.MonitoringToggle {
	switch {
	case !b.Valid:
		return commodorepb.MonitoringToggle_MONITORING_TOGGLE_INHERIT
	case b.Bool:
		return commodorepb.MonitoringToggle_MONITORING_TOGGLE_ON
	default:
		return commodorepb.MonitoringToggle_MONITORING_TOGGLE_OFF
	}
}

// streamFromConfigRow maps the shared generated stream projection. Operational
// state still comes from the Periscope data plane.
func (s *CommodoreServer) streamFromConfigRow(row commodoredb.StreamConfigRow) (*commodorepb.Stream, error) {
	if !row.CreatedAt.Valid || !row.UpdatedAt.Valid {
		return nil, fmt.Errorf("stream %s has NULL timestamps", row.ID)
	}
	stream := &commodorepb.Stream{
		StreamId: row.ID, InternalName: row.InternalName, StreamKey: row.StreamKey,
		PlaybackId: row.PlaybackID, Title: row.Title,
		IsRecordingEnabled: row.IsRecordingEnabled.Bool,
		IsRecording:        row.IsRecordingEnabled.Bool,
		IngestMode:         row.IngestMode, Monitoring: monitoringToggleFromNullBool(row.MonitoringEnabled),
		CreatedAt: timestamppb.New(row.CreatedAt.Time), UpdatedAt: timestamppb.New(row.UpdatedAt.Time),
	}
	if row.Description.Valid {
		stream.Description = row.Description.String
	}
	if row.ActiveIngestClusterID.Valid {
		stream.ActiveIngestClusterId = row.ActiveIngestClusterID.String
	}
	if stream.IngestMode == "" {
		stream.IngestMode = "push"
	}
	if stream.IngestMode == "pull" && row.SourceURIEnc.Valid {
		sourceURI, err := s.pullSourceEncryptor.Decrypt(row.SourceURIEnc.String)
		if err != nil {
			return nil, err
		}
		class, classErr := pullsource.Classify(sourceURI)
		if classErr != nil {
			s.logger.WithError(classErr).WithField("stream_id", stream.StreamId).Debug("pull source classification failed")
		}
		stream.PullSource = buildPullSourceView(sourceURI, row.PullEnabled.Bool, class, row.AllowedClusterIDs)
	}

	stream.ThumbnailAssets = s.buildStreamThumbnailAssets(row.ActiveIngestClusterID, stream.StreamId)
	if row.DVRChapterMode.Valid {
		stream.DvrChapterMode = row.DVRChapterMode.String
	}
	if row.DVRChapterIntervalSeconds.Valid && row.DVRChapterIntervalSeconds.Int32 > 0 {
		v := row.DVRChapterIntervalSeconds.Int32
		stream.DvrChapterIntervalSeconds = &v
	}
	if row.DVRRetentionDaysOverride.Valid {
		v := row.DVRRetentionDaysOverride.Int32
		stream.DvrRetentionDaysOverride = &v
	}
	if row.ClipRetentionDaysOverride.Valid {
		v := row.ClipRetentionDaysOverride.Int32
		stream.ClipRetentionDaysOverride = &v
	}

	return stream, nil
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

	billingStatus, err := s.purserClient.GetTenantAdmissionStatus(ctx, tenantID)
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
	return commodoredb.New(s.db).ArtifactIdentifierExists(ctx, identifier)
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
func (s *CommodoreServer) CreateClip(ctx context.Context, req *sharedpb.CreateClipRequest) (*sharedpb.CreateClipResponse, error) {
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
	source, err := commodoredb.New(s.db).GetClipSourceStream(ctx, commodoredb.GetClipSourceStreamParams{
		ID: streamID, TenantID: tenantID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "stream not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Route to the cluster where the stream is ingesting (if known), else primary
	var foghornClient *foghornclient.GRPCClient
	var clipClusterID string
	if freshClusterID, ok := selectActiveIngestCluster(source.ActiveIngestClusterID, source.ActiveIngestClusterUpdatedAt, time.Now()); ok {
		foghornClient, err = s.resolveFoghornForCluster(ctx, freshClusterID, tenantID)
		if err == nil {
			clipClusterID = freshClusterID
		} else {
			s.logger.WithFields(logging.Fields{
				"tenant_id":                  tenantID,
				"stream_id":                  streamID,
				"active_ingest_cluster_id":   freshClusterID,
				"active_ingest_cluster_time": source.ActiveIngestClusterUpdatedAt.Time,
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
			"internal_name": source.InternalName,
			"error":         err,
		}).Error("Failed to generate artifact identifiers for clip")
		return nil, status.Errorf(codes.Internal, "failed to generate clip identifiers: %v", err)
	}

	// Resolve retention via the per-class cascade (per-stream override →
	// tenant per-class default → system default), clamped by the tier cap.
	// User-supplied expires_at is treated as a per-asset override and
	// clamped to the same cap.
	resolvedDays, retentionErr := s.resolveInitialRetention(ctx, commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_CLIP, tenantID, streamID)
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
	paramsJSON, err := json.Marshal(requestedParams)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshal clip params: %v", err)
	}

	// Build Foghorn request with pre-generated hash
	foghornReq := &sharedpb.CreateClipRequest{
		TenantId:           tenantID,
		StreamInternalName: source.InternalName,
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

	// Durable creation intent BEFORE the cross-service call. Keyed by the same
	// (tenant, kind, clip_hash) identity Foghorn dedups on, it captures everything
	// needed to finish the commodore.clips row (the fulfilled timing is fetched
	// from Foghorn on convergence). A crash or a lost/ambiguous response then
	// leaves a recoverable intent instead of a live Foghorn clip with no catalog
	// row — the sweep completes it (committed) or removes the catalog-only row (aborted).
	intentRequestID := uuid.New().String()
	var retentionUnix *int64
	if retentionUntil != nil {
		u := retentionUntil.Unix()
		retentionUnix = &u
	}
	var policyPtr *string
	if source.PlaybackPolicy != "" {
		v := source.PlaybackPolicy
		policyPtr = &v
	}
	var secretPtr *string
	if source.PlaybackWebhookSecretEnc.Valid {
		v := source.PlaybackWebhookSecretEnc.String
		secretPtr = &v
	}
	intentPayload := clipCreationPayload{
		ClipID:           clipID,
		UserID:           userID,
		StreamID:         streamID,
		InternalName:     artifactInternalName,
		PlaybackID:       playbackID,
		Title:            req.Title,
		Description:      req.Description,
		ClipMode:         req.Mode.String(),
		RequestedParams:  string(paramsJSON),
		OriginClusterID:  clipClusterID,
		RetentionUnixSec: retentionUnix,
		RequiresAuth:     source.RequiresAuth,
		PlaybackPolicy:   policyPtr,
		WebhookSecretEnc: secretPtr,
	}
	persistedRequestID, intentErr := upsertCreationIntent(ctx, s.db, tenantID, creationIntentKindClip, clipHash, intentRequestID, clipClusterID, intentPayload)
	if intentErr != nil {
		return nil, status.Errorf(codes.Internal, "failed to record clip creation intent: %v", intentErr)
	}
	// A retry of the same (tenant, clip) reuses the intent's ALREADY-PERSISTED
	// request_id; keying Foghorn on a freshly minted one would mismatch the ledger.
	intentRequestID = persistedRequestID

	// Carry the intent request_id so Foghorn keys its command ledger on it — the
	// sweep resolves this attempt's outcome by request_id, not by artifact presence.
	foghornReq.RequestId = &intentRequestID

	// Call Foghorn for artifact lifecycle management.
	resp, trailers, err := foghornClient.CreateClip(ctx, foghornReq)
	if err != nil {
		// An RPC error does NOT prove rejection: Foghorn may have committed the
		// clip before the response was lost. Only a DEFINITIVE rejection (an
		// application-level negative response) may abort now; an ambiguous error
		// leaves the intent PENDING for the sweep. Never compensate-delete here —
		// deleting a clip Foghorn committed is exactly the phantom this avoids.
		if creationCreateErrorIsDefinitive(err) {
			if abErr := s.abortCreationIntent(ctx,
				creationIntentRow{tenantID: tenantID, kind: creationIntentKindClip, artifactHash: clipHash, originClusterID: clipClusterID},
				"", "foghorn rejected clip create", true); abErr != nil && !errors.Is(abErr, errIntentCASMiss) {
				s.logger.WithError(abErr).WithField("clip_hash", clipHash).Warn("Failed to abort clip creation intent")
			}
		}
		s.logger.WithError(err).WithField("clip_hash", clipHash).Error("Failed to create clip artifact via Foghorn")
		return nil, grpcutil.PropagateError(ctx, err, trailers)
	}

	// Foghorn durably holds the clip. Register commodore.clips with the fulfilled
	// range Foghorn harvested (the single authoritative source of
	// start_time/duration). On a local write failure or a missing range we do NOT
	// fail the create or delete the Foghorn artifact: the intent stays pending and
	// the convergence sweep completes the catalog row from Foghorn's authoritative
	// timing.
	startTime, duration, haveTiming := fulfilledClipTiming(resp)
	if !haveTiming {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"clip_hash": clipHash,
		}).Warn("Foghorn clip commit returned no fulfilled timing; catalog row deferred to convergence sweep")
		return resp, nil
	}
	// Write the clips row and terminalize the intent through the shared terminalizer,
	// under the same per-artifact advisory lock + deletion-marker check the sweep and
	// delete paths use (so this create cannot insert a live row behind a deletion
	// marker). On any failure the intent stays pending and the sweep completes it.
	clipRow := creationIntentRow{tenantID: tenantID, kind: creationIntentKindClip, artifactHash: clipHash, originClusterID: clipClusterID}
	if cErr := s.commitCreationIntent(ctx, clipRow, "", clipCatalogRowMutator(clipRow, intentPayload, startTime, duration)); cErr != nil && !errors.Is(cErr, errIntentCASMiss) {
		s.logger.WithError(cErr).WithField("clip_hash", clipHash).Warn("Clip registry write failed after Foghorn commit; left to convergence sweep")
		return resp, nil
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":     tenantID,
		"clip_hash":     clipHash,
		"clip_id":       clipID,
		"internal_name": source.InternalName,
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
func fulfilledClipTiming(resp *sharedpb.CreateClipResponse) (startTimeMs, durationMs int64, ok bool) {
	startTimeMs, durationMs = resp.GetEffectiveStartMs(), resp.GetEffectiveDurationMs()
	return startTimeMs, durationMs, startTimeMs > 0 && durationMs > 0
}

// GetClip returns clip business registry metadata (no lifecycle data).
// Lifecycle/access data must come from Periscope (data plane).
func (s *CommodoreServer) GetClip(ctx context.Context, req *sharedpb.GetClipRequest) (*sharedpb.ClipInfo, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	clipHash := req.GetClipHash()
	if clipHash == "" {
		return nil, status.Error(codes.InvalidArgument, "clip_hash is required")
	}

	row, err := commodoredb.New(s.db).GetClipRegistry(ctx, commodoredb.GetClipRegistryParams{
		TenantID: tenantID, ClipHash: clipHash,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "clip not found")
	} else if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	if !row.CreatedAt.Valid || !row.UpdatedAt.Valid {
		return nil, status.Error(codes.Internal, "database returned clip without timestamps")
	}
	clip := &sharedpb.ClipInfo{
		Id:         row.ID,
		ClipHash:   row.ClipHash,
		PlaybackId: row.PlaybackID,
		StreamId:   row.CStreamID,
		StartTime:  row.StartTime / 1000, // Convert ms to seconds
		Duration:   row.Duration / 1000,  // Convert ms to seconds
		Status:     "registry",
		CreatedAt:  timestamppb.New(row.CreatedAt.Time),
		UpdatedAt:  timestamppb.New(row.UpdatedAt.Time),
	}
	if row.Title.Valid {
		clip.Title = row.Title.String
	}
	if row.Description.Valid {
		clip.Description = row.Description.String
	}
	if row.ClipMode.Valid {
		clip.ClipMode = &row.ClipMode.String
	}
	if row.RequestedParams != "" {
		requestedParams := row.RequestedParams
		clip.RequestedParams = &requestedParams
	}
	if row.RetentionSource != "" {
		src := row.RetentionSource
		clip.RetentionSource = &src
	}
	if row.SizeBytes.Valid {
		size := row.SizeBytes.Int64
		clip.SizeBytes = &size
	}
	if row.RetentionUntil.Valid {
		expiresAt := timestamppb.New(row.RetentionUntil.Time)
		clip.ExpiresAt = expiresAt
	}
	thumbnailCluster := sql.NullString{String: row.ThumbnailCluster, Valid: row.ThumbnailCluster != ""}
	clip.ThumbnailAssets = s.buildArtifactThumbnailAssets(row.HasThumbnails, thumbnailCluster, clipHash)

	return clip, nil
}

// DeleteClip proxies clip deletion to Foghorn
func (s *CommodoreServer) DeleteClip(ctx context.Context, req *sharedpb.DeleteClipRequest) (*sharedpb.DeleteClipResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Look up clip info for deletion event and cluster-aware routing
	route, routeErr := commodoredb.New(s.db).GetClipDeletionRoute(ctx, commodoredb.GetClipDeletionRouteParams{
		ClipHash: req.ClipHash, TenantID: tenantID,
	})
	if routeErr != nil && !errors.Is(routeErr, sql.ErrNoRows) {
		s.logger.WithError(routeErr).WithField("clip_hash", req.ClipHash).Debug("Clip deletion route lookup failed; falling back to tenant route")
	}

	foghornClient, err := s.resolveFoghornForArtifact(ctx, tenantID, route.OriginClusterID.String)
	if err != nil {
		return nil, err
	}

	resp, trailers, err := foghornClient.DeleteClip(ctx, req.ClipHash, &tenantID)
	if err != nil {
		s.logger.WithError(err).Error("Failed to delete clip via Foghorn")
		return nil, grpcutil.PropagateError(ctx, err, trailers)
	}

	// The catalog deletion is projected by the Foghorn artifact reconciler, the sole revision authority:
	// Foghorn soft-deletes its media-plane row here (bumping catalog_revision), and the reconciler writes
	// the durable tombstone marker at that authoritative revision. Commodore performs no local catalog
	// mutation, so no non-authoritative revision can beat a stalled snapshot and resurrect the asset.
	if resp.Success {
		s.emitArtifactEvent(ctx, eventArtifactDeleted, tenantID, userID, ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP, req.ClipHash, route.StreamID, "deleted", nil)
	}

	return resp, nil
}

// ============================================================================
// DVR SERVICE (Commodore → Foghorn proxy)
// ============================================================================

// StopDVR proxies DVR stop to Foghorn
func (s *CommodoreServer) StopDVR(ctx context.Context, req *sharedpb.StopDVRRequest) (*sharedpb.StopDVRResponse, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	route, routeErr := commodoredb.New(s.db).GetDVRDeletionRoute(ctx, commodoredb.GetDVRDeletionRouteParams{
		DvrHash: req.DvrHash, TenantID: tenantID,
	})
	var streamID *string
	if routeErr == nil && route.StreamID != "" {
		streamID = &route.StreamID
	}

	foghornClient, err := s.resolveFoghornForArtifact(ctx, tenantID, route.OriginClusterID.String)
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
func (s *CommodoreServer) DeleteDVR(ctx context.Context, req *sharedpb.DeleteDVRRequest) (*sharedpb.DeleteDVRResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Look up DVR info for deletion event and cluster-aware routing
	route, routeErr := commodoredb.New(s.db).GetDVRDeletionRoute(ctx, commodoredb.GetDVRDeletionRouteParams{
		DvrHash: req.DvrHash, TenantID: tenantID,
	})
	if routeErr != nil && !errors.Is(routeErr, sql.ErrNoRows) {
		s.logger.WithError(routeErr).WithField("dvr_hash", req.DvrHash).Debug("DVR deletion route lookup failed; falling back to tenant route")
	}

	foghornClient, err := s.resolveFoghornForArtifact(ctx, tenantID, route.OriginClusterID.String)
	if err != nil {
		return nil, err
	}

	resp, trailers, err := foghornClient.DeleteDVR(ctx, req.DvrHash, &tenantID)
	if err != nil {
		s.logger.WithError(err).Error("Failed to delete DVR via Foghorn")
		return nil, grpcutil.PropagateError(ctx, err, trailers)
	}

	// The catalog deletion of the parent DVR AND its chapters is projected by the Foghorn artifact
	// reconciler, the sole revision authority: Foghorn soft-deletes the parent and cascades its child
	// chapter artifacts here (bumping each row's catalog_revision), and the reconciler writes a durable
	// tombstone marker for each at its authoritative revision — removing the business row and the
	// dvr_chapter_playback mapping in the same projection. Commodore performs no local catalog mutation,
	// so no non-authoritative revision can resurrect a deleted asset.
	if resp.Success {
		s.emitArtifactEvent(ctx, eventArtifactDeleted, tenantID, userID, ipcpb.ArtifactEvent_ARTIFACT_TYPE_DVR, req.DvrHash, route.StreamID, "deleted", nil)
	}

	return resp, nil
}

// ResolveViewerEndpoint proxies viewer endpoint resolution to Foghorn
// and enriches the response with stream metadata from Commodore's database
func (s *CommodoreServer) ResolveViewerEndpoint(ctx context.Context, req *sharedpb.ViewerEndpointRequest) (*sharedpb.ViewerEndpointResponse, error) {
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
		for _, key := range []string{"x-payment", "payment-signature"} {
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
		tenantID := resp.Metadata.GetTenantId()
		// Enrichment is tenant-scoped only. If Foghorn didn't resolve a tenant we
		// skip rather than run an unscoped stream lookup — no cross-tenant read path
		// (mirrors finalizeIngestResponse).
		if streamID != "" && tenantID != "" {
			metadataRow, err := commodoredb.New(s.db).GetStreamDisplayMetadata(ctx, commodoredb.GetStreamDisplayMetadataParams{
				ID: streamID, TenantID: tenantID,
			})
			if err == nil {
				if metadataRow.Title != "" {
					resp.Metadata.Title = &metadataRow.Title
				}
				if metadataRow.Description.Valid && metadataRow.Description.String != "" {
					resp.Metadata.Description = &metadataRow.Description.String
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
	playbackID, err := commodoredb.New(s.db).NormalizeArtifactPlaybackID(ctx, contentID)
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
func (s *CommodoreServer) ResolveIngestEndpoint(ctx context.Context, req *sharedpb.IngestEndpointRequest) (*sharedpb.IngestEndpointResponse, error) {
	tenantID := ctxkeys.GetTenantID(ctx)

	// Routing always follows the stream key, never the caller.
	//
	// The key identifies the stream, so it is the only input that can honour the
	// active ingest lease and reach the stream owner's placement. Routing by the
	// caller's tenant instead — which an optional JWT makes the common SDK case —
	// would hand an owner an endpoint on their primary cluster while the lease
	// sits elsewhere, and PUSH_REWRITE would then reject the publish as a
	// duplicate; for a non-owner it would resolve against the wrong tenant
	// entirely. Authentication decides what metadata comes back, in
	// finalizeIngestResponse below, and nothing else.
	foghornClient, _, err := s.resolveFoghornForStreamKey(ctx, req.StreamKey)
	if err != nil {
		return nil, err
	}

	resp, trailers, err := foghornClient.ResolveIngestEndpoint(ctx, req.StreamKey, req.ViewerIp)
	if err != nil {
		s.logger.WithError(err).Error("Failed to resolve ingest endpoint from Foghorn")
		return nil, grpcutil.PropagateError(ctx, err, trailers)
	}

	s.finalizeIngestResponse(ctx, tenantID, resp)

	return resp, nil
}

// finalizeIngestResponse enriches ingest metadata with tenant-scoped stream info
// and, for any non-owner resolve (anonymous, or an authenticated caller whose
// tenant does not match the stream's), strips fields a bare key-holder must not
// receive. Extracted from ResolveIngestEndpoint so the trust boundary is
// unit-testable without a live Foghorn.
func (s *CommodoreServer) finalizeIngestResponse(ctx context.Context, callerTenantID string, resp *sharedpb.IngestEndpointResponse) {
	if resp == nil || resp.Metadata == nil {
		return
	}
	md := resp.Metadata

	// Ownership is tenant match, not merely "is authenticated": resolveIngestEndpoint
	// is an allowlisted query that accepts an optional JWT, so an authenticated caller
	// from another tenant must NOT be treated as the owner.
	isOwner := callerTenantID != "" && callerTenantID == md.TenantId

	// Title/description are authoritative from Commodore's own tenant-scoped lookup
	// below — never trusted from the upstream (Foghorn) response. Clear any echoed
	// values first so a non-owner cannot retain metadata we did not derive under
	// their own tenant scope.
	md.Title = nil
	md.Description = nil

	// Enrich metadata with stream info from Commodore's database (best-effort).
	// Scope to the CALLER's tenant when authenticated, so a signed-in non-owner
	// cannot read another tenant's stream row; anonymous callers scope to the
	// tenant Foghorn resolved from the key. If neither is known, skip — never an
	// unscoped query.
	if md.StreamId != "" {
		scopeTenant := callerTenantID
		if scopeTenant == "" {
			scopeTenant = md.TenantId
		}
		if scopeTenant != "" {
			metadataRow, err := commodoredb.New(s.db).GetStreamDisplayMetadata(ctx, commodoredb.GetStreamDisplayMetadataParams{
				ID: md.StreamId, TenantID: scopeTenant,
			})
			if err == nil {
				if metadataRow.Title != "" {
					md.Title = &metadataRow.Title
				}
				if metadataRow.Description.Valid && metadataRow.Description.String != "" {
					md.Description = &metadataRow.Description.String
				}
			}
			// Silently ignore errors - enrichment is best-effort
		}
	}

	// The echoed stream key and owning tenant are returned ONLY to the owning
	// tenant. An anonymous caller or an authenticated caller whose tenant does not
	// match the resolved stream's tenant is not the owner and receives neither — a
	// bare key-holder already has the key and must not learn tenancy.
	if !isOwner {
		stripSensitiveIngestMetadata(md)
	}
}

// stripSensitiveIngestMetadata clears fields a non-owner ingest resolve must not
// return: the echoed stream key and the owning tenant ID. Callers decide who is a
// non-owner (anonymous, or an authenticated caller whose tenant does not match).
func stripSensitiveIngestMetadata(md *sharedpb.IngestMetadata) {
	if md == nil {
		return
	}
	md.StreamKey = ""
	md.TenantId = ""
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
func (s *CommodoreServer) SetNodeOperationalMode(ctx context.Context, req *foghorncontrolpb.SetNodeModeRequest) (*foghorncontrolpb.SetNodeModeResponse, error) {
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
func (s *CommodoreServer) GetNodeHealth(ctx context.Context, req *foghorncontrolpb.GetNodeHealthRequest) (*foghorncontrolpb.GetNodeHealthResponse, error) {
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

	// Drain commodore.stream_cleanup_outbox: durably deliver a deleted stream's thumbnail-cleanup obligation to
	// Foghorn, retried until acked, so a live stream's thumbnails never leak on a best-effort delivery failure.
	go commodoreServer.runStreamCleanupOutboxWorker(context.Background())

	// Drain commodore.service_event_outbox to Decklog: a Decklog outage degrades to
	// outbox-backlog growth rather than dropped events.
	go commodoreServer.runServiceEventOutboxWorker(context.Background())

	// Drain pending artifact creation intents. Converges every create whose
	// cross-service Foghorn response was lost or ambiguous to a durable terminal
	// outcome — a live catalog row (Foghorn committed) or a clean row absence
	// (Foghorn rejected; no tombstone, since an aborted create never had a Foghorn
	// revision) — so no Clip/VOD/DVR creation strands one plane.
	go commodoreServer.runCreationIntentSweep(context.Background())

	// Register all services
	commodorepb.RegisterInternalServiceServer(server, commodoreServer)
	commodorepb.RegisterUserServiceServer(server, commodoreServer)
	commodorepb.RegisterStreamServiceServer(server, commodoreServer)
	commodorepb.RegisterStreamKeyServiceServer(server, commodoreServer)
	commodorepb.RegisterDeveloperServiceServer(server, commodoreServer)
	// ClipService, DVRService, ViewerService, and VodService proxy to Foghorn via gRPC
	commodorepb.RegisterClipServiceServer(server, commodoreServer)
	commodorepb.RegisterDVRServiceServer(server, commodoreServer)
	commodorepb.RegisterViewerServiceServer(server, commodoreServer)
	commodorepb.RegisterVodServiceServer(server, commodoreServer)
	commodorepb.RegisterNodeManagementServiceServer(server, commodoreServer)
	commodorepb.RegisterPushTargetServiceServer(server, commodoreServer)
	commodorepb.RegisterPlaybackAccessControlServiceServer(server, commodoreServer)

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
func (s *CommodoreServer) CreateVodUpload(ctx context.Context, req *sharedpb.CreateVodUploadRequest) (*sharedpb.CreateVodUploadResponse, error) {
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
	resolvedDays, retentionErr := s.resolveInitialRetention(ctx, commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_VOD, tenantID, "")
	if retentionErr != nil {
		s.logger.WithError(retentionErr).WithField("tenant_id", tenantID).Warn("VOD retention resolution failed; falling back to 30-day horizon for safety")
		resolvedDays = 30
	}
	var retentionUntil sql.NullTime
	if resolvedDays > 0 {
		retentionUntil = sql.NullTime{Valid: true, Time: time.Now().UTC().Add(time.Duration(resolvedDays) * 24 * time.Hour)}
	}

	// Register the vod_assets business row AND its durable creation intent in ONE
	// transaction, BEFORE the Foghorn call. Writing them together closes the
	// crash-between-them window that would otherwise leave a catalog-only phantom
	// (a vod_assets row with no intent to reconcile it). The intent guards the row:
	// an ambiguous Foghorn error must NOT drop it (Foghorn may have committed the
	// artifact + multipart + outbox before the response was lost); only the sweep
	// (or a definitive rejection here) removes it.
	intentRequestID := uuid.New().String()
	regErr := fwdb.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if execErr := commodoredb.New(tx).InsertVODUploadRegistration(ctx, commodoredb.InsertVODUploadRegistrationParams{
			ID:              vodID,
			TenantID:        tenantID,
			UserID:          userID,
			VodHash:         vodHash,
			InternalName:    artifactInternalName,
			PlaybackID:      playbackID,
			Title:           sql.NullString{String: req.GetTitle(), Valid: true},
			Description:     sql.NullString{String: req.GetDescription(), Valid: true},
			Filename:        req.Filename,
			ContentType:     sql.NullString{String: req.GetContentType(), Valid: true},
			SizeBytes:       sql.NullInt64{Int64: req.SizeBytes, Valid: true},
			OriginClusterID: sql.NullString{String: vodRoute.clusterID, Valid: true},
			RetentionUntil:  retentionUntil,
		}); execErr != nil {
			return execErr
		}
		// A retry of the same (tenant, vod) reuses the intent's ALREADY-PERSISTED
		// request_id; keying Foghorn on a freshly minted one would mismatch the ledger.
		persisted, upErr := upsertCreationIntent(ctx, tx, tenantID, creationIntentKindVOD, vodHash, intentRequestID, vodRoute.clusterID, nil)
		if upErr != nil {
			return upErr
		}
		intentRequestID = persisted
		return nil
	})
	if regErr != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"filename":  req.Filename,
			"error":     regErr,
		}).Error("Failed to register VOD asset + creation intent")
		return nil, status.Errorf(codes.Internal, "failed to register VOD asset: %v", regErr)
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id": tenantID,
		"vod_hash":  vodHash,
		"vod_id":    vodID,
		"filename":  req.Filename,
	}).Info("Registered VOD asset in business registry")

	// Build Foghorn request with pre-generated hash. request_id keys Foghorn's
	// command ledger so the sweep resolves this attempt by request_id.
	foghornReq := &sharedpb.CreateVodUploadRequest{
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
		RequestId:     &intentRequestID,
	}

	// Call Foghorn for S3 multipart upload setup
	resp, trailers, err := foghornClient.CreateVodUpload(ctx, foghornReq)
	if err != nil {
		s.logger.WithError(err).WithField("vod_hash", vodHash).Error("Failed to create VOD upload via Foghorn")
		// An RPC error does NOT prove rejection. Only a DEFINITIVE rejection aborts
		// now (proven not-created): the shared terminalizer removes the catalog-only
		// row and aborts the intent atomically. An ambiguous error leaves both the row
		// and the pending intent so the sweep can converge once Foghorn's actual state
		// is known. No tombstone is written — an aborted create never had a Foghorn
		// artifact/revision.
		if creationCreateErrorIsDefinitive(err) {
			if abErr := s.abortCreationIntent(context.Background(),
				creationIntentRow{tenantID: tenantID, kind: creationIntentKindVOD, artifactHash: vodHash, originClusterID: vodRoute.clusterID},
				"", "foghorn rejected vod upload create", true); abErr != nil && !errors.Is(abErr, errIntentCASMiss) {
				s.logger.WithError(abErr).WithField("vod_hash", vodHash).Warn("Failed to abort VOD creation intent")
			}
		}
		return nil, grpcutil.PropagateError(ctx, err, trailers)
	}

	if cErr := s.commitCreationIntent(ctx,
		creationIntentRow{tenantID: tenantID, kind: creationIntentKindVOD, artifactHash: vodHash, originClusterID: vodRoute.clusterID},
		"", nil); cErr != nil && !errors.Is(cErr, errIntentCASMiss) {
		s.logger.WithError(cErr).WithField("vod_hash", vodHash).Warn("Failed to mark VOD creation intent committed")
	}

	if resp != nil && resp.PlaybackId == "" {
		resp.PlaybackId = playbackID
	}
	return resp, nil
}

// CompleteVodUpload finalizes multipart upload via Foghorn
func (s *CommodoreServer) CompleteVodUpload(ctx context.Context, req *sharedpb.CompleteVodUploadRequest) (*sharedpb.CompleteVodUploadResponse, error) {
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
	foghornReq := &sharedpb.CompleteVodUploadRequest{
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
func (s *CommodoreServer) GetVodUploadStatus(ctx context.Context, req *sharedpb.GetVodUploadStatusRequest) (*sharedpb.GetVodUploadStatusResponse, error) {
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

	playbackID, err := commodoredb.New(s.db).GetVODPlaybackID(ctx, commodoredb.GetVODPlaybackIDParams{
		TenantID: tenantID, VodHash: resp.ArtifactHash,
	})
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
func (s *CommodoreServer) AbortVodUpload(ctx context.Context, req *sharedpb.AbortVodUploadRequest) (*sharedpb.AbortVodUploadResponse, error) {
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

	// No business-row cleanup here: Foghorn's abort deletes the artifact and the reconciler
	// projects that deletion through UpdateArtifactCatalogSnapshot (deleted=true), which removes
	// the catalog row and writes the tombstone marker. Deleting it here would race that projection.

	s.logger.WithFields(logging.Fields{
		"tenant_id": tenantID,
		"upload_id": req.UploadId,
	}).Info("Aborted VOD upload")

	return resp, nil
}

// ListStorageArtifacts returns the account storage browser's canonical
// registry projection. This is intentionally served from Commodore instead of
// Bridge joining one page each of clips/DVR/VOD, so search, sorting, and
// pagination run against the full tenant dataset.
// maxBatchArtifactHashes bounds a batch exact-hash lookup so a pathological caller can't ask for
// an unbounded ANY() list. Top Assets enrichment is at most a page of ranked assets.
const maxBatchArtifactHashes = 500

// cleanArtifactHashes trims, drops empty entries, and de-duplicates a batch hash filter
// (preserving first-seen order). An all-whitespace input returns an empty slice; the caller must
// treat "batch requested but nothing valid" as match-nothing, NOT as an unfiltered query. The
// caller rejects an oversized request BEFORE calling this (no silent truncation).
func cleanArtifactHashes(hashes []string) []string {
	if len(hashes) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(hashes))
	out := make([]string, 0, len(hashes))
	for _, h := range hashes {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	return out
}

func (s *CommodoreServer) ListStorageArtifacts(ctx context.Context, req *commodorepb.ListStorageArtifactsRequest) (*commodorepb.ListStorageArtifactsResponse, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetTenantId() != "" && req.GetTenantId() != tenantID {
		return nil, status.Error(codes.PermissionDenied, "tenant mismatch")
	}
	// Reject an oversized batch outright rather than silently truncating it (which would return a
	// plausible-but-partial result the caller can't distinguish from a complete one).
	if len(req.GetArtifactHashes()) > maxBatchArtifactHashes {
		return nil, status.Errorf(codes.InvalidArgument, "artifact_hashes batch too large (%d > %d)", len(req.GetArtifactHashes()), maxBatchArtifactHashes)
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

	// status is a closed set (the derived lifecycle status). Reject unknown values rather than
	// silently returning an empty catalog for a typo.
	switch strings.TrimSpace(req.GetStatus()) {
	case "", "ready", "failed", "processing", "expired":
	default:
		return nil, status.Errorf(codes.InvalidArgument, "unknown status filter %q (want ready|failed|processing|expired)", req.GetStatus())
	}

	normalizedKinds := make([]string, 0, len(req.GetKinds()))
	for _, kind := range req.GetKinds() {
		normalized := strings.ToLower(strings.TrimSpace(kind))
		switch normalized {
		case "vod", "dvr", "chapter", "clip":
			normalizedKinds = append(normalizedKinds, normalized)
		default:
			return nil, status.Errorf(codes.InvalidArgument, "unknown kind filter %q (want vod|dvr|chapter|clip)", kind)
		}
	}
	var artifactHashes []string
	if len(req.GetArtifactHashes()) > 0 {
		artifactHashes = cleanArtifactHashes(req.GetArtifactHashes())
		if len(artifactHashes) > 0 {
			limit = len(artifactHashes)
		}
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
	catalog, err := commodoredb.New(s.db).ListStorageArtifactCatalog(ctx, commodoredb.StorageArtifactFilter{
		TenantID:       tenantID,
		StreamID:       req.GetStreamId(),
		Status:         strings.TrimSpace(req.GetStatus()),
		ArtifactHash:   strings.TrimSpace(req.GetArtifactHash()),
		ArtifactHashes: artifactHashes,
		Search:         strings.TrimSpace(req.GetSearch()),
		Kinds:          normalizedKinds,
		SortField:      sortField,
		SortDirection:  sortDirection,
		Limit:          limit,
		Offset:         offset,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "storage artifact catalog query: %v", err)
	}

	artifacts := make([]*commodorepb.StorageArtifactInfo, 0, len(catalog.Rows))
	for _, row := range catalog.Rows {
		artifact := &commodorepb.StorageArtifactInfo{
			Kind:             row.Kind,
			Id:               row.ID,
			ArtifactHash:     row.ArtifactHash,
			StreamTitle:      row.StreamTitle,
			Title:            row.Title,
			SecondaryLabel:   row.SecondaryLabel,
			Status:           row.Status.String,
			CreatedAt:        timestamppb.New(row.CreatedAt),
			UpdatedAt:        timestamppb.New(row.UpdatedAt),
			StorageClusterId: row.StorageClusterID,
			HasThumbnails:    row.HasThumbnails,
		}
		if row.PlaybackID != "" {
			artifact.PlaybackId = &row.PlaybackID
		}
		if row.StreamID != "" {
			artifact.StreamId = &row.StreamID
		}
		if row.SizeBytes.Valid {
			value := row.SizeBytes.Int64
			artifact.SizeBytes = &value
		}
		if row.StorageLocation.Valid && row.StorageLocation.String != "" {
			value := row.StorageLocation.String
			artifact.StorageLocation = &value
		}
		if row.ExpiresAt.Valid {
			artifact.ExpiresAt = timestamppb.New(row.ExpiresAt.Time)
		}
		if row.RetentionSource != "" {
			artifact.RetentionSource = &row.RetentionSource
		}
		if row.OriginType != "" {
			artifact.OriginType = &row.OriginType
		}
		if row.OriginID != "" {
			artifact.OriginId = &row.OriginID
		}
		if row.Description != "" {
			artifact.Description = &row.Description
		}
		if row.ErrorMessage != "" {
			artifact.ErrorMessage = &row.ErrorMessage
		}
		if row.DurationMs.Valid {
			value := row.DurationMs.Int64
			artifact.DurationMs = &value
		}
		if row.Tracks.Valid && row.Tracks.String != "" {
			parsed, trackErr := commodoreclient.UnmarshalMediaTracks([]byte(row.Tracks.String))
			if trackErr != nil {
				return nil, status.Errorf(codes.Internal, "decode tracks for %s: %v", row.ArtifactHash, trackErr)
			}
			artifact.Tracks = parsed
		}
		if row.SyncStatus.Valid && row.SyncStatus.String != "" {
			value := row.SyncStatus.String
			artifact.SyncStatus = &value
		}
		if row.IsSynced.Valid {
			value := row.IsSynced.Bool
			artifact.IsSynced = &value
		}
		if row.IsFinalized.Valid {
			value := row.IsFinalized.Bool
			artifact.IsFinalized = &value
		}
		artifact.ThumbnailAssets = s.buildArtifactThumbnailAssets(
			row.HasThumbnails,
			sql.NullString{String: row.ThumbnailServingCluster, Valid: row.ThumbnailServingCluster != ""},
			row.ArtifactHash,
		)
		artifacts = append(artifacts, artifact)
	}
	total := catalog.Total
	kindCounts := catalog.KindCounts
	hasNext := len(artifacts) > limit
	if hasNext {
		artifacts = artifacts[:limit]
	}

	return &commodorepb.ListStorageArtifactsResponse{
		Artifacts:   artifacts,
		TotalCount:  total,
		HasNextPage: hasNext,
		KindCounts:  kindCounts,
	}, nil
}

// DeleteVodAsset deletes a VOD asset via Foghorn
func (s *CommodoreServer) DeleteVodAsset(ctx context.Context, req *sharedpb.DeleteVodAssetRequest) (*sharedpb.DeleteVodAssetResponse, error) {
	// Get tenant context from metadata
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Look up origin cluster for cluster-aware routing
	originClusterID, routeErr := commodoredb.New(s.db).GetVODOriginCluster(ctx, commodoredb.GetVODOriginClusterParams{
		VodHash: req.ArtifactHash, TenantID: tenantID,
	})
	if routeErr != nil && !errors.Is(routeErr, sql.ErrNoRows) {
		s.logger.WithError(routeErr).WithField("artifact_hash", req.ArtifactHash).Debug("VOD deletion route lookup failed; falling back to tenant route")
	}

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

	// The catalog deletion is projected by the Foghorn artifact reconciler, the sole revision authority:
	// Foghorn soft-deletes its media-plane row here (bumping catalog_revision), and the reconciler writes
	// the durable tombstone marker at that authoritative revision — removing the business row and any
	// dvr_chapter_playback mapping. Commodore performs no local catalog mutation.
	if resp.Success {
		s.emitArtifactEvent(ctx, eventArtifactDeleted, tenantID, userID, ipcpb.ArtifactEvent_ARTIFACT_TYPE_VOD, req.ArtifactHash, "", "deleted", nil)
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
func (s *CommodoreServer) TerminateTenantStreams(ctx context.Context, req *foghorncontrolpb.TerminateTenantStreamsRequest) (*foghorncontrolpb.TerminateTenantStreamsResponse, error) {
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

	return &foghorncontrolpb.TerminateTenantStreamsResponse{
		StreamsTerminated:  totalStreams,
		SessionsTerminated: totalSessions,
		StreamNames:        allStreamNames,
	}, nil
}

// InvalidateTenantCache clears cached suspension status for a tenant (called on reactivation)
func (s *CommodoreServer) InvalidateTenantCache(ctx context.Context, req *foghorncontrolpb.InvalidateTenantCacheRequest) (*foghorncontrolpb.InvalidateTenantCacheResponse, error) {
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

	return &foghorncontrolpb.InvalidateTenantCacheResponse{
		EntriesInvalidated: totalInvalidated,
	}, nil
}

// ============================================================================
// CROSS-SERVICE: BILLING DATA ACCESS
// Called by Purser to avoid cross-service database access.
// ============================================================================

// GetTenantUserCount returns active and total user counts for a tenant.
// Called by Purser billing job for user-based billing calculations.
func (s *CommodoreServer) GetTenantUserCount(ctx context.Context, req *commodorepb.GetTenantUserCountRequest) (*commodorepb.GetTenantUserCountResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	counts, err := commodoredb.New(s.db).GetTenantUserCounts(ctx, tenantID)

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Error("Failed to get tenant user count")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	return &commodorepb.GetTenantUserCountResponse{
		ActiveCount: counts.ActiveCount,
		TotalCount:  counts.TotalCount,
	}, nil
}

// GetTenantPrimaryUser returns the primary user info for a tenant.
// Called by Purser billing job for billing notifications and invoices.
// Returns the first owner/admin user, or the first user if no privileged user exists.
func (s *CommodoreServer) GetTenantPrimaryUser(ctx context.Context, req *commodorepb.GetTenantPrimaryUserRequest) (*commodorepb.GetTenantPrimaryUserResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	primary, err := commodoredb.New(s.db).GetTenantPrimaryUser(ctx, tenantID)

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
	if primary.FirstName.Valid && primary.FirstName.String != "" {
		name = primary.FirstName.String
	}
	if primary.LastName.Valid && primary.LastName.String != "" {
		if name != "" {
			name += " "
		}
		name += primary.LastName.String
	}

	return &commodorepb.GetTenantPrimaryUserResponse{
		UserId: primary.ID,
		Email:  primary.Email,
		Name:   name,
	}, nil
}

// CreateUserInTenant creates a user in an existing tenant without triggering
// tenant creation or billing initialization. SERVICE_TOKEN auth only.
func (s *CommodoreServer) CreateUserInTenant(ctx context.Context, req *commodorepb.CreateUserInTenantRequest) (*commodorepb.CreateUserInTenantResponse, error) {
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "CreateUserInTenant requires service token auth")
	}

	tenantID := req.GetTenantId()
	email := normalizeEmail(req.GetEmail())
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

	queries := commodoredb.New(s.db)
	_, err := queries.FindUserIDByEmail(ctx, sql.NullString{String: email, Valid: true})
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

	err = queries.InsertVerifiedTenantUser(ctx, commodoredb.InsertVerifiedTenantUserParams{
		ID: userID, TenantID: tenantID,
		Email:        sql.NullString{String: email, Valid: true},
		PasswordHash: sql.NullString{String: hashedPassword, Valid: true},
		FirstName:    sql.NullString{String: req.GetFirstName(), Valid: true},
		LastName:     sql.NullString{String: req.GetLastName(), Valid: true},
		Role:         role, Permissions: permissions, CreatedAt: sql.NullTime{Time: now, Valid: true},
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create user: %v", err)
	}

	s.logger.WithFields(logging.Fields{
		"user_id":   userID,
		"tenant_id": tenantID,
		"email":     email,
		"role":      role,
	}).Info("User created in existing tenant via CreateUserInTenant")

	return &commodorepb.CreateUserInTenantResponse{
		User: &commodorepb.User{
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
