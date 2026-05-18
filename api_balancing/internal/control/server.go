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

	"frameworks/api_balancing/internal/artifactoutbox"
	"frameworks/api_balancing/internal/ingesterrors"
	"frameworks/api_balancing/internal/state"
	"frameworks/api_balancing/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	navclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/navigator"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/geoip"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"

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
	stream       pb.HelmsmanControl_ConnectServer
	last         time.Time
	peerAddr     string
	canonicalID  string // node ID after fingerprint/enrollment resolution (may differ from registry key)
	clusterID    string
	relayBaseURL string // base URL Mist on this node uses to reach Helmsman's /internal/artifact/* relay
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
	case *pb.ForwardCommandRequest_InvalidateSessions:
		return "invalidate_sessions"
	case *pb.ForwardCommandRequest_ProcessingJob:
		return "processing_job"
	case *pb.ForwardCommandRequest_Freeze:
		return "freeze"
	case *pb.ForwardCommandRequest_DesiredStateUpdate:
		return "desired_state_update"
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

// SetDB sets the database connection for clip operations. The in-memory
// defrost tracker is hydrated separately via BootstrapDefrostTracker, called
// once at process startup from the binary's main after the DB is wired and
// before serving traffic — never from SetDB, because that path is also used
// by tests that wire mock DBs and don't expect a stray query.
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
		SELECT sca.cluster_id
		FROM quartermaster.service_cluster_assignments sca
		JOIN quartermaster.service_instances si ON si.id = sca.service_instance_id
		JOIN quartermaster.services svc ON svc.service_id = si.service_id
		WHERE si.instance_id = $1
		  AND svc.type = 'foghorn'
		  AND si.status = 'running'
		  AND sca.is_active = true
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
			registry.conns[nodeID] = &conn{
				stream:       stream,
				last:         time.Now(),
				peerAddr:     peerAddr,
				relayBaseURL: strings.TrimRight(x.Register.GetRelayBaseUrl(), "/"),
			}
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
					c.clusterID = clusterID
					registry.conns[canonicalNodeID] = c
				}
				registry.mu.Unlock()

				if rs := GetRedisStore(); rs != nil {
					if err := rs.SetConnOwner(context.Background(), canonicalNodeID, GetInstanceID(), GetAdvertiseAddr()); err != nil {
						registry.log.WithError(err).WithField("node_id", canonicalNodeID).Warn("Failed to set canonical conn owner in Redis")
					}
				}
			}
			registry.mu.Lock()
			if c, ok := registry.conns[nodeID]; ok {
				c.clusterID = clusterID
			}
			if c, ok := registry.conns[canonicalNodeID]; ok {
				c.clusterID = clusterID
			}
			registry.mu.Unlock()

			// Determine operational mode: DB-persisted wins over Helmsman's request
			operationalMode := resolveOperationalMode(canonicalNodeID, x.Register.GetRequestedMode())
			seed := composeConfigSeed(canonicalNodeID, x.Register.GetRoles(), peerAddr, operationalMode, clusterID)
			if tenantID != "" {
				seed.TenantId = tenantID
			}
			stripWildcardSiteWithoutTLS(seed)
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
			registry.log.WithFields(logging.Fields{
				"node_id":       nodeID,
				"internal_name": x.DvrStartRequest.GetInternalName(),
			}).Error("Rejected DVRStartRequest from edge control stream; DVR starts must use Foghorn StartDVR")
		case *pb.ControlMessage_DvrProgress:
			// Handle DVR progress updates from storage Helmsman
			go processDVRProgress(x.DvrProgress, nodeID, registry.log)
		case *pb.ControlMessage_DvrStopped:
			// Handle DVR completion from storage Helmsman
			go processDVRStopped(x.DvrStopped, nodeID, registry.log)
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
		case *pb.ControlMessage_RelayResolveRequest:
			// Read-through relay resolution: sidecar wants presigned URLs +
			// chapter refs for an asset it's about to serve over
			// /internal/artifact/*. Same control-stream pattern as CanDelete.
			go processRelayResolveRequest(x.RelayResolveRequest, nodeID, stream, registry.log)
		case *pb.ControlMessage_SyncComplete:
			// Handle sync completion from Helmsman (dual-storage architecture)
			go processSyncComplete(x.SyncComplete, nodeID, registry.log)
		case *pb.ControlMessage_ModeChangeRequest:
			go processModeChangeRequest(x.ModeChangeRequest, nodeID, stream, registry.log)
		case *pb.ControlMessage_UpdateApplyResult:
			go processUpdateApplyResult(x.UpdateApplyResult, nodeID, registry.log)
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
		case *pb.ControlMessage_RecordDvrSegmentRequest:
			go processRecordDVRSegment(x.RecordDvrSegmentRequest, nodeID, stream, registry.log)
		case *pb.ControlMessage_MarkDvrSegmentUploaded:
			go processMarkDVRSegmentUploaded(x.MarkDvrSegmentUploaded, nodeID, registry.log)
		case *pb.ControlMessage_DvrSegmentDropped:
			go processDVRSegmentDropped(x.DvrSegmentDropped, nodeID, registry.log)
		case *pb.ControlMessage_EvictableSegmentsRequest:
			go processEvictableSegmentsRequest(x.EvictableSegmentsRequest, nodeID, stream, registry.log)
		case *pb.ControlMessage_RestoreLocalSegmentIndexRequest:
			go processRestoreLocalSegmentIndexRequest(x.RestoreLocalSegmentIndexRequest, nodeID, stream, registry.log)
		case *pb.ControlMessage_ConfigSeedApplyResult:
			if x.ConfigSeedApplyResult != nil {
				ack := x.ConfigSeedApplyResult
				canonicalID := nodeID
				clusterID := ""
				registry.mu.RLock()
				if c := registry.conns[nodeID]; c != nil {
					if c.canonicalID != "" {
						canonicalID = c.canonicalID
					}
					clusterID = c.clusterID
				}
				registry.mu.RUnlock()
				go func(a *pb.ConfigSeedApplyResult, canonical, resolvedClusterID string) {
					ackClusterID := resolvedClusterID
					if ackClusterID == "" && quartermasterClient != nil && canonical != "" {
						lookupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
						defer cancel()
						if resp, err := quartermasterClient.GetNode(lookupCtx, canonical); err == nil && resp.GetNode() != nil {
							ackClusterID = resp.GetNode().GetClusterId()
						}
					}
					reportApplyResultToNavigator(a, canonical, ackClusterID, registry.log)
				}(ack, canonicalID, clusterID)
			}
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

// ServiceRegistrar is a function that registers additional gRPC services
type ServiceRegistrar func(srv *grpc.Server)

// GRPCServerConfig contains configuration for starting the control gRPC server
type GRPCServerConfig struct {
	Addr         string
	Logger       logging.Logger
	ServiceToken string
	JWTSecret    string
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

	nodeControlMethods := []string{
		pb.NodeControlService_SetNodeOperationalMode_FullMethodName,
		pb.NodeControlService_GetNodeHealth_FullMethodName,
	}

	if cfg.ServiceToken != "" {
		skipMethods := []string{
			"/grpc.health.v1.Health/Check",
			"/grpc.health.v1.Health/Watch",
			// HelmsmanControl uses bootstrap token validated in-method
			pb.HelmsmanControl_Connect_FullMethodName,
			// EdgeProvisioning uses enrollment token validated in-method
			"/foghorn.EdgeProvisioningService/PreRegisterEdge",
		}
		if strings.TrimSpace(cfg.JWTSecret) != "" {
			skipMethods = append(skipMethods, nodeControlMethods...)
		}
		authInterceptor := middleware.GRPCAuthInterceptor(middleware.GRPCAuthConfig{
			ServiceToken: cfg.ServiceToken,
			Logger:       cfg.Logger,
			SkipMethods:  skipMethods,
		})
		unaryInterceptors = append([]grpc.UnaryServerInterceptor{authInterceptor}, unaryInterceptors...)
	}
	if cfg.ServiceToken != "" || strings.TrimSpace(cfg.JWTSecret) != "" {
		nodeAuth := nodeControlAuthInterceptor(cfg.ServiceToken, cfg.JWTSecret, cfg.Logger)
		unaryInterceptors = append([]grpc.UnaryServerInterceptor{nodeAuth}, unaryInterceptors...)
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

func nodeControlAuthInterceptor(serviceToken, jwtSecret string, logger logging.Logger) grpc.UnaryServerInterceptor {
	protected := map[string]bool{
		pb.NodeControlService_SetNodeOperationalMode_FullMethodName: true,
		pb.NodeControlService_GetNodeHealth_FullMethodName:          true,
	}
	serviceToken = strings.TrimSpace(serviceToken)
	jwtSecret = strings.TrimSpace(jwtSecret)
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

func handleArtifactDeleted(deleted *pb.ArtifactDeleted, nodeID string, logger logging.Logger) {
	artifactHash := deleted.GetArtifactHash()
	reason := deleted.GetReason()

	logger.WithFields(logging.Fields{
		"artifact_hash": artifactHash,
		"reason":        reason,
		"node_id":       nodeID,
	}).Info("Artifact deleted on node")

	if err := state.DefaultManager().ApplyArtifactDeleted(streamCtx(), artifactHash, nodeID); err != nil {
		logger.WithError(err).WithField("artifact_hash", artifactHash).Warn("Failed to apply artifact deletion to stream state")
	}
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

	// Sidecar reports its local view; Foghorn drives the canonical state
	// machine through FinalizeDVR(). The "stopped" alias maps to "completed"
	// for the new state machine; "deleted" passes through unchanged so the
	// retention cleanup path still works.
	if status == "deleted" {
		if applyErr := state.DefaultManager().ApplyDVRStopped(streamCtx(), dvrHash, "deleted", int64(durationSeconds), uint64(sizeBytes), manifestPath, errorMsg, storageNodeID); applyErr != nil {
			logger.WithError(applyErr).WithField("dvr_hash", dvrHash).Warn("ApplyDVRStopped(deleted) failed")
		}
		if dvrDeletedHandler != nil {
			go dvrDeletedHandler(dvrHash, uint64(sizeBytes), storageNodeID)
		}
		return
	}

	// Drive the idempotent finalize transition. FinalizeDVR retries bounded
	// pending uploads, classifies any remaining gaps, closes the current
	// chapter as VOD, and transitions the artifact to a terminal state. The
	// sidecar's status field here is advisory; Foghorn's classification is
	// authoritative.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		final, err := FinalizeDVR(ctx, dvrHash, FinalizeOptions{
			ReportedStatus:  status,
			ReportedError:   errorMsg,
			DurationSeconds: int64(durationSeconds),
			SizeBytes:       uint64(sizeBytes),
			StorageNodeID:   storageNodeID,
		})
		if err != nil {
			if final.ArtifactStatus == "" {
				logger.WithError(err).WithField("dvr_hash", dvrHash).Error("FinalizeDVR failed")
				return
			}
			logger.WithError(err).WithFields(logging.Fields{
				"dvr_hash":     dvrHash,
				"final_status": final.ArtifactStatus,
			}).Warn("FinalizeDVR completed with follow-up error")
		}
		if applyErr := state.DefaultManager().ApplyDVRStopped(streamCtx(), dvrHash, final.ArtifactStatus, int64(durationSeconds), uint64(sizeBytes), final.ManifestPath, errorMsg, storageNodeID); applyErr != nil {
			logger.WithError(applyErr).WithField("dvr_hash", dvrHash).Warn("ApplyDVRStopped after FinalizeDVR failed")
		}
		if dvrStoppedHandler != nil {
			go dvrStoppedHandler(dvrHash, final.ArtifactStatus, storageNodeID, uint64(sizeBytes), final.ManifestPath, errorMsg)
		}
	}()
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

// BuildDTSCURI returns a full DTSC URI for a Mist stream on a node.
// streamName must include the Mist prefix (e.g. "live+<internal_name>",
// "dvr+<dvr_internal_name>") — the prefix is meaningful to Mist's input
// routing on the pulling node and this function is prefix-agnostic.
func BuildDTSCURI(nodeID, streamName string, logger logging.Logger) string {
	base := GetDTSCBase(nodeID, logger)
	if base == "" || streamName == "" {
		return ""
	}
	base = strings.TrimSuffix(base, "/")
	return base + "/" + streamName
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
			Def:   &pb.StreamDef{Name: "live", Realtime: false, StopSessions: false, Tags: []string{"live"}},
			Roles: []string{"ingest", "edge"},
			Caps:  []string{"ingest", "edge"},
		},
		{
			Id:    "vod",
			Def:   &pb.StreamDef{Name: "vod", Realtime: false, StopSessions: false, Tags: []string{"vod"}},
			Roles: []string{"edge", "storage"},
			Caps:  []string{"edge", "storage"},
		},
		{
			Id:    "dvr",
			Def:   &pb.StreamDef{Name: "dvr", Realtime: false, StopSessions: false, Tags: []string{"dvr"}},
			Roles: []string{"edge", "storage"},
			Caps:  []string{"edge", "storage"},
		},
		{
			Id:    "processing",
			Def:   &pb.StreamDef{Name: "processing", Realtime: true, StopSessions: false, Tags: []string{"processing"}},
			Roles: []string{"edge", "storage"},
			Caps:  []string{"processing"},
		},
		{
			Id:    "pull",
			Def:   &pb.StreamDef{Name: "pull", Realtime: false, StopSessions: false, Tags: []string{"pull"}},
			Roles: []string{"edge"},
			Caps:  []string{"edge"},
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
	var isPlatformOfficial bool
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
					CertPem:       certResp.GetCertPem(),
					KeyPem:        certResp.GetKeyPem(),
					Domain:        certResp.GetDomain(),
					ExpiresAt:     certResp.GetExpiresAt(),
					BundleId:      "cluster:" + slug,
					SiteAddresses: []string{wildcardDomain},
				}
			}
		}

		// Resolve cluster kind to decide whether to distribute the
		// platform-edge multi-SAN cert. Only platform_official clusters
		// receive it; third-party / marketplace / tenant-private edges
		// are excluded for trust-boundary reasons.
		if quartermasterClient != nil {
			cCtx, cCancel := context.WithTimeout(context.Background(), 5*time.Second)
			resp, cErr := quartermasterClient.GetCluster(cCtx, resolvedClusterID)
			cCancel()
			if cErr == nil && resp != nil && resp.GetCluster() != nil {
				isPlatformOfficial = resp.GetCluster().GetIsPlatformOfficial()
			}
		}
	}

	caBundle := readConfiguredCABundle()
	telemetry := buildEdgeTelemetryConfig(nodeID, resolvedClusterID, ownerTenantID)

	seed := &pb.ConfigSeed{
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
		SeedVersion:         nextSeedVersion(nodeID),
	}
	if tlsBundle != nil {
		seed.TlsBundles = []*pb.TLSCertBundle{tlsBundle}
	}
	if isPlatformOfficial {
		if extra := fetchPlatformEdgeBundle(); extra != nil {
			seed.TlsBundles = append(seed.TlsBundles, extra)
		}
	}
	// Per-tenant TLS bundles: for every paying tenant subscribed to this
	// cluster, include their *.{tenant}.cdn.{root} cert. Best-effort;
	// missing certs (still pending issuance) are skipped silently and
	// reconciled on the next cycle.
	seed.TlsBundles = append(seed.TlsBundles, fetchTenantBundles(resolvedClusterID)...)
	return seed
}

// fetchTenantBundles queries Quartermaster for the paying tenants
// subscribed to clusterID, then pulls each tenant's TLS bundle from
// Navigator. Returns only bundles that exist (cert issuance complete).
// Bundles for tenants still in cert_issuing state are skipped.
func fetchTenantBundles(clusterID string) []*pb.TLSCertBundle {
	if clusterID == "" || quartermasterClient == nil || navigatorClient == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := quartermasterClient.ListAliasedTenantsForCluster(ctx, clusterID)
	if err != nil || resp == nil || len(resp.GetTenants()) == 0 {
		return nil
	}
	rootDomain := platformRootDomain()
	tenantZoneLabel := pkgdns.TenantAliasZoneLabel

	out := make([]*pb.TLSCertBundle, 0, len(resp.GetTenants()))
	for _, ref := range resp.GetTenants() {
		bundleID := "tenant:" + ref.GetTenantId()
		certCtx, certCancel := context.WithTimeout(context.Background(), 5*time.Second)
		certResp, certErr := navigatorClient.GetTLSBundle(certCtx, &pb.GetTLSBundleRequest{BundleId: bundleID})
		certCancel()
		if certErr != nil || certResp == nil || !certResp.GetFound() {
			continue
		}
		apex := ref.GetSubdomain() + "." + tenantZoneLabel + "." + rootDomain
		out = append(out, &pb.TLSCertBundle{
			CertPem:       certResp.GetCertPem(),
			KeyPem:        certResp.GetKeyPem(),
			Domain:        apex,
			ExpiresAt:     certResp.GetExpiresAt(),
			BundleId:      bundleID,
			SiteAddresses: []string{apex, "*." + apex},
		})
	}
	return out
}

// fetchPlatformEdgeBundle pulls the platform-edge multi-SAN cert from
// Navigator. Returns nil if Navigator is unavailable or the cert hasn't
// been issued yet. Caller is responsible for deciding which nodes
// receive this bundle (only platform_official cluster edges).
func fetchPlatformEdgeBundle() *pb.TLSCertBundle {
	if navigatorClient == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := navigatorClient.GetTLSBundle(ctx, &pb.GetTLSBundleRequest{
		BundleId: "platform:edge-multi",
	})
	if err != nil || resp == nil || !resp.GetFound() {
		return nil
	}
	rootDomain := platformRootDomain()
	return &pb.TLSCertBundle{
		CertPem:       resp.GetCertPem(),
		KeyPem:        resp.GetKeyPem(),
		Domain:        strings.Join(resp.GetDomains(), ","),
		ExpiresAt:     resp.GetExpiresAt(),
		BundleId:      "platform:edge-multi",
		SiteAddresses: platformEdgeSiteAddresses(rootDomain),
	}
}

// platformEdgeSiteAddresses returns the 5 hostnames the platform-edge
// multi-SAN cert covers. Helmsman renders one Caddy site block bound
// to these names.
func platformEdgeSiteAddresses(rootDomain string) []string {
	return []string{
		"edge." + rootDomain,
		"edge-ingest." + rootDomain,
		"edge-egress." + rootDomain,
		"edge-storage." + rootDomain,
		"edge-processing." + rootDomain,
	}
}

// FoghornBalancerBase is the exported entry-point for callers outside this
// package (e.g. the trigger handler returning balance: URIs from STREAM_SOURCE
// for pull+ streams).
func FoghornBalancerBase(clusterID string) string {
	return foghornBalancerBase(clusterID)
}

// foghornBalancerBase returns the public HTTP base URL Helmsman should use for
// MistServer's balance:<base> source. Runtime cluster state wins: edge nodes get
// their cluster-scoped Foghorn DNS name. Env overrides are fallback escape
// hatches for non-managed deployments.
func foghornBalancerBase(clusterID string) string {
	if v := strings.TrimSpace(os.Getenv("FOGHORN_PUBLIC_BASE")); v != "" {
		return v
	}
	if isLocalBuildEnv() {
		if v := strings.TrimSpace(os.Getenv("FOGHORN_URL")); v != "" {
			return v
		}
		if h := strings.TrimSpace(os.Getenv("FOGHORN_HOST")); h != "" {
			return fmt.Sprintf("http://%s:18008", h)
		}
	}
	if clusterID != "" {
		rootDomain := platformRootDomain()
		clusterSlug := pkgdns.SanitizeLabel(clusterID)
		if clusterSlug != "" && rootDomain != "" {
			if fqdn, ok := pkgdns.ServiceFQDN("foghorn", clusterSlug+"."+rootDomain); ok && fqdn != "" {
				return "https://" + fqdn
			}
		}
	}
	if v := strings.TrimSpace(os.Getenv("FOGHORN_URL")); v != "" {
		return v
	}
	if h := strings.TrimSpace(os.Getenv("FOGHORN_HOST")); h != "" {
		return fmt.Sprintf("https://%s:18008", h)
	}
	return "http://foghorn:18008"
}

func isLocalBuildEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BUILD_ENV"))) {
	case "dev", "development", "local", "test":
		return true
	default:
		return false
	}
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

func SendDesiredStateUpdate(nodeID string, update *pb.DesiredStateUpdate) error {
	err := SendLocalDesiredStateUpdate(nodeID, update)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil || update == nil {
		return ErrNotConnected
	}
	return relayFailure(err, commandRelay.forward(context.Background(), &pb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &pb.ForwardCommandRequest_DesiredStateUpdate{DesiredStateUpdate: update},
	}))
}

func SendLocalDesiredStateUpdate(nodeID string, update *pb.DesiredStateUpdate) error {
	if update == nil {
		return fmt.Errorf("nil DesiredStateUpdate")
	}
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	return c.stream.Send(&pb.ControlMessage{
		Payload: &pb.ControlMessage_DesiredStateUpdate{DesiredStateUpdate: update},
		SentAt:  timestamppb.Now(),
	})
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

func processUpdateApplyResult(result *pb.UpdateApplyResult, fallbackNodeID string, log logging.Logger) {
	if result == nil {
		return
	}
	nodeID := strings.TrimSpace(fallbackNodeID)
	payloadNodeID := strings.TrimSpace(result.GetNodeId())
	if nodeID == "" {
		nodeID = payloadNodeID
	}
	if nodeID == "" {
		return
	}
	if payloadNodeID != "" && fallbackNodeID != "" && payloadNodeID != fallbackNodeID {
		if log != nil {
			log.WithFields(logging.Fields{
				"stream_node_id":  fallbackNodeID,
				"payload_node_id": payloadNodeID,
			}).Warn("Rejected node update apply result for a different stream identity")
		}
		return
	}
	success := true
	sawComponent := false
	var details []string
	expectedVersions := make(map[string]string)
	for _, component := range result.GetComponents() {
		if component == nil {
			continue
		}
		sawComponent = true
		if !component.GetSuccess() {
			success = false
		}
		if component.GetDetail() != "" {
			details = append(details, fmt.Sprintf("%s: %s", component.GetComponent(), component.GetDetail()))
		}
		if component.GetSuccess() {
			name := strings.ToLower(strings.TrimSpace(component.GetComponent()))
			version := strings.TrimSpace(component.GetVersion())
			if name != "" {
				expectedVersions[name] = version
			}
		}
	}
	phase := "idle"
	lastError := ""
	targetRelease := strings.TrimSpace(result.GetTargetRelease())
	updateState, foundUpdateState, updateStateErr := currentNodeUpdateState(nodeID)
	if updateStateErr != nil {
		if log != nil {
			log.WithError(updateStateErr).WithField("node_id", nodeID).Warn("Rejected node update apply result because update state could not be loaded")
		}
		return
	}
	if !foundUpdateState || updateState.TargetRelease == "" || targetRelease == "" || targetRelease != updateState.TargetRelease || !updatePhaseAcceptsApplyResult(updateState.Phase) {
		if log != nil {
			log.WithFields(logging.Fields{
				"node_id":                nodeID,
				"result_target_release":  targetRelease,
				"current_target_release": updateState.TargetRelease,
				"current_phase":          updateState.Phase,
				"state_found":            foundUpdateState,
			}).Warn("Rejected node update apply result without matching persisted update state")
		}
		return
	}
	if !sawComponent || !success {
		phase = "failed"
		lastError = strings.Join(details, "; ")
		if lastError == "" && !sawComponent {
			lastError = "no component results"
		}
	} else if updateResultIncludesMist(result) && updatePhaseNeedsMistWarmup(updateState.Phase) {
		phase = "warming"
		if updatePhaseRestoresRouting(updateState.Phase) {
			phase = "warming_restore"
		}
		if err := persistNodeUpdateStateWithDeadlineAndExpected(nodeID, targetRelease, phase, "", time.Now().Add(90*time.Second), expectedVersions); err != nil && log != nil {
			log.WithError(err).WithField("node_id", nodeID).Warn("Failed to persist node update warmup phase")
		}
		go completeUpdateWarmup(nodeID, targetRelease, expectedVersions, time.Now(), log)
		if log != nil {
			log.WithFields(logging.Fields{
				"node_id":        nodeID,
				"target_release": targetRelease,
				"phase":          phase,
			}).Info("Processed node update apply result")
		}
		return
	} else if updatePhaseRestoresRouting(updateState.Phase) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := state.DefaultManager().SetNodeOperationalMode(ctx, nodeID, state.NodeModeNormal, "update-orchestrator"); err != nil && log != nil {
			log.WithError(err).WithField("node_id", nodeID).Warn("Failed to return node to normal mode after update")
		}
		cancel()
		if err := PushOperationalMode(nodeID, pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL); err != nil && log != nil {
			log.WithError(err).WithField("node_id", nodeID).Warn("Failed to push normal mode after update")
		}
	}
	if err := persistNodeUpdateState(nodeID, targetRelease, phase, lastError); err != nil && log != nil {
		log.WithError(err).WithField("node_id", nodeID).Warn("Failed to persist node update result")
	}
	if log != nil {
		log.WithFields(logging.Fields{
			"node_id":        nodeID,
			"target_release": targetRelease,
			"phase":          phase,
		}).Info("Processed node update apply result")
	}
}

func updateResultIncludesMist(result *pb.UpdateApplyResult) bool {
	for _, component := range result.GetComponents() {
		if component != nil && strings.EqualFold(strings.TrimSpace(component.GetComponent()), "mist") {
			return true
		}
	}
	return false
}

func completeUpdateWarmup(nodeID, targetRelease string, expectedVersions map[string]string, notBefore time.Time, log logging.Logger) {
	deadline := time.Now().Add(90 * time.Second)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		current, found, err := currentNodeUpdateState(nodeID)
		if err != nil {
			persistNodeUpdateStateWithLog(nodeID, targetRelease, "failed", err.Error(), log, "Failed to persist node update warmup state lookup failure")
			if log != nil {
				log.WithError(err).WithField("node_id", nodeID).Warn("Failed to load node update warmup state")
			}
			return
		}
		if !found {
			if log != nil {
				log.WithField("node_id", nodeID).Warn("Stopped node update warmup because update state is missing")
			}
			return
		}
		if current.TargetRelease != "" && current.TargetRelease != targetRelease {
			if log != nil {
				log.WithFields(logging.Fields{
					"node_id":                nodeID,
					"warmup_target_release":  targetRelease,
					"current_target_release": current.TargetRelease,
				}).Warn("Stopped node update warmup after target changed")
			}
			return
		}
		if ok, reason, err := CompleteUpdateWarmupIfReady(context.Background(), nodeID, targetRelease, expectedVersions, notBefore, log); err != nil {
			fenceNodeAfterUpdateWarmupFailure(nodeID, log)
			persistNodeUpdateStateWithLog(nodeID, targetRelease, "failed", err.Error(), log, "Failed to persist node update warmup failure")
			if log != nil {
				log.WithError(err).WithField("node_id", nodeID).Warn("Failed to complete node update warmup")
			}
			return
		} else if ok {
			return
		} else if log != nil {
			log.WithFields(logging.Fields{
				"node_id": nodeID,
				"reason":  reason,
			}).Debug("Node update warmup probe not ready")
		}
		if time.Now().After(deadline) {
			fenceNodeAfterUpdateWarmupFailure(nodeID, log)
			persistNodeUpdateStateWithLog(nodeID, targetRelease, "failed", "warmup probe timed out", log, "Failed to persist node update warmup timeout")
			if log != nil {
				log.WithField("node_id", nodeID).Warn("Node update warmup probe timed out")
			}
			return
		}
		<-ticker.C
	}
}

func fenceNodeAfterUpdateWarmupFailure(nodeID string, log logging.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := state.DefaultManager().SetNodeOperationalMode(ctx, nodeID, state.NodeModeMaintenance, "update-orchestrator"); err != nil && log != nil {
		log.WithError(err).WithField("node_id", nodeID).Warn("Failed to fence node after update warmup failure")
	}
	if err := PushOperationalMode(nodeID, pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_MAINTENANCE); err != nil && log != nil {
		log.WithError(err).WithField("node_id", nodeID).Warn("Failed to push maintenance mode after update warmup failure")
	}
}

// CompleteUpdateWarmupIfReady completes warmup once health, version reporting,
// and the warmup endpoint all confirm the applied release.
func CompleteUpdateWarmupIfReady(ctx context.Context, nodeID, targetRelease string, expectedVersions map[string]string, notBefore time.Time, log logging.Logger) (bool, string, error) {
	current, found, err := currentNodeUpdateState(nodeID)
	if err != nil {
		return false, "", err
	}
	if !found {
		return false, "update state missing", nil
	}
	if current.TargetRelease != "" && targetRelease != "" && current.TargetRelease != targetRelease {
		return false, "target release changed", nil
	}
	if ok, reason := nodeWarmupReady(nodeID, expectedVersions, notBefore); !ok {
		return false, reason, nil
	}
	if updatePhaseRestoresRouting(current.Phase) {
		setCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := state.DefaultManager().SetNodeOperationalMode(setCtx, nodeID, state.NodeModeNormal, "update-orchestrator"); err != nil {
			return false, "", err
		}
		if err := PushOperationalMode(nodeID, pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL); err != nil {
			return false, "", err
		}
	}
	if err := persistNodeUpdateState(nodeID, targetRelease, "idle", ""); err != nil {
		return false, "", err
	}
	if log != nil {
		log.WithFields(logging.Fields{
			"node_id":        nodeID,
			"target_release": targetRelease,
		}).Info("Completed node update warmup")
	}
	return true, "", nil
}

func nodeWarmupReady(nodeID string, expectedVersions map[string]string, notBefore time.Time) (bool, string) {
	node := state.DefaultManager().GetNodeState(nodeID)
	if node == nil {
		return false, "node state missing"
	}
	if !node.IsHealthy || node.IsStale {
		return false, "node health not fresh"
	}
	if !node.LastHeartbeat.After(notBefore) && !node.LastUpdate.After(notBefore) {
		return false, "fresh heartbeat pending"
	}
	if ok, reason := expectedComponentVersionsReported(nodeID, expectedVersions); !ok {
		return false, reason
	}
	if err := probeWarmupEndpoint(node.BaseURL); err != nil {
		return false, err.Error()
	}
	return true, ""
}

func expectedComponentVersionsReported(nodeID string, expected map[string]string) (bool, string) {
	if len(expected) == 0 {
		return false, "component version confirmation missing"
	}
	if db == nil {
		return false, "component version database unavailable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for component, version := range expected {
		if strings.TrimSpace(version) == "" {
			return false, fmt.Sprintf("%s result version missing", component)
		}
		var current string
		err := db.QueryRowContext(ctx, `
			SELECT COALESCE(current_version, '')
			FROM foghorn.node_components
			WHERE node_id = $1 AND component = $2
		`, nodeID, component).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Sprintf("%s version not reported", component)
		}
		if err != nil {
			return false, fmt.Sprintf("read %s version: %v", component, err)
		}
		if strings.TrimSpace(current) != version {
			return false, fmt.Sprintf("%s version %q pending", component, version)
		}
	}
	return true, ""
}

func probeWarmupEndpoint(baseURL string) error {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return fmt.Errorf("node base URL missing")
	}
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL, nil)
	if err != nil {
		return fmt.Errorf("build warmup probe: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("warmup endpoint probe failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode >= http.StatusInternalServerError {
		return fmt.Errorf("warmup endpoint returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func persistNodeUpdateStateWithLog(nodeID, targetRelease, phase, lastError string, log logging.Logger, message string) {
	if err := persistNodeUpdateState(nodeID, targetRelease, phase, lastError); err != nil && log != nil {
		log.WithError(err).WithField("node_id", nodeID).Warn(message)
	}
}

func persistNodeUpdateState(nodeID, targetRelease, phase, lastError string) error {
	return persistNodeUpdateStateWithDeadline(nodeID, targetRelease, phase, lastError, time.Time{})
}

func persistNodeUpdateStateWithDeadline(nodeID, targetRelease, phase, lastError string, deadline time.Time) error {
	return persistNodeUpdateStateWithDeadlineAndExpected(nodeID, targetRelease, phase, lastError, deadline, nil)
}

func persistNodeUpdateStateWithDeadlineAndExpected(nodeID, targetRelease, phase, lastError string, deadline time.Time, expected map[string]string) error {
	if db == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deadlineArg := any(nil)
	if !deadline.IsZero() {
		deadlineArg = deadline
	}
	expectedArg := any(nil)
	if len(expected) > 0 {
		encoded, err := json.Marshal(expected)
		if err != nil {
			return err
		}
		expectedArg = string(encoded)
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO foghorn.node_update_state (node_id, target_release, phase, deadline, expected_components, last_error, updated_at)
		VALUES ($1, NULLIF($2, ''), $3, $5, COALESCE($6::jsonb, '{}'::jsonb), NULLIF($4, ''), NOW())
		ON CONFLICT (node_id) DO UPDATE SET
			target_release = EXCLUDED.target_release,
			phase = EXCLUDED.phase,
			deadline = EXCLUDED.deadline,
			expected_components = CASE
				WHEN $6::jsonb IS NULL THEN foghorn.node_update_state.expected_components
				ELSE EXCLUDED.expected_components
			END,
			last_error = EXCLUDED.last_error,
			updated_at = NOW()
	`, nodeID, targetRelease, phase, lastError, deadlineArg, expectedArg)
	return err
}

type nodeUpdateProgress struct {
	TargetRelease string
	Phase         string
}

func currentNodeUpdateState(nodeID string) (nodeUpdateProgress, bool, error) {
	if db == nil || strings.TrimSpace(nodeID) == "" {
		return nodeUpdateProgress{}, false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var progress nodeUpdateProgress
	err := db.QueryRowContext(ctx, `
		SELECT COALESCE(target_release, ''), phase
		FROM foghorn.node_update_state
		WHERE node_id = $1
	`, nodeID).Scan(&progress.TargetRelease, &progress.Phase)
	if errors.Is(err, sql.ErrNoRows) {
		return nodeUpdateProgress{}, false, nil
	}
	if err != nil {
		return nodeUpdateProgress{}, false, err
	}
	return progress, true, nil
}

func updatePhaseRestoresRouting(phase string) bool {
	switch phase {
	case "cordoning", "draining", "drained", "updating_restore", "warming_restore":
		return true
	default:
		return false
	}
}

func updatePhaseNeedsMistWarmup(phase string) bool {
	switch phase {
	case "cordoning", "draining", "drained", "updating", "updating_restore", "warming", "warming_restore":
		return true
	default:
		return false
	}
}

func updatePhaseAcceptsApplyResult(phase string) bool {
	switch phase {
	case "updating", "updating_restore", "warming", "warming_restore":
		return true
	default:
		return false
	}
}

// S3ClientInterface defines the storage operations used by freeze, defrost,
// sync, cleanup, and DVR chapter materialization.
type S3ClientInterface interface {
	GeneratePresignedPUT(key string, expiry time.Duration) (string, error)
	GeneratePresignedGET(key string, expiry time.Duration) (string, error)
	PutObject(ctx context.Context, key string, body []byte, contentType string) error
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

// SetS3Client sets the S3 client for cold storage operations.
func SetS3Client(client S3ClientInterface) {
	s3Client = client
}

// Storage delegation wiring. Set once at startup from cmd/foghorn/main.go;
// nil-safe defaults fall back to "always local mint" for tests and for
// deployments running without federation enabled.
var (
	storageResolverFactory func(ctx context.Context, tenantID string) *storage.ClusterResolver
	storageMintDelegate    StorageMintDelegate
	storageDeleteDelegate  StorageDeleteDelegate
)

// StorageMintDelegate sends a MintStorageURLs request to the Foghorn pool
// that owns the named storage cluster's S3. Wired from main.go to the
// federation client + peer manager pair; absent in tests or when
// federation isn't enabled.
type StorageMintDelegate func(ctx context.Context, targetClusterID string, req *pb.MintStorageURLsRequest) (*pb.MintStorageURLsResponse, error)

// StorageDeleteDelegate sends a DeleteStorageObjects request to the
// Foghorn pool that owns the named storage cluster's S3. Wired from
// main.go to the federation client + peer manager pair; absent in tests
// or when federation isn't enabled. Cleanup paths fall back to a clear
// "remote storage cleanup pending" when the delegate is nil so we don't
// accidentally delete against the wrong bucket.
type StorageDeleteDelegate func(ctx context.Context, targetClusterID string, req *pb.DeleteStorageObjectsRequest) (*pb.DeleteStorageObjectsResponse, error)

// SetStorageResolverFactory wires the per-request storage cluster resolver
// factory. Called once at startup.
func SetStorageResolverFactory(f func(ctx context.Context, tenantID string) *storage.ClusterResolver) {
	storageResolverFactory = f
}

// SetStorageMintDelegate wires the cross-cluster MintStorageURLs sender
// used when the resolver picks StorageMintViaFederation. Called once at
// startup; absent ⇒ federation mode falls back to a clear reject so we
// don't accidentally local-mint against the wrong S3 backing.
func SetStorageMintDelegate(d StorageMintDelegate) {
	storageMintDelegate = d
}

// SetStorageDeleteDelegate wires the cross-cluster DeleteStorageObjects
// sender used by cleanup paths when an artifact's storage_cluster_id
// points to a peer cluster. Called once at startup.
func SetStorageDeleteDelegate(d StorageDeleteDelegate) {
	storageDeleteDelegate = d
}

// GetStorageDeleteDelegate returns the wired delegate (nil when
// federation isn't enabled). Cleanup helpers consume it via this
// accessor to keep the package boundary thin and testable.
func GetStorageDeleteDelegate() StorageDeleteDelegate {
	return storageDeleteDelegate
}

// resolveOfficialClusterID returns the tenant's official cluster per
// Quartermaster.GetClusterRouting. Cached for officialClusterCacheTTL.
// Returns "" on RPC failure or when the tenant has no official cluster —
// the storage resolver treats an empty slot as missing-candidate, not a
// fatal error.
const officialClusterCacheTTL = 60 * time.Second

var officialClusterCache = cache.New(cache.Options{
	TTL:                  officialClusterCacheTTL,
	StaleWhileRevalidate: 0,
	NegativeTTL:          5 * time.Second,
	MaxEntries:           10000,
}, cache.MetricsHooks{})

// resolveFreezeStorageCluster runs the storage resolver for the freeze
// flow. Origin candidate is the artifact row's origin_cluster_id.
// Official candidate comes from Quartermaster.GetClusterRouting via the
// cached helper. When no resolver factory is wired (tests / minimal dev
// setups) falls back to (origin, StorageMintLocal).
func resolveFreezeStorageCluster(ctx context.Context, tenantID, originClusterID string) (string, storage.StorageMintMode) {
	if storageResolverFactory == nil {
		return originClusterID, storage.StorageMintLocal
	}
	resolver := storageResolverFactory(ctx, tenantID)
	if resolver == nil {
		return originClusterID, storage.StorageMintLocal
	}
	return resolver.Resolve(storage.ResolverInput{
		OriginClusterID:   originClusterID,
		OfficialClusterID: resolveOfficialClusterID(ctx, tenantID),
	})
}

// resolveThumbnailStorageCluster runs the storage resolver for the
// thumbnail upload flow. Origin candidate is the artifact / live stream's
// authoritative cluster; official candidate comes from the cached
// Quartermaster lookup. Mirrors resolveFreezeStorageCluster's fallback
// behaviour when no factory is wired (tests / minimal dev setups).
func resolveThumbnailStorageCluster(ctx context.Context, tenantID, originClusterID string) (string, storage.StorageMintMode) {
	if storageResolverFactory == nil {
		return originClusterID, storage.StorageMintLocal
	}
	resolver := storageResolverFactory(ctx, tenantID)
	if resolver == nil {
		return originClusterID, storage.StorageMintLocal
	}
	return resolver.Resolve(storage.ResolverInput{
		OriginClusterID:   originClusterID,
		OfficialClusterID: resolveOfficialClusterID(ctx, tenantID),
	})
}

// thumbnailContentType maps an allowlisted thumbnail filename to the
// MIME type the federated mint should record on the presigned PUT.
func thumbnailContentType(fileName string) string {
	switch fileName {
	case "poster.jpg", "sprite.jpg":
		return "image/jpeg"
	case "sprite.vtt":
		return "text/vtt"
	}
	return "application/octet-stream"
}

// buildFreezeMintRequest constructs the MintStorageURLs request that
// matches the local-mint code paths' S3 key shapes for each freeze asset
// type. Returns nil for unsupported asset types so the caller can reject
// with a clear reason.
func buildFreezeMintRequest(assetType, assetHash, tenantID, requestingCluster, targetCluster, localPath string) *pb.MintStorageURLsRequest {
	base := &pb.MintStorageURLsRequest{
		TenantId:          tenantID,
		RequestingCluster: requestingCluster,
		TargetClusterId:   targetCluster,
	}
	switch assetType {
	case "clip":
		format := "mp4"
		if idx := strings.LastIndex(localPath, "."); idx != -1 {
			format = localPath[idx+1:]
		}
		base.ArtifactType = "clip"
		base.ArtifactKey = assetHash
		base.Op = pb.MintStorageURLsRequest_OPERATION_PUT_SINGLE
		base.ContentType = "video/" + format
		return base
	case "vod":
		format := "mp4"
		if idx := strings.LastIndex(localPath, "."); idx != -1 {
			format = localPath[idx+1:]
		}
		base.ArtifactType = "vod"
		base.ArtifactKey = assetHash
		base.Op = pb.MintStorageURLsRequest_OPERATION_PUT_SINGLE
		base.ContentType = "video/" + format
		return base
	}
	return nil
}

// lookupAuthoritativeClusterUnambiguous reads COALESCE(storage_cluster_id,
// origin_cluster_id) for an artifact hash. CanDeleteRequest does not carry
// tenant_id, so to avoid letting a same-hash row from a different tenant
// influence the can-delete shortcut we only return an answer when exactly
// one row matches the hash. Returns (cluster, true) on the unambiguous
// single-row case; (_, false) if zero rows, multiple rows, or DB error.
func lookupAuthoritativeClusterUnambiguous(ctx context.Context, artifactHash string, logger logging.Logger) (string, bool) {
	if db == nil {
		return "", false
	}
	rows, err := db.QueryContext(ctx, `
		SELECT COALESCE(storage_cluster_id, origin_cluster_id)
		FROM foghorn.artifacts
		WHERE artifact_hash = $1
	`, artifactHash)
	if err != nil {
		logger.WithError(err).WithField("asset_hash", artifactHash).Warn("authoritative-cluster lookup failed")
		return "", false
	}
	defer rows.Close()
	var first sql.NullString
	count := 0
	for rows.Next() {
		var cluster sql.NullString
		if scanErr := rows.Scan(&cluster); scanErr != nil {
			logger.WithError(scanErr).WithField("asset_hash", artifactHash).Warn("authoritative-cluster scan failed")
			return "", false
		}
		if count == 0 {
			first = cluster
		}
		count++
		if count > 1 {
			break
		}
	}
	if err := rows.Err(); err != nil {
		logger.WithError(err).WithField("asset_hash", artifactHash).Warn("authoritative-cluster row iteration failed")
		return "", false
	}
	if count != 1 {
		if count > 1 {
			logger.WithField("asset_hash", artifactHash).Warn("authoritative-cluster lookup ambiguous (multiple tenant rows for hash); skipping remote-synced shortcut")
		}
		return "", false
	}
	if !first.Valid {
		return "", true
	}
	return first.String, true
}

// persistFreezeStorageCluster updates the artifact row's storage_cluster_id
// after a federated mint. The UPDATE is scoped by (artifact_hash, tenant_id)
// — storage ownership is a tenant-scoped attribute and a missing tenant
// filter would let a same-hash row in a different tenant get rewritten.
// NULL is preserved when the chosen storage cluster matches origin so rows
// without a storage redirect look unchanged.
func persistFreezeStorageCluster(ctx context.Context, artifactHash, tenantID, storageCluster string) {
	if db == nil || strings.TrimSpace(artifactHash) == "" || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(storageCluster) == "" {
		return
	}
	var artifactType string
	err := db.QueryRowContext(ctx, `
		UPDATE foghorn.artifacts
		SET storage_cluster_id = $3,
		    updated_at = NOW()
		WHERE artifact_hash = $1
		  AND tenant_id = $2
		  AND COALESCE(storage_cluster_id, '') <> $3
		RETURNING artifact_type
	`, artifactHash, tenantID, storageCluster).Scan(&artifactType)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Row already at this cluster — no work, no notify needed.
			return
		}
		// Soft failure — the upload still works, the read side just
		// can't reconstruct the storage cluster from the row.
		controlLogger().WithError(err).WithFields(logging.Fields{
			"artifact_hash":   artifactHash,
			"tenant_id":       tenantID,
			"storage_cluster": storageCluster,
		}).Warn("persistFreezeStorageCluster: UPDATE failed; storage cluster may be stale on read side")
		return
	}
	notifyCommodoreStorageCluster(ctx, artifactHash, tenantID, artifactType, storageCluster)
}

// notifyCommodoreStorageCluster pushes a storage cluster ownership change
// to Commodore's registry projection. UpdateArtifactStorageCluster never
// flips has_thumbnails — that's the readiness RPC.
func notifyCommodoreStorageCluster(ctx context.Context, artifactHash, tenantID, artifactType, storageCluster string) {
	if CommodoreClient == nil || tenantID == "" {
		return
	}
	assetType, ok := artifactAssetTypeFromString(artifactType)
	if !ok {
		return
	}
	notifyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := CommodoreClient.UpdateArtifactStorageCluster(notifyCtx, tenantID, assetType, artifactHash, storageCluster); err != nil {
		controlLogger().WithError(err).WithFields(logging.Fields{
			"artifact_hash":   artifactHash,
			"tenant_id":       tenantID,
			"storage_cluster": storageCluster,
			"asset_type":      artifactType,
		}).Warn("Failed to notify Commodore of artifact storage cluster change")
	}
}

func resolveOfficialClusterID(ctx context.Context, tenantID string) string {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" || quartermasterClient == nil {
		return ""
	}
	v, ok, err := officialClusterCache.Get(ctx, "official:"+tenantID, func(loadCtx context.Context, _ string) (interface{}, bool, error) {
		rctx, cancel := context.WithTimeout(loadCtx, 1*time.Second)
		defer cancel()
		routing, qErr := quartermasterClient.GetClusterRouting(rctx, &pb.GetClusterRoutingRequest{TenantId: tenantID})
		if qErr != nil {
			return "", false, qErr
		}
		if routing == nil || routing.OfficialClusterId == nil {
			return "", true, nil
		}
		return *routing.OfficialClusterId, true, nil
	})
	if err != nil || !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
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

	// Note: the s3Client nil-check is deferred until after the storage
	// resolver runs. A self-host pool with no local S3 client must still
	// be able to delegate to the platform pool's S3 via federation;
	// rejecting up front would foreclose that path.

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if assetType == "dvr" || assetType == "dvr_segment" || assetType == "dvr_manifest" {
		sendFreezePermissionResponse(stream, &pb.FreezePermissionResponse{
			RequestId: requestID,
			AssetHash: assetHash,
			Approved:  false,
			Reason:    "dvr_freeze_unsupported",
		}, logger)
		return
	}

	lookupHash := assetHash
	lookupType := assetType

	var streamName string
	var originCluster sql.NullString
	var syncStatus sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT stream_internal_name, origin_cluster_id, sync_status
		FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND artifact_type = $2`,
		lookupHash, lookupType).Scan(&streamName, &originCluster, &syncStatus)

	// Resolve tenant (and stream/origin if DB row was missing) via Commodore.
	// origin_cluster_id is required by the storage resolver's origin-first
	// rule: a self-hosted origin with its own S3 should be preferred over
	// the official cluster, but only if we know which cluster that is.
	var tenantID string
	var commodoreOrigin string
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
				commodoreOrigin = resp.OriginClusterId
			}
		case "vod":
			if resp, resolveErr := CommodoreClient.ResolveVodHash(resolveCtx, assetHash); resolveErr == nil && resp.Found {
				tenantID = resp.TenantId
				if streamName == "" {
					streamName = resp.InternalName
				}
				commodoreOrigin = resp.OriginClusterId
			}
		}
	}
	if commodoreOrigin != "" && !originCluster.Valid {
		originCluster = sql.NullString{String: commodoreOrigin, Valid: true}
	}

	// If DB row was missing but Commodore resolved the artifact, create the
	// lifecycle row. Persist origin_cluster_id so the storage resolver can
	// honor origin-first on subsequent freezes for this asset.
	if err != nil && tenantID != "" && streamName != "" {
		insertOrigin := sql.NullString{String: commodoreOrigin, Valid: commodoreOrigin != ""}
		if _, dbErr := db.ExecContext(ctx, `
			INSERT INTO foghorn.artifacts
				(artifact_hash, artifact_type, stream_internal_name, tenant_id,
				 origin_cluster_id, storage_location, sync_status, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, 'local', 'pending', NOW(), NOW())
			ON CONFLICT (artifact_hash) DO NOTHING`,
			lookupHash, lookupType, streamName, tenantID, insertOrigin); dbErr != nil {
			logger.WithError(dbErr).WithField("asset_hash", lookupHash).Error("failed to create lifecycle row from Commodore")
		}
		logger.WithFields(logging.Fields{
			"asset_hash":        lookupHash,
			"asset_type":        lookupType,
			"tenant_id":         tenantID,
			"origin_cluster_id": commodoreOrigin,
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

	// Resolve the storage cluster for this asset using the same chain
	// CreateVodUpload uses: origin artifact row, tenant routing, then this
	// Foghorn's process cluster. The chosen
	// cluster decides local-mint vs federated-mint vs reject; it also
	// drives the storage_cluster_id we persist below for read-side
	// reconstruction.
	originClusterID := ""
	if originCluster.Valid {
		originClusterID = originCluster.String
	}
	storageCluster, mintMode := resolveFreezeStorageCluster(ctx, tenantID, originClusterID)

	// Remote artifact: storage cluster is authoritative — skip upload,
	// just evict. Replaces the prior origin_cluster_id-only check so a
	// row with delegated storage routes to the storage cluster, not origin.
	// NULL storage_cluster_id falls back to origin via the resolver's
	// behaviour for rows created before storage_cluster_id was populated.
	if storageCluster != "" && storageCluster != localClusterID && !isServedCluster(storageCluster) {
		logger.WithFields(logging.Fields{
			"asset_hash":      assetHash,
			"storage_cluster": storageCluster,
			"origin_cluster":  originClusterID,
		}).Info("Remote artifact — skip_upload=true (storage cluster's S3 authoritative)")
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

	// Branch on the resolver verdict. Local mode keeps the existing
	// per-type s3Client paths below. Federation mode delegates the mint
	// to the Foghorn pool that owns storageCluster's S3. Unavailable
	// rejects with a structured reason so the operator can act.
	switch mintMode {
	case storage.StorageUnavailable:
		sendFreezePermissionResponse(stream, &pb.FreezePermissionResponse{
			RequestId: requestID,
			AssetHash: assetHash,
			Approved:  false,
			Reason:    "service_unavailable",
		}, logger)
		return

	case storage.StorageMintViaFederation:
		if storageMintDelegate == nil {
			logger.WithField("storage_cluster", storageCluster).Warn("Federated mint required but no delegate wired")
			sendFreezePermissionResponse(stream, &pb.FreezePermissionResponse{
				RequestId: requestID,
				AssetHash: assetHash,
				Approved:  false,
				Reason:    "peer_unreachable",
			}, logger)
			return
		}
		mintReq := buildFreezeMintRequest(assetType, assetHash, tenantID, localClusterID, storageCluster, localPath)
		if mintReq == nil {
			sendFreezePermissionResponse(stream, &pb.FreezePermissionResponse{
				RequestId: requestID,
				AssetHash: assetHash,
				Approved:  false,
				Reason:    "unsupported_asset_type",
			}, logger)
			return
		}
		mintResp, mintErr := storageMintDelegate(ctx, storageCluster, mintReq)
		if mintErr != nil || mintResp == nil || !mintResp.GetAccepted() {
			reason := "peer_unreachable"
			if mintResp != nil && mintResp.GetReason() != "" {
				reason = mintResp.GetReason()
			}
			logger.WithError(mintErr).WithFields(logging.Fields{
				"asset_hash":      assetHash,
				"storage_cluster": storageCluster,
				"reason":          reason,
			}).Warn("Federated MintStorageURLs rejected freeze")
			sendFreezePermissionResponse(stream, &pb.FreezePermissionResponse{
				RequestId: requestID,
				AssetHash: assetHash,
				Approved:  false,
				Reason:    reason,
			}, logger)
			return
		}
		if mintResp.GetPresignedPutUrl() != "" {
			response.PresignedPutUrl = mintResp.GetPresignedPutUrl()
		}
		if len(mintResp.GetSegmentUrls()) > 0 {
			response.SegmentUrls = mintResp.GetSegmentUrls()
		}
		persistFreezeStorageCluster(ctx, lookupHash, tenantID, storageCluster)
		sendFreezePermissionResponse(stream, response, logger)
		return
	}

	// StorageMintLocal requires a configured local S3 client; federation
	// minting above uses the origin cluster's storage surface instead.
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

	switch assetType {
	case "clip":
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
	case "vod":
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

	if _, dbErr := db.ExecContext(context.Background(), `UPDATE foghorn.artifacts SET storage_location = 'freezing', sync_status = 'in_progress', updated_at = NOW() WHERE artifact_hash = $1`, assetHash); dbErr != nil {
		logger.WithError(dbErr).WithField("asset_hash", assetHash).Error("failed to mark artifact as freezing")
	}

	// Persist storage_cluster_id when the resolver picked a cluster other
	// than origin so the read side can reconstruct the right Chandler /
	// PrepareArtifact target.
	if storageCluster != "" && storageCluster != originClusterID {
		persistFreezeStorageCluster(context.Background(), lookupHash, tenantID, storageCluster)
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
		// Update artifact storage location in database. Reset failure_count
		// so a later eviction + restore can use the full retry budget again.
		if _, dbErr := db.ExecContext(ctx, `
				UPDATE foghorn.artifacts
				SET storage_location = 'local',
				    sync_status = 'synced',
				    s3_url = NULLIF($1, ''),
				    frozen_at = NOW(),
			    last_sync_attempt = NOW(),
			    sync_error = NULL,
			    failure_count = 0,
			    updated_at = NOW()
			WHERE artifact_hash = $2`,
			s3URL, assetHash); dbErr != nil {
			logger.WithError(dbErr).WithField("asset_hash", assetHash).Error("failed to update artifact after successful freeze")
		}
	} else {
		// Distinguish "local file is gone" (terminal lost_local — no retry, no
		// S3 cleanup needed) from a transient failure that should be retried.
		newSyncStatus := "failed"
		if complete.GetLocalMissing() {
			newSyncStatus = "lost_local"
		}
		// failure_count drives the retry-budget + exponential-backoff in
		// retryFailed. We only increment for transient failures — lost_local
		// is terminal, so leaving the counter alone is fine.
		// status='failed' on lost_local pairs with sync_status='lost_local' as
		// the tombstone marker: playback / billing / cleanup-pressure paths
		// already exclude status='failed', so the row is discoverable in admin
		// listings without being treated as a usable asset.
		if _, dbErr := db.ExecContext(ctx, `
			UPDATE foghorn.artifacts
			SET storage_location = 'local',
			    sync_status = $3,
			    status = CASE WHEN $3 = 'lost_local' THEN 'failed' ELSE status END,
			    sync_error = NULLIF($1,''),
			    last_sync_attempt = NOW(),
			    failure_count = CASE WHEN $3 = 'failed' THEN failure_count + 1 ELSE failure_count END,
			    updated_at = NOW()
			WHERE artifact_hash = $2
		`, errorMsg, assetHash, newSyncStatus); dbErr != nil {
			logger.WithError(dbErr).WithField("asset_hash", assetHash).Error("failed to revert artifact after freeze failure")
		}
		// lost_local is terminal — skip the partial-S3-cleanup branch since
		// nothing was uploaded.
		if complete.GetLocalMissing() {
			logger.WithFields(logging.Fields{
				"asset_hash": assetHash,
				"node_id":    nodeID,
			}).Warn("Artifact marked lost_local: local source file is gone before sync; will not retry")
			return
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
	reason := complete.GetReason()

	// Decrement the per-node in-flight count on terminal status only.
	// DVR sends status="ready" once the local manifest is written and
	// playback can start, but continues downloading segments — counting
	// that as a terminal would let PickDefrostNode pile more defrosts on
	// a node still doing disk/network work. "success" and "failed" are
	// the actual terminals.
	reportingNodeID := complete.GetNodeId()
	if reportingNodeID == "" {
		reportingNodeID = nodeID
	}
	if status == "success" || status == "failed" {
		DecrementDefrost(reportingNodeID)
	}

	logger.WithFields(logging.Fields{
		"request_id": requestID,
		"asset_hash": assetHash,
		"status":     status,
		"local_path": localPath,
		"size_bytes": sizeBytes,
		"error":      errorMsg,
		"reason":     reason.String(),
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
		// Revert storage_location on failure so future defrosts can retry.
		// CAS-gate on (storage_location='defrosting' AND
		// defrost_node_id=reportingNode) so a stale failure from a node
		// the row no longer points at cannot blow away a newer successful
		// defrost's state. RowsAffected = 0 means somebody else (a fresh
		// defrost, a successful complete, the stale-defrost cleaner)
		// already moved the row; suppress the retry too.
		reportingNodeID := complete.GetNodeId()
		if reportingNodeID == "" {
			reportingNodeID = nodeID
		}
		revertedRow := false
		if res, dbErr := db.ExecContext(context.Background(), `
			UPDATE foghorn.artifacts
			SET storage_location = 's3',
			    defrost_node_id = NULL,
			    defrost_started_at = NULL,
			    updated_at = NOW()
			WHERE artifact_hash = $1
			  AND storage_location = 'defrosting'
			  AND defrost_node_id = $2
		`, assetHash, reportingNodeID); dbErr != nil {
			logger.WithError(dbErr).WithField("asset_hash", assetHash).Error("failed to revert artifact after defrost failure")
		} else if n, raErr := res.RowsAffected(); raErr == nil && n > 0 {
			revertedRow = true
		}
		logger.WithFields(logging.Fields{
			"asset_hash":   assetHash,
			"error":        errorMsg,
			"reason":       reason.String(),
			"reverted_row": revertedRow,
		}).Warn("Defrost failed; revert + retry gated by CAS")

		// REASON_INSUFFICIENT_SPACE: retry on a different node (VOD only).
		// Skip when CAS missed — the failure is stale and a newer defrost
		// owns the row now.
		if reason == pb.DefrostComplete_REASON_INSUFFICIENT_SPACE && revertedRow {
			go retryDefrostAfterInsufficientSpace(assetHash, reportingNodeID, logger)
		}
	}

	// Notify any waiting defrost requests
	notifyDefrostComplete(assetHash, status == "success", localPath)
}

// retryDefrostAfterInsufficientSpace handles the REASON_INSUFFICIENT_SPACE
// retry path. Loads asset_type from foghorn.artifacts (authoritative —
// never inferred). DVR rows can't reach this path: active DVR is served
// directly from the recording origin's rolling manifest (or DTSC pull
// cross-node); finalized chapters are addressed as their own VOD
// artifacts and follow the normal VOD defrost path. The lookup still
// loads asset_type for logging context. In-memory guard caps retries
// to one per artifact per 5 min; longer-tail recovery is owned by
// StaleDefrostCleanupJob.
func retryDefrostAfterInsufficientSpace(assetHash, failedNodeID string, logger logging.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if db == nil {
		return
	}

	var assetType string
	if err := db.QueryRowContext(ctx, `
		SELECT artifact_type FROM foghorn.artifacts WHERE artifact_hash = $1
	`, assetHash).Scan(&assetType); err != nil {
		logger.WithError(err).WithField("asset_hash", assetHash).Warn("retry lookup failed; skipping")
		return
	}

	if !TryConsumeRetryGuard(assetHash, 5*time.Minute) {
		logger.WithField("asset_hash", assetHash).Info("retry skipped: guard active (recent retry pending)")
		return
	}

	excluded := map[string]struct{}{failedNodeID: {}}
	nextNode, err := PickDefrostNode(0, 0, excluded)
	if err != nil {
		logger.WithError(err).WithFields(logging.Fields{
			"asset_hash":     assetHash,
			"failed_node_id": failedNodeID,
		}).Warn("retry skipped: no alternate storage node available")
		return
	}

	logger.WithFields(logging.Fields{
		"asset_hash":     assetHash,
		"failed_node_id": failedNodeID,
		"retry_node_id":  nextNode,
	}).Info("Retrying defrost on alternate node after REASON_INSUFFICIENT_SPACE")

	if _, retryErr := requestDefrost(ctx, assetType, assetHash, nextNode, 30*time.Second, logger, false, "", nil); retryErr != nil {
		logger.WithError(retryErr).WithFields(logging.Fields{
			"asset_hash":    assetHash,
			"retry_node_id": nextNode,
		}).Warn("retry defrost send failed")
	}
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

// SendLocalInvalidateSessions sends an InvalidateSessionsRequest to a Helmsman
// that has its bidirectional stream attached to this Foghorn instance.
//
// invalidate_sessions does NOT disconnect viewers — it tells MistServer to
// re-run USER_NEW for active sessions on the listed streams. Viewers whose
// tokens still pass the (refreshed) policy continue with a brief reconnect
// blip; viewers whose tokens are now invalid are denied.
func SendLocalInvalidateSessions(nodeID string, req *pb.InvalidateSessionsRequest) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &pb.ControlMessage{
		Payload: &pb.ControlMessage_InvalidateSessionsRequest{InvalidateSessionsRequest: req},
		SentAt:  timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

// SendInvalidateSessions sends an InvalidateSessionsRequest to the given node,
// relaying through Foghorn HA if the stream is held by a peer instance.
func SendInvalidateSessions(nodeID string, req *pb.InvalidateSessionsRequest) error {
	err := SendLocalInvalidateSessions(nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	return relayFailure(err, commandRelay.forward(context.Background(), &pb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &pb.ForwardCommandRequest_InvalidateSessions{InvalidateSessions: req},
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

	// Chapter finalization jobs use a string job_id ("chapter-finalize-<chapter_id>")
	// and have no row in foghorn.processing_jobs (its job_id is UUID). Route
	// them through a dedicated handler that advances chapter state + registers
	// the chapter VOD artifact without touching the processing_jobs table.
	if chapterID := chapterIDFromJobID(result.GetJobId()); chapterID != "" {
		handleChapterFinalizeResult(ctx, chapterID, jobStatus, result, nodeID, logger)
		return
	}

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

				// Update artifact format, size, and sync status so the processed
				// file gets synced to S3 and future defrosts use the correct
				// size for admission control. Keep the original upload URL in
				// s3_url until the replacement upload is durably synced (sync
				// completion updates s3_url + vod_metadata.s3_key together).
				if _, dbErr := db.ExecContext(ctx, `
						UPDATE foghorn.artifacts
						SET format = $1,
						    size_bytes = $3,
						    sync_status = 'pending',
						    storage_location = 'local',
						    updated_at = NOW()
						WHERE artifact_hash = $2`, newFormat, artifactHash, sizeBytes); dbErr != nil {
					logger.WithError(dbErr).WithField("artifact_hash", artifactHash).Error("failed to update artifact format/size after processing")
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
			return
		}
		if chapterID := chapterIDFromJobID(progress.GetJobId()); chapterID != "" {
			processChapterFinalizeProgress(ctx, chapterID, progressPct, logger)
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
			if err := artifactoutbox.EnqueueVodLifecycle(vodData); err != nil {
				logger.WithError(err).Warn("Failed to send processing progress lifecycle event")
			}
		}()
	}
}

func processChapterFinalizeProgress(ctx context.Context, chapterID string, progressPct int32, logger logging.Logger) {
	var artifactHash, tenantID string
	err := db.QueryRowContext(ctx, `
		UPDATE foghorn.dvr_chapters c
		   SET finalize_started_at = NOW()
		  FROM foghorn.artifacts a
		 WHERE c.chapter_id = $1
		   AND c.playback_artifact_hash = a.artifact_hash
		   AND c.state = 'finalizing'
		 RETURNING c.playback_artifact_hash, a.tenant_id::text
	`, chapterID).Scan(&artifactHash, &tenantID)
	if err != nil {
		if err != sql.ErrNoRows {
			logger.WithError(err).WithField("chapter_id", chapterID).Warn("Failed to update chapter finalize progress")
		}
		return
	}
	vodData := &pb.VodLifecycleData{
		Status:      pb.VodLifecycleData_STATUS_PROCESSING,
		VodHash:     artifactHash,
		TenantId:    &tenantID,
		ProgressPct: &progressPct,
	}
	go artifactoutbox.EnqueueVodLifecycleLogged(vodData)
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

	var (
		streamName     string
		tenantFromArti sql.NullString
	)
	err := db.QueryRowContext(ctx, `
		SELECT stream_internal_name, tenant_id::text
		FROM foghorn.artifacts
		WHERE artifact_hash = $1`,
		assetHash).Scan(&streamName, &tenantFromArti)
	if err != nil {
		logger.WithError(err).Error("Failed to lookup asset for dtsh sync")
		return
	}

	var tenantID string
	switch assetType {
	case "clip":
		if CommodoreClient != nil {
			rpcCtx, rpcCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer rpcCancel()
			if resp, err := CommodoreClient.ResolveClipHash(rpcCtx, assetHash); err == nil && resp.Found {
				tenantID = resp.TenantId
			}
		}
	case "dvr":
		if CommodoreClient != nil {
			rpcCtx, rpcCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer rpcCancel()
			if resp, err := CommodoreClient.ResolveDVRHash(rpcCtx, assetHash); err == nil && resp.Found {
				tenantID = resp.TenantId
			}
		}
	case "vod":
		// VOD artifacts (including hidden chapter-origin artifacts) are registered
		// directly in foghorn.artifacts with tenant_id stamped at creation, so
		// foghorn is the single authority for this lookup.
		if tenantFromArti.Valid {
			tenantID = tenantFromArti.String
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
	} else if assetType == "vod" {
		// VOD layout: vod/<tenant>/<hash>/<hash>.<ext> with sidecar at
		// vod/<tenant>/<hash>/<hash>.<ext>.dtsh next to the main file.
		format := "mp4"
		if idx := strings.LastIndex(filePath, "."); idx != -1 {
			format = filePath[idx+1:]
		}
		s3Key := s3Client.BuildVodS3Key(tenantID, assetHash, assetHash+"."+format) + ".dtsh"
		presignedURL, err := s3Client.GeneratePresignedPUT(s3Key, expiry)
		if err != nil {
			logger.WithError(err).Error("Failed to generate presigned URL for VOD .dtsh")
			return
		}
		req.PresignedPutUrl = presignedURL
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

	// Look up asset info from foghorn.artifacts. size_bytes is used by Helmsman
	// for defrost admission (preflight HasSpaceFor + proactive background
	// cleanup). NULL is acceptable — Helmsman skips admission when 0.
	var streamName, storageLocation, format, tenantID string
	var s3Key, filename, streamID, artifactInternalName sql.NullString
	var artifactSizeBytes sql.NullInt64
	err := db.QueryRowContext(ctx, `
		SELECT a.stream_internal_name,
		       COALESCE(a.storage_location, 'local'),
		       COALESCE(a.format, ''),
		       COALESCE(a.tenant_id::text, ''),
		       COALESCE(v.s3_key, ''),
		       COALESCE(v.filename, ''),
		       a.stream_id::text,
		       a.internal_name,
		       a.size_bytes
		FROM foghorn.artifacts a
		LEFT JOIN foghorn.vod_metadata v ON a.artifact_hash = v.artifact_hash
		WHERE a.artifact_hash = $1 AND a.artifact_type = $2`,
		assetHash, artifactType).Scan(&streamName, &storageLocation, &format, &tenantID, &s3Key, &filename, &streamID, &artifactInternalName, &artifactSizeBytes)
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

	if artifactType == "dvr" {
		return "", fmt.Errorf("DVR archive playback requires chapter context; use the chapter API (dvrChapter)")
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

	expectedSizeBytes := uint64(0)
	if artifactSizeBytes.Valid && artifactSizeBytes.Int64 > 0 {
		expectedSizeBytes = uint64(artifactSizeBytes.Int64)
	}

	req := &pb.DefrostRequest{
		RequestId:          requestID,
		AssetType:          assetType,
		AssetHash:          assetHash,
		TenantId:           tenantID,
		StreamInternalName: streamName,
		InternalName:       artifactInternalName.String,
		TimeoutSeconds:     int32(timeout.Seconds()),
		UrlExpirySeconds:   int64(expiry.Seconds()),
		ExpectedSizeBytes:  expectedSizeBytes,
	}

	storageBase := storageBasePathForNode(nodeID)

	if useRemoteURLs {
		// Remote defrost: use presigned URLs supplied by the origin cluster
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

	// Send defrost request to node. Track in-flight count for placement.
	IncrementDefrost(nodeID)
	if err := SendDefrostRequest(nodeID, req); err != nil {
		// Send failed before the node could observe it; the tracker bump
		// would otherwise leak so we decrement here. (A successful send
		// pairs with a decrement on DefrostComplete arrival.)
		DecrementDefrost(nodeID)
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

// artifactRepo provides database access for dual-storage sync tracking.
var artifactRepo state.ArtifactRepository

// SetArtifactRepository sets the artifact repository for sync tracking.
func SetArtifactRepository(repo state.ArtifactRepository) {
	artifactRepo = repo
}

// GetRelayBaseURL returns the URL Mist on the given node uses to reach
// Helmsman's /internal/artifact/* relay. Captured at Register time from the
// node's HELMSMAN_RELAY_BASE_URL env var. Returns "" when the node has not
// connected or did not advertise a relay URL — callers must treat this as
// "cannot route through relay, abort STREAM_SOURCE" rather than fabricating
// 127.0.0.1, which is wrong for container deployments where Mist and
// Helmsman are separate services.
func GetRelayBaseURL(nodeID string) string {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	c, ok := registry.conns[nodeID]
	if !ok || c == nil {
		return ""
	}
	return c.relayBaseURL
}

// processCanDeleteRequest handles can-delete checks from Helmsman. Before
// deleting a local asset copy, Helmsman asks Foghorn if it's safe.
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
		// Check if this is a remote artifact (storage cluster's S3 holds the
		// authoritative copy). storage_cluster_id is the cluster whose S3
		// minted the upload URLs; NULL falls back to origin_cluster_id.
		// CanDeleteRequest carries no tenant_id, so we read every row for
		// the hash and only honor the remote-synced shortcut when there is
		// exactly one match — multiple rows mean we can't prove which
		// tenant's record this delete belongs to and could bleed a remote
		// disposition across tenants.
		if db != nil {
			authoritativeCluster, ok := lookupAuthoritativeClusterUnambiguous(context.Background(), assetHash, logger)
			if ok && authoritativeCluster != "" && !isServedCluster(authoritativeCluster) {
				response.SafeToDelete = true
				response.Reason = "remote_synced"
				logger.WithFields(logging.Fields{
					"asset_hash":            assetHash,
					"authoritative_cluster": authoritativeCluster,
				}).Info("Remote artifact — safe to delete (storage cluster's S3 authoritative)")
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

		// Update foghorn.artifacts with storage_location, dtsh_synced, and
		// the post-sync size. size_bytes is critical for future defrost
		// admission: processed VODs upload at a different size than the
		// original, and admission preflights against the artifact row's size.
		if _, dbErr := db.ExecContext(ctx, `
			UPDATE foghorn.artifacts
			SET storage_location = 'local',
			    s3_url = COALESCE(NULLIF($1,''), s3_url),
			    dtsh_synced = $2,
			    size_bytes = COALESCE(NULLIF($4::BIGINT, 0), size_bytes),
			    last_sync_attempt = NOW(),
			    sync_error = NULL,
			    updated_at = NOW()
			WHERE artifact_hash = $3
			  AND sync_status = 'synced'`,
			s3URL, dtshIncluded, assetHash, int64(sizeBytes)); dbErr != nil {
			logger.WithError(dbErr).WithField("asset_hash", assetHash).Error("failed to mark artifact as synced")
		}

		// Chapter artifacts (origin_type='dvr_chapter') advance their
		// chapter row from finalized → frozen once both sync_status
		// AND dtsh_synced are true. This is the trigger the reclaim
		// sweep waits on; without it source TS segments stay pinned.
		if dtshIncluded {
			if chapterID := chapterOriginIDForArtifact(ctx, assetHash); chapterID != "" {
				if frzErr := MarkChapterFrozen(ctx, chapterID); frzErr != nil {
					logger.WithError(frzErr).WithFields(logging.Fields{
						"chapter_id":    chapterID,
						"artifact_hash": assetHash,
					}).Warn("Chapter freeze transition failed")
				} else {
					logger.WithFields(logging.Fields{
						"chapter_id":    chapterID,
						"artifact_hash": assetHash,
					}).Info("Chapter frozen — source segments eligible for reclaim")
				}
			}
		}

		// For VOD, the s3_key in vod_metadata is the canonical defrost source.
		// On processed-VOD replacement uploads the key derived above (from
		// tenant/hash/format) differs from the original upload key; persist
		// the new value so defrost reads the synced location, not the
		// original-upload row.
		if artifactType == "vod" && s3URL != "" && db != nil {
			derivedKey := ""
			if s3Client != nil && tenantID != "" {
				f := format
				if f == "" {
					f = "mp4"
				}
				derivedKey = s3Client.BuildVodS3Key(tenantID, assetHash, assetHash+"."+f)
			}
			if derivedKey != "" {
				if _, dbErr := db.ExecContext(ctx, `
					INSERT INTO foghorn.vod_metadata (artifact_hash, s3_key, filename)
					VALUES ($1, $2, $3)
					ON CONFLICT (artifact_hash) DO UPDATE SET s3_key = EXCLUDED.s3_key`,
					assetHash, derivedKey, assetHash+"."+format); dbErr != nil {
					logger.WithError(dbErr).WithField("asset_hash", assetHash).Warn("failed to update vod_metadata.s3_key after sync")
				}
			}
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
		// Sync failed. local_missing=true is terminal lost_local; transient
		// failures stay 'failed' and are retried with backoff/cap.
		newSyncStatus := "failed"
		if complete.GetLocalMissing() {
			newSyncStatus = "lost_local"
		}
		if err := artifactRepo.SetSyncStatus(ctx, assetHash, newSyncStatus, ""); err != nil {
			logger.WithError(err).Error("Failed to update sync status to " + newSyncStatus)
		}

		if _, dbErr := db.ExecContext(ctx, `
			UPDATE foghorn.artifacts
			SET storage_location = 'local',
			    sync_status = $3,
			    status = CASE WHEN $3 = 'lost_local' THEN 'failed' ELSE status END,
			    sync_error = NULLIF($1,''),
			    last_sync_attempt = NOW(),
			    failure_count = CASE WHEN $3 = 'failed' THEN failure_count + 1 ELSE failure_count END,
			    updated_at = NOW()
			WHERE artifact_hash = $2`,
			errorMsg, assetHash, newSyncStatus); dbErr != nil {
			logger.WithError(dbErr).WithField("asset_hash", assetHash).Error("failed to record sync failure")
		}

		logger.WithFields(logging.Fields{
			"asset_hash": assetHash,
			"error":      errorMsg,
		}).Warn("Asset sync to S3 failed")
	}
}

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
		// composeConfigSeed resolves the FULL bundle set:
		//   - cluster wildcard (from fetchClusterTLSBundle internally)
		//   - platform-edge / pool-assigned multi-SAN (when applicable)
		//   - per-tenant *.{tenant}.cdn.{root} bundles (from
		//     fetchTenantBundles)
		// Fingerprinting on JUST the cluster wildcard would mask tenant
		// bundle changes; adding/removing a paying tenant's cluster
		// subscription would never trigger a push until the cluster
		// wildcard itself rotated. Fingerprint the full set instead.
		mode := resolveOperationalMode(n.canonicalID, pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_UNSPECIFIED)
		seed := composeConfigSeed(n.canonicalID, nil, n.peerAddr, mode, "")
		stripWildcardSiteWithoutTLS(seed)

		nextState := tlsBundleSetState(seed.GetTlsBundles(), seedCaBundle)

		prev, ok := lastPushedTLSState.Load(n.connID)
		if prevState, isString := prev.(string); ok && isString && prevState == nextState {
			continue
		}

		if err := SendConfigSeed(n.connID, seed); err != nil {
			log.WithError(err).WithField("node_id", n.canonicalID).Warn("Failed to push renewed TLS bundles")
			continue
		}

		lastPushedTLSState.Store(n.connID, nextState)
		bundleCount := len(seed.GetTlsBundles())
		clusterDomain := ""
		if seed.GetTls() != nil {
			clusterDomain = seed.GetTls().GetDomain()
		}
		if bundleCount == 0 {
			log.WithFields(logging.Fields{
				"node_id": n.canonicalID,
				"conn_id": n.connID,
			}).Info("Removed TLS bundles from edge after navigator reported no certificates")
			continue
		}

		log.WithFields(logging.Fields{
			"node_id":        n.canonicalID,
			"conn_id":        n.connID,
			"bundle_count":   bundleCount,
			"cluster_domain": clusterDomain,
		}).Info("Pushed refreshed TLS bundle set to edge")
	}
}

func stripWildcardSiteWithoutTLS(seed *pb.ConfigSeed) {
	if seed == nil || seed.GetTls() != nil || seed.GetSite() == nil {
		return
	}
	if strings.HasPrefix(seed.GetSite().GetSiteAddress(), "*.") {
		seed.Site = nil
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

	slug := pkgdns.SanitizeLabel(node.GetClusterId())
	wildcardDomain := fmt.Sprintf("*.%s.%s", slug, rootDomain)
	return &pb.TLSCertBundle{
		CertPem:       certResp.GetCertPem(),
		KeyPem:        certResp.GetKeyPem(),
		Domain:        certResp.GetDomain(),
		ExpiresAt:     certResp.GetExpiresAt(),
		BundleId:      "cluster:" + slug,
		SiteAddresses: []string{wildcardDomain},
	}, true, nil
}

func tlsBundleState(bundle *pb.TLSCertBundle) string {
	return tlsMaterialState(bundle, nil)
}

// tlsBundleSetState fingerprints the full ordered set of TLS bundles
// plus the CA bundle. Used by the refresh loop to dedup pushes: a
// change in any tenant or platform bundle (added, removed, or rotated)
// produces a different fingerprint, which guarantees the next refresh
// pushes a fresh ConfigSeed instead of stalling on the cluster bundle's
// fingerprint alone.
func tlsBundleSetState(bundles []*pb.TLSCertBundle, caBundle []byte) string {
	if len(bundles) == 0 && len(caBundle) == 0 {
		return tlsStateNoCert
	}
	payload := make([]byte, 0, len(caBundle)+512)
	for _, b := range bundles {
		if b == nil {
			continue
		}
		payload = append(payload, b.GetBundleId()...)
		payload = append(payload, '\x00')
		payload = append(payload, b.GetCertPem()...)
		payload = append(payload, '\x00')
		payload = append(payload, b.GetKeyPem()...)
		payload = append(payload, '\x00')
		payload = append(payload, b.GetDomain()...)
		payload = fmt.Appendf(payload, "\x00%d", b.GetExpiresAt())
		payload = append(payload, '\x00')
	}
	payload = append(payload, caBundle...)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
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

	// Note: s3Client nil-check moved to inside the StorageMintLocal branch
	// so a self-host pool with no local S3 can still federate to platform
	// storage when the resolver picks that path.

	// Resolve internal_name → stable S3 key identifier + cluster context
	// for the storage resolver. The MistServer wildcard prefix ("live+" /
	// "vod+") routes; the bare name is the lookup key.
	var (
		thumbnailKey       string
		thumbTenantID      string
		thumbOriginCluster string
		isLive             bool
		streamInternalName string
	)
	bareName := mist.ExtractInternalName(internalName)
	switch {
	case strings.HasPrefix(internalName, "live+"):
		isLive = true
		streamInternalName = bareName
		// In-memory StreamState carries StreamID + TenantID populated by
		// PUSH_REWRITE on ingest. It does NOT carry cluster context, so
		// origin cluster must come from Commodore even when state is hot.
		if ss := state.DefaultManager().GetStreamState(bareName); ss != nil && ss.StreamID != "" {
			thumbnailKey = ss.StreamID
			thumbTenantID = ss.TenantID
		}
		if (thumbnailKey == "" || thumbOriginCluster == "") && CommodoreClient != nil {
			resp, err := CommodoreClient.ResolveInternalName(context.Background(), bareName)
			if err != nil || resp == nil {
				logger.WithFields(logging.Fields{
					"stream_name":   internalName,
					"internal_name": bareName,
					"error":         err,
				}).Warn("Could not resolve internal_name to stream_id for thumbnail upload")
				return
			}
			if thumbnailKey == "" {
				if resp.StreamId == "" {
					logger.WithFields(logging.Fields{
						"stream_name":   internalName,
						"internal_name": bareName,
					}).Warn("Commodore returned no stream_id for thumbnail upload")
					return
				}
				thumbnailKey = resp.StreamId
				state.DefaultManager().SetStreamStreamID(bareName, resp.StreamId)
			}
			if thumbTenantID == "" {
				thumbTenantID = resp.GetTenantId()
			}
			thumbOriginCluster = resp.GetOriginClusterId()
		}
		if thumbnailKey == "" {
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
		// VOD+: also pull tenant_id and the authoritative storage cluster
		// (storage_cluster_id with origin_cluster_id fallback) so the
		// resolver can pick the right pool. Caller's stream state has no
		// VOD context so the artifact row is the only source.
		var artifactHash string
		var tenantID sql.NullString
		var authoritativeCluster sql.NullString
		if err := conn.QueryRowContext(context.Background(),
			`SELECT artifact_hash, tenant_id::text, COALESCE(storage_cluster_id, origin_cluster_id)
			   FROM foghorn.artifacts
			  WHERE internal_name = $1`,
			bareName,
		).Scan(&artifactHash, &tenantID, &authoritativeCluster); err != nil {
			logger.WithFields(logging.Fields{
				"stream_name":   internalName,
				"internal_name": bareName,
			}).Warn("Could not resolve internal_name to artifact_hash for thumbnail upload")
			return
		}
		thumbnailKey = artifactHash
		if tenantID.Valid {
			thumbTenantID = tenantID.String
		}
		if authoritativeCluster.Valid {
			thumbOriginCluster = authoritativeCluster.String
		}
		logger.WithFields(logging.Fields{
			"stream_name":   internalName,
			"internal_name": bareName,
			"artifact_hash": thumbnailKey,
		}).Info("Resolved artifact hash for thumbnail S3 key")
	case strings.HasPrefix(internalName, "dvr+"):
		conn := GetDB()
		if conn == nil {
			logger.Warn("DB not available for DVR thumbnail resolution")
			return
		}
		token := strings.TrimPrefix(internalName, "dvr+")
		target, err := resolveDVRThumbnailTarget(context.Background(), conn, token)
		if err != nil {
			logger.WithFields(logging.Fields{
				"stream_name": internalName,
				"dvr_token":   token,
			}).Warn("Could not resolve dvr+ stream to artifact_hash for thumbnail upload")
			return
		}
		thumbnailKey = target.artifactHash
		if target.tenantID.Valid {
			thumbTenantID = target.tenantID.String
		}
		if target.authoritativeCluster.Valid {
			thumbOriginCluster = target.authoritativeCluster.String
		}
		logger.WithFields(logging.Fields{
			"stream_name":   internalName,
			"dvr_token":     token,
			"artifact_hash": thumbnailKey,
		}).Info("Resolved DVR artifact hash for thumbnail S3 key")
	default:
		logger.WithField("internal_name", internalName).Warn("Thumbnail upload from unrecognised stream prefix; expected live+, vod+, or dvr+")
		return
	}

	// Run the same storage resolver used by freeze and CreateVodUpload.
	// Without a tenant, thumbOriginCluster/localClusterID are the only
	// available storage ownership signals.
	storageCluster, mintMode := resolveThumbnailStorageCluster(context.Background(), thumbTenantID, thumbOriginCluster)

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

	switch mintMode {
	case storage.StorageUnavailable:
		logger.WithFields(logging.Fields{
			"internal_name":  internalName,
			"tenant_id":      thumbTenantID,
			"origin_cluster": thumbOriginCluster,
		}).Warn("Storage resolver returned unavailable for thumbnail upload — dropping")
		return

	case storage.StorageMintViaFederation:
		if storageMintDelegate == nil {
			logger.WithField("storage_cluster", storageCluster).Warn("Federated thumbnail mint required but no delegate wired — dropping")
			return
		}
		// streamInternalName goes on the request only for live thumbs so
		// the callee can verify tenant via stream state. For vod+ the
		// callee verifies via foghorn.artifacts WHERE artifact_hash =
		// <key prefix>.
		streamCtxName := ""
		if isLive {
			streamCtxName = streamInternalName
		}
		for _, fp := range filePaths {
			fileName := filepath.Base(fp)
			if !allowedThumbnailFiles[fileName] {
				logger.WithField("file_name", fileName).Warn("Rejected thumbnail filename not in allowlist")
				continue
			}
			mintReq := &pb.MintStorageURLsRequest{
				TenantId:           thumbTenantID,
				RequestingCluster:  localClusterID,
				TargetClusterId:    storageCluster,
				ArtifactType:       "thumbnail",
				ArtifactKey:        thumbnailKey + "/" + fileName,
				Op:                 pb.MintStorageURLsRequest_OPERATION_PUT_SINGLE,
				ContentType:        thumbnailContentType(fileName),
				StreamInternalName: streamCtxName,
			}
			mintResp, mintErr := storageMintDelegate(context.Background(), storageCluster, mintReq)
			if mintErr != nil || mintResp == nil || !mintResp.GetAccepted() {
				logger.WithError(mintErr).WithFields(logging.Fields{
					"file_name":       fileName,
					"storage_cluster": storageCluster,
				}).Warn("Federated MintStorageURLs failed for thumbnail")
				continue
			}
			resp.Uploads = append(resp.Uploads, &pb.ThumbnailUploadResponse_PresignedUpload{
				FileName:     fileName,
				PresignedUrl: mintResp.GetPresignedPutUrl(),
				S3Key:        mintResp.GetS3Key(),
				LocalPath:    fp,
			})
		}

	default: // StorageMintLocal
		if s3Client == nil {
			logger.Warn("S3 client not configured, ignoring thumbnail upload request")
			return
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
	invalidateChandlerThumbnailCache(thumbnailKey, s3Keys, logger)
}

type chandlerInvalidateRequest struct {
	AssetKey string   `json:"assetKey"`
	Files    []string `json:"files"`
}

func invalidateChandlerThumbnailCache(thumbnailKey string, s3Keys []string, logger logging.Logger) {
	if thumbnailKey == "" || len(s3Keys) == 0 {
		return
	}

	serviceToken := strings.TrimSpace(os.Getenv("SERVICE_TOKEN"))
	if serviceToken == "" {
		logger.Warn("SERVICE_TOKEN missing, skipping Chandler thumbnail cache invalidation")
		return
	}

	files := make([]string, 0, len(s3Keys))
	seen := make(map[string]bool, len(s3Keys))
	for _, key := range s3Keys {
		file := filepath.Base(key)
		switch file {
		case "poster.jpg", "sprite.jpg", "sprite.vtt":
			if !seen[file] {
				seen[file] = true
				files = append(files, file)
			}
		}
	}
	if len(files) == 0 {
		return
	}

	baseURLs := getChandlerInternalBaseURLs()
	if len(baseURLs) == 0 {
		logger.Warn("Chandler URL missing, skipping thumbnail cache invalidation")
		return
	}

	body, err := json.Marshal(chandlerInvalidateRequest{
		AssetKey: thumbnailKey,
		Files:    files,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to encode Chandler cache invalidation request")
		return
	}

	for _, baseURL := range baseURLs {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/internal/assets/cache/invalidate", strings.NewReader(string(body)))
		if err != nil {
			cancel()
			logger.WithError(err).WithField("base_url", baseURL).Warn("Failed to build Chandler cache invalidation request")
			continue
		}
		httpReq.Header.Set("Authorization", "Bearer "+serviceToken)
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(httpReq)
		cancel()
		if err != nil {
			logger.WithError(err).WithFields(logging.Fields{
				"thumbnail_key": thumbnailKey,
				"base_url":      baseURL,
			}).Warn("Chandler thumbnail cache invalidation failed")
			continue
		}
		statusCode := resp.StatusCode
		_ = resp.Body.Close()
		if statusCode < 200 || statusCode >= 300 {
			logger.WithFields(logging.Fields{
				"thumbnail_key": thumbnailKey,
				"base_url":      baseURL,
				"status":        statusCode,
			}).Warn("Chandler thumbnail cache invalidation returned non-2xx")
			continue
		}
		logger.WithFields(logging.Fields{
			"thumbnail_key": thumbnailKey,
			"base_url":      baseURL,
			"files":         files,
		}).Debug("Chandler thumbnail cache invalidated")
	}
}

func getChandlerInternalBaseURLs() []string {
	if base := strings.TrimSpace(os.Getenv("CHANDLER_INTERNAL_URL")); base != "" {
		return splitChandlerBaseURLs(base)
	}
	return splitChandlerBaseURLs(getChandlerBaseURL())
}

func splitChandlerBaseURLs(raw string) []string {
	parts := strings.Split(raw, ",")
	baseURLs := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		baseURL := strings.TrimRight(strings.TrimSpace(part), "/")
		if baseURL == "" || seen[baseURL] {
			continue
		}
		seen[baseURL] = true
		baseURLs = append(baseURLs, baseURL)
	}
	return baseURLs
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

// markArtifactHasThumbnails flips has_thumbnails on the first confirmed
// artifact thumbnail upload and projects that state to Commodore once.
func markArtifactHasThumbnails(artifactHash string, logger logging.Logger) {
	conn := GetDB()
	if conn == nil {
		logger.Warn("DB not available, cannot mark artifact thumbnails")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var (
		tenantID         sql.NullString
		artifactType     string
		storageClusterID sql.NullString
		originClusterID  sql.NullString
	)
	err := conn.QueryRowContext(ctx, `
		UPDATE foghorn.artifacts
		   SET has_thumbnails = true, updated_at = NOW()
		 WHERE artifact_hash = $1
		   AND has_thumbnails IS DISTINCT FROM true
		RETURNING tenant_id::text, artifact_type, storage_cluster_id, origin_cluster_id
	`, artifactHash).Scan(&tenantID, &artifactType, &storageClusterID, &originClusterID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return
		}
		logger.WithFields(logging.Fields{
			"artifact_hash": artifactHash,
			"error":         err,
		}).Error("Failed to mark artifact has_thumbnails")
		return
	}
	logger.WithField("artifact_hash", artifactHash).Info("Artifact thumbnails marked as uploaded")

	if CommodoreClient == nil || !tenantID.Valid || tenantID.String == "" {
		return
	}
	cluster := storageClusterID.String
	if cluster == "" {
		cluster = originClusterID.String
	}
	if cluster == "" {
		logger.WithField("artifact_hash", artifactHash).Warn("Artifact thumbnail readiness has no cluster projection")
		return
	}
	assetType, ok := artifactAssetTypeFromString(artifactType)
	if !ok {
		logger.WithFields(logging.Fields{
			"artifact_hash": artifactHash,
			"artifact_type": artifactType,
		}).Warn("Unknown artifact_type; skipping Commodore thumbnail notify")
		return
	}
	notifyCtx, notifyCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer notifyCancel()
	if _, err := CommodoreClient.MarkArtifactThumbnailsReady(notifyCtx, tenantID.String, assetType, artifactHash, cluster); err != nil {
		logger.WithError(err).WithFields(logging.Fields{
			"artifact_hash": artifactHash,
			"asset_type":    artifactType,
		}).Warn("Failed to notify Commodore of artifact thumbnail readiness")
	}
}

// artifactAssetTypeFromString maps foghorn.artifacts.artifact_type values to
// the proto enum used by MarkArtifactThumbnailsReady /
// UpdateArtifactStorageCluster.
func artifactAssetTypeFromString(t string) (pb.ArtifactAssetType, bool) {
	switch t {
	case "clip":
		return pb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_CLIP, true
	case "dvr":
		return pb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_DVR, true
	case "vod":
		return pb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD, true
	default:
		return pb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_UNSPECIFIED, false
	}
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

// chandlerPerClusterCache caches per-cluster Chandler base URLs (chandler.<slug>.<base>)
// resolved via Quartermaster. 5-minute TTL per cluster. The cache is keyed by
// cluster_id; values are the fully-formed `https://chandler.<slug>.<base-domain>`
// string. Empty cluster_id and resolution failures are NOT cached so transient
// Quartermaster outages don't poison subsequent lookups.
var (
	chandlerPerClusterMu    sync.RWMutex
	chandlerPerClusterCache = map[string]chandlerCachedURL{}
)

type chandlerCachedURL struct {
	url        string
	resolvedAt time.Time
}

const chandlerPerClusterTTL = 5 * time.Minute

// getChandlerBaseURLForCluster returns the Chandler base URL for the named
// cluster — `https://chandler.<cluster-slug>.<cluster-base-domain>` derived
// from Quartermaster cluster metadata. Per-cluster cache with a 5-minute TTL
// per entry; cache state is independent of `resolvedChandlerBaseURL`, so a
// per-cluster lookup never mutates the platform-level Chandler URL that other
// callers depend on.
//
// Returns "" if the cluster ID is empty, no cluster lookup is configured, the
// Quartermaster lookup fails, or the cluster has no slug/base-domain.
func getChandlerBaseURLForCluster(clusterID string) string {
	clusterID = strings.TrimSpace(clusterID)
	if clusterID == "" {
		return ""
	}

	chandlerPerClusterMu.RLock()
	if entry, ok := chandlerPerClusterCache[clusterID]; ok && time.Since(entry.resolvedAt) < chandlerPerClusterTTL {
		chandlerPerClusterMu.RUnlock()
		return entry.url
	}
	chandlerPerClusterMu.RUnlock()

	if getClusterFn == nil {
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
	url := "https://" + fqdn

	chandlerPerClusterMu.Lock()
	chandlerPerClusterCache[clusterID] = chandlerCachedURL{url: url, resolvedAt: time.Now()}
	chandlerPerClusterMu.Unlock()

	return url
}

// clearChandlerPerClusterCache resets the per-cluster Chandler URL cache. Test
// hook only — production callers should not invalidate this cache directly.
func clearChandlerPerClusterCache() {
	chandlerPerClusterMu.Lock()
	chandlerPerClusterCache = map[string]chandlerCachedURL{}
	chandlerPerClusterMu.Unlock()
}
