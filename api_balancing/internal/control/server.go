package control

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"frameworks/api_balancing/internal/ingesterrors"
	"frameworks/api_balancing/internal/state"
	"frameworks/pkg/cache"
	"frameworks/pkg/clients/commodore"
	"frameworks/pkg/clients/decklog"
	navclient "frameworks/pkg/clients/navigator"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	pkgdns "frameworks/pkg/dns"
	"frameworks/pkg/geoip"
	"frameworks/pkg/grpcutil"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
	"frameworks/pkg/mist"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/version"

	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/reflection"
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

var edgeIdentityPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,99}$`)

func platformRootDomain() string {
	rootDomain := strings.TrimSpace(os.Getenv("BRAND_DOMAIN"))
	if rootDomain == "" {
		rootDomain = "frameworks.network"
	}
	return rootDomain
}

func normalizePreferredEdgeNodeID(raw string) string {
	candidate := strings.ToLower(strings.TrimSpace(raw))
	if candidate == "" {
		return ""
	}
	if idx := strings.Index(candidate, "."); idx > 0 {
		candidate = candidate[:idx]
	}
	candidate = pkgdns.SanitizeLabel(candidate)
	if !edgeIdentityPattern.MatchString(candidate) {
		return ""
	}
	return candidate
}

func edgeNodeRecordLabel(nodeID string) string {
	label := pkgdns.SanitizeLabel(nodeID)
	if strings.HasPrefix(label, "edge-") {
		return label
	}
	return "edge-" + label
}

func buildBootstrapEdgeNodeRequest(ctx context.Context, reg *pb.Register, nodeID, peerAddr, token, targetClusterID string, servedClusterIDs []string) *pb.BootstrapEdgeNodeRequest {
	host := ""
	if md, ok := metadata.FromIncomingContext(ctx); ok {
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

	req := &pb.BootstrapEdgeNodeRequest{Token: token, Hostname: nodeID, Ips: []string{host}, ServedClusterIds: servedClusterIDs}
	if strings.TrimSpace(targetClusterID) != "" {
		targetCluster := strings.TrimSpace(targetClusterID)
		req.TargetClusterId = &targetCluster
	}

	if reg != nil && reg.Fingerprint != nil {
		fp := reg.Fingerprint
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

	return req
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
	stream      pb.HelmsmanControl_ConnectServer
	last        time.Time
	peerAddr    string
	canonicalID string // node ID after fingerprint/enrollment resolution (may differ from registry key)
}

var registry *Registry
var clipHashResolver func(string) (string, string, error)
var db *sql.DB
var localClusterID string
var servedClusters atomic.Pointer[sync.Map]
var chandlerBaseMu sync.RWMutex
var resolvedChandlerBaseURL string

func init() {
	servedClusters.Store(&sync.Map{})
}

var quartermasterClient *qmclient.GRPCClient
var navigatorClient *navclient.Client
var serverCert serverCertHolder

// serverCertHolder stores the current server TLS certificate, updated atomically
// by the CertRefreshLoop when Navigator renews the cluster wildcard cert.
type serverCertHolder struct {
	cert atomic.Pointer[tls.Certificate]
}

func (h *serverCertHolder) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c := h.cert.Load()
	if c == nil {
		return nil, fmt.Errorf("no TLS certificate loaded")
	}
	return c, nil
}

// validateBootstrapTokenFn allows tests to override token validation.
// In production this calls quartermasterClient.ValidateBootstrapToken.
var validateBootstrapTokenFn func(ctx context.Context, token string) (*pb.ValidateBootstrapTokenResponse, error)
var getNodeOwnerFn func(ctx context.Context, nodeID string) (*pb.NodeOwnerResponse, error)
var getClusterFn func(ctx context.Context, clusterID string) (*pb.InfrastructureCluster, error)
var geoipCache *cache.Cache
var decklogClient *decklog.BatchedClient
var dvrStopRegistry DVRStopRegistry

type DVRStopRegistry interface {
	RegisterPendingDVRStop(internalName string)
}

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
	OutputsJSON string         // Raw outputs JSON from MistServer
	Outputs     map[string]any // Parsed outputs map
	LastUpdate  time.Time
}

// Optional analytics callbacks set by handlers package
var clipProgressHandler func(*pb.ClipProgress)
var clipDoneHandler func(*pb.ClipDone)
var artifactDeletedHandler func(context.Context, *pb.ArtifactDeleted)
var dvrDeletedHandler func(dvrHash string, sizeBytes uint64, nodeID string)
var dvrStoppedHandler func(dvrHash string, finalStatus string, nodeID string, sizeBytes uint64, manifestPath string, errorMsg string)
var artifactMapUpdatedHandler func(nodeID string)

// SetClipHandlers registers callbacks for analytics emission
func SetClipHandlers(onProgress func(*pb.ClipProgress), onDone func(*pb.ClipDone), onDeleted func(context.Context, *pb.ArtifactDeleted)) {
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

// SetOnArtifactMapUpdated registers a callback invoked when Helmsman reports a real artifact-map change.
func SetOnArtifactMapUpdated(handler func(nodeID string)) {
	artifactMapUpdatedHandler = handler
}

// NotifyArtifactMapUpdated notifies the registered callback about an artifact-map change.
func NotifyArtifactMapUpdated(nodeID string) {
	if artifactMapUpdatedHandler == nil || strings.TrimSpace(nodeID) == "" {
		return
	}
	artifactMapUpdatedHandler(nodeID)
}

// Init initializes the global registry
func Init(logger logging.Logger, cClient *commodore.GRPCClient, processor MistTriggerProcessor) {
	registry = &Registry{conns: make(map[string]*conn), log: logger}
	CommodoreClient = cClient
	mistTriggerProcessor = processor
}

// AliveNodeIDs returns IDs of nodes with a heartbeat within the given threshold.
// Used by the edge health sync to batch-report alive edges to Quartermaster.
func AliveNodeIDs(staleThreshold time.Duration) []string {
	if registry == nil {
		return nil
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	cutoff := time.Now().Add(-staleThreshold)
	ids := make([]string, 0, len(registry.conns))
	for _, c := range registry.conns {
		if c.last.After(cutoff) {
			id := c.canonicalID
			if id == "" {
				continue
			}
			ids = append(ids, id)
		}
	}
	return ids
}

// CommandRelay forwards control commands to the Foghorn instance holding a node's stream.
type CommandRelay struct {
	store         *state.RedisStateStore
	instanceID    string
	advertiseAddr string
	pool          CommandRelayPool
	logger        logging.Logger
}

// CommandRelayPool is the subset of FoghornPool needed by the relay layer.
type CommandRelayPool interface {
	GetOrCreate(key, addr string) (CommandRelayClient, error)
}

// CommandRelayClient is the subset of foghorn.GRPCClient needed by the relay layer.
type CommandRelayClient interface {
	Relay() pb.FoghornRelayClient
}

var commandRelay *CommandRelay

// InitRelay sets up the HA command relay. Pass nil to disable (single-instance mode).
// advertiseAddr is the host:port that peer instances use to reach this instance's gRPC server.
func InitRelay(store *state.RedisStateStore, instanceID, advertiseAddr string, pool CommandRelayPool, logger logging.Logger) {
	if store == nil || pool == nil {
		commandRelay = nil
		return
	}
	commandRelay = &CommandRelay{
		store:         store,
		instanceID:    instanceID,
		advertiseAddr: advertiseAddr,
		pool:          pool,
		logger:        logger,
	}
}

// GetRedisStore returns the relay's RedisStateStore (used by lifecycle hooks).
// Returns nil if relay is not configured.
func GetRedisStore() *state.RedisStateStore {
	if commandRelay == nil {
		return nil
	}
	return commandRelay.store
}

// GetInstanceID returns the relay's instance ID.
func GetInstanceID() string {
	if commandRelay == nil {
		return ""
	}
	return commandRelay.instanceID
}

// GetAdvertiseAddr returns the relay's advertise address (host:port).
func GetAdvertiseAddr() string {
	if commandRelay == nil {
		return ""
	}
	return commandRelay.advertiseAddr
}

func (r *CommandRelay) forward(ctx context.Context, req *pb.ForwardCommandRequest) error {
	if r == nil || r.store == nil {
		return fmt.Errorf("relay: not configured")
	}
	commandType := RelayCommandType(req)
	requestID := RelayRequestID(req)
	log := r.logger.WithFields(logging.Fields{
		"target_node_id":  req.GetTargetNodeId(),
		"target_instance": "",
		"command_type":    commandType,
		"request_id":      requestID,
	})
	incRelayForward(commandType, "attempt")

	owner, err := r.store.GetConnOwner(ctx, req.TargetNodeId)
	if err != nil {
		incRelayForward(commandType, "owner_lookup_error")
		return fmt.Errorf("relay: lookup owner for node %s: %w", req.TargetNodeId, err)
	}
	if owner.InstanceID == "" {
		incRelayForward(commandType, "owner_missing")
		return ErrNotConnected
	}
	log = log.WithField("target_instance", owner.InstanceID)
	if owner.InstanceID == r.instanceID {
		incRelayForward(commandType, "owner_is_self")
		return ErrNotConnected
	}
	if owner.GRPCAddr == "" {
		incRelayForward(commandType, "owner_missing_addr")
		return fmt.Errorf("relay: no address for instance %s", owner.InstanceID)
	}
	evictStale := func() {
		_, _ = r.store.DeleteConnOwnerIfMatch(ctx, req.TargetNodeId, owner.InstanceID, owner.GRPCAddr)
	}
	if r.pool == nil {
		incRelayForward(commandType, "pool_unavailable")
		return fmt.Errorf("relay: no client pool configured")
	}
	client, err := r.pool.GetOrCreate(owner.InstanceID, owner.GRPCAddr)
	if err != nil {
		evictStale()
		incRelayForward(commandType, "dial_error")
		return fmt.Errorf("relay: dial %s: %w", owner.GRPCAddr, err)
	}
	ctx = metadata.AppendToOutgoingContext(ctx,
		"x-foghorn-instance-id", r.instanceID,
		"x-relay-target-node-id", req.GetTargetNodeId(),
		"x-relay-command-type", commandType,
		"x-relay-request-id", requestID,
	)
	resp, err := client.Relay().ForwardCommand(ctx, req)
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			return fmt.Errorf("relay: peer %s does not implement ForwardCommand: %w", owner.InstanceID, err)
		}
		evictStale()
		incRelayForward(commandType, "rpc_error")
		log.WithError(err).Warn("Relay forward RPC failed")
		return fmt.Errorf("relay: forward to %s: %w", owner.InstanceID, err)
	}
	if resp == nil {
		evictStale()
		return fmt.Errorf("relay: peer %s returned nil response", owner.InstanceID)
	}
	if !resp.Delivered {
		evictStale()
		incRelayForward(commandType, "peer_rejected")
		log.WithField("peer_error", resp.Error).Warn("Relay forward rejected by peer")
		return fmt.Errorf("relay: peer %s rejected: %s", owner.InstanceID, resp.Error)
	}
	incRelayForward(commandType, "delivered")
	return nil
}

func relayFailure(localErr, relayErr error) error {
	if relayErr == nil {
		return nil
	}
	if localErr == nil {
		return relayErr
	}
	return fmt.Errorf("%w (relay failed: %w)", localErr, relayErr)
}

func RelayCommandType(req *pb.ForwardCommandRequest) string {
	switch req.GetCommand().(type) {
	case *pb.ForwardCommandRequest_ConfigSeed:
		return "config_seed"
	case *pb.ForwardCommandRequest_ClipPull:
		return "clip_pull"
	case *pb.ForwardCommandRequest_DvrStart:
		return "dvr_start"
	case *pb.ForwardCommandRequest_DvrStop:
		return "dvr_stop"
	case *pb.ForwardCommandRequest_ClipDelete:
		return "clip_delete"
	case *pb.ForwardCommandRequest_DvrDelete:
		return "dvr_delete"
	case *pb.ForwardCommandRequest_VodDelete:
		return "vod_delete"
	case *pb.ForwardCommandRequest_Defrost:
		return "defrost"
	case *pb.ForwardCommandRequest_DtshSync:
		return "dtsh_sync"
	case *pb.ForwardCommandRequest_StopSessions:
		return "stop_sessions"
	case *pb.ForwardCommandRequest_ProcessingJob:
		return "processing_job"
	case *pb.ForwardCommandRequest_Freeze:
		return "freeze"
	default:
		return "unknown"
	}
}

func RelayRequestID(req *pb.ForwardCommandRequest) string {
	switch cmd := req.GetCommand().(type) {
	case *pb.ForwardCommandRequest_ClipPull:
		return cmd.ClipPull.GetRequestId()
	case *pb.ForwardCommandRequest_DvrStart:
		return cmd.DvrStart.GetRequestId()
	case *pb.ForwardCommandRequest_DvrStop:
		return cmd.DvrStop.GetRequestId()
	case *pb.ForwardCommandRequest_ClipDelete:
		return cmd.ClipDelete.GetRequestId()
	case *pb.ForwardCommandRequest_DvrDelete:
		return cmd.DvrDelete.GetRequestId()
	case *pb.ForwardCommandRequest_VodDelete:
		return cmd.VodDelete.GetRequestId()
	case *pb.ForwardCommandRequest_Defrost:
		return cmd.Defrost.GetRequestId()
	case *pb.ForwardCommandRequest_DtshSync:
		return cmd.DtshSync.GetRequestId()
	case *pb.ForwardCommandRequest_ProcessingJob:
		return cmd.ProcessingJob.GetJobId()
	case *pb.ForwardCommandRequest_Freeze:
		return cmd.Freeze.GetRequestId()
	default:
		return ""
	}
}

// SetDB sets the database connection for clip operations
func SetDB(database *sql.DB) {
	db = database
}

// GetDB returns the package-level DB for cross-package queries.
func GetDB() *sql.DB {
	return db
}

// SetLocalClusterID sets the primary cluster ID and marks it as served.
func SetLocalClusterID(id string) {
	localClusterID = id
	servedClusters.Load().Store(id, true)
	clearResolvedChandlerBaseURL()
}

// GetLocalClusterID returns the primary cluster ID for this Foghorn instance.
func GetLocalClusterID() string {
	return localClusterID
}

// AddServedCluster registers an additional cluster served by this Foghorn.
func AddServedCluster(id string) {
	servedClusters.Load().Store(id, true)
}

func isServedCluster(id string) bool {
	if id == "" {
		return false
	}
	_, ok := servedClusters.Load().Load(id)
	return ok
}

// IsServedCluster reports whether this Foghorn instance serves cluster id.
func IsServedCluster(id string) bool {
	return isServedCluster(id)
}

// LoadServedClusters bulk-loads all active cluster assignments from the DB
// and atomically swaps the served set. localClusterID is always preserved.
func LoadServedClusters() {
	if db == nil {
		return
	}
	instanceID := strings.TrimSpace(os.Getenv("FOGHORN_INSTANCE_ID"))
	if instanceID == "" {
		return
	}

	rows, err := db.QueryContext(context.Background(), `
		SELECT fca.cluster_id
		FROM quartermaster.foghorn_cluster_assignments fca
		JOIN quartermaster.service_instances si ON si.id = fca.foghorn_instance_id
		JOIN quartermaster.services svc ON svc.service_id = si.service_id
		WHERE si.instance_id = $1
		  AND svc.type = 'foghorn'
		  AND si.status = 'running'
		  AND fca.is_active = true
	`, instanceID)
	if err != nil {
		return
	}
	defer rows.Close()

	fresh := &sync.Map{}
	if localClusterID != "" {
		fresh.Store(localClusterID, true)
	}
	for rows.Next() {
		var clusterID string
		if rows.Scan(&clusterID) == nil && clusterID != "" {
			fresh.Store(clusterID, true)
		}
	}
	servedClusters.Store(fresh)
}

// ServedClustersSnapshot returns the current set of served cluster IDs (sorted).
func ServedClustersSnapshot() []string {
	var ids []string
	servedClusters.Load().Range(func(key, _ any) bool {
		if s, ok := key.(string); ok {
			ids = append(ids, s)
		}
		return true
	})
	sort.Strings(ids)
	return ids
}

// StartServedClustersRefresh periodically reloads cluster assignments from the DB.
func StartServedClustersRefresh(ctx context.Context, interval time.Duration, log logging.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			LoadServedClusters()
			log.WithField("clusters", ServedClustersSnapshot()).Debug("Refreshed served clusters from DB")
		}
	}
}

// SetClipHashResolver sets the resolver for clip hash lookups
func SetClipHashResolver(resolver func(string) (string, string, error)) {
	clipHashResolver = resolver
}

// SetQuartermasterClient sets the Quartermaster client for edge enrollment and lookups
func SetQuartermasterClient(c *qmclient.GRPCClient) {
	quartermasterClient = c
	clearResolvedChandlerBaseURL()
}

func init() {
	getNodeOwnerFn = func(ctx context.Context, nodeID string) (*pb.NodeOwnerResponse, error) {
		if quartermasterClient == nil {
			return nil, status.Error(codes.Unavailable, "quartermaster unavailable")
		}
		return quartermasterClient.GetNodeOwner(ctx, nodeID)
	}
	getClusterFn = func(ctx context.Context, clusterID string) (*pb.InfrastructureCluster, error) {
		if quartermasterClient == nil {
			return nil, status.Error(codes.Unavailable, "quartermaster unavailable")
		}
		resp, err := quartermasterClient.GetCluster(ctx, clusterID)
		if err != nil {
			return nil, err
		}
		return resp.GetCluster(), nil
	}
}

func clearResolvedChandlerBaseURL() {
	chandlerBaseMu.Lock()
	resolvedChandlerBaseURL = ""
	chandlerBaseMu.Unlock()
}

func cachedChandlerBaseURL() string {
	chandlerBaseMu.RLock()
	defer chandlerBaseMu.RUnlock()
	return resolvedChandlerBaseURL
}

func cacheChandlerBaseURL(value string) {
	chandlerBaseMu.Lock()
	resolvedChandlerBaseURL = value
	chandlerBaseMu.Unlock()
}

func resolvePlatformChandlerBaseURL() string {
	clusterID := strings.TrimSpace(localClusterID)
	if clusterID == "" || getClusterFn == nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	cluster, err := getClusterFn(ctx, clusterID)
	if err != nil || cluster == nil {
		return ""
	}

	baseDomain := strings.TrimSpace(cluster.GetBaseUrl())
	if baseDomain == "" {
		return ""
	}

	clusterSlug := pkgdns.ClusterSlug(clusterID, cluster.GetClusterName())
	if clusterSlug == "" {
		return ""
	}

	fqdn, ok := pkgdns.ServiceFQDN("chandler", clusterSlug+"."+baseDomain)
	if !ok || fqdn == "" {
		return ""
	}

	return "https://" + fqdn
}

func reconcileNodeCluster(ctx context.Context, canonicalNodeID, clusterID string, logger logging.Logger) string {
	if canonicalNodeID == "" || getNodeOwnerFn == nil {
		return clusterID
	}

	lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	ownerResp, err := getNodeOwnerFn(lookupCtx, canonicalNodeID)
	if err != nil {
		logger.WithError(err).WithField("node_id", canonicalNodeID).Debug("Node cluster reconciliation lookup failed")
		return clusterID
	}

	if ownerResp.GetClusterId() != "" && ownerResp.GetClusterId() != clusterID {
		logger.WithFields(logging.Fields{
			"node_id":           canonicalNodeID,
			"cluster_id_before": clusterID,
			"cluster_id_after":  ownerResp.GetClusterId(),
		}).Info("Reconciled node cluster from Quartermaster")
		return ownerResp.GetClusterId()
	}

	return clusterID
}

// SetNavigatorClient sets the Navigator client used for cluster wildcard certificate retrieval.
func SetNavigatorClient(c *navclient.Client) { navigatorClient = c }

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
			canonicalNodeID := nodeID
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
				if canonicalNodeID != "" && canonicalNodeID != nodeID {
					if c, ok := registry.conns[canonicalNodeID]; ok && c.stream == stream {
						delete(registry.conns, canonicalNodeID)
					}
				}
				registry.mu.Unlock()
				state.DefaultManager().MarkNodeDisconnected(nodeID)
				if canonicalNodeID != "" && canonicalNodeID != nodeID {
					state.DefaultManager().MarkNodeDisconnected(canonicalNodeID)
				}
				if rs := GetRedisStore(); rs != nil {
					if _, err := rs.DeleteConnOwnerIfMatch(context.Background(), nodeID, GetInstanceID(), GetAdvertiseAddr()); err != nil {
						registry.log.WithError(err).WithField("node_id", nodeID).Warn("Failed to clean conn owner in Redis")
					}
					if canonicalNodeID != "" && canonicalNodeID != nodeID {
						if _, err := rs.DeleteConnOwnerIfMatch(context.Background(), canonicalNodeID, GetInstanceID(), GetAdvertiseAddr()); err != nil {
							registry.log.WithError(err).WithField("node_id", canonicalNodeID).Warn("Failed to clean conn owner in Redis")
						}
					}
				}
			}

			// HA: register connection ownership in Redis so peer instances can relay commands
			if rs := GetRedisStore(); rs != nil {
				if err := rs.SetConnOwner(context.Background(), nodeID, GetInstanceID(), GetAdvertiseAddr()); err != nil {
					registry.log.WithError(err).WithField("node_id", nodeID).Warn("Failed to set conn owner in Redis")
				}
			}

			// Fingerprint-based tenant resolution (pre-provisioned mappings only; no creation here)
			tenantID := ""
			clusterID := ""
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
				// TenantID/ClusterID are resolved below via fingerprint or enrollment.
				state.DefaultManager().SetNodeConnectionInfo(context.Background(), nodeID, host, tenantID, clusterID, nil)

				fpReq := &pb.ResolveNodeFingerprintRequest{PeerIp: host}
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
					resp, err := quartermasterClient.ResolveNodeFingerprint(ctx, fpReq)
					cancel()
					if err == nil && resp != nil {
						tenantID = resp.TenantId
						if resp.CanonicalNodeId != "" {
							canonicalNodeID = resp.CanonicalNodeId
						}
						registry.log.WithFields(logging.Fields{"node_id": canonicalNodeID, "tenant_id": tenantID}).Info("Resolved tenant via fingerprint")
					} else if err != nil {
						registry.log.WithError(err).WithField("node_id", nodeID).Debug("Fingerprint resolution did not match; enrollment token may be required")
					}
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
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				req := buildBootstrapEdgeNodeRequest(stream.Context(), x.Register, nodeID, peerAddr, tok, localClusterID, ServedClustersSnapshot())
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
				clusterID = resp.ClusterId
				registry.log.WithFields(logging.Fields{"node_id": canonicalNodeID, "tenant_id": tenantID, "cluster_id": clusterID}).Info("Edge node enrolled via Quartermaster")
			}

			clusterID = reconcileNodeCluster(stream.Context(), canonicalNodeID, clusterID, registry.log)

			// Persist resolved tenant/cluster ownership on the node state
			if clusterID != "" {
				AddServedCluster(clusterID)
			}
			if tenantID != "" || clusterID != "" {
				state.DefaultManager().SetNodeConnectionInfo(context.Background(), canonicalNodeID, "", tenantID, clusterID, nil)
				// When canonical differs, also stamp the actively-heartbeated nodeID
				// so the balancer's tenant filter sees the correct ownership.
				if canonicalNodeID != nodeID {
					state.DefaultManager().SetNodeConnectionInfo(context.Background(), nodeID, "", tenantID, clusterID, nil)
				}
			}
			if fingerprintResolved {
				// Fingerprint resolution means Quartermaster already knows this node;
				// do not let a stale activation-probe flag from Redis keep it unroutable.
				state.DefaultManager().SetProbeVerified(canonicalNodeID, true)
				if canonicalNodeID != nodeID {
					state.DefaultManager().SetProbeVerified(nodeID, true)
				}
			}

			// Store canonical node ID back into conn for cert refresh and other lookups
			if canonicalNodeID != nodeID {
				registry.mu.Lock()
				if c, ok := registry.conns[nodeID]; ok {
					c.canonicalID = canonicalNodeID
					registry.conns[canonicalNodeID] = c
				}
				registry.mu.Unlock()

				if rs := GetRedisStore(); rs != nil {
					if err := rs.SetConnOwner(context.Background(), canonicalNodeID, GetInstanceID(), GetAdvertiseAddr()); err != nil {
						registry.log.WithError(err).WithField("node_id", canonicalNodeID).Warn("Failed to set canonical conn owner in Redis")
					}
				}
			}

			// Determine operational mode: DB-persisted wins over Helmsman's request
			operationalMode := resolveOperationalMode(canonicalNodeID, x.Register.GetRequestedMode())
			seed := composeConfigSeed(canonicalNodeID, x.Register.GetRoles(), peerAddr, operationalMode, clusterID)
			if tenantID != "" {
				seed.TenantId = tenantID
			}
			// Wildcard site without TLS cert is unusable — Caddy would attempt
			// auto-ACME DNS-01 which isn't configured. Stay on bootstrap until
			// Navigator provisions the cert.
			if seed.GetTls() == nil && seed.GetSite() != nil && strings.HasPrefix(seed.GetSite().GetSiteAddress(), "*.") {
				seed.Site = nil
			}
			_ = SendConfigSeed(nodeID, seed)

			// Fresh enrollments without a usable site are not routable.
			if !fingerprintResolved && (seed.GetSite() == nil || seed.GetSite().GetEdgeDomain() == "") {
				state.DefaultManager().SetProbeVerified(canonicalNodeID, false)
				if canonicalNodeID != nodeID {
					state.DefaultManager().SetProbeVerified(nodeID, false)
				}
				registry.log.WithField("node_id", canonicalNodeID).Warn("Fresh enrollment produced no site config; node marked unverified")
			}

			// Activation: reconnecting nodes (fingerprint resolved) are already verified
			// (ProbeVerified defaults to true in newNodeState). Fresh enrollments get
			// probed — Foghorn verifies the HTTPS endpoint before routing traffic.
			if !fingerprintResolved && seed.GetSite() != nil && seed.GetSite().GetEdgeDomain() != "" {
				state.DefaultManager().SetProbeVerified(canonicalNodeID, false)
				if canonicalNodeID != nodeID {
					state.DefaultManager().SetProbeVerified(nodeID, false)
				}
				go probeEdgeActivation(canonicalNodeID, seed.GetSite().GetEdgeDomain(), nodeID)
			}

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

			// Register per-capability service instances for DNS routing
			if quartermasterClient != nil && clusterID != "" {
				go func(reg *pb.Register, nid, cid, addr string) {
					peerHost, _, _ := net.SplitHostPort(addr)
					if peerHost == "" {
						peerHost = addr
					}
					caps := map[string]bool{
						"edge-egress":     reg.GetCapEdge(),
						"edge-ingest":     reg.GetCapIngest(),
						"edge-storage":    reg.GetCapStorage(),
						"edge-processing": reg.GetCapProcessing(),
					}
					healthEp := "/api"
					for svcType, enabled := range caps {
						if !enabled {
							continue
						}
						capCtx, capCancel := context.WithTimeout(context.Background(), 5*time.Second)
						_, err := quartermasterClient.BootstrapService(capCtx, &pb.BootstrapServiceRequest{
							Type:           svcType,
							Version:        version.Version,
							Protocol:       "http",
							HealthEndpoint: &healthEp,
							Port:           18008,
							AdvertiseHost:  &peerHost,
							Host:           peerHost,
							ClusterId:      &cid,
							NodeId:         &nid,
						})
						capCancel()
						if err != nil {
							registry.log.WithFields(logging.Fields{
								"node_id":      nid,
								"service_type": svcType,
								"error":        err,
							}).Warn("Failed to register edge capability service instance")
						} else {
							registry.log.WithFields(logging.Fields{
								"node_id":      nid,
								"service_type": svcType,
							}).Info("Registered edge capability service instance")
						}
					}
				}(x.Register, canonicalNodeID, clusterID, peerAddr)
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
				go artifactDeletedHandler(context.Background(), x.ArtifactDeleted)
			}
			go handleArtifactDeleted(x.ArtifactDeleted, nodeID, registry.log)
		case *pb.ControlMessage_Heartbeat:
			if nodeID != "" {
				canonicalNodeID := nodeID
				registry.mu.Lock()
				c := registry.conns[nodeID]
				if c != nil {
					c.last = time.Now()
					if c.canonicalID != "" {
						canonicalNodeID = c.canonicalID
					}
				}
				registry.mu.Unlock()
				if c == nil {
					// Connection was removed (e.g. activation failed) — terminate stream
					return nil
				}
				state.DefaultManager().TouchNode(nodeID, true)
				if canonicalNodeID != nodeID {
					state.DefaultManager().TouchNode(canonicalNodeID, true)
				}
				// HA: refresh connection ownership TTL
				if rs := GetRedisStore(); rs != nil {
					refreshOrRestore := func(nid string) {
						if err := rs.RefreshConnOwner(context.Background(), nid); err != nil {
							if errors.Is(err, state.ErrConnOwnerMissing) {
								if setErr := rs.SetConnOwner(context.Background(), nid, GetInstanceID(), GetAdvertiseAddr()); setErr != nil {
									registry.log.WithError(setErr).WithField("node_id", nid).Warn("Failed to restore conn owner in Redis")
								}
							} else {
								registry.log.WithError(err).WithField("node_id", nid).Warn("Failed to refresh conn owner TTL")
							}
						}
					}
					refreshOrRestore(nodeID)
					if canonicalNodeID != nodeID {
						refreshOrRestore(canonicalNodeID)
					}
				}
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
			go processFreezeComplete(context.Background(), x.FreezeComplete, nodeID, registry.log)
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
		case *pb.ControlMessage_ModeChangeRequest:
			go processModeChangeRequest(x.ModeChangeRequest, nodeID, stream, registry.log)
		case *pb.ControlMessage_ValidateEdgeTokenRequest:
			go processValidateEdgeToken(msg.GetRequestId(), x.ValidateEdgeTokenRequest, nodeID, stream, registry.log)
		case *pb.ControlMessage_ProcessingJobResult:
			if x.ProcessingJobResult.GetStatus() == "cache_update" {
				// Refresh cached overrides before returning so the restarted push
				// reads the latest value from Helmsman.
				processProcessingJobResult(x.ProcessingJobResult, nodeID, registry.log)
			} else {
				go processProcessingJobResult(x.ProcessingJobResult, nodeID, registry.log)
			}
		case *pb.ControlMessage_ProcessingJobProgress:
			go processProcessingJobProgress(x.ProcessingJobProgress, registry.log)
		case *pb.ControlMessage_ThumbnailUploadRequest:
			go processThumbnailUploadRequest(msg.GetRequestId(), x.ThumbnailUploadRequest, nodeID, stream, registry.log)
		case *pb.ControlMessage_ThumbnailUploaded:
			go processThumbnailUploaded(x.ThumbnailUploaded, nodeID, registry.log)
		}
	}
	if nodeID != "" {
		registry.mu.Lock()
		c := registry.conns[nodeID]
		canonicalID := ""
		if c != nil {
			canonicalID = c.canonicalID
		}
		delete(registry.conns, nodeID)
		if canonicalID != "" && canonicalID != nodeID {
			if cc, ok := registry.conns[canonicalID]; ok && cc.stream == stream {
				delete(registry.conns, canonicalID)
			}
		}
		registry.mu.Unlock()
		state.DefaultManager().MarkNodeDisconnected(nodeID)
		if canonicalID != "" && canonicalID != nodeID {
			state.DefaultManager().MarkNodeDisconnected(canonicalID)
		}
		if rs := GetRedisStore(); rs != nil {
			if _, err := rs.DeleteConnOwnerIfMatch(context.Background(), nodeID, GetInstanceID(), GetAdvertiseAddr()); err != nil {
				registry.log.WithError(err).WithField("node_id", nodeID).Warn("Failed to clean conn owner in Redis")
			}
			if canonicalID != "" && canonicalID != nodeID {
				if _, err := rs.DeleteConnOwnerIfMatch(context.Background(), canonicalID, GetInstanceID(), GetAdvertiseAddr()); err != nil {
					registry.log.WithError(err).WithField("node_id", canonicalID).Warn("Failed to clean conn owner in Redis")
				}
			}
		}
		registry.log.WithField("node_id", nodeID).Info("Helmsman disconnected")
	}
	return nil
}

// CleanupLocalConnOwners removes Redis conn_owner keys for currently connected nodes,
// but only when the key still belongs to this instance.
func CleanupLocalConnOwners(ctx context.Context) {
	rs := GetRedisStore()
	if rs == nil {
		return
	}

	instanceID := GetInstanceID()
	advertiseAddr := GetAdvertiseAddr()
	if instanceID == "" || advertiseAddr == "" {
		return
	}

	nodeIDs := make([]string, 0)
	registry.mu.RLock()
	for nodeID := range registry.conns {
		nodeIDs = append(nodeIDs, nodeID)
	}
	registry.mu.RUnlock()

	for _, nodeID := range nodeIDs {
		deleted, err := rs.DeleteConnOwnerIfMatch(ctx, nodeID, instanceID, advertiseAddr)
		if err != nil {
			registry.log.WithError(err).WithField("node_id", nodeID).Warn("Failed to clean conn owner during shutdown")
			continue
		}
		if deleted {
			registry.log.WithField("node_id", nodeID).Info("Cleaned conn owner during shutdown")
		}
	}
}

func SendLocalClipPull(nodeID string, req *pb.ClipPullRequest) error {
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

// SendClipPull sends a ClipPullRequest to the given node, relaying via HA if needed.
func SendClipPull(nodeID string, req *pb.ClipPullRequest) error {
	err := SendLocalClipPull(nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if relayErr := commandRelay.forward(ctx, &pb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &pb.ForwardCommandRequest_ClipPull{ClipPull: req},
	}); relayErr != nil {
		return relayFailure(err, relayErr)
	}
	return nil
}

func SendLocalDVRStart(nodeID string, req *pb.DVRStartRequest) error {
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

// SendDVRStart sends a DVRStartRequest to the given node, relaying via HA if needed.
func SendDVRStart(nodeID string, req *pb.DVRStartRequest) error {
	err := SendLocalDVRStart(nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if relayErr := commandRelay.forward(ctx, &pb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &pb.ForwardCommandRequest_DvrStart{DvrStart: req},
	}); relayErr != nil {
		return relayFailure(err, relayErr)
	}
	return nil
}

func SendLocalDVRStop(nodeID string, req *pb.DVRStopRequest) error {
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

// SendDVRStop sends a DVRStopRequest to the given node, relaying via HA if needed.
func SendDVRStop(nodeID string, req *pb.DVRStopRequest) error {
	err := SendLocalDVRStop(nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if relayErr := commandRelay.forward(ctx, &pb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &pb.ForwardCommandRequest_DvrStop{DvrStop: req},
	}); relayErr != nil {
		return relayFailure(err, relayErr)
	}
	return nil
}

func SendLocalClipDelete(nodeID string, req *pb.ClipDeleteRequest) error {
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

// SendClipDelete sends a ClipDeleteRequest to the given node, relaying via HA if needed.
func SendClipDelete(nodeID string, req *pb.ClipDeleteRequest) error {
	err := SendLocalClipDelete(nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if relayErr := commandRelay.forward(ctx, &pb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &pb.ForwardCommandRequest_ClipDelete{ClipDelete: req},
	}); relayErr != nil {
		return relayFailure(err, relayErr)
	}
	return nil
}

func SendLocalDVRDelete(nodeID string, req *pb.DVRDeleteRequest) error {
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

// SendDVRDelete sends a DVRDeleteRequest to the given node, relaying via HA if needed.
func SendDVRDelete(nodeID string, req *pb.DVRDeleteRequest) error {
	err := SendLocalDVRDelete(nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if relayErr := commandRelay.forward(ctx, &pb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &pb.ForwardCommandRequest_DvrDelete{DvrDelete: req},
	}); relayErr != nil {
		return relayFailure(err, relayErr)
	}
	return nil
}

func SendLocalVodDelete(nodeID string, req *pb.VodDeleteRequest) error {
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

// SendVodDelete sends a VodDeleteRequest to the given node, relaying via HA if needed.
func SendVodDelete(nodeID string, req *pb.VodDeleteRequest) error {
	err := SendLocalVodDelete(nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if relayErr := commandRelay.forward(ctx, &pb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &pb.ForwardCommandRequest_VodDelete{VodDelete: req},
	}); relayErr != nil {
		return relayFailure(err, relayErr)
	}
	return nil
}

// StopDVRByInternalName finds an active DVR for a stream and sends a stop to its storage node
func StopDVRByInternalName(internalName string, logger logging.Logger) {
	if db == nil || internalName == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Query foghorn.artifacts for active DVR, join with artifact_nodes for node_id
	var dvrHash, storageNodeID string
	err := db.QueryRowContext(ctx, `
        SELECT a.artifact_hash, COALESCE(an.node_id,'')
        FROM foghorn.artifacts a
        LEFT JOIN foghorn.artifact_nodes an ON a.artifact_hash = an.artifact_hash
        WHERE a.stream_internal_name = $1 AND a.artifact_type = 'dvr'
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
	updateCtx, updateCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer updateCancel()
	if _, err := db.ExecContext(updateCtx, `UPDATE foghorn.artifacts SET status = 'stopping', updated_at = NOW() WHERE artifact_hash = $1`, dvrHash); err != nil {
		logger.WithError(err).WithField("dvr_hash", dvrHash).Warn("Failed to update DVR status to stopping")
	}
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
		dvrData.StreamInternalName = &internalName
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
func StartGRPCServer(ctx context.Context, cfg GRPCServerConfig) (*grpc.Server, error) {
	lc := net.ListenConfig{}
	lis, err := lc.Listen(ctx, "tcp", cfg.Addr)
	if err != nil {
		return nil, err
	}

	// Auto-detect TLS: file-based cert → Navigator wildcard → insecure fallback
	var opts []grpc.ServerOption
	certFile := os.Getenv("GRPC_TLS_CERT_PATH")
	keyFile := os.Getenv("GRPC_TLS_KEY_PATH")

	if certFile != "" && keyFile != "" {
		tlsCfg := grpcutil.ServerTLSConfig{
			CertFile: certFile,
			KeyFile:  keyFile,
		}
		waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := grpcutil.WaitForServerTLSFiles(waitCtx, tlsCfg, cfg.Logger); err != nil {
			return nil, fmt.Errorf("timed out waiting for file-based gRPC TLS: %w", err)
		}
		tlsOpt, err := grpcutil.ServerTLS(tlsCfg, cfg.Logger)
		if err != nil {
			return nil, fmt.Errorf("failed to configure file-based TLS: %w", err)
		}
		opts = append(opts, tlsOpt)
		cfg.Logger.WithFields(logging.Fields{
			"cert_file": certFile,
			"key_file":  keyFile,
		}).Info("gRPC server TLS: file-based")
	} else if navigatorClient != nil {
		rootDomain := platformRootDomain()
		wildcardDomain := fmt.Sprintf("*.%s.%s", pkgdns.SanitizeLabel(localClusterID), rootDomain)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		certResp, certErr := navigatorClient.GetCertificate(ctx, &pb.GetCertificateRequest{Domain: wildcardDomain})
		cancel()
		if certErr == nil && certResp != nil && certResp.GetFound() {
			cert, err := tls.X509KeyPair([]byte(certResp.GetCertPem()), []byte(certResp.GetKeyPem()))
			if err != nil {
				return nil, fmt.Errorf("failed to parse Navigator certificate: %w", err)
			}
			serverCert.cert.Store(&cert)
			creds := credentials.NewTLS(&tls.Config{
				GetCertificate: serverCert.GetCertificate,
			})
			opts = append(opts, grpc.Creds(creds))
			cfg.Logger.WithFields(logging.Fields{
				"domain":     wildcardDomain,
				"expires_at": certResp.GetExpiresAt(),
			}).Info("gRPC server TLS: Navigator-backed")
		} else {
			if !allowInsecureControlGRPC() {
				_ = lis.Close()
				return nil, fmt.Errorf("navigator certificate unavailable and insecure control gRPC is disabled")
			}
			cfg.Logger.WithError(certErr).Warn("Navigator available but no cert found; gRPC server running without TLS")
		}
	} else {
		if !allowInsecureControlGRPC() {
			_ = lis.Close()
			return nil, fmt.Errorf("no TLS certificate source configured and insecure control gRPC is disabled")
		}
		cfg.Logger.Info("gRPC server running without TLS (no cert source)")
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
				pb.HelmsmanControl_Connect_FullMethodName,
				// EdgeProvisioning uses enrollment token validated in-method
				"/foghorn.EdgeProvisioningService/PreRegisterEdge",
			},
		})
		unaryInterceptors = append([]grpc.UnaryServerInterceptor{authInterceptor}, unaryInterceptors...)
	}

	opts = append(opts, grpc.ChainUnaryInterceptor(unaryInterceptors...))

	// Add stream auth interceptor for PeerChannel and other streaming RPCs
	if cfg.ServiceToken != "" {
		streamAuth := middleware.GRPCStreamAuthInterceptor(middleware.GRPCAuthConfig{
			ServiceToken: cfg.ServiceToken,
			Logger:       cfg.Logger,
			SkipMethods: []string{
				"/grpc.health.v1.Health/Watch",
				// HelmsmanControl uses bootstrap token validated in-method
				pb.HelmsmanControl_Connect_FullMethodName,
			},
		})
		opts = append(opts, grpc.ChainStreamInterceptor(streamAuth))
	}

	srv := grpc.NewServer(opts...)
	pb.RegisterHelmsmanControlServer(srv, &Server{})
	RegisterEdgeProvisioningService(srv)

	// gRPC health service for control plane
	hs := health.NewServer()
	hs.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	hs.SetServingStatus(pb.HelmsmanControl_ServiceDesc.ServiceName, grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(srv, hs)
	reflection.Register(srv)

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

func allowInsecureControlGRPC() bool {
	return config.GetEnvBool("GRPC_ALLOW_INSECURE", false)
}

// Helpers

var ErrNotConnected = status.Error(codes.Unavailable, "node not connected")

// shouldRelay reports whether a local send error warrants a relay attempt.
// Beyond ErrNotConnected (node absent from registry), it also triggers relay
// when stream.Send failed and the node was concurrently removed — covering
// the race between a stream dying and handleHelmsmanStream cleaning up.
func shouldRelay(nodeID string, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNotConnected) {
		return true
	}
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	return c == nil
}

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
	ctx := context.Background()
	// Get DVR hash and stream_id from Commodore registration
	dvrHash := req.GetDvrHash()
	streamID := req.GetStreamId()
	var artifactRoutingName string
	if dvrHash == "" || streamID == "" {
		if CommodoreClient == nil {
			logger.Error("Commodore not available for DVR registration")
			return
		}
		regCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		regReq := &pb.RegisterDVRRequest{
			TenantId:           req.GetTenantId(),
			UserId:             req.GetUserId(),
			StreamInternalName: req.GetInternalName(),
		}
		// Pass retention from DVR config if available
		if cfg := req.GetConfig(); cfg != nil && cfg.RetentionDays > 0 {
			retentionTime := time.Now().AddDate(0, 0, int(cfg.RetentionDays))
			regReq.RetentionUntil = timestamppb.New(retentionTime)
		}
		resp, err := CommodoreClient.RegisterDVR(regCtx, regReq)
		if err != nil {
			logger.WithError(err).Error("Failed to register DVR with Commodore")
			return
		}
		dvrHash = resp.DvrHash
		streamID = resp.GetStreamId()
		artifactRoutingName = resp.GetInternalName()
	}

	logger.WithFields(logging.Fields{
		"dvr_hash":      dvrHash,
		"internal_name": req.GetInternalName(),
		"node_id":       nodeID,
	}).Info("Processing DVR start request")

	// Tag ingest node stream instance with DVR requested
	state.DefaultManager().UpdateStreamInstanceInfo(req.GetInternalName(), nodeID, map[string]any{
		"dvr_status": "requested",
		"dvr_hash":   dvrHash,
	})

	// Store artifact lifecycle state in foghorn.artifacts with context for Decklog events
	_, err := db.ExecContext(ctx, `
		INSERT INTO foghorn.artifacts (
			artifact_hash, artifact_type, stream_internal_name, internal_name, stream_id, tenant_id, user_id, status, request_id, format, created_at, updated_at
		) VALUES ($1, 'dvr', $2, NULLIF($3,''), NULLIF($4,'')::uuid, NULLIF($5,'')::uuid, NULLIF($6,'')::uuid, 'requested', $7, 'm3u8', NOW(), NOW())
		ON CONFLICT (artifact_hash) DO UPDATE SET
			status = 'requested',
			stream_internal_name = COALESCE(foghorn.artifacts.stream_internal_name, EXCLUDED.stream_internal_name),
			internal_name = COALESCE(foghorn.artifacts.internal_name, EXCLUDED.internal_name),
			stream_id = COALESCE(foghorn.artifacts.stream_id, EXCLUDED.stream_id),
			tenant_id = COALESCE(foghorn.artifacts.tenant_id, EXCLUDED.tenant_id),
			user_id = COALESCE(foghorn.artifacts.user_id, EXCLUDED.user_id),
			format = COALESCE(foghorn.artifacts.format, EXCLUDED.format),
			updated_at = NOW()`,
		dvrHash, req.GetInternalName(), artifactRoutingName, streamID, req.GetTenantId(), req.GetUserId(), dvrHash)

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
		if _, dbErr := db.ExecContext(ctx, `UPDATE foghorn.artifacts SET status = 'failed', error_message = $1, updated_at = NOW() WHERE artifact_hash = $2`,
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
	_, err = db.ExecContext(ctx, `
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
		if _, dbErr := db.ExecContext(ctx, `UPDATE foghorn.artifacts SET status = 'failed', error_message = $1, updated_at = NOW() WHERE artifact_hash = $2`,
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
	state.DefaultManager().UpdateStreamInstanceInfo(req.GetInternalName(), storageNodeID, map[string]any{
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Look up the DVR artifact in database to get source stream info
	var internalName string
	err := db.QueryRowContext(ctx, `
		SELECT stream_internal_name
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
	state.DefaultManager().UpdateStreamInstanceInfo(internalName, requestingNodeID, map[string]any{
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
	updateCtx, updateCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer updateCancel()

	_, err = db.ExecContext(updateCtx, `
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
	dtscURI := strings.ReplaceAll(dtscTemplate, "HOST", hostname)

	// Use the template's static prefix when checking DVR readiness.
	baseDTSCURI := strings.ReplaceAll(dtscURI, "$", "")

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
	if trigger != nil {
		if ns := state.DefaultManager().GetNodeState(nodeID); ns != nil && strings.TrimSpace(ns.ClusterID) != "" {
			cid := strings.TrimSpace(ns.ClusterID)
			trigger.ClusterId = &cid
		} else if (trigger.ClusterId == nil || strings.TrimSpace(trigger.GetClusterId()) == "") && strings.TrimSpace(localClusterID) != "" {
			cid := strings.TrimSpace(localClusterID)
			trigger.ClusterId = &cid
		}
	}

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
			if ingestErr, ok := errors.AsType[*ingesterrors.IngestError](err); ok {
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

const edgeTelemetryTokenTTL = 365 * 24 * time.Hour

func composeConfigSeed(nodeID string, _ []string, peerAddr string, operationalMode pb.NodeOperationalMode, clusterID string) *pb.ConfigSeed {
	var lat, lon float64
	var loc string
	var ownerTenantID string

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
		{
			Id:    "processing",
			Def:   &pb.StreamDef{Name: "processing+$", Realtime: true, StopSessions: false, Tags: []string{"processing"}},
			Roles: []string{"edge", "storage"},
			Caps:  []string{"processing"},
		},
	}

	var tlsBundle *pb.TLSCertBundle
	var siteConfig *pb.SiteConfig

	resolvedClusterID := clusterID
	if quartermasterClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		node, err := quartermasterClient.GetNodeByLogicalName(ctx, nodeID)
		cancel()
		if err == nil && node != nil {
			if resolvedClusterID == "" {
				resolvedClusterID = strings.TrimSpace(node.GetClusterId())
			}
		}
	}
	if getNodeOwnerFn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		ownerResp, err := getNodeOwnerFn(ctx, nodeID)
		cancel()
		if err == nil && ownerResp != nil {
			ownerTenantID = strings.TrimSpace(ownerResp.GetOwnerTenantId())
			if resolvedClusterID == "" {
				resolvedClusterID = strings.TrimSpace(ownerResp.GetClusterId())
			}
		}
	}
	if resolvedClusterID != "" {
		rootDomain := platformRootDomain()
		slug := pkgdns.SanitizeLabel(resolvedClusterID)

		siteConfig = &pb.SiteConfig{
			SiteAddress: fmt.Sprintf("*.%s.%s", slug, rootDomain),
			EdgeDomain:  fmt.Sprintf("%s.%s.%s", edgeNodeRecordLabel(nodeID), slug, rootDomain),
			PoolDomain:  fmt.Sprintf("edge.%s.%s", slug, rootDomain),
			AcmeEmail:   os.Getenv("ACME_EMAIL"),
		}

		if navigatorClient != nil {
			wildcardDomain := fmt.Sprintf("*.%s.%s", slug, rootDomain)
			certCtx, certCancel := context.WithTimeout(context.Background(), 5*time.Second)
			certResp, certErr := navigatorClient.GetCertificate(certCtx, &pb.GetCertificateRequest{Domain: wildcardDomain})
			certCancel()
			if certErr == nil && certResp != nil && certResp.GetFound() {
				tlsBundle = &pb.TLSCertBundle{
					CertPem:   certResp.GetCertPem(),
					KeyPem:    certResp.GetKeyPem(),
					Domain:    certResp.GetDomain(),
					ExpiresAt: certResp.GetExpiresAt(),
				}
			}
		}
	}

	caBundle := readConfiguredCABundle()
	telemetry := buildEdgeTelemetryConfig(nodeID, resolvedClusterID, ownerTenantID)

	return &pb.ConfigSeed{
		NodeId:              nodeID,
		Latitude:            lat,
		Longitude:           lon,
		LocationName:        loc,
		Templates:           templates,
		OperationalMode:     operationalMode,
		Tls:                 tlsBundle,
		Site:                siteConfig,
		CaBundle:            caBundle,
		TenantId:            ownerTenantID,
		Telemetry:           telemetry,
		FoghornBalancerBase: foghornBalancerBase(resolvedClusterID),
	}
}

// foghornBalancerBase returns the public HTTP base URL Helmsman should use for
// MistServer's balance:<base> source. Runtime cluster state wins: edge nodes get
// their cluster-scoped Foghorn DNS name. Env overrides are fallback escape
// hatches for non-managed deployments.
func foghornBalancerBase(clusterID string) string {
	if clusterID != "" {
		rootDomain := platformRootDomain()
		clusterSlug := pkgdns.SanitizeLabel(clusterID)
		if clusterSlug != "" && rootDomain != "" {
			if fqdn, ok := pkgdns.ServiceFQDN("foghorn", clusterSlug+"."+rootDomain); ok && fqdn != "" {
				return "https://" + fqdn
			}
		}
	}
	if v := strings.TrimSpace(os.Getenv("FOGHORN_PUBLIC_BASE")); v != "" {
		return v
	}
	if h := strings.TrimSpace(os.Getenv("FOGHORN_HOST")); h != "" {
		return fmt.Sprintf("https://%s:18008", h)
	}
	return "http://foghorn:18008"
}

type edgeTelemetryClaims struct {
	NodeID    string `json:"node_id"`
	ClusterID string `json:"cluster_id"`
	TenantID  string `json:"tenant_id,omitempty"`
	Role      string `json:"role"`
	VMAccess  struct {
		MetricsExtraLabels string `json:"metrics_extra_labels"`
	} `json:"vm_access"`
	jwt.RegisteredClaims
}

func buildEdgeTelemetryConfig(nodeID, clusterID, tenantID string) *pb.EdgeTelemetryConfig {
	nodeID = strings.TrimSpace(nodeID)
	clusterID = strings.TrimSpace(clusterID)
	if nodeID == "" || clusterID == "" {
		return nil
	}
	writeURL := edgeTelemetryWriteURL(clusterID)
	if writeURL == "" {
		return nil
	}
	token, expiresAt, err := mintEdgeTelemetryToken(nodeID, clusterID, tenantID)
	if err != nil {
		logging.NewLogger().WithError(err).WithFields(logging.Fields{
			"node_id":    nodeID,
			"cluster_id": clusterID,
		}).Warn("Failed to mint edge telemetry token")
		return nil
	}
	return &pb.EdgeTelemetryConfig{
		Enabled:     true,
		WriteUrl:    writeURL,
		BearerToken: token,
		ExpiresAt:   expiresAt.Format(time.RFC3339),
	}
}

func edgeTelemetryWriteURL(clusterID string) string {
	clusterID = strings.TrimSpace(clusterID)
	if clusterID == "" {
		return ""
	}
	clusterSlug := pkgdns.SanitizeLabel(clusterID)
	rootDomain := platformRootDomain()
	if getClusterFn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		cluster, err := getClusterFn(ctx, clusterID)
		cancel()
		if err == nil && cluster != nil {
			if slug := pkgdns.ClusterSlug(clusterID, cluster.GetClusterName()); slug != "" {
				clusterSlug = slug
			}
			if baseURL := strings.TrimSpace(cluster.GetBaseUrl()); baseURL != "" {
				rootDomain = baseURL
			}
		}
	}
	if clusterSlug == "" || rootDomain == "" {
		return ""
	}
	fqdn, ok := pkgdns.ServiceFQDN("telemetry", clusterSlug+"."+rootDomain)
	if !ok || fqdn == "" {
		return ""
	}
	return "https://" + fqdn + "/api/v1/write"
}

func mintEdgeTelemetryToken(nodeID, clusterID, tenantID string) (string, time.Time, error) {
	privateKey, err := parseEdgeTelemetryPrivateKey()
	if err != nil {
		return "", time.Time{}, err
	}
	now := time.Now().UTC()
	expiresAt := now.Add(edgeTelemetryTokenTTL)
	claims := edgeTelemetryClaims{
		NodeID:    nodeID,
		ClusterID: clusterID,
		TenantID:  strings.TrimSpace(tenantID),
		Role:      "edge_metrics_write",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "foghorn",
			Subject:   "edge/" + nodeID,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-1 * time.Minute)),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}
	claims.VMAccess.MetricsExtraLabels = "frameworks_node=" + nodeID
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	signed, err := token.SignedString(privateKey)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign telemetry token: %w", err)
	}
	return signed, expiresAt, nil
}

func parseEdgeTelemetryPrivateKey() (*ecdsa.PrivateKey, error) {
	encoded := strings.TrimSpace(os.Getenv("EDGE_TELEMETRY_JWT_PRIVATE_KEY_PEM_B64"))
	if encoded == "" {
		return nil, fmt.Errorf("EDGE_TELEMETRY_JWT_PRIVATE_KEY_PEM_B64 is not set")
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode telemetry private key: %w", err)
	}
	block, _ := pem.Decode(decoded)
	if block == nil {
		return nil, fmt.Errorf("decode telemetry private key PEM: no PEM block found")
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse telemetry private key: %w", err)
	}
	key, ok := keyAny.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("telemetry private key is %T, expected ECDSA", keyAny)
	}
	return key, nil
}

func resolveClusterTLSBundle(nodeID string) *pb.TLSCertBundle {
	bundle, found, err := fetchClusterTLSBundle(nodeID)
	if err != nil || !found {
		return nil
	}
	return bundle
}

func SendLocalConfigSeed(nodeID string, seed *pb.ConfigSeed) error {
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

// SendConfigSeed sends a ConfigSeed to the given node, relaying via HA if needed.
func SendConfigSeed(nodeID string, seed *pb.ConfigSeed) error {
	err := SendLocalConfigSeed(nodeID, seed)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil || seed == nil {
		return ErrNotConnected
	}
	return relayFailure(err, commandRelay.forward(context.Background(), &pb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &pb.ForwardCommandRequest_ConfigSeed{ConfigSeed: seed},
	}))
}

func SendLocalPushOperationalMode(nodeID string, mode pb.NodeOperationalMode) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}

	// Helmsman sidecar does NOT merge ConfigSeeds; ApplySeed overwrites lastSeed.
	// Send a full seed to avoid wiping previously seeded fields.
	seed := composeConfigSeed(nodeID, nil, c.peerAddr, mode, "")
	msg := &pb.ControlMessage{
		Payload: &pb.ControlMessage_ConfigSeed{ConfigSeed: seed},
		SentAt:  timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

// PushOperationalMode sends a ConfigSeed with the specified operational mode to the node,
// relaying via HA if needed.
func PushOperationalMode(nodeID string, mode pb.NodeOperationalMode) error {
	err := SendLocalPushOperationalMode(nodeID, mode)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	// For relay: compose a full ConfigSeed (without peer addr, since we don't hold the conn)
	seed := composeConfigSeed(nodeID, nil, "", mode, "")
	return relayFailure(err, commandRelay.forward(context.Background(), &pb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &pb.ForwardCommandRequest_ConfigSeed{ConfigSeed: seed},
	}))
}

// processModeChangeRequest handles an upstream mode change request from Helmsman.
// Validates the mode and applies it via the existing SetNodeOperationalMode + PushOperationalMode path.
func processModeChangeRequest(req *pb.ModeChangeRequest, nodeID string, _ pb.HelmsmanControl_ConnectServer, log logging.Logger) {
	if req == nil {
		return
	}

	protoMode := req.GetRequestedMode()
	if protoMode == pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_UNSPECIFIED {
		protoMode = pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL
	}

	log.WithFields(logging.Fields{
		"node_id":        nodeID,
		"requested_mode": protoMode.String(),
		"reason":         req.GetReason(),
	}).Info("Received mode change request from Helmsman")

	var stateMode state.NodeOperationalMode
	switch protoMode {
	case pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_DRAINING:
		stateMode = state.NodeModeDraining
	case pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_MAINTENANCE:
		stateMode = state.NodeModeMaintenance
	default:
		stateMode = state.NodeModeNormal
	}

	setBy := "helmsman:" + req.GetReason()
	if err := state.DefaultManager().SetNodeOperationalMode(context.Background(), nodeID, stateMode, setBy); err != nil {
		log.WithError(err).WithField("node_id", nodeID).Error("Failed to set operational mode from Helmsman request")
		return
	}

	if err := PushOperationalMode(nodeID, protoMode); err != nil {
		log.WithError(err).WithField("node_id", nodeID).Warn("Failed to push operational mode back to node")
	}
}

// ==================== Cold Storage (Freeze/Defrost) Handlers ====================

// S3ClientInterface defines the interface for S3 operations (for dependency injection)
type S3ClientInterface interface {
	GeneratePresignedPUT(key string, expiry time.Duration) (string, error)
	GeneratePresignedGET(key string, expiry time.Duration) (string, error)
	ListPrefix(ctx context.Context, prefix string) ([]string, error)
	Delete(ctx context.Context, key string) error
	DeleteByURL(ctx context.Context, s3URL string) error
	DeletePrefix(ctx context.Context, prefix string) (int, error)
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// For DVR segment/manifest incremental sync, resolve to the parent DVR artifact.
	// The artifacts table stores one row per DVR with artifact_type='dvr', but Helmsman
	// sends dvr_segment/dvr_manifest with compound hashes like "dvrHash/filename".
	lookupHash := assetHash
	lookupType := assetType
	if assetType == "dvr_segment" || assetType == "dvr_manifest" {
		lookupType = "dvr"
		if before, _, ok := strings.Cut(assetHash, "/"); ok {
			lookupHash = before
		}
	}

	var streamName string
	var originCluster sql.NullString
	var syncStatus sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT stream_internal_name, origin_cluster_id, sync_status
		FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND artifact_type = $2`,
		lookupHash, lookupType).Scan(&streamName, &originCluster, &syncStatus)

	// For DVR segment/manifest incremental sync, assetHash is "{dvr_hash}/{filename}"
	dvrHash := assetHash
	if assetType == "dvr_segment" || assetType == "dvr_manifest" {
		if before, _, ok := strings.Cut(assetHash, "/"); ok {
			dvrHash = before
		}
	}

	// Resolve tenant (and stream name if DB row was missing) via Commodore
	var tenantID string
	if CommodoreClient != nil {
		resolveCtx, resolveCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer resolveCancel()
		switch assetType {
		case "clip":
			if resp, resolveErr := CommodoreClient.ResolveClipHash(resolveCtx, assetHash); resolveErr == nil && resp.Found {
				tenantID = resp.TenantId
				if streamName == "" {
					streamName = resp.StreamInternalName
				}
			}
		case "dvr", "dvr_segment", "dvr_manifest":
			if resp, resolveErr := CommodoreClient.ResolveDVRHash(resolveCtx, dvrHash); resolveErr == nil && resp.Found {
				tenantID = resp.TenantId
				if streamName == "" {
					streamName = resp.StreamInternalName
				}
			}
		case "vod":
			if resp, resolveErr := CommodoreClient.ResolveVodHash(resolveCtx, assetHash); resolveErr == nil && resp.Found {
				tenantID = resp.TenantId
				if streamName == "" {
					streamName = resp.InternalName
				}
			}
		}
	}

	// If DB row was missing but Commodore resolved the artifact, create the lifecycle row
	if err != nil && tenantID != "" && streamName != "" {
		if _, dbErr := db.ExecContext(ctx, `
			INSERT INTO foghorn.artifacts
				(artifact_hash, artifact_type, stream_internal_name, tenant_id,
				 storage_location, sync_status, created_at, updated_at)
			VALUES ($1, $2, $3, $4, 'local', 'pending', NOW(), NOW())
			ON CONFLICT (artifact_hash) DO NOTHING`,
			lookupHash, lookupType, streamName, tenantID); dbErr != nil {
			logger.WithError(dbErr).WithField("asset_hash", lookupHash).Error("failed to create lifecycle row from Commodore")
		}
		logger.WithFields(logging.Fields{
			"asset_hash": lookupHash,
			"asset_type": lookupType,
			"tenant_id":  tenantID,
		}).Info("Created lifecycle row from Commodore during freeze permission")
		err = nil
	}

	if err != nil {
		logger.WithFields(logging.Fields{
			"asset_hash":  assetHash,
			"asset_type":  assetType,
			"lookup_hash": lookupHash,
			"lookup_type": lookupType,
			"error":       err,
		}).Error("Asset not found in database or Commodore")
		sendFreezePermissionResponse(stream, &pb.FreezePermissionResponse{
			RequestId: requestID,
			AssetHash: assetHash,
			Approved:  false,
			Reason:    "asset_not_found",
		}, logger)
		return
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

	// Remote artifact: origin S3 is authoritative — skip upload, just evict
	isRemote := originCluster.Valid && originCluster.String != "" && !isServedCluster(originCluster.String)
	if isRemote {
		logger.WithFields(logging.Fields{
			"asset_hash":     assetHash,
			"origin_cluster": originCluster.String,
		}).Info("Remote artifact — skip_upload=true (origin S3 authoritative)")
		sendFreezePermissionResponse(stream, &pb.FreezePermissionResponse{
			RequestId:  requestID,
			AssetHash:  assetHash,
			Approved:   true,
			SkipUpload: true,
		}, logger)
		return
	}

	// Already synced to S3 — no need to re-freeze
	if syncStatus.Valid && syncStatus.String == "synced" {
		logger.WithFields(logging.Fields{
			"asset_hash": assetHash,
			"asset_type": assetType,
			"node_id":    nodeID,
		}).Debug("Asset already synced to S3, rejecting freeze permission")
		sendFreezePermissionResponse(stream, &pb.FreezePermissionResponse{
			RequestId: requestID,
			AssetHash: assetHash,
			Approved:  false,
			Reason:    "already_synced",
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
		if _, after, ok := strings.Cut(assetHash, "/"); ok {
			filename = after
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
		if _, dbErr := db.ExecContext(context.Background(), `UPDATE foghorn.artifacts SET storage_location = 'freezing', sync_status = 'in_progress', updated_at = NOW() WHERE artifact_hash = $1`, assetHash); dbErr != nil {
			logger.WithError(dbErr).WithField("asset_hash", assetHash).Error("failed to mark artifact as freezing")
		}
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
func processFreezeComplete(ctx context.Context, complete *pb.FreezeComplete, nodeID string, logger logging.Logger) {
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
		if _, dbErr := db.ExecContext(ctx, `
				UPDATE foghorn.artifacts
				SET storage_location = 'local',
				    sync_status = 'synced',
				    s3_url = NULLIF($1, ''),
				    frozen_at = NOW(),
			    last_sync_attempt = NOW(),
			    sync_error = NULL,
			    updated_at = NOW()
			WHERE artifact_hash = $2`,
			s3URL, assetHash); dbErr != nil {
			logger.WithError(dbErr).WithField("asset_hash", assetHash).Error("failed to update artifact after successful freeze")
		}
	} else {
		// Revert storage location on failure
		if _, dbErr := db.ExecContext(ctx, `
			UPDATE foghorn.artifacts
			SET storage_location = 'local',
			    sync_status = 'failed',
			    sync_error = NULLIF($1,''),
			    last_sync_attempt = NOW(),
			    updated_at = NOW()
			WHERE artifact_hash = $2
		`, errorMsg, assetHash); dbErr != nil {
			logger.WithError(dbErr).WithField("asset_hash", assetHash).Error("failed to revert artifact after freeze failure")
		}

		// Clean up partial S3 uploads to avoid storage garbage
		if s3Client != nil {
			var artifactType, streamName, tenantID string
			_ = db.QueryRowContext(ctx, `
				SELECT artifact_type, COALESCE(stream_internal_name,''), COALESCE(tenant_id::text,'')
				FROM foghorn.artifacts WHERE artifact_hash = $1`, assetHash).Scan(&artifactType, &streamName, &tenantID)
			if tenantID != "" {
				go func() {
					cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cleanCancel()
					var prefix string
					switch artifactType {
					case "dvr":
						prefix = s3Client.BuildDVRS3Key(tenantID, streamName, assetHash)
					case "clip":
						prefix = s3Client.BuildClipS3Key(tenantID, streamName, assetHash, "")
						// Clip key includes format extension — strip it to get the prefix
						if idx := strings.LastIndex(prefix, "."); idx != -1 {
							prefix = prefix[:idx]
						}
					case "vod":
						prefix = s3Client.BuildVodS3Key(tenantID, assetHash, assetHash)
						if idx := strings.LastIndex(prefix, "/"); idx != -1 {
							prefix = prefix[:idx+1] + assetHash
						}
					}
					if prefix != "" {
						deleted, err := s3Client.DeletePrefix(cleanCtx, prefix)
						if err != nil {
							logger.WithError(err).WithField("prefix", prefix).Warn("Failed to clean up partial S3 uploads")
						} else if deleted > 0 {
							logger.WithFields(logging.Fields{
								"prefix":  prefix,
								"deleted": deleted,
							}).Info("Cleaned up partial S3 uploads after freeze failure")
						}
					}
				}()
			}
		}
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
		result, err := db.ExecContext(context.Background(), `
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
		if _, dbErr := db.ExecContext(context.Background(), `
			UPDATE foghorn.artifacts
			SET storage_location = 's3',
			    defrost_node_id = NULL,
			    defrost_started_at = NULL,
			    updated_at = NOW()
			WHERE artifact_hash = $1
			  AND storage_location = 'defrosting'
			  AND (defrost_node_id = $2 OR defrost_node_id IS NULL)
		`, assetHash, reportingNodeID); dbErr != nil {
			logger.WithError(dbErr).WithField("asset_hash", assetHash).Error("failed to revert artifact after defrost failure")
		}
		logger.WithFields(logging.Fields{
			"asset_hash": assetHash,
			"error":      errorMsg,
		}).Warn("Defrost failed, reverted to s3")
	}

	// Notify any waiting defrost requests
	notifyDefrostComplete(assetHash, status == "success", localPath)
}

func SendLocalDefrostRequest(nodeID string, req *pb.DefrostRequest) error {
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

// SendDefrostRequest sends a DefrostRequest to the given node, relaying via HA if needed.
func SendDefrostRequest(nodeID string, req *pb.DefrostRequest) error {
	err := SendLocalDefrostRequest(nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	return relayFailure(err, commandRelay.forward(context.Background(), &pb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &pb.ForwardCommandRequest_Defrost{Defrost: req},
	}))
}

// SendFreezeRequest sends a proactive FreezeRequest to the given node, relaying via HA if needed.
func SendFreezeRequest(nodeID string, req *pb.FreezeRequest) error {
	err := SendLocalFreezeRequest(nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	return relayFailure(err, commandRelay.forward(context.Background(), &pb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &pb.ForwardCommandRequest_Freeze{Freeze: req},
	}))
}

func SendLocalFreezeRequest(nodeID string, req *pb.FreezeRequest) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &pb.ControlMessage{
		RequestId: req.RequestId,
		Payload:   &pb.ControlMessage_FreezeRequest{FreezeRequest: req},
		SentAt:    timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

func SendLocalDtshSyncRequest(nodeID string, req *pb.DtshSyncRequest) error {
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

// SendDtshSyncRequest sends a DtshSyncRequest to the given node, relaying via HA if needed.
func SendDtshSyncRequest(nodeID string, req *pb.DtshSyncRequest) error {
	err := SendLocalDtshSyncRequest(nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	return relayFailure(err, commandRelay.forward(context.Background(), &pb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &pb.ForwardCommandRequest_DtshSync{DtshSync: req},
	}))
}

func SendLocalStopSessions(nodeID string, req *pb.StopSessionsRequest) error {
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

// SendStopSessions sends a StopSessionsRequest to the given node, relaying via HA if needed.
func SendStopSessions(nodeID string, req *pb.StopSessionsRequest) error {
	err := SendLocalStopSessions(nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	return relayFailure(err, commandRelay.forward(context.Background(), &pb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &pb.ForwardCommandRequest_StopSessions{StopSessions: req},
	}))
}

// SendLocalActivatePushTargets sends an ActivatePushTargets message to a local Helmsman.
func SendLocalActivatePushTargets(nodeID string, req *pb.ActivatePushTargets) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &pb.ControlMessage{
		Payload: &pb.ControlMessage_ActivatePushTargets{ActivatePushTargets: req},
		SentAt:  timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

// SendActivatePushTargets sends ActivatePushTargets to the given node, relaying via HA if needed.
func SendActivatePushTargets(nodeID string, req *pb.ActivatePushTargets) error {
	err := SendLocalActivatePushTargets(nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	return relayFailure(err, commandRelay.forward(context.Background(), &pb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &pb.ForwardCommandRequest_ActivatePushTargets{ActivatePushTargets: req},
	}))
}

// SendLocalDeactivatePushTargets sends a DeactivatePushTargets message to a local Helmsman.
func SendLocalDeactivatePushTargets(nodeID string, req *pb.DeactivatePushTargets) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &pb.ControlMessage{
		Payload: &pb.ControlMessage_DeactivatePushTargets{DeactivatePushTargets: req},
		SentAt:  timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

// SendDeactivatePushTargets sends DeactivatePushTargets to the given node, relaying via HA if needed.
func SendDeactivatePushTargets(nodeID string, req *pb.DeactivatePushTargets) error {
	err := SendLocalDeactivatePushTargets(nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	return relayFailure(err, commandRelay.forward(context.Background(), &pb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &pb.ForwardCommandRequest_DeactivatePushTargets{DeactivatePushTargets: req},
	}))
}

// ProcessingJobResultHandler is called after a processing job result is persisted.
// Set by the grpc package to avoid circular imports (control → grpc).
type ProcessingJobResultHandler func(ctx context.Context, jobID, status string, outputs map[string]string, errorMsg string)

// ProcessConfigCacheUpdater updates the STREAM_PROCESS cache for an artifact.
// Used for Livepeer → local fallback: Helmsman tells Foghorn to cache the
// local-only config so the restarted push gets it via STREAM_PROCESS.
type ProcessConfigCacheUpdater func(artifactHash, processesJSON string)

var onProcessingJobResult ProcessingJobResultHandler
var onProcessConfigCacheUpdate ProcessConfigCacheUpdater

// SetProcessingJobResultHandler registers a callback for processing job results.
func SetProcessingJobResultHandler(h ProcessingJobResultHandler) {
	onProcessingJobResult = h
}

// SetProcessConfigCacheUpdater registers the cache updater for Livepeer fallback.
func SetProcessConfigCacheUpdater(h ProcessConfigCacheUpdater) {
	onProcessConfigCacheUpdate = h
}

// processProcessingJobResult handles job completion/failure results from Helmsman.
func processProcessingJobResult(result *pb.ProcessingJobResult, nodeID string, logger logging.Logger) {
	fields := logging.Fields{
		"job_id":  result.GetJobId(),
		"status":  result.GetStatus(),
		"node_id": nodeID,
	}

	if db == nil {
		logger.WithFields(fields).Error("DB not configured for processing job result")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	jobStatus := result.GetStatus()
	switch jobStatus {
	case "cache_update":
		artifactHash := result.GetOutputs()["artifact_hash"]
		processesJSON := result.GetOutputs()["processes_json"]
		if artifactHash != "" && processesJSON != "" && onProcessConfigCacheUpdate != nil {
			onProcessConfigCacheUpdate(artifactHash, processesJSON)
			logger.WithField("artifact_hash", artifactHash).Info("Updated process config cache for Livepeer fallback")
		}
		return
	case "completed":
		var outputMeta *string
		if len(result.GetOutputs()) > 0 {
			b, _ := json.Marshal(result.GetOutputs())
			s := string(b)
			outputMeta = &s
		}
		_, err := db.ExecContext(ctx, `
			UPDATE foghorn.processing_jobs
			SET status = 'completed',
			    output_metadata = $2,
			    completed_at = NOW(),
			    updated_at = NOW()
			WHERE job_id = $1
		`, result.GetJobId(), outputMeta)
		if err != nil {
			logger.WithError(err).WithFields(fields).Error("Failed to update processing job to completed")
			return
		}
		logger.WithFields(fields).Info("Processing job completed")

		// Register processed output in warm cache + in-memory state so vod+
		// STREAM_SOURCE resolves immediately (same pattern as defrost completion).
		if outputPath := result.GetOutputPath(); outputPath != "" {
			var artifactHash, oldS3URL, oldFormat string
			_ = db.QueryRowContext(ctx, `
				SELECT artifact_hash, COALESCE(s3_url,''), COALESCE(format,'')
				FROM foghorn.processing_jobs pj
				JOIN foghorn.artifacts a ON pj.artifact_hash = a.artifact_hash
				WHERE pj.job_id = $1`, result.GetJobId()).Scan(&artifactHash, &oldS3URL, &oldFormat)
			if artifactHash != "" {
				sizeBytes := result.GetOutputSizeBytes()
				newFormat := strings.TrimPrefix(filepath.Ext(outputPath), ".")

				if artifactRepo != nil {
					if err := artifactRepo.AddCachedNodeWithPath(ctx, artifactHash, nodeID, outputPath, sizeBytes); err != nil {
						logger.WithError(err).WithFields(fields).Warn("Failed to register processed artifact in warm cache")
					}
				}
				state.DefaultManager().AddNodeArtifact(nodeID, &pb.StoredArtifact{
					ClipHash:  artifactHash,
					FilePath:  outputPath,
					SizeBytes: uint64(sizeBytes),
					CreatedAt: time.Now().Unix(),
					Format:    newFormat,
				})

				// Update artifact format to match processed output and reset sync
				// status so the processed file gets synced to S3. Keep the original
				// upload URL in s3_url until the replacement upload is durably synced.
				if _, dbErr := db.ExecContext(ctx, `
						UPDATE foghorn.artifacts
						SET format = $1,
						    sync_status = 'pending',
						    storage_location = 'local',
						    updated_at = NOW()
						WHERE artifact_hash = $2`, newFormat, artifactHash); dbErr != nil {
					logger.WithError(dbErr).WithField("artifact_hash", artifactHash).Error("failed to update artifact format after processing")
				}

				logger.WithFields(logging.Fields{
					"artifact_hash": artifactHash,
					"node_id":       nodeID,
					"output_path":   outputPath,
					"old_format":    oldFormat,
					"new_format":    newFormat,
				}).Info("Registered processed artifact for immediate playback")
			}
		}

	case "failed":
		_, err := db.ExecContext(ctx, `
			UPDATE foghorn.processing_jobs
			SET status = 'failed',
			    error_message = $2,
			    updated_at = NOW()
			WHERE job_id = $1
		`, result.GetJobId(), result.GetError())
		if err != nil {
			logger.WithError(err).WithFields(fields).Error("Failed to update processing job to failed")
			return
		}
		logger.WithFields(fields).WithField("error", result.GetError()).Warn("Processing job failed")

	default:
		logger.WithFields(fields).Warn("Unknown processing job result status")
		return
	}

	// Notify pipeline handler
	if onProcessingJobResult != nil {
		onProcessingJobResult(ctx, result.GetJobId(), jobStatus, result.GetOutputs(), result.GetError())
	}
}

// processProcessingJobProgress handles periodic progress updates from Helmsman.
// Refreshes updated_at (preventing stale recovery) and emits lifecycle events.
func processProcessingJobProgress(progress *pb.ProcessingJobProgress, logger logging.Logger) {
	if db == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	progressPct := progress.GetProgressPct()

	// Update job progress and refresh updated_at so stale recovery doesn't requeue
	var artifactHash sql.NullString
	var tenantID string
	err := db.QueryRowContext(ctx, `
		UPDATE foghorn.processing_jobs
		SET progress = $2, updated_at = NOW()
		WHERE job_id = $1 AND status IN ('dispatched', 'processing')
		RETURNING artifact_hash, tenant_id::text
	`, progress.GetJobId(), progressPct).Scan(&artifactHash, &tenantID)
	if err != nil {
		if err != sql.ErrNoRows {
			logger.WithError(err).WithField("job_id", progress.GetJobId()).Warn("Failed to update processing job progress")
		}
		return
	}

	// Emit VodLifecycleData with progress for Periscope
	if decklogClient != nil && artifactHash.Valid {
		vodData := &pb.VodLifecycleData{
			Status:      pb.VodLifecycleData_STATUS_PROCESSING,
			VodHash:     artifactHash.String,
			ProgressPct: &progressPct,
		}
		if tenantID != "" {
			vodData.TenantId = &tenantID
		}
		go func() {
			if err := decklogClient.SendVodLifecycle(vodData); err != nil {
				logger.WithError(err).Warn("Failed to send processing progress lifecycle event")
			}
		}()
	}
}

func SendLocalProcessingJob(nodeID string, req *pb.ProcessingJobRequest) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &pb.ControlMessage{
		Payload: &pb.ControlMessage_ProcessingJobRequest{ProcessingJobRequest: req},
		SentAt:  timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

// SendProcessingJob sends a ProcessingJobRequest to the given node, relaying via HA if needed.
func SendProcessingJob(nodeID string, req *pb.ProcessingJobRequest) error {
	err := SendLocalProcessingJob(nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	return relayFailure(err, commandRelay.forward(context.Background(), &pb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &pb.ForwardCommandRequest_ProcessingJob{ProcessingJob: req},
	}))
}

// GeneratePresignedGETForArtifact generates a presigned GET URL for an artifact's S3 object.
// The s3URL should be the full S3 URL (s3://bucket/key) stored in foghorn.artifacts.
func GeneratePresignedGETForArtifact(_ context.Context, s3URL string) (string, error) {
	if s3Client == nil {
		return "", fmt.Errorf("s3 client not configured")
	}
	// Extract key from s3:// URL — the S3 client's GeneratePresignedGET expects a key
	key := s3URL
	if strings.HasPrefix(s3URL, "s3://") {
		// s3://bucket/key -> key (bucket is configured on the client)
		parts := strings.SplitN(s3URL[5:], "/", 2)
		if len(parts) == 2 {
			key = parts[1]
		}
	}
	return s3Client.GeneratePresignedGET(key, 1*time.Hour)
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Look up stream info from foghorn.artifacts
	var streamName string
	err := db.QueryRowContext(ctx, `
		SELECT stream_internal_name
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
		rpcCtx, rpcCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer rpcCancel()
		switch assetType {
		case "clip":
			if resp, err := CommodoreClient.ResolveClipHash(rpcCtx, assetHash); err == nil && resp.Found {
				tenantID = resp.TenantId
			}
		case "dvr":
			if resp, err := CommodoreClient.ResolveDVRHash(rpcCtx, assetHash); err == nil && resp.Found {
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
	return requestDefrost(ctx, assetType, assetHash, nodeID, timeout, logger, false, "", nil)
}

// StartRemoteDefrost initiates a defrost using presigned URLs provided by a
// remote (origin) cluster. Use this instead of StartDefrost when the artifact
// lives in a different S3 bucket that the local s3Client cannot access.
func StartRemoteDefrost(ctx context.Context, assetType, assetHash, nodeID string, timeout time.Duration, logger logging.Logger, remoteURL string, remoteSegmentURLs map[string]string) (string, error) {
	return requestDefrost(ctx, assetType, assetHash, nodeID, timeout, logger, false, remoteURL, remoteSegmentURLs)
}

func requestDefrost(ctx context.Context, assetType, assetHash, nodeID string, timeout time.Duration, logger logging.Logger, wait bool, remoteURL string, remoteSegmentURLs map[string]string) (string, error) {
	useRemoteURLs := remoteURL != "" || len(remoteSegmentURLs) > 0
	if !useRemoteURLs && s3Client == nil {
		return "", fmt.Errorf("s3 client not configured")
	}
	if db == nil {
		return "", fmt.Errorf("database not available")
	}

	artifactType := assetType

	// Look up asset info from foghorn.artifacts
	var streamName, storageLocation, format, tenantID string
	var s3Key, filename, streamID, artifactInternalName sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT a.stream_internal_name,
		       COALESCE(a.storage_location, 'local'),
		       COALESCE(a.format, ''),
		       COALESCE(a.tenant_id::text, ''),
		       COALESCE(v.s3_key, ''),
		       COALESCE(v.filename, ''),
		       a.stream_id::text,
		       a.internal_name
		FROM foghorn.artifacts a
		LEFT JOIN foghorn.vod_metadata v ON a.artifact_hash = v.artifact_hash
		WHERE a.artifact_hash = $1 AND a.artifact_type = $2`,
		assetHash, artifactType).Scan(&streamName, &storageLocation, &format, &tenantID, &s3Key, &filename, &streamID, &artifactInternalName)
	if err != nil {
		return "", fmt.Errorf("asset not found: %w", err)
	}

	// Prefer denormalized tenant_id stored in foghorn.artifacts; fall back to Commodore when absent.
	if tenantID == "" {
		if CommodoreClient != nil {
			switch artifactType {
			case "clip":
				if resp, errResolve := CommodoreClient.ResolveClipHash(ctx, assetHash); errResolve == nil && resp.Found {
					tenantID = resp.TenantId
				}
			case "dvr":
				if resp, errResolve := CommodoreClient.ResolveDVRHash(ctx, assetHash); errResolve == nil && resp.Found {
					tenantID = resp.TenantId
				}
			case "vod":
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

	result, err := db.ExecContext(ctx, `
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
		err := db.QueryRowContext(ctx, `
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

	// Generate presigned GET URLs (or use remote URLs from origin cluster)
	expiry := 30 * time.Minute
	requestID := fmt.Sprintf("defrost-%s-%d", assetHash, time.Now().UnixNano())

	req := &pb.DefrostRequest{
		RequestId:          requestID,
		AssetType:          assetType,
		AssetHash:          assetHash,
		TenantId:           tenantID,
		StreamInternalName: streamName,
		InternalName:       artifactInternalName.String,
		TimeoutSeconds:     int32(timeout.Seconds()),
		UrlExpirySeconds:   int64(expiry.Seconds()),
	}

	storageBase := storageBasePathForNode(nodeID)

	if useRemoteURLs {
		// Remote defrost: use presigned URLs supplied by the origin cluster
		if artifactType == "dvr" {
			req.SegmentUrls = remoteSegmentURLs
			req.Streaming = true
			dvrStreamID := ""
			if CommodoreClient != nil {
				if resp, err := CommodoreClient.ResolveDVRHash(ctx, assetHash); err == nil && resp.Found {
					dvrStreamID = resp.StreamId
				}
			}
			if dvrStreamID == "" && streamID.Valid && streamID.String != "" {
				dvrStreamID = streamID.String
			}
			if dvrStreamID == "" {
				return "", fmt.Errorf("could not resolve stream_id for DVR asset")
			}
			req.LocalPath = filepath.Join(storageBase, "dvr", dvrStreamID, assetHash)
		} else {
			req.PresignedGetUrl = remoteURL
			f := format
			if f == "" {
				f = "mp4"
			}
			switch artifactType {
			case "clip":
				req.LocalPath = filepath.Join(storageBase, "clips", streamName, fmt.Sprintf("%s.%s", assetHash, f))
			case "vod":
				req.LocalPath = filepath.Join(storageBase, "vod", fmt.Sprintf("%s.%s", assetHash, f))
			default:
				return "", fmt.Errorf("unsupported asset type for remote defrost: %s", assetType)
			}
		}
	} else if artifactType == "clip" {
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
			segName := segKey
			if idx := strings.LastIndex(segKey, "/"); idx != -1 {
				segName = segKey[idx+1:]
			}
			req.SegmentUrls[segName] = presignedURL
		}

		dvrStreamID := ""
		if CommodoreClient != nil {
			if resp, err := CommodoreClient.ResolveDVRHash(ctx, assetHash); err == nil && resp.Found {
				dvrStreamID = resp.StreamId
			}
		}
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
		if _, dbErr := db.ExecContext(ctx, `
			UPDATE foghorn.artifacts
			SET storage_location = 's3',
			    defrost_node_id = NULL,
			    defrost_started_at = NULL,
			    updated_at = NOW()
			WHERE artifact_hash = $1
			  AND storage_location = 'defrosting'
			  AND defrost_node_id = $2
		`, assetHash, nodeID); dbErr != nil {
			logger.WithError(dbErr).WithField("asset_hash", assetHash).Error("failed to revert artifact after defrost send failure")
		}
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
		// Check if this is a remote artifact (origin S3 has authoritative copy)
		if db != nil {
			var originCluster sql.NullString
			_ = db.QueryRowContext(context.Background(), `
				SELECT origin_cluster_id FROM foghorn.artifacts WHERE artifact_hash = $1 LIMIT 1
			`, assetHash).Scan(&originCluster)
			if originCluster.Valid && originCluster.String != "" && !isServedCluster(originCluster.String) {
				response.SafeToDelete = true
				response.Reason = "remote_synced"
				logger.WithFields(logging.Fields{
					"asset_hash":     assetHash,
					"origin_cluster": originCluster.String,
				}).Info("Remote artifact — safe to delete (origin S3 authoritative)")
				sendCanDeleteResponse(stream, response, logger)
				return
			}
		}

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

	switch status {
	case "success":
		var artifactType, internalName, format, tenantID, previousS3URL string
		// If Helmsman didn't provide s3_url (typical), compute it from stored artifact metadata.
		if db != nil {
			_ = db.QueryRowContext(ctx, `
				SELECT COALESCE(artifact_type,''), COALESCE(stream_internal_name,''), COALESCE(format,''), COALESCE(tenant_id::text,''), COALESCE(s3_url,'')
				FROM foghorn.artifacts
				WHERE artifact_hash = $1
			`, assetHash).Scan(&artifactType, &internalName, &format, &tenantID, &previousS3URL)
		}
		if s3URL == "" && s3Client != nil {
			if tenantID != "" {
				switch artifactType {
				case "clip":
					if format == "" {
						format = "mp4"
					}
					if internalName != "" {
						s3Key := s3Client.BuildClipS3Key(tenantID, internalName, assetHash, format)
						s3URL = s3Client.BuildS3URL(s3Key)
					}
				case "dvr":
					if internalName != "" {
						s3Prefix := s3Client.BuildDVRS3Key(tenantID, internalName, assetHash)
						s3URL = s3Client.BuildS3URL(s3Prefix)
					}
				case "vod":
					if format == "" {
						format = "mp4"
					}
					s3Key := s3Client.BuildVodS3Key(tenantID, assetHash, assetHash+"."+format)
					s3URL = s3Client.BuildS3URL(s3Key)
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

		// Update foghorn.artifacts with storage_location and dtsh_synced
		if _, dbErr := db.ExecContext(ctx, `
			UPDATE foghorn.artifacts
			SET storage_location = 'local',
			    s3_url = COALESCE(NULLIF($1,''), s3_url),
			    dtsh_synced = $2,
			    last_sync_attempt = NOW(),
			    sync_error = NULL,
			    updated_at = NOW()
			WHERE artifact_hash = $3
			  AND sync_status = 'synced'`,
			s3URL, dtshIncluded, assetHash); dbErr != nil {
			logger.WithError(dbErr).WithField("asset_hash", assetHash).Error("failed to mark artifact as synced")
		}

		logger.WithFields(logging.Fields{
			"asset_hash":    assetHash,
			"s3_url":        s3URL,
			"node_id":       reportingNodeID,
			"dtsh_included": dtshIncluded,
		}).Info("Asset synced to S3, local copy retained")

		if artifactType == "vod" && previousS3URL != "" && s3URL != "" && previousS3URL != s3URL && s3Client != nil {
			if err := s3Client.DeleteByURL(ctx, previousS3URL); err != nil {
				logger.WithError(err).WithFields(logging.Fields{
					"asset_hash":      assetHash,
					"replaced_s3_url": previousS3URL,
					"new_s3_url":      s3URL,
				}).Warn("Failed to delete replaced VOD source from S3 after sync")
			} else {
				logger.WithFields(logging.Fields{
					"asset_hash":      assetHash,
					"replaced_s3_url": previousS3URL,
					"new_s3_url":      s3URL,
				}).Info("Deleted replaced VOD source from S3 after sync")
			}
		}

	case "evicted_remote":
		// Remote-origin artifact: local copy was deleted, original lives on origin S3.
		// Mark as synced on S3 and remove this node from warm cache.
		if err := artifactRepo.SetSyncStatus(ctx, assetHash, "synced", ""); err != nil {
			logger.WithError(err).Error("Failed to update sync status for evicted remote")
		}

		if _, dbErr := db.ExecContext(ctx, `
			UPDATE foghorn.artifacts
			SET storage_location = 's3',
			    sync_status = 'synced',
			    last_sync_attempt = NOW(),
			    sync_error = NULL,
			    updated_at = NOW()
			WHERE artifact_hash = $1`, assetHash); dbErr != nil {
			logger.WithError(dbErr).WithField("asset_hash", assetHash).Error("failed to mark evicted artifact as s3-resident")
		}

		if _, dbErr := db.ExecContext(ctx, `
			DELETE FROM foghorn.artifact_nodes
			WHERE artifact_hash = $1 AND node_id = $2`, assetHash, reportingNodeID); dbErr != nil {
			logger.WithError(dbErr).WithFields(logging.Fields{"asset_hash": assetHash, "node_id": reportingNodeID}).Error("failed to remove cached node after eviction")
		}

		// Remove from in-memory + Redis so routing stops directing to this node
		state.DefaultManager().RemoveNodeArtifact(reportingNodeID, assetHash)

		logger.WithFields(logging.Fields{
			"asset_hash": assetHash,
			"node_id":    reportingNodeID,
		}).Info("Remote artifact evicted locally, marked as S3-resident")

	default:
		// Sync failed
		if err := artifactRepo.SetSyncStatus(ctx, assetHash, "failed", ""); err != nil {
			logger.WithError(err).Error("Failed to update sync status to failed")
		}

		if _, dbErr := db.ExecContext(ctx, `
			UPDATE foghorn.artifacts
			SET storage_location = 'local',
			    sync_status = 'failed',
			    sync_error = NULLIF($1,''),
			    last_sync_attempt = NOW(),
			    updated_at = NOW()
			WHERE artifact_hash = $2`,
			errorMsg, assetHash); dbErr != nil {
			logger.WithError(dbErr).WithField("asset_hash", assetHash).Error("failed to record sync failure")
		}

		logger.WithFields(logging.Fields{
			"asset_hash": assetHash,
			"error":      errorMsg,
		}).Warn("Asset sync to S3 failed")
	}
}

// ==================== Cert Refresh Loop ====================

const tlsStateNoCert = "<no-cert>"

var lastPushedTLSState sync.Map // connID -> tls state fingerprint (cert hash or tlsStateNoCert)

// StartCertRefreshLoop periodically re-checks TLS certificates for all connected
// Helmsman nodes and re-pushes ConfigSeed when a cert has been renewed.
func StartCertRefreshLoop(ctx context.Context, interval time.Duration, log logging.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshTLSBundles(log)
		}
	}
}

func refreshTLSBundles(log logging.Logger) {
	// Refresh the server's own TLS certificate if Navigator-backed
	if navigatorClient != nil && serverCert.cert.Load() != nil {
		rootDomain := platformRootDomain()
		domain := fmt.Sprintf("*.%s.%s", pkgdns.SanitizeLabel(localClusterID), rootDomain)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		certResp, certErr := navigatorClient.GetCertificate(ctx, &pb.GetCertificateRequest{Domain: domain})
		cancel()
		if certErr == nil && certResp != nil && certResp.GetFound() {
			cert, err := tls.X509KeyPair([]byte(certResp.GetCertPem()), []byte(certResp.GetKeyPem()))
			if err == nil {
				serverCert.cert.Store(&cert)
				log.WithField("domain", domain).Debug("Refreshed server TLS certificate from Navigator")
			}
		}
	}

	registry.mu.RLock()
	nodes := make([]struct {
		connID      string // registry key (used for SendConfigSeed)
		canonicalID string // QM-resolved node ID (used for resolveClusterTLSBundle)
		peerAddr    string
	}, 0, len(registry.conns))
	for id, c := range registry.conns {
		cid := c.canonicalID
		if cid == "" {
			cid = id
		}
		nodes = append(nodes, struct {
			connID      string
			canonicalID string
			peerAddr    string
		}{id, cid, c.peerAddr})
	}
	registry.mu.RUnlock()

	if len(nodes) == 0 {
		return
	}

	seedCaBundle := readConfiguredCABundle()

	for _, n := range nodes {
		bundle, _, err := fetchClusterTLSBundle(n.canonicalID)
		if err != nil {
			log.WithError(err).WithField("node_id", n.canonicalID).Warn("Failed to resolve TLS bundle for node")
			continue
		}

		nextState := tlsMaterialState(bundle, seedCaBundle)

		prev, ok := lastPushedTLSState.Load(n.connID)
		if prevState, isString := prev.(string); ok && isString && prevState == nextState {
			continue
		}

		mode := resolveOperationalMode(n.canonicalID, pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_UNSPECIFIED)
		seed := composeConfigSeed(n.canonicalID, nil, n.peerAddr, mode, "")
		// Override TLS with the bundle we already resolved above.
		// composeConfigSeed resolves TLS internally, which can fail
		// transiently on a second call; using the pre-resolved bundle
		// avoids pushing a seed that silently drops TLS.
		seed.Tls = bundle
		if err := SendConfigSeed(n.connID, seed); err != nil {
			log.WithError(err).WithField("node_id", n.canonicalID).Warn("Failed to push renewed TLS certificate")
			continue
		}

		lastPushedTLSState.Store(n.connID, nextState)
		if bundle == nil {
			log.WithFields(logging.Fields{
				"node_id": n.canonicalID,
				"conn_id": n.connID,
			}).Info("Removed TLS certificate from edge after navigator reported no certificate")
			continue
		}

		log.WithFields(logging.Fields{
			"node_id":    n.canonicalID,
			"conn_id":    n.connID,
			"domain":     bundle.GetDomain(),
			"expires_at": bundle.GetExpiresAt(),
		}).Info("Pushed renewed TLS certificate to edge")
	}
}

// probeEdgeActivation verifies an edge node's HTTPS endpoint is serving with a valid
// TLS certificate after ConfigSeed delivery. Retries every 5s for up to 60s.
// On success, marks the node as probe-verified so the load balancer includes it.
// On failure, closes the gRPC stream to force re-enrollment.
func probeEdgeActivation(nodeID, edgeDomain, connID string) {
	if edgeDomain == "" {
		registry.log.WithField("node_id", nodeID).Warn("No edge domain for activation probe, auto-verifying")
		state.DefaultManager().SetProbeVerified(nodeID, true)
		return
	}

	systemRoots, err := x509.SystemCertPool()
	if err != nil {
		registry.log.WithError(err).Warn("Failed to load system cert pool for activation probe, auto-verifying")
		state.DefaultManager().SetProbeVerified(nodeID, true)
		return
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    systemRoots,
				MinVersion: tls.VersionTLS12,
			},
		},
	}

	probeURL := "https://" + edgeDomain + "/"
	maxAttempts := 12
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		time.Sleep(5 * time.Second)

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, probeURL, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			registry.log.WithFields(logging.Fields{
				"node_id": nodeID, "domain": edgeDomain,
				"attempt": attempt, "error": err,
			}).Debug("Activation probe failed")
			continue
		}
		resp.Body.Close()

		// 503 = still serving bootstrap page, not activated yet
		if resp.StatusCode == http.StatusServiceUnavailable {
			registry.log.WithFields(logging.Fields{
				"node_id": nodeID, "domain": edgeDomain, "attempt": attempt,
			}).Debug("Activation probe got 503 (bootstrap), retrying")
			continue
		}

		// Any non-503 response with valid TLS = activated
		state.DefaultManager().SetProbeVerified(nodeID, true)
		registry.log.WithFields(logging.Fields{
			"node_id": nodeID, "domain": edgeDomain, "attempt": attempt,
		}).Info("Edge activation probe succeeded")
		return
	}

	// All attempts exhausted — disconnect the node so Helmsman re-enrolls
	registry.log.WithFields(logging.Fields{
		"node_id": nodeID, "domain": edgeDomain,
	}).Warn("Edge activation probe failed after all attempts, disconnecting node")

	registry.mu.Lock()
	c := registry.conns[connID]
	if c != nil {
		// Send error before removing so Helmsman knows why it was disconnected
		if err := sendControlError(c.stream, "ACTIVATION_FAILED", "edge proxy did not activate within timeout"); err != nil {
			registry.log.WithError(err).WithField("node_id", nodeID).Warn("Failed to send activation failure to node")
		}
		delete(registry.conns, connID)
		if nodeID != connID {
			if cc, ok := registry.conns[nodeID]; ok && cc.stream == c.stream {
				delete(registry.conns, nodeID)
			}
		}
	}
	registry.mu.Unlock()

	if c != nil {
		state.DefaultManager().MarkNodeDisconnected(connID)
		if nodeID != connID {
			state.DefaultManager().MarkNodeDisconnected(nodeID)
		}
	}
}

func fetchClusterTLSBundle(nodeID string) (*pb.TLSCertBundle, bool, error) {
	if quartermasterClient == nil || navigatorClient == nil {
		return nil, false, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	node, err := quartermasterClient.GetNodeByLogicalName(ctx, nodeID)
	if err != nil {
		return nil, false, err
	}
	if node == nil || strings.TrimSpace(node.GetClusterId()) == "" {
		return nil, false, nil
	}

	rootDomain := platformRootDomain()
	domain := fmt.Sprintf("*.%s.%s", pkgdns.SanitizeLabel(node.GetClusterId()), rootDomain)

	certResp, certErr := navigatorClient.GetCertificate(ctx, &pb.GetCertificateRequest{Domain: domain})
	if certErr != nil {
		return nil, false, certErr
	}
	if certResp == nil || !certResp.GetFound() {
		if certResp != nil && certResp.GetError() != "" {
			return nil, false, fmt.Errorf("navigator: %s", certResp.GetError())
		}
		return nil, false, nil
	}

	return &pb.TLSCertBundle{
		CertPem:   certResp.GetCertPem(),
		KeyPem:    certResp.GetKeyPem(),
		Domain:    certResp.GetDomain(),
		ExpiresAt: certResp.GetExpiresAt(),
	}, true, nil
}

func tlsBundleState(bundle *pb.TLSCertBundle) string {
	return tlsMaterialState(bundle, nil)
}

func tlsMaterialState(bundle *pb.TLSCertBundle, caBundle []byte) string {
	if bundle == nil && len(caBundle) == 0 {
		return tlsStateNoCert
	}
	payload := make([]byte, 0, len(caBundle)+128)
	if bundle != nil {
		payload = append(payload, bundle.GetCertPem()...)
		payload = append(payload, '\x00')
		payload = append(payload, bundle.GetKeyPem()...)
		payload = append(payload, '\x00')
		payload = append(payload, bundle.GetDomain()...)
		payload = fmt.Appendf(payload, "\x00%d", bundle.GetExpiresAt())
	}
	payload = append(payload, '\x00')
	payload = append(payload, caBundle...)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func readConfiguredCABundle() []byte {
	caPath := strings.TrimSpace(os.Getenv("GRPC_TLS_CA_PATH"))
	if caPath == "" {
		return nil
	}
	caBundle, err := os.ReadFile(caPath)
	if err != nil {
		logging.NewLogger().WithError(err).WithField("path", caPath).Warn("Failed to read configured gRPC CA bundle")
		return nil
	}
	if len(caBundle) == 0 {
		return nil
	}
	return caBundle
}

// ==================== Edge Provisioning (PreRegisterEdge) ====================

type EdgeProvisioningServer struct {
	pb.UnimplementedEdgeProvisioningServiceServer
}

func RegisterEdgeProvisioningService(srv *grpc.Server) {
	pb.RegisterEdgeProvisioningServiceServer(srv, &EdgeProvisioningServer{})
}

func (s *EdgeProvisioningServer) PreRegisterEdge(ctx context.Context, req *pb.PreRegisterEdgeRequest) (*pb.PreRegisterEdgeResponse, error) {
	token := strings.TrimSpace(req.GetEnrollmentToken())
	if token == "" {
		return nil, status.Errorf(codes.InvalidArgument, "enrollment_token is required")
	}

	// Extract client IP from gRPC peer for token IP-binding validation
	var clientIP string
	if p, ok := peer.FromContext(ctx); ok {
		if host, _, err := net.SplitHostPort(p.Addr.String()); err == nil {
			clientIP = host
		}
	}

	// Validate token without consuming. PreRegisterEdge is advisory only — it
	// previews edge identity and stages TLS certs but creates no database
	// records. Consumption is deferred to BootstrapEdgeNode, which creates
	// the infrastructure_nodes record. Consuming here would burn single-use
	// tokens before Helmsman can enroll via BootstrapEdgeNode.
	validateFn := validateBootstrapTokenFn
	if validateFn == nil {
		if quartermasterClient == nil {
			return nil, status.Error(codes.Unavailable, "enrollment service unavailable")
		}
		validateFn = func(c context.Context, t string) (*pb.ValidateBootstrapTokenResponse, error) {
			return quartermasterClient.ValidateBootstrapTokenEx(c, &pb.ValidateBootstrapTokenRequest{
				Token:    t,
				ClientIp: clientIP,
				Consume:  false,
			})
		}
	}
	valCtx, valCancel := context.WithTimeout(ctx, 5*time.Second)
	defer valCancel()
	valResp, valErr := validateFn(valCtx, token)
	if valErr != nil {
		return nil, status.Errorf(codes.Unavailable, "token validation failed: %v", valErr)
	}
	if !valResp.GetValid() {
		return nil, status.Errorf(codes.Unauthenticated, "invalid enrollment token: %s", valResp.GetReason())
	}
	if valResp.GetKind() != "edge_node" {
		return nil, status.Errorf(codes.PermissionDenied, "token kind %q is not valid for edge enrollment", valResp.GetKind())
	}

	// Use token's bound cluster_id if available, otherwise fall back to env
	clusterID := valResp.GetClusterId()
	if clusterID == "" {
		clusterID = localClusterID
	}
	if clusterID == "" {
		clusterID = "default"
	}
	clusterSlug := pkgdns.SanitizeLabel(clusterID)
	if clusterSlug == "" {
		clusterSlug = "default"
	}
	AddServedCluster(clusterID)

	nodeID := normalizePreferredEdgeNodeID(req.GetPreferredNodeId())
	if nodeID == "" {
		b := make([]byte, 6)
		if _, randErr := rand.Read(b); randErr != nil {
			return nil, fmt.Errorf("generate random node ID: %w", randErr)
		}
		nodeID = hex.EncodeToString(b)
	}

	rootDomain := platformRootDomain()

	edgeDomain := fmt.Sprintf("%s.%s.%s", edgeNodeRecordLabel(nodeID), clusterSlug, rootDomain)
	poolDomain := fmt.Sprintf("edge.%s.%s", clusterSlug, rootDomain)
	foghornAddr := fmt.Sprintf("foghorn.%s.%s:18019", clusterSlug, rootDomain)

	resp := &pb.PreRegisterEdgeResponse{
		NodeId:           nodeID,
		EdgeDomain:       edgeDomain,
		PoolDomain:       poolDomain,
		ClusterSlug:      clusterSlug,
		ClusterId:        clusterID,
		FoghornGrpcAddr:  foghornAddr,
		InternalCaBundle: readConfiguredCABundle(),
		Telemetry:        buildEdgeTelemetryConfig(nodeID, clusterID, strings.TrimSpace(valResp.GetTenantId())),
	}

	return resp, nil
}

func processValidateEdgeToken(requestID string, req *pb.ValidateEdgeTokenRequest, nodeID string, stream pb.HelmsmanControl_ConnectServer, logger logging.Logger) {
	token := req.GetToken()
	resp := &pb.ValidateEdgeTokenResponse{Valid: false}

	if token == "" || CommodoreClient == nil {
		sendEdgeTokenResponse(requestID, stream, resp, logger)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	apiResp, err := CommodoreClient.ValidateAPIToken(ctx, token)
	if err != nil {
		logger.WithError(err).WithField("node_id", nodeID).Warn("Edge token validation failed")
		sendEdgeTokenResponse(requestID, stream, resp, logger)
		return
	}

	resp.Valid = apiResp.GetValid()
	resp.UserId = apiResp.GetUserId()
	resp.TenantId = apiResp.GetTenantId()
	resp.Role = apiResp.GetRole()
	resp.Permissions = apiResp.GetPermissions()

	sendEdgeTokenResponse(requestID, stream, resp, logger)
}

func sendEdgeTokenResponse(requestID string, stream pb.HelmsmanControl_ConnectServer, resp *pb.ValidateEdgeTokenResponse, logger logging.Logger) {
	msg := &pb.ControlMessage{
		RequestId: requestID,
		SentAt:    timestamppb.Now(),
		Payload:   &pb.ControlMessage_ValidateEdgeTokenResponse{ValidateEdgeTokenResponse: resp},
	}
	if err := stream.Send(msg); err != nil {
		logger.WithError(err).Warn("Failed to send edge token validation response")
	}
}

// processThumbnailUploadRequest resolves internal_name → stable ID, generates
// presigned PUT URLs for each thumbnail file, and sends them back to Helmsman.
// S3 keys use stable identifiers: stream_id (UUID) for live streams,
// artifact_hash (32-char hex) for artifacts. Never playback_id (rotatable).
func processThumbnailUploadRequest(requestID string, req *pb.ThumbnailUploadRequest, nodeID string, stream pb.HelmsmanControl_ConnectServer, logger logging.Logger) {
	internalName := req.GetInternalName()
	filePaths := req.GetFilePaths()

	logger.WithFields(logging.Fields{
		"internal_name": internalName,
		"file_count":    len(filePaths),
		"node_id":       nodeID,
	}).Info("Processing thumbnail upload request")

	if s3Client == nil {
		logger.Warn("S3 client not configured, ignoring thumbnail upload request")
		return
	}

	// Resolve internal_name → stable S3 key identifier.
	// Live streams: stream_id (UUID, never rotated).
	// Artifacts: artifact_hash (32-char hex, immutable PK).
	// The MistServer wildcard prefix ("live+" / "vod+") routes; the bare name is the lookup key.
	var thumbnailKey string
	bareName := mist.ExtractInternalName(internalName)
	switch {
	case strings.HasPrefix(internalName, "live+"):
		// In-memory StreamState is populated by PUSH_REWRITE on ingest. If foghorn restarted
		// while the stream was already live, fall back to Commodore — it owns the
		// internal_name → stream_id mapping authoritatively.
		if ss := state.DefaultManager().GetStreamState(bareName); ss != nil && ss.StreamID != "" {
			thumbnailKey = ss.StreamID
		} else if CommodoreClient != nil {
			resp, err := CommodoreClient.ResolveInternalName(context.Background(), bareName)
			if err != nil || resp == nil || resp.StreamId == "" {
				logger.WithFields(logging.Fields{
					"stream_name":   internalName,
					"internal_name": bareName,
					"error":         err,
				}).Warn("Could not resolve internal_name to stream_id for thumbnail upload")
				return
			}
			thumbnailKey = resp.StreamId
			state.DefaultManager().SetStreamStreamID(bareName, resp.StreamId)
		} else {
			logger.WithField("internal_name", bareName).Warn("Commodore client unavailable for stream_id resolution")
			return
		}
		logger.WithFields(logging.Fields{
			"stream_name":   internalName,
			"internal_name": bareName,
			"stream_id":     thumbnailKey,
		}).Info("Resolved live stream_id for thumbnail S3 key")
	case strings.HasPrefix(internalName, "vod+"):
		conn := GetDB()
		if conn == nil {
			logger.Warn("DB not available for artifact hash resolution")
			return
		}
		if err := conn.QueryRowContext(context.Background(),
			`SELECT artifact_hash FROM foghorn.artifacts WHERE internal_name = $1`,
			bareName,
		).Scan(&thumbnailKey); err != nil {
			logger.WithFields(logging.Fields{
				"stream_name":   internalName,
				"internal_name": bareName,
			}).Warn("Could not resolve internal_name to artifact_hash for thumbnail upload")
			return
		}
		logger.WithFields(logging.Fields{
			"stream_name":   internalName,
			"internal_name": bareName,
			"artifact_hash": thumbnailKey,
		}).Info("Resolved artifact hash for thumbnail S3 key")
	default:
		logger.WithField("internal_name", internalName).Warn("Thumbnail upload from unrecognised stream prefix; expected live+ or vod+")
		return
	}

	expiry := 15 * time.Minute
	resp := &pb.ThumbnailUploadResponse{
		ThumbnailKey: thumbnailKey,
		Uploads:      make([]*pb.ThumbnailUploadResponse_PresignedUpload, 0, len(filePaths)),
	}

	allowedThumbnailFiles := map[string]bool{
		"poster.jpg": true,
		"sprite.jpg": true,
		"sprite.vtt": true,
	}

	for _, fp := range filePaths {
		fileName := filepath.Base(fp)
		if !allowedThumbnailFiles[fileName] {
			logger.WithField("file_name", fileName).Warn("Rejected thumbnail filename not in allowlist")
			continue
		}
		s3Key := "thumbnails/" + thumbnailKey + "/" + fileName

		presignedURL, err := s3Client.GeneratePresignedPUT(s3Key, expiry)
		if err != nil {
			logger.WithFields(logging.Fields{
				"file_name": fileName,
				"s3_key":    s3Key,
				"error":     err,
			}).Error("Failed to generate presigned PUT URL for thumbnail")
			continue
		}
		resp.Uploads = append(resp.Uploads, &pb.ThumbnailUploadResponse_PresignedUpload{
			FileName:     fileName,
			PresignedUrl: presignedURL,
			S3Key:        s3Key,
			LocalPath:    fp,
		})
	}

	if len(resp.Uploads) == 0 {
		logger.Warn("No presigned URLs generated for thumbnail upload")
		return
	}

	msg := &pb.ControlMessage{
		RequestId: requestID,
		SentAt:    timestamppb.Now(),
		Payload:   &pb.ControlMessage_ThumbnailUploadResponse{ThumbnailUploadResponse: resp},
	}
	if err := stream.Send(msg); err != nil {
		logger.WithError(err).Error("Failed to send thumbnail upload response")
	}
}

// processThumbnailUploaded handles confirmation after Helmsman uploads thumbnail
// files to S3. For artifact thumbnails (DVR/clip), marks has_thumbnails=true.
// Stream thumbnails need no DB update — the frontend resolves them via
// deterministic Chandler URL from assetsDomain + stream_id.
func processThumbnailUploaded(req *pb.ThumbnailUploaded, nodeID string, logger logging.Logger) {
	thumbnailKey := req.GetThumbnailKey()
	s3Keys := req.GetS3Keys()

	logger.WithFields(logging.Fields{
		"thumbnail_key": thumbnailKey,
		"s3_keys":       s3Keys,
		"node_id":       nodeID,
	}).Info("Thumbnail upload confirmed")

	if isArtifactThumbnail(thumbnailKey) {
		markArtifactHasThumbnails(thumbnailKey, logger)
	}
}

// isArtifactThumbnail checks if the thumbnail key is an artifact hash.
// Artifact hashes are 32-char hex strings. Stream IDs are UUIDs (36 chars with dashes).
func isArtifactThumbnail(thumbnailKey string) bool {
	if len(thumbnailKey) != 32 {
		return false
	}
	for _, c := range thumbnailKey {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// markArtifactHasThumbnails sets has_thumbnails=true on the artifact after sprite upload.
func markArtifactHasThumbnails(artifactHash string, logger logging.Logger) {
	conn := GetDB()
	if conn == nil {
		logger.Warn("DB not available, cannot mark artifact thumbnails")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := conn.ExecContext(ctx, `UPDATE foghorn.artifacts SET has_thumbnails = true, updated_at = NOW() WHERE artifact_hash = $1`, artifactHash)
	if err != nil {
		logger.WithFields(logging.Fields{
			"artifact_hash": artifactHash,
			"error":         err,
		}).Error("Failed to mark artifact has_thumbnails")
		return
	}
	logger.WithField("artifact_hash", artifactHash).Info("Artifact thumbnails marked as uploaded")
}

// getChandlerBaseURL returns the Chandler base URL from environment.
func getChandlerBaseURL() string {
	chandlerBase := strings.TrimSpace(os.Getenv("CHANDLER_BASE_URL"))
	if chandlerBase != "" {
		return chandlerBase
	}
	if cached := cachedChandlerBaseURL(); cached != "" {
		return cached
	}
	if derived := resolvePlatformChandlerBaseURL(); derived != "" {
		cacheChandlerBaseURL(derived)
		return derived
	}
	if chandlerBase == "" {
		chandlerHost := strings.TrimSpace(os.Getenv("CHANDLER_HOST"))
		chandlerPort := strings.TrimSpace(os.Getenv("CHANDLER_PORT"))
		if chandlerHost == "" {
			chandlerHost = "chandler"
		}
		if chandlerPort == "" {
			chandlerPort = "18020"
		}
		chandlerBase = "http://" + chandlerHost + ":" + chandlerPort
	}
	return chandlerBase
}
