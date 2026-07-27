package grpc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"

	"frameworks/api_balancing/internal/artifactoutbox"
	"frameworks/api_balancing/internal/artifacts"
	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/federation"
	"frameworks/api_balancing/internal/handlers"
	"frameworks/api_balancing/internal/identity"
	"frameworks/api_balancing/internal/jobs"
	"frameworks/api_balancing/internal/policybundle"
	"frameworks/api_balancing/internal/state"
	"frameworks/api_balancing/internal/storage"
	"frameworks/api_balancing/internal/triggers"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	purserclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/purser"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clips"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/dvrpolicy"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/geoip"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/x402"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// S3ClientInterface defines the S3 operations needed by FoghornGRPCServer
type S3ClientInterface interface {
	CreateMultipartUpload(ctx context.Context, key string, contentType string) (string, error)
	GeneratePresignedUploadParts(key, uploadID string, partCount int, expiry time.Duration) ([]storage.UploadPart, error)
	CompleteMultipartUpload(ctx context.Context, key, uploadID string, parts []storage.CompletedPart) error
	AbortMultipartUpload(ctx context.Context, key, uploadID string) error
	// Exists reports whether the final object is present. Used by CompleteVodUpload to reconcile a
	// retry where the multipart already completed on a prior attempt (S3 returns NoSuchUpload).
	Exists(ctx context.Context, key string) (bool, error)
	ListUploadedParts(ctx context.Context, key, uploadID string) ([]storage.UploadedPart, error)
	BuildVodS3Key(tenantID, artifactHash, filename string) string
	BuildS3URL(key string) string
	Delete(ctx context.Context, key string) error
	PutObject(ctx context.Context, key string, body []byte, contentType string) error
	GeneratePresignedGET(key string, expiry time.Duration) (string, error)
}

// CacheInvalidator is implemented by the trigger processor to invalidate and lookup cached tenant data
type CacheInvalidator interface {
	InvalidateTenantCache(tenantID string) int
	InvalidatePlaybackAuthCache(tenantID string, internalNames []string) int
	GetBillingStatus(ctx context.Context, internalName, tenantID string) *triggers.BillingStatus
	GetClusterPeers(internalName, tenantID string) []*clusterpeerpb.TenantClusterPeer
}

type federationRPC interface {
	QueryStream(ctx context.Context, peerClusterID, peerAddr string, req *foghornfederationpb.QueryStreamRequest) (*foghornfederationpb.QueryStreamResponse, error)
	NotifyOriginPull(ctx context.Context, peerClusterID, peerAddr string, req *foghornfederationpb.OriginPullNotification) (*foghornfederationpb.OriginPullAck, error)
	PrepareArtifact(ctx context.Context, peerClusterID, peerAddr string, req *foghornfederationpb.PrepareArtifactRequest) (*foghornfederationpb.PrepareArtifactResponse, error)
	ForwardArtifactCommand(ctx context.Context, peerClusterID, peerAddr string, req *foghornfederationpb.ForwardArtifactCommandRequest) (*foghornfederationpb.ForwardArtifactCommandResponse, error)
}

type peerAddrResolver interface {
	GetPeerAddr(clusterID string) string
	GetPeers() map[string]string
}

// FoghornGRPCServer implements the Foghorn control plane gRPC services
type FoghornGRPCServer struct {
	foghornpb.UnimplementedClipControlServiceServer
	foghornpb.UnimplementedDVRControlServiceServer
	foghornpb.UnimplementedViewerControlServiceServer
	foghornpb.UnimplementedVodControlServiceServer
	foghornpb.UnimplementedTenantControlServiceServer
	foghornpb.UnimplementedNodeControlServiceServer
	sharedpb.UnimplementedArtifactCreationStatusServiceServer

	db                  *sql.DB
	logger              logging.Logger
	lb                  *balancer.LoadBalancer
	geoipReader         *geoip.Reader
	geoipCache          *cache.Cache
	decklogClient       *decklog.BatchedClient
	s3Client            S3ClientInterface
	cacheInvalidator    CacheInvalidator
	purserClient        *purserclient.GRPCClient
	remoteEdgeCache     *federation.RemoteEdgeCache
	federationClient    federationRPC
	peerManager         peerAddrResolver
	quartermasterClient quartermasterRoutingResolver
	storageResolver     storageResolverFactory
	clusterID           string
	instanceID          string
	redisStore          *state.RedisStateStore
	// fanOutShared dedups + memoizes cold QueryStream fan-outs per
	// (tenant, stream), same machinery as the HTTP /play path.
	fanOutShared    *balancer.SharedFanOut
	pendingDVRStops map[string]time.Time
	pendingDVRMu    sync.Mutex
	originPullMu    sync.Mutex
	originPulling   map[string]struct{}
	artifactCleaner *artifacts.Cleaner
	// Signed-policy-bundle cache wired in cmd/foghorn/main.go. nil
	// disables the bundle pathway; admission falls back to per-request
	// Commodore ResolvePlaybackPolicy calls.
	policyBundleCache   *policybundle.Cache
	policyBundleFetcher policybundle.FetchFunc
}

// quartermasterRoutingResolver is the narrow Quartermaster surface this
// server uses to resolve a tenant's official cluster + cluster_peers
// metadata (for S3 backing lookup).
type quartermasterRoutingResolver interface {
	GetClusterRouting(ctx context.Context, req *quartermasterpb.GetClusterRoutingRequest) (*quartermasterpb.ClusterRoutingResponse, error)
}

// storageResolverFactory builds a per-request storage.ClusterResolver. The
// factory is injected so tests can supply a stub without wiring real S3
// config. Production wires it from cmd/foghorn/main.go to read the local
// STORAGE_S3_* config and consult Quartermaster for advertised backings.
type storageResolverFactory func(ctx context.Context, tenantID string) *storage.ClusterResolver

// SetPolicyBundleCache wires the signed-policy-bundle cache and its
// fetcher. May be called once at startup; nil disables the bundle pathway
// and admission falls back to per-request policy lookups. The fetcher is
// stored separately because the cache type doesn't hold a default
// FetchFunc — admission supplies it per Get call.
func (s *FoghornGRPCServer) SetPolicyBundleCache(c *policybundle.Cache, fetcher policybundle.FetchFunc) {
	s.policyBundleCache = c
	s.policyBundleFetcher = fetcher
}

// NewFoghornGRPCServer creates a new Foghorn gRPC server
func NewFoghornGRPCServer(
	db *sql.DB,
	logger logging.Logger,
	lb *balancer.LoadBalancer,
	geoReader *geoip.Reader,
	geoCache *cache.Cache,
	decklogClient *decklog.BatchedClient,
	s3Client S3ClientInterface,
	purserClient *purserclient.GRPCClient,
) *FoghornGRPCServer {
	return &FoghornGRPCServer{
		db:              db,
		logger:          logger,
		lb:              lb,
		geoipReader:     geoReader,
		geoipCache:      geoCache,
		decklogClient:   decklogClient,
		s3Client:        s3Client,
		purserClient:    purserClient,
		fanOutShared:    balancer.NewSharedFanOut(5 * time.Second),
		pendingDVRStops: make(map[string]time.Time),
		originPulling:   make(map[string]struct{}),
	}
}

// RegisterServices registers all Foghorn gRPC services with the server
func (s *FoghornGRPCServer) RegisterServices(grpcServer *grpc.Server) {
	foghornpb.RegisterClipControlServiceServer(grpcServer, s)
	foghornpb.RegisterDVRControlServiceServer(grpcServer, s)
	foghornpb.RegisterViewerControlServiceServer(grpcServer, s)
	foghornpb.RegisterVodControlServiceServer(grpcServer, s)
	foghornpb.RegisterTenantControlServiceServer(grpcServer, s)
	foghornpb.RegisterNodeControlServiceServer(grpcServer, s)
	sharedpb.RegisterArtifactCreationStatusServiceServer(grpcServer, s)
}

// enrichClusterID returns the cluster for an operation. Prefers explicit
// cluster_id from the caller; otherwise resolves through the identity
// facade, which layers in-memory state (serving node's cluster) over the
// shared registry (origin cluster) — so an instance that never saw the
// ingest trigger flow still attributes correctly instead of returning "".
//
// Tenant-aware guard prevents cross-tenant stream name collisions from
// enriching artifacts with another tenant's cluster context.
func (s *FoghornGRPCServer) enrichClusterID(explicit, streamName, tenantID string) string {
	if explicit != "" {
		return explicit
	}
	resolver := identity.Default()
	if streamName == "" || resolver == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	id, err := resolver.ResolveStream(ctx, streamName)
	if err != nil {
		return ""
	}
	if tenantID != "" && id.TenantID != "" && id.TenantID != tenantID {
		return ""
	}
	if id.ServingCluster != "" {
		return id.ServingCluster
	}
	return id.OriginClusterID
}

// resolveDVROriginCluster resolves the SINGLE origin cluster reused for both the
// Commodore intent/business row (via RegisterDVR) and Foghorn's own artifact row,
// so the two planes never record different origins. It must be non-empty: an
// empty-origin intent can never converge (the ack drain's Foghorn resolver rejects
// an empty cluster), so it falls back to the storage node's cluster, then this
// Foghorn's own cell. ok is false only when all three are empty — the caller then
// fails the create instead of persisting an unconvergeable intent.
func (s *FoghornGRPCServer) resolveDVROriginCluster(req *sharedpb.StartDVRRequest, storageNodeID string) (cluster string, ok bool) {
	cluster = s.enrichClusterID(req.GetClusterId(), req.InternalName, req.GetTenantId())
	if cluster == "" {
		if ns := state.DefaultManager().GetNodeState(storageNodeID); ns != nil && ns.ClusterID != "" {
			cluster = ns.ClusterID
		}
	}
	if cluster == "" {
		cluster = s.clusterID
	}
	return cluster, cluster != ""
}

// SetCacheInvalidator sets the cache invalidator for tenant cache management
func (s *FoghornGRPCServer) SetCacheInvalidator(ci CacheInvalidator) {
	s.cacheInvalidator = ci
}

// SetArtifactCleaner wires the shared cleanup helper used by DeleteClip,
// DeleteDVR, and DeleteVodAsset to delete S3 bytes (locally or via the
// federation delete delegate). Wired from cmd/foghorn/main.go; nil in
// tests that don't focus on cleanup, in which case the delete handlers
// soft-delete only and report cleanup as pending.
func (s *FoghornGRPCServer) SetArtifactCleaner(c *artifacts.Cleaner) {
	s.artifactCleaner = c
}

// SetRemoteEdgeCache enables remote edge scoring for cross-cluster viewer routing.
func (s *FoghornGRPCServer) SetRemoteEdgeCache(cache *federation.RemoteEdgeCache, clusterID, instanceID string) {
	s.remoteEdgeCache = cache
	s.clusterID = clusterID
	s.instanceID = instanceID
}

// SetClusterID records this Foghorn's local cluster for storage placement.
func (s *FoghornGRPCServer) SetClusterID(clusterID string) {
	s.clusterID = clusterID
}

// SetRedisStateStore enables shared HA helpers that use Foghorn's cluster
// Redis state store but are owned by the gRPC server surface.
func (s *FoghornGRPCServer) SetRedisStateStore(store *state.RedisStateStore) {
	s.redisStore = store
}

// SetFederationClient enables cross-cluster QueryStream/NotifyOriginPull RPCs.
func (s *FoghornGRPCServer) SetFederationClient(fc *federation.FederationClient) {
	s.federationClient = fc
}

// SetPeerManager enables peer address lookups for federation calls.
// SetQuartermasterClient wires the Quartermaster client used to resolve a
// tenant's official cluster (CreateVodUpload, freeze flow). Wired from
// cmd/foghorn/main.go after qmClient is constructed.
func (s *FoghornGRPCServer) SetQuartermasterClient(qm quartermasterRoutingResolver) {
	s.quartermasterClient = qm
}

// SetStorageResolverFactory wires the per-request storage cluster resolver
// factory. Production wires this from cmd/foghorn/main.go with the local S3
// config + Quartermaster cluster_peers lookup; tests inject focused stubs.
func (s *FoghornGRPCServer) SetStorageResolverFactory(f storageResolverFactory) {
	s.storageResolver = f
}

// resolveVodStorageCluster runs the storage resolver for a VOD upload.
// Origin candidate is the caller-supplied cluster_id (the tenant's intended
// ingest cluster). Official candidate comes from Quartermaster's
// GetClusterRouting if a Quartermaster client is wired. Returns
// (cluster, mode); when no resolver factory is configured (tests / minimal
// dev setups), falls back to local-mint against the caller-supplied cluster.
func (s *FoghornGRPCServer) resolveVodStorageCluster(ctx context.Context, tenantID, ingestClusterID string) (string, storage.StorageMintMode) {
	if s.storageResolver == nil {
		// No resolver wired (tests / minimal dev setups) — preserve current
		// behaviour: assume local mint against the caller's cluster.
		return ingestClusterID, storage.StorageMintLocal
	}
	resolver := s.storageResolver(ctx, tenantID)
	if resolver == nil {
		return ingestClusterID, storage.StorageMintLocal
	}
	officialCluster := ""
	if s.quartermasterClient != nil {
		routingCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
		defer cancel()
		if routing, err := s.quartermasterClient.GetClusterRouting(routingCtx, &quartermasterpb.GetClusterRoutingRequest{TenantId: tenantID}); err == nil && routing != nil {
			officialCluster = strings.TrimSpace(routing.GetOfficialClusterId())
			if officialCluster == "" {
				// Quartermaster omits official_cluster_id when it equals the tenant's primary cluster;
				// normalize to the primary so single-cluster tenants still resolve a durable destination.
				officialCluster = strings.TrimSpace(routing.GetClusterId())
			}
		}
	}
	// Durable destination is the tenant's OFFICIAL cluster only (mirrors the freeze contract). The ingest/origin
	// cluster is source-authority attribution, NOT a durable-storage candidate — an advertised BYOC origin must
	// never win the durable write. A remote official → federation; an unresolved official → unavailable.
	return resolver.Resolve(storage.ResolverInput{
		OfficialClusterID: officialCluster,
	})
}

func (s *FoghornGRPCServer) SetPeerManager(pm *federation.PeerManager) {
	s.peerManager = pm
}

// forwardArtifactToFederation fans out a ForwardArtifactCommand to all known peers.
// Returns (handled, error). If any peer reports handled=true, stops immediately.
func (s *FoghornGRPCServer) forwardArtifactToFederation(ctx context.Context, command, artifactHash, tenantID, streamID string) (bool, error) {
	if ctx.Value(ctxkeys.KeyNoForward) != nil {
		return false, nil
	}
	if tenantID == "" {
		s.logger.WithFields(logging.Fields{
			"command":       command,
			"artifact_hash": artifactHash,
		}).Warn("Skipping federation forward for artifact command without tenant_id")
		return false, nil
	}
	if s.federationClient == nil || s.peerManager == nil {
		return false, nil
	}
	peers := s.peerManager.GetPeers()
	if len(peers) == 0 {
		return false, nil
	}

	fwdCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	clusterIDs := make([]string, 0, len(peers))
	for clusterID := range peers {
		clusterIDs = append(clusterIDs, clusterID)
	}
	sort.Strings(clusterIDs)

	req := &foghornfederationpb.ForwardArtifactCommandRequest{
		Command:      command,
		ArtifactHash: artifactHash,
		TenantId:     tenantID,
		StreamId:     streamID,
	}

	for _, clusterID := range clusterIDs {
		addr := peers[clusterID]
		if clusterID == s.clusterID {
			continue
		}
		resp, err := s.federationClient.ForwardArtifactCommand(fwdCtx, clusterID, addr, req)
		if err != nil {
			s.logger.WithError(err).WithFields(logging.Fields{
				"peer_cluster":  clusterID,
				"command":       command,
				"artifact_hash": artifactHash,
			}).Debug("Federation forward failed for peer")
			continue
		}
		if resp.GetHandled() {
			s.logger.WithFields(logging.Fields{
				"peer_cluster":  clusterID,
				"command":       command,
				"artifact_hash": artifactHash,
			}).Info("Artifact command handled by federation peer")
			return true, nil
		}
	}
	return false, nil
}

// remoteArtifactAdapter wraps RemoteEdgeCache to satisfy control.RemoteArtifactLookup.
type remoteArtifactAdapter struct {
	cache *federation.RemoteEdgeCache
}

// remoteArtifactLookup returns a RemoteArtifactLookup backed by the cache, or nil.
func (s *FoghornGRPCServer) remoteArtifactLookup() control.RemoteArtifactLookup {
	if s.remoteEdgeCache == nil {
		return nil
	}
	return &remoteArtifactAdapter{cache: s.remoteEdgeCache}
}

func (a *remoteArtifactAdapter) GetRemoteArtifacts(ctx context.Context, artifactHash string) ([]*control.RemoteArtifactInfo, error) {
	entries, err := a.cache.GetRemoteArtifacts(ctx, artifactHash)
	if err != nil {
		return nil, err
	}
	infos := make([]*control.RemoteArtifactInfo, 0, len(entries))
	for _, e := range entries {
		infos = append(infos, &control.RemoteArtifactInfo{
			PeerCluster:  e.PeerCluster,
			NodeID:       e.NodeID,
			BaseURL:      e.BaseURL,
			SizeBytes:    e.SizeBytes,
			AccessCount:  e.AccessCount,
			LastAccessed: e.LastAccessed,
			GeoLat:       e.GeoLat,
			GeoLon:       e.GeoLon,
		})
	}
	return infos, nil
}

func (s *FoghornGRPCServer) RegisterPendingDVRStop(internalName string) {
	if internalName == "" {
		return
	}
	if s.redisStore != nil {
		if err := s.redisStore.RegisterPendingDVRStop(context.Background(), internalName, time.Now()); err != nil {
			s.logger.WithError(err).WithField("internal_name", internalName).Warn("Failed to register pending DVR stop in Redis")
		}
		return
	}
	s.pendingDVRMu.Lock()
	s.pendingDVRStops[internalName] = time.Now()
	s.pendingDVRMu.Unlock()
}

func (s *FoghornGRPCServer) consumePendingDVRStop(internalName string) bool {
	if internalName == "" {
		return false
	}
	if s.redisStore != nil {
		ok, err := s.redisStore.ConsumePendingDVRStop(context.Background(), internalName)
		if err != nil {
			s.logger.WithError(err).WithField("internal_name", internalName).Warn("Failed to consume pending DVR stop from Redis")
		}
		return ok
	}
	s.pendingDVRMu.Lock()
	_, ok := s.pendingDVRStops[internalName]
	if ok {
		delete(s.pendingDVRStops, internalName)
	}
	s.pendingDVRMu.Unlock()
	return ok
}

func (s *FoghornGRPCServer) emitDVRStartFailure(req *sharedpb.StartDVRRequest, reason string) {
	// No foghorn.artifacts row exists yet at the early-validation failures that call this
	// (source/storage resolution rejects before the DVR INSERT), so there is no state row to
	// couple the event to. Enqueue the FAILED event durably (own statement, not gated on
	// decklogClient, error not swallowed); the outbox worker delivers it to Decklog.
	dvrData := &ipcpb.DVRLifecycleData{
		Status: ipcpb.DVRLifecycleData_STATUS_FAILED,
		Error:  &reason,
		StreamInternalName: func() *string {
			if req.InternalName != "" {
				return &req.InternalName
			}
			return nil
		}(),
		StreamId: func() *string {
			if req.StreamId != nil && *req.StreamId != "" {
				return req.StreamId
			}
			return nil
		}(),
		TenantId: func() *string {
			if req.TenantId != "" {
				return &req.TenantId
			}
			return nil
		}(),
		UserId: func() *string {
			if req.UserId != nil && *req.UserId != "" {
				return req.UserId
			}
			return nil
		}(),
	}
	if err := artifactoutbox.EnqueueDVRLifecycle(dvrData); err != nil {
		s.logger.WithError(err).WithField("internal_name", req.InternalName).Error("Failed to enqueue DVR start-failure lifecycle event")
	}
}

// resolveEffectiveDVRConfig clamps the caller-requested DVR window through
// pkg/dvrpolicy. Inputs come from the caller (StartDVRRequest carries the
// tier policy bundle and any caller-supplied window); cluster overrides
// come from the local Foghorn process env (one Foghorn per cluster).
//
// The live DVR window is INDEPENDENT of retention. Retention is post-end-
// only and computed at FinalizeDVR from the snapshotted dvr_retention_days
// column; it does not clamp the rolling Mist window.
func (s *FoghornGRPCServer) resolveEffectiveDVRConfig(req *sharedpb.StartDVRRequest) dvrpolicy.Effective {
	tier := dvrpolicy.Tier{}
	if p := req.GetDvrPolicy(); p != nil {
		tier.DefaultWindowSeconds = int(p.GetDefaultWindowSeconds())
		tier.MaxWindowSeconds = int(p.GetMaxWindowSeconds())
		tier.DefaultSegmentDurationSeconds = int(p.GetDefaultSegmentDurationSeconds())
		tier.MaxEntries = int(p.GetMaxEntries())
		tier.AllowClusterExtension = p.GetAllowClusterExtension()
	}
	cluster := dvrpolicy.Cluster{}
	if cfg := s.dvrClusterPolicy(); cfg != nil {
		cluster = *cfg
	}
	requested := int(req.GetDvrWindowSeconds())
	effective := dvrpolicy.Resolve(
		dvrpolicy.Request{DVRWindowSeconds: requested},
		tier,
		cluster,
	)
	// Surface clamps so operators can see tier/cluster ceilings biting in
	// production. Two distinct cases worth flagging: caller asked for more
	// than they got (request clamped) and tier asked for more than the
	// cluster allows (cluster cap biting).
	if requested > 0 && requested > effective.DVRWindowSeconds {
		s.logger.WithFields(logging.Fields{
			"tenant_id":           req.GetTenantId(),
			"requested_seconds":   requested,
			"effective_seconds":   effective.DVRWindowSeconds,
			"tier_max_seconds":    tier.MaxWindowSeconds,
			"cluster_max_seconds": cluster.MaxWindowSeconds,
		}).Info("DVR window clamped below caller request")
	}
	if effective.UsedDefaultFallback {
		s.logger.WithFields(logging.Fields{
			"tenant_id":         req.GetTenantId(),
			"effective_seconds": effective.DVRWindowSeconds,
		}).Warn("DVR policy missing tier defaults; using platform fallback window")
	}
	return effective
}

func waitForStreamSource(ctx context.Context, internalName string, timeout time.Duration) (nodeID string, baseURL string, ok bool) {
	if nodeID, baseURL, ok = control.GetStreamSource(internalName); ok {
		return nodeID, baseURL, true
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-waitCtx.Done():
			return "", "", false
		case <-ticker.C:
			if nodeID, baseURL, ok = control.GetStreamSource(internalName); ok {
				return nodeID, baseURL, true
			}
		}
	}
}

func waitForStreamSourceWithHint(ctx context.Context, internalName, sourceNodeHint string, timeout time.Duration) (nodeID string, baseURL string, ok bool) {
	if nodeID, baseURL, ok = sourceNodeFromHint(sourceNodeHint); ok {
		return nodeID, baseURL, true
	}
	return waitForStreamSource(ctx, internalName, timeout)
}

func sourceNodeFromHint(sourceNodeHint string) (nodeID string, baseURL string, ok bool) {
	sourceNodeHint = strings.TrimSpace(sourceNodeHint)
	if sourceNodeHint == "" {
		return "", "", false
	}
	ns := state.DefaultManager().GetNodeState(sourceNodeHint)
	if ns == nil || !ns.IsHealthy || ns.LastHeartbeat.IsZero() || time.Since(ns.LastHeartbeat) > 2*time.Minute {
		return "", "", false
	}
	return sourceNodeHint, ns.BaseURL, true
}

// dvrRetentionDays extracts the post-end retention days to snapshot onto
// foghorn.artifacts.dvr_retention_days at DVR start. FinalizeDVR reads this
// snapshot months later, never re-resolving a tenant tier that may have
// changed. Commodore has already run the per-class cascade and stamped the
// optional field; we trust it. Unset means a direct-Foghorn caller (test
// path, or a caller bypassing Commodore) — fall back to the 30-day system
// default.
//
// Return semantics: 0 means "keep forever" (FinalizeDVR writes NULL
// retention_until); >0 sets that many days.
func dvrRetentionDays(p *sharedpb.DVRPolicy) int32 {
	if p == nil || p.RecordingRetentionDays == nil {
		return 30
	}
	return *p.RecordingRetentionDays
}

// resolveArtifactInitialRetention computes the retention_until column for
// a new artifact insert (clip / VOD). When commodoreDays is non-nil, the
// upstream Commodore call has already run the full cascade (per-asset →
// per-stream → per-class tenant → system default → tier cap); we trust it.
// Otherwise (direct-Foghorn callers — tests, internal retries) we resolve
// locally against the tier cap and the per-class system default.
//
// Returned NullTime: Valid=false means write NULL retention_until (artifact
// never auto-expires).
func resolveArtifactInitialRetention(ctx context.Context, purser *purserclient.GRPCClient, tenantID string, commodoreDays *int32, systemDefaultDays int32, logger logging.Logger) sql.NullTime {
	const safeFallbackDays = 30

	if commodoreDays != nil {
		days := *commodoreDays
		if days <= 0 {
			return sql.NullTime{Valid: false}
		}
		return sql.NullTime{Valid: true, Time: time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour)}
	}

	cap := int32(0)
	if purser != nil && tenantID != "" {
		bs, err := purser.GetTenantBillingStatus(ctx, tenantID)
		if err != nil {
			logger.WithError(err).WithField("tenant_id", tenantID).Warn("Artifact retention: Purser billing status lookup failed; falling back to 30-day horizon")
			return sql.NullTime{Valid: true, Time: time.Now().UTC().Add(safeFallbackDays * 24 * time.Hour)}
		}
		if bs != nil {
			cap = bs.GetRecordingRetentionDays()
		}
	}
	intended := systemDefaultDays
	if intended <= 0 {
		if cap <= 0 {
			return sql.NullTime{Valid: false}
		}
		return sql.NullTime{Valid: true, Time: time.Now().UTC().Add(time.Duration(cap) * 24 * time.Hour)}
	}
	if cap > 0 && intended > cap {
		intended = cap
	}
	return sql.NullTime{Valid: true, Time: time.Now().UTC().Add(time.Duration(intended) * 24 * time.Hour)}
}

// dvrClusterPolicy returns the per-cluster DVR ceiling, if configured.
// Operator surface: gitops env file sets DVR_CLUSTER_MAX_WINDOW_SECONDS
// and DVR_CLUSTER_MAX_ENTRIES per cluster. Both default to 0 (no cluster
// override; tier ceilings stand). When set, dvrpolicy.Resolve clamps every
// resolved window through the cluster cap.
//
// Enterprise tenants whose tier flag AllowClusterExtension=true may have
// their max window raised by the cluster setting (up to platform_max=72h);
// non-enterprise tenants ignore the cluster window extension and only feel
// the cluster cap as a ceiling, not a floor.
func (s *FoghornGRPCServer) dvrClusterPolicy() *dvrpolicy.Cluster {
	maxWindow := config.GetEnvInt("DVR_CLUSTER_MAX_WINDOW_SECONDS", 0)
	maxEntries := config.GetEnvInt("DVR_CLUSTER_MAX_ENTRIES", 0)
	if maxWindow <= 0 && maxEntries <= 0 {
		return nil
	}
	return &dvrpolicy.Cluster{
		MaxWindowSeconds: maxWindow,
		MaxEntries:       maxEntries,
	}
}

// emitRoutingEvent sends a routing decision event via the shared builder.
// Delegates to handlers.SendRoutingEvent with the server's decklog client.
func (s *FoghornGRPCServer) emitRoutingEvent(
	primary *sharedpb.ViewerEndpoint,
	viewerLat, viewerLon, nodeLat, nodeLon float64,
	internalName, streamTenantID, streamID string,
	durationMs float32,
	candidatesCount int32,
	eventType, source string,
) {
	if s.decklogClient == nil || primary == nil {
		return
	}

	selectedNode := primary.BaseUrl
	if selectedNode == "" {
		selectedNode = primary.Url
	}

	go handlers.SendRoutingEvent(s.decklogClient, &handlers.RoutingEvent{
		Status:          "success",
		Details:         "grpc_resolve",
		Score:           uint64(primary.LoadScore),
		InternalName:    internalName,
		StreamID:        streamID,
		StreamTenantID:  streamTenantID,
		ClientLat:       viewerLat,
		ClientLon:       viewerLon,
		SelectedNode:    selectedNode,
		SelectedNodeID:  primary.NodeId,
		NodeLat:         nodeLat,
		NodeLon:         nodeLon,
		NodeName:        primary.NodeId,
		LatencyMs:       durationMs,
		CandidatesCount: candidatesCount,
		EventType:       eventType,
		Source:          source,
	})
}

// CLIP CONTROL SERVICE IMPLEMENTATION

// buildClipLifecycleData creates an enriched ClipLifecycleData with timing fields
// CRITICAL: This function fixes the missing enrichment bug documented in ipc.proto lines 575-580
func buildClipLifecycleData(stage ipcpb.ClipLifecycleData_Stage, req *sharedpb.CreateClipRequest, reqID, clipHash string) *ipcpb.ClipLifecycleData {
	data := &ipcpb.ClipLifecycleData{
		Stage:     stage,
		RequestId: &reqID,
	}
	if clipHash != "" {
		data.ClipHash = clipHash
	}
	if req.TenantId != "" {
		data.TenantId = &req.TenantId
	}
	if req.StreamInternalName != "" {
		data.StreamInternalName = &req.StreamInternalName
	}
	if req.StreamId != nil && *req.StreamId != "" {
		data.StreamId = req.StreamId
	}
	// CRITICAL: Enrich with timing fields for analytics
	if req.StartUnix != nil {
		data.StartUnix = req.StartUnix
	}
	if req.StopUnix != nil {
		data.StopUnix = req.StopUnix
	}
	if req.StartMs != nil {
		data.StartMs = req.StartMs
	}
	if req.StopMs != nil {
		data.StopMs = req.StopMs
	}
	if req.DurationSec != nil {
		data.DurationSec = req.DurationSec
	}
	// Include mode for analytics
	if req.Mode != sharedpb.ClipMode_CLIP_MODE_UNSPECIFIED {
		modeStr := req.Mode.String()
		data.ClipMode = &modeStr
	}
	if req.ExpiresAt != nil {
		data.ExpiresAt = req.ExpiresAt
	}
	if req.UserId != nil && *req.UserId != "" {
		data.UserId = req.UserId
	}
	return data
}

func clipProcessingSourceKind(kind ipcpb.ClipPullRequest_SourceKind) string {
	switch kind {
	case ipcpb.ClipPullRequest_SOURCE_KIND_LIVE:
		return "live"
	case ipcpb.ClipPullRequest_SOURCE_KIND_DVR_ROLLING:
		return "dvr_rolling"
	case ipcpb.ClipPullRequest_SOURCE_KIND_CHAPTER:
		return "chapter"
	default:
		return ""
	}
}

func clipProcessingPreferredNode(nodeID string) string {
	if nodeID == "" {
		return ""
	}
	node := state.DefaultManager().GetNodeState(nodeID)
	if node == nil || !node.CapProcessing || !node.IsHealthy {
		return ""
	}
	if !node.CanRunClass(mist.ProcessingClassVideoTranscode) {
		return ""
	}
	return nodeID
}

// CreateClip creates a new clip from a stream. It records the attempt in Foghorn's command
// ledger — 'accepted' before any fallible precheck, then 'committed' atomically with the
// artifact row, or a deferred 'rejected' on any pre-commit exit — which Commodore's
// convergence sweep reads to terminalize the intent. See docs/architecture/creation-saga.md.
func (s *FoghornGRPCServer) CreateClip(ctx context.Context, req *sharedpb.CreateClipRequest) (resp *sharedpb.CreateClipResponse, err error) {
	var prog creationLedgerProgress
	defer func() {
		err = s.finalizeCreationCommand(req.GetRequestId(), req.GetTenantId(), "clip", req.GetClipHash(), &prog, err)
	}()
	resp, err = s.createClipImpl(ctx, req, &prog)
	return resp, err
}

// existingClipResult returns the idempotent CreateClip response for a clip artifact
// that already exists (a retry after a lost response). found is false when no such
// artifact exists. The fulfilled range is recovered from the durable processing job so
// the response matches the original create; a second pass must not create a second
// artifact/job/outbox event.
func (s *FoghornGRPCServer) existingClipResult(ctx context.Context, tenantID, clipHash, playbackID string) (resp *sharedpb.CreateClipResponse, found bool, err error) {
	if tenantID == "" || clipHash == "" {
		return nil, false, nil
	}
	var existingStatus string
	idErr := s.db.QueryRowContext(ctx, `
		SELECT status FROM foghorn.artifacts
		 WHERE artifact_hash = $1 AND artifact_type = 'clip' AND tenant_id = $2
	`, clipHash, tenantID).Scan(&existingStatus)
	if errors.Is(idErr, sql.ErrNoRows) {
		return nil, false, nil
	}
	if idErr != nil {
		return nil, false, status.Errorf(codes.Internal, "failed to check existing clip artifact: %v", idErr)
	}
	startMsEff, durationMsEff := s.clipFulfilledTimingMs(ctx, tenantID, clipHash)
	return &sharedpb.CreateClipResponse{
		Status:              existingStatus,
		ClipHash:            clipHash,
		PlaybackId:          playbackID,
		EffectiveStartMs:    startMsEff,
		EffectiveDurationMs: durationMsEff,
	}, true, nil
}

func (s *FoghornGRPCServer) createClipImpl(ctx context.Context, req *sharedpb.CreateClipRequest, prog *creationLedgerProgress) (*sharedpb.CreateClipResponse, error) {
	if req.StreamInternalName == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_internal_name is required")
	}
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.GetInternalName() == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name is required")
	}
	if strings.TrimSpace(req.GetProcessesJson()) == "" {
		return nil, status.Error(codes.FailedPrecondition, "clip processing profile unavailable")
	}
	// The durable creation ledger is keyed by (request_id, clip_hash). When request_id is set (an
	// intent-driven Commodore create) the caller-minted clip_hash MUST accompany it: otherwise 'accepted'
	// would be recorded under the empty hash while 'committed' keys on the hash Foghorn later generates, so
	// the commit CAS could never match and the artifact transaction would always roll back. Commodore always
	// sends both; enforce the contract so a caller that omits the hash fails fast rather than silently.
	if req.GetRequestId() != "" && strings.TrimSpace(req.GetClipHash()) == "" {
		return nil, status.Error(codes.InvalidArgument, "clip_hash is required when request_id is set")
	}

	// Record 'accepted' in the durable command ledger (keyed by request_id + clip_hash)
	// BEFORE any fallible precheck: a failure past this point is a still-'accepted' row the
	// deferred finalizer flips to 'rejected', never an absent row the sweep polls forever. A
	// failed durable write fails the RPC so the client re-drives. See
	// docs/architecture/creation-saga.md.
	acceptState, acceptErr := s.recordCreationCommandAcceptedDurable(ctx, req.GetRequestId(), req.GetTenantId(), "clip", req.GetClipHash(), prog)
	if acceptErr != nil {
		s.logger.WithError(acceptErr).WithField("clip_hash", req.GetClipHash()).Error("Failed to record accepted clip creation command")
		if errors.Is(acceptErr, errCreationCommandIdentityMismatch) {
			return nil, status.Error(codes.FailedPrecondition, "request_id already used for a different artifact")
		}
		return nil, status.Errorf(codes.Unavailable, "failed to record clip creation attempt: %v", acceptErr)
	}
	// A terminal retry must not resume the create's external work. 'rejected' is
	// definitive; 'committed' means the clip artifact is already durable, so short-circuit
	// to the idempotent existing-artifact result (keyed by the request-carried clip_hash)
	// without re-dispatching.
	switch acceptState {
	case creationCommandRejected:
		return nil, status.Error(codes.FailedPrecondition, "clip creation was terminally rejected")
	case creationCommandCommitted:
		resp, found, existErr := s.existingClipResult(ctx, req.GetTenantId(), req.GetClipHash(), req.GetPlaybackId())
		if existErr != nil {
			return nil, existErr
		}
		if !found {
			return nil, status.Error(codes.Internal, "committed clip command has no artifact row")
		}
		s.logger.WithField("clip_hash", req.GetClipHash()).Info("CreateClip retry of a committed create; returning existing result (idempotent)")
		return resp, nil
	}

	// Clip size is not known until export completes; reject only when the
	// tenant is already at cap. See checkStorageEntitlement docs.
	if err := s.checkStorageEntitlement(ctx, req.TenantId, 0); err != nil {
		return nil, err
	}

	format := "mkv"

	// Select the storage-capable node that will write the clip artifact. The
	// source node can differ; Helmsman then pulls from that source node's Mist
	// /view endpoint and writes locally. TENANT ISOLATION: scope selection to this
	// tenant so a tenant-operated storage node can never receive another tenant's
	// durable work (the balancer only applies the isolation when the scope is set).
	sctx := context.WithValue(ctx, ctxkeys.KeyCapability, "storage")
	if req.TenantId != "" {
		sctx = context.WithValue(sctx, ctxkeys.KeyClusterScope, req.TenantId)
	}
	storageHost, _, _, _, _, err := s.lb.GetBestNodeWithScore(sctx, "", 0, 0, map[string]int{}, "", false)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "no storage node available: %v", err)
	}
	storageNodeID := s.lb.GetNodeIDByHost(storageHost)
	if storageNodeID == "" {
		return nil, status.Error(codes.Unavailable, "storage node not connected")
	}

	// Generate request_id for correlation
	reqID := uuid.New().String()

	// Normalize the requested clip range to absolute Unix-ms across all
	// ClipModes. The dispatcher (pickClipSource) compares against the
	// live shm boundary, rolling DVR window, and chapter ranges in
	// absolute wall-clock — every mode has to land in that space
	// before dispatch. Hash/storage still use the start+duration shape
	// downstream.
	clipStartMsAbs, clipEndMsAbs, normErr := resolveClipAbsoluteRangeMs(req, req.StreamInternalName)
	if normErr != nil {
		return nil, status.Errorf(codes.InvalidArgument, "resolve clip range: %v", normErr)
	}
	startMs := clipStartMsAbs
	durationMs := clipEndMsAbs - clipStartMsAbs

	// Use provided clip_hash from Commodore if available, otherwise generate locally
	var clipHash string
	if req.GetClipHash() != "" {
		clipHash = req.GetClipHash()
	} else {
		// Generate a hash locally when Commodore does not provide one.
		var errHash error
		clipHash, errHash = clips.GenerateClipHash(req.StreamInternalName, startMs, durationMs)
		if errHash != nil {
			s.logger.WithError(errHash).Error("Failed to generate clip hash")
			return nil, status.Error(codes.Internal, "failed to generate clip hash")
		}
	}

	// Idempotency: a retry after a lost response (client retry, or Commodore
	// re-driving the same intent) carries the SAME Commodore-minted clip_hash,
	// which is the artifact PK. If a clip artifact already exists for this
	// identity, return the SAME fulfilled range instead of re-dispatching — a
	// second pass must not create a second artifact/job/outbox event. The
	// fulfilled range is recovered from the durable processing job so the
	// response matches the original.
	if req.GetTenantId() != "" {
		resp, found, existErr := s.existingClipResult(ctx, req.GetTenantId(), clipHash, req.GetPlaybackId())
		if existErr != nil {
			return nil, existErr
		}
		if found {
			// The artifact already exists (a retry after a lost response). Ensure this
			// attempt's command row is 'committed' before returning success, so a live
			// artifact is never left behind a forever-'accepted' command the sweep would
			// poll as in-flight.
			if ensErr := s.ensureCreationCommandCommitted(ctx, req.GetRequestId(), req.GetTenantId(), "clip", clipHash, prog); ensErr != nil {
				return nil, status.Errorf(codes.Unavailable, "failed to finalize clip creation command: %v", ensErr)
			}
			s.logger.WithField("clip_hash", clipHash).Info("CreateClip is a retry for an existing clip artifact; returning existing result (idempotent)")
			return resp, nil
		}
	}

	// Emit STAGE_REQUESTED event to Decklog (with enriched timing fields)
	// Cluster attribution must never end up NULL: thumbnail readiness,
	// freeze storage resolution, and Chandler URL construction all key off
	// it. Stream-state enrichment misses for bare mist_native sources, so
	// fall back to the dispatch target's cluster, then this Foghorn's own.
	clipCluster := s.enrichClusterID(req.GetClusterId(), req.StreamInternalName, req.GetTenantId())
	if clipCluster == "" {
		if ns := state.DefaultManager().GetNodeState(storageNodeID); ns != nil && ns.ClusterID != "" {
			clipCluster = ns.ClusterID
		}
	}
	if clipCluster == "" {
		clipCluster = s.clusterID
	}
	// STAGE_REQUESTED precedes the foghorn.artifacts INSERT (dispatch can still reject the request
	// before any row is written), so there is no artifact/command-ledger row to couple it to. It is
	// therefore explicitly BEST-EFFORT analytics: the enqueue failure is logged and does NOT fail
	// the create — the durable record of this attempt is the command ledger ('accepted', written
	// earlier) plus the artifact row written below, not this pre-INSERT event. The outbox worker
	// delivers enqueued events to Decklog with retry.
	{
		clipData := buildClipLifecycleData(ipcpb.ClipLifecycleData_STAGE_REQUESTED, req, reqID, clipHash)
		if clipCluster != "" {
			clipData.OriginClusterId = &clipCluster
			clipData.ServingClusterId = &clipCluster
		}
		if enqErr := artifactoutbox.EnqueueClipLifecycle(clipData); enqErr != nil {
			s.logger.WithError(enqErr).WithField("clip_hash", clipHash).Error("Failed to enqueue clip requested lifecycle event (best-effort; create proceeds)")
		}
	}

	// Source dispatch first — coverage-aware best-effort selection of
	// LIVE / DVR_ROLLING / CHAPTER (see pickClipSource). Invalid ranges,
	// genuine assessment errors, and ranges that overlap no source all
	// reject HERE, before any foghorn.artifacts / artifact_nodes inserts,
	// so a rejected request never leaves an orphan clip row behind.
	// (requested_params for audit are stored by Commodore, not Foghorn.)
	clipEndMs := startMs + durationMs
	liveCov, dvrCov, chapCov, dispatchErr := s.computeClipCoverages(ctx, req.TenantId, req.StreamInternalName, startMs, clipEndMs)
	var dispatch clipSourceDecision
	if dispatchErr == nil {
		dispatch, dispatchErr = chooseClipSource(startMs, clipEndMs, liveCov, dvrCov, chapCov)
	}
	if dispatchErr != nil {
		// Dispatch rejected before any foghorn.artifacts row was written (INSERT happens later),
		// so there is no state row to couple this to. Enqueue durably, no decklogClient gate.
		{
			failedData := buildClipLifecycleData(ipcpb.ClipLifecycleData_STAGE_FAILED, req, reqID, clipHash)
			errMsg := fmt.Sprintf("clip source dispatch: %v", dispatchErr)
			failedData.Error = &errMsg
			if clipCluster != "" {
				failedData.OriginClusterId = &clipCluster
				failedData.ServingClusterId = &clipCluster
			}
			if enqErr := artifactoutbox.EnqueueClipLifecycle(failedData); enqErr != nil {
				s.logger.WithError(enqErr).WithField("clip_hash", clipHash).Error("Failed to enqueue clip dispatch-failed lifecycle event")
			}
		}
		s.logger.WithFields(logging.Fields{
			"tenant_id":     req.GetTenantId(),
			"stream_id":     req.GetStreamId(),
			"internal_name": req.GetStreamInternalName(),
			"clip_hash":     clipHash,
			"start_ms":      startMs,
			"end_ms":        clipEndMs,
			"error":         dispatchErr,
		}).Warn("Rejected clip source dispatch")
		return nil, status.Errorf(codes.FailedPrecondition, "clip source dispatch: %v", dispatchErr)
	}
	// Live-style sources are harvested from their recording node when
	// that differs from the clip output node. Same-node pulls use the
	// local Mist configured beside Helmsman; remote pulls use the source
	// node's public /view surface.
	var sourceHost string
	var sourceNodeID string
	var ingestHost string
resolve:
	for {
		switch dispatch.kind {
		case ipcpb.ClipPullRequest_SOURCE_KIND_LIVE:
			// Derive from sctx (NOT ctx) so KeyClusterScope=req.TenantId carries through: the live source
			// selection must stay tenant-isolated, since a source that advertises storage can be adopted as the
			// durable destination below and a cross-tenant node must never be reachable through that path.
			ictx := context.WithValue(sctx, ctxkeys.KeyCapability, "ingest")
			host, _, _, _, _, ingestErr := s.lb.GetBestNodeWithScore(ictx, req.StreamInternalName, 0, 0, map[string]int{}, "", true)
			liveNodeID := ""
			if ingestErr == nil {
				liveNodeID = s.lb.GetNodeIDByHost(host)
			}
			if ingestErr != nil || liveNodeID == "" {
				// Live was the best coverage but its source node is
				// unroutable (no ingest node, or the host is not a
				// connected node). Drop the live candidate and re-rank
				// among the recorded sources rather than failing the clip.
				// A live-fully-covered request short-circuits recorded
				// assessment, so the DVR/chapter candidates may be empty —
				// assess them now that a recorded fallback is needed.
				liveCov = clipCoverage{kind: ipcpb.ClipPullRequest_SOURCE_KIND_LIVE}
				recordedDVR, recordedChap, recErr := s.computeRecordedCoverages(ctx, req.TenantId, req.StreamInternalName, startMs, clipEndMs)
				if recErr != nil {
					return nil, status.Errorf(codes.Unavailable, "live clip source unroutable and recorded source assessment failed: %v", recErr)
				}
				dvrCov, chapCov = recordedDVR, recordedChap
				fallback, reErr := chooseClipSource(startMs, clipEndMs, liveCov, dvrCov, chapCov)
				if reErr != nil {
					return nil, status.Errorf(codes.Unavailable, "live clip source unroutable and no recorded source covers the range: %v", ingestErr)
				}
				s.logger.WithError(ingestErr).WithFields(logging.Fields{
					"clip_hash":     clipHash,
					"internal_name": req.StreamInternalName,
					"fallback_kind": fallback.kind.String(),
				}).Warn("Live clip source unroutable; falling back to recorded source")
				dispatch = fallback
				ingestHost = ""
				continue resolve
			}
			ingestHost = host
			sourceHost = host
			sourceNodeID = liveNodeID
		case ipcpb.ClipPullRequest_SOURCE_KIND_DVR_ROLLING:
			dvrNodeID := dispatch.sourceNodeID
			var dvrHost string
			var hostErr error
			if dvrNodeID != "" {
				dvrHost, hostErr = s.lb.GetNodeByID(dvrNodeID)
			}
			if dvrNodeID == "" || hostErr != nil {
				// The winning DVR's recording node is not in current state
				// (absent/disconnected). Drop the DVR candidate and re-rank
				// among the remaining sources rather than failing the clip.
				dvrCov = clipCoverage{kind: ipcpb.ClipPullRequest_SOURCE_KIND_DVR_ROLLING}
				fallback, reErr := chooseClipSource(startMs, clipEndMs, liveCov, dvrCov, chapCov)
				if reErr != nil {
					return nil, status.Errorf(codes.Unavailable, "active DVR recording node unavailable and no other source covers the range: %v", hostErr)
				}
				s.logger.WithError(hostErr).WithFields(logging.Fields{
					"clip_hash":     clipHash,
					"internal_name": req.StreamInternalName,
					"dvr_node_id":   dvrNodeID,
					"fallback_kind": fallback.kind.String(),
				}).Warn("DVR recording node unroutable; falling back to another source")
				dispatch = fallback
				continue resolve
			}
			sourceNodeID = dvrNodeID
			sourceHost = dvrHost
		}
		break resolve
	}
	if sourceNodeID != "" {
		if sourceNode := state.DefaultManager().GetNodeState(sourceNodeID); sourceNode != nil && sourceNode.CapStorage {
			// Co-locate the clip write on the source node (avoids a cross-node pull) ONLY when that node is a
			// valid tenant-scoped storage destination — the same isolation the initial GetBestNodeWithScore(sctx)
			// applied. A source node dedicated to another tenant advertising storage must NOT silently replace
			// the authorized destination; fall back to the tenant-scoped storage node in that case.
			tenantOK := req.TenantId == "" || sourceNode.TenantID == "" || sourceNode.TenantID == req.TenantId
			if tenantOK {
				storageNodeID = sourceNodeID
				storageHost = sourceHost
			} else {
				s.logger.WithFields(logging.Fields{
					"clip_hash":       clipHash,
					"source_node_id":  sourceNodeID,
					"source_tenant":   sourceNode.TenantID,
					"req_tenant":      req.TenantId,
					"storage_node_id": storageNodeID,
				}).Warn("Source node advertises storage but is dedicated to another tenant; keeping tenant-scoped storage destination")
			}
		}
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":              req.GetTenantId(),
		"stream_id":              req.GetStreamId(),
		"internal_name":          req.GetStreamInternalName(),
		"clip_hash":              clipHash,
		"start_ms":               startMs,
		"end_ms":                 clipEndMs,
		"source_kind":            dispatch.kind.String(),
		"source_stream":          dispatch.streamName,
		"source_node_id":         dispatch.sourceNodeID,
		"source_dvr_hash":        dispatch.dvrHash,
		"source_chapter_hash":    dispatch.chapterArtifactHash,
		"effective_start_ms":     dispatch.effectiveStartMs,
		"effective_end_ms":       dispatch.effectiveEndMs,
		"partial":                dispatch.partial,
		"coverage_ms":            dispatch.coverageMs,
		"reason":                 dispatch.reason,
		"requested_clip_mode":    req.GetMode().String(),
		"requested_duration_sec": req.GetDurationSec(),
	}).Info("Selected clip source dispatch")

	storagePath := clips.BuildClipStoragePath(req.StreamInternalName, clipHash, format)
	clipRetentionUntil := resolveArtifactInitialRetention(ctx, s.purserClient, req.TenantId, req.RetentionDays, 30 /* clip system default */, s.logger)

	// Resolve the source stream name and base URL BEFORE creating any artifact row. The LIVE case
	// can fail (unroutable source); resolving it here means a rejected request never leaves an
	// orphan clip row behind. Range normalization, dispatch, and source-node selection above are
	// already pre-insert, so all failure paths that reject the request precede the artifact insert.
	sourceStreamName := dispatch.streamName
	sourceBaseURL := ""
	switch dispatch.kind {
	case ipcpb.ClipPullRequest_SOURCE_KIND_LIVE:
		var sourceErr error
		sourceStreamName, sourceErr = clipLiveSourceStreamName(ctx, req)
		if sourceErr != nil {
			return nil, status.Errorf(codes.FailedPrecondition, "clip source route unavailable: %v", sourceErr)
		}
		if sourceHost != "" && sourceNodeID != "" && sourceNodeID != storageNodeID {
			sourceBaseURL = control.DeriveMistHTTPBase(sourceHost)
		}
	case ipcpb.ClipPullRequest_SOURCE_KIND_DVR_ROLLING:
		if sourceHost != "" && sourceNodeID != "" && sourceNodeID != storageNodeID {
			sourceBaseURL = control.DeriveMistHTTPBase(sourceHost)
		}
	}

	// The fulfilled range is already whole-second aligned by source selection
	// (chooseClipSource ranks and clamps on harvestable seconds, rejecting a
	// sub-second collapse before any artifact insert). source_start_unix /
	// source_stop_unix are exact divisions, and the same range is reported back
	// to Commodore as start_time/duration.
	startUnix := dispatch.effectiveStartMs / 1000
	stopUnix := dispatch.effectiveEndMs / 1000
	sourceKind := clipProcessingSourceKind(dispatch.kind)
	if sourceKind == "" {
		return nil, status.Errorf(codes.Internal, "unsupported clip source kind: %s", dispatch.kind.String())
	}
	preferredNodeID := clipProcessingPreferredNode(sourceNodeID)
	if sourceHost != "" && sourceNodeID != "" && preferredNodeID != sourceNodeID {
		sourceBaseURL = control.DeriveMistHTTPBase(sourceHost)
	}
	sourceParams := map[string]string{
		"source_kind":        sourceKind,
		"source_stream_name": sourceStreamName,
		"source_format":      format,
		"source_start_unix":  strconv.FormatInt(startUnix, 10),
		"source_stop_unix":   strconv.FormatInt(stopUnix, 10),
		"output_stream_name": req.StreamInternalName,
	}
	if sourceBaseURL != "" {
		sourceParams["source_base_url"] = sourceBaseURL
	}
	if dispatch.dvrHash != "" {
		sourceParams["source_dvr_hash"] = dispatch.dvrHash
	}
	if dispatch.chapterArtifactHash != "" {
		sourceParams["source_chapter_artifact_hash"] = dispatch.chapterArtifactHash
	}

	// Create the clip artifact (directly as 'queued'), insert its processing job, and emit the QUEUED
	// lifecycle event in ONE transaction (durable outbox). Every resolution/validation that can reject
	// the request ran above, before any row exists, so this commit is the artifact's first durable
	// state. Committing the artifact row, the processing job, and the QUEUED event together is atomic: a
	// clip is never durable without its processing job. InsertProcessingJobWithSourceParamsTx joins THIS
	// tx via pg_advisory_xact_lock (a transaction-scoped advisory lock, released on our commit/rollback),
	// so the per-artifact insert dedup holds while composing with the outer tx. The event carries the
	// tenant id (buildClipLifecycleData sets it from req.TenantId); the outbox rejects an empty-tenant
	// event, which would roll this tx back. If the job insert fails, the whole tx rolls back — no orphan
	// clip, no orphan job.
	queuedData := buildClipLifecycleData(ipcpb.ClipLifecycleData_STAGE_QUEUED, req, reqID, clipHash)
	if clipCluster != "" {
		queuedData.OriginClusterId = &clipCluster
		queuedData.ServingClusterId = &clipCluster
	}
	var jobID string
	if txErr := s.withArtifactLifecycleTx(ctx, func(tx *sql.Tx) error {
		if _, execErr := tx.ExecContext(ctx, `
			INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, stream_internal_name, internal_name, stream_id, tenant_id, user_id, status, request_id, manifest_path, format, origin_cluster_id, retention_until, created_at, updated_at)
			VALUES ($1, 'clip', $2, $3, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, NULLIF($6, '')::uuid, 'queued', $7, $8, $9, $10, $11, NOW(), NOW())
		`, clipHash, req.StreamInternalName, req.GetInternalName(), req.GetStreamId(), req.TenantId, req.GetUserId(), reqID, storagePath, format, clipCluster, clipRetentionUntil); execErr != nil {
			return execErr
		}
		insertedJobID, jobErr := jobs.InsertProcessingJobWithSourceParamsTx(ctx, tx, req.TenantId, clipHash, "process", nil, req.GetProcessesJson(), "", sourceParams, preferredNodeID)
		if jobErr != nil {
			return jobErr
		}
		jobID = insertedJobID
		// Commit the command ledger in the SAME tx as the artifact row so the
		// 'committed' outcome (with the artifact's catalog_revision) is durable
		// together with the clip.
		if cmdErr := recordCreationCommandCommitted(ctx, tx, req.GetRequestId(), req.GetTenantId(), "clip", clipHash); cmdErr != nil {
			return cmdErr
		}
		return artifactoutbox.EnqueueClipLifecycleTx(ctx, tx, queuedData)
	}); txErr != nil {
		s.logger.WithFields(logging.Fields{
			"clip_hash":     clipHash,
			"internal_name": req.StreamInternalName,
			"error":         txErr,
		}).Error("Failed to store clip artifact, processing job, and queued lifecycle event")
		return nil, status.Error(codes.Internal, "failed to store artifact")
	}
	// The artifact row and its 'committed' ledger row committed together; the deferred
	// finalizer must not now record a contradictory 'rejected'.
	prog.committed = true
	// The durable queue write committed; wake local dispatchers. The tx variant intentionally does not
	// notify (the row is not durable until commit), so this is the notify site.
	jobs.NotifyProcessingJobQueued()
	s.logger.WithFields(logging.Fields{
		"clip_hash":       clipHash,
		"job_id":          jobID,
		"preferred_node":  preferredNodeID,
		"source_kind":     sourceKind,
		"source_stream":   sourceStreamName,
		"source_base_url": sourceBaseURL,
	}).Info("Queued clip processing job")

	// The artifact was created as 'queued' and its STAGE_QUEUED lifecycle event was committed in the
	// same transaction above, so there is no separate (swallow-prone) enqueue here.

	// Mirror the durable phase onto the in-memory stream state. The artifact row, the outbox event,
	// and the RPC response all report 'queued'; publishing an older 'requested' here would let a
	// consumer observe a regressed lifecycle phase relative to what durably committed.
	state.DefaultManager().UpdateStreamInstanceInfo(req.StreamInternalName, sourceNodeID, map[string]any{
		"clip_status":     "queued",
		"clip_request_id": reqID,
		"clip_format":     format,
	})

	return &sharedpb.CreateClipResponse{
		Status:              "queued",
		IngestHost:          ingestHost,
		StorageHost:         storageHost,
		NodeId:              storageNodeID,
		RequestId:           reqID,
		ClipHash:            clipHash,
		PlaybackId:          req.GetPlaybackId(),
		EffectiveStartMs:    dispatch.effectiveStartMs,
		EffectiveDurationMs: dispatch.effectiveEndMs - dispatch.effectiveStartMs,
		Partial:             dispatch.partial,
	}, nil
}

// DeleteStreamThumbnails removes a live stream's thumbnail control rows + objects. Commodore calls this on stream
// deletion: a live stream (asset_key = stream_id) has no artifact row, so the purge job never reaches it, and its
// FINAL active version is never superseded, so version-supersession GC never reclaims it — without this the
// pointer + objects would be stranded. S3 deletion is routed by the thumbnail's OWN destination cluster.
func (s *FoghornGRPCServer) DeleteStreamThumbnails(ctx context.Context, req *sharedpb.DeleteStreamThumbnailsRequest) (*sharedpb.DeleteStreamThumbnailsResponse, error) {
	streamID := strings.TrimSpace(req.GetStreamId())
	tenantID := strings.TrimSpace(req.GetTenantId())
	if streamID == "" || tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id and tenant_id are required")
	}
	dbh := control.GetDB()
	// Order: read destinations → sweep S3 → ONLY THEN drop the control rows. Sweeping S3 BEFORE deleting the rows
	// means a sweep failure RETAINS the rows (and their destination attribution) so a retry can complete, instead
	// of losing where the objects live; and we NEVER report success when cleanup didn't happen (no false ack).
	// A live stream is being deleted, so there is no concurrent publication to fence against (unlike the purge
	// job, which drops rows first to fence artifact republication).
	if s.artifactCleaner == nil {
		s.logger.WithField("stream_id", streamID).Warn("Artifact cleaner not wired; stream thumbnails NOT cleaned")
		return &sharedpb.DeleteStreamThumbnailsResponse{Success: false, Message: "cleaner not wired; thumbnails not cleaned"}, nil
	}
	clusters, cErr := control.ThumbnailDestinationClusters(ctx, dbh, tenantID, streamID)
	if cErr != nil {
		return nil, status.Errorf(codes.Internal, "read thumbnail destinations: %v", cErr)
	}
	if len(clusters) == 0 {
		clusters = []string{""}
	}
	for _, c := range clusters {
		if sErr := s.artifactCleaner.DeleteThumbnailsOnCluster(ctx, tenantID, streamID, c); sErr != nil {
			// Retain the control rows (destination attribution) for a retry; report the honest failure.
			s.logger.WithError(sErr).WithFields(logging.Fields{"stream_id": streamID, "destination_cluster": c}).
				Warn("Stream thumbnail S3 sweep failed; control rows retained for retry")
			return &sharedpb.DeleteStreamThumbnailsResponse{Success: false, Message: "s3 cleanup failed: " + sErr.Error()}, nil
		}
	}
	// S3 objects confirmed gone → drop the control rows. A failure here leaves harmless orphan rows (the objects
	// are already deleted); surface it as an internal error so the caller can log/retry.
	if dErr := control.DeleteThumbnailControlRows(ctx, dbh, tenantID, streamID); dErr != nil {
		return nil, status.Errorf(codes.Internal, "delete thumbnail control rows: %v", dErr)
	}
	return &sharedpb.DeleteStreamThumbnailsResponse{Success: true}, nil
}

// DeleteClip deletes a clip
func (s *FoghornGRPCServer) DeleteClip(ctx context.Context, req *sharedpb.DeleteClipRequest) (*sharedpb.DeleteClipResponse, error) {
	if req.ClipHash == "" {
		return nil, status.Error(codes.InvalidArgument, "clip_hash is required")
	}
	// NOTE: tenant_id validation now happens at Commodore level

	// Check current status from foghorn.artifacts
	var (
		currentStatus    string
		sizeBytes        sql.NullInt64
		retentionUntil   sql.NullTime
		internalName     sql.NullString
		denormTenantID   sql.NullString
		denormUserID     sql.NullString
		format           sql.NullString
		storageClusterID sql.NullString
		originClusterID  sql.NullString
		activeObjectKey  sql.NullString
		activeDtshKey    sql.NullString
		syncObjectKey    sql.NullString
		durableLocal     sql.NullBool
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT status, size_bytes, retention_until, stream_internal_name, tenant_id, user_id,
		       format, storage_cluster_id, origin_cluster_id, active_object_key,
		       active_dtsh_key, sync_object_key, durable_backend_local
		FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND artifact_type = 'clip' AND tenant_id = $2
	`, req.ClipHash, req.GetTenantId()).Scan(&currentStatus, &sizeBytes, &retentionUntil, &internalName, &denormTenantID, &denormUserID, &format, &storageClusterID, &originClusterID, &activeObjectKey, &activeDtshKey, &syncObjectKey, &durableLocal)

	if errors.Is(err, sql.ErrNoRows) {
		if handled, _ := s.forwardArtifactToFederation(ctx, "delete_clip", req.ClipHash, req.GetTenantId(), ""); handled {
			return &sharedpb.DeleteClipResponse{Success: true, Message: "clip deleted via federation"}, nil
		}
		return nil, status.Error(codes.NotFound, "clip not found")
	} else if err != nil {
		return nil, status.Error(codes.Internal, "failed to check clip existence")
	}

	if currentStatus == "deleted" {
		return &sharedpb.DeleteClipResponse{
			Success: false,
			Message: "clip is already deleted",
		}, nil
	}

	// Get node_id from artifact_nodes
	var nodeID string
	_ = s.db.QueryRowContext(ctx, `
		SELECT node_id FROM foghorn.artifact_nodes
		WHERE artifact_hash = $1 AND NOT is_orphaned
		ORDER BY last_seen_at DESC LIMIT 1
	`, req.ClipHash).Scan(&nodeID)

	// DURABLE STATE FIRST: soft-delete the clip and record the DELETED lifecycle event in ONE
	// transaction (durable outbox, tenant-scoped) BEFORE removing any bytes. A byte-first delete
	// could leave a playable catalog row pointing at bytes that are already gone if the DB/outbox
	// tx then fails. Commodore enrichment is a network call, so build the event OUTSIDE the
	// transaction; the cleanup error is unknown here (cleanup runs after commit) and is reported
	// only in the RPC message. The outbox worker — not decklogClient — owns Decklog delivery.
	clipData := s.buildClipDeletedLifecycleData(ctx, req.ClipHash, nodeID, sizeBytes, retentionUntil, internalName, denormTenantID, denormUserID, "")
	transitioned := false
	if err = s.withArtifactLifecycleTx(ctx, func(tx *sql.Tx) error {
		res, execErr := tx.ExecContext(ctx, `
			UPDATE foghorn.artifacts SET status = 'deleted', updated_at = NOW()
			WHERE artifact_hash = $1 AND artifact_type = 'clip' AND tenant_id = $2
			  AND status != 'deleted'
		`, req.ClipHash, req.GetTenantId())
		if execErr != nil {
			return execErr
		}
		affected, raErr := res.RowsAffected()
		if raErr != nil {
			return raErr
		}
		if affected == 0 {
			// Already deleted (or gone) at UPDATE time — do not enqueue a second DELETED event.
			return nil
		}
		transitioned = true
		return artifactoutbox.EnqueueClipLifecycleTx(ctx, tx, clipData)
	}); err != nil {
		s.logger.WithError(err).Error("Failed to delete clip")
		return nil, status.Error(codes.Internal, "failed to delete clip")
	}

	// Lost a concurrent delete race (row already 'deleted' when the guarded UPDATE ran): report
	// already-deleted and do NO physical cleanup — the winning delete owns the bytes. Returning here
	// prevents duplicate DELETED analytics and byte removal for a delete this call did not commit.
	if !transitioned {
		return &sharedpb.DeleteClipResponse{
			Success: false,
			Message: "clip is already deleted",
		}, nil
	}

	// Terminate any not-yet-finished processing job so a deleted clip never processes (e.g. delete
	// races a freshly-queued job, or registry-write compensation). Best-effort and OUTSIDE the
	// delete transaction: the dispatcher also skips deleted artifacts, so a failure here only risks
	// a queued row lingering, not the clip re-processing.
	if _, jobErr := s.db.ExecContext(ctx, `
		UPDATE foghorn.processing_jobs
		SET status = 'failed', error_message = 'clip deleted', updated_at = NOW()
		WHERE artifact_hash = $1 AND status IN ('queued', 'dispatched', 'processing')
	`, req.ClipHash); jobErr != nil {
		s.logger.WithError(jobErr).WithField("clip_hash", req.ClipHash).Warn("Failed to cancel processing jobs for deleted clip")
	}

	// Physical cleanup runs ONLY after the soft-delete committed. Failures are non-fatal and marked
	// cleanup-pending: the catalog is already durably deleted, so any lingering bytes are storage
	// garbage the orphan/purge job reclaims, not a correctness bug.
	cleanupError := ""

	// Send delete request to Helmsman if we know the storage node
	if nodeID != "" {
		requestID := uuid.NewString()
		deleteReq := &ipcpb.ClipDeleteRequest{
			ClipHash:  req.ClipHash,
			RequestId: requestID,
		}
		if errSend := control.SendClipDelete(nodeID, deleteReq); errSend != nil {
			cleanupError = fmt.Sprintf("node cleanup pending: %v", errSend)
			// Log but don't fail - the soft delete already committed, cleanup can happen later
			s.logger.WithFields(logging.Fields{
				"clip_hash": req.ClipHash,
				"node_id":   nodeID,
				"error":     errSend,
			}).Warn("Failed to send clip delete to storage node, will be cleaned up later")
		} else {
			s.logger.WithFields(logging.Fields{
				"clip_hash":  req.ClipHash,
				"node_id":    nodeID,
				"request_id": requestID,
			}).Info("Sent clip delete request to storage node")
		}
	}

	// Delete S3 bytes (cross-cluster aware via the federation delete delegate). Failure marks
	// cleanup-pending; the row is already in the purge cycle for retries.
	if s.artifactCleaner == nil {
		if cleanupError != "" {
			cleanupError += "; "
		}
		cleanupError += "s3 cleanup pending: cleaner not wired"
		s.logger.WithField("clip_hash", req.ClipHash).Warn("Artifact cleaner not wired; clip S3 cleanup deferred to purge job")
	} else if errCleanup := s.artifactCleaner.Delete(ctx, artifacts.ArtifactRef{
		Hash:                req.ClipHash,
		Type:                "clip",
		TenantID:            req.GetTenantId(),
		StreamInternal:      internalName.String,
		Format:              format.String,
		StorageClusterID:    storageClusterID.String,
		OriginClusterID:     originClusterID.String,
		ActiveObjectKey:     activeObjectKey.String,
		ActiveDtshKey:       activeDtshKey.String,
		PendingObjectKey:    syncObjectKey.String,
		DurableBackendLocal: durableLocal.Bool,
	}); errCleanup != nil {
		if cleanupError != "" {
			cleanupError += "; "
		}
		cleanupError += fmt.Sprintf("s3 cleanup pending: %v", errCleanup)
		s.logger.WithError(errCleanup).WithField("clip_hash", req.ClipHash).Warn("Failed to delete clip from S3, will be retried by purge job")
	}

	s.logger.WithField("clip_hash", req.ClipHash).Info("Clip soft-deleted successfully")

	message := "clip deleted successfully"
	if cleanupError != "" {
		message = "clip deleted (" + cleanupError + ")"
	}
	return &sharedpb.DeleteClipResponse{
		Success: true,
		Message: message,
	}, nil
}

// DVR CONTROL SERVICE IMPLEMENTATION

// errDVRStartRaced signals that the recording UPDATE matched zero rows: a concurrent terminal
// transition (stop/fail/finalize) moved the artifact out of the startable set between the storage
// node accepting the recording and this commit. The start did not durably apply.
var errDVRStartRaced = errors.New("dvr start raced a concurrent terminal transition")

// buildDVRStartedLifecycleData builds the STATUS_STARTED lifecycle event for a DVR. Shared by the
// primary start path and the retry-reconciliation path so both carry identical identity fields. The
// tenant id must be set — the outbox rejects an empty-tenant lifecycle event.
func buildDVRStartedLifecycleData(req *sharedpb.StartDVRRequest, dvrHash, dvrCluster, streamID string) *ipcpb.DVRLifecycleData {
	data := &ipcpb.DVRLifecycleData{
		Status:           ipcpb.DVRLifecycleData_STATUS_STARTED,
		DvrHash:          dvrHash,
		OriginClusterId:  &dvrCluster,
		ServingClusterId: &dvrCluster,
		StartedAt:        func() *int64 { t := time.Now().Unix(); return &t }(),
	}
	if streamID != "" {
		data.StreamId = &streamID
	} else if req.StreamId != nil && *req.StreamId != "" {
		data.StreamId = req.StreamId
	}
	if req.TenantId != "" {
		data.TenantId = &req.TenantId
	}
	if req.InternalName != "" {
		data.StreamInternalName = &req.InternalName
	}
	if req.UserId != nil && *req.UserId != "" {
		data.UserId = req.UserId
	}
	return data
}

// reconcileStartingDVR handles a retry that finds an existing DVR row still in 'starting' (a prior
// StartDVR persisted the command attempt and (attempted to) dispatch it, but never observed the
// node's confirmation). It returns an HONEST status string, never an optimistic promotion:
//
//   - 'recording': the node's first DVRProgress already promoted the row (positive confirmation) —
//     the recording is genuinely running, so the caller returns "already_started".
//   - 'requested'/'starting': still in flight, the node has not yet confirmed — the caller returns
//     "starting". We do NOT re-send the start command inline here; the authoritative recording
//     transition is driven exclusively by the node's progress report. A crash between the
//     'starting' persist and the node's first progress ack
//     leaves the row 'starting' with no progress arriving; that honest, non-terminal state is
//     reconciled by jobs.DVRStartingRecoveryJob, which reads the persisted dvr_start_dispatch
//     descriptor and idempotently re-dispatches SendDVRStart (the sidecar treats a repeat start for
//     the same hash+stream as an idempotent ack), or finalizes the row failed if it stays
//     unrecoverable — strictly safer than a false 'recording'.
//   - anything terminal ('completed'/'completed_partial'/'failed'/'deleted'): a concurrent
//     stop/finalize won — surface FailedPrecondition rather than a false already_started/started.
//
// The returned string is the wire Status the caller reports; the bool is whether that maps to an
// active recording (true) so the caller can choose already_started vs starting.
func (s *FoghornGRPCServer) reconcileStartingDVR(ctx context.Context, req *sharedpb.StartDVRRequest, dvrHash string) (string, error) {
	var current string
	err := s.db.QueryRowContext(ctx, `
		SELECT status FROM foghorn.artifacts
		 WHERE artifact_hash = $1 AND artifact_type = 'dvr' AND tenant_id = $2
	`, dvrHash, req.TenantId).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return "", status.Error(codes.FailedPrecondition, "DVR start could not be reconciled; artifact no longer exists")
	} else if err != nil {
		s.logger.WithError(err).WithField("dvr_hash", dvrHash).Error("Failed to re-read DVR status during start reconciliation")
		return "", status.Error(codes.Internal, "failed to reconcile DVR start")
	}
	switch current {
	case "recording":
		return "already_started", nil
	case "requested", "starting":
		return "starting", nil
	default:
		return "", status.Errorf(codes.FailedPrecondition, "DVR start could not be reconciled; recording state is terminal (%s)", current)
	}
}

// StartDVR initiates DVR recording for a stream
func (s *FoghornGRPCServer) StartDVR(ctx context.Context, req *sharedpb.StartDVRRequest) (*sharedpb.StartDVRResponse, error) {
	return s.startDVR(ctx, req, "")
}

// StartDVRWithSourceHint starts DVR with the node that just accepted ingest.
// Auto-record calls this from PUSH_REWRITE before active-stream lifecycle
// state may have propagated through Redis/DB. Manual API starts intentionally
// use StartDVR so they still require observed live source state.
func (s *FoghornGRPCServer) StartDVRWithSourceHint(ctx context.Context, req *sharedpb.StartDVRRequest, sourceNodeID string) (*sharedpb.StartDVRResponse, error) {
	return s.startDVR(ctx, req, sourceNodeID)
}

func (s *FoghornGRPCServer) startDVR(ctx context.Context, req *sharedpb.StartDVRRequest, sourceNodeHint string) (resp *sharedpb.StartDVRResponse, retErr error) {
	if req.InternalName == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name is required")
	}
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	// DVR length is unknown at start time; reject only when the tenant is
	// already at cap. Active recordings finish on their own (no mid-session
	// kill). See checkStorageEntitlement docs.
	if err := s.checkStorageEntitlement(ctx, req.TenantId, 0); err != nil {
		s.emitDVRStartFailure(req, err.Error())
		return nil, err
	}

	// Resolve effective DVR live-window / segment / max-entries policy. The
	// caller (Commodore manual path; Foghorn auto-record) supplies the tier
	// policy bundle; pkg/dvrpolicy clamps it through tier_max + cluster_max
	// + platform_max. Applied only at start time — Mist split cannot change
	// mid-push, so tier upgrades affect the next session, not this one.
	//
	// Retention is intentionally NOT factored in: live window and retention
	// are independent concepts. retention_until lands on the artifact at
	// FinalizeDVR (ended_at + dvr_retention_days*24h, read from the
	// snapshot column we set below).
	effective := s.resolveEffectiveDVRConfig(req)

	// Resolve actual source node for this stream. In HA mode the Foghorn
	// receiving StartDVR may not be the Foghorn that handled the latest
	// Helmsman trigger; shared Redis state usually catches up immediately,
	// but manual DVR starts can race the stream lifecycle write. Wait
	// briefly so the command can still route through the HA control relay
	// once we know the node ID.
	sourceNodeID, baseURL, ok := waitForStreamSourceWithHint(ctx, req.InternalName, sourceNodeHint, 6*time.Second)
	if !ok {
		s.emitDVRStartFailure(req, "no source node available")
		return nil, status.Error(codes.Unavailable, "no source node available")
	}

	// Select storage node. TENANT ISOLATION: scope to this tenant so a tenant-operated storage node can never
	// receive another tenant's durable recording (the balancer applies the isolation only when the scope is set).
	sctx := context.WithValue(ctx, ctxkeys.KeyCapability, "storage")
	if req.TenantId != "" {
		sctx = context.WithValue(sctx, ctxkeys.KeyClusterScope, req.TenantId)
	}
	storageHost, _, _, _, _, err := s.lb.GetBestNodeWithScore(sctx, "", 0, 0, map[string]int{}, "", false)
	if err != nil {
		s.emitDVRStartFailure(req, fmt.Sprintf("no storage node available: %v", err))
		return nil, status.Errorf(codes.Unavailable, "no storage node available: %v", err)
	}

	storageNodeID := s.lb.GetNodeIDByHost(storageHost)
	if storageNodeID == "" {
		s.emitDVRStartFailure(req, "storage node not connected")
		return nil, status.Error(codes.Unavailable, "storage node not connected")
	}

	// Resolve the origin cluster ONCE; both the Commodore intent (RegisterDVR below)
	// and Foghorn's artifact row use this exact value so the two planes never diverge.
	dvrCluster, clusterOK := s.resolveDVROriginCluster(req, storageNodeID)
	if !clusterOK {
		s.emitDVRStartFailure(req, "unable to resolve DVR origin cluster")
		return nil, status.Error(codes.FailedPrecondition, "unable to resolve DVR origin cluster")
	}

	// Check for existing active DVR in foghorn.artifacts. A retry can land here on a row a prior
	// attempt left in 'starting' (the storage node was told to record but the durable transition was
	// never confirmed). Do NOT blindly report already_started for that: reconcile it below.
	var existingHash, existingStatus string
	_ = s.db.QueryRowContext(ctx, `
		SELECT artifact_hash, status FROM foghorn.artifacts
		WHERE stream_internal_name=$1 AND artifact_type='dvr' AND status IN ('requested','starting','recording') AND tenant_id = $2
		ORDER BY created_at DESC LIMIT 1
	`, req.InternalName, req.TenantId).Scan(&existingHash, &existingStatus)

	if existingHash != "" {
		playbackID := ""
		if control.CommodoreClient != nil {
			if resp, errResolve := control.CommodoreClient.ResolveDVRHash(ctx, existingHash); errResolve == nil && resp.Found {
				playbackID = resp.PlaybackId
			}
		}
		if existingStatus == "starting" || existingStatus == "requested" {
			// Reconcile the unconfirmed prior attempt instead of returning a false already_started. A
			// 'requested' row now also carries the durable dvr_start_dispatch descriptor (written with the
			// insert), so the recovery worker can resume it — a retry must return the HONEST in-flight state,
			// never already_started-dead. reconcileStartingDVR reports "already_started" only if the node
			// already confirmed (row is 'recording'), "starting" if still in flight ('requested'/'starting'),
			// or an error if the row went terminal. It never optimistically promotes to 'recording'.
			reconciledStatus, recErr := s.reconcileStartingDVR(ctx, req, existingHash)
			if recErr != nil {
				return nil, recErr
			}
			return &sharedpb.StartDVRResponse{
				Status:        reconciledStatus,
				DvrHash:       existingHash,
				IngestHost:    baseURL,
				StorageHost:   storageHost,
				StorageNodeId: storageNodeID,
				PlaybackId:    playbackID,
			}, nil
		}
		return &sharedpb.StartDVRResponse{
			Status:        "already_started",
			DvrHash:       existingHash,
			IngestHost:    baseURL,
			StorageHost:   storageHost,
			StorageNodeId: storageNodeID,
			PlaybackId:    playbackID,
		}, nil
	}

	// Register DVR in Commodore business registry to get hash
	var dvrHash string
	var artifactInternalName string
	var playbackID string
	var streamID string
	// intentRequestID keys Foghorn's command ledger for this DVR create attempt (distinct
	// from the tracing requestID below). See docs/architecture/creation-saga.md.
	var intentRequestID string
	// Guarantee a terminal ledger outcome for every exit past the intent mint: the defer
	// records 'rejected' only if 'accepted' ran but the artifact insert did not commit
	// (finalizeCreationCommand's own guard). Closes over intentRequestID + dvrHash.
	var ledgerProg creationLedgerProgress
	defer func() {
		retErr = s.finalizeCreationCommand(intentRequestID, req.TenantId, "dvr", dvrHash, &ledgerProg, retErr)
	}()
	if control.CommodoreClient != nil {
		regReq := &commodorepb.RegisterDVRRequest{
			TenantId:           req.TenantId,
			UserId:             req.GetUserId(),
			StreamId:           req.GetStreamId(),
			StreamInternalName: req.InternalName,
			OriginClusterId:    dvrCluster,
		}
		var regResp *commodorepb.RegisterDVRResponse
		regResp, err = control.CommodoreClient.RegisterDVR(ctx, regReq)
		if err != nil {
			s.logger.WithError(err).Error("Failed to register DVR with Commodore")
			return nil, status.Errorf(codes.Internal, "failed to register DVR: %v", err)
		}
		dvrHash = regResp.DvrHash
		artifactInternalName = regResp.GetInternalName()
		playbackID = regResp.GetPlaybackId()
		streamID = regResp.GetStreamId()
		intentRequestID = regResp.GetRequestId()
	} else {
		return nil, status.Error(codes.Unavailable, "Commodore not available")
	}

	// Record the DVR create attempt in-flight in the command ledger, keyed by the
	// intent request_id. Any pre-commit failure below terminalizes 'rejected' via the
	// deferred finalizer; the artifact insert records 'committed' in the same tx. If
	// 'accepted' cannot be written durably, FAIL the RPC before persisting anything.
	acceptState, acceptErr := s.recordCreationCommandAcceptedDurable(ctx, intentRequestID, req.TenantId, "dvr", dvrHash, &ledgerProg)
	if acceptErr != nil {
		s.logger.WithError(acceptErr).WithField("dvr_hash", dvrHash).Error("Failed to record accepted DVR creation command")
		if errors.Is(acceptErr, errCreationCommandIdentityMismatch) {
			return nil, status.Error(codes.FailedPrecondition, "request_id already used for a different artifact")
		}
		return nil, status.Errorf(codes.Unavailable, "failed to record DVR creation attempt: %v", acceptErr)
	}
	// A terminal retry of this DVR create attempt must not resume it. 'rejected' is
	// definitive; 'committed' means the DVR artifact is already durable, so report it as
	// already started rather than inserting a duplicate. (A fresh RegisterDVR normally
	// mints a new hash, so these branches only fire on a re-driven intent.)
	switch acceptState {
	case creationCommandRejected:
		return nil, status.Error(codes.FailedPrecondition, "DVR creation was terminally rejected")
	case creationCommandCommitted:
		return &sharedpb.StartDVRResponse{
			Status:        "already_started",
			DvrHash:       dvrHash,
			IngestHost:    baseURL,
			StorageHost:   storageHost,
			StorageNodeId: storageNodeID,
			PlaybackId:    playbackID,
		}, nil
	}

	// Generate request_id for tracing (distinct from artifact hash)
	requestID := uuid.New().String()

	// Store artifact lifecycle state in foghorn.artifacts. The DVR-policy
	// snapshot columns (dvr_window_seconds, dvr_chapter_mode, dvr_chapter_interval,
	// dvr_retention_days) capture the resolved policy at start time so finalize
	// months later applies the same policy even if the tenant's tier has changed
	// during the recording. retention_until is left NULL here — FinalizeDVR
	// computes it as ended_at + dvr_retention_days*24h (post-end semantics).
	// Stream config is authoritative: empty/NULL mode means chapters
	// are off, regardless of cluster defaults. The sweeper already
	// filters on dvr_chapter_mode IS NOT NULL AND != '', so leaving
	// this empty fully disables chapter rotation for the recording.
	chapterMode := req.GetDvrChapterMode()
	chapterInterval := req.GetDvrChapterIntervalSeconds()
	retentionDays := dvrRetentionDays(req.GetDvrPolicy())
	// Persist Commodore's resolved DVR process snapshot so the
	// rolling-DVR surface (dvr+<internal>) keeps serving lifecycle-specific
	// thumbnails/sprites across Foghorn restarts and process-cache TTL expiry.
	// Same snapshot pattern as dvr_window_seconds / dvr_chapter_mode.
	dvrProcessesJSON := req.GetProcessesJson()

	// Resolve the source runtime name + DTSC input and build the durable dvr_start_dispatch descriptor
	// (target node + every field to rebuild the DVRStartRequest, state='pending') BEFORE inserting the
	// row, so the descriptor is written ATOMICALLY with the 'requested' insert (same statement). A crash
	// after the row exists then ALWAYS leaves a descriptor the stale-'requested'/'starting' recovery
	// worker can find and resume — there is no window where a 'requested' row exists without one. These
	// resolutions can fail; because nothing is persisted yet, a failure just expires the Commodore
	// registry row we minted and returns, with no artifact row to finalize.
	sourceStreamName := dvrSourceStreamName(req.InternalName)
	fullDTSC := control.BuildDTSCURI(sourceNodeID, sourceStreamName, s.logger)
	if fullDTSC == "" {
		// No artifact row was written; the deferred finalizer records 'rejected' and
		// the Commodore sweep removes the catalog-only dvr_recordings row on abort.
		s.emitDVRStartFailure(req, "DTSC output not available on source node")
		return nil, status.Error(codes.Unavailable, "DTSC output not available on source node")
	}
	sourceBaseURL := fullDTSC
	if storageNodeID == sourceNodeID {
		sourceBaseURL = ""
	}
	dispatchJSON, dispatchErr := json.Marshal(jobs.DVRStartDispatch{
		State:             jobs.DVRDispatchStatePending,
		NodeID:            storageNodeID,
		NodeBaseURL:       storageHost,
		SourceRuntimeName: sourceStreamName,
		SourceBaseURL:     sourceBaseURL,
		SegmentSeconds:    int32(effective.SegmentDurationSeconds),
		WindowSeconds:     int32(effective.DVRWindowSeconds),
		MaxEntries:        int32(effective.MaxEntries),
		StreamID:          streamID,
		InternalName:      req.InternalName,
		DispatchedAt:      time.Now().Unix(),
	})
	if dispatchErr != nil {
		// Nothing persisted; the deferred finalizer records 'rejected' and the
		// Commodore sweep removes the catalog-only dvr_recordings row on abort.
		s.logger.WithError(dispatchErr).WithField("dvr_hash", dvrHash).Error("Failed to encode DVR start dispatch descriptor; not persisting DVR row")
		return nil, status.Error(codes.Internal, "failed to encode DVR start command attempt")
	}

	// Insert the DVR artifact row AND record the 'committed' ledger outcome in ONE
	// transaction (same shape as CreateClip/CreateVodUpload). A separate committed
	// write could otherwise fail after a durable DVR insert and leave a live recording
	// with a forever-'accepted' intent; committing them together removes that window.
	// On failure the deferred finalizer records 'rejected' and the Commodore sweep
	// removes the catalog-only dvr_recordings row on abort — no best-effort cleanup here.
	if txErr := s.withArtifactLifecycleTx(ctx, func(tx *sql.Tx) error {
		if _, execErr := tx.ExecContext(ctx, `
			INSERT INTO foghorn.artifacts (
				artifact_hash, artifact_type, stream_internal_name, internal_name,
				stream_id, tenant_id, user_id,
				status, request_id, format, origin_cluster_id,
				dvr_window_seconds, dvr_chapter_mode, dvr_chapter_interval, dvr_retention_days, dvr_processes_json,
				dvr_start_dispatch,
				created_at, updated_at
			)
			VALUES ($1, 'dvr', $2, $3, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, NULLIF($6, '')::uuid,
			        'requested', $7, 'm3u8', $8, $9, NULLIF($10, '')::text, NULLIF($11, 0)::int, NULLIF($12, 0)::int, NULLIF($13, '')::text,
			        $14::jsonb,
			        NOW(), NOW())
		`,
			dvrHash, req.InternalName, artifactInternalName, streamID, req.TenantId, req.GetUserId(), requestID, dvrCluster,
			effective.DVRWindowSeconds, chapterMode, chapterInterval, retentionDays, dvrProcessesJSON,
			string(dispatchJSON),
		); execErr != nil {
			return execErr
		}
		// Composed into the SAME tx so the 'committed' ledger row (with the artifact's
		// catalog_revision, read from the row just inserted above) is durable together
		// with the DVR artifact.
		return recordCreationCommandCommitted(ctx, tx, intentRequestID, req.TenantId, "dvr", dvrHash)
	}); txErr != nil {
		s.logger.WithFields(logging.Fields{
			"dvr_hash":      dvrHash,
			"internal_name": req.InternalName,
			"error":         txErr,
		}).Error("Failed to store DVR artifact in database")
		return nil, status.Error(codes.Internal, "failed to store DVR artifact")
	}
	// The DVR artifact row and its 'committed' ledger row committed together; the
	// deferred finalizer must not now record a contradictory 'rejected'.
	ledgerProg.committed = true

	if s.consumePendingDVRStop(req.InternalName) {
		final, finalErr := control.FinalizeDVR(ctx, dvrHash, control.FinalizeOptions{
			ReportedStatus: "failed",
			ReportedError:  "stream ended before DVR start",
			StorageNodeID:  storageNodeID,
		})
		if finalErr != nil && final.ArtifactStatus == "" {
			s.logger.WithError(finalErr).WithField("dvr_hash", dvrHash).Error("Failed to finalize DVR after pending stream stop")
			return nil, status.Error(codes.Internal, "failed to finalize stopped DVR")
		}
		if finalErr != nil {
			s.logger.WithError(finalErr).WithFields(logging.Fields{
				"dvr_hash":     dvrHash,
				"final_status": final.ArtifactStatus,
			}).Warn("Pending-stop DVR finalized with follow-up error")
		}
		responseStatus := final.ArtifactStatus
		if responseStatus == "" {
			responseStatus = "failed"
		}
		// control.FinalizeDVR (called above) enqueues the terminal STOPPED lifecycle event INSIDE its
		// own finalize transaction (see dvr_finalize.go), so the state transition and its event commit
		// atomically. A second independent enqueue here would produce a DUPLICATE STOPPED event, so we
		// deliberately do not emit one on this pending-stop path.
		return &sharedpb.StartDVRResponse{
			Status:        responseStatus,
			DvrHash:       dvrHash,
			IngestHost:    baseURL,
			StorageHost:   storageHost,
			StorageNodeId: storageNodeID,
			PlaybackId:    playbackID,
		}, nil
	}

	// Store node assignment in foghorn.artifact_nodes. The recording
	// node is the origin (writes segments as they land); is_complete
	// stays false on this parent DVR row — it's a multi-file recording,
	// not a single relayable artifact. Completeness is registered per
	// chapter by the DVR chapter finalize, each under its own VOD hash.
	// Route through the transactional origin-registration path so a cache→origin
	// promotion emits UPDATED atomically (a raw upsert + presence-only refresh would
	// miss the role change).
	if err = control.RegisterDVRRecordingOrigin(ctx, dvrHash, storageNodeID, storageHost); err != nil {
		// The recording-origin row (foghorn.artifact_nodes) is what routes rolling-DVR playback and
		// targets the stop command; a DVR with no origin can be neither served nor cleanly stopped.
		// Fail the start HONESTLY rather than proceeding to command a recording with no durable origin.
		// Nothing was dispatched to the node yet, so finalizing the just-created 'requested' row failed
		// is a complete rollback. The stale-'starting' recovery worker re-runs this same registration on
		// re-dispatch, so a crash AFTER a successful registration is still reconciled.
		s.logger.WithError(err).WithField("dvr_hash", dvrHash).Error("Failed to store DVR recording-origin assignment; failing start")
		if final, finalErr := control.FinalizeDVR(ctx, dvrHash, control.FinalizeOptions{
			ReportedStatus: "failed",
			ReportedError:  fmt.Sprintf("recording-origin registration failed: %v", err),
			StorageNodeID:  storageNodeID,
		}); finalErr != nil && final.ArtifactStatus == "" {
			s.logger.WithError(finalErr).WithField("dvr_hash", dvrHash).Error("Failed to finalize DVR after recording-origin registration failure")
		}
		return nil, status.Error(codes.Internal, "failed to store DVR recording-origin assignment")
	}

	// DVR configuration. Effective live-window / segment / max_entries are
	// resolved by pkg/dvrpolicy above. The sidecar applies these values
	// verbatim and never interprets tier or cluster context.
	config := &ipcpb.DVRConfig{
		Enabled:          true,
		Format:           "ts",
		SegmentDuration:  int32(effective.SegmentDurationSeconds),
		DvrWindowSeconds: int32(effective.DVRWindowSeconds),
		MaxEntries:       int32(effective.MaxEntries),
		// The sidecar runs until Mist accepts a stop; FinalizeDVR computes
		// retention_until after the stream session ends.
		RetentionUntil: 0,
	}

	// Transition 'requested'->'starting' (guarded, tenant-scoped) and CHECK it committed (RowsAffected)
	// BEFORE telling the node to record. The dvr_start_dispatch descriptor is already durable from the
	// insert above; this UPDATE only advances the state, re-asserting the same descriptor so the "row is
	// 'starting' only with a descriptor present" invariant holds explicitly. If the update errors OR
	// matches zero rows (a concurrent stop/finalize already moved the row terminal), we MUST NOT send the
	// external start command: launching a recording with no durable backing is exactly the inconsistency
	// this ordering prevents. Return an error; nothing was sent, so there is nothing to compensate.
	startingRes, startingErr := s.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		   SET status = 'starting', dvr_start_dispatch = $3::jsonb, updated_at = NOW()
		 WHERE artifact_hash = $1 AND artifact_type = 'dvr' AND tenant_id = $2 AND status = 'requested'
	`, dvrHash, req.TenantId, string(dispatchJSON))
	if startingErr != nil {
		s.logger.WithError(startingErr).WithFields(logging.Fields{
			"dvr_hash":  dvrHash,
			"tenant_id": req.TenantId,
		}).Error("Failed to persist DVR 'starting' command attempt; not sending start command")
		return nil, status.Error(codes.Internal, "failed to persist DVR start command attempt")
	}
	startingRows, startingRAErr := startingRes.RowsAffected()
	if startingRAErr != nil {
		s.logger.WithError(startingRAErr).WithField("dvr_hash", dvrHash).Error("Failed to read DVR 'starting' RowsAffected; not sending start command")
		return nil, status.Error(codes.Internal, "failed to persist DVR start command attempt")
	}
	if startingRows == 0 {
		// The row left 'requested' before we could claim it (concurrent stop/finalize). Do not send.
		s.logger.WithFields(logging.Fields{
			"dvr_hash":  dvrHash,
			"tenant_id": req.TenantId,
		}).Error("DVR row left 'requested' before start-command persist; not sending start command")
		return nil, status.Error(codes.Aborted, "DVR start raced a concurrent stop before command dispatch")
	}

	// Send gRPC control message to storage Helmsman. source_runtime_name
	// is Foghorn-authoritative: Helmsman uses it verbatim as the Mist
	// push_start.stream arg, no silent live+ default for mist_native.
	dvrReq := &ipcpb.DVRStartRequest{
		DvrHash:           dvrHash,
		InternalName:      req.InternalName,
		SourceRuntimeName: sourceStreamName,
		SourceBaseUrl:     sourceBaseURL,
		RequestId:         dvrHash,
		Config:            config,
		StreamId:          streamID,
	}

	if err := control.SendDVRStart(storageNodeID, dvrReq); err != nil {
		if classifyDVRSendError(err) != dvrSendDefinitiveReject {
			// AMBIGUOUS send (transport/timeout/Unavailable/not-connected): the node MAY have accepted the
			// start before the error surfaced. Terminalizing here would mark a possibly-running recording
			// 'failed' and drop it out of the recovery scan. Instead leave the row 'starting' with its
			// durable dvr_start_dispatch descriptor (state 'pending') so DVRStartingRecoveryJob idempotently
			// re-dispatches — and, past the hard grace with no node progress, stops+finalizes. Return an
			// error so the caller may also retry (the sidecar treats a repeat start for the same hash+stream
			// as an ack), but do NOT finalize.
			s.logger.WithError(err).WithFields(logging.Fields{
				"dvr_hash": dvrHash,
				"node_id":  storageNodeID,
			}).Error("Ambiguous DVR start dispatch; leaving 'starting' for recovery reconciliation (not finalizing)")
			return nil, status.Error(codes.Unavailable, "DVR start dispatch ambiguous; left for recovery reconciliation")
		}
		// DEFINITIVE rejection: the node rejected the command outright and is NOT recording. Terminalize now.
		final, finalErr := control.FinalizeDVR(ctx, dvrHash, control.FinalizeOptions{
			ReportedStatus: "failed",
			ReportedError:  fmt.Sprintf("storage node rejected DVR start: %v", err),
			StorageNodeID:  storageNodeID,
		})
		if finalErr != nil && final.ArtifactStatus == "" {
			s.logger.WithError(finalErr).WithField("dvr_hash", dvrHash).Error("Failed to finalize DVR after storage start rejection")
		}
		// control.FinalizeDVR above owns and commits the terminal FAILED state AND its FAILED lifecycle
		// event atomically (setArtifactFailed enqueues it in-tx). We do NOT enqueue a second FAILED event
		// here — that produced duplicate terminal history.

		s.logger.WithFields(logging.Fields{
			"dvr_hash": dvrHash,
			"node_id":  storageNodeID,
			"error":    err,
		}).Error("Storage node definitively rejected DVR start request")
		return nil, status.Error(codes.Internal, "failed to start DVR on storage node")
	}
	// The node's first progress report is the authoritative "recording" ack. The storage sidecar
	// (api_sidecar DVRManager) sets its job Status to "recording" once Mist is actually writing
	// segments and emits a DVRProgress control message; Foghorn's processDVRProgress -> ApplyDVRProgress
	// -> UpdateDVRProgressByHash promotes the artifact 'starting'->'recording' (guarded on
	// status IN ('requested','starting','recording')). That first progress event — NOT this RPC — is the
	// authoritative "recording" signal. So StartDVR must NOT optimistically claim 'recording': after the
	// node ACCEPTS the start command we leave the row 'starting' and let the node's confirmation drive
	// the recording transition. We report Status "starting" (honest "start requested"), never a
	// "started"/"recording"-as-confirmed with no evidence.
	//
	// We still commit the STARTED lifecycle event here (start command was durably persisted and
	// dispatched) in ONE transaction so the event can't be lost. The tx does NOT change status; it only
	// guards that the row is still in a startable/recording state (a concurrent stop/finalize may have
	// moved it terminal after SendDVRStart) so no STARTED event is minted for a dead artifact. A tx
	// failure is NOT fatal and drives NO compensating stop: the node accepted the recording and the row
	// is already durably 'starting' with its dvr_start_dispatch descriptor, so a lost STARTED event
	// leaves node progress and DVRStartingRecoveryJob to reconcile the row.
	dvrData := buildDVRStartedLifecycleData(req, dvrHash, dvrCluster, streamID)
	if txErr := s.withArtifactLifecycleTx(ctx, func(tx *sql.Tx) error {
		// Guard only — the row stays 'starting' (or 'recording' if the node already confirmed between
		// our persist above and here). A terminal status means a concurrent stop/finalize won; abort so
		// we emit no STARTED event for an artifact that is no longer starting.
		res, execErr := tx.ExecContext(ctx, `
			UPDATE foghorn.artifacts
			   SET updated_at = NOW()
			 WHERE artifact_hash = $1 AND artifact_type = 'dvr' AND tenant_id = $2
			   AND status IN ('starting', 'recording')
		`, dvrHash, req.TenantId)
		if execErr != nil {
			return execErr
		}
		affected, raErr := res.RowsAffected()
		if raErr != nil {
			return raErr
		}
		if affected == 0 {
			return errDVRStartRaced
		}
		return artifactoutbox.EnqueueDVRLifecycleTx(ctx, tx, dvrData)
	}); txErr != nil {
		// The node ACCEPTED the start and the row is durably 'starting' with its dvr_start_dispatch
		// descriptor (persisted before send). The only work this tx does is emit the STARTED lifecycle
		// event, so its failure does NOT mean the recording failed — we neither compensate-stop nor mark
		// the row failed here. Two cases:
		//   - errDVRStartRaced (guard matched 0 rows): a concurrent stop/finalize already moved the row
		//     terminal and owns tearing the node down (every concurrent terminalizer issues its own stop).
		//     Emit no STARTED event for a dead artifact; report the honest superseded result.
		//   - any other error (outbox/DB): the row stays 'starting'/'pending'. The node's first progress
		//     promotes it to 'recording', or the DVRStartingRecoveryJob deadline path stops+finalizes.
		//     Warn and fall through to return the honest 'starting' status rather than a fatal error that
		//     would imply the recording never started.
		if errors.Is(txErr, errDVRStartRaced) {
			s.logger.WithError(txErr).WithField("dvr_hash", dvrHash).Warn("DVR started on storage node but the row was concurrently moved terminal; STARTED event not emitted, concurrent stop/finalize owns teardown")
			return nil, status.Error(codes.Aborted, "DVR start superseded by a concurrent stop")
		}
		s.logger.WithError(txErr).WithField("dvr_hash", dvrHash).Warn("DVR started on storage node but STARTED lifecycle event failed to commit; row left 'starting'/'pending' for progress + recovery reconciliation")
	}

	// Reflect the honest in-flight status. The row is 'starting'; the node's first DVRProgress promotes
	// it to 'recording' (which ApplyDVRProgress also mirrors onto this stream-instance dvr_status).
	state.DefaultManager().UpdateStreamInstanceInfo(req.InternalName, storageNodeID, map[string]any{
		"dvr_status": "starting",
		"dvr_hash":   dvrHash,
	})

	return &sharedpb.StartDVRResponse{
		Status:        "starting",
		DvrHash:       dvrHash,
		IngestHost:    baseURL,
		StorageHost:   storageHost,
		StorageNodeId: storageNodeID,
		PlaybackId:    playbackID,
	}, nil
}

// dvrSourceStreamName returns the Mist runtime name of the source stream
// for a DVR push. Prefers the registry's RuntimeName (IngestMode-aware:
// live+ for push, pull+ for pull, bare for mist_native). Falls back to
// the observed Mist instance name and finally to the bare internal name.
// Never defaults to live+ when the mode is unknown — that would mis-route
// DVR for mist_native sources.
func dvrSourceStreamName(internalName string) string {
	internalName = strings.TrimSpace(internalName)
	if internalName == "" {
		return ""
	}
	// Registry is authoritative when IngestMode is known: returns the
	// canonical runtime name (bare for mist-native, live+/pull+ for
	// push/pull). Bare-matches-internal isn't a "no entry" signal —
	// check IngestMode directly so mist-native sources don't fall
	// through to the observed-state path that only recognises `+`-bearing
	// names.
	if control.StreamRegistryInstance != nil {
		entry, err := control.StreamRegistryInstance.ResolveSourceByInternalName(context.Background(), internalName)
		if err == nil && entry.IngestMode != 0 {
			return control.RuntimeNameFor(entry.IngestMode, entry.InternalName)
		}
	}
	// Cold-cache fallback: trust the observed Mist instance name when it
	// carries a prefix. Bare observed names fall through to bare
	// internal_name — never to a silent live+ default.
	if ss := state.DefaultManager().GetStreamState(internalName); ss != nil {
		observed := strings.TrimSpace(ss.StreamName)
		if observed != "" && strings.Contains(observed, "+") {
			return control.MistSourceNameFromObservedStream(observed)
		}
	}
	return internalName
}

func clipLiveSourceStreamName(ctx context.Context, req *sharedpb.CreateClipRequest) (string, error) {
	internalName := strings.TrimSpace(req.GetStreamInternalName())
	if internalName == "" {
		return "", fmt.Errorf("stream_internal_name is required")
	}
	if control.StreamRegistryInstance != nil {
		entry, err := control.StreamRegistryInstance.ResolveSourceByInternalName(ctx, internalName)
		if err == nil && entry.IngestMode != 0 {
			return control.RuntimeNameFor(entry.IngestMode, entry.InternalName), nil
		}
	}
	if control.CommodoreClient == nil {
		return "", fmt.Errorf("commodore client unavailable")
	}
	resp, err := control.CommodoreClient.ResolveStreamContext(ctx, "", "", internalName, req.GetClusterId())
	if err != nil {
		return "", err
	}
	if !resp.GetAdmitted() || resp.GetInternalName() == "" {
		return "", fmt.Errorf("stream context rejected: %s", resp.GetAdmissionReason())
	}
	ingestMode, modeErr := control.IngestModeFromWire(resp.GetIngestMode())
	if modeErr != nil {
		return "", fmt.Errorf("ingest_mode %q: %w", resp.GetIngestMode(), modeErr)
	}
	if control.StreamRegistryInstance != nil {
		control.StreamRegistryInstance.UpsertLocalSource(control.StreamEntry{
			StreamID:        resp.GetStreamId(),
			TenantID:        resp.GetTenantId(),
			PlaybackID:      resp.GetPlaybackId(),
			InternalName:    resp.GetInternalName(),
			IngestMode:      ingestMode,
			OriginClusterID: resp.GetOriginClusterId(),
		})
	}
	return control.RuntimeNameFor(ingestMode, resp.GetInternalName()), nil
}

// StopDVR stops an active DVR recording
func (s *FoghornGRPCServer) StopDVR(ctx context.Context, req *sharedpb.StopDVRRequest) (*sharedpb.StopDVRResponse, error) {
	if req.DvrHash == "" {
		return nil, status.Error(codes.InvalidArgument, "dvr_hash is required")
	}
	// NOTE: tenant_id validation now happens at Commodore level

	// Get DVR artifact info
	var (
		dvrStatus      string
		internalName   string
		sizeBytes      sql.NullInt64
		retentionUntil sql.NullTime
		startedAt      sql.NullTime
		endedAt        sql.NullTime
		denormTenantID sql.NullString
		denormUserID   sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT status, COALESCE(stream_internal_name, ''), size_bytes, retention_until, started_at, ended_at, tenant_id, user_id
		FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND artifact_type = 'dvr' AND tenant_id = $2
	`, req.DvrHash, req.GetTenantId()).Scan(&dvrStatus, &internalName, &sizeBytes, &retentionUntil, &startedAt, &endedAt, &denormTenantID, &denormUserID)

	if errors.Is(err, sql.ErrNoRows) {
		streamID := ""
		if req.StreamId != nil {
			streamID = *req.StreamId
		}
		if handled, _ := s.forwardArtifactToFederation(ctx, "stop_dvr", req.DvrHash, req.GetTenantId(), streamID); handled {
			return &sharedpb.StopDVRResponse{Success: true, Message: "DVR stopped via federation"}, nil
		}
		return nil, status.Error(codes.NotFound, "DVR recording not found")
	} else if err != nil {
		s.logger.WithError(err).Error("Failed to fetch DVR artifact")
		return nil, status.Error(codes.Internal, "failed to fetch DVR artifact")
	}

	// Get node_id from artifact_nodes. Finalization retry can still run without
	// one, but an active stop needs a storage node to receive the Mist stop.
	var (
		nodeID        string
		nodeSizeBytes sql.NullInt64
	)
	if nodeErr := s.db.QueryRowContext(ctx, `
		SELECT node_id, size_bytes FROM foghorn.artifact_nodes
		WHERE artifact_hash = $1 AND NOT is_orphaned
		ORDER BY last_seen_at DESC LIMIT 1
	`, req.DvrHash).Scan(&nodeID, &nodeSizeBytes); nodeErr != nil && !errors.Is(nodeErr, sql.ErrNoRows) {
		s.logger.WithError(nodeErr).WithField("dvr_hash", req.DvrHash).Warn("Failed to look up storage node for DVR stop")
	}

	switch dvrStatus {
	case "finalizing":
		retrySizeBytes := uint64(0)
		if sizeBytes.Valid && sizeBytes.Int64 > 0 {
			retrySizeBytes = uint64(sizeBytes.Int64)
		} else if nodeSizeBytes.Valid && nodeSizeBytes.Int64 > 0 {
			retrySizeBytes = uint64(nodeSizeBytes.Int64)
		}
		retryDurationSeconds := int64(0)
		if startedAt.Valid && endedAt.Valid && endedAt.Time.After(startedAt.Time) {
			retryDurationSeconds = int64(endedAt.Time.Sub(startedAt.Time).Seconds())
		}
		go func() {
			retryCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			final, finalErr := control.FinalizeDVR(retryCtx, req.DvrHash, control.FinalizeOptions{
				ReportedStatus:  dvrStatus,
				DurationSeconds: retryDurationSeconds,
				SizeBytes:       retrySizeBytes,
				StorageNodeID:   nodeID,
			})
			if finalErr != nil {
				s.logger.WithError(finalErr).WithFields(logging.Fields{
					"dvr_hash":     req.DvrHash,
					"final_status": final.ArtifactStatus,
				}).Warn("Stale DVR finalization retry failed")
			}
			if final.ArtifactStatus != "" && !final.NoOp {
				if applyErr := state.DefaultManager().ApplyDVRStopped(context.Background(), req.DvrHash, final.ArtifactStatus, retryDurationSeconds, retrySizeBytes, final.ManifestPath, "", nodeID); applyErr != nil {
					s.logger.WithError(applyErr).WithField("dvr_hash", req.DvrHash).Warn("ApplyDVRStopped after stale finalization retry failed")
				}
			}
		}()
		return &sharedpb.StopDVRResponse{
			Success: true,
			Message: "DVR finalization retry scheduled",
		}, nil
	case "completed", "completed_partial", "failed", "ready", "deleted":
		return &sharedpb.StopDVRResponse{
			Success: false,
			Message: fmt.Sprintf("DVR recording already finished with status: %s", dvrStatus),
		}, nil
	}

	if nodeID == "" {
		return nil, status.Error(codes.Unavailable, "no storage node available for this DVR")
	}

	// Send stop command to storage Helmsman
	stopReq := &ipcpb.DVRStopRequest{
		DvrHash:   req.DvrHash,
		RequestId: req.DvrHash,
	}

	if errStop := control.SendDVRStop(nodeID, stopReq); errStop != nil {
		return nil, status.Errorf(codes.Unavailable, "storage node unavailable: %v", errStop)
	}

	// Update status in foghorn.artifacts
	_, err = s.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts SET status = 'stopping', updated_at = NOW()
		WHERE artifact_hash = $1 AND artifact_type = 'dvr' AND tenant_id = $2
	`, req.DvrHash, req.GetTenantId())
	if err != nil {
		s.logger.WithError(err).Error("Failed to update DVR status to stopping")
	}

	return &sharedpb.StopDVRResponse{
		Success: true,
		Message: "DVR recording stopping",
	}, nil
}

// withArtifactLifecycleTx runs fn inside a single transaction and commits it, so a durable state
// transition and its lifecycle outbox row commit atomically (or not at all). The outbox drain
// worker — not the producer — owns Decklog delivery, so this must never gate on decklogClient.
func (s *FoghornGRPCServer) withArtifactLifecycleTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // best-effort rollback of an uncommitted tx
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteDVR deletes a DVR recording and its files
func (s *FoghornGRPCServer) DeleteDVR(ctx context.Context, req *sharedpb.DeleteDVRRequest) (*sharedpb.DeleteDVRResponse, error) {
	if req.DvrHash == "" {
		return nil, status.Error(codes.InvalidArgument, "dvr_hash is required")
	}
	// NOTE: tenant_id validation now happens at Commodore level

	// Get DVR artifact info
	var (
		dvrStatus        string
		internalName     string
		sizeBytes        sql.NullInt64
		retentionUntil   sql.NullTime
		startedAt        sql.NullTime
		endedAt          sql.NullTime
		denormTenantID   sql.NullString
		denormUserID     sql.NullString
		storageClusterID sql.NullString
		originClusterID  sql.NullString
		activeObjectKey  sql.NullString
		activeDtshKey    sql.NullString
		syncObjectKey    sql.NullString
		durableLocal     sql.NullBool
	)

	err := s.db.QueryRowContext(ctx, `
		SELECT status, COALESCE(stream_internal_name, ''), size_bytes, retention_until, started_at, ended_at, tenant_id, user_id,
		       storage_cluster_id, origin_cluster_id, active_object_key, active_dtsh_key, sync_object_key, durable_backend_local
		FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND artifact_type = 'dvr' AND tenant_id = $2
	`, req.DvrHash, req.GetTenantId()).Scan(&dvrStatus, &internalName, &sizeBytes, &retentionUntil, &startedAt, &endedAt, &denormTenantID, &denormUserID, &storageClusterID, &originClusterID, &activeObjectKey, &activeDtshKey, &syncObjectKey, &durableLocal)

	if errors.Is(err, sql.ErrNoRows) {
		if handled, _ := s.forwardArtifactToFederation(ctx, "delete_dvr", req.DvrHash, req.GetTenantId(), ""); handled {
			return &sharedpb.DeleteDVRResponse{Success: true, Message: "DVR deleted via federation"}, nil
		}
		return nil, status.Error(codes.NotFound, "DVR recording not found")
	} else if err != nil {
		s.logger.WithError(err).Error("Failed to fetch DVR artifact")
		return nil, status.Error(codes.Internal, "failed to fetch DVR artifact")
	}

	// Get node_id from artifact_nodes
	var nodeID string
	_ = s.db.QueryRowContext(ctx, `
		SELECT node_id FROM foghorn.artifact_nodes
		WHERE artifact_hash = $1 AND NOT is_orphaned
		ORDER BY last_seen_at DESC LIMIT 1
	`, req.DvrHash).Scan(&nodeID)

	// If still recording, stop it first
	if dvrStatus == "recording" || dvrStatus == "requested" || dvrStatus == "starting" {
		if nodeID != "" {
			stopReq := &ipcpb.DVRStopRequest{
				DvrHash:   req.DvrHash,
				RequestId: req.DvrHash,
			}
			if errStop := control.SendDVRStop(nodeID, stopReq); errStop != nil {
				s.logger.WithFields(logging.Fields{
					"dvr_hash": req.DvrHash,
					"node_id":  nodeID,
					"error":    errStop,
				}).Warn("Failed to send DVR stop before delete")
			}
		}
	}

	// DURABLE STATE FIRST: soft-delete the parent DVR, cascade its child chapter artifacts, and
	// remove the chapter rows — all in ONE transaction — BEFORE deleting any bytes. A byte-first
	// delete could leave an active catalog row whose bytes are already gone (on a subsequent DB
	// failure), or leave active children the purge job can never discover. Because parent+children
	// are marked deleted here, the standard orphan/purge flow reclaims ALL their bytes.
	childHashes, parentTransitioned, delErr := control.SoftDeleteDVRAndChapters(ctx, req.DvrHash, req.GetTenantId())
	if delErr != nil {
		s.logger.WithError(delErr).WithField("dvr_hash", req.DvrHash).Error("Failed to soft-delete DVR recording")
		return nil, status.Error(codes.Internal, "failed to delete DVR recording")
	}

	// Already-deleted parent (idempotent re-delete, or the losing side of a concurrent delete): the
	// cascade above still repaired any children that were never soft-deleted, so report
	// their hashes for the caller's catalog cascade — but return Success=false so the Gateway does
	// NOT emit a duplicate API deletion event. No physical cleanup: the parent's bytes were already
	// reclaimed (or are pending) from the original delete.
	if !parentTransitioned {
		return &sharedpb.DeleteDVRResponse{
			Success:              false,
			Message:              "DVR recording is already deleted",
			DeletedChapterHashes: childHashes,
		}, nil
	}
	s.logger.WithFields(logging.Fields{"dvr_hash": req.DvrHash, "chapters": len(childHashes)}).Info("DVR recording + chapters soft-deleted (durable state committed)")

	cleanupError := ""

	// Now physical cleanup (retryable; failure only marks cleanup-pending — the durable delete
	// already committed, and the orphan/purge job reclaims any bytes left behind).
	if nodeID != "" {
		requestID := uuid.NewString()
		deleteReq := &ipcpb.DVRDeleteRequest{
			DvrHash:   req.DvrHash,
			RequestId: requestID,
		}
		if errDelete := control.SendDVRDelete(nodeID, deleteReq); errDelete != nil {
			cleanupError = fmt.Sprintf("node cleanup pending: %v", errDelete)
			s.logger.WithFields(logging.Fields{
				"dvr_hash": req.DvrHash,
				"node_id":  nodeID,
				"error":    errDelete,
			}).Warn("Failed to send DVR delete to storage node, will be cleaned up later")
		} else {
			s.logger.WithFields(logging.Fields{
				"dvr_hash":   req.DvrHash,
				"node_id":    nodeID,
				"request_id": requestID,
			}).Info("Sent DVR delete request to storage node")
		}
	}

	// Delete S3 bytes (cross-cluster aware). Failure marks cleanup-pending.
	if s.artifactCleaner == nil {
		if cleanupError != "" {
			cleanupError += "; "
		}
		cleanupError += "s3 cleanup pending: cleaner not wired"
		s.logger.WithField("dvr_hash", req.DvrHash).Warn("Artifact cleaner not wired; DVR S3 cleanup deferred to purge job")
	} else if errCleanup := s.artifactCleaner.Delete(ctx, artifacts.ArtifactRef{
		Hash:                req.DvrHash,
		Type:                "dvr",
		TenantID:            req.GetTenantId(),
		StreamInternal:      internalName,
		StorageClusterID:    storageClusterID.String,
		OriginClusterID:     originClusterID.String,
		ActiveObjectKey:     activeObjectKey.String,
		ActiveDtshKey:       activeDtshKey.String,
		PendingObjectKey:    syncObjectKey.String,
		DurableBackendLocal: durableLocal.Bool,
	}); errCleanup != nil {
		if cleanupError != "" {
			cleanupError += "; "
		}
		cleanupError += fmt.Sprintf("s3 cleanup pending: %v", errCleanup)
		s.logger.WithError(errCleanup).WithField("dvr_hash", req.DvrHash).Warn("Failed to delete DVR from S3, will be retried by purge job")
	}

	// The DVR-deleted (and per-chapter VOD-deleted) lifecycle events were already enqueued
	// DURABLY inside SoftDeleteDVRAndChapters' transaction — no separate loss-prone emit here.

	message := "DVR recording deleted successfully"
	if cleanupError != "" {
		message = "DVR recording deleted (" + cleanupError + ")"
	}
	return &sharedpb.DeleteDVRResponse{
		Success:              true,
		Message:              message,
		DeletedChapterHashes: childHashes,
	}, nil
}

// VIEWER CONTROL SERVICE IMPLEMENTATION

// ResolveViewerEndpoint resolves the best endpoint(s) for a viewer
func (s *FoghornGRPCServer) ResolveViewerEndpoint(ctx context.Context, req *sharedpb.ViewerEndpointRequest) (*sharedpb.ViewerEndpointResponse, error) {
	if req.ContentId == "" {
		return nil, status.Error(codes.InvalidArgument, "content_id is required")
	}

	// Always resolve content type from the public ID (do not trust caller-provided type)
	resolution, err := control.ResolveContent(ctx, req.ContentId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "failed to resolve content: %v", err)
	}
	resolvedType := resolution.ContentType
	s.logger.WithFields(logging.Fields{
		"content_id":   req.ContentId,
		"content_type": resolvedType,
	}).Info("Resolved content type from ID")

	resourcePath := "viewer://" + req.ContentId
	x402Paid := x402PaidFromMetadata(ctx)
	paymentHeader := x402.GetPaymentHeaderFromContext(ctx)
	clientIP := req.GetViewerIp()

	if !x402Paid && paymentHeader != "" && s.purserClient != nil && resolution.TenantId != "" {
		paid, errPay := s.handleX402ViewerPayment(ctx, resolution.TenantId, resourcePath, paymentHeader, clientIP)
		if errPay != nil {
			return nil, errPay
		}
		x402Paid = paid
	}

	// Check billing status for the content owner
	if s.cacheInvalidator != nil && resolution.TenantId != "" {
		billingTarget := control.ResolvePlaybackPolicyTarget(ctx, resolution.ContentId, resolution.InternalName)
		billingInternalName := billingTarget.InternalName
		billing := s.cacheInvalidator.GetBillingStatus(ctx, billingInternalName, resolution.TenantId)
		if billing != nil {
			// Hard block: tenant suspended (balance < -$10)
			if billing.IsSuspended && !x402Paid {
				s.logger.WithFields(logging.Fields{
					"content_id": req.ContentId,
					"tenant_id":  resolution.TenantId,
				}).Warn("Rejecting viewer: content owner suspended")
				return nil, s.paymentRequiredError(ctx, resolution.TenantId, resourcePath, "payment required - owner account suspended")
			}
			// Soft block: balance negative for prepaid (return 402-equivalent)
			if billing.BillingModel == "prepaid" && billing.IsBalanceNegative && !x402Paid {
				s.logger.WithFields(logging.Fields{
					"content_id": req.ContentId,
					"tenant_id":  resolution.TenantId,
				}).Warn("Rejecting viewer: content owner balance exhausted (402)")
				return nil, s.paymentRequiredError(ctx, resolution.TenantId, resourcePath, "payment required - content owner needs to top up balance")
			}
		}
	}

	if resolution.RequiresAuth {
		if authErr := s.enforceResolvePlaybackPolicy(ctx, req, resolution); authErr != nil {
			return nil, authErr
		}
	}

	// GeoIP resolution
	// IMPORTANT: default to NaN so missing GeoIP does not look like a real (0,0) coordinate.
	lat, lon := math.NaN(), math.NaN()
	viewerIP := req.GetViewerIp()

	if viewerIP != "" && s.geoipReader != nil {
		if geoData := geoip.LookupCached(ctx, s.geoipReader, s.geoipCache, viewerIP); geoData != nil {
			lat = geoData.Latitude
			lon = geoData.Longitude
		}
	}

	var response *sharedpb.ViewerEndpointResponse

	switch resolvedType {
	case "live":
		response, err = s.resolveLiveViewerEndpoint(ctx, req, lat, lon, resolution.RoutingInternalName(), resolution.TenantId, resolution.StreamId, resolution.ClusterPeers)
	case "dvr":
		response, err = s.resolveDVRViewerEndpoint(ctx, req, lat, lon, resolution)
	case "clip", "vod":
		response, err = s.resolveArtifactViewerEndpoint(ctx, req, lat, lon)
	default:
		return nil, status.Error(codes.InvalidArgument, "content_type must resolve to 'live', 'dvr', 'clip', or 'vod'")
	}

	if err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"content_type": resolvedType,
			"content_id":   req.ContentId,
		}).Error("Failed to resolve viewer endpoint")
		return nil, err
	}

	// Create virtual viewer for live streams (consistent with HTTP handlers)
	if resolvedType == "live" && response.Primary != nil && response.Primary.NodeId != "" {
		internalName := resolution.RoutingInternalName()
		if internalName == "" {
			internalName = req.ContentId
		}
		viewerID := state.DefaultManager().CreateVirtualViewer(response.Primary.NodeId, internalName, clientIP)
		control.AppendViewerCorrelationID(response, viewerID)
	}

	// Enrich live metadata from unified state
	if resolvedType == "live" && response.Metadata != nil {
		stateKey := resolution.RoutingInternalName()
		if stateKey == "" {
			stateKey = req.ContentId
		}
		st := state.DefaultManager().GetStreamState(stateKey)
		if st != nil {
			response.Metadata.IsLive = st.Status == "live"
			response.Metadata.Status = st.Status
			response.Metadata.Viewers = int32(st.Viewers)
			response.Metadata.BufferState = st.BufferState
		}
	}

	return response, nil
}

func (s *FoghornGRPCServer) enforceResolvePlaybackPolicy(ctx context.Context, req *sharedpb.ViewerEndpointRequest, resolution *control.ContentResolution) error {
	if control.CommodoreClient == nil {
		s.logger.WithFields(logging.Fields{
			"content_id": req.GetContentId(),
			"reason":     "policy-client-unavailable",
		}).Warn("Rejecting protected resolve request")
		return status.Error(codes.PermissionDenied, "playback access denied")
	}
	target := control.ResolvePlaybackPolicyTarget(ctx, resolution.ContentId, resolution.InternalName)
	policy, err := control.CommodoreClient.ResolvePlaybackPolicyForEnforcement(ctx, target.ContentID)
	if err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"content_id": req.GetContentId(),
			"reason":     "policy-fetch-failed",
		}).Warn("Rejecting protected resolve request")
		return status.Error(codes.PermissionDenied, "playback access denied")
	}
	policyInternalName := mist.ExtractInternalName(target.InternalName)
	if policyInternalName == "" {
		policyInternalName = resolution.InternalName
	}
	decision := triggers.EvaluatePlaybackPolicyWithRecorder(ctx, s.logger, policyInternalName, &ipcpb.ViewerConnectTrigger{
		StreamName:  policyInternalName,
		SessionId:   "resolve:" + req.GetContentId(),
		Host:        req.GetViewerIp(),
		RequestUrl:  "viewer://" + req.GetContentId(),
		ViewerToken: req.GetViewerToken(),
		Connector:   "resolve",
	}, policy, control.CommodoreClient)
	if decision != "true" {
		return status.Error(codes.PermissionDenied, "playback access denied")
	}
	return nil
}

func (s *FoghornGRPCServer) resolveLiveViewerEndpoint(ctx context.Context, req *sharedpb.ViewerEndpointRequest, lat, lon float64, internalName, tenantID, streamID string, clusterPeers []*clusterpeerpb.TenantClusterPeer) (*sharedpb.ViewerEndpointResponse, error) {
	start := time.Now()
	deps := &control.PlaybackDependencies{
		DB:             s.db,
		LB:             s.lb,
		GeoLat:         lat,
		GeoLon:         lon,
		LocalClusterID: s.clusterID,
	}

	if internalName == "" {
		return nil, status.Error(codes.NotFound, "stream not found")
	}

	// Loop prevention: skip remote edges if we're already pulling this stream
	skipRemote := false
	if control.StreamRegistryInstance != nil {
		if _, ok := control.StreamRegistryInstance.LocalReplication(ctx, internalName); ok {
			skipRemote = true
		}
	}

	// Collect remote edge candidates from federation cache.
	// Primary source: cluster peers from resolution (free with every Commodore call).
	// Fallback: trigger processor cache (for streams ingesting locally).
	allPeers := clusterPeers
	if !skipRemote && s.remoteEdgeCache != nil && len(allPeers) > 0 {
		deps.RemoteEdges = s.collectRemoteEdges(ctx, allPeers)
	}
	if !skipRemote && s.remoteEdgeCache != nil && len(deps.RemoteEdges) == 0 && s.cacheInvalidator != nil {
		if tpPeers := s.cacheInvalidator.GetClusterPeers(internalName, tenantID); len(tpPeers) > 0 {
			deps.RemoteEdges = s.collectRemoteEdges(ctx, tpPeers)
			if len(allPeers) == 0 {
				allPeers = tpPeers
			}
		}
	}
	// Pre-warmed path: StreamAdvertisement-fed registry edges (5s cadence)
	// before paying a fan-out; same ordering as the HTTP /play handler.
	if !skipRemote && len(deps.RemoteEdges) == 0 {
		deps.RemoteEdges = control.FederatedRemoteEdges(internalName)
	}
	// Cold start: EdgeSummary cache empty, no usable ads, but peers exist:
	// fan out QueryStream (single-flighted + memoized; detached from this
	// caller's cancellation so an abandoned request can't poison the shared
	// memo window with an empty candidate set).
	if !skipRemote && len(deps.RemoteEdges) == 0 && len(allPeers) > 0 && s.fanOutShared != nil {
		deps.RemoteEdges = s.fanOutShared.Do(tenantID+"/"+internalName, func() []balancer.RemoteEdgeCandidate {
			fanCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			return s.queryStreamFanOut(fanCtx, internalName, tenantID, lat, lon, allPeers)
		})
	}

	response, err := control.ResolveLivePlayback(ctx, deps, req.ContentId, internalName, streamID, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "%v", err)
	}

	// If a remote cluster won the summary-level comparison, confirm with QueryStream.
	// An infra-error from arrangement bubbles up as 5xx rather than silently
	// degrading to a summary-level redirect.
	if response.Primary != nil && response.Primary.ClusterId != "" {
		confirmed, confirmErr := s.confirmRemoteEndpoint(ctx, response, req.ContentId, internalName, tenantID, lat, lon)
		if confirmErr != nil {
			return nil, status.Errorf(codes.Unavailable, "%v", confirmErr)
		}
		if confirmed != nil {
			response = confirmed
		}
	}

	// Emit routing event for analytics
	if response.Primary != nil {
		durationMs := float32(time.Since(start).Milliseconds())
		candidatesCount := int32(0)
		if response.Primary != nil {
			candidatesCount = int32(1 + len(response.Fallbacks))
		}
		s.emitRoutingEvent(response.Primary, lat, lon, 0, 0, internalName, tenantID, streamID, durationMs, candidatesCount, "grpc_resolve", "grpc")
	}

	return response, nil
}

// collectRemoteEdges queries the federation cache for each peer cluster's edge summary
// and converts the results to RemoteEdgeCandidates for the load balancer.
func (s *FoghornGRPCServer) collectRemoteEdges(ctx context.Context, peers []*clusterpeerpb.TenantClusterPeer) []balancer.RemoteEdgeCandidate {
	var candidates []balancer.RemoteEdgeCandidate
	for _, peer := range peers {
		if peer.GetClusterId() == s.clusterID || peer.GetClusterId() == "" || control.IsServedCluster(peer.GetClusterId()) {
			continue
		}
		// Liveness gate: a peer's EdgeSummary (60s TTL) outlives its heartbeat (30s TTL),
		// so a peer dead 30–60s still has a cached summary. Skip it once the heartbeat key
		// has expired, so stale telemetry can't attract cross-cluster routing.
		if hb, hbErr := s.remoteEdgeCache.GetPeerHeartbeat(ctx, peer.GetClusterId()); hbErr != nil || hb == nil {
			continue
		}
		record, err := s.remoteEdgeCache.GetEdgeSummary(ctx, peer.GetClusterId())
		if err != nil || record == nil {
			continue
		}
		for _, edge := range record.Edges {
			candidates = append(candidates, balancer.RemoteEdgeCandidate{
				ClusterID:   peer.GetClusterId(),
				NodeID:      edge.NodeID,
				BaseURL:     edge.BaseURL,
				GeoLat:      edge.GeoLat,
				GeoLon:      edge.GeoLon,
				BWAvailable: edge.BWAvailableAvg,
				CPUPercent:  edge.CPUPercentAvg,
				RAMUsed:     edge.RAMUsed,
				RAMMax:      edge.RAMMax,
			})
		}
	}
	return candidates
}

// confirmRemoteEndpoint validates a summary-level remote win by calling
// QueryStream on the winning cluster(s). Returns:
//
//	(non-nil, nil) — confirmed; caller swaps the summary-level response
//	                  for this richer one.
//	(nil, nil)     — confirmation failed softly (LB miss, no peer reply,
//	                  no DTSC URL); caller keeps the summary-level redirect.
//	(nil, err)     — arrangement infra failure (registry/deps/peer/notify);
//	                  caller surfaces 5xx instead of silently redirecting.
func (s *FoghornGRPCServer) confirmRemoteEndpoint(ctx context.Context, response *sharedpb.ViewerEndpointResponse, viewKey, internalName, tenantID string, lat, lon float64) (*sharedpb.ViewerEndpointResponse, error) {
	if s.federationClient == nil || s.peerManager == nil {
		return nil, nil
	}

	type remoteHit struct {
		clusterID string
		score     float64
	}
	var remotes []remoteHit
	seen := make(map[string]bool)

	if response.Primary != nil && response.Primary.ClusterId != "" && !seen[response.Primary.ClusterId] {
		seen[response.Primary.ClusterId] = true
		remotes = append(remotes, remoteHit{clusterID: response.Primary.ClusterId, score: response.Primary.LoadScore})
	}
	for _, fb := range response.Fallbacks {
		if fb.ClusterId != "" && !seen[fb.ClusterId] {
			seen[fb.ClusterId] = true
			remotes = append(remotes, remoteHit{clusterID: fb.ClusterId, score: fb.LoadScore})
		}
	}
	if len(remotes) == 0 {
		return nil, nil
	}

	type queryResult struct {
		clusterID string
		resp      *foghornfederationpb.QueryStreamResponse
	}
	ch := make(chan queryResult, len(remotes))
	var wg sync.WaitGroup

	for _, r := range remotes {
		addr := s.peerManager.GetPeerAddr(r.clusterID)
		if addr == "" {
			continue
		}
		wg.Add(1)
		go func(cid, caddr string) {
			defer wg.Done()
			qCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			resp, err := s.federationClient.QueryStream(qCtx, cid, caddr, &foghornfederationpb.QueryStreamRequest{
				StreamName:        internalName,
				ViewerLat:         lat,
				ViewerLon:         lon,
				RequestingCluster: s.clusterID,
				TenantId:          tenantID,
			})
			if err != nil || resp == nil || len(resp.Candidates) == 0 {
				return
			}
			ch <- queryResult{clusterID: cid, resp: resp}
		}(r.clusterID, addr)
	}
	go func() { wg.Wait(); close(ch) }()

	var bestCandidate *foghornfederationpb.EdgeCandidate
	var bestCluster string
	for qr := range ch {
		for _, c := range qr.resp.Candidates {
			if bestCandidate == nil || c.BwScore > bestCandidate.BwScore {
				bestCandidate = c
				bestCluster = qr.clusterID
			}
		}
	}
	if bestCandidate == nil {
		return nil, nil
	}

	// Try origin-pull: pre-arrange local replication so MistServer can pull via DTSC.
	// Infra-error propagates so the viewer sees 5xx instead of a silently-degraded redirect.
	arranged, arrangeErr := s.arrangeOriginPull(ctx, bestCandidate, bestCluster, internalName, tenantID, viewKey, lat, lon, response)
	if arrangeErr != nil {
		return nil, arrangeErr
	}
	if arranged != nil {
		return arranged, nil
	}

	// No origin-pull possible — redirect viewer to the remote cluster directly
	playURL := control.PlaybackEdgeRedirectURL(bestCandidate.BaseUrl, viewKey)
	confirmed := &sharedpb.ViewerEndpointResponse{
		Primary: &sharedpb.ViewerEndpoint{
			NodeId:    bestCandidate.NodeId,
			BaseUrl:   bestCandidate.BaseUrl,
			Protocol:  "redirect",
			Url:       playURL,
			LoadScore: float64(bestCandidate.BwScore),
			ClusterId: bestCluster,
		},
		Metadata: response.Metadata,
	}
	for _, fb := range response.Fallbacks {
		if fb.ClusterId == "" {
			confirmed.Fallbacks = append(confirmed.Fallbacks, fb)
		}
	}

	s.logger.WithFields(logging.Fields{
		"stream":         internalName,
		"remote_cluster": bestCluster,
		"remote_node":    bestCandidate.NodeId,
		"remote_score":   bestCandidate.BwScore,
	}).Info("Remote endpoint confirmed via QueryStream — redirecting (no local capacity)")

	return confirmed, nil
}

// arrangeOriginPull pre-arranges a local DTSC pull from a remote source.
// Three return shapes:
//
//	(non-nil, nil) — arrangement succeeded; viewer goes to local edge
//	(nil, nil)     — soft refusal (no DTSC URL, contention, no local
//	                  capacity, loop prevention); caller falls through to
//	                  peer-redirect fallback
//	(nil, err)     — infra failure (registry/deps/peer/notify); caller
//	                  surfaces a 5xx so operators see the underlying break
//	                  instead of a silently-degraded redirect. Use
//	                  federation.IsArrangeInfraError to discriminate.
//
// Thin wrapper around federation.ArrangeOriginPull — the in-process
// tryBeginOriginPull guard sits in front of the shared helper to coalesce
// concurrent gRPC arrangement requests on this instance before they all
// line up on the Redis lock.
func (s *FoghornGRPCServer) arrangeOriginPull(ctx context.Context, remote *foghornfederationpb.EdgeCandidate, remoteCluster, internalName, tenantID, viewKey string, lat, lon float64, original *sharedpb.ViewerEndpointResponse) (*sharedpb.ViewerEndpointResponse, error) {
	if s.remoteEdgeCache == nil || remote.DtscUrl == "" {
		return nil, nil
	}

	registry := control.StreamRegistryInstance
	if !s.tryBeginOriginPull(internalName) {
		if registry != nil {
			if loc, ok := registry.LocalReplication(ctx, internalName); ok {
				if endpoint := s.buildLocalEndpoint(loc.DestNodeID, loc.DestNodeBaseURL, viewKey); endpoint != nil {
					return &sharedpb.ViewerEndpointResponse{Primary: endpoint, Metadata: original.Metadata}, nil
				}
			}
		}
		return nil, nil
	}
	defer s.finishOriginPull(internalName)

	// Pre-clear a stale registry entry whose dest node disappeared so
	// the shared helper re-runs NotifyOriginPull instead of reusing.
	// This case is gRPC-specific (loop only invokes arrangeOriginPull
	// when the registry didn't already resolve to a usable endpoint).
	if registry != nil {
		if loc, ok := registry.LocalReplication(ctx, internalName); ok {
			if s.buildLocalEndpoint(loc.DestNodeID, loc.DestNodeBaseURL, viewKey) == nil {
				registry.ClearReplicating(internalName)
			}
		}
	}

	deps := &federation.ArrangeOriginPullDeps{
		Cache:        s.remoteEdgeCache,
		PeerResolver: s.peerManager,
		FedClient:    s.federationClient,
		InstanceID:   s.instanceID,
		ClusterID:    s.clusterID,
		Logger:       s.logger,
	}
	result, err := deps.ArrangeOriginPull(ctx, federation.ArrangeOriginPullRequest{
		InternalName:  internalName,
		Remote:        remote,
		RemoteCluster: remoteCluster,
		TenantID:      tenantID,
		Lat:           lat,
		Lon:           lon,
		LBPicker: func(pickCtx context.Context, pickLat, pickLon float64, pickTenant string) (string, string, error) {
			lbCtx := context.WithValue(pickCtx, ctxkeys.KeyCapability, "edge")
			if pickTenant != "" {
				lbCtx = context.WithValue(lbCtx, ctxkeys.KeyClusterScope, pickTenant)
			}
			host, _, _, _, _, pickErr := s.lb.GetBestNodeWithScore(lbCtx, "", pickLat, pickLon, nil, "", false)
			if pickErr != nil {
				return "", "", pickErr
			}
			return host, s.lb.GetNodeIDByHost(host), nil
		},
	})
	if err != nil {
		if federation.IsArrangeInfraError(err) {
			s.logger.WithError(err).WithFields(logging.Fields{
				"stream":         internalName,
				"remote_cluster": remoteCluster,
			}).Error("ArrangeOriginPull: infra failure; refusing to silently redirect")
			return nil, err
		}
		return nil, nil
	}
	if result == nil {
		return nil, nil
	}
	endpoint := s.buildLocalEndpoint(result.DestNodeID, result.DestNodeBaseURL, viewKey)
	if endpoint != nil {
		return &sharedpb.ViewerEndpointResponse{Primary: endpoint, Metadata: original.Metadata}, nil
	}
	return nil, nil
}

func (s *FoghornGRPCServer) tryBeginOriginPull(streamName string) bool {
	if streamName == "" {
		return false
	}
	s.originPullMu.Lock()
	defer s.originPullMu.Unlock()
	if _, exists := s.originPulling[streamName]; exists {
		return false
	}
	s.originPulling[streamName] = struct{}{}
	return true
}

func (s *FoghornGRPCServer) finishOriginPull(streamName string) {
	if streamName == "" {
		return
	}
	s.originPullMu.Lock()
	delete(s.originPulling, streamName)
	s.originPullMu.Unlock()
}

// buildLocalEndpoint constructs a ViewerEndpoint from a local node serving
// an active inbound replication. Takes the dest-node identity directly so
// callers can pass it from either the registry's Location or any other
// origin-pull bookkeeping without round-tripping through a record type.
func (s *FoghornGRPCServer) buildLocalEndpoint(destNodeID, destNodeBaseURL, viewKey string) *sharedpb.ViewerEndpoint {
	outputs, exists := control.GetNodeOutputs(destNodeID)
	if !exists || outputs.Outputs == nil {
		return nil
	}
	endpoint := control.BuildViewerEndpointFromOutputs(destNodeID, outputs, viewKey, true)
	if endpoint != nil && destNodeBaseURL != "" {
		endpoint.BaseUrl = destNodeBaseURL
	}
	return endpoint
}

// queryStreamFanOut performs cold-start QueryStream to peer clusters when EdgeSummary is empty.
func (s *FoghornGRPCServer) queryStreamFanOut(ctx context.Context, internalName, tenantID string, lat, lon float64, peers []*clusterpeerpb.TenantClusterPeer) []balancer.RemoteEdgeCandidate {
	if s.federationClient == nil || s.peerManager == nil {
		return nil
	}

	type result struct {
		candidates []balancer.RemoteEdgeCandidate
	}
	ch := make(chan result, len(peers))
	var wg sync.WaitGroup

	for _, peer := range peers {
		if peer.GetClusterId() == s.clusterID || peer.GetClusterId() == "" || control.IsServedCluster(peer.GetClusterId()) {
			continue
		}
		addr := s.peerManager.GetPeerAddr(peer.GetClusterId())
		if addr == "" {
			continue
		}
		wg.Add(1)
		go func(peerID, peerAddr string) {
			defer wg.Done()
			qCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			resp, err := s.federationClient.QueryStream(qCtx, peerID, peerAddr, &foghornfederationpb.QueryStreamRequest{
				StreamName:        internalName,
				ViewerLat:         lat,
				ViewerLon:         lon,
				RequestingCluster: s.clusterID,
				TenantId:          tenantID,
			})
			if err != nil || resp == nil || len(resp.Candidates) == 0 {
				ch <- result{}
				return
			}
			var cands []balancer.RemoteEdgeCandidate
			for _, c := range resp.Candidates {
				cands = append(cands, balancer.RemoteEdgeCandidate{
					ClusterID:   peerID,
					NodeID:      c.NodeId,
					BaseURL:     c.BaseUrl,
					GeoLat:      c.GeoLat,
					GeoLon:      c.GeoLon,
					BWAvailable: c.BwAvailable,
					CPUPercent:  c.CpuPercent,
					RAMUsed:     c.RamUsed,
					RAMMax:      c.RamMax,
				})
			}
			ch <- result{candidates: cands}
		}(peer.GetClusterId(), addr)
	}
	go func() { wg.Wait(); close(ch) }()

	var all []balancer.RemoteEdgeCandidate
	for r := range ch {
		all = append(all, r.candidates...)
	}
	return all
}

// resolveDVRViewerEndpoint routes DVR viewer requests by lifecycle:
// active recordings use live-style edge selection so any healthy edge
// can serve via Mist's PLAY_REWRITE → STREAM_SOURCE → DTSC pull path
// from the recording origin; finalized DVRs fall through to the warm-
// cache artifact routing used by clip/VOD.
//
// resolution.InternalName for a DVR artifact playbackID is already
// "dvr+<dvr_internal_name>" (set by control.ResolveStream); the edge's
// PLAY_REWRITE rewrites the public ID back to that stream name before
// STREAM_SOURCE fires.
//
// Fail-closed semantics for active DVR: when a recording is in flight
// but the recording-origin lookup is ambiguous or fails, we surface
// Unavailable rather than route through the archive/warm-cache lane.
// Archive routing would silently land viewers on nodes that don't own
// the live segments and produce stale playback.
func (s *FoghornGRPCServer) resolveDVRViewerEndpoint(ctx context.Context, req *sharedpb.ViewerEndpointRequest, lat, lon float64, resolution *control.ContentResolution) (*sharedpb.ViewerEndpointResponse, error) {
	dvrInternalName := mist.ExtractInternalName(resolution.InternalName)
	dispatch, derr := control.ResolveDVRArtifactDispatch(ctx, dvrInternalName)
	if derr != nil {
		s.logger.WithError(derr).WithFields(logging.Fields{
			"content_id":    req.GetContentId(),
			"internal_name": dvrInternalName,
		}).Warn("DVR dispatch lookup failed")
		// Don't silently downgrade to artifact routing — the dispatch
		// helper errors specifically on active-DVR ambiguity, which
		// must not be papered over with stale-segment fallback.
		return nil, status.Error(codes.Unavailable, "DVR routing unavailable")
	}
	if dispatch != nil && dispatch.Status != "" && control.IsActiveDVRStatus(dispatch.Status) {
		if dispatch.RecordingNode == "" {
			s.logger.WithFields(logging.Fields{
				"content_id":    req.GetContentId(),
				"internal_name": dvrInternalName,
				"status":        dispatch.Status,
			}).Warn("Active DVR has no resolvable recording origin; refusing to fall back to archive routing")
			return nil, status.Error(codes.Unavailable, "active DVR recording origin not yet registered; retry")
		}
		resp, err := s.resolveLiveViewerEndpoint(ctx, req, lat, lon, resolution.InternalName, resolution.TenantId, resolution.StreamId, resolution.ClusterPeers)
		if err != nil {
			return nil, err
		}
		// The live resolver labels metadata as live; rewrite to DVR
		// identity so clients see what they're actually playing.
		overrideActiveDVRMetadata(resp, dispatch)
		return resp, nil
	}
	latest, latestErr := s.latestPlayableChapterForDVR(ctx, dispatch)
	if latestErr != nil {
		s.logger.WithError(latestErr).WithFields(logging.Fields{
			"content_id":    req.GetContentId(),
			"internal_name": dvrInternalName,
		}).Warn("Stopped DVR: latest-chapter lookup failed")
		return nil, status.Error(codes.Unavailable, "DVR no longer active; chapter lookup failed")
	}
	if latest == "" {
		return nil, status.Error(codes.FailedPrecondition, "DVR is no longer active and has no playable chapters yet; query dvrChapters when finalization completes")
	}
	chapterReq := &sharedpb.ViewerEndpointRequest{
		ContentId: latest,
	}
	if vip := req.GetViewerIp(); vip != "" {
		chapterReq.ViewerIp = &vip
	}
	if vt := req.GetViewerToken(); vt != "" {
		chapterReq.ViewerToken = &vt
	}
	return s.ResolveViewerEndpoint(ctx, chapterReq)
}

// latestPlayableChapterForDVR returns the Commodore-minted public
// playback_id of the most-recent playable chapter for a stopped DVR.
// Returns "" with no error when no chapter has been dispatched yet.
func (s *FoghornGRPCServer) latestPlayableChapterForDVR(ctx context.Context, dispatch *control.DVRArtifactDispatch) (string, error) {
	if dispatch == nil || dispatch.DVRHash == "" {
		return "", nil
	}
	var pid sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT playback_id
		  FROM foghorn.dvr_chapters
		 WHERE artifact_hash = $1
		   AND state IN ('finalized', 'frozen', 'reclaimed')
		   AND playback_id IS NOT NULL
		 ORDER BY end_ms DESC
		 LIMIT 1
	`, dispatch.DVRHash).Scan(&pid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !pid.Valid {
		return "", nil
	}
	return pid.String, nil
}

// overrideActiveDVRMetadata rewrites PlaybackMetadata produced by the
// live resolver so it reports the DVR identity that's actually being
// served. Without this clients see ContentType="live" for an active
// DVR endpoint and lose the rolling-window/seek semantics distinction.
func overrideActiveDVRMetadata(resp *sharedpb.ViewerEndpointResponse, dispatch *control.DVRArtifactDispatch) {
	if resp == nil || resp.Metadata == nil || dispatch == nil {
		return
	}
	resp.Metadata.ContentType = "dvr"
	resp.Metadata.Status = "recording"
	resp.Metadata.DvrStatus = "recording"
	// IsLive stays true: the surface is live-replayable. The status
	// field carries the distinction between "live" and "recording".
}

func (s *FoghornGRPCServer) resolveArtifactViewerEndpoint(ctx context.Context, req *sharedpb.ViewerEndpointRequest, lat, lon float64) (*sharedpb.ViewerEndpointResponse, error) {
	start := time.Now()
	deps := &control.PlaybackDependencies{
		DB:              s.db,
		LB:              s.lb,
		GeoLat:          lat,
		GeoLon:          lon,
		FedClient:       s.federationClient,
		PeerResolver:    s.peerManager,
		LocalClusterID:  s.clusterID,
		RemoteArtifacts: s.remoteArtifactLookup(),
	}

	response, err := control.ResolveArtifactPlayback(ctx, deps, req.ContentId)
	if err != nil {
		if errors.Is(err, control.ErrCrossClusterArtifactUnavailable) {
			// Fail-fast — peer origin hasn't pushed the artifact to S3
			// yet. codes.Unavailable; caller retries at the app layer.
			return nil, status.Error(codes.Unavailable, err.Error())
		}
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Error(codes.NotFound, err.Error())
		}
		if strings.Contains(err.Error(), "not available") || strings.Contains(err.Error(), "unknown") {
			return nil, status.Error(codes.Unavailable, err.Error())
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}

	// Emit routing event for analytics
	if response.Primary != nil && response.Metadata != nil {
		durationMs := float32(time.Since(start).Milliseconds())
		candidatesCount := int32(0)
		if response.Primary != nil {
			candidatesCount = int32(1 + len(response.Fallbacks))
		}
		internalName := ""
		if target, _ := control.ResolveStream(ctx, req.ContentId); target != nil {
			internalName = target.InternalName
		}
		s.emitRoutingEvent(response.Primary, 0, 0, 0, 0, internalName, response.Metadata.GetTenantId(), response.Metadata.GetStreamId(), durationMs, candidatesCount, "grpc_resolve", "grpc")
	}

	return response, nil
}

func x402PaidFromMetadata(ctx context.Context) bool {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok || md == nil {
		return false
	}
	values := md.Get("x402-paid")
	if len(values) == 0 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(values[0])) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func (s *FoghornGRPCServer) handleX402ViewerPayment(ctx context.Context, tenantID, resourcePath, paymentHeader, clientIP string) (bool, error) {
	if tenantID == "" || paymentHeader == "" || s.purserClient == nil {
		return false, nil
	}

	result, err := x402.SettleX402Payment(ctx, x402.SettlementOptions{
		PaymentHeader: paymentHeader,
		Resource:      resourcePath,
		AuthTenantID:  "",
		ClientIP:      clientIP,
		Purser:        s.purserClient,
		Commodore:     nil,
		Logger:        s.logger,
		Resolution: &x402.ResourceResolution{
			Resource: resourcePath,
			Kind:     x402.ResourceKindViewer,
			TenantID: tenantID,
			Resolved: true,
		},
	})

	if err != nil {
		return false, s.mapSettlementErrorToGRPC(ctx, tenantID, resourcePath, err)
	}

	if result == nil || result.Settle == nil || !result.Settle.Success {
		return false, s.paymentFailedError(ctx, tenantID, resourcePath, "payment settlement failed")
	}

	return true, nil
}

func (s *FoghornGRPCServer) mapSettlementErrorToGRPC(ctx context.Context, tenantID, resourcePath string, err *x402.SettlementError) error {
	switch err.Code {
	case x402.ErrInvalidPayment:
		return s.paymentFailedError(ctx, tenantID, resourcePath, err.Message)
	case x402.ErrBillingDetailsRequired:
		return s.billingDetailsRequiredError(err.Message)
	case x402.ErrAuthOnly:
		return s.paymentRequiredError(ctx, tenantID, resourcePath, "payment required - balance exhausted")
	case x402.ErrVerificationFailed:
		return s.paymentFailedError(ctx, tenantID, resourcePath, err.Message)
	case x402.ErrSettlementFailed:
		return s.paymentFailedError(ctx, tenantID, resourcePath, err.Message)
	default:
		return s.paymentFailedError(ctx, tenantID, resourcePath, err.Message)
	}
}

func (s *FoghornGRPCServer) billingDetailsRequiredError(message string) error {
	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = "billing details required"
	}
	return status.Error(codes.FailedPrecondition, msg)
}

func (s *FoghornGRPCServer) paymentRequiredError(ctx context.Context, tenantID, resourcePath, message string) error {
	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = "payment required"
	}
	st := status.New(codes.FailedPrecondition, msg)
	if s.purserClient != nil {
		reqs, err := s.purserClient.GetPaymentRequirements(ctx, tenantID, resourcePath)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to get x402 payment requirements")
		} else if reqs != nil {
			if stWith, err := st.WithDetails(reqs); err == nil {
				st = stWith
			}
		}
	}
	return st.Err()
}

func (s *FoghornGRPCServer) paymentFailedError(ctx context.Context, tenantID, resourcePath, message string) error {
	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = "payment failed"
	}
	st := status.New(codes.FailedPrecondition, msg)
	if s.purserClient != nil {
		reqs, err := s.purserClient.GetPaymentRequirements(ctx, tenantID, resourcePath)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to get x402 payment requirements")
		} else if reqs != nil {
			if stWith, err := st.WithDetails(reqs); err == nil {
				st = stWith
			}
		}
	}
	return st.Err()
}

// VOD CONTROL SERVICE IMPLEMENTATION

// generateVodHash creates a unique hash for a VOD upload
func generateVodHash(tenantID, filename string, timestamp time.Time) string {
	data := fmt.Sprintf("%s:%s:%d", tenantID, filename, timestamp.UnixNano())
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])[:32] // 32 char hash like clips
}

// CreateVodUpload initiates a multipart upload and returns presigned URLs. It records the
// attempt in Foghorn's command ledger — 'accepted' before any fallible precheck and before
// the external S3 multipart create, then 'committed' atomically with the artifact row, or a
// deferred 'rejected' on any pre-commit exit. See docs/architecture/creation-saga.md.
func (s *FoghornGRPCServer) CreateVodUpload(ctx context.Context, req *sharedpb.CreateVodUploadRequest) (resp *sharedpb.CreateVodUploadResponse, err error) {
	var prog creationLedgerProgress
	defer func() {
		err = s.finalizeCreationCommand(req.GetRequestId(), req.GetTenantId(), "vod", req.GetVodHash(), &prog, err)
	}()
	resp, err = s.createVodUploadImpl(ctx, req, &prog)
	return resp, err
}

// existingVodUploadResult returns the idempotent CreateVodUpload response for a VOD
// artifact whose multipart already exists (a retry after a lost response), re-signing
// the EXISTING S3 multipart from its persisted s3_key + upload_id rather than creating a
// second one. found is false when no such artifact exists.
func (s *FoghornGRPCServer) existingVodUploadResult(ctx context.Context, tenantID, artifactHash, playbackID string, partSize int64) (resp *sharedpb.CreateVodUploadResponse, found bool, err error) {
	if tenantID == "" || artifactHash == "" {
		return nil, false, nil
	}
	var (
		existingUploadID  string
		existingS3Key     string
		existingParts     int
		existingExpiresAt time.Time
	)
	idErr := s.db.QueryRowContext(ctx, `
		SELECT m.s3_upload_id, m.s3_key, m.total_parts, m.upload_expires_at
		  FROM foghorn.artifacts a
		  JOIN foghorn.vod_metadata m ON m.artifact_hash = a.artifact_hash
		 WHERE a.artifact_hash = $1 AND a.artifact_type = 'vod' AND a.tenant_id = $2
	`, artifactHash, tenantID).Scan(&existingUploadID, &existingS3Key, &existingParts, &existingExpiresAt)
	if errors.Is(idErr, sql.ErrNoRows) {
		return nil, false, nil
	}
	if idErr != nil {
		return nil, false, status.Errorf(codes.Internal, "failed to check existing VOD upload: %v", idErr)
	}
	if s.s3Client == nil {
		return nil, false, status.Error(codes.FailedPrecondition, "S3 storage not configured")
	}
	reParts, reErr := s.s3Client.GeneratePresignedUploadParts(existingS3Key, existingUploadID, existingParts, 2*time.Hour)
	if reErr != nil {
		return nil, false, status.Errorf(codes.Internal, "failed to re-sign existing upload URLs: %v", reErr)
	}
	protoParts := make([]*sharedpb.VodUploadPart, len(reParts))
	for i, p := range reParts {
		protoParts[i] = &sharedpb.VodUploadPart{PartNumber: int32(p.PartNumber), PresignedUrl: p.PresignedURL}
	}
	return &sharedpb.CreateVodUploadResponse{
		UploadId:     existingUploadID,
		ArtifactId:   artifactHash,
		ArtifactHash: artifactHash,
		PartSize:     partSize,
		Parts:        protoParts,
		ExpiresAt:    timestamppb.New(existingExpiresAt),
		PlaybackId:   playbackID,
	}, true, nil
}

func (s *FoghornGRPCServer) createVodUploadImpl(ctx context.Context, req *sharedpb.CreateVodUploadRequest, prog *creationLedgerProgress) (*sharedpb.CreateVodUploadResponse, error) {
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.Filename == "" {
		return nil, status.Error(codes.InvalidArgument, "filename is required")
	}
	if req.SizeBytes <= 0 {
		return nil, status.Error(codes.InvalidArgument, "size_bytes must be positive")
	}
	if req.GetInternalName() == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name is required")
	}
	// The durable creation ledger is keyed by (request_id, vod_hash). When request_id is set the
	// caller-minted vod_hash MUST accompany it: otherwise 'accepted' is recorded under the empty hash while
	// 'committed' keys on the hash Foghorn generates, so the commit CAS could never match and the artifact
	// transaction would always roll back. Commodore always sends both; enforce the contract.
	if req.GetRequestId() != "" && strings.TrimSpace(req.GetVodHash()) == "" {
		return nil, status.Error(codes.InvalidArgument, "vod_hash is required when request_id is set")
	}

	// Record 'accepted' in the durable command ledger (keyed by request_id + vod_hash)
	// BEFORE any fallible precheck and before the external S3 multipart create: a failure
	// past this point is a still-'accepted' row the deferred finalizer flips to 'rejected',
	// never an absent row the sweep polls forever. A failed durable write fails the RPC so
	// the client re-drives. See docs/architecture/creation-saga.md.
	acceptState, acceptErr := s.recordCreationCommandAcceptedDurable(ctx, req.GetRequestId(), req.GetTenantId(), "vod", req.GetVodHash(), prog)
	if acceptErr != nil {
		s.logger.WithError(acceptErr).WithField("vod_hash", req.GetVodHash()).Error("Failed to record accepted VOD creation command")
		if errors.Is(acceptErr, errCreationCommandIdentityMismatch) {
			return nil, status.Error(codes.FailedPrecondition, "request_id already used for a different artifact")
		}
		return nil, status.Errorf(codes.Unavailable, "failed to record VOD creation attempt: %v", acceptErr)
	}
	// A terminal retry must not resume the create's external work. 'rejected' is
	// definitive; 'committed' means the upload artifact + multipart already exist, so
	// short-circuit to the idempotent existing-artifact result (re-signing the EXISTING
	// multipart) without creating a second S3 upload.
	switch acceptState {
	case creationCommandRejected:
		return nil, status.Error(codes.FailedPrecondition, "VOD creation was terminally rejected")
	case creationCommandCommitted:
		partSize, _ := storage.CalculatePartSize(req.SizeBytes)
		resp, found, existErr := s.existingVodUploadResult(ctx, req.GetTenantId(), req.GetVodHash(), req.GetPlaybackId(), partSize)
		if existErr != nil {
			return nil, existErr
		}
		if !found {
			return nil, status.Error(codes.Internal, "committed VOD command has no artifact row")
		}
		s.logger.WithField("vod_hash", req.GetVodHash()).Info("CreateVodUpload retry of a committed create; returning existing multipart (idempotent)")
		return resp, nil
	}

	// Upload size is known up-front; reject when the upload would push the
	// tenant over their storage cap. See checkStorageEntitlement docs.
	if err := s.checkStorageEntitlement(ctx, req.TenantId, req.SizeBytes); err != nil {
		return nil, err
	}

	// VOD multipart upload is local-mint only: when the resolver picks a
	// remote storage cluster, callers receive
	// storage_delegation_unsupported_for_vod. The Create/Complete/Abort
	// multipart lifecycle is not exposed via the federation MintStorageURLs
	// RPC, so we cannot delegate the create here.
	storageCluster, mintMode := s.resolveVodStorageCluster(ctx, req.GetTenantId(), req.GetClusterId())
	switch mintMode {
	case storage.StorageMintViaFederation:
		return nil, status.Error(codes.Unimplemented, "storage_delegation_unsupported_for_vod")
	case storage.StorageUnavailable:
		return nil, status.Error(codes.FailedPrecondition, "storage service unavailable")
	}
	if s.s3Client == nil {
		return nil, status.Error(codes.FailedPrecondition, "S3 storage not configured")
	}

	// Use hash from Commodore if provided, otherwise generate
	// Commodore is authoritative for hash generation in production flows
	artifactHash := req.GetVodHash()
	if artifactHash == "" {
		artifactHash = generateVodHash(req.TenantId, req.Filename, time.Now())
	}

	// Calculate part size and count
	partSize, partCount := storage.CalculatePartSize(req.SizeBytes)

	// Idempotency: a retry after a lost response carries the SAME Commodore-minted
	// vod_hash (the artifact PK). If the artifact + its multipart metadata already
	// exist, re-sign the EXISTING S3 multipart upload and return it rather than
	// creating a second multipart/artifact/outbox event. Presigned URLs are
	// re-derived from the persisted s3_key + upload_id, so the retry response is
	// equivalent to the original.
	if req.GetVodHash() != "" {
		resp, found, existErr := s.existingVodUploadResult(ctx, req.TenantId, artifactHash, req.GetPlaybackId(), partSize)
		if existErr != nil {
			return nil, existErr
		}
		if found {
			// The artifact + multipart already exist (a retry after a lost response).
			// Ensure this attempt's command row is 'committed' before returning success, so
			// a live artifact is never left behind a forever-'accepted' command the sweep
			// would poll as in-flight.
			if ensErr := s.ensureCreationCommandCommitted(ctx, req.GetRequestId(), req.GetTenantId(), "vod", artifactHash, prog); ensErr != nil {
				return nil, status.Errorf(codes.Unavailable, "failed to finalize VOD creation command: %v", ensErr)
			}
			s.logger.WithFields(logging.Fields{
				"artifact_hash": artifactHash,
				"upload_id":     resp.GetUploadId(),
			}).Info("CreateVodUpload is a retry for an existing upload; returning existing multipart (idempotent)")
			return resp, nil
		}
	}

	// Build S3 key
	s3Key := s.s3Client.BuildVodS3Key(req.TenantId, artifactHash, req.Filename)

	// Determine content type
	contentType := req.GetContentType()
	if contentType == "" {
		contentType = "video/mp4" // default
	}

	// Create S3 multipart upload
	uploadID, err := s.s3Client.CreateMultipartUpload(ctx, s3Key, contentType)
	if err != nil {
		s.logger.WithError(err).Error("Failed to create S3 multipart upload")
		return nil, status.Errorf(codes.Internal, "failed to create upload: %v", err)
	}

	// Generate presigned URLs for all parts (2 hour expiry)
	parts, err := s.s3Client.GeneratePresignedUploadParts(s3Key, uploadID, partCount, 2*time.Hour)
	if err != nil {
		// Abort the multipart upload since we can't generate URLs
		_ = s.s3Client.AbortMultipartUpload(ctx, s3Key, uploadID)
		s.logger.WithError(err).Error("Failed to generate presigned URLs")
		return nil, status.Errorf(codes.Internal, "failed to generate upload URLs: %v", err)
	}

	// Extract format from filename extension (e.g., "video.mp4" → "mp4")
	vodFormat := strings.TrimPrefix(filepath.Ext(req.Filename), ".")
	if vodFormat == "" {
		// Abort the upload - we need a file extension to determine format
		_ = s.s3Client.AbortMultipartUpload(ctx, s3Key, uploadID)
		return nil, status.Errorf(codes.InvalidArgument, "filename must have an extension to determine format")
	}

	// Store the artifact row, its VOD metadata, and the STATUS_REQUESTED lifecycle event in ONE
	// transaction (durable outbox). The S3 multipart upload was created above as an external side
	// effect; if this transaction fails we ABORT that upload so no orphaned multipart lingers, then
	// return an error — we never return success with half-written rows or a missing lifecycle event.
	// storage_cluster_id is set to the resolver-chosen cluster when it differs from the request's
	// cluster_id (origin); when they match the column stays NULL to preserve the prior
	// origin-as-storage semantic.
	storageClusterArg := sql.NullString{}
	if storageCluster != "" && storageCluster != req.GetClusterId() {
		storageClusterArg = sql.NullString{String: storageCluster, Valid: true}
	}
	// VOD system default is infinite (Mux / Cloudflare Stream baseline).
	// Commodore-supplied retention_days takes precedence; the tier cap
	// (0=uncapped on paid, finite on Free) clamps the result.
	vodRetentionUntil := resolveArtifactInitialRetention(ctx, s.purserClient, req.TenantId, req.RetentionDays, 0 /* infinite VOD default */, s.logger)
	uploadExpiresAt := time.Now().Add(2 * time.Hour)

	// The REQUESTED event MUST carry the tenant id: the outbox rejects an empty-tenant lifecycle
	// event (ErrLifecycleMissingTenant), which would roll this transaction back.
	vodData := &ipcpb.VodLifecycleData{
		Status:      ipcpb.VodLifecycleData_STATUS_REQUESTED,
		VodHash:     artifactHash,
		UploadId:    &uploadID,
		Filename:    &req.Filename,
		ContentType: &contentType,
		SizeBytes:   proto.Uint64(uint64(req.SizeBytes)),
		TenantId:    &req.TenantId,
		StartedAt:   proto.Int64(time.Now().Unix()),
	}
	if req.UserId != "" {
		vodData.UserId = &req.UserId
	}
	if cid := req.GetClusterId(); cid != "" {
		vodData.OriginClusterId = &cid
		vodData.ServingClusterId = &cid
	}

	if txErr := s.withArtifactLifecycleTx(ctx, func(tx *sql.Tx) error {
		if _, execErr := tx.ExecContext(ctx, `
			INSERT INTO foghorn.artifacts (
				artifact_hash, artifact_type, internal_name,
				tenant_id, user_id, status,
				sync_status, size_bytes, s3_url, format, origin_cluster_id, storage_cluster_id, retention_until, created_at, updated_at
			)
			VALUES ($1, 'vod', $2, NULLIF($3, '')::uuid, NULLIF($4, '')::uuid, 'uploading',
			        'in_progress', $5, $6, $7, $8, $9, $10, NOW(), NOW())
		`, artifactHash, req.GetInternalName(), req.TenantId, req.UserId, req.SizeBytes, s.s3Client.BuildS3URL(s3Key), vodFormat, req.GetClusterId(), storageClusterArg, vodRetentionUntil); execErr != nil {
			return execErr
		}
		if _, execErr := tx.ExecContext(ctx, `
			INSERT INTO foghorn.vod_metadata (
				artifact_hash, filename, title, description, content_type,
				s3_upload_id, s3_key, upload_expires_at, total_parts, created_at, updated_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW(), NOW())
		`, artifactHash, req.Filename, req.GetTitle(), req.GetDescription(), contentType, uploadID, s3Key, uploadExpiresAt, partCount); execErr != nil {
			return execErr
		}
		// Commit the command ledger in the SAME tx as the artifact row so the
		// 'committed' outcome is durable together with the VOD.
		if cmdErr := recordCreationCommandCommitted(ctx, tx, req.GetRequestId(), req.GetTenantId(), "vod", artifactHash); cmdErr != nil {
			return cmdErr
		}
		return artifactoutbox.EnqueueVodLifecycleTx(ctx, tx, vodData)
	}); txErr != nil {
		if abortErr := s.s3Client.AbortMultipartUpload(ctx, s3Key, uploadID); abortErr != nil {
			s.logger.WithError(abortErr).WithField("upload_id", uploadID).Warn("Failed to abort multipart upload after create transaction failure")
		}
		s.logger.WithError(txErr).WithField("artifact_hash", artifactHash).Error("Failed to persist VOD upload create (artifact+metadata+requested event)")
		return nil, status.Error(codes.Internal, "failed to record upload")
	}
	// The artifact row and its 'committed' ledger row committed together; the deferred
	// finalizer must not now record a contradictory 'rejected'.
	prog.committed = true

	s.logger.WithFields(logging.Fields{
		"artifact_hash": artifactHash,
		"upload_id":     uploadID,
		"tenant_id":     req.TenantId,
		"filename":      req.Filename,
		"size_bytes":    req.SizeBytes,
		"part_count":    partCount,
		"part_size":     partSize,
	}).Info("Created VOD multipart upload")

	// The storage cluster is written to foghorn.artifacts (authoritative) at registration; the
	// artifact reconciler projects storage_cluster_id onto the Commodore catalog with its
	// revision guard, so no unguarded fire-and-forget projection is done here.

	// Convert storage.UploadPart to proto
	protoParts := make([]*sharedpb.VodUploadPart, len(parts))
	for i, p := range parts {
		protoParts[i] = &sharedpb.VodUploadPart{
			PartNumber:   int32(p.PartNumber),
			PresignedUrl: p.PresignedURL,
		}
	}

	return &sharedpb.CreateVodUploadResponse{
		UploadId:     uploadID,
		ArtifactId:   artifactHash,
		ArtifactHash: artifactHash,
		PartSize:     partSize,
		Parts:        protoParts,
		ExpiresAt:    timestamppb.New(uploadExpiresAt),
		PlaybackId:   req.GetPlaybackId(),
	}, nil
}

// GetVodUploadStatus reports server-authoritative state of an in-flight multipart upload.
// Used by the gateway/MCP and by the browser uploader's reload-recovery path to reconcile
// local state against what S3 has actually received.
func (s *FoghornGRPCServer) GetVodUploadStatus(ctx context.Context, req *sharedpb.GetVodUploadStatusRequest) (*sharedpb.GetVodUploadStatusResponse, error) {
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.UploadId == "" {
		return nil, status.Error(codes.InvalidArgument, "upload_id is required")
	}

	var (
		artifactHash    string
		s3Key           string
		artStatus       string
		errorMessage    sql.NullString
		retentionUntil  sql.NullTime
		uploadExpiresAt sql.NullTime
		totalParts      sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, `
			SELECT v.artifact_hash, COALESCE(v.s3_key, ''), a.status,
			       a.error_message, a.retention_until, v.upload_expires_at, v.total_parts
			FROM foghorn.vod_metadata v
			JOIN foghorn.artifacts a ON v.artifact_hash = a.artifact_hash
			WHERE v.s3_upload_id = $1 AND a.tenant_id = $2
		`, req.UploadId, req.TenantId).Scan(
		&artifactHash, &s3Key, &artStatus,
		&errorMessage, &retentionUntil, &uploadExpiresAt, &totalParts,
	)
	if errors.Is(err, sql.ErrNoRows) {
		// Wrong-tenant or missing upload — collapse both into NotFound to avoid existence leak.
		return nil, status.Error(codes.NotFound, "upload not found")
	} else if err != nil {
		s.logger.WithError(err).Error("Failed to load upload status")
		return nil, status.Error(codes.Internal, "failed to load upload status")
	}

	resp := &sharedpb.GetVodUploadStatusResponse{
		UploadId:     req.UploadId,
		State:        mapArtifactStatusToVodStatus(artStatus),
		ArtifactHash: artifactHash,
	}
	if errorMessage.Valid && errorMessage.String != "" {
		resp.LastErrorCode = vodUploadLastErrorCode(resp.State, errorMessage.String)
	}
	if retentionUntil.Valid {
		resp.RetentionUntil = timestamppb.New(retentionUntil.Time)
	}
	if uploadExpiresAt.Valid {
		resp.ExpiresAt = timestamppb.New(uploadExpiresAt.Time)
	}

	// Multipart-complete uploads report stored object metadata, not S3 part state.
	switch resp.State {
	case sharedpb.VodStatus_VOD_STATUS_PROCESSING,
		sharedpb.VodStatus_VOD_STATUS_READY,
		sharedpb.VodStatus_VOD_STATUS_FAILED,
		sharedpb.VodStatus_VOD_STATUS_DELETED:
		return resp, nil
	}

	// Expired session: report EXPIRED without paying for a ListParts call.
	if uploadExpiresAt.Valid && time.Now().After(uploadExpiresAt.Time) {
		resp.State = sharedpb.VodStatus_VOD_STATUS_EXPIRED
		resp.LastErrorCode = "upload_expired"
		return resp, nil
	}

	// Live session: reconcile against S3.
	if s.s3Client == nil {
		return resp, nil
	}
	uploaded, err := s.s3Client.ListUploadedParts(ctx, s3Key, req.UploadId)
	if err != nil {
		s.logger.WithError(err).Warn("ListUploadedParts failed; returning state without reconciliation")
		resp.LastErrorCode = "storage_reconciliation_failed"
		return resp, nil
	}
	resp.UploadedParts = make([]*sharedpb.VodUploadedPart, 0, len(uploaded))
	for _, p := range uploaded {
		resp.UploadedParts = append(resp.UploadedParts, &sharedpb.VodUploadedPart{
			PartNumber: int32(p.PartNumber),
			Etag:       p.ETag,
			SizeBytes:  p.SizeBytes,
		})
	}
	if totalParts.Valid {
		missing := storage.MissingPartNumbers(uploaded, int(totalParts.Int64))
		resp.MissingParts = make([]int32, 0, len(missing))
		for _, m := range missing {
			resp.MissingParts = append(resp.MissingParts, int32(m))
		}
	}
	return resp, nil
}

// mapArtifactStatusToVodStatus maps the foghorn.artifacts.status string column to the
// VodStatus enum surfaced to clients. Unknown/empty maps to UNSPECIFIED.
func mapArtifactStatusToVodStatus(s string) sharedpb.VodStatus {
	switch s {
	case "uploading", "requested":
		return sharedpb.VodStatus_VOD_STATUS_UPLOADING
	case "processing":
		return sharedpb.VodStatus_VOD_STATUS_PROCESSING
	case "completed", "complete", "done", "ready", "synced":
		return sharedpb.VodStatus_VOD_STATUS_READY
	case "failed":
		return sharedpb.VodStatus_VOD_STATUS_FAILED
	case "deleted":
		return sharedpb.VodStatus_VOD_STATUS_DELETED
	default:
		return sharedpb.VodStatus_VOD_STATUS_UNSPECIFIED
	}
}

func vodUploadLastErrorCode(state sharedpb.VodStatus, errorMessage string) string {
	if errorMessage == "" {
		return ""
	}
	switch state {
	case sharedpb.VodStatus_VOD_STATUS_FAILED:
		return "processing_failed"
	case sharedpb.VodStatus_VOD_STATUS_DELETED:
		return "deleted"
	default:
		return "artifact_error"
	}
}

// isNoSuchUploadError reports whether an S3 error is a genuine, SDK-TYPED "the multipart upload id no
// longer exists" signal — a prior attempt already completed (or aborted) it. Classification is by TYPE,
// never by substring: the aws-sdk-go-v2 layer wraps the API error with %w, so a real NoSuchUpload is an
// *types.NoSuchUpload (which also satisfies smithy.APIError with code "NoSuchUpload"). An unrelated
// provider error (AccessDenied, NoSuchBucket, ...) whose message merely contains "does not exist" is NOT
// a gone upload and must not be mistaken for one — treating it as gone would falsely reconcile/converge.
func isNoSuchUploadError(err error) bool {
	if err == nil {
		return false
	}
	var noSuch *types.NoSuchUpload
	if errors.As(err, &noSuch) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "NoSuchUpload"
	}
	return false
}

// s3CompletionClass classifies a CompleteMultipartUpload error so CompleteVodUpload can distinguish a
// definite failure from cases where the object may already be committed on S3.
type s3CompletionClass int

const (
	// s3CompletionDefiniteFailure: a permanent, client-side failure (e.g. 4xx AccessDenied / malformed
	// request). The object did not land, so marking the artifact 'failed' is safe.
	s3CompletionDefiniteFailure s3CompletionClass = iota
	// s3CompletionMaybeCompleted: NoSuchUpload — the multipart id is gone because a prior attempt
	// already completed (object present) or aborted (object absent) it. Probe Exists to decide.
	s3CompletionMaybeCompleted
	// s3CompletionAmbiguous: a transport/5xx failure (timeout, deadline, connection reset, HTTP 5xx).
	// S3 MAY have committed the object even though the client saw an error. Probe Exists; if that is
	// inconclusive (object absent or the probe itself errored) leave the row 'completing' for later
	// reconciliation rather than recording a false 'failed'.
	s3CompletionAmbiguous
)

// classifyS3CompletionError classifies a CompleteMultipartUpload error by TYPE (AWS SDK typed errors /
// transport error types), not by string matching, so an ambiguous transport or 5xx failure — which
// may have committed the object server-side — is never treated as a definite failure that would
// strand a completed object as a 'failed' artifact.
func classifyS3CompletionError(err error) s3CompletionClass {
	if err == nil {
		return s3CompletionDefiniteFailure
	}
	// NoSuchUpload (typed): the multipart id is gone (prior attempt completed or aborted it).
	if isNoSuchUploadError(err) {
		return s3CompletionMaybeCompleted
	}
	// Ambiguous transport failures: the request may have reached S3 and been applied before the client
	// saw the error.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return s3CompletionAmbiguous
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return s3CompletionAmbiguous
	}
	// Smithy typed API error: a 5xx server fault is ambiguous (the write may have landed); a 4xx client
	// fault is a definite, permanent failure.
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		if apiErr.ErrorFault() == smithy.FaultServer {
			return s3CompletionAmbiguous
		}
		return s3CompletionDefiniteFailure
	}
	// Opaque transport error with no typed signal (bare "connection reset" / "broken pipe" / EOF
	// strings some SDK/proxy layers surface): prefer leaving 'completing' over a false 'failed'.
	if isAmbiguousTransportError(err) {
		return s3CompletionAmbiguous
	}
	return s3CompletionDefiniteFailure
}

// isAmbiguousTransportError is the string-based fallback for transport failures that arrive without a
// typed net.Error / smithy.APIError signal. Conservative on purpose: a false ambiguous only delays a
// genuine failure until the recovery scan confirms the object absent, whereas a false definite-failure
// permanently strands an already-committed object.
func isAmbiguousTransportError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, frag := range []string{
		"connection reset",
		"connection refused",
		"broken pipe",
		"unexpected eof",
		"i/o timeout",
		"timeout",
		"deadline exceeded",
		"tls handshake",
		"no such host",
		"server closed",
		"reset by peer",
	} {
		if strings.Contains(msg, frag) {
			return true
		}
	}
	return false
}

// vodPartsMatchContract reports whether the retry's part set is identical to the persisted completion
// contract's (same count, same {part_number, etag} pairs), independent of request ordering. A mismatch
// means the retry would complete a DIFFERENT multipart set than the first claim persisted.
func vodPartsMatchContract(reqParts []*sharedpb.VodCompletedPart, contractParts []jobs.VodCompletionPart) bool {
	if len(reqParts) != len(contractParts) {
		return false
	}
	sorted := make([]jobs.VodCompletionPart, len(reqParts))
	for i, p := range reqParts {
		sorted[i] = jobs.VodCompletionPart{PartNumber: p.PartNumber, ETag: p.Etag}
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].PartNumber < sorted[j].PartNumber })
	for i := range sorted {
		if sorted[i].PartNumber != contractParts[i].PartNumber || sorted[i].ETag != contractParts[i].ETag {
			return false
		}
	}
	return true
}

// dvrSendClass classifies a SendDVRStart error by whether it PROVES the node did not (and will not)
// record. The send is fire-and-forget over the control stream, so almost every error is ambiguous: the
// command may have been delivered and accepted before the transport error surfaced.
type dvrSendClass int

const (
	// dvrSendRecoverable: transport/timeout/unavailable/not-connected — the node MAY have accepted the
	// start. Leave the row 'starting' with its durable dvr_start_dispatch descriptor for the recovery
	// worker to reconcile (idempotent re-dispatch, or stop+finalize past the hard grace).
	dvrSendRecoverable dvrSendClass = iota
	// dvrSendDefinitiveReject: an explicit negative response (a client-error gRPC code) proving the node
	// rejected the command outright and is NOT recording — safe to terminalize immediately.
	dvrSendDefinitiveReject
)

// classifyDVRSendError maps a SendDVRStart error to its recovery class. Only an explicit client-error
// rejection is definitive; every transport/availability failure is recoverable (ambiguous delivery).
func classifyDVRSendError(err error) dvrSendClass {
	if err == nil {
		return dvrSendRecoverable
	}
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.InvalidArgument, codes.FailedPrecondition, codes.NotFound, codes.PermissionDenied, codes.Unauthenticated:
			return dvrSendDefinitiveReject
		default:
			// Unavailable / DeadlineExceeded / Unknown / Canceled / Internal / etc.: ambiguous delivery.
			return dvrSendRecoverable
		}
	}
	// Untyped transport error: treat as ambiguous rather than strand a possibly-running recording.
	return dvrSendRecoverable
}

// CompleteVodUpload finalizes a multipart upload after all parts are uploaded
func (s *FoghornGRPCServer) CompleteVodUpload(ctx context.Context, req *sharedpb.CompleteVodUploadRequest) (*sharedpb.CompleteVodUploadResponse, error) {
	// NOTE: tenant_id validation happens at Commodore level (matches clips pattern)
	if req.UploadId == "" {
		return nil, status.Error(codes.InvalidArgument, "upload_id is required")
	}
	if len(req.Parts) == 0 {
		return nil, status.Error(codes.InvalidArgument, "parts are required")
	}
	if s.s3Client == nil {
		return nil, status.Error(codes.FailedPrecondition, "S3 storage not configured")
	}

	// Look up the upload, accepting the in-flight 'uploading' state, the transient 'completing' claim,
	// and the already-advanced 'processing' state so a retry converges idempotently instead of
	// 404-ing or re-running multipart completion. tenant_id validation happens at Commodore level.
	var artifactHash, s3Key, artifactStatus string
	var sizeBytes sql.NullInt64
	var userID sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT v.artifact_hash, v.s3_key, a.size_bytes, a.user_id, a.status
		FROM foghorn.vod_metadata v
		JOIN foghorn.artifacts a ON v.artifact_hash = a.artifact_hash
		WHERE v.s3_upload_id = $1 AND a.tenant_id = $2
		  AND a.status IN ('uploading', 'completing', 'processing')
	`, req.UploadId, req.TenantId).Scan(&artifactHash, &s3Key, &sizeBytes, &userID, &artifactStatus)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "upload not found or already completed")
	} else if err != nil {
		s.logger.WithError(err).Error("Failed to fetch upload info")
		return nil, status.Error(codes.Internal, "failed to fetch upload info")
	}

	// A retry that already advanced to 'processing' on a prior attempt converges here: the multipart
	// completion AND the durable transition both already happened, so return the asset without
	// re-running either. This is the idempotent no-op that prevents a NoSuchUpload-driven false
	// failure on the second call.
	if artifactStatus == "processing" {
		asset, lookupErr := s.lookupCompletedUploadAsset(artifactHash, false)
		if lookupErr != nil {
			s.logger.WithError(lookupErr).WithField("artifact_hash", artifactHash).Error("Failed to fetch already-processing asset on CompleteVodUpload retry")
			return &sharedpb.CompleteVodUploadResponse{Asset: &sharedpb.VodAssetInfo{
				ArtifactHash: artifactHash,
				Status:       sharedpb.VodStatus_VOD_STATUS_PROCESSING,
			}}, nil
		}
		return &sharedpb.CompleteVodUploadResponse{Asset: asset}, nil
	}

	// Build the durable multipart completion descriptor (s3 key, upload id, ordered parts) so the
	// completing-VOD recovery job can RETRY CompleteMultipartUpload after a crash that lands before the
	// call below. Parts are ordered by part number — the order S3 requires at completion.
	descriptorParts := make([]jobs.VodCompletionPart, len(req.Parts))
	for i, p := range req.Parts {
		descriptorParts[i] = jobs.VodCompletionPart{PartNumber: p.PartNumber, ETag: p.Etag}
	}
	sort.Slice(descriptorParts, func(i, j int) bool { return descriptorParts[i].PartNumber < descriptorParts[j].PartNumber })
	descriptorJSON, descriptorErr := json.Marshal(jobs.VodCompletionDescriptor{
		S3Key:    s3Key,
		UploadID: req.UploadId,
		Parts:    descriptorParts,
	})
	if descriptorErr != nil {
		s.logger.WithError(descriptorErr).WithField("artifact_hash", artifactHash).Error("Failed to encode VOD completion descriptor")
		return nil, status.Error(codes.Internal, "failed to encode upload completion descriptor")
	}

	// Claim the upload for completion AND persist the processing spec + full multipart completion
	// descriptor ATOMICALLY, in ONE transaction, BEFORE the external CompleteMultipartUpload call. Fail
	// closed: if the spec/descriptor write fails the whole tx rolls back and the claim does NOT happen, so
	// a row is NEVER left 'completing' without the persisted spec+descriptor the recovery job needs to
	// reproduce the requested outputs and retry completion. The claim is guarded on the still-'uploading'
	// state AND the matching s3_upload_id (RowsAffected). A row already 'completing' (a prior attempt that
	// crashed after this atomic claim) skips the claim and reconciles below.
	if artifactStatus == "uploading" {
		claimed := false
		claimErr := s.withArtifactLifecycleTx(ctx, func(tx *sql.Tx) error {
			claimRes, execErr := tx.ExecContext(ctx, `
				UPDATE foghorn.artifacts
				SET status = 'completing',
				    last_sync_attempt = NOW(),
				    updated_at = NOW()
				WHERE artifact_hash = $1 AND tenant_id = $2 AND status = 'uploading'
				  AND artifact_hash IN (
				      SELECT artifact_hash FROM foghorn.vod_metadata WHERE s3_upload_id = $3
				  )
			`, artifactHash, req.TenantId, req.UploadId)
			if execErr != nil {
				return execErr
			}
			affected, raErr := claimRes.RowsAffected()
			if raErr != nil {
				return raErr
			}
			if affected == 0 {
				// A concurrent caller claimed/advanced/aborted this upload; commit an empty tx and skip persist.
				claimed = false
				return nil
			}
			// COALESCE preserves a spec captured by a prior attempt when a retry arrives without one.
			if _, persistErr := tx.ExecContext(ctx, `
				UPDATE foghorn.vod_metadata
				   SET processes_json = COALESCE(NULLIF($2, ''), processes_json),
				       vod_completion_descriptor = $4::jsonb,
				       updated_at = NOW()
				 WHERE artifact_hash = $1 AND s3_upload_id = $3
			`, artifactHash, req.GetProcessesJson(), req.UploadId, string(descriptorJSON)); persistErr != nil {
				return persistErr
			}
			claimed = true
			return nil
		})
		if claimErr != nil {
			s.logger.WithError(claimErr).WithField("artifact_hash", artifactHash).Error("Failed to atomically claim VOD upload and persist completion descriptor")
			return nil, status.Error(codes.Internal, "failed to claim upload for completion")
		}
		if !claimed {
			// The claim matched no row (concurrent claim/advance/abort). Since the claim failed closed, the
			// descriptor was NOT persisted for us either — do not proceed to complete.
			return nil, status.Error(codes.FailedPrecondition, "upload already being completed or no longer pending")
		}
	}

	// The row is now claimed ('completing'), which means the durable completion contract — the multipart
	// upload id, the ordered part set, and the requested processing spec — is persisted on vod_metadata.
	// Load it and treat it as AUTHORITATIVE for the rest of this call. Only the first claim (from
	// 'uploading') wrote the contract; a retry that arrives while the row is already 'completing' MUST
	// converge the SAME multipart part set and dispatch the SAME requested outputs the first claim
	// persisted — never the retry request's parts/spec, which a concurrent or stale caller could have
	// diverged into a DIFFERENT object or output set.
	var persistedDescriptorJSON, authoritativeProcessesJSON string
	if scanErr := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(vod_completion_descriptor::text, ''), COALESCE(processes_json, '')
		  FROM foghorn.vod_metadata WHERE artifact_hash = $1
	`, artifactHash).Scan(&persistedDescriptorJSON, &authoritativeProcessesJSON); scanErr != nil {
		s.logger.WithError(scanErr).WithField("artifact_hash", artifactHash).Error("Failed to load persisted VOD completion contract")
		return nil, status.Error(codes.Internal, "failed to load upload completion contract")
	}
	var contract jobs.VodCompletionDescriptor
	if persistedDescriptorJSON == "" || json.Unmarshal([]byte(persistedDescriptorJSON), &contract) != nil || contract.UploadID == "" || len(contract.Parts) == 0 {
		// A claimed row must carry a decodable descriptor; without it we cannot safely complete a
		// deterministic part set, so fail rather than fall back to the retry request's parts.
		s.logger.WithField("artifact_hash", artifactHash).Error("Claimed VOD upload has no usable completion contract")
		return nil, status.Error(codes.Internal, "upload completion contract missing or unusable")
	}

	// Reject a retry whose upload id / parts / processing spec DIVERGE from the persisted contract rather
	// than completing a different multipart set or changing the requested outputs. An empty spec on the
	// retry is an idempotent replay (no spec re-sent), not a divergence.
	if req.UploadId != contract.UploadID || !vodPartsMatchContract(req.Parts, contract.Parts) {
		s.logger.WithFields(logging.Fields{
			"artifact_hash":      artifactHash,
			"req_upload_id":      req.UploadId,
			"contract_upload_id": contract.UploadID,
		}).Warn("CompleteVodUpload retry diverges from persisted completion contract; rejecting")
		return nil, status.Error(codes.FailedPrecondition, "upload completion request does not match the persisted completion contract")
	}
	if reqSpec := req.GetProcessesJson(); reqSpec != "" && authoritativeProcessesJSON != "" && reqSpec != authoritativeProcessesJSON {
		s.logger.WithField("artifact_hash", artifactHash).Warn("CompleteVodUpload retry processing spec diverges from persisted contract; rejecting")
		return nil, status.Error(codes.FailedPrecondition, "upload processing spec does not match the persisted completion contract")
	}

	// Convert the CONTRACT's parts (already ordered by part number at persist time) to storage parts, so
	// completion always converges the persisted multipart set regardless of the retry request's ordering.
	storageParts := make([]storage.CompletedPart, len(contract.Parts))
	for i, p := range contract.Parts {
		storageParts[i] = storage.CompletedPart{
			PartNumber: int(p.PartNumber),
			ETag:       p.ETag,
		}
	}

	// Complete the S3 multipart upload. Classify any error by TYPE (classifyS3CompletionError):
	//   - NoSuchUpload: a prior attempt completed (object present) or aborted (object absent) it.
	//   - Ambiguous (timeout / connection reset / 5xx): S3 MAY have committed the object anyway.
	// For BOTH we probe Exists before deciding, so we never record 'failed' for an object that landed:
	//   * object present               -> converge to 'processing' (idempotent, no re-completion)
	//   * object absent + definite fail -> mark 'failed'
	//   * object absent + ambiguous, or the Exists probe itself errors -> leave the row 'completing'
	//     for later reconciliation (client retry OR the CompletingVodRecoveryJob), never 'failed'.
	completeErr := s.s3Client.CompleteMultipartUpload(ctx, s3Key, contract.UploadID, storageParts)
	if completeErr != nil {
		completionClass := classifyS3CompletionError(completeErr)
		genuineFailure := completionClass == s3CompletionDefiniteFailure
		if completionClass == s3CompletionMaybeCompleted || completionClass == s3CompletionAmbiguous {
			exists, existsErr := s.s3Client.Exists(ctx, s3Key)
			if existsErr != nil {
				// Can't determine whether the object landed; leave the row 'completing' for a later
				// retry rather than recording a false failure OR a false success.
				s.logger.WithError(existsErr).WithFields(logging.Fields{
					"artifact_hash":    artifactHash,
					"upload_id":        req.UploadId,
					"s3_key":           s3Key,
					"completion_class": completionClass,
				}).Error("VOD completion errored and object-existence check failed; leaving 'completing' for reconciliation")
				return nil, status.Errorf(codes.Internal, "failed to reconcile upload completion: %v", existsErr)
			}
			if exists {
				// The final object is present (a prior attempt completed it, or an ambiguous transport
				// error masked a server-side success). Do NOT re-complete — converge idempotently.
				genuineFailure = false
				s.logger.WithFields(logging.Fields{
					"artifact_hash":    artifactHash,
					"upload_id":        req.UploadId,
					"s3_key":           s3Key,
					"completion_class": completionClass,
				}).Info("VOD object present despite completion error; converging to processing")
			} else if completionClass == s3CompletionAmbiguous {
				// Object absent AND the error was ambiguous: S3 may still be finalizing, or the write
				// never landed. Do NOT mark 'failed' — leave 'completing' for the recovery scan to
				// converge (if the object appears) or fail after a grace period.
				s.logger.WithError(completeErr).WithFields(logging.Fields{
					"artifact_hash": artifactHash,
					"upload_id":     req.UploadId,
					"s3_key":        s3Key,
				}).Error("Ambiguous VOD completion error and object absent; leaving 'completing' for reconciliation")
				return nil, status.Errorf(codes.Internal, "ambiguous upload completion; left for reconciliation: %v", completeErr)
			}
			// Otherwise: NoSuchUpload + object absent -> the multipart was aborted and nothing landed;
			// genuineFailure stays true and we mark 'failed' below.
		}
		if genuineFailure {
			s.logger.WithError(completeErr).Error("Failed to complete S3 multipart upload")
			// Commit the FAILED status and its lifecycle event in ONE transaction (durable outbox), so
			// the failure history can't be lost by a nil client, a crash, or a fire-and-forget enqueue.
			errMsg := fmt.Sprintf("S3 upload failed: %v", completeErr)
			vodData := &ipcpb.VodLifecycleData{
				Status:      ipcpb.VodLifecycleData_STATUS_FAILED,
				VodHash:     artifactHash,
				UploadId:    &req.UploadId,
				Error:       &errMsg,
				TenantId:    &req.TenantId,
				CompletedAt: proto.Int64(time.Now().Unix()),
			}
			if userID.Valid && userID.String != "" {
				vodData.UserId = &userID.String
			}
			if sizeBytes.Valid && sizeBytes.Int64 > 0 {
				vodData.SizeBytes = proto.Uint64(uint64(sizeBytes.Int64))
			}
			if txErr := s.withArtifactLifecycleTx(ctx, func(tx *sql.Tx) error {
				// Tenant-scoped + guarded: only a still-in-flight upload (uploading/completing)
				// transitions to failed. A concurrent abort/delete (status='deleted'), a completion
				// that already won (ready), or an already-advanced 'processing' row must NOT be flipped
				// back to failed, and no FAILED event is emitted if no row moved.
				res, execErr := tx.ExecContext(ctx, `
					UPDATE foghorn.artifacts
					SET status = 'failed',
					    sync_status = 'failed',
					    sync_error = $1,
					    error_message = $1,
					    last_sync_attempt = NOW(),
					    updated_at = NOW()
					WHERE artifact_hash = $2 AND tenant_id = $3
					  AND status NOT IN ('deleted', 'ready', 'failed', 'processing')
				`, errMsg, artifactHash, req.TenantId)
				if execErr != nil {
					return execErr
				}
				affected, raErr := res.RowsAffected()
				if raErr != nil {
					return raErr
				}
				if affected == 0 {
					return nil // no valid transition — don't emit a false FAILED
				}
				return artifactoutbox.EnqueueVodLifecycleTx(ctx, tx, vodData)
			}); txErr != nil {
				s.logger.WithError(txErr).WithField("artifact_hash", artifactHash).Error("Failed to commit VOD upload failure state+event")
			}
			return nil, status.Errorf(codes.Internal, "failed to complete upload: %v", completeErr)
		}
	}

	// Advance the upload to 'processing' and record the PROCESSING lifecycle event in ONE
	// transaction (durable outbox, tenant-scoped), so the state change and its event commit
	// atomically. The outbox worker — not decklogClient — owns Decklog delivery.
	s3URL := s.s3Client.BuildS3URL(s3Key)
	processingData := &ipcpb.VodLifecycleData{
		Status:      ipcpb.VodLifecycleData_STATUS_PROCESSING,
		VodHash:     artifactHash,
		UploadId:    &req.UploadId,
		S3Url:       &s3URL,
		TenantId:    &req.TenantId,
		CompletedAt: proto.Int64(time.Now().Unix()),
	}
	if userID.Valid && userID.String != "" {
		processingData.UserId = &userID.String
	}
	if sizeBytes.Valid && sizeBytes.Int64 > 0 {
		processingData.SizeBytes = proto.Uint64(uint64(sizeBytes.Int64))
	}
	// Guarded + tenant-scoped: only a row we successfully CLAIMED ('completing') under THIS multipart
	// upload id may advance to 'processing'. A concurrent abort/delete (or a mismatched upload id)
	// leaves 0 rows affected, so we must NOT dispatch processing or overwrite a terminal state.
	noTransition := false
	if txErr := s.withArtifactLifecycleTx(ctx, func(tx *sql.Tx) error {
		res, execErr := tx.ExecContext(ctx, `
			UPDATE foghorn.artifacts
			SET status = 'processing',
			    storage_location = 's3',
			    sync_status = 'synced',
			    sync_error = NULL,
			    last_sync_attempt = NOW(),
			    frozen_at = COALESCE(frozen_at, NOW()),
			    s3_url = COALESCE(s3_url, $2),
			    -- The multipart upload landed on THIS cell's local S3 backend; persist stable billing attribution.
			    durable_backend_local = true,
			    -- Populate the authoritative object pointer from the recorded multipart key so reads/deletion
			    -- resolve active_object_key uniformly (a direct upload has no freeze candidate; the key is the
			    -- multipart object).
			    active_object_key = COALESCE(active_object_key, (SELECT s3_key FROM foghorn.vod_metadata WHERE artifact_hash = foghorn.artifacts.artifact_hash)),
			    updated_at = NOW()
			WHERE artifact_hash = $1 AND tenant_id = $3
			  AND status = 'completing'
			  AND artifact_hash IN (
			      SELECT artifact_hash FROM foghorn.vod_metadata WHERE s3_upload_id = $4
			  )
		`, artifactHash, s3URL, req.TenantId, contract.UploadID)
		if execErr != nil {
			return execErr
		}
		affected, raErr := res.RowsAffected()
		if raErr != nil {
			return raErr
		}
		if affected == 0 {
			noTransition = true
			return nil // nothing valid to transition — commit an empty tx, emit no event
		}
		// Insert the processing job on THIS transaction so the 'completing'->'processing' state, its
		// PROCESSING lifecycle event, and the job that actually drives processing commit together or
		// not at all. A job-insert failure rolls back the transition and the event, so the row can
		// never end up 'processing' with no job (which the scanner — selecting only 'completing' —
		// would never revisit). InsertProcessingJobWithSourceParamsTx composes with the outer tx via a
		// transaction-scoped advisory lock and dedups to any existing active job for idempotent retries.
		if _, jobErr := jobs.InsertProcessingJobWithSourceParamsTx(ctx, tx, req.TenantId, artifactHash, "process", nil, authoritativeProcessesJSON, "", nil, ""); jobErr != nil {
			return jobErr
		}
		return artifactoutbox.EnqueueVodLifecycleTx(ctx, tx, processingData)
	}); txErr != nil {
		// DIVERGENCE: the S3 multipart upload is complete (fresh completion or reconciled via Exists),
		// but the durable 'processing' transition + event + job did NOT commit. The row stays
		// 'completing', so a retry re-enters this RPC and converges. Surface the error at ERROR with
		// enough identity to reconcile (S3 done, PG not) instead of silently dispatching processing.
		s.logger.WithError(txErr).WithFields(logging.Fields{
			"artifact_hash": artifactHash,
			"upload_id":     req.UploadId,
			"tenant_id":     req.TenantId,
			"s3_key":        s3Key,
		}).Error("VOD S3 upload completed but processing state+event failed to commit; reconcile needed")
		return nil, status.Errorf(codes.Internal, "upload completed in storage but failed to record processing state: %v", txErr)
	}
	if noTransition {
		// The claimed row left 'completing' under us. If a concurrent caller already advanced it to
		// 'processing', converge idempotently and return the asset; otherwise a concurrent
		// abort/delete moved it terminal — do not resurrect it back to 'processing'.
		var curStatus string
		if scanErr := s.db.QueryRowContext(ctx, `
			SELECT status FROM foghorn.artifacts WHERE artifact_hash = $1 AND tenant_id = $2
		`, artifactHash, req.TenantId).Scan(&curStatus); scanErr == nil && curStatus == "processing" {
			s.logger.WithFields(logging.Fields{
				"artifact_hash": artifactHash,
				"upload_id":     req.UploadId,
				"tenant_id":     req.TenantId,
			}).Info("VOD upload already advanced to processing by a concurrent completion; converging")
			asset, lookupErr := s.lookupCompletedUploadAsset(artifactHash, false)
			if lookupErr != nil {
				// State is known-good (row is 'processing'); a failed asset re-read still returns the
				// durable PROCESSING status rather than a spurious error.
				s.logger.WithError(lookupErr).WithField("artifact_hash", artifactHash).Warn("Converged VOD upload to processing but asset re-read failed; returning PROCESSING")
				return &sharedpb.CompleteVodUploadResponse{Asset: &sharedpb.VodAssetInfo{
					ArtifactHash: artifactHash,
					Status:       sharedpb.VodStatus_VOD_STATUS_PROCESSING,
				}}, nil
			}
			return &sharedpb.CompleteVodUploadResponse{Asset: asset}, nil
		}
		s.logger.WithFields(logging.Fields{
			"artifact_hash": artifactHash,
			"upload_id":     req.UploadId,
			"tenant_id":     req.TenantId,
		}).Warn("VOD upload no longer pending under this upload id; skipping processing dispatch")
		return nil, status.Error(codes.FailedPrecondition, "upload already aborted, deleted, or completed")
	}

	// The durable transition, its PROCESSING event, AND the processing job all committed in the tx
	// above. The ...Tx job insert intentionally does not notify (the row is not durable until commit),
	// so this is the notify site that wakes local dispatchers.
	jobs.NotifyProcessingJobQueued()

	s.logger.WithFields(logging.Fields{
		"artifact_hash": artifactHash,
		"upload_id":     req.UploadId,
		"tenant_id":     req.TenantId,
		"parts":         len(req.Parts),
	}).Info("Completed VOD multipart upload, starting processing")

	// Fetch and return the asset. The processing job committed atomically with the 'processing'
	// transition above, so there is no "processing without a job" failure mode to reflect here.
	asset, err := s.lookupCompletedUploadAsset(artifactHash, false)
	if err != nil {
		s.logger.WithError(err).Error("Failed to fetch asset after upload completion")
		return &sharedpb.CompleteVodUploadResponse{Asset: &sharedpb.VodAssetInfo{
			ArtifactHash: artifactHash,
			Status:       sharedpb.VodStatus_VOD_STATUS_PROCESSING,
		}}, nil
	}

	return &sharedpb.CompleteVodUploadResponse{Asset: asset}, nil
}

// AbortVodUpload cancels an in-progress multipart upload
func (s *FoghornGRPCServer) AbortVodUpload(ctx context.Context, req *sharedpb.AbortVodUploadRequest) (*sharedpb.AbortVodUploadResponse, error) {
	// NOTE: tenant_id validation happens at Commodore level (matches clips pattern)
	if req.UploadId == "" {
		return nil, status.Error(codes.InvalidArgument, "upload_id is required")
	}
	if s.s3Client == nil {
		return nil, status.Error(codes.FailedPrecondition, "S3 storage not configured")
	}

	// Get artifact info by upload_id
	// NOTE: tenant_id validation happens at Commodore level (matches clips pattern)
	var artifactHash, s3Key string
	var userID sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT v.artifact_hash, v.s3_key, a.user_id
		FROM foghorn.vod_metadata v
		JOIN foghorn.artifacts a ON v.artifact_hash = a.artifact_hash
		WHERE v.s3_upload_id = $1 AND a.status = 'uploading' AND a.tenant_id = $2
	`, req.UploadId, req.TenantId).Scan(&artifactHash, &s3Key, &userID)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "upload not found or already completed")
	} else if err != nil {
		s.logger.WithError(err).Error("Failed to fetch upload info")
		return nil, status.Error(codes.Internal, "failed to fetch upload info")
	}

	// Claim ownership of the abort with a GUARDED transition 'uploading'->'aborting' (tenant-scoped,
	// RowsAffected checked) BEFORE any S3 call. If this matches 0 rows a concurrent completion (or another
	// abort) already moved the row out of 'uploading'; we MUST NOT destroy the multipart upload — return
	// honestly and leave S3 untouched. Only the winner of this claim may abort the S3 upload. vod_metadata
	// (with s3_key + s3_upload_id) is left intact until the abort finishes so jobs.AbortingVodRecoveryJob
	// can converge an interrupted abort from the durable 'aborting' row.
	claimRes, claimErr := s.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		SET status = 'aborting', updated_at = NOW()
		WHERE artifact_hash = $1 AND tenant_id = $2 AND status = 'uploading'
	`, artifactHash, req.TenantId)
	if claimErr != nil {
		s.logger.WithError(claimErr).WithField("artifact_hash", artifactHash).Error("Failed to claim VOD upload abort; not touching S3")
		return nil, status.Error(codes.Internal, "failed to claim upload abort")
	}
	claimed, claimRAErr := claimRes.RowsAffected()
	if claimRAErr != nil {
		s.logger.WithError(claimRAErr).WithField("artifact_hash", artifactHash).Error("Failed to read abort-claim RowsAffected; not touching S3")
		return nil, status.Error(codes.Internal, "failed to claim upload abort")
	}
	if claimed == 0 {
		// A concurrent completion or abort already moved the row off 'uploading'; the winner owns the
		// multipart upload. Do NOT call AbortMultipartUpload — leave S3 untouched.
		s.logger.WithFields(logging.Fields{
			"artifact_hash": artifactHash,
			"upload_id":     req.UploadId,
			"tenant_id":     req.TenantId,
		}).Info("VOD upload abort raced a concurrent completion/abort; leaving S3 untouched")
		return nil, status.Error(codes.FailedPrecondition, "upload is no longer in 'uploading' state; abort raced a concurrent completion")
	}

	// We own the abort ('aborting' claimed). Destroy the S3 multipart upload idempotently: a NoSuchUpload
	// / already-aborted result means a prior attempt already tore it down and is success. Any other error
	// leaves the row 'aborting' for jobs.AbortingVodRecoveryJob to re-run — do not delete metadata or emit
	// DELETED yet.
	if abortErr := s.s3Client.AbortMultipartUpload(ctx, s3Key, req.UploadId); abortErr != nil && !isNoSuchUploadError(abortErr) {
		s.logger.WithError(abortErr).WithFields(logging.Fields{
			"artifact_hash": artifactHash,
			"upload_id":     req.UploadId,
			"s3_key":        s3Key,
		}).Warn("Failed to abort S3 multipart upload; leaving 'aborting' for recovery")
		return nil, status.Error(codes.Internal, "failed to abort S3 multipart upload; left for recovery")
	}

	// S3 upload destroyed (or already gone). Transition 'aborting'->'deleted', delete metadata, and record
	// the DELETED lifecycle event in ONE transaction (durable outbox). The guard on status='aborting'
	// makes this idempotent against jobs.AbortingVodRecoveryJob running the same convergence.
	vodData := &ipcpb.VodLifecycleData{
		Status:      ipcpb.VodLifecycleData_STATUS_DELETED,
		VodHash:     artifactHash,
		UploadId:    &req.UploadId,
		TenantId:    &req.TenantId,
		CompletedAt: proto.Int64(time.Now().Unix()),
	}
	if userID.Valid && userID.String != "" {
		vodData.UserId = &userID.String
	}
	if err = s.withArtifactLifecycleTx(ctx, func(tx *sql.Tx) error {
		res, execErr := tx.ExecContext(ctx, `
			UPDATE foghorn.artifacts
			SET status = 'deleted', updated_at = NOW()
			WHERE artifact_hash = $1 AND tenant_id = $2 AND status = 'aborting'
		`, artifactHash, req.TenantId)
		if execErr != nil {
			return execErr
		}
		affected, raErr := res.RowsAffected()
		if raErr != nil {
			return raErr
		}
		if affected == 0 {
			return nil // the recovery worker already converged this 'aborting' row
		}
		if _, execErr := tx.ExecContext(ctx, `DELETE FROM foghorn.vod_metadata WHERE artifact_hash = $1`, artifactHash); execErr != nil {
			return execErr
		}
		return artifactoutbox.EnqueueVodLifecycleTx(ctx, tx, vodData)
	}); err != nil {
		s.logger.WithError(err).Error("Failed to finalize aborted artifact")
		return nil, status.Error(codes.Internal, "failed to clean up aborted upload")
	}

	s.logger.WithFields(logging.Fields{
		"artifact_hash": artifactHash,
		"upload_id":     req.UploadId,
		"tenant_id":     req.TenantId,
	}).Info("Aborted VOD multipart upload")

	return &sharedpb.AbortVodUploadResponse{
		Success: true,
		Message: "upload aborted successfully",
	}, nil
}

// DeleteVodAsset deletes a VOD asset
func (s *FoghornGRPCServer) DeleteVodAsset(ctx context.Context, req *sharedpb.DeleteVodAssetRequest) (*sharedpb.DeleteVodAssetResponse, error) {
	// NOTE: tenant_id validation happens at Commodore level (matches clips pattern)
	if req.ArtifactHash == "" {
		return nil, status.Error(codes.InvalidArgument, "artifact_hash is required")
	}

	// Check current status
	// NOTE: tenant_id validation happens at Commodore level (matches clips pattern)
	var (
		currentStatus    string
		s3Key            string
		s3URL            sql.NullString
		formatStr        sql.NullString
		sizeBytes        sql.NullInt64
		retentionUntil   sql.NullTime
		userID           sql.NullString
		storageClusterID sql.NullString
		originClusterID  sql.NullString
		originType       sql.NullString
		activeObjectKey  sql.NullString
		activeDtshKey    sql.NullString
		syncObjectKey    sql.NullString
		durableLocal     sql.NullBool
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT a.status, COALESCE(v.s3_key, ''), a.s3_url, a.format,
		       a.size_bytes, a.retention_until, a.user_id,
		       a.storage_cluster_id, a.origin_cluster_id, COALESCE(a.origin_type, ''),
		       a.active_object_key, a.active_dtsh_key, a.sync_object_key, a.durable_backend_local
		FROM foghorn.artifacts a
		LEFT JOIN foghorn.vod_metadata v ON a.artifact_hash = v.artifact_hash
		WHERE a.artifact_hash = $1 AND a.artifact_type = 'vod' AND a.tenant_id = $2
	`, req.ArtifactHash, req.GetTenantId()).Scan(&currentStatus, &s3Key, &s3URL, &formatStr, &sizeBytes, &retentionUntil, &userID, &storageClusterID, &originClusterID, &originType, &activeObjectKey, &activeDtshKey, &syncObjectKey, &durableLocal)

	if errors.Is(err, sql.ErrNoRows) {
		if handled, _ := s.forwardArtifactToFederation(ctx, "delete_vod", req.ArtifactHash, req.GetTenantId(), ""); handled {
			return &sharedpb.DeleteVodAssetResponse{Success: true, Message: "VOD deleted via federation"}, nil
		}
		return nil, status.Error(codes.NotFound, "VOD asset not found")
	} else if err != nil {
		s.logger.WithError(err).Error("Failed to check VOD asset")
		return nil, status.Error(codes.Internal, "failed to check VOD asset")
	}

	// A finalized DVR chapter is stored as artifact_type='vod' with origin_type='dvr_chapter',
	// but it is owned by the parent DVR's chapter ledger (foghorn.dvr_chapters). Deleting it
	// through the generic VOD path would erase its bytes while leaving the chapter row pointing
	// at a dead artifact — orphaning the ledger. Chapter deletion must go through the
	// chapter/recording-aware path, so reject it here rather than corrupt the ledger.
	if originType.String == "dvr_chapter" {
		return nil, status.Error(codes.FailedPrecondition, "artifact is a DVR chapter; delete via the recording, not as a VOD asset")
	}

	if currentStatus == "deleted" {
		return &sharedpb.DeleteVodAssetResponse{
			Success: false,
			Message: "VOD asset is already deleted",
		}, nil
	}

	// DURABLE STATE FIRST: soft-delete the VOD and record the STATUS_DELETED lifecycle event in ONE
	// transaction (durable outbox, tenant-scoped) BEFORE removing any bytes. A byte-first delete
	// could leave a playable catalog row pointing at bytes that are already gone if the DB/outbox
	// tx then fails. The cleanup error is unknown here (cleanup runs after commit). The outbox
	// worker — not decklogClient — owns Decklog delivery.
	vodData := &ipcpb.VodLifecycleData{
		Status:      ipcpb.VodLifecycleData_STATUS_DELETED,
		VodHash:     req.ArtifactHash,
		TenantId:    &req.TenantId,
		CompletedAt: proto.Int64(time.Now().Unix()),
	}
	if userID.Valid && userID.String != "" {
		vodData.UserId = &userID.String
	}
	if sizeBytes.Valid && sizeBytes.Int64 > 0 {
		sb := uint64(sizeBytes.Int64)
		vodData.SizeBytes = &sb
	}
	if retentionUntil.Valid {
		exp := retentionUntil.Time.Unix()
		vodData.ExpiresAt = &exp
	}
	transitioned := false
	if err = s.withArtifactLifecycleTx(ctx, func(tx *sql.Tx) error {
		// Retire any outstanding freeze attempt on the terminal transition: a late completion for the
		// abandoned attempt then matches nothing (its request/node is gone). sync_object_key is
		// DELIBERATELY retained so the purge sweep can free an object whose PUT lands after deletion —
		// the delete has authorized-but-not-yet-landed bytes to clean up.
		res, execErr := tx.ExecContext(ctx, `
			UPDATE foghorn.artifacts
			SET status = 'deleted',
			    sync_request_id = NULL, sync_node_id = NULL,
			    updated_at = NOW()
			WHERE artifact_hash = $1 AND artifact_type = 'vod' AND tenant_id = $2
			  AND status != 'deleted'
		`, req.ArtifactHash, req.TenantId)
		if execErr != nil {
			return execErr
		}
		affected, raErr := res.RowsAffected()
		if raErr != nil {
			return raErr
		}
		if affected == 0 {
			// Already deleted (or gone) at UPDATE time — do not enqueue a second DELETED event.
			return nil
		}
		transitioned = true
		return artifactoutbox.EnqueueVodLifecycleTx(ctx, tx, vodData)
	}); err != nil {
		s.logger.WithError(err).Error("Failed to delete VOD asset")
		return nil, status.Error(codes.Internal, "failed to delete VOD asset")
	}

	// Lost a concurrent delete race (row already 'deleted' when the guarded UPDATE ran): report
	// already-deleted and do NO physical cleanup — the winning delete owns the bytes. Returning
	// here prevents duplicate DELETED analytics and byte removal for a delete this call did not
	// commit.
	if !transitioned {
		return &sharedpb.DeleteVodAssetResponse{
			Success: false,
			Message: "VOD asset is already deleted",
		}, nil
	}

	s.logger.WithFields(logging.Fields{
		"artifact_hash": req.ArtifactHash,
		"tenant_id":     req.TenantId,
	}).Info("VOD asset soft-deleted successfully")

	// Physical cleanup runs ONLY after the soft-delete committed. Failures are non-fatal: the
	// catalog is already durably deleted, so any lingering bytes are storage garbage the
	// orphan/purge job reclaims, not a correctness bug.

	// If it was still uploading, abort the multipart upload so no orphaned parts linger.
	if currentStatus == "uploading" {
		var uploadID string
		if scanErr := s.db.QueryRowContext(ctx, `
			SELECT s3_upload_id FROM foghorn.vod_metadata WHERE artifact_hash = $1
		`, req.ArtifactHash).Scan(&uploadID); scanErr != nil {
			s.logger.WithError(scanErr).WithField("artifact_hash", req.ArtifactHash).Warn("Failed to look up multipart upload id for abort, deferring to purge job")
		}
		if uploadID != "" && s3Key != "" && s.s3Client != nil {
			if abortErr := s.s3Client.AbortMultipartUpload(ctx, s3Key, uploadID); abortErr != nil {
				s.logger.WithError(abortErr).WithField("artifact_hash", req.ArtifactHash).Warn("Failed to abort multipart upload, will be retried by purge job")
			}
		}
	}

	// Send delete request to nodes that have this VOD cached
	rows, queryErr := s.db.QueryContext(ctx, `
		SELECT node_id FROM foghorn.artifact_nodes
		WHERE artifact_hash = $1 AND NOT is_orphaned
	`, req.ArtifactHash)
	if queryErr == nil {
		defer func() { _ = rows.Close() }()
		requestID := uuid.NewString()
		for rows.Next() {
			var nodeID string
			if scanErr := rows.Scan(&nodeID); scanErr != nil {
				continue
			}
			deleteReq := &ipcpb.VodDeleteRequest{
				VodHash:   req.ArtifactHash,
				RequestId: requestID,
			}
			if sendErr := control.SendVodDelete(nodeID, deleteReq); sendErr != nil {
				s.logger.WithFields(logging.Fields{
					"artifact_hash": req.ArtifactHash,
					"node_id":       nodeID,
					"error":         sendErr,
				}).Warn("Failed to send VOD delete to storage node, will be cleaned up later")
			} else {
				s.logger.WithFields(logging.Fields{
					"artifact_hash": req.ArtifactHash,
					"node_id":       nodeID,
					"request_id":    requestID,
				}).Debug("Sent VOD delete request to storage node")
			}
		}
	}

	// Delete from S3 (cross-cluster aware via the federation delete delegate). The cleaner derives
	// the target from vod_metadata.s3_key, falling back to a.s3_url and finally to the deterministic
	// BuildVodS3Key shape so VODs whose s3_key was never recorded still get cleaned. Skipped for an
	// uploading row whose multipart was just aborted. Failure marks cleanup-pending; the row is
	// already in the purge cycle for retries.
	if currentStatus != "uploading" {
		if s.artifactCleaner == nil {
			s.logger.WithField("artifact_hash", req.ArtifactHash).Warn("Artifact cleaner not wired; VOD S3 cleanup deferred to purge job")
		} else if errDelete := s.artifactCleaner.Delete(ctx, artifacts.ArtifactRef{
			Hash:                req.ArtifactHash,
			Type:                "vod",
			TenantID:            req.GetTenantId(),
			Format:              formatStr.String,
			VODS3Key:            s3Key,
			S3URL:               s3URL.String,
			StorageClusterID:    storageClusterID.String,
			OriginClusterID:     originClusterID.String,
			ActiveObjectKey:     activeObjectKey.String,
			ActiveDtshKey:       activeDtshKey.String,
			PendingObjectKey:    syncObjectKey.String,
			DurableBackendLocal: durableLocal.Bool,
		}); errDelete != nil {
			s.logger.WithFields(logging.Fields{
				"artifact_hash": req.ArtifactHash,
				"s3_key":        s3Key,
				"error":         errDelete,
			}).Warn("Failed to delete from S3, will be retried by purge job")
		}
	}

	return &sharedpb.DeleteVodAssetResponse{
		Success: true,
		Message: "VOD asset deleted successfully",
	}, nil
}

// Helper functions for VOD service

func (s *FoghornGRPCServer) getVodAssetInfo(ctx context.Context, artifactHash string) (*sharedpb.VodAssetInfo, error) {
	query := `
		SELECT a.artifact_hash, a.artifact_hash, a.status, a.size_bytes,
		       COALESCE(a.storage_location, 'pending'), COALESCE(a.s3_url, ''),
		       a.error_message, a.created_at, a.updated_at, a.retention_until,
		       COALESCE(v.filename, ''), COALESCE(v.title, ''), COALESCE(v.description, ''),
		       v.duration_ms, v.resolution, v.video_codec, v.audio_codec, v.bitrate_kbps,
		       COALESCE(v.s3_upload_id, ''), COALESCE(v.s3_key, '')
		FROM foghorn.artifacts a
		LEFT JOIN foghorn.vod_metadata v ON a.artifact_hash = v.artifact_hash
		WHERE a.artifact_hash = $1 AND a.artifact_type = 'vod' AND a.status != 'deleted'
	`
	row := s.db.QueryRowContext(ctx, query, artifactHash)
	return s.scanVodAssetRow(row)
}

func (s *FoghornGRPCServer) lookupCompletedUploadAsset(artifactHash string, pipelineFailed bool) (*sharedpb.VodAssetInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	asset, err := s.getVodAssetInfo(ctx, artifactHash)
	if err == nil {
		return asset, nil
	}
	if pipelineFailed {
		return &sharedpb.VodAssetInfo{
			ArtifactHash: artifactHash,
			Status:       sharedpb.VodStatus_VOD_STATUS_FAILED,
		}, nil
	}
	return nil, err
}

func (s *FoghornGRPCServer) scanVodAssetRow(row *sql.Row) (*sharedpb.VodAssetInfo, error) {
	var id, artifactHash, statusStr, storageLocation, s3URL, filename, title, description string
	var videoCodec, audioCodec, resolution, s3UploadID, s3Key sql.NullString
	var sizeBytes sql.NullInt64
	var durationMs, bitrateKbps sql.NullInt32
	var errorMessage sql.NullString
	var createdAt, updatedAt time.Time
	var expiresAt sql.NullTime

	err := row.Scan(
		&id, &artifactHash, &statusStr, &sizeBytes,
		&storageLocation, &s3URL, &errorMessage,
		&createdAt, &updatedAt, &expiresAt,
		&filename, &title, &description,
		&durationMs, &resolution, &videoCodec, &audioCodec, &bitrateKbps,
		&s3UploadID, &s3Key,
	)
	if err != nil {
		return nil, err
	}

	return buildVodAssetInfo(
		id, artifactHash, statusStr, storageLocation, filename, title, description,
		sizeBytes, durationMs, resolution, videoCodec, audioCodec, bitrateKbps,
		s3UploadID, s3Key, errorMessage, createdAt, updatedAt, expiresAt,
	), nil
}

func buildVodAssetInfo(
	id, artifactHash, statusStr, storageLocation, filename, title, description string,
	sizeBytes sql.NullInt64, durationMs sql.NullInt32, resolution, videoCodec, audioCodec sql.NullString,
	bitrateKbps sql.NullInt32, s3UploadID, s3Key, errorMessage sql.NullString,
	createdAt, updatedAt time.Time, expiresAt sql.NullTime,
) *sharedpb.VodAssetInfo {
	// Map status string to proto enum
	var protoStatus sharedpb.VodStatus
	switch statusStr {
	case "uploading":
		protoStatus = sharedpb.VodStatus_VOD_STATUS_UPLOADING
	case "processing":
		protoStatus = sharedpb.VodStatus_VOD_STATUS_PROCESSING
	case "completed", "complete", "done", "ready", "synced":
		protoStatus = sharedpb.VodStatus_VOD_STATUS_READY
	case "failed":
		protoStatus = sharedpb.VodStatus_VOD_STATUS_FAILED
	case "deleted":
		protoStatus = sharedpb.VodStatus_VOD_STATUS_DELETED
	default:
		protoStatus = sharedpb.VodStatus_VOD_STATUS_UNSPECIFIED
	}

	asset := &sharedpb.VodAssetInfo{
		Id:              id,
		ArtifactHash:    artifactHash,
		Title:           title,
		Description:     description,
		Filename:        filename,
		Status:          protoStatus,
		StorageLocation: storageLocation,
		CreatedAt:       timestamppb.New(createdAt),
		UpdatedAt:       timestamppb.New(updatedAt),
	}

	if sizeBytes.Valid {
		asset.SizeBytes = &sizeBytes.Int64
	}
	if durationMs.Valid {
		asset.DurationMs = &durationMs.Int32
	}
	if resolution.Valid {
		asset.Resolution = &resolution.String
	}
	if videoCodec.Valid {
		asset.VideoCodec = &videoCodec.String
	}
	if audioCodec.Valid {
		asset.AudioCodec = &audioCodec.String
	}
	if bitrateKbps.Valid {
		asset.BitrateKbps = &bitrateKbps.Int32
	}
	if s3UploadID.Valid {
		asset.S3UploadId = &s3UploadID.String
	}
	if s3Key.Valid {
		asset.S3Key = &s3Key.String
	}
	if errorMessage.Valid {
		asset.ErrorMessage = &errorMessage.String
	}
	if expiresAt.Valid {
		asset.ExpiresAt = timestamppb.New(expiresAt.Time)
	}

	return asset
}

// buildClipDeletedLifecycleData assembles the STAGE_DELETED clip event, enriching tenant/user/
// stream/timing fields from Commodore (a best-effort network lookup) when available. It performs
// no DB writes and is called BEFORE the delete transaction so the enrichment round-trip never
// holds a DB transaction open; the caller enqueues the returned value on its tx.
func (s *FoghornGRPCServer) buildClipDeletedLifecycleData(
	ctx context.Context,
	clipHash string,
	nodeID string,
	sizeBytes sql.NullInt64,
	retentionUntil sql.NullTime,
	internalName sql.NullString,
	denormTenantID sql.NullString,
	denormUserID sql.NullString,
	cleanupError string,
) *ipcpb.ClipLifecycleData {
	var (
		tenantIDStr     string
		userIDStr       string
		internalNameStr string
		streamID        string
		clipMode        *string
		startUnix       *int64
		stopUnix        *int64
		startMs         *int64
		stopMs          *int64
		durationSec     *int64
	)

	if denormTenantID.Valid {
		tenantIDStr = denormTenantID.String
	}
	if denormUserID.Valid {
		userIDStr = denormUserID.String
	}
	if internalName.Valid {
		internalNameStr = internalName.String
	}

	if control.CommodoreClient != nil {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if resp, err := control.CommodoreClient.ResolveClipHash(cctx, clipHash); err == nil && resp.Found {
			if resp.TenantId != "" {
				tenantIDStr = resp.TenantId
			}
			if resp.UserId != "" {
				userIDStr = resp.UserId
			}
			if resp.StreamInternalName != "" {
				internalNameStr = resp.StreamInternalName
			}
			if resp.StreamId != "" {
				streamID = resp.StreamId
			}
			if resp.ClipMode != "" {
				m := resp.ClipMode
				clipMode = &m
			}
			if resp.StartTime > 0 && resp.Duration > 0 {
				sMs := resp.StartTime
				eMs := resp.StartTime + resp.Duration
				sU := sMs / 1000
				eU := eMs / 1000
				dS := resp.Duration / 1000
				startMs, stopMs = &sMs, &eMs
				startUnix, stopUnix = &sU, &eU
				durationSec = &dS
			}
		}
	}

	clipData := &ipcpb.ClipLifecycleData{
		Stage:    ipcpb.ClipLifecycleData_STAGE_DELETED,
		ClipHash: clipHash,
	}
	if cleanupError != "" {
		clipData.Error = &cleanupError
	}
	if nodeID != "" {
		clipData.NodeId = &nodeID
	}
	if tenantIDStr != "" {
		clipData.TenantId = &tenantIDStr
	}
	if internalNameStr != "" {
		clipData.StreamInternalName = &internalNameStr
	}
	if streamID != "" {
		clipData.StreamId = &streamID
	}
	if userIDStr != "" {
		clipData.UserId = &userIDStr
	}
	if sizeBytes.Valid && sizeBytes.Int64 > 0 {
		sb := uint64(sizeBytes.Int64)
		clipData.SizeBytes = &sb
	}
	if retentionUntil.Valid {
		exp := retentionUntil.Time.Unix()
		clipData.ExpiresAt = &exp
	}
	clipData.ClipMode = clipMode
	clipData.StartUnix = startUnix
	clipData.StopUnix = stopUnix
	clipData.StartMs = startMs
	clipData.StopMs = stopMs
	clipData.DurationSec = durationSec

	return clipData
}

// TerminateTenantStreams stops all active streams for a suspended tenant
// Called by Purser when a tenant's prepaid balance drops below -$10
func (s *FoghornGRPCServer) TerminateTenantStreams(ctx context.Context, req *foghorncontrolpb.TerminateTenantStreamsRequest) (*foghorncontrolpb.TerminateTenantStreamsResponse, error) {
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id": req.TenantId,
		"reason":    req.Reason,
	}).Info("Terminating tenant streams due to suspension")

	// Get all active streams for this tenant from the stream state manager
	streams := s.lb.GetStreamsByTenant(req.TenantId)
	if len(streams) == 0 {
		s.logger.WithField("tenant_id", req.TenantId).Debug("No active streams to terminate")
		return &foghorncontrolpb.TerminateTenantStreamsResponse{
			StreamsTerminated:  0,
			SessionsTerminated: 0,
			StreamNames:        []string{},
		}, nil
	}

	// Group streams by node for efficient batch stop_sessions calls
	streamsByNode := make(map[string][]string)
	var allStreamNames []string
	for _, stream := range streams {
		allStreamNames = append(allStreamNames, stream.InternalName)
		// Get the node from stream instances
		instances := s.lb.GetStreamInstances(stream.InternalName)
		for nodeID := range instances {
			streamsByNode[nodeID] = append(streamsByNode[nodeID], stream.InternalName)
		}
	}

	// Send stop_sessions to each node
	sessionsTerminated := int32(0)
	for nodeID, nodeStreams := range streamsByNode {
		stopReq := &ipcpb.StopSessionsRequest{
			StreamNames: nodeStreams,
			TenantId:    req.TenantId,
			Reason:      req.Reason,
		}
		if err := control.SendStopSessions(nodeID, stopReq); err != nil {
			s.logger.WithFields(logging.Fields{
				"node_id":   nodeID,
				"tenant_id": req.TenantId,
				"error":     err,
			}).Warn("Failed to send stop_sessions to node")
			// Continue trying other nodes
		} else {
			sessionsTerminated += int32(len(nodeStreams))
		}
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":           req.TenantId,
		"streams_terminated":  len(allStreamNames),
		"sessions_terminated": sessionsTerminated,
		"stream_names":        allStreamNames,
	}).Info("Tenant stream termination completed")

	return &foghorncontrolpb.TerminateTenantStreamsResponse{
		StreamsTerminated:  int32(len(allStreamNames)),
		SessionsTerminated: sessionsTerminated,
		StreamNames:        allStreamNames,
	}, nil
}

// InvalidatePlaybackAuth sends invalidate_sessions to Helmsman nodes holding
// the listed live streams or artifacts. Called by Commodore after a playback
// policy or signing-key mutation. The re-fired USER_NEW reads the fresh policy
// and decides allow/deny per session. Empty internal_names fans out across the
// tenant's known live streams and artifact sessions.
func (s *FoghornGRPCServer) InvalidatePlaybackAuth(ctx context.Context, req *foghornpb.InvalidatePlaybackAuthRequest) (*foghornpb.InvalidatePlaybackAuthResponse, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	// bundle_revoke fast path: bump the policy-bundle cache watermark so
	// any cached bundle below bundle_min_version is rejected on its next
	// Get. Must run before per-node session-invalidation fanout — the
	// watermark guards admission and session invalidation re-fires
	// USER_NEW; both surfaces have to agree on the new minimum.
	if req.GetReason() == "bundle_revoke" && s.policyBundleCache != nil && req.GetBundleMinVersion() > 0 {
		s.policyBundleCache.BumpWatermark(req.GetTenantId(), req.GetStreamId(), req.GetBundleMinVersion())
	}

	names := s.resolvePlaybackAuthInvalidationNames(ctx, req.GetTenantId(), req.GetInternalNames())
	if len(names) == 0 {
		return &foghornpb.InvalidatePlaybackAuthResponse{}, nil
	}

	if s.cacheInvalidator != nil {
		s.cacheInvalidator.InvalidatePlaybackAuthCache(req.GetTenantId(), names)
	}

	// Group by node so each Helmsman gets a single batched call.
	streamsByNode := make(map[string][]string)
	for _, name := range names {
		instances := s.lb.GetStreamInstances(name)
		for nodeID := range instances {
			streamsByNode[nodeID] = append(streamsByNode[nodeID], name)
		}
		for nodeID := range s.artifactSessionNodes(ctx, req.GetTenantId(), name) {
			streamsByNode[nodeID] = append(streamsByNode[nodeID], name)
		}
	}

	dispatched := int32(0)
	attempted := int32(len(streamsByNode))
	failedNodeIDs := make([]string, 0)
	for nodeID, nodeStreams := range streamsByNode {
		invReq := &ipcpb.InvalidateSessionsRequest{
			StreamNames: nodeStreams,
			TenantId:    req.GetTenantId(),
			Reason:      req.GetReason(),
		}
		if err := control.SendInvalidateSessions(nodeID, invReq); err != nil {
			s.logger.WithFields(logging.Fields{
				"node_id":   nodeID,
				"tenant_id": req.GetTenantId(),
				"reason":    req.GetReason(),
				"error":     err,
			}).Warn("Failed to dispatch invalidate_sessions to node")
			failedNodeIDs = append(failedNodeIDs, nodeID)
			continue
		}
		dispatched++
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":           req.GetTenantId(),
		"reason":              req.GetReason(),
		"streams_invalidated": len(names),
		"nodes_attempted":     attempted,
		"nodes_dispatched":    dispatched,
		"nodes_failed":        len(failedNodeIDs),
	}).Info("Dispatched invalidate_sessions for playback-policy change")

	return &foghornpb.InvalidatePlaybackAuthResponse{
		StreamsInvalidated: int32(len(names)),
		NodesDispatched:    dispatched,
		NodesAttempted:     attempted,
		NodesFailed:        int32(len(failedNodeIDs)),
		FailedNodeIds:      failedNodeIDs,
	}, nil
}

func (s *FoghornGRPCServer) resolvePlaybackAuthInvalidationNames(ctx context.Context, tenantID string, requested []string) []string {
	seen := map[string]struct{}{}
	add := func(name string, out *[]string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		*out = append(*out, name)
	}

	var names []string
	if len(requested) > 0 {
		for _, name := range requested {
			add(name, &names)
		}
		return names
	}

	for _, st := range s.lb.GetStreamsByTenant(tenantID) {
		add(st.InternalName, &names)
	}
	for _, name := range s.tenantArtifactSessionNames(ctx, tenantID) {
		add(name, &names)
	}
	return names
}

func (s *FoghornGRPCServer) tenantArtifactSessionNames(ctx context.Context, tenantID string) []string {
	if s.db == nil || tenantID == "" {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT internal_name
		FROM foghorn.artifacts
		WHERE tenant_id = $1
		  AND status != 'deleted'
		  AND COALESCE(internal_name, '') != ''
	`, tenantID)
	if err != nil {
		s.logger.WithError(err).WithField("tenant_id", tenantID).Warn("playback-auth invalidation: artifact lookup failed")
		return nil
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			s.logger.WithError(err).Warn("playback-auth invalidation: artifact row scan failed")
			return names
		}
		names = append(names, artifactSessionName(name))
	}
	return names
}

func (s *FoghornGRPCServer) artifactSessionNodes(ctx context.Context, tenantID, internalName string) map[string]struct{} {
	nodes := map[string]struct{}{}
	hash := s.artifactHashForSessionName(ctx, tenantID, internalName)
	if hash == "" {
		return nodes
	}
	for _, node := range state.DefaultManager().FindNodesByArtifactHash(hash) {
		if node.NodeID != "" {
			nodes[node.NodeID] = struct{}{}
		}
	}
	if len(nodes) > 0 || s.db == nil {
		return nodes
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT an.node_id
		FROM foghorn.artifacts a
		JOIN foghorn.artifact_nodes an ON an.artifact_hash = a.artifact_hash
		WHERE a.artifact_hash = $1
		  AND a.tenant_id = $2
		  AND a.status != 'deleted'
		  AND COALESCE(an.is_orphaned, false) = false
	`, hash, tenantID)
	if err != nil {
		s.logger.WithError(err).WithField("artifact_hash", hash).Warn("playback-auth invalidation: artifact node lookup failed")
		return nodes
	}
	defer rows.Close()
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			s.logger.WithError(err).Warn("playback-auth invalidation: artifact node row scan failed")
			return nodes
		}
		if nodeID != "" {
			nodes[nodeID] = struct{}{}
		}
	}
	return nodes
}

func (s *FoghornGRPCServer) artifactHashForSessionName(ctx context.Context, tenantID, internalName string) string {
	if s.db == nil || tenantID == "" || internalName == "" {
		return ""
	}
	bare := mist.ExtractInternalName(internalName)
	var hash string
	if err := s.db.QueryRowContext(ctx, `
		SELECT artifact_hash
		FROM foghorn.artifacts
		WHERE internal_name = $1
		  AND tenant_id = $2
		  AND status != 'deleted'
		LIMIT 1
	`, bare, tenantID).Scan(&hash); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			s.logger.WithError(err).WithField("internal_name", bare).Warn("playback-auth invalidation: artifact hash lookup failed")
		}
		return ""
	}
	return hash
}

func artifactSessionName(internalName string) string {
	internalName = strings.TrimSpace(internalName)
	if internalName == "" || strings.Contains(internalName, "+") {
		return internalName
	}
	return "vod+" + internalName
}

// InvalidateTenantCache clears cached suspension status for a tenant (called on reactivation)
func (s *FoghornGRPCServer) InvalidateTenantCache(ctx context.Context, req *foghorncontrolpb.InvalidateTenantCacheRequest) (*foghorncontrolpb.InvalidateTenantCacheResponse, error) {
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	if s.cacheInvalidator == nil {
		s.logger.WithField("tenant_id", req.TenantId).Warn("Cache invalidator not configured, skipping cache invalidation")
		return &foghorncontrolpb.InvalidateTenantCacheResponse{
			EntriesInvalidated: 0,
		}, nil
	}

	entriesInvalidated := s.cacheInvalidator.InvalidateTenantCache(req.TenantId)

	s.logger.WithFields(logging.Fields{
		"tenant_id":           req.TenantId,
		"reason":              req.Reason,
		"entries_invalidated": entriesInvalidated,
	}).Info("Invalidated tenant cache entries")

	return &foghorncontrolpb.InvalidateTenantCacheResponse{
		EntriesInvalidated: int32(entriesInvalidated),
	}, nil
}

// SetNodeOperationalMode changes a node's operational mode with tenant ownership validation.
func (s *FoghornGRPCServer) SetNodeOperationalMode(ctx context.Context, req *foghorncontrolpb.SetNodeModeRequest) (*foghorncontrolpb.SetNodeModeResponse, error) {
	nodeID := strings.TrimSpace(req.GetNodeId())
	if nodeID == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id is required")
	}

	ns := state.DefaultManager().GetNodeState(nodeID)
	if ns == nil {
		return nil, status.Error(codes.NotFound, "node not found")
	}
	if err := authorizeNodeLifecycle(ctx, ns); err != nil {
		return nil, err
	}

	mode := state.NodeOperationalMode(strings.ToLower(strings.TrimSpace(req.GetMode())))
	if err := state.DefaultManager().SetNodeOperationalMode(ctx, nodeID, mode, strings.TrimSpace(req.GetSetBy())); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid mode: %v", err)
	}

	protoMode := handlers.StateToProtoMode(mode)
	if err := control.PushOperationalMode(nodeID, protoMode); err != nil {
		s.logger.WithFields(logging.Fields{
			"node_id": nodeID,
			"mode":    mode,
			"error":   err,
		}).Warn("Failed to push operational mode to node (may not be connected)")
	}

	return &foghorncontrolpb.SetNodeModeResponse{
		NodeId:  nodeID,
		Mode:    string(state.DefaultManager().GetNodeOperationalMode(nodeID)),
		Message: fmt.Sprintf("Node %s set to %s", nodeID, mode),
		Status:  foghorncontrolpb.SetNodeModeStatus_SET_NODE_MODE_STATUS_SUCCESS,
	}, nil
}

// GetNodeHealth returns real-time health and routing state for a node.
func (s *FoghornGRPCServer) GetNodeHealth(ctx context.Context, req *foghorncontrolpb.GetNodeHealthRequest) (*foghorncontrolpb.GetNodeHealthResponse, error) {
	nodeID := strings.TrimSpace(req.GetNodeId())
	if nodeID == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id is required")
	}

	ns := state.DefaultManager().GetNodeState(nodeID)
	if ns == nil {
		return nil, status.Error(codes.NotFound, "node not found")
	}
	if err := authorizeNodeLifecycle(ctx, ns); err != nil {
		return nil, err
	}

	lastHB := ""
	if !ns.LastHeartbeat.IsZero() {
		lastHB = ns.LastHeartbeat.UTC().Format(time.RFC3339)
	}

	resp := &foghorncontrolpb.GetNodeHealthResponse{
		NodeId:            nodeID,
		OperationalMode:   string(state.DefaultManager().GetNodeOperationalMode(nodeID)),
		IsHealthy:         ns.IsHealthy && !ns.IsStale,
		ActiveViewers:     int32(state.DefaultManager().GetNodeActiveViewers(nodeID)),
		ActiveStreams:     int32(state.DefaultManager().GetNodeActiveStreams(nodeID)),
		LastHeartbeat:     lastHB,
		ClusterId:         ns.ClusterID,
		TenantId:          ns.TenantID,
		CpuPercent:        ns.CPU,
		RamUsedMb:         ns.RAMCurrent,
		RamMaxMb:          ns.RAMMax,
		BandwidthUpMbps:   ns.UpSpeed,
		BandwidthDownMbps: ns.DownSpeed,
		BwLimitMbps:       ns.BWLimit,
		DiskTotalBytes:    ns.DiskTotalBytes,
		DiskUsedBytes:     ns.DiskUsedBytes,
		Location:          ns.Location,
	}
	if ns.Latitude != nil {
		resp.Latitude = ns.Latitude
	}
	if ns.Longitude != nil {
		resp.Longitude = ns.Longitude
	}
	resp.ComponentVersions = s.loadNodeComponentVersions(ctx, nodeID)
	return resp, nil
}

func (s *FoghornGRPCServer) loadNodeComponentVersions(ctx context.Context, nodeID string) []*foghorncontrolpb.NodeComponentVersion {
	if s == nil || s.db == nil {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT component, COALESCE(current_version, '')
		FROM foghorn.node_components
		WHERE node_id = $1
		ORDER BY component
	`, nodeID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*foghorncontrolpb.NodeComponentVersion
	for rows.Next() {
		v := &foghorncontrolpb.NodeComponentVersion{}
		if err := rows.Scan(&v.Component, &v.Version); err != nil {
			return nil
		}
		out = append(out, v)
	}
	return out
}

func authorizeNodeLifecycle(ctx context.Context, ns *state.NodeState) error {
	if ctxkeys.GetAuthType(ctx) == "service" {
		return nil
	}
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return status.Error(codes.Unauthenticated, "node lifecycle authentication required")
	}
	if ns.TenantID == "" || ns.TenantID != tenantID {
		return status.Error(codes.PermissionDenied, "node is not owned by this tenant")
	}
	return nil
}

// checkStorageEntitlement rejects new durable artifact writes when the
// tenant's current artifact bytes (plus any known additionalBytes) would
// meet or exceed their storage_limit_bytes entitlement. Returns a gRPC
// ResourceExhausted error on cap breach; nil otherwise.
//
// Fails open on Purser/DB errors — admission should not break on infra
// blips. The cap is per-tenant, point-in-time, and orthogonal to the
// storage_gb_seconds_cold billing meter (which is time-weighted and drives
// invoice lines, not admission).
func (s *FoghornGRPCServer) checkStorageEntitlement(ctx context.Context, tenantID string, additionalBytes int64) error {
	if s.purserClient == nil || tenantID == "" {
		return nil
	}
	billingStatus, err := s.purserClient.GetTenantBillingStatus(ctx, tenantID)
	if err != nil || billingStatus == nil {
		// Fail-open: log and admit. Misbehaving Purser must not block writes.
		if err != nil {
			s.logger.WithFields(logging.Fields{
				"tenant_id": tenantID,
				"error":     err,
			}).Warn("Storage entitlement check skipped: failed to resolve billing status")
		}
		return nil
	}
	limit := billingStatus.GetStorageLimitBytes()
	if limit <= 0 {
		return nil
	}

	var current int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(size_bytes), 0)
		FROM foghorn.artifacts
		WHERE tenant_id = $1::uuid
		  AND status NOT IN ('failed', 'expired', 'deleted', 'aborted')
	`, tenantID).Scan(&current); err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Warn("Storage entitlement check skipped: failed to sum tenant artifact bytes")
		return nil //nolint:nilerr // fail-open: DB outage must not block durable writes
	}

	return storageCapDecision(current, additionalBytes, limit)
}

// storageCapDecision returns ResourceExhausted when (current + additional)
// would breach the cap, or when current already meets it. additionalBytes=0
// means the caller cannot pre-declare a size (DVR start, clip export) — in
// that case only the at-or-over-cap rule applies, allowing best-effort writes
// while the tenant still has headroom. Extracted from checkStorageEntitlement
// so the policy is unit-testable without Purser/DB mocks.
func storageCapDecision(currentBytes, additionalBytes, limitBytes int64) error {
	if limitBytes <= 0 {
		return nil
	}
	overCap := currentBytes >= limitBytes
	wouldOverCap := additionalBytes > 0 && currentBytes+additionalBytes > limitBytes
	if !overCap && !wouldOverCap {
		return nil
	}
	return status.Errorf(
		codes.ResourceExhausted,
		"storage cap reached: %.2f GB used of %.2f GB limit — delete content or upgrade",
		float64(currentBytes)/float64(1<<30),
		float64(limitBytes)/float64(1<<30),
	)
}
