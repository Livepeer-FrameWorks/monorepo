package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"frameworks/pkg/clips"
	"frameworks/pkg/dvr"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/validation"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Registry holds active Helmsman control streams keyed by node_id
type Registry struct {
	mu    sync.RWMutex
	conns map[string]*conn
	log   logging.Logger
}

type conn struct {
	stream pb.HelmsmanControl_ConnectServer
	last   time.Time
}

var registry *Registry
var nodeService NodeMetricsProcessor
var streamHealthHandler func(string, string, string, bool, map[string]interface{})
var clipHashResolver func(string) (string, string, error)
var db *sql.DB

// Stream health tracking for DVR readiness
var streamHealthMutex sync.RWMutex
var streamHealthMap = make(map[string]*StreamHealthStatus)

// Node outputs tracking for DTSC URI resolution
var nodeOutputsMutex sync.RWMutex
var nodeOutputsMap = make(map[string]*NodeOutputs)

// StreamHealthStatus tracks the health state of streams for DVR readiness checks
type StreamHealthStatus struct {
	InternalName  string
	IsHealthy     bool
	BufferState   string // FULL, EMPTY, DRY, RECOVER
	Status        string // live, offline
	LastUpdate    time.Time
	SourceNodeID  string // Node ID where the stream originates
	SourceBaseURL string // Base URL of source node
}

// NodeOutputs tracks the MistServer output configurations for each node
type NodeOutputs struct {
	NodeID      string
	BaseURL     string
	OutputsJSON string                 // Raw outputs JSON from MistServer
	Outputs     map[string]interface{} // Parsed outputs map
	LastUpdate  time.Time
}

// Optional analytics callbacks set by handlers package
var clipProgressHandler func(*pb.ClipProgress)
var clipDoneHandler func(*pb.ClipDone)

// SetClipHandlers registers callbacks for analytics emission
func SetClipHandlers(onProgress func(*pb.ClipProgress), onDone func(*pb.ClipDone)) {
	clipProgressHandler = onProgress
	clipDoneHandler = onDone
}

// NodeMetricsProcessor interface for handling node metrics (implemented by handlers)
type NodeMetricsProcessor interface {
	ProcessNodeMetrics(nodeID, baseURL string, isHealthy bool, latitude, longitude *float64, location string, metrics *validation.FoghornNodeUpdate) error
}

// Init initializes the global registry
func Init(logger logging.Logger) {
	registry = &Registry{conns: make(map[string]*conn), log: logger}
}

// SetNodeService sets the node metrics processor
func SetNodeService(service NodeMetricsProcessor) {
	nodeService = service
}

// SetDB sets the database connection for clip operations
func SetDB(database *sql.DB) {
	db = database
}

// SetStreamHealthHandler sets the handler for stream health updates
func SetStreamHealthHandler(handler func(string, string, string, bool, map[string]interface{})) {
	streamHealthHandler = handler
}

// SetClipHashResolver sets the resolver for clip hash lookups
func SetClipHashResolver(resolver func(string) (string, string, error)) {
	clipHashResolver = resolver
}

// Server implements HelmsmanControl
type Server struct {
	pb.UnimplementedHelmsmanControlServer
}

func (s *Server) Connect(stream pb.HelmsmanControl_ConnectServer) error {
	var nodeID string
	// On initial message we expect a Register
	for {
		msg, err := stream.Recv()
		if err != nil {
			break
		}
		switch x := msg.GetPayload().(type) {
		case *pb.ControlMessage_Register:
			nodeID = x.Register.GetNodeId()
			if nodeID == "" {
				p, _ := peer.FromContext(stream.Context())
				registry.log.WithField("peer", func() string {
					if p != nil {
						return p.Addr.String()
					}
					return ""
				}()).Warn("Register without node_id")
				continue
			}
			registry.mu.Lock()
			registry.conns[nodeID] = &conn{stream: stream, last: time.Now()}
			registry.mu.Unlock()
			registry.log.WithField("node_id", nodeID).Info("Helmsman registered")
		case *pb.ControlMessage_NodeUpdate:
			// Update heartbeat and process extended metrics
			if nodeID != "" {
				registry.mu.Lock()
				if c := registry.conns[nodeID]; c != nil {
					c.last = time.Now()
				}
				registry.mu.Unlock()

				// Process extended node metrics
				go processNodeUpdate(x.NodeUpdate, registry.log)
			}
		case *pb.ControlMessage_ClipProgress:
			if clipProgressHandler != nil {
				go clipProgressHandler(x.ClipProgress)
			}
		case *pb.ControlMessage_ClipDone:
			if clipDoneHandler != nil {
				go clipDoneHandler(x.ClipDone)
			}
		case *pb.ControlMessage_Heartbeat:
			if nodeID != "" {
				registry.mu.Lock()
				if c := registry.conns[nodeID]; c != nil {
					c.last = time.Now()
				}
				registry.mu.Unlock()
			}
		case *pb.ControlMessage_StreamHealthUpdate:
			// Handle stream health updates
			go processStreamHealthUpdate(x.StreamHealthUpdate, registry.log)
		case *pb.ControlMessage_DvrStartRequest:
			// Handle DVR start requests from ingest Helmsman
			go processDVRStartRequest(x.DvrStartRequest, nodeID, registry.log)
		case *pb.ControlMessage_DvrProgress:
			// Handle DVR progress updates from storage Helmsman
			go processDVRProgress(x.DvrProgress, registry.log)
		case *pb.ControlMessage_DvrStopped:
			// Handle DVR completion from storage Helmsman
			go processDVRStopped(x.DvrStopped, registry.log)
		case *pb.ControlMessage_DvrReadyRequest:
			// Handle DVR readiness check from storage Helmsman
			go processDVRReadyRequest(x.DvrReadyRequest, nodeID, stream, registry.log)
		}
	}
	if nodeID != "" {
		registry.mu.Lock()
		delete(registry.conns, nodeID)
		registry.mu.Unlock()
		registry.log.WithField("node_id", nodeID).Info("Helmsman disconnected")
	}
	return nil
}

// SendClipPull sends a ClipPullRequest to the given node if connected
func SendClipPull(nodeID string, req *pb.ClipPullRequest) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &pb.ControlMessage{
		Payload: &pb.ControlMessage_ClipPullRequest{ClipPullRequest: req},
		SentAt:  timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

// SendDVRStart sends a DVRStartRequest to the given node if connected
func SendDVRStart(nodeID string, req *pb.DVRStartRequest) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &pb.ControlMessage{
		Payload: &pb.ControlMessage_DvrStartRequest{DvrStartRequest: req},
		SentAt:  timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

// SendDVRStop sends a DVRStopRequest to the given node if connected
func SendDVRStop(nodeID string, req *pb.DVRStopRequest) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &pb.ControlMessage{
		Payload: &pb.ControlMessage_DvrStopRequest{DvrStopRequest: req},
		SentAt:  timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

// StartGRPCServer starts the control gRPC server on the given addr (e.g., ":18009")
func StartGRPCServer(addr string, logger logging.Logger) (*grpc.Server, error) {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	srv := grpc.NewServer()
	pb.RegisterHelmsmanControlServer(srv, &Server{})
	go func() {
		if err := srv.Serve(lis); err != nil {
			logger.WithError(err).Error("Control gRPC server exited")
		}
	}()
	return srv, nil
}

// processNodeUpdate converts gRPC NodeUpdate to validation.FoghornNodeUpdate and forwards to node service
func processNodeUpdate(update *pb.NodeUpdate, logger logging.Logger) {
	if nodeService == nil {
		logger.Warn("NodeService not set, dropping NodeUpdate")
		return
	}

	// Convert protobuf message to validation types
	nodeMetrics := &validation.FoghornNodeUpdate{
		CPU:        float64(update.GetCpuTenths()) / 10.0, // Convert tenths to percentage
		RAMMax:     float64(update.GetRamMax()),
		RAMCurrent: float64(update.GetRamCurrent()),
		UpSpeed:    float64(update.GetUpSpeed()),
		DownSpeed:  float64(update.GetDownSpeed()),
		BWLimit:    float64(update.GetBwLimit()),
		Location: validation.FoghornLocationData{
			Latitude:  update.GetLatitude(),
			Longitude: update.GetLongitude(),
			Name:      update.GetLocation(),
		},
	}

	// Convert capabilities
	if caps := update.GetCapabilities(); caps != nil {
		nodeMetrics.Capabilities = validation.FoghornNodeCapabilities{
			Ingest:     caps.GetIngest(),
			Edge:       caps.GetEdge(),
			Storage:    caps.GetStorage(),
			Processing: caps.GetProcessing(),
			Roles:      caps.GetRoles(),
		}
	}

	// Convert storage info
	if storage := update.GetStorage(); storage != nil {
		nodeMetrics.Storage = validation.FoghornStorageInfo{
			LocalPath: storage.GetLocalPath(),
			S3Bucket:  storage.GetS3Bucket(),
			S3Prefix:  storage.GetS3Prefix(),
		}
	}

	// Convert limits
	if limits := update.GetLimits(); limits != nil {
		nodeMetrics.Limits = &validation.FoghornNodeLimits{
			MaxTranscodes:        int(limits.GetMaxTranscodes()),
			StorageCapacityBytes: limits.GetStorageCapacityBytes(),
			StorageUsedBytes:     limits.GetStorageUsedBytes(),
		}
	}

	// Convert stream metrics
	nodeMetrics.Streams = make(map[string]validation.FoghornStreamData)
	for streamName, streamData := range update.GetStreams() {
		nodeMetrics.Streams[streamName] = validation.FoghornStreamData{
			Total:     streamData.GetTotal(),
			Inputs:    streamData.GetInputs(),
			BytesUp:   streamData.GetBytesUp(),
			BytesDown: streamData.GetBytesDown(),
			Bandwidth: streamData.GetBandwidth(),
		}
	}

	// Convert artifacts (using secure clip hashes)
	for _, artifact := range update.GetArtifacts() {
		// Use stream_name/clip_hash as ID (no tenant info exposed)
		artifactID := artifact.GetStreamName() + "/" + artifact.GetClipHash()

		nodeMetrics.Artifacts = append(nodeMetrics.Artifacts, validation.FoghornStoredArtifact{
			ID:        artifactID,
			Type:      "clip", // Default to clip type
			Path:      artifact.GetFilePath(),
			URL:       artifact.GetS3Url(),
			SizeBytes: artifact.GetSizeBytes(),
			CreatedAt: artifact.GetCreatedAt(),
			Format:    artifact.GetFormat(),
		})
	}

	// Extract location pointers
	var latitude, longitude *float64
	if update.GetLatitude() != 0 {
		lat := update.GetLatitude()
		latitude = &lat
	}
	if update.GetLongitude() != 0 {
		lon := update.GetLongitude()
		longitude = &lon
	}

	// Track node outputs for DTSC URI resolution
	if outputsJSON := update.GetOutputsJson(); outputsJSON != "" {
		go updateNodeOutputs(update.GetNodeId(), update.GetBaseUrl(), outputsJSON, logger)
	}

	// Forward to node service
	if err := nodeService.ProcessNodeMetrics(
		update.GetNodeId(),
		update.GetBaseUrl(),
		update.GetIsHealthy(),
		latitude,
		longitude,
		update.GetLocation(),
		nodeMetrics,
	); err != nil {
		logger.WithFields(logging.Fields{
			"node_id": update.GetNodeId(),
			"error":   err,
		}).Error("Failed to process node metrics via gRPC")
	} else {
		logger.WithField("node_id", update.GetNodeId()).Debug("Processed node metrics via gRPC")
	}
}

// Helpers

var ErrNotConnected = status.Error(codes.Unavailable, "node not connected")

// handleClipProgress processes clip progress updates from Helmsman nodes
func handleClipProgress(progress *pb.ClipProgress, logger logging.Logger) {
	requestID := progress.GetRequestId()
	percent := progress.GetPercent()
	message := progress.GetMessage()

	logger.WithFields(logging.Fields{
		"request_id": requestID,
		"percent":    percent,
		"message":    message,
	}).Debug("Clip progress update")

	if db == nil {
		logger.Warn("Database not set, cannot update clip progress")
		return
	}

	// Update clips table with processing progress
	// Note: We track by request_id which was stored during HandleCreateClip
	_, err := db.Exec(`
		UPDATE foghorn.clips 
		SET status = CASE 
			WHEN $2 = 100 THEN 'processing'
			ELSE 'processing'
		END,
		updated_at = NOW()
		WHERE request_id = $1`,
		requestID, percent)

	if err != nil {
		logger.WithFields(logging.Fields{
			"request_id": requestID,
			"error":      err,
		}).Error("Failed to update clip progress in database")
		return
	}

	logger.WithFields(logging.Fields{
		"request_id": requestID,
		"percent":    percent,
	}).Debug("Updated clip progress in database")
}

// handleClipDone processes clip completion notifications from Helmsman nodes
func handleClipDone(done *pb.ClipDone, logger logging.Logger) {
	requestID := done.GetRequestId()
	filePath := done.GetFilePath()
	sizeBytes := done.GetSizeBytes()
	status := done.GetStatus()
	errorMsg := done.GetError()

	logger.WithFields(logging.Fields{
		"request_id": requestID,
		"file_path":  filePath,
		"size_bytes": sizeBytes,
		"status":     status,
		"error":      errorMsg,
	}).Info("Clip processing completed")

	if db == nil {
		logger.Warn("Database not set, cannot update clip completion")
		return
	}

	// Update clips table with final status and file info
	var clipStatus clips.ClipStatus
	if status == "success" {
		clipStatus = clips.ClipStatusReady
	} else {
		clipStatus = clips.ClipStatusFailed
	}

	_, err := db.Exec(`
		UPDATE foghorn.clips 
		SET status = $1, 
		    storage_path = $2,
		    size_bytes = $3,
		    error_message = NULLIF($4, ''),
		    updated_at = NOW()
		WHERE request_id = $5`,
		clipStatus, filePath, int64(sizeBytes), errorMsg, requestID)

	if err != nil {
		logger.WithFields(logging.Fields{
			"request_id": requestID,
			"error":      err,
		}).Error("Failed to update clip completion in database")
		return
	}

	// Update artifact registry if successful
	if status == "success" && filePath != "" {
		// Extract node_id from the request context or stored mapping
		// For now, we'll skip this as we don't have the node context here
		// This could be enhanced to track which node processed which request
		logger.WithField("request_id", requestID).Debug("Clip marked as ready")
	} else {
		logger.WithFields(logging.Fields{
			"request_id": requestID,
			"error":      errorMsg,
		}).Warn("Clip processing failed")
	}
}

// processStreamHealthUpdate processes stream health updates from Helmsman nodes
func processStreamHealthUpdate(update *pb.StreamHealthUpdate, logger logging.Logger) {
	if streamHealthHandler == nil {
		logger.Warn("StreamHealthHandler not set, dropping StreamHealthUpdate")
		return
	}

	// Convert protobuf details to map
	details := make(map[string]interface{})
	bufferState := ""
	status := ""

	if update.Details != nil {
		details["buffer_state"] = update.Details.BufferState
		details["status"] = update.Details.Status
		details["stream_details"] = update.Details.StreamDetails
		bufferState = update.Details.BufferState
		status = update.Details.Status
	}

	// Update stream health tracking for DVR readiness
	go updateStreamHealthTracking(
		update.GetInternalName(),
		update.GetNodeId(),
		update.GetIsHealthy(),
		bufferState,
		status,
		logger,
	)

	// Call the handler with converted parameters
	streamHealthHandler(
		update.GetNodeId(),
		update.GetStreamName(),
		update.GetInternalName(),
		update.GetIsHealthy(),
		details,
	)

	logger.WithFields(logging.Fields{
		"node_id":       update.GetNodeId(),
		"stream_name":   update.GetStreamName(),
		"internal_name": update.GetInternalName(),
		"is_healthy":    update.GetIsHealthy(),
	}).Debug("Processed stream health update via gRPC")
}

// processDVRStartRequest handles DVR start requests from ingest Helmsman
func processDVRStartRequest(req *pb.DVRStartRequest, nodeID string, logger logging.Logger) {
	// Generate DVR hash if not provided
	dvrHash := req.GetDvrHash()
	if dvrHash == "" {
		var err error
		dvrHash, err = dvr.GenerateDVRHash()
		if err != nil {
			logger.WithError(err).Error("Failed to generate DVR hash")
			return
		}
	}

	logger.WithFields(logging.Fields{
		"dvr_hash":      dvrHash,
		"internal_name": req.GetInternalName(),
		"tenant_id":     req.GetTenantId(),
		"node_id":       nodeID,
	}).Info("Processing DVR start request")

	// Store minimal state in database
	_, err := db.Exec(`
		INSERT INTO foghorn.dvr_requests (
			request_hash, tenant_id, internal_name, status, created_at
		) VALUES ($1, $2, $3, 'requested', NOW())
		ON CONFLICT (request_hash) DO UPDATE SET
			status = 'requested',
			updated_at = NOW()`,
		dvrHash, req.GetTenantId(), req.GetInternalName())

	if err != nil {
		logger.WithFields(logging.Fields{
			"dvr_hash": dvrHash,
			"error":    err,
		}).Error("Failed to store DVR request")
		return
	}

	// Find available storage node with DVR capabilities
	storageNodeID, storageNodeURL, err := findStorageNodeForDVR(req.GetTenantId(), logger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"dvr_hash": dvrHash,
			"error":    err,
		}).Error("Failed to find storage node for DVR")

		// Update request as failed
		db.Exec(`UPDATE foghorn.dvr_requests SET status = 'failed', error_message = $1, updated_at = NOW() WHERE request_hash = $2`,
			err.Error(), dvrHash)
		return
	}

	// Construct source DTSC URL from ingest node
	ingestNodeURL, err := getNodeBaseURL(nodeID)
	if err != nil {
		logger.WithFields(logging.Fields{
			"dvr_hash": dvrHash,
			"node_id":  nodeID,
			"error":    err,
		}).Error("Failed to get ingest node URL")
		return
	}

	sourceDTSCURL := ingestNodeURL + "/" + req.GetInternalName() + ".dtsc"

	// Create enhanced DVR request for storage node
	enhancedReq := &pb.DVRStartRequest{
		DvrHash:       dvrHash,
		InternalName:  req.GetInternalName(),
		SourceBaseUrl: sourceDTSCURL,
		RequestId:     req.GetRequestId(),
		Config:        req.GetConfig(),
		TenantId:      req.GetTenantId(),
		UserId:        req.GetUserId(),
	}

	// Update database with storage node info
	_, err = db.Exec(`
		UPDATE foghorn.dvr_requests 
		SET storage_node_id = $1, storage_node_url = $2, updated_at = NOW()
		WHERE request_hash = $3`,
		storageNodeID, storageNodeURL, dvrHash)

	if err != nil {
		logger.WithFields(logging.Fields{
			"dvr_hash": dvrHash,
			"error":    err,
		}).Error("Failed to update DVR request with storage node info")
	}

	// Forward enhanced request to storage node
	if err := SendDVRStart(storageNodeID, enhancedReq); err != nil {
		logger.WithFields(logging.Fields{
			"dvr_hash":        dvrHash,
			"storage_node_id": storageNodeID,
			"error":           err,
		}).Error("Failed to send DVR start to storage node")

		// Update request as failed
		db.Exec(`UPDATE foghorn.dvr_requests SET status = 'failed', error_message = $1, updated_at = NOW() WHERE request_hash = $2`,
			err.Error(), dvrHash)
		return
	}

	logger.WithFields(logging.Fields{
		"dvr_hash":        dvrHash,
		"storage_node_id": storageNodeID,
		"source_url":      sourceDTSCURL,
	}).Info("DVR start request forwarded to storage node")
}

// processDVRProgress handles DVR progress updates from storage Helmsman
func processDVRProgress(progress *pb.DVRProgress, logger logging.Logger) {
	dvrHash := progress.GetDvrHash()
	status := progress.GetStatus()
	segmentCount := progress.GetSegmentCount()
	sizeBytes := progress.GetSizeBytes()
	message := progress.GetMessage()

	logger.WithFields(logging.Fields{
		"dvr_hash":      dvrHash,
		"status":        status,
		"segment_count": segmentCount,
		"size_bytes":    sizeBytes,
		"message":       message,
	}).Debug("DVR progress update")

	if db == nil {
		logger.Warn("Database not set, cannot update DVR progress")
		return
	}

	// Update DVR request with progress
	_, err := db.Exec(`
		UPDATE foghorn.dvr_requests 
		SET status = $2,
		    size_bytes = $3,
		    updated_at = NOW()
		WHERE request_hash = $1`,
		dvrHash, status, sizeBytes)

	if err != nil {
		logger.WithFields(logging.Fields{
			"dvr_hash": dvrHash,
			"error":    err,
		}).Error("Failed to update DVR progress in database")
		return
	}

	logger.WithFields(logging.Fields{
		"dvr_hash":      dvrHash,
		"status":        status,
		"segment_count": segmentCount,
	}).Debug("Updated DVR progress in database")
}

// processDVRStopped handles DVR completion from storage Helmsman
func processDVRStopped(stopped *pb.DVRStopped, logger logging.Logger) {
	dvrHash := stopped.GetDvrHash()
	status := stopped.GetStatus()
	errorMsg := stopped.GetError()
	manifestPath := stopped.GetManifestPath()
	durationSeconds := stopped.GetDurationSeconds()
	sizeBytes := stopped.GetSizeBytes()

	logger.WithFields(logging.Fields{
		"dvr_hash":         dvrHash,
		"status":           status,
		"manifest_path":    manifestPath,
		"duration_seconds": durationSeconds,
		"size_bytes":       sizeBytes,
		"error":            errorMsg,
	}).Info("DVR recording completed")

	if db == nil {
		logger.Warn("Database not set, cannot update DVR completion")
		return
	}

	// Determine final status
	var finalStatus string
	if status == "success" {
		finalStatus = "completed"
	} else {
		finalStatus = "failed"
	}

	// Update DVR request with final status and details
	_, err := db.Exec(`
		UPDATE foghorn.dvr_requests 
		SET status = $1,
		    ended_at = NOW(),
		    duration_seconds = $2,
		    size_bytes = $3,
		    manifest_path = $4,
		    error_message = NULLIF($5, ''),
		    updated_at = NOW()
		WHERE request_hash = $6`,
		finalStatus, durationSeconds, sizeBytes, manifestPath, errorMsg, dvrHash)

	if err != nil {
		logger.WithFields(logging.Fields{
			"dvr_hash": dvrHash,
			"error":    err,
		}).Error("Failed to update DVR completion in database")
		return
	}

	if status == "success" && manifestPath != "" {
		logger.WithFields(logging.Fields{
			"dvr_hash":      dvrHash,
			"manifest_path": manifestPath,
			"size_bytes":    sizeBytes,
		}).Info("DVR recording completed successfully")
	} else {
		logger.WithFields(logging.Fields{
			"dvr_hash": dvrHash,
			"error":    errorMsg,
		}).Warn("DVR recording failed")
	}
}

// findStorageNodeForDVR finds an available storage node with DVR capabilities for the given tenant
func findStorageNodeForDVR(tenantID string, logger logging.Logger) (string, string, error) {
	// Get the load balancer from handlers package
	// This accesses the same load balancer used for regular node routing
	nodes := getLoadBalancerNodes()
	if nodes == nil {
		return "", "", fmt.Errorf("load balancer not available")
	}

	// Find nodes with storage capabilities
	var bestNode *balancerNode
	var bestScore uint64

	for baseURL, node := range nodes {
		// Skip non-storage nodes
		if !node.IsStorageCapable() {
			continue
		}

		// Skip inactive nodes
		if !node.IsActive() {
			continue
		}

		// Calculate a simple score based on available resources
		// Higher score is better (more available resources)
		storageScore := uint64(0)

		// Factor in available storage space
		capacityBytes := node.GetStorageCapacityBytes()
		usedBytes := node.GetStorageUsedBytes()
		if capacityBytes > usedBytes {
			availableStorage := capacityBytes - usedBytes
			storageScore += availableStorage / (1024 * 1024 * 1024) // Convert to GB for scoring
		}

		// Factor in CPU availability (lower CPU usage = higher score)
		cpu := node.GetCPU()
		if cpu < 800 { // Less than 80% CPU usage
			storageScore += (1000 - cpu) / 10 // 0-20 point bonus
		}

		// Factor in RAM availability
		ramMax := node.GetRAMMax()
		ramCurrent := node.GetRAMCurrent()
		if ramMax > ramCurrent {
			availableRAM := ramMax - ramCurrent
			storageScore += availableRAM / 1024 // Convert MB to GB-ish for scoring
		}

		if storageScore > bestScore {
			bestScore = storageScore
			bestNode = &balancerNode{
				BaseURL: baseURL,
				NodeID:  getNodeIDFromLoadBalancer(baseURL),
			}
		}
	}

	if bestNode == nil {
		return "", "", fmt.Errorf("no available storage nodes found")
	}

	logger.WithFields(logging.Fields{
		"tenant_id": tenantID,
		"node_id":   bestNode.NodeID,
		"base_url":  bestNode.BaseURL,
		"score":     bestScore,
	}).Debug("Selected storage node for DVR")

	return bestNode.NodeID, bestNode.BaseURL, nil
}

// balancerNode is a helper struct for node selection
type balancerNode struct {
	BaseURL string
	NodeID  string
}

// getNodeBaseURL retrieves the base URL for a given node ID
func getNodeBaseURL(nodeID string) (string, error) {
	// Use the load balancer's GetNodeByID method to look up the base URL
	baseURL := getNodeBaseURLFromLoadBalancer(nodeID)
	if baseURL == "" {
		return "", fmt.Errorf("node %s not found in load balancer", nodeID)
	}
	return baseURL, nil
}

// ResolveClipHash implements the ResolveClipHash RPC method
func (s *Server) ResolveClipHash(ctx context.Context, req *pb.ClipHashRequest) (*pb.ClipHashResponse, error) {
	if clipHashResolver == nil {
		return nil, status.Error(codes.Unimplemented, "clip hash resolution not configured")
	}

	tenantID, streamName, err := clipHashResolver(req.GetClipHash())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	if tenantID == "" {
		return nil, status.Error(codes.NotFound, "clip not found")
	}

	return &pb.ClipHashResponse{
		ClipHash:   req.GetClipHash(),
		TenantId:   tenantID,
		StreamName: streamName,
	}, nil
}

// Global references to handlers' load balancer (set by handlers.Init)
var loadBalancerInstance LoadBalancerInterface

// LoadBalancerInterface defines methods needed from the load balancer
type LoadBalancerInterface interface {
	GetNodes() map[string]NodeInterface
	GetNodeByID(nodeID string) (string, error)
	GetNodeIDByHost(host string) string
}

// NodeInterface defines the node properties we need for DVR selection
type NodeInterface interface {
	IsStorageCapable() bool
	IsActive() bool
	GetStorageCapacityBytes() uint64
	GetStorageUsedBytes() uint64
	GetCPU() uint64
	GetRAMMax() uint64
	GetRAMCurrent() uint64
}

// SetLoadBalancer allows handlers package to inject the load balancer instance
func SetLoadBalancer(lb LoadBalancerInterface) {
	loadBalancerInstance = lb
}

// getLoadBalancerNodes returns nodes from the load balancer with type conversion
func getLoadBalancerNodes() map[string]NodeInterface {
	if loadBalancerInstance == nil {
		return nil
	}

	nodes := make(map[string]NodeInterface)
	for baseURL, node := range loadBalancerInstance.GetNodes() {
		nodes[baseURL] = node
	}
	return nodes
}

// getNodeBaseURLFromLoadBalancer gets base URL for a node ID
func getNodeBaseURLFromLoadBalancer(nodeID string) string {
	if loadBalancerInstance == nil {
		return ""
	}

	baseURL, err := loadBalancerInstance.GetNodeByID(nodeID)
	if err != nil {
		return ""
	}
	return baseURL
}

// getNodeIDFromLoadBalancer gets node ID for a base URL
func getNodeIDFromLoadBalancer(baseURL string) string {
	if loadBalancerInstance == nil {
		return ""
	}

	return loadBalancerInstance.GetNodeIDByHost(baseURL)
}

// updateStreamHealthTracking updates the stream health map for DVR readiness checks
func updateStreamHealthTracking(internalName, nodeID string, isHealthy bool, bufferState, status string, logger logging.Logger) {
	streamHealthMutex.Lock()
	defer streamHealthMutex.Unlock()

	// Get DTSC output URI from node configuration
	sourceBaseURL := getDTSCOutputURI(nodeID, logger)
	if sourceBaseURL == "" {
		logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"node_id":       nodeID,
		}).Debug("Could not resolve DTSC output URI for stream health tracking")
	}

	streamHealthMap[internalName] = &StreamHealthStatus{
		InternalName:  internalName,
		IsHealthy:     isHealthy,
		BufferState:   bufferState,
		Status:        status,
		LastUpdate:    time.Now(),
		SourceNodeID:  nodeID,
		SourceBaseURL: sourceBaseURL,
	}

	logger.WithFields(logging.Fields{
		"internal_name": internalName,
		"node_id":       nodeID,
		"is_healthy":    isHealthy,
		"buffer_state":  bufferState,
		"status":        status,
	}).Debug("Updated stream health tracking")
}

// processDVRReadyRequest handles DVR readiness checks from storage Helmsman
func processDVRReadyRequest(req *pb.DVRReadyRequest, requestingNodeID string, stream pb.HelmsmanControl_ConnectServer, logger logging.Logger) {
	dvrHash := req.GetDvrHash()

	logger.WithFields(logging.Fields{
		"dvr_hash":           dvrHash,
		"requesting_node_id": requestingNodeID,
	}).Debug("Processing DVR readiness request")

	// Look up the DVR request in database to get stream info
	var tenantID, internalName string
	err := db.QueryRow(`
		SELECT tenant_id, internal_name 
		FROM foghorn.dvr_requests 
		WHERE request_hash = $1`,
		dvrHash).Scan(&tenantID, &internalName)

	if err != nil {
		logger.WithFields(logging.Fields{
			"dvr_hash": dvrHash,
			"error":    err,
		}).Error("DVR request not found in database")

		// Send not ready response
		response := &pb.DVRReadyResponse{
			DvrHash: dvrHash,
			Ready:   false,
			Reason:  "dvr_request_not_found",
		}
		sendDVRReadyResponse(stream, response, logger)
		return
	}

	// Check stream health status
	streamHealthMutex.RLock()
	streamHealth, exists := streamHealthMap[internalName]
	streamHealthMutex.RUnlock()

	if !exists {
		logger.WithFields(logging.Fields{
			"dvr_hash":      dvrHash,
			"internal_name": internalName,
		}).Debug("Stream health not tracked yet")

		response := &pb.DVRReadyResponse{
			DvrHash: dvrHash,
			Ready:   false,
			Reason:  "stream_not_tracked",
		}
		sendDVRReadyResponse(stream, response, logger)
		return
	}

	// Check if stream is ready for DVR (healthy and buffer full/recovering)
	isReady := streamHealth.IsHealthy &&
		(streamHealth.BufferState == "FULL" || streamHealth.BufferState == "RECOVER") &&
		streamHealth.Status == "live"

	if !isReady {
		var reason string
		if !streamHealth.IsHealthy {
			reason = "stream_unhealthy"
		} else if streamHealth.Status != "live" {
			reason = "stream_offline"
		} else {
			reason = "stream_booting"
		}

		logger.WithFields(logging.Fields{
			"dvr_hash":      dvrHash,
			"internal_name": internalName,
			"is_healthy":    streamHealth.IsHealthy,
			"buffer_state":  streamHealth.BufferState,
			"status":        streamHealth.Status,
			"reason":        reason,
		}).Debug("Stream not ready for DVR")

		response := &pb.DVRReadyResponse{
			DvrHash: dvrHash,
			Ready:   false,
			Reason:  reason,
		}
		sendDVRReadyResponse(stream, response, logger)
		return
	}

	// Stream is ready! Build source URI and potentially mutate config
	sourceURI := streamHealth.SourceBaseURL + "/" + internalName + ".dtsc"

	// Get original config from database and potentially enhance it
	var configBytes []byte
	err = db.QueryRow(`
		SELECT dr.request_hash, s.recording_config
		FROM foghorn.dvr_requests dr
		JOIN foghorn.streams s ON s.internal_name = dr.internal_name
		WHERE dr.request_hash = $1`,
		dvrHash).Scan(&dvrHash, &configBytes)

	// Default config in case we can't get it from database
	config := &pb.DVRConfig{
		Enabled:         true,
		RetentionDays:   30,
		Format:          "ts",
		SegmentDuration: 6,
	}

	// TODO: Parse configBytes and potentially mutate based on stream characteristics
	// For example:
	// - Adjust segment duration based on keyframe interval
	// - Change format based on codec detection
	// - Set bitrate limits based on stream quality

	logger.WithFields(logging.Fields{
		"dvr_hash":      dvrHash,
		"internal_name": internalName,
		"source_uri":    sourceURI,
		"is_ready":      true,
	}).Info("Stream ready for DVR recording")

	response := &pb.DVRReadyResponse{
		DvrHash:   dvrHash,
		Ready:     true,
		SourceUri: sourceURI,
		Config:    config,
		Reason:    "stream_ready",
	}
	sendDVRReadyResponse(stream, response, logger)

	// Update database status to indicate storage node is starting recording
	_, err = db.Exec(`
		UPDATE foghorn.dvr_requests 
		SET status = 'starting', started_at = NOW(), updated_at = NOW()
		WHERE request_hash = $1`,
		dvrHash)

	if err != nil {
		logger.WithFields(logging.Fields{
			"dvr_hash": dvrHash,
			"error":    err,
		}).Error("Failed to update DVR request status to starting")
	}
}

// sendDVRReadyResponse sends a DVRReadyResponse back to the requesting storage node
func sendDVRReadyResponse(stream pb.HelmsmanControl_ConnectServer, response *pb.DVRReadyResponse, logger logging.Logger) {
	msg := &pb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &pb.ControlMessage_DvrReadyResponse{DvrReadyResponse: response},
	}

	if err := stream.Send(msg); err != nil {
		logger.WithFields(logging.Fields{
			"dvr_hash": response.DvrHash,
			"error":    err,
		}).Error("Failed to send DVR ready response")
	}
}

// updateNodeOutputs updates the node outputs map for DTSC URI resolution
func updateNodeOutputs(nodeID, baseURL, outputsJSON string, logger logging.Logger) {
	nodeOutputsMutex.Lock()
	defer nodeOutputsMutex.Unlock()

	// Parse the outputs JSON
	var outputs map[string]interface{}
	if err := json.Unmarshal([]byte(outputsJSON), &outputs); err != nil {
		logger.WithFields(logging.Fields{
			"node_id": nodeID,
			"error":   err,
		}).Error("Failed to parse node outputs JSON")
		return
	}

	nodeOutputsMap[nodeID] = &NodeOutputs{
		NodeID:      nodeID,
		BaseURL:     baseURL,
		OutputsJSON: outputsJSON,
		Outputs:     outputs,
		LastUpdate:  time.Now(),
	}

	logger.WithFields(logging.Fields{
		"node_id":  nodeID,
		"base_url": baseURL,
	}).Debug("Updated node outputs tracking")
}

// getDTSCOutputURI constructs the DTSC output URI for a given node using MistServer outputs configuration
func getDTSCOutputURI(nodeID string, logger logging.Logger) string {
	nodeOutputsMutex.RLock()
	nodeOutput, exists := nodeOutputsMap[nodeID]
	nodeOutputsMutex.RUnlock()

	if !exists {
		logger.WithField("node_id", nodeID).Debug("Node outputs not tracked yet")
		return ""
	}

	// Look for DTSC output in the outputs map
	dtscOutput, exists := nodeOutput.Outputs["DTSC"]
	if !exists {
		logger.WithField("node_id", nodeID).Debug("No DTSC output found in node outputs")
		return ""
	}

	// DTSC output format is typically "dtsc://HOST/$"
	dtscTemplate, ok := dtscOutput.(string)
	if !ok {
		logger.WithFields(logging.Fields{
			"node_id":     nodeID,
			"dtsc_output": dtscOutput,
		}).Debug("DTSC output is not a string")
		return ""
	}

	// Replace HOST with the actual node hostname
	// Extract hostname from base URL (e.g., "https://mist-seattle.stronk.rocks" -> "mist-seattle.stronk.rocks")
	hostname := nodeOutput.BaseURL
	if strings.HasPrefix(hostname, "https://") {
		hostname = strings.TrimPrefix(hostname, "https://")
	}
	if strings.HasPrefix(hostname, "http://") {
		hostname = strings.TrimPrefix(hostname, "http://")
	}

	// Replace HOST placeholder with actual hostname
	dtscURI := strings.Replace(dtscTemplate, "HOST", hostname, -1)

	// For DVR readiness, we want the base URI without the $ placeholder
	// The $ will be replaced with the actual stream name later
	baseDTSCURI := strings.Replace(dtscURI, "$", "", -1)

	// Remove trailing slash if present
	baseDTSCURI = strings.TrimSuffix(baseDTSCURI, "/")

	logger.WithFields(logging.Fields{
		"node_id":       nodeID,
		"hostname":      hostname,
		"dtsc_template": dtscTemplate,
		"dtsc_uri":      baseDTSCURI,
	}).Debug("Constructed DTSC base URI")

	return baseDTSCURI
}

// GetNodeOutputs returns the outputs for a given node ID (for viewer endpoint resolution)
func GetNodeOutputs(nodeID string) (*NodeOutputs, bool) {
	nodeOutputsMutex.RLock()
	defer nodeOutputsMutex.RUnlock()

	outputs, exists := nodeOutputsMap[nodeID]
	return outputs, exists
}
