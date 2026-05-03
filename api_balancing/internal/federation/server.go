package federation

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ClipCreator creates clips on the local cluster. Implemented by FoghornGRPCServer.
type ClipCreator interface {
	CreateClip(ctx context.Context, req *pb.CreateClipRequest) (*pb.CreateClipResponse, error)
}

// DVRCreator starts DVR recordings on the local cluster. Implemented by FoghornGRPCServer.
type DVRCreator interface {
	StartDVR(ctx context.Context, req *pb.StartDVRRequest) (*pb.StartDVRResponse, error)
}

// ArtifactCommandHandler dispatches artifact lifecycle commands locally.
// Implemented by FoghornGRPCServer; used by ForwardArtifactCommand.
type ArtifactCommandHandler interface {
	DeleteClip(ctx context.Context, req *pb.DeleteClipRequest) (*pb.DeleteClipResponse, error)
	StopDVR(ctx context.Context, req *pb.StopDVRRequest) (*pb.StopDVRResponse, error)
	DeleteDVR(ctx context.Context, req *pb.DeleteDVRRequest) (*pb.DeleteDVRResponse, error)
	DeleteVodAsset(ctx context.Context, req *pb.DeleteVodAssetRequest) (*pb.DeleteVodAssetResponse, error)
}

// FederationS3Client abstracts S3 operations used by federation so tests
// can inject fakes without real AWS credentials.
type FederationS3Client interface {
	GeneratePresignedGET(key string, expiry time.Duration) (string, error)
	GeneratePresignedPUT(key string, expiry time.Duration) (string, error)
	GeneratePresignedURLsForDVR(dvrPrefix string, isUpload bool, expiry time.Duration) (map[string]string, error)
	BuildClipS3Key(tenantID, streamName, clipHash, format string) string
	BuildDVRS3Key(tenantID, internalName, dvrHash string) string
	BuildVodS3Key(tenantID, artifactHash, filename string) string
}

// FederationServer implements the FoghornFederation gRPC service.
// It handles cross-cluster stream queries, origin-pull notifications,
// artifact preparation, and bidirectional telemetry via PeerChannel.
type FederationServer struct {
	pb.UnimplementedFoghornFederationServer
	logger          logging.Logger
	lb              *balancer.LoadBalancer
	clusterID       string
	cache           *RemoteEdgeCache
	db              *sql.DB
	s3Client        FederationS3Client
	clipCreator     ClipCreator
	dvrCreator      DVRCreator
	artifactHandler ArtifactCommandHandler
	peerManager     PeerAddrResolver
	fedClient       *FederationClient

	// Storage-cluster ownership inputs. Both PrepareArtifact (read-side
	// redirect) and MintStorageURLs (write-side ownership check) consult
	// these. Wired from cmd/foghorn/main.go; unset in tests by default
	// (canLocallyMintFor returns false for any non-empty target, which is
	// the conservative answer when we don't know).
	localS3Backing       S3Backing
	advertisedBacking    AdvertisedBackingFunc
	isServedCluster      func(clusterID string) bool
	mintArtifactResolver MintArtifactResolver

	// storageMintCounter records MintStorageURLs outcomes. Optional;
	// nil-safe (the handler only increments when the counter is set).
	storageMintCounter *prometheus.CounterVec
}

// SetMintArtifactResolver wires the Commodore-backed authoritative
// tenant + asset-context resolver used by MintStorageURLs as the
// fallback when the local foghorn.artifacts row isn't on this pool.
// Optional; absent ⇒ callee accepts only locally-cached rows.
func (s *FederationServer) SetMintArtifactResolver(r MintArtifactResolver) {
	s.mintArtifactResolver = r
}

// SetStorageMintMetric wires the MintStorageURLs outcome counter (label:
// result). Production wires this from cmd/foghorn/main.go alongside the
// other federation metrics; tests can leave it unset.
func (s *FederationServer) SetStorageMintMetric(c *prometheus.CounterVec) {
	s.storageMintCounter = c
}

func (s *FederationServer) recordStorageMint(result string) {
	if s.storageMintCounter == nil {
		return
	}
	s.storageMintCounter.WithLabelValues(result).Inc()
}

// AdvertisedBackingFunc returns the S3 backing tuple Quartermaster has on
// record for the (tenant, cluster) pair, sourced from the tenant's
// cluster_peers metadata. ok=false when the tenant doesn't have access to
// the cluster or the cluster doesn't advertise S3 backing.
type AdvertisedBackingFunc func(ctx context.Context, tenantID, clusterID string) (S3Backing, bool)

// MintArtifactResolver is the Commodore surface MintStorageURLs uses as
// the authoritative tenant-binding source when the local foghorn.artifacts
// row hasn't been seen on this Foghorn pool yet (delegated mints from a
// peer pool that wrote the row, or live thumbnails which never have a
// row). Mirrors the Resolve*Hash chain processFreezePermissionRequest
// uses on the producer side. Production wires this to *commodore.GRPCClient;
// tests can pass a stub or leave it nil to skip the Commodore fallback
// (callee then accepts only locally-cached rows).
type MintArtifactResolver interface {
	ResolveInternalName(ctx context.Context, internalName string) (*pb.ResolveInternalNameResponse, error)
	ResolveClipHash(ctx context.Context, clipHash string) (*pb.ResolveClipHashResponse, error)
	ResolveDVRHash(ctx context.Context, dvrHash string) (*pb.ResolveDVRHashResponse, error)
	ResolveVodHash(ctx context.Context, vodHash string) (*pb.ResolveVodHashResponse, error)
}

// S3Backing is the federation server's view of an S3 backing tuple.
// Equality on the full tuple is required for "this Foghorn pool can mint
// locally for that cluster" — bucket-name match alone collides across
// S3-compatible providers (MinIO, R2, Bunny Storage). Mirrors
// storage.S3Backing without the import dependency to keep package layering
// clean.
type S3Backing struct {
	Bucket   string
	Endpoint string
	Region   string
}

func (b S3Backing) Equal(other S3Backing) bool {
	return strings.EqualFold(strings.TrimSpace(b.Bucket), strings.TrimSpace(other.Bucket)) &&
		strings.EqualFold(strings.TrimSpace(b.Endpoint), strings.TrimSpace(other.Endpoint)) &&
		strings.EqualFold(strings.TrimSpace(b.Region), strings.TrimSpace(other.Region))
}

// PeerAddrResolver resolves gRPC addresses for peer clusters.
type PeerAddrResolver interface {
	GetPeerAddr(clusterID string) string
}

// FederationServerConfig holds dependencies for the federation server.
type FederationServerConfig struct {
	Logger          logging.Logger
	LB              *balancer.LoadBalancer
	ClusterID       string
	Cache           *RemoteEdgeCache
	DB              *sql.DB
	S3Client        FederationS3Client
	ClipCreator     ClipCreator
	DVRCreator      DVRCreator
	ArtifactHandler ArtifactCommandHandler
	PeerManager     PeerAddrResolver
	FedClient       *FederationClient

	// Storage ownership inputs (optional; when unset, canLocallyMintFor
	// returns false and PrepareArtifact emits redirect for any non-empty
	// authoritative cluster). Production wires all three from
	// cmd/foghorn/main.go; tests can omit them or set focused stubs.
	LocalS3Backing    S3Backing
	AdvertisedBacking AdvertisedBackingFunc
	IsServedCluster   func(clusterID string) bool
}

// NewFederationServer creates a new federation gRPC server.
func NewFederationServer(cfg FederationServerConfig) *FederationServer {
	return &FederationServer{
		logger:            cfg.Logger,
		lb:                cfg.LB,
		clusterID:         cfg.ClusterID,
		cache:             cfg.Cache,
		db:                cfg.DB,
		s3Client:          cfg.S3Client,
		clipCreator:       cfg.ClipCreator,
		dvrCreator:        cfg.DVRCreator,
		artifactHandler:   cfg.ArtifactHandler,
		peerManager:       cfg.PeerManager,
		fedClient:         cfg.FedClient,
		localS3Backing:    cfg.LocalS3Backing,
		advertisedBacking: cfg.AdvertisedBacking,
		isServedCluster:   cfg.IsServedCluster,
	}
}

// canLocallyMintFor reports whether this Foghorn pool can mint presigned
// URLs against the named storage cluster's S3 for the given tenant.
// Requires: the cluster is served locally AND the local S3 client's backing
// tuple matches the cluster's advertised backing per Quartermaster's
// cluster_peers metadata for this tenant. Returns false for empty inputs or
// when ownership inputs aren't configured (tests).
//
// Unified ownership rule used by both PrepareArtifact (redirect emit when
// remote owns) and MintStorageURLs (callee validation that we actually own
// what the caller claims). Keeping the rule shared prevents asymmetry where
// a pool would redirect a read but accept a write to the same cluster.
func (s *FederationServer) canLocallyMintFor(ctx context.Context, tenantID, targetClusterID string) bool {
	tenant := strings.TrimSpace(tenantID)
	target := strings.TrimSpace(targetClusterID)
	if tenant == "" || target == "" {
		return false
	}
	if s.isServedCluster == nil || !s.isServedCluster(target) {
		return false
	}
	if s.advertisedBacking == nil {
		return false
	}
	advertised, ok := s.advertisedBacking(ctx, tenant, target)
	if !ok || strings.TrimSpace(advertised.Bucket) == "" {
		return false
	}
	return s.localS3Backing.Equal(advertised)
}

// SetClipCreator wires the clip creation delegate (set after FoghornGRPCServer is created).
func (s *FederationServer) SetClipCreator(cc ClipCreator) { s.clipCreator = cc }

// SetDVRCreator wires the DVR creation delegate (set after FoghornGRPCServer is created).
func (s *FederationServer) SetDVRCreator(dc DVRCreator) { s.dvrCreator = dc }

// SetArtifactCommandHandler wires the artifact command delegate (set after FoghornGRPCServer is created).
func (s *FederationServer) SetArtifactCommandHandler(h ArtifactCommandHandler) {
	s.artifactHandler = h
}

// RegisterServices registers the FoghornFederation service on the gRPC server.
func (s *FederationServer) RegisterServices(srv *grpc.Server) {
	pb.RegisterFoghornFederationServer(srv, s)
}

// QueryStream handles a peer cluster asking whether we have a stream and
// returns scored local edge candidates. Reuses the existing load balancer
// scoring algorithm and enriches results with DTSC URLs for origin-pull.
func (s *FederationServer) QueryStream(ctx context.Context, req *pb.QueryStreamRequest) (*pb.QueryStreamResponse, error) {
	if err := requireFederationServiceAuth(ctx); err != nil {
		return nil, err
	}
	if req.StreamName == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_name required")
	}
	if req.RequestingCluster == "" {
		return nil, status.Error(codes.InvalidArgument, "requesting_cluster required")
	}
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}
	if req.RequestingCluster == s.clusterID {
		return nil, status.Error(codes.InvalidArgument, "cannot query own cluster")
	}

	// Shared-LB tenant isolation: only return candidates if the stream belongs to the requested tenant
	sm := state.DefaultManager()
	if sm != nil {
		ss := sm.GetStreamState(req.StreamName)
		if ss != nil && ss.TenantID != "" && ss.TenantID != req.TenantId {
			return &pb.QueryStreamResponse{OriginClusterId: s.clusterID}, nil
		}
	}

	log := s.logger.WithFields(logging.Fields{
		"stream":             req.StreamName,
		"requesting_cluster": req.RequestingCluster,
		"viewer_lat":         req.ViewerLat,
		"viewer_lon":         req.ViewerLon,
	})

	// Score local nodes for this stream using the existing load balancer.
	// isSourceSelection=true restricts to nodes with active inputs (origin nodes only).
	lbctx := context.WithValue(ctx, ctxkeys.KeyClusterScope, req.TenantId)
	nodes, err := s.lb.GetTopNodesWithScores(lbctx, req.StreamName, req.ViewerLat, req.ViewerLon, nil, "", 10, req.IsSourceSelection)
	if err != nil {
		log.WithError(err).Debug("No local candidates for federated query")
		return &pb.QueryStreamResponse{OriginClusterId: s.clusterID}, nil
	}

	candidates := make([]*pb.EdgeCandidate, 0, len(nodes))
	sm = state.DefaultManager()

	for _, n := range nodes {
		ns := sm.GetNodeState(n.NodeID)
		if ns == nil {
			continue
		}

		dtscURL := control.BuildDTSCURI(n.NodeID, req.StreamName, true, s.logger)

		ss := sm.GetStreamState(req.StreamName)
		var bufferState string
		if ss != nil && ss.NodeID == n.NodeID {
			bufferState = ss.BufferState
		}

		var viewerCount uint32
		viewers := sm.GetNodeActiveViewers(n.NodeID)
		if viewers > 0 {
			viewerCount = uint32(viewers)
		}

		isOrigin := false
		if ss != nil && ss.NodeID == n.NodeID {
			isOrigin = ss.Status == "live" && ss.Inputs > 0
		}

		candidate := &pb.EdgeCandidate{
			NodeId:      n.NodeID,
			BaseUrl:     ns.BaseURL,
			DtscUrl:     dtscURL,
			BwScore:     n.Score,
			GeoScore:    0, // geo is baked into the composite score
			IsOrigin:    isOrigin,
			BufferState: bufferState,
			GeoLat:      n.GeoLatitude,
			GeoLon:      n.GeoLongitude,
			ViewerCount: viewerCount,
			CpuPercent:  ns.CPU,
			BwAvailable: AvailBandwidthFromNodeState(ns),
			RamUsed:     uint64(ns.RAMCurrent),
			RamMax:      uint64(ns.RAMMax),
		}
		candidates = append(candidates, candidate)
	}

	log.WithField("candidates", len(candidates)).Info("Federated QueryStream responded")
	return &pb.QueryStreamResponse{
		Candidates:      candidates,
		OriginClusterId: s.clusterID,
	}, nil
}

// NotifyOriginPull handles a peer telling us they intend to pull a stream.
// We validate the stream exists locally, select the best source node,
// build a DTSC URL, and store an active replication record.
func (s *FederationServer) NotifyOriginPull(ctx context.Context, req *pb.OriginPullNotification) (*pb.OriginPullAck, error) {
	if err := requireFederationServiceAuth(ctx); err != nil {
		return nil, err
	}
	if req.StreamName == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_name required")
	}
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}
	if req.DestClusterId == "" {
		return nil, status.Error(codes.InvalidArgument, "dest_cluster_id required")
	}

	log := s.logger.WithFields(logging.Fields{
		"stream":       req.StreamName,
		"dest_cluster": req.DestClusterId,
		"dest_node":    req.DestNodeId,
	})
	if s.cache == nil {
		return &pb.OriginPullAck{Accepted: false, Reason: "origin-pull cache unavailable"}, nil
	}

	// Find best source node for the stream
	sourceNodeID := req.SourceNodeId
	if sourceNodeID == "" {
		lbCtx := context.WithValue(ctx, ctxkeys.KeyClusterScope, req.TenantId)
		// Auto-select: use load balancer to find best node with this stream
		// isSourceSelection=true to find nodes with active inputs (not replicated)
		bestHost, _, _, _, _, err := s.lb.GetBestNodeWithScore(lbCtx, req.StreamName, 0, 0, nil, "", true)
		if err != nil {
			log.WithError(err).Warn("No source node available for origin-pull")
			return &pb.OriginPullAck{
				Accepted: false,
				Reason:   "no source node available: " + err.Error(),
			}, nil
		}
		sourceNodeID = s.lb.GetNodeIDByHost(bestHost)
		if sourceNodeID == "" {
			return &pb.OriginPullAck{
				Accepted: false,
				Reason:   "could not resolve source node ID",
			}, nil
		}
	}

	// Verify stream exists on the selected source
	sm := state.DefaultManager()
	ss := sm.GetStreamState(req.StreamName)
	if ss == nil {
		return &pb.OriginPullAck{
			Accepted: false,
			Reason:   "stream not found locally",
		}, nil
	}
	if req.TenantId != "" && ss.TenantID != "" && ss.TenantID != req.TenantId {
		return &pb.OriginPullAck{
			Accepted: false,
			Reason:   "stream tenant mismatch",
		}, nil
	}

	// Build DTSC pull URL
	dtscURL := control.BuildDTSCURI(sourceNodeID, req.StreamName, true, s.logger)
	if dtscURL == "" {
		return &pb.OriginPullAck{
			Accepted: false,
			Reason:   "could not build DTSC URI for source node",
		}, nil
	}

	// Get source node's base URL for the active replication record
	ns := sm.GetNodeState(sourceNodeID)
	baseURL := ""
	if ns != nil {
		baseURL = ns.BaseURL
	}

	// Store active replication record (bridges gap until stream appears in local RIB)
	record := &ActiveReplicationRecord{
		StreamName:    req.StreamName,
		SourceNodeID:  sourceNodeID,
		SourceCluster: s.clusterID,
		DestCluster:   req.DestClusterId,
		DestNodeID:    req.DestNodeId,
		DTSCURL:       dtscURL,
		BaseURL:       baseURL,
		CreatedAt:     time.Now(),
	}
	if err := s.cache.SetActiveReplication(ctx, record); err != nil {
		log.WithError(err).Error("Failed to store active replication record")
		return &pb.OriginPullAck{
			Accepted: false,
			Reason:   "origin-pull temporarily unavailable",
		}, nil
	}

	log.WithFields(logging.Fields{
		"source_node": sourceNodeID,
		"dtsc_url":    dtscURL,
	}).Info("Origin-pull accepted")

	return &pb.OriginPullAck{
		Accepted: true,
		DtscUrl:  dtscURL,
	}, nil
}

// PrepareArtifact handles cross-cluster artifact preparation requests.
// The origin cluster looks up the artifact in its local DB, verifies it is
// synced to S3, and returns presigned GET URLs so the requesting cluster
// can defrost the artifact without sharing S3 credentials.
func (s *FederationServer) PrepareArtifact(ctx context.Context, req *pb.PrepareArtifactRequest) (*pb.PrepareArtifactResponse, error) {
	if err := requireFederationServiceAuth(ctx); err != nil {
		return nil, err
	}
	hash := req.GetArtifactId()
	if hash == "" {
		hash = req.GetClipHash()
	}
	if hash == "" {
		return nil, status.Error(codes.InvalidArgument, "artifact_id or clip_hash required")
	}
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}
	if s.db == nil || s.s3Client == nil {
		return &pb.PrepareArtifactResponse{Error: "origin storage not configured"}, nil
	}

	log := s.logger.WithFields(logging.Fields{
		"artifact_hash":      hash,
		"tenant_id":          tenantID,
		"requesting_cluster": req.GetRequestingCluster(),
		"artifact_type":      req.GetArtifactType(),
	})

	var internalName, streamInternalName, artifactType, format, storageLocation, syncStatus string
	var sizeBytes sql.NullInt64
	var authoritativeCluster sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(internal_name, ''),
		       COALESCE(stream_internal_name, ''),
		       artifact_type,
		       COALESCE(format, ''),
		       COALESCE(storage_location, ''),
		       COALESCE(sync_status, ''),
		       size_bytes,
		       COALESCE(storage_cluster_id, origin_cluster_id)
		FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND tenant_id = $2 AND status != 'deleted'
	`, hash, tenantID).Scan(&internalName, &streamInternalName, &artifactType, &format, &storageLocation, &syncStatus, &sizeBytes, &authoritativeCluster)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &pb.PrepareArtifactResponse{Error: "artifact not found"}, nil
		}
		log.WithError(err).Error("PrepareArtifact DB query failed")
		return nil, status.Error(codes.Internal, "failed to query artifact")
	}

	// Authoritative cluster = where the bytes actually live. NULL preserves
	// the prior origin-as-storage semantic for rows written before delegation
	// existed.
	if authoritativeCluster.Valid && authoritativeCluster.String != "" {
		log = log.WithField("authoritative_cluster", authoritativeCluster.String)
		// Redirect when this Foghorn pool can't sign URLs against the
		// authoritative cluster's bucket. Uses the unified ownership rule
		// (served + backing tuple match) so a pool serving the cluster but
		// configured against a different S3 still redirects rather than
		// signing against the wrong endpoint.
		if !s.canLocallyMintFor(ctx, tenantID, authoritativeCluster.String) {
			log.Info("PrepareArtifact: storage owned elsewhere — redirecting")
			return &pb.PrepareArtifactResponse{
				RedirectClusterId: authoritativeCluster.String,
			}, nil
		}
	}

	location := strings.ToLower(strings.TrimSpace(storageLocation))
	syncSt := strings.ToLower(strings.TrimSpace(syncStatus))

	if syncSt != "synced" {
		switch location {
		case "local", "freezing":
			log.Info("PrepareArtifact: artifact not yet frozen, requesting async freeze")
			go s.triggerAsyncFreeze(hash, artifactType, tenantID)
			return &pb.PrepareArtifactResponse{Ready: false, EstReadySeconds: 30}, nil
		case "defrosting":
			return &pb.PrepareArtifactResponse{Ready: false, EstReadySeconds: 15}, nil
		case "s3":
			log.WithFields(logging.Fields{"storage_location": location, "sync_status": syncSt}).Warn("PrepareArtifact: metadata drift detected")
			return &pb.PrepareArtifactResponse{Error: "artifact metadata inconsistent: s3 location without synced status"}, nil
		default:
			return &pb.PrepareArtifactResponse{Error: "artifact in unexpected state: " + location}, nil
		}
	}

	artType := strings.ToLower(strings.TrimSpace(artifactType))
	if req.GetArtifactType() != "" {
		requestedType := strings.ToLower(strings.TrimSpace(req.GetArtifactType()))
		if requestedType != artType {
			return &pb.PrepareArtifactResponse{Error: "artifact type mismatch"}, nil
		}
		artType = requestedType
	}

	switch artType {
	case "clip", "vod":
		s3Key := s.buildArtifactS3Key(artType, tenantID, streamInternalName, hash, format)
		presignedURL, err := s.s3Client.GeneratePresignedGET(s3Key, 15*time.Minute)
		if err != nil {
			log.WithError(err).Error("Failed to generate presigned GET for artifact")
			return &pb.PrepareArtifactResponse{Error: "failed to generate download URL"}, nil
		}
		var size uint64
		if sizeBytes.Valid && sizeBytes.Int64 > 0 {
			size = uint64(sizeBytes.Int64)
		}
		log.WithField("artifact_type", artType).Info("PrepareArtifact: presigned URL generated")
		return &pb.PrepareArtifactResponse{
			Url:                presignedURL,
			SizeBytes:          size,
			Ready:              true,
			Format:             format,
			InternalName:       internalName,
			StreamInternalName: streamInternalName,
		}, nil

	case "dvr":
		dvrPrefix := s.s3Client.BuildDVRS3Key(tenantID, streamInternalName, hash)
		segmentURLs, err := s.s3Client.GeneratePresignedURLsForDVR(dvrPrefix, false, 30*time.Minute)
		if err != nil {
			log.WithError(err).Error("Failed to generate presigned DVR segment URLs")
			return &pb.PrepareArtifactResponse{Error: "failed to generate DVR segment URLs"}, nil
		}
		if len(segmentURLs) == 0 {
			return &pb.PrepareArtifactResponse{Error: "no DVR segments found in S3"}, nil
		}
		var size uint64
		if sizeBytes.Valid && sizeBytes.Int64 > 0 {
			size = uint64(sizeBytes.Int64)
		}
		log.WithField("segment_count", len(segmentURLs)).Info("PrepareArtifact: DVR segment URLs generated")
		return &pb.PrepareArtifactResponse{
			SegmentUrls:        segmentURLs,
			SizeBytes:          size,
			Ready:              true,
			Format:             format,
			InternalName:       internalName,
			StreamInternalName: streamInternalName,
		}, nil

	default:
		return &pb.PrepareArtifactResponse{Error: "unknown artifact type: " + artType}, nil
	}
}

func (s *FederationServer) buildArtifactS3Key(artType, tenantID, internalName, hash, format string) string {
	switch artType {
	case "clip":
		return s.s3Client.BuildClipS3Key(tenantID, internalName, hash, format)
	case "dvr":
		return s.s3Client.BuildDVRS3Key(tenantID, internalName, hash)
	case "vod":
		return s.s3Client.BuildVodS3Key(tenantID, hash, hash+"."+format)
	default:
		return "artifacts/" + tenantID + "/" + hash
	}
}

// MintStorageURLs issues presigned PUT URLs for an upload that the
// requesting Foghorn cannot mint locally. The callee MUST own the named
// target storage cluster (served + S3 backing tuple match) and the artifact
// MUST belong to the requesting tenant. VOD multipart create/complete/abort
// is not exposed via this RPC; callers requesting that flow are rejected.
func (s *FederationServer) MintStorageURLs(ctx context.Context, req *pb.MintStorageURLsRequest) (*pb.MintStorageURLsResponse, error) {
	if err := requireFederationServiceAuth(ctx); err != nil {
		return nil, err
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}
	if req.GetTargetClusterId() == "" {
		return nil, status.Error(codes.InvalidArgument, "target_cluster_id required")
	}
	if req.GetArtifactType() == "" {
		return nil, status.Error(codes.InvalidArgument, "artifact_type required")
	}
	if req.GetArtifactKey() == "" {
		return nil, status.Error(codes.InvalidArgument, "artifact_key required")
	}
	if req.GetOp() == pb.MintStorageURLsRequest_OPERATION_UNSPECIFIED {
		return nil, status.Error(codes.InvalidArgument, "op required")
	}

	log := s.logger.WithFields(logging.Fields{
		"tenant_id":          req.GetTenantId(),
		"requesting_cluster": req.GetRequestingCluster(),
		"target_cluster":     req.GetTargetClusterId(),
		"artifact_type":      req.GetArtifactType(),
		"op":                 req.GetOp().String(),
	})

	// Storage ownership — the authoritative claim.
	if !s.canLocallyMintFor(ctx, req.GetTenantId(), req.GetTargetClusterId()) {
		log.Warn("MintStorageURLs: this Foghorn does not own the target storage cluster for this tenant")
		s.recordStorageMint("storage_not_owned_here")
		return &pb.MintStorageURLsResponse{Accepted: false, Reason: "storage_not_owned_here"}, nil
	}
	if s.s3Client == nil {
		log.Warn("MintStorageURLs: ownership claim accepted but s3 client is nil (configuration race)")
		s.recordStorageMint("s3_error")
		return &pb.MintStorageURLsResponse{Accepted: false, Reason: "s3_error"}, nil
	}

	// Operation / artifact-type compatibility.
	artType := strings.ToLower(strings.TrimSpace(req.GetArtifactType()))
	op := req.GetOp()
	switch artType {
	case "thumbnail", "clip", "dvr_segment", "dvr_manifest", "vod":
		// vod here is the single-PUT freeze of an existing VOD asset.
		// Federated VOD multipart create flows through CreateVodUpload,
		// which rejects with storage_delegation_unsupported_for_vod.
		if op != pb.MintStorageURLsRequest_OPERATION_PUT_SINGLE {
			s.recordStorageMint("unsupported_operation")
			return &pb.MintStorageURLsResponse{Accepted: false, Reason: "unsupported_operation"}, nil
		}
	case "dvr":
		if op != pb.MintStorageURLsRequest_OPERATION_PUT_DVR_SET {
			s.recordStorageMint("unsupported_operation")
			return &pb.MintStorageURLsResponse{Accepted: false, Reason: "unsupported_operation"}, nil
		}
		if len(req.GetSegmentFilenames()) == 0 {
			s.recordStorageMint("unsupported_operation")
			return &pb.MintStorageURLsResponse{Accepted: false, Reason: "unsupported_operation"}, nil
		}
	default:
		s.recordStorageMint("unsupported_artifact_type")
		return &pb.MintStorageURLsResponse{Accepted: false, Reason: "unsupported_artifact_type"}, nil
	}

	// Tenant ownership + asset context: try local foghorn.artifacts first,
	// fall back to Commodore (Resolve*Hash for artifacts, ResolveInternalName
	// for live thumbs). Mirrors the resolution chain
	// processFreezePermissionRequest already uses on the producer side.
	mctx, ok := s.resolveMintArtifactContext(ctx, req, artType)
	if !ok {
		s.recordStorageMint("tenant_mismatch")
		return &pb.MintStorageURLsResponse{Accepted: false, Reason: "tenant_mismatch"}, nil
	}

	// Build the S3 key per artifact type, mirroring the local-mint code
	// paths so a delegated upload lands at the same key as a local one
	// would for the same input.
	expiry := 30 * time.Minute
	if artType == "thumbnail" {
		expiry = 15 * time.Minute
	}
	switch artType {
	case "thumbnail":
		// artifact_key is "<streamID-or-artifactHash>/<filename>". Use the
		// caller-provided key directly — both live (streamID) and vod/clip
		// (artifact_hash) shapes match the existing S3 layout
		// `thumbnails/<key>/<file>` produced by processThumbnailUploadRequest.
		s3Key := "thumbnails/" + req.GetArtifactKey()
		url, err := s.s3Client.GeneratePresignedPUT(s3Key, expiry)
		if err != nil {
			log.WithError(err).Error("GeneratePresignedPUT failed")
			s.recordStorageMint("s3_error")
			return &pb.MintStorageURLsResponse{Accepted: false, Reason: "s3_error"}, nil
		}
		s.recordStorageMint("accepted")
		return &pb.MintStorageURLsResponse{
			Accepted:         true,
			S3Key:            s3Key,
			PresignedPutUrl:  url,
			UrlExpirySeconds: uint32(expiry.Seconds()),
		}, nil

	case "clip":
		// Mirror processFreezePermissionRequest's clip-key construction.
		// The caller passes the artifact_key as the clip hash; format is
		// derived from content_type (default mp4 when empty).
		format := "mp4"
		if ct := strings.TrimSpace(req.GetContentType()); ct != "" {
			if before, after, ok := strings.Cut(ct, "/"); ok && before == "video" && after != "" {
				format = after
			}
		}
		s3Key := s.s3Client.BuildClipS3Key(req.GetTenantId(), mctx.streamName, req.GetArtifactKey(), format)
		url, err := s.s3Client.GeneratePresignedPUT(s3Key, expiry)
		if err != nil {
			log.WithError(err).Error("clip GeneratePresignedPUT failed")
			s.recordStorageMint("s3_error")
			return &pb.MintStorageURLsResponse{Accepted: false, Reason: "s3_error"}, nil
		}
		s.recordStorageMint("accepted")
		return &pb.MintStorageURLsResponse{
			Accepted:         true,
			S3Key:            s3Key,
			PresignedPutUrl:  url,
			UrlExpirySeconds: uint32(expiry.Seconds()),
		}, nil

	case "dvr":
		dvrPrefix := s.s3Client.BuildDVRS3Key(req.GetTenantId(), mctx.streamName, req.GetArtifactKey())
		segmentURLs := map[string]string{}
		for _, fn := range req.GetSegmentFilenames() {
			fn = strings.TrimSpace(fn)
			if fn == "" {
				continue
			}
			s3Key := dvrPrefix + "/" + fn
			url, err := s.s3Client.GeneratePresignedPUT(s3Key, expiry)
			if err != nil {
				log.WithError(err).WithField("segment", fn).Error("dvr segment GeneratePresignedPUT failed")
				s.recordStorageMint("s3_error")
				return &pb.MintStorageURLsResponse{Accepted: false, Reason: "s3_error"}, nil
			}
			segmentURLs[fn] = url
		}
		s.recordStorageMint("accepted")
		return &pb.MintStorageURLsResponse{
			Accepted:         true,
			S3Key:            dvrPrefix,
			SegmentUrls:      segmentURLs,
			UrlExpirySeconds: uint32(expiry.Seconds()),
		}, nil

	case "vod":
		// Single-PUT freeze of an already-existing VOD asset (NOT
		// multipart create — that flow lives in CreateVodUpload and is
		// intentionally rejected via storage_delegation_unsupported_for_vod).
		// Mirror the local-mint path's key shape exactly so a delegated
		// freeze lands at the same key as a local one would: filename is
		// `<hash>.<format>`, format derived from content_type.
		format := "mp4"
		if ct := strings.TrimSpace(req.GetContentType()); ct != "" {
			if before, after, ok := strings.Cut(ct, "/"); ok && before == "video" && after != "" {
				format = after
			}
		}
		s3Key := s.s3Client.BuildVodS3Key(req.GetTenantId(), req.GetArtifactKey(), req.GetArtifactKey()+"."+format)
		url, err := s.s3Client.GeneratePresignedPUT(s3Key, expiry)
		if err != nil {
			log.WithError(err).Error("vod GeneratePresignedPUT failed")
			s.recordStorageMint("s3_error")
			return &pb.MintStorageURLsResponse{Accepted: false, Reason: "s3_error"}, nil
		}
		s.recordStorageMint("accepted")
		return &pb.MintStorageURLsResponse{
			Accepted:         true,
			S3Key:            s3Key,
			PresignedPutUrl:  url,
			UrlExpirySeconds: uint32(expiry.Seconds()),
		}, nil

	case "dvr_segment", "dvr_manifest":
		// artifact_key is "<parent_dvr_hash>/<filename>". Build the same key
		// shape the local-mint path would have used: BuildDVRS3Key(tenant,
		// streamName, parentHash) + filename.
		parentHash, fileName, ok := strings.Cut(req.GetArtifactKey(), "/")
		if !ok || parentHash == "" || fileName == "" {
			s.recordStorageMint("unsupported_operation")
			return &pb.MintStorageURLsResponse{Accepted: false, Reason: "unsupported_operation"}, nil
		}
		dvrPrefix := s.s3Client.BuildDVRS3Key(req.GetTenantId(), mctx.streamName, parentHash)
		s3Key := dvrPrefix + "/" + fileName
		url, err := s.s3Client.GeneratePresignedPUT(s3Key, expiry)
		if err != nil {
			log.WithError(err).Error("dvr incremental GeneratePresignedPUT failed")
			s.recordStorageMint("s3_error")
			return &pb.MintStorageURLsResponse{Accepted: false, Reason: "s3_error"}, nil
		}
		s.recordStorageMint("accepted")
		return &pb.MintStorageURLsResponse{
			Accepted:         true,
			S3Key:            s3Key,
			PresignedPutUrl:  url,
			UrlExpirySeconds: uint32(expiry.Seconds()),
		}, nil
	}

	// Unreachable — switch above is exhaustive given the artifact-type guard.
	return &pb.MintStorageURLsResponse{Accepted: false, Reason: "unsupported_artifact_type"}, nil
}

// mintArtifactContext is what resolveMintArtifactContext returns to the
// MintStorageURLs handler — the bundle of fields the per-type S3 key
// builders need. ok=false means the asset could not be authoritatively
// bound to the requested tenant; the handler then rejects with
// tenant_mismatch.
type mintArtifactContext struct {
	tenantID        string
	streamName      string // stream_internal_name (clip/dvr); not populated for vod or live thumb
	internalName    string // artifact routing name (clip/dvr/vod); empty for live thumb
	originClusterID string // cluster that originally created this artifact (for cache-heal row)
	streamID        string // public stream ID; populated for live thumbnails (used to validate artifact_key prefix)
}

// mintArtifactTypeCompatible answers whether a stored row's artifact_type can
// satisfy a requested mint artType. Each mint type builds a different S3 key
// shape, so a same-tenant DVR hash must NOT be acceptable as a clip mint —
// the resulting key would land somewhere the downstream consumers won't find
// it. Thumbnail (artifact-backed branch — live thumbnails are handled
// upstream) accepts any of clip/dvr/vod since it just consults the parent
// asset for routing context.
func mintArtifactTypeCompatible(rowType, requestedType string) bool {
	switch requestedType {
	case "clip":
		return rowType == "clip"
	case "vod":
		return rowType == "vod"
	case "dvr", "dvr_segment", "dvr_manifest":
		return rowType == "dvr"
	case "thumbnail":
		return rowType == "clip" || rowType == "dvr" || rowType == "vod"
	}
	return false
}

// resolveMintArtifactContext is the unified resolver for MintStorageURLs.
// It mirrors the resolution chain processFreezePermissionRequest already
// uses on the producer side: fast-path the local foghorn.artifacts row
// when present, fall back to Commodore.Resolve*Hash (or
// ResolveInternalName for live thumbs) when missing. When Commodore fills
// a gap on a non-thumbnail asset, an opportunistic cache-heal row is
// inserted so subsequent delegated mints fast-path locally; healing
// failures are logged but never block the response. Live thumbnails do
// NOT heal (no DB row exists by design).
func (s *FederationServer) resolveMintArtifactContext(ctx context.Context, req *pb.MintStorageURLsRequest, artType string) (mintArtifactContext, bool) {
	if artType == "thumbnail" && req.GetStreamInternalName() != "" {
		return s.resolveLiveThumbnailContext(ctx, req)
	}

	// Artifact-backed path. artifact_key may be "<hash>" or
	// "<hash>/<filename>" for dvr_segment/dvr_manifest/thumbnail-vod;
	// strip any /file suffix to get the lookup hash.
	lookupHash := req.GetArtifactKey()
	if before, _, ok := strings.Cut(lookupHash, "/"); ok && before != "" {
		lookupHash = before
	}
	if lookupHash == "" {
		return mintArtifactContext{}, false
	}

	// Fast path: local foghorn.artifacts filtered by tenant. Filtering by
	// tenant in the WHERE prevents leaking cross-tenant existence through
	// row-found vs row-missing branches. The row's artifact_type must also
	// be compatible with the requested mint type — otherwise a same-tenant
	// DVR/VOD hash could be requested as clip and pass through to a
	// wrong-shape S3 key build.
	if s.db != nil {
		var rowArtifactType string
		var streamName sql.NullString
		var internalName sql.NullString
		var originCluster sql.NullString
		err := s.db.QueryRowContext(ctx, `
			SELECT artifact_type,
			       COALESCE(stream_internal_name, ''),
			       COALESCE(internal_name, ''),
			       COALESCE(origin_cluster_id, '')
			FROM foghorn.artifacts
			WHERE artifact_hash = $1 AND tenant_id = $2
			LIMIT 1
		`, lookupHash, req.GetTenantId()).Scan(&rowArtifactType, &streamName, &internalName, &originCluster)
		if err == nil {
			if !mintArtifactTypeCompatible(rowArtifactType, artType) {
				return mintArtifactContext{}, false
			}
			// Clip / DVR S3 keys embed stream_internal_name as a path
			// segment (BuildClipS3Key, BuildDVRS3Key). An empty value would
			// produce malformed keys like "clips/<tenant>//.../...". When
			// the row exists but lacks stream_internal_name, fall through
			// to Commodore — its Resolve*Hash always carries
			// stream_internal_name from the JOIN against streams.
			needsStreamName := artType == "clip" || artType == "dvr" || artType == "dvr_segment" || artType == "dvr_manifest"
			rowUsable := !needsStreamName || (streamName.Valid && streamName.String != "")
			if rowUsable {
				ctx := mintArtifactContext{tenantID: req.GetTenantId()}
				if streamName.Valid {
					ctx.streamName = streamName.String
				}
				if internalName.Valid {
					ctx.internalName = internalName.String
				}
				if originCluster.Valid {
					ctx.originClusterID = originCluster.String
				}
				return ctx, true
			}
		}
		// Any DB error other than no-rows → keep going to Commodore. The
		// fallback either confirms or rejects authoritatively.
	}

	// Commodore fallback: same Resolve*Hash chain processFreezePermissionRequest
	// uses. Returns tenant + stream + internal-name + origin-cluster from
	// the system of record.
	if s.mintArtifactResolver == nil {
		return mintArtifactContext{}, false
	}
	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	switch artType {
	case "clip":
		resp, err := s.mintArtifactResolver.ResolveClipHash(rctx, lookupHash)
		if err != nil || resp == nil || !resp.GetFound() {
			return mintArtifactContext{}, false
		}
		if resp.GetTenantId() != req.GetTenantId() {
			return mintArtifactContext{}, false
		}
		ctxOut := mintArtifactContext{
			tenantID:        resp.GetTenantId(),
			streamName:      resp.GetStreamInternalName(),
			internalName:    resp.GetInternalName(),
			originClusterID: resp.GetOriginClusterId(),
		}
		s.healMintArtifactRow(ctx, lookupHash, "clip", ctxOut)
		return ctxOut, true

	case "dvr", "dvr_segment", "dvr_manifest":
		resp, err := s.mintArtifactResolver.ResolveDVRHash(rctx, lookupHash)
		if err != nil || resp == nil || !resp.GetFound() {
			return mintArtifactContext{}, false
		}
		if resp.GetTenantId() != req.GetTenantId() {
			return mintArtifactContext{}, false
		}
		ctxOut := mintArtifactContext{
			tenantID:        resp.GetTenantId(),
			streamName:      resp.GetStreamInternalName(),
			internalName:    resp.GetInternalName(),
			originClusterID: resp.GetOriginClusterId(),
		}
		s.healMintArtifactRow(ctx, lookupHash, "dvr", ctxOut)
		return ctxOut, true

	case "vod":
		resp, err := s.mintArtifactResolver.ResolveVodHash(rctx, lookupHash)
		if err != nil || resp == nil || !resp.GetFound() {
			return mintArtifactContext{}, false
		}
		if resp.GetTenantId() != req.GetTenantId() {
			return mintArtifactContext{}, false
		}
		ctxOut := mintArtifactContext{
			tenantID:        resp.GetTenantId(),
			internalName:    resp.GetInternalName(),
			originClusterID: resp.GetOriginClusterId(),
		}
		s.healMintArtifactRow(ctx, lookupHash, "vod", ctxOut)
		return ctxOut, true

	case "thumbnail":
		// Vod-thumbnail path: artifact_key is "<artifact_hash>/<file>".
		// The local DB miss above means we don't know the asset type
		// from the row — try clip then dvr then vod, accept the first
		// hit. This is rare (cache miss for an existing artifact's
		// thumbnail) so the extra RPCs are acceptable.
		if resp, err := s.mintArtifactResolver.ResolveClipHash(rctx, lookupHash); err == nil && resp != nil && resp.GetFound() && resp.GetTenantId() == req.GetTenantId() {
			ctxOut := mintArtifactContext{
				tenantID:        resp.GetTenantId(),
				streamName:      resp.GetStreamInternalName(),
				internalName:    resp.GetInternalName(),
				originClusterID: resp.GetOriginClusterId(),
			}
			s.healMintArtifactRow(ctx, lookupHash, "clip", ctxOut)
			return ctxOut, true
		}
		if resp, err := s.mintArtifactResolver.ResolveDVRHash(rctx, lookupHash); err == nil && resp != nil && resp.GetFound() && resp.GetTenantId() == req.GetTenantId() {
			ctxOut := mintArtifactContext{
				tenantID:        resp.GetTenantId(),
				streamName:      resp.GetStreamInternalName(),
				internalName:    resp.GetInternalName(),
				originClusterID: resp.GetOriginClusterId(),
			}
			s.healMintArtifactRow(ctx, lookupHash, "dvr", ctxOut)
			return ctxOut, true
		}
		if resp, err := s.mintArtifactResolver.ResolveVodHash(rctx, lookupHash); err == nil && resp != nil && resp.GetFound() && resp.GetTenantId() == req.GetTenantId() {
			ctxOut := mintArtifactContext{
				tenantID:        resp.GetTenantId(),
				internalName:    resp.GetInternalName(),
				originClusterID: resp.GetOriginClusterId(),
			}
			s.healMintArtifactRow(ctx, lookupHash, "vod", ctxOut)
			return ctxOut, true
		}
		return mintArtifactContext{}, false
	}

	return mintArtifactContext{}, false
}

// resolveLiveThumbnailContext handles the live-thumbnail branch: stream
// state is the fast path, Commodore.ResolveInternalName the fallback.
// Validates both tenant AND that the request's artifact_key prefix matches
// the resolved stream_id — without that prefix check, a caller could
// request another stream's thumbnail key by setting artifact_key to that
// stream's UUID while still passing their own tenant's
// stream_internal_name in the request.
func (s *FederationServer) resolveLiveThumbnailContext(ctx context.Context, req *pb.MintStorageURLsRequest) (mintArtifactContext, bool) {
	wantStreamID, _, ok := strings.Cut(req.GetArtifactKey(), "/")
	if !ok || wantStreamID == "" {
		s.logger.WithField("artifact_key", req.GetArtifactKey()).Warn("live thumbnail artifact_key missing <streamID>/<file> shape")
		return mintArtifactContext{}, false
	}

	// Fast path: local stream state.
	if sm := state.DefaultManager(); sm != nil {
		if ss := sm.GetStreamState(req.GetStreamInternalName()); ss != nil {
			if ss.TenantID != req.GetTenantId() {
				return mintArtifactContext{}, false
			}
			if ss.StreamID != "" && ss.StreamID != wantStreamID {
				s.logger.WithFields(logging.Fields{
					"want_stream_id":   wantStreamID,
					"actual_stream_id": ss.StreamID,
				}).Warn("live thumbnail artifact_key streamID does not match stream state")
				return mintArtifactContext{}, false
			}
			if ss.StreamID != "" {
				return mintArtifactContext{tenantID: ss.TenantID, streamID: ss.StreamID}, true
			}
			// Stream state has tenant but no stream_id yet — fall through
			// to Commodore for the authoritative stream_id.
		}
	}

	// Commodore fallback: the storage Foghorn pool usually does NOT have
	// the live ingest stream in its in-memory state. Commodore is the
	// authoritative tenant + stream_id source.
	if s.mintArtifactResolver == nil {
		return mintArtifactContext{}, false
	}
	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	resp, err := s.mintArtifactResolver.ResolveInternalName(rctx, req.GetStreamInternalName())
	if err != nil || resp == nil {
		return mintArtifactContext{}, false
	}
	if resp.GetTenantId() != req.GetTenantId() || resp.GetStreamId() == "" {
		return mintArtifactContext{}, false
	}
	if resp.GetStreamId() != wantStreamID {
		s.logger.WithFields(logging.Fields{
			"want_stream_id":   wantStreamID,
			"actual_stream_id": resp.GetStreamId(),
		}).Warn("live thumbnail artifact_key streamID does not match Commodore stream record")
		return mintArtifactContext{}, false
	}
	return mintArtifactContext{tenantID: resp.GetTenantId(), streamID: resp.GetStreamId()}, true
}

// healMintArtifactRow inserts a minimal lifecycle row for an artifact the
// callee just learned about from Commodore. Mirrors the cache-heal
// processFreezePermissionRequest does on the producer side. Best-effort
// only — failures are logged but never propagated; the mint succeeds
// regardless.
func (s *FederationServer) healMintArtifactRow(ctx context.Context, artifactHash, artifactType string, mctx mintArtifactContext) {
	if s.db == nil {
		return
	}
	internalName := sql.NullString{String: mctx.internalName, Valid: mctx.internalName != ""}
	streamName := sql.NullString{String: mctx.streamName, Valid: mctx.streamName != ""}
	originCluster := sql.NullString{String: mctx.originClusterID, Valid: mctx.originClusterID != ""}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO foghorn.artifacts
			(artifact_hash, artifact_type, tenant_id,
			 stream_internal_name, internal_name, origin_cluster_id,
			 storage_location, sync_status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'pending', 'pending', NOW(), NOW())
		ON CONFLICT (artifact_hash) DO NOTHING
	`, artifactHash, artifactType, mctx.tenantID, streamName, internalName, originCluster); err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"artifact_hash": artifactHash,
			"artifact_type": artifactType,
		}).Warn("MintStorageURLs cache-heal insert failed")
	}
}

func (s *FederationServer) triggerAsyncFreeze(hash, artifactType, _tenantID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	nodeID, err := control.PickStorageNodeIDPublic()
	if err != nil {
		s.logger.WithError(err).WithField("artifact_hash", hash).Warn("No storage node for async freeze")
		return
	}
	if _, err := control.StartDefrost(ctx, artifactType, hash, nodeID, 30*time.Second, s.logger); err != nil {
		s.logger.WithError(err).WithField("artifact_hash", hash).Debug("Async freeze/defrost trigger failed (may already be in progress)")
	}
}

// CreateRemoteClip handles a peer cluster requesting clip creation on the origin.
// The origin has the live stream locally and can create the clip directly.
func (s *FederationServer) CreateRemoteClip(ctx context.Context, req *pb.RemoteClipRequest) (*pb.RemoteClipResponse, error) {
	if err := requireFederationServiceAuth(ctx); err != nil {
		return nil, err
	}
	if req.GetStreamInternalName() == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_internal_name required")
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}
	if s.clipCreator == nil {
		return &pb.RemoteClipResponse{Accepted: false, Reason: "clip creation not available"}, nil
	}

	log := s.logger.WithFields(logging.Fields{
		"internal_name":      req.GetStreamInternalName(),
		"requesting_cluster": req.GetRequestingCluster(),
		"clip_hash":          req.GetClipHash(),
	})

	// Verify stream exists locally
	sm := state.DefaultManager()
	ss := sm.GetStreamState(req.GetStreamInternalName())
	if ss == nil || ss.Status != "live" {
		return &pb.RemoteClipResponse{Accepted: false, Reason: "stream not live on origin"}, nil
	}
	if ss.TenantID != "" && ss.TenantID != req.GetTenantId() {
		return &pb.RemoteClipResponse{Accepted: false, Reason: "stream tenant mismatch"}, nil
	}

	// Delegate to local clip creation — convert plain fields to optional pointers
	clipReq := &pb.CreateClipRequest{
		StreamInternalName: req.GetStreamInternalName(),
		TenantId:           req.GetTenantId(),
		Format:             req.GetFormat(),
	}
	if uid := req.GetUserId(); uid != "" {
		clipReq.UserId = &uid
	}
	if pid := req.GetPlaybackId(); pid != "" {
		clipReq.PlaybackId = &pid
	}
	if v := req.GetStartUnix(); v != 0 {
		clipReq.StartUnix = &v
	}
	if v := req.GetStopUnix(); v != 0 {
		clipReq.StopUnix = &v
	}
	if v := req.GetStartMs(); v != 0 {
		clipReq.StartMs = &v
	}
	if v := req.GetStopMs(); v != 0 {
		clipReq.StopMs = &v
	}
	if v := req.GetDurationSec(); v != 0 {
		clipReq.DurationSec = &v
	}
	clipResp, err := s.clipCreator.CreateClip(ctx, clipReq)
	if err != nil {
		log.WithError(err).Warn("Remote clip creation failed")
		return &pb.RemoteClipResponse{Accepted: false, Reason: err.Error()}, nil
	}

	log.WithField("clip_hash", clipResp.GetClipHash()).Info("Remote clip created on origin")
	return &pb.RemoteClipResponse{
		Accepted:      true,
		ClipHash:      clipResp.GetClipHash(),
		StorageNodeId: clipResp.GetNodeId(),
	}, nil
}

// CreateRemoteDVR handles a peer cluster requesting DVR recording on the origin.
// DVR must record on the origin cluster (co-located with ingest source).
func (s *FederationServer) CreateRemoteDVR(ctx context.Context, req *pb.RemoteDVRRequest) (*pb.RemoteDVRResponse, error) {
	if err := requireFederationServiceAuth(ctx); err != nil {
		return nil, err
	}
	if req.GetStreamInternalName() == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_internal_name required")
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}
	if s.dvrCreator == nil {
		return &pb.RemoteDVRResponse{Accepted: false, Reason: "DVR not available"}, nil
	}

	log := s.logger.WithFields(logging.Fields{
		"internal_name":      req.GetStreamInternalName(),
		"requesting_cluster": req.GetRequestingCluster(),
		"dvr_hash":           req.GetDvrHash(),
	})

	// Verify stream exists locally
	sm := state.DefaultManager()
	ss := sm.GetStreamState(req.GetStreamInternalName())
	if ss == nil || ss.Status != "live" {
		return &pb.RemoteDVRResponse{Accepted: false, Reason: "stream not live on origin"}, nil
	}
	if ss.TenantID != "" && ss.TenantID != req.GetTenantId() {
		return &pb.RemoteDVRResponse{Accepted: false, Reason: "stream tenant mismatch"}, nil
	}

	// Delegate to local DVR start
	dvrReq := &pb.StartDVRRequest{
		InternalName: req.GetStreamInternalName(),
		TenantId:     req.GetTenantId(),
	}
	if uid := req.GetUserId(); uid != "" {
		dvrReq.UserId = &uid
	}
	dvrResp, err := s.dvrCreator.StartDVR(ctx, dvrReq)
	if err != nil {
		log.WithError(err).Warn("Remote DVR creation failed")
		return &pb.RemoteDVRResponse{Accepted: false, Reason: err.Error()}, nil
	}

	log.WithField("dvr_hash", dvrResp.GetDvrHash()).Info("Remote DVR started on origin")
	return &pb.RemoteDVRResponse{
		Accepted: true,
		DvrHash:  dvrResp.GetDvrHash(),
	}, nil
}

// PeerChannel is a bidirectional stream for real-time telemetry exchange.
// The receiving side writes EdgeTelemetry and ReplicationEvents to Redis;
// the sending side pushes telemetry for locally active replicated streams.
func (s *FederationServer) PeerChannel(stream pb.FoghornFederation_PeerChannelServer) error {
	ctx := stream.Context()
	if err := requireFederationServiceAuth(ctx); err != nil {
		return err
	}

	var peerClusterID string
	var once sync.Once

	log := s.logger.WithField("rpc", "PeerChannel")

	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			log.WithField("peer_cluster", peerClusterID).Info("PeerChannel closed by peer")
			return nil
		}
		if err != nil {
			if status.Code(err) == codes.Canceled {
				return nil
			}
			return err
		}

		once.Do(func() {
			peerClusterID = msg.ClusterId
			log = log.WithField("peer_cluster", peerClusterID)
			log.Info("PeerChannel established")
		})
		if peerClusterID == "" {
			return status.Error(codes.InvalidArgument, "cluster_id required in first peer message")
		}
		if peerClusterID == s.clusterID {
			return status.Error(codes.InvalidArgument, "cannot open PeerChannel to own cluster")
		}
		if msg.ClusterId != "" && msg.ClusterId != peerClusterID {
			return status.Error(codes.PermissionDenied, "cluster_id mismatch on peer channel")
		}

		switch payload := msg.Payload.(type) {
		case *pb.PeerMessage_EdgeTelemetry:
			s.handleEdgeTelemetry(ctx, peerClusterID, payload.EdgeTelemetry)

		case *pb.PeerMessage_ReplicationEvent:
			s.handleReplicationEvent(ctx, peerClusterID, payload.ReplicationEvent)

		case *pb.PeerMessage_ClusterSummary:
			s.handleClusterSummary(ctx, peerClusterID, payload.ClusterSummary)

		case *pb.PeerMessage_StreamLifecycle:
			s.handleStreamLifecycle(ctx, peerClusterID, payload.StreamLifecycle)

		case *pb.PeerMessage_StreamAd:
			s.handleStreamAdvertisement(ctx, peerClusterID, payload.StreamAd)

		case *pb.PeerMessage_ArtifactAd:
			s.handleArtifactAdvertisement(ctx, peerClusterID, payload.ArtifactAd)

		case *pb.PeerMessage_PeerHeartbeat:
			s.handlePeerHeartbeat(ctx, peerClusterID, payload.PeerHeartbeat)

		case *pb.PeerMessage_CapacitySummary:
			// CapacitySummary received — no handler yet (dCDN future)

		default:
			log.Warn("Unknown PeerMessage payload type, ignoring")
		}
	}
}

func (s *FederationServer) handleEdgeTelemetry(ctx context.Context, peerClusterID string, t *pb.EdgeTelemetry) {
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
	if err := s.cache.SetRemoteEdge(ctx, peerClusterID, entry); err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"peer_cluster": peerClusterID,
			"node_id":      t.NodeId,
		}).Warn("Failed to cache remote edge telemetry")
	}
}

func (s *FederationServer) handleReplicationEvent(ctx context.Context, peerClusterID string, r *pb.ReplicationEvent) {
	entry := &RemoteReplicationEntry{
		StreamName: r.StreamName,
		NodeID:     r.NodeId,
		ClusterID:  peerClusterID,
		BaseURL:    r.BaseUrl,
		DTSCURL:    r.DtscUrl,
		Available:  r.Available,
		UpdatedAt:  time.Now().Unix(),
	}
	if err := s.cache.SetRemoteReplication(ctx, peerClusterID, entry); err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"peer_cluster": peerClusterID,
			"stream":       r.StreamName,
			"available":    r.Available,
		}).Warn("Failed to cache remote replication event")
	}
}

func (s *FederationServer) handleStreamLifecycle(ctx context.Context, peerClusterID string, ev *pb.StreamLifecycleEvent) {
	if ev.GetIsLive() {
		clusterID := peerClusterID
		if clusterID == "" {
			clusterID = ev.GetClusterId()
		}
		if err := s.cache.SetRemoteLiveStream(ctx, ev.GetTenantId(), ev.GetInternalName(), &RemoteLiveStreamEntry{
			ClusterID: clusterID,
			TenantID:  ev.GetTenantId(),
			UpdatedAt: time.Now().Unix(),
		}); err != nil {
			s.logger.WithError(err).WithFields(logging.Fields{
				"peer_cluster":  peerClusterID,
				"internal_name": ev.GetInternalName(),
			}).Warn("Failed to cache remote live stream")
		}
	} else {
		if err := s.cache.DeleteRemoteLiveStream(ctx, ev.GetTenantId(), ev.GetInternalName()); err != nil {
			s.logger.WithError(err).WithFields(logging.Fields{
				"peer_cluster":  peerClusterID,
				"internal_name": ev.GetInternalName(),
			}).Warn("Failed to delete remote live stream")
		}
	}
}

func (s *FederationServer) handleClusterSummary(ctx context.Context, peerClusterID string, summary *pb.ClusterEdgeSummary) {
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
	if err := s.cache.SetEdgeSummary(ctx, peerClusterID, record); err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"peer_cluster": peerClusterID,
			"edge_count":   len(edges),
		}).Warn("Failed to cache cluster edge summary")
	}
}

func (s *FederationServer) handleArtifactAdvertisement(ctx context.Context, peerClusterID string, ad *pb.ArtifactAdvertisement) {
	if ad == nil {
		return
	}
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
		if err := s.cache.SetRemoteArtifact(ctx, peerClusterID, entry); err != nil {
			s.logger.WithError(err).WithFields(logging.Fields{
				"peer_cluster":  peerClusterID,
				"artifact_hash": loc.ArtifactHash,
				"node_id":       loc.NodeId,
			}).Warn("Failed to cache remote artifact location")
		}
	}
}

func (s *FederationServer) handleStreamAdvertisement(ctx context.Context, peerClusterID string, ad *pb.StreamAdvertisement) {
	if ad == nil {
		return
	}
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
		OriginClusterID: peerClusterID,
		IsLive:          ad.IsLive,
		Edges:           edges,
		Timestamp:       ad.Timestamp,
	}
	if err := s.cache.SetStreamAd(ctx, peerClusterID, record); err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"peer_cluster":  peerClusterID,
			"internal_name": ad.InternalName,
		}).Warn("Failed to cache stream advertisement")
	}
	// Maintain playback_id reverse index for peer stream resolution
	if ad.PlaybackId != "" && ad.IsLive {
		if err := s.cache.SetPlaybackIndex(ctx, ad.PlaybackId, ad.InternalName); err != nil {
			s.logger.WithError(err).WithFields(logging.Fields{
				"peer_cluster": peerClusterID,
				"playback_id":  ad.PlaybackId,
			}).Warn("Failed to cache playback index")
		}
	}
}

func (s *FederationServer) handlePeerHeartbeat(ctx context.Context, peerClusterID string, hb *pb.PeerHeartbeat) {
	if hb == nil {
		return
	}
	record := &PeerHeartbeatRecord{
		ProtocolVersion:  hb.ProtocolVersion,
		StreamCount:      hb.StreamCount,
		TotalBWAvailable: hb.TotalBwAvailable,
		EdgeCount:        hb.EdgeCount,
		UptimeSeconds:    hb.UptimeSeconds,
		Capabilities:     hb.Capabilities,
	}
	if err := s.cache.SetPeerHeartbeat(ctx, peerClusterID, record); err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"peer_cluster": peerClusterID,
		}).Warn("Failed to cache peer heartbeat")
	}
}

// ListTenantArtifacts returns all artifact metadata for a tenant on this cluster.
func (s *FederationServer) ListTenantArtifacts(ctx context.Context, req *pb.ListTenantArtifactsRequest) (*pb.ListTenantArtifactsResponse, error) {
	if err := requireFederationServiceAuth(ctx); err != nil {
		return nil, err
	}
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}
	if s.db == nil {
		return nil, status.Error(codes.Internal, "database not available")
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT artifact_hash, artifact_type, COALESCE(internal_name, ''),
		       COALESCE(format, ''), COALESCE(storage_location, ''),
		       COALESCE(sync_status, ''), COALESCE(s3_url, ''),
		       COALESCE(size_bytes, 0),
		       COALESCE(EXTRACT(EPOCH FROM created_at)::bigint, 0),
		       COALESCE(EXTRACT(EPOCH FROM frozen_at)::bigint, 0),
		       COALESCE(stream_internal_name, '')
		FROM foghorn.artifacts
		WHERE tenant_id = $1 AND status != 'deleted'
		ORDER BY created_at DESC
	`, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to query artifacts: %v", err)
	}
	defer rows.Close()

	var artifacts []*pb.ArtifactMetadata
	for rows.Next() {
		var a pb.ArtifactMetadata
		if err := rows.Scan(&a.ArtifactHash, &a.ArtifactType, &a.InternalName,
			&a.Format, &a.StorageLocation, &a.SyncStatus, &a.S3Url,
			&a.SizeBytes, &a.CreatedAt, &a.FrozenAt, &a.StreamInternalName); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to scan artifact: %v", err)
		}
		artifacts = append(artifacts, &a)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "row iteration error: %v", err)
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":          tenantID,
		"requesting_cluster": req.GetRequestingCluster(),
		"artifact_count":     len(artifacts),
	}).Info("ListTenantArtifacts served")

	return &pb.ListTenantArtifactsResponse{Artifacts: artifacts}, nil
}

// MigrateArtifactMetadata fetches artifact records from a source cluster and inserts them locally.
func (s *FederationServer) MigrateArtifactMetadata(ctx context.Context, req *pb.MigrateArtifactMetadataRequest) (*pb.MigrateArtifactMetadataResponse, error) {
	if err := requireFederationServiceAuth(ctx); err != nil {
		return nil, err
	}
	tenantID := req.GetTenantId()
	sourceClusterID := req.GetSourceClusterId()
	if tenantID == "" || sourceClusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and source_cluster_id required")
	}
	if s.db == nil {
		return nil, status.Error(codes.Internal, "database not available")
	}
	if s.fedClient == nil || s.peerManager == nil {
		return nil, status.Error(codes.Internal, "federation client not available")
	}

	log := s.logger.WithFields(logging.Fields{
		"tenant_id":      tenantID,
		"source_cluster": sourceClusterID,
	})

	peerAddr := s.peerManager.GetPeerAddr(sourceClusterID)
	if peerAddr == "" {
		return &pb.MigrateArtifactMetadataResponse{Error: "source cluster address unknown"}, nil
	}

	listResp, err := s.fedClient.ListTenantArtifacts(ctx, sourceClusterID, peerAddr, &pb.ListTenantArtifactsRequest{
		TenantId:          tenantID,
		RequestingCluster: s.clusterID,
	})
	if err != nil {
		return &pb.MigrateArtifactMetadataResponse{Error: "failed to list artifacts from source: " + err.Error()}, nil //nolint:nilerr // error encoded in response message
	}

	var migrated, exists int32
	for _, a := range listResp.Artifacts {
		inserted, err := upsertMigratedArtifactMetadata(ctx, s.db, tenantID, sourceClusterID, a)
		if err != nil {
			log.WithError(err).WithField("artifact_hash", a.ArtifactHash).Warn("Failed to upsert migrated artifact")
			continue
		}
		if inserted {
			migrated++
		} else {
			exists++
		}
	}

	log.WithFields(logging.Fields{
		"migrated":       migrated,
		"already_exists": exists,
		"total_source":   len(listResp.Artifacts),
	}).Info("Artifact metadata migration complete")

	return &pb.MigrateArtifactMetadataResponse{
		MigratedCount: migrated,
		AlreadyExists: exists,
	}, nil
}

func upsertMigratedArtifactMetadata(ctx context.Context, db *sql.DB, tenantID, sourceClusterID string, a *pb.ArtifactMetadata) (bool, error) {
	result, err := db.ExecContext(ctx, `
		INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, internal_name, stream_internal_name, format, status, storage_location, sync_status, s3_url, size_bytes, origin_cluster_id)
		VALUES ($1, $2, $3, $4, $11, $5, 'active', $6, $7, $8, $9, $10)
		ON CONFLICT (artifact_hash) DO NOTHING
	`, a.ArtifactHash, a.ArtifactType, tenantID, a.InternalName, a.Format,
		a.StorageLocation, a.SyncStatus, a.S3Url, a.SizeBytes, sourceClusterID, a.StreamInternalName)
	if err != nil {
		return false, err
	}
	rowsAffected, _ := result.RowsAffected()
	if rowsAffected > 0 {
		return true, nil
	}

	_, err = db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		SET internal_name = CASE
				WHEN COALESCE(internal_name, '') = '' AND $4 <> '' THEN $4
				ELSE internal_name
			END,
			stream_internal_name = CASE
				WHEN COALESCE(stream_internal_name, '') = '' AND $11 <> '' THEN $11
				ELSE stream_internal_name
			END,
			format = CASE
				WHEN COALESCE(format, '') = '' AND $5 <> '' THEN $5
				ELSE format
			END,
			storage_location = CASE
				WHEN COALESCE(storage_location, '') = '' AND $6 <> '' THEN $6
				ELSE storage_location
			END,
			sync_status = CASE
				WHEN COALESCE(sync_status, '') = '' AND $7 <> '' THEN $7
				ELSE sync_status
			END,
			s3_url = CASE
				WHEN COALESCE(s3_url, '') = '' AND $8 <> '' THEN $8
				ELSE s3_url
			END,
			size_bytes = CASE
				WHEN COALESCE(size_bytes, 0) = 0 AND $9 > 0 THEN $9
				ELSE size_bytes
			END,
			origin_cluster_id = CASE
				WHEN COALESCE(origin_cluster_id, '') = '' THEN $10
				ELSE origin_cluster_id
			END
		WHERE artifact_hash = $1 AND artifact_type = $2 AND tenant_id = $3
	`, a.ArtifactHash, a.ArtifactType, tenantID, a.InternalName, a.Format,
		a.StorageLocation, a.SyncStatus, a.S3Url, a.SizeBytes, sourceClusterID, a.StreamInternalName)
	if err != nil {
		return false, err
	}

	return false, nil
}

// ForwardArtifactCommand handles a peer forwarding an artifact command it couldn't
// handle locally. Dispatches to the local handler based on the command field.
func (s *FederationServer) ForwardArtifactCommand(ctx context.Context, req *pb.ForwardArtifactCommandRequest) (*pb.ForwardArtifactCommandResponse, error) {
	if err := requireFederationServiceAuth(ctx); err != nil {
		return nil, err
	}
	if req.GetArtifactHash() == "" {
		return nil, status.Error(codes.InvalidArgument, "artifact_hash required")
	}
	if req.GetCommand() == "" {
		return nil, status.Error(codes.InvalidArgument, "command required")
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}
	if err := s.revalidateForwardedArtifact(ctx, req); err != nil {
		if status.Code(err) == codes.NotFound {
			return &pb.ForwardArtifactCommandResponse{Handled: false}, nil
		}
		if status.Code(err) == codes.FailedPrecondition {
			return &pb.ForwardArtifactCommandResponse{Handled: false, Error: err.Error()}, nil
		}
		return nil, err
	}
	if s.artifactHandler == nil {
		return &pb.ForwardArtifactCommandResponse{Handled: false, Error: "artifact handler not available"}, nil
	}

	log := s.logger.WithFields(logging.Fields{
		"command":       req.GetCommand(),
		"artifact_hash": req.GetArtifactHash(),
		"tenant_id":     req.GetTenantId(),
	})

	// Suppress re-forwarding: this request already came from a peer
	ctx = context.WithValue(ctx, ctxkeys.KeyNoForward, true)

	switch req.GetCommand() {
	case "delete_clip":
		resp, err := s.artifactHandler.DeleteClip(ctx, &pb.DeleteClipRequest{
			ClipHash: req.GetArtifactHash(),
			TenantId: req.GetTenantId(),
		})
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return &pb.ForwardArtifactCommandResponse{Handled: false}, nil
			}
			return &pb.ForwardArtifactCommandResponse{Handled: false, Error: err.Error()}, nil
		}
		log.Info("Forwarded delete_clip handled locally")
		return &pb.ForwardArtifactCommandResponse{Handled: resp.GetSuccess()}, nil

	case "stop_dvr":
		stopReq := &pb.StopDVRRequest{
			DvrHash:  req.GetArtifactHash(),
			TenantId: req.GetTenantId(),
		}
		if req.GetStreamId() != "" {
			streamID := req.GetStreamId()
			stopReq.StreamId = &streamID
		}
		resp, err := s.artifactHandler.StopDVR(ctx, stopReq)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return &pb.ForwardArtifactCommandResponse{Handled: false}, nil
			}
			return &pb.ForwardArtifactCommandResponse{Handled: false, Error: err.Error()}, nil
		}
		log.Info("Forwarded stop_dvr handled locally")
		return &pb.ForwardArtifactCommandResponse{Handled: resp.GetSuccess()}, nil

	case "delete_dvr":
		resp, err := s.artifactHandler.DeleteDVR(ctx, &pb.DeleteDVRRequest{
			DvrHash:  req.GetArtifactHash(),
			TenantId: req.GetTenantId(),
		})
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return &pb.ForwardArtifactCommandResponse{Handled: false}, nil
			}
			return &pb.ForwardArtifactCommandResponse{Handled: false, Error: err.Error()}, nil
		}
		log.Info("Forwarded delete_dvr handled locally")
		return &pb.ForwardArtifactCommandResponse{Handled: resp.GetSuccess()}, nil

	case "delete_vod":
		resp, err := s.artifactHandler.DeleteVodAsset(ctx, &pb.DeleteVodAssetRequest{
			ArtifactHash: req.GetArtifactHash(),
			TenantId:     req.GetTenantId(),
		})
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return &pb.ForwardArtifactCommandResponse{Handled: false}, nil
			}
			return &pb.ForwardArtifactCommandResponse{Handled: false, Error: err.Error()}, nil
		}
		log.Info("Forwarded delete_vod handled locally")
		return &pb.ForwardArtifactCommandResponse{Handled: resp.GetSuccess()}, nil

	default:
		return &pb.ForwardArtifactCommandResponse{
			Handled: false,
			Error:   "unknown command: " + req.GetCommand(),
		}, nil
	}
}

func (s *FederationServer) revalidateForwardedArtifact(ctx context.Context, req *pb.ForwardArtifactCommandRequest) error {
	if s.db == nil {
		s.logger.WithFields(logging.Fields{
			"command":       req.GetCommand(),
			"artifact_hash": req.GetArtifactHash(),
			"tenant_id":     req.GetTenantId(),
		}).Warn("Skipping forwarded artifact revalidation because database is unavailable")
		return nil
	}

	artifactType, err := artifactTypeForForwardCommand(req.GetCommand())
	if err != nil {
		return err
	}

	var dbStreamID sql.NullString
	err = s.db.QueryRowContext(ctx, `
		SELECT stream_id::text
		FROM foghorn.artifacts
		WHERE artifact_hash = $1
		  AND artifact_type = $2
		  AND tenant_id = $3
		  AND status != 'deleted'
		LIMIT 1
	`, req.GetArtifactHash(), artifactType, req.GetTenantId()).Scan(&dbStreamID)
	if errors.Is(err, sql.ErrNoRows) {
		return status.Error(codes.NotFound, "artifact not found")
	}
	if err != nil {
		return status.Error(codes.Internal, "failed to verify artifact ownership")
	}

	if req.GetCommand() == "stop_dvr" && req.GetStreamId() != "" && dbStreamID.Valid && dbStreamID.String != "" && dbStreamID.String != req.GetStreamId() {
		return status.Error(codes.FailedPrecondition, "stream_id mismatch")
	}

	return nil
}

func artifactTypeForForwardCommand(command string) (string, error) {
	switch command {
	case "delete_clip":
		return "clip", nil
	case "stop_dvr", "delete_dvr":
		return "dvr", nil
	case "delete_vod":
		return "vod", nil
	default:
		return "", status.Error(codes.InvalidArgument, "unknown command: "+command)
	}
}

// AvailBandwidthFromNodeState returns available bandwidth for a NodeState.
// Extracted to avoid reaching into unexported fields.
func AvailBandwidthFromNodeState(ns *state.NodeState) uint64 {
	if ns.BWLimit <= 0 {
		return 0
	}
	used := ns.UpSpeed + ns.DownSpeed
	avail := ns.BWLimit - used
	if avail < 0 {
		return 0
	}
	return uint64(avail)
}

func requireFederationServiceAuth(ctx context.Context) error {
	if ctxkeys.GetAuthType(ctx) != "service" {
		return status.Error(codes.PermissionDenied, "federation rpc requires service authentication")
	}
	return nil
}
