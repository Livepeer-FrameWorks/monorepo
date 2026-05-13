package grpc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"frameworks/api_balancing/internal/artifactoutbox"
	"frameworks/api_balancing/internal/artifacts"
	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/federation"
	"frameworks/api_balancing/internal/handlers"
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
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/x402"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
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
	GetClusterPeers(internalName, tenantID string) []*pb.TenantClusterPeer
}

type federationRPC interface {
	QueryStream(ctx context.Context, peerClusterID, peerAddr string, req *pb.QueryStreamRequest) (*pb.QueryStreamResponse, error)
	NotifyOriginPull(ctx context.Context, peerClusterID, peerAddr string, req *pb.OriginPullNotification) (*pb.OriginPullAck, error)
	PrepareArtifact(ctx context.Context, peerClusterID, peerAddr string, req *pb.PrepareArtifactRequest) (*pb.PrepareArtifactResponse, error)
	ForwardArtifactCommand(ctx context.Context, peerClusterID, peerAddr string, req *pb.ForwardArtifactCommandRequest) (*pb.ForwardArtifactCommandResponse, error)
}

type peerAddrResolver interface {
	GetPeerAddr(clusterID string) string
	GetPeers() map[string]string
}

// FoghornGRPCServer implements the Foghorn control plane gRPC services
type FoghornGRPCServer struct {
	pb.UnimplementedClipControlServiceServer
	pb.UnimplementedDVRControlServiceServer
	pb.UnimplementedViewerControlServiceServer
	pb.UnimplementedVodControlServiceServer
	pb.UnimplementedTenantControlServiceServer
	pb.UnimplementedNodeControlServiceServer

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
	pendingDVRStops     map[string]time.Time
	pendingDVRMu        sync.Mutex
	originPullMu        sync.Mutex
	originPulling       map[string]struct{}
	artifactCleaner     *artifacts.Cleaner
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
	GetClusterRouting(ctx context.Context, req *pb.GetClusterRoutingRequest) (*pb.ClusterRoutingResponse, error)
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
		pendingDVRStops: make(map[string]time.Time),
		originPulling:   make(map[string]struct{}),
	}
}

// RegisterServices registers all Foghorn gRPC services with the server
func (s *FoghornGRPCServer) RegisterServices(grpcServer *grpc.Server) {
	pb.RegisterClipControlServiceServer(grpcServer, s)
	pb.RegisterDVRControlServiceServer(grpcServer, s)
	pb.RegisterViewerControlServiceServer(grpcServer, s)
	pb.RegisterVodControlServiceServer(grpcServer, s)
	pb.RegisterTenantControlServiceServer(grpcServer, s)
	pb.RegisterNodeControlServiceServer(grpcServer, s)
}

// enrichClusterID returns the cluster for an operation. Prefers explicit
// cluster_id from the caller. Falls back to stream's node state for
// media-plane-initiated flows (e.g. DVR triggered by MistServer).
//
// Tenant-aware fallback prevents cross-tenant stream name collisions from
// enriching artifacts with another tenant's cluster context.
func (s *FoghornGRPCServer) enrichClusterID(explicit, streamName, tenantID string) string {
	if explicit != "" {
		return explicit
	}
	if streamName != "" {
		if ss := state.DefaultManager().GetStreamState(streamName); ss != nil && ss.NodeID != "" {
			if tenantID != "" && ss.TenantID != "" && ss.TenantID != tenantID {
				return ""
			}
			if ns := state.DefaultManager().GetNodeState(ss.NodeID); ns != nil && ns.ClusterID != "" {
				return ns.ClusterID
			}
		}
	}
	return ""
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
func (s *FoghornGRPCServer) SetRemoteEdgeCache(cache *federation.RemoteEdgeCache, clusterID string) {
	s.remoteEdgeCache = cache
	s.clusterID = clusterID
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
		if routing, err := s.quartermasterClient.GetClusterRouting(routingCtx, &pb.GetClusterRoutingRequest{TenantId: tenantID}); err == nil && routing != nil && routing.OfficialClusterId != nil {
			officialCluster = *routing.OfficialClusterId
		}
	}
	return resolver.Resolve(storage.ResolverInput{
		OriginClusterID:   ingestClusterID,
		OfficialClusterID: officialCluster,
		LegacyClusterID:   s.clusterID,
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

	req := &pb.ForwardArtifactCommandRequest{
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
	s.pendingDVRMu.Lock()
	s.pendingDVRStops[internalName] = time.Now()
	s.pendingDVRMu.Unlock()
}

func (s *FoghornGRPCServer) consumePendingDVRStop(internalName string) bool {
	if internalName == "" {
		return false
	}
	s.pendingDVRMu.Lock()
	_, ok := s.pendingDVRStops[internalName]
	if ok {
		delete(s.pendingDVRStops, internalName)
	}
	s.pendingDVRMu.Unlock()
	return ok
}

func (s *FoghornGRPCServer) emitDVRStartFailure(req *pb.StartDVRRequest, reason string) {
	if s.decklogClient == nil {
		return
	}
	dvrData := &pb.DVRLifecycleData{
		Status: pb.DVRLifecycleData_STATUS_FAILED,
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
	go artifactoutbox.EnqueueDVRLifecycleLogged(dvrData)
}

// resolveEffectiveDVRConfig clamps the caller-requested DVR window through
// pkg/dvrpolicy. Inputs come from the caller (StartDVRRequest carries the
// tier policy bundle and any caller-supplied window); cluster overrides
// come from the local Foghorn process env (one Foghorn per cluster).
//
// The live DVR window is INDEPENDENT of retention. Retention is post-end-
// only and computed at FinalizeDVR from the snapshotted dvr_retention_days
// column; it does not clamp the rolling Mist window.
func (s *FoghornGRPCServer) resolveEffectiveDVRConfig(req *pb.StartDVRRequest) dvrpolicy.Effective {
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

// dvrRetentionDays returns the post-end retention days to snapshot onto
// foghorn.artifacts.dvr_retention_days at DVR start. FinalizeDVR reads this
// snapshot months later, never re-resolving a tenant tier that may have changed.
func dvrRetentionDays(p *pb.DVRPolicy) int32 {
	if p == nil {
		return 30
	}
	if days := p.GetRecordingRetentionDays(); days > 0 {
		return days
	}
	return 30
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
	primary *pb.ViewerEndpoint,
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

// StartGRPCServer starts the Foghorn gRPC server
func StartGRPCServer(ctx context.Context, addr string, server *FoghornGRPCServer) error {
	lc := net.ListenConfig{}
	lis, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	serverOpts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(nodeControlAuthInterceptor(server.logger), grpcutil.SanitizeUnaryServerInterceptor()),
	}
	tlsCfg := grpcutil.ServerTLSConfig{
		CertFile:      strings.TrimSpace(os.Getenv("GRPC_TLS_CERT_PATH")),
		KeyFile:       strings.TrimSpace(os.Getenv("GRPC_TLS_KEY_PATH")),
		AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	err = grpcutil.WaitForServerTLSFiles(waitCtx, tlsCfg, server.logger)
	if err != nil {
		return fmt.Errorf("wait for foghorn gRPC TLS files: %w", err)
	}
	tlsOpt, err := grpcutil.ServerTLS(tlsCfg, server.logger)
	if err != nil {
		return fmt.Errorf("configure foghorn gRPC TLS: %w", err)
	}
	if tlsOpt != nil {
		serverOpts = append(serverOpts, tlsOpt)
	}
	grpcServer := grpc.NewServer(serverOpts...)
	server.RegisterServices(grpcServer)

	// gRPC health service for Foghorn control APIs
	hs := health.NewServer()
	hs.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	hs.SetServingStatus(pb.ClipControlService_ServiceDesc.ServiceName, grpc_health_v1.HealthCheckResponse_SERVING)
	hs.SetServingStatus(pb.DVRControlService_ServiceDesc.ServiceName, grpc_health_v1.HealthCheckResponse_SERVING)
	hs.SetServingStatus(pb.ViewerControlService_ServiceDesc.ServiceName, grpc_health_v1.HealthCheckResponse_SERVING)
	hs.SetServingStatus(pb.VodControlService_ServiceDesc.ServiceName, grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(grpcServer, hs)
	reflection.Register(grpcServer)

	server.logger.WithField("addr", addr).Info("Starting Foghorn gRPC server")
	return grpcServer.Serve(lis)
}

func nodeControlAuthInterceptor(logger logging.Logger) grpc.UnaryServerInterceptor {
	protected := map[string]bool{
		pb.NodeControlService_SetNodeOperationalMode_FullMethodName: true,
		pb.NodeControlService_GetNodeHealth_FullMethodName:          true,
	}
	serviceToken := strings.TrimSpace(os.Getenv("SERVICE_TOKEN"))
	jwtSecret := strings.TrimSpace(os.Getenv("JWT_SECRET"))
	authInterceptor := middleware.GRPCAuthInterceptor(middleware.GRPCAuthConfig{
		ServiceToken: serviceToken,
		JWTSecret:    []byte(jwtSecret),
		Logger:       logger,
	})
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if !protected[info.FullMethod] {
			return handler(ctx, req)
		}
		if serviceToken == "" && jwtSecret == "" {
			return nil, status.Error(codes.Unauthenticated, "node lifecycle auth is not configured")
		}
		return authInterceptor(ctx, req, info, handler)
	}
}

// CLIP CONTROL SERVICE IMPLEMENTATION

// buildClipLifecycleData creates an enriched ClipLifecycleData with timing fields
// CRITICAL: This function fixes the missing enrichment bug documented in ipc.proto lines 575-580
func buildClipLifecycleData(stage pb.ClipLifecycleData_Stage, req *pb.CreateClipRequest, reqID, clipHash string) *pb.ClipLifecycleData {
	data := &pb.ClipLifecycleData{
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
	if req.Mode != pb.ClipMode_CLIP_MODE_UNSPECIFIED {
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

// CreateClip creates a new clip from a stream
func (s *FoghornGRPCServer) CreateClip(ctx context.Context, req *pb.CreateClipRequest) (*pb.CreateClipResponse, error) {
	if req.StreamInternalName == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_internal_name is required")
	}
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.GetInternalName() == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name is required")
	}

	// Clip size is not known until export completes; reject only when the
	// tenant is already at cap. See checkStorageEntitlement docs.
	if err := s.checkStorageEntitlement(ctx, req.TenantId, 0); err != nil {
		return nil, err
	}

	format := req.GetFormat()
	if format == "" {
		format = "mp4"
	}

	// Select ingest node (cap=ingest)
	ictx := context.WithValue(ctx, ctxkeys.KeyCapability, "ingest")
	ingestHost, _, _, _, _, err := s.lb.GetBestNodeWithScore(ictx, req.StreamInternalName, 0, 0, map[string]int{}, "", true)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "no ingest node available: %v", err)
	}

	// Select storage node (cap=storage)
	sctx := context.WithValue(ctx, ctxkeys.KeyCapability, "storage")
	storageHost, _, _, _, _, err := s.lb.GetBestNodeWithScore(sctx, "", 0, 0, map[string]int{}, "", false)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "no storage node available: %v", err)
	}

	// Generate request_id for correlation
	reqID := uuid.New().String()

	// Get storage node ID
	storageNodeID := s.lb.GetNodeIDByHost(storageHost)
	if storageNodeID == "" {
		return nil, status.Error(codes.Unavailable, "storage node not connected")
	}

	// Resolve timing for hash generation and DB storage
	// Use start_unix or start_ms depending on mode, convert to milliseconds for storage
	var startMs, durationMs int64
	if req.StartUnix != nil {
		startMs = *req.StartUnix * 1000 // Convert seconds to ms
	} else if req.StartMs != nil {
		startMs = *req.StartMs * 1000 // start_ms is actually seconds, convert to ms
	}
	if req.DurationSec != nil {
		durationMs = *req.DurationSec * 1000 // Convert seconds to ms
	} else if req.StopUnix != nil && req.StartUnix != nil {
		durationMs = (*req.StopUnix - *req.StartUnix) * 1000
	} else if req.StopMs != nil && req.StartMs != nil {
		durationMs = (*req.StopMs - *req.StartMs) * 1000
	}

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

	// Emit STAGE_REQUESTED event to Decklog (with enriched timing fields)
	clipCluster := s.enrichClusterID(req.GetClusterId(), req.StreamInternalName, req.GetTenantId())
	if s.decklogClient != nil {
		clipData := buildClipLifecycleData(pb.ClipLifecycleData_STAGE_REQUESTED, req, reqID, clipHash)
		if clipCluster != "" {
			clipData.OriginClusterId = &clipCluster
			clipData.ServingClusterId = &clipCluster
		}
		go artifactoutbox.EnqueueClipLifecycleLogged(clipData)
	}

	// Build requested_params JSON for audit
	requestedParams := map[string]any{
		"mode": req.Mode.String(),
	}
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
	// requestedParams is stored in Commodore business registry, not in Foghorn
	// Retention policy (ExpiresAt) is also managed in Commodore

	// Store artifact lifecycle state in foghorn.artifacts
	// NOTE: Business registry (tenant, user, title, etc.) is stored in commodore.clips
	// tenant_id and user_id are denormalized here for Decklog events and fallback when Commodore is unavailable
	// retention_until defaults to 30 days (system default, not user-configured yet)
	storagePath := clips.BuildClipStoragePath(req.StreamInternalName, clipHash, format)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, stream_internal_name, internal_name, tenant_id, user_id, status, request_id, manifest_path, format, origin_cluster_id, retention_until, created_at, updated_at)
		VALUES ($1, 'clip', $2, $3, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, 'requested', $6, $7, $8, $9, NOW() + INTERVAL '30 days', NOW(), NOW())
	`, clipHash, req.StreamInternalName, req.GetInternalName(), req.TenantId, req.GetUserId(), reqID, storagePath, format, clipCluster)

	if err != nil {
		// Commodore registration succeeded (clip_hash provided) but Foghorn insert failed
		// Accept eventual consistency - Commodore record remains for audit/billing
		// RetentionJob will eventually clean up orphan artifacts
		s.logger.WithFields(logging.Fields{
			"clip_hash":     clipHash,
			"internal_name": req.StreamInternalName,
			"error":         err,
		}).Error("Failed to store clip artifact in database (Commodore record persists)")
		return nil, status.Error(codes.Internal, "failed to store artifact")
	}

	// Store node assignment in foghorn.artifact_nodes
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO foghorn.artifact_nodes (artifact_hash, node_id, file_path, base_url, cached_at)
		VALUES ($1, $2, $3, $4, NOW())
	`, clipHash, storageNodeID, storagePath, storageHost)

	if err != nil {
		s.logger.WithError(err).Error("Failed to store artifact node assignment")
		// Don't fail the request, the artifact was created
	}

	// Send gRPC message to storage Helmsman
	clipReq := &pb.ClipPullRequest{
		ClipHash:      clipHash,
		StreamName:    req.StreamInternalName,
		Format:        format,
		OutputName:    clipHash,
		SourceBaseUrl: control.DeriveMistHTTPBase(ingestHost),
		RequestId:     reqID,
	}
	if req.StartUnix != nil {
		clipReq.StartUnix = req.StartUnix
	}
	if req.StopUnix != nil {
		clipReq.StopUnix = req.StopUnix
	}
	if req.StartMs != nil {
		clipReq.StartMs = req.StartMs
	}
	if req.StopMs != nil {
		clipReq.StopMs = req.StopMs
	}
	if req.DurationSec != nil {
		clipReq.DurationSec = req.DurationSec
	}

	if err := control.SendClipPull(storageNodeID, clipReq); err != nil {
		// Mark artifact as failed since we couldn't send to Helmsman
		_, _ = s.db.ExecContext(ctx, `
			UPDATE foghorn.artifacts SET status = 'failed', error_message = $1, updated_at = NOW()
			WHERE artifact_hash = $2 AND tenant_id = $3
		`, fmt.Sprintf("storage node unavailable: %v", err), clipHash, req.TenantId)

		// Emit FAILED event to Decklog
		if s.decklogClient != nil {
			failedData := buildClipLifecycleData(pb.ClipLifecycleData_STAGE_FAILED, req, reqID, clipHash)
			failedData.Error = func() *string { e := fmt.Sprintf("storage node unavailable: %v", err); return &e }()
			go func() {
				if errSend := artifactoutbox.EnqueueClipLifecycle(failedData); errSend != nil {
					s.logger.WithError(errSend).Error("Failed to emit clip failed event")
				}
			}()
		}

		s.logger.WithFields(logging.Fields{
			"clip_hash": clipHash,
			"node_id":   storageNodeID,
			"error":     err,
		}).Error("Failed to send clip request to storage node")
		return nil, status.Errorf(codes.Unavailable, "storage node unavailable: %v", err)
	}

	// Emit STAGE_QUEUED event to Decklog (with enriched timing fields)
	if s.decklogClient != nil {
		clipData := buildClipLifecycleData(pb.ClipLifecycleData_STAGE_QUEUED, req, reqID, clipHash)
		clipData.CompletedAt = func() *int64 { t := time.Now().Unix(); return &t }()
		go artifactoutbox.EnqueueClipLifecycleLogged(clipData)
	}

	// Update stream state
	state.DefaultManager().UpdateStreamInstanceInfo(req.StreamInternalName, storageNodeID, map[string]any{
		"clip_status":     "requested",
		"clip_request_id": reqID,
		"clip_format":     format,
	})

	return &pb.CreateClipResponse{
		Status:      "queued",
		IngestHost:  ingestHost,
		StorageHost: storageHost,
		NodeId:      storageNodeID,
		RequestId:   reqID,
		ClipHash:    clipHash,
		PlaybackId:  req.GetPlaybackId(),
	}, nil
}

// DeleteClip deletes a clip
func (s *FoghornGRPCServer) DeleteClip(ctx context.Context, req *pb.DeleteClipRequest) (*pb.DeleteClipResponse, error) {
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
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT status, size_bytes, retention_until, stream_internal_name, tenant_id, user_id,
		       format, storage_cluster_id, origin_cluster_id
		FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND artifact_type = 'clip' AND tenant_id = $2
	`, req.ClipHash, req.GetTenantId()).Scan(&currentStatus, &sizeBytes, &retentionUntil, &internalName, &denormTenantID, &denormUserID, &format, &storageClusterID, &originClusterID)

	if errors.Is(err, sql.ErrNoRows) {
		if handled, _ := s.forwardArtifactToFederation(ctx, "delete_clip", req.ClipHash, req.GetTenantId(), ""); handled {
			return &pb.DeleteClipResponse{Success: true, Message: "clip deleted via federation"}, nil
		}
		return nil, status.Error(codes.NotFound, "clip not found")
	} else if err != nil {
		return nil, status.Error(codes.Internal, "failed to check clip existence")
	}

	if currentStatus == "deleted" {
		return &pb.DeleteClipResponse{
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

	cleanupError := ""

	// Send delete request to Helmsman if we know the storage node
	if nodeID != "" {
		requestID := uuid.NewString()
		deleteReq := &pb.ClipDeleteRequest{
			ClipHash:  req.ClipHash,
			RequestId: requestID,
		}
		if errSend := control.SendClipDelete(nodeID, deleteReq); errSend != nil {
			cleanupError = fmt.Sprintf("node cleanup pending: %v", errSend)
			// Log but don't fail - the soft delete still works, cleanup can happen later
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

	// Delete S3 bytes immediately (cross-cluster aware via the federation
	// delete delegate). Failure marks cleanup-pending; soft-delete still
	// proceeds so the row enters the purge cycle for retries.
	if s.artifactCleaner == nil {
		if cleanupError != "" {
			cleanupError += "; "
		}
		cleanupError += "s3 cleanup pending: cleaner not wired"
		s.logger.WithField("clip_hash", req.ClipHash).Warn("Artifact cleaner not wired; clip S3 cleanup deferred to purge job")
	} else if errCleanup := s.artifactCleaner.Delete(ctx, artifacts.ArtifactRef{
		Hash:             req.ClipHash,
		Type:             "clip",
		TenantID:         req.GetTenantId(),
		StreamInternal:   internalName.String,
		Format:           format.String,
		StorageClusterID: storageClusterID.String,
		OriginClusterID:  originClusterID.String,
	}); errCleanup != nil {
		if cleanupError != "" {
			cleanupError += "; "
		}
		cleanupError += fmt.Sprintf("s3 cleanup pending: %v", errCleanup)
		s.logger.WithError(errCleanup).WithField("clip_hash", req.ClipHash).Warn("Failed to delete clip from S3, will be retried by purge job")
	}

	// Soft delete in foghorn.artifacts
	_, err = s.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts SET status = 'deleted', updated_at = NOW()
		WHERE artifact_hash = $1 AND artifact_type = 'clip' AND tenant_id = $2
	`, req.ClipHash, req.GetTenantId())
	if err != nil {
		s.logger.WithError(err).Error("Failed to delete clip")
		return nil, status.Error(codes.Internal, "failed to delete clip")
	}

	s.logger.WithField("clip_hash", req.ClipHash).Info("Clip soft-deleted successfully")

	// Emit deletion lifecycle immediately (do not wait for node cleanup)
	s.emitClipDeletedLifecycle(ctx, req.ClipHash, nodeID, sizeBytes, retentionUntil, internalName, denormTenantID, denormUserID, cleanupError)

	message := "clip deleted successfully"
	if cleanupError != "" {
		message = "clip deleted (" + cleanupError + ")"
	}
	return &pb.DeleteClipResponse{
		Success: true,
		Message: message,
	}, nil
}

// DVR CONTROL SERVICE IMPLEMENTATION

// StartDVR initiates DVR recording for a stream
func (s *FoghornGRPCServer) StartDVR(ctx context.Context, req *pb.StartDVRRequest) (*pb.StartDVRResponse, error) {
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

	dvrCluster := s.enrichClusterID(req.GetClusterId(), req.InternalName, req.GetTenantId())

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

	// Resolve actual source node for this stream
	sourceNodeID, baseURL, ok := control.GetStreamSource(req.InternalName)
	if !ok {
		s.emitDVRStartFailure(req, "no source node available")
		return nil, status.Error(codes.Unavailable, "no source node available")
	}

	// Select storage node
	sctx := context.WithValue(ctx, ctxkeys.KeyCapability, "storage")
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

	// Check for existing active DVR in foghorn.artifacts
	var existingHash string
	_ = s.db.QueryRowContext(ctx, `
		SELECT artifact_hash FROM foghorn.artifacts
		WHERE stream_internal_name=$1 AND artifact_type='dvr' AND status IN ('requested','starting','recording')
		ORDER BY created_at DESC LIMIT 1
	`, req.InternalName).Scan(&existingHash)

	if existingHash != "" {
		playbackID := ""
		if control.CommodoreClient != nil {
			if resp, errResolve := control.CommodoreClient.ResolveDVRHash(ctx, existingHash); errResolve == nil && resp.Found {
				playbackID = resp.PlaybackId
			}
		}
		return &pb.StartDVRResponse{
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
	if control.CommodoreClient != nil {
		regReq := &pb.RegisterDVRRequest{
			TenantId:           req.TenantId,
			UserId:             req.GetUserId(),
			StreamId:           req.GetStreamId(),
			StreamInternalName: req.InternalName,
			OriginClusterId:    s.enrichClusterID(req.GetClusterId(), req.InternalName, req.GetTenantId()),
		}
		var regResp *pb.RegisterDVRResponse
		regResp, err = control.CommodoreClient.RegisterDVR(ctx, regReq)
		if err != nil {
			s.logger.WithError(err).Error("Failed to register DVR with Commodore")
			return nil, status.Errorf(codes.Internal, "failed to register DVR: %v", err)
		}
		dvrHash = regResp.DvrHash
		artifactInternalName = regResp.GetInternalName()
		playbackID = regResp.GetPlaybackId()
		streamID = regResp.GetStreamId()
	} else {
		return nil, status.Error(codes.Unavailable, "Commodore not available")
	}

	// Generate request_id for tracing (distinct from artifact hash)
	requestID := uuid.New().String()

	// Store artifact lifecycle state in foghorn.artifacts. The DVR-policy
	// snapshot columns (dvr_window_seconds, dvr_chapter_mode, dvr_chapter_interval,
	// dvr_retention_days) capture the resolved policy at start time so finalize
	// months later applies the same policy even if the tenant's tier has changed
	// during the recording. retention_until is left NULL here — FinalizeDVR
	// computes it as ended_at + dvr_retention_days*24h (post-end semantics).
	chapterMode := req.GetDvrChapterMode()
	if chapterMode == "" {
		chapterMode = "window_sized_chapters"
	}
	chapterInterval := req.GetDvrChapterIntervalSeconds()
	retentionDays := dvrRetentionDays(req.GetDvrPolicy())
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO foghorn.artifacts (
			artifact_hash, artifact_type, stream_internal_name, internal_name,
			stream_id, tenant_id, user_id,
			status, request_id, format, origin_cluster_id,
			dvr_window_seconds, dvr_chapter_mode, dvr_chapter_interval, dvr_retention_days,
			created_at, updated_at
		)
		VALUES ($1, 'dvr', $2, $3, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, NULLIF($6, '')::uuid,
		        'requested', $7, 'm3u8', $8, $9, NULLIF($10, '')::text, NULLIF($11, 0)::int, NULLIF($12, 0)::int,
		        NOW(), NOW())
	`,
		dvrHash, req.InternalName, artifactInternalName, streamID, req.TenantId, req.GetUserId(), requestID, dvrCluster,
		effective.DVRWindowSeconds, chapterMode, chapterInterval, retentionDays,
	)

	if err != nil {
		if control.CommodoreClient != nil {
			if _, cleanupErr := control.CommodoreClient.UpdateDVRRetention(ctx, &pb.UpdateDVRRetentionRequest{
				DvrHash:        dvrHash,
				TenantId:       req.TenantId,
				RetentionUntil: timestamppb.New(time.Now().UTC()),
			}); cleanupErr != nil {
				s.logger.WithError(cleanupErr).WithFields(logging.Fields{
					"dvr_hash":  dvrHash,
					"tenant_id": req.TenantId,
				}).Warn("Failed to expire DVR registry row after Foghorn insert failure")
			}
		}
		s.logger.WithFields(logging.Fields{
			"dvr_hash":      dvrHash,
			"internal_name": req.InternalName,
			"error":         err,
		}).Error("Failed to store DVR artifact in database")
		return nil, status.Error(codes.Internal, "failed to store DVR artifact")
	}

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
		if s.decklogClient != nil {
			stoppedAt := time.Now().Unix()
			errorMsg := "stream ended before DVR start"
			dvrData := &pb.DVRLifecycleData{
				Status:  pb.DVRLifecycleData_STATUS_STOPPED,
				DvrHash: dvrHash,
				EndedAt: &stoppedAt,
				Error:   &errorMsg,
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
				StreamInternalName: func() *string {
					if req.InternalName != "" {
						return &req.InternalName
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
			go artifactoutbox.EnqueueDVRLifecycleLogged(dvrData)
		}
		return &pb.StartDVRResponse{
			Status:        responseStatus,
			DvrHash:       dvrHash,
			IngestHost:    baseURL,
			StorageHost:   storageHost,
			StorageNodeId: storageNodeID,
			PlaybackId:    playbackID,
		}, nil
	}

	// Store node assignment in foghorn.artifact_nodes
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO foghorn.artifact_nodes (artifact_hash, node_id, base_url, cached_at)
		VALUES ($1, $2, $3, NOW())
	`, dvrHash, storageNodeID, storageHost)

	if err != nil {
		s.logger.WithError(err).Error("Failed to store DVR artifact node assignment")
		// Don't fail the request, the artifact was created
	}

	// DVR configuration. Effective live-window / segment / max_entries are
	// resolved by pkg/dvrpolicy above. The sidecar applies these values
	// verbatim and never interprets tier or cluster context.
	config := &pb.DVRConfig{
		Enabled:          true,
		Format:           "ts",
		SegmentDuration:  int32(effective.SegmentDurationSeconds),
		DvrWindowSeconds: int32(effective.DVRWindowSeconds),
		MaxEntries:       int32(effective.MaxEntries),
		// The sidecar runs until Mist accepts a stop; FinalizeDVR computes
		// retention_until after the stream session ends.
		RetentionUntil: 0,
	}

	fullDTSC := control.BuildDTSCURI(sourceNodeID, req.InternalName, true, s.logger)
	if fullDTSC == "" {
		final, finalErr := control.FinalizeDVR(ctx, dvrHash, control.FinalizeOptions{
			ReportedStatus: "failed",
			ReportedError:  "DTSC output not available on source node",
			StorageNodeID:  storageNodeID,
		})
		if finalErr != nil && final.ArtifactStatus == "" {
			s.logger.WithError(finalErr).WithField("dvr_hash", dvrHash).Error("Failed to finalize DVR after DTSC lookup failure")
		}
		return nil, status.Error(codes.Unavailable, "DTSC output not available on source node")
	}

	// Send gRPC control message to storage Helmsman
	dvrReq := &pb.DVRStartRequest{
		DvrHash:       dvrHash,
		InternalName:  req.InternalName,
		SourceBaseUrl: fullDTSC,
		RequestId:     dvrHash,
		Config:        config,
		StreamId:      streamID,
	}

	if err := control.SendDVRStart(storageNodeID, dvrReq); err != nil {
		final, finalErr := control.FinalizeDVR(ctx, dvrHash, control.FinalizeOptions{
			ReportedStatus: "failed",
			ReportedError:  fmt.Sprintf("storage node unavailable: %v", err),
			StorageNodeID:  storageNodeID,
		})
		if finalErr != nil && final.ArtifactStatus == "" {
			s.logger.WithError(finalErr).WithField("dvr_hash", dvrHash).Error("Failed to finalize DVR after storage start failure")
		}

		// Emit FAILED event to Decklog
		if s.decklogClient != nil {
			failedData := &pb.DVRLifecycleData{
				Status:  pb.DVRLifecycleData_STATUS_FAILED,
				DvrHash: dvrHash,
				Error:   func() *string { e := fmt.Sprintf("storage node unavailable: %v", err); return &e }(),
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
				StreamInternalName: func() *string {
					if req.InternalName != "" {
						return &req.InternalName
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
			go func() {
				if errSend := artifactoutbox.EnqueueDVRLifecycle(failedData); errSend != nil {
					s.logger.WithError(errSend).Error("Failed to emit DVR failed event")
				}
			}()
		}

		s.logger.WithFields(logging.Fields{
			"dvr_hash": dvrHash,
			"node_id":  storageNodeID,
			"error":    err,
		}).Error("Failed to send DVR start request to storage node")
		return nil, status.Error(codes.Internal, "failed to start DVR on storage node")
	}

	// Emit DVR STATUS_STARTED event to Decklog
	if s.decklogClient != nil {
		dvrData := &pb.DVRLifecycleData{
			Status:           pb.DVRLifecycleData_STATUS_STARTED,
			DvrHash:          dvrHash,
			OriginClusterId:  &dvrCluster,
			ServingClusterId: &dvrCluster,
			StartedAt:        func() *int64 { t := time.Now().Unix(); return &t }(),
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
			StreamInternalName: func() *string {
				if req.InternalName != "" {
					return &req.InternalName
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
		go artifactoutbox.EnqueueDVRLifecycleLogged(dvrData)
	}

	// Update stream state
	state.DefaultManager().UpdateStreamInstanceInfo(req.InternalName, storageNodeID, map[string]any{
		"dvr_status": "requested",
		"dvr_hash":   dvrHash,
	})

	return &pb.StartDVRResponse{
		Status:        "started",
		DvrHash:       dvrHash,
		IngestHost:    baseURL,
		StorageHost:   storageHost,
		StorageNodeId: storageNodeID,
		PlaybackId:    playbackID,
	}, nil
}

// StopDVR stops an active DVR recording
func (s *FoghornGRPCServer) StopDVR(ctx context.Context, req *pb.StopDVRRequest) (*pb.StopDVRResponse, error) {
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
			return &pb.StopDVRResponse{Success: true, Message: "DVR stopped via federation"}, nil
		}
		return nil, status.Error(codes.NotFound, "DVR recording not found")
	} else if err != nil {
		s.logger.WithError(err).Error("Failed to fetch DVR artifact")
		return nil, status.Error(codes.Internal, "failed to fetch DVR artifact")
	}

	switch dvrStatus {
	case "completed", "completed_partial", "failed", "ready", "deleted", "finalizing":
		return &pb.StopDVRResponse{
			Success: false,
			Message: fmt.Sprintf("DVR recording already finished with status: %s", dvrStatus),
		}, nil
	}

	// Get node_id from artifact_nodes
	var nodeID string
	_ = s.db.QueryRowContext(ctx, `
		SELECT node_id FROM foghorn.artifact_nodes
		WHERE artifact_hash = $1 AND NOT is_orphaned
		ORDER BY last_seen_at DESC LIMIT 1
	`, req.DvrHash).Scan(&nodeID)

	if nodeID == "" {
		return nil, status.Error(codes.Unavailable, "no storage node available for this DVR")
	}

	// Send stop command to storage Helmsman
	stopReq := &pb.DVRStopRequest{
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

	return &pb.StopDVRResponse{
		Success: true,
		Message: "DVR recording stopping",
	}, nil
}

// DeleteDVR deletes a DVR recording and its files
func (s *FoghornGRPCServer) DeleteDVR(ctx context.Context, req *pb.DeleteDVRRequest) (*pb.DeleteDVRResponse, error) {
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
	)

	err := s.db.QueryRowContext(ctx, `
		SELECT status, COALESCE(stream_internal_name, ''), size_bytes, retention_until, started_at, ended_at, tenant_id, user_id,
		       storage_cluster_id, origin_cluster_id
		FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND artifact_type = 'dvr' AND tenant_id = $2
	`, req.DvrHash, req.GetTenantId()).Scan(&dvrStatus, &internalName, &sizeBytes, &retentionUntil, &startedAt, &endedAt, &denormTenantID, &denormUserID, &storageClusterID, &originClusterID)

	if errors.Is(err, sql.ErrNoRows) {
		if handled, _ := s.forwardArtifactToFederation(ctx, "delete_dvr", req.DvrHash, req.GetTenantId(), ""); handled {
			return &pb.DeleteDVRResponse{Success: true, Message: "DVR deleted via federation"}, nil
		}
		return nil, status.Error(codes.NotFound, "DVR recording not found")
	} else if err != nil {
		s.logger.WithError(err).Error("Failed to fetch DVR artifact")
		return nil, status.Error(codes.Internal, "failed to fetch DVR artifact")
	}

	if dvrStatus == "deleted" {
		return &pb.DeleteDVRResponse{
			Success: false,
			Message: "DVR recording is already deleted",
		}, nil
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
			stopReq := &pb.DVRStopRequest{
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

	cleanupError := ""

	// Send delete request to Helmsman if we know the storage node
	if nodeID != "" {
		requestID := uuid.NewString()
		deleteReq := &pb.DVRDeleteRequest{
			DvrHash:   req.DvrHash,
			RequestId: requestID,
		}
		if errDelete := control.SendDVRDelete(nodeID, deleteReq); errDelete != nil {
			cleanupError = fmt.Sprintf("node cleanup pending: %v", errDelete)
			// Log but don't fail - the soft delete still works, cleanup can happen later
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

	// Delete S3 bytes immediately (cross-cluster aware). Failure marks
	// cleanup-pending; soft-delete still proceeds.
	if s.artifactCleaner == nil {
		if cleanupError != "" {
			cleanupError += "; "
		}
		cleanupError += "s3 cleanup pending: cleaner not wired"
		s.logger.WithField("dvr_hash", req.DvrHash).Warn("Artifact cleaner not wired; DVR S3 cleanup deferred to purge job")
	} else if errCleanup := s.artifactCleaner.Delete(ctx, artifacts.ArtifactRef{
		Hash:             req.DvrHash,
		Type:             "dvr",
		TenantID:         req.GetTenantId(),
		StreamInternal:   internalName,
		StorageClusterID: storageClusterID.String,
		OriginClusterID:  originClusterID.String,
	}); errCleanup != nil {
		if cleanupError != "" {
			cleanupError += "; "
		}
		cleanupError += fmt.Sprintf("s3 cleanup pending: %v", errCleanup)
		s.logger.WithError(errCleanup).WithField("dvr_hash", req.DvrHash).Warn("Failed to delete DVR from S3, will be retried by purge job")
	}

	// Soft delete in foghorn.artifacts
	_, err = s.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts SET status = 'deleted', updated_at = NOW()
		WHERE artifact_hash = $1 AND artifact_type = 'dvr'
	`, req.DvrHash)
	if err != nil {
		s.logger.WithError(err).Error("Failed to delete DVR recording")
		return nil, status.Error(codes.Internal, "failed to delete DVR recording")
	}

	s.logger.WithField("dvr_hash", req.DvrHash).Info("DVR recording soft-deleted successfully")

	// Emit deletion lifecycle immediately (do not wait for node cleanup)
	s.emitDVRDeletedLifecycle(ctx, req.DvrHash, nodeID, sizeBytes, retentionUntil, startedAt, endedAt, internalName, denormTenantID, denormUserID, cleanupError)

	message := "DVR recording deleted successfully"
	if cleanupError != "" {
		message = "DVR recording deleted (" + cleanupError + ")"
	}
	return &pb.DeleteDVRResponse{
		Success: true,
		Message: message,
	}, nil
}

// VIEWER CONTROL SERVICE IMPLEMENTATION

// ResolveViewerEndpoint resolves the best endpoint(s) for a viewer
func (s *FoghornGRPCServer) ResolveViewerEndpoint(ctx context.Context, req *pb.ViewerEndpointRequest) (*pb.ViewerEndpointResponse, error) {
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
		billingTarget := control.ResolvePlaybackPolicyTarget(ctx, req.GetContentId(), resolution.InternalName)
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

	var response *pb.ViewerEndpointResponse

	switch resolvedType {
	case "live":
		response, err = s.resolveLiveViewerEndpoint(ctx, req, lat, lon, resolution.RoutingInternalName(), resolution.TenantId, resolution.StreamId, resolution.ClusterPeers)
	case "dvr", "clip", "vod":
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

func (s *FoghornGRPCServer) enforceResolvePlaybackPolicy(ctx context.Context, req *pb.ViewerEndpointRequest, resolution *control.ContentResolution) error {
	if control.CommodoreClient == nil {
		s.logger.WithFields(logging.Fields{
			"content_id": req.GetContentId(),
			"reason":     "policy-client-unavailable",
		}).Warn("Rejecting protected resolve request")
		return status.Error(codes.PermissionDenied, "playback access denied")
	}
	target := control.ResolvePlaybackPolicyTarget(ctx, req.GetContentId(), resolution.InternalName)
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
	decision := triggers.EvaluatePlaybackPolicyWithRecorder(ctx, s.logger, policyInternalName, &pb.ViewerConnectTrigger{
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

func (s *FoghornGRPCServer) resolveLiveViewerEndpoint(ctx context.Context, req *pb.ViewerEndpointRequest, lat, lon float64, internalName, tenantID, streamID string, clusterPeers []*pb.TenantClusterPeer) (*pb.ViewerEndpointResponse, error) {
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
	if s.remoteEdgeCache != nil {
		if record, _ := s.remoteEdgeCache.GetActiveReplication(ctx, internalName); record != nil {
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
	// Cold start: EdgeSummary cache empty but peers exist — fan out QueryStream
	if !skipRemote && len(deps.RemoteEdges) == 0 && len(allPeers) > 0 {
		deps.RemoteEdges = s.queryStreamFanOut(ctx, internalName, tenantID, lat, lon, allPeers)
	}

	response, err := control.ResolveLivePlayback(ctx, deps, req.ContentId, internalName, streamID, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "%v", err)
	}

	// If a remote cluster won the summary-level comparison, confirm with QueryStream
	if response.Primary != nil && response.Primary.ClusterId != "" {
		confirmed := s.confirmRemoteEndpoint(ctx, response, req.ContentId, internalName, tenantID, lat, lon)
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
func (s *FoghornGRPCServer) collectRemoteEdges(ctx context.Context, peers []*pb.TenantClusterPeer) []balancer.RemoteEdgeCandidate {
	var candidates []balancer.RemoteEdgeCandidate
	for _, peer := range peers {
		if peer.GetClusterId() == s.clusterID || peer.GetClusterId() == "" || control.IsServedCluster(peer.GetClusterId()) {
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

// confirmRemoteEndpoint validates a summary-level remote win by calling QueryStream
// on the winning cluster(s). Returns nil if confirmation fails (caller keeps original).
func (s *FoghornGRPCServer) confirmRemoteEndpoint(ctx context.Context, response *pb.ViewerEndpointResponse, viewKey, internalName, tenantID string, lat, lon float64) *pb.ViewerEndpointResponse {
	if s.federationClient == nil || s.peerManager == nil {
		return nil
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
		return nil
	}

	type queryResult struct {
		clusterID string
		resp      *pb.QueryStreamResponse
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
			resp, err := s.federationClient.QueryStream(qCtx, cid, caddr, &pb.QueryStreamRequest{
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

	var bestCandidate *pb.EdgeCandidate
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
		return nil
	}

	// Try origin-pull: pre-arrange local replication so MistServer can pull via DTSC
	if arranged := s.arrangeOriginPull(ctx, bestCandidate, bestCluster, internalName, tenantID, viewKey, lat, lon, response); arranged != nil {
		return arranged
	}

	// No origin-pull possible — redirect viewer to the remote cluster directly
	playURL := "https://" + bestCandidate.BaseUrl + "/play/" + viewKey
	confirmed := &pb.ViewerEndpointResponse{
		Primary: &pb.ViewerEndpoint{
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

	return confirmed
}

// arrangeOriginPull pre-arranges a local DTSC pull from a remote source.
// Returns a response pointing to the local edge, or nil if arrangement fails.
// The actual DTSC pull happens when MistServer's PLAY_REWRITE trigger fires.
func (s *FoghornGRPCServer) arrangeOriginPull(ctx context.Context, remote *pb.EdgeCandidate, remoteCluster, internalName, tenantID, viewKey string, lat, lon float64, original *pb.ViewerEndpointResponse) *pb.ViewerEndpointResponse {
	if s.remoteEdgeCache == nil || remote.DtscUrl == "" {
		return nil
	}

	if !s.tryBeginOriginPull(internalName) {
		if record, _ := s.remoteEdgeCache.GetActiveReplication(ctx, internalName); record != nil {
			if endpoint := s.buildLocalEndpoint(record, viewKey); endpoint != nil {
				return &pb.ViewerEndpointResponse{Primary: endpoint, Metadata: original.Metadata}
			}
		}
		return nil
	}
	defer s.finishOriginPull(internalName)

	// Already pulling this stream? Return the existing local endpoint.
	if record, _ := s.remoteEdgeCache.GetActiveReplication(ctx, internalName); record != nil {
		endpoint := s.buildLocalEndpoint(record, viewKey)
		if endpoint != nil {
			return &pb.ViewerEndpointResponse{Primary: endpoint, Metadata: original.Metadata}
		}
		_ = s.remoteEdgeCache.DeleteActiveReplication(ctx, internalName)
	}

	// Loop prevention: don't pull from a cluster already pulling from us
	if replications, _ := s.remoteEdgeCache.GetRemoteReplications(ctx, internalName); len(replications) > 0 {
		for _, r := range replications {
			if r.ClusterID == remoteCluster {
				return nil
			}
		}
	}

	// Find a healthy local edge with capacity (tenant-scoped on shared Foghorns)
	lbCtx := context.WithValue(ctx, ctxkeys.KeyCapability, "edge")
	if tenantID != "" {
		lbCtx = context.WithValue(lbCtx, ctxkeys.KeyClusterScope, tenantID)
	}
	localHost, _, _, _, _, err := s.lb.GetBestNodeWithScore(lbCtx, "", lat, lon, nil, "", false)
	if err != nil {
		return nil
	}
	localNodeID := s.lb.GetNodeIDByHost(localHost)
	if localNodeID == "" {
		return nil
	}

	// NotifyOriginPull: tell the source cluster we intend to pull
	peerAddr := s.peerManager.GetPeerAddr(remoteCluster)
	if peerAddr == "" {
		return nil
	}
	notifyCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ack, err := s.federationClient.NotifyOriginPull(notifyCtx, remoteCluster, peerAddr, &pb.OriginPullNotification{
		StreamName:    internalName,
		SourceNodeId:  remote.NodeId,
		DestClusterId: s.clusterID,
		DestNodeId:    localNodeID,
		TenantId:      tenantID,
	})
	if err != nil || !ack.GetAccepted() {
		return nil
	}

	// Record the in-flight replication so balance/source endpoints return the DTSC URL
	record := &federation.ActiveReplicationRecord{
		StreamName:    internalName,
		SourceNodeID:  remote.NodeId,
		SourceCluster: remoteCluster,
		DestCluster:   s.clusterID,
		DestNodeID:    localNodeID,
		DTSCURL:       ack.DtscUrl,
		BaseURL:       localHost,
		CreatedAt:     time.Now(),
	}
	if err := s.remoteEdgeCache.SetActiveReplication(ctx, record); err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"stream":         internalName,
			"source_cluster": remoteCluster,
			"source_node":    remote.NodeId,
			"dest_node":      localNodeID,
		}).Warn("Origin-pull acked but local active replication cache write failed")
		return nil
	}

	s.logger.WithFields(logging.Fields{
		"stream":         internalName,
		"source_cluster": remoteCluster,
		"source_node":    remote.NodeId,
		"dest_node":      localNodeID,
		"dtsc_url":       ack.DtscUrl,
	}).Info("Origin-pull arranged via gRPC, serving viewer from local edge")

	endpoint := s.buildLocalEndpoint(record, viewKey)
	if endpoint != nil {
		return &pb.ViewerEndpointResponse{Primary: endpoint, Metadata: original.Metadata}
	}
	return nil
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

// buildLocalEndpoint constructs a ViewerEndpoint from a local node with an active replication.
func (s *FoghornGRPCServer) buildLocalEndpoint(record *federation.ActiveReplicationRecord, viewKey string) *pb.ViewerEndpoint {
	outputs, exists := control.GetNodeOutputs(record.DestNodeID)
	if !exists || outputs.Outputs == nil {
		return nil
	}
	publicHost := control.ExtractPublicHostFromOutputs(outputs.Outputs)
	var protocol, endpointURL string
	if webrtcURL, ok := outputs.Outputs["WebRTC"]; ok {
		protocol = "webrtc"
		endpointURL = control.ResolveTemplateURL(webrtcURL, outputs.BaseURL, viewKey)
		if publicHost != "" {
			endpointURL = strings.ReplaceAll(endpointURL, "HOST", publicHost)
		}
	} else if hlsURL, ok := outputs.Outputs["HLS (TS)"]; ok {
		protocol = "hls"
		endpointURL = control.ResolveTemplateURL(hlsURL, outputs.BaseURL, viewKey)
		if publicHost != "" {
			endpointURL = strings.ReplaceAll(endpointURL, "HOST", publicHost)
		}
	}
	if endpointURL == "" {
		return nil
	}
	return &pb.ViewerEndpoint{
		NodeId:   record.DestNodeID,
		BaseUrl:  record.BaseURL,
		Protocol: protocol,
		Url:      endpointURL,
	}
}

// queryStreamFanOut performs cold-start QueryStream to peer clusters when EdgeSummary is empty.
func (s *FoghornGRPCServer) queryStreamFanOut(ctx context.Context, internalName, tenantID string, lat, lon float64, peers []*pb.TenantClusterPeer) []balancer.RemoteEdgeCandidate {
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
			resp, err := s.federationClient.QueryStream(qCtx, peerID, peerAddr, &pb.QueryStreamRequest{
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

func (s *FoghornGRPCServer) resolveArtifactViewerEndpoint(ctx context.Context, req *pb.ViewerEndpointRequest, lat, lon float64) (*pb.ViewerEndpointResponse, error) {
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
		var defrostErr *control.DefrostingError
		if errors.As(err, &defrostErr) {
			retryAfter := defrostErr.RetryAfterSeconds
			if retryAfter <= 0 {
				retryAfter = 10
			}
			_ = grpc.SetTrailer(ctx, metadata.Pairs("retry-after", strconv.Itoa(retryAfter)))
			return nil, status.Error(codes.Unavailable, defrostErr.Error())
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

// CreateVodUpload initiates a multipart upload and returns presigned URLs
func (s *FoghornGRPCServer) CreateVodUpload(ctx context.Context, req *pb.CreateVodUploadRequest) (*pb.CreateVodUploadResponse, error) {
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

	// Generate artifact ID (UUID)
	artifactID := uuid.New().String()

	// Extract format from filename extension (e.g., "video.mp4" → "mp4")
	vodFormat := strings.TrimPrefix(filepath.Ext(req.Filename), ".")
	if vodFormat == "" {
		// Abort the upload - we need a file extension to determine format
		_ = s.s3Client.AbortMultipartUpload(ctx, s3Key, uploadID)
		return nil, status.Errorf(codes.InvalidArgument, "filename must have an extension to determine format")
	}

	// Store artifact in foghorn.artifacts with status='uploading'.
	// storage_cluster_id is set to the resolver-chosen cluster when it
	// differs from the request's cluster_id (origin); when they match the
	// column stays NULL to preserve the prior origin-as-storage semantic.
	storageClusterArg := sql.NullString{}
	if storageCluster != "" && storageCluster != req.GetClusterId() {
		storageClusterArg = sql.NullString{String: storageCluster, Valid: true}
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO foghorn.artifacts (
			id, artifact_hash, artifact_type, internal_name,
			tenant_id, user_id, status,
			sync_status, size_bytes, s3_url, format, origin_cluster_id, storage_cluster_id, retention_until, created_at, updated_at
		)
		VALUES ($1, $2, 'vod', $3, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, 'uploading',
		        'in_progress', $6, $7, $8, $9, $10, NOW() + INTERVAL '30 days', NOW(), NOW())
	`, artifactID, artifactHash, req.GetInternalName(), req.TenantId, req.UserId, req.SizeBytes, s.s3Client.BuildS3URL(s3Key), vodFormat, req.GetClusterId(), storageClusterArg)

	if err != nil {
		// Abort S3 upload since we can't track it
		_ = s.s3Client.AbortMultipartUpload(ctx, s3Key, uploadID)
		s.logger.WithError(err).Error("Failed to store VOD artifact")
		return nil, status.Errorf(codes.Internal, "failed to store artifact: %v", err)
	}

	uploadExpiresAt := time.Now().Add(2 * time.Hour)

	// Store VOD metadata
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO foghorn.vod_metadata (
			artifact_hash, filename, title, description, content_type,
			s3_upload_id, s3_key, upload_expires_at, total_parts, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW(), NOW())
	`, artifactHash, req.Filename, req.GetTitle(), req.GetDescription(), contentType, uploadID, s3Key, uploadExpiresAt, partCount)

	if err != nil {
		s.logger.WithError(err).Error("Failed to store VOD metadata")
		if abortErr := s.s3Client.AbortMultipartUpload(ctx, s3Key, uploadID); abortErr != nil {
			s.logger.WithError(abortErr).WithField("upload_id", uploadID).Warn("Failed to abort multipart upload after metadata write failure")
		}
		if _, markErr := s.db.ExecContext(ctx, `
			UPDATE foghorn.artifacts
			SET status = 'failed',
			    sync_status = 'failed',
			    sync_error = $1,
			    error_message = $1,
			    updated_at = NOW()
			WHERE artifact_hash = $2
		`, fmt.Sprintf("failed to store upload metadata: %v", err), artifactHash); markErr != nil {
			s.logger.WithError(markErr).WithField("artifact_hash", artifactHash).Error("Failed to mark VOD artifact failed after metadata write failure")
		}
		return nil, status.Error(codes.Internal, "failed to store upload metadata")
	}

	s.logger.WithFields(logging.Fields{
		"artifact_hash": artifactHash,
		"upload_id":     uploadID,
		"tenant_id":     req.TenantId,
		"filename":      req.Filename,
		"size_bytes":    req.SizeBytes,
		"part_count":    partCount,
		"part_size":     partSize,
	}).Info("Created VOD multipart upload")

	// Emit VOD lifecycle event to Decklog (STATUS_REQUESTED)
	if s.decklogClient != nil {
		vodData := &pb.VodLifecycleData{
			Status:      pb.VodLifecycleData_STATUS_REQUESTED,
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
		go artifactoutbox.EnqueueVodLifecycleLogged(vodData)
	}

	// Convert storage.UploadPart to proto
	protoParts := make([]*pb.VodUploadPart, len(parts))
	for i, p := range parts {
		protoParts[i] = &pb.VodUploadPart{
			PartNumber:   int32(p.PartNumber),
			PresignedUrl: p.PresignedURL,
		}
	}

	return &pb.CreateVodUploadResponse{
		UploadId:     uploadID,
		ArtifactId:   artifactID,
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
func (s *FoghornGRPCServer) GetVodUploadStatus(ctx context.Context, req *pb.GetVodUploadStatusRequest) (*pb.GetVodUploadStatusResponse, error) {
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

	resp := &pb.GetVodUploadStatusResponse{
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
	case pb.VodStatus_VOD_STATUS_PROCESSING,
		pb.VodStatus_VOD_STATUS_READY,
		pb.VodStatus_VOD_STATUS_FAILED,
		pb.VodStatus_VOD_STATUS_DELETED:
		return resp, nil
	}

	// Expired session: report EXPIRED without paying for a ListParts call.
	if uploadExpiresAt.Valid && time.Now().After(uploadExpiresAt.Time) {
		resp.State = pb.VodStatus_VOD_STATUS_EXPIRED
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
	resp.UploadedParts = make([]*pb.VodUploadedPart, 0, len(uploaded))
	for _, p := range uploaded {
		resp.UploadedParts = append(resp.UploadedParts, &pb.VodUploadedPart{
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
func mapArtifactStatusToVodStatus(s string) pb.VodStatus {
	switch s {
	case "uploading", "requested":
		return pb.VodStatus_VOD_STATUS_UPLOADING
	case "processing":
		return pb.VodStatus_VOD_STATUS_PROCESSING
	case "ready":
		return pb.VodStatus_VOD_STATUS_READY
	case "failed":
		return pb.VodStatus_VOD_STATUS_FAILED
	case "deleted":
		return pb.VodStatus_VOD_STATUS_DELETED
	default:
		return pb.VodStatus_VOD_STATUS_UNSPECIFIED
	}
}

func vodUploadLastErrorCode(state pb.VodStatus, errorMessage string) string {
	if errorMessage == "" {
		return ""
	}
	switch state {
	case pb.VodStatus_VOD_STATUS_FAILED:
		return "processing_failed"
	case pb.VodStatus_VOD_STATUS_DELETED:
		return "deleted"
	default:
		return "artifact_error"
	}
}

// CompleteVodUpload finalizes a multipart upload after all parts are uploaded
func (s *FoghornGRPCServer) CompleteVodUpload(ctx context.Context, req *pb.CompleteVodUploadRequest) (*pb.CompleteVodUploadResponse, error) {
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

	// Get artifact info by upload_id
	// NOTE: tenant_id validation happens at Commodore level (matches clips pattern)
	var artifactHash, s3Key string
	var sizeBytes sql.NullInt64
	var userID sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT v.artifact_hash, v.s3_key, a.size_bytes, a.user_id
		FROM foghorn.vod_metadata v
		JOIN foghorn.artifacts a ON v.artifact_hash = a.artifact_hash
		WHERE v.s3_upload_id = $1 AND a.status = 'uploading' AND a.tenant_id = $2
	`, req.UploadId, req.TenantId).Scan(&artifactHash, &s3Key, &sizeBytes, &userID)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "upload not found or already completed")
	} else if err != nil {
		s.logger.WithError(err).Error("Failed to fetch upload info")
		return nil, status.Error(codes.Internal, "failed to fetch upload info")
	}

	// Convert proto parts to storage parts
	storageParts := make([]storage.CompletedPart, len(req.Parts))
	for i, p := range req.Parts {
		storageParts[i] = storage.CompletedPart{
			PartNumber: int(p.PartNumber),
			ETag:       p.Etag,
		}
	}

	// Complete S3 multipart upload
	err = s.s3Client.CompleteMultipartUpload(ctx, s3Key, req.UploadId, storageParts)
	if err != nil {
		s.logger.WithError(err).Error("Failed to complete S3 multipart upload")
		// Update status to 'failed'
		_, _ = s.db.ExecContext(ctx, `
			UPDATE foghorn.artifacts
			SET status = 'failed',
			    sync_status = 'failed',
			    sync_error = $1,
			    error_message = $1,
			    last_sync_attempt = NOW(),
			    updated_at = NOW()
			WHERE artifact_hash = $2
		`, fmt.Sprintf("S3 upload failed: %v", err), artifactHash)
		// Emit VOD lifecycle event (STATUS_FAILED)
		if s.decklogClient != nil {
			errMsg := fmt.Sprintf("S3 upload failed: %v", err)
			vodData := &pb.VodLifecycleData{
				Status:      pb.VodLifecycleData_STATUS_FAILED,
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
			go artifactoutbox.EnqueueVodLifecycleLogged(vodData)
		}
		return nil, status.Errorf(codes.Internal, "failed to complete upload: %v", err)
	}

	// Update artifact: S3 upload done, start processing pipeline
	s3URL := s.s3Client.BuildS3URL(s3Key)
	_, err = s.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		SET status = 'processing',
		    storage_location = 's3',
		    sync_status = 'synced',
		    sync_error = NULL,
		    last_sync_attempt = NOW(),
		    frozen_at = COALESCE(frozen_at, NOW()),
		    s3_url = COALESCE(s3_url, $2),
		    updated_at = NOW()
		WHERE artifact_hash = $1
	`, artifactHash, s3URL)
	if err != nil {
		s.logger.WithError(err).Error("Failed to update artifact status")
	}

	s.logger.WithFields(logging.Fields{
		"artifact_hash": artifactHash,
		"upload_id":     req.UploadId,
		"tenant_id":     req.TenantId,
		"parts":         len(req.Parts),
	}).Info("Completed VOD multipart upload, starting processing")

	// Queue processing job (metadata extraction + thumbnails + optional transcode).
	// Retry once with a fresh context — the INSERT can fail transiently (connection
	// loss, context timeout from the upload RPC). If both attempts fail, mark the
	// artifact as failed and emit STATUS_FAILED. We don't serve unprocessed VODs.
	pipelineFailed := false
	if vodPipeline != nil {
		startErr := vodPipeline.StartPipeline(ctx, req.TenantId, artifactHash, req.ProcessesJson)
		if startErr != nil {
			s.logger.WithError(startErr).Warn("Processing pipeline INSERT failed, retrying")
			retryCtx, retryCancel := context.WithTimeout(context.Background(), 5*time.Second)
			startErr = vodPipeline.StartPipeline(retryCtx, req.TenantId, artifactHash, req.ProcessesJson)
			retryCancel()
		}
		if startErr != nil {
			s.logger.WithError(startErr).Error("Processing pipeline INSERT failed after retry")
			pipelineFailed = true
			if revertErr := s.markVodArtifactFailed(artifactHash); revertErr != nil {
				s.logger.WithError(revertErr).Error("Failed to mark artifact as failed")
			}
		}
	}

	if s.decklogClient != nil {
		lifecycleStatus := pb.VodLifecycleData_STATUS_PROCESSING
		if pipelineFailed {
			lifecycleStatus = pb.VodLifecycleData_STATUS_FAILED
		}
		vodData := &pb.VodLifecycleData{
			Status:      lifecycleStatus,
			VodHash:     artifactHash,
			UploadId:    &req.UploadId,
			S3Url:       &s3URL,
			TenantId:    &req.TenantId,
			CompletedAt: proto.Int64(time.Now().Unix()),
		}
		if userID.Valid && userID.String != "" {
			vodData.UserId = &userID.String
		}
		if sizeBytes.Valid && sizeBytes.Int64 > 0 {
			vodData.SizeBytes = proto.Uint64(uint64(sizeBytes.Int64))
		}
		go artifactoutbox.EnqueueVodLifecycleLogged(vodData)
	}

	// Fetch and return the asset
	asset, err := s.lookupCompletedUploadAsset(artifactHash, pipelineFailed)
	if err != nil {
		s.logger.WithError(err).Error("Failed to fetch asset after upload completion")
		status := pb.VodStatus_VOD_STATUS_PROCESSING
		if pipelineFailed {
			status = pb.VodStatus_VOD_STATUS_FAILED
		}
		return &pb.CompleteVodUploadResponse{Asset: &pb.VodAssetInfo{
			ArtifactHash: artifactHash,
			Status:       status,
		}}, nil
	}

	return &pb.CompleteVodUploadResponse{Asset: asset}, nil
}

// AbortVodUpload cancels an in-progress multipart upload
func (s *FoghornGRPCServer) AbortVodUpload(ctx context.Context, req *pb.AbortVodUploadRequest) (*pb.AbortVodUploadResponse, error) {
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

	// Abort S3 multipart upload
	err = s.s3Client.AbortMultipartUpload(ctx, s3Key, req.UploadId)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to abort S3 multipart upload")
		// Continue to delete the database record anyway
	}

	// Delete artifact and metadata
	_, _ = s.db.ExecContext(ctx, `DELETE FROM foghorn.vod_metadata WHERE artifact_hash = $1`, artifactHash)
	_, err = s.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		SET status = 'deleted', updated_at = NOW()
		WHERE artifact_hash = $1
	`, artifactHash)
	if err != nil {
		s.logger.WithError(err).Error("Failed to delete aborted artifact")
		return nil, status.Error(codes.Internal, "failed to clean up aborted upload")
	}

	s.logger.WithFields(logging.Fields{
		"artifact_hash": artifactHash,
		"upload_id":     req.UploadId,
		"tenant_id":     req.TenantId,
	}).Info("Aborted VOD multipart upload")

	// Emit VOD lifecycle event (STATUS_DELETED)
	if s.decklogClient != nil {
		vodData := &pb.VodLifecycleData{
			Status:      pb.VodLifecycleData_STATUS_DELETED,
			VodHash:     artifactHash,
			UploadId:    &req.UploadId,
			TenantId:    &req.TenantId,
			CompletedAt: proto.Int64(time.Now().Unix()),
		}
		if userID.Valid && userID.String != "" {
			vodData.UserId = &userID.String
		}
		go artifactoutbox.EnqueueVodLifecycleLogged(vodData)
	}

	return &pb.AbortVodUploadResponse{
		Success: true,
		Message: "upload aborted successfully",
	}, nil
}

// GetVodAsset returns a single VOD asset by hash
func (s *FoghornGRPCServer) GetVodAsset(ctx context.Context, req *pb.GetVodAssetRequest) (*pb.VodAssetInfo, error) {
	// NOTE: tenant_id validation happens at Commodore level (matches clips pattern)
	if req.ArtifactHash == "" {
		return nil, status.Error(codes.InvalidArgument, "artifact_hash is required")
	}

	asset, err := s.getVodAssetInfo(ctx, req.ArtifactHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "VOD asset not found")
		}
		s.logger.WithError(err).Error("Failed to fetch VOD asset")
		return nil, status.Error(codes.Internal, "failed to fetch VOD asset")
	}

	return asset, nil
}

// ListVodAssets returns paginated list of VOD assets
// NOTE: Tenant-wide queries should go through Commodore.ListVodAssets (business registry owner)
// This Foghorn endpoint is for lifecycle data queries, matching clips pattern
func (s *FoghornGRPCServer) ListVodAssets(ctx context.Context, req *pb.ListVodAssetsRequest) (*pb.ListVodAssetsResponse, error) {
	// NOTE: tenant_id validation happens at Commodore level (matches clips pattern)
	// Tenant-wide VOD listing should go through Commodore.ListVodAssets
	// This endpoint returns lifecycle data for artifact-specific queries

	// Parse bidirectional keyset pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	builder := &pagination.KeysetBuilder{
		TimestampColumn: "a.created_at",
		IDColumn:        "a.artifact_hash",
	}

	// Build base WHERE clause - no tenant_id filter (matches clips pattern)
	baseWhere := "a.artifact_type = 'vod' AND a.status != 'deleted'"
	args := []any{}
	argIdx := 1

	// Count total
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM foghorn.artifacts a WHERE %s", baseWhere)
	var total int32
	if errCount := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); errCount != nil {
		s.logger.WithError(errCount).Error("Failed to count VOD assets")
		return nil, status.Error(codes.Internal, "failed to count VOD assets")
	}

	// Build select query with keyset pagination
	selectQuery := fmt.Sprintf(`
		SELECT a.id, a.artifact_hash, a.status, a.size_bytes,
		       COALESCE(a.storage_location, 'pending'), COALESCE(a.s3_url, ''),
		       a.error_message, a.created_at, a.updated_at, a.retention_until,
		       COALESCE(v.filename, ''), COALESCE(v.title, ''), COALESCE(v.description, ''),
		       v.duration_ms, v.resolution, v.video_codec, v.audio_codec, v.bitrate_kbps,
		       COALESCE(v.s3_upload_id, ''), COALESCE(v.s3_key, '')
		FROM foghorn.artifacts a
		LEFT JOIN foghorn.vod_metadata v ON a.artifact_hash = v.artifact_hash
		WHERE %s`, baseWhere)

	// Add keyset condition if cursor provided
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		selectQuery += " AND " + condition
		args = append(args, cursorArgs...)
	}

	// Add ORDER BY and LIMIT
	selectQuery += " " + builder.OrderBy(params)
	selectQuery += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	// Fetch assets
	rows, err := s.db.QueryContext(ctx, selectQuery, args...)
	if err != nil {
		s.logger.WithError(err).Error("Failed to fetch VOD assets")
		return nil, status.Error(codes.Internal, "failed to fetch VOD assets")
	}
	defer rows.Close()

	var assets []*pb.VodAssetInfo
	for rows.Next() {
		asset, err := s.scanVodAsset(rows)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan VOD asset")
			continue
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

	// Build response with proper hasNextPage/hasPreviousPage
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

// DeleteVodAsset deletes a VOD asset
func (s *FoghornGRPCServer) DeleteVodAsset(ctx context.Context, req *pb.DeleteVodAssetRequest) (*pb.DeleteVodAssetResponse, error) {
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
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT a.status, COALESCE(v.s3_key, ''), a.s3_url, a.format,
		       a.size_bytes, a.retention_until, a.user_id,
		       a.storage_cluster_id, a.origin_cluster_id
		FROM foghorn.artifacts a
		LEFT JOIN foghorn.vod_metadata v ON a.artifact_hash = v.artifact_hash
		WHERE a.artifact_hash = $1 AND a.artifact_type = 'vod' AND a.tenant_id = $2
	`, req.ArtifactHash, req.GetTenantId()).Scan(&currentStatus, &s3Key, &s3URL, &formatStr, &sizeBytes, &retentionUntil, &userID, &storageClusterID, &originClusterID)

	if errors.Is(err, sql.ErrNoRows) {
		if handled, _ := s.forwardArtifactToFederation(ctx, "delete_vod", req.ArtifactHash, req.GetTenantId(), ""); handled {
			return &pb.DeleteVodAssetResponse{Success: true, Message: "VOD deleted via federation"}, nil
		}
		return nil, status.Error(codes.NotFound, "VOD asset not found")
	} else if err != nil {
		s.logger.WithError(err).Error("Failed to check VOD asset")
		return nil, status.Error(codes.Internal, "failed to check VOD asset")
	}

	if currentStatus == "deleted" {
		return &pb.DeleteVodAssetResponse{
			Success: false,
			Message: "VOD asset is already deleted",
		}, nil
	}

	// If uploading, abort the multipart upload first
	if currentStatus == "uploading" {
		var uploadID string
		_ = s.db.QueryRowContext(ctx, `
			SELECT s3_upload_id FROM foghorn.vod_metadata WHERE artifact_hash = $1
		`, req.ArtifactHash).Scan(&uploadID)

		if uploadID != "" && s3Key != "" && s.s3Client != nil {
			_ = s.s3Client.AbortMultipartUpload(ctx, s3Key, uploadID)
		}
	}

	cleanupErrors := make([]string, 0, 2)

	// Send delete request to nodes that have this VOD cached
	rows, err := s.db.QueryContext(ctx, `
		SELECT node_id FROM foghorn.artifact_nodes
		WHERE artifact_hash = $1 AND NOT is_orphaned
	`, req.ArtifactHash)
	if err == nil {
		defer func() { _ = rows.Close() }()
		requestID := uuid.NewString()
		for rows.Next() {
			var nodeID string
			if scanErr := rows.Scan(&nodeID); scanErr != nil {
				continue
			}
			deleteReq := &pb.VodDeleteRequest{
				VodHash:   req.ArtifactHash,
				RequestId: requestID,
			}
			if sendErr := control.SendVodDelete(nodeID, deleteReq); sendErr != nil {
				cleanupErrors = append(cleanupErrors, fmt.Sprintf("node %s cleanup pending: %v", nodeID, sendErr))
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

	// Delete from S3 immediately (cross-cluster aware via the federation
	// delete delegate). The cleaner derives the target from
	// vod_metadata.s3_key, falling back to a.s3_url and finally to the
	// deterministic BuildVodS3Key shape so VODs whose s3_key was never
	// recorded still get cleaned. Failure marks cleanup-pending; soft-
	// delete still proceeds so the row enters the purge cycle for
	// retries.
	if currentStatus != "uploading" {
		if s.artifactCleaner == nil {
			cleanupErrors = append(cleanupErrors, "s3 cleanup pending: cleaner not wired")
			s.logger.WithField("artifact_hash", req.ArtifactHash).Warn("Artifact cleaner not wired; VOD S3 cleanup deferred to purge job")
		} else if errDelete := s.artifactCleaner.Delete(ctx, artifacts.ArtifactRef{
			Hash:             req.ArtifactHash,
			Type:             "vod",
			TenantID:         req.GetTenantId(),
			Format:           formatStr.String,
			VODS3Key:         s3Key,
			S3URL:            s3URL.String,
			StorageClusterID: storageClusterID.String,
			OriginClusterID:  originClusterID.String,
		}); errDelete != nil {
			cleanupErrors = append(cleanupErrors, fmt.Sprintf("s3 cleanup pending: %v", errDelete))
			s.logger.WithFields(logging.Fields{
				"artifact_hash": req.ArtifactHash,
				"s3_key":        s3Key,
				"error":         errDelete,
			}).Warn("Failed to delete from S3, will be retried by purge job")
		}
	}

	// Soft delete in foghorn.artifacts
	_, err = s.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts SET status = 'deleted', updated_at = NOW()
		WHERE artifact_hash = $1 AND artifact_type = 'vod'
	`, req.ArtifactHash)
	if err != nil {
		s.logger.WithError(err).Error("Failed to delete VOD asset")
		return nil, status.Error(codes.Internal, "failed to delete VOD asset")
	}

	s.logger.WithFields(logging.Fields{
		"artifact_hash": req.ArtifactHash,
		"tenant_id":     req.TenantId,
	}).Info("VOD asset soft-deleted successfully")

	// Emit VOD lifecycle event (STATUS_DELETED)
	if s.decklogClient != nil {
		var cleanupError string
		if len(cleanupErrors) > 0 {
			cleanupError = strings.Join(cleanupErrors, "; ")
		}
		vodData := &pb.VodLifecycleData{
			Status:      pb.VodLifecycleData_STATUS_DELETED,
			VodHash:     req.ArtifactHash,
			TenantId:    &req.TenantId,
			CompletedAt: proto.Int64(time.Now().Unix()),
		}
		if cleanupError != "" {
			vodData.Error = &cleanupError
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
		go artifactoutbox.EnqueueVodLifecycleLogged(vodData)
	}

	return &pb.DeleteVodAssetResponse{
		Success: true,
		Message: "VOD asset deleted successfully",
	}, nil
}

// Helper functions for VOD service

func (s *FoghornGRPCServer) getVodAssetInfo(ctx context.Context, artifactHash string) (*pb.VodAssetInfo, error) {
	query := `
		SELECT a.id, a.artifact_hash, a.status, a.size_bytes,
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

func (s *FoghornGRPCServer) markVodArtifactFailed(artifactHash string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := s.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		SET status = 'failed', updated_at = NOW()
		WHERE artifact_hash = $1
	`, artifactHash)
	return err
}

func (s *FoghornGRPCServer) lookupCompletedUploadAsset(artifactHash string, pipelineFailed bool) (*pb.VodAssetInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	asset, err := s.getVodAssetInfo(ctx, artifactHash)
	if err == nil {
		return asset, nil
	}
	if pipelineFailed {
		return &pb.VodAssetInfo{
			ArtifactHash: artifactHash,
			Status:       pb.VodStatus_VOD_STATUS_FAILED,
		}, nil
	}
	return nil, err
}

func (s *FoghornGRPCServer) scanVodAsset(rows *sql.Rows) (*pb.VodAssetInfo, error) {
	var id, artifactHash, statusStr, storageLocation, s3URL, filename, title, description string
	var videoCodec, audioCodec, resolution, s3UploadID, s3Key sql.NullString
	var sizeBytes sql.NullInt64
	var durationMs, bitrateKbps sql.NullInt32
	var errorMessage sql.NullString
	var createdAt, updatedAt time.Time
	var expiresAt sql.NullTime

	err := rows.Scan(
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

func (s *FoghornGRPCServer) scanVodAssetRow(row *sql.Row) (*pb.VodAssetInfo, error) {
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
) *pb.VodAssetInfo {
	// Map status string to proto enum
	var protoStatus pb.VodStatus
	switch statusStr {
	case "uploading":
		protoStatus = pb.VodStatus_VOD_STATUS_UPLOADING
	case "processing":
		protoStatus = pb.VodStatus_VOD_STATUS_PROCESSING
	case "ready":
		protoStatus = pb.VodStatus_VOD_STATUS_READY
	case "failed":
		protoStatus = pb.VodStatus_VOD_STATUS_FAILED
	case "deleted":
		protoStatus = pb.VodStatus_VOD_STATUS_DELETED
	default:
		protoStatus = pb.VodStatus_VOD_STATUS_UNSPECIFIED
	}

	asset := &pb.VodAssetInfo{
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

func (s *FoghornGRPCServer) emitClipDeletedLifecycle(
	ctx context.Context,
	clipHash string,
	nodeID string,
	sizeBytes sql.NullInt64,
	retentionUntil sql.NullTime,
	internalName sql.NullString,
	denormTenantID sql.NullString,
	denormUserID sql.NullString,
	cleanupError string,
) {
	if s.decklogClient == nil {
		return
	}

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

	clipData := &pb.ClipLifecycleData{
		Stage:    pb.ClipLifecycleData_STAGE_DELETED,
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

	go artifactoutbox.EnqueueClipLifecycleLogged(clipData)
}

func (s *FoghornGRPCServer) emitDVRDeletedLifecycle(
	ctx context.Context,
	dvrHash string,
	nodeID string,
	sizeBytes sql.NullInt64,
	retentionUntil sql.NullTime,
	startedAt sql.NullTime,
	endedAt sql.NullTime,
	internalName string,
	denormTenantID sql.NullString,
	denormUserID sql.NullString,
	cleanupError string,
) {
	if s.decklogClient == nil {
		return
	}

	var (
		tenantIDStr     string
		userIDStr       string
		internalNameStr string
		streamID        string
	)

	if denormTenantID.Valid {
		tenantIDStr = denormTenantID.String
	}
	if denormUserID.Valid {
		userIDStr = denormUserID.String
	}
	if internalName != "" {
		internalNameStr = internalName
	}

	if control.CommodoreClient != nil {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if resp, err := control.CommodoreClient.ResolveDVRHash(cctx, dvrHash); err == nil && resp.Found {
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
		}
	}

	dvrData := &pb.DVRLifecycleData{
		Status:  pb.DVRLifecycleData_STATUS_DELETED,
		DvrHash: dvrHash,
	}
	if cleanupError != "" {
		dvrData.Error = &cleanupError
	}
	if nodeID != "" {
		dvrData.NodeId = &nodeID
	}
	if tenantIDStr != "" {
		dvrData.TenantId = &tenantIDStr
	}
	if internalNameStr != "" {
		dvrData.StreamInternalName = &internalNameStr
	}
	if streamID != "" {
		dvrData.StreamId = &streamID
	}
	if userIDStr != "" {
		dvrData.UserId = &userIDStr
	}
	if sizeBytes.Valid && sizeBytes.Int64 > 0 {
		sb := uint64(sizeBytes.Int64)
		dvrData.SizeBytes = &sb
	}
	if retentionUntil.Valid {
		exp := retentionUntil.Time.Unix()
		dvrData.ExpiresAt = &exp
	}
	if startedAt.Valid {
		st := startedAt.Time.Unix()
		dvrData.StartedAt = &st
	}
	if endedAt.Valid {
		et := endedAt.Time.Unix()
		dvrData.EndedAt = &et
	}

	go artifactoutbox.EnqueueDVRLifecycleLogged(dvrData)
}

// TerminateTenantStreams stops all active streams for a suspended tenant
// Called by Purser when a tenant's prepaid balance drops below -$10
func (s *FoghornGRPCServer) TerminateTenantStreams(ctx context.Context, req *pb.TerminateTenantStreamsRequest) (*pb.TerminateTenantStreamsResponse, error) {
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
		return &pb.TerminateTenantStreamsResponse{
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
		stopReq := &pb.StopSessionsRequest{
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

	return &pb.TerminateTenantStreamsResponse{
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
func (s *FoghornGRPCServer) InvalidatePlaybackAuth(ctx context.Context, req *pb.InvalidatePlaybackAuthRequest) (*pb.InvalidatePlaybackAuthResponse, error) {
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
		return &pb.InvalidatePlaybackAuthResponse{}, nil
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
		invReq := &pb.InvalidateSessionsRequest{
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

	return &pb.InvalidatePlaybackAuthResponse{
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
func (s *FoghornGRPCServer) InvalidateTenantCache(ctx context.Context, req *pb.InvalidateTenantCacheRequest) (*pb.InvalidateTenantCacheResponse, error) {
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	if s.cacheInvalidator == nil {
		s.logger.WithField("tenant_id", req.TenantId).Warn("Cache invalidator not configured, skipping cache invalidation")
		return &pb.InvalidateTenantCacheResponse{
			EntriesInvalidated: 0,
		}, nil
	}

	entriesInvalidated := s.cacheInvalidator.InvalidateTenantCache(req.TenantId)

	s.logger.WithFields(logging.Fields{
		"tenant_id":           req.TenantId,
		"reason":              req.Reason,
		"entries_invalidated": entriesInvalidated,
	}).Info("Invalidated tenant cache entries")

	return &pb.InvalidateTenantCacheResponse{
		EntriesInvalidated: int32(entriesInvalidated),
	}, nil
}

// SetNodeOperationalMode changes a node's operational mode with tenant ownership validation.
func (s *FoghornGRPCServer) SetNodeOperationalMode(ctx context.Context, req *pb.SetNodeModeRequest) (*pb.SetNodeModeResponse, error) {
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

	return &pb.SetNodeModeResponse{
		NodeId:  nodeID,
		Mode:    string(state.DefaultManager().GetNodeOperationalMode(nodeID)),
		Message: fmt.Sprintf("Node %s set to %s", nodeID, mode),
		Status:  pb.SetNodeModeStatus_SET_NODE_MODE_STATUS_SUCCESS,
	}, nil
}

// GetNodeHealth returns real-time health and routing state for a node.
func (s *FoghornGRPCServer) GetNodeHealth(ctx context.Context, req *pb.GetNodeHealthRequest) (*pb.GetNodeHealthResponse, error) {
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

	resp := &pb.GetNodeHealthResponse{
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

func (s *FoghornGRPCServer) loadNodeComponentVersions(ctx context.Context, nodeID string) []*pb.NodeComponentVersion {
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
	var out []*pb.NodeComponentVersion
	for rows.Next() {
		v := &pb.NodeComponentVersion{}
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
// average_storage_gb billing meter (which is time-averaged and drives
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
