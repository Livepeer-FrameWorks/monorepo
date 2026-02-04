package control

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"frameworks/api_balancing/internal/ingesterrors"
	"frameworks/api_balancing/internal/state"
	"frameworks/pkg/cache"
	"frameworks/pkg/clients/commodore"
	"frameworks/pkg/clients/decklog"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/geoip"
	"frameworks/pkg/grpcutil"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
	pb "frameworks/pkg/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func streamCtx() context.Context { return context.Background() }

func categorizeEnrollmentError(err error) bool {
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	switch st.Code() {
	case codes.Unauthenticated, codes.PermissionDenied, codes.InvalidArgument:
		return true
	default:
		return false
	}
}

func sendControlError(stream pb.HelmsmanControl_ConnectServer, code, message string) error {
	return stream.Send(&pb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &pb.ControlMessage_Error{Error: &pb.ControlError{Code: code, Message: message}},
	})
}

// Registry holds active Helmsman control streams keyed by node_id
type Registry struct {
	mu    sync.RWMutex
	conns map[string]*conn
	log   logging.Logger
}

type conn struct {
	stream   pb.HelmsmanControl_ConnectServer
	last     time.Time
	peerAddr string
}

var registry *Registry
var clipHashResolver func(string) (string, string, error)
var db *sql.DB
var quartermasterClient *qmclient.GRPCClient
var livepeerGatewayURL string // Set from main.go if LIVEPEER_GATEWAY_URL is configured
var geoipCache *cache.Cache
var decklogClient *decklog.BatchedClient
var dvrStopRegistry DVRStopRegistry

type DVRStopRegistry interface {
	RegisterPendingDVRStop(internalName string)
}

// SetLivepeerGatewayURL sets the Livepeer Gateway URL for processing config
func SetLivepeerGatewayURL(url string) { livepeerGatewayURL = url }

// SetDVRStopRegistry sets the registry used for deferring DVR stop requests.
func SetDVRStopRegistry(registry DVRStopRegistry) { dvrStopRegistry = registry }

// SetDecklogClient sets the Decklog client for DVR lifecycle emissions.
func SetDecklogClient(client *decklog.BatchedClient) { decklogClient = client }

// GetStreamSource returns the source node and base URL for a given internal_name if known
func GetStreamSource(internalName string) (nodeID string, baseURL string, ok bool) {
	// Prefer a node that reports inputs and is not replicated.
	instances := state.DefaultManager().GetStreamInstances(internalName)
	var bestID string
	var bestAt time.Time
	for id, inst := range instances {
		if inst.Inputs > 0 && !inst.Replicated && inst.Status != "offline" {
			if bestID == "" || inst.LastUpdate.After(bestAt) {
				bestID = id
				bestAt = inst.LastUpdate
			}
		}
	}
	if bestID != "" {
		if ns := state.DefaultManager().GetNodeState(bestID); ns != nil {
			return bestID, ns.BaseURL, true
		}
		return bestID, "", true
	}

	// Fallback: early-start flows can see STREAM_BUFFER before node stats populate Inputs.
	// In that case, use the stream union state's NodeID.
	if st := state.DefaultManager().GetStreamState(internalName); st != nil && st.NodeID != "" {
		if ns := state.DefaultManager().GetNodeState(st.NodeID); ns != nil {
			return st.NodeID, ns.BaseURL, true
		}
		return st.NodeID, "", true
	}

	return "", "", false
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
var artifactDeletedHandler func(*pb.ArtifactDeleted)
var dvrDeletedHandler func(dvrHash string, sizeBytes uint64, nodeID string)
var dvrStoppedHandler func(dvrHash string, finalStatus string, nodeID string, sizeBytes uint64, manifestPath string, errorMsg string)

// SetClipHandlers registers callbacks for analytics emission
func SetClipHandlers(onProgress func(*pb.ClipProgress), onDone func(*pb.ClipDone), onDeleted func(*pb.ArtifactDeleted)) {
	clipProgressHandler = onProgress
	clipDoneHandler = onDone
	artifactDeletedHandler = onDeleted
}

// SetDVRDeletedHandler registers callback for DVR deletion analytics
func SetDVRDeletedHandler(handler func(dvrHash string, sizeBytes uint64, nodeID string)) {
	dvrDeletedHandler = handler
}

// SetDVRStoppedHandler registers callback for DVR stopped analytics
func SetDVRStoppedHandler(handler func(dvrHash string, finalStatus string, nodeID string, sizeBytes uint64, manifestPath string, errorMsg string)) {
	dvrStoppedHandler = handler
}

// Init initializes the global registry
func Init(logger logging.Logger, cClient *commodore.GRPCClient, processor MistTriggerProcessor) {
	registry = &Registry{conns: make(map[string]*conn), log: logger}
	CommodoreClient = cClient
	mistTriggerProcessor = processor
}

// SetDB sets the database connection for clip operations
func SetDB(database *sql.DB) {
	db = database
}

// SetClipHashResolver sets the resolver for clip hash lookups
func SetClipHashResolver(resolver func(string) (string, string, error)) {
	clipHashResolver = resolver
}

// SetQuartermasterClient sets the Quartermaster client for edge enrollment and lookups
func SetQuartermasterClient(c *qmclient.GRPCClient) { quartermasterClient = c }

// SetGeoIPCache sets the GeoIP cache for cached lookup usage.
func SetGeoIPCache(c *cache.Cache) { geoipCache = c }

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
			var peerAddr string
			if p, _ := peer.FromContext(stream.Context()); p != nil {
				peerAddr = p.Addr.String()
			}
			registry.mu.Lock()
			registry.conns[nodeID] = &conn{stream: stream, last: time.Now(), peerAddr: peerAddr}
			registry.mu.Unlock()
			registry.log.WithField("node_id", nodeID).Info("Helmsman registered")
			// Mark node healthy in unified state (baseURL unknown at register)
			state.DefaultManager().SetNodeInfo(nodeID, "", true, nil, nil, "", "", nil)

			cleanup := func() {
				registry.mu.Lock()
				delete(registry.conns, nodeID)
				registry.mu.Unlock()
				state.DefaultManager().MarkNodeDisconnected(nodeID)
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

				// Register node IP with state manager for same-host avoidance logic.
				// Also store the resolved TenantID if this is a dedicated node.
				// Tags are not currently provided during initial registration.
				state.DefaultManager().SetNodeConnectionInfo(nodeID, host, tenantID, nil)

				var country, city string
				var lat, lon float64
				geoOnce.Do(func() { geoipReader = geoip.GetSharedReader() })
				if geoipReader != nil {
					if gd := geoip.LookupCached(stream.Context(), geoipReader, geoipCache, host); gd != nil {
						country = gd.CountryCode
						city = gd.City
						lat = gd.Latitude
						lon = gd.Longitude
					}
				}
				fpReq := &pb.ResolveNodeFingerprintRequest{PeerIp: host, GeoCountry: country, GeoCity: city, GeoLatitude: lat, GeoLongitude: lon}
				if x.Register != nil && x.Register.Fingerprint != nil {
					fp := x.Register.Fingerprint
					fpReq.LocalIpv4 = append(fpReq.LocalIpv4, fp.GetLocalIpv4()...)
					fpReq.LocalIpv6 = append(fpReq.LocalIpv6, fp.GetLocalIpv6()...)
					if fp.GetMacsSha256() != "" {
						s := fp.GetMacsSha256()
						fpReq.MacsSha256 = &s
					}
					if fp.GetMachineIdSha256() != "" {
						s := fp.GetMachineIdSha256()
						fpReq.MachineIdSha256 = &s
					}
				}
				if quartermasterClient != nil {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					if resp, err := quartermasterClient.ResolveNodeFingerprint(ctx, fpReq); err == nil && resp != nil {
						tenantID = resp.TenantId
						if resp.CanonicalNodeId != "" {
							canonicalNodeID = resp.CanonicalNodeId
						}
						registry.log.WithFields(logging.Fields{"node_id": canonicalNodeID, "tenant_id": tenantID}).Info("Resolved tenant via fingerprint")
					}
					cancel()
				}
			}

			fingerprintResolved := tenantID != ""
			tok := strings.TrimSpace(x.Register.GetEnrollmentToken())

			if !fingerprintResolved && tok == "" {
				registry.log.WithField("node_id", nodeID).Error("New edge node missing enrollment token")
				_ = sendControlError(stream, "ENROLLMENT_REQUIRED", "new edge nodes must provide an enrollment token")
				cleanup()
				return nil
			}

			if fingerprintResolved {
				if tok != "" {
					registry.log.WithField("node_id", nodeID).Debug("Ignoring enrollment token for already-registered node")
				}
			} else if tok != "" {
				if quartermasterClient == nil {
					registry.log.WithField("node_id", nodeID).Error("Quartermaster client unavailable for enrollment")
					_ = sendControlError(stream, "ENROLLMENT_UNAVAILABLE", "enrollment service temporarily unavailable")
					cleanup()
					return nil
				}
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
				req := &pb.BootstrapEdgeNodeRequest{Token: tok, Ips: []string{host}}
				// Include client-provided fingerprint to bind mapping at enrollment
				if x.Register != nil && x.Register.Fingerprint != nil {
					fp := x.Register.Fingerprint
					if v := fp.GetLocalIpv4(); len(v) > 0 {
						req.LocalIpv4 = append(req.LocalIpv4, v...)
					}
					if v := fp.GetLocalIpv6(); len(v) > 0 {
						req.LocalIpv6 = append(req.LocalIpv6, v...)
					}
					if fp.GetMacsSha256() != "" {
						s := fp.GetMacsSha256()
						req.MacsSha256 = &s
					}
					if fp.GetMachineIdSha256() != "" {
						s := fp.GetMachineIdSha256()
						req.MachineIdSha256 = &s
					}
				}
				resp, err := quartermasterClient.BootstrapEdgeNode(ctx, req)
				if err != nil {
					if categorizeEnrollmentError(err) {
						registry.log.WithError(err).WithField("node_id", nodeID).Error("Edge enrollment failed: invalid token")
						_ = sendControlError(stream, "ENROLLMENT_FAILED", "enrollment token invalid or expired")
					} else {
						registry.log.WithError(err).WithField("node_id", nodeID).Error("Edge enrollment unavailable")
						_ = sendControlError(stream, "ENROLLMENT_UNAVAILABLE", "enrollment service temporarily unavailable")
					}
					cleanup()
					return nil
				}
				if resp == nil {
					registry.log.WithField("node_id", nodeID).Error("Edge enrollment returned empty response")
					_ = sendControlError(stream, "ENROLLMENT_UNAVAILABLE", "enrollment service temporarily unavailable")
					cleanup()
					return nil
				}
				if resp.NodeId != "" {
					canonicalNodeID = resp.NodeId
				}
				tenantID = resp.TenantId
				registry.log.WithFields(logging.Fields{"node_id": canonicalNodeID, "tenant_id": tenantID}).Info("Edge node enrolled via Quartermaster")
			}

			// Determine operational mode: DB-persisted wins over Helmsman's request
			operationalMode := resolveOperationalMode(canonicalNodeID, x.Register.GetRequestedMode())
			seed := composeConfigSeed(canonicalNodeID, x.Register.GetRoles(), peerAddr, operationalMode)
			if tenantID != "" {
				seed.TenantId = tenantID
			}
			_ = SendConfigSeed(nodeID, seed)

			// Forward hardware specs to Quartermaster if present
			if quartermasterClient != nil && (x.Register.CpuCores != nil || x.Register.MemoryGb != nil || x.Register.DiskGb != nil) {
				go func(reg *pb.Register, nid string) {
					hwCtx, hwCancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer hwCancel()
					err := quartermasterClient.UpdateNodeHardware(hwCtx, &pb.UpdateNodeHardwareRequest{
						NodeId:   nid,
						CpuCores: reg.CpuCores,
						MemoryGb: reg.MemoryGb,
						DiskGb:   reg.DiskGb,
					})
					if err != nil {
						registry.log.WithFields(logging.Fields{
							"node_id": nid,
							"error":   err,
						}).Warn("Failed to update node hardware specs in Quartermaster")
					} else {
						registry.log.WithFields(logging.Fields{
							"node_id":   nid,
							"cpu_cores": reg.GetCpuCores(),
							"memory_gb": reg.GetMemoryGb(),
							"disk_gb":   reg.GetDiskGb(),
						}).Info("Updated node hardware specs in Quartermaster")
					}
				}(x.Register, canonicalNodeID)
			}
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
		case *pb.ControlMessage_ArtifactDeleted:
			if artifactDeletedHandler != nil {
				go artifactDeletedHandler(x.ArtifactDeleted)
			}
			go handleArtifactDeleted(x.ArtifactDeleted, nodeID, registry.log)
		case *pb.ControlMessage_Heartbeat:
			if nodeID != "" {
				registry.mu.Lock()
				if c := registry.conns[nodeID]; c != nil {
					c.last = time.Now()
				}
				registry.mu.Unlock()
				// Refresh node health/last update
				state.DefaultManager().TouchNode(nodeID, true)
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
			incMistTrigger(x.MistTrigger.GetTriggerType(), x.MistTrigger.GetBlocking(), "received")
			go processMistTrigger(x.MistTrigger, nodeID, stream, registry.log)
		case *pb.ControlMessage_FreezePermissionRequest:
			// Handle freeze permission request from Helmsman (cold storage)
			go processFreezePermissionRequest(x.FreezePermissionRequest, nodeID, stream, registry.log)
		case *pb.ControlMessage_FreezeProgress:
			// Handle freeze progress updates from Helmsman
			go processFreezeProgress(x.FreezeProgress, nodeID, registry.log)
		case *pb.ControlMessage_FreezeComplete:
			// Handle freeze completion from Helmsman
			go processFreezeComplete(x.FreezeComplete, nodeID, registry.log)
		case *pb.ControlMessage_DefrostProgress:
			// Handle defrost progress updates from Helmsman
			go processDefrostProgress(x.DefrostProgress, nodeID, registry.log)
		case *pb.ControlMessage_DefrostComplete:
			// Handle defrost completion from Helmsman
			go processDefrostComplete(x.DefrostComplete, nodeID, registry.log)
		case *pb.ControlMessage_CanDeleteRequest:
			// Handle can-delete check from Helmsman (dual-storage architecture)
			go processCanDeleteRequest(x.CanDeleteRequest, nodeID, stream, registry.log)
		case *pb.ControlMessage_SyncComplete:
			// Handle sync completion from Helmsman (dual-storage architecture)
			go processSyncComplete(x.SyncComplete, nodeID, registry.log)
		}
	}
	if nodeID != "" {
		registry.mu.Lock()
		delete(registry.conns, nodeID)
		registry.mu.Unlock()
		state.DefaultManager().MarkNodeDisconnected(nodeID)
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

// SendClipDelete sends a ClipDeleteRequest to the given node to delete clip files
func SendClipDelete(nodeID string, req *pb.ClipDeleteRequest) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &pb.ControlMessage{
		Payload: &pb.ControlMessage_ClipDelete{ClipDelete: req},
		SentAt:  timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

// SendDVRDelete sends a DVRDeleteRequest to the given node to delete DVR recording files
func SendDVRDelete(nodeID string, req *pb.DVRDeleteRequest) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &pb.ControlMessage{
		Payload: &pb.ControlMessage_DvrDelete{DvrDelete: req},
		SentAt:  timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

// SendVodDelete sends a VodDeleteRequest to the given node to delete VOD asset files
func SendVodDelete(nodeID string, req *pb.VodDeleteRequest) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &pb.ControlMessage{
		Payload: &pb.ControlMessage_VodDelete{VodDelete: req},
		SentAt:  timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

// StopDVRByInternalName finds an active DVR for a stream and sends a stop to its storage node
func StopDVRByInternalName(internalName string, logger logging.Logger) {
	if db == nil || internalName == "" {
		return
	}
	// Query foghorn.artifacts for active DVR, join with artifact_nodes for node_id
	var dvrHash, storageNodeID string
	err := db.QueryRow(`
        SELECT a.artifact_hash, COALESCE(an.node_id,'')
        FROM foghorn.artifacts a
        LEFT JOIN foghorn.artifact_nodes an ON a.artifact_hash = an.artifact_hash
        WHERE a.internal_name = $1 AND a.artifact_type = 'dvr'
              AND a.status IN ('requested','starting','recording')
        ORDER BY a.created_at DESC
        LIMIT 1`, internalName).Scan(&dvrHash, &storageNodeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) && dvrStopRegistry != nil {
			dvrStopRegistry.RegisterPendingDVRStop(internalName)
		}
		return
	}
	if storageNodeID == "" || dvrHash == "" {
		if dvrHash == "" && dvrStopRegistry != nil {
			dvrStopRegistry.RegisterPendingDVRStop(internalName)
		}
		return
	}
	if err := SendDVRStop(storageNodeID, &pb.DVRStopRequest{DvrHash: dvrHash, RequestId: dvrHash}); err != nil {
		logger.WithError(err).WithFields(logging.Fields{
			"dvr_hash": dvrHash,
			"node_id":  storageNodeID,
		}).Warn("Failed to send DVR stop command")
		return
	}
	_, _ = db.Exec(`UPDATE foghorn.artifacts SET status = 'stopping', updated_at = NOW() WHERE artifact_hash = $1`, dvrHash)
}

func emitIngestDVRFailure(dvrHash, streamID, errorMsg string, req *pb.DVRStartRequest, logger logging.Logger) {
	if decklogClient == nil {
		return
	}

	dvrData := &pb.DVRLifecycleData{
		Status:  pb.DVRLifecycleData_STATUS_FAILED,
		DvrHash: dvrHash,
		Error:   &errorMsg,
	}
	if internalName := req.GetInternalName(); internalName != "" {
		dvrData.InternalName = &internalName
	}
	if tenantID := req.GetTenantId(); tenantID != "" {
		dvrData.TenantId = &tenantID
	}
	if userID := req.GetUserId(); userID != "" {
		dvrData.UserId = &userID
	}
	if streamID != "" {
		dvrData.StreamId = &streamID
	}

	go func() {
		if err := decklogClient.SendDVRLifecycle(dvrData); err != nil {
			logger.WithError(err).WithField("dvr_hash", dvrHash).Warn("Failed to emit DVR start failure event")
		}
	}()
}

// ServiceRegistrar is a function that registers additional gRPC services
type ServiceRegistrar func(srv *grpc.Server)

// GRPCServerConfig contains configuration for starting the control gRPC server
type GRPCServerConfig struct {
	Addr         string
	Logger       logging.Logger
	ServiceToken string
	Registrars   []ServiceRegistrar
}

// StartGRPCServer starts the control gRPC server on the given addr (e.g., ":18009")
// Additional services can be registered via Registrars in the config.
func StartGRPCServer(cfg GRPCServerConfig) (*grpc.Server, error) {
	lis, err := net.Listen("tcp", cfg.Addr)
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

		cfg.Logger.WithFields(logging.Fields{
			"cert_file": certFile,
			"key_file":  keyFile,
		}).Info("Starting gRPC server with TLS")
	} else {
		cfg.Logger.Info("Starting gRPC server with insecure connection")
	}

	unaryInterceptors := []grpc.UnaryServerInterceptor{
		grpcutil.SanitizeUnaryServerInterceptor(),
	}

	// Add auth interceptor if SERVICE_TOKEN is configured
	if cfg.ServiceToken != "" {
		authInterceptor := middleware.GRPCAuthInterceptor(middleware.GRPCAuthConfig{
			ServiceToken: cfg.ServiceToken,
			Logger:       cfg.Logger,
			SkipMethods: []string{
				"/grpc.health.v1.Health/Check",
				"/grpc.health.v1.Health/Watch",
				// HelmsmanControl uses bootstrap token validated in-method
				"/proto.HelmsmanControl/Connect",
			},
		})
		unaryInterceptors = append([]grpc.UnaryServerInterceptor{authInterceptor}, unaryInterceptors...)
	}

	opts = append(opts, grpc.ChainUnaryInterceptor(unaryInterceptors...))
	srv := grpc.NewServer(opts...)
	pb.RegisterHelmsmanControlServer(srv, &Server{})

	// gRPC health service for control plane
	hs := health.NewServer()
	hs.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	hs.SetServingStatus(pb.HelmsmanControl_ServiceDesc.ServiceName, grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(srv, hs)

	// Register additional services
	for _, reg := range cfg.Registrars {
		reg(srv)
	}

	go func() {
		if err := srv.Serve(lis); err != nil {
			cfg.Logger.WithError(err).Error("Control gRPC server exited")
		}
	}()
	return srv, nil
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
	}).Info("Clip progress update")

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

// handleArtifactDeleted processes artifact deletion notifications from Helmsman nodes
func handleArtifactDeleted(deleted *pb.ArtifactDeleted, nodeID string, logger logging.Logger) {
	clipHash := deleted.GetClipHash()
	reason := deleted.GetReason()

	logger.WithFields(logging.Fields{
		"clip_hash": clipHash,
		"reason":    reason,
		"node_id":   nodeID,
	}).Info("Artifact deleted on node")

	// Update state manager
	_ = state.DefaultManager().ApplyArtifactDeleted(streamCtx(), clipHash, nodeID)
}

// processDVRStartRequest handles DVR start requests from ingest Helmsman
func processDVRStartRequest(req *pb.DVRStartRequest, nodeID string, logger logging.Logger) {
	// Get DVR hash and stream_id from Commodore registration
	dvrHash := req.GetDvrHash()
	streamID := req.GetStreamId()
	if dvrHash == "" || streamID == "" {
		if CommodoreClient == nil {
			logger.Error("Commodore not available for DVR registration")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		regReq := &pb.RegisterDVRRequest{
			TenantId:     req.GetTenantId(),
			UserId:       req.GetUserId(),
			InternalName: req.GetInternalName(),
		}
		// Pass retention from DVR config if available
		if cfg := req.GetConfig(); cfg != nil && cfg.RetentionDays > 0 {
			retentionTime := time.Now().AddDate(0, 0, int(cfg.RetentionDays))
			regReq.RetentionUntil = timestamppb.New(retentionTime)
		}
		resp, err := CommodoreClient.RegisterDVR(ctx, regReq)
		if err != nil {
			logger.WithError(err).Error("Failed to register DVR with Commodore")
			return
		}
		dvrHash = resp.DvrHash
		streamID = resp.GetStreamId()
	}

	logger.WithFields(logging.Fields{
		"dvr_hash":      dvrHash,
		"internal_name": req.GetInternalName(),
		"node_id":       nodeID,
	}).Info("Processing DVR start request")

	// Tag ingest node stream instance with DVR requested
	state.DefaultManager().UpdateStreamInstanceInfo(req.GetInternalName(), nodeID, map[string]interface{}{
		"dvr_status": "requested",
		"dvr_hash":   dvrHash,
	})

	// Store artifact lifecycle state in foghorn.artifacts with context for Decklog events
	_, err := db.Exec(`
		INSERT INTO foghorn.artifacts (
			artifact_hash, artifact_type, internal_name, stream_id, tenant_id, user_id, status, request_id, format, created_at, updated_at
		) VALUES ($1, 'dvr', $2, NULLIF($3,'')::uuid, NULLIF($4,'')::uuid, NULLIF($5,'')::uuid, 'requested', $6, 'm3u8', NOW(), NOW())
		ON CONFLICT (artifact_hash) DO UPDATE SET
			status = 'requested',
			stream_id = COALESCE(foghorn.artifacts.stream_id, EXCLUDED.stream_id),
			tenant_id = COALESCE(foghorn.artifacts.tenant_id, EXCLUDED.tenant_id),
			user_id = COALESCE(foghorn.artifacts.user_id, EXCLUDED.user_id),
			format = COALESCE(foghorn.artifacts.format, EXCLUDED.format),
			updated_at = NOW()`,
		dvrHash, req.GetInternalName(), streamID, req.GetTenantId(), req.GetUserId(), dvrHash)

	if err != nil {
		logger.WithFields(logging.Fields{
			"dvr_hash": dvrHash,
			"error":    err,
		}).Error("Failed to store DVR artifact")
		return
	}

	// Find available storage node with DVR capabilities
	storageNodeID, storageNodeURL, err := findStorageNodeForDVR(req.GetTenantId(), logger)
	if err != nil {
		logger.WithFields(logging.Fields{
			"dvr_hash": dvrHash,
			"error":    err,
		}).Error("Failed to find storage node for DVR")

		// Update artifact as failed
		if _, dbErr := db.Exec(`UPDATE foghorn.artifacts SET status = 'failed', error_message = $1, updated_at = NOW() WHERE artifact_hash = $2`,
			err.Error(), dvrHash); dbErr != nil {
			logger.WithError(dbErr).Warn("Failed to update artifact status to failed")
		}
		emitIngestDVRFailure(dvrHash, streamID, err.Error(), req, logger)
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
		StreamId:      streamID,
	}

	// Store node assignment in foghorn.artifact_nodes
	_, err = db.Exec(`
		INSERT INTO foghorn.artifact_nodes (artifact_hash, node_id, base_url, cached_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (artifact_hash, node_id) DO UPDATE SET
			base_url = $3,
			last_seen_at = NOW()`,
		dvrHash, storageNodeID, storageNodeURL)

	if err != nil {
		logger.WithFields(logging.Fields{
			"dvr_hash": dvrHash,
			"error":    err,
		}).Error("Failed to store DVR node assignment")
	}

	// Forward enhanced request to storage node
	if err := SendDVRStart(storageNodeID, enhancedReq); err != nil {
		logger.WithFields(logging.Fields{
			"dvr_hash":        dvrHash,
			"storage_node_id": storageNodeID,
			"error":           err,
		}).Error("Failed to send DVR start to storage node")

		// Update artifact as failed
		if _, dbErr := db.Exec(`UPDATE foghorn.artifacts SET status = 'failed', error_message = $1, updated_at = NOW() WHERE artifact_hash = $2`,
			err.Error(), dvrHash); dbErr != nil {
			logger.WithError(dbErr).Warn("Failed to update artifact status to failed")
		}
		emitIngestDVRFailure(dvrHash, streamID, err.Error(), req, logger)
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
	}).Info("DVR progress update")

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

	// Map Helmsman status to DB status
	var finalStatus string
	switch status {
	case "success":
		finalStatus = "completed"
	case "stopped":
		finalStatus = "stopped"
	case "deleted":
		finalStatus = "deleted"
	default:
		finalStatus = "failed"
	}
	_ = state.DefaultManager().ApplyDVRStopped(streamCtx(), dvrHash, finalStatus, int64(durationSeconds), uint64(sizeBytes), manifestPath, errorMsg, storageNodeID)

	// Emit analytics for deletion (after Helmsman confirmation)
	if finalStatus == "deleted" && dvrDeletedHandler != nil {
		go dvrDeletedHandler(dvrHash, uint64(sizeBytes), storageNodeID)
	}
	if finalStatus != "deleted" && dvrStoppedHandler != nil {
		go dvrStoppedHandler(dvrHash, finalStatus, storageNodeID, uint64(sizeBytes), manifestPath, errorMsg)
	}
}

// findStorageNodeForDVR finds an available storage node with DVR capabilities for the given tenant
func findStorageNodeForDVR(tenantID string, logger logging.Logger) (string, string, error) {
	if loadBalancerInstance == nil {
		return "", "", fmt.Errorf("load balancer not available")
	}

	nodes := loadBalancerInstance.GetNodes()

	// Find nodes with storage capabilities
	var bestNode *balancerNode
	var bestScore uint64

	for baseURL, node := range nodes {
		// Skip non-storage nodes
		if !node.CapStorage {
			continue
		}

		// Skip inactive nodes
		if !node.IsHealthy {
			continue
		}

		// Calculate a simple score based on available resources
		// Higher score is better (more available resources)
		storageScore := uint64(0)

		// Factor in available storage space
		capacityBytes := node.StorageCapacityBytes
		usedBytes := node.StorageUsedBytes

		// Use real-time disk usage if available
		if node.DiskTotalBytes > 0 {
			capacityBytes = node.DiskTotalBytes
			usedBytes = node.DiskUsedBytes
		}

		if capacityBytes > usedBytes {
			availableStorage := capacityBytes - usedBytes
			storageScore += availableStorage / (1024 * 1024 * 1024) // Convert to GB for scoring
		}

		// Factor in CPU availability (lower CPU usage = higher score)
		cpu := uint64(node.CPU)
		if cpu < 800 { // Less than 80% CPU usage (assuming tenths)
			storageScore += (1000 - cpu) / 10 // 0-20 point bonus
		}

		// Factor in RAM availability
		ramMax := uint64(node.RAMMax)
		ramCurrent := uint64(node.RAMCurrent)
		if ramMax > ramCurrent {
			availableRAM := ramMax - ramCurrent
			storageScore += availableRAM / 1024 // Convert MB to GB-ish for scoring
		}

		if storageScore > bestScore {
			bestScore = storageScore
			bestNode = &balancerNode{
				BaseURL: baseURL,
				NodeID:  node.NodeID,
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
	}).Info("Selected storage node for DVR")

	return bestNode.NodeID, bestNode.BaseURL, nil
}

// balancerNode is a helper struct for node selection
type balancerNode struct {
	BaseURL string
	NodeID  string
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
	GetNodes() map[string]state.NodeState
	GetNodeByID(nodeID string) (string, error)
	GetNodeIDByHost(host string) string
}

// SetLoadBalancer allows handlers package to inject the load balancer instance
func SetLoadBalancer(lb LoadBalancerInterface) {
	loadBalancerInstance = lb
}

// processDVRReadyRequest handles DVR readiness checks from storage Helmsman
func processDVRReadyRequest(req *pb.DVRReadyRequest, requestingNodeID string, stream pb.HelmsmanControl_ConnectServer, logger logging.Logger) {
	dvrHash := req.GetDvrHash()

	logger.WithFields(logging.Fields{
		"dvr_hash":           dvrHash,
		"requesting_node_id": requestingNodeID,
	}).Info("Processing DVR readiness request")

	// Look up the DVR artifact in database to get stream info
	var internalName string
	err := db.QueryRow(`
		SELECT internal_name
		FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND artifact_type = 'dvr'`,
		dvrHash).Scan(&internalName)

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
	streamState := state.DefaultManager().GetStreamState(internalName)

	if streamState == nil {
		logger.WithFields(logging.Fields{
			"dvr_hash":      dvrHash,
			"internal_name": internalName,
		}).Info("Stream health not tracked yet")

		response := &pb.DVRReadyResponse{
			DvrHash: dvrHash,
			Ready:   false,
			Reason:  "stream_not_tracked",
		}
		sendDVRReadyResponse(stream, response, logger)
		return
	}

	// Check if stream is ready for DVR (healthy and buffer full/recovering)
	isReady := !streamState.HasIssues &&
		(streamState.BufferState == "FULL" || streamState.BufferState == "RECOVER") &&
		streamState.Status == "live"

	if !isReady {
		var reason string
		if streamState.HasIssues {
			reason = "stream_unhealthy"
		} else if streamState.Status != "live" {
			reason = "stream_offline"
		} else {
			reason = "stream_booting"
		}

		logger.WithFields(logging.Fields{
			"dvr_hash":      dvrHash,
			"internal_name": internalName,
			"has_issues":    streamState.HasIssues,
			"buffer_state":  streamState.BufferState,
			"status":        streamState.Status,
			"reason":        reason,
		}).Info("Stream not ready for DVR")

		response := &pb.DVRReadyResponse{
			DvrHash: dvrHash,
			Ready:   false,
			Reason:  reason,
		}
		sendDVRReadyResponse(stream, response, logger)
		return
	}

	// Stream is ready! Build source URI and potentially mutate config
	sourceNodeID, _, ok := GetStreamSource(internalName)
	if !ok {
		logger.WithFields(logging.Fields{
			"dvr_hash":      dvrHash,
			"internal_name": internalName,
		}).Warn("Stream ready but no source node available for DVR")
		response := &pb.DVRReadyResponse{
			DvrHash: dvrHash,
			Ready:   false,
			Reason:  "stream_source_missing",
		}
		sendDVRReadyResponse(stream, response, logger)
		return
	}
	sourceURI := BuildDTSCURI(sourceNodeID, internalName, true, logger)

	// Tag storage node (requesting node) instance as ready with source URI
	state.DefaultManager().UpdateStreamInstanceInfo(internalName, requestingNodeID, map[string]interface{}{
		"dvr_status":     "ready",
		"dvr_source_uri": sourceURI,
	})

	// Default DVR config - recording_config would come from Commodore stream settings
	// TODO: Fetch config from Commodore.GetStreamConfig if needed
	config := &pb.DVRConfig{
		Enabled:         true,
		RetentionDays:   30,
		Format:          "ts",
		SegmentDuration: 6,
	}

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

	// Update artifact status to indicate storage node is starting recording
	_, err = db.Exec(`
		UPDATE foghorn.artifacts
		SET status = 'starting', started_at = NOW(), updated_at = NOW()
		WHERE artifact_hash = $1`,
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

// getDTSCOutputURI constructs the DTSC output URI for a given node using MistServer outputs configuration
func getDTSCOutputURI(nodeID string, logger logging.Logger) string {
	// Get node state from unified state manager
	nodeState := state.DefaultManager().GetNodeState(nodeID)
	if nodeState == nil {
		logger.WithField("node_id", nodeID).Info("Node state not found")
		return ""
	}

	if nodeState.Outputs == nil {
		logger.WithField("node_id", nodeID).Info("No outputs found in node state")
		return ""
	}

	// Look for DTSC output in the outputs map
	dtscOutput, exists := nodeState.Outputs["DTSC"]
	if !exists {
		logger.WithField("node_id", nodeID).Info("No DTSC output found in node outputs")
		return ""
	}

	// DTSC output format is typically "dtsc://HOST/$"
	dtscTemplate, ok := dtscOutput.(string)
	if !ok {
		logger.WithFields(logging.Fields{
			"node_id":     nodeID,
			"dtsc_output": dtscOutput,
		}).Info("DTSC output is not a string")
		return ""
	}

	// Replace HOST with the actual node hostname
	// Extract hostname from base URL (e.g., "https://mist-seattle.stronk.rocks" -> "mist-seattle.stronk.rocks")
	hostname := nodeState.BaseURL
	hostname = strings.TrimPrefix(hostname, "https://")
	hostname = strings.TrimPrefix(hostname, "http://")

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
	}).Info("Constructed DTSC base URI")

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
	return nil, false
}

// Global handlers set by handlers package for trigger processing
var mistTriggerProcessor MistTriggerProcessor

// MistTriggerProcessor interface for handling MistServer triggers
type MistTriggerProcessor interface {
	ProcessTrigger(triggerType string, rawPayload []byte, nodeID string) (string, bool, error)
	ProcessTypedTrigger(trigger *pb.MistTrigger) (string, bool, error)
}

// processMistTrigger processes typed MistServer triggers forwarded from Helmsman
func processMistTrigger(trigger *pb.MistTrigger, nodeID string, stream pb.HelmsmanControl_ConnectServer, logger logging.Logger) {
	triggerType := trigger.GetTriggerType()
	requestID := trigger.GetRequestId()
	blocking := trigger.GetBlocking()

	logger.WithFields(logging.Fields{
		"trigger_type":   triggerType,
		"request_id":     requestID,
		"node_id":        nodeID,
		"blocking":       blocking,
		"payload_type":   fmt.Sprintf("%T", trigger.GetTriggerPayload()),
		"payload_is_nil": trigger.GetTriggerPayload() == nil,
	}).Info("Processing typed MistServer trigger - TRACE")

	if mistTriggerProcessor == nil {
		incMistTrigger(triggerType, blocking, "processor_missing")
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
		incMistTrigger(triggerType, blocking, "processed_error")
		logger.WithFields(logging.Fields{
			"trigger_type": triggerType,
			"request_id":   requestID,
			"error":        err,
		}).Error("Failed to process MistServer trigger")

		if blocking {
			errorCode := pb.IngestErrorCode_INGEST_ERROR_INTERNAL
			var ingestErr *ingesterrors.IngestError
			if errors.As(err, &ingestErr) {
				errorCode = ingestErr.Code
			}
			// Send error response for blocking triggers
			response := &pb.MistTriggerResponse{
				RequestId: requestID,
				Response:  "",
				Abort:     true,
				ErrorCode: errorCode,
			}
			sendMistTriggerResponse(stream, response, logger)
		}
		return
	}

	if shouldAbort {
		incMistTrigger(triggerType, blocking, "processed_abort")
	} else {
		incMistTrigger(triggerType, blocking, "processed_ok")
	}

	// For non-blocking triggers, we're done
	if !blocking {
		logger.WithFields(logging.Fields{
			"trigger_type": triggerType,
			"request_id":   requestID,
		}).Info("Successfully processed non-blocking trigger")
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
	}).Info("Sent MistTrigger response")
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

// resolveOperationalMode determines the authoritative mode for a node.
// Priority: DB-persisted mode > Helmsman's requested mode > default (NORMAL).
func resolveOperationalMode(nodeID string, requestedMode pb.NodeOperationalMode) pb.NodeOperationalMode {
	// Check if we have a persisted mode in state (loaded from DB on startup or set by admin)
	persistedMode := state.DefaultManager().GetNodeOperationalMode(nodeID)
	if persistedMode != "" && persistedMode != state.NodeModeNormal {
		// Non-normal mode is persisted (admin set it), use that
		switch persistedMode {
		case state.NodeModeDraining:
			return pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_DRAINING
		case state.NodeModeMaintenance:
			return pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_MAINTENANCE
		}
	}

	// No persisted override, honor Helmsman's request if valid
	if requestedMode != pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_UNSPECIFIED {
		return requestedMode
	}

	return pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL
}

// Config seed composition and sending
var geoOnce sync.Once
var geoipReader *geoip.Reader

func composeConfigSeed(nodeID string, _ []string, peerAddr string, operationalMode pb.NodeOperationalMode) *pb.ConfigSeed {
	var lat, lon float64
	var loc string

	geoOnce.Do(func() {
		geoipReader = geoip.GetSharedReader()
	})

	if geoipReader != nil {
		if gd := geoip.LookupCached(context.Background(), geoipReader, geoipCache, peerAddr); gd != nil {
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

	// Processing configuration with Livepeer Gateway availability and codec support matrix
	processingConfig := &pb.ProcessingConfig{
		LivepeerGatewayAvailable: livepeerGatewayURL != "",
		LivepeerGatewayUrl:       livepeerGatewayURL,
		GatewayInputCodecs:       []string{"H264"},                       // Livepeer only supports H.264
		LocalInputCodecs:         []string{"H264", "H265", "AV1", "VP9"}, // MistServer supports these locally
	}

	return &pb.ConfigSeed{
		NodeId:          nodeID,
		Latitude:        lat,
		Longitude:       lon,
		LocationName:    loc,
		Templates:       templates,
		Processing:      processingConfig,
		OperationalMode: operationalMode,
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

// PushOperationalMode sends a ConfigSeed with the specified operational mode to the node.
// Used when admin sets mode via API to notify the connected Helmsman.
func PushOperationalMode(nodeID string, mode pb.NodeOperationalMode) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}

	// Helmsan sidecar does NOT merge ConfigSeeds; ApplySeed overwrites lastSeed.
	// Send a full seed to avoid wiping previously seeded fields.
	seed := composeConfigSeed(nodeID, nil, c.peerAddr, mode)
	msg := &pb.ControlMessage{
		Payload: &pb.ControlMessage_ConfigSeed{ConfigSeed: seed},
		SentAt:  timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

// ==================== Cold Storage (Freeze/Defrost) Handlers ====================

// S3ClientInterface defines the interface for S3 operations (for dependency injection)
type S3ClientInterface interface {
	GeneratePresignedPUT(key string, expiry time.Duration) (string, error)
	GeneratePresignedGET(key string, expiry time.Duration) (string, error)
	ListPrefix(ctx context.Context, prefix string) ([]string, error)
	BuildClipS3Key(tenantID, streamName, clipHash, format string) string
	BuildDVRS3Key(tenantID, internalName, dvrHash string) string
	BuildVodS3Key(tenantID, artifactHash, filename string) string
	BuildS3URL(key string) string
}

var s3Client S3ClientInterface

// SetS3Client sets the S3 client for cold storage operations
func SetS3Client(client S3ClientInterface) {
	s3Client = client
}

// processFreezePermissionRequest handles freeze permission requests from Helmsman
// Generates presigned URLs for secure S3 uploads without exposing credentials
func processFreezePermissionRequest(req *pb.FreezePermissionRequest, nodeID string, stream pb.HelmsmanControl_ConnectServer, logger logging.Logger) {
	requestID := req.GetRequestId()
	assetType := req.GetAssetType()
	assetHash := req.GetAssetHash()
	localPath := req.GetLocalPath()
	sizeBytes := req.GetSizeBytes()

	logger.WithFields(logging.Fields{
		"request_id": requestID,
		"asset_type": assetType,
		"asset_hash": assetHash,
		"size_bytes": sizeBytes,
		"node_id":    nodeID,
	}).Info("Processing freeze permission request")

	// Check if S3 client is configured
	if s3Client == nil {
		logger.Warn("S3 client not configured, rejecting freeze request")
		sendFreezePermissionResponse(stream, &pb.FreezePermissionResponse{
			RequestId: requestID,
			AssetHash: assetHash,
			Approved:  false,
			Reason:    "s3_not_configured",
		}, logger)
		return
	}

	// Look up asset info from foghorn.artifacts for internal_name
	var streamName string
	err := db.QueryRow(`
		SELECT internal_name
		FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND artifact_type = $2`,
		assetHash, assetType).Scan(&streamName)
	if err != nil {
		logger.WithFields(logging.Fields{
			"asset_hash": assetHash,
			"asset_type": assetType,
			"error":      err,
		}).Error("Asset not found in database")
		sendFreezePermissionResponse(stream, &pb.FreezePermissionResponse{
			RequestId: requestID,
			AssetHash: assetHash,
			Approved:  false,
			Reason:    "asset_not_found",
		}, logger)
		return
	}

	// Get tenant_id from Commodore (business registry owner)
	var tenantID string
	// For DVR segment/manifest incremental sync, assetHash is "{dvr_hash}/{filename}"
	dvrHash := assetHash
	if assetType == "dvr_segment" || assetType == "dvr_manifest" {
		if idx := strings.Index(assetHash, "/"); idx != -1 {
			dvrHash = assetHash[:idx]
		}
	}
	if CommodoreClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if assetType == "clip" {
			if resp, err := CommodoreClient.ResolveClipHash(ctx, assetHash); err == nil && resp.Found {
				tenantID = resp.TenantId
			}
		} else if assetType == "dvr" || assetType == "dvr_segment" || assetType == "dvr_manifest" {
			if resp, err := CommodoreClient.ResolveDVRHash(ctx, dvrHash); err == nil && resp.Found {
				tenantID = resp.TenantId
			}
		} else if assetType == "vod" {
			if resp, err := CommodoreClient.ResolveVodHash(ctx, assetHash); err == nil && resp.Found {
				tenantID = resp.TenantId
			}
		}
	}
	if tenantID == "" {
		logger.WithFields(logging.Fields{
			"asset_hash": assetHash,
			"asset_type": assetType,
		}).Error("Could not resolve tenant for asset")
		sendFreezePermissionResponse(stream, &pb.FreezePermissionResponse{
			RequestId: requestID,
			AssetHash: assetHash,
			Approved:  false,
			Reason:    "tenant_not_found",
		}, logger)
		return
	}

	// Generate presigned URLs
	expiry := 30 * time.Minute
	expirySeconds := int64(expiry.Seconds())

	response := &pb.FreezePermissionResponse{
		RequestId:        requestID,
		AssetHash:        assetHash,
		Approved:         true,
		UrlExpirySeconds: expirySeconds,
	}

	if assetType == "clip" {
		// Single file - extract format from path
		format := "mp4"
		if idx := strings.LastIndex(localPath, "."); idx != -1 {
			format = localPath[idx+1:]
		}
		s3Key := s3Client.BuildClipS3Key(tenantID, streamName, assetHash, format)
		presignedURL, err := s3Client.GeneratePresignedPUT(s3Key, expiry)
		if err != nil {
			logger.WithError(err).Error("Failed to generate presigned PUT URL for clip")
			sendFreezePermissionResponse(stream, &pb.FreezePermissionResponse{
				RequestId: requestID,
				AssetHash: assetHash,
				Approved:  false,
				Reason:    "presign_failed",
			}, logger)
			return
		}
		response.PresignedPutUrl = presignedURL
	} else if assetType == "dvr" {
		// DVR directory - need presigned URLs for all segments
		s3Prefix := s3Client.BuildDVRS3Key(tenantID, streamName, assetHash)
		response.SegmentUrls = make(map[string]string)

		// Iterate over filenames provided by Helmsman
		for _, filename := range req.GetFilenames() {
			// Construct full S3 key for this file (relative to DVR prefix)
			// filename is relative, e.g., "segments/0_0.ts" or "hash.m3u8"
			s3Key := s3Prefix + "/" + filename

			url, err := s3Client.GeneratePresignedPUT(s3Key, expiry)
			if err != nil {
				logger.WithError(err).WithField("filename", filename).Error("Failed to generate presigned URL")
				continue
			}
			response.SegmentUrls[filename] = url
		}

		logger.WithFields(logging.Fields{
			"asset_hash": assetHash,
			"s3_prefix":  s3Prefix,
			"file_count": len(response.SegmentUrls),
		}).Info("Generated presigned URLs for DVR freeze")
	} else if assetType == "dvr_segment" || assetType == "dvr_manifest" {
		// Incremental DVR sync - single segment or manifest file
		// assetHash is "{dvr_hash}/{filename}", extract filename
		filename := ""
		if idx := strings.Index(assetHash, "/"); idx != -1 {
			filename = assetHash[idx+1:]
		}
		if filename == "" && len(req.GetFilenames()) > 0 {
			filename = req.GetFilenames()[0]
		}
		s3Prefix := s3Client.BuildDVRS3Key(tenantID, streamName, dvrHash)
		s3Key := s3Prefix + "/" + filename

		presignedURL, err := s3Client.GeneratePresignedPUT(s3Key, expiry)
		if err != nil {
			logger.WithError(err).Error("Failed to generate presigned PUT URL for DVR segment")
			sendFreezePermissionResponse(stream, &pb.FreezePermissionResponse{
				RequestId: requestID,
				AssetHash: assetHash,
				Approved:  false,
				Reason:    "presign_failed",
			}, logger)
			return
		}
		response.PresignedPutUrl = presignedURL
		response.SegmentUrls = map[string]string{filename: presignedURL}

		logger.WithFields(logging.Fields{
			"asset_hash": assetHash,
			"dvr_hash":   dvrHash,
			"filename":   filename,
			"s3_key":     s3Key,
		}).Info("Generated presigned URL for DVR incremental sync")
	} else if assetType == "vod" {
		// VOD single file - extract format from path
		format := "mp4"
		if idx := strings.LastIndex(localPath, "."); idx != -1 {
			format = localPath[idx+1:]
		}
		// VOD uses artifact_hash as filename base, with tenant context
		s3Key := s3Client.BuildVodS3Key(tenantID, assetHash, fmt.Sprintf("%s.%s", assetHash, format))
		presignedURL, err := s3Client.GeneratePresignedPUT(s3Key, expiry)
		if err != nil {
			logger.WithError(err).Error("Failed to generate presigned PUT URL for VOD")
			sendFreezePermissionResponse(stream, &pb.FreezePermissionResponse{
				RequestId: requestID,
				AssetHash: assetHash,
				Approved:  false,
				Reason:    "presign_failed",
			}, logger)
			return
		}
		response.PresignedPutUrl = presignedURL

		logger.WithFields(logging.Fields{
			"asset_hash": assetHash,
			"s3_key":     s3Key,
		}).Info("Generated presigned URL for VOD freeze")
	}

	// Update artifact to mark as freezing (skip for incremental segment sync)
	if assetType != "dvr_segment" && assetType != "dvr_manifest" {
		_, _ = db.Exec(`UPDATE foghorn.artifacts SET storage_location = 'freezing', updated_at = NOW() WHERE artifact_hash = $1`, assetHash)
	}

	sendFreezePermissionResponse(stream, response, logger)

	logger.WithFields(logging.Fields{
		"request_id": requestID,
		"asset_hash": assetHash,
		"asset_type": assetType,
	}).Info("Freeze permission granted with presigned URLs")
}

// sendFreezePermissionResponse sends a FreezePermissionResponse back to Helmsman
func sendFreezePermissionResponse(stream pb.HelmsmanControl_ConnectServer, response *pb.FreezePermissionResponse, logger logging.Logger) {
	msg := &pb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &pb.ControlMessage_FreezePermissionResponse{FreezePermissionResponse: response},
	}

	if err := stream.Send(msg); err != nil {
		logger.WithFields(logging.Fields{
			"request_id": response.RequestId,
			"error":      err,
		}).Error("Failed to send freeze permission response")
	}
}

// processFreezeProgress handles freeze progress updates from Helmsman
func processFreezeProgress(progress *pb.FreezeProgress, nodeID string, logger logging.Logger) {
	logger.WithFields(logging.Fields{
		"request_id":     progress.GetRequestId(),
		"asset_hash":     progress.GetAssetHash(),
		"percent":        progress.GetPercent(),
		"bytes_uploaded": progress.GetBytesUploaded(),
		"node_id":        nodeID,
	}).Info("Freeze progress update")
}

// processFreezeComplete handles freeze completion from Helmsman
func processFreezeComplete(complete *pb.FreezeComplete, nodeID string, logger logging.Logger) {
	requestID := complete.GetRequestId()
	assetHash := complete.GetAssetHash()
	status := complete.GetStatus()
	s3URL := complete.GetS3Url()
	sizeBytes := complete.GetSizeBytes()
	errorMsg := complete.GetError()

	logger.WithFields(logging.Fields{
		"request_id": requestID,
		"asset_hash": assetHash,
		"status":     status,
		"s3_url":     s3URL,
		"size_bytes": sizeBytes,
		"error":      errorMsg,
		"node_id":    nodeID,
	}).Info("Freeze operation completed")

	if status == "success" {
		// Update artifact storage location in database
		_, _ = db.Exec(`
				UPDATE foghorn.artifacts
				SET storage_location = 'local',
				    sync_status = 'synced',
				    s3_url = NULLIF($1, ''),
				    frozen_at = NOW(),
			    last_sync_attempt = NOW(),
			    sync_error = NULL,
			    updated_at = NOW()
			WHERE artifact_hash = $2`,
			s3URL, assetHash)
	} else {
		// Revert storage location on failure
		_, _ = db.Exec(`
			UPDATE foghorn.artifacts
			SET storage_location = 'local',
			    sync_status = 'failed',
			    sync_error = NULLIF($1,''),
			    last_sync_attempt = NOW(),
			    updated_at = NOW()
			WHERE artifact_hash = $2
		`, errorMsg, assetHash)
	}
}

// processDefrostProgress handles defrost progress updates from Helmsman
func processDefrostProgress(progress *pb.DefrostProgress, nodeID string, logger logging.Logger) {
	logger.WithFields(logging.Fields{
		"request_id":          progress.GetRequestId(),
		"asset_hash":          progress.GetAssetHash(),
		"percent":             progress.GetPercent(),
		"bytes_downloaded":    progress.GetBytesDownloaded(),
		"segments_downloaded": progress.GetSegmentsDownloaded(),
		"total_segments":      progress.GetTotalSegments(),
		"message":             progress.GetMessage(),
		"node_id":             nodeID,
	}).Info("Defrost progress update")
}

// processDefrostComplete handles defrost completion from Helmsman
func processDefrostComplete(complete *pb.DefrostComplete, nodeID string, logger logging.Logger) {
	requestID := complete.GetRequestId()
	assetHash := complete.GetAssetHash()
	status := complete.GetStatus()
	localPath := complete.GetLocalPath()
	sizeBytes := complete.GetSizeBytes()
	errorMsg := complete.GetError()

	logger.WithFields(logging.Fields{
		"request_id": requestID,
		"asset_hash": assetHash,
		"status":     status,
		"local_path": localPath,
		"size_bytes": sizeBytes,
		"error":      errorMsg,
		"node_id":    nodeID,
	}).Info("Defrost operation completed")

	if status == "success" {
		// Update storage location back to local in database
		reportingNodeID := complete.GetNodeId()
		if reportingNodeID == "" {
			reportingNodeID = nodeID
		}
		result, err := db.Exec(`
			UPDATE foghorn.artifacts
			SET storage_location = 'local',
			    defrost_node_id = NULL,
			    defrost_started_at = NULL,
			    updated_at = NOW()
			WHERE artifact_hash = $1
			  AND storage_location = 'defrosting'
			  AND (defrost_node_id = $2 OR defrost_node_id IS NULL)
		`, assetHash, reportingNodeID)
		if err != nil {
			logger.WithError(err).WithFields(logging.Fields{
				"asset_hash": assetHash,
				"node_id":    reportingNodeID,
			}).Warn("Failed to update storage location after defrost")
		}
		updatedRows := int64(0)
		if result != nil {
			if rows, err := result.RowsAffected(); err == nil {
				updatedRows = rows
			}
		}
		if updatedRows == 0 {
			logger.WithFields(logging.Fields{
				"asset_hash": assetHash,
				"node_id":    reportingNodeID,
			}).Warn("Defrost completion skipped; state already updated")
		}

		// Record that this node now has a warm/local cached copy.
		// This is intentionally independent from sync_status (S3 remains authoritative once synced).
		if updatedRows > 0 && artifactRepo != nil && reportingNodeID != "" {
			if err := artifactRepo.AddCachedNodeWithPath(context.Background(), assetHash, reportingNodeID, localPath, int64(sizeBytes)); err != nil {
				logger.WithError(err).WithFields(logging.Fields{
					"asset_hash": assetHash,
					"node_id":    reportingNodeID,
				}).Warn("Failed to add cached node after defrost")
			}

			state.DefaultManager().AddNodeArtifact(reportingNodeID, &pb.StoredArtifact{
				ClipHash:  assetHash,
				FilePath:  localPath,
				SizeBytes: sizeBytes,
				CreatedAt: time.Now().Unix(),
				Format:    strings.TrimPrefix(filepath.Ext(localPath), "."),
			})
		}
	} else {
		// Revert storage_location on failure so future defrosts can retry
		reportingNodeID := complete.GetNodeId()
		if reportingNodeID == "" {
			reportingNodeID = nodeID
		}
		_, _ = db.Exec(`
			UPDATE foghorn.artifacts
			SET storage_location = 's3',
			    defrost_node_id = NULL,
			    defrost_started_at = NULL,
			    updated_at = NOW()
			WHERE artifact_hash = $1
			  AND storage_location = 'defrosting'
			  AND (defrost_node_id = $2 OR defrost_node_id IS NULL)
		`, assetHash, reportingNodeID)
		logger.WithFields(logging.Fields{
			"asset_hash": assetHash,
			"error":      errorMsg,
		}).Warn("Defrost failed, reverted to s3")
	}

	// Notify any waiting defrost requests
	notifyDefrostComplete(assetHash, status == "success", localPath)
}

// SendDefrostRequest sends a DefrostRequest to the given node with presigned URLs
func SendDefrostRequest(nodeID string, req *pb.DefrostRequest) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &pb.ControlMessage{
		Payload: &pb.ControlMessage_DefrostRequest{DefrostRequest: req},
		SentAt:  timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

// SendDtshSyncRequest sends a DtshSyncRequest to the given node to upload just the .dtsh file
func SendDtshSyncRequest(nodeID string, req *pb.DtshSyncRequest) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &pb.ControlMessage{
		Payload: &pb.ControlMessage_DtshSyncRequest{DtshSyncRequest: req},
		SentAt:  timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

// SendStopSessions sends a StopSessionsRequest to the given node to terminate stream sessions
// Used when a tenant is suspended due to insufficient balance
func SendStopSessions(nodeID string, req *pb.StopSessionsRequest) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &pb.ControlMessage{
		Payload: &pb.ControlMessage_StopSessionsRequest{StopSessionsRequest: req},
		SentAt:  timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

// TriggerDtshSync is called when .dtsh appeared after the main asset was already synced
// It generates presigned URLs and sends DtshSyncRequest to the node
func TriggerDtshSync(nodeID, assetHash, assetType, filePath string) {
	if s3Client == nil || db == nil {
		return
	}

	logger := registry.log.WithFields(logging.Fields{
		"node_id":    nodeID,
		"asset_hash": assetHash,
		"asset_type": assetType,
	})

	// Look up stream info from foghorn.artifacts
	var streamName string
	err := db.QueryRow(`
		SELECT internal_name
		FROM foghorn.artifacts
		WHERE artifact_hash = $1`,
		assetHash).Scan(&streamName)
	if err != nil {
		logger.WithError(err).Error("Failed to lookup asset for dtsh sync")
		return
	}

	// Get tenant_id from Commodore (business registry owner)
	var tenantID string
	if CommodoreClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if assetType == "clip" {
			if resp, err := CommodoreClient.ResolveClipHash(ctx, assetHash); err == nil && resp.Found {
				tenantID = resp.TenantId
			}
		} else if assetType == "dvr" {
			if resp, err := CommodoreClient.ResolveDVRHash(ctx, assetHash); err == nil && resp.Found {
				tenantID = resp.TenantId
			}
		}
	}
	if tenantID == "" {
		logger.Error("Could not resolve tenant for dtsh sync")
		return
	}

	expiry := 30 * time.Minute
	expirySeconds := int64(expiry.Seconds())
	requestID := fmt.Sprintf("dtsh-%s-%d", assetHash, time.Now().UnixNano())

	req := &pb.DtshSyncRequest{
		RequestId:        requestID,
		AssetType:        assetType,
		AssetHash:        assetHash,
		LocalPath:        filePath,
		UrlExpirySeconds: expirySeconds,
	}

	if assetType == "clip" {
		// For clips: single .dtsh file next to the main file
		format := "mp4"
		if idx := strings.LastIndex(filePath, "."); idx != -1 {
			format = filePath[idx+1:]
		}
		s3Key := s3Client.BuildClipS3Key(tenantID, streamName, assetHash, format) + ".dtsh"
		presignedURL, err := s3Client.GeneratePresignedPUT(s3Key, expiry)
		if err != nil {
			logger.WithError(err).Error("Failed to generate presigned URL for clip .dtsh")
			return
		}
		req.PresignedPutUrl = presignedURL
	} else if assetType == "dvr" {
		// For DVR: may have multiple .dtsh files in the directory
		// We'll provide a map of presigned URLs for common .dtsh file patterns
		s3Prefix := s3Client.BuildDVRS3Key(tenantID, streamName, assetHash)
		req.DtshUrls = make(map[string]string)

		// Generate presigned URLs for common .dtsh file patterns
		// The main one is assetHash.m3u8.dtsh
		dtshNames := []string{
			assetHash + ".m3u8.dtsh",
			assetHash + ".dtsh",
		}
		for _, dtshName := range dtshNames {
			s3Key := s3Prefix + "/" + dtshName
			url, err := s3Client.GeneratePresignedPUT(s3Key, expiry)
			if err != nil {
				logger.WithError(err).WithField("dtsh_name", dtshName).Warn("Failed to generate presigned URL for DVR .dtsh")
				continue
			}
			req.DtshUrls[dtshName] = url
		}

		if len(req.DtshUrls) == 0 {
			logger.Error("Failed to generate any presigned URLs for DVR .dtsh files")
			return
		}
	}

	if err := SendDtshSyncRequest(nodeID, req); err != nil {
		logger.WithError(err).Error("Failed to send DtshSyncRequest")
		return
	}

	logger.Info("Sent DtshSyncRequest for incremental .dtsh sync")
}

// DefrostWaiter tracks waiters for defrost completion
type DefrostWaiter struct {
	done chan struct{}
	ok   bool
	path string
}

var (
	defrostWaiters   = make(map[string][]*DefrostWaiter)
	defrostWaitersMu sync.Mutex
)

// WaitForDefrost waits for a defrost operation to complete
func WaitForDefrost(assetHash string, timeout time.Duration) (string, bool) {
	waiter := &DefrostWaiter{done: make(chan struct{})}

	defrostWaitersMu.Lock()
	defrostWaiters[assetHash] = append(defrostWaiters[assetHash], waiter)
	defrostWaitersMu.Unlock()

	select {
	case <-waiter.done:
		return waiter.path, waiter.ok
	case <-time.After(timeout):
		// Remove waiter on timeout
		defrostWaitersMu.Lock()
		waiters := defrostWaiters[assetHash]
		for i, w := range waiters {
			if w == waiter {
				defrostWaiters[assetHash] = append(waiters[:i], waiters[i+1:]...)
				break
			}
		}
		defrostWaitersMu.Unlock()
		return "", false
	}
}

// notifyDefrostComplete notifies all waiters that defrost is complete
func notifyDefrostComplete(assetHash string, ok bool, path string) {
	defrostWaitersMu.Lock()
	waiters := defrostWaiters[assetHash]
	delete(defrostWaiters, assetHash)
	defrostWaitersMu.Unlock()

	for _, w := range waiters {
		w.ok = ok
		w.path = path
		close(w.done)
	}
}

// Default storage base path when node has no StorageLocal configured.
// Matches HELMSMAN_STORAGE_LOCAL_PATH default for consistent path reconstruction.
var defaultStorageBase = "/var/lib/mistserver/recordings"

// SetDefaultStorageBase overrides the default storage base path (FOGHORN_DEFAULT_STORAGE_BASE).
func SetDefaultStorageBase(path string) {
	if path != "" {
		defaultStorageBase = path
	}
}

func storageBasePathForNode(nodeID string) string {
	if nodeID != "" {
		if ns := state.DefaultManager().GetNodeState(nodeID); ns != nil && ns.StorageLocal != "" {
			return ns.StorageLocal
		}
	}
	return defaultStorageBase
}

// StartDefrost initiates a defrost operation but does not wait for completion.
// Returns local path if a defrost was started, or empty if already local.
func StartDefrost(ctx context.Context, assetType, assetHash, nodeID string, timeout time.Duration, logger logging.Logger) (string, error) {
	return requestDefrost(ctx, assetType, assetHash, nodeID, timeout, logger, false)
}

func requestDefrost(ctx context.Context, assetType, assetHash, nodeID string, timeout time.Duration, logger logging.Logger, wait bool) (string, error) {
	if s3Client == nil {
		return "", fmt.Errorf("s3 client not configured")
	}
	if db == nil {
		return "", fmt.Errorf("database not available")
	}

	artifactType := assetType

	// Look up asset info from foghorn.artifacts
	var streamName, storageLocation, format, tenantID string
	var s3Key, filename, streamID sql.NullString
	err := db.QueryRow(`
		SELECT a.internal_name,
		       COALESCE(a.storage_location, 'local'),
		       COALESCE(a.format, ''),
		       COALESCE(a.tenant_id::text, ''),
		       COALESCE(v.s3_key, ''),
		       COALESCE(v.filename, ''),
		       a.stream_id::text
		FROM foghorn.artifacts a
		LEFT JOIN foghorn.vod_metadata v ON a.artifact_hash = v.artifact_hash
		WHERE a.artifact_hash = $1 AND a.artifact_type = $2`,
		assetHash, artifactType).Scan(&streamName, &storageLocation, &format, &tenantID, &s3Key, &filename, &streamID)
	if err != nil {
		return "", fmt.Errorf("asset not found: %w", err)
	}

	// Prefer denormalized tenant_id stored in foghorn.artifacts; fall back to Commodore when absent.
	if tenantID == "" {
		if CommodoreClient != nil {
			if artifactType == "clip" {
				if resp, errResolve := CommodoreClient.ResolveClipHash(ctx, assetHash); errResolve == nil && resp.Found {
					tenantID = resp.TenantId
				}
			} else if artifactType == "dvr" {
				if resp, errResolve := CommodoreClient.ResolveDVRHash(ctx, assetHash); errResolve == nil && resp.Found {
					tenantID = resp.TenantId
				}
			} else if artifactType == "vod" {
				if resp, errResolve := CommodoreClient.ResolveVodHash(ctx, assetHash); errResolve == nil && resp.Found {
					tenantID = resp.TenantId
				}
			}
		}
		if tenantID == "" {
			return "", fmt.Errorf("could not resolve tenant for asset")
		}
	}

	// Check if already local
	if storageLocation == "local" {
		return "", nil // Already local, no defrost needed
	}

	// Check if already defrosting
	if storageLocation == "defrosting" {
		if wait {
			// Wait for existing defrost to complete
			path, ok := WaitForDefrost(assetHash, timeout)
			if !ok {
				return "", fmt.Errorf("defrost timeout")
			}
			return path, nil
		}
		return "", NewDefrostingError(10, "defrost already in progress")
	}

	result, err := db.Exec(`
		UPDATE foghorn.artifacts
		SET storage_location = 'defrosting',
		    defrost_node_id = $2,
		    defrost_started_at = NOW(),
		    tenant_id = COALESCE(tenant_id, $3::uuid),
		    updated_at = NOW()
		WHERE artifact_hash = $1
		  AND artifact_type = $4
		  AND storage_location = 's3'
		  AND (tenant_id::text = $3 OR tenant_id IS NULL)
	`, assetHash, nodeID, tenantID, artifactType)
	if err != nil {
		return "", fmt.Errorf("failed to mark defrosting: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("failed to read defrost update status: %w", err)
	}
	if affected == 0 {
		var currentLocation string
		err := db.QueryRow(`
			SELECT COALESCE(storage_location, '')
			FROM foghorn.artifacts
			WHERE artifact_hash = $1
			  AND artifact_type = $2
			  AND (tenant_id::text = $3 OR tenant_id IS NULL)
		`, assetHash, artifactType, tenantID).Scan(&currentLocation)
		if err != nil {
			return "", fmt.Errorf("asset not found: %w", err)
		}
		currentLocation = strings.ToLower(strings.TrimSpace(currentLocation))
		if currentLocation == "local" {
			return "", nil
		}
		if currentLocation == "defrosting" {
			if wait {
				path, ok := WaitForDefrost(assetHash, timeout)
				if !ok {
					return "", fmt.Errorf("defrost timeout")
				}
				return path, nil
			}
			return "", NewDefrostingError(10, "defrost already in progress")
		}
		return "", fmt.Errorf("asset not in defrostable state: %s", currentLocation)
	}

	// Generate presigned GET URLs
	expiry := 30 * time.Minute
	requestID := fmt.Sprintf("defrost-%s-%d", assetHash, time.Now().UnixNano())

	req := &pb.DefrostRequest{
		RequestId:        requestID,
		AssetType:        assetType,
		AssetHash:        assetHash,
		TenantId:         tenantID,
		InternalName:     streamName,
		TimeoutSeconds:   int32(timeout.Seconds()),
		UrlExpirySeconds: int64(expiry.Seconds()),
	}

	storageBase := storageBasePathForNode(nodeID)

	if artifactType == "clip" {
		// Single file defrost
		clipFormat := format
		if clipFormat == "" {
			clipFormat = "mp4"
		}
		s3Key := s3Client.BuildClipS3Key(tenantID, streamName, assetHash, clipFormat)
		presignedURL, err := s3Client.GeneratePresignedGET(s3Key, expiry)
		if err != nil {
			return "", fmt.Errorf("failed to generate presigned GET URL: %w", err)
		}
		req.PresignedGetUrl = presignedURL
		req.LocalPath = filepath.Join(storageBase, "clips", streamName, fmt.Sprintf("%s.%s", assetHash, clipFormat))
	} else if artifactType == "dvr" {
		// DVR defrost - get segment list from S3 and generate URLs
		// S3 key uses internal_name (stored in foghorn.artifacts)
		s3Prefix := s3Client.BuildDVRS3Key(tenantID, streamName, assetHash)
		segments, err := s3Client.ListPrefix(ctx, s3Prefix)
		if err != nil {
			return "", fmt.Errorf("failed to list DVR segments: %w", err)
		}

		req.SegmentUrls = make(map[string]string)
		req.Streaming = true

		for _, segKey := range segments {
			presignedURL, err := s3Client.GeneratePresignedGET(segKey, expiry)
			if err != nil {
				logger.WithError(err).WithField("segment", segKey).Warn("Failed to generate presigned URL for segment")
				continue
			}
			// Extract segment name from key
			segName := segKey
			if idx := strings.LastIndex(segKey, "/"); idx != -1 {
				segName = segKey[idx+1:]
			}
			req.SegmentUrls[segName] = presignedURL
		}

		// Local path uses stream_id (not internal_name) to match DVR recording structure
		dvrStreamID := ""
		if CommodoreClient != nil {
			if resp, err := CommodoreClient.ResolveDVRHash(ctx, assetHash); err == nil && resp.Found {
				dvrStreamID = resp.StreamId
			}
		}
		// Fallback to cached stream_id in foghorn.artifacts when Commodore unavailable
		if dvrStreamID == "" && streamID.Valid && streamID.String != "" {
			dvrStreamID = streamID.String
		}
		if dvrStreamID == "" {
			return "", fmt.Errorf("could not resolve stream_id for DVR asset")
		}
		req.LocalPath = filepath.Join(storageBase, "dvr", dvrStreamID, assetHash)
	} else if artifactType == "vod" {
		// VOD defrost - single file
		if !s3Key.Valid || s3Key.String == "" {
			if filename.Valid && filename.String != "" {
				s3Key = sql.NullString{String: s3Client.BuildVodS3Key(tenantID, assetHash, filename.String), Valid: true}
			} else if format != "" {
				fakeName := fmt.Sprintf("%s.%s", assetHash, format)
				s3Key = sql.NullString{String: s3Client.BuildVodS3Key(tenantID, assetHash, fakeName), Valid: true}
			}
		}
		if !s3Key.Valid || s3Key.String == "" {
			return "", fmt.Errorf("missing S3 key for VOD asset")
		}
		presignedURL, err := s3Client.GeneratePresignedGET(s3Key.String, expiry)
		if err != nil {
			return "", fmt.Errorf("failed to generate presigned GET URL: %w", err)
		}
		vodFormat := format
		if vodFormat == "" {
			vodFormat = "mp4"
		}
		req.PresignedGetUrl = presignedURL
		req.LocalPath = filepath.Join(storageBase, "vod", fmt.Sprintf("%s.%s", assetHash, vodFormat))
	} else {
		return "", fmt.Errorf("unsupported asset type for defrost: %s", assetType)
	}

	// Send defrost request to node
	if err := SendDefrostRequest(nodeID, req); err != nil {
		// Revert storage location
		_, _ = db.Exec(`
			UPDATE foghorn.artifacts
			SET storage_location = 's3',
			    defrost_node_id = NULL,
			    defrost_started_at = NULL,
			    updated_at = NOW()
			WHERE artifact_hash = $1
			  AND storage_location = 'defrosting'
			  AND defrost_node_id = $2
		`, assetHash, nodeID)
		return "", fmt.Errorf("failed to send defrost request: %w", err)
	}

	if wait {
		// Wait for defrost to complete
		path, ok := WaitForDefrost(assetHash, timeout)
		if !ok {
			return "", fmt.Errorf("defrost timeout")
		}
		return path, nil
	}

	return req.LocalPath, nil
}

// ==================== Dual-Storage (Sync/CanDelete) Handlers ====================

// artifactRepo provides database access for dual-storage sync tracking
var artifactRepo state.ArtifactRepository

// SetArtifactRepository sets the artifact repository for sync tracking
func SetArtifactRepository(repo state.ArtifactRepository) {
	artifactRepo = repo
}

// processCanDeleteRequest handles can-delete checks from Helmsman
// Before deleting a local asset copy, Helmsman asks Foghorn if it's safe
func processCanDeleteRequest(req *pb.CanDeleteRequest, nodeID string, stream pb.HelmsmanControl_ConnectServer, logger logging.Logger) {
	assetHash := req.GetAssetHash()
	requestingNodeID := req.GetNodeId()
	if requestingNodeID == "" {
		requestingNodeID = nodeID
	}

	logger.WithFields(logging.Fields{
		"asset_hash": assetHash,
		"node_id":    requestingNodeID,
	}).Info("Processing can-delete request")

	response := &pb.CanDeleteResponse{
		AssetHash:    assetHash,
		SafeToDelete: false,
		Reason:       "unknown",
	}

	if artifactRepo == nil {
		logger.Warn("Artifact repository not configured, rejecting delete")
		response.Reason = "not_configured"
		sendCanDeleteResponse(stream, response, logger)
		return
	}

	// Check if artifact is synced to S3
	synced, err := artifactRepo.IsSynced(context.Background(), assetHash)
	if err != nil {
		logger.WithError(err).WithField("asset_hash", assetHash).Error("Failed to check sync status")
		response.Reason = "db_error"
		sendCanDeleteResponse(stream, response, logger)
		return
	}

	if synced {
		response.SafeToDelete = true
		response.Reason = "synced"

		// Calculate warm duration (how long asset was cached before eviction)
		cachedAt, err := artifactRepo.GetCachedAt(context.Background(), assetHash)
		if err == nil && cachedAt > 0 {
			warmDurationMs := time.Now().UnixMilli() - cachedAt
			response.WarmDurationMs = warmDurationMs
			logger.WithFields(logging.Fields{
				"asset_hash":       assetHash,
				"warm_duration_ms": warmDurationMs,
			}).Info("Asset synced to S3, safe to delete local copy")
		} else {
			logger.WithField("asset_hash", assetHash).Info("Asset synced to S3, safe to delete local copy (no cached_at)")
		}
	} else {
		// Check if sync is in progress
		info, err := artifactRepo.GetArtifactSyncInfo(context.Background(), assetHash)
		if err != nil {
			response.Reason = "db_error"
		} else if info == nil {
			response.Reason = "not_found"
		} else if info.SyncStatus == "in_progress" {
			response.Reason = "sync_pending"
		} else if info.SyncStatus == "failed" {
			response.Reason = "sync_failed"
		} else {
			response.Reason = "not_synced"
		}
		logger.WithFields(logging.Fields{
			"asset_hash": assetHash,
			"reason":     response.Reason,
		}).Info("Asset not safe to delete")
	}

	sendCanDeleteResponse(stream, response, logger)
}

// sendCanDeleteResponse sends a CanDeleteResponse back to Helmsman
func sendCanDeleteResponse(stream pb.HelmsmanControl_ConnectServer, response *pb.CanDeleteResponse, logger logging.Logger) {
	msg := &pb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &pb.ControlMessage_CanDeleteResponse{CanDeleteResponse: response},
	}

	if err := stream.Send(msg); err != nil {
		logger.WithFields(logging.Fields{
			"asset_hash": response.AssetHash,
			"error":      err,
		}).Error("Failed to send can-delete response")
	}
}

// processSyncComplete handles sync completion from Helmsman
// After Helmsman uploads an asset to S3 (without deleting local), it notifies Foghorn
func processSyncComplete(complete *pb.SyncComplete, nodeID string, logger logging.Logger) {
	requestID := complete.GetRequestId()
	assetHash := complete.GetAssetHash()
	status := complete.GetStatus()
	s3URL := complete.GetS3Url()
	sizeBytes := complete.GetSizeBytes()
	errorMsg := complete.GetError()
	reportingNodeID := complete.GetNodeId()
	if reportingNodeID == "" {
		reportingNodeID = nodeID
	}

	logger.WithFields(logging.Fields{
		"request_id": requestID,
		"asset_hash": assetHash,
		"status":     status,
		"s3_url":     s3URL,
		"size_bytes": sizeBytes,
		"error":      errorMsg,
		"node_id":    reportingNodeID,
	}).Info("Sync operation completed")

	if artifactRepo == nil {
		logger.Warn("Artifact repository not configured, cannot update sync status")
		return
	}

	ctx := context.Background()

	dtshIncluded := complete.GetDtshIncluded()

	if status == "success" {
		// If Helmsman didn't provide s3_url (typical), compute it from stored artifact metadata.
		if s3URL == "" && s3Client != nil && db != nil {
			var artifactType, internalName, format, tenantID string
			_ = db.QueryRowContext(ctx, `
				SELECT COALESCE(artifact_type,''), COALESCE(internal_name,''), COALESCE(format,''), COALESCE(tenant_id::text,'')
				FROM foghorn.artifacts
				WHERE artifact_hash = $1
			`, assetHash).Scan(&artifactType, &internalName, &format, &tenantID)
			if tenantID != "" && internalName != "" {
				switch artifactType {
				case "clip":
					if format == "" {
						format = "mp4"
					}
					s3Key := s3Client.BuildClipS3Key(tenantID, internalName, assetHash, format)
					s3URL = s3Client.BuildS3URL(s3Key)
				case "dvr":
					s3Prefix := s3Client.BuildDVRS3Key(tenantID, internalName, assetHash)
					s3URL = s3Client.BuildS3URL(s3Prefix)
				}
			}
		}

		// Update artifact registry with sync status and S3 URL
		if err := artifactRepo.SetSyncStatus(ctx, assetHash, "synced", s3URL); err != nil {
			logger.WithError(err).Error("Failed to update sync status in artifact registry")
		}

		// Add this node to cached_nodes (it has a local copy)
		if err := artifactRepo.AddCachedNode(ctx, assetHash, reportingNodeID); err != nil {
			logger.WithError(err).Error("Failed to add cached node")
		}

		// Update foghorn.artifacts with sync status
		_, _ = db.Exec(`
			UPDATE foghorn.artifacts
			SET storage_location = 'local',
			    sync_status = 'synced',
			    s3_url = COALESCE(NULLIF($1,''), s3_url),
			    dtsh_synced = $2,
			    last_sync_attempt = NOW(),
			    sync_error = NULL,
			    updated_at = NOW()
			WHERE artifact_hash = $3`,
			s3URL, dtshIncluded, assetHash)

		logger.WithFields(logging.Fields{
			"asset_hash":    assetHash,
			"s3_url":        s3URL,
			"node_id":       reportingNodeID,
			"dtsh_included": dtshIncluded,
		}).Info("Asset synced to S3, local copy retained")
	} else {
		// Sync failed
		if err := artifactRepo.SetSyncStatus(ctx, assetHash, "failed", ""); err != nil {
			logger.WithError(err).Error("Failed to update sync status to failed")
		}

		// Update error in foghorn.artifacts
		_, _ = db.Exec(`
			UPDATE foghorn.artifacts
			SET storage_location = 'local',
			    sync_status = 'failed',
			    sync_error = NULLIF($1,''),
			    last_sync_attempt = NOW(),
			    updated_at = NOW()
			WHERE artifact_hash = $2`,
			errorMsg, assetHash)

		logger.WithFields(logging.Fields{
			"asset_hash": assetHash,
			"error":      errorMsg,
		}).Warn("Asset sync to S3 failed")
	}
}
