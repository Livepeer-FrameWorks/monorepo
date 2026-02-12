package federation

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"frameworks/pkg/clients/foghorn"
	"frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
)

// PeerManager manages PeerChannel lifecycles and periodic peer discovery.
// It refreshes the peer list from Quartermaster every 30s and maintains
// one PeerChannel per peer cluster for bidirectional telemetry exchange.
// In multi-replica deployments, only the leader instance runs the active
// peering loop (Redis-based leader lease).
type PeerManager struct {
	clusterID  string
	instanceID string // unique per-process, for leader election
	pool       *foghorn.FoghornPool
	qm         *quartermaster.GRPCClient
	cache      *RemoteEdgeCache
	logger     logging.Logger

	mu            sync.RWMutex
	peers         map[string]*peerState      // cluster_id -> peer state
	streamPeers   map[string]map[string]bool // cluster_id -> set of active stream names
	metricHistory map[string][]metricSample  // node_id -> recent BW/CPU samples for 30s averaging
	done          chan struct{}
	isLeader      bool
	startTime     time.Time
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
	lifecycle   peerLifecycleType
	cancel      context.CancelFunc
	stream      pb.FoghornFederation_PeerChannelClient
	lastRefresh time.Time
	connected   bool
	s3Config    *ClusterS3Config
}

// PeerManagerConfig holds dependencies for the peer manager.
type PeerManagerConfig struct {
	ClusterID  string
	InstanceID string // unique per-process; used for leader lease
	Pool       *foghorn.FoghornPool
	QM         *quartermaster.GRPCClient
	Cache      *RemoteEdgeCache
	Logger     logging.Logger
}

// NewPeerManager creates and starts a new peer manager.
func NewPeerManager(cfg PeerManagerConfig) *PeerManager {
	pm := &PeerManager{
		clusterID:     cfg.ClusterID,
		instanceID:    cfg.InstanceID,
		pool:          cfg.Pool,
		qm:            cfg.QM,
		cache:         cfg.Cache,
		logger:        cfg.Logger,
		peers:         make(map[string]*peerState),
		streamPeers:   make(map[string]map[string]bool),
		metricHistory: make(map[string][]metricSample),
		done:          make(chan struct{}),
		startTime:     time.Now(),
	}
	go pm.run()
	return pm
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

// GetPeerS3Config returns the S3 configuration for a peer cluster, or nil if unknown.
func (pm *PeerManager) GetPeerS3Config(clusterID string) *ClusterS3Config {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	if ps, ok := pm.peers[clusterID]; ok {
		return ps.s3Config
	}
	return nil
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

// NotifyPeers accepts peer discovery hints from stream validation.
// All replicas register addresses (so GetPeerAddr works everywhere);
// only the leader opens PeerChannel connections.
func (pm *PeerManager) NotifyPeers(peers []*pb.TenantClusterPeer) {
	var changed bool

	pm.mu.Lock()
	for _, peer := range peers {
		if peer.GetClusterId() == pm.clusterID || peer.GetClusterId() == "" {
			continue
		}
		lifecycle := peerStreamScoped
		if peer.GetRole() == "official" || peer.GetRole() == "preferred" {
			lifecycle = peerAlwaysOn
		}

		addr := "foghorn." + peer.GetClusterSlug() + "." + peer.GetBaseUrl() + ":" + federationPort
		if existing, known := pm.peers[peer.GetClusterId()]; known {
			if existing.addr != addr {
				changed = true
			}
			existing.addr = addr
			existing.lifecycle = lifecycle
			existing.lastRefresh = time.Now()
			existing.s3Config = &ClusterS3Config{
				ClusterID:  peer.GetClusterId(),
				S3Bucket:   peer.GetS3Bucket(),
				S3Endpoint: peer.GetS3Endpoint(),
				S3Region:   peer.GetS3Region(),
			}
			continue
		}

		ps := &peerState{
			addr:        addr,
			lifecycle:   lifecycle,
			lastRefresh: time.Now(),
			s3Config: &ClusterS3Config{
				ClusterID:  peer.GetClusterId(),
				S3Bucket:   peer.GetS3Bucket(),
				S3Endpoint: peer.GetS3Endpoint(),
				S3Region:   peer.GetS3Region(),
			},
		}
		pm.peers[peer.GetClusterId()] = ps
		changed = true

		if pm.isLeader {
			go pm.connectPeer(peer.GetClusterId(), ps)
		}

		pm.logger.WithFields(map[string]interface{}{
			"peer_cluster": peer.GetClusterId(),
			"addr":         addr,
			"role":         peer.GetRole(),
			"lifecycle":    lifecycle,
			"is_leader":    pm.isLeader,
		}).Info("Demand-driven peer discovered from stream validation")
	}
	isLeader := pm.isLeader
	pm.mu.Unlock()

	if changed && isLeader && pm.cache != nil {
		pm.syncPeerAddressesToRedis()
	}
}

func (pm *PeerManager) shouldSendStreamToPeer(peerID string, ps *peerState, streamName, tenantID string) bool {
	if len(ps.tenantIDs) > 0 && tenantID != "" && !slices.Contains(ps.tenantIDs, tenantID) {
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

// TrackStream records that a stream is active and associated with specific peer clusters.
// Called when a stream goes live (PUSH_REWRITE) to maintain stream-scoped peer lifetimes.
func (pm *PeerManager) TrackStream(streamName string, clusterIDs []string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for _, cid := range clusterIDs {
		if cid == pm.clusterID || cid == "" {
			continue
		}
		if pm.streamPeers[cid] == nil {
			pm.streamPeers[cid] = make(map[string]bool)
		}
		pm.streamPeers[cid][streamName] = true
		pm.flushStreamPeersToRedis(cid)
	}
}

// UntrackStream removes a stream from all peer clusters. If a stream-scoped peer
// has no remaining active streams, its PeerChannel is closed.
func (pm *PeerManager) UntrackStream(streamName string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for cid, streams := range pm.streamPeers {
		delete(streams, streamName)
		if len(streams) == 0 {
			delete(pm.streamPeers, cid)
			pm.flushStreamPeersToRedis(cid)
			ps, ok := pm.peers[cid]
			if ok && ps.lifecycle == peerStreamScoped {
				if ps.cancel != nil {
					ps.cancel()
				}
				delete(pm.peers, cid)
				pm.logger.WithFields(map[string]interface{}{
					"peer_cluster": cid,
					"stream":       streamName,
				}).Info("Closed stream-scoped peer (no remaining streams)")
			}
		} else {
			pm.flushStreamPeersToRedis(cid)
		}
	}
}

// flushStreamPeersToRedis persists the current stream set for a peer cluster.
// Must be called with pm.mu held.
func (pm *PeerManager) flushStreamPeersToRedis(peerClusterID string) {
	if pm.cache == nil {
		return
	}
	streams := pm.streamPeers[peerClusterID]
	var list []string
	for s := range streams {
		list = append(list, s)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := pm.cache.SetStreamPeers(ctx, peerClusterID, list); err != nil {
		pm.logger.WithError(err).WithField("peer_cluster", peerClusterID).Warn("Failed to flush stream peers to Redis")
	}
}

// loadStreamPeersFromRedis loads persisted stream-peer mappings on leader takeover.
func (pm *PeerManager) loadStreamPeersFromRedis() {
	if pm.cache == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	all, err := pm.cache.LoadAllStreamPeers(ctx)
	if err != nil {
		pm.logger.WithError(err).Warn("Failed to load stream peers from Redis on leader takeover")
		return
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for cid, streams := range all {
		if cid == pm.clusterID || cid == "" {
			continue
		}
		if pm.streamPeers[cid] == nil {
			pm.streamPeers[cid] = make(map[string]bool)
		}
		for _, s := range streams {
			pm.streamPeers[cid][s] = true
		}
	}
	if len(all) > 0 {
		pm.logger.WithField("peer_count", len(all)).Info("Restored stream-peer mappings from Redis")
	}
}

const (
	peerRefreshInterval   = 5 * time.Minute // reconciliation only; demand-driven path handles fast discovery
	telemetryPushInterval = 5 * time.Second
	summaryPushInterval   = 15 * time.Second
	artifactPushInterval  = 30 * time.Second
	peerReconnectBackoff  = 10 * time.Second
	heartbeatPushInterval = 10 * time.Second
	federationPort        = "18019" // standard Foghorn gRPC port for federation
	leaderAcquireInterval = 5 * time.Second
	leaderRole            = "peer_manager"
	protocolVersion       = uint32(1)
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

		pm.loadPeerAddressesFromRedis()

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

	pm.mu.Lock()
	pm.isLeader = true
	pm.mu.Unlock()

	defer func() {
		pm.mu.Lock()
		pm.isLeader = false
		pm.mu.Unlock()
		pm.disconnectAllPeers()
		if pm.cache != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			pm.cache.ReleaseLeaderLease(ctx, leaderRole, pm.instanceID)
		}
		pm.logger.Info("Released PeerManager leadership")
	}()

	pm.loadStreamPeersFromRedis()
	pm.refreshPeers()
	if pm.cache != nil {
		pm.syncPeerAddressesToRedis()
	}

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
			pm.refreshPeers()
			if pm.cache != nil {
				pm.syncPeerAddressesToRedis()
			}
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
			pm.pushHeartbeat()
		}
	}
}

// refreshPeers queries Quartermaster for peer clusters and manages connections.
func (pm *PeerManager) refreshPeers() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := pm.qm.ListPeers(ctx, pm.clusterID)
	if err != nil {
		pm.logger.WithError(err).Warn("Failed to refresh federation peers")
		return
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Track which peers are still valid
	seen := make(map[string]bool, len(resp.Peers))

	for _, peer := range resp.Peers {
		if peer.FoghornAddr == "" {
			continue
		}
		seen[peer.ClusterId] = true

		existing, ok := pm.peers[peer.ClusterId]
		if ok && existing.addr == peer.FoghornAddr && existing.connected {
			// Peer unchanged and connected, update tenant list
			existing.tenantIDs = peer.SharedTenantIds
			existing.lastRefresh = time.Now()
			continue
		}

		// New peer or address changed — (re)connect
		var existingS3 *ClusterS3Config
		if ok {
			existingS3 = existing.s3Config
			if existing.cancel != nil {
				existing.cancel()
			}
		}

		ps := &peerState{
			addr:        peer.FoghornAddr,
			tenantIDs:   peer.SharedTenantIds,
			lastRefresh: time.Now(),
			s3Config:    existingS3,
		}
		pm.peers[peer.ClusterId] = ps

		go pm.connectPeer(peer.ClusterId, ps)
	}

	// Remove peers no longer in the list
	for id, ps := range pm.peers {
		if !seen[id] {
			if ps.cancel != nil {
				ps.cancel()
			}
			delete(pm.peers, id)
			pm.logger.WithField("peer_cluster", id).Info("Removed stale federation peer")
		}
	}

	pm.logger.WithField("peer_count", len(pm.peers)).Debug("Federation peers refreshed")
}

// syncPeerAddressesToRedis writes the current peer address map to Redis so
// non-leader replicas can populate their local cache via loadPeerAddressesFromRedis.
func (pm *PeerManager) syncPeerAddressesToRedis() {
	pm.mu.RLock()
	addrs := make(map[string]string, len(pm.peers))
	for id, ps := range pm.peers {
		addrs[id] = ps.addr
	}
	pm.mu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := pm.cache.SetPeerAddresses(ctx, addrs); err != nil {
		pm.logger.WithError(err).Debug("Failed to sync peer addresses to Redis")
	}
}

// loadPeerAddressesFromRedis reads peer addresses from Redis into the local map.
// New peers are added; existing peers get their address updated if the leader
// refreshed from Quartermaster. Does not remove peers missing from Redis
// (demand-driven discoveries may not yet be synced).
func (pm *PeerManager) loadPeerAddressesFromRedis() {
	if pm.cache == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	addrs, err := pm.cache.GetPeerAddresses(ctx)
	if err != nil {
		pm.logger.WithError(err).Debug("Failed to load peer addresses from Redis")
		return
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()
	for clusterID, addr := range addrs {
		if clusterID == pm.clusterID {
			continue
		}
		if existing, ok := pm.peers[clusterID]; ok {
			existing.addr = addr
			continue
		}
		pm.peers[clusterID] = &peerState{
			addr:        addr,
			lastRefresh: time.Now(),
		}
	}
}

// connectPeer opens a PeerChannel to the given peer and runs the receive loop.
func (pm *PeerManager) connectPeer(clusterID string, ps *peerState) {
	for {
		select {
		case <-pm.done:
			return
		default:
		}

		ctx, cancel := context.WithCancel(context.Background())

		pm.mu.Lock()
		// Check if this peer was removed while we were reconnecting
		current, ok := pm.peers[clusterID]
		if !ok || current != ps {
			pm.mu.Unlock()
			cancel()
			return
		}
		ps.cancel = cancel
		pm.mu.Unlock()

		client, err := pm.pool.GetOrCreate(clusterID, ps.addr)
		if err != nil {
			pm.logger.WithError(err).WithField("peer_cluster", clusterID).Warn("Failed to get Foghorn client for peer")
			cancel()
			time.Sleep(peerReconnectBackoff)
			continue
		}

		stream, err := client.Federation().PeerChannel(ctx)
		if err != nil {
			pm.logger.WithError(err).WithField("peer_cluster", clusterID).Warn("Failed to open PeerChannel")
			cancel()
			time.Sleep(peerReconnectBackoff)
			continue
		}

		pm.mu.Lock()
		ps.stream = stream
		ps.connected = true
		pm.mu.Unlock()

		pm.logger.WithField("peer_cluster", clusterID).Info("PeerChannel connected")

		// Receive loop — processes incoming messages until the stream closes
		pm.recvLoop(clusterID, stream)

		pm.mu.Lock()
		ps.connected = false
		ps.stream = nil
		pm.mu.Unlock()

		cancel()

		pm.logger.WithField("peer_cluster", clusterID).Info("PeerChannel disconnected, will reconnect")
		time.Sleep(peerReconnectBackoff)
	}
}

// recvLoop reads PeerMessages from the stream and writes telemetry to Redis.
func (pm *PeerManager) recvLoop(peerClusterID string, stream pb.FoghornFederation_PeerChannelClient) {
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
		case *pb.PeerMessage_EdgeTelemetry:
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

		case *pb.PeerMessage_ReplicationEvent:
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

		case *pb.PeerMessage_ClusterSummary:
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

		case *pb.PeerMessage_StreamLifecycle:
			ev := payload.StreamLifecycle
			if ev.GetIsLive() {
				if err := pm.cache.SetRemoteLiveStream(ctx, ev.GetInternalName(), &RemoteLiveStreamEntry{
					ClusterID: ev.GetClusterId(),
					TenantID:  ev.GetTenantId(),
					UpdatedAt: time.Now().Unix(),
				}); err != nil {
					pm.logger.WithError(err).Debug("Failed to cache remote live stream from PeerChannel")
				}
			} else {
				if err := pm.cache.DeleteRemoteLiveStream(ctx, ev.GetInternalName()); err != nil {
					pm.logger.WithError(err).Debug("Failed to delete remote live stream from PeerChannel")
				}
			}

		case *pb.PeerMessage_StreamAd:
			ad := payload.StreamAd
			if ad != nil {
				edges := make([]*StreamAdEdge, 0, len(ad.Edges))
				for _, e := range ad.Edges {
					edges = append(edges, &StreamAdEdge{
						NodeID:      e.NodeId,
						BaseURL:     e.BaseUrl,
						DTSCURL:     e.DtscUrl,
						IsOrigin:    e.IsOrigin,
						BWAvailable: e.BwAvailable,
						CPUPercent:  e.CpuPercent,
						ViewerCount: e.ViewerCount,
						GeoLat:      e.GeoLat,
						GeoLon:      e.GeoLon,
						BufferState: e.BufferState,
					})
				}
				record := &StreamAdRecord{
					InternalName:    ad.InternalName,
					TenantID:        ad.TenantId,
					PlaybackID:      ad.PlaybackId,
					OriginClusterID: ad.OriginClusterId,
					IsLive:          ad.IsLive,
					Edges:           edges,
					Timestamp:       ad.Timestamp,
				}
				if err := pm.cache.SetStreamAd(ctx, peerClusterID, record); err != nil {
					pm.logger.WithError(err).Debug("Failed to cache stream ad from PeerChannel")
				}
				if ad.PlaybackId != "" && ad.IsLive {
					if err := pm.cache.SetPlaybackIndex(ctx, ad.PlaybackId, ad.InternalName); err != nil {
						pm.logger.WithError(err).Debug("Failed to update playback index from PeerChannel")
					}
				}
			}

		case *pb.PeerMessage_ArtifactAd:
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
					}
					if err := pm.cache.SetRemoteArtifact(ctx, peerClusterID, entry); err != nil {
						pm.logger.WithError(err).Debug("Failed to cache remote artifact from PeerChannel")
					}
				}
			}

		case *pb.PeerMessage_PeerHeartbeat:
			hb := payload.PeerHeartbeat
			if hb != nil {
				record := &PeerHeartbeatRecord{
					ProtocolVersion:  hb.ProtocolVersion,
					StreamCount:      hb.StreamCount,
					TotalBWAvailable: hb.TotalBwAvailable,
					EdgeCount:        hb.EdgeCount,
					UptimeSeconds:    hb.UptimeSeconds,
					Capabilities:     hb.Capabilities,
				}
				if err := pm.cache.SetPeerHeartbeat(ctx, peerClusterID, record); err != nil {
					pm.logger.WithError(err).Debug("Failed to cache peer heartbeat from PeerChannel")
				}
			}

		case *pb.PeerMessage_CapacitySummary:
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
	var messages []*pb.PeerMessage
	for _, snap := range snapshot.Nodes {
		if !snap.IsActive || len(snap.Streams) == 0 {
			continue
		}

		ns := sm.GetNodeState(snap.NodeID)
		if ns == nil {
			continue
		}

		for streamName := range snap.Streams {
			msg := &pb.PeerMessage{
				ClusterId: pm.clusterID,
				Payload: &pb.PeerMessage_EdgeTelemetry{
					EdgeTelemetry: &pb.EdgeTelemetry{
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
		pm.pool.Touch(peerID)
		for _, msg := range messages {
			tel, ok := msg.GetPayload().(*pb.PeerMessage_EdgeTelemetry)
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
			if err := ps.stream.Send(msg); err != nil {
				pm.logger.WithError(err).WithField("peer_cluster", peerID).Debug("Failed to send telemetry to peer")
				break
			}
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
		seen[ss.InternalName] = true
		lifecycleMsg := &pb.PeerMessage{
			ClusterId: pm.clusterID,
			Payload: &pb.PeerMessage_StreamLifecycle{
				StreamLifecycle: &pb.StreamLifecycleEvent{
					InternalName:  ss.InternalName,
					TenantId:      ss.TenantID,
					ClusterId:     pm.clusterID,
					IsLive:        true,
					TimestampUnix: now,
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
			if err := ps.stream.Send(lifecycleMsg); err != nil {
				pm.logger.WithError(err).WithField("peer_cluster", peerID).Debug("Failed to send lifecycle heartbeat to peer")
				break
			}
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

	var edges []*pb.EdgeSnapshot
	for _, snap := range snapshot.Nodes {
		if !snap.IsActive || snap.BWAvailable == 0 {
			continue
		}
		ns := sm.GetNodeState(snap.NodeID)
		if ns == nil {
			continue
		}
		bwAvg, cpuAvg := pm.recordAndAverage(snap.NodeID, snap.BWAvailable, snap.CPU)
		edges = append(edges, &pb.EdgeSnapshot{
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

	msg := &pb.PeerMessage{
		ClusterId: pm.clusterID,
		Payload: &pb.PeerMessage_ClusterSummary{
			ClusterSummary: &pb.ClusterEdgeSummary{
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
		pm.pool.Touch(peerID)
		if err := ps.stream.Send(msg); err != nil {
			pm.logger.WithError(err).WithField("peer_cluster", peerID).Debug("Failed to send cluster summary to peer")
		}
	}
}

func artifactTypeToString(t pb.ArtifactEvent_ArtifactType) string {
	switch t {
	case pb.ArtifactEvent_ARTIFACT_TYPE_CLIP:
		return "clip"
	case pb.ArtifactEvent_ARTIFACT_TYPE_DVR:
		return "dvr"
	case pb.ArtifactEvent_ARTIFACT_TYPE_VOD:
		return "vod"
	default:
		return "clip"
	}
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

	var locs []*pb.ArtifactLocation
	for _, snap := range snapshot.Nodes {
		if !snap.IsActive {
			continue
		}
		ns := sm.GetNodeState(snap.NodeID)
		if ns == nil || len(ns.Artifacts) == 0 {
			continue
		}
		for _, a := range ns.Artifacts {
			locs = append(locs, &pb.ArtifactLocation{
				ArtifactHash: a.ClipHash,
				ArtifactType: artifactTypeToString(a.ArtifactType),
				NodeId:       snap.NodeID,
				BaseUrl:      ns.BaseURL,
				SizeBytes:    a.SizeBytes,
				AccessCount:  uint32(a.AccessCount),
				LastAccessed: a.LastAccessed,
				GeoLat:       snap.GeoLatitude,
				GeoLon:       snap.GeoLongitude,
			})
		}
	}

	if len(locs) == 0 {
		return
	}

	msg := &pb.PeerMessage{
		ClusterId: pm.clusterID,
		Payload: &pb.PeerMessage_ArtifactAd{
			ArtifactAd: &pb.ArtifactAdvertisement{
				Artifacts: locs,
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
		pm.pool.Touch(peerID)
		if err := ps.stream.Send(msg); err != nil {
			pm.logger.WithError(err).WithField("peer_cluster", peerID).Debug("Failed to send artifact advertisement to peer")
		}
	}
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
		ss    *state.StreamState
		edges []*pb.PeerStreamEdge
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
				si = &streamInfo{ss: ss}
				streams[streamName] = si
			}
			isOrigin := si.ss.NodeID == snap.NodeID && si.ss.Inputs > 0
			si.edges = append(si.edges, &pb.PeerStreamEdge{
				NodeId:      snap.NodeID,
				BaseUrl:     ns.BaseURL,
				DtscUrl:     control.BuildDTSCURI(snap.NodeID, streamName, true, pm.logger),
				IsOrigin:    isOrigin,
				BwAvailable: snap.BWAvailable,
				CpuPercent:  snap.CPU,
				ViewerCount: uint32(sm.GetNodeActiveViewers(snap.NodeID)),
				GeoLat:      snap.GeoLatitude,
				GeoLon:      snap.GeoLongitude,
				BufferState: si.ss.BufferState,
			})
		}
	}

	if len(streams) == 0 {
		return
	}

	now := time.Now().Unix()
	var messages []*pb.PeerMessage
	for _, si := range streams {
		messages = append(messages, &pb.PeerMessage{
			ClusterId: pm.clusterID,
			Payload: &pb.PeerMessage_StreamAd{
				StreamAd: &pb.StreamAdvertisement{
					InternalName:    si.ss.InternalName,
					TenantId:        si.ss.TenantID,
					PlaybackId:      si.ss.PlaybackID,
					OriginClusterId: pm.clusterID,
					IsLive:          true,
					Edges:           si.edges,
					Timestamp:       now,
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
		pm.pool.Touch(peerID)
		for _, msg := range messages {
			ad, ok := msg.GetPayload().(*pb.PeerMessage_StreamAd)
			if !ok {
				continue
			}
			if !pm.shouldSendStreamToPeer(peerID, ps, ad.StreamAd.InternalName, ad.StreamAd.TenantId) {
				continue
			}
			if err := ps.stream.Send(msg); err != nil {
				pm.logger.WithError(err).WithField("peer_cluster", peerID).Debug("Failed to send stream ad to peer")
				break
			}
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

	msg := &pb.PeerMessage{
		ClusterId: pm.clusterID,
		Payload: &pb.PeerMessage_PeerHeartbeat{
			PeerHeartbeat: &pb.PeerHeartbeat{
				ProtocolVersion:  protocolVersion,
				StreamCount:      streamCount,
				TotalBwAvailable: totalBW,
				EdgeCount:        edgeCount,
				UptimeSeconds:    pm.uptimeSeconds(),
				Capabilities:     []string{"stream_ad", "artifact_ad", "capacity_summary"},
			},
		},
	}

	pm.mu.RLock()
	defer pm.mu.RUnlock()

	for peerID, ps := range pm.peers {
		if !ps.connected || ps.stream == nil {
			continue
		}
		if err := ps.stream.Send(msg); err != nil {
			pm.logger.WithError(err).WithField("peer_cluster", peerID).Debug("Failed to send heartbeat to peer")
		}
	}
}

func (pm *PeerManager) uptimeSeconds() int64 {
	return int64(time.Since(pm.startTime).Seconds())
}

// checkReplicationCompletion detects when origin-pulled streams appear in local state,
// clears the ActiveReplication record, and broadcasts a ReplicationEvent to peers.
func (pm *PeerManager) checkReplicationCompletion() {
	if pm.cache == nil {
		return
	}
	sm := state.DefaultManager()
	if sm == nil {
		return
	}

	ctx := context.Background()
	records, err := pm.cache.GetAllActiveReplications(ctx)
	if err != nil || len(records) == 0 {
		return
	}

	for _, record := range records {
		if record.DestCluster != pm.clusterID {
			continue
		}
		st := sm.GetStreamState(record.StreamName)
		if st == nil || st.Status != "live" {
			continue
		}

		_ = pm.cache.DeleteActiveReplication(ctx, record.StreamName)
		pm.broadcastToPeers(&pb.PeerMessage{
			ClusterId: pm.clusterID,
			Payload: &pb.PeerMessage_ReplicationEvent{
				ReplicationEvent: &pb.ReplicationEvent{
					StreamName: record.StreamName,
					NodeId:     record.DestNodeID,
					ClusterId:  pm.clusterID,
					Available:  true,
					BaseUrl:    record.BaseURL,
				},
			},
		})
		pm.logger.WithField("stream", record.StreamName).Info("Replication complete, ActiveReplication cleared")
	}
}

// broadcastToPeers sends a message to all connected peer channels.
func (pm *PeerManager) broadcastToPeers(msg *pb.PeerMessage) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	for peerID, ps := range pm.peers {
		if !ps.connected || ps.stream == nil {
			continue
		}
		if err := ps.stream.Send(msg); err != nil {
			pm.logger.WithError(err).WithField("peer_cluster", peerID).Debug("Failed to broadcast to peer")
		}
	}
}

// IsStreamLiveOnPeer checks Redis for a remote live stream entry.
// Returns the peer cluster ID if the stream is live elsewhere, or ("", false) if not.
// Fail-open: returns ("", false) on Redis errors so ingest is never blocked by cache issues.
func (pm *PeerManager) IsStreamLiveOnPeer(ctx context.Context, internalName string) (string, bool) {
	if pm.cache == nil {
		return "", false
	}
	entry, err := pm.cache.GetRemoteLiveStream(ctx, internalName)
	if err != nil || entry == nil {
		return "", false
	}
	return entry.ClusterID, true
}

// BroadcastStreamLifecycle notifies eligible peers that a stream went live or offline.
func (pm *PeerManager) BroadcastStreamLifecycle(internalName, tenantID string, isLive bool) {
	msg := &pb.PeerMessage{
		ClusterId: pm.clusterID,
		Payload: &pb.PeerMessage_StreamLifecycle{
			StreamLifecycle: &pb.StreamLifecycleEvent{
				InternalName:  internalName,
				TenantId:      tenantID,
				ClusterId:     pm.clusterID,
				IsLive:        isLive,
				TimestampUnix: time.Now().Unix(),
			},
		},
	}

	pm.mu.RLock()
	defer pm.mu.RUnlock()
	for peerID, ps := range pm.peers {
		if !ps.connected || ps.stream == nil {
			continue
		}
		if !pm.shouldSendStreamToPeer(peerID, ps, internalName, tenantID) {
			continue
		}
		if err := ps.stream.Send(msg); err != nil {
			pm.logger.WithError(err).WithField("peer_cluster", peerID).Debug("Failed to broadcast lifecycle to peer")
		}
	}
}
