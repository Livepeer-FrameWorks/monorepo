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
	"frameworks/api_balancing/internal/storage"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

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

// FederationServer implements the FoghornFederation gRPC service.
// It handles cross-cluster stream queries, origin-pull notifications,
// artifact preparation, and bidirectional telemetry via PeerChannel.
type FederationServer struct {
	pb.UnimplementedFoghornFederationServer
	logger      logging.Logger
	lb          *balancer.LoadBalancer
	clusterID   string
	cache       *RemoteEdgeCache
	db          *sql.DB
	s3Client    *storage.S3Client
	clipCreator ClipCreator
	dvrCreator  DVRCreator
	peerManager PeerAddrResolver
	fedClient   *FederationClient
}

// PeerAddrResolver resolves gRPC addresses for peer clusters.
type PeerAddrResolver interface {
	GetPeerAddr(clusterID string) string
}

// FederationServerConfig holds dependencies for the federation server.
type FederationServerConfig struct {
	Logger      logging.Logger
	LB          *balancer.LoadBalancer
	ClusterID   string
	Cache       *RemoteEdgeCache
	DB          *sql.DB
	S3Client    *storage.S3Client
	ClipCreator ClipCreator
	DVRCreator  DVRCreator
	PeerManager PeerAddrResolver
	FedClient   *FederationClient
}

// NewFederationServer creates a new federation gRPC server.
func NewFederationServer(cfg FederationServerConfig) *FederationServer {
	return &FederationServer{
		logger:      cfg.Logger,
		lb:          cfg.LB,
		clusterID:   cfg.ClusterID,
		cache:       cfg.Cache,
		db:          cfg.DB,
		s3Client:    cfg.S3Client,
		clipCreator: cfg.ClipCreator,
		dvrCreator:  cfg.DVRCreator,
		peerManager: cfg.PeerManager,
		fedClient:   cfg.FedClient,
	}
}

// SetClipCreator wires the clip creation delegate (set after FoghornGRPCServer is created).
func (s *FederationServer) SetClipCreator(cc ClipCreator) { s.clipCreator = cc }

// SetDVRCreator wires the DVR creation delegate (set after FoghornGRPCServer is created).
func (s *FederationServer) SetDVRCreator(dc DVRCreator) { s.dvrCreator = dc }

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
	if ss.TenantID != "" && ss.TenantID != req.TenantId {
		return &pb.OriginPullAck{
			Accepted: false,
			Reason:   "stream not found locally",
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
		// Still return success — the pull can proceed even if caching fails
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

	var internalName, artifactType, format, storageLocation, syncStatus string
	var sizeBytes sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(internal_name, ''),
		       artifact_type,
		       COALESCE(format, ''),
		       COALESCE(storage_location, ''),
		       COALESCE(sync_status, ''),
		       size_bytes
		FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND tenant_id = $2 AND status != 'deleted'
	`, hash, tenantID).Scan(&internalName, &artifactType, &format, &storageLocation, &syncStatus, &sizeBytes)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &pb.PrepareArtifactResponse{Error: "artifact not found"}, nil
		}
		log.WithError(err).Error("PrepareArtifact DB query failed")
		return nil, status.Error(codes.Internal, "failed to query artifact")
	}

	location := strings.ToLower(strings.TrimSpace(storageLocation))
	syncSt := strings.ToLower(strings.TrimSpace(syncStatus))

	if syncSt != "synced" && location != "s3" {
		switch location {
		case "local", "freezing":
			log.Info("PrepareArtifact: artifact not yet frozen, requesting async freeze")
			go s.triggerAsyncFreeze(hash, artifactType, tenantID)
			return &pb.PrepareArtifactResponse{Ready: false, EstReadySeconds: 30}, nil
		case "defrosting":
			return &pb.PrepareArtifactResponse{Ready: false, EstReadySeconds: 15}, nil
		default:
			return &pb.PrepareArtifactResponse{Error: "artifact in unexpected state: " + location}, nil
		}
	}

	artType := strings.ToLower(strings.TrimSpace(artifactType))
	if req.GetArtifactType() != "" {
		artType = strings.ToLower(req.GetArtifactType())
	}

	switch artType {
	case "clip", "vod":
		s3Key := s.buildArtifactS3Key(artType, tenantID, internalName, hash, format)
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
			Url:          presignedURL,
			SizeBytes:    size,
			Ready:        true,
			Format:       format,
			InternalName: internalName,
		}, nil

	case "dvr":
		dvrPrefix := s.s3Client.BuildDVRS3Key(tenantID, internalName, hash)
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
			SegmentUrls:  segmentURLs,
			SizeBytes:    size,
			Ready:        true,
			Format:       format,
			InternalName: internalName,
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
	if req.GetInternalName() == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name required")
	}
	if s.clipCreator == nil {
		return &pb.RemoteClipResponse{Accepted: false, Reason: "clip creation not available"}, nil
	}

	log := s.logger.WithFields(logging.Fields{
		"internal_name":      req.GetInternalName(),
		"requesting_cluster": req.GetRequestingCluster(),
		"clip_hash":          req.GetClipHash(),
	})

	// Verify stream exists locally
	sm := state.DefaultManager()
	ss := sm.GetStreamState(req.GetInternalName())
	if ss == nil || ss.Status != "live" {
		return &pb.RemoteClipResponse{Accepted: false, Reason: "stream not live on origin"}, nil
	}

	// Delegate to local clip creation — convert plain fields to optional pointers
	clipReq := &pb.CreateClipRequest{
		InternalName: req.GetInternalName(),
		TenantId:     req.GetTenantId(),
		Format:       req.GetFormat(),
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
	if req.GetInternalName() == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name required")
	}
	if s.dvrCreator == nil {
		return &pb.RemoteDVRResponse{Accepted: false, Reason: "DVR not available"}, nil
	}

	log := s.logger.WithFields(logging.Fields{
		"internal_name":      req.GetInternalName(),
		"requesting_cluster": req.GetRequestingCluster(),
		"dvr_hash":           req.GetDvrHash(),
	})

	// Verify stream exists locally
	sm := state.DefaultManager()
	ss := sm.GetStreamState(req.GetInternalName())
	if ss == nil || ss.Status != "live" {
		return &pb.RemoteDVRResponse{Accepted: false, Reason: "stream not live on origin"}, nil
	}

	// Delegate to local DVR start
	dvrReq := &pb.StartDVRRequest{
		InternalName: req.GetInternalName(),
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
		if msg.ClusterId != "" && msg.ClusterId != peerClusterID {
			return status.Error(codes.PermissionDenied, "cluster_id mismatch on peer channel")
		}

		if peerClusterID == "" {
			return status.Error(codes.InvalidArgument, "peer cluster_id required")
		}
		if peerClusterID == s.clusterID {
			return status.Error(codes.InvalidArgument, "cannot open PeerChannel to own cluster")
		}
		if msg.GetClusterId() != "" && msg.GetClusterId() != peerClusterID {
			return status.Error(codes.InvalidArgument, "peer cluster_id changed during stream")
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
		if err := s.cache.SetRemoteLiveStream(ctx, ev.GetInternalName(), &RemoteLiveStreamEntry{
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
		if err := s.cache.DeleteRemoteLiveStream(ctx, ev.GetInternalName()); err != nil {
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
		       COALESCE(EXTRACT(EPOCH FROM frozen_at)::bigint, 0)
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
			&a.SizeBytes, &a.CreatedAt, &a.FrozenAt); err != nil {
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
		result, err := s.db.ExecContext(ctx, `
			INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, internal_name, format, status, storage_location, sync_status, s3_url, size_bytes, origin_cluster_id)
			VALUES ($1, $2, $3, $4, $5, 'active', $6, $7, $8, $9, $10)
			ON CONFLICT (artifact_hash, artifact_type, tenant_id) DO NOTHING
		`, a.ArtifactHash, a.ArtifactType, tenantID, a.InternalName, a.Format,
			a.StorageLocation, a.SyncStatus, a.S3Url, a.SizeBytes, sourceClusterID)
		if err != nil {
			log.WithError(err).WithField("artifact_hash", a.ArtifactHash).Warn("Failed to insert migrated artifact")
			continue
		}
		rowsAffected, _ := result.RowsAffected()
		if rowsAffected > 0 {
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
