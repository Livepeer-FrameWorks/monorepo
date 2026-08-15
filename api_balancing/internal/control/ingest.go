package control

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/state"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"

	"google.golang.org/grpc/codes"
)

var localIngestAdmission struct {
	sync.Mutex
	inflight   int
	connecting int
}

var ErrLocalIngestCapacity = errors.New("local ingest placement-renewal capacity exhausted")

// ReserveLocalIngestAdmission enforces the publisher count one Foghorn can renew inside the shared
// placement lease. The in-flight count closes concurrent admission bursts between the live snapshot
// and source projection. Call release on every return path.
func ReserveLocalIngestAdmission(limit int) (release func(), err error) {
	if limit <= 0 {
		return func() {}, ErrLocalIngestCapacity
	}
	localIngestAdmission.Lock()
	live := localSourceProjectionCount()
	if live+localIngestAdmission.inflight+localIngestAdmission.connecting >= limit {
		localIngestAdmission.Unlock()
		return func() {}, ErrLocalIngestCapacity
	}
	localIngestAdmission.inflight++
	localIngestAdmission.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			localIngestAdmission.Lock()
			localIngestAdmission.inflight--
			localIngestAdmission.Unlock()
		})
	}, nil
}

// reserveLocalIngestNodeConnection keeps control-connection failover from moving more live
// publishers onto this Foghorn than its placement job can renew. Publishers already owned by a
// local connection are part of live; publishers arriving with a node connected elsewhere are held
// in connecting until the new connection becomes dispatch-visible.
func reserveLocalIngestNodeConnection(nodeID string, limit int) (release func(), err error) {
	if strings.TrimSpace(nodeID) == "" || limit <= 0 {
		return func() {}, ErrLocalIngestCapacity
	}
	localIngestAdmission.Lock()
	joining := 0
	if _, alreadyLocal := currentNodeSession(nodeID); !alreadyLocal {
		joining = sourceProjectionCountForNode(nodeID)
	}
	live := localSourceProjectionCount()
	if live+localIngestAdmission.inflight+localIngestAdmission.connecting+joining > limit {
		localIngestAdmission.Unlock()
		return func() {}, ErrLocalIngestCapacity
	}
	localIngestAdmission.connecting += joining
	localIngestAdmission.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			localIngestAdmission.Lock()
			localIngestAdmission.connecting -= joining
			localIngestAdmission.Unlock()
		})
	}, nil
}

func sourceProjectionCountForNode(nodeID string) int {
	registry := StreamRegistryInstance
	if registry == nil || strings.TrimSpace(nodeID) == "" {
		return 0
	}
	count := 0
	for _, entry := range registry.Snapshot() {
		for _, loc := range entry.Locations {
			if loc.SourceActive && loc.OwnerNodeID == nodeID {
				count++
				break
			}
		}
	}
	return count
}

// localSourceProjectionCount counts active publishers whose Helmsman control connection is owned by
// this Foghorn. The registry is replicated, so connection ownership is the partition that makes the
// renewal ceiling per replica. A newly accepted publisher is covered by the in-flight reservation
// until its source projection becomes visible here.
func localSourceProjectionCount() int {
	registry := StreamRegistryInstance
	if registry == nil {
		return 0
	}
	count := 0
	for _, entry := range registry.Snapshot() {
		for _, loc := range entry.Locations {
			if !loc.SourceActive || strings.TrimSpace(loc.OwnerNodeID) == "" {
				continue
			}
			if _, connected := currentNodeSession(loc.OwnerNodeID); connected {
				count++
				break
			}
		}
	}
	return count
}

// Ingest ports are platform-wide, not per-node: no registration message
// advertises them, and MistServer pins TSSRT to 8889 across the fleet. Names
// match the ones the gateway already resolves infrastructure config with.
func ingestRTMPPort() int { return config.GetEnvInt("STREAMING_RTMP_PORT", 1935) }
func ingestSRTPort() int  { return config.GetEnvInt("STREAMING_SRT_PORT", 8889) }

// maxIngestEndpoints is how many usable endpoints a response carries
// (primary + fallbacks).
const maxIngestEndpoints = 5

// IngestDependencies carries what ingest endpoint resolution needs, mirroring
// PlaybackDependencies on the viewer side.
type IngestDependencies struct {
	LB     *balancer.LoadBalancer
	GeoLat float64
	GeoLon float64
}

// IngestDenial is a refused ingest resolution, carrying the status vocabulary
// for both transports so HTTP and gRPC cannot drift apart.
type IngestDenial struct {
	HTTPStatus int
	Code       string
	GRPCCode   codes.Code
	Message    string
}

func (d *IngestDenial) Error() string { return d.Code + ": " + d.Message }

// EvaluateIngestAdmission decides whether a resolved stream context may be
// published to. It reads the whole response rather than the rejection enum
// alone, because admission facts arrive in three shapes: a nil response, the
// ingest mode, and the enum.
//
// Order matters. Pull-mode is checked before the admitted short-circuit: a
// pull stream is legitimately admitted (Commodore admits it for playback and
// materialization) yet must never accept a push. Returns nil when admitted.
func EvaluateIngestAdmission(resp *commodorepb.ResolveStreamContextResponse) *IngestDenial {
	if resp == nil {
		return &IngestDenial{
			HTTPStatus: 403,
			Code:       "INGEST_DENIED",
			GRPCCode:   codes.PermissionDenied,
			Message:    "ingest denied",
		}
	}

	if strings.EqualFold(strings.TrimSpace(resp.GetIngestMode()), "pull") {
		return &IngestDenial{
			HTTPStatus: 409,
			Code:       "PULL_MODE_STREAM",
			GRPCCode:   codes.FailedPrecondition,
			Message:    "pull streams do not accept push ingest",
		}
	}

	if resp.GetAdmitted() {
		return nil
	}

	message := strings.TrimSpace(resp.GetAdmissionReason())
	denial := func(httpStatus int, code string, grpcCode codes.Code, fallback string) *IngestDenial {
		if message == "" {
			message = fallback
		}
		return &IngestDenial{HTTPStatus: httpStatus, Code: code, GRPCCode: grpcCode, Message: message}
	}

	switch resp.GetRejectionReason() {
	case commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_INVALID_KEY:
		return denial(404, "INVALID_STREAM_KEY", codes.NotFound, "invalid stream key")
	case commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_USER_INACTIVE:
		return denial(403, "ACCOUNT_INACTIVE", codes.PermissionDenied, "account is inactive")
	case commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_PULL_MODE:
		return denial(409, "PULL_MODE_STREAM", codes.FailedPrecondition, "pull streams do not accept push ingest")
	case commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_TENANT_SUSPENDED:
		return denial(403, "ACCOUNT_SUSPENDED", codes.PermissionDenied, "account suspended")
	case commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_BALANCE_NEGATIVE:
		return denial(402, "PAYMENT_REQUIRED", codes.FailedPrecondition, "payment required")
	case commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_NOT_ENTITLED:
		return denial(403, "CLUSTER_NOT_ENTITLED", codes.PermissionDenied, "tenant not entitled to this cluster")
	case commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_CLASS_MISMATCH:
		return denial(403, "CLUSTER_CLASS_MISMATCH", codes.PermissionDenied, "cluster class not permitted for this tenant")
	case commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_PROTOCOL_NOT_SUPPORTED:
		return denial(415, "PROTOCOL_NOT_SUPPORTED", codes.InvalidArgument, "ingest protocol not supported")
	case commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_UNHEALTHY:
		return denial(503, "CLUSTER_UNHEALTHY", codes.Unavailable, "ingest cluster is unhealthy")
	case commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_DUPLICATE_INGEST:
		return denial(409, "DUPLICATE_INGEST", codes.AlreadyExists, "stream is already ingesting elsewhere")
	default:
		return denial(403, "INGEST_DENIED", codes.PermissionDenied, "ingest denied")
	}
}

// nodeOutputsFromState reads a node's advertised outputs from the live
// registry, never from the database.
func nodeOutputsFromState(nodeID string) map[string]any {
	ns := state.DefaultManager().GetNodeState(nodeID)
	if ns == nil {
		return nil
	}
	return ns.Outputs
}

// nodeClusterID returns the virtual media cluster a node is registered in, or
// "" when it is unattributed.
func nodeClusterID(nodeID string) string {
	if ns := state.DefaultManager().GetNodeState(nodeID); ns != nil {
		return strings.TrimSpace(ns.ClusterID)
	}
	return ""
}

type ingestPublicAddress struct {
	scheme    string
	authority string
	hostname  string
}

func parseIngestPublicAddress(raw string) (ingestPublicAddress, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.Hostname() == "" || u.User != nil ||
		(u.Scheme != "http" && u.Scheme != "https") {
		return ingestPublicAddress{}, false
	}
	return ingestPublicAddress{
		scheme:    u.Scheme,
		authority: u.Host,
		hostname:  u.Hostname(),
	}, true
}

// ingestAddressForNode prefers lifecycle BaseURL because it retains the external scheme and port;
// output templates are the fallback for nodes that do not advertise one.
func ingestAddressForNode(nodeID string, outputs map[string]any) (ingestPublicAddress, bool) {
	if ns := state.DefaultManager().GetNodeState(nodeID); ns != nil {
		if address, ok := parseIngestPublicAddress(ns.BaseURL); ok {
			return address, true
		}
	}

	for _, keys := range [][]string{
		{"HLS", "HLS (TS)"},
		{"HTTP", "MP4", "MP4 progressive"},
		{"CMAF", "HLS (CMAF)"},
		{"HDS", "Flash Dynamic (HDS)"},
	} {
		raw, ok := findOutputRaw(outputs, keys...)
		if !ok {
			continue
		}
		var candidate string
		switch value := raw.(type) {
		case string:
			candidate = value
		case []any:
			if len(value) > 0 {
				candidateValue, isString := value[0].(string)
				if !isString {
					continue
				}
				candidate = candidateValue
			}
		}
		candidate = strings.Trim(candidate, "[]\"")
		if address, ok := parseIngestPublicAddress(candidate); ok {
			return address, true
		}
	}
	return ingestPublicAddress{}, false
}

// buildIngestEndpoint renders the per-protocol ingest URLs for one node. WHIP
// keeps the advertised HTTP scheme/authority; RTMP and SRT use fleet-wide ports
// because node lifecycle does not advertise protocol-specific ingest ports.
func buildIngestEndpoint(nodeID string, address ingestPublicAddress, streamKey, region, clusterID string, loadScore float64) *sharedpb.IngestEndpoint {
	if address.scheme == "" || address.authority == "" || address.hostname == "" || strings.TrimSpace(streamKey) == "" {
		return nil
	}
	escapedKey := url.PathEscape(streamKey)

	baseURL := address.scheme + "://" + address.authority
	whip := baseURL + "/webrtc/" + escapedKey
	rtmp := "rtmp://" + net.JoinHostPort(address.hostname, strconv.Itoa(ingestRTMPPort())) + "/live/" + escapedKey
	srt := "srt://" + net.JoinHostPort(address.hostname, strconv.Itoa(ingestSRTPort())) + "?streamid=" + url.QueryEscape(streamKey)

	endpoint := &sharedpb.IngestEndpoint{
		NodeId:    nodeID,
		BaseUrl:   baseURL,
		WhipUrl:   &whip,
		RtmpUrl:   &rtmp,
		SrtUrl:    &srt,
		Kind:      sharedpb.IngestEndpointKind_INGEST_ENDPOINT_KIND_NODE_SPECIFIC,
		ClusterId: clusterID,
	}
	if region = strings.TrimSpace(region); region != "" {
		endpoint.Region = &region
	}
	endpoint.LoadScore = &loadScore

	return endpoint
}

// ResolveIngestEndpoints picks the ingest-capable nodes best suited to a
// publisher and renders their per-protocol URLs, best first.
//
// Selection is scoped two ways: to nodes advertising the ingest capability,
// and to the virtual media clusters in the peer envelope Commodore returned.
// That envelope already carries Commodore's plan-class and health verdict, so
// this resolver re-derives neither. NodeState.TenantID is descriptive ownership
// metadata, not authorization, so candidates are ranked from the shared pool
// and filtered by their own authenticated cluster; a missing tenant is refused
// before any shared-pool selection.
func ResolveIngestEndpoints(
	ctx context.Context,
	deps *IngestDependencies,
	streamCtx *commodorepb.ResolveStreamContextResponse,
	streamKey string,
) (*sharedpb.IngestEndpointResponse, error) {
	if deps == nil || deps.LB == nil {
		return nil, fmt.Errorf("load balancer not available")
	}
	if streamCtx == nil {
		return nil, fmt.Errorf("stream context required")
	}
	tenantID := strings.TrimSpace(streamCtx.GetTenantId())
	if tenantID == "" {
		return nil, fmt.Errorf("stream context has no tenant; refusing to resolve ingest across the shared node pool")
	}

	lbctx := context.WithValue(ctx, ctxkeys.KeyCapability, "ingest")

	// Rank every eligible node (maxNodes<=0 disables the balancer's cutoff)
	// and stop once enough usable ones are found. Usability — a resolvable
	// public host — is not part of the balancer's ranking, so any fixed
	// pre-filter cutoff would report "no ingest capacity" whenever that many
	// top-ranked nodes lacked usable outputs while a lower-ranked node would
	// have served.
	nodes, err := deps.LB.GetTopNodesWithScores(lbctx, "", deps.GeoLat, deps.GeoLon, make(map[string]int), "", 0, false)
	if err != nil {
		return nil, fmt.Errorf("no suitable ingest nodes available: %w", err)
	}

	var endpoints []*sharedpb.IngestEndpoint
	for _, node := range nodes {
		if len(endpoints) >= maxIngestEndpoints {
			break
		}

		// In-memory outputs only. GetNodeOutputs falls back to a per-node
		// database read on a background context, which on this unauthenticated
		// path would turn one request into an unbounded, uncancellable query
		// fan-out. A node whose outputs are not in the live registry is not a
		// node we should be handing a publisher to anyway.
		// The node's own authenticated cluster: an unattributed node cannot be
		// authorized, and the predicate below fails closed on an empty cluster.
		candidateClusterID := nodeClusterID(node.NodeID)
		if !ingestClusterAllowed(streamCtx, candidateClusterID) {
			continue
		}

		outputs := nodeOutputsFromState(node.NodeID)
		address, ok := ingestAddressForNode(node.NodeID, outputs)
		if !ok {
			continue
		}

		endpoint := buildIngestEndpoint(
			node.NodeID, address, streamKey, node.LocationName,
			// One Foghorn pools nodes from many virtual clusters, so the
			// endpoint carries the cluster the node actually belongs to —
			// SDKs pin on this field.
			candidateClusterID,
			float64(node.Score),
		)
		if endpoint == nil {
			continue
		}
		endpoints = append(endpoints, endpoint)
	}

	if len(endpoints) == 0 {
		return nil, fmt.Errorf("no ingest-capable nodes available")
	}

	return &sharedpb.IngestEndpointResponse{
		Primary:   endpoints[0],
		Fallbacks: endpoints[1:],
		Metadata: &sharedpb.IngestMetadata{
			StreamId:         streamCtx.GetStreamId(),
			StreamKey:        streamKey,
			TenantId:         streamCtx.GetTenantId(),
			RecordingEnabled: streamCtx.GetIsRecordingEnabled(),
		},
	}, nil
}

// ingestClusterAllowed reports whether a candidate node's virtual media
// cluster may receive this publish.
//
// The bound is the cluster-peer envelope on the resolve response — the same
// envelope playback authorizes cross-cluster candidates against. It is
// Quartermaster's cluster↔tenant grant already narrowed by the tenant's plan
// classes and by cluster health, so entitlement is never re-derived here; the
// generic ClusterAccessibleForTenant predicate reads raw grants and would admit
// a cluster the plan excludes or that has degraded.
//
// A live claim narrows it to one cluster: a reconnect must return to the
// cluster already ingesting, or PUSH_REWRITE refuses it as DUPLICATE_INGEST.
// origin_cluster_id cannot serve as that fence — it falls back to the tenant's
// route and is always populated.
func ingestClusterAllowed(streamCtx *commodorepb.ResolveStreamContextResponse, candidateClusterID string) bool {
	if streamCtx == nil {
		return false
	}
	candidateClusterID = strings.TrimSpace(candidateClusterID)
	if candidateClusterID == "" {
		return false
	}
	if pinned := strings.TrimSpace(streamCtx.GetActiveIngestClusterId()); pinned != "" {
		return candidateClusterID == pinned
	}
	for _, peer := range streamCtx.GetClusterPeers() {
		if strings.TrimSpace(peer.GetClusterId()) != candidateClusterID {
			continue
		}
		// Commodore has already dropped unhealthy peers from this envelope, so
		// this is defence in depth rather than the primary check — but an
		// unhealthy cluster that did reach here would be a publisher sent
		// somewhere PUSH_REWRITE refuses. Unknown health fails closed.
		return strings.EqualFold(strings.TrimSpace(peer.GetHealthStatus()), "healthy")
	}
	return false
}

// LocallyPublishedStream is a stream whose publisher is connected to a node
// this Foghorn owns, with the placement claim that publisher's session holds.
type LocallyPublishedStream struct {
	TenantID     string
	InternalName string
	OwnerNodeID  string
	// ClusterID and ClaimToken come from the durable ingest session, so renewal
	// re-asserts the claim the session was admitted under and owns, rather than
	// the node's present registration.
	ClusterID  string
	ClaimToken string
}

// sessionClaimKey identifies a claim by the exact publisher session that holds
// it: the stream, the node it landed on, the connection (Mist trigger UUID), and
// the durable generation that connection minted.
//
// Renewal matches all of them. The registry is a projection and can lag or
// drift, and a looser key lets a stale entry re-assert some other session's
// claim — by stream alone, any owner would match; adding only the node still
// matches a superseded session on that same node.
func sessionClaimKey(tenantID, internalName, nodeID, triggerUUID, generation string) string {
	return tenantID + "|" + internalName + "|" + nodeID + "|" + triggerUUID + "|" + generation
}

// activeIngestSessionClaims maps each open ingest session to the placement claim
// it holds, keyed by that session's full identity. Cell-scoped administrative scan
// over Foghorn's own schema (the tenant-filter rule's documented exception): the
// renewal worker reconciles every tenant's placements in one pass, and each row
// carries its tenant_id, which scopes every downstream renewal RPC.
func activeIngestSessionClaims(ctx context.Context) (map[string]LocallyPublishedStream, error) {
	if db == nil {
		return nil, fmt.Errorf("active ingest session claims require the durable session store")
	}
	rows, err := db.QueryContext(ctx, `
		SELECT tenant_id::text, stream_internal_name, node_id, COALESCE(ingest_cluster_id, ''), start_trigger_uuid, id::text
		FROM foghorn.ingest_sessions
		WHERE ended_at IS NULL
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	claims := make(map[string]LocallyPublishedStream)
	for rows.Next() {
		var claim LocallyPublishedStream
		var generation string
		if err := rows.Scan(&claim.TenantID, &claim.InternalName, &claim.OwnerNodeID, &claim.ClusterID, &claim.ClaimToken, &generation); err != nil {
			return nil, err
		}
		claims[sessionClaimKey(claim.TenantID, claim.InternalName, claim.OwnerNodeID, claim.ClaimToken, generation)] = claim
	}
	return claims, rows.Err()
}

// LocallyPublishedStreams lists the streams currently being published to nodes
// this Foghorn owns.
//
// The registry's per-cluster Location is source-active between an accepted
// PUSH_REWRITE and its PUSH_INPUT_CLOSE — the same fact admission uses to
// reject a duplicate publisher — so it is the platform's answer to "is someone
// pushing this right now". Ownership is confirmed against local node state:
// a Location's cluster may be a federated peer's, whose publishers are that
// peer's to account for, and whose node this Foghorn has no state for.
//
// A source-active Location alone is not enough. Locations are deliberately
// non-evictable while source-active and survive in Redis without a TTL, and a
// node record outlives a disconnect (unhealthy, then stale). Renewing from
// those would hold placement for a publisher that has crashed, which no other
// cluster could then take. The owner must therefore still be a node this
// Foghorn considers healthy and non-stale — the same predicate every other
// serve/placement path applies.
//
// The claim is read from the open ingest session rather than re-resolved from
// the node: the session records the cluster it was admitted into and the
// publisher connection that owns the claim, so a node reassigned mid-session
// still has its original claim re-asserted rather than a new one asserted
// beside it.
func LocallyPublishedStreams(ctx context.Context) ([]LocallyPublishedStream, error) {
	registry := StreamRegistryInstance
	if registry == nil {
		return nil, nil
	}
	claims, err := activeIngestSessionClaims(ctx)
	if err != nil {
		// Without the sessions there is no claim to re-assert: renewing under a
		// re-resolved cluster is what this exists to avoid. Claims lapse on the
		// lease clock and the next tick recovers.
		return nil, err
	}
	var live []LocallyPublishedStream
	for _, entry := range registry.Snapshot() {
		for _, loc := range entry.Locations {
			if !loc.SourceActive || loc.OwnerNodeID == "" {
				continue
			}
			owner := state.DefaultManager().GetNodeState(loc.OwnerNodeID)
			if owner == nil || !owner.IsHealthy || owner.IsStale {
				continue
			}
			// Renewal is sharded on the owner node's control connection, which
			// this process either holds or does not. The stream registry is
			// synchronized across Foghorn replicas, so every replica sees every
			// publisher; without this, each would renew the whole fleet and the
			// per-replica budget would bound nothing. A node's control stream
			// lives on exactly one replica, so this partitions the fleet with no
			// gap and no overlap, and adding a replica moves the connections
			// that land on it along with their renewal work.
			if _, connected := currentNodeSession(loc.OwnerNodeID); !connected {
				continue
			}
			// Matched on the projection's full session identity — node,
			// connection, and generation — not just the stream. The registry can
			// drift, and a looser match lets a stale entry keep an unrelated
			// session's claim alive for as long as its node stays healthy.
			claim, ok := claims[sessionClaimKey(
				entry.TenantID, entry.InternalName,
				loc.OwnerNodeID, loc.SourceTriggerUUID, loc.SourceGeneration,
			)]
			if !ok || claim.ClusterID == "" || claim.ClaimToken == "" {
				// A source-active projection without its matching open session has
				// no authoritative cluster or claim token to renew.
				continue
			}
			live = append(live, LocallyPublishedStream{
				TenantID:     entry.TenantID,
				InternalName: entry.InternalName,
				OwnerNodeID:  loc.OwnerNodeID,
				ClusterID:    claim.ClusterID,
				ClaimToken:   claim.ClaimToken,
			})
		}
	}
	return live, nil
}

// StripSensitiveIngestMetadata removes the fields an anonymous HTTP caller must
// not receive back. The stream key is the credential they already presented, so
// echoing it only widens where it can be logged; tenant identity remains internal.
// The authenticated gRPC path applies its own owner-aware filtering.
func StripSensitiveIngestMetadata(resp *sharedpb.IngestEndpointResponse) {
	if resp == nil || resp.Metadata == nil {
		return
	}
	resp.Metadata.StreamKey = ""
	resp.Metadata.TenantId = ""
}
