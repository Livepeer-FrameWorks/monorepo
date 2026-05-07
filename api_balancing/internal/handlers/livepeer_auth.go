package handlers

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"frameworks/api_balancing/internal/federation"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"github.com/gin-gonic/gin"
)

// livepeerAuthRequest is the body sent by go-livepeer's auth webhook.
// go-livepeer POSTs {"url": "<incomingRequestURL>"} on the first segment of a new session.
type livepeerAuthRequest struct {
	URL string `json:"url"`
}

// livepeerAuthResponse is what go-livepeer expects back.
// ManifestID is required — an empty value or non-200 status rejects the stream.
// TenantID and StreamID propagate FrameWorks tenant context into go-livepeer's
// authWebhookResponse → core.StreamParameters, so the gateway can stamp
// per-session telemetry with the right tenant. Empty values are tolerated by
// the gateway during rollout.
type livepeerAuthResponse struct {
	ManifestID string `json:"manifestID"`
	TenantID   string `json:"tenantID,omitempty"`
	StreamID   string `json:"streamID,omitempty"`
}

// LivepeerAuthContext is the resolved tenant/stream context for an authorized
// livepeer-gateway transcode request. Authorize returns this on success and
// nil on rejection. The fields here flow into the auth webhook response and,
// from there, into go-livepeer's StreamParameters via createRTMPStreamIDHandler.
type LivepeerAuthContext struct {
	TenantID     string
	StreamID     string
	InternalName string
}

// HandleLivepeerAuth handles the auth webhook from go-livepeer gateways.
// It validates that the manifestID in the push URL corresponds to an active
// stream owned by a real tenant — refuses random unauthorised transcode requests.
//
// URL format: http://gateway:8935/live/<manifestID>/<segNum>.ts
func HandleLivepeerAuth(c *gin.Context) {
	var req livepeerAuthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		logger.WithError(err).Warn("livepeer auth: invalid request body")
		incLivepeerAuthRejected("invalid_request")
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	manifestID := extractManifestID(req.URL)
	if manifestID == "" {
		logger.WithField("url", req.URL).Warn("livepeer auth: could not extract manifestID from URL")
		incLivepeerAuthRejected("invalid_request")
		c.JSON(http.StatusForbidden, gin.H{"error": "invalid stream URL"})
		return
	}

	resolver := defaultLivepeerAuthResolver()
	authCtx, reason := resolver.Authorize(c.Request.Context(), manifestID)
	if authCtx == nil {
		logger.WithFields(logging.Fields{
			"manifest_id": manifestID,
			"reason":      reason,
		}).Warn("livepeer auth: unknown stream rejected")
		incLivepeerAuthRejected(reason)
		c.JSON(http.StatusForbidden, gin.H{"error": "unknown stream"})
		return
	}

	logger.WithFields(logging.Fields{
		"manifest_id": manifestID,
		"tenant_id":   authCtx.TenantID,
		"stream_id":   authCtx.StreamID,
	}).Debug("livepeer auth: stream authorized")
	c.JSON(http.StatusOK, livepeerAuthResponse{
		ManifestID: manifestID,
		TenantID:   authCtx.TenantID,
		StreamID:   authCtx.StreamID,
	})
}

// extractManifestID parses the manifestID from a go-livepeer push URL.
// Expected path: /live/<manifestID>/<segNum>.ts (or just /live/<manifestID>/...)
func extractManifestID(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}

	// Path: /live/<manifestID>/0.ts
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "live" {
		return ""
	}
	return parts[1]
}

// LivepeerAuthRejection reasons reported via metrics + structured log.
const (
	authRejectStreamNotFound       = "stream_not_found"
	authRejectStreamNotLive        = "stream_not_live"
	authRejectPeerContextMissing   = "peer_context_missing"
	authRejectPeerUnreachable      = "peer_unreachable"
	authRejectInvalidRequest       = "invalid_request"
	authRejectCommodoreUnreachable = "commodore_unreachable"
)

func incLivepeerAuthRejected(reason string) {
	if metrics == nil || metrics.LivepeerAuthRejected == nil {
		return
	}
	metrics.LivepeerAuthRejected.WithLabelValues(reason).Inc()
}

// commodoreInternalNameResolver is the minimum surface the auth resolver needs
// from Commodore — narrow so tests can substitute an in-memory stub.
type commodoreInternalNameResolver interface {
	ResolveInternalName(ctx context.Context, internalName string) (*pb.ResolveInternalNameResponse, error)
}

// federationStreamQuerier is the minimum federation surface the auth resolver
// needs to confirm a stream is live on a peer cluster.
type federationStreamQuerier interface {
	QueryStream(ctx context.Context, clusterID, addr string, req *pb.QueryStreamRequest) (*pb.QueryStreamResponse, error)
}

// LivepeerAuthResolver answers "is this manifestID a real, live stream owned by
// a real tenant" through a four-step chain: local in-memory state, positive-result
// LRU, Commodore manifest resolution, federation peer fan-out. On success it
// returns the resolved tenant/stream context so callers can propagate tenant
// attribution into per-session telemetry.
type LivepeerAuthResolver struct {
	LocalCluster  string
	StreamLookup  func(manifestID string) *LivepeerAuthContext
	Commodore     commodoreInternalNameResolver
	Federation    federationStreamQuerier
	PeerAddrs     peerAddrResolver // shared with the rest of the handlers package
	PositiveCache *authPositiveCache
	PeerQueryWait time.Duration
	Logger        logging.Logger
}

// defaultLivepeerAuthResolver constructs the resolver from package-level state
// configured at handler Init time.
func defaultLivepeerAuthResolver() *LivepeerAuthResolver {
	return &LivepeerAuthResolver{
		LocalCluster: clusterID,
		StreamLookup: func(manifestID string) *LivepeerAuthContext {
			s := state.DefaultManager().GetStreamState(manifestID)
			if s == nil {
				return nil
			}
			return &LivepeerAuthContext{
				TenantID:     s.TenantID,
				StreamID:     s.StreamID,
				InternalName: s.InternalName,
			}
		},
		Commodore:     commodoreAdapter{client: commodoreClient},
		Federation:    federationAdapter{client: federationClient},
		PeerAddrs:     peerManager,
		PositiveCache: livepeerAuthPositiveCache,
		PeerQueryWait: 2 * time.Second,
		Logger:        logger,
	}
}

// Authorize runs the resolution chain. Returns (ctx, "") on success with the
// resolved tenant/stream context; (nil, reason) with one of the constants above
// when the stream cannot be confirmed.
func (r *LivepeerAuthResolver) Authorize(ctx context.Context, manifestID string) (*LivepeerAuthContext, string) {
	// 1. Local in-memory state. Pub/sub keeps this in sync within a Foghorn pool
	// when EnableRedisSync is on, so this hit covers same-instance and same-pool
	// streams without a network round trip.
	if r.StreamLookup != nil {
		if c := r.StreamLookup(manifestID); c != nil {
			return c, ""
		}
	}

	// 2. Positive-result cache. Avoids repeated Commodore + peer fan-out for the
	// burst of segments at session startup. Caches the full auth context, not
	// just a boolean, so the cached path still produces tenant attribution.
	if r.PositiveCache != nil {
		if c := r.PositiveCache.get(manifestID); c != nil {
			return c, ""
		}
	}

	// 3. Commodore: confirm manifest belongs to a real tenant + get peer context.
	if r.Commodore == nil {
		return nil, authRejectCommodoreUnreachable
	}
	resp, err := r.Commodore.ResolveInternalName(ctx, manifestID)
	if err != nil {
		if r.Logger != nil {
			r.Logger.WithError(err).WithField("manifest_id", manifestID).Warn("livepeer auth: ResolveInternalName failed")
		}
		return nil, authRejectCommodoreUnreachable
	}
	if resp == nil || strings.TrimSpace(resp.GetTenantId()) == "" {
		return nil, authRejectStreamNotFound
	}

	authCtx := &LivepeerAuthContext{
		TenantID:     resp.GetTenantId(),
		StreamID:     resp.GetStreamId(),
		InternalName: manifestID,
	}

	// 4. Federation peer fan-out. The stream may be live on a peer instance or
	// peer cluster that this Foghorn doesn't directly serve.
	peers := resp.GetClusterPeers()
	if len(peers) == 0 {
		return nil, authRejectPeerContextMissing
	}
	if r.Federation == nil || r.PeerAddrs == nil {
		return nil, authRejectPeerContextMissing
	}

	queryCtx := ctx
	if r.PeerQueryWait > 0 {
		var cancel context.CancelFunc
		queryCtx, cancel = context.WithTimeout(ctx, r.PeerQueryWait)
		defer cancel()
	}

	// anyAnswered flips only when a peer responds without an RPC error. A peer
	// addr that never produces a clean response counts as unreachable, not as a
	// vote of "stream is not live".
	anyAnswered := false
	for _, peer := range peers {
		peerCluster := strings.TrimSpace(peer.GetClusterId())
		if peerCluster == "" || peerCluster == r.LocalCluster {
			continue
		}
		addr := r.PeerAddrs.GetPeerAddr(peerCluster)
		if addr == "" {
			continue
		}
		peerResp, qerr := r.Federation.QueryStream(queryCtx, peerCluster, addr, &pb.QueryStreamRequest{
			StreamName:        manifestID,
			RequestingCluster: r.LocalCluster,
			TenantId:          authCtx.TenantID,
			IsSourceSelection: true,
		})
		if qerr != nil {
			if r.Logger != nil {
				r.Logger.WithError(qerr).WithFields(logging.Fields{
					"manifest_id": manifestID,
					"peer":        peerCluster,
				}).Debug("livepeer auth: peer QueryStream failed")
			}
			continue
		}
		anyAnswered = true
		if peerResp != nil && len(peerResp.GetCandidates()) > 0 {
			if r.PositiveCache != nil {
				r.PositiveCache.add(manifestID, authCtx)
			}
			return authCtx, ""
		}
	}

	if !anyAnswered {
		return nil, authRejectPeerUnreachable
	}
	return nil, authRejectStreamNotLive
}

// authPositiveCache holds short-lived "this manifest is authorised" entries to
// avoid Commodore + peer fan-out on every segment at session startup. The cache
// stores the full LivepeerAuthContext so cache hits still propagate tenant
// attribution to telemetry consumers.
type authPositiveCache struct {
	mu      sync.Mutex
	entries map[string]authCacheEntry
	ttl     time.Duration
}

type authCacheEntry struct {
	ctx *LivepeerAuthContext
	exp time.Time
}

func newAuthPositiveCache(ttl time.Duration) *authPositiveCache {
	return &authPositiveCache{
		entries: map[string]authCacheEntry{},
		ttl:     ttl,
	}
}

func (c *authPositiveCache) get(manifestID string) *LivepeerAuthContext {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[manifestID]
	if !ok {
		return nil
	}
	if time.Now().After(e.exp) {
		delete(c.entries, manifestID)
		return nil
	}
	return e.ctx
}

func (c *authPositiveCache) add(manifestID string, authCtx *LivepeerAuthContext) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[manifestID] = authCacheEntry{ctx: authCtx, exp: time.Now().Add(c.ttl)}
}

// livepeerAuthPositiveCache is the package-level positive cache used by the
// default resolver. 15s matches the typical livepeer-gateway segment cadence
// for an active session — a stream that just authorised will see many segments
// arrive in that window without needing a fresh peer fan-out.
var livepeerAuthPositiveCache = newAuthPositiveCache(15 * time.Second)

// commodoreAdapter wraps the package-level *commodore.GRPCClient so the resolver
// can depend on a narrow interface for testability.
type commodoreAdapter struct {
	client *commodore.GRPCClient
}

func (a commodoreAdapter) ResolveInternalName(ctx context.Context, internalName string) (*pb.ResolveInternalNameResponse, error) {
	if a.client == nil {
		return nil, errCommodoreUnavailable
	}
	return a.client.ResolveInternalName(ctx, internalName)
}

type federationAdapter struct {
	client *federation.FederationClient
}

func (a federationAdapter) QueryStream(ctx context.Context, clusterID, addr string, req *pb.QueryStreamRequest) (*pb.QueryStreamResponse, error) {
	if a.client == nil {
		return nil, errFederationUnavailable
	}
	return a.client.QueryStream(ctx, clusterID, addr, req)
}

var (
	errCommodoreUnavailable  = newAuthError("commodore client unavailable")
	errFederationUnavailable = newAuthError("federation client unavailable")
)

type authError string

func newAuthError(msg string) error { return authError(msg) }
func (e authError) Error() string   { return string(e) }
