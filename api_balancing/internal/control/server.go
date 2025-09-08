package control

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"frameworks/api_balancing/internal/state"
	qmapi "frameworks/pkg/api/quartermaster"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/dvr"
	"frameworks/pkg/geoip"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func streamCtx() context.Context { return context.Background() }

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
var streamHealthHandler func(string, string, string, bool, map[string]interface{})
var clipHashResolver func(string) (string, string, error)
var db *sql.DB
var quartermasterClient *qmclient.Client

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

// GetStreamSource returns the source node and base URL for a given internal_name if known
func GetStreamSource(internalName string) (nodeID string, baseURL string, ok bool) {
	if s := state.DefaultManager().GetStreamState(internalName); s != nil {
		nodeID = s.NodeID
		if ns := state.DefaultManager().GetNodeState(nodeID); ns != nil {
			baseURL = ns.BaseURL
		}
		if nodeID != "" {
			return nodeID, baseURL, true
		}
	}

	streamHealthMutex.RLock()
	sh, exists := streamHealthMap[internalName]
	streamHealthMutex.RUnlock()
	if !exists {
		return "", "", false
	}
	return sh.SourceNodeID, sh.SourceBaseURL, true
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
	ProcessNodeMetrics(nodeID, baseURL string, isHealthy bool, latitude, longitude *float64, location string, metrics *pb.NodeLifecycleUpdate) error
}

// Init initializes the global registry
func Init(logger logging.Logger) {
	registry = &Registry{conns: make(map[string]*conn), log: logger}
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

// SetQuartermasterClient sets the Quartermaster client for edge enrollment and lookups
func SetQuartermasterClient(c *qmclient.Client) { quartermasterClient = c }

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
			// Mark node healthy in unified state (baseURL unknown at register)
			state.DefaultManager().SetNodeInfo(nodeID, "", true, nil, nil, "", "", nil)
			var peerAddr string
			if p, _ := peer.FromContext(stream.Context()); p != nil {
				peerAddr = p.Addr.String()
			}

			// Fingerprint-based tenant resolution (pre-provisioned mappings only; no creation here)
			tenantID := ""
			canonicalNodeID := nodeID
			{
				// Build resolver request
				host := ""
				if md, ok := metadata.FromIncomingContext(stream.Context()); ok {
					if fwd := md.Get("x-forwarded-for"); len(fwd) > 0 {
						parts := strings.Split(fwd[0], ",")
						if len(parts) > 0 {
							host = strings.TrimSpace(parts[0])
						}
					}
				}
				if host == "" {
					h, _, _ := net.SplitHostPort(peerAddr)
					if h == "" {
						h = peerAddr
					}
					host = h
				}
				var country, city string
				var lat, lon float64
				geoOnce.Do(func() { geoipReader = geoip.GetSharedReader() })
				if geoipReader != nil {
					if gd := geoipReader.Lookup(host); gd != nil {
						country = gd.CountryCode
						city = gd.City
						lat = gd.Latitude
						lon = gd.Longitude
					}
				}
				fpReq := &qmapi.ResolveNodeFingerprintRequest{PeerIP: host, GeoCountry: country, GeoCity: city, GeoLatitude: lat, GeoLongitude: lon}
				if x.Register != nil && x.Register.Fingerprint != nil {
					fp := x.Register.Fingerprint
					fpReq.LocalIPv4 = append(fpReq.LocalIPv4, fp.GetLocalIpv4()...)
					fpReq.LocalIPv6 = append(fpReq.LocalIPv6, fp.GetLocalIpv6()...)
					if fp.GetMacsSha256() != "" {
						s := fp.GetMacsSha256()
						fpReq.MacsSHA256 = &s
					}
					if fp.GetMachineIdSha256() != "" {
						s := fp.GetMachineIdSha256()
						fpReq.MachineIDSHA256 = &s
					}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if resp, err := quartermasterClient.ResolveNodeFingerprint(ctx, fpReq); err == nil && resp != nil {
					tenantID = resp.TenantID
					if resp.CanonicalNodeID != "" {
						canonicalNodeID = resp.CanonicalNodeID
					}
					registry.log.WithFields(logging.Fields{"node_id": canonicalNodeID, "tenant_id": tenantID}).Info("Resolved tenant via fingerprint")
				}
				cancel()
			}

			// Edge enrollment handshake: if enrollment token provided, register this node in Quartermaster
			if tok := strings.TrimSpace(x.Register.GetEnrollmentToken()); tok != "" && quartermasterClient != nil {
				// Parse client IP from forwarded metadata or peer address
				host := ""
				if md, ok := metadata.FromIncomingContext(stream.Context()); ok {
					if fwd := md.Get("x-forwarded-for"); len(fwd) > 0 {
						// Use first IP in list
						parts := strings.Split(fwd[0], ",")
						if len(parts) > 0 {
							host = strings.TrimSpace(parts[0])
						}
					}
				}
				if host == "" {
					h, _, _ := net.SplitHostPort(peerAddr)
					if h == "" {
						h = peerAddr
					}
					host = h
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				req := &qmapi.BootstrapEdgeNodeRequest{Token: tok, IPs: []string{host}}
				// Include client-provided fingerprint to bind mapping at enrollment
				if x.Register != nil && x.Register.Fingerprint != nil {
					fp := x.Register.Fingerprint
					if v := fp.GetLocalIpv4(); len(v) > 0 {
						req.LocalIPv4 = append(req.LocalIPv4, v...)
					}
					if v := fp.GetLocalIpv6(); len(v) > 0 {
						req.LocalIPv6 = append(req.LocalIPv6, v...)
					}
					if fp.GetMacsSha256() != "" {
						s := fp.GetMacsSha256()
						req.MacsSHA256 = &s
					}
					if fp.GetMachineIdSha256() != "" {
						s := fp.GetMachineIdSha256()
						req.MachineIDSHA256 = &s
					}
				}
				if resp, err := quartermasterClient.BootstrapEdgeNode(ctx, req); err == nil && resp != nil {
					if resp.NodeID != "" {
						canonicalNodeID = resp.NodeID
					}
					tenantID = resp.TenantID
					registry.log.WithFields(logging.Fields{"node_id": canonicalNodeID, "tenant_id": tenantID}).Info("Edge node enrolled via Quartermaster")
				} else if err != nil {
					registry.log.WithError(err).WithField("node_id", nodeID).Warn("Edge enrollment failed; continuing without mapping")
				}
			}

			seed := composeConfigSeed(canonicalNodeID, x.Register.GetRoles(), peerAddr)
			if tenantID != "" {
				seed.TenantId = tenantID
			}
			_ = SendConfigSeed(nodeID, seed)
		case *pb.ControlMessage_ClipProgress:
			if clipProgressHandler != nil {
				go clipProgressHandler(x.ClipProgress)
			}
			go handleClipProgress(x.ClipProgress, nodeID, registry.log)
		case *pb.ControlMessage_ClipDone:
			if clipDoneHandler != nil {
				go clipDoneHandler(x.ClipDone)
			}
			go handleClipDone(x.ClipDone, nodeID, registry.log)
		case *pb.ControlMessage_Heartbeat:
			if nodeID != "" {
				registry.mu.Lock()
				if c := registry.conns[nodeID]; c != nil {
					c.last = time.Now()
				}
				registry.mu.Unlock()
				// Refresh node health/last update
				state.DefaultManager().SetNodeInfo(nodeID, "", true, nil, nil, "", "", nil)
			}
		case *pb.ControlMessage_DvrStartRequest:
			// Handle DVR start requests from ingest Helmsman
			go processDVRStartRequest(x.DvrStartRequest, nodeID, registry.log)
		case *pb.ControlMessage_DvrProgress:
			// Handle DVR progress updates from storage Helmsman
			go processDVRProgress(x.DvrProgress, nodeID, registry.log)
		case *pb.ControlMessage_DvrStopped:
			// Handle DVR completion from storage Helmsman
			go processDVRStopped(x.DvrStopped, nodeID, registry.log)
		case *pb.ControlMessage_DvrReadyRequest:
			// Handle DVR readiness check from storage Helmsman
			go processDVRReadyRequest(x.DvrReadyRequest, nodeID, stream, registry.log)
		case *pb.ControlMessage_MistTrigger:
			// Handle MistServer trigger forwarding from Helmsman
			go processMistTrigger(x.MistTrigger, nodeID, stream, registry.log)
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

// StopDVRByInternalName finds an active DVR for a stream and sends a stop to its storage node
func StopDVRByInternalName(internalName string, logger logging.Logger) {
	if db == nil || internalName == "" {
		return
	}
	var dvrHash, storageNodeID string
	err := db.QueryRow(`
        SELECT request_hash, COALESCE(storage_node_id,'')
        FROM foghorn.dvr_requests
        WHERE internal_name = $1 AND status IN ('requested','starting','recording')
        ORDER BY created_at DESC
        LIMIT 1`, internalName).Scan(&dvrHash, &storageNodeID)
	if err != nil {
		return
	}
	if storageNodeID == "" || dvrHash == "" {
		return
	}
	_ = SendDVRStop(storageNodeID, &pb.DVRStopRequest{DvrHash: dvrHash, RequestId: dvrHash})
	_, _ = db.Exec(`UPDATE foghorn.dvr_requests SET status = 'stopping', updated_at = NOW() WHERE request_hash = $1`, dvrHash)
}

// StartGRPCServer starts the control gRPC server on the given addr (e.g., ":18009")
func StartGRPCServer(addr string, logger logging.Logger) (*grpc.Server, error) {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	// Configure TLS based on environment variables
	var opts []grpc.ServerOption
	if os.Getenv("GRPC_USE_TLS") == "true" {
		certFile := os.Getenv("GRPC_TLS_CERT_PATH")
		keyFile := os.Getenv("GRPC_TLS_KEY_PATH")

		if certFile == "" || keyFile == "" {
			return nil, fmt.Errorf("GRPC_TLS_CERT_PATH and GRPC_TLS_KEY_PATH must be set when GRPC_USE_TLS=true")
		}

		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load TLS certificates: %w", err)
		}

		creds := credentials.NewTLS(&tls.Config{
			Certificates: []tls.Certificate{cert},
		})
		opts = append(opts, grpc.Creds(creds))

		logger.WithFields(logging.Fields{
			"cert_file": certFile,
			"key_file":  keyFile,
		}).Info("Starting gRPC server with TLS")
	} else {
		logger.Info("Starting gRPC server with insecure connection")
	}

	srv := grpc.NewServer(opts...)
	pb.RegisterHelmsmanControlServer(srv, &Server{})
	go func() {
		if err := srv.Serve(lis); err != nil {
			logger.WithError(err).Error("Control gRPC server exited")
		}
	}()
	return srv, nil
}

// processNodeUpdate converts gRPC NodeUpdate to validation.FoghornNodeUpdate and forwards to node service
func processNodeUpdate(update *pb.NodeLifecycleUpdate, logger logging.Logger) {
	// Update stream stats per stream
	for streamName, streamData := range update.GetStreams() {
		state.DefaultManager().UpdateNodeStats(streamName, update.GetNodeId(), int(streamData.GetTotal()), int(streamData.GetInputs()), int64(streamData.GetBytesUp()), int64(streamData.GetBytesDown()))
	}

	// Apply full node lifecycle with write-through
	_ = state.DefaultManager().ApplyNodeLifecycle(streamCtx(), update)

	// Track node outputs for DTSC URI resolution and update unified node state
	if outputsJSON := update.GetOutputsJson(); outputsJSON != "" {
		go updateNodeOutputs(update.GetNodeId(), update.GetBaseUrl(), outputsJSON, logger)
	}

	// Trigger sidecar to perform immediate JSON metrics poll & upload
	go func() {
		registry.mu.RLock()
		c := registry.conns[update.GetNodeId()]
		registry.mu.RUnlock()
		if c == nil {
			return
		}
		seed := &pb.MistTrigger{
			TriggerType: "seed_poll",
			NodeId:      update.GetNodeId(),
			Timestamp:   time.Now().Unix(),
			Blocking:    false,
			RequestId:   fmt.Sprintf("seed-%d", time.Now().UnixNano()),
		}
		msg := &pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_MistTrigger{MistTrigger: seed}}
		_ = c.stream.Send(msg)
	}()
}

// Helpers

var ErrNotConnected = status.Error(codes.Unavailable, "node not connected")

// handleClipProgress processes clip progress updates from Helmsman nodes
func handleClipProgress(progress *pb.ClipProgress, nodeID string, logger logging.Logger) {
	requestID := progress.GetRequestId()
	percent := progress.GetPercent()
	message := progress.GetMessage()

	logger.WithFields(logging.Fields{
		"request_id": requestID,
		"percent":    percent,
		"message":    message,
	}).Debug("Clip progress update")

	_ = state.DefaultManager().ApplyClipProgress(streamCtx(), requestID, percent, message, nodeID)
}

// handleClipDone processes clip completion notifications from Helmsman nodes
func handleClipDone(done *pb.ClipDone, nodeID string, logger logging.Logger) {
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

	_ = state.DefaultManager().ApplyClipDone(streamCtx(), requestID, status, filePath, sizeBytes, errorMsg, nodeID)
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

	// Tag ingest node stream instance with DVR requested
	state.DefaultManager().UpdateStreamInstanceInfo(req.GetInternalName(), nodeID, map[string]interface{}{
		"dvr_status": "requested",
		"dvr_hash":   dvrHash,
	})

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

	// Construct source DTSC URL from ingest node outputs

	sourceDTSCURL := BuildDTSCURI(nodeID, req.GetInternalName(), true, logger)
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

	// Tag storage node stream instance with start info
	state.DefaultManager().UpdateStreamInstanceInfo(req.GetInternalName(), storageNodeID, map[string]interface{}{
		"dvr_status":     "starting",
		"dvr_hash":       dvrHash,
		"dvr_source_uri": sourceDTSCURL,
	})
}

// processDVRProgress handles DVR progress updates from storage Helmsman
func processDVRProgress(progress *pb.DVRProgress, storageNodeID string, logger logging.Logger) {
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

	_ = state.DefaultManager().ApplyDVRProgress(streamCtx(), dvrHash, status, uint64(sizeBytes), uint32(segmentCount), storageNodeID)
}

// processDVRStopped handles DVR completion from storage Helmsman
func processDVRStopped(stopped *pb.DVRStopped, storageNodeID string, logger logging.Logger) {
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

	finalStatus := "failed"
	if status == "success" {
		finalStatus = "completed"
	}
	_ = state.DefaultManager().ApplyDVRStopped(streamCtx(), dvrHash, finalStatus, int64(durationSeconds), uint64(sizeBytes), manifestPath, errorMsg, storageNodeID)
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
	// Reflect into unified state manager
	if status == "live" {
		_ = state.DefaultManager().UpdateStreamFromBuffer(internalName, internalName, nodeID, "", bufferState, "")
	} else if status == "offline" {
		state.DefaultManager().SetOffline(internalName, nodeID)
	}

	// Maintain legacy map temporarily for callers still reading it
	streamHealthMutex.Lock()
	// Get DTSC output URI from node configuration
	sourceBaseURL := getDTSCOutputURI(nodeID, logger)
	streamHealthMap[internalName] = &StreamHealthStatus{
		InternalName:  internalName,
		IsHealthy:     isHealthy,
		BufferState:   bufferState,
		Status:        status,
		LastUpdate:    time.Now(),
		SourceNodeID:  nodeID,
		SourceBaseURL: sourceBaseURL,
	}
	streamHealthMutex.Unlock()
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
	sourceURI := BuildDTSCURI(streamHealth.SourceNodeID, internalName, true, logger)

	// Tag storage node (requesting node) instance as ready with source URI
	state.DefaultManager().UpdateStreamInstanceInfo(internalName, requestingNodeID, map[string]interface{}{
		"dvr_status":     "ready",
		"dvr_source_uri": sourceURI,
	})

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

	// also reflect in unified node state
	state.DefaultManager().SetNodeInfo(nodeID, baseURL, true, nil, nil, "", outputsJSON, outputs)

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

// GetDTSCBase returns the DTSC base URI (e.g., dtsc://HOST:PORT) for a node.
func GetDTSCBase(nodeID string, logger logging.Logger) string {
	return getDTSCOutputURI(nodeID, logger)
}

// BuildDTSCURI returns a full DTSC URI for a stream on a node.
// When live is true, it prefixes the stream name with "live+".
func BuildDTSCURI(nodeID, internalName string, live bool, logger logging.Logger) string {
	base := GetDTSCBase(nodeID, logger)
	if base == "" || internalName == "" {
		return ""
	}
	name := internalName
	if live {
		name = "live+" + internalName
	}
	base = strings.TrimSuffix(base, "/")
	return base + "/" + name
}

// GetNodeOutputs returns the outputs for a given node ID (for viewer endpoint resolution)
func GetNodeOutputs(nodeID string) (*NodeOutputs, bool) {
	ns := state.DefaultManager().GetNodeState(nodeID)
	if ns != nil && (ns.Outputs != nil || ns.OutputsRaw != "") {
		return &NodeOutputs{
			NodeID:      nodeID,
			BaseURL:     ns.BaseURL,
			OutputsJSON: ns.OutputsRaw,
			Outputs:     ns.Outputs,
			LastUpdate:  ns.LastUpdate,
		}, true
	}

	nodeOutputsMutex.RLock()
	defer nodeOutputsMutex.RUnlock()

	outputs, exists := nodeOutputsMap[nodeID]
	return outputs, exists
}

// Global handlers set by handlers package for trigger processing
var mistTriggerProcessor MistTriggerProcessor

// MistTriggerProcessor interface for handling MistServer triggers
type MistTriggerProcessor interface {
	ProcessTrigger(triggerType string, rawPayload []byte, nodeID string) (string, bool, error)
	ProcessTypedTrigger(trigger *pb.MistTrigger) (string, bool, error)
}

// SetMistTriggerProcessor sets the trigger processor (called by handlers package)
func SetMistTriggerProcessor(processor MistTriggerProcessor) {
	mistTriggerProcessor = processor
}

// processMistTrigger processes typed MistServer triggers forwarded from Helmsman
func processMistTrigger(trigger *pb.MistTrigger, nodeID string, stream pb.HelmsmanControl_ConnectServer, logger logging.Logger) {
	triggerType := trigger.GetTriggerType()
	requestID := trigger.GetRequestId()
	blocking := trigger.GetBlocking()

	logger.WithFields(logging.Fields{
		"trigger_type": triggerType,
		"request_id":   requestID,
		"node_id":      nodeID,
		"blocking":     blocking,
	}).Debug("Processing typed MistServer trigger")

	if mistTriggerProcessor == nil {
		logger.Error("MistTriggerProcessor not set, cannot process triggers")
		if blocking {
			// Send error response for blocking triggers
			response := &pb.MistTriggerResponse{
				RequestId: requestID,
				Response:  "",
				Abort:     true,
			}
			sendMistTriggerResponse(stream, response, logger)
		}
		return
	}

	// Process the typed trigger directly through the handlers package
	responseText, shouldAbort, err := mistTriggerProcessor.ProcessTypedTrigger(trigger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"trigger_type": triggerType,
			"request_id":   requestID,
			"error":        err,
		}).Error("Failed to process MistServer trigger")

		if blocking {
			// Send error response for blocking triggers
			response := &pb.MistTriggerResponse{
				RequestId: requestID,
				Response:  "",
				Abort:     true,
			}
			sendMistTriggerResponse(stream, response, logger)
		}
		return
	}

	// For non-blocking triggers, we're done
	if !blocking {
		logger.WithFields(logging.Fields{
			"trigger_type": triggerType,
			"request_id":   requestID,
		}).Debug("Successfully processed non-blocking trigger")
		return
	}

	// For blocking triggers, send the response back to Helmsman
	response := &pb.MistTriggerResponse{
		RequestId: requestID,
		Response:  responseText,
		Abort:     shouldAbort,
	}

	sendMistTriggerResponse(stream, response, logger)

	logger.WithFields(logging.Fields{
		"trigger_type": triggerType,
		"request_id":   requestID,
		"response":     responseText,
		"abort":        shouldAbort,
	}).Debug("Sent MistTrigger response")
}

// sendMistTriggerResponse sends a MistTriggerResponse back to Helmsman
func sendMistTriggerResponse(stream pb.HelmsmanControl_ConnectServer, response *pb.MistTriggerResponse, logger logging.Logger) {
	msg := &pb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &pb.ControlMessage_MistTriggerResponse{MistTriggerResponse: response},
	}

	if err := stream.Send(msg); err != nil {
		logger.WithFields(logging.Fields{
			"request_id": response.RequestId,
			"error":      err,
		}).Error("Failed to send MistTrigger response")
	}
}

// Config seed composition and sending
var geoOnce sync.Once
var geoipReader *geoip.Reader

func composeConfigSeed(nodeID string, _ []string, peerAddr string) *pb.ConfigSeed {
	var lat, lon float64
	var loc string

	geoOnce.Do(func() {
		geoipReader = geoip.GetSharedReader()
	})

	if geoipReader != nil {
		if gd := geoipReader.Lookup(peerAddr); gd != nil {
			lat = gd.Latitude
			lon = gd.Longitude
			if gd.City != "" {
				loc = gd.City
			} else if gd.CountryName != "" {
				loc = gd.CountryName
			}
		}
	}

	templates := []*pb.StreamTemplate{
		{
			Id:    "live",
			Def:   &pb.StreamDef{Name: "live+$", Realtime: true, StopSessions: false, Tags: []string{"live"}},
			Roles: []string{"ingest", "edge"},
			Caps:  []string{"ingest", "edge"},
		},
		{
			Id:    "vod",
			Def:   &pb.StreamDef{Name: "vod+$", Realtime: false, StopSessions: false, Tags: []string{"vod"}},
			Roles: []string{"edge", "storage"},
			Caps:  []string{"edge", "storage"},
		},
	}

	return &pb.ConfigSeed{
		NodeId:       nodeID,
		Latitude:     lat,
		Longitude:    lon,
		LocationName: loc,
		Templates:    templates,
	}
}

func SendConfigSeed(nodeID string, seed *pb.ConfigSeed) error {
	if seed == nil {
		return fmt.Errorf("nil ConfigSeed")
	}
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &pb.ControlMessage{
		Payload: &pb.ControlMessage_ConfigSeed{ConfigSeed: seed},
		SentAt:  timestamppb.Now(),
	}
	return c.stream.Send(msg)
}
