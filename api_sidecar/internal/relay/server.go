// Package relay is the read-through artifact relay that sits between Mist
// and S3. Mist sees a stable seekable HTTP source for every playable
// artifact (and for safe-wrapper processing input). Behind that URL the
// relay materializes bytes from local disk (warm), from S3 into disk (cold,
// healthy), or from S3 straight to socket (cold, pressured).
//
// Resolution metadata is fetched from Foghorn via
// control.RequestRelayResolve (presigned media URL, expected size,
// .dtsh sidecar URLs) and TTL-cached in memory; long playback sessions
// refresh mid-stream when the first presigned URL expires.
//
// Lease protection is installed at STREAM_SOURCE by handlers, not
// here: acquireSourceLeaseFromRelayURL reserves the on-disk paths the
// relay may write to (canonical media file + per-asset .blocks
// directory + .dtsh/.gop sidecars) so a fetch-in-progress cannot be
// evicted by cleanup.
package relay

import (
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"frameworks/api_sidecar/internal/admission"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// Resolver knows how to ask Foghorn for the durable source coordinates of an
// asset. Implementations call out over the control stream; tests inject a
// fake.
type Resolver interface {
	Resolve(ctx ResolveContext) (*ResolveResult, error)
}

// FreezeHandoff accepts a freshly-written .dtsh sidecar and schedules its
// upload to S3 via the existing freeze pipeline. Implementations live in the
// handlers package (which owns FreezePermission flow).
type FreezeHandoff interface {
	OnLocalDtshGenerated(assetKind, assetHash, localPath string)
}

// HeatToucher records a viewer-side access for a local path. The relay
// calls Touch on each warm block-cache read so the cleanup/eviction
// path can rank by real playback heat instead of just file mtime — a
// .blocks dir whose blocks were last actually read 5 minutes ago and
// is still being polled by viewers must outlive a .blocks dir whose
// blocks have a newer mtime (from a single cold fill) but zero
// subsequent reads. Implemented by leases.HeatTracker.
type HeatToucher interface {
	Touch(path string)
}

// Server is the relay HTTP front-end. Construct one with New, mount it on the
// shared Gin engine via MountRoutes, and pass it into the rest of the
// sidecar's wiring.
type Server struct {
	basePath   string
	admitter   admission.Admitter
	resolver   Resolver
	freeze     FreezeHandoff
	heat       HeatToucher
	logger     logging.Logger
	httpc      *http.Client
	cache      *resolveCache
	blockSize  int64
	coldFetch  *blockFetchCoalescer
	nodeID     string
	authorizer RelayPullAuthorizer
	// trustedCIDRs are RemoteAddr ranges that bypass the authorize gate like
	// loopback does (still AND-gated by no proxy-forward markers). For the
	// local Mist→Helmsman hop where Mist dials a non-loopback service address
	// (docker: helmsman:18007). Empty in production/native — loopback only.
	trustedCIDRs []*net.IPNet
	// authzCache memoizes recent ALLOW decisions per (grant_id, path) so a
	// multi-block pull session makes one authorize round-trip, not one per
	// block. Denials are not cached (transient Foghorn errors must retry).
	authzMu    sync.Mutex
	authzCache map[string]time.Time
	// defrost coalesces cold S3 read-through bytes per asset into
	// ACTION_CACHED lifecycle events for cold-read amplification analytics.
	defrost *defrostAggregator
}

// Options configures the relay. basePath is the Helmsman storage root used
// for canonical artifact paths (basePath/vod/<hash>.<ext>, etc.).
type Options struct {
	BasePath string
	Admitter admission.Admitter
	Resolver Resolver
	Freeze   FreezeHandoff
	Heat     HeatToucher
	Logger   logging.Logger
	// HTTPClient is used for outbound fetches of presigned URLs.
	// Defaults to http.DefaultClient when nil; tests inject a recording
	// transport.
	HTTPClient *http.Client
	// BlockSize sets the per-asset block-cache granularity in bytes.
	// Zero uses DefaultBlockSize (32 MiB). Tests use a smaller value
	// so fixture bodies span multiple blocks.
	BlockSize int64
	// NodeID is this Helmsman's own node id. Carried for logging/diagnostics.
	NodeID string
	// Authorizer validates inbound peer-relay pulls online against Foghorn.
	// Defaults to a control-stream-backed authorizer when nil; tests inject
	// a fake. Non-loopback / non-trusted-CIDR requests are denied when the
	// authorizer denies, errors, or times out (fail closed).
	Authorizer RelayPullAuthorizer
	// RelayTrustedCIDR is a comma-separated list of CIDRs whose RemoteAddr
	// bypasses the authorize gate (like loopback), for the local
	// Mist→Helmsman hop when Mist dials a non-loopback service address
	// (docker: helmsman:18007). The bypass is still AND-gated by absence of
	// proxy-forward markers, so Caddy-forwarded peer traffic on the same
	// subnet still gets authorized. Empty = loopback-only (the
	// production/native default). NOT for peer-node traffic.
	RelayTrustedCIDR string
}

// New constructs a relay server. nil-tolerant for optional fields:
// Freeze and Logger may be nil for tests; Admitter and Resolver and
// BasePath must be set.
func New(opts Options) *Server {
	c := opts.HTTPClient
	if c == nil {
		c = http.DefaultClient
	}
	blockSize := opts.BlockSize
	if blockSize <= 0 {
		blockSize = DefaultBlockSize
	}
	authorizer := opts.Authorizer
	if authorizer == nil {
		authorizer = NewControlAuthorizer()
	}
	return &Server{
		basePath:     opts.BasePath,
		admitter:     opts.Admitter,
		resolver:     opts.Resolver,
		freeze:       opts.Freeze,
		heat:         opts.Heat,
		logger:       opts.Logger,
		httpc:        c,
		cache:        newResolveCache(),
		blockSize:    blockSize,
		coldFetch:    newBlockFetchCoalescer(),
		nodeID:       strings.TrimSpace(opts.NodeID),
		authorizer:   authorizer,
		trustedCIDRs: parseTrustedCIDRs(opts.RelayTrustedCIDR, opts.Logger),
		authzCache:   make(map[string]time.Time),
		defrost:      newDefrostAggregator(),
	}
}

// parseTrustedCIDRs parses a comma-separated CIDR list. Unparseable entries
// are logged and skipped rather than failing construction — a bad entry must
// not silently widen trust, but it also must not take the relay down.
func parseTrustedCIDRs(raw string, logger logging.Logger) []*net.IPNet {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var nets []*net.IPNet
	for part := range strings.SplitSeq(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		_, n, err := net.ParseCIDR(part)
		if err != nil {
			if logger != nil {
				logger.WithError(err).WithField("cidr", part).Warn("relay: ignoring invalid HELMSMAN_RELAY_TRUSTED_CIDR entry")
			}
			continue
		}
		nets = append(nets, n)
	}
	return nets
}

// inTrustedCIDR reports whether remoteAddr's IP falls in any configured
// trusted CIDR. Operates on the literal socket peer (RemoteAddr), never a
// forwarded header.
func (s *Server) inTrustedCIDR(remoteAddr string) bool {
	if len(s.trustedCIDRs) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil {
		return false
	}
	for _, n := range s.trustedCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// MountRoutes registers the /internal/artifact/* route group on the given
// Gin engine. Localhost requests (Mist on the same box) bypass auth so
// the normal cold-fetch path is unchanged. Non-localhost requests must
// present an opaque peer-relay grant id (Authorization: Bearer) that
// Foghorn confirms via AuthorizeRelayPull — these come from peer edges
// (same or other cluster) reading hot-but-unsynced bytes from this node.
//
// Bypass uses c.Request.RemoteAddr only, never X-Forwarded-For or any
// other header — Caddy forwards remote traffic to the same loopback
// socket and trusting forwarded headers would defeat the auth gate.
func (s *Server) MountRoutes(r *gin.Engine) {
	g := r.Group("/internal/artifact", s.peerAuthMiddleware())

	// VOD: flat path (storage/vod/<hash>.<ext>). URL preserves the same
	// shape so Mist sees a stable seekable HTTP source with the right
	// extension for input dispatch.
	g.HEAD("/vod/:file", func(c *gin.Context) { s.serveFile(c, "vod") })
	g.GET("/vod/:file", func(c *gin.Context) { s.serveFile(c, "vod") })
	g.PUT("/vod/:file", func(c *gin.Context) { s.putSidecar(c, "vod") })
	g.PUT("/vod/:file/", func(c *gin.Context) { s.putSidecar(c, "vod") })

	// Clip: stream-nested (clip/<stream>/<hash>.<ext>). Foghorn always
	// emits this shape — the clip's source stream name is required to
	// build the path, so the wildcard always carries the stream/hash
	// split. Stream identity in the path (rather than ?s=) survives
	// Mist's input + ".dtsh" sidecar mutation.
	g.HEAD("/clip/*path", func(c *gin.Context) { s.serveClipRoute(c) })
	g.GET("/clip/*path", func(c *gin.Context) { s.serveClipRoute(c) })
	g.PUT("/clip/*path", func(c *gin.Context) { s.putClipRoute(c) })

	// Processing input. Same flat layout under storage/upload/.
	g.HEAD("/upload/:file", s.serveUpload)
	g.GET("/upload/:file", s.serveUpload)
	g.PUT("/upload/:file", func(c *gin.Context) { s.putSidecar(c, "upload") })
	g.PUT("/upload/:file/", func(c *gin.Context) { s.putSidecar(c, "upload") })
}

// peerAuthMiddleware gates /internal/artifact/*.
//
// Loopback callers (RemoteAddr 127.0.0.1 / ::1) and configured trusted-CIDR
// callers pass through ONLY when they carry no proxy-forwarding markers — Mist
// on the same box / the local docker hop never carries those; Caddy
// reverse-proxying remote (peer) traffic always does. The proxy-marker check is
// the critical second leg: Caddy's hop arrives over loopback/bridge, so a pure
// RemoteAddr check would let any external request through the edge FQDN bypass
// the gate.
//
// Everything else is a peer pull: it must present an opaque grant id
// (Authorization: Bearer) that Foghorn — the authority — confirms via
// AuthorizeRelayPull. The edge holds no signing key. Deny/error/timeout →
// 401 (fail closed). ALLOW is briefly cached per (grant_id, path) so a
// multi-block session authorizes once.
func (s *Server) peerAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if (isLoopbackRemoteAddr(c.Request.RemoteAddr) || s.inTrustedCIDR(c.Request.RemoteAddr)) && !hasProxyForwardMarker(c.Request.Header) {
			c.Next()
			return
		}
		// Peers are read-only. Writes (PUT — Mist's local externalWriter
		// sidecar/clip write-back) are valid ONLY on the loopback/trusted-CIDR
		// bypass above; a peer grant authorizes reads, never overwriting the
		// origin node's canonical bytes or .dtsh.
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.AbortWithStatus(http.StatusMethodNotAllowed)
			return
		}
		grantID := ""
		if t, ok := strings.CutPrefix(c.Request.Header.Get("Authorization"), "Bearer "); ok {
			grantID = strings.TrimSpace(t)
		}
		if grantID == "" {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		// Authorize against the escaped path: producers mint the grant's allowed
		// paths with url.PathEscape on the stream segment, so the comparison must
		// use the same encoding. c.Request.URL.Path is percent-decoded by
		// net/http and would not match a grant path whose stream name contains an
		// escapable character (the hash/ext segments are escape-free, so hash
		// extraction below is unaffected).
		reqPath := c.Request.URL.EscapedPath()
		if s.authzCached(grantID, reqPath) {
			c.Next()
			return
		}
		// hashFromFile strips .dtsh then the media ext, so media and sidecar
		// paths both yield the bare artifact hash the grant was minted with.
		artifactHash := hashFromFile(path.Base(reqPath))
		allowed, err := s.authorizer.AuthorizeRelayPull(c.Request.Context(), grantID, artifactHash, reqPath)
		if err != nil {
			if s.logger != nil {
				s.logger.WithError(err).WithField("remote_addr", c.Request.RemoteAddr).Debug("relay pull authorize failed; denying")
			}
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		if !allowed {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		s.authzStore(grantID, reqPath)
		c.Next()
	}
}

// authzCacheTTL bounds how long an ALLOW is trusted without re-asking Foghorn.
// Short so revocation/topology changes take effect quickly; long enough that a
// block-range pull session authorizes once.
const authzCacheTTL = 10 * time.Second

func authzCacheKey(grantID, reqPath string) string { return grantID + "|" + reqPath }

func (s *Server) authzCached(grantID, reqPath string) bool {
	key := authzCacheKey(grantID, reqPath)
	s.authzMu.Lock()
	defer s.authzMu.Unlock()
	exp, ok := s.authzCache[key]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.authzCache, key)
		return false
	}
	return true
}

// authzCacheSweepAt bounds the cache: each peer-relay session uses a fresh
// grant id whose ALLOW entry is never re-read after it expires, so without a
// sweep the map would leak one entry per session. When it crosses this size
// on insert we drop expired entries inline (no background goroutine needed —
// the relay Server has no start/stop lifecycle to hang one on).
const authzCacheSweepAt = 1024

func (s *Server) authzStore(grantID, reqPath string) {
	now := time.Now()
	s.authzMu.Lock()
	if len(s.authzCache) >= authzCacheSweepAt {
		for k, exp := range s.authzCache {
			if now.After(exp) {
				delete(s.authzCache, k)
			}
		}
		// Hard cap: if a burst of >cap distinct grants is still live inside the
		// TTL window the expired sweep frees nothing, so evict arbitrary entries
		// until under the bound. An evicted live session just re-authorizes
		// online on its next block (correctness-safe), keeping this exposed peer
		// endpoint's memory hard-bounded rather than soft-bounded.
		for k := range s.authzCache {
			if len(s.authzCache) < authzCacheSweepAt {
				break
			}
			delete(s.authzCache, k)
		}
	}
	s.authzCache[authzCacheKey(grantID, reqPath)] = now.Add(authzCacheTTL)
	s.authzMu.Unlock()
}

// hasProxyForwardMarker returns true when the request carries any of
// the headers Caddy (or any sane reverse proxy) sets on forwarded
// traffic. Used together with RemoteAddr=loopback to distinguish
// "Mist on the same box" (no markers) from "Caddy proxying a remote
// request to loopback" (always at least one marker).
func hasProxyForwardMarker(h http.Header) bool {
	for _, name := range []string{
		"X-Forwarded-For",
		"X-Forwarded-Proto",
		"X-Forwarded-Host",
		"X-Real-Ip",
		"Forwarded",
	} {
		if h.Get(name) != "" {
			return true
		}
	}
	return false
}

// isLoopbackRemoteAddr returns true only when remoteAddr is a literal
// loopback IP. Empty/parse-failure returns false (deny). Trusted
// proxy headers are NEVER consulted — Caddy forwards remote traffic on
// the same socket and trusting X-Forwarded-For would turn the bypass
// into an auth bypass.
func isLoopbackRemoteAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}
