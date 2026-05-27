package triggers

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/geo"
	"frameworks/api_balancing/internal/ingesterrors"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/geoip"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pullsource"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/tenants"
	"github.com/prometheus/client_golang/prometheus"
)

// streamContext holds cached tenant and user information for a stream
type streamContext struct {
	TenantID          string
	UserID            string
	StreamID          string
	Source            string
	UpdatedAt         time.Time
	LastError         string
	BillingModel      string                  // "postpaid" or "prepaid" - affects cache TTL
	IsSuspended       bool                    // true if tenant is suspended (balance < -$10)
	IsBalanceNegative bool                    // true if prepaid balance <= 0 (should return 402)
	OfficialClusterID string                  // billing-tier cluster for coverage routing
	OriginClusterID   string                  // cluster where stream was originally ingested (for federation attribution)
	ClusterPeers      []*pb.TenantClusterPeer // tenant's full cluster context for demand-driven peering
	ProcessesJSON     string                  // MistServer process config for STREAM_PROCESS trigger
	RequiresAuth      bool                    // true when this playback object has a protected playback policy
	RequiresAuthKnown bool                    // false means the local marker could not be resolved
	DVRPolicy         *pb.DVRPolicy           // tier DVR policy bundle (live window, segment, max entries)
	// Broadcaster billing-allowance state, cached at PUSH_REWRITE so USER_NEW
	// can apply the viewer-side load gate without a fresh Commodore round-trip.
	// IsFreeTier is the tier-identity flag from Purser (tier_name == 'free'),
	// not derived from per-meter unit_price. AllowanceExhausted is true when
	// any free-tier metered allowance is past its included_quantity.
	IsFreeTier         bool
	AllowanceExhausted bool
	// Per-tenant runtime caps from Quartermaster, cached so USER_NEW can
	// enforce max_viewers without re-fetching. 0 means "unlimited" for that
	// dimension.
	MaxStreams int32
	MaxViewers int32
}

// PeerNotifier is the single federation interface for the trigger processor.
// Covers peer discovery, stream tracking, and cross-cluster ingest dedup.
// Implemented by PeerManager which delegates reads to Redis internally.
type PeerNotifier interface {
	NotifyPeers(peers []*pb.TenantClusterPeer, tenantID string)
	TrackStream(streamName string, clusterIDs []string)
	UntrackStream(streamName string)
	BroadcastStreamLifecycle(internalName, tenantID string, isLive bool)
	IsStreamLiveOnPeer(ctx context.Context, internalName, tenantID string) (clusterID string, ok bool)
}

// DVRStarter handles DVR recording orchestration.
// Implemented by FoghornGRPCServer to allow direct internal DVR start without Commodore hop.
type DVRStarter interface {
	StartDVR(ctx context.Context, req *pb.StartDVRRequest) (*pb.StartDVRResponse, error)
}

type DVRStarterWithSourceHint interface {
	StartDVRWithSourceHint(ctx context.Context, req *pb.StartDVRRequest, sourceNodeID string) (*pb.StartDVRResponse, error)
}

// Processor implements the MistTriggerProcessor interface for handling MistServer triggers
type Processor struct {
	logger              logging.Logger
	commodoreClient     *commodore.GRPCClient
	quartermasterClient *qmclient.GRPCClient
	decklogClient       *decklog.BatchedClient
	loadBalancer        *balancer.LoadBalancer
	geoipClient         *geoip.Reader
	geoipCache          *cache.Cache
	dvrService          DVRStarter // Internal DVR orchestration (FoghornGRPCServer)
	metrics             *ProcessorMetrics
	nodeID              string
	clusterID           string
	ownerTenantID       string

	streamCache        *cache.Cache // Cache stream context (tenant + user)
	streamCacheMetaMu  sync.Mutex
	streamCacheHits    uint64
	streamCacheMisses  uint64
	streamCacheResInt  uint64
	streamCacheResPb   uint64
	streamCacheResErr  uint64
	streamCacheLastAt  time.Time
	streamCacheLastErr string

	nodeUUIDCache     *cache.Cache // Cache node_id (logical) -> UUID
	nodeClusterCache  *cache.Cache // Cache node_id (logical) -> cluster_id
	clusterOwnerCache *cache.Cache // Cache cluster_id -> owner_tenant_id
	peerNotifier      PeerNotifier // demand-driven peer discovery (nil when federation disabled)

	gatewayMu         sync.RWMutex
	gatewayURLs       map[string]gatewayCacheEntry // cluster_id -> resolved URL ("" = no gateway), 5min TTL each
	gatewayDiscoverer livepeerGatewayDiscoverer    // override for tests; falls back to quartermasterClient when nil

	// Hold-down so an in-flight ResolveIdentifier cannot repopulate stale
	// stream-context entries between InvalidatePlaybackAuthCache and the
	// next USER_NEW. Keyed by either full cache-key (tenant:internal) or
	// tenant_id; the latter blocks any new key for that tenant.
	streamCacheHoldsMu     sync.Mutex
	streamCacheKeyHolds    map[string]time.Time
	streamCacheTenantHolds map[string]time.Time

	// clientBatcher coalesces enriched ClientLifecycleUpdate samples per
	// (tenant, stream, node) before forwarding to Decklog as
	// CLIENT_LIFECYCLE_BATCH triggers. Lazily started on first use after
	// SetMetrics so the drop counter is available to the batcher.
	clientBatcher   *clientLifecycleBatcher
	clientBatcherMu sync.Mutex
}

// streamCacheHoldDuration is how long writes to the stream-context cache are
// suppressed after an invalidation. Picked to comfortably outlast an in-flight
// gRPC call to Commodore (typical p99 ~200ms) without retaining stale state.
const streamCacheHoldDuration = 2 * time.Second

// gatewayCacheEntry caches a per-cluster Livepeer gateway URL with its resolution time.
// Empty URL is a valid cached negative result.
type gatewayCacheEntry struct {
	url        string
	resolvedAt time.Time
}

// livepeerGatewayDiscoverer is the narrow service-discovery surface the gateway
// resolver depends on, scoped to a single Quartermaster method so tests can
// substitute an in-memory stub without spinning up a gRPC server.
type livepeerGatewayDiscoverer interface {
	DiscoverServices(ctx context.Context, serviceType, clusterID string, pagination *pb.CursorPaginationRequest) (*pb.ServiceDiscoveryResponse, error)
}

// NewProcessor creates a new MistServer trigger processor
func NewProcessor(logger logging.Logger, commodoreClient *commodore.GRPCClient, decklogClient *decklog.BatchedClient, loadBalancer *balancer.LoadBalancer, geoipClient *geoip.Reader) *Processor {
	p := &Processor{
		logger:          logger,
		commodoreClient: commodoreClient,
		decklogClient:   decklogClient,
		loadBalancer:    loadBalancer,
		geoipClient:     geoipClient,
		nodeID:          os.Getenv("NODE_ID"),
		clusterID:       os.Getenv("CLUSTER_ID"),
	}

	p.streamCache = cache.New(cache.Options{
		TTL:                  10 * time.Minute,
		StaleWhileRevalidate: streamCacheSWR(),
		NegativeTTL:          0,
		MaxEntries:           50000,
		SkipStore:            p.streamCacheHeld,
	}, cache.MetricsHooks{
		OnHit:  func(_ map[string]string) { atomic.AddUint64(&p.streamCacheHits, 1) },
		OnMiss: func(_ map[string]string) { atomic.AddUint64(&p.streamCacheMisses, 1) },
	})

	p.nodeUUIDCache = cache.New(cache.Options{
		TTL:                  1 * time.Hour,
		StaleWhileRevalidate: 15 * time.Minute,
		NegativeTTL:          0,
		MaxEntries:           50000,
	}, cache.MetricsHooks{})

	p.nodeClusterCache = cache.New(cache.Options{
		TTL:                  1 * time.Hour,
		StaleWhileRevalidate: 15 * time.Minute,
		NegativeTTL:          0,
		MaxEntries:           50000,
	}, cache.MetricsHooks{})

	p.clusterOwnerCache = cache.New(cache.Options{
		TTL:                  1 * time.Hour,
		StaleWhileRevalidate: 15 * time.Minute,
		NegativeTTL:          0,
		MaxEntries:           50000,
	}, cache.MetricsHooks{})

	return p
}

// CacheProcessConfig stores process config for STREAM_PROCESS trigger lookup.
// Called by the ProcessingDispatcher before dispatching a job to a node.
func (p *Processor) CacheProcessConfig(internalName, processesJSON string) {
	if p.streamCache != nil && processesJSON != "" {
		p.streamCache.Set("process:"+internalName, processesJSON, 30*time.Minute)
	}
}

// gatewayCacheTTL is how long a per-cluster Livepeer gateway URL stays cached
// (positive or negative result) before re-resolving via Quartermaster.
const gatewayCacheTTL = 5 * time.Minute

// getLivepeerGatewayURLForCluster returns the cached Livepeer gateway URL for the
// given cluster. Refreshes from Quartermaster every 5 minutes per cluster. Returns
// "" when no gateway is registered for that cluster. Negative results are cached
// to avoid hot-loop discovery against clusters that legitimately have no gateway.
func (p *Processor) getLivepeerGatewayURLForCluster(clusterID string) string {
	clusterID = strings.TrimSpace(clusterID)
	if clusterID == "" {
		return ""
	}

	p.gatewayMu.RLock()
	if entry, ok := p.gatewayURLs[clusterID]; ok && time.Since(entry.resolvedAt) < gatewayCacheTTL {
		p.gatewayMu.RUnlock()
		return entry.url
	}
	p.gatewayMu.RUnlock()

	disc := p.gatewayDiscoverer
	if disc == nil {
		disc = p.quartermasterClient
	}
	if disc == nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := disc.DiscoverServices(ctx, "livepeer-gateway", clusterID, nil)

	resolved := ""
	if err == nil && len(resp.GetInstances()) > 0 {
		resolved = livepeerGatewayURLFromInstance(resp.GetInstances()[0])
	}

	p.gatewayMu.Lock()
	if p.gatewayURLs == nil {
		p.gatewayURLs = map[string]gatewayCacheEntry{}
	}
	p.gatewayURLs[clusterID] = gatewayCacheEntry{url: resolved, resolvedAt: time.Now()}
	p.gatewayMu.Unlock()

	return resolved
}

func livepeerGatewayURLFromInstance(inst *pb.ServiceInstance) string {
	if inst == nil {
		return ""
	}

	metadata := inst.GetMetadata()
	publicHost := strings.TrimSpace(metadata[servicedefs.LivepeerGatewayMetadataPublicHost])
	host := publicHost
	if host == "" {
		host = strings.TrimSpace(inst.GetHost())
	}
	if host == "" {
		return ""
	}

	scheme := strings.TrimSpace(metadata[servicedefs.LivepeerGatewayMetadataPublicScheme])
	if scheme == "" {
		scheme = strings.TrimSpace(inst.GetProtocol())
	}
	if scheme == "" || publicHost != "" {
		scheme = "https"
	}

	port := inst.GetPort()
	if publicHost != "" {
		port = 443
	} else if rawPort := strings.TrimSpace(metadata[servicedefs.LivepeerGatewayMetadataPublicPort]); rawPort != "" {
		if parsed, convErr := strconv.Atoi(rawPort); convErr == nil && parsed > 0 {
			port = int32(parsed)
		}
	}

	if (scheme == "https" && port == 443) || (scheme == "http" && port == 80) || port == 0 {
		return fmt.Sprintf("%s://%s", scheme, host)
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, port)
}

// SubstituteGatewayURL replaces {{gateway_url}} in process config JSON with the
// first registered Livepeer gateway URL across the candidate clusters, in order.
// Trigger callers pass [origin, official, p.clusterID] so a self-host operator
// who runs their own gateway wins over the platform fallback. Nil/empty
// candidates falls back to p.clusterID.
//
// If no candidate has a gateway, Livepeer process entries are stripped (audio
// transcode and thumbnail processes still run).
func (p *Processor) SubstituteGatewayURL(processesJSON string, candidates []string) string {
	if !strings.Contains(processesJSON, "{{gateway_url}}") {
		return processesJSON
	}

	if len(candidates) == 0 {
		candidates = []string{p.clusterID}
	}

	seen := map[string]struct{}{}
	tried := []string{}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, dup := seen[candidate]; dup {
			continue
		}
		seen[candidate] = struct{}{}
		tried = append(tried, candidate)

		if url := p.getLivepeerGatewayURLForCluster(candidate); url != "" {
			return strings.ReplaceAll(processesJSON, "{{gateway_url}}", url)
		}
	}

	p.logger.WithField("candidates", tried).Warn(
		"Livepeer gateway not registered in any candidate cluster — stripping Livepeer processes (service_unavailable)",
	)
	if p.metrics != nil && p.metrics.ServiceResolutionRejected != nil {
		p.metrics.ServiceResolutionRejected.WithLabelValues("service_unavailable", "livepeer-gateway").Inc()
	}
	return mist.StripLivepeerProcesses(processesJSON)
}

func streamCacheSWR() time.Duration {
	swr := 30 * time.Second
	if raw := os.Getenv("STREAM_CACHE_SWR"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil {
			return parsed
		}
	}
	return swr
}

// StreamContextCacheEntry is a single cached mapping used for tenant/user enrichment.
type StreamContextCacheEntry struct {
	Key       string    `json:"key"`
	TenantID  string    `json:"tenant_id"`
	UserID    string    `json:"user_id"`
	StreamID  string    `json:"stream_id"`
	Source    string    `json:"source"`
	UpdatedAt time.Time `json:"updated_at"`
	LastError string    `json:"last_error,omitempty"`
}

// StreamContextCacheSnapshot is a point-in-time view of the stream context cache + basic health stats.
type StreamContextCacheSnapshot struct {
	GeneratedAt time.Time                 `json:"generated_at"`
	Size        int                       `json:"size"`
	Hits        uint64                    `json:"hits"`
	Misses      uint64                    `json:"misses"`
	ResInternal uint64                    `json:"resolves_internal_name"`
	ResPlayback uint64                    `json:"resolves_playback_id"`
	ResErrors   uint64                    `json:"resolve_errors"`
	LastResolve time.Time                 `json:"last_resolve_at,omitempty"`
	LastError   string                    `json:"last_error,omitempty"`
	Entries     []StreamContextCacheEntry `json:"entries"`
}

func (p *Processor) StreamContextCacheSnapshot() StreamContextCacheSnapshot {
	var entries []StreamContextCacheEntry
	if p.streamCache != nil {
		for _, e := range p.streamCache.Snapshot() {
			info, ok := e.Value.(streamContext)
			if !ok {
				continue
			}
			lastErr := info.LastError
			if lastErr == "" && e.Err != nil {
				lastErr = e.Err.Error()
			}
			entries = append(entries, StreamContextCacheEntry{
				Key:       e.Key,
				TenantID:  info.TenantID,
				UserID:    info.UserID,
				StreamID:  info.StreamID,
				Source:    info.Source,
				UpdatedAt: info.UpdatedAt,
				LastError: lastErr,
			})
		}
	}

	p.streamCacheMetaMu.Lock()
	lastAt := p.streamCacheLastAt
	lastErr := p.streamCacheLastErr
	p.streamCacheMetaMu.Unlock()

	return StreamContextCacheSnapshot{
		GeneratedAt: time.Now(),
		Size:        len(entries),
		Hits:        atomic.LoadUint64(&p.streamCacheHits),
		Misses:      atomic.LoadUint64(&p.streamCacheMisses),
		ResInternal: atomic.LoadUint64(&p.streamCacheResInt),
		ResPlayback: atomic.LoadUint64(&p.streamCacheResPb),
		ResErrors:   atomic.LoadUint64(&p.streamCacheResErr),
		LastResolve: lastAt,
		LastError:   lastErr,
		Entries:     entries,
	}
}

// BillingStatus contains billing status for enforcement decisions
type BillingStatus struct {
	TenantID          string
	BillingModel      string // "postpaid" or "prepaid"
	IsSuspended       bool   // true if balance < -$10 (hard block)
	IsBalanceNegative bool   // true if balance <= 0 (402 warning)
	FromCache         bool   // true if status came from cache
}

// GetBillingStatus looks up billing status for a stream/artifact owner.
// First checks the stream cache (populated during ingest), then falls back to Quartermaster.
// Parameters:
//   - internalName: stream's internal name (e.g., "abc123-def456") - used for cache lookup
//   - tenantID: tenant ID - used for Quartermaster fallback if not in cache
//
// Returns nil if status cannot be determined (fail-open).
func (p *Processor) GetBillingStatus(ctx context.Context, internalName, tenantID string) *BillingStatus {
	// Try cache first (keyed by tenant + internal name)
	if p.streamCache != nil && internalName != "" && tenantID != "" {
		cacheKey := tenantID + ":" + internalName
		if cached, ok := p.streamCache.Peek(cacheKey); ok {
			//nolint:errcheck // cache stores streamContext values; enforced on write
			info := cached.(streamContext)
			return &BillingStatus{
				TenantID:          info.TenantID,
				BillingModel:      info.BillingModel,
				IsSuspended:       info.IsSuspended,
				IsBalanceNegative: info.IsBalanceNegative,
				FromCache:         true,
			}
		}
	}

	// Fallback to Quartermaster if we have tenant ID
	if p.quartermasterClient != nil && tenantID != "" {
		qmCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		resp, err := p.quartermasterClient.ValidateTenant(qmCtx, tenantID, "")
		if err == nil && resp != nil && resp.Valid {
			return &BillingStatus{
				TenantID:          tenantID,
				BillingModel:      resp.BillingModel,
				IsSuspended:       resp.IsSuspended,
				IsBalanceNegative: resp.IsBalanceNegative,
				FromCache:         false,
			}
		}
		if err != nil {
			p.logger.WithFields(logging.Fields{
				"tenant_id":     tenantID,
				"internal_name": internalName,
				"error":         err,
			}).Warn("Quartermaster billing lookup failed")
		}
	}

	return nil // Fail-open
}

// GetClusterPeers returns the cached cluster peers for a stream, if available.
func (p *Processor) GetClusterPeers(internalName, tenantID string) []*pb.TenantClusterPeer {
	if p.streamCache == nil || internalName == "" || tenantID == "" {
		return nil
	}
	cacheKey := tenantID + ":" + internalName
	if cached, ok := p.streamCache.Peek(cacheKey); ok {
		if info, ok := cached.(streamContext); ok {
			return info.ClusterPeers
		}
	}
	return nil
}

// GetStreamOrigin returns the tenantID and origin cluster for a stream by scanning the cache.
// Unlike GetClusterPeers which requires both tenantID and internalName, this only needs
// the internalName — useful when MistServer asks for source selection without tenant context.
func (p *Processor) GetStreamOrigin(internalName string) (tenantID, originClusterID string) {
	if p.streamCache == nil || internalName == "" {
		return "", ""
	}
	suffix := ":" + internalName
	var found int
	for _, entry := range p.streamCache.Snapshot() {
		if strings.HasSuffix(entry.Key, suffix) {
			if info, ok := entry.Value.(streamContext); ok {
				tenantID = info.TenantID
				originClusterID = info.OriginClusterID
				found++
				if found > 1 {
					return "", ""
				}
			}
		}
	}
	return tenantID, originClusterID
}

// InvalidateTenantCache evicts all cache entries for a specific tenant.
// Called when tenant suspension status changes (e.g., after payment).
// Returns the number of entries invalidated.
func (p *Processor) InvalidateTenantCache(tenantID string) int {
	if p.streamCache == nil || tenantID == "" {
		return 0
	}

	// Get all cache entries and find those belonging to this tenant
	var keysToEvict []string
	for _, e := range p.streamCache.Snapshot() {
		if strings.HasPrefix(e.Key, tenantID+":") {
			keysToEvict = append(keysToEvict, e.Key)
			continue
		}
		info, ok := e.Value.(streamContext)
		if ok && info.TenantID == tenantID {
			keysToEvict = append(keysToEvict, e.Key)
		}
	}

	// Evict each matching entry
	for _, key := range keysToEvict {
		p.streamCache.Delete(key)
	}
	p.holdStreamCacheTenant(tenantID)

	if p.commodoreClient != nil {
		p.commodoreClient.InvalidateTenantCacheKeys(tenantID)
	}

	if len(keysToEvict) > 0 {
		p.logger.WithFields(logging.Fields{
			"tenant_id":           tenantID,
			"entries_invalidated": len(keysToEvict),
		}).Info("Invalidated tenant cache entries")
	}

	return len(keysToEvict)
}

// holdStreamCacheKey suppresses repopulation of cacheKey for the hold window.
// Caller passes the full "tenant:internal" cache key.
func (p *Processor) holdStreamCacheKey(cacheKey string) {
	if cacheKey == "" {
		return
	}
	p.streamCacheHoldsMu.Lock()
	defer p.streamCacheHoldsMu.Unlock()
	if p.streamCacheKeyHolds == nil {
		p.streamCacheKeyHolds = make(map[string]time.Time)
	}
	p.streamCacheKeyHolds[cacheKey] = time.Now().Add(streamCacheHoldDuration)
}

// holdStreamCacheTenant suppresses repopulation of every "tenant:..." key
// for the hold window. Used by tenant-wide invalidation.
func (p *Processor) holdStreamCacheTenant(tenantID string) {
	if tenantID == "" {
		return
	}
	p.streamCacheHoldsMu.Lock()
	defer p.streamCacheHoldsMu.Unlock()
	if p.streamCacheTenantHolds == nil {
		p.streamCacheTenantHolds = make(map[string]time.Time)
	}
	p.streamCacheTenantHolds[tenantID] = time.Now().Add(streamCacheHoldDuration)
}

// streamCacheHeld reports whether cacheKey is currently held down. Prunes
// expired entries opportunistically.
func (p *Processor) streamCacheHeld(cacheKey string) bool {
	if cacheKey == "" {
		return false
	}
	now := time.Now()
	p.streamCacheHoldsMu.Lock()
	defer p.streamCacheHoldsMu.Unlock()
	if t, ok := p.streamCacheKeyHolds[cacheKey]; ok {
		if now.Before(t) {
			return true
		}
		delete(p.streamCacheKeyHolds, cacheKey)
	}
	if i := strings.IndexByte(cacheKey, ':'); i > 0 {
		tenantID := cacheKey[:i]
		if t, ok := p.streamCacheTenantHolds[tenantID]; ok {
			if now.Before(t) {
				return true
			}
			delete(p.streamCacheTenantHolds, tenantID)
		}
	}
	return false
}

// InvalidatePlaybackAuthCache evicts cached stream-context markers before
// active sessions are rechecked by MistServer.
func (p *Processor) InvalidatePlaybackAuthCache(tenantID string, internalNames []string) int {
	if p.streamCache == nil || tenantID == "" {
		return 0
	}
	if len(internalNames) == 0 {
		return p.InvalidateTenantCache(tenantID)
	}

	seen := make(map[string]struct{}, len(internalNames))
	keysToEvict := make([]string, 0, len(internalNames))
	for _, name := range internalNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		key := tenantID + ":" + mist.ExtractInternalName(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keysToEvict = append(keysToEvict, key)
	}
	for _, key := range keysToEvict {
		p.streamCache.Delete(key)
		p.holdStreamCacheKey(key)
	}
	if p.commodoreClient != nil {
		p.commodoreClient.InvalidateTenantCacheKeys(tenantID)
	}
	if len(keysToEvict) > 0 {
		p.logger.WithFields(logging.Fields{
			"tenant_id":           tenantID,
			"entries_invalidated": len(keysToEvict),
		}).Info("Invalidated playback auth cache entries")
	}
	return len(keysToEvict)
}

// SetQuartermasterClient configures the Quartermaster client for node UUID lookups
func (p *Processor) SetQuartermasterClient(c *qmclient.GRPCClient) {
	p.quartermasterClient = c
}

// SetCommodoreClient configures the Commodore client for stream resolution.
func (p *Processor) SetCommodoreClient(c *commodore.GRPCClient) {
	p.commodoreClient = c
}

// SetGeoIPCache configures a cache for GeoIP lookups
func (p *Processor) SetGeoIPCache(c *cache.Cache) {
	p.geoipCache = c
}

// SetDVRService configures the DVR orchestration service for auto-start recordings
func (p *Processor) SetDVRService(svc DVRStarter) {
	p.dvrService = svc
}

// SetPeerNotifier configures demand-driven peer discovery from stream validation.
func (p *Processor) SetPeerNotifier(n PeerNotifier) {
	p.peerNotifier = n
}

// SetMetrics configures optional Prometheus metrics for the trigger processor.
func (p *Processor) SetMetrics(m *ProcessorMetrics) {
	p.metrics = m
}

// getClientBatcher returns the lazily-initialised CLIENT_LIFECYCLE batcher.
// Construction is deferred so the drop-counter metric configured via
// SetMetrics is available to the batcher's send-failure path.
func (p *Processor) getClientBatcher() *clientLifecycleBatcher {
	p.clientBatcherMu.Lock()
	defer p.clientBatcherMu.Unlock()
	if p.clientBatcher != nil {
		return p.clientBatcher
	}
	var drops *prometheus.CounterVec
	if p.metrics != nil {
		drops = p.metrics.ClientLifecycleBatchDrops
	}
	p.clientBatcher = newClientLifecycleBatcher(p.sendClientLifecycleBatchToDecklog, p.logger, drops)
	return p.clientBatcher
}

// Shutdown drains any in-flight client-lifecycle batches before the process
// exits. Safe to call multiple times; further calls are no-ops.
func (p *Processor) Shutdown(ctx context.Context) error {
	p.clientBatcherMu.Lock()
	b := p.clientBatcher
	p.clientBatcherMu.Unlock()
	if b == nil {
		return nil
	}
	return b.Shutdown(ctx)
}

// SetClusterID sets the emitting cluster identifier for trigger enrichment.
func (p *Processor) SetClusterID(clusterID string) {
	if strings.TrimSpace(clusterID) == "" {
		return
	}
	p.clusterID = clusterID
}

// SetOwnerTenantID sets the cluster owner tenant for infra event attribution.
func (p *Processor) SetOwnerTenantID(tenantID string) {
	if strings.TrimSpace(tenantID) == "" {
		return
	}
	p.ownerTenantID = tenantID
}

func (p *Processor) ensureTriggerTenantID(trigger *pb.MistTrigger) string {
	if trigger == nil {
		return ""
	}
	if tid := trigger.GetTenantId(); strings.TrimSpace(tid) != "" {
		return tid
	}

	// Some trigger types carry tenant_id only inside the payload; accept/mirror it to the envelope
	// before enforcing the decklog send guard.
	switch tp := trigger.GetTriggerPayload().(type) {
	case *pb.MistTrigger_StreamLifecycleUpdate:
		if tid := strings.TrimSpace(tp.StreamLifecycleUpdate.GetTenantId()); tid != "" {
			trigger.TenantId = &tid
			return tid
		}
	case *pb.MistTrigger_ClientLifecycleUpdate:
		if tid := strings.TrimSpace(tp.ClientLifecycleUpdate.GetTenantId()); tid != "" {
			trigger.TenantId = &tid
			return tid
		}
	case *pb.MistTrigger_ClipLifecycleData:
		if tid := strings.TrimSpace(tp.ClipLifecycleData.GetTenantId()); tid != "" {
			trigger.TenantId = &tid
			return tid
		}
	case *pb.MistTrigger_DvrLifecycleData:
		if tid := strings.TrimSpace(tp.DvrLifecycleData.GetTenantId()); tid != "" {
			trigger.TenantId = &tid
			return tid
		}
	case *pb.MistTrigger_VodLifecycleData:
		if tid := strings.TrimSpace(tp.VodLifecycleData.GetTenantId()); tid != "" {
			trigger.TenantId = &tid
			return tid
		}
	case *pb.MistTrigger_StorageLifecycleData:
		if tid := strings.TrimSpace(tp.StorageLifecycleData.GetTenantId()); tid != "" {
			trigger.TenantId = &tid
			return tid
		}
	case *pb.MistTrigger_StorageSnapshot:
		if tid := strings.TrimSpace(tp.StorageSnapshot.GetTenantId()); tid != "" {
			trigger.TenantId = &tid
			return tid
		}
	case *pb.MistTrigger_ProcessBilling:
		if tid := strings.TrimSpace(tp.ProcessBilling.GetTenantId()); tid != "" {
			trigger.TenantId = &tid
			return tid
		}
	}

	return ""
}

func (p *Processor) sendTriggerToDecklog(trigger *pb.MistTrigger) error {
	return p.sendTriggerToDecklogContext(context.Background(), trigger)
}

func (p *Processor) sendClientLifecycleBatchToDecklog(trigger *pb.MistTrigger) error {
	ctx, cancel := context.WithTimeout(context.Background(), clientBatchSendTimeout)
	defer cancel()
	return p.sendTriggerToDecklogContext(ctx, trigger)
}

func (p *Processor) sendTriggerToDecklogContext(ctx context.Context, trigger *pb.MistTrigger) error {
	if trigger == nil {
		return fmt.Errorf("nil trigger")
	}

	if p.ensureTriggerTenantID(trigger) == "" {
		if p.metrics != nil && p.metrics.DecklogTriggerSends != nil {
			p.metrics.DecklogTriggerSends.WithLabelValues(trigger.GetTriggerType(), "tenant_missing").Inc()
		}
		p.logger.WithFields(logging.Fields{
			"trigger_type": trigger.GetTriggerType(),
			"node_id":      trigger.GetNodeId(),
		}).Warn("Refusing to send trigger without tenant_id")
		return fmt.Errorf("tenant_id required for trigger type %s", trigger.GetTriggerType())
	}

	if p.metrics != nil && p.metrics.DecklogTriggerSends != nil {
		p.metrics.DecklogTriggerSends.WithLabelValues(trigger.GetTriggerType(), "attempt").Inc()
	}

	if p.decklogClient == nil {
		if p.metrics != nil && p.metrics.DecklogTriggerSends != nil {
			p.metrics.DecklogTriggerSends.WithLabelValues(trigger.GetTriggerType(), "client_nil").Inc()
		}
		return fmt.Errorf("decklog client not configured")
	}

	if err := p.decklogClient.SendTriggerContext(ctx, trigger); err != nil {
		if p.metrics != nil && p.metrics.DecklogTriggerSends != nil {
			p.metrics.DecklogTriggerSends.WithLabelValues(trigger.GetTriggerType(), "error").Inc()
		}
		return err
	}

	if p.metrics != nil && p.metrics.DecklogTriggerSends != nil {
		p.metrics.DecklogTriggerSends.WithLabelValues(trigger.GetTriggerType(), "success").Inc()
	}
	return nil
}

func shouldSurfaceDecklogError(trigger *pb.MistTrigger) bool {
	switch trigger.GetTriggerType() {
	case string(mist.TriggerUserEnd),
		string(mist.TriggerStreamEnd),
		string(mist.TriggerPushEnd),
		string(mist.TriggerRecordingEnd),
		string(mist.TriggerRecordingSegment),
		string(mist.TriggerLivepeerSegmentComplete),
		string(mist.TriggerProcessAVSegmentComplete):
		return true
	default:
		return false
	}
}

// ProcessTypedTrigger processes a typed protobuf MistTrigger directly
func (p *Processor) ProcessTypedTrigger(trigger *pb.MistTrigger) (string, bool, error) {
	if trigger == nil {
		return "", true, fmt.Errorf("nil trigger")
	}
	switch trigger.GetTriggerPayload().(type) {
	case *pb.MistTrigger_PushRewrite:
		return p.handlePushRewrite(trigger)
	case *pb.MistTrigger_PlayRewrite:
		return p.handlePlayRewrite(trigger)
	case *pb.MistTrigger_StreamSource:
		return p.handleStreamSource(trigger)
	case *pb.MistTrigger_StreamProcess:
		return p.handleStreamProcess(trigger)
	case *pb.MistTrigger_PushOutStart:
		return p.handlePushOutStart(trigger)
	case *pb.MistTrigger_PushEnd:
		return p.handlePushEnd(trigger)
	case *pb.MistTrigger_ViewerConnect:
		return p.handleUserNew(trigger)
	case *pb.MistTrigger_ViewerDisconnect:
		return p.handleUserEnd(trigger)
	case *pb.MistTrigger_StreamBuffer:
		return p.handleStreamBuffer(trigger)
	case *pb.MistTrigger_StreamEnd:
		return p.handleStreamEnd(trigger)
	case *pb.MistTrigger_TrackList:
		return p.handleLiveTrackList(trigger)
	case *pb.MistTrigger_RecordingComplete:
		return p.handleRecordingEnd(trigger)
	case *pb.MistTrigger_RecordingSegment:
		return p.handleRecordingSegment(trigger)
	case *pb.MistTrigger_StreamLifecycleUpdate:
		return p.handleStreamLifecycleUpdate(trigger)
	case *pb.MistTrigger_ClientLifecycleUpdate:
		return p.handleClientLifecycleUpdate(trigger)
	case *pb.MistTrigger_NodeLifecycleUpdate:
		return p.handleNodeLifecycleUpdate(trigger)
	case *pb.MistTrigger_DvrLifecycleData:
		return p.handleDVRLifecycleData(trigger)
	case *pb.MistTrigger_StorageLifecycleData:
		return p.handleStorageLifecycleData(trigger)
	case *pb.MistTrigger_ProcessBilling:
		return p.handleProcessBilling(trigger)
	case *pb.MistTrigger_RawMistWebhook:
		return p.handleRawMistWebhook(trigger)
	default:
		return "", trigger.GetBlocking(), fmt.Errorf("unsupported trigger payload type")
	}
}

func (p *Processor) handleRawMistWebhook(trigger *pb.MistTrigger) (string, bool, error) {
	if _, ok := trigger.GetTriggerPayload().(*pb.MistTrigger_RawMistWebhook); !ok {
		return "", false, fmt.Errorf("unexpected payload type for RawMistWebhook: %T", trigger.GetTriggerPayload())
	}
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"trigger_type":    trigger.GetTriggerType(),
			"source_event_id": trigger.GetRequestId(),
			"error":           err,
		}).Error("Failed to send raw Mist webhook trigger to Decklog")
		if shouldSurfaceDecklogError(trigger) {
			return "", false, err
		}
	}
	return "", false, nil
}

// handleProcessBilling forwards ProcessBillingEvent to Decklog
func (p *Processor) handleProcessBilling(trigger *pb.MistTrigger) (string, bool, error) {
	payload, ok := trigger.GetTriggerPayload().(*pb.MistTrigger_ProcessBilling)
	if !ok {
		return "", false, fmt.Errorf("unexpected payload type for ProcessBilling: %T", trigger.GetTriggerPayload())
	}
	pbill := payload.ProcessBilling
	internalName := mist.ExtractInternalName(pbill.GetStreamName())

	// Enrich tenant context if not already present
	if pbill.TenantId == nil {
		info := p.applyStreamContext(trigger, internalName)
		if info.TenantID != "" {
			pbill.TenantId = &info.TenantID
		}
	} else if *pbill.TenantId != "" {
		trigger.TenantId = pbill.TenantId
	}
	if pbill.StreamId == nil || *pbill.StreamId == "" {
		if streamID := trigger.GetStreamId(); streamID != "" {
			pbill.StreamId = &streamID
		}
	}

	// Stamp cluster identity onto the billing event so processing minutes
	// are billed against the right cluster's pricing model. Foghorn has the
	// authoritative local cluster_id; Helmsman doesn't, which is why this
	// enrichment lives here rather than at the producer.
	if (pbill.ClusterId == nil || *pbill.ClusterId == "") && p.clusterID != "" {
		clusterID := p.clusterID
		pbill.ClusterId = &clusterID
	}
	if pbill.OriginClusterId == nil || *pbill.OriginClusterId == "" {
		if origin := trigger.GetOriginClusterId(); origin != "" {
			oc := origin
			pbill.OriginClusterId = &oc
		}
	}

	p.logger.WithFields(logging.Fields{
		"internal_name": internalName,
		"process_type":  pbill.GetProcessType(),
		"duration_ms":   pbill.GetDurationMs(),
		"node_id":       trigger.GetNodeId(),
	}).Debug("Processing process_billing trigger")

	// Forward to Decklog
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"trigger_type": trigger.GetTriggerType(),
			"error":        err,
		}).Error("Failed to send process billing trigger to Decklog")
		if shouldSurfaceDecklogError(trigger) {
			return "", false, err
		}
	}
	return "", false, nil
}

// ProcessTrigger satisfies the interface but is not used (control server uses ProcessTypedTrigger)
func (p *Processor) ProcessTrigger(triggerType string, rawPayload []byte, nodeID string) (string, bool, error) {
	return "", true, fmt.Errorf("ProcessTrigger not implemented - use ProcessTypedTrigger for fully typed flow")
}

// handleStorageLifecycleData forwards StorageLifecycleData to Decklog
func (p *Processor) handleStorageLifecycleData(trigger *pb.MistTrigger) (string, bool, error) {
	payload, ok := trigger.GetTriggerPayload().(*pb.MistTrigger_StorageLifecycleData)
	if !ok {
		return "", false, fmt.Errorf("unexpected payload type for StorageLifecycleData: %T", trigger.GetTriggerPayload())
	}
	sld := payload.StorageLifecycleData
	if sld.TenantId != nil {
		trigger.TenantId = sld.TenantId
	}
	if sld.InternalName != nil && *sld.InternalName != "" {
		p.applyStreamContext(trigger, *sld.InternalName)
	} else if sld.StreamId != nil && *sld.StreamId != "" {
		p.applyStreamContext(trigger, *sld.StreamId)
	} else if sld.AssetHash != "" {
		// Helmsman doesn't have platform context — resolve via Commodore's
		// unified resolver which accepts clip_hash/dvr_hash/vod_hash.
		p.applyStreamContext(trigger, sld.AssetHash)
	}
	if sld.StreamId == nil || *sld.StreamId == "" {
		if streamID := trigger.GetStreamId(); streamID != "" {
			sld.StreamId = &streamID
		}
	}

	// Forward to Decklog
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"trigger_type": trigger.GetTriggerType(),
			"error":        err,
		}).Error("Failed to send storage lifecycle trigger to Decklog")
	}
	return "", false, nil
}

// handleDVRLifecycleData forwards DVRLifecycleData to Decklog
func (p *Processor) handleDVRLifecycleData(trigger *pb.MistTrigger) (string, bool, error) {
	payload, ok := trigger.GetTriggerPayload().(*pb.MistTrigger_DvrLifecycleData)
	if !ok {
		return "", false, fmt.Errorf("unexpected payload type for DVRLifecycleData: %T", trigger.GetTriggerPayload())
	}
	dld := payload.DvrLifecycleData
	// Enrich tenant context if available in the payload
	if dld.TenantId != nil && *dld.TenantId != "" {
		trigger.TenantId = dld.TenantId
	}
	if dld.StreamInternalName != nil && *dld.StreamInternalName != "" {
		normalizedName := mist.ExtractInternalName(*dld.StreamInternalName)
		p.applyStreamContext(trigger, normalizedName)
	} else if dld.StreamId != nil && *dld.StreamId != "" {
		// Fallback: resolve tenant/user context from stream_id (UUID)
		p.applyStreamContext(trigger, *dld.StreamId)
	} else if dld.DvrHash != "" {
		// Helmsman may only know the DVR artifact hash at this point.
		p.applyStreamContext(trigger, dld.DvrHash)
	}
	if dld.StreamId == nil || *dld.StreamId == "" {
		if streamID := trigger.GetStreamId(); streamID != "" {
			dld.StreamId = &streamID
		}
	}

	// Forward to Decklog
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"trigger_type": trigger.GetTriggerType(),
			"dvr_hash":     dld.GetDvrHash(),
			"error":        err,
		}).Error("Failed to send DVR lifecycle trigger to Decklog")
	}
	return "", false, nil
}

// handlePushRewrite processes PUSH_REWRITE trigger (blocking)
func (p *Processor) handlePushRewrite(trigger *pb.MistTrigger) (string, bool, error) {
	payload, ok := trigger.GetTriggerPayload().(*pb.MistTrigger_PushRewrite)
	if !ok {
		return "", false, fmt.Errorf("unexpected payload type for PushRewrite: %T", trigger.GetTriggerPayload())
	}
	pushRewrite := payload.PushRewrite
	p.logger.WithFields(logging.Fields{
		"stream_key": pushRewrite.GetStreamName(), // This is the stream key
		"node_id":    trigger.GetNodeId(),
		"push_url":   pushRewrite.GetPushUrl(),
		"hostname":   pushRewrite.GetHostname(),
	}).Debug("Processing PUSH_REWRITE trigger")

	ingestClusterID := strings.TrimSpace(p.resolveNodeClusterID(trigger.GetNodeId()))
	if ingestClusterID == "" {
		ingestClusterID = strings.TrimSpace(trigger.GetClusterId())
	}
	if ingestClusterID == "" {
		ingestClusterID = strings.TrimSpace(p.clusterID)
	}

	streamValidation, err := p.commodoreClient.ValidateStreamKey(context.Background(), pushRewrite.GetStreamName(), ingestClusterID)
	if err != nil {
		p.logger.WithFields(logging.Fields{
			"stream_key": pushRewrite.GetStreamName(),
			"error":      err,
		}).Error("Failed to validate stream key with Commodore")
		return "", true, ingesterrors.New(pb.IngestErrorCode_INGEST_ERROR_INTERNAL, "failed to validate stream key")
	}

	if !streamValidation.Valid {
		message := streamValidation.Error
		if message == "" {
			message = "invalid stream key"
		}
		return "", true, ingesterrors.New(pb.IngestErrorCode_INGEST_ERROR_INVALID_STREAM_KEY, message)
	}

	// Check if tenant is suspended (prepaid balance < -$10)
	// Reject new ingests for suspended tenants
	if streamValidation.IsSuspended {
		p.logger.WithFields(logging.Fields{
			"stream_key": pushRewrite.GetStreamName(),
			"tenant_id":  streamValidation.TenantId,
		}).Warn("Rejecting ingest: tenant suspended due to negative balance")
		return "", true, ingesterrors.New(pb.IngestErrorCode_INGEST_ERROR_ACCOUNT_SUSPENDED, "account suspended - please top up your balance")
	}

	// Check if balance is negative (balance <= 0, but not yet suspended)
	// Return 402-style error for new ingests requiring payment
	if streamValidation.IsBalanceNegative {
		p.logger.WithFields(logging.Fields{
			"stream_key": pushRewrite.GetStreamName(),
			"tenant_id":  streamValidation.TenantId,
		}).Warn("Rejecting ingest: insufficient balance (402 Payment Required)")
		return "", true, ingesterrors.New(pb.IngestErrorCode_INGEST_ERROR_PAYMENT_REQUIRED, "payment required - please top up your balance")
	}

	// Load-aware free-tier admission: if the tenant has exhausted a free-tier
	// allowance (delivered_minutes today) AND the cluster is under load, deny
	// new ingest so paying tenants keep capacity. Idle clusters keep admitting
	// over-allowance free tenants — exhaustion alone is not a hard cap.
	// Active streams are never killed here; only new PUSH_REWRITE is gated.
	if reason, blocked := p.evaluateFreeTierAdmission(streamValidation, ingestClusterID); blocked {
		p.logger.WithFields(logging.Fields{
			"stream_key": pushRewrite.GetStreamName(),
			"tenant_id":  streamValidation.TenantId,
			"reason":     reason,
		}).Warn("Rejecting ingest: free-tier allowance exhausted under cluster load")
		return "", true, ingesterrors.New(pb.IngestErrorCode_INGEST_ERROR_FREE_TIER_EXHAUSTED, reason)
	}

	// Per-tenant concurrent-stream cap. Hard limit independent of cluster
	// load — once a tenant has max_streams active, the next PUSH_REWRITE is
	// rejected regardless of load. Counters live in Foghorn's tenant_capacity
	// state; cap value comes from Purser tier entitlements, optionally
	// overridden by Quartermaster cluster access, then forwarded through
	// Commodore.ValidateStreamKey. A re-fire of an already-tracked stream is
	// admitted (the count doesn't change).
	registerTenantStream := false
	if caps := streamValidation.GetTenantResourceLimits(); caps.GetMaxStreams() > 0 {
		tc := state.DefaultTenantCapacity()
		internalName := streamValidation.GetInternalName()
		tc.ReconcileStreams(streamValidation.TenantId, liveInternalNamesForTenant(streamValidation.TenantId))
		current := tc.CountStreams(streamValidation.TenantId)
		alreadyTracked := tc.HasStream(streamValidation.TenantId, internalName)
		if !alreadyTracked && int32(current) >= caps.GetMaxStreams() {
			p.logger.WithFields(logging.Fields{
				"stream_key":  pushRewrite.GetStreamName(),
				"tenant_id":   streamValidation.TenantId,
				"current":     current,
				"max_streams": caps.GetMaxStreams(),
			}).Warn("Rejecting ingest: tenant concurrent-stream cap reached")
			return "", true, ingesterrors.New(
				pb.IngestErrorCode_INGEST_ERROR_TENANT_STREAM_CAP,
				fmt.Sprintf("concurrent stream cap reached (%d/%d) — close another stream or upgrade", current, caps.GetMaxStreams()),
			)
		}
		registerTenantStream = true
	}

	// Cross-cluster dedup: reject if stream is already live on a peer cluster
	if p.peerNotifier != nil {
		if remoteCluster, ok := p.peerNotifier.IsStreamLiveOnPeer(context.Background(), streamValidation.InternalName, streamValidation.TenantId); ok {
			p.logger.WithFields(logging.Fields{
				"internal_name":  streamValidation.InternalName,
				"remote_cluster": remoteCluster,
			}).Warn("Rejecting duplicate ingest — stream already live on peer cluster")
			return "", true, ingesterrors.New(pb.IngestErrorCode_INGEST_ERROR_DUPLICATE_INGEST, "stream already live on cluster "+remoteCluster)
		}
	}

	if registerTenantStream {
		state.DefaultTenantCapacity().RegisterStream(streamValidation.TenantId, streamValidation.GetInternalName())
	}

	// Cache stream context (tenant + user + billing info)
	if p.streamCache != nil {
		isFree, exhausted := freeTierAllowanceState(streamValidation.GetAllowances())
		caps := streamValidation.GetTenantResourceLimits()
		info := streamContext{
			TenantID:           streamValidation.TenantId,
			UserID:             streamValidation.UserId,
			StreamID:           streamValidation.StreamId,
			Source:             "validate_stream_key",
			UpdatedAt:          time.Now(),
			BillingModel:       streamValidation.BillingModel,
			IsSuspended:        streamValidation.IsSuspended,
			IsBalanceNegative:  streamValidation.IsBalanceNegative,
			OfficialClusterID:  streamValidation.GetOfficialClusterId(),
			OriginClusterID:    streamValidation.GetOriginClusterId(),
			ClusterPeers:       streamValidation.GetClusterPeers(),
			ProcessesJSON:      streamValidation.GetProcessesJson(),
			DVRPolicy:          streamValidation.GetDvrPolicy(),
			MaxStreams:         caps.GetMaxStreams(),
			MaxViewers:         caps.GetMaxViewers(),
			IsFreeTier:         isFree,
			AllowanceExhausted: exhausted,
		}
		if p.peerNotifier != nil && len(info.ClusterPeers) > 0 {
			p.peerNotifier.NotifyPeers(info.ClusterPeers, streamValidation.TenantId)
			var cids []string
			for _, peer := range info.ClusterPeers {
				cids = append(cids, peer.GetClusterId())
			}
			p.peerNotifier.TrackStream(streamValidation.InternalName, cids)
		}
		// Use shorter cache TTL for prepaid tenants (1 min vs 10 min)
		// This ensures faster enforcement of balance changes
		cacheTTL := 10 * time.Minute
		if streamValidation.BillingModel == "prepaid" {
			cacheTTL = 1 * time.Minute
		}
		if streamValidation.TenantId != "" {
			cacheKey := streamValidation.TenantId + ":" + streamValidation.InternalName
			p.streamCache.Set(cacheKey, info, cacheTTL)
		}
		// Secondary index for STREAM_PROCESS lookup (keyed by internal name only).
		// Substitute {{gateway_url}} with origin-first cluster resolution so a
		// self-host operator's own gateway wins over the platform fallback.
		if info.ProcessesJSON != "" {
			candidates := []string{
				streamValidation.GetOriginClusterId(),
				streamValidation.GetOfficialClusterId(),
				p.clusterID,
			}
			info.ProcessesJSON = p.SubstituteGatewayURL(info.ProcessesJSON, candidates)
			p.streamCache.Set("process:"+streamValidation.InternalName, info.ProcessesJSON, cacheTTL)
		}
		p.streamCacheMetaMu.Lock()
		p.streamCacheLastAt = info.UpdatedAt
		p.streamCacheLastErr = ""
		p.streamCacheMetaMu.Unlock()
	}
	if streamValidation.TenantId != "" {
		trigger.TenantId = &streamValidation.TenantId
	}
	if streamValidation.UserId != "" {
		trigger.UserId = &streamValidation.UserId
	}
	if streamValidation.StreamId != "" {
		trigger.StreamId = &streamValidation.StreamId
	}
	if streamValidation.StreamId != "" {
		streamID := streamValidation.StreamId
		pushRewrite.StreamId = &streamID
	}
	if trigger.GetNodeId() != "" && streamValidation.GetInternalName() != "" {
		state.DefaultManager().UpdateNodeStats(streamValidation.GetInternalName(), trigger.GetNodeId(), 0, 1, 0, 0, false)
	}
	if originClusterID := streamValidation.GetOriginClusterId(); originClusterID != "" {
		trigger.OriginClusterId = &originClusterID
	}
	if originClusterID := streamValidation.GetOriginClusterId(); originClusterID != "" &&
		(trigger.ClusterId == nil || strings.TrimSpace(trigger.GetClusterId()) == "" || strings.TrimSpace(trigger.GetClusterId()) == strings.TrimSpace(p.clusterID)) {
		trigger.ClusterId = &originClusterID
	} else if (trigger.ClusterId == nil || strings.TrimSpace(trigger.GetClusterId()) == "") && ingestClusterID != "" {
		clusterID := ingestClusterID
		trigger.ClusterId = &clusterID
	}

	// Detect protocol from push URL
	protocol := p.detectProtocol(pushRewrite.GetPushUrl())

	// Get geographic data from node configuration
	var latitude, longitude *float64
	var location string
	var nodeBucket *pb.GeoBucket
	if nodeConfig := p.getNodeConfig(trigger.GetNodeId()); nodeConfig != nil {
		if nodeConfig.Latitude != 0 {
			latitude = &nodeConfig.Latitude
		}
		if nodeConfig.Longitude != 0 {
			longitude = &nodeConfig.Longitude
		}
		if nodeConfig.Location != "" {
			location = nodeConfig.Location
		}
		if b, centLat, centLon, ok := geo.Bucket(nodeConfig.Latitude, nodeConfig.Longitude); ok {
			nodeBucket = b
			latitude = &centLat
			longitude = &centLon
		}
	}

	// Enrich the existing MistTrigger with geo data and forward to Decklog
	// The PUSH_REWRITE trigger payload already contains all the stream ingest data
	pushRewrite.Protocol = &protocol
	pushRewrite.NodeId = &trigger.NodeId
	if latitude != nil {
		pushRewrite.Latitude = latitude
	}
	if longitude != nil {
		pushRewrite.Longitude = longitude
	}
	if nodeBucket != nil {
		pushRewrite.NodeBucket = nodeBucket
	}
	if location != "" {
		pushRewrite.Location = &location
	}

	// GeoIP enrich publisher location from hostname (encoder IP)
	if p.geoipClient != nil {
		if geoData := p.geoipClient.Lookup(pushRewrite.GetHostname()); geoData != nil {
			if geoData.CountryCode != "" {
				pushRewrite.PublisherCountryCode = &geoData.CountryCode
			}
			if geoData.City != "" {
				pushRewrite.PublisherCity = &geoData.City
			}
			if b, centLat, centLon, ok := geo.Bucket(geoData.Latitude, geoData.Longitude); ok {
				pushRewrite.PublisherBucket = b
				pushRewrite.PublisherLatitude = &centLat
				pushRewrite.PublisherLongitude = &centLon
			}
		}
	}

	// Forward the enriched MistTrigger directly to Decklog (Data Plane)
	// This flows to Periscope for operational state tracking
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"stream_key": pushRewrite.GetStreamName(),
			"error":      err,
		}).Error("Failed to send stream ingest event to Decklog")
	}

	// Control-plane stream lifecycle is derived from Decklog events.

	// Check if DVR recording is enabled for this stream and start it
	if streamValidation.IsRecordingEnabled {
		p.logger.WithFields(logging.Fields{
			"internal_name": streamValidation.InternalName,
		}).Info("DVR recording enabled for stream, starting DVR")

		// Stream validation already resolved tenant DVR policy; Foghorn owns
		// the recording session and chapter materialization state.
		go func() {
			if p.dvrService == nil {
				p.logger.WithField("internal_name", streamValidation.InternalName).
					Error("DVR service not configured, cannot start recording")
				return
			}
			userID := streamValidation.UserId
			dvrCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			dvrReq := &pb.StartDVRRequest{
				TenantId:      streamValidation.TenantId,
				InternalName:  streamValidation.InternalName,
				UserId:        &userID,
				ClusterId:     streamValidation.GetOriginClusterId(),
				DvrPolicy:     streamValidation.GetDvrPolicy(),
				ProcessesJson: streamValidation.GetDvrProcessesJson(),
			}
			var dvrResponse *pb.StartDVRResponse
			var err error
			if hinted, ok := p.dvrService.(DVRStarterWithSourceHint); ok {
				dvrResponse, err = hinted.StartDVRWithSourceHint(dvrCtx, dvrReq, trigger.GetNodeId())
			} else {
				dvrResponse, err = p.dvrService.StartDVR(dvrCtx, dvrReq)
			}
			if err != nil {
				p.logger.WithFields(logging.Fields{
					"internal_name": streamValidation.InternalName,
					"tenant_id":     streamValidation.TenantId,
					"error":         err,
				}).Error("Failed to start DVR recording")
			} else {
				p.logger.WithFields(logging.Fields{
					"internal_name": streamValidation.InternalName,
					"tenant_id":     streamValidation.TenantId,
					"dvr_hash":      dvrResponse.GetDvrHash(),
					"status":        dvrResponse.GetStatus(),
				}).Info("DVR recording started")
			}
		}()
	}

	// Activate multistream push targets if any are configured
	if len(streamValidation.GetPushTargets()) > 0 {
		go p.activatePushTargets(
			trigger.GetNodeId(),
			streamValidation.InternalName,
			streamValidation.GetTenantId(),
			streamValidation.GetPushTargets(),
		)
	}

	// Set playback_id and stream_id on stream state for federation and thumbnail S3 keying
	if streamValidation.PlaybackId != "" {
		state.DefaultManager().SetStreamPlaybackID(streamValidation.InternalName, streamValidation.PlaybackId)
	}
	if streamValidation.StreamId != "" {
		state.DefaultManager().SetStreamStreamID(streamValidation.InternalName, streamValidation.StreamId)
	}

	// Broadcast stream-live to federated peers for cross-cluster dedup
	if p.peerNotifier != nil {
		p.peerNotifier.BroadcastStreamLifecycle(streamValidation.InternalName, streamValidation.TenantId, true)
	}

	// Return wildcard stream name for MistServer routing (live+ format)
	return fmt.Sprintf("live+%s", streamValidation.InternalName), false, nil
}

// handlePlayRewrite processes PLAY_REWRITE trigger (blocking)
func (p *Processor) handlePlayRewrite(trigger *pb.MistTrigger) (string, bool, error) {
	payload, ok := trigger.GetTriggerPayload().(*pb.MistTrigger_PlayRewrite)
	if !ok {
		return "", false, fmt.Errorf("unexpected payload type for PlayRewrite: %T", trigger.GetTriggerPayload())
	}
	defaultStream := payload.PlayRewrite
	playbackID := defaultStream.GetRequestedStream() // This is the stream name / playback ID

	p.logger.WithFields(logging.Fields{
		"requested_stream": defaultStream.GetRequestedStream(), // playback ID
		"viewer_host":      defaultStream.GetViewerHost(),
		"output_type":      defaultStream.GetOutputType(),
		"request_url":      defaultStream.GetRequestUrl(),
		"node_id":          trigger.GetNodeId(),
	}).Debug("Processing PLAY_REWRITE trigger")

	// Resolve the playback ID to its canonical internal name (e.g. "live+uuid" or "vod+hash").
	target, err := control.ResolveStream(context.Background(), playbackID)
	if (err != nil || target == nil || target.InternalName == "") && !strings.Contains(playbackID, "+") {
		var abort bool
		target, abort, err = p.resolveBarePlayRewriteTarget(playbackID)
		if abort || err != nil {
			return "", abort, err
		}
	}
	if err != nil || target == nil {
		p.logger.WithFields(logging.Fields{
			"playback_id": playbackID,
			"error":       err,
		}).Warn("PLAY_REWRITE: resolver rejected token")
		return "", false, nil //nolint:nilerr // resolver-rejection is not a Mist-level error; empty rewrite = not found
	}

	if target.InternalName == "" {
		p.logger.WithFields(logging.Fields{
			"playback_id": playbackID,
		}).Debug("PLAY_REWRITE: stream not found")
		return "", false, nil
	}

	// Check stream owner's billing status from cache (set during PUSH_REWRITE).
	// Falls back to Quartermaster when cache misses.
	billingInternalName := control.DVRChapterPolicyInternalName(context.Background(), target.InternalName)
	billing := p.GetBillingStatus(context.Background(), billingInternalName, target.TenantID)
	if billing != nil {
		if billing.IsSuspended {
			p.logger.WithFields(logging.Fields{
				"playback_id": playbackID,
				"tenant_id":   billing.TenantID,
				"from_cache":  billing.FromCache,
			}).Warn("Rejecting viewer: stream owner suspended")
			return "", true, fmt.Errorf("stream unavailable - owner account suspended")
		}
		if billing.BillingModel == "prepaid" && billing.IsBalanceNegative {
			p.logger.WithFields(logging.Fields{
				"playback_id":   playbackID,
				"tenant_id":     billing.TenantID,
				"billing_model": billing.BillingModel,
				"from_cache":    billing.FromCache,
			}).Warn("Rejecting viewer: stream owner balance exhausted (402)")
			return "", true, fmt.Errorf("payment required - stream owner needs to top up balance")
		}
	} else if target.TenantID != "" {
		p.logger.WithFields(logging.Fields{
			"playback_id":   playbackID,
			"tenant_id":     target.TenantID,
			"internal_name": target.InternalName,
		}).Debug("Billing status unknown, failing open")
	}

	// Enrich with resolved internal name (UUID without prefix) for analytics correlation.
	// This ensures analytics can correlate viewer events with infrastructure events.
	resolvedName := mist.ExtractInternalName(target.InternalName)
	defaultStream.ResolvedInternalName = &resolvedName

	// Enrich the PlayRewriteTrigger (ViewerResolveTrigger) with viewer geographic data via GeoIP lookup.
	if p.geoipClient != nil && defaultStream.GetViewerHost() != "" {
		if geoData := geoip.LookupCached(context.Background(), p.geoipClient, p.geoipCache, defaultStream.GetViewerHost()); geoData != nil {
			defaultStream.CountryCode = &geoData.CountryCode
			defaultStream.City = &geoData.City
			defaultStream.Latitude = &geoData.Latitude
			defaultStream.Longitude = &geoData.Longitude

			p.logger.WithFields(logging.Fields{
				"viewer_ip":    defaultStream.GetViewerHost(),
				"country_code": geoData.CountryCode,
				"city":         geoData.City,
				"playback_id":  playbackID,
			}).Debug("Enriched PLAY_REWRITE with viewer geo data")
		}
	}

	// Enrich with node location name for analytics (e.g., "us-east-1", "Frankfurt")
	if nodeConfig := p.getNodeConfig(trigger.GetNodeId()); nodeConfig != nil {
		if nodeConfig.Location != "" {
			defaultStream.NodeLocation = &nodeConfig.Location
		}
	}

	// Apply the resolved TenantID if available.
	if target.TenantID != "" {
		trigger.TenantId = &target.TenantID
	}
	if target.StreamID != "" {
		trigger.StreamId = &target.StreamID
		defaultStream.StreamId = &target.StreamID
	}
	isLivePlayback := target.ContentType == "live" || strings.HasPrefix(target.InternalName, "live+")
	if isLivePlayback && mist.IsPlaybackViewerRequest(defaultStream.GetOutputType(), defaultStream.GetRequestUrl()) {
		correlationID := extractCorrelationID(defaultStream.GetRequestUrl())
		if viewerID, started := state.DefaultManager().StartVirtualViewerByID(correlationID, trigger.GetNodeId(), resolvedName, defaultStream.GetViewerHost()); started {
			state.DefaultManager().UpdateUserConnection(resolvedName, trigger.GetNodeId(), target.TenantID, 1)
			p.logger.WithFields(logging.Fields{
				"viewer_id":     viewerID,
				"internal_name": resolvedName,
				"viewer_host":   defaultStream.GetViewerHost(),
				"output_type":   defaultStream.GetOutputType(),
				"node_id":       trigger.GetNodeId(),
			}).Info("Started playback viewer from PLAY_REWRITE")
		}
	}

	go func(tr *pb.MistTrigger, requested string) {
		if err := p.sendTriggerToDecklog(tr); err != nil {
			p.logger.WithFields(logging.Fields{
				"requested_stream": requested,
				"trigger_type":     tr.GetTriggerType(),
				"error":            err,
			}).Error("Failed to send play_rewrite trigger to Decklog")
		}
	}(trigger, playbackID)

	// Return the resolved fully-qualified stream name (e.g. "live+uuid") to MistServer.
	return target.InternalName, false, nil
}

func (p *Processor) resolveBarePlayRewriteTarget(streamName string) (*control.StreamTarget, bool, error) {
	if p.commodoreClient == nil || strings.TrimSpace(streamName) == "" {
		return nil, false, nil
	}

	streamCtx, lookupField, err := p.resolveBareManagedStreamContext(streamName)
	if err != nil {
		p.logger.WithFields(logging.Fields{
			"requested_stream": streamName,
			"error":            err,
		}).Warn("PLAY_REWRITE: bare managed stream lookup failed")
		return nil, false, err
	}
	if streamCtx == nil || streamCtx.GetInternalName() == "" {
		return nil, false, nil
	}
	if !streamCtx.GetAdmitted() {
		if streamCtx.GetAdmissionReason() == "stream not found" {
			return nil, false, nil
		}
		return nil, true, fmt.Errorf("stream unavailable: %s", streamCtx.GetAdmissionReason())
	}

	resolvedName := control.MistSourceNameForIngestMode(streamCtx.GetInternalName(), streamCtx.GetIngestMode())
	p.logger.WithFields(logging.Fields{
		"requested_stream": streamName,
		"lookup_field":     lookupField,
		"internal_name":    streamCtx.GetInternalName(),
		"resolved_stream":  resolvedName,
		"ingest_mode":      streamCtx.GetIngestMode(),
	}).Info("PLAY_REWRITE resolved bare managed stream")

	return &control.StreamTarget{
		InternalName: resolvedName,
		IsVod:        false,
		TenantID:     streamCtx.GetTenantId(),
		StreamID:     streamCtx.GetStreamId(),
		ContentType:  "live",
		ClusterPeers: streamCtx.GetClusterPeers(),
	}, false, nil
}

// handleStreamSource processes STREAM_SOURCE trigger (blocking)
func (p *Processor) handleStreamSource(trigger *pb.MistTrigger) (string, bool, error) {
	payload, ok := trigger.GetTriggerPayload().(*pb.MistTrigger_StreamSource)
	if !ok {
		return "", false, fmt.Errorf("unexpected payload type for StreamSource: %T", trigger.GetTriggerPayload())
	}
	streamSource := payload.StreamSource
	streamName := streamSource.GetStreamName()

	p.logger.WithFields(logging.Fields{
		"stream_name": streamName,
		"node_id":     trigger.GetNodeId(),
	}).Debug("Processing STREAM_SOURCE trigger")

	// Pushed live streams have no static source; MistServer receives them from
	// encoders. Pull streams are handled below.
	if strings.HasPrefix(streamName, "live+") {
		p.logger.WithFields(logging.Fields{
			"stream_name": streamName,
			"node_id":     trigger.GetNodeId(),
		}).Debug("STREAM_SOURCE not applicable for live streams; aborting")
		return "", true, nil
	}

	// processing+ streams: resolve to presigned S3 URL for processing input
	if strings.HasPrefix(streamName, "processing+") {
		artifactHash := strings.TrimPrefix(streamName, "processing+")
		return p.resolveProcessSource(artifactHash, trigger.GetNodeId())
	}

	// pull+ streams: return balance:<foghorn> so MistInBalancer asks /source for
	// the chosen origin. The upstream URI itself stays server-side — we never
	// embed it in the returned string (avoid trusting query-param fallbacks at
	// /source). /source re-resolves via Commodore and scores upstream-vs-cluster.
	if strings.HasPrefix(streamName, "pull+") {
		return p.resolvePullSource(streamName, trigger)
	}

	if !strings.Contains(streamName, "+") && p.commodoreClient != nil {
		resp, lookupMode, err := p.resolveBareManagedStreamContext(streamName)
		if err == nil && resp != nil && resp.GetAdmitted() && resp.GetIngestMode() == "mist_native" {
			base := control.FoghornBalancerBase(p.clusterID)
			if base == "" {
				p.logger.WithField("stream_name", streamName).Warn("STREAM_SOURCE: mist_native stream has no Foghorn balancer base; aborting")
				return "", true, nil
			}
			p.logger.WithFields(logging.Fields{
				"stream_name":   streamName,
				"internal_name": resp.GetInternalName(),
				"lookup_mode":   lookupMode,
				"cluster_id":    p.clusterID,
			}).Info("STREAM_SOURCE: mist_native stream returning balance URI")
			return "balance:" + base, false, nil
		}
		if err != nil {
			p.logger.WithError(err).WithFields(logging.Fields{
				"stream_name": streamName,
				"cluster_id":  p.clusterID,
			}).Warn("STREAM_SOURCE: mist_native stream context lookup failed")
		} else if resp != nil && resp.GetAdmissionReason() != "" {
			p.logger.WithFields(logging.Fields{
				"stream_name":       streamName,
				"lookup_mode":       lookupMode,
				"ingest_mode":       resp.GetIngestMode(),
				"admission_reason":  resp.GetAdmissionReason(),
				"rejection_reason":  resp.GetRejectionReason().String(),
				"resolved_internal": resp.GetInternalName(),
			}).Debug("STREAM_SOURCE: bare stream is not an admitted mist_native source")
		}
	}

	// dvr+<dvr_internal_name>: rolling DVR surface for an actively
	// recording stream. The origin serves the local rolling manifest;
	// other edges DTSC-pull the dvr+<internal_name> stream from the
	// origin so the puller's Mist materializes the rolling view from
	// the origin's frames-with-timestamps. Chapter playback is NOT
	// served via dvr+ — finalized chapter artifacts are addressed by
	// their VOD playback ID and flow through the standard vod+ path.
	if strings.HasPrefix(streamName, "dvr+") {
		token := strings.TrimPrefix(streamName, "dvr+")
		if token == "" {
			return "", true, nil
		}
		dispatch, dispatchErr := control.ResolveDVRArtifactDispatch(context.Background(), token)
		if dispatchErr != nil {
			p.logger.WithError(dispatchErr).WithFields(logging.Fields{
				"stream_name": streamName,
				"token":       token,
			}).Warn("STREAM_SOURCE: dvr+ artifact dispatch lookup failed")
		}
		if dispatch == nil || dispatch.DVRHash == "" {
			p.logger.WithFields(logging.Fields{
				"stream_name": streamName,
				"token":       token,
			}).Warn("STREAM_SOURCE: dvr+ token did not resolve to a DVR artifact; chapter playback uses the chapter artifact's VOD playbackId")
			return "", true, nil
		}
		if dispatch.RecordingNode == "" {
			p.logger.WithFields(logging.Fields{
				"stream_name": streamName,
				"dvr_hash":    dispatch.DVRHash,
			}).Warn("STREAM_SOURCE: dvr+ resolution for finalized DVR — use chapter playbackId")
			return "", true, nil
		}
		if dispatch.RecordingNode == trigger.GetNodeId() {
			localPath := control.LocalRollingDVRManifestPath(dispatch.StreamID, dispatch.DVRHash, trigger.GetNodeId())
			if localPath == "" {
				p.logger.WithFields(logging.Fields{
					"stream_name": streamName,
					"dvr_hash":    dispatch.DVRHash,
					"stream_id":   dispatch.StreamID,
					"node_id":     trigger.GetNodeId(),
				}).Warn("STREAM_SOURCE: dvr+ rolling manifest path unresolved on recording origin; aborting")
				return "", true, nil
			}
			p.logger.WithFields(logging.Fields{
				"stream_name": streamName,
				"dvr_hash":    dispatch.DVRHash,
				"local_path":  localPath,
			}).Debug("STREAM_SOURCE: dvr+ rolling DVR served from local manifest on recording origin")
			return localPath, false, nil
		}
		dtscURL := control.BuildDTSCURI(dispatch.RecordingNode, streamName, p.logger)
		if dtscURL == "" {
			p.logger.WithFields(logging.Fields{
				"stream_name":    streamName,
				"recording_node": dispatch.RecordingNode,
				"viewer_node":    trigger.GetNodeId(),
			}).Warn("STREAM_SOURCE: dvr+ no DTSC output advertised on recording origin; aborting")
			return "", true, nil
		}
		p.logger.WithFields(logging.Fields{
			"stream_name":    streamName,
			"recording_node": dispatch.RecordingNode,
			"viewer_node":    trigger.GetNodeId(),
			"dtsc_url":       dtscURL,
		}).Debug("STREAM_SOURCE: dvr+ rolling DVR pulled via DTSC from recording origin")
		return dtscURL, false, nil
	}

	// Extract artifact internal name (strips vod+ or any other prefix)
	artifactInternal := mist.ExtractInternalName(streamName)

	artifactHash := ""
	originClusterID := ""
	contentType := ""
	tenantID := ""
	if control.CommodoreClient != nil && artifactInternal != "" {
		if resp, err := control.CommodoreClient.ResolveArtifactInternalName(context.Background(), artifactInternal); err == nil && resp.Found {
			artifactHash = resp.ArtifactHash
			originClusterID = resp.GetOriginClusterId()
			contentType = resp.GetContentType()
			tenantID = resp.GetTenantId()
		}
	}
	if artifactHash == "" {
		// Chapter artifacts are vod+<artifact_hash> by construction and
		// aren't registered in Commodore — resolve via foghorn directly
		// to recover tenant/origin context for the relay URL path below.
		if chapter := control.ResolveChapterArtifactByHash(context.Background(), artifactInternal); chapter != nil {
			artifactHash = chapter.ArtifactHash
			originClusterID = chapter.OriginClusterID
			contentType = "vod"
			tenantID = chapter.TenantID
		}
	}
	if artifactHash == "" {
		p.logger.WithFields(logging.Fields{
			"internal_name": artifactInternal,
			"stream_name":   streamName,
		}).Warn("Artifact internal name not found; cannot resolve stream source")
		return "", true, nil
	}

	target, err := control.ResolveStream(context.Background(), streamName)
	if err != nil {
		p.logger.WithFields(logging.Fields{
			"stream_name": streamName,
			"error":       err,
		}).Warn("Failed to resolve stream source")
	}
	if target != nil {
		if target.TenantID != "" {
			trigger.TenantId = &target.TenantID
		}
		if target.StreamID != "" {
			trigger.StreamId = &target.StreamID
			streamSource.StreamId = &target.StreamID
		}
	}

	// Read-through relay: hand Mist a stable Helmsman URL for the artifact
	// regardless of warm state. Helmsman either serves the local file
	// (warm) or fetches from S3 via RelayResolve (cold), per admission
	// policy. Defrost is no longer the playback gate.
	_, artifactInfo := state.DefaultManager().FindNodeByArtifactHash(artifactHash)
	format := ""
	if artifactInfo != nil {
		format = artifactInfo.GetFormat()
		p.logger.WithFields(logging.Fields{
			"artifact_hash": artifactHash,
			"stream_name":   streamName,
			"file_path":     artifactInfo.GetFilePath(),
			"format":        format,
			"size_bytes":    artifactInfo.GetSizeBytes(),
		}).Debug("VOD artifact warm on this node; routing through relay anyway")
	}
	// Persisted descriptor — format + stream_internal_name in one DB
	// hit. The clip-writer nests as clips/<stream>/<hash>.<ext>, so
	// passing the stream name lets the relay probe the nested warm path.
	// VOD flat layout doesn't need it but the lookup is the same cost.
	// The DB format is the storage contract. Warm node state is telemetry
	// and may lag seed or processing corrections, so it only fills blanks.
	desc := lookupArtifactDescriptor(context.Background(), artifactHash)
	format = selectArtifactRelayFormat(desc, format)
	if format != "" {
		kind := kindFromAssetType(contentType)
		if kind == "" {
			kind = kindFromAssetType(desc.ArtifactType)
		}
		if kind == "" {
			kind = "vod"
		}
		relayURL := buildVODRelayURL(trigger.GetNodeId(), kind, artifactHash, format, desc.StreamInternal)
		if relayURL == "" {
			// No advertised relay base URL on this node — abort so Mist
			// retries rather than receiving a 127.0.0.1 URL that would dial
			// itself in container deployments.
			p.logger.WithFields(logging.Fields{
				"artifact_hash": artifactHash,
				"stream_name":   streamName,
				"node_id":       trigger.GetNodeId(),
			}).Warn("STREAM_SOURCE: VOD artifact — no relay base URL advertised by node; aborting")
			return "", true, nil
		}

		go func(tr *pb.MistTrigger, name string) {
			if err := p.sendTriggerToDecklog(tr); err != nil {
				p.logger.WithFields(logging.Fields{
					"stream_name":  name,
					"trigger_type": tr.GetTriggerType(),
					"error":        err,
				}).Error("Failed to send stream_source trigger to Decklog")
			}
		}(trigger, streamName)

		p.logger.WithFields(logging.Fields{
			"artifact_hash": artifactHash,
			"stream_name":   streamName,
			"format":        format,
			"relay_url":     relayURL,
		}).Info("VOD STREAM_SOURCE routed through Helmsman read-through relay")
		return relayURL, false, nil
	}

	// Cross-cluster: check local foghorn.artifacts DB for adopted copy
	if artifactHash != "" && control.GetDB() != nil {
		var storageLocation string
		dbErr := control.GetDB().QueryRowContext(context.Background(), `
			SELECT COALESCE(storage_location, '')
			FROM foghorn.artifacts
			WHERE artifact_hash = $1 AND status != 'deleted' LIMIT 1
		`, artifactHash).Scan(&storageLocation)
		loc := strings.ToLower(strings.TrimSpace(storageLocation))
		if dbErr == nil && loc == "defrosting" {
			p.logger.WithField("artifact_hash", artifactHash).Debug("STREAM_SOURCE: artifact defrosting, MistServer will retry")
			return "", true, nil
		}
	}

	// Cross-cluster: if origin is remote and we don't have the artifact, trigger async defrost
	if originClusterID != "" && artifactHash != "" && contentType != "" && tenantID != "" {
		p.logger.WithFields(logging.Fields{
			"artifact_hash":  artifactHash,
			"origin_cluster": originClusterID,
			"content_type":   contentType,
		}).Info("STREAM_SOURCE: remote artifact — triggering async defrost")
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			nodeID, err := control.PickStorageNodeIDPublic()
			if err != nil {
				p.logger.WithError(err).Debug("No storage node for async remote defrost")
				return
			}
			if _, err := control.StartDefrost(ctx, contentType, artifactHash, nodeID, 30*time.Second, p.logger); err != nil {
				p.logger.WithError(err).Debug("Async remote defrost trigger failed")
			}
		}()
		return "", true, nil
	}

	p.logger.WithFields(logging.Fields{
		"artifact_hash": artifactHash,
		"stream_name":   streamName,
	}).Warn("Artifact not found")

	go func(tr *pb.MistTrigger, name string) {
		if err := p.sendTriggerToDecklog(tr); err != nil {
			p.logger.WithFields(logging.Fields{
				"stream_name":  name,
				"trigger_type": tr.GetTriggerType(),
				"error":        err,
			}).Error("Failed to send stream_source trigger to Decklog")
		}
	}(trigger, streamName)

	// Return empty to let MistServer use default source (will fail for VOD)
	return "", true, nil
}

// ValidatePullSourceURI returns true when a URI is parseable, uses a
// supported MistServer pull-input scheme, and is not in the always-blocked
// set (loopback, link-local, .internal, etc). It does NOT enforce per-
// cluster allow_private_pull_sources — that decision needs the executing
// node's logical media cluster, which the caller must look up via
// Quartermaster (resolvePullSource does this defensively below).
func ValidatePullSourceURI(uri string) bool {
	return pullsource.IsValid(uri)
}

// resolvePullSource handles STREAM_SOURCE for pull+<internal_name> streams. We
// return balance:<foghorn-base> so MistInBalancer calls /source on this Foghorn
// to pick the actual origin (active in-cluster DTSC node vs the upstream URI).
// The stored upstream URI is never embedded in the returned string — /source
// re-resolves it server-side from Commodore.
func (p *Processor) resolvePullSource(streamName string, trigger *pb.MistTrigger) (string, bool, error) {
	internalName := strings.TrimPrefix(streamName, "pull+")
	if internalName == "" {
		return "", true, nil
	}

	if control.CommodoreClient == nil {
		p.logger.WithField("stream_name", streamName).Error("Commodore client unavailable for pull source resolution")
		return "", true, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := control.CommodoreClient.ResolvePullSourceByInternalName(ctx, internalName)
	if err != nil {
		p.logger.WithFields(logging.Fields{
			"stream_name":   streamName,
			"internal_name": internalName,
			"error":         err,
		}).Warn("Failed to resolve pull source from Commodore")
		p.recordPullSourceEvent(nil, internalName, "commodore_error", err.Error())
		return "", true, nil
	}
	if resp == nil || !resp.GetFound() {
		p.logger.WithField("stream_name", streamName).Warn("Pull source not found")
		p.recordPullSourceEvent(resp, internalName, "not_found", "")
		return "", true, nil
	}
	if !resp.GetEnabled() {
		p.logger.WithField("stream_name", streamName).Info("Pull source disabled by tenant; refusing to start input")
		p.recordPullSourceEvent(resp, internalName, "disabled", "")
		return "", true, nil
	}
	class, classErr := pullsource.Classify(resp.GetSourceUri())
	if class == pullsource.ClassBlocked {
		p.logger.WithFields(logging.Fields{
			"stream_name": streamName,
			"error":       classErr,
		}).Warn("Pull source URI is not supported; refusing")
		detail := ""
		if classErr != nil {
			detail = classErr.Error()
		}
		p.recordPullSourceEvent(resp, internalName, "blocked_uri", detail)
		return "", true, nil
	}
	// Defensive placement check: the bootstrap/CLI layer + runtime CRUD
	// validators should have rejected misconfigured pulls upfront, but if
	// a stale row + new cluster policy / allowed_cluster_ids collide we
	// deny here rather than dial. Same shared helper as render / Commodore
	// apply / viewer routing / /source.
	triggerClusterID := p.resolveNodeClusterID(trigger.GetNodeId())
	localCapability := false
	if class == pullsource.ClassPrivate {
		localCapability = p.clusterAllowsPrivatePullSources(streamName, triggerClusterID)
	}
	localCandidates := []pullsource.ClusterCapability{}
	if triggerClusterID != "" {
		localCandidates = append(localCandidates, pullsource.ClusterCapability{
			ID:                      triggerClusterID,
			AllowPrivatePullSources: localCapability,
		})
	}
	eligible, rejects := pullsource.FilterPlacementClusters(class, resp.GetAllowedClusterIds(), localCandidates)
	if len(eligible) == 0 {
		detail := triggerClusterID
		if len(rejects) > 0 {
			detail = formatTriggerPlacementRejects(rejects, triggerClusterID)
		}
		p.logger.WithFields(logging.Fields{
			"stream_name": streamName,
			"cluster_id":  triggerClusterID,
			"detail":      detail,
		}).Warn("Pull source not placeable on the executing cluster; refusing")
		p.recordPullSourceEvent(resp, internalName, "cluster_not_allowed", detail)
		return "", true, nil
	}

	if tid := resp.GetTenantId(); tid != "" {
		trigger.TenantId = &tid
	}
	if sid := resp.GetStreamId(); sid != "" {
		trigger.StreamId = &sid
	}

	base := control.FoghornBalancerBase(triggerClusterID)
	if base == "" {
		p.logger.WithField("node_id", trigger.GetNodeId()).Error("Foghorn balancer base unresolved for pull source")
		p.recordPullSourceEvent(resp, internalName, "foghorn_base_unresolved", trigger.GetNodeId())
		return "", true, nil
	}

	p.logger.WithFields(logging.Fields{
		"stream_name":   streamName,
		"internal_name": internalName,
		"node_id":       trigger.GetNodeId(),
		"cluster_id":    triggerClusterID,
	}).Info("Resolved pull+ source to balance: URI; /source will pick origin")

	// No fallback param — /source resolves the upstream URI server-side from
	// Commodore. Trusting a fallback param here would be a source-injection vector.
	p.recordPullSourceEvent(resp, internalName, "resolved", triggerClusterID)
	return "balance:" + base, false, nil
}

func (p *Processor) resolveBareManagedStreamContext(streamName string) (*pb.ResolveStreamContextResponse, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	resp, err := p.commodoreClient.ResolveStreamContext(ctx, "", "", streamName, p.clusterID)
	cancel()
	if err != nil {
		return nil, "internal_name", err
	}
	if resp != nil && (resp.GetAdmitted() || resp.GetAdmissionReason() != "stream not found") {
		return resp, "internal_name", nil
	}

	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
	resp, err = p.commodoreClient.ResolveStreamContext(ctx, "", streamName, "", p.clusterID)
	cancel()
	if err != nil {
		return nil, "playback_id", err
	}
	return resp, "playback_id", nil
}

// formatTriggerPlacementRejects renders FilterPlacementClusters rejections
// into a single recordPullSourceEvent detail string for STREAM_SOURCE refusals.
// Uses the executing clusterID as fallback context for empty-list rejects.
func formatTriggerPlacementRejects(rejects []pullsource.PlacementReject, triggerClusterID string) string {
	parts := make([]string, 0, len(rejects))
	for _, r := range rejects {
		switch r.Reason {
		case pullsource.PlacementRejectEmptyForPrivate:
			parts = append(parts, fmt.Sprintf("cluster=%s reason=empty_for_private", triggerClusterID))
		case pullsource.PlacementRejectUnknownCluster:
			parts = append(parts, fmt.Sprintf("cluster=%s reason=not_in_allowed_cluster_ids", r.ClusterID))
		case pullsource.PlacementRejectMissingPrivateCapability:
			parts = append(parts, fmt.Sprintf("cluster=%s reason=missing_private_capability", r.ClusterID))
		default:
			parts = append(parts, fmt.Sprintf("cluster=%s reason=%s", r.ClusterID, r.Reason))
		}
	}
	return strings.Join(parts, ";")
}

func (p *Processor) recordPullSourceEvent(resp *pb.ResolvePullSourceByInternalNameResponse, internalName, kind, detail string) {
	if control.CommodoreClient == nil || internalName == "" || kind == "" {
		return
	}
	tenantID := tenants.SystemTenantID.String()
	streamID := ""
	if resp != nil {
		if resp.GetTenantId() != "" {
			tenantID = resp.GetTenantId()
		}
		streamID = resp.GetStreamId()
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := control.CommodoreClient.RecordPullSourceEvent(ctx, &pb.RecordPullSourceEventRequest{
			TenantId:     tenantID,
			StreamId:     streamID,
			InternalName: internalName,
			EventKind:    kind,
			Detail:       detail,
		}); err != nil {
			p.logger.WithError(err).WithFields(logging.Fields{
				"internal_name": internalName,
				"event_kind":    kind,
			}).Debug("Failed to record pull-source lifecycle event")
		}
	}()
}

// resolveProcessSource returns the Mist source URL for a processing+ stream.
//
// Safe-wrapper inputs (.mp4, .mov, .mkv, .webm, .ts, .m3u8) route through
// Helmsman's /internal/artifact/upload/<hash>.<ext> relay — Mist reads via
// HTTP::URIReader and admission biases to memory-only for sequential
// one-shot reads, so a multi-GB upload doesn't double-write disk during
// processing.
//
// Unsafe wrappers (.avi, .flv, .m4v) cannot be opened by Mist over HTTP
// (FLV is fopen-only, AV input only auto-matches local paths, .m4v has
// no http source_match). For those the processing dispatcher stages the
// upload to disk before booting Mist, and Helmsman's processing+
// STREAM_SOURCE shortcut returns the local path. Foghorn should never
// be asked to resolve an unsafe-wrapper source — if we are, the stage
// is missing and handing Mist a presigned S3 URL it can't open would
// just spin the job. Abort instead so the trigger retries.
func (p *Processor) resolveProcessSource(artifactHash, nodeID string) (string, bool, error) {
	if artifactHash == "" {
		return "", true, nil
	}
	db := control.GetDB()
	if db == nil {
		p.logger.Error("DB not available for process+ source resolution")
		return "", true, nil
	}

	var jobSourceURL sql.NullString
	err := db.QueryRowContext(context.Background(), `
		SELECT source_url
		FROM foghorn.processing_jobs
		WHERE artifact_hash = $1
		  AND status IN ('dispatched', 'processing')
		  AND source_url IS NOT NULL
		ORDER BY updated_at DESC
		LIMIT 1
	`, artifactHash).Scan(&jobSourceURL)
	if err == nil && jobSourceURL.Valid && strings.TrimSpace(jobSourceURL.String) != "" {
		sourceURL := strings.TrimSpace(jobSourceURL.String)
		p.logger.WithFields(logging.Fields{
			"artifact_hash": artifactHash,
			"node_id":       nodeID,
			"source_url":    sourceURL,
		}).Info("Resolved process+ source from processing job")
		return sourceURL, false, nil
	}
	if err != nil && err != sql.ErrNoRows {
		p.logger.WithError(err).WithField("artifact_hash", artifactHash).Warn("Failed to look up processing job source")
		return "", true, nil
	}

	var format string
	err = db.QueryRowContext(context.Background(), `
		SELECT COALESCE(format,'')
		FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND s3_url IS NOT NULL
	`, artifactHash).Scan(&format)
	if err != nil {
		p.logger.WithError(err).WithField("artifact_hash", artifactHash).Warn("Failed to look up format for process+ source")
		return "", true, nil
	}

	if isRelaySafeFormat(format) {
		relayURL := buildUploadRelayURL(nodeID, artifactHash, format)
		if relayURL == "" {
			// No relay base URL advertised by this node. Abort the
			// trigger so processing is rescheduled rather than handing
			// Mist a direct presigned-S3 URL that bypasses Helmsman
			// admission/pressure handling. Mist retries STREAM_SOURCE
			// after the abort window; Foghorn can pick another node
			// (or the operator fixes the missing config).
			p.logger.WithFields(logging.Fields{
				"artifact_hash": artifactHash,
				"node_id":       nodeID,
				"format":        format,
			}).Warn("process+ source: no relay base URL advertised by node; aborting")
			return "", true, nil
		}
		p.logger.WithFields(logging.Fields{
			"artifact_hash": artifactHash,
			"node_id":       nodeID,
			"format":        format,
			"relay_url":     relayURL,
		}).Info("Resolved process+ source to Helmsman upload relay")
		return relayURL, false, nil
	}

	p.logger.WithFields(logging.Fields{
		"artifact_hash": artifactHash,
		"node_id":       nodeID,
		"format":        format,
	}).Warn("process+ source: unsafe-wrapper input not staged locally by Helmsman; aborting so Mist retries")
	return "", true, nil
}

// handleStreamProcess returns cached MistServer process config for the
// STREAM_PROCESS trigger. MistServer's MistProc pipeline only runs in
// live / realtime modes — there is no VOD-mode processing. Stream-name
// support matrix:
//   - live+ / pull+   : live ingest. Config populated during
//     PUSH_REWRITE from ValidateStreamKeyResponse.
//   - dvr+<internal>  : rolling-DVR playback surface. Config resolves
//     from the dvr_processes_json snapshot stamped
//     onto foghorn.artifacts at StartDVR — that's the
//     durable authority across Foghorn restarts and
//     cache TTL, not the in-flight live cache.
//   - processing+     : simulated-live processing pipeline. Config
//     populated by ProcessingDispatcher (regular VOD
//     processing) or the chapter finalization queue
//     before job dispatch.
//   - vod+            : read-only file playback. MistProc does not
//     support VOD mode — NEVER returns config here.
func (p *Processor) handleStreamProcess(trigger *pb.MistTrigger) (string, bool, error) {
	streamName := trigger.GetStreamProcess().GetStreamName()
	internalName := mist.ExtractInternalName(streamName)

	p.logger.WithFields(logging.Fields{
		"stream_name":   streamName,
		"internal_name": internalName,
		"node_id":       trigger.GetNodeId(),
	}).Debug("Processing STREAM_PROCESS trigger")

	if p.streamCache == nil {
		return "", false, nil
	}

	// vod+ is the read-only playback path — Mist has no VOD-mode
	// MistProc. Return empty config explicitly so nothing tries to
	// boot Thumbs/sprite/Livepeer for it.
	if strings.HasPrefix(streamName, "vod+") {
		return "", false, nil
	}

	if val, ok := p.streamCache.Peek("process:" + internalName); ok {
		processesJSON, _ := val.(string)
		return processesJSON, false, nil
	}

	if strings.HasPrefix(streamName, "processing+") {
		if cfg := p.resolveProcessingProcessConfig(internalName); cfg != "" {
			return cfg, false, nil
		}
	}

	// dvr+<dvr_internal_name>: the durable answer is the dvr_processes_json
	// snapshot stamped onto foghorn.artifacts at StartDVR. The streamCache
	// only carries the in-flight live config; the snapshot is what
	// guarantees DVR-specific Thumbs/sprite tracks survive Foghorn
	// restarts and cache TTL.
	if strings.HasPrefix(streamName, "dvr+") {
		if cfg := p.resolveRollingDVRProcessConfig(internalName); cfg != "" {
			return cfg, false, nil
		}
	}

	return "", false, nil
}

func (p *Processor) resolveProcessingProcessConfig(artifactHash string) string {
	db := control.GetDB()
	if db == nil || artifactHash == "" {
		return ""
	}
	var processesJSON sql.NullString
	if err := db.QueryRowContext(context.Background(),
		`SELECT processes_json::text
		   FROM foghorn.processing_jobs
		  WHERE artifact_hash = $1
		    AND status IN ('queued', 'dispatched', 'processing')
		  ORDER BY created_at DESC
		  LIMIT 1`,
		artifactHash,
	).Scan(&processesJSON); err != nil {
		return ""
	}
	if !processesJSON.Valid {
		return ""
	}
	cfg := strings.TrimSpace(processesJSON.String)
	if cfg == "" {
		return ""
	}
	cfg = p.SubstituteGatewayURL(cfg, nil)
	p.CacheProcessConfig(artifactHash, cfg)
	return cfg
}

// resolveRollingDVRProcessConfig returns the DVR lifecycle processes_json
// snapshot stored on the DVR artifact row at StartDVR. The snapshot
// is the authority: it survives Foghorn restarts and cache TTL expiry
// that the PUSH_REWRITE-populated streamCache does not. The in-memory
// cache is only a fast path for live ingest — dvr+ playback must not
// depend on it.
func (p *Processor) resolveRollingDVRProcessConfig(dvrInternalName string) string {
	db := control.GetDB()
	if db == nil || dvrInternalName == "" {
		return ""
	}
	var processesJSON sql.NullString
	if err := db.QueryRowContext(context.Background(),
		`SELECT dvr_processes_json
		   FROM foghorn.artifacts
		  WHERE internal_name = $1
		    AND artifact_type = 'dvr'`,
		dvrInternalName,
	).Scan(&processesJSON); err != nil {
		return ""
	}
	if !processesJSON.Valid {
		return ""
	}
	return processesJSON.String
}

// handlePushEnd processes PUSH_END trigger (non-blocking)
func (p *Processor) handlePushEnd(trigger *pb.MistTrigger) (string, bool, error) {
	payload, ok := trigger.GetTriggerPayload().(*pb.MistTrigger_PushEnd)
	if !ok {
		return "", false, fmt.Errorf("unexpected payload type for PushEnd: %T", trigger.GetTriggerPayload())
	}
	pushEnd := payload.PushEnd
	internalName := mist.ExtractInternalName(pushEnd.GetStreamName())

	// processing+ push completions are handled sidecar-side (signals job handler)
	if strings.HasPrefix(pushEnd.GetStreamName(), "processing+") {
		return "", false, nil
	}

	p.applyStreamContext(trigger, internalName)
	if streamID := trigger.GetStreamId(); streamID != "" {
		pushEnd.StreamId = &streamID
	}

	var decklogErr error

	// Send enriched trigger to Decklog
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"push_id":       pushEnd.GetPushId(),
			"trigger_type":  trigger.GetTriggerType(),
			"error":         err,
		}).Error("Failed to send push end trigger to Decklog")
		decklogErr = err
	}

	// Update multistream push target status based on push result
	targetURI := pushEnd.GetTargetUriAfter()
	if targetURI == "" {
		targetURI = pushEnd.GetTargetUriBefore()
	}
	if targetURI != "" {
		status := "idle"
		var lastErr *string
		if pushEnd.GetPushStatus() != "" && pushEnd.GetPushStatus() != "0" {
			status = "failed"
			logMsg := pushEnd.GetLogMessages()
			if logMsg != "" {
				lastErr = &logMsg
			}
		}
		go p.updatePushTargetStatus(pushEnd.GetStreamName(), targetURI, status, lastErr)
	}

	if decklogErr != nil && shouldSurfaceDecklogError(trigger) {
		return "", false, decklogErr
	}
	return "", false, nil
}

// handlePushOutStart processes PUSH_OUT_START trigger (blocking)
func (p *Processor) handlePushOutStart(trigger *pb.MistTrigger) (string, bool, error) {
	payload, ok := trigger.GetTriggerPayload().(*pb.MistTrigger_PushOutStart)
	if !ok {
		return "", false, fmt.Errorf("unexpected payload type for PushOutStart: %T", trigger.GetTriggerPayload())
	}
	pushOutStart := payload.PushOutStart
	// nodeID is available via trigger.GetNodeId() and flows to Decklog with the full trigger
	internalName := mist.ExtractInternalName(pushOutStart.GetStreamName())

	p.applyStreamContext(trigger, internalName)
	if streamID := trigger.GetStreamId(); streamID != "" {
		pushOutStart.StreamId = &streamID
	}

	// Control-plane stream lifecycle is derived from Decklog events.

	// Send enriched trigger to Decklog (Data Plane)
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"push_target":   pushOutStart.GetPushTarget(),
			"trigger_type":  trigger.GetTriggerType(),
			"error":         err,
		}).Error("Failed to send push out start trigger to Decklog")
	}

	// Update multistream push target status to "pushing"
	go p.updatePushTargetStatus(pushOutStart.GetStreamName(), pushOutStart.GetPushTarget(), "pushing", nil)

	return pushOutStart.GetPushTarget(), false, nil
}

// handleUserNew processes USER_NEW trigger (blocking)
func (p *Processor) handleUserNew(trigger *pb.MistTrigger) (string, bool, error) {
	payload, ok := trigger.GetTriggerPayload().(*pb.MistTrigger_ViewerConnect)
	if !ok {
		return "", false, fmt.Errorf("unexpected payload type for ViewerConnect: %T", trigger.GetTriggerPayload())
	}
	userNew := payload.ViewerConnect
	internalName := mist.ExtractInternalName(userNew.GetStreamName())
	p.logger.WithFields(logging.Fields{
		"session_id":      userNew.GetSessionId(),
		"internal_name":   internalName,
		"connection_addr": userNew.GetHost(),
		"connector":       userNew.GetConnector(),
		"node_id":         trigger.GetNodeId(),
	}).Debug("Processing USER_NEW trigger")

	if !mist.IsPlaybackViewerRequest(userNew.GetConnector(), userNew.GetRequestUrl()) {
		p.logger.WithFields(logging.Fields{
			"session_id":    userNew.GetSessionId(),
			"internal_name": internalName,
			"connector":     userNew.GetConnector(),
			"node_id":       trigger.GetNodeId(),
		}).Debug("Ignoring non-viewer USER_NEW connector")
		return "true", false, nil
	}

	info := p.applyStreamContext(trigger, userNew.GetStreamName())
	if streamID := trigger.GetStreamId(); streamID != "" {
		userNew.StreamId = &streamID
	}
	if info.OriginClusterID != "" {
		trigger.OriginClusterId = &info.OriginClusterID
	}

	// Viewer-side load gate: when the broadcaster is on free tier and the
	// serving cluster is under load, deny new viewers. Over-allowance free
	// streams are gated sooner (80%) than within-allowance free streams
	// (95% redline). Paying broadcasters' viewers are always admitted. The
	// broadcaster's allowance state was cached in streamContext at
	// PUSH_REWRITE time so no Commodore round-trip is needed here.
	//
	// Load is evaluated on the serving cluster (where the viewer hits an
	// edge) — that's where the egress bandwidth and CPU live. trigger
	// ClusterId is set by the firing edge; fall back to the local Foghorn
	// instance's cluster, then to the stream's origin cluster.
	viewerCluster := strings.TrimSpace(trigger.GetClusterId())
	if viewerCluster == "" {
		viewerCluster = strings.TrimSpace(p.clusterID)
	}
	if viewerCluster == "" {
		viewerCluster = info.OriginClusterID
	}
	if reason, blocked := p.evaluateViewerAdmission(info, viewerCluster); blocked {
		p.logger.WithFields(logging.Fields{
			"session_id":    userNew.GetSessionId(),
			"internal_name": internalName,
			"tenant_id":     info.TenantID,
			"cluster_id":    viewerCluster,
			"reason":        reason,
		}).Warn("Rejecting viewer: free-tier under cluster load")
		return "false", false, nil
	}

	// Per-tenant concurrent-viewer cap. Hard limit, independent of cluster
	// load. Set semantics on session_id makes re-fires (USER_NEW after
	// reconnect with the same session_id) idempotent — already-tracked
	// sessions are admitted without consuming a fresh slot. Cap value is the
	// broadcaster's tenant max_viewers, cached in streamContext at
	// PUSH_REWRITE.
	if info.TenantID != "" && info.MaxViewers > 0 {
		tc := state.DefaultTenantCapacity()
		sessionID := userNew.GetSessionId()
		current := tc.CountViewers(info.TenantID)
		alreadyTracked := tc.HasViewer(info.TenantID, sessionID)
		if !alreadyTracked && int32(current) >= info.MaxViewers {
			p.logger.WithFields(logging.Fields{
				"session_id":  sessionID,
				"tenant_id":   info.TenantID,
				"current":     current,
				"max_viewers": info.MaxViewers,
			}).Warn("Rejecting viewer: tenant concurrent-viewer cap reached")
			return "false", false, nil
		}
		tc.RegisterViewer(info.TenantID, sessionID)
	}

	// Enrich ViewerConnect payload directly
	userNew.NodeId = func() *string { s := trigger.GetNodeId(); return &s }()

	// Add viewer geographic data from GeoIP if available (bucketized)
	if p.geoipClient != nil && userNew.GetHost() != "" {
		if geoData := geoip.LookupCached(context.Background(), p.geoipClient, p.geoipCache, userNew.GetHost()); geoData != nil {
			userNew.ClientCountry = &geoData.CountryCode
			userNew.ClientCity = &geoData.City
			if bucket, centLat, centLon, ok := geo.Bucket(geoData.Latitude, geoData.Longitude); ok {
				userNew.ClientLatitude = &centLat
				userNew.ClientLongitude = &centLon
				userNew.ClientBucket = bucket
			}
			// keep node bucket if available
			if nodeCfg := p.getNodeConfig(trigger.GetNodeId()); nodeCfg != nil {
				if bucket, _, _, ok := geo.Bucket(nodeCfg.Latitude, nodeCfg.Longitude); ok {
					userNew.NodeBucket = bucket
				}
			}

			p.logger.WithFields(logging.Fields{
				"connection_ip": userNew.GetHost(),
				"country_code":  geoData.CountryCode,
				"city":          geoData.City,
				"session_id":    userNew.GetSessionId(),
			}).Debug("Enriched USER_NEW with connection geo data (bucketized)")
		}
	}
	// Note: Client IP redaction now happens at API layer (GraphQL resolvers, Signalman)
	// Raw IP in 'host' field is preserved for ClickHouse storage and future analysis

	// Send enriched trigger to Decklog
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"session_id":    userNew.GetSessionId(),
			"internal_name": internalName,
			"trigger_type":  trigger.GetTriggerType(),
			"error":         err,
		}).Error("Failed to send user connection trigger to Decklog")
	}

	clientIP := userNew.GetHost()
	correlationID := extractCorrelationID(userNew.GetRequestUrl())
	if attached := state.DefaultManager().AttachVirtualViewerSession(
		correlationID,
		trigger.GetNodeId(),
		internalName,
		clientIP,
		userNew.GetSessionId(),
	); attached {
		p.logger.WithFields(logging.Fields{
			"node_id":       trigger.GetNodeId(),
			"internal_name": internalName,
			"client_ip":     clientIP,
			"session_id":    userNew.GetSessionId(),
		}).Debug("Attached Mist session to active playback viewer")
	}

	decision, err := p.enforcePlaybackPolicy(context.Background(), internalName, info, userNew)
	if err != nil {
		p.logger.WithError(err).WithField("internal_name", internalName).Error("playback policy enforcement errored; denying")
		return "false", false, nil
	}
	return decision, false, nil
}

// handleStreamBuffer processes STREAM_BUFFER trigger (non-blocking)
// Forwards the original StreamBufferTrigger to Decklog with full track data and health metrics.
func (p *Processor) handleStreamBuffer(trigger *pb.MistTrigger) (string, bool, error) {
	// Extract StreamBuffer payload from protobuf
	payload, ok := trigger.GetTriggerPayload().(*pb.MistTrigger_StreamBuffer)
	if !ok {
		return "", false, fmt.Errorf("unexpected payload type for StreamBuffer: %T", trigger.GetTriggerPayload())
	}
	streamBuffer := payload.StreamBuffer

	p.logger.WithFields(logging.Fields{
		"internal_name":    streamBuffer.GetStreamName(),
		"buffer_state":     streamBuffer.GetBufferState(),
		"track_count":      len(streamBuffer.GetTracks()),
		"stream_buffer_ms": streamBuffer.GetStreamBufferMs(),
		"stream_jitter_ms": streamBuffer.GetStreamJitterMs(),
		"mist_issues":      streamBuffer.GetMistIssues(),
		"node_id":          trigger.GetNodeId(),
	}).Debug("Processing STREAM_BUFFER trigger")

	// Control-plane stream lifecycle is derived from Decklog events.

	info := p.applyStreamContext(trigger, streamBuffer.GetStreamName())
	if streamID := trigger.GetStreamId(); streamID != "" {
		streamBuffer.StreamId = &streamID
	}

	// Update state from buffer first (this sets StartedAt on first buffer event)
	// CRITICAL: Extract internal name from stream name (e.g., "live+demo_stream" -> "demo_stream")
	// to avoid creating duplicate state entries for the same logical stream
	internalName := mist.ExtractInternalName(streamBuffer.GetStreamName())
	_ = state.DefaultManager().UpdateStreamFromBuffer(
		streamBuffer.GetStreamName(),
		internalName,
		trigger.GetNodeId(),
		info.TenantID,
		streamBuffer.GetBufferState(),
		"",
	)

	// Forward original StreamBufferTrigger to Decklog (preserves all track data and health metrics)
	// Helmsman already enriched it with has_issues, issues_description, quality_tier, etc.
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"internal_name": streamBuffer.GetStreamName(),
			"trigger_type":  trigger.GetTriggerType(),
			"error":         err,
		}).Error("Failed to send stream buffer trigger to Decklog")
	}

	return "", false, nil
}

// handleStreamEnd processes STREAM_END trigger (non-blocking)
func (p *Processor) handleStreamEnd(trigger *pb.MistTrigger) (string, bool, error) {
	// Extract StreamEnd payload from protobuf
	payload, ok := trigger.GetTriggerPayload().(*pb.MistTrigger_StreamEnd)
	if !ok {
		return "", false, fmt.Errorf("unexpected payload type for StreamEnd: %T", trigger.GetTriggerPayload())
	}
	streamEnd := payload.StreamEnd
	// CRITICAL: Extract internal name to match state keys
	internalName := mist.ExtractInternalName(streamEnd.GetStreamName())
	nodeID := trigger.GetNodeId()

	p.logger.WithFields(logging.Fields{
		"internal_name": internalName,
		"node_id":       nodeID,
	}).Debug("Processing STREAM_END trigger")

	// Control-plane stream lifecycle is derived from Decklog events.

	_ = p.applyStreamContext(trigger, internalName)
	streamEnd.NodeId = &nodeID
	if streamID := trigger.GetStreamId(); streamID != "" {
		streamEnd.StreamId = &streamID
	}

	var decklogErr error

	// Send enriched trigger to Decklog
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"trigger_type":  trigger.GetTriggerType(),
			"error":         err,
		}).Error("Failed to send stream end trigger to Decklog")
		decklogErr = err
	}

	// Update state offline
	state.DefaultManager().SetOffline(internalName, nodeID)

	// Decrement the broadcaster's concurrent-stream count. Idempotent: a
	// stream not in the set is a no-op, so duplicate STREAM_END fires are
	// safe. TenantID may be empty when streamContext is missing (e.g. cache
	// expired before STREAM_END arrived); the helper short-circuits in that
	// case.
	state.DefaultTenantCapacity().UnregisterStream(trigger.GetTenantId(), internalName)

	// Broadcast stream-offline to federated peers + clean up stream-scoped peers
	if p.peerNotifier != nil {
		p.peerNotifier.BroadcastStreamLifecycle(internalName, trigger.GetTenantId(), false)
		p.peerNotifier.UntrackStream(internalName)
	}

	// Stop DVR on its storage node if active
	control.StopDVRByInternalName(internalName, p.logger)

	// Deactivate multistream push targets on the origin node
	go p.deactivatePushTargets(nodeID, streamEnd.GetStreamName())

	if decklogErr != nil && shouldSurfaceDecklogError(trigger) {
		return "", false, decklogErr
	}
	return "", false, nil
}

// handleUserEnd processes USER_END trigger (non-blocking)
func (p *Processor) handleUserEnd(trigger *pb.MistTrigger) (string, bool, error) {
	payload, ok := trigger.GetTriggerPayload().(*pb.MistTrigger_ViewerDisconnect)
	if !ok {
		return "", false, fmt.Errorf("unexpected payload type for ViewerDisconnect: %T", trigger.GetTriggerPayload())
	}
	userEnd := payload.ViewerDisconnect
	internalStreamName := mist.ExtractInternalName(userEnd.GetStreamName())
	p.logger.WithFields(logging.Fields{
		"session_id":        userEnd.GetSessionId(),
		"internal_name":     userEnd.GetStreamName(),
		"connection_addr":   userEnd.GetHost(),
		"seconds_connected": userEnd.GetDuration(),
		"uploaded_bytes":    userEnd.GetUpBytes(),
		"downloaded_bytes":  userEnd.GetDownBytes(),
		"connector":         userEnd.GetConnector(),
		"node_id":           trigger.GetNodeId(),
	}).Debug("Processing USER_END trigger")

	if !mist.IsPlaybackViewerRequest(userEnd.GetConnector(), "") &&
		!state.DefaultManager().HasActiveVirtualViewerSession(userEnd.GetSessionId(), trigger.GetNodeId(), internalStreamName) {
		p.logger.WithFields(logging.Fields{
			"session_id":    userEnd.GetSessionId(),
			"internal_name": internalStreamName,
			"connector":     userEnd.GetConnector(),
			"node_id":       trigger.GetNodeId(),
		}).Debug("Ignoring non-viewer USER_END connector")
		return "", false, nil
	}

	info := p.applyStreamContext(trigger, userEnd.GetStreamName())
	if streamID := trigger.GetStreamId(); streamID != "" {
		userEnd.StreamId = &streamID
	}
	if info.OriginClusterID != "" {
		trigger.OriginClusterId = &info.OriginClusterID
	}

	// Decrement the broadcaster's concurrent-viewer count. Set semantics
	// make duplicate USER_END fires safe (no-op for unknown sessions).
	state.DefaultTenantCapacity().UnregisterViewer(info.TenantID, userEnd.GetSessionId())

	userEnd.NodeId = func() *string { s := trigger.GetNodeId(); return &s }()

	// Add viewer geographic data from GeoIP if available (bucketized)
	if p.geoipClient != nil && userEnd.GetHost() != "" {
		if geoData := geoip.LookupCached(context.Background(), p.geoipClient, p.geoipCache, userEnd.GetHost()); geoData != nil {
			userEnd.CountryCode = &geoData.CountryCode
			userEnd.City = &geoData.City
			if bucket, centLat, centLon, ok := geo.Bucket(geoData.Latitude, geoData.Longitude); ok {
				userEnd.Latitude = &centLat
				userEnd.Longitude = &centLon
				userEnd.ClientBucket = bucket
			}
			if nodeCfg := p.getNodeConfig(trigger.GetNodeId()); nodeCfg != nil {
				if bucket, _, _, ok := geo.Bucket(nodeCfg.Latitude, nodeCfg.Longitude); ok {
					userEnd.NodeBucket = bucket
				}
			}

			p.logger.WithFields(logging.Fields{
				"connection_ip": userEnd.GetHost(),
				"country_code":  geoData.CountryCode,
				"city":          geoData.City,
				"session_id":    userEnd.GetSessionId(),
			}).Debug("Enriched USER_END with connection geo data (bucketized)")
		}
	}

	var decklogErr error

	// Send enriched trigger to Decklog
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"session_id":    userEnd.GetSessionId(),
			"internal_name": userEnd.GetStreamName(),
			"trigger_type":  trigger.GetTriggerType(),
			"error":         err,
		}).Error("Failed to send user disconnect trigger to Decklog")
		decklogErr = err
	}

	clientIP := userEnd.GetHost()
	if disconnected := state.DefaultManager().DisconnectVirtualViewerBySessionID(userEnd.GetSessionId(), trigger.GetNodeId(), internalStreamName, clientIP); disconnected {
		state.DefaultManager().UpdateUserConnection(internalStreamName, trigger.GetNodeId(), info.TenantID, -1)
	}

	if decklogErr != nil && shouldSurfaceDecklogError(trigger) {
		return "", false, decklogErr
	}
	return "", false, nil
}

func extractCorrelationID(requestURL string) string {
	if requestURL == "" {
		return ""
	}
	parsedURL, err := url.Parse(requestURL)
	if err != nil {
		return ""
	}
	return parsedURL.Query().Get("fwcid")
}

// handleLiveTrackList processes LIVE_TRACK_LIST trigger (non-blocking)
func (p *Processor) handleLiveTrackList(trigger *pb.MistTrigger) (string, bool, error) {
	// Extract LiveTrackList payload from protobuf
	payload, ok := trigger.GetTriggerPayload().(*pb.MistTrigger_TrackList)
	if !ok {
		return "", false, fmt.Errorf("unexpected payload type for TrackList: %T", trigger.GetTriggerPayload())
	}
	liveTrackList := payload.TrackList
	// CRITICAL: Extract internal name to match state keys
	internalName := mist.ExtractInternalName(liveTrackList.GetStreamName())
	nodeID := trigger.GetNodeId()
	tracks := liveTrackList.GetTracks()

	p.logger.WithFields(logging.Fields{
		"internal_name": internalName,
		"node_id":       nodeID,
	}).Debug("Processing LIVE_TRACK_LIST trigger")

	// Track list is now structured data
	p.logger.WithFields(logging.Fields{
		"internal_name": internalName,
		"track_count":   len(tracks),
		"quality_tier":  liveTrackList.GetQualityTier(),
	}).Debug("Processing structured LIVE_TRACK_LIST")

	// Quality metrics are available but we send raw trackListJSON to protobuf

	info := p.applyStreamContext(trigger, internalName)
	if streamID := trigger.GetStreamId(); streamID != "" {
		liveTrackList.StreamId = &streamID
	}

	// Send enriched trigger to Decklog
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"trigger_type":  trigger.GetTriggerType(),
			"error":         err,
		}).Error("Failed to send track list trigger to Decklog")
	}

	// Update state track list - using empty JSON string since we have structured data
	state.DefaultManager().UpdateTrackList(internalName, nodeID, info.TenantID, "")

	return "", false, nil
}

// handleRecordingEnd processes RECORDING_END trigger (non-blocking)
func (p *Processor) handleRecordingEnd(trigger *pb.MistTrigger) (string, bool, error) {
	// Extract RecordingEnd payload from protobuf
	payload, ok := trigger.GetTriggerPayload().(*pb.MistTrigger_RecordingComplete)
	if !ok {
		return "", false, fmt.Errorf("unexpected payload type for RecordingComplete: %T", trigger.GetTriggerPayload())
	}
	recordingEnd := payload.RecordingComplete
	internalName := mist.ExtractInternalName(recordingEnd.GetStreamName())
	nodeID := trigger.GetNodeId()

	p.logger.WithFields(logging.Fields{
		"internal_name":     internalName,
		"file_path":         recordingEnd.GetFilePath(),
		"output_protocol":   recordingEnd.GetOutputProtocol(),
		"bytes_written":     recordingEnd.GetBytesWritten(),
		"seconds_writing":   recordingEnd.GetSecondsWriting(),
		"time_started":      recordingEnd.GetTimeStarted(),
		"time_ended":        recordingEnd.GetTimeEnded(),
		"media_duration_ms": recordingEnd.GetMediaDurationMs(),
		"node_id":           nodeID,
	}).Debug("Processing RECORDING_END trigger")

	// Control-plane recording lifecycle is derived from Decklog events.

	_ = p.applyStreamContext(trigger, internalName)
	if streamID := trigger.GetStreamId(); streamID != "" {
		recordingEnd.StreamId = &streamID
	}

	// Send enriched trigger to Decklog
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"trigger_type":  trigger.GetTriggerType(),
			"error":         err,
		}).Error("Failed to send recording trigger to Decklog")
		if shouldSurfaceDecklogError(trigger) {
			return "", false, err
		}
	}

	return "", false, nil
}

// handleRecordingSegment processes RECORDING_SEGMENT trigger (non-blocking)
func (p *Processor) handleRecordingSegment(trigger *pb.MistTrigger) (string, bool, error) {
	// Extract RecordingSegment payload from protobuf
	payload, ok := trigger.GetTriggerPayload().(*pb.MistTrigger_RecordingSegment)
	if !ok {
		return "", false, fmt.Errorf("unexpected payload type for RecordingSegment: %T", trigger.GetTriggerPayload())
	}
	seg := payload.RecordingSegment
	internalName := mist.ExtractInternalName(seg.GetStreamName())

	// Enrich tenant context before forwarding
	info := p.applyStreamContext(trigger, internalName)
	if streamID := trigger.GetStreamId(); streamID != "" {
		seg.StreamId = &streamID
	}

	p.logger.WithFields(logging.Fields{
		"internal_name": internalName,
		"file_path":     seg.GetFilePath(),
		"duration_ms":   seg.GetDurationMs(),
		"node_id":       trigger.GetNodeId(),
		"tenant_id":     info.TenantID,
	}).Debug("Processing RECORDING_SEGMENT trigger")

	// Forward the enriched trigger to Decklog for analytics/billing
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithError(err).WithFields(logging.Fields{
			"internal_name": internalName,
			"node_id":       trigger.GetNodeId(),
		}).Error("Failed to send RECORDING_SEGMENT trigger to Decklog")
		if shouldSurfaceDecklogError(trigger) {
			return "", false, err
		}
	}

	return "", false, nil
}

// handleStreamLifecycleUpdate forwards StreamLifecycleUpdate to Decklog and updates state
func (p *Processor) handleStreamLifecycleUpdate(trigger *pb.MistTrigger) (string, bool, error) {
	payload, ok := trigger.GetTriggerPayload().(*pb.MistTrigger_StreamLifecycleUpdate)
	if !ok {
		return "", false, fmt.Errorf("unexpected payload type for StreamLifecycleUpdate: %T", trigger.GetTriggerPayload())
	}
	slu := payload.StreamLifecycleUpdate
	internal := mist.ExtractInternalName(slu.GetInternalName())
	nodeID := slu.GetNodeId()

	// Enrich tenant context before forwarding (same pattern as handleStreamEnd)
	info := p.applyStreamContext(trigger, internal)
	if info.TenantID != "" && slu.TenantId == nil {
		slu.TenantId = &info.TenantID
	}
	if slu.StreamId == nil || *slu.StreamId == "" {
		if streamID := trigger.GetStreamId(); streamID != "" {
			slu.StreamId = &streamID
		}
	}
	if slu.StreamId == nil || *slu.StreamId == "" {
		p.logger.WithFields(logging.Fields{
			"internal_name": internal,
			"trigger_type":  trigger.GetTriggerType(),
		}).Warn("StreamLifecycleUpdate missing stream_id")
	}

	// Enrich with StartedAt from state manager (for duration calculation)
	// State manager tracks when stream first went live
	if streamState := state.DefaultManager().GetStreamState(internal); streamState != nil {
		if streamState.StartedAt != nil && slu.StartedAt == nil {
			startedAtUnix := streamState.StartedAt.Unix()
			slu.StartedAt = &startedAtUnix
		}
		viewers := uint32(streamState.Viewers)
		slu.TotalViewers = &viewers
	}

	// Forward the enriched StreamLifecycleUpdate to Decklog
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"internal_name": internal,
			"trigger_type":  trigger.GetTriggerType(),
			"error":         err,
		}).Error("Failed to send stream lifecycle update to Decklog")
	}

	if slu.GetStatus() == "offline" {
		state.DefaultManager().SetOffline(internal, nodeID)
	} else {
		// Update stream stats in state manager for load balancing
		// This is critical: the balancer requires inputs > 0 to consider a node for playback
		total := int(slu.GetTotalViewers())
		inputs := int(slu.GetTotalInputs())
		up := int64(slu.GetUploadedBytes())
		down := int64(slu.GetDownloadedBytes())
		replicated := slu.GetReplicated()
		state.DefaultManager().UpdateNodeStats(internal, nodeID, total, inputs, up, down, replicated)
	}
	return "", false, nil
}

// handleClientLifecycleUpdate enriches ClientLifecycleUpdate and queues it for
// batched forwarding to Decklog.
func (p *Processor) handleClientLifecycleUpdate(trigger *pb.MistTrigger) (string, bool, error) {
	payload, ok := trigger.GetTriggerPayload().(*pb.MistTrigger_ClientLifecycleUpdate)
	if !ok {
		return "", false, fmt.Errorf("unexpected payload type for ClientLifecycleUpdate: %T", trigger.GetTriggerPayload())
	}
	clu := payload.ClientLifecycleUpdate
	internal := clu.GetInternalName()

	// Enrich tenant context before forwarding (same pattern as handleUserNew/handleUserEnd)
	info := p.applyStreamContext(trigger, internal)
	if info.TenantID != "" && clu.TenantId == nil {
		clu.TenantId = &info.TenantID
	}
	if clu.StreamId == nil || *clu.StreamId == "" {
		if streamID := trigger.GetStreamId(); streamID != "" {
			clu.StreamId = &streamID
		}
	}
	if clu.StreamId == nil || *clu.StreamId == "" {
		p.logger.WithFields(logging.Fields{
			"internal_name": internal,
			"trigger_type":  trigger.GetTriggerType(),
		}).Warn("Dropping client lifecycle update without stream_id")
		return "", false, nil
	}

	// Refuse to batch samples that failed tenant enrichment; tenant_id is the
	// authoritative scoping field and missing it would silently pollute another
	// tenant's QoE rollup. This preserves the prior single-event drop semantics.
	if clu.GetTenantId() == "" {
		p.logger.WithFields(logging.Fields{
			"internal_name": internal,
			"trigger_type":  trigger.GetTriggerType(),
		}).Warn("Dropping client lifecycle update without tenant_id")
		return "", false, nil
	}

	// Buffer the enriched sample. The batcher flushes per (tenant, stream, node)
	// on size or age. Add() never blocks the processor; send failures are
	// dropped as lossy QoE telemetry rather than back-pressuring MistServer
	// triggers.
	p.getClientBatcher().Add(clu)
	return "", false, nil
}

// handleNodeLifecycleUpdate processes NODE_LIFECYCLE_UPDATE triggers using protobuf directly
func (p *Processor) handleNodeLifecycleUpdate(trigger *pb.MistTrigger) (string, bool, error) {
	payload, ok := trigger.GetTriggerPayload().(*pb.MistTrigger_NodeLifecycleUpdate)
	if !ok {
		return "", false, fmt.Errorf("unexpected payload type for NodeLifecycleUpdate: %T", trigger.GetTriggerPayload())
	}
	nu := payload.NodeLifecycleUpdate

	p.logger.WithFields(logging.Fields{
		"node_id":    nu.GetNodeId(),
		"is_healthy": nu.GetIsHealthy(),
		"bw_limit":   nu.GetBwLimit(),
		"ram_max":    nu.GetRamMax(),
		"location":   nu.GetLocation(),
	}).Info("Received NodeLifecycleUpdate from Helmsman")

	// Parse latitude/longitude for state manager
	var latitude, longitude *float64
	if geo.IsValidLatLon(nu.GetLatitude(), nu.GetLongitude()) {
		lat := nu.GetLatitude()
		lon := nu.GetLongitude()
		latitude = &lat
		longitude = &lon
	}

	// Update node heartbeat and info in state manager
	state.DefaultManager().TouchNode(nu.GetNodeId(), nu.GetIsHealthy())
	state.DefaultManager().SetNodeInfo(nu.GetNodeId(), nu.GetBaseUrl(), nu.GetIsHealthy(), latitude, longitude, nu.GetLocation(), nu.GetOutputsJson(), nil)

	// Log mismatch between Helmsman-reported mode and Foghorn-authoritative mode.
	// Foghorn owns operational mode; Helmsman's heartbeat is confirmation only.
	if reportedMode, ok := mapOperationalMode(nu.GetOperationalMode()); ok {
		authoritativeMode := state.DefaultManager().GetNodeOperationalMode(nu.GetNodeId())
		if authoritativeMode != reportedMode {
			p.logger.WithFields(logging.Fields{
				"node_id":            nu.GetNodeId(),
				"reported_mode":      reportedMode,
				"authoritative_mode": authoritativeMode,
				"trigger_id":         trigger.GetRequestId(),
			}).Warn("Helmsman reported mode differs from Foghorn authoritative mode (may need ConfigSeed push)")
		}
	}

	// Update node metrics using protobuf data directly
	state.DefaultManager().UpdateNodeMetrics(nu.GetNodeId(), struct {
		CPU                  float64
		RAMMax               float64
		RAMCurrent           float64
		UpSpeed              float64
		DownSpeed            float64
		BWLimit              float64
		CapIngest            bool
		CapEdge              bool
		CapStorage           bool
		CapProcessing        bool
		Roles                []string
		StorageCapacityBytes uint64
		StorageUsedBytes     uint64
		MaxTranscodes        int
		CurrentTranscodes    int
	}{
		CPU:           float64(nu.GetCpuTenths()) / 10.0,
		RAMMax:        float64(nu.GetRamMax()),
		RAMCurrent:    float64(nu.GetRamCurrent()),
		UpSpeed:       float64(nu.GetUpSpeed()),
		DownSpeed:     float64(nu.GetDownSpeed()),
		BWLimit:       float64(nu.GetBwLimit()),
		CapIngest:     nu.GetCapabilities() != nil && nu.GetCapabilities().GetIngest(),
		CapEdge:       nu.GetCapabilities() != nil && nu.GetCapabilities().GetEdge(),
		CapStorage:    nu.GetCapabilities() != nil && nu.GetCapabilities().GetStorage(),
		CapProcessing: nu.GetCapabilities() != nil && nu.GetCapabilities().GetProcessing(),
		Roles: func() []string {
			if nu.GetCapabilities() == nil {
				return nil
			}
			return nu.GetCapabilities().GetRoles()
		}(),
		StorageCapacityBytes: func() uint64 {
			if nu.GetLimits() == nil {
				return 0
			}
			return nu.GetLimits().GetStorageCapacityBytes()
		}(),
		StorageUsedBytes: func() uint64 {
			if nu.GetLimits() == nil {
				return 0
			}
			return nu.GetLimits().GetStorageUsedBytes()
		}(),
		MaxTranscodes: func() int {
			if nu.GetLimits() == nil {
				return 0
			}
			return int(nu.GetLimits().GetMaxTranscodes())
		}(),
		CurrentTranscodes: 0,
	})

	// Update storage paths if present
	if storage := nu.GetStorage(); storage != nil {
		state.DefaultManager().SetNodeStoragePaths(nu.GetNodeId(), storage.GetLocalPath(), storage.GetS3Bucket(), storage.GetS3Prefix())
	}

	// Update GPU info if present (TODO: Add GPU field to NodeLifecycleUpdate protobuf)
	// if gpu := nu.GetGpu(); gpu != nil {
	//     state.DefaultManager().SetNodeGPUInfo(nu.GetNodeId(), gpu.GetVendor(), int(gpu.GetCount()), int(gpu.GetMemoryMb()), gpu.GetComputeCapability())
	// }

	// Update disk usage from OS-level stats reported by Helmsman
	state.DefaultManager().UpdateNodeDiskUsage(nu.GetNodeId(), nu.GetDiskTotalBytes(), nu.GetDiskUsedBytes())
	if err := state.DefaultManager().ApplyNodeLifecycle(context.Background(), nu); err != nil {
		p.logger.WithError(err).WithField("node_id", nu.GetNodeId()).Warn("Failed to persist node lifecycle snapshot")
	}

	// Calculate total connections across all streams for virtual viewer reconciliation
	var totalConnections int
	for _, s := range nu.GetStreams() {
		totalConnections += int(s.GetTotal())
	}

	// Reconcile virtual viewers with real metrics from Helmsman
	// This replaces DecayAddBandwidth() - times out stale PENDING viewers and updates bandwidth estimates
	state.DefaultManager().ReconcileVirtualViewers(nu.GetNodeId(), totalConnections, nu.GetUpSpeed())

	// Update stream stats for each stream
	// CRITICAL: Extract internal name to match state keys (e.g., "live+demo_stream" -> "demo_stream")
	for streamName, s := range nu.GetStreams() {
		internalName := mist.ExtractInternalName(streamName)
		state.DefaultManager().UpdateNodeStats(internalName, nu.GetNodeId(), int(s.GetTotal()), int(s.GetInputs()), int64(s.GetBytesUp()), int64(s.GetBytesDown()), s.GetReplicated())
	}

	previousArtifacts := func() []*pb.StoredArtifact {
		nodeState := state.DefaultManager().GetNodeState(nu.GetNodeId())
		if nodeState == nil {
			return nil
		}
		return nodeState.Artifacts
	}()

	// Update artifacts directly from protobuf - this is critical for VOD playback.
	// Called unconditionally: an empty slice clears stale artifacts from a node
	// that has lost all local files (prevents ghost artifacts in routing).
	state.DefaultManager().SetNodeArtifacts(nu.GetNodeId(), nu.GetArtifacts())
	if !artifactMapsEqual(previousArtifacts, nu.GetArtifacts()) {
		control.NotifyArtifactMapUpdated(nu.GetNodeId())
	}

	// Enrich with database UUID for subscription lookups (frontend uses UUID, not logical name)
	if uuid := p.resolveNodeUUID(nu.GetNodeId()); uuid != "" {
		nu.NodeUuid = &uuid
	}

	// Resolve owner tenant dynamically from the node's cluster (Foghorn manages many clusters).
	ownerTID := ""
	if cID := p.resolveNodeClusterID(nu.GetNodeId()); cID != "" {
		ownerTID = p.resolveClusterOwnerTenantID(cID)
	}
	if ownerTID == "" {
		ownerTID = p.ownerTenantID
	}
	if (trigger.TenantId == nil || *trigger.TenantId == "") && ownerTID != "" {
		trigger.TenantId = &ownerTID
	}
	if (nu.TenantId == nil || *nu.TenantId == "") && ownerTID != "" {
		nu.TenantId = &ownerTID
	}

	// Forward complete node lifecycle event to Decklog using protobuf directly
	// CRITICAL: Strip artifacts before sending to Decklog/Analytics to avoid excessive data
	nu.Artifacts = nil
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"node_id":      nu.GetNodeId(),
			"trigger_type": trigger.GetTriggerType(),
			"error":        err,
		}).Error("Failed to send node lifecycle update to Decklog")
	}

	return "", false, nil
}

func mapOperationalMode(mode pb.NodeOperationalMode) (state.NodeOperationalMode, bool) {
	switch mode {
	case pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL:
		return state.NodeModeNormal, true
	case pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_DRAINING:
		return state.NodeModeDraining, true
	case pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_MAINTENANCE:
		return state.NodeModeMaintenance, true
	default:
		return "", false
	}
}

func artifactMapsEqual(current, incoming []*pb.StoredArtifact) bool {
	if len(current) != len(incoming) {
		return false
	}
	if len(current) == 0 {
		return true
	}

	counts := make(map[string]int, len(current))
	for _, artifact := range current {
		counts[artifactMapKey(artifact)]++
	}

	for _, artifact := range incoming {
		key := artifactMapKey(artifact)
		if counts[key] == 0 {
			return false
		}
		counts[key]--
		if counts[key] == 0 {
			delete(counts, key)
		}
	}

	return len(counts) == 0
}

func artifactMapKey(artifact *pb.StoredArtifact) string {
	if artifact == nil {
		return "<nil>"
	}

	return fmt.Sprintf("%s|%s|%s|%d|%d|%s|%t|%d",
		artifact.GetClipHash(),
		artifact.GetStreamName(),
		artifact.GetFilePath(),
		artifact.GetSizeBytes(),
		artifact.GetCreatedAt(),
		artifact.GetFormat(),
		artifact.GetHasDtsh(),
		artifact.GetArtifactType(),
	)
}

// resolveNodeUUID resolves a node's logical name (e.g., "edge-node-1") to its database UUID.
// Uses a local cache to avoid repeated Quartermaster lookups (node IDs rarely change).
// Returns empty string if lookup fails or Quartermaster is unavailable.
func (p *Processor) resolveNodeUUID(nodeID string) string {
	if nodeID == "" {
		return ""
	}

	if p.nodeUUIDCache == nil {
		return ""
	}

	// Lookup from Quartermaster if client available
	if p.quartermasterClient == nil {
		return ""
	}

	val, ok, _ := p.nodeUUIDCache.Get(context.Background(), nodeID, func(ctx context.Context, key string) (interface{}, bool, error) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()

		node, err := p.quartermasterClient.GetNodeByLogicalName(ctx, key)
		if err != nil {
			p.logger.WithFields(logging.Fields{
				"node_id": key,
				"error":   err,
			}).Debug("Failed to resolve node UUID from Quartermaster")
			return nil, false, err
		}

		if node == nil || node.GetId() == "" {
			return nil, false, fmt.Errorf("node not found")
		}

		return node.GetId(), true, nil
	})
	if !ok {
		return ""
	}
	if uuid, ok := val.(string); ok {
		return uuid
	}
	return ""
}

// clusterAllowsPrivatePullSources returns true iff Quartermaster reports
// allow_private_pull_sources=true for clusterID. Empty clusterID, missing
// Quartermaster client, or any lookup failure returns false (fail-closed)
// so a defensive STREAM_SOURCE check refuses rather than dialing a private
// upstream from a cluster whose policy we cannot confirm.
//
// streamName is logged on failure so an operator can correlate the deny.
func (p *Processor) clusterAllowsPrivatePullSources(streamName, clusterID string) bool {
	if clusterID == "" || p.quartermasterClient == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := p.quartermasterClient.GetCluster(ctx, clusterID)
	if err != nil {
		p.logger.WithError(err).WithFields(logging.Fields{
			"stream_name": streamName,
			"cluster_id":  clusterID,
		}).Warn("clusterAllowsPrivatePullSources: GetCluster failed; failing closed")
		return false
	}
	if resp == nil || resp.GetCluster() == nil {
		return false
	}
	return resp.GetCluster().GetAllowPrivatePullSources()
}

// resolveNodeClusterID resolves a node's logical name to its cluster_id.
// Uses a local cache to avoid repeated Quartermaster lookups.
func (p *Processor) resolveNodeClusterID(nodeID string) string {
	if nodeID == "" || p.nodeClusterCache == nil || p.quartermasterClient == nil {
		return ""
	}

	val, ok, _ := p.nodeClusterCache.Get(context.Background(), nodeID, func(ctx context.Context, key string) (interface{}, bool, error) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()

		node, err := p.quartermasterClient.GetNodeByLogicalName(ctx, key)
		if err != nil {
			p.logger.WithFields(logging.Fields{
				"node_id": key,
				"error":   err,
			}).Debug("Failed to resolve node cluster from Quartermaster")
			return nil, false, err
		}

		if node == nil || node.GetClusterId() == "" {
			return nil, false, fmt.Errorf("node or cluster not found")
		}

		return node.GetClusterId(), true, nil
	})
	if !ok {
		return ""
	}
	if clusterID, ok := val.(string); ok {
		return clusterID
	}
	return ""
}

// resolveClusterOwnerTenantID resolves a cluster_id to its owner_tenant_id.
// Uses a local cache to avoid repeated Quartermaster lookups.
func (p *Processor) resolveClusterOwnerTenantID(clusterID string) string {
	if clusterID == "" || p.clusterOwnerCache == nil || p.quartermasterClient == nil {
		return ""
	}

	val, ok, _ := p.clusterOwnerCache.Get(context.Background(), clusterID, func(ctx context.Context, key string) (interface{}, bool, error) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()

		resp, err := p.quartermasterClient.GetCluster(ctx, key)
		if err != nil {
			p.logger.WithFields(logging.Fields{
				"cluster_id": key,
				"error":      err,
			}).Debug("Failed to resolve cluster owner from Quartermaster")
			return nil, false, err
		}

		if resp == nil || resp.GetCluster() == nil || resp.GetCluster().GetOwnerTenantId() == "" {
			return nil, false, fmt.Errorf("cluster or owner not found")
		}

		return resp.GetCluster().GetOwnerTenantId(), true, nil
	})
	if !ok {
		return ""
	}
	if ownerID, ok := val.(string); ok {
		return ownerID
	}
	return ""
}

// GenerateAndSendStorageSnapshots generates and sends an hourly storage snapshot to Decklog
func (p *Processor) GenerateAndSendStorageSnapshots() error {
	p.logger.Info("Starting GenerateAndSendStorageSnapshots")
	ctx := context.Background()
	snapshot := state.DefaultManager().GetBalancerSnapshotAtomic()
	if snapshot == nil {
		p.logger.Warn("Balancer snapshot is empty, skipping storage snapshot generation")
		return nil
	}

	for _, nodeSnap := range snapshot.Nodes {
		// Skip non-storage nodes or unhealthy nodes
		if !nodeSnap.CapStorage || !nodeSnap.IsActive {
			continue
		}

		// Get full node state to access artifacts
		nodeState := state.DefaultManager().GetNodeState(nodeSnap.NodeID)
		if nodeState == nil {
			continue
		}

		// Node's tenant_id and location from its own state
		nodeOwnerTenantID := ""
		if t := nodeState.TenantID; t != "" {
			nodeOwnerTenantID = t
		}
		nodeLocation := nodeState.Location
		nodeCapabilities := &pb.NodeCapabilities{
			Ingest:     nodeState.CapIngest,
			Edge:       nodeState.CapEdge,
			Storage:    nodeState.CapStorage,
			Processing: nodeState.CapProcessing,
			Roles:      nodeState.Roles,
		}

		// Map to store aggregated usage per tenant for this node
		tenantUsageMap := make(map[string]*pb.TenantStorageUsage)

		// Iterate through artifacts to sum up usage per tenant
		for _, artifact := range nodeState.Artifacts {
			var tenantID string
			var contentType string

			// Resolve tenant and content type from artifact hash using unified resolver
			if target, err := control.ResolveArtifactByHash(ctx, artifact.GetClipHash()); err == nil {
				tenantID = target.TenantID
				contentType = target.ContentType
			} else {
				p.logger.WithError(err).WithField("clip_hash", artifact.GetClipHash()).Warn("Failed to resolve tenant for artifact, skipping")
				continue
			}

			if tenantID == "" {
				// Fallback: If artifact is on a dedicated node, use node's tenant ID
				if nodeOwnerTenantID != "" {
					tenantID = nodeOwnerTenantID
				} else {
					continue
				}
			}

			usage := tenantUsageMap[tenantID]
			if usage == nil {
				usage = &pb.TenantStorageUsage{TenantId: tenantID}
				tenantUsageMap[tenantID] = usage
			}

			usage.TotalBytes += artifact.GetSizeBytes()
			usage.FileCount++

			// Categorize by content type (resolved from DB)
			switch contentType {
			case "clip":
				usage.ClipBytes += artifact.GetSizeBytes()
			case "dvr":
				usage.DvrBytes += artifact.GetSizeBytes()
			default:
				// Unknown content type - count towards clips as fallback
				usage.ClipBytes += artifact.GetSizeBytes()
			}
			// VodBytes: Reserved for user-uploaded video artifacts (not yet implemented)
		}

		snapshotTenantID := nodeOwnerTenantID
		if snapshotTenantID == "" && p.ownerTenantID != "" {
			snapshotTenantID = p.ownerTenantID
		}

		// Construct the StorageSnapshot message
		var tenantUsages []*pb.TenantStorageUsage
		for _, tu := range tenantUsageMap {
			tenantUsages = append(tenantUsages, tu)
		}

		// Provider attribution: hot edge cache lives on the node. The
		// node's owning tenant is the storage provider (cluster owner on
		// the marketplace, FrameWorks for platform clusters); the cluster
		// they advertise this node into is the provider cluster. Backend
		// is the on-disk edge cache. Settlement rating uses these to
		// route marketplace payouts. See docs/architecture/meter-contracts.md.
		hotProviderTenantID := p.ownerTenantID
		storageSnapshot := &pb.StorageSnapshot{
			NodeId:                   nodeSnap.NodeID,
			Timestamp:                time.Now().Unix(),
			TenantId:                 func() *string { s := snapshotTenantID; return &s }(),
			Location:                 func() *string { s := nodeLocation; return &s }(),
			Capabilities:             nodeCapabilities,
			Usage:                    tenantUsages,
			StorageScope:             stringPtr("hot"),
			StorageProviderTenantId:  func() *string { s := hotProviderTenantID; return &s }(),
			StorageProviderClusterId: func() *string { s := p.clusterID; return &s }(),
			StorageBackend:           stringPtr("edge_disk"),
		}

		// Send to Decklog
		trigger := &pb.MistTrigger{
			TriggerType: "STORAGE_SNAPSHOT",
			NodeId:      nodeSnap.NodeID,
			Timestamp:   time.Now().Unix(),
			TenantId:    func() *string { s := snapshotTenantID; return &s }(),
			ClusterId:   func() *string { s := p.clusterID; return &s }(),
			TriggerPayload: &pb.MistTrigger_StorageSnapshot{
				StorageSnapshot: storageSnapshot,
			},
		}

		if err := p.sendTriggerToDecklog(trigger); err != nil {
			p.logger.WithError(err).WithField("node_id", nodeSnap.NodeID).Error("Failed to send StorageSnapshot to Decklog")
		} else {
			p.logger.WithField("node_id", nodeSnap.NodeID).Info("Successfully sent StorageSnapshot to Decklog")
		}
	}

	// Emit a cold-storage snapshot (S3 authoritative) aggregated across artifacts table.
	coldUsageMap, err := control.GetColdStorageUsage(context.Background())
	if err != nil {
		p.logger.WithError(err).Warn("Failed to compute cold storage usage")
		return nil
	}
	if len(coldUsageMap) == 0 {
		return nil
	}

	var coldUsages []*pb.TenantStorageUsage
	for _, usage := range coldUsageMap {
		totalBytes := usage.DvrBytes + usage.ClipBytes + usage.VodBytes
		coldUsages = append(coldUsages, &pb.TenantStorageUsage{
			TenantId:        usage.TenantID,
			TotalBytes:      totalBytes,
			FileCount:       usage.FileCount,
			DvrBytes:        usage.DvrBytes,
			ClipBytes:       usage.ClipBytes,
			VodBytes:        usage.VodBytes,
			FrozenDvrBytes:  usage.DvrBytes,
			FrozenClipBytes: usage.ClipBytes,
			FrozenVodBytes:  usage.VodBytes,
		})
	}

	coldTenantID := p.ownerTenantID
	// Provider attribution: S3 freezer is operated by the cluster's owner
	// tenant (FrameWorks for the platform clusters; the marketplace
	// cluster operator for third-party clusters). Customer billing rates the
	// usage tenant; settlement views can route by these provider fields.
	coldSnapshot := &pb.StorageSnapshot{
		NodeId:                   "s3",
		Timestamp:                time.Now().Unix(),
		TenantId:                 func() *string { s := coldTenantID; return &s }(),
		Usage:                    coldUsages,
		StorageScope:             stringPtr("cold"),
		StorageProviderTenantId:  func() *string { s := coldTenantID; return &s }(),
		StorageProviderClusterId: func() *string { s := p.clusterID; return &s }(),
		StorageBackend:           stringPtr("s3"),
	}

	coldTrigger := &pb.MistTrigger{
		TriggerType: "STORAGE_SNAPSHOT",
		NodeId:      "s3",
		Timestamp:   time.Now().Unix(),
		TenantId:    func() *string { s := coldTenantID; return &s }(),
		ClusterId:   func() *string { s := p.clusterID; return &s }(),
		TriggerPayload: &pb.MistTrigger_StorageSnapshot{
			StorageSnapshot: coldSnapshot,
		},
	}

	if err := p.sendTriggerToDecklog(coldTrigger); err != nil {
		p.logger.WithError(err).Warn("Failed to send cold storage snapshot to Decklog")
	} else {
		p.logger.Info("Successfully sent cold storage snapshot to Decklog")
	}
	return nil
}

func stringPtr(s string) *string {
	return &s
}

func (p *Processor) resolveStreamContext(ctx context.Context, key, tenantIDHint string, allowCache bool) (streamContext, bool, error) {
	// For artifacts (VOD playback), check in-memory state first.
	// This avoids Commodore calls for artifacts we already know about.
	// Key may be artifact_hash (from processing+) or artifact_internal_name (from vod+).
	if tenantIDHint != "" && p.streamCache != nil {
		_, artifactInfo := state.DefaultManager().FindNodeByArtifactHash(key)
		if artifactInfo == nil {
			_, artifactInfo = state.DefaultManager().FindNodeByArtifactInternalName(key)
		}
		if artifactInfo != nil && artifactInfo.GetStreamName() != "" {
			parentInternal := mist.ExtractInternalName(artifactInfo.GetStreamName())
			cacheKey := tenantIDHint + ":" + parentInternal
			if v, ok := p.streamCache.Peek(cacheKey); ok {
				if parentInfo, ok := v.(streamContext); ok && parentInfo.TenantID != "" {
					info := streamContext{
						TenantID:          parentInfo.TenantID,
						UserID:            parentInfo.UserID,
						StreamID:          parentInfo.StreamID,
						Source:            "artifact_parent_cache",
						UpdatedAt:         time.Now(),
						OfficialClusterID: parentInfo.OfficialClusterID,
						OriginClusterID:   parentInfo.OriginClusterID,
						RequiresAuth:      parentInfo.RequiresAuth,
						RequiresAuthKnown: parentInfo.RequiresAuthKnown,
					}
					p.streamCacheMetaMu.Lock()
					p.streamCacheLastAt = info.UpdatedAt
					p.streamCacheLastErr = ""
					p.streamCacheMetaMu.Unlock()
					return info, true, nil
				}
			}
		}
	}

	// Fallback: call Commodore's unified resolver (single call checks all registries)
	if p.commodoreClient == nil {
		err := fmt.Errorf("commodore client not configured")
		atomic.AddUint64(&p.streamCacheResErr, 1)
		p.streamCacheMetaMu.Lock()
		p.streamCacheLastAt = time.Now()
		p.streamCacheLastErr = err.Error()
		p.streamCacheMetaMu.Unlock()
		return streamContext{}, false, err
	}

	resp, err := p.commodoreClient.ResolveIdentifier(ctx, key)
	if err != nil {
		atomic.AddUint64(&p.streamCacheResErr, 1)
		p.streamCacheMetaMu.Lock()
		p.streamCacheLastAt = time.Now()
		p.streamCacheLastErr = err.Error()
		p.streamCacheMetaMu.Unlock()
		p.logger.WithFields(logging.Fields{
			"identifier": key,
			"error":      err,
		}).Warn("Failed to resolve identifier from Commodore")
		return streamContext{}, false, err
	}

	if !resp.GetFound() {
		atomic.AddUint64(&p.streamCacheResErr, 1)
		p.streamCacheMetaMu.Lock()
		p.streamCacheLastAt = time.Now()
		p.streamCacheLastErr = "not found"
		p.streamCacheMetaMu.Unlock()
		p.logger.WithFields(logging.Fields{
			"identifier": key,
		}).Warn("Identifier not found in any Commodore registry")
		return streamContext{}, false, fmt.Errorf("identifier not found")
	}

	// Cache the result
	now := time.Now()
	info := streamContext{
		TenantID:          resp.GetTenantId(),
		UserID:            resp.GetUserId(),
		StreamID:          resp.GetStreamId(),
		Source:            "resolve_" + resp.GetIdentifierType(),
		UpdatedAt:         now,
		OriginClusterID:   resp.GetOriginClusterId(),
		ClusterPeers:      resp.GetClusterPeers(),
		RequiresAuth:      resp.GetRequiresAuth(),
		RequiresAuthKnown: true,
	}

	if resp.GetIdentifierType() == "playback_id" {
		atomic.AddUint64(&p.streamCacheResPb, 1)
	} else {
		atomic.AddUint64(&p.streamCacheResInt, 1)
	}

	p.streamCacheMetaMu.Lock()
	p.streamCacheLastAt = now
	p.streamCacheLastErr = ""
	p.streamCacheMetaMu.Unlock()

	// If this was a playback_id, also cache by the canonical internal_name
	if allowCache && resp.GetIdentifierType() == "playback_id" && resp.GetInternalName() != "" && p.streamCache != nil && resp.GetTenantId() != "" {
		cacheKey := resp.GetTenantId() + ":" + resp.GetInternalName()
		p.streamCache.Set(cacheKey, info, 10*time.Minute)
	}

	p.logger.WithFields(logging.Fields{
		"identifier":      key,
		"identifier_type": resp.GetIdentifierType(),
		"tenant_id":       info.TenantID,
	}).Debug("Resolved identifier from Commodore")

	return info, true, nil
}

// getStreamContext gets tenant and user IDs from cache, with fallback to Commodore
func (p *Processor) getStreamContext(ctx context.Context, streamName, tenantIDHint string) streamContext {
	if streamName == "" {
		return streamContext{}
	}

	internalName := mist.ExtractInternalName(streamName)
	if p.streamCache == nil || tenantIDHint == "" {
		info, ok, _ := p.resolveStreamContext(ctx, internalName, tenantIDHint, false)
		if !ok {
			return streamContext{}
		}
		return info
	}

	cacheKey := tenantIDHint + ":" + internalName
	val, ok, _ := p.streamCache.Get(ctx, cacheKey, func(ctx context.Context, _ string) (interface{}, bool, error) {
		return p.resolveStreamContext(ctx, internalName, tenantIDHint, true)
	})

	if !ok {
		return streamContext{}
	}
	if info, ok := val.(streamContext); ok {
		return info
	}
	return streamContext{}
}

// applyStreamContext enriches trigger with tenant/user/stream IDs if available.
func (p *Processor) applyStreamContext(trigger *pb.MistTrigger, streamName string) streamContext {
	tenantHint := ""
	if trigger != nil && trigger.TenantId != nil {
		tenantHint = *trigger.TenantId
	}
	info := p.getStreamContext(context.Background(), streamName, tenantHint)
	if trigger == nil {
		return info
	}
	if info.TenantID != "" && (trigger.TenantId == nil || *trigger.TenantId == "") {
		trigger.TenantId = &info.TenantID
	}
	if info.UserID != "" && (trigger.UserId == nil || *trigger.UserId == "") {
		trigger.UserId = &info.UserID
	}
	if info.StreamID != "" && (trigger.StreamId == nil || *trigger.StreamId == "") {
		trigger.StreamId = &info.StreamID
	}
	if info.OriginClusterID != "" && (trigger.OriginClusterId == nil || *trigger.OriginClusterId == "") {
		trigger.OriginClusterId = &info.OriginClusterID
	}
	if (trigger.ClusterId == nil || *trigger.ClusterId == "") && p.clusterID != "" {
		clusterID := p.clusterID
		trigger.ClusterId = &clusterID
	}
	return info
}

// detectProtocol extracts protocol from push URL
func (p *Processor) detectProtocol(pushURL string) string {
	if pushURL == "" {
		return ""
	}

	if strings.HasPrefix(pushURL, "rtmp://") {
		return "rtmp"
	} else if strings.HasPrefix(pushURL, "srt://") {
		return "srt"
	} else if strings.HasPrefix(pushURL, "whip://") {
		return "whip"
	} else if strings.HasPrefix(pushURL, "http://") || strings.HasPrefix(pushURL, "https://") {
		return "http"
	}

	return ""
}

// NodeConfig represents node configuration including geographic data
type NodeConfig struct {
	Latitude  float64
	Longitude float64
	Location  string
}

// getNodeConfig returns node configuration including geographic data
func (p *Processor) getNodeConfig(nodeID string) *NodeConfig {
	// Get node state directly from unified state manager
	nodeState := state.DefaultManager().GetNodeState(nodeID)
	if nodeState == nil {
		return nil
	}

	config := &NodeConfig{
		Location: nodeState.Location,
	}

	// Handle pointer types for latitude/longitude
	if nodeState.Latitude != nil {
		config.Latitude = *nodeState.Latitude
	}
	if nodeState.Longitude != nil {
		config.Longitude = *nodeState.Longitude
	}

	return config
}

// pushTargetInfo stores metadata for a tracked multistream push target.
type pushTargetInfo struct {
	TargetID string
	TenantID string
}

// activePushTargetsMu protects activePushTargetMap.
var (
	activePushTargetsMu sync.Mutex
	activePushTargetMap = map[string]map[string]pushTargetInfo{} // streamName -> targetURI -> info
)

// trackPushTargets stores push target metadata so PUSH_OUT_START/PUSH_END
// can map target URIs back to push target IDs for status updates.
func trackPushTargets(streamName, tenantID string, targets []*pb.PushTargetInternal) {
	activePushTargetsMu.Lock()
	defer activePushTargetsMu.Unlock()
	m := make(map[string]pushTargetInfo, len(targets))
	for _, t := range targets {
		m[t.GetTargetUri()] = pushTargetInfo{TargetID: t.GetId(), TenantID: tenantID}
	}
	activePushTargetMap[streamName] = m
}

// untrackPushTargets removes push target tracking for a stream.
func untrackPushTargets(streamName string) {
	activePushTargetsMu.Lock()
	defer activePushTargetsMu.Unlock()
	delete(activePushTargetMap, streamName)
}

// lookupPushTarget finds the push target info for a stream+URI pair.
func lookupPushTarget(streamName, targetURI string) (pushTargetInfo, bool) {
	activePushTargetsMu.Lock()
	defer activePushTargetsMu.Unlock()
	if m, ok := activePushTargetMap[streamName]; ok {
		if info, found := m[targetURI]; found {
			return info, true
		}
	}
	return pushTargetInfo{}, false
}

// activatePushTargets sends push targets (from ValidateStreamKeyResponse) to the
// origin node's Helmsman for activation. Called asynchronously from handlePushRewrite.
func (p *Processor) activatePushTargets(nodeID, internalName, tenantID string, targets []*pb.PushTargetInternal) {
	specs := make([]*pb.PushTargetSpec, 0, len(targets))
	for _, t := range targets {
		specs = append(specs, &pb.PushTargetSpec{
			TargetId:  t.GetId(),
			TargetUri: t.GetTargetUri(),
			Name:      t.GetName(),
		})
	}

	streamName := fmt.Sprintf("live+%s", internalName)

	// Track targets so PUSH_OUT_START/PUSH_END can update status
	trackPushTargets(streamName, tenantID, targets)

	if err := control.SendActivatePushTargets(nodeID, &pb.ActivatePushTargets{
		StreamName: streamName,
		Targets:    specs,
	}); err != nil {
		p.logger.WithFields(logging.Fields{
			"node_id":      nodeID,
			"stream_name":  streamName,
			"target_count": len(specs),
			"error":        err,
		}).Error("Failed to send ActivatePushTargets to Helmsman")
		return
	}

	p.logger.WithFields(logging.Fields{
		"node_id":      nodeID,
		"stream_name":  streamName,
		"target_count": len(specs),
	}).Info("Activated multistream push targets")
}

// deactivatePushTargets tells Helmsman to stop all pushes for a stream.
func (p *Processor) deactivatePushTargets(nodeID, streamName string) {
	untrackPushTargets(streamName)

	if err := control.SendDeactivatePushTargets(nodeID, &pb.DeactivatePushTargets{
		StreamName: streamName,
	}); err != nil {
		p.logger.WithFields(logging.Fields{
			"node_id":     nodeID,
			"stream_name": streamName,
			"error":       err,
		}).Warn("Failed to send DeactivatePushTargets to Helmsman")
	}
}

// updatePushTargetStatus updates push target status in Commodore when a
// push starts or ends. Called from handlePushOutStart and handlePushEnd.
func (p *Processor) updatePushTargetStatus(streamName, targetURI, status string, lastError *string) {
	info, found := lookupPushTarget(streamName, targetURI)
	if !found {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.commodoreClient.UpdatePushTargetStatus(ctx, info.TargetID, info.TenantID, status, lastError); err != nil {
		p.logger.WithFields(logging.Fields{
			"target_id":   info.TargetID,
			"stream_name": streamName,
			"status":      status,
			"error":       err,
		}).Warn("Failed to update push target status in Commodore")
	}
}

// admissionDecision is the load-aware outcome for free-tier traffic.
type admissionDecision int

const (
	admissionAdmit admissionDecision = iota
	admissionRejectOverAllowance
	admissionRejectRedline
)

// loadThresholds parameterizes the three-tier free-tier admission policy.
//   - rejectOverAllowance: load fraction at which free tenants over their
//     allowance are denied new traffic. Lower = sooner to reject.
//   - rejectAnyFree: load fraction at which any free traffic is denied (even
//     within allowance). The redline above which paying tenants take priority.
type loadThresholds struct {
	rejectOverAllowance float64
	rejectAnyFree       float64
}

// freeTierAllowanceState reduces the per-meter allowance list to two flags
// used by the load gate: is the tenant on the free tier, and have they
// exhausted any free-tier allowance. IsFreeTier comes from tier-identity in
// Purser (tier_name == 'free'); a paid tenant with a zero-priced meter is
// not "free" for admission purposes.
func freeTierAllowanceState(allowances []*pb.MeterAllowance) (isFreeTier, exhausted bool) {
	for _, a := range allowances {
		if a == nil {
			continue
		}
		if a.GetIsFreeTier() {
			isFreeTier = true
			if a.GetExhausted() {
				exhausted = true
				return
			}
		}
	}
	return
}

func liveInternalNamesForTenant(tenantID string) []string {
	streams := state.DefaultManager().GetStreamsByTenant(tenantID)
	names := make([]string, 0, len(streams))
	for _, stream := range streams {
		if stream != nil && stream.InternalName != "" {
			names = append(names, stream.InternalName)
		}
	}
	return names
}

func envFloatInRange(key string, fallback, min, max float64) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil || parsed <= min || parsed >= max {
		return fallback
	}
	return parsed
}

// ingestLoadThresholds returns the broadcaster-admission policy. Defaults:
//   - over-allowance free is rejected at 50% cluster load
//   - any free (incl. within-allowance) is rejected at 95% (redline)
//
// Configurable via FOGHORN_INGEST_REJECT_OVER_ALLOWANCE_LOAD and
// FOGHORN_INGEST_REJECT_FREE_LOAD.
func ingestLoadThresholds() loadThresholds {
	return loadThresholds{
		rejectOverAllowance: envFloatInRange("FOGHORN_INGEST_REJECT_OVER_ALLOWANCE_LOAD", 0.5, 0, 1),
		rejectAnyFree:       envFloatInRange("FOGHORN_INGEST_REJECT_FREE_LOAD", 0.95, 0, 1),
	}
}

// viewerLoadThresholds returns the viewer-admission policy. Defaults:
//   - over-allowance broadcaster's viewers rejected at 80%
//   - any viewer of a free broadcaster rejected at 95% (redline)
//
// Viewers are cheaper individually but multiply faster than ingest, so the
// over-allowance reject point is higher than ingest's 50%.
func viewerLoadThresholds() loadThresholds {
	return loadThresholds{
		rejectOverAllowance: envFloatInRange("FOGHORN_VIEWER_REJECT_OVER_ALLOWANCE_LOAD", 0.8, 0, 1),
		rejectAnyFree:       envFloatInRange("FOGHORN_VIEWER_REJECT_FREE_LOAD", 0.95, 0, 1),
	}
}

// applyLoadGate evaluates the three-tier policy. Paying tenants are always
// admitted. Free tenants over their allowance are rejected once load crosses
// rejectOverAllowance. At the redline (rejectAnyFree), all free traffic is
// rejected regardless of allowance state.
func applyLoadGate(loadFrac float64, isFreeTier, exhausted bool, thresh loadThresholds) admissionDecision {
	if !isFreeTier {
		return admissionAdmit
	}
	if loadFrac >= thresh.rejectAnyFree {
		return admissionRejectRedline
	}
	if exhausted && loadFrac >= thresh.rejectOverAllowance {
		return admissionRejectOverAllowance
	}
	return admissionAdmit
}

// clusterLoadFraction returns the cluster's load (0.0–1.0) and whether a
// signal is present. Wraps ClusterLoad so callers don't have to convert the
// percent and check the sample count themselves.
func clusterLoadFraction(clusterID string) (frac float64, ok bool) {
	if strings.TrimSpace(clusterID) == "" {
		return 0, false
	}
	pct, samples := state.DefaultManager().ClusterLoad(clusterID)
	if samples == 0 {
		return 0, false
	}
	return pct / 100.0, true
}

// evaluateFreeTierAdmission applies the three-tier ingest gate. Free tenants
// past their allowance are rejected at 50% cluster load; all free tenants are
// rejected at the 95% redline. Existing live streams are never killed —
// rejection applies only to new PUSH_REWRITE admissions.
func (p *Processor) evaluateFreeTierAdmission(streamValidation *pb.ValidateStreamKeyResponse, clusterID string) (string, bool) {
	if streamValidation == nil {
		return "", false
	}
	isFree, exhausted := freeTierAllowanceState(streamValidation.GetAllowances())
	if !isFree {
		return "", false
	}

	loadFrac, ok := clusterLoadFraction(clusterID)
	if !ok {
		// No cluster context or no load signal — admit and log. Caller can
		// distinguish in audit but the policy treats both the same: fail-open.
		p.logger.WithFields(logging.Fields{
			"tenant_id":  streamValidation.GetTenantId(),
			"cluster_id": clusterID,
			"exhausted":  exhausted,
		}).Info("Admitted free-tier ingest: no cluster load signal")
		return "", false
	}

	thresh := ingestLoadThresholds()
	switch applyLoadGate(loadFrac, isFree, exhausted, thresh) {
	case admissionAdmit:
		p.logger.WithFields(logging.Fields{
			"tenant_id":  streamValidation.GetTenantId(),
			"cluster_id": clusterID,
			"load_frac":  loadFrac,
			"exhausted":  exhausted,
			"thresholds": fmt.Sprintf("over=%.2f redline=%.2f", thresh.rejectOverAllowance, thresh.rejectAnyFree),
		}).Info("Admitted free-tier ingest: cluster has spare capacity")
		return "", false
	case admissionRejectOverAllowance:
		return fmt.Sprintf(
			"free-tier allowance exhausted and cluster is at load (%.0f%%, threshold %.0f%%) — upgrade or retry off-peak",
			loadFrac*100,
			thresh.rejectOverAllowance*100,
		), true
	case admissionRejectRedline:
		return fmt.Sprintf(
			"cluster at redline (%.0f%%, threshold %.0f%%); free-tier ingest denied to preserve capacity for paying tenants — upgrade or retry off-peak",
			loadFrac*100,
			thresh.rejectAnyFree*100,
		), true
	}
	return "", false
}

// evaluateViewerAdmission applies the viewer-side three-tier gate. The
// broadcaster's tenant identity governs the decision (a viewer connecting to
// a free-tier stream is what consumes free-tier delivered minutes).
// Broadcaster state lives in the stream context cache populated at
// PUSH_REWRITE; clusterID is the cluster serving this viewer.
func (p *Processor) evaluateViewerAdmission(ctx streamContext, clusterID string) (string, bool) {
	if !ctx.IsFreeTier {
		return "", false
	}
	loadFrac, ok := clusterLoadFraction(clusterID)
	if !ok {
		return "", false
	}
	thresh := viewerLoadThresholds()
	switch applyLoadGate(loadFrac, ctx.IsFreeTier, ctx.AllowanceExhausted, thresh) {
	case admissionAdmit:
		return "", false
	case admissionRejectOverAllowance:
		return fmt.Sprintf(
			"viewer denied: stream's free-tier allowance is exhausted and cluster is at load (%.0f%%, threshold %.0f%%)",
			loadFrac*100,
			thresh.rejectOverAllowance*100,
		), true
	case admissionRejectRedline:
		return fmt.Sprintf(
			"viewer denied: cluster at redline (%.0f%%, threshold %.0f%%); free-tier viewers denied to preserve capacity for paying tenants",
			loadFrac*100,
			thresh.rejectAnyFree*100,
		), true
	}
	return "", false
}
