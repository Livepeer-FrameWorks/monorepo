package control

import (
	"bytes"
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
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"frameworks/api_balancing/internal/artifactoutbox"
	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/identity"
	"frameworks/api_balancing/internal/ingesterrors"
	"frameworks/api_balancing/internal/state"
	"frameworks/api_balancing/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	navclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/navigator"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/geoip"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/nodeidentity"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	foghornrelaypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_relay"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/streamident"
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
	"google.golang.org/protobuf/proto"
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
	rootDomain := pkgdns.NormalizeDomainScope(os.Getenv("BRAND_DOMAIN"))
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

func buildBootstrapEdgeNodeRequest(ctx context.Context, reg *ipcpb.Register, nodeID, peerAddr, token, targetClusterID string, servedClusterIDs []string) *quartermasterpb.BootstrapEdgeNodeRequest {
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

	req := &quartermasterpb.BootstrapEdgeNodeRequest{Token: token, Hostname: nodeID, Ips: []string{host}, ServedClusterIds: servedClusterIDs}
	if reg != nil {
		req.NodeIdentityPublicKeyEd25519 = append([]byte(nil), reg.GetNodeIdentityPublicKeyEd25519()...)
		req.RotateNodeIdentity = reg.GetNodeIdentityRotationRequested()
	}
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

func sendControlError(stream ipcpb.HelmsmanControl_ConnectServer, code, message string) error {
	return stream.Send(&ipcpb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &ipcpb.ControlMessage_Error{Error: &ipcpb.ControlError{Code: code, Message: message}},
	})
}

// Registry holds active Helmsman control streams keyed by node_id
type Registry struct {
	mu    sync.RWMutex
	conns map[string]*conn
	log   logging.Logger
}

type conn struct {
	stream ipcpb.HelmsmanControl_ConnectServer
	last   time.Time
	// rawNodeID is the registry key the node heartbeats under (the id it self-asserted at Register). Kept so the
	// server-owned NodeSession (see nodeSession) can carry BOTH the raw and canonical ids without a registry
	// reverse-lookup.
	rawNodeID    string
	peerAddr     string
	canonicalID  string // node ID after fingerprint/enrollment resolution (may differ from registry key)
	clusterID    string
	relayBaseURL string // base URL Mist on this node uses to reach Helmsman's /internal/artifact/* relay
	// protocolVersion is the control-protocol version this sidecar declared at registration
	// (Register.control_protocol_version). It gates protocol-dependent dispatch — notably staged freeze —
	// on an OBSERVED capability rather than a per-request self-asserted flag. 0 = a pre-staged-freeze sidecar.
	protocolVersion int32
	// fence is the monotonic ownership fence issued to THIS control connection at registration. It
	// orders whole-node artifact reports across reconnects (a later connection ranks strictly
	// higher) and is used to write a terminal watermark tombstone when the connection is evicted.
	fence int64
	// superseded is set the moment this connection is removed or replaced in the registry, UNDER sendGate.
	superseded atomic.Bool
	// sendGate serializes a dispatch's (check-superseded → Send) against retirement's (set-superseded). Holding
	// it makes the superseded check and the Send atomic w.r.t. a reconnect retiring the connection, so a
	// command is never transmitted over a connection a reconnect has already retired. *conn is always
	// heap-allocated and shared by pointer, so the mutex is never copied.
	sendGate sync.Mutex
}

// NodeSession is the server-owned, authenticated identity of a control connection. It is built from the values
// Quartermaster resolved at Register (fingerprint/enrollment + cluster reconcile) and is immutable for the
// connection's lifetime — handlers use it INSTEAD of re-deriving identity from node-reported NodeState (which a
// reconnect can race) or trusting payload identity fields.
//
// It carries NODE + CLUSTER identity only. The cluster may have a Quartermaster owner tenant, but that is a
// cluster-ownership fact for node-level/provider attribution — it is NEVER media-tenant provenance. Media tenant
// is always resolved per-resource (job/stream/artifact); see PLAN_MEDIA_AUTHORITY_FOUNDATION.md.
type NodeSession struct {
	RawNodeID       string // registry key the node heartbeats under
	CanonicalNodeID string // node id after fingerprint/enrollment resolution
	ClusterID       string // authoritative virtual cluster (server-resolved at enrollment, never payload)
	Fence           int64  // monotonic ownership fence for this connection generation
	ProtocolVersion int32
}

// NodeID returns the id handlers should attribute effects to: the canonical id when resolution produced one,
// else the raw registry key.
func (s NodeSession) NodeID() string {
	if strings.TrimSpace(s.CanonicalNodeID) != "" {
		return s.CanonicalNodeID
	}
	return s.RawNodeID
}

// session snapshots the connection's authenticated identity. Callers hold no lock; the fields are written once
// during Register (before the conn is dispatch-visible) and only read thereafter.
// session returns the connection's immutable authenticated identity. The identity fields are set ONCE during
// Register — after fingerprint/enrollment resolves the canonical node + cluster and BEFORE the conn is published
// into registry.conns — and never mutated after, so any dispatch-visible conn always carries a complete session.
func (c *conn) session() NodeSession {
	return NodeSession{
		RawNodeID:       c.rawNodeID,
		CanonicalNodeID: c.canonicalID,
		ClusterID:       c.clusterID,
		Fence:           c.fence,
		ProtocolVersion: c.protocolVersion,
	}
}

// currentNodeSession returns the authenticated session for the connection currently registered under nodeID.
// ok=false when there is no current connection (the node isn't connected here). This is the authoritative
// identity source for handlers, replacing race-prone GetNodeState-for-identity lookups.
func currentNodeSession(nodeID string) (NodeSession, bool) {
	if registry == nil {
		return NodeSession{}, false
	}
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return NodeSession{}, false
	}
	return c.session(), true
}

func currentControlConn(nodeID string, stream ipcpb.HelmsmanControl_ConnectServer) (*conn, bool) {
	if registry == nil {
		return nil, false
	}
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil || c.stream != stream {
		return nil, false
	}
	return c, true
}

// connIsCurrentlyRegistered reports whether c is STILL the connection the registry maps nodeID to. Send paths
// call this WHILE holding c.sendGate: a replacement publishes the new conn into the registry map BEFORE
// retiring the old one (retireConn runs after the swap, outside registry.mu), so a sender that already fetched
// the old pointer could otherwise pass the superseded check (flag not yet set) and transmit over a stream a
// reconnect has replaced. The registry-generation recheck closes that window without reintroducing the
// registry.mu/sendGate coupling (retireConn never holds registry.mu, so sendGate→registry.mu can't invert).
func connIsCurrentlyRegistered(nodeID string, c *conn) bool {
	if registry == nil || c == nil {
		return false
	}
	registry.mu.RLock()
	cur := registry.conns[nodeID]
	registry.mu.RUnlock()
	return cur == c
}

func controlStreamIsCurrentOrUntracked(nodeID string, stream ipcpb.HelmsmanControl_ConnectServer) bool {
	if registry == nil {
		return true
	}
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return true
	}
	return c.stream == stream
}

// removedConn identifies a control connection dropped from the registry, carrying its ownership fence
// so the conn_owner release matches the EXACT connection and cannot delete a newer owner's key.
type removedConn struct {
	id    string
	fence int64
}

// retireConn marks a connection superseded. Senders hold sendGate across their superseded check AND
// their Send, so this store alone preserves the invariant: a sender either passed its check before
// the store (it was in flight — it completes on the connection it validated) or acquires the gate
// afterward, observes the flag, and aborts. Deliberately does NOT acquire sendGate itself: a Send
// stalled on gRPC flow control holds the gate indefinitely, and retirement (and the registration
// replacing the connection) must never block behind a dead peer's stalled write.
func retireConn(c *conn) {
	c.superseded.Store(true)
}

func removeCurrentControlConn(nodeID, canonicalID string, stream ipcpb.HelmsmanControl_ConnectServer) []removedConn {
	if registry == nil {
		return nil
	}
	removed := make([]removedConn, 0, 2)
	toRetire := make([]*conn, 0, 2)
	registry.mu.Lock()
	if c, ok := registry.conns[nodeID]; ok && c.stream == stream {
		delete(registry.conns, nodeID)
		toRetire = append(toRetire, c)
		removed = append(removed, removedConn{id: nodeID, fence: c.fence})
	}
	if canonicalID != "" && canonicalID != nodeID {
		if c, ok := registry.conns[canonicalID]; ok && c.stream == stream {
			delete(registry.conns, canonicalID)
			toRetire = append(toRetire, c)
			removed = append(removed, removedConn{id: canonicalID, fence: c.fence})
		}
	}
	registry.mu.Unlock()
	// Retire OUTSIDE registry.mu (see retireConn): the conns are already unlinked, so no new sender can find
	// them; the sendGate alone serializes the superseded-store against a concurrent sender's check+Send.
	for _, c := range toRetire {
		retireConn(c)
	}
	return removed
}

func releaseConnOwnerForDisconnect(nodeID string, fence int64, log logging.Logger) bool {
	rs := GetRedisStore()
	if rs == nil {
		return true
	}
	deleted, err := rs.DeleteConnOwnerIfMatch(context.Background(), nodeID, GetInstanceID(), GetAdvertiseAddr(), fence)
	if err != nil {
		log.WithError(err).WithField("node_id", nodeID).Warn("Failed to clean conn owner in Redis")
		return false
	}
	if deleted {
		return true
	}
	owner, err := rs.GetConnOwner(context.Background(), nodeID)
	if err != nil {
		log.WithError(err).WithField("node_id", nodeID).Warn("Failed to inspect conn owner after disconnect")
		return false
	}
	return owner.InstanceID == ""
}

func cleanupControlDisconnect(nodeID, canonicalID string, stream ipcpb.HelmsmanControl_ConnectServer, log logging.Logger) {
	for _, rc := range removeCurrentControlConn(nodeID, canonicalID, stream) {
		id := rc.id
		if releaseConnOwnerForDisconnect(id, rc.fence, log) {
			// An announced restart (helmsman's "node_restarting" lifecycle
			// event) holds node health until the reconnect deadline: the
			// data plane keeps serving while the sidecar restarts. The conn
			// owner is still released above so the node can reconnect to any
			// HA instance. Unannounced disconnects (crash, SIGKILL) have no
			// armed window and go unhealthy immediately.
			if deadline, ok := state.DefaultManager().NodePendingReconnect(id); ok && time.Now().Before(deadline) {
				log.WithFields(logging.Fields{
					"node_id":  id,
					"deadline": deadline.Format(time.RFC3339),
				}).Info("Deferring disconnect for announced restart")
				deferPendingDisconnect(id, deadline, log)
			} else {
				state.DefaultManager().MarkNodeDisconnected(id)
			}
		}
		ForgetManagedStreamLastSent(id)
	}
}

// armRestartWindow arms the announced-restart reconnect window for the
// node's registered and canonical identifiers — disconnect cleanup checks
// the window per identifier, and fingerprint resolution can rewrite the id.
// Must run synchronously in the control receive loop: helmsman exits
// ~500ms after announcing, and the async trigger path both races the
// disconnect cleanup and drops triggers from no-longer-current streams.
func armRestartWindow(nodeID string, stream ipcpb.HelmsmanControl_ConnectServer, log logging.Logger) {
	canonicalNodeID := nodeID
	if registry != nil {
		registry.mu.RLock()
		if c, ok := registry.conns[nodeID]; ok && c.stream == stream && c.canonicalID != "" {
			canonicalNodeID = c.canonicalID
		}
		registry.mu.RUnlock()
	}
	deadline := time.Now().Add(state.RestartReconnectWindow())
	state.DefaultManager().SetNodePendingReconnect(nodeID, deadline)
	if canonicalNodeID != nodeID {
		state.DefaultManager().SetNodePendingReconnect(canonicalNodeID, deadline)
	}
	log.WithFields(logging.Fields{
		"node_id":  canonicalNodeID,
		"deadline": deadline.Format(time.RFC3339),
	}).Info("Helmsman announced restart; holding node health for reconnect window")
}

func deferPendingDisconnect(nodeID string, deadline time.Time, log logging.Logger) {
	time.AfterFunc(time.Until(deadline), func() {
		finalizePendingDisconnect(nodeID, log)
	})
}

// finalizePendingDisconnect runs when an announced-restart window expires.
// No-ops when the node already reconnected: the Register path (this
// instance) disarms the window, and a Redis conn owner means the node
// reconnected to another HA instance — a stale unhealthy publish from the
// old owner would fight that instance's healthy state.
func finalizePendingDisconnect(nodeID string, log logging.Logger) {
	if _, ok := state.DefaultManager().NodePendingReconnect(nodeID); !ok {
		return
	}
	if rs := GetRedisStore(); rs != nil {
		if owner, err := rs.GetConnOwner(context.Background(), nodeID); err == nil && owner.InstanceID != "" {
			state.DefaultManager().ClearNodePendingReconnect(nodeID)
			return
		}
	}
	state.DefaultManager().ClearNodePendingReconnect(nodeID)
	state.DefaultManager().MarkNodeDisconnected(nodeID)
	log.WithField("node_id", nodeID).Warn("Announced restart did not reconnect in time; marking node disconnected")
}

// lockedStream serializes Send on a single Helmsman control stream. gRPC's
// ServerStream.SendMsg is NOT safe for concurrent goroutines, but the
// per-message handlers in Connect run as separate `go process*()` goroutines
// and several send on this same bidi stream (the high-frequency
// AuthorizeRelayPull among them). Wrapping the stream once at Connect entry and
// using it everywhere (conn.stream + handler dispatch) funnels every Send
// through this mutex with no call-site changes. Recv is left on the embedded
// stream: concurrent Send+Recv is allowed by gRPC, only concurrent Send+Send is
// not.
type lockedStream struct {
	ipcpb.HelmsmanControl_ConnectServer
	sendMu sync.Mutex
}

func (s *lockedStream) Send(msg *ipcpb.ControlMessage) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.HelmsmanControl_ConnectServer.Send(msg)
}

var registry *Registry
var clipHashResolver func(string) (string, string, error)
var db *sql.DB
var localClusterID string
var localControlCellID string
var servedClusters atomic.Pointer[sync.Map]

// The platform-shared allowlist names the clusters that may serve ANY resolved tenant's durable bytes (the fast path
// in ClusterAccessibleForTenant). It is NOT servedClusters (which only means "this Foghorn operates the cluster"; a
// served cluster can be tenant-dedicated/BYOC). It has two independent sources kept in separate sets so
// revocation works correctly:
//   - platformSharedConfig: operator-declared via FOGHORN_PLATFORM_SHARED_CLUSTERS, set once at startup.
//   - platformSharedDerived: Quartermaster's canonical is_platform_official fact, atomically REPLACED on every
//     refresh (LoadPlatformSharedClusters) so a revoked cluster stops being authorized on the next successful
//     refresh — a set that only accumulated would keep a revoked cluster authorized forever.
//
// Empty (both sets) ⇒ no cluster is platform-shared (that fast path authorizes nothing). Per-tenant, per-cluster serve
// entitlement is enforced separately by ClusterAccessibleForTenant — the tenant's official cluster plus its active
// cluster_cluster_access peer set — so a non-shared cluster still serves a tenant it is specifically entitled to.
var platformSharedConfig atomic.Pointer[sync.Map]
var platformSharedDerived atomic.Pointer[sync.Map]

// platformSharedDerivedAt is the time.Time of the last SUCCESSFUL derived refresh (nil = never). It stores a
// full time.Time (with its MONOTONIC reading) rather than a wall-clock unix stamp so freshness is measured
// monotonically — a backward wall-clock jump cannot extend authority past the TTL. The derived snapshot is
// trusted only within platformSharedDerivedTTL; a stale snapshot HARD-EXPIRES to fail closed.
var platformSharedDerivedAt atomic.Pointer[time.Time]

const platformSharedDerivedTTL = 15 * time.Minute

var chandlerBaseMu sync.RWMutex
var resolvedChandlerBaseURL string

func init() {
	servedClusters.Store(&sync.Map{})
	platformSharedConfig.Store(&sync.Map{})
	platformSharedDerived.Store(&sync.Map{})
}

// AddPlatformSharedCluster designates a cluster as an EXPLICITLY-configured platform-shared edge. This set is
// operator-declared and not revoked at runtime; canonical Quartermaster status flows through the separately-
// refreshed derived set instead (see LoadPlatformSharedClusters).
func AddPlatformSharedCluster(id string) {
	if strings.TrimSpace(id) == "" {
		return
	}
	platformSharedConfig.Load().Store(id, true)
}

// IsPlatformSharedCluster reports whether the cluster is a platform-shared edge per EITHER the explicit config
// or the canonical Quartermaster-derived snapshot.
func IsPlatformSharedCluster(id string) bool {
	if strings.TrimSpace(id) == "" {
		return false
	}
	if _, ok := platformSharedConfig.Load().Load(id); ok {
		return true
	}
	// The Quartermaster-derived snapshot is trusted only while fresh: a hard expiry means a prolonged QM
	// outage denies (fail closed) instead of authorizing on a stale set forever. Measured monotonically.
	at := platformSharedDerivedAt.Load()
	if at == nil || time.Since(*at) > platformSharedDerivedTTL {
		return false
	}
	_, ok := platformSharedDerived.Load().Load(id)
	return ok
}

var quartermasterClient *qmclient.GRPCClient

// servedClustersAPI is the narrow Quartermaster surface LoadServedClusters
// needs. The concrete *qmclient.GRPCClient satisfies it; tests supply a stub.
type servedClustersAPI interface {
	ListServiceClusterAssignments(ctx context.Context, instanceID, serviceType string) (*quartermasterpb.ListServiceClusterAssignmentsResponse, error)
}

// servedClustersClient is a synchronized holder for the Quartermaster client
// used by the periodic served-clusters refresh. The refresh goroutine and the
// reconnect goroutine (which re-sets the client) run concurrently, so this path
// reads/writes the client through an atomic pointer rather than the plain
// quartermasterClient global.
var servedClustersClient atomic.Pointer[qmclient.GRPCClient]

var navigatorClient *navclient.Client
var configSeedApplyAckWriter configSeedApplyAckRecorder
var serverCert serverCertHolder
var errStreamNotCurrent = errors.New("helmsman control stream is not current for node")
var errConfigSeedApplyAckDurabilityUnavailable = errors.New("ConfigSeed apply ACK durability unavailable")
var errConfigSeedApplyAckIdentityMissing = errors.New("ConfigSeed apply ACK requires a registered node and cluster")

// serverCertHolder stores the current server TLS certificate set, updated
// atomically by the CertRefreshLoop when file-based or Navigator-backed TLS
// material changes.
type serverCertHolder struct {
	certs atomic.Pointer[serverCertSet]
}

type serverCertSet struct {
	defaultCert *tls.Certificate
	byName      map[string]*tls.Certificate
}

type configSeedApplyAckRecorder interface {
	Enqueue(context.Context, string, string, *ipcpb.ConfigSeedApplyResult) error
}

func (h *serverCertHolder) StoreBundles(bundles []*ipcpb.TLSCertBundle) error {
	set := &serverCertSet{byName: map[string]*tls.Certificate{}}
	for _, bundle := range bundles {
		if bundle == nil || strings.TrimSpace(bundle.GetCertPem()) == "" || strings.TrimSpace(bundle.GetKeyPem()) == "" {
			continue
		}
		cert, err := tls.X509KeyPair([]byte(bundle.GetCertPem()), []byte(bundle.GetKeyPem()))
		if err != nil {
			return fmt.Errorf("parse TLS bundle %q: %w", bundle.GetBundleId(), err)
		}
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return fmt.Errorf("parse TLS leaf certificate %q: %w", bundle.GetBundleId(), err)
		}
		cert.Leaf = leaf
		certPtr := &cert
		if set.defaultCert == nil {
			set.defaultCert = certPtr
		}
		names := tlsBundleNames(bundle)
		for _, name := range names {
			if !certCoversBundleName(cert.Leaf, name) {
				return fmt.Errorf("TLS bundle %q certificate does not cover configured name %q", bundle.GetBundleId(), name)
			}
			set.byName[name] = certPtr
		}
	}
	if set.defaultCert == nil {
		return fmt.Errorf("no usable TLS bundles")
	}
	h.certs.Store(set)
	return nil
}

func (h *serverCertHolder) Loaded() bool {
	return h.certs.Load() != nil
}

func (h *serverCertHolder) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	set := h.certs.Load()
	if set == nil || set.defaultCert == nil {
		return nil, fmt.Errorf("no TLS certificate loaded")
	}
	serverName := ""
	if hello != nil {
		serverName = strings.Trim(strings.ToLower(hello.ServerName), ".")
	}
	if serverName != "" {
		if cert := set.byName[serverName]; cert != nil {
			return cert, nil
		}
		for pattern, cert := range set.byName {
			if cert != nil && wildcardMatches(pattern, serverName) {
				return cert, nil
			}
		}
	}
	return set.defaultCert, nil
}

func tlsBundleNames(bundle *ipcpb.TLSCertBundle) []string {
	seen := map[string]struct{}{}
	out := []string{}
	add := func(value string) {
		value = strings.Trim(strings.ToLower(strings.TrimSpace(value)), ".")
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	for _, name := range strings.Split(bundle.GetDomain(), ",") {
		add(name)
	}
	for _, name := range bundle.GetSiteAddresses() {
		add(name)
	}
	return out
}

func certCoversBundleName(cert *x509.Certificate, name string) bool {
	if cert == nil {
		return false
	}
	name = strings.Trim(strings.ToLower(strings.TrimSpace(name)), ".")
	if name == "" {
		return true
	}
	if strings.HasPrefix(name, "*.") {
		for _, dnsName := range cert.DNSNames {
			if strings.Trim(strings.ToLower(strings.TrimSpace(dnsName)), ".") == name {
				return true
			}
		}
		return cert.VerifyHostname("foghorn."+strings.TrimPrefix(name, "*.")) == nil
	}
	return cert.VerifyHostname(name) == nil
}

func wildcardMatches(pattern, serverName string) bool {
	pattern = strings.Trim(strings.ToLower(strings.TrimSpace(pattern)), ".")
	serverName = strings.Trim(strings.ToLower(strings.TrimSpace(serverName)), ".")
	if !strings.HasPrefix(pattern, "*.") || serverName == "" {
		return false
	}
	suffix := strings.TrimPrefix(pattern, "*.")
	if !strings.HasSuffix(serverName, "."+suffix) {
		return false
	}
	prefix := strings.TrimSuffix(serverName, "."+suffix)
	return prefix != "" && !strings.Contains(prefix, ".")
}

// validateBootstrapTokenFn allows tests to override token validation.
// In production this calls quartermasterClient.ValidateBootstrapToken.
var validateBootstrapTokenFn func(ctx context.Context, token string) (*quartermasterpb.ValidateBootstrapTokenResponse, error)
var getNodeOwnerFn func(ctx context.Context, nodeID string) (*quartermasterpb.NodeOwnerResponse, error)
var getClusterFn func(ctx context.Context, clusterID string) (*quartermasterpb.InfrastructureCluster, error)

// registerThumbnailServingCellFn is the register-before-mint call to Commodore; overridable in tests. In production it
// dials the live CommodoreClient. Returns registered=false with an error when no client is wired.
var registerThumbnailServingCellFn = func(ctx context.Context, streamID, tenantID, clusterID string) (bool, error) {
	if CommodoreClient == nil {
		return false, errNoCommodoreForThumbnailRegister
	}
	return CommodoreClient.RegisterStreamThumbnailServingCell(ctx, streamID, tenantID, clusterID)
}

var errNoCommodoreForThumbnailRegister = errors.New("no commodore client to register the thumbnail serving cell")

// thumbnailRegistrationCacheTTL bounds how often a live stream's serving cell is re-registered. Live thumbnails refresh
// every few seconds, but the ownership fact (this stream's thumbnails are served by THIS cell) is stable, so
// registering once and caching the positive result collapses a per-refresh cross-service write into at most one write
// per interval. This is a WRITE-AMPLIFICATION optimization ONLY — it is NOT a deletion-safety mechanism, and its value
// is not a safety ceiling. Deletion safety is enforced per attempt by ClaimThumbnailAttempt's tombstone fence
// (thumbnail_publication.go): once a stream is deleted a durable cleanup tombstone exists, and NO further presigned
// upload authority is issued, regardless of whether the ownership registration is still cached. A presigned PUT from an
// attempt that CLAIMED before the tombstone can still land up to thumbnailUploadTTL later; those stragglers are
// reclaimed by the deletion's delayed sweep and are bounded by the upload TTL, not by this cache TTL.
const thumbnailRegistrationCacheTTL = 2 * time.Minute

// thumbnailRegistrationCache memoizes a SUCCESSFUL serving-cell registration per live stream (key = stream id; the cell
// is always this Foghorn's localClusterID). Refusals/errors are NOT cached — they are re-checked on the next mint.
var thumbnailRegistrationCache = cache.New(cache.Options{
	TTL:        thumbnailRegistrationCacheTTL,
	MaxEntries: 200000,
}, cache.MetricsHooks{})

// liveThumbnailMintMayProceed is the register-before-mint fence: a live thumbnail may be minted ONLY once this cell is
// durably registered as a serving cell (so a later deletion can reach the bytes). Returns false — dropping the upload —
// when registration errors (no client / transport) OR is refused (registered=false: the stream was deleted, deletion
// won the serialize race, no object must be created). A prior success is cached for thumbnailRegistrationCacheTTL, so a
// steady stream registers at most once per interval rather than on every refresh.
func liveThumbnailMintMayProceed(streamID, tenantID, clusterID string, logger logging.Logger) bool {
	v, ok, err := thumbnailRegistrationCache.Get(context.Background(), streamID, func(loadCtx context.Context, _ string) (interface{}, bool, error) {
		regCtx, regCancel := context.WithTimeout(loadCtx, 5*time.Second)
		defer regCancel()
		registered, rErr := registerThumbnailServingCellFn(regCtx, streamID, tenantID, clusterID)
		if rErr != nil {
			return false, false, rErr // do not cache transport/no-client errors
		}
		if !registered {
			return false, false, nil // do not cache a refusal (deleted stream) — re-check on the next mint
		}
		return true, true, nil // cache the permanent ownership fact for the TTL
	})
	if err != nil {
		logger.WithError(err).WithFields(logging.Fields{"thumbnail_key": streamID, "tenant_id": tenantID}).
			Warn("Failed to register live thumbnail serving cell; dropping (mint fenced on durable ownership)")
		return false
	}
	if !ok {
		logger.WithFields(logging.Fields{"thumbnail_key": streamID, "tenant_id": tenantID}).
			Info("Live thumbnail serving-cell registration refused (stream deleted); dropping upload")
		return false
	}
	registered, isBool := v.(bool)
	return isBool && registered
}

var geoipCache *cache.Cache

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
	if nodeID, baseURL, ok := getStreamSourceFromLifecycleDB(internalName); ok {
		return nodeID, baseURL, true
	}

	return "", "", false
}

func getStreamSourceFromLifecycleDB(internalName string) (nodeID string, baseURL string, ok bool) {
	if db == nil || internalName == "" {
		return "", "", false
	}
	rows, err := foghorndb.New(db).ListRecentNodeLifecycles(context.Background())
	if err != nil {
		return "", "", false
	}
	for _, row := range rows {
		var update ipcpb.NodeLifecycleUpdate
		if err := json.Unmarshal([]byte(row.Lifecycle), &update); err != nil {
			continue
		}
		stream := update.GetStreams()[internalName]
		if stream == nil || stream.GetInputs() <= 0 || stream.GetReplicated() {
			continue
		}
		return row.NodeID, update.GetBaseUrl(), true
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
var artifactDeletedHandler func(context.Context, *ipcpb.ArtifactDeleted)
var artifactMapUpdatedHandler func(nodeID string)
var catalogDirtyHandler func()

// SetArtifactDeletedHandler registers the callback for node-local
// artifact deletion/eviction reconciliation + DELETED lifecycle emission.
func SetArtifactDeletedHandler(onDeleted func(context.Context, *ipcpb.ArtifactDeleted)) {
	artifactDeletedHandler = onDeleted
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

// SetOnCatalogDirty registers a callback invoked when an authoritative artifact lifecycle
// mutation commits and the durable Commodore catalog projection needs a refresh.
func SetOnCatalogDirty(handler func()) {
	catalogDirtyHandler = handler
}

// NotifyCatalogDirty kicks the artifact reconciler (the sole catalog writer) after a committed
// lifecycle mutation, so the durable catalog reflects the new sync/finalize/freeze state
// promptly instead of waiting for the slow fallback pass. This is a catalog-specific signal:
// unlike NotifyArtifactMapUpdated (a node cache-map change), it fires on lifecycle transitions
// even when no node-map delta occurred.
func NotifyCatalogDirty() {
	if catalogDirtyHandler == nil {
		return
	}
	catalogDirtyHandler()
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
	Relay() foghornrelaypb.FoghornRelayClient
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

func (r *CommandRelay) forward(ctx context.Context, req *foghornrelaypb.ForwardCommandRequest) error {
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
		if _, derr := r.store.DeleteConnOwnerIfMatch(ctx, req.TargetNodeId, owner.InstanceID, owner.GRPCAddr, owner.Fence); derr != nil {
			log.WithError(derr).WithField("node_id", req.TargetNodeId).Debug("Failed to evict stale conn owner during relay")
		}
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

func RelayCommandType(req *foghornrelaypb.ForwardCommandRequest) string {
	switch req.GetCommand().(type) {
	case *foghornrelaypb.ForwardCommandRequest_ConfigSeed:
		return "config_seed"
	case *foghornrelaypb.ForwardCommandRequest_DvrStart:
		return "dvr_start"
	case *foghornrelaypb.ForwardCommandRequest_DvrStop:
		return "dvr_stop"
	case *foghornrelaypb.ForwardCommandRequest_ClipDelete:
		return "clip_delete"
	case *foghornrelaypb.ForwardCommandRequest_DvrDelete:
		return "dvr_delete"
	case *foghornrelaypb.ForwardCommandRequest_VodDelete:
		return "vod_delete"
	case *foghornrelaypb.ForwardCommandRequest_DtshSync:
		return "dtsh_sync"
	case *foghornrelaypb.ForwardCommandRequest_StopSessions:
		return "stop_sessions"
	case *foghornrelaypb.ForwardCommandRequest_InvalidateSessions:
		return "invalidate_sessions"
	case *foghornrelaypb.ForwardCommandRequest_DeactivatePushTargets:
		return "deactivate_push_targets"
	case *foghornrelaypb.ForwardCommandRequest_ProcessingJob:
		return "processing_job"
	case *foghornrelaypb.ForwardCommandRequest_Freeze:
		return "freeze"
	case *foghornrelaypb.ForwardCommandRequest_DesiredStateUpdate:
		return "desired_state_update"
	case *foghornrelaypb.ForwardCommandRequest_ApplyManagedStream:
		return "apply_managed_stream"
	case *foghornrelaypb.ForwardCommandRequest_RetractManagedStream:
		return "retract_managed_stream"
	default:
		return "unknown"
	}
}

func RelayRequestID(req *foghornrelaypb.ForwardCommandRequest) string {
	switch cmd := req.GetCommand().(type) {
	case *foghornrelaypb.ForwardCommandRequest_DvrStart:
		return cmd.DvrStart.GetRequestId()
	case *foghornrelaypb.ForwardCommandRequest_DvrStop:
		return cmd.DvrStop.GetRequestId()
	case *foghornrelaypb.ForwardCommandRequest_ClipDelete:
		return cmd.ClipDelete.GetRequestId()
	case *foghornrelaypb.ForwardCommandRequest_DvrDelete:
		return cmd.DvrDelete.GetRequestId()
	case *foghornrelaypb.ForwardCommandRequest_VodDelete:
		return cmd.VodDelete.GetRequestId()
	case *foghornrelaypb.ForwardCommandRequest_DtshSync:
		return cmd.DtshSync.GetRequestId()
	case *foghornrelaypb.ForwardCommandRequest_ProcessingJob:
		return cmd.ProcessingJob.GetJobId()
	case *foghornrelaypb.ForwardCommandRequest_Freeze:
		return cmd.Freeze.GetRequestId()
	default:
		return ""
	}
}

// SetDB sets the shared database connection for control-plane repositories and lifecycle ledgers.
func SetDB(database *sql.DB) {
	db = database
}

// GetDB returns the package-level DB for cross-package queries.
func GetDB() *sql.DB {
	return db
}

// controlLogger returns the package-level logger, falling back to a fresh
// service logger if the registry has not been initialized yet.
func controlLogger() logging.Logger {
	if registry != nil && registry.log != nil {
		return registry.log
	}
	return logging.NewLoggerWithService("foghorn")
}

// SetLocalClusterID sets the primary cluster ID and marks it as served. It does NOT mark it platform-shared:
// a local CLUSTER_ID can be a tenant-dedicated/BYOC cluster, and inferring platform-shared authority from it
// would let a tenantless node there serve every resolved tenant's media. Platform-shared status comes ONLY from the canonical
// is_platform_official fact (LoadPlatformSharedClusters) or explicit validated config — see nodeMayServeTenant.
func SetLocalClusterID(id string) {
	localClusterID = id
	if strings.TrimSpace(localControlCellID) == "" {
		localControlCellID = id
	}
	servedClusters.Load().Store(id, true)
	clearResolvedChandlerBaseURL()
}

// SetLocalControlCellID records the failure-isolation cell independently of
// the virtual clusters served by authenticated node sessions.
func SetLocalControlCellID(id string) {
	localControlCellID = strings.TrimSpace(id)
	if localControlCellID == "" {
		localControlCellID = strings.TrimSpace(localClusterID)
	}
}

// LoadPlatformSharedClusters refreshes the Quartermaster-DERIVED platform-shared snapshot from the canonical
// is_platform_official fact, fetched through the Quartermaster client with a bounded context. On success it
// ATOMICALLY REPLACES the derived set, so a cluster whose flag was revoked stops being authorized this cycle.
// On any error (client unset, RPC failure) it leaves the previous snapshot in place — a transient QM outage
// must not suddenly deny every platform edge. The explicit config set is untouched.
func LoadPlatformSharedClusters() {
	if quartermasterClient == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := quartermasterClient.ListOfficialClusters(ctx)
	if err != nil || resp == nil {
		return
	}
	applyPlatformSharedRefresh(resp.GetClusters())
}

// applyPlatformSharedRefresh filters to ACTIVE official clusters, atomically replaces the derived snapshot,
// and stamps the refresh time (starting the freshness window). Split out so the filter/replace/expiry logic
// is unit-testable without a live Quartermaster.
func applyPlatformSharedRefresh(clusters []*quartermasterpb.InfrastructureCluster) {
	fresh := &sync.Map{}
	for _, c := range clusters {
		// Defense in depth: only ACTIVE official clusters confer platform-shared authority.
		if !c.GetIsPlatformOfficial() || !c.GetIsActive() {
			continue
		}
		if id := strings.TrimSpace(c.GetClusterId()); id != "" {
			fresh.Store(id, true)
		}
	}
	platformSharedDerived.Store(fresh)
	now := time.Now()
	platformSharedDerivedAt.Store(&now)
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

// LoadServedClusters bulk-loads this instance's active cluster assignments from
// Quartermaster (the schema owner) and atomically swaps the served set. The
// client is read from the synchronized holder so this never races the reconnect
// goroutine. Fail-quiet: a nil client leaves the previous snapshot in place.
func LoadServedClusters() {
	if c := servedClustersClient.Load(); c != nil {
		loadServedClustersFrom(c)
	}
}

// loadServedClustersFrom performs the RPC fetch + snapshot swap against the
// given client. Fail-quiet: a missing instance ID, RPC error, or nil response
// leaves the previous snapshot in place. Takes the client as an argument so it
// is unit-testable with a stub.
func loadServedClustersFrom(client servedClustersAPI) {
	instanceID := strings.TrimSpace(os.Getenv("FOGHORN_INSTANCE_ID"))
	if instanceID == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := client.ListServiceClusterAssignments(ctx, instanceID, "foghorn")
	if err != nil || resp == nil {
		return
	}
	applyServedClustersRefresh(resp.GetClusterIds())
}

// applyServedClustersRefresh atomically replaces the served set from the given
// cluster IDs. localClusterID is always preserved. Split out so the
// filter/replace logic is unit-testable without a live Quartermaster.
func applyServedClustersRefresh(clusterIDs []string) {
	fresh := &sync.Map{}
	if localClusterID != "" {
		fresh.Store(localClusterID, true)
	}
	for _, clusterID := range clusterIDs {
		if clusterID != "" {
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

// StartServedClustersRefresh periodically reloads cluster assignments from Quartermaster.
func StartServedClustersRefresh(ctx context.Context, interval time.Duration, log logging.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			LoadServedClusters()
			LoadPlatformSharedClusters()
			log.WithField("clusters", ServedClustersSnapshot()).Debug("Refreshed served + platform-shared clusters from Quartermaster")
		}
	}
}

// SetClipHashResolver sets the resolver for clip hash lookups
func SetClipHashResolver(resolver func(string) (string, string, error)) {
	clipHashResolver = resolver
}

// SetQuartermasterClient sets the Quartermaster client for edge enrollment and lookups
// resolveNodeFingerprintFn is the seam the Connect handler uses to resolve a node's canonical identity + tenant
// from its fingerprint. Wired to the real Quartermaster client; overridable in tests so registration can reach
// the post-resolution ownership acquisition with an authenticated identity.
var resolveNodeFingerprintFn func(ctx context.Context, req *quartermasterpb.ResolveNodeFingerprintRequest) (*quartermasterpb.ResolveNodeFingerprintResponse, error)

func SetQuartermasterClient(c *qmclient.GRPCClient) {
	quartermasterClient = c
	servedClustersClient.Store(c)
	if c != nil {
		resolveNodeFingerprintFn = c.ResolveNodeFingerprint
	} else {
		resolveNodeFingerprintFn = nil
	}
	clearResolvedChandlerBaseURL()
}

func init() {
	getNodeOwnerFn = func(ctx context.Context, nodeID string) (*quartermasterpb.NodeOwnerResponse, error) {
		if quartermasterClient == nil {
			return nil, status.Error(codes.Unavailable, "quartermaster unavailable")
		}
		return quartermasterClient.GetNodeOwner(ctx, nodeID)
	}
	getClusterFn = func(ctx context.Context, clusterID string) (*quartermasterpb.InfrastructureCluster, error) {
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

	baseDomain := pkgdns.NormalizeDomainScope(cluster.GetBaseUrl())
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

// SetNavigatorClient sets the Navigator client used for cluster TLS bundle retrieval.
func SetNavigatorClient(c *navclient.Client) { navigatorClient = c }

// SetConfigSeedApplyAckWriter installs Foghorn's local durable boundary for
// Helmsman apply results. Navigator delivery happens from the outbox worker.
func SetConfigSeedApplyAckWriter(w configSeedApplyAckRecorder) { configSeedApplyAckWriter = w }

func acceptConfigSeedApplyResult(ctx context.Context, writer configSeedApplyAckRecorder, session NodeSession, ack *ipcpb.ConfigSeedApplyResult) error {
	if writer == nil {
		return errConfigSeedApplyAckDurabilityUnavailable
	}
	if session.NodeID() == "" || strings.TrimSpace(session.ClusterID) == "" {
		return errConfigSeedApplyAckIdentityMissing
	}
	return writer.Enqueue(ctx, session.NodeID(), session.ClusterID, ack)
}

// SetGeoIPCache sets the GeoIP cache for cached lookup usage.
func SetGeoIPCache(c *cache.Cache) { geoipCache = c }

// Server implements HelmsmanControl
type Server struct {
	ipcpb.UnimplementedHelmsmanControlServer
}

func (s *Server) Connect(stream ipcpb.HelmsmanControl_ConnectServer) error {
	// Serialize Send across the goroutine-dispatched handlers below; gRPC
	// SendMsg is not concurrency-safe. Reassigning the parameter means every
	// downstream use (conn.stream storage, stream-identity comparisons, handler
	// dispatch) shares this one wrapper, so all sends funnel through its mutex.
	stream = &lockedStream{HelmsmanControl_ConnectServer: stream}
	var nodeID string
	// connFence is the ownership fence issued to THIS control connection at Register. It is captured
	// per-Connect-invocation, so a stale older connection's goroutine-dispatched handlers stamp the
	// lower fence and a reconnect (new Connect, higher fence) supersedes — the ordering key for
	// whole-node artifact reports across reconnects.
	var connFence int64
	// connProtocolVersion is the control-protocol version THIS connection declared at Register, captured
	// per-Connect-invocation. Freeze admission is bound to this captured value (passed into the handler),
	// NOT re-looked-up by node id later: a reconnect can replace the registry entry between a request's
	// receipt and its goroutine execution, so a re-lookup would admit an old session's request under a new
	// session's version. Binding to the capture keeps admission and the response stream on the same session.
	var connProtocolVersion int32
	// connSession is the IMMUTABLE authenticated identity of THIS connection, captured once at Register (after
	// identity resolution, when the conn is published) and passed BY VALUE into handlers. Handlers attribute work
	// to this captured session rather than re-reading the registry, so a reconnect that replaces the registry
	// entry between a message's receipt and its goroutine execution can never substitute a newer session for work
	// received on this connection.
	var connSession NodeSession
	// On initial message we expect a Register
	for {
		msg, err := stream.Recv()
		if err != nil {
			break
		}
		if nodeID != "" {
			if _, ok := currentControlConn(nodeID, stream); !ok {
				registry.log.WithField("node_id", nodeID).Warn("Closing stale Helmsman control stream")
				return nil
			}
		}
		// Pre-resolution non-dispatchability: every message except Register requires this connection's fully
		// resolved, published identity (connSession, captured once at Register after fingerprint/enrollment
		// resolution). A control message arriving before Register completes has no authenticated node to
		// attribute to or authorize against, so drop it rather than dispatch under an empty identity.
		if _, isRegister := msg.GetPayload().(*ipcpb.ControlMessage_Register); !isRegister && connSession.NodeID() == "" {
			registry.log.Warn("Dropping control message received before node identity resolution")
			continue
		}
		switch x := msg.GetPayload().(type) {
		case *ipcpb.ControlMessage_Register:
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
			// HARD PROTOCOL CUT: reject any sidecar below the minimum at the EARLIEST point — before fence allocation,
			// fingerprint authentication, ownership acquisition, or registry publication — so a not-yet-upgraded (or
			// third-party) sidecar mutates NO state and simply fails closed with FailedPrecondition. There is no code
			// path that admits a below-minimum sidecar. This is also what makes inventory authority session-owned: past
			// here every admitted session is
			// inventory-authoritative, so the processor never trusts a payload field to decide.
			connProtocolVersion = x.Register.GetControlProtocolVersion()
			if connProtocolVersion < MinControlProtocolVersion {
				registry.log.WithFields(logging.Fields{
					"node_id":          nodeID,
					"protocol_version": connProtocolVersion,
					"minimum":          MinControlProtocolVersion,
				}).Warn("Rejecting control registration below the minimum protocol version; upgrade the edge sidecar")
				return status.Errorf(codes.FailedPrecondition, "control protocol version %d is below the minimum %d; upgrade the edge sidecar", connProtocolVersion, MinControlProtocolVersion)
			}
			if proofErr := nodeidentity.VerifyRegistration(x.Register, time.Now()); proofErr != nil {
				registry.log.WithError(proofErr).WithField("node_id", nodeID).Warn("Rejecting control registration without valid node proof")
				return status.Error(codes.Unauthenticated, "node identity proof is invalid")
			}

			cleanup := func() {
				cleanupControlDisconnect(nodeID, canonicalNodeID, stream, registry.log)
			}

			// SECURITY (identity-before-mutation): Redis conn ownership and the Ready=false artifact takeover
			// marker are acquired ONLY on the server-resolved CANONICAL id, and ONLY AFTER fingerprint/enrollment
			// resolution succeeds (below). Acquiring them here on the raw, self-asserted node_id would let a
			// connection that asserts an existing node's id — then fails enrollment — cordon that node's
			// artifacts and steal its ownership fence. Resolution mutates no ownership/readiness state; every
			// such mutation happens post-resolution, keyed on the authenticated identity.

			// Fingerprint-based tenant resolution (pre-provisioned mappings only; no creation here)
			tenantID := ""
			clusterID := ""
			host := ""
			var fingerprintResolutionErr error
			identityResolvedByControlPlane := false
			identityResolvedFromLocalAdmission := false
			durableFingerprintMatch := quartermasterpb.NodeFingerprintMatchSource_NODE_FINGERPRINT_MATCH_SOURCE_UNSPECIFIED
			durableAdmissionEligible := false
			{
				// Build resolver request
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

				// SECURITY: the node's IP is NOT written to state here — a registration that fails
				// fingerprint/enrollment below never authenticated, and writing its connection info on the raw,
				// self-asserted id would let it mutate a victim node's BinHost/liveness/routing/DNS. The write
				// happens only AFTER resolution succeeds, on the canonical id (see SetNodeConnectionInfo below).

				fpReq := &quartermasterpb.ResolveNodeFingerprintRequest{
					PeerIp:                       host,
					NodeIdentityPublicKeyEd25519: append([]byte(nil), x.Register.GetNodeIdentityPublicKeyEd25519()...),
				}
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
				if resolveNodeFingerprintFn != nil {
					release, acquired := acquireNodeAdmissionControlPlaneSlot()
					var resp *quartermasterpb.ResolveNodeFingerprintResponse
					var err error
					if !acquired {
						incNodeAdmissionEvent("preauth", "saturated")
						err = status.Error(codes.ResourceExhausted, "node identity authority concurrency limit reached")
					} else {
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						resp, err = resolveNodeFingerprintFn(ctx, fpReq)
						cancel()
						release()
					}
					if err == nil && resp != nil && bytes.Equal(resp.GetNodeIdentityPublicKeyEd25519(), x.Register.GetNodeIdentityPublicKeyEd25519()) {
						tenantID = resp.TenantId
						if resp.CanonicalNodeId != "" {
							canonicalNodeID = resp.CanonicalNodeId
						}
						identityResolvedByControlPlane = tenantID != ""
						durableFingerprintMatch = resp.GetMatchSource()
						durableAdmissionEligible = durableFingerprintMatch == quartermasterpb.NodeFingerprintMatchSource_NODE_FINGERPRINT_MATCH_SOURCE_MACHINE_ID ||
							durableFingerprintMatch == quartermasterpb.NodeFingerprintMatchSource_NODE_FINGERPRINT_MATCH_SOURCE_MACS
						registry.log.WithFields(logging.Fields{"node_id": canonicalNodeID, "tenant_id": tenantID}).Info("Resolved tenant via fingerprint")
					} else if err == nil && resp != nil {
						fingerprintResolutionErr = status.Error(codes.PermissionDenied, "node identity key does not match enrolled fingerprint")
						registry.log.WithField("node_id", nodeID).Warn("Fingerprint resolved to a different enrolled node key")
					} else if err != nil {
						fingerprintResolutionErr = err
						registry.log.WithError(err).WithField("node_id", nodeID).Debug("Fingerprint resolution did not match; enrollment token may be required")
					}
				}
			}

			fingerprintResolved := tenantID != ""
			tok := strings.TrimSpace(x.Register.GetEnrollmentToken())
			if !fingerprintResolved && (status.Code(fingerprintResolutionErr) == codes.NotFound || status.Code(fingerprintResolutionErr) == codes.PermissionDenied) {
				revokeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				revokeErr := revokeDurableNodeAdmission(revokeCtx, x.Register)
				cancel()
				if revokeErr != nil {
					incNodeAdmissionEvent("revoke", "failure")
					registry.log.WithError(revokeErr).WithField("node_id", nodeID).Warn("Could not remove authoritatively rejected local node admission")
				} else {
					incNodeAdmissionEvent("revoke", "success")
				}
			}
			if !fingerprintResolved && tok == "" && nodeIdentityAuthorityUnavailable(fingerprintResolutionErr, resolveNodeFingerprintFn != nil) {
				lookupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				admission, admissionErr := loadDurableNodeAdmission(lookupCtx, x.Register)
				cancel()
				if admissionErr == nil {
					incNodeAdmissionEvent("load", "success")
					canonicalNodeID = admission.canonicalNodeID
					tenantID = admission.tenantID
					clusterID = admission.clusterID
					fingerprintResolved = true
					identityResolvedFromLocalAdmission = true
					registry.log.WithFields(logging.Fields{
						"node_id": canonicalNodeID, "tenant_id": tenantID, "cluster_id": clusterID,
					}).Info("Recovered previously authenticated node admission from the local media cell")
				} else {
					incNodeAdmissionEvent("load", "failure")
					registry.log.WithError(admissionErr).WithField("node_id", nodeID).Debug("No usable local node admission during identity-authority outage")
				}
			}

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
				release, acquired := acquireNodeAdmissionControlPlaneSlot()
				if !acquired {
					incNodeAdmissionEvent("preauth", "saturated")
					if sendErr := sendControlError(stream, "ENROLLMENT_UNAVAILABLE", "enrollment service is busy; retry safely"); sendErr != nil {
						registry.log.WithError(sendErr).WithField("node_id", nodeID).Debug("Could not report enrollment saturation")
					}
					cleanup()
					return status.Error(codes.ResourceExhausted, "node enrollment concurrency limit reached")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				req := buildBootstrapEdgeNodeRequest(stream.Context(), x.Register, nodeID, peerAddr, tok, localClusterID, ServedClustersSnapshot())
				resp, err := quartermasterClient.BootstrapEdgeNode(ctx, req)
				cancel()
				release()
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
				identityResolvedByControlPlane = tenantID != ""
				durableFingerprintMatch = quartermasterpb.NodeFingerprintMatchSource_NODE_FINGERPRINT_MATCH_SOURCE_UNSPECIFIED
				durableAdmissionEligible = identityResolvedByControlPlane
				registry.log.WithFields(logging.Fields{"node_id": canonicalNodeID, "tenant_id": tenantID, "cluster_id": clusterID}).Info("Edge node enrolled via Quartermaster")
			}
			if fingerprintResolved || identityResolvedByControlPlane {
				proofCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				proofErr := consumeNodeIdentityProofFn(proofCtx, x.Register)
				cancel()
				if proofErr != nil {
					registry.log.WithError(proofErr).WithField("node_id", nodeID).Warn("Rejecting replayed or unpersistable node identity proof")
					cleanup()
					return status.Error(codes.Unauthenticated, "node identity proof was already used")
				}
			}

			if !identityResolvedFromLocalAdmission {
				clusterID = reconcileNodeCluster(stream.Context(), canonicalNodeID, clusterID, registry.log)
			}
			if identityResolvedByControlPlane && durableAdmissionEligible {
				persistCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				persistErr := persistDurableNodeAdmission(persistCtx, canonicalNodeID, tenantID, clusterID, x.Register, durableFingerprintMatch)
				cancel()
				if persistErr != nil {
					incNodeAdmissionEvent("persist", "failure")
					registry.log.WithError(persistErr).WithField("node_id", canonicalNodeID).
						Warn("Could not persist authenticated node admission for control-plane outage recovery")
				} else {
					incNodeAdmissionEvent("persist", "success")
				}
			}

			// Allocate ordering state only after the node has proved possession of its
			// enrolled key and its identity has resolved. Garbage registrations therefore
			// cannot advance the node's durable ownership counter or create ownership state.
			fence, ferr := AllocateNodeControlFence(stream.Context(), canonicalNodeID)
			if ferr != nil {
				registry.log.WithError(ferr).WithField("node_id", nodeID).Error("Failed to allocate node control fence; rejecting registration")
				return status.Error(codes.Unavailable, "control fence allocation failed")
			}
			connFence = fence
			// Keep the connection out of the dispatchable registry until every ownership
			// claim below succeeds.
			newConn := &conn{
				stream:          stream,
				last:            time.Now(),
				rawNodeID:       nodeID,
				peerAddr:        peerAddr,
				relayBaseURL:    strings.TrimRight(x.Register.GetRelayBaseUrl(), "/"),
				protocolVersion: x.Register.GetControlProtocolVersion(),
				fence:           fence,
			}

			// Identity is now RESOLVED and authenticated. Acquire fenced Redis ownership of the CANONICAL id
			// (fail CLOSED on err/superseded) — this is the FIRST ownership mutation, so a registration rejected
			// during resolution above never touched any node's ownership. A strictly-higher fence already owning
			// the canonical id means a concurrent reconnect landed first → reject.
			if rs := GetRedisStore(); rs != nil {
				acquired, err := rs.AcquireConnOwnerFenced(context.Background(), canonicalNodeID, GetInstanceID(), GetAdvertiseAddr(), connFence)
				if err != nil {
					registry.log.WithError(err).WithField("node_id", canonicalNodeID).Error("Failed to acquire canonical conn owner in Redis; rejecting registration (fail closed)")
					cleanup()
					return status.Error(codes.Unavailable, "canonical conn owner acquisition failed")
				}
				if !acquired {
					registry.log.WithField("node_id", canonicalNodeID).Warn("A higher-fence connection already owns the canonical node; rejecting superseded registration")
					cleanup()
					return status.Error(codes.Aborted, "superseded by a newer control connection")
				}
			}

			// Install the fence acceptance floor + publish the fenced Ready=false artifact takeover marker on the
			// CANONICAL id so HA peers re-arm the artifact cordon for this connection. REQUIRED before dispatch
			// visibility; fail CLOSED (release the ownership just acquired) so a marker that can't be durably
			// published never leaves peers routing to a stale owner's Ready=true inventory.
			if ferr := state.DefaultManager().RecordNodeArtifactFence(canonicalNodeID, connFence); ferr != nil {
				releaseConnOwnerForDisconnect(canonicalNodeID, connFence, registry.log)
				cleanup()
				if errors.Is(ferr, state.ErrArtifactInventorySuperseded) {
					registry.log.WithField("node_id", canonicalNodeID).Warn("A higher-fence connection superseded this node's artifact takeover marker; rejecting superseded registration")
					return status.Error(codes.Aborted, "superseded by a newer control connection")
				}
				registry.log.WithError(ferr).WithField("node_id", canonicalNodeID).Error("Failed to publish artifact takeover marker; rejecting registration (fail closed)")
				return status.Error(codes.Unavailable, "artifact takeover marker publish failed")
			}

			// Renewal follows control-connection ownership. Reserve capacity for the publishers that
			// move with this node before making the connection dispatch-visible; otherwise a replica
			// failover could overload the placement job even though every original admission passed.
			releaseConnectionCapacity, capacityErr := reserveLocalIngestNodeConnection(canonicalNodeID, streamident.MaxPublishersPerFoghorn)
			if capacityErr != nil {
				releaseConnOwnerForDisconnect(canonicalNodeID, connFence, registry.log)
				cleanup()
				registry.log.WithError(capacityErr).WithField("node_id", canonicalNodeID).
					Error("Rejecting Helmsman registration: local placement-renewal capacity exhausted")
				return status.Error(codes.ResourceExhausted, "ingest placement-renewal capacity exhausted")
			}

			// Persist resolved tenant/cluster ownership + the peer IP on the CANONICAL (authenticated) node state.
			// This is the FIRST node connection-info write of the registration — deferred to here
			// (post-resolution) so only an authenticated node's canonical id ever mutates liveness/routing state
			// (the same-host-avoidance BinHost included). The self-asserted raw id is UNVERIFIED and is never
			// written to shared state; below, the loop's node id is rebound to the canonical id.
			if clusterID != "" {
				AddServedCluster(clusterID)
			}
			if tenantID != "" || clusterID != "" || host != "" {
				state.DefaultManager().SetNodeConnectionInfo(context.Background(), canonicalNodeID, host, tenantID, clusterID, nil)
			}
			if fingerprintResolved {
				// Fingerprint resolution means Quartermaster already knows this node;
				// do not let a stale activation-probe flag from Redis keep it unroutable.
				state.DefaultManager().SetProbeVerified(canonicalNodeID, true)
			}

			// A successful re-register is the ONLY disarm for an
			// announced-restart window (besides finalization): heartbeats
			// can't disarm it because the pre-restart process keeps
			// heartbeating through its post-announce drain.
			state.DefaultManager().ClearNodePendingReconnect(canonicalNodeID)

			// Identity is fully resolved and the CANONICAL ownership fence is held (the raw asserted id is NOT
			// fenced — a global-monotonic fence would always let a new connection win, so it cannot certify an
			// alias). Stamp the resolved canonical id + cluster onto the (still-unpublished) conn so its
			// NodeSession is COMPLETE and IMMUTABLE, then publish it into the dispatchable registry. Every
			// command-dispatch path reads registry.conns, so nothing can route to this connection (or read an
			// incomplete session from it) before this point.
			newConn.canonicalID = canonicalNodeID
			newConn.clusterID = clusterID
			connSession = newConn.session()

			// From here the node's IDENTITY is its authenticated CANONICAL id. The self-asserted raw id is
			// UNVERIFIED and is NOT an authorization alias: a divergent raw id could belong to another node on a
			// peer Foghorn, and local-registry absence is not HA-safe proof of ownership. Rebind the receive
			// loop's node id to canonical so EVERY subsequent path (registry publish, config dispatch, heartbeat
			// ownership refresh, relay authorization) uses only the proven identity — never the raw assertion.
			rawAssertedID := nodeID
			nodeID = canonicalNodeID
			if rawAssertedID != canonicalNodeID {
				registry.log.WithFields(logging.Fields{"canonical_node_id": canonicalNodeID, "asserted_node_id": rawAssertedID}).
					Info("Node registered under its canonical id; the divergent asserted raw id is not aliased (unverified)")
			}

			// A completed activation proves only that the prior Helmsman/Mist process created the
			// admitted generation's outputs. Before this new connection becomes dispatch-visible,
			// re-arm those exact retained target sets under its higher authenticated fence. This is
			// local durable recovery: it does not resolve current stream policy from Commodore, and it
			// does not substitute targets from a newer authority version onto an older live publisher.
			rearmCtx, rearmCancel := context.WithTimeout(context.Background(), 5*time.Second)
			rearmed, rearmErr := RequeueActivePushTargetActivationsForNode(rearmCtx, canonicalNodeID, connFence, GetInstanceID())
			rearmCancel()
			if rearmErr != nil {
				// Registration still establishes the node's authenticated local control path. The
				// durable Foghorn database is the only dependency here (never Commodore/QM/Purser);
				// if it is unavailable, do not turn that into a control-plane-style edge outage or
				// discard the existing process state. A later reconnect re-attempts the re-arm.
				registry.log.WithError(rearmErr).WithField("node_id", canonicalNodeID).
					Warn("Active push-target restart recovery could not be armed on this registration")
			}
			if rearmed > 0 {
				registry.log.WithFields(logging.Fields{"node_id": canonicalNodeID, "activations": rearmed}).
					Info("Re-armed active push-target outputs for Helmsman restart recovery")
			}

			registry.mu.Lock()
			var retire *conn
			if prevConn, ok := registry.conns[canonicalNodeID]; ok && prevConn != newConn {
				retire = prevConn // a stale dispatcher must not send over the replaced connection
			}
			registry.conns[canonicalNodeID] = newConn
			registry.mu.Unlock()
			releaseConnectionCapacity()
			// Retire the replaced connection OUTSIDE registry.mu (see retireConn): it is no longer the
			// registered conn, so retiring it can't block a new dispatch, only in-flight ones.
			if retire != nil {
				retireConn(retire)
			}
			registry.log.WithField("node_id", canonicalNodeID).Info("Helmsman registered")
			state.DefaultManager().TouchNode(canonicalNodeID, true)

			// Hydrate the managed-stream lastSent map from the sidecar's
			// post-restart applied set so a Foghorn restart followed by a
			// DB-row removal still emits a Retract for the orphaned stream.
			// Runs after canonical-node-ID resolution so the hydrated
			// entries land under the same key connectedNodesInCluster will
			// later report; raw nodeID would miss when fingerprint
			// resolution rewrites the identifier.
			if applied := x.Register.GetAppliedManagedStreams(); len(applied) > 0 {
				hydrationID := canonicalNodeID
				if hydrationID == "" {
					hydrationID = nodeID
				}
				HydrateManagedStreamLastSentForNode(hydrationID, applied)
			}

			// Determine operational mode: DB-persisted wins over Helmsman's request
			operationalMode := resolveOperationalMode(canonicalNodeID, x.Register.GetRequestedMode())
			observeCtx, observeCancel := context.WithTimeout(context.Background(), 3*time.Second)
			observeErr := observeAppliedSeedVersion(observeCtx, canonicalNodeID, x.Register.GetAppliedConfigSeedVersion())
			observeCancel()
			if observeErr != nil {
				fields := logging.Fields{"node_id": canonicalNodeID, "applied_seed_version": x.Register.GetAppliedConfigSeedVersion()}
				if !canRetainAppliedConfigSeed(fingerprintResolved, x.Register.GetAppliedConfigSeedVersion()) {
					registry.log.WithError(observeErr).WithFields(fields).Error("Cannot validate durable ConfigSeed state for node without a usable local seed")
					cleanup()
					return status.Error(codes.Unavailable, "configuration version state is unavailable")
				}
				// Version observation is advisory for an authenticated reconnect.
				// If the local database cannot advance its counter, composition below
				// will fail to allocate/persist a replacement and the node retains its
				// already-applied durable seed.
				registry.log.WithError(observeErr).WithFields(fields).Warn("Could not advance ConfigSeed counter; retaining the node's locally applied seed")
			}
			seed, fallback := composeConfigSeedCandidate(canonicalNodeID, x.Register.GetRoles(), peerAddr, operationalMode, clusterID)
			if tenantID != "" {
				seed.TenantId = tenantID
				fallback.preserveTenant = false
			}
			stripWildcardSiteWithoutTLS(seed)
			seedErr := SendConfigSeedWithFallback(canonicalNodeID, seed, fallback)
			if seedErr != nil {
				fields := logging.Fields{"node_id": canonicalNodeID, "applied_seed_version": x.Register.GetAppliedConfigSeedVersion()}
				if !canRetainAppliedConfigSeed(fingerprintResolved, x.Register.GetAppliedConfigSeedVersion()) {
					state.DefaultManager().SetProbeVerified(canonicalNodeID, false)
					registry.log.WithError(seedErr).WithFields(fields).Error("Rejecting node without a usable ConfigSeed")
					cleanup()
					return status.Error(codes.Unavailable, "node configuration is unavailable")
				}
				// The authenticated reconnect keeps its already-applied local seed.
				// A failure to allocate or persist a replacement must not convert a
				// Foghorn database outage into a media-plane outage.
				registry.log.WithError(seedErr).WithFields(fields).Warn("Node retained its locally applied ConfigSeed")
			}

			// Fresh enrollments without a usable site are not routable.
			if !fingerprintResolved && (seed.GetSite() == nil || seed.GetSite().GetEdgeDomain() == "") {
				state.DefaultManager().SetProbeVerified(canonicalNodeID, false)
				registry.log.WithField("node_id", canonicalNodeID).Warn("Fresh enrollment produced no site config; node marked unverified")
			}

			// Activation: reconnecting nodes (fingerprint resolved) are already verified
			// (ProbeVerified defaults to true in newNodeState). Fresh enrollments get
			// probed — Foghorn verifies the HTTPS endpoint before routing traffic.
			if !fingerprintResolved && seed.GetSite() != nil && seed.GetSite().GetEdgeDomain() != "" {
				state.DefaultManager().SetProbeVerified(canonicalNodeID, false)
				go probeEdgeActivation(canonicalNodeID, seed.GetSite().GetEdgeDomain(), canonicalNodeID)
			}

			// Forward hardware specs to Quartermaster if present
			if quartermasterClient != nil && (x.Register.CpuCores != nil || x.Register.MemoryGb != nil || x.Register.DiskGb != nil) {
				go func(reg *ipcpb.Register, nid string) {
					hwCtx, hwCancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer hwCancel()
					err := quartermasterClient.UpdateNodeHardware(hwCtx, &quartermasterpb.UpdateNodeHardwareRequest{
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
				go func(reg *ipcpb.Register, nid, cid, addr string) {
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
						_, err := quartermasterClient.BootstrapService(capCtx, &quartermasterpb.BootstrapServiceRequest{
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
		case *ipcpb.ControlMessage_ArtifactDeleted:
			// SECURITY: the payload node_id is node-asserted and could name ANOTHER node — the DB callback below
			// uses it to remove an artifact_nodes placement + emit a LOST event, so a connection could evict a
			// peer's placement by naming it. Overwrite it with the authenticated CANONICAL session id BEFORE any
			// consumer reads it (both consumers are spawned after this synchronous write, so there is no race).
			if x.ArtifactDeleted != nil {
				x.ArtifactDeleted.NodeId = connSession.NodeID()
				// A legacy direct-delivery payload has no explicit deletion time;
				// its envelope timestamp supplies the same ordering boundary.
				if x.ArtifactDeleted.GetDeletedAtMs() <= 0 && msg.GetSentAt() != nil {
					x.ArtifactDeleted.DeletedAtMs = msg.GetSentAt().AsTime().UnixMilli()
				}
			}
			if artifactDeletedHandler != nil {
				go artifactDeletedHandler(context.Background(), x.ArtifactDeleted)
			}
		case *ipcpb.ControlMessage_Heartbeat:
			if nodeID != "" {
				canonicalNodeID := nodeID
				registry.mu.Lock()
				c := registry.conns[nodeID]
				if c != nil && c.stream == stream {
					c.last = time.Now()
					if c.canonicalID != "" {
						canonicalNodeID = c.canonicalID
					}
				}
				registry.mu.Unlock()
				if c == nil || c.stream != stream {
					return nil
				}
				state.DefaultManager().TouchNode(nodeID, true)
				if canonicalNodeID != nodeID {
					state.DefaultManager().TouchNode(canonicalNodeID, true)
				}
				// Refresh the verified-applied set for the managed-stream
				// reconciler: Heartbeat carries the sidecar's current
				// applied snapshot so Foghorn detects Mist-add failures
				// (where the wire Apply succeeded but Mist rejected the
				// config) and re-emits on the next reconciler tick.
				if hb := x.Heartbeat; hb != nil {
					UpdateVerifiedAppliedFromHeartbeat(canonicalNodeID, hb.GetAppliedManagedStreams())
				}
				// HA: refresh connection ownership TTL under the fence. If a higher-fence connection has
				// taken over (a reconnect landed on another instance), this stream is superseded and MUST
				// close — that is how the losing connection is torn down after ownership handoff.
				if rs := GetRedisStore(); rs != nil {
					refreshOrRestore := func(nid string) (lost bool) {
						err := rs.RefreshConnOwnerFenced(context.Background(), nid, GetInstanceID(), GetAdvertiseAddr(), connFence)
						switch {
						case err == nil:
							return false
						case errors.Is(err, state.ErrConnOwnerMissing):
							// Key expired (Redis blip / TTL lapse): re-acquire with our fence. If a strictly
							// higher fence beat us to it, we lost ownership and must close.
							acquired, aerr := rs.AcquireConnOwnerFenced(context.Background(), nid, GetInstanceID(), GetAdvertiseAddr(), connFence)
							if aerr != nil {
								registry.log.WithError(aerr).WithField("node_id", nid).Warn("Failed to restore conn owner in Redis")
								return false
							}
							return !acquired
						case errors.Is(err, state.ErrConnOwnerLost):
							return true
						default:
							registry.log.WithError(err).WithField("node_id", nid).Warn("Failed to refresh conn owner TTL")
							return false
						}
					}
					lost := refreshOrRestore(nodeID)
					if canonicalNodeID != nodeID {
						lost = refreshOrRestore(canonicalNodeID) || lost
					}
					if lost {
						registry.log.WithField("node_id", nodeID).Warn("Lost conn ownership to a higher-fence connection; closing superseded stream")
						cleanupControlDisconnect(nodeID, canonicalNodeID, stream, registry.log)
						return status.Error(codes.Aborted, "superseded by a newer control connection")
					}
				}
			}
		case *ipcpb.ControlMessage_DvrStartRequest:
			registry.log.WithFields(logging.Fields{
				"node_id":       nodeID,
				"internal_name": x.DvrStartRequest.GetInternalName(),
			}).Error("Rejected DVRStartRequest from edge control stream; DVR starts must use Foghorn StartDVR")
		case *ipcpb.ControlMessage_DvrProgress:
			// Handle DVR progress updates from storage Helmsman
			go processDVRProgress(x.DvrProgress, connSession, registry.log)
		case *ipcpb.ControlMessage_DvrStopped:
			// Handle DVR completion from storage Helmsman
			go processDVRStopped(x.DvrStopped, connSession, registry.log)
		case *ipcpb.ControlMessage_MistTrigger:
			// Handle MistServer trigger forwarding from Helmsman
			incMistTrigger(x.MistTrigger.GetTriggerType(), x.MistTrigger.GetBlocking(), "received")
			// Arm the announced-restart window synchronously, before the
			// goroutine dispatch: helmsman exits ~500ms after announcing,
			// and once disconnect cleanup runs, processMistTrigger drops
			// the trigger as coming from a stale stream — the announce
			// would be lost exactly when it matters.
			if nu := x.MistTrigger.GetNodeLifecycleUpdate(); nu != nil {
				// Bind the report to THIS connection's authenticated identity: the downstream
				// processor keys memory/Redis/Postgres off nu.NodeId, so a stale/malformed sidecar
				// must not be able to name another node in its payload. Overwrite it with the canonical
				// session id (log a mismatch), never the raw registry key.
				if pid := nu.GetNodeId(); pid != "" && pid != connSession.NodeID() && registry.log != nil {
					registry.log.WithFields(logging.Fields{"conn_node_id": connSession.NodeID(), "payload_node_id": pid}).
						Warn("Node lifecycle payload node_id != connection node_id; using connection identity")
				}
				nu.NodeId = connSession.NodeID()
				// Stamp THIS connection's ownership fence onto the report (the sidecar leaves it 0);
				// Foghorn owns cross-reconnect ordering, so the fence is attached here on receive.
				nu.ArtifactsConnectionFence = connFence
				if nu.GetEventType() == state.EventNodeRestarting {
					armRestartWindow(nodeID, stream, registry.log)
				}
			}
			go processMistTrigger(x.MistTrigger, connSession, stream, registry.log)
		case *ipcpb.ControlMessage_FreezePermissionRequest:
			// Handle freeze permission request from Helmsman (cold storage). Admission is bound to THIS
			// session — the protocol version AND the ownership fence captured at Register — so a goroutine
			// dispatched from a since-superseded connection neither admits under a newer version nor claims
			// an attempt for a connection that no longer owns the node.
			go processFreezePermissionRequest(x.FreezePermissionRequest, connSession.NodeID(), connProtocolVersion, connFence, stream, registry.log)
		case *ipcpb.ControlMessage_FreezeProgress:
			// Handle freeze progress updates from Helmsman
			go processFreezeProgress(x.FreezeProgress, connSession.NodeID(), registry.log)
		case *ipcpb.ControlMessage_CanDeleteRequest:
			// Handle can-delete check from Helmsman (dual-storage architecture)
			go processCanDeleteRequest(x.CanDeleteRequest, connSession.NodeID(), stream, registry.log)
		case *ipcpb.ControlMessage_RelayResolveRequest:
			// Read-through relay resolution: sidecar wants presigned URLs +
			// chapter refs for an asset it's about to serve over
			// /internal/artifact/*. Same control-stream pattern as CanDelete.
			// Bound to the authenticated CANONICAL identity (nodeID was rebound to canonical at Register); the
			// grant is minted here and authorized in AuthorizeRelayPull below against the same canonical id —
			// mint↔authorize bind to one PROVEN identity, and nodeMayServeTenant reads only canonical state.
			go processRelayResolveRequest(x.RelayResolveRequest, connSession.NodeID(), stream, registry.log)
		case *ipcpb.ControlMessage_AuthorizeRelayPullRequest:
			// Serving edge asks Foghorn to authorize an inbound peer-relay pull against the grant Foghorn minted
			// at resolve time, bound to the same authenticated CANONICAL identity as the mint (consistent
			// mint↔authorize), on top of the opaque grant (exact path + hash + 5-min TTL + origin-cluster-only).
			go processAuthorizeRelayPullRequest(x.AuthorizeRelayPullRequest, connSession.NodeID(), stream, registry.log)
		case *ipcpb.ControlMessage_DrainStreamResponse:
			// Correlated completion of a prior-owner drain dispatched by the admission-effects
			// worker. unloaded=false with an empty error means the stream was already absent — an
			// idempotent success. A reported error leaves the obligation leg pending for retry.
			go processDrainStreamResponse(x.DrainStreamResponse, connSession, registry.log)
		case *ipcpb.ControlMessage_ActivatePushTargetsResult:
			// Correlated completion of a push-target activation dispatched by the admission-effects
			// worker. Only converged=true from the connection that is STILL current completes the
			// leg; a reconnect re-arms active outputs under its higher authenticated fence, so a
			// delayed result from the retired connection must not settle the replay.
			go func(result *ipcpb.ActivatePushTargetsResult, session NodeSession) {
				activationNodeID := session.NodeID()
				if !result.GetConverged() {
					registry.log.WithFields(logging.Fields{
						"node_id":     activationNodeID,
						"stream_name": result.GetStreamName(),
						"error":       result.GetError(),
					}).Warn("Push-target activation did not converge; obligation leg stays pending")
					return
				}
				current, ok := currentNodeSession(activationNodeID)
				if !ok || current.Fence != session.Fence {
					registry.log.WithFields(logging.Fields{
						"node_id": activationNodeID, "connection_fence": session.Fence,
					}).Warn("Ignoring push-target activation acknowledgement from a retired control connection")
					return
				}
				ackCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if err := MarkAdmissionActivationDone(ackCtx, activationNodeID, result.GetSourceGeneration(), session.Fence); err != nil {
					registry.log.WithError(err).WithField("node_id", activationNodeID).Warn("Failed to record activation acknowledgement")
				}
			}(x.ActivatePushTargetsResult, connSession)
		case *ipcpb.ControlMessage_SyncComplete:
			// Handle sync completion from Helmsman (dual-storage architecture)
			var nodeClockCompletedAtMs int64
			if msg.GetSentAt() != nil {
				nodeClockCompletedAtMs = msg.GetSentAt().AsTime().UnixMilli()
			}
			go processSyncCompleteAt(x.SyncComplete, connSession.NodeID(), nodeClockCompletedAtMs, registry.log)
		case *ipcpb.ControlMessage_ModeChangeRequest:
			go processModeChangeRequest(x.ModeChangeRequest, connSession.NodeID(), stream, registry.log)
		case *ipcpb.ControlMessage_UpdateApplyResult:
			go processUpdateApplyResult(x.UpdateApplyResult, connSession.NodeID(), registry.log)
		case *ipcpb.ControlMessage_ValidateEdgeTokenRequest:
			go processValidateEdgeToken(msg.GetRequestId(), x.ValidateEdgeTokenRequest, connSession.NodeID(), stream, registry.log)
		case *ipcpb.ControlMessage_EdgeMistAdminSessionRequest:
			go processEdgeMistAdminSession(msg.GetRequestId(), x.EdgeMistAdminSessionRequest, connSession.NodeID(), stream, registry.log)
		case *ipcpb.ControlMessage_ProcessingJobResult:
			if x.ProcessingJobResult.GetStatus() == "cache_update" {
				// Refresh cached overrides before returning so the restarted push
				// reads the latest value from Helmsman.
				processProcessingJobResult(x.ProcessingJobResult, connSession.NodeID(), registry.log)
			} else {
				go processProcessingJobResult(x.ProcessingJobResult, connSession.NodeID(), registry.log)
			}
		case *ipcpb.ControlMessage_ProcessingJobProgress:
			go processProcessingJobProgress(x.ProcessingJobProgress, connSession.NodeID(), registry.log)
		case *ipcpb.ControlMessage_ThumbnailUploadRequest:
			go processThumbnailUploadRequest(msg.GetRequestId(), x.ThumbnailUploadRequest, connSession.NodeID(), connProtocolVersion, stream, registry.log)
		case *ipcpb.ControlMessage_ThumbnailUploaded:
			go processThumbnailUploaded(x.ThumbnailUploaded, connSession.NodeID(), registry.log)
		case *ipcpb.ControlMessage_RecordDvrSegmentRequest:
			go processRecordDVRSegment(x.RecordDvrSegmentRequest, connSession.NodeID(), stream, registry.log)
		case *ipcpb.ControlMessage_MarkDvrSegmentUploaded:
			go processMarkDVRSegmentUploaded(x.MarkDvrSegmentUploaded, connSession.NodeID(), registry.log)
		case *ipcpb.ControlMessage_DvrSegmentDropped:
			go processDVRSegmentDropped(x.DvrSegmentDropped, connSession.NodeID(), registry.log)
		case *ipcpb.ControlMessage_EvictableSegmentsRequest:
			go processEvictableSegmentsRequest(x.EvictableSegmentsRequest, connSession.NodeID(), stream, registry.log)
		case *ipcpb.ControlMessage_RestoreLocalSegmentIndexRequest:
			go processRestoreLocalSegmentIndexRequest(x.RestoreLocalSegmentIndexRequest, connSession.NodeID(), stream, registry.log)
		case *ipcpb.ControlMessage_ConfigSeedApplyResult:
			if x.ConfigSeedApplyResult != nil {
				ackCtx, cancel := context.WithTimeout(stream.Context(), 5*time.Second)
				err := acceptConfigSeedApplyResult(ackCtx, configSeedApplyAckWriter, connSession, x.ConfigSeedApplyResult)
				cancel()
				if err != nil {
					if errors.Is(err, errConfigSeedApplyAckIdentityMissing) {
						return status.Error(codes.FailedPrecondition, err.Error())
					}
					registry.log.WithError(err).WithField("node_id", connSession.NodeID()).Error("Failed to durably accept ConfigSeed apply ACK")
					return status.Error(codes.Unavailable, "ConfigSeed apply ACK persistence failed")
				}
			}
		}
	}
	if nodeID != "" {
		canonicalID := nodeID
		if c, ok := currentControlConn(nodeID, stream); ok && c.canonicalID != "" {
			canonicalID = c.canonicalID
		}
		cleanupControlDisconnect(nodeID, canonicalID, stream, registry.log)
		registry.log.WithField("node_id", nodeID).Info("Helmsman disconnected")
	}
	return nil
}

func processDrainStreamResponse(resp *ipcpb.DrainStreamResponse, session NodeSession, logger logging.Logger) bool {
	nodeID := session.NodeID()
	if resp.GetError() != "" {
		logger.WithFields(logging.Fields{
			"node_id":      nodeID,
			"runtime_name": resp.GetRuntimeName(),
			"error":        resp.GetError(),
		}).Warn("Prior-owner drain reported failure; obligation leg stays pending")
		return false
	}
	// Drain completion is durable once the stream is absent, so unlike
	// push-target activation it does not need to be re-armed after a Mist
	// restart. It still must come from the currently authenticated control
	// connection: a retired stream cannot settle obligations after node
	// ownership has moved to a newer fence.
	current, ok := currentNodeSession(nodeID)
	if !ok || current.Fence != session.Fence {
		logger.WithFields(logging.Fields{
			"node_id": nodeID, "connection_fence": session.Fence,
		}).Warn("Ignoring drain acknowledgement from a retired control connection")
		return false
	}
	ackCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := MarkAdmissionDrainDone(ackCtx, nodeID, resp.GetSourceGeneration()); err != nil {
		logger.WithError(err).WithField("node_id", nodeID).Warn("Failed to record drain acknowledgement")
	}
	return true
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

	owned := make([]removedConn, 0)
	registry.mu.RLock()
	for nodeID, c := range registry.conns {
		owned = append(owned, removedConn{id: nodeID, fence: c.fence})
	}
	registry.mu.RUnlock()

	for _, rc := range owned {
		deleted, err := rs.DeleteConnOwnerIfMatch(ctx, rc.id, instanceID, advertiseAddr, rc.fence)
		if err != nil {
			registry.log.WithError(err).WithField("node_id", rc.id).Warn("Failed to clean conn owner during shutdown")
			continue
		}
		if deleted {
			registry.log.WithField("node_id", rc.id).Info("Cleaned conn owner during shutdown")
		}
	}
}

// sendOnConnBounded runs a gate-fenced send with a caller deadline (the earlier of timeout and ctx).
// stream.Send has no cancellation primitive of its own (gRPC flow control can block it indefinitely),
// so this deadline does not pretend to stop the underlying write goroutine. A timeout is treated as
// evidence the connection is DEAD (a healthy peer drains
// its receive window in milliseconds): the connection is RETIRED — superseded flag set and unlinked
// from the registry — so every queued or future sender aborts instead of piling more commands
// behind the stalled write, and dispatches fail fast with ErrNotConnected until the node
// re-registers on a fresh stream. The one stalled Send may remain until the old transport closes
// and may still be delivered; every worker-driven destructive command is therefore fenced at
// Helmsman by both transport age and exact ingest generation (prior owner for drain, admitted
// generation for activation, ended generation for deactivation).
func sendOnConnBounded(ctx context.Context, nodeID string, c *conn, msg *ipcpb.ControlMessage, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() {
		c.sendGate.Lock()
		defer c.sendGate.Unlock()
		if c.superseded.Load() || !connIsCurrentlyRegistered(nodeID, c) {
			done <- ErrConnSuperseded
			return
		}
		done <- c.stream.Send(msg)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		retireStalledConn(nodeID, c)
		return fmt.Errorf("control-stream send to %s canceled: %w", nodeID, ctx.Err())
	case <-timer.C:
		retireStalledConn(nodeID, c)
		return fmt.Errorf("control-stream send to %s timed out after %s; connection retired", nodeID, timeout)
	}
}

// retireStalledConn retires a connection whose Send exceeded its deadline: flags it superseded (all
// queued/future senders abort) and unlinks it from the registry if it is still the current entry,
// so new dispatches fail fast rather than queueing behind a dead peer.
func retireStalledConn(nodeID string, c *conn) {
	retireConn(c)
	registry.mu.Lock()
	if cur, ok := registry.conns[nodeID]; ok && cur == c {
		delete(registry.conns, nodeID)
	}
	registry.mu.Unlock()
}

// SendLocalDrainStream dispatches a prior-owner drain over the named node's local bidi stream.
// Used by the admission-effects worker when a projection change replaced the publisher on this
// node. Gated to THIS connection generation: the superseded check and the Send are atomic under
// sendGate, so a command is never transmitted over a stream a reconnect already retired.
func SendLocalDrainStream(ctx context.Context, nodeID string, req *ipcpb.DrainStreamRequest) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &ipcpb.ControlMessage{
		Payload: &ipcpb.ControlMessage_DrainStreamRequest{DrainStreamRequest: req},
		SentAt:  timestamppb.Now(),
	}
	return sendOnConnBounded(ctx, nodeID, c, msg, 5*time.Second)
}

// SendDrainStream sends a DrainStreamRequest to the named node, relaying
// via HA if the target's bidi stream is held by a different Foghorn
// instance.
func SendDrainStream(ctx context.Context, nodeID string, req *ipcpb.DrainStreamRequest) error {
	err := SendLocalDrainStream(ctx, nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if relayErr := commandRelay.forward(ctx, &foghornrelaypb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &foghornrelaypb.ForwardCommandRequest_DrainStream{DrainStream: req},
	}); relayErr != nil {
		return relayFailure(err, relayErr)
	}
	return nil
}

func SendLocalDVRStart(nodeID string, req *ipcpb.DVRStartRequest) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &ipcpb.ControlMessage{
		Payload: &ipcpb.ControlMessage_DvrStartRequest{DvrStartRequest: req},
		SentAt:  timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

// SendDVRStart sends a DVRStartRequest to the given node, relaying via HA if needed.
func SendDVRStart(nodeID string, req *ipcpb.DVRStartRequest) error {
	err := SendLocalDVRStart(nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if relayErr := commandRelay.forward(ctx, &foghornrelaypb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &foghornrelaypb.ForwardCommandRequest_DvrStart{DvrStart: req},
	}); relayErr != nil {
		return relayFailure(err, relayErr)
	}
	return nil
}

func SendLocalDVRStop(nodeID string, req *ipcpb.DVRStopRequest) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &ipcpb.ControlMessage{
		Payload: &ipcpb.ControlMessage_DvrStopRequest{DvrStopRequest: req},
		SentAt:  timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

// SendDVRStop sends a DVRStopRequest to the given node, relaying via HA if needed.
func SendDVRStop(nodeID string, req *ipcpb.DVRStopRequest) error {
	err := SendLocalDVRStop(nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if relayErr := commandRelay.forward(ctx, &foghornrelaypb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &foghornrelaypb.ForwardCommandRequest_DvrStop{DvrStop: req},
	}); relayErr != nil {
		return relayFailure(err, relayErr)
	}
	return nil
}

func SendLocalClipDelete(nodeID string, req *ipcpb.ClipDeleteRequest) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &ipcpb.ControlMessage{
		Payload: &ipcpb.ControlMessage_ClipDelete{ClipDelete: req},
		SentAt:  timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

// SendClipDelete sends a ClipDeleteRequest to the given node, relaying via HA if needed.
func SendClipDelete(nodeID string, req *ipcpb.ClipDeleteRequest) error {
	err := SendLocalClipDelete(nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if relayErr := commandRelay.forward(ctx, &foghornrelaypb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &foghornrelaypb.ForwardCommandRequest_ClipDelete{ClipDelete: req},
	}); relayErr != nil {
		return relayFailure(err, relayErr)
	}
	return nil
}

func SendLocalDVRDelete(nodeID string, req *ipcpb.DVRDeleteRequest) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &ipcpb.ControlMessage{
		Payload: &ipcpb.ControlMessage_DvrDelete{DvrDelete: req},
		SentAt:  timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

// SendDVRDelete sends a DVRDeleteRequest to the given node, relaying via HA if needed.
func SendDVRDelete(nodeID string, req *ipcpb.DVRDeleteRequest) error {
	err := SendLocalDVRDelete(nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if relayErr := commandRelay.forward(ctx, &foghornrelaypb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &foghornrelaypb.ForwardCommandRequest_DvrDelete{DvrDelete: req},
	}); relayErr != nil {
		return relayFailure(err, relayErr)
	}
	return nil
}

func SendLocalVodDelete(nodeID string, req *ipcpb.VodDeleteRequest) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &ipcpb.ControlMessage{
		Payload: &ipcpb.ControlMessage_VodDelete{VodDelete: req},
		SentAt:  timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

// SendVodDelete sends a VodDeleteRequest to the given node, relaying via HA if needed.
func SendVodDelete(nodeID string, req *ipcpb.VodDeleteRequest) error {
	err := SendLocalVodDelete(nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if relayErr := commandRelay.forward(ctx, &foghornrelaypb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &foghornrelaypb.ForwardCommandRequest_VodDelete{VodDelete: req},
	}); relayErr != nil {
		return relayFailure(err, relayErr)
	}
	return nil
}

// CreateIngestSession durably mints — or idempotently returns — the ingest session for
// a publisher connection identified by (tenant, node, MistServer connector PID), fenced
// by the start trigger's UUID/time. Foghorn calls this on an ACCEPTED PUSH_REWRITE,
// BEFORE launching any async DVR work, and binds the recording to the returned session
// id. A same-node reconnect is a NEW connector process → a new PID → a new session, so
// its recording is genuinely fresh.
//
// Idempotency + PID-reuse fencing, serialized by a (tenant,node,pid) advisory lock so the
// identity comparison always runs against a committed predecessor (never two inserters
// racing) and the partial-unique active-PID index is never violated: a repeat PUSH_REWRITE
// for the same connection (same trigger UUID, same stream) returns the existing session; a
// PID the OS reused for a NEWER connector (different UUID, later start) ends the stale
// still-active session (its PUSH_INPUT_CLOSE was lost) AND atomically claims its orphaned
// DVR's stop, then mints fresh; a DIFFERENT-UUID OLDER trigger is REJECTED (a stale
// reordered trigger for a superseded connection must not borrow the replacement generation);
// a same-UUID different-stream is rejected. Fails CLOSED (returns an error) when db is nil —
// a push cannot be admitted without a durable generation.
// dvrIntent is the durable DVR start intent (JSON of the StartDVR inputs) for a
// record:true session, or nil for a non-recording session. It is written in the SAME
// insert as the session so a record:true stream's recording obligation is durable
// before the push is approved — DVRIntentRecovery replays it if the async StartDVR is
// lost to a crash. On a duplicate/idempotent path the existing row's intent is kept.
// IngestSessionOutcome is the typed lifecycle result of CreateIngestSession, so the caller can
// distinguish an admissible session from a trigger whose session has ALREADY ENDED (which must be
// denied, not admitted — see the AlreadyEnded value). Only meaningful when the returned error is nil.
type IngestSessionOutcome int

// IngestAuthoritySnapshot identifies the immutable signed policy generation
// accepted for one publisher session. A nil/absent snapshot is retained only
// for connected-mode and pre-v0.3.0 compatibility; outage admission always
// supplies a complete snapshot.
type IngestAuthoritySnapshot struct {
	MediaAuthorityID       string
	MediaAuthorityVersion  int64
	TenantAuthorityVersion int64
	ProcessesJSON          string
	CapacityMaxStreams     int32
}

const (
	// IngestSessionActive: an OPEN session backs this push — freshly minted, an idempotent
	// duplicate of the same connection, or a same-node PID-reuse replacement. The push may be
	// admitted; the returned id is the durable generation.
	IngestSessionActive IngestSessionOutcome = iota + 1
	// IngestSessionAlreadyEnded: this EXACT trigger's connector has already been closed (its own
	// PUSH_INPUT_CLOSE won the race and ended it first). The returned id is the existing ended
	// session's when one was already inserted; it is empty when a durable close-before-insert
	// tombstone prevented any session from being minted. In either case the caller MUST deny/no-op
	// admission side effects (input state, capacity, drain, Decklog, DVR) for a connector that is
	// already gone.
	IngestSessionAlreadyEnded
	// IngestSessionRejectedDuplicate: a DIFFERENT active publisher already holds this stream (on
	// this or another node / replica). The DB, under the stream-scoped advisory lock, is the
	// authority for single-publisher-per-stream; the caller MUST deny the push. A genuine reconnect
	// succeeds once the incumbent's session is ended by its close or the STREAM_END reaper. Returned
	// id is empty.
	IngestSessionRejectedDuplicate
	// IngestSessionRejectedCapacity means the tenant already has the maximum
	// number of active publisher sessions allowed by the authority snapshot.
	IngestSessionRejectedCapacity
)

// ingestStreamAdvisoryLockKey is the (tenant, stream) advisory-lock key that serializes ingest
// admission (CreateIngestSession) and close (FinalizeIngestSessionClose) across replicas. BOTH MUST
// use this exact key so a close-before-insert fully serializes against the mint. Length-prefixed,
// colon-joined so distinct (tenant, stream) pairs never collide; NUL is avoided because hashtext()
// rejects 0x00.
func ingestStreamAdvisoryLockKey(tenantID, internalName string) string {
	return strconv.Itoa(len(tenantID)) + ":" + tenantID + ":" + strconv.Itoa(len(internalName)) + ":" + internalName
}

func ingestTenantCapacityAdvisoryLockKey(tenantID string) string {
	return "tenant-capacity:" + strconv.Itoa(len(tenantID)) + ":" + tenantID
}

func CreateIngestSession(ctx context.Context, tenantID, nodeID, internalName string, connectorPID int64, triggerUUID string, startedAtMillis int64, dvrIntent []byte, ingestClusterID string, logger logging.Logger, authority ...IngestAuthoritySnapshot) (string, IngestSessionOutcome, error) {
	if db == nil {
		// Fail CLOSED: without a DB we cannot persist the session identity the source-presence
		// fence and DVR obligation depend on, so the caller must deny the push rather than admit
		// one with no durable generation. (Production always wires db; tests inject a mock DB.)
		return "", 0, fmt.Errorf("ingest session cannot be persisted: no database configured")
	}
	if tenantID == "" || nodeID == "" || connectorPID <= 0 || triggerUUID == "" || startedAtMillis <= 0 {
		// Fail closed on any missing Mist identity: every Mist HTTP trigger carries X-PID,
		// X-Trigger-UUID and X-Trigger-UnixMillis, so an absent value is a malformed/non-Mist
		// request that must not mint an unidentifiable session (the schema CHECKs reject it too).
		return "", 0, fmt.Errorf("ingest session missing required identity: tenant=%q node=%q pid=%d trigger_uuid=%q started_millis=%d", tenantID, nodeID, connectorPID, triggerUUID, startedAtMillis)
	}
	if len(authority) > 1 {
		return "", 0, errors.New("ingest session accepts at most one authority snapshot")
	}
	var authoritySnapshot *IngestAuthoritySnapshot
	if len(authority) == 1 {
		authoritySnapshot = &authority[0]
		hasAuthority := strings.TrimSpace(authoritySnapshot.MediaAuthorityID) != "" || authoritySnapshot.MediaAuthorityVersion != 0 || authoritySnapshot.TenantAuthorityVersion != 0
		if hasAuthority && (strings.TrimSpace(authoritySnapshot.MediaAuthorityID) == "" || authoritySnapshot.MediaAuthorityVersion <= 0 || authoritySnapshot.TenantAuthorityVersion <= 0) {
			return "", 0, errors.New("ingest authority snapshot requires identity and positive object/tenant versions")
		}
		if !hasAuthority && strings.TrimSpace(authoritySnapshot.ProcessesJSON) == "" && authoritySnapshot.CapacityMaxStreams <= 0 {
			return "", 0, errors.New("empty ingest session snapshot")
		}
		if authoritySnapshot.CapacityMaxStreams < 0 {
			return "", 0, errors.New("ingest session capacity limit cannot be negative")
		}
	}
	// The tenant advisory lock serializes capped count+insert decisions only when each waiter sees
	// rows committed before it acquired the lock. Make that PostgreSQL dependency explicit: under
	// REPEATABLE READ a transaction could retain the snapshot it took while waiting and miss the
	// preceding admission.
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return "", 0, fmt.Errorf("begin ingest-session tx: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			logger.WithError(rbErr).Warn("Failed to roll back ingest-session tx")
		}
	}()

	q := foghorndb.New(tx)
	// Serialize every tenant admission before taking the narrower stream lock.
	// Capped and unlimited authority generations can overlap during rollout or
	// an outage; taking this lock unconditionally keeps a capped count+insert
	// atomic against either generation. PostgreSQL is the shared authority, so
	// cache state can neither revoke nor strand a publisher.
	if lockErr := q.LockIngestStream(ctx, ingestTenantCapacityAdvisoryLockKey(tenantID)); lockErr != nil {
		return "", 0, fmt.Errorf("acquire ingest-session tenant capacity lock: %w", lockErr)
	}

	// STREAM-scoped advisory lock: serialize ALL admissions for this (tenant, stream) — across
	// Foghorn REPLICAS, since the lock is held in the shared per-cell database. This is the HA
	// authority the process-local StreamRegistry cannot be: two replicas handling PUSH_REWRITEs for
	// the same stream on different nodes contend here, so exactly one wins the admission decision.
	// A (node, PID)-scoped lock could not provide this — two nodes' admissions for one stream would
	// never contend on it. A connector PID maps to exactly one stream, so the stream lock also
	// subsumes same-PID serialization. FinalizeIngestSessionClose takes the SAME lock, so a close-before-insert fully
	// serializes against this mint (the close's tombstone is committed before the mint reads it, or
	// this mint commits before the close runs and the close ends the row). Released on commit/rollback.
	if lockErr := q.LockIngestStream(ctx, ingestStreamAdvisoryLockKey(tenantID, internalName)); lockErr != nil {
		return "", 0, fmt.Errorf("acquire ingest-session stream lock: %w", lockErr)
	}

	// 1. Has THIS exact trigger EXECUTION (keyed (tenant, node, UUID); the UUID identifies one trigger
	// firing and is stable across its blocking-trigger retries) already minted a session? Resolves
	// idempotent re-fires and the already-ended race without consulting the stream incumbent.
	uuidRow, uuidErr := q.LockIngestSessionByTrigger(ctx, foghorndb.LockIngestSessionByTriggerParams{
		TenantID: tenantID, NodeID: nodeID, StartTriggerUuid: triggerUUID,
	})
	switch {
	case uuidErr == nil:
		uuidID, uuidStream, uuidEnded, uuidPID := uuidRow.ID, uuidRow.StreamInternalName, uuidRow.Ended, uuidRow.ConnectorPid
		if uuidStream != internalName || uuidPID != connectorPID {
			// Same trigger UUID bound to a DIFFERENT stream or connector PID — an anomaly (a UUID
			// identifies one trigger execution, which belongs to one connector admitting one
			// stream). A same-UUID/different-PID replay is not a retry of the persisted connection;
			// fail closed rather than resume a row whose identity does not match the caller.
			return "", IngestSessionRejectedDuplicate, tx.Commit()
		}
		if uuidEnded {
			// This exact trigger already CLOSED (its own PUSH_INPUT_CLOSE won the race). Deny — never
			// re-admit an already-gone connector.
			return uuidID, IngestSessionAlreadyEnded, tx.Commit()
		}
		// Idempotent duplicate PUSH_REWRITE for the SAME still-open connection.
		return uuidID, IngestSessionActive, tx.Commit()
	case errors.Is(uuidErr, sql.ErrNoRows):
		// New trigger UUID — fall through to the stream-incumbent decision.
	default:
		return "", 0, fmt.Errorf("look up ingest session by trigger UUID: %w", uuidErr)
	}

	// 2. Is a DIFFERENT publisher already the ACTIVE source for this STREAM? At most one row here
	// (uq_foghorn_ingest_sessions_active_per_stream). Under the stream lock this is the authoritative
	// single-publisher decision.
	var staleStopClaims []DVRStopClaim // dispatched AFTER commit (PID-reuse orphan stop)
	var staleSessionID, staleNodeID string
	inc, incErr := q.LockActiveStreamIngestSession(ctx, foghorndb.LockActiveStreamIngestSessionParams{TenantID: tenantID, StreamInternalName: internalName})
	switch {
	case incErr == nil:
		incID, incNode, incPID, incMillis := inc.ID, inc.NodeID, inc.ConnectorPid, inc.StartedAtUnixMillis
		// An incumbent holds the stream. The ONLY case that supersedes it is the OS reusing this
		// exact (node, PID) for a NEWER connector while the incumbent's row still lingers active
		// (its close was lost) — a genuine same-connection-slot replacement. Anything else (a
		// different node, a different PID on the same node, or an older/equal trigger time) is a
		// duplicate publisher and is REJECTED to protect the incumbent; a real reconnect is admitted
		// once the incumbent is ended by its close or the STREAM_END reaper.
		if incNode == nodeID && incPID == connectorPID && startedAtMillis > incMillis {
			if endErr := q.EndSupersededPIDIngestSession(ctx, foghorndb.EndSupersededPIDIngestSessionParams{SessionID: incID, EndedAtUnixMillis: sql.NullInt64{Int64: startedAtMillis, Valid: true}}); endErr != nil {
				return "", 0, fmt.Errorf("end stale ingest session on PID reuse: %w", endErr)
			}
			claims, claimErr := ClaimDVRStops(ctx, tx, `ingest_generation = $1::uuid AND tenant_id::text = $2`, incID, tenantID)
			if claimErr != nil {
				return "", 0, fmt.Errorf("claim stale DVR stop on PID reuse: %w", claimErr)
			}
			staleStopClaims = claims
			staleSessionID = incID
			staleNodeID = incNode
		} else {
			return "", IngestSessionRejectedDuplicate, tx.Commit()
		}
	case errors.Is(incErr, sql.ErrNoRows):
		// No active publisher — mint fresh below.
	default:
		return "", 0, fmt.Errorf("look up active stream ingest session: %w", incErr)
	}

	// 2b. Close-before-insert tombstone: did a PUSH_INPUT_CLOSE for THIS connector already commit
	// while no session row existed (concurrent dispatch / WAL redelivery processed the close before
	// this rewrite)? Under the shared stream lock, a tombstone whose close event is at or after this
	// session's start means the publisher is already gone — deny rather than mint an active session
	// for a dead connector. The event-time bound (close >= start) keeps a genuine LATER reconnect on
	// a reused (node, PID) — which starts after the old close — from being blocked.
	tombstoned, tsErr := q.IngestCloseTombstoneExists(ctx, foghorndb.IngestCloseTombstoneExistsParams{
		TenantID: tenantID, NodeID: nodeID, ConnectorPid: connectorPID,
		StreamInternalName: internalName, CloseUnixMillis: startedAtMillis,
	})
	if tsErr != nil {
		return "", 0, fmt.Errorf("check ingest close tombstone: %w", tsErr)
	}
	if tombstoned {
		if staleSessionID != "" {
			revision, revisionErr := nextSourceRevision(ctx, tx, tenantID, internalName)
			if revisionErr != nil {
				return "", 0, revisionErr
			}
			if enqueueErr := enqueueOfflineEffectTx(ctx, tx, tenantID, internalName, staleNodeID, staleSessionID, revision, OfflineEffectIntent{
				SetNodeOffline: true, TeardownStream: true, BroadcastOffline: true,
			}); enqueueErr != nil {
				return "", 0, enqueueErr
			}
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return "", 0, fmt.Errorf("commit tombstoned ingest decision: %w", commitErr)
		}
		DispatchDVRStops(staleStopClaims, logger)
		return "", IngestSessionAlreadyEnded, nil
	}

	// Count after PID supersession so replacing a stale connector for the same
	// stream is capacity-neutral. The tenant lock makes count+insert atomic
	// against admissions for different streams.
	if authoritySnapshot != nil && authoritySnapshot.CapacityMaxStreams > 0 {
		activeCount, countErr := q.CountActiveTenantIngestSessions(ctx, tenantID)
		if countErr != nil {
			return "", 0, fmt.Errorf("count active tenant ingest sessions: %w", countErr)
		}
		if activeCount >= authoritySnapshot.CapacityMaxStreams {
			// A newer connector on the same (node, PID) proves the old generation is stale even
			// when the successor cannot be admitted. Retire that generation and its obligations;
			// rolling this transaction back would resurrect a publisher Mist has already replaced.
			if staleSessionID != "" {
				revision, revisionErr := nextSourceRevision(ctx, tx, tenantID, internalName)
				if revisionErr != nil {
					return "", 0, revisionErr
				}
				if enqueueErr := enqueueOfflineEffectTx(ctx, tx, tenantID, internalName, staleNodeID, staleSessionID, revision, OfflineEffectIntent{
					SetNodeOffline: true, TeardownStream: true, BroadcastOffline: true,
				}); enqueueErr != nil {
					return "", 0, enqueueErr
				}
			}
			if commitErr := tx.Commit(); commitErr != nil {
				return "", 0, fmt.Errorf("commit capacity-rejected ingest decision: %w", commitErr)
			}
			DispatchDVRStops(staleStopClaims, logger)
			return "", IngestSessionRejectedCapacity, nil
		}
	}

	// 3. Mint. No ON CONFLICT clause: the stream lock has already serialized this decision, and the
	// partial unique (tenant, stream) / (tenant, node, PID) / (tenant, node, UUID) indexes are the
	// durable backstop — a violation here means a bug or lock-hash collision, so surface it (fail
	// closed) rather than silently absorb it.
	var newID string
	var insErr error
	if authoritySnapshot == nil {
		newID, insErr = q.InsertIngestSession(ctx, foghorndb.InsertIngestSessionParams{
			TenantID: tenantID, NodeID: nodeID, StreamInternalName: internalName, ConnectorPid: connectorPID,
			StartTriggerUuid: triggerUUID, StartedAtUnixMillis: startedAtMillis,
			DvrIntent: sql.NullString{String: string(dvrIntent), Valid: len(dvrIntent) > 0}, IngestClusterID: ingestClusterID,
		})
	} else {
		newID, insErr = q.InsertIngestSessionWithAuthority(ctx, foghorndb.InsertIngestSessionWithAuthorityParams{
			TenantID: tenantID, NodeID: nodeID, StreamInternalName: internalName, ConnectorPid: connectorPID,
			StartTriggerUuid: triggerUUID, StartedAtUnixMillis: startedAtMillis,
			DvrIntent: sql.NullString{String: string(dvrIntent), Valid: len(dvrIntent) > 0}, IngestClusterID: ingestClusterID,
			MediaAuthorityID:       sql.NullString{String: strings.TrimSpace(authoritySnapshot.MediaAuthorityID), Valid: strings.TrimSpace(authoritySnapshot.MediaAuthorityID) != ""},
			MediaAuthorityVersion:  sql.NullInt64{Int64: authoritySnapshot.MediaAuthorityVersion, Valid: authoritySnapshot.MediaAuthorityVersion > 0},
			TenantAuthorityVersion: sql.NullInt64{Int64: authoritySnapshot.TenantAuthorityVersion, Valid: authoritySnapshot.TenantAuthorityVersion > 0},
			ProcessesJson:          authoritySnapshot.ProcessesJSON,
			CapacityMaxStreams:     authoritySnapshot.CapacityMaxStreams,
		})
	}
	if insErr != nil {
		return "", 0, fmt.Errorf("insert ingest session: %w", insErr)
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return "", 0, fmt.Errorf("commit ingest session: %w", commitErr)
	}
	// The stale session's orphaned DVR stop (if any) is durable now — dispatch best-effort.
	DispatchDVRStops(staleStopClaims, logger)
	return newID, IngestSessionActive, nil
}

// DVR command generations Foghorn stamps on DVRStart/DVRStop. A stop's generation is
// strictly higher than a start's, so Helmsman — which applies highest-generation-wins
// with a stop tombstone — rejects a start that a newer stop has already superseded.
// This closes the stop-overtakes-start race: a start that committed status='starting'
// but has not yet sent DVRStart can be superseded by a stop; if the stop reaches
// Helmsman first, the late start is rejected idempotently instead of creating a live
// writer behind a terminal Foghorn row.
const (
	DVRStartCommandGeneration int64 = 1
	DVRStopCommandGeneration  int64 = 2
)

// DVRStartLockKey is the (stream, source node) key startDVR takes a Postgres advisory
// lock on to serialize concurrent starts for the same (stream, source node)
// (duplicate-start prevention). It is NODE-scoped, NOT an ingest-session identity: it
// cannot distinguish a same-node reconnect from the original session. COLON-joined (NOT
// NUL: PostgreSQL text passed to hashtext() rejects 0x00 with "invalid byte sequence for
// encoding UTF8", which would fail EVERY real startDVR at its advisory lock). A hash
// collision between distinct pairs only over-serializes two unrelated starts — never
// incorrect. Length-prefixed so "a:b"+"c" and "a"+"b:c" cannot collide as raw text.
func DVRStartLockKey(internalName, sourceNodeID string) string {
	return strconv.Itoa(len(internalName)) + ":" + internalName + ":" + sourceNodeID
}

// dvrStopQueryer is satisfied by both *sql.DB and *sql.Tx, so a stop can be claimed
// standalone or inside a larger transaction (e.g. atomically with a session end).
type dvrStopQueryer = foghorndb.DBTX

// DVRStopClaim is one durably-claimed stop obligation ready to dispatch.
type DVRStopClaim struct {
	DVRHash       string
	StorageNodeID string
}

// ClaimDVRStops is the DURABLE half of the single claim-before-send DVR-stop primitive
// used by every stop path (StopDVR RPC, STREAM_END backstop, PUSH_INPUT_CLOSE finalizer).
// It transitions every ACTIVE dvr row matched by whereSQL to status='stopping' +
// dvr_start_dispatch.state='stop_pending' — the durable obligation the recovery drain
// re-sends until DVRStopped acks — and returns the claims to dispatch. Two invariants it
// enforces that a bare "send then write status" cannot:
//
//   - claim BEFORE send: the obligation is durable first, so a lost/accepted send is
//     redriven by DVRStartingRecoveryJob instead of vanishing.
//   - active-status guard: a row a fast DVRStopped already moved terminal is NOT
//     re-claimed, so a stop can never overwrite a completed recording back to 'stopping'.
//
// whereSQL is a TRUSTED internal fragment (never user input); its placeholders continue
// from the fixed guard, so its first placeholder is $1. Pass a *sql.Tx as q to claim
// inside a larger transaction; the caller dispatches with DispatchDVRStops AFTER commit.
func ClaimDVRStops(ctx context.Context, q dvrStopQueryer, whereSQL string, args ...interface{}) ([]DVRStopClaim, error) {
	dbq := foghorndb.New(q)
	var claims []DVRStopClaim
	switch whereSQL {
	case `artifact_hash = $1 AND tenant_id = $2`, `artifact_hash = $1 AND tenant_id::text = $2`:
		values, err := dvrStopStringArgs(args, 2)
		if err != nil {
			return nil, err
		}
		rows, err := dbq.ClaimDVRStopByArtifact(ctx, foghorndb.ClaimDVRStopByArtifactParams{ArtifactHash: values[0], TenantID: values[1]})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			claims = append(claims, DVRStopClaim{DVRHash: row.ArtifactHash, StorageNodeID: row.StorageNodeID})
		}
		return claims, nil
	case `ingest_generation = $1::uuid AND tenant_id::text = $2`:
		values, err := dvrStopStringArgs(args, 2)
		if err != nil {
			return nil, err
		}
		rows, err := dbq.ClaimDVRStopsForGeneration(ctx, foghorndb.ClaimDVRStopsForGenerationParams{IngestGeneration: values[0], TenantID: values[1]})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			claims = append(claims, DVRStopClaim{DVRHash: row.ArtifactHash, StorageNodeID: row.StorageNodeID})
		}
		return claims, nil
	case `stream_internal_name = $1 AND dvr_start_dispatch->>'source_node_id' = $2 AND tenant_id::text = $3
		 AND (ingest_generation IS NULL OR NOT EXISTS (
		     SELECT 1 FROM foghorn.ingest_sessions s
		      WHERE s.id = foghorn.artifacts.ingest_generation AND s.ended_at IS NULL))`:
		values, err := dvrStopStringArgs(args, 3)
		if err != nil {
			return nil, err
		}
		rows, err := dbq.ClaimDVRStopsForEndedSource(ctx, foghorndb.ClaimDVRStopsForEndedSourceParams{StreamInternalName: sql.NullString{String: values[0], Valid: true}, DvrStartDispatch: sql.NullString{String: values[1], Valid: true}, TenantID: values[2]})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			claims = append(claims, DVRStopClaim{DVRHash: row.ArtifactHash, StorageNodeID: row.StorageNodeID})
		}
		return claims, nil
	default:
		return nil, fmt.Errorf("unsupported DVR stop predicate %q", whereSQL)
	}
}

func dvrStopStringArgs(args []interface{}, want int) ([]string, error) {
	if len(args) != want {
		return nil, fmt.Errorf("DVR stop predicate requires %d arguments, got %d", want, len(args))
	}
	values := make([]string, want)
	for i, arg := range args {
		value, ok := arg.(string)
		if !ok {
			return nil, fmt.Errorf("DVR stop argument %d has type %T, want string", i+1, arg)
		}
		values[i] = value
	}
	return values, nil
}

// DispatchDVRStops is the SEND half of the primitive: a best-effort immediate DVRStop per
// claim so a healthy node stops promptly. The obligation is already durable, so a failed
// send is left for the recovery drain — never surfaced as a caller error.
func DispatchDVRStops(claims []DVRStopClaim, logger logging.Logger) {
	for _, c := range claims {
		if c.StorageNodeID == "" {
			continue
		}
		if sendErr := SendDVRStop(c.StorageNodeID, &ipcpb.DVRStopRequest{DvrHash: c.DVRHash, RequestId: c.DVRHash, CommandGeneration: DVRStopCommandGeneration}); sendErr != nil {
			logger.WithError(sendErr).WithFields(logging.Fields{
				"dvr_hash": c.DVRHash,
				"node_id":  c.StorageNodeID,
			}).Info("Immediate DVR stop send failed; recovery drain will re-send (obligation is durable)")
		}
	}
}

// StopDVRForEndedSource claims the durable stop obligation on the active DVR whose
// recorded source node is endedSourceNodeID for internalName. DVR finalization is
// node-local (keyed on the source node), not stream-wide: a recording belongs to the
// node that produced its source, so it is stopped on that node's STREAM_END
// regardless of current stream ownership (the takeover case where this node is no
// longer the owner and the stream-wide stop would be suppressed).
//
// The claim is one guarded atomic UPDATE (state='stop_pending' + status='stopping',
// re-guarded on active status so a fast DVRStopped is not resurrected), scoped by
// tenant and artifact type; then a best-effort stop is sent and DVRStartingRecoveryJob
// re-sends on failure. A nil DB is an unconfigured/test no-op; missing scope or a
// query failure fails CLOSED (returns an error) so the durable STREAM_END gets a
// retryable negative ack rather than truncating the WAL.
//
// This is the node-keyed BACKSTOP; the precise primary finalizer is
// FinalizeIngestSessionClose on PUSH_INPUT_CLOSE (keyed on the ingest generation). A
// start that committed status='starting' but not yet sent DVRStart, then loses to a stop
// claimed here, no longer creates a live writer behind a terminal row: DVRStart carries a
// command generation below the stop's, so Helmsman rejects the superseded start, and the
// start-redelivery path (DVRStartingRecoveryJob) revalidates against the durable
// ingest_sessions.ended_at before re-dispatching. An early STREAM_END that beats the
// async DVR insert is handled by the close-before-start fence in startDVR
// (ingest_sessions.ended_at) and by DVRIntentRecovery replaying the durable intent.
func StopDVRForEndedSource(internalName, tenantID, endedSourceNodeID string, logger logging.Logger) error {
	if db == nil {
		// Not a runtime "database unavailable" (that path is the query failing below,
		// which fails closed): db is wired once at startup and is never nil at runtime.
		// A nil here means an unconfigured/test process with no DB, so there is no DVR
		// state to stop — a genuine no-op, not a swallowed failure.
		return nil
	}
	if internalName == "" || tenantID == "" || endedSourceNodeID == "" {
		return fmt.Errorf("DVR stop missing required scope: internal_name=%q tenant_id=%q source_node=%q", internalName, tenantID, endedSourceNodeID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Claim every ORPHANED DVR for this (stream, source node), not just the newest. Same-node
	// sessions can OVERLAP now (a reconnect mints a fresh generation while a prior session whose
	// PUSH_INPUT_CLOSE was lost still lingers 'recording'); a LIMIT 1 would leave that older
	// writer running forever. But the fence below is critical: a DVR bound to a STILL-ACTIVE
	// ingest session must NOT be stopped. STREAM_END/vanish for one connection can race a live
	// reconnect that already re-minted a generation and bound its own DVR — stopping ALL DVRs on
	// the node would kill that live recording. So claim only DVRs whose bound ingest_generation
	// is ENDED (its session's ended_at IS NOT NULL) or NULL (legacy/unbound); leave any bound to
	// an active session running.
	claims, err := ClaimDVRStops(ctx, db,
		`stream_internal_name = $1 AND dvr_start_dispatch->>'source_node_id' = $2 AND tenant_id::text = $3
		 AND (ingest_generation IS NULL OR NOT EXISTS (
		     SELECT 1 FROM foghorn.ingest_sessions s
		      WHERE s.id = foghorn.artifacts.ingest_generation AND s.ended_at IS NULL))`,
		internalName, endedSourceNodeID, tenantID)
	if err != nil {
		return fmt.Errorf("claim DVR stop obligation for ended source session: %w", err)
	}
	DispatchDVRStops(claims, logger)
	return nil
}

// OpenIngestSessionCluster returns the virtual media cluster an OPEN ingest
// session for this exact publisher connection was admitted into, or "" when the
// connection has no open session.
//
// PUSH_REWRITE consults it BEFORE claiming placement, so an idempotent re-fire
// claims the cluster its session is bound to rather than whatever the node is
// registered in now. Without that, a node reassigned mid-session has its retry
// either refused against the still-live claim in the old cluster, or claiming
// the new one and then finding the session permanently bound to the old — with
// renewal asserting one cluster while the record says another.
//
// Keyed on the mint's own identity — (tenant, node, trigger UUID) — so it reads
// only this tenant's rows. The caller therefore resolves the tenant first, from
// a non-claiming validation, and claims placement only after this returns.
func OpenIngestSessionCluster(ctx context.Context, tenantID, nodeID, triggerUUID string) (string, error) {
	if db == nil || tenantID == "" || nodeID == "" || triggerUUID == "" {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	clusterID, err := foghorndb.New(db).GetOpenIngestSessionCluster(ctx, foghorndb.GetOpenIngestSessionClusterParams{TenantID: tenantID, NodeID: nodeID, StartTriggerUuid: triggerUUID})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("look up open ingest session cluster: %w", err)
	}
	return clusterID, nil
}

// EndIngestSessionsForStreamEnd is the STREAM_END / vanish REAPER: it durably ends every ACTIVE
// ingest session for (tenant, node, stream) whose start is AT OR BEFORE the offline edge's event
// time, and claims each one's bound DVR stop, in ONE transaction. This is what makes STREAM_END a
// real backstop for a LOST PUSH_INPUT_CLOSE — without it a lost close leaves the session open
// forever, and (because admission now protects the incumbent) wedges every reconnect. The
// event-time fence (started_at_unix_millis <= eventMillis) preserves a reconnect that came up AFTER
// the stream ended (its session started later, so it is left active). eventMillis <= 0 (old Mist /
// missing X-Trigger-UnixMillis header) is a deliberate NO-OP: with no reliable event time we cannot
// fence a reconnect, so we end nothing and let the conservative offline fence handle it. Returns the
// number of sessions ended. Fails closed (error) on a query failure; nil DB is a no-op.
func EndIngestSessionsForStreamEnd(ctx context.Context, tenantID, nodeID, internalName string, eventMillis int64, logger logging.Logger) (int, error) {
	if db == nil || eventMillis <= 0 {
		return 0, nil
	}
	if tenantID == "" || nodeID == "" || internalName == "" {
		return 0, fmt.Errorf("stream-end reaper missing scope: tenant=%q node=%q stream=%q", tenantID, nodeID, internalName)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin stream-end reaper tx: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			logger.WithError(rbErr).Warn("Failed to roll back stream-end reaper tx")
		}
	}()
	// Scope the rows in a closure so defer rows.Close() runs BEFORE the ClaimDVRStops queries below
	// reuse the transaction (a tx cannot have open rows while issuing the next query).
	endedIDs, scanErr := foghorndb.New(tx).ReapStreamEndIngestSessions(ctx, foghorndb.ReapStreamEndIngestSessionsParams{
		TenantID: tenantID, NodeID: nodeID, StreamInternalName: internalName, EndedAtUnixMillis: sql.NullInt64{Int64: eventMillis, Valid: true},
	})
	if scanErr != nil {
		return 0, fmt.Errorf("reap ingest sessions on stream end: %w", scanErr)
	}
	// Claim each reaped session's bound DVR stop in the SAME transaction, so a lost close can never
	// leave a live writer behind a reaped session.
	var allClaims []DVRStopClaim
	for _, ended := range endedIDs {
		claims, claimErr := ClaimDVRStops(ctx, tx, `ingest_generation = $1::uuid AND tenant_id::text = $2`, ended.SessionID, tenantID)
		if claimErr != nil {
			return 0, fmt.Errorf("claim DVR stop for reaped session %s: %w", ended.SessionID, claimErr)
		}
		allClaims = append(allClaims, claims...)
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return 0, fmt.Errorf("commit stream-end reaper: %w", commitErr)
	}
	DispatchDVRStops(allClaims, logger)
	return len(endedIDs), nil
}

// EndExactMissingIngestSession retires only the admitted runtime identity that
// Helmsman's authoritative Mist inventory proved absent. The stream lock orders
// it against a same-name replacement; a replacement generation remains open and
// causes the subsequent offline fence to suppress every stream-wide effect.
func EndExactMissingIngestSession(ctx context.Context, tenantID, nodeID, internalName, generation string, connectorPID, eventMillis int64, logger logging.Logger) (bool, error) {
	if db == nil {
		return false, errors.New("runtime-absence reaper requires the durable session store")
	}
	if tenantID == "" || nodeID == "" || internalName == "" || generation == "" || connectorPID <= 0 || eventMillis <= 0 {
		return false, fmt.Errorf("runtime-absence reaper missing identity: tenant=%q node=%q stream=%q generation=%q pid=%d event_millis=%d", tenantID, nodeID, internalName, generation, connectorPID, eventMillis)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin runtime-absence reaper tx: %w", err)
	}
	defer rollbackQuiet(tx)
	q := foghorndb.New(tx)
	if lockErr := q.LockIngestStream(ctx, ingestStreamAdvisoryLockKey(tenantID, internalName)); lockErr != nil {
		return false, fmt.Errorf("lock runtime-absence reaper: %w", lockErr)
	}
	ended, err := q.ReapExactMissingIngestSession(ctx, foghorndb.ReapExactMissingIngestSessionParams{
		EndedAtUnixMillis: sql.NullInt64{Int64: eventMillis, Valid: true}, TenantID: tenantID,
		NodeID: nodeID, StreamInternalName: internalName, Generation: generation, ConnectorPid: connectorPID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, tx.Commit()
	}
	if err != nil {
		return false, fmt.Errorf("reap exact missing ingest session: %w", err)
	}
	claims, err := ClaimDVRStops(ctx, tx, `ingest_generation = $1::uuid AND tenant_id::text = $2`, ended.SessionID, tenantID)
	if err != nil {
		return false, fmt.Errorf("claim DVR stop for missing runtime %s: %w", ended.SessionID, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit runtime-absence reaper: %w", err)
	}
	DispatchDVRStops(claims, logger)
	return true, nil
}

// FenceOfflineBackstop serializes a session-agnostic offline edge with admission. Under the shared
// stream advisory lock it suppresses the edge when any ingest session remains open; otherwise it
// allocates an ordered source revision and persists the requested offline effects in the same
// transaction. The immediate inactive projection updates routing, while the durable worker retries
// all external effects under this lock. Errors fail closed.
func FenceOfflineBackstop(ctx context.Context, registry *StreamRegistry, tenantID, nodeID, internalName string, intent OfflineEffectIntent) (bool, int64, error) {
	if registry == nil {
		return false, 0, nil
	}
	// Retain the source generation on the inactive projection for identity and diagnostics.
	generation, active, ok := registry.SourceGenerationSnapshot(internalName, nodeID)

	if db == nil {
		return false, 0, fmt.Errorf("offline fence requires the durable session store")
	}
	_ = active
	_ = ok
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(internalName) == "" || strings.TrimSpace(nodeID) == "" {
		return false, 0, fmt.Errorf("offline fence missing scope: tenant=%q stream=%q", tenantID, internalName)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, 0, fmt.Errorf("begin offline-fence tx: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			logging.NewLogger().WithError(rbErr).Warn("Failed to roll back offline-fence tx")
		}
	}()
	q := foghorndb.New(tx)
	if lockErr := q.LockIngestStream(ctx, ingestStreamAdvisoryLockKey(tenantID, internalName)); lockErr != nil {
		return false, 0, fmt.Errorf("acquire offline-fence stream lock: %w", lockErr)
	}
	hasActive, probeErr := q.HasActiveStreamIngestSession(ctx, foghorndb.HasActiveStreamIngestSessionParams{TenantID: tenantID, StreamInternalName: internalName})
	if probeErr != nil {
		return false, 0, fmt.Errorf("offline-fence active probe: %w", probeErr)
	}
	if hasActive {
		return false, 0, tx.Commit()
	}
	rev, revErr := nextSourceRevision(ctx, tx, tenantID, internalName)
	if revErr != nil {
		return false, 0, revErr
	}
	if enqueueErr := enqueueOfflineEffectTx(ctx, tx, tenantID, internalName, nodeID, generation, rev, intent); enqueueErr != nil {
		return false, 0, enqueueErr
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return false, 0, fmt.Errorf("commit offline-fence: %w", commitErr)
	}
	// Publish the source-ownership transition immediately for routing. The durable effect row remains
	// the retry boundary: a Redis failure is returned to durable trigger callers, and the worker later
	// replays the same revision. A reconnect admitted after this transaction receives a higher revision,
	// so Redis rejects this inactive publish if it arrives late.
	applied, publishErr := registry.PublishSourceInactive(internalName, nodeID, generation, rev)
	if publishErr != nil {
		return false, rev, fmt.Errorf("publish offline source projection: %w", publishErr)
	}
	if !applied {
		return false, rev, nil
	}
	return true, rev, nil
}

// ProjectSourceIfCurrent allocates the DB winner's revision and publishes it only while generation
// remains the active session under the stream advisory lock. applied=false means the generation ended
// before projection; callers must deny it without admission side effects. The prior owner is returned
// only for an applied projection and is the node that must be drained. DB, lock, and Redis failures
// fail closed and durably retire a still-pending session.
func ProjectSourceIfCurrent(ctx context.Context, registry *StreamRegistry, tenantID, nodeID, internalName string, connectorPID int64, triggerUUID, generation string, intent AdmissionEffectIntent) (applied bool, resumed bool, err error) {
	if registry == nil {
		return false, false, nil
	}
	if db == nil {
		return false, false, fmt.Errorf("project-if-current requires the durable session store")
	}
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(internalName) == "" || strings.TrimSpace(generation) == "" {
		return false, false, fmt.Errorf("project-if-current missing scope: tenant=%q stream=%q generation=%q", tenantID, internalName, generation)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, false, fmt.Errorf("begin project-if-current tx: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			logging.NewLogger().WithError(rbErr).Warn("Failed to roll back project-if-current tx")
		}
	}()
	q := foghorndb.New(tx)
	if lockErr := q.LockIngestStream(ctx, ingestStreamAdvisoryLockKey(tenantID, internalName)); lockErr != nil {
		return false, false, fmt.Errorf("acquire project-if-current stream lock: %w", lockErr)
	}
	probe, probeErr := q.ProbeCurrentSourceProjection(ctx, foghorndb.ProbeCurrentSourceProjectionParams{TenantID: tenantID, StreamInternalName: internalName, Generation: generation})
	if probeErr != nil {
		return false, false, fmt.Errorf("project-if-current active-session probe: %w", probeErr)
	}
	isCurrent, revision, projectionState := probe.IsCurrent, probe.SourceRevision, probe.ProjectionState
	if !isCurrent {
		// This session ended (its own close won) or was superseded by a newer admission while this
		// projection was delayed — drop it, and signal the caller to DENY (no side effects).
		return false, false, tx.Commit()
	}
	if projectionState == "active" {
		// A RESUMED projection: this exact generation already crossed the shared CAS and was durably
		// confirmed — this call is a blocking-trigger retry whose first response was lost (or a
		// replica re-handling the trigger). The once-only admission effects are owed by the durable
		// obligation inserted with the confirmation (applied by the admission-effects worker), so
		// the caller has nothing to re-run or skip here. An active row must carry the revision the
		// CAS accepted; its absence is an invariant violation, not a resumable state — fail closed.
		if !revision.Valid || revision.Int64 <= 0 {
			return false, false, fmt.Errorf("resumed projection %s has no persisted source revision (invariant violation)", generation)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return false, false, fmt.Errorf("commit project-if-current resume: %w", commitErr)
		}
		// Re-assert the registry projection at the persisted revision so a cache-cold replica repairs
		// its local view (the equal-revision CAS is idempotent for the exact same identity). The
		// result is CHECKED, not assumed. A strictly newer shared watermark while this DB generation
		// remains current is cache divergence, so the repair path below advances durable authority and
		// republishes above that watermark. An error leaves shared state unknown and fails closed.
		_, projected, republishErr := registry.ProjectSource(internalName, nodeID, connectorPID, triggerUUID, generation, revision.Int64)
		if republishErr != nil {
			return false, false, fmt.Errorf("resumed projection re-publish: %w", republishErr)
		}
		if !projected {
			repaired, repairErr := repairResumedSourceProjection(ctx, registry, tenantID, nodeID, internalName, connectorPID, triggerUUID, generation)
			// The durable generation was already active before repair began. Surface
			// that fact even when shared projection repair fails, so the caller never
			// runs pending-session cleanup against established authority.
			return repaired, true, repairErr
		}
		return true, true, nil
	}
	// Draw the monotonic source revision UNDER the lock, so this projection orders strictly after any
	// prior transition for the stream and a stale replica's write cannot make old ownership look newer.
	rev := revision.Int64
	if !revision.Valid || rev <= 0 {
		var revErr error
		rev, revErr = nextSourceRevision(ctx, tx, tenantID, internalName)
		if revErr != nil {
			return false, false, revErr
		}
		if updateErr := q.PersistSourceProjectionRevision(ctx, foghorndb.PersistSourceProjectionRevisionParams{Generation: generation, SourceRevision: sql.NullInt64{Int64: rev, Valid: true}}); updateErr != nil {
			return false, false, fmt.Errorf("persist source projection revision: %w", updateErr)
		}
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return false, false, fmt.Errorf("commit project-if-current: %w", commitErr)
	}
	prior, priorGeneration, projected, projectErr := registry.projectSourceWithPriorGeneration(internalName, nodeID, connectorPID, triggerUUID, generation, rev)
	if projectErr != nil {
		cause := fmt.Errorf("publish source projection: %w", projectErr)
		return false, false, errors.Join(cause, abortPendingSourceProjection(ctx, tenantID, internalName, generation))
	}
	if !projected {
		cause := fmt.Errorf("source projection revision %d lost the shared CAS", rev)
		return false, false, errors.Join(cause, abortPendingSourceProjection(ctx, tenantID, internalName, generation))
	}
	// Confirm the projection AND persist the once-only admission-effect obligation ATOMICALLY: the
	// obligation (push-target activation, prior-owner drain from the CAS result, federation live
	// broadcast) exists if and only if this generation was confirmed, so a crash at ANY later point
	// cannot lose the effects (the admission-effects worker applies them under the stream lock) and
	// a trigger retry cannot duplicate them (one obligation per generation).
	confirmTx, confirmErr := db.BeginTx(ctx, nil)
	if confirmErr != nil {
		cause := fmt.Errorf("begin projection confirmation: %w", confirmErr)
		return false, false, errors.Join(cause, abortPendingSourceProjection(ctx, tenantID, internalName, generation))
	}
	defer rollbackQuiet(confirmTx)
	marked, markErr := foghorndb.New(confirmTx).ConfirmSourceProjection(ctx, foghorndb.ConfirmSourceProjectionParams{
		Generation: generation, TenantID: tenantID, StreamInternalName: internalName, SourceRevision: sql.NullInt64{Int64: rev, Valid: true},
	})
	if markErr != nil {
		cause := fmt.Errorf("confirm source projection: %w", markErr)
		return false, false, errors.Join(cause, abortPendingSourceProjection(ctx, tenantID, internalName, generation))
	}
	if marked != 1 {
		cause := fmt.Errorf("source session ended before projection confirmation")
		return false, false, errors.Join(cause, abortPendingSourceProjection(ctx, tenantID, internalName, generation))
	}
	if enqueueErr := enqueueAdmissionEffectTx(ctx, confirmTx, tenantID, internalName, nodeID, generation, rev, prior, priorGeneration, intent); enqueueErr != nil {
		cause := fmt.Errorf("enqueue admission effects: %w", enqueueErr)
		return false, false, errors.Join(cause, abortPendingSourceProjection(ctx, tenantID, internalName, generation))
	}
	if commitErr := confirmTx.Commit(); commitErr != nil {
		cause := fmt.Errorf("commit projection confirmation: %w", commitErr)
		return false, false, errors.Join(cause, abortPendingSourceProjection(ctx, tenantID, internalName, generation))
	}
	return true, false, nil
}

// repairResumedSourceProjection resolves the only valid source of truth after
// an active DB generation loses Redis's revision CAS: the stream-locked DB row.
// It reads the watermark under the stream lock, releases that transaction,
// allocates above the watermark, then reacquires and revalidates the stream
// before advancing the session and its once-only effect fence together. The
// allocator lock and Redis I/O therefore never overlap, and a genuine stream
// transition in the intentional unlock window is detected before mutation.
func repairResumedSourceProjection(ctx context.Context, registry *StreamRegistry, tenantID, nodeID, internalName string, connectorPID int64, triggerUUID, generation string) (bool, error) {
	for {
		if err := ctx.Err(); err != nil {
			return false, fmt.Errorf("resumed source repair deadline: %w", err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return false, fmt.Errorf("begin resumed source repair: %w", err)
		}
		q := foghorndb.New(tx)
		if err = q.LockIngestStream(ctx, ingestStreamAdvisoryLockKey(tenantID, internalName)); err != nil {
			rollbackQuiet(tx)
			return false, fmt.Errorf("lock resumed source repair: %w", err)
		}
		probe, probeErr := q.ProbeCurrentSourceProjection(ctx, foghorndb.ProbeCurrentSourceProjectionParams{
			TenantID: tenantID, StreamInternalName: internalName, Generation: generation,
		})
		if probeErr != nil {
			rollbackQuiet(tx)
			return false, fmt.Errorf("probe resumed source repair: %w", probeErr)
		}
		if !probe.IsCurrent || probe.ProjectionState != "active" || !probe.SourceRevision.Valid || probe.SourceRevision.Int64 <= 0 {
			rollbackQuiet(tx)
			return false, nil
		}
		expectedRevision := probe.SourceRevision.Int64
		// Release the stream lock before advancing the per-stream repair counter. A
		// concurrent transition may be waiting for this same stream lock; retaining
		// both would create a cross-connection lock inversion. The shared
		// watermark is also read after this rollback: Redis must not extend the
		// lifetime of the database transaction that serializes publisher state.
		rollbackQuiet(tx)
		sharedRevision, sharedErr := registry.sharedSourceRevision(ctx, internalName)
		if sharedErr != nil {
			return false, fmt.Errorf("read resumed source watermark: %w", sharedErr)
		}
		minimumRevision := max(sharedRevision, expectedRevision)
		newRevision, revisionErr := foghorndb.AllocateSourceProjectionRevisionAfter(ctx, db, tenantID, internalName, minimumRevision)
		if revisionErr != nil {
			return false, fmt.Errorf("allocate resumed source repair revision: %w", revisionErr)
		}
		tx, err = db.BeginTx(ctx, nil)
		if err != nil {
			return false, fmt.Errorf("begin resumed source repair apply: %w", err)
		}
		q = foghorndb.New(tx)
		if err = q.LockIngestStream(ctx, ingestStreamAdvisoryLockKey(tenantID, internalName)); err != nil {
			rollbackQuiet(tx)
			return false, fmt.Errorf("lock resumed source repair apply: %w", err)
		}
		probe, probeErr = q.ProbeCurrentSourceProjection(ctx, foghorndb.ProbeCurrentSourceProjectionParams{
			TenantID: tenantID, StreamInternalName: internalName, Generation: generation,
		})
		if probeErr != nil {
			rollbackQuiet(tx)
			return false, fmt.Errorf("revalidate resumed source repair: %w", probeErr)
		}
		if !probe.IsCurrent || probe.ProjectionState != "active" || !probe.SourceRevision.Valid || probe.SourceRevision.Int64 <= 0 {
			rollbackQuiet(tx)
			return false, nil
		}
		if probe.SourceRevision.Int64 != expectedRevision {
			rollbackQuiet(tx)
			continue
		}
		advanced, advanceErr := q.AdvanceActiveSourceProjectionRevision(ctx, foghorndb.AdvanceActiveSourceProjectionRevisionParams{
			NewRevision: sql.NullInt64{Int64: newRevision, Valid: true}, Generation: generation,
			TenantID: tenantID, StreamInternalName: internalName,
			PreviousRevision: probe.SourceRevision,
		})
		if advanceErr != nil {
			rollbackQuiet(tx)
			return false, fmt.Errorf("advance resumed source revision: %w", advanceErr)
		}
		if advanced != 1 {
			rollbackQuiet(tx)
			continue
		}
		effectAdvanced, effectErr := q.AdvanceAdmissionEffectSourceRevision(ctx, foghorndb.AdvanceAdmissionEffectSourceRevisionParams{
			NewRevision: newRevision, Generation: generation, TenantID: tenantID,
			StreamInternalName: internalName, PreviousRevision: probe.SourceRevision.Int64,
		})
		if effectErr != nil {
			rollbackQuiet(tx)
			return false, fmt.Errorf("advance resumed admission-effect revision: %w", effectErr)
		}
		if effectAdvanced != 1 {
			effectRevision, lookupErr := q.GetAdmissionEffectSourceRevision(ctx, foghorndb.GetAdmissionEffectSourceRevisionParams{
				Generation: generation, TenantID: tenantID, StreamInternalName: internalName,
			})
			switch {
			case errors.Is(lookupErr, sql.ErrNoRows):
				// Terminal effect rows are retention data, not active source authority. Once every
				// owed leg has settled they may be purged while the ingest session remains active;
				// repairing that session must not manufacture a new effect or reject its publisher.
			case lookupErr != nil:
				rollbackQuiet(tx)
				return false, fmt.Errorf("inspect resumed admission-effect revision: %w", lookupErr)
			default:
				rollbackQuiet(tx)
				return false, fmt.Errorf("advance resumed admission-effect revision: generation %s has revision %d, expected %d", generation, effectRevision, probe.SourceRevision.Int64)
			}
		}
		// Keep the per-stream advisory lock through the shared CAS. Concurrent
		// retries for this generation cannot overtake one another in the
		// commit-to-publish gap. A rejected write rolls back the stream state, but
		// never the already-issued cell-wide fencing token.
		_, projected, projectErr := registry.ProjectSource(internalName, nodeID, connectorPID, triggerUUID, generation, newRevision)
		if projectErr != nil {
			rollbackQuiet(tx)
			return false, fmt.Errorf("publish resumed source repair: %w", projectErr)
		}
		if !projected {
			rollbackQuiet(tx)
			continue
		}
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit resumed source repair: %w", err)
		}
		return true, nil
	}
}

// abortPendingSourceProjection releases stream authority after a projection that could not be
// confirmed. The session end and inactive projection intent commit together under the stream lock,
// so a denied PUSH_REWRITE cannot leave an open row blocking later publishers.
func abortPendingSourceProjection(ctx context.Context, tenantID, internalName, generation string) error {
	if db == nil {
		return errors.New("abort pending source projection: no database configured")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin abort pending source projection: %w", err)
	}
	defer rollbackQuiet(tx)
	q := foghorndb.New(tx)
	if lockErr := q.LockIngestStream(ctx, ingestStreamAdvisoryLockKey(tenantID, internalName)); lockErr != nil {
		return fmt.Errorf("lock abort pending source projection: %w", lockErr)
	}
	aborted, err := q.AbortPendingSourceProjection(ctx, foghorndb.AbortPendingSourceProjectionParams{Generation: generation, TenantID: tenantID, StreamInternalName: internalName})
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return fmt.Errorf("end pending source projection: %w", err)
	}
	revision, err := nextSourceRevision(ctx, tx, tenantID, internalName)
	if err != nil {
		return err
	}
	if err := enqueueOfflineEffectTx(ctx, tx, tenantID, internalName, aborted.NodeID, generation, revision, OfflineEffectIntent{}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit abort pending source projection: %w", err)
	}
	return nil
}

// AbortPendingIngestSession releases a durable admission that could not pass a post-mint gate.
// The database row is the single-publisher authority, so callers must end it before returning a
// denial or it would strand the stream behind a pending session.
func AbortPendingIngestSession(ctx context.Context, tenantID, internalName, generation string) error {
	return abortPendingSourceProjection(ctx, tenantID, internalName, generation)
}

// nextSourceRevision advances the counter at the same tenant/stream key where
// source authority is compared. The caller already holds that stream's lock.
func nextSourceRevision(ctx context.Context, tx *sql.Tx, tenantID, internalName string) (int64, error) {
	rev, err := foghorndb.New(tx).NextSourceProjectionRevision(ctx, foghorndb.NextSourceProjectionRevisionParams{
		TenantID: tenantID, StreamInternalName: internalName,
	})
	if err != nil {
		return 0, fmt.Errorf("draw source projection revision: %w", err)
	}
	return rev, nil
}

// CloseFinalization is the durable result of FinalizeIngestSessionClose.
type CloseFinalization struct {
	// EndedSessionID is the session this close ended, or "" when the close was fenced
	// (an older event than the active session's start under PID reuse) or the session
	// was already ended. "" means nothing was finalized.
	EndedSessionID string
	// ClaimToken / ClusterID are the placement claim the ended session held: the
	// publisher connection that owns it and the cluster it was admitted into.
	// The release matches on both, so a close cannot clear a claim that is not
	// this session's.
	ClaimToken string
	ClusterID  string
	// DVRHash / StorageNodeID identify the DVR whose stop obligation was claimed in the
	// same transaction, for a best-effort immediate send. Empty when no active DVR was
	// bound; the recovery drain re-sends regardless once stop_pending is durable.
	DVRHash       string
	StorageNodeID string
}

// FinalizeIngestSessionClose ends the exact ingest generation, claims its DVR stop, and queues its
// source-offline effects in one stream-locked transaction. The committed stop is drained by
// DVRStartingRecoveryJob when immediate dispatch is unavailable; an uncommitted close is retried
// from Helmsman's trigger WAL.
//
// The event-time fence (started_at_unix_millis <= closeMillis) leaves a newer same-node
// session intact when the OS reused the connector PID; a fenced or already-ended close
// ends nothing, claims nothing, and returns an empty result (idempotent).
func FinalizeIngestSessionClose(ctx context.Context, tenantID, nodeID string, connectorPID, closeMillis int64, internalName string, logger logging.Logger) (CloseFinalization, error) {
	var res CloseFinalization
	if db == nil {
		return res, errors.New("ingest session close cannot be finalized: no database configured")
	}
	if tenantID == "" || nodeID == "" || connectorPID <= 0 || closeMillis <= 0 || internalName == "" {
		// Fail closed on a malformed close. Mist supplies X-PID and X-Trigger-UnixMillis on
		// every trigger, so a missing PID or event time is a malformed/non-Mist close — and a
		// zero event time cannot be fenced against PID reuse. The stream name scopes the
		// session end (a PID maps to one stream). Reject rather than finalize unfenced or
		// cross-stream (the caller surfaces this as a retryable NACK).
		return res, fmt.Errorf("ingest session close missing required identity: tenant=%q node=%q pid=%d close_millis=%d stream=%q", tenantID, nodeID, connectorPID, closeMillis, internalName)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("begin ingest-close tx: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			logger.WithError(rbErr).Warn("Failed to roll back ingest-close tx")
		}
	}()

	// Serialize against CreateIngestSession on the SAME (tenant, stream) lock so a close-before-insert
	// is ordered: either this close's tombstone commits before the mint reads it (the mint then denies
	// the dead connector), or the mint commits first and the UPDATE below ends the row it created.
	q := foghorndb.New(tx)
	if lockErr := q.LockIngestStream(ctx, ingestStreamAdvisoryLockKey(tenantID, internalName)); lockErr != nil {
		return res, fmt.Errorf("acquire ingest-close stream lock: %w", lockErr)
	}

	ended, endErr := q.CloseIngestSession(ctx, foghorndb.CloseIngestSessionParams{
		TenantID: tenantID, NodeID: nodeID, ConnectorPid: connectorPID,
		CloseUnixMillis: sql.NullInt64{Int64: closeMillis, Valid: true}, StreamInternalName: internalName,
	})
	if errors.Is(endErr, sql.ErrNoRows) {
		// No active session to end — either a duplicate / already-ended / event-time-fenced close, OR
		// this close arrived BEFORE its own PUSH_REWRITE could mint the session (concurrent trigger
		// dispatch + WAL redelivery). Record a durable tombstone so CreateIngestSession denies the late
		// rewrite instead of resurrecting a dead publisher as an active session. Harmless when the close
		// was merely a duplicate: a genuine reconnect starts AFTER this close's event time and so is not
		// blocked, and the reaper sweeps the tombstone on a TTL.
		if insErr := q.InsertIngestCloseTombstone(ctx, foghorndb.InsertIngestCloseTombstoneParams{
			TenantID: tenantID, NodeID: nodeID, ConnectorPid: connectorPID, StreamInternalName: internalName, CloseUnixMillis: closeMillis,
		}); insErr != nil {
			return res, fmt.Errorf("record ingest close tombstone: %w", insErr)
		}
		return res, tx.Commit()
	}
	if endErr != nil {
		return res, fmt.Errorf("end ingest session on close: %w", endErr)
	}
	res.EndedSessionID = ended.ID
	res.ClaimToken = ended.StartTriggerUuid
	res.ClusterID = ended.ClusterID

	// Claim the stop obligation for the DVR bound to THIS generation, in the SAME tx as the
	// session end (atomic: either both commit and the durable stop_pending is drained by
	// recovery even if the send below fails, or neither commits and the close is retried).
	claims, claimErr := ClaimDVRStops(ctx, tx,
		`ingest_generation = $1::uuid AND tenant_id::text = $2`, ended.ID, tenantID)
	if claimErr != nil {
		return CloseFinalization{}, fmt.Errorf("claim DVR stop obligation for ingest generation: %w", claimErr)
	}
	revision, revisionErr := nextSourceRevision(ctx, tx, tenantID, internalName)
	if revisionErr != nil {
		return CloseFinalization{}, revisionErr
	}
	if enqueueErr := enqueueOfflineEffectTx(ctx, tx, tenantID, internalName, nodeID, ended.ID, revision, OfflineEffectIntent{
		SetNodeOffline: true, TeardownStream: true, BroadcastOffline: true,
	}); enqueueErr != nil {
		return CloseFinalization{}, enqueueErr
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return CloseFinalization{}, fmt.Errorf("commit ingest-close finalize: %w", commitErr)
	}

	// Durable now (survives the send failing). Dispatch best-effort AFTER commit.
	if len(claims) > 0 {
		res.DVRHash = claims[0].DVRHash
		res.StorageNodeID = claims[0].StorageNodeID
	}
	DispatchDVRStops(claims, logger)
	return res, nil
}

// UnstartedDVRIntent is an active ingest session whose durable DVR intent has not yet
// produced a bound recording — the crash-recovery seed for a record:true stream.
type UnstartedDVRIntent struct {
	SessionID    string
	TenantID     string
	InternalName string
	NodeID       string
	Intent       []byte
	Attempts     int // post-claim attempt count (informational; there is no cap — transient failures retry while active)
}

// DVRIntentLeaseDuration is how long a claimed intent is leased to one replica. It doubles
// as retry backoff: a transiently-failing intent is not re-claimed until the lease expires.
// There is NO attempt cap: a transient StartDVR failure retries for as long as the session is
// active (ClaimUnstartedDVRIntents selects ended_at IS NULL only), so a recoverable outage never
// permanently abandons a required recording. Only a structurally-invalid (undecodable) intent is
// moved to the terminal dvr_intent_error state.
const DVRIntentLeaseDuration = 5 * time.Minute

// ClaimUnstartedDVRIntents ATOMICALLY claims (leases + counts) active sessions carrying a DVR
// intent that have no bound DVR artifact, are not terminally errored, are older than the grace,
// and whose lease has expired. The lease makes it HA-safe (a
// concurrent Foghorn replica claiming the same row is excluded until the lease lapses) and is
// the retry backoff. Returns the claimed rows (with post-increment attempt count) for the
// caller to replay. DVRIntentRecovery replays StartDVR for each so a crash between the
// synchronous intent persist and the async StartDVR insert cannot drop a recording.
func ClaimUnstartedDVRIntents(ctx context.Context, olderThan time.Duration, limit int) ([]UnstartedDVRIntent, error) {
	if db == nil {
		return nil, nil
	}
	graceSeconds := int64(olderThan / time.Second)
	leaseSeconds := int64(DVRIntentLeaseDuration / time.Second)
	// Selection bounds the retry to LIVE work: only intents for a session that is still active
	// (ended_at IS NULL) with no terminal error and no bound DVR artifact yet, off-lease and past
	// the grace. There is no attempt-cap filter — transient StartDVR failures retry under the lease
	// for as long as the session is active (a recoverable outage must not permanently abandon a
	// recording); once the stream ends, ended_at drops the row from the scan. dvr_intent_attempts is
	// informational (it grows in a persistent-failure loop, bounded in RATE by the lease).
	rows, err := foghorndb.New(db).ClaimUnstartedDVRIntents(ctx, foghorndb.ClaimUnstartedDVRIntentsParams{
		GraceSeconds: graceSeconds, BatchLimit: int32(limit), LeaseSeconds: leaseSeconds,
	})
	if err != nil {
		return nil, fmt.Errorf("claim unstarted DVR intents: %w", err)
	}
	out := make([]UnstartedDVRIntent, len(rows))
	for i, row := range rows {
		out[i] = UnstartedDVRIntent{SessionID: row.ID, TenantID: row.TenantID, InternalName: row.StreamInternalName,
			NodeID: row.NodeID, Intent: []byte(row.DvrIntent), Attempts: int(row.DvrIntentAttempts)}
	}
	return out, nil
}

// FailDVRIntent records an EXPLICIT terminal error on a session's DVR intent so it is never
// re-claimed and the abandonment is operator-visible — used ONLY for a permanently-undecodable
// (structurally invalid) payload. Transient StartDVR failures are NOT terminalized here; they
// retry under the lease while the session stays active.
func FailDVRIntent(ctx context.Context, tenantID, sessionID, reason string) error {
	if db == nil {
		return nil
	}
	if err := foghorndb.New(db).FailDVRIntent(ctx, foghorndb.FailDVRIntentParams{
		SessionID: sessionID, TenantID: tenantID, DvrIntentError: sql.NullString{String: reason, Valid: true},
	}); err != nil {
		return fmt.Errorf("record DVR intent terminal error: %w", err)
	}
	return nil
}

// ServiceRegistrar is a function that registers additional gRPC services
type ServiceRegistrar func(srv *grpc.Server)

// GRPCServerConfig contains configuration for starting the Foghorn control gRPC
// listeners. The control plane is split into two listeners sharing one process:
//
//   - Internal: internal-CA leaf only, serves `foghorn.internal`. Audience is
//     mesh-only service traffic: Foghorn control APIs, federation, and HA relay.
//     Wire those registrars via InternalRegistrars.
//
//   - External: Navigator-backed ACME wildcards only. Audience is public edge
//     traffic: Helmsman control and edge bootstrap. Do not register service
//     control APIs here.
type GRPCServerConfig struct {
	InternalBindAddr   string
	ExternalBindAddr   string
	Logger             logging.Logger
	ServiceToken       string
	JWTSecret          string
	InternalRegistrars []ServiceRegistrar
}

// GRPCServers is the pair of gRPC servers returned by StartGRPCServers.
type GRPCServers struct {
	Internal *grpc.Server
	External *grpc.Server
}

// StartGRPCServers starts the Foghorn internal and external control gRPC
// listeners. The two listeners differ in cert source, audience, and registered
// services; see GRPCServerConfig.
func StartGRPCServers(ctx context.Context, cfg GRPCServerConfig) (*GRPCServers, error) {
	if strings.TrimSpace(cfg.InternalBindAddr) == "" {
		return nil, fmt.Errorf("InternalBindAddr is required")
	}
	if strings.TrimSpace(cfg.ExternalBindAddr) == "" {
		return nil, fmt.Errorf("ExternalBindAddr is required")
	}

	internal, err := startInternalGRPCListener(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("start internal gRPC listener: %w", err)
	}

	external, err := startExternalGRPCListener(ctx, cfg)
	if err != nil {
		internal.GracefulStop()
		return nil, fmt.Errorf("start external gRPC listener: %w", err)
	}

	return &GRPCServers{Internal: internal, External: external}, nil
}

// startInternalGRPCListener listens on the internal-CA bind addr. Serves
// `foghorn.internal`. Registers mesh-only control services via
// InternalRegistrars plus health + reflection. No HelmsmanControl and no
// EdgeProvisioning; those are public edge APIs.
func startInternalGRPCListener(ctx context.Context, cfg GRPCServerConfig) (*grpc.Server, error) {
	lc := net.ListenConfig{}
	lis, err := lc.Listen(ctx, "tcp", cfg.InternalBindAddr)
	if err != nil {
		return nil, err
	}

	certFile := os.Getenv("GRPC_TLS_CERT_PATH")
	keyFile := os.Getenv("GRPC_TLS_KEY_PATH")

	var opts []grpc.ServerOption
	if certFile != "" || keyFile != "" {
		tlsCfg := grpcutil.ServerTLSConfig{CertFile: certFile, KeyFile: keyFile}
		waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := grpcutil.WaitForServerTLSFiles(waitCtx, tlsCfg, cfg.Logger); err != nil {
			_ = lis.Close()
			return nil, fmt.Errorf("timed out waiting for file-based gRPC TLS: %w", err)
		}
		serverOpt, err := grpcutil.ServerTLS(tlsCfg, cfg.Logger)
		if err != nil {
			_ = lis.Close()
			return nil, fmt.Errorf("configure internal listener TLS: %w", err)
		}
		opts = append(opts, serverOpt)
		cfg.Logger.WithFields(logging.Fields{
			"bind_addr": cfg.InternalBindAddr,
			"cert_file": certFile,
			"key_file":  keyFile,
		}).Info("Foghorn internal gRPC listener TLS: file-based internal-CA leaf")
	} else if !allowInsecureControlGRPC() {
		_ = lis.Close()
		return nil, fmt.Errorf("internal gRPC listener requires GRPC_TLS_CERT_PATH/GRPC_TLS_KEY_PATH or GRPC_ALLOW_INSECURE=true")
	} else {
		cfg.Logger.WithField("bind_addr", cfg.InternalBindAddr).Info("Foghorn internal gRPC listener running without TLS")
	}

	opts = appendCommonInterceptors(opts, cfg)

	srv := grpc.NewServer(opts...)
	registerHealthAndReflection(srv)
	for _, reg := range cfg.InternalRegistrars {
		reg(srv)
	}

	go func() {
		if err := srv.Serve(lis); err != nil {
			cfg.Logger.WithError(err).Error("Foghorn internal gRPC listener exited")
		}
	}()
	return srv, nil
}

// startExternalGRPCListener listens on the external bind addr. Serves cluster
// FQDNs via Navigator-backed ACME wildcards. Registers only HelmsmanControl,
// EdgeProvisioning, health, and reflection.
func startExternalGRPCListener(ctx context.Context, cfg GRPCServerConfig) (*grpc.Server, error) {
	lc := net.ListenConfig{}
	lis, err := lc.Listen(ctx, "tcp", cfg.ExternalBindAddr)
	if err != nil {
		return nil, err
	}

	rootDomain := platformRootDomain()
	tlsBundles := []*ipcpb.TLSCertBundle{}

	if navigatorClient != nil {
		waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		bundles, certErr := waitForServedClusterTLSBundles(waitCtx, rootDomain)
		if certErr == nil && len(bundles) > 0 {
			tlsBundles = append(tlsBundles, bundles...)
			bundleIDs := make([]string, 0, len(bundles))
			domains := make([]string, 0, len(bundles))
			for _, bundle := range bundles {
				bundleIDs = append(bundleIDs, bundle.GetBundleId())
				domains = append(domains, bundle.GetDomain())
			}
			cfg.Logger.WithFields(logging.Fields{
				"bind_addr":  cfg.ExternalBindAddr,
				"bundle_ids": bundleIDs,
				"domains":    domains,
			}).Info("Foghorn external gRPC listener TLS: Navigator ACME cluster wildcards")
		} else {
			_ = lis.Close()
			if certErr == nil {
				certErr = fmt.Errorf("no served cluster TLS bundles found")
			}
			return nil, fmt.Errorf("navigator certificate unavailable for served Foghorn clusters: %w", certErr)
		}
	}

	var opts []grpc.ServerOption
	if len(tlsBundles) > 0 {
		if err := serverCert.StoreBundles(tlsBundles); err != nil {
			_ = lis.Close()
			return nil, fmt.Errorf("parse external listener TLS certificates: %w", err)
		}
		creds := credentials.NewTLS(&tls.Config{
			GetCertificate: serverCert.GetCertificate,
			MinVersion:     tls.VersionTLS12,
		})
		opts = append(opts, grpc.Creds(creds))
	} else if !allowInsecureControlGRPC() {
		_ = lis.Close()
		return nil, fmt.Errorf("external gRPC listener requires Navigator bundles or GRPC_ALLOW_INSECURE=true")
	} else {
		cfg.Logger.WithField("bind_addr", cfg.ExternalBindAddr).Info("Foghorn external gRPC listener running without TLS")
	}

	opts = appendCommonInterceptors(opts, cfg)

	srv := grpc.NewServer(opts...)
	ipcpb.RegisterHelmsmanControlServer(srv, &Server{})
	RegisterEdgeProvisioningService(srv)
	registerHealthAndReflection(srv, ipcpb.HelmsmanControl_ServiceDesc.ServiceName, "foghorn.EdgeProvisioningService")

	go func() {
		if err := srv.Serve(lis); err != nil {
			cfg.Logger.WithError(err).Error("Foghorn external gRPC listener exited")
		}
	}()
	return srv, nil
}

func registerHealthAndReflection(srv *grpc.Server, serviceNames ...string) {
	hs := health.NewServer()
	hs.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	for _, serviceName := range serviceNames {
		hs.SetServingStatus(serviceName, grpc_health_v1.HealthCheckResponse_SERVING)
	}
	grpc_health_v1.RegisterHealthServer(srv, hs)
	reflection.Register(srv)
}

func appendCommonInterceptors(opts []grpc.ServerOption, cfg GRPCServerConfig) []grpc.ServerOption {
	unaryInterceptors := []grpc.UnaryServerInterceptor{
		grpcutil.SanitizeUnaryServerInterceptor(),
	}

	nodeControlMethods := []string{
		foghornpb.NodeControlService_SetNodeOperationalMode_FullMethodName,
		foghornpb.NodeControlService_GetNodeHealth_FullMethodName,
	}

	if cfg.ServiceToken != "" {
		skipMethods := []string{
			"/grpc.health.v1.Health/Check",
			"/grpc.health.v1.Health/Watch",
			ipcpb.HelmsmanControl_Connect_FullMethodName,
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

	if cfg.ServiceToken != "" {
		streamAuth := middleware.GRPCStreamAuthInterceptor(middleware.GRPCAuthConfig{
			ServiceToken: cfg.ServiceToken,
			Logger:       cfg.Logger,
			SkipMethods: []string{
				"/grpc.health.v1.Health/Watch",
				ipcpb.HelmsmanControl_Connect_FullMethodName,
			},
		})
		opts = append(opts, grpc.ChainStreamInterceptor(streamAuth))
	}
	return opts
}

func nodeControlAuthInterceptor(serviceToken, jwtSecret string, logger logging.Logger) grpc.UnaryServerInterceptor {
	protected := map[string]bool{
		foghornpb.NodeControlService_SetNodeOperationalMode_FullMethodName: true,
		foghornpb.NodeControlService_GetNodeHealth_FullMethodName:          true,
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

// ErrConnSuperseded is returned when a dispatch targets a control connection that a reconnect retired. It is
// distinct from ErrNotConnected and explicitly NOT relayable (shouldRelay returns false for it): the node is
// present via a NEWER session on this instance, so re-forwarding would deliver the command to the new owner
// after the caller already treated the local attempt as failed. The caller reverts/retries instead.
var ErrConnSuperseded = status.Error(codes.Unavailable, "control connection superseded by a reconnect")

// ErrFreezeProtocolUnsupported is returned by the final owning send when the target sidecar's declared
// control-protocol version is below FreezeStagedProtocolMin. It is distinct from ErrNotConnected so the HA
// relay path does NOT re-forward it (the node IS connected here — it is simply too old): the freeze fails
// closed and the artifact stays local/retryable until the sidecar is upgraded.
var ErrFreezeProtocolUnsupported = status.Error(codes.FailedPrecondition, "sidecar control-protocol version does not support staged freeze")

// FreezeStagedProtocolMin is the minimum control-protocol version a sidecar must declare (in Register) to be
// handed a staged, server-minted freeze. A sidecar below it neither uploads to the attempt-scoped staging
// key nor echoes the server-minted attempt id, so it can complete neither the staging HEAD verification nor
// the attempt-scoped completion CAS. Freeze admission to such a node FAILS CLOSED. Version 0 is a
// pre-staged-freeze sidecar (Register.control_protocol_version absent).
const FreezeStagedProtocolMin int32 = 1

// ThumbnailStagedProtocolMin is the minimum control-protocol version a sidecar must declare to be handed a
// staged, server-minted thumbnail publication. Below it, the sidecar neither uploads to the per-attempt staging
// keys nor echoes the attempt id, so it can neither be verified nor bound to the completion CAS — the mint
// FAILS CLOSED (no legacy fixed-key overwrite path). It is above FreezeStagedProtocolMin because thumbnail staging
// is a distinct capability gated separately from freeze. Registration enforces MinControlProtocolVersion (>= this),
// so every connected sidecar satisfies the gate.
const ThumbnailStagedProtocolMin int32 = 2

// AuthoritativeInventoryProtocolMin is the minimum control-protocol version at which a sidecar emits a VERSIONED
// whole-node artifact inventory (a monotonic report_seq plus the incomplete-scan flag). A report's own report_seq is
// the per-report proof that a specific inventory is authoritative (fence-tied acceptance). Because registration
// rejects any sidecar below MinControlProtocolVersion (>= this), EVERY connected session is inventory-authoritative —
// there is no legacy inventory path.
const AuthoritativeInventoryProtocolMin int32 = ThumbnailStagedProtocolMin

// IngestGenerationFencingProtocolMin is the first control protocol that records the accepted
// PUSH_REWRITE generation and rejects stale drain/activation/deactivation commands at Helmsman.
// The durable outboxes rely on this receiver fence because a timed-out bidi Send is not transport
// cancellation and may surface later on the old connection.
const IngestGenerationFencingProtocolMin int32 = 3

// NodeIdentityProofProtocolMin is the first protocol that signs each
// registration with a persisted node identity and a one-time nonce.
const NodeIdentityProofProtocolMin int32 = 4

// MinControlProtocolVersion is the HARD minimum a sidecar must declare in Register to connect at all. A registration
// below this is REJECTED (FailedPrecondition), not admitted under a compatibility path. This is what makes inventory
// authority session-owned rather than payload-selected: a sub-min sidecar cannot connect, so every report the
// processor sees is from an authoritative session.
const MinControlProtocolVersion int32 = NodeIdentityProofProtocolMin

// ControlFeatures is the SINGLE place a sidecar session's protocol-gated capabilities are decided. It is derived
// once from the negotiated control-protocol version (Register.control_protocol_version) and consumed by placement
// and handlers, so protocol behavior lives in one contract instead of scattered `if version < X` checks. Registration
// enforces MinControlProtocolVersion, so for a connected session every capability below is currently always true.
//
// A false capability never means "drop the node": a dispatched operation the session cannot do FAILS CLOSED and the
// work stays local/retryable until the sidecar is upgraded.
type ControlFeatures struct {
	StagedFreeze           bool // server-minted staged freeze (>= FreezeStagedProtocolMin)
	StagedThumbnail        bool // server-minted staged thumbnail publication (>= ThumbnailStagedProtocolMin)
	AuthoritativeInventory bool // versioned whole-node artifact inventory (>= AuthoritativeInventoryProtocolMin)
}

// ControlFeaturesForProtocol derives the capability set a declared control-protocol version supports.
func ControlFeaturesForProtocol(v int32) ControlFeatures {
	return ControlFeatures{
		StagedFreeze:           v >= FreezeStagedProtocolMin,
		StagedThumbnail:        v >= ThumbnailStagedProtocolMin,
		AuthoritativeInventory: v >= AuthoritativeInventoryProtocolMin,
	}
}

// features returns the control capabilities this connection's declared protocol version supports.
func (c *conn) features() ControlFeatures { return ControlFeaturesForProtocol(c.protocolVersion) }

// connIsCurrentOwner reports whether the node's currently-registered control connection is the SAME one that
// carried a request (matched by ownership fence). A goroutine dispatched from a since-superseded connection
// gets false, so it can decline to claim an attempt for a connection that no longer owns the node. A missing
// registry entry (never registered / cleaned up) also returns false — fail closed.
func connIsCurrentOwner(nodeID string, fence int64) bool {
	if registry == nil {
		return false
	}
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	return c != nil && c.fence == fence
}

// NodeFreezeProtocolOK reports whether the node's LOCAL control connection declares a protocol version that
// supports staged, server-minted freeze. known=false means this instance holds no local connection for the
// node (owned by a peer, or not connected) — a caller must NOT treat that as "incapable"; the owning
// instance's session-bound interactive admission is the authority. Used as a proactive-dispatch pre-check so
// the reconciler does not repeatedly claim+revert against a known-old locally-connected sidecar.
func NodeFreezeProtocolOK(nodeID string) (ok bool, known bool) {
	if registry == nil {
		return false, false
	}
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return false, false
	}
	return c.features().StagedFreeze, true
}

// shouldRelay reports whether a local send error warrants a relay attempt.
// Beyond ErrNotConnected (node absent from registry), it also triggers relay
// when stream.Send failed and the node was concurrently removed — covering
// the race between a stream dying and handleHelmsmanStream cleaning up.
func shouldRelay(nodeID string, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrConnSuperseded) {
		return false // a newer LOCAL session owns the node; never re-forward to the new owner
	}
	if errors.Is(err, ErrNotConnected) {
		return true
	}
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	return c == nil
}

// processDVRProgress handles DVR progress updates from storage Helmsman
func processDVRProgress(progress *ipcpb.DVRProgress, session NodeSession, logger logging.Logger) {
	// The DVR-owner authorization below binds the report to the dispatched recording node; that owner is
	// stored under the canonical id, so authorize/attribute against the authenticated session, never the raw key.
	storageNodeID := session.NodeID()
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

	// Authorize the reporting node against the dispatched recording owner (strictly, and scoped to the
	// owning tenant) BEFORE any sink. A mismatched node — even against a terminal row — is rejected with
	// zero mutation. Fail closed when the tenant cannot be resolved or the lookup errors.
	if dvrHash != "" && storageNodeID != "" {
		tenantID, found, ownErr := dvrOwnerTenant(streamCtx(), dvrHash)
		if ownErr != nil || !found {
			logger.WithError(ownErr).WithFields(logging.Fields{
				"dvr_hash":       dvrHash,
				"reporting_node": storageNodeID,
			}).Warn("Ignoring DVR progress: no tenant-owned DVR row for hash (or lookup failed)")
			return
		}
		if authorized, chkErr := dvrReportNodeAuthorized(streamCtx(), dvrHash, tenantID, storageNodeID, dvrAuthStrict); chkErr != nil || !authorized {
			logger.WithError(chkErr).WithFields(logging.Fields{
				"dvr_hash":       dvrHash,
				"reporting_node": storageNodeID,
			}).Warn("Ignoring DVR progress: reporting node is not the dispatched recording owner (or lookup failed)")
			return
		}
	}

	// The durable progress write classifies the report: applied=true only for an accepted active
	// transition. A terminal/finalizing no-op does not mirror into stream-instance state.
	applied, perr := state.DefaultManager().ApplyDVRProgress(streamCtx(), dvrHash, status, uint64(sizeBytes), uint32(segmentCount), storageNodeID)
	if perr != nil {
		logger.WithError(perr).WithField("dvr_hash", dvrHash).Debug("ApplyDVRProgress failed")
	}

	// Refresh artifact_nodes / emit node-copy GAINED ONLY for an accepted active transition. A terminal
	// no-op must not revive node presence or re-emit GAINED for a settled recording.
	if applied && db != nil && dvrHash != "" && storageNodeID != "" {
		if err := foghorndb.New(db).RefreshDVRArtifactNodeProgress(streamCtx(), foghorndb.RefreshDVRArtifactNodeProgressParams{
			ArtifactHash: dvrHash, NodeID: storageNodeID,
			SegmentCount: sql.NullInt32{Int32: int32(segmentCount), Valid: true}, SizeBytes: sql.NullInt64{Int64: int64(sizeBytes), Valid: true},
		}); err != nil {
			logger.WithError(err).WithFields(logging.Fields{
				"dvr_hash": dvrHash,
				"node_id":  storageNodeID,
			}).Warn("Failed to refresh active DVR artifact node from progress update")
		} else if rerr := RefreshNodeCopy(streamCtx(), dvrHash, storageNodeID); rerr != nil {
			logger.WithError(rerr).WithField("dvr_hash", dvrHash).Warn("Failed to emit node-copy GAINED after DVR progress refresh")
		}
	}
}

// processDVRStopped handles DVR completion from storage Helmsman
func processDVRStopped(stopped *ipcpb.DVRStopped, session NodeSession, logger logging.Logger) {
	// FinalizeDVR binds the completion to the dispatched recording node (ReportingNodeID); that owner is the
	// canonical id, so authorize/attribute against the authenticated session, never the raw registry key.
	storageNodeID := session.NodeID()
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
		// Bind the deleted report to the dispatched recording node (the finalize branch below is bound
		// inside FinalizeDVR via ReportingNodeID). A report from any other node for an existing recording
		// is rejected without mutating; a duplicate delete against a genuinely absent row is a safe no-op
		// (idempotent-stop mode). The owner check is scoped to the tenant that owns the hash. Fail closed
		// on a tenant-lookup query error: abort with no state mutation.
		tenantID, found, ownErr := dvrOwnerTenant(streamCtx(), dvrHash)
		if ownErr != nil {
			logger.WithError(ownErr).WithFields(logging.Fields{"dvr_hash": dvrHash, "reporting_node": storageNodeID}).
				Warn("Ignoring DVRStopped(deleted): owner-tenant lookup failed; failing closed")
			return
		}
		if found {
			if ok, chkErr := dvrReportNodeAuthorized(streamCtx(), dvrHash, tenantID, storageNodeID, dvrAuthIdempotentStop); chkErr != nil || !ok {
				logger.WithError(chkErr).WithFields(logging.Fields{"dvr_hash": dvrHash, "reporting_node": storageNodeID}).
					Warn("Ignoring DVRStopped(deleted): reporting node is not the dispatched recording owner (or lookup failed)")
				return
			}
		}
		if applyErr := state.DefaultManager().ApplyDVRStopped(streamCtx(), dvrHash, "deleted", int64(durationSeconds), uint64(sizeBytes), manifestPath, errorMsg, storageNodeID); applyErr != nil {
			logger.WithError(applyErr).WithField("dvr_hash", dvrHash).Warn("ApplyDVRStopped(deleted) failed")
		}
		// The authoritative DELETED lifecycle event is emitted in-transaction by SoftDeleteDVRAndChapters
		// (the deletion path), so a sidecar 'deleted' report does NOT emit a second, duplicate event.
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
			ReportingNodeID: storageNodeID,
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
		// The terminal DVR STOPPED lifecycle event is enqueued INSIDE FinalizeDVR's transaction (durable,
		// atomic with the terminal state) — no separate crash-lossy callback here.
	}()
}

// ResolveClipHash implements the ResolveClipHash RPC method
func (s *Server) ResolveClipHash(ctx context.Context, req *ipcpb.ClipHashRequest) (*ipcpb.ClipHashResponse, error) {
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

	return &ipcpb.ClipHashResponse{
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

	hostname := ExtractPublicHostFromOutputs(nodeState.Outputs)
	if hostname == "" {
		hostname = hostOnlyForMistTemplate(nodeState.BaseURL)
	} else {
		hostname = hostOnlyForMistTemplate(hostname)
	}
	if hostname == "" {
		logger.WithField("node_id", nodeID).Info("Unable to determine DTSC host")
		return ""
	}

	// Replace HOST placeholder with actual hostname
	dtscURI := strings.ReplaceAll(dtscTemplate, "HOST", hostname)

	// Use the template's static prefix when checking DVR readiness.
	baseDTSCURI := strings.ReplaceAll(dtscURI, "$", "")

	// Remove trailing slash if present
	baseDTSCURI = strings.TrimSuffix(baseDTSCURI, "/")
	baseDTSCURI = normalizeDTSCBaseURI(baseDTSCURI)

	logger.WithFields(logging.Fields{
		"node_id":       nodeID,
		"hostname":      hostname,
		"dtsc_template": dtscTemplate,
		"dtsc_uri":      baseDTSCURI,
	}).Info("Constructed DTSC base URI")

	return baseDTSCURI
}

func normalizeDTSCBaseURI(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "dtsc" || u.Host == "" {
		return raw
	}
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), "4200")
	}
	return strings.TrimSuffix(u.String(), "/")
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
	if db != nil && nodeID != "" {
		row, err := foghorndb.New(db).GetPersistedNodeOutputs(context.Background(), nodeID)
		if err == nil {
			baseURL, outputsRaw := row.BaseUrl, row.OutputsJson
			var outputs map[string]any
			if outputsRaw != "" {
				if err := json.Unmarshal([]byte(outputsRaw), &outputs); err != nil {
					return nil, false
				}
			}
			if len(outputs) > 0 {
				return &NodeOutputs{
					NodeID:      nodeID,
					BaseURL:     baseURL,
					OutputsJSON: outputsRaw,
					Outputs:     outputs,
				}, true
			}
		}
	}
	return nil, false
}

// Global handlers set by handlers package for trigger processing
var mistTriggerProcessor MistTriggerProcessor

// MistTriggerProcessor interface for handling MistServer triggers
type MistTriggerProcessor interface {
	ProcessTrigger(triggerType string, rawPayload []byte, nodeID string) (string, bool, error)
	ProcessTypedTrigger(trigger *ipcpb.MistTrigger) (string, bool, error)
}

// classifyTriggerError maps a processor error to the ack error_code and
// retryable flag Helmsman uses to decide between backoff-and-resend vs
// dead-letter.
func classifyTriggerError(err error) (ipcpb.TriggerAckErrorCode, bool) {
	if err == nil {
		return ipcpb.TriggerAckErrorCode_TRIGGER_ACK_ERROR_NONE, false
	}
	if ingestErr, ok := errors.AsType[*ingesterrors.IngestError](err); ok {
		if retryable, explicit := ingestErr.RetryableOverride(); explicit {
			// Disposition and diagnostic class are independent. A deterministic
			// internal lifecycle outcome is terminal without pretending it was a
			// schema violation.
			return ipcpb.TriggerAckErrorCode_TRIGGER_ACK_ERROR_INTERNAL, retryable
		}
		switch ingestErr.Code {
		case ipcpb.IngestErrorCode_INGEST_ERROR_INVALID_STREAM_KEY,
			ipcpb.IngestErrorCode_INGEST_ERROR_ACCOUNT_SUSPENDED,
			ipcpb.IngestErrorCode_INGEST_ERROR_PAYMENT_REQUIRED,
			ipcpb.IngestErrorCode_INGEST_ERROR_DUPLICATE_INGEST,
			ipcpb.IngestErrorCode_INGEST_ERROR_FREE_TIER_EXHAUSTED,
			ipcpb.IngestErrorCode_INGEST_ERROR_TENANT_STREAM_CAP:
			return ipcpb.TriggerAckErrorCode_TRIGGER_ACK_ERROR_SCHEMA, false
		case ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT:
			return ipcpb.TriggerAckErrorCode_TRIGGER_ACK_ERROR_DOWNSTREAM_UNAVAILABLE, true
		default:
			return ipcpb.TriggerAckErrorCode_TRIGGER_ACK_ERROR_INTERNAL, true
		}
	}
	// Decklog client / Kafka publish errors and everything else are
	// assumed transient. Helmsman will retry with the same source_event_id;
	// downstream dedupe (raw_mist_triggers + canonical ledger argMax)
	// collapses duplicates.
	return ipcpb.TriggerAckErrorCode_TRIGGER_ACK_ERROR_KAFKA_PUBLISH, true
}

// sendMistTriggerAck delivers the durable ack back to Helmsman on the
// same control stream. Caller invokes for any mist.IsDurableTriggerType
// trigger regardless of blocking flag.
func sendMistTriggerAck(stream ipcpb.HelmsmanControl_ConnectServer, requestID string, err error, logger logging.Logger) {
	if stream == nil {
		return
	}
	code, retryable := classifyTriggerError(err)
	ack := &ipcpb.MistTriggerAck{
		RequestId: requestID,
		Success:   err == nil,
		Retryable: retryable,
		ErrorCode: code,
	}
	if err != nil {
		ack.ErrorMessage = err.Error()
	}
	msg := &ipcpb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &ipcpb.ControlMessage_MistTriggerAck{MistTriggerAck: ack},
	}
	if sendErr := stream.Send(msg); sendErr != nil {
		logger.WithFields(logging.Fields{
			"request_id": requestID,
			"error":      sendErr,
		}).Warn("Failed to send MistTriggerAck; Helmsman will retry from WAL")
	}
}

const blockingTriggerReplayTTL = 10 * time.Minute

type blockingTriggerReplayEntry struct {
	key       string
	ready     chan struct{}
	response  *ipcpb.MistTriggerResponse
	expiresAt time.Time
}

var blockingTriggerReplay = struct {
	sync.Mutex
	entries   map[string]*blockingTriggerReplayEntry
	lastSweep time.Time
}{entries: map[string]*blockingTriggerReplayEntry{}}

func acquireBlockingTriggerReplay(key string) (*blockingTriggerReplayEntry, bool) {
	now := time.Now()
	blockingTriggerReplay.Lock()
	defer blockingTriggerReplay.Unlock()
	if now.Sub(blockingTriggerReplay.lastSweep) >= time.Minute {
		for replayKey, entry := range blockingTriggerReplay.entries {
			if !now.Before(entry.expiresAt) {
				delete(blockingTriggerReplay.entries, replayKey)
			}
		}
		blockingTriggerReplay.lastSweep = now
	}
	if entry := blockingTriggerReplay.entries[key]; entry != nil && now.Before(entry.expiresAt) {
		return entry, false
	}
	entry := &blockingTriggerReplayEntry{key: key, ready: make(chan struct{}), expiresAt: now.Add(blockingTriggerReplayTTL)}
	blockingTriggerReplay.entries[key] = entry
	return entry, true
}

func completeBlockingTriggerReplay(entry *blockingTriggerReplayEntry, response *ipcpb.MistTriggerResponse, retain bool) {
	blockingTriggerReplay.Lock()
	entry.response = cloneMistTriggerResponse(response)
	if !retain && blockingTriggerReplay.entries[entry.key] == entry {
		delete(blockingTriggerReplay.entries, entry.key)
	}
	close(entry.ready)
	blockingTriggerReplay.Unlock()
}

func cloneMistTriggerResponse(response *ipcpb.MistTriggerResponse) *ipcpb.MistTriggerResponse {
	if response == nil {
		return nil
	}
	return &ipcpb.MistTriggerResponse{
		RequestId:          response.RequestId,
		Response:           response.Response,
		Abort:              response.Abort,
		ErrorCode:          response.ErrorCode,
		IngestGeneration:   response.IngestGeneration,
		IngestConnectorPid: response.IngestConnectorPid,
		Action:             response.Action,
		Reason:             response.Reason,
	}
}

// processMistTrigger processes typed MistServer triggers forwarded from Helmsman
func processMistTrigger(trigger *ipcpb.MistTrigger, session NodeSession, stream ipcpb.HelmsmanControl_ConnectServer, logger logging.Logger) {
	nodeID := session.NodeID()
	if trigger != nil {
		// Bind the trigger to THIS connection's IMMUTABLE authenticated session (captured at Register and passed
		// by value), never the payload and never a registry re-fetch: overwrite the self-asserted node_id with
		// the canonical authenticated id, and the cluster_id with the session's server-resolved cluster. A node
		// therefore cannot attribute routing/storage/lifecycle/accounting effects to another node/cluster by
		// naming them in the payload, and a reconnect can't substitute a newer session for this connection's
		// work. There is NO local-cluster fallback: an authenticated connection carries its resolved cluster.
		trigger.NodeId = nodeID
		cid := strings.TrimSpace(session.ClusterID)
		trigger.ClusterId = stringPtrIfNotEmpty(cid)
		controlCellID := strings.TrimSpace(localControlCellID)
		trigger.ControlCellId = stringPtrIfNotEmpty(controlCellID)
		// Origin is resource identity, not connection identity. Nodes cannot assert it;
		// the trigger processor restores it only from server-owned stream/artifact state.
		trigger.OriginClusterId = nil
		syncMistTriggerPlacement(trigger, cid, controlCellID)
	}

	triggerType := trigger.GetTriggerType()
	requestID := trigger.GetRequestId()
	blocking := trigger.GetBlocking()
	needsDurableAck := mist.IsDurableTriggerType(triggerType)

	if !controlStreamIsCurrentOrUntracked(nodeID, stream) {
		incMistTrigger(triggerType, blocking, "stale_connection")
		logger.WithFields(logging.Fields{
			"trigger_type": triggerType,
			"request_id":   requestID,
			"node_id":      nodeID,
		}).Warn("Dropping MistServer trigger from stale Helmsman control stream")
		if blocking {
			sendMistTriggerResponse(stream, &ipcpb.MistTriggerResponse{
				RequestId: requestID,
				Response:  "",
				Abort:     true,
				ErrorCode: ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL,
			}, logger)
		}
		if needsDurableAck {
			sendMistTriggerAck(stream, requestID, errStreamNotCurrent, logger)
		}
		return
	}

	var replayEntry *blockingTriggerReplayEntry
	if blocking && trigger.GetTriggerUuid() != "" {
		key := nodeID + "\x1f" + triggerType + "\x1f" + trigger.GetTriggerUuid()
		entry, owner := acquireBlockingTriggerReplay(key)
		if !owner {
			select {
			case <-entry.ready:
			case <-stream.Context().Done():
				return
			}
			response := cloneMistTriggerResponse(entry.response)
			if response != nil {
				response.RequestId = requestID
				sendMistTriggerResponse(stream, response, logger)
			}
			return
		}
		replayEntry = entry
	}

	logger.WithFields(logging.Fields{
		"trigger_type":   triggerType,
		"request_id":     requestID,
		"node_id":        nodeID,
		"blocking":       blocking,
		"payload_type":   fmt.Sprintf("%T", trigger.GetTriggerPayload()),
		"payload_is_nil": trigger.GetTriggerPayload() == nil,
	}).Debug("Processing typed MistServer trigger")

	if mistTriggerProcessor == nil {
		incMistTrigger(triggerType, blocking, "processor_missing")
		logger.Error("MistTriggerProcessor not set, cannot process triggers")
		if blocking {
			// Send error response for blocking triggers
			response := &ipcpb.MistTriggerResponse{
				RequestId: requestID,
				Response:  "",
				Abort:     true,
			}
			if replayEntry != nil {
				completeBlockingTriggerReplay(replayEntry, response, false)
			}
			sendMistTriggerResponse(stream, response, logger)
		}
		if needsDurableAck {
			sendMistTriggerAck(stream, requestID, fmt.Errorf("processor not configured"), logger)
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
			errorCode := ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL
			if ingestErr, ok := errors.AsType[*ingesterrors.IngestError](err); ok {
				errorCode = ingestErr.Code
			}
			// Send error response for blocking triggers
			response := &ipcpb.MistTriggerResponse{
				RequestId: requestID,
				Response:  "",
				Abort:     true,
				ErrorCode: errorCode,
			}
			if replayEntry != nil {
				// A terminal business rejection is a completed trigger outcome and
				// must replay exactly like an approval. Retryable infrastructure
				// failures wake current waiters but leave a later delivery free to
				// execute the handler again.
				_, retryable := classifyTriggerError(err)
				completeBlockingTriggerReplay(replayEntry, response, !retryable)
			}
			sendMistTriggerResponse(stream, response, logger)
		}
		if needsDurableAck {
			sendMistTriggerAck(stream, requestID, err, logger)
		}
		return
	}

	if shouldAbort {
		incMistTrigger(triggerType, blocking, "processed_abort")
	} else {
		incMistTrigger(triggerType, blocking, "processed_ok")
	}

	// For non-blocking triggers, we're done — unless the trigger type
	// requires a durable ack (Helmsman has a WAL row waiting).
	if !blocking {
		if needsDurableAck {
			sendMistTriggerAck(stream, requestID, nil, logger)
		}
		logger.WithFields(logging.Fields{
			"trigger_type": triggerType,
			"request_id":   requestID,
		}).Debug("Successfully processed non-blocking trigger")
		return
	}

	// For blocking triggers, send the response back to Helmsman
	response := &ipcpb.MistTriggerResponse{
		RequestId: requestID,
		Response:  responseText,
		Abort:     shouldAbort,
	}
	if shouldAbort {
		response.Action = ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_DENY
	} else if responseText != "" {
		response.Action = ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_VALUE
	} else {
		switch triggerType {
		case string(mist.TriggerStreamSource), string(mist.TriggerStreamProcess):
			response.Action = ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_USE_CONFIGURED
		case string(mist.TriggerPushOutStart):
			response.Action = ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_KEEP
		case string(mist.TriggerPlayRewrite), string(mist.TriggerPushRewrite), string(mist.TriggerUserNew):
			response.Action = ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_DENY
		}
	}
	if !shouldAbort && trigger.GetPushRewrite() != nil {
		response.IngestGeneration = trigger.GetEventId()
		response.IngestConnectorPid = trigger.GetPushRewrite().GetPid()
	}
	if replayEntry != nil {
		completeBlockingTriggerReplay(replayEntry, response, true)
	}

	sendMistTriggerResponse(stream, response, logger)
	if needsDurableAck {
		// A blocking trigger that also needs durable ack: send both. The
		// blocking response is what Mist waits on; the ack is what
		// Helmsman uses to truncate its WAL row.
		sendMistTriggerAck(stream, requestID, nil, logger)
	}

	loggedResponse := responseText
	if triggerType == string(mist.TriggerStreamProcess) && loggedResponse != "" {
		// STREAM_PROCESS responses can contain the short-lived Livepeer job
		// capability. The response belongs on the Mist control stream only; a
		// raw copy in logs would turn the log backend into a credential store.
		loggedResponse = "<redacted process configuration>"
	}
	logger.WithFields(logging.Fields{
		"trigger_type": triggerType,
		"request_id":   requestID,
		"response":     loggedResponse,
		"abort":        shouldAbort,
	}).Info("Sent MistTrigger response")
}

func syncMistTriggerPlacement(trigger *ipcpb.MistTrigger, clusterID, controlCellID string) {
	originClusterID := trigger.GetOriginClusterId()
	clusterIDPtr := stringPtrIfNotEmpty(clusterID)
	originClusterIDPtr := stringPtrIfNotEmpty(originClusterID)
	controlCellIDPtr := stringPtrIfNotEmpty(controlCellID)
	switch payload := trigger.GetTriggerPayload().(type) {
	case *ipcpb.MistTrigger_ViewerConnect:
		if payload.ViewerConnect == nil {
			return
		}
		payload.ViewerConnect.ClusterId = clusterIDPtr
		payload.ViewerConnect.OriginClusterId = originClusterIDPtr
		payload.ViewerConnect.ControlCellId = controlCellIDPtr
	case *ipcpb.MistTrigger_ViewerDisconnect:
		if payload.ViewerDisconnect == nil {
			return
		}
		payload.ViewerDisconnect.ClusterId = clusterIDPtr
		payload.ViewerDisconnect.OriginClusterId = originClusterIDPtr
		payload.ViewerDisconnect.ControlCellId = controlCellIDPtr
	case *ipcpb.MistTrigger_StorageLifecycleData:
		if payload.StorageLifecycleData == nil {
			return
		}
		payload.StorageLifecycleData.ClusterId = clusterIDPtr
		payload.StorageLifecycleData.OriginClusterId = originClusterIDPtr
		payload.StorageLifecycleData.ControlCellId = controlCellIDPtr
	case *ipcpb.MistTrigger_ProcessBilling:
		if payload.ProcessBilling == nil {
			return
		}
		payload.ProcessBilling.ClusterId = clusterIDPtr
		payload.ProcessBilling.OriginClusterId = originClusterIDPtr
		payload.ProcessBilling.ControlCellId = controlCellIDPtr
	}
}

// sendMistTriggerResponse sends a MistTriggerResponse back to Helmsman
func sendMistTriggerResponse(stream ipcpb.HelmsmanControl_ConnectServer, response *ipcpb.MistTriggerResponse, logger logging.Logger) {
	msg := &ipcpb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &ipcpb.ControlMessage_MistTriggerResponse{MistTriggerResponse: response},
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
func resolveOperationalMode(nodeID string, requestedMode ipcpb.NodeOperationalMode) ipcpb.NodeOperationalMode {
	// Check if we have a persisted mode in state (loaded from DB on startup or set by admin)
	persistedMode := state.DefaultManager().GetNodeOperationalMode(nodeID)
	if persistedMode != "" && persistedMode != state.NodeModeNormal {
		// Non-normal mode is persisted (admin set it), use that
		switch persistedMode {
		case state.NodeModeDraining:
			return ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_DRAINING
		case state.NodeModeMaintenance:
			return ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_MAINTENANCE
		}
	}

	// No persisted override, honor Helmsman's request if valid
	if requestedMode != ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_UNSPECIFIED {
		return requestedMode
	}

	return ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL
}

// Config seed composition and sending
var geoOnce sync.Once
var geoipReader *geoip.Reader

const edgeTelemetryTokenTTL = 365 * 24 * time.Hour

func composeConfigSeedCandidate(nodeID string, _ []string, peerAddr string, operationalMode ipcpb.NodeOperationalMode, clusterID string) (*ipcpb.ConfigSeed, configSeedFallback) {
	var lat, lon float64
	var loc string
	var ownerTenantID string
	ownerResolved := false
	clusterResolved := strings.TrimSpace(clusterID) != ""

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

	templates := []*ipcpb.StreamTemplate{
		{
			Id:    "live",
			Def:   &ipcpb.StreamDef{Name: "live", Realtime: false, StopSessions: false, Tags: []string{"live"}},
			Roles: []string{"ingest", "edge"},
			Caps:  []string{"ingest", "edge"},
		},
		{
			Id:    "vod",
			Def:   &ipcpb.StreamDef{Name: "vod", Realtime: false, StopSessions: false, Tags: []string{"vod"}},
			Roles: []string{"edge", "storage"},
			Caps:  []string{"edge", "storage"},
		},
		{
			Id:    "dvr",
			Def:   &ipcpb.StreamDef{Name: "dvr", Realtime: false, StopSessions: false, Tags: []string{"dvr"}},
			Roles: []string{"edge", "storage"},
			Caps:  []string{"edge", "storage"},
		},
		{
			Id:    "processing",
			Def:   &ipcpb.StreamDef{Name: "processing", Realtime: true, ProcessControlledRealtime: true, StopSessions: false, Tags: []string{"processing"}},
			Roles: []string{"edge", "storage"},
			Caps:  []string{"processing"},
		},
		{
			Id:    "pull",
			Def:   &ipcpb.StreamDef{Name: "pull", Realtime: false, StopSessions: false, Tags: []string{"pull"}},
			Roles: []string{"edge"},
			Caps:  []string{"edge"},
		},
	}

	var tlsBundle *ipcpb.TLSCertBundle
	var siteConfig *ipcpb.SiteConfig

	resolvedClusterID := clusterID
	if quartermasterClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		node, err := quartermasterClient.GetNodeByLogicalName(ctx, nodeID)
		cancel()
		if err == nil && node != nil {
			clusterResolved = true
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
			ownerResolved = true
			ownerTenantID = strings.TrimSpace(ownerResp.GetOwnerTenantId())
			clusterResolved = true
			if resolvedClusterID == "" {
				resolvedClusterID = strings.TrimSpace(ownerResp.GetClusterId())
			}
		}
	}
	var isPlatformOfficial bool
	clusterTLSResolved := resolvedClusterID == "" && clusterResolved
	clusterClassResolved := clusterTLSResolved
	platformTLSResolved := clusterTLSResolved
	if resolvedClusterID != "" {
		rootDomain := platformRootDomain()
		slug := pkgdns.SanitizeLabel(resolvedClusterID)

		siteConfig = &ipcpb.SiteConfig{
			SiteAddress: fmt.Sprintf("*.%s.%s", slug, rootDomain),
			EdgeDomain:  pkgdns.EdgeNodeFQDN(nodeID, slug, rootDomain),
			PoolDomain:  fmt.Sprintf("edge.%s.%s", slug, rootDomain),
			AcmeEmail:   os.Getenv("ACME_EMAIL"),
		}

		if bundle, found, bundleErr := fetchClusterTLSBundleByClusterID(resolvedClusterID, rootDomain); bundleErr == nil {
			clusterTLSResolved = true
			if found {
				tlsBundle = bundle
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
				clusterClassResolved = true
				isPlatformOfficial = resp.GetCluster().GetIsPlatformOfficial()
			}
		}
		if clusterClassResolved && !isPlatformOfficial {
			platformTLSResolved = true
		}
	}

	caBundle := readConfiguredCABundle()
	telemetry := buildEdgeTelemetryConfig(nodeID, resolvedClusterID, ownerTenantID)

	seed := &ipcpb.ConfigSeed{
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
		FoghornBalancerBase: FoghornBalancerBaseForNode(resolvedClusterID, nodeID),
		SeedVersion:         0,
	}
	if tlsBundle != nil {
		seed.TlsBundles = []*ipcpb.TLSCertBundle{tlsBundle}
	}
	if isPlatformOfficial {
		extra, extraErr := fetchPlatformEdgeBundle()
		if extraErr == nil {
			platformTLSResolved = true
			if extra != nil {
				seed.TlsBundles = append(seed.TlsBundles, extra)
			}
		}
	}
	// Per-tenant TLS bundles: for every paying tenant subscribed to this
	// cluster, include their *.{tenant}.cdn.{root} cert. Best-effort;
	// missing certs (still pending issuance) are skipped silently and
	// reconciled on the next cycle.
	tenantBundles, removedTenantBundleIDs, tenantBundlesErr := fetchTenantBundles(resolvedClusterID)
	tenantTLSResolved := tenantBundlesErr == nil && clusterResolved
	if tenantBundlesErr == nil {
		seed.TlsBundles = append(seed.TlsBundles, tenantBundles...)
	}
	tlsResolved := clusterTLSResolved && clusterClassResolved && platformTLSResolved && tenantTLSResolved
	return seed, configSeedFallback{
		preserveTenant:      !ownerResolved,
		preserveSite:        !clusterResolved,
		preserveTLS:         !tlsResolved,
		preserveTelemetry:   !ownerResolved || !clusterResolved,
		removedTLSBundleIDs: removedTenantBundleIDs,
	}
}

// composeConfigSeed is retained for pure callers and tests. Production send
// paths use composeConfigSeedCandidate plus SendConfigSeedWithFallback so the
// fallback base is read under the same row lock as persistence.
func composeConfigSeed(nodeID string, roles []string, peerAddr string, operationalMode ipcpb.NodeOperationalMode, clusterID string) *ipcpb.ConfigSeed {
	seed, fallback := composeConfigSeedCandidate(nodeID, roles, peerAddr, operationalMode, clusterID)
	lastGood, err := loadLastConfigSeed(context.Background(), nodeID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		logging.NewLogger().WithError(err).WithField("node_id", nodeID).Warn("Failed to load last durable ConfigSeed")
	}
	if lastGood != nil {
		return mergeConfigSeedFallback(seed, lastGood, fallback)
	}
	return seed
}

type configSeedFallback struct {
	preserveTenant    bool
	preserveSite      bool
	preserveTLS       bool
	preserveTelemetry bool
	// removedTLSBundleIDs contains authoritative Navigator found=false results.
	// Unlike a shorter Quartermaster list, these must delete durable last-good
	// material on the next seed.
	removedTLSBundleIDs map[string]struct{}
}

// mergeConfigSeedFallback preserves central-plane-derived fields from the
// last durably sent seed only when compose observed a central dependency
// failure. Locally derived identity, templates, mode, capability URL and the
// newly allocated version always win.
func mergeConfigSeedFallback(current, lastGood *ipcpb.ConfigSeed, fallback configSeedFallback) *ipcpb.ConfigSeed {
	if current == nil || lastGood == nil || current.GetNodeId() != lastGood.GetNodeId() {
		return current
	}
	merged := proto.CloneOf(lastGood)
	merged.NodeId = current.GetNodeId()
	merged.SeedVersion = current.GetSeedVersion()
	merged.OperationalMode = current.GetOperationalMode()
	merged.Templates = current.GetTemplates()
	if current.GetLatitude() != 0 || current.GetLongitude() != 0 || current.GetLocationName() != "" {
		merged.Latitude = current.GetLatitude()
		merged.Longitude = current.GetLongitude()
		merged.LocationName = current.GetLocationName()
	}
	if !fallback.preserveTenant {
		merged.TenantId = current.GetTenantId()
	}
	if !fallback.preserveSite {
		merged.Site = current.GetSite()
	}
	if !fallback.preserveTLS {
		merged.Tls = current.GetTls()
	}
	merged.TlsBundles = mergeConfigSeedTLSBundles(
		current.GetTlsBundles(), lastGood.GetTlsBundles(), fallback.preserveTLS, fallback.removedTLSBundleIDs, time.Now(),
	)
	if len(current.GetCaBundle()) > 0 {
		merged.CaBundle = current.GetCaBundle()
	}
	if !fallback.preserveTelemetry {
		merged.Telemetry = current.GetTelemetry()
	}
	if current.GetFoghornBalancerBase() != "" {
		merged.FoghornBalancerBase = current.GetFoghornBalancerBase()
	}
	if current.GetProcessing() != nil {
		merged.Processing = current.GetProcessing()
	}
	return merged
}

// mergeConfigSeedTLSBundles treats a shorter tenant-authority list as an
// incomplete control-plane view, not an edge credential revocation. Current
// material always wins by bundle ID; an omitted tenant bundle remains local
// only through its certificate validity bound. Explicit subscription removal
// first withdraws that cluster's Navigator DNS membership, so retaining the
// no-longer-routed credential until expiry preserves outage tolerance without
// preserving traffic authority. On a broader TLS resolution failure, the same
// rule also preserves missing non-tenant bundles from the durable seed.
func mergeConfigSeedTLSBundles(current, lastGood []*ipcpb.TLSCertBundle, preserveAll bool, authoritativeRemovals map[string]struct{}, now time.Time) []*ipcpb.TLSCertBundle {
	merged := make([]*ipcpb.TLSCertBundle, 0, len(current)+len(lastGood))
	seen := make(map[string]struct{}, len(current)+len(lastGood))
	for _, bundle := range current {
		if bundle == nil || strings.TrimSpace(bundle.GetBundleId()) == "" {
			continue
		}
		bundleID := bundle.GetBundleId()
		if _, duplicate := seen[bundleID]; duplicate {
			continue
		}
		seen[bundleID] = struct{}{}
		merged = append(merged, bundle)
	}
	for _, bundle := range lastGood {
		if bundle == nil {
			continue
		}
		bundleID := strings.TrimSpace(bundle.GetBundleId())
		if bundleID == "" {
			continue
		}
		if _, present := seen[bundleID]; present {
			continue
		}
		if _, removed := authoritativeRemovals[bundleID]; removed {
			continue
		}
		validTenantAuthority := strings.HasPrefix(bundleID, "tenant:") && bundle.GetExpiresAt() > now.Unix()
		if !preserveAll && !validTenantAuthority {
			continue
		}
		seen[bundleID] = struct{}{}
		merged = append(merged, bundle)
	}
	return merged
}

// fetchTenantBundles queries Quartermaster for the paying tenants
// subscribed to clusterID, then pulls each tenant's TLS bundle from
// Navigator. During an alias transition Navigator may return the last-good
// bundle; its SANs, not Quartermaster's replacement label, remain the serving
// authority until the replacement certificate is issued and applied.
func fetchTenantBundles(clusterID string) ([]*ipcpb.TLSCertBundle, map[string]struct{}, error) {
	if clusterID == "" {
		return nil, nil, nil
	}
	if quartermasterClient == nil || navigatorClient == nil {
		return nil, nil, errors.New("tenant TLS authority is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := quartermasterClient.ListAliasedTenantsForCluster(ctx, clusterID)
	if err != nil {
		return nil, nil, err
	}
	if resp == nil || len(resp.GetTenants()) == 0 {
		return nil, nil, nil
	}
	rootDomain := platformRootDomain()
	tenantZoneLabel := pkgdns.TenantAliasZoneLabel
	return collectTenantBundles(resp.GetTenants(), tenantZoneLabel, rootDomain, func(bundleID string) (*dnspb.GetTLSBundleResponse, error) {
		certCtx, certCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer certCancel()
		return navigatorClient.GetTLSBundle(certCtx, &dnspb.GetTLSBundleRequest{BundleId: bundleID})
	})
}

func collectTenantBundles(tenants []*quartermasterpb.AliasedTenantRef, tenantZoneLabel, rootDomain string, fetch func(string) (*dnspb.GetTLSBundleResponse, error)) ([]*ipcpb.TLSCertBundle, map[string]struct{}, error) {
	out := make([]*ipcpb.TLSCertBundle, 0, len(tenants))
	authoritativeRemovals := make(map[string]struct{})
	for _, ref := range tenants {
		bundleID := "tenant:" + ref.GetTenantId()
		certResp, certErr := fetch(bundleID)
		if certErr != nil {
			return nil, nil, certErr
		}
		found, presenceErr := navigatorTLSBundleFound(certResp)
		if presenceErr != nil {
			return nil, nil, fmt.Errorf("tenant TLS bundle %s: %w", bundleID, presenceErr)
		}
		if !found {
			authoritativeRemovals[bundleID] = struct{}{}
			continue
		}
		siteAddresses, addressErr := tenantBundleSiteAddresses(certResp.GetDomains(), tenantZoneLabel, rootDomain)
		if addressErr != nil {
			return nil, nil, fmt.Errorf("tenant TLS bundle %s: %w", bundleID, addressErr)
		}
		out = append(out, &ipcpb.TLSCertBundle{
			CertPem:       certResp.GetCertPem(),
			KeyPem:        certResp.GetKeyPem(),
			Domain:        strings.Join(siteAddresses, ","),
			ExpiresAt:     certResp.GetExpiresAt(),
			BundleId:      bundleID,
			SiteAddresses: siteAddresses,
			Version:       certResp.GetVersion(),
		})
	}
	return out, authoritativeRemovals, nil
}

func tenantBundleSiteAddresses(domains []string, tenantZoneLabel, rootDomain string) ([]string, error) {
	tenantZoneLabel = strings.Trim(strings.ToLower(strings.TrimSpace(tenantZoneLabel)), ".")
	rootDomain = strings.Trim(strings.ToLower(strings.TrimSpace(rootDomain)), ".")
	if tenantZoneLabel == "" || rootDomain == "" {
		return nil, errors.New("tenant alias zone is empty")
	}
	suffix := "." + tenantZoneLabel + "." + rootDomain
	domainSet := make(map[string]struct{}, len(domains))
	for _, domain := range domains {
		domain = strings.Trim(strings.ToLower(strings.TrimSpace(domain)), ".")
		if domain != "" {
			domainSet[domain] = struct{}{}
		}
	}
	var apex string
	for domain := range domainSet {
		if strings.HasPrefix(domain, "*.") || !strings.HasSuffix(domain, suffix) {
			continue
		}
		label := strings.TrimSuffix(domain, suffix)
		if label == "" || strings.Contains(label, ".") {
			continue
		}
		if _, covered := domainSet["*."+domain]; !covered {
			continue
		}
		if apex != "" && apex != domain {
			return nil, errors.New("bundle contains multiple tenant alias authorities")
		}
		apex = domain
	}
	if apex == "" {
		return nil, errors.New("bundle does not contain an alias apex/wildcard SAN pair")
	}
	wildcard := "*." + apex
	additional := make([]string, 0, len(domainSet)-2)
	for domain := range domainSet {
		if domain != apex && domain != wildcard {
			additional = append(additional, domain)
		}
	}
	sort.Strings(additional)
	return append([]string{apex, wildcard}, additional...), nil
}

// fetchPlatformEdgeBundle pulls the platform-edge multi-SAN cert from
// Navigator. Returns nil if Navigator is unavailable or the cert hasn't
// been issued yet. Caller is responsible for deciding which nodes
// receive this bundle (only platform_official cluster edges).
func fetchPlatformEdgeBundle() (*ipcpb.TLSCertBundle, error) {
	if navigatorClient == nil {
		return nil, errors.New("platform edge TLS authority is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := navigatorClient.GetTLSBundle(ctx, &dnspb.GetTLSBundleRequest{
		BundleId: "platform:edge-multi",
	})
	if err != nil {
		return nil, err
	}
	found, presenceErr := navigatorTLSBundleFound(resp)
	if presenceErr != nil {
		return nil, presenceErr
	}
	if !found {
		return nil, nil
	}
	rootDomain := platformRootDomain()
	return &ipcpb.TLSCertBundle{
		CertPem:       resp.GetCertPem(),
		KeyPem:        resp.GetKeyPem(),
		Domain:        strings.Join(resp.GetDomains(), ","),
		ExpiresAt:     resp.GetExpiresAt(),
		BundleId:      "platform:edge-multi",
		SiteAddresses: platformEdgeSiteAddresses(rootDomain),
		Version:       resp.GetVersion(),
	}, nil
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
		MetricsExtraLabels []string `json:"metrics_extra_labels"`
	} `json:"vm_access"`
	jwt.RegisteredClaims
}

func buildEdgeTelemetryConfig(nodeID, clusterID, tenantID string) *commonpb.EdgeTelemetryConfig {
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
	return &commonpb.EdgeTelemetryConfig{
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
			if baseURL := pkgdns.NormalizeDomainScope(cluster.GetBaseUrl()); baseURL != "" {
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
	claims.VMAccess.MetricsExtraLabels = []string{"frameworks_node=" + nodeID}
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
	if key, parseErr := x509.ParseECPrivateKey(block.Bytes); parseErr == nil {
		return key, nil
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

func resolveClusterTLSBundle(nodeID string) *ipcpb.TLSCertBundle {
	bundle, found, err := fetchClusterTLSBundle(nodeID)
	if err != nil || !found {
		return nil
	}
	return bundle
}

func SendLocalConfigSeed(nodeID string, seed *ipcpb.ConfigSeed) error {
	if seed == nil {
		return fmt.Errorf("nil ConfigSeed")
	}
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &ipcpb.ControlMessage{
		Payload: &ipcpb.ControlMessage_ConfigSeed{ConfigSeed: seed},
		SentAt:  timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

func SendLocalBalancerCapabilityUpdate(nodeID string, update *ipcpb.BalancerCapabilityUpdate) error {
	if update == nil {
		return fmt.Errorf("nil BalancerCapabilityUpdate")
	}
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	return c.stream.Send(&ipcpb.ControlMessage{
		Payload: &ipcpb.ControlMessage_BalancerCapabilityUpdate{BalancerCapabilityUpdate: update},
		SentAt:  timestamppb.Now(),
	})
}

// SendConfigSeed sends a ConfigSeed to the given node, relaying via HA if needed.
func SendConfigSeed(nodeID string, seed *ipcpb.ConfigSeed) error {
	persistCtx, persistCancel := context.WithTimeout(context.Background(), 3*time.Second)
	_, persistErr := allocateAndPersistConfigSeed(persistCtx, nodeID, seed)
	persistCancel()
	if persistErr != nil {
		return persistErr
	}
	err := SendLocalConfigSeed(nodeID, seed)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil || seed == nil {
		return ErrNotConnected
	}
	return relayFailure(err, commandRelay.forward(context.Background(), &foghornrelaypb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &foghornrelaypb.ForwardCommandRequest_ConfigSeed{ConfigSeed: seed},
	}))
}

// SendConfigSeedWithFallback merges outage-preserved fields from the latest
// durable seed while holding the node's ConfigSeed row lock, then sends that
// exact committed payload. A concurrent producer therefore cannot be rolled
// back by a stale pre-transaction fallback clone.
func SendConfigSeedWithFallback(nodeID string, seed *ipcpb.ConfigSeed, fallback configSeedFallback) error {
	if seed == nil {
		return fmt.Errorf("nil ConfigSeed")
	}
	persistCtx, persistCancel := context.WithTimeout(context.Background(), 3*time.Second)
	persisted, persistErr := prepareAndPersistConfigSeed(persistCtx, nodeID, func(latest *ipcpb.ConfigSeed) (*ipcpb.ConfigSeed, error) {
		candidate := proto.CloneOf(seed)
		if latest != nil {
			candidate = mergeConfigSeedFallback(candidate, latest, fallback)
		}
		return candidate, nil
	})
	persistCancel()
	if persistErr != nil {
		return persistErr
	}
	proto.Reset(seed)
	proto.Merge(seed, persisted)
	err := SendLocalConfigSeed(nodeID, seed)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	return relayFailure(err, commandRelay.forward(context.Background(), &foghornrelaypb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &foghornrelaypb.ForwardCommandRequest_ConfigSeed{ConfigSeed: seed},
	}))
}

func SendDesiredStateUpdate(nodeID string, update *ipcpb.DesiredStateUpdate) error {
	err := SendLocalDesiredStateUpdate(nodeID, update)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil || update == nil {
		return ErrNotConnected
	}
	return relayFailure(err, commandRelay.forward(context.Background(), &foghornrelaypb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &foghornrelaypb.ForwardCommandRequest_DesiredStateUpdate{DesiredStateUpdate: update},
	}))
}

func SendLocalDesiredStateUpdate(nodeID string, update *ipcpb.DesiredStateUpdate) error {
	if update == nil {
		return fmt.Errorf("nil DesiredStateUpdate")
	}
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	return c.stream.Send(&ipcpb.ControlMessage{
		Payload: &ipcpb.ControlMessage_DesiredStateUpdate{DesiredStateUpdate: update},
		SentAt:  timestamppb.Now(),
	})
}

func SendLocalPushOperationalMode(nodeID string, mode ipcpb.NodeOperationalMode) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}

	// Helmsman sidecar does NOT merge ConfigSeeds; ApplySeed overwrites lastSeed.
	// Send a full seed to avoid wiping previously seeded fields.
	seed, fallback := composeConfigSeedCandidate(nodeID, nil, c.peerAddr, mode, "")
	return SendConfigSeedWithFallback(nodeID, seed, fallback)
}

// PushOperationalMode sends a ConfigSeed with the specified operational mode to the node,
// relaying via HA if needed.
func PushOperationalMode(nodeID string, mode ipcpb.NodeOperationalMode) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	peerAddr := ""
	if c != nil {
		peerAddr = c.peerAddr
	}
	seed, fallback := composeConfigSeedCandidate(nodeID, nil, peerAddr, mode, "")
	return SendConfigSeedWithFallback(nodeID, seed, fallback)
}

// processModeChangeRequest handles an upstream mode change request from Helmsman.
// Validates the mode and applies it via the existing SetNodeOperationalMode + PushOperationalMode path.
func processModeChangeRequest(req *ipcpb.ModeChangeRequest, nodeID string, _ ipcpb.HelmsmanControl_ConnectServer, log logging.Logger) {
	if req == nil {
		return
	}

	protoMode := req.GetRequestedMode()
	if protoMode == ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_UNSPECIFIED {
		protoMode = ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL
	}

	log.WithFields(logging.Fields{
		"node_id":        nodeID,
		"requested_mode": protoMode.String(),
		"reason":         req.GetReason(),
	}).Info("Received mode change request from Helmsman")

	var stateMode state.NodeOperationalMode
	switch protoMode {
	case ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_DRAINING:
		stateMode = state.NodeModeDraining
	case ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_MAINTENANCE:
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

func processUpdateApplyResult(result *ipcpb.UpdateApplyResult, fallbackNodeID string, log logging.Logger) {
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
		if err := PushOperationalMode(nodeID, ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL); err != nil && log != nil {
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

func updateResultIncludesMist(result *ipcpb.UpdateApplyResult) bool {
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
	if err := PushOperationalMode(nodeID, ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_MAINTENANCE); err != nil && log != nil {
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
		if err := PushOperationalMode(nodeID, ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL); err != nil {
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
		current, err := foghorndb.New(db).GetNodeComponentVersion(ctx, foghorndb.GetNodeComponentVersionParams{NodeID: nodeID, Component: component})
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
	deadlineArg := sql.NullTime{Time: deadline, Valid: !deadline.IsZero()}
	expectedArg := sql.NullString{}
	if len(expected) > 0 {
		encoded, err := json.Marshal(expected)
		if err != nil {
			return err
		}
		expectedArg = sql.NullString{String: string(encoded), Valid: true}
	}
	return foghorndb.New(db).UpsertNodeUpdateProgress(ctx, foghorndb.UpsertNodeUpdateProgressParams{
		NodeID: nodeID, TargetRelease: targetRelease, Phase: phase, LastError: lastError,
		Deadline: deadlineArg, ExpectedComponents: expectedArg,
	})
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
	row, err := foghorndb.New(db).GetNodeUpdateProgress(ctx, nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return nodeUpdateProgress{}, false, nil
	}
	if err != nil {
		return nodeUpdateProgress{}, false, err
	}
	return nodeUpdateProgress{TargetRelease: row.TargetRelease, Phase: row.Phase}, true, nil
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

// S3ClientInterface defines the storage operations used by freeze, sync,
// cleanup, and DVR chapter materialization.
type S3ClientInterface interface {
	GeneratePresignedPUT(key string, expiry time.Duration) (string, error)
	GeneratePresignedGET(key string, expiry time.Duration) (string, error)
	PutObject(ctx context.Context, key string, body []byte, contentType string) error
	ListPrefix(ctx context.Context, prefix string) ([]string, error)
	Delete(ctx context.Context, key string) error
	DeleteByURL(ctx context.Context, s3URL string) error
	DeletePrefix(ctx context.Context, prefix string) (int, error)
	ParseS3URL(s3URL string) (string, error)
	// ParseLocalS3URL is ParseS3URL with a bucket guard: it errors unless the URL is under THIS cell's local
	// bucket, so a foreign/remote-provider pointer is never presigned or routed as a local object.
	ParseLocalS3URL(s3URL string) (string, error)
	BuildClipS3Key(tenantID, streamName, clipHash, format string) string
	BuildDVRS3Key(tenantID, internalName, dvrHash string) string
	BuildVodS3Key(tenantID, artifactHash, filename string) string
	BuildS3URL(key string) string
	// Exists / GetObjectSize HEAD the object so completion can VERIFY a node's self-reported upload before
	// promoting it to durable state (both satisfied by *storage.S3Client). Exists maps not-found to (false, nil);
	// a transient error is returned so the caller can leave the attempt retryable rather than fail it.
	Exists(ctx context.Context, key string) (bool, error)
	GetObjectSize(ctx context.Context, key string) (int64, error)
	// HeadObjectInfo returns existence + size + ETag in one HEAD; PromoteObject copies a staging object to
	// the canonical key conditional on that ETag (then deletes staging). Together they let completion consume
	// an attempt-scoped staging upload without ever exposing a canonical-key PUT to the node.
	HeadObjectInfo(ctx context.Context, key string) (bool, int64, string, error)
	PromoteObject(ctx context.Context, srcKey, dstKey, ifMatchETag string) error
	// BackendDescriptor returns this client's IMMUTABLE storage-backend identity (bucket, endpoint, region, prefix),
	// so a write can capture the backend_id fingerprint (BackendFingerprint) it landed on for repoint-safe cleanup later.
	BackendDescriptor() (bucket, endpoint, region, prefix string)
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
	storageDeleteDelegate  StorageDeleteDelegate
)

// StorageDeleteDelegate sends a DeleteStorageObjects request to the
// Foghorn pool that owns the named storage cluster's S3. Wired from
// main.go to the federation client + peer manager pair; absent in tests
// or when federation isn't enabled. Cleanup paths fall back to a clear
// "remote storage cleanup pending" when the delegate is nil so we don't
// accidentally delete against the wrong bucket.
type StorageDeleteDelegate func(ctx context.Context, targetClusterID string, req *foghornfederationpb.DeleteStorageObjectsRequest) (*foghornfederationpb.DeleteStorageObjectsResponse, error)

// SetStorageResolverFactory wires the per-request storage cluster resolver
// factory. Called once at startup.
func SetStorageResolverFactory(f func(ctx context.Context, tenantID string) *storage.ClusterResolver) {
	storageResolverFactory = f
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

// mintAttemptID mints a server-owned freeze attempt id (128 bits of entropy, hex). The node ECHOES it at
// completion, so the DB claim binds a Foghorn-ASSIGNED operation rather than a node-chosen request id.
// Returns "" on a crypto/rand failure so the caller FAILS CLOSED (never reusing a predictable id across
// attempts, which would let a retry consume another attempt's staging capability).
func mintAttemptID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

// FreezeStagingKey derives the attempt-scoped staging object key from the base descriptor key. The presigned
// PUT (both the interactive permission path and the reconciler push path) targets this key; completion HEAD-
// verifies it and promotes it to a FRESH attempt-versioned CANDIDATE key (FreezePublishKey) — never the bare
// descriptor key. Scoping by the attempt id makes staging keys unguessable across attempts. It is the ONE
// staging-key builder both freeze dispatch paths and completion share.
func FreezeStagingKey(canonicalKey, attemptID string) string {
	return canonicalKey + ".staging." + attemptID
}

// FreezePublishKey derives the IMMUTABLE, attempt-versioned CANDIDATE key that a completion PUBLISHES the media
// object to — the durable address recorded in active_object_key once the guarded CAS flips the pointer to it.
// It is distinct from both the staging key (which the node holds a PUT for) and the bare descriptor key:
// publishing to a fresh per-attempt key means a promote never overwrites an already-served object, so a
// rollback after the copy cannot expose uncommitted bytes. The .dtsh index is NOT co-located here — it is
// version-addressed independently at FreezePublishDtshKey (<k>.dtsh.att-<attempt>).
func FreezePublishKey(canonicalKey, attemptID string) string {
	return canonicalKey + ".att-" + attemptID
}

// FreezePublishDtshKey derives the IMMUTABLE, attempt-versioned key the .dtsh index is PUBLISHED to (recorded
// in active_dtsh_key). Flat and derivable from (canonical, attempt) exactly like the staging key, so recovery
// and the terminal-clear trigger can reconstruct it. Version-addressing the .dtsh (not co-locating it at a
// fixed <media>.dtsh) means a late/duplicate attempt writes a DIFFERENT key and can never overwrite the live
// index before losing its CAS — the same guarantee the main object has.
func FreezePublishDtshKey(canonicalKey, attemptID string) string {
	return canonicalKey + ".dtsh.att-" + attemptID
}

// FreezeAssignment is the server-owned outcome of authorizing + claiming a LOCAL freeze.
type FreezeAssignment struct {
	AttemptID    string // server-minted; the node echoes it at completion
	CanonicalKey string // base descriptor key (persisted as sync_object_key); completion derives the staging + versioned candidate keys from it, never handing it to the node
	StagingURL   string // presigned PUT to the attempt-scoped staging key
	DestCluster  string // the official durable cluster this cell mints into
}

// PrepareLocalFreezeAssignment is the SINGLE shared freeze contract used by BOTH the interactive permission
// path and the proactive reconciler push path, so neither can store to the wrong backend or skip
// authorization: it resolves the tenant's OFFICIAL durable destination, authorizes source+destination,
// requires the official backing to be THIS cell's local backend, server-mints the attempt, presigns the
// attempt-scoped STAGING PUT, and CLAIMS the attempt (persisting the destination storage cluster + canonical
// key). Returns a structured denial reason when ok=false; the artifact is left untouched on any denial.
func PrepareLocalFreezeAssignment(ctx context.Context, assetType, assetHash, tenantID, streamName, serverFormat, originClusterID, nodeID string, expiry time.Duration) (FreezeAssignment, string, bool) {
	routing, ok := tenantStorageRoutingFn(ctx, tenantID)
	if !ok {
		return FreezeAssignment{}, "authorization_check_failed", false
	}
	destCluster := strings.TrimSpace(routing.officialCluster)

	nodeTenant, nodeCluster := "", ""
	if ns := state.DefaultManager().GetNodeState(nodeID); ns != nil {
		nodeTenant, nodeCluster = ns.TenantID, ns.ClusterID
	}
	if !authorizeStorageReplication(nodeTenant, nodeCluster, tenantID, destCluster, routing) {
		return FreezeAssignment{}, "cluster_not_authorized", false
	}
	if s3Client == nil {
		return FreezeAssignment{}, "s3_not_configured", false
	}
	if !canMintOfficialLocallyFn(ctx, tenantID, destCluster) {
		return FreezeAssignment{}, "official_storage_remote", false
	}

	attemptID := mintAttemptID()
	if attemptID == "" {
		return FreezeAssignment{}, "attempt_mint_failed", false
	}

	canonicalKey := ""
	switch assetType {
	case "clip":
		canonicalKey = s3Client.BuildClipS3Key(tenantID, streamName, assetHash, serverFormat)
	case "vod":
		canonicalKey = s3Client.BuildVodS3Key(tenantID, assetHash, fmt.Sprintf("%s.%s", assetHash, serverFormat))
	}
	if canonicalKey == "" {
		return FreezeAssignment{}, "unsupported_asset_type", false
	}
	stagingURL, err := s3Client.GeneratePresignedPUT(FreezeStagingKey(canonicalKey, attemptID), expiry)
	if err != nil {
		return FreezeAssignment{}, "presign_failed", false
	}

	scAttr := ""
	if destCluster != "" && destCluster != originClusterID {
		scAttr = destCluster
	}
	if claimed, cErr := claimFreezeAttempt(ctx, assetHash, attemptID, nodeID, tenantID, scAttr, canonicalKey); cErr != nil || !claimed {
		return FreezeAssignment{}, "not_claimable", false
	}
	return FreezeAssignment{AttemptID: attemptID, CanonicalKey: canonicalKey, StagingURL: stagingURL, DestCluster: destCluster}, "", true
}

// resolveThumbnailStorageCluster runs the STRICT official-durable resolver for the thumbnail upload flow. The only
// durable destination is the tenant's OFFICIAL cluster (from the cached Quartermaster lookup); the origin/ingest
// cluster is never a candidate. A missing resolver or an unresolved official FAILS CLOSED (StorageUnavailable) —
// there is no local/caller fallback, so a routing failure never silently mints a tenant's thumbnails locally or
// onto a BYOC origin. A remote official resolves to federation (which the mint then drops, see the caller).
func resolveThumbnailStorageCluster(ctx context.Context, tenantID, _ string) (string, storage.StorageMintMode) {
	// FAIL CLOSED when no storage resolver is wired: without it there is no proof of the tenant's official
	// durable destination, and assuming the origin cluster is locally mintable would contradict official-durable
	// selection (and could mint a tenant's thumbnails onto a BYOC origin). A missing resolver is unavailable.
	if storageResolverFactory == nil {
		return "", storage.StorageUnavailable
	}
	resolver := storageResolverFactory(ctx, tenantID)
	if resolver == nil {
		return "", storage.StorageUnavailable
	}
	// Durable destination is the tenant's OFFICIAL cluster only, via the STRICT resolver: the origin cluster is
	// never a candidate, and an unresolved official is StorageUnavailable (no local/caller fallback) — an
	// advertised BYOC origin can never win the durable thumbnail write, and a routing/config failure never
	// silently mints locally. A remote official → federation.
	return resolver.ResolveOfficialDurable(resolveOfficialClusterID(ctx, tenantID))
}

func resolveOfficialClusterID(ctx context.Context, tenantID string) string {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" || quartermasterClient == nil {
		return ""
	}
	v, ok, err := officialClusterCache.Get(ctx, "official:"+tenantID, func(loadCtx context.Context, _ string) (interface{}, bool, error) {
		rctx, cancel := context.WithTimeout(loadCtx, 1*time.Second)
		defer cancel()
		routing, qErr := quartermasterClient.GetClusterRouting(rctx, &quartermasterpb.GetClusterRoutingRequest{TenantId: tenantID})
		if qErr != nil {
			return "", false, qErr
		}
		if routing == nil {
			return "", true, nil
		}
		official := strings.TrimSpace(routing.GetOfficialClusterId())
		if official == "" {
			// Quartermaster omits official_cluster_id when it equals the tenant's primary cluster; normalize
			// to the primary so single-cluster tenants still resolve a durable destination (mirrors freeze).
			official = strings.TrimSpace(routing.GetClusterId())
		}
		return official, true, nil
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
func processFreezePermissionRequest(req *ipcpb.FreezePermissionRequest, nodeID string, connProtocolVersion int32, connFence int64, stream ipcpb.HelmsmanControl_ConnectServer, logger logging.Logger) {
	requestID := req.GetRequestId()
	assetType := req.GetAssetType()
	assetHash := req.GetAssetHash()
	sizeBytes := req.GetSizeBytes()

	logger.WithFields(logging.Fields{
		"request_id": requestID,
		"asset_type": assetType,
		"asset_hash": assetHash,
		"size_bytes": sizeBytes,
		"node_id":    nodeID,
	}).Info("Processing freeze permission request")

	// Note: the s3Client nil-check is deferred until after the storage resolver runs, so the resolver
	// verdict (unavailable / peer-cluster reject / local) is produced first with a structured reason
	// rather than a blanket "s3_not_configured".

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if assetType == "dvr" || assetType == "dvr_segment" || assetType == "dvr_manifest" {
		sendFreezePermissionResponse(stream, &ipcpb.FreezePermissionResponse{
			RequestId: requestID,
			AssetHash: assetHash,
			Approved:  false,
			Reason:    "dvr_freeze_unsupported",
		}, logger)
		return
	}

	// Server-observed protocol gate: a sidecar that predates the staged, server-minted freeze protocol would
	// upload to the canonical key and complete without the attempt id, so a granted freeze could never verify
	// or match the completion CAS. Deny explicitly (fail closed) so an un-upgraded node is signalled rather
	// than silently accumulating in_progress attempts that only stale recovery reaps. The version is the one
	// CAPTURED for THIS session at Register (passed in), so the check and the response below are bound to the
	// same connection that made the request — a concurrent reconnect cannot admit this request under a
	// different session's version.
	if !ControlFeaturesForProtocol(connProtocolVersion).StagedFreeze {
		logger.WithFields(logging.Fields{"node_id": nodeID, "protocol_version": connProtocolVersion, "required": FreezeStagedProtocolMin}).
			Warn("Denying freeze: sidecar control-protocol version predates staged freeze; upgrade the sidecar")
		sendFreezePermissionResponse(stream, &ipcpb.FreezePermissionResponse{
			RequestId: requestID,
			AssetHash: assetHash,
			Approved:  false,
			Reason:    "sidecar_protocol_unsupported",
		}, logger)
		return
	}

	lookupHash := assetHash
	lookupType := assetType

	// Resolve tenant (and stream/origin) via the identity facade FIRST — the foghorn.artifacts lookup
	// below MUST be tenant-scoped so a request can never read another tenant's row (partition scoping /
	// authorization, not collision avoidance: artifact_hash is a randomly-minted, globally-unique id).
	// origin_cluster_id is used only for storage-cluster attribution (scAttr), not destination selection.
	var tenantID, commodoreOrigin, commodoreStream string
	var identityErr error
	if resolver := identity.Default(); resolver != nil {
		resolveCtx, resolveCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer resolveCancel()
		if id, resolveErr := resolver.ResolveArtifact(resolveCtx, assetHash, assetType); resolveErr == nil {
			tenantID = id.TenantID
			commodoreStream = id.StreamInternalName
			commodoreOrigin = id.OriginClusterID
		} else {
			identityErr = resolveErr
		}
	} else {
		identityErr = errors.New("identity resolver not configured")
	}

	// TENANT-SCOPED metadata lookup (stream/origin/sync_status/format). The catalog format is the
	// IMMUTABLE server-owned input to the canonical S3 key, so this read must FAIL CLOSED: a transient DB
	// error would otherwise default the format to mp4 and mint an mp4 key, while completion (which
	// re-reads the row) could later record a *.webm key synced. On a genuine query error we reject the
	// freeze; only sql.ErrNoRows (row not yet registered) is tolerated — the authorization gate below
	// then rejects it anyway.
	var streamName string
	var originCluster sql.NullString
	var storageClusterCol sql.NullString
	var syncStatus sql.NullString
	var catalogFormat string
	if tenantID != "" {
		meta, metaErr := foghorndb.New(db).GetFreezeArtifactMetadata(ctx, foghorndb.GetFreezeArtifactMetadataParams{
			ArtifactHash: lookupHash, ArtifactType: lookupType, TenantID: tenantID,
		})
		if metaErr == nil {
			streamName, originCluster, storageClusterCol, syncStatus = meta.StreamInternalName.String, meta.OriginClusterID, meta.StorageClusterID, meta.SyncStatus
			catalogFormat = meta.Format
		} else if !errors.Is(metaErr, sql.ErrNoRows) {
			logger.WithError(metaErr).WithField("asset_hash", lookupHash).Warn("Freeze metadata lookup failed; rejecting (cannot derive canonical object key without the catalog format)")
			sendFreezePermissionResponse(stream, &ipcpb.FreezePermissionResponse{
				RequestId: requestID, AssetHash: assetHash, Approved: false, Reason: "metadata_unavailable",
			}, logger)
			return
		}
	}
	// The canonical key's format is server-owned (catalog), defaulting to mp4 when the catalog has none
	// yet — the SAME default completion applies, so the two stages always agree on the object.
	serverFormat := "mp4"
	if f := strings.TrimSpace(catalogFormat); f != "" {
		serverFormat = f
	}
	if streamName == "" {
		streamName = commodoreStream
	}
	if commodoreOrigin != "" && !originCluster.Valid {
		originCluster = sql.NullString{String: commodoreOrigin, Valid: true}
	}

	// AUTHORIZATION — enforced BEFORE any state creation (no lifecycle row is written for an
	// unauthorized request), in TWO independent gates:
	//
	//   (1) POSSESSION (here): the requesting node must hold a COMPLETE, non-orphaned copy of a LIVE
	//       (non-deleted) artifact (foghorn.artifact_nodes, tenant-scoped via the artifacts join). Same
	//       canonical "servable complete copy" predicate the relay/reconciler use.
	//
	//   (2) TWO-SIDED STORAGE AUTHORITY (after the destination is resolved, below): possession is
	//       SELF-ATTESTED, so it is necessary but not sufficient. The node's SERVER-OWNED tenant/cluster
	//       (Quartermaster enrollment, held in NodeState) must be authorized to replicate for the artifact
	//       tenant, AND the tenant must be entitled to the destination cluster Foghorn resolves — the same
	//       tenant_cluster_access entitlement the cross-cluster serving path uses. See authorizeStorageReplication.
	//
	// Fail closed on a DB error, no qualifying copy, or an unauthorized cluster/tenant. (Completion later
	// re-checks request/node/state and consumes the persisted server-derived descriptor.)
	if tenantID == "" {
		reason := "identity_invalid"
		if errors.Is(identityErr, identity.ErrUnavailable) {
			reason = "identity_unavailable"
		} else if errors.Is(identityErr, identity.ErrUnknown) || errors.Is(identityErr, identity.ErrNotFound) {
			reason = "asset_not_found"
		} else if identityErr != nil {
			reason = "identity_unavailable"
		}
		entry := logger.WithFields(logging.Fields{"asset_hash": assetHash, "asset_type": assetType, "reason": reason})
		if reason == "asset_not_found" {
			entry.Debug("Freeze skipped for uncatalogued asset")
		} else {
			entry.WithError(identityErr).Warn("Could not resolve tenant for freeze request")
		}
		sendFreezePermissionResponse(stream, &ipcpb.FreezePermissionResponse{
			RequestId: requestID, AssetHash: assetHash, Approved: false, Reason: reason,
		}, logger)
		return
	}
	nodeHoldsCopy, aErr := foghorndb.New(db).NodeHoldsLiveArtifactCopy(ctx, foghorndb.NodeHoldsLiveArtifactCopyParams{
		ArtifactHash: lookupHash, NodeID: nodeID, TenantID: tenantID,
	})
	if aErr != nil {
		logger.WithError(aErr).WithFields(logging.Fields{"asset_hash": lookupHash, "node_id": nodeID}).
			Error("Failed to verify node artifact ownership for freeze permission")
		sendFreezePermissionResponse(stream, &ipcpb.FreezePermissionResponse{
			RequestId: requestID, AssetHash: assetHash, Approved: false, Reason: "authorization_check_failed",
		}, logger)
		return
	}
	if !nodeHoldsCopy {
		logger.WithFields(logging.Fields{"asset_hash": lookupHash, "node_id": nodeID, "tenant_id": tenantID}).
			Warn("Denying freeze permission: requesting node holds no complete, non-orphaned copy of the live artifact")
		sendFreezePermissionResponse(stream, &ipcpb.FreezePermissionResponse{
			RequestId: requestID, AssetHash: assetHash, Approved: false, Reason: "node_copy_absent",
		}, logger)
		return
	}

	// Possession (gate 1) above proved a live foghorn.artifacts row + a complete node copy exist for this
	// (tenant, hash), so there is no missing-row recovery or uncataloged-reject path here: a truly absent
	// artifact fails the ownership gate and returned above. streamName/origin come from the tenant-scoped
	// lookup or the identity facade.

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

	// Already durably stored on a REMOTE cluster → skip upload, just evict the local warm copy. Checked
	// BEFORE authorizing a new mint (possession authorizes eviction of an already-durable copy). Requires
	// VERIFIED durability (sync_status='synced'); the DURABLE location is storage_cluster_id ONLY — a
	// not-yet-synced artifact has a NULL storage_cluster_id and proceeds to a local mint into official storage.
	artifactStorageCluster := strings.TrimSpace(storageClusterCol.String)
	if artifactStorageCluster != "" && artifactStorageCluster != localClusterID && !isServedCluster(artifactStorageCluster) {
		if !syncStatus.Valid || syncStatus.String != "synced" {
			logger.WithFields(logging.Fields{
				"asset_hash": assetHash, "storage_cluster": artifactStorageCluster, "sync_status": syncStatus.String,
			}).Warn("Rejecting freeze: remote storage cluster but artifact is not yet durably synced (cannot verify remote durability from this cell)")
			sendFreezePermissionResponse(stream, &ipcpb.FreezePermissionResponse{
				RequestId: requestID, AssetHash: assetHash, Approved: false, Reason: "remote_not_durable",
			}, logger)
			return
		}
		logger.WithFields(logging.Fields{"asset_hash": assetHash, "storage_cluster": artifactStorageCluster}).
			Info("Remote artifact already durably synced — skip_upload=true (evict local warm copy)")
		sendFreezePermissionResponse(stream, &ipcpb.FreezePermissionResponse{
			RequestId: requestID, AssetHash: assetHash, Approved: true, SkipUpload: true,
		}, logger)
		return
	}

	// Bind admission to the CURRENT session before claiming: if this connection was superseded by a reconnect
	// between the request's arrival and this goroutine (a new fence now owns the node), do NOT claim an attempt
	// or presign — the response would go to a dead stream and the claim would be orphaned. The newer connection
	// re-drives its own freezes. (SendLocalFreezeRequest applies the same current-session discipline for the
	// proactive path.)
	if !connIsCurrentOwner(nodeID, connFence) {
		logger.WithFields(logging.Fields{"asset_hash": assetHash, "node_id": nodeID, "conn_fence": connFence}).
			Warn("Ignoring freeze permission request from a superseded connection")
		return
	}

	// STORAGE ASSIGNMENT (gate 2 + mint + claim), through the ONE shared contract the reconciler also uses:
	// resolve the OFFICIAL destination, authorize source+destination against the tenant's server-owned
	// entitlement, require the official backing to be THIS cell's local backend, server-mint the attempt,
	// presign the attempt-scoped STAGING PUT, and claim. Any denial leaves the artifact untouched.
	expiry := 30 * time.Minute
	assignment, reason, ok := PrepareLocalFreezeAssignment(ctx, assetType, assetHash, tenantID, streamName, serverFormat, originClusterID, nodeID, expiry)
	if !ok {
		logger.WithFields(logging.Fields{"asset_hash": assetHash, "node_id": nodeID, "tenant_id": tenantID, "reason": reason}).
			Warn("Freeze permission denied")
		sendFreezePermissionResponse(stream, &ipcpb.FreezePermissionResponse{
			RequestId: requestID, AssetHash: assetHash, Approved: false, Reason: reason,
		}, logger)
		return
	}

	grant := &ipcpb.FreezePermissionResponse{
		RequestId:        requestID,
		AssetHash:        assetHash,
		Approved:         true,
		PresignedPutUrl:  assignment.StagingURL,
		UrlExpirySeconds: int64(expiry.Seconds()),
		AttemptId:        assignment.AttemptID,
	}
	// FENCE the GRANT to THIS connection generation: it carries a freshly CLAIMED, server-minted attempt, so if
	// a reconnect superseded this connection in the window between the admission check above and here, the grant
	// must NOT reach the retired stream — the newer connection re-drives its own freezes and stale recovery
	// reclaims the orphaned claim. Route through the conn's sendGate (same discipline as SendLocalFreezeRequest)
	// so the superseded check and Send cannot interleave with retirement; a non-current stream is skipped.
	c, ok := currentControlConn(nodeID, stream)
	if !ok {
		logger.WithFields(logging.Fields{"asset_hash": assetHash, "node_id": nodeID, "attempt_id": assignment.AttemptID}).
			Warn("Not sending freeze grant: connection is no longer the current session (superseded by a reconnect)")
		return
	}
	c.sendGate.Lock()
	if c.superseded.Load() || !connIsCurrentlyRegistered(nodeID, c) {
		c.sendGate.Unlock()
		logger.WithFields(logging.Fields{"asset_hash": assetHash, "node_id": nodeID, "attempt_id": assignment.AttemptID}).
			Warn("Not sending freeze grant: connection superseded after admission")
		return
	}
	sendFreezePermissionResponse(stream, grant, logger)
	c.sendGate.Unlock()

	logger.WithFields(logging.Fields{
		"request_id": requestID, "attempt_id": assignment.AttemptID, "asset_hash": assetHash, "asset_type": assetType,
	}).Info("Freeze permission granted with presigned URLs")
}

// sendFreezePermissionResponse sends a FreezePermissionResponse back to Helmsman
func sendFreezePermissionResponse(stream ipcpb.HelmsmanControl_ConnectServer, response *ipcpb.FreezePermissionResponse, logger logging.Logger) {
	msg := &ipcpb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &ipcpb.ControlMessage_FreezePermissionResponse{FreezePermissionResponse: response},
	}

	if err := stream.Send(msg); err != nil {
		logger.WithFields(logging.Fields{
			"request_id": response.RequestId,
			"error":      err,
		}).Error("Failed to send freeze permission response")
	}
}

// claimFreezeAttempt is the local-mint convenience wrapper over ClaimFreezeAttempt using the package db
// handle; see ClaimFreezeAttempt for the guarded-claim contract.
func claimFreezeAttempt(ctx context.Context, assetHash, requestID, nodeID, tenantID, storageCluster, objectKey string) (claimed bool, err error) {
	return ClaimFreezeAttempt(ctx, db, assetHash, requestID, nodeID, tenantID, storageCluster, objectKey)
}

// ClaimFreezeAttempt is the one guarded freeze-claim, invoked by PrepareLocalFreezeAssignment for both the
// interactive and reconciler paths. In a single tenant-scoped UPDATE it re-authorizes (status='ready', a
// complete non-orphaned copy present on this node) and reserves the attempt, persisting the attempt
// identity (the SERVER-MINTED attempt id + authenticated node id), the resolved storage cluster, and the
// server-derived sync_object_key together. An idempotent same-attempt/node re-claim is accepted only with
// the identical key+cluster, so the persisted descriptor is immutable and completion promotes exactly the
// object the presigned PUT targeted. Returns claimed=false (deny) on a DB error or an unclaimable row.
func ClaimFreezeAttempt(ctx context.Context, dbh *sql.DB, assetHash, requestID, nodeID, tenantID, storageCluster, objectKey string) (claimed bool, err error) {
	if dbh == nil {
		return false, nil // no DB → cannot record an attempt → deny (fail closed)
	}
	if tenantID == "" || requestID == "" || nodeID == "" {
		return false, nil // identity incomplete → cannot scope the attempt → deny (fail closed)
	}
	if objectKey == "" {
		return false, nil // no server-derived descriptor → cannot bind the object → deny (fail closed)
	}
	// The winning (re)publication writes the object to THIS cell's CURRENT store, so the row MUST adopt the proven
	// current fingerprint. Require it: no local fingerprint (no S3 wired) means we cannot attribute the freeze, so deny
	// rather than fall back to a stale/empty backend the strict cleanup worker would later refuse.
	localBID := localBackendFingerprint()
	if localBID == "" {
		return false, fmt.Errorf("claim freeze attempt %s: no local backend fingerprint (no local S3 store) to attribute the freeze", requestID)
	}
	// status = 'ready' is the lifecycle-ready gate: a clip/vod is freezable ONLY after processing has
	// published it. A complete copy reported while the artifact is still 'uploading'/'processing' must NOT
	// be claimable, because the processing completion resets sync_status/storage_location (and may rewrite
	// format) and would strand a freeze claimed too early. The INITIAL claim (pending/failed/synced) binds
	// the descriptor + storage cluster. An idempotent same-request/node re-claim is accepted ONLY when it
	// carries the IDENTICAL object key AND storage cluster: a retry that computed a different key/cluster
	// matches zero rows (deny), so the presigned URL the caller returns can never target a different object
	// than the persisted descriptor completion will consume.
	// The claim and its publication-ledger rows commit in ONE transaction: the ledger records the attempt's
	// deterministic staging + candidate objects BEFORE the node holds any PUT URL, so a later completion whose
	// guarded transaction is lost (e.g. a concurrent duplicate that clears the attempt identity) can never leak
	// an uploaded/promoted object — the sweep collects it from the durable, identity-independent ledger row.
	tx, txErr := dbh.BeginTx(ctx, nil)
	if txErr != nil {
		return false, txErr
	}
	defer tx.Rollback() //nolint:errcheck // best-effort on the non-commit paths
	n, execErr := foghorndb.New(tx).ClaimFreezeAttempt(ctx, foghorndb.ClaimFreezeAttemptParams{
		ArtifactHash: assetHash, SyncRequestID: sql.NullString{String: requestID, Valid: true}, SyncNodeID: sql.NullString{String: nodeID, Valid: true},
		TenantID: tenantID, StorageClusterID: storageCluster, SyncObjectKey: sql.NullString{String: objectKey, Valid: true}, BackendID: sql.NullString{String: localBID, Valid: true},
	})
	if execErr != nil {
		return false, execErr
	}
	if n == 0 {
		return false, nil // nothing claimed → rollback, no ledger rows
	}
	if lErr := RecordFreezePublicationLedgerTx(ctx, tx, assetHash, tenantID, requestID, objectKey); lErr != nil {
		return false, lErr // ledger record failed → rollback the claim too (fail closed; the caller retries)
	}
	if cErr := tx.Commit(); cErr != nil {
		return false, cErr
	}
	return true, nil
}

// claimDtshAttempt records the outstanding incremental-.dtsh sync attempt (request + node) on an
// ALREADY-SYNCED artifact BEFORE the DtshSyncRequest is dispatched, so the completion can be
// authenticated the same way a freeze/sync completion is. It is TENANT-SCOPED (a claim can never touch
// another tenant's artifact) and claimable only when the main upload is 'synced' and the index isn't
// yet marked synced. It refuses to steal a live attempt owned by a DIFFERENT request/node, and — so a
// node re-reporting the same missing .dtsh every ~10s can't hammer a failed attempt — a 'failed'
// attempt is re-claimable ONLY after a backoff that GROWS with dtsh_failure_count (30s per prior
// failure, capped at 10min). A truly stuck 'in_progress' attempt (node died mid-upload) self-heals
// after 10min. Returns claimed=false (deny) on a DB error or an unclaimable row.
func claimDtshAttempt(ctx context.Context, assetHash, requestID, nodeID, tenantID string) (claimed bool, err error) {
	if db == nil {
		return false, nil
	}
	if requestID == "" || nodeID == "" || tenantID == "" {
		return false, nil // incomplete identity → fail closed
	}
	// Same live-copy contract as the main freeze claim, PLUS a kind-specific published-lifecycle gate that
	// matches the durable chk_active_dtsh_state constraint: the artifact must be in its legitimate synced
	// state (clip/vod='ready') — never a NULL/processing/failed/requested row — AND THIS node must still hold
	// a complete, non-orphaned copy, so a late .dtsh dispatch can't target a terminal/in-flight artifact or a
	// node that no longer owns the bytes.
	//
	// One transaction: the CTE snapshots (FOR UPDATE) the PREVIOUS attempt id + descriptor before the guarded
	// UPDATE overwrites the identity, so that when this claim REPLACES a different, stale (>10min) in_progress
	// attempt, that attempt's (possibly-uploaded) .dtsh staging object is durably enqueued for deletion rather
	// than leaked. (The main freeze has no equivalent here because stale-freeze recovery resets + enqueues the
	// prior attempt before it becomes re-claimable.)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // best-effort on non-commit paths
	row, qErr := foghorndb.New(tx).ClaimDtshAttempt(ctx, foghorndb.ClaimDtshAttemptParams{
		ArtifactHash: assetHash, DtshSyncRequestID: sql.NullString{String: requestID, Valid: true},
		DtshSyncNodeID: sql.NullString{String: nodeID, Valid: true}, TenantID: tenantID,
	})
	if errors.Is(qErr, sql.ErrNoRows) {
		return false, nil // not claimable
	}
	if qErr != nil {
		return false, qErr
	}
	objectKey, prevReq := row.ObjectKey, row.OldRequest
	if prevReq != "" && prevReq != requestID && objectKey != "" {
		// The superseded prior attempt may have uploaded its .dtsh staging AND had its versioned candidate
		// promoted before it lost the race — enqueue both so neither leaks.
		if eErr := EnqueueDtshAttemptGarbageTx(ctx, tx, objectKey, prevReq); eErr != nil {
			return false, eErr
		}
	}
	if cErr := tx.Commit(); cErr != nil {
		return false, cErr
	}
	return true, nil
}

// clearDtshAttempt releases a claimed .dtsh attempt back to a retryable 'failed' state (identity
// cleared, failure count bumped) — used when the request could not even be dispatched, so the next
// node report re-triggers it instead of leaving a phantom in_progress attempt.
func clearDtshAttempt(ctx context.Context, assetHash, requestID, nodeID, tenantID string) {
	if db == nil || tenantID == "" {
		return
	}
	// One transaction: release the claim AND durably enqueue the .dtsh staging object AND its versioned
	// candidate for cleanup. The dispatch failure is AMBIGUOUS (the node may have received the request and
	// uploaded the .dtsh to its staging key despite the send error, and a concurrent completion may even have
	// promoted the candidate before losing the CAS), so clearing the only identity without scheduling those
	// keys would leak them — stale recovery would never see them (this row is already synced, not freezing).
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		if registry != nil {
			registry.log.WithError(err).WithField("asset_hash", assetHash).Debug("clearDtshAttempt: begin tx failed")
		}
		return
	}
	defer tx.Rollback() //nolint:errcheck // best-effort on non-commit paths
	objectKey, qErr := foghorndb.New(tx).ClearDtshAttempt(ctx, foghorndb.ClearDtshAttemptParams{
		ArtifactHash: assetHash, DtshSyncRequestID: sql.NullString{String: requestID, Valid: true},
		DtshSyncNodeID: sql.NullString{String: nodeID, Valid: true}, TenantID: tenantID,
	})
	if errors.Is(qErr, sql.ErrNoRows) {
		return // nothing to release (already cleared / re-claimed); the >10min stale recovery still covers it
	}
	if qErr != nil {
		if registry != nil {
			registry.log.WithError(qErr).WithField("asset_hash", assetHash).Warn("clearDtshAttempt: best-effort release failed (row stays in_progress; >10min stale re-claim recovers)")
		}
		return
	}
	if objectKey != "" {
		if eErr := EnqueueDtshAttemptGarbageTx(ctx, tx, objectKey, requestID); eErr != nil {
			if registry != nil {
				registry.log.WithError(eErr).WithField("asset_hash", assetHash).Warn("clearDtshAttempt: enqueue .dtsh cleanup failed (row stays in_progress; >10min stale re-claim recovers)")
			}
			return
		}
	}
	if cErr := tx.Commit(); cErr != nil && registry != nil {
		registry.log.WithError(cErr).WithField("asset_hash", assetHash).Warn("clearDtshAttempt: commit failed (row stays in_progress; >10min stale re-claim recovers)")
	}
}

// applyDtshCompletionFailure attributes a FAILED completion to the persisted .dtsh attempt (request +
// node). A .dtsh sync runs on an already-synced row, so its failure never matches the main-upload
// guard and would otherwise be silently dropped; matching the dtsh attempt identity records the
// failure (retryable 'failed' + backoff counter) and clears the attempt so the next node report can
// re-trigger it. Returns handled=true when this completion was a known dtsh attempt (so the caller
// does NOT also run the main-upload failure guard).
func applyDtshCompletionFailure(ctx context.Context, assetHash, reportingNodeID, requestID, errorMsg, tenantID string, logger logging.Logger) (handled bool) {
	if db == nil || requestID == "" || reportingNodeID == "" || tenantID == "" {
		return false
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		logger.WithError(err).WithField("asset_hash", assetHash).Error("failed to begin dtsh sync failure tx")
		return false
	}
	defer tx.Rollback() //nolint:errcheck // best-effort on non-commit paths
	// Clear the attempt and RETURN the descriptor so the .dtsh staging object (which the node may have
	// uploaded despite reporting failure) AND its versioned candidate (a completion may have promoted it before
	// the row lost the CAS) are durably enqueued for deletion on the SAME transaction.
	objectKey, qErr := foghorndb.New(tx).FailDtshAttempt(ctx, foghorndb.FailDtshAttemptParams{
		ArtifactHash: assetHash, DtshSyncRequestID: sql.NullString{String: requestID, Valid: true},
		DtshSyncNodeID: sql.NullString{String: reportingNodeID, Valid: true}, ErrorMessage: errorMsg, TenantID: tenantID,
	})
	if errors.Is(qErr, sql.ErrNoRows) {
		return false // not a recognized dtsh attempt → let the main-upload failure guard handle it
	}
	if qErr != nil {
		logger.WithError(qErr).WithField("asset_hash", assetHash).Error("failed to record dtsh sync failure")
		return false
	}
	if objectKey != "" {
		if eErr := EnqueueDtshAttemptGarbageTx(ctx, tx, objectKey, requestID); eErr != nil {
			logger.WithError(eErr).WithField("asset_hash", assetHash).Error("failed to enqueue .dtsh cleanup on failure")
			return false
		}
	}
	if cErr := tx.Commit(); cErr != nil {
		logger.WithError(cErr).WithField("asset_hash", assetHash).Error("failed to commit dtsh sync failure")
		return false
	}
	incArtifactSyncOutcome("dtsh_failed")
	logger.WithFields(logging.Fields{"asset_hash": assetHash, "request_id": requestID, "node_id": reportingNodeID, "error": errorMsg}).
		Warn("Incremental .dtsh sync failed; attempt recorded as retryable")
	return true
}

// processFreezeProgress handles freeze progress updates from Helmsman
func processFreezeProgress(progress *ipcpb.FreezeProgress, nodeID string, logger logging.Logger) {
	logger.WithFields(logging.Fields{
		"request_id":     progress.GetRequestId(),
		"asset_hash":     progress.GetAssetHash(),
		"percent":        progress.GetPercent(),
		"bytes_uploaded": progress.GetBytesUploaded(),
		"node_id":        nodeID,
	}).Debug("Freeze progress update")
}

// SendFreezeRequest sends a proactive FreezeRequest to the given node, relaying via HA if needed.
func SendFreezeRequest(nodeID string, req *ipcpb.FreezeRequest) error {
	err := SendLocalFreezeRequest(nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	return relayFailure(err, commandRelay.forward(context.Background(), &foghornrelaypb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &foghornrelaypb.ForwardCommandRequest_Freeze{Freeze: req},
	}))
}

func SendLocalFreezeRequest(nodeID string, req *ipcpb.FreezeRequest) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	// AUTHORITATIVE admission gate. This is the FINAL owning send for a staged, server-minted freeze — both
	// the local reconciler dispatch and an HA-relayed proactive freeze terminate here on the instance that
	// owns the connection. The reconciler's pre-check is only advisory (it deliberately proceeds for
	// peer-owned nodes, and a node can reconnect with an older protocol between the pre-check and this send),
	// so the CURRENT session's declared protocol MUST be validated here, immediately before transmitting.
	// Fail closed with a non-relayable error (shouldRelay leaves a present-but-old conn un-relayed) so an old
	// sidecar can never receive a staged freeze it would mishandle.
	if !c.features().StagedFreeze {
		return ErrFreezeProtocolUnsupported
	}
	// FENCE dispatch to THIS connection generation: hold sendGate across the superseded check AND the Send, so
	// retirement (which sets superseded under the same gate) cannot interleave. Either this send completes on a
	// still-current connection, or it observes superseded and aborts with a non-relayable error — never a Send
	// over a connection a reconnect already retired.
	c.sendGate.Lock()
	defer c.sendGate.Unlock()
	if c.superseded.Load() || !connIsCurrentlyRegistered(nodeID, c) {
		return ErrConnSuperseded
	}
	msg := &ipcpb.ControlMessage{
		RequestId: req.RequestId,
		Payload:   &ipcpb.ControlMessage_FreezeRequest{FreezeRequest: req},
		SentAt:    timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

func SendLocalDtshSyncRequest(nodeID string, req *ipcpb.DtshSyncRequest) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	// FENCE dispatch to THIS connection generation (see SendLocalFreezeRequest): a .dtsh sync stages a versioned
	// index the node PUTs, so a send over a reconnect-retired connection would dispatch work the old session
	// can't complete. Hold sendGate across the superseded check + registry-generation recheck AND the Send.
	c.sendGate.Lock()
	defer c.sendGate.Unlock()
	if c.superseded.Load() || !connIsCurrentlyRegistered(nodeID, c) {
		return ErrConnSuperseded
	}
	msg := &ipcpb.ControlMessage{
		Payload: &ipcpb.ControlMessage_DtshSyncRequest{DtshSyncRequest: req},
		SentAt:  timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

// SendDtshSyncRequest sends a DtshSyncRequest to the given node, relaying via HA if needed.
func SendDtshSyncRequest(nodeID string, req *ipcpb.DtshSyncRequest) error {
	err := SendLocalDtshSyncRequest(nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	return relayFailure(err, commandRelay.forward(context.Background(), &foghornrelaypb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &foghornrelaypb.ForwardCommandRequest_DtshSync{DtshSync: req},
	}))
}

func SendLocalStopSessions(nodeID string, req *ipcpb.StopSessionsRequest) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &ipcpb.ControlMessage{
		Payload: &ipcpb.ControlMessage_StopSessionsRequest{StopSessionsRequest: req},
		SentAt:  timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

// SendStopSessions sends a StopSessionsRequest to the given node, relaying via HA if needed.
func SendStopSessions(nodeID string, req *ipcpb.StopSessionsRequest) error {
	err := SendLocalStopSessions(nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	return relayFailure(err, commandRelay.forward(context.Background(), &foghornrelaypb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &foghornrelaypb.ForwardCommandRequest_StopSessions{StopSessions: req},
	}))
}

// SendLocalInvalidateSessions sends an InvalidateSessionsRequest to a Helmsman
// that has its bidirectional stream attached to this Foghorn instance.
//
// invalidate_sessions does NOT disconnect viewers — it tells MistServer to
// re-run USER_NEW for active sessions on the listed streams. Viewers whose
// tokens still pass the (refreshed) policy continue with a brief reconnect
// blip; viewers whose tokens are now invalid are denied.
func SendLocalInvalidateSessions(nodeID string, req *ipcpb.InvalidateSessionsRequest) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &ipcpb.ControlMessage{
		Payload: &ipcpb.ControlMessage_InvalidateSessionsRequest{InvalidateSessionsRequest: req},
		SentAt:  timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

// SendInvalidateSessions sends an InvalidateSessionsRequest to the given node,
// relaying through Foghorn HA if the stream is held by a peer instance.
func SendInvalidateSessions(nodeID string, req *ipcpb.InvalidateSessionsRequest) error {
	err := SendLocalInvalidateSessions(nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	return relayFailure(err, commandRelay.forward(context.Background(), &foghornrelaypb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &foghornrelaypb.ForwardCommandRequest_InvalidateSessions{InvalidateSessions: req},
	}))
}

// SendLocalActivatePushTargets dispatches push-target activation over THIS replica's control
// connection only — deliberately no HA relay: the admission-effects worker tracks the targets in
// its process-local status map immediately before this send, and PUSH_OUT_START/PUSH_END
// attribution resolves against that map on the connection-owner replica, so tracking and dispatch
// must stay on one replica. Gated to THIS connection generation: the superseded check and the Send
// are atomic under sendGate, so a reconnect moving the node to another replica between the worker's
// ownership check and this send fails the dispatch (the obligation retries on the new owner, which
// re-tracks) instead of transmitting over a retired stream.
func SendLocalActivatePushTargets(ctx context.Context, nodeID string, req *ipcpb.ActivatePushTargets) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &ipcpb.ControlMessage{
		Payload: &ipcpb.ControlMessage_ActivatePushTargets{ActivatePushTargets: req},
		SentAt:  timestamppb.Now(),
	}
	return sendOnConnBounded(ctx, nodeID, c, msg, 5*time.Second)
}

// SendLocalDeactivatePushTargets sends a DeactivatePushTargets message to a local Helmsman.
func SendLocalDeactivatePushTargets(ctx context.Context, nodeID string, req *ipcpb.DeactivatePushTargets) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &ipcpb.ControlMessage{
		Payload: &ipcpb.ControlMessage_DeactivatePushTargets{DeactivatePushTargets: req},
		SentAt:  timestamppb.Now(),
	}
	// Caller-bounded like the other worker-driven dispatches. Retirement bounds the callback and
	// prevents queue growth; Helmsman's exact ended-generation fence protects a successor if the
	// already-running transport write surfaces late.
	return sendOnConnBounded(ctx, nodeID, c, msg, 5*time.Second)
}

// SendDeactivatePushTargets sends DeactivatePushTargets to the given node, relaying via HA if needed.
func SendDeactivatePushTargets(ctx context.Context, nodeID string, req *ipcpb.DeactivatePushTargets) error {
	err := SendLocalDeactivatePushTargets(ctx, nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	return relayFailure(err, commandRelay.forward(ctx, &foghornrelaypb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &foghornrelaypb.ForwardCommandRequest_DeactivatePushTargets{DeactivatePushTargets: req},
	}))
}

// ProcessConfigCacheUpdater updates the STREAM_PROCESS cache for an artifact.
// Used for Livepeer → local fallback: Helmsman tells Foghorn to cache the
// local-only config so the restarted push gets it via STREAM_PROCESS.
type ProcessConfigCacheUpdater func(artifactHash, processesJSON string)

var onProcessConfigCacheUpdate ProcessConfigCacheUpdater

// SetProcessConfigCacheUpdater registers the cache updater for Livepeer fallback.
func SetProcessConfigCacheUpdater(h ProcessConfigCacheUpdater) {
	onProcessConfigCacheUpdate = h
}

// clipPartialShortfallMs is the tolerance before a clip whose measured output
// is shorter than its requested span counts as partial. Mirrors Helmsman's
// maxRenditionSpanShortfallMs so the two sides agree on "materially shorter".
const clipPartialShortfallMs = 2000

// processingSpeedFromOutputs reconstructs the speed telemetry Helmsman
// attached to the job result outputs map (see processingSpeedTelemetry on the
// Helmsman side) for lifecycle enrichment. Returns nil stats when absent.
func processingSpeedFromOutputs(outputs map[string]string) (*ipcpb.ProcessingSpeedStats, *int64) {
	var wallMs *int64
	if raw := outputs["processing_wall_ms"]; raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
			wallMs = &v
		}
	}
	if outputs["speed_source"] == "" {
		return nil, wallMs
	}
	pf := func(k string) float64 {
		v, err := strconv.ParseFloat(outputs[k], 64)
		if err != nil {
			return 0
		}
		return v
	}
	pu := func(k string) uint32 {
		v, err := strconv.ParseUint(outputs[k], 10, 32)
		if err != nil {
			return 0
		}
		return uint32(v)
	}
	stats := &ipcpb.ProcessingSpeedStats{
		Ticks:            pu("speed_ticks"),
		SpeedMin:         pf("speed_min_x"),
		SpeedAvg:         pf("speed_avg_x"),
		SpeedMax:         pf("speed_max_x"),
		HardSlowTicks:    pu("hard_slow_ticks"),
		RegularSlowTicks: pu("regular_slow_ticks"),
		RampUps:          pu("ramp_ups"),
		LockoutTicks:     pu("lockout_ticks"),
		StaleHoldTicks:   pu("stale_hold_ticks"),
	}
	if raw := outputs["drain_ms"]; raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
			stats.DrainMs = &v
		}
	}
	return stats, wallMs
}

// processProcessingJobResult handles job completion/failure results from Helmsman.
func processProcessingJobResult(result *ipcpb.ProcessingJobResult, nodeID string, logger logging.Logger) {
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

	// Chapter finalization jobs use an attempt-fenced string job_id
	// ("chapter-finalize-v2-<attempt>-<chapter_id>")
	// and have no row in foghorn.processing_jobs (its job_id is UUID). Route
	// them through a dedicated handler that advances chapter state + registers
	// the chapter VOD artifact without touching the processing_jobs table.
	if chapterID, attempt, ok := chapterFinalizeIdentityFromJobID(result.GetJobId()); ok {
		handleChapterFinalizeResult(ctx, chapterID, jobStatus, attempt, result, nodeID, logger)
		return
	}

	switch jobStatus {
	case "cache_update":
		artifactHash := result.GetOutputs()["artifact_hash"]
		processesJSON := result.GetOutputs()["processes_json"]
		if artifactHash != "" && processesJSON != "" {
			// Bind to the EXACT reported job owned by the reporting node: full (job_id, artifact, node, active)
			// identity. Keying on (artifact_hash, node) alone would rewrite every sibling active job for the
			// same artifact on that node (active jobs are not unique by artifact); a foreign node, or a job not
			// dispatched to the reporting node, matches nothing.
			n, err := foghorndb.New(db).UpdateProcessingJobCache(ctx, foghorndb.UpdateProcessingJobCacheParams{
				JobID: result.GetJobId(), ArtifactHash: sql.NullString{String: artifactHash, Valid: true},
				ProcessesJson: sql.NullString{String: processesJSON, Valid: true}, ProcessingNodeID: sql.NullString{String: nodeID, Valid: true},
			})
			if err != nil {
				logger.WithError(err).WithField("artifact_hash", artifactHash).Warn("Failed to persist processing process config update")
				return
			}
			if n == 0 {
				logger.WithFields(fields).WithField("artifact_hash", artifactHash).Warn("Ignoring cache_update for a job not assigned to the reporting node")
				return
			}
			if onProcessConfigCacheUpdate != nil {
				onProcessConfigCacheUpdate(artifactHash, processesJSON)
			}
			logger.WithField("artifact_hash", artifactHash).Info("Updated process config for Livepeer fallback")
		}
		return
	case "completed":
		// A completed result MUST carry a produced output path. An empty one is a malformed
		// report (truncated IPC, producer bug): blessing it as completed would strand the
		// artifact "processing" forever with no file. Fail the job terminally instead — the
		// artifact flips to failed and the lifecycle/UI reflect reality.
		if result.GetOutputPath() == "" {
			failProcessingJobAtomic(ctx, result.GetJobId(), "completed processing result carried no output path", nodeID, logger, fields)
			return
		}

		var outputMeta *string
		if len(result.GetOutputs()) > 0 {
			if b, mErr := json.Marshal(result.GetOutputs()); mErr == nil {
				s := string(b)
				outputMeta = &s
			} else {
				logger.WithError(mErr).WithFields(fields).Warn("Failed to marshal completion outputs; storing null output_metadata")
			}
		}

		// The terminal transition is ONE transaction: artifact readiness + duration + tracks +
		// vod_metadata + origin placement (with its node-copy event) + lifecycle enqueue commit
		// together, and the job is marked completed LAST. Any failure rolls back, leaving the job
		// dispatched/processing so stale recovery retries — a completed job is never left with an
		// unready/unregistered artifact. In-memory state + reconciler wake happen only post-commit.
		completionTx, txErr := db.BeginTx(ctx, nil)
		if txErr != nil {
			logger.WithError(txErr).WithFields(fields).Error("Failed to begin completion transaction")
			return
		}
		committed := false
		defer func() {
			if !committed {
				completionTx.Rollback() //nolint:errcheck // best-effort rollback of an uncommitted tx
			}
		}()

		// Lock the job and confirm it is still active. A cancelled/deleted job or a duplicate
		// (already-completed) result is a no-op — no resurrection.
		q := foghorndb.New(completionTx)
		job, lockErr := q.LockProcessingJobForCompletion(ctx, result.GetJobId())
		if lockErr != nil {
			if errors.Is(lockErr, sql.ErrNoRows) {
				logger.WithFields(fields).Warn("Completion for unknown processing job; ignoring")
				return
			}
			logger.WithError(lockErr).WithFields(fields).Error("Failed to lock processing job for completion")
			return
		}
		jobStatusNow, assignedNode := job.Status.String, job.ProcessingNodeID
		if jobStatusNow != "dispatched" && jobStatusNow != "processing" {
			logger.WithFields(fields).Warn("Ignoring completion for a non-active processing job (cancelled, deleted, or duplicate)")
			return
		}
		// Bind the result to the node the job was dispatched to: the reporting node becomes the recorded
		// origin, so a mismatched node completing another's job would forge origin placement. Dispatch persists
		// processing_node_id (guarded, before the node can report) for EVERY node-dispatched job, so a
		// node-reported completion must carry a non-empty assignment that equals the reporting node. An empty
		// assignment is not a "nodeless" allowance — it is an unbound wildcard, so reject it (fail closed); a
		// genuinely internal/gateway completion would need its own non-node ingress.
		if assignedNode == "" || assignedNode != nodeID {
			logger.WithFields(fields).WithField("assigned_node", assignedNode).
				Warn("Ignoring completion whose reporting node does not match the assigned processing node")
			return
		}

		// Artifact terminal state (only when there's a produced output); captured for the
		// post-commit side effects.
		outputPath := result.GetOutputPath()
		var (
			haveArtifact                                   bool
			artifactHash, artifactType, tenantID, streamID string
			streamInternalName, newFormat                  string
			sizeBytes, actualDurationMs                    int64
			partial                                        bool
		)
		if outputPath != "" {
			var oldS3URL, oldFormat string
			var requestedStartUnix, requestedStopUnix int64
			// Lock the artifact row too. A lookup failure must NOT acknowledge the job — return
			// (rollback) so it retries.
			artifact, lookupErr := q.LockProcessingArtifactForCompletion(ctx, result.GetJobId())
			if lookupErr != nil {
				// A GENUINELY missing artifact row (ErrNoRows: the row was hard-deleted, or the
				// job points at a hash with no artifact) must NOT be acknowledged as completed —
				// roll back so stale recovery retries, and a permanently-gone artifact bounds out
				// via max-retries → failed instead of a false "completed". This is distinct from a
				// row that EXISTS in a terminal/deleted state (soft-deleted mid-processing), which
				// the readiness UPDATE's affected==0 guard below handles by completing without
				// publication.
				if errors.Is(lookupErr, sql.ErrNoRows) {
					logger.WithFields(fields).Warn("Completion for a job whose artifact row is missing; rolling back to retry")
					return
				}
				logger.WithError(lookupErr).WithFields(fields).Error("Failed to look up artifact for completion; will retry")
				return
			}
			artifactHash, artifactType, tenantID, streamID, streamInternalName = artifact.ArtifactHash, artifact.ArtifactType, artifact.TenantID, artifact.StreamID, artifact.StreamInternalName
			oldS3URL, oldFormat, requestedStartUnix, requestedStopUnix = artifact.S3Url, artifact.Format, artifact.RequestedStartUnix, artifact.RequestedStopUnix
			if artifactHash != "" {
				_ = oldS3URL
				sizeBytes = result.GetOutputSizeBytes()
				newFormat = strings.TrimPrefix(filepath.Ext(outputPath), ".")
				actualDurationMs = result.GetMediaDurationMs()
				// A best-effort source (live buffer shallower than the requested range) legitimately
				// yields a shorter clip: it publishes as partial rather than failing.
				requestedSpanMs := (requestedStopUnix - requestedStartUnix) * 1000
				partial = actualDurationMs > 0 && requestedSpanMs > 0 &&
					requestedSpanMs-actualDurationMs > clipPartialShortfallMs

				// Authoritative A/V track capture; tracks_present gates replace-vs-preserve.
				tracksPresent := result.GetTracksPresent()
				tracksJSON, tErr := marshalRecordingTracks(result.GetTracks())
				if tErr != nil {
					logger.WithError(tErr).WithField("artifact_hash", artifactHash).Warn("Failed to marshal processed artifact tracks; leaving existing summary")
					tracksPresent = false
					tracksJSON = "[]"
				}

				// Claim readiness on the transaction. A row deleted/failed mid-processing matches
				// 0 (guard) — the job still completes, but nothing is published.
				affected, dbErr := q.MarkProcessingArtifactReady(ctx, foghorndb.MarkProcessingArtifactReadyParams{
					Format: sql.NullString{String: newFormat, Valid: newFormat != ""}, ArtifactHash: artifactHash,
					SizeBytes: sql.NullInt64{Int64: sizeBytes, Valid: true}, DurationMs: actualDurationMs,
					Tracks: json.RawMessage(tracksJSON), TracksPresent: tracksPresent,
				})
				if dbErr != nil {
					logger.WithError(dbErr).WithField("artifact_hash", artifactHash).Error("Failed to update artifact readiness; will retry")
					return
				}
				if affected == 0 {
					logger.WithFields(fields).WithField("artifact_hash", artifactHash).Warn("Processed artifact no longer active (deleted/failed); completing job without publication")
				} else {
					haveArtifact = true

					// VOD metadata (codec/resolution/…) from Helmsman stream info — same tx.
					if artifactType == "vod" {
						o := result.GetOutputs()
						textArg := func(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }
						if mErr := q.UpdateCompletedVODMetadata(ctx, foghorndb.UpdateCompletedVODMetadataParams{
							ArtifactHash: artifactHash, DurationMs: textArg(o["duration_ms"]), Resolution: textArg(o["resolution"]),
							VideoCodec: textArg(o["video_codec"]), AudioCodec: textArg(o["audio_codec"]), BitrateKbps: textArg(o["bitrate_kbps"]),
							Width: textArg(o["width"]), Height: textArg(o["height"]), Fps: textArg(o["fps"]),
							AudioChannels: textArg(o["audio_channels"]), AudioSampleRate: textArg(o["audio_sample_rate"]),
						}); mErr != nil {
							logger.WithError(mErr).WithField("artifact_hash", artifactHash).Error("Failed to update vod_metadata; will retry")
							return
						}
					}
					// Origin placement + node-copy event — same tx. This node wrote the canonical file;
					// register as origin (is_complete=true) so it can serve peer-relay while it uploads.
					if err := RegisterOriginArtifactTx(ctx, completionTx, artifactHash, nodeID, outputPath, sizeBytes, true); err != nil {
						logger.WithError(err).WithField("artifact_hash", artifactHash).Error("Failed to register origin artifact; will retry")
						return
					}
					// Lifecycle enqueue in the SAME tx (durable outbox), regardless of Decklog conn.
					if artifactType == "clip" {
						if streamID == "" {
							streamID = resolveLifecycleStreamID(ctx, streamInternalName)
						}
						clipData := &ipcpb.ClipLifecycleData{
							Stage:    ipcpb.ClipLifecycleData_STAGE_DONE,
							ClipHash: artifactHash,
							ProgressPercent: func() *uint32 {
								p := uint32(100)
								return &p
							}(),
							FilePath:        &outputPath,
							SizeBytes:       func() *uint64 { s := uint64(sizeBytes); return &s }(),
							CompletedAt:     func() *int64 { t := time.Now().Unix(); return &t }(),
							NodeId:          &nodeID,
							StorageLocation: func() *string { v := "local"; return &v }(),
							SyncStatus:      func() *string { v := "pending"; return &v }(),
							HasLocalCopy:    func() *bool { v := true; return &v }(),
							IsSynced:        func() *bool { v := false; return &v }(),
							IsFinalized:     func() *bool { v := false; return &v }(),
						}
						if tenantID != "" {
							clipData.TenantId = &tenantID
						}
						if streamID != "" {
							clipData.StreamId = &streamID
						}
						if streamInternalName != "" {
							clipData.StreamInternalName = &streamInternalName
						}
						if actualDurationMs > 0 {
							durationSec := actualDurationMs / 1000
							clipData.DurationSec = &durationSec
						}
						if sp, wallMs := processingSpeedFromOutputs(result.GetOutputs()); sp != nil || wallMs != nil {
							clipData.ProcessingSpeed = sp
							clipData.ProcessingWallMs = wallMs
						}
						if err := artifactoutbox.EnqueueClipLifecycleTx(ctx, completionTx, clipData); err != nil {
							logger.WithError(err).WithField("artifact_hash", artifactHash).Error("Failed to enqueue clip lifecycle; will retry")
							return
						}
					}
					if artifactType == "vod" {
						vodData := &ipcpb.VodLifecycleData{
							Status:          ipcpb.VodLifecycleData_STATUS_COMPLETED,
							VodHash:         artifactHash,
							FilePath:        &outputPath,
							SizeBytes:       func() *uint64 { s := uint64(sizeBytes); return &s }(),
							CompletedAt:     func() *int64 { t := time.Now().Unix(); return &t }(),
							NodeId:          &nodeID,
							ProgressPct:     func() *int32 { p := int32(100); return &p }(),
							StorageLocation: func() *string { v := "local"; return &v }(),
							SyncStatus:      func() *string { v := "pending"; return &v }(),
							HasLocalCopy:    func() *bool { v := true; return &v }(),
							IsSynced:        func() *bool { v := false; return &v }(),
							IsFinalized:     func() *bool { v := false; return &v }(),
						}
						if tenantID != "" {
							vodData.TenantId = &tenantID
						}
						if sp, wallMs := processingSpeedFromOutputs(result.GetOutputs()); sp != nil || wallMs != nil {
							vodData.ProcessingSpeed = sp
							vodData.ProcessingWallMs = wallMs
						}
						if err := artifactoutbox.EnqueueVodLifecycleTx(ctx, completionTx, vodData); err != nil {
							logger.WithError(err).WithField("artifact_hash", artifactHash).Error("Failed to enqueue vod lifecycle; will retry")
							return
						}
					}
					_ = oldFormat
				} // end else (artifact published)
			} // end if artifactHash != ""
		} // end if outputPath != ""

		// Mark the job completed LAST, then commit the whole terminal transition atomically.
		var outputMetadata sql.NullString
		if outputMeta != nil {
			outputMetadata = sql.NullString{String: *outputMeta, Valid: true}
		}
		if err := q.CompleteProcessingJob(ctx, foghorndb.CompleteProcessingJobParams{JobID: result.GetJobId(), OutputMetadata: outputMetadata}); err != nil {
			logger.WithError(err).WithFields(fields).Error("Failed to mark job completed; will retry")
			return
		}
		if err := completionTx.Commit(); err != nil {
			logger.WithError(err).WithFields(fields).Error("Failed to commit completion; will retry")
			return
		}
		committed = true
		logger.WithFields(fields).Info("Processing job completed")

		// After commit: in-memory state + reconciler wake (best-effort, not part of durability).
		if haveArtifact {
			state.DefaultManager().AddNodeArtifact(nodeID, &ipcpb.StoredArtifact{
				ClipHash:   artifactHash,
				FilePath:   outputPath,
				SizeBytes:  uint64(sizeBytes),
				CreatedAt:  time.Now().Unix(),
				Format:     newFormat,
				Role:       ipcpb.StoredArtifact_ROLE_ORIGIN,
				IsComplete: true,
			})
			if partial {
				logger.WithFields(logging.Fields{"artifact_hash": artifactHash, "actual_duration_ms": actualDurationMs}).Warn("Clip published partial: source covered less than the requested range")
			}
			NotifyArtifactMapUpdated(nodeID)
		}

	case "failed":
		failProcessingJobAtomic(ctx, result.GetJobId(), result.GetError(), nodeID, logger, fields)

	default:
		logger.WithFields(fields).Warn("Unknown processing job result status")
		return
	}
}

// failProcessingJobAtomic drives a processing job to its terminal failed state as ONE
// transaction: the job is locked FOR UPDATE (so a cancelled/deleted/duplicate job is a
// no-op — no resurrection), marked failed, and — for clip/vod artifacts — the artifact is
// flipped to failed and the failure lifecycle is enqueued on the same tx. Either everything
// commits or nothing does, so a failed job is never left with an artifact still "processing"
// (the split-write hazard the pre-transaction path carried). A transient error rolls back and
// leaves the job active for stale recovery to retry.
func failProcessingJobAtomic(ctx context.Context, jobID, errMsg, reportingNode string, logger logging.Logger, fields logging.Fields) {
	tx, txErr := db.BeginTx(ctx, nil)
	if txErr != nil {
		logger.WithError(txErr).WithFields(fields).Error("Failed to begin failure transaction")
		return
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback() //nolint:errcheck // best-effort rollback of an uncommitted tx
		}
	}()

	// Lock the job and resolve its artifact in one shot. LEFT JOIN so a job with no artifact
	// row still returns its status; FOR UPDATE OF pj serializes against the completion path.
	q := foghorndb.New(tx)
	job, lookupErr := q.LockProcessingJobForFailure(ctx, jobID)
	if lookupErr != nil {
		if errors.Is(lookupErr, sql.ErrNoRows) {
			logger.WithFields(fields).Warn("Failure for unknown processing job; ignoring")
			return
		}
		logger.WithError(lookupErr).WithFields(fields).Error("Failed to lock processing job for failure; will retry")
		return
	}
	jobStatusNow, assignedNode, artHash, artType := job.Status.String, job.ProcessingNodeID, job.ArtifactHash, job.ArtifactType
	tenantID, streamID, streamInternalName := job.TenantID, job.StreamID, job.StreamInternalName
	if jobStatusNow != "dispatched" && jobStatusNow != "processing" {
		logger.WithFields(fields).Warn("Ignoring failure for a non-active processing job (cancelled, deleted, or duplicate)")
		return
	}
	// Bind the failure to the assigned node so a foreign node cannot fail another node's job. Both callers
	// (the "failed" report and the internal empty-output→fail) pass the reporting connection's node id, and
	// dispatch persists processing_node_id for every node-dispatched job, so require a non-empty assignment
	// that equals the reporting node. An empty assignment is an unbound wildcard — reject it (fail closed).
	if reportingNode == "" || assignedNode == "" || assignedNode != reportingNode {
		logger.WithFields(fields).WithField("assigned_node", assignedNode).
			Warn("Ignoring failure whose reporting node does not match the assigned processing node")
		return
	}

	if err := q.MarkProcessingJobFailed(ctx, foghorndb.MarkProcessingJobFailedParams{JobID: jobID, ErrorMessage: sql.NullString{String: errMsg, Valid: true}}); err != nil {
		logger.WithError(err).WithFields(fields).Error("Failed to mark job failed; will retry")
		return
	}

	// Mark the artifact failed (clip AND vod) on the same tx. Without this a failed VOD/clip
	// stays "processing" in the UI forever. The guard only touches a PRE-TERMINAL artifact and is
	// tenant-scoped: a concurrently deleted/expired/aborted/already-ready artifact (or a
	// hash-collision across tenants) must never be resurrected to 'failed'.
	if artHash != "" && (artType == "clip" || artType == "vod") {
		artFailed, err := q.MarkProcessingArtifactFailed(ctx, foghorndb.MarkProcessingArtifactFailedParams{
			ArtifactHash: artHash, ErrorMessage: sql.NullString{String: errMsg, Valid: true}, TenantID: tenantID,
		})
		if err != nil {
			logger.WithError(err).WithField("artifact_hash", artHash).Error("Failed to mark artifact failed; will retry")
			return
		}
		// Only emit FAILED if THIS tx actually transitioned the artifact; a 0-row result means it
		// was already terminal (concurrently ready/deleted/expired/aborted) and a false FAILED
		// analytics event must not be emitted. The job-failed transition still commits.
		if artFailed == 0 {
			// nothing to publish
		} else if artType == "clip" {
			// Resolve the stream id when the artifact row lacks it: periscope-ingest drops
			// lifecycle events without a valid stream UUID — how a failed clip stays
			// "processing" in the UI forever.
			if streamID == "" {
				streamID = resolveLifecycleStreamID(ctx, streamInternalName)
			}
			clipData := &ipcpb.ClipLifecycleData{
				Stage:    ipcpb.ClipLifecycleData_STAGE_FAILED,
				ClipHash: artHash,
				Error:    &errMsg,
			}
			if tenantID != "" {
				clipData.TenantId = &tenantID
			}
			if streamID != "" {
				clipData.StreamId = &streamID
			}
			if streamInternalName != "" {
				clipData.StreamInternalName = &streamInternalName
			}
			if err := artifactoutbox.EnqueueClipLifecycleTx(ctx, tx, clipData); err != nil {
				logger.WithError(err).WithField("artifact_hash", artHash).Error("Failed to enqueue clip failure lifecycle; will retry")
				return
			}
		} else {
			vodData := &ipcpb.VodLifecycleData{
				Status:  ipcpb.VodLifecycleData_STATUS_FAILED,
				VodHash: artHash,
				Error:   &errMsg,
			}
			if tenantID != "" {
				vodData.TenantId = &tenantID
			}
			if err := artifactoutbox.EnqueueVodLifecycleTx(ctx, tx, vodData); err != nil {
				logger.WithError(err).WithField("artifact_hash", artHash).Error("Failed to enqueue vod failure lifecycle; will retry")
				return
			}
		}
	}

	if err := tx.Commit(); err != nil {
		logger.WithError(err).WithFields(fields).Error("Failed to commit failure; will retry")
		return
	}
	committed = true
	logger.WithFields(fields).WithField("error", errMsg).Warn("Processing job failed")
}

// processProcessingJobProgress handles periodic progress updates from Helmsman.
// Refreshes updated_at (preventing stale recovery) and emits lifecycle events.
func processProcessingJobProgress(progress *ipcpb.ProcessingJobProgress, nodeID string, logger logging.Logger) {
	if db == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	progressPct := clampProgressPct(progress.GetProgressPct())
	// Chapter jobs deliberately use an attempt-fenced non-UUID identity and do
	// not have a processing_jobs row. Route them before the generic UUID query;
	// PostgreSQL would reject the chapter job ID as invalid UUID rather than
	// returning sql.ErrNoRows, which would otherwise drop the liveness heartbeat.
	if chapterID, attempt, ok := chapterFinalizeIdentityFromJobID(progress.GetJobId()); ok {
		processChapterFinalizeProgress(ctx, chapterID, nodeID, attempt, progressPct, logger)
		return
	}

	// Update job progress and refresh updated_at so stale recovery doesn't requeue. Bind STRICTLY to the
	// assigned node so a foreign node cannot refresh another node's job (which would defeat stale recovery on a
	// genuinely stuck node). processing_node_id is persisted before the node can report, so an active
	// node-dispatched job always carries it; require an exact match (a NULL assignment is an unbound wildcard).
	q := foghorndb.New(db)
	var artifactType, streamID, streamInternalName string
	updated, err := q.UpdateProcessingJobProgress(ctx, foghorndb.UpdateProcessingJobProgressParams{
		JobID: progress.GetJobId(), Progress: sql.NullInt32{Int32: progressPct, Valid: true}, ProcessingNodeID: sql.NullString{String: nodeID, Valid: true},
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			logger.WithFields(logging.Fields{
				"job_id":  progress.GetJobId(),
				"node_id": nodeID,
			}).Debug("Ignored processing progress from a non-owner or terminal job")
		} else {
			logger.WithError(err).WithField("job_id", progress.GetJobId()).Warn("Failed to update processing job progress")
		}
		return
	}
	artifactHash, tenantID := updated.ArtifactHash, updated.TenantID
	if updated.Progress.Valid {
		progressPct = clampProgressPct(updated.Progress.Int32)
	}

	if artifactHash.Valid {
		lifecycle, typeErr := q.GetProcessingArtifactLifecycle(ctx, artifactHash.String)
		if typeErr == nil {
			artifactType, streamID, streamInternalName = lifecycle.ArtifactType, lifecycle.StreamID, lifecycle.StreamInternalName
		}
		if typeErr != nil && !errors.Is(typeErr, sql.ErrNoRows) {
			logger.WithError(typeErr).WithField("artifact_hash", artifactHash.String).Warn("Failed to look up processing artifact type")
		}
	}

	// This is a best-effort PROGRESS SAMPLE, not a state transition — the artifact's
	// 'processing' transition already committed atomically at dispatch. A dropped sample
	// only loses one progress tick, so it stays best-effort. Capture does not depend on a
	// live decklog client: the Enqueue*Logged helpers write the outbox row unconditionally
	// (a queued row is delivered once decklog reconnects) and log — rather than swallow — an
	// enqueue failure.
	if artifactHash.Valid {
		if artifactType == "clip" {
			clipData := &ipcpb.ClipLifecycleData{
				Stage:           ipcpb.ClipLifecycleData_STAGE_PROGRESS,
				ClipHash:        artifactHash.String,
				ProgressPercent: func() *uint32 { p := uint32(progressPct); return &p }(),
			}
			if tenantID != "" {
				clipData.TenantId = &tenantID
			}
			if streamID != "" {
				clipData.StreamId = &streamID
			}
			if streamInternalName != "" {
				clipData.StreamInternalName = &streamInternalName
			}
			artifactoutbox.EnqueueClipLifecycleLogged(clipData)
			return
		}

		// Emit VodLifecycleData with progress for VOD processing. DVR chapter
		// finalization has its own path above because chapter jobs are not in
		// foghorn.processing_jobs.
		vodData := &ipcpb.VodLifecycleData{
			Status:      ipcpb.VodLifecycleData_STATUS_PROCESSING,
			VodHash:     artifactHash.String,
			ProgressPct: &progressPct,
		}
		if tenantID != "" {
			vodData.TenantId = &tenantID
		}
		artifactoutbox.EnqueueVodLifecycleLogged(vodData)
	}
}

func clampProgressPct(progress int32) int32 {
	return max(0, min(progress, 100))
}

func processChapterFinalizeProgress(ctx context.Context, chapterID, nodeID string, expectedAttempt, progressPct int32, logger logging.Logger) {
	// The node and attempt together own the lease. A report from a foreign node,
	// a replaced attempt, or an attempt-less legacy job matches no row.
	row, err := foghorndb.New(db).UpdateChapterFinalizeProgress(ctx, foghorndb.UpdateChapterFinalizeProgressParams{
		ChapterID: chapterID, FinalizeNodeID: sql.NullString{String: nodeID, Valid: true}, ExpectedAttempt: expectedAttempt,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			logger.WithFields(logging.Fields{
				"chapter_id": chapterID,
				"node_id":    nodeID,
				"attempt":    expectedAttempt,
			}).Debug("Ignored chapter progress from a non-owner or stale attempt")
		} else {
			logger.WithError(err).WithField("chapter_id", chapterID).Warn("Failed to update chapter finalize progress")
		}
		return
	}
	artifactHash, tenantID := row.PlaybackArtifactHash.String, row.ATenantID
	vodData := &ipcpb.VodLifecycleData{
		Status:      ipcpb.VodLifecycleData_STATUS_PROCESSING,
		VodHash:     artifactHash,
		TenantId:    &tenantID,
		ProgressPct: &progressPct,
	}
	// Best-effort progress sample. EnqueueVodLifecycleLogged already writes the durable outbox row
	// synchronously and logs (not swallows) failure, so no goroutine wrapper is needed — a bare `go`
	// here only risks losing the write on shutdown.
	artifactoutbox.EnqueueVodLifecycleLogged(vodData)
}

func SendLocalProcessingJob(nodeID string, req *ipcpb.ProcessingJobRequest) error {
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	// FENCE dispatch to THIS connection generation (same discipline as SendLocalFreezeRequest): hold sendGate
	// across the superseded check AND the Send so a reconnect that retired this connection can't interleave —
	// otherwise a processing job could be delivered to a retired connection and run twice. Fail closed with a
	// non-relayable error on supersede.
	c.sendGate.Lock()
	defer c.sendGate.Unlock()
	if c.superseded.Load() || !connIsCurrentlyRegistered(nodeID, c) {
		return ErrConnSuperseded
	}
	msg := &ipcpb.ControlMessage{
		Payload: &ipcpb.ControlMessage_ProcessingJobRequest{ProcessingJobRequest: req},
		SentAt:  timestamppb.Now(),
	}
	return c.stream.Send(msg)
}

// SendProcessingJob sends a ProcessingJobRequest to the given node, relaying via HA if needed.
func SendProcessingJob(nodeID string, req *ipcpb.ProcessingJobRequest) error {
	err := SendLocalProcessingJob(nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	return relayFailure(err, commandRelay.forward(context.Background(), &foghornrelaypb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &foghornrelaypb.ForwardCommandRequest_ProcessingJob{ProcessingJob: req},
	}))
}

// GeneratePresignedGETForArtifact generates a presigned GET URL for an artifact's S3 object.
// The s3URL should be the full S3 URL (s3://bucket/key) stored in foghorn.artifacts.
func GeneratePresignedGETForArtifact(_ context.Context, s3URL string) (string, error) {
	if s3Client == nil {
		return "", fmt.Errorf("s3 client not configured")
	}
	key := s3URL
	if strings.HasPrefix(s3URL, "s3://") {
		// FAIL CLOSED for a FOREIGN bucket: this mints a LOCAL presigned URL, so signing a federation-adopted
		// remote object's s3_url here would hand out a URL against the wrong backend. ParseLocalS3URL errors
		// unless the URL is under this cell's bucket; callers (relay resolve, processing dispatch) then route to
		// federation / revert, never presign a remote object locally. (Cross-provider input is the RFC.)
		parsed, err := s3Client.ParseLocalS3URL(s3URL)
		if err != nil {
			return "", err
		}
		key = parsed
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

	// Resolve the OWNER tenant through the identity facade (the sanctioned hash→tenant authority) so the
	// artifact lookup below is tenant-scoped (repository invariant), rather than reading by hash alone.
	tenantID := ""
	if resolver := identity.Default(); resolver != nil {
		if id, rErr := resolver.ResolveArtifact(ctx, assetHash, assetType); rErr == nil {
			tenantID = strings.TrimSpace(id.TenantID)
		}
	}
	if tenantID == "" {
		logger.Error("Could not resolve tenant for dtsh sync")
		return
	}

	syncObjectKey, err := foghorndb.New(db).GetArtifactSyncObjectKey(ctx, foghorndb.GetArtifactSyncObjectKeyParams{ArtifactHash: assetHash, TenantID: tenantID})
	if err != nil {
		logger.WithError(err).Error("Failed to lookup asset for dtsh sync")
		return
	}

	expiry := 30 * time.Minute
	expirySeconds := int64(expiry.Seconds())
	requestID := fmt.Sprintf("dtsh-%s-%d", assetHash, time.Now().UnixNano())

	req := &ipcpb.DtshSyncRequest{
		RequestId:        requestID,
		AssetType:        assetType,
		AssetHash:        assetHash,
		LocalPath:        filePath,
		UrlExpirySeconds: expirySeconds,
	}

	switch assetType {
	case "clip":
		// The .dtsh sidecar's canonical key is derived from the persisted descriptor (sync_object_key +
		// ".dtsh"), never the node path. Like the main object, the presigned PUT targets an attempt-scoped
		// STAGING key — completion HEAD-verifies it and PROMOTES it to the canonical .dtsh key, so the node
		// never holds a canonical .dtsh PUT and cannot overwrite/enlarge the index after verification.
		if !syncObjectKey.Valid || syncObjectKey.String == "" {
			logger.Error("Cannot dispatch clip .dtsh sync: no persisted sync_object_key descriptor")
			return
		}
		presignedURL, err := s3Client.GeneratePresignedPUT(FreezeStagingKey(syncObjectKey.String+".dtsh", requestID), expiry)
		if err != nil {
			logger.WithError(err).Error("Failed to generate presigned URL for clip .dtsh")
			return
		}
		req.PresignedPutUrl = presignedURL
	case "dvr":
		// Whole-DVR .dtsh sync is NOT dispatched: DVR whole-freeze is unsupported, and a multi-object DVR
		// index has no single descriptor to stage+verify — issuing canonical PUT URLs would hand the node an
		// unverifiable overwrite window. DVR playback uses per-chapter VOD artifacts, whose .dtsh IS staged
		// and verified via the VOD path. (Whole-DVR index staging is docs/rfcs/cross-cluster-durable-replication-v1.md scope.)
		logger.Debug("Skipping whole-DVR .dtsh sync (unsupported; chapters sync via the staged VOD path)")
		return
	case "vod":
		// VOD .dtsh, like clip: the presigned PUT targets an attempt-scoped STAGING key derived from the
		// persisted descriptor (sync_object_key + ".dtsh"), never the canonical key or the node filePath —
		// completion HEAD-verifies + promotes it, so the node cannot overwrite/enlarge the index afterward.
		if !syncObjectKey.Valid || syncObjectKey.String == "" {
			logger.Error("Cannot dispatch vod .dtsh sync: no persisted sync_object_key descriptor")
			return
		}
		presignedURL, err := s3Client.GeneratePresignedPUT(FreezeStagingKey(syncObjectKey.String+".dtsh", requestID), expiry)
		if err != nil {
			logger.WithError(err).Error("Failed to generate presigned URL for VOD .dtsh")
			return
		}
		req.PresignedPutUrl = presignedURL
	default:
		// Only clip and vod have a single staged+verified .dtsh index. Any other type (DVR is handled above;
		// anything else is unexpected) must NOT claim a dtsh attempt or dispatch a request with an empty URL.
		logger.Warn("Skipping .dtsh sync: unsupported asset_type")
		return
	}

	// Record the attempt BEFORE dispatch so the completion (success OR failure) is authenticated
	// against this exact request/node. An unclaimable row (index already synced, a live attempt on
	// another node that isn't yet stale, or a recent failure still within backoff) means we must NOT
	// dispatch a competing upload.
	if claimed, cErr := claimDtshAttempt(ctx, assetHash, requestID, nodeID, tenantID); cErr != nil || !claimed {
		logger.WithError(cErr).WithField("request_id", requestID).
			Debug("Skipping .dtsh sync: attempt not claimable (already synced or owned by a live attempt)")
		return
	}

	if err := SendDtshSyncRequest(nodeID, req); err != nil {
		logger.WithError(err).Error("Failed to send DtshSyncRequest")
		// Undo the claim (bounded ctx) so the next node report re-triggers. On a settlement failure the row
		// stays dtsh in_progress and the >10min stale re-claim in claimDtshAttempt enqueues its .dtsh staging.
		clearCtx, clearCancel := context.WithTimeout(context.Background(), 10*time.Second)
		clearDtshAttempt(clearCtx, assetHash, requestID, nodeID, tenantID)
		clearCancel()
		return
	}

	logger.Info("Sent DtshSyncRequest for incremental .dtsh sync")
}

// Default storage base path when node has no StorageLocal configured.
// Matches HELMSMAN_STORAGE_LOCAL_PATH's native default for consistent path
// reconstruction (containers use /data/storage but always report
// StorageLocal). Override with FOGHORN_DEFAULT_STORAGE_BASE for fleets on a
// legacy layout.
var defaultStorageBase = "/var/lib/frameworks/edge-storage"

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

// artifactRepo provides database access for dual-storage sync tracking.
var artifactRepo state.ArtifactRepository

// SetArtifactRepository sets the artifact repository for sync tracking.
func SetArtifactRepository(repo state.ArtifactRepository) {
	artifactRepo = repo
}

// DeleteNodeArtifact removes a node's local-copy row and emits a LOST placement in
// one transaction (via the repository), so explicit deletion/eviction updates the
// analytics projection.
func DeleteNodeArtifact(ctx context.Context, artifactHash, nodeID string, nodeClockDeletedAtMs int64) (state.NodeArtifactDeletionOutcome, error) {
	if artifactRepo == nil {
		return state.NodeArtifactDeletionParentMissing, nil
	}
	return artifactRepo.DeleteNodeArtifact(ctx, artifactHash, nodeID, nodeClockDeletedAtMs)
}

// ReconcileNodeCopies seeds never-emitted present copies and sweeps stale-present rows
// so the analytics projection is seeded on boot and heals non-emitting writes. No-op if
// the repository is unset. Returns the number of copies emitted.
func ReconcileNodeCopies(ctx context.Context) (int, error) {
	if artifactRepo == nil {
		return 0, nil
	}
	return artifactRepo.ReconcileNodeCopies(ctx)
}

// RefreshNodeCopy synchronously emits GAINED for a present copy whose analytics
// projection is absent — called by writers that restore/create presence outside the
// emitting paths so the transition lands immediately. No-op if the repository is unset.
func RefreshNodeCopy(ctx context.Context, artifactHash, nodeID string) error {
	if artifactRepo == nil {
		return nil
	}
	return artifactRepo.RefreshNodeCopy(ctx, artifactHash, nodeID)
}

// RegisterDVRRecordingOrigin registers the DVR recording node as origin (with base_url)
// through the atomic transition path. No-op if the repository is unset.
func RegisterDVRRecordingOrigin(ctx context.Context, artifactHash, nodeID, baseURL string) error {
	if artifactRepo == nil {
		return nil
	}
	return artifactRepo.RegisterDVRRecordingOrigin(ctx, artifactHash, nodeID, baseURL)
}

// GetRelayBaseURL returns the URL Mist on the given node uses to reach
// Helmsman's /internal/artifact/* relay. Captured at Register time from the
// node's HELMSMAN_RELAY_BASE_URL env var. Returns "" when the node has not
// connected or did not advertise a relay URL — callers must treat this as
// "cannot route through relay, abort STREAM_SOURCE" rather than fabricating
// 127.0.0.1, which is wrong wherever Mist and Helmsman do not share loopback
// (the dev compose bridge runs them as separate containers).
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
func processCanDeleteRequest(req *ipcpb.CanDeleteRequest, nodeID string, stream ipcpb.HelmsmanControl_ConnectServer, logger logging.Logger) {
	assetHash := req.GetAssetHash()
	requestingNodeID := req.GetNodeId()
	if requestingNodeID == "" {
		requestingNodeID = nodeID
	}

	logger.WithFields(logging.Fields{
		"asset_hash": assetHash,
		"node_id":    requestingNodeID,
	}).Info("Processing can-delete request")

	response := &ipcpb.CanDeleteResponse{
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

	ctx := context.Background()
	info, err := artifactRepo.GetArtifactSyncInfo(ctx, assetHash)
	if err != nil {
		logger.WithError(err).WithField("asset_hash", assetHash).Error("Failed to get sync status")
		response.Reason = "db_error"
		sendCanDeleteResponse(stream, response, logger)
		return
	}

	terminal := info != nil && (info.LifecycleStatus == "deleted" || info.LifecycleStatus == "expired" || info.LifecycleStatus == "aborted")
	if terminal {
		response.SafeToDelete = true
		response.Reason = "catalog_terminal"
		logger.WithFields(logging.Fields{
			"asset_hash": assetHash,
			"status":     info.LifecycleStatus,
		}).Info("Terminal catalog artifact is safe to remove locally")
	} else if durable, reason := verifyDurableArtifactCopy(ctx, info, logger); durable {
		response.SafeToDelete = true
		response.Reason = reason

		// Calculate warm duration (how long asset was cached before eviction)
		cachedAt, err := artifactRepo.GetCachedAt(ctx, assetHash)
		if err == nil && cachedAt > 0 {
			warmDurationMs := time.Now().UnixMilli() - cachedAt
			response.WarmDurationMs = warmDurationMs
			logger.WithFields(logging.Fields{
				"asset_hash":       assetHash,
				"warm_duration_ms": warmDurationMs,
			}).Info("Asset durable copy verified, safe to delete local copy")
		} else {
			logger.WithField("asset_hash", assetHash).Info("Asset durable copy verified, safe to delete local copy (no cached_at)")
		}
	} else {
		// No local durability proof. A remote storage/origin cluster attribution is NOT durability proof:
		// storage_cluster_id/origin_cluster_id only record WHERE the bytes would live, not that a durable
		// object exists — a pending/failed artifact carries a cluster id but no synced object. Fail CLOSED
		// (SafeToDelete stays false) and report the real sync state.

		// Check if sync is in progress
		if info == nil {
			response.Reason = "not_found"
		} else if info.SyncStatus == "in_progress" {
			response.Reason = "sync_pending"
		} else if info.SyncStatus == "failed" {
			response.Reason = "sync_failed"
		} else if info.SyncStatus == "synced" {
			_, response.Reason = verifyDurableArtifactCopy(ctx, info, logger)
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

func verifyDurableArtifactCopy(ctx context.Context, info *state.ArtifactSyncInfo, logger logging.Logger) (bool, string) {
	if info == nil {
		return false, "not_found"
	}
	if info.SyncStatus != "synced" {
		return false, "not_synced"
	}
	if info.S3URL == "" {
		return false, "synced_missing_s3_url"
	}
	if s3Client == nil {
		return false, "s3_not_configured"
	}
	key, err := s3Client.ParseS3URL(info.S3URL)
	if err != nil || key == "" {
		if logger != nil && err != nil {
			logger.WithError(err).WithField("asset_hash", info.ArtifactHash).Warn("Failed to parse synced artifact S3 URL")
		}
		return false, "s3_url_invalid"
	}
	keys, err := s3Client.ListPrefix(ctx, key)
	if err != nil {
		if logger != nil {
			logger.WithError(err).WithFields(logging.Fields{
				"asset_hash": info.ArtifactHash,
				"s3_key":     key,
			}).Warn("Failed to verify synced artifact in S3")
		}
		return false, "s3_verify_failed"
	}
	for _, got := range keys {
		if got == key {
			return true, "synced_verified"
		}
	}
	if logger != nil {
		logger.WithFields(logging.Fields{
			"asset_hash": info.ArtifactHash,
			"s3_key":     key,
		}).Warn("Artifact marked synced but durable object is missing")
	}
	return false, "s3_object_missing"
}

// sendCanDeleteResponse sends a CanDeleteResponse back to Helmsman
func sendCanDeleteResponse(stream ipcpb.HelmsmanControl_ConnectServer, response *ipcpb.CanDeleteResponse, logger logging.Logger) {
	msg := &ipcpb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &ipcpb.ControlMessage_CanDeleteResponse{CanDeleteResponse: response},
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
func processSyncComplete(complete *ipcpb.SyncComplete, nodeID string, logger logging.Logger) {
	processSyncCompleteAt(complete, nodeID, 0, logger)
}

func processSyncCompleteAt(complete *ipcpb.SyncComplete, nodeID string, nodeClockCompletedAtMs int64, logger logging.Logger) {
	requestID := complete.GetRequestId()
	assetHash := complete.GetAssetHash()
	status := complete.GetStatus()
	// s3_url is never trusted from the node — the canonical URL is derived server-side from the persisted
	// descriptor below, so this starts empty. The reporting node is the authenticated connection (the
	// payload no longer carries a node_id), so a node can never attribute this sync to a different node.
	s3URL := ""
	sizeBytes := complete.GetSizeBytes()
	errorMsg := complete.GetError()
	reportingNodeID := nodeID

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

	// Bounded: the completion does S3 HEAD + a promote (server-side copy) and a short DB transaction. An
	// unbounded context would let a stalled S3 call hold the transaction open indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dtshIncluded := complete.GetDtshIncluded()

	// Resolve the artifact OWNER tenant through the identity facade (the sanctioned hash→tenant authority)
	// so EVERY completion query below is tenant-scoped, per the repository invariant. A completion that
	// cannot be attributed to an owner tenant is refused (the tenant-scoped guards match zero rows).
	ownerTenant := ""
	if resolver := identity.Default(); resolver != nil {
		if id, rErr := resolver.ResolveArtifact(ctx, assetHash, ""); rErr == nil {
			ownerTenant = strings.TrimSpace(id.TenantID)
		}
	}

	switch status {
	case "success":
		incArtifactSyncOutcome("success")
		var artifactType, internalName, format, tenantID, streamID, syncObjectKey, syncStatusRow, previousActiveKey, previousActiveDtshKey, previousS3URL, previousDtshReq string
		// The identity read is TENANT-SCOPED (ownerTenant, resolved via the identity facade) AND scoped to the
		// SERVER-ASSIGNED attempt for THIS node: the row matches only when the owner tenant ($2) matches AND
		// the echoed request id ($3, = FreezePermissionResponse.attempt_id) + authenticated connection ($4)
		// equal the persisted MAIN or DTSH attempt identity. This satisfies the tenant-scoping invariant and
		// yields tenantID="" (→ downstream no-op) for a stale / duplicate / wrong-node / wrong-tenant
		// completion. A genuine read error refuses (fail closed).
		if db != nil {
			attempt, idErr := foghorndb.New(db).GetSyncCompletionAttempt(ctx, foghorndb.GetSyncCompletionAttemptParams{
				ArtifactHash: assetHash, TenantID: ownerTenant, SyncRequestID: sql.NullString{String: requestID, Valid: true}, SyncNodeID: sql.NullString{String: reportingNodeID, Valid: true},
			})
			if idErr == nil {
				artifactType, internalName, format, tenantID, streamID = attempt.ArtifactType, attempt.StreamInternalName, attempt.Format, attempt.TenantID, attempt.StreamID
				syncObjectKey, syncStatusRow, previousActiveKey, previousActiveDtshKey, previousS3URL, previousDtshReq = attempt.SyncObjectKey, attempt.SyncStatus, attempt.ActiveObjectKey, attempt.ActiveDtshKey, attempt.S3Url, attempt.DtshSyncRequestID
			} else if !errors.Is(idErr, sql.ErrNoRows) {
				logger.WithError(idErr).WithField("asset_hash", assetHash).Error("sync completion: attempt-scoped identity pre-read failed; refusing to apply")
				return
			}
		}
		// LEGACY ROWS (synced before version-addressing) carry no active_object_key/active_dtsh_key: the durable
		// object lives at the key encoded in s3_url and any .dtsh co-located at <media>.dtsh. A non-empty s3_url on
		// a clip/vod means a prior durable object exists — recover its key (prefix-aware, via the live client) so a
		// re-publish SUPERSEDES and enqueues it (rather than orphaning it) and an incremental .dtsh has a media key
		// to gate/co-locate under. This is gated on s3_url, NOT sync_status: a re-upload resets the row to
		// in_progress, so the exact leak (re-uploaded legacy VOD's old object) surfaces with sync_status
		// 'in_progress'. A first freeze has an empty s3_url, so this never fabricates a bogus predecessor. The
		// postdeploy backfill fills active_object_key from vod_metadata for the common case; this closes the rest.
		if previousActiveKey == "" && previousS3URL != "" && (artifactType == "clip" || artifactType == "vod") && s3Client != nil {
			if k, perr := s3Client.ParseS3URL(previousS3URL); perr == nil && k != "" {
				previousActiveKey = k
				if previousActiveDtshKey == "" {
					previousActiveDtshKey = k + ".dtsh"
				}
			}
		}
		// A clip/vod in_progress attempt always carries the sync_object_key bound at claim time. Refuse a
		// descriptor-less clip/vod completion rather than promote an unverified reconstructed key: the row
		// stays in_progress for stale recovery to requeue with a bound descriptor.
		if (artifactType == "clip" || artifactType == "vod") && syncObjectKey == "" {
			logger.WithField("asset_hash", assetHash).Warn("Refusing descriptor-less clip/vod sync completion; row left for stale recovery to requeue with a bound descriptor")
			return
		}
		// SERVER-SIDE VERIFICATION + PUBLICATION, performed OUTSIDE the database transaction. A node's
		// "success" and reported size_bytes are SELF-ATTESTED. The node wrote to an attempt-scoped STAGING key;
		// Foghorn HEAD-verifies it and PROMOTES (conditional copy on the observed ETag) to a FRESH, IMMUTABLE
		// candidate key FreezePublishKey(sync_object_key, attempt) — a per-attempt key the node never held a
		// PUT for and that is NOT any currently-served object. Because publication targets a fresh key, this
		// copy can never overwrite live bytes, so it is safe to run it BEFORE (not inside) the transaction: the
		// transaction then only FLIPS the active_object_key pointer to the verified candidate. If the CAS is
		// lost or the commit fails, the candidate is simply an unreferenced orphan (enqueued for durable
		// cleanup) — a rollback can never expose it. A missing object / 0 bytes FAILS CLOSED; a transient error
		// is RETRYABLE. The recorded size is the PROVIDER-OBSERVED size. Only clip/vod carry a single object.
		stagingToCleanup := ""     // main staging; enqueued for durable cleanup at commit
		dtshStagingToCleanup := "" // .dtsh staging; same
		publishMainKey := ""       // the fresh candidate the main object was PUBLISHED to (empty ⇒ dtsh-only / DVR)
		publishDtshKey := ""       // the fresh, attempt-versioned key the .dtsh index was PUBLISHED to
		if s3Client != nil && syncObjectKey != "" && (artifactType == "clip" || artifactType == "vod") {
			if syncStatusRow == "in_progress" {
				stagingKey := FreezeStagingKey(syncObjectKey, requestID)
				exists, size, etag, hErr := s3Client.HeadObjectInfo(ctx, stagingKey)
				if hErr != nil {
					logger.WithError(hErr).WithFields(logging.Fields{"asset_hash": assetHash, "staging_key": stagingKey}).
						Warn("Sync completion staging HEAD failed (transient); leaving attempt in_progress for retry")
					return
				}
				if !exists {
					logger.WithFields(logging.Fields{"asset_hash": assetHash, "staging_key": stagingKey}).
						Warn("Refusing sync completion: no staged object (node reported success without a verified upload); leaving attempt for stale recovery")
					return
				}
				if size <= 0 {
					logger.WithFields(logging.Fields{"asset_hash": assetHash, "staging_key": stagingKey}).
						Warn("Refusing sync completion: staged object is empty (0 bytes); leaving attempt for stale recovery")
					return
				}
				if strings.TrimSpace(etag) == "" {
					logger.WithFields(logging.Fields{"asset_hash": assetHash, "staging_key": stagingKey}).
						Warn("Sync completion: staged object HEAD returned no ETag; leaving attempt in_progress for retry")
					return
				}
				candidate := FreezePublishKey(syncObjectKey, requestID)
				// Re-assert the main staging + candidate ledger rows (idempotent). The AUTHORITATIVE record was
				// written at claim time (RecordFreezePublicationLedgerTx), so a failure here is non-fatal — the
				// sweep still collects these objects from the claim-time rows; log and proceed.
				if lErr := RecordPublicationPairDB(ctx, db, assetHash, tenantID, requestID, stagingKey, candidate); lErr != nil {
					logger.WithError(lErr).WithField("asset_hash", assetHash).Debug("Sync completion: main publication ledger re-assert failed (non-fatal; claim-time record is authoritative)")
				}
				if pErr := s3Client.PromoteObject(ctx, stagingKey, candidate, etag); pErr != nil {
					// RETRYABLE (left in_progress). Do NOT enqueue the candidate: the attempt keeps the SAME
					// server-minted id, so a retry re-publishes to the SAME candidate key (idempotent copy) and
					// may make it ACTIVE — enqueuing it here would race the retry and could delete the live
					// object. A truly-abandoned attempt is collected by stale-freeze recovery (which clears the
					// identity first, then derives + enqueues this exact candidate) — no reuse hazard.
					logger.WithError(pErr).WithFields(logging.Fields{"asset_hash": assetHash, "staging_key": stagingKey, "candidate_key": candidate}).
						Warn("Sync completion publication failed (overwrite race or transient); leaving attempt in_progress for retry")
					return
				}
				publishMainKey = candidate
				stagingToCleanup = stagingKey
				// The provider-observed size is authoritative for durable state AND billing.
				sizeBytes = uint64(size)
			}
			// The .dtsh index CO-LOCATES with the current media object version (readers derive it as
			// <active_object_key>.dtsh). Publish the verified staged .dtsh to <mediaKey>.dtsh, where mediaKey is
			// the just-published main object (bundled) or the row's CURRENT active object (incremental .dtsh on
			// an already-synced row). If nothing verifiable is staged, downgrade (the incremental .dtsh retries).
			if dtshIncluded {
				mediaKey := publishMainKey
				if mediaKey == "" {
					mediaKey = previousActiveKey
				}
				stagingKey := FreezeStagingKey(syncObjectKey+".dtsh", requestID)
				dExists, dSize, dEtag, dErr := s3Client.HeadObjectInfo(ctx, stagingKey)
				if dErr != nil {
					logger.WithError(dErr).WithFields(logging.Fields{"asset_hash": assetHash, "staging_key": stagingKey}).
						Warn("Sync completion .dtsh staging HEAD failed (transient); leaving attempt in_progress for retry")
					return
				}
				if dExists {
					// The node UPLOADED a .dtsh staging object. Schedule it for cleanup NOW — regardless of whether
					// we finalize — so a downgrade (invalid/zero-byte size, a ledger-write failure, or a failed
					// promote) on a committing MAIN completion cannot leak it: the success tx enqueues
					// dtshStagingToCleanup. (On the incremental dtsh-only path a downgrade returns without a commit;
					// there the >10min stale-.dtsh re-claim collects this staging object.)
					dtshStagingToCleanup = stagingKey
				}
				if mediaKey == "" || !dExists || dSize <= 0 || strings.TrimSpace(dEtag) == "" {
					logger.WithFields(logging.Fields{"asset_hash": assetHash, "staging_key": stagingKey}).
						Warn("Not finalizing dtsh_synced: no verifiable staged .dtsh index (or no media object to co-locate under)")
					dtshIncluded = false
				} else {
					// mediaKey still GATES finalization (there must be a media object), but the .dtsh KEY is now
					// version-addressed by the attempt (not co-located at a fixed <media>.dtsh).
					dcand := FreezePublishDtshKey(syncObjectKey, requestID)
					// Record the .dtsh staging + candidate ledger rows (idempotent). For a BUNDLED attempt these
					// re-assert rows already written at claim time (RecordFreezePublicationLedgerTx); for an
					// INCREMENTAL .dtsh-only attempt (claimDtshAttempt, which does NOT write the ledger at claim
					// time) this insertion IS the durability source for the .dtsh candidate. Either way, if it fails
					// we must NOT finalize dtsh_synced: without a durable ledger row a completion lost to a
					// concurrent duplicate could leak the promoted candidate — so downgrade and let the retry re-run.
					if lErr := RecordPublicationPairDB(ctx, db, assetHash, tenantID, requestID, stagingKey, dcand); lErr != nil {
						logger.WithError(lErr).WithField("asset_hash", assetHash).Warn("Sync completion: failed to record .dtsh publication ledger; not finalizing dtsh_synced")
						dtshIncluded = false
					} else if pErr := s3Client.PromoteObject(ctx, stagingKey, dcand, dEtag); pErr != nil {
						// Downgrade (retryable). The ledger row (recorded above) lets the sweep collect the candidate;
						// dtshStagingToCleanup (set above) enqueues the staging on a committing main completion.
						logger.WithError(pErr).WithFields(logging.Fields{"asset_hash": assetHash, "staging_key": stagingKey}).
							Warn("Not finalizing dtsh_synced: .dtsh publication failed (overwrite race or transient)")
						dtshIncluded = false
					} else {
						publishDtshKey = dcand
					}
				}
			}
		}

		// The canonical S3 URL is ALWAYS derived SERVER-SIDE (s3URL starts empty above), from the object we
		// just PUBLISHED (the fresh candidate) for a main upload; for the dtsh-only path (media unchanged) the
		// row keeps its current s3_url (the guarded UPDATE's COALESCE preserves it since applied==0). DVR is a
		// prefix. A synced main upload MUST carry a usable URL — fail closed if S3 is unavailable.
		if s3Client != nil {
			switch {
			case publishMainKey != "":
				s3URL = s3Client.BuildS3URL(publishMainKey)
			case syncStatusRow == "synced" && previousActiveKey != "":
				s3URL = s3Client.BuildS3URL(previousActiveKey)
			case artifactType == "dvr" && tenantID != "" && internalName != "":
				s3Prefix := s3Client.BuildDVRS3Key(tenantID, internalName, assetHash)
				s3URL = s3Client.BuildS3URL(s3Prefix)
			case syncObjectKey != "":
				s3URL = s3Client.BuildS3URL(syncObjectKey)
			}
		}
		if s3URL == "" {
			logger.WithField("asset_hash", assetHash).Warn("Refusing sync completion: could not derive a usable canonical S3 URL (S3 unavailable/misconfigured); leaving attempt for stale recovery")
			return
		}

		if db == nil {
			return
		}

		// This completion is bound by the EXACT (attempt id + node) match on the outstanding attempt, enforced
		// by the guarded UPDATE below. The attempt id is SERVER-MINTED (issued at permission time, echoed by
		// the node), and the node is the authenticated connection — so a node can only complete an operation
		// Foghorn assigned. The artifact-owner tenant scopes the mutation as PARTITION SCOPING (sourced from
		// the attempt-scoped read above). When no main-upload attempt matches, the UPDATE affects zero rows
		// and the code falls through to the incremental .dtsh path below.
		guardTenant := tenantID

		// Read-only work BEFORE the guarded transaction: the chapter this artifact finalized (if any)
		// and the canonical VOD S3 key to persist. Doing the lookups here keeps the transaction short.
		chapterID := ""
		if dtshIncluded {
			chapterID = chapterOriginIDForArtifact(ctx, assetHash)
		}
		// vod_metadata.s3_key must equal the PUBLISHED object key (active_object_key) — the exact object the
		// bytes were promoted to — so relay/deletion address the served version, not the bare descriptor.
		// publishMainKey is non-empty for a main vod upload (the descriptor-less refusal above guarantees it).
		derivedVodKey := ""
		if artifactType == "vod" && publishMainKey != "" {
			derivedVodKey = publishMainKey
		}

		// Build the storage-state analytics event NOW (identity/dtsh enrichment does its reads here,
		// before the transaction opens) so it can be enqueued through the SAME transaction as the S3
		// state change — a committed sync can never be lost to a disconnected Decklog or a crash.
		syncLifecycleEvent := buildArtifactStorageLifecycleEvent(ctx, artifactStorageStateLifecycle{
			artifactHash:    assetHash,
			artifactType:    artifactType,
			tenantID:        tenantID,
			streamID:        streamID,
			streamInternal:  internalName,
			s3URL:           s3URL,
			sizeBytes:       sizeBytes,
			nodeID:          reportingNodeID,
			storageLocation: "local",
			syncStatus:      "synced",
			hot:             true,
			synced:          true,
			finalized:       dtshIncluded,
			frozen:          false,
		})

		// ONE guarded, idempotent transaction. The terminal UPDATE only fires when the row is still
		// in_progress AND the completion matches the EXACT outstanding attempt (attempt id = $5 AND node =
		// $6, both required — a NULL/absent attempt id does NOT match). The attempt id is SERVER-MINTED and
		// echoed by the authenticated node, so completion consumes a Foghorn-assigned operation. tenant_id =
		// $7 (the artifact owner) is partition scoping. A duplicate, stale, or wrong-node completion matches
		// zero rows and the whole transaction is a no-op — no double node-copy, no resurrected lifecycle, no
		// wrong-node attribution. Node-copy, VOD metadata, and the chapter freeze all commit atomically with
		// the artifact promotion. The attempt identity is cleared here.
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			logger.WithError(err).WithField("asset_hash", assetHash).Error("failed to begin sync completion tx")
			return
		}
		defer tx.Rollback() //nolint:errcheck // rollback is best-effort on the non-commit paths
		q := foghorndb.New(tx)
		applied, err := q.CompleteMainArtifactSync(ctx, foghorndb.CompleteMainArtifactSyncParams{
			S3Url: s3URL, DtshSynced: sql.NullBool{Bool: dtshIncluded, Valid: true}, ArtifactHash: assetHash, SizeBytes: int64(sizeBytes),
			SyncRequestID: sql.NullString{String: requestID, Valid: true}, SyncNodeID: sql.NullString{String: reportingNodeID, Valid: true}, TenantID: guardTenant,
			SyncObjectKey: syncObjectKey, ActiveObjectKey: publishMainKey, ActiveDtshKey: publishDtshKey,
		})
		if err != nil {
			logger.WithError(err).WithField("asset_hash", assetHash).Error("failed to mark artifact as synced")
			return
		}
		if applied == 0 {
			// Not an in_progress→synced transition. An incremental .dtsh sync (TriggerDtshSync) runs on
			// an ALREADY-SYNCED artifact and reports back through this same SyncComplete path, so it can
			// never match the in_progress guard. Handle it as its own idempotent transition, authenticated
			// against the persisted DTSH attempt (request + node): set dtsh_synced on the synced row and
			// clear the attempt, then advance the chapter freeze that depends on it — without touching
			// sync_status or node copies. A stale/duplicate/wrong-node dtsh completion matches zero rows
			// (guarded on the attempt identity AND dtsh not-yet-set) and is a no-op.
			if dtshIncluded {
				// Tenant is MANDATORY here (no wildcard): the metadata pre-read resolved it, and a dtsh
				// completion for an unresolvable tenant must not finalize the index or advance reclaim.
				if tenantID == "" {
					logger.WithField("asset_hash", assetHash).Warn("Ignoring dtsh completion: unresolved tenant")
					return
				}
				dApplied, dErr := q.CompleteIncrementalDtshSync(ctx, foghorndb.CompleteIncrementalDtshSyncParams{
					ArtifactHash: assetHash, DtshSyncRequestID: sql.NullString{String: requestID, Valid: true},
					DtshSyncNodeID: sql.NullString{String: reportingNodeID, Valid: true}, TenantID: tenantID, ActiveDtshKey: publishDtshKey,
				})
				if dErr != nil {
					logger.WithError(dErr).WithField("asset_hash", assetHash).Error("failed to apply dtsh sync")
					return
				}
				if dApplied == 0 {
					// This completion LOST its CAS (a duplicate won, or the row is no longer in the pending-dtsh
					// state). Any object it published (recorded in the freeze_publication_ledger BEFORE the promote)
					// is left for reconcileFreezePublicationLedger to collect durably — it is req-aware, so a live
					// candidate or a still-retrying attempt is never deleted. Roll back and return.
					logger.WithField("asset_hash", assetHash).Debug("Ignoring sync completion: no in_progress attempt and no pending dtsh transition")
					return
				}
				// The .dtsh index was already PUBLISHED (out of tx) to the fresh publishDtshKey; this transition
				// flipped active_dtsh_key to it. If it superseded a PREVIOUS .dtsh version, that old index is now
				// unreferenced — durably enqueue it.
				if previousActiveDtshKey != "" && previousActiveDtshKey != publishDtshKey {
					if eErr := EnqueueStagingCleanupTx(ctx, tx, previousActiveDtshKey); eErr != nil {
						logger.WithError(eErr).WithField("asset_hash", assetHash).Error("failed to enqueue superseded .dtsh cleanup in dtsh sync completion")
						return
					}
				}
				if chapterID != "" {
					if frzErr := MarkChapterFrozenTx(ctx, tx, chapterID); frzErr != nil {
						logger.WithError(frzErr).WithFields(logging.Fields{"chapter_id": chapterID, "artifact_hash": assetHash}).
							Error("Chapter freeze transition failed in dtsh sync completion")
						return
					}
				}
				// Durable analytics for the finalization transition, on the same tx (syncLifecycleEvent
				// was built with finalized=dtshIncluded=true for this path).
				if lErr := enqueueArtifactStorageLifecycleTx(ctx, tx, syncLifecycleEvent); lErr != nil {
					logger.WithError(lErr).WithField("asset_hash", assetHash).Error("failed to enqueue storage lifecycle in dtsh sync completion")
					return
				}
				// Durably enqueue the superseded .dtsh staging object for deletion ON this transaction.
				if eErr := EnqueueStagingCleanupTx(ctx, tx, dtshStagingToCleanup); eErr != nil {
					logger.WithError(eErr).WithField("asset_hash", assetHash).Error("failed to enqueue .dtsh staging cleanup in dtsh sync completion")
					return
				}
				// This completion's .dtsh candidate is now LIVE (active_dtsh_key) and its staging is enqueued —
				// clear their publication-ledger rows so the sweep never reconsiders them.
				if lErr := ClearPublicationLedgerTx(ctx, tx, dtshStagingToCleanup, publishDtshKey); lErr != nil {
					logger.WithError(lErr).WithField("asset_hash", assetHash).Error("failed to clear publication ledger in dtsh sync completion")
					return
				}
				if cErr := tx.Commit(); cErr != nil {
					logger.WithError(cErr).WithField("asset_hash", assetHash).Error("failed to commit dtsh sync completion")
					return
				}
				NotifyCatalogDirty()
				if chapterID != "" {
					logger.WithFields(logging.Fields{"chapter_id": chapterID, "artifact_hash": assetHash}).
						Info("Chapter frozen — source segments eligible for reclaim")
				}
				logger.WithField("asset_hash", assetHash).Info("Incremental .dtsh sync applied")
				return
			}
			// This attempt LOST the guarded CAS (duplicate/stale/wrong-node). Any main/.dtsh object it published
			// was recorded in the freeze_publication_ledger BEFORE the promote, so reconcileFreezePublicationLedger
			// durably collects whichever candidate is orphaned (and its staging) — req-aware, so a live candidate
			// (a concurrent duplicate that won with the SAME key) or a still-retrying attempt is never deleted.
			// Nothing to do inline; roll back and return.
			logger.WithFields(logging.Fields{
				"asset_hash": assetHash,
				"request_id": requestID,
				"node_id":    reportingNodeID,
			}).Debug("Ignoring sync completion that does not match the outstanding attempt (duplicate/stale/wrong-node)")
			return
		}

		// applied == 1: THIS attempt won the CAS and the pointer flip above published the new object. The main
		// (and any bundled .dtsh) object was already promoted to its fresh candidate key OUTSIDE this
		// transaction, so nothing overwrites a served object and there is no S3 mutation inside the tx. If this
		// re-published over a PREVIOUS version (active_object_key changed), that superseded MEDIA object and the
		// superseded .dtsh index (the OLD active_dtsh_key, when this attempt published a new one) are now
		// unreferenced — durably enqueue them for cleanup on THIS transaction.
		if previousActiveKey != "" && previousActiveKey != publishMainKey {
			supersededKeys := []string{previousActiveKey}
			if previousActiveDtshKey != "" && previousActiveDtshKey != publishDtshKey {
				supersededKeys = append(supersededKeys, previousActiveDtshKey)
			}
			for _, sk := range supersededKeys {
				if eErr := EnqueueStagingCleanupTx(ctx, tx, sk); eErr != nil {
					logger.WithError(eErr).WithField("asset_hash", assetHash).Error("failed to enqueue superseded object cleanup in sync completion")
					return
				}
			}
		}
		// When this main upload BUNDLED a .dtsh ($2/dtshIncluded=true), the guarded UPDATE also CLEARED any
		// overlapping incremental .dtsh attempt's identity (a different request id that was in-flight). That
		// attempt may have uploaded its .dtsh staging and even promoted its versioned candidate — neither is
		// derivable once the identity is gone, so enqueue both here. The bundled attempt's own keys use
		// `requestID` (staging is enqueued as dtshStagingToCleanup; candidate is the new active_dtsh_key we keep).
		if dtshIncluded && previousDtshReq != "" && previousDtshReq != requestID && syncObjectKey != "" {
			if eErr := EnqueueDtshAttemptGarbageTx(ctx, tx, syncObjectKey, previousDtshReq); eErr != nil {
				logger.WithError(eErr).WithField("asset_hash", assetHash).Error("failed to enqueue superseded incremental .dtsh cleanup in sync completion")
				return
			}
		}

		// Add this node to cached_nodes (it has a local copy) IN THE SAME TRANSACTION. Pass the synced
		// size so the row and the emitted node-copy transition carry a real size, not zero.
		copyApplied, err := AddCachedNodeCopyTx(ctx, tx, assetHash, reportingNodeID, "", int64(sizeBytes), nodeClockCompletedAtMs)
		if err != nil {
			logger.WithError(err).WithField("asset_hash", assetHash).Error("failed to add cached node copy in sync completion")
			return
		}
		if !copyApplied {
			logger.WithFields(logging.Fields{
				"asset_hash":                 assetHash,
				"node_id":                    reportingNodeID,
				"node_clock_completed_at_ms": nodeClockCompletedAtMs,
			}).Info("Ignoring sync-complete placement superseded by a newer node deletion")
		}

		// For VOD, the s3_key in vod_metadata is the canonical S3 key. On processed-VOD replacement
		// uploads the derived key differs from the original upload key; persist the new value so relay
		// reads the synced location, not the original-upload row.
		if derivedVodKey != "" {
			if dbErr := q.UpsertSyncedVODObjectKey(ctx, foghorndb.UpsertSyncedVODObjectKeyParams{
				ArtifactHash: assetHash, S3Key: sql.NullString{String: derivedVodKey, Valid: true}, Filename: sql.NullString{String: assetHash + "." + format, Valid: true},
			}); dbErr != nil {
				logger.WithError(dbErr).WithField("asset_hash", assetHash).Error("failed to update vod_metadata.s3_key in sync completion")
				return
			}
		}

		// Chapter artifacts (origin_type='dvr_chapter') advance finalized → frozen once both
		// sync_status AND dtsh_synced are true, atomically with this completion. This is the trigger
		// the reclaim sweep waits on; without it source TS segments stay pinned.
		if chapterID != "" {
			if frzErr := MarkChapterFrozenTx(ctx, tx, chapterID); frzErr != nil {
				logger.WithError(frzErr).WithFields(logging.Fields{
					"chapter_id":    chapterID,
					"artifact_hash": assetHash,
				}).Error("Chapter freeze transition failed in sync completion")
				return
			}
		}

		// Durable storage-state analytics: enqueue the pre-built lifecycle event onto THIS transaction,
		// so the S3 transition and its stats record commit together (no fire-and-forget loss).
		if lErr := enqueueArtifactStorageLifecycleTx(ctx, tx, syncLifecycleEvent); lErr != nil {
			logger.WithError(lErr).WithField("asset_hash", assetHash).Error("failed to enqueue storage lifecycle in sync completion")
			return
		}

		// The staging objects are now superseded by the promoted canonical copies. Enqueue their deletion ON
		// THIS transaction so cleanup is DURABLE: a crash between the promote and commit leaves the staging
		// bytes for an idempotent retry, and once committed the StagingCleanupJob deletes them with retries
		// from the durable queue (never a one-shot best-effort delete that leaks storage on failure).
		for _, sk := range []string{stagingToCleanup, dtshStagingToCleanup} {
			if eErr := EnqueueStagingCleanupTx(ctx, tx, sk); eErr != nil {
				logger.WithError(eErr).WithField("asset_hash", assetHash).Error("failed to enqueue staging cleanup in sync completion")
				return
			}
		}

		// This completion's published candidates are now LIVE (active_object_key / active_dtsh_key) and its
		// staging objects are enqueued above — clear their publication-ledger rows on THIS transaction so the
		// sweep never reconsiders them. Deletes strictly by object_key, so an orphaned .dtsh candidate from a
		// mixed-duplicate peer completion keeps its own ledger row for the sweep.
		if lErr := ClearPublicationLedgerTx(ctx, tx, stagingToCleanup, dtshStagingToCleanup, publishMainKey, publishDtshKey); lErr != nil {
			logger.WithError(lErr).WithField("asset_hash", assetHash).Error("failed to clear publication ledger in sync completion")
			return
		}
		if err := tx.Commit(); err != nil {
			logger.WithError(err).WithField("asset_hash", assetHash).Error("failed to commit sync completion")
			return
		}

		// The durable S3 lifecycle is now committed on the authoritative foghorn.artifacts row; the
		// artifact reconciler is the single writer that projects it onto the Commodore catalog. Kick it
		// explicitly on this committed change so the catalog reflects 'synced' promptly.
		NotifyCatalogDirty()
		if chapterID != "" {
			logger.WithFields(logging.Fields{
				"chapter_id":    chapterID,
				"artifact_hash": assetHash,
			}).Info("Chapter frozen — source segments eligible for reclaim")
		}

		logger.WithFields(logging.Fields{
			"asset_hash":    assetHash,
			"s3_url":        s3URL,
			"node_id":       reportingNodeID,
			"dtsh_included": dtshIncluded,
		}).Info("Asset synced to S3, local copy retained")
		// (The storage-state analytics event was enqueued durably inside the committed transaction above.)

		// (A re-published VOD's superseded object is enqueued durably via previousActiveKey in the transaction
		// above — no best-effort post-commit delete.)

	default:
		// A .dtsh sync failure reports through this same path (dtsh_included=false) and runs on an
		// already-synced row, so it NEVER matches the main-upload attempt guard. Attribute it to the
		// persisted DTSH attempt FIRST (tenant-scoped); only a completion that is NOT a known dtsh
		// attempt falls through to the main-upload failure guard. The failure tenant is the identity-resolved
		// owner (ownerTenant), NOT an ad-hoc unscoped hash lookup — both failure guards are tenant-scoped.
		if applyDtshCompletionFailure(ctx, assetHash, reportingNodeID, requestID, errorMsg, ownerTenant, logger) {
			break
		}
		// Shared guarded failure path. Applied only on a real, attempt-matched transition.
		if applySyncCompletionFailure(ctx, assetHash, reportingNodeID, requestID, errorMsg, ownerTenant, complete.GetLocalMissing(), nodeClockCompletedAtMs, logger) {
			logger.WithFields(logging.Fields{"asset_hash": assetHash, "error": errorMsg}).Warn("Asset sync to S3 failed")
		}
	}
}

// applySyncCompletionFailure applies a failed / lost_local sync completion as ONE guarded transaction,
// shared by the SyncComplete failure paths. It locks and verifies the EXACT outstanding
// attempt (in_progress + request id + node — no null wildcard, so an unauthenticated/legacy-null row
// cannot be completed; stale recovery returns such rows to retryable). local_missing is a per-NODE
// fact: it orphans THIS node's copy and declares terminal lost_local ONLY when no other complete copy
// survives, otherwise retryable 'failed'. Returns applied=true only on a real transition, so callers
// run post-completion side effects (S3 cleanup) ONLY for a completion that actually matched — never
// for a duplicate/stale/wrong-node one.
func applySyncCompletionFailure(ctx context.Context, assetHash, reportingNodeID, requestID, errorMsg, ownerTenant string, localMissing bool, nodeClockCompletedAtMs int64, logger logging.Logger) (applied bool) {
	if db == nil {
		incArtifactSyncOutcome("failed")
		return false
	}
	if strings.TrimSpace(ownerTenant) == "" {
		// No identity-resolved owner tenant → cannot tenant-scope the guard; fail closed (a settlement is
		// never applied unscoped).
		return false
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		logger.WithError(err).WithField("asset_hash", assetHash).Error("failed to begin sync failure tx")
		return false
	}
	defer tx.Rollback() //nolint:errcheck // best-effort on non-commit paths

	// Guard FIRST: lock the row and verify it is the EXACT outstanding attempt (request id + node) while
	// still in_progress, BEFORE any orphaning — that exact match is the authorization, so a
	// stale/unauthenticated/wrong-node completion never drops a copy or mutates state. The lock reads the
	// artifact owner (tenant_id), which scopes the terminal UPDATE below as partition scoping. A query
	// error rejects the completion (fail closed).
	q := foghorndb.New(tx)
	locked, guardErr := q.LockSyncFailureAttempt(ctx, foghorndb.LockSyncFailureAttemptParams{
		ArtifactHash: assetHash, SyncRequestID: sql.NullString{String: requestID, Valid: true},
		SyncNodeID: sql.NullString{String: reportingNodeID, Valid: true}, TenantID: ownerTenant,
	})
	if errors.Is(guardErr, sql.ErrNoRows) {
		logger.WithFields(logging.Fields{"asset_hash": assetHash, "request_id": requestID, "node_id": reportingNodeID}).
			Debug("Ignoring failure completion that does not match the outstanding attempt (duplicate/stale/wrong-node)")
		return false
	}
	lockedTenant, lockedObjectKey := locked.TenantID, locked.SyncObjectKey
	if guardErr != nil {
		logger.WithError(guardErr).WithField("asset_hash", assetHash).Error("failed to lock artifact for sync failure")
		return false
	}

	newSyncStatus := "failed"
	if localMissing {
		// Drop the failed node's copy (emit LOST) atomically, then check whether any OTHER node still
		// holds a present, complete copy.
		deletionOutcome, delErr := DeleteNodeArtifactTx(ctx, tx, assetHash, reportingNodeID, nodeClockCompletedAtMs)
		if delErr != nil {
			logger.WithError(delErr).WithField("asset_hash", assetHash).Error("failed to orphan local_missing node copy")
			return false
		}
		ObserveArtifactDeletionOutcome(string(deletionOutcome))
		switch deletionOutcome {
		case state.NodeArtifactDeletionFenced:
			logger.WithFields(logging.Fields{
				"asset_hash":                 assetHash,
				"node_id":                    reportingNodeID,
				"node_clock_completed_at_ms": nodeClockCompletedAtMs,
				"deletion_outcome":           deletionOutcome,
			}).Warn("Ignoring stale local_missing copy state superseded by newer node inventory")
		case state.NodeArtifactDeletionApplied:
			otherComplete, cErr := q.CountOtherCompleteArtifactCopies(ctx, foghorndb.CountOtherCompleteArtifactCopiesParams{ArtifactHash: assetHash, NodeID: reportingNodeID})
			if cErr != nil {
				logger.WithError(cErr).WithField("asset_hash", assetHash).Error("failed to count surviving copies")
				return false
			}
			if otherComplete == 0 {
				newSyncStatus = "lost_local" // no viable source remains → terminal
			}
		case state.NodeArtifactDeletionAbsent:
			// Absence is not proof that no viable source exists: inventory may not
			// have observed the reporting copy yet. Keep the attempt retryable.
		case state.NodeArtifactDeletionParentMissing:
			logger.WithField("asset_hash", assetHash).Error("sync failure lost its locked artifact parent")
			return false
		default:
			logger.WithFields(logging.Fields{"asset_hash": assetHash, "deletion_outcome": deletionOutcome}).Error("sync failure received an unknown deletion outcome")
			return false
		}
	}
	incArtifactSyncOutcome(newSyncStatus)

	// The row is already locked+guarded above; this UPDATE keys on the hash under the artifact-owner
	// tenant (tenant_id = $4, partition scoping from the locked row). The attempt identity is cleared.
	if uErr := q.FailMainArtifactSync(ctx, foghorndb.FailMainArtifactSyncParams{ArtifactHash: assetHash, SyncStatus: newSyncStatus, ErrorMessage: errorMsg, TenantID: lockedTenant}); uErr != nil {
		logger.WithError(uErr).WithField("asset_hash", assetHash).Error("failed to record sync failure")
		return false
	}
	// This failure WON the row lock, so the concurrent SUCCESS (if any) for the SAME attempt lost its CAS. The
	// success may have already PUBLISHED its candidate objects (main + .dtsh) outside its transaction — and its
	// own lost-CAS cleanup is only best-effort. So durably enqueue, ON this transaction, BOTH the attempt's
	// staging objects AND its published candidates (all deterministic from lockedObjectKey + requestID). The
	// identity is cleared here, so no future attempt reuses these keys. Descriptor-less rows enqueue nothing.
	if lockedObjectKey != "" {
		for _, key := range []string{
			FreezeStagingKey(lockedObjectKey, requestID),
			FreezeStagingKey(lockedObjectKey+".dtsh", requestID),
			FreezePublishKey(lockedObjectKey, requestID),
			FreezePublishDtshKey(lockedObjectKey, requestID),
		} {
			if eErr := EnqueueStagingCleanupTx(ctx, tx, key); eErr != nil {
				logger.WithError(eErr).WithField("asset_hash", assetHash).Error("failed to enqueue cleanup on sync failure")
				return false
			}
		}
	}
	if err := tx.Commit(); err != nil {
		logger.WithError(err).WithField("asset_hash", assetHash).Error("failed to commit sync failure")
		return false
	}
	// lost_local means nothing was uploaded from this node — the caller must not run S3 cleanup.
	return newSyncStatus == "failed"
}

type artifactStorageStateLifecycle struct {
	artifactHash    string
	artifactType    string
	tenantID        string
	streamID        string
	streamInternal  string
	s3URL           string
	sizeBytes       uint64
	nodeID          string
	storageLocation string
	syncStatus      string
	hot             bool
	synced          bool
	finalized       bool
	frozen          bool
}

// buildArtifactStorageLifecycleEvent enriches + constructs the storage-state lifecycle proto for a
// successful S3 state transition WITHOUT Decklog gating and WITHOUT emitting. The caller enqueues the
// returned event through its OWN transaction (durable outbox via enqueueArtifactStorageLifecycleTx),
// so a committed S3 transition can never be lost to a disconnected drain client or a process exit.
// Enrichment (identity resolve, dtsh lookup) runs here, BEFORE the caller opens its transaction, so no
// network/DB round-trip is held during the commit. Returns nil for an unknown/unresolved type.
func buildArtifactStorageLifecycleEvent(ctx context.Context, state artifactStorageStateLifecycle) any {
	if state.artifactHash == "" {
		return nil
	}
	if state.frozen && state.synced && !state.finalized {
		state.finalized = dtshSyncedForArtifact(ctx, state.artifactHash)
	}
	if state.tenantID == "" || state.artifactType == "" {
		artifactType, tenantID, streamInternal, streamID := artifactLifecycleIdentity(ctx, state.artifactHash)
		if state.artifactType == "" {
			state.artifactType = artifactType
		}
		if state.tenantID == "" {
			state.tenantID = tenantID
		}
		if state.streamInternal == "" {
			state.streamInternal = streamInternal
		}
		if state.streamID == "" {
			state.streamID = streamID
		}
	}
	switch state.artifactType {
	case "clip":
		return &ipcpb.ClipLifecycleData{
			Stage:              ipcpb.ClipLifecycleData_STAGE_DONE,
			ClipHash:           state.artifactHash,
			S3Url:              stringPtrIfNotEmpty(state.s3URL),
			SizeBytes:          uint64PtrIfNonZero(state.sizeBytes),
			NodeId:             stringPtrIfNotEmpty(state.nodeID),
			TenantId:           stringPtrIfNotEmpty(state.tenantID),
			StreamId:           stringPtrIfNotEmpty(state.streamID),
			StreamInternalName: stringPtrIfNotEmpty(state.streamInternal),
			StorageLocation:    stringPtrIfNotEmpty(state.storageLocation),
			SyncStatus:         stringPtrIfNotEmpty(state.syncStatus),
			HasLocalCopy:       boolPtr(state.hot),
			IsSynced:           boolPtr(state.synced),
			IsFinalized:        boolPtr(state.finalized),
			ProgressPercent:    uint32Ptr(100),
			CompletedAt:        int64Ptr(time.Now().Unix()),
		}
	case "vod":
		return &ipcpb.VodLifecycleData{
			Status:          ipcpb.VodLifecycleData_STATUS_COMPLETED,
			VodHash:         state.artifactHash,
			S3Url:           stringPtrIfNotEmpty(state.s3URL),
			SizeBytes:       uint64PtrIfNonZero(state.sizeBytes),
			NodeId:          stringPtrIfNotEmpty(state.nodeID),
			TenantId:        stringPtrIfNotEmpty(state.tenantID),
			StorageLocation: stringPtrIfNotEmpty(state.storageLocation),
			SyncStatus:      stringPtrIfNotEmpty(state.syncStatus),
			HasLocalCopy:    boolPtr(state.hot),
			IsSynced:        boolPtr(state.synced),
			IsFinalized:     boolPtr(state.finalized),
			ProgressPct:     int32Ptr(100),
			CompletedAt:     int64Ptr(time.Now().Unix()),
		}
	case "dvr":
		return &ipcpb.DVRLifecycleData{
			Status:             ipcpb.DVRLifecycleData_STATUS_STOPPED,
			DvrHash:            state.artifactHash,
			SizeBytes:          uint64PtrIfNonZero(state.sizeBytes),
			NodeId:             stringPtrIfNotEmpty(state.nodeID),
			TenantId:           stringPtrIfNotEmpty(state.tenantID),
			StreamId:           stringPtrIfNotEmpty(state.streamID),
			StreamInternalName: stringPtrIfNotEmpty(state.streamInternal),
			StorageLocation:    stringPtrIfNotEmpty(state.storageLocation),
			SyncStatus:         stringPtrIfNotEmpty(state.syncStatus),
			HasLocalCopy:       boolPtr(state.hot),
			IsSynced:           boolPtr(state.synced),
			IsFinalized:        boolPtr(state.finalized),
		}
	default:
		return nil
	}
}

// enqueueArtifactStorageLifecycleTx writes a pre-built storage-state lifecycle event onto the caller's
// transaction, so it commits atomically with the S3 state change. Delivery is the drain worker's job;
// Decklog connectivity affects DRAINING, not capture.
func enqueueArtifactStorageLifecycleTx(ctx context.Context, tx *sql.Tx, event any) error {
	switch e := event.(type) {
	case *ipcpb.ClipLifecycleData:
		if e.GetTenantId() == "" {
			return fmt.Errorf("clip storage lifecycle event for %s missing tenant — refusing to commit state without an attributable analytics event", e.GetClipHash())
		}
		return artifactoutbox.EnqueueClipLifecycleTx(ctx, tx, e)
	case *ipcpb.VodLifecycleData:
		if e.GetTenantId() == "" {
			return fmt.Errorf("vod storage lifecycle event for %s missing tenant — refusing to commit state without an attributable analytics event", e.GetVodHash())
		}
		return artifactoutbox.EnqueueVodLifecycleTx(ctx, tx, e)
	case *ipcpb.DVRLifecycleData:
		if e.GetTenantId() == "" {
			return fmt.Errorf("dvr storage lifecycle event for %s missing tenant — refusing to commit state without an attributable analytics event", e.GetDvrHash())
		}
		return artifactoutbox.EnqueueDVRLifecycleTx(ctx, tx, e)
	default:
		// A nil or unknown event means buildArtifactStorageLifecycleEvent couldn't resolve the
		// artifact's type/identity. The promised analytics record can't be produced, so the state
		// transaction must NOT commit silently without it — roll back and let the completion retry.
		return fmt.Errorf("unresolved storage lifecycle event (%T) — refusing to commit S3 state change without its analytics record", event)
	}
}

func artifactLifecycleIdentity(ctx context.Context, artifactHash string) (artifactType, tenantID, streamInternal, streamID string) {
	resolver := identity.Default()
	if resolver == nil || artifactHash == "" {
		return "", "", "", ""
	}
	id, err := resolver.ResolveArtifact(ctx, artifactHash, "")
	if err != nil {
		return "", "", "", ""
	}
	return id.Kind, id.TenantID, id.StreamInternalName, id.StreamID
}

// resolveLifecycleStreamID maps a source internal name to its stream UUID
// via the identity facade. Lifecycle events without a valid stream UUID are
// dropped by periscope-ingest, so emitters backfill it here.
func resolveLifecycleStreamID(ctx context.Context, streamInternalName string) string {
	resolver := identity.Default()
	if resolver == nil || streamInternalName == "" {
		return ""
	}
	rctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	id, err := resolver.ResolveStream(rctx, streamInternalName)
	if err != nil {
		return ""
	}
	return id.StreamID
}

func dtshSyncedForArtifact(ctx context.Context, artifactHash string) bool {
	if db == nil || artifactHash == "" {
		return false
	}
	synced, err := foghorndb.New(db).DtshSyncedForArtifact(ctx, artifactHash)
	if err != nil {
		return false
	}
	return synced
}

func stringPtrIfNotEmpty(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func uint64PtrIfNonZero(v uint64) *uint64 {
	if v == 0 {
		return nil
	}
	return &v
}

func uint32Ptr(v uint32) *uint32 { return &v }

func int32Ptr(v int32) *int32 { return &v }

func int64Ptr(v int64) *int64 { return &v }

func boolPtr(v bool) *bool { return &v }

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
	// Refresh the server's own TLS certificates. The Foghorn control listener
	// serves both internal mesh callers and public cluster-FQDN callers.
	if serverCert.Loaded() {
		bundles := []*ipcpb.TLSCertBundle{}
		if certFile, keyFile := os.Getenv("GRPC_TLS_CERT_PATH"), os.Getenv("GRPC_TLS_KEY_PATH"); certFile != "" || keyFile != "" {
			if bundle, err := fileServerTLSBundle(certFile, keyFile); err == nil {
				bundles = append(bundles, bundle)
			} else {
				log.WithError(err).Warn("Failed to refresh file-based server TLS certificate")
			}
		}
		rootDomain := platformRootDomain()
		navBundles, certErr := fetchServedClusterTLSBundles(rootDomain)
		if certErr == nil && len(navBundles) > 0 {
			bundles = append(bundles, navBundles...)
		} else if navigatorClient != nil {
			if certErr == nil {
				certErr = fmt.Errorf("no served cluster TLS bundles found")
			}
			log.WithError(certErr).Warn("Skipping server TLS refresh because Navigator ACME bundles are unavailable")
			return
		}
		if len(bundles) > 0 {
			if err := serverCert.StoreBundles(bundles); err == nil {
				bundleIDs := make([]string, 0, len(bundles))
				for _, bundle := range bundles {
					bundleIDs = append(bundleIDs, bundle.GetBundleId())
				}
				log.WithField("bundle_ids", bundleIDs).Debug("Refreshed server TLS certificates")
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
		//   - cluster TLS bundle (from fetchClusterTLSBundle internally)
		//   - platform-edge / pool-assigned multi-SAN (when applicable)
		//   - per-tenant *.{tenant}.cdn.{root} bundles (from
		//     fetchTenantBundles)
		// Fingerprinting on JUST the cluster bundle would mask tenant
		// bundle changes; adding/removing a paying tenant's cluster
		// subscription would never trigger a push until the cluster
		// bundle itself rotated. Fingerprint the full set instead.
		mode := resolveOperationalMode(n.canonicalID, ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_UNSPECIFIED)
		seed, fallback := composeConfigSeedCandidate(n.canonicalID, nil, n.peerAddr, mode, "")
		stripWildcardSiteWithoutTLS(seed)

		preview := seed
		if lastGood, loadErr := loadLastConfigSeed(context.Background(), n.connID); loadErr == nil && lastGood != nil {
			preview = mergeConfigSeedFallback(seed, lastGood, fallback)
		} else if loadErr != nil && !errors.Is(loadErr, sql.ErrNoRows) {
			log.WithError(loadErr).WithField("node_id", n.canonicalID).Warn("Failed to preview durable TLS authority")
		}
		nextState := tlsBundleSetState(preview.GetTlsBundles(), seedCaBundle)

		prev, ok := lastPushedTLSState.Load(n.connID)
		if prevState, isString := prev.(string); ok && isString && prevState == nextState {
			continue
		}

		if err := SendConfigSeedWithFallback(n.connID, seed, fallback); err != nil {
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

func stripWildcardSiteWithoutTLS(seed *ipcpb.ConfigSeed) {
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

func fetchClusterTLSBundle(nodeID string) (*ipcpb.TLSCertBundle, bool, error) {
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
	return fetchClusterTLSBundleByClusterID(node.GetClusterId(), rootDomain)
}

func fetchClusterTLSBundleByClusterID(clusterID, rootDomain string) (*ipcpb.TLSCertBundle, bool, error) {
	if navigatorClient == nil {
		return nil, false, errors.New("cluster TLS authority is unavailable")
	}
	bundleID, wildcardDomain, ok := clusterTLSBundleLookup(clusterID, rootDomain)
	if !ok {
		return nil, false, fmt.Errorf("cluster %q has no valid TLS bundle identity", clusterID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	certResp, certErr := navigatorClient.GetTLSBundle(ctx, &dnspb.GetTLSBundleRequest{BundleId: bundleID})
	if certErr != nil {
		return nil, false, certErr
	}
	found, presenceErr := navigatorTLSBundleFound(certResp)
	if presenceErr != nil {
		return nil, false, presenceErr
	}
	if !found {
		return nil, false, nil
	}

	return &ipcpb.TLSCertBundle{
		CertPem:       certResp.GetCertPem(),
		KeyPem:        certResp.GetKeyPem(),
		Domain:        wildcardDomain,
		ExpiresAt:     certResp.GetExpiresAt(),
		BundleId:      bundleID,
		SiteAddresses: []string{wildcardDomain},
		Version:       certResp.GetVersion(),
	}, true, nil
}

// navigatorTLSBundleFound keeps absence distinct from a failed authority read.
// Callers may remove durable material only for a non-nil, error-free response
// that explicitly reports found=false.
func navigatorTLSBundleFound(resp *dnspb.GetTLSBundleResponse) (bool, error) {
	if resp == nil {
		return false, errors.New("navigator returned an empty TLS bundle response")
	}
	if errText := strings.TrimSpace(resp.GetError()); errText != "" {
		return false, fmt.Errorf("navigator: %s", errText)
	}
	return resp.GetFound(), nil
}

func fileServerTLSBundle(certFile, keyFile string) (*ipcpb.TLSCertBundle, error) {
	if strings.TrimSpace(certFile) == "" || strings.TrimSpace(keyFile) == "" {
		return nil, fmt.Errorf("both GRPC_TLS_CERT_PATH and GRPC_TLS_KEY_PATH are required together")
	}
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, fmt.Errorf("read cert file %q: %w", certFile, err)
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("read key file %q: %w", keyFile, err)
	}
	return &ipcpb.TLSCertBundle{
		BundleId: "file:" + certFile,
		Domain:   "foghorn.internal",
		CertPem:  string(certPEM),
		KeyPem:   string(keyPEM),
	}, nil
}

func waitForServedClusterTLSBundles(ctx context.Context, rootDomain string) ([]*ipcpb.TLSCertBundle, error) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var lastErr error
	for {
		bundles, err := fetchServedClusterTLSBundles(rootDomain)
		if err == nil && len(bundles) > 0 {
			return bundles, nil
		}
		if err != nil {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			if lastErr != nil {
				return nil, fmt.Errorf("%w: %w", ctx.Err(), lastErr)
			}
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func fetchServedClusterTLSBundles(rootDomain string) ([]*ipcpb.TLSCertBundle, error) {
	return collectServedClusterTLSBundles(servedClusterIDsForTLS(), func(clusterID string) (*ipcpb.TLSCertBundle, bool, error) {
		return fetchClusterTLSBundleByClusterID(clusterID, rootDomain)
	})
}

func collectServedClusterTLSBundles(clusterIDs []string, fetch func(string) (*ipcpb.TLSCertBundle, bool, error)) ([]*ipcpb.TLSCertBundle, error) {
	if len(clusterIDs) == 0 {
		return nil, nil
	}
	bundles := make([]*ipcpb.TLSCertBundle, 0, len(clusterIDs))
	for _, clusterID := range clusterIDs {
		bundle, found, err := fetch(clusterID)
		if err != nil {
			return nil, fmt.Errorf("fetch TLS bundle for served cluster %q: %w", clusterID, err)
		}
		if found && bundle != nil {
			bundles = append(bundles, bundle)
		}
	}
	return bundles, nil
}

func servedClusterIDsForTLS() []string {
	seen := map[string]struct{}{}
	out := []string{}
	add := func(clusterID string) {
		clusterID = strings.TrimSpace(clusterID)
		if clusterID == "" {
			return
		}
		if _, ok := seen[clusterID]; ok {
			return
		}
		seen[clusterID] = struct{}{}
		out = append(out, clusterID)
	}
	add(localClusterID)
	for _, clusterID := range ServedClustersSnapshot() {
		add(clusterID)
	}
	return out
}

func clusterTLSBundleLookup(clusterID, rootDomain string) (string, string, bool) {
	rootDomain = strings.Trim(strings.TrimSpace(rootDomain), ".")
	if rootDomain == "" {
		return "", "", false
	}

	clusterName := ""
	if getClusterFn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		cluster, err := getClusterFn(ctx, strings.TrimSpace(clusterID))
		cancel()
		if err == nil && cluster != nil {
			clusterName = cluster.GetClusterName()
		}
	}

	slug := pkgdns.ClusterSlug(clusterID, clusterName)
	if slug == "" || slug == "default" {
		return "", "", false
	}
	wildcardDomain := fmt.Sprintf("*.%s.%s", slug, rootDomain)
	return "cluster:" + slug, wildcardDomain, true
}

func tlsBundleState(bundle *ipcpb.TLSCertBundle) string {
	return tlsMaterialState(bundle, nil)
}

// tlsBundleSetState fingerprints the full ordered set of TLS bundles
// plus the CA bundle. Used by the refresh loop to dedup pushes: a
// change in any tenant or platform bundle (added, removed, or rotated)
// produces a different fingerprint, which guarantees the next refresh
// pushes a fresh ConfigSeed instead of stalling on the cluster bundle's
// fingerprint alone.
func tlsBundleSetState(bundles []*ipcpb.TLSCertBundle, caBundle []byte) string {
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
		payload = append(payload, '\x00')
		payload = append(payload, b.GetVersion()...)
		for _, address := range b.GetSiteAddresses() {
			payload = append(payload, '\x00')
			payload = append(payload, address...)
		}
		payload = fmt.Appendf(payload, "\x00%d", b.GetExpiresAt())
		payload = append(payload, '\x00')
	}
	payload = append(payload, caBundle...)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func tlsMaterialState(bundle *ipcpb.TLSCertBundle, caBundle []byte) string {
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
	foghornpb.UnimplementedEdgeProvisioningServiceServer
}

func RegisterEdgeProvisioningService(srv *grpc.Server) {
	foghornpb.RegisterEdgeProvisioningServiceServer(srv, &EdgeProvisioningServer{})
}

func (s *EdgeProvisioningServer) PreRegisterEdge(ctx context.Context, req *foghornpb.PreRegisterEdgeRequest) (*foghornpb.PreRegisterEdgeResponse, error) {
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
		validateFn = func(c context.Context, t string) (*quartermasterpb.ValidateBootstrapTokenResponse, error) {
			return quartermasterClient.ValidateBootstrapTokenEx(c, &quartermasterpb.ValidateBootstrapTokenRequest{
				Token:    t,
				ClientIp: clientIP,
				Consume:  false,
			})
		}
	}
	valCtx, valCancel := context.WithTimeout(ctx, 15*time.Second)
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

	edgeDomain := pkgdns.EdgeNodeFQDN(nodeID, clusterSlug, rootDomain)
	poolDomain := fmt.Sprintf("edge.%s.%s", clusterSlug, rootDomain)
	foghornAddr := fmt.Sprintf("foghorn.%s.%s:18029", clusterSlug, rootDomain)

	resp := &foghornpb.PreRegisterEdgeResponse{
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

func processValidateEdgeToken(requestID string, req *ipcpb.ValidateEdgeTokenRequest, nodeID string, stream ipcpb.HelmsmanControl_ConnectServer, logger logging.Logger) {
	token := req.GetToken()
	resp := &ipcpb.ValidateEdgeTokenResponse{Valid: false}

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

func sendEdgeTokenResponse(requestID string, stream ipcpb.HelmsmanControl_ConnectServer, resp *ipcpb.ValidateEdgeTokenResponse, logger logging.Logger) {
	msg := &ipcpb.ControlMessage{
		RequestId: requestID,
		SentAt:    timestamppb.Now(),
		Payload:   &ipcpb.ControlMessage_ValidateEdgeTokenResponse{ValidateEdgeTokenResponse: resp},
	}
	if err := stream.Send(msg); err != nil {
		logger.WithError(err).Warn("Failed to send edge token validation response")
	}
}

// processEdgeMistAdminSession relays a Mist-admin session validation
// request from Helmsman to Commodore. The connected nodeID is injected
// here so a token minted for one edge cannot be replayed against another.
func processEdgeMistAdminSession(requestID string, req *ipcpb.EdgeMistAdminSessionRequest, nodeID string, stream ipcpb.HelmsmanControl_ConnectServer, logger logging.Logger) {
	resp := &ipcpb.EdgeMistAdminSessionResponse{Valid: false}
	if req.GetToken() == "" || nodeID == "" || CommodoreClient == nil {
		sendEdgeMistAdminSessionResponse(requestID, stream, resp, logger)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	commResp, err := CommodoreClient.ValidateMistAdminSession(ctx, &commodorepb.ValidateMistAdminSessionRequest{
		Token:          req.GetToken(),
		ExpectedNodeId: nodeID,
	})
	if err != nil {
		logger.WithError(err).WithField("node_id", nodeID).
			Warn("Mist admin session validation failed at Commodore")
		sendEdgeMistAdminSessionResponse(requestID, stream, resp, logger)
		return
	}

	resp.Valid = commResp.GetValid()
	resp.UserId = commResp.GetUserId()
	resp.TenantId = commResp.GetTenantId()
	resp.Role = commResp.GetRole()
	resp.NodeId = commResp.GetNodeId()
	resp.ClusterId = commResp.GetClusterId()
	resp.ExpiresAt = commResp.GetExpiresAt()

	sendEdgeMistAdminSessionResponse(requestID, stream, resp, logger)
}

func sendEdgeMistAdminSessionResponse(requestID string, stream ipcpb.HelmsmanControl_ConnectServer, resp *ipcpb.EdgeMistAdminSessionResponse, logger logging.Logger) {
	msg := &ipcpb.ControlMessage{
		RequestId: requestID,
		SentAt:    timestamppb.Now(),
		Payload:   &ipcpb.ControlMessage_EdgeMistAdminSessionResponse{EdgeMistAdminSessionResponse: resp},
	}
	if err := stream.Send(msg); err != nil {
		logger.WithError(err).Warn("Failed to send mist admin session response")
	}
}

// processThumbnailUploadRequest resolves internal_name → stable ID, generates
// presigned PUT URLs for each thumbnail file, and sends them back to Helmsman.
// S3 keys use stable identifiers: stream_id (UUID) for live streams,
// artifact_hash (32-char hex) for artifacts. Never playback_id (rotatable).
// nodeProducesThumbnailResource reports whether nodeID is actually serving/producing the resource a thumbnail
// PUT would overwrite — the per-resource task authorization that keeps a merely tenant-entitled node from
// naming a resource it isn't running. FAIL CLOSED on every unproven case. Live: the node must be running the
// stream (in-memory stream instances). Artifacts (vod/dvr/processing): it must hold a non-orphaned copy OR be
// the artifact's assigned processing node (the copy may not be registered yet during processing).
func nodeProducesThumbnailResource(ctx context.Context, nodeID string, kind streamident.Kind, isLive bool, streamInternalName, resourceKey, tenantID string) bool {
	if strings.TrimSpace(nodeID) == "" || strings.TrimSpace(resourceKey) == "" || strings.TrimSpace(tenantID) == "" {
		return false
	}
	if isLive {
		_, ok := state.DefaultManager().GetStreamInstances(streamInternalName)[nodeID]
		return ok
	}
	switch kind {
	case streamident.KindArtifactVOD, streamident.KindArtifactDVR, streamident.KindArtifactProcessing:
		conn := GetDB()
		if conn == nil {
			return false
		}
		// TENANT-SCOPED so a hash collision across tenants can't cross-authorize. A serving node must hold a
		// COMPLETE, non-orphaned copy; a processing node must be the artifact's assigned processor. Both are
		// scoped to the resolved tenant.
		ok, err := foghorndb.New(conn).ThumbnailResourceProducedByNode(ctx, foghorndb.ThumbnailResourceProducedByNodeParams{ArtifactHash: resourceKey, NodeID: nodeID, TenantID: tenantID})
		if err != nil {
			return false
		}
		return ok.Valid && ok.Bool
	default:
		return false
	}
}

func processThumbnailUploadRequest(requestID string, req *ipcpb.ThumbnailUploadRequest, nodeID string, connProtocolVersion int32, stream ipcpb.HelmsmanControl_ConnectServer, logger logging.Logger) {
	internalName := req.GetInternalName()
	filePaths := req.GetFilePaths()

	logger.WithFields(logging.Fields{
		"internal_name": internalName,
		"file_count":    len(filePaths),
		"node_id":       nodeID,
	}).Info("Processing thumbnail upload request")

	// Strict protocol gate (fail closed, no legacy path): only a sidecar that declares the staged-thumbnail
	// protocol uploads to the per-attempt staging keys and echoes the attempt id at completion. A sidecar below
	// it could neither be verified nor bound to the publication CAS, so it gets NO mint.
	if !ControlFeaturesForProtocol(connProtocolVersion).StagedThumbnail {
		logger.WithFields(logging.Fields{
			"internal_name":    internalName,
			"node_id":          nodeID,
			"protocol_version": connProtocolVersion,
		}).Warn("Thumbnail upload denied: sidecar control-protocol version below staged-thumbnail minimum")
		return
	}

	// No Chandler capability handshake: thumbnails are served from a DETERMINISTIC static key, so publication does not
	// depend on Chandler being reachable/ready. The bytes land safely and serving resumes when Chandler returns
	// (docs/architecture/thumbnails.md). Foghorn/Chandler backend agreement is enforced at deploy by the CLI.

	// Note: s3Client nil-check moved to inside the StorageMintLocal branch
	// so a self-host pool with no local S3 can still federate to platform
	// storage when the resolver picks that path.

	// Resolve internal_name → stable S3 key identifier + cluster context
	// for the storage resolver. Mist runtime prefix routes; bare name is
	// the lookup key for both push-live (rare; bare lives only briefly
	// before sidecar reports live+<x>) and mist-native sources.
	var (
		thumbnailKey       string
		thumbTenantID      string
		thumbOriginCluster string
		isLive             bool
		streamInternalName string
	)
	parsed := streamident.Parse(internalName)
	bareName := parsed.Concrete
	bareMistNative := false
	var resolvedStreamID, resolvedTenantID, resolvedOriginCluster string
	if parsed.Kind == streamident.KindBare {
		// Registry fast path: warm-cache mist-native admission saves a
		// Commodore round-trip per thumbnail batch.
		if StreamRegistryInstance != nil {
			if entry, err := StreamRegistryInstance.ResolveSourceByInternalName(context.Background(), bareName); err == nil && entry.IngestMode == IngestMistNative {
				bareMistNative = true
				resolvedStreamID = entry.StreamID
				resolvedTenantID = entry.TenantID
				resolvedOriginCluster = entry.OriginClusterID
			}
		}
		if !bareMistNative && CommodoreClient != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			// Identifier-only read: declare no cluster (see resolveRoutingStreamIdentity).
			resp, err := CommodoreClient.ResolveStreamContext(ctx, "", "", bareName, "")
			cancel()
			if err == nil && resp != nil && resp.GetAdmitted() && resp.GetIngestMode() == "mist_native" {
				bareMistNative = true
				resolvedStreamID = resp.GetStreamId()
				resolvedTenantID = resp.GetTenantId()
				resolvedOriginCluster = resp.GetOriginClusterId()
			}
		}
	}
	switch {
	case parsed.Kind == streamident.KindSourceLive || bareMistNative:
		isLive = true
		streamInternalName = bareName
		// Identity facade layers in-memory state (StreamID + TenantID from
		// PUSH_REWRITE) over the registry/Commodore (origin cluster, which
		// state never carries). The bare-native pre-resolution above
		// already warmed the registry for mist-native names.
		if resolver := identity.Default(); resolver != nil {
			rctx, rcancel := context.WithTimeout(context.Background(), 3*time.Second)
			id, resolveErr := resolver.ResolveStream(rctx, bareName)
			rcancel()
			if resolveErr == nil {
				thumbnailKey = id.StreamID
				thumbTenantID = id.TenantID
				thumbOriginCluster = id.OriginClusterID
			}
		}
		// Merge in the bare-native pre-resolution for any field the facade
		// could not attribute.
		if thumbnailKey == "" {
			thumbnailKey = resolvedStreamID
		}
		if thumbTenantID == "" {
			thumbTenantID = resolvedTenantID
		}
		if thumbOriginCluster == "" {
			thumbOriginCluster = resolvedOriginCluster
		}
		if thumbnailKey == "" {
			logger.WithFields(logging.Fields{
				"stream_name":   internalName,
				"internal_name": bareName,
			}).Warn("Could not resolve internal_name to stream_id for thumbnail upload")
			return
		}
		// Heal the in-memory union so the next consumer's state fast path
		// hits without a registry/Commodore round-trip.
		state.DefaultManager().SetStreamStreamID(bareName, thumbnailKey)
		logger.WithFields(logging.Fields{
			"stream_name":   internalName,
			"internal_name": bareName,
			"stream_id":     thumbnailKey,
		}).Info("Resolved live/mist_native stream_id for thumbnail S3 key")
	case parsed.Kind == streamident.KindArtifactVOD:
		conn := GetDB()
		if conn == nil {
			logger.Warn("DB not available for artifact hash resolution")
			return
		}
		// VOD+: also pull tenant_id and the authoritative storage cluster
		// (storage_cluster_id with origin_cluster_id fallback) so the
		// resolver can pick the right pool. Caller's stream state has no
		// VOD context so the artifact row is the only source.
		row, err := foghorndb.New(conn).ResolveThumbnailVODArtifact(context.Background(), sql.NullString{String: bareName, Valid: true})
		if err != nil {
			logger.WithFields(logging.Fields{
				"stream_name":   internalName,
				"internal_name": bareName,
			}).Warn("Could not resolve internal_name to artifact_hash for thumbnail upload")
			return
		}
		artifactHash, tenantID, authoritativeCluster := row.ArtifactHash, sql.NullString{String: row.TenantID, Valid: row.TenantID != ""}, row.ClusterID
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
	case parsed.Kind == streamident.KindArtifactProcessing:
		conn := GetDB()
		if conn == nil {
			logger.Warn("DB not available for processing thumbnail resolution")
			return
		}
		token := parsed.Concrete
		row, err := foghorndb.New(conn).ResolveThumbnailProcessingArtifact(context.Background(), token)
		if err != nil {
			logger.WithFields(logging.Fields{
				"stream_name":   internalName,
				"artifact_hash": token,
				"error":         err,
			}).Warn("Could not resolve processing+ stream to artifact_hash for thumbnail upload")
			return
		}
		tenantID := sql.NullString{String: row.TenantID, Valid: row.TenantID != ""}
		authoritativeCluster := sql.NullString{String: row.ClusterID, Valid: row.ClusterID != ""}
		artifactType := row.ArtifactType
		thumbnailKey = token
		if tenantID.Valid {
			thumbTenantID = tenantID.String
		}
		if authoritativeCluster.Valid {
			thumbOriginCluster = authoritativeCluster.String
		}
		logger.WithFields(logging.Fields{
			"stream_name":     internalName,
			"artifact_hash":   thumbnailKey,
			"artifact_type":   artifactType,
			"storage_cluster": thumbOriginCluster,
		}).Info("Resolved processing artifact hash for thumbnail S3 key")
	case parsed.Kind == streamident.KindArtifactDVR:
		conn := GetDB()
		if conn == nil {
			logger.Warn("DB not available for DVR thumbnail resolution")
			return
		}
		token := parsed.Concrete
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
		logger.WithField("internal_name", internalName).Warn("Thumbnail upload from unrecognised stream prefix; expected live+, mist_native bare name, vod+, processing+, or dvr+")
		return
	}

	// Authorize the reporting node for the resolved tenant BEFORE minting any overwrite-capable PUT URL: a node
	// not entitled to this tenant must not obtain write URLs for its fixed-key thumbnail objects merely by
	// naming a stream/artifact it does not own (same authority model as relay resolve, nodeMayServeTenant).
	// Fail closed: an unresolved (empty) tenant is denied.
	if !nodeMayServeTenant(nodeID, thumbTenantID) {
		logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"tenant_id":     thumbTenantID,
			"node_id":       nodeID,
		}).Warn("Thumbnail upload denied: reporting node is not authorized for the resolved tenant")
		return
	}

	// Beyond tenant authority, require the reporting node to actually PRODUCE the named resource: a fixed-key,
	// overwrite-capable PUT must not be mintable by a tenant-entitled node (e.g. a tenantless platform-shared
	// edge) merely for NAMING a resource it is not serving/processing. Live: the node must be running the
	// stream; artifacts: it must hold a complete copy (serving) or be its assigned processing node. Fail closed.
	ownCtx, ownCancel := context.WithTimeout(context.Background(), 3*time.Second)
	produces := nodeProducesThumbnailResource(ownCtx, nodeID, parsed.Kind, isLive, streamInternalName, thumbnailKey, thumbTenantID)
	ownCancel()
	if !produces {
		logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"thumbnail_key": thumbnailKey,
			"node_id":       nodeID,
		}).Warn("Thumbnail upload denied: reporting node does not serve/produce the named resource")
		return
	}

	// Resolve the storage destination. LIVE thumbnails are EPHEMERAL derivatives served from the INGEST cell while the
	// stream is live — Commodore builds their URL from active_ingest_cluster_id, so they must be stored on THIS (ingest)
	// cell, not the tenant's official durable cluster. Resolving official for a cross-cell live stream would return a
	// remote destination that federated publication then DROPS, leaving a URL to an object never published. Local mint
	// keeps live publication and its URL on the same cell (active_ingest is authoritative). ARTIFACT thumbnails are
	// durable and still resolve the official cluster (recorded as thumbnail_serving_cluster_id).
	var storageCluster string
	var mintMode storage.StorageMintMode
	if isLive {
		storageCluster, mintMode = localClusterID, storage.StorageMintLocal
	} else {
		storageCluster, mintMode = resolveThumbnailStorageCluster(context.Background(), thumbTenantID, thumbOriginCluster)
	}
	if mintMode == storage.StorageUnavailable {
		logger.WithFields(logging.Fields{
			"internal_name":  internalName,
			"tenant_id":      thumbTenantID,
			"origin_cluster": thumbOriginCluster,
		}).Warn("Storage resolver returned unavailable for thumbnail upload — dropping")
		return
	}
	// Fail closed on a remote (federated) ARTIFACT-thumbnail destination — CONSISTENT with byte storage, not a
	// thumbnail-specific gap: a durable artifact's bytes are themselves fail-closed cross-cell (VOD upload to a remote
	// official returns storage_delegation_unsupported_for_vod; freeze authorizes only a local official), so in a
	// supported single-backend-per-cell deployment an artifact lives on its origin cell and its thumbnail mints there
	// too — this branch is only reached by an unsupported cross-cell topology. Completion verifies + promotes only
	// through the LOCAL S3 client, so a federated attempt would strand on a remote staging key it can never
	// HEAD/promote; this cell never mints one. (Live thumbnails differ — minted locally on the ingest cell by the
	// isLive branch above, so they DO serve cross-cell.) See docs/architecture/durable-media-storage.md.
	if mintMode == storage.StorageMintViaFederation {
		logger.WithFields(logging.Fields{
			"internal_name":   internalName,
			"tenant_id":       thumbTenantID,
			"storage_cluster": storageCluster,
		}).Warn("Artifact thumbnail durable destination is a remote cell; cross-cell artifact thumbnail publication is unsupported — dropping produced bytes (fail closed)")
		return
	}

	allowedThumbnailFiles := map[string]bool{
		"poster.jpg": true,
		"sprite.jpg": true,
		"sprite.vtt": true,
	}
	// Dedupe to the allowlisted files this batch will publish, keeping the first local path per file.
	type thumbFileTarget struct{ fileName, localPath string }
	var targets []thumbFileTarget
	seenFile := map[string]bool{}
	for _, fp := range filePaths {
		fileName := filepath.Base(fp)
		if !allowedThumbnailFiles[fileName] {
			logger.WithField("file_name", fileName).Warn("Rejected thumbnail filename not in allowlist")
			continue
		}
		if seenFile[fileName] {
			continue
		}
		seenFile[fileName] = true
		targets = append(targets, thumbFileTarget{fileName: fileName, localPath: fp})
	}
	if len(targets) == 0 {
		logger.Warn("No allowlisted thumbnail files in upload request")
		return
	}

	// REGISTER-BEFORE-MINT (live only): a live stream's thumbnails are minted on THIS ingest cell's own per-cell
	// Foghorn database, so deletion cleanup can only reach them if this cell is durably recorded as a serving cell
	// FIRST. The registration is fenced by deleted_at IS NULL, so it serializes with DeleteStream on the stream row:
	// register wins → the cell is in the deletion's cleanup fan-out; deletion wins → the fence refuses and we drop here
	// rather than mint an object no cleanup will reach. thumbnailKey is the live stream's Commodore UUID.
	if isLive && !liveThumbnailMintMayProceed(thumbnailKey, thumbTenantID, localClusterID, logger) {
		return
	}

	// Mint the server-owned attempt and persist the assignment + per-file STAGING object rows BEFORE handing
	// any presigned URL. The node uploads to per-attempt staging keys and echoes attempt_id at completion, so
	// the completion binds a Foghorn-ASSIGNED operation. Fail closed on a crypto/rand or claim failure.
	attemptID := mintAttemptID()
	if attemptID == "" {
		logger.Error("Failed to mint thumbnail attempt id; dropping")
		return
	}
	fileNames := make([]string, 0, len(targets))
	for _, t := range targets {
		fileNames = append(fileNames, t.fileName)
	}
	// Presigned-upload lifetime. This same constant bounds the deletion delayed-sweep window (DeterministicCopyWindow),
	// so a PUT issued just before a deletion cannot land after cleanup finalized and strand an object.
	expiry := thumbnailUploadTTL
	claimCtx, claimCancel := context.WithTimeout(context.Background(), 5*time.Second)
	// backend evidence (I2): the mint only reaches here for a StorageMintLocal destination (Unavailable + remote
	// federation are dropped above), so ClaimThumbnailAttempt records durable_backend_local from that invariant —
	// cleanup then routes the sweep local even when the official destination is a locally-backed ALIAS.
	claimed, claimErr := ClaimThumbnailAttempt(claimCtx, GetDB(), attemptID, thumbTenantID, thumbnailKey, nodeID, storageCluster, fileNames, time.Now().Add(expiry))
	claimCancel()
	if claimErr != nil || !claimed {
		logger.WithError(claimErr).WithFields(logging.Fields{
			"internal_name": internalName,
			"thumbnail_key": thumbnailKey,
			"attempt_id":    attemptID,
		}).Warn("Failed to claim thumbnail attempt; dropping (a stuck assignment is swept by the reconciler)")
		return
	}

	resp := &ipcpb.ThumbnailUploadResponse{
		ThumbnailKey: thumbnailKey,
		AttemptId:    attemptID,
		Uploads:      make([]*ipcpb.ThumbnailUploadResponse_PresignedUpload, 0, len(targets)),
	}

	switch mintMode {
	case storage.StorageMintViaFederation:
		// Unreachable: federated destinations are rejected above (fail closed) because completion can only
		// verify/promote locally. Guard defensively so a future resolver change can never silently hand out
		// remote staging URLs that strand.
		logger.WithField("storage_cluster", storageCluster).Error("Federated thumbnail mint reached the mint switch; dropping (must be rejected before claim)")
		return

	default: // StorageMintLocal
		if s3Client == nil {
			logger.Warn("S3 client not configured, ignoring thumbnail upload request")
			return
		}
		// ALL-OR-NOTHING: prepare a presigned URL for EVERY object; a single failure fails the whole attempt so
		// the node never receives a partial assignment (which the completion could never fully verify).
		for _, t := range targets {
			stagingKey := ThumbnailStagingKey(thumbnailKey, attemptID, t.fileName)
			presignedURL, err := s3Client.GeneratePresignedPUT(stagingKey, expiry)
			if err != nil {
				logger.WithFields(logging.Fields{"file_name": t.fileName, "s3_key": stagingKey, "error": err}).
					Error("Failed to presign a thumbnail object; failing the whole attempt (no partial assignment)")
				failCtx, failCancel := context.WithTimeout(context.Background(), 5*time.Second)
				if fErr := FailThumbnailAttempt(failCtx, GetDB(), attemptID); fErr != nil {
					logger.WithError(fErr).WithField("attempt_id", attemptID).Warn("Failed to fail thumbnail attempt after presign error; recovery will sweep on lease expiry")
				}
				failCancel()
				return
			}
			resp.Uploads = append(resp.Uploads, &ipcpb.ThumbnailUploadResponse_PresignedUpload{
				FileName:     t.fileName,
				PresignedUrl: presignedURL,
				S3Key:        stagingKey,
				LocalPath:    t.localPath,
			})
		}
	}

	if len(resp.Uploads) != len(targets) {
		logger.Warn("Not every thumbnail object was presigned; not dispatching a partial assignment")
		return
	}

	msg := &ipcpb.ControlMessage{
		RequestId: requestID,
		SentAt:    timestamppb.Now(),
		Payload:   &ipcpb.ControlMessage_ThumbnailUploadResponse{ThumbnailUploadResponse: resp},
	}
	if err := stream.Send(msg); err != nil {
		logger.WithError(err).Error("Failed to send thumbnail upload response")
	}
}

// processThumbnailUploaded handles the confirmation after Helmsman uploads a thumbnail attempt's files to their
// per-attempt STAGING keys. It binds the confirmation to the server-minted assignment (echoed attempt_id),
// HEAD-verifies + promotes each staged object to its IMMUTABLE version key, then flips the tenant-scoped active
// pointer via the guarded CAS. There is no legacy fixed-key completion: a confirm without an attempt id is
// dropped.
func processThumbnailUploaded(req *ipcpb.ThumbnailUploaded, nodeID string, logger logging.Logger) {
	attemptID := req.GetAttemptId()
	if strings.TrimSpace(attemptID) == "" {
		logger.WithField("thumbnail_key", req.GetThumbnailKey()).
			Warn("Thumbnail confirm without attempt_id; dropping (staged publication requires the server-minted attempt)")
		return
	}
	completeThumbnailPublication(context.Background(), attemptID, req.GetThumbnailKey(), nodeID, logger)
}

// completeThumbnailPublication drives one attempt from verified staging to a published active pointer. Every
// non-terminal exit leaves the attempt for the recovery reconciler (a transient S3/DB error is retried; a
// missing staged object is swept after expiry) — it never publishes a partial or unverified set.
func completeThumbnailPublication(ctx context.Context, attemptID, echoedKey, nodeID string, logger logging.Logger) {
	dbh := GetDB()
	a, objs, found, err := LoadThumbnailAttempt(ctx, dbh, attemptID)
	if err != nil {
		logger.WithError(err).WithField("attempt_id", attemptID).Warn("Thumbnail completion: load attempt failed; leaving for recovery")
		return
	}
	if !found {
		logger.WithField("attempt_id", attemptID).Warn("Thumbnail completion: unknown or expired-swept attempt; dropping")
		return
	}
	// Bind the confirmation to the ASSIGNED node + asset — a node must not complete another node's attempt, and
	// the echoed key must match the assignment. Fail closed on mismatch.
	if a.NodeID != nodeID {
		logger.WithFields(logging.Fields{"attempt_id": attemptID, "assigned_node": a.NodeID, "reporting_node": nodeID}).
			Warn("Thumbnail completion: reporting node is not the assigned node; dropping")
		return
	}
	if strings.TrimSpace(echoedKey) != "" && echoedKey != a.AssetKey {
		logger.WithFields(logging.Fields{"attempt_id": attemptID, "assignment_asset": a.AssetKey, "echoed_key": echoedKey}).
			Warn("Thumbnail completion: echoed thumbnail_key does not match the assignment; dropping")
		return
	}
	switch a.Status {
	case "published":
		return // idempotent: a duplicate confirm for an already-published attempt is a no-op
	case "failed":
		logger.WithField("attempt_id", attemptID).Warn("Thumbnail completion: attempt already failed; dropping")
		return
	}
	finishThumbnailPublication(ctx, dbh, a, objs, nodeID, logger)
}

// finishThumbnailPublication runs the S3 verify -> promote -> publish core for an already-loaded, non-terminal
// attempt: it fences on lease expiry and the parent tombstone, HEAD-verifies + promotes every staged object to
// its immutable version key, then flips the active pointer and runs the best-effort side effects. Shared by the
// node completion and the recovery reconciler (which re-drives a completion whose ThumbnailUploaded was lost, so
// a one-shot VOD thumbnail is not permanently orphaned). nodeID is only best-effort has_thumbnails backfill
// attribution; the publication itself is bound to the server-minted attempt id.
func finishThumbnailPublication(ctx context.Context, dbh *sql.DB, a ThumbnailAssignment, objs []ThumbnailObject, nodeID string, logger logging.Logger) {
	attemptID := a.AttemptID
	// BOUND the S3 verify/promote/settle work to well under the candidate-cleanup grace (thumbnailCompletionDeadline
	// << thumbnailCandidateCleanupGrace). This does NOT prove the object store cannot land a CopyObject after our
	// context is cancelled — a provider may still complete it server-side — so it is not an absolute fence. What it
	// DOES do: (1) callers pass context.Background(), so without a bound a single stuck op could run for hours; the
	// bound aborts it and leaves the attempt for recovery, and (2) the residual risk is only a per-token ORPHAN
	// (`v/{token}/…` is private to this completion, so a stale late copy can never overwrite the live winner — no
	// data corruption). Such an orphan is reclaimed by the grace-delayed candidate cleanup, and the reconciler's
	// prefix-delete on asset deletion is the backstop. Corruption-safe; leak-bounded, not leak-proof.
	ctx, cancel := context.WithTimeout(ctx, thumbnailCompletionDeadline)
	defer cancel()
	// EXPIRY FENCE: refuse to complete an attempt past its lease. It stays in a recovery-eligible state, so the
	// reconciler fails + sweeps it. Because recovery only fails EXPIRED attempts, a non-expired attempt promoted
	// below can never be concurrently failed — closing the promote-vs-fail leak for the common path.
	if !a.Expiry.After(time.Now()) {
		logger.WithField("attempt_id", attemptID).Warn("Thumbnail completion: attempt lease expired; dropping (recovery will fail it)")
		return
	}
	// PARENT-TOMBSTONE FENCE: never promote a thumbnail whose parent artifact is being purged. Purge marks the
	// artifact 'deleted' well before it sweeps (retention age), so a completion arriving after that must NOT
	// write new version objects into a prefix the purge will delete. Drop and enqueue the staged objects for
	// cleanup. Live streams (asset_key = stream_id) have no artifact row and are never affected.
	if deleted, tErr := parentArtifactTombstoned(ctx, GetDB(), a.AssetKey); tErr != nil {
		logger.WithError(tErr).WithField("attempt_id", attemptID).Warn("Thumbnail completion: parent-tombstone check failed; leaving attempt for recovery")
		return
	} else if deleted {
		logger.WithFields(logging.Fields{"attempt_id": attemptID, "asset_key": a.AssetKey}).
			Info("Thumbnail completion: parent artifact is tombstoned; dropping and failing the attempt")
		failCtx, failCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if fErr := FailThumbnailAttempt(failCtx, GetDB(), attemptID); fErr != nil {
			logger.WithError(fErr).WithField("attempt_id", attemptID).Warn("Failed to fail tombstoned thumbnail attempt; recovery will sweep on lease expiry")
		}
		failCancel()
		return
	}
	// PUBLICATION LEASE: acquire the single-flight fence BEFORE any S3 HEAD/promote. This makes the promotion of
	// this attempt's version objects single-flight (a concurrent completion of the same attempt cannot double-copy
	// and overwrite the immutable version key), and it blocks the recovery fail-sweep from expiring the attempt
	// while this promotion is in flight (failAndSweep honors publish_leased_until). If we cannot acquire it —
	// another completion holds it, or the attempt is terminal/expired — leave it for that holder / recovery.
	publishToken, lErr := AcquireThumbnailPublishLease(ctx, GetDB(), attemptID, 2*time.Minute)
	if lErr != nil {
		logger.WithError(lErr).WithField("attempt_id", attemptID).Warn("Thumbnail completion: acquire publication lease failed; leaving for recovery")
		return
	}
	if publishToken == "" {
		logger.WithField("attempt_id", attemptID).Debug("Thumbnail completion: publication lease held by another completion (or attempt terminal/expired); leaving")
		return
	}
	if s3Client == nil {
		// Local verify/promote needs the S3 client. A federated destination (official cluster on another cell)
		// is not completed here — its staged objects live on another cell's S3, which this cell cannot verify.
		logger.WithField("attempt_id", attemptID).Warn("Thumbnail completion: no local S3 client; leaving attempt for recovery")
		return
	}

	// RECORD CLEANUP BEFORE PROMOTION: enqueue this holder's per-token CANDIDATE keys (`v/{token}/…`) for deletion
	// BEFORE copying any bytes there. If this completion then loses (a peer re-acquired the lease), fails, or
	// crashes between promote and publish, its private candidate is already queued and the shared StagingCleanupJob
	// reclaims it. The WINNER de-registers exactly these keys inside its publish CAS (they become the live version),
	// so only a non-winning holder's candidate stays queued — and a stale holder's key is distinct per token, so it
	// can never dequeue or overwrite the winner's object.
	candidateKeys := make([]string, 0, len(objs))
	for _, o := range objs {
		candidateKeys = append(candidateKeys, ThumbnailVersionKey(a.AssetKey, publishToken, o.FileName))
	}
	// GRACE-DELAYED so the shared cleanup worker cannot claim + settle the row before promotion creates the object
	// (a delete of the absent key would drop the row and orphan the promoted object). The winner de-registers these
	// keys inside its publish CAS well within the grace; a loser/crash fires after the grace, object present.
	if cErr := EnqueueThumbnailCleanupDeferred(ctx, GetDB(), candidateKeys, thumbnailCandidateCleanupGrace); cErr != nil {
		logger.WithError(cErr).WithField("attempt_id", attemptID).Warn("Thumbnail completion: pre-promotion candidate enqueue failed; leaving for recovery")
		return
	}

	// Verify + promote EVERY object to its per-token CANDIDATE key (`v/{token}/…`). All must succeed to publish — a
	// missing/empty staged object or a transient error returns and leaves the attempt for retry/sweep (no partial
	// publish). No leak-cleanup is needed on any exit below: this holder's candidate keys were enqueued for deletion
	// BEFORE the first promote, so a loss/failure/crash here leaves them queued and the StagingCleanupJob reclaims
	// them; the winning completion de-registers exactly these keys inside its publish CAS. Because the candidate
	// segment is the holder's private token, a stale holder can only ever write (and later have cleaned) its OWN
	// objects — it can never overwrite or dequeue the winner's.
	for _, o := range objs {
		exists, size, etag, hErr := s3Client.HeadObjectInfo(ctx, o.StagingKey)
		if hErr != nil {
			logger.WithError(hErr).WithFields(logging.Fields{"attempt_id": attemptID, "staging_key": o.StagingKey}).
				Warn("Thumbnail completion: staging HEAD failed (transient); leaving for retry")
			return
		}
		if !exists || size <= 0 || strings.TrimSpace(etag) == "" {
			logger.WithFields(logging.Fields{"attempt_id": attemptID, "staging_key": o.StagingKey, "exists": exists, "size": size}).
				Warn("Thumbnail completion: staged object missing/empty; leaving for recovery")
			return
		}
		versionKey := ThumbnailVersionKey(a.AssetKey, publishToken, o.FileName)
		if pErr := s3Client.PromoteObject(ctx, o.StagingKey, versionKey, etag); pErr != nil {
			logger.WithError(pErr).WithFields(logging.Fields{"attempt_id": attemptID, "version_key": versionKey}).
				Warn("Thumbnail completion: promote to candidate key failed (transient); leaving for retry")
			return
		}
		moved, mErr := MarkThumbnailObjectVerifiedToken(ctx, dbh, attemptID, o.FileName, versionKey, etag, size, publishToken)
		if mErr != nil {
			logger.WithError(mErr).WithField("attempt_id", attemptID).Warn("Thumbnail completion: recording verified object failed; leaving for recovery")
			return
		}
		if !moved {
			// The assignment is no longer fenced under THIS token (a peer re-acquired the lease, it expired, or the
			// asset is gone). Our promoted candidate is our OWN private key — re-arm it due-now (object exists) for
			// prompt reclamation instead of waiting out the grace (which still reclaims it if this fails).
			if reErr := EnqueueThumbnailCleanup(ctx, dbh, candidateKeys); reErr != nil {
				logger.WithError(reErr).WithField("attempt_id", attemptID).Debug("Thumbnail completion: candidate re-arm failed; grace-delayed enqueue still reclaims")
			}
			logger.WithField("attempt_id", attemptID).Warn("Thumbnail completion: assignment no longer fenced under this lease; leaving for recovery")
			return
		}
	}

	// Enter the durable publishing state (token-fenced), then CAS the active pointer to this token's candidate. Any
	// non-winning exit leaves the candidate queued (enqueued pre-promotion) for reclamation — no cleanup here.
	entered, eErr := EnterThumbnailPublishingToken(ctx, dbh, attemptID, publishToken)
	if eErr != nil {
		logger.WithError(eErr).WithField("attempt_id", attemptID).Warn("Thumbnail completion: enter-publishing failed; leaving for retry")
		return
	}
	if !entered {
		// Object exists but this holder cannot publish (concurrent completion / expiry / lost lease). Re-arm its
		// private candidate due-now for prompt reclamation (the grace-delayed enqueue still reclaims if this fails).
		if reErr := EnqueueThumbnailCleanup(ctx, dbh, candidateKeys); reErr != nil {
			logger.WithError(reErr).WithField("attempt_id", attemptID).Debug("Thumbnail completion: candidate re-arm failed; grace-delayed enqueue still reclaims")
		}
		logger.WithField("attempt_id", attemptID).Warn("Thumbnail completion: could not enter publishing (concurrent completion/expiry/lost lease); leaving for recovery")
		return
	}
	activated, pErr := PublishThumbnailAttemptToken(ctx, dbh, attemptID, publishToken)
	if pErr != nil {
		logger.WithError(pErr).WithField("attempt_id", attemptID).Warn("Thumbnail completion: publish CAS failed; leaving for retry")
		return
	}
	logger.WithFields(logging.Fields{"attempt_id": attemptID, "asset_key": a.AssetKey, "token": publishToken, "activated": activated}).
		Info("Thumbnail attempt published")

	// The staging-cleanup enqueue is committed ATOMICALLY with the pointer CAS inside PublishThumbnailAttempt (so a
	// crash after the CAS can never skip it). has_thumbnails is deferred to the fenced projection below. Here we run
	// the winner's projection + best-effort side effects: the origin-cluster backfill / Commodore projection (via
	// markArtifactHasThumbnails, idempotent) and the Chandler cache invalidation (Chandler otherwise self-heals via
	// its short object-cache TTL). A loser that didn't activate leaves the live version's side effects untouched.
	if activated {
		fileNames := make([]string, 0, len(objs))
		for _, o := range objs {
			fileNames = append(fileNames, o.FileName)
		}
		// DURABLE PROJECTION (winner only), EVENTUAL. The publish CAS marked the attempt 'published' but left it
		// UNPROJECTED. projectAndMarkThumbnailFromToken CLAIMS (short lock), COPIES the winning version objects to the
		// DETERMINISTIC served keys (thumbnails/{asset}/{file}) OUTSIDE any lock, then CAS-SETTLES: it stamps projected +
		// exposes has_thumbnails only for the still-active winner and arms a one-shot reassert. A PostgreSQL lock cannot
		// strictly serialize the S3 copy (its destination write is unconditional and an accepted copy can complete after
		// the context is cancelled), so a loser's straggler overwrite is corrected by the winner's reassert (and a
		// resurrection by the delayed delete sweep) when it lands within the copy window — the contract is eventual,
		// with a straggler past the assumed provider tail an accepted residual risk. See docs/architecture/thumbnails.md.
		if marked, mErr := projectAndMarkThumbnailFromToken(ctx, dbh, s3Client, attemptID, a.AssetKey, a.TenantID, a.DestinationCluster, publishToken, fileNames, logger); mErr != nil {
			logger.WithError(mErr).WithField("attempt_id", attemptID).Warn("Thumbnail projection failed; leaving unprojected for recovery")
		} else if marked {
			// Node-authorized convergence: has_thumbnails is already flipped by the settle; this backfills a MISSING
			// (both-NULL) artifact origin cluster from the reporting node's own cluster — legitimate provenance for a
			// live upload. It does NOT stamp the thumbnail's official STORAGE destination onto origin (that would corrupt
			// provenance); recovery, which has no reporting node, simply skips this.
			markArtifactHasThumbnails(a.AssetKey, nodeID, logger)
		}
		// Chandler's invalidation evicts the deterministic thumbnails/{asset}/{file} key so the next request re-pulls
		// the freshly projected object. Best-effort (outside any tx) — Chandler self-heals via its cache TTL.
		invalidateChandlerThumbnailCache(a.AssetKey, fileNames, logger)
	}
}

const (
	deterministicPromoteRetries = 3
	deterministicPromoteBackoff = 200 * time.Millisecond
)

// copyThumbnailObjectsToDeterministic copies each object's VersionKey → its deterministic served key
// (thumbnails/{asset}/{file}) with a bounded per-object retry. It is PURE S3 and runs OUTSIDE any transaction/lock (a
// PostgreSQL lock cannot serialize this network copy), between the CLAIM and SETTLE fences of projectAndMarkThumbnail.
// The destination write is unconditional, so a stale straggler is possible; the winner's reassert + the delayed delete
// sweep converge it. Returns true iff EVERY object was copied (an empty or absent source, or a retry-exhausted transport
// error, returns false → the caller commits nothing and recovery re-drives).
func copyThumbnailObjectsToDeterministic(ctx context.Context, client S3ClientInterface, assetKey string, objs []ThumbnailObject, logger logging.Logger) bool {
	if client == nil {
		return false
	}
	allCopied := true
	for _, o := range objs {
		if strings.TrimSpace(o.VersionKey) == "" {
			allCopied = false // no source recorded yet → leave for recovery once verification has landed it
			continue
		}
		detKey := ThumbnailDeterministicKey(assetKey, o.FileName)
		var lastErr error
		copied := false
		absent := false
		for attempt := 0; attempt < deterministicPromoteRetries; attempt++ {
			exists, _, etag, hErr := client.HeadObjectInfo(ctx, o.VersionKey)
			if hErr != nil {
				lastErr = hErr // transient — retry
			} else if !exists || strings.TrimSpace(etag) == "" {
				absent = true // authoritative absence — the source object isn't there yet; do not retry
				break
			} else if pErr := client.PromoteObject(ctx, o.VersionKey, detKey, etag); pErr != nil {
				lastErr = pErr // transient — retry
			} else {
				copied = true
				lastErr = nil
				break // success
			}
			if attempt < deterministicPromoteRetries-1 {
				select {
				case <-ctx.Done():
					lastErr = ctx.Err()
					attempt = deterministicPromoteRetries // stop
				case <-time.After(deterministicPromoteBackoff):
				}
			}
		}
		if !copied {
			allCopied = false
			if absent {
				logger.WithField("version_key", o.VersionKey).Debug("Deterministic projection: source version object not present yet; leaving unprojected")
			} else if lastErr != nil {
				logger.WithError(lastErr).WithField("deterministic_key", detKey).
					Warn("Deterministic thumbnail projection failed after retries; leaving unprojected for recovery to re-drive")
			}
		}
	}
	return allCopied
}

// projectAndMarkThumbnailFromToken is the publish-path entry to the fenced projection: it computes each source
// version key from the winning publishToken (ThumbnailVersionKey) — the just-loaded attempt does not carry the
// version_key in memory (verification writes it to the DB, not to the in-memory objects) — then runs the fenced
// project+mark. Returns marked=true only when every object was copied AND the projection stamp committed.
func projectAndMarkThumbnailFromToken(ctx context.Context, dbh *sql.DB, client S3ClientInterface, attemptID, assetKey, tenantID, servingCluster, publishToken string, fileNames []string, logger logging.Logger) (bool, error) {
	return projectAndMarkThumbnail(ctx, dbh, client, attemptID, assetKey, tenantID, servingCluster, thumbnailObjectsFromToken(assetKey, publishToken, fileNames), logger)
}

// thumbnailObjectsFromToken builds the projection source objects for the publish path: each file's source is its
// per-token version key COMPUTED from the winning publishToken (ThumbnailVersionKey), NOT a possibly-stale/empty
// in-memory version_key — the just-loaded attempt does not carry it (verification writes it to the DB, not to the
// in-memory objects). Recovery, by contrast, loads the persisted version_key via LoadThumbnailAttempt.
func thumbnailObjectsFromToken(assetKey, publishToken string, fileNames []string) []ThumbnailObject {
	objs := make([]ThumbnailObject, 0, len(fileNames))
	for _, file := range fileNames {
		objs = append(objs, ThumbnailObject{FileName: file, VersionKey: ThumbnailVersionKey(assetKey, publishToken, file)})
	}
	return objs
}

// ReprojectPublishedThumbnailAttempt is the recovery re-drive for a published-but-unprojected attempt: it loads the
// attempt — whose objects carry their DB-persisted version_key (written at verification) — and runs the fenced
// project+mark. Returns progressed=true ONLY when the projection actually stamped this pass (real progress), so the
// leased recovery worker settles a genuinely-projected attempt and BACKS OFF one whose source is still absent or whose
// copy failed (poison isolation) rather than re-selecting it at the head every tick. A superseded/already-projected/
// tombstoned attempt returns progressed=false with no error (the fence rejected it). No local S3 client → nothing to
// project (not progress).
func ReprojectPublishedThumbnailAttempt(ctx context.Context, attemptID string) (bool, error) {
	dbh := GetDB()
	a, objs, found, err := LoadThumbnailAttempt(ctx, dbh, attemptID)
	if err != nil {
		return false, err
	}
	if !found || a.Status != "published" || s3Client == nil {
		return false, nil
	}
	logger := logging.NewLoggerWithService("foghorn-thumbnail-recovery")
	// Recovery records the same AUTHORITATIVE thumbnail_serving_cluster_id as the immediate path — the winning
	// assignment's persisted destination_cluster (the official-durable cluster the thumbnail was projected to). It never
	// touches origin/storage cluster (provenance / byte placement), which a dedicated serving-cluster field replaces.
	return projectAndMarkThumbnail(ctx, dbh, s3Client, attemptID, a.AssetKey, a.TenantID, a.DestinationCluster, objs, logger)
}

// ReassertThumbnailProjection is the eventual-convergence step: past the max-copy window, the CURRENT winner re-copies
// its version objects to the deterministic served key ONCE, correcting a straggler overwrite from an earlier loser
// whose accepted copy completed within that window, then clears the reassert clock. A straggler that lands AFTER the
// window (i.e. the provider tail exceeded projectionProviderAmbiguityWindow) is NOT corrected by this one-shot pass —
// the accepted, rare cosmetic residual risk documented there. If the asset was superseded or has gone
// terminal/tombstoned, no re-copy is done — the clock is simply cleared (one-shot). Returns progressed=true when
// the clock was cleared this pass (converged) so the recovery worker settles it; a failed re-copy leaves the clock set
// for a later retry (progressed=false → backoff). No local S3 client → nothing to do.
func ReassertThumbnailProjection(ctx context.Context, attemptID string) (bool, error) {
	dbh := GetDB()
	if dbh == nil || s3Client == nil {
		return false, nil
	}
	a, objs, found, err := LoadThumbnailAttempt(ctx, dbh, attemptID)
	if err != nil {
		return false, err
	}
	if !found {
		// Row gone (e.g. GC'd / deleted): the clock went with it. Nothing to re-copy; treat as converged.
		return true, nil
	}
	logger := logging.NewLoggerWithService("foghorn-thumbnail-reassert")
	// Re-copy ONLY while still the live, projected winner. gateThumbnailProjection(allowProjected=true) rejects a
	// superseded/tombstoned/terminal asset — then we skip the copy and just clear the clock (nothing to re-assert).
	live, gErr := gateThumbnailProjection(ctx, dbh, attemptID, a.AssetKey, true)
	if gErr != nil {
		return false, gErr
	}
	if live {
		if !copyThumbnailObjectsToDeterministic(ctx, s3Client, a.AssetKey, objs, logger) {
			return false, nil // transient copy failure → leave the clock set for a later pass
		}
	}
	if cErr := clearThumbnailReassert(ctx, dbh, attemptID); cErr != nil {
		return false, cErr
	}
	return true, nil
}

// CompleteThumbnailAttemptForRecovery re-drives a completion whose ThumbnailUploaded confirmation was lost (a
// dropped send, or a Foghorn crash after the node uploaded but before the attempt reached 'publishing'). The
// recovery reconciler invokes it for non-expired attempts stuck in a pre-publishing state past a staleness
// threshold: it re-runs the SAME idempotent verify -> promote -> publish core against the staged objects (HEAD
// on a not-yet-uploaded staging object simply leaves the attempt for the next pass). Without it, a one-shot VOD
// thumbnail whose completion was lost would be failed + swept at lease expiry and lost. Bound to the assigned
// node for best-effort backfill attribution only.
//
// Returns progressed=true ONLY when this pass drove the attempt to a TERMINAL state (published or settled-failed)
// — so the reconciler counts real completions, not attempts still stuck (a not-yet-uploaded / poison row leaves
// the attempt unchanged and returns false, and must not be reported as completed).
func CompleteThumbnailAttemptForRecovery(ctx context.Context, attemptID string, logger logging.Logger) (progressed bool, err error) {
	dbh := GetDB()
	a, objs, found, lErr := LoadThumbnailAttempt(ctx, dbh, attemptID)
	if lErr != nil {
		return false, lErr
	}
	if !found {
		return false, nil
	}
	switch a.Status {
	case "published", "failed":
		return false, nil // already terminal before this pass; not progress WE made
	}
	finishThumbnailPublication(ctx, dbh, a, objs, a.NodeID, logger)
	// finishThumbnailPublication reports failures only via logs; re-read to learn whether the attempt actually
	// reached a terminal state this pass (published or settled-failed) vs was left stuck for retry.
	after, _, ok, aErr := LoadThumbnailAttempt(ctx, dbh, attemptID)
	if aErr != nil {
		return false, aErr
	}
	if !ok {
		return true, nil // row gone (e.g. GC'd after publish) → it progressed
	}
	return after.Status == "published" || after.Status == "failed", nil
}

type chandlerInvalidateRequest struct {
	AssetKey string   `json:"assetKey"`
	Files    []string `json:"files"`
}

// canonicalThumbnailFiles is the fixed allowlist of thumbnail files an asset owns (mirrors Chandler's allowlist).
var canonicalThumbnailFiles = []string{"poster.jpg", "sprite.jpg", "sprite.vtt"}

// PushChandlerThumbnailInvalidate best-effort evicts a deleted asset's cached thumbnail objects from the in-cell
// Chandler so a warm cache does not keep serving bytes the cleanup sweep is removing. Chandler is a dumb cache with
// no delete/GONE semantics — the durable authority is the stream_cleanup_obligation tombstone (which fences
// re-publication) plus the prefix sweep that removes the objects; this only shrinks the cached-after-delete window
// below the cache TTL. Safe on any path that tombstones an asset (live-stream deletion, artifact soft-delete).
func PushChandlerThumbnailInvalidate(assetKey string, logger logging.Logger) {
	postChandlerInvalidate(chandlerInvalidateRequest{
		AssetKey: assetKey,
		Files:    canonicalThumbnailFiles,
	}, logger)
}

func invalidateChandlerThumbnailCache(thumbnailKey string, s3Keys []string, logger logging.Logger) {
	if thumbnailKey == "" || len(s3Keys) == 0 {
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
	postChandlerInvalidate(chandlerInvalidateRequest{
		AssetKey: thumbnailKey,
		Files:    files,
	}, logger)
}

// postChandlerInvalidate fans the invalidation request out to every configured in-cell Chandler, best-effort:
// missing token/URL or any transport/non-2xx failure is logged and skipped, never surfaced. Shared by the
// publish push (invalidateChandlerThumbnailCache) and the delete-eviction push (PushChandlerThumbnailInvalidate).
func postChandlerInvalidate(req chandlerInvalidateRequest, logger logging.Logger) {
	if req.AssetKey == "" || len(req.Files) == 0 {
		return
	}
	serviceToken := strings.TrimSpace(os.Getenv("SERVICE_TOKEN"))
	if serviceToken == "" {
		logger.Warn("SERVICE_TOKEN missing, skipping Chandler thumbnail cache invalidation")
		return
	}
	baseURLs := getChandlerInternalBaseURLs()
	if len(baseURLs) == 0 {
		logger.Warn("Chandler URL missing, skipping thumbnail cache invalidation")
		return
	}
	thumbnailKey := req.AssetKey
	files := req.Files
	body, err := json.Marshal(req)
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

// markArtifactHasThumbnails flips has_thumbnails on the first confirmed
// artifact thumbnail upload and projects that state to Commodore once.
// nodeID is the uploading node; it backstops cluster attribution when the
// artifact row carries no cluster (e.g. clips of bare mist_native sources
// created before cluster stamping was made robust).
// markArtifactHasThumbnails returns whether the confirmation was AUTHORIZED (and therefore whether the caller
// may run the Chandler cache invalidation): true only for a tenant-entitled confirmation on an existing
// artifact row; false when there is no artifact row to bind (an unverifiable key — fail closed), when the
// reporting node is not entitled to the artifact's tenant, or on a DB error.
func markArtifactHasThumbnails(artifactHash, nodeID string, logger logging.Logger) bool {
	conn := GetDB()
	if conn == nil {
		logger.Warn("DB not available, cannot mark artifact thumbnails")
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Resolve the artifact's tenant + cluster context BEFORE any write so the reporting node can be
	// authorized. A key with no artifact row (e.g. a live stream_id thumbnail) has nothing to mark and no
	// tenant to bind — return quietly.
	row, selErr := foghorndb.New(conn).GetArtifactThumbnailMarkContext(ctx, artifactHash)
	if errors.Is(selErr, sql.ErrNoRows) {
		// No artifact row for this key: there is nothing to bind authorization against, so FAIL CLOSED —
		// an unverifiable key must not flip state or trigger a cache side effect. (Live stream thumbnails
		// carry no artifact row; their cache freshness relies on Chandler's TTL, and a per-stream serve
		// capability is the workload-identity RFC.)
		return false
	}
	tenantID, storageClusterID, originClusterID, alreadyMarked := row.TenantID, row.StorageClusterID, row.OriginClusterID, row.HasThumbnails
	if selErr != nil {
		logger.WithFields(logging.Fields{
			"artifact_hash": artifactHash,
			"error":         selErr,
		}).Error("Failed to resolve artifact for thumbnail marking")
		return false
	}
	// Bind the confirmation to the reporting node's tenant entitlement (same single-provider model as the PUT
	// minting path). A node not entitled to this tenant must not flip has_thumbnails or trigger the cache
	// side effects. FAIL CLOSED: a missing/empty artifact tenant is denied (an unbound tenant is not a pass),
	// as is any node the tenant check rejects.
	if tenantID == "" || !nodeMayServeTenant(nodeID, tenantID) {
		logger.WithFields(logging.Fields{
			"artifact_hash": artifactHash,
			"tenant_id":     tenantID,
			"node_id":       nodeID,
		}).Warn("Thumbnail confirmation denied: reporting node is not authorized for the artifact tenant")
		return false
	}

	if !alreadyMarked {
		if err := foghorndb.New(conn).MarkArtifactThumbnailPresent(ctx, artifactHash); err != nil {
			logger.WithFields(logging.Fields{
				"artifact_hash": artifactHash,
				"error":         err,
			}).Error("Failed to mark artifact has_thumbnails")
			return false
		}
		logger.WithField("artifact_hash", artifactHash).Info("Artifact thumbnails marked as uploaded")
	}

	cluster := storageClusterID.String
	if cluster == "" {
		cluster = originClusterID.String
	}
	if cluster == "" {
		// The artifact row never got a cluster stamped (stream-state
		// enrichment misses for bare mist_native sources). The uploading
		// node's cluster is ground truth here; backfill the row so freeze
		// resolution and playback URL construction heal too.
		if ns := state.DefaultManager().GetNodeState(nodeID); ns != nil && ns.ClusterID != "" {
			cluster = ns.ClusterID
		}
		if cluster == "" {
			cluster = localClusterID
		}
		if cluster != "" {
			if dbErr := foghorndb.New(conn).BackfillArtifactOriginCluster(ctx, foghorndb.BackfillArtifactOriginClusterParams{ArtifactHash: artifactHash, OriginClusterID: sql.NullString{String: cluster, Valid: true}}); dbErr != nil {
				logger.WithError(dbErr).WithField("artifact_hash", artifactHash).Warn("Failed to backfill artifact origin cluster")
			} else {
				logger.WithFields(logging.Fields{
					"artifact_hash": artifactHash,
					"cluster_id":    cluster,
				}).Info("Backfilled artifact origin cluster from uploading node")
			}
		}
	}
	// has_thumbnails and the origin/storage cluster are now on the authoritative
	// foghorn.artifacts row (written above); the artifact reconciler projects them onto the
	// Commodore catalog with its revision guard, so no unguarded direct projection is done here.
	return true
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

// explicitLocalChandlerConfigured reports whether this deployment names ONE explicit local Chandler origin via
// CHANDLER_BASE_URL — the single-cluster / local-nginx model where every asset URL is served from this cell. It is the
// only positive signal that the local Chandler is the correct serving origin for a stream whose ingest cluster is
// unknown; a managed multi-cell deployment leaves it empty (per-cluster origins are derived from Quartermaster), where
// an unresolved cluster must NOT silently resolve to this cell.
func explicitLocalChandlerConfigured() bool {
	return strings.TrimSpace(os.Getenv("CHANDLER_BASE_URL")) != ""
}

// chandlerPerClusterCache caches per-cluster Chandler asset origins resolved
// via Quartermaster. 5-minute TTL per cluster. The cache is keyed by
// cluster_id; empty cluster_id and resolution failures are NOT cached so
// transient Quartermaster outages don't poison subsequent lookups.
var (
	chandlerPerClusterMu    sync.RWMutex
	chandlerPerClusterCache = map[string]chandlerCachedURL{}
)

type chandlerCachedURL struct {
	url        string
	resolvedAt time.Time
}

const chandlerPerClusterTTL = 5 * time.Minute

// getChandlerBaseURLForCluster returns the public Chandler asset origin for
// the named cluster. Local/single-node deployments set CHANDLER_BASE_URL so
// asset URLs stay on the nginx /assets route; managed deployments can derive a
// per-cluster Chandler origin from Quartermaster metadata. Per-cluster cache
// state is independent of `resolvedChandlerBaseURL`, so a per-cluster lookup
// never mutates the platform-level Chandler URL that other callers depend on.
//
// Returns "" if the cluster ID is empty, no cluster lookup is configured, the
// Quartermaster lookup fails, or the cluster has no slug/base-domain.
func getChandlerBaseURLForCluster(clusterID string) string {
	if explicit := strings.TrimSpace(os.Getenv("CHANDLER_BASE_URL")); explicit != "" {
		return strings.TrimRight(explicit, "/")
	}

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
	baseDomain := pkgdns.NormalizeDomainScope(cluster.GetBaseUrl())
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
