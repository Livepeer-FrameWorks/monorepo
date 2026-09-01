package federation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"frameworks/api_balancing/internal/artifactoutbox"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/geo"
	"frameworks/api_balancing/internal/state"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	foghornfed "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn/federation"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// PeerManager manages PeerChannel lifecycles and periodic peer discovery.
// It reconciles the peer list from Quartermaster every five minutes and maintains
// one PeerChannel per peer cluster for bidirectional telemetry exchange.
// In multi-replica deployments, only the leader instance runs the active
// peering loop (Redis-based leader lease).
type PeerManager struct {
	clusterID              string
	controlCellID          string
	instanceID             string // unique per-process, for leader election
	pool                   federationPeerPool
	peerDiscovery          clusterPeerDiscovery
	cache                  *RemoteEdgeCache
	logger                 logging.Logger
	decklogClient          *decklog.BatchedClient
	ownerTenantID          string
	selfGeoFunc            func() (float64, float64, string)
	artifactTenantResolver func(ctx context.Context, hashes []string) (map[string]string, error)
	canPurgeMemberships    func(context.Context, []control.AdmissionEffectFence) (map[string]bool, error)

	mu                            sync.RWMutex
	peers                         map[string]*peerState      // cluster_id -> peer state
	streamPeers                   map[string]map[string]bool // cluster_id -> set of active stream names
	streamTenants                 map[string]string          // stream name -> tenant id
	streamMemberships             map[string]StreamPeerMembership
	trackedTenantRefs             map[string]map[string]int
	trackedAddrRefs               map[string]map[string]map[int64]int
	trackedAlwaysOnRefs           map[string]int
	quartermasterHints            map[string]PeerHint       // leader-owned authoritative refresh snapshot
	quartermasterHintsRefreshedAt time.Time                 // bounds renewal when Quartermaster stops answering
	metricHistory                 map[string][]metricSample // node_id -> recent BW/CPU samples for 30s averaging
	nextPeerRunnerToken           uint64
	done                          chan struct{}
	isLeader                      bool
	leaderReady                   bool
	startTime                     time.Time
	reconnectBackoff              time.Duration
	tombstoneScanCursor           uint64

	unresolvedAdMu     sync.Mutex
	unresolvedAdLogged map[string]time.Time // artifact_hash -> last skip log, throttles the 30s ad loop
}

// metricSample stores a single BW/CPU observation for moving-average computation.
type metricSample struct {
	bwAvailable uint64
	cpuPercent  float64
	ts          time.Time
}

type peerLifecycleType int

const (
	peerAlwaysOn     peerLifecycleType = iota // official ↔ preferred cluster pair
	peerStreamScoped                          // other subscribed clusters
)

type peerState struct {
	addr        string
	tenantIDs   []string
	tenantSet   map[string]struct{}
	lifecycle   peerLifecycleType
	cancel      context.CancelFunc
	stream      foghornfederationpb.FoghornFederation_PeerChannelClient
	sendCh      chan *foghornfederationpb.PeerMessage // owned by the per-peer writer goroutine; producers enqueue, never Send directly
	dropped     atomic.Uint64                         // frames evicted (drop-oldest) because the mailbox was full (backpressured peer)
	lastRefresh time.Time
	connected   bool
	runnerToken uint64
	lat         float64
	lon         float64
	location    string
}

type peerConnectRequest struct {
	clusterID string
	state     *peerState
	token     uint64
}

type clusterPeerDiscovery interface {
	ListPeers(ctx context.Context, clusterID string) (*quartermasterpb.ListPeersResponse, error)
}

type federationPeerClient interface {
	OpenPeerChannel(ctx context.Context) (foghornfederationpb.FoghornFederation_PeerChannelClient, error)
}

type federationPeerPool interface {
	GetOrCreate(clusterID, addr string) (federationPeerClient, error)
	Touch(clusterID string)
}

type foghornPoolAdapter struct {
	pool *foghorn.FoghornPool
}

type foghornPeerClient struct {
	client *foghorn.GRPCClient
}

func newFoghornPoolAdapter(pool *foghorn.FoghornPool) federationPeerPool {
	if pool == nil {
		return nil
	}
	return &foghornPoolAdapter{pool: pool}
}

func (a *foghornPoolAdapter) GetOrCreate(clusterID, addr string) (federationPeerClient, error) {
	client, err := a.pool.GetOrCreate(clusterID, addr)
	if err != nil {
		return nil, err
	}
	return &foghornPeerClient{client: client}, nil
}

func (a *foghornPoolAdapter) Touch(clusterID string) {
	a.pool.Touch(clusterID)
}

func (c *foghornPeerClient) OpenPeerChannel(ctx context.Context) (foghornfederationpb.FoghornFederation_PeerChannelClient, error) {
	return foghornfed.For(c.client).Federation().PeerChannel(ctx)
}

// PeerManagerConfig holds dependencies for the peer manager.
type PeerManagerConfig struct {
	ClusterID     string
	ControlCellID string
	InstanceID    string // unique per-process; used for leader lease
	Pool          *foghorn.FoghornPool
	QM            *quartermaster.GRPCClient
	PeerDiscovery clusterPeerDiscovery
	Cache         *RemoteEdgeCache
	Logger        logging.Logger
	DecklogClient *decklog.BatchedClient
	OwnerTenantID string
	SelfGeoFunc   func() (float64, float64, string) // lat, lon, location — avoids import cycle with handlers
	// ArtifactTenantResolver batch-resolves artifact hashes to tenant ids from
	// the artifact registry. Artifacts outlive their streams, so in-memory
	// stream state alone cannot attribute warm files from ended streams.
	ArtifactTenantResolver func(ctx context.Context, hashes []string) (map[string]string, error)
	// CanPurgeMemberships proves through the admission ledger that no pending callback at or below
	// an ended membership revision can still issue TrackStream.
	CanPurgeMemberships func(context.Context, []control.AdmissionEffectFence) (map[string]bool, error)
}

// NewPeerManager creates and starts a new peer manager.
func NewPeerManager(cfg PeerManagerConfig) *PeerManager {
	peerDiscovery := cfg.PeerDiscovery
	if peerDiscovery == nil {
		peerDiscovery = cfg.QM
	}

	pm := &PeerManager{
		clusterID:              cfg.ClusterID,
		controlCellID:          cfg.ControlCellID,
		instanceID:             cfg.InstanceID,
		pool:                   newFoghornPoolAdapter(cfg.Pool),
		peerDiscovery:          peerDiscovery,
		cache:                  cfg.Cache,
		logger:                 cfg.Logger,
		decklogClient:          cfg.DecklogClient,
		ownerTenantID:          cfg.OwnerTenantID,
		selfGeoFunc:            cfg.SelfGeoFunc,
		artifactTenantResolver: cfg.ArtifactTenantResolver,
		canPurgeMemberships:    cfg.CanPurgeMemberships,
		peers:                  make(map[string]*peerState),
		streamPeers:            make(map[string]map[string]bool),
		streamTenants:          make(map[string]string),
		streamMemberships:      make(map[string]StreamPeerMembership),
		trackedTenantRefs:      make(map[string]map[string]int),
		trackedAddrRefs:        make(map[string]map[string]map[int64]int),
		trackedAlwaysOnRefs:    make(map[string]int),
		quartermasterHints:     make(map[string]PeerHint),
		metricHistory:          make(map[string][]metricSample),
		done:                   make(chan struct{}),
		startTime:              time.Now(),
		reconnectBackoff:       peerReconnectBackoff,
		unresolvedAdLogged:     make(map[string]time.Time),
	}
	go pm.run()
	return pm
}

func (pm *PeerManager) SetOwnerTenantID(ownerTenantID string) {
	if ownerTenantID == "" {
		return
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.ownerTenantID = ownerTenantID
}

// emitFederationEvent sends a topology/lifecycle event to Decklog (fire-and-forget).
// Automatically enriches with local/remote geo from self-geo and peer cache.
func (pm *PeerManager) emitFederationEvent(data *ipcpb.FederationEventData) {
	pm.enrichFederationEventGeo(data)

	if data.GetTenantId() == "" {
		pm.logger.WithFields(logging.Fields{
			"event_type":    data.GetEventType().String(),
			"local_cluster": data.GetLocalCluster(),
		}).Warn("Skipping federation event without tenant_id")
		return
	}
	if pm.decklogClient == nil {
		return
	}
	go func() {
		if err := artifactoutbox.EnqueueFederationEvent(data); err != nil {
			pm.logger.WithError(err).Debug("Failed to emit federation event")
		}
	}()
}

func (pm *PeerManager) enrichFederationEventGeo(data *ipcpb.FederationEventData) {
	if data.TenantId == nil {
		pm.mu.RLock()
		tenantID := pm.ownerTenantID
		pm.mu.RUnlock()
		if tenantID != "" {
			data.TenantId = &tenantID
		}
	}
	if data.LocalCluster == "" {
		data.LocalCluster = pm.clusterID
	}
	if data.ControlCellId == nil && pm.controlCellID != "" {
		controlCellID := pm.controlCellID
		data.ControlCellId = &controlCellID
	}
	if data.RemoteCluster == "" && data.PeerCluster != nil {
		data.RemoteCluster = data.GetPeerCluster()
	}
	if data.LocalLat == nil && pm.selfGeoFunc != nil {
		lat, lon, _ := pm.selfGeoFunc()
		if geo.IsValidLatLon(lat, lon) {
			data.LocalLat = &lat
			data.LocalLon = &lon
		}
	}
	if data.RemoteLat == nil && data.RemoteCluster != "" {
		rLat, rLon := pm.GetPeerGeo(data.RemoteCluster)
		if geo.IsValidLatLon(rLat, rLon) {
			data.RemoteLat = &rLat
			data.RemoteLon = &rLon
		}
	}
}

// GetPeerGeo returns the cached geo coordinates for a peer cluster's foghorn.
func (pm *PeerManager) GetPeerGeo(clusterID string) (float64, float64) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	if ps, ok := pm.peers[clusterID]; ok {
		return ps.lat, ps.lon
	}
	return 0, 0
}

// Close stops the peer manager and all PeerChannel streams.
func (pm *PeerManager) Close() {
	close(pm.done)
	pm.disconnectAllPeers()
	if pm.cache != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		pm.cache.ReleaseLeaderLease(ctx, leaderRole, pm.instanceID)
	}
}

// disconnectAllPeers cancels all peer connections and clears the peer map.
func (pm *PeerManager) disconnectAllPeers() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for id, ps := range pm.peers {
		if ps.cancel != nil {
			ps.cancel()
		}
		delete(pm.peers, id)
	}
}

// GetPeers returns a snapshot of known peer cluster IDs and addresses.
func (pm *PeerManager) GetPeers() map[string]string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	result := make(map[string]string, len(pm.peers))
	for id, ps := range pm.peers {
		result[id] = ps.addr
	}
	return result
}

// GetPeerAddr returns the gRPC address for a peer cluster, or empty if unknown.
func (pm *PeerManager) GetPeerAddr(clusterID string) string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	if ps, ok := pm.peers[clusterID]; ok {
		return ps.addr
	}
	return ""
}

// IsPeerConnected returns whether the PeerChannel to a given cluster is active.
func (pm *PeerManager) IsPeerConnected(clusterID string) bool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	if ps, ok := pm.peers[clusterID]; ok {
		return ps.connected
	}
	return false
}

// LeaderInstanceID returns the instance currently holding PeerManager leadership ("" when unknown):
// self when leading, otherwise the leader-lease holder from Redis. Used to route leader-affine
// durable obligations (federation broadcasts) to the replica that can actually execute them.
func (pm *PeerManager) LeaderInstanceID() string {
	pm.mu.Lock()
	leading := pm.isLeader
	pm.mu.Unlock()
	if leading {
		return pm.instanceID
	}
	if pm.cache == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return pm.cache.GetLeaderInstance(ctx, leaderRole)
}

// IsLeader reports whether this replica currently holds PeerManager leadership. Only the leader
// opens PeerChannel connections, so federation lifecycle broadcasts are meaningful only from the
// leader — a non-leader broadcast would reach zero peers while looking successful.
func (pm *PeerManager) IsLeader() bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.isLeader && pm.leaderReady
}

func (pm *PeerManager) quartermasterContributorID() string {
	return pm.instanceID + ":quartermaster"
}

func (pm *PeerManager) shouldSendStreamToPeer(peerID string, ps *peerState, streamName, tenantID string) bool {
	if len(ps.tenantIDs) > 0 && tenantID != "" && !peerHasTenant(ps, tenantID) {
		return false
	}
	if tenantID != "" && len(ps.tenantIDs) == 0 && ps.lifecycle == peerStreamScoped {
		return false
	}
	if ps.lifecycle == peerStreamScoped {
		streams := pm.streamPeers[peerID]
		if len(streams) == 0 || !streams[streamName] {
			return false
		}
	}
	return true
}

func peerHasTenant(ps *peerState, tenantID string) bool {
	if ps == nil {
		return false
	}
	if ps.tenantSet != nil {
		_, ok := ps.tenantSet[tenantID]
		return ok
	}
	return slices.Contains(ps.tenantIDs, tenantID)
}

func replacePeerTenants(ps *peerState, tenantIDs []string) {
	ps.tenantIDs = append(ps.tenantIDs[:0], tenantIDs...)
	ps.tenantSet = make(map[string]struct{}, len(ps.tenantIDs))
	for _, tenantID := range ps.tenantIDs {
		ps.tenantSet[tenantID] = struct{}{}
	}
}

// TrackStream durably records one generation's complete peer set. The return value is false when a
// newer revision or an ended equal revision already owns the membership; callers must not emit the
// stale generation's lifecycle event in that case.
func (pm *PeerManager) TrackStream(ctx context.Context, streamName, tenantID, sourceGeneration string, sourceRevision int64, admissionHints []control.AdmissionPeerHint) (bool, error) {
	streamName = strings.TrimSpace(streamName)
	tenantID = strings.TrimSpace(tenantID)
	sourceGeneration = strings.TrimSpace(sourceGeneration)
	if streamName == "" || tenantID == "" || sourceGeneration == "" || sourceRevision <= 0 {
		return false, errors.New("federation stream tracking requires stream, tenant, generation, and positive revision")
	}
	peerHints := make(map[string]PeerHint, len(admissionHints))
	membership := StreamPeerMembership{
		StreamName: streamName, TenantID: tenantID, SourceGeneration: sourceGeneration,
		SourceRevision: sourceRevision, Active: true,
	}
	for _, admissionHint := range admissionHints {
		clusterID := strings.TrimSpace(admissionHint.ClusterID)
		addr := strings.TrimSpace(admissionHint.Addr)
		if clusterID == "" || addr == "" {
			return false, errors.New("federation stream tracking received an incomplete durable peer hint")
		}
		if clusterID == pm.clusterID || control.IsServedCluster(clusterID) {
			continue
		}
		if existing, ok := peerHints[clusterID]; ok {
			if existing.Addr != addr || existing.AlwaysOn != admissionHint.AlwaysOn {
				return false, fmt.Errorf("federation stream tracking has conflicting hints for peer %s", clusterID)
			}
			continue
		}
		peerHints[clusterID] = PeerHint{Addr: addr, AlwaysOn: admissionHint.AlwaysOn, Tenants: []string{tenantID}}
		membership.Peers = append(membership.Peers, StreamPeerTarget{ClusterID: clusterID, Addr: addr, AlwaysOn: admissionHint.AlwaysOn})
	}
	var err error
	membership, err = normalizeStreamPeerMembership(membership)
	if err != nil {
		return false, err
	}
	if pm.cache != nil {
		current, persistErr := pm.cache.SetStreamPeerMembership(ctx, membership)
		if persistErr != nil {
			return false, fmt.Errorf("persist federation stream membership: %w", persistErr)
		}
		if !current {
			return false, nil
		}
	}
	pm.mu.Lock()
	var replacedPeers []StreamPeerTarget
	if previous, ok := pm.streamMemberships[streamName]; ok {
		switch {
		case previous.SourceRevision > membership.SourceRevision:
			pm.mu.Unlock()
			return false, nil
		case previous.SourceRevision == membership.SourceRevision && previous.SourceGeneration != membership.SourceGeneration:
			pm.mu.Unlock()
			return false, errors.New("federation stream tracking revision conflicts with another generation")
		case previous.SourceRevision == membership.SourceRevision && !previous.Active:
			pm.mu.Unlock()
			return false, nil
		case previous.SourceRevision == membership.SourceRevision && !streamPeerMembershipEqual(previous, membership):
			pm.mu.Unlock()
			return false, errors.New("federation stream tracking revision conflicts with another peer set")
		case previous.SourceRevision == membership.SourceRevision:
			toConnect := pm.importAdmissionPeerHintsLocked(peerHints)
			pm.mu.Unlock()
			pm.connectAuthorityPeers(toConnect)
			return true, pm.streamMembershipReadiness(ctx, membership)
		}
		pm.removeStreamMembershipLocked(previous)
		replacedPeers = previous.Peers
	}
	pm.addStreamMembershipLocked(membership)
	pm.closeUntrackedStreamScopedPeersLocked(replacedPeers, streamName)
	toConnect := pm.importAdmissionPeerHintsLocked(peerHints)
	pm.mu.Unlock()
	pm.connectAuthorityPeers(toConnect)
	return true, pm.streamMembershipReadiness(ctx, membership)
}

func (pm *PeerManager) streamMembershipReadiness(ctx context.Context, membership StreamPeerMembership) error {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	var readinessErr error
	for _, peer := range membership.Peers {
		if err := ctx.Err(); err != nil {
			return errors.Join(readinessErr, err)
		}
		ps := pm.peers[peer.ClusterID]
		if ps == nil {
			readinessErr = errors.Join(readinessErr, fmt.Errorf("federation peer %s has no imported discovery hint", peer.ClusterID))
		} else if !ps.connected || ps.stream == nil {
			readinessErr = errors.Join(readinessErr, fmt.Errorf("federation peer %s is not connected", peer.ClusterID))
		}
	}
	return readinessErr
}

// UntrackStream installs a revision tombstone and removes only the matching generation. If a
// stream-scoped peer has no remaining active streams, its PeerChannel is closed.
func (pm *PeerManager) UntrackStream(ctx context.Context, streamName, tenantID, sourceGeneration string, sourceRevision int64) error {
	streamName = strings.TrimSpace(streamName)
	tenantID = strings.TrimSpace(tenantID)
	sourceGeneration = strings.TrimSpace(sourceGeneration)
	if streamName == "" || tenantID == "" || sourceGeneration == "" || sourceRevision <= 0 {
		return errors.New("federation stream untracking requires stream, tenant, generation, and positive revision")
	}
	tombstone := StreamPeerMembership{
		StreamName: streamName, TenantID: tenantID, SourceGeneration: sourceGeneration,
		SourceRevision: sourceRevision, Active: false, EndedAtUnixMilli: time.Now().UnixMilli(),
	}
	var err error
	tombstone, err = normalizeStreamPeerMembership(tombstone)
	if err != nil {
		return err
	}
	if pm.cache != nil {
		current, err := pm.cache.EndStreamPeerMembership(ctx, tombstone)
		if err != nil {
			return fmt.Errorf("end federation stream membership: %w", err)
		}
		if !current {
			return nil
		}
	}
	pm.mu.Lock()
	previous, tracked := pm.streamMemberships[streamName]
	if tracked && previous.SourceRevision > sourceRevision {
		pm.mu.Unlock()
		return nil
	}
	if tracked && previous.SourceRevision == sourceRevision && previous.SourceGeneration != sourceGeneration {
		pm.mu.Unlock()
		return errors.New("federation stream untracking revision conflicts with another generation")
	}
	if tracked {
		pm.removeStreamMembershipLocked(previous)
	}
	if pm.cache == nil {
		// Without shared Redis, the in-process tombstone is the only stale callback fence.
		pm.addStreamMembershipLocked(tombstone)
	}
	pm.closeUntrackedStreamScopedPeersLocked(previous.Peers, streamName)
	pm.mu.Unlock()
	return ctx.Err()
}

func streamPeerMembershipEqual(a, b StreamPeerMembership) bool {
	return a.Version == b.Version && a.StreamName == b.StreamName && a.TenantID == b.TenantID &&
		a.SourceGeneration == b.SourceGeneration && a.SourceRevision == b.SourceRevision &&
		a.Active == b.Active && slices.Equal(a.Peers, b.Peers)
}

func (pm *PeerManager) closeUntrackedStreamScopedPeersLocked(peers []StreamPeerTarget, streamName string) {
	for _, peer := range peers {
		ps, ok := pm.peers[peer.ClusterID]
		if !ok || ps.lifecycle != peerStreamScoped || len(pm.streamPeers[peer.ClusterID]) != 0 {
			continue
		}
		if ps.cancel != nil {
			ps.cancel()
		}
		delete(pm.peers, peer.ClusterID)
		pm.logger.WithFields(map[string]interface{}{
			"peer_cluster": peer.ClusterID,
			"stream":       streamName,
		}).Info("Closed stream-scoped peer (no remaining streams)")
	}
}

func (pm *PeerManager) addStreamMembershipLocked(membership StreamPeerMembership) {
	pm.streamMemberships[membership.StreamName] = membership
	if !membership.Active {
		return
	}
	pm.streamTenants[membership.StreamName] = membership.TenantID
	for _, peer := range membership.Peers {
		if pm.streamPeers[peer.ClusterID] == nil {
			pm.streamPeers[peer.ClusterID] = make(map[string]bool)
		}
		pm.streamPeers[peer.ClusterID][membership.StreamName] = true
		if pm.trackedTenantRefs[peer.ClusterID] == nil {
			pm.trackedTenantRefs[peer.ClusterID] = make(map[string]int)
		}
		pm.trackedTenantRefs[peer.ClusterID][membership.TenantID]++
		if pm.trackedAddrRefs[peer.ClusterID] == nil {
			pm.trackedAddrRefs[peer.ClusterID] = make(map[string]map[int64]int)
		}
		if pm.trackedAddrRefs[peer.ClusterID][peer.Addr] == nil {
			pm.trackedAddrRefs[peer.ClusterID][peer.Addr] = make(map[int64]int)
		}
		pm.trackedAddrRefs[peer.ClusterID][peer.Addr][membership.SourceRevision]++
		if peer.AlwaysOn {
			pm.trackedAlwaysOnRefs[peer.ClusterID]++
		}
	}
}

func (pm *PeerManager) removeStreamMembershipLocked(membership StreamPeerMembership) {
	delete(pm.streamMemberships, membership.StreamName)
	if !membership.Active {
		return
	}
	delete(pm.streamTenants, membership.StreamName)
	for _, peer := range membership.Peers {
		if streams := pm.streamPeers[peer.ClusterID]; streams != nil {
			delete(streams, membership.StreamName)
			if len(streams) == 0 {
				delete(pm.streamPeers, peer.ClusterID)
			}
		}
		decrementRef(pm.trackedTenantRefs[peer.ClusterID], membership.TenantID)
		if len(pm.trackedTenantRefs[peer.ClusterID]) == 0 {
			delete(pm.trackedTenantRefs, peer.ClusterID)
		}
		decrementRevisionRef(pm.trackedAddrRefs[peer.ClusterID][peer.Addr], membership.SourceRevision)
		if len(pm.trackedAddrRefs[peer.ClusterID][peer.Addr]) == 0 {
			delete(pm.trackedAddrRefs[peer.ClusterID], peer.Addr)
		}
		if len(pm.trackedAddrRefs[peer.ClusterID]) == 0 {
			delete(pm.trackedAddrRefs, peer.ClusterID)
		}
		if peer.AlwaysOn {
			pm.trackedAlwaysOnRefs[peer.ClusterID]--
			if pm.trackedAlwaysOnRefs[peer.ClusterID] <= 0 {
				delete(pm.trackedAlwaysOnRefs, peer.ClusterID)
			}
		}
	}
}

func decrementRevisionRef(refs map[int64]int, revision int64) {
	if refs == nil {
		return
	}
	refs[revision]--
	if refs[revision] <= 0 {
		delete(refs, revision)
	}
}

func decrementRef(refs map[string]int, key string) {
	if refs == nil {
		return
	}
	refs[key]--
	if refs[key] <= 0 {
		delete(refs, key)
	}
}

// loadStreamPeerMembershipsFromRedis installs only the exact active membership snapshot. Ended
// records remain in Redis until ledger-coordinated cleanup, but never inflate leader process state.
func (pm *PeerManager) loadStreamPeerMembershipsFromRedis() error {
	if pm.cache == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	all, err := pm.cache.LoadAllStreamPeerMemberships(ctx)
	if err != nil {
		return fmt.Errorf("load stream peers on leader takeover: %w", err)
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.streamPeers = make(map[string]map[string]bool)
	pm.streamTenants = make(map[string]string)
	pm.streamMemberships = make(map[string]StreamPeerMembership)
	pm.trackedTenantRefs = make(map[string]map[string]int)
	pm.trackedAddrRefs = make(map[string]map[string]map[int64]int)
	pm.trackedAlwaysOnRefs = make(map[string]int)
	for _, membership := range all {
		if !membership.Active {
			continue
		}
		filtered := membership.Peers[:0]
		for _, peer := range membership.Peers {
			if peer.ClusterID != pm.clusterID && !control.IsServedCluster(peer.ClusterID) {
				filtered = append(filtered, peer)
			}
		}
		membership.Peers = filtered
		pm.addStreamMembershipLocked(membership)
	}
	if len(pm.streamMemberships) > 0 {
		pm.logger.WithField("active_stream_count", len(pm.streamMemberships)).Info("Restored active stream-peer memberships from Redis")
	}
	return nil
}

func (pm *PeerManager) cleanupStreamMembershipTombstones(ctx context.Context) error {
	if pm.cache == nil || pm.canPurgeMemberships == nil {
		return nil
	}
	records, next, err := pm.cache.ScanEndedStreamPeerMemberships(ctx, pm.tombstoneScanCursor, streamPeerTombstoneScanCount)
	if err != nil {
		return err
	}
	pm.tombstoneScanCursor = next
	cutoff := time.Now().Add(-streamPeerTombstoneRetention).UnixMilli()
	candidates := make([]StreamPeerMembership, 0, len(records))
	fences := make([]control.AdmissionEffectFence, 0, len(records))
	for _, record := range records {
		if record.EndedAtUnixMilli > cutoff {
			continue
		}
		candidates = append(candidates, record)
		fences = append(fences, control.AdmissionEffectFence{
			TenantID: record.TenantID, InternalName: record.StreamName, SourceRevision: record.SourceRevision,
		})
	}
	if len(fences) == 0 {
		return nil
	}
	purgeable, err := pm.canPurgeMemberships(ctx, fences)
	if err != nil {
		return fmt.Errorf("prove ended membership cleanup safety: %w", err)
	}
	approved := make([]StreamPeerMembership, 0, len(candidates))
	for _, record := range candidates {
		if !purgeable[record.StreamName] {
			continue
		}
		approved = append(approved, record)
	}
	if len(approved) == 0 {
		return nil
	}
	if _, err := pm.cache.PurgeEndedStreamPeerMemberships(ctx, approved); err != nil {
		return err
	}
	return nil
}

const (
	peerRefreshInterval          = 5 * time.Minute // reconciliation only; demand-driven path handles fast discovery
	telemetryPushInterval        = 5 * time.Second
	summaryPushInterval          = 15 * time.Second
	artifactPushInterval         = 30 * time.Second
	peerReconnectBackoff         = 10 * time.Second
	streamPeerTombstoneRetention = time.Hour
	streamPeerTombstoneScanCount = int64(512)
	heartbeatPushInterval        = 10 * time.Second
	leaderAcquireInterval        = 5 * time.Second
	leaderRole                   = "peer_manager"
	protocolVersion              = uint32(1)
	// peerSendQueueSize bounds the per-peer writer mailbox. Every federation frame
	// is best-effort with its own backstop (periodic re-push, or a TTL on the
	// receiver's cache), so on overflow the oldest frame is evicted (latest-wins)
	// rather than stalling a producer that may hold pm.mu.
	peerSendQueueSize = 256
)

// run is the main goroutine. It loops trying to acquire the leader lease;
// once acquired it runs the active peering loop until the lease is lost.
// Non-leaders periodically sync peer addresses from Redis so that
// GetPeerAddr works on every replica.
func (pm *PeerManager) run() {
	for {
		select {
		case <-pm.done:
			return
		default:
		}

		if pm.tryAcquireLease() {
			pm.runAsLeader()
		}

		if err := pm.loadPeerAddressesFromRedis(); err != nil {
			pm.logger.WithError(err).Debug("Failed to load federation peer authority")
		}

		select {
		case <-pm.done:
			return
		case <-time.After(leaderAcquireInterval):
		}
	}
}

func (pm *PeerManager) tryAcquireLease() bool {
	if pm.cache == nil {
		return true // no Redis → single-instance mode, always leader
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return pm.cache.TryAcquireLeaderLease(ctx, leaderRole, pm.instanceID)
}

func (pm *PeerManager) renewLease() bool {
	if pm.cache == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return pm.cache.RenewLeaderLease(ctx, leaderRole, pm.instanceID)
}

// runAsLeader runs the full peering lifecycle: refresh peers, push telemetry,
// push summaries, and check replication completion. Returns when leadership
// is lost or pm.done is closed.
func (pm *PeerManager) runAsLeader() {
	pm.logger.WithField("instance_id", pm.instanceID).Info("Acquired PeerManager leadership")

	defer func() {
		pm.mu.Lock()
		wasReady := pm.leaderReady
		pm.isLeader = false
		pm.leaderReady = false
		pm.mu.Unlock()
		if wasReady {
			pm.emitFederationEvent(&ipcpb.FederationEventData{
				EventType: ipcpb.FederationEventType_LEADER_LOST,
				Role:      strPtr("peer_manager"),
			})
		}
		pm.disconnectAllPeers()
		if pm.cache != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := pm.cache.PublishPeerHints(ctx, pm.quartermasterContributorID(), map[string]PeerHint{}); err != nil {
				pm.logger.WithError(err).Debug("Failed to revoke Quartermaster peer contribution on leader exit")
			}
			pm.cache.ReleaseLeaderLease(ctx, leaderRole, pm.instanceID)
		}
		pm.logger.Info("Released PeerManager leadership")
	}()

	if err := pm.loadStreamPeerMembershipsFromRedis(); err != nil {
		pm.logger.WithError(err).Warn("Cannot reconstruct exact stream-peer state; relinquishing leadership")
		return
	}
	pm.mu.Lock()
	pm.quartermasterHints = make(map[string]PeerHint)
	pm.quartermasterHintsRefreshedAt = time.Time{}
	pm.isLeader = true
	pm.leaderReady = false
	pm.mu.Unlock()
	if err := pm.loadPeerAddressesFromRedis(); err != nil {
		pm.logger.WithError(err).Warn("Cannot establish exact federation authority; relinquishing leadership")
		return
	}
	pm.mu.Lock()
	pm.leaderReady = true
	pm.mu.Unlock()
	pm.emitFederationEvent(&ipcpb.FederationEventData{
		EventType: ipcpb.FederationEventType_LEADER_ACQUIRED,
		Role:      strPtr("peer_manager"),
	})
	pm.refreshPeers()

	refreshTicker := time.NewTicker(peerRefreshInterval)
	telemetryTicker := time.NewTicker(telemetryPushInterval)
	summaryTicker := time.NewTicker(summaryPushInterval)
	artifactTicker := time.NewTicker(artifactPushInterval)
	heartbeatTicker := time.NewTicker(heartbeatPushInterval)
	defer refreshTicker.Stop()
	defer telemetryTicker.Stop()
	defer summaryTicker.Stop()
	defer artifactTicker.Stop()
	defer heartbeatTicker.Stop()

	for {
		select {
		case <-pm.done:
			return
		case <-refreshTicker.C:
			// Replace the leader's authoritative Quartermaster contribution; refreshPeers then
			// reconciles it with independently leased demand discoveries.
			pm.refreshPeers()
		case <-telemetryTicker.C:
			if !pm.renewLease() {
				pm.logger.Warn("Lost PeerManager leader lease, stepping down")
				return
			}
			pm.pushTelemetry()
			pm.pushStreamAds()
			pm.checkReplicationCompletion()
		case <-summaryTicker.C:
			pm.pushSummary()
		case <-artifactTicker.C:
			pm.pushArtifacts()
		case <-heartbeatTicker.C:
			// Quartermaster remains leased. Active membership changes only through incremental
			// track/untrack records; proven-safe ended fences are collected separately below.
			pm.publishQuartermasterHints()
			if err := pm.loadPeerAddressesFromRedis(); err != nil {
				pm.logger.WithError(err).Warn("Failed to reconcile federation peer authority")
			}
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := pm.cleanupStreamMembershipTombstones(cleanupCtx); err != nil {
				pm.logger.WithError(err).Warn("Failed to clean ended stream-peer membership fences")
			}
			cleanupCancel()
			pm.pushHeartbeat()
		}
	}
}

// refreshPeers queries Quartermaster for peer clusters and manages connections.
func (pm *PeerManager) refreshPeers() {
	if pm.peerDiscovery == nil {
		pm.logger.Warn("Skipping federation peer refresh: peer discovery source is nil")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := pm.peerDiscovery.ListPeers(ctx, pm.clusterID)
	if err != nil {
		pm.logger.WithError(err).Warn("Failed to refresh federation peers")
		return
	}

	hints := make(map[string]PeerHint, len(resp.Peers))
	for _, peer := range resp.Peers {
		if strings.TrimSpace(peer.FoghornAddr) == "" || strings.TrimSpace(peer.ClusterId) == "" || peer.ClusterId == pm.clusterID {
			continue
		}
		hints[peer.ClusterId] = PeerHint{
			Addr:     peer.FoghornAddr,
			AlwaysOn: true,
			Tenants:  append([]string(nil), peer.SharedTenantIds...),
		}
	}
	pm.mu.Lock()
	pm.quartermasterHints = hints
	pm.quartermasterHintsRefreshedAt = time.Now()
	pm.mu.Unlock()
	pm.publishQuartermasterHints()
	if err := pm.loadPeerAddressesFromRedis(); err != nil {
		pm.logger.WithError(err).Warn("Failed to reconcile refreshed federation peers")
	}
}

func (pm *PeerManager) localPeerHints() map[string]PeerHint {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.localPeerHintsLocked()
}

func (pm *PeerManager) localPeerHintsLocked() map[string]PeerHint {
	hints := pm.trackedPeerHintsLocked()
	mergePeerHintMaps(hints, pm.quartermasterHints)
	return hints
}

func (pm *PeerManager) trackedPeerHintsLocked() map[string]PeerHint {
	hints := make(map[string]PeerHint, len(pm.trackedAddrRefs))
	for clusterID, addresses := range pm.trackedAddrRefs {
		tenants := pm.trackedTenantRefs[clusterID]
		if len(addresses) == 0 || len(tenants) == 0 {
			continue
		}
		addr, ok := unambiguousTrackedAddress(addresses)
		if !ok {
			// Publisher source revisions do not order topology observations. Omitting a conflicting
			// peer fails closed unless leased Quartermaster authority supplies it during the merge.
			continue
		}
		tenantList := make([]string, 0, len(tenants))
		for tenantID := range tenants {
			tenantList = append(tenantList, tenantID)
		}
		slices.Sort(tenantList)
		hints[clusterID] = PeerHint{
			Addr:     addr,
			AlwaysOn: pm.trackedAlwaysOnRefs[clusterID] > 0,
			Tenants:  tenantList,
		}
	}
	return hints
}

func unambiguousTrackedAddress(addresses map[string]map[int64]int) (string, bool) {
	var selected string
	for addr, revisions := range addresses {
		for _, count := range revisions {
			if count > 0 {
				if selected != "" && selected != addr {
					return "", false
				}
				selected = addr
				break
			}
		}
	}
	return selected, selected != ""
}

func (pm *PeerManager) publishQuartermasterHints() {
	pm.mu.Lock()
	if pm.quartermasterHintsRefreshedAt.IsZero() || time.Since(pm.quartermasterHintsRefreshedAt) > peerRefreshInterval+peerAddrTTL {
		pm.quartermasterHints = make(map[string]PeerHint)
	}
	hints := make(map[string]PeerHint, len(pm.quartermasterHints))
	for clusterID, hint := range pm.quartermasterHints {
		hints[clusterID] = hint
	}
	pm.mu.Unlock()
	if pm.cache == nil {
		if err := pm.loadPeerAddressesFromRedis(); err != nil {
			pm.logger.WithError(err).Warn("Failed to reconcile local federation peers")
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := pm.cache.PublishPeerHints(ctx, pm.quartermasterContributorID(), hints); err != nil {
		pm.logger.WithError(err).Warn("Failed to publish authoritative Quartermaster peer snapshot")
	}
}

// loadPeerAddressesFromRedis reconciles leased Quartermaster authority with non-expiring active
// stream memberships. A leader uses its exact in-memory membership snapshot; non-leaders read the
// same authoritative Redis hash so GetPeerAddr remains available on every replica.
func (pm *PeerManager) loadPeerAddressesFromRedis() error {
	if pm.cache == nil {
		pm.mu.Lock()
		toConnect := pm.reconcilePeerHintsLocked(pm.localPeerHintsLocked())
		pm.mu.Unlock()
		pm.connectAuthorityPeers(toConnect)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	addrs, err := pm.cache.GetPeerAddresses(ctx)
	if err != nil {
		return fmt.Errorf("load leased peer addresses: %w", err)
	}
	if pm.reconcileLeaderPeerHints(addrs) {
		return nil
	}

	memberships, loadErr := pm.cache.LoadAllStreamPeerMemberships(ctx)
	if loadErr != nil {
		return fmt.Errorf("load active stream peer addresses: %w", loadErr)
	}
	membershipHints := peerHintsFromMemberships(memberships)
	mergePeerHintMaps(membershipHints, addrs)

	pm.reconcileLoadedPeerHints(membershipHints, addrs)
	return nil
}

// reconcileLeaderPeerHints merges externally loaded leases with current process-local membership
// and applies the resulting authority without releasing pm.mu between recomputation and mutation.
func (pm *PeerManager) reconcileLeaderPeerHints(external map[string]PeerHint) bool {
	pm.mu.Lock()
	if !pm.isLeader {
		pm.mu.Unlock()
		return false
	}
	hints := pm.localPeerHintsLocked()
	mergePeerHintMaps(hints, external)
	toConnect := pm.reconcilePeerHintsLocked(hints)
	pm.mu.Unlock()
	pm.connectAuthorityPeers(toConnect)
	return true
}

func (pm *PeerManager) reconcileLoadedPeerHints(membershipHints, external map[string]PeerHint) {
	pm.mu.Lock()
	if pm.isLeader {
		// Leadership changed during Redis I/O. Discard the external membership snapshot rather than
		// applying it over the leader's newer process-local tracking state.
		membershipHints = pm.localPeerHintsLocked()
		mergePeerHintMaps(membershipHints, external)
	}
	toConnect := pm.reconcilePeerHintsLocked(membershipHints)
	pm.mu.Unlock()
	pm.connectAuthorityPeers(toConnect)
}

func peerHintsFromMemberships(memberships map[string]StreamPeerMembership) map[string]PeerHint {
	hints := make(map[string]PeerHint)
	addresses := make(map[string]map[string]bool)
	tenants := make(map[string]map[string]bool)
	for _, membership := range memberships {
		if !membership.Active {
			continue
		}
		for _, peer := range membership.Peers {
			hint := hints[peer.ClusterID]
			hint.AlwaysOn = hint.AlwaysOn || peer.AlwaysOn
			if addresses[peer.ClusterID] == nil {
				addresses[peer.ClusterID] = make(map[string]bool)
			}
			addresses[peer.ClusterID][peer.Addr] = true
			if tenants[peer.ClusterID] == nil {
				tenants[peer.ClusterID] = make(map[string]bool)
			}
			tenants[peer.ClusterID][membership.TenantID] = true
			hints[peer.ClusterID] = hint
		}
	}
	for clusterID, hint := range hints {
		if len(addresses[clusterID]) != 1 {
			// Publisher revisions order source ownership, not Quartermaster topology. Conflicting
			// captured endpoints contribute no address authority; a current leased Quartermaster
			// hint may still supply the peer when maps are merged by the caller.
			delete(hints, clusterID)
			continue
		}
		hint.Tenants = make([]string, 0, len(tenants[clusterID]))
		for tenantID := range tenants[clusterID] {
			hint.Tenants = append(hint.Tenants, tenantID)
		}
		slices.Sort(hint.Tenants)
		for addr := range addresses[clusterID] {
			hint.Addr = addr
		}
		hints[clusterID] = hint
	}
	return hints
}

func mergePeerHintMaps(target, incoming map[string]PeerHint) {
	for clusterID, next := range incoming {
		current, exists := target[clusterID]
		currentUnrestricted := exists && current.AlwaysOn && len(current.Tenants) == 0
		nextUnrestricted := next.AlwaysOn && len(next.Tenants) == 0
		if !exists || next.AlwaysOn || !current.AlwaysOn {
			current.Addr = next.Addr
		}
		current.AlwaysOn = current.AlwaysOn || next.AlwaysOn
		if currentUnrestricted {
			// Empty tenant scope on an always-on authority means unrestricted.
		} else if nextUnrestricted {
			current.Tenants = nil
		} else {
			tenantSet := make(map[string]bool, len(current.Tenants)+len(next.Tenants))
			for _, tenantID := range current.Tenants {
				if tenantID != "" {
					tenantSet[tenantID] = true
				}
			}
			for _, tenantID := range next.Tenants {
				if tenantID != "" {
					tenantSet[tenantID] = true
				}
			}
			current.Tenants = current.Tenants[:0]
			for tenantID := range tenantSet {
				current.Tenants = append(current.Tenants, tenantID)
			}
			slices.Sort(current.Tenants)
		}
		target[clusterID] = current
	}
}

func (pm *PeerManager) reconcilePeerHintsLocked(hints map[string]PeerHint) []peerConnectRequest {
	toConnect := make([]peerConnectRequest, 0)
	for clusterID, hint := range hints {
		if clusterID == pm.clusterID {
			continue
		}
		lifecycle := peerStreamScoped
		if hint.AlwaysOn {
			lifecycle = peerAlwaysOn
		}
		if existing := pm.peers[clusterID]; existing != nil {
			if existing.addr != hint.Addr {
				existing.addr = hint.Addr
				if existing.cancel != nil {
					existing.cancel()
				}
			}
			replacePeerTenants(existing, hint.Tenants)
			existing.lifecycle = lifecycle
			existing.lastRefresh = time.Now()
			if pm.isLeader {
				if request, reserved := pm.reservePeerRunnerLocked(clusterID, existing); reserved {
					toConnect = append(toConnect, request)
				}
			}
			continue
		}
		ps := &peerState{addr: hint.Addr, lifecycle: lifecycle, lastRefresh: time.Now()}
		replacePeerTenants(ps, hint.Tenants)
		pm.peers[clusterID] = ps
		if pm.isLeader {
			if request, reserved := pm.reservePeerRunnerLocked(clusterID, ps); reserved {
				toConnect = append(toConnect, request)
			}
		}
	}
	for clusterID, ps := range pm.peers {
		if _, present := hints[clusterID]; present {
			continue
		}
		if ps.cancel != nil {
			ps.cancel()
		}
		delete(pm.peers, clusterID)
		pm.logger.WithField("peer_cluster", clusterID).Info("Removed peer whose authority lease expired or was revoked")
	}
	return toConnect
}

// importAdmissionPeerHintsLocked adds the peers carried by one admission obligation without
// replacing unrelated authority. The caller holds pm.mu across membership mutation, authority
// recomputation, and peer mutation, so an older import cannot apply a stale endpoint snapshot.
func (pm *PeerManager) importAdmissionPeerHintsLocked(hints map[string]PeerHint) []peerConnectRequest {
	// The obligation's address is discovery input, not perpetual endpoint authority. Resolve every
	// touched cluster from the complete active membership set. Conflicting captured addresses are
	// omitted unless current leased Quartermaster authority resolves them.
	touched := make([]string, 0, len(hints))
	for clusterID := range hints {
		touched = append(touched, clusterID)
	}
	authoritative := pm.localPeerHintsLocked()
	for clusterID := range hints {
		if hint, ok := authoritative[clusterID]; ok {
			hints[clusterID] = hint
		} else {
			delete(hints, clusterID)
		}
	}
	for _, clusterID := range touched {
		if _, resolved := hints[clusterID]; resolved {
			continue
		}
		if existing := pm.peers[clusterID]; existing != nil {
			if existing.cancel != nil {
				existing.cancel()
			}
			delete(pm.peers, clusterID)
		}
	}
	toConnect := make([]peerConnectRequest, 0, len(hints))
	for clusterID, hint := range hints {
		lifecycle := peerStreamScoped
		if hint.AlwaysOn {
			lifecycle = peerAlwaysOn
		}
		if existing := pm.peers[clusterID]; existing != nil {
			if existing.lifecycle != peerAlwaysOn || lifecycle == peerAlwaysOn {
				if existing.addr != hint.Addr {
					existing.addr = hint.Addr
					if existing.cancel != nil {
						existing.cancel()
					}
				}
			}
			if existing.lifecycle != peerAlwaysOn {
				existing.lifecycle = lifecycle
			}
			if existing.lifecycle != peerAlwaysOn || len(existing.tenantIDs) != 0 {
				for _, tenantID := range hint.Tenants {
					if !peerHasTenant(existing, tenantID) {
						if existing.tenantSet == nil {
							replacePeerTenants(existing, existing.tenantIDs)
						}
						existing.tenantIDs = append(existing.tenantIDs, tenantID)
						existing.tenantSet[tenantID] = struct{}{}
					}
				}
			}
			existing.lastRefresh = time.Now()
			if pm.isLeader {
				if request, reserved := pm.reservePeerRunnerLocked(clusterID, existing); reserved {
					toConnect = append(toConnect, request)
				}
			}
			continue
		}
		state := &peerState{addr: hint.Addr, lifecycle: lifecycle, lastRefresh: time.Now()}
		replacePeerTenants(state, hint.Tenants)
		pm.peers[clusterID] = state
		if pm.isLeader {
			if request, reserved := pm.reservePeerRunnerLocked(clusterID, state); reserved {
				toConnect = append(toConnect, request)
			}
		}
	}
	return toConnect
}

func (pm *PeerManager) reservePeerRunnerLocked(clusterID string, ps *peerState) (peerConnectRequest, bool) {
	if ps.connected || ps.runnerToken != 0 {
		return peerConnectRequest{}, false
	}
	pm.nextPeerRunnerToken++
	if pm.nextPeerRunnerToken == 0 {
		pm.nextPeerRunnerToken++
	}
	ps.runnerToken = pm.nextPeerRunnerToken
	return peerConnectRequest{clusterID: clusterID, state: ps, token: ps.runnerToken}, true
}

func (pm *PeerManager) connectAuthorityPeers(peers []peerConnectRequest) {
	for _, peer := range peers {
		go pm.connectPeer(peer)
	}
}

// connectPeer opens a PeerChannel to the given peer and runs the receive loop.
func (pm *PeerManager) connectPeer(request peerConnectRequest) {
	clusterID, ps, token := request.clusterID, request.state, request.token
	defer pm.releasePeerRunner(request)
	if pm.pool == nil {
		pm.logger.WithField("peer_cluster", clusterID).Warn("Skipping peer connect: foghorn pool is nil")
		return
	}
	backoff := pm.reconnectBackoff
	if backoff <= 0 {
		backoff = peerReconnectBackoff
	}

	for {
		select {
		case <-pm.done:
			return
		default:
		}

		ctx, cancel := context.WithCancel(context.Background())

		pm.mu.Lock()
		current, ok := pm.peers[clusterID]
		if !ok || current != ps || ps.runnerToken != token {
			pm.mu.Unlock()
			cancel()
			return
		}
		ps.cancel = cancel
		addr := ps.addr
		pm.mu.Unlock()

		client, err := pm.pool.GetOrCreate(clusterID, addr)
		if err != nil {
			pm.logger.WithError(err).WithField("peer_cluster", clusterID).Warn("Failed to get Foghorn client for peer")
			cancel()
			time.Sleep(backoff)
			continue
		}

		stream, err := client.OpenPeerChannel(ctx)
		if err != nil {
			pm.logger.WithError(err).WithField("peer_cluster", clusterID).Warn("Failed to open PeerChannel")
			cancel()
			time.Sleep(backoff)
			continue
		}

		pm.mu.Lock()
		current, ok = pm.peers[clusterID]
		if !ok || current != ps || ps.runnerToken != token || ctx.Err() != nil {
			pm.mu.Unlock()
			cancel()
			continue
		}
		ps.stream = stream
		ps.sendCh = make(chan *foghornfederationpb.PeerMessage, peerSendQueueSize)
		ps.connected = true
		sendCh := ps.sendCh
		pm.mu.Unlock()

		// One writer goroutine per connection. Every producer — the leader push
		// loops and the trigger-path BroadcastStreamLifecycle — enqueues onto
		// sendCh; only this goroutine calls stream.Send. That keeps gRPC Sends
		// non-concurrent and stops a wedged peer from stalling a producer that
		// holds pm.mu.
		go pm.peerWriteLoop(ctx, clusterID, sendCh, stream, cancel)

		pm.logger.WithField("peer_cluster", clusterID).Info("PeerChannel connected")
		pm.emitFederationEvent(&ipcpb.FederationEventData{
			EventType:   ipcpb.FederationEventType_PEER_CONNECTED,
			PeerCluster: &clusterID,
		})

		// Receive loop — processes incoming messages until the stream closes
		pm.recvLoop(clusterID, stream)

		pm.mu.Lock()
		current, ok = pm.peers[clusterID]
		owned := ok && current == ps && ps.runnerToken == token
		if owned {
			ps.connected = false
			ps.stream = nil
			ps.sendCh = nil
		}
		pm.mu.Unlock()

		cancel() // also unblocks peerWriteLoop via ctx
		if !owned {
			return
		}

		pm.logger.WithField("peer_cluster", clusterID).Info("PeerChannel disconnected, will reconnect")
		pm.emitFederationEvent(&ipcpb.FederationEventData{
			EventType:   ipcpb.FederationEventType_PEER_DISCONNECTED,
			PeerCluster: &clusterID,
		})
		time.Sleep(backoff)
	}
}

func (pm *PeerManager) releasePeerRunner(request peerConnectRequest) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	current := pm.peers[request.clusterID]
	if current != request.state || current.runnerToken != request.token {
		return
	}
	if current.cancel != nil {
		current.cancel()
	}
	current.cancel = nil
	current.connected = false
	current.stream = nil
	current.sendCh = nil
	current.runnerToken = 0
}

func (pm *PeerManager) touchPool(clusterID string) {
	if pm.pool != nil {
		pm.pool.Touch(clusterID)
	}
}

// enqueue offers a frame to a peer's writer goroutine without blocking. Callers
// may hold pm.mu (R or W): the channel op never blocks. Producers must never call
// stream.Send directly — the single-writer invariant is what keeps gRPC Sends
// non-concurrent and keeps a backpressured peer from stalling the leader loop.
//
// Backpressure policy is latest-wins (drop-oldest). Every federation frame is
// best-effort with its own backstop, so evicting one under load is safe and
// dropping the newest (letting a stale frame drain later) would be worse:
//   - telemetry/summary/heartbeat/ads, live StreamAds, and live-lifecycle events
//     re-push every periodic tick;
//   - an offline-lifecycle event is backstopped by the receiver's RemoteLiveStream
//     30s TTL, which expires on its own once the live re-push stops;
//   - a ReplicationEvent is an ephemeral loop-prevention hint (RemoteReplication
//     5min TTL, never refreshed) — losing it only risks a redundant origin pull.
//
// No frame needs guaranteed delivery, so there is deliberately one mailbox.
func (pm *PeerManager) enqueue(peerID string, ps *peerState, msg *foghornfederationpb.PeerMessage) bool {
	if !ps.connected || ps.sendCh == nil {
		return false
	}
	// Fast path: room in the mailbox.
	select {
	case ps.sendCh <- msg:
		return true
	default:
	}
	// Full: evict the oldest queued frame to make room for the fresher one.
	select {
	case <-ps.sendCh:
		if n := ps.dropped.Add(1); n%peerSendQueueSize == 1 {
			pm.logger.WithFields(logging.Fields{"peer_cluster": peerID, "dropped_total": n}).Debug("peer send queue full; evicting oldest frame (latest-wins)")
		}
	default:
	}
	// Enqueue the fresh frame; if a concurrent producer refilled the slot, drop
	// this one rather than block.
	select {
	case ps.sendCh <- msg:
		return true
	default:
		ps.dropped.Add(1)
		return false
	}
}

// peerWriteLoop is the sole sender on a peer's stream. It drains the mailbox
// until the connection context is cancelled or a Send fails; a Send failure
// cancels the context so recvLoop unwinds and connectPeer reconnects.
func (pm *PeerManager) peerWriteLoop(ctx context.Context, peerID string, sendCh <-chan *foghornfederationpb.PeerMessage, stream foghornfederationpb.FoghornFederation_PeerChannelClient, cancel context.CancelFunc) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-sendCh:
			if !ok {
				return
			}
			if err := stream.Send(msg); err != nil {
				pm.logger.WithError(err).WithField("peer_cluster", peerID).Debug("peer writer Send failed; tearing down connection")
				cancel()
				return
			}
		}
	}
}

// recvLoop reads PeerMessages from the stream and writes telemetry to Redis.
func (pm *PeerManager) recvLoop(peerClusterID string, stream foghornfederationpb.FoghornFederation_PeerChannelClient) {
	for {
		msg, err := stream.Recv()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				pm.logger.WithError(err).WithField("peer_cluster", peerClusterID).Debug("PeerChannel recv error")
			}
			return
		}

		if pm.cache == nil {
			continue
		}

		ctx := context.Background()

		switch payload := msg.Payload.(type) {
		case *foghornfederationpb.PeerMessage_EdgeTelemetry:
			t := payload.EdgeTelemetry
			entry := &RemoteEdgeEntry{
				StreamName:  t.StreamName,
				NodeID:      t.NodeId,
				BaseURL:     t.BaseUrl,
				BWAvailable: t.BwAvailable,
				ViewerCount: t.ViewerCount,
				CPUPercent:  t.CpuPercent,
				RAMUsed:     t.RamUsed,
				RAMMax:      t.RamMax,
				GeoLat:      t.GeoLat,
				GeoLon:      t.GeoLon,
				UpdatedAt:   time.Now().Unix(),
			}
			if err := pm.cache.SetRemoteEdge(ctx, peerClusterID, entry); err != nil {
				pm.logger.WithError(err).Debug("Failed to cache remote edge from PeerChannel")
			}

		case *foghornfederationpb.PeerMessage_ReplicationEvent:
			r := payload.ReplicationEvent
			entry := &RemoteReplicationEntry{
				StreamName: r.StreamName,
				NodeID:     r.NodeId,
				ClusterID:  r.ClusterId,
				BaseURL:    r.BaseUrl,
				DTSCURL:    r.DtscUrl,
				Available:  r.Available,
				UpdatedAt:  time.Now().Unix(),
			}
			if err := pm.cache.SetRemoteReplication(ctx, peerClusterID, entry); err != nil {
				pm.logger.WithError(err).Debug("Failed to cache remote replication from PeerChannel")
			}

		case *foghornfederationpb.PeerMessage_ClusterSummary:
			summary := payload.ClusterSummary
			edges := make([]*EdgeSummaryEntry, 0, len(summary.Edges))
			for _, e := range summary.Edges {
				edges = append(edges, &EdgeSummaryEntry{
					NodeID:         e.NodeId,
					BaseURL:        e.BaseUrl,
					GeoLat:         e.GeoLat,
					GeoLon:         e.GeoLon,
					BWAvailableAvg: e.BwAvailableAvg,
					CPUPercentAvg:  e.CpuPercentAvg,
					RAMUsed:        e.RamUsed,
					RAMMax:         e.RamMax,
					TotalViewers:   e.TotalViewers,
					Roles:          e.Roles,
				})
			}
			record := &EdgeSummaryRecord{
				Edges:     edges,
				Timestamp: summary.Timestamp,
			}
			if err := pm.cache.SetEdgeSummary(ctx, peerClusterID, record); err != nil {
				pm.logger.WithError(err).Debug("Failed to cache cluster summary from PeerChannel")
			}

		case *foghornfederationpb.PeerMessage_StreamLifecycle:
			ev := payload.StreamLifecycle
			if ev == nil || strings.TrimSpace(ev.GetClusterId()) != peerClusterID {
				pm.logger.WithFields(logging.Fields{
					"peer_cluster":    peerClusterID,
					"claimed_cluster": ev.GetClusterId(),
				}).Warn("Rejected stream lifecycle with mismatched PeerChannel identity")
				continue
			}
			if _, err := pm.cache.ApplyRemoteStreamLifecycle(ctx, ev.GetTenantId(), ev.GetInternalName(), &RemoteLiveStreamEntry{
				ClusterID:      peerClusterID,
				TenantID:       ev.GetTenantId(),
				SourceRevision: ev.GetSourceRevision(),
				UpdatedAt:      time.Now().Unix(),
			}, ev.GetIsLive()); err != nil {
				pm.logger.WithError(err).Debug("Failed to apply remote stream lifecycle from PeerChannel")
			}

		case *foghornfederationpb.PeerMessage_StreamAd:
			ad := payload.StreamAd
			if ad != nil {
				// Mirror PeerChannel-delivered ads into the unified stream
				// registry so they land in the same inventory as
				// gRPC-delivered ones (handleStreamAdvertisement does the
				// same).
				if control.StreamRegistryInstance != nil {
					ecands := make([]control.EdgeCandidate, 0, len(ad.Edges))
					for _, e := range ad.Edges {
						ecands = append(ecands, control.EdgeCandidate{
							NodeID:      e.NodeId,
							BaseURL:     e.BaseUrl,
							DTSCURL:     e.DtscUrl,
							IsOrigin:    e.IsOrigin,
							BWAvailable: int64(e.BwAvailable),
							CPUPercent:  e.CpuPercent,
							ViewerCount: int32(e.ViewerCount),
							GeoLat:      e.GeoLat,
							GeoLon:      e.GeoLon,
							BufferState: e.BufferState,
						})
					}
					originCluster := ad.OriginClusterId
					if originCluster == "" {
						originCluster = peerClusterID
					}
					control.StreamRegistryInstance.UpsertFederatedSource(
						peerClusterID,
						control.StreamEntry{
							TenantID:        ad.TenantId,
							PlaybackID:      ad.PlaybackId,
							InternalName:    ad.InternalName,
							OriginClusterID: originCluster,
						},
						control.Location{
							IsLiveNow:       ad.IsLive,
							AdTimestamp:     ad.Timestamp,
							EdgeCandidates:  ecands,
							RecordingNodeID: ad.DvrRecordingNodeId,
						},
					)
				}
			}

		case *foghornfederationpb.PeerMessage_ArtifactAd:
			ad := payload.ArtifactAd
			if ad != nil {
				for _, loc := range ad.Artifacts {
					entry := &RemoteArtifactEntry{
						ArtifactHash: loc.ArtifactHash,
						ArtifactType: loc.ArtifactType,
						NodeID:       loc.NodeId,
						BaseURL:      loc.BaseUrl,
						SizeBytes:    loc.SizeBytes,
						AccessCount:  loc.AccessCount,
						LastAccessed: loc.LastAccessed,
						GeoLat:       loc.GeoLat,
						GeoLon:       loc.GeoLon,
						UpdatedAt:    time.Now().Unix(),
						TenantID:     loc.TenantId,
					}
					if err := pm.cache.SetRemoteArtifact(ctx, peerClusterID, entry); err != nil {
						pm.logger.WithError(err).Debug("Failed to cache remote artifact from PeerChannel")
					}
				}
			}

		case *foghornfederationpb.PeerMessage_PeerHeartbeat:
			hb := payload.PeerHeartbeat
			if hb != nil {
				record := &PeerHeartbeatRecord{
					ProtocolVersion:  hb.ProtocolVersion,
					StreamCount:      hb.StreamCount,
					TotalBWAvailable: hb.TotalBwAvailable,
					EdgeCount:        hb.EdgeCount,
					UptimeSeconds:    hb.UptimeSeconds,
					Capabilities:     hb.Capabilities,
					Lat:              hb.FoghornLat,
					Lon:              hb.FoghornLon,
					Location:         hb.FoghornLocation,
				}
				if err := pm.cache.SetPeerHeartbeat(ctx, peerClusterID, record); err != nil {
					pm.logger.WithError(err).Debug("Failed to cache peer heartbeat from PeerChannel")
				}
				pm.mu.Lock()
				if ps, ok := pm.peers[peerClusterID]; ok {
					ps.lat = hb.FoghornLat
					ps.lon = hb.FoghornLon
					ps.location = hb.FoghornLocation
				}
				pm.mu.Unlock()
			}

		case *foghornfederationpb.PeerMessage_CapacitySummary:
			// CapacitySummary received — stored when handler is implemented
		}
	}
}

// pushTelemetry sends EdgeTelemetry for locally active replicated streams
// to all connected peers every 5s.
func (pm *PeerManager) pushTelemetry() {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	// Collect nodes with active replications from local state
	sm := state.DefaultManager()
	if sm == nil {
		return
	}

	snapshot := sm.GetBalancerSnapshotAtomic()
	if snapshot == nil {
		return
	}

	// Build telemetry messages for nodes that have streams
	var messages []*foghornfederationpb.PeerMessage
	for _, snap := range snapshot.Nodes {
		if !snap.IsActive || len(snap.Streams) == 0 {
			continue
		}

		ns := sm.GetNodeState(snap.NodeID)
		if ns == nil {
			continue
		}

		for streamName := range snap.Streams {
			msg := &foghornfederationpb.PeerMessage{
				ClusterId: pm.clusterID,
				Payload: &foghornfederationpb.PeerMessage_EdgeTelemetry{
					EdgeTelemetry: &foghornfederationpb.EdgeTelemetry{
						StreamName:  streamName,
						NodeId:      snap.NodeID,
						BaseUrl:     ns.BaseURL,
						BwAvailable: snap.BWAvailable,
						ViewerCount: uint32(sm.GetNodeActiveViewers(snap.NodeID)),
						CpuPercent:  snap.CPU,
						RamUsed:     uint64(snap.RAMCurrent),
						RamMax:      uint64(snap.RAMMax),
						GeoLat:      snap.GeoLatitude,
						GeoLon:      snap.GeoLongitude,
					},
				},
			}
			messages = append(messages, msg)
		}
	}

	if len(messages) == 0 {
		return
	}

	// Send to all connected peers
	for peerID, ps := range pm.peers {
		if !ps.connected || ps.stream == nil {
			continue
		}
		pm.touchPool(peerID)
		for _, msg := range messages {
			tel, ok := msg.GetPayload().(*foghornfederationpb.PeerMessage_EdgeTelemetry)
			if !ok || tel.EdgeTelemetry == nil {
				continue
			}
			ss := sm.GetStreamState(tel.EdgeTelemetry.StreamName)
			tenantID := ""
			if ss != nil {
				tenantID = ss.TenantID
			}
			if !pm.shouldSendStreamToPeer(peerID, ps, tel.EdgeTelemetry.StreamName, tenantID) {
				continue
			}
			pm.enqueue(peerID, ps, msg)
		}
	}

	// Heartbeat: re-broadcast lifecycle events for all locally live streams.
	// Refreshes the 30s TTL on peer clusters' Redis keys. Dedup by stream name
	// since the same stream may be on multiple nodes.
	seen := make(map[string]bool)
	now := time.Now().Unix()
	for _, ss := range sm.GetAllStreamStates() {
		if ss.Status != "live" || seen[ss.InternalName] {
			continue
		}
		membership, tracked := pm.streamMemberships[ss.InternalName]
		if !tracked || !membership.Active || membership.SourceRevision <= 0 {
			continue
		}
		seen[ss.InternalName] = true
		lifecycleMsg := &foghornfederationpb.PeerMessage{
			ClusterId: pm.clusterID,
			Payload: &foghornfederationpb.PeerMessage_StreamLifecycle{
				StreamLifecycle: &foghornfederationpb.StreamLifecycleEvent{
					InternalName:   ss.InternalName,
					TenantId:       ss.TenantID,
					ClusterId:      pm.clusterID,
					IsLive:         true,
					TimestampUnix:  now,
					SourceRevision: membership.SourceRevision,
				},
			},
		}
		for peerID, ps := range pm.peers {
			if !ps.connected || ps.stream == nil {
				continue
			}
			if !pm.shouldSendStreamToPeer(peerID, ps, ss.InternalName, ss.TenantID) {
				continue
			}
			pm.enqueue(peerID, ps, lifecycleMsg)
		}
	}
}

const metricWindowDuration = 30 * time.Second

// recordAndAverage records a BW/CPU sample for a node and returns the 30s moving average.
func (pm *PeerManager) recordAndAverage(nodeID string, bw uint64, cpu float64) (uint64, float64) {
	now := time.Now()
	cutoff := now.Add(-metricWindowDuration)

	samples := pm.metricHistory[nodeID]
	// Prune expired samples
	n := 0
	for _, s := range samples {
		if s.ts.After(cutoff) {
			samples[n] = s
			n++
		}
	}
	samples = samples[:n]

	samples = append(samples, metricSample{bwAvailable: bw, cpuPercent: cpu, ts: now})
	pm.metricHistory[nodeID] = samples

	var bwSum uint64
	var cpuSum float64
	for _, s := range samples {
		bwSum += s.bwAvailable
		cpuSum += s.cpuPercent
	}
	count := uint64(len(samples))
	return bwSum / count, cpuSum / float64(count)
}

// pushSummary sends a ClusterEdgeSummary with 30s-averaged node metrics to all connected peers.
func (pm *PeerManager) pushSummary() {
	sm := state.DefaultManager()
	if sm == nil {
		return
	}

	snapshot := sm.GetBalancerSnapshotAtomic()
	if snapshot == nil {
		return
	}

	var edges []*foghornfederationpb.EdgeSnapshot
	for _, snap := range snapshot.Nodes {
		if !snap.IsActive || snap.BWAvailable == 0 {
			continue
		}
		ns := sm.GetNodeState(snap.NodeID)
		if ns == nil {
			continue
		}
		bwAvg, cpuAvg := pm.recordAndAverage(snap.NodeID, snap.BWAvailable, snap.CPU)
		edges = append(edges, &foghornfederationpb.EdgeSnapshot{
			NodeId:         snap.NodeID,
			BaseUrl:        ns.BaseURL,
			GeoLat:         snap.GeoLatitude,
			GeoLon:         snap.GeoLongitude,
			BwAvailableAvg: bwAvg,
			CpuPercentAvg:  cpuAvg,
			RamUsed:        uint64(snap.RAMCurrent),
			RamMax:         uint64(snap.RAMMax),
			TotalViewers:   uint32(sm.GetNodeActiveViewers(snap.NodeID)),
			Roles:          append([]string(nil), snap.Roles...),
		})
	}

	if len(edges) == 0 {
		return
	}

	msg := &foghornfederationpb.PeerMessage{
		ClusterId: pm.clusterID,
		Payload: &foghornfederationpb.PeerMessage_ClusterSummary{
			ClusterSummary: &foghornfederationpb.ClusterEdgeSummary{
				Edges:     edges,
				Timestamp: time.Now().Unix(),
			},
		},
	}

	pm.mu.RLock()
	defer pm.mu.RUnlock()

	for peerID, ps := range pm.peers {
		if !ps.connected || ps.stream == nil {
			continue
		}
		pm.touchPool(peerID)
		pm.enqueue(peerID, ps, msg)
	}
}

func artifactTypeToString(t ipcpb.ArtifactEvent_ArtifactType) string {
	switch t {
	case ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP:
		return "clip"
	case ipcpb.ArtifactEvent_ARTIFACT_TYPE_DVR:
		return "dvr"
	case ipcpb.ArtifactEvent_ARTIFACT_TYPE_VOD:
		return "vod"
	default:
		return "clip"
	}
}

// NewDBArtifactTenantResolver returns an ArtifactTenantResolver backed by the
// foghorn.artifacts registry — the authority for artifact→tenant attribution.
func NewDBArtifactTenantResolver(db *sql.DB) func(ctx context.Context, hashes []string) (map[string]string, error) {
	return func(ctx context.Context, hashes []string) (map[string]string, error) {
		if db == nil || len(hashes) == 0 {
			return nil, nil
		}
		rows, err := foghorndb.New(db).ResolveArtifactTenants(ctx, hashes)
		if err != nil {
			return nil, err
		}
		tenants := make(map[string]string, len(hashes))
		for _, row := range rows {
			tenants[row.ArtifactHash] = row.TenantID
		}
		return tenants, nil
	}
}

// resolveArtifactTenants batch-resolves artifact hashes to tenant ids via the
// configured registry resolver. Returns an empty map when no resolver is set
// or the lookup fails (callers then skip those ads).
func (pm *PeerManager) resolveArtifactTenants(hashes []string) map[string]string {
	if len(hashes) == 0 || pm.artifactTenantResolver == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tenants, err := pm.artifactTenantResolver(ctx, hashes)
	if err != nil {
		pm.logger.WithError(err).Warn("pushArtifacts: artifact tenant registry lookup failed")
		return nil
	}
	return tenants
}

// shouldLogUnresolvedAd throttles the per-cycle skip warning: the ad loop runs
// every 30s and an unattributable artifact stays unattributable, so log each
// hash at most once an hour.
func (pm *PeerManager) shouldLogUnresolvedAd(hash string) bool {
	const logInterval = time.Hour
	now := time.Now()
	pm.unresolvedAdMu.Lock()
	defer pm.unresolvedAdMu.Unlock()
	if last, ok := pm.unresolvedAdLogged[hash]; ok && now.Sub(last) < logInterval {
		return false
	}
	// Drop stale entries so the map doesn't grow with long-evicted artifacts.
	for h, last := range pm.unresolvedAdLogged {
		if now.Sub(last) >= logInterval {
			delete(pm.unresolvedAdLogged, h)
		}
	}
	pm.unresolvedAdLogged[hash] = now
	return true
}

// pushArtifacts sends an ArtifactAdvertisement with all hot artifacts across all
// local edge nodes to connected peers. Sent every 30s. Artifact hashes are opaque
// identifiers — the receiving cluster only uses them when it has a matching
// authenticated playback request through Commodore.
func (pm *PeerManager) pushArtifacts() {
	sm := state.DefaultManager()
	if sm == nil {
		return
	}

	snapshot := sm.GetBalancerSnapshotAtomic()
	if snapshot == nil {
		return
	}

	// Pass 1: collect candidate ads, attributing tenants from in-memory
	// stream state where the source stream is still known.
	type pendingAd struct {
		loc        *foghornfederationpb.ArtifactLocation
		streamName string
	}
	var pending []pendingAd
	var unresolvedHashes []string
	for _, snap := range snapshot.Nodes {
		if !snap.IsActive {
			continue
		}
		ns := sm.GetNodeState(snap.NodeID)
		if ns == nil || len(ns.Artifacts) == 0 {
			continue
		}
		for _, a := range ns.Artifacts {
			tenantID := ""
			if a.StreamName != "" {
				if ss := sm.GetStreamState(a.StreamName); ss != nil {
					tenantID = ss.TenantID
				}
			}
			if tenantID == "" {
				unresolvedHashes = append(unresolvedHashes, a.ClipHash)
			}
			pending = append(pending, pendingAd{
				streamName: a.StreamName,
				loc: &foghornfederationpb.ArtifactLocation{
					ArtifactHash: a.ClipHash,
					ArtifactType: artifactTypeToString(a.ArtifactType),
					NodeId:       snap.NodeID,
					BaseUrl:      ns.BaseURL,
					SizeBytes:    a.SizeBytes,
					AccessCount:  uint32(a.AccessCount),
					LastAccessed: a.LastAccessed,
					GeoLat:       snap.GeoLatitude,
					GeoLon:       snap.GeoLongitude,
					TenantId:     tenantID,
				},
			})
		}
	}

	// Pass 2: artifacts outlive their streams, so resolve the remainder from
	// the artifact registry (the authority for artifact→tenant attribution).
	registryTenants := pm.resolveArtifactTenants(unresolvedHashes)

	var locs []*foghornfederationpb.ArtifactLocation
	for _, ad := range pending {
		if ad.loc.TenantId == "" {
			ad.loc.TenantId = registryTenants[ad.loc.ArtifactHash]
		}
		if ad.loc.TenantId == "" {
			// Refuse to advertise an artifact we can't attribute to a
			// tenant — empty TenantId would otherwise broadcast hot
			// location across peers regardless of tenant scope.
			if pm.shouldLogUnresolvedAd(ad.loc.ArtifactHash) {
				pm.logger.WithFields(logging.Fields{
					"artifact_hash": ad.loc.ArtifactHash,
					"node_id":       ad.loc.NodeId,
					"stream_name":   ad.streamName,
				}).Warn("pushArtifacts: skipping ad with unresolved tenant; artifact registry has no tenant for this hash")
			}
			continue
		}
		locs = append(locs, ad.loc)
	}

	if len(locs) == 0 {
		return
	}

	pm.mu.RLock()
	defer pm.mu.RUnlock()

	ts := time.Now().Unix()
	for peerID, ps := range pm.peers {
		if !ps.connected || ps.stream == nil {
			continue
		}
		var peerLocs []*foghornfederationpb.ArtifactLocation
		for _, loc := range locs {
			// Empty-scope peers (no tenant filter) accept everything; tenant-
			// scoped peers only see ads for tenants they're entitled to.
			// Empty TenantId was filtered upstream — guard here too so a
			// malformed loc can't leak across peers.
			if loc.TenantId == "" {
				continue
			}
			if len(ps.tenantIDs) == 0 || peerHasTenant(ps, loc.TenantId) {
				peerLocs = append(peerLocs, loc)
			}
		}
		if len(peerLocs) == 0 {
			continue
		}
		msg := &foghornfederationpb.PeerMessage{
			ClusterId: pm.clusterID,
			Payload: &foghornfederationpb.PeerMessage_ArtifactAd{
				ArtifactAd: &foghornfederationpb.ArtifactAdvertisement{
					Artifacts: peerLocs,
					Timestamp: ts,
				},
			},
		}
		pm.touchPool(peerID)
		pm.enqueue(peerID, ps, msg)
	}
}

// lookupDVRRecordingNodes returns a stream_internal_name -> node_id
// map for streams in the supplied set that have an active DVR
// recording. Active = artifact_type='dvr' AND status IN
// (requested,starting,recording), recording node = the
// non-orphaned artifact_nodes row (mirrors ResolveDVRArtifactDispatch's
// invariant: exactly one non-orphaned row while status is active).
//
// Empty result when DB unavailable or query fails — federation ad
// emission proceeds without dvr_recording_node_id and receiving
// clusters fall through to their normal STREAM_SOURCE dvr+ branch.
func (pm *PeerManager) lookupDVRRecordingNodes(names []string) map[string]string {
	if len(names) == 0 {
		return nil
	}
	db := control.GetDB()
	if db == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rows, err := foghorndb.New(db).ResolveActiveDVRNodes(ctx, names)
	if err != nil {
		pm.logger.WithError(err).Debug("DVR recording-node lookup for ad batch failed")
		return nil
	}
	out := make(map[string]string, len(names))
	for _, row := range rows {
		streamName, nodeID := row.StreamInternalName.String, row.NodeID
		if existing, ok := out[streamName]; ok && existing != nodeID {
			// Should be impossible per ResolveDVRArtifactDispatch's
			// single-recording-node invariant; skip extras to stay
			// deterministic.
			continue
		}
		out[streamName] = nodeID
	}
	return out
}

// pushStreamAds broadcasts a StreamAdvertisement per live stream to all connected
// peers every 5s. Each advertisement carries the full edge list for that stream,
// enabling peers to build an Adj-RIB-In that replaces Commodore + QueryStream RPCs.
func (pm *PeerManager) pushStreamAds() {
	sm := state.DefaultManager()
	if sm == nil {
		return
	}

	snapshot := sm.GetBalancerSnapshotAtomic()
	if snapshot == nil {
		return
	}

	type streamInfo struct {
		ss              *state.StreamState
		edges           []*foghornfederationpb.PeerStreamEdge
		originClusterID string
	}
	streams := make(map[string]*streamInfo)

	for _, snap := range snapshot.Nodes {
		if !snap.IsActive || len(snap.Streams) == 0 {
			continue
		}
		ns := sm.GetNodeState(snap.NodeID)
		if ns == nil {
			continue
		}
		for streamName := range snap.Streams {
			si, ok := streams[streamName]
			if !ok {
				ss := sm.GetStreamState(streamName)
				if ss == nil || ss.Status != "live" {
					continue
				}
				si = &streamInfo{ss: ss, originClusterID: pm.clusterID}
				streams[streamName] = si
			}
			isOrigin := si.ss.NodeID == snap.NodeID && si.ss.Inputs > 0
			sourceStreamName := streamName
			if control.StreamRegistryInstance != nil {
				if entry, err := control.StreamRegistryInstance.ResolveSourceByInternalName(context.Background(), streamName); err == nil {
					if entry.OriginClusterID != "" {
						si.originClusterID = entry.OriginClusterID
					}
					if entry.IngestMode != 0 {
						sourceStreamName = control.RuntimeNameFor(entry.IngestMode, entry.InternalName)
					} else if strings.Contains(si.ss.StreamName, "+") {
						sourceStreamName = control.MistSourceNameFromObservedStream(si.ss.StreamName)
					}
				} else if strings.Contains(si.ss.StreamName, "+") {
					sourceStreamName = control.MistSourceNameFromObservedStream(si.ss.StreamName)
				}
			} else if strings.Contains(si.ss.StreamName, "+") {
				sourceStreamName = control.MistSourceNameFromObservedStream(si.ss.StreamName)
			}
			si.edges = append(si.edges, &foghornfederationpb.PeerStreamEdge{
				NodeId:      snap.NodeID,
				BaseUrl:     ns.BaseURL,
				DtscUrl:     control.BuildDTSCURI(snap.NodeID, sourceStreamName, pm.logger),
				IsOrigin:    isOrigin,
				BwAvailable: snap.BWAvailable,
				CpuPercent:  snap.CPU,
				ViewerCount: uint32(sm.GetNodeActiveViewers(snap.NodeID)),
				GeoLat:      snap.GeoLatitude,
				GeoLon:      snap.GeoLongitude,
				BufferState: si.ss.BufferState,
				RamUsed:     uint64(ns.RAMCurrent),
				RamMax:      uint64(ns.RAMMax),
			})
		}
	}

	if len(streams) == 0 {
		return
	}

	// Look up the recording node for any stream that has an active
	// DVR. One query per ad batch — cheaper than per-stream and
	// avoids touching the per-stream loop above. Receiver clusters
	// stash this on the federated Location so cross-cluster
	// STREAM_SOURCE dvr+<hash> can arrange a DTSC pull from the
	// recording origin.
	streamNames := make([]string, 0, len(streams))
	for name := range streams {
		streamNames = append(streamNames, name)
	}
	dvrRecordingNodes := pm.lookupDVRRecordingNodes(streamNames)

	now := time.Now().Unix()
	var messages []*foghornfederationpb.PeerMessage
	for _, si := range streams {
		messages = append(messages, &foghornfederationpb.PeerMessage{
			ClusterId: pm.clusterID,
			Payload: &foghornfederationpb.PeerMessage_StreamAd{
				StreamAd: &foghornfederationpb.StreamAdvertisement{
					InternalName:       si.ss.InternalName,
					TenantId:           si.ss.TenantID,
					PlaybackId:         si.ss.PlaybackID,
					OriginClusterId:    si.originClusterID,
					IsLive:             true,
					Edges:              si.edges,
					Timestamp:          now,
					DvrRecordingNodeId: dvrRecordingNodes[si.ss.InternalName],
				},
			},
		})
	}

	pm.mu.RLock()
	defer pm.mu.RUnlock()

	for peerID, ps := range pm.peers {
		if !ps.connected || ps.stream == nil {
			continue
		}
		pm.touchPool(peerID)
		for _, msg := range messages {
			ad, ok := msg.GetPayload().(*foghornfederationpb.PeerMessage_StreamAd)
			if !ok {
				continue
			}
			if !pm.shouldSendStreamToPeer(peerID, ps, ad.StreamAd.InternalName, ad.StreamAd.TenantId) {
				continue
			}
			pm.enqueue(peerID, ps, msg)
		}
	}
}

// pushHeartbeat sends a PeerHeartbeat with cluster-wide stats to all connected peers.
func (pm *PeerManager) pushHeartbeat() {
	sm := state.DefaultManager()
	if sm == nil {
		return
	}

	snapshot := sm.GetBalancerSnapshotAtomic()
	if snapshot == nil {
		return
	}

	var streamCount uint32
	var totalBW uint64
	var edgeCount uint32

	seen := make(map[string]bool)
	for _, ss := range sm.GetAllStreamStates() {
		if ss.Status == "live" && !seen[ss.InternalName] {
			seen[ss.InternalName] = true
			streamCount++
		}
	}

	for _, snap := range snapshot.Nodes {
		if !snap.IsActive {
			continue
		}
		edgeCount++
		totalBW += snap.BWAvailable
	}

	hb := &foghornfederationpb.PeerHeartbeat{
		ProtocolVersion:  protocolVersion,
		StreamCount:      streamCount,
		TotalBwAvailable: totalBW,
		EdgeCount:        edgeCount,
		UptimeSeconds:    pm.uptimeSeconds(),
		Capabilities:     []string{"stream_ad", "artifact_ad", "capacity_summary"},
	}
	if pm.selfGeoFunc != nil {
		hb.FoghornLat, hb.FoghornLon, hb.FoghornLocation = pm.selfGeoFunc()
	}
	msg := &foghornfederationpb.PeerMessage{
		ClusterId: pm.clusterID,
		Payload:   &foghornfederationpb.PeerMessage_PeerHeartbeat{PeerHeartbeat: hb},
	}

	pm.mu.RLock()
	defer pm.mu.RUnlock()

	for peerID, ps := range pm.peers {
		if !ps.connected || ps.stream == nil {
			continue
		}
		pm.enqueue(peerID, ps, msg)
	}
}

func (pm *PeerManager) uptimeSeconds() int64 {
	return int64(time.Since(pm.startTime).Seconds())
}

// checkReplicationCompletion detects when origin-pulled streams appear in
// local state, clears the registry's replication mark, and broadcasts a
// ReplicationEvent to peers. The "in-flight replication" state lives on
// control.StreamRegistry; the local cluster's Location carries
// ReplicatingFrom + PullDTSCURL + DestNodeID + DestNodeBaseURL +
// PullSourceNodeID populated by MarkReplicating.
func (pm *PeerManager) checkReplicationCompletion() {
	if control.StreamRegistryInstance == nil {
		return
	}
	sm := state.DefaultManager()
	if sm == nil {
		return
	}

	replications := control.StreamRegistryInstance.AllLocalReplications()
	if len(replications) == 0 {
		return
	}

	for streamName, loc := range replications {
		st := sm.GetStreamState(streamName)
		if st == nil || st.Status != "live" {
			continue
		}

		instances := sm.GetStreamInstances(streamName)
		destInstance, ok := instances[loc.DestNodeID]
		if !ok || destInstance.Status != "live" {
			continue
		}

		control.StreamRegistryInstance.ClearReplicating(streamName)
		nameCopy := streamName
		pm.broadcastToPeers(&foghornfederationpb.PeerMessage{
			ClusterId: pm.clusterID,
			Payload: &foghornfederationpb.PeerMessage_ReplicationEvent{
				ReplicationEvent: &foghornfederationpb.ReplicationEvent{
					StreamName: nameCopy,
					NodeId:     loc.DestNodeID,
					ClusterId:  pm.clusterID,
					Available:  true,
					BaseUrl:    loc.DestNodeBaseURL,
				},
			},
		})
		pm.logger.WithField("stream", nameCopy).Info("Replication complete, registry mark cleared")
		originClusterID := loc.ReplicatingFrom
		if registryOrigin, ok := control.StreamRegistryInstance.OriginCluster(nameCopy); ok {
			originClusterID = registryOrigin
		}
		pm.emitFederationEvent(originPullCompletedEvent(nameCopy, loc, originClusterID, st.TenantID))
	}
}

func originPullCompletedEvent(streamName string, loc control.Location, originClusterID, streamTenantID string) *ipcpb.FederationEventData {
	destNode := loc.DestNodeID
	sourceNode := loc.PullSourceNodeID
	dtsc := loc.PullDTSCURL
	data := &ipcpb.FederationEventData{
		EventType:       ipcpb.FederationEventType_ORIGIN_PULL_COMPLETED,
		RemoteCluster:   loc.ReplicatingFrom,
		OriginClusterId: &originClusterID,
		StreamName:      &streamName,
		SourceNode:      &sourceNode,
		DestNode:        &destNode,
		DtscUrl:         &dtsc,
	}
	if tenantID := strings.TrimSpace(streamTenantID); tenantID != "" {
		data.StreamTenantId = &tenantID
	}
	return data
}

// broadcastToPeers sends a message to all connected peer channels. Used for
// replication-complete events — a best-effort loop-prevention hint (the
// receiver's RemoteReplication entry is a 5min TTL, never refreshed), so it
// rides the same best-effort mailbox as everything else.
func (pm *PeerManager) broadcastToPeers(msg *foghornfederationpb.PeerMessage) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	for peerID, ps := range pm.peers {
		if !ps.connected || ps.stream == nil {
			continue
		}
		pm.enqueue(peerID, ps, msg)
	}
}

// IsStreamLiveOnPeer checks Redis for a remote live stream entry.
// Returns the peer cluster ID if the stream is live elsewhere, or ("", false) if not.
// Fail-open: returns ("", false) on Redis errors so ingest is never blocked by cache issues.
func (pm *PeerManager) IsStreamLiveOnPeer(ctx context.Context, internalName, tenantID string) (string, bool) {
	if pm.cache == nil {
		return "", false
	}
	entry, err := pm.cache.GetRemoteLiveStream(ctx, tenantID, internalName)
	if err != nil || entry == nil {
		return "", false
	}
	if tenantID != "" && entry.TenantID != "" && entry.TenantID != tenantID {
		return "", false
	}
	return entry.ClusterID, true
}

// BroadcastStreamLifecycle notifies eligible peers that a stream went live or offline.
func (pm *PeerManager) BroadcastStreamLifecycle(ctx context.Context, internalName, tenantID string, sourceRevision int64, isLive bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if sourceRevision <= 0 {
		return errors.New("federation stream lifecycle requires a positive source revision")
	}
	msg := &foghornfederationpb.PeerMessage{
		ClusterId: pm.clusterID,
		Payload: &foghornfederationpb.PeerMessage_StreamLifecycle{
			StreamLifecycle: &foghornfederationpb.StreamLifecycleEvent{
				InternalName:   internalName,
				TenantId:       tenantID,
				ClusterId:      pm.clusterID,
				IsLive:         isLive,
				TimestampUnix:  time.Now().Unix(),
				SourceRevision: sourceRevision,
			},
		},
	}

	// Live events are refreshed every periodic tick. Receivers retain the offline revision fence
	// after removing the live marker so a delayed frame from an older channel cannot resurrect it.
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	required := make(map[string]*peerState)
	for peerID, streams := range pm.streamPeers {
		if streams[internalName] {
			required[peerID] = pm.peers[peerID]
		}
	}
	for peerID, ps := range pm.peers {
		if ps.lifecycle == peerAlwaysOn && pm.shouldSendStreamToPeer(peerID, ps, internalName, tenantID) {
			required[peerID] = ps
		}
	}
	// Verification and enqueue share pm.mu: a disconnect cannot slip between TrackStream's
	// persisted requirement and this completion decision. A failed enqueue keeps the durable leg
	// pending; successfully queued lifecycle frames remain idempotent if a later peer also fails.
	for peerID, ps := range required {
		if err := ctx.Err(); err != nil {
			return err
		}
		if ps == nil {
			if !isLive {
				delete(required, peerID)
				continue
			}
			return fmt.Errorf("required federation peer %s has no current authority lease", peerID)
		}
		if !pm.shouldSendStreamToPeer(peerID, ps, internalName, tenantID) {
			if !isLive {
				delete(required, peerID)
				continue
			}
			return fmt.Errorf("required federation peer %s is not authorized for tenant lifecycle", peerID)
		}
		if !ps.connected || ps.stream == nil || ps.sendCh == nil {
			return fmt.Errorf("required federation peer %s is not connected", peerID)
		}
	}
	for peerID, ps := range required {
		if !pm.enqueue(peerID, ps, msg) {
			return fmt.Errorf("required federation peer %s lifecycle mailbox is unavailable", peerID)
		}
	}
	return nil
}

func strPtr(s string) *string { return &s }
